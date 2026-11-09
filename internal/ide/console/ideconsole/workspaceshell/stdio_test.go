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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

func newStdioExecutor(t *testing.T, mock *mockExecutor) *Executor {
	t.Helper()
	exec := NewExecutor(mock)
	exec.stdio.root = t.TempDir()
	return exec
}

func stdioOutput(
	t *testing.T, exec *Executor, args ...string,
) string {
	t.Helper()
	iter, err := exec.HandleCommand(
		context.Background(),
		repl.Command{Name: "process", Args: append([]string{"stdio"}, args...)},
		repl.NopProgressWriter(),
	)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	return collectRenderedText(t, iter)
}

func lastStart(m *mockExecutor) workspaceapi.Cmd {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.starts[len(m.starts)-1]
}

func sinkFor(t *testing.T, exec *Executor, pid workspaceapi.Pid) *stdioSink {
	t.Helper()
	exec.mu.RLock()
	defer exec.mu.RUnlock()
	info, ok := exec.processes[pid]
	require.True(t, ok)
	return info.stdio
}

func TestStdioNilStreamsCaptured(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	pid, err := exec.Start(context.Background(), workspaceapi.Cmd{
		Path: "/usr/bin/zls",
	})
	require.NoError(t, err)

	started := lastStart(mock)
	sinkFile, ok := started.Stderr.(*os.File)
	require.True(t, ok, "nil stderr should be replaced by an *os.File")
	assert.Same(t, sinkFile, started.Stdout,
		"both streams should share one sink file")

	_, err = sinkFile.Write([]byte("warn (diag): build failed\n"))
	require.NoError(t, err)

	out := stdioOutput(t, exec, strconv.Itoa(int(pid)))
	assert.Contains(t, out, "warn (diag): build failed")
	assert.Contains(t, out, "stderr")
}

func TestStdioFileStreamPassedThrough(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	ptyStub, err := os.CreateTemp(t.TempDir(), "pty-*")
	require.NoError(t, err)
	defer func() { _ = ptyStub.Close() }()

	pid, err := exec.Start(context.Background(), workspaceapi.Cmd{
		Path:   "/bin/sh",
		Stdout: ptyStub,
		Stderr: ptyStub,
	})
	require.NoError(t, err)

	started := lastStart(mock)
	assert.Same(t, ptyStub, started.Stdout,
		"file-backed streams must reach the child untouched")
	assert.Same(t, ptyStub, started.Stderr)

	sinkPath, _, _ := sinkFor(t, exec, pid).report()
	assert.Empty(t, sinkPath, "no sink file should be created")

	out := stdioOutput(t, exec, strconv.Itoa(int(pid)))
	assert.Contains(t, out, filepath.Base(ptyStub.Name()))
	assert.Contains(t, out, "no output captured")
}

// syncBuffer is deliberately not a workspaceapi.File, so classify
// must tee it rather than pass it through.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
	err error
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return 0, b.err
	}
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestStdioPlainWriterIsTeed(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	var sink syncBuffer
	pid, err := exec.Start(context.Background(), workspaceapi.Cmd{
		Path:   "/bin/sh",
		Stderr: &sink,
	})
	require.NoError(t, err)

	stderr := lastStart(mock).Stderr
	require.NotSame(t, &sink, stderr)
	_, err = stderr.Write([]byte("hello from the child\n"))
	require.NoError(t, err)

	assert.Equal(t, "hello from the child\n", sink.String())
	assert.Contains(t,
		stdioOutput(t, exec, strconv.Itoa(int(pid))),
		"hello from the child")
}

func TestStdioTeeFailsOpen(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	var target syncBuffer
	pid, err := exec.Start(context.Background(), workspaceapi.Cmd{
		Path:   "/bin/sh",
		Stderr: &target,
	})
	require.NoError(t, err)

	sinkFor(t, exec, pid).close()

	stderr := lastStart(mock).Stderr
	n, err := stderr.Write([]byte("still delivered\n"))
	require.NoError(t, err, "a broken sink must not fail the real write")
	assert.Equal(t, len("still delivered\n"), n)
	assert.Equal(t, "still delivered\n", target.String())
}

