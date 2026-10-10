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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/blue/release/cdnrelease"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/console/pkgconsole"
	"unstable.build/rune/internal/ide/hostenv"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/localstorage"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace"
)

func TestPackageManagerConcurrent(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages(
		release.Package{Name: "go"},
		release.Package{Name: "six", Latest: "2"},
	)
	bundles := idepkgtest.MakeBundles(
		[]release.Bundle{
			{Package: "go", Version: "2"},
			{Package: "go", Version: "3"},
			{Package: "go", Version: "1", CreatedAt: time.Now()},
		},
		[]release.Bundle{
			{Package: "six", Version: "1"},
			{Package: "six", Version: "2"},
		},
	)
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.SetMissProgressComplete(true)
	cfg := defaultCfg()
	var m *testWorkspaceManagerHandler
	var mu sync.Mutex
	// Sync scheduler — production hosts (gui.Update / tui.Run)
	// dispatch UserFunc while holding the IDE locker. This stub
	// runs fn on the calling goroutine without locking; the test
	// caller is responsible for whatever ordering it needs.
	cfg.scheduleNextTick = func(fn func()) bool {
		fn()
		return true
	}
	m = newTestWorkspaceManagerHandlerForPkgManagerWithInterrupterCfg(t, rm, true,
		term.NopInterrupter(), cfg, &mu)
	require.NoError(t, m.pkgmanager.setAutoInstall())

	const n = 1000
	var errs [n]error
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			it, err := m.pkgmanager.LibDir(context.Background(), "go")
			if err != nil {
				errs[i] = err
				return
			}
			defer it.Close()
			for {
				_, ok := it.Next(context.Background())
				if !ok {
					break
				}
			}
			if err := it.Err(); err != nil {
				errs[i] = err
			}
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("LibDir %d returned error: %v", i, err)
		}
	}
}

func TestPkgManager_InstallLatest_AlreadyInstalledByAnotherCaller(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
	bundles := idepkgtest.MakeBundles(
		[]release.Bundle{{Package: "go", Version: "1", CreatedAt: time.Now()}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.SetMissProgressComplete(true)
	cfg := defaultCfg()
	cfg.scheduleNextTick = func(fn func()) bool {
		fn()
		return true
	}
	m := newTestWorkspaceManagerHandlerForPkgManagerWithInterrupterCfg(t, rm, true,
		term.NopInterrupter(), cfg, new(sync.Mutex))

	// Another caller finishes the install after this one saw the
	// package missing but before it starts installing.
	require.NoError(t, m.pkgmanager.pkg.InstallPackageVersion(
		context.Background(), "go", "1", nil))
	it, err := m.pkgmanager.installLatest(context.Background(), "go", "1")
	require.NoError(t, err)
	require.NoError(t, it.Close())
}

func TestPkgManager_LibDir_NotAuthenticated(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.ExpectReturnErr(auth.ErrNotAuthenticated)

	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer m.Close()

	_, err := m.pkgmanager.LibDir(context.Background(), "go")
	require.Error(t, err)
	assert.True(t, errors.Is(err, storageapi.ErrNotFound),
		"expected storageapi.ErrNotFound, got %v", err)
}

func TestPkgManager_HandlePkgInstall_NotAuthenticated(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.ExpectReturnErr(auth.ErrNotAuthenticated)

	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer m.Close()

	h := pkgconsole.New(pkgconsole.Config{Manager: m.pkgmanager.pkg})
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: pkgconsole.CommandName,
		Args: []string{"install", "go"},
	}, repl.NopProgressWriter())
	require.ErrorIs(t, err, auth.ErrNotAuthenticated)
}

func TestPkgManager_HandlePkgInstall_Forbidden(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.ExpectReturnErr(&cdnrelease.StatusError{
		URL:    "https://example/api/releases/darwin-arm64/packages",
		Status: http.StatusForbidden,
	})

	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer m.Close()

	h := pkgconsole.New(pkgconsole.Config{Manager: m.pkgmanager.pkg})
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: pkgconsole.CommandName,
		Args: []string{"install", "go"},
	}, repl.NopProgressWriter())
	require.ErrorIs(t, err, idepkg.ErrForbidden)
}

