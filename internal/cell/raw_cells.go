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
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
)

const (
	defColumnCap int = 64
	defRowCap    int = 64
	// readFromWithView carves exact-size rows out of shared backing
	// slabs. Slabs grow geometrically from init to max so small buffers
	// do not retain a large mostly-unused slab (rows pin their slab for
	// the lifetime of the buffer), while large loads settle on ~384KiB
	// slabs (at 24 bytes per cell).
	readSlabInitCells int = 256
	readSlabMaxCells  int = 16 * 1024
	// maxFreeRows bounds the storage recycled from trimRowsFromStart for
	// reuse by appendBlankRows, so unbalanced trim/append usage cannot
	// retain unbounded memory.
	maxFreeRows int = 4096
)

// rawCells is a matrix of term.Cell.
type rawCells struct {
	columnCap   int
	rowCap      int
	cells       [][]term.Cell
	rowMeta     []rawRowMeta
	ringHead    int
	free        [][]term.Cell
	fillInChar  rune
	performance bool
	blank       term.Cell
	zwj         bool
	zwjPos      term.Coordinates
}

type rawRowMeta struct {
	occupied int
	blank    term.Cell
}

// init initializes this rawCells with the given tabspaces config and resets its contents.
func (c *rawCells) init() {
	c.setFillInChar(' ')
	c.reset()
}

func (c *rawCells) initWithCap(rowCap, columnCap int, fillInChar rune) {
	c.performance = true
	c.setFillInChar(fillInChar)
	c.resetWithCap(rowCap, columnCap)
}

func (c *rawCells) setFillInChar(ch rune) {
	s := string(ch)
	c.fillInChar = ch
	c.blank = term.Cell{
		Ch:    ch,
		Width: uint8(graphemecluster.StringWidth(s)),
		Bytes: uint8(len(s)),
	}
}

func (c *rawCells) reset() {
	c.resetWithCap(defRowCap, defColumnCap)
}

func (c *rawCells) resetWithCap(rowCap, columnCap int) {
	if columnCap <= 0 {
		columnCap = defColumnCap
	}
	if rowCap <= 0 {
		rowCap = defRowCap
	}
	c.columnCap = columnCap
	c.rowCap = rowCap
	c.cells = make([][]term.Cell, 1, c.rowCap)
	c.rowMeta = make([]rawRowMeta, 1, c.rowCap)
	c.ringHead = 0
	c.cells[0] = makeNewRow(0, c.columnCap)
	c.free = nil
	c.zwj = false
	c.zwjPos = term.Coordinates{}
}

func (c *rawCells) adoptCells(cells [][]term.Cell) {
	if len(cells) == 0 {
		panic("rawCells.adoptCells: cells must contain at least one row")
	}
	c.cells = cells
	c.rowMeta = make([]rawRowMeta, len(cells))
	for i, row := range cells {
		c.rowMeta[i].occupied = len(row)
	}
	c.ringHead = 0
	c.free = nil
	c.zwj = false
	c.zwjPos = term.Coordinates{}
}

func assertValidCoords(pos term.Coordinates) {
	if pos.X < 0 || pos.Y < 0 {
		panic(fmt.Sprintf("invalid coordinates: %+v", pos))
	}
}

func makeNewRow(length, capacity int) (row []term.Cell) {
	capacity = int(math.Max(float64(length), float64(capacity)))
	row = make([]term.Cell, length, capacity)
	return
}

func (c *rawCells) physicalRow(y int) int {
	if len(c.cells) == 0 {
		return 0
	}
	return (c.ringHead + y) % len(c.cells)
}

func (c *rawCells) ensureRowMeta() {
	if len(c.rowMeta) == len(c.cells) {
		return
	}
	c.normalizeRows()
	c.rowMeta = make([]rawRowMeta, len(c.cells))
	for i, row := range c.cells {
		c.rowMeta[i] = rawRowMeta{occupied: len(row), blank: c.blank}
	}
}

func (c *rawCells) row(y int) []term.Cell {
	return c.cells[c.physicalRow(y)]
}

