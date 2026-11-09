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
	"io"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// A Buffer offers a high level API to manipulate a matrix of term.Cell.
type Buffer struct {
	cells    *rawCells
	undoer   *undoer
	selector selector
	rootPub  *syncPublisher
	usagePub *syncPublisher
	safew    safeEditor

	// effective View and Editor
	view   View
	editor Editor
}

type safeEditor struct {
	editor Editor
	view   View
}

func fromToInBounds(cells View, from, to term.Coordinates) (
	newFrom, newTo term.Coordinates, ok bool,
) {
	rows := cells.Rows()
	if rows == 0 || from.Y < 0 || from.Y >= rows ||
		(from.Y == rows-1 && from.X > cells.Columns(from.Y)) {
		return
	}
	if to.Y < 0 {
		return
	}

	if cols := cells.Columns(from.Y); from.X > cols {
		from.X = cols
	}

	if to.Y >= rows {
		to.Y = rows - 1
		to.X = cells.Columns(rows - 1)
	} else if cols := cells.Columns(to.Y); to.X > cols {
		to.X = cols
	}

	return from, to, true
}

func (s safeEditor) Edit(ctx context.Context, start, end term.Coordinates, str string) (
	from, to term.Coordinates, old string,
) {
	if start.Y < 0 || start.X < 0 || end.Y < 0 || end.X < 0 {
		return
	}
	// only check in case of delete range
	if start != end {
		var ok bool
		s, e, ok := fromToInBounds(s.view, start, end)
		// if it's replace, proceed with insert
		if !ok && str == "" {
			from = s
			to = s
			return
		} else if !ok {
			end = start
		} else {
			start = s
			end = e
		}
	}
	return s.editor.Edit(ctx, start, end, str)
}

// NewBuffer allocates storage for a new Buffer and initializes it.
func NewBuffer() (b *Buffer) {
	b = new(Buffer)
	b.Init()
	return b
}

// InitPerformance initializes this Buffer without Undo, Redo,
// SubscribeUsage, UnsubscribeUsage, Subscribe or Unsubscribe functionality.
// Calling any of the subscribe methods will cause the calling goroutine to panic.
func (b *Buffer) InitPerformance(rowCapacity, columnCapacity int, fillInChar rune) {
	cells := new(rawCells)
	cells.initWithCap(rowCapacity, columnCapacity, fillInChar)
	b.initPerformanceWithCells(cells)
}

// BlankCell returns the measured cell used to fill blank space, kept in
// step with the fill-in char so callers can pass its width without
// re-measuring.
func (b *Buffer) BlankCell() term.Cell {
	return b.cells.blank
}

// ResetCapacity resets the capacity given to new rows. Non-positive
// values fall back to the default capacity.
func (b *Buffer) ResetCapacity(capacity int) {
	if capacity <= 0 {
		capacity = defColumnCap
	}
	b.cells.columnCap = capacity
}

// ResetPerformanceCapacity releases the current backing storage and resets this
// performance-mode buffer to a single empty row with the requested capacities.
// It is intended for long-lived buffers that may have grown large temporarily
// and need to drop references to those large row slices.
func (b *Buffer) ResetPerformanceCapacity(rowCapacity, columnCapacity int) {
	if b.undoer != nil {
		panic("ResetPerformanceCapacity should not be used if not initialized via InitPerformance")
	}
	b.cells.resetWithCap(rowCapacity, columnCapacity)
}

// Init initializes this Buffer with the default configuration.
func (b *Buffer) Init() {
	cells := new(rawCells)
	cells.init()
	b.initWithCells(cells)
}

// Insert is equivalent to calling InsertContext with context.Background.
func (b *Buffer) Insert(pos term.Coordinates, r rune) (next term.Coordinates) {
	return b.InsertContext(context.Background(), pos, r)
}

// InsertContext inserts a rune in the given position and shift the cells to the right
func (b *Buffer) InsertContext(ctx context.Context, pos term.Coordinates, r rune) (next term.Coordinates) {
	_, next, _ = b.editor.Edit(ctx, pos, pos, string(r))
	return
}

// EditWithAttr replaces the contents of the buffer within those coordinates
// replaces it for str and applies the given term.Attributes.
func (b *Buffer) EditWithAttr(
	ctx context.Context, start, end term.Coordinates, str string, attr term.Attributes,
) (from, to term.Coordinates, old string) {
	from, to, old = b.safew.Edit(ctx, start, end, str)
	b.selector.iterateCells(from, to, func(i int, from, to term.Coordinates, cells []term.Cell) {
		for x := range cells {
			cells[x].Bg = attr.Bg
			cells[x].Fg = attr.Fg
			cells[x].Attrs = attr.Attrs
		}
	})
	return
}

