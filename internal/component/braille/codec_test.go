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
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/braille"
)

func TestIsPattern(t *testing.T) {
	tests := []struct {
		r    rune
		want bool
	}{
		{braille.Blank, true},
		{'⣿', true},
		{'⡇', true},
		{'\u27ff', false},
		{'\u2900', false},
		{' ', false},
		{'a', false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, braille.IsPattern(tt.r), "%U", tt.r)
	}
}

func TestBit(t *testing.T) {
	all := braille.Blank
	for y := range braille.CellDotsY {
		for x := range braille.CellDotsX {
			bit := braille.Bit(x, y)
			assert.Zero(t, all&bit, "dot %d,%d shares a bit", x, y)
			all |= bit
		}
	}
	assert.Equal(t, '⣿', all)
	assert.Equal(t, '⡇', braille.Blank|braille.Bit(0, 0)|braille.Bit(0, 1)|braille.Bit(0, 2)|braille.Bit(0, 3))
	assert.Equal(t, '⢸', braille.Blank|braille.Bit(1, 0)|braille.Bit(1, 1)|braille.Bit(1, 2)|braille.Bit(1, 3))
}

// Images below are sized so their dots come out square: a dot is 1.15
// times taller than wide, so 20x23 pixels span as many dot columns as
// rows.
func TestEncode(t *testing.T) {
	red := color.NRGBA{R: 255, A: 255}
	blue := color.NRGBA{B: 255, A: 255}
	transparent := color.NRGBA{}
	blank := term.NewCell(braille.Blank, 1, term.Attributes{})
	full := func(r, g, b int32) term.Cell {
		return term.NewCell('⣿', 1, term.Attributes{Fg: term.NewRGBColor(r, g, b)})
	}

	tests := []struct {
		name       string
		img        image.Image
		cols, rows int
		want       [][]term.Cell
	}{
		{
			name: "opaque image fills the grid",
			img:  fill(20, 23, func(int, int) color.NRGBA { return red }),
			cols: 2, rows: 1,
			want: [][]term.Cell{{full(255, 0, 0), full(255, 0, 0)}},
		},
		{
			name: "transparent image leaves blank cells",
			img:  fill(20, 23, func(int, int) color.NRGBA { return transparent }),
			cols: 2, rows: 1,
			want: [][]term.Cell{{blank, blank}},
		},
		{
			name: "dots follow the opaque half",
			img: fill(40, 46, func(x, _ int) color.NRGBA {
				if x < 20 {
					return red
				}
				return transparent
			}),
			cols: 2, rows: 1,
			want: [][]term.Cell{{full(255, 0, 0), blank}},
		},
		{
			name: "narrow image is centered without stretching",
			img:  fill(20, 46, func(int, int) color.NRGBA { return red }),
			cols: 4, rows: 1,
			want: [][]term.Cell{{
				blank,
				term.NewCell('⢸', 1, term.Attributes{Fg: term.NewRGBColor(255, 0, 0)}),
				term.NewCell('⡇', 1, term.Attributes{Fg: term.NewRGBColor(255, 0, 0)}),
				blank,
			}},
		},
		{
			name: "cell color is the mean of its dots",
			img: fill(20, 46, func(_, y int) color.NRGBA {
				if y < 23 {
					return red
				}
				return blue
			}),
			cols: 1, rows: 1,
			want: [][]term.Cell{{full(128, 0, 128)}},
		},
		{
			name: "mostly transparent dots stay lowered",
			img:  fill(20, 23, func(int, int) color.NRGBA { return color.NRGBA{R: 255, A: 100} }),
			cols: 2, rows: 1,
			want: [][]term.Cell{{blank, blank}},
		},
		{
			name: "mostly opaque dots are raised",
			img:  fill(20, 23, func(int, int) color.NRGBA { return color.NRGBA{R: 255, A: 160} }),
			cols: 2, rows: 1,
			want: [][]term.Cell{{full(255, 0, 0), full(255, 0, 0)}},
		},
		{
			name: "zero columns",
			img:  fill(20, 23, func(int, int) color.NRGBA { return red }),
			cols: 0, rows: 2,
			want: [][]term.Cell{{}, {}},
		},
		{
			name: "empty image",
			img:  image.NewNRGBA(image.Rect(0, 0, 0, 0)),
			cols: 1, rows: 1,
			want: [][]term.Cell{{blank}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, braille.Encode(tt.img, tt.cols, tt.rows, 0))
		})
	}
}

