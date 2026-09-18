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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// bindingsOf returns the command key bindings declared by a YAML config.
func bindingsOf(t *testing.T, src []byte) map[string]any {
	t.Helper()
	cfg := map[string]any{}
	require.NoError(t, yaml.Unmarshal(src, &cfg))

	command, ok := cfg["command"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	bindings, ok := command["key_bindings"].(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return bindings
}

func bindingsOfFile(t *testing.T, path string) map[string]any {
	t.Helper()
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	return bindingsOf(t, src)
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

func copyToTemp(t *testing.T, src string) string {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

var legacyConfigs = []struct {
	name   string
	legacy string
	editor string
	preset string
}{
	{"modal", "testdata/legacy_override_modal.yaml", editorModal, "preset_modal.yaml"},
	{"emacs", "testdata/legacy_override_emacs.yaml", editorEmacs, "preset_emacs.yaml"},
	{
		"standard",
		"testdata/legacy_override_standard_darwin.yaml",
		editorStandard,
		"",
	},
}

// TestMigrateConfigKeyBindingsAppliesCurrentPreset is the guarantee the
// migration exists for: a config written before the presets owned the
// binding list ends up with the bindings its editor's preset ships today.
func TestMigrateConfigKeyBindingsAppliesCurrentPreset(t *testing.T) {
	for _, tc := range legacyConfigs {
		t.Run(tc.name, func(t *testing.T) {
			preset, err := renderPreset(tc.editor, true)
			require.NoError(t, err)
			want := bindingsOf(t, []byte(preset))
			require.NotEmpty(t, want)

			path := copyToTemp(t, tc.legacy)
			require.NoError(t, migrateConfigKeyBindings(path))

			got := bindingsOfFile(t, path)
			for key, cmd := range want {
				require.Equalf(t, cmd, got[key],
					"%s must come from the current preset", key)
			}
		})
	}
}

// TestMigrateConfigKeyBindingsPicksUpMovedBindings pins that a migrated
// config tracks a binding the preset has relocated since it was written,
// rather than being frozen at the value it originally shipped with.
func TestMigrateConfigKeyBindingsPicksUpMovedBindings(t *testing.T) {
	preset, err := renderPreset(editorEmacs, true)
	require.NoError(t, err)
	want := bindingsOf(t, []byte(preset))

	moved := "<meta-o>"
	require.Equal(t, "fexplorer", want[moved],
		"fixture assumes the emacs preset homes fexplorer on %s", moved)

	legacy := bindingsOfFile(t, "testdata/legacy_override_emacs.yaml")
	require.NotContains(t, legacy, moved,
		"fixture assumes the legacy config predates the move")

	path := copyToTemp(t, "testdata/legacy_override_emacs.yaml")
	require.NoError(t, migrateConfigKeyBindings(path))
	require.Equal(t, "fexplorer", bindingsOfFile(t, path)[moved])
}

func TestStandardPresetsKeepMetaSlashForLineComments(t *testing.T) {
	for _, path := range []string{
		"preset_standard_darwin.yaml",
		"preset_standard_linux.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			bindings := bindingsOfFile(t, path)
			require.Equal(t, "cheatsheet", bindings["<alt-/>"])
			require.NotContains(t, bindings, "<meta-/>")
		})
	}
}

func TestStandardPresetsAvoidEditorKeyConflicts(t *testing.T) {
	tests := []struct {
		path        string
		explorerKey string
	}{
		{"preset_standard_darwin.yaml", "<shift-meta-e>"},
		{"preset_standard_linux.yaml", "<ctrl-shift-e>"},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			bindings := bindingsOfFile(t, tc.path)
			require.Equal(t, "quit", bindings["<meta-q>"])
			require.Equal(t, "fexplorer", bindings[tc.explorerKey])
			require.Equal(t, "cursorhistory prev", bindings["<alt-,>"])
			require.Equal(t, "cursorhistory next", bindings["<alt-.>"])
			require.Equal(t, "lsp hover", bindings["<alt-t>"])
			require.Equal(t,
				"echo {prompt}jumptoast<space>locals.scm<space>local.definition.var<space>",
				bindings["<alt-x>"])

			for _, stale := range []string{
				"<shift-tab>", "<ctrl-->", "<ctrl-shift-->",
				"<ctrl-alt-h>", "<ctrl-alt-v>",
				"<ctrl-meta-q>", "<ctrl-alt-i>", "<ctrl-shift-alt-h>",
			} {
				require.NotContains(t, bindings, stale)
			}
		})
	}
}

