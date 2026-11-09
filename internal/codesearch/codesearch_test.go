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
	_, root, fsys := newWorkspace(t, sampleFiles)
	idx := openIndex(t, Config{
		Root: root, FS: fsys, File: filepath.Join(t.TempDir(), "index"), ExcludedFolders: []string{"vendor"},
	})
	assert.Equal(t, Status{Ready: true, Files: 5}, idx.Status())
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
	dir := t.TempDir()
	root, err := workspaceapi.ParseURI("file:///home/me/project")
	require.NoError(t, err)
	other, err := workspaceapi.ParseURI("file:///home/me/other")
	require.NoError(t, err)
	file := IndexFile(dir, root)
	for _, f := range []string{file, file + ".42.new", file + ".42.new.tmp123", IndexFile(dir, other)} {
		require.NoError(t, os.WriteFile(f, nil, 0o600))
	}

	require.NoError(t, CleanupWorkspaceHook(dir)(context.Background(), root))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, filepath.Base(IndexFile(dir, other)), entries[0].Name())
}
