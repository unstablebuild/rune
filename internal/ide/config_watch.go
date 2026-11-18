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
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"unstable.build/rune/internal/debug"
)

// configWatchDebounce is how long the watcher waits after the last change
// to a config file before it reports it, so the several events a save
// produces, and a save-by-rename, reload the config once.
const configWatchDebounce = 200 * time.Millisecond

// configWatcher calls onChange when a config file changes on disk. It
// watches the directories of the files rather than the files, so a save
// that replaces the file keeps being observed, and ignores the other
// entries of those directories.
type configWatcher struct {
	scheme   schemeapi.Scheme
	debounce time.Duration
	onChange func()

	mu      sync.Mutex
	files   map[string]struct{}
	watches map[string]*configDirWatch
	timer   *time.Timer
	closed  bool
}

type configDirWatch struct {
	id   int
	done chan struct{}
}

func newConfigWatcher(scheme schemeapi.Scheme, onChange func()) *configWatcher {
	return &configWatcher{
		scheme:   scheme,
		debounce: configWatchDebounce,
		onChange: onChange,
		files:    make(map[string]struct{}),
		watches:  make(map[string]*configDirWatch),
	}
}

// set makes files, absolute paths, the ones whose changes are reported,
// replacing the previous set.
func (w *configWatcher) set(files []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.files = make(map[string]struct{}, len(files))
	dirs := make(map[string]struct{}, len(files))
	for _, file := range files {
		file = canonicalConfigPath(file)
		w.files[file] = struct{}{}
		dirs[filepath.Dir(file)] = struct{}{}
	}
	for dir, watch := range w.watches {
		if _, ok := dirs[dir]; ok {
			continue
		}
		w.stop(dir, watch)
	}
	for dir := range dirs {
		if _, ok := w.watches[dir]; ok {
			continue
		}
		w.start(dir)
	}
}

// start watches dir. The scheme owns the channel until the watch stops;
// one channel per watch because a scheme may close it when it closes.
// It must be called with w.mu held.
func (w *configWatcher) start(dir string) {
	ch := make(chan schemeapi.EventInfo, 64)
	id, err := w.scheme.Watch(dir, ch,
		schemeapi.Create, schemeapi.Write, schemeapi.Rename, schemeapi.Remove)
	if err != nil {
		log.Warnf("watch config directory %s: %v", dir, err)
		return
	}
	watch := &configDirWatch{id: id, done: make(chan struct{})}
	w.watches[dir] = watch
	go debug.CapturePanicReport(func() {
		for {
			select {
			case <-watch.done:
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if w.watched(ev.URI().Path()) {
					w.bump()
				}
			}
		}
	})
}

// stop ends the watch of dir. It must be called with w.mu held.
func (w *configWatcher) stop(dir string, watch *configDirWatch) {
	delete(w.watches, dir)
	close(watch.done)
	if err := w.scheme.StopWatch(watch.id); err != nil {
		log.Warnf("stop watching config directory %s: %v", dir, err)
	}
}

func (w *configWatcher) watched(path string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.files[canonicalConfigPath(path)]
	return ok
}

// canonicalConfigPath resolves the symlinks in the directory of path, the
// way the scheme reports the files in it, and keeps the file name as it
// is since the file may not exist.
func canonicalConfigPath(path string) string {
	path = filepath.Clean(path)
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return path
	}
	return filepath.Join(dir, filepath.Base(path))
}

// bump starts the debounce over.
func (w *configWatcher) bump() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	if w.timer == nil {
		w.timer = time.AfterFunc(w.debounce, w.fire)
		return
	}
	w.timer.Reset(w.debounce)
}

func (w *configWatcher) fire() {
	w.mu.Lock()
	w.timer = nil
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return
	}
	debug.CapturePanicReport(w.onChange)
}

// close stops every watch and the pending report, if any.
func (w *configWatcher) close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	for dir, watch := range w.watches {
		w.stop(dir, watch)
	}
}