func TestStdioTeeCapTruncates(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	var target syncBuffer
	pid, err := exec.Start(context.Background(), workspaceapi.Cmd{
		Path:   "/bin/sh",
		Stderr: &target,
	})
	require.NoError(t, err)

	stderr := lastStart(mock).Stderr
	chunk := make([]byte, 1<<20)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for range 5 {
		_, err = stderr.Write(chunk)
		require.NoError(t, err)
	}

	sink := sinkFor(t, exec, pid)
	_, _, truncated := sink.report()
	assert.True(t, truncated)
	sink.mu.Lock()
	written := sink.written
	sink.mu.Unlock()
	assert.Equal(t, int64(stdioSinkCap), written)
	assert.Equal(t, 5<<20, len(target.String()),
		"the real stream is never capped")
	assert.Contains(t,
		stdioOutput(t, exec, strconv.Itoa(int(pid))),
		"capture stopped after")
}

func TestStdioReadableAfterExit(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	pid, err := exec.Start(context.Background(), workspaceapi.Cmd{
		Path: "/usr/bin/zls",
	})
	require.NoError(t, err)

	sinkFile := lastStart(mock).Stderr.(*os.File)
	_, err = sinkFile.Write([]byte("post-mortem line\n"))
	require.NoError(t, err)

	exec.mu.RLock()
	done := exec.processes[pid].done
	exec.mu.RUnlock()
	sink := sinkFor(t, exec, pid)

	mock.exitProcess(pid)
	<-done

	sink.mu.Lock()
	closed := sink.file == nil
	sink.mu.Unlock()
	assert.True(t, closed, "sink handle should be closed on exit")

	assert.Contains(t,
		stdioOutput(t, exec, strconv.Itoa(int(pid))),
		"post-mortem line")
}

func TestStdioCloseRemovesSinkDir(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	_, err := exec.Start(context.Background(), workspaceapi.Cmd{Path: "/bin/sh"})
	require.NoError(t, err)

	exec.stdio.mu.Lock()
	dir := exec.stdio.dir
	exec.stdio.mu.Unlock()
	require.NotEmpty(t, dir)
	require.DirExists(t, dir)

	require.NoError(t, exec.Close())
	_, err = os.Stat(dir)
	assert.True(t, errors.Is(err, os.ErrNotExist))
}

func TestStdioUsageErrors(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	_, err := exec.handleStdio(nil)
	assert.ErrorContains(t, err, "usage: process stdio <pid>")

	_, err = exec.handleStdio([]string{"nope"})
	assert.ErrorContains(t, err, "invalid pid")

	_, err = exec.handleStdio([]string{"4242"})
	assert.ErrorContains(t, err, "unknown pid")
}

func TestCompleteStdio(t *testing.T) {
	mock := newMockExecutor()
	exec := newStdioExecutor(t, mock)

	pid, err := exec.Start(context.Background(), workspaceapi.Cmd{Path: "/bin/sh"})
	require.NoError(t, err)

	ctx := context.Background()
	subs, err := exec.Complete(ctx, "process", []string{"std"})
	require.NoError(t, err)
	defer func() { _ = subs.Close() }()
	gotSubs, err := iterator.ToSlice(ctx, subs)
	require.NoError(t, err)
	assert.Equal(t, []string{"stdio"}, gotSubs)

	pids, err := exec.Complete(ctx, "process", []string{"stdio", ""})
	require.NoError(t, err)
	defer func() { _ = pids.Close() }()
	gotPids, err := iterator.ToSlice(ctx, pids)
	require.NoError(t, err)
	assert.Contains(t, gotPids, strconv.Itoa(int(pid)))
}