func TestPkgManager_CompletePkgInstall_Forbidden(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.ExpectReturnErr(&cdnrelease.StatusError{
		URL:    "https://example/api/releases/darwin-arm64/packages",
		Status: http.StatusForbidden,
	})

	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer m.Close()

	h := pkgconsole.New(pkgconsole.Config{Manager: m.pkgmanager.pkg})
	_, err := h.Complete(context.Background(), pkgconsole.CommandName,
		[]string{"install", ""})
	require.ErrorIs(t, err, idepkg.ErrForbidden)
}

func TestPkgManager_HandlePkgInstall_Forbidden_Integration(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	rm := cdnrelease.NewManager(srv.Client(), srv.URL)

	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer m.Close()

	h := pkgconsole.New(pkgconsole.Config{Manager: m.pkgmanager.pkg})
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: pkgconsole.CommandName,
		Args: []string{"install", "go"},
	}, repl.NopProgressWriter())
	require.ErrorIs(t, err, idepkg.ErrForbidden)
}

func TestPackageManagerLibDir(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages(
		release.Package{Name: "go", Latest: "3"},
		release.Package{Name: "six", Latest: "2"},
	)
	bundles := idepkgtest.MakeBundles(
		[]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
			{Package: "go", Version: "3"},
		},
		[]release.Bundle{
			{Package: "six", Version: "1"},
			{Package: "six", Version: "2"},
		},
	)
	t.Run("prompt, no install", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)

		it, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			{"",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
█●██████████████████████████████████████
│                                      │
│  Do you want to install package      │
│  "go"?                               │
│                                      │
│                                      │
│                                      │
│Yes           Yes, Always          No │
└──────────────────────────────────────┘
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
			{"N",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│          workspaceWallpaper          │
│                                      │
│                                      │
│                                      │
│                                      │
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
		}

		handlertest.TestHandlerSequence(t, m, 40, 15, cases)

		_, err = iterator.ToSlice(context.Background(), it)
		require.Equal(t, document.ErrNotFound, err)

		require.NoError(t, m.Close())
	})
	t.Run("prompt, user key ESC", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)

		it, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			{"<",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│          workspaceWallpaper          │
│                                      │
│                                      │
│                                      │
│                                      │
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
		}

		handlertest.TestHandlerSequence(t, m, 40, 15, cases)

		_, err = iterator.ToSlice(context.Background(), it)
		require.Equal(t, document.ErrNotFound, err)

		require.NoError(t, m.Close())
	})

	t.Run("prompt, yes install", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)

		it, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			{"Y",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│          workspaceWallpaper          │
│                                      │
│                                      │
│                                      │
│                                      │
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
		}

		handlertest.TestHandlerSequence(t, m, 40, 15, cases)

		slice, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.NotEmpty(t, slice)
		require.NoError(t, m.Close())
	})

	t.Run("prompt, yes, always install", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)

		it, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			{"A",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│          workspaceWallpaper          │
│                                      │
│                                      │
│                                      │
│                                      │
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
		}

		handlertest.TestHandlerSequence(t, m, 40, 15, cases)

		slice, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.NotEmpty(t, slice)

		it, err = m.pkgmanager.LibDir(context.Background(), "six")
		require.NoError(t, err)

		slice, err = iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.NotEmpty(t, slice)

		require.NoError(t, m.Close())
	})

	t.Run("auto_install config installs without prompting", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		rm.SetMissProgressComplete(true)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
		m.pkgmanager.autoInstall = true

		it, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		slice, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.NotEmpty(t, slice)
		require.NoError(t, m.Close())
	})

	t.Run("legacy stored never is ignored and prompts again", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
		require.NoError(t, m.pkgmanager.storage.Set(context.Background(),
			installStorageKey, installStorageValue{Value: false}))

		it, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			{"N",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│          workspaceWallpaper          │
│                                      │
│                                      │
│                                      │
│                                      │
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
		}
		handlertest.TestHandlerSequence(t, m, 40, 15, cases)

		_, err = iterator.ToSlice(context.Background(), it)
		require.Equal(t, document.ErrNotFound, err)
		require.NoError(t, m.Close())
	})

	t.Run("onboarding active installs without prompting", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		rm.SetMissProgressComplete(true)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
		m.pkgmanager.autoInstall = false
		m.onboardingActive = func() bool { return true }

		it, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		slice, err := iterator.ToSlice(context.Background(), it)
		require.NoError(t, err)
		assert.NotEmpty(t, slice)
		require.NoError(t, m.Close())
	})

	for name, decline := range map[string]term.Event{
		"no":      {Type: term.EventKey, Ch: 'N'},
		"dismiss": {Type: term.EventKey, Key: term.KeyEsc},
	} {
		t.Run("declining with "+name+" is not asked again", func(t *testing.T) {
			t.Parallel()

			rm := idepkgtest.NewReleaseManager(pkgs, bundles)
			m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
			defer func() { require.NoError(t, m.Close()) }()

			it, err := m.pkgmanager.LibDir(context.Background(), "go")
			require.NoError(t, err)
			m.Resize(40, 15)
			require.Contains(t, handlertest.DrawHandler(m, 40, 15), "Do you want to install")
			m.Handle(decline)
			_, err = iterator.ToSlice(context.Background(), it)
			require.ErrorIs(t, err, storageapi.ErrNotFound)

			it, err = m.pkgmanager.LibDir(context.Background(), "go")
			assert.Nil(t, it)
			require.ErrorIs(t, err, storageapi.ErrNotFound)
			assert.NotContains(t, handlertest.DrawHandler(m, 40, 15), "Do you want to install")
		})
	}

	t.Run("onboarding inactive keeps prompting", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
		m.pkgmanager.autoInstall = false
		m.onboardingActive = func() bool { return false }

		it, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			{"",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
█●██████████████████████████████████████
│                                      │
│  Do you want to install package      │
│  "go"?                               │
│                                      │
│                                      │
│                                      │
│Yes           Yes, Always          No │
└──────────────────────────────────────┘
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
			{"N",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│          workspaceWallpaper          │
│                                      │
│                                      │
│                                      │
│                                      │
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
		}
		handlertest.TestHandlerSequence(t, m, 40, 15, cases)

		_, err = iterator.ToSlice(context.Background(), it)
		require.Equal(t, document.ErrNotFound, err)
		require.NoError(t, m.Close())
	})

	t.Run("prompt, yes install, simultaneous calls to LibDir", func(t *testing.T) {
		t.Parallel()

		rm := idepkgtest.NewReleaseManager(pkgs, bundles)
		m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)

		it1, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		it2, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		it3, err := m.pkgmanager.LibDir(context.Background(), "go")
		require.NoError(t, err)

		cases := []handlertest.SequenceTestCase{
			{"Y",
				`┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│          workspaceWallpaper          │
│                                      │
│                                      │
│                                      │
│                                      │
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`},
		}

		handlertest.TestHandlerSequence(t, m, 40, 15, cases)

		for _, it := range []iterator.Iterator[string]{it1, it2, it3} {
			slice, err := iterator.ToSlice(context.Background(), it)
			require.NoError(t, err)
			assert.NotEmpty(t, slice)
			require.NoError(t, m.Close())
		}
	})
}

