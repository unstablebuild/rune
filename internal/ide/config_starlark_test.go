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

package ide

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/workspace"
)

// decodeStarlark is a thin test helper mirroring the legacy positional
// signature so tests read naturally.
func decodeStarlark(src string, params, base map[string]any) (map[string]any, error) {
	return decodeStarlarkConfig(starlarkConfigSource{
		src:    []byte(src),
		params: params,
		base:   base,
	})
}

// readRuneStar reads the shipped rune.star joined with themes.star, mirroring
// how the production config (cmd/rune/config.go) assembles the default
// Starlark source. rune.star references GUI_THEMES, which themes.star binds.
func readRuneStar(t *testing.T) []byte {
	t.Helper()
	themes, err := os.ReadFile("../../cmd/rune/themes.star")
	require.NoError(t, err)
	runeStar, err := os.ReadFile("../../cmd/rune/rune.star")
	require.NoError(t, err)
	return append(append(themes, '\n'), runeStar...)
}

func TestDecodeStarlarkConfigBasic(t *testing.T) {
	src := `
config = {
    "log_path": "/tmp/debug.log",
    "log_level": "info",
    "clipboard": "system",
    "editor": {
        "mode": "modal",
        "autoindent": True,
        "highlights": {
            "keyword": {"fg": "yellow"},
        },
    },
    "gui": {
        "themes": {
            "romero": {"red": "#990000"},
        },
    },
}
`
	cfg, err := decodeStarlark(src, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "/tmp/debug.log", cfg["log_path"])
	assert.Equal(t, "info", cfg["log_level"])
	assert.Equal(t, "system", cfg["clipboard"])

	editor := cfg["editor"].(map[string]any)
	assert.Equal(t, "modal", editor["mode"])
	assert.Equal(t, true, editor["autoindent"])

	hl := editor["highlights"].(map[string]any)
	keyword := hl["keyword"].(map[string]any)
	assert.Equal(t, "yellow", keyword["fg"])

	gui := cfg["gui"].(map[string]any)
	themes := gui["themes"].(map[string]any)
	romero := themes["romero"].(map[string]any)
	assert.Equal(t, "#990000", romero["red"])
}

func TestDecodeStarlarkConfigTopLevelControl(t *testing.T) {
	src := `
is_gui = False

theme = "romero" if is_gui else "carmack"

aliases = {}
for kb in [("<m-q>", "quit"), ("<m-t>", "tabnew")]:
    aliases[kb[0]] = kb[1]

config = {
    "gui": {"default_theme": theme},
    "command": {"key_bindings": aliases},
}
`
	cfg, err := decodeStarlark(src, nil, nil)
	require.NoError(t, err)
	gui := cfg["gui"].(map[string]any)
	assert.Equal(t, "carmack", gui["default_theme"])

	cmd := cfg["command"].(map[string]any)
	kb := cmd["key_bindings"].(map[string]any)
	assert.Equal(t, "quit", kb["<m-q>"])
	assert.Equal(t, "tabnew", kb["<m-t>"])
}

func TestDecodeStarlarkConfigLists(t *testing.T) {
	src := `
config = {
    "command": {
        "aliases": {
            "worktreenew": [
                "!! git worktree add",
                "workspaceopen",
                "workspaceready workspacerename",
            ],
        },
    },
    "flags": ("dim", "bold"),
    "ints": [1, 2, 3],
}
`
	cfg, err := decodeStarlark(src, nil, nil)
	require.NoError(t, err)
	cmd := cfg["command"].(map[string]any)
	aliases := cmd["aliases"].(map[string]any)
	assert.Equal(t, []any{
		"!! git worktree add",
		"workspaceopen",
		"workspaceready workspacerename",
	}, aliases["worktreenew"])
	assert.Equal(t, []any{"dim", "bold"}, cfg["flags"])
	assert.Equal(t, []any{1, 2, 3}, cfg["ints"])
}

func TestDecodeStarlarkConfigTypes(t *testing.T) {
	src := `
config = {
    "yes": True,
    "no": False,
    "n": 42,
    "f": 3.5,
    "s": "hello",
    "null": None,
}
`
	cfg, err := decodeStarlark(src, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, true, cfg["yes"])
	assert.Equal(t, false, cfg["no"])
	assert.Equal(t, 42, cfg["n"])
	assert.Equal(t, 3.5, cfg["f"])
	assert.Equal(t, "hello", cfg["s"])
	assert.Nil(t, cfg["null"])
}

func TestDecodeStarlarkConfigMissingGlobal(t *testing.T) {
	_, err := decodeStarlark("x = 1\n", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "config")
}

func TestDecodeStarlarkConfigNotADict(t *testing.T) {
	_, err := decodeStarlark(`config = 1`, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be a dict")
}

func TestDecodeStarlarkConfigRejectsLoad(t *testing.T) {
	_, err := decodeStarlark(`load("other.star", "x")
config = {"x": x}
`, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "load")
}

func TestDecodeStarlarkConfigNonStringKey(t *testing.T) {
	_, err := decodeStarlark(`config = {1: "a"}`, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keys must be strings")
}

func TestDecodeConfigFileUsesFilenameExtension(t *testing.T) {
	cfg, err := decodeConfigFile(
		strings.NewReader(`config = {"log_level": "debug"}`), "config.star")
	require.NoError(t, err)
	assert.Equal(t, "debug", cfg["log_level"])

	cfg, err = decodeConfigFile(strings.NewReader("log_level: info\n"), "config.yaml")
	require.NoError(t, err)
	assert.Equal(t, "info", cfg["log_level"])

	_, err = decodeConfigFile(strings.NewReader(`config = {"log_level": "debug"}`), "config.yaml")
	require.Error(t, err)

	_, err = decodeConfigFile(strings.NewReader("log_level: info\n"), "config.star")
	require.Error(t, err)
}

//go:embed testdata/runerc_sample.star
var starlarkSampleConfig string

// TestStarlarkSampleEndToEnd loads a realistic rune.star-style config through decodeConfigFile
// and asserts the resulting config is shaped as expected.
func TestStarlarkSampleEndToEnd(t *testing.T) {
	cfg, err := decodeConfigFile(strings.NewReader(starlarkSampleConfig), "rune.star")
	require.NoError(t, err)

	assert.Equal(t, "info", cfg["log_level"])
	editor := cfg["editor"].(map[string]any)
	assert.Equal(t, "modal", editor["mode"])
	assert.Equal(t, true, editor["autoindent"])

	cmd := cfg["command"].(map[string]any)
	kb := cmd["key_bindings"].(map[string]any)
	assert.Equal(t, "quit", kb["<m-q>"])
	assert.Equal(t, "tabfocus 3", kb["<a-3>"]) // produced by the for-loop

	aliases := cmd["aliases"].(map[string]any)
	assert.Equal(t, []any{
		`!! ROOT=$(git rev-parse --git-common-dir) && ROOT=$(cd "$ROOT/.." && pwd) || exit 1`,
		`!! ROOT_HASH=$(printf %s "$ROOT" | (sha256sum 2>/dev/null || shasum -a 256) | cut -c1-4)`,
		`!! ROOT_NAME=${ROOT##*/}`,
		`!! WORKTREE=$RUNE_DATADIR/worktrees/$ROOT_NAME-$ROOT_HASH/$1`,
		`!! git worktree add "$WORKTREE" -b $1`,
		"workspaceopen $WORKTREE",
		"workspaceready workspacerename $1",
	}, aliases["worktreenew"])
}

// TestStarlarkConfigLoadsFromFile exercises loadFileConfig end-to-end
// with a .star-suffixed path.
func TestStarlarkConfigLoadsFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rune.star")
	require.NoError(t, os.WriteFile(path, []byte(starlarkSampleConfig), 0o644))

	c := &ideConfig{cfg: map[string]any{}, errors: map[string]error{}}
	require.NoError(t, loadFileConfig(c, path))
	assert.Equal(t, "info", c.cfg["log_level"])
}

func TestYAMLConfigLoadsFromFileByExtension(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("log_level: info\n"), 0o644))

	c := &ideConfig{cfg: map[string]any{}, errors: map[string]error{}}
	require.NoError(t, loadFileConfig(c, path))
	assert.Equal(t, "info", c.cfg["log_level"])
}