func (c *rawCells) normalizeRows() {
	if c.ringHead == 0 || len(c.cells) <= 1 {
		return
	}
	slices.Reverse(c.cells[:c.ringHead])
	slices.Reverse(c.cells[c.ringHead:])
	slices.Reverse(c.cells)
	if len(c.rowMeta) == len(c.cells) {
		slices.Reverse(c.rowMeta[:c.ringHead])
		slices.Reverse(c.rowMeta[c.ringHead:])
		slices.Reverse(c.rowMeta)
	}
	c.ringHead = 0
}

func (c *rawCells) linearize() {
	c.normalizeRows()
	c.rowMeta = nil
}

func (c *rawCells) insertNewRow(pos term.Coordinates) {
	assertValidCoords(pos)
	sourceRow := c.cells[pos.Y]
	targetY := pos.Y + 1

	// make enough space for one more row
	c.cells = append(c.cells, nil)
	copy(c.cells[targetY:], c.cells[pos.Y:])

	// if not last position, copy the rest of cells to the next row
	if pos.X < len(sourceRow) {
		c.cells[pos.Y] = c.cells[pos.Y][:pos.X]
		length := len(sourceRow[pos.X:])
		c.cells[targetY] = makeNewRow(length, c.columnCap)
		copy(c.cells[targetY], sourceRow[pos.X:])
	} else {
		c.cells[targetY] = makeNewRow(0, c.columnCap)
	}
}

func (c *rawCells) splitRowsBatch(splits []RowSplit, padToWidth int, fill term.Cell) (added int) {
	if len(splits) == 0 {
		return 0
	}
	for _, s := range splits {
		if s.Times <= 0 || s.Width <= 0 {
			continue
		}
		added += s.Times
	}
	if added == 0 {
		return 0
	}

	oldLen := len(c.cells)
	newLen := oldLen + added

	var dst [][]term.Cell
	if cap(c.cells) >= newLen {
		dst = c.cells[:newLen]
	} else {
		dst = make([][]term.Cell, newLen, newLen+newLen/2)
	}

	// If padding tails, pre-allocate one slab large enough to hold every
	// tail that needs padding. We only fill the cells that won't be copied
	// over from the original tail.
	var padSlab []term.Cell
	var padOff int
	if padToWidth > 0 {
		var padTails int
		for _, s := range splits {
			if s.Times <= 0 || s.Width <= 0 {
				continue
			}
			orig := c.cells[s.Y]
			tailLen := len(orig) - s.Times*s.Width
			if tailLen < padToWidth {
				padTails++
			}
		}
		if padTails > 0 {
			padSlab = make([]term.Cell, padTails*padToWidth)
		}
	}

	// Walk backwards over the original rows. Maintain a `shift` that tracks
	// how many positions everything below the current read pointer has been
	// pushed down by accumulated splits (from later, higher-index, original
	// rows). Each split inserts s.Times rows into the OUTPUT, after its head.
	//
	// We process splits from last to first. Between splits, we copy the
	// untouched tail rows down by `shift` positions.
	splitIdx := len(splits) - 1
	read := oldLen
	write := newLen
	for read > 0 {
		// Find next applicable split with Y < read.
		for splitIdx >= 0 {
			s := splits[splitIdx]
			if s.Times <= 0 || s.Width <= 0 {
				splitIdx--
				continue
			}
			if s.Y < read {
				break
			}
			splitIdx--
		}
		var nextSplitY int
		if splitIdx >= 0 {
			nextSplitY = splits[splitIdx].Y
		} else {
			nextSplitY = -1
		}

		// Copy untouched rows in (nextSplitY, read) down to dst.
		untouched := read - (nextSplitY + 1)
		if untouched > 0 {
			copy(dst[write-untouched:write], c.cells[nextSplitY+1:read])
			write -= untouched
		}

		if splitIdx < 0 {
			break
		}
		// Apply the split at splits[splitIdx].
		s := splits[splitIdx]
		orig := c.cells[s.Y]
		origLen := len(orig)
		// Emit the trailing pieces (j = s.Times .. 1), then the head at s.Y.
		for j := s.Times; j >= 1; j-- {
			start := j * s.Width
			var end int
			if j == s.Times {
				end = origLen
			} else {
				end = start + s.Width
			}
			write--
			if j == s.Times && padToWidth > 0 && end-start < padToWidth && padSlab != nil {
				// Materialize tail into padSlab at full padToWidth. Only
				// the pad region (past the original tail length) needs
				// fill; the original tail cells are copied in.
				slot := padSlab[padOff : padOff+padToWidth : padOff+padToWidth]
				tailLen := end - start
				copy(slot[:tailLen], orig[start:end])
				for i := tailLen; i < padToWidth; i++ {
					slot[i] = fill
				}
				dst[write] = slot
				padOff += padToWidth
			} else {
				dst[write] = orig[start:end:end]
			}
		}
		write--
		dst[write] = orig[:s.Width:s.Width]
		read = s.Y
		splitIdx--
	}

	c.cells = dst
	return added
}