func TestEncodeKeepsProportions(t *testing.T) {
	opaque := func(int, int) color.NRGBA { return color.NRGBA{R: 255, A: 255} }
	for _, cellAspect := range []float64{0, 2.0, 2.5, 1.0} {
		drawnAspect := cellAspect
		if cellAspect == 0 {
			drawnAspect = asciiart.HeightToWidthCellAspectRatio
		}
		dotAspect := drawnAspect * braille.CellDotsX / braille.CellDotsY
		for _, size := range [][2]int{{100, 100}, {196, 320}, {300, 100}, {40, 400}} {
			cells := braille.Encode(fill(size[0], size[1], opaque), 200, 60, cellAspect)
			dots := raisedBounds(cells)
			want := float64(size[0]) / float64(size[1])
			got := float64(dots.Dx()) / (float64(dots.Dy()) * dotAspect)
			assert.InDelta(t, want, got, want*0.03,
				"%dx%d image on %.1f cells drawn %dx%d dots",
				size[0], size[1], cellAspect, dots.Dx(), dots.Dy())
		}
	}
}

func TestEncodeFitsAnyGrid(t *testing.T) {
	img := fill(30, 70, func(int, int) color.NRGBA { return color.NRGBA{G: 255, A: 255} })
	for _, size := range [][2]int{{1, 1}, {3, 17}, {80, 24}, {7, 2}, {200, 60}} {
		cells := braille.Encode(img, size[0], size[1], 0)
		require.Len(t, cells, size[1])
		raised := false
		for _, row := range cells {
			require.Len(t, row, size[0])
			for _, c := range row {
				require.True(t, braille.IsPattern(c.Ch))
				raised = raised || c.Ch != braille.Blank
			}
		}
		assert.True(t, raised, "%dx%d grid draws nothing", size[0], size[1])
	}
}

func TestEncodeBlocks(t *testing.T) {
	red := color.NRGBA{R: 255, A: 255}
	blue := color.NRGBA{B: 255, A: 255}
	transparent := color.NRGBA{}
	blank := term.NewCell(braille.Blank, 1, term.Attributes{})
	block := func(r, g, b int32) term.Cell {
		return term.NewCell(braille.Block, 1, term.Attributes{Fg: term.NewRGBColor(r, g, b)})
	}

	tests := []struct {
		name       string
		img        image.Image
		cols, rows int
		want       [][]term.Cell
	}{
		{
			name: "opaque image fills the grid",
			img:  fill(20, 23, func(int, int) color.NRGBA { return red }),
			cols: 2, rows: 1,
			want: [][]term.Cell{{block(255, 0, 0), block(255, 0, 0)}},
		},
		{
			name: "transparent image leaves blank cells",
			img:  fill(20, 23, func(int, int) color.NRGBA { return transparent }),
			cols: 2, rows: 1,
			want: [][]term.Cell{{blank, blank}},
		},
		{
			name: "half covered cells are filled",
			img: fill(20, 46, func(_, y int) color.NRGBA {
				if y < 23 {
					return red
				}
				return transparent
			}),
			cols: 1, rows: 1,
			want: [][]term.Cell{{block(255, 0, 0)}},
		},
		{
			name: "less than half covered cells stay blank",
			img: fill(20, 46, func(_, y int) color.NRGBA {
				if y < 17 {
					return red
				}
				return transparent
			}),
			cols: 1, rows: 1,
			want: [][]term.Cell{{blank}},
		},
		{
			name: "cell color is the mean of the image in it",
			img: fill(20, 46, func(_, y int) color.NRGBA {
				if y < 23 {
					return red
				}
				return blue
			}),
			cols: 1, rows: 1,
			want: [][]term.Cell{{block(128, 0, 128)}},
		},
		{
			name: "narrow image is centered on the dots layout",
			img:  fill(20, 46, func(int, int) color.NRGBA { return red }),
			cols: 4, rows: 1,
			want: [][]term.Cell{{blank, block(255, 0, 0), block(255, 0, 0), blank}},
		},
		{
			name: "empty image",
			img:  image.NewNRGBA(image.Rect(0, 0, 0, 0)),
			cols: 1, rows: 1,
			want: [][]term.Cell{{blank}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, braille.EncodeBlocks(tt.img, tt.cols, tt.rows, 0))
		})
	}
}

