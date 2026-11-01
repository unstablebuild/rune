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

package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/component"
	tconfig "unstable.build/rune/internal/config"
)

func TestGetFrameCharset(t *testing.T) {
	def := component.FrameCharSetDefault()
	def.Graphics = component.FrameCharSetGraphics{
		TopLeft:  "file:///def/tl.png",
		TopRight: "file:///def/tr.png",
	}
	allGraphics := component.FrameCharSetGraphics{
		TopLeft:          "file:///img/tl.png",
		TopRight:         "file:///img/tr.png",
		BottomLeft:       "file:///img/bl.png",
		BottomRight:      "file:///img/br.png",
		HorizontalTop:    "https://example.com/top.png",
		HorizontalBottom: "https://example.com/bottom.png",
		VerticalLeft:     "http://example.com/left.png",
		VerticalRight:    "http://example.com/right.png",
	}
	withRunes := func(cs component.FrameCharSet) component.FrameCharSet {
		cs.TopLeft, cs.HorizontalTop = '╭', '═'
		return cs
	}
	withGraphics := func(g component.FrameCharSetGraphics) component.FrameCharSet {
		cs := def
		cs.Graphics = g
		return cs
	}

	tests := []struct {
		name    string
		cfg     map[string]any
		want    component.FrameCharSet
		wantErr []string
	}{
		{
			name: "runes only keep the default graphics",
			cfg:  map[string]any{"topleft": "╭", "horizontaltop": "═"},
			want: withRunes(def),
		},
		{
			name: "every graphics slot",
			cfg: map[string]any{"graphics": map[string]any{
				"topleft":          "file:///img/tl.png",
				"topright":         "file:///img/tr.png",
				"bottomleft":       "file:///img/bl.png",
				"bottomright":      "file:///img/br.png",
				"horizontaltop":    "https://example.com/top.png",
				"horizontalbottom": "https://example.com/bottom.png",
				"verticalleft":     "http://example.com/left.png",
				"verticalright":    "http://example.com/right.png",
			}},
			want: withGraphics(allGraphics),
		},
		{
			name: "some slots keep the rest from the default",
			cfg: map[string]any{
				"topleft": "╭", "horizontaltop": "═",
				"graphics": map[string]any{
					"topleft":       "file:///img/tl.png",
					"verticalright": "file:///img/right.png",
				},
			},
			want: func() component.FrameCharSet {
				cs := withRunes(def)
				cs.Graphics.TopLeft = "file:///img/tl.png"
				cs.Graphics.VerticalRight = "file:///img/right.png"
				return cs
			}(),
		},
		{
			name: "an empty string clears a slot",
			cfg:  map[string]any{"graphics": map[string]any{"topright": ""}},
			want: withGraphics(component.FrameCharSetGraphics{TopLeft: "file:///def/tl.png"}),
		},
		{
			name: "an unsupported scheme fails",
			cfg: map[string]any{
				"topleft": "╭",
				"graphics": map[string]any{
					"topleft":     "ftp://example.com/tl.png",
					"topright":    "file:///img/tr.png",
					"bottomright": "/no/scheme.png",
				},
			},
			want: def,
			wantErr: []string{
				`graphics.topleft: imageuri: unsupported scheme "ftp"`,
				`graphics.bottomright: imageuri: unsupported scheme ""`,
			},
		},
		{
			name:    "a slot that is not a string fails",
			cfg:     map[string]any{"graphics": map[string]any{"verticalleft": 1}},
			want:    def,
			wantErr: []string{"graphics.verticalleft: " + config.ErrInvalidType.Error()},
		},
		{
			name:    "graphics that is not a map fails",
			cfg:     map[string]any{"graphics": "file:///img/tl.png"},
			want:    def,
			wantErr: []string{"graphics: " + config.ErrInvalidType.Error()},
		},
		{
			name: "overlay mode",
			cfg:  map[string]any{"graphics": map[string]any{"mode": "overlay"}},
			want: func() component.FrameCharSet {
				cs := def
				cs.Graphics.Overlay = true
				return cs
			}(),
		},
		{
			name: "replace mode",
			cfg:  map[string]any{"graphics": map[string]any{"mode": "replace"}},
			want: def,
		},
		{
			name: "an unknown mode fails with the slots",
			cfg: map[string]any{"graphics": map[string]any{
				"mode":    "underlay",
				"topleft": "ftp://example.com/tl.png",
			}},
			want: def,
			wantErr: []string{
				`graphics.mode: expected "replace" or "overlay", got "underlay"`,
				`graphics.topleft: imageuri: unsupported scheme "ftp"`,
			},
		},
		{
			name:    "a mode that is not a string fails",
			cfg:     map[string]any{"graphics": map[string]any{"mode": true}},
			want:    def,
			wantErr: []string{"graphics.mode: " + config.ErrInvalidType.Error()},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := config.MapConfig(map[string]any{"frame_charset": tt.cfg})
			got, err := tconfig.GetFrameCharset(c, "frame_charset", def)
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)
			}
			for _, want := range tt.wantErr {
				assert.ErrorContains(t, err, want)
			}
			assert.Equal(t, tt.want, got)
		})
	}

	t.Run("a missing mode keeps the default's", func(t *testing.T) {
		overlay := def
		overlay.Graphics.Overlay = true
		c := config.MapConfig(map[string]any{"frame_charset": map[string]any{
			"graphics": map[string]any{"topleft": "file:///img/tl.png"},
		}})
		got, err := tconfig.GetFrameCharset(c, "frame_charset", overlay)
		require.NoError(t, err)
		assert.True(t, got.Graphics.Overlay)
		assert.Equal(t, "file:///img/tl.png", got.Graphics.TopLeft)
	})

	t.Run("a missing key returns ErrNotFound", func(t *testing.T) {
		got, err := tconfig.GetFrameCharset(config.MapConfig(map[string]any{}), "frame_charset", def)
		assert.ErrorIs(t, err, config.ErrNotFound)
		assert.Equal(t, def, got)
	})
}

