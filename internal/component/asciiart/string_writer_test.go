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

package asciiart

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestStringWriterDrawImage(t *testing.T) {
	src := loadImage("testdata/image_2.png")
	// halfClear is white on its left half and fully transparent on its
	// right half.
	halfClear := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := range 2 {
		for x := range 2 {
			halfClear.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		}
	}
	tests := []struct {
		name          string
		width, height int
		img           term.Image
		// before is written on row 0 over the dots, before the image is
		// placed; a space in it clears the dot.
		before string
		// after is written on row 0 after the image is placed.
		after    string
		expected string
	}{
		{
			name:  "contain centers the raster and leaves the letterbox",
			width: 40, height: 10,
			img: term.Image{Src: src, Width: 40, Height: 10, Fit: term.ImageFitContain},
			expected: `
........@@@@@@@@@@@@@@@@@@@@@@@@........
........@@@@#bbb##ccc#7cc;#@@@@@........
........@@@@@cb$@ccc#@;;;#@@@@@@........
........@@@@#cccb#;;;$#;;;@@@@@@........
........@@@@#cc;#;;;;#::::@@@@@@........
........@@@@@1;;;@3:::@::#@@@@@@........
........@@@@#;;:##:::#3+++#@@@@@........
........@@@@@@@@@@@@@@@@@@@@@@@@........
........@#6@#@@#+:#c@#@#@a=#=##@........
........@@@@@@@@@@@@@@@@@@@@@@@@........`,
		},
		{
			name:  "fill stretches the raster over the placement",
			width: 40, height: 10,
			img: term.Image{Src: src, Width: 40, Height: 10, Fit: term.ImageFitFill},
			expected: `
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@@@@@@@#bbbbbb@@cccccc##cccccc#@@@@@@@@@
@@@@@@@@#bbc$@@cccccb#@a;;;;;#@@@@@@@@@@
@@@@@@@#;ccccc#@c;;;;;$@#;;;;;@@@@@@@@@@
@@@@@@@#;cc;;#@b;;;;;1##:::::;@@@@@@@@@@
@@@@@@@@#;:;;;:@#3+::::@@#::+@@@@@@@@@@@
@@@@@@@#;;;::;@@::::::#@:+++++#@@@@@@@@@
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@##@:#@##@@:##W+9##=@##=@@=@@=+###+#W#@@
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@`,
		},
		{
			name:  "covers only the cells of a placement at an offset",
			width: 40, height: 10,
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 4, Y: 2},
				Width: 16, Height: 6, Fit: term.ImageFitFill,
			},
			expected: `
........................................
........................................
....@@@@@@@@@@@@@@@@....................
....@@@#c@cc#;;;@@@@....................
....@@@ccbc;;#::@@@@....................
....@@@;;;5::@:+@@@@....................
....@@@@@@@@@@@@@@@@....................
....#@####@########@....................
........................................
........................................`,
		},
		{
			name:  "clip cuts the placement",
			width: 40, height: 10,
			img: term.Image{
				Src: src, Width: 40, Height: 10, Fit: term.ImageFitFill,
				Clip: image.Rect(10, 3, 30, 7),
			},
			expected: `
........................................
........................................
........................................
..........cccc#@c;;;;;$@#;;;;;..........
..........c;;#@b;;;;;1##:::::;..........
..........:;;;:@#3+::::@@#::+@..........
..........;::;@@::::::#@:+++++..........
........................................
........................................
........................................`,
		},
		{
			name:  "crop selects a part of the source",
			width: 40, height: 10,
			img: term.Image{
				Src: src, Width: 40, Height: 10, Fit: term.ImageFitFill,
				Crop: image.Rect(0, 0, 97, 179),
			},
			expected: `
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@@@@@@@@@@@@@@@0bbbbbbbbbbb8@@@#cccccccc
@@@@@@@@@@@@@@@@##cbbbccaW#@@@;ccccccccc
@@@@@@@@@@@@@@@#cbccccccccc;##@##:;;;;;;
@@@@@@@@@@@@@@@#;bcccc;;;;5#@@#;;;;;;;;;
@@@@@@@@@@@@@@@@##1;:;;;;;;;;#@@@##c::::
@@@@@@@@@@@@@@@c;;;;;;:::::3@@@#;:::::::
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@@##:#@#67##@##a$#@@@@b:####+#0:+##a+#=9
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@`,
		},
		{
			name:  "clips a placement that leaves the writer",
			width: 40, height: 10,
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 30, Y: 6},
				Width: 20, Height: 8, Fit: term.ImageFitFill,
			},
			expected: `
........................................
........................................
........................................
........................................
........................................
........................................
..............................@@@@@@@@@@
..............................@@@@bbb@cc
..............................@@@@@@cc@@
..............................@@@@cc;#;;`,
		},
		{
			name:  "draws nothing for a placement outside the writer",
			width: 40, height: 10,
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 40, Y: 0},
				Width: 10, Height: 10, Fit: term.ImageFitFill,
			},
			expected: `
........................................
........................................
........................................
........................................
........................................
........................................
........................................
........................................
........................................
........................................`,
		},
		{
			name:  "draws nothing for an empty placement",
			width: 40, height: 10,
			img: term.Image{Src: src, Fit: term.ImageFitFill},
			expected: `
........................................
........................................
........................................
........................................
........................................
........................................
........................................
........................................
........................................
........................................`,
		},
		{
			name:  "cells written afterwards cover the image",
			width: 40, height: 10,
			img:   term.Image{Src: src, Width: 40, Height: 10, Fit: term.ImageFitFill},
			after: "drawn over",
			expected: `
drawn over@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@@@@@@@#bbbbbb@@cccccc##cccccc#@@@@@@@@@
@@@@@@@@#bbc$@@cccccb#@a;;;;;#@@@@@@@@@@
@@@@@@@#;ccccc#@c;;;;;$@#;;;;;@@@@@@@@@@
@@@@@@@#;cc;;#@b;;;;;1##:::::;@@@@@@@@@@
@@@@@@@@#;:;;;:@#3+::::@@#::+@@@@@@@@@@@
@@@@@@@#;;;::;@@::::::#@:+++++#@@@@@@@@@
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@
@##@:#@##@@:##W+9##=@##=@@=@@=+###+#W#@@
@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@`,
		},
		{
			name:  "a placement below text only shows through cells without a glyph",
			width: 12, height: 2,
			img: term.Image{
				Src: src, Width: 12, Height: 2, Fit: term.ImageFitFill,
				Layer: term.ImageLayerBelowText,
			},
			before: "text  ",
			expected: `
text#c......
............`,
		},
		{
			name:  "a placement below the background only shows through default backgrounds",
			width: 12, height: 2,
			img: term.Image{
				Src: src, Width: 12, Height: 2, Fit: term.ImageFitFill,
				Layer: term.ImageLayerBelowBackground,
			},
			before: "  ab  ",
			expected: `
@@ab  ......
............`,
		},
		{
			name:  "cells under fully transparent pixels show what is under them",
			width: 12, height: 2,
			img: term.Image{
				Src: halfClear, Pos: term.Coordinates{X: 2}, Width: 8, Height: 2,
				Fit: term.ImageFitFill,
			},
			before: "text",
			expected: `
te@@@@......
..@@@@......`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &placement{img: tt.img, before: tt.before, after: tt.after}
			p.Resize(tt.width, tt.height)
			w := NewStringWriter(tt.width, tt.height, DefaultConfig())
			comptest.TestComponent(t, p, w, []comptest.TestCase{{Expected: tt.expected}})
			assert.Equal(t, []bool{true}, p.reported)
		})
	}
}