func TestMigrateConfigKeyBindingsSelectsPresetByEditor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config string
		want   string
	}{
		{"absent mode defaults to modal", "log_level: info\n", editorModal},
		{"modal", "editor:\n  mode: modal\n", editorModal},
		{"standard", "editor:\n  mode: standard\n", editorStandard},
		{"emacs", "editor:\n  mode: emacs\n", editorEmacs},
		{"deprecated modeless", "editor:\n  mode: modeless\n", editorStandard},
		{
			"exo uses its fallback",
			"editor:\n  mode: exo\n  exo:\n    fallback: emacs\n",
			editorEmacs,
		},
		{"exo without fallback", "editor:\n  mode: exo\n", editorModal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{}
			require.NoError(t, yaml.Unmarshal([]byte(tc.config), &cfg))
			require.Equal(t, tc.want, presetEditorFor(cfg))

			preset, err := renderPreset(tc.want, true)
			require.NoError(t, err)
			want := bindingsOf(t, []byte(preset))

			path := writeConfig(t, tc.config)
			require.NoError(t, migrateConfigKeyBindings(path))
			require.Equal(t, want, bindingsOfFile(t, path))
		})
	}
}

func TestMigrateConfigKeyBindingsKeepsCustomBindings(t *testing.T) {
	const legacy = `editor:
  mode: emacs
command:
  key_bindings:
    "<c-x><c-y>": mycommand
    "<a-w>": ""
`
	path := writeConfig(t, legacy)
	require.NoError(t, migrateConfigKeyBindings(path))

	got := bindingsOfFile(t, path)
	require.Equal(t, "mycommand", got["<c-x><c-y>"],
		"a binding the preset does not define must survive")
	require.NotContains(t, got, "<a-w>",
		"an empty value only unbound a rune.star default that is now gone")
}

// TestMigrateConfigKeyBindingsPresetWinsOverStaleValue covers a config
// still carrying a preset binding at its old value: the preset owns it,
// so the current value must replace it rather than be kept.
func TestMigrateConfigKeyBindingsPresetWinsOverStaleValue(t *testing.T) {
	preset, err := renderPreset(editorModal, true)
	require.NoError(t, err)
	want := bindingsOf(t, []byte(preset))
	require.Equal(t, "quit", want["<meta-q>"])

	path := writeConfig(t, "command:\n  key_bindings:\n    \"<meta-q>\": staleaction\n")
	require.NoError(t, migrateConfigKeyBindings(path))
	require.Equal(t, "quit", bindingsOfFile(t, path)["<meta-q>"])
}

func TestMigrateConfigKeyBindingsStampsVersionAndIsIdempotent(t *testing.T) {
	for _, tc := range legacyConfigs {
		t.Run(tc.name, func(t *testing.T) {
			path := copyToTemp(t, tc.legacy)
			require.NoError(t, migrateConfigKeyBindings(path))

			src, err := os.ReadFile(path)
			require.NoError(t, err)
			cfg := map[string]any{}
			require.NoError(t, yaml.Unmarshal(src, &cfg))
			require.Equal(t, currentConfigVersion, cfg[configVersionKey])

			require.NoError(t, migrateConfigKeyBindings(path))
			again, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, string(src), string(again),
				"a migrated config must not be rewritten again")
		})
	}
}

// TestMigrateConfigKeyBindingsLeavesShippedPresetsAlone proves a fresh
// install is never touched.
func TestMigrateConfigKeyBindingsLeavesShippedPresetsAlone(t *testing.T) {
	for _, name := range []string{
		"preset_modal.yaml", "preset_emacs.yaml",
		"preset_standard_darwin.yaml", "preset_standard_linux.yaml",
	} {
		t.Run(name, func(t *testing.T) {
			path := copyToTemp(t, name)
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			require.NoError(t, migrateConfigKeyBindings(path))
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
}

func TestMigrateConfigKeyBindingsPreservesUnrelatedSettings(t *testing.T) {
	const legacy = `log_level: debug
gui:
  default_theme: mullen
command:
  aliases:
    w: write
  key_bindings:
    "<m-q>": quit
`
	path := writeConfig(t, legacy)
	require.NoError(t, migrateConfigKeyBindings(path))

	src, err := os.ReadFile(path)
	require.NoError(t, err)
	cfg := map[string]any{}
	require.NoError(t, yaml.Unmarshal(src, &cfg))

	require.Equal(t, "debug", cfg["log_level"])
	require.Equal(t, "mullen",
		cfg["gui"].(map[string]any)["default_theme"])
	require.Equal(t, map[string]any{"w": "write"},
		cfg["command"].(map[string]any)["aliases"])
}

func TestMigrateConfigKeyBindingsMissingFileIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, migrateConfigKeyBindings(path))
	require.NoFileExists(t, path)
}

func TestMigrateBootstrappedConfigSkipsStarlark(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.star")
	const body = "config = {\"log_level\": \"info\"}\n"
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	migrateBootstrappedConfig(path)

	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, body, string(after),
		"a Starlark config must never be rewritten by the splicer")
}
