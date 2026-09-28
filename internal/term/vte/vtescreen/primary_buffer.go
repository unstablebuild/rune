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

package vtescreen

import (
	"fmt"
	"strings"

	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/vte/vteparser"
)

// PrimaryBuffer wraps a Buffer to provide scroll-back for a primary vte screen buffer.
// This implementation doesn't provide methods to update the underlying offset
// (it's always 0, with InvertOffset set to true), so callers must access the underlying
// Cells and add scroll via an external component. This is important to keep
// implementation simple, and avoid user scrolling interfering with standard vte processing.
type PrimaryBuffer struct {
	AltBuffer
	wraps      int
	maxHistory int
	minWidth   int
}

// NewPrimaryBuffer allocates storage for a new PrimaryBuffer and initializes it.
func NewPrimaryBuffer(minWidth, maxHistory int) *PrimaryBuffer {
	ret := new(PrimaryBuffer)
	ret.Init(minWidth, maxHistory)
	return ret
}

// Init initializes this PrimaryBuffer.
func (b *PrimaryBuffer) Init(minWidth int, maxHistory int) {
	b.AltBuffer.Init()
	b.maxHistory = maxHistory
	b.minWidth = minWidth
}

// Restore replaces this primary buffer's rendered cells and cursor with a
// saved snapshot while preserving primary-buffer configuration.
func (b *PrimaryBuffer) Restore(cells [][]term.Cell, cursor term.Coordinates, width, height int) {
	b.AltBuffer.restore(cells, cursor, max(b.minWidth, width), height)
}

// ScrollUpHistory scrolls the visible screen up by count lines by
// appending blank rows after the last row, growing history. The oldest
// row allocation is recycled once maxHistory rows are present. When
// history is disabled (maxHistory <= 0) the visible buffer is rotated.
func (b *PrimaryBuffer) ScrollUpHistory(count int) {
	if b.maxHistory <= 0 {
		b.ScrollUp(0, b.Cells.Rows(), count)
		return
	}
	b.Cells.AppendBlankRowsBounded(count, b.width, max(b.maxHistory, b.height))
}

// MarkWrapAtCursor marks the current line/column of the cursor
// as a wrapped line, so it can later be un-wrapped upon Resize.
func (b *PrimaryBuffer) MarkWrapAtCursor() {
	pos := b.CursorAtScroll()
	c := b.CellAt(pos)
	if c == nil {
		return
	}
	// use unused field to mark that line is wrapped oob
	c.Bytes = cell.WrapMarker
}

// LogicalRow is a position within the buffer's logical lines, as
// returned by PrimaryBuffer.LogicalRow.
type LogicalRow struct {
	Line, Offset int
}

// LogicalRow locates absolute row y within the logical lines of this
// buffer: the index of the logical line containing it and y's offset
// within that line. A logical line is a maximal run of rows joined by
// wrap markers, and a resize only changes how many rows each one
// occupies, so the pair survives the reflow. Rows above the buffer
// keep their distance as a negative offset from line 0.
func (b *PrimaryBuffer) LogicalRow(y int) (line, offset int) {
	if y < 0 {
		return 0, y
	}
	rows := b.Cells.RawCells()
	head := 0
	for i := range min(y, len(rows)) {
		if !isWrapped(rows[i]) {
			line++
			head = i + 1
		}
	}
	return line, y - head
}

// RowForLogical is the inverse of LogicalRow, clamped to the rows the
// logical line occupies now.
func (b *PrimaryBuffer) RowForLogical(line, offset int) int {
	rows := b.Cells.RawCells()
	head := 0
	for seen := 0; seen < line && head < len(rows); head++ {
		if !isWrapped(rows[head]) {
			seen++
		}
	}
	if offset <= 0 {
		return head + offset
	}
	last := head
	for last < len(rows)-1 && isWrapped(rows[last]) {
		last++
	}
	return min(head+offset, last)
}

// isWrapped reports whether a row is continued on the next one.
func isWrapped(row []term.Cell) bool {
	return len(row) > 0 && row[len(row)-1].Bytes == cell.WrapMarker
}

