// Copyright (C) 2017-2026 The Rune Authors
// SPDX-License-Identifier: GPL-3.0-or-later
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or (at
// your option) any later version.
//
// This program is distributed in the hope that it will be useful, but
// WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the GNU
// General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.

package workspaceshell

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/ideshell"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/processctx"
)

const testWidth = 200

// mockExecutor is a minimal workspaceapi.Executor for testing.
type mockExecutor struct {
	mu       sync.Mutex
	nextPid  workspaceapi.Pid
	starts   []workspaceapi.Cmd
	signals  []signalCall
	watchers map[workspaceapi.Pid]workspaceapi.ProcessWatcher
	startErr error
	// When true, SIGTERM also causes process exit in Signal.
	termExits bool
}

type signalCall struct {
	pid workspaceapi.Pid
	sig syscall.Signal
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		nextPid:  1,
		watchers: make(map[workspaceapi.Pid]workspaceapi.ProcessWatcher),
	}
}

func (m *mockExecutor) Start(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.startErr != nil {
		return 0, m.startErr
	}
	pid := m.nextPid
	m.nextPid++
	m.starts = append(m.starts, cmd)
	m.watchers[pid] = cmd.Watcher
	return pid, nil
}

func (m *mockExecutor) Signal(
	pid workspaceapi.Pid, sig syscall.Signal,
) error {
	m.mu.Lock()
	m.signals = append(m.signals, signalCall{pid: pid, sig: sig})
	w := m.watchers[pid]
	termExits := m.termExits
	m.mu.Unlock()
	// Simulate SIGKILL causing immediate process exit.
	if sig == syscall.SIGKILL && w != nil {
		w.WatchProcess() <- nil
	}
	if sig == syscall.SIGTERM && termExits && w != nil {
		w.WatchProcess() <- nil
	}
	return nil
}

func (m *mockExecutor) Close() error { return nil }

// exitProcess simulates a process exiting successfully.
func (m *mockExecutor) exitProcess(pid workspaceapi.Pid) {
	m.exitProcessWithError(pid, nil)
}

// exitProcessWithError simulates a process exiting with
// the given error.
func (m *mockExecutor) exitProcessWithError(
	pid workspaceapi.Pid, err error,
) {
	m.mu.Lock()
	w := m.watchers[pid]
	m.mu.Unlock()
	if w != nil {
		w.WatchProcess() <- err
	}
}

func collectText(
	t *testing.T,
	iter iterator.Iterator[component.Responsive],
) []string {
	t.Helper()
	ctx := context.Background()
	var lines []string
	for {
		item, ok := iter.Next(ctx)
		if !ok {
			break
		}
		h := item.Height(testWidth)
		if h <= 0 {
			continue
		}
		w := term.NewStringWriter(testWidth, h)
		item.Resize(testWidth, h)
		item.Draw(w)
		_ = w.Flush()
		lines = append(lines, w.String())
	}
	require.NoError(t, iter.Err())
	return lines
}

func collectRenderedText(
	t *testing.T,
	iter iterator.Iterator[component.Responsive],
) string {
	t.Helper()
	return strings.Join(collectText(t, iter), "\n")
}

func TestStartTracksProcess(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/gopls",
		Args: []string{"-rpc.trace"},
		Dir:  "/home/user",
	})
	require.NoError(t, err)
	assert.Equal(t, workspaceapi.Pid(1), pid)

	// Process should appear in status output.
	iter := exec.handleStatus()
	defer func() { _ = iter.Close() }()
	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "PID")
	assert.Contains(t, out, "UPTIME")
	assert.Contains(t, out, "LAST ERR")
	assert.Contains(t, out, "COMMAND")
	assert.Contains(t, out, "gopls")
	assert.Contains(t, out, "-rpc.trace")
}

func TestFormatCmd(t *testing.T) {
	cases := []struct {
		name string
		info processInfo
		want string
	}{
		{
			name: "path and args",
			info: processInfo{path: "/bin/ls", args: []string{"-l"}},
			want: "/bin/ls -l",
		},
		{
			name: "path only",
			info: processInfo{path: "/bin/sleep"},
			want: "/bin/sleep",
		},
		{
			name: "empty path is rendered as login shell",
			info: processInfo{},
			want: "(login shell)",
		},
		{
			name: "empty path with args still shows args",
			info: processInfo{args: []string{"--login", "-i"}},
			want: "(login shell) --login -i",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, formatCmd(tc.info))
		})
	}
}

func TestHandleCommandStatusEmptyPathRendersLoginShell(t *testing.T) {
	// Regression: term/vte spawns the user's login shell by passing
	// an empty Cmd.Path (the protocol contract). The actual binary is
	// resolved inside the file scheme, so the workspaceshell tracker
	// only sees an empty path. process status/tree/info must not
	// render an empty cell for these entries.
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{})
	require.NoError(t, err)

	statusIter := exec.handleStatus()
	defer func() { _ = statusIter.Close() }()
	statusOut := collectRenderedText(t, statusIter)
	assert.Contains(t, statusOut, "(login shell)")

	treeIter := exec.handleTree()
	defer func() { _ = treeIter.Close() }()
	treeOut := collectRenderedText(t, treeIter)
	assert.Contains(t, treeOut, "(login shell)")

	infoIter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"info", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = infoIter.Close() }()
	infoOut := collectRenderedText(t, infoIter)
	assert.Contains(t, infoOut, "(login shell)")
}

func fixedTime(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestStartPipesOriginalWatcher(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	origCh := make(chan error, 1)
	origWatcher := workspaceapi.ChanProcessWatcher(origCh)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{
		Path:    "/usr/bin/ls",
		Watcher: origWatcher,
	})
	require.NoError(t, err)

	// Simulate process exit.
	mock.exitProcess(pid)

	// Original watcher should also be notified.
	exitErr := <-origCh
	assert.NoError(t, exitErr)
}

func TestExitRemovesProcess(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/ls",
	})
	require.NoError(t, err)

	// Simulate exit.
	mock.exitProcess(pid)

	// Wait for goroutine to clean up by checking ps.
	assert.Eventually(t, func() bool {
		exec.mu.RLock()
		defer exec.mu.RUnlock()
		return len(exec.processes) == 0
	}, 1e9, 1e6)
}

