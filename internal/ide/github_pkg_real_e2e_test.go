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

//go:build e2e

package ide

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
	"unstable.build/rune/internal/ide/pkgshell"
)

func TestGitHubPkgRealEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real github package e2e in -short mode")
	}
	if os.Getenv("RUNE_GITHUB_PKG_REAL_E2E") != "1" {
		t.Skip("set RUNE_GITHUB_PKG_REAL_E2E=1 to run the real github " +
			"package e2e (clones over the network and runs the toolchain)")
	}

	// The python package is tagged (v0.0.1, v0.0.2, v0.0.3), so it
	// exercises tag listing, pinned-tag install, and update detection
	// (the default branch is ahead of an old tag).
	t.Run("python", func(t *testing.T) {
		uvBin := lookPathOrSkip(t, "uv")
		f := realGitHubFixture{
			pkgID:       "github.com/ubcabo/rune-extension-testdata-py",
			extID:       "py-demo",
			lang:        "python",
			requirement: "python",
			toolName:    "uv",
			toolPath:    uvBin,
			installTag:  "v0.0.1",
			wantTags:    []release.Version{"v0.0.1", "v0.0.2", "v0.0.3"},
		}
		runRealGitHubPkgE2E(t, f)
	})

	// The go package has no tags, so it exercises the tagless path:
	// List is `latest` only and install resolves latest to the HEAD
	// short commit sha.
	t.Run("go", func(t *testing.T) {
		goBin := lookPathOrSkip(t, "go")
		f := realGitHubFixture{
			pkgID:       "github.com/ubcabo/rune-extension-testdata-go",
			extID:       "go-demo",
			lang:        "go",
			requirement: "go",
			toolName:    "go",
			toolPath:    goBin,
			wantTags:    nil,
		}
		runRealGitHubPkgE2E(t, f)
	})
}

// realGitHubFixture describes one language variant of the real-GitHub
// end-to-end test.
type realGitHubFixture struct {
	pkgID       string
	extID       string
	lang        string
	requirement string
	toolName    string
	toolPath    string
	// installTag, when set, installs that tag and asserts an update to
	// HEAD is available; empty installs `latest`.
	installTag string
	// wantTags are the tags List must include besides `latest`.
	wantTags []release.Version
}

func runRealGitHubPkgE2E(t *testing.T, f realGitHubFixture) {
	t.Helper()
	m, dispatch := newRealGitHubPkgHandler(t, f.requirement, f.toolName, f.toolPath)
	ctx := context.Background()

	// List resolves against real GitHub: the moving `latest` plus the
	// repository's published tags.
	it, err := m.pkgmanager.pkg.ListPackageVersions(ctx, f.pkgID, nil)
	require.NoError(t, err)
	bundles, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	versions := make([]release.Version, len(bundles))
	for i, b := range bundles {
		versions[i] = b.Version
	}
	require.Contains(t, versions, release.Latest, "latest must be installable")
	for _, tag := range f.wantTags {
		require.Contains(t, versions, tag, "published tag %s must be listed", tag)
	}
	if len(f.wantTags) == 0 {
		require.Equal(t, []release.Version{release.Latest}, versions,
			"an untagged repository lists only latest")
	}

	h := pkgshell.New(pkgshell.Config{Manager: m.pkgmanager.pkg})

	installArgs := []string{"install", f.pkgID}
	if f.installTag != "" {
		installArgs = append(installArgs, f.installTag)
	}
	_, err = h.HandleCommand(ctx, repl.Command{
		Name: pkgshell.CommandName,
		Args: installArgs,
	}, repl.NopProgressWriter())
	require.NoError(t, err)

	version, ok := pkgVersionInUse(t, m.pkgmanager.pkg, f.pkgID)
	require.True(t, ok, "package must be installed")
	if f.installTag != "" {
		assert.Equal(t, release.Version(f.installTag), version,
			"a tag install stores the tag name as the version")

		// The default branch has moved past the pinned tag, so an
		// update to the current HEAD short sha must be reported.
		updates, err := idepkg.CheckForUpdates(ctx, m.pkgmanager.pkg)
		require.NoError(t, err)
		var pkgUpdate *idepkg.Update
		for i := range updates {
			if updates[i].Package == f.pkgID {
				pkgUpdate = &updates[i]
				break
			}
		}
		require.NotNil(t, pkgUpdate,
			"an update must be available: HEAD is ahead of the installed tag")
		assert.Equal(t, release.Version(f.installTag), pkgUpdate.Current)
		assert.Len(t, string(pkgUpdate.Latest), 12,
			"the available update is the HEAD short commit sha")
		assert.NotEqual(t, pkgUpdate.Current, pkgUpdate.Latest)
	} else {
		assert.Len(t, string(version), 12,
			"a latest install stores the resolved HEAD short commit sha")
	}

	// The extension starts live off the config merge and connects
	// through the SDK; its command becomes dispatchable once
	// registration completes. Dispatch it, then poll for the sentinel
	// the command writes.
	require.Eventually(t, func() bool {
		return dispatch(ctx, f.extID) == nil &&
			storageSentinelPresent(ctx, m.sixDir, f.extID, f.lang)
	}, 5*time.Minute, 2*time.Second,
		"the %s command must register and write its sentinel on dispatch", f.extID)
}

// newRealGitHubPkgHandler builds the pkg-install harness pointed at real
// GitHub (no remote-URL override) with an official release manager that
// serves the language requirement by delivering the host toolchain into
// <dataDir>/bin. It returns the handler and a helper that dispatches a
// command to the opened workspace.
func newRealGitHubPkgHandler(
	t *testing.T, requirement, toolName, toolPath string,
) (*testWorkspaceManagerHandler, func(ctx context.Context, cmd string, args ...string) error) {
	t.Helper()

	// The official distribution serves the language requirement; its
	// tarball delivers the host toolchain into <dataDir>/bin so
	// `uv run` / `go run` starts the extension.
	pkgs := idepkgtest.MakePackages(release.Package{Name: requirement, Latest: "1"})
	bundles := idepkgtest.MakeBundles(
		[]release.Bundle{{Package: requirement, Version: "1"}})
	rm := idepkgtest.NewReleaseManager(pkgs, bundles)
	rm.SetMissProgressComplete(true)
	rm.SetTarball(requirement, toolWrapperTarball(t, toolName, toolPath))

	wsDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	configPath := filepath.Join(t.TempDir(), "rune.yaml")
	require.NoError(t, os.WriteFile(configPath,
		[]byte("editor:\n  mode: modal\n"), 0o644))

	// No WithRemoteURL override: github IDs resolve against real GitHub.
	m := newPkgInstallExtHandler(t, configPath, rm, nopExtensions{}, nil)
	t.Cleanup(func() { _ = m.Close() })

	runner, err := extensionv2.NewRunner(context.Background(), m.mu, m.sixDir)
	require.NoError(t, err)
	m.extensionRunner = runner

	wsURI, err := workspaceapi.CurrentUserHostURI(wsDir)
	require.NoError(t, err)
	require.NoError(t, m.addWorkspace(wsURI, true, false, -1))
	m.quiesce()

	dispatch := func(ctx context.Context, cmd string, args ...string) error {
		w := m.workspaces[m.focus]
		if w == nil {
			return assert.AnError
		}
		return w.ex.dispatchCommandCtx(ctx, cmd, args...)
	}
	return m, dispatch
}