// InsertWithAttr writes str and gives it attr term.Attributes.
func (b *Buffer) InsertWithAttr(
	pos term.Coordinates, r rune, attr term.Attributes,
) (next term.Coordinates) {
	_, next, _ = b.EditWithAttr(context.Background(), pos, pos, string(r), attr)
	return
}

// InsertStringWithAttr inserts str with the given attr as the background
// and foreground cell term.Attributes.
func (b *Buffer) InsertStringWithAttr(
	at term.Coordinates, str string, attr term.Attributes,
) (from, until term.Coordinates) {
	from, until, _ = b.EditWithAttr(context.Background(), at, at, str, attr)
	return
}

// DeleteRow is equivalent to calling DeleteRowContext
// with context.Background.
func (b *Buffer) DeleteRow(y int) (ok bool) {
	return b.DeleteRowContext(context.Background(), y)
}

// DeleteRowContext deletes the row at term.Coordinates.Y
func (b *Buffer) DeleteRowContext(ctx context.Context, y int) (ok bool) {
	if y < 0 || y >= b.view.Rows() {
		return
	}
	var from, to term.Coordinates
	if y == 0 {
		from = term.Coordinates{Y: y}
		to = term.Coordinates{Y: y + 1}
	} else {
		from = term.Coordinates{Y: y - 1, X: b.view.Columns(y - 1)}
		to = term.Coordinates{Y: y, X: b.view.Columns(y)}
	}
	_, _, old := b.editor.Edit(ctx, from, to, "")
	ok = old != ""
	return
}

// TruncateRowFrom is equivalent to calling TruncateRowFromContext
// with context.Background.
func (b *Buffer) TruncateRowFrom(from term.Coordinates) (ok bool) {
	return b.TruncateRowFromContext(context.Background(), from)
}

// TruncateRowFromContext truncates the row at term.Coordinates.Y starting
// from term.Coordinates.X
func (b *Buffer) TruncateRowFromContext(
	ctx context.Context, from term.Coordinates,
) (ok bool) {
	to := term.Coordinates{Y: from.Y}
	from, to, ok = fromToInBounds(b.view, from, to)
	if !ok {
		return
	}
	cols := b.view.Columns(to.Y)
	if cols == 0 {
		ok = false
		return
	}
	to.X = cols
	b.editor.Edit(ctx, from, to, "")
	return
}

// TruncateFrom is equivalent to calling TruncateFromContext with
// context.Background.
func (b *Buffer) TruncateFrom(from term.Coordinates) (ok bool) {
	return b.TruncateFromContext(context.Background(), from)
}

// TruncateFromContext truncates from the given position to the end of the buffer.
func (b *Buffer) TruncateFromContext(
	ctx context.Context, from term.Coordinates,
) (ok bool) {
	return b.truncateFromContextView(ctx, from, b.view)
}

// Replace is equivalent to calling ReplaceContext with
// context.Background.
func (b *Buffer) Replace(str string) {
	b.ReplaceContext(context.Background(), str)
}

// ReplaceContext replaces the content of the buffer with str.
func (b *Buffer) ReplaceContext(ctx context.Context, str string) {
	from := term.Coordinates{}
	to := term.Coordinates{Y: b.view.Rows()}
	b.editor.Edit(ctx, from, to, str)
}

// ResetCells replaces this Buffer's contents in place with a deep copy
// of cells. Unlike Replace, it does NOT route through the Editor chain —
// any Editor installed via WithEditor and any subscribers are bypassed.
// Use for snapshot restore and similar bulk reloads where preserving the
// *cell.Buffer / *rawCells identity matters and the change is not a
// user-observable edit.
//
// In performance mode the copy uses a single contiguous slab so the
// rowsContiguous fast path applies; otherwise a row-by-row copy is used.
// The caller's slice is never aliased.
//
// Panics if cells is empty: the Buffer always maintains the invariant
// that there is at least one row. Callers wanting to clear a buffer
// should use Reset or ResetPerformanceCapacity instead.
func (b *Buffer) ResetCells(cells [][]term.Cell) {
	if len(cells) == 0 {
		panic("cell.Buffer.ResetCells: cells must contain at least one row")
	}
	var copied [][]term.Cell
	if b.undoer == nil {
		// performance mode: keep rows contiguous in one slab.
		copied = copyCellsContiguous(make([][]term.Cell, len(cells)), cells)
	} else {
		copied = term.CopyCells(make([][]term.Cell, len(cells)), cells)
	}
	b.cells.adoptCells(copied)
}

// ConflateRow is equivalent to calling ConflateRowContext
// with context.Background.
func (b *Buffer) ConflateRow(y int) (x int, ok bool) {
	return b.ConflateRowContext(context.Background(), y)
}