func TestStartError(t *testing.T) {
	mock := newMockExecutor()
	mock.startErr = errors.New("boom")
	exec := NewExecutor(mock)

	ctx := context.Background()
	_, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/x"})
	assert.Error(t, err)

	// No process tracked.
	exec.mu.RLock()
	assert.Empty(t, exec.processes)
	exec.mu.RUnlock()
}

func TestSignalDelegates(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	err := exec.Signal(42, syscall.SIGTERM)
	require.NoError(t, err)

	mock.mu.Lock()
	assert.Equal(t, []signalCall{{pid: 42, sig: syscall.SIGTERM}}, mock.signals)
	mock.mu.Unlock()
}

func TestHandleCommandStatus(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	_, err := exec.Start(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/gopls",
		Args: []string{"-rpc.trace"},
	})
	require.NoError(t, err)
	_, err = exec.Start(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/bash",
	})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"status"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "PID")
	assert.Contains(t, out, "UPTIME")
	assert.Contains(t, out, "LAST ERR")
	assert.Contains(t, out, "COMMAND")
	assert.Contains(t, out, "gopls")
	assert.Contains(t, out, "bash")
}

func TestDefaultSubcommandIsHelp(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()

	iter, err := exec.HandleCommand(ctx, repl.Command{Name: "process"}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	combined := ""
	combined += out
	assert.Contains(t, combined, "process status")
	assert.Contains(t, combined, "process audit")
	assert.Contains(t, combined, "process tree")
	assert.Contains(t, combined, "process info")
	assert.Contains(t, combined, "process signal")
	assert.Contains(t, combined, "process stop")
}

func TestHandleCommandSignal(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/sleep"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"signal", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	mock.mu.Lock()
	require.Len(t, mock.signals, 1)
	assert.Equal(t, pid, mock.signals[0].pid)
	assert.Equal(t, syscall.SIGTERM, mock.signals[0].sig)
	mock.mu.Unlock()
}

func TestHandleCommandSignalWithFlagSignal(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/sleep"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"signal", "-9", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	mock.mu.Lock()
	require.Len(t, mock.signals, 1)
	assert.Equal(t, pid, mock.signals[0].pid)
	assert.Equal(t, syscall.SIGKILL, mock.signals[0].sig)
	mock.mu.Unlock()
}

func TestHandleCommandSignalWithTrailingSignal(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/sleep"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"signal", strconv.Itoa(int(pid)), "9"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	mock.mu.Lock()
	require.Len(t, mock.signals, 1)
	assert.Equal(t, pid, mock.signals[0].pid)
	assert.Equal(t, syscall.SIGKILL, mock.signals[0].sig)
	mock.mu.Unlock()
}

func TestHandleCommandSignalNoArgs(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()
	_, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"signal"},
	}, repl.NopProgressWriter())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestHandleCommandSignalInvalidPid(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()
	_, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"signal", "abc"},
	}, repl.NopProgressWriter())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid pid")
}

func TestHandleCommandSignalInvalidSignal(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()
	_, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"signal", "-xyz", "1"},
	}, repl.NopProgressWriter())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid signal")
}

func TestHandleCommandStop(t *testing.T) {
	mock := newMockExecutor()
	mock.termExits = true // SIGTERM causes exit
	exec := NewExecutor(mock)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/sleep"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"stop", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	assert.NotEmpty(t, out)
	assert.Contains(t, out[0], "stopped")

	mock.mu.Lock()
	// Graceful stop sends SIGTERM first.
	require.GreaterOrEqual(t, len(mock.signals), 1)
	assert.Equal(t, pid, mock.signals[0].pid)
	assert.Equal(t, syscall.SIGTERM, mock.signals[0].sig)
	mock.mu.Unlock()
}

func TestHandleCommandUnknown(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()
	_, err := exec.HandleCommand(ctx, repl.Command{Name: "nope"}, repl.NopProgressWriter())
	assert.True(t, errors.Is(err, repl.ErrNotFound))
}

func TestRegisterProcessCommand(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	r := ideshell.NewRegistry()
	exec.RegisterProcessCommand(r)

	ctx := context.Background()
	iter, err := r.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"status"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
}

func TestHandleCommandUnknownSubcommand(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()
	_, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"nope"},
	}, repl.NopProgressWriter())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown process subcommand")
}

func TestCompleteSignalPids(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	// Start processes with PIDs 1, 2, 3.
	for range 3 {
		_, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/x"})
		require.NoError(t, err)
	}

	// Complete "process signal 1" → should return "1".
	iter, err := exec.Complete(ctx, "process", []string{"signal", "1"})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, got)

	// Complete "process signal " with empty prefix → all PIDs.
	iter2, err := exec.Complete(ctx, "process", []string{"signal", ""})
	require.NoError(t, err)
	defer func() { _ = iter2.Close() }()
	got2, err := iterator.ToSlice(ctx, iter2)
	require.NoError(t, err)
	assert.Equal(t, []string{"1", "2", "3"}, got2)
}

func TestCompleteStopPids(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	_, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/x"})
	require.NoError(t, err)

	iter, err := exec.Complete(ctx, "process", []string{"stop", ""})
	require.NoError(t, err)
	got, err := iterator.ToSlice(ctx, iter)
	_ = iter.Close()
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, got)
}

func TestCompleteSubcommands(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()

	iter, err := exec.Complete(ctx, "process", []string{})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"status", "audit", "tree", "info", "signal", "stop", "stdio",
	}, got)

	iter2, err := exec.Complete(ctx, "process", []string{"st"})
	require.NoError(t, err)
	defer func() { _ = iter2.Close() }()
	got2, err := iterator.ToSlice(ctx, iter2)
	require.NoError(t, err)
	assert.Equal(t, []string{"status", "stop", "stdio"}, got2)
}

