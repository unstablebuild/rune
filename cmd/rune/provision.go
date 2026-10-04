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
	"context"
	"errors"
	"io"
	"net/http"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/blue/release/cdnrelease"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/gitpkg"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/multipkg"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacessh"
)

// provisionRemote runs on the remote `rune -x` server before it starts
// serving. It mirrors the local toolchain by installing the packages named in
// the --install manifest into the remote ~/.rune, then loads the remote config
// (now including any gui.env block merged by those installs, plus the
// workspace-root .rune/config.yaml overlay) and applies gui.env and the
// ~/.rune/bin PATH entry to this process so LSP/tool/debug children inherit a
// working toolchain.
//
// Failure policy: provisioning failures must never abort the ssh connection.
// Every fallible step warns and continues.
func provisionRemote(scheme schemeapi.Scheme, uri workspaceapi.URI) {
	installRemotePackages(scheme)
	// The post-install phase (config overlay fetch over SSH, gui.env, PATH)
	// runs with no other progress source, so emit a finalizing checkpoint to
	// keep the progress bar alive through it. The bar is closed by the local
	// side only when the ServerReady sentinel arrives, not here.
	emitFinalizing(os.Stderr)
	loadRemoteConfigAndApplyEnv(scheme, uri)
}

// installRemotePackages installs and activates every package in the --install
// manifest. Ordering matters: installs must complete before the config is
// loaded, because a package's gui.env block is merged into the remote config
// during UsePackageVersion.
//
// Progress is streamed as JSON Lines to os.Stderr: provisioning runs before
// StartSchemeServer, so stderr is still a plain pipe here and the local side
// (workspacessh) forwards each line to a browser notification.
func installRemotePackages(scheme schemeapi.Scheme) {
	installRemotePackagesTo(scheme, os.Stderr)
}

func installRemotePackagesTo(scheme schemeapi.Scheme, progress io.Writer) {
	entries := idepkg.ParseProvisionManifest(*flagWorkspaceServerInstall)
	if len(entries) == 0 {
		return
	}
	installRemotePackageEntries(scheme, progress, entries, newRemoteReleaseManager())
}

func newRemoteReleaseManager(gitOpts ...gitpkg.Option) release.Manager {
	// Release downloads are unauthenticated, matching newAPIClient: the
	// oauth transport fails client-side for logged-out users, which would
	// break installs under the usage-based paywall.
	official := cdnrelease.NewManager(
		&http.Client{},
		idepkg.ReleasesURL(*flagHTTPAddress, idepkg.HostArch()))
	return multipkg.New(gitpkg.New(gitOpts...), official)
}

func installRemotePackageEntries(
	scheme schemeapi.Scheme, progress io.Writer, entries []idepkg.ProvisionEntry,
	releaseManager release.Manager,
) {
	configBase := func() map[string]any {
		tree, err := ide.DefaultConfigTree(runeDefaultConfig())
		if err != nil {
			log.Warnf("provision: decode default config tree: %v", err)
			return nil
		}
		return tree
	}

	// Resolve the user's editor mode before installing so package config.star
	// scripts that read RUNE_EDITOR_MODE (e.g. mode-aware key bindings) decode
	// instead of failing with "undefined: RUNE_EDITOR_MODE". The mode is a user
	// setting independent of any package, so it is safe to read here, before the
	// installs that merge package gui.env into the config.
	editorMode := resolveRemoteEditorMode()

	trust := pkgtrust.NewStore(*flagDataPath, trustKeyringFetcher())
	mgr, storage := idepkg.NewProvisioningManager(
		newRuneStorage(*flagDataPath), releaseManager, scheme,
		*flagDataPath, *flagConfigPath, editorMode, configBase, trust)
	defer func() { _ = storage.Close() }()

	ctx := context.Background()
	if err := mgr.Reconcile(ctx); err != nil {
		log.Warnf("provision: reconcile remote packages: %v", err)
	}
	total := len(entries)
	// Installs run sequentially: each package's gui.env is merged into the
	// same user config during activation, and concurrent read-modify-write
	// of that shared file loses env keys. Serializing keeps the merge
	// correct without a lock.
	for i, e := range entries {
		installOnePackage(ctx, mgr, progress, e, i+1, total)
	}
	emitProvisionProgress(progress, workspacessh.ProvisionProgress{
		Index: total, Total: total, Phase: workspacessh.ProvisionPhaseDone,
	})
}

