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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/document/docmarshal/docbson"
	"github.com/unstablebuild/blue/document/docmarshal/doctoml"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/blue/release/cdnrelease"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/localstorage/bluestore"
	"unstable.build/rune/internal/workspace/walkdir"
)

func TestLibDir(t *testing.T) {
	t.Parallel()
	t.Run("returns installed files", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		it, err := m.LibDir(context.Background(), "go")
		require.NoError(t, err)
		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		// test that paths are absolute
		actual := make([]string, 0)
		for _, path := range installed {
			require.True(t, filepath.IsAbs(path))
			actual = append(actual, filepath.Base(path))
		}
		expected := []string{
			"highlights.scm",
			"tags.scm",
			"indents.scm",
			"tree-sitter.so",
			"go",
			"gofmt",
			"goimports",
			"gopls",
		}
		assert.ElementsMatch(t, expected, actual)
	})
	t.Run("if install started, returned iterator blocks until package is done installing", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		it, err := m.LibDir(context.Background(), "go")
		require.NoError(t, err)
		actual, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.NotEmpty(t, actual)
	})
	t.Run("multiple cals to LibDir while installing return complete iterators", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		it1, err := m.LibDir(context.Background(), "go")
		require.NoError(t, err)

		it2, err := m.LibDir(context.Background(), "go")
		require.NoError(t, err)

		it3, err := m.LibDir(context.Background(), "go")
		require.NoError(t, err)

		for _, it := range []iterator.Iterator[string]{it1, it2, it3} {
			files, err := iterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			var actual []string
			for _, file := range files {
				actual = append(actual, filepath.Base(file))
			}
			expected := []string{
				"highlights.scm", "tags.scm", "indents.scm",
				"tree-sitter.so", "go",
				"gofmt", "goimports", "gopls",
			}
			assert.ElementsMatch(t, expected, actual)
		}
	})
	t.Run("returns error if package is not installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.LibDir(context.Background(), "go")
		require.Equal(t, ErrNotInstalled, err)
	})
	t.Run("survives manager restart with reconcile", func(t *testing.T) {
		t.Parallel()
		type storageFactory struct {
			name string
			make func() storageapi.Service
		}
		factories := []storageFactory{
			{"bson", func() storageapi.Service {
				return storagestub.NewInMemoryServiceWithMarshaler(docbson.Marshaler())
			}},
			{"toml", func() storageapi.Service {
				return storagestub.NewInMemoryServiceWithMarshaler(doctoml.Marshaler())
			}},
			{"toml_partitioned", func() storageapi.Service {
				return storageapi.WithPartition(
					storagestub.NewInMemoryServiceWithMarshaler(doctoml.Marshaler()), "idepkg")
			}},
		}
		for _, sf := range factories {
			t.Run(sf.name, func(t *testing.T) {
				t.Parallel()
				pkgs := idepkgtest.MakePackages()
				versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})

				temp, err := os.MkdirTemp("", "")
				require.NoError(t, err)
				t.Cleanup(func() { _ = os.RemoveAll(temp) })

				configPath := filepath.Join(temp, "config.yaml")
				storage := sf.make()
				fileScheme := newLocalScheme(temp)
				wm := &mockPrompter{
					floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
						h.Resize(70, 20)
						h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
						return &mockWindow{}, nil
					},
				}

				// First Manager: install a package.
				n1 := idepkgtest.NewNotifications(t)
				rm := idepkgtest.NewReleaseManager(pkgs, versions)
				m1 := NewManager(n1, rm, storage,
					idepkgtest.TrustStore(),
					fileScheme, temp, configPath, wm, syncTick, term.NopInterrupter())

				err = m1.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
				require.NoError(t, err)
				n1.RequireNoErrorNotification()

				// Sanity: LibDir works on the first manager.
				it, err := m1.LibDir(context.Background(), "go")
				require.NoError(t, err)
				files, err := iterator.ToSlice(context.Background(), it)
				require.NoError(t, err)
				require.NotEmpty(t, files)

				// Second Manager: same storage and dataDir, simulating a restart.
				n2 := idepkgtest.NewNotifications(t)
				m2 := NewManager(n2, rm, storage,
					idepkgtest.TrustStore(),
					fileScheme, temp, configPath, wm, syncTick, term.NopInterrupter())

				err = m2.Reconcile(context.Background())
				require.NoError(t, err)

				// LibDir must still work after restart + reconcile.
				it, err = m2.LibDir(context.Background(), "go")
				require.NoError(t, err)
				files, err = iterator.ToSlice(context.Background(), it)
				require.NoError(t, err)
				require.NotEmpty(t, files)
			})
		}
	})
}

func TestDescribePackage(t *testing.T) {
	t.Parallel()
	t.Run("no packages", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.DescribePackage(context.Background(), "myPkg")
		assert.EqualError(t, err, "not found")
	})
	t.Run("returns error if package id is empty", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.DescribePackage(context.Background(), "")
		require.Error(t, err)
	})
	t.Run("returns package", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		pkg, err := m.DescribePackage(context.Background(), "go")
		require.NoError(t, err)
		assert.Equal(t, release.Package{Name: "go"}, pkg)
	})
	t.Run("bubbles up release manager error", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles()
		m, _, r, _ := newTestManager(t, pkgs, versions)

		r.ExpectReturnErr(errors.New("boom"))
		_, err := m.DescribePackage(context.Background(), "go")
		assert.EqualError(t, err, "boom")
	})
}

func TestDescribeRelease(t *testing.T) {
	t.Parallel()
	t.Run("no releases", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.DescribeRelease(context.Background(), "myPkg", "notFoundRelease")
		assert.EqualError(t, err, "not found")
	})
	t.Run("returns error if package id is empty", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "", Version: "m"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.DescribeRelease(context.Background(), "", "m")
		require.Error(t, err)
	})
	t.Run("returns error if release id is empty", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: ""}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.DescribeRelease(context.Background(), "go", "")
		require.Error(t, err)
	})
	t.Run("returns release bundle", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "m"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		pkg, err := m.DescribeRelease(context.Background(), "go", "m")
		require.NoError(t, err)
		assert.Equal(t, "go", pkg.Package)
		assert.Equal(t, release.Version("m"), pkg.Version)
	})
	t.Run("bubbles up release manager error", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "m"}})
		m, _, r, _ := newTestManager(t, pkgs, versions)

		r.ExpectReturnErr(errors.New("boom"))
		_, err := m.DescribeRelease(context.Background(), "go", "m")
		assert.EqualError(t, err, "boom")
	})
}

func TestListPackages(t *testing.T) {
	t.Parallel()
	t.Run("no packages", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		it, err := m.ListPackages(context.Background(), nil)
		require.NoError(t, err)

		_, empty := iterator.IsEmpty(context.Background(), it)
		assert.True(t, empty)
	})
	t.Run("returns packages", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"}, release.Package{Name: "ox"})
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		it, err := m.ListPackages(context.Background(), nil)
		require.NoError(t, err)

		actual, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.ElementsMatch(t, []release.Package{{Name: "go"}, {Name: "ox"}}, actual)
	})
	t.Run("bubbles up release manager error", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles()
		m, _, r, _ := newTestManager(t, pkgs, versions)

		r.ExpectReturnErr(errors.New("boom"))
		_, err := m.ListPackages(context.Background(), nil)
		assert.EqualError(t, err, "boom")
	})
}

func TestListPackageVersions(t *testing.T) {
	t.Parallel()
	t.Run("no packages", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.ListPackageVersions(context.Background(), "go", nil)
		require.Error(t, err)
		assert.EqualError(t, err, "not found")
	})
	t.Run("returns error if package id is empty", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.ListPackageVersions(context.Background(), "", nil)
		require.Error(t, err)
	})
	t.Run("returns packages", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		it, err := m.ListPackageVersions(context.Background(), "go", nil)
		require.NoError(t, err)

		actual, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.ElementsMatch(t, []release.Bundle{{Package: "go", Version: "1"}}, actual)
	})
	t.Run("bubbles up release manager error", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles()
		m, _, r, _ := newTestManager(t, pkgs, versions)

		r.ExpectReturnErr(errors.New("boom"))
		_, err := m.ListPackageVersions(context.Background(), "go", nil)
		assert.EqualError(t, err, "boom")
	})
}

func TestLatestVersion(t *testing.T) {
	t.Parallel()
	t.Run("prefers the package Latest pointer", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "1.2.3"})
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		v, err := m.LatestVersion(context.Background(), "go")
		require.NoError(t, err)
		assert.Equal(t, release.Version("1.2.3"), v)
	})
	t.Run("falls back to newest bundle by creation time", func(t *testing.T) {
		t.Parallel()
		base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1", CreatedAt: base},
			{Package: "go", Version: "3", CreatedAt: base.Add(48 * time.Hour)},
			{Package: "go", Version: "2", CreatedAt: base.Add(24 * time.Hour)},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		v, err := m.LatestVersion(context.Background(), "go")
		require.NoError(t, err)
		assert.Equal(t, release.Version("3"), v)
	})
	t.Run("returns ErrNoReleases when the package has no bundles", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.LatestVersion(context.Background(), "go")
		assert.ErrorIs(t, err, ErrNoReleases)
	})
	t.Run("bubbles up describe error", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, err := m.LatestVersion(context.Background(), "go")
		assert.ErrorIs(t, err, ErrPackageNotFound)
	})
}

