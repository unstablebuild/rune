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
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunUpgradeLinux_ReportsExtractProgress(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()

	files := map[string]string{"rune.app/bin/rune": "#!/bin/sh\necho new\n"}
	rng := rand.New(rand.NewSource(1))
	for i := range 32 {
		buf := make([]byte, 8*1024)
		_, err := rng.Read(buf)
		require.NoError(t, err)
		files[fmt.Sprintf("rune.app/share/rune/blob-%02d", i)] = string(buf)
	}
	tarball := filepath.Join(cache, "rune-v0.42.1.tar.gz")
	writeReleaseTarGz(t, tarball, files)
	info, err := os.Stat(tarball)
	require.NoError(t, err)

	rec := &progressSampleRecorder{}
	opts := upgradeOpts{
		manifest:         Manifest{Version: "v0.42.1"},
		currentVersion:   "v0.42.0",
		cacheDir:         cache,
		installRoot:      root,
		appName:          "rune.app",
		cliBinaryRelPath: filepath.Join("bin", "rune"),
		backupRetention:  1,
		ops:              realLinuxOps{},
		pw:               rec,
	}
	require.NoError(t, runUpgradeLinux(context.Background(), opts, tarball))

	samples := rec.snapshot()
	var extractSamples []upgradeSample
	for _, s := range samples {
		if strings.HasSuffix(s.units, " extracted") {
			extractSamples = append(extractSamples, s)
		}
	}
	require.GreaterOrEqual(t, len(extractSamples), 2,
		"expected several extraction samples, got %+v", samples)
	require.Contains(t, rec.units(), "installing")

	scaled, scaledTotal, unit := scaleBytes(info.Size(), info.Size())
	require.Equal(t, scaledTotal, scaled)
	for i, s := range extractSamples {
		require.Equal(t, unit+" extracted", s.units)
		require.Equal(t, scaledTotal, s.total)
		require.Less(t, s.progress, s.total,
			"extraction must not report a boundary sample")
		if i > 0 {
			require.GreaterOrEqual(t, s.progress, extractSamples[i-1].progress,
				"extraction progress must be monotonic")
		}
	}
}

func TestRunUpgradeLinux_HappyPath(t *testing.T) {
	fake := &fakePlatformOps{
		archiveLayout: map[string]string{
			"bin/rune":          "#!/bin/sh\necho new\n",
			"share/rune/README": "hello",
		},
	}
	root := t.TempDir()
	cache := t.TempDir()
	fake.archiveContent = []byte("tarball-bytes")
	sum := sha256.Sum256(fake.archiveContent)

	opts := upgradeOpts{
		manifest: Manifest{
			Version:  "v0.42.1",
			Filename: "rune-v0.42.1.tar.gz",
			URL:      "https://example.invalid/rune-v0.42.1.tar.gz",
			SHA256:   hex.EncodeToString(sum[:]),
		},
		currentVersion:   "v0.42.0",
		cacheDir:         cache,
		installRoot:      root,
		appName:          "rune.app",
		cliSymlinkPath:   filepath.Join(root, "bin-cli", "rune"),
		cliBinaryRelPath: filepath.Join("bin", "rune"),
		backupRetention:  1,
		ops:              fake,
	}

	// Simulate an existing install.
	existing := filepath.Join(root, "rune.app", "bin", "rune")
	require.NoError(t, os.MkdirAll(filepath.Dir(existing), 0o755))
	require.NoError(t, os.WriteFile(existing, []byte("#!/bin/sh\necho old\n"), 0o755))

	err := runUpgradeLinux(context.Background(), opts, filepath.Join(cache, opts.manifest.Filename))
	// runUpgradeLinux is invoked directly because runUpgrade gates on
	// runtime.GOOS. The artifact path is derived the same way runUpgrade
	// would (cache + filename); make sure it exists for the verifier
	// path even though we bypass Download here.
	require.NoError(t, err)

	// Tarball is extracted into place.
	got, err := os.ReadFile(filepath.Join(root, "rune.app", "bin", "rune"))
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\necho new\n", string(got))

	// Symlink points at the new binary.
	target, err := os.Readlink(opts.cliSymlinkPath)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "rune.app", "bin", "rune"), target)

	// Backup of the old install was created.
	_, err = os.Stat(filepath.Join(root, "rune.app.bak-v0.42.0"))
	require.NoError(t, err)
}

