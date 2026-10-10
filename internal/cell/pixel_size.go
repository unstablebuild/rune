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

package cell

import (
	"context"
	"image"
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// PixelSize is the size of a cell in device pixels. It tells which
// cells a placement moved by pixels lands on, which the cell grid alone
// cannot. The sides need not be whole pixels, as a scaled font's cell
// is not, so cell edges are rounded to pixels. A size with a side that
// is not positive tells nothing: it covers no cells and is not carried
// by a context.
type PixelSize struct {
	Width, Height float64
}

func (s PixelSize) valid() bool {
	return s.Width > 0 && s.Height > 0
}

type pixelSizeKey struct{}

// ContextWithPixelSize returns a context carrying size for the writers
// drawn into with it.
func ContextWithPixelSize(ctx context.Context, size PixelSize) context.Context {
	return context.WithValue(ctx, pixelSizeKey{}, size)
}

// PixelSizeFromContext returns the cell size ctx carries, reporting
// false when it carries none.
func PixelSizeFromContext(ctx context.Context) (PixelSize, bool) {
	if ctx == nil {
		return PixelSize{}, false
	}
	size, ok := ctx.Value(pixelSizeKey{}).(PixelSize)
	return size, ok && size.valid()
}

// Pixels returns the pixels a right-exclusive cell rectangle spans.
func (s PixelSize) Pixels(r image.Rectangle) image.Rectangle {
	return image.Rect(
		int(math.Round(float64(r.Min.X)*s.Width)),
		int(math.Round(float64(r.Min.Y)*s.Height)),
		int(math.Round(float64(r.Max.X)*s.Width)),
		int(math.Round(float64(r.Max.Y)*s.Height)),
	)
}

// Cells returns the smallest cell rectangle whose pixels cover r, which
// is empty when r is or the size has no side to divide by.
func (s PixelSize) Cells(r image.Rectangle) image.Rectangle {
	if r.Empty() || !s.valid() {
		return image.Rectangle{}
	}
	return image.Rect(
		int(math.Floor(float64(r.Min.X)/s.Width)),
		int(math.Floor(float64(r.Min.Y)/s.Height)),
		int(math.Ceil(float64(r.Max.X)/s.Width)),
		int(math.Ceil(float64(r.Max.Y)/s.Height)),
	)
}

// Covered returns the cells img covers: the ones its raster lands on
// once it is moved by img.Offset, confined to its clip. A zero size
// cannot tell, so it takes the placement to cover its own cells.
func (s PixelSize) Covered(img term.Image) image.Rectangle {
	if img.Offset == (image.Point{}) || !s.valid() {
		return img.Visible()
	}
	landed := s.Cells(s.Pixels(img.Bounds()).Add(img.Offset))
	if img.Clip.Empty() {
		return landed
	}
	return landed.Intersect(img.Clip)
}
