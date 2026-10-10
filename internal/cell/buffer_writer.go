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

var _ term.Writer = (*BufferWriter)(nil)

// imageMarkWidth is the cell width DrawImage stamps on the cells a
// placement covers. No caller writes a cell that wide, so a cell still
// carrying it was not written over after the placement, and SetCell
// needs no bookkeeping of its own.
const imageMarkWidth = math.MaxUint8

// coveredFrom is the first a resolved mark takes when the cell was
// written over after the last placement that marked it, so no pending
// placement shows there.
const coveredFrom = math.MaxInt32

// glyphSpill is how many columns past its own a cell's glyph paints:
// grapheme clusters are at most two cells wide. A wide cell written
// just left of a placement therefore covers its first column.
const glyphSpill = 1

// imageMark is what DrawImage keeps for a cell it marks.
type imageMark struct {
	// width is the cell width the mark replaced, or imageMarkWidth once
	// the cell is resolved and holds its width again.
	width uint8
	// first is the earliest pending placement the cell still shows.
	// Placements before it were written over and re-marked since, and
	// none shows at coveredFrom.
	first int32
}

// markedArea is the area DrawImage marks for a placement visible in r:
// the cells it covers and those whose glyphs can spill into it.
func markedArea(r image.Rectangle) image.Rectangle {
	r.Min.X = max(0, r.Min.X-glyphSpill)
	return r
}

// BufferWriter satisfies term.Writer with a Buffer. Besides the cells,
// it collects the image placements drawn into it so that whoever reads
// the frame back can composite them over the cells. Cells written after
// a placement cover it, as they would had it been drawn in order. A
// placement moved by pixels covers the cells it lands on when the
// writer's context carries a PixelSize, and its own cells otherwise.
type BufferWriter struct {
	width, height int
	Cursor        term.Coordinates
	cells         [][]term.Cell
	ctx           context.Context

	images []term.Image
	// pending are the placements whose cells are still marked, in draw
	// order. They move to images, cut to the cells nothing was written
	// over, on the next read. marks is allocated by the first DrawImage,
	// as most frames carry no placement.
	pending []pendingImage
	marks   []imageMark
	// open and next are scratch rectangles for cutting placements.
	open, next []image.Rectangle
}

