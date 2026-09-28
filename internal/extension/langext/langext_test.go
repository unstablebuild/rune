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

package langext

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/workspace/walkdir"
)

func TestFindProjectRoot(t *testing.T) {
	const ws = "/ws"
	markers := []string{"pyproject.toml", ".venv"}

	cases := []struct {
		name    string
		paths   []string // file/dir markers present, relative to ws
		file    string   // opened file, absolute
		wantRel string
		wantOK  bool
	}{
		{
			name:    "nearest root wins over ancestor",
			paths:   []string{"pyproject.toml", "deploy/worker/pyproject.toml"},
			file:    "/ws/deploy/worker/app/main.py",
			wantRel: "deploy/worker",
			wantOK:  true,
		},
		{
			name:    "marker at workspace root",
			paths:   []string{"pyproject.toml"},
			file:    "/ws/main.py",
			wantRel: "",
			wantOK:  true,
		},
		{
			name:    "no marker yields not found",
			paths:   []string{"deploy/worker/app/main.py"},
			file:    "/ws/deploy/worker/app/main.py",
			wantRel: "",
			wantOK:  false,
		},
		{
			name:    "file in same dir as marker",
			paths:   []string{"deploy/worker/pyproject.toml"},
			file:    "/ws/deploy/worker/main.py",
			wantRel: "deploy/worker",
			wantOK:  true,
		},
		{
			name:    "venv directory marker",
			paths:   []string{"svc/.venv/"},
			file:    "/ws/svc/main.py",
			wantRel: "svc",
			wantOK:  true,
		},
		{
			name:    "file outside workspace ignored",
			paths:   []string{"pyproject.toml"},
			file:    "/elsewhere/main.py",
			wantRel: "",
			wantOK:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mfs := newMemFS(ws)
			for _, p := range tc.paths {
				if strings.HasSuffix(p, "/") {
					mfs.addDir(filepath.Join(ws, p))
					continue
				}
				mfs.addFile(filepath.Join(ws, p))
			}
			wsURI, err := workspaceapi.ParseURI("file://" + ws)
			require.NoError(t, err)
			fileURI, err := workspaceapi.ParseURI("file://" + tc.file)
			require.NoError(t, err)

			root, ok := FindProjectRoot(mfs, wsURI, fileURI, markers)
			assert.Equal(t, tc.wantOK, ok)
			if !tc.wantOK {
				return
			}
			assert.Equal(t, tc.wantRel, root.RelPath)
			wantDir := filepath.Join(ws, tc.wantRel)
			assert.Equal(t, wantDir, root.Dir)
			assert.Equal(t, "file://"+wantDir, root.URI)
		})
	}
}