func TestSetReleaseManager(t *testing.T) {
	t.Parallel()
	rm := idepkgtest.NewReleaseManager(idepkgtest.MakePackages(), idepkgtest.MakeBundles())
	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)

	// Before the second release manager is set, there are no packages.
	h0 := pkgconsole.New(pkgconsole.Config{Manager: m.pkgmanager.pkg})
	it0, err := h0.Complete(context.Background(), pkgconsole.CommandName,
		[]string{"install", ""})
	require.NoError(t, err)
	names0, err := iterator.ToSlice(context.Background(), it0)
	require.NoError(t, err)
	require.Empty(t, names0)

	pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "3"}})
	rm2 := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm2.SetMissProgressComplete(true)
	m.setReleaseManager(rm2)

	// After re-setting the release manager, the new packages are
	// reachable through a freshly constructed pkg shell.
	h := pkgconsole.New(pkgconsole.Config{Manager: m.pkgmanager.pkg})
	it, err := h.Complete(context.Background(), pkgconsole.CommandName,
		[]string{"install", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Equal(t, []string{"go"}, names)

	require.NoError(t, m.Close())
}

func TestEditorRespellsWhatAnOlderReleaseExpandedWhenItStarts(t *testing.T) {
	const expanded = "editor:\n  mode: modal\n" +
		"gui:\n  env:\n    STARTPKG_HOME: <DATA>/lib/startpkg\n" +
		"    PATH: /opt/mine/bin:<DATA>/lib/startpkg/bin:$PATH\n" +
		"debugger:\n  startpkg:\n    command: <DATA>/lib/startpkg/dap --listen={addr}\n" +
		"extensions:\n  startpkg:\n    path: <DATA>/lib/startpkg/ext\n" +
		"tutorials:\n  startpkg-intro: <DATA>/lib/startpkg/intro.star\n"
	const respelled = "editor:\n  mode: modal\n" +
		"gui:\n  env:\n    STARTPKG_HOME: $RUNE_DATADIR/lib/startpkg\n" +
		"    PATH: /opt/mine/bin:$RUNE_DATADIR/lib/startpkg/bin:$PATH\n" +
		"debugger:\n  startpkg:\n    command: $RUNE_DATADIR/lib/startpkg/dap --listen={addr}\n" +
		"extensions:\n  startpkg:\n    path: $RUNE_DATADIR/lib/startpkg/ext\n" +
		"tutorials:\n  startpkg-intro: $RUNE_DATADIR/lib/startpkg/intro.star\n"
	for _, launch := range launches {
		t.Run(launch.name, func(t *testing.T) {
			h := newRestartHost(t)
			h.writeConfig(t, expanded)

			e := h.start(t, launch)
			assert.Equal(t, respelled, h.readConfig(t))
			merges := h.takeMerges()
			require.Len(t, merges, 1)
			assert.True(t, merges[0].TouchesPath("gui", "env"),
				"the host re-applies gui.env after the merge")
			assert.Contains(t, e.tutorialNames(), "startpkg-intro")
			assert.Zero(t, countFloatingWindows(e.IDE, e.mu),
				"the user already had everything the package provides: nothing to ask")
			e.open(t)
			e.requireExtensionStartedAt(t, h.expand("<DATA>/lib/startpkg/ext"))
			e.stop(t)

			e = h.start(t, launch)
			assert.Equal(t, respelled, h.readConfig(t))
			assert.Empty(t, h.takeMerges(), "the next start has nothing to respell")
			assert.Zero(t, countFloatingWindows(e.IDE, e.mu))
		})
	}
}

func TestEditorAsksAtStartBeforeReplacingAnotherMachinesDataDir(t *testing.T) {
	const foreign = "editor:\n  mode: modal\n" +
		"gui:\n  env:\n    STARTPKG_HOME: /home/other/.rune/lib/startpkg\n" +
		"    PATH: $RUNE_DATADIR/lib/startpkg/bin:$PATH\n" +
		"debugger:\n  startpkg:\n    command: $RUNE_DATADIR/lib/startpkg/dap --listen={addr}\n" +
		"extensions:\n  startpkg:\n    path: $RUNE_DATADIR/lib/startpkg/ext\n" +
		"tutorials:\n  startpkg-intro: $RUNE_DATADIR/lib/startpkg/intro.star\n"
	const allowed = "editor:\n  mode: modal\n" +
		"gui:\n  env:\n    STARTPKG_HOME: $RUNE_DATADIR/lib/startpkg\n" +
		"    PATH: $RUNE_DATADIR/lib/startpkg/bin:$PATH\n" +
		"debugger:\n  startpkg:\n    command: $RUNE_DATADIR/lib/startpkg/dap --listen={addr}\n" +
		"extensions:\n  startpkg:\n    path: $RUNE_DATADIR/lib/startpkg/ext\n" +
		"tutorials:\n  startpkg-intro: $RUNE_DATADIR/lib/startpkg/intro.star\n"
	for _, launch := range launches {
		for _, answer := range []struct {
			name  string
			key   rune
			after string
		}{
			{name: "allowed", key: 'a', after: allowed},
			{name: "denied", key: 'd', after: foreign},
		} {
			t.Run(launch.name+"/"+answer.name, func(t *testing.T) {
				h := newRestartHost(t)
				h.writeConfig(t, foreign)

				e := h.start(t, launch)
				assert.Equal(t, foreign, h.readConfig(t), "nothing changes before the user answers")
				assert.Empty(t, h.takeMerges())
				require.Equal(t, 1, countFloatingWindows(e.IDE, e.mu), "the prompt is on the screen the user sees")
				screen := e.screen()
				assert.Contains(t, screen, "startpkg")
				assert.Contains(t, screen, "STARTPKG_HOME: $RUNE_DATADIR/lib/startpkg",
					"the prompt shows what it writes")

				e.press(answer.key)
				assert.Equal(t, answer.after, h.readConfig(t))
				assert.Zero(t, countFloatingWindows(e.IDE, e.mu))
				merges := h.takeMerges()
				if answer.after == foreign {
					assert.Empty(t, merges)
					return
				}
				require.Len(t, merges, 1)
				assert.True(t, merges[0].TouchesPath("gui", "env"))
				e.stop(t)

				e = h.start(t, launch)
				assert.Equal(t, allowed, h.readConfig(t))
				assert.Zero(t, countFloatingWindows(e.IDE, e.mu), "an approved value is not asked about again")
				assert.Empty(t, h.takeMerges())
			})
		}
	}
}

type launch struct {
	name string
	// inWorkspace launches as `rune <dir>` does.
	inWorkspace bool
}

var launches = []launch{
	{name: "home screen"},
	{name: "workspace", inWorkspace: true},
}

type restartHost struct {
	dir, dataDir, configPath string
	rm                       *idepkgtest.ReleaseManager

	mu     sync.Mutex
	merges []idepkg.ConfigMergeEvent
}

const startpkgConfig = "gui:\n  env:\n    STARTPKG_HOME: $RUNE_DATADIR/lib/$RUNE_PKG_ID\n" +
	"    PATH: $RUNE_DATADIR/lib/$RUNE_PKG_ID/bin:$PATH\n" +
	"debugger:\n  startpkg:\n    command: $RUNE_DATADIR/lib/$RUNE_PKG_ID/dap --listen={addr}\n" +
	"extensions:\n  startpkg:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/ext\n" +
	"tutorials:\n  startpkg-intro: $RUNE_DATADIR/lib/$RUNE_PKG_ID/intro.star\n"

func newRestartHost(t *testing.T) *restartHost {
	t.Helper()
	dir := t.TempDir()
	h := &restartHost{
		dir:        dir,
		dataDir:    t.TempDir(),
		configPath: filepath.Join(dir, "rune.yaml"),
	}
	h.rm = idepkgtest.NewReleaseManager(
		idepkgtest.MakePackages(release.Package{Name: "startpkg", Latest: "1"}),
		idepkgtest.MakeBundles([]release.Bundle{{Package: "startpkg", Version: "1"}}))
	h.rm.SetMissProgressComplete(true)
	h.rm.SetTarball("startpkg", filesTarball(t, map[string]string{
		"config.yaml":    startpkgConfig,
		"intro.star":     minimalStarTutorial,
		"lib/readme.txt": "startpkg\n",
	}))
	require.NoError(t, os.WriteFile(h.configPath, []byte("editor:\n  mode: modal\n"), 0o644))

	// Without e.mu: the merge hook takes the handler lock.
	e := h.start(t, launch{})
	require.NoError(t, e.PackageManager().InstallPackageVersion(context.Background(),
		"startpkg", "1", repl.NopProgressWriter()))
	e.drain()
	e.stop(t)
	h.takeMerges()
	return h
}

type startedEditor struct {
	*IDE
	mu      *sync.Mutex
	drain   func() uint64
	runner  *recordingRunner
	dir     string
	dataDir string
	stop    func(t *testing.T)
}

func (h *restartHost) start(t *testing.T, l launch) *startedEditor {
	t.Helper()
	mu := new(sync.Mutex)
	sched, drain := newTestScheduler(t, mu)
	storage := localstorage.New(context.Background(), h.dataDir, docbson.Marshaler())
	runner := &recordingRunner{}
	workspace := ""
	if l.inWorkspace {
		workspace = h.dir
	}
	i, err := New(workspace, h.configPath, h.dataDir, idepkgtest.TrustStore(), storage,
		WithReleaseManager(h.rm),
		WithPublishEvent(nopPublishEvent),
		WithExtensionsRunner(recordingExtensionsRunner{runner: runner}),
		WithLocker(mu),
		WithScheduleNextTick(sched),
		WithPackageConfigMergeHook(h.afterMerge),
		// Its reopen prompt would be a second floating window.
		WithoutSessionReopen(),
	)
	require.NoError(t, err)
	stopped := false
	stop := func(t *testing.T) {
		if stopped {
			return
		}
		stopped = true
		drain()
		require.NoError(t, i.Close())
		require.NoError(t, storage.Close())
	}
	t.Cleanup(func() { stop(t) })
	_ = i.Ready()
	drain()
	i.WaitWorkspaces()
	drain()
	mu.Lock()
	i.root.Resize(120, 40)
	mu.Unlock()
	drain()
	return &startedEditor{IDE: i, mu: mu, drain: drain, runner: runner,
		dir: h.dir, dataDir: h.dataDir, stop: stop}
}

func (e *startedEditor) open(t *testing.T) {
	t.Helper()
	uri, err := workspaceapi.CurrentUserHostURI(e.dir)
	require.NoError(t, err)
	e.mu.Lock()
	require.NoError(t, e.workspaceHandler.addWorkspace(uri, false, false, -1))
	e.mu.Unlock()
	e.drain()
	e.WaitWorkspaces()
	e.drain()
}

func (h *restartHost) afterMerge(event idepkg.ConfigMergeEvent) (idepkg.ConfigMergeResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.merges = append(h.merges, event)
	return idepkg.ConfigMergeResult{LivePaths: [][]string{{"gui", "env"}}}, nil
}

func (h *restartHost) takeMerges() []idepkg.ConfigMergeEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	merges := h.merges
	h.merges = nil
	return merges
}

