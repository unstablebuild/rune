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
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"golang.org/x/image/draw"
	"unstable.build/rune/internal/cell"
)

var _ term.Writer = (*StringWriter)(nil)

// The nominal cell an offset placement moves by, matching the 2.3 cell
// aspect ratio the encoder assumes.
const (
	nominalCellWidth  = 10
	nominalCellHeight = 23
)

// StringWriter is a term.StringWriter that can draw images: DrawImage
// encodes the placement as ASCII art into the cells it covers, so an
// image can be asserted in the same string as the cells around it.
//
// Fit, Crop and Clip are honored, and ImageFitContain leaves the cells
// around the centered raster untouched. A cell shows what a compositing
// writer would: a placement above text shows over any glyph, one below
// text only over a cell without a glyph, and one below the background
// only over a cell that also has the default background. Where
// placements overlap, the cell shows the one on the highest layer, and
// the last drawn of those on the same layer. Fully transparent pixels
// show whatever is under them, and a cell written after the placements
// covers all of them. Offset moves a placement by the whole cells it
// rounds to, taking a cell to span 10x23 pixels. RasterOffset is ignored
// because it moves the raster by less than a cell. Placements outside
// the writer are clipped rather than panicking like SetCell.
type StringWriter struct {
	*term.StringWriter
	config        Config
	width, height int
	// art holds, for every cell, the placement that drew it last.
	art []artCell
}

// artCell is what a cell a placement drew remembers to compose the
// placements drawn over it later.
type artCell struct {
	drawn bool
	layer term.ImageLayer
	// under is the cell written there before any placement drew it.
	under term.Cell
}

// NewStringWriter returns a width x height StringWriter that encodes
// images with config. config.MaintainAspectRatio is ignored in favor of
// each placement's Fit.
func NewStringWriter(width, height int, config Config) *StringWriter {
	config.MaintainAspectRatio = false
	return &StringWriter{
		StringWriter: term.NewStringWriter(width, height),
		config:       config,
		width:        width,
		height:       height,
		art:          make([]artCell, width*height),
	}
}

// Resize satisfies term.StringWriter.
func (w *StringWriter) Resize(width, height int) {
	w.StringWriter.Resize(width, height)
	w.width, w.height = width, height
	w.art = make([]artCell, width*height)
}

// Clear satisfies term.StringWriter.
func (w *StringWriter) Clear(attr term.Attributes) error {
	clear(w.art)
	return w.StringWriter.Clear(attr)
}

// SetCell satisfies term.Writer. The cell covers every placement drawn
// there before it.
func (w *StringWriter) SetCell(pos term.Coordinates, c term.Cell) {
	w.StringWriter.SetCell(pos, c)
	w.art[pos.Y*w.width+pos.X] = artCell{}
}

// DrawImage satisfies term.Writer. It always reports true.
func (w *StringWriter) DrawImage(img term.Image) bool {
	img.Pos.X += int(math.Round(float64(img.Offset.X) / nominalCellWidth))
	img.Pos.Y += int(math.Round(float64(img.Offset.Y) / nominalCellHeight))
	visible := img.Visible().Intersect(image.Rect(0, 0, w.width, w.height))
	if visible.Empty() || img.Src == nil {
		return true
	}
	src := img.Src
	if !img.Crop.Empty() {
		crop := img.Crop.Intersect(src.Bounds())
		sub := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
		draw.Draw(sub, sub.Rect, src, crop.Min, draw.Src)
		src = sub
	}
	srcSize := src.Bounds().Size()
	if srcSize.X <= 0 || srcSize.Y <= 0 {
		return true
	}

	raster := img.Bounds()
	if img.Fit == term.ImageFitContain {
		width, height := ResizeMaintainAspectRatio(
			srcSize.X, srcSize.Y, raster.Dx(), raster.Dy())
		raster = image.Rect(0, 0, width, height).Add(raster.Min).Add(image.Pt(
			(raster.Dx()-width)/2, (raster.Dy()-height)/2))
	}
	visible = visible.Intersect(raster)
	if visible.Empty() {
		return true
	}

	buf := cell.NewBuffer()
	Encode(buf, raster.Dx(), raster.Dy(), src, w.config)
	// Encode keeps no alpha, so it is sampled again the same way.
	alpha := image.NewNRGBA(image.Rect(0, 0, raster.Dx(), raster.Dy()))
	w.config.Scaler.Scale(alpha, alpha.Rect, src, src.Bounds(), draw.Over, nil)
	cells := w.Cells()
	for y := visible.Min.Y; y < visible.Max.Y; y++ {
		for x := visible.Min.X; x < visible.Max.X; x++ {
			pos := term.Coordinates{X: x - raster.Min.X, Y: y - raster.Min.Y}
			if alpha.NRGBAAt(pos.X, pos.Y).A == 0 {
				continue
			}
			i := y*w.width + x
			under, art := cells[i], w.art[i]
			if art.drawn {
				if depth(art.layer) > depth(img.Layer) {
					continue
				}
				under = art.under
			}
			if !showsThrough(under, img.Layer) {
				continue
			}
			c, _ := buf.Cell(pos)
			w.StringWriter.SetCell(term.Coordinates{X: x, Y: y}, c)
			w.art[i] = artCell{drawn: true, layer: img.Layer, under: under}
		}
	}
	return true
}

// depth orders the layers from the bottom up.
func depth(layer term.ImageLayer) int {
	switch layer {
	case term.ImageLayerBelowBackground:
		return 0
	case term.ImageLayerBelowText:
		return 1
	default:
		return 2
	}
}

// showsThrough reports whether a placement on layer is visible over c.
func showsThrough(c term.Cell, layer term.ImageLayer) bool {
	blank := c.Ch == 0 || c.Ch == ' '
	switch layer {
	case term.ImageLayerBelowText:
		return blank
	case term.ImageLayerBelowBackground:
		return blank && c.Bg == term.ColorDefault
	default:
		return true
	}
}
