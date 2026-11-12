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

package ideupgrade

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
)

// upgradeOpts is the per-call data needed by runUpgrade. Decoupling
// from Manager keeps runUpgrade testable in isolation against a
// fakePlatformOps with no IDE/storage dependencies.
type upgradeOpts struct {
	manifest         Manifest
	currentVersion   string
	cacheDir         string
	installRoot      string
	appName          string
	cliSymlinkPath   string
	cliBinaryRelPath string
	backupRetention  int
	ops              platformOps
	// pw receives phase markers and byte-transfer samples for the
	// whole upgrade. It may be nil, in which case progress reporting
	// is skipped.
	pw repl.ProgressWriter
}

// progress forwards a sample to opts.pw when one is configured.
func (o upgradeOpts) progress(n, total int64, units string) {
	if o.pw == nil {
		return
	}
	o.pw.Progress(n, total, units)
}

// byteSampler returns the byte-transfer callback handed to the
// platform ops for verb ("downloaded", "extracted"). Samples are
// scaled to a human-readable unit. The boundary sample is swallowed so
// the host progress UI stays alive for the phases that follow:
// reaching total is how a progress display learns work is over.
func (o upgradeOpts) byteSampler(verb string) func(n, total int64) {
	if o.pw == nil {
		return nil
	}
	return func(n, total int64) {
		if total <= 0 || n >= total {
			return
		}
		scaledN, scaledTotal, unit := scaleBytes(n, total)
		o.pw.Progress(scaledN, scaledTotal, unit+" "+verb)
	}
}

// runUpgrade is the OS-agnostic orchestration of a manifest-driven
// upgrade. It downloads the artifact, verifies it, replaces the
// installed app bundle (with backup + rollback on failure), refreshes
// the CLI symlink, and trims old backups.
//
// runUpgrade is split out from Manager so it can be exercised end to
// end against a fakePlatformOps without spinning up an IDE.
func runUpgrade(ctx context.Context, opts upgradeOpts) error {
	if opts.ops == nil {
		return errors.New("ideupgrade: platformOps is nil")
	}
	if opts.manifest.URL == "" || opts.manifest.SHA256 == "" {
		return errors.New("ideupgrade: manifest missing url or sha256")
	}

	if err := os.MkdirAll(opts.cacheDir, 0o755); err != nil {
		return fmt.Errorf("mkdir cache: %w", err)
	}

	if err := preflightFreeSpace(opts); err != nil {
		return err
	}

	// Pick a destination filename based on the manifest filename, falling
	// back to the URL basename. Either way we keep it inside cacheDir.
	artifactName := opts.manifest.Filename
	if artifactName == "" {
		artifactName = path.Base(opts.manifest.URL)
	}
	if artifactName == "" || artifactName == "/" {
		return errors.New("ideupgrade: cannot derive artifact filename")
	}
	artifactPath := filepath.Join(opts.cacheDir, artifactName)

	if err := opts.ops.Download(
		ctx, opts.manifest.URL, artifactPath, opts.byteSampler("downloaded"),
	); err != nil {
		return fmt.Errorf("download artifact: %w", err)
	}
	defer func() { _ = opts.ops.RemoveAll(artifactPath) }()

	opts.progress(0, 1, "verifying download")
	if err := opts.ops.VerifySHA256(artifactPath, opts.manifest.SHA256); err != nil {
		return fmt.Errorf("verify sha256: %w", err)
	}

	var err error
	switch runtime.GOOS {
	case "darwin":
		err = runUpgradeDarwin(ctx, opts, artifactPath)
	case "linux":
		err = runUpgradeLinux(ctx, opts, artifactPath)
	default:
		return fmt.Errorf("ideupgrade: unsupported os %q", runtime.GOOS)
	}
	if err != nil {
		return err
	}
	opts.progress(1, 1, "done")
	return nil
}

// scaleBytes picks a human-readable byte unit based on total and
// returns progress/total scaled to that unit. The unit is picked
// from total so it stays stable across successive samples.
func scaleBytes(progress, total int64) (int64, int64, string) {
	const (
		kib = 1024
		mib = kib * 1024
		gib = mib * 1024
		tib = gib * 1024
	)
	switch {
	case total >= tib:
		return progress / tib, total / tib, "TiB"
	case total >= gib:
		return progress / gib, total / gib, "GiB"
	case total >= mib:
		return progress / mib, total / mib, "MiB"
	case total >= kib:
		return progress / kib, total / kib, "KiB"
	default:
		return progress, total, "B"
	}
}

