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

package gitpkg

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	backendhttp "github.com/go-git/go-git/v6/backend/http"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
)

func TestIsGitPkgID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id string
		ok bool
	}{
		{"github.com/owner/repo", true},
		{"github.com/o-w_n.er/re.po-1", true},
		{"github.com/Owner/Repo", true},
		{"gitlab.com/owner/repo", true},
		{"bitbucket.org/owner/repo", true},
		{"git.example.com/owner/repo", true},
		{"gitlab.com/group/subgroup/repo", true},
		{"gitlab.com/a/b/c/d/repo", true},
		{"go", false},
		{"python", false},
		{"", false},
		{"github.com", false},
		{"github.com/owner", false},
		{"localhost/owner/repo", false},
		{"host/owner/repo", false},
		{"github.com//repo", false},
		{"github.com/owner/", false},
		{"github.com/./repo", false},
		{"github.com/../repo", false},
		{"github.com/owner/..", false},
		{"gitlab.com/group/../repo", false},
		{"github.com/owner/re$po", false},
		{"github.com/owner/re po", false},
		{"github.com/owner/re:po", false},
		{"https://github.com/owner/repo", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.ok, IsGitPkgID(tc.id), "id=%q", tc.id)
	}
}

func TestRemoteURLError(t *testing.T) {
	t.Parallel()
	const (
		noID     = "; name git packages by host and path, like github.com/owner/repo"
		viaSSH   = "; to enable SSH for the git CLI fallback, run: git config --global url."
		toGitHub = ".insteadOf https://github.com/"
	)
	cases := []struct {
		id   string
		want string
	}{
		{"github.com/owner/repo", ""},
		{"gitlab.com/group/subgroup/repo", ""},
		{"go", ""},
		{"a:b", ""},
		{"", ""},
		{"https://github.com/ernestrc/rune-test-private-repo",
			`"https://github.com/ernestrc/rune-test-private-repo" is a URL; ` +
				`use github.com/ernestrc/rune-test-private-repo`},
		{"https://github.com/owner/repo.git",
			`"https://github.com/owner/repo.git" is a URL; use github.com/owner/repo`},
		{"https://github.com/owner/repo/",
			`"https://github.com/owner/repo/" is a URL; use github.com/owner/repo`},
		{"https://github.com/owner/repo/tree/main/src",
			`"https://github.com/owner/repo/tree/main/src" is a URL; use github.com/owner/repo`},
		{"http://gitlab.com/group/subgroup/repo.git",
			`"http://gitlab.com/group/subgroup/repo.git" is a URL; ` +
				`use gitlab.com/group/subgroup/repo`},
		{"git://github.com/owner/repo",
			`"git://github.com/owner/repo" is a URL; use github.com/owner/repo`},
		{"https://user:secret@github.com/owner/repo",
			`"https://user:xxxxx@github.com/owner/repo" is a URL; use github.com/owner/repo`},
		{"https://git.example.com:8443/owner/repo",
			`"https://git.example.com:8443/owner/repo" is a URL` + noID},
		{"https://github.com", `"https://github.com" is a URL` + noID},
		{"git@github.com:ernestrc/rune-test-private-repo",
			`"git@github.com:ernestrc/rune-test-private-repo" is an SSH URL; ` +
				`use github.com/ernestrc/rune-test-private-repo` +
				viaSSH + `"git@github.com:"` + toGitHub},
		{"git@github.com:owner/repo.git",
			`"git@github.com:owner/repo.git" is an SSH URL; use github.com/owner/repo` +
				viaSSH + `"git@github.com:"` + toGitHub},
		{"github.com:owner/repo",
			`"github.com:owner/repo" is an SSH URL; use github.com/owner/repo` +
				viaSSH + `"github.com:"` + toGitHub},
		{"ssh://git@github.com/owner/repo.git",
			`"ssh://git@github.com/owner/repo.git" is an SSH URL; use github.com/owner/repo` +
				viaSSH + `"ssh://git@github.com/"` + toGitHub},
		{"ssh://git@git.example.com:2222/group/repo",
			`"ssh://git@git.example.com:2222/group/repo" is an SSH URL; ` +
				`use git.example.com/group/repo` + viaSSH +
				`"ssh://git@git.example.com:2222/".insteadOf https://git.example.com/`},
		{"git@localhost:repo", `"git@localhost:repo" is an SSH URL` + noID},
	}
	for _, tc := range cases {
		err := RemoteURLError(tc.id)
		if tc.want == "" {
			assert.NoError(t, err, "id=%q", tc.id)
			continue
		}
		assert.EqualError(t, err, tc.want, "id=%q", tc.id)
	}
}

