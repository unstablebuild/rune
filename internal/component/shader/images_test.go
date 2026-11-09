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
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestImageCutterCutsPlacementsToTheCellsShowingThem(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	picture := term.Image{Src: src, ID: 7, Pos: term.Coordinates{X: 2, Y: 1}, Width: 4, Height: 3}
	clipped := picture
	clipped.Clip = image.Rect(2, 1, 6, 3)
	tests := []struct {
		name   string
		images []term.Image
		hidden []image.Point
		want   []image.Rectangle
	}{
		{
			name: "shown everywhere", images: []term.Image{picture},
			want: []image.Rectangle{image.Rect(2, 1, 6, 4)},
		},
		{
			name: "hidden elsewhere", images: []term.Image{picture},
			hidden: []image.Point{{0, 0}, {1, 1}, {6, 3}, {7, 4}},
			want:   []image.Rectangle{image.Rect(2, 1, 6, 4)},
		},
		{
			name: "hidden on a cell", images: []term.Image{picture},
			hidden: []image.Point{{3, 2}},
			want: []image.Rectangle{
				image.Rect(2, 1, 6, 2), image.Rect(2, 2, 3, 3),
				image.Rect(4, 2, 6, 3), image.Rect(2, 3, 6, 4),
			},
		},
		{
			name: "hidden on a column", images: []term.Image{picture},
			hidden: []image.Point{{4, 1}, {4, 2}, {4, 3}},
			want:   []image.Rectangle{image.Rect(2, 1, 4, 4), image.Rect(5, 1, 6, 4)},
		},
		{
			name: "keeps clips", images: []term.Image{clipped},
			hidden: []image.Point{{2, 1}},
			want:   []image.Rectangle{image.Rect(3, 1, 6, 2), image.Rect(2, 2, 6, 3)},
		},
		{
			name: "hidden everywhere", images: []term.Image{picture},
			hidden: []image.Point{
				{2, 1}, {3, 1}, {4, 1}, {5, 1}, {2, 2}, {3, 2}, {4, 2}, {5, 2},
				{2, 3}, {3, 3}, {4, 3}, {5, 3},
			},
		},
		{name: "nothing to cut"},
	}
	var c imageCutter
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shows := func(x, y int) bool {
				for _, p := range tt.hidden {
					if p == image.Pt(x, y) {
						return false
					}
				}
				return true
			}
			lead := term.Image{ID: 1}
			got := c.cut([]term.Image{lead}, tt.images, 8, 5, shows)
			require.Equal(t, lead, got[0], "appends to dst")
			var clips []image.Rectangle
			for _, img := range got[1:] {
				assert.Equal(t, picture.ID, img.ID)
				assert.Equal(t, picture.Bounds(), img.Bounds())
				clips = append(clips, img.Clip)
			}
			assert.Equal(t, tt.want, clips)
		})
	}
}

func TestImageCutterFollowsTheScreenSize(t *testing.T) {
	picture := term.Image{Src: image.NewRGBA(image.Rect(0, 0, 1, 1)), ID: 7, Width: 30, Height: 20}
	var c imageCutter
	all := func(int, int) bool { return true }
	for _, size := range []image.Point{{8, 5}, {20, 10}, {8, 5}} {
		got := c.cut(nil, []term.Image{picture}, size.X, size.Y, all)
		require.Len(t, got, 1)
		assert.Equal(t, image.Rectangle{Max: size}, got[0].Clip)
	}
}