// ConflateRowContext will conflate row at index i with the next row
// It's the opposite operation as WrapRowContext.
func (b *Buffer) ConflateRowContext(
	ctx context.Context, y int,
) (x int, ok bool) {
	if b.undoer == nil {
		if y < 0 || b.Rows() <= 1 || y >= b.Rows()-1 {
			return 0, false
		}
		b.cells.linearize()
		x = len(b.cells.cells[y])
		b.cells.conflate(y)
		return x, true
	}

	from := term.Coordinates{Y: y}
	to := term.Coordinates{Y: y + 1}
	from, to, ok = fromToInBounds(b.view, from, to)
	if !ok || b.Rows() <= 1 || y == b.Rows()-1 {
		ok = false
		return
	}
	from.X = b.view.Columns(from.Y)
	b.editor.Edit(ctx, from, to, "")
	x = from.X
	return
}

// MergeMarkedRows merges runs of rows whose last cell is marked, treating
// each run as a single logical line, in a single O(rows) pass over the
// underlying row slice.
//
// Only row indexes in [0, end) are eligible to start a group. Groups may
// still extend beyond end if a matching marker is found on later rows.
//
// clearMark is called to reset the sentinel on the head cell of each group
// and on any intermediate cells that were marked.
//
// This is only supported in performance mode.
func (b *Buffer) MergeMarkedRows(
	end int,
	isMark func(term.Cell) bool,
	clearMark func(*term.Cell),
) (merged int, ok bool) {
	if b.undoer != nil {
		panic("MergeMarkedRows should not be used if not initialized via InitPerformance")
	}
	b.cells.linearize()
	return b.cells.mergeMarkedRows(end, isMark, clearMark), true
}

// RowSplit describes a single row split applied by SplitRowsBatch: row Y is
// re-sliced into (Times+1) consecutive rows, the first Times of them having
// length Width, plus a trailing row containing whatever remainder is left.
type RowSplit struct {
	Y     int
	Width int
	Times int
}

// SplitRowsBatch applies the provided splits in a single pass over the row
// slice. Splits must reference distinct, strictly increasing row indices,
// and Width*Times must be <= row length for each split. Only supported in
// performance mode.
func (b *Buffer) SplitRowsBatch(splits []RowSplit) (added int, ok bool) {
	if b.undoer != nil {
		panic("SplitRowsBatch should not be used if not initialized via InitPerformance")
	}
	if !b.validRowSplits(splits) {
		return 0, false
	}
	b.cells.linearize()
	return b.cells.splitRowsBatch(splits, 0, term.Cell{}), true
}

// SplitRowsBatchPadded is like SplitRowsBatch but any tail piece of a split
// whose length is less than padToWidth is padded to padToWidth with fill.
// Padding is performed by materializing new rows drawn from a single slab
// allocation so per-tail allocations are avoided. Only supported in
// performance mode.
func (b *Buffer) SplitRowsBatchPadded(splits []RowSplit, padToWidth int, fill term.Cell) (added int, ok bool) {
	if b.undoer != nil {
		panic("SplitRowsBatchPadded should not be used if not initialized via InitPerformance")
	}
	if padToWidth < 0 || !b.validRowSplits(splits) {
		return 0, false
	}
	b.cells.linearize()
	return b.cells.splitRowsBatch(splits, padToWidth, fill), true
}

func (b *Buffer) validRowSplits(splits []RowSplit) bool {
	previousY := -1
	for _, split := range splits {
		if split.Y <= previousY || split.Y < 0 || split.Y >= b.cells.Rows() {
			return false
		}
		previousY = split.Y
		if split.Width <= 0 || split.Times <= 0 {
			continue
		}
		if split.Width > len(b.cells.row(split.Y))/split.Times {
			return false
		}
	}
	return true
}

// ExtendRowToWidth pads row y with fill cells until its length equals
// width, in a single allocation when growing capacity is needed. It is
// equivalent to repeated InsertContext calls appending the fill char, but
// avoids repeated slice grows and editor overhead. Only supported in
// performance mode; returns (0, false) otherwise.
func (b *Buffer) ExtendRowToWidth(y, width int) (added int, ok bool) {
	if b.undoer != nil {
		panic("ExtendRowToWidth should not be used if not initialized via InitPerformance")
	}
	return b.cells.extendRowToWidth(y, width), true
}

// TrimRowsFromEnd removes up to count trailing rows from the buffer without
// allocating intermediate strings. Only supported in performance mode;
// returns (0, false) otherwise. Returns the number of rows actually removed.
func (b *Buffer) TrimRowsFromEnd(count int) (removed int, ok bool) {
	if b.undoer != nil {
		panic("TrimRowsFromEnd should not be used if not initialized via InitPerformance")
	}
	return b.cells.trimRowsFromEnd(count), true
}