// fixtureFile is a file committed into a fixture repository.
type fixtureFile struct {
	content string
	mode    os.FileMode
}

// validConfigYAML is a minimal installable git package config.
const validConfigYAML = `extensions:
  demo:
    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/main.py
requirements:
  - python
`

// initFixtureRepo materializes files into dir and commits them,
// returning the commit SHA.
func initFixtureRepo(t *testing.T, dir string, files map[string]fixtureFile) string {
	t.Helper()
	repo, err := git.PlainInit(dir, false)
	require.NoError(t, err)
	for name, f := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o777))
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		require.NoError(t, os.WriteFile(path, []byte(f.content), mode))
	}
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(".")
	require.NoError(t, err)
	sha, err := wt.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "fixture",
			Email: "fixture@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)
	return sha.String()
}

// commitFixtureRepo writes more files into an existing fixture repo and
// commits them, returning the new commit SHA.
func commitFixtureRepo(t *testing.T, dir string, files map[string]fixtureFile) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	for name, f := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o777))
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		require.NoError(t, os.WriteFile(path, []byte(f.content), mode))
	}
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(".")
	require.NoError(t, err)
	sha, err := wt.Commit("update", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "fixture",
			Email: "fixture@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)
	return sha.String()
}

// tagFixtureRepo creates a lightweight tag at the repo's current HEAD.
func tagFixtureRepo(t *testing.T, dir, tag string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	require.NoError(t, err)
	head, err := repo.Head()
	require.NoError(t, err)
	_, err = repo.CreateTag(tag, head.Hash(), nil)
	require.NoError(t, err)
	return head.Hash().String()
}

// newFixtureManager serves every repository under base over git
// smart-HTTP and returns a release.Manager whose git IDs resolve
// against that server. git is hidden from PATH so a go-git failure is
// not masked by the host's git CLI fallback; the CLI is covered by the
// e2e suite.
func newFixtureManager(t *testing.T, base string) release.Manager {
	t.Helper()
	t.Setenv("PATH", "")
	srv := httptest.NewServer(backendhttp.NewBackend(
		transport.NewFilesystemLoader(osfs.New(base), false)))
	t.Cleanup(srv.Close)
	return New(WithRemoteURL(func(pkgID string) string {
		return srv.URL + "/" + pkgID
	}))
}

func TestGetPackage(t *testing.T) {
	t.Run("resolves HEAD as latest version", func(t *testing.T) {
		base := t.TempDir()
		sha := initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: validConfigYAML}})
		m := newFixtureManager(t, base)

		pkg, err := m.GetPackage(context.Background(), "github.com/owner/repo")
		require.NoError(t, err)
		assert.Equal(t, "github.com/owner/repo", pkg.Name)
		assert.Equal(t, release.Version(sha[:shortSHALen]), pkg.Latest)
	})
	t.Run("missing repository errors", func(t *testing.T) {
		m := newFixtureManager(t, t.TempDir())
		_, err := m.GetPackage(context.Background(), "github.com/owner/missing")
		require.Error(t, err)
	})
}

func TestList(t *testing.T) {
	t.Run("latest only when no tags", func(t *testing.T) {
		base := t.TempDir()
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: validConfigYAML}})
		m := newFixtureManager(t, base)

		it, err := m.List(context.Background(), "github.com/owner/repo", nil)
		require.NoError(t, err)
		bundles, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		require.Len(t, bundles, 1)
		assert.Equal(t, release.Latest, bundles[0].Version)
	})
	t.Run("latest plus each tag", func(t *testing.T) {
		base := t.TempDir()
		repoDir := filepath.Join(base, "github.com", "owner", "repo")
		initFixtureRepo(t, repoDir,
			map[string]fixtureFile{"config.yaml": {content: validConfigYAML}})
		tagFixtureRepo(t, repoDir, "v1.0.0")
		tagFixtureRepo(t, repoDir, "v2.0.0")
		m := newFixtureManager(t, base)

		it, err := m.List(context.Background(), "github.com/owner/repo", nil)
		require.NoError(t, err)
		bundles, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		versions := make([]release.Version, len(bundles))
		for i, b := range bundles {
			versions[i] = b.Version
		}
		assert.Equal(t, release.Latest, versions[0], "latest must be first")
		assert.ElementsMatch(t,
			[]release.Version{release.Latest, "v1.0.0", "v2.0.0"}, versions)
	})
}

