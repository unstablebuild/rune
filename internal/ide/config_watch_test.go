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
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
)

// watchRecordingScheme records which directories a configWatcher watches.
type watchRecordingScheme struct {
	schemeapi.Scheme
	mu      sync.Mutex
	dirs    map[int]string
	stopped []string
}

func (s *watchRecordingScheme) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	id, err := s.Scheme.Watch(path, c, events...)
	if err != nil {
		return id, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirs[id] = path
	return id, nil
}

func (s *watchRecordingScheme) StopWatch(id int) error {
	s.mu.Lock()
	s.stopped = append(s.stopped, s.dirs[id])
	delete(s.dirs, id)
	s.mu.Unlock()
	return s.Scheme.StopWatch(id)
}

func (s *watchRecordingScheme) watching() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var dirs []string
	for _, dir := range s.dirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	return dirs
}

func TestConfigWatcher(t *testing.T) {
	t.Parallel()
	const debounce = 20 * time.Millisecond

	newWatcher := func(t *testing.T) (*configWatcher, *watchRecordingScheme, *atomic.Int32) {
		t.Helper()
		uri, err := workspaceapi.ParseURI("memory:///")
		require.NoError(t, err)
		mem, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		t.Cleanup(func() { _ = mem.(io.Closer).Close() })
		scheme := &watchRecordingScheme{Scheme: mem, dirs: make(map[int]string)}
		var changes atomic.Int32
		w := newConfigWatcher(scheme, func() { changes.Add(1) })
		w.debounce = debounce
		t.Cleanup(w.close)
		return w, scheme, &changes
	}
	touch := func(t *testing.T, s schemeapi.Scheme, path string) {
		t.Helper()
		f, err := s.Create(path)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	settled := func(changes *atomic.Int32, want int32) func() bool {
		return func() bool { return changes.Load() == want }
	}

	t.Run("several changes to a watched file report once", func(t *testing.T) {
		t.Parallel()
		w, scheme, changes := newWatcher(t)
		w.set([]string{"/cfg/config.yaml", "/cfg/etc/a.yaml"})
		assert.Equal(t, []string{"/cfg", "/cfg/etc"}, scheme.watching(),
			"the directories of the files are watched, once each")

		for range 3 {
			touch(t, scheme, "/cfg/config.yaml")
		}
		touch(t, scheme, "/cfg/etc/a.yaml")
		require.Eventually(t, settled(changes, 1), time.Second, time.Millisecond)
		assert.Never(t, func() bool { return changes.Load() > 1 },
			5*debounce, time.Millisecond, "the changes coalesce into one report")

		touch(t, scheme, "/cfg/etc/a.yaml")
		require.Eventually(t, settled(changes, 2), time.Second, time.Millisecond,
			"a later change reports again")
	})

	t.Run("an unrelated file in a watched directory is ignored", func(t *testing.T) {
		t.Parallel()
		w, scheme, changes := newWatcher(t)
		w.set([]string{"/cfg/config.yaml"})
		touch(t, scheme, "/cfg/other.yaml")
		touch(t, scheme, "/cfg/sub/config.yaml")
		assert.Never(t, func() bool { return changes.Load() > 0 },
			5*debounce, time.Millisecond)
	})

	t.Run("set follows the files of the next load", func(t *testing.T) {
		t.Parallel()
		w, scheme, changes := newWatcher(t)
		w.set([]string{"/cfg/config.yaml", "/cfg/etc/a.yaml"})
		w.set([]string{"/cfg/config.yaml", "/dotfiles/theme.yaml"})
		assert.Equal(t, []string{"/cfg", "/dotfiles"}, scheme.watching(),
			"a directory no longer needed is unwatched, a new one watched")
		assert.Equal(t, []string{"/cfg/etc"}, scheme.stopped)

		touch(t, scheme, "/cfg/etc/a.yaml")
		assert.Never(t, func() bool { return changes.Load() > 0 },
			5*debounce, time.Millisecond, "a file no longer included is ignored")
		touch(t, scheme, "/dotfiles/theme.yaml")
		require.Eventually(t, settled(changes, 1), time.Second, time.Millisecond)
	})

	t.Run("close stops the watches and a pending report", func(t *testing.T) {
		t.Parallel()
		w, scheme, changes := newWatcher(t)
		w.set([]string{"/cfg/config.yaml"})
		touch(t, scheme, "/cfg/config.yaml")
		w.close()
		assert.Empty(t, scheme.watching())
		touch(t, scheme, "/cfg/config.yaml")
		assert.Never(t, func() bool { return changes.Load() > 0 },
			5*debounce, time.Millisecond)
		w.set([]string{"/cfg/config.yaml"})
		assert.Empty(t, scheme.watching(), "a closed watcher watches nothing")
	})
}
