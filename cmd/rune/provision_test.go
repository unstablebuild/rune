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

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/internal/ide/gitpkg"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacessh"
)

// setFlagForTest temporarily points a *string flag at value and restores it.
func setFlagForTest(t *testing.T, target *string, value string) {
	t.Helper()
	old := *target
	*target = value
	t.Cleanup(func() { *target = old })
}

func newTestFileScheme(t *testing.T, root string) (workspace.Workspace, workspaceapi.URI) {
	t.Helper()
	uri, err := workspaceapi.CurrentUserHostURI(root)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = scheme.Close() })
	cwd := workspace.NewSchemeWorkspace(uri, scheme, func(fn func()) bool { fn(); return true })
	return cwd, uri
}

// TestLoadRemoteConfigAppliesGUIEnvAfterInstall asserts the load-after-install
// ordering on the -x path: once a package install has merged a gui.env block
// into the remote ~/.rune config, loading that config applies the vars to the
// process environment so extension-spawned tools inherit them.
func TestLoadRemoteConfigAppliesGUIEnvAfterInstall(t *testing.T) {
	const (
		envKey = "RUNE_TEST_REMOTE_GOROOT"
		envVal = "/remote/go/root"
	)
	require.NoError(t, os.Unsetenv(envKey))
	t.Cleanup(func() { _ = os.Unsetenv(envKey) })

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "config.yaml")
	// The remote config as it exists AFTER a package install merged its
	// gui.env block.
	merged := "editor:\n  mode: modal\ngui:\n  env:\n    " + envKey + ": " + envVal + "\n"
	require.NoError(t, os.WriteFile(configPath, []byte(merged), 0o644))

	setFlagForTest(t, flagConfigPath, configPath)
	setFlagForTest(t, flagDataPath, dataDir)

	cwd, uri := newTestFileScheme(t, t.TempDir())
	cfg := loadRemoteConfigAndApplyEnv(cwd, uri)
	require.NotNil(t, cfg)

	assert.Equal(t, envVal, os.Getenv(envKey),
		"gui.env from the remote config must be applied to the process env")

	// ~/.rune/bin must be on PATH so package executables resolve.
	assert.Contains(t, os.Getenv("PATH"), filepath.Join(dataDir, "bin"),
		"~/.rune/bin must be prepended to PATH")
}

// TestWorkspaceOverlayWinsOverHomeConfig asserts the workspace-root
// .rune/config.yaml overlay is layered on top of the remote-home config.
func TestWorkspaceOverlayWinsOverHomeConfig(t *testing.T) {
	const envKey = "RUNE_TEST_OVERLAY_VAR"
	require.NoError(t, os.Unsetenv(envKey))
	t.Cleanup(func() { _ = os.Unsetenv(envKey) })

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath,
		[]byte("editor:\n  mode: modal\ngui:\n  env:\n    "+envKey+": home\n"), 0o644))

	setFlagForTest(t, flagConfigPath, configPath)
	setFlagForTest(t, flagDataPath, dataDir)

	wsRoot := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(wsRoot, ".rune"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wsRoot, workspaceConfigFilename),
		[]byte("gui:\n  env:\n    "+envKey+": workspace\n"), 0o644))

	cwd, uri := newTestFileScheme(t, wsRoot)
	loadRemoteConfigAndApplyEnv(cwd, uri)

	assert.Equal(t, "workspace", os.Getenv(envKey),
		"workspace-root .rune/config.yaml must override the home config")
}

// TestInstallRemotePackagesEmptyManifestIsNoop asserts an empty --install
// manifest performs no install work (and does not panic).
func TestInstallRemotePackagesEmptyManifestIsNoop(t *testing.T) {
	setFlagForTest(t, flagWorkspaceServerInstall, "")
	setFlagForTest(t, flagDataPath, t.TempDir())
	cwd, _ := newTestFileScheme(t, t.TempDir())
	// Should return immediately without touching storage or the network.
	installRemotePackages(cwd)
}

// TestInstallRemotePackagesEmptyManifestEmitsNothing asserts that with no
// packages to install, nothing is written to the progress sink.
func TestInstallRemotePackagesEmptyManifestEmitsNothing(t *testing.T) {
	setFlagForTest(t, flagWorkspaceServerInstall, "")
	setFlagForTest(t, flagDataPath, t.TempDir())
	cwd, _ := newTestFileScheme(t, t.TempDir())

	var sink strings.Builder
	installRemotePackagesTo(cwd, &sink)
	assert.Empty(t, sink.String(), "empty manifest must emit no progress lines")
}