// TrimRowsFromStart removes up to count leading rows from the buffer
// without allocating intermediate strings, keeping at least one row.
// Trimmed row storage is recycled for reuse by AppendBlankRows. Only
// supported in performance mode. Returns the number of rows removed.
func (b *Buffer) TrimRowsFromStart(count int) (removed int, ok bool) {
	if b.undoer != nil {
		panic("TrimRowsFromStart should not be used if not initialized via InitPerformance")
	}
	return b.cells.trimRowsFromStart(count), true
}

// AppendBlankRows appends count rows of width cells filled with the
// buffer's fill-in char after the last row, reusing storage recycled by
// TrimRowsFromStart when available. Only supported in performance mode.
func (b *Buffer) AppendBlankRows(count, width int) (ok bool) {
	if b.undoer != nil {
		panic("AppendBlankRows should not be used if not initialized via InitPerformance")
	}
	b.cells.appendBlankRows(count, width)
	return true
}

// AppendBlankRowsBounded appends blank rows while keeping at most limit
// rows. Once full, the oldest row storage is recycled as the new last
// row without shifting the remaining row slice.
func (b *Buffer) AppendBlankRowsBounded(count, width, limit int) (ok bool) {
	if b.undoer != nil {
		panic("AppendBlankRowsBounded should not be used if not initialized via InitPerformance")
	}
	b.cells.appendBlankRowsBounded(count, width, limit)
	return true
}

// WrapRow is equivalent to calling WrapRowContext
// with context.Background.
func (b *Buffer) WrapRow(y, at int) (ok bool) {
	return b.WrapRowContext(context.Background(), y, at)
}

// WrapRowContext will wraps the row at index i at the given column.
// It's the opposite operation as ConflateRowContext.
func (b *Buffer) WrapRowContext(
	ctx context.Context, y, at int,
) (ok bool) {
	if b.undoer == nil {
		if y < 0 || y >= b.Rows() || at > b.Columns(y) {
			return false
		}
		b.cells.linearize()
		b.cells.insertNewRow(term.Coordinates{Y: y, X: at})
		return true
	}

	if y < 0 || y >= b.Rows() || at > b.Columns(y) {
		return
	}
	pos := term.Coordinates{Y: y, X: at}
	b.editor.Edit(ctx, pos, pos, "\n")
	ok = true
	return
}

// DeleteCell is equivalent to calling DeleteCellContext
// with context.Background.
func (b *Buffer) DeleteCell(pos term.Coordinates) (term.Coordinates, rune, bool) {
	return b.DeleteCellContext(context.Background(), pos)
}

// DeleteCellContext removes the cell at the given position.
// It returns the position at which the current cell started,
// if the width was > 1.
func (b *Buffer) DeleteCellContext(
	ctx context.Context, pos term.Coordinates,
) (term.Coordinates, rune, bool) {
	from := pos
	to := term.Coordinates{X: from.X + 1, Y: from.Y}
	from, to, ok := fromToInBounds(b.view, from, to)
	if !ok {
		return term.Coordinates{}, 0, false
	}

	start, _, str := b.editor.Edit(ctx, from, to, "")
	if str == "" {
		return term.Coordinates{}, 0, false
	}

	return start, []rune(str)[0], true
}

// Rows returns the number of rows in the Buffer.
func (b *Buffer) Rows() int {
	return b.view.Rows()
}

// Columns returns the number of cells of row at index y.
func (b *Buffer) Columns(y int) int {
	return b.view.Columns(y)
}

// MaxColumns returns the max number of columns.
func (b *Buffer) MaxColumns() (max int) {
	for i := 0; i < b.Rows(); i++ {
		if col := b.Columns(i); col > max {
			max = col
		}
	}
	return
}

// Cell returns the cell and true or a zero-valued cell and false if there is no
// cell at position.
func (b *Buffer) Cell(pos term.Coordinates) (term.Cell, bool) {
	return b.view.Cell(pos)
}

// Row returns the cells at logical row y. The returned slice is
// invalidated by operations that add, remove, or reorder rows.
func (b *Buffer) Row(y int) []term.Cell {
	view, internal := b.view.(*rawCells)
	if !internal || view != b.cells {
		cells := b.view.RawCells()
		if y < 0 || y >= len(cells) {
			return nil
		}
		return cells[y]
	}
	if y < 0 || y >= b.cells.Rows() {
		return nil
	}
	return b.cells.row(y)
}