func (h *restartHost) expand(s string) string {
	return strings.ReplaceAll(s, "<DATA>", h.dataDir)
}

func (h *restartHost) writeConfig(t *testing.T, config string) {
	t.Helper()
	require.NoError(t, os.WriteFile(h.configPath, []byte(h.expand(config)), 0o644))
}

func (h *restartHost) readConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(h.configPath)
	require.NoError(t, err)
	return string(b)
}

func (e *startedEditor) tutorialNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.TutorialNames()
}

func (e *startedEditor) screen() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	w := term.NewStringWriter(120, 40)
	e.root.Draw(w)
	_ = w.Flush()
	return w.String()
}

func (e *startedEditor) press(key rune) {
	e.mu.Lock()
	e.root.Handle(term.Event{Type: term.EventKey, Ch: key})
	e.mu.Unlock()
	e.drain()
}

func (e *startedEditor) requireExtensionStartedAt(t *testing.T, entrypoint string) {
	t.Helper()
	var calls []runCall
	require.Eventually(t, func() bool {
		calls = slices.DeleteFunc(e.runner.runCalls(), func(c runCall) bool {
			return c.id != "startpkg"
		})
		return len(calls) > 0
	}, 10*time.Second, 20*time.Millisecond, "startpkg's extension never started")
	for _, c := range calls {
		assert.Equal(t, entrypoint, hostenv.ExpandDataDir(c.path, e.dataDir))
	}
}

func filesTarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(content)),
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gzw.Close())
	return buf.Bytes()
}

func pkgVersionInUse(
	t testing.TB, pm idepkg.PackageManager, pkgID string,
) (release.Version, bool) {
	t.Helper()
	version, ok, err := pm.PackageVersionInUse(context.Background(), pkgID)
	require.NoError(t, err)
	return version, ok
}

// hostPackageManager stands in for the package manager of another host.
// It implements only what pkgManager may call; any other method panics
// on the nil embedded interface.
type hostPackageManager struct {
	idepkg.PackageManager
	mu        sync.Mutex
	latest    release.Version
	installed map[string]release.Version
}

func newHostPackageManager(latest release.Version) *hostPackageManager {
	return &hostPackageManager{latest: latest, installed: map[string]release.Version{}}
}

func (f *hostPackageManager) LibDir(
	_ context.Context, pkgID string,
) (iterator.Iterator[string], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	version, ok := f.installed[pkgID]
	if !ok {
		return nil, idepkg.ErrNotInstalled
	}
	return iterator.FromSlice([]string{
		"/home/studio/.rune/pkg/" + pkgID + "/" + string(version) + "/bin/" + pkgID,
	}), nil
}

func (f *hostPackageManager) LatestVersion(
	context.Context, string,
) (release.Version, error) {
	return f.latest, nil
}