// TestInstallRemotePackagesEmitsProgress asserts the JSON-Lines progress
// stream. The release endpoint is pointed at an unreachable address so each
// install fails offline, exercising the installing → failed sequence per
// package followed by a final done line — without any network access. The
// assertions are order-independent except that the terminal done line must
// come last.
func TestInstallRemotePackagesEmitsProgress(t *testing.T) {
	setFlagForTest(t, flagWorkspaceServerInstall, "pkg-a@1.0.0,pkg-b@2.0.0")
	setFlagForTest(t, flagDataPath, t.TempDir())
	setFlagForTest(t, flagConfigPath, filepath.Join(t.TempDir(), "config.yaml"))
	// 127.0.0.1:0 is not a listening endpoint, so release downloads fail fast.
	setFlagForTest(t, flagHTTPAddress, "http://127.0.0.1:0")
	cwd, _ := newTestFileScheme(t, t.TempDir())

	var sink strings.Builder
	installRemotePackagesTo(cwd, &sink)

	var got []workspacessh.ProvisionProgress
	scanner := bufio.NewScanner(strings.NewReader(sink.String()))
	for scanner.Scan() {
		p, ok := workspacessh.ParseProvisionProgressLine(scanner.Bytes())
		require.True(t, ok, "every emitted line must parse as progress: %q", scanner.Text())
		got = append(got, p)
	}
	require.NoError(t, scanner.Err())

	require.NotEmpty(t, got)
	// The terminal done line must be emitted last, after every package.
	last := got[len(got)-1]
	assert.Equal(t, workspacessh.ProvisionPhaseDone, last.Phase)
	assert.Equal(t, 2, last.Total)

	// Each package must contribute exactly one installing and one failed
	// line. Failed lines must carry the underlying error detail.
	type phaseKey struct {
		pkg   string
		phase string
	}
	counts := map[phaseKey]int{}
	for _, p := range got[:len(got)-1] {
		counts[phaseKey{p.Package, p.Phase}]++
		if p.Phase == workspacessh.ProvisionPhaseFailed {
			assert.NotEmpty(t, p.Detail,
				"failed line for %s must carry the install error detail", p.Package)
		}
	}
	for _, pkg := range []string{"pkg-a", "pkg-b"} {
		assert.Equal(t, 1, counts[phaseKey{pkg, workspacessh.ProvisionPhaseInstalling}],
			"%s must emit exactly one installing line", pkg)
		assert.Equal(t, 1, counts[phaseKey{pkg, workspacessh.ProvisionPhaseFailed}],
			"%s must emit exactly one failed line", pkg)
	}
}

func TestRemoteProvisioningInstallsGitPackage(t *testing.T) {
	const pkgID = "github.com/unstablebuild/test-extension"
	repoDir := t.TempDir()
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "config.yaml"), []byte(
		"extensions:\n  test:\n    path: $RUNE_DATADIR/lib/$RUNE_PKG_ID/main.py\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "main.py"),
		[]byte("print('test')\n"), 0o644))
	wt, err := repo.Worktree()
	require.NoError(t, err)
	_, err = wt.Add(".")
	require.NoError(t, err)
	sha, err := wt.Commit("fixture", &git.CommitOptions{Author: &object.Signature{
		Name: "fixture", Email: "fixture@example.com", When: time.Now(),
	}})
	require.NoError(t, err)
	version := sha.String()[:12]

	dataDir := t.TempDir()
	setFlagForTest(t, flagDataPath, dataDir)
	setFlagForTest(t, flagConfigPath, filepath.Join(dataDir, "config.yaml"))
	setFlagForTest(t, flagHTTPAddress, "http://127.0.0.1:0")
	cwd, _ := newTestFileScheme(t, t.TempDir())
	releaseManager := newRemoteReleaseManager(gitpkg.WithRemoteURL(func(id string) string {
		assert.Equal(t, pkgID, id)
		return repoDir
	}))

	var sink strings.Builder
	installRemotePackageEntries(cwd, &sink, []idepkg.ProvisionEntry{{
		ID: pkgID, Version: version,
	}}, releaseManager)

	libPath := filepath.Join(dataDir, "lib", filepath.FromSlash(pkgID))
	target, err := os.Readlink(libPath)
	require.NoError(t, err)
	assert.Equal(t,
		filepath.Join(dataDir, "pkg", filepath.FromSlash(pkgID), version), target)
	assert.Contains(t, sink.String(), `"phase":"activating"`)
	assert.NotContains(t, sink.String(), `"phase":"failed"`)
}