// preflightHeadroomExtra is the constant slack we add to the
// space-required estimate. It absorbs filesystem overhead (HFS+/APFS
// block padding, ditto's temporary files, hdiutil's mount-point
// staging) so the check is conservative without being noisy.
const preflightHeadroomExtra = 200 * 1024 * 1024

// preflightFreeSpace fails fast when there is not enough free space
// on either the cache filesystem (DMG/tarball download lands here)
// or the install-root filesystem (new bundle + backup live here
// simultaneously until pruneBackups runs). Skipped when the manifest
// does not advertise Size — without it we can't bound the requirement.
func preflightFreeSpace(opts upgradeOpts) error {
	if opts.manifest.Size <= 0 {
		return nil
	}
	required := uint64(opts.manifest.Size)*2 + preflightHeadroomExtra
	for _, p := range []string{opts.cacheDir, opts.installRoot} {
		free, err := opts.ops.FreeSpace(p)
		if err != nil {
			return fmt.Errorf("check free space at %s: %w", p, err)
		}
		if free < required {
			return fmt.Errorf(
				"insufficient free space at %s: need %d MiB, have %d MiB",
				p, required/(1024*1024), free/(1024*1024))
		}
	}
	return nil
}

// runUpgradeDarwin is the macOS-specific orchestration: mount DMG,
// assess Gatekeeper, snapshot the existing app, ditto the new bundle
// in, re-verify the installed bundle, refresh symlink, retire old
// backups, detach the DMG.
func runUpgradeDarwin(ctx context.Context, opts upgradeOpts, dmgPath string) error {
	opts.progress(0, 1, "mounting image")
	mountpoint, detach, err := opts.ops.MountDMG(ctx, dmgPath)
	if err != nil {
		return fmt.Errorf("mount dmg: %w", err)
	}
	defer func() {
		if detach != nil {
			_ = detach()
		}
	}()

	srcApp, err := findAppBundle(mountpoint)
	if err != nil {
		return fmt.Errorf("find .app in dmg: %w", err)
	}

	opts.progress(0, 1, "verifying image")
	if err := opts.ops.AssessGatekeeper(ctx, srcApp); err != nil {
		return fmt.Errorf("gatekeeper assess: %w", err)
	}

	dstApp := filepath.Join(opts.installRoot, opts.appName)
	backup := backupPath(dstApp, opts.currentVersion)

	hasExisting := pathExists(dstApp)
	symlinkOwned := shouldRefreshCLISymlink(opts.cliSymlinkPath, dstApp)
	if hasExisting {
		if err := removeStaleBackup(opts, backup); err != nil {
			return err
		}
		if err := opts.ops.RenameAtomic(dstApp, backup); err != nil {
			return fmt.Errorf("snapshot existing app: %w", err)
		}
	}

	opts.progress(0, 1, "installing")
	if err := opts.ops.Ditto(ctx, srcApp, dstApp); err != nil {
		if rbErr := rollbackDarwin(opts, dstApp, backup, hasExisting); rbErr != nil {
			return fmt.Errorf("ditto failed: %w; rollback failed: %v", err, rbErr)
		}
		return fmt.Errorf("ditto: %w", err)
	}

	// Post-install verification: catches mutations to the installed
	// bundle that would prevent macOS from launching it (e.g. an
	// async AV scanner rewriting xattrs after ditto, or a stale
	// .zcompdump-style file inside the bundle). spctl re-checks the
	// Gatekeeper policy at the on-disk path; codesign --verify
	// checks the seal integrity of every signed component.
	opts.progress(0, 1, "verifying install")
	if err := opts.ops.AssessGatekeeper(ctx, dstApp); err != nil {
		if rbErr := rollbackDarwin(opts, dstApp, backup, hasExisting); rbErr != nil {
			return fmt.Errorf("post-install gatekeeper assess failed: %w; rollback failed: %v", err, rbErr)
		}
		return fmt.Errorf("post-install gatekeeper assess: %w", err)
	}
	if err := opts.ops.VerifyCodesign(ctx, dstApp); err != nil {
		if rbErr := rollbackDarwin(opts, dstApp, backup, hasExisting); rbErr != nil {
			return fmt.Errorf("post-install codesign verify failed: %w; rollback failed: %v", err, rbErr)
		}
		return fmt.Errorf("post-install codesign verify: %w", err)
	}

	if err := refreshCLISymlink(opts, dstApp, symlinkOwned); err != nil {
		return err
	}

	pruneBackups(opts, dstApp)
	return nil
}

