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

package extension

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"gopkg.in/yaml.v3"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

// noTheme stands in for a host that names no colour theme, which is
// what leaves the gauge ramps in the palette colours they were
// configured with.
var noTheme = config.MapConfig(map[string]any{})

func TestStatusBarConfigDefaultsWhenAbsent(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{}), noTheme, stubNotifications{})
	assert.Equal(t, defaultStatusBarConfig, got)
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
	}), noTheme, stubNotifications{})

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
	assert.Equal(t, dialoguetui.StatusBarStatusConfig{
		Attrs: term.Attributes{Fg: term.ColorBlack, Bg: term.ColorLime},
		Animation: dialoguetui.StatusBarAnimation{
			Frames: []string{"a", "b", "c"},
			Attrs:  term.Attributes{Fg: term.ColorYellow},
		},
	}, got.Statuses["THINKING"])
	assert.Equal(t, dialoguetui.StatusBarStatusConfig{
		Attrs:     dialoguetui.DefaultStatuses["REASONING"].Attrs,
		Animation: dialoguetui.StatusBarAnimation{Frames: []string{"x", "y"}},
	}, got.Statuses["REASONING"],
		"naming only an animation keeps the shipped attributes")
	assert.Equal(t, dialoguetui.DefaultStatuses["SENDING"],
		got.Statuses["SENDING"],
		"naming one status must not drop the rest of the palette")
	assert.Equal(t, []dialoguetui.StatusBarComponent{
		{Type: dialoguetui.StatusBarStatus, Template: "%s"},
		{Type: dialoguetui.StatusBarVoid},
		{Type: dialoguetui.StatusBarModel, Template: "%s"},
	}, got.Layout)
}

func TestStatusBarConfigInvalidLayoutFallsBack(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{
		"status_bar": map[string]any{
			"layout":      `{{ .Nonsense }}`,
			"gauge_width": 7,
		},
	}), noTheme, stubNotifications{})

	assert.Nil(t, got.Layout)
	assert.Equal(t, 7, got.GaugeWidth)
	assert.True(t, got.Enabled)
}

func TestStatusBarConfigDisabled(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{
		"status_bar": map[string]any{"enabled": false},
	}), noTheme, stubNotifications{})
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
		config.MapConfig(doc.Extensions.RuneAgent.Config), noTheme, stubNotifications{})

	assert.True(t, got.Enabled)
	assert.Equal(t, 18, got.GaugeWidth)
	assert.Equal(t, dialoguetui.DefaultStatusBarBackground, got.BackgroundColor)
	assert.Equal(t, dialoguetui.DefaultStatusBarForeground, got.ForegroundColor)
	assert.NotEmpty(t, got.Layout)
	assert.Equal(t, dialoguetui.DefaultStatuses, got.Statuses,
		"the shipped status palette must match the built-in one")
	assert.Equal(t, dialoguetui.DefaultContextGaugeFill, got.ContextGaugeFill,
		"the shipped context ramp must match the built-in one")
	assert.Equal(t, dialoguetui.DefaultCacheGaugeFill, got.CacheGaugeFill,
		"the shipped cache ramp must match the built-in one")
	reversed := slices.Clone(got.CacheGaugeFill)
	slices.Reverse(reversed)
	assert.Equal(t, reversed, got.ContextGaugeFill,
		"the cache ramp lists the context ramp's stops backwards")
	assert.EqualValues(t, dialoguetui.DefaultGaugeStartRune, got.GaugeStartRune)
	assert.EqualValues(t, dialoguetui.DefaultGaugeEndRune, got.GaugeEndRune)
	assert.Empty(t, got.Shader, "the shipped bar is not animated")
}

func TestStatusBarConfigRejectsUnknownShader(t *testing.T) {
	got := statusBarConfig(config.MapConfig(map[string]any{
		"status_bar": map[string]any{"shader": "nonsense"},
	}), noTheme, stubNotifications{})
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
			}), noTheme, stubNotifications{})
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
	}), stubNotifications{})

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
		}), stubNotifications{})

	require.NotEmpty(t, got.ContextGaugeFill)
	assert.Equal(t, term.NewHexColor(0x98be65), got.ContextGaugeFill[0].Bg)
	assert.Equal(t, term.NewHexColor(0x98be65),
		got.CacheGaugeFill[len(got.CacheGaugeFill)-1].Bg,
		"the cache ramp runs the same stops backwards")
}
