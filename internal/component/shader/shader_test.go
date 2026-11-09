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

package shader

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestShadeImages(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, A: 255})
	picture := term.Image{Src: src, ID: term.NewImageID(), Pos: term.Coordinates{X: 1}, Width: 2, Height: 1}
	faintSrc := image.NewRGBA(image.Rect(0, 0, 1, 1))
	faintSrc.SetRGBA(0, 0, color.RGBA{R: 50, G: 20, A: 60})
	faint := picture
	faint.Src, faint.ID = faintSrc, term.NewImageID()
	bg := term.NewRGBColor(40, 40, 40)
	screen := func(ch rune, bgs ...term.Color) [][]term.Cell {
		row := make([]term.Cell, len(bgs))
		for x, bg := range bgs {
			row[x] = term.NewCell(ch, 1, term.Attributes{Bg: bg})
		}
		return [][]term.Cell{row}
	}
	tests := []struct {
		name       string
		picture    *term.Image
		shader     Shader
		want       [][]term.Cell
		wantImages []term.Image
	}{
		{
			name:   "a shader of cells shades the pictures painted onto them in place of drawing them",
			shader: halveBg{},
			want: func() [][]term.Cell {
				cells := screen(' ', term.NewRGBColor(20, 20, 20), term.NewRGBColor(100, 50, 0),
					term.NewRGBColor(100, 50, 0), term.NewRGBColor(20, 20, 20))
				cells[0][0].Ch, cells[0][3].Ch = 'b', 'b'
				return cells
			}(),
		},
		{
			name:   "pictures show as drawn over the cells the shader leaves as painted",
			shader: halveBg{from: 2},
			want: func() [][]term.Cell {
				cells := screen('b', bg, bg, term.NewRGBColor(100, 50, 0), term.NewRGBColor(20, 20, 20))
				cells[0][2].Ch = ' '
				return cells
			}(),
			wantImages: clipped(picture, image.Rect(1, 0, 2, 1)),
		},
		{
			name: "the color of a blank does not show",
			shader: shadeFunc(func(_, _ int, in [][]term.Cell) {
				for x := range in[0] {
					in[0][x].Fg = term.ColorRed
				}
			}),
			want: func() [][]term.Cell {
				cells := screen('b', bg, bg, bg, bg)
				cells[0][0].Fg, cells[0][3].Fg = term.ColorRed, term.ColorRed
				return cells
			}(),
			wantImages: clipped(picture, image.Rect(1, 0, 3, 1)),
		},
		{
			name:    "pictures show as drawn over the text the shader recolors",
			picture: &faint,
			shader: shadeFunc(func(_, _ int, in [][]term.Cell) {
				for x := range in[0] {
					in[0][x].Fg = term.ColorRed
				}
			}),
			want: func() [][]term.Cell {
				cells := screen('b', bg, bg, bg, bg)
				for x := range cells[0] {
					cells[0][x].Fg = term.ColorRed
				}
				return cells
			}(),
			wantImages: clipped(faint, image.Rect(1, 0, 3, 1)),
		},
		{
			name:       "an image shader shades its pictures itself",
			shader:     &imageShader{images: keepImages},
			want:       screen('a', bg, bg, bg, bg),
			wantImages: []term.Image{picture},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := picture
			if tt.picture != nil {
				img = *tt.picture
			}
			cells := screen('b', bg, bg, bg, bg)
			got := ShadeImages(tt.shader, 0, 2, cells, []term.Image{img})
			assert.Equal(t, tt.want, cells)
			assert.Equal(t, tt.wantImages, got)
		})
	}
}

func clipped(img term.Image, r image.Rectangle) []term.Image {
	img, _ = img.Clipped(r)
	return []term.Image{img}
}

// halveBg is a shader of cells that halves the RGB backgrounds of the
// cells it shades from column from on.
type halveBg struct{ from int }

func (s halveBg) Shade(_, _ int, in [][]term.Cell) {
	for _, row := range in {
		for x, c := range row {
			if x >= s.from && c.Bg.IsRGB() {
				r, g, b := c.Bg.RGB()
				row[x].Bg = term.NewRGBColor(r/2, g/2, b/2)
			}
		}
	}
}

type shadeFunc func(frame, total int, in [][]term.Cell)

func (f shadeFunc) Shade(frame, total int, in [][]term.Cell) { f(frame, total, in) }
