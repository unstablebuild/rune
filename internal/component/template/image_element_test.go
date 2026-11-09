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
	"context"
	"fmt"
	"image"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template/parse"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/goleak"
	"unstable.build/rune/internal/component/imageuri/imageuritest"
	"unstable.build/rune/internal/component/template"
)

func TestNewImageElement(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const cells, pixels = template.MaxImageCells, template.MaxImagePixelOffset
	tests := []struct {
		name          string
		tmpl          string
		spec          func(*template.Image)
		noStorage     bool
		noInterrupter bool
		wantErr       string
		wantSize      [2]int
	}{
		{
			name: "with the defaults of an image directive",
			tmpl: "%s",
			spec: func(img *template.Image) {
				img.Width, img.Reserve = template.Length{N: template.DefaultImageWidth}, template.DefaultImageWidth
				img.Fit, img.TTL = term.ImageFitContain, template.DefaultImageTTL
			},
			wantSize: [2]int{2, 1},
		},
		{
			name: "at every bound at once",
			tmpl: "%s",
			spec: func(img *template.Image) {
				img.Width, img.Height, img.Reserve = template.Length{N: cells}, cells, cells
				img.XOffset, img.YOffset = template.Length{N: -cells}, cells
				img.XOffsetPixels, img.YOffsetPixels = pixels, -pixels
				img.ZIndex, img.TTL, img.Alt = math.MinInt32, 0, ""
			},
			wantSize: [2]int{cells, 1},
		},
		{
			name: "spanning the layout without reserving cells",
			tmpl: "%s",
			spec: func(img *template.Image) {
				img.Width, img.Reserve = template.Length{N: 100, Percent: true}, 0
			},
		},
		{name: "for the zero Image", spec: func(img *template.Image) { *img = template.Image{} }, wantErr: `src: imageuri: unsupported scheme ""`},
		{name: "without a width", spec: func(img *template.Image) { img.Width = template.Length{} }, wantErr: "width must be from 1 to 4096, got 0"},
		{name: "without a height", spec: func(img *template.Image) { img.Height = 0 }, wantErr: "height must be from 1 to 4096, got 0"},
		{name: "with the largest int height", spec: func(img *template.Image) { img.Height = math.MaxInt }, wantErr: "height must be from 1 to 4096"},
		{name: "with a negative reserve", spec: func(img *template.Image) { img.Reserve = -1 }, wantErr: "reserve must be from 0 to 4096, got -1"},
		{name: "with the largest int reserve", spec: func(img *template.Image) { img.Reserve = math.MaxInt }, wantErr: "reserve must be from 0 to 4096"},
		{name: "with the smallest int x_offset", spec: func(img *template.Image) { img.XOffset = template.Length{N: math.MinInt} }, wantErr: "x_offset must be from -4096 to 4096"},
		{name: "with the largest int x_offset percentage", spec: func(img *template.Image) { img.XOffset = template.Length{N: math.MaxInt, Percent: true} }, wantErr: "x_offset must be from -100% to 100%"},
		{name: "with the largest int pixel offset", spec: func(img *template.Image) { img.YOffsetPixels = math.MaxInt }, wantErr: "y_pixel_offset must be from -65536 to 65536"},
		{name: "with an unknown fit", spec: func(img *template.Image) { img.Fit = 9 }, wantErr: "fit must be contain or fill, got 9"},
		{name: "with an escape sequence in its alt", spec: func(img *template.Image) { img.Alt = "\x1b[2J" }, wantErr: `got "\x1b[2J"`},
		{name: "with a negative ttl", spec: func(img *template.Image) { img.TTL = -time.Hour }, wantErr: "ttl must not be negative, got -1h0m0s"},
		{name: "without storage", noStorage: true, wantErr: "imageuri: nil storage"},
		{name: "without an interrupter", noInterrupter: true, wantErr: "imageuri: nil interrupter"},
		{
			name:      "with an invalid spec and without storage",
			spec:      func(img *template.Image) { img.Height = 0 },
			noStorage: true,
			wantErr:   "height must be from 1 to 4096, got 0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var storage storageapi.Service = storagestub.NewInMemoryService()
			if tt.noStorage {
				storage = nil
			}
			var irq term.Interrupter = term.NopInterrupter()
			if tt.noInterrupter {
				irq = nil
			}
			el, err := template.NewImageElement(imageSpec(t, tt.spec), tt.tmpl,
				term.Attributes{}, term.ColorDefault, storage, irq)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, el)
				return
			}
			require.NoError(t, err)
			defer func() { assert.NoError(t, el.Close()) }()
			w, h := el.Dimensions()
			assert.Equal(t, tt.wantSize, [2]int{w, h})
		})
	}
}

func TestNewLoadedImageElement(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	tests := []struct {
		name     string
		spec     func(*template.Image)
		wantErr  string
		wantSize [2]int
	}{
		{name: "draws the image it is given from its first frame", wantSize: [2]int{6, 1}},
		{
			name: "reads nothing from its URI",
			spec: func(img *template.Image) {
				img.URI = "file:///does/not/exist.png"
			},
			wantSize: [2]int{6, 1},
		},
		{name: "fails for an invalid spec", spec: func(img *template.Image) { img.Height = 0 }, wantErr: "height must be from 1 to 4096, got 0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			el, err := template.NewLoadedImageElement(imageSpec(t, tt.spec), src, "ab%scd",
				term.Attributes{}, term.ColorDefault)
			if tt.wantErr != "" {
				assert.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, el)
				return
			}
			require.NoError(t, err)
			defer func() { assert.NoError(t, el.Close()) }()
			w, h := el.Dimensions()
			assert.Equal(t, tt.wantSize, [2]int{w, h})

			template.PlaceImages([]component.Floating{el}, 0, 20, 20, 3)
			rec := &recorder{}
			el.DrawImage(rec, false)
			require.Len(t, rec.images, 1)
			assert.Same(t, src, rec.images[0].Src)
		})
	}
}