func (c *rawCells) doInsertAt(pos term.Coordinates, r []rune, width, byteCount uint8) {
	// make sure we have enough capacity
	c.cells[pos.Y] = append(c.cells[pos.Y], term.Cell{})
	copy(c.cells[pos.Y][pos.X+1:], c.cells[pos.Y][pos.X:])
	var combining []rune
	if len(r) > 1 {
		combining = r[1:]
	}
	cell := term.Cell{Ch: r[0], Width: width, Bytes: byteCount}
	cell.SetCombining(combining)
	c.cells[pos.Y][pos.X] = cell
}

func (c *rawCells) insertAt(pos term.Coordinates, r []rune, width uint8, byteCount uint8) (
	next term.Coordinates,
) {
	switch r[0] {
	// zero-width joiner, at position 0, indicates that previous cell is not complete
	// This mechanism is needed because input event processes one rune at a time
	// This assumes that a zwj and the runes of the grapheme cluster it belongs to
	// are inserted sequentially
	case '\u200d':
		c.zwj = true
		c.doInsertAt(pos, r, width, byteCount)
		next = term.Coordinates{X: pos.X + 1, Y: pos.Y}
		c.zwjPos = next
		return
	case '\n':
		c.insertNewRow(pos)
		next = term.Coordinates{X: 0, Y: pos.Y + 1}
	default:
		at := pos
		c.doInsertAt(at, r, width, byteCount)
		next = term.Coordinates{X: at.X + 1, Y: at.Y}
	}

	if c.zwj {
		if c.zwjPos == pos {
			str := c.String()
			c.reset()
			_, _ = c.readFromWithView(strings.NewReader(str), c)
			next = pos
			next.X-- // cells were combined and \u200d removed
		}
		c.zwj = false
		c.zwjPos = term.Coordinates{}
	}

	return
}

// specialized, most common case for perf improvement
func (c *rawCells) doInsertAtPerf(pos term.Coordinates, r rune, width, byteCount uint8) {
	// make sure we have enough capacity
	c.cells[pos.Y] = append(c.cells[pos.Y], term.Cell{})
	copy(c.cells[pos.Y][pos.X+1:], c.cells[pos.Y][pos.X:])
	cell := term.Cell{Ch: r, Width: width, Bytes: byteCount}
	c.cells[pos.Y][pos.X] = cell
}

// specialized, most common case for perf improvements
func (c *rawCells) insertAtPerf(pos term.Coordinates, r rune, width uint8, byteCount uint8) (
	next term.Coordinates,
) {
	switch r {
	case '\u200d':
		next = term.Coordinates{X: pos.X + 1, Y: pos.Y}
		c.zwj = true
		c.doInsertAtPerf(pos, r, width, byteCount)
		c.zwjPos = next
		return
	case '\n':
		c.insertNewRow(pos)
		next = term.Coordinates{X: 0, Y: pos.Y + 1}
	default:
		at := pos
		c.doInsertAtPerf(at, r, width, byteCount)
		next = term.Coordinates{X: at.X + 1, Y: at.Y}
	}

	if c.zwj {
		if c.zwjPos == pos {
			str := c.String()
			c.reset()
			_, _ = c.readFromWithView(strings.NewReader(str), c)
			next = term.Coordinates{X: pos.X, Y: pos.Y}
		}
		c.zwj = false
		c.zwjPos = term.Coordinates{}
	}

	return
}

func (c *rawCells) fillInRows(y int) (n int) {
	for y >= len(c.cells) {
		n++
		row := makeNewRow(0, c.columnCap)
		c.cells = append(c.cells, row)
	}
	return
}

func (c *rawCells) fillInColumns(pos term.Coordinates) (n int) {
	for pos.X > len(c.cells[pos.Y]) {
		c.cells[pos.Y] = append(c.cells[pos.Y], term.Cell{Ch: c.fillInChar})
		n++
	}
	return
}