// TestResolveRemoteEditorMode asserts the remote provisioning server resolves
// the user's editor mode from its ~/.rune config so RUNE_EDITOR_MODE is
// predeclared for package config.star scripts. exo resolves to its fallback,
// a missing config falls back to the vim default rather than an empty
// (undefined) mode, and a config that still says "modal" reaches packages as
// "vim".
func TestResolveRemoteEditorMode(t *testing.T) {
	t.Run("resolves exo to configured fallback", func(t *testing.T) {
		dataDir := t.TempDir()
		configPath := filepath.Join(dataDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath, []byte(
			"editor:\n  mode: exo\n  exo:\n    command: vim {file}\n"+
				"    goto: \"<esc>:{line}<enter>\"\n    quit: \"<esc>:qa<enter>\"\n"+
				"    fallback: helix\n"), 0o644))
		setFlagForTest(t, flagConfigPath, configPath)

		assert.Equal(t, "helix", resolveRemoteEditorMode())
	})

	t.Run("missing config defaults to vim", func(t *testing.T) {
		setFlagForTest(t, flagConfigPath, filepath.Join(t.TempDir(), "config.yaml"))

		assert.Equal(t, "vim", resolveRemoteEditorMode())
	})

	t.Run("resolves the deprecated modal mode to vim", func(t *testing.T) {
		dataDir := t.TempDir()
		configPath := filepath.Join(dataDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath,
			[]byte("editor:\n  mode: modal\n"), 0o644))
		setFlagForTest(t, flagConfigPath, configPath)

		assert.Equal(t, "vim", resolveRemoteEditorMode())
	})

	t.Run("resolves standard mode", func(t *testing.T) {
		dataDir := t.TempDir()
		configPath := filepath.Join(dataDir, "config.yaml")
		require.NoError(t, os.WriteFile(configPath,
			[]byte("editor:\n  mode: standard\n"), 0o644))
		setFlagForTest(t, flagConfigPath, configPath)

		assert.Equal(t, "standard", resolveRemoteEditorMode())
	})
}

// fakeInstaller stands in for the provisioning manager so the version-fallback
// logic can be tested without network or storage.
type fakeInstaller struct {
	// installErr[version] is returned by InstallPackageVersion for that version.
	installErr map[string]error
	latest     map[string]release.Version
	latestErr  map[string]error
	// downloadSamples[version] is a sequence of (progress,total) byte samples
	// pushed to the ProgressWriter before InstallPackageVersion returns, to
	// exercise the downloading-phase adapter.
	downloadSamples map[string][][2]int64

	installed []string // versions passed to InstallPackageVersion, in order
	used      []string // versions passed to UsePackageVersion, in order
}

func (f *fakeInstaller) InstallPackageVersion(
	_ context.Context, _ string, version release.Version, pw repl.ProgressWriter,
) error {
	f.installed = append(f.installed, string(version))
	for _, s := range f.downloadSamples[string(version)] {
		pw.Progress(s[0], s[1], "bytes")
	}
	return f.installErr[string(version)]
}

func (f *fakeInstaller) UsePackageVersion(
	_ context.Context, _ string, version release.Version,
) error {
	f.used = append(f.used, string(version))
	return nil
}

func (f *fakeInstaller) LatestVersion(
	_ context.Context, id string,
) (release.Version, error) {
	if err := f.latestErr[id]; err != nil {
		return "", err
	}
	return f.latest[id], nil
}