func TestTranslateReleaseErrors(t *testing.T) {
	t.Parallel()

	notFound := &cdnrelease.StatusError{
		URL:    "https://example/api/releases/darwin-arm64/packages/go",
		Status: http.StatusNotFound,
	}
	serverErr := &cdnrelease.StatusError{
		URL:    "https://example/api/releases/darwin-arm64/packages",
		Status: http.StatusBadGateway,
	}
	downloadErr := &cdnrelease.StatusError{
		// Empty URL marks the signed-URL data download branch.
		Status: http.StatusForbidden,
	}
	missingArtifactErr := &cdnrelease.StatusError{
		// Empty URL marks the signed-URL data download branch.
		Status: http.StatusNotFound,
	}

	t.Run("DescribePackage 404 -> ErrPackageNotFound", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := newTestManager(t,
			idepkgtest.MakePackages(release.Package{Name: "go"}),
			idepkgtest.MakeBundles())
		r.ExpectReturnErr(notFound)

		_, err := m.DescribePackage(context.Background(), "go")
		require.ErrorIs(t, err, ErrPackageNotFound)
		assert.Contains(t, err.Error(), `package "go" does not exist`)
	})

	t.Run("ListPackages 5xx -> ErrServerUnavailable", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := newTestManager(t,
			idepkgtest.MakePackages(), idepkgtest.MakeBundles())
		r.ExpectReturnErr(serverErr)

		_, err := m.ListPackages(context.Background(), nil)
		require.ErrorIs(t, err, ErrServerUnavailable)
		assert.Contains(t, err.Error(), "status 502")
	})

	t.Run("ListPackageVersions 404 -> ErrPackageNotFound", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := newTestManager(t,
			idepkgtest.MakePackages(release.Package{Name: "go"}),
			idepkgtest.MakeBundles())
		r.ExpectReturnErr(notFound)

		_, err := m.ListPackageVersions(context.Background(), "go", nil)
		require.ErrorIs(t, err, ErrPackageNotFound)
		assert.Contains(t, err.Error(), `package "go" does not exist`)
	})

	t.Run("DescribeRelease 404 -> ErrVersionNotFound", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := newTestManager(t,
			idepkgtest.MakePackages(release.Package{Name: "go"}),
			idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}}))
		r.ExpectReturnErr(notFound)

		_, err := m.DescribeRelease(context.Background(), "go", "1")
		require.ErrorIs(t, err, ErrVersionNotFound)
		assert.Contains(t, err.Error(), `version "1" of package "go" does not exist`)
	})

	t.Run("DescribeRelease signed-URL failure -> ErrServerUnavailable", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := newTestManager(t,
			idepkgtest.MakePackages(release.Package{Name: "go"}),
			idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}}))
		r.ExpectReturnErr(downloadErr)

		_, err := m.DescribeRelease(context.Background(), "go", "1")
		require.ErrorIs(t, err, ErrServerUnavailable)
		assert.Contains(t, err.Error(), `download of "go" version "1" failed`)
	})

	t.Run("DescribeRelease signed-URL 404 -> ErrArtifactMissing", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := newTestManager(t,
			idepkgtest.MakePackages(release.Package{Name: "go"}),
			idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}}))
		r.ExpectReturnErr(missingArtifactErr)

		_, err := m.DescribeRelease(context.Background(), "go", "1")
		require.ErrorIs(t, err, ErrArtifactMissing)
		assert.NotErrorIs(t, err, ErrServerUnavailable)
		assert.Contains(t, err.Error(), `"go" version "1"`)
		assert.NotContains(t, err.Error(), "status 404")
	})

	t.Run("non-StatusError passes through", func(t *testing.T) {
		t.Parallel()
		m, _, r, _ := newTestManager(t,
			idepkgtest.MakePackages(release.Package{Name: "go"}),
			idepkgtest.MakeBundles())
		r.ExpectReturnErr(errors.New("boom"))

		_, err := m.DescribePackage(context.Background(), "go")
		assert.EqualError(t, err, "boom")
	})
}

func TestInstallPackageVersion(t *testing.T) {
	t.Parallel()
	t.Run("returns error if pkg is empty", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)
		err := m.InstallPackageVersion(context.Background(), "", "1", repl.NopProgressWriter())
		assert.Error(t, err)
	})

	t.Run("returns error if version is empty", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)
		err := m.InstallPackageVersion(context.Background(), "go", "", repl.NopProgressWriter())
		assert.Error(t, err)
	})

	t.Run("installs a package with executables", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, _, datadir := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		assertDataDirExists(t, datadir, "go")
		assertExecutables(t, datadir, goTarExpectedExecutables...)
	})

	t.Run("install a package and version already installed fails", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		err = m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		assert.Error(t, err)
	})

	t.Run("installs a package without executables", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "testpkg", Version: "1"}})
		m, n, _, datadir := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "testpkg", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		assertDataDirExists(t, datadir, "testpkg")
		assertExecutables(t, datadir)
	})

	t.Run("install errors are retryable", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, r, datadir := newTestManager(t, pkgs, versions)

		r.ExpectReturnErr(errors.New("boom"))
		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.Error(t, err)

		r.ExpectReturnErr(nil)
		n.Reset()
		err = m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		assertDataDirExists(t, datadir, "go")
		assertExecutables(t, datadir, goTarExpectedExecutables...)
	})
	t.Run("notification progress is completed, even if writer doesn't", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, r, datadir := newTestManager(t, pkgs, versions)
		r.SetMissProgressComplete(true)

		pw := &recordingProgressWriter{}
		err := m.InstallPackageVersion(context.Background(), "go", "1", pw)
		require.NoError(t, err)
		n.RequireNoErrorNotification()
		assert.True(t, pw.completed(),
			"progress writer should be driven to completion even if the "+
				"release manager omits the final progress sample")

		assertDataDirExists(t, datadir, "go")
		assertExecutables(t, datadir, goTarExpectedExecutables...)
	})
}

func TestListInstalledPackageVersions(t *testing.T) {
	t.Parallel()
	t.Run("returns installed packages", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		it, err := m.ListInstalledPackages(context.Background())
		require.NoError(t, err)

		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		assert.ElementsMatch(t, []string{"go"}, installed)
	})
	t.Run("deduplicates when multiple versions are installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		it, err := m.ListInstalledPackages(context.Background())
		require.NoError(t, err)

		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		assert.Equal(t, []string{"go"}, installed)
	})
	t.Run("returns nothing if there are no packages installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		it, err := m.ListInstalledPackages(context.Background())
		require.NoError(t, err)

		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Empty(t, installed)
	})
}

func TestListInstalledPackages(t *testing.T) {
	t.Parallel()
	t.Run("returns installed packages", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		it, err := m.ListInstalledPackageVersions(context.Background(), "go")
		require.NoError(t, err)

		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		assert.ElementsMatch(t, []release.Version{"1"}, installed)
	})
	t.Run("returns nothing if there are no packages installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _ := newTestManager(t, pkgs, versions)

		it, err := m.ListInstalledPackageVersions(context.Background(), "go")
		require.NoError(t, err)

		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Empty(t, installed)
	})
}

func TestPackageVersionInUse(t *testing.T) {
	t.Parallel()
	t.Run("returns installed package version in use", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		actual, ok := versionInUse(t, m, "go")
		require.True(t, ok)
		assert.Equal(t, release.Version("1"), actual)
	})

	t.Run("returns latest installed package version", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		actual, ok := versionInUse(t, m, "go")
		require.True(t, ok)
		assert.Equal(t, release.Version("2"), actual)
	})

	t.Run("returns package version in use, after UsePackageVersion", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.UsePackageVersion(context.Background(), "go", "1")
		require.NoError(t, err)

		actual, ok := versionInUse(t, m, "go")
		require.True(t, ok)
		assert.Equal(t, release.Version("1"), actual)
	})

	t.Run("returns false if package is not installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		_, ok := versionInUse(t, m, "go")
		require.False(t, ok)
	})
}

func TestUsePackageVersion(t *testing.T) {
	t.Parallel()
	t.Run("returns error if package is not installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.UsePackageVersion(context.Background(), "go", "1")
		require.Error(t, err)
	})

	t.Run("returns error if version is not installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.UsePackageVersion(context.Background(), "go", "2")
		require.Error(t, err)
	})

	t.Run("relinks executables and lib", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, datadir := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		require.NoError(t, os.RemoveAll(filepath.Join(datadir, "bin", "go")))
		require.NoError(t, os.RemoveAll(filepath.Join(datadir, "lib", "go")))

		err = m.UsePackageVersion(context.Background(), "go", "1")
		require.NoError(t, err)

		assertDataDirExists(t, datadir, "go")
		assertExecutables(t, datadir, goTarExpectedExecutables...)
	})
	t.Run("returns error if version already in use", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.UsePackageVersion(context.Background(), "go", "2")
		require.Equal(t, err, ErrVersionInUse)
	})
}

func TestDeletePackage(t *testing.T) {
	t.Parallel()
	t.Run("deletes all versions of an installed package", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, datadir := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.DeletePackage(context.Background(), "go")
		require.NoError(t, err)

		assertDataDirNotExists(t, datadir, "go")
		assertExecutables(t, datadir /* none */)
	})

	t.Run("returns ErrNotInstalled if package is not installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.DeletePackage(context.Background(), "testpkg")
		require.Equal(t, ErrNotInstalled, err)
	})
}