func TestHelp(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()
	iter, err := exec.Help(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	assert.NotEmpty(t, out)
	combined := ""
	for _, line := range out {
		combined += line
	}
	assert.Contains(t, combined, "process status")
	assert.Contains(t, combined, "process audit")
	assert.Contains(t, combined, "process tree")
	assert.Contains(t, combined, "process info")
	assert.Contains(t, combined, "process signal")
	assert.Contains(t, combined, "process stop")
}

func TestPSSortedByPid(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	// Start 3 processes.
	for _, path := range []string{"/c", "/a", "/b"} {
		_, err := exec.Start(ctx, workspaceapi.Cmd{Path: path})
		require.NoError(t, err)
	}

	iter := exec.handleStatus()
	defer func() { _ = iter.Close() }()
	out := collectRenderedText(t, iter)

	// PIDs should be 1, 2, 3 in order.
	idxC := strings.Index(out, "/c")
	idxA := strings.Index(out, "/a")
	idxB := strings.Index(out, "/b")
	require.NotEqual(t, -1, idxC)
	require.NotEqual(t, -1, idxA)
	require.NotEqual(t, -1, idxB)
	assert.Less(t, idxC, idxA)
	assert.Less(t, idxA, idxB)
}

func TestPSShowsUptime(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exec.now = fixedTime(started)

	ctx := context.Background()
	_, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/x"})
	require.NoError(t, err)

	// Advance time by 5 minutes and 32 seconds.
	exec.now = fixedTime(started.Add(5*time.Minute + 32*time.Second))

	iter := exec.handleStatus()
	defer func() { _ = iter.Close() }()
	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "5m32s")
}

func TestExitTracksStats(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	cmd := workspaceapi.Cmd{Path: "/bin/x", Args: []string{"a"}}

	pid, err := exec.Start(ctx, cmd)
	require.NoError(t, err)

	// Simulate exit with error.
	mock.exitProcessWithError(pid, errors.New("segfault"))

	key := makeCmdKey(cmd.Path, cmd.Args)
	assert.Eventually(t, func() bool {
		exec.mu.RLock()
		defer exec.mu.RUnlock()
		return exec.stats[key] != nil
	}, 1e9, 1e6)

	exec.mu.RLock()
	assert.EqualError(t, exec.stats[key].lastErr, "segfault")
	exec.mu.RUnlock()
}

func TestStatusShowsLastErr(t *testing.T) {
	// lastErr is tracked per cmdKey and displayed for any running
	// process that shares that key.
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	cmd := workspaceapi.Cmd{Path: "/bin/x"}

	pid, err := exec.Start(ctx, cmd)
	require.NoError(t, err)
	mock.exitProcessWithError(pid, errors.New("process failed very badly with a long error"))
	assert.Eventually(t, func() bool {
		exec.mu.RLock()
		defer exec.mu.RUnlock()
		return len(exec.processes) == 0
	}, 1e9, 1e6)

	_, err = exec.Start(ctx, cmd)
	require.NoError(t, err)

	iter := exec.handleStatus()
	defer func() { _ = iter.Close() }()
	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "LAST ERR")
	assert.Contains(t, out, "process failed very badly")
	assert.Contains(t, out, "…")
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0s"},
		{45 * time.Second, "45s"},
		{5*time.Minute + 32*time.Second, "5m32s"},
		{2*time.Hour + 15*time.Minute, "2h15m"},
		{36 * time.Hour, "1d12h"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, formatDuration(tc.d), "duration=%v", tc.d)
	}
}

func TestContextParentPidTracksParent(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	parent, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/parent"})
	require.NoError(t, err)

	childCtx := ContextWithParentPid(ctx, parent)
	child, err := exec.Start(childCtx, workspaceapi.Cmd{
		Path: "/bin/child",
	})
	require.NoError(t, err)

	exec.mu.RLock()
	parentInfo := exec.processes[parent]
	childInfo := exec.processes[child]
	exec.mu.RUnlock()

	assert.Equal(t, workspaceapi.Pid(0), parentInfo.parent)
	assert.Equal(t, parent, childInfo.parent)
}

func TestHandleCommandTree(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	_, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/a"})
	require.NoError(t, err)
	_, err = exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/b"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"tree"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "Process tree")
	assert.Contains(t, out, "PID 1")
	assert.Contains(t, out, "PPID: —")
	assert.Contains(t, out, "/bin/a")
	assert.Contains(t, out, "/bin/b")
}

func TestHandleCommandTreeWithParent(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	parent, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/ext"})
	require.NoError(t, err)
	childCtx := ContextWithParentPid(ctx, parent)
	_, err = exec.Start(childCtx, workspaceapi.Cmd{
		Path: "/bin/lsp",
	})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"tree"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "/bin/ext")
	assert.Contains(t, out, "PPID: —")
	assert.Contains(t, out, "/bin/lsp")
	assert.Contains(t, out, "PPID: "+strconv.Itoa(int(parent)))
}

func TestHandleCommandInfo(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exec.now = fixedTime(started)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/gopls",
		Args: []string{"-rpc.trace"},
		Dir:  "/home/user/project",
		Env:  []string{"GOPATH=/go", "HOME=/home/user"},
	})
	require.NoError(t, err)

	// Advance time 10 minutes.
	exec.now = fixedTime(started.Add(10 * time.Minute))

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"info", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	combined := out + "\n"
	assert.Contains(t, combined, "PID:")
	assert.Contains(t, combined, "1")
	assert.Contains(t, combined, "Command:")
	assert.Contains(t, combined, "gopls -rpc.trace")
	assert.Contains(t, combined, "Directory:")
	assert.Contains(t, combined, "/home/user/project")
	assert.Contains(t, combined, "Started:")
	assert.Contains(t, combined, "Uptime:")
	assert.Contains(t, combined, "10m0s")
	assert.Contains(t, combined, "Environment:")
	assert.Contains(t, combined, "GOPATH=/go")
	assert.Contains(t, combined, "HOME=/home/user")
}