// MutableRow returns row y and records that cells before end may be
// modified. It is only supported in performance mode.
func (b *Buffer) MutableRow(y, end int) []term.Cell {
	if b.undoer != nil {
		panic("MutableRow should not be used if not initialized via InitPerformance")
	}
	if y < 0 || y >= b.cells.Rows() {
		return nil
	}
	b.cells.ensureRowMeta()
	physical := b.cells.physicalRow(y)
	b.cells.rowMeta[physical].occupied = max(b.cells.rowMeta[physical].occupied, end)
	return b.cells.cells[physical]
}

// SetRow replaces the cells at logical row y. It is only supported in
// performance mode.
func (b *Buffer) SetRow(y int, row []term.Cell) {
	if b.undoer != nil {
		panic("SetRow should not be used if not initialized via InitPerformance")
	}
	if y < 0 || y >= b.cells.Rows() {
		return
	}
	b.cells.ensureRowMeta()
	physical := b.cells.physicalRow(y)
	b.cells.cells[physical] = row
	b.cells.rowMeta[physical].occupied = max(b.cells.rowMeta[physical].occupied, len(row))
}

// DeleteRowRange removes [start, end) from logical row y without
// linearizing circular row storage. It is only supported in performance
// mode.
func (b *Buffer) DeleteRowRange(y, start, end int) {
	if b.undoer != nil {
		panic("DeleteRowRange should not be used if not initialized via InitPerformance")
	}
	if y < 0 || y >= b.cells.Rows() || start < 0 || start >= end {
		return
	}
	columns := len(b.cells.row(y))
	if start >= columns {
		return
	}
	row := b.MutableRow(y, columns)
	end = min(end, len(row))
	copy(row[start:], row[end:])
	b.SetRow(y, row[:len(row)-(end-start)])
}

// CopyRows copies rows in logical order into dst, reusing dst's row
// capacities where possible.
func (b *Buffer) CopyRows(dst [][]term.Cell) [][]term.Cell {
	rows := b.Rows()
	if len(dst) > rows {
		dst = dst[:rows]
	} else {
		for len(dst) < rows {
			dst = append(dst, nil)
		}
	}
	for y := range rows {
		row := b.Row(y)
		if row == nil {
			dst[y] = nil
			continue
		}
		if cap(dst[y]) < len(row) {
			dst[y] = make([]term.Cell, len(row))
		} else if len(row) == 0 && dst[y] == nil {
			dst[y] = make([]term.Cell, 0)
		} else {
			dst[y] = dst[y][:len(row)]
		}
		copy(dst[y], row)
	}
	return dst
}

// ResetRowRange fills [start, end) in logical row y. Resetting a whole
// row establishes a new blank template so later recycling only clears
// cells modified after this call.
func (b *Buffer) ResetRowRange(y, start, end int, fill term.Cell) {
	if b.undoer != nil {
		panic("ResetRowRange should not be used if not initialized via InitPerformance")
	}
	b.cells.resetRowRange(y, start, end, fill)
}

// RotateRows rotates [start, end) toward lower row indexes by count.
// A negative count rotates toward higher indexes.
func (b *Buffer) RotateRows(start, end, count int) {
	if b.undoer != nil {
		panic("RotateRows should not be used if not initialized via InitPerformance")
	}
	b.cells.rotateRows(start, end, count)
}

// RawCells gives clients read-only access to the underlying cell matrix.
// The rows alias the buffer's live storage, so callers must not mutate
// them or retain them across an edit; use term.CloneCells to snapshot.
func (b *Buffer) RawCells() [][]term.Cell {
	return b.view.RawCells()
}

// InsertString is equivalent to calling InsertStringContext
// with context.Background.
func (b *Buffer) InsertString(at term.Coordinates, str string) (
	from, until term.Coordinates,
) {
	return b.InsertStringContext(context.Background(), at, str)
}

// InsertStringContext inserts string in the given position and shifts the remaining cells.
// insert never fails: if at is out-of-bounds, this method fills in the rows
// and/or columns of cells with blank spaces.
// It returns the start of the insert 'from', including the filled-in blank spaces
// and where the next logical Insert should go 'until'.
func (b *Buffer) InsertStringContext(
	ctx context.Context, at term.Coordinates, str string,
) (from, until term.Coordinates) {
	from, until, _ = b.editor.Edit(ctx, at, at, str)
	return
}

// Delete is equivalent to calling DeleteContext with
// context.Background.
func (b *Buffer) Delete(from, to term.Coordinates) (start term.Coordinates, str string) {
	return b.DeleteContext(context.Background(), from, to)
}

// DeleteContext removes cells in left-inclusive right-exclusive range
// and returns the corresponding string representation of the cells removed,
// along with the true start of the range.
func (b *Buffer) DeleteContext(ctx context.Context, from, to term.Coordinates) (
	start term.Coordinates, str string,
) {
	start, _, str = b.safew.Edit(ctx, from, to, "")
	return
}

