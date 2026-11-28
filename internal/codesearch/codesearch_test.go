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

package codesearch

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/codesearch/index"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/workspace"
)

// sampleFiles is a workspace with what a search leaves out: what git
// ignores, a hidden folder, an excluded folder and a binary file, and
// what those leave out in turn.
var sampleFiles = map[string]string{
	".gitignore":             "tmp/\n*.log\n",
	"main.go":                "package main\n\nfunc main() {\n\tRetry(3)\n}\n",
	"retry/retry.go":         "package retry\n\n// Retry retries with backoff.\nfunc Retry(n int) {}\n",
	"retry/backoff.go":       "package retry\n\nfunc backoff(n int) int { return n * n }\n",
	"docs/README.md":         "# Retry\n\nRetry with BACKOFF.\n",
	"tmp/scratch.go":         "func Retry() // ignored\n",
	"tmp/.hidden/retry.go":   "func Retry() // hidden in what is ignored\n",
	".cache/retry.go":        "func Retry() // hidden\n",
	".cache/.inner/retry.go": "func Retry() // hidden in what is hidden\n",
	".cache/retry.log":       "Retry // ignored in what is hidden\n",
	"vendor/lib.go":          "func Retry() // excluded\n",
	"vendor/tmp/retry.go":    "func Retry() // ignored in what is excluded\n",
	"vendor/nested/retry.go": "func Retry() // excluded in what is excluded\n",
	"logs/retry.log":         "Retry // ignored\n",
	"bin/tool":               "Retry\x00\x01\x02",
}

// old is when the sample files were last modified, well before any
// index of them is built.
var old = time.Now().Add(-time.Hour)

func newWorkspace(t *testing.T, files map[string]string) (string, workspaceapi.URI, workspaceapi.FileSystem) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		writeFile(t, dir, name, content, old)
	}
	root, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), root)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	return dir, root, scheme
}

func writeFile(t *testing.T, dir, name, content string, mtime time.Time) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
	require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
	require.NoError(t, os.Chtimes(p, mtime, mtime))
}

func openIndex(t *testing.T, cfg Config) *Index {
	t.Helper()
	if cfg.Notify == nil {
		cfg.Notify = &recordingNotifications{}
	}
	cfg.Schedule = func(fn func()) bool { fn(); return true }
	idx := Open(cfg)
	t.Cleanup(func() { _ = idx.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, idx.Wait(ctx))
	return idx
}

func search(t *testing.T, idx *Index, req searchapi.RegexpRequest) []string {
	t.Helper()
	it, err := idx.RegexpSearch(context.Background(), req)
	require.NoError(t, err)
	matches, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	out := []string{}
	for _, m := range matches {
		out = append(out, fmt.Sprintf("%s:%d:%s", m.Path, m.Line, m.Text))
	}
	return out
}

func event(root workspaceapi.URI, tpe textapi.EventType, rel string) textapi.Event {
	uri, err := workspaceapi.ParseURI(root.String() + "/" + rel)
	if err != nil {
		panic(err)
	}
	return textapi.Event{Type: tpe, URI: uri}
}

// recordingNotifications records what is notified, and every progress
// update as "<message> <progress>/<total>".
type recordingNotifications struct {
	mu       sync.Mutex
	notified []string
	updates  []string
}

func (n *recordingNotifications) Notify(_ browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified = append(n.notified, fmt.Sprintf(msg, args...))
	return fmt.Sprintf("n%d", len(n.notified)), nil
}

func (n *recordingNotifications) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return n.Notify(level, msg, args...)
}

func (n *recordingNotifications) UpdateNotificationProgress(_, message string, progress, total int64) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if total <= 0 || progress > total {
		return fmt.Errorf("invalid progress %d/%d", progress, total)
	}
	n.updates = append(n.updates, fmt.Sprintf("%s %d/%d", message, progress, total))
	return nil
}

func (n *recordingNotifications) snapshot() (notified, updates []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notified...), append([]string(nil), n.updates...)
}

// gatedFS holds the first read of name after arm until unblock, so a
// test can act while the index is built.
type gatedFS struct {
	workspaceapi.FileSystem
	name string

	mu      sync.Mutex
	armed   bool
	entered chan struct{}
	release chan struct{}
	once    *sync.Once
}

func (f *gatedFS) arm() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.armed = true
	f.entered = make(chan struct{})
	f.release = make(chan struct{})
	f.once = &sync.Once{}
}

func (f *gatedFS) waitEntered(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	entered := f.entered
	f.mu.Unlock()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatalf("the build never read %s", f.name)
	}
}

func (f *gatedFS) unblock() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.once != nil {
		f.once.Do(func() { close(f.release) })
	}
}

func (f *gatedFS) OpenFile(name string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	f.mu.Lock()
	gated := f.armed && name == f.name
	if gated {
		f.armed = false
	}
	entered, release := f.entered, f.release
	f.mu.Unlock()
	if gated {
		close(entered)
		<-release
	}
	return f.FileSystem.OpenFile(name, flag, mode)
}

// indexEnv is the index of a workspace whose main.go the build reads
// through a gate. Its index persists in indexDir, unless it has none.
type indexEnv struct {
	dir      string
	root     workspaceapi.URI
	fs       *gatedFS
	indexDir string
	file     string
	notify   *recordingNotifications
	idx      *Index
}

func newIndexEnv(t *testing.T, files map[string]string, indexed bool) *indexEnv {
	t.Helper()
	dir, root, fsys := newWorkspace(t, files)
	e := &indexEnv{
		dir: dir, root: root, fs: &gatedFS{FileSystem: fsys, name: "main.go"},
		indexDir: t.TempDir(), notify: &recordingNotifications{},
	}
	if indexed {
		e.file = IndexFile(e.indexDir, root)
	}
	return e
}