func TestDecodeStarlarkConfigWithParams(t *testing.T) {
	src := `
config = {
    "mode": mode,
    "tui":  tui,
}
`
	cfg, err := decodeStarlark(src,
		map[string]any{"tui": true, "mode": "modeless"}, nil)
	require.NoError(t, err)
	assert.Equal(t, true, cfg["tui"])
	assert.Equal(t, "modeless", cfg["mode"])

	// Same script with different params produces a different result.
	cfg, err = decodeStarlark(src,
		map[string]any{"tui": false, "mode": "modal"}, nil)
	require.NoError(t, err)
	assert.Equal(t, false, cfg["tui"])
	assert.Equal(t, "modal", cfg["mode"])
}

// TestRuneStarFixture decodes the shipped cmd/rune/rune.star and checks it
// branches correctly on the tui/mode params.
func TestRuneStarFixture(t *testing.T) {
	data := readRuneStar(t)

	cases := []struct {
		name   string
		params map[string]any
		checks func(*testing.T, map[string]any)
	}{
		{
			name:   "gui",
			params: map[string]any{"mode": "modal", "tui": false},
			checks: func(t *testing.T, cfg map[string]any) {
				editor := cfg["editor"].(map[string]any)
				// rune.star no longer sets editor.mode; the bootstrap
				// override layered on top is what picks the mode.
				_, hasMode := editor["mode"]
				assert.False(t, hasMode, "editor.mode should not be set in rune.star")
				assert.Equal(t, false, editor["auto_pair"])
				exo := editor["exo"].(map[string]any)
				assert.Equal(t, false, exo["experimental_highlights"])
				assert.NotContains(t, exo, "override_highlights")
				emacs := editor["emacs"].(map[string]any)
				assert.Equal(t, map[string]any{"fg": "default", "bg": "default"}, emacs["attr"])
				assert.Equal(t, map[string]any{"fg": "default", "bg": "gray"},
					emacs["message_bar"].(map[string]any)["attr"])
				assert.Equal(t, map[string]any{"fg": "grey", "bg": "yellow"}, emacs["search_attr"])
				search := editor["standard"].(map[string]any)["search"].(map[string]any)
				assertStandardSearchMap(t, search, "default")
				assert.Equal(t, map[string]any{"fg": "grey", "bg": "yellow"}, search["match_attr"])
				assert.NotContains(t, search, "status_attr")
				statusLayout := editor["status_bar"].(map[string]any)["layout"].(string)
				assert.Contains(t, statusLayout, "{{ .Status | bg \"red\" | fg \"white\" | bold }}")
				// GUI-specific window manager frame charset should use the
				// braille-ish corners.
				wm := cfg["browser"].(map[string]any)["window_manager"].(map[string]any)
				cs := wm["frame_charset"].(map[string]any)
				assert.Equal(t, "🭽", cs["topleft"])
				assert.NotContains(t, cfg, "default_attr")
			},
		},
		{
			name:   "tui",
			params: map[string]any{"mode": "modal", "tui": true},
			checks: func(t *testing.T, cfg map[string]any) {
				assert.Contains(t, cfg, "default_attr")
				wm := cfg["browser"].(map[string]any)["window_manager"].(map[string]any)
				cs := wm["frame_charset"].(map[string]any)
				assert.Equal(t, "┌", cs["topleft"])
				editor := cfg["editor"].(map[string]any)
				_, hasMode := editor["mode"]
				assert.False(t, hasMode, "editor.mode should not be set in rune.star")
				assert.Equal(t, false, editor["auto_pair"])
				emacs := editor["emacs"].(map[string]any)
				assert.Equal(t, map[string]any{"fg": "default", "bg": "default"}, emacs["attr"])
				assert.Equal(t, map[string]any{"fg": "default", "bg": "gray"},
					emacs["message_bar"].(map[string]any)["attr"])
				assert.Equal(t, map[string]any{"fg": "grey", "bg": "yellow"}, emacs["search_attr"])
				search := editor["standard"].(map[string]any)["search"].(map[string]any)
				assertStandardSearchMap(t, search, "#1e1e1e")
				assert.Equal(t, map[string]any{
					"fg": "default", "bg": "#1e1e1e", "flags": "reverse",
				}, search["match_attr"])
				// status_attr is intentionally unset so the status bar layout
				// owns the find prompt styling.
				assert.NotContains(t, search, "status_attr")
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := decodeStarlarkConfig(starlarkConfigSource{
				src:      data,
				filename: "rune.star",
				params:   c.params,
			})
			require.NoError(t, err)
			c.checks(t, cfg)
		})
	}
}

func assertStandardSearchMap(t *testing.T, search map[string]any, bg string) {
	t.Helper()
	assert.Equal(t, "<m-f>", search["find_key"])
	assert.Equal(t, "<m-r>", search["replace_key"])
	for _, key := range []string{
		"attr", "input_attr", "placeholder_attr", "frame_attr", "focus_frame_attr",
		"button_attr", "button_hover_attr", "match_attr", "current_match_attr",
	} {
		assert.Contains(t, search, key)
	}
	assert.Equal(t, map[string]any{"fg": "default", "bg": bg}, search["attr"])
	assert.Equal(t, map[string]any{"fg": "silver", "bg": bg}, search["focus_frame_attr"])
	assert.Equal(t, map[string]any{"fg": "default", "bg": "gray"}, search["button_attr"])
	assert.Equal(t, map[string]any{"fg": "default", "bg": "blue"}, search["button_hover_attr"])
}

// TestRuneStarAsDefaultConfig wires the shipped rune.star Starlark config through
// the full loadConfig path to ensure initConfig + validateConfig are happy
// with the decoded tree.
func TestRuneStarAsDefaultConfig(t *testing.T) {
	data := readRuneStar(t)

	var cfg ideConfig
	require.NoError(t, loadConfig(&cfg, "nonExistent", browser.NopWallpaper(),
		DefaultConfig{
			src:   string(data),
			modal: true,
			tui:   false,
		},
		term.RingBell, term.ScheduleNextTick, ""))
	assert.Equal(t, "modal", cfg.editorMode())
	assert.False(t, cfg.editorAutoPair())
	assert.False(t, cfg.editorAutoSave())
	assert.True(t, cfg.editorSwapDir())
	assert.NotContains(t, cfg.errors, "editor.swap_dir")
	assert.Equal(t, "info", cfg.cfg["log_level"])
	assert.Equal(t, 2000, cfg.consoleMaxHistory())
	assert.True(t, cfg.telemetryEnabled())
}

