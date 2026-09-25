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
	"context"
	"maps"
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/text"
)

// DefaultChar is the default char used to fill empty cells.
const DefaultChar = '\x00'

// AltBuffer implements a vte terminal screen buffer by wrapping a cell.Buffer
// and implementing vte screen buffer semantics. This buffer does not offer
// support for scroll-back or 'history', which keeps the implementation
// immensely simpler. Implementations that need scroll-back should
// use PrimaryAltBuffer instead.
type AltBuffer struct {
	Cells cell.Buffer
	// this scroll is used for drawing and converting coordinates,
	// not for scrolling.
	scroll                 component.Scroll
	width                  int
	height                 int
	topScrollableRegion    int // start of scrollable region
	bottomScrollableRegion int // end of scrollable region
	selection              struct {
		mode text.SelectMode
		from term.Coordinates
		to   term.Coordinates
	}

	defaultChar rune
	savedCursor CursorState
	cursor      CursorState
	ctx         context.Context
}

// CursorState holds the state of the cursor.
type CursorState struct {
	position term.Coordinates
	attr     term.Attributes
	hidden   bool // hidden flag on cursor attrs, not cursor itself
	Charsets map[vteparser.CharsetIndex]vteparser.StandardCharset
}

// NewAltBuffer allocates storage for a new AltBuffer and initializes it.
func NewAltBuffer() *AltBuffer {
	ret := new(AltBuffer)
	ret.Init()
	return ret
}

// Init initializes this buffer.
func (b *AltBuffer) Init() {
	b.defaultChar = DefaultChar
	b.width = 1
	b.height = 1
	b.topScrollableRegion = 0
	b.bottomScrollableRegion = b.height
	b.cursor = CursorState{
		Charsets: make(map[vteparser.CharsetIndex]vteparser.StandardCharset),
	}
	b.Cells.InitPerformance(120, 80, b.defaultChar)
	b.resetLinesTrim(0, b.height, true, b.defaultChar)
	b.scroll.InitPerformance(&b.Cells)
	b.scroll.SetTabspaces(1)
	b.scroll.InvertOffset = true
	b.scroll.Resize(1, 1)
	b.ctx = NewContext(context.Background())
}

// restore replaces this buffer's rendered cells and cursor with a saved
// snapshot while keeping the buffer usable for subsequent VTE writes.
func (b *AltBuffer) restore(cells [][]term.Cell, cursor term.Coordinates, width, height int) {
	if width <= 0 {
		width = max(1, maxColumns(cells))
	}
	if height <= 0 {
		height = max(1, len(cells))
	}

	b.width = width
	b.height = height
	b.topScrollableRegion = 0
	b.bottomScrollableRegion = height
	// Mutate in place. *cell.Buffer and *rawCells identities are
	// preserved so any Editor/View pinned by viHandler or
	// component.Scroll remains valid across the restore.
	if len(cells) == 0 {
		// ResetPerformanceCapacity preserves *cell.Buffer / *rawCells
		// identity while clearing all rows back to a single empty
		// row — exactly the invariant needed for an empty snapshot.
		b.Cells.ResetPerformanceCapacity(height, width)
		b.resetLinesTrim(0, height, true, b.defaultChar)
	} else {
		b.Cells.ResetCells(cells)
		b.Cells.ResetCapacity(width)
	}
	// b.scroll already points at &b.Cells; only Resize is needed.
	b.scroll.Resize(width, height)
	b.cursor.position = cursor
	if b.cursor.Charsets == nil {
		b.cursor.Charsets = make(map[vteparser.CharsetIndex]vteparser.StandardCharset)
	}
	if b.savedCursor.Charsets == nil {
		b.savedCursor.Charsets = make(map[vteparser.CharsetIndex]vteparser.StandardCharset)
	}
	b.ctx = NewContext(context.Background())
}

func maxColumns(cells [][]term.Cell) (ret int) {
	for _, row := range cells {
		ret = max(ret, len(row))
	}
	return ret
}