func TestStringWriterResize(t *testing.T) {
	src := loadImage("testdata/image_2.png")
	p := &placement{img: term.Image{Src: src, Width: 40, Height: 10, Fit: term.ImageFitFill}}
	p.Resize(12, 4)
	w := NewStringWriter(40, 10, DefaultConfig())
	w.Resize(12, 4)
	comptest.TestComponent(t, p, w, []comptest.TestCase{{Expected: `
@@@@@@@@@@@@
@@@@@@@#bbbb
@@@@@@@@#bbc
@@@@@@@#;ccc`}})
}

func TestStringWriterOffset(t *testing.T) {
	at := func(offset image.Point, clip image.Rectangle) term.Image {
		return term.Image{
			Src: solidGray(128), Pos: term.Coordinates{X: 4, Y: 1}, Width: 4, Height: 1,
			Fit: term.ImageFitFill, Offset: offset, Clip: clip,
		}
	}
	tests := []struct {
		name     string
		img      term.Image
		expected string
	}{
		{
			name: "no offset",
			img:  at(image.Point{}, image.Rectangle{}),
			expected: `
............
....aaaa....
............`,
		},
		{
			name: "a cell's width moves it one cell right",
			img:  at(image.Pt(10, 0), image.Rectangle{}),
			expected: `
............
.....aaaa...
............`,
		},
		{
			name: "less than half a cell does not move it",
			img:  at(image.Pt(4, -11), image.Rectangle{}),
			expected: `
............
....aaaa....
............`,
		},
		{
			name: "half a cell or more rounds to the next cell",
			img:  at(image.Pt(15, 0), image.Rectangle{}),
			expected: `
............
......aaaa..
............`,
		},
		{
			name: "a negative offset moves it left",
			img:  at(image.Pt(-20, 0), image.Rectangle{}),
			expected: `
............
..aaaa......
............`,
		},
		{
			name: "a cell's height moves it one row down",
			img:  at(image.Pt(0, 23), image.Rectangle{}),
			expected: `
............
............
....aaaa....`,
		},
		{
			name: "more than half a cell up moves it one row up",
			img:  at(image.Pt(0, -12), image.Rectangle{}),
			expected: `
....aaaa....
............
............`,
		},
		{
			name: "the writer clips a placement moved partly off it",
			img:  at(image.Pt(-50, 0), image.Rectangle{}),
			expected: `
............
aaa.........
............`,
		},
		{
			name: "its clip confines it where it lands",
			img:  at(image.Pt(20, 0), image.Rect(0, 0, 7, 3)),
			expected: `
............
......a.....
............`,
		},
		{
			name: "it shows where it lands inside its clip although its cells are outside",
			img:  at(image.Pt(0, 23), image.Rect(0, 2, 12, 3)),
			expected: `
............
............
....aaaa....`,
		},
		{
			name: "it draws nothing when it lands outside its clip",
			img:  at(image.Pt(0, 23), image.Rect(0, 1, 12, 2)),
			expected: `
............
............
............`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &placement{img: tt.img}
			p.Resize(12, 3)
			w := NewStringWriter(12, 3, DefaultConfig())
			comptest.TestComponent(t, p, w, []comptest.TestCase{{Expected: tt.expected}})
			assert.Equal(t, []bool{true}, p.reported)
		})
	}
}

