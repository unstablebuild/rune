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

package llamaserver

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
)

// newTestService builds a Service wired to the given executor with fast
// lifecycle timers so tests run quickly.
func newTestService(t *testing.T, exec *fakeExecutor, cfg Config) (*Service, *recordingNotifications) {
	t.Helper()
	notis := &recordingNotifications{}
	svc := New(cfg, exec, NewFixedLocator("/usr/bin/llama-server"), notis)
	t.Cleanup(func() { _ = svc.Close() })
	return svc, notis
}

func TestService_Acquire_StartsOnceAndReuses(t *testing.T) {
	exec := &fakeExecutor{}
	svc, _ := newTestService(t, exec, Config{StartupTimeout: 5 * time.Second})

	m := testModel("a")
	proc1, rel1, err := svc.acquire(context.Background(), m)
	require.NoError(t, err)
	require.NotNil(t, proc1)
	rel1()

	proc2, rel2, err := svc.acquire(context.Background(), m)
	require.NoError(t, err)
	rel2()

	assert.Same(t, proc1, proc2, "same model reuses the running server")
	assert.Equal(t, 1, exec.startCount(), "server started exactly once")
}

func TestService_Acquire_DistinctModelsStartSeparateServers(t *testing.T) {
	exec := &fakeExecutor{}
	svc, _ := newTestService(t, exec, Config{StartupTimeout: 5 * time.Second, MaxServers: 4})

	_, rel1, err := svc.acquire(context.Background(), testModel("a"))
	require.NoError(t, err)
	rel1()
	_, rel2, err := svc.acquire(context.Background(), testModel("b"))
	require.NoError(t, err)
	rel2()

	assert.Equal(t, 2, exec.startCount())
}

func TestService_IdleTimeout_StopsAndEvicts(t *testing.T) {
	exec := &fakeExecutor{}
	svc, _ := newTestService(t, exec, Config{
		StartupTimeout: 5 * time.Second,
		IdleTimeout:    30 * time.Millisecond,
	})

	_, rel, err := svc.acquire(context.Background(), testModel("a"))
	require.NoError(t, err)
	rel() // arms the idle timer

	require.Eventually(t, func() bool {
		svc.mu.Lock()
		n := len(svc.servers)
		svc.mu.Unlock()
		return n == 0
	}, 2*time.Second, 10*time.Millisecond, "idle server should be evicted")

	require.Eventually(t, func() bool {
		return exec.runningCount() == 0
	}, 2*time.Second, 10*time.Millisecond, "idle server process should be stopped")
}

func TestService_MaxServers_EvictsLRU(t *testing.T) {
	exec := &fakeExecutor{}
	svc, _ := newTestService(t, exec, Config{
		StartupTimeout: 5 * time.Second,
		MaxServers:     1,
		IdleTimeout:    time.Hour,
	})

	_, rel1, err := svc.acquire(context.Background(), testModel("a"))
	require.NoError(t, err)
	rel1() // idle, evictable

	// Second distinct model must evict the first (cap = 1).
	_, rel2, err := svc.acquire(context.Background(), testModel("b"))
	require.NoError(t, err)
	rel2()

	svc.mu.Lock()
	n := len(svc.servers)
	svc.mu.Unlock()
	assert.Equal(t, 1, n, "cap enforced")
	assert.Equal(t, 2, exec.startCount(), "second model started after eviction")
}

func TestService_Acquire_ServerNotInstalled(t *testing.T) {
	exec := &fakeExecutor{}
	notis := &recordingNotifications{}
	svc := New(Config{}, exec, NewFixedLocator(""), notis)
	t.Cleanup(func() { _ = svc.Close() })

	_, _, err := svc.acquire(context.Background(), testModel("a"))
	assert.ErrorIs(t, err, ErrServerNotInstalled)
	assert.Equal(t, 0, exec.startCount(), "no server started when binary missing")
}

