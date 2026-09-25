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

package vteprobe

import "github.com/unstablebuild/rune-go-sdk/term"

// Slab is a reusable scratch arena for Cursor.Infer. Passing the same
// Slab to successive Infer calls lets the expensive per-line tab
// expansion and the alignment scratch slices be recycled instead of
// re-allocated on every call, without making Infer stateful: a Slab
// holds only transient working memory whose contents are overwritten at
// the start of each call, never results carried between calls.
//
// A Slab is not safe for concurrent use; give each goroutine its own.
// The zero value is usable, and NewSlab is provided for callers that
// prefer explicit construction. A nil *Slab is allowed everywhere a
// *Slab is accepted and means "allocate fresh", so callers that do not
// care about reuse can pass nil.
type Slab struct {
	// runes is a bump-allocated backing store for expanded line cells.
	// Each Infer call resets the high-water mark to 0 and re-fills it.
	runes []rune
	used  int

	// expanded holds, per call, the tab-expanded rune view of every
	// file line for one tabstop, memoised so the two alignment passes
	// (1:1 and wrap) do not expand the same lines twice. memoTabstop
	// records which tabstop expanded currently holds (0 = empty).
	expanded    [][]rune
	memoTabstop int

	// rowToLine is reusable scratch for the candidate-vote simulation.
	rowToLine []int
	// segCounts is reusable scratch for per-line wrap segment counts.
	segCounts []int
	// bodies is reusable scratch for the trimmed rendered row bodies.
	bodies [][]rune

	rows    []extractedRow
	rowRune []rune
	rowAttr []term.Attributes
	rowCol  []int

	wrapRowFileLine []int
	wrapOffset      []int
	wrapFolded      []bool
}

// NewSlab returns an empty Slab ready to be threaded through Infer
// calls.
func NewSlab() *Slab {
	return &Slab{}
}

// reset prepares the slab for a fresh Infer call: working buffers keep
// their capacity but are logically emptied, and the expansion memo is
// invalidated.
func (s *Slab) reset() {
	s.used = 0
	s.memoTabstop = 0
	s.expanded = s.expanded[:0]
}

// expandLinesFor returns the tab-expanded rune view of every file line
// at tabstop, memoised within the current call. The returned slices are
// owned by the slab and valid only until the next reset; callers must
// not retain them across Infer calls. tab expansion writes into the
// bump arena so a steady-state caller that reuses the slab performs no
// allocation once the arena is large enough.
func (s *Slab) expandLinesFor(lines [][]term.Cell, tabstop int) [][]rune {
	if s.memoTabstop == tabstop && len(s.expanded) == len(lines) {
		return s.expanded
	}
	// Switching tabstop (or first use this call): rebuild from the
	// start of the arena so memory is bounded by a single tabstop's
	// expansion rather than growing per tabstop.
	s.used = 0
	if cap(s.expanded) >= len(lines) {
		s.expanded = s.expanded[:len(lines)]
	} else {
		s.expanded = make([][]rune, len(lines))
	}
	for i, ln := range lines {
		s.expanded[i] = s.expandCellsInto(ln, tabstop)
	}
	s.memoTabstop = tabstop
	return s.expanded
}

// expandCellsInto tab-expands one line into the bump arena and returns
// the sub-slice holding it. The slice aliases the arena, so it stays
// valid only while the arena is not reset or grown past it; within a
// single expandLinesFor pass the arena only grows by appends, so earlier
// slices remain valid for the duration of the call.
func (s *Slab) expandCellsInto(line []term.Cell, tabstop int) []rune {
	if tabstop <= 0 {
		tabstop = 1
	}
	start := s.used
	col := 0
	for _, cell := range line {
		if cell.Ch != '\t' {
			s.runes = appendRune(s.runes, &s.used, cell.Ch)
			for _, comb := range cell.CombiningRunes() {
				s.runes = appendRune(s.runes, &s.used, comb)
			}
			col++
			continue
		}
		pad := tabstop - (col % tabstop)
		for k := 0; k < pad; k++ {
			s.runes = appendRune(s.runes, &s.used, ' ')
		}
		col += pad
	}
	return s.runes[start:s.used:s.used]
}