// open opens the index without waiting for it to load.
func (e *indexEnv) open(t *testing.T) {
	t.Helper()
	e.idx = Open(Config{
		Root: e.root, FS: e.fs, File: e.file, Notify: e.notify,
		Schedule: func(fn func()) bool { fn(); return true },
	})
	t.Cleanup(func() {
		e.fs.unblock()
		_ = e.idx.Close()
	})
}

func (e *indexEnv) wait(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, e.idx.Wait(ctx))
}

func (e *indexEnv) handle(tpe textapi.EventType, rel string) bool {
	return e.idx.Handle(context.Background(), event(e.root, tpe, rel))
}

// builds is how many builds of the index were notified.
func (e *indexEnv) builds() int {
	notified, _ := e.notify.snapshot()
	return len(notified)
}

func (e *indexEnv) lastUpdate() string {
	_, updates := e.notify.snapshot()
	if len(updates) == 0 {
		return ""
	}
	return updates[len(updates)-1]
}

// indexFiles lists the files in the folder of the index.
func (e *indexEnv) indexFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(e.indexDir)
	require.NoError(t, err)
	out := []string{}
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

// tmpIndexFile is where a build of the index in file writes it.
func tmpIndexFile(file string) string {
	return fmt.Sprintf("%s.%d.new", file, os.Getpid())
}

var needle = searchapi.RegexpRequest{Pattern: "needle"}

// needleFiles is a workspace whose two files the needle is in.
var needleFiles = map[string]string{
	"main.go":    "package main\n\n// needle\n",
	"lib/lib.go": "package lib\n\n// needle\n",
}

var bothNeedles = []string{"lib/lib.go:3:// needle", "main.go:3:// needle"}

func TestSearch(t *testing.T) {
	tests := []struct {
		name  string
		query searchapi.RegexpRequest
		want  []string
	}{
		{
			name:  "a word",
			query: searchapi.RegexpRequest{Pattern: "Retry"},
			want: []string{
				"docs/README.md:1:# Retry",
				"docs/README.md:3:Retry with BACKOFF.",
				"main.go:4:\tRetry(3)",
				"retry/retry.go:3:// Retry retries with backoff.",
				"retry/retry.go:4:func Retry(n int) {}",
			},
		},
		{
			name:  "ignoring case",
			query: searchapi.RegexpRequest{Pattern: "backoff", IgnoreCase: true},
			want: []string{
				"docs/README.md:3:Retry with BACKOFF.",
				"retry/backoff.go:3:func backoff(n int) int { return n * n }",
				"retry/retry.go:3:// Retry retries with backoff.",
			},
		},
		{
			name:  "a regular expression",
			query: searchapi.RegexpRequest{Pattern: `^func \w+\(n int\)`},
			want: []string{
				"retry/backoff.go:3:func backoff(n int) int { return n * n }",
				"retry/retry.go:4:func Retry(n int) {}",
			},
		},
		{
			name:  "without trigrams",
			query: searchapi.RegexpRequest{Pattern: `^\S$|^}`},
			want:  []string{"main.go:5:}"},
		},
		{
			name:  "in folders and files",
			query: searchapi.RegexpRequest{Pattern: "Retry", Roots: []string{"docs", "main.go", "missing"}},
			want: []string{
				"docs/README.md:1:# Retry",
				"docs/README.md:3:Retry with BACKOFF.",
				"main.go:4:\tRetry(3)",
			},
		},
		{
			name:  "up to a limit",
			query: searchapi.RegexpRequest{Pattern: "Retry", Limit: 2},
			want: []string{
				"docs/README.md:1:# Retry",
				"docs/README.md:3:Retry with BACKOFF.",
			},
		},
		{
			name:  "nothing",
			query: searchapi.RegexpRequest{Pattern: "palindrome"},
			want:  []string{},
		},
		{
			name:  "in files whose name matches",
			query: searchapi.RegexpRequest{Pattern: "Retry", Include: []string{"*.md", "main.go"}},
			want: []string{
				"docs/README.md:1:# Retry",
				"docs/README.md:3:Retry with BACKOFF.",
				"main.go:4:\tRetry(3)",
			},
		},
		{
			name:  "up to a limit by file",
			query: searchapi.RegexpRequest{Pattern: "Retry", MaxPerFile: 1},
			want: []string{
				"docs/README.md:1:# Retry",
				"main.go:4:\tRetry(3)",
				"retry/retry.go:3:// Retry retries with backoff.",
			},
		},
		{
			name: "in what is hidden, ignored and excluded when it is a root",
			query: searchapi.RegexpRequest{
				Pattern: "Retry", Roots: []string{".cache", "/tmp/", "vendor", "logs/retry.log"},
			},
			want: []string{
				".cache/retry.go:1:func Retry() // hidden",
				"logs/retry.log:1:Retry // ignored",
				"tmp/scratch.go:1:func Retry() // ignored",
				"vendor/lib.go:1:func Retry() // excluded",
			},
		},
		{
			name:  "in a hidden root and the workspace",
			query: searchapi.RegexpRequest{Pattern: "hidden$", Roots: []string{".", ".cache"}},
			want:  []string{".cache/retry.go:1:func Retry() // hidden"},
		},
		{
			name:  "in a file in a hidden folder",
			query: searchapi.RegexpRequest{Pattern: "Retry", Roots: []string{".cache/.inner/retry.go"}},
			want:  []string{".cache/.inner/retry.go:1:func Retry() // hidden in what is hidden"},
		},
	}
	for _, indexed := range []bool{true, false} {
		_, root, fsys := newWorkspace(t, sampleFiles)
		cfg := Config{Root: root, FS: fsys, ExcludedFolders: []string{"vendor", "vendor/nested"}}
		if indexed {
			cfg.File = filepath.Join(t.TempDir(), "index")
		}
		idx := openIndex(t, cfg)
		assert.Equal(t, indexed, idx.Status().Ready)
		for _, tt := range tests {
			t.Run(fmt.Sprintf("%s indexed=%v", tt.name, indexed), func(t *testing.T) {
				assert.Equal(t, tt.want, search(t, idx, tt.query))
			})
		}
	}
}