func TestImageElementLiterals(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tests := []struct {
		name    string
		tmpl    string
		reserve int
		// wantSize is the element's Dimensions.
		wantSize [2]int
		// wantDrawn is the element drawn in its Dimensions.
		wantDrawn string
		// wantAnchor is the cell the image is centered on.
		wantAnchor int
	}{
		{name: "nothing but reserved cells", tmpl: "", reserve: 2, wantSize: [2]int{2, 1}, wantDrawn: "  "},
		{name: "nothing at all", tmpl: "", reserve: 0, wantSize: [2]int{0, 0}, wantDrawn: ""},
		{name: "a bare verb", tmpl: "%s", reserve: 2, wantSize: [2]int{2, 1}, wantDrawn: "  "},
		{name: "literals on both sides", tmpl: "<%s>", reserve: 2, wantSize: [2]int{4, 1}, wantDrawn: "<  >", wantAnchor: 1},
		{name: "literals around nothing reserved", tmpl: "<%s>", reserve: 0, wantSize: [2]int{2, 1}, wantDrawn: "<>", wantAnchor: 1},
		{name: "no verb draws the literal first", tmpl: "abc", reserve: 2, wantSize: [2]int{5, 1}, wantDrawn: "abc  ", wantAnchor: 3},
		{name: "only the first verb holds the cells", tmpl: "<%s>%s", reserve: 2, wantSize: [2]int{6, 1}, wantDrawn: "<  >%s", wantAnchor: 1},
		{name: "other verbs are drawn as they are", tmpl: "%d%v%s%x%%", reserve: 2, wantSize: [2]int{10, 1}, wantDrawn: "%d%v  %x%%", wantAnchor: 4},
		{name: "a percent sign right before the verb", tmpl: "100%%s", reserve: 2, wantSize: [2]int{6, 1}, wantDrawn: "100%  ", wantAnchor: 4},
		{name: "a lone percent sign", tmpl: "%", reserve: 2, wantSize: [2]int{3, 1}, wantDrawn: "%  ", wantAnchor: 1},
		{name: "spaces are kept", tmpl: "  %s  ", reserve: 2, wantSize: [2]int{6, 1}, wantDrawn: "      ", wantAnchor: 2},
		{name: "tabs are drawn as spaces", tmpl: "a\tb%s\tc", reserve: 2, wantSize: [2]int{7, 1}, wantDrawn: "a b   c", wantAnchor: 3},
		{name: "NULs are drawn as spaces", tmpl: "\x00%s\x00", reserve: 2, wantSize: [2]int{4, 1}, wantDrawn: "    ", wantAnchor: 1},
		{name: "newlines do not break the bar", tmpl: "a\nb%s", reserve: 2, wantSize: [2]int{5, 1}, wantDrawn: "a b  ", wantAnchor: 3},
		{name: "other control characters are drawn as spaces", tmpl: "\r\x1b\x7f\u0085%s", reserve: 2, wantSize: [2]int{6, 1}, wantDrawn: "      ", wantAnchor: 4},
		{name: "wide characters take two cells", tmpl: "日本%s語", reserve: 2, wantSize: [2]int{8, 1}, wantDrawn: "日 本   語 ", wantAnchor: 4},
		{name: "an emoji takes two cells", tmpl: "🙂%s", reserve: 2, wantSize: [2]int{4, 1}, wantDrawn: "🙂   ", wantAnchor: 2},
		{name: "an emoji sequence is one glyph", tmpl: "👨‍👩‍👧%s", reserve: 2, wantSize: [2]int{4, 1}, wantDrawn: "👨   ", wantAnchor: 2},
		{name: "a combining mark takes no cell", tmpl: "e\u0301%s", reserve: 2, wantSize: [2]int{3, 1}, wantDrawn: "e  ", wantAnchor: 1},
		{name: "shades are drawn too", tmpl: "█▓%s░", reserve: 2, wantSize: [2]int{5, 1}, wantDrawn: "█▓  ░", wantAnchor: 2},
		{name: "invalid UTF-8 is drawn as a replacement character", tmpl: "\xff%s", reserve: 2, wantSize: [2]int{3, 1}, wantDrawn: "\ufffd  ", wantAnchor: 1},
		{
			name:     "the most reserved cells",
			tmpl:     "%s",
			reserve:  template.MaxImageCells,
			wantSize: [2]int{template.MaxImageCells, 1}, wantDrawn: strings.Repeat(" ", template.MaxImageCells),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irq := make(interrupts, 1)
			el := newImageElementWith(t, tt.tmpl, imageSpec(t, func(img *template.Image) {
				img.URI += ".missing"
				img.Width, img.Reserve, img.Alt = template.Length{N: 1}, tt.reserve, "@"
				img.MaxCut = template.MaxImageCells
			}), irq)
			w, h := el.Dimensions()
			assert.Equal(t, tt.wantSize, [2]int{w, h}, "dimensions")

			drawn := term.NewStringWriter(w, 1)
			el.Resize(w, 1)
			el.Draw(drawn)
			require.NoError(t, drawn.Flush())
			assert.Equal(t, tt.wantDrawn, drawn.String(), "drawn")

			const width = 64
			template.PlaceImages([]component.Floating{el}, 0, width, width, 1)
			frame := drawUntil(t, el, width, 1, irq, func(frame string) bool {
				return strings.Contains(frame, "@")
			})
			assert.Equal(t, tt.wantAnchor, strings.Index(frame, "@"), "anchor")
		})
	}
}