func TestHandleCommandInfoNotFound(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()
	_, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"info", "999"},
	}, repl.NopProgressWriter())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestHandleCommandInfoErrored(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	started := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	exec.now = fixedTime(started)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{
		Path: "/usr/bin/gopls",
		Args: []string{"-rpc.trace"},
		Dir:  "/home/user/project",
	})
	require.NoError(t, err)

	// Advance time 10 minutes, then simulate exit with an error.
	exec.now = fixedTime(started.Add(10 * time.Minute))
	mock.exitProcessWithError(pid, errors.New("boom"))

	// Wait for the exit goroutine to fully finish updating history.
	// We must observe the goroutine after it released e.mu so that
	// any subsequent mutation of exec.now is race-free.
	assert.Eventually(t, func() bool {
		exec.mu.RLock()
		defer exec.mu.RUnlock()
		_, running := exec.processes[pid]
		return !running
	}, time.Second, time.Millisecond)

	// Advance time further to confirm uptime is frozen at ended-started.
	exec.now = fixedTime(started.Add(1 * time.Hour))

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"info", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "PID:")
	assert.Contains(t, out, "gopls -rpc.trace")
	assert.Contains(t, out, "Uptime:")
	assert.Contains(t, out, "10m0s")
	assert.Contains(t, out, "Ended:")
	assert.Contains(t, out, "Last Error:")
	assert.Contains(t, out, "boom")
}

func TestHandleCommandInfoNoArgs(t *testing.T) {
	exec := NewExecutor(newMockExecutor())
	ctx := context.Background()
	_, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"info"},
	}, repl.NopProgressWriter())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "usage")
}

func TestCompleteInfoPids(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	_, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/x"})
	require.NoError(t, err)

	iter, err := exec.Complete(ctx, "process", []string{"info", ""})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Equal(t, []string{"1"}, got)
}

func TestProcessDoneChannelClosedOnExit(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/x"})
	require.NoError(t, err)

	// Grab done channel.
	exec.mu.RLock()
	done := exec.processes[pid].done
	exec.mu.RUnlock()

	// Simulate exit.
	mock.exitProcess(pid)

	// done should be closed.
	select {
	case <-done:
		// ok
	case <-time.After(time.Second):
		t.Fatal("done channel not closed after process exit")
	}
}

func TestHandleCommandStopGraceful(t *testing.T) {
	mock := newMockExecutor()
	mock.termExits = true // SIGTERM causes immediate exit
	exec := NewExecutor(mock)

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/sleep"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"stop", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	require.NotEmpty(t, out)
	assert.Contains(t, out[0], "stopped")

	// Only SIGTERM should have been sent.
	mock.mu.Lock()
	require.Len(t, mock.signals, 1)
	assert.Equal(t, syscall.SIGTERM, mock.signals[0].sig)
	mock.mu.Unlock()
}

func TestHandleCommandStopForcedAfterTimeout(t *testing.T) {
	mock := newMockExecutor()
	// Don't set termExits — SIGTERM won't cause exit.
	exec := NewExecutor(mock)
	exec.stopGrace = 10 * time.Millisecond // Short grace for test.

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/sleep"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"stop", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectText(t, iter)
	require.NotEmpty(t, out)
	assert.Contains(t, out[0], "killed")
	assert.Contains(t, out[0], "timed out")

	// Both SIGTERM and SIGKILL should have been sent.
	mock.mu.Lock()
	require.Len(t, mock.signals, 2)
	assert.Equal(t, syscall.SIGTERM, mock.signals[0].sig)
	assert.Equal(t, syscall.SIGKILL, mock.signals[1].sig)
	mock.mu.Unlock()
}

func TestHandleCommandAuditShowsAllProcesses(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	started := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	exec.now = fixedTime(started)

	ctx := context.Background()

	// Start two processes.
	pid1, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/a"})
	require.NoError(t, err)
	_, err = exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/b"})
	require.NoError(t, err)

	// Exit the first process.
	exec.now = fixedTime(started.Add(5 * time.Second))
	mock.exitProcessWithError(pid1, errors.New("crash"))

	// Wait for exit goroutine to run.
	assert.Eventually(t, func() bool {
		exec.mu.RLock()
		defer exec.mu.RUnlock()
		_, running := exec.processes[pid1]
		return !running
	}, time.Second, time.Millisecond)

	// Advance time for uptime display.
	exec.now = fixedTime(started.Add(10 * time.Second))

	// Status should show only the running process.
	statusIter := exec.handleStatus()
	defer func() { _ = statusIter.Close() }()
	statusOut := collectRenderedText(t, statusIter)
	assert.Contains(t, statusOut, "/bin/b")

	// Audit should show both processes.
	auditIter := exec.handleAudit()
	defer func() { _ = auditIter.Close() }()
	auditOut := collectRenderedText(t, auditIter)
	assert.Contains(t, auditOut, "PID")
	assert.Contains(t, auditOut, "UPTIME")
	assert.Contains(t, auditOut, "LAST ERR")

	// Exited process should show its runtime (5s) and error.
	assert.Contains(t, auditOut, "/bin/a")
	assert.Contains(t, auditOut, "5s")
	assert.Contains(t, auditOut, "crash")

	// Running process should show current uptime (10s).
	assert.Contains(t, auditOut, "/bin/b")
	assert.Contains(t, auditOut, "10s")
}

func TestHandleCommandAuditViaDispatch(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	_, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/x"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"audit"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "/bin/x")
}

func TestContextParentPidPropagation(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)

	ctx := context.Background()

	// Start parent process normally.
	parent, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/parent"})
	require.NoError(t, err)

	// Start child process with parent PID in context.
	childCtx := ContextWithParentPid(ctx, parent)
	child, err := exec.Start(childCtx, workspaceapi.Cmd{Path: "/bin/child"})
	require.NoError(t, err)

	exec.mu.RLock()
	parentInfo := exec.processes[parent]
	childInfo := exec.processes[child]
	exec.mu.RUnlock()

	assert.Equal(t, workspaceapi.Pid(0), parentInfo.parent,
		"parent should have no parent")
	assert.Equal(t, parent, childInfo.parent,
		"child should have parent PID from context")
}

func TestContextParentPidShowsInTree(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()

	parent, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/ext"})
	require.NoError(t, err)

	childCtx := ContextWithParentPid(ctx, parent)
	_, err = exec.Start(childCtx, workspaceapi.Cmd{Path: "/bin/lsp"})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"tree"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "/bin/ext")
	assert.Contains(t, out, "PPID: —")
	assert.Contains(t, out, "/bin/lsp")
	assert.Contains(t, out, strconv.Itoa(int(parent)))
}

