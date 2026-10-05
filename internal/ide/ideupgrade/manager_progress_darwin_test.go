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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
)

func TestManagerRunUpgrade_PostsDownloadProgress(t *testing.T) {
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}

	fake := &fakePlatformOps{}
	const newBinary = "#!/bin/sh\necho new\n"
	fake.dmgPayload = map[string]string{"Contents/MacOS/rune": newBinary}
	fake.archiveContent = []byte("dmg-bytes")
	sum := sha256.Sum256(fake.archiveContent)

	writeExistingApp(t, root, "#!/bin/sh\necho old\n")
	existingBinary := filepath.Join(root, "Rune.app", "Contents", "MacOS", "rune")
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "Rune.app", "Contents", "Info.plist"),
		[]byte("<plist/>"), 0o644))

	notifs := &recordingNotifications{}
	mgr, err := newWithPlatformOps(Config{
		CurrentVersion:   "v9.9.0",
		Arch:             "darwin-arm64",
		ManifestURL:      "https://example.invalid",
		Storage:          storagestub.NewInMemoryService(),
		Notifications:    notifs,
		Executable:       func() (string, error) { return existingBinary, nil },
		InstallRoot:      root,
		AppName:          "Rune.app",
		CLISymlinkPath:   filepath.Join(t.TempDir(), "rune"),
		CLIBinaryRelPath: filepath.Join("Contents", "MacOS", "rune"),
		CacheDir:         t.TempDir(),
		BackupRetention:  1,
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
		Now:              func() time.Time { return time.Unix(1_700_000_000, 0) },
	}, fake)
	require.NoError(t, err)

	manifest := Manifest{
		Version:  "v9.9.9",
		Filename: "Rune-v9.9.9.dmg",
		URL:      "https://example.invalid/Rune-v9.9.9.dmg",
		SHA256:   hex.EncodeToString(sum[:]),
		Size:     int64(len(fake.archiveContent)),
	}

	require.NoError(t, mgr.upgradeWithNotifications(context.Background(), manifest))

	notifyN, samples := notifs.snapshot()
	require.Equal(t, 2, notifyN,
		"expected one anchor Notify plus the terminal success Notify")
	require.NotEmpty(t, samples, "expected progress samples")

	var sawLive, sawComplete bool
	for _, s := range samples {
		if s.total > 0 && s.progress < s.total {
			sawLive = true
		}
		if s.total > 0 && s.progress == s.total {
			sawComplete = true
		}
	}
	require.True(t, sawLive, "expected at least one in-flight progress sample")
	require.True(t, sawComplete, "expected a completion sample reaching total")
}