func TestImageElementStylesLiterals(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// colors are the foreground and background of a cell.
	type colors struct{ Fg, Bg term.Color }
	tests := []struct {
		name  string
		attrs term.Attributes
		bg    term.Color
		// The cells of "a%s█" with one cell reserved.
		wantText, wantReserved, wantShade colors
		wantAttrs                         term.AttrMask
	}{
		{name: "without attributes"},
		{
			name:         "with a foreground over the bar",
			attrs:        term.Attributes{Fg: term.ColorRed},
			bg:           term.ColorNavy,
			wantText:     colors{term.ColorRed, term.ColorNavy},
			wantReserved: colors{term.ColorRed, term.ColorNavy},
			wantShade:    colors{term.ColorNavy, term.ColorNavy},
		},
		{
			name:         "with its own background fading into the bar",
			attrs:        term.Attributes{Fg: term.ColorRed, Bg: term.ColorGreen},
			bg:           term.ColorNavy,
			wantText:     colors{term.ColorRed, term.ColorGreen},
			wantReserved: colors{term.ColorRed, term.ColorGreen},
			wantShade:    colors{term.ColorGreen, term.ColorNavy},
		},
		{
			name:      "bold",
			attrs:     term.Attributes{Attrs: term.AttrBold},
			wantAttrs: term.AttrBold,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			el, err := template.NewImageElement(imageSpec(t, func(img *template.Image) {
				img.Reserve = 1
			}), "a%s█", tt.attrs, tt.bg, storagestub.NewInMemoryService(), term.NopInterrupter())
			require.NoError(t, err)
			defer func() { assert.NoError(t, el.Close()) }()
			w := term.NewStringWriter(3, 1)
			el.Resize(3, 1)
			el.Draw(w)

			cells := w.Cells()
			require.Len(t, cells, 3)
			assert.Equal(t, tt.wantText, colors{cells[0].Fg, cells[0].Bg}, "text")
			assert.Equal(t, tt.wantReserved, colors{cells[1].Fg, cells[1].Bg}, "reserved")
			assert.Equal(t, tt.wantShade, colors{cells[2].Fg, cells[2].Bg}, "shade")
			assert.Equal(t, tt.wantAttrs, cells[0].Attrs&tt.wantAttrs, "attributes")
		})
	}
}