// Resize resizes this AltBuffer and resets the vertical margins.
func (b *AltBuffer) Resize(width, height int) {
	b.width = width
	b.height = height
	b.resetLinesTrim(0, height, true, b.defaultChar)
	b.SetScrollableRegion(0, 0, true)
	b.scroll.Resize(width, height)
}

// Insert inserts a new character at the cursor position, shifting right
// all the cells to the right of the cursor. It does not extend the number columns
// in the buffer, as it should always be capped at exactly b.Width(), set by
// the previous call to Resize.
func (b *AltBuffer) Insert(c rune, width int, charset vteparser.CharsetIndex) {
	b.InsertAt(b.cursor.position, c, width, charset)
}

// InsertAt inserts a new character at the given position, shifting right
// all the cells to the right of the position. It does not extend the number columns
// in the buffer, as it should always be capped at exactly b.Width(), set by
// the previous call to Resize.
func (b *AltBuffer) InsertAt(
	at term.Coordinates, c rune, width int, charset vteparser.CharsetIndex,
) {
	b.Cells.InsertContext(b.ctx, at, b.defaultChar)
	b.WriteAt(at, c, width, charset)
	columns := b.Cells.Columns(at.Y)
	if columns > b.width {
		from := term.Coordinates{Y: at.Y, X: b.width}
		to := term.Coordinates{Y: at.Y, X: columns}
		b.Cells.DeleteContext(b.ctx, from, to)
	}
}

// Write writes the given character with the given width to the cell
// at the current cursor position.
func (b *AltBuffer) Write(c rune, width int, charset vteparser.CharsetIndex) {
	b.WriteAt(b.cursor.position, c, width, charset)
}

// WriteRun writes the leading bytes of run (each a printable ASCII
// character occupying one cell) at the cursor position and reports how
// many were written. Zero means the caller must fall back to
// per-character writes: a non-identity charset mapping, a concealed
// cursor, a short target row or an overwritten wide cell all need
// WriteAt's handling.
func (b *AltBuffer) WriteRun(run []byte, charset vteparser.CharsetIndex) int {
	return b.writeRunAt(b.cursor.position, run, charset)
}

// Glyph is a decoded printable codepoint and its display width, the
// unit WriteGlyphRun writes.
type Glyph struct {
	Ch    rune
	Width uint8
}

// WriteGlyphRun writes glyphs at the cursor position, one cell each,
// and reports how many it wrote. Zero means the caller must fall back
// to per-glyph Write, under the same conditions as WriteRun.
func (b *AltBuffer) WriteGlyphRun(glyphs []Glyph, charset vteparser.CharsetIndex) int {
	return b.writeGlyphRunAt(b.cursor.position, glyphs, charset)
}

func (b *AltBuffer) writeGlyphRunAt(
	pos term.Coordinates, glyphs []Glyph, charset vteparser.CharsetIndex,
) int {
	_, ok := b.runTarget(pos, len(glyphs), charset)
	if !ok {
		return 0
	}
	target := b.Cells.MutableRow(pos.Y, pos.X+len(glyphs))[pos.X : pos.X+len(glyphs)]
	for i, g := range glyphs {
		target[i] = term.NewCell(g.Ch, g.Width, b.cursor.attr)
	}
	return len(glyphs)
}

func (b *AltBuffer) writeRunAt(
	pos term.Coordinates, run []byte, charset vteparser.CharsetIndex,
) int {
	// Deliberately not sharing runTarget: this runs once per printable
	// run and the extra call costs ~6% on single-character runs.
	if b.cursor.hidden {
		return 0
	}
	if cs, ok := b.cursor.Charsets[charset]; ok && cs != vteparser.StandardCharsetASCII {
		return 0
	}
	if pos.Y < 0 || pos.Y >= b.Cells.Rows() || pos.X < 0 {
		return 0
	}
	row := b.Cells.Row(pos.Y)
	end := pos.X + len(run)
	if end > len(row) {
		return 0
	}
	target := row[pos.X:end]
	for i := range target {
		if target[i].Width > 1 {
			return 0
		}
	}
	target = b.Cells.MutableRow(pos.Y, end)[pos.X:end]
	for i, c := range run {
		target[i] = term.NewCell(rune(c), 1, b.cursor.attr)
	}
	return len(run)
}

