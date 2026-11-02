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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/mock/gomock"

	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/sounds"
	"unstable.build/rune/internal/term/gui"
)

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

func TestTelemetryEnabledFromUserConfig(t *testing.T) {
	dir := t.TempDir()
	assert.True(t, ide.TelemetryEnabled(mustLoadConfig(t,
		filepath.Join(dir, "missing.yaml"))))

	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path,
		[]byte("telemetry:\n  enabled: false\n"), 0o644))
	assert.False(t, ide.TelemetryEnabled(mustLoadConfig(t, path)))
}

func TestDefaultSearchIndexing(t *testing.T) {
	cfg := mustLoadConfig(t, filepath.Join(t.TempDir(), "config.star"))
	for _, tt := range []struct {
		section, key string
		want         bool
	}{
		{"search", "regex_enabled", true},
		{"search", "semantic_enabled", false},
		{"workspace", "symboldb", true},
	} {
		t.Run(tt.section+"."+tt.key, func(t *testing.T) {
			section, err := cfg.GetConfig(tt.section)
			require.NoError(t, err)
			got, err := section.GetBool(tt.key)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDefaultConfigShowsTheLogoWithItsIntro(t *testing.T) {
	cfg := mustLoadConfig(t, filepath.Join(t.TempDir(), "config.star"))

	workspace, err := cfg.GetConfig("workspace")
	require.NoError(t, err)
	for key, want := range map[string]string{
		"wallpaper_image_ascii":        "",
		"wallpaper_ascii":              "",
		"wallpaper_density_characters": " ▓▓▓▓",
	} {
		got, err := workspace.GetString(key)
		require.NoError(t, err, key)
		assert.Equal(t, want, got, key)
	}
	image, err := workspace.GetMap("wallpaper_image")
	require.NoError(t, err)
	assert.Equal(t, map[string]any{
		"path": "", "width": "60%", "height": "60%", "fit": "contain",
	}, image)

	animations, err := cfg.GetConfig("animations")
	require.NoError(t, err)
	intro, err := animations.GetBool("intro")
	require.NoError(t, err)
	assert.True(t, intro)
}

func TestWallpaperImageOverridesTheDefaultSection(t *testing.T) {
	section := func(path string) map[string]any {
		return map[string]any{"path": path, "width": "60%", "height": "60%", "fit": "contain"}
	}
	tests := []struct {
		name   string
		file   string
		config string
		want   any
	}{
		{
			name:   "a path in YAML",
			file:   "config.yaml",
			config: "workspace:\n  wallpaper_image: ~/a.png\n",
			want:   "~/a.png",
		},
		{
			name:   "a section in YAML",
			file:   "config.yaml",
			config: "workspace:\n  wallpaper_image:\n    path: ~/a.png\n    fit: cover\n",
			want:   map[string]any{"path": "~/a.png", "width": "60%", "height": "60%", "fit": "cover"},
		},
		{
			name:   "a path in Starlark",
			file:   "config.star",
			config: "config[\"workspace\"][\"wallpaper_image\"] = \"~/a.png\"\n",
			want:   "~/a.png",
		},
		{
			name:   "the path of the section in Starlark",
			file:   "config.star",
			config: "config[\"workspace\"][\"wallpaper_image\"][\"path\"] = \"~/a.png\"\n",
			want:   section("~/a.png"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.file)
			require.NoError(t, os.WriteFile(path, []byte(tt.config), 0o644))
			workspace, err := mustLoadConfig(t, path).GetConfig("workspace")
			require.NoError(t, err)
			var got any
			workspace.Iterate(func(k string, v any) {
				if k == "wallpaper_image" {
					got = v
				}
			})
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDeprecatedWallpaperKeyIsKeptOverTheDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("workspace:\n  wallpaper: art\n"), 0o644))

	workspace, err := mustLoadConfig(t, path).GetConfig("workspace")
	require.NoError(t, err)
	old, err := workspace.GetString("wallpaper")
	require.NoError(t, err)
	assert.Equal(t, "art", old)
	ascii, err := workspace.GetString("wallpaper_ascii")
	require.NoError(t, err)
	assert.Empty(t, ascii, "the empty default defers to the deprecated key")
}

func mustLoadConfig(t *testing.T, path string) config.Config {
	t.Helper()
	cfg, err := ide.Config(path, runeDefaultConfig())
	require.NoError(t, err)
	return cfg
}

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
		{"case-insensitive", config.MapConfig(map[string]any{"alt_modifier": "Right"}), gui.AltModifierRight},
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

func TestGetCursorEffect(t *testing.T) {
	dir := t.TempDir()
	shaderPath := filepath.Join(dir, "cursor.kage")
	require.NoError(t, os.WriteFile(shaderPath, []byte("package main"), 0o644))
	t.Setenv("RUNE_TEST_SHADER_DIR", dir)
	cursor := func(v any) config.Config {
		return config.MapConfig(map[string]any{
			"animations": map[string]any{"cursor": v},
		})
	}

	cases := []struct {
		name         string
		cfg          config.Config
		wantSrc      []byte
		wantDuration time.Duration
		wantNotify   bool
	}{
		{name: "no animations is off", cfg: config.MapConfig(map[string]any{})},
		{
			name: "absent is off",
			cfg:  config.MapConfig(map[string]any{"animations": map[string]any{}}),
		},
		{
			name: "animations of the wrong type is off",
			cfg:  config.MapConfig(map[string]any{"animations": "off"}),
		},
		{name: "false is off", cfg: cursor(false)},
		{
			name:         "true plays the built-in trail",
			cfg:          cursor(true),
			wantSrc:      gui.CursorTrailShader,
			wantDuration: defaultCursorEffectDuration,
		},
		{
			name:       "cursor of the wrong type",
			cfg:        cursor("yes"),
			wantNotify: true,
		},
		{
			name: "disabled",
			cfg:  cursor(map[string]any{"enabled": false, "duration": "1s"}),
		},
		{
			name:       "enabled of the wrong type",
			cfg:        cursor(map[string]any{"enabled": "yes"}),
			wantNotify: true,
		},
		{
			name:         "enabled",
			cfg:          cursor(map[string]any{"enabled": true}),
			wantSrc:      gui.CursorTrailShader,
			wantDuration: defaultCursorEffectDuration,
		},
		{
			name:         "a section is on unless disabled",
			cfg:          cursor(map[string]any{}),
			wantSrc:      gui.CursorTrailShader,
			wantDuration: defaultCursorEffectDuration,
		},
		{
			name:         "duration",
			cfg:          cursor(map[string]any{"duration": "400ms"}),
			wantSrc:      gui.CursorTrailShader,
			wantDuration: 400 * time.Millisecond,
		},
		{
			name:       "invalid duration",
			cfg:        cursor(map[string]any{"duration": "soon"}),
			wantNotify: true,
		},
		{
			name:       "non-positive duration",
			cfg:        cursor(map[string]any{"duration": "0s"}),
			wantNotify: true,
		},
		{
			name:         "shader file with variables expanded",
			cfg:          cursor(map[string]any{"shader": "$RUNE_TEST_SHADER_DIR/cursor.kage"}),
			wantSrc:      []byte("package main"),
			wantDuration: defaultCursorEffectDuration,
		},
		{
			name:       "missing shader file",
			cfg:        cursor(map[string]any{"shader": filepath.Join(dir, "missing.kage")}),
			wantNotify: true,
		},
		{
			name:       "shader of the wrong type",
			cfg:        cursor(map[string]any{"shader": 42}),
			wantNotify: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			b := browsertest.NewMockBrowser(ctrl)
			if tc.wantNotify {
				b.EXPECT().Notify(gomock.Any(), gomock.Any(), gomock.Any()).Return("", nil)
			}
			src, duration := getCursorEffect(b, tc.cfg)
			assert.Equal(t, tc.wantSrc, src)
			assert.Equal(t, tc.wantDuration, duration)
		})
	}
}

func TestGetCursorEffectFromUserConfig(t *testing.T) {
	cases := []struct {
		name         string
		file         string
		config       string
		wantSrc      []byte
		wantDuration time.Duration
	}{
		{name: "off by default", file: "config.star"},
		{
			name:         "enabled in Starlark",
			file:         "config.star",
			config:       "config[\"animations\"][\"cursor\"][\"enabled\"] = True\n",
			wantSrc:      gui.CursorTrailShader,
			wantDuration: defaultCursorEffectDuration,
		},
		{
			name:         "true in YAML",
			file:         "config.yaml",
			config:       "animations:\n  cursor: true\n",
			wantSrc:      gui.CursorTrailShader,
			wantDuration: defaultCursorEffectDuration,
		},
		{
			name:         "enabled with a duration in YAML",
			file:         "config.yaml",
			config:       "animations:\n  cursor:\n    enabled: true\n    duration: 400ms\n",
			wantSrc:      gui.CursorTrailShader,
			wantDuration: 400 * time.Millisecond,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.file)
			require.NoError(t, os.WriteFile(path, []byte(tc.config), 0o644))

			ctrl := gomock.NewController(t)
			src, duration := getCursorEffect(browsertest.NewMockBrowser(ctrl),
				mustLoadConfig(t, path))
			assert.Equal(t, tc.wantSrc, src)
			assert.Equal(t, tc.wantDuration, duration)
		})
	}
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

func TestGetGUISoundVolume(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		want float64
		warn bool
	}{
		{name: "absent", cfg: map[string]any{}, want: 1},
		{name: "in range", cfg: map[string]any{"sound_volume": 0.4}, want: 0.4},
		{name: "mute", cfg: map[string]any{"sound_volume": 0}, want: 0},
		{name: "above range", cfg: map[string]any{"sound_volume": 3}, want: 1, warn: true},
		{name: "below range", cfg: map[string]any{"sound_volume": -1}, want: 0, warn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			b := browsertest.NewMockBrowser(ctrl)
			if tc.warn {
				b.EXPECT().Notify(browserapi.LevelWarn, gomock.Any(), gomock.Any()).
					Return("", nil)
			}
			assert.Equal(t, tc.want, getGUISoundVolume(b, config.MapConfig(tc.cfg)))
		})
	}
}

func TestSoundDefaults(t *testing.T) {
	cfg := mustLoadConfig(t, filepath.Join(t.TempDir(), "missing.yaml"))
	guiCfg, err := cfg.GetConfig("gui")
	require.NoError(t, err)
	volume, err := guiCfg.GetFloat("sound_volume")
	require.NoError(t, err)
	assert.Equal(t, 1.0, volume)

	terminalCfg, err := cfg.GetConfig("terminal")
	require.NoError(t, err)
	bell, err := terminalCfg.GetString("bell")
	require.NoError(t, err)
	assert.Equal(t, sounds.Warning, bell)

	entries, err := cfg.GetMap("sounds")
	require.NoError(t, err)
	assert.Empty(t, entries)
}
