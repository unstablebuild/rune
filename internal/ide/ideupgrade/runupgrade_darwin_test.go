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

//go:build darwin

package ideupgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// newDarwinFixture returns a fake DMG payload (a single Rune.app
// containing Contents/MacOS/rune) plus the upgradeOpts that drive a
// darwin-flavoured upgrade against a temp install root.
func newDarwinFixture(t *testing.T, fake *fakePlatformOps) (upgradeOpts, string) {
	t.Helper()
	root := t.TempDir()
	cache := t.TempDir()

	const newBinary = "#!/bin/sh\necho new\n"
	fake.dmgPayload = map[string]string{
		"Contents/MacOS/rune": newBinary,
	}
	// Anything works for the artifact bytes; sha256 is verified against this.
	fake.archiveContent = []byte("dmg-bytes")
	sum := sha256.Sum256(fake.archiveContent)

	manifest := Manifest{
		Version:  "v9.9.9",
		Filename: "Rune-v9.9.9.dmg",
		URL:      "https://example.invalid/Rune-v9.9.9.dmg",
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     int64(len(fake.archiveContent)),
	}
	opts := upgradeOpts{
		manifest:         manifest,
		currentVersion:   "v9.9.0",
		cacheDir:         cache,
		installRoot:      root,
		appName:          "Rune.app",
		cliSymlinkPath:   filepath.Join(root, "bin", "rune"),
		cliBinaryRelPath: filepath.Join("Contents", "MacOS", "rune"),
		backupRetention:  1,
		ops:              fake,
	}
	return opts, root
}

// writeExistingApp materializes an existing Rune.app bundle at
// installRoot populated with the given binary content.
func writeExistingApp(t *testing.T, installRoot, content string) {
	t.Helper()
	app := filepath.Join(installRoot, "Rune.app", "Contents", "MacOS", "rune")
	require.NoError(t, os.MkdirAll(filepath.Dir(app), 0o755))
	require.NoError(t, os.WriteFile(app, []byte(content), 0o755))
}

func countCalls(seq []string, name string) int {
	n := 0
	for _, c := range seq {
		if c == name {
			n++
		}
	}
	return n
}

func TestRunUpgradeDarwin_HappyPath(t *testing.T) {
	fake := &fakePlatformOps{}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "#!/bin/sh\necho old\n")

	err := runUpgrade(context.Background(), opts)
	require.NoError(t, err)

	// New binary is in place.
	got, err := os.ReadFile(filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"))
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\necho new\n", string(got))

	// Backup of the old version was created.
	backup := filepath.Join(root, "Rune.app.bak-v9.9.0")
	_, err = os.Stat(backup)
	require.NoError(t, err)

	// Symlink resolves to the new CLI binary.
	target, err := os.Readlink(opts.cliSymlinkPath)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"), target)

	// Call sequence includes the expected steps.
	seq := fake.callSeq()
	wantSeq := []string{
		"Download:" + opts.manifest.URL,
		"VerifySHA256",
		"MountDMG",
		"AssessGatekeeper",
		"Rename:Rune.app->Rune.app.bak-v9.9.0",
		"Ditto",
		"VerifyCodesign",
		"Symlink",
	}
	for _, want := range wantSeq {
		require.Contains(t, seq, want, "missing call %q in sequence %v", want, seq)
	}
	// AssessGatekeeper runs once before the swap and once after.
	require.Equal(t, 2, countCalls(seq, "AssessGatekeeper"))
	require.Equal(t, 1, countCalls(seq, "VerifyCodesign"))
	// Detach is called via deferred runUpgradeDarwin cleanup.
	require.Contains(t, seq, "Detach")
}