func TestExtensionIDParentsSubprocesses(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := processctx.ContextWithExtensionID(context.Background(), "test-extension")
	parent, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/ext"})
	require.NoError(t, err)

	child, err := exec.Start(ctx, workspaceapi.Cmd{Path: "/bin/lsp"})
	require.NoError(t, err)

	exec.mu.RLock()
	parentInfo := exec.processes[parent]
	childInfo := exec.processes[child]
	exec.mu.RUnlock()

	assert.Equal(t, workspaceapi.Pid(0), parentInfo.parent)
	assert.Equal(t, parent, childInfo.parent)

	iter := exec.handleTree()
	defer func() { _ = iter.Close() }()
	out := collectRenderedText(t, iter)
	assert.Contains(t, out, "/bin/ext")
	assert.Contains(t, out, strconv.Itoa(int(parent)))
	assert.Contains(t, out, "/bin/lsp")
}

func TestHandleCommandInfoRedactsSecrets(t *testing.T) {
	mock := newMockExecutor()
	exec := NewExecutor(mock)
	exec.now = fixedTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	ctx := context.Background()
	pid, err := exec.Start(ctx, workspaceapi.Cmd{
		Path: "/bin/ext",
		Env: []string{
			"RUNE_CERT=secret-cert-value",
			"RUNE_TOKEN=secret-token-value",
			"IDE_CERT=ide-cert-value",
			"IDE_TOKEN=ide-token-value",
			"HOME=/home/user",
			"NO_EQUALS",
		},
	})
	require.NoError(t, err)

	iter, err := exec.HandleCommand(ctx, repl.Command{
		Name: "process",
		Args: []string{"info", strconv.Itoa(int(pid))},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := collectRenderedText(t, iter)
	combined := out

	// Secret values must be redacted.
	assert.Contains(t, combined, "RUNE_CERT=****")
	assert.Contains(t, combined, "RUNE_TOKEN=****")
	assert.Contains(t, combined, "IDE_CERT=****")
	assert.Contains(t, combined, "IDE_TOKEN=****")

	// Original secret values must not appear.
	assert.NotContains(t, combined, "secret-cert-value")
	assert.NotContains(t, combined, "secret-token-value")
	assert.NotContains(t, combined, "ide-cert-value")
	assert.NotContains(t, combined, "ide-token-value")

	// Non-secret env vars must remain visible.
	assert.Contains(t, combined, "HOME=/home/user")
	assert.Contains(t, combined, "NO_EQUALS")
}

func TestRedactEnv(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"RUNE_CERT=abc", "RUNE_CERT=****"},
		{"RUNE_TOKEN=xyz", "RUNE_TOKEN=****"},
		{"IDE_CERT=123", "IDE_CERT=****"},
		{"IDE_TOKEN=456", "IDE_TOKEN=****"},
		{"HOME=/home/user", "HOME=/home/user"},
		{"PATH=/usr/bin", "PATH=/usr/bin"},
		{"NO_EQUALS", "NO_EQUALS"},
		{"RUNE_CERT=", "RUNE_CERT=****"},
		{"IDE_TOKEN=has=equals", "IDE_TOKEN=****"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, redactEnv(tc.input), "input=%q", tc.input)
	}
}

// schemeExecutorAdapter wraps a schemeapi.Executor as a
// workspaceapi.Executor for use with NewExecutor.
type schemeExecutorAdapter struct {
	s schemeapi.Executor
}

func (a schemeExecutorAdapter) Start(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	return a.s.StartCommand(ctx, cmd)
}

func (a schemeExecutorAdapter) Signal(
	pid workspaceapi.Pid, sig syscall.Signal,
) error {
	return a.s.Signal(pid, sig)
}

func (a schemeExecutorAdapter) Close() error {
	return a.s.Close()
}

func TestE2EProcessTreeWithRealExecutor(t *testing.T) {
	tmpDir := t.TempDir()

	uri, err := workspaceapi.ParseURI("file://" + tmpDir)
	require.NoError(t, err)

	scheme, err := workspace.NewFileScheme(
		context.Background(), config.NopConfig(), uri,
	)
	require.NoError(t, err)
	defer func() { _ = scheme.Close() }()

	exec := NewExecutor(schemeExecutorAdapter{s: scheme.(schemeapi.Executor)})

	ctx := context.Background()

	// Start a long-running "parent" process.
	parentPid, err := exec.Start(ctx, workspaceapi.Cmd{
		Path: "/bin/sleep",
		Args: []string{"30"},
	})
	require.NoError(t, err)
	defer func() { _ = exec.Signal(parentPid, syscall.SIGKILL) }()

	// Start a "child" process with logical parent context.
	childCtx := ContextWithParentPid(ctx, parentPid)
	childPid, err := exec.Start(childCtx, workspaceapi.Cmd{
		Path: "/bin/sleep",
		Args: []string{"30"},
	})
	require.NoError(t, err)
	defer func() { _ = exec.Signal(childPid, syscall.SIGKILL) }()

	// Verify the tree output shows the parent-child relationship.
	iter := exec.handleTree()
	defer func() { _ = iter.Close() }()
	out := collectRenderedText(t, iter)

	assert.Contains(t, out, "Process tree")
	assert.Contains(t, out, strconv.Itoa(int(parentPid)))
	assert.Contains(t, out, "PPID: —")
	assert.Contains(t, out, "/bin/sleep")
	assert.Contains(t, out, strconv.Itoa(int(childPid)))
	assert.Contains(t, out, "PPID: "+strconv.Itoa(int(parentPid)))
}

// RUNE-181 — comprehensive regression suite for the
// `process status` / `process audit` markdown table renderer.
//
// The renderer wraps each cell in `` `…` `` and separates cells
// with `|`. A naive implementation breaks the table when an
// argv element contains literal newlines, backticks, or pipes
// — embedded `\n` terminates the markdown row early and stray
// backticks/pipes split the cell. These tests guarantee that
// for *any* shell heredoc / inline-script argv we have ever
// seen in the wild, the rendered row stays on exactly one line
// and every cell stays inside its delimiters.
//
// Test cases are listed at the top; fixtures, helpers and the
// supporting argv corpus live at the bottom of this file per
// the project's "table tests on top, fixtures on bottom"
// convention.