// pendingImage is a placement and the cells it covers, which its marks
// are on.
type pendingImage struct {
	img     term.Image
	covered image.Rectangle
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
	w.images, w.pending, w.marks = nil, nil, nil
	w.open, w.next = nil, nil
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

// DrawImage satisfies term.Writer. The placement is kept, clipped to
// the buffer, until the next Clear, and Images returns it.
func (w *BufferWriter) DrawImage(img term.Image) bool {
	size, _ := PixelSizeFromContext(w.ctx)
	screen := image.Rect(0, 0, w.width, w.height)
	img, ok := img.Clipped(size.Covered(img).Intersect(screen))
	if !ok {
		return true
	}
	if w.marks == nil {
		w.marks = make([]imageMark, w.width*w.height)
	}
	idx := int32(len(w.pending))
	covered := size.Covered(img)
	m := markedArea(covered)
	for y := m.Min.Y; y < m.Max.Y; y++ {
		row, marks := w.rowMarks(y, m.Min.X, m.Max.X)
		for x := range row {
			c := &row[x]
			if c.Width == imageMarkWidth {
				continue
			}
			marks[x] = imageMark{width: c.Width, first: idx}
			c.Width = imageMarkWidth
		}
	}
	w.pending = append(w.pending, pendingImage{img: img, covered: covered})
	return true
}

// Flush satisfies term.Writer
func (w *BufferWriter) Flush() error {
	return nil
}

// Clear satisfies term.Writer. Placements live for a single frame, so
// they are dropped along with the cells.
func (w *BufferWriter) Clear(attr term.Attributes) error {
	// Release the Src references so a picture that is no longer drawn
	// does not keep its pixels alive until the slice is overwritten.
	clear(w.images)
	w.images = w.images[:0]
	clear(w.pending)
	w.pending = w.pending[:0]
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
	w.resolve()
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

// RawCells returns the raw cells written since the last Clear.
func (w *BufferWriter) RawCells() [][]term.Cell {
	w.resolve()
	return w.cells
}

// Images returns the placements drawn since the last Clear, in the
// order they were drawn, cut to the cells not written over after them.
// The slice is owned by the writer and is only valid until the next
// Clear.
func (w *BufferWriter) Images() []term.Image {
	w.resolve()
	return w.images
}

// rowMarks returns row y's cells and marks over columns [x0, x1).
func (w *BufferWriter) rowMarks(y, x0, x1 int) ([]term.Cell, []imageMark) {
	row := w.cells[y][x0:x1]
	base := y * w.width
	return row, w.marks[base+x0 : base+x1][:len(row)]
}

// resolve moves the pending placements to images and restores the
// widths their marks replaced.
func (w *BufferWriter) resolve() {
	if len(w.pending) == 0 {
		return
	}
	if w.open == nil {
		// A row's visible runs are separated by at least one covered
		// cell.
		maxRuns := (w.width + 1) / 2
		w.open = make([]image.Rectangle, 0, maxRuns)
		w.next = make([]image.Rectangle, 0, maxRuns)
	}
	for i, img := range w.pending {
		w.appendUncovered(img, int32(i))
	}
	clear(w.pending)
	w.pending = w.pending[:0]
}

// appendUncovered appends pending placement i to images, clipped to the
// cells that still show it, and resolves the marks in its area. Each
// row's visible runs are merged with the rectangle above them when their
// columns match, so a placement with a window over it splits into a
// handful of rectangles rather than one per row.
func (w *BufferWriter) appendUncovered(p pendingImage, i int32) {
	img, r := p.img, p.covered
	m := markedArea(r)
	open := w.open[:0]
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row, marks := w.rowMarks(y, m.Min.X, m.Max.X)
		next := w.next[:0]
		j, start := 0, -1
		// Columns are relative to m.Min.X, so the spill column left of
		// the placement starts out covered.
		coveredTo := r.Min.X - m.Min.X
		for x := range row {
			c, mark := &row[x], &marks[x]
			if mark.width != imageMarkWidth {
				if c.Width == imageMarkWidth {
					c.Width = mark.width
				} else {
					mark.first = coveredFrom
				}
				mark.width = imageMarkWidth
			}
			if mark.first > i {
				// A cell written after the placement covers it, and so
				// does the rest of its glyph when it is wide.
				coveredTo = max(coveredTo, x+max(1, int(c.Width)))
			}
			if x >= coveredTo {
				if start < 0 {
					start = x
				}
				continue
			}
			if start >= 0 {
				next, j = w.endRun(img, open, next, j, image.Rect(m.Min.X+start, y, m.Min.X+x, y+1))
				start = -1
			}
		}
		if start >= 0 {
			next, j = w.endRun(img, open, next, j, image.Rect(m.Min.X+start, y, r.Max.X, y+1))
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

// endRun adds run, a row's visible run, to next: it extends the
// rectangle in open[j:] above it with the same columns, and appends to
// images the ones left of it, which no later run can extend.
func (w *BufferWriter) endRun(
	img term.Image, open, next []image.Rectangle, j int, run image.Rectangle,
) ([]image.Rectangle, int) {
	for j < len(open) && open[j].Min.X < run.Min.X {
		w.images = append(w.images, clippedTo(img, open[j]))
		j++
	}
	if j < len(open) && open[j].Min.X == run.Min.X && open[j].Max.X == run.Max.X {
		run = open[j].Union(run)
		j++
	}
	return append(next, run), j
}

func clippedTo(img term.Image, r image.Rectangle) term.Image {
	img, _ = img.Clipped(r)
	return img
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
