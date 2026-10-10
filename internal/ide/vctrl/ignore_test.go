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

package vctrl

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"go.uber.org/mock/gomock"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/schemetest"
	"unstable.build/rune/internal/workspace/workspaceapitest"
)

func TestLoadGitignore(t *testing.T) {
	t.Run("uses .gitignore excludes", func(t *testing.T) {
		cwd := newScheme(t)
		touchGitignore(t, cwd, ".ox.awe")
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		assert.True(t, matcher.Match(makeURI(t, cwd, ".ox.awe"), false))
		assert.False(t, matcher.Match(makeURI(t, cwd, ".ox.go"), false))
	})

	t.Run("uses recursive .gitignore excludes", func(t *testing.T) {
		cwd := newFileScheme(t) // memscheme doesn't support dirs
		touchGitignoreAt(t, cwd, ".ox.CALIU", gitIgnoreFile)
		touchGitignoreAt(t, cwd, ".ox.BOIRA", filepath.Join("dir", "moreDirs", gitIgnoreFile))
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		assert.True(t, matcher.Match(makeURI(t, cwd, ".ox.CALIU"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, "dir/moreDirs/.ox.BOIRA"), false))
		assert.False(t, matcher.Match(makeURI(t, cwd, ".ox.go"), false))
	})

	t.Run("nested .gitignore patterns are scoped to their directory", func(t *testing.T) {
		cwd := newFileScheme(t) // memscheme doesn't support dirs
		// vendor/.gitignore says "*" — must only match inside vendor/, not
		// across the whole workspace.
		touchGitignoreAt(t, cwd, "*", filepath.Join("vendor", gitIgnoreFile))
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		// Inside vendor/: pattern applies.
		assert.True(t, matcher.Match(makeURI(t, cwd, "vendor/foo.go"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, "vendor/sub/bar.go"), false))

		// Outside vendor/: pattern must NOT apply. This is the regression
		// from a nested pattern leaking globally and matching every entry.
		assert.False(t, matcher.Match(makeURI(t, cwd, "main.go"), false))
		assert.False(t, matcher.Match(makeURI(t, cwd, "cmd"), true))
		assert.False(t, matcher.Match(makeURI(t, cwd, "cmd/main.go"), false))
		assert.False(t, matcher.Match(makeURI(t, cwd, "pkg/lib.go"), false))
	})

	t.Run("uses .gitignore entire directory excludes", func(t *testing.T) {
		cwd := newScheme(t)
		touchGitignore(t, cwd, ".ox/")
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		assert.True(t, matcher.Match(makeURI(t, cwd, ".ox/awe"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, ".ox/awe/inspiring/ide"), false))
		assert.False(t, matcher.Match(makeURI(t, cwd, ".ox.go"), false))
	})

	t.Run("uses .gitignore excludes with comments", func(t *testing.T) {
		cwd := newScheme(t)
		touchGitignore(t, cwd, "#this is a comment\n.ox.awe\n#this is another comment")
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		assert.True(t, matcher.Match(makeURI(t, cwd, ".ox.awe"), false))
	})

	t.Run("file does not exist, uses common ignores", func(t *testing.T) {
		cwd := newScheme(t)
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		assert.True(t, matcher.Match(makeURI(t, cwd, "file.swp"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, ".file.swp"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, "file.rswp"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, ".file.rswp"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, ".ox.sock"), false))
		assert.False(t, matcher.Match(makeURI(t, cwd, ".ox.awe"), false))
	})

	t.Run("common ignores exclude dependency and build noise dirs", func(t *testing.T) {
		cwd := newScheme(t)
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		// Python tooling / virtualenv / caches.
		assert.True(t, matcher.Match(makeURI(t, cwd, ".venv/lib/x.py"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, ".tox/py3/x.py"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, ".mypy_cache/x"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, ".pytest_cache/x"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, "__pycache__/z.pyc"), false))
		// Nested virtualenv/caches in a monorepo sub-package.
		assert.True(t, matcher.Match(makeURI(t, cwd, "pkg/.venv/lib/x.py"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, "pkg/__pycache__/z.pyc"), false))

		// JS/TS dependencies (unanchored, matches at any depth).
		assert.True(t, matcher.Match(makeURI(t, cwd, "node_modules/y.js"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, "pkg/node_modules/y.js"), false))

		// Rust build output is root-anchored.
		assert.True(t, matcher.Match(makeURI(t, cwd, "target/debug/app"), false))

		// Real source is not excluded.
		assert.False(t, matcher.Match(makeURI(t, cwd, "src/main.py"), false))
		// A nested user dir literally named "target" must NOT be excluded
		// (Rust pattern is root-anchored).
		assert.False(t, matcher.Match(makeURI(t, cwd, "src/target/x.rs"), false))
	})

	t.Run("VCS metadata dirs are hidden as entries and at any depth", func(t *testing.T) {
		cwd := newScheme(t)
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		// The explorer queries the bare directory entry with isDir=true;
		// it must be hidden, not just its contents.
		for _, dir := range []string{".git", ".hg", ".svn", ".bzr", ".DS_Store"} {
			assert.True(t, matcher.Match(makeURI(t, cwd, dir), true),
				"%s entry must be hidden", dir)
			assert.True(t, matcher.Match(makeURI(t, cwd, dir+"/inner"), false),
				"%s contents must be hidden", dir)
			// A nested repo/checkout under the workspace root (e.g. the
			// file explorer opened in $HOME navigating into src/blue/.git).
			assert.True(t, matcher.Match(makeURI(t, cwd, "src/blue/"+dir), true),
				"nested %s entry must be hidden", dir)
			assert.True(t, matcher.Match(makeURI(t, cwd, "src/blue/"+dir+"/HEAD"), false),
				"nested %s contents must be hidden", dir)
		}
	})

	t.Run("uri returns error is bubbled up", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := schemetest.NewMockScheme(ctrl)
		f := workspaceapitest.NewMockFile(ctrl)

		f.EXPECT().Read(gomock.Any()).Return(0, io.EOF).AnyTimes()
		f.EXPECT().Close().Return(nil).AnyTimes()
		mock.EXPECT().OpenFile(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(f, nil).
			AnyTimes()
		mock.EXPECT().URI(gomock.Any()).Return(workspaceapi.URI{}, errors.New("boom"))
		_, err := LoadGitignore(mock)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "boom")
	})

	t.Run("open .gitignore returns permissions error is ignored", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := schemetest.NewMockScheme(ctrl)

		uri, err := workspaceapi.ParseURI("memory:///tmp")
		require.NoError(t, err)

		// Construction reads only the workspace root: .git/info/exclude
		// then .gitignore. No directory listing is performed.
		mock.EXPECT().OpenFile(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, os.ErrPermission).
			Times(2)
		mock.EXPECT().URI(gomock.Any()).Return(uri, nil)
		_, err = LoadGitignore(mock)
		require.NoError(t, err)
	})

	t.Run(".gitignore is malformed, uses common patterns", func(t *testing.T) {
		cwd := newScheme(t)
		touchGitignore(t, cwd, "\x00\x00\x00{}jfl\x00kewjlkfw\njfklwjjk##")
		matcher, err := LoadGitignore(cwd)
		require.NoError(t, err)

		assert.False(t, matcher.Match(makeURI(t, cwd, ".ox.awe"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, ".ox.sock"), false))
		assert.True(t, matcher.Match(makeURI(t, cwd, "filename.swp"), false))
	})

	t.Run("construction reads only the workspace root", func(t *testing.T) {
		cwd := newFileScheme(t)
		touchGitignoreAt(t, cwd, ".ox.CALIU", gitIgnoreFile)
		touchGitignoreAt(t, cwd, ".ox.BOIRA", filepath.Join("dir", "moreDirs", gitIgnoreFile))
		counting := &countingReader{FileReader: cwd}

		_, err := LoadGitignore(counting)
		require.NoError(t, err)

		// No directory listing, and only the root's .gitignore /
		// .git/info/exclude are opened — never the deep one.
		assert.Zero(t, counting.readDirs, "must not walk the tree")
		for _, p := range counting.openedPaths() {
			assert.NotContains(t, p, "moreDirs",
				"deep .gitignore must not be read at construction")
		}
	})

	t.Run("unreadable nested .gitignore is logged not silently dropped", func(t *testing.T) {
		hook := logtest.NewGlobal()
		t.Cleanup(hook.Reset)

		ctrl := gomock.NewController(t)
		mock := schemetest.NewMockScheme(ctrl)

		uri, err := workspaceapi.ParseURI("memory:///tmp")
		require.NoError(t, err)
		mock.EXPECT().URI(gomock.Any()).Return(uri, nil).AnyTimes()

		// The nested dir/.gitignore exists but cannot be read; every other
		// ignore-file probe reports "not found" (the normal, silent case).
		deep := filepath.Join("dir", gitIgnoreFile)
		mock.EXPECT().
			OpenFile(deep, gomock.Any(), gomock.Any()).
			Return(nil, os.ErrPermission).
			AnyTimes()
		mock.EXPECT().
			OpenFile(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, os.ErrNotExist).
			AnyTimes()

		matcher, err := LoadGitignore(mock)
		require.NoError(t, err)
		require.Empty(t, hook.AllEntries(),
			"construction must not log: missing root files are normal")

		// Matching a path under dir/ forces the unreadable file to be read.
		matcher.MatchRelPath(filepath.Join("dir", "secret.txt"), false)

		entry := hook.LastEntry()
		require.NotNil(t, entry, "an unreadable .gitignore must be logged")
		assert.Equal(t, log.WarnLevel, entry.Level)
		assert.Contains(t, entry.Message, gitIgnoreFile)
		assert.Contains(t, entry.Message, "dir")
		assert.Contains(t, entry.Message, os.ErrPermission.Error())
	})

	t.Run("gitlink .git file is treated as missing, not logged", func(t *testing.T) {
		hook := logtest.NewGlobal()
		t.Cleanup(hook.Reset)

		ctrl := gomock.NewController(t)
		mock := schemetest.NewMockScheme(ctrl)

		uri, err := workspaceapi.ParseURI("memory:///tmp")
		require.NoError(t, err)
		mock.EXPECT().URI(gomock.Any()).Return(uri, nil).AnyTimes()

		// dir/.git is a gitlink file (submodule/worktree pointer), so
		// probing dir/.git/info/exclude fails with ENOTDIR.
		exclude := filepath.Join("dir", infoExcludeFile)
		mock.EXPECT().
			OpenFile(exclude, gomock.Any(), gomock.Any()).
			Return(nil, &fs.PathError{
				Op: "open", Path: exclude, Err: syscall.ENOTDIR,
			}).
			AnyTimes()
		mock.EXPECT().
			OpenFile(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(nil, os.ErrNotExist).
			AnyTimes()

		matcher, err := LoadGitignore(mock)
		require.NoError(t, err)

		matcher.MatchRelPath(filepath.Join("dir", "file.txt"), false)
		assert.Empty(t, hook.AllEntries(),
			"a gitlink .git file is the normal submodule/worktree layout")
	})

	t.Run("nested .gitignore is read lazily and cached per directory", func(t *testing.T) {
		cwd := newFileScheme(t)
		touchGitignoreAt(t, cwd, ".ox.BOIRA", filepath.Join("dir", "moreDirs", gitIgnoreFile))
		counting := &countingReader{FileReader: cwd}

		matcher, err := LoadGitignore(counting)
		require.NoError(t, err)

		deep := filepath.Join("dir", "moreDirs", gitIgnoreFile)
		require.NotContains(t, counting.openedPaths(), deep)

		// Matching a path under dir/moreDirs reads that directory's
		// .gitignore and applies it.
		assert.True(t, matcher.Match(makeURI(t, cwd, "dir/moreDirs/.ox.BOIRA"), false))
		assert.Contains(t, counting.openedPaths(), deep)

		opensAfterFirst := counting.opens
		// A sibling lookup in the same directory must hit the cache and
		// open nothing new.
		assert.False(t, matcher.Match(makeURI(t, cwd, "dir/moreDirs/keep.go"), false))
		assert.Equal(t, opensAfterFirst, counting.opens,
			"sibling lookup must not re-open cached .gitignore files")
	})

	t.Run("nested directory patterns are parsed once and cached", func(t *testing.T) {
		cwd := newFileScheme(t)
		touchGitignoreAt(t, cwd, ".ox.BOIRA", filepath.Join("dir", "moreDirs", gitIgnoreFile))
		counting := &countingReader{FileReader: cwd}

		matcher, err := LoadGitignore(counting)
		require.NoError(t, err)

		// Constructing the matcher reads only the root: its .gitignore and
		// .git/info/exclude. Nothing under dir/ is touched yet.
		opensAfterConstruct := counting.opens

		// The first lookup under dir/moreDirs reads each ancestor's
		// .gitignore and .git/info/exclude exactly once: dir/ and
		// dir/moreDirs/ -> 2 dirs * 2 files = 4 opens. The root is already
		// cached from construction.
		assert.True(t, matcher.Match(makeURI(t, cwd, "dir/moreDirs/.ox.BOIRA"), false))
		assert.Equal(t, opensAfterConstruct+4, counting.opens,
			"first nested lookup reads each uncached ancestor's ignore files once")

		opensAfterFirst := counting.opens

		// Re-matching the same path and siblings whose ancestor chain is
		// already cached (root, dir/, dir/moreDirs/) must be served entirely
		// from the per-directory cache without re-opening any ignore file.
		for _, rel := range []string{
			"dir/moreDirs/.ox.BOIRA",
			"dir/moreDirs/sibling.go",
			"dir/another.go",
		} {
			matcher.Match(makeURI(t, cwd, rel), false)
		}
		assert.Equal(t, opensAfterFirst, counting.opens,
			"cached nested directories must not be re-parsed")
	})

	t.Run("deeply nested directory chain is fully cached", func(t *testing.T) {
		cwd := newFileScheme(t)
		// .gitignore lives at the bottom of a long ancestor chain.
		chain := []string{"a", "b", "c", "d", "e"}
		under := func(leaf string) string {
			return filepath.Join(append(slices.Clone(chain), leaf)...)
		}
		touchGitignoreAt(t, cwd, "*.tmp", under(gitIgnoreFile))
		counting := &countingReader{FileReader: cwd}

		matcher, err := LoadGitignore(counting)
		require.NoError(t, err)
		opensAfterConstruct := counting.opens

		deepFile := under("x.tmp")
		// The first lookup reads .gitignore + .git/info/exclude for each of
		// the five uncached ancestor directories (a .. a/b/c/d/e); the root
		// was cached at construction.
		assert.True(t, matcher.Match(makeURI(t, cwd, deepFile), false))
		assert.Equal(t, opensAfterConstruct+len(chain)*2, counting.opens,
			"each ancestor in the deep chain is read exactly once")

		opensAfterFirst := counting.opens
		// Re-matching the same deep path and a sibling beside it must be
		// served entirely from the per-directory cache: every directory in
		// the chain, not just the leaf, stays cached.
		assert.True(t, matcher.Match(makeURI(t, cwd, deepFile), false))
		assert.False(t, matcher.Match(makeURI(t, cwd, under("keep.go")), false))
		assert.Equal(t, opensAfterFirst, counting.opens,
			"the entire deep chain must remain cached")
	})

	t.Run("concurrent matches are race-free", func(t *testing.T) {
		cwd := newFileScheme(t)
		touchGitignoreAt(t, cwd, "*.tmp", filepath.Join("a", "b", gitIgnoreFile))
		matcher, err := LoadGitignore(&countingReader{FileReader: cwd})
		require.NoError(t, err)

		var wg sync.WaitGroup
		for range 32 {
			wg.Go(func() {
				assert.True(t, matcher.Match(makeURI(t, cwd, "a/b/x.tmp"), false))
				assert.False(t, matcher.Match(makeURI(t, cwd, "a/b/x.go"), false))
			})
		}
		wg.Wait()
	})
}