// runTarget returns the n cells a bulk write starting at pos would
// overwrite. It reports false when the write must fall back to the
// per-character path.
func (b *AltBuffer) runTarget(
	pos term.Coordinates, n int, charset vteparser.CharsetIndex,
) ([]term.Cell, bool) {
	if b.cursor.hidden {
		return nil, false
	}
	if cs, ok := b.cursor.Charsets[charset]; ok && cs != vteparser.StandardCharsetASCII {
		return nil, false
	}
	if pos.Y < 0 || pos.Y >= b.Cells.Rows() || pos.X < 0 {
		return nil, false
	}
	row := b.Cells.Row(pos.Y)
	end := pos.X + n
	if end > len(row) {
		return nil, false
	}
	target := row[pos.X:end]
	for i := range target {
		if target[i].Width > 1 {
			return nil, false
		}
	}
	return target, true
}

// WriteAt writes the given character with the given width to the cell
// at the current cursor position.
func (b *AltBuffer) WriteAt(
	at term.Coordinates, c rune, width int, charset vteparser.CharsetIndex,
) {
	if charset, ok := b.cursor.Charsets[charset]; ok {
		c = charset.Map(c)
	}
	if b.cursor.hidden {
		c = b.defaultChar
	}
	cell := b.CellAt(at)
	if cell == nil {
		b.InsertAt(at, c, width, charset)
		return
	}
	*cell = term.NewCell(c, uint8(width), b.cursor.attr)
}

// ResetCells erases all the cells from start to end, on the current
// cursor line. The start to end range is left inclusive, right exclusive.
func (b *AltBuffer) ResetCells(start, end int) {
	b.resetCellsAt(b.cursor.position.Y, start, end, b.defaultChar)
}

// ResetLines erases all the lines from start to end.
// The start to end range is left inclusive, right exclusive.
// The `end` argument is capped to height.
func (b *AltBuffer) ResetLines(start, end int) {
	end = int(math.Min(float64(b.height), float64(end)))
	b.ResetLinesWith(start, end, b.defaultChar)
}

// ResetLinesWith erases all the lines from start to end,
// using with and the default attributes as the new content.
// The start to end range is left inclusive, right exclusive.
// The `end` argument is not capped, so care must be taken
// when using this method.
func (b *AltBuffer) ResetLinesWith(start, end int, with rune) {
	b.resetLinesTrim(start, end, false, with)
}

// Delete deletes the the given number of cells, shifting left
// all the cells to the right of the cursor.
func (b *AltBuffer) Delete(count int) {
	b.DeleteAt(b.cursor.position, count)
}

// DeleteAt deletes the the given number of cells, shifting left
// all the cells to the right of given position.
func (b *AltBuffer) DeleteAt(pos term.Coordinates, count int) {
	if count <= 0 || pos.Y < 0 || pos.Y >= b.Cells.Rows() || pos.X < 0 {
		return
	}
	columns := b.Cells.Columns(pos.Y)
	if pos.X >= columns {
		return
	}
	count = min(count, columns-pos.X)

	row := b.Cells.MutableRow(pos.Y, columns)
	copy(row[pos.X:], row[pos.X+count:])
	blank := term.NewCell(b.defaultChar, 1, term.Attributes{Bg: b.cursor.attr.Bg})
	b.Cells.ResetRowRange(pos.Y, columns-count, columns, blank)
}

// SetCursorAtScreen updates the cursor position in the screen, clamped
// to it.
func (b *AltBuffer) SetCursorAtScreen(c term.Coordinates) {
	b.cursor.position.X = max(0, min(c.X, b.width-1))
	b.cursor.position.Y = max(0, min(c.Y, b.height-1))
}

// ScrollUp scrolls up the scrollable region set by SetScrollableRegion by count of lines
func (b *AltBuffer) ScrollUp(start, end, count int) {
	if count <= 0 || start < 0 || end > b.Cells.Rows() || start >= end {
		return
	}
	if end-start <= count {
		b.ResetLinesWith(start, end, b.defaultChar)
		return
	}

	b.Cells.RotateRows(start, end, count)

	b.ResetLinesWith(end-count, end, b.defaultChar)
}