func TestDeletePackageVersion(t *testing.T) {
	t.Parallel()
	t.Run("deletes a version of an installed package", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, datadir := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.DeletePackageVersion(context.Background(), "go", "1", false)
		require.NoError(t, err)

		assertDataDirExists(t, datadir, "go")
		assertExecutables(t, datadir, goTarExpectedExecutables...)
	})
	t.Run("returns ErrNotInstalled if package and version is not installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, datadir := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.DeletePackageVersion(context.Background(), "go", "3", false)
		require.Equal(t, ErrNotInstalled, err)

		assertDataDirExists(t, datadir, "go")
		assertExecutables(t, datadir, goTarExpectedExecutables...)
	})
	t.Run("returns ErrVersionInUse if package version is in use and force is false", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, datadir := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.DeletePackageVersion(context.Background(), "go", "2", false)
		require.Equal(t, err, ErrVersionInUse)

		assertDataDirExists(t, datadir, "go")
		assertExecutables(t, datadir, goTarExpectedExecutables...)
	})
	t.Run("removes lib+executables if package version is in use and force is true", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, datadir := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.DeletePackageVersion(context.Background(), "go", "2", true)
		require.NoError(t, err)

		assertExecutables(t, datadir /* none */)

		// use package version remaining version works after delete+force
		err = m.UsePackageVersion(context.Background(), "go", "1")
		require.NoError(t, err)
		assertDataDirExists(t, datadir, "go")
		assertExecutables(t, datadir, goTarExpectedExecutables...)
	})
}

func assertDataDirExists(t *testing.T, datadir string, pkgs ...string) {
	t.Helper()

	info, err := os.Stat(datadir)
	require.NoError(t, err)
	require.True(t, info.IsDir())

	info, err = os.Stat(filepath.Join(datadir, "bin"))
	require.NoError(t, err)
	require.True(t, info.IsDir())

	info, err = os.Stat(filepath.Join(datadir, "lib"))
	require.NoError(t, err)
	require.True(t, info.IsDir())

	info, err = os.Stat(filepath.Join(datadir, "pkg"))
	require.NoError(t, err)
	require.True(t, info.IsDir())

	files := listFiles(t, datadir)
	require.NoError(t, err)
	filesDebug := fmt.Sprintf("all files: %+v", files)

	for _, pkg := range pkgs {
		info, err = os.Stat(filepath.Join(datadir, "lib", pkg))
		require.NoError(t, err)
		assert.True(t, info.IsDir(), filesDebug)

		info, err = os.Stat(filepath.Join(datadir, "pkg", pkg))
		require.NoError(t, err)
		assert.True(t, info.IsDir(), filesDebug)
	}
}

func assertDataDirNotExists(t *testing.T, datadir string, pkgs ...string) {
	t.Helper()

	info, err := os.Stat(datadir)
	require.NoError(t, err)
	require.True(t, info.IsDir())

	for _, pkg := range pkgs {
		_, err = os.Stat(filepath.Join(datadir, "lib", pkg))
		require.Error(t, err)

		_, err = os.Stat(filepath.Join(datadir, "pkg", pkg))
		require.Error(t, err)
	}
}

var goTarExpectedExecutables = []string{
	"go", "gofmt", "goimports", "gopls",
}

func assertExecutables(t *testing.T, datadir string, expected ...string) {
	t.Helper()

	bindir := filepath.Join(datadir, "bin")
	info, err := os.Stat(bindir)
	require.NoError(t, err)
	require.True(t, info.IsDir())

	actual := listFiles(t, bindir)
	require.NoError(t, err)

	require.Len(t, actual, len(expected))
	assert.ElementsMatch(t, expected, actual)
}

func listFiles(t *testing.T, bindir string) []string {
	scheme := newLocalScheme(bindir)
	it, err := walkdir.ListFiles(context.Background(), scheme, ".")
	require.NoError(t, err)
	files, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	return files
}

var syncTick = func(fn func()) bool { fn(); return true }

func TestVerifyExtensionEntrypoint(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		// path selects the entrypoint handed to the manager, mirroring
		// what a package config would expand to.
		path func(t *testing.T, dataDir string) string
		// corrupt mutates the install after it succeeded.
		corrupt func(t *testing.T, dataDir string)
		wantOK  bool
	}{
		{
			name: "published layout package dir",
			path: func(_ *testing.T, dataDir string) string {
				return filepath.Join(
					makePackageVersionDirname(dataDir, dotPkgID, verifyPkgVersion),
					"bin", dotEntrypoint)
			},
			wantOK: true,
		},
		{
			name: "published layout bin copy",
			path: func(_ *testing.T, dataDir string) string {
				return filepath.Join(makeBinDirname(dataDir), dotEntrypoint)
			},
			wantOK: true,
		},
		{
			name: "published layout lib symlink",
			path: func(_ *testing.T, dataDir string) string {
				return filepath.Join(makeLibDirname(dataDir), dotPkgID, "bin", dotEntrypoint)
			},
			wantOK: true,
		},
		{
			name: "unprefixed manifest bin copy",
			path: func(_ *testing.T, dataDir string) string {
				return filepath.Join(makeBinDirname(dataDir), flatEntrypoint)
			},
			wantOK: true,
		},
		{
			name: "executable absent from every manifest",
			path: func(t *testing.T, dataDir string) string {
				path := filepath.Join(makeBinDirname(dataDir), "extension_stranger")
				require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755))
				return path
			},
		},
		{
			name: "executable outside the datadir",
			path: func(t *testing.T, _ string) string {
				path := filepath.Join(t.TempDir(), dotEntrypoint)
				require.NoError(t, os.WriteFile(path, []byte(entrypointScript), 0o755))
				return path
			},
		},
		{
			name: "tampered bin copy",
			path: func(_ *testing.T, dataDir string) string {
				return filepath.Join(makeBinDirname(dataDir), dotEntrypoint)
			},
			corrupt: func(t *testing.T, dataDir string) {
				require.NoError(t, os.WriteFile(
					filepath.Join(makeBinDirname(dataDir), dotEntrypoint),
					[]byte("#!/bin/sh\nexit 1\n"), 0o755))
			},
		},
		{
			name: "tampered packaged original",
			path: func(_ *testing.T, dataDir string) string {
				return filepath.Join(makeBinDirname(dataDir), dotEntrypoint)
			},
			corrupt: func(t *testing.T, dataDir string) {
				require.NoError(t, os.WriteFile(filepath.Join(
					makePackageVersionDirname(dataDir, dotPkgID, verifyPkgVersion),
					"bin", dotEntrypoint), []byte("#!/bin/sh\nexit 1\n"), 0o755))
			},
		},
		{
			name: "tampered package config",
			path: func(_ *testing.T, dataDir string) string {
				return filepath.Join(makeBinDirname(dataDir), dotEntrypoint)
			},
			corrupt: func(t *testing.T, dataDir string) {
				require.NoError(t, os.WriteFile(filepath.Join(
					makePackageVersionDirname(dataDir, dotPkgID, verifyPkgVersion),
					"config.yaml"), []byte("extensions: {}\n"), 0o644))
			},
		},
		{
			name: "manifest no longer matches provenance",
			path: func(_ *testing.T, dataDir string) string {
				return filepath.Join(makeBinDirname(dataDir), dotEntrypoint)
			},
			corrupt: func(t *testing.T, dataDir string) {
				path := makeManifestFilename(dataDir, dotPkgID, verifyPkgVersion)
				manifest, err := readManifest(path)
				require.NoError(t, err)
				manifest = append(manifest, pkgtrust.Entry{Path: "./bin/extra"})
				require.NoError(t, writeManifest(path, manifest))
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			manager, dataDir := installVerifyPackages(t)
			path := tc.path(t, dataDir)
			if tc.corrupt != nil {
				tc.corrupt(t, dataDir)
			}
			fingerprint, ok := manager.VerifyExtensionEntrypoint(path)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.NotEmpty(t, fingerprint)
				return
			}
			assert.Empty(t, fingerprint)
		})
	}
}

type recordingProgressWriter struct {
	mu       sync.Mutex
	complete bool
}

func (w *recordingProgressWriter) Progress(progress, total int64, _ string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if total > 0 && progress >= total {
		w.complete = true
	}
}

func (w *recordingProgressWriter) completed() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.complete
}

func newTestManager(
	t *testing.T,
	packages map[string]release.Package,
	versions map[string][]release.Bundle,
) (*Manager, *idepkgtest.Notifications, *idepkgtest.ReleaseManager, string) {
	return newTestManagerWithTrust(t, packages, versions, idepkgtest.TrustStore())
}

func newTestManagerWithTrust(
	t *testing.T,
	packages map[string]release.Package,
	versions map[string][]release.Bundle,
	trust *pkgtrust.Store,
) (*Manager, *idepkgtest.Notifications, *idepkgtest.ReleaseManager, string) {
	temp, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(temp)
	})
	configPath := filepath.Join(temp, "config.yaml")
	n := idepkgtest.NewNotifications(t)
	m := idepkgtest.NewReleaseManager(packages, versions)
	fileScheme := newLocalScheme(temp)
	wm := &mockPrompter{
		floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			// Auto-accept: send Enter to select "Allow"
			h.Resize(70, 20)
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			return &mockWindow{}, nil
		},
	}
	manager := NewManager(n, m, storagestub.NewInMemoryService(),
		trust,
		fileScheme, temp, configPath, wm, syncTick, term.NopInterrupter())
	return manager, n, m, temp
}

func versionInUse(t testing.TB, m PackageManager, pkgID string) (release.Version, bool) {
	t.Helper()
	version, ok, err := m.PackageVersionInUse(context.Background(), pkgID)
	require.NoError(t, err)
	return version, ok
}

// mockPrompter opens each prompt as a handler.Prompt with a markdown
// message, as the host does, and floats it with floatingFn, which
// answers it as the user would.
type mockPrompter struct {
	floatingFn func(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error)

	mu      sync.Mutex
	prompts []browserapi.PromptConfig
}

