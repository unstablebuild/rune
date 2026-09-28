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

package ide

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
)

// outOfRootPollInterval bounds how long an out-of-root tab can show stale
// content after an external change.
var outOfRootPollInterval = 2 * time.Second

var outOfRootTabEvents = []textapi.EventType{
	textapi.EventTypeOpen,
	textapi.EventTypeClose,
}

type statScheme interface {
	Stat(path string) (os.FileInfo, error)
}

// outOfRootWatcher detects external changes to files of tabs that live
// outside the workspace root, which the workspace's recursive watcher does
// not cover.
//
// It polls with Stat rather than subscribing to a filesystem watch: some
// watcher backends (FSEvents) are recursive and filter events after the
// fact, so watching the parent of a file in $HOME would stream every change
// under $HOME. The backend belongs to the workspace host, which may differ
// from the local OS, so it cannot be special-cased. Remote workspaces are
// not polled at all, since every Stat would be a round trip.
//
// Changes are handed to handleFSChange on the host scheduler, not published
// as editor events, so LSP servers and extensions keep seeing only in-root
// filesystem events. handleFSChange also drops them for externally-managed
// editors, which install a watcher of their own.
type outOfRootWatcher struct {
	ex       *ex
	scheme   statScheme
	root     workspaceapi.URI
	rootPath string
	interval time.Duration
	enabled  bool

	mu     sync.Mutex
	tabs   map[string]*outOfRootTab
	cancel context.CancelFunc
	closed bool

	polls sync.WaitGroup
}

type outOfRootTab struct {
	uri   workspaceapi.URI
	state fileState
}

// fileState is the last observation of a file. A state that is not known
// (the initial Stat failed) is never compared, so a transient failure cannot
// be mistaken for the file having been created or removed.
type fileState struct {
	known   bool
	exists  bool
	modTime time.Time
	size    int64
}

func newOutOfRootWatcher(
	e *ex, m workspace.Workspace, root workspaceapi.URI,
) *outOfRootWatcher {
	ctx, cancel := context.WithCancel(context.Background())
	w := &outOfRootWatcher{
		ex:       e,
		scheme:   m,
		root:     root,
		rootPath: resolvePath(root.Path()),
		interval: outOfRootPollInterval,
		enabled:  !isRemoteWorkspace(m),
		tabs:     make(map[string]*outOfRootTab),
		cancel:   cancel,
	}
	if w.enabled {
		w.startPolling(ctx)
	}
	return w
}

func isRemoteWorkspace(m workspace.Workspace) bool {
	rs, ok := m.(workspace.RemoteScheme)
	return ok && rs.OnDisconnect() != nil
}

// resolvePath resolves symlinks so a tab opened through a link into the root
// is not mistaken for an out-of-root file and reloaded by both watchers.
// A file that does not exist yet is resolved through its directory.
func resolvePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	dir, name := filepath.Split(path)
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return filepath.Join(resolved, name)
	}
	return filepath.Clean(path)
}

// Handle implements textapi.EventHandler.
func (w *outOfRootWatcher) Handle(_ context.Context, ev textapi.Event) bool {
	switch ev.Type {
	case textapi.EventTypeOpen:
		if w.enabled && w.isOutOfRoot(ev.URI) {
			w.add(ev.URI)
		}
	case textapi.EventTypeClose:
		w.remove(ev.URI)
	}
	return false
}

func (w *outOfRootWatcher) isOutOfRoot(uri workspaceapi.URI) bool {
	// A URI on another scheme or host is not reachable through this
	// workspace, and internal tabs (memory://, terminals) have no file.
	sameRoot, err := workspaceapi.WithPath(uri, w.root.Path())
	if err != nil || !sameRoot.Equal(w.root) || !filepath.IsAbs(uri.Path()) {
		return false
	}
	rel, err := filepath.Rel(w.rootPath, resolvePath(uri.Path()))
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (w *outOfRootWatcher) add(uri workspaceapi.URI) {
	state := w.stat(uri)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.tabs[uri.String()] = &outOfRootTab{uri: uri, state: state}
}

func (w *outOfRootWatcher) remove(uri workspaceapi.URI) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.tabs, uri.String())
}

func (w *outOfRootWatcher) startPolling(ctx context.Context) {
	w.polls.Add(1)
	go debug.CapturePanicReport(func() {
		defer w.polls.Done()
		w.poll(ctx)
	})
}

func (w *outOfRootWatcher) poll(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.pollOnce(ctx)
		}
	}
}

// pollOnce stats every tracked file off the host goroutine and reports only
// the files whose state actually changed.
func (w *outOfRootWatcher) pollOnce(ctx context.Context) {
	w.mu.Lock()
	uris := make([]workspaceapi.URI, 0, len(w.tabs))
	for _, tab := range w.tabs {
		uris = append(uris, tab.uri)
	}
	w.mu.Unlock()

	for _, uri := range uris {
		state, err := w.statErr(uri)
		if err != nil {
			// Transient: the last known state is kept, so the change is
			// reported by the first poll that succeeds.
			w.ex.log(log.DebugLevel, "poll out-of-root tab %s: %v", uri, err)
			continue
		}
		flag, changed := w.observe(uri, state)
		if !changed {
			continue
		}
		w.report(ctx, uri, flag)
	}
}

// observe records the new state of uri and reports how it changed.
func (w *outOfRootWatcher) observe(
	uri workspaceapi.URI, state fileState,
) (schemeapi.Event, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	tab, ok := w.tabs[uri.String()]
	if !ok {
		return 0, false
	}
	flag, changed := fileChange(tab.state, state)
	tab.state = state
	return flag, changed
}

// report hands the change to the host scheduler, where handleFSChange runs
// under the same lock as the workspace watcher's own dispatch.
func (w *outOfRootWatcher) report(
	ctx context.Context, uri workspaceapi.URI, flag schemeapi.Event,
) {
	w.ex.sched(func() {
		if ctx.Err() != nil || w.ex.closed {
			return
		}
		handleFSChange(w.ex, flag, uri, nil)
	})
}

func fileChange(prev, cur fileState) (schemeapi.Event, bool) {
	if !prev.known {
		return 0, false
	}
	switch {
	case !prev.exists && cur.exists:
		return schemeapi.Create, true
	case prev.exists && !cur.exists:
		return schemeapi.Remove, true
	case cur.exists && (!prev.modTime.Equal(cur.modTime) || prev.size != cur.size):
		return schemeapi.Write, true
	}
	return 0, false
}

func (w *outOfRootWatcher) stat(uri workspaceapi.URI) fileState {
	state, err := w.statErr(uri)
	if err != nil {
		w.ex.log(log.DebugLevel, "stat out-of-root tab %s: %v", uri, err)
	}
	return state
}

func (w *outOfRootWatcher) statErr(uri workspaceapi.URI) (fileState, error) {
	info, err := w.scheme.Stat(uri.Path())
	if errors.Is(err, os.ErrNotExist) {
		return fileState{known: true}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	return fileState{
		known:   true,
		exists:  true,
		modTime: info.ModTime(),
		size:    info.Size(),
	}, nil
}

// Close stops polling. It does not wait for the poll goroutine, which may be
// blocked on the host scheduler Close is called from.
func (w *outOfRootWatcher) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	clear(w.tabs)
	w.cancel()
}

// wait blocks until the poll goroutine has exited. It should be used for
// testing only.
func (w *outOfRootWatcher) wait() {
	w.polls.Wait()
}
