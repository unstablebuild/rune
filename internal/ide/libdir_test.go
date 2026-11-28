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
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/idepkg"
)

// fakeLibDirs lists files, the files of each package it has. A lookup of
// another package fails with err, or idepkg.ErrNotInstalled when err is
// nil.
type fakeLibDirs struct {
	mu    sync.Mutex
	files map[string][]string
	err   error
	// listErr is the error the listing of a package reports.
	listErr error
	// pending makes the listing of a package wait for its context, as
	// one waiting on an install prompt does.
	pending bool
	// gate holds lookups until it is closed.
	gate  chan struct{}
	calls int
}

func (f *fakeLibDirs) LibDir(ctx context.Context, pkgID string) (sdkiterator.Iterator[string], error) {
	f.mu.Lock()
	f.calls++
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	files, ok := f.files[pkgID]
	switch {
	case !ok && f.err != nil:
		return nil, f.err
	case !ok:
		return nil, idepkg.ErrNotInstalled
	case f.listErr != nil:
		return sdkiterator.Error[string](f.listErr), nil
	case f.pending:
		return sdkiterator.FromFunc(func(ctx context.Context) (string, bool, error) {
			<-ctx.Done()
			return "", false, ctx.Err()
		}, func() error { return nil }), nil
	}
	return sdkiterator.FromSlice(slices.Clone(files)), nil
}

func (f *fakeLibDirs) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLibDirs) set(pkgID string, files []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if files == nil {
		delete(f.files, pkgID)
		return
	}
	f.files[pkgID] = files
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newTestSharedLibDir(installed, next *fakeLibDirs, cacheListings bool) (
	*sharedLibDir, *fakeClock,
) {
	if installed.files == nil {
		installed.files = make(map[string][]string)
	}
	if next.files == nil {
		next.files = make(map[string][]string)
	}
	clock := &fakeClock{t: time.Unix(0, 0)}
	s := newSharedLibDir(installed, next, cacheListings)
	s.now = clock.now
	return s, clock
}

func listLibDir(ctx context.Context, s *sharedLibDir, pkgID string) ([]string, error) {
	it, err := s.LibDir(ctx, pkgID)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	return sdkiterator.ToSlice(ctx, it)
}

// waitLookupCallers waits for n callers to wait for or hold the lookup of
// pkgID in flight.
func waitLookupCallers(t *testing.T, s *sharedLibDir, pkgID string, n int) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		l, ok := s.inflight[pkgID]
		return ok && l.refs == n
	}, 5*time.Second, time.Millisecond)
}