func (m *mockPrompter) Prompt(
	cfg browserapi.PromptConfig, h handler.PromptHandler,
) (browserapi.Window, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.prompts = append(m.prompts, cfg)
	m.mu.Unlock()
	if m.floatingFn == nil {
		return &mockWindow{}, nil
	}
	return m.floatingFn(handler.NewPrompt(handler.PromptConfig{
		PromptConfig: component.PromptConfig{
			Message: cfg.Message,
			Options: cfg.Options,
			NewMessage: func(msg string) component.Floating {
				mcfg := markdown.DefaultConfig()
				mcfg.HeaderPrefix = false
				mkd, err := markdown.NewWithConfig(msg, mcfg)
				if err != nil {
					panic(err)
				}
				return component.NewAspectRatioFloatingResponsive(mkd, component.DefaultAspectRatio)
			},
		},
		OptionBindings: cfg.Bindings,
		PromptHandler:  h,
	}), browserapi.FloatingConfig{Alignment: component.AlignmentCentered})
}

type mockWindow struct{}

func (m *mockWindow) WindowID() uint64 { return 0 }

// localScheme implements schemeapi.Scheme using os package functions for testing.
type localScheme struct {
	root string
}

func newLocalScheme(root string) *localScheme {
	return &localScheme{root: root}
}

func (s *localScheme) resolve(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(s.root, path)
}

func (s *localScheme) URI(path string) (workspaceapi.URI, error) {
	return workspaceapi.ParseURI("file://" + s.resolve(path))
}

func (s *localScheme) Root() string { return s.root }

func (s *localScheme) NewFile(_ uintptr, _ string) workspaceapi.File {
	panic("not implemented")
}

func (s *localScheme) Chroot(path string) (schemeapi.Scheme, error) {
	return newLocalScheme(s.resolve(path)), nil
}

func (s *localScheme) Watch(_ string, _ chan<- schemeapi.EventInfo, _ ...schemeapi.Event) (int, error) {
	panic("not implemented")
}

func (s *localScheme) StopWatch(_ int) error {
	panic("not implemented")
}

// schemeapi.FileSystem

func (s *localScheme) Create(filename string) (workspaceapi.File, error) {
	return os.Create(s.resolve(filename))
}

func (s *localScheme) Open(filename string) (workspaceapi.File, error) {
	return os.Open(s.resolve(filename))
}

func (s *localScheme) OpenFile(filename string, flag int, perm fs.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(s.resolve(filename), flag, perm)
}

func (s *localScheme) Stat(filename string) (fs.FileInfo, error) {
	return os.Stat(s.resolve(filename))
}

func (s *localScheme) Rename(oldpath, newpath string) error {
	return os.Rename(s.resolve(oldpath), s.resolve(newpath))
}

func (s *localScheme) Remove(filename string) error {
	return os.Remove(s.resolve(filename))
}

func (s *localScheme) Join(elem ...string) string {
	return filepath.Join(elem...)
}

func (s *localScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	return os.CreateTemp(s.resolve(dir), prefix)
}

func (s *localScheme) Lstat(filename string) (fs.FileInfo, error) {
	return os.Lstat(s.resolve(filename))
}

func (s *localScheme) Symlink(oldname, newname string) error {
	return os.Symlink(oldname, s.resolve(newname))
}

func (s *localScheme) Readlink(link string) (string, error) {
	return os.Readlink(s.resolve(link))
}

func (s *localScheme) ReadDir(path string) ([]fs.DirEntry, error) {
	return os.ReadDir(s.resolve(path))
}

func (s *localScheme) MkdirAll(filename string, perm fs.FileMode) error {
	return os.MkdirAll(s.resolve(filename), perm)
}

// schemeapi.Executor

func (s *localScheme) StartCommand(_ context.Context, _ workspaceapi.Cmd) (workspaceapi.Pid, error) {
	panic("not implemented")
}

func (s *localScheme) Signal(_ workspaceapi.Pid, _ syscall.Signal) error {
	panic("not implemented")
}

func (*localScheme) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}

func (s *localScheme) Close() error { return nil }

// schemeapi.Terminal

func (s *localScheme) NewPty(_ context.Context) (workspaceapi.Pty, error) {
	panic("not implemented")
}

func (s *localScheme) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error {
	panic("not implemented")
}

func (s *localScheme) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	panic("not implemented")
}

func TestPromptConfigChangeAllow(t *testing.T) {
	t.Parallel()
	var selected = -1
	prompt := handler.NewPrompt(handler.PromptConfig{
		PromptConfig: component.PromptConfig{
			Message: "Allow changes?",
			Options: []string{"Allow", "Deny"},
		},
		PromptHandler: handler.FuncPromptHandler(
			func(idx int, _ string) { selected = idx },
			func() error { return nil },
		),
	})
	prompt.Resize(40, 15)
	exit, handled := prompt.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit)
	assert.True(t, handled)
	assert.Equal(t, 0, selected)
}

func TestPromptConfigChangeDeny(t *testing.T) {
	t.Parallel()
	var selected = -1
	prompt := handler.NewPrompt(handler.PromptConfig{
		PromptConfig: component.PromptConfig{
			Message: "Allow changes?",
			Options: []string{"Allow", "Deny"},
		},
		PromptHandler: handler.FuncPromptHandler(
			func(idx int, _ string) { selected = idx },
			func() error { return nil },
		),
	})
	prompt.Resize(40, 15)
	// Move right to "Deny" then press Enter
	prompt.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
	exit, handled := prompt.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, exit)
	assert.True(t, handled)
	assert.Equal(t, 1, selected)
}

func TestPromptConfigChangeEscape(t *testing.T) {
	t.Parallel()
	var selected = -1
	prompt := handler.NewPrompt(handler.PromptConfig{
		PromptConfig: component.PromptConfig{
			Message: "Allow changes?",
			Options: []string{"Allow", "Deny"},
		},
		PromptHandler: handler.FuncPromptHandler(
			func(idx int, _ string) { selected = idx },
			func() error { return nil },
		),
	})
	prompt.Resize(40, 15)
	exit, handled := prompt.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit)
	assert.True(t, handled)
	// OnSelect should not have been called
	assert.Equal(t, -1, selected)
}

func notificationMessages(n *idepkgtest.Notifications) []string {
	var msgs []string
	for _, noti := range n.Active() {
		msgs = append(msgs, noti.Msg)
	}
	sort.Strings(msgs)
	return msgs
}

// --- Test helpers for crash-safe download tests ---

func createStaleEntry(
	t *testing.T, s document.Service, pkgID string, version release.Version,
) {
	t.Helper()
	key := idepkgTestStorageKey(pkgID, version)
	err := s.Create(context.Background(), key, newPkgVersionValue(pkgID, version))
	require.NoError(t, err)
}

func createCompleteEntry(
	t *testing.T, s document.Service, pkgID string, version release.Version,
) {
	t.Helper()
	key := idepkgTestStorageKey(pkgID, version)
	val := newPkgVersionValue(pkgID, version)
	val.Complete = true
	err := s.Create(context.Background(), key, val)
	require.NoError(t, err)
}

func assertStorageEntryComplete(
	t *testing.T, s document.Service, pkgID string, version release.Version,
) {
	t.Helper()
	key := idepkgTestStorageKey(pkgID, version)
	var val pkgVersionValue
	err := s.Get(context.Background(), key, &val)
	require.NoError(t, err, "storage entry should exist")
	assert.True(t, val.Complete, "storage entry should be complete")
}

func assertStorageEntryNotExists(
	t *testing.T, s document.Service, pkgID string, version release.Version,
) {
	t.Helper()
	key := idepkgTestStorageKey(pkgID, version)
	var val pkgVersionValue
	err := s.Get(context.Background(), key, &val)
	assert.True(t, errors.Is(err, document.ErrNotFound),
		"storage entry should not exist, got: %v", err)
}

func idepkgTestStorageKey(pkgID string, version release.Version) string {
	return fmt.Sprintf("%s:%s", pkgID, version)
}

// newTestManagerWithStorage is like newTestManager but returns the storage service too.
func newTestManagerWithStorage(
	t *testing.T,
	packages map[string]release.Package,
	versions map[string][]release.Bundle,
) (*Manager, *idepkgtest.Notifications, *idepkgtest.ReleaseManager, string, document.Service) {
	temp, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(temp)
	})
	configPath := filepath.Join(temp, "config.yaml")
	n := idepkgtest.NewNotifications(t)
	m := idepkgtest.NewReleaseManager(packages, versions)
	fileScheme := newLocalScheme(temp)
	wm := &mockPrompter{
		floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			h.Resize(70, 20)
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			return &mockWindow{}, nil
		},
	}
	storage := document.NewInMemoryService()
	manager := NewManager(n, m, bluestore.AdaptTo(storage),
		idepkgtest.TrustStore(),
		fileScheme, temp, configPath, wm, syncTick, term.NopInterrupter())
	return manager, n, m, temp, storage
}

// --- Reconcile tests ---

