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

	"github.com/unstablebuild/rune-go-sdk/term"
	"golang.org/x/image/draw"
	"unstable.build/rune/internal/cell"
)

var _ term.Writer = (*StringWriter)(nil)

// StringWriter is a term.StringWriter that can draw images: DrawImage
// encodes the placement as ASCII art into the cells it covers, so an
// image can be asserted in the same string as the cells around it.
//
// Fit, Crop and Clip are honored, and ImageFitContain leaves the cells
// around the centered raster untouched. Cells keep no separate image
// layer: whatever is written to a cell last is what it shows, whatever
// the placement's Layer. Offset is ignored because it moves the raster
// by less than a cell. Placements outside the writer are clipped rather
// than panicking like SetCell.
type StringWriter struct {
	*term.StringWriter
	config        Config
	width, height int
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
	}
}

// Resize satisfies term.StringWriter.
func (w *StringWriter) Resize(width, height int) {
	w.StringWriter.Resize(width, height)
	w.width, w.height = width, height
}

// DrawImage satisfies term.Writer. It always reports true.
func (w *StringWriter) DrawImage(img term.Image) bool {
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
	for y := visible.Min.Y; y < visible.Max.Y; y++ {
		for x := visible.Min.X; x < visible.Max.X; x++ {
			c, _ := buf.Cell(term.Coordinates{X: x - raster.Min.X, Y: y - raster.Min.Y})
			w.SetCell(term.Coordinates{X: x, Y: y}, c)
		}
	}
	return true
}
