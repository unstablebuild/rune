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

package agentools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func TestSessionManager_Create_and_exit(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	defer func() { _ = mgr.Close() }()

	sess, err := mgr.Create("echo hello", t.TempDir(), "", false, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, sess.ID, minSessionID)
	assert.Less(t, sess.ID, maxSessionID)

	exited := sess.Wait(5 * time.Second)
	assert.True(t, exited)
	assert.Equal(t, 0, sess.ExitCode())
	assert.Contains(t, sess.Output(), "hello")
}

func TestSessionManager_Get(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	defer func() { _ = mgr.Close() }()

	sess, err := mgr.Create("echo ok", t.TempDir(), "", false, nil)
	require.NoError(t, err)

	got, ok := mgr.Get(sess.ID)
	assert.True(t, ok)
	assert.Equal(t, sess.ID, got.ID)

	_, ok = mgr.Get(99999)
	assert.False(t, ok)
}

func TestSessionManager_nonzero_exit(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	defer func() { _ = mgr.Close() }()

	sess, err := mgr.Create("exit 42", t.TempDir(), "", false, nil)
	require.NoError(t, err)

	exited := sess.Wait(5 * time.Second)
	assert.True(t, exited)
	assert.Equal(t, 42, sess.ExitCode())
}

func TestSessionManager_long_running_yield(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	defer func() { _ = mgr.Close() }()

	sess, err := mgr.Create("sleep 10", t.TempDir(), "", false, nil)
	require.NoError(t, err)

	// Process should still be running after a short wait.
	exited := sess.Wait(100 * time.Millisecond)
	assert.False(t, exited)
	assert.False(t, sess.Exited())
	assert.Greater(t, sess.WallTime(), 0.0)
}

func TestSessionManager_WriteStdin(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	defer func() { _ = mgr.Close() }()

	// cat reads from stdin and echoes to stdout.
	sess, err := mgr.Create("cat", t.TempDir(), "", false, nil)
	require.NoError(t, err)

	err = sess.WriteStdin([]byte("hello from stdin\n"))
	require.NoError(t, err)

	// Close stdin to let cat exit.
	_ = sess.stdin.Close()
	exited := sess.Wait(5 * time.Second)
	assert.True(t, exited)
	assert.Contains(t, sess.Output(), "hello from stdin")
}

func TestSessionManager_Close_kills_sessions(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)

	sess, err := mgr.Create("sleep 60", t.TempDir(), "", false, nil)
	require.NoError(t, err)
	assert.False(t, sess.Exited())

	_ = mgr.Close()

	// After Close, the session should exit (context cancelled + SIGKILL).
	select {
	case <-sess.done:
		// OK
	case <-time.After(5 * time.Second):
		t.Fatal("session did not exit after Close")
	}
}

func TestSessionManager_eviction(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	defer func() { _ = mgr.Close() }()

	dir := t.TempDir()

	// Fill up to max.
	sessions := make([]*Session, maxSessions)
	for i := range maxSessions {
		sess, err := mgr.Create("echo "+strings.Repeat("x", i), dir, "", false, nil)
		require.NoError(t, err, "session %d", i)
		sessions[i] = sess
	}

	// Wait for first session to complete so it's evictable.
	sessions[0].Wait(5 * time.Second)
	require.True(t, sessions[0].Exited())

	// Creating one more should evict the oldest completed.
	sess, err := mgr.Create("echo new", dir, "", false, nil)
	require.NoError(t, err)
	require.NotNil(t, sess)

	// Original session 0 should no longer be gettable.
	_, ok := mgr.Get(sessions[0].ID)
	assert.False(t, ok)
}

func TestSessionManager_closed_manager(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	_ = mgr.Close()

	_, err := mgr.Create("echo hi", t.TempDir(), "", false, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "session manager closed")
}

func TestSessionManager_custom_shell(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	defer func() { _ = mgr.Close() }()

	sess, err := mgr.Create("echo shell_test", t.TempDir(), "sh", false, nil)
	require.NoError(t, err)

	exited := sess.Wait(5 * time.Second)
	assert.True(t, exited)
	assert.Contains(t, sess.Output(), "shell_test")
}

func TestSessionManager_Create_env(t *testing.T) {
	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "nil adds nothing to the host environment",
		},
		{
			name: "explicit is passed through",
			env:  []string{"FOO=bar"},
			want: []string{"FOO=bar"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("PATH", "/stale")
			t.Setenv("RUNE_SOCKET", "unix:///tmp/rune.sock")

			recorded := make(chan workspaceapi.Cmd, 1)
			rec := &recordingExec{startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
				recorded <- cmd
				cmd.Watcher.WatchProcess() <- nil
				return 1, nil
			}}
			mgr := NewSessionManager(context.Background(), rec, nil)
			defer func() { _ = mgr.Close() }()

			_, err := mgr.Create("true", t.TempDir(), "", false, tt.env)
			require.NoError(t, err)

			assert.Equal(t, tt.want, (<-recorded).Env)
		})
	}
}

func TestSession_WallTime(t *testing.T) {
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	defer func() { _ = mgr.Close() }()

	sess, err := mgr.Create("sleep 0.1", t.TempDir(), "", false, nil)
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	assert.Greater(t, sess.WallTime(), 0.01)
}