// emitFinalizing emits a single finalizing checkpoint so the post-install
// config/env phase remains visible on the progress bar. Index/Total are left
// zero: the local notifier maps the finalizing phase to a fixed fraction and
// does not need per-package indices here.
func emitFinalizing(w io.Writer) {
	emitProvisionProgress(w, workspacessh.ProvisionProgress{
		Phase: workspacessh.ProvisionPhaseFinalizing,
	})
}

// remoteInstaller is the slice of the provisioning manager installOnePackage
// needs, kept small so the version-fallback logic can be tested without
// network or storage.
type remoteInstaller interface {
	InstallPackageVersion(ctx context.Context, id string, version release.Version, pw repl.ProgressWriter) error
	UsePackageVersion(ctx context.Context, id string, version release.Version) error
	LatestVersion(ctx context.Context, id string) (release.Version, error)
}

// installOnePackage installs and activates a single manifest entry, emitting
// progress. When the pinned version cannot be installed (e.g. it is not
// published for the remote's platform, or the package/version 404s), it
// degrades to the latest available version so the remote toolchain still comes
// up rather than failing on an arch-specific version gap. A failed-install
// warning is only surfaced when the latest version is also unavailable, so a
// recoverable pinned-version gap never produces a spurious notification.
// Returns whether the package was installed.
func installOnePackage(
	ctx context.Context, inst remoteInstaller, progress io.Writer,
	e idepkg.ProvisionEntry, index, total int,
) bool {
	emitProvisionProgress(progress, workspacessh.ProvisionProgress{
		Index: index, Total: total, Package: e.ID, Version: e.Version,
		Phase: workspacessh.ProvisionPhaseInstalling,
	})

	version := release.Version(e.Version)
	pw := newDownloadProgressWriter(progress, e.ID, string(version), index, total)
	err := inst.InstallPackageVersion(ctx, e.ID, version, pw)
	// A fully-installed package is an idempotent no-op: fall through to
	// activation instead of reporting a failure, so re-provisioning never
	// surfaces a spurious "already installed" warning.
	if err != nil && errors.Is(err, idepkg.ErrAlreadyInstalled) {
		err = nil
	}
	if err != nil {
		// The pinned version may not be installable on the remote (a
		// platform/arch gap, a 404 for the package or version, etc.). Rather
		// than warn, transparently degrade to the latest available version so
		// the remote toolchain still comes up. The pinned-version failure is
		// only logged at debug level, so a recoverable gap never produces a
		// user-facing warning.
		if latest, lerr := inst.LatestVersion(ctx, e.ID); lerr != nil {
			log.Warnf("provision: resolve latest %s (pinned %s failed: %v): %v",
				e.ID, e.Version, err, lerr)
		} else {
			log.Debugf("provision: %s@%s not installable (%v); installing latest %s",
				e.ID, e.Version, err, latest)
			version = latest
			pw = newDownloadProgressWriter(progress, e.ID, string(version), index, total)
			err = inst.InstallPackageVersion(ctx, e.ID, version, pw)
			if err != nil && errors.Is(err, idepkg.ErrAlreadyInstalled) {
				err = nil
			}
		}
	}
	if err != nil {
		log.Warnf("provision: install %s@%s: %v", e.ID, version, err)
		emitProvisionProgress(progress, workspacessh.ProvisionProgress{
			Index: index, Total: total, Package: e.ID, Version: string(version),
			Phase: workspacessh.ProvisionPhaseFailed, Detail: err.Error(),
		})
		return false
	}
	if err := inst.UsePackageVersion(ctx, e.ID, version); err != nil {
		log.Warnf("provision: use %s@%s: %v", e.ID, version, err)
	}
	emitProvisionProgress(progress, workspacessh.ProvisionProgress{
		Index: index, Total: total, Package: e.ID, Version: string(version),
		Phase: workspacessh.ProvisionPhaseActivating,
	})
	return true
}