func TestRunUpgradeDarwin_SHA256Mismatch(t *testing.T) {
	fake := &fakePlatformOps{}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "old")
	opts.manifest.SHA256 = strings.Repeat("0", 64)

	err := runUpgrade(context.Background(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "verify sha256")

	seq := fake.callSeq()
	require.NotContains(t, seq, "MountDMG")
	require.NotContains(t, seq, "Ditto")

	// Existing bundle is intact.
	got, err := os.ReadFile(filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"))
	require.NoError(t, err)
	require.Equal(t, "old", string(got))
}

func TestRunUpgradeDarwin_GatekeeperRejection(t *testing.T) {
	fake := &fakePlatformOps{gatekeeperErrs: []error{errors.New("denied")}}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "old")

	err := runUpgrade(context.Background(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gatekeeper")

	got, err := os.ReadFile(filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"))
	require.NoError(t, err)
	require.Equal(t, "old", string(got))

	seq := fake.callSeq()
	require.NotContains(t, seq, "Ditto")
}

func TestRunUpgradeDarwin_PostInstallGatekeeperFails_RollsBack(t *testing.T) {
	// First AssessGatekeeper (pre-install) succeeds; second (post-install)
	// fails. The new bundle must be removed and the old one restored.
	fake := &fakePlatformOps{
		gatekeeperErrs: []error{nil, errors.New("post-install denied")},
	}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "#!/bin/sh\necho old\n")

	err := runUpgrade(context.Background(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "gatekeeper")

	got, err := os.ReadFile(filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"))
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\necho old\n", string(got))

	_, err = os.Stat(filepath.Join(root, "Rune.app.bak-v9.9.0"))
	require.True(t, os.IsNotExist(err), "backup should be gone after rollback")

	seq := fake.callSeq()
	require.Equal(t, 2, countCalls(seq, "AssessGatekeeper"))
}

func TestRunUpgradeDarwin_PostInstallCodesignFails_RollsBack(t *testing.T) {
	fake := &fakePlatformOps{
		verifyCodesignErrs: []error{errors.New("codesign verify failed")},
	}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "#!/bin/sh\necho old\n")

	err := runUpgrade(context.Background(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "codesign")

	got, err := os.ReadFile(filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"))
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\necho old\n", string(got))

	_, err = os.Stat(filepath.Join(root, "Rune.app.bak-v9.9.0"))
	require.True(t, os.IsNotExist(err), "backup should be gone after rollback")
}

func TestRunUpgradeDarwin_DittoFailureRollsBack(t *testing.T) {
	fake := &fakePlatformOps{dittoErr: errors.New("ditto failed")}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "old")

	err := runUpgrade(context.Background(), opts)
	require.Error(t, err)

	// Old bundle restored.
	got, err := os.ReadFile(filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"))
	require.NoError(t, err)
	require.Equal(t, "old", string(got))

	// Backup directory removed by rollback.
	_, err = os.Stat(filepath.Join(root, "Rune.app.bak-v9.9.0"))
	require.True(t, os.IsNotExist(err))
}

func TestRunUpgradeDarwin_SymlinkFailureDoesNotRollBack(t *testing.T) {
	fake := &fakePlatformOps{symlinkErr: errors.New("perm denied")}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "old")

	// Symlink failures are non-fatal now: the bundle is already in
	// place, so runUpgrade surfaces the symlink error as a progress
	// phase and returns nil rather than rolling the whole upgrade back.
	rec := &progressSampleRecorder{}
	opts.pw = rec
	err := runUpgrade(context.Background(), opts)
	require.NoError(t, err)
	phases := rec.units()
	require.NotEmpty(t, phases)
	var sawSymlinkWarning bool
	for _, msg := range phases {
		if strings.Contains(msg, "CLI symlink") {
			sawSymlinkWarning = true
			break
		}
	}
	require.True(t, sawSymlinkWarning,
		"expected a progress phase about the failed CLI symlink, got %v", phases)

	// New bundle is still in place; backup snapshot is preserved.
	got, err := os.ReadFile(filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"))
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\necho new\n", string(got))
	_, err = os.Stat(filepath.Join(root, "Rune.app.bak-v9.9.0"))
	require.NoError(t, err)
}

func TestRunUpgradeDarwin_PrunesOldBackups(t *testing.T) {
	fake := &fakePlatformOps{}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "old")

	// Pre-seed two stale backups; only one most recent backup should
	// remain after the upgrade because BackupRetention=1.
	old1 := filepath.Join(root, "Rune.app.bak-v9.0.0")
	old2 := filepath.Join(root, "Rune.app.bak-v9.5.0")
	require.NoError(t, os.MkdirAll(old1, 0o755))
	require.NoError(t, os.MkdirAll(old2, 0o755))
	old := time.Now().Add(-72 * time.Hour)
	require.NoError(t, os.Chtimes(old1, old, old))
	require.NoError(t, os.Chtimes(old2, old.Add(time.Hour), old.Add(time.Hour)))

	err := runUpgrade(context.Background(), opts)
	require.NoError(t, err)

	// The freshly-created backup is the most recent and must remain.
	_, err = os.Stat(filepath.Join(root, "Rune.app.bak-v9.9.0"))
	require.NoError(t, err)
	// One of the older backups should be gone.
	_, errOld1 := os.Stat(old1)
	require.True(t, os.IsNotExist(errOld1), "old backup %s should be pruned", old1)
}

func TestRunUpgradeDarwin_ReplacesStaleBackup(t *testing.T) {
	fake := &fakePlatformOps{}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "#!/bin/sh\necho old\n")
	stale := filepath.Join(root, "Rune.app.bak-v9.9.0", "Contents", "MacOS", "rune")
	require.NoError(t, os.MkdirAll(filepath.Dir(stale), 0o755))
	require.NoError(t, os.WriteFile(stale, []byte("stale"), 0o755))

	require.NoError(t, runUpgrade(context.Background(), opts))

	got, err := os.ReadFile(filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune"))
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\necho new\n", string(got))
	got, err = os.ReadFile(stale)
	require.NoError(t, err)
	require.Equal(t, "#!/bin/sh\necho old\n", string(got))
}

func TestRunUpgrade_RejectsInsufficientSpace(t *testing.T) {
	fake := &fakePlatformOps{}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "old")

	// Advertise a 100 MiB artifact and pretend the install root has
	// only 1 MiB free. The check must fail before Download runs.
	opts.manifest.Size = 100 * 1024 * 1024
	fake.freeSpace = map[string]uint64{
		opts.installRoot: 1 * 1024 * 1024,
		opts.cacheDir:    10 * 1024 * 1024 * 1024,
	}

	err := runUpgrade(context.Background(), opts)
	require.Error(t, err)
	require.Contains(t, err.Error(), "insufficient free space")

	seq := fake.callSeq()
	for _, c := range seq {
		require.NotContains(t, c, "Download:")
		require.NotEqual(t, "MountDMG", c)
		require.NotEqual(t, "Ditto", c)
	}
}

func TestRunUpgrade_AcceptsWhenSizeUnknown(t *testing.T) {
	// Manifest doesn't advertise a size; the pre-flight check must
	// skip rather than block the upgrade.
	fake := &fakePlatformOps{}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "old")
	opts.manifest.Size = 0
	fake.freeSpace = map[string]uint64{
		opts.installRoot: 1, // would normally be too small
		opts.cacheDir:    1,
	}

	err := runUpgrade(context.Background(), opts)
	require.NoError(t, err)
}

func TestRunUpgrade_ReportsPhasedProgress(t *testing.T) {
	fake := &fakePlatformOps{}
	opts, root := newDarwinFixture(t, fake)
	writeExistingApp(t, root, "old")

	rec := &progressSampleRecorder{}
	opts.pw = rec

	err := runUpgrade(context.Background(), opts)
	require.NoError(t, err)

	samples := rec.snapshot()
	require.NotEmpty(t, samples, "expected at least one progress sample")
	require.Subset(t, rec.units(), []string{
		"B downloaded",
		"verifying download",
		"mounting image",
		"verifying image",
		"installing",
		"verifying install",
		"done",
	})

	// Only the terminal sample may reach total: an intermediate
	// boundary sample would dismiss the host's progress display
	// before the later phases are reported.
	for _, s := range samples[:len(samples)-1] {
		require.Less(t, s.progress, s.total,
			"non-terminal sample %+v must not reach total", s)
	}
	last := samples[len(samples)-1]
	require.Equal(t, upgradeSample{1, 1, "done"}, last)
}