func TestModalPresetUsesHomeRowResizeBindings(t *testing.T) {
	base, err := decodeDefaultConfig(DefaultConfig{
		src: string(readRuneStar(t)), modal: true, tui: false,
	})
	require.NoError(t, err)

	overlay, err := os.ReadFile("../../cmd/rune/preset_modal.yaml")
	require.NoError(t, err)
	raw, err := decodeOverlayConfigFile(
		bytes.NewReader(overlay), "preset_modal.yaml", base)
	require.NoError(t, err)

	cfg := ideConfig{cfg: raw, errors: map[string]error{}}
	mappings := cfg.commandKeyMappings()
	for key, wantCmd := range map[string]string{
		"<alt-meta-h>": "windowresize decrease width",
		"<alt-meta-j>": "windowresize decrease height",
		"<alt-meta-k>": "windowresize increase height",
		"<alt-meta-l>": "windowresize increase width",
	} {
		seq := mustParseBindingKey(t, key)
		require.Equalf(t, [][]string{strings.Split(wantCmd, " ")}, mappings[seq],
			"%s must run %q", key, wantCmd)
	}

	for _, key := range []string{
		"<shift-meta-up>", "<shift-meta-down>",
		"<shift-meta-left>", "<shift-meta-right>",
	} {
		_, ok := mappings[mustParseBindingKey(t, key)]
		require.Falsef(t, ok, "%s must not remain bound", key)
	}
}

// TestRuneStarModelsConfig verifies the shipped rune.star renders a
// `models` block with the documented sub-keys. This locks the schema so
// downstream loaders can rely on the keys being present.
func TestRuneStarModelsConfig(t *testing.T) {
	data := readRuneStar(t)

	cfg, err := decodeStarlarkConfig(starlarkConfigSource{
		src:      data,
		filename: "rune.star",
		params:   map[string]any{"mode": "modal", "tui": false},
	})
	require.NoError(t, err)

	models, ok := cfg["models"].(map[string]any)
	require.True(t, ok, "models block missing")
	// The default model is a router alias (`models alias set default
	// <provider/model>`), not a config key.
	_, hasDefault := models["default"]
	assert.False(t, hasDefault, "models.default is not a config key")
	assert.Equal(t, "auto", models["reasoning_summary"])
	assert.Equal(t, false, models["debug_http"])

	for _, prov := range []string{
		"openai", "anthropic", "gemini", "bedrock", "codex", "claude", "custom", "local",
	} {
		_, ok := models[prov].(map[string]any)
		assert.Truef(t, ok, "models.%s missing", prov)
	}

	openai := models["openai"].(map[string]any)
	assert.Contains(t, openai, "base_url")
	assert.Contains(t, openai, "reasoning_effort")

	local := models["local"].(map[string]any)
	assert.Contains(t, local, "models_cache_dir")
	assert.Contains(t, local, "n_gpu_layers")
	assert.Contains(t, local, "threads")
	assert.Contains(t, local, "flash_attention")
	assert.Contains(t, local, "batch_size")
	assert.Contains(t, local, "max_output_tokens")
	assert.Contains(t, local, "chat_template")
	assert.Contains(t, local, "n_cache_reuse")
	sampling, ok := local["sampling"].(map[string]any)
	require.True(t, ok, "models.local.sampling missing")
	for _, key := range []string{
		"seed", "temperature", "top_k", "top_p", "min_p",
		"repeat_penalty", "repeat_last_n",
		"freq_penalty", "presence_penalty",
		"typical_p", "top_n_sigma",
		"mirostat", "mirostat_tau", "mirostat_eta",
		"dynatemp_range", "dynatemp_exponent",
		"xtc_probability", "xtc_threshold",
		"dry_multiplier", "dry_base",
		"dry_allowed_length", "dry_penalty_last_n",
	} {
		assert.Containsf(t, sampling, key, "models.local.sampling.%s missing", key)
	}
}

func TestDecodeStarlarkOverlayRead(t *testing.T) {
	base := map[string]any{
		"editor":     map[string]any{"mode": "modal"},
		"log_level":  "info",
		"extensions": map[string]any{},
	}
	src := `
if config["editor"]["mode"] == "modal":
    config["log_level"] = "debug"
    config["extensions"]["my_extension"] = {"enabled": True}
else:
    config["log_level"] = "warn"
`
	cfg, err := decodeStarlark(src, nil, base)
	require.NoError(t, err)
	assert.Equal(t, "debug", cfg["log_level"])
	ext := cfg["extensions"].(map[string]any)
	my := ext["my_extension"].(map[string]any)
	assert.Equal(t, true, my["enabled"])
}

func TestDecodeStarlarkOverlayRebind(t *testing.T) {
	base := map[string]any{"log_level": "info"}
	src := `config = {"log_level": "error"}`
	cfg, err := decodeStarlark(src, nil, base)
	require.NoError(t, err)
	assert.Equal(t, "error", cfg["log_level"])
}

func TestDecodeOverlayConfigFileStarlark(t *testing.T) {
	base := map[string]any{
		"editor":    map[string]any{"mode": "modal"},
		"log_level": "info",
	}
	src := `
if config["editor"]["mode"] == "modal":
    config["command"] = {"key": "<c-p>"}
`
	cfg, err := decodeOverlayConfigFile(strings.NewReader(src), "override.star", base)
	require.NoError(t, err)
	cmd := cfg["command"].(map[string]any)
	assert.Equal(t, "<c-p>", cmd["key"])
	// Existing keys from base are preserved.
	assert.Equal(t, "info", cfg["log_level"])
}

func TestDecodeOverlayConfigFileYAML(t *testing.T) {
	base := map[string]any{
		"editor":    map[string]any{"mode": "modal"},
		"log_level": "info",
	}
	cfg, err := decodeOverlayConfigFile(
		strings.NewReader("log_level: debug\neditor:\n  autoindent: true\n"),
		"override.yaml", base)
	require.NoError(t, err)
	assert.Equal(t, "debug", cfg["log_level"])
	editor := cfg["editor"].(map[string]any)
	// deep-merge: existing mode preserved, new autoindent added.
	assert.Equal(t, "modal", editor["mode"])
	assert.Equal(t, true, editor["autoindent"])
}

func TestDecodeOverlayConfigFileStarlarkRebindMergesBase(t *testing.T) {
	base := map[string]any{
		"editor":    map[string]any{"mode": "vi"},
		"log_level": "info",
	}
	src := `config = {"command": {"key": "<c-p>"}}`
	cfg, err := decodeOverlayConfigFile(strings.NewReader(src),
		"override.star", base)
	require.NoError(t, err)
	cmd := cfg["command"].(map[string]any)
	assert.Equal(t, "<c-p>", cmd["key"])
	// Base keys preserved across a top-level rebind.
	assert.Equal(t, "info", cfg["log_level"])
	editor := cfg["editor"].(map[string]any)
	assert.Equal(t, "vi", editor["mode"])
}

func TestDecodeOverlayConfigFileStarlarkEmptyPreservesBase(t *testing.T) {
	base := map[string]any{"log_level": "info"}
	cfg, err := decodeOverlayConfigFile(
		strings.NewReader("# just a comment\n"),
		"override.star", base)
	require.NoError(t, err)
	assert.Equal(t, "info", cfg["log_level"])
}

func TestLoadConfigStarUserOverlayPreservesEmbeddedRuneStar(t *testing.T) {
	data := readRuneStar(t)

	dir := t.TempDir()
	userPath := filepath.Join(dir, "config.star")
	require.NoError(t, os.WriteFile(userPath,
		[]byte(`config = {"log_level": "debug"}`), 0o644))

	var cfg ideConfig
	require.NoError(t, loadConfig(&cfg, userPath, browser.NopWallpaper(),
		DefaultConfig{
			src:   string(data),
			modal: true,
			tui:   false,
		},
		term.RingBell, term.ScheduleNextTick, ""))

	assert.Equal(t, "debug", cfg.cfg["log_level"])
	// Defaults from cmd/rune/rune.star survive the top-level rebind.
	assert.Equal(t, "modal", cfg.editorMode())
	assert.False(t, cfg.editorAutoPair())
	assert.Equal(t, 2000, cfg.consoleMaxHistory())
}

