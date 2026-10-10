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
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/ide/starlarkconfig"
)

func TestGUIEnvLiveApplyHookAppliesNewlyMergedVar(t *testing.T) {
	const (
		envKey = "RUNE_TEST_LIVE_APPLY_GOROOT"
		envVal = "/from/merged/config"
	)
	t.Setenv(envKey, "")
	require.NoError(t, os.Unsetenv(envKey))

	// IDE starts from a config that has no gui.env at all, mirroring a fresh
	// install before the go package merges its env block.
	b := newConfiguredBootstrapForEnvTest(t, configFilename,
		"editor:\n  mode: modal\n", t.TempDir())

	// The package manager merges gui.env into the user config on disk before
	// invoking the post-merge hook. Reproduce that on-disk state.
	merged := "editor:\n  mode: modal\ngui:\n  env:\n    " + envKey + ": " + envVal + "\n"
	require.NoError(t, os.WriteFile(b.configPath, []byte(merged), 0o644))

	event := mergeEvent(t, "gui:\n  env:\n    "+envKey+": "+envVal+"\n")
	require.True(t, event.TouchesPath("gui", "env"))

	result, err := b.guiEnvLiveApplyHook(event)
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"gui", "env"}}, result.LivePaths,
		"merging a gui.env var must be reported as live-applied")
	assert.Equal(t, envVal, os.Getenv(envKey),
		"guiEnvLiveApplyHook must apply the freshly-merged gui.env to the live "+
			"environment so extensions started after the merge (and gopls) inherit it")
}

func TestGUIEnvLiveApplyHookAppliesVarPresentAtStartup(t *testing.T) {
	const (
		envKey = "RUNE_TEST_LIVE_APPLY_STARTUP"
		envVal = "/present/at/startup"
	)
	t.Setenv(envKey, "")
	require.NoError(t, os.Unsetenv(envKey))

	configBody := "editor:\n  mode: modal\ngui:\n  env:\n    " + envKey + ": " + envVal + "\n"
	b := newConfiguredBootstrapForEnvTest(t, configFilename, configBody, t.TempDir())

	event := mergeEvent(t, "gui:\n  env:\n    "+envKey+": "+envVal+"\n")
	result, err := b.guiEnvLiveApplyHook(event)
	require.NoError(t, err)
	assert.Equal(t, [][]string{{"gui", "env"}}, result.LivePaths)
	assert.Equal(t, envVal, os.Getenv(envKey))
}

func TestGUIEnvLiveApplyHookStarlarkOverlayConfig(t *testing.T) {
	const (
		envKey = "RUNE_TEST_LIVE_APPLY_STAR_OVERLAY"
		envVal = "/from/starlark/config"
	)
	t.Setenv(envKey, "")
	require.NoError(t, os.Unsetenv(envKey))

	b := newConfiguredBootstrapForEnvTest(t, configStarFilename,
		"config[\"terminal\"][\"initial_reservoir\"] = 2\n", t.TempDir())

	// Reproduce the on-disk state after a package install: the manager
	// appends its managed gui.env block to the user's config.star before
	// invoking the post-merge hook.
	require.NoError(t, starlarkconfig.WriteManagedConfigFileAtomic(
		b.configPath, map[string]any{
			"gui": map[string]any{"env": map[string]any{envKey: envVal}},
		}))

	event := mergeEvent(t, "gui:\n  env:\n    "+envKey+": "+envVal+"\n")
	require.True(t, event.TouchesPath("gui", "env"))

	result, err := b.guiEnvLiveApplyHook(event)
	require.NoError(t, err,
		"gui.env reload must decode the Starlark overlay config against the "+
			"full default tree")
	assert.Equal(t, [][]string{{"gui", "env"}}, result.LivePaths)
	assert.Equal(t, envVal, os.Getenv(envKey))
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
// in dataDir makes isBootstrapped true, so newBootstrapHandler builds the real
// configured IDE (with the production packageConfigMergeHook wired via
// WithPackageConfigMergeHook) instead of opening the OAuth bootstrap flow. The
// apiclient is pointed at a 404 server so construction never touches the
// network, and installBackupDir keeps install-ID tamper detection off the
// machine-global temp dir.
func newConfiguredBootstrapForEnvTest(
	t *testing.T, filename, configBody, installBackupDir string,
) *bootstrapHandler {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	// The live-apply hook changes the process environment.
	t.Setenv("PATH", os.Getenv("PATH"))
	t.Setenv("RUNE_DATADIR", os.Getenv("RUNE_DATADIR"))

	dataDir := t.TempDir()
	configPath := filepath.Join(dataDir, filename)
	require.NoError(t, os.WriteFile(configPath, []byte(configBody), 0o644))

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
	rootCfg, err := ide.Config(configPath, runeDefaultConfig())
	require.NoError(t, err)

	b, err := newBootstrapHandler(
		dataDir, configPath, "" /* workspace */, "", /* shellRCDir */
		hostenv.New(dataDir, ""), nil, /* filenames */
		nil /* launchCmd */, ide.FuncExtensionsRunner(testE2EExtensionsRunner),
		mu, publishEvent, nil /* cellPixelSize */, nil, /* setAltModifier */
		func(*url.URL) error { return nil }, clipboard.NewInMemory(),
		installBackupDir, rootCfg, pkgtrust.NewStore(dataDir, nil),
	)
	require.NoError(t, err)
	require.NotNil(t, b.realIDE, "config.yaml in dataDir must build the configured IDE directly")
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func mergeEvent(t *testing.T, diffYAML string) idepkg.ConfigMergeEvent {
	t.Helper()
	var doc yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(diffYAML), &doc))
	return idepkg.ConfigMergeEvent{Diff: &doc}
}