func TestEscapeMarkdownTableCell(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "plain ascii", in: "hello", want: "hello"},
		{name: "unicode passthrough", in: "héllo — world", want: "héllo — world"},
		{name: "tab is preserved", in: "a\tb", want: "a\tb"},

		{name: "single pipe", in: "a|b", want: "a\\|b"},
		{name: "many pipes", in: "a|b|c|d", want: "a\\|b\\|c\\|d"},
		{name: "single backtick", in: "a`b", want: "a\\`b"},
		{name: "many backticks", in: "`a``b`", want: "\\`a\\`\\`b\\`"},

		{name: "lf only", in: "\n", want: "␤"},
		{name: "cr only", in: "\r", want: "␤"},
		{name: "crlf only", in: "\r\n", want: "␤"},
		{name: "lone lf middle", in: "a\nb", want: "a␤b"},
		{name: "lone cr middle", in: "a\rb", want: "a␤b"},
		{name: "crlf middle", in: "a\r\nb", want: "a␤b"},
		{name: "multiple lfs", in: "a\n\nb\n", want: "a␤␤b␤"},
		{name: "mixed cr lf crlf", in: "a\rb\nc\r\nd", want: "a␤b␤c␤d"},
		{name: "trailing newline", in: "a\n", want: "a␤"},
		{name: "leading newline", in: "\na", want: "␤a"},

		{
			name: "heredoc body",
			in:   "bash -c cd /tmp && python3 <<PY\nprint(1)\nPY",
			want: "bash -c cd /tmp && python3 <<PY␤print(1)␤PY",
		},
		{
			name: "all specials at once",
			in:   "echo `whoami` | tee /tmp/x\n",
			want: "echo \\`whoami\\` \\| tee /tmp/x␤",
		},
		{
			name: "ansi-c quoted newline",
			in:   "printf $'line1\\nline2\\n'\n",
			want: "printf $'line1\\nline2\\n'␤",
		},
		{
			name: "nul byte is preserved",
			in:   "a\x00b",
			want: "a\x00b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := escapeMarkdownTableCell(tc.in)
			assert.Equal(t, tc.want, got)
			assertSanitizedCell(t, got)
		})
	}
}

func TestBuildProcessTableMarkdownSource_HeredocCorpus(t *testing.T) {
	for _, tc := range heredocCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			entry := psEntry{
				pid:     tc.pid,
				uptime:  tc.uptime,
				lastErr: tc.lastErr,
				command: formatCmdFromArgv(tc.path, tc.args),
			}
			got := buildProcessTableMarkdownSource(
				[]psEntry{entry},
			)
			assert.Equal(t, tableHeader+tc.wantRow, got,
				"markdown source mismatch")
			assertTableStructure(t, got, 1)
		})
	}
}

func TestBuildProcessTableMarkdownSource_Structural(t *testing.T) {
	cases := []struct {
		name    string
		entries []psEntry
		want    string
	}{
		{
			name:    "empty table is header-only",
			entries: nil,
			want:    tableHeader,
		},
		{
			name: "single benign entry",
			entries: []psEntry{
				benignEntry(1, 5*time.Second, "/bin/ls -l"),
			},
			want: tableHeader +
				"| `1` | `5s` | — | `/bin/ls -l` |\n",
		},
		{
			name: "multiple entries sorted by pid",
			entries: []psEntry{
				benignEntry(20, 6*time.Second, "/bin/b"),
				benignEntry(10, 5*time.Second, "/bin/a"),
				benignEntry(15, 7*time.Second, "/bin/c"),
			},
			want: tableHeader +
				"| `10` | `5s` | — | `/bin/a` |\n" +
				"| `15` | `7s` | — | `/bin/c` |\n" +
				"| `20` | `6s` | — | `/bin/b` |\n",
		},
		{
			name: "lastErr with embedded newline",
			entries: []psEntry{{
				pid:     5,
				uptime:  4 * time.Second,
				lastErr: "boom\nstack",
				command: "/bin/x",
			}},
			want: tableHeader +
				"| `5` | `4s` | boom␤stack | `/bin/x` |\n",
		},
		{
			name: "lastErr with backtick and pipe",
			entries: []psEntry{{
				pid:     6,
				uptime:  time.Second,
				lastErr: "exit `1` | core",
				command: "/bin/y",
			}},
			want: tableHeader +
				"| `6` | `1s` | exit \\`1\\` \\| core | `/bin/y` |\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildProcessTableMarkdownSource(tc.entries)
			assert.Equal(t, tc.want, got)
			assertTableStructure(t, got, len(tc.entries))
		})
	}
}

func TestHandleStatusHeredocCorpus(t *testing.T) {
	for _, tc := range heredocCorpus() {
		t.Run(tc.name, func(t *testing.T) {
			mock := newMockExecutor()
			exec := NewExecutor(mock)
			exec.now = fixedTime(
				time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			)

			ctx := context.Background()
			_, err := exec.Start(ctx, workspaceapi.Cmd{
				Path: tc.path,
				Args: tc.args,
			})
			require.NoError(t, err)

			iter := exec.handleStatus()
			defer func() { _ = iter.Close() }()
			out := collectRenderedText(t, iter)

			assert.Contains(t, out, "PID")
			assert.Contains(t, out, "UPTIME")
			assert.Contains(t, out, "LAST ERR")
			assert.Contains(t, out, "COMMAND")
			if argvHasNewline(tc.path, tc.args) {
				assert.Contains(t, out, "␤",
					"sanitizer marker missing")
			}
			if base := basename(tc.path); base != "" {
				assert.Contains(t, out, base,
					"command basename missing")
			}
		})
	}
}

// ============================================================
// Fixtures and helpers (table tests on top, fixtures below).
// ============================================================

// tableHeader is the literal two-line markdown header emitted
// by buildProcessTableMarkdownSource. Centralised so individual
// test cases stay focused on the row(s) they exercise.
const tableHeader = "| PID | UPTIME | LAST ERR | COMMAND |\n" +
	"| --- | --- | --- | --- |\n"