func TestLoadWorkspaceConfigStar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.star")
	require.NoError(t, os.WriteFile(path, []byte(`config["log_level"] = "debug"`), 0o644))

	c := &ideConfig{cfg: map[string]any{"log_level": "info"}, errors: map[string]error{}}
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	ws := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
	defer ws.Close()

	isConfigErr, err := loadWorkspaceConfig("config.star", ws, uri, c)
	require.NoError(t, err)
	assert.False(t, isConfigErr)
	assert.Equal(t, "debug", c.cfg["log_level"])
}

func TestLoadWorkspaceConfigStarOverridesExtensionConfigFromBase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.star")
	require.NoError(t, os.WriteFile(path, []byte(`
config["extensions"]["git"]["config"]["from_star"] = "star"
config["extensions"]["git"]["config"]["nested"]["override"] = "star"
`), 0o644))

	c := &ideConfig{cfg: map[string]any{
		"extensions": map[string]any{
			"git": map[string]any{
				"path": "myPath",
				"config": map[string]any{
					"from_base": "base",
					"nested": map[string]any{
						"keep":     "yes",
						"override": "base",
					},
				},
			},
		},
	}, errors: map[string]error{}}
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	ws := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
	defer ws.Close()

	isConfigErr, err := loadWorkspaceConfig("config.star", ws, uri, c)
	require.NoError(t, err)
	assert.False(t, isConfigErr)

	exts := c.extensions()
	gitExt, ok := exts["git"]
	require.True(t, ok)

	extCfg, ok := gitExt.config()
	require.True(t, ok)

	fromBase, err := extCfg.GetString("from_base")
	require.NoError(t, err)
	assert.Equal(t, "base", fromBase)

	fromStar, err := extCfg.GetString("from_star")
	require.NoError(t, err)
	assert.Equal(t, "star", fromStar)

	nested, err := extCfg.GetConfig("nested")
	require.NoError(t, err)

	keep, err := nested.GetString("keep")
	require.NoError(t, err)
	assert.Equal(t, "yes", keep)

	override, err := nested.GetString("override")
	require.NoError(t, err)
	assert.Equal(t, "star", override)
}

func TestLoadWorkspaceConfigYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("log_level: warn\n"), 0o644))

	c := &ideConfig{cfg: map[string]any{"log_level": "info"}, errors: map[string]error{}}
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	scheme, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	ws := workspace.NewSchemeWorkspace(uri, scheme, inlineSchedule)
	defer ws.Close()

	isConfigErr, err := loadWorkspaceConfig("config.yaml", ws, uri, c)
	require.NoError(t, err)
	assert.False(t, isConfigErr)
	assert.Equal(t, "warn", c.cfg["log_level"])
}

func TestDecodeOverlayConfigFileUsesFilenameExtension(t *testing.T) {
	base := map[string]any{"log_level": "info"}

	cfg, err := decodeOverlayConfigFile(
		strings.NewReader(`config["log_level"] = "debug"`),
		"config.star", cloneTestMap(base))
	require.NoError(t, err)
	assert.Equal(t, "debug", cfg["log_level"])

	cfg, err = decodeOverlayConfigFile(
		strings.NewReader("log_level: warn\n"),
		"config.yaml", cloneTestMap(base))
	require.NoError(t, err)
	assert.Equal(t, "warn", cfg["log_level"])

	_, err = decodeOverlayConfigFile(
		strings.NewReader(`config["log_level"] = "debug"`),
		"config.yaml", cloneTestMap(base))
	require.Error(t, err)

	_, err = decodeOverlayConfigFile(
		strings.NewReader("log_level: warn\n"),
		"config.star", cloneTestMap(base))
	require.Error(t, err)
}

func TestStandardPresetUsesModifierLayoutBindings(t *testing.T) {
	runeStar := readRuneStar(t)

	common := map[string]string{
		"<alt-i>": "windowfocus up",
		"<alt-j>": "windowfocus left",
		"<alt-k>": "windowfocus down",
		"<alt-l>": "windowfocus right",

		"<alt-shift-i>": "windowmove up",
		"<alt-shift-j>": "windowmove left",
		"<alt-shift-k>": "windowmove down",
		"<alt-shift-l>": "windowmove right",

		"<alt-meta-i>": "windowresize increase height",
		"<alt-meta-j>": "windowresize decrease width",
		"<alt-meta-k>": "windowresize decrease height",
		"<alt-meta-l>": "windowresize increase width",

		"<alt-h>": "windowdefaultsplit h",
		"<alt-v>": "windowdefaultsplit v",

		"<alt-n>":           "windownew",
		"<alt-enter>":       "terminalneworsplit",
		"<alt-q>":           "windowclose",
		"<alt-shift-q>":     "windowcloseall",
		"<alt-m>":           "windowtogglemaximize",
		"<alt-shift-enter>": "echo {prompt}windowconverttab<space>",
		"<alt-w>":           "tabclose",
		"<alt-[>":           "tabprevious",
		"<alt-]>":           "tabnext",
		"<alt-shift-[>":     "tabmove left",
		"<alt-shift-]>":     "tabmove right",

		"<alt-t>":       "lsp hover",
		"<alt-shift-t>": "echo {prompt}lsp<space>hover<space>",
		"<alt-p>":       "lsp implementation",
		"<alt-shift-p>": "echo {prompt}lsp<space>implementation<space>",
		"<alt-e>":       "lsp diagnostics",
		"<alt-x>":       "echo {prompt}jumptoast<space>locals.scm<space>local.definition.var<space>",

		"<alt-,>": "cursorhistory prev",
		"<alt-.>": "cursorhistory next",

		"<ctrl-meta-i>": "gitprevchange",
		"<ctrl-meta-k>": "gitnextchange",
		"<ctrl-meta-j>": "lspprevdiagnostic",
		"<ctrl-meta-l>": "lspnextdiagnostic",
	}
	tests := []struct {
		name    string
		file    string
		unbound []string
	}{
		{
			name: "darwin",
			file: "preset_standard_darwin.yaml",
		},
		{
			name: "linux",
			file: "preset_standard_linux.yaml",
			unbound: []string{
				"<c-s-a-j>", "<c-s-a-l>",
				"<c-s-m-i>", "<c-s-m-j>", "<c-s-m-k>", "<c-s-m-l>",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base, err := decodeDefaultConfig(DefaultConfig{
				src: string(runeStar), modal: true, tui: false,
			})
			require.NoError(t, err)

			overlay, err := os.ReadFile(filepath.Join("../../cmd/rune", tc.file))
			require.NoError(t, err)
			cfg, err := decodeOverlayConfigFile(
				bytes.NewReader(overlay), tc.file, base)
			require.NoError(t, err)

			c := &ideConfig{cfg: cfg, errors: map[string]error{}}
			mappings := c.commandKeyMappings()

			wantBound := make(map[string]string, len(common))
			for key, cmd := range common {
				wantBound[key] = cmd
			}
			for key, wantCmd := range wantBound {
				seq := mustParseBindingKey(t, key)
				got, ok := mappings[seq]
				require.Truef(t, ok, "%s must be bound", key)
				require.Equalf(t, [][]string{strings.Split(wantCmd, " ")},
					got, "%s must run %q", key, wantCmd)
			}

			lookup := c.commandKeyBindingLookup()
			for wantCmd, wantKey := range map[string]string{
				"windowfocus up":               "<alt-i>",
				"windowfocus left":             "<alt-j>",
				"windowfocus down":             "<alt-k>",
				"windowfocus right":            "<alt-l>",
				"windowmove up":                "<alt-shift-i>",
				"windowmove left":              "<alt-shift-j>",
				"windowmove down":              "<alt-shift-k>",
				"windowmove right":             "<alt-shift-l>",
				"windowresize increase height": "<alt-meta-i>",
				"windowresize decrease width":  "<alt-meta-j>",
				"windowresize decrease height": "<alt-meta-k>",
				"windowresize increase width":  "<alt-meta-l>",
				"tabprevious":                  "<alt-[>",
				"tabnext":                      "<alt-]>",
				"tabmove left":                 "<alt-shift-[>",
				"tabmove right":                "<alt-shift-]>",
				"lspprevdiagnostic":            "<ctrl-meta-j>",
				"lspnextdiagnostic":            "<ctrl-meta-l>",
				"gitprevchange":                "<ctrl-meta-i>",
				"gitnextchange":                "<ctrl-meta-k>",
				"windowdefaultsplit h":         "<alt-h>",
				"windowdefaultsplit v":         "<alt-v>",
				"lsp hover":                    "<alt-t>",
				"lsp implementation":           "<alt-p>",
				"lsp diagnostics":              "<alt-e>",
				"cursorhistory prev":           "<alt-,>",
				"cursorhistory next":           "<alt-.>",
				"windowtogglemaximize":         "<alt-m>",
			} {
				cmd := strings.Split(wantCmd, " ")
				require.Equalf(t, wantKey, lookup(cmd[0], cmd[1:]),
					"%q must resolve to %s", wantCmd, wantKey)
			}

			for _, key := range []string{
				"<m-left>", "<m-right>", "<m-down>", "<m-up>",
				"<s-m-left>", "<s-m-right>", "<s-m-down>", "<s-m-up>",
				"<a-s-left>", "<a-s-right>",
				"<m-h>", "<m-i>", "<m-j>", "<m-k>", "<m-l>",
				"<s-m-h>", "<s-m-i>", "<s-m-j>", "<s-m-k>", "<s-m-l>",
				"<a-s-h>",
				"<c-a-m-i>", "<c-a-m-j>", "<c-a-m-k>", "<c-a-m-l>",
				"<c-i>", "<c-k>",
			} {
				seq := mustParseBindingKey(t, key)
				if got, ok := mappings[seq]; ok {
					require.Equalf(t, [][]string{{""}}, got,
						"%s is an editor chord and must not carry a layout command", key)
				}
			}
			for _, key := range tc.unbound {
				seq := mustParseBindingKey(t, key)
				if got, ok := mappings[seq]; ok {
					require.Equalf(t, [][]string{{""}}, got,
						"%s must not carry a stale Standard layout command", key)
				}
			}
			for _, key := range []string{"<ctrl-meta-h>", "<ctrl-meta-v>"} {
				seq := mustParseBindingKey(t, key)
				if got, ok := mappings[seq]; ok {
					require.Equalf(t, [][]string{{""}}, got,
						"%s must not retain its superseded Standard binding", key)
				}
			}
		})
	}
}