// realLinuxOps drives runUpgradeLinux through the production code
// paths it actually uses on Linux (extraction, atomic rename, symlink
// refresh, cleanup), backed by real filesystem operations. The Linux
// upgrade is pure tar+os work, so it runs faithfully on any OS. The
// darwin-only methods are unreachable from runUpgradeLinux and panic
// to make accidental use obvious.
type realLinuxOps struct{}

func (realLinuxOps) Download(context.Context, string, string, func(int64, int64)) error {
	panic("Download not used by runUpgradeLinux test")
}
func (realLinuxOps) VerifySHA256(string, string) error { return nil }
func (realLinuxOps) MountDMG(context.Context, string) (string, func() error, error) {
	panic("MountDMG not used by runUpgradeLinux test")
}
func (realLinuxOps) AssessGatekeeper(context.Context, string) error {
	panic("AssessGatekeeper not used by runUpgradeLinux test")
}
func (realLinuxOps) VerifyCodesign(context.Context, string) error {
	panic("VerifyCodesign not used by runUpgradeLinux test")
}
func (realLinuxOps) Ditto(context.Context, string, string) error {
	panic("Ditto not used by runUpgradeLinux test")
}
func (realLinuxOps) ExtractTarGz(
	ctx context.Context, archivePath, destDir string,
	progress func(n, total int64),
) error {
	return extractTarGz(ctx, archivePath, destDir, progress)
}
func (realLinuxOps) Symlink(target, linkPath string) error { return replaceSymlink(target, linkPath) }
func (realLinuxOps) RenameAtomic(src, dst string) error    { return os.Rename(src, dst) }
func (realLinuxOps) RemoveAll(path string) error           { return os.RemoveAll(path) }
func (realLinuxOps) FreeSpace(string) (uint64, error)      { return 1 << 60, nil }

// writeReleaseTarGz writes a gzip-compressed tar to path whose entries
// are the given relpath->content map. Callers prefix the relpaths with
// the top-level directory (e.g. "rune.app/bin/rune") so the byte layout
// matches what `tar --format=ustar -czf rel.tar.gz rune.app` produces
// in cmd/rune/Makefile.
func writeReleaseTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		require.NoError(t, tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o755,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
}