// heredocCase is one row of the language-realistic argv corpus.
// Each case fully describes a tracked process plus the expected
// rendered markdown row.
type heredocCase struct {
	name    string
	path    string
	args    []string
	pid     workspaceapi.Pid
	uptime  time.Duration
	lastErr string
	wantRow string
}

// heredocCorpus returns the realistic argv invocations we want
// to be bullet-proof against. Each entry models a real way a
// shell, REPL, or build tool spawns a child process whose argv
// contains characters that previously broke the markdown table.
func heredocCorpus() []heredocCase {
	return []heredocCase{
		{
			name: "bash -c heredoc python (rune-181 repro)",
			path: "/bin/bash",
			args: []string{
				"-c",
				"cd /tmp && python3 <<PY\nprint(1)\nPY",
			},
			pid:     42,
			uptime:  7 * time.Second,
			lastErr: "—",
			wantRow: "| `42` | `7s` | — | " +
				"`/bin/bash -c cd /tmp && python3 " +
				"<<PY␤print(1)␤PY` |\n",
		},
		{
			name: "bash -c quoted heredoc no expansion",
			path: "/bin/bash",
			args: []string{
				"-c",
				"cat <<'EOF'\nliteral $HOME `cmd`\nEOF",
			},
			pid:     43,
			uptime:  3 * time.Second,
			lastErr: "—",
			wantRow: "| `43` | `3s` | — | " +
				"`/bin/bash -c cat <<'EOF'␤" +
				"literal $HOME \\`cmd\\`␤EOF` |\n",
		},
		{
			name: "bash -c dash heredoc tab-strip",
			path: "/bin/bash",
			args: []string{
				"-c",
				"cat <<-EOF\n\tindented\n\tEOF",
			},
			pid:     44,
			uptime:  time.Second,
			lastErr: "—",
			wantRow: "| `44` | `1s` | — | " +
				"`/bin/bash -c cat <<-EOF␤\tindented" +
				"␤\tEOF` |\n",
		},
		{
			name: "dash -c pipe between commands",
			path: "/bin/dash",
			args: []string{
				"-c",
				"ls | wc -l",
			},
			pid:     45,
			uptime:  2 * time.Second,
			lastErr: "—",
			wantRow: "| `45` | `2s` | — | " +
				"`/bin/dash -c ls \\| wc -l` |\n",
		},
		{
			name: "zsh -c command substitution",
			path: "/bin/zsh",
			args: []string{
				"-c",
				"echo `whoami`@`hostname`",
			},
			pid:     46,
			uptime:  4 * time.Second,
			lastErr: "—",
			wantRow: "| `46` | `4s` | — | " +
				"`/bin/zsh -c echo \\`whoami\\`@" +
				"\\`hostname\\`` |\n",
		},
		{
			name: "ksh print heredoc",
			path: "/bin/ksh",
			args: []string{
				"-c",
				"print -- <<DONE\nhi\nDONE",
			},
			pid:     47,
			uptime:  5 * time.Second,
			lastErr: "—",
			wantRow: "| `47` | `5s` | — | " +
				"`/bin/ksh -c print -- <<DONE␤hi␤" +
				"DONE` |\n",
		},
		{
			name: "python -c inline",
			path: "/usr/bin/python3",
			args: []string{
				"-c",
				"import sys\nprint(sys.argv)\n",
			},
			pid:     48,
			uptime:  6 * time.Second,
			lastErr: "—",
			wantRow: "| `48` | `6s` | — | " +
				"`/usr/bin/python3 -c import sys␤" +
				"print(sys.argv)␤` |\n",
		},
		{
			name: "perl -e multiline",
			path: "/usr/bin/perl",
			args: []string{
				"-e",
				"use strict;\nprint \"hi\\n\";",
			},
			pid:     49,
			uptime:  8 * time.Second,
			lastErr: "—",
			wantRow: "| `49` | `8s` | — | " +
				"`/usr/bin/perl -e use strict;␤print " +
				"\"hi\\n\";` |\n",
		},
		{
			name: "perl heredoc EOF quoted",
			path: "/bin/bash",
			args: []string{
				"-c",
				"perl <<'PERL'\nprint 1;\nPERL",
			},
			pid:     50,
			uptime:  9 * time.Second,
			lastErr: "—",
			wantRow: "| `50` | `9s` | — | " +
				"`/bin/bash -c perl <<'PERL'␤print 1;␤" +
				"PERL` |\n",
		},
		{
			name: "ruby squiggly heredoc",
			path: "/bin/bash",
			args: []string{
				"-c",
				"ruby <<~RUBY\n  puts :hi\nRUBY",
			},
			pid:     51,
			uptime:  10 * time.Second,
			lastErr: "—",
			wantRow: "| `51` | `10s` | — | " +
				"`/bin/bash -c ruby <<~RUBY␤  puts :hi␤" +
				"RUBY` |\n",
		},
		{
			name: "node heredoc js",
			path: "/bin/bash",
			args: []string{
				"-c",
				"node <<JS\nconsole.log(1)\nJS",
			},
			pid:     52,
			uptime:  11 * time.Second,
			lastErr: "—",
			wantRow: "| `52` | `11s` | — | " +
				"`/bin/bash -c node <<JS␤console.log(1)␤" +
				"JS` |\n",
		},
		{
			name: "php heredoc EOT",
			path: "/bin/bash",
			args: []string{
				"-c",
				"php <<<EOT\n<?php echo 1; ?>\nEOT",
			},
			pid:     53,
			uptime:  12 * time.Second,
			lastErr: "—",
			wantRow: "| `53` | `12s` | — | " +
				"`/bin/bash -c php <<<EOT␤<?php echo 1; ?>␤" +
				"EOT` |\n",
		},
		{
			name: "psql heredoc SQL pipe",
			path: "/bin/bash",
			args: []string{
				"-c",
				"psql <<SQL\nSELECT 1 | 2;\nSQL",
			},
			pid:     54,
			uptime:  13 * time.Second,
			lastErr: "—",
			wantRow: "| `54` | `13s` | — | " +
				"`/bin/bash -c psql <<SQL␤SELECT 1 \\| 2;␤" +
				"SQL` |\n",
		},
		{
			name: "awk multiline program",
			path: "/usr/bin/awk",
			args: []string{
				"BEGIN { print 1 }\n{ print $0 }\nEND { print 2 }",
				"/etc/hosts",
			},
			pid:     55,
			uptime:  14 * time.Second,
			lastErr: "—",
			wantRow: "| `55` | `14s` | — | " +
				"`/usr/bin/awk BEGIN { print 1 }␤{ print $0 }" +
				"␤END { print 2 } /etc/hosts` |\n",
		},
		{
			name: "sed multiline script",
			path: "/usr/bin/sed",
			args: []string{
				"-e",
				"s/a/b/\n/start/,/end/{\n  d\n}",
				"file",
			},
			pid:     56,
			uptime:  15 * time.Second,
			lastErr: "—",
			wantRow: "| `56` | `15s` | — | " +
				"`/usr/bin/sed -e s/a/b/␤/start/,/end/{␤  " +
				"d␤} file` |\n",
		},
		{
			name: "ssh remote heredoc with crlf",
			path: "/usr/bin/ssh",
			args: []string{
				"host",
				"bash <<'REMOTE'\r\nls\r\nREMOTE",
			},
			pid:     57,
			uptime:  16 * time.Second,
			lastErr: "—",
			wantRow: "| `57` | `16s` | — | " +
				"`/usr/bin/ssh host bash <<'REMOTE'␤ls␤" +
				"REMOTE` |\n",
		},
		{
			name:    "single argv literal newlines only",
			path:    "/bin/echo",
			args:    []string{"\n\n\n"},
			pid:     58,
			uptime:  17 * time.Second,
			lastErr: "—",
			wantRow: "| `58` | `17s` | — | " +
				"`/bin/echo ␤␤␤` |\n",
		},
		{
			name:    "argv with only carriage returns",
			path:    "/bin/echo",
			args:    []string{"a\rb\rc"},
			pid:     59,
			uptime:  18 * time.Second,
			lastErr: "—",
			wantRow: "| `59` | `18s` | — | " +
				"`/bin/echo a␤b␤c` |\n",
		},
		{
			name: "all-special argv (lf + cr + pipe + backtick)",
			path: "/bin/bash",
			args: []string{
				"-c",
				"echo `id` | tee log\r\nexit",
			},
			pid:     60,
			uptime:  19 * time.Second,
			lastErr: "—",
			wantRow: "| `60` | `19s` | — | " +
				"`/bin/bash -c echo \\`id\\` \\| tee log␤" +
				"exit` |\n",
		},
		{
			name:    "empty path is rendered as login shell",
			path:    "",
			args:    []string{"--login", "-c", "echo hi"},
			pid:     61,
			uptime:  20 * time.Second,
			lastErr: "—",
			wantRow: "| `61` | `20s` | — | " +
				"`(login shell) --login -c echo hi` |\n",
		},
		{
			name: "lastErr with newline alongside heredoc command",
			path: "/bin/bash",
			args: []string{
				"-c",
				"python3 <<PY\nraise SystemExit(1)\nPY",
			},
			pid:     62,
			uptime:  21 * time.Second,
			lastErr: "Traceback\nSystemExit: 1",
			wantRow: "| `62` | `21s` | Traceback␤SystemExit: 1 | " +
				"`/bin/bash -c python3 <<PY␤raise SystemExit(1)" +
				"␤PY` |\n",
		},
	}
}