func TestPlaceImages(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const cells, pixels = template.MaxImageCells, template.MaxImagePixelOffset
	// placed is what PlaceImages and DrawImage decide about a placement.
	type placed struct {
		Pos                  term.Coordinates
		Width, Height        int
		Offset               image.Point
		Overflow             bool
		VerticalRenderOffset bool
		Layer                term.ImageLayer
		Fit                  term.ImageFit
	}
	at := func(x, y, w, h int) placed {
		return placed{Pos: term.Coordinates{X: x, Y: y}, Width: w, Height: h}
	}
	with := func(p placed, fn func(*placed)) placed {
		fn(&p)
		return p
	}
	tests := []struct {
		name string
		// before is the text laid out before the image element "<%s>",
		// which reserves 2 cells: the image is anchored right after "<".
		before                string
		x, width, total, rows int
		spec                  func(*template.Image)
		shifted               bool
		expected              placed
	}{
		{
			name:   "is centered on its first reserved cell",
			before: "abc", width: 20, total: 20, rows: 3,
			expected: at(2, 2, 4, 1),
		},
		{
			name:   "is laid out from x",
			before: "abc", x: 10, width: 10, total: 20, rows: 3,
			expected: at(12, 2, 4, 1),
		},
		{
			name:   "is laid out from a negative x",
			before: "abc", x: -10, width: 20, total: 20, rows: 3,
			expected: at(-8, 2, 4, 1),
		},
		{
			name:   "is laid out from a far x",
			before: "abc", x: 10000, width: 20, total: 20, rows: 3,
			expected: at(10002, 2, 4, 1),
		},
		{
			name:   "follows elements cut to the width left",
			before: "abcdef", width: 4, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.MaxCut = cells },
			expected: at(3, 2, 4, 1),
		},
		{
			name:   "follows elements cut to no width",
			before: "abc", width: 0, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.MaxCut = cells },
			expected: at(-1, 2, 4, 1),
		},
		{
			name:   "takes a negative width for no width",
			before: "abc", width: -5, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.MaxCut = cells },
			expected: at(-1, 2, 4, 1),
		},
		{
			name:   "follows wide characters by the cells they take",
			before: "日本", width: 20, total: 20, rows: 3,
			expected: at(3, 2, 4, 1),
		},
		{
			name:   "is centered on its anchor when its width is odd",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.Width = template.Length{N: 3} },
			expected: at(3, 2, 3, 1),
		},
		{
			name:   "covers its anchor alone when it is one cell wide",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.Width = template.Length{N: 1} },
			expected: at(4, 2, 1, 1),
		},
		{
			name:   "is half a row up when its height is even",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.Height = 2 },
			expected: at(2, 1, 4, 2),
		},
		{
			name:   "is centered on the bar when its height is odd",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.Height = 3 },
			expected: at(2, 1, 4, 3),
		},
		{
			name:   "extends past a layout shorter than it",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.Height = 7 },
			expected: at(2, -1, 4, 7),
		},
		{
			name:   "sits on the only row",
			before: "abc", width: 20, total: 20, rows: 1,
			expected: at(2, 0, 4, 1),
		},
		{
			name:   "sits on the last of many rows",
			before: "abc", width: 20, total: 20, rows: 1000,
			expected: at(2, 999, 4, 1),
		},
		{
			name:   "is moved right and up by cells",
			before: "abc", width: 20, total: 20, rows: 3,
			spec: func(img *template.Image) {
				img.Height, img.XOffset, img.YOffset = 3, template.Length{N: 2}, -1
			},
			expected: at(4, 0, 4, 3),
		},
		{
			name:   "is moved left and down by cells",
			before: "abc", width: 20, total: 20, rows: 3,
			spec: func(img *template.Image) {
				img.XOffset, img.YOffset = template.Length{N: -3}, 2
			},
			expected: at(-1, 4, 4, 1),
		},
		{
			name:   "is moved by the most cells",
			before: "abc", width: 20, total: 20, rows: 3,
			spec: func(img *template.Image) {
				img.XOffset, img.YOffset = template.Length{N: cells}, -cells
			},
			expected: at(2+cells, 2-cells, 4, 1),
		},
		{
			name:   "is sized and moved by percentages of the layout",
			before: "abc", width: 20, total: 20, rows: 3,
			spec: func(img *template.Image) {
				img.Width = template.Length{N: 50, Percent: true}
				img.XOffset = template.Length{N: 50, Percent: true}
			},
			expected: at(9, 2, 10, 1),
		},
		{
			name:   "truncates percentages toward zero",
			before: "abc", width: 20, total: 20, rows: 3,
			spec: func(img *template.Image) {
				img.Width = template.Length{N: 33, Percent: true}
				img.XOffset = template.Length{N: -9, Percent: true}
			},
			expected: at(0, 2, 6, 1),
		},
		{
			name:   "spans the layout at 100%",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.Width = template.Length{N: 100, Percent: true} },
			expected: at(-6, 2, 20, 1),
		},
		{
			name:   "is moved left by a negative percentage",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.XOffset = template.Length{N: -50, Percent: true} },
			expected: at(-8, 2, 4, 1),
		},
		{
			name:   "takes percentages of the layout, not of the bar",
			before: "abc", width: 20, total: 40, rows: 3,
			spec: func(img *template.Image) {
				img.Width = template.Length{N: 50, Percent: true}
				img.XOffset = template.Length{N: 25, Percent: true}
			},
			expected: at(4, 2, 20, 1),
		},
		{
			name:   "keeps its cells in a layout with no width",
			before: "abc", width: 20, total: 0, rows: 3,
			spec:     func(img *template.Image) { img.XOffset = template.Length{N: 50, Percent: true} },
			expected: at(2, 2, 4, 1),
		},
		{
			name:   "is moved by pixels, overflows and shifts with the bar",
			before: "abc", width: 20, total: 20, rows: 3,
			spec: func(img *template.Image) {
				img.XOffsetPixels, img.YOffsetPixels, img.Overflow = 3, -5, true
			},
			shifted: true,
			expected: with(at(2, 2, 4, 1), func(p *placed) {
				p.Offset, p.Overflow, p.VerticalRenderOffset = image.Pt(3, -5), true, true
			}),
		},
		{
			name:   "is moved by the most pixels",
			before: "abc", width: 20, total: 20, rows: 3,
			spec: func(img *template.Image) {
				img.XOffsetPixels, img.YOffsetPixels = -pixels, pixels
			},
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.Offset = image.Pt(-pixels, pixels) }),
		},
		{
			name:   "overflows without shifting",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.Overflow = true },
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.Overflow = true }),
		},
		{
			name:   "shifts without overflowing",
			before: "abc", width: 20, total: 20, rows: 3,
			shifted:  true,
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.VerticalRenderOffset = true }),
		},
		{
			name:   "is drawn over the text at the highest z_index",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.ZIndex = math.MaxInt32 },
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.Layer = term.ImageLayerAboveText }),
		},
		{
			name:   "is drawn under the text at a negative z_index",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.ZIndex = -1 },
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.Layer = term.ImageLayerBelowText }),
		},
		{
			name:   "is drawn over the backgrounds at ZIndexBackground",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.ZIndex = template.ZIndexBackground },
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.Layer = term.ImageLayerBelowText }),
		},
		{
			name:   "is drawn under the backgrounds below ZIndexBackground",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.ZIndex = template.ZIndexBackground - 1 },
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.Layer = term.ImageLayerBelowBackground }),
		},
		{
			name:   "is drawn under the backgrounds at the lowest z_index",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.ZIndex = math.MinInt32 },
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.Layer = term.ImageLayerBelowBackground }),
		},
		{
			name:   "keeps its proportions with fit contain",
			before: "abc", width: 20, total: 20, rows: 3,
			spec:     func(img *template.Image) { img.Fit = term.ImageFitContain },
			expected: with(at(2, 2, 4, 1), func(p *placed) { p.Fit = term.ImageFitContain }),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irq := make(interrupts, 1)
			el := newImageElementWith(t, "<%s>", imageSpec(t, tt.spec), irq)
			items := []component.Floating{component.NewString(tt.before), el}
			template.PlaceImages(items, tt.x, tt.width, tt.total, tt.rows)

			rec := drawLoaded(t, el, tt.shifted, irq)
			require.Len(t, rec.images, 1)
			img := rec.images[0]
			assert.Equal(t, tt.expected, placed{
				Pos: img.Pos, Width: img.Width, Height: img.Height,
				Offset: img.Offset, Overflow: img.Overflow,
				VerticalRenderOffset: img.VerticalRenderOffset,
				Layer:                img.Layer, Fit: img.Fit,
			})
			assert.Equal(t, image.Rectangle{}, img.Clip, "the writer cuts the image off")
		})
	}
}