// ScrollDown scrolls down the scrollable region set by SetScrollableRegion by count of lines
func (b *AltBuffer) ScrollDown(start, end, count int) {
	if count <= 0 || start < 0 || end > b.Cells.Rows() || start >= end {
		return
	}
	if end-start <= count {
		b.ResetLinesWith(start, end, b.defaultChar)
		return
	}

	b.Cells.RotateRows(start, end, -count)

	b.ResetLinesWith(start, start+count, b.defaultChar)
}

// SetScrollableRegion sets the start and end of the scrollable area.
func (b *AltBuffer) SetScrollableRegion(top int, bottom int, end bool) {
	b.topScrollableRegion = int(math.Min(float64(top), float64(b.height)))
	if end {
		b.bottomScrollableRegion = b.height
	} else {
		b.bottomScrollableRegion = int(math.Min(float64(bottom), float64(b.height)))
	}
}

// BottomScrollableRegion returns the bottom margin, set by SetVerticalScrollableRegions.
func (b *AltBuffer) BottomScrollableRegion() int {
	return b.bottomScrollableRegion
}

// TopScrollableRegion returns the bottom margin, set by SetVerticalScrollableRegions.
func (b *AltBuffer) TopScrollableRegion() int {
	return b.topScrollableRegion
}

// Height returns the height set by Resize.
func (b *AltBuffer) Height() int {
	return b.height
}

// Width returns the height set by Resize.
func (b *AltBuffer) Width() int {
	return b.width
}

// Columns returns the columns of the given line.
func (b *AltBuffer) Columns(line int) int {
	if line < 0 || line >= b.Cells.Rows() {
		return 0
	}
	return b.Cells.Columns(line)
}

// CellAt returns the cell at the given position or nil
// if there's no cell at the given position.
func (b *AltBuffer) CellAt(pos term.Coordinates) *term.Cell {
	// Do not use Height, or intended number of screen lines here:
	// there might be a significant latency betwen resizing and upserting cells.
	// This effectively prevents Insert(pos)=ok then CellAt(pos)=nil
	if pos.Y < 0 || pos.Y >= b.Cells.Rows() {
		return nil
	}
	row := b.Cells.Row(pos.Y)
	if pos.X < 0 || pos.X >= len(row) {
		return nil
	}
	return &b.Cells.MutableRow(pos.Y, pos.X+1)[pos.X]
}

// RowCells returns the cells of content row y, aliasing the buffer's
// storage, or nil when the row does not exist.
func (b *AltBuffer) RowCells(y int) []term.Cell {
	if y < 0 || y >= b.Cells.Rows() {
		return nil
	}
	return b.Cells.Row(y)
}

// PrevCellAtCursor returns the cell immediately left of the cursor, or nil
// at the start of a line, so a grapheme continuation can be merged into
// the cell that holds the base of its cluster.
func (b *AltBuffer) PrevCellAtCursor() *term.Cell {
	pos := b.cursor.position
	if pos.X == 0 {
		return nil
	}
	pos.X--
	return b.CellAt(pos)
}

// AdvanceColumns returns 1: the alternate buffer stores one cell per
// grapheme and its cursor is a cell index, so a wide glyph still advances
// the cursor by a single cell and the renderer expands its width.
func (b *AltBuffer) AdvanceColumns(int) int {
	return 1
}

// SaveCursor saves the current cursor state to be restored
// later by RestoreCursor.
func (b *AltBuffer) SaveCursor() {
	b.SetSavedCursor(b.CloneCursor())
}

// CloneCursor clones the current cursor state and returns it.
func (b *AltBuffer) CloneCursor() (ret CursorState) {
	ret.attr = b.cursor.attr
	ret.position = b.cursor.position
	ret.Charsets = make(map[vteparser.CharsetIndex]vteparser.StandardCharset)
	maps.Copy(ret.Charsets, b.cursor.Charsets)
	return ret
}

// SetCursor sets the current cursor state to c.
func (b *AltBuffer) SetCursor(c CursorState) {
	b.cursor = c
}