func (c *rawCells) extendRowToWidth(y, width int) (added int) {
	if y < 0 || y >= len(c.cells) {
		return 0
	}
	c.ensureRowMeta()
	physical := c.physicalRow(y)
	row := c.cells[physical]
	cur := len(row)
	if cur >= width {
		return 0
	}
	need := width - cur
	if cap(row) < width {
		grown := make([]term.Cell, width)
		copy(grown, row)
		row = grown
	} else {
		row = row[:width]
	}
	fill := c.blank
	for i := cur; i < width; i++ {
		row[i] = fill
	}
	c.cells[physical] = row
	c.rowMeta[physical].occupied = max(c.rowMeta[physical].occupied, width)
	return need
}

// trimRowsFromEnd drops up to count rows from the end of c.cells, keeping
// at least one row (matches the invariant maintained elsewhere). It is
// allocation-free.
func (c *rawCells) trimRowsFromEnd(count int) (removed int) {
	c.normalizeRows()
	if count <= 0 {
		return 0
	}
	n := len(c.cells)
	if n <= 1 {
		return 0
	}
	removed = count
	removed = min(removed, n-1)
	newLen := n - removed
	// Drop the trailing row pointers so the underlying []term.Cell allocations
	// are not pinned by the unused tail of the backing array.
	clear(c.cells[newLen:])
	c.cells = c.cells[:newLen]
	if len(c.rowMeta) >= n {
		clear(c.rowMeta[newLen:])
		c.rowMeta = c.rowMeta[:newLen]
	}
	return removed
}

func (c *rawCells) trimRowsFromStart(count int) (removed int) {
	c.normalizeRows()
	if count <= 0 {
		return 0
	}
	n := len(c.cells)
	if n <= 1 {
		return 0
	}
	removed = min(count, n-1)
	if keep := min(removed, maxFreeRows-len(c.free)); keep > 0 {
		c.free = append(c.free, c.cells[:keep]...)
	}
	copy(c.cells, c.cells[removed:])
	newLen := n - removed
	// Drop the trailing row pointers so the underlying []term.Cell
	// allocations are not pinned by the unused tail of the backing array.
	clear(c.cells[newLen:])
	c.cells = c.cells[:newLen]
	if len(c.rowMeta) >= n {
		copy(c.rowMeta, c.rowMeta[removed:])
		clear(c.rowMeta[newLen:])
		c.rowMeta = c.rowMeta[:newLen]
	}
	return removed
}

func (c *rawCells) appendBlankRows(count, width int) {
	c.normalizeRows()
	if count <= 0 || width < 0 {
		return
	}
	c.ensureRowMeta()
	fill := c.blank
	for range count {
		row := c.takeFreeRow(width)
		if len(row) > 0 {
			row[0] = fill
			for filled := 1; filled < len(row); filled *= 2 {
				copy(row[filled:], row[:filled])
			}
		}
		c.cells = append(c.cells, row)
		c.rowMeta = append(c.rowMeta, rawRowMeta{blank: fill})
	}
}

func (c *rawCells) appendBlankRowsBounded(count, width, limit int) {
	if count <= 0 || width < 0 || limit <= 0 {
		return
	}
	c.ensureRowMeta()
	if excess := len(c.cells) - limit; excess > 0 {
		c.trimRowsFromStart(excess)
	}
	for range count {
		if len(c.cells) < limit {
			row := makeNewRow(width, c.columnCap)
			fillCells(row, c.blank)
			c.normalizeRows()
			c.cells = append(c.cells, row)
			c.rowMeta = append(c.rowMeta, rawRowMeta{blank: c.blank})
			continue
		}

		physical := c.ringHead
		row := c.cells[physical]
		meta := c.rowMeta[physical]
		switch {
		case cap(row) < width:
			row = makeNewRow(width, c.columnCap)
			fillCells(row, c.blank)
		case len(row) != width || meta.blank != c.blank:
			row = row[:width]
			fillCells(row, c.blank)
		default:
			fillCells(row[:min(meta.occupied, width)], c.blank)
		}
		c.cells[physical] = row
		c.rowMeta[physical] = rawRowMeta{blank: c.blank}
		c.ringHead = (c.ringHead + 1) % len(c.cells)
	}
}