// benignEntry returns a psEntry with a `—` lastErr — the
// shape produced by handleStatus for a still-running process
// that has no recorded error.
func benignEntry(
	pid workspaceapi.Pid, uptime time.Duration, command string,
) psEntry {
	return psEntry{
		pid:     pid,
		uptime:  uptime,
		lastErr: "—",
		command: command,
	}
}

// formatCmdFromArgv mirrors formatCmd's argv→string projection
// without constructing a full processInfo. Tests use it so the
// markdown-source assertions exercise the same join logic that
// handleStatus/handleAudit perform at runtime.
func formatCmdFromArgv(path string, args []string) string {
	return formatCmd(processInfo{path: path, args: args})
}

// argvHasNewline reports whether any element of the
// path-plus-args tuple contains a CR or LF — used by the
// end-to-end test to decide whether the sanitizer marker is
// expected in the rendered output.
func argvHasNewline(path string, args []string) bool {
	if strings.ContainsAny(path, "\r\n") {
		return true
	}
	for _, a := range args {
		if strings.ContainsAny(a, "\r\n") {
			return true
		}
	}
	return false
}

// basename returns the last path component, or "" if path is
// empty. The end-to-end test uses it as a canary that the
// COMMAND cell still carries something resembling the original
// invocation after sanitization.
func basename(path string) string {
	if path == "" {
		return ""
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

// assertSanitizedCell encodes the cell-level invariants the
// sanitizer must enforce: no raw CR/LF, no unescaped backticks,
// no unescaped pipes.
func assertSanitizedCell(t *testing.T, got string) {
	t.Helper()
	assert.NotContains(t, got, "\n", "raw LF survived")
	assert.NotContains(t, got, "\r", "raw CR survived")
	for i := 0; i < len(got); i++ {
		switch got[i] {
		case '`':
			if i == 0 || got[i-1] != '\\' {
				t.Fatalf("unescaped backtick at %d in %q",
					i, got)
			}
		case '|':
			if i == 0 || got[i-1] != '\\' {
				t.Fatalf("unescaped pipe at %d in %q",
					i, got)
			}
		}
	}
}

// assertTableStructure encodes the row-level invariants the
// renderer must enforce: header rows + exactly one row per
// entry, each row terminated by a single '\n', no raw CR
// anywhere in the source, and every row begins with "| " and
// ends with " |".
func assertTableStructure(t *testing.T, got string, entries int) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	require.Equal(t, 2+entries, len(lines),
		"expected header(2)+%d entries; got %d lines: %q",
		entries, len(lines), lines)
	assert.NotContains(t, got, "\r", "raw CR in markdown source")
	for i, line := range lines {
		assert.True(t, strings.HasPrefix(line, "|"),
			"row %d missing leading pipe: %q", i, line)
		assert.True(t, strings.HasSuffix(line, "|"),
			"row %d missing trailing pipe: %q", i, line)
	}
}
