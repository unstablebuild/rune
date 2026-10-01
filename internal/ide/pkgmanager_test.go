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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/pkgshell"
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

// TestPkgManager_HandlePkgInstall_NotAuthenticated verifies that the
// interactive :pkginstall command suppresses ErrNotAuthenticated and
// surfaces the raw auth error from the `pkg install` shell command.
func TestPkgManager_HandlePkgInstall_NotAuthenticated(t *testing.T) {
	t.Parallel()
	pkgs := idepkgtest.MakePackages(release.Package{Name: "go"})
	bundles := idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.ExpectReturnErr(auth.ErrNotAuthenticated)

	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer m.Close()

	h := pkgshell.New(pkgshell.Config{
		Manager:       m.pkgmanager.pkg,
		UpdateChecker: m.pkgmanager.uc,
	})
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: pkgshell.CommandName,
		Args: []string{"install", "go"},
	}, repl.NopProgressWriter())
	require.ErrorIs(t, err, auth.ErrNotAuthenticated)
}

// TestPkgManager_HandlePkgInstall_Forbidden verifies that the
// `pkg install` shell command translates a 403 from the cdnrelease
// endpoint into the friendly ErrForbidden sentinel instead of
// bubbling up the raw cdnrelease error.
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

	h := pkgshell.New(pkgshell.Config{
		Manager:       m.pkgmanager.pkg,
		UpdateChecker: m.pkgmanager.uc,
	})
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: pkgshell.CommandName,
		Args: []string{"install", "go"},
	}, repl.NopProgressWriter())
	require.ErrorIs(t, err, idepkg.ErrForbidden)
}

// TestPkgManager_CompletePkgInstall_Forbidden verifies that tab
// completion against the releases endpoint surfaces the friendly
// ErrForbidden sentinel to the completer framework when the server
// returns 403.
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

	h := pkgshell.New(pkgshell.Config{
		Manager:       m.pkgmanager.pkg,
		UpdateChecker: m.pkgmanager.uc,
	})
	_, err := h.Complete(context.Background(), pkgshell.CommandName,
		[]string{"install", ""})
	require.ErrorIs(t, err, idepkg.ErrForbidden)
}

// TestPkgManager_HandlePkgInstall_Forbidden_Integration spins up a
// real cdnrelease.Manager against an httptest.Server returning 403
// so the end-to-end error type/status contract between blue and rune
// is exercised, not just the in-process mock.
func TestPkgManager_HandlePkgInstall_Forbidden_Integration(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()
	rm := cdnrelease.NewManager(srv.Client(), srv.URL)

	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer m.Close()

	h := pkgshell.New(pkgshell.Config{
		Manager:       m.pkgmanager.pkg,
		UpdateChecker: m.pkgmanager.uc,
	})
	_, err := h.HandleCommand(context.Background(), repl.Command{
		Name: pkgshell.CommandName,
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
	h0 := pkgshell.New(pkgshell.Config{
		Manager:       m.pkgmanager.pkg,
		UpdateChecker: m.pkgmanager.uc,
	})
	it0, err := h0.Complete(context.Background(), pkgshell.CommandName,
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
	h := pkgshell.New(pkgshell.Config{
		Manager:       m.pkgmanager.pkg,
		UpdateChecker: m.pkgmanager.uc,
	})
	it, err := h.Complete(context.Background(), pkgshell.CommandName,
		[]string{"install", ""})
	require.NoError(t, err)
	names, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Equal(t, []string{"go"}, names)

	require.NoError(t, m.Close())
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
	m.tutorialsInstalled = func([]string) (bool, error) { return false, nil }
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
	m.tutorialsInstalled = func([]string) (bool, error) { return false, nil }
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