func (f *hostPackageManager) InstallPackageVersion(
	_ context.Context, pkgID string, version release.Version, pw repl.ProgressWriter,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.installed[pkgID] = version
	return nil
}

func (f *hostPackageManager) installedVersion(pkgID string) (release.Version, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.installed[pkgID]
	return v, ok
}

func TestPkgManagerPromptsForHost(t *testing.T) {
	t.Parallel()
	const prompt = `┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
█●██████████████████████████████████████
│                                      │
│  Do you want to install package      │
│  "go" on studio?                     │
│                                      │
│                                      │
│                                      │
│Yes           Yes, Always          No │
└──────────────────────────────────────┘
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`
	const closed = `┌──────────────────────────────────────┐
│                                      │
├──────────────────────────────────────┤
│                                      │
│                                      │
│                                      │
│                                      │
│          workspaceWallpaper          │
│                                      │
│                                      │
│                                      │
│                                      │
├──────────────────────────────────────┤
│1                                     │
└━─────────────────────────────────────┘`
	tests := []struct {
		name    string
		input   string
		want    []string
		wantErr error
	}{
		{name: "yes installs on the host", input: "Y",
			want: []string{"/home/studio/.rune/pkg/go/2/bin/go"}},
		{name: "no installs nothing", input: "N", wantErr: storageapi.ErrNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rm := idepkgtest.NewReleaseManager(idepkgtest.MakePackages(), idepkgtest.MakeBundles())
			m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
			defer func() { require.NoError(t, m.Close()) }()

			host := newHostPackageManager("2")
			pm := newPkgManager(host, "studio", m.pkgmanager.pkg, m.notifications.current(),
				storageapi.WithPartition(m.ideStorage, idepkg.StoragePartition),
				m.workspaceManagerHandler, m.scheduleNextTick,
				term.NopInterrupter(), false)
			defer func() { require.NoError(t, pm.Close()) }()

			it, err := pm.LibDir(context.Background(), "go")
			require.NoError(t, err)

			handlertest.RunHandlerSequence(t, m, 40, 15, []handlertest.SequenceTestCase{
				{InputSequence: "", Expected: prompt},
				{InputSequence: tc.input, Expected: closed},
			})

			paths, err := iterator.ToSlice(context.Background(), it)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				_, ok := host.installedVersion("go")
				assert.False(t, ok)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, paths)
			}
			_, local := pkgVersionInUse(t, m.pkgmanager.pkg, "go")
			assert.False(t, local, "a host install must not touch the local manager")
		})
	}
}