// Edit removes cells in left-inclusive right-exclusive range (start, end) and
// inserts s at the start of the range. It returns the corresponding string
// representation of the content removed and the true from, to range.
//
// As opposed to cell.Editor.Edit, this method does not panic if range
// is out of bounds. Instead, it trims the coordinates to be in-bounds or
// simply does nothing and returned str is empty.
func (b *Buffer) Edit(ctx context.Context, start, end term.Coordinates, s string) (
	from, to term.Coordinates, old string,
) {
	return b.safew.Edit(ctx, start, end, s)
}

// DeleteLine is equivalent to calling DeleteLineContext
// with context.Background.
func (b *Buffer) DeleteLine(from, to term.Coordinates) (
	start term.Coordinates, str string,
) {
	return b.DeleteLineContext(context.Background(), from, to)
}

// DeleteLineContext deletes the lines starting at from, between from, to and including end.
// See Delete for more information about the return values.
func (b *Buffer) DeleteLineContext(
	ctx context.Context, from, to term.Coordinates,
) (start term.Coordinates, str string) {
	from, to = term.CoordinatesSort(from, to)
	// special case: fromToInbounds trims y>=rows,
	// and resets to max cols of last row, but a DeleteLineContext
	// should always delete an entire line, including the newline character, if possible:
	// The only instance where it's not possible and not expected is when rows <= 1.
	lastRow := b.Rows() - 1
	specialCase := from.Y == lastRow && to.Y >= lastRow && b.Rows() > 1
	from, to, ok := fromToInBounds(b.view, from, to)
	if !ok {
		return
	}
	if specialCase {
		to.Y = from.Y
		to.X = b.Columns(to.Y)
		from.Y--
		from.X = b.Columns(from.Y)
	} else {
		from.X, to.X = 0, 0
		to.Y++
	}
	start, _, str = b.editor.Edit(ctx, from, to, "")
	return
}

// DeleteBlock is equivalent to calling DeleteBlockContext
// with context.Background.
func (b *Buffer) DeleteBlock(from, to term.Coordinates) (
	start term.Coordinates, str string,
) {
	return b.DeleteBlockContext(context.Background(), from, to)
}

// DeleteBlockContext deletes the blocks of cells between from, to. See SelectBlock for more
// information about how DeleteBlock selects the cells to delete.
// See Delete for more information about the return values.
func (b *Buffer) DeleteBlockContext(
	ctx context.Context, from, to term.Coordinates,
) (start term.Coordinates, str string) {
	var builder strings.Builder
	b.selector.iterateBlocks(from, to,
		func(i int, from, to term.Coordinates, cells []term.Cell) {
			if i != 0 {
				builder.WriteRune('\n')
			}
			columns := b.Columns(from.Y)
			if columns == 0 || from.X > columns {
				if i == 0 {
					start = term.Coordinates{Y: from.Y}
				}
				return
			}

			if to.X > columns {
				to.X = columns
			}
			blockStart, _, str := b.safew.Edit(ctx, from, to, "")

			if i == 0 {
				start = blockStart
			}
			builder.WriteString(str)
		})

	str = builder.String()
	return
}

// Reset resets the contents of this Buffer.
// When the the buffer's view has not been updated via WithView, then
// Reset is equivalent to TruncateFrom(term.Coordinates{}). On the other
// hand, when the view has been updated, Reset differs from TruncateFrom
// in that it provides a guarantee that all of the content is removed.
func (b *Buffer) Reset() {
	b.truncateFromContextView(context.Background(), term.Coordinates{}, b.cells)
}

// ReloadContents replaces the whole buffer with str as a single edit
// dispatched at the undo layer, so SubscribeUsage subscribers do not
// observe it while Subscribe subscribers and undo do. A disk reload is
// an internal full-buffer replacement, not a user edit: usage
// subscribers that translate user edits into side effects (clipboard
// copy-on-delete, dot-repeat capture) must not see the cleared
// contents, the same way they do not see undo replays.
func (b *Buffer) ReloadContents(ctx context.Context, str string) {
	from, to, ok := fromToInBounds(b.cells,
		term.Coordinates{}, term.Coordinates{Y: b.cells.Rows()})
	if !ok {
		return
	}
	b.undoer.Edit(ctx, from, to, str)
}

// ResetPerformance resets the underlying cells, without discarding their capacity.
// This shopuld only be used when buffer has been initialized with InitPerformance,
// as subscribers won't be notified of this change, and it will break undo correctness.
func (b *Buffer) ResetPerformance() {
	b.cells.linearize()
	for y, row := range b.cells.cells {
		b.cells.cells[y] = row[:0]
	}
}