func TestCopyImagesCopiesThePixelsShown(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i := range src.Pix {
		src.Pix[i] = uint8(i)
	}
	picture := term.Image{Src: src, ID: 7, Version: 3, Pos: term.Coordinates{X: 1}, Width: 2, Height: 1}
	left, right := picture, picture
	left.Clip, right.Clip = image.Rect(1, 0, 2, 1), image.Rect(2, 0, 3, 1)
	cropped := picture
	cropped.Crop = image.Rect(1, 1, 3, 2)
	anonymous := term.Image{Src: src, Width: 1, Height: 1}

	got := copyImages([]term.Image{left, right, cropped, anonymous, anonymous})
	require.Len(t, got, 5)
	for i, want := range []term.Image{left, right, cropped, anonymous, anonymous} {
		img := got[i]
		assert.Equal(t, want.Bounds(), img.Bounds(), "placement %d", i)
		assert.Equal(t, want.Clip, img.Clip, "placement %d", i)
		assert.Empty(t, img.Crop, "placement %d", i)
		assert.NotEqual(t, want.ID, img.ID, "placement %d", i)
		assert.NotSame(t, src, img.Src, "placement %d", i)
	}
	assert.Same(t, got[0].Src, got[1].Src, "the parts of a picture share a copy")
	assert.Equal(t, got[0].ID, got[1].ID)
	assert.NotSame(t, got[3].Src, got[4].Src, "placements without an ID are told apart")
	assert.Equal(t, src.Pix, got[0].Src.(*image.RGBA).Pix)
	assert.Equal(t, src.Pix[src.PixOffset(1, 1):src.PixOffset(3, 1)], got[2].Src.(*image.RGBA).Pix,
		"copies the crop")

	src.Pix[0] = 99
	assert.Zero(t, got[0].Src.(*image.RGBA).Pix[0], "copies are left as they were")
	assert.Nil(t, copyImages(nil))
}

func TestDesaturatePixels(t *testing.T) {
	tests := []struct {
		amount float64
		in     color.RGBA
		want   color.RGBA
	}{
		{0, color.RGBA{R: 200, G: 100, B: 50, A: 255}, color.RGBA{R: 200, G: 100, B: 50, A: 255}},
		{1, color.RGBA{R: 200, G: 100, B: 50, A: 255}, color.RGBA{R: 124, G: 124, B: 124, A: 255}},
		{0.5, color.RGBA{R: 200, G: 100, B: 50, A: 255}, color.RGBA{R: 162, G: 112, B: 87, A: 255}},
		{2, color.RGBA{R: 200, G: 100, B: 50, A: 255}, color.RGBA{R: 124, G: 124, B: 124, A: 255}},
		{1, color.RGBA{R: 100, G: 50, B: 25, A: 128}, color.RGBA{R: 62, G: 62, B: 62, A: 128}},
	}
	for _, tt := range tests {
		img := image.NewRGBA(image.Rect(3, 2, 5, 3))
		img.SetRGBA(3, 2, tt.in)
		img.SetRGBA(4, 2, tt.in)
		desaturatePixels(img, tt.amount)
		assert.Equal(t, tt.want, img.RGBAAt(3, 2), "%v by %.1f", tt.in, tt.amount)
		assert.Equal(t, tt.want, img.RGBAAt(4, 2), "%v by %.1f", tt.in, tt.amount)
	}
}

