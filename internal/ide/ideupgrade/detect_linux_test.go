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

//go:build linux

package ideupgrade

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedLinuxAppDir materializes <root>/<appName>/bin/<binName> matching
// the layout install.sh ships.
func seedLinuxAppDir(t *testing.T, root, appName, binName string) string {
	t.Helper()
	binDir := filepath.Join(root, appName, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	bin := filepath.Join(binDir, binName)
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))
	resolved, err := filepath.EvalSymlinks(bin)
	require.NoError(t, err)
	return resolved
}

// seedInstall materializes a default install under root and returns
// its resolved binary path.
func seedInstall(t *testing.T, root string) string {
	t.Helper()
	return seedLinuxAppDir(t, root, "rune.app", "rune")
}

func TestDetectRunningInstall_LinuxHappyPath(t *testing.T) {
	root := t.TempDir()
	bin := seedLinuxAppDir(t, root, "rune.app", "rune")

	got, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return bin, nil },
	})
	require.NoError(t, err)
	require.Equal(t, "rune.app", got.AppName)
	require.Equal(t, filepath.Join("bin", "rune"), got.CLIBinaryRelPath)
	resolvedRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	require.Equal(t, resolvedRoot, got.InstallRoot)
}

func TestDetectRunningInstall_LinuxRefusesNonBinLayout(t *testing.T) {
	root := t.TempDir()
	// Binary lives directly under <root>/rune, not <root>/bin/rune.
	bin := filepath.Join(root, "rune")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))

	_, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return bin, nil },
	})
	require.Error(t, err)
	var notSupported *ErrUpgradeNotSupported
	require.True(t, errors.As(err, &notSupported))
	require.ErrorIs(t, notSupported.Reason, errNoAppBundle)
}

func TestDetectRunningInstall_LinuxRefusesSystemAndSandboxPrefixes(t *testing.T) {
	cases := []struct {
		name string
		path string
		want error
	}{
		{"usr", "/usr/bin/rune", errSystemInstall},
		{"opt", "/opt/rune/bin/rune", errSystemInstall},
		{"bin", "/bin/rune", errSystemInstall},
		{"sbin", "/sbin/rune", errSystemInstall},
		{"snap", "/snap/rune/current/bin/rune", errSandboxInstall},
		{"flatpak", "/var/lib/flatpak/app/rune/bin/rune", errSandboxInstall},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := detectRunningInstall(Config{
				Executable: func() (string, error) { return tc.path, nil },
			})
			require.Error(t, err)
			var notSupported *ErrUpgradeNotSupported
			require.True(t, errors.As(err, &notSupported))
			require.ErrorIs(t, notSupported.Reason, tc.want)
		})
	}
}
