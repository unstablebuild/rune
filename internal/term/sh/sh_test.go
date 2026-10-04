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

package sh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

type mockHandler struct {
	mu       sync.Mutex
	calls    []repl.Command
	handleFn func(context.Context, repl.Command, repl.ProgressWriter) (
		iterator.Iterator[component.Responsive], error,
	)
	completeFn func(
		context.Context, string, []string,
	) (iterator.Iterator[string], error)
}

func (m *mockHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	m.mu.Lock()
	m.calls = append(m.calls, cmd)
	m.mu.Unlock()
	if m.handleFn != nil {
		return m.handleFn(ctx, cmd, pw)
	}
	return iterator.FromSlice[component.Responsive](nil), nil
}

func (m *mockHandler) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if m.completeFn != nil {
		return m.completeFn(ctx, cmd, args)
	}
	return iterator.FromSlice[string](nil), nil
}

type recordingProgressWriter struct {
	mu      sync.Mutex
	calls   int
	updates []progressUpdate
}

type progressUpdate struct {
	progress int64
	total    int64
	units    string
}

func (w *recordingProgressWriter) Progress(progress, total int64, units string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	w.updates = append(w.updates, progressUpdate{
		progress: progress,
		total:    total,
		units:    units,
	})
}

func collectOutput(
	t *testing.T,
	iter iterator.Iterator[component.Responsive],
) ([]string, error) {
	t.Helper()
	ctx := context.Background()
	var lines []string
	for {
		item, ok := iter.Next(ctx)
		if !ok {
			break
		}
		// Resize so that String() returns the correct
		// text (ResponsiveString needs a proper width).
		h := item.Height(pipeWidth)
		if h > 0 {
			item.Resize(pipeWidth, h)
		}
		lines = append(
			lines,
			responsiveToText(item, pipeWidth),
		)
	}
	return lines, iter.Err()
}

