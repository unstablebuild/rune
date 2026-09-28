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

package extutil

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestGetColorRGB(t *testing.T) {
	theme := map[string]any{
		"gui": map[string]any{
			"default_theme": "test",
			"themes": map[string]any{
				"other": map[string]any{"green": "#010101"},
				"test": map[string]any{
					"green":      "#98be65",
					"red":        "saddlebrown",
					"background": "#000000",
				},
			},
		},
	}

	tests := []struct {
		name  string
		cfg   map[string]any
		color term.Color
		want  term.Color
	}{
		{
			name:  "theme names the color",
			cfg:   theme,
			color: term.ColorGreen,
			want:  term.NewHexColor(0x98be65),
		},
		{
			name:  "theme names another color",
			cfg:   theme,
			color: term.ColorRed,
			want:  term.GetColor("saddlebrown").TrueColor(),
		},
		{
			name:  "theme leaves the color out",
			cfg:   theme,
			color: term.ColorAqua,
			want:  term.ColorAqua.TrueColor(),
		},
		{
			// term.Color.Name picks one of a slot's aliases at random,
			// so a theme keyed "grey" has to resolve just as reliably
			// as one keyed "gray".
			name: "theme names an alias of the slot",
			cfg: map[string]any{"gui": map[string]any{
				"default_theme": "test",
				"themes": map[string]any{
					"test": map[string]any{"grey": "#282c34"},
				},
			}},
			color: term.ColorGray,
			want:  term.NewHexColor(0x282c34),
		},
		{
			name:  "already an rgb color",
			cfg:   theme,
			color: term.NewHexColor(0x123456),
			want:  term.NewHexColor(0x123456),
		},
		{
			name:  "terminal default",
			cfg:   theme,
			color: term.ColorDefault,
			want:  term.ColorDefault,
		},
		{
			name:  "no config at all",
			cfg:   map[string]any{},
			color: term.ColorGreen,
			want:  term.ColorGreen,
		},
		{
			name:  "no theme named",
			cfg:   map[string]any{"gui": map[string]any{"default_theme": ""}},
			color: term.ColorGreen,
			want:  term.ColorGreen,
		},
		{
			name: "theme named but missing",
			cfg: map[string]any{"gui": map[string]any{
				"default_theme": "gone",
				"themes":        map[string]any{"test": map[string]any{}},
			}},
			color: term.ColorGreen,
			want:  term.ColorGreen,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Repeated because the color-name table is a map: a lookup
			// that depends on its iteration order passes only sometimes.
			for range 50 {
				got, err := GetColorRGB(config.MapConfig(tc.cfg), tc.color)
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

// Resolving a color must not change what any other color in this
// process resolves to, since the palette is shared with everything
// else the extension draws.
func TestGetColorRGBLeavesThePaletteAlone(t *testing.T) {
	before := term.ColorGreen.Hex()
	_, err := GetColorRGB(config.MapConfig(map[string]any{
		"gui": map[string]any{
			"default_theme": "test",
			"themes": map[string]any{
				"test": map[string]any{"green": "#98be65"},
			},
		},
	}), term.ColorGreen)
	require.NoError(t, err)
	assert.EqualValues(t, before, term.ColorGreen.Hex())
}
