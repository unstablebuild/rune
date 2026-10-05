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
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v6/osfs"
	git "github.com/go-git/go-git/v6"
	backendhttp "github.com/go-git/go-git/v6/backend/http"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/pkgshell"
)

// gitFixtureCommit writes files into a fixture repo worktree and
// commits them, returning the commit SHA.
func gitFixtureCommit(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		repo, err = git.PlainInit(dir, false)
		require.NoError(t, err)
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o777))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(".")
	require.NoError(t, err)
	sha, err := wt.Commit("fixture", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "fixture",
			Email: "fixture@example.com",
			When:  time.Now(),
		},
	})
	require.NoError(t, err)
	return sha.String()
}

// serveGitFixtures serves every repo under base over git smart-HTTP and
// returns a mapper from git package ID to its fixture remote URL,
// suitable for a handler's gitRemoteURL field.
func serveGitFixtures(t *testing.T, base string) func(pkgID string) string {
	t.Helper()
	srv := httptest.NewServer(backendhttp.NewBackend(
		transport.NewFilesystemLoader(osfs.New(base), false)))
	t.Cleanup(srv.Close)
	return func(pkgID string) string { return srv.URL + "/" + pkgID }
}

func TestPkgManagerGitHubInstallFacade(t *testing.T) {
	const ghID = "github.com/owner/repo"
	base := t.TempDir()
	repoDir := filepath.Join(base, "github.com", "owner", "repo")
	sha := gitFixtureCommit(t, repoDir, map[string]string{
		"config.yaml": "requirements:\n  - six\n" +
			"extensions:\n  demo:\n" +
			"    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/main.py\n",
		"main.py": "print('hi')\n",
	})
	ghURL := serveGitFixtures(t, base)

	pkgs := idepkgtest.MakePackages(release.Package{Name: "six", Latest: "2"})
	bundles := idepkgtest.MakeBundles(
		[]release.Bundle{{Package: "six", Version: "2"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0, ghURL)
	defer m.Close()

	h := pkgshell.New(pkgshell.Config{Manager: m.pkgmanager.pkg})
	ctx := context.Background()

	// install resolves HEAD as the latest version, clones, and installs
	// the declared requirement from the official distribution first.
	_, err := h.HandleCommand(ctx, repl.Command{
		Name: pkgshell.CommandName,
		Args: []string{"install", ghID},
	}, repl.NopProgressWriter())
	require.NoError(t, err)

	version, ok := pkgVersionInUse(t, m.pkgmanager.pkg, ghID)
	require.True(t, ok)
	assert.Equal(t, release.Version(sha[:12]), version)
	_, ok = pkgVersionInUse(t, m.pkgmanager.pkg, "six")
	assert.True(t, ok, "requirement must be installed from the official distribution")

	it, err := m.pkgmanager.LibDir(ctx, ghID)
	require.NoError(t, err)
	var names []string
	for {
		f, ok := it.Next(ctx)
		if !ok {
			break
		}
		names = append(names, filepath.Base(f))
	}
	require.NoError(t, it.Err())
	require.NoError(t, it.Close())
	assert.ElementsMatch(t, []string{"config.yaml", "main.py"}, names)

	// a new commit surfaces as an available update
	sha2 := gitFixtureCommit(t, repoDir, map[string]string{
		"main.py": "print('v2')\n",
	})
	updates, err := idepkg.CheckForUpdates(ctx, m.pkgmanager.pkg)
	require.NoError(t, err)
	require.Len(t, updates, 1)
	assert.Equal(t, idepkg.Update{
		Package: ghID,
		Current: release.Version(sha[:12]),
		Latest:  release.Version(sha2[:12]),
	}, updates[0])

	// remove cleans up through the shell
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: pkgshell.CommandName,
		Args: []string{"remove", ghID},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	_, ok = pkgVersionInUse(t, m.pkgmanager.pkg, ghID)
	assert.False(t, ok)
}
