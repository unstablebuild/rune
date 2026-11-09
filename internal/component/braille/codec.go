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

// Package braille draws images with Unicode braille patterns. Each cell
// packs a grid of [CellDotsX] by [CellDotsY] dots, which gives eight times
// the resolution of one character per cell. It also draws images with one
// [Block] per cell, and as pictures for screens that draw images, on the
// same layout, so all renderings of an image line up.
package braille

import (
	"image"
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"golang.org/x/image/draw"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/asciiart"
)

const (
	// Blank is the braille pattern with no raised dots. It renders as an
	// empty cell yet still reads as braille, which lets shaders tell
	// braille artwork apart from the rest of the screen.
	Blank rune = '\u2800'
	// Block is the character [EncodeBlocks] fills cells with.
	Block rune = '▓'
	// CellDotsX is the number of dot columns in a cell.
	CellDotsX = 2
	// CellDotsY is the number of dot rows in a cell.
	CellDotsY = 4

	last rune = '\u28ff'
)

// dotBits follows the Unicode braille layout, where dots 7 and 8 were
// added below the original six and so do not continue their numbering.
var dotBits = [CellDotsY][CellDotsX]rune{
	{0x01, 0x08},
	{0x02, 0x10},
	{0x04, 0x20},
	{0x40, 0x80},
}

// IsPattern reports whether r is a braille pattern, [Blank] included.
func IsPattern(r rune) bool {
	return Blank <= r && r <= last
}

// Bit returns the bit that raises the dot at column x and row y of a
// cell, to be OR'ed into [Blank]. x must be below [CellDotsX] and y below
// [CellDotsY].
func Bit(x, y int) rune {
	return dotBits[y][x]
}

// Encode draws img on a grid of cols by rows cells, each cellAspect times
// taller than wide. The image is scaled to the largest size that fits the
// grid without distorting it, and centered. A cellAspect of zero or less
// assumes [asciiart.HeightToWidthCellAspectRatio].
//
// A dot is raised where at least half of the area it covers is opaque.
// A cell with raised dots takes the mean color of the image under them as
// its foreground; every other cell is [Blank] with default attributes.
// The result always has rows rows of cols cells.
func Encode(img image.Image, cols, rows int, cellAspect float64) [][]term.Cell {
	return encode(img, cols, rows, cellAspect, func(dots *cellSamples) (rune, sample) {
		ch, raised := Blank, sample{}
		for y := range CellDotsY {
			for x := range CellDotsX {
				if s := dots[y][x]; s.a >= 0.5 {
					ch |= Bit(x, y)
					raised.add(s)
				}
			}
		}
		return ch, raised
	})
}

// EncodeBlocks draws img like [Encode], on the same layout, but with a
// [Block] in each cell at least half covered by the opaque parts of the
// image, colored with the mean color of the image in the cell. Every
// other cell is [Blank] with default attributes.
//
// Encode raises dots in every cell EncodeBlocks fills with a block.
func EncodeBlocks(img image.Image, cols, rows int, cellAspect float64) [][]term.Cell {
	return encode(img, cols, rows, cellAspect, func(dots *cellSamples) (rune, sample) {
		var all sample
		for y := range CellDotsY {
			for x := range CellDotsX {
				all.add(dots[y][x])
			}
		}
		if all.a < CellDotsX*CellDotsY/2 {
			return Blank, sample{}
		}
		return Block, all
	})
}

// EncodePicture renders picture on the layout [Encode] gives img on a grid
// of cols by rows cells, for a screen whose cells are size device pixels.
// picture is stretched over the dots Encode would draw img with, so it can
// be a sharper copy of img.
//
// It returns the smallest rectangle of cells holding those dots, and their
// pixels, which are transparent around the picture. The rectangle is empty
// when img does not show on the grid or size has a side that is not
// positive.
func EncodePicture(
	img, picture image.Image, cols, rows int, cellAspect float64, size cell.PixelSize,
) (image.Rectangle, *image.RGBA) {
	dots := layout(img.Bounds(), cols, rows, cellAspect)
	if dots.Empty() || size.Width <= 0 || size.Height <= 0 {
		return image.Rectangle{}, nil
	}
	cells := image.Rect(
		dots.Min.X/CellDotsX, dots.Min.Y/CellDotsY,
		(dots.Max.X+CellDotsX-1)/CellDotsX, (dots.Max.Y+CellDotsY-1)/CellDotsY)
	pixelX := func(dot int) int {
		return int(math.Round((float64(dot)/CellDotsX - float64(cells.Min.X)) * size.Width))
	}
	pixelY := func(dot int) int {
		return int(math.Round((float64(dot)/CellDotsY - float64(cells.Min.Y)) * size.Height))
	}
	dst := image.NewRGBA(image.Rect(0, 0,
		int(math.Round(float64(cells.Dx())*size.Width)),
		int(math.Round(float64(cells.Dy())*size.Height))))
	at := image.Rect(pixelX(dots.Min.X), pixelY(dots.Min.Y), pixelX(dots.Max.X), pixelY(dots.Max.Y))
	draw.BiLinear.Scale(dst, at, picture, picture.Bounds(), draw.Src, nil)
	return cells, dst
}

// sample is the image under a dot: its opacity in 0..1, and its color
// premultiplied by that opacity in 0..255 per channel.
type sample struct{ r, g, b, a float64 }

