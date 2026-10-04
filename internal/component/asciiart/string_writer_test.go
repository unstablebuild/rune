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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestStringWriterDrawImage(t *testing.T) {
	src := loadImage("testdata/image_2.png")
	tests := []struct {
		name          string
		width, height int
		img           term.Image
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &placement{img: tt.img, after: tt.after}
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

// placement fills its area with dots, places img and then writes after
// on row 0, recording what DrawImage reported.
type placement struct {
	img           term.Image
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
	p.reported = append(p.reported, w.DrawImage(p.img))
	for x, r := range p.after {
		w.SetCell(term.Coordinates{X: x}, term.NewCell(r, 1, term.Attributes{}))
	}
}
