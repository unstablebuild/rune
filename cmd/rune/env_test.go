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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"gopkg.in/yaml.v3"

	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/hostenv"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/term/gui"
)

func TestApplyGUIEnvAppliesNewlyIncludedVar(t *testing.T) {
	const (
		envKey = "RUNE_TEST_LIVE_APPLY_GOROOT"
		envVal = "/from/included/config"
	)
	t.Setenv(envKey, "")
	require.NoError(t, os.Unsetenv(envKey))

	// IDE starts from a config that has no gui.env at all, mirroring a fresh
	// install before the go package adds its env block.
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\n", t.TempDir())

	// The package manager writes the package config and includes it in the
	// user config on disk before the config change runs. Reproduce that
	// on-disk state.
	etc := filepath.Join(filepath.Dir(b.configPath), "etc")
	require.NoError(t, os.MkdirAll(etc, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(etc, "go.yaml"),
		[]byte("gui:\n  env:\n    "+envKey+": "+envVal+"\n"), 0o644))
	require.NoError(t, os.WriteFile(b.configPath, []byte(
		"editor:\n  mode: modal\ninclude:\n  - $RUNE_DATADIR/etc/go.yaml\n"), 0o644))

	require.NoError(t, b.applyGUIEnv())
	assert.Equal(t, envVal, os.Getenv(envKey),
		"applyGUIEnv must apply the freshly-included gui.env to the live "+
			"environment so extensions started after the change (and gopls) inherit it")
}

func TestApplyGUIEnvAppliesVarPresentAtStartup(t *testing.T) {
	const (
		envKey = "RUNE_TEST_LIVE_APPLY_STARTUP"
		envVal = "/present/at/startup"
	)
	t.Setenv(envKey, "")
	require.NoError(t, os.Unsetenv(envKey))

	configBody := "editor:\n  mode: modal\ngui:\n  env:\n    " + envKey + ": " + envVal + "\n"
	b := newConfiguredBootstrapForEnvTest(t, configFilename, configBody, t.TempDir())

	require.NoError(t, b.applyGUIEnv())
	assert.Equal(t, envVal, os.Getenv(envKey))
}

func TestApplyGUIEnvStarlarkConfigIncludingYAML(t *testing.T) {
	const (
		envKey = "RUNE_TEST_LIVE_APPLY_STAR_OVERLAY"
		envVal = "/from/starlark/config"
	)
	t.Setenv(envKey, "")
	require.NoError(t, os.Unsetenv(envKey))

	b := newConfiguredBootstrapForEnvTest(t, configStarFilename,
		"config[\"terminal\"][\"initial_reservoir\"] = 2\n", t.TempDir())

	// Reproduce the on-disk state after a package install: the manager
	// writes the package config and adds an include line to the user's
	// config.star before the config change runs.
	etc := filepath.Join(filepath.Dir(b.configPath), "etc")
	require.NoError(t, os.MkdirAll(etc, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(etc, "go.yaml"),
		[]byte("gui:\n  env:\n    "+envKey+": "+envVal+"\n"), 0o644))
	require.NoError(t, os.WriteFile(b.configPath, []byte(
		"include(\"$RUNE_DATADIR/etc/go.yaml\")\n"+
			"config[\"terminal\"][\"initial_reservoir\"] = 2\n"), 0o644))

	require.NoError(t, b.applyGUIEnv(),
		"gui.env reload must decode the Starlark config against the full default tree")
	assert.Equal(t, envVal, os.Getenv(envKey))
}

func TestConfigChangeHookSkipsGUIEnvWhenUntouched(t *testing.T) {
	const (
		envKey = "RUNE_TEST_LIVE_APPLY_UNTOUCHED"
		envVal = "/not/applied"
	)
	t.Setenv(envKey, "")
	require.NoError(t, os.Unsetenv(envKey))

	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\ngui:\n  env:\n    "+envKey+": "+envVal+"\n", t.TempDir())

	result, err := b.configChangeHook(configChange(t, "editor:\n  tabspaces: 2\n"))
	require.NoError(t, err)
	assert.NotContains(t, result.LivePaths, []string{"gui", "env"})
	assert.Equal(t, "", os.Getenv(envKey), "an untouched gui.env is not applied")
}