func TestReconcile(t *testing.T) {
	t.Parallel()

	t.Run("crash_after_storage_create_before_download", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		assertStorageEntryNotExists(t, storage, "go", "1")
		_, serr := os.Stat(makePackageVersionDirname(datadir, "go", "1"))
		assert.True(t, os.IsNotExist(serr))
	})

	t.Run("crash_during_extraction", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")
		// Create partial pkg dir
		pkgDir := makePackageVersionDirname(datadir, "go", "1")
		require.NoError(t, os.MkdirAll(pkgDir, 0777))
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "partial.txt"), []byte("x"), 0644))

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		assertStorageEntryNotExists(t, storage, "go", "1")
		_, serr := os.Stat(pkgDir)
		assert.True(t, os.IsNotExist(serr))
	})

	t.Run("crash_after_extraction_before_storage_update", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")
		pkgDir := makePackageVersionDirname(datadir, "go", "1")
		require.NoError(t, os.MkdirAll(pkgDir, 0777))

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		assertStorageEntryNotExists(t, storage, "go", "1")
		_, serr := os.Stat(pkgDir)
		assert.True(t, os.IsNotExist(serr))
	})

	t.Run("crash_during_link_lib_copy_bin", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")
		pkgDir := makePackageVersionDirname(datadir, "go", "1")
		require.NoError(t, os.MkdirAll(pkgDir, 0777))
		libLink := makePackageLibDirname(datadir, "go")
		require.NoError(t, os.Symlink(pkgDir, libLink))

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		assertStorageEntryNotExists(t, storage, "go", "1")
		_, serr := os.Stat(pkgDir)
		assert.True(t, os.IsNotExist(serr))
		_, serr = os.Lstat(libLink)
		assert.True(t, os.IsNotExist(serr))
	})

	t.Run("staging_dir_leftover", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")
		stagingDir := makeStagingDirname(datadir, "go", "1")
		require.NoError(t, os.MkdirAll(stagingDir, 0777))

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		_, serr := os.Stat(stagingDir)
		assert.True(t, os.IsNotExist(serr))
		assertStorageEntryNotExists(t, storage, "go", "1")
	})

	t.Run("stale_tmp_symlink", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, _ := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		tmpLink := filepath.Join(datadir, "lib", "go.tmp")
		require.NoError(t, os.Symlink("/nonexistent", tmpLink))

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		_, serr := os.Lstat(tmpLink)
		assert.True(t, os.IsNotExist(serr))
	})

	t.Run("multiple_packages_one_stale", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, _, _, storage := newTestManagerWithStorage(t, pkgs, versions)

		// Install "go:1" fully
		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		// Create stale "testpkg:1"
		createStaleEntry(t, storage, "testpkg", "1")

		err = m.Reconcile(context.Background())
		require.NoError(t, err)

		assertStorageEntryComplete(t, storage, "go", "1")
		assertStorageEntryNotExists(t, storage, "testpkg", "1")
	})

	t.Run("no_incomplete_installs", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _, storage := newTestManagerWithStorage(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.Reconcile(context.Background())
		require.NoError(t, err)

		assertStorageEntryComplete(t, storage, "go", "1")
	})

	t.Run("empty_storage", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, _, _ := newTestManagerWithStorage(t, pkgs, versions)

		err := m.Reconcile(context.Background())
		require.NoError(t, err)
	})

	t.Run("all_entries_stale", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")
		createStaleEntry(t, storage, "testpkg", "1")

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		assertStorageEntryNotExists(t, storage, "go", "1")
		assertStorageEntryNotExists(t, storage, "testpkg", "1")
	})

	t.Run("complete_entry_missing_dir", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createCompleteEntry(t, storage, "go", "1")
		// Don't create the pkg dir — simulates dir deletion

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		assertStorageEntryNotExists(t, storage, "go", "1")
	})

	t.Run("skips_installed_version_trees", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		// An installed toolchain tree is thousands of entries; the sweep
		// must not descend into it. Staging-looking names inside it belong
		// to the package and must survive as proof the tree was skipped.
		createCompleteEntry(t, storage, "go", "1")
		installed := makePackageVersionDirname(datadir, "go", "1")
		insideStaging := filepath.Join(installed, "src", ".staging-vendored")
		require.NoError(t, os.MkdirAll(insideStaging, 0777))
		insideManifest := filepath.Join(installed, "src", ".manifest-vendored.json")
		require.NoError(t, os.WriteFile(insideManifest, []byte("{}"), 0644))

		// Package-level leftovers from an aborted install of another version.
		strayStaging := makeStagingDirname(datadir, "go", "2")
		require.NoError(t, os.MkdirAll(strayStaging, 0777))
		orphanManifest := makeManifestFilename(datadir, "go", "2")
		require.NoError(t, os.WriteFile(orphanManifest, []byte("{}"), 0644))

		require.NoError(t, m.Reconcile(context.Background()))

		_, err := os.Stat(insideStaging)
		assert.NoError(t, err, "walk must not descend into installed version trees")
		_, err = os.Stat(insideManifest)
		assert.NoError(t, err, "walk must not descend into installed version trees")
		_, err = os.Stat(strayStaging)
		assert.True(t, os.IsNotExist(err), "stray package-level staging dir must be removed")
		_, err = os.Stat(orphanManifest)
		assert.True(t, os.IsNotExist(err), "orphan package-level manifest must be removed")
		assertStorageEntryComplete(t, storage, "go", "1")
	})

	t.Run("then_install_succeeds", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		assertStorageEntryComplete(t, storage, "go", "1")
		assertDataDirExists(t, datadir, "go")
	})

	t.Run("then_delete_returns_not_installed", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		err = m.DeletePackageVersion(context.Background(), "go", "1", false)
		require.Equal(t, ErrNotInstalled, err)
	})

	t.Run("then_list_excludes_recovered", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles()
		m, _, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "testpkg", "1")

		err := m.Reconcile(context.Background())
		require.NoError(t, err)

		it, err := m.ListInstalledPackages(context.Background())
		require.NoError(t, err)
		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.Empty(t, installed)
	})
}

// --- Install stale entry handling tests ---

func TestInstallStaleEntry(t *testing.T) {
	t.Parallel()

	t.Run("detects_and_cleans_stale_entry", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)

		createStaleEntry(t, storage, "go", "1")

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		assertStorageEntryComplete(t, storage, "go", "1")
		assertDataDirExists(t, datadir, "go")
	})

	t.Run("stale_entry_with_partial_files", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")
		pkgDir := makePackageVersionDirname(datadir, "go", "1")
		require.NoError(t, os.MkdirAll(pkgDir, 0777))
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "partial.txt"), []byte("x"), 0644))

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		assertStorageEntryComplete(t, storage, "go", "1")
		assertDataDirExists(t, datadir, "go")
	})

	t.Run("completed_entry_rejects_reinstall", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _, _ := newTestManagerWithStorage(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "already been installed")
		assert.ErrorIs(t, err, ErrAlreadyInstalled)
	})

	t.Run("stale_entry_with_staging_dir", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, n, _, datadir, storage := newTestManagerWithStorage(t, pkgs, versions)
		require.NoError(t, makePkgDirs(datadir))

		createStaleEntry(t, storage, "go", "1")
		stagingDir := makeStagingDirname(datadir, "go", "1")
		require.NoError(t, os.MkdirAll(stagingDir, 0777))

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		n.RequireNoErrorNotification()

		assertStorageEntryComplete(t, storage, "go", "1")
		_, serr := os.Stat(stagingDir)
		assert.True(t, os.IsNotExist(serr))
	})
}

// --- Listing methods filter incomplete tests ---

func TestListInstalledPackagesExcludesStale(t *testing.T) {
	t.Parallel()

	t.Run("excludes_stale", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _, storage := newTestManagerWithStorage(t, pkgs, versions)

		// Install "go:1" fully
		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		// Create stale "testpkg:1"
		createStaleEntry(t, storage, "testpkg", "1")

		it, err := m.ListInstalledPackages(context.Background())
		require.NoError(t, err)
		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		assert.Equal(t, []string{"go"}, installed)
	})
}

func TestListInstalledPackageVersionsExcludesStale(t *testing.T) {
	t.Parallel()

	t.Run("excludes_stale", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{
				{Package: "go", Version: "1"},
				{Package: "go", Version: "2"},
			},
		)
		m, _, _, _, storage := newTestManagerWithStorage(t, pkgs, versions)

		// Install "go:1" fully
		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		// Create stale "go:2"
		createStaleEntry(t, storage, "go", "2")

		it, err := m.ListInstalledPackageVersions(context.Background(), "go")
		require.NoError(t, err)
		installed, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)

		assert.Equal(t, []release.Version{"1"}, installed)
	})
}

func TestUsePackageVersionRejectsIncomplete(t *testing.T) {
	t.Parallel()

	t.Run("rejects_incomplete", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{
				{Package: "go", Version: "1"},
				{Package: "go", Version: "2"},
			},
		)
		m, _, _, _, storage := newTestManagerWithStorage(t, pkgs, versions)

		// Install "go:1" fully
		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		// Create stale "go:2"
		createStaleEntry(t, storage, "go", "2")

		err = m.UsePackageVersion(context.Background(), "go", "2")
		require.Equal(t, ErrNotInstalled, err)
	})
}

// --- Atomic operation tests ---

func TestInstallAtomicOperations(t *testing.T) {
	t.Parallel()

	t.Run("no_staging_dir_after_success", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, datadir, _ := newTestManagerWithStorage(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		stagingDir := makeStagingDirname(datadir, "go", "1")
		_, serr := os.Stat(stagingDir)
		assert.True(t, os.IsNotExist(serr), "staging dir should not exist after success")
	})

	t.Run("storage_entry_is_complete_after_success", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
		m, _, _, _, storage := newTestManagerWithStorage(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		assertStorageEntryComplete(t, storage, "go", "1")
	})
}