func TestPlaceImagesHidesImages(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tests := []struct {
		name         string
		width        template.Length
		total, rows  int
		neverPlaced  bool
		shownAtFirst bool
	}{
		{name: "without rows", width: template.Length{N: 4}, total: 20, rows: 0},
		{name: "with negative rows", width: template.Length{N: 4}, total: 20, rows: -3},
		{name: "with the fewest rows", width: template.Length{N: 4}, total: 20, rows: math.MinInt},
		{name: "when a percentage width rounds to no cells", width: template.Length{N: 1, Percent: true}, total: 20, rows: 3},
		{name: "in a layout with no width", width: template.Length{N: 50, Percent: true}, total: 0, rows: 3},
		{name: "in a layout with a negative width", width: template.Length{N: 50, Percent: true}, total: -20, rows: 3},
		{name: "before it is placed", width: template.Length{N: 4}, neverPlaced: true},
		{name: "once placed again without rows", width: template.Length{N: 4}, total: 20, rows: 0, shownAtFirst: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irq := make(interrupts, 1)
			el := newImageElementWith(t, "%s", imageSpec(t, func(img *template.Image) {
				img.Width = tt.width
			}), irq)
			items := []component.Floating{el}
			if tt.shownAtFirst {
				template.PlaceImages(items, 0, 20, 20, 3)
				drawLoaded(t, el, false, irq)
			}
			if !tt.neverPlaced {
				template.PlaceImages(items, 0, 20, tt.total, tt.rows)
			}
			rec := &recorder{}
			el.DrawImage(rec, false)
			assert.Empty(t, rec.images)
			assert.Zero(t, rec.cells)
		})
	}
}

func TestPlaceImagesHidesImagesTheBarCutsOff(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tests := []struct {
		name string
		// before and after are laid out around the image element "<%s>",
		// which takes up 4 cells.
		before, after string
		width, maxCut int
		shown         bool
	}{
		{name: "is shown when the bar fits it", before: "abc", width: 7, shown: true},
		{name: "is shown when the bar cuts max_cut of its cells", before: "abc", width: 6, maxCut: 1, shown: true},
		{name: "is hidden when the bar cuts more than max_cut of its cells", before: "abc", width: 5, maxCut: 1},
		{name: "is hidden when the bar cuts any of its cells at no max_cut", before: "abc", width: 6},
		{name: "is hidden when the bar cuts all of its cells", before: "abc", width: 3, maxCut: 3},
		{name: "is hidden past the elements the bar cuts", before: "abcdefgh", width: 6, maxCut: 4},
		{name: "is hidden in a bar with a negative width", before: "abc", width: -5, maxCut: 6},
		{name: "is shown when the bar cuts only what follows it", before: "abc", after: "defgh", width: 7, shown: true},
		{name: "is shown in no width at the most max_cut", before: "abc", maxCut: template.MaxImageCells, shown: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irq := make(interrupts, 1)
			el := newImageElementWith(t, "<%s>", imageSpec(t, func(img *template.Image) {
				img.MaxCut = tt.maxCut
			}), irq)
			items := []component.Floating{
				component.NewString(tt.before), el, component.NewString(tt.after),
			}
			template.PlaceImages(items, 0, 20, 20, 3)
			drawLoaded(t, el, false, irq)

			template.PlaceImages(items, 0, tt.width, 20, 3)
			if tt.shown {
				rec := drawLoaded(t, el, false, irq)
				assert.Len(t, rec.images, 1)
				return
			}
			rec := &recorder{}
			el.DrawImage(rec, false)
			assert.Empty(t, rec.images)
			assert.Zero(t, rec.cells)
		})
	}
}

func TestPlaceImagesWalksTheBar(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	// item is a literal, or an image element when tmpl is not empty.
	type item struct {
		text, tmpl string
		reserve    int
	}
	text := func(s string) item { return item{text: s} }
	img := func(tmpl string, reserve int) item { return item{tmpl: tmpl, reserve: reserve} }
	tests := []struct {
		name     string
		items    []item
		x, width int
		// wantAnchors are the cells the images are centered on, in order.
		wantAnchors []int
	}{
		{
			name:        "anchors every image after what precedes it",
			items:       []item{text("ab"), img("%s", 2), text("cde"), img("<%s>", 1)},
			width:       20,
			wantAnchors: []int{2, 8},
		},
		{
			name:        "anchors adjacent images",
			items:       []item{img("%s", 2), img("%s", 3), img("%s", 1)},
			width:       20,
			wantAnchors: []int{0, 2, 5},
		},
		{
			name:        "anchors images that reserve nothing on the next element",
			items:       []item{img("%s", 0), text("ab"), img("%s", 0)},
			width:       20,
			wantAnchors: []int{0, 2},
		},
		{
			name:        "anchors images past elements cut to the width left",
			items:       []item{text("ab"), img("%s", 2), text("cde"), img("<%s>", 1)},
			width:       3,
			wantAnchors: []int{2, 4},
		},
		{
			name:        "anchors images in no width from x",
			items:       []item{text("ab"), img("%s", 2), text("cde"), img("<%s>", 1)},
			x:           7,
			wantAnchors: []int{7, 8},
		},
		{
			name:        "anchors images from a negative x",
			items:       []item{text("ab"), img("%s", 2), text("cde"), img("<%s>", 1)},
			x:           -5,
			width:       20,
			wantAnchors: []int{-3, 3},
		},
		{name: "anchors nothing among literals", items: []item{text("ab"), text("cd")}, width: 20},
		{name: "anchors nothing without items", width: 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var items []component.Floating
			var imgs []*template.ImageElement
			var irqs []interrupts
			for _, it := range tt.items {
				if it.tmpl == "" {
					items = append(items, component.NewString(it.text))
					continue
				}
				irq := make(interrupts, 1)
				el := newImageElementWith(t, it.tmpl, imageSpec(t, func(img *template.Image) {
					img.Width, img.Reserve, img.MaxCut = template.Length{N: 1}, it.reserve, template.MaxImageCells
				}), irq)
				items, imgs, irqs = append(items, el), append(imgs, el), append(irqs, irq)
			}
			template.PlaceImages(items, tt.x, tt.width, 20, 2)

			var anchors []int
			for i, el := range imgs {
				rec := drawLoaded(t, el, false, irqs[i])
				require.Len(t, rec.images, 1)
				assert.Equal(t, 1, rec.images[0].Pos.Y, "image %d is on the bar", i)
				anchors = append(anchors, rec.images[0].Pos.X)
			}
			assert.Equal(t, tt.wantAnchors, anchors)
		})
	}
}