// bufferProgressWriter collects the tarball and progress samples.
type bufferProgressWriter struct {
	bytes.Buffer
	samples int
}

func (b *bufferProgressWriter) Progress(progress, total int64, units string) {
	b.samples++
}

func untarAll(t *testing.T, data []byte) map[string]*tar.Header {
	t.Helper()
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	tr := tar.NewReader(gzr)
	entries := make(map[string]*tar.Header)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		entries[hdr.Name] = hdr
		_, err = io.Copy(io.Discard, tr)
		require.NoError(t, err)
	}
	return entries
}

// entryContent returns the content of a single file from a gzipped tar
// stream, failing the test if the file is absent.
func entryContent(t *testing.T, data []byte, name string) string {
	t.Helper()
	gzr, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if hdr.Name == name {
			var buf bytes.Buffer
			_, err = io.Copy(&buf, tr)
			require.NoError(t, err)
			return buf.String()
		}
	}
	t.Fatalf("entry %q not found in tarball", name)
	return ""
}

func TestGet(t *testing.T) {
	t.Run("streams worktree tarball", func(t *testing.T) {
		base := t.TempDir()
		sha := initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{
				"config.yaml":    {content: validConfigYAML},
				"main.py":        {content: "print('hi')\n"},
				"scripts/run.sh": {content: "#!/bin/sh\n", mode: 0o755},
			})
		m := newFixtureManager(t, base)

		pw := &bufferProgressWriter{}
		bundle, err := m.Get(context.Background(),
			"github.com/owner/repo", release.Latest, pw)
		require.NoError(t, err)
		assert.Equal(t, release.Version(sha[:shortSHALen]), bundle.Version,
			"latest install stores the resolved HEAD short sha")
		assert.False(t, bundle.CreatedAt.IsZero())
		assert.Positive(t, pw.samples)

		entries := untarAll(t, pw.Bytes())
		require.Contains(t, entries, "config.yaml")
		require.Contains(t, entries, "main.py")
		require.Contains(t, entries, "scripts/run.sh")
		assert.NotZero(t, entries["scripts/run.sh"].FileInfo().Mode()&0o111,
			"exec bit must be preserved")
		for name := range entries {
			assert.NotContains(t, name, ".git/")
		}
	})
	t.Run("discard progress writer skips cloning", func(t *testing.T) {
		base := t.TempDir()
		sha := initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: validConfigYAML}})
		m := newFixtureManager(t, base)

		bundle, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, release.NopProgressWriter(io.Discard))
		require.NoError(t, err)
		assert.Equal(t, release.Version(sha[:shortSHALen]), bundle.Version)
	})
	t.Run("installs a tag's worktree", func(t *testing.T) {
		base := t.TempDir()
		repoDir := filepath.Join(base, "github.com", "owner", "repo")
		initFixtureRepo(t, repoDir, map[string]fixtureFile{
			"config.yaml": {content: validConfigYAML},
			"main.py":     {content: "print('v1')\n"},
		})
		tagFixtureRepo(t, repoDir, "v1.0.0")
		// HEAD moves past the tag with different content.
		commitFixtureRepo(t, repoDir, map[string]fixtureFile{
			"main.py": {content: "print('head')\n"},
		})
		m := newFixtureManager(t, base)

		pw := &bufferProgressWriter{}
		bundle, err := m.Get(context.Background(),
			"github.com/owner/repo", "v1.0.0", pw)
		require.NoError(t, err)
		assert.Equal(t, release.Version("v1.0.0"), bundle.Version,
			"a tag install stores the tag name")
		entries := untarAll(t, pw.Bytes())
		require.Contains(t, entries, "main.py")
		assert.Equal(t, "print('v1')\n", entryContent(t, pw.Bytes(), "main.py"),
			"the tag's tree must be installed, not HEAD")
	})
	t.Run("installs a commit's worktree", func(t *testing.T) {
		base := t.TempDir()
		repoDir := filepath.Join(base, "github.com", "owner", "repo")
		first := initFixtureRepo(t, repoDir, map[string]fixtureFile{
			"config.yaml": {content: validConfigYAML},
			"main.py":     {content: "print('first')\n"},
		})
		commitFixtureRepo(t, repoDir, map[string]fixtureFile{
			"main.py": {content: "print('head')\n"},
		})
		m := newFixtureManager(t, base)

		pw := &bufferProgressWriter{}
		bundle, err := m.Get(context.Background(),
			"github.com/owner/repo", release.Version(first[:shortSHALen]), pw)
		require.NoError(t, err)
		assert.Equal(t, release.Version(first[:shortSHALen]), bundle.Version,
			"a commit install stores the requested hash")
		assert.Equal(t, "print('first')\n", entryContent(t, pw.Bytes(), "main.py"),
			"the requested commit's tree must be installed, not HEAD")
	})
	t.Run("unknown version errors", func(t *testing.T) {
		base := t.TempDir()
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: validConfigYAML}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			"v9.9.9", &bufferProgressWriter{})
		require.ErrorContains(t, err, "unknown version")
	})
	t.Run("missing config.yaml errors", func(t *testing.T) {
		base := t.TempDir()
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"README.md": {content: "hi"}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.ErrorContains(t, err, "config.yaml")
	})
	t.Run("theme-only package installs", func(t *testing.T) {
		base := t.TempDir()
		cfg := "gui:\n  themes:\n    mytheme:\n      background: '#101010'\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{
				"config.yaml": {content: cfg},
				"README.md":   {content: "# My theme\n"},
			})
		m := newFixtureManager(t, base)
		pw := &bufferProgressWriter{}

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, pw)
		require.NoError(t, err)
		assert.Equal(t, cfg, entryContent(t, pw.Bytes(), "config.yaml"))
	})
	t.Run("alias-only package installs", func(t *testing.T) {
		base := t.TempDir()
		cfg := "command:\n  aliases:\n    hello: echo hello\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: cfg}})
		m := newFixtureManager(t, base)
		pw := &bufferProgressWriter{}

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, pw)
		require.NoError(t, err)
		assert.Equal(t, cfg, entryContent(t, pw.Bytes(), "config.yaml"))
	})
	t.Run("other config overlay installs", func(t *testing.T) {
		base := t.TempDir()
		cfg := "gui:\n  env:\n    MY_TOOL: enabled\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: cfg}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.NoError(t, err)
	})
	t.Run("empty config errors", func(t *testing.T) {
		base := t.TempDir()
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: "{}\n"}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.ErrorContains(t, err, "non-empty")
	})
	t.Run("non-source extension path errors", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  demo:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/bin/demo\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: cfg}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.ErrorContains(t, err, "source entrypoint")
	})
	t.Run("extension config without a path installs", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  rune-agent:\n    config:\n      status_bar:\n        enabled: true\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: cfg}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.NoError(t, err)
	})
	t.Run("empty extension path errors", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  demo:\n    path: ''\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: cfg}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.ErrorContains(t, err, "path must not be empty")
	})
	t.Run("go entrypoint must be main.go", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  demo:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/other.go\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: cfg}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.ErrorContains(t, err, "main.go")
	})
	t.Run("stdlib go package directory installs without go.sum", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  demo:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{
				"config.yaml": {content: cfg},
				"go.mod":      {content: "module demo\n\ngo 1.22\n"},
				"main.go":     {content: "package main\n\nfunc main() {}\n"},
			})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.NoError(t, err)
	})
	t.Run("go package directory with requires and go.sum installs", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  demo:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID\n"
		goMod := "module demo\n\ngo 1.22\n\nrequire example.com/dep v1.0.0\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{
				"config.yaml": {content: cfg},
				"go.mod":      {content: goMod},
				"go.sum":      {content: "example.com/dep v1.0.0 h1:abc=\n"},
				"main.go":     {content: "package main\n\nfunc main() {}\n"},
			})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.NoError(t, err)
	})
	t.Run("go package directory with requires but no go.sum errors", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  demo:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID\n"
		goMod := "module demo\n\ngo 1.22\n\nrequire example.com/dep v1.0.0\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{
				"config.yaml": {content: cfg},
				"go.mod":      {content: goMod},
				"main.go":     {content: "package main\n\nfunc main() {}\n"},
			})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.ErrorContains(t, err, "go.sum")
	})
	t.Run("go package directory without go.mod errors", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  demo:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{
				"config.yaml": {content: cfg},
				"main.go":     {content: "package main\n\nfunc main() {}\n"},
			})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.ErrorContains(t, err, "source entrypoint")
	})
	t.Run("invalid requirements errors", func(t *testing.T) {
		base := t.TempDir()
		cfg := "extensions:\n  demo:\n    path: main.py\nrequirements: python\n"
		initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
			map[string]fixtureFile{"config.yaml": {content: cfg}})
		m := newFixtureManager(t, base)

		_, err := m.Get(context.Background(), "github.com/owner/repo",
			release.Latest, &bufferProgressWriter{})
		require.ErrorContains(t, err, "requirements")
	})
}

