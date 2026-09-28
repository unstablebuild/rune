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

	"github.com/unstablebuild/rune-go-sdk/term"
)

var _ term.Writer = (*BufferWriter)(nil)

// BufferWriter satisfies term.Writer with a Buffer.
type BufferWriter struct {
	width, height int
	Cursor        term.Coordinates
	cells         [][]term.Cell
	ctx           context.Context
}

// NewBufferWriter allocates storage for a new BufferWriter and initializes it.
func NewBufferWriter(ctx context.Context, width, height int) *BufferWriter {
	ret := new(BufferWriter)
	ret.Init(ctx, width, height)
	return ret
}

// Init initializes a BufferWriter's internal structures.
func (w *BufferWriter) Init(ctx context.Context, width, height int) {
	w.width, w.height = width, height
	w.ctx = ctx
	w.cells = make([][]term.Cell, w.height)
	for i := 0; i < w.height; i++ {
		w.cells[i] = make([]term.Cell, w.width)
	}
}

// SetCell satisfies term.Writer
func (w *BufferWriter) SetCell(pos term.Coordinates, c term.Cell) {
	if pos.Y < 0 || pos.Y >= len(w.cells) {
		return
	}
	row := w.cells[pos.Y]
	if pos.X < 0 || pos.X >= len(row) {
		return
	}
	row[pos.X] = c
}

// UnionAttributes satisfies term.Writer
func (w *BufferWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	if pos.X >= w.width || pos.Y >= w.height || pos.X < 0 || pos.Y < 0 {
		return
	}
	w.cells[pos.Y][pos.X].SetAttributes(term.AttributesUnion(
		w.cells[pos.Y][pos.X].Attributes(), attr))
}

// DrawImage satisfies term.Writer. A cell buffer carries cells only;
// the GUI wraps this writer to collect placements.
func (w *BufferWriter) DrawImage(term.Image) bool {
	return false
}

// Flush satisfies term.Writer
func (w *BufferWriter) Flush() error {
	return nil
}

// Clear satisfies term.Writer
func (w *BufferWriter) Clear(attr term.Attributes) error {
	if len(w.cells) == 0 {
		return nil
	}
	blank := term.NewCell(0, 0, attr)
	first := w.cells[0]
	for x := range first {
		first[x] = blank
	}
	// A cell carries a pointer, so storing one pays a write barrier
	// check; copy pays it once per row.
	for _, row := range w.cells[1:] {
		for x := copy(row, first); x < len(row); x++ {
			row[x] = blank
		}
	}
	return nil
}

// SetCursor satisfies term.Writer
func (w *BufferWriter) SetCursor(pos term.Coordinates) {
	w.Cursor = pos
}

// ToBuffer copies the underlying cells to b.
func (w *BufferWriter) ToBuffer(b *Buffer) {
	cells := new(rawCells)
	// trim starting at the first null column
	// of each row
	for y, row := range w.cells {
		for x, cell := range row {
			if cell.Ch == 0 {
				w.cells[y] = w.cells[y][:x]
				break
			}
		}
	}
	cells.cells = w.cells
	cells.setFillInChar(' ')
	cells.columnCap = defColumnCap
	cells.rowCap = defRowCap
	b.initWithCells(cells)
}

// RawCells returns the raw cells written so far to this BufferWritter.
func (w *BufferWriter) RawCells() [][]term.Cell {
	return w.cells
}

// Context returns the context passed to BufferWriterr's constructors,
// or the last context set via SetContext.
func (w *BufferWriter) Context() context.Context {
	return w.ctx
}

// SetContext sets the context to be returned in the next call to Context.
func (w *BufferWriter) SetContext(ctx context.Context) {
	w.ctx = ctx
}
