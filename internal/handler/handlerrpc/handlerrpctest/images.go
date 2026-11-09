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

package handlerrpctest

import (
	"context"
	"image"
	"image/color"
	"math"
	"slices"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Gradient returns a width by height raster that differs for every version.
func Gradient(width, height int, version uint64) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			img.SetRGBA(x, y, color.RGBA{
				R: byte(x * 255 / max(1, width-1)),
				G: byte(y * 255 / max(1, height-1)),
				B: byte(version * 37),
				A: 0xff,
			})
		}
	}
	return img
}

// RasterHandler places a Gradient over its area at the cell pixel size of
// its draw, rounded up, and draws 'F' cells when DrawImage fails.
type RasterHandler struct {
	mu            sync.Mutex
	id            term.ImageID
	version       uint64
	width, height int
	sizes         []CellPixelSize
	shown         []bool
}

// CellPixelSize is the cell size a draw saw; zero for none.
type CellPixelSize struct {
	Width, Height float64
}

// NewRasterHandler returns a RasterHandler at version 1.
func NewRasterHandler() *RasterHandler {
	return &RasterHandler{id: term.NewImageID(), version: 1}
}

// SetVersion changes the raster the next draws place.
func (h *RasterHandler) SetVersion(version uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.version = version
}

// Raster returns the raster the handler places at its current size.
func (h *RasterHandler) Raster() *image.RGBA {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.raster()
}

func (h *RasterHandler) raster() *image.RGBA {
	cellWidth, cellHeight := 1, 1
	if n := len(h.sizes); n > 0 && h.sizes[n-1].Width > 0 {
		cellWidth = int(math.Ceil(h.sizes[n-1].Width))
		cellHeight = int(math.Ceil(h.sizes[n-1].Height))
	}
	return Gradient(h.width*cellWidth, h.height*cellHeight, h.version)
}

// CellPixelSizes returns the cell size every draw saw, in order.
func (h *RasterHandler) CellPixelSizes() []CellPixelSize {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.sizes)
}

// Shown returns what DrawImage reported on every draw, in order.
func (h *RasterHandler) Shown() []bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.shown)
}

// Resize satisfies tui.Handler.
func (h *RasterHandler) Resize(width, height int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.width, h.height = width, height
}

// Draw satisfies tui.Handler.
func (h *RasterHandler) Draw(w term.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	width, height, _ := term.CellPixelSizeFromContext(w.Context())
	h.sizes = append(h.sizes, CellPixelSize{Width: width, Height: height})
	shown := w.DrawImage(term.Image{
		ID:      h.id,
		Version: h.version,
		Src:     h.raster(),
		Width:   h.width,
		Height:  h.height,
	})
	h.shown = append(h.shown, shown)
	if shown {
		return
	}
	for y := range h.height {
		for x := range h.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, term.Cell{Ch: 'F', Width: 1, Bytes: 1})
		}
	}
}

// Handle satisfies tui.Handler.
func (h *RasterHandler) Handle(term.Event) (exit, handled bool) { return false, false }

// Cursor satisfies tui.Handler.
func (h *RasterHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}

// Selection satisfies tui.Handler.
func (h *RasterHandler) Selection() (string, bool) { return "", false }

// Close satisfies io.Closer.
func (h *RasterHandler) Close() error { return nil }

// RecordedImage is a placement with a copy of its pixels when drawn.
type RecordedImage struct {
	term.Image
	Pix []byte
}

// ImageRecorder records placements, and counts the cells written after the
// first one.
type ImageRecorder struct {
	Ctx             context.Context
	Cells           int
	CellsAfterImage int
	Images          []RecordedImage
}

// Context satisfies term.Writer.
func (r *ImageRecorder) Context() context.Context {
	if r.Ctx == nil {
		return context.Background()
	}
	return r.Ctx
}

// SetCell satisfies term.Writer.
func (r *ImageRecorder) SetCell(term.Coordinates, term.Cell) {
	r.Cells++
	if len(r.Images) > 0 {
		r.CellsAfterImage++
	}
}

// UnionAttributes satisfies term.Writer.
func (r *ImageRecorder) UnionAttributes(term.Coordinates, term.Attributes) {}

// DrawImage satisfies term.Writer.
func (r *ImageRecorder) DrawImage(img term.Image) bool {
	rec := RecordedImage{Image: img}
	if rgba, ok := img.Src.(*image.RGBA); ok {
		rec.Pix = slices.Clone(rgba.Pix)
	}
	r.Images = append(r.Images, rec)
	return true
}