// Resize resizes this Buffer and resets the vertical margins.
func (b *PrimaryBuffer) Resize(width, height int) {
	cursor := b.CursorAtScroll()
	savedCursor := b.scroll.WindowToScrollCoordinates(b.savedCursor.position)

	origWidth := width
	width = max(b.minWidth, width)

	var wraps int
	if width != 0 && width > b.width {
		wraps = b.growColumns(width, height)
	} else if width != 0 && width < b.width {
		wraps = b.shrinkColumns(width)
	}
	if height != 0 && (height > b.height || wraps < b.wraps) {
		b.growLines(width, height)
	}
	if height != 0 && (height < b.height || wraps > b.wraps) {
		b.shrinkLines(height)
	}
	b.wraps = wraps
	b.width = width
	b.height = height
	b.SetScrollableRegion(0, 0, true)
	b.scroll.Resize(origWidth, height)
	b.Cells.ResetCapacity(width)

	cursor.Y = max(0, cursor.Y+wraps)
	b.cursor.position, _ = b.scroll.ScrollToWindowCoordinates(cursor)
	savedCursor.Y = max(0, b.savedCursor.position.Y+wraps)
	b.savedCursor.position = savedCursor
}

func (b *PrimaryBuffer) growLines(width, height int) {
	if b.Cells.Rows() < height {
		pos := term.Coordinates{
			Y: max(0, height-1),
			X: max(0, width-1),
		}
		b.Cells.InsertContext(b.AltBuffer.ctx, pos, b.AltBuffer.defaultChar)
	}
}

func (b *PrimaryBuffer) shrinkLines(height int) {
	pos := b.CursorAtScroll()
	rows := b.Cells.Rows()
	if rows <= height {
		return
	}
	// We may trim any rows beyond the cursor row down to at most `height`.
	floor := max(pos.Y+1, height)
	if rows <= floor {
		return
	}
	if _, ok := b.Cells.TrimRowsFromEnd(rows - floor); ok {
		return
	}
	for y := b.Cells.Rows() - 1; y > 0 && b.Cells.Rows() > height; y-- {
		if pos.Y >= y {
			break
		}
		from := term.Coordinates{Y: y}
		to := term.Coordinates{Y: y}
		b.Cells.DeleteLineContext(b.AltBuffer.ctx, from, to)
	}
}

func (b *PrimaryBuffer) growColumns(width, _ int) (wraps int) {
	if width == 0 {
		return
	}
	pos := b.CursorAtScroll()
	for y := max(0, b.Cells.Rows()-1); y >= 0; y-- {
		if y == pos.Y {
			wraps = b.wrapTopLines(y, width)
			break
		}
		b.Cells.ExtendRowToWidth(y, width)
	}

	return
}

func (b *PrimaryBuffer) shrinkColumns(width int) (wraps int) {
	pos := b.CursorAtScroll()
	var y int
	for y = max(0, b.Cells.Rows()-1); y >= 0; y-- {
		if y == pos.Y {
			wraps = b.wrapTopLines(y, width)
			break
		}
		if cols := b.Cells.Columns(y); cols > width {
			from := term.Coordinates{Y: y, X: width}
			to := term.Coordinates{Y: y, X: cols}
			b.Cells.DeleteContext(b.AltBuffer.ctx, from, to)
		}
	}

	y += wraps
	for ; y > 0 && y < b.Cells.Rows(); y-- {
		if cols := b.Cells.Columns(y); cols > width {
			from := term.Coordinates{Y: y, X: width}
			to := term.Coordinates{Y: y, X: cols}
			b.Cells.DeleteContext(b.AltBuffer.ctx, from, to)
		}
	}

	return
}