// SetSavedCursor sets the saved cursor, to be restored
// later by RestoreCursor.
func (b *AltBuffer) SetSavedCursor(c CursorState) {
	b.savedCursor = c
}

// RestoreCursor sets the cursor to the previously stored
// cursor via SaveCursor or SetSavedCursor.
func (b *AltBuffer) RestoreCursor() {
	b.SetCursor(b.savedCursor)
}

// Cursor returns the current CursorState.
func (b *AltBuffer) Cursor() CursorState {
	return b.cursor
}

// CursorAtScreen returns the current cursor position in relation
// to the screen coordinates. This is equivalent to CursorAtScroll.
func (b *AltBuffer) CursorAtScreen() term.Coordinates {
	return b.cursor.position
}

// CursorAtScroll returns the current cursor position in relation
// to the scroll contents. This is equivalent to CursorAtScreen.
func (b *AltBuffer) CursorAtScroll() term.Coordinates {
	return b.cursor.position
}

// SetHiddenCursor marks as hidden the current cursor attributes.
func (b *AltBuffer) SetHiddenCursor(hidden bool) {
	b.cursor.hidden = hidden
}

// CursorAttributes returns the current cursor attributes.
func (b *AltBuffer) CursorAttributes() term.Attributes {
	return b.cursor.attr
}

// SetCursorAttributes sets the default cursor attributes.
func (b *AltBuffer) SetCursorAttributes(attr term.Attributes) {
	b.cursor.attr = attr
}

// ConfigureCharset configures the given charset index to use charset.
func (b *AltBuffer) ConfigureCharset(
	index vteparser.CharsetIndex, charset vteparser.StandardCharset,
) {
	b.cursor.Charsets[index] = charset
}

// MaxColumns returns the max columns of the underlying cell.Buffer.
func (b *AltBuffer) MaxColumns() int {
	return b.Cells.MaxColumns()
}

// Rows returns the number of rows in this AltBuffer.
func (b *AltBuffer) Rows() int {
	return b.Cells.Rows()
}

// Draw renders this buffer onto w.
func (b *AltBuffer) Draw(w term.Writer) {
	b.scroll.Draw(w)
}

// SetDefaultAttributes sets the default attributes to be used
// in the next call to Draw.
func (b *AltBuffer) SetDefaultAttributes(attr term.Attributes) {
	b.scroll.Attributes = attr
}

// SelectWordAt selects the word at the given screen position.
func (b *AltBuffer) SelectWordAt(pos term.Coordinates) {
	pos = b.scroll.WindowToScrollCoordinates(pos)
	if pos.Y < 0 {
		return
	}
	b.selection.from, b.selection.to, _ = b.scroll.WordAt(pos)
	b.selection.mode = text.StandardSelection
}

// Unselect clears the current selection if there's any.
func (b *AltBuffer) Unselect() {
	b.selection.mode = text.NoSelection
}

// Select anchors the given screen position as the start
// and end of a text selection.
func (b *AltBuffer) Select(pos term.Coordinates) {
	pos = b.scroll.WindowToScrollCoordinates(pos)
	if pos.Y < 0 {
		return
	}
	b.selection.from = pos
	pos.X++
	b.selection.to = pos
	b.selection.mode = text.StandardSelection
}

// SelectEnd anchors the current screen position as the end of a text selection.
func (b *AltBuffer) SelectEnd(pos term.Coordinates) {
	if b.selection.mode == text.NoSelection {
		return
	}
	pos = b.scroll.WindowToScrollCoordinates(pos)
	if pos.Y < 0 {
		return
	}
	// buffer selection has right exclusive semantics
	pos.X++
	b.selection.to = pos
}

// SelectLine anchors the current screen position as the start and end line of
// the text selection.
func (b *AltBuffer) SelectLine(pos term.Coordinates) {
	pos = b.scroll.WindowToScrollCoordinates(pos)
	if pos.Y < 0 {
		return
	}
	b.selection.from = pos
	pos.X++
	b.selection.to = pos
	b.selection.mode = text.LineSelection
}

