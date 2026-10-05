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

package exoeditor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
)

// countingNotifications records how many warn-level notifications the
// watcher emitted so tests can assert the missing-file case does not
// surface a "watch ... no such file" error to the user.
type countingNotifications struct {
	mu   sync.Mutex
	warn int
}

func (n *countingNotifications) Notify(
	level browserapi.NotificationLevel, _ string, _ ...any,
) (string, error) {
	if level == browserapi.LevelWarn {
		n.mu.Lock()
		n.warn++
		n.mu.Unlock()
	}
	return "", nil
}

func (n *countingNotifications) NotifyOnce(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return "", nil
}

func (n *countingNotifications) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	return nil
}

func (n *countingNotifications) warnCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.warn
}

// syncReloader records reload URIs under a mutex; the watcher goroutine
// and the test goroutine both touch it.
type syncReloader struct {
	mu    sync.Mutex
	calls []workspaceapi.URI
}

func (r *syncReloader) Reload(uri workspaceapi.URI) error {
	r.mu.Lock()
	r.calls = append(r.calls, uri)
	r.mu.Unlock()
	return nil
}

func (r *syncReloader) reloadCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func newFileSchemeWorkspace(t *testing.T, dir string) (workspace.Workspace, schemeapi.Scheme) {
	t.Helper()
	wsURI, err := workspaceapi.ParseURI("file://" + dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), wsURI)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	ws := workspace.NewSchemeWorkspace(wsURI, scheme, func(fn func()) bool { fn(); return true })
	return ws, scheme
}

func TestStartWatcherFallsBackToParentDirForMissingFile(t *testing.T) {
	dir := t.TempDir()
	ws, _ := newFileSchemeWorkspace(t, dir)

	fpath := filepath.Join(dir, "new.txt")
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	notes := &countingNotifications{}
	rel := &syncReloader{}
	h := &editorHandler{
		resource:         fileURI,
		cwd:              ws,
		notifications:    notes,
		scheduleNextTick: func(fn func()) bool { fn(); return true },
		reloader:         rel,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.startWatcher(ctx)

	require.Eventually(t, func() bool { return h.watchActive.Load() }, time.Second,
		10*time.Millisecond, "watcher must stay active for a not-yet-created file")
	require.Equal(t, 0, notes.warnCount(),
		"opening a missing file must not surface a watch warning")

	require.NoError(t, os.WriteFile(fpath, []byte("hello\n"), 0o644))

	require.Eventually(t, func() bool {
		return rel.reloadCount() > 0
	}, 5*time.Second, 10*time.Millisecond,
		"creating the watched file must trigger a reload")

	rel.mu.Lock()
	first := rel.calls[0]
	rel.mu.Unlock()
	assert.Equal(t, fileURI, first,
		"reload must target the resource URI, not the symlink-resolved event path")
}

// gatedWatchWorkspace delays Watch until gate is closed (or returns
// watchErr immediately if set), modelling the Linux notify backend whose
// recursive-watch fallback walks the whole tree before returning.
type gatedWatchWorkspace struct {
	workspace.Workspace
	gate     <-chan struct{}
	watchErr error
}

func (w *gatedWatchWorkspace) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	if w.watchErr != nil {
		return 0, w.watchErr
	}
	if w.gate != nil {
		<-w.gate
	}
	return w.Workspace.Watch(path, c, events...)
}

