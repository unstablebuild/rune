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

package template_test

import (
	"math"
	"testing"
	"text/template/parse"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/template"
)

func TestParseAction(t *testing.T) {
	const src = `src "file:///tmp/hud.png"`
	// image is an Image with every directive at its default.
	image := template.Image{
		URI:     "file:///tmp/hud.png",
		Width:   template.Length{N: 2},
		Height:  1,
		Reserve: 2,
		Fit:     term.ImageFitContain,
		Alt:     template.DefaultImageAlt,
		TTL:     24 * time.Hour,
	}
	with := func(fn func(*template.Image)) template.Image {
		img := image
		fn(&img)
		return img
	}
	tests := []struct {
		name    string
		action  string
		want    template.Action
		wantErr string
	}{
		{
			name:   "a field with attributes",
			action: `.Status | fg "red" | bold`,
			want: template.Action{Field: "Status", Attributes: term.Attributes{
				Fg: term.ColorRed, Attrs: term.AttrBold,
			}},
		},
		{
			name:   "an image with every directive left out",
			action: `.Image | ` + src,
			want:   template.Action{Field: "Image", Image: image},
		},
		{
			name: "an image with every directive",
			action: `.Image | src "https://example.com/a.png" | width 6 | height 3 | ` +
				`reserve 4 | x_offset -2 | y_offset 1 | x_pixel_offset 5 | y_pixel_offset -3 | fit "fill" | ` +
				`z_index -3 | overflow | alt "logo" | ttl "1h30m" | bg "navy"`,
			want: template.Action{
				Field:      "Image",
				Attributes: term.Attributes{Bg: term.ColorNavy},
				Image: template.Image{
					URI:           "https://example.com/a.png",
					Width:         template.Length{N: 6},
					Height:        3,
					Reserve:       4,
					XOffset:       template.Length{N: -2},
					YOffset:       1,
					XOffsetPixels: 5,
					YOffsetPixels: -3,
					Fit:           term.ImageFitFill,
					ZIndex:        -3,
					Overflow:      true,
					Alt:           "logo",
					TTL:           90 * time.Minute,
				},
			},
		},
		{
			name:   "reserve defaults to a width in cells",
			action: `.Image | ` + src + ` | width 5`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Reserve = template.Length{N: 5}, 5
			})},
		},
		{
			name:   "reserve defaults to nothing for a percentage width",
			action: `.Image | ` + src + ` | width "100%"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Reserve = template.Length{N: 100, Percent: true}, 0
			})},
		},
		{
			name:   "an explicit reserve overrides the width",
			action: `.Image | ` + src + ` | reserve 0 | width 8`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Reserve = template.Length{N: 8}, 0
			})},
		},
		{
			name:   "an empty alt draws nothing",
			action: `.Image | ` + src + ` | alt ""`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Alt = ""
			})},
		},
		{
			name:   "a positive z_index",
			action: `.Image | ` + src + ` | z_index 7`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.ZIndex = 7
			})},
		},
		{
			name:   "an explicit zero z_index",
			action: `.Image | ` + src + ` | z_index 0`,
			want:   template.Action{Field: "Image", Image: image},
		},
		{
			name:   "the last z_index wins",
			action: `.Image | ` + src + ` | z_index 2 | z_index -2`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.ZIndex = -2
			})},
		},
		{
			name:   "the lowest z_index",
			action: `.Image | ` + src + ` | z_index -2147483648`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.ZIndex = math.MinInt32
			})},
		},
		{
			name:   "the highest z_index",
			action: `.Image | ` + src + ` | z_index 2147483647`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.ZIndex = math.MaxInt32
			})},
		},
		{
			name:   "offsets to the right and up",
			action: `.Image | ` + src + ` | x_offset 3 | y_offset -2`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.XOffset, img.YOffset = template.Length{N: 3}, -2
			})},
		},
		{
			name:   "pixel offsets to the left and down",
			action: `.Image | ` + src + ` | x_pixel_offset -7 | y_pixel_offset 4`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.XOffsetPixels, img.YOffsetPixels = -7, 4
			})},
		},
		{
			name:   "pixel offsets leave the cell offsets alone",
			action: `.Image | ` + src + ` | x_offset "50%" | x_pixel_offset 3 | y_offset 1 | y_pixel_offset -2`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.XOffset, img.YOffset = template.Length{N: 50, Percent: true}, 1
				img.XOffsetPixels, img.YOffsetPixels = 3, -2
			})},
		},
		{
			name:   "an x_offset as a percentage",
			action: `.Image | ` + src + ` | x_offset "50%"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.XOffset = template.Length{N: 50, Percent: true}
			})},
		},
		{
			name:   "a negative x_offset as a percentage",
			action: `.Image | ` + src + ` | x_offset "-100%"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.XOffset = template.Length{N: -100, Percent: true}
			})},
		},
		{
			name:   "an overflowing image",
			action: `.Image | ` + src + ` | overflow`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Overflow = true
			})},
		},
		{
			name:   "overflow given twice",
			action: `.Image | overflow | ` + src + ` | overflow`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Overflow = true
			})},
		},
		{name: "an image without src", action: `.Image | width 2`, wantErr: ".Image requires src"},
		{name: "an image directive on another field", action: `.Status | width 2`, wantErr: "width only applies to .Image, not .Status"},
		{name: "an unsupported scheme", action: `.Image | src "ftp://example.com/a.png"`, wantErr: `src: imageuri: unsupported scheme "ftp"`},
		{name: "a relative path", action: `.Image | src "hud.png"`, wantErr: `unsupported scheme ""`},
		{name: "a src that is not a string", action: `.Image | src 3`, wantErr: "src argument must be a string"},
		{name: "a zero width", action: `.Image | ` + src + ` | width 0`, wantErr: "width must be at least 1, got 0"},
		{name: "a fractional width", action: `.Image | ` + src + ` | width 1.5`, wantErr: "width argument must be an integer"},
		{name: "a width over 100%", action: `.Image | ` + src + ` | width "120%"`, wantErr: `percentage from 1% to 100%, got "120%"`},
		{name: "a width without a percent sign", action: `.Image | ` + src + ` | width "50"`, wantErr: `got "50"`},
		{name: "a zero height", action: `.Image | ` + src + ` | height 0`, wantErr: "height must be at least 1"},
		{name: "a negative reserve", action: `.Image | ` + src + ` | reserve -1`, wantErr: "reserve must be at least 0, got -1"},
		{name: "an x_offset over 100%", action: `.Image | ` + src + ` | x_offset "101%"`, wantErr: `x_offset must be a number of cells or a percentage from -100% to 100%, got "101%"`},
		{name: "an x_offset without a percent sign", action: `.Image | ` + src + ` | x_offset "3"`, wantErr: `got "3"`},
		{name: "a fractional x_offset", action: `.Image | ` + src + ` | x_offset 0.5`, wantErr: "x_offset argument must be an integer"},
		{name: "a y_offset as a percentage", action: `.Image | ` + src + ` | y_offset "50%"`, wantErr: "y_offset argument must be an integer"},
		{name: "a fractional x_pixel_offset", action: `.Image | ` + src + ` | x_pixel_offset 0.5`, wantErr: "x_pixel_offset argument must be an integer"},
		{name: "an x_pixel_offset as a percentage", action: `.Image | ` + src + ` | x_pixel_offset "50%"`, wantErr: "x_pixel_offset argument must be an integer"},
		{name: "a y_pixel_offset as a percentage", action: `.Image | ` + src + ` | y_pixel_offset "50%"`, wantErr: "y_pixel_offset argument must be an integer"},
		{name: "a y_pixel_offset without its argument", action: `.Image | ` + src + ` | y_pixel_offset`, wantErr: "y_pixel_offset takes one argument"},
		{name: "a pixel offset on another field", action: `.Status | x_pixel_offset 1`, wantErr: "x_pixel_offset only applies to .Image, not .Status"},
		{name: "an unknown fit", action: `.Image | ` + src + ` | fit "cover"`, wantErr: `fit must be one of "contain", "fill", got "cover"`},
		{name: "a z_index under the lowest", action: `.Image | ` + src + ` | z_index -2147483649`, wantErr: "z_index must be from -2147483648 to 2147483647, got -2147483649"},
		{name: "a z_index over the highest", action: `.Image | ` + src + ` | z_index 2147483648`, wantErr: "z_index must be from -2147483648 to 2147483647, got 2147483648"},
		{name: "a fractional z_index", action: `.Image | ` + src + ` | z_index 1.5`, wantErr: "z_index argument must be an integer"},
		{name: "a z_index as a string", action: `.Image | ` + src + ` | z_index "top"`, wantErr: "z_index argument must be an integer"},
		{name: "a z_index without its argument", action: `.Image | ` + src + ` | z_index`, wantErr: "z_index takes one argument"},
		{name: "a z_index on another field", action: `.Status | z_index 1`, wantErr: "z_index only applies to .Image, not .Status"},
		{name: "overflow with an argument", action: `.Image | ` + src + ` | overflow true`, wantErr: "overflow takes no arguments"},
		{name: "overflow with a string argument", action: `.Image | ` + src + ` | overflow "no"`, wantErr: "overflow takes no arguments"},
		{name: "overflow on another field", action: `.Status | overflow`, wantErr: "overflow only applies to .Image, not .Status"},
		{name: "an unparsable ttl", action: `.Image | ` + src + ` | ttl "soon"`, wantErr: `invalid duration "soon"`},
		{name: "a negative ttl", action: `.Image | ` + src + ` | ttl "-1h"`, wantErr: "ttl must not be negative"},
		{name: "a directive without its argument", action: `.Image | ` + src + ` | height`, wantErr: "height takes one argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trees, err := parse.Parse("layout", "{{ "+tt.action+" }}", "{{", "}}",
				template.AllowedFuncs())
			require.NoError(t, err)
			node := trees["layout"].Root.Nodes[0].(*parse.ActionNode)

			got, err := template.ParseAction(node)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLengthCells(t *testing.T) {
	tests := []struct {
		length template.Length
		total  int
		want   int
	}{
		{template.Length{N: 4}, 80, 4},
		{template.Length{N: 4}, 2, 4},
		{template.Length{N: 100, Percent: true}, 80, 80},
		{template.Length{N: 50, Percent: true}, 81, 40},
		{template.Length{N: 1, Percent: true}, 80, 0},
		{template.Length{N: -3}, 80, -3},
		{template.Length{N: -50, Percent: true}, 81, -40},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.length.Cells(tt.total), "%+v of %d", tt.length, tt.total)
	}
}

func TestImageLayer(t *testing.T) {
	tests := []struct {
		zindex int32
		want   term.ImageLayer
	}{
		{math.MaxInt32, term.ImageLayerAboveText},
		{1, term.ImageLayerAboveText},
		{0, term.ImageLayerAboveText},
		{-1, term.ImageLayerBelowText},
		{template.ZIndexBackground, term.ImageLayerBelowText},
		{template.ZIndexBackground - 1, term.ImageLayerBelowBackground},
		{math.MinInt32, term.ImageLayerBelowBackground},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, template.Image{ZIndex: tt.zindex}.Layer(), "z_index %d", tt.zindex)
	}
}
