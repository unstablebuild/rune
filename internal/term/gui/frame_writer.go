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

package gui

import (
	"context"
	"image"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

var _ term.Writer = (*frameWriter)(nil)

// frameWriter is the writer handlers draw a frame into. It is a cell
// buffer that additionally collects image placements so the renderer
// can composite them over the cells.
type frameWriter struct {
	*cell.BufferWriter
	width, height int
	images        []term.Image
}

func newFrameWriter(ctx context.Context, width, height int) *frameWriter {
	return &frameWriter{
		BufferWriter: cell.NewBufferWriter(ctx, width, height),
		width:        width,
		height:       height,
	}
}

// Clear satisfies term.Writer. Placements live for a single frame, so
// they are dropped along with the cells.
func (w *frameWriter) Clear(attr term.Attributes) error {
	// Release the Src references so a picture that is no longer drawn
	// does not keep its pixels alive until the slice is overwritten.
	clear(w.images)
	w.images = w.images[:0]
	return w.BufferWriter.Clear(attr)
}

// DrawImage satisfies term.Writer.
func (w *frameWriter) DrawImage(img term.Image) bool {
	img, ok := img.Clipped(image.Rect(0, 0, w.width, w.height))
	if !ok {
		return true
	}
	w.images = append(w.images, img)
	return true
}

// Images returns the placements collected since the last Clear, in the
// order they were drawn. The slice is owned by the writer and is only
// valid until the next Clear.
func (w *frameWriter) Images() []term.Image {
	return w.images
}