func TestGoGitRunsBeforeGitCLI(t *testing.T) {
	const id = "github.com/owner/repo"
	base := t.TempDir()
	sha := initFixtureRepo(t, filepath.Join(base, "github.com", "owner", "repo"),
		map[string]fixtureFile{
			"config.yaml": {content: validConfigYAML},
			"main.py":     {content: "print('hi')\n"},
		})
	m := newFixtureManager(t, base)
	// A git on PATH that cannot be executed: reaching the CLI fails
	// before any process runs.
	name := "git"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, name), nil, 0o755))
	t.Setenv("PATH", bin)
	ctx := context.Background()

	pkg, err := m.GetPackage(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, release.Version(sha[:shortSHALen]), pkg.Latest)

	pw := &bufferProgressWriter{}
	bundle, err := m.Get(ctx, id, release.Latest, pw)
	require.NoError(t, err)
	assert.Equal(t, release.Version(sha[:shortSHALen]), bundle.Version)
	assert.Equal(t, "print('hi')\n", entryContent(t, pw.Bytes(), "main.py"))

	_, err = m.GetPackage(ctx, "github.com/owner/missing")
	require.Error(t, err)
	goGitErr, cliErr, ok := strings.Cut(err.Error(), "; git CLI fallback: ")
	require.True(t, ok, "a go-git failure must be retried with the CLI: %v", err)
	assert.Contains(t, goGitErr, transport.ErrRepositoryNotFound.Error())
	assert.Contains(t, cliErr, "git ls-remote")
}