func TestSearchInvalidPattern(t *testing.T) {
	_, root, fsys := newWorkspace(t, sampleFiles)
	idx := openIndex(t, Config{Root: root, FS: fsys})
	_, err := idx.RegexpSearch(context.Background(), searchapi.RegexpRequest{Pattern: "a("})
	assert.ErrorContains(t, err, "missing closing )")
	_, err = idx.RegexpSearch(context.Background(), searchapi.RegexpRequest{Pattern: "a", Include: []string{"[a-"}})
	assert.ErrorContains(t, err, `include "[a-"`)
}

func TestIndexStatus(t *testing.T) {
	tests := []struct {
		name   string
		noFile bool
		// opens is set when state opens the index itself.
		opens bool
		// state brings the open index to the state described, and
		// returns what lets it go on, if anything holds it.
		state   func(t *testing.T, e *indexEnv) func()
		want    Status
		wantErr bool
	}{
		{
			name:   "without an index file",
			noFile: true,
			state:  func(*testing.T, *indexEnv) func() { return nil },
			want:   Status{},
		},
		{
			name:  "built for the first time",
			opens: true,
			state: func(t *testing.T, e *indexEnv) func() {
				e.fs.arm()
				e.open(t)
				e.fs.waitEntered(t)
				return e.fs.unblock
			},
			want: Status{Building: true},
		},
		{
			name:  "built",
			state: func(*testing.T, *indexEnv) func() { return nil },
			want:  Status{Ready: true, Files: 2},
		},
		{
			name: "changed since it was built",
			state: func(t *testing.T, e *indexEnv) func() {
				writeFile(t, e.dir, "main.go", "package main\n", time.Now())
				e.handle(textapi.EventTypeFlush, "main.go")
				e.wait(t)
				return nil
			},
			want: Status{Ready: true, Files: 2, Changed: 1},
		},
		{
			name: "built again",
			state: func(t *testing.T, e *indexEnv) func() {
				e.fs.arm()
				require.NoError(t, e.idx.Reindex())
				e.fs.waitEntered(t)
				return e.fs.unblock
			},
			want: Status{Ready: true, Building: true, Files: 2},
		},
		{
			name: "failed to build",
			state: func(t *testing.T, e *indexEnv) func() {
				require.NoError(t, e.idx.Close())
				require.NoError(t, os.Remove(e.file))
				require.NoError(t, os.Mkdir(tmpIndexFile(e.file), 0o755))
				e.open(t)
				e.wait(t)
				return nil
			},
			wantErr: true,
		},
		{
			name: "removed",
			state: func(t *testing.T, e *indexEnv) func() {
				require.NoError(t, e.idx.Remove())
				return nil
			},
			want: Status{Removed: true},
		},
		{
			name: "closed",
			state: func(t *testing.T, e *indexEnv) func() {
				require.NoError(t, e.idx.Close())
				return nil
			},
			want: Status{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newIndexEnv(t, needleFiles, !tt.noFile)
			if !tt.opens {
				e.open(t)
				e.wait(t)
			}
			if release := tt.state(t, e); release != nil {
				defer release()
			}
			got := e.idx.Status()
			if tt.wantErr {
				assert.NotEmpty(t, got.Err)
				got.Err = ""
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildProgressNotification(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		noIndex      bool
		wantNotified []string
		wantUpdates  []string
	}{
		{name: "empty workspace"},
		{name: "without an index", files: sampleFiles, noIndex: true},
		{
			name:         "one file",
			files:        map[string]string{"main.go": "package main\n"},
			wantNotified: []string{"Indexing the workspace for regexp search"},
			wantUpdates: []string{
				"Indexing for regexp search: . 0/1",
				"Indexed 1 file for regexp search 1/1",
			},
		},
		{
			name:         "a folder at a time",
			files:        sampleFiles,
			wantNotified: []string{"Indexing the workspace for regexp search"},
			wantUpdates: []string{
				"Indexing for regexp search: . 0/6",
				"Indexing for regexp search: bin 1/6",
				"Indexing for regexp search: docs 2/6",
				"Indexing for regexp search: . 3/6",
				"Indexing for regexp search: retry 4/6",
				"Indexed 5 files for regexp search 5/5",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, root, fsys := newWorkspace(t, tt.files)
			notify := &recordingNotifications{}
			cfg := Config{Root: root, FS: fsys, ExcludedFolders: []string{"vendor"}, Notify: notify}
			if !tt.noIndex {
				cfg.File = filepath.Join(t.TempDir(), "index")
			}
			openIndex(t, cfg)

			notified, updates := notify.snapshot()
			assert.Equal(t, tt.wantNotified, notified)
			assert.Equal(t, tt.wantUpdates, updates)
		})
	}
}

func TestReindex(t *testing.T) {
	tests := []struct {
		name   string
		noFile bool
		// before runs before the index is opened.
		before func(t *testing.T, e *indexEnv)
		// whileOpening runs while the first build reads main.go.
		whileOpening func(t *testing.T, e *indexEnv)
		// change runs once the index is open, before Reindex.
		change func(t *testing.T, e *indexEnv)
		// whileBuilding runs while the build Reindex asks for reads
		// main.go.
		whileBuilding func(t *testing.T, e *indexEnv)
		reindexes     int
		wantErr       error
		wantStatus    Status
		// wantSearch is nil when the index cannot be searched.
		wantSearch []string
		wantBuilds int
	}{
		{
			name:       "builds the index again",
			reindexes:  1,
			wantStatus: Status{Ready: true, Files: 2},
			wantSearch: bothNeedles,
			wantBuilds: 2,
		},
		{
			name: "reads what changed without events",
			change: func(t *testing.T, e *indexEnv) {
				writeFile(t, e.dir, "main.go", "package main\n", time.Now())
				writeFile(t, e.dir, "lib/new.go", "package lib\n\n// needle\n", time.Now())
				require.Equal(t, []string{"lib/lib.go:3:// needle"}, search(t, e.idx, needle),
					"the index does not know of the new file")
			},
			reindexes:  1,
			wantStatus: Status{Ready: true, Files: 3},
			wantSearch: []string{"lib/lib.go:3:// needle", "lib/new.go:3:// needle"},
			wantBuilds: 2,
		},
		{
			name: "forgets what was removed without events",
			change: func(t *testing.T, e *indexEnv) {
				require.NoError(t, os.Remove(filepath.Join(e.dir, "lib", "lib.go")))
			},
			reindexes:  1,
			wantStatus: Status{Ready: true, Files: 1},
			wantSearch: []string{"main.go:3:// needle"},
			wantBuilds: 2,
		},
		{
			name: "holds the changes searches read since it was built",
			change: func(t *testing.T, e *indexEnv) {
				writeFile(t, e.dir, "main.go", "package main\n\n// needle again\n", time.Now())
				e.handle(textapi.EventTypeFlush, "main.go")
				e.wait(t)
				require.Equal(t, 1, e.idx.Status().Changed)
			},
			reindexes:  1,
			wantStatus: Status{Ready: true, Files: 2},
			wantSearch: []string{"lib/lib.go:3:// needle", "main.go:3:// needle again"},
			wantBuilds: 2,
		},
		{
			name: "clears why the last build failed",
			before: func(t *testing.T, e *indexEnv) {
				require.NoError(t, os.Mkdir(tmpIndexFile(e.file), 0o755))
			},
			change: func(t *testing.T, e *indexEnv) {
				require.NotEmpty(t, e.idx.Status().Err)
				require.NoError(t, os.Remove(tmpIndexFile(e.file)))
			},
			reindexes:  1,
			wantStatus: Status{Ready: true, Files: 2},
			wantSearch: bothNeedles,
			wantBuilds: 2,
		},
		{
			name:       "builds once for the reindexes asked for together",
			reindexes:  3,
			wantStatus: Status{Ready: true, Files: 2},
			wantSearch: bothNeedles,
			wantBuilds: 2,
		},
		{
			name: "builds again when asked while building",
			whileBuilding: func(t *testing.T, e *indexEnv) {
				require.NoError(t, e.idx.Reindex())
			},
			reindexes:  1,
			wantStatus: Status{Ready: true, Files: 2},
			wantSearch: bothNeedles,
			wantBuilds: 3,
		},
		{
			name: "builds again when asked while first built",
			whileOpening: func(t *testing.T, e *indexEnv) {
				require.NoError(t, e.idx.Reindex())
			},
			wantStatus: Status{Ready: true, Files: 2},
			wantSearch: bothNeedles,
			wantBuilds: 2,
		},
		{
			name: "searches the old index while building",
			whileBuilding: func(t *testing.T, e *indexEnv) {
				assert.Equal(t, Status{Ready: true, Building: true, Files: 2}, e.idx.Status())
				assert.Equal(t, bothNeedles, search(t, e.idx, needle))
			},
			reindexes:  1,
			wantStatus: Status{Ready: true, Files: 2},
			wantSearch: bothNeedles,
			wantBuilds: 2,
		},
		{
			name: "searches what changed while building",
			whileBuilding: func(t *testing.T, e *indexEnv) {
				writeFile(t, e.dir, "lib/late.go", "package lib\n\n// needle\n", time.Now())
				e.handle(textapi.EventTypeCreate, "lib/late.go")
			},
			reindexes:  1,
			wantStatus: Status{Ready: true, Files: 2, Changed: 1},
			wantSearch: []string{"lib/late.go:3:// needle", "lib/lib.go:3:// needle", "main.go:3:// needle"},
			wantBuilds: 2,
		},
		{
			name:       "does nothing without an index file",
			noFile:     true,
			reindexes:  1,
			wantStatus: Status{},
			wantSearch: bothNeedles,
		},
		{
			name: "fails once removed",
			change: func(t *testing.T, e *indexEnv) {
				require.NoError(t, e.idx.Remove())
			},
			reindexes:  1,
			wantErr:    ErrRemoved,
			wantStatus: Status{Removed: true},
			wantSearch: bothNeedles,
			wantBuilds: 1,
		},
		{
			name: "fails once closed",
			change: func(t *testing.T, e *indexEnv) {
				require.NoError(t, e.idx.Close())
			},
			reindexes:  1,
			wantErr:    errClosed,
			wantStatus: Status{},
			wantBuilds: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newIndexEnv(t, needleFiles, !tt.noFile)
			if tt.before != nil {
				tt.before(t, e)
			}
			if tt.whileOpening != nil {
				e.fs.arm()
				e.open(t)
				e.fs.waitEntered(t)
				tt.whileOpening(t, e)
				e.fs.unblock()
			} else {
				e.open(t)
			}
			e.wait(t)
			if tt.change != nil {
				tt.change(t, e)
			}
			if tt.whileBuilding != nil {
				e.fs.arm()
			}

			for range tt.reindexes {
				assert.ErrorIs(t, e.idx.Reindex(), tt.wantErr)
			}
			if tt.whileBuilding != nil {
				e.fs.waitEntered(t)
				tt.whileBuilding(t, e)
				e.fs.unblock()
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := e.idx.Wait(ctx); tt.wantErr == errClosed {
				assert.ErrorIs(t, err, errClosed)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantBuilds, e.builds(), "builds notified once Wait returns")
			assert.Equal(t, tt.wantStatus, e.idx.Status())
			if tt.wantSearch != nil {
				assert.Equal(t, tt.wantSearch, search(t, e.idx, needle))
			}
			if e.file != "" && tt.wantErr != errClosed {
				_, err := os.Stat(e.file)
				assert.Equal(t, tt.wantStatus.Ready, err == nil, "the index persists while searches use it")
			}
		})
	}
}

func TestRemove(t *testing.T) {
	other, err := workspaceapi.ParseURI("file:///home/me/other")
	require.NoError(t, err)
	tests := []struct {
		name   string
		noFile bool
		// before runs before the index is opened.
		before func(t *testing.T, e *indexEnv)
		// duringBuild removes the index while its first build reads
		// main.go.
		duringBuild bool
		// change runs once the index is open, before Remove.
		change     func(t *testing.T, e *indexEnv)
		wantErr    error
		wantStatus Status
		// wantIndex is set when the index file is left.
		wantIndex  bool
		wantBuilds int
		// wantLastUpdate is the last update of the build notification,
		// unless empty.
		wantLastUpdate string
	}{
		{
			name: "deletes the index and what an interrupted build left",
			change: func(t *testing.T, e *indexEnv) {
				leftover := fmt.Sprintf("%s.%d.new", e.file, os.Getpid()+1)
				require.NoError(t, os.WriteFile(leftover, nil, 0o600))
			},
			wantStatus:     Status{Removed: true},
			wantBuilds:     1,
			wantLastUpdate: "Indexed 2 files for regexp search 2/2",
		},
		{
			name:           "stops the build under way",
			duringBuild:    true,
			wantStatus:     Status{Removed: true},
			wantBuilds:     1,
			wantLastUpdate: "Indexing for regexp search stopped: " + ErrRemoved.Error() + " 1/1",
		},
		{
			name: "drops the build asked for",
			change: func(t *testing.T, e *indexEnv) {
				require.NoError(t, e.idx.Reindex())
			},
			wantStatus:     Status{Removed: true},
			wantBuilds:     1,
			wantLastUpdate: "Indexed 2 files for regexp search 2/2",
		},
		{
			name: "drops the changes searches read",
			change: func(t *testing.T, e *indexEnv) {
				writeFile(t, e.dir, "main.go", "package main\n\n// needle\n", time.Now())
				e.handle(textapi.EventTypeFlush, "main.go")
			},
			wantStatus:     Status{Removed: true},
			wantBuilds:     1,
			wantLastUpdate: "Indexed 2 files for regexp search 2/2",
		},
		{
			name: "clears why the last build failed",
			before: func(t *testing.T, e *indexEnv) {
				require.NoError(t, os.Mkdir(tmpIndexFile(e.file), 0o755))
			},
			wantStatus: Status{Removed: true},
			wantBuilds: 1,
		},
		{
			name:       "without an index file",
			noFile:     true,
			wantStatus: Status{Removed: true},
		},
		{
			name: "fails once removed",
			change: func(t *testing.T, e *indexEnv) {
				require.NoError(t, e.idx.Remove())
			},
			wantErr:        ErrRemoved,
			wantStatus:     Status{Removed: true},
			wantBuilds:     1,
			wantLastUpdate: "Indexed 2 files for regexp search 2/2",
		},
		{
			name: "fails once closed",
			change: func(t *testing.T, e *indexEnv) {
				require.NoError(t, e.idx.Close())
			},
			wantErr:        errClosed,
			wantStatus:     Status{},
			wantIndex:      true,
			wantBuilds:     1,
			wantLastUpdate: "Indexed 2 files for regexp search 2/2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newIndexEnv(t, needleFiles, !tt.noFile)
			otherIndex := filepath.Base(IndexFile(e.indexDir, other))
			require.NoError(t, os.WriteFile(filepath.Join(e.indexDir, otherIndex), nil, 0o600))
			if tt.before != nil {
				tt.before(t, e)
			}

			var err error
			if tt.duringBuild {
				e.fs.arm()
				e.open(t)
				e.fs.waitEntered(t)
				removed := make(chan error, 1)
				go func() { removed <- e.idx.Remove() }()
				require.Eventually(t, func() bool { return e.idx.ctx.Err() != nil },
					10*time.Second, time.Millisecond, "Remove stops the build")
				e.fs.unblock()
				err = <-removed
			} else {
				e.open(t)
				e.wait(t)
				if tt.change != nil {
					tt.change(t, e)
				}
				err = e.idx.Remove()
			}
			assert.ErrorIs(t, err, tt.wantErr)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := e.idx.Wait(ctx); tt.wantErr == errClosed {
				assert.ErrorIs(t, err, errClosed)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tt.wantStatus, e.idx.Status())
			want := []string{otherIndex}
			if tt.wantIndex {
				want = append(want, filepath.Base(e.file))
			}
			assert.ElementsMatch(t, want, e.indexFiles(t), "only the index of the workspace is deleted")
			assert.Equal(t, tt.wantBuilds, e.builds())
			if tt.wantLastUpdate != "" {
				assert.Equal(t, tt.wantLastUpdate, e.lastUpdate())
			}
			if tt.wantErr == errClosed {
				return
			}

			writeFile(t, e.dir, "lib/new.go", "package lib\n\n// needle\n", time.Now())
			assert.False(t, e.handle(textapi.EventTypeCreate, "lib/new.go"))
			require.NoError(t, e.idx.Wait(ctx))
			assert.Equal(t, Status{Removed: true}, e.idx.Status(), "the index is not kept anymore")
			assert.Equal(t, []string{"lib/lib.go:3:// needle", "lib/new.go:3:// needle", "main.go:3:// needle"},
				search(t, e.idx, needle), "searches read every file")
			assert.Equal(t, tt.wantBuilds, e.builds(), "the index is not built again")
		})
	}
}

func TestHandle(t *testing.T) {
	files := map[string]string{
		"main.go":     "package main\n",
		"lib/lib.go":  "package lib\n",
		"lib/util.go": "package lib\n",
	}
	outside, err := workspaceapi.ParseURI("file:///elsewhere/main.go")
	require.NoError(t, err)
	tests := []struct {
		name   string
		noFile bool
		// closes is set when state closes the index.
		closes bool
		state  func(t *testing.T, e *indexEnv)
		ev     func(e *indexEnv) textapi.Event
		// wantChanged is how many files searches read once the event is
		// handled, and wantSettled once the index looked at it.
		wantChanged, wantSettled int
	}{
		{
			name:        "a file flushed",
			ev:          func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeFlush, "main.go") },
			wantChanged: 1, wantSettled: 1,
		},
		{
			name:        "a file changed on disk",
			ev:          func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeChange, "main.go") },
			wantChanged: 1, wantSettled: 1,
		},
		{
			name:        "a file created",
			ev:          func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeCreate, "main.go") },
			wantChanged: 1, wantSettled: 1,
		},
		{
			name:        "a file removed",
			ev:          func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeRemove, "gone.go") },
			wantChanged: 1, wantSettled: 1,
		},
		{
			name:        "a file renamed",
			ev:          func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeRename, "main.go") },
			wantChanged: 1, wantSettled: 1,
		},
		{
			name:        "a folder created",
			ev:          func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeCreate, "lib") },
			wantChanged: 1, wantSettled: 2,
		},
		{
			name:        "a file searches leave out",
			ev:          func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeCreate, ".hidden/x.go") },
			wantChanged: 1, wantSettled: 0,
		},
		{
			name: "a file edited",
			ev:   func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeEdit, "main.go") },
		},
		{
			name: "a file opened",
			ev:   func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeOpen, "main.go") },
		},
		{
			name: "a file closed",
			ev:   func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeClose, "main.go") },
		},
		{
			name: "a file outside the workspace",
			ev:   func(*indexEnv) textapi.Event { return textapi.Event{Type: textapi.EventTypeFlush, URI: outside} },
		},
		{
			name: "the workspace itself",
			ev:   func(e *indexEnv) textapi.Event { return textapi.Event{Type: textapi.EventTypeChange, URI: e.root} },
		},
		{
			name:   "without an index file",
			noFile: true,
			ev:     func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeFlush, "main.go") },
		},
		{
			name:  "once removed",
			state: func(t *testing.T, e *indexEnv) { require.NoError(t, e.idx.Remove()) },
			ev:    func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeFlush, "main.go") },
		},
		{
			name:   "once closed",
			closes: true,
			state:  func(t *testing.T, e *indexEnv) { require.NoError(t, e.idx.Close()) },
			ev:     func(e *indexEnv) textapi.Event { return event(e.root, textapi.EventTypeFlush, "main.go") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newIndexEnv(t, files, !tt.noFile)
			e.open(t)
			e.wait(t)
			if tt.state != nil {
				tt.state(t, e)
			}

			assert.False(t, e.idx.Handle(context.Background(), tt.ev(e)), "other handlers see the event too")
			assert.Equal(t, tt.wantChanged, e.idx.Status().Changed)
			if tt.closes {
				return
			}
			e.wait(t)
			assert.Equal(t, tt.wantSettled, e.idx.Status().Changed)
			wantBuilds := 1
			if tt.noFile {
				wantBuilds = 0
			}
			assert.Equal(t, wantBuilds, e.builds(), "too few changes to build the index again")
		})
	}
}