func (s *sample) add(o sample) {
	s.r += o.r
	s.g += o.g
	s.b += o.b
	s.a += o.a
}

// color returns the mean color of the samples added to s, weighted by
// their opacity.
func (s sample) color() term.Color {
	return term.NewRGBColor(
		int32(math.Round(s.r/s.a)),
		int32(math.Round(s.g/s.a)),
		int32(math.Round(s.b/s.a)))
}

type cellSamples [CellDotsY][CellDotsX]sample

// encode lays img out as described in [Encode] and lets draw pick the
// character of each cell from the samples of its dots, along with the
// samples whose mean color the cell takes.
func encode(
	img image.Image, cols, rows int, cellAspect float64,
	draw func(*cellSamples) (rune, sample),
) [][]term.Cell {
	cols, rows = max(0, cols), max(0, rows)
	out := make([][]term.Cell, rows)
	for y := range out {
		out[y] = make([]term.Cell, cols)
		for x := range out[y] {
			out[y][x] = term.NewCell(Blank, 1, term.Attributes{})
		}
	}
	dots := sampleDots(img, cols, rows, cellAspect)
	if dots == nil {
		return out
	}
	var cell cellSamples
	for cy := range rows {
		for cx := range cols {
			for y := range CellDotsY {
				for x := range CellDotsX {
					cell[y][x] = dots[(cy*CellDotsY+y)*cols*CellDotsX+cx*CellDotsX+x]
				}
			}
			ch, colored := draw(&cell)
			if ch == Blank {
				continue
			}
			out[cy][cx].Ch = ch
			out[cy][cx].Fg = colored.color()
		}
	}
	return out
}

// sampleDots returns the image under each dot of a grid of cols by rows
// cells, row by row, laid out as described in [Encode]. It returns nil
// when the image does not show on the grid.
func sampleDots(img image.Image, cols, rows int, cellAspect float64) []sample {
	bounds := img.Bounds()
	dots := layout(bounds, cols, rows, cellAspect)
	if dots.Empty() {
		return nil
	}

	gridW := cols * CellDotsX
	out := make([]sample, gridW*rows*CellDotsY)
	pxW := float64(bounds.Dx()) / float64(dots.Dx())
	pxH := float64(bounds.Dy()) / float64(dots.Dy())
	for dy := range dots.Dy() {
		for dx := range dots.Dx() {
			r, g, b, a := coverage(img,
				float64(bounds.Min.X)+float64(dx)*pxW,
				float64(bounds.Min.Y)+float64(dy)*pxH,
				pxW, pxH)
			out[(dy+dots.Min.Y)*gridW+dx+dots.Min.X] = sample{r, g, b, a}
		}
	}
	return out
}

// layout returns the dots of a grid of cols by rows cells that an image of
// bounds is drawn on, as described in [Encode]. It is empty when the image
// does not show on the grid.
func layout(bounds image.Rectangle, cols, rows int, cellAspect float64) image.Rectangle {
	if cols <= 0 || rows <= 0 || bounds.Empty() {
		return image.Rectangle{}
	}
	gridW, gridH := cols*CellDotsX, rows*CellDotsY
	if cellAspect <= 0 {
		cellAspect = asciiart.HeightToWidthCellAspectRatio
	}
	dotAspect := cellAspect * CellDotsX / CellDotsY
	// Dots are taller than wide, so keeping the image's proportions
	// takes more dot columns per dot row than it has pixel columns per
	// pixel row.
	dotsPerRow := float64(bounds.Dx()) / float64(bounds.Dy()) * dotAspect
	dotsH := gridH
	dotsW := int(math.Round(float64(dotsH) * dotsPerRow))
	if dotsW > gridW {
		dotsW = gridW
		dotsH = int(math.Round(float64(dotsW) / dotsPerRow))
	}
	if dotsW == 0 || dotsH == 0 {
		return image.Rectangle{}
	}
	offX, offY := (gridW-dotsW)/2, (gridH-dotsH)/2
	return image.Rect(offX, offY, offX+dotsW, offY+dotsH)
}

// coverage returns the opacity of the w by h area of img at x, y, along
// with its color premultiplied by that opacity in 0..255 per channel.
// Pixels the area only partially overlaps count in proportion.
func coverage(img image.Image, x, y, w, h float64) (r, g, b, a float64) {
	bounds := img.Bounds()
	x1, y1 := x+w, y+h
	var total float64
	for py := int(math.Floor(y)); float64(py) < y1 && py < bounds.Max.Y; py++ {
		wy := math.Min(y1, float64(py+1)) - math.Max(y, float64(py))
		for px := int(math.Floor(x)); float64(px) < x1 && px < bounds.Max.X; px++ {
			wx := math.Min(x1, float64(px+1)) - math.Max(x, float64(px))
			weight := wx * wy
			pr, pg, pb, pa := img.At(px, py).RGBA()
			r += weight * float64(pr)
			g += weight * float64(pg)
			b += weight * float64(pb)
			a += weight * float64(pa)
			total += weight
		}
	}
	if total == 0 {
		return 0, 0, 0, 0
	}
	const scale = 0xffff / 255.0
	return r / total / scale, g / total / scale, b / total / scale, a / total / 0xffff
}