func TestService_Acquire_StartupTimeout(t *testing.T) {
	exec := &fakeExecutor{healthDelay: time.Hour} // never becomes healthy
	svc, _ := newTestService(t, exec, Config{StartupTimeout: 50 * time.Millisecond})

	_, _, err := svc.acquire(context.Background(), testModel("a"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")
}

func TestService_ProgressReporting(t *testing.T) {
	exec := &fakeExecutor{
		emitLines: []string{
			"load_tensors: loading model tensors 10%",
			"load_tensors: loading model tensors 60%",
		},
	}
	svc, notis := newTestService(t, exec, Config{StartupTimeout: 5 * time.Second})

	_, rel, err := svc.acquire(context.Background(), testModel("a"))
	require.NoError(t, err)
	rel()

	require.Eventually(t, func() bool {
		for _, rec := range notis.progressRecords() {
			if rec.progress == 100 && rec.total == 100 {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "progress should reach (100,100)")

	for _, rec := range notis.progressRecords() {
		assert.Positive(t, rec.total, "total must never be zero")
		assert.LessOrEqual(t, rec.progress, rec.total)
	}

	assert.Contains(t, notis.levels(), browserapi.LevelSuccess,
		"a success notification is emitted when the model becomes ready")
}

func TestService_ProgressStreamsAllOutputLines(t *testing.T) {
	exec := &fakeExecutor{
		emitLines: []string{
			"llama_model_loader: loaded meta data",
			"  load_tensors: loading model tensors 40%  ",
			"llama_context: constructing llama_context",
		},
	}
	svc, notis := newTestService(t, exec, Config{StartupTimeout: 5 * time.Second})

	_, rel, err := svc.acquire(context.Background(), testModel("a"))
	require.NoError(t, err)
	rel()

	require.Eventually(t, func() bool {
		msgs := notis.progressMessages()
		return slices.Contains(msgs, "llama_model_loader: loaded meta data") &&
			slices.Contains(msgs, "llama_context: constructing llama_context")
	}, 2*time.Second, 10*time.Millisecond,
		"non-percentage output lines should stream to the notification message")

	assert.Contains(t, notis.progressMessages(),
		"load_tensors: loading model tensors 40%",
		"percentage lines are trimmed of surrounding whitespace")

	for _, rec := range notis.progressRecords() {
		assert.Positive(t, rec.total, "total must never be zero")
		assert.LessOrEqual(t, rec.progress, rec.total)
	}
}

func TestService_Close_StopsAllServers(t *testing.T) {
	exec := &fakeExecutor{}
	notis := &recordingNotifications{}
	svc := New(Config{StartupTimeout: 5 * time.Second, MaxServers: 4}, exec,
		NewFixedLocator("/usr/bin/llama-server"), notis)

	_, rel1, err := svc.acquire(context.Background(), testModel("a"))
	require.NoError(t, err)
	rel1()
	_, rel2, err := svc.acquire(context.Background(), testModel("b"))
	require.NoError(t, err)
	rel2()

	require.NoError(t, svc.Close())
	require.Eventually(t, func() bool {
		return exec.runningCount() == 0
	}, 2*time.Second, 10*time.Millisecond, "all servers stopped on Close")

	_, _, err = svc.acquire(context.Background(), testModel("a"))
	assert.ErrorIs(t, err, ErrServerClosed)
}

func TestService_Crash_RestartsOnNextAcquire(t *testing.T) {
	exec := &fakeExecutor{}
	svc, _ := newTestService(t, exec, Config{StartupTimeout: 5 * time.Second, IdleTimeout: time.Hour})

	m := testModel("a")
	proc1, rel1, err := svc.acquire(context.Background(), m)
	require.NoError(t, err)
	rel1()

	// Simulate a crash: the process exits on its own (watcher fires) while
	// its lifecycle context is still live, which the watcher must observe
	// and evict.
	exec.crash(1)
	require.Eventually(t, func() bool {
		svc.mu.Lock()
		_, present := svc.servers[m.key()]
		svc.mu.Unlock()
		return !present
	}, 2*time.Second, 10*time.Millisecond, "crashed server evicted")

	proc2, rel2, err := svc.acquire(context.Background(), m)
	require.NoError(t, err)
	rel2()
	assert.NotSame(t, proc1, proc2, "next acquire starts a fresh server")
	assert.Equal(t, 2, exec.startCount())
}