func TestInstallPackageVersionNonUTF8PAXXattr(t *testing.T) {
	t.Parallel()
	pkgID := "paxpkg"
	pkgs := idepkgtest.MakePackages()
	versions := idepkgtest.MakeBundles([]release.Bundle{
		{Package: pkgID, Version: "1"},
	})

	tarball := makePAXXattrTarball(t)

	m, n, r, datadir, storage := newTestManagerWithLocalStorage(t, pkgs, versions)
	r.SetTarball(pkgID, tarball)

	err := m.InstallPackageVersion(context.Background(), pkgID, "1", repl.NopProgressWriter())
	require.NoError(t, err)
	n.RequireNoErrorNotification()

	// The binary must end up in the package's bin directory.
	binPath := filepath.Join(datadir, "bin", "tool")
	info, err := os.Stat(binPath)
	require.NoError(t, err)
	assert.True(t, info.Mode()&0111 != 0, "installed file must be executable")

	// Round-trip the persisted record through the TOML store. Before the
	// fix, the file written during install contained raw 0xad bytes inside
	// a TOML string and the next read failed with "invalid UTF-8 byte:
	// 0xad". After the fix, the stored value carries only Name/Mode and
	// reads succeed.
	var got pkgVersionValue
	key := m.makeDownloadKey(pkgID, "1")
	require.NoError(t, storage.Get(context.Background(), key, &got))
	assert.Equal(t, pkgID, got.Package)
	require.Len(t, got.Executables, 1)
	assert.Equal(t, "bin/tool", got.Executables[0].Name)
}

// newTestManagerWithLocalStorage is like newTestManager but uses the
// production-style TOML-backed local storage so that any non-UTF-8 bytes in
// persisted values surface as marshaling errors.
func newTestManagerWithLocalStorage(
	t *testing.T,
	packages map[string]release.Package,
	versions map[string][]release.Bundle,
) (*Manager, *idepkgtest.Notifications, *idepkgtest.ReleaseManager, string, storageapi.Service) {
	temp, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(temp)
	})
	configPath := filepath.Join(temp, "config.yaml")
	n := idepkgtest.NewNotifications(t)
	r := idepkgtest.NewReleaseManager(packages, versions)
	fileScheme := newLocalScheme(temp)
	wm := &mockPrompter{
		floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
			h.Resize(70, 20)
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
			return &mockWindow{}, nil
		},
	}
	storage := localstorage.New(context.Background(), temp, docbson.Marshaler())
	manager := NewManager(n, r, storage,
		idepkgtest.TrustStore(),
		fileScheme, temp, configPath, wm, syncTick, term.NopInterrupter())
	return manager, n, r, temp, storage
}

// makePAXXattrTarball builds a gzipped tar with a single executable whose
// PAX records include the macOS "com.apple.provenance" xattr containing a
// non-UTF-8 byte (0xad). This mirrors what Go binaries on macOS carry and
// is the payload that triggered the original install failure.
func makePAXXattrTarball(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	const content = "#!/bin/sh\necho hi\n"
	hdr := &tar.Header{
		Name:   "bin/tool",
		Mode:   0o755,
		Size:   int64(len(content)),
		Format: tar.FormatPAX,
		PAXRecords: map[string]string{
			"SCHILY.xattr.com.apple.provenance": "\xad",
		},
	}
	require.NoError(t, tw.WriteHeader(hdr))
	_, err := tw.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}

// failingUpdateStorage wraps a storageapi.Service and returns failErr from
// Update calls whose ID matches failKey. All other operations delegate
// to the inner service unchanged.
type failingUpdateStorage struct {
	storageapi.Service
	failKey string
	failErr error
}

func (s *failingUpdateStorage) Update(
	ctx context.Context, id string,
	updates []storageapi.Update, precond ...storageapi.Precondition,
) error {
	if id == s.failKey {
		return s.failErr
	}
	return s.Service.Update(ctx, id, updates, precond...)
}

func TestInstallNoConfigPromptOnFailure(t *testing.T) {
	t.Parallel()
	t.Run("no config prompt scheduled when storage update fails", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages()
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "configpkg", Version: "1"},
		})

		temp, err := os.MkdirTemp("", "")
		require.NoError(t, err)
		t.Cleanup(func() { _ = os.RemoveAll(temp) })

		configPath := filepath.Join(temp, "config.yaml")
		// Create user config so processConfig would otherwise schedule a prompt.
		require.NoError(t, os.WriteFile(configPath, []byte("{}\n"), 0644))

		n := idepkgtest.NewNotifications(t)
		rel := idepkgtest.NewReleaseManager(pkgs, versions)
		fileScheme := newLocalScheme(temp)
		var promptCalled atomic.Bool
		wm := &mockPrompter{
			floatingFn: func(_ browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
				// Recorded and asserted on the test goroutine after
				// Wait — install runs on a different goroutine where
				// t.Fatal would not reliably abort the install path.
				promptCalled.Store(true)
				return &mockWindow{}, nil
			},
		}
		storage := &failingUpdateStorage{
			Service: storagestub.NewInMemoryService(),
			failKey: "configpkg:1",
			failErr: errors.New("simulated storage update failure"),
		}
		m := NewManager(n, rel, storage,
			idepkgtest.TrustStore(), fileScheme, temp, configPath, wm,
			syncTick, term.NopInterrupter())

		err = m.InstallPackageVersion(context.Background(), "configpkg", "1", repl.NopProgressWriter())
		require.Error(t, err)
		assert.False(t, promptCalled.Load(),
			"config prompt must not be scheduled when install fails")

		// The installed package dir should have been rolled back.
		pkgDir := makePackageVersionDirname(temp, "configpkg", "1")
		_, statErr := os.Stat(pkgDir)
		assert.True(t, os.IsNotExist(statErr),
			"package version dir should be removed after rollback")

		// User config should be untouched.
		data, err := os.ReadFile(configPath)
		require.NoError(t, err)
		assert.Equal(t, "{}\n", string(data),
			"user config should not be modified when install fails")
	})
}

func TestEditorModeParamExposed(t *testing.T) {
	t.Parallel()

	const src = `
config = {}
if RUNE_EDITOR_MODE == "standard":
    config["picked_mode"] = "standard"
else:
    config["picked_mode"] = "other"
`
	cases := []struct {
		mode    string
		wantMod string
	}{
		{"vim", "other"},
		{"standard", "standard"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			got, err := decodePkgConfig(
				"config.star", []byte(src), "pkg", release.Version("1"), tc.mode,
			)
			require.NoError(t, err)
			assert.Equal(t, tc.wantMod, got["picked_mode"])
		})
	}
}

func TestPkgConfigFilePrefersYAML(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.star"), []byte("config = {}\n"), 0o644))
	assert.Equal(t, filepath.Join(dir, "config.yaml"), pkgConfigFile(dir))
}

func TestPkgConfigFileFallsBackToStar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.star"), []byte("config = {}\n"), 0o644))
	assert.Equal(t, filepath.Join(dir, "config.star"), pkgConfigFile(dir))
}

func TestPkgConfigFileNestedWrapperDir(t *testing.T) {
	t.Parallel()
	t.Run("yaml under single wrapper dir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "rune-agent")
		require.NoError(t, os.MkdirAll(filepath.Join(wrapper, "bin"), 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(wrapper, "config.yaml"), []byte("{}\n"), 0o644))
		assert.Equal(t, filepath.Join(wrapper, "config.yaml"), pkgConfigFile(dir))
	})
	t.Run("star under single wrapper dir", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "pkg")
		require.NoError(t, os.MkdirAll(wrapper, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(wrapper, "config.star"), []byte("config = {}\n"), 0o644))
		assert.Equal(t, filepath.Join(wrapper, "config.star"), pkgConfigFile(dir))
	})
	t.Run("top level still wins over nested", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		wrapper := filepath.Join(dir, "rune-agent")
		require.NoError(t, os.MkdirAll(wrapper, 0o755))
		require.NoError(t, os.WriteFile(
			filepath.Join(wrapper, "config.yaml"), []byte("{}\n"), 0o644))
		require.NoError(t, os.WriteFile(
			filepath.Join(dir, "config.yaml"), []byte("{}\n"), 0o644))
		assert.Equal(t, filepath.Join(dir, "config.yaml"), pkgConfigFile(dir))
	})
}

func fileInode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	st, ok := info.Sys().(*syscall.Stat_t)
	require.True(t, ok, "expected *syscall.Stat_t")
	return uint64(st.Ino)
}

func TestCopyExecutablesAtomicSwap(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	require.NoError(t, os.Mkdir(filepath.Join(srcDir, "bin"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcDir, "bin", "ext"),
		[]byte("new-binary"), 0o755))

	target := filepath.Join(dstDir, "ext")
	require.NoError(t, os.WriteFile(target, []byte("old-binary"), 0o755))
	oldInode := fileInode(t, target)

	files := []executableEntry{{Name: "bin/ext", Mode: 0o755}}
	require.NoError(t, copyExecutables(files, srcDir, dstDir))

	got, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "new-binary", string(got))

	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())

	assert.NotEqual(t, oldInode, fileInode(t, target),
		"executable should be swapped via rename, not truncated in place")

	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	require.Len(t, entries, 1, "no leftover temp files")
	assert.Equal(t, "ext", entries[0].Name())
}

func TestCopyExecutablesMissingSource(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	files := []executableEntry{{Name: "bin/missing", Mode: 0o755}}
	err := copyExecutables(files, srcDir, dstDir)
	require.Error(t, err)
	entries, derr := os.ReadDir(dstDir)
	require.NoError(t, derr)
	assert.Empty(t, entries, "no temp files left behind on error")
}

