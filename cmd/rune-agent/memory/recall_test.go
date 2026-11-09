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

package memory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
)

// stubFileSystem implements workspaceapi.FileSystem for testing Available.
type stubFileSystem struct {
	statErr map[string]error
}

func (s *stubFileSystem) Stat(path string) (os.FileInfo, error) {
	if err, ok := s.statErr[path]; ok {
		return nil, err
	}
	return nil, nil // exists
}

func (s *stubFileSystem) URI(string) (workspaceapi.URI, error) {
	return workspaceapi.URI{}, nil
}
func (s *stubFileSystem) OpenFile(string, int, os.FileMode) (workspaceapi.File, error) {
	return nil, nil
}
func (s *stubFileSystem) Remove(string) error                   { return nil }
func (s *stubFileSystem) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }
func (s *stubFileSystem) MkdirAll(string, os.FileMode) error    { return nil }

func TestAvailable(t *testing.T) {
	t.Run("true when go.mod exists", func(t *testing.T) {
		fs := &stubFileSystem{statErr: map[string]error{}}
		assert.True(t, Available(fs, "/data/memories"))
	})

	t.Run("false when go.mod missing", func(t *testing.T) {
		fs := &stubFileSystem{statErr: map[string]error{
			"/data/memories/go.mod": os.ErrNotExist,
		}}
		assert.False(t, Available(fs, "/data/memories"))
	})

	t.Run("false on stat error", func(t *testing.T) {
		fs := &stubFileSystem{statErr: map[string]error{
			"/data/memories/go.mod": fmt.Errorf("permission denied"),
		}}
		assert.False(t, Available(fs, "/data/memories"))
	})
}

// mockExecutor records Start calls and simulates process completion.
type mockExecutor struct {
	startFn func(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error)
}

func (m *mockExecutor) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return m.startFn(ctx, cmd)
}

func (m *mockExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (m *mockExecutor) Close() error                                  { return nil }

func TestRecall_NoMatch(t *testing.T) {
	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			// Empty stdout — no memories matched.
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	out, err := Recall(context.Background(), exec, "/data/memories", RecallInput{
		Task: "fix the bug",
	})
	require.NoError(t, err)
	assert.Equal(t, "", out)
}

func TestRecall_WithOutput(t *testing.T) {
	const want = "[mem-1] Always run tests before committing"

	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			// Verify args are built correctly.
			args := strings.Join(cmd.Args, " ")
			assert.Contains(t, args, "-task")
			assert.Contains(t, args, "-files")
			assert.Contains(t, args, "-budget")

			_, _ = io.WriteString(cmd.Stdout, want+"\n")
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	out, err := Recall(context.Background(), exec, "/data/memories", RecallInput{
		Files: []string{"agent.go", "handler.go"},
		Task:  "fix deadlock",
	})
	require.NoError(t, err)
	assert.Equal(t, want, out)
}

func TestRecall_ProcessError(t *testing.T) {
	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			_, _ = fmt.Fprintln(cmd.Stderr.(*bytes.Buffer), "compilation error")
			cmd.Watcher.WatchProcess() <- fmt.Errorf("exit status 1")
			return 0, nil
		},
	}

	_, err := Recall(context.Background(), exec, "/data/memories", RecallInput{
		Task: "anything",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
}

func TestRecall_Timeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			// Never send on watcher — simulates a hung process.
			return 0, nil
		},
	}

	_, err := Recall(ctx, exec, "/data/memories", RecallInput{Task: "test"})
	require.Error(t, err)
}

func TestRecall_DefaultBudget(t *testing.T) {
	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			args := strings.Join(cmd.Args, " ")
			assert.Contains(t, args, "-budget 10")
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	_, _ = Recall(context.Background(), exec, "/data/memories", RecallInput{
		Task: "test",
	})
}

func TestFetchConversation(t *testing.T) {
	const transcript = "### user\n\nfix the bug\n\n### assistant\n\nDone."

	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			args := strings.Join(cmd.Args, " ")
			assert.Contains(t, args, "-conversation mem-123")

			_, _ = io.WriteString(cmd.Stdout, transcript+"\n")
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	out, err := FetchConversation(context.Background(), exec, "/data/memories", "mem-123")
	require.NoError(t, err)
	assert.Equal(t, transcript, out)
}

func TestFetchConversation_NotFound(t *testing.T) {
	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			_, _ = fmt.Fprintln(cmd.Stderr.(*bytes.Buffer), "memory not found: bad-id")
			cmd.Watcher.WatchProcess() <- fmt.Errorf("exit status 1")
			return 0, nil
		},
	}

	_, err := FetchConversation(context.Background(), exec, "/data/memories", "bad-id")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exit status 1")
}

func TestRecaller_NotAvailable(t *testing.T) {
	fs := &stubFileSystem{statErr: map[string]error{
		"/data/memory/go.mod": os.ErrNotExist,
	}}
	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			t.Error("executor should not be called when workspace is unavailable")
			return 0, nil
		},
	}

	r := NewRecaller(fs, exec, "/data/memory")
	_, err := r.Recall(context.Background(), nil, "fix the bug", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available")
}

func TestRecaller_Available(t *testing.T) {
	const raw = "[mem-1] Always test"

	fs := &stubFileSystem{statErr: map[string]error{}}
	exec := &mockExecutor{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			_, _ = io.WriteString(cmd.Stdout, raw+"\n")
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	r := NewRecaller(fs, exec, "/data/memory")
	memories, err := r.Recall(context.Background(), nil, "fix the bug", "")
	require.NoError(t, err)
	require.Len(t, memories, 1)
	assert.Equal(t, "mem-1", memories[0].ID)
	assert.Equal(t, "Always test", memories[0].Content)
}

func TestParseMemories(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []agent.Memory
	}{
		{
			name: "empty string",
			raw:  "",
			want: nil,
		},
		{
			name: "single memory",
			raw:  "[handler-refactor] (coding) When renaming handler methods, update mock registry.",
			want: []agent.Memory{
				{ID: "handler-refactor", Content: "(coding) When renaming handler methods, update mock registry."},
			},
		},
		{
			name: "multiple memories",
			raw: "[handler-refactor] (coding) When renaming handler methods, update mock registry.\n" +
				"[iterator-bug] (debugging) Iterator.Close must be called before reuse.",
			want: []agent.Memory{
				{ID: "handler-refactor", Content: "(coding) When renaming handler methods, update mock registry."},
				{ID: "iterator-bug", Content: "(debugging) Iterator.Close must be called before reuse."},
			},
		},
		{
			name: "skips blank lines",
			raw:  "\n[foo] bar\n\n[baz] qux\n",
			want: []agent.Memory{
				{ID: "foo", Content: "bar"},
				{ID: "baz", Content: "qux"},
			},
		},
		{
			name: "skips malformed lines",
			raw:  "no bracket here\n[good] content\nbad [] stuff",
			want: []agent.Memory{
				{ID: "good", Content: "content"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseMemories(tt.raw)
			assert.Equal(t, tt.want, got)
		})
	}
}

func (*mockExecutor) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}