// emitProvisionProgress writes one JSON-Lines progress record. Emission is
// best-effort: it never changes the warn-and-continue provisioning policy, so
// marshal and write errors are ignored.
func emitProvisionProgress(w io.Writer, p workspacessh.ProvisionProgress) {
	line, err := workspacessh.EncodeProvisionProgress(p)
	if err != nil {
		return
	}
	_, _ = io.WriteString(w, line)
}

// downloadProgressWriter adapts the release manager's byte-level ProgressWriter
// onto the JSON-Lines provisioning stream, emitting downloading records for the
// in-flight package. Byte counts are scaled to KiB and emission is throttled to
// only fire when the human-scaled value changes, so a large download cannot
// flood the SSH stderr back-channel.
type downloadProgressWriter struct {
	w       io.Writer
	id      string
	version string
	index   int
	total   int
	lastKiB int64
	emitted bool
}

func newDownloadProgressWriter(
	w io.Writer, id, version string, index, total int,
) *downloadProgressWriter {
	return &downloadProgressWriter{
		w: w, id: id, version: version, index: index, total: total,
	}
}

// Progress implements repl.ProgressWriter.
func (d *downloadProgressWriter) Progress(progress, total int64, _ string) {
	const kib = 1024
	doneKiB := progress / kib
	ofKiB := total / kib
	if d.emitted && doneKiB == d.lastKiB {
		return
	}
	d.lastKiB = doneKiB
	d.emitted = true
	emitProvisionProgress(d.w, workspacessh.ProvisionProgress{
		Index: d.index, Total: d.total, Package: d.id, Version: d.version,
		Phase: workspacessh.ProvisionPhaseDownloading,
		Done:  int(doneKiB), Of: int(ofKiB), Units: "KiB",
	})
}

// loadRemoteConfigAndApplyEnv loads the remote ~/.rune config overlaid with
// the workspace-root .rune/config.yaml, applies its gui.env block to this
// process, and prepends ~/.rune/bin to PATH. On any error it warns and
// continues so serving is never blocked.
func loadRemoteConfigAndApplyEnv(scheme schemeapi.Scheme, uri workspaceapi.URI) {
	cwd := workspace.NewSchemeWorkspace(uri, scheme,
		func(fn func()) bool { fn(); return true })
	rootCfg, err := ide.ConfigWithOverlays(
		*flagConfigPath, runeDefaultConfig(), cwd,
		[]string{workspaceConfigFilename},
	)
	if err != nil {
		log.Warnf("provision: load remote config: %v", err)
		if rootCfg == nil {
			rootCfg = config.NopConfig()
		}
	}

	guiCfg, ok, err := getGUIConfig(rootCfg)
	if err != nil {
		log.Warnf("provision: read gui config: %v", err)
	} else if ok {
		if env, err := getGUIEnvVars(guiCfg); err != nil {
			log.Warnf("provision: read gui.env: %v", err)
		} else if err := applyGUIEnvVars(env); err != nil {
			log.Warnf("provision: apply gui.env: %v", err)
		}
	}

	if err := setupRuneBinPATH(*flagDataPath); err != nil {
		log.Warnf("provision: set ~/.rune/bin on PATH: %v", err)
	}
}

// resolveRemoteEditorMode resolves the user's editor mode from the remote
// ~/.rune config so it can be exposed to package config.star scripts as
// RUNE_EDITOR_MODE during install. It reads only the base config, not the
// workspace-root overlay: editor.mode is a user/home setting and reading it
// here (before installs merge package gui.env) avoids a second overlay fetch
// over SSH, which loadRemoteConfigAndApplyEnv already performs post-install. On
// any load error it warns and returns the modal default (via ide.PkgEditorMode),
// so the mode is always concrete and provisioning is never aborted.
func resolveRemoteEditorMode() string {
	cfg, err := ide.Config(*flagConfigPath, runeDefaultConfig())
	if err != nil {
		log.Warnf("provision: resolve editor mode: %v", err)
	}
	return ide.PkgEditorMode(cfg)
}