func (b *PrimaryBuffer) wrapTopLines(at, width int) (n int) {
	if width == 0 {
		return
	}
	// Unwrap previous wraps in a single O(rows) pass.
	merged, ok := b.Cells.MergeMarkedRows(at+1,
		func(c term.Cell) bool { return c.Bytes == cell.WrapMarker },
		func(c *term.Cell) { c.Bytes = 0 })
	if ok {
		at -= merged
		n -= merged
	}
	b.log(log.TraceLevel, "un-wrapped %d lines", -n)
	if at < 0 {
		return
	}
	// wraps represents the wrapped lines and so
	// it's always a positive number, whereas n represent the total
	// wraps variation, so it can be negative if we unwrapped more
	// lines that we wrapped.
	//
	// After the unwrap pass, rows [0..at] are each a single logical line.
	// For each one we either:
	//   - pad short rows to `width`,
	//   - trim trailing blanks and possibly split into multiple rows of
	//     length `width`.
	//
	// Trim and pad operations mutate only a single row. Splits change the
	// outer row count — we collect them and apply in a single batched
	// structural edit (`SplitRowsBatchPadded`) to avoid repeated O(rows)
	// slice shifts.
	var splits []cell.RowSplit
	var created int
	cells := b.Cells.RawCells()
	for y := 0; y <= at && y < len(cells); y++ {
		row := cells[y]
		var x int
		for x = len(row) - 1; x > 0; x-- {
			cell := row[x]
			if cell.Ch != b.defaultChar && cell.Ch != ' ' {
				break
			}
		}
		cols := len(row)
		switch {
		case x < width && width <= cols:
			from := term.Coordinates{Y: y, X: width}
			to := term.Coordinates{Y: y, X: cols}
			b.Cells.DeleteContext(b.AltBuffer.ctx, from, to)
		case x >= width && width <= cols:
			// If trimming trailing blanks would not change the split
			// count, skip the trim entirely and let the trailing blanks
			// become part of the tail row. This avoids a DeleteContext
			// call plus pad-slab fill work for those same blanks.
			contentLen := x + 1
			trimmedTimes := contentLen / width
			if contentLen%width == 0 {
				trimmedTimes--
			}
			untrimmedTimes := cols / width
			if cols%width == 0 {
				untrimmedTimes--
			}
			if trimmedTimes != untrimmedTimes {
				from := term.Coordinates{Y: y, X: contentLen}
				to := term.Coordinates{Y: y, X: cols}
				b.Cells.DeleteContext(b.AltBuffer.ctx, from, to)
				cells = b.Cells.RawCells()
				cols = len(cells[y])
			}
			times := cols / width
			remainder := cols % width
			if remainder == 0 {
				times--
			}
			if times > 0 {
				if splits == nil {
					// Cap hint: at most one split per remaining row.
					remain := (at + 1) - y
					splits = make([]cell.RowSplit, 0, remain)
				}
				splits = append(splits, cell.RowSplit{
					Y: y, Width: width, Times: times,
				})
				created += times
			}
			b.log(log.TraceLevel, "wrapped a new line line (%d), times: %d", y, times)
		case x < width && width > cols:
			b.Cells.ExtendRowToWidth(y, width)
		default:
			panic("pack it up boys")
		}
		// Structural edits above (Delete, Extend) may have moved the
		// backing slice; refresh our local view.
		cells = b.Cells.RawCells()
	}
	if len(splits) > 0 {
		// Pad split tails to `width` via a single slab allocation so the
		// subsequent "grow history lines" loop is a no-op for tail rows.
		b.Cells.SplitRowsBatchPadded(splits, width, b.Cells.BlankCell())
		cells = b.Cells.RawCells()
		shift := 0
		for _, s := range splits {
			headY := s.Y + shift
			// Mark head + Times-1 intermediate pieces. The final (tail)
			// piece at headY + Times is the tail of the original line and
			// is NOT marked, so the next resize knows where the logical
			// line actually ends.
			for i := range s.Times {
				row := cells[headY+i]
				row[len(row)-1].Bytes = cell.WrapMarker
			}
			shift += s.Times
		}
		n += created
		at += created
	}
	wraps := created

	// grow history lines that fall short or were wrapped
	for y := range at {
		if b.Cells.Columns(y) < width {
			b.Cells.ExtendRowToWidth(y, width)
		}
	}
	b.log(log.TraceLevel, "wrapped back %d lines", wraps)
	return
}

// Dimensions returns the dimensions of this buffer.
func (b *PrimaryBuffer) Dimensions() (width, height int) {
	width = b.width
	height = b.height
	return
}

// InsertLines inserts blank lines at the given position's line.
func (b *PrimaryBuffer) InsertLines(count int, pos term.Coordinates) {
	var builder strings.Builder
	if pos.X == 0 {
		for range count {
			for range b.width {
				_, _ = builder.WriteRune(b.defaultChar)
			}
			_ = builder.WriteByte('\n')
		}
	} else {
		for range count {
			_ = builder.WriteByte('\n')
			for range b.width {
				_, _ = builder.WriteRune(b.defaultChar)
			}
		}
	}
	b.Cells.Edit(b.ctx, pos, pos, builder.String())
	if b.Cells.Rows() > b.maxHistory {
		to := b.Cells.Rows() - b.maxHistory - 1
		b.log(log.TraceLevel, "rows > max history: %d", to)
		b.DeleteLines(0, to)
	}
}