func TestEncodeBlocksLineUpWithDots(t *testing.T) {
	disc := fill(97, 131, func(x, y int) color.NRGBA {
		dx, dy := float64(x-48), float64(y-65)
		return color.NRGBA{R: uint8(x), G: uint8(y), A: uint8(min(255, max(0, 4000-dx*dx-dy*dy)))}
	})
	for _, size := range [][2]int{{80, 24}, {37, 19}, {200, 60}} {
		for _, aspect := range []float64{0, 1.7, 2.6} {
			dots := braille.Encode(disc, size[0], size[1], aspect)
			blocks := braille.EncodeBlocks(disc, size[0], size[1], aspect)
			filled := 0
			for y, row := range blocks {
				for x, c := range row {
					if c.Ch != braille.Block {
						require.Equal(t, braille.Blank, c.Ch)
						continue
					}
					filled++
					require.NotEqual(t, braille.Blank, dots[y][x].Ch,
						"%dx%d on %.1f: block at %d,%d has no dots", size[0], size[1], aspect, x, y)
				}
			}
			assert.Positive(t, filled, "%dx%d on %.1f draws no blocks", size[0], size[1], aspect)
		}
	}
}

func TestEncodePicture(t *testing.T) {
	green := func(int, int) color.NRGBA { return color.NRGBA{G: 255, A: 255} }
	tests := []struct {
		name       string
		imgW, imgH int
		cols, rows int
		cellAspect float64
		size       cell.PixelSize
		wantCells  image.Rectangle
		wantSize   image.Point
		// wantPicture is where the picture lands, in pixels.
		wantPicture image.Rectangle
	}{
		{
			name: "tall image", imgW: 10, imgH: 20, cols: 10, rows: 5, cellAspect: 2,
			size:      cell.PixelSize{Width: 10, Height: 20},
			wantCells: image.Rect(2, 0, 8, 5), wantSize: image.Pt(60, 100),
			wantPicture: image.Rect(5, 0, 55, 100),
		},
		{
			name: "fractional cells", imgW: 10, imgH: 20, cols: 10, rows: 5, cellAspect: 2,
			size:      cell.PixelSize{Width: 7.5, Height: 15},
			wantCells: image.Rect(2, 0, 8, 5), wantSize: image.Pt(45, 75),
			wantPicture: image.Rect(4, 0, 41, 75),
		},
		{
			name: "wide image", imgW: 40, imgH: 10, cols: 10, rows: 5, cellAspect: 2,
			size:      cell.PixelSize{Width: 10, Height: 20},
			wantCells: image.Rect(0, 1, 10, 3), wantSize: image.Pt(100, 40),
			wantPicture: image.Rect(0, 15, 100, 40),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := fill(tt.imgW, tt.imgH, func(int, int) color.NRGBA { return color.NRGBA{R: 255, A: 255} })
			picture := fill(4*tt.imgW, 4*tt.imgH, green)
			cells, pixels := braille.EncodePicture(img, picture, tt.cols, tt.rows, tt.cellAspect, tt.size)
			assert.Equal(t, tt.wantCells, cells)
			require.NotNil(t, pixels)
			assert.Equal(t, tt.wantSize, pixels.Rect.Size())
			assert.Equal(t, tt.wantPicture, opaqueBounds(pixels))
			center := tt.wantPicture.Min.Add(tt.wantPicture.Size().Div(2))
			assert.Equal(t, color.RGBA{G: 255, A: 255}, pixels.RGBAAt(center.X, center.Y), "draws the picture, not the image")
		})
	}

	t.Run("nothing shows", func(t *testing.T) {
		img := fill(10, 20, green)
		for _, size := range []cell.PixelSize{{}, {Width: 10}, {Height: 20}} {
			cells, pixels := braille.EncodePicture(img, img, 10, 5, 2, size)
			assert.Empty(t, cells, "size %v", size)
			assert.Nil(t, pixels, "size %v", size)
		}
		cells, pixels := braille.EncodePicture(img, img, 0, 5, 2, cell.PixelSize{Width: 10, Height: 20})
		assert.Empty(t, cells)
		assert.Nil(t, pixels)
	})
}