func fillCells(row []term.Cell, fill term.Cell) {
	if len(row) == 0 {
		return
	}
	row[0] = fill
	for filled := 1; filled < len(row); filled *= 2 {
		copy(row[filled:], row[:filled])
	}
}

func (c *rawCells) resetRowRange(y, start, end int, fill term.Cell) {
	if y < 0 || y >= c.Rows() || start < 0 || start >= end {
		return
	}
	c.ensureRowMeta()
	physical := c.physicalRow(y)
	row := c.cells[physical]
	end = min(end, len(row))
	if start >= end {
		return
	}
	meta := &c.rowMeta[physical]
	if start == 0 && end == len(row) {
		if meta.blank == fill {
			fillCells(row[:min(meta.occupied, len(row))], fill)
			*meta = rawRowMeta{blank: fill}
			return
		}
		fillCells(row, fill)
		*meta = rawRowMeta{blank: fill}
	} else {
		fillCells(row[start:end], fill)
		meta.occupied = max(meta.occupied, end)
	}
}

func (c *rawCells) rotateRows(start, end, count int) {
	c.ensureRowMeta()
	start = max(0, start)
	end = min(end, len(c.cells))
	length := end - start
	if length <= 1 {
		return
	}
	count %= length
	if count < 0 {
		count += length
	}
	if count == 0 {
		return
	}
	if start == 0 && end == len(c.cells) {
		c.ringHead = c.physicalRow(count)
		return
	}
	c.normalizeRows()
	if count == 1 {
		row := c.cells[start]
		copy(c.cells[start:end-1], c.cells[start+1:end])
		c.cells[end-1] = row
		meta := c.rowMeta[start]
		copy(c.rowMeta[start:end-1], c.rowMeta[start+1:end])
		c.rowMeta[end-1] = meta
		return
	}
	if count == length-1 {
		row := c.cells[end-1]
		copy(c.cells[start+1:end], c.cells[start:end-1])
		c.cells[start] = row
		meta := c.rowMeta[end-1]
		copy(c.rowMeta[start+1:end], c.rowMeta[start:end-1])
		c.rowMeta[start] = meta
		return
	}
	rotate := func(rows [][]term.Cell) {
		slices.Reverse(rows[:count])
		slices.Reverse(rows[count:])
		slices.Reverse(rows)
	}
	rotate(c.cells[start:end])
	meta := c.rowMeta[start:end]
	slices.Reverse(meta[:count])
	slices.Reverse(meta[count:])
	slices.Reverse(meta)
}

func (c *rawCells) takeFreeRow(width int) []term.Cell {
	for n := len(c.free); n > 0; n = len(c.free) {
		row := c.free[n-1]
		c.free[n-1] = nil
		c.free = c.free[:n-1]
		if cap(row) >= width {
			return row[:width]
		}
	}
	return makeNewRow(width, c.columnCap)
}

func (c *rawCells) fillInCoords(pos term.Coordinates) (
	from, to term.Coordinates, rowsFilled int,
) {
	assertValidCoords(pos)
	from = pos
	if rowsFilled = c.fillInRows(pos.Y); rowsFilled != 0 {
		from.Y -= rowsFilled
		// if rawCells was un-initialized or for some
		// reason base row was removed i.e. a truncate op
		if from.Y < 0 {
			from.Y = 0
		}
		from.X = c.Columns(from.Y)
		c.fillInColumns(pos)
	} else {
		from.X -= c.fillInColumns(pos)
	}
	to = pos

	return
}

func (c *rawCells) insert(at term.Coordinates, str string) (
	from, to term.Coordinates,
) {
	var rowsFilled int
	from, to, rowsFilled = c.fillInCoords(at)
	// fillInCoords fills in with newlines up to Y
	if rowsFilled != 0 {
		var i int
		for ; i < len(str) && str[i] == '\n'; i++ {
		}
		str = str[i:]
	}
	next := to
	state := -1
	var cluster string
	var width uint8
	for len(str) > 0 {
		cluster, str, width, state = graphemecluster.StepString(str, state)
		bytecount := uint8(len([]byte(cluster)))
		if bytecount == 1 { // specialized perf case for ASCII, avoids allocs
			for _, r := range cluster { // extract rune with no allocs
				next = c.insertAtPerf(next, r, width, bytecount)
			}
		} else {
			next = c.insertAt(next, []rune(cluster), width, bytecount)
		}
	}
	to = next
	return
}