// rollbackDarwin restores the previously snapshotted bundle. It is
// shared by every failure path after the rename-into-backup step so
// the error wrapping at the call site stays focused on the *cause*
// rather than the recovery mechanics.
func rollbackDarwin(opts upgradeOpts, dstApp, backup string, hasExisting bool) error {
	_ = opts.ops.RemoveAll(dstApp)
	if !hasExisting {
		return nil
	}
	return opts.ops.RenameAtomic(backup, dstApp)
}

// runUpgradeLinux is the linux-specific orchestration: extract
// tarball, atomically swap install dirs, refresh symlink, retire old
// backups.
func runUpgradeLinux(ctx context.Context, opts upgradeOpts, archivePath string) error {
	dstApp := filepath.Join(opts.installRoot, opts.appName)
	stagingDir := dstApp + ".new"
	backup := backupPath(dstApp, opts.currentVersion)

	_ = opts.ops.RemoveAll(stagingDir)
	if err := opts.ops.ExtractTarGz(
		ctx, archivePath, stagingDir, opts.byteSampler("extracted"),
	); err != nil {
		return fmt.Errorf("extract tarball: %w", err)
	}

	// The release tarball's single top-level directory is the app
	// dir itself (`tar ... rune.app`), so extraction yields
	// `<staging>/rune.app/bin/rune`. Swapping the staging dir straight
	// into place would bury the binary one level too deep
	// (`<install>/rune.app/rune.app/bin/rune`), leaving the CLI
	// symlink and the XDG .desktop Exec= path dangling. Unwrap the
	// nested app dir so the swap source has the expected layout.
	swapSrc, err := unwrapStagedApp(stagingDir)
	if err != nil {
		_ = opts.ops.RemoveAll(stagingDir)
		return fmt.Errorf("locate extracted app: %w", err)
	}

	hasExisting := pathExists(dstApp)
	if hasExisting {
		if err := removeStaleBackup(opts, backup); err != nil {
			_ = opts.ops.RemoveAll(stagingDir)
			return err
		}
		if err := opts.ops.RenameAtomic(dstApp, backup); err != nil {
			_ = opts.ops.RemoveAll(stagingDir)
			return fmt.Errorf("snapshot existing app: %w", err)
		}
	}

	symlinkOwned := shouldRefreshCLISymlink(opts.cliSymlinkPath, dstApp)
	opts.progress(0, 1, "installing")
	if err := opts.ops.RenameAtomic(swapSrc, dstApp); err != nil {
		// Restore previous app bundle.
		_ = opts.ops.RemoveAll(stagingDir)
		if hasExisting {
			if rbErr := opts.ops.RenameAtomic(backup, dstApp); rbErr != nil {
				return fmt.Errorf("install rename failed: %w; rollback failed: %v", err, rbErr)
			}
		}
		return fmt.Errorf("rename staging into place: %w", err)
	}
	// When swapSrc was the nested app dir, the now-empty staging
	// wrapper is left behind; remove it.
	if swapSrc != stagingDir {
		_ = opts.ops.RemoveAll(stagingDir)
	}

	if err := refreshCLISymlink(opts, dstApp, symlinkOwned); err != nil {
		return err
	}

	pruneBackups(opts, dstApp)
	return nil
}

// backupPath returns the path used to snapshot the previously
// installed app bundle before an upgrade. Format:
//
//	<installRoot>/<appName>.bak-<currentVersion>
//
// When currentVersion is empty (development build) we still use a
// stable suffix so the rollback path is deterministic.
func backupPath(installPath, currentVersion string) string {
	suffix := currentVersion
	if suffix == "" {
		suffix = "previous"
	}
	return installPath + ".bak-" + suffix
}

// removeStaleBackup deletes a backup left at backup by an earlier
// upgrade from the same version, which would make snapshotting the
// existing app fail. pruneBackups would delete it after this upgrade
// anyway.
func removeStaleBackup(opts upgradeOpts, backup string) error {
	if err := opts.ops.RemoveAll(backup); err != nil {
		return fmt.Errorf("remove stale backup: %w", err)
	}
	return nil
}

// pruneBackups deletes all but the most recent BackupRetention
// backup snapshots produced by previous upgrades. The most recent
// backup (just created) is always retained.
func pruneBackups(opts upgradeOpts, installPath string) {
	if opts.backupRetention <= 0 {
		opts.backupRetention = 1
	}
	dir := filepath.Dir(installPath)
	prefix := filepath.Base(installPath) + ".bak-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type backup struct {
		path    string
		modTime int64
	}
	var backups []backup
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backup{
			path:    filepath.Join(dir, name),
			modTime: info.ModTime().UnixNano(),
		})
	}
	if len(backups) <= opts.backupRetention {
		return
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].modTime > backups[j].modTime
	})
	for _, b := range backups[opts.backupRetention:] {
		_ = opts.ops.RemoveAll(b.path)
	}
}