func TestHandleCommand(t *testing.T) {
	cases := []struct {
		name               string
		cmd                repl.Command
		handleFn           func(context.Context, repl.Command, repl.ProgressWriter) (iterator.Iterator[component.Responsive], error)
		wantCalls          []repl.Command
		wantCallsUnordered []repl.Command
		wantOut            []string
		wantErr            bool
		wantIterErr        bool
	}{
		{
			name:    "builtin echo",
			cmd:     repl.Command{Name: "echo", Args: []string{"hello"}},
			wantOut: []string{"hello"},
		},
		{
			name: "custom command",
			cmd:  repl.Command{Name: "mycmd", Args: []string{"arg1", "arg2"}},
			wantCalls: []repl.Command{
				{Name: "mycmd", Args: []string{"arg1", "arg2"}},
			},
		},
		{
			name: "pipeline",
			cmd:  repl.Command{Name: "mycmd", Args: []string{"|", "mycmd2"}},
			handleFn: func(_ context.Context, cmd repl.Command, _ repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				if cmd.Name == "mycmd" {
					return iterator.FromSlice([]component.Responsive{
						component.NewResponsiveString(
							"piped-data",
							component.StringResponsiveConfig{},
						),
					}), nil
				}
				return iterator.FromSlice[component.Responsive](nil), nil
			},
			// Pipeline commands run concurrently, so use
			// ElementsMatch (order-insensitive).
			wantCallsUnordered: []repl.Command{
				{Name: "mycmd", Args: []string{}},
				{Name: "mycmd2", Args: []string{}},
			},
		},
		{
			name: "semicolons",
			cmd:  repl.Command{Name: "mycmd;", Args: []string{"mycmd2"}},
			wantCalls: []repl.Command{
				{Name: "mycmd", Args: []string{}},
				{Name: "mycmd2", Args: []string{}},
			},
		},
		{
			name:    "variable expansion",
			cmd:     repl.Command{Name: "FOO=bar;", Args: []string{"echo", "$FOO"}},
			wantOut: []string{"bar"},
		},
		{
			name:    "syntax error",
			cmd:     repl.Command{Name: "echo", Args: []string{`"unterminated`}},
			wantErr: true,
		},
		{
			name: "command error",
			cmd:  repl.Command{Name: "mycmd"},
			handleFn: func(_ context.Context, _ repl.Command, _ repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				return nil, errors.New("boom")
			},
			wantOut:     []string{"boom"},
			wantIterErr: true,
		},
		{
			name: "fallback to next",
			cmd:  repl.Command{Name: "echo", Args: []string{"hello"}},
			handleFn: func(_ context.Context, _ repl.Command, _ repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				return nil, repl.ErrNotFound
			},
			wantOut: []string{"hello"},
		},
		{
			name: "empty command",
			cmd:  repl.Command{},
		},
		{
			name:    "logical AND with true",
			cmd:     repl.Command{Name: "true", Args: []string{"&&", "echo", "yes"}},
			wantOut: []string{"yes"},
		},
		{
			name:    "logical OR with false",
			cmd:     repl.Command{Name: "false", Args: []string{"||", "echo", "fallback"}},
			wantOut: []string{"fallback"},
		},
		{
			name:    "subshell",
			cmd:     repl.Command{Name: "(echo", Args: []string{"hello)"}},
			wantOut: []string{"hello"},
		},
		{
			name: "multi-line output",
			cmd:  repl.Command{Name: "mycmd"},
			handleFn: func(_ context.Context, _ repl.Command, _ repl.ProgressWriter) (
				iterator.Iterator[component.Responsive], error,
			) {
				return iterator.FromSlice([]component.Responsive{
					component.NewResponsiveString("line1", component.StringResponsiveConfig{}),
					component.NewResponsiveString("line2", component.StringResponsiveConfig{}),
					component.NewResponsiveString("line3", component.StringResponsiveConfig{}),
				}), nil
			},
			wantCalls: []repl.Command{
				{Name: "mycmd", Args: []string{}},
			},
			wantOut: []string{"line1", "line2", "line3"},
		},
		{
			name:        "exit status error",
			cmd:         repl.Command{Name: "false"},
			wantIterErr: true,
		},
		{
			name:        "stderr output",
			cmd:         repl.Command{Name: "echo", Args: []string{"err", ">&2"}},
			wantOut:     []string{"err"},
			wantIterErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := &mockHandler{handleFn: tc.handleFn}
			h := New(mock, workspaceapi.URI{})
			ctx := context.Background()
			iter, err := h.HandleCommand(ctx, tc.cmd, repl.NopProgressWriter())
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			defer func() { _ = iter.Close() }()
			out, iterErr := collectOutput(t, iter)
			if tc.wantIterErr {
				assert.Error(t, iterErr)
			} else {
				assert.NoError(t, iterErr)
			}
			if tc.wantOut != nil {
				assert.Equal(t, tc.wantOut, out)
			}
			if tc.wantCalls != nil {
				mock.mu.Lock()
				assert.Equal(t, tc.wantCalls, mock.calls)
				mock.mu.Unlock()
			}
			if tc.wantCallsUnordered != nil {
				mock.mu.Lock()
				assert.ElementsMatch(t, tc.wantCallsUnordered, mock.calls)
				mock.mu.Unlock()
			}
		})
	}
}

func TestCancelStopsCommand(t *testing.T) {
	mock := &mockHandler{
		handleFn: func(_ context.Context, _ repl.Command, _ repl.ProgressWriter) (
			iterator.Iterator[component.Responsive], error,
		) {
			return nil, repl.ErrNotFound
		},
	}
	h := New(mock, workspaceapi.URI{})
	ctx, cancel := context.WithCancel(context.Background())

	// "yes" produces infinite output; the iterator must
	// stop promptly once the context is cancelled.
	iter, err := h.HandleCommand(ctx, repl.Command{
		Name: "yes",
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	// Read a few items to prove the command started.
	for range 3 {
		_, ok := iter.Next(ctx)
		require.True(t, ok, "expected output from yes")
	}

	cancel()

	// After cancellation the iterator must drain within a
	// short deadline. If lineWriter.Write never returns an
	// error the subprocess keeps running and this times out.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, ok := iter.Next(context.Background())
			if !ok {
				return
			}
		}
	}()

	select {
	case <-done:
		// OK — iterator stopped.
	case <-time.After(5 * time.Second):
		t.Fatal("iterator did not stop after context cancellation")
	}
}

