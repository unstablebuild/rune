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
		MaxCut:  template.DefaultImageMaxCut,
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
				`z_index -3 | overflow | max_cut 3 | alt "logo" | ttl "1h30m" | bg "navy"`,
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
					MaxCut:        3,
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
		{
			name:   "a max_cut of zero",
			action: `.Image | ` + src + ` | max_cut 0`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.MaxCut = 0
			})},
		},
		{
			name:   "the largest max_cut",
			action: `.Image | ` + src + ` | max_cut 4096`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.MaxCut = 4096
			})},
		},
		{
			name:   "directives before src",
			action: `.Image | width 3 | height 2 | ` + src,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Height, img.Reserve = template.Length{N: 3}, 2, 3
			})},
		},
		{
			name:   "the last of a directive given twice wins",
			action: `.Image | src "https://example.com/a.png" | width 3 | ` + src + ` | width 5 | alt "a" | alt "b"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Reserve, img.Alt = template.Length{N: 5}, 5, "b"
			})},
		},
		{
			name:   "a reserve before the width still overrides it",
			action: `.Image | ` + src + ` | reserve 1 | width "50%" | width 8`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Reserve = template.Length{N: 8}, 1
			})},
		},
		{
			name:   "styled like any other element",
			action: `.Image | bold | ` + src + ` | fg "red" | italic`,
			want: template.Action{
				Field: "Image",
				Attributes: term.Attributes{
					Fg: term.ColorRed, Attrs: term.AttrBold | term.AttrItalic,
				},
				Image: image,
			},
		},
		{
			name:   "integers in hex, octal, binary, with underscores and exponents",
			action: `.Image | ` + src + ` | width 0x10 | height 0o7 | reserve 0b11 | x_offset -1_0 | y_offset 1e1`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Height, img.Reserve = template.Length{N: 16}, 7, 3
				img.XOffset, img.YOffset = template.Length{N: -10}, 10
			})},
		},
		{
			name:   "the largest sizes",
			action: `.Image | ` + src + ` | width 4096 | height 4096`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Height, img.Reserve = template.Length{N: 4096}, 4096, 4096
			})},
		},
		{
			name:   "the furthest offsets",
			action: `.Image | ` + src + ` | x_offset -4096 | y_offset 4096 | x_pixel_offset 65536 | y_pixel_offset -65536`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.XOffset, img.YOffset = template.Length{N: -4096}, 4096
				img.XOffsetPixels, img.YOffsetPixels = 65536, -65536
			})},
		},
		{
			name:   "a percentage with a plus sign",
			action: `.Image | ` + src + ` | width "+1%" | x_offset "+100%"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Reserve = template.Length{N: 1, Percent: true}, 0
				img.XOffset = template.Length{N: 100, Percent: true}
			})},
		},
		{
			name:   "a percentage in a raw string",
			action: `.Image | ` + src + " | width `25%`",
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Width, img.Reserve = template.Length{N: 25, Percent: true}, 0
			})},
		},
		{
			name:   "a zero percentage offset",
			action: `.Image | ` + src + ` | x_offset "-0%"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.XOffset = template.Length{Percent: true}
			})},
		},
		{
			name:   "zero offsets",
			action: `.Image | ` + src + ` | x_offset 0 | y_offset -0 | x_pixel_offset 0 | y_pixel_offset 0`,
			want:   template.Action{Field: "Image", Image: image},
		},
		{
			name:   "z_index in hex",
			action: `.Image | ` + src + ` | z_index -0x80000000`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.ZIndex = math.MinInt32
			})},
		},
		{
			name:   "fit contain given explicitly",
			action: `.Image | ` + src + ` | fit "fill" | fit "contain"`,
			want:   template.Action{Field: "Image", Image: image},
		},
		{
			name:   "an alt over lines",
			action: `.Image | ` + src + ` | alt "a\nb"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Alt = "a\nb"
			})},
		},
		{
			name:   "an alt of wide glyphs, emoji and spaces",
			action: `.Image | ` + src + ` | alt " 日本 🙂 "`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.Alt = " 日本 🙂 "
			})},
		},
		{
			name:   "a zero ttl",
			action: `.Image | ` + src + ` | ttl "0"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.TTL = 0
			})},
		},
		{
			name:   "a negative zero ttl",
			action: `.Image | ` + src + ` | ttl "-0s"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.TTL = 0
			})},
		},
		{
			name:   "a compound ttl",
			action: `.Image | ` + src + ` | ttl "1h0m30.5s"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.TTL = time.Hour + 30*time.Second + 500*time.Millisecond
			})},
		},
		{
			name:   "an upper-case scheme",
			action: `.Image | src "FILE:///tmp/hud.png"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.URI = "FILE:///tmp/hud.png"
			})},
		},
		{
			name:   "a path with spaces",
			action: `.Image | src "file:///tmp/my hud.png"`,
			want: template.Action{Field: "Image", Image: with(func(img *template.Image) {
				img.URI = "file:///tmp/my hud.png"
			})},
		},
		{name: "an image without src", action: `.Image | width 2`, wantErr: ".Image requires src"},
		{name: "an image with an empty src", action: `.Image | src ""`, wantErr: ".Image requires src"},
		{name: "an image without src and an invalid width", action: `.Image | width 0`, wantErr: ".Image requires src"},
		{name: "a field chain", action: `.Image.Logo | ` + src, wantErr: "unsupported field expression"},
		{name: "an image directive on another field", action: `.Status | width 2`, wantErr: "width only applies to .Image, not .Status"},
		{name: "an unsupported scheme", action: `.Image | src "ftp://example.com/a.png"`, wantErr: `src: imageuri: unsupported scheme "ftp"`},
		{name: "a data URI", action: `.Image | src "data:image/png;base64,AAAA"`, wantErr: `src: imageuri: unsupported scheme "data"`},
		{name: "a relative path", action: `.Image | src "hud.png"`, wantErr: `unsupported scheme ""`},
		{name: "a file URI with a host but no path", action: `.Image | src "file://host"`, wantErr: "has no path"},
		{name: "an https URI without a host", action: `.Image | src "https://"`, wantErr: "has no host"},
		{name: "an unparsable src", action: `.Image | src "file:///%zz"`, wantErr: `invalid URL escape "%zz"`},
		{name: "an invalid src given before a valid one", action: `.Image | src "ftp://a" | ` + src, want: template.Action{Field: "Image", Image: image}},
		{name: "a src that is not a string", action: `.Image | src 3`, wantErr: "src argument must be a string"},
		{name: "a src without its argument", action: `.Image | src`, wantErr: "src takes one argument"},
		{name: "a src with two arguments", action: `.Image | src "file:///a.png" "file:///b.png"`, wantErr: "src takes one argument"},
		{name: "a zero width", action: `.Image | ` + src + ` | width 0`, wantErr: "width must be from 1 to 4096, got 0"},
		{name: "a negative zero width", action: `.Image | ` + src + ` | width -0`, wantErr: "width must be from 1 to 4096, got 0"},
		{name: "a negative width", action: `.Image | ` + src + ` | width -2`, wantErr: "width must be from 1 to 4096, got -2"},
		{name: "a width past the most cells", action: `.Image | ` + src + ` | width 4097`, wantErr: "width must be from 1 to 4096, got 4097"},
		{name: "the largest integer width", action: `.Image | ` + src + ` | width 9223372036854775807`, wantErr: "width must be from 1 to 4096, got 9223372036854775807"},
		{name: "the smallest integer width", action: `.Image | ` + src + ` | width -9223372036854775808`, wantErr: "width must be from 1 to 4096, got -9223372036854775808"},
		{name: "a fractional width", action: `.Image | ` + src + ` | width 1.5`, wantErr: "width argument must be an integer"},
		{name: "a complex width", action: `.Image | ` + src + ` | width 1i`, wantErr: "width argument must be an integer"},
		{name: "a character width", action: `.Image | ` + src + ` | width 'a'`, wantErr: "width argument must be an integer"},
		{name: "a nil width", action: `.Image | ` + src + ` | width nil`, wantErr: "width argument must be an integer"},
		{name: "a boolean width", action: `.Image | ` + src + ` | width true`, wantErr: "width argument must be an integer"},
		{name: "a field width", action: `.Image | ` + src + ` | width .Width`, wantErr: "width argument must be an integer"},
		{name: "a parenthesized width", action: `.Image | ` + src + ` | width (1)`, wantErr: "width argument must be an integer"},
		{name: "a function as the width", action: `.Image | ` + src + ` | width bold`, wantErr: "width argument must be an integer"},
		{name: "a width with two arguments", action: `.Image | ` + src + ` | width 1 2`, wantErr: "width takes one argument"},
		{name: "a width without its argument", action: `.Image | ` + src + ` | width`, wantErr: "width takes one argument"},
		{name: "a width over 100%", action: `.Image | ` + src + ` | width "120%"`, wantErr: "width must be from 1% to 100%, got 120%"},
		{name: "a zero percentage width", action: `.Image | ` + src + ` | width "0%"`, wantErr: "width must be from 1% to 100%, got 0%"},
		{name: "a negative percentage width", action: `.Image | ` + src + ` | width "-50%"`, wantErr: "width must be from 1% to 100%, got -50%"},
		{name: "a percentage width past the largest integer", action: `.Image | ` + src + ` | width "99999999999999999999%"`, wantErr: `got "99999999999999999999%"`},
		{name: "a width without a percent sign", action: `.Image | ` + src + ` | width "50"`, wantErr: `width must be a number of cells or a percentage such as "50%", got "50"`},
		{name: "a lone percent sign", action: `.Image | ` + src + ` | width "%"`, wantErr: `got "%"`},
		{name: "a doubled percent sign", action: `.Image | ` + src + ` | width "50%%"`, wantErr: `got "50%%"`},
		{name: "a percentage with a space before it", action: `.Image | ` + src + ` | width " 50%"`, wantErr: `got " 50%"`},
		{name: "a percentage with a space before its sign", action: `.Image | ` + src + ` | width "50 %"`, wantErr: `got "50 %"`},
		{name: "a fractional percentage", action: `.Image | ` + src + ` | width "1.5%"`, wantErr: `got "1.5%"`},
		{name: "a hex percentage", action: `.Image | ` + src + ` | width "0x10%"`, wantErr: `got "0x10%"`},
		{name: "an empty string width", action: `.Image | ` + src + ` | width ""`, wantErr: `got ""`},
		{name: "a zero height", action: `.Image | ` + src + ` | height 0`, wantErr: "height must be from 1 to 4096, got 0"},
		{name: "a negative height", action: `.Image | ` + src + ` | height -3`, wantErr: "height must be from 1 to 4096, got -3"},
		{name: "a height past the most cells", action: `.Image | ` + src + ` | height 4097`, wantErr: "height must be from 1 to 4096, got 4097"},
		{name: "the largest integer height", action: `.Image | ` + src + ` | height 9223372036854775807`, wantErr: "height must be from 1 to 4096"},
		{name: "a height as a percentage", action: `.Image | ` + src + ` | height "50%"`, wantErr: "height argument must be an integer"},
		{name: "a negative reserve", action: `.Image | ` + src + ` | reserve -1`, wantErr: "reserve must be from 0 to 4096, got -1"},
		{name: "a reserve past the most cells", action: `.Image | ` + src + ` | reserve 4097`, wantErr: "reserve must be from 0 to 4096, got 4097"},
		{name: "the largest integer reserve", action: `.Image | ` + src + ` | reserve 9223372036854775807`, wantErr: "reserve must be from 0 to 4096"},
		{name: "a reserve as a percentage", action: `.Image | ` + src + ` | reserve "50%"`, wantErr: "reserve argument must be an integer"},
		{name: "a reserve defaulting to a width past the most cells", action: `.Image | ` + src + ` | width 5000`, wantErr: "width must be from 1 to 4096, got 5000"},
		{name: "an x_offset over 100%", action: `.Image | ` + src + ` | x_offset "101%"`, wantErr: "x_offset must be from -100% to 100%, got 101%"},
		{name: "an x_offset under -100%", action: `.Image | ` + src + ` | x_offset "-101%"`, wantErr: "x_offset must be from -100% to 100%, got -101%"},
		{name: "an x_offset without a percent sign", action: `.Image | ` + src + ` | x_offset "3"`, wantErr: `got "3"`},
		{name: "an x_offset past the most cells", action: `.Image | ` + src + ` | x_offset 4097`, wantErr: "x_offset must be from -4096 to 4096, got 4097"},
		{name: "the smallest integer x_offset", action: `.Image | ` + src + ` | x_offset -9223372036854775808`, wantErr: "x_offset must be from -4096 to 4096"},
		{name: "a fractional x_offset", action: `.Image | ` + src + ` | x_offset 0.5`, wantErr: "x_offset argument must be an integer"},
		{name: "a y_offset as a percentage", action: `.Image | ` + src + ` | y_offset "50%"`, wantErr: "y_offset argument must be an integer"},
		{name: "a y_offset past the most cells", action: `.Image | ` + src + ` | y_offset -4097`, wantErr: "y_offset must be from -4096 to 4096, got -4097"},
		{name: "the largest integer y_offset", action: `.Image | ` + src + ` | y_offset 9223372036854775807`, wantErr: "y_offset must be from -4096 to 4096"},
		{name: "a fractional x_pixel_offset", action: `.Image | ` + src + ` | x_pixel_offset 0.5`, wantErr: "x_pixel_offset argument must be an integer"},
		{name: "an x_pixel_offset as a percentage", action: `.Image | ` + src + ` | x_pixel_offset "50%"`, wantErr: "x_pixel_offset argument must be an integer"},
		{name: "an x_pixel_offset past the most pixels", action: `.Image | ` + src + ` | x_pixel_offset -65537`, wantErr: "x_pixel_offset must be from -65536 to 65536, got -65537"},
		{name: "the largest integer x_pixel_offset", action: `.Image | ` + src + ` | x_pixel_offset 9223372036854775807`, wantErr: "x_pixel_offset must be from -65536 to 65536"},
		{name: "a y_pixel_offset as a percentage", action: `.Image | ` + src + ` | y_pixel_offset "50%"`, wantErr: "y_pixel_offset argument must be an integer"},
		{name: "a y_pixel_offset past the most pixels", action: `.Image | ` + src + ` | y_pixel_offset 65537`, wantErr: "y_pixel_offset must be from -65536 to 65536, got 65537"},
		{name: "a y_pixel_offset without its argument", action: `.Image | ` + src + ` | y_pixel_offset`, wantErr: "y_pixel_offset takes one argument"},
		{name: "a pixel offset on another field", action: `.Status | x_pixel_offset 1`, wantErr: "x_pixel_offset only applies to .Image, not .Status"},
		{name: "an unknown fit", action: `.Image | ` + src + ` | fit "cover"`, wantErr: `fit must be one of "contain", "fill", got "cover"`},
		{name: "a fit in another case", action: `.Image | ` + src + ` | fit "Contain"`, wantErr: `got "Contain"`},
		{name: "an empty fit", action: `.Image | ` + src + ` | fit ""`, wantErr: `got ""`},
		{name: "a fit that is not a string", action: `.Image | ` + src + ` | fit 1`, wantErr: "fit argument must be a string"},
		{name: "a fit without its argument", action: `.Image | ` + src + ` | fit`, wantErr: "fit takes one argument"},
		{name: "a z_index under the lowest", action: `.Image | ` + src + ` | z_index -2147483649`, wantErr: "z_index must be from -2147483648 to 2147483647, got -2147483649"},
		{name: "a z_index over the highest", action: `.Image | ` + src + ` | z_index 2147483648`, wantErr: "z_index must be from -2147483648 to 2147483647, got 2147483648"},
		{name: "the largest integer z_index", action: `.Image | ` + src + ` | z_index 9223372036854775807`, wantErr: "z_index must be from -2147483648 to 2147483647"},
		{name: "a fractional z_index", action: `.Image | ` + src + ` | z_index 1.5`, wantErr: "z_index argument must be an integer"},
		{name: "a z_index as a string", action: `.Image | ` + src + ` | z_index "top"`, wantErr: "z_index argument must be an integer"},
		{name: "a character z_index", action: `.Image | ` + src + ` | z_index 'z'`, wantErr: "z_index argument must be an integer"},
		{name: "a z_index without its argument", action: `.Image | ` + src + ` | z_index`, wantErr: "z_index takes one argument"},
		{name: "a z_index on another field", action: `.Status | z_index 1`, wantErr: "z_index only applies to .Image, not .Status"},
		{name: "overflow with an argument", action: `.Image | ` + src + ` | overflow true`, wantErr: "overflow takes no arguments"},
		{name: "overflow with a string argument", action: `.Image | ` + src + ` | overflow "no"`, wantErr: "overflow takes no arguments"},
		{name: "overflow on another field", action: `.Status | overflow`, wantErr: "overflow only applies to .Image, not .Status"},
		{name: "a negative max_cut", action: `.Image | ` + src + ` | max_cut -1`, wantErr: "max_cut must be from 0 to 4096, got -1"},
		{name: "a max_cut past the most cells", action: `.Image | ` + src + ` | max_cut 4097`, wantErr: "max_cut must be from 0 to 4096, got 4097"},
		{name: "a max_cut as a percentage", action: `.Image | ` + src + ` | max_cut "50%"`, wantErr: "max_cut argument must be an integer"},
		{name: "a max_cut without its argument", action: `.Image | ` + src + ` | max_cut`, wantErr: "max_cut takes one argument"},
		{name: "a max_cut on another field", action: `.Status | max_cut 1`, wantErr: "max_cut only applies to .Image, not .Status"},
		{name: "an alt with a tab", action: `.Image | ` + src + ` | alt "a\tb"`, wantErr: `alt must be UTF-8 text without control characters other than newlines, got "a\tb"`},
		{name: "an alt with a NUL", action: `.Image | ` + src + ` | alt "\x00"`, wantErr: `got "\x00"`},
		{name: "an alt with an escape sequence", action: `.Image | ` + src + ` | alt "\x1b[2J"`, wantErr: `got "\x1b[2J"`},
		{name: "an alt with a carriage return", action: `.Image | ` + src + ` | alt "a\rb"`, wantErr: `got "a\rb"`},
		{name: "an alt of invalid UTF-8", action: `.Image | ` + src + ` | alt "\xff"`, wantErr: `got "\xff"`},
		{name: "an alt that is not a string", action: `.Image | ` + src + ` | alt 1`, wantErr: "alt argument must be a string"},
		{name: "an alt without its argument", action: `.Image | ` + src + ` | alt`, wantErr: "alt takes one argument"},
		{name: "an alt with two arguments", action: `.Image | ` + src + ` | alt "a" "b"`, wantErr: "alt takes one argument"},
		{name: "an unparsable ttl", action: `.Image | ` + src + ` | ttl "soon"`, wantErr: `invalid duration "soon"`},
		{name: "a negative ttl", action: `.Image | ` + src + ` | ttl "-1h"`, wantErr: "ttl must not be negative, got -1h0m0s"},
		{name: "a ttl without a unit", action: `.Image | ` + src + ` | ttl "1"`, wantErr: `missing unit in duration "1"`},
		{name: "an empty ttl", action: `.Image | ` + src + ` | ttl ""`, wantErr: `invalid duration ""`},
		{name: "a ttl past the longest duration", action: `.Image | ` + src + ` | ttl "9999999h"`, wantErr: `invalid duration "9999999h"`},
		{name: "a ttl as a number", action: `.Image | ` + src + ` | ttl 5`, wantErr: "ttl argument must be a string"},
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
