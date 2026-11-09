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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/template"
)

func TestImageValidate(t *testing.T) {
	const cells, pixels = template.MaxImageCells, template.MaxImagePixelOffset
	tests := []struct {
		name    string
		spec    func(*template.Image)
		wantErr string
	}{
		{name: "every field in range"},
		{name: "an https URI", spec: func(img *template.Image) { img.URI = "https://example.com/a.png" }},
		{name: "an upper-case scheme", spec: func(img *template.Image) { img.URI = "FILE:///a.png" }},
		{name: "the narrowest width", spec: func(img *template.Image) { img.Width = template.Length{N: 1} }},
		{name: "the widest width", spec: func(img *template.Image) { img.Width = template.Length{N: cells} }},
		{name: "the narrowest percentage", spec: func(img *template.Image) { img.Width = template.Length{N: 1, Percent: true} }},
		{name: "the widest percentage", spec: func(img *template.Image) { img.Width = template.Length{N: 100, Percent: true} }},
		{name: "the tallest height", spec: func(img *template.Image) { img.Height = cells }},
		{name: "nothing reserved", spec: func(img *template.Image) { img.Reserve = 0 }},
		{name: "the most reserved", spec: func(img *template.Image) { img.Reserve = cells }},
		{
			name: "the furthest cell offsets",
			spec: func(img *template.Image) { img.XOffset, img.YOffset = template.Length{N: -cells}, cells },
		},
		{
			name: "the furthest cell offsets the other way",
			spec: func(img *template.Image) { img.XOffset, img.YOffset = template.Length{N: cells}, -cells },
		},
		{name: "the furthest percentage left", spec: func(img *template.Image) { img.XOffset = template.Length{N: -100, Percent: true} }},
		{name: "the furthest percentage right", spec: func(img *template.Image) { img.XOffset = template.Length{N: 100, Percent: true} }},
		{name: "a zero percentage offset", spec: func(img *template.Image) { img.XOffset = template.Length{Percent: true} }},
		{
			name: "the furthest pixel offsets",
			spec: func(img *template.Image) { img.XOffsetPixels, img.YOffsetPixels = -pixels, pixels },
		},
		{
			name: "the furthest pixel offsets the other way",
			spec: func(img *template.Image) { img.XOffsetPixels, img.YOffsetPixels = pixels, -pixels },
		},
		{name: "fit contain", spec: func(img *template.Image) { img.Fit = term.ImageFitContain }},
		{name: "the lowest z_index", spec: func(img *template.Image) { img.ZIndex = math.MinInt32 }},
		{name: "the highest z_index", spec: func(img *template.Image) { img.ZIndex = math.MaxInt32 }},
		{name: "the most max_cut", spec: func(img *template.Image) { img.MaxCut = cells }},
		{name: "an empty alt", spec: func(img *template.Image) { img.Alt = "" }},
		{name: "an alt over lines", spec: func(img *template.Image) { img.Alt = "a\nb\n" }},
		{name: "an alt of wide glyphs", spec: func(img *template.Image) { img.Alt = "日本🙂👨‍👩‍👧" }},
		{name: "an alt of combining marks", spec: func(img *template.Image) { img.Alt = "e\u0301\u200b" }},
		{name: "an alt of spaces", spec: func(img *template.Image) { img.Alt = "   " }},
		{name: "a zero ttl", spec: func(img *template.Image) { img.TTL = 0 }},

		{name: "the zero Image", spec: func(img *template.Image) { *img = template.Image{} }, wantErr: `src: imageuri: unsupported scheme ""`},
		{name: "no URI", spec: func(img *template.Image) { img.URI = "" }, wantErr: `src: imageuri: unsupported scheme ""`},
		{name: "a relative path", spec: func(img *template.Image) { img.URI = "a.png" }, wantErr: `unsupported scheme ""`},
		{name: "a file URI without a path", spec: func(img *template.Image) { img.URI = "file:" }, wantErr: "has no path"},
		{name: "an http URI without a host", spec: func(img *template.Image) { img.URI = "http:///a.png" }, wantErr: "has no host"},
		{name: "an unparsable URI", spec: func(img *template.Image) { img.URI = "file:///%zz" }, wantErr: "invalid URL escape"},
		{name: "a data URI", spec: func(img *template.Image) { img.URI = "data:image/png;base64,AA" }, wantErr: `unsupported scheme "data"`},
		{name: "no width", spec: func(img *template.Image) { img.Width = template.Length{} }, wantErr: "width must be from 1 to 4096, got 0"},
		{name: "a negative width", spec: func(img *template.Image) { img.Width = template.Length{N: -1} }, wantErr: "width must be from 1 to 4096, got -1"},
		{name: "a width past the most cells", spec: func(img *template.Image) { img.Width = template.Length{N: cells + 1} }, wantErr: "width must be from 1 to 4096, got 4097"},
		{name: "the largest int width", spec: func(img *template.Image) { img.Width = template.Length{N: math.MaxInt} }, wantErr: "width must be from 1 to 4096"},
		{name: "the smallest int width", spec: func(img *template.Image) { img.Width = template.Length{N: math.MinInt} }, wantErr: "width must be from 1 to 4096"},
		{name: "a zero percentage width", spec: func(img *template.Image) { img.Width = template.Length{Percent: true} }, wantErr: "width must be from 1% to 100%, got 0%"},
		{name: "a negative percentage width", spec: func(img *template.Image) { img.Width = template.Length{N: -50, Percent: true} }, wantErr: "width must be from 1% to 100%, got -50%"},
		{name: "a percentage width past 100%", spec: func(img *template.Image) { img.Width = template.Length{N: 101, Percent: true} }, wantErr: "width must be from 1% to 100%, got 101%"},
		{name: "the largest percentage width", spec: func(img *template.Image) { img.Width = template.Length{N: math.MaxInt, Percent: true} }, wantErr: "width must be from 1% to 100%"},
		{name: "no height", spec: func(img *template.Image) { img.Height = 0 }, wantErr: "height must be from 1 to 4096, got 0"},
		{name: "a negative height", spec: func(img *template.Image) { img.Height = -1 }, wantErr: "height must be from 1 to 4096, got -1"},
		{name: "a height past the most cells", spec: func(img *template.Image) { img.Height = cells + 1 }, wantErr: "height must be from 1 to 4096, got 4097"},
		{name: "the largest int height", spec: func(img *template.Image) { img.Height = math.MaxInt }, wantErr: "height must be from 1 to 4096"},
		{name: "a negative reserve", spec: func(img *template.Image) { img.Reserve = -1 }, wantErr: "reserve must be from 0 to 4096, got -1"},
		{name: "a reserve past the most cells", spec: func(img *template.Image) { img.Reserve = cells + 1 }, wantErr: "reserve must be from 0 to 4096, got 4097"},
		{name: "the largest int reserve", spec: func(img *template.Image) { img.Reserve = math.MaxInt }, wantErr: "reserve must be from 0 to 4096"},
		{name: "an x_offset past the most cells left", spec: func(img *template.Image) { img.XOffset = template.Length{N: -cells - 1} }, wantErr: "x_offset must be from -4096 to 4096, got -4097"},
		{name: "an x_offset past the most cells right", spec: func(img *template.Image) { img.XOffset = template.Length{N: cells + 1} }, wantErr: "x_offset must be from -4096 to 4096, got 4097"},
		{name: "the smallest int x_offset", spec: func(img *template.Image) { img.XOffset = template.Length{N: math.MinInt} }, wantErr: "x_offset must be from -4096 to 4096"},
		{name: "an x_offset percentage past -100%", spec: func(img *template.Image) { img.XOffset = template.Length{N: -101, Percent: true} }, wantErr: "x_offset must be from -100% to 100%, got -101%"},
		{name: "an x_offset percentage past 100%", spec: func(img *template.Image) { img.XOffset = template.Length{N: 101, Percent: true} }, wantErr: "x_offset must be from -100% to 100%, got 101%"},
		{name: "a y_offset past the most cells up", spec: func(img *template.Image) { img.YOffset = -cells - 1 }, wantErr: "y_offset must be from -4096 to 4096, got -4097"},
		{name: "a y_offset past the most cells down", spec: func(img *template.Image) { img.YOffset = cells + 1 }, wantErr: "y_offset must be from -4096 to 4096, got 4097"},
		{name: "the largest int y_offset", spec: func(img *template.Image) { img.YOffset = math.MaxInt }, wantErr: "y_offset must be from -4096 to 4096"},
		{name: "an x_pixel_offset past the most pixels", spec: func(img *template.Image) { img.XOffsetPixels = pixels + 1 }, wantErr: "x_pixel_offset must be from -65536 to 65536, got 65537"},
		{name: "the smallest int x_pixel_offset", spec: func(img *template.Image) { img.XOffsetPixels = math.MinInt }, wantErr: "x_pixel_offset must be from -65536 to 65536"},
		{name: "a y_pixel_offset past the most pixels", spec: func(img *template.Image) { img.YOffsetPixels = -pixels - 1 }, wantErr: "y_pixel_offset must be from -65536 to 65536, got -65537"},
		{name: "the largest int y_pixel_offset", spec: func(img *template.Image) { img.YOffsetPixels = math.MaxInt }, wantErr: "y_pixel_offset must be from -65536 to 65536"},
		{name: "an unknown fit", spec: func(img *template.Image) { img.Fit = term.ImageFitContain + 1 }, wantErr: "fit must be contain or fill, got 2"},
		{name: "the largest fit", spec: func(img *template.Image) { img.Fit = math.MaxUint8 }, wantErr: "fit must be contain or fill, got 255"},
		{name: "a negative max_cut", spec: func(img *template.Image) { img.MaxCut = -1 }, wantErr: "max_cut must be from 0 to 4096, got -1"},
		{name: "a max_cut past the most cells", spec: func(img *template.Image) { img.MaxCut = cells + 1 }, wantErr: "max_cut must be from 0 to 4096, got 4097"},
		{name: "an alt with a tab", spec: func(img *template.Image) { img.Alt = "a\tb" }, wantErr: `alt must be UTF-8 text without control characters other than newlines, got "a\tb"`},
		{name: "an alt with a NUL", spec: func(img *template.Image) { img.Alt = "\x00" }, wantErr: `got "\x00"`},
		{name: "an alt with a carriage return", spec: func(img *template.Image) { img.Alt = "a\r\nb" }, wantErr: `got "a\r\nb"`},
		{name: "an alt with an escape sequence", spec: func(img *template.Image) { img.Alt = "\x1b[31mx" }, wantErr: `got "\x1b[31mx"`},
		{name: "an alt with a DEL", spec: func(img *template.Image) { img.Alt = "\x7f" }, wantErr: `got "\x7f"`},
		{name: "an alt with a C1 control", spec: func(img *template.Image) { img.Alt = "\u0085" }, wantErr: `got "\u0085"`},
		{name: "an alt of invalid UTF-8", spec: func(img *template.Image) { img.Alt = "\xff" }, wantErr: `got "\xff"`},
		{name: "a negative ttl", spec: func(img *template.Image) { img.TTL = -time.Nanosecond }, wantErr: "ttl must not be negative, got -1ns"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := template.Image{
				URI:     "file:///tmp/a.png",
				Width:   template.Length{N: 2},
				Height:  1,
				Reserve: 2,
				Alt:     template.DefaultImageAlt,
				TTL:     time.Hour,
			}
			if tt.spec != nil {
				tt.spec(&img)
			}
			err := img.Validate()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, tt.wantErr)
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
		{template.Length{N: 4}, 0, 4},
		{template.Length{N: 4}, -80, 4},
		{template.Length{N: 0}, 80, 0},
		{template.Length{N: -3}, 80, -3},
		{template.Length{N: 100, Percent: true}, 80, 80},
		{template.Length{N: 50, Percent: true}, 81, 40},
		{template.Length{N: 1, Percent: true}, 80, 0},
		{template.Length{N: 1, Percent: true}, 100, 1},
		{template.Length{N: 0, Percent: true}, 80, 0},
		{template.Length{N: 50, Percent: true}, 0, 0},
		{template.Length{N: 50, Percent: true}, 1, 0},
		{template.Length{N: 50, Percent: true}, -81, -40},
		{template.Length{N: -50, Percent: true}, 81, -40},
		{template.Length{N: -100, Percent: true}, 80, -80},
		{template.Length{N: -50, Percent: true}, -81, 40},
		{template.Length{N: 100, Percent: true}, template.MaxImageCells, template.MaxImageCells},
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
		{template.ZIndexBackground + 1, term.ImageLayerBelowText},
		{template.ZIndexBackground, term.ImageLayerBelowText},
		{template.ZIndexBackground - 1, term.ImageLayerBelowBackground},
		{math.MinInt32 + 1, term.ImageLayerBelowBackground},
		{math.MinInt32, term.ImageLayerBelowBackground},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, template.Image{ZIndex: tt.zindex}.Layer(), "z_index %d", tt.zindex)
	}
}