func TestStartWatcherDoesNotBlockOnSlowWatch(t *testing.T) {
	dir := t.TempDir()
	ws, _ := newFileSchemeWorkspace(t, dir)

	fpath := filepath.Join(dir, "exists.txt")
	require.NoError(t, os.WriteFile(fpath, []byte("initial\n"), 0o644))
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	gate := make(chan struct{})
	rel := &syncReloader{}
	h := &editorHandler{
		resource:         fileURI,
		cwd:              &gatedWatchWorkspace{Workspace: ws, gate: gate},
		notifications:    &countingNotifications{},
		scheduleNextTick: func(fn func()) bool { fn(); return true },
		reloader:         rel,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	done := make(chan struct{})
	go func() {
		h.startWatcher(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("startWatcher blocked on a slow Watch")
	}

	// Releasing the gate lets the watch arm; a write then reloads.
	close(gate)
	require.Eventually(t, func() bool { return h.watchActive.Load() }, time.Second,
		10*time.Millisecond, "watch must arm once the slow Watch returns")

	require.NoError(t, os.WriteFile(fpath, []byte("updated\n"), 0o644))
	require.Eventually(t, func() bool {
		return rel.reloadCount() > 0
	}, 5*time.Second, 10*time.Millisecond,
		"writing the watched file must trigger a reload once the watch arms")
}

func TestStartWatcherSurfacesWatchError(t *testing.T) {
	dir := t.TempDir()
	ws, _ := newFileSchemeWorkspace(t, dir)

	fpath := filepath.Join(dir, "exists.txt")
	require.NoError(t, os.WriteFile(fpath, []byte("initial\n"), 0o644))
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	notes := &countingNotifications{}
	h := &editorHandler{
		resource:         fileURI,
		cwd:              &gatedWatchWorkspace{Workspace: ws, watchErr: errors.New("watch exploded")},
		notifications:    notes,
		scheduleNextTick: func(fn func()) bool { fn(); return true },
		reloader:         &syncReloader{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.startWatcher(ctx)

	require.Eventually(t, func() bool {
		return notes.warnCount() > 0
	}, time.Second, 10*time.Millisecond,
		"a failed watch must surface a warn notification")
	assert.False(t, h.watchActive.Load(), "a failed watch must not be marked active")
}

func TestStartWatcherDirModeIgnoresSiblingFiles(t *testing.T) {
	dir := t.TempDir()
	ws, _ := newFileSchemeWorkspace(t, dir)

	fpath := filepath.Join(dir, "target.txt")
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	rel := &syncReloader{}
	h := &editorHandler{
		resource:         fileURI,
		cwd:              ws,
		notifications:    &countingNotifications{},
		scheduleNextTick: func(fn func()) bool { fn(); return true },
		reloader:         rel,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.startWatcher(ctx)
	require.Eventually(t, func() bool { return h.watchActive.Load() }, time.Second,
		10*time.Millisecond, "watch must arm")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.txt"),
		[]byte("noise\n"), 0o644))

	// Give the watcher a chance to (incorrectly) react before
	// asserting it stayed quiet.
	assert.Never(t, func() bool {
		return rel.reloadCount() > 0
	}, 500*time.Millisecond, 25*time.Millisecond,
		"writes to sibling files must not reload the open file")
}

func TestStartWatcherDirectModeReloadsResourceURI(t *testing.T) {
	dir := t.TempDir()
	ws, _ := newFileSchemeWorkspace(t, dir)

	fpath := filepath.Join(dir, "exists.txt")
	require.NoError(t, os.WriteFile(fpath, []byte("initial\n"), 0o644))
	fileURI, err := workspaceapi.ParseURI("file://" + fpath)
	require.NoError(t, err)

	rel := &syncReloader{}
	h := &editorHandler{
		resource:         fileURI,
		cwd:              ws,
		notifications:    &countingNotifications{},
		scheduleNextTick: func(fn func()) bool { fn(); return true },
		reloader:         rel,
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.startWatcher(ctx)
	require.Eventually(t, func() bool { return h.watchActive.Load() }, time.Second,
		10*time.Millisecond, "watch must arm")

	require.NoError(t, os.WriteFile(fpath, []byte("updated\n"), 0o644))

	require.Eventually(t, func() bool {
		return rel.reloadCount() > 0
	}, 5*time.Second, 10*time.Millisecond,
		"writing the watched file must trigger a reload")

	rel.mu.Lock()
	first := rel.calls[0]
	rel.mu.Unlock()
	assert.Equal(t, fileURI, first,
		"reload must target the resource URI, not the symlink-resolved event path")
}