func TestImageElementDrawsAltText(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tests := []struct {
		name string
		spec func(*template.Image)
		// loads keeps the image loadable: the frame cannot draw images,
		// so it draws the alt text in their place.
		loads    bool
		expected []string
	}{
		{
			name:     "on its anchor",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 1}, "x" },
			expected: []string{"        ", "        ", "   x    "},
		},
		{
			name:     "in place of an image the writer cannot draw",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 1}, "x" },
			loads:    true,
			expected: []string{"        ", "        ", "   x    "},
		},
		{
			name:     "centered over its cells",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 4}, "x" },
			expected: []string{"        ", "        ", "  x     "},
		},
		{
			name:     "centered over a percentage of the layout",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 50, Percent: true}, "ab" },
			expected: []string{"        ", "        ", "  ab    "},
		},
		{
			name:     "cut to its cells",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 3}, "abcdef" },
			expected: []string{"        ", "        ", "  abc   "},
		},
		{
			name:     "with spaces kept",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 3}, " b " },
			expected: []string{"        ", "        ", "   b    "},
		},
		{
			name:     "with a wide glyph that fits",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 2}, "日" },
			expected: []string{"        ", "        ", "  日     "},
		},
		{
			name:     "without a wide glyph that does not fit",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 1}, "日" },
			expected: []string{"        ", "        ", "        "},
		},
		{
			name:     "without an emoji that does not fit",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 1}, "👨‍👩‍👧" },
			expected: []string{"        ", "        ", "        "},
		},
		{
			name:     "with the wide glyphs that fit",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 5}, "日本語" },
			expected: []string{"        ", "        ", " 日 本    "},
		},
		{
			name: "over its rows",
			spec: func(img *template.Image) {
				img.Width, img.Height, img.Alt = template.Length{N: 2}, 2, "ab\ncd"
			},
			expected: []string{"        ", "  ab    ", "  cd    "},
		},
		{
			name:     "cut to its rows",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 1}, "a\nb\nc" },
			expected: []string{"        ", "        ", "   a    "},
		},
		{
			name:     "as nothing when it is empty",
			spec:     func(img *template.Image) { img.Width, img.Alt = template.Length{N: 1}, "" },
			expected: []string{"        ", "        ", "        "},
		},
		{
			name: "unmoved by pixel offsets",
			spec: func(img *template.Image) {
				img.Width, img.Alt = template.Length{N: 1}, "x"
				img.XOffsetPixels, img.YOffsetPixels = template.MaxImagePixelOffset, -template.MaxImagePixelOffset
			},
			expected: []string{"        ", "        ", "   x    "},
		},
		{
			name: "moved by cell offsets",
			spec: func(img *template.Image) {
				img.Width, img.Alt = template.Length{N: 1}, "x"
				img.XOffset, img.YOffset = template.Length{N: 2}, -1
			},
			expected: []string{"        ", "     x  ", "        "},
		},
		{
			name: "moved by a percentage of the layout",
			spec: func(img *template.Image) {
				img.Width, img.Alt = template.Length{N: 1}, "x"
				img.XOffset = template.Length{N: 50, Percent: true}
			},
			expected: []string{"        ", "        ", "       x"},
		},
		{
			name: "cut off past the left of the frame",
			spec: func(img *template.Image) {
				img.Width, img.Alt = template.Length{N: 3}, "abc"
				img.XOffset = template.Length{N: -4}
			},
			expected: []string{"        ", "        ", "c       "},
		},
		{
			name: "cut off past the right of the frame",
			spec: func(img *template.Image) {
				img.Width, img.Alt = template.Length{N: 3}, "abc"
				img.XOffset = template.Length{N: 4}
			},
			expected: []string{"        ", "        ", "      ab"},
		},
		{
			name: "cut off past the top of the frame",
			spec: func(img *template.Image) {
				img.Width, img.Height, img.Alt = template.Length{N: 1}, 3, "a\nb\nc"
				img.YOffset = -3
			},
			expected: []string{"   c    ", "        ", "        "},
		},
		{
			name: "cut off past the bottom of the frame",
			spec: func(img *template.Image) {
				img.Width, img.Alt = template.Length{N: 1}, "x"
				img.YOffset = 1
			},
			expected: []string{"        ", "        ", "        "},
		},
		{
			name: "cut off even when the image overflows",
			spec: func(img *template.Image) {
				img.Width, img.Alt, img.Overflow = template.Length{N: 3}, "abc", true
				img.XOffset = template.Length{N: -4}
			},
			expected: []string{"        ", "        ", "c       "},
		},
		{
			name: "over the text whatever the z_index",
			spec: func(img *template.Image) {
				img.Width, img.Alt, img.ZIndex = template.Length{N: 1}, "x", math.MinInt32
			},
			expected: []string{"        ", "        ", "   x    "},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irq := make(interrupts, 1)
			el := newImageElementWith(t, "%s", imageSpec(t, func(img *template.Image) {
				if !tt.loads {
					img.URI += ".missing"
				}
				tt.spec(img)
			}), irq)
			template.PlaceImages([]component.Floating{component.NewString("abc"), el}, 0, 20, 8, 3)

			want := strings.Join(tt.expected, "\n")
			drawUntil(t, el, 8, 3, irq, func(frame string) bool { return frame == want })
		})
	}
}