func TestStandardPresetUsesPlatformApplicationBindings(t *testing.T) {
	runeStar := readRuneStar(t)

	for _, tc := range []struct {
		name       string
		file       string
		commandKey string
		bound      map[string]string
	}{
		{
			name:       "darwin",
			file:       "preset_standard_darwin.yaml",
			commandKey: "<s-m-p>",
			bound: map[string]string{
				"<m-s>":   "write",
				"<m-o>":   "searchfile",
				"<m-t>":   "tabnew",
				"<m-w>":   "tabclose",
				"<s-m-f>": "searchtext",
			},
		},
		{
			name:       "linux",
			file:       "preset_standard_linux.yaml",
			commandKey: "<c-s-p>",
			bound: map[string]string{
				"<c-s>":   "write",
				"<c-s-s>": "writeall",
				"<c-o>":   "searchfile",
				"<c-n>":   "tabnew",
				"<a-w>":   "tabclose",
				"<c-s-f>": "searchtext",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := decodeDefaultConfig(DefaultConfig{
				src: string(runeStar), modal: true, tui: false,
			})
			require.NoError(t, err)

			overlay, err := os.ReadFile(filepath.Join("../../cmd/rune", tc.file))
			require.NoError(t, err)
			cfg, err := decodeOverlayConfigFile(
				bytes.NewReader(overlay), tc.file, base)
			require.NoError(t, err)

			c := &ideConfig{cfg: cfg, errors: map[string]error{}}
			require.Equal(t, mustParseBindingKey(t, tc.commandKey).First,
				c.commandKey())
			mappings := c.commandKeyMappings()
			for key, wantCmd := range tc.bound {
				seq := mustParseBindingKey(t, key)
				got, ok := mappings[seq]
				require.Truef(t, ok, "%s must be bound", key)
				require.Equalf(t, [][]string{strings.Split(wantCmd, " ")},
					got, "%s must run %q", key, wantCmd)
			}
		})
	}
}

// TestStandardPresetUnbindsStaleModalChords guards against H-based modal
// aliases surviving after standard mode re-homes layout onto IJKL.
func TestStandardPresetUnbindsStaleModalChords(t *testing.T) {
	runeStar := readRuneStar(t)

	base, err := decodeDefaultConfig(DefaultConfig{
		src: string(runeStar), modal: true, tui: false,
	})
	require.NoError(t, err)

	overlay, err := os.ReadFile("../../cmd/rune/preset_standard_darwin.yaml")
	require.NoError(t, err)
	cfg, err := decodeOverlayConfigFile(
		bytes.NewReader(overlay), "preset_standard_darwin.yaml", base)
	require.NoError(t, err)

	c := &ideConfig{cfg: cfg, errors: map[string]error{}}
	mappings := c.commandKeyMappings()

	// The stale modal chords must no longer run their old commands.
	staleUnbound := map[string]string{
		"<m-h>":          "windowfocus left",
		"<m-j>":          "windowfocus down",
		"<m-k>":          "windowfocus up",
		"<s-m-h>":        "windowmove left",
		"<s-m-j>":        "windowmove down",
		"<s-m-k>":        "windowmove up",
		"<alt-meta-h>":   "windowresize decrease width",
		"<a-s-h>":        "tabmove left",
		"<a-h>":          "tabprevious",
		"<c-o>":          "cursorhistory prev",
		"<c-i>":          "cursorhistory next",
		"<shift-tab>":    "fexplorer",
		"<ctrl-->":       "cursorhistory prev",
		"<ctrl-shift-->": "cursorhistory next",
		"<ctrl-alt-h>":   "lsp hover",
		"<ctrl-alt-v>":   "echo {prompt}jumptoast<space>locals.scm<space>local.definition.var<space>",
		"<ctrl-meta-q>":  "lsp hover",
		"<ctrl-alt-i>":   "lsp implementation",
		"<ctrl-alt-j>":   "cursorhistory prev",
		"<ctrl-alt-l>":   "cursorhistory next",
	}
	for key, oldCmd := range staleUnbound {
		seq := mustParseBindingKey(t, key)
		if got, ok := mappings[seq]; ok {
			require.NotEqualf(t, [][]string{strings.Split(oldCmd, " ")}, got,
				"%s must not still run %q", key, oldCmd)
		}
	}

	// The reverse lookup must resolve to one deterministic standard chord.
	lookup := c.commandKeyBindingLookup()
	wantResolved := map[string][]string{
		"<alt-shift-j>": {"windowmove", "left"},
		"<alt-j>":       {"windowfocus", "left"},
		"<alt-meta-j>":  {"windowresize", "decrease", "width"},
		"<alt-h>":       {"windowdefaultsplit", "h"},
		"<alt-v>":       {"windowdefaultsplit", "v"},
		"<alt-shift-[>": {"tabmove", "left"},
		"<alt-]>":       {"tabnext"},
		"<alt-[>":       {"tabprevious"},
		"<alt-.>":       {"cursorhistory", "next"},
		"<alt-,>":       {"cursorhistory", "prev"},
		"<alt-t>":       {"lsp", "hover"},
		"<alt-p>":       {"lsp", "implementation"},
		"<alt-e>":       {"lsp", "diagnostics"},
	}
	for wantKey, cmd := range wantResolved {
		got := lookup(cmd[0], cmd[1:])
		require.Equalf(t, wantKey, got,
			"%v must resolve to %s, got %s", cmd, wantKey, got)
	}
}