func TestStartLoginPathResolvePropagatesError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell resolution is POSIX-only")
	}
	shell := filepath.Join(t.TempDir(), "fakeshell")
	require.NoError(t, os.WriteFile(shell, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Setenv("SHELL", shell)

	err := <-startLoginShellPATHResolve(hostenv.New(t.TempDir(), ""))
	assert.Error(t, err, "a shell that prints no PATH must fail the resolve")
}

func TestApplyShellPATHAndGUIEnvWithPATHWaits(t *testing.T) {
	t.Setenv("PATH", "/resolved/bin")
	t.Setenv("RUNE_DATADIR", "")
	dataDir := t.TempDir()
	cfg := config.MapConfig(map[string]any{
		"env": map[string]any{"PATH": "/extra/bin:$PATH"},
	})

	pathDone := make(chan error, 1)
	pathDone <- nil

	require.NoError(t, applyShellPATHAndGUIEnv(hostenv.New(dataDir, ""), cfg, pathDone))

	assert.Equal(t, filepath.Join(dataDir, "bin")+":/extra/bin:/resolved/bin", os.Getenv("PATH"))
}

// newConfiguredBootstrapForEnvTest builds a real, already-bootstrapped
// bootstrapHandler against the given on-disk config, written to dataDir as
// filename (config.yaml or config.star, selecting the decoder). A config file
// at configPath makes isBootstrapped true, so newBootstrapHandler builds the real
// apiclient is pointed at a 404 server so construction never touches the
// network, and installBackupDir keeps install-ID tamper detection off the
// machine-global temp dir.
func newConfiguredBootstrapForEnvTest(
	t *testing.T, filename, configBody, installBackupDir string,
) *bootstrapHandler {
	t.Helper()
	b := newBootstrapForTest(t, filename, configBody, installBackupDir, http.NotFoundHandler())
	require.NotNil(t, b.realIDE, "config.yaml in dataDir must build the configured IDE directly")
	return b
}

// newBootstrapForTest builds a real bootstrapHandler whose API server is
// api. An empty configBody writes no config, so the handler starts the
// first-run flow on the pre-config IDE instead of the configured one.
func newBootstrapForTest(
	t *testing.T, filename, configBody, installBackupDir string, api http.Handler,
) *bootstrapHandler {
	t.Helper()

	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	// The live-apply hook changes the process environment.
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("RUNE_DATADIR", os.Getenv("RUNE_DATADIR"))

	dataDir := t.TempDir()
	t.Setenv("RUNE_DATADIR", dataDir)
	configPath := filepath.Join(dataDir, filename)
	if configBody != "" {
		require.NoError(t, os.WriteFile(configPath, []byte(configBody), 0o644))
	}

	restoreFlags := overrideBootstrapFlags(t, bootstrapFlagOverrides{
		httpAddress:    srv.URL,
		dataPath:       dataDir,
		configPath:     configPath,
		websiteAddress: "https://rune.test",
	})
	t.Cleanup(restoreFlags)

	mu := new(sync.Mutex)
	publishEvent, stopPump := newBootstrapPublishPump(mu)
	t.Cleanup(stopPump)
	rootCfg, err := rootConfig(configPath)
	require.NoError(t, err)

	soundOutput := gui.NewSounds()
	soundOutput.SetMuted(true)
	b, err := newBootstrapHandler(
		dataDir, configPath, "" /* workspace */, "", /* shellRCDir */
		hostenv.New(dataDir, ""), nil, /* filenames */
		nil /* launchCmd */, ide.FuncExtensionsRunner(testE2EExtensionsRunner),
		mu, publishEvent, nil, /* cellPixelSize */
		func(*url.URL) error { return nil }, clipboard.NewInMemory(), soundOutput,
		installBackupDir, rootCfg, pkgtrust.NewStore(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// configChange is the change that adds the keys of diffYAML to an empty
// config.
func configChange(t *testing.T, diffYAML string) ide.ConfigChange {
	t.Helper()
	var added map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(diffYAML), &added))
	return ide.ConfigChange{Old: map[string]any{}, New: added, Source: "test"}
}