func TestBuildFailureFinishesTheProgressNotification(t *testing.T) {
	_, root, fsys := newWorkspace(t, sampleFiles)
	file := filepath.Join(t.TempDir(), "index")
	require.NoError(t, os.Mkdir(fmt.Sprintf("%s.%d.new", file, os.Getpid()), 0o755),
		"the writer cannot publish its temporary file")
	notify := &recordingNotifications{}
	idx := openIndex(t, Config{Root: root, FS: fsys, File: file, Notify: notify})
	why := idx.Status().Err
	require.NotEmpty(t, why)

	notified, updates := notify.snapshot()
	assert.Equal(t, []string{"Indexing the workspace for regexp search"}, notified)
	require.NotEmpty(t, updates)
	assert.Equal(t, "Indexing for regexp search stopped: "+why+" 1/1", updates[len(updates)-1])
}

func TestSearchSeesChangesSinceIndexed(t *testing.T) {
	dir, root, fsys := newWorkspace(t, sampleFiles)
	idx := openIndex(t, Config{
		Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index"), ExcludedFolders: []string{"vendor"},
	})

	writeFile(t, dir, "main.go", "package main\n\nfunc main() { Palindrome() }\n", time.Now())
	writeFile(t, dir, "new/new.go", "package new\n\n// Palindrome\n", time.Now())
	require.NoError(t, os.Remove(filepath.Join(dir, "docs", "README.md")))
	writeFile(t, dir, "tmp/scratch.go", "// Palindrome, ignored\n", time.Now())
	idx.Handle(context.Background(), event(root, textapi.EventTypeFlush, "main.go"))
	idx.Handle(context.Background(), event(root, textapi.EventTypeCreate, "new"))
	idx.Handle(context.Background(), event(root, textapi.EventTypeRemove, "docs/README.md"))
	idx.Handle(context.Background(), event(root, textapi.EventTypeChange, "tmp/scratch.go"))

	check := func(t *testing.T) {
		assert.Equal(t, []string{
			"main.go:3:func main() { Palindrome() }",
			"new/new.go:3:// Palindrome",
		}, search(t, idx, searchapi.RegexpRequest{Pattern: "Palindrome"}))
		assert.Equal(t, []string{"new/new.go:3:// Palindrome"},
			search(t, idx, searchapi.RegexpRequest{Pattern: "Palindrome", Roots: []string{"new/new.go"}}))
		assert.Equal(t, []string{
			"retry/retry.go:3:// Retry retries with backoff.",
			"retry/retry.go:4:func Retry(n int) {}",
		}, search(t, idx, searchapi.RegexpRequest{Pattern: "Retry"}))
	}
	t.Run("before the events are looked at", check)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, idx.Wait(ctx))
	assert.Equal(t, 3, idx.Status().Changed,
		"the folder is replaced by its file, and what git ignores is dropped")
	t.Run("after", check)
}