// Version returns the version of this buffer.
func (b *Buffer) Version() int {
	if b.undoer == nil {
		return 0
	}
	return b.undoer.version
}

// ReadFrom reads data from r until EOF and appends it to the buffer, growing
// the buffer as needed. The return value n is the number of bytes read. Any
// error except io.EOF encountered during the read is also returned.
func (b *Buffer) ReadFrom(r io.Reader) (int64, error) {
	return b.cells.readFromWithView(r, b.view)
}

// ResetVersion reset version resets the version of the buffer to 0,
// so further calls to Undo will fail until buffer is edited further.
func (b *Buffer) ResetVersion() {
	b.undoer.version = 0
}

// Write satisfies io.Writer.
func (b *Buffer) Write(p []byte) (int, error) {
	nextWrite := nextWrite(b.view)
	b.editor.Edit(context.Background(), nextWrite, nextWrite, string(p))
	return len(p), nil
}

// WriteString is equivalent to WriteStringContext with context.Background.
func (b *Buffer) WriteString(p string) {
	b.WriteStringContext(context.Background(), p)
}

// WriteStringContext writes the given string at the end of the buffer
func (b *Buffer) WriteStringContext(ctx context.Context, p string) {
	nextWrite := nextWrite(b.view)
	b.editor.Edit(ctx, nextWrite, nextWrite, p)
}

// WriteStringWithAttr inserts str with the given attr as the background
// and foreground cell term.Attributes.
func (b *Buffer) WriteStringWithAttr(str string, attr term.Attributes) {
	at := nextWrite(b.view)
	b.InsertStringWithAttr(at, str, attr)
}

// Undo reverses the last update to the Buffer.
// Redo can be used to reverse Undo.
func (b *Buffer) Undo() (bool, term.Coordinates) {
	if b.undoer == nil {
		return false, term.Coordinates{}
	}
	return b.undoer.undo()
}

// MarkStartUndo marks the current undo version as the start of the next
// group created by GroupUndo.
func (b *Buffer) MarkStartUndo() bool {
	if b.undoer == nil {
		return false
	}
	return b.undoer.startMergeUndo()
}

// GroupUndo merges all the undos since the last MarkStartUndo was called,
// or all undos currently available.
func (b *Buffer) GroupUndo() bool {
	if b.undoer == nil {
		return false
	}
	return b.undoer.endMergeUndo()
}

// GroupEdits runs fn and makes the edits it applies a single undo step.
// Unlike a MarkStartUndo/GroupUndo pair it nests: a MarkStartUndo scope that
// encloses the call, or that a subscriber opens while fn runs, still covers
// the group when its GroupUndo arrives.
func (b *Buffer) GroupEdits(fn func()) {
	if b.undoer == nil {
		fn()
		return
	}
	b.undoer.group(fn)
}

// Redo reverses the previously reversed update to the Buffer.
func (b *Buffer) Redo() (bool, term.Coordinates) {
	if b.undoer == nil {
		return false, term.Coordinates{}
	}
	return b.undoer.redo()
}

// Select returns the cells inside the given coordinates or nil if coordinates
// are out of bounds.
func (b *Buffer) Select(from term.Coordinates, to term.Coordinates) (
	[][]term.Cell, []Selection, bool,
) {
	from, to, ok := fromToInBounds(b.view, from, to)
	if !ok {
		return nil, nil, false
	}
	cell, sels := b.selector.selectCells(from, to)
	return cell, sels, true
}

// TokenAt returns the token that satisfies the isAllowed function
// and its start and end positions.
//
// It returns an empty string if the token at the given position does
// not satisfy isAllowed.
func (b *Buffer) TokenAt(pos term.Coordinates, isAllowed func(rune) bool) (
	term.Coordinates, term.Coordinates, string,
) {
	var builder strings.Builder
	cells := b.RawCells()
	rows := b.Rows()

	start := pos
	for start.Y < rows && start.X < b.Columns(start.Y) && start.X >= 0 {
		c := cells[start.Y][start.X]
		if isAllowed(c.Ch) {
			start.X--
			continue
		}
		break
	}

	if start == pos {
		return start, start, builder.String()
	}

	start.X++
	end := start
	for end.Y < rows && end.X < b.Columns(end.Y) && end.X >= 0 {
		c := cells[end.Y][end.X]
		if isAllowed(c.Ch) {
			end.X++
			builder.WriteRune(c.Ch)
			continue
		}
		break
	}

	return start, end, builder.String()
}

// SelectLine returns the lines inside the given coordinates or nil if
// coordinates are out of bounds.
func (b *Buffer) SelectLine(from term.Coordinates, to term.Coordinates) (
	[][]term.Cell, []Selection, bool,
) {
	from, to, ok := fromToInBounds(b.view, from, to)
	if !ok {
		return nil, nil, false
	}
	cell, sels := b.selector.selectLine(from, to)
	return cell, sels, true
}