func TestRunUpgradeLinux_RealTarballTopDir(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()

	const newBinary = "#!/bin/sh\necho new\n"
	tarball := filepath.Join(cache, "rune-v0.42.1.tar.gz")
	writeReleaseTarGz(t, tarball, map[string]string{
		"rune.app/bin/rune":                        newBinary,
		"rune.app/share/applications/rune.desktop": "[Desktop Entry]\nExec=rune\n",
		"rune.app/share/rune/README":               "hello",
	})

	opts := upgradeOpts{
		manifest: Manifest{
			Version:  "v0.42.1",
			Filename: "rune-v0.42.1.tar.gz",
			URL:      "https://example.invalid/rune-v0.42.1.tar.gz",
		},
		currentVersion:   "v0.42.0",
		cacheDir:         cache,
		installRoot:      root,
		appName:          "rune.app",
		cliSymlinkPath:   filepath.Join(root, "bin-cli", "rune"),
		cliBinaryRelPath: filepath.Join("bin", "rune"),
		backupRetention:  1,
		ops:              realLinuxOps{},
	}

	// Mirror install.sh's on-disk layout: an existing install plus a
	// CLI symlink that points into it (so the upgrade treats it as
	// owned and refreshes it).
	existing := filepath.Join(root, "rune.app", "bin", "rune")
	require.NoError(t, os.MkdirAll(filepath.Dir(existing), 0o755))
	require.NoError(t, os.WriteFile(existing, []byte("#!/bin/sh\necho old\n"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Dir(opts.cliSymlinkPath), 0o755))
	require.NoError(t, os.Symlink(existing, opts.cliSymlinkPath))

	require.NoError(t, runUpgradeLinux(context.Background(), opts, tarball))

	// The new binary must land at <install>/rune.app/bin/rune, never
	// nested under an extra rune.app/ directory.
	got, err := os.ReadFile(filepath.Join(root, "rune.app", "bin", "rune"))
	require.NoError(t, err)
	require.Equal(t, newBinary, string(got))

	_, err = os.Stat(filepath.Join(root, "rune.app", "rune.app"))
	require.True(t, os.IsNotExist(err),
		"upgrade must not produce a nested rune.app/rune.app directory")

	// No staging wrapper is left behind.
	_, err = os.Stat(filepath.Join(root, "rune.app.new"))
	require.True(t, os.IsNotExist(err), "staging dir must be cleaned up")

	// The CLI symlink (and, equivalently, the .desktop Exec= absolute
	// path) resolves to the real new binary.
	resolved, err := filepath.EvalSymlinks(opts.cliSymlinkPath)
	require.NoError(t, err)
	wantResolved, err := filepath.EvalSymlinks(filepath.Join(root, "rune.app", "bin", "rune"))
	require.NoError(t, err)
	require.Equal(t, wantResolved, resolved)

	// The previous install was snapshotted as a backup.
	_, err = os.Stat(filepath.Join(root, "rune.app.bak-v0.42.0"))
	require.NoError(t, err)
}

func TestRunUpgradeLinux_StagedDirNameMismatch(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()

	const newBinary = "#!/bin/sh\necho new\n"
	tarball := filepath.Join(cache, "rune-v1.0.4.tar.gz")
	writeReleaseTarGz(t, tarball, map[string]string{
		"rune.app/bin/rune":          newBinary,
		"rune.app/share/rune/README": "hello",
	})

	const appName = "rune.app.bak-v1.0.3"
	opts := upgradeOpts{
		manifest: Manifest{
			Version:  "v1.0.4",
			Filename: "rune-v1.0.4.tar.gz",
			URL:      "https://example.invalid/rune-v1.0.4.tar.gz",
		},
		currentVersion:   "v1.0.3",
		cacheDir:         cache,
		installRoot:      root,
		appName:          appName,
		cliSymlinkPath:   filepath.Join(root, "bin-cli", "rune"),
		cliBinaryRelPath: filepath.Join("bin", "rune"),
		backupRetention:  1,
		ops:              realLinuxOps{},
	}

	existing := filepath.Join(root, appName, "bin", "rune")
	require.NoError(t, os.MkdirAll(filepath.Dir(existing), 0o755))
	require.NoError(t, os.WriteFile(existing, []byte("#!/bin/sh\necho old\n"), 0o755))

	require.NoError(t, runUpgradeLinux(context.Background(), opts, tarball))

	got, err := os.ReadFile(filepath.Join(root, appName, "bin", "rune"))
	require.NoError(t, err)
	require.Equal(t, newBinary, string(got))

	_, err = os.Stat(filepath.Join(root, appName, "rune.app"))
	require.True(t, os.IsNotExist(err),
		"upgrade must not bury the bundle under a nested rune.app directory")

	_, err = os.Stat(filepath.Join(root, appName+".new"))
	require.True(t, os.IsNotExist(err), "staging dir must be cleaned up")
}

func TestRunUpgradeLinux_RenameFailureRollsBack(t *testing.T) {
	root := t.TempDir()
	cache := t.TempDir()
	fake := &fakePlatformOps{
		archiveLayout: map[string]string{"bin/rune": "new"},
		renameErr: map[string]error{
			// Fail when we try to swap the staging dir into place.
			filepath.Join(root, "rune.app"): errors.New("cross-device"),
		},
		renameOnce: true,
	}
	fake.archiveContent = []byte("t")
	sum := sha256.Sum256(fake.archiveContent)

	opts := upgradeOpts{
		manifest: Manifest{
			Version:  "v2",
			Filename: "rune.tar.gz",
			URL:      "https://example.invalid/rune.tar.gz",
			SHA256:   hex.EncodeToString(sum[:]),
		},
		currentVersion:   "v1",
		cacheDir:         cache,
		installRoot:      root,
		appName:          "rune.app",
		cliSymlinkPath:   filepath.Join(root, "cli", "rune"),
		cliBinaryRelPath: filepath.Join("bin", "rune"),
		backupRetention:  1,
		ops:              fake,
	}

	existing := filepath.Join(root, "rune.app", "bin", "rune")
	require.NoError(t, os.MkdirAll(filepath.Dir(existing), 0o755))
	require.NoError(t, os.WriteFile(existing, []byte("old"), 0o755))

	err := runUpgradeLinux(context.Background(), opts, filepath.Join(cache, opts.manifest.Filename))
	require.Error(t, err)

	// Old install is restored from the backup.
	got, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "old", string(got))
}