func TestOpenFindsChangesSinceIndexed(t *testing.T) {
	dir, root, fsys := newWorkspace(t, sampleFiles)
	cfg := Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index")}
	require.NoError(t, openIndex(t, cfg).Close())

	writeFile(t, dir, "main.go", "package main\n\n// Palindrome\n", time.Now())
	writeFile(t, dir, "moved.go", "// Palindrome\n", old)

	idx := openIndex(t, cfg)
	assert.Equal(t, Status{Ready: true, Files: 7, Changed: 1}, idx.Status())
	assert.Contains(t, idx.unindexed, "moved.go")
	assert.Equal(t, []string{
		"main.go:3:// Palindrome",
		"moved.go:1:// Palindrome",
	}, search(t, idx, searchapi.RegexpRequest{Pattern: "Palindrome"}))
}

func TestIndexBuiltAgainAfterManyChanges(t *testing.T) {
	dir, root, fsys := newWorkspace(t, sampleFiles)
	notify := &recordingNotifications{}
	idx := openIndex(t, Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index"), Notify: notify})

	for n := range rebuildAfter + 1 {
		name := fmt.Sprintf("gen/f%03d.go", n)
		writeFile(t, dir, name, fmt.Sprintf("// Generated %d\n", n), time.Now())
		idx.Handle(context.Background(), event(root, textapi.EventTypeCreate, name))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, idx.Wait(ctx))

	assert.Equal(t, Status{Ready: true, Files: 7 + rebuildAfter + 1}, idx.Status())
	assert.Equal(t, []string{"gen/f042.go:1:// Generated 42"}, search(t, idx, searchapi.RegexpRequest{Pattern: `Generated 42$`}))

	notified, updates := notify.snapshot()
	assert.Equal(t, []string{
		"Indexing the workspace for regexp search",
		"Indexing the workspace for regexp search",
	}, notified, "building it again is notified too")
	assert.Equal(t, fmt.Sprintf("Indexed %d files for regexp search %[1]d/%[1]d", 7+rebuildAfter+1),
		updates[len(updates)-1])
}

func TestCorruptIndexIsBuiltAgain(t *testing.T) {
	_, root, fsys := newWorkspace(t, sampleFiles)
	file := filepath.Join(t.TempDir(), "index")
	require.NoError(t, os.WriteFile(file, []byte("not an index"), 0o600))

	idx := openIndex(t, Config{Root: root, FS: fsys, File: file})
	assert.True(t, idx.Status().Ready)
	assert.Equal(t, []string{"retry/backoff.go:3:func backoff(n int) int { return n * n }"},
		search(t, idx, searchapi.RegexpRequest{Pattern: "func backoff"}))
}

func TestSearchFilesOmittedByIndex(t *testing.T) {
	files := map[string]string{
		"short.txt": "needle\n",
		"long.txt":  strings.Repeat("x", 3000) + " needle\n",
		"utf8.txt":  "\xff needle\n",
	}
	_, root, fsys := newWorkspace(t, files)
	cfg := Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index")}
	for _, name := range []string{"built", "reopened"} {
		t.Run(name, func(t *testing.T) {
			idx := openIndex(t, cfg)
			defer idx.Close()
			assert.Equal(t, []string{
				"long.txt:1:" + strings.TrimSuffix(files["long.txt"], "\n"),
				"short.txt:1:needle",
				"utf8.txt:1:\xff needle",
			}, search(t, idx, searchapi.RegexpRequest{Pattern: "needle"}))
		})
	}
}

func TestUnindexedFilesDoNotCauseRepeatedRebuilds(t *testing.T) {
	files := map[string]string{"normal.txt": "needle"}
	for n := range rebuildAfter + 1 {
		files[fmt.Sprintf("long%03d.txt", n)] = strings.Repeat("x", 3000) + " needle"
	}
	_, root, fsys := newWorkspace(t, files)
	notify := &recordingNotifications{}
	cfg := Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index"), Notify: notify}
	require.NoError(t, openIndex(t, cfg).Close())
	idx := openIndex(t, cfg)
	idx.Handle(context.Background(), event(root, textapi.EventTypeChange, "normal.txt"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, idx.Wait(ctx))
	assert.Len(t, search(t, idx, searchapi.RegexpRequest{Pattern: "needle"}), len(files))
	notified, _ := notify.snapshot()
	assert.Len(t, notified, 1)
}

func TestIndexPathOrder(t *testing.T) {
	_, root, fsys := newWorkspace(t, map[string]string{
		"a.go": "needle", "a/file.go": "needle", "a-b.go": "needle",
	})
	idx := openIndex(t, Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index")})
	assert.Equal(t, Status{Ready: true, Files: 3}, idx.Status())
	assert.Equal(t, []string{"a/file.go:1:needle", "a-b.go:1:needle", "a.go:1:needle"},
		search(t, idx, searchapi.RegexpRequest{Pattern: "needle"}))
}

func TestTruncatedIndexNamesAreRebuilt(t *testing.T) {
	_, root, fsys := newWorkspace(t, map[string]string{"file.txt": "needle"})
	notify := &recordingNotifications{}
	cfg := Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index"), Notify: notify}
	require.NoError(t, openIndex(t, cfg).Close())
	data, err := os.ReadFile(cfg.File)
	require.NoError(t, err)
	trailer := len(data) - len("\ncsearch trlr 2\n") - 8*8
	binary.BigEndian.PutUint64(data[trailer+3*8:], 2) // one name fewer than the trailer promises
	require.NoError(t, os.WriteFile(cfg.File, data, 0o600))
	idx := openIndex(t, cfg)
	assert.Equal(t, Status{Ready: true, Files: 1}, idx.Status())
	assert.Equal(t, []string{"file.txt:1:needle"}, search(t, idx, searchapi.RegexpRequest{Pattern: "needle"}))
	notified, _ := notify.snapshot()
	assert.Len(t, notified, 2)
}

type cancelReadFS struct {
	workspaceapi.FileSystem
	cancel context.CancelFunc
	file   *cancelReadFile
}

func (f *cancelReadFS) OpenFile(name string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	file, err := f.FileSystem.OpenFile(name, flag, mode)
	if err != nil || name != "file.txt" {
		return file, err
	}
	f.file = &cancelReadFile{File: file, cancel: f.cancel}
	return f.file, nil
}

type cancelReadFile struct {
	workspaceapi.File
	cancel context.CancelFunc
	closed bool
}

func (f *cancelReadFile) Read(b []byte) (int, error) {
	n, err := f.File.Read(b)
	f.cancel()
	return n, err
}

func (f *cancelReadFile) Close() error {
	f.closed = true
	return f.File.Close()
}

func TestCanceledBuildKeepsPublishedIndex(t *testing.T) {
	for _, duringRead := range []bool{true, false} {
		t.Run(fmt.Sprintf("duringRead=%v", duringRead), func(t *testing.T) {
			_, root, fsys := newWorkspace(t, map[string]string{"file.txt": "needle"})
			cfg := Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index")}
			require.NoError(t, openIndex(t, cfg).Close())
			before, err := os.ReadFile(cfg.File)
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fs := &cancelReadFS{FileSystem: fsys, cancel: cancel}
			if duringRead {
				cfg.FS = fs
			}
			base, err := index.Open(cfg.File)
			require.NoError(t, err)
			defer base.Close()
			idx := &Index{cfg: cfg, ctx: ctx, base: base, ready: true, updated: make(chan struct{})}
			prog := &buildProgress{notify: &recordingNotifications{}, schedule: func(fn func()) bool { fn(); return true }}
			tmp, paths, err := idx.write(time.Now(), prog)
			if duringRead {
				require.ErrorIs(t, err, context.Canceled)
				require.NotNil(t, fs.file)
				assert.True(t, fs.file.closed)
			} else {
				require.NoError(t, err)
				cancel()
			}
			_, err = idx.finish(tmp, paths, err)
			require.ErrorIs(t, err, context.Canceled)
			assert.Same(t, base, idx.base)
			assert.True(t, idx.ready)
			after, err := os.ReadFile(cfg.File)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			files, err := filepath.Glob(cfg.File + "*")
			require.NoError(t, err)
			assert.Equal(t, []string{cfg.File}, files)
		})
	}
}

func TestCorruptPostingsFallBackAndReindex(t *testing.T) {
	_, root, fsys := newWorkspace(t, map[string]string{"file.txt": "aaaa"})
	notify := &recordingNotifications{}
	cfg := Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index"), Notify: notify}
	require.NoError(t, openIndex(t, cfg).Close())
	data, err := os.ReadFile(cfg.File)
	require.NoError(t, err)
	trailer := len(data) - len("\ncsearch trlr 2\n") - 8*8
	postData := binary.BigEndian.Uint64(data[trailer+4*8:])
	data[postData+3] = 0 // damage the only posting list, leaving the names readable
	require.NoError(t, os.WriteFile(cfg.File, data, 0o600))

	idx := openIndex(t, cfg)
	assert.Equal(t, []string{"file.txt:1:aaaa"}, search(t, idx, searchapi.RegexpRequest{Pattern: "aaa"}))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, idx.Wait(ctx))
	assert.True(t, idx.Status().Ready)
	notified, _ := notify.snapshot()
	assert.Len(t, notified, 2, "the corrupt index was rebuilt after the fallback search")
}

func TestSearchAfterClose(t *testing.T) {
	_, root, fsys := newWorkspace(t, sampleFiles)
	idx := openIndex(t, Config{Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index")})
	require.NoError(t, idx.Close())
	require.NoError(t, idx.Close())
	_, err := idx.RegexpSearch(context.Background(), searchapi.RegexpRequest{Pattern: "Retry"})
	assert.ErrorIs(t, err, errClosed)
	assert.False(t, idx.Handle(context.Background(), event(root, textapi.EventTypeFlush, "main.go")))
}

func TestCleanupWorkspaceHook(t *testing.T) {
	root, err := workspaceapi.ParseURI("file:///home/me/project")
	require.NoError(t, err)
	other, err := workspaceapi.ParseURI("file:///home/me/other")
	require.NoError(t, err)
	tests := []struct {
		name string
		// files are the files of the folder of the indexes, by the
		// workspace whose index they are.
		files    map[workspaceapi.URI][]string
		noFolder bool
		want     []string
	}{
		{
			name: "the index and what interrupted builds left",
			files: map[workspaceapi.URI][]string{
				root:  {"", ".42.new", ".42.new.tmp123"},
				other: {"", ".7.new"},
			},
			want: []string{"", ".7.new"},
		},
		{
			name:  "a workspace never indexed",
			files: map[workspaceapi.URI][]string{other: {""}},
			want:  []string{""},
		},
		{
			name:     "no folder of indexes",
			noFolder: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.noFolder {
				dir = filepath.Join(dir, "missing")
			}
			for ws, suffixes := range tt.files {
				for _, suffix := range suffixes {
					require.NoError(t, os.WriteFile(IndexFile(dir, ws)+suffix, nil, 0o600))
				}
			}

			require.NoError(t, CleanupWorkspaceHook(dir)(context.Background(), root))

			want := []string{}
			for _, suffix := range tt.want {
				want = append(want, filepath.Base(IndexFile(dir, other))+suffix)
			}
			got := []string{}
			if entries, err := os.ReadDir(dir); err == nil {
				for _, entry := range entries {
					got = append(got, entry.Name())
				}
			}
			assert.ElementsMatch(t, want, got, "the indexes of other workspaces are kept")
		})
	}
}