func TestCopyExecutablesSkipsEntriesOutsideBin(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()
	// Install records from older releases list executables found anywhere
	// in the package; reusing such a version must not copy them.
	files := []executableEntry{
		{Name: "bin/tool", Mode: 0o755},
		{Name: "lib/tree-sitter.so", Mode: 0o755},
		{Name: "pkg/tool/darwin_arm64/link", Mode: 0o755},
	}
	for _, f := range files {
		path := filepath.Join(srcDir, filepath.FromSlash(f.Name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, nil, 0o755))
	}

	require.NoError(t, copyExecutables(files, srcDir, dstDir))

	entries, err := os.ReadDir(dstDir)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "tool", entries[0].Name())
}

func TestUntarCollectsOnlyTopLevelBinExecutables(t *testing.T) {
	t.Parallel()
	entries := []struct {
		name string
		mode int64
		want bool
	}{
		{name: "bin/tool", mode: 0o755, want: true},
		{name: "./bin/dotted", mode: 0o755, want: true},
		{name: "bin/readme.txt", mode: 0o644},
		{name: "bin/.hidden", mode: 0o755},
		{name: "bin/nested/tool", mode: 0o755},
		{name: "lib/tree-sitter.so", mode: 0o755},
		{name: "pkg/tool/darwin_arm64/link", mode: 0o755},
		{name: "agent-browser/bin/agent-browser", mode: 0o755},
		{name: "make.bash", mode: 0o755},
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	var want []executableEntry
	for _, e := range entries {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode}))
		if e.want {
			want = append(want, executableEntry{Name: e.name, Mode: e.mode})
		}
	}
	require.NoError(t, tw.Close())

	got, _, err := untar(t.TempDir(), &buf, nil)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

const (
	verifyPkgVersion = release.Version("1")
	dotPkgID         = "dotpkg"
	dotEntrypoint    = "extension_dot"
	flatPkgID        = "flatpkg"
	flatEntrypoint   = "extension_flat"
	entrypointScript = "#!/bin/sh\nexit 0\n"
)

// installVerifyPackages installs two signed packages that differ only in the
// tar shape publishers produce: dotpkg carries the "./" prefixes `tar -c .`
// emits, flatpkg does not. Installing both keeps every lookup scanning past a
// package that does not own the executable being verified.
func installVerifyPackages(t *testing.T) (*Manager, string) {
	t.Helper()
	manager, _, rm, dataDir := newTestManager(t,
		idepkgtest.MakePackages(
			release.Package{Name: dotPkgID, Latest: verifyPkgVersion},
			release.Package{Name: flatPkgID, Latest: verifyPkgVersion},
		),
		idepkgtest.MakeBundles(
			[]release.Bundle{{Package: dotPkgID, Version: verifyPkgVersion}},
			[]release.Bundle{{Package: flatPkgID, Version: verifyPkgVersion}},
		))
	rm.SetTarball(dotPkgID, pkgTarball(t, dotEntrypoint, "./"))
	rm.SetTarball(flatPkgID, pkgTarball(t, flatEntrypoint, ""))
	for _, pkgID := range []string{dotPkgID, flatPkgID} {
		require.NoError(t, manager.InstallPackageVersion(
			context.Background(), pkgID, verifyPkgVersion, repl.NopProgressWriter()))
	}
	return manager, dataDir
}

func TestVerifyExtensionEntrypointAfterUpgrade(t *testing.T) {
	t.Parallel()
	manager, _, rm, dataDir := newTestManager(t,
		idepkgtest.MakePackages(release.Package{Name: dotPkgID, Latest: "2"}),
		idepkgtest.MakeBundles([]release.Bundle{
			{Package: dotPkgID, Version: "1"},
			{Package: dotPkgID, Version: "2"},
		}))
	rm.SetTarball(dotPkgID, pkgTarballScript(t, dotEntrypoint, "./", entrypointScript))
	require.NoError(t, manager.InstallPackageVersion(
		context.Background(), dotPkgID, "1", repl.NopProgressWriter()))
	rm.SetTarball(dotPkgID, pkgTarballScript(t, dotEntrypoint, "./", "#!/bin/sh\nexit 2\n"))
	require.NoError(t, manager.InstallPackageVersion(
		context.Background(), dotPkgID, "2", repl.NopProgressWriter()))

	fingerprint, ok := manager.VerifyExtensionEntrypoint(
		filepath.Join(makeBinDirname(dataDir), dotEntrypoint))
	assert.True(t, ok)
	assert.NotEmpty(t, fingerprint)
}

func TestVerifyExtensionEntrypointEmbeddedSigners(t *testing.T) {
	t.Parallel()

	// testdata/signed/<fingerprint>.tar.gz is a stub package and the .asc
	// beside it its detached signature, made offline with that embedded
	// signer's real private key. The tarball must be re-signed if it changes.
	signers := []struct {
		fingerprint string
		entrypoint  string
	}{
		{fingerprint: "D3F9E65DE72888CC03D45CF5064D4ABCFA6D9338", entrypoint: "extension_d3f9e65d"},
		{fingerprint: "E885B45B2BD6E53B5C50077A49421B9547B5B830", entrypoint: "extension_e885b45b"},
	}
	type signedFixture struct {
		tarball   []byte
		keyID     string
		signature string
	}
	load := func(t *testing.T, fingerprint string) signedFixture {
		t.Helper()
		path := filepath.Join("testdata", "signed", fingerprint+".tar.gz")
		tarball, err := os.ReadFile(path)
		require.NoError(t, err)
		signature, err := os.ReadFile(path + ".asc")
		require.NoError(t, err)
		// Release signing records the primary key's ID, which for a v4 key
		// is the low 64 bits of its fingerprint.
		return signedFixture{
			tarball:   tarball,
			keyID:     fingerprint[len(fingerprint)-16:],
			signature: string(signature),
		}
	}
	const pkgID = "signed"
	install := func(t *testing.T, tarball []byte, signedBy signedFixture) (*Manager, string, error) {
		t.Helper()
		manager, _, rm, dataDir := newTestManagerWithTrust(t,
			idepkgtest.MakePackages(release.Package{Name: pkgID, Latest: verifyPkgVersion}),
			idepkgtest.MakeBundles([]release.Bundle{{Package: pkgID, Version: verifyPkgVersion}}),
			pkgtrust.NewStore())
		rm.SetSignedTarball(pkgID, tarball, signedBy.keyID, signedBy.signature)
		err := manager.InstallPackageVersion(
			context.Background(), pkgID, verifyPkgVersion, repl.NopProgressWriter())
		return manager, dataDir, err
	}

	for i, signer := range signers {
		t.Run(signer.fingerprint, func(t *testing.T) {
			t.Parallel()
			fixture := load(t, signer.fingerprint)
			manager, dataDir, err := install(t, fixture.tarball, fixture)
			require.NoError(t, err)

			fingerprint, ok := manager.VerifyExtensionEntrypoint(
				filepath.Join(makeBinDirname(dataDir), signer.entrypoint))
			require.True(t, ok)
			assert.Equal(t, signer.fingerprint, fingerprint)
			assert.True(t, pkgtrust.NewStore().IsTrustedFingerprint(fingerprint))
		})

		other := signers[(i+1)%len(signers)]
		t.Run(signer.fingerprint+" signature does not transfer", func(t *testing.T) {
			t.Parallel()
			tarball := load(t, signer.fingerprint).tarball
			_, _, err := install(t, tarball, load(t, other.fingerprint))
			require.ErrorContains(t, err, "verify bundle signature")
		})
	}
}

// pkgTarball builds a gzipped tar holding an extension entrypoint under bin/,
// which is where package configs point through $RUNE_DATADIR/bin/<name>.
func pkgTarball(t *testing.T, entrypoint, prefix string) []byte {
	t.Helper()
	return pkgTarballScript(t, entrypoint, prefix, entrypointScript)
}

func pkgTarballScript(t *testing.T, entrypoint, prefix, script string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	if prefix != "" {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: prefix, Mode: 0o755, Typeflag: tar.TypeDir,
		}))
	}
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: prefix + "bin/", Mode: 0o755, Typeflag: tar.TypeDir,
	}))
	config := "extensions:\n  " + entrypoint +
		":\n    path: '$RUNE_DATADIR/bin/" + entrypoint + "'\n"
	for _, file := range []struct {
		name    string
		content string
		mode    int64
	}{
		{name: prefix + "config.yaml", content: config, mode: 0o644},
		{name: prefix + "bin/" + entrypoint, content: script, mode: 0o755},
	} {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: file.name, Mode: file.mode, Size: int64(len(file.content)),
		}))
		_, err := tw.Write([]byte(file.content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// etcHost is a Manager over a temporary data directory that records the
// packages whose config it wrote.
type etcHost struct {
	m          *Manager
	n          *idepkgtest.Notifications
	rm         *idepkgtest.ReleaseManager
	dataDir    string
	configPath string

	mu     sync.Mutex
	writes []string
}

// newEtcHost returns a host whose user config is configName under the
// data directory, and that serves the given bundles; pkgTars are the
// tarballs served for the packages that have one, the others serve the
// embedded fixtures.
func newEtcHost(
	t *testing.T, configName string, bundles [][]release.Bundle, pkgTars map[string][]byte,
) *etcHost {
	t.Helper()
	dataDir := t.TempDir()
	h := &etcHost{
		n:          idepkgtest.NewNotifications(t),
		dataDir:    dataDir,
		configPath: filepath.Join(dataDir, configName),
	}
	var packages []release.Package
	for _, versions := range bundles {
		packages = append(packages, release.Package{
			Name: versions[0].Package, Latest: versions[len(versions)-1].Version,
		})
	}
	h.rm = idepkgtest.NewReleaseManager(
		idepkgtest.MakePackages(packages...), idepkgtest.MakeBundles(bundles...))
	for id, tarball := range pkgTars {
		h.rm.SetTarball(id, tarball)
	}
	h.m = NewManager(h.n, h.rm, storagestub.NewInMemoryService(), idepkgtest.TrustStore(),
		newLocalScheme(dataDir), dataDir, h.configPath, &mockPrompter{}, syncTick,
		term.NopInterrupter(), WithAfterConfigWrite(h.afterWrite))
	return h
}

func (h *etcHost) afterWrite(pkgID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.writes = append(h.writes, pkgID)
}

func (h *etcHost) written() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.writes)
}