func TestImageElementLifecycle(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tests := []struct {
		name string
		run  func(t *testing.T, el *template.ImageElement, irq interrupts)
	}{
		{
			name: "draws no image before it is placed",
			run: func(t *testing.T, el *template.ImageElement, _ interrupts) {
				rec := &recorder{}
				el.DrawImage(rec, true)
				assert.Empty(t, rec.images)
				assert.Zero(t, rec.cells)
			},
		},
		{
			name: "draws no literals before it is resized",
			run: func(t *testing.T, el *template.ImageElement, _ interrupts) {
				rec := &recorder{}
				el.Draw(rec)
				assert.Zero(t, rec.cells)
			},
		},
		{
			name: "draws its literals cut to a smaller size",
			run: func(t *testing.T, el *template.ImageElement, _ interrupts) {
				w := term.NewStringWriter(3, 1)
				el.Resize(3, 1)
				el.Draw(w)
				require.NoError(t, w.Flush())
				assert.Equal(t, "ab ", w.String())
			},
		},
		{
			name: "draws nothing in no size",
			run: func(t *testing.T, el *template.ImageElement, _ interrupts) {
				rec := &recorder{}
				el.Resize(0, 0)
				el.Draw(rec)
				el.Resize(-1, -1)
				el.Draw(rec)
				assert.Zero(t, rec.cells)
			},
		},
		{
			name: "draws the image again where it is placed again",
			run: func(t *testing.T, el *template.ImageElement, irq interrupts) {
				template.PlaceImages([]component.Floating{el}, 0, 20, 20, 3)
				drawLoaded(t, el, false, irq)
				template.PlaceImages([]component.Floating{el}, 5, 20, 20, 1)
				rec := drawLoaded(t, el, false, irq)
				require.Len(t, rec.images, 1)
				// 5 + len("ab") - 4/2
				assert.Equal(t, term.Coordinates{X: 5, Y: 0}, rec.images[0].Pos)
			},
		},
		{
			name: "closes more than once",
			run: func(t *testing.T, el *template.ImageElement, _ interrupts) {
				assert.NoError(t, el.Close())
				assert.NoError(t, el.Close())
			},
		},
		{
			name: "stops loading when closed while it loads",
			run: func(t *testing.T, el *template.ImageElement, _ interrupts) {
				template.PlaceImages([]component.Floating{el}, 0, 20, 20, 3)
				el.DrawImage(&recorder{}, false)
				assert.NoError(t, el.Close())
			},
		},
		{
			name: "never draws the image once closed",
			run: func(t *testing.T, el *template.ImageElement, irq interrupts) {
				template.PlaceImages([]component.Floating{el}, 0, 20, 20, 3)
				require.NoError(t, el.Close())
				rec := &recorder{}
				for range 3 {
					el.DrawImage(rec, false)
					select {
					case <-irq:
					case <-time.After(50 * time.Millisecond):
					}
				}
				assert.Empty(t, rec.images)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irq := make(interrupts, 1)
			el := newImageElementWith(t, "ab%scd", imageSpec(t, nil), irq)
			tt.run(t, el, irq)
		})
	}
}

func TestSortImages(t *testing.T) {
	tests := []struct {
		name   string
		zindex []int32
		// want are the indices in zindex of the sorted images.
		want []int
	}{
		{name: "nothing"},
		{name: "one image", zindex: []int32{3}, want: []int{0}},
		{name: "ties keep the layout's order", zindex: []int32{0, 0, 0}, want: []int{0, 1, 2}},
		{name: "already in order", zindex: []int32{-1, 0, 1}, want: []int{0, 1, 2}},
		{name: "in reverse order", zindex: []int32{1, 0, -1}, want: []int{2, 1, 0}},
		{name: "ties among others", zindex: []int32{0, -1, 0, -5}, want: []int{3, 1, 0, 2}},
		{
			name:   "across every layer",
			zindex: []int32{math.MaxInt32, math.MinInt32, template.ZIndexBackground, 0, template.ZIndexBackground - 1},
			want:   []int{1, 4, 2, 3, 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var imgs, want []*template.ImageElement
			for _, z := range tt.zindex {
				imgs = append(imgs, newImageElement(t, "%s", imageSpec(t, func(img *template.Image) {
					img.ZIndex = z
				})))
			}
			for _, i := range tt.want {
				want = append(want, imgs[i])
			}
			template.SortImages(imgs)
			assert.Equal(t, want, imgs)
		})
	}
}