func copyToBuilder(builder *strings.Builder, cells [][]term.Cell) {
	for i, r := range cells {
		if i != 0 {
			builder.WriteByte('\n')
		}
		copyRowToBuilder(builder, r)
	}
}

func copyRowToBuilder(builder *strings.Builder, cells []term.Cell) {
	builder.Grow(len(cells)) // almost every time this is exact
	for _, c := range cells {
		builder.WriteRune(c.Ch)
		for _, comb := range c.CombiningRunes() {
			builder.WriteRune(comb)
		}
	}
}

// asciiChars holds every ASCII codepoint in order, so a delete that
// removes exactly one such cell can report the removed text as a slice
// of this constant instead of allocating a string. Overwriting a narrow
// glyph with a wide one drops the cell the wide glyph now covers, once
// per wide character of bulk output.
const asciiChars = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f" +
	"\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f" +
	` !"#$%&'()*+,-./0123456789:;<=>?` +
	`@ABCDEFGHIJKLMNOPQRSTUVWXYZ[\]^_` +
	"`abcdefghijklmnopqrstuvwxyz{|}~\x7f"

// singleASCIICellAt reports the rune of the cell the [start, end) range
// covers when that range is exactly one cell holding one ASCII rune with
// no combining marks.
func (c *rawCells) singleASCIICellAt(start, end term.Coordinates) (rune, bool) {
	if end.X != start.X+1 || start.Y >= len(c.cells) || end.X > len(c.cells[start.Y]) {
		return 0, false
	}
	cell := c.cells[start.Y][start.X]
	if cell.CombiningRunes() != nil || cell.Ch < 0 || cell.Ch >= utf8.RuneSelf {
		return 0, false
	}
	return cell.Ch, true
}

func copyToBuffer(builder *bytes.Buffer, cells [][]term.Cell) {
	for i, r := range cells {
		if i != 0 {
			builder.WriteByte('\n')
		}
		copyRowToBuffer(builder, r)
	}
}

func copyRowToBuffer(builder *bytes.Buffer, cells []term.Cell) {
	builder.Grow(len(cells)) // almost every time this is exact
	for _, c := range cells {
		builder.WriteRune(c.Ch)
		for _, comb := range c.CombiningRunes() {
			builder.WriteRune(comb)
		}
	}
}

func (c *rawCells) conflate(row int) {
	// copy cells from next row into current row
	rlen := len(c.cells[row+1])
	if rlen != 0 {
		c.cells[row] = append(c.cells[row], c.cells[row+1]...)
	}

	// copy all rows into row we just moved up and trim last row
	copy(c.cells[row+1:], c.cells[row+2:])
	// Drop the conflated row pointer from the tail so its []term.Cell
	// allocation can be GC'd before the slot is overwritten.
	last := len(c.cells) - 1
	c.cells[last] = nil
	c.cells = c.cells[:last]
}

func (c *rawCells) mergeMarkedRows(
	end int,
	isMark func(term.Cell) bool,
	clearMark func(*term.Cell),
) (merged int) {
	n := len(c.cells)
	if end > n {
		end = n
	}
	if end <= 0 || n <= 1 {
		return 0
	}

	write := 0
	for read := 0; read < n; {
		row := c.cells[read]
		// Start of a potential group: check if this row is eligible (i.e.
		// head row must be within [0, end)) and its last cell is marked.
		lastCol := len(row) - 1
		if read >= end || lastCol < 0 || !isMark(row[lastCol]) {
			// No group starts here; just copy if write != read.
			if write != read {
				c.cells[write] = row
			}
			write++
			read++
			continue
		}

		// Collect consecutive rows that participate in this group.
		groupEnd := read
		for groupEnd+1 < n {
			curRow := c.cells[groupEnd]
			curLast := len(curRow) - 1
			if curLast < 0 || !isMark(curRow[curLast]) {
				break
			}
			groupEnd++
		}
		// groupEnd is inclusive last row of the merged line.

		// Compute total length and clear marks on intermediate rows.
		total := len(row)
		clearMark(&row[lastCol])
		for y := read + 1; y <= groupEnd; y++ {
			nextRow := c.cells[y]
			if last := len(nextRow) - 1; last >= 0 && y < groupEnd && isMark(nextRow[last]) {
				clearMark(&nextRow[last])
			}
			total += len(nextRow)
		}

		// Merge.
		merge := groupEnd - read
		if merge == 0 {
			if write != read {
				c.cells[write] = row
			}
			write++
			read++
			continue
		}

		if cap(row) < total {
			grown := make([]term.Cell, len(row), total)
			copy(grown, row)
			row = grown
		}
		for y := read + 1; y <= groupEnd; y++ {
			row = append(row, c.cells[y]...)
		}
		c.cells[write] = row
		write++
		read = groupEnd + 1
		merged += merge
	}
	// Drop the trailing row pointers so the merged-away row allocations are
	// not pinned by the unused tail of the backing array.
	clear(c.cells[write:])
	c.cells = c.cells[:write]
	return merged
}