func TestEncodePictureLinesUpWithDots(t *testing.T) {
	img := fill(97, 131, func(int, int) color.NRGBA { return color.NRGBA{B: 255, A: 255} })
	for _, grid := range [][2]int{{80, 24}, {37, 19}, {200, 60}} {
		for _, cellAspect := range []float64{1.7, 2, 2.6} {
			size := cell.PixelSize{Width: 8, Height: 8 * cellAspect}
			cells, pixels := braille.EncodePicture(img, img, grid[0], grid[1], cellAspect, size)
			dots := raisedBounds(braille.Encode(img, grid[0], grid[1], cellAspect))
			want := image.Rect(
				int(math.Round((float64(dots.Min.X)/braille.CellDotsX-float64(cells.Min.X))*size.Width)),
				int(math.Round((float64(dots.Min.Y)/braille.CellDotsY-float64(cells.Min.Y))*size.Height)),
				int(math.Round((float64(dots.Max.X)/braille.CellDotsX-float64(cells.Min.X))*size.Width)),
				int(math.Round((float64(dots.Max.Y)/braille.CellDotsY-float64(cells.Min.Y))*size.Height)))
			assert.Equal(t, want, opaqueBounds(pixels), "%v on %.1f", grid, cellAspect)
			assert.Equal(t, dots, image.Rect(
				cells.Min.X*braille.CellDotsX, cells.Min.Y*braille.CellDotsY,
				cells.Max.X*braille.CellDotsX, cells.Max.Y*braille.CellDotsY).Intersect(dots),
				"%v on %.1f: dots outside the picture's cells", grid, cellAspect)
		}
	}
}

// raisedBounds returns the smallest rectangle, in dots, holding every
// raised dot of cells.
func raisedBounds(cells [][]term.Cell) image.Rectangle {
	var r image.Rectangle
	for y, row := range cells {
		for x, c := range row {
			for dy := range braille.CellDotsY {
				for dx := range braille.CellDotsX {
					if c.Ch&braille.Bit(dx, dy) == 0 {
						continue
					}
					gx, gy := x*braille.CellDotsX+dx, y*braille.CellDotsY+dy
					r = r.Union(image.Rect(gx, gy, gx+1, gy+1))
				}
			}
		}
	}
	return r
}

func opaqueBounds(img *image.RGBA) image.Rectangle {
	var r image.Rectangle
	for y := img.Rect.Min.Y; y < img.Rect.Max.Y; y++ {
		for x := img.Rect.Min.X; x < img.Rect.Max.X; x++ {
			if img.RGBAAt(x, y).A > 0 {
				r = r.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return r
}

func fill(w, h int, at func(x, y int) color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetNRGBA(x, y, at(x, y))
		}
	}
	return img
}
