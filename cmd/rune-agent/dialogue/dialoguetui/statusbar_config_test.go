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

package dialoguetui

import (
	"bytes"
	"context"
	"fmt"
	stdimage "image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/term"
	"gopkg.in/yaml.v3"
)

// noTheme stands in for a host that names no colour theme, which is
// what leaves the gauge ramps in the palette colours they were
// configured with.
var noTheme = config.MapConfig(map[string]any{})

type nopNotifications struct{}

func (nopNotifications) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopNotifications) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopNotifications) UpdateNotificationProgress(_, _ string, _, _ int64) error { return nil }

func TestStatusBarConfigDefaultsWhenAbsent(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{}), noTheme, nopNotifications{})
	assert.Equal(t, DefaultStatusBarConfig(), got)
}

func TestStatusBarConfigReadsEveryKey(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{
		"status_bar": map[string]any{
			"enabled":     true,
			"layout":      `{{ .Status }}{{ .ShiftRight }}{{ .Model }}`,
			"gauge_width": 12,
			"background_attr": map[string]any{
				"bg": "blue",
				"fg": "olive",
			},
			"gauge_empty_attr": map[string]any{
				"fg": "gray",
			},
			"gauge_start_rune": "[",
			"gauge_end_rune":   "]",
			"shader":           "burn",
			"gauge_cap_attr": map[string]any{
				"fg": "yellow", "bg": "navy",
			},
			"context_gauge_fill_attrs": []any{
				map[string]any{"fg": "black", "bg": "aqua"},
				map[string]any{"fg": "black", "bg": "lime"},
			},
			"cache_gauge_fill_attrs": []any{
				map[string]any{"fg": "black", "bg": "lime"},
				map[string]any{"fg": "black", "bg": "aqua"},
			},
			"status": map[string]any{
				"THINKING": map[string]any{
					"attr": map[string]any{"fg": "black", "bg": "lime"},
					"animation": map[string]any{
						"ch":   "abc",
						"attr": map[string]any{"fg": "yellow"},
					},
				},
				"REASONING": map[string]any{
					"animation": map[string]any{"ch": "xy"},
				},
			},
		},
	}), noTheme, nopNotifications{})

	assert.True(t, got.Enabled)
	assert.Equal(t, 12, got.GaugeWidth)
	assert.Equal(t, term.ColorBlue, got.BackgroundColor)
	assert.Equal(t, term.ColorOlive, got.ForegroundColor)
	assert.Equal(t, term.Attributes{Fg: term.ColorGray}, got.GaugeEmptyAttr)
	assert.EqualValues(t, '[', got.GaugeStartRune)
	assert.EqualValues(t, ']', got.GaugeEndRune)
	assert.Equal(t, "burn", got.Shader)
	assert.Equal(t, term.Attributes{Fg: term.ColorYellow, Bg: term.ColorNavy},
		got.GaugeCapAttr)
	assert.Equal(t, []term.Attributes{
		{Fg: term.ColorBlack, Bg: term.ColorAqua},
		{Fg: term.ColorBlack, Bg: term.ColorLime},
	}, got.ContextGaugeFill)
	assert.Equal(t, []term.Attributes{
		{Fg: term.ColorBlack, Bg: term.ColorLime},
		{Fg: term.ColorBlack, Bg: term.ColorAqua},
	}, got.CacheGaugeFill)
	assert.Equal(t, StatusBarStatusConfig{
		Attrs: term.Attributes{Fg: term.ColorBlack, Bg: term.ColorLime},
		Animation: StatusBarAnimation{
			Frames: []string{"a", "b", "c"},
			Attrs:  term.Attributes{Fg: term.ColorYellow},
		},
	}, got.Statuses["THINKING"])
	assert.Equal(t, StatusBarStatusConfig{
		Attrs:     DefaultStatuses["REASONING"].Attrs,
		Animation: StatusBarAnimation{Frames: []string{"x", "y"}},
	}, got.Statuses["REASONING"],
		"naming only an animation keeps the shipped attributes")
	assert.Equal(t, DefaultStatuses["SENDING"],
		got.Statuses["SENDING"],
		"naming one status must not drop the rest of the palette")
	assert.Equal(t, []StatusBarComponent{
		{Type: StatusBarStatus, Template: "%s"},
		{Type: StatusBarVoid},
		{Type: StatusBarModel, Template: "%s"},
	}, got.Layout)
}

func TestStatusBarConfigInvalidLayoutFallsBack(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{
		"status_bar": map[string]any{
			"layout":      `{{ .Nonsense }}`,
			"gauge_width": 7,
		},
	}), noTheme, nopNotifications{})

	assert.Nil(t, got.Layout)
	assert.Equal(t, 7, got.GaugeWidth)
	assert.True(t, got.Enabled)
}

func TestStatusBarConfigDisabled(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{
		"status_bar": map[string]any{"enabled": false},
	}), noTheme, nopNotifications{})
	assert.False(t, got.Enabled)
}