func newTestWorkspaceManagerHandlerForPkgManager(
	t *testing.T, releaseManager release.Manager,
	showManual bool, notificationsWidth int,
	gitRemoteURL ...func(pkgID string) string,
) *testWorkspaceManagerHandler {
	cfg := defaultCfg()
	// disable progress hint animation to avoid flaky assertion on spinner frame
	updatedCommandCfg := cfg.cfg["command"].(map[string]any)
	updatedCommandCfg["show_progress_hint"] = false

	homeURI, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	m := new(testWorkspaceManagerHandler)
	m.workspaceManagerHandler = new(workspaceManagerHandler)
	if len(gitRemoteURL) > 0 {
		m.gitRemoteURL = gitRemoteURL[0]
	}

	// ensure that command manual is always shown
	if showManual {
		updatedCfg := defaultCfg().cfg["command"].(map[string]any)
		updatedCfg["show_manual"] = true
		cfg.cfg["command"] = updatedCfg
	}
	runner := FuncExtensionsRunner(testRunnerFn)
	shutdownShaderCfg := nopShutdownShaderConfig()
	mu := new(sync.Mutex)
	interrupter := term.NopInterrupter()
	shRunner := new(shaderRunner)
	shRunner.init(handler.Nop(), interrupter, term.Attributes{},
		shutdownShaderCfg, loadingShaderConfig{}, openShaderConfig{},
		component.FrameCharSetDefault())
	// pkgmanager tests drive the install prompt synchronously: a
	// LibDir call schedules the Prompt to open and hands back an
	// iterator that blocks until the user picks an option. With
	// the async default scheduler the Prompt would open after the
	// test starts dispatching keys, races and deadlocks. Use a
	// sync scheduler that runs fn on the calling goroutine.
	cfg.scheduleNextTick = func(fn func()) bool {
		fn()
		return true
	}

	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	// A package install merges the extension into the user config, so
	// configPath must be a real writable file inside the temp dir;
	// otherwise the default sentinel path drops config + .backup files
	// in the package working directory.
	cfg.configPath = filepath.Join(dir, "rune.yaml")
	manager := workspace.NewManager(cfg.workspace(), cfg.scheduleNextTick)
	manager.RegisterScheme(workspace.FileScheme, workspace.NewFileScheme)

	notiCfg := notificationsConfig()
	notiCfg.Width = notificationsWidth
	storage := localstorage.New(context.Background(), dir, docbson.Marshaler())
	m.tutorialsInstalled = func([]string) ([]string, error) { return nil, nil }
	err = m.workspaceManagerHandler.init(nil, homeURI, manager,
		notiCfg, cfg, storage,
		dir, func(ev term.Event) bool {
			if ev.Type == term.EventInterrupt {
				interrupter.Interrupt(ev.Context)
			}
			return true
		}, runner, idepkgtest.TrustStore(), mu, nil,
		func() (ideConfig, error) { return cfg, nil },
		".sixrc", 0, 0, 0, '1', 0, 0, true, nil, releaseManager,
		shRunner, 0, nil, false, false, newCommandObserverRegistry())
	require.NoError(t, err)
	m.subscribeCommand(textapi.CommandManual{Name: "pkgwait"}, text.FuncCommandHandler(
		func(ctx context.Context, cmd textapi.Command) error {
			require.Len(t, cmd.Args, 1)
			it, err := m.pkgmanager.pkg.LibDir(ctx, cmd.Args[0])
			require.NoError(t, err)
			it.Next(ctx)
			it.Close()
			return nil
		}, nil))
	return m
}