// DeleteLines deletes the lines from start to end, inclusively.
func (b *PrimaryBuffer) DeleteLines(start, end int) {
	if start > end {
		return
	}
	end = min(end, b.Cells.Rows()-1)

	from := term.Coordinates{Y: start}
	to := term.Coordinates{Y: end}
	b.Cells.DeleteLineContext(b.ctx, from, to)
}

// Reset clears the screen and removes history, effectively
// leaving the content as blank and the cursor position at the top.
func (b *PrimaryBuffer) Reset() {
	b.Cells.ResetPerformanceCapacity(b.height, b.width)
	b.resetLinesTrim(0, b.height, true, b.defaultChar)
	b.SetScrollableRegion(0, 0, true)
	b.SetCursorAtScreen(term.Coordinates{})
}

// Clear clears the screen and moves the current view into history,
// effectively leaving the content as blank and the cursor position at
// the top. It returns the number of rows the content moved up, 0 when
// the view was already blank.
func (b *PrimaryBuffer) Clear() int {
	// find the last row that's not a blank row; cursor cannot
	// be used because some shell implementations move the cursor
	// before sending the clear sequence. Cases:
	//
	//  X  X
	//  ---- start view
	//  XXXX
	//    XX
	//  ---- end view
	//
	//  1122 insert/count
	cells := b.Cells.RawCells()
	for y := b.Rows() - 1; y >= 0; y-- {
		for x := 0; x < b.Columns(y); x++ {
			c := cells[y][x]
			if c.Ch == b.defaultChar {
				continue
			}
			endWindowCoordinates, _ := b.scroll.ScrollToWindowCoordinates(term.Coordinates{Y: y})
			count := endWindowCoordinates.Y + 1
			if count <= 0 {
				return 0
			}
			b.InsertLines(count, term.Coordinates{Y: y, X: b.Columns(y)})
			b.cursor.position = term.Coordinates{}
			b.log(log.TraceLevel, "clear view, insert lines count %d, found non-blank at y:%d", count, y)
			return count
		}
	}
	return 0
}

// ClearHistory clears the scrollback history, leaving the current view as-is.
func (b *PrimaryBuffer) ClearHistory() bool {
	count := b.Cells.Rows() - b.height
	if count <= 0 {
		return false
	}
	b.DeleteLines(0, count-1)
	return true
}

// CursorAtScroll returns the cursor position in relation to the underlying
// content scroll.
func (b *PrimaryBuffer) CursorAtScroll() term.Coordinates {
	pos := b.scroll.WindowToScrollCoordinates(b.cursor.position)
	pos.X = max(0, pos.X)
	pos.Y = max(0, pos.Y)
	return pos
}

// ResetLines erases all the lines from start to end.
// The start to end range is left inclusive, right exclusive.
// As oppposed to AltBuffer's ResetLines, the `end` argument is not capped.
func (b *PrimaryBuffer) ResetLines(start, end int) {
	b.AltBuffer.ResetLinesWith(start, end, b.defaultChar)
}

// Insert inserts a new character at the cursor position, shifting right
// all the cells to the right of the cursor. It does not extend the number columns
// in the buffer, as it should always be capped at exactly b.Width(), set by
// the previous call to Resize.
func (b *PrimaryBuffer) Insert(c rune, width int, charset vteparser.CharsetIndex) {
	pos := b.CursorAtScroll()
	b.AltBuffer.InsertAt(pos, c, width, charset)
}

// Write writes the given character with the given width to the cell
// at the current cursor position.
func (b *PrimaryBuffer) Write(c rune, width int, charset vteparser.CharsetIndex) {
	pos := b.CursorAtScroll()
	overwritten := 0
	if cell := b.AltBuffer.CellAt(pos); cell != nil {
		overwritten = b.AdvanceColumns(int(cell.Width))
	}
	b.AltBuffer.WriteAt(pos, c, width, charset)
	if overwritten != 0 {
		b.consumeCoveredCells(pos, b.AdvanceColumns(width)-overwritten)
	}
}