// Selection returns the current selection or false if no
// text is currently selected.
func (b *AltBuffer) Selection() (cells [][]term.Cell, ok bool) {
	mode, from, to, _ := b.SelectionCoordinatesAtScroll()
	switch mode {
	case text.StandardSelection:
		cells, _, ok = b.Cells.Select(from, to)
	case text.LineSelection:
		cells, _, ok = b.Cells.SelectLine(from, to)
		if ok {
			// Append an empty row so CellsToString produces a trailing
			// newline, matching line-selection copy/paste semantics.
			cells = append(cells, []term.Cell{})
		}
	default:
	}

	return
}

// SetDefaultChar sets the default character to use for filling cells
// up to width and heighgt. This should be used for testing only.
func (b *AltBuffer) SetDefaultChar(ch rune) {
	b.defaultChar = ch
	b.Cells.InitPerformance(120, 80, b.defaultChar)
	b.resetLinesTrim(0, b.height, true, b.defaultChar)
}

// SelectionCoordinatesAtScroll returns the content/scroll coordinates of the selected text.
// The returned coordinates are left inclusive, right exclusive.
func (b *AltBuffer) SelectionCoordinatesAtScroll() (
	mode text.SelectMode, from, to term.Coordinates, ok bool,
) {
	from, to = term.CoordinatesSort(b.selection.from, b.selection.to)
	mode = b.selection.mode
	ok = b.selection.mode != text.NoSelection
	return
}

// SelectionCoordinatesAtScreen returns the screen coordinates of the selected text.
// The returned coordinates are left inclusive, right exclusive.
func (b *AltBuffer) SelectionCoordinatesAtScreen() (
	mode text.SelectMode, from, to term.Coordinates, ok bool,
) {
	mode, from, to, ok = b.SelectionCoordinatesAtScroll()
	from, _ = b.scroll.ScrollToWindowCoordinates(from)
	to, _ = b.scroll.ScrollToWindowCoordinates(to)
	return
}

// Scroll returns this buffer's underlying component.Scroll.
func (b *AltBuffer) Scroll() *component.Scroll {
	return &b.scroll
}

// resetCellsAt erases all the cells from start to end, at the given line,
// The start to end range is left inclusive, right exclusive.
func (b *AltBuffer) resetCellsAt(y int, start, end int, with rune) {
	if y < 0 || start < 0 || start >= end {
		return
	}
	baseBlank := term.NewCell(with, 1, term.Attributes{})
	targetWidth := max(end, b.width)
	columns := b.Cells.Columns(y)
	if y >= b.Cells.Rows() {
		firstNew := b.Cells.Rows()
		b.Cells.AppendBlankRows(y-firstNew+1, targetWidth)
		for newY := firstNew; newY <= y; newY++ {
			b.Cells.ResetRowRange(newY, 0, targetWidth, baseBlank)
		}
	} else if end > columns {
		_, _ = b.Cells.ExtendRowToWidth(y, targetWidth)
		b.Cells.ResetRowRange(y, columns, targetWidth, baseBlank)
	}
	blank := term.NewCell(with, 1, term.Attributes{Bg: b.cursor.attr.Bg})
	b.Cells.ResetRowRange(y, start, end, blank)
}

func (b *AltBuffer) resetLinesTrim(start, end int, trim bool, with rune) {
	if start < 0 || end <= 0 || start >= end {
		return
	}
	// ensure there are enough rows
	if end > b.Cells.Rows() {
		b.Cells.InsertContext(b.ctx, term.Coordinates{Y: end - 1}, with)
	} else if end < b.Cells.Rows() && trim {
		from := term.Coordinates{Y: end - 1, X: b.Cells.Columns(end - 1)}
		b.Cells.TruncateFromContext(b.ctx, from)
	}

	b.trimColumns(start, end, with, true)
}

func (b *AltBuffer) trimColumns(start, end int, with rune, reset bool) {
	for y := start; y < end; y++ {
		columns := b.Cells.Columns(y)
		if columns > b.width {
			from := term.Coordinates{Y: y, X: b.width}
			to := term.Coordinates{Y: y, X: columns}
			b.Cells.DeleteContext(b.ctx, from, to)
		}
		if reset {
			b.resetCellsAt(y, 0, b.width, with)
		}
	}
}