// TestEmacsPresetKeepsCommandsOffEditorChords pins that the emacs preset
// opens the command prompt with M-x (on <alt>, authentic Emacs Meta) and
// uses GNU Emacs navigation keys where Rune has matching behavior, and homes
// Rune-only commands on the <meta> (Cmd) window/workspace/tab layer. The emacs editor owns the single-modifier
// control and alt chords (motion, kill, yank, mark, folds, M-x), so any host
// command sharing one of those would be shadowed and unreachable — every base
// <alt> command binding is therefore explicitly unbound. <meta> is free of
// editor bindings, so frequent directional operations live there and remain
// available when terminal programs intercept <c-x>. GNU-compatible file and
// session commands can still use C-x, but layout management remains global.
func TestEmacsPresetKeepsCommandsOffEditorChords(t *testing.T) {
	runeStar := readRuneStar(t)

	base, err := decodeDefaultConfig(DefaultConfig{
		src: string(runeStar), modal: true, tui: false,
	})
	require.NoError(t, err)

	overlay, err := os.ReadFile("../../cmd/rune/preset_emacs.yaml")
	require.NoError(t, err)
	cfg, err := decodeOverlayConfigFile(
		bytes.NewReader(overlay), "preset_emacs.yaml", base)
	require.NoError(t, err)

	require.NoError(t, validateConfig(cfg),
		"the emacs preset must validate cleanly")

	c := &ideConfig{cfg: cfg, errors: map[string]error{}}
	require.Equal(t, editorModeEmacs, c.editorMode())

	// The terminal's scrollback find reuses isearch-forward's key instead
	// of the default <meta-f>, which this preset keeps live for
	// windowfocus right (see the wantLive table below).
	require.Equal(t, term.KeyComb{Mod: term.ModCtrl, Ch: 's'},
		c.terminalSearchConfig().FindKey)

	// The command prompt opens with M-x (execute-extended-command) on the
	// authentic Meta layer (<alt>).
	require.Equal(t, term.KeyComb{Mod: term.ModAlt, Ch: 'x'}, c.commandKey())

	mappings := c.commandKeyMappings()

	wantBound := map[string]string{
		"<c-x><c-s>": "write",
		"<c-x>s":     "writeall",
		"<c-x><c-c>": "quit",
		"<c-x><c-f>": "searchfile",
		"<a-,>":      "cursorhistory prev",
		"<c-a-,>":    "cursorhistory next",
		"<c-x><c-x>": "exchangepointandmark",
		"<a-.>":      "lsp definition",
		"<a-s-/>":    "lsp references",
		"<c-a-.>":    "echo {prompt}lsp<space>definition<space>",
		"<c-s-a-/>":  "echo {prompt}lsp<space>references<space>",
		"<m-j>": "echo {prompt}jumptoast<space>locals.scm<space>" +
			"local.definition.method|local.definition.function<space>",
		"<a-s>o":     "searchtext",
		"<m-h>":      "lsp hover",
		"<c-a-\\\\>": "lsp format",
		"<c-a-i>":    "lsp complete",
		"<f5>":       "gitprevchange",
		"<f6>":       "gitnextchange",
		"<f7>":       "lspprevdiagnostic",
		"<f8>":       "lspnextdiagnostic",
		"<f9>":       "lsp diagnostics",
		"<m-f3>":     "jumptolocation next bookmark",
		"<s-m-f3>":   "jumptolocation previous bookmark",
		"<m-f4>":     "locationhighlight bookmark",
	}
	for key, wantCmd := range wantBound {
		seq := mustParseBindingKey(t, key)
		got, ok := mappings[seq]
		require.Truef(t, ok, "%s must be bound", key)
		require.Equalf(t, [][]string{strings.Split(wantCmd, " ")},
			got, "%s must run %q", key, wantCmd)
	}

	// The host layout uses Emacs's P/N/B/F direction vocabulary without
	// consuming the editor-owned C-P/N/B/F and M-P/N/B/F chords.
	wantLive := map[string]string{
		"<m-p>":   "windowfocus up",
		"<m-b>":   "windowfocus left",
		"<m-n>":   "windowfocus down",
		"<m-f>":   "windowfocus right",
		"<m-g>":   "jumptolocation next search",
		"<s-m-g>": "jumptolocation prev search",
		"<m-i>":   "lsp implementation",
		"<s-m-i>": "echo {prompt}lsp<space>implementation<space>",
		"<s-m-h>": "echo {prompt}lsp<space>hover<space>",

		"<s-m-p>": "windowmove up",
		"<s-m-b>": "windowmove left",
		"<s-m-n>": "windowmove down",
		"<s-m-f>": "windowmove right",

		"<m-up>":    "windowresize increase height",
		"<m-left>":  "windowresize decrease width",
		"<m-down>":  "windowresize decrease height",
		"<m-right>": "windowresize increase width",

		"<m-d>":   "windownew down",
		"<m-r>":   "windownew right",
		"<m-k>":   "windowclose",
		"<s-m-k>": "windowcloseall",
		"<m-m>":   "windowtogglemaximize",
		"<a-s-x>": "history",

		"<c-m-h>":   "windowdefaultsplit h",
		"<c-m-v>":   "windowdefaultsplit v",
		"<m-w>":     "tabclose",
		"<m-1>":     "workspacefocus 1",
		"<s-m-1>":   "workspacemove 1",
		"<m-enter>": "terminalneworsplit",
		"<c-tab>":   "tabnext",
		"<c-s-tab>": "tabprevious",
		"<m-]>":     "tabnext",
		"<m-[>":     "tabprevious",
		"<s-m-]>":   "tabmove right",
		"<s-m-[>":   "tabmove left",

		"<m-o>":   "fexplorer",
		"<m-l>":   "lspnextdiagnostic",
		"<s-m-l>": "lspprevdiagnostic",
		"<m-e>":   "lsp diagnostics",
		"<m-s>": "echo {prompt}jumptoast<space>locals.scm<space>" +
			"local.definition.type<space>",
		"<m-x>": "echo {prompt}jumptoast<space>locals.scm<space>" +
			"local.definition.var<space>",
	}
	for key, wantCmd := range wantLive {
		seq := mustParseBindingKey(t, key)
		got, ok := mappings[seq]
		require.Truef(t, ok, "%s must be bound on the <meta> IDE layer", key)
		require.Equalf(t, [][]string{strings.Split(wantCmd, " ")},
			got, "%s must run %q", key, wantCmd)
	}

	lookup := c.commandKeyBindingLookup()
	for wantCmd, wantKey := range map[string]string{
		"windowfocus up":               "<meta-p>",
		"windowfocus left":             "<meta-b>",
		"windowfocus down":             "<meta-n>",
		"windowfocus right":            "<meta-f>",
		"windowmove up":                "<shift-meta-p>",
		"windowmove left":              "<shift-meta-b>",
		"windowmove down":              "<shift-meta-n>",
		"windowmove right":             "<shift-meta-f>",
		"windowresize increase height": "<meta-up>",
		"windowresize decrease width":  "<meta-left>",
		"windowresize decrease height": "<meta-down>",
		"windowresize increase width":  "<meta-right>",
		"windowclose":                  "<meta-k>",
		"windowcloseall":               "<shift-meta-k>",
		"windownew down":               "<meta-d>",
		"windownew right":              "<meta-r>",
		"windowtogglemaximize":         "<meta-m>",
		"history":                      "<alt-shift-x>",
		"cursorhistory prev":           "<alt-,>",
		"cursorhistory next":           "<ctrl-alt-,>",
		"lsp definition":               "<alt-.>",
		"lsp references":               "<alt-shift-/>",
		"lsp implementation":           "<meta-i>",
		"lsp hover":                    "<meta-h>",
		"lsp format":                   "<ctrl-alt-\\\\>",
		"lsp diagnostics":              "<meta-e>",
		"searchtext":                   "<alt-s>o",
		"jumptolocation next search":   "<meta-g>",
		"jumptolocation prev search":   "<shift-meta-g>",
		"fexplorer":                    "<meta-o>",
		"lsp complete":                 "<ctrl-alt-i>",
		"tabclose":                     "<meta-w>",
		"tabprevious":                  "<meta-[>",
		"tabnext":                      "<meta-]>",
		"tabmove left":                 "<shift-meta-[>",
		"tabmove right":                "<shift-meta-]>",
		"gitprevchange":                "<f5>",
		"gitnextchange":                "<f6>",
		"lspprevdiagnostic":            "<shift-meta-l>",
		"lspnextdiagnostic":            "<meta-l>",
	} {
		cmd := strings.Split(wantCmd, " ")
		require.Equalf(t, wantKey, lookup(cmd[0], cmd[1:]),
			"%q must resolve to %s", wantCmd, wantKey)
	}

	for _, key := range []string{
		"<s-m-j>",
		"<alt-meta-h>", "<alt-meta-j>", "<alt-meta-k>", "<alt-meta-l>",
		"<c-m-i>", "<c-m-j>", "<c-m-k>", "<c-m-l>",
		"<c-s-m-i>", "<c-s-m-j>", "<c-s-m-k>", "<c-s-m-l>",
		"<c-a-m-i>", "<c-a-m-j>", "<c-a-m-k>", "<c-a-m-l>",
		"<c-a-m-up>", "<c-a-m-left>", "<c-a-m-down>", "<c-a-m-right>",
		"<c-o>", "<c-i>", "<s-tab>", "<f2>",
	} {
		seq := mustParseBindingKey(t, key)
		if got, ok := mappings[seq]; ok {
			require.Equalf(t, [][]string{{""}}, got,
				"%s is a stale HJKL layout chord", key)
		}
	}

	for _, key := range []string{
		"<c-x>9", "<c-x>d", "<c-x>b", "<c-x>j", "<c-x>?",
	} {
		_, ok := mappings[mustParseBindingKey(t, key)]
		require.Falsef(t, ok,
			"%s must not remain as a terminal-inaccessible layout binding", key)
	}

	for _, key := range []string{
		"<c-x>[", "<c-x>]", "<c-x>g", "<c-x>n", "<c-x>p",
		"<c-x>r", "<c-x>i", "<c-x>t", "<c-x>e", "<c-x>/",
	} {
		seq := mustParseBindingKey(t, key)
		if got, ok := mappings[seq]; ok {
			require.Equalf(t, [][]string{{""}}, got,
				"%s must not carry an unrelated Rune command", key)
		}
	}

	// None of the emacs editor's single-modifier editing chords may carry
	// a live host command binding: they belong to the editor and would be
	// shadowed if the command layer claimed them. An explicit unbind maps
	// to the empty command [[""]], which the dispatcher treats as no-op.
	// <alt> is authentic Meta (M-f/b/d/w, M-x, case ops), so every base
	// <alt> command chord is unbound; C-SPC is set-mark.
	reserved := []string{
		"<a-f>", "<a-b>", "<a-d>", "<a-w>", "<a-l>", "<a-h>",
		"<a-t>", "<a-r>", "<a-i>", "<a-j>", "<a-k>", "<a-v>",
		"<a-s>", "<a-`>", "<a-s-l>", "<a-s-h>",
		"<a-1>", "<a-2>", "<a-9>", "<a-s-1>", "<a-s-9>",
		"<alt-enter>", "<c-space>",
		// Sentence motion, zap, digit arguments, negative argument and the
		// GNU undo chords also belong to the editor.
		"<a-a>", "<a-e>", "<a-z>", "<a-0>", "<a-->",
		"<c-u>", "<c-/>", "<c-_>", "<c-->",
	}
	for _, key := range reserved {
		seq := mustParseBindingKey(t, key)
		if got, ok := mappings[seq]; ok {
			require.Equalf(t, [][]string{{""}}, got,
				"%s is an emacs editor chord and must not carry a live "+
					"command binding (found %v)", key, got)
		}
	}

	// C-c cannot be a command prefix: the editor claims a bare <c-c> for
	// copy, so no <c-c>-prefixed sequence would ever fire. Guard against a
	// future edit reintroducing one.
	for seq := range mappings {
		require.NotEqualf(t, term.KeyComb{Mod: term.ModCtrl, Ch: 'c'}, seq.First,
			"no command may use <c-c> as a prefix in emacs mode: %v", seq)
	}
}