// TestInstallOnePackageFallsBackToLatest asserts that when the pinned version is
// not published for the remote platform (ErrVersionNotFound), the install
// degrades to the latest available version and reports it via progress.
func TestInstallOnePackageFallsBackToLatest(t *testing.T) {
	inst := &fakeInstaller{
		installErr: map[string]error{
			"v1.2.3": fmt.Errorf("nope: %w", idepkg.ErrVersionNotFound),
			"v9.9.9": nil,
		},
		latest: map[string]release.Version{"go": "v9.9.9"},
	}
	var sink strings.Builder
	ok := installOnePackage(context.Background(), inst, &sink,
		idepkg.ProvisionEntry{ID: "go", Version: "v1.2.3"}, 1, 1)
	require.True(t, ok, "fallback install must succeed")

	assert.Equal(t, []string{"v1.2.3", "v9.9.9"}, inst.installed,
		"must try the pinned version, then the latest")
	assert.Equal(t, []string{"v9.9.9"}, inst.used,
		"must activate the fallback version")

	var got []workspacessh.ProvisionProgress
	scanner := bufio.NewScanner(strings.NewReader(sink.String()))
	for scanner.Scan() {
		p, ok := workspacessh.ParseProvisionProgressLine(scanner.Bytes())
		require.True(t, ok)
		got = append(got, p)
	}
	// installing(pinned) then activating(fallback) — the activating line must
	// reflect the version actually installed, not the pinned one.
	require.Len(t, got, 2)
	assert.Equal(t, workspacessh.ProvisionPhaseInstalling, got[0].Phase)
	assert.Equal(t, workspacessh.ProvisionPhaseActivating, got[1].Phase)
	assert.Equal(t, "v9.9.9", got[1].Version,
		"activating progress must show the fallback version")
}

// TestInstallOnePackagePinnedSucceeds asserts the happy path: when the pinned
// version installs, no fallback is attempted.
func TestInstallOnePackagePinnedSucceeds(t *testing.T) {
	inst := &fakeInstaller{installErr: map[string]error{"v1.2.3": nil}}
	var sink strings.Builder
	ok := installOnePackage(context.Background(), inst, &sink,
		idepkg.ProvisionEntry{ID: "go", Version: "v1.2.3"}, 1, 1)
	require.True(t, ok)
	assert.Equal(t, []string{"v1.2.3"}, inst.installed)
	assert.Equal(t, []string{"v1.2.3"}, inst.used)
}

// TestInstallOnePackageFallsBackOnAnyError asserts that any pinned-version
// install failure (not only ErrVersionNotFound) transparently degrades to the
// latest available version, so a recoverable pinned-version gap never emits a
// failed progress line (and therefore never a warning notification).
func TestInstallOnePackageFallsBackOnAnyError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		pinnedErr error
	}{
		{"version not found", fmt.Errorf("nope: %w", idepkg.ErrVersionNotFound)},
		{"package not found", fmt.Errorf("nope: %w", idepkg.ErrPackageNotFound)},
		{"generic error", errors.New("network blip")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inst := &fakeInstaller{
				installErr: map[string]error{"v1.2.3": tc.pinnedErr, "v9.9.9": nil},
				latest:     map[string]release.Version{"go": "v9.9.9"},
			}
			var sink strings.Builder
			ok := installOnePackage(context.Background(), inst, &sink,
				idepkg.ProvisionEntry{ID: "go", Version: "v1.2.3"}, 1, 1)
			require.True(t, ok, "fallback to latest must succeed")
			assert.Equal(t, []string{"v1.2.3", "v9.9.9"}, inst.installed,
				"must try pinned then latest")
			assert.Equal(t, []string{"v9.9.9"}, inst.used)
			assert.NotContains(t, sink.String(), "failed",
				"a recoverable pinned-version gap must not emit a failed progress line")
		})
	}
}

// TestInstallOnePackageFailsWhenLatestAlsoFails asserts that a failed progress
// line is only emitted when the latest version is unavailable too.
func TestInstallOnePackageFailsWhenLatestAlsoFails(t *testing.T) {
	inst := &fakeInstaller{
		installErr: map[string]error{"v1.2.3": errors.New("network down"), "v9.9.9": errors.New("still down")},
		latest:     map[string]release.Version{"go": "v9.9.9"},
	}
	var sink strings.Builder
	ok := installOnePackage(context.Background(), inst, &sink,
		idepkg.ProvisionEntry{ID: "go", Version: "v1.2.3"}, 1, 1)
	require.False(t, ok)
	assert.Equal(t, []string{"v1.2.3", "v9.9.9"}, inst.installed)
	assert.Contains(t, sink.String(), "failed")
}

