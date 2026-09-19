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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"gopkg.in/yaml.v3"

	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/pkgtrust"
	"unstable.build/rune/internal/ide/starlarkconfig"
)

// TestGUIEnvLiveApplyHookAppliesNewlyMergedVar is the black-box regression for
// the install-time GOROOT bug: when a package install merges a brand-new
// gui.env block into the user config, guiEnvLiveApplyHook must apply it to the
// live process environment so the freshly-started extension (and the gopls it
// launches) inherit it. The bug was that the hook read gui.env from
// IDE.Config() — the in-memory config snapshot captured when the IDE was
// constructed — which predates the merge and therefore never contains the new
// var, so os.Setenv was never called and gopls came up with GOROOT unset.
//
// The test builds a real configured IDE from a config without the var, then
// performs the on-disk merge the package manager would (writing the var into
// the config file) and drives the real hook with a real merge event. The live
// environment must reflect the merged var.
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
	assert.True(t, result.LiveApplied,
		"merging a gui.env var must be reported as live-applied")
	assert.Equal(t, envVal, os.Getenv(envKey),
		"guiEnvLiveApplyHook must apply the freshly-merged gui.env to the live "+
			"environment so extensions started after the merge (and gopls) inherit it")
}

// TestGUIEnvLiveApplyHookAppliesVarPresentAtStartup is a control: when the var
// is already in the config the IDE loaded, the hook applies it. This guards
// against a fix that simply hard-codes values and pins the contract that the
// hook reflects the persisted gui.env.
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
	assert.True(t, result.LiveApplied)
	assert.Equal(t, envVal, os.Getenv(envKey))
}

// TestGUIEnvLiveApplyHookStarlarkOverlayConfig is the black-box regression for
// the `key "terminal" not in dict` bug: a Starlark user config that mutates a
// nested key of the default tree (config["terminal"]["initial_reservoir"] = 2)
// broke the gui.env reload after a package install merged its env block. The
// hook reloaded the config through ide.Config without the rune.star baseline,
// so the overlay subscript hit an empty `config` dict and the whole gui.env
// live-apply failed. The reload must use the same baseline the IDE uses.
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
	assert.True(t, result.LiveApplied)
	assert.Equal(t, envVal, os.Getenv(envKey))
}

// fakeShell writes an executable shell script with the given body and returns
// its path. The script ignores all the interactive/login flags Rune passes.
func fakeShell(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("login-shell resolution is POSIX-only")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fakeshell")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake shell: %v", err)
	}
	return path
}

// fakeLoginShell writes a fake shell that prints the given pre-marker banner
// lines, then the env marker, then a NUL-delimited PATH entry — mimicking a
// real interactive shell whose rc files emit chatter before `env -0` runs.
func fakeLoginShell(t *testing.T, path string, banner ...string) string {
	t.Helper()
	var body strings.Builder
	for _, l := range banner {
		body.WriteString("printf '%s\\n' " + shellQuote(l) + "\n")
	}
	body.WriteString("printf '%s' " + shellQuote(runeShellEnvMarker) + "\n")
	if path != "" {
		body.WriteString("printf 'PATH=%s\\0' " + shellQuote(path) + "\n")
	}
	return fakeShell(t, body.String())
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestResolveLoginPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		banner  []string
		want    string
		wantErr bool
	}{
		{
			name:   "ignores leading banner lines",
			path:   "/login/bin:/usr/bin",
			banner: []string{"welcome-banner", "", "PATH=/should/be/ignored"},
			want:   "/login/bin:/usr/bin",
		},
		{
			name: "single entry",
			path: "/usr/bin",
			want: "/usr/bin",
		},
		{
			name:    "no PATH after marker is an error",
			path:    "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SHELL", fakeLoginShell(t, tc.path, tc.banner...))

			got, err := resolveLoginPath(loginPathTimeout, userShell)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got PATH %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveLoginPath: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got PATH %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveLoginPathParsesMarkerEnv asserts the resolver locates PATH after
// the env marker even when the shell prints pre-marker chatter that itself
// looks like KEY=VALUE env output.
func TestResolveLoginPathParsesMarkerEnv(t *testing.T) {
	t.Setenv("SHELL", fakeLoginShell(t, "/login/bin:/usr/bin",
		"some banner", "PATH=/decoy/should/not/win", "HOME=/decoy"))

	got, err := resolveLoginPath(loginPathTimeout, userShell)
	require.NoError(t, err)
	assert.Equal(t, "/login/bin:/usr/bin", got)
}

// TestResolveLoginPathTimesOut reproduces the GUI freeze: a shell that hangs
// (and ignores SIGTERM) must not block the resolver forever. With a short
// injected timeout the resolver returns an error instead of hanging.
func TestResolveLoginPathTimesOut(t *testing.T) {
	hanging := fakeShell(t, "trap '' TERM\nwhile true; do sleep 1; done\n")
	t.Setenv("SHELL", hanging)

	start := time.Now()
	_, err := resolveLoginPath(200*time.Millisecond, userShell)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("resolveLoginPath blocked too long: %v", elapsed)
	}
}