func TestLineWriterReturnsErrorOnCancelledContext(t *testing.T) {
	ch := make(chan component.Responsive, 8)
	ctx, cancel := context.WithCancel(context.Background())
	w := &lineWriter{ch: ch, ctx: ctx}

	// Write succeeds before cancellation.
	n, err := w.Write([]byte("hello\n"))
	require.NoError(t, err)
	assert.Equal(t, 6, n)

	cancel()

	// Write returns context error after cancellation.
	_, err = w.Write([]byte("world\n"))
	assert.ErrorIs(t, err, context.Canceled)
}

func TestHandleCommandForwardsProgressWriter(t *testing.T) {
	mock := &mockHandler{
		handleFn: func(_ context.Context, cmd repl.Command, pw repl.ProgressWriter) (
			iterator.Iterator[component.Responsive], error,
		) {
			if cmd.Name == "mycmd" {
				pw.Progress(3, 10, "items")
			}
			return iterator.FromSlice[component.Responsive](nil), nil
		},
	}
	h := New(mock, workspaceapi.URI{})
	pw := &recordingProgressWriter{}

	iter, err := h.HandleCommand(context.Background(), repl.Command{
		Name: "mycmd",
	}, pw)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out, iterErr := collectOutput(t, iter)
	require.NoError(t, iterErr)
	assert.Empty(t, out)

	pw.mu.Lock()
	defer pw.mu.Unlock()
	require.Equal(t, 1, pw.calls)
	assert.Equal(t, []progressUpdate{{
		progress: 3,
		total:    10,
		units:    "items",
	}}, pw.updates)
}

func TestExitStatusError(t *testing.T) {
	mock := &mockHandler{}
	h := New(mock, workspaceapi.URI{})
	ctx := context.Background()
	iter, err := h.HandleCommand(ctx, repl.Command{
		Name: "false",
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	_, iterErr := collectOutput(t, iter)
	require.Error(t, iterErr)
	var exitErr *repl.ExitError
	require.True(t, errors.As(iterErr, &exitErr))
	assert.Equal(t, 1, exitErr.Code)
}

func TestComplete(t *testing.T) {
	called := false
	mock := &mockHandler{
		completeFn: func(
			_ context.Context, cmd string, args []string,
		) (iterator.Iterator[string], error) {
			called = true
			assert.Equal(t, "foo", cmd)
			assert.Equal(t, []string{"bar"}, args)
			return iterator.FromSlice([]string{"baz"}), nil
		},
	}
	h := New(mock, workspaceapi.URI{})
	iter, err := h.Complete(
		context.Background(), "foo", []string{"bar"},
	)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(context.Background(), iter)
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, []string{"baz"}, got)
}

// blockingIter blocks on Next until released or the context is
// cancelled, modelling a completer that streams from a slow background
// scan (e.g. the debugger-launch program completer).
func blockingIter(release <-chan struct{}) iterator.Iterator[string] {
	return iterator.FromFunc(
		func(ctx context.Context) (string, bool, error) {
			select {
			case <-ctx.Done():
				return "", false, ctx.Err()
			case <-release:
				return "", false, nil
			}
		},
		func() error { return nil },
	)
}

func TestCompleteNeverBlocksCallingGoroutine(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		args []string
	}{
		{"arg completion, non-executable", "definitely-not-on-path", []string{""}},
		// "sh" is reliably on PATH, exercising the file-fallback path
		// whose emptiness probe used to block.
		{"arg completion, executable", "sh", []string{""}},
		{"command-name completion", "definitely-not-on-path", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			t.Cleanup(func() { close(release) })
			mock := &mockHandler{
				completeFn: func(
					context.Context, string, []string,
				) (iterator.Iterator[string], error) {
					return blockingIter(release), nil
				},
			}
			h := New(mock, workspaceapi.URI{})

			done := make(chan iterator.Iterator[string], 1)
			go func() {
				it, err := h.Complete(context.Background(), tc.cmd, tc.args)
				require.NoError(t, err)
				done <- it
			}()

			select {
			case it := <-done:
				_ = it.Close()
			case <-time.After(2 * time.Second):
				t.Fatal("Complete blocked on the underlying completer")
			}
		})
	}
}

func TestCompleteFallsBackWhenUnderlyingEmpty(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "alpha.txt"), nil, 0o600))
	t.Chdir(dir)

	mock := &mockHandler{
		completeFn: func(
			context.Context, string, []string,
		) (iterator.Iterator[string], error) {
			return iterator.FromSlice[string](nil), nil
		},
	}
	h := New(mock, workspaceapi.URI{})

	iter, err := h.Complete(context.Background(), "sh", []string{"alph"})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(context.Background(), iter)
	require.NoError(t, err)
	assert.Contains(t, got, "alpha.txt")
}