func (h *etcHost) install(t *testing.T, pkgID string, version release.Version) {
	t.Helper()
	require.NoError(t, h.m.InstallPackageVersion(
		context.Background(), pkgID, version, repl.NopProgressWriter()))
	h.n.RequireNoErrorNotification()
}

func (h *etcHost) writeConfig(t *testing.T, config string) {
	t.Helper()
	require.NoError(t, os.WriteFile(h.configPath, []byte(config), 0o644))
}

func (h *etcHost) readConfig(t *testing.T) string {
	t.Helper()
	return readFileString(t, h.configPath)
}

func (h *etcHost) etcPath(pkgID, ext string) string {
	return makePackageEtcPath(h.dataDir, pkgID, ext)
}

func (h *etcHost) readEtc(t *testing.T, pkgID, ext string) string {
	t.Helper()
	return readFileString(t, h.etcPath(pkgID, ext))
}

const configPkgV1YAML = "env:\n  GOROOT: $RUNE_DATADIR/pkg/$RUNE_PKG_ID/$RUNE_PKG_VERSION/go\n" +
	"settings:\n  theme: dark\n  indent: 4\n"

const configPkgV2YAML = "env:\n  GOROOT: $RUNE_DATADIR/pkg/$RUNE_PKG_ID/$RUNE_PKG_VERSION/go\n" +
	"settings:\n  theme: light\n  indent: 2\n  newkey: added\n"

func configPkgBundles() [][]release.Bundle {
	return [][]release.Bundle{{
		{Package: "configpkg", Version: "1"},
		{Package: "configpkg", Version: "2"},
	}}
}

func TestInstallConfig(t *testing.T) {
	t.Parallel()

	t.Run("a yaml config is copied with a trailer and included", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.yaml", configPkgBundles(), nil)
		h.install(t, "configpkg", "1")

		assert.Equal(t, configPkgV1YAML+etcTrailer("configpkg", "1"),
			h.readEtc(t, "configpkg", ".yaml"))
		assert.Equal(t, "include:\n  - $RUNE_DATADIR/etc/configpkg.yaml\n", h.readConfig(t))
		assert.Equal(t, []string{"configpkg"}, h.written())
	})

	t.Run("a star config is copied verbatim", func(t *testing.T) {
		t.Parallel()
		const star = "# the package's own comment\nconfig[\"settings\"] = {\"from\": RUNE_PKG_ID}"
		h := newEtcHost(t, "config.yaml",
			[][]release.Bundle{{{Package: "starpkg", Version: "3"}}},
			map[string][]byte{"starpkg": idepkgtest.MakeTarball(t, map[string]string{
				"config.star": star, "lib/readme.txt": "x\n",
			})})
		h.install(t, "starpkg", "3")

		assert.Equal(t, star+"\n"+etcTrailer("starpkg", "3"), h.readEtc(t, "starpkg", ".star"))
		assert.Equal(t, "include:\n  - $RUNE_DATADIR/etc/starpkg.star\n", h.readConfig(t))
	})

	t.Run("the include goes into the existing list of a user config", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.yaml", configPkgBundles(), nil)
		h.writeConfig(t, "# mine\ninclude:\n  - ~/mine.yaml\neditor:\n  mode: vim\n")
		h.install(t, "configpkg", "1")

		assert.Equal(t, "# mine\ninclude:\n  - ~/mine.yaml\n  - $RUNE_DATADIR/etc/configpkg.yaml\n"+
			"editor:\n  mode: vim\n", h.readConfig(t))
	})

	t.Run("a starlark user config gets an include call", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.star", configPkgBundles(), nil)
		h.writeConfig(t, "# mine\nconfig[\"editor\"][\"mode\"] = \"vim\"\n")
		h.install(t, "configpkg", "1")

		assert.Equal(t, "# mine\ninclude(\"$RUNE_DATADIR/etc/configpkg.yaml\")\n"+
			"config[\"editor\"][\"mode\"] = \"vim\"\n", h.readConfig(t))
	})

	t.Run("a commented-out include is not added again", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.yaml", configPkgBundles(), nil)
		const config = "include:\n  # - $RUNE_DATADIR/etc/configpkg.yaml\n"
		h.writeConfig(t, config)
		h.install(t, "configpkg", "1")

		assert.Equal(t, config, h.readConfig(t))
		assert.FileExists(t, h.etcPath("configpkg", ".yaml"))
	})

	t.Run("use rewrites the etc file for the version in use", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.yaml", configPkgBundles(), nil)
		h.install(t, "configpkg", "1")
		h.install(t, "configpkg", "2")
		assert.Equal(t, configPkgV2YAML+etcTrailer("configpkg", "2"),
			h.readEtc(t, "configpkg", ".yaml"))

		require.NoError(t, h.m.UsePackageVersion(context.Background(), "configpkg", "1"))

		assert.Equal(t, configPkgV1YAML+etcTrailer("configpkg", "1"),
			h.readEtc(t, "configpkg", ".yaml"))
		assert.Equal(t, "include:\n  - $RUNE_DATADIR/etc/configpkg.yaml\n", h.readConfig(t),
			"the include is not repeated")
		assert.Equal(t, []string{"configpkg", "configpkg", "configpkg"}, h.written())
	})

	t.Run("a version in the other format replaces the sibling and its include", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.yaml", configPkgBundles(), nil)
		h.install(t, "configpkg", "1")
		h.rm.SetTarball("configpkg", idepkgtest.MakeTarball(t, map[string]string{
			"config.star": "config[\"settings\"] = {}\n", "lib/readme.txt": "x\n",
		}))
		h.install(t, "configpkg", "2")

		assert.NoFileExists(t, h.etcPath("configpkg", ".yaml"))
		assert.FileExists(t, h.etcPath("configpkg", ".star"))
		assert.Equal(t, "include:\n  - $RUNE_DATADIR/etc/configpkg.star\n", h.readConfig(t))
	})

	t.Run("a version without a config removes the package's", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.yaml", configPkgBundles(), nil)
		h.install(t, "configpkg", "1")
		h.rm.SetTarball("configpkg", idepkgtest.MakeTarball(t, map[string]string{
			"lib/readme.txt": "x\n",
		}))
		h.install(t, "configpkg", "2")

		assert.NoFileExists(t, h.etcPath("configpkg", ".yaml"))
		assert.Equal(t, "include:\n", h.readConfig(t))
	})

	t.Run("a nested package id makes the etc directories", func(t *testing.T) {
		t.Parallel()
		const id = "github.com/owner/repo"
		h := newEtcHost(t, "config.yaml",
			[][]release.Bundle{{{Package: id, Version: "abc1234"}}},
			map[string][]byte{id: idepkgtest.MakeTarball(t, map[string]string{
				"config.yaml": "extensions:\n  repo:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID\n",
			})})
		h.install(t, id, "abc1234")

		assert.FileExists(t, h.etcPath(id, ".yaml"))
		assert.Equal(t, "include:\n  - $RUNE_DATADIR/etc/github.com/owner/repo.yaml\n", h.readConfig(t))
	})
}

func TestDeletePackageRemovesConfig(t *testing.T) {
	t.Parallel()

	t.Run("the whole package", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.yaml", configPkgBundles(), nil)
		h.writeConfig(t, "include:\n  - ~/mine.yaml\n")
		h.install(t, "configpkg", "1")
		h.install(t, "configpkg", "2")

		require.NoError(t, h.m.DeletePackage(context.Background(), "configpkg"))

		assert.NoFileExists(t, h.etcPath("configpkg", ".yaml"))
		assert.Equal(t, "include:\n  - ~/mine.yaml\n", h.readConfig(t))
		assert.Equal(t, []string{"configpkg", "configpkg", "configpkg"}, h.written(),
			"the hook runs once for the removal")
	})

	t.Run("the version in use when forced", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.star", configPkgBundles(), nil)
		h.install(t, "configpkg", "1")
		h.install(t, "configpkg", "2")

		require.NoError(t, h.m.DeletePackageVersion(context.Background(), "configpkg", "2", true))

		assert.NoFileExists(t, h.etcPath("configpkg", ".yaml"))
		assert.Equal(t, "", h.readConfig(t))
	})

	t.Run("a version not in use leaves the config", func(t *testing.T) {
		t.Parallel()
		h := newEtcHost(t, "config.yaml", configPkgBundles(), nil)
		h.install(t, "configpkg", "1")
		h.install(t, "configpkg", "2")

		require.NoError(t, h.m.DeletePackageVersion(context.Background(), "configpkg", "1", false))

		assert.Equal(t, configPkgV2YAML+etcTrailer("configpkg", "2"),
			h.readEtc(t, "configpkg", ".yaml"))
		assert.Equal(t, "include:\n  - $RUNE_DATADIR/etc/configpkg.yaml\n", h.readConfig(t))
		assert.Equal(t, []string{"configpkg", "configpkg"}, h.written())
	})
}