// WriteRun writes the leading bytes of run (each a printable ASCII
// character occupying one cell) at the cursor, converting the cursor
// position to scroll coordinates once instead of per character.
// Overwriting a Width<=1 cell with a single-width glyph never covers a
// neighbor, so the consumeCoveredCells dance is not needed here; wide
// target cells make WriteRun report 0 so the caller falls back to
// Write. See AltBuffer.WriteRun for the full fallback contract.
func (b *PrimaryBuffer) WriteRun(run []byte, charset vteparser.CharsetIndex) int {
	return b.writeRunAt(b.CursorAtScroll(), run, charset)
}

// WriteGlyphRun writes glyphs at the cursor in one pass and reports how
// many it wrote; 0 demands the per-glyph Write path. The glyphs occupy
// one cell each but cover their full display width, so the cells the
// extra columns now hide are dropped in a single splice instead of
// Write's per-glyph consumeCoveredCells. Every covered cell must be
// narrow: a wide one under the run would leave half a glyph behind,
// which only consumeCoveredCells handles.
func (b *PrimaryBuffer) WriteGlyphRun(glyphs []Glyph, charset vteparser.CharsetIndex) int {
	columns := 0
	for _, g := range glyphs {
		columns += b.AdvanceColumns(int(g.Width))
	}
	pos := b.CursorAtScroll()
	if _, ok := b.runTarget(pos, columns, charset); !ok {
		return 0
	}
	if n := b.writeGlyphRunAt(pos, glyphs, charset); n == 0 {
		return 0
	}
	if columns > len(glyphs) {
		b.Cells.DeleteRowRange(pos.Y, pos.X+len(glyphs), pos.X+columns)
	}
	return len(glyphs)
}

// consumeCoveredCells removes the cells whose columns a glyph
// overwrite at pos now covers. The primary buffer stores one cell per
// glyph and rows must sum to at most the terminal width in visual
// columns; overwriting a cell in place with a wider glyph would
// otherwise leave the covered neighbor cell(s) behind, growing the row
// past the screen and allowing a spurious horizontal scroll.
func (b *PrimaryBuffer) consumeCoveredCells(pos term.Coordinates, delta int) {
	next := term.Coordinates{Y: pos.Y, X: pos.X + 1}
	for delta > 0 {
		cell := b.AltBuffer.CellAt(next)
		if cell == nil {
			return
		}
		delta -= b.AdvanceColumns(int(cell.Width))
		b.Cells.DeleteRowRange(next.Y, next.X, next.X+1)
	}
	// a consumed glyph was wider than the columns claimed: keep the
	// leftover column blank so following glyphs stay in place.
	for ; delta < 0; delta++ {
		b.Cells.InsertContext(b.AltBuffer.ctx, next, b.defaultChar)
	}
}

// PrevCellAtCursor returns the cell immediately left of the cursor in
// scroll coordinates, or nil at the start of a line, so a grapheme
// continuation merges into the cell holding the base of its cluster.
func (b *PrimaryBuffer) PrevCellAtCursor() *term.Cell {
	pos := b.CursorAtScroll()
	if pos.X == 0 {
		return nil
	}
	pos.X--
	return b.AltBuffer.CellAt(pos)
}

// AdvanceColumns returns the glyph's display width (at least one): the
// primary buffer's cursor tracks visual columns, so a wide glyph advances
// past both of the columns it occupies.
func (b *PrimaryBuffer) AdvanceColumns(width int) int {
	return max(1, width)
}

// Delete deletes the the given number of cells, shifting left
// all the cells to the right of the cursor.
func (b *PrimaryBuffer) Delete(count int) {
	pos := b.CursorAtScroll()
	b.AltBuffer.DeleteAt(pos, count)
}

// ResetCells erases all the cells from start to end, on the current
// cursor line. The start to end range is left inclusive, right exclusive.
func (b *PrimaryBuffer) ResetCells(start, end int) {
	pos := b.CursorAtScroll()
	b.resetCellsAt(pos.Y, start, end, b.defaultChar)
}

func (t *PrimaryBuffer) log(level log.Level, line string, params ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "vtescreen.PrimaryBuffer").
		WithField("instance", fmt.Sprintf("%p", t)).
		Logf(level, line, params...)
}