func TestStringWriterComposesPlacements(t *testing.T) {
	// a, b and c encode as rows of 'a', '6' and '=' respectively.
	a, b, c := solidGray(128), solidGray(200), solidGray(80)
	// halfB is b on its left half and fully transparent on its right half.
	halfB := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	for x := range 2 {
		halfB.SetNRGBA(x, 0, color.NRGBA{R: 200, G: 200, B: 200, A: 255})
	}
	at := func(src image.Image, x, width int, layer term.ImageLayer) term.Image {
		return term.Image{
			Src: src, Pos: term.Coordinates{X: x}, Width: width, Height: 1,
			Fit: term.ImageFitFill, Layer: layer,
		}
	}
	const (
		above      = term.ImageLayerAboveText
		below      = term.ImageLayerBelowText
		background = term.ImageLayerBelowBackground
	)
	tests := []struct {
		name string
		// under is written before the placements: '~' is a blank cell with
		// a background color, and any other rune a cell with that glyph.
		under string
		imgs  []term.Image
		// after is written over the placements from the first cell.
		after    string
		expected string
	}{
		{
			name:     "nothing placed",
			under:    "text        ",
			expected: "text        ",
		},
		{
			name:     "placements that do not overlap",
			under:    "            ",
			imgs:     []term.Image{at(a, 0, 4, above), at(b, 8, 4, below)},
			expected: "aaaa    6666",
		},
		{
			name:     "a later placement above text covers an earlier one",
			under:    "text        ",
			imgs:     []term.Image{at(a, 0, 6, above), at(b, 3, 6, above)},
			expected: "aaa666666   ",
		},
		{
			name:     "a later placement below text covers an earlier one",
			under:    "text        ",
			imgs:     []term.Image{at(a, 0, 8, below), at(b, 6, 6, below)},
			expected: "textaa666666",
		},
		{
			name:     "a later placement below the background covers an earlier one",
			under:    "  ~~        ",
			imgs:     []term.Image{at(a, 0, 8, background), at(b, 6, 6, background)},
			expected: "aa  aa666666",
		},
		{
			name:     "a later placement below text stays under an earlier one above it",
			under:    "            ",
			imgs:     []term.Image{at(a, 0, 6, above), at(b, 3, 6, below)},
			expected: "aaaaaa666   ",
		},
		{
			name:     "a later placement below the background stays under an earlier one below text",
			under:    "            ",
			imgs:     []term.Image{at(a, 0, 6, below), at(b, 3, 6, background)},
			expected: "aaaaaa666   ",
		},
		{
			name:     "a later placement below text covers an earlier one below the background",
			under:    "  ~~        ",
			imgs:     []term.Image{at(a, 0, 8, background), at(b, 3, 6, below)},
			expected: "aa 666666   ",
		},
		{
			name:     "a placement below text stays under a glyph an earlier placement covers",
			under:    "text        ",
			imgs:     []term.Image{at(a, 0, 2, above), at(b, 0, 6, below)},
			expected: "aaxt66      ",
		},
		{
			name:     "the transparent pixels of the top placement show the one under it",
			under:    "            ",
			imgs:     []term.Image{at(a, 0, 8, above), at(halfB, 2, 4, above)},
			expected: "aa66aaaa    ",
		},
		{
			name:  "the transparent pixels of a placement show every layer under it",
			under: "  ~~        ",
			imgs: []term.Image{
				at(c, 0, 12, background), at(halfB, 0, 8, below), at(a, 10, 2, above),
			},
			expected: "6666======aa",
		},
		{
			name:     "cells written after the placements cover all of them",
			under:    "            ",
			imgs:     []term.Image{at(a, 0, 6, below), at(b, 3, 6, above)},
			after:    "xyzw",
			expected: "xyzw66666   ",
		},
		{
			name:     "a placement below text stays under a glyph written over earlier placements",
			under:    "            ",
			imgs:     []term.Image{at(a, 0, 6, above)},
			after:    "xy",
			expected: "xyaaaa      ",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &scene{under: tt.under, imgs: tt.imgs, after: tt.after}
			s.Resize(12, 1)
			w := NewStringWriter(12, 1, DefaultConfig())
			comptest.TestComponent(t, s, w, []comptest.TestCase{
				{Expected: "\n" + tt.expected},
				{Expected: "\n" + tt.expected},
			})
		})
	}
}