func TestCompletePrefersUnderlyingOverFallback(t *testing.T) {
	mock := &mockHandler{
		completeFn: func(
			context.Context, string, []string,
		) (iterator.Iterator[string], error) {
			return iterator.FromSlice([]string{"one", "two"}), nil
		},
	}
	h := New(mock, workspaceapi.URI{})

	iter, err := h.Complete(context.Background(), "sh", []string{""})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(context.Background(), iter)
	require.NoError(t, err)
	assert.Equal(t, []string{"one", "two"}, got)
}

func TestInterpreterDirFromWorkspaceURI(t *testing.T) {
	// pwd must report the workspace directory, not the process
	// working directory. Under a macOS .app launch the process cwd
	// is the bundle, so inheriting it would let commands run inside
	// the application and corrupt it.
	wsDir := t.TempDir()
	otherDir := t.TempDir()
	t.Chdir(otherDir)

	uri, err := workspaceapi.CurrentUserHostURI(wsDir)
	require.NoError(t, err)

	mock := &mockHandler{}
	h := New(mock, uri)
	iter, err := h.HandleCommand(context.Background(), repl.Command{
		Name: "pwd",
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out, iterErr := collectOutput(t, iter)
	require.NoError(t, iterErr)
	require.Len(t, out, 1)
	wantDir, err := filepath.EvalSymlinks(wsDir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(out[0]))
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir)
}

func TestInterpreterDirDefaultsToProcessCwd(t *testing.T) {
	// The zero URI leaves the interpreter at the process working
	// directory so non-IDE callers keep their previous behavior.
	dir := t.TempDir()
	t.Chdir(dir)

	mock := &mockHandler{}
	h := New(mock, workspaceapi.URI{})
	iter, err := h.HandleCommand(context.Background(), repl.Command{
		Name: "pwd",
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out, iterErr := collectOutput(t, iter)
	require.NoError(t, iterErr)
	require.Len(t, out, 1)
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(out[0]))
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir)
}

func TestFileCompletion(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.txt"), nil, 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "run.sh"), nil, 0755))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "subdir"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".hidden"), nil, 0644))

	// Create a fake executable so LookPath finds "mycmd".
	binDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "mycmd"), []byte("#!/bin/sh\n"), 0755))
	t.Setenv("PATH", binDir)

	t.Chdir(dir)

	mock := &mockHandler{}
	h := New(mock, workspaceapi.URI{})
	ctx := context.Background()

	cases := []struct {
		name string
		cmd  string
		args []string
		want []string
	}{
		{
			name: "empty prefix lists non-hidden entries",
			cmd:  "mycmd",
			args: []string{""},
			want: []string{"readme.txt", "run.sh", "subdir/"},
		},
		{
			name: "prefix filters entries",
			cmd:  "mycmd",
			args: []string{"r"},
			want: []string{"readme.txt", "run.sh"},
		},
		{
			name: "dot prefix includes hidden files",
			cmd:  "mycmd",
			args: []string{"."},
			want: []string{".hidden"},
		},
		{
			name: "unknown command returns nothing",
			cmd:  "nonexistent",
			args: []string{"r"},
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iter, err := h.Complete(ctx, tc.cmd, tc.args)
			require.NoError(t, err)
			defer func() { _ = iter.Close() }()
			got, err := iterator.ToSlice(ctx, iter)
			require.NoError(t, err)
			if tc.want == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestPathCompletion(t *testing.T) {
	dir := t.TempDir()
	// Create a fake executable.
	fakeCmd := filepath.Join(dir, "fake-cmd")
	err := os.WriteFile(fakeCmd, []byte("#!/bin/sh\n"), 0755)
	require.NoError(t, err)

	// Override PATH to the temp dir.
	t.Setenv("PATH", dir)

	mock := &mockHandler{}
	h := New(mock, workspaceapi.URI{})
	ctx := context.Background()

	// Should find fake-cmd with prefix "fak".
	iter, err := h.Complete(ctx, "fak", nil)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()
	got, err := iterator.ToSlice(ctx, iter)
	require.NoError(t, err)
	assert.Equal(t, []string{"fake-cmd"}, got)

	// Nonexistent prefix should return empty.
	iter2, err := h.Complete(ctx, "nonexistent", nil)
	require.NoError(t, err)
	defer func() { _ = iter2.Close() }()
	got2, err := iterator.ToSlice(ctx, iter2)
	require.NoError(t, err)
	assert.Empty(t, got2)
}

func TestForwardsResponsivesDirectly(t *testing.T) {
	want := []component.Responsive{
		&markerResponsive{id: 1},
		&markerResponsive{id: 2},
		&markerResponsive{id: 3},
	}
	mock := &mockHandler{
		handleFn: func(_ context.Context, _ repl.Command, _ repl.ProgressWriter) (
			iterator.Iterator[component.Responsive], error,
		) {
			return iterator.FromSlice(want), nil
		},
	}
	h := New(mock, workspaceapi.URI{})
	ctx := context.Background()
	iter, err := h.HandleCommand(ctx, repl.Command{Name: "mycmd"}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	var got []component.Responsive
	for {
		item, ok := iter.Next(ctx)
		if !ok {
			break
		}
		got = append(got, item)
	}
	require.NoError(t, iter.Err())
	require.Len(t, got, len(want))
	for i := range want {
		assert.Samef(t, want[i], got[i],
			"item %d was not forwarded as-is", i)
	}
}

// markerResponsive is a distinctive component.Responsive used to
// verify pointer identity passes through sh.commandHandler.
type markerResponsive struct {
	id int
}

func (m *markerResponsive) Height(int) int         { return 1 }
func (m *markerResponsive) Resize(_, _ int)        {}
func (m *markerResponsive) Draw(term.Writer)       {}
func (m *markerResponsive) Dimensions() (int, int) { return 0, 0 }

// stubExecutor records StartCommand invocations and signals the
// command as finished via the Cmd.Watcher so workspaceExec unblocks.
type stubExecutor struct {
	mu       sync.Mutex
	commands []workspaceapi.Cmd
	exitErr  error
}

var _ schemeapi.Executor = (*stubExecutor)(nil)

func (s *stubExecutor) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	s.mu.Lock()
	s.commands = append(s.commands, cmd)
	s.mu.Unlock()
	if cmd.Watcher != nil {
		cmd.Watcher.WatchProcess() <- s.exitErr
	}
	return 1, nil
}

func (s *stubExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (s *stubExecutor) Close() error                                  { return nil }

func TestExternalCommandRoutedThroughExecutor(t *testing.T) {
	// A path that does not exist locally, mimicking a remote (ssh)
	// workspace whose directory only exists on the remote host. With
	// the pre-fix local interp.Dir the constructor would fail with
	// "could not stat".
	uri, err := workspaceapi.CurrentUserHostURI("/nonexistent/remote/workspace")
	require.NoError(t, err)

	exec := &stubExecutor{}
	mock := &mockHandler{
		handleFn: func(_ context.Context, _ repl.Command, _ repl.ProgressWriter) (
			iterator.Iterator[component.Responsive], error,
		) {
			return nil, repl.ErrNotFound
		},
	}
	h := New(mock, uri, WithExecutor(exec))

	iter, err := h.HandleCommand(context.Background(), repl.Command{
		Name: "ls",
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	_, iterErr := collectOutput(t, iter)
	require.NoError(t, iterErr)

	exec.mu.Lock()
	defer exec.mu.Unlock()
	require.Len(t, exec.commands, 1)
	assert.Equal(t, "ls", exec.commands[0].Path)
	assert.Equal(t, "/nonexistent/remote/workspace", exec.commands[0].Dir)
}

func TestExternalCommandFallsBackToLocalWithoutExecutor(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	mock := &mockHandler{
		handleFn: func(_ context.Context, _ repl.Command, _ repl.ProgressWriter) (
			iterator.Iterator[component.Responsive], error,
		) {
			return nil, repl.ErrNotFound
		},
	}
	h := New(mock, workspaceapi.URI{})

	iter, err := h.HandleCommand(context.Background(), repl.Command{
		Name: "pwd",
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out, iterErr := collectOutput(t, iter)
	require.NoError(t, iterErr)
	require.Len(t, out, 1)
	wantDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(out[0]))
	require.NoError(t, err)
	assert.Equal(t, wantDir, gotDir)
}