func TestHiddenBaseMatcher(t *testing.T) {
	m := HiddenBaseMatcher()

	cases := []struct {
		relpath string
		isDir   bool
		want    bool
	}{
		{".git", true, true},
		{".DS_Store", false, true},
		{"sub/.git", true, true},
		{"sub/.gitignore", false, true},
		{"src", true, false},
		{"src/main.go", false, false},
		{"a/b.c/d", false, false},
		{".", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.relpath, func(t *testing.T) {
			assert.Equal(t, tc.want,
				m.MatchRelPath(tc.relpath, tc.isDir),
				"MatchRelPath(%q, %v)", tc.relpath, tc.isDir)
		})
	}

	// Match goes through workspaceapi.URI: only the basename
	// portion of the path drives the decision.
	cwd := newScheme(t)
	assert.True(t, m.Match(makeURI(t, cwd, ".git"), true))
	assert.True(t, m.Match(makeURI(t, cwd, "deep/.git"), true))
	assert.False(t, m.Match(makeURI(t, cwd, "src"), true))
}

func newScheme(t *testing.T) schemeapi.Scheme {
	uri, err := workspaceapi.ParseURI("memory:///tmp")
	require.NoError(t, err)
	scheme, err := workspace.NewMemoryScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	return scheme
}

func newFileScheme(t *testing.T) schemeapi.Scheme {
	tempDir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(tempDir)
	})
	uri, err := workspaceapi.ParseURI(filepath.Join("file:///", tempDir))
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	return scheme
}

