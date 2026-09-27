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
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

var _ term.Writer = (*frameWriter)(nil)

// imageMarkWidth is the cell width DrawImage stamps on the cells a
// placement covers. No caller writes a cell that wide, so a cell still
// carrying it was not written over after the placement, and SetCell
// needs no bookkeeping of its own.
const imageMarkWidth = math.MaxUint8

// glyphSpill is how many columns past its own a cell's glyph paints:
// grapheme clusters are at most two cells wide. A wide cell written
// just left of a placement therefore covers its first column.
const glyphSpill = 1

// frameWriter is the writer handlers draw a frame into. It is a cell
// buffer that additionally collects image placements so the renderer
// can composite them over the cells. Cells written after a placement
// cover it, as they would had it been drawn in order.
type frameWriter struct {
	*cell.BufferWriter
	width, height int
	images        []term.Image

	// pending are the placements whose cells are still marked, in draw
	// order. They move to images, cut to the cells nothing was written
	// over, on the next read.
	pending []term.Image
	marks   []imageMark
	// open and next are scratch rectangles for cutting placements.
	open, next []image.Rectangle
}

// imageMark is what DrawImage keeps for a cell it marks.
type imageMark struct {
	// width is the cell width the mark replaced.
	width uint8
	// first is the earliest pending placement the cell still shows.
	// Placements before it were written over and re-marked since.
	first int32
}

// markedArea is the area DrawImage marks for a placement visible in r:
// the cells it covers and those whose glyphs can spill into it.
func markedArea(r image.Rectangle) image.Rectangle {
	r.Min.X = max(0, r.Min.X-glyphSpill)
	return r
}

func newFrameWriter(ctx context.Context, width, height int) *frameWriter {
	// A row's visible runs are separated by at least one covered cell.
	maxRuns := (width + 1) / 2
	return &frameWriter{
		BufferWriter: cell.NewBufferWriter(ctx, width, height),
		width:        width,
		height:       height,
		marks:        make([]imageMark, width*height),
		open:         make([]image.Rectangle, 0, maxRuns),
		next:         make([]image.Rectangle, 0, maxRuns),
	}
}

// Clear satisfies term.Writer. Placements live for a single frame, so
// they are dropped along with the cells.
func (w *frameWriter) Clear(attr term.Attributes) error {
	// Release the Src references so a picture that is no longer drawn
	// does not keep its pixels alive until the slice is overwritten.
	clear(w.images)
	w.images = w.images[:0]
	clear(w.pending)
	w.pending = w.pending[:0]
	return w.BufferWriter.Clear(attr)
}

// DrawImage satisfies term.Writer.
func (w *frameWriter) DrawImage(img term.Image) bool {
	img, ok := img.Clipped(image.Rect(0, 0, w.width, w.height))
	if !ok {
		return true
	}
	idx := int32(len(w.pending))
	cells := w.BufferWriter.RawCells()
	m := markedArea(img.Visible())
	for y := m.Min.Y; y < m.Max.Y; y++ {
		row, marks := cells[y], w.marks[y*w.width:]
		for x := m.Min.X; x < m.Max.X; x++ {
			if row[x].Width == imageMarkWidth {
				continue
			}
			marks[x] = imageMark{width: row[x].Width, first: idx}
			row[x].Width = imageMarkWidth
		}
	}
	w.pending = append(w.pending, img)
	return true
}

// Images returns the placements collected since the last Clear, in the
// order they were drawn, cut to the cells not written over after them.
// The slice is owned by the writer and is only valid until the next
// Clear.
func (w *frameWriter) Images() []term.Image {
	w.resolve()
	return w.images
}

// RawCells returns the cells written since the last Clear.
func (w *frameWriter) RawCells() [][]term.Cell {
	w.resolve()
	return w.BufferWriter.RawCells()
}

// resolve moves the pending placements to images and restores the
// widths their marks replaced.
func (w *frameWriter) resolve() {
	if len(w.pending) == 0 {
		return
	}
	cells := w.BufferWriter.RawCells()
	for i, img := range w.pending {
		w.appendUncovered(cells, img, int32(i))
	}
	for _, img := range w.pending {
		m := markedArea(img.Visible())
		for y := m.Min.Y; y < m.Max.Y; y++ {
			row, marks := cells[y], w.marks[y*w.width:]
			for x := m.Min.X; x < m.Max.X; x++ {
				if row[x].Width == imageMarkWidth {
					row[x].Width = marks[x].width
				}
			}
		}
	}
	clear(w.pending)
	w.pending = w.pending[:0]
}

// appendUncovered appends pending placement i to images, clipped to the
// cells that still show it. Each row's visible runs are merged with the
// rectangle above them when their columns match, so a placement with a
// window over it splits into a handful of rectangles rather than one
// per row.
func (w *frameWriter) appendUncovered(cells [][]term.Cell, img term.Image, i int32) {
	r := img.Visible()
	m := markedArea(r)
	open := w.open[:0]
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row, marks := cells[y], w.marks[y*w.width:]
		next := w.next[:0]
		j, start, coveredTo := 0, -1, 0
		for x := m.Min.X; x <= r.Max.X; x++ {
			shown := false
			if x < r.Max.X {
				width, after := row[x].Width, true
				if width == imageMarkWidth {
					width, after = marks[x].width, marks[x].first > i
				}
				if after {
					// A cell written after the placement covers it, and so
					// does the rest of its glyph when it is wide.
					coveredTo = max(coveredTo, x+max(1, int(width)))
				}
				shown = x >= r.Min.X && x >= coveredTo
			}
			if shown {
				if start < 0 {
					start = x
				}
				continue
			}
			if start < 0 {
				continue
			}
			for j < len(open) && open[j].Min.X < start {
				w.images = append(w.images, clippedTo(img, open[j]))
				j++
			}
			if j < len(open) && open[j].Min.X == start && open[j].Max.X == x {
				next = append(next, open[j].Union(image.Rect(start, y, x, y+1)))
				j++
			} else {
				next = append(next, image.Rect(start, y, x, y+1))
			}
			start = -1
		}
		for ; j < len(open); j++ {
			w.images = append(w.images, clippedTo(img, open[j]))
		}
		w.next = open
		open = next
	}
	for _, rect := range open {
		w.images = append(w.images, clippedTo(img, rect))
	}
	w.open = open
}

func clippedTo(img term.Image, r image.Rectangle) term.Image {
	img, _ = img.Clipped(r)
	return img
}