func FuzzImageElement(f *testing.F) {
	uri := samplePictureURI(f)
	seeds := []struct {
		directives, tmpl      string
		x, width, total, rows int
		shifted               bool
	}{
		{"", "%s", 0, 20, 20, 3, false},
		{`| width 4 | height 3 | reserve 2`, "<%s>", 4, 10, 20, 3, true},
		{`| width "100%" | x_offset "-100%" | overflow`, "%s", -10, -10, -20, -1, false},
		{`| width 4096 | height 4096 | x_offset 4096 | y_offset -4096`, "%s", 1 << 20, 1 << 20, 1 << 20, 1 << 20, true},
		{`| x_pixel_offset -65536 | y_pixel_offset 65536 | z_index -2147483648`, "日本%s🙂", 0, 2, 2, 1, false},
		{`| alt "日本語" | fit "fill" | ttl "0"`, "\x00\t%s\n%d%%s", math.MinInt, math.MaxInt, math.MinInt, math.MaxInt, true},
		{`| alt ""`, "\xff█▓%s░", math.MaxInt, math.MinInt, math.MaxInt, math.MinInt, false},
	}
	for _, s := range seeds {
		f.Add(s.directives, s.tmpl, s.x, s.width, s.total, s.rows, s.shifted)
	}
	f.Fuzz(func(t *testing.T, directives, tmpl string, x, width, total, rows int, shifted bool) {
		trees, err := parse.Parse("layout", fmt.Sprintf("{{ .Image | src %q %s }}", uri, directives),
			"{{", "}}", template.AllowedFuncs())
		if err != nil {
			return
		}
		node, ok := trees["layout"].Root.Nodes[0].(*parse.ActionNode)
		if !ok {
			return
		}
		act, err := template.ParseAction(node)
		if err != nil {
			return
		}
		el, err := template.NewImageElement(act.Image, tmpl, act.Attributes, term.ColorDefault,
			storagestub.NewInMemoryService(), term.NopInterrupter())
		require.NoError(t, err, "a parsed image is valid")
		defer func() { assert.NoError(t, el.Close()) }()

		w, h := el.Dimensions()
		require.GreaterOrEqual(t, w, act.Image.Reserve)
		require.LessOrEqual(t, h, 1, "the literals stay on the bar's row")
		el.Resize(w, h)
		el.Draw(term.NewStringWriter(w, h))

		// A layout is no more cells across than a screen.
		layout := func(n int) int { return n % (2 * template.MaxImageCells) }
		total = layout(total)
		items := []component.Floating{component.NewString("ab"), el}
		template.PlaceImages(items, layout(x), layout(width), total, layout(rows))
		rec := &recorder{}
		frame := term.NewStringWriter(16, 4)
		for range 3 {
			el.DrawImage(rec, shifted)
			el.DrawImage(term.BoundsCheckWriter(16, 4, frame), shifted)
		}
		for _, img := range rec.images {
			assert.Positive(t, img.Width)
			assert.LessOrEqual(t, img.Width, max(template.MaxImageCells, total))
			assert.Equal(t, act.Image.Height, img.Height)
		}
	})
}

// imageSpec returns a 4 cells wide image of the sample picture, with fn
// applied when it is not nil.
func imageSpec(t *testing.T, fn func(*template.Image)) template.Image {
	t.Helper()
	img := template.Image{
		URI:     samplePictureURI(t),
		Width:   template.Length{N: 4},
		Height:  1,
		Reserve: 2,
		MaxCut:  template.DefaultImageMaxCut,
		Fit:     term.ImageFitFill,
		Alt:     template.DefaultImageAlt,
		TTL:     time.Hour,
	}
	if fn != nil {
		fn(&img)
	}
	return img
}

// samplePictureURI writes the sample picture to a temporary directory
// and returns its file URI.
func samplePictureURI(tb testing.TB) string {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), "sample.png")
	require.NoError(tb, os.WriteFile(path, imageuritest.SamplePNG, 0o600))
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func newImageElement(t *testing.T, tmpl string, spec template.Image) *template.ImageElement {
	t.Helper()
	return newImageElementWith(t, tmpl, spec, term.NopInterrupter())
}

func newImageElementWith(
	t *testing.T, tmpl string, spec template.Image, irq term.Interrupter,
) *template.ImageElement {
	t.Helper()
	el, err := template.NewImageElement(spec, tmpl, term.Attributes{}, term.ColorDefault,
		storagestub.NewInMemoryService(), irq)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, el.Close()) })
	return el
}

// drawLoaded draws el until its image has loaded, and returns what the
// draw that placed it recorded.
func drawLoaded(
	t *testing.T, el *template.ImageElement, shifted bool, irq interrupts,
) *recorder {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		rec := &recorder{}
		el.DrawImage(rec, shifted)
		if len(rec.images) > 0 {
			return rec
		}
		select {
		case <-irq:
		case <-timeout:
			t.Fatal("timed out waiting for the image to load")
		}
	}
}

// drawUntil draws el into a width x height frame, which cannot draw
// images, until done accepts the frame, and returns it.
func drawUntil(
	t *testing.T, el *template.ImageElement, width, height int, irq interrupts,
	done func(frame string) bool,
) string {
	t.Helper()
	w := term.NewStringWriter(width, height)
	timeout := time.After(5 * time.Second)
	for {
		require.NoError(t, w.Clear(term.Attributes{}))
		el.DrawImage(term.BoundsCheckWriter(width, height, w), false)
		require.NoError(t, w.Flush())
		if frame := w.String(); done(frame) {
			return frame
		}
		select {
		case <-irq:
		case <-timeout:
			t.Fatalf("timed out, last frame %q", w.String())
		}
	}
}

// recorder records the images drawn into it and counts the cells.
type recorder struct {
	images []term.Image
	cells  int
}

func (r *recorder) SetCell(term.Coordinates, term.Cell)               { r.cells++ }
func (r *recorder) UnionAttributes(term.Coordinates, term.Attributes) {}
func (r *recorder) Context() context.Context                          { return context.Background() }

func (r *recorder) DrawImage(img term.Image) bool {
	r.images = append(r.images, img)
	return true
}

// interrupts coalesces pending interrupts like the host event loop.
type interrupts chan struct{}

func (i interrupts) Interrupt(context.Context) error {
	select {
	case i <- struct{}{}:
	default:
	}
	return nil
}
