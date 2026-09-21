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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/mock/gomock"

	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/term/gui"
)

// TestIDEConfigOverlaySubscriptResolvesDefaultTree is a regression test for
// ide.Config decoding user configs against a bare `config = {}` default
// instead of the full rune.star tree. An overlay-style user config that
// mutates a nested default key (config["terminal"]["initial_reservoir"] = 2)
// used to fail with `key "terminal" not in dict` on the gui.env load paths.
// Requiring the DefaultConfig argument keeps every ide.Config caller on the
// same baseline the running IDE uses.
func TestIDEConfigOverlaySubscriptResolvesDefaultTree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.star")
	require.NoError(t, os.WriteFile(path,
		[]byte("config[\"terminal\"][\"initial_reservoir\"] = 2\n"), 0o644))

	cfg, err := ide.Config(path, runeDefaultConfig())
	require.NoError(t, err)

	term, err := cfg.GetConfig("terminal")
	require.NoError(t, err)
	got, err := term.GetInt("initial_reservoir")
	require.NoError(t, err)
	assert.Equal(t, 2, got)
}

// TestTelemetryEnabledFromUserConfig covers the opt-out path newAPIClient
// depends on: telemetry ships enabled, and a user config that sets
// telemetry.enabled to false turns it off.
func TestTelemetryEnabledFromUserConfig(t *testing.T) {
	dir := t.TempDir()
	assert.True(t, ide.TelemetryEnabled(mustLoadConfig(t,
		filepath.Join(dir, "missing.yaml"))))

	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("telemetry:\n  enabled: false\n"), 0o644))
	assert.False(t, ide.TelemetryEnabled(mustLoadConfig(t, path)))
}

func mustLoadConfig(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := ide.Config(path, runeDefaultConfig())
	require.NoError(t, err)
	return cfg
}

// TestDefaultQuickMenuButtons guards the quick menu shipped in rune.star
// against an entry that validation rejects, which would silently drop a
// button at runtime. It compares parsed buttons against raw entries
// rather than pinning the list, so editing the menu does not fail here.
func TestDefaultQuickMenuButtons(t *testing.T) {
	cfg, err := ide.Config(filepath.Join(t.TempDir(), "config.star"),
		runeDefaultConfig())
	require.NoError(t, err)

	guiCfg, err := cfg.GetConfig("gui")
	require.NoError(t, err)
	entries, err := guiCfg.GetSlice("quick_menu")
	require.NoError(t, err)
	require.NotEmpty(t, entries)

	buttons := ide.QuickMenuButtons(cfg)
	assert.Len(t, buttons, len(entries),
		"every shipped quick menu entry must survive validation")

	seen := make(map[string]bool, len(buttons))
	for _, button := range buttons {
		assert.NotEmpty(t, button.Symbol, "%q has no symbol", button.ID())
		assert.NotEmpty(t, button.Title, "%q has no title", button.ID())
		assert.False(t, seen[button.ID()], "duplicate command %q", button.ID())
		seen[button.ID()] = true
	}
}

func TestGetGUIKeyMapping(t *testing.T) {
	t.Run("parses valid mappings", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		b := browsertest.NewMockBrowser(ctrl)

		cfg := config.MapConfig(map[string]any{
			"key_mapping": map[string]any{
				"<capslock>": "<esc>",
				"<numlock>":  "a",
			},
		})

		got := getGUIKeyMapping(b, cfg)
		want := map[term.KeyComb]term.KeyComb{
			{Key: term.KeyCapsLock}: {Key: term.KeyEsc},
			{Key: term.KeyNumLock}:  {Ch: 'a'},
		}
		assert.Equal(t, want, got)
	})

	t.Run("returns nil when absent", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		b := browsertest.NewMockBrowser(ctrl)

		got := getGUIKeyMapping(b, config.MapConfig(map[string]any{}))
		assert.Nil(t, got)
	})

	t.Run("skips invalid entries and notifies", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		b := browsertest.NewMockBrowser(ctrl)
		// One report for the bad source, one for the bad target, one for
		// the non-string value.
		b.EXPECT().Notify(gomock.Any(), gomock.Any(), gomock.Any()).
			Return("", nil).Times(3)

		cfg := config.MapConfig(map[string]any{
			"key_mapping": map[string]any{
				"<capslock>":   "<esc>",
				"<not-a-key>":  "<esc>",
				"<numlock>":    "<also-bad>",
				"<scrolllock>": 42,
			},
		})

		got := getGUIKeyMapping(b, cfg)
		want := map[term.KeyComb]term.KeyComb{
			{Key: term.KeyCapsLock}: {Key: term.KeyEsc},
		}
		assert.Equal(t, want, got)
	})
}

func TestGetGUIAltModifier(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want gui.AltModifier
	}{
		{"absent reserves neither", config.MapConfig(map[string]any{}), gui.AltModifierNone},
		{"none", config.MapConfig(map[string]any{"alt_modifier": "none"}), gui.AltModifierNone},
		{"right", config.MapConfig(map[string]any{"alt_modifier": "right"}), gui.AltModifierRight},
		{"left", config.MapConfig(map[string]any{"alt_modifier": "left"}), gui.AltModifierLeft},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			b := browsertest.NewMockBrowser(ctrl)
			require.Equal(t, tc.want, getGUIAltModifier(b, tc.cfg))
		})
	}

	t.Run("invalid value reserves neither", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		b := browsertest.NewMockBrowser(ctrl)
		b.EXPECT().Notify(gomock.Any(), gomock.Any(), gomock.Any()).Return("", nil)

		got := getGUIAltModifier(b, config.MapConfig(map[string]any{"alt_modifier": "middle"}))
		require.Equal(t, gui.AltModifierNone, got)
	})
}

// TestGetGUIAltModifierUnconfigured asserts the contract through the real
// config loader: a user config that never set the key reserves neither.
func TestGetGUIAltModifierUnconfigured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("gui:\n  default_theme: romero\n"), 0o644))

	guiCfg, err := mustLoadConfig(t, path).GetConfig("gui")
	require.NoError(t, err)

	ctrl := gomock.NewController(t)
	b := browsertest.NewMockBrowser(ctrl)
	require.Equal(t, gui.AltModifierNone, getGUIAltModifier(b, guiCfg))
}

func TestGetGUIFontSize(t *testing.T) {
	t.Run("returns 0 when absent to select DPI-aware default", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		b := browsertest.NewMockBrowser(ctrl)

		got := getGUIFontSize(b, config.MapConfig(map[string]any{}))
		assert.Equal(t, float64(0), got)
	})

	t.Run("returns configured size when set", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		b := browsertest.NewMockBrowser(ctrl)

		cfg := config.MapConfig(map[string]any{"font_size": 15.0})
		got := getGUIFontSize(b, cfg)
		assert.Equal(t, float64(15), got)
	})
}
