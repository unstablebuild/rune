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

package hostenv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
)

// hostEnvTest isolates the process variables a Host sets so each test
// restores them.
func hostEnvTest(t *testing.T, path string, vars ...string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the host PATH tests use POSIX lists")
	}
	t.Setenv("PATH", path)
	t.Setenv(DataDirVar, "")
	for _, v := range vars {
		t.Setenv(v, "")
		require.NoError(t, os.Unsetenv(v))
	}
	return t.TempDir()
}

func pathList(entries ...string) string {
	return strings.Join(entries, ":")
}

func TestHostApplyExportsDataDirAndPutsBinFirstOnce(t *testing.T) {
	dataDir := hostEnvTest(t, pathList("/usr/bin", "/usr/local/bin"))
	h := New(dataDir, "")

	require.NoError(t, h.Apply(nil))
	require.NoError(t, h.Apply(nil))

	binDir := filepath.Join(dataDir, "bin")
	assert.Equal(t, pathList(binDir, "/usr/bin", "/usr/local/bin"), os.Getenv("PATH"))
	assert.Equal(t, dataDir, os.Getenv(DataDirVar))
	for _, sub := range []string{"bin", "lib"} {
		fi, err := os.Stat(filepath.Join(dataDir, sub))
		require.NoError(t, err, "managed dir %q not created", sub)
		assert.True(t, fi.IsDir())
	}
}

func TestHostApplyGUIEnvSemantics(t *testing.T) {
	dataDir := hostEnvTest(t, "/usr/bin",
		"STRING_EXPAND", "NUMBER", "BOOLEAN", "IN_DATADIR")
	t.Setenv("RUNE_TEST_BASE", "base-value")

	require.NoError(t, New(dataDir, "").Apply(config.MapConfig(map[string]any{
		"STRING_EXPAND": "$RUNE_TEST_BASE/sub",
		"NUMBER":        42,
		"BOOLEAN":       true,
		"IN_DATADIR":    "$RUNE_DATADIR/go",
	})))

	assert.Equal(t, "base-value/sub", os.Getenv("STRING_EXPAND"))
	assert.Equal(t, "42", os.Getenv("NUMBER"))
	assert.Equal(t, "true", os.Getenv("BOOLEAN"))
	assert.Equal(t, filepath.Join(dataDir, "go"), os.Getenv("IN_DATADIR"))
}

func TestHostApplyTwiceYieldsTheSameEnvironment(t *testing.T) {
	dataDir := hostEnvTest(t, pathList("/usr/bin", "/bin"), "GOROOT")
	t.Setenv("GOFLAGS", "-mod=mod")
	shellRCDir := t.TempDir()
	h := New(dataDir, shellRCDir)
	env := config.MapConfig(map[string]any{
		"PATH":    "$RUNE_DATADIR/python/bin:$RUNE_DATADIR/bin:$PATH:/opt/after",
		"GOFLAGS": "$GOFLAGS -trimpath",
		"GOROOT":  "$RUNE_DATADIR/go",
	})

	require.NoError(t, h.Apply(env))
	first := os.Environ()
	firstSh, err := os.ReadFile(filepath.Join(shellRCDir, ShellFragment))
	require.NoError(t, err)
	require.NoError(t, h.Apply(env))

	assert.ElementsMatch(t, first, os.Environ())
	assert.Equal(t, pathList(filepath.Join(dataDir, "bin"),
		filepath.Join(dataDir, "python", "bin"), "/usr/bin", "/bin", "/opt/after"),
		os.Getenv("PATH"))
	assert.Equal(t, "-mod=mod -trimpath", os.Getenv("GOFLAGS"))
	assert.Equal(t, filepath.Join(dataDir, "go"), os.Getenv("GOROOT"))
	secondSh, err := os.ReadFile(filepath.Join(shellRCDir, ShellFragment))
	require.NoError(t, err)
	assert.Equal(t, string(firstSh), string(secondSh))
}

func TestHostApplyDropsEntriesRemovedFromGUIEnv(t *testing.T) {
	dataDir := hostEnvTest(t, "/usr/bin")
	h := New(dataDir, "")

	require.NoError(t, h.Apply(config.MapConfig(map[string]any{
		"PATH": "/pkg/bin:$PATH",
	})))
	require.NoError(t, h.Apply(nil))

	assert.Equal(t, pathList(filepath.Join(dataDir, "bin"), "/usr/bin"), os.Getenv("PATH"))
}

func TestHostSetBasePATH(t *testing.T) {
	dataDir := hostEnvTest(t, pathList("/svc/bin", "/usr/bin"))
	binDir := filepath.Join(dataDir, "bin")
	h := New(dataDir, "")
	env := config.MapConfig(map[string]any{"PATH": "/pkg/bin:$PATH"})
	require.NoError(t, h.Apply(env))

	// The probed login shell inherits the PATH Rune set up.
	require.NoError(t, h.SetBasePATH(pathList("/login/bin", binDir, "/pkg/bin", "/usr/bin")))

	assert.Equal(t, pathList(binDir, "/pkg/bin", "/login/bin", "/usr/bin", "/svc/bin"),
		os.Getenv("PATH"), "the login PATH comes first and inherited entries are kept")

	require.NoError(t, h.Apply(nil))
	assert.Equal(t, pathList(binDir, "/login/bin", "/usr/bin", "/svc/bin"), os.Getenv("PATH"),
		"entries Rune placed are not part of the base")
}

func TestHostSetBasePATHBeforeApplyWaitsForApply(t *testing.T) {
	dataDir := hostEnvTest(t, "/min/path")
	h := New(dataDir, "")

	require.NoError(t, h.SetBasePATH("/login/bin"))
	assert.Equal(t, "/min/path", os.Getenv("PATH"))

	require.NoError(t, h.Apply(nil))
	assert.Equal(t, pathList(filepath.Join(dataDir, "bin"), "/login/bin", "/min/path"),
		os.Getenv("PATH"))
}

func TestHostWritesShellFragmentsOnlyWithAShellRCDir(t *testing.T) {
	dataDir := hostEnvTest(t, "/usr/bin")
	shellRCDir := filepath.Join(t.TempDir(), "shellrc")

	require.NoError(t, New(dataDir, "").Apply(nil))
	require.NoError(t, New(dataDir, shellRCDir).Apply(nil))

	for _, name := range []string{ShellFragment, FishFragment} {
		data, err := os.ReadFile(filepath.Join(shellRCDir, name))
		require.NoError(t, err)
		assert.Contains(t, string(data), filepath.Join(dataDir, "bin"))
	}
	_, err := os.Stat(filepath.Join(dataDir, "shellrc"))
	assert.True(t, os.IsNotExist(err), "a host without a shell rc dir writes no fragments")
}