// appendRune appends r to buf, growing it if needed, and advances *used.
// It returns the (possibly reallocated) backing slice.
func appendRune(buf []rune, used *int, r rune) []rune {
	if *used == len(buf) {
		buf = append(buf, r)
	} else {
		buf[*used] = r
	}
	*used++
	return buf
}

// segCountBuf returns an []int of length n reusing the slab's scratch.
func (s *Slab) segCountBuf(n int) []int {
	if cap(s.segCounts) >= n {
		return s.segCounts[:n]
	}
	s.segCounts = make([]int, n)
	return s.segCounts
}

// rowToLineBuf returns an []int of length n reusing the slab's scratch.
func (s *Slab) rowToLineBuf(n int) []int {
	if cap(s.rowToLine) >= n {
		return s.rowToLine[:n]
	}
	s.rowToLine = make([]int, n)
	return s.rowToLine
}

// bodyBuf returns a [][]rune of length n reusing the slab's scratch.
func (s *Slab) bodyBuf(n int) [][]rune {
	if cap(s.bodies) >= n {
		return s.bodies[:n]
	}
	s.bodies = make([][]rune, n)
	return s.bodies
}

func (s *Slab) rowBuf(n int) []extractedRow {
	if cap(s.rows) >= n {
		return s.rows[:n]
	}
	s.rows = make([]extractedRow, n)
	return s.rows
}

func (s *Slab) beginExtractRows(raw [][]term.Cell) ([]extractedRow, []int) {
	rows := s.rowBuf(len(raw))
	rowWidths := s.rowWidths(raw)
	neededRunes := 0
	neededCols := 0
	for _, width := range rowWidths {
		neededRunes += width
		neededCols += width
	}
	if cap(s.rowRune) < neededRunes {
		s.rowRune = make([]rune, neededRunes)
	} else {
		s.rowRune = s.rowRune[:neededRunes]
	}
	if cap(s.rowAttr) < neededRunes {
		s.rowAttr = make([]term.Attributes, neededRunes)
	} else {
		s.rowAttr = s.rowAttr[:neededRunes]
	}
	if cap(s.rowCol) < neededCols {
		s.rowCol = make([]int, neededCols)
	} else {
		s.rowCol = s.rowCol[:neededCols]
	}
	return rows, rowWidths
}

func (s *Slab) rowWidths(raw [][]term.Cell) []int {
	widths := s.rowToLineBuf(len(raw))
	for y, row := range raw {
		width := 0
		for _, c := range row {
			cellWidth := int(c.Width)
			if cellWidth < 1 {
				cellWidth = 1
			}
			width += cellWidth
		}
		widths[y] = width
	}
	return widths
}

func (s *Slab) wrapInfoBuf(totalRows int) wrapInfo {
	if cap(s.wrapRowFileLine) < totalRows {
		s.wrapRowFileLine = make([]int, totalRows)
	} else {
		s.wrapRowFileLine = s.wrapRowFileLine[:totalRows]
		clear(s.wrapRowFileLine)
	}
	if cap(s.wrapOffset) < totalRows {
		s.wrapOffset = make([]int, totalRows)
	} else {
		s.wrapOffset = s.wrapOffset[:totalRows]
		clear(s.wrapOffset)
	}
	if cap(s.wrapFolded) < totalRows {
		s.wrapFolded = make([]bool, totalRows)
	} else {
		s.wrapFolded = s.wrapFolded[:totalRows]
		clear(s.wrapFolded)
	}
	return wrapInfo{
		rowFileLine: s.wrapRowFileLine,
		wrapOffset:  s.wrapOffset,
		folded:      s.wrapFolded,
	}
}