func (c *rawCells) deleteRowRange(
	builder *strings.Builder, row, fromX, toX int,
) {
	copyRowToBuilder(builder, c.cells[row][fromX:toX])
	c.removeRowRange(row, fromX, toX)
}

func (c *rawCells) removeRowRange(row, fromX, toX int) {
	copy(c.cells[row][fromX:], c.cells[row][toX:])
	c.cells[row] = c.cells[row][:len(c.cells[row])-(toX-fromX)]
}

func (c *rawCells) delete(from, to term.Coordinates) (
	start, end term.Coordinates, str string,
) {
	assertValidCoords(from)
	assertValidCoords(to)
	start, end = term.CoordinatesSort(from, to)

	if start.Y == end.Y {
		if r, ok := c.singleASCIICellAt(start, end); ok {
			c.removeRowRange(start.Y, start.X, end.X)
			return start, end, asciiChars[r : r+1]
		}
		var builder strings.Builder
		c.deleteRowRange(&builder, start.Y, start.X, end.X)
		str = builder.String()
		return
	}

	builder := strings.Builder{}

	// trim til end of first row
	if start.X < len(c.cells[start.Y]) {
		c.deleteRowRange(&builder, start.Y, start.X, len(c.cells[start.Y]))
	}
	if start.Y+1 < len(c.cells) {
		builder.WriteByte('\n')
	}

	// copy rows in between and move last row to second row, if applicable
	lastRow := end.Y
	if diff := end.Y - start.Y; diff > 1 {
		copyToBuilder(&builder, c.cells[start.Y+1:end.Y])
		copy(c.cells[start.Y+1:], c.cells[end.Y:])
		// Zero the dropped tail before truncating: each [][]term.Cell slot is
		// a slice header whose data ptr pins a potentially-large row of cells
		// until overwritten.
		newLen := len(c.cells) - diff + 1
		clear(c.cells[newLen:])
		c.cells = c.cells[:newLen]

		lastRow = start.Y + 1
		if lastRow < len(c.cells) {
			builder.WriteByte('\n')
		}
	}

	// then remove cells from last row; start.Y is now last row to delete
	if end.X > 0 {
		c.deleteRowRange(&builder, lastRow, 0, end.X)
	}

	// conflate last row in range
	if lastRow < len(c.cells) {
		c.conflate(start.Y)
	}

	str = builder.String()

	return
}

func (c *rawCells) Edit(_ context.Context, start, end term.Coordinates, str string) (
	from, to term.Coordinates, old string,
) {
	c.normalizeRows()
	defer func() { c.rowMeta = nil }()
	// Merge a grapheme-cluster continuation typed one keystroke after its
	// base (skin-tone modifier, variation selector, joiner) into the
	// preceding cell so it stays one undoable edit instead of a broken box.
	if start == end {
		if s, e, base, ok := c.continuationReplace(start, str); ok {
			start, end, str = s, e, base+str
		}
	}

	from = start
	to = start
	if start != end {
		from, _, old = c.delete(start, end)
		to = from
	}

	if str != "" {
		from, to = c.insert(from, str)
	}

	return
}