// SelectBlock returns the block of cells inside the given coordinates or nil if
// coordinates are out of bounds.
func (b *Buffer) SelectBlock(from term.Coordinates, to term.Coordinates) (
	[][]term.Cell, []Selection, bool,
) {
	from, to, ok := fromToInBounds(b.view, from, to)
	if !ok {
		return nil, nil, false
	}
	cell, sels := b.selector.selectBlock(from, to)
	return cell, sels, true
}

func (b *Buffer) String() string {
	return b.view.String()
}

// ShiftRowRight shifts row one tab to the right. It returns
// the number of cells that the line was shifted.
func (b *Buffer) ShiftRowRight(row int) {
	b.Insert(term.Coordinates{Y: row}, '\t')
}

// ShiftRowLeft shifts row one tab to the left. It returns
// the number of cells that the line was shifted.
func (b *Buffer) ShiftRowLeft(row int) bool {
	from, to := term.Coordinates{Y: row}, term.Coordinates{Y: row, X: 1}
	from, to, ok := fromToInBounds(b.view, from, to)
	if !ok {
		return false
	}
	c, ok := b.view.Cell(from)
	if !ok {
		return false
	}
	switch c.Ch {
	case '\t', ' ':
		b.Delete(from, to)
		return true
	default:
		return false
	}
}

// Subscribe subscribes s to all updates to the underlying buffer.
func (b *Buffer) Subscribe(s Subscriber) {
	b.rootPub.Subscribe(s)
}

// Unsubscribe unsubscribes s from updates.
func (b *Buffer) Unsubscribe(s Subscriber) {
	b.rootPub.Unsubscribe(s)
}

// SubscribeUsage subscribes s to direct update calls to this buffer. Unlike Subscribe,
// this method does not capture indirect updates to Buffer, for instance via Undo.
func (b *Buffer) SubscribeUsage(s Subscriber) {
	b.usagePub.Subscribe(s)
}

// UnsubscribeUsage reverses SubscribeUsage.
func (b *Buffer) UnsubscribeUsage(s Subscriber) {
	b.usagePub.Unsubscribe(s)
}

// Height returns the required height if this Buffer was to be drawn on a term.Editor.
func (b *Buffer) Height() int {
	return b.Rows()
}

// Width returns the required width if this Buffer was to be drawn on a term.Editor.
func (b *Buffer) Width() int {
	var ret int
	for _, row := range b.RawCells() {
		if len(row) > ret {
			ret = len(row)
		}
	}
	return ret
}

// View returns this Buffer as a cell.View.
func (b *Buffer) View() View {
	return b.view
}

// Editor returns a cell.Editor that doesn't panic on out-of-bounds calls.
func (b *Buffer) Editor() Editor {
	return b.safew
}

// Size returns the total size in cells of this buffer.
func (b *Buffer) Size() (ret int) {
	for _, row := range b.RawCells() {
		ret += len(row)
	}
	return
}

// WithView installs a new view and returns this Buffer's previous view.
// This should only be utilized for advanced use cases.
func (b *Buffer) WithView(v View) (ret View) {
	ret = b.view
	b.setView(v)
	return
}

// WithEditor installs a new Editor and returns this Buffer's previous Editor.
// This should only be utilized for advanced use cases.
func (b *Buffer) WithEditor(e Editor) (ret Editor) {
	ret = b.editor
	b.setEditor(e)
	return
}

func (b *Buffer) setView(view View) {
	b.view = view
	b.safew.view = b.view
	b.selector.view = b.view
}

func (b *Buffer) setEditor(ed Editor) {
	b.editor = ed
	b.safew.editor = ed
}

func (b *Buffer) initWithCells(cells *rawCells) {
	b.cells = cells
	// setup the root publisher as the deepest Editor
	b.rootPub = newPublisher(b.cells)
	b.undoer = newUndoer(b.rootPub)
	// setup the usage publisher at the shallowest Editor
	b.usagePub = newPublisher(b.undoer)

	b.setEditor(b.usagePub)
	b.setView(b.cells)
}

func (b *Buffer) initPerformanceWithCells(cells *rawCells) {
	b.cells = cells
	b.cells.performance = true

	b.setEditor(b.cells)
	b.setView(b.cells)
}

func (b *Buffer) truncateFromContextView(
	ctx context.Context, from term.Coordinates, view View,
) (ok bool) {
	to := term.Coordinates{Y: view.Rows()}
	from, to, ok = fromToInBounds(view, from, to)
	if !ok {
		return
	}
	b.editor.Edit(ctx, from, to, "")
	return
}