// findAppBundle returns the absolute path of the first .app directory
// found at the top level of root.
func findAppBundle(root string) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", root, err)
	}
	for _, e := range entries {
		if e.IsDir() && strings.HasSuffix(e.Name(), ".app") {
			return filepath.Join(root, e.Name()), nil
		}
	}
	return "", fmt.Errorf("no .app bundle in %s", root)
}

func pathExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// unwrapStagedApp returns the directory inside stagingDir that should
// be swapped into place. The Linux release tarball wraps everything in
// a single top-level directory (e.g. `rune.app/`), so after extraction
// the real bundle lives at `<staging>/<dir>/bin/...`. That nested dir
// is returned so the swap lands the bundle at `<install>/<appName>`
// rather than `<install>/<appName>/<dir>`.
//
// The nested directory is located by its `bin/` layout, not by name:
// when the running binary was launched from a leftover backup (e.g.
// after a double upgrade without restarting), the detected appName is
// the backup directory name (`rune.app.bak-...`) and will not match the
// tarball's `rune.app` top-level dir.
//
// If no wrapper is present (an already-flat layout), stagingDir itself
// is returned unchanged.
func unwrapStagedApp(stagingDir string) (string, error) {
	if info, err := os.Stat(filepath.Join(stagingDir, "bin")); err == nil && info.IsDir() {
		return stagingDir, nil
	}
	entries, err := os.ReadDir(stagingDir)
	if err != nil {
		return "", fmt.Errorf("read staging dir %s: %w", stagingDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		nested := filepath.Join(stagingDir, e.Name())
		if bin, err := os.Stat(filepath.Join(nested, "bin")); err == nil && bin.IsDir() {
			return nested, nil
		}
	}
	return "", fmt.Errorf("no bin/ directory under %s or any of its top-level subdirectories", stagingDir)
}

// shouldRefreshCLISymlink decides whether refreshCLISymlink should
// rewrite opts.cliSymlinkPath. The rule is:
//
//   - if the symlink does not exist → refresh (create).
//   - if the symlink resolves into the install path (oldApp) →
//     refresh (the user installed it via the standard layout, we
//     own it).
//   - otherwise → leave it alone (user has their own arrangement).
//
// We resolve the symlink before the in-place replace so we can
// observe the *old* target; after the swap, the symlink would
// resolve into the new bundle and the heuristic would no longer
// distinguish "ours" from "user's".
func shouldRefreshCLISymlink(symlinkPath, oldApp string) bool {
	if symlinkPath == "" {
		return false
	}
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		// Either it doesn't exist (create) or it isn't a symlink
		// (treat as user-owned and leave it).
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		return false
	}
	resolved, err := filepath.EvalSymlinks(symlinkPath)
	if err != nil {
		// Dangling symlink; previous target likely lived inside the
		// old install but is now gone. Use the raw link target.
		resolved = target
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(filepath.Dir(symlinkPath), resolved)
		}
	}
	resolved = filepath.Clean(resolved)
	oldApp = filepath.Clean(oldApp)
	rel, err := filepath.Rel(oldApp, resolved)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rel, "..") && rel != "."
}

// refreshCLISymlink updates opts.cliSymlinkPath to point at the new
// binary inside dstApp when owned is true. owned is precomputed by
// shouldRefreshCLISymlink *before* the in-place replace so the
// "points into old install" check still works.
//
// A symlink-only failure is non-fatal: the bundle is already in
// place and we don't want to roll the whole upgrade back over a
// symlink that we couldn't write. The outcome is surfaced as a
// progress phase and logged, and the upgrade proceeds.
func refreshCLISymlink(opts upgradeOpts, dstApp string, owned bool) error {
	if !owned {
		if opts.cliSymlinkPath != "" {
			opts.progress(0, 1, fmt.Sprintf(
				"left existing CLI symlink at %s untouched",
				opts.cliSymlinkPath))
		}
		return nil
	}
	target := filepath.Join(dstApp, opts.cliBinaryRelPath)
	if err := opts.ops.Symlink(target, opts.cliSymlinkPath); err != nil {
		log.WithError(err).Warnf(
			"ideupgrade: refresh CLI symlink at %s", opts.cliSymlinkPath)
		opts.progress(0, 1, fmt.Sprintf(
			"failed to refresh CLI symlink at %s", opts.cliSymlinkPath))
		return nil
	}
	return nil
}