func TestSharedLibDir(t *testing.T) {
	ctx := context.Background()
	goFiles := []string{"/data/lib/go/tree-sitter.so", "/data/lib/go/highlights.scm"}

	t.Run("concurrent lookups of a package share one lookup and its listing", func(t *testing.T) {
		installed := &fakeLibDirs{}
		next := &fakeLibDirs{files: map[string][]string{"go": goFiles}, gate: make(chan struct{})}
		s, _ := newTestSharedLibDir(installed, next, false)
		const n = 50
		results := make([][]string, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { results[i], errs[i] = listLibDir(ctx, s, "go") })
		}
		waitLookupCallers(t, s, "go", n)
		close(next.gate)
		wg.Wait()
		assert.Equal(t, 1, installed.callCount())
		assert.Equal(t, 1, next.callCount())
		for i := range n {
			require.NoError(t, errs[i])
			assert.Equal(t, goFiles, results[i])
		}
	})

	t.Run("an error is shared with its class intact", func(t *testing.T) {
		next := &fakeLibDirs{
			err:  fmt.Errorf("get latest version: %w", storageapi.ErrNotFound),
			gate: make(chan struct{}),
		}
		s, _ := newTestSharedLibDir(&fakeLibDirs{}, next, false)
		const n = 10
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() { _, errs[i] = s.LibDir(ctx, "go") })
		}
		waitLookupCallers(t, s, "go", n)
		close(next.gate)
		wg.Wait()
		assert.Equal(t, 1, next.callCount())
		for _, err := range errs {
			assert.ErrorIs(t, err, storageapi.ErrNotFound)
		}
	})

	t.Run("a failure is not retried within the TTL and is retried after it", func(t *testing.T) {
		next := &fakeLibDirs{err: storageapi.ErrNotFound}
		s, clock := newTestSharedLibDir(&fakeLibDirs{}, next, false)
		for range 3 {
			_, err := s.LibDir(ctx, "go")
			assert.ErrorIs(t, err, storageapi.ErrNotFound)
		}
		assert.Equal(t, 1, next.callCount())

		clock.advance(libDirFailureTTL - time.Second)
		_, err := s.LibDir(ctx, "go")
		assert.ErrorIs(t, err, storageapi.ErrNotFound)
		assert.Equal(t, 1, next.callCount())

		clock.advance(time.Second)
		_, err = s.LibDir(ctx, "go")
		assert.ErrorIs(t, err, storageapi.ErrNotFound)
		assert.Equal(t, 2, next.callCount())
	})

	t.Run("a declined install is remembered as a failure", func(t *testing.T) {
		next := &fakeLibDirs{files: map[string][]string{"go": goFiles}, listErr: storageapi.ErrNotFound}
		s, _ := newTestSharedLibDir(&fakeLibDirs{}, next, true)
		_, err := listLibDir(ctx, s, "go")
		assert.ErrorIs(t, err, storageapi.ErrNotFound)
		_, err = s.LibDir(ctx, "go")
		assert.ErrorIs(t, err, storageapi.ErrNotFound)
		assert.Equal(t, 1, next.callCount())
	})

	t.Run("a package installed meanwhile bypasses a remembered failure", func(t *testing.T) {
		installed := &fakeLibDirs{}
		next := &fakeLibDirs{err: storageapi.ErrNotFound}
		s, _ := newTestSharedLibDir(installed, next, false)
		_, err := s.LibDir(ctx, "go")
		require.ErrorIs(t, err, storageapi.ErrNotFound)

		installed.set("go", goFiles)
		files, err := listLibDir(ctx, s, "go")
		require.NoError(t, err)
		assert.Equal(t, goFiles, files)
		assert.Equal(t, 1, next.callCount())
	})

	t.Run("cancelling one caller does not fail the others", func(t *testing.T) {
		next := &fakeLibDirs{files: map[string][]string{"go": goFiles}, gate: make(chan struct{})}
		s, _ := newTestSharedLibDir(&fakeLibDirs{}, next, false)
		cancelled, cancel := context.WithCancel(ctx)
		var cancelledErr, err error
		var files []string
		var wg sync.WaitGroup
		wg.Go(func() { _, cancelledErr = s.LibDir(cancelled, "go") })
		wg.Go(func() { files, err = listLibDir(ctx, s, "go") })
		waitLookupCallers(t, s, "go", 2)
		cancel()
		require.Eventually(t, func() bool {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.inflight["go"].refs == 1
		}, 5*time.Second, time.Millisecond)
		close(next.gate)
		wg.Wait()
		assert.ErrorIs(t, cancelledErr, context.Canceled)
		require.NoError(t, err)
		assert.Equal(t, goFiles, files)
		assert.Equal(t, 1, next.callCount())
	})

	t.Run("a listing is reused until a directory holding it changes", func(t *testing.T) {
		dir := t.TempDir()
		for _, v := range []string{"1", "2"} {
			require.NoError(t, os.Mkdir(filepath.Join(dir, v), 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(dir, v, "tree-sitter.so"),
				[]byte(v), 0o644))
		}
		require.NoError(t, os.Mkdir(filepath.Join(dir, "lib"), 0o755))
		lib := filepath.Join(dir, "lib", "go")
		use := func(version string) {
			t.Helper()
			if err := os.Remove(lib); err != nil && !errors.Is(err, os.ErrNotExist) {
				require.NoError(t, err)
			}
			require.NoError(t, os.Symlink(filepath.Join(dir, version), lib))
		}
		use("1")
		files := []string{filepath.Join(lib, "tree-sitter.so")}
		installed := &fakeLibDirs{files: map[string][]string{"go": files}}
		s, _ := newTestSharedLibDir(installed, &fakeLibDirs{}, true)
		list := func() {
			t.Helper()
			got, err := listLibDir(ctx, s, "go")
			require.NoError(t, err)
			assert.Equal(t, files, got)
		}

		list()
		list()
		assert.Equal(t, 1, installed.callCount(), "listed once while unchanged")

		use("2")
		list()
		list()
		assert.Equal(t, 2, installed.callCount(), "listed again after a version switch")

		require.NoError(t, os.Remove(lib))
		installed.set("go", nil)
		_, err := s.LibDir(ctx, "go")
		assert.ErrorIs(t, err, idepkg.ErrNotInstalled)
		assert.Equal(t, 3, installed.callCount(), "looked up again once uninstalled")
	})

	t.Run("a host's listings are never reused", func(t *testing.T) {
		dir := t.TempDir()
		files := []string{filepath.Join(dir, "tree-sitter.so")}
		installed := &fakeLibDirs{files: map[string][]string{"go": files}}
		s, _ := newTestSharedLibDir(installed, &fakeLibDirs{}, false)
		for range 2 {
			got, err := listLibDir(ctx, s, "go")
			require.NoError(t, err)
			assert.Equal(t, files, got)
		}
		assert.Equal(t, 2, installed.callCount())
	})

	t.Run("closing an iterator that was never listed does not block", func(t *testing.T) {
		next := &fakeLibDirs{files: map[string][]string{"go": goFiles}, pending: true}
		s, _ := newTestSharedLibDir(&fakeLibDirs{}, next, false)
		it, err := s.LibDir(ctx, "go")
		require.NoError(t, err)
		closed := make(chan error)
		go func() { closed <- it.Close() }()
		select {
		case err := <-closed:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("Close blocked")
		}

		it, err = s.LibDir(ctx, "go")
		require.NoError(t, err)
		require.NoError(t, it.Close())
		assert.Equal(t, 2, next.callCount(), "the abandoned lookup ended")
	})
}