func TestGetGraphics(t *testing.T) {
	type slots struct {
		Overlay  bool
		Up, Down string
	}
	def := slots{Up: "file:///def/up.png", Down: "file:///def/down.png"}

	tests := []struct {
		name    string
		cfg     map[string]any
		want    slots
		wantErr []string
	}{
		{
			name: "a missing table keeps every slot",
			cfg:  map[string]any{},
			want: def,
		},
		{
			name: "the slots it names change",
			cfg: map[string]any{"art": map[string]any{
				"mode": "overlay",
				"up":   "https://example.com/up.png",
			}},
			want: slots{Overlay: true, Up: "https://example.com/up.png", Down: def.Down},
		},
		{
			name: "an empty string clears a slot",
			cfg:  map[string]any{"art": map[string]any{"down": ""}},
			want: slots{Up: def.Up},
		},
		{
			name: "keys that are not slots are ignored",
			cfg:  map[string]any{"art": map[string]any{"outset": map[string]any{"left": 1}}},
			want: def,
		},
		{
			name: "an invalid entry changes nothing",
			cfg: map[string]any{"art": map[string]any{
				"mode": "blend",
				"up":   "file:///img/up.png",
				"down": "ftp://example.com/down.png",
			}},
			want: def,
			wantErr: []string{
				`art.mode: expected "replace" or "overlay", got "blend"`,
				`art.down: imageuri: unsupported scheme "ftp"`,
			},
		},
		{
			name:    "a table that is not a map fails",
			cfg:     map[string]any{"art": true},
			want:    def,
			wantErr: []string{"art: " + config.ErrInvalidType.Error()},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := def
			err := tconfig.GetGraphics(config.MapConfig(tt.cfg), "art", &got.Overlay,
				tconfig.GraphicsSlot{Key: "up", URI: &got.Up},
				tconfig.GraphicsSlot{Key: "down", URI: &got.Down},
			)
			if len(tt.wantErr) == 0 {
				require.NoError(t, err)
			}
			for _, want := range tt.wantErr {
				assert.ErrorContains(t, err, want)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