func touchGitignore(t *testing.T, cwd schemeapi.Scheme, content string) {
	touchGitignoreAt(t, cwd, content, gitIgnoreFile)
}

func touchGitignoreAt(t *testing.T, cwd schemeapi.Scheme, content, at string) {
	require.NoError(t, cwd.MkdirAll(filepath.Dir(at), 0777))
	file, werr := cwd.OpenFile(at, os.O_CREATE|os.O_RDWR, 0666)
	require.Nil(t, werr)

	_, err := file.Write([]byte(content))
	require.NoError(t, err)

	err = file.Close()
	require.NoError(t, err)
}

func makeURI(t *testing.T, cwd schemeapi.Scheme, file string) workspaceapi.URI {
	uri, err := cwd.URI(file)
	require.NoError(t, err)
	return uri
}

// countingReader wraps a FileReader to record file-system access so
// tests can assert that LoadGitignore loads .gitignore files lazily
// rather than walking the whole tree up front. It is safe for
// concurrent use.
type countingReader struct {
	FileReader

	mu       sync.Mutex
	opens    int
	readDirs int
	opened   []string
}

func (c *countingReader) OpenFile(
	path string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	c.mu.Lock()
	c.opens++
	c.opened = append(c.opened, path)
	c.mu.Unlock()
	return c.FileReader.OpenFile(path, flag, perm)
}

func (c *countingReader) ReadDir(name string) ([]fs.DirEntry, error) {
	c.mu.Lock()
	c.readDirs++
	c.mu.Unlock()
	return c.FileReader.ReadDir(name)
}

func (c *countingReader) openedPaths() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.opened...)
}