// TestValidateCommandPromptFallback pins the command.key guard for the
// non-modal editors: a bare unmodified key cannot open the prompt (the
// editor would swallow it), so validation rewrites it to <s-m-p>, which
// stays clear of the emacs control chords. Modified keys, <c-space>, and
// modal mode are left untouched.
func TestValidateCommandPromptFallback(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    string
		key     string
		wantErr bool
		wantKey string
	}{
		{"standard bare key rewritten", "standard", "p", true, "<s-m-p>"},
		{"emacs bare key rewritten", "emacs", "p", true, "<s-m-p>"},
		{"emacs alt-x (M-x) kept", "emacs", "<a-x>", false, "<a-x>"},
		{"standard shift-meta-p kept", "standard", "<s-m-p>", false, "<s-m-p>"},
		{"emacs ctrl-space kept", "emacs", "<c-space>", false, "<c-space>"},
		{"modal bare key kept", "modal", "p", false, "p"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := map[string]any{
				"editor":  map[string]any{"mode": tc.mode},
				"command": map[string]any{"key": tc.key},
			}
			c := &ideConfig{cfg: cfg, errors: map[string]error{}}
			err := validateCommandPrompt(c, cfg)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantKey,
				cfg["command"].(map[string]any)["key"])
		})
	}
}

// mustParseBindingKey mirrors commandKeyMappings' own key parsing: it
// first tries a two-key handler.Sequence, then falls back to a single
// term key stored in Sequence.First. This keeps the test lookup keyed
// the same way the resolved binding map is.
func mustParseBindingKey(t *testing.T, key string) handler.Sequence {
	t.Helper()
	seq, err := handler.ParseSequence(key)
	if err != nil {
		seq.First, err = term.ParseKey(key)
		require.NoErrorf(t, err, "parse binding key %q", key)
	}
	return seq
}