func newTestWorkspaceManagerHandlerForPkgManagerWithInterrupterCfg(
	t *testing.T, releaseManager release.Manager, showManual bool,
	interrupter term.Interrupter,
	cfg ideConfig, mu sync.Locker,
) *testWorkspaceManagerHandler {
	dir, err := os.MkdirTemp("", "")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	// Share the caller's (deliberately synchronous) scheduler so the
	// manager and the handler dispatch through the same path.
	manager := workspace.NewManager(cfg.workspace(), cfg.scheduleNextTick)
	manager.RegisterScheme(workspace.FileScheme, workspace.NewFileScheme)
	ret := newTestWorkspaceManagerHandlerWithReleaseManager(t, manager,
		cfg, FuncExtensionsRunner(testRunnerFn), nil, nil, dir, nil,
		nopShutdownShaderConfig(), releaseManager, showManual, interrupter, mu)
	// only home workspace has a sync command prompt
	ret.empty.syncCommandPrompt = true
	// this allows blocking until packages are installed, for testing
	ret.subscribeCommand(textapi.CommandManual{Name: "pkgwait"}, text.FuncCommandHandler(
		func(ctx context.Context, cmd textapi.Command) error {
			require.Len(t, cmd.Args, 1)
			it, err := ret.pkgmanager.pkg.LibDir(ctx, cmd.Args[0])
			require.NoError(t, err)
			it.Next(ctx)
			it.Close()
			return nil
		}, nil))
	return ret
}

func newTestWorkspaceManagerHandlerWithReleaseManager(
	t *testing.T, manager *workspace.Manager,
	cfg ideConfig, runner ExtensionsRunner,
	extensions map[string]Extension, files []string, dir string,
	onTabsClick func(int) bool,
	shutdownShaderCfg shutdownShaderConfig,
	releaseManager release.Manager,
	showManual bool,
	interrupter term.Interrupter,
	mu sync.Locker,
) *testWorkspaceManagerHandler {
	homeURI, err := workspaceapi.ParseURI("file:///tmp")
	require.NoError(t, err)

	m := new(testWorkspaceManagerHandler)
	m.workspaceManagerHandler = new(workspaceManagerHandler)
	// ensure that command manual is always shown
	updatedCfg := defaultCfg().cfg["command"].(map[string]any)
	if showManual {
		updatedCfg["show_manual"] = true
	}
	cfg.cfg["command"] = updatedCfg

	shRunner := new(shaderRunner)
	shRunner.init(handler.Nop(), interrupter, term.Attributes{},
		shutdownShaderCfg, loadingShaderConfig{}, openShaderConfig{},
		component.FrameCharSetDefault())
	// Sync scheduler — see the matching block in
	// newTestWorkspaceManagerHandlerForPkgManager.
	cfg.scheduleNextTick = func(fn func()) bool {
		fn()
		return true
	}

	storage2 := localstorage.New(context.Background(), dir, docbson.Marshaler())
	m.tutorialsInstalled = func([]string) ([]string, error) { return nil, nil }
	err = m.workspaceManagerHandler.init(nil, homeURI, manager,
		notificationsConfig(), cfg, storage2,
		dir, func(ev term.Event) bool {
			if ev.Type == term.EventInterrupt {
				interrupter.Interrupt(ev.Context)
			}
			return true
		}, runner, idepkgtest.TrustStore(), mu, extensions,
		func() (ideConfig, error) { return cfg, nil },
		".sixrc", 0, 0, 0, '1', 0, 0, true, onTabsClick, releaseManager,
		shRunner, 0, nil, false, false, newCommandObserverRegistry())
	require.NoError(t, err)
	for i, file := range files {
		require.NoError(t, m.openFile(file, i == 0))
	}
	return m
}
