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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// seedAppBundle materializes <root>/<appName>/Contents/MacOS/<binName>
// and Contents/Info.plist so detectRunningInstall accepts the layout.
// Returns the resolved binary path (EvalSymlinks-friendly).
func seedAppBundle(t *testing.T, root, appName, binName string) string {
	t.Helper()
	binDir := filepath.Join(root, appName, "Contents", "MacOS")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	bin := filepath.Join(binDir, binName)
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))
	plist := filepath.Join(root, appName, "Contents", "Info.plist")
	require.NoError(t, os.WriteFile(plist, []byte("<plist/>"), 0o644))
	resolved, err := filepath.EvalSymlinks(bin)
	require.NoError(t, err)
	return resolved
}

// seedInstall materializes a default install under root and returns
// its resolved binary path.
func seedInstall(t *testing.T, root string) string {
	t.Helper()
	return seedAppBundle(t, root, "Rune.app", "rune")
}

func TestDetectRunningInstall_DarwinHappyPath(t *testing.T) {
	root := t.TempDir()
	bin := seedAppBundle(t, root, "Rune.app", "rune")

	got, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return bin, nil },
	})
	require.NoError(t, err)
	require.Equal(t, "Rune.app", got.AppName)
	require.Equal(t,
		filepath.Join("Contents", "MacOS", "rune"),
		got.CLIBinaryRelPath)
	// InstallRoot should be the resolved parent of the .app, not the
	// raw t.TempDir() (which on darwin may live under /var/folders/).
	resolvedRoot, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	require.Equal(t, resolvedRoot, got.InstallRoot)
}

func TestDetectRunningInstall_DarwinAcceptsNonRuneAppName(t *testing.T) {
	root := t.TempDir()
	bin := seedAppBundle(t, root, "Rune-Beta.app", "rune")

	got, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return bin, nil },
	})
	require.NoError(t, err)
	require.Equal(t, "Rune-Beta.app", got.AppName)
}

func TestDetectRunningInstall_DarwinViaSymlink(t *testing.T) {
	root := t.TempDir()
	bin := seedAppBundle(t, root, "Rune.app", "rune")

	link := filepath.Join(t.TempDir(), "rune-shim")
	require.NoError(t, os.Symlink(bin, link))

	got, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return link, nil },
	})
	require.NoError(t, err)
	require.Equal(t, "Rune.app", got.AppName)
}

func TestDetectRunningInstall_DarwinRefusesDevBinary(t *testing.T) {
	root := t.TempDir()
	dev := filepath.Join(root, "go-build", "rune")
	require.NoError(t, os.MkdirAll(filepath.Dir(dev), 0o755))
	require.NoError(t, os.WriteFile(dev, []byte("#!/bin/sh\n"), 0o755))

	_, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return dev, nil },
	})
	require.Error(t, err)
	var notSupported *ErrUpgradeNotSupported
	require.True(t, errors.As(err, &notSupported))
	require.ErrorIs(t, notSupported.Reason, errNoAppBundle)
}

func TestDetectRunningInstall_DarwinRefusesAppWithoutInfoPlist(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "Rune.app", "Contents", "MacOS")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	bin := filepath.Join(binDir, "rune")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))
	// no Info.plist

	_, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return bin, nil },
	})
	require.Error(t, err)
	var notSupported *ErrUpgradeNotSupported
	require.True(t, errors.As(err, &notSupported))
}

func TestDetectRunningInstall_DarwinRefusesBinaryOutsideMacOS(t *testing.T) {
	root := t.TempDir()
	// Create a valid bundle but put the binary in a sibling
	// directory (Contents/Resources) so detection rejects it.
	resourcesDir := filepath.Join(root, "Rune.app", "Contents", "Resources")
	require.NoError(t, os.MkdirAll(resourcesDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "Rune.app", "Contents", "Info.plist"),
		[]byte("<plist/>"), 0o644))
	bin := filepath.Join(resourcesDir, "rune")
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755))

	_, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return bin, nil },
	})
	require.Error(t, err)
	var notSupported *ErrUpgradeNotSupported
	require.True(t, errors.As(err, &notSupported))
}

func TestDetectRunningInstall_DarwinRefusesTranslocated(t *testing.T) {
	// We can't actually run a translocated bundle in a unit test;
	// exercise isTranslocatedPath directly with a synthetic path
	// that matches the macOS pattern.
	translocated := "/private/var/folders/xx/yyy/T/AppTranslocation/UUID/d/Rune.app/Contents/MacOS/rune"
	require.True(t, isTranslocatedPath(translocated))

	_, err := detectRunningInstall(Config{
		Executable: func() (string, error) { return translocated, nil },
	})
	require.Error(t, err)
	var notSupported *ErrUpgradeNotSupported
	require.True(t, errors.As(err, &notSupported))
	require.ErrorIs(t, notSupported.Reason, errTranslocated)
}
