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

package braille_test

import (
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/braille"
)

func TestComponentDrawsImageEncodedForItsCells(t *testing.T) {
	img := fill(20, 46, func(_, y int) color.NRGBA {
		if y < 23 {
			return color.NRGBA{R: 255, A: 255}
		}
		return color.NRGBA{}
	})
	var cellW, cellH int
	c := braille.NewComponent(img, func() (int, int) { return cellW, cellH }, braille.Dots)
	draw := func(w, h int) [][]term.Cell {
		buf := cell.NewBufferWriter(context.Background(), w, h)
		c.Draw(buf)
		return buf.RawCells()
	}

	tests := []struct {
		name         string
		cols, rows   int
		cellW, cellH int
		cellAspect   float64
	}{
		{"cell size unknown", 6, 3, 0, 0, 0},
		{"cell size reported", 6, 3, 10, 20, 2},
		{"font changed", 6, 3, 9, 24, 24.0 / 9},
		{"resized", 10, 5, 9, 24, 24.0 / 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cellW, cellH = tt.cellW, tt.cellH
			c.Resize(tt.cols, tt.rows)
			assert.Equal(t, braille.Encode(img, tt.cols, tt.rows, tt.cellAspect), draw(tt.cols, tt.rows))
		})
	}

	t.Run("font changed without resizing", func(t *testing.T) {
		cellW, cellH = 10, 10
		assert.Equal(t, braille.Encode(img, 10, 5, 1), draw(10, 5))
	})
}

func TestComponentWithoutCellSizeUsesDefaultCells(t *testing.T) {
	img := fill(20, 46, func(int, int) color.NRGBA { return color.NRGBA{B: 255, A: 255} })
	c := braille.NewComponent(img, nil, braille.Dots)
	c.Resize(8, 4)
	buf := cell.NewBufferWriter(context.Background(), 8, 4)
	c.Draw(buf)
	assert.Equal(t, braille.Encode(img, 8, 4, 0), buf.RawCells())
}

func TestComponentDrawsInItsStyle(t *testing.T) {
	img := fill(30, 50, func(x, y int) color.NRGBA {
		if (x-15)*(x-15)+(y-25)*(y-25) < 200 {
			return color.NRGBA{G: 200, A: 255}
		}
		return color.NRGBA{}
	})
	tests := []struct {
		style  braille.Style
		encode func(image.Image, int, int, float64) [][]term.Cell
	}{
		{braille.Dots, braille.Encode},
		{braille.Blocks, braille.EncodeBlocks},
	}
	for _, tt := range tests {
		c := braille.NewComponent(img, func() (int, int) { return 8, 18 }, tt.style)
		c.Resize(20, 8)
		buf := cell.NewBufferWriter(context.Background(), 20, 8)
		c.Draw(buf)
		assert.Equal(t, tt.encode(img, 20, 8, 18.0/8), buf.RawCells(), "style %d", tt.style)
	}
}

func TestComponentDrawsItsPictureWhereTheWriterDrawsImages(t *testing.T) {
	img := fill(10, 20, func(int, int) color.NRGBA { return color.NRGBA{R: 255, A: 255} })
	picture := fill(40, 80, func(int, int) color.NRGBA { return color.NRGBA{G: 255, A: 255} })
	size := cell.PixelSize{Width: 10, Height: 20}
	withSize := cell.ContextWithPixelSize(context.Background(), size)
	_, pixels := braille.EncodePicture(img, picture, 10, 5, 2, size)
	blocks := braille.EncodeBlocks(img, 10, 5, 2)
	blank := make([][]term.Cell, 5)
	for y := range blank {
		blank[y] = make([]term.Cell, 10)
		for x := range blank[y] {
			blank[y][x] = term.NewCell(braille.Blank, 1, term.Attributes{})
		}
	}

	tests := []struct {
		name       string
		ctx        context.Context
		noImages   bool
		wantCells  [][]term.Cell
		wantImages []term.Image
	}{
		{name: "cell size unknown", ctx: context.Background(), wantCells: blocks},
		{name: "writer without images", ctx: withSize, noImages: true, wantCells: blocks},
		{
			name: "writer with images", ctx: withSize, wantCells: blank,
			wantImages: []term.Image{{
				Src: pixels, Pos: term.Coordinates{X: 2}, Width: 6, Height: 5, Fit: term.ImageFitFill,
				Clip: image.Rect(2, 0, 8, 5),
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := braille.NewComponent(img, func() (int, int) { return 10, 20 }, braille.Blocks).
				WithPicture(func() image.Image { return picture })
			c.Resize(10, 5)
			buf := cell.NewBufferWriter(tt.ctx, 10, 5)
			var w term.Writer = buf
			if tt.noImages {
				w = imagelessWriter{buf}
			}
			c.Draw(w)
			assert.Equal(t, tt.wantCells, buf.RawCells())
			images := buf.Images()
			for i := range images {
				images[i].ID, images[i].Version = 0, 0
			}
			assert.Equal(t, tt.wantImages, images)
		})
	}
}

func TestComponentEncodesItsPictureOnlyWhenItsLayoutChanges(t *testing.T) {
	img := fill(10, 20, func(int, int) color.NRGBA { return color.NRGBA{R: 255, A: 255} })
	calls := 0
	cellW, cellH := 10, 20
	c := braille.NewComponent(img, func() (int, int) { return cellW, cellH }, braille.Dots).
		WithPicture(func() image.Image { calls++; return img })
	size := cell.PixelSize{Width: 10, Height: 20}
	draw := func(cols, rows int) term.Image {
		buf := cell.NewBufferWriter(cell.ContextWithPixelSize(context.Background(), size), cols, rows)
		c.Draw(buf)
		require.Len(t, buf.Images(), 1)
		return buf.Images()[0]
	}

	c.Draw(cell.NewBufferWriter(context.Background(), 10, 5))
	assert.Zero(t, calls, "the picture is not needed without images")

	c.Resize(10, 5)
	first := draw(10, 5)
	assert.Equal(t, first, draw(10, 5), "redrawn as is")

	steps := []struct {
		name   string
		change func()
	}{
		{"resized", func() { c.Resize(12, 5) }},
		{"font changed", func() { cellW, cellH = 9, 20 }},
		{"screen scaled", func() { size = cell.PixelSize{Width: 18, Height: 40} }},
	}
	last := first
	for _, step := range steps {
		step.change()
		got := draw(12, 5)
		assert.Equal(t, first.ID, got.ID, step.name)
		assert.Greater(t, got.Version, last.Version, "%s: re-encoded", step.name)
		last = got
	}
	assert.Equal(t, 1, calls, "the picture is only loaded once")
}

// imagelessWriter is a writer that cannot draw images.
type imagelessWriter struct{ *cell.BufferWriter }

func (imagelessWriter) DrawImage(term.Image) bool { return false }