func mustLegacyModelessConfigFromGit(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"editor": map[string]any{
			"mode": "modeless",
			"modeless": map[string]any{
				"attr":        map[string]any{"fg": "default", "bg": "default"},
				"bar_attr":    map[string]any{"fg": "default", "bg": "default"},
				"search_attr": map[string]any{"fg": "grey", "bg": "yellow"},
			},
			"status_bar": map[string]any{
				"layout": `██▓▒░  {{ .Filepath }}  {{ .GitShortRef }}   {{ .GitDiffAdd | fg "green" }}   {{ .GitDiffDel | fg "red" }} {{ .ShiftRight }} {{ .CursorColumn }}:{{ .CursorLine }}  {{ .TotalLines }} lines  {{ .Language | bold }}  ░▒▓██`,
			},
		},
		"command": map[string]any{
			"key": "<s-m-p>",
			"key_bindings": map[string]any{
				"<m-n>":                 "windownew",
				"<m-o>":                 "lsphover",
				"<m-s>":                 "write",
				"<a-m-s>":               "writeall",
				"<m-w>":                 "tabclose",
				"<m-q>":                 "quit",
				"<m-s-n>":               "windownew",
				"<m-s-w>":               "windowclose",
				"<m-p>":                 "searchfile",
				"<a-g>":                 "searchtext",
				"<m-r>":                 "echo {prompt}jumptoast<space>locals.scm<space>local.definition.type<space>",
				"<s-m-r>":               "searchtype",
				"<m-;>":                 "searchtext",
				"<c-m-p>":               "echo <s-m-p>workspacefocus{wait}<space>",
				"<m-f2>":                "locationtoggle bookmark",
				"<f2>":                  "jumptolocation next bookmark",
				"<s-f2>":                "jumptolocation prev bookmark",
				"<s-m-f2>":              "locationdeleteall bookmark",
				"<a-m-right>":           "tabnext",
				"<a-m-left>":            "tabprevious",
				"<m-f>":                 "searchtext",
				"<m-g>":                 "jumptolocation next search",
				"<s-m-g>":               "jumptolocation prev search",
				"<m-u>":                 "cursorhistory prev",
				"<s-m-u>":               "cursorhistory next",
				"<m-,>":                 "config",
				"<a-m-h>":               "tabmove left",
				"<s-m-]>":               "tabnext",
				"<s-m-[>":               "tabprevious",
				"<c-g>":                 "echo <esc>:",
				"<ctrl-meta-p>":         "echo <esc>:workspacefocus<space>",
				"<alt-meta-down>":       "lspgotodef",
				"<f12>":                 "lspgotodef",
				"<alt-shift-meta-down>": "lspref",
				"<m-j>":                 "",
				"<m-k>":                 "",
				"<m-l>":                 "",
				"<m-h>":                 "",
				"<m-=>":                 "guifontsize increase",
				"<m-->":                 "guifontsize decrease",
				"<m-t>":                 "tabnew",
				"<a-m-l>":               "tabmove right",
				"<m-c>":                 "clipboardcopy",
				"<m-v>":                 "clipboardpaste",
				"<m-y>":                 "echolastcmd",
				"<s-m-h>":               "windowfocus left",
				"<s-m-l>":               "windowfocus right",
				"<s-m-j>":               "windowfocus down",
				"<s-m-k>":               "windowfocus up",
				"<a-s-m-h>":             "windowmove left",
				"<a-s-m-l>":             "windowmove right",
				"<a-s-m-j>":             "windowmove down",
				"<a-s-m-k>":             "windowmove up",
				"<s-m-backspace>":       "windowresize reset",
				"<s-m-+>":               []any{"windowresize max width", "windowresize max height"},
				"<s-m-->":               []any{"windowresize min width", "windowresize min height"},
				"<s-m-up>":              "windowresize increase height",
				"<s-m-down>":            "windowresize decrease height",
				"<s-m-left>":            "windowresize decrease width",
				"<s-m-right>":           "windowresize increase width",
				"<s-m-w>":               "windowclose",
				"<c-m-h>":               "windowdefaultsplit h",
				"<c-m-v>":               "windowdefaultsplit v",
				"<s-m-f>":               "windowtogglemaximize",
				"<m-b>":                 "lspformat",
				"<m-m>":                 "lspformatimports",
				"<a-j>":                 "gitnextchange",
				"<a-k>":                 "gitprevchange",
				"<m-\\\\>":              "searchtext",
				"<m-enter>":             "terminalneworsplit",
				"<s-m-enter>":           "!",
				"gf":                    "editfileoncursor",
				"<m-1>":                 "workspacefocus 1",
				"<m-2>":                 "workspacefocus 2",
				"<m-3>":                 "workspacefocus 3",
				"<m-4>":                 "workspacefocus 4",
				"<m-5>":                 "workspacefocus 5",
				"<m-6>":                 "workspacefocus 6",
				"<m-7>":                 "workspacefocus 7",
				"<m-8>":                 "workspacefocus 8",
				"<m-9>":                 "workspacefocus 9",
			},
		},
		"terminal": map[string]any{"modal": false},
	}
}

func execCommandOutput(name string, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() != 0 {
			return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return "", err
	}
	return stdout.String(), nil
}

func cloneTestMap(m map[string]any) map[string]any {
	return normalizeTestConfig(m).(map[string]any)
}

func normalizeTestConfig(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = normalizeTestConfig(val)
		}
		return out
	case map[any]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k.(string)] = normalizeTestConfig(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = normalizeTestConfig(val)
		}
		return out
	default:
		return t
	}
}

func normalizeLegacyExpected(cfg map[string]any) map[string]any {
	out := cloneTestMap(cfg)
	delete(out, "frame_theme")
	delete(out, "frame_charset_theme")
	delete(out, "frameunion_charset_theme")
	delete(out, "frame_attr_theme")
	delete(out, "focus_frame_charset_theme")
	delete(out, "focus_frame_attr_theme")
	delete(out, "color_theme_1")
	delete(out, "color_theme_2")
	delete(out, "color_theme_3")
	delete(out, "color_theme_4")
	delete(out, "color_theme_5")
	delete(out, "color_theme_6")
	delete(out, "color_theme_7")
	delete(out, "color_theme_8")
	extsIfc, ok := out["extensions"]
	if !ok {
		return out
	}
	exts, ok := extsIfc.(map[string]any)
	if !ok {
		return out
	}
	legacyFile, hasFile := exts["fuzzy_file"].(map[string]any)
	legacyLine, hasLine := exts["fuzzy_line"].(map[string]any)
	_, hasSearch := exts["fuzzy_search"]
	if !hasSearch && !hasFile && !hasLine {
		return out
	}
	fuzzySearch := map[string]any{
		"path": "extension_fuzzy_search",
		"config": map[string]any{
			"syntax": map[string]any{
				"case_sensitive": true,
				"algo":           "fuzzy",
			},
		},
	}
	if cfgMap, ok := exts["fuzzy_search"].(map[string]any); ok {
		fuzzySearch = cloneTestMap(cfgMap)
		if _, ok := fuzzySearch["path"]; !ok {
			fuzzySearch["path"] = "extension_fuzzy_search"
		}
		configMap, ok := fuzzySearch["config"].(map[string]any)
		if !ok {
			configMap = map[string]any{}
			fuzzySearch["config"] = configMap
		}
		if _, ok := configMap["syntax"]; !ok {
			configMap["syntax"] = map[string]any{"case_sensitive": true, "algo": "fuzzy"}
		}
	}
	configMap := fuzzySearch["config"].(map[string]any)
	if hasFile {
		configMap["file"] = cloneTestMap(legacyFile["config"].(map[string]any))
		delete(exts, "fuzzy_file")
	}
	if hasLine {
		configMap["line"] = cloneTestMap(legacyLine["config"].(map[string]any))
		delete(exts, "fuzzy_line")
	}
	exts["fuzzy_search"] = fuzzySearch
	return out
}

func flattenConfigCSV(cfg map[string]any) []string {
	var rows []string
	var walk func(string, any)
	walk = func(prefix string, v any) {
		switch t := v.(type) {
		case map[string]any:
			if len(t) == 0 {
				rows = append(rows, prefix+",{}")
				return
			}
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				next := k
				if prefix != "" {
					next = prefix + "." + k
				}
				walk(next, t[k])
			}
		case []any:
			if len(t) == 0 {
				rows = append(rows, prefix+",[]")
				return
			}
			for i, val := range t {
				walk(prefix+"["+strconv.Itoa(i)+"]", val)
			}
		default:
			rows = append(rows, prefix+","+stringifyTestScalar(t))
		}
	}
	walk("", cfg)
	sort.Strings(rows)
	return rows
}

func stringifyTestScalar(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(t)
	}
}