func TestPaintPictures(t *testing.T) {
	red := color.RGBA{R: 255, A: 255}
	green := color.RGBA{G: 255, A: 255}
	blue := color.RGBA{B: 255, A: 255}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	pixels := func(w, h int, colors ...color.RGBA) *image.RGBA {
		src := image.NewRGBA(image.Rect(0, 0, w, h))
		for i, c := range colors {
			src.SetRGBA(i%w, i/w, c)
		}
		return src
	}
	picture := func(src *image.RGBA, r image.Rectangle) term.Image {
		return term.Image{
			Src: src, ID: term.NewImageID(), Pos: term.Coordinates{X: r.Min.X, Y: r.Min.Y},
			Width: r.Dx(), Height: r.Dy(),
		}
	}
	rgb := func(c color.RGBA) term.Color {
		return term.NewRGBColor(int32(c.R), int32(c.G), int32(c.B))
	}
	text := term.Attributes{Fg: term.ColorWhite}
	navy := term.Attributes{Fg: term.ColorWhite, Bg: term.NewRGBColor(0, 0, 200)}
	screen := func(attr term.Attributes) [][]term.Cell {
		cells := make([][]term.Cell, 3)
		for y := range cells {
			cells[y] = make([]term.Cell, 4)
			for x := range cells[y] {
				cells[y][x] = term.NewCell('x', 1, attr)
			}
		}
		return cells
	}
	painted := func(cells [][]term.Cell, bg term.Color, points ...image.Point) [][]term.Cell {
		for _, p := range points {
			cells[p.Y][p.X] = term.NewCell(' ', 1, term.Attributes{Fg: cells[p.Y][p.X].Fg, Bg: bg})
		}
		return cells
	}
	under := func(cells [][]term.Cell, bg term.Color, points ...image.Point) [][]term.Cell {
		for _, p := range points {
			cells[p.Y][p.X].Bg = bg
		}
		return cells
	}
	quadrants := picture(pixels(2, 2, red, green, blue, white), image.Rect(1, 1, 3, 3))
	layered := func(img term.Image, layer term.ImageLayer) term.Image {
		img.Layer = layer
		return img
	}
	moved := quadrants
	moved.Offset = image.Pt(int(sampleCell.Width), 0)
	clipped := quadrants
	clipped.Clip = image.Rect(2, 1, 3, 3)
	cropped := quadrants
	cropped.Crop = image.Rect(1, 0, 2, 2)
	contained := picture(pixels(1, 4, red, red, red, red), image.Rect(0, 0, 3, 1))
	contained.Fit = term.ImageFitContain
	wide := screen(text)
	wide[1][0] = term.NewCell('世', 2, text)
	wide[1][1] = term.Cell{Fg: term.ColorWhite}
	tests := []struct {
		name   string
		cells  [][]term.Cell
		images []term.Image
		want   [][]term.Cell
	}{
		{
			name:   "stretches pictures over their cells, hiding the text",
			cells:  screen(text),
			images: []term.Image{quadrants},
			want: func() [][]term.Cell {
				c := screen(text)
				painted(c, rgb(red), image.Pt(1, 1))
				painted(c, rgb(green), image.Pt(2, 1))
				painted(c, rgb(blue), image.Pt(1, 2))
				return painted(c, rgb(white), image.Pt(2, 2))
			}(),
		},
		{
			name:   "averages the pixels over a cell",
			cells:  screen(text),
			images: []term.Image{picture(pixels(2, 1, red, blue), image.Rect(0, 0, 1, 1))},
			want:   painted(screen(text), term.NewRGBColor(128, 0, 128), image.Pt(0, 0)),
		},
		{
			name:   "paints the cells a picture is moved onto",
			cells:  screen(text),
			images: []term.Image{moved},
			want: func() [][]term.Cell {
				c := screen(text)
				painted(c, rgb(red), image.Pt(2, 1))
				painted(c, rgb(green), image.Pt(3, 1))
				painted(c, rgb(blue), image.Pt(2, 2))
				return painted(c, rgb(white), image.Pt(3, 2))
			}(),
		},
		{
			name:   "paints only the cells a picture is clipped to",
			cells:  screen(text),
			images: []term.Image{clipped},
			want: func() [][]term.Cell {
				c := screen(text)
				painted(c, rgb(green), image.Pt(2, 1))
				return painted(c, rgb(white), image.Pt(2, 2))
			}(),
		},
		{
			name:   "paints the crop of a picture",
			cells:  screen(text),
			images: []term.Image{cropped},
			want: func() [][]term.Cell {
				c := screen(text)
				painted(c, rgb(green), image.Pt(1, 1), image.Pt(2, 1))
				return painted(c, rgb(white), image.Pt(1, 2), image.Pt(2, 2))
			}(),
		},
		{
			name:   "leaves the cells around a contained picture",
			cells:  screen(text),
			images: []term.Image{contained},
			want:   painted(screen(text), rgb(red), image.Pt(1, 0)),
		},
		{
			name:   "keeps the text over a picture below it",
			cells:  screen(text),
			images: []term.Image{layered(quadrants, term.ImageLayerBelowText)},
			want: func() [][]term.Cell {
				c := screen(text)
				under(c, rgb(red), image.Pt(1, 1))
				under(c, rgb(green), image.Pt(2, 1))
				under(c, rgb(blue), image.Pt(1, 2))
				return under(c, rgb(white), image.Pt(2, 2))
			}(),
		},
		{
			name: "shows a picture below the background only through the default one",
			cells: func() [][]term.Cell {
				c := screen(text)
				c[1][2].Bg, c[2][2].Bg = navy.Bg, navy.Bg
				return c
			}(),
			images: []term.Image{layered(quadrants, term.ImageLayerBelowBackground)},
			want: func() [][]term.Cell {
				c := screen(text)
				c[1][2].Bg, c[2][2].Bg = navy.Bg, navy.Bg
				under(c, rgb(red), image.Pt(1, 1))
				return under(c, rgb(blue), image.Pt(1, 2))
			}(),
		},
		{
			name:  "paints the layers from the bottom up",
			cells: screen(text),
			images: []term.Image{
				picture(pixels(1, 1, red), image.Rect(0, 0, 1, 1)),
				layered(picture(pixels(1, 1, blue), image.Rect(0, 0, 2, 1)), term.ImageLayerBelowText),
			},
			want: func() [][]term.Cell {
				c := screen(text)
				painted(c, rgb(red), image.Pt(0, 0))
				return under(c, rgb(blue), image.Pt(1, 0))
			}(),
		},
		{
			name:   "blends a translucent picture into a background of a known color",
			cells:  screen(navy),
			images: []term.Image{picture(pixels(1, 1, color.RGBA{R: 128, A: 128}), image.Rect(0, 0, 1, 1))},
			want:   painted(screen(navy), term.NewRGBColor(128, 0, 100), image.Pt(0, 0)),
		},
		{
			name:  "paints the default background where a picture covers at least half of it",
			cells: screen(text),
			images: []term.Image{
				picture(pixels(1, 1, color.RGBA{R: 128, A: 128}), image.Rect(0, 0, 1, 1)),
				picture(pixels(1, 1, color.RGBA{G: 64, A: 64}), image.Rect(1, 0, 2, 1)),
			},
			want: painted(screen(text), rgb(red), image.Pt(0, 0)),
		},
		{
			name:   "leaves the cells under transparent pixels",
			cells:  screen(text),
			images: []term.Image{picture(pixels(1, 1), image.Rect(0, 0, 4, 3))},
			want:   screen(text),
		},
		{
			name:   "hides the whole of a wide character",
			cells:  wide,
			images: []term.Image{picture(pixels(1, 1, red), image.Rect(1, 1, 2, 2))},
			want: func() [][]term.Cell {
				c := screen(text)
				c[1][0] = term.NewCell(' ', 1, text)
				return painted(c, rgb(red), image.Pt(1, 1))
			}(),
		},
		{
			name:   "clips pictures to the screen",
			cells:  screen(text),
			images: []term.Image{picture(pixels(1, 1, red), image.Rect(3, 2, 6, 5))},
			want:   painted(screen(text), rgb(red), image.Pt(3, 2)),
		},
		{
			name:   "skips pictures without pixels",
			cells:  screen(text),
			images: []term.Image{{Width: 2, Height: 2}, picture(pixels(0, 0), image.Rect(0, 0, 2, 2))},
			want:   screen(text),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paintPictures(tt.cells, tt.images)
			assert.Equal(t, tt.want, tt.cells)
		})
	}
}

func TestPaintPicturesOnRaggedScreens(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 255, A: 255})
	img := term.Image{Src: src, Width: 3, Height: 3}
	cells := [][]term.Cell{nil, {{}}, {{}, {}, {}}}
	assert.NotPanics(t, func() { paintPictures(cells, []term.Image{img}) })
	assert.Equal(t, term.NewRGBColor(255, 0, 0), cells[1][0].Bg)
	assert.Equal(t, term.NewRGBColor(255, 0, 0), cells[2][2].Bg)
	assert.NotPanics(t, func() { paintPictures(nil, []term.Image{img}) })
}