func TestStringWriterComposesAfterClear(t *testing.T) {
	w := NewStringWriter(4, 1, DefaultConfig())
	w.DrawImage(term.Image{Src: solidGray(128), Width: 4, Height: 1, Fit: term.ImageFitFill})
	assert.NoError(t, w.Clear(term.Attributes{}))
	w.DrawImage(term.Image{
		Src: solidGray(200), Width: 4, Height: 1, Fit: term.ImageFitFill,
		Layer: term.ImageLayerBelowText,
	})
	assert.NoError(t, w.Flush())
	assert.Equal(t, "6666", w.String())
}

func solidGray(v uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 4, 1))
	for x := range 4 {
		img.SetNRGBA(x, 0, color.NRGBA{R: v, G: v, B: v, A: 255})
	}
	return img
}

// scene writes under on row 0, then places imgs in order and writes
// after over them.
type scene struct {
	under         string
	imgs          []term.Image
	after         string
	width, height int
}

func (s *scene) Resize(width, height int) {
	s.width, s.height = width, height
}

func (s *scene) Draw(w term.Writer) {
	for x, r := range []rune(s.under) {
		c := term.NewCell(r, 1, term.Attributes{})
		if r == '~' {
			c = term.NewCell(' ', 1, term.Attributes{Bg: term.ColorNavy})
		}
		w.SetCell(term.Coordinates{X: x}, c)
	}
	for _, img := range s.imgs {
		w.DrawImage(img)
	}
	for x, r := range s.after {
		w.SetCell(term.Coordinates{X: x}, term.NewCell(r, 1, term.Attributes{}))
	}
}

// placement fills its area with dots, writes before on row 0, places img
// and then writes after on row 0, recording what DrawImage reported.
// The last two cells of before carry a background color.
type placement struct {
	img           term.Image
	before        string
	after         string
	width, height int
	reported      []bool
}

func (p *placement) Resize(width, height int) {
	p.width, p.height = width, height
}

func (p *placement) Draw(w term.Writer) {
	for y := range p.height {
		for x := range p.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('.', 1, term.Attributes{}))
		}
	}
	before := []rune(p.before)
	for x, r := range before {
		var attrs term.Attributes
		if x >= len(before)-2 {
			attrs.Bg = term.ColorNavy
		}
		w.SetCell(term.Coordinates{X: x}, term.NewCell(r, 1, attrs))
	}
	p.reported = append(p.reported, w.DrawImage(p.img))
	for x, r := range p.after {
		w.SetCell(term.Coordinates{X: x}, term.NewCell(r, 1, term.Attributes{}))
	}
}
