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

package idepkg

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
)

const ghPkgID = "github.com/foo/bar"

func TestValidatePkgPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in string
		ok bool
	}{
		// hierarchical github ids nest as real dirs and are accepted
		{"github.com/foo/bar", true},
		{"github.com/f-o_o./b.a-r_1", true},
		{"github.com/foo/bar/baz", true},
		{"go", true},
		{"a:b", true},
		// traversal-unsafe segments are rejected
		{"", false},
		{"github.com/../bar", false},
		{"..", false},
		{".", false},
		{"a/./b", false},
		{"a//b", false},
		{"/leading", false},
		{"trailing/", false},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.ok, validatePkgPath(tc.in) == nil, "in=%q", tc.in)
	}
}

func ghPkgTarball(t *testing.T) []byte {
	return makeReqPkgTarball(t, map[string]string{
		"config.yaml": "extensions:\n  demo:\n" +
			"    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/main.py\n",
		"main.py": "print('hi')\n",
	})
}

func TestInstallGitHubID(t *testing.T) {
	t.Parallel()
	t.Run("install nests dirs and links lib", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: ghPkgID, Latest: "abc123def456"})
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: ghPkgID, Version: "abc123def456"}})
		m, n, rm, temp := newTestManager(t, pkgs, versions)
		rm.SetTarball(ghPkgID, ghPkgTarball(t))

		err := m.InstallPackageVersion(
			context.Background(), ghPkgID, "abc123def456", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		version, ok := versionInUse(t, m, ghPkgID)
		require.True(t, ok)
		assert.Equal(t, release.Version("abc123def456"), version)

		libDir := filepath.Join(temp, "lib", "github.com", "foo", "bar")
		target, err := os.Readlink(libDir)
		require.NoError(t, err)
		assert.Equal(t,
			filepath.Join(temp, "pkg", "github.com", "foo", "bar", "abc123def456"),
			target)

		it, err := m.LibDir(context.Background(), ghPkgID)
		require.NoError(t, err)
		files, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		var names []string
		for _, f := range files {
			names = append(names, filepath.Base(f))
		}
		assert.ElementsMatch(t, []string{"config.yaml", "main.py"}, names)
	})
	t.Run("requirements of a github package install first", func(t *testing.T) {
		t.Parallel()
		const depID = "python"
		pkgs := idepkgtest.MakePackages(
			release.Package{Name: ghPkgID, Latest: "abc123def456"},
			release.Package{Name: depID, Latest: "1"},
		)
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: ghPkgID, Version: "abc123def456"}},
			[]release.Bundle{{Package: depID, Version: "1"}},
		)
		m, n, rm, _ := newTestManager(t, pkgs, versions)
		rm.SetTarball(ghPkgID, makeReqPkgTarball(t, map[string]string{
			"config.yaml": "requirements:\n  - " + depID + "\n" +
				"extensions:\n  demo:\n" +
				"    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/main.py\n",
			"main.py": "print('hi')\n",
		}))
		rm.SetTarball(depID, makeReqPkgTarball(t, map[string]string{
			"lib/python.txt": "python\n",
		}))

		err := m.InstallPackageVersion(
			context.Background(), ghPkgID, "abc123def456", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		_, ok := versionInUse(t, m, depID)
		assert.True(t, ok, "requirement must be installed")
		_, ok = versionInUse(t, m, ghPkgID)
		assert.True(t, ok, "github package must be installed")
	})
	t.Run("delete package cleans up nested dirs", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: ghPkgID, Latest: "abc123def456"})
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: ghPkgID, Version: "abc123def456"}})
		m, n, rm, temp := newTestManager(t, pkgs, versions)
		rm.SetTarball(ghPkgID, ghPkgTarball(t))

		err := m.InstallPackageVersion(
			context.Background(), ghPkgID, "abc123def456", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		require.NoError(t, m.DeletePackage(context.Background(), ghPkgID))

		_, ok := versionInUse(t, m, ghPkgID)
		assert.False(t, ok)
		_, err = os.Stat(filepath.Join(temp, "pkg", "github.com", "foo", "bar"))
		assert.True(t, os.IsNotExist(err), "pkg dir must be removed")
		_, err = os.Lstat(filepath.Join(temp, "lib", "github.com", "foo", "bar"))
		assert.True(t, os.IsNotExist(err), "lib symlink must be removed")

		assert.ErrorIs(t,
			m.DeletePackage(context.Background(), ghPkgID), ErrNotInstalled)
	})
}

func TestReconcileCleansNestedStaging(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages()
	versions := idepkgtest.MakeBundles()
	m, _, _, temp := newTestManager(t, pkgs, versions)

	// Simulate a crash mid-install: an incomplete storage entry with a
	// leftover staging dir nested too deep for the depth-1 sweep.
	ctx := context.Background()
	key := m.makeDownloadKey(ghPkgID, "abc123def456")
	require.NoError(t, m.storage.Create(ctx, key,
		newPkgVersionValue(ghPkgID, "abc123def456")))
	stagingDir := makeStagingDirname(temp, ghPkgID, "abc123def456")
	require.NoError(t, os.MkdirAll(stagingDir, 0o777))

	require.NoError(t, m.Reconcile(ctx))

	_, err := os.Stat(stagingDir)
	assert.True(t, os.IsNotExist(err), "staging dir must be removed")
	var val pkgVersionValue
	assert.Error(t, m.storage.Get(ctx, key, &val),
		"incomplete storage entry must be removed")
}