func (c *rawCells) continuationReplace(pos term.Coordinates, str string) (
	start, end term.Coordinates, base string, ok bool,
) {
	if str == "" || str == "\n" {
		return
	}
	if pos.X <= 0 || pos.Y >= len(c.cells) || pos.X > len(c.cells[pos.Y]) {
		return
	}
	prev := c.cells[pos.Y][pos.X-1]
	if prev.Ch == 0 || prev.Ch == '\n' {
		return
	}
	base = string(append([]rune{prev.Ch}, prev.CombiningRunes()...))
	// Reject boundary-forming runes (TAB, newline) that are zero-width but
	// start their own cluster: only merge when base+str stays one cluster.
	if _, rest, _, _ := graphemecluster.StepString(base+str, -1); rest != "" {
		base = ""
		return
	}
	start = term.Coordinates{X: pos.X - 1, Y: pos.Y}
	end = pos
	ok = true
	return
}

func (c *rawCells) Columns(y int) (j int) {
	j = len(c.row(y))
	return
}

func (c *rawCells) Rows() int {
	return len(c.cells)
}

func (c *rawCells) String() string {
	return term.CellsToString(c.RawCells())
}

func (c *rawCells) RawCells() [][]term.Cell {
	if !c.performance {
		return c.cells
	}
	c.ensureRowMeta()
	for i, row := range c.cells {
		c.rowMeta[i].occupied = len(row)
	}
	c.normalizeRows()
	return c.cells
}

func (c *rawCells) Cell(pos term.Coordinates) (
	cell term.Cell, ok bool,
) {
	assertValidCoords(pos)
	if pos.Y >= c.Rows() || pos.X >= len(c.row(pos.Y)) {
		return
	}
	cell = c.row(pos.Y)[pos.X]
	ok = true
	return
}

func (c *rawCells) ReadFrom(r io.Reader) (int64, error) {
	return c.readFromWithView(r, c)
}

func (c *rawCells) readFromWithView(r io.Reader, view View) (int64, error) {
	rowY := nextWrite(view).Y
	reader := bufio.NewReader(r)
	n := int64(0)

	// Each line accumulates in scratch and is committed as an exact-size
	// (cap==len) sub-slice of a shared slab, so short rows do not retain
	// columnCap-sized backing arrays for the lifetime of the buffer. The
	// 3-index slice means a later append to a committed row copies it
	// back out of the slab instead of clobbering its neighbour.
	scratch := c.cells[rowY]
	var slab []term.Cell
	var slabOff int
	slabSize := readSlabInitCells
	touched := false
	commit := func() {
		m := len(scratch)
		if slab == nil || cap(slab)-slabOff < m {
			slab = make([]term.Cell, max(slabSize, m))
			slabSize = min(slabSize*2, readSlabMaxCells)
			slabOff = 0
		}
		row := slab[slabOff : slabOff+m : slabOff+m]
		slabOff += m
		copy(row, scratch)
		c.cells[rowY] = row
		scratch = scratch[:0]
	}
	for {
		str, err := reader.ReadString('\n')
		state := -1
		var cluster string
		var width, byteCount uint8
		n += int64(len([]byte(str)))
		for len(str) > 0 {
			touched = true
			// NOTE: this is significantly slower than, just ignoring grapheme clusters
			// but it should be ok as it's done once per file, and because calculating the width
			// is front loaded, it should amortize over long interactions on a particular file.
			cluster, str, width, state = graphemecluster.StepString(str, state)
			// most of the type we'll hit this branch, which performs no extra allocations
			// in particular, the conversio from cluster string to []rune causes 1 slice
			// per cell, in each file, which is quite bit of of overhead.
			if len(cluster) == 1 && width <= 1 {
				byteCount = 1
				switch cluster[0] {
				case '\n':
					commit()
					c.cells = append(c.cells, nil)
					rowY++
				default:
					cell := term.Cell{
						Ch:    rune(cluster[0]),
						Width: width,
						Bytes: byteCount,
					}
					scratch = append(scratch, cell)
				}
			} else {
				byteCount = uint8(len([]byte(cluster)))
				r := []rune(cluster)
				switch r[0] {
				case '\n':
					commit()
					c.cells = append(c.cells, nil)
					rowY++
				default:
					cell := term.Cell{
						Ch:    r[0],
						Width: width,
						Bytes: byteCount,
					}
					if len(r) > 1 {
						cell.SetCombining(r[1:])
					}
					scratch = append(scratch, cell)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				err = nil
			}
			if touched {
				commit()
			}
			return n, err
		}
	}
}