// TestResolveLoginPathFallsBackShell asserts that when $SHELL is empty the
// resolver consults userShell() rather than silently using /bin/sh.
func TestResolveLoginPathFallsBackShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("login-shell resolution is POSIX-only")
	}
	t.Setenv("SHELL", "")
	fake := fakeLoginShell(t, "/fallback/bin")

	got, err := resolveLoginPath(loginPathTimeout, func() (string, error) { return fake, nil })
	require.NoError(t, err)
	assert.Equal(t, "/fallback/bin", got)
}

// TestSetupManagedBinPathPrependsBinDirOnce asserts that setupManagedBinPath
// creates the managed dirs and prepends the bin dir to PATH exactly once.
func TestPrependPATH(t *testing.T) {
	for _, tc := range []struct {
		name, dir, base string
		sep             rune
		want            string
	}{
		{"unix", "/rune/bin", "/usr/bin:/bin", ':', "/rune/bin:/usr/bin:/bin"},
		{"windows", `C:\rune\bin`, `C:\Windows;C:\Tools`, ';', `C:\rune\bin;C:\Windows;C:\Tools`},
		{"empty base", "/rune/bin", "", ':', "/rune/bin:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := prependPATH(tc.dir, tc.base, tc.sep); got != tc.want {
				t.Fatalf("prependPATH() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSetupManagedBinPathPrependsBinDirOnce(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PATH", "/usr/bin:/usr/local/bin")

	if err := setupRuneBinPATH(dataDir); err != nil {
		t.Fatalf("setupManagedBinPath: %v", err)
	}

	binDir := filepath.Join(dataDir, "bin")
	want := binDir + string(os.PathListSeparator) + "/usr/bin:/usr/local/bin"
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("got PATH %q, want %q", got, want)
	}

	for _, sub := range []string{"bin", "lib"} {
		if fi, err := os.Stat(filepath.Join(dataDir, sub)); err != nil || !fi.IsDir() {
			t.Fatalf("managed dir %q not created: %v", sub, err)
		}
	}
}

// TestStartLoginPathResolveObservesResolvedPATH asserts that, after the
// background resolve is joined, PATH is the managed bin dir followed by the
// resolved login PATH and a gui.env value expanding $PATH observes it.
// Regression for the gui.env PATH startup race.
func TestStartLoginPathResolveObservesResolvedPATH(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("RUNE_DATADIR", dataDir)
	t.Setenv("SHELL", fakeLoginShell(t, "/login/bin:/usr/bin"))

	t.Setenv("PATH", "/min/gui/path")
	if err := setupRuneBinPATH(dataDir); err != nil {
		t.Fatalf("setupManagedBinPath: %v", err)
	}
	if err := <-startLoginShellPATHResolve(dataDir); err != nil {
		t.Fatalf("startLoginShellPATHResolve: %v", err)
	}

	binDir := filepath.Join(dataDir, "bin")
	want := binDir + ":/login/bin:/usr/bin"
	if got := os.Getenv("PATH"); got != want {
		t.Fatalf("got PATH %q, want %q", got, want)
	}
	if got := os.Getenv("PATH"); strings.Contains(got, "/min/gui/path") {
		t.Fatalf("minimal launch PATH leaked into resolved PATH: %q", got)
	}

	expanded := os.ExpandEnv("$RUNE_DATADIR/lib/foo/bin:$PATH")
	if !strings.Contains(expanded, filepath.Join(dataDir, "lib", "foo", "bin")) {
		t.Fatalf("RUNE_DATADIR not expanded in gui.env value: %q", expanded)
	}
	if !strings.Contains(expanded, "/login/bin") {
		t.Fatalf("gui.env $PATH did not observe resolved login PATH: %q", expanded)
	}
	if !strings.Contains(expanded, binDir) {
		t.Fatalf("gui.env $PATH missing managed bin dir: %q", expanded)
	}
}

// TestStartLoginPathResolvePropagatesError asserts that a resolution
// failure is delivered on the channel rather than swallowed.
func TestStartLoginPathResolvePropagatesError(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SHELL", fakeLoginShell(t, "" /* empty PATH output */))

	if err := <-startLoginShellPATHResolve(dataDir); err == nil {
		t.Fatal("expected resolution error, got nil")
	}
}

func TestApplyGUIEnvVarsSemantics(t *testing.T) {
	t.Setenv("RUNE_TEST_BASE", "base-value")

	env := config.JSONFromMap(map[string]any{
		"STRING_EXPAND": "$RUNE_TEST_BASE/sub",
		"NUMBER":        42,
		"BOOLEAN":       true,
	})
	require.NoError(t, applyGUIEnvVars(env))

	assert.Equal(t, "base-value/sub", os.Getenv("STRING_EXPAND"),
		"string values expand via os.Expand")
	assert.Equal(t, "42", os.Getenv("NUMBER"),
		"non-string values use fmt.Sprintf(%%v)")
	assert.Equal(t, "true", os.Getenv("BOOLEAN"))
}

func TestApplyGUIEnvVarsUpdatesLiveEnv(t *testing.T) {
	require.Empty(t, os.Getenv("RUNE_LIVE_APPLY_TEST"))

	env := config.JSONFromMap(map[string]any{"RUNE_LIVE_APPLY_TEST": "set"})
	require.NoError(t, applyGUIEnvVars(env))
	assert.Equal(t, "set", os.Getenv("RUNE_LIVE_APPLY_TEST"))
	t.Cleanup(func() { _ = os.Unsetenv("RUNE_LIVE_APPLY_TEST") })
}

func TestApplyGUIEnvVarsWithLookup(t *testing.T) {
	lookup := func(key string) string {
		if key == "CUSTOM" {
			return "custom-value"
		}
		return ""
	}
	env := config.JSONFromMap(map[string]any{"OUT": "$CUSTOM/x"})
	require.NoError(t, applyGUIEnvVarsWithLookup(env, lookup))
	assert.Equal(t, "custom-value/x", os.Getenv("OUT"))
	t.Cleanup(func() { _ = os.Unsetenv("OUT") })
}

// TestApplyShellPATHAndGUIEnvWithPATHWaits asserts that when gui.env defines
// PATH, the resolve result is consumed before gui.env is applied so the value
// expands against the resolved login PATH.
func TestApplyShellPATHAndGUIEnvWithPATHWaits(t *testing.T) {
	t.Setenv("PATH", "/resolved/bin")
	cfg := config.MapConfig(map[string]any{
		"env": map[string]any{"PATH": "/extra/bin:$PATH"},
	})

	pathDone := make(chan error, 1)
	pathDone <- nil

	require.NoError(t, applyShellPATHAndGUIEnv(cfg, pathDone))

	assert.Equal(t, "/extra/bin:/resolved/bin", os.Getenv("PATH"))
}

// newConfiguredBootstrapForEnvTest builds a real, already-bootstrapped
// bootstrapHandler against the given on-disk config, written to dataDir as
// filename (config.yaml or config.star, selecting the decoder). A config file
// in dataDir makes isBootstrapped true, so newBootstrapHandler builds the real
// configured IDE (with the production guiEnvLiveApplyHook wired via
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
		dataDir, configPath, "" /* workspace */, "" /* zdotDir */, nil, /* filenames */
		nil /* launchCmd */, ide.FuncExtensionsRunner(testE2EExtensionsRunner),
		mu, publishEvent,
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