func TestFindProjectRoots(t *testing.T) {
	const ws = "/ws"
	markers := []string{"pyproject.toml", ".venv"}

	cases := []struct {
		name     string
		paths    []string
		ignore   []string
		maxDepth int
		want     []string
	}{
		{
			name:     "reports nested roots alongside the ancestor",
			paths:    []string{"pyproject.toml", "svc/api/pyproject.toml"},
			maxDepth: 6,
			want:     []string{"", "svc/api"},
		},
		{
			name:     "sibling roots stream in lexical order",
			paths:    []string{"svc/web/pyproject.toml", "svc/api/pyproject.toml"},
			maxDepth: 6,
			want:     []string{"svc/api", "svc/web"},
		},
		{
			name:     "ignored venv marks its parent, not itself",
			paths:    []string{"svc/.venv/"},
			ignore:   []string{"svc/.venv"},
			maxDepth: 6,
			want:     []string{"svc"},
		},
		{
			name:     "descent stops at maxDepth",
			paths:    []string{"a/b/c/pyproject.toml"},
			maxDepth: 2,
			want:     nil,
		},
		{
			name:     "ignored trees are not descended into",
			paths:    []string{"vendor/pkg/pyproject.toml", "svc/pyproject.toml"},
			ignore:   []string{"vendor"},
			maxDepth: 6,
			want:     []string{"svc"},
		},
		{
			name:     "workspace with no project yields nothing",
			paths:    []string{"svc/main.py"},
			maxDepth: 6,
			want:     nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mfs := newMemFS(ws)
			for _, p := range tc.paths {
				if strings.HasSuffix(p, "/") {
					mfs.addDir(filepath.Join(ws, p))
					continue
				}
				mfs.addFile(filepath.Join(ws, p))
			}
			wsURI, err := workspaceapi.ParseURI("file://" + ws)
			require.NoError(t, err)

			roots, err := iterator.ToSlice(context.Background(),
				FindProjectRoots(mfs, wsURI, markers, stubFilter(tc.ignore), tc.maxDepth))
			require.NoError(t, err)
			got := make([]string, 0, len(roots))
			for _, r := range roots {
				got = append(got, r.RelPath)
				wantDir := filepath.Join(ws, r.RelPath)
				assert.Equal(t, wantDir, r.Dir)
				assert.Equal(t, "file://"+wantDir, r.URI)
			}
			if tc.want == nil {
				assert.Empty(t, got)
				return
			}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestFindProjectRootsStreams(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile(filepath.Join(ws, "a/pyproject.toml"))
	mfs.addFile(filepath.Join(ws, "b/pyproject.toml"))
	mfs.addFile(filepath.Join(ws, "c/pyproject.toml"))
	wsURI, err := workspaceapi.ParseURI("file://" + ws)
	require.NoError(t, err)

	it := FindProjectRoots(mfs, wsURI, []string{"pyproject.toml"}, nil, 6)
	t.Cleanup(func() { require.NoError(t, it.Close()) })

	first, ok := it.Next(context.Background())
	require.True(t, ok)
	assert.Equal(t, "a", first.RelPath)
	assert.Equal(t, int32(1), mfs.readDirs.Load(),
		"only the workspace root should have been listed to reach the first root")

	rest, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "c"}, []string{rest[0].RelPath, rest[1].RelPath})
}

// stubFilter ignores the exact relative paths listed.
func stubFilter(paths []string) walkdir.Filter {
	if paths == nil {
		return nil
	}
	return filterFunc(func(relpath string, _ bool) bool {
		return slices.Contains(paths, relpath)
	})
}

type filterFunc func(relpath string, isDir bool) bool

func (f filterFunc) MatchRelPath(relpath string, isDir bool) bool {
	return f(relpath, isDir)
}

func TestInitializerOpenTriggersOneInitRoot(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	var calls atomic.Int32
	roots := make(chan Root, 4)
	cfg := pyConfig(func(_ context.Context, r Root) error {
		calls.Add(1)
		roots <- r
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())
	require.Equal(t, []textapi.EventType{
		textapi.EventTypeOpen, textapi.EventTypeChange, textapi.EventTypeCreate,
	}, ed.subscribedTo)

	ed.fire(t, openEvent("/ws/svc/main.py"))
	got := <-roots
	assert.Equal(t, "svc", got.RelPath)

	// A second open of the same root must not re-run InitRoot.
	ed.fire(t, openEvent("/ws/svc/other.py"))
	assertNoMoreRoots(t, roots)
	assert.Equal(t, int32(1), calls.Load())
}

// TestInitializerChangeTriggersInitRoot covers the core fix: an
// out-of-band write (agent apply_patch) surfaces as EventTypeChange with
// no preceding editor open, and must still bring up the nested root.
func TestInitializerChangeTriggersInitRoot(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	roots := make(chan Root, 4)
	cfg := pyConfig(func(_ context.Context, r Root) error {
		roots <- r
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	ed.fire(t, changeEvent("/ws/svc/main.py"))
	got := <-roots
	assert.Equal(t, "svc", got.RelPath)
}

// TestInitializerCreateTriggersInitRoot covers a newly-created .py under a
// nested project bringing the root up without an open.
func TestInitializerCreateTriggersInitRoot(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	roots := make(chan Root, 4)
	cfg := pyConfig(func(_ context.Context, r Root) error {
		roots <- r
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	ed.fire(t, createEvent("/ws/svc/new.py"))
	got := <-roots
	assert.Equal(t, "svc", got.RelPath)
}

// TestInitializerClaimedRootSkipsWalk asserts that once a root is
// initialized, a later change under it walks no filesystem and does not
// re-run InitRoot. Steady-state editing inside an active project must be
// O(1).
func TestInitializerClaimedRootSkipsWalk(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	var calls atomic.Int32
	roots := make(chan Root, 4)
	cfg := pyConfig(func(_ context.Context, r Root) error {
		calls.Add(1)
		roots <- r
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	ed.fire(t, changeEvent("/ws/svc/main.py"))
	<-roots

	statsAfterInit := mfs.stats.Load()
	ed.fire(t, changeEvent("/ws/svc/pkg/other.py"))
	assertNoMoreRoots(t, roots)
	assert.Equal(t, int32(1), calls.Load())
	assert.Equal(t, statsAfterInit, mfs.stats.Load(),
		"a change under an initialized root must not walk the filesystem")
}

// TestInitializerNegativeCacheBoundsWalks asserts a source file with no
// enclosing marker is walked once per distinct parent dir, not once per
// event, so a storm of writes to marker-less files cannot trigger mass
// walks.
func TestInitializerNegativeCacheBoundsWalks(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws) // no markers anywhere

	var calls atomic.Int32
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	ed.fire(t, changeEvent("/ws/stray/a.py"))
	statsAfterFirst := mfs.stats.Load()
	require.Positive(t, statsAfterFirst, "first change must walk once")

	for range 50 {
		ed.fire(t, changeEvent("/ws/stray/a.py"))
		ed.fire(t, changeEvent("/ws/stray/b.py"))
	}
	assert.Equal(t, statsAfterFirst, mfs.stats.Load(),
		"repeat changes in a walked-unresolved dir must not re-walk")
	assert.Equal(t, int32(0), calls.Load())
}

// TestInitializerScaffoldAfterChangeNeedsNoReload covers the exact
// pitfall the negative cache must not create: a source file is written in
// a marker-less dir (cached unresolved via change), the user then
// scaffolds a project there, and discovery must succeed from the ensuing
// create events alone — no editor open, no workspace reload. Only later
// changes are cache-gated; creates always re-walk.
func TestInitializerScaffoldAfterChangeNeedsNoReload(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws) // no markers yet

	roots := make(chan Root, 4)
	cfg := pyConfig(func(_ context.Context, r Root) error {
		roots <- r
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	// A change with no enclosing marker caches svc as unresolved.
	ed.fire(t, changeEvent("/ws/svc/main.py"))
	assertNoMoreRoots(t, roots)

	// The project appears. Even if the marker's own create is never
	// observed, creating a source file re-walks and discovers the root.
	mfs.addFile("/ws/svc/pyproject.toml")
	ed.fire(t, createEvent("/ws/svc/app.py"))

	got := <-roots
	assert.Equal(t, "svc", got.RelPath)
}

// TestInitializerCreateAlwaysRewalks asserts a create in a change-cached
// dir re-walks (rather than trusting the negative cache), so the cache
// can never wedge discovery for a project that materializes there.
func TestInitializerCreateAlwaysRewalks(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)

	roots := make(chan Root, 4)
	cfg := pyConfig(func(_ context.Context, r Root) error {
		roots <- r
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	ed.fire(t, changeEvent("/ws/svc/main.py")) // caches svc unresolved
	assertNoMoreRoots(t, roots)

	mfs.addFile("/ws/svc/pyproject.toml")
	ed.fire(t, createEvent("/ws/svc/pyproject.toml")) // marker create heals cache
	ed.fire(t, changeEvent("/ws/svc/main.py"))        // now resolves

	got := <-roots
	assert.Equal(t, "svc", got.RelPath)
}

// TestInitializerOpenAlwaysRewalks asserts an open in a change-cached dir
// re-walks, so simply opening a file heals a stale negative-cache entry.
func TestInitializerOpenAlwaysRewalks(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)

	roots := make(chan Root, 4)
	cfg := pyConfig(func(_ context.Context, r Root) error {
		roots <- r
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	ed.fire(t, changeEvent("/ws/svc/main.py")) // caches svc unresolved
	assertNoMoreRoots(t, roots)

	mfs.addFile("/ws/svc/pyproject.toml")
	ed.fire(t, openEvent("/ws/svc/main.py")) // open ignores the cache

	got := <-roots
	assert.Equal(t, "svc", got.RelPath)
}

// TestInitializerStormBound asserts that a flood of changes across a few
// roots runs InitRoot at most once per distinct root, and that Handle
// returns promptly (never blocks on bring-up).
func TestInitializerStormBound(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/a/pyproject.toml")
	mfs.addFile("/ws/b/pyproject.toml")
	mfs.addFile("/ws/c/pyproject.toml")

	var calls atomic.Int32
	release := make(chan struct{})
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		<-release // hold bring-up open; Handle must not block on it
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	dirs := []string{"a", "b", "c"}
	done := make(chan struct{})
	go func() {
		for n := range 300 {
			d := dirs[n%len(dirs)]
			ed.fire(t, changeEvent(filepath.Join(ws, d, "f"+strconv.Itoa(n)+".py")))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Handle blocked on bring-up during storm")
	}
	close(release)

	assert.LessOrEqual(t, calls.Load(), int32(len(dirs)),
		"InitRoot must run at most once per distinct root")
}

// TestInitializerKillSwitchOpenOnly asserts that with WatchEvents limited
// to Open, a change triggers nothing (the pathological-monorepo escape
// hatch).
func TestInitializerKillSwitchOpenOnly(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	var calls atomic.Int32
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		return nil
	})
	cfg.WatchEvents = []textapi.EventType{textapi.EventTypeOpen}

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())
	require.Equal(t, []textapi.EventType{textapi.EventTypeOpen}, ed.subscribedTo)

	ed.fire(t, changeEvent("/ws/svc/main.py"))
	assert.Equal(t, int32(0), calls.Load())
}

// TestInitializerMixedOpenChangeDedupe asserts that a racing Open and
// Change for the same new root bring it up exactly once.
func TestInitializerMixedOpenChangeDedupe(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 16)
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	var wg sync.WaitGroup
	for n := range 8 {
		ev := openEvent("/ws/svc/main.py")
		if n%2 == 0 {
			ev = changeEvent("/ws/svc/main.py")
		}
		wg.Go(func() { ed.fire(t, ev) })
	}
	<-started
	close(release)
	wg.Wait()

	assertNoExtraStart(t, started)
	assert.Equal(t, int32(1), calls.Load())
}

// TestInitializerAsyncSurvivesEventContextCancel reproduces the bug where
// the background bring-up was tied to the editor's per-event dispatch
// context: that context is canceled as soon as Handle returns, which
// killed the in-flight uv/LSP bring-up with "context canceled". The async
// work must instead run under the long-lived workspace context, so the
// context InitRoot observes stays live after Handle returns.
func TestInitializerAsyncSurvivesEventContextCancel(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	proceed := make(chan struct{})
	errs := make(chan error, 1)
	cfg := pyConfig(func(ctx context.Context, _ Root) error {
		<-proceed // ensure the event ctx is canceled before we inspect ours
		errs <- ctx.Err()
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	// Deliver the open with a context that is canceled the instant Handle
	// returns, exactly as the editor's event dispatch does.
	evCtx, cancel := context.WithCancel(context.Background())
	ed.handler.Handle(evCtx, openEvent("/ws/svc/main.py"))
	cancel()
	close(proceed)

	select {
	case err := <-errs:
		assert.NoError(t, err, "bring-up must not observe a canceled context")
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for InitRoot")
	}
}

func TestInitializerConcurrentOpensDedupe(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 16)
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		started <- struct{}{}
		<-release // hold the first claim open so racing opens observe it
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			ed.fire(t, openEvent("/ws/svc/main.py"))
		})
	}
	<-started // exactly one bring-up should start
	close(release)
	wg.Wait()

	assertNoExtraStart(t, started)
	assert.Equal(t, int32(1), calls.Load())
}

func TestInitializerIgnoresNonMatchingFiles(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	var calls atomic.Int32
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	ed.fire(t, openEvent("/ws/svc/README.md"))
	assert.Equal(t, int32(0), calls.Load())
}

func TestInitializerNoMarkerDoesNotInitialize(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws) // no markers anywhere

	var calls atomic.Int32
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	ed.fire(t, openEvent("/ws/svc/stray.py"))
	assert.Equal(t, int32(0), calls.Load())
}

func TestInitializeAtDedupedAgainstLaterEvents(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/pyproject.toml")

	var calls atomic.Int32
	roots := make(chan Root, 4)
	cfg := pyConfig(func(_ context.Context, r Root) error {
		calls.Add(1)
		roots <- r
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	root := rootForDir(mfs, ws, ws)
	require.NoError(t, i.InitializeAt(context.Background(), root))
	got := <-roots
	assert.Equal(t, "", got.RelPath)

	// An open that resolves to the already-initialized root is a no-op.
	ed.fire(t, openEvent("/ws/main.py"))
	assertNoMoreRoots(t, roots)
	assert.Equal(t, int32(1), calls.Load())
}

func TestInitializeAtRetriesAfterFailure(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/pyproject.toml")

	var calls atomic.Int32
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		if calls.Add(1) == 1 {
			return assert.AnError
		}
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	root := rootForDir(mfs, ws, ws)
	require.Error(t, i.InitializeAt(context.Background(), root))
	require.NoError(t, i.InitializeAt(context.Background(), root))
	assert.Equal(t, int32(2), calls.Load(), "failed bring-up must be retryable")
}

func TestReinitializeRerunsAllKnownRoots(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/a/Cargo.toml")
	mfs.addFile("/ws/b/Cargo.toml")

	var calls atomic.Int32
	cfg := ProjectConfig{
		LanguageID: "rust",
		Markers:    []string{"Cargo.toml"},
		FileMatch: func(uri workspaceapi.URI) bool {
			return strings.HasSuffix(uri.Path(), ".rs")
		},
		InitRoot: func(_ context.Context, _ Root) error {
			calls.Add(1)
			return nil
		},
	}

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	require.NoError(t, i.InitializeAt(context.Background(), rootForDir(mfs, ws, "/ws/a")))
	require.NoError(t, i.InitializeAt(context.Background(), rootForDir(mfs, ws, "/ws/b")))
	require.Equal(t, int32(2), calls.Load())

	require.NoError(t, i.Reinitialize(context.Background()))
	assert.Equal(t, int32(4), calls.Load(), "every known root must be re-initialized")

	// Roots stay known after reinit, so a later reinit re-runs them again.
	require.NoError(t, i.Reinitialize(context.Background()))
	assert.Equal(t, int32(6), calls.Load())
}

func TestReinitializeNoOpWhenNothingInitialized(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)

	var calls atomic.Int32
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	require.NoError(t, i.Reinitialize(context.Background()))
	assert.Equal(t, int32(0), calls.Load())
}

func TestReinitializeSkipsRootStillInitializing(t *testing.T) {
	const ws = "/ws"
	mfs := newMemFS(ws)
	mfs.addFile("/ws/svc/pyproject.toml")

	var calls atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	cfg := pyConfig(func(_ context.Context, _ Root) error {
		calls.Add(1)
		started <- struct{}{}
		<-release // hold the in-flight claim open across Reinitialize
		return nil
	})

	ed := &fakeEditor{}
	i := NewInitializer(context.Background(), mfs, ed, cfg)
	require.NoError(t, i.Start())

	// Drive an open that starts a bring-up and blocks it in InitRoot.
	go ed.fire(t, openEvent("/ws/svc/main.py"))
	<-started

	// The only root is still initializing, so Reinitialize must skip it.
	require.NoError(t, i.Reinitialize(context.Background()))
	assert.Equal(t, int32(1), calls.Load(), "an initializing root must not be re-run")

	close(release)
}

// pyConfig builds a python-shaped ProjectConfig with the given InitRoot.
func pyConfig(initRoot func(context.Context, Root) error) ProjectConfig {
	return ProjectConfig{
		LanguageID: "python",
		Markers:    []string{"pyproject.toml", ".venv"},
		FileMatch: func(uri workspaceapi.URI) bool {
			return strings.HasSuffix(uri.Path(), ".py")
		},
		WatchEvents: []textapi.EventType{
			textapi.EventTypeOpen, textapi.EventTypeChange, textapi.EventTypeCreate,
		},
		InitRoot: initRoot,
	}
}

func openEvent(path string) textapi.Event {
	uri, _ := workspaceapi.ParseURI("file://" + path)
	return textapi.Event{Type: textapi.EventTypeOpen, URI: uri}
}

func changeEvent(path string) textapi.Event {
	uri, _ := workspaceapi.ParseURI("file://" + path)
	return textapi.Event{Type: textapi.EventTypeChange, URI: uri}
}

func createEvent(path string) textapi.Event {
	uri, _ := workspaceapi.ParseURI("file://" + path)
	return textapi.Event{Type: textapi.EventTypeCreate, URI: uri}
}

func assertNoMoreRoots(t *testing.T, roots <-chan Root) {
	t.Helper()
	select {
	case r := <-roots:
		t.Fatalf("unexpected extra InitRoot for %q", r.Dir)
	default:
	}
}

func assertNoExtraStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
		t.Fatal("a second bring-up started; dedupe failed")
	default:
	}
}

// fakeEditor is a textapi.Editor that only records the open-event
// subscription so tests can drive Handle directly. Every other method is
// an unused stub.
type fakeEditor struct {
	mu           sync.Mutex
	subscribedTo []textapi.EventType
	handler      textapi.EventHandler
}

var _ textapi.Editor = (*fakeEditor)(nil)

func (e *fakeEditor) SubscribeEvents(types []textapi.EventType, h textapi.EventHandler) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.subscribedTo = types
	e.handler = h
	return nil
}

// fire delivers ev to the subscribed handler, failing if Start was not
// called first.
func (e *fakeEditor) fire(t *testing.T, ev textapi.Event) {
	t.Helper()
	e.mu.Lock()
	h := e.handler
	e.mu.Unlock()
	require.NotNil(t, h, "no event handler subscribed")
	h.Handle(context.Background(), ev)
}

func (e *fakeEditor) Editor(workspaceapi.URI) (textapi.Handler, error) { return nil, nil }
func (e *fakeEditor) SetLocationList(
	textapi.Handler, textapi.LocationPriority, string, textapi.LocationList,
) error {
	return nil
}
func (e *fakeEditor) MoveToNextLocation(textapi.Handler, string) error { return nil }
func (e *fakeEditor) MoveToPrevLocation(textapi.Handler, string) error { return nil }
func (e *fakeEditor) Cursor(textapi.Handler) (term.Coordinates, error) {
	return term.Coordinates{}, nil
}
func (e *fakeEditor) SetCursor(textapi.Handler, term.Coordinates) error { return nil }
func (e *fakeEditor) CellView(textapi.Handler) textapi.CellView         { return nil }
func (e *fakeEditor) CellEditor(textapi.Handler) textapi.CellEditor     { return nil }
func (e *fakeEditor) SetDefaultAttributes(textapi.Handler, term.Attributes) error {
	return nil
}

// memFS is an in-memory workspaceapi.FileSystem with real path-join
// semantics so the upward marker walk can be exercised without touching
// disk.
type memFS struct {
	root     string
	files    map[string]bool
	dirs     map[string]bool
	stats    atomic.Int32
	readDirs atomic.Int32
}

func newMemFS(root string) *memFS {
	return &memFS{root: root, files: map[string]bool{}, dirs: map[string]bool{}}
}

func (m *memFS) addFile(p string) { m.files[filepath.Clean(p)] = true }
func (m *memFS) addDir(p string)  { m.dirs[filepath.Clean(p)] = true }

func (m *memFS) resolve(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(m.root, p)
}

func (m *memFS) URI(p string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + m.resolve(p))
}

func (m *memFS) Stat(p string) (os.FileInfo, error) {
	m.stats.Add(1)
	name := m.resolve(p)
	if m.files[name] {
		return memFileInfo{name: filepath.Base(name)}, nil
	}
	if m.dirs[name] {
		return memFileInfo{name: filepath.Base(name), dir: true}, nil
	}
	return nil, &fs.PathError{Op: "stat", Path: p, Err: os.ErrNotExist}
}

// ReadDir synthesizes the listing implied by the registered files and
// dirs, so an unregistered intermediate path still lists as a directory.
func (m *memFS) ReadDir(p string) ([]os.DirEntry, error) {
	m.readDirs.Add(1)
	dir := m.resolve(p)
	isDir := map[string]bool{}
	mark := func(path string, registered bool) {
		rel, err := filepath.Rel(dir, path)
		if err != nil || rel == "." || rel == ".." || hasParentPrefix(rel) {
			return
		}
		name, _, nested := strings.Cut(rel, string(filepath.Separator))
		isDir[name] = isDir[name] || nested || registered
	}
	for f := range m.files {
		mark(f, false)
	}
	for d := range m.dirs {
		mark(d, true)
	}
	out := make([]os.DirEntry, 0, len(isDir))
	for name, d := range isDir {
		out = append(out, memDirEntry{memFileInfo{name: name, dir: d}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func (m *memFS) OpenFile(string, int, os.FileMode) (workspaceapi.File, error) {
	return nil, os.ErrInvalid
}
func (m *memFS) Remove(string) error                { return os.ErrInvalid }
func (m *memFS) MkdirAll(string, os.FileMode) error { return os.ErrInvalid }

type memFileInfo struct {
	name string
	dir  bool
}

func (i memFileInfo) Name() string       { return i.name }
func (i memFileInfo) Size() int64        { return 0 }
func (i memFileInfo) Mode() os.FileMode  { return 0 }
func (i memFileInfo) ModTime() time.Time { return time.Time{} }
func (i memFileInfo) IsDir() bool        { return i.dir }
func (i memFileInfo) Sys() any           { return nil }

type memDirEntry struct{ memFileInfo }

func (e memDirEntry) Type() os.FileMode          { return e.Mode() }
func (e memDirEntry) Info() (os.FileInfo, error) { return e.memFileInfo, nil }