func TestShippedStatusBarConfigParses(t *testing.T) {
	data, err := os.ReadFile(agentYAMLPath)
	require.NoError(t, err)

	var doc struct {
		Extensions struct {
			RuneAgent struct {
				Config map[string]any `yaml:"config"`
			} `yaml:"rune-agent"`
		} `yaml:"extensions"`
	}
	require.NoError(t, yaml.Unmarshal(data, &doc))

	got := statusBarConfig(
		config.MapConfig(doc.Extensions.RuneAgent.Config), noTheme, nopNotifications{})

	assert.True(t, got.Enabled)
	assert.Equal(t, 18, got.GaugeWidth)
	assert.Equal(t, DefaultStatusBarBackground, got.BackgroundColor)
	assert.Equal(t, DefaultStatusBarForeground, got.ForegroundColor)
	assert.NotEmpty(t, got.Layout)
	assert.Equal(t, DefaultStatuses, got.Statuses,
		"the shipped status palette must match the built-in one")
	assert.Equal(t, DefaultContextGaugeFill, got.ContextGaugeFill,
		"the shipped context ramp must match the built-in one")
	assert.Equal(t, DefaultCacheGaugeFill, got.CacheGaugeFill,
		"the shipped cache ramp must match the built-in one")
	reversed := slices.Clone(got.CacheGaugeFill)
	slices.Reverse(reversed)
	assert.Equal(t, reversed, got.ContextGaugeFill,
		"the cache ramp lists the context ramp's stops backwards")
	assert.EqualValues(t, DefaultGaugeStartRune, got.GaugeStartRune)
	assert.EqualValues(t, DefaultGaugeEndRune, got.GaugeEndRune)
	assert.Empty(t, got.Shader, "the shipped bar is not animated")
}

func TestStatusBarConfigRejectsUnknownShader(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{
		"status_bar": map[string]any{"shader": "nonsense"},
	}), noTheme, nopNotifications{})
	assert.Empty(t, got.Shader)
}

func TestStatusBarConfigShaderCadence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		block    map[string]any
		wantFPS  int
		wantLoop time.Duration
	}{
		{
			name:    "named",
			block:   map[string]any{"shader_fps": 12, "shader_loop": "3s"},
			wantFPS: 12, wantLoop: 3 * time.Second,
		},
		{
			name:  "absent",
			block: map[string]any{},
		},
		{
			name:  "unparsable loop is ignored",
			block: map[string]any{"shader_loop": "soon"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := statusBarConfig(config.MapConfig(map[string]any{
				"status_bar": tc.block,
			}), noTheme, nopNotifications{})
			assert.Equal(t, tc.wantFPS, got.ShaderFPS)
			assert.Equal(t, tc.wantLoop, got.ShaderLoop)
		})
	}
}

func TestStatusBarConfigResolvesGaugeRampAgainstTheme(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{
		"status_bar": map[string]any{
			"context_gauge_fill_attrs": []any{
				map[string]any{"fg": "black", "bg": "green"},
				map[string]any{"fg": "black", "bg": "aqua"},
			},
		},
	}), config.MapConfig(map[string]any{
		"gui": map[string]any{
			"default_theme": "test",
			"themes": map[string]any{
				"test":  map[string]any{"green": "#98be65"},
				"other": map[string]any{"green": "#010101"},
			},
		},
	}), nopNotifications{})

	assert.Equal(t, []term.Attributes{
		{Fg: term.ColorBlack.TrueColor(), Bg: term.NewHexColor(0x98be65)},
		{Fg: term.ColorBlack.TrueColor(), Bg: term.ColorAqua.TrueColor()},
	}, got.ContextGaugeFill)
}

func TestStatusBarConfigResolvesBuiltInRampAgainstTheme(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{}),
		config.MapConfig(map[string]any{
			"gui": map[string]any{
				"default_theme": "test",
				"themes": map[string]any{
					"test": map[string]any{"green": "#98be65"},
				},
			},
		}), nopNotifications{})

	require.NotEmpty(t, got.ContextGaugeFill)
	assert.Equal(t, term.NewHexColor(0x98be65), got.ContextGaugeFill[0].Bg)
	assert.Equal(t, term.NewHexColor(0x98be65),
		got.CacheGaugeFill[len(got.CacheGaugeFill)-1].Bg,
		"the cache ramp runs the same stops backwards")
}

func TestRefreshStatusBarImagesReadsImagesOverwrittenInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ornament.png")
	writePNG := func(w, h int) {
		t.Helper()
		var buf bytes.Buffer
		require.NoError(t, png.Encode(&buf, stdimage.NewRGBA(stdimage.Rect(0, 0, w, h))))
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
	}
	uri := (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	layout, err := ParseStatusBarLayout(
		fmt.Sprintf(`{{ .Image | src %q }}{{ .Model }}{{ .Image | src %[1]q }}`, uri))
	require.NoError(t, err)
	storage := storagestub.NewInMemoryService()

	writePNG(1, 1)
	first := refreshStatusBarImages(context.Background(), layout, storage)
	require.Contains(t, first, uri)
	assert.Equal(t, stdimage.Rect(0, 0, 1, 1), first[uri].Bounds())

	writePNG(3, 2)
	again := refreshStatusBarImages(context.Background(), layout, storage)
	require.Len(t, again, 1)
	assert.Equal(t, stdimage.Rect(0, 0, 3, 2), again[uri].Bounds(), "the cached copy is dropped")

	require.NoError(t, os.Remove(path))
	assert.Empty(t, refreshStatusBarImages(context.Background(), layout, storage),
		"an image that does not load is left out")
}