func TestWithCLIFallback(t *testing.T) {
	goGitErr := errors.New("go-git failed")
	cliErr := errors.New("CLI failed")
	cases := []struct {
		name       string
		noGit      bool
		cancel     bool
		firstErr   error
		secondErr  error
		wantCalls  []string
		wantErrors []error
	}{
		{name: "go-git succeeds", wantCalls: []string{"go-git"}},
		{name: "CLI succeeds", firstErr: goGitErr, wantCalls: []string{"go-git", "CLI"}},
		{name: "both fail", firstErr: goGitErr, secondErr: cliErr,
			wantCalls: []string{"go-git", "CLI"}, wantErrors: []error{goGitErr, cliErr}},
		{name: "git unavailable", noGit: true, firstErr: goGitErr,
			wantCalls: []string{"go-git"}, wantErrors: []error{goGitErr}},
		{name: "cancelled go-git request", cancel: true,
			wantCalls: []string{"go-git"}, wantErrors: []error{context.Canceled}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			t.Setenv("PATH", bin)
			gitBin := filepath.Join(bin, "git")
			if runtime.GOOS == "windows" {
				gitBin += ".exe"
			}
			if !tc.noGit {
				// LookPath can find this file, but the callback never executes it.
				require.NoError(t, os.WriteFile(gitBin, nil, 0o755))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls []string
			value, err := withCLIFallback(ctx, "github.com/owner/repo",
				func() (string, error) {
					calls = append(calls, "go-git")
					if tc.cancel {
						cancel()
						return "go-git", ctx.Err()
					}
					return "go-git", tc.firstErr
				},
				func(path string) (string, error) {
					assert.Equal(t, gitBin, path)
					calls = append(calls, "CLI")
					return "CLI", tc.secondErr
				})
			assert.Equal(t, tc.wantCalls, calls)
			assert.Equal(t, tc.wantCalls[len(tc.wantCalls)-1], value)
			if len(tc.wantErrors) == 0 {
				require.NoError(t, err)
			}
			for _, want := range tc.wantErrors {
				assert.ErrorIs(t, err, want)
			}
		})
	}
}

func TestUnsupportedOperations(t *testing.T) {
	t.Parallel()
	m := New()
	ctx := context.Background()
	const id = "github.com/owner/repo"
	assert.ErrorIs(t, m.Create(ctx, release.Package{Name: id}), ErrNotSupported)
	assert.ErrorIs(t, m.UpdatePackageMetadata(ctx, id, nil), ErrNotSupported)
	assert.ErrorIs(t, m.DeletePackage(ctx, id), ErrNotSupported)
	assert.ErrorIs(t, m.Upload(ctx, release.Bundle{Package: id}, nil), ErrNotSupported)
	assert.ErrorIs(t, m.Delete(ctx, id, "abc"), ErrNotSupported)
}