// TestInstallOnePackageFallbackAlsoMissing asserts that if both the pinned and
// the latest versions are unavailable, the package fails cleanly.
func TestInstallOnePackageFallbackAlsoMissing(t *testing.T) {
	inst := &fakeInstaller{
		installErr: map[string]error{
			"v1.2.3": fmt.Errorf("x: %w", idepkg.ErrVersionNotFound),
		},
		latestErr: map[string]error{"go": errors.New("no releases")},
	}
	var sink strings.Builder
	ok := installOnePackage(context.Background(), inst, &sink,
		idepkg.ProvisionEntry{ID: "go", Version: "v1.2.3"}, 1, 1)
	require.False(t, ok)
	assert.Contains(t, sink.String(), "failed")
}

// TestInstallOnePackageAlreadyInstalledIsNoOp asserts that re-provisioning an
// already-installed package is idempotent: it still activates the package and
// reports an activating (not failed) progress line, so the user never sees a
// spurious "already been installed" warning.
func TestInstallOnePackageAlreadyInstalledIsNoOp(t *testing.T) {
	inst := &fakeInstaller{
		installErr: map[string]error{
			"v1.2.3": fmt.Errorf("boom: %w", idepkg.ErrAlreadyInstalled),
		},
	}
	var sink strings.Builder
	ok := installOnePackage(context.Background(), inst, &sink,
		idepkg.ProvisionEntry{ID: "go", Version: "v1.2.3"}, 1, 1)
	require.True(t, ok, "an already-installed package is a successful no-op")
	assert.Equal(t, []string{"v1.2.3"}, inst.used,
		"an already-installed package must still be activated")
	assert.NotContains(t, sink.String(), "failed",
		"re-installing an existing package must not emit a failed progress line")

	var got []workspacessh.ProvisionProgress
	scanner := bufio.NewScanner(strings.NewReader(sink.String()))
	for scanner.Scan() {
		p, ok := workspacessh.ParseProvisionProgressLine(scanner.Bytes())
		require.True(t, ok)
		got = append(got, p)
	}
	require.Len(t, got, 2)
	assert.Equal(t, workspacessh.ProvisionPhaseInstalling, got[0].Phase)
	assert.Equal(t, workspacessh.ProvisionPhaseActivating, got[1].Phase)
}

// TestInstallOnePackageEmitsThrottledDownloadProgress asserts that byte-level
// download progress is forwarded as downloading lines scaled to KiB, and that
// samples within the same KiB bucket are throttled to a single line so a large
// download cannot flood the stderr back-channel.
func TestInstallOnePackageEmitsThrottledDownloadProgress(t *testing.T) {
	inst := &fakeInstaller{
		installErr: map[string]error{"v1.2.3": nil},
		downloadSamples: map[string][][2]int64{
			// Two samples in the first KiB bucket collapse to one line; the
			// next two cross into new KiB buckets and each emit.
			"v1.2.3": {
				{100, 4096},
				{500, 4096},
				{1024, 4096},
				{2048, 4096},
			},
		},
	}
	var sink strings.Builder
	ok := installOnePackage(context.Background(), inst, &sink,
		idepkg.ProvisionEntry{ID: "go", Version: "v1.2.3"}, 1, 2)
	require.True(t, ok)

	var downloading []workspacessh.ProvisionProgress
	scanner := bufio.NewScanner(strings.NewReader(sink.String()))
	for scanner.Scan() {
		p, ok := workspacessh.ParseProvisionProgressLine(scanner.Bytes())
		require.True(t, ok)
		if p.Phase == workspacessh.ProvisionPhaseDownloading {
			downloading = append(downloading, p)
		}
	}
	require.Len(t, downloading, 3, "samples within one KiB bucket must be throttled")
	assert.Equal(t, 0, downloading[0].Done)
	assert.Equal(t, 1, downloading[1].Done)
	assert.Equal(t, 2, downloading[2].Done)
	assert.Equal(t, 4, downloading[0].Of)
	assert.Equal(t, "KiB", downloading[0].Units)
	assert.Equal(t, "go", downloading[0].Package)
	assert.Equal(t, "v1.2.3", downloading[0].Version)
}

// TestEmitFinalizingEmitsFinalizingLine asserts the post-install finalizing
// checkpoint is emitted so the local notifier keeps the progress bar alive
// through the config/env phase.
func TestEmitFinalizingEmitsFinalizingLine(t *testing.T) {
	var sink strings.Builder
	emitFinalizing(&sink)
	p, ok := workspacessh.ParseProvisionProgressLine(
		[]byte(strings.TrimRight(sink.String(), "\n")))
	require.True(t, ok)
	assert.Equal(t, workspacessh.ProvisionPhaseFinalizing, p.Phase)
}
