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
	"strconv"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
)

// buildPerfBuffer returns a Buffer initialised in performance mode whose rows
// match the given text (one row per string). Each row is given capacity equal
// to length so rowsContiguous-style tests see deterministic slices.
func buildPerfBuffer(rows ...string) *Buffer {
	b := new(Buffer)
	b.InitPerformance(0, 0, ' ')
	b.cells.cells = make([][]term.Cell, len(rows))
	for i, r := range rows {
		b.cells.cells[i] = stringToCells(r)
	}
	if len(rows) == 0 {
		b.cells.cells = [][]term.Cell{{}}
	}
	return b
}

func stringToCells(s string) []term.Cell {
	rs := []rune(s)
	out := make([]term.Cell, len(rs))
	for i, r := range rs {
		out[i] = term.Cell{Ch: r, Width: 1, Bytes: uint8(len(string(r)))}
	}
	return out
}

func cellsRowsAsStrings(b *Buffer) []string {
	out := make([]string, b.Rows())
	for y := 0; y < b.Rows(); y++ {
		row := b.RawCells()[y]
		runes := make([]rune, len(row))
		for x, c := range row {
			runes[x] = c.Ch
		}
		out[y] = string(runes)
	}
	return out
}

func TestExtendRowToWidth(t *testing.T) {
	t.Run("extends short row using fillInChar", func(t *testing.T) {
		b := buildPerfBuffer("ab")
		added, ok := b.ExtendRowToWidth(0, 5)
		require.True(t, ok)
		assert.Equal(t, 3, added)
		assert.Equal(t, []string{"ab   "}, cellsRowsAsStrings(b))
	})
	t.Run("no-op when row already wide enough", func(t *testing.T) {
		b := buildPerfBuffer("abcde")
		added, ok := b.ExtendRowToWidth(0, 3)
		require.True(t, ok)
		assert.Equal(t, 0, added)
		assert.Equal(t, []string{"abcde"}, cellsRowsAsStrings(b))
	})
	t.Run("out of bounds row", func(t *testing.T) {
		b := buildPerfBuffer("ab")
		added, ok := b.ExtendRowToWidth(5, 8)
		require.True(t, ok)
		assert.Equal(t, 0, added)
	})
	t.Run("rejects non-performance mode", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() {
			_, _ = b.ExtendRowToWidth(0, 8)
		})
	})
	t.Run("uses custom fill char", func(t *testing.T) {
		b := new(Buffer)
		b.InitPerformance(0, 0, '.')
		b.cells.cells = [][]term.Cell{stringToCells("ab")}
		added, ok := b.ExtendRowToWidth(0, 4)
		require.True(t, ok)
		assert.Equal(t, 2, added)
		assert.Equal(t, []string{"ab.."}, cellsRowsAsStrings(b))
	})
	t.Run("extends the logical row after ring wrap", func(t *testing.T) {
		b := buildPerfBuffer("a", "bb", "ccc")
		require.True(t, b.AppendBlankRowsBounded(1, 1, 3))

		added, ok := b.ExtendRowToWidth(0, 4)
		require.True(t, ok)
		assert.Equal(t, 2, added)
		assert.Equal(t, []string{"bb  ", "ccc", " "}, cellsRowsAsStrings(b))
	})
}

func TestTrimRowsFromEnd(t *testing.T) {
	t.Run("trims requested rows", func(t *testing.T) {
		b := buildPerfBuffer("a", "b", "c", "d")
		removed, ok := b.TrimRowsFromEnd(2)
		require.True(t, ok)
		assert.Equal(t, 2, removed)
		assert.Equal(t, []string{"a", "b"}, cellsRowsAsStrings(b))
	})
	t.Run("keeps at least one row", func(t *testing.T) {
		b := buildPerfBuffer("a", "b")
		removed, ok := b.TrimRowsFromEnd(10)
		require.True(t, ok)
		assert.Equal(t, 1, removed)
		assert.Equal(t, []string{"a"}, cellsRowsAsStrings(b))
	})
	t.Run("no-op on single row buffer", func(t *testing.T) {
		b := buildPerfBuffer("a")
		removed, ok := b.TrimRowsFromEnd(1)
		require.True(t, ok)
		assert.Equal(t, 0, removed)
	})
	t.Run("rejects non-positive count", func(t *testing.T) {
		b := buildPerfBuffer("a", "b")
		removed, ok := b.TrimRowsFromEnd(0)
		require.True(t, ok)
		assert.Equal(t, 0, removed)
	})
	t.Run("rejects non-performance mode", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() {
			_, _ = b.TrimRowsFromEnd(1)
		})
	})
}

func TestTrimRowsFromStart(t *testing.T) {
	t.Run("trims requested rows", func(t *testing.T) {
		b := buildPerfBuffer("a", "b", "c", "d")
		removed, ok := b.TrimRowsFromStart(2)
		require.True(t, ok)
		assert.Equal(t, 2, removed)
		assert.Equal(t, []string{"c", "d"}, cellsRowsAsStrings(b))
	})
	t.Run("keeps at least one row", func(t *testing.T) {
		b := buildPerfBuffer("a", "b")
		removed, ok := b.TrimRowsFromStart(10)
		require.True(t, ok)
		assert.Equal(t, 1, removed)
		assert.Equal(t, []string{"b"}, cellsRowsAsStrings(b))
	})
	t.Run("no-op on single row buffer", func(t *testing.T) {
		b := buildPerfBuffer("a")
		removed, ok := b.TrimRowsFromStart(1)
		require.True(t, ok)
		assert.Equal(t, 0, removed)
	})
	t.Run("rejects non-positive count", func(t *testing.T) {
		b := buildPerfBuffer("a", "b")
		removed, ok := b.TrimRowsFromStart(0)
		require.True(t, ok)
		assert.Equal(t, 0, removed)
	})
	t.Run("rejects non-performance mode", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() {
			_, _ = b.TrimRowsFromStart(1)
		})
	})
}

func TestAppendBlankRows(t *testing.T) {
	t.Run("appends rows filled with fillInChar", func(t *testing.T) {
		b := buildPerfBuffer("ab")
		ok := b.AppendBlankRows(2, 3)
		require.True(t, ok)
		assert.Equal(t, []string{"ab", "   ", "   "}, cellsRowsAsStrings(b))
	})
	t.Run("fill cells match the edit insert shape", func(t *testing.T) {
		b := buildPerfBuffer("a")
		require.True(t, b.AppendBlankRows(1, 2))
		for _, c := range b.RawCells()[1] {
			assert.Equal(t, term.Cell{Ch: ' ', Width: 1, Bytes: 1}, c)
		}
	})
	t.Run("non-positive count is a no-op", func(t *testing.T) {
		b := buildPerfBuffer("a")
		require.True(t, b.AppendBlankRows(0, 3))
		assert.Equal(t, []string{"a"}, cellsRowsAsStrings(b))
	})
	t.Run("reuses storage recycled by TrimRowsFromStart", func(t *testing.T) {
		b := buildPerfBuffer("abc", "def", "g")
		recycled := &b.RawCells()[0][0]
		removed, ok := b.TrimRowsFromStart(1)
		require.True(t, ok)
		require.Equal(t, 1, removed)
		require.True(t, b.AppendBlankRows(1, 3))
		assert.Equal(t, []string{"def", "g", "   "}, cellsRowsAsStrings(b))
		appended := &b.RawCells()[2][0]
		assert.Same(t, recycled, appended)
	})
	t.Run("allocates when recycled rows are too narrow", func(t *testing.T) {
		b := buildPerfBuffer("ab", "cd", "e")
		recycled := &b.RawCells()[0][0]
		_, _ = b.TrimRowsFromStart(1)
		require.True(t, b.AppendBlankRows(1, 5))
		assert.Equal(t, []string{"cd", "e", "     "}, cellsRowsAsStrings(b))
		assert.NotSame(t, recycled, &b.RawCells()[2][0])
	})
	t.Run("rejects non-performance mode", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() {
			_ = b.AppendBlankRows(1, 1)
		})
	})
	t.Run("follows a fill character changed after rows exist", func(t *testing.T) {
		b := buildPerfBuffer("a")
		require.True(t, b.AppendBlankRows(1, 2))
		assert.Equal(t, []string{"a", "  "}, cellsRowsAsStrings(b))

		b.cells.setFillInChar('漢')
		require.True(t, b.AppendBlankRows(1, 2))
		for _, c := range b.RawCells()[2] {
			assert.Equal(t, term.Cell{Ch: '漢', Width: 2, Bytes: 3}, c)
		}
	})
	t.Run("measures a zero fill character", func(t *testing.T) {
		b := new(Buffer)
		b.InitPerformance(4, 4, '\x00')
		require.True(t, b.AppendBlankRows(1, 2))
		for _, c := range b.RawCells()[1] {
			assert.Equal(t, term.Cell{Ch: '\x00', Width: 0, Bytes: 1}, c)
		}
	})
}

func TestAppendBlankRowsBounded(t *testing.T) {
	t.Run("preserves order across repeated ring wraps", func(t *testing.T) {
		b := buildPerfBuffer("0", "1", "2", "3")

		for i := 4; i < 20; i++ {
			row := b.RawCells()[0]
			recycled := &row[0]
			require.True(t, b.AppendBlankRowsBounded(1, 1, 4))
			last := b.Rows() - 1
			b.RawCells()[last][0].Ch = rune('0' + i%10)
			assert.Same(t, recycled, &b.RawCells()[last][0])
		}

		assert.Equal(t, []string{"6", "7", "8", "9"}, cellsRowsAsStrings(b))
	})

	t.Run("grows until the bound then recycles", func(t *testing.T) {
		b := buildPerfBuffer("a", "b")

		require.True(t, b.AppendBlankRowsBounded(2, 1, 4))
		assert.Equal(t, []string{"a", "b", " ", " "}, cellsRowsAsStrings(b))
		recycled := &b.RawCells()[0][0]

		require.True(t, b.AppendBlankRowsBounded(1, 1, 4))
		assert.Equal(t, []string{"b", " ", " ", " "}, cellsRowsAsStrings(b))
		assert.Same(t, recycled, &b.RawCells()[3][0])
	})

	t.Run("drops resize-created rows above the bound", func(t *testing.T) {
		b := buildPerfBuffer("a", "b", "c", "d", "e")

		require.True(t, b.AppendBlankRowsBounded(1, 1, 3))
		assert.Equal(t, []string{"d", "e", " "}, cellsRowsAsStrings(b))
	})

	t.Run("RawCells mutations use logical row order", func(t *testing.T) {
		b := buildPerfBuffer("a", "b", "c")
		require.True(t, b.AppendBlankRowsBounded(2, 1, 3))

		rows := b.RawCells()
		rows[0][0].Ch = 'x'
		rows[2][0].Ch = 'z'
		assert.Equal(t, []string{"x", " ", "z"}, cellsRowsAsStrings(b))
	})

	t.Run("resets the occupied prefix of sparse rows", func(t *testing.T) {
		b := new(Buffer)
		b.InitPerformance(1, 8, ' ')
		require.True(t, b.AppendBlankRowsBounded(1, 8, 1))
		row := b.MutableRow(0, 1)
		row[0].Ch = 'x'
		// The untouched suffix remains the row's blank template and is
		// not part of the reset work.
		require.Equal(t, 1, b.cells.rowMeta[0].occupied)

		require.True(t, b.AppendBlankRowsBounded(1, 8, 1))
		assert.Equal(t, "        ", stringRow(b.Row(0)))
		assert.Equal(t, 0, b.cells.rowMeta[0].occupied)
	})

	t.Run("fully resets dense rows", func(t *testing.T) {
		b := new(Buffer)
		b.InitPerformance(1, 8, ' ')
		require.True(t, b.AppendBlankRowsBounded(1, 8, 1))
		row := b.MutableRow(0, 8)
		for i := range row {
			row[i].Ch = 'x'
		}

		require.True(t, b.AppendBlankRowsBounded(1, 8, 1))
		assert.Equal(t, "        ", stringRow(b.Row(0)))
	})

	t.Run("fully resets when the blank template changes", func(t *testing.T) {
		b := new(Buffer)
		b.InitPerformance(1, 8, ' ')
		require.True(t, b.AppendBlankRowsBounded(1, 8, 1))
		b.MutableRow(0, 1)[0].Ch = 'x'
		b.cells.setFillInChar('.')

		require.True(t, b.AppendBlankRowsBounded(1, 8, 1))
		assert.Equal(t, "........", stringRow(b.Row(0)))
	})

	t.Run("rejects non-performance mode", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() {
			_ = b.AppendBlankRowsBounded(1, 1, 4)
		})
	})
}

func stringRow(row []term.Cell) string {
	runes := make([]rune, len(row))
	for i, cell := range row {
		runes[i] = cell.Ch
	}
	return string(runes)
}

func TestResetPerformanceCapacityDropsBackingStorage(t *testing.T) {
	b := new(Buffer)
	b.InitPerformance(2, 2, '.')
	b.cells.cells = make([][]term.Cell, 100, 200)
	for i := range b.cells.cells {
		b.cells.cells[i] = make([]term.Cell, 100, 200)
	}

	b.ResetPerformanceCapacity(3, 4)

	assert.Equal(t, 1, b.Rows())
	assert.Equal(t, 3, cap(b.cells.cells))
	assert.Equal(t, 0, len(b.cells.cells[0]))
	assert.Equal(t, 4, cap(b.cells.cells[0]))
	assert.Equal(t, '.', b.cells.fillInChar)
}

func TestResetPerformanceCapacityRejectsNonPerformanceMode(t *testing.T) {
	b := NewBuffer()
	assert.Panics(t, func() {
		b.ResetPerformanceCapacity(1, 1)
	})
}

const testMark uint8 = 0x80

func markLastCell(cells []term.Cell) {
	if len(cells) == 0 {
		return
	}
	cells[len(cells)-1].Bytes = testMark
}

func TestMergeMarkedRows(t *testing.T) {
	isMark := func(c term.Cell) bool { return c.Bytes == testMark }
	clearMark := func(c *term.Cell) { c.Bytes = 0 }
	t.Run("merges a single two-row group", func(t *testing.T) {
		b := buildPerfBuffer("abc", "def", "gh")
		markLastCell(b.cells.cells[0]) // "abc" continues to "def"
		merged, ok := b.MergeMarkedRows(3, isMark, clearMark)
		require.True(t, ok)
		assert.Equal(t, 1, merged)
		assert.Equal(t, []string{"abcdef", "gh"}, cellsRowsAsStrings(b))
		assert.Equal(t, uint8(0), b.cells.cells[0][2].Bytes,
			"mark should be cleared on merged boundary")
	})
	t.Run("merges a chain of marked rows", func(t *testing.T) {
		b := buildPerfBuffer("ab", "cd", "ef", "gh")
		markLastCell(b.cells.cells[0])
		markLastCell(b.cells.cells[1])
		markLastCell(b.cells.cells[2])
		merged, ok := b.MergeMarkedRows(4, isMark, clearMark)
		require.True(t, ok)
		assert.Equal(t, 3, merged)
		assert.Equal(t, []string{"abcdefgh"}, cellsRowsAsStrings(b))
	})
	t.Run("does not start a group past end", func(t *testing.T) {
		b := buildPerfBuffer("ab", "cd", "ef")
		markLastCell(b.cells.cells[1]) // "cd" marks, but head is at y=1 >= end=1
		merged, ok := b.MergeMarkedRows(1, isMark, clearMark)
		require.True(t, ok)
		assert.Equal(t, 0, merged)
		assert.Equal(t, []string{"ab", "cd", "ef"}, cellsRowsAsStrings(b))
	})
	t.Run("ignores rows with no cells", func(t *testing.T) {
		b := buildPerfBuffer("ab", "", "cd")
		merged, ok := b.MergeMarkedRows(3, isMark, clearMark)
		require.True(t, ok)
		assert.Equal(t, 0, merged)
	})
	t.Run("merges through a contiguous slab", func(t *testing.T) {
		// Simulate what copyCellsContiguous produces: all rows share a
		// single backing slab with cap==len. This should trigger the
		// unsafe fast path in mergeMarkedRows.
		src := [][]term.Cell{
			stringToCells("ab"),
			stringToCells("cd"),
			stringToCells("ef"),
		}
		b := new(Buffer)
		b.InitPerformance(0, 0, ' ')
		b.cells.cells = copyCellsContiguous(b.cells.cells, src)
		markLastCell(b.cells.cells[0])
		markLastCell(b.cells.cells[1])
		merged, ok := b.MergeMarkedRows(3, isMark, clearMark)
		require.True(t, ok)
		assert.Equal(t, 2, merged)
		assert.Equal(t, []string{"abcdef"}, cellsRowsAsStrings(b))
	})
	t.Run("rejects non-performance mode", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() {
			_, _ = b.MergeMarkedRows(1, isMark, clearMark)
		})
	})
}

func TestSplitRowsBatch(t *testing.T) {
	t.Run("splits single row into two pieces", func(t *testing.T) {
		b := buildPerfBuffer("abcdefgh")
		added, ok := b.SplitRowsBatch([]RowSplit{{Y: 0, Width: 4, Times: 1}})
		require.True(t, ok)
		assert.Equal(t, 1, added)
		assert.Equal(t, []string{"abcd", "efgh"}, cellsRowsAsStrings(b))
	})
	t.Run("splits single row into three pieces", func(t *testing.T) {
		b := buildPerfBuffer("abcdefghij")
		added, ok := b.SplitRowsBatch([]RowSplit{{Y: 0, Width: 3, Times: 2}})
		require.True(t, ok)
		assert.Equal(t, 2, added)
		// expected: "abc", "def", "ghij"
		assert.Equal(t, []string{"abc", "def", "ghij"}, cellsRowsAsStrings(b))
	})
	t.Run("multiple splits in one batch preserve other rows", func(t *testing.T) {
		b := buildPerfBuffer("AAAA", "keep1", "BBBB", "keep2")
		added, ok := b.SplitRowsBatch([]RowSplit{
			{Y: 0, Width: 2, Times: 1},
			{Y: 2, Width: 2, Times: 1},
		})
		require.True(t, ok)
		assert.Equal(t, 2, added)
		assert.Equal(t, []string{
			"AA", "AA", "keep1", "BB", "BB", "keep2",
		}, cellsRowsAsStrings(b))
	})
	t.Run("empty splits is a no-op", func(t *testing.T) {
		b := buildPerfBuffer("abc", "def")
		added, ok := b.SplitRowsBatch(nil)
		require.True(t, ok)
		assert.Equal(t, 0, added)
		assert.Equal(t, []string{"abc", "def"}, cellsRowsAsStrings(b))
	})
	t.Run("pieces do not share writable capacity", func(t *testing.T) {
		// Appending to the head after a split must not clobber the tail
		// piece's backing memory.
		b := buildPerfBuffer("abcdefgh")
		_, _ = b.SplitRowsBatch([]RowSplit{{Y: 0, Width: 4, Times: 1}})
		b.cells.cells[0] = append(b.cells.cells[0], term.Cell{Ch: 'Z'})
		assert.Equal(t, []string{"abcdZ", "efgh"}, cellsRowsAsStrings(b))
	})
	t.Run("rejects non-performance mode", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() {
			_, _ = b.SplitRowsBatch([]RowSplit{{Y: 0, Width: 1, Times: 1}})
		})
	})
}

func TestSplitRowsBatchPadded(t *testing.T) {
	t.Run("pads short tail with fill char", func(t *testing.T) {
		b := buildPerfBuffer("abcde")
		added, ok := b.SplitRowsBatchPadded(
			[]RowSplit{{Y: 0, Width: 3, Times: 1}}, 3, ringDotCell())
		require.True(t, ok)
		assert.Equal(t, 1, added)
		assert.Equal(t, []string{"abc", "de."}, cellsRowsAsStrings(b))
	})
	t.Run("leaves exact-width tail untouched", func(t *testing.T) {
		b := buildPerfBuffer("abcdef")
		_, ok := b.SplitRowsBatchPadded(
			[]RowSplit{{Y: 0, Width: 3, Times: 1}}, 3, ringDotCell())
		require.True(t, ok)
		assert.Equal(t, []string{"abc", "def"}, cellsRowsAsStrings(b))
	})
	t.Run("works with padToWidth=0 (same as unpadded)", func(t *testing.T) {
		b := buildPerfBuffer("abcde")
		_, ok := b.SplitRowsBatchPadded(
			[]RowSplit{{Y: 0, Width: 3, Times: 1}}, 0, ringDotCell())
		require.True(t, ok)
		assert.Equal(t, []string{"abc", "de"}, cellsRowsAsStrings(b))
	})
	t.Run("pads multiple short tails from single slab", func(t *testing.T) {
		b := buildPerfBuffer("abcXY", "def123")
		_, ok := b.SplitRowsBatchPadded([]RowSplit{
			{Y: 0, Width: 3, Times: 1},
			{Y: 1, Width: 3, Times: 1},
		}, 3, ringDotCell())
		require.True(t, ok)
		assert.Equal(t, []string{"abc", "XY.", "def", "123"}, cellsRowsAsStrings(b))
	})
	t.Run("rejects non-performance mode", func(t *testing.T) {
		b := NewBuffer()
		assert.Panics(t, func() {
			_, _ = b.SplitRowsBatchPadded(
				[]RowSplit{{Y: 0, Width: 1, Times: 1}}, 2, ringSpaceCell())
		})
	})
}

func TestCopyCellsContiguous(t *testing.T) {
	t.Run("produces rows with cap==len, contiguous in memory", func(t *testing.T) {
		src := [][]term.Cell{
			stringToCells("abc"),
			stringToCells("de"),
			stringToCells("fghi"),
		}
		dst := copyCellsContiguous(nil, src)
		require.Len(t, dst, 3)
		for i, want := range []string{"abc", "de", "fghi"} {
			assert.Equal(t, len(want), len(dst[i]))
			assert.Equal(t, len(want), cap(dst[i]))
		}
	})
	t.Run("empty rows become nil", func(t *testing.T) {
		src := [][]term.Cell{stringToCells("ab"), {}, stringToCells("cd")}
		dst := copyCellsContiguous(nil, src)
		require.Len(t, dst, 3)
		assert.Nil(t, dst[1])
	})
	t.Run("empty src yields empty dst rows", func(t *testing.T) {
		src := [][]term.Cell{{}, {}}
		dst := copyCellsContiguous(nil, src)
		require.Len(t, dst, 2)
		assert.Nil(t, dst[0])
		assert.Nil(t, dst[1])
	})
}

func TestConflatePerfModeFastPath(t *testing.T) {
	t.Run("conflates two rows without allocating", func(t *testing.T) {
		b := buildPerfBuffer("abc", "def")
		x, ok := b.ConflateRow(0)
		require.True(t, ok)
		assert.Equal(t, 3, x)
		assert.Equal(t, []string{"abcdef"}, cellsRowsAsStrings(b))
	})
	t.Run("rejects conflate on last row", func(t *testing.T) {
		b := buildPerfBuffer("abc", "def")
		_, ok := b.ConflateRow(1)
		assert.False(t, ok)
	})
	t.Run("rejects conflate on single-row buffer", func(t *testing.T) {
		b := buildPerfBuffer("abc")
		_, ok := b.ConflateRow(0)
		assert.False(t, ok)
	})
}

func TestWrapRowPerfModeFastPath(t *testing.T) {
	t.Run("wraps a row at a column, keeping cells on both sides", func(t *testing.T) {
		b := buildPerfBuffer("abcdefgh")
		ok := b.WrapRow(0, 4)
		require.True(t, ok)
		assert.Equal(t, []string{"abcd", "efgh"}, cellsRowsAsStrings(b))
	})
	t.Run("wraps at end of row creates trailing empty row", func(t *testing.T) {
		b := buildPerfBuffer("abcd")
		ok := b.WrapRow(0, 4)
		require.True(t, ok)
		assert.Equal(t, 2, b.Rows())
		assert.Equal(t, 4, b.Columns(0))
		assert.Equal(t, 0, b.Columns(1))
	})
	t.Run("rejects out of bounds at", func(t *testing.T) {
		b := buildPerfBuffer("abcd")
		ok := b.WrapRow(0, 5)
		assert.False(t, ok)
	})
	t.Run("rejects out of bounds row", func(t *testing.T) {
		b := buildPerfBuffer("abcd")
		ok := b.WrapRow(1, 0)
		assert.False(t, ok)
	})
}

func TestWrapConflateRoundTrip(t *testing.T) {
	b := buildPerfBuffer("abcdefgh")
	require.True(t, b.WrapRow(0, 4))
	x, ok := b.ConflateRow(0)
	require.True(t, ok)
	assert.Equal(t, 4, x)
	assert.Equal(t, []string{"abcdefgh"}, cellsRowsAsStrings(b))
}

func TestCellsToBufferPerformance(t *testing.T) {
	t.Run("preserves row contents", func(t *testing.T) {
		src := [][]term.Cell{
			stringToCells("abc"),
			stringToCells("de"),
			stringToCells("fghi"),
		}
		b := CellsToBufferPerformance(src, ' ')
		assert.Equal(t, []string{"abc", "de", "fghi"}, cellsRowsAsStrings(b))
	})
	t.Run("initializes with at least one row", func(t *testing.T) {
		b := CellsToBufferPerformance(nil, ' ')
		assert.Equal(t, 1, b.Rows())
	})
	t.Run("uses fillInChar for later extend", func(t *testing.T) {
		b := CellsToBufferPerformance([][]term.Cell{stringToCells("ab")}, '.')
		added, ok := b.ExtendRowToWidth(0, 5)
		require.True(t, ok)
		assert.Equal(t, 3, added)
		assert.Equal(t, []string{"ab..."}, cellsRowsAsStrings(b))
	})
	t.Run("is in performance mode (no undo)", func(t *testing.T) {
		b := CellsToBufferPerformance([][]term.Cell{stringToCells("a")}, ' ')
		assert.Nil(t, b.undoer,
			"CellsToBufferPerformance should not allocate an undoer")
	})
	t.Run("wrap then conflate round trips correctly", func(t *testing.T) {
		b := CellsToBufferPerformance([][]term.Cell{stringToCells("abcdefgh")}, ' ')
		require.True(t, b.WrapRow(0, 4))
		assert.Equal(t, []string{"abcd", "efgh"}, cellsRowsAsStrings(b))
		_, ok := b.ConflateRow(0)
		require.True(t, ok)
		assert.Equal(t, []string{"abcdefgh"}, cellsRowsAsStrings(b))
	})
	t.Run("supports MergeMarkedRows across copied slab", func(t *testing.T) {
		src := [][]term.Cell{
			stringToCells("ab"),
			stringToCells("cd"),
			stringToCells("ef"),
		}
		b := CellsToBufferPerformance(src, ' ')
		markLastCell(b.cells.cells[0])
		markLastCell(b.cells.cells[1])
		merged, ok := b.MergeMarkedRows(3,
			func(c term.Cell) bool { return c.Bytes == testMark },
			func(c *term.Cell) { c.Bytes = 0 })
		require.True(t, ok)
		assert.Equal(t, 2, merged)
		assert.Equal(t, []string{"abcdef"}, cellsRowsAsStrings(b))
	})
}

func TestASCIIChars(t *testing.T) {
	require.Len(t, asciiChars, utf8.RuneSelf)
	for c := range utf8.RuneSelf {
		assert.Equal(t, string(rune(c)), asciiChars[c:c+1], "index %d", c)
	}
}

func TestBufferResetPerformanceCapacityTable(t *testing.T) {
	tests := []struct {
		name                                string
		rowCapacity, columnCapacity         int
		wantRowCapacity, wantColumnCapacity int
	}{
		{
			name:        "custom capacities",
			rowCapacity: 3, columnCapacity: 5,
			wantRowCapacity: 3, wantColumnCapacity: 5,
		},
		{
			name:            "zero capacities use defaults",
			wantRowCapacity: defRowCap, wantColumnCapacity: defColumnCap,
		},
		{
			name:        "negative capacities use defaults",
			rowCapacity: -2, columnCapacity: -3,
			wantRowCapacity: defRowCap, wantColumnCapacity: defColumnCap,
		},
		{
			name:        "one cell capacities",
			rowCapacity: 1, columnCapacity: 1,
			wantRowCapacity: 1, wantColumnCapacity: 1,
		},
		{
			name:        "grow capacities",
			rowCapacity: 128, columnCapacity: 256,
			wantRowCapacity: 128, wantColumnCapacity: 256,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer(ringComplexRows(), '.', 2)
			require.NotZero(t, b.cells.ringHead)
			_, _ = b.TrimRowsFromStart(2)
			require.NotEmpty(t, b.cells.free)
			b.cells.zwj = true
			b.cells.zwjPos = term.Coordinates{X: 3, Y: 2}

			b.ResetPerformanceCapacity(tt.rowCapacity, tt.columnCapacity)

			assert.Equal(t, 1, b.Rows())
			assert.NotNil(t, b.Row(0))
			assert.Empty(t, b.Row(0))
			assert.Equal(t, tt.wantRowCapacity, cap(b.cells.cells))
			assert.Equal(t, tt.wantColumnCapacity, cap(b.Row(0)))
			assert.Equal(t, tt.wantRowCapacity, b.cells.rowCap)
			assert.Equal(t, tt.wantColumnCapacity, b.cells.columnCap)
			assert.Equal(t, '.', b.cells.fillInChar)
			assert.Equal(t, ringDotCell(), b.cells.blank)
			assert.Zero(t, b.cells.ringHead)
			assert.Equal(t, []rawRowMeta{{}}, b.cells.rowMeta)
			assert.Nil(t, b.cells.free)
			assert.False(t, b.cells.zwj)
			assert.Equal(t, term.Coordinates{}, b.cells.zwjPos)
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { NewBuffer().ResetPerformanceCapacity(1, 1) })
	})
}

func TestBufferExtendRowToWidthTable(t *testing.T) {
	complex := []term.Cell{
		{}, ringSpaceCell(), ringTabCell(), ringWideCell('漢'),
		ringCombiningCell(), ringStyledCell('z'),
	}
	tests := []struct {
		name      string
		initial   [][]term.Cell
		fill      rune
		head      int
		y, width  int
		spare     int
		wantAdded int
		want      [][]term.Cell
	}{
		{
			name: "nil row", initial: [][]term.Cell{nil, stringToCells("tail")}, fill: ' ',
			y: 0, width: 3, wantAdded: 3,
			want: [][]term.Cell{repeatCell(ringSpaceCell(), 3), stringToCells("tail")},
		},
		{
			name: "empty row", initial: [][]term.Cell{{}, stringToCells("tail")}, fill: '.',
			y: 0, width: 2, wantAdded: 2,
			want: [][]term.Cell{repeatCell(ringDotCell(), 2), stringToCells("tail")},
		},
		{
			name: "zero target preserves nonnil empty", initial: [][]term.Cell{{}}, fill: ' ',
			y: 0, width: 0, want: [][]term.Cell{{}},
		},
		{
			name: "negative target", initial: [][]term.Cell{complex}, fill: ' ',
			y: 0, width: -1, want: [][]term.Cell{complex},
		},
		{
			name: "equal target", initial: [][]term.Cell{complex}, fill: ' ',
			y: 0, width: len(complex), want: [][]term.Cell{complex},
		},
		{
			name: "preserves complex prefix", initial: [][]term.Cell{complex}, fill: ' ',
			y: 0, width: len(complex) + 2, wantAdded: 2,
			want: [][]term.Cell{append(cloneCellRow(complex), ringSpaceCell(), ringSpaceCell())},
		},
		{
			name: "grows within spare capacity", initial: [][]term.Cell{complex[:2]}, fill: '.',
			y: 0, width: 5, spare: 8, wantAdded: 3,
			want: [][]term.Cell{append(cloneCellRow(complex[:2]), ringDotCell(), ringDotCell(), ringDotCell())},
		},
		{
			name: "wide fill", initial: [][]term.Cell{stringToCells("a")}, fill: '漢',
			y: 0, width: 3, wantAdded: 2,
			want: [][]term.Cell{append(stringToCells("a"), ringWideCell('漢'), ringWideCell('漢'))},
		},
		{
			name: "tab fill", initial: [][]term.Cell{nil}, fill: '\t',
			y: 0, width: 2, wantAdded: 2,
			want: [][]term.Cell{repeatCell(measuredFillCell('\t'), 2)},
		},
		{
			name: "zero fill", initial: [][]term.Cell{nil}, fill: 0,
			y: 0, width: 2, wantAdded: 2,
			want: [][]term.Cell{repeatCell(measuredFillCell(0), 2)},
		},
		{
			name: "wrapped logical row", initial: [][]term.Cell{stringToCells("a"), complex, nil},
			fill: ' ', head: 2, y: 1, width: len(complex) + 1, wantAdded: 1,
			want: [][]term.Cell{stringToCells("a"), append(cloneCellRow(complex), ringSpaceCell()), nil},
		},
		{
			name: "negative row", initial: [][]term.Cell{complex}, fill: ' ',
			y: -1, width: 9, want: [][]term.Cell{complex},
		},
		{
			name: "row at end", initial: [][]term.Cell{complex}, fill: ' ',
			y: 1, width: 9, want: [][]term.Cell{complex},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b *Buffer
			if tt.head != 0 {
				b = newWrappedRingTestBuffer(tt.initial, tt.fill, tt.head)
			} else {
				b = newRingTestBuffer(tt.initial, tt.fill)
			}
			if tt.spare > 0 {
				row := make([]term.Cell, len(b.Row(tt.y)), tt.spare)
				copy(row, b.Row(tt.y))
				b.SetRow(tt.y, row)
			}

			added, ok := b.ExtendRowToWidth(tt.y, tt.width)

			require.True(t, ok)
			assert.Equal(t, tt.wantAdded, added)
			assertBufferLogicalRows(t, b, tt.want)
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { _, _ = NewBuffer().ExtendRowToWidth(0, 1) })
	})
}

func TestBufferTrimRowsFromEndTable(t *testing.T) {
	tests := []struct {
		name        string
		initial     [][]term.Cell
		head, count int
		wantRemoved int
		want        [][]term.Cell
	}{
		{name: "negative count", initial: ringComplexRows(), count: -1, want: ringComplexRows()},
		{name: "zero count", initial: ringComplexRows(), count: 0, want: ringComplexRows()},
		{name: "single row", initial: [][]term.Cell{nil}, count: 3, want: [][]term.Cell{nil}},
		{name: "trim one", initial: ringComplexRows(), count: 1, wantRemoved: 1, want: cloneCellRows(ringComplexRows()[:4])},
		{name: "trim through nil and empty", initial: ringComplexRows(), count: 3, wantRemoved: 3, want: cloneCellRows(ringComplexRows()[:2])},
		{name: "keep one", initial: ringComplexRows(), count: 99, wantRemoved: 4, want: cloneCellRows(ringComplexRows()[:1])},
		{name: "wrapped storage", initial: ringComplexRows(), head: 2, count: 2, wantRemoved: 2, want: cloneCellRows(ringComplexRows()[:3])},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer(tt.initial, ' ', tt.head)
			removed, ok := b.TrimRowsFromEnd(tt.count)
			require.True(t, ok)
			assert.Equal(t, tt.wantRemoved, removed)
			assertBufferLogicalRows(t, b, tt.want)
			assert.Zero(t, b.cells.ringHead)
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { _, _ = NewBuffer().TrimRowsFromEnd(1) })
	})
}

func TestBufferTrimRowsFromStartTable(t *testing.T) {
	tests := []struct {
		name        string
		initial     [][]term.Cell
		head, count int
		wantRemoved int
		want        [][]term.Cell
	}{
		{name: "negative count", initial: ringComplexRows(), count: -1, want: ringComplexRows()},
		{name: "zero count", initial: ringComplexRows(), count: 0, want: ringComplexRows()},
		{name: "single empty row", initial: [][]term.Cell{{}}, count: 3, want: [][]term.Cell{{}}},
		{name: "trim nil row", initial: ringComplexRows(), count: 1, wantRemoved: 1, want: cloneCellRows(ringComplexRows()[1:])},
		{name: "trim nil empty and zero cells", initial: ringComplexRows(), count: 3, wantRemoved: 3, want: cloneCellRows(ringComplexRows()[3:])},
		{name: "keep one", initial: ringComplexRows(), count: 99, wantRemoved: 4, want: cloneCellRows(ringComplexRows()[4:])},
		{name: "wrapped storage", initial: ringComplexRows(), head: 3, count: 2, wantRemoved: 2, want: cloneCellRows(ringComplexRows()[2:])},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer(tt.initial, ' ', tt.head)
			removed, ok := b.TrimRowsFromStart(tt.count)
			require.True(t, ok)
			assert.Equal(t, tt.wantRemoved, removed)
			assertBufferLogicalRows(t, b, tt.want)
			assert.Zero(t, b.cells.ringHead)
			assert.Len(t, b.cells.free, tt.wantRemoved)
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { _, _ = NewBuffer().TrimRowsFromStart(1) })
	})
}

func TestBufferAppendBlankRowsTable(t *testing.T) {
	tests := []struct {
		name         string
		initial      [][]term.Cell
		fill         rune
		trim         int
		count, width int
		want         [][]term.Cell
	}{
		{
			name: "appends after nil and empty rows", initial: [][]term.Cell{nil, {}}, fill: ' ',
			count: 2, width: 3,
			want: [][]term.Cell{nil, {}, repeatCell(ringSpaceCell(), 3), repeatCell(ringSpaceCell(), 3)},
		},
		{name: "zero count", initial: ringComplexRows(), fill: ' ', count: 0, width: 2, want: ringComplexRows()},
		{name: "negative count", initial: ringComplexRows(), fill: ' ', count: -1, width: 2, want: ringComplexRows()},
		{name: "negative width", initial: ringComplexRows(), fill: ' ', count: 1, width: -1, want: ringComplexRows()},
		{
			name: "zero width creates nonnil empty row", initial: [][]term.Cell{nil}, fill: ' ',
			count: 1, width: 0, want: [][]term.Cell{nil, {}},
		},
		{
			name: "wide fill", initial: [][]term.Cell{stringToCells("a")}, fill: '漢',
			count: 1, width: 2,
			want: [][]term.Cell{stringToCells("a"), repeatCell(ringWideCell('漢'), 2)},
		},
		{
			name: "tab fill", initial: [][]term.Cell{stringToCells("a")}, fill: '\t',
			count: 1, width: 2,
			want: [][]term.Cell{stringToCells("a"), repeatCell(measuredFillCell('\t'), 2)},
		},
		{
			name: "zero fill", initial: [][]term.Cell{stringToCells("a")}, fill: 0,
			count: 1, width: 2,
			want: [][]term.Cell{stringToCells("a"), repeatCell(measuredFillCell(0), 2)},
		},
		{
			name: "dirty recycled rows are fully cleared",
			initial: [][]term.Cell{
				{ringStyledCell('x'), ringWideCell('漢'), ringCombiningCell(), ringTabCell()},
				stringToCells("keep"),
			},
			fill: ' ', trim: 1, count: 1, width: 4,
			want: [][]term.Cell{stringToCells("keep"), repeatCell(ringSpaceCell(), 4)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newRingTestBuffer(tt.initial, tt.fill)
			if tt.trim > 0 {
				removed, ok := b.TrimRowsFromStart(tt.trim)
				require.True(t, ok)
				require.Equal(t, tt.trim, removed)
			}

			require.True(t, b.AppendBlankRows(tt.count, tt.width))
			assertBufferLogicalRows(t, b, tt.want)
		})
	}

	t.Run("repeated trim append recycling", func(t *testing.T) {
		b := newRingTestBuffer([][]term.Cell{
			{ringStyledCell('a'), ringCombiningCell(), ringWideCell('漢')},
			{ringTabCell(), ringStyledCell('b'), ringSpaceCell()},
			stringToCells("end"),
		}, ' ')
		model := b.CopyRows(nil)
		for round := range 16 {
			removed, ok := b.TrimRowsFromStart(1)
			require.True(t, ok)
			require.Equal(t, 1, removed)
			model = append([][]term.Cell(nil), model[1:]...)
			require.True(t, b.AppendBlankRows(1, 3))
			model = append(model, repeatCell(ringSpaceCell(), 3))
			row := []term.Cell{ringStyledCell(rune('a' + round%26)), ringCombiningCell(), ringWideCell('界')}
			b.SetRow(b.Rows()-1, cloneCellRow(row))
			model[len(model)-1] = cloneCellRow(row)
			assertBufferLogicalRows(t, b, model)
		}
	})

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { _ = NewBuffer().AppendBlankRows(1, 1) })
	})
}

func TestBufferMergeMarkedRowsTable(t *testing.T) {
	styledA := ringStyledCell('a')
	styledB := ringStyledCell('b')
	markedA := markedCell(styledA)
	markedB := markedCell(styledB)
	clearedA := styledA
	clearedA.Bytes = 0
	clearedB := styledB
	clearedB.Bytes = 0
	complexHead := []term.Cell{ringCombiningCell(), markedA}
	complexTail := []term.Cell{ringWideCell('漢'), ringTabCell(), ringStyledCell('z')}

	tests := []struct {
		name       string
		initial    [][]term.Cell
		head, end  int
		wantMerged int
		want       [][]term.Cell
	}{
		{name: "no marks", initial: ringComplexRows(), end: 5, want: ringComplexRows()},
		{
			name: "complex cells preserve integrity", initial: [][]term.Cell{complexHead, complexTail, nil}, end: 3,
			wantMerged: 1,
			want:       [][]term.Cell{{ringCombiningCell(), clearedA, ringWideCell('漢'), ringTabCell(), ringStyledCell('z')}, nil},
		},
		{
			name: "marked head consumes nil row", initial: [][]term.Cell{{markedA}, nil, {styledB}}, end: 3,
			wantMerged: 1, want: [][]term.Cell{{clearedA}, {styledB}},
		},
		{
			name: "marked head consumes nonnil empty row", initial: [][]term.Cell{{markedA}, {}, {styledB}}, end: 3,
			wantMerged: 1, want: [][]term.Cell{{clearedA}, {styledB}},
		},
		{
			name: "group continues beyond end", initial: [][]term.Cell{{styledA}, {markedA}, {markedB}, {ringWideCell('界')}}, end: 2,
			wantMerged: 2,
			want:       [][]term.Cell{{styledA}, {clearedA, clearedB, ringWideCell('界')}},
		},
		{
			name: "two groups", initial: [][]term.Cell{{markedA}, {ringTabCell()}, {markedB}, {ringWideCell('語')}}, end: 4,
			wantMerged: 2,
			want:       [][]term.Cell{{clearedA, ringTabCell()}, {clearedB, ringWideCell('語')}},
		},
		{name: "negative end", initial: [][]term.Cell{{markedA}, {styledB}}, end: -1, want: [][]term.Cell{{markedA}, {styledB}}},
		{name: "zero end", initial: [][]term.Cell{{markedA}, {styledB}}, end: 0, want: [][]term.Cell{{markedA}, {styledB}}},
		{
			name: "end beyond rows", initial: [][]term.Cell{{markedA}, {styledB}}, end: 99,
			wantMerged: 1, want: [][]term.Cell{{clearedA, styledB}},
		},
		{
			name: "wrapped storage is linearized", initial: [][]term.Cell{{markedA}, {styledB}, nil}, head: 2, end: 3,
			wantMerged: 1, want: [][]term.Cell{{clearedA, styledB}, nil},
		},
	}

	isMark := func(cell term.Cell) bool { return cell.Bytes == testMark }
	clearMark := func(cell *term.Cell) { cell.Bytes = 0 }
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer(tt.initial, ' ', tt.head)
			merged, ok := b.MergeMarkedRows(tt.end, isMark, clearMark)
			require.True(t, ok)
			assert.Equal(t, tt.wantMerged, merged)
			assertBufferLogicalRows(t, b, tt.want)
			assert.Zero(t, b.cells.ringHead)
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() {
			_, _ = NewBuffer().MergeMarkedRows(1, isMark, clearMark)
		})
	})
}

func TestBufferSplitRowsBatchTable(t *testing.T) {
	complex := []term.Cell{
		{}, ringSpaceCell(), ringTabCell(), ringWideCell('漢'),
		ringCombiningCell(), ringStyledCell('z'),
	}
	tests := []struct {
		name       string
		initial    [][]term.Cell
		head       int
		splits     []RowSplit
		wantAdded  int
		wantOK     bool
		want       [][]term.Cell
		appendHead bool
	}{
		{name: "nil splits", initial: [][]term.Cell{nil, {}, complex}, splits: nil, wantOK: true, want: [][]term.Cell{nil, {}, complex}},
		{name: "empty splits", initial: [][]term.Cell{nil, {}, complex}, splits: []RowSplit{}, wantOK: true, want: [][]term.Cell{nil, {}, complex}},
		{name: "zero width ignored", initial: [][]term.Cell{complex}, splits: []RowSplit{{Y: 0, Width: 0, Times: 3}}, wantOK: true, want: [][]term.Cell{complex}},
		{name: "zero times ignored", initial: [][]term.Cell{complex}, splits: []RowSplit{{Y: 0, Width: 2}}, wantOK: true, want: [][]term.Cell{complex}},
		{
			name: "complex cells split exactly", initial: [][]term.Cell{complex},
			splits: []RowSplit{{Y: 0, Width: 2, Times: 2}}, wantAdded: 2, wantOK: true,
			want: [][]term.Cell{complex[:2], complex[2:4], complex[4:]}, appendHead: true,
		},
		{
			name: "exact consumption leaves empty tail", initial: [][]term.Cell{complex},
			splits: []RowSplit{{Y: 0, Width: 3, Times: 2}}, wantAdded: 2, wantOK: true,
			want: [][]term.Cell{complex[:3], complex[3:6], {}},
		},
		{
			name:      "multiple increasing splits preserve nil and empty rows",
			initial:   [][]term.Cell{complex[:4], nil, complex[2:], {}, stringToCells("tail")},
			splits:    []RowSplit{{Y: 0, Width: 2, Times: 1}, {Y: 2, Width: 2, Times: 1}},
			wantAdded: 2, wantOK: true,
			want: [][]term.Cell{complex[:2], complex[2:4], nil, complex[2:4], complex[4:], {}, stringToCells("tail")},
		},
		{
			name: "wrapped logical indexes", initial: [][]term.Cell{stringToCells("keep"), complex, nil}, head: 2,
			splits: []RowSplit{{Y: 1, Width: 2, Times: 1}}, wantAdded: 1, wantOK: true,
			want: [][]term.Cell{stringToCells("keep"), complex[:2], complex[2:], nil},
		},
		{name: "duplicate row rejected", initial: [][]term.Cell{complex}, splits: []RowSplit{{Y: 0, Width: 1, Times: 1}, {Y: 0, Width: 1, Times: 1}}, want: [][]term.Cell{complex}},
		{name: "descending rows rejected", initial: [][]term.Cell{complex, complex}, splits: []RowSplit{{Y: 1, Width: 1, Times: 1}, {Y: 0, Width: 1, Times: 1}}, want: [][]term.Cell{complex, complex}},
		{name: "negative row rejected", initial: [][]term.Cell{complex}, splits: []RowSplit{{Y: -1, Width: 1, Times: 1}}, want: [][]term.Cell{complex}},
		{name: "row at end rejected", initial: [][]term.Cell{complex}, splits: []RowSplit{{Y: 1, Width: 1, Times: 1}}, want: [][]term.Cell{complex}},
		{name: "split past row rejected", initial: [][]term.Cell{complex}, splits: []RowSplit{{Y: 0, Width: 4, Times: 2}}, want: [][]term.Cell{complex}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer(tt.initial, ' ', tt.head)
			added, ok := b.SplitRowsBatch(tt.splits)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantAdded, added)
			assertBufferLogicalRows(t, b, tt.want)
			if tt.appendHead {
				b.SetRow(0, append(b.Row(0), ringStyledCell('q')))
				assert.Equal(t, complex[2:4], b.Row(1))
			}
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() {
			_, _ = NewBuffer().SplitRowsBatch([]RowSplit{{Y: 0, Width: 1, Times: 1}})
		})
	})
}

func TestBufferSplitRowsBatchPaddedTable(t *testing.T) {
	complex := []term.Cell{
		{}, ringSpaceCell(), ringTabCell(), ringWideCell('漢'),
		ringCombiningCell(), ringStyledCell('z'),
	}
	tests := []struct {
		name       string
		initial    [][]term.Cell
		head       int
		splits     []RowSplit
		padToWidth int
		fill       term.Cell
		wantAdded  int
		wantOK     bool
		want       [][]term.Cell
	}{
		{name: "nil splits", initial: [][]term.Cell{nil, {}, complex}, wantOK: true, want: [][]term.Cell{nil, {}, complex}},
		{
			name: "space pads complex tail", initial: [][]term.Cell{complex},
			splits: []RowSplit{{Y: 0, Width: 4, Times: 1}}, padToWidth: 4, fill: ringSpaceCell(),
			wantAdded: 1, wantOK: true,
			want: [][]term.Cell{complex[:4], {complex[4], complex[5], ringSpaceCell(), ringSpaceCell()}},
		},
		{
			name: "wide fill is copied verbatim", initial: [][]term.Cell{complex[:3]},
			splits: []RowSplit{{Y: 0, Width: 2, Times: 1}}, padToWidth: 3, fill: ringWideCell('漢'),
			wantAdded: 1, wantOK: true,
			want: [][]term.Cell{complex[:2], {complex[2], ringWideCell('漢'), ringWideCell('漢')}},
		},
		{
			name: "tab fill is copied verbatim", initial: [][]term.Cell{complex[:3]},
			splits: []RowSplit{{Y: 0, Width: 2, Times: 1}}, padToWidth: 3, fill: measuredFillCell('\t'),
			wantAdded: 1, wantOK: true,
			want: [][]term.Cell{complex[:2], {complex[2], measuredFillCell('\t'), measuredFillCell('\t')}},
		},
		{
			name: "zero fill is copied verbatim", initial: [][]term.Cell{complex[:3]},
			splits: []RowSplit{{Y: 0, Width: 2, Times: 1}}, padToWidth: 3, fill: measuredFillCell(0),
			wantAdded: 1, wantOK: true,
			want: [][]term.Cell{complex[:2], {complex[2], measuredFillCell(0), measuredFillCell(0)}},
		},
		{
			name: "exact tail remains untouched", initial: [][]term.Cell{complex},
			splits: []RowSplit{{Y: 0, Width: 3, Times: 1}}, padToWidth: 3, fill: ringDotCell(),
			wantAdded: 1, wantOK: true, want: [][]term.Cell{complex[:3], complex[3:]},
		},
		{
			name: "pad below tail remains untouched", initial: [][]term.Cell{complex},
			splits: []RowSplit{{Y: 0, Width: 2, Times: 1}}, padToWidth: 3, fill: ringDotCell(),
			wantAdded: 1, wantOK: true, want: [][]term.Cell{complex[:2], complex[2:]},
		},
		{
			name: "zero padding matches unpadded", initial: [][]term.Cell{complex[:5]},
			splits: []RowSplit{{Y: 0, Width: 3, Times: 1}}, fill: ringDotCell(),
			wantAdded: 1, wantOK: true, want: [][]term.Cell{complex[:3], complex[3:5]},
		},
		{
			name: "empty tail is fully padded", initial: [][]term.Cell{complex},
			splits: []RowSplit{{Y: 0, Width: 3, Times: 2}}, padToWidth: 2, fill: ringDotCell(),
			wantAdded: 2, wantOK: true,
			want: [][]term.Cell{complex[:3], complex[3:6], {ringDotCell(), ringDotCell()}},
		},
		{
			name:    "multiple padded tails and wrapped storage",
			initial: [][]term.Cell{complex[:3], nil, complex[2:5]}, head: 2,
			splits:     []RowSplit{{Y: 0, Width: 2, Times: 1}, {Y: 2, Width: 2, Times: 1}},
			padToWidth: 3, fill: ringDotCell(), wantAdded: 2, wantOK: true,
			want: [][]term.Cell{
				complex[:2], {complex[2], ringDotCell(), ringDotCell()}, nil,
				complex[2:4], {complex[4], ringDotCell(), ringDotCell()},
			},
		},
		{name: "negative pad rejected", initial: [][]term.Cell{complex}, splits: []RowSplit{{Y: 0, Width: 2, Times: 1}}, padToWidth: -1, fill: ringDotCell(), want: [][]term.Cell{complex}},
		{name: "invalid split rejected", initial: [][]term.Cell{complex}, splits: []RowSplit{{Y: 0, Width: 4, Times: 2}}, padToWidth: 3, fill: ringDotCell(), want: [][]term.Cell{complex}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer(tt.initial, ' ', tt.head)
			added, ok := b.SplitRowsBatchPadded(tt.splits, tt.padToWidth, tt.fill)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantAdded, added)
			assertBufferLogicalRows(t, b, tt.want)
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() {
			_, _ = NewBuffer().SplitRowsBatchPadded(
				[]RowSplit{{Y: 0, Width: 1, Times: 1}}, 2, ringSpaceCell(),
			)
		})
	})
}

func measuredFillCell(ch rune) term.Cell {
	str := string(ch)
	return term.Cell{
		Ch: ch, Width: uint8(graphemecluster.StringWidth(str)), Bytes: uint8(len(str)),
	}
}

func markedCell(cell term.Cell) term.Cell {
	cell.Bytes = testMark
	return cell
}

func TestBufferRowTable(t *testing.T) {
	complex := ringComplexRows()
	reversed := cloneCellRows(complex)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}

	tests := []struct {
		name     string
		rows     [][]term.Cell
		rotate   int
		viewRows [][]term.Cell
		y        int
		want     []term.Cell
	}{
		{name: "nil row", rows: complex, y: 0, want: nil},
		{name: "empty row", rows: complex, y: 1, want: []term.Cell{}},
		{name: "zero and space cells", rows: complex, y: 2, want: complex[2]},
		{name: "tab and wide cells", rows: complex, y: 3, want: complex[3]},
		{name: "combining and styled cells", rows: complex, y: 4, want: complex[4]},
		{name: "negative row", rows: complex, y: -1, want: nil},
		{name: "row at end", rows: complex, y: len(complex), want: nil},
		{
			name: "logical row after whole rotation", rows: complex, rotate: 2,
			y: 0, want: complex[2],
		},
		{
			name: "logical row across physical boundary", rows: complex, rotate: -1,
			y: 1, want: complex[0],
		},
		{
			name: "custom view order", rows: complex, viewRows: reversed,
			y: 0, want: reversed[0],
		},
		{
			name: "custom view out of bounds", rows: complex, viewRows: reversed,
			y: len(reversed), want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newRingTestBuffer(tt.rows, ' ')
			if tt.rotate != 0 {
				b.RotateRows(0, b.Rows(), tt.rotate)
				require.NotZero(t, b.cells.ringHead)
			}
			if tt.viewRows != nil {
				b.WithView(ringTestView{rows: cloneCellRows(tt.viewRows)})
			}

			got := b.Row(tt.y)
			assert.Equal(t, tt.want, got)
			if tt.want != nil && len(tt.want) == 0 {
				assert.NotNil(t, got)
			}
		})
	}
}

func TestBufferMutableRowTable(t *testing.T) {
	tests := []struct {
		name         string
		y            int
		end          int
		wantRow      []term.Cell
		wantOccupied int
		mutate       bool
	}{
		{name: "negative row", y: -1, end: 1},
		{name: "row at end", y: 3, end: 1},
		{name: "negative occupied end", y: 0, end: -4, wantRow: stringToCells("a")},
		{name: "zero occupied end", y: 0, end: 0, wantRow: stringToCells("a")},
		{name: "occupied prefix", y: 1, end: 1, wantRow: stringToCells("bb"), wantOccupied: 1},
		{name: "occupied end beyond row", y: 2, end: 9, wantRow: stringToCells("ccc"), wantOccupied: 9},
		{
			name: "mutation targets logical row", y: 0, end: 1,
			wantRow: stringToCells("a"), wantOccupied: 1, mutate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer([][]term.Cell{
				stringToCells("a"), stringToCells("bb"), stringToCells("ccc"),
			}, ' ', 1)
			b.cells.rowMeta = make([]rawRowMeta, b.Rows())
			require.Equal(t, 1, b.cells.ringHead)

			got := b.MutableRow(tt.y, tt.end)
			assert.Equal(t, tt.wantRow, got)
			if tt.y < 0 || tt.y >= b.Rows() {
				return
			}
			physical := b.cells.physicalRow(tt.y)
			assert.Equal(t, tt.wantOccupied, b.cells.rowMeta[physical].occupied)
			if tt.mutate {
				got[0].Ch = 'x'
				assert.Equal(t, 'x', b.Row(tt.y)[0].Ch)
				assert.Equal(t, "bb", stringRow(b.Row(1)))
				assert.Equal(t, "ccc", stringRow(b.Row(2)))
			}
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { NewBuffer().MutableRow(0, 1) })
	})
}

func TestBufferSetRowTable(t *testing.T) {
	wide := ringWideCell('界')
	combining := ringCombiningCell()
	tests := []struct {
		name        string
		y           int
		replacement []term.Cell
		want        [][]term.Cell
	}{
		{
			name: "replace first logical row after rotation", y: 0,
			replacement: []term.Cell{wide, combining},
			want:        [][]term.Cell{{wide, combining}, stringToCells("b"), stringToCells("c")},
		},
		{
			name: "replace with nil row", y: 1, replacement: nil,
			want: [][]term.Cell{stringToCells("a"), nil, stringToCells("c")},
		},
		{
			name: "replace with empty row", y: 2, replacement: []term.Cell{},
			want: [][]term.Cell{stringToCells("a"), stringToCells("b"), {}},
		},
		{
			name: "negative row is ignored", y: -1, replacement: []term.Cell{wide},
			want: [][]term.Cell{stringToCells("a"), stringToCells("b"), stringToCells("c")},
		},
		{
			name: "row at end is ignored", y: 3, replacement: []term.Cell{wide},
			want: [][]term.Cell{stringToCells("a"), stringToCells("b"), stringToCells("c")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer([][]term.Cell{
				stringToCells("a"), stringToCells("b"), stringToCells("c"),
			}, ' ', 1)
			require.Equal(t, 1, b.cells.ringHead)

			b.SetRow(tt.y, cloneCellRow(tt.replacement))
			assertBufferLogicalRows(t, b, tt.want)
			assert.Equal(t, 1, b.cells.ringHead)
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { NewBuffer().SetRow(0, nil) })
	})
}

func TestBufferDeleteRowRangeTable(t *testing.T) {
	zero := term.Cell{}
	space := ringSpaceCell()
	tab := ringTabCell()
	wide := ringWideCell('漢')
	combining := ringCombiningCell()
	styled := ringStyledCell('z')
	row := []term.Cell{zero, space, tab, wide, combining, styled}

	tests := []struct {
		name       string
		y          int
		start, end int
		want       [][]term.Cell
	}{
		{name: "delete middle", y: 0, start: 2, end: 4, want: [][]term.Cell{{zero, space, combining, styled}, stringToCells("tail")}},
		{name: "delete wide and combining", y: 0, start: 3, end: 5, want: [][]term.Cell{{zero, space, tab, styled}, stringToCells("tail")}},
		{name: "delete whole row", y: 0, start: 0, end: len(row), want: [][]term.Cell{{}, stringToCells("tail")}},
		{name: "end is clipped", y: 0, start: 4, end: 99, want: [][]term.Cell{{zero, space, tab, wide}, stringToCells("tail")}},
		{name: "empty range", y: 0, start: 2, end: 2, want: [][]term.Cell{row, stringToCells("tail")}},
		{name: "reversed range", y: 0, start: 4, end: 2, want: [][]term.Cell{row, stringToCells("tail")}},
		{name: "negative start", y: 0, start: -1, end: 2, want: [][]term.Cell{row, stringToCells("tail")}},
		{name: "start past row", y: 0, start: 9, end: 10, want: [][]term.Cell{row, stringToCells("tail")}},
		{name: "negative row", y: -1, start: 0, end: 1, want: [][]term.Cell{row, stringToCells("tail")}},
		{name: "row at end", y: 2, start: 0, end: 1, want: [][]term.Cell{row, stringToCells("tail")}},
		{name: "nil row", y: 0, start: 0, end: 1, want: [][]term.Cell{nil, stringToCells("tail")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := row
			if tt.name == "nil row" {
				first = nil
			}
			b := newWrappedRingTestBuffer(
				[][]term.Cell{first, stringToCells("tail")}, ' ', 1,
			)
			require.NotZero(t, b.cells.ringHead)

			assert.NotPanics(t, func() { b.DeleteRowRange(tt.y, tt.start, tt.end) })
			assertBufferLogicalRows(t, b, tt.want)
		})
	}

	for _, y := range []int{-1, 2, 99} {
		t.Run("invalid row "+strconv.Itoa(y)+" with zero ring head", func(t *testing.T) {
			b := newRingTestBuffer([][]term.Cell{row, stringToCells("tail")}, ' ')
			assert.NotPanics(t, func() { b.DeleteRowRange(y, 0, 1) })
			assertBufferLogicalRows(t, b, [][]term.Cell{row, stringToCells("tail")})
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { NewBuffer().DeleteRowRange(0, 0, 1) })
	})
}

func TestBufferCopyRowsTable(t *testing.T) {
	complex := ringComplexRows()
	reversed := cloneCellRows(complex)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}

	tests := []struct {
		name     string
		rows     [][]term.Cell
		rotate   int
		dst      [][]term.Cell
		viewRows [][]term.Cell
		want     [][]term.Cell
	}{
		{name: "nil destination", rows: complex, rotate: 2, want: rotateCellRows(complex, 0, len(complex), 2)},
		{name: "short destination", rows: complex, dst: make([][]term.Cell, 1), want: complex},
		{name: "long destination is truncated", rows: complex, dst: make([][]term.Cell, len(complex)+3), want: complex},
		{
			name: "reuses rows with capacity", rows: complex,
			dst:  [][]term.Cell{make([]term.Cell, 0, 8), nil, make([]term.Cell, 0, 8)},
			want: complex,
		},
		{name: "custom view order", rows: complex, viewRows: reversed, want: reversed},
		{
			name: "custom view can expose more rows", rows: [][]term.Cell{stringToCells("internal")},
			viewRows: complex, want: complex,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newRingTestBuffer(tt.rows, ' ')
			if tt.rotate != 0 {
				b.RotateRows(0, b.Rows(), tt.rotate)
			}
			if tt.viewRows != nil {
				b.WithView(ringTestView{rows: cloneCellRows(tt.viewRows)})
			}

			var got [][]term.Cell
			assert.NotPanics(t, func() { got = b.CopyRows(tt.dst) })
			assert.Equal(t, tt.want, got)
			if len(got) > 0 && len(got[0]) > 0 {
				original := b.Row(0)[0]
				got[0][0].Ch = 'X'
				assert.Equal(t, original, b.Row(0)[0], "CopyRows must not alias row storage")
			}
		})
	}

	t.Run("works in normal mode", func(t *testing.T) {
		b := NewBuffer()
		b.ResetCells(complex)
		want := make([][]term.Cell, b.Rows())
		for y := range want {
			want[y] = cloneCellRow(b.Row(y))
		}
		assert.Equal(t, want, b.CopyRows(nil))
	})
}

func TestBufferResetRowRangeTable(t *testing.T) {
	row := []term.Cell{
		{}, ringSpaceCell(), ringTabCell(), ringWideCell('界'),
		ringCombiningCell(), ringStyledCell('z'),
	}
	space := ringSpaceCell()
	tab := ringTabCell()
	wide := ringWideCell('語')
	styled := ringStyledCell('q')

	tests := []struct {
		name       string
		y          int
		start, end int
		fill       term.Cell
		want       [][]term.Cell
	}{
		{name: "partial space reset", y: 0, start: 1, end: 3, fill: space, want: [][]term.Cell{{row[0], space, space, row[3], row[4], row[5]}, nil}},
		{name: "partial tab reset", y: 0, start: 2, end: 5, fill: tab, want: [][]term.Cell{{row[0], row[1], tab, tab, tab, row[5]}, nil}},
		{name: "full wide reset", y: 0, start: 0, end: len(row), fill: wide, want: [][]term.Cell{repeatCell(wide, len(row)), nil}},
		{name: "styled fill", y: 0, start: 4, end: 6, fill: styled, want: [][]term.Cell{{row[0], row[1], row[2], row[3], styled, styled}, nil}},
		{name: "zero fill", y: 0, start: 0, end: 2, fill: term.Cell{}, want: [][]term.Cell{{{}, {}, row[2], row[3], row[4], row[5]}, nil}},
		{name: "end clipped", y: 0, start: 4, end: 99, fill: space, want: [][]term.Cell{{row[0], row[1], row[2], row[3], space, space}, nil}},
		{name: "negative row", y: -1, start: 0, end: 1, fill: space, want: [][]term.Cell{row, nil}},
		{name: "row at end", y: 2, start: 0, end: 1, fill: space, want: [][]term.Cell{row, nil}},
		{name: "negative start", y: 0, start: -1, end: 2, fill: space, want: [][]term.Cell{row, nil}},
		{name: "empty range", y: 0, start: 2, end: 2, fill: space, want: [][]term.Cell{row, nil}},
		{name: "reversed range", y: 0, start: 4, end: 2, fill: space, want: [][]term.Cell{row, nil}},
		{name: "start past row", y: 0, start: 9, end: 10, fill: space, want: [][]term.Cell{row, nil}},
		{name: "nil row", y: 1, start: 0, end: 1, fill: space, want: [][]term.Cell{row, nil}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newWrappedRingTestBuffer([][]term.Cell{row, nil}, ' ', 1)
			require.NotZero(t, b.cells.ringHead)

			b.ResetRowRange(tt.y, tt.start, tt.end, tt.fill)
			assertBufferLogicalRows(t, b, tt.want)
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { NewBuffer().ResetRowRange(0, 0, 1, space) })
	})
}

func TestBufferRotateRowsTable(t *testing.T) {
	tests := []struct {
		name                   string
		start, end, count      int
		initialRotation        int
		wantNonzeroRingHead    bool
		wantNormalizedRingHead bool
	}{
		{name: "whole left one", start: 0, end: 5, count: 1, wantNonzeroRingHead: true},
		{name: "whole right one", start: 0, end: 5, count: -1, wantNonzeroRingHead: true},
		{name: "whole large positive", start: 0, end: 5, count: 12, wantNonzeroRingHead: true},
		{name: "whole large negative", start: 0, end: 5, count: -12, wantNonzeroRingHead: true},
		{name: "whole multiple is no-op", start: 0, end: 5, count: 10},
		{name: "zero count", start: 0, end: 5, count: 0},
		{name: "partial left one", start: 1, end: 5, count: 1, wantNormalizedRingHead: true},
		{name: "partial right one", start: 0, end: 4, count: -1, wantNormalizedRingHead: true},
		{name: "partial general rotation", start: 0, end: 5, count: 2, initialRotation: 1, wantNonzeroRingHead: true},
		{name: "partial after wrapped storage", start: 1, end: 5, count: 2, initialRotation: 2, wantNormalizedRingHead: true},
		{name: "clamps negative start", start: -3, end: 4, count: 1, wantNormalizedRingHead: true},
		{name: "clamps oversized end", start: 1, end: 99, count: -1, wantNormalizedRingHead: true},
		{name: "start past end", start: 4, end: 2, count: 1},
		{name: "range before rows", start: -4, end: -1, count: 1},
		{name: "range after rows", start: 7, end: 9, count: 1},
		{name: "single row range", start: 2, end: 3, count: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := ringComplexRows()
			b := newRingTestBuffer(rows, ' ')
			meta := make([]rawRowMeta, len(rows))
			for i := range meta {
				meta[i] = rawRowMeta{occupied: i, blank: term.Cell{Ch: rune('A' + i)}}
			}
			b.cells.rowMeta = append([]rawRowMeta(nil), meta...)

			wantRows := cloneCellRows(rows)
			wantMeta := append([]rawRowMeta(nil), meta...)
			if tt.initialRotation != 0 {
				b.RotateRows(0, b.Rows(), tt.initialRotation)
				wantRows = rotateCellRows(wantRows, 0, len(wantRows), tt.initialRotation)
				wantMeta = rotateRowMeta(wantMeta, 0, len(wantMeta), tt.initialRotation)
			}

			b.RotateRows(tt.start, tt.end, tt.count)
			wantRows = rotateCellRows(wantRows, tt.start, tt.end, tt.count)
			wantMeta = rotateRowMeta(wantMeta, tt.start, tt.end, tt.count)

			assertBufferLogicalRows(t, b, wantRows)
			assert.Equal(t, wantMeta, logicalRowMeta(b))
			if tt.wantNonzeroRingHead {
				assert.NotZero(t, b.cells.ringHead)
			}
			if tt.wantNormalizedRingHead {
				assert.Zero(t, b.cells.ringHead)
			}
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { NewBuffer().RotateRows(0, 1, 1) })
	})
}

func TestBufferAppendBlankRowsBoundedTable(t *testing.T) {
	tests := []struct {
		name                string
		initial             [][]term.Cell
		fill                rune
		count, width, limit int
		want                [][]term.Cell
		wantWrapped         bool
	}{
		{
			name: "grows below limit", initial: [][]term.Cell{nil}, fill: ' ',
			count: 2, width: 2, limit: 4,
			want: [][]term.Cell{nil, repeatCell(ringSpaceCell(), 2), repeatCell(ringSpaceCell(), 2)},
		},
		{
			name: "recycles full buffer without normalization", initial: [][]term.Cell{stringToCells("a"), stringToCells("b"), stringToCells("c")}, fill: ' ',
			count: 2, width: 1, limit: 3,
			want: [][]term.Cell{stringToCells("c"), {ringSpaceCell()}, {ringSpaceCell()}}, wantWrapped: true,
		},
		{
			name: "count larger than limit", initial: [][]term.Cell{stringToCells("old")}, fill: '.',
			count: 7, width: 3, limit: 3,
			want: [][]term.Cell{repeatCell(ringDotCell(), 3), repeatCell(ringDotCell(), 3), repeatCell(ringDotCell(), 3)}, wantWrapped: true,
		},
		{
			name: "trims initial rows over limit", initial: [][]term.Cell{stringToCells("a"), stringToCells("b"), stringToCells("c"), stringToCells("d")}, fill: ' ',
			count: 1, width: 1, limit: 2,
			want: [][]term.Cell{stringToCells("d"), {ringSpaceCell()}}, wantWrapped: true,
		},
		{
			name: "limit one", initial: [][]term.Cell{{ringWideCell('漢'), ringCombiningCell()}}, fill: ' ',
			count: 3, width: 4, limit: 1,
			want: [][]term.Cell{repeatCell(ringSpaceCell(), 4)},
		},
		{
			name: "zero width preserves empty row", initial: [][]term.Cell{stringToCells("a"), stringToCells("b")}, fill: ' ',
			count: 1, width: 0, limit: 2,
			want: [][]term.Cell{stringToCells("b"), {}}, wantWrapped: true,
		},
		{
			name: "wide fill cell", initial: [][]term.Cell{stringToCells("a")}, fill: '漢',
			count: 1, width: 2, limit: 1,
			want: [][]term.Cell{repeatCell(ringWideCell('漢'), 2)},
		},
		{
			name: "zero fill cell", initial: [][]term.Cell{stringToCells("a")}, fill: '\x00',
			count: 1, width: 2, limit: 1,
			want: [][]term.Cell{repeatCell(term.Cell{Ch: '\x00', Bytes: 1}, 2)},
		},
		{name: "zero count", initial: ringComplexRows(), fill: ' ', count: 0, width: 2, limit: 3, want: ringComplexRows()},
		{name: "negative count", initial: ringComplexRows(), fill: ' ', count: -1, width: 2, limit: 3, want: ringComplexRows()},
		{name: "negative width", initial: ringComplexRows(), fill: ' ', count: 1, width: -1, limit: 3, want: ringComplexRows()},
		{name: "zero limit", initial: ringComplexRows(), fill: ' ', count: 1, width: 2, limit: 0, want: ringComplexRows()},
		{name: "negative limit", initial: ringComplexRows(), fill: ' ', count: 1, width: 2, limit: -1, want: ringComplexRows()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newRingTestBuffer(tt.initial, tt.fill)
			require.True(t, b.AppendBlankRowsBounded(tt.count, tt.width, tt.limit))
			assertBufferLogicalRows(t, b, tt.want)
			if tt.wantWrapped && b.Rows() > 1 {
				assert.NotZero(t, b.cells.ringHead)
			}
		})
	}

	t.Run("panics outside performance mode", func(t *testing.T) {
		assert.Panics(t, func() { NewBuffer().AppendBlankRowsBounded(1, 1, 1) })
	})
}

type ringTestView struct {
	rows [][]term.Cell
}

func (v ringTestView) Rows() int { return len(v.rows) }

func (v ringTestView) Columns(y int) int {
	if y < 0 || y >= len(v.rows) {
		return 0
	}
	return len(v.rows[y])
}

func (v ringTestView) Cell(pos term.Coordinates) (term.Cell, bool) {
	if pos.Y < 0 || pos.Y >= len(v.rows) || pos.X < 0 || pos.X >= len(v.rows[pos.Y]) {
		return term.Cell{}, false
	}
	return v.rows[pos.Y][pos.X], true
}

func (v ringTestView) RawCells() [][]term.Cell { return v.rows }

func (v ringTestView) String() string { return term.CellsToString(v.rows) }

func newRingTestBuffer(rows [][]term.Cell, fill rune) *Buffer {
	if len(rows) == 0 {
		panic("newRingTestBuffer requires at least one row")
	}
	b := new(Buffer)
	b.InitPerformance(len(rows), 8, fill)
	b.cells.adoptCells(cloneCellRows(rows))
	return b
}

func newWrappedRingTestBuffer(rows [][]term.Cell, fill rune, head int) *Buffer {
	b := newRingTestBuffer(rows, fill)
	if len(rows) <= 1 {
		return b
	}
	head %= len(rows)
	if head < 0 {
		head += len(rows)
	}
	physicalRows := make([][]term.Cell, len(rows))
	physicalMeta := make([]rawRowMeta, len(rows))
	for y := range rows {
		physical := (head + y) % len(rows)
		physicalRows[physical] = b.cells.cells[y]
		physicalMeta[physical] = b.cells.rowMeta[y]
	}
	b.cells.cells = physicalRows
	b.cells.rowMeta = physicalMeta
	b.cells.ringHead = head
	return b
}

func ringComplexRows() [][]term.Cell {
	return [][]term.Cell{
		nil,
		{},
		{{}, ringSpaceCell()},
		{ringTabCell(), ringWideCell('漢')},
		{ringCombiningCell(), ringStyledCell('z')},
	}
}

func ringSpaceCell() term.Cell {
	return term.Cell{Ch: ' ', Width: 1, Bytes: 1}
}

func ringDotCell() term.Cell {
	return term.Cell{Ch: '.', Width: 1, Bytes: 1}
}

func ringTabCell() term.Cell {
	return term.Cell{Ch: '\t', Width: 4, Bytes: 1}
}

func ringWideCell(ch rune) term.Cell {
	return term.Cell{Ch: ch, Width: 2, Bytes: uint8(len(string(ch)))}
}

func ringCombiningCell() term.Cell {
	cell := term.Cell{Ch: 'e', Width: 1, Bytes: 3}
	cell.SetCombining([]rune{'\u0301'})
	return cell
}

func ringStyledCell(ch rune) term.Cell {
	return term.Cell{
		Ch: ch, Width: 1, Bytes: uint8(len(string(ch))),
		Fg: term.ColorRed, Bg: term.ColorBlue, Attrs: term.AttrBold | term.AttrUnderline,
	}
}

func cloneCellRows(rows [][]term.Cell) [][]term.Cell {
	cloned := make([][]term.Cell, len(rows))
	for i, row := range rows {
		cloned[i] = cloneCellRow(row)
	}
	return cloned
}

func cloneCellRow(row []term.Cell) []term.Cell {
	if row == nil {
		return nil
	}
	cloned := make([]term.Cell, len(row))
	copy(cloned, row)
	for i := range cloned {
		if combining := row[i].CombiningRunes(); combining != nil {
			cloned[i].SetCombining(append([]rune(nil), combining...))
		}
	}
	return cloned
}

func repeatCell(cell term.Cell, count int) []term.Cell {
	row := make([]term.Cell, count)
	for i := range row {
		row[i] = cell
	}
	return row
}

func assertBufferLogicalRows(t *testing.T, b *Buffer, want [][]term.Cell) {
	t.Helper()
	require.Equal(t, len(want), b.Rows())
	require.NotEmpty(t, b.cells.cells)
	assert.GreaterOrEqual(t, b.cells.ringHead, 0)
	assert.Less(t, b.cells.ringHead, len(b.cells.cells))
	assert.True(t, len(b.cells.rowMeta) == 0 || len(b.cells.rowMeta) == len(b.cells.cells))
	for y, wantRow := range want {
		gotRow := b.Row(y)
		assert.Equalf(t, wantRow, gotRow, "row %d", y)
		if wantRow != nil && len(wantRow) == 0 {
			assert.NotNilf(t, gotRow, "row %d", y)
		}
		assert.Equalf(t, len(wantRow), b.Columns(y), "columns at row %d", y)
		for x, wantCell := range wantRow {
			gotCell, ok := b.Cell(term.Coordinates{X: x, Y: y})
			require.Truef(t, ok, "cell %d,%d", x, y)
			assert.Equalf(t, wantCell, gotCell, "cell %d,%d", x, y)
		}
	}
}

func rotateCellRows(rows [][]term.Cell, start, end, count int) [][]term.Cell {
	ret := cloneCellRows(rows)
	rotateSlice(ret, start, end, count)
	return ret
}

func rotateRowMeta(meta []rawRowMeta, start, end, count int) []rawRowMeta {
	ret := append([]rawRowMeta(nil), meta...)
	rotateSlice(ret, start, end, count)
	return ret
}

func rotateSlice[T any](values []T, start, end, count int) {
	start = max(0, start)
	end = min(end, len(values))
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
	segment := append([]T(nil), values[start:end]...)
	copy(values[start:end], append(segment[count:], segment[:count]...))
}

func logicalRowMeta(b *Buffer) []rawRowMeta {
	meta := make([]rawRowMeta, b.Rows())
	for y := range meta {
		meta[y] = b.cells.rowMeta[b.cells.physicalRow(y)]
	}
	return meta
}

func TestBufferLinearizeThenRingTable(t *testing.T) {
	tests := []struct {
		name      string
		initial   [][]term.Cell
		linearize func(*testing.T, *Buffer, *[][]term.Cell, int)
	}{
		{
			name:    "generic edit",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, _ *[][]term.Cell, _ int) {
				t.Helper()
				from := term.Coordinates{}
				gotFrom, gotTo, old := b.Edit(context.Background(), from, from, "")
				assert.Equal(t, from, gotFrom)
				assert.Equal(t, from, gotTo)
				assert.Empty(t, old)
			},
		},
		{
			name:    "raw matrix access",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, round int) {
				t.Helper()
				rows := b.RawCells()
				require.Equal(t, *model, rows)
				if y, ok := firstRowWithCells(*model, 1); ok {
					rows[y][0].Bg = term.Color(round + 1)
					(*model)[y][0].Bg = term.Color(round + 1)
				}
			},
		},
		{
			name:    "wrap and conflate",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, _ int) {
				t.Helper()
				y, ok := firstRowWithCells(*model, 2)
				require.True(t, ok)
				at := len((*model)[y]) / 2
				require.True(t, b.WrapRow(y, at))
				x, ok := b.ConflateRow(y)
				require.True(t, ok)
				assert.Equal(t, at, x)
			},
		},
		{
			name:    "split and conflate",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, _ int) {
				t.Helper()
				y, ok := firstRowWithCells(*model, 2)
				require.True(t, ok)
				width := len((*model)[y]) / 2
				added, ok := b.SplitRowsBatch([]RowSplit{{Y: y, Width: width, Times: 1}})
				require.True(t, ok)
				require.Equal(t, 1, added)
				_, ok = b.ConflateRow(y)
				require.True(t, ok)
			},
		},
		{
			name:    "padded split and conflate",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, _ int) {
				t.Helper()
				y, ok := firstRowWithCells(*model, 2)
				require.True(t, ok)
				width := len((*model)[y]) / 2
				tailWidth := len((*model)[y]) - width
				added, ok := b.SplitRowsBatchPadded(
					[]RowSplit{{Y: y, Width: width, Times: 1}}, tailWidth, ringDotCell(),
				)
				require.True(t, ok)
				require.Equal(t, 1, added)
				_, ok = b.ConflateRow(y)
				require.True(t, ok)
			},
		},
		{
			name:    "marked merge round trip",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, _ int) {
				t.Helper()
				y, ok := firstRowWithCells(*model, 2)
				require.True(t, ok)
				at := len((*model)[y]) / 2
				require.True(t, b.WrapRow(y, at))
				head := b.MutableRow(y, at)
				head[len(head)-1].Bytes = testMark
				(*model)[y][at-1].Bytes = 0
				merged, ok := b.MergeMarkedRows(
					y+1,
					func(cell term.Cell) bool { return cell.Bytes == testMark },
					func(cell *term.Cell) { cell.Bytes = 0 },
				)
				require.True(t, ok)
				assert.Equal(t, 1, merged)
			},
		},
		{
			name:    "trim start",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, _ int) {
				t.Helper()
				if len(*model) <= 1 {
					return
				}
				removed, ok := b.TrimRowsFromStart(1)
				require.True(t, ok)
				require.Equal(t, 1, removed)
				*model = append([][]term.Cell(nil), (*model)[1:]...)
			},
		},
		{
			name:    "trim end",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, _ int) {
				t.Helper()
				if len(*model) <= 1 {
					return
				}
				removed, ok := b.TrimRowsFromEnd(1)
				require.True(t, ok)
				require.Equal(t, 1, removed)
				*model = (*model)[:len(*model)-1]
			},
		},
		{
			name:    "reset performance",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, _ int) {
				t.Helper()
				b.ResetPerformance()
				for y, row := range *model {
					if row == nil {
						(*model)[y] = nil
					} else {
						(*model)[y] = row[:0]
					}
				}
			},
		},
		{
			name:    "reset cells",
			initial: ringTransitionRows(),
			linearize: func(t *testing.T, b *Buffer, model *[][]term.Cell, _ int) {
				t.Helper()
				b.ResetCells(*model)
				for y, row := range *model {
					if len(row) == 0 {
						(*model)[y] = nil
					}
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := cloneCellRows(tt.initial)
			b := newWrappedRingTestBuffer(tt.initial, ' ', 2)
			require.NotZero(t, b.cells.ringHead)

			for round := range 12 {
				tt.linearize(t, b, &model, round)
				assert.Zero(t, b.cells.ringHead, "linearizer round %d", round)
				assertBufferLogicalRows(t, b, model)

				exerciseRingMutationRound(t, b, &model, round)
			}

			assert.Equal(t, model, b.CopyRows(nil))
			assert.Equal(t, model, b.RawCells())
			assert.Zero(t, b.cells.ringHead)
		})
	}
}

func ringTransitionRows() [][]term.Cell {
	return [][]term.Cell{
		{ringStyledCell('a'), ringSpaceCell(), ringTabCell(), ringWideCell('漢')},
		{ringCombiningCell(), ringStyledCell('b'), ringSpaceCell()},
		stringToCells("third"),
		nil,
		{},
	}
}

func exerciseRingMutationRound(
	t *testing.T, b *Buffer, model *[][]term.Cell, round int,
) {
	t.Helper()
	space := ringSpaceCell()
	limit := 6
	count := 1 + round%2
	width := 5 + round%3
	require.True(t, b.AppendBlankRowsBounded(count, width, limit))
	*model = modelAppendBlankRowsBounded(*model, count, width, limit, space)
	assertBufferLogicalRows(t, b, *model)

	last := len(*model) - 1
	dirty := ringDirtyRow(round)
	row := b.MutableRow(last, len(dirty))
	copy(row, dirty)
	copy((*model)[last], dirty)
	assertBufferLogicalRows(t, b, *model)

	if round%3 == 0 {
		replacement := []term.Cell{
			ringStyledCell(rune('k' + round%10)), ringWideCell('界'), ringCombiningCell(),
		}
		b.SetRow(last, cloneCellRow(replacement))
		(*model)[last] = cloneCellRow(replacement)
		assertBufferLogicalRows(t, b, *model)
	}

	wholeCount := []int{1, -1, 7, -8}[round%4]
	b.RotateRows(0, b.Rows(), wholeCount)
	rotateSlice(*model, 0, len(*model), wholeCount)
	assertBufferLogicalRows(t, b, *model)

	resetY, ok := firstRowWithCells(*model, 2)
	if ok {
		fill := space
		if round%2 != 0 {
			fill = ringStyledCell(' ')
		}
		end := min(3, len((*model)[resetY]))
		b.ResetRowRange(resetY, 1, end, fill)
		modelResetRowRange(*model, resetY, 1, end, fill)
		assertBufferLogicalRows(t, b, *model)
	}

	deleteY, ok := firstRowWithCells(*model, 4)
	if ok {
		b.DeleteRowRange(deleteY, 1, 3)
		modelDeleteRowRange(*model, deleteY, 1, 3)
		assertBufferLogicalRows(t, b, *model)
	}

	extendY := round % len(*model)
	targetWidth := len((*model)[extendY]) + 1 + round%2
	added, ok := b.ExtendRowToWidth(extendY, targetWidth)
	require.True(t, ok)
	assert.Equal(t, modelExtendRow(*model, extendY, targetWidth, space), added)
	assertBufferLogicalRows(t, b, *model)

	if len(*model) > 3 {
		start := 1
		end := len(*model) - 1
		partialCount := 1
		if round%2 != 0 {
			partialCount = -1
		}
		b.RotateRows(start, end, partialCount)
		rotateSlice(*model, start, end, partialCount)
		assertBufferLogicalRows(t, b, *model)
	}

	assert.Equal(t, *model, b.CopyRows(nil))
}

func ringDirtyRow(round int) []term.Cell {
	return []term.Cell{
		ringStyledCell(rune('0' + round%10)),
		ringWideCell('語'),
		ringCombiningCell(),
		ringTabCell(),
	}
}

func firstRowWithCells(rows [][]term.Cell, count int) (int, bool) {
	for y, row := range rows {
		if len(row) >= count {
			return y, true
		}
	}
	return 0, false
}

func modelAppendBlankRowsBounded(
	rows [][]term.Cell, count, width, limit int, fill term.Cell,
) [][]term.Cell {
	if count <= 0 || width < 0 || limit <= 0 {
		return rows
	}
	if excess := len(rows) - limit; excess > 0 {
		rows = append([][]term.Cell(nil), rows[excess:]...)
	}
	for range count {
		blank := repeatCell(fill, width)
		if len(rows) < limit {
			rows = append(rows, blank)
		} else {
			copy(rows, rows[1:])
			rows[len(rows)-1] = blank
		}
	}
	return rows
}

func modelResetRowRange(
	rows [][]term.Cell, y, start, end int, fill term.Cell,
) {
	if y < 0 || y >= len(rows) || start < 0 || start >= end {
		return
	}
	end = min(end, len(rows[y]))
	if start >= end {
		return
	}
	for x := start; x < end; x++ {
		rows[y][x] = fill
	}
}

func modelDeleteRowRange(rows [][]term.Cell, y, start, end int) {
	if y < 0 || y >= len(rows) || start < 0 || start >= end || start >= len(rows[y]) {
		return
	}
	end = min(end, len(rows[y]))
	copy(rows[y][start:], rows[y][end:])
	rows[y] = rows[y][:len(rows[y])-(end-start)]
}

func modelExtendRow(rows [][]term.Cell, y, width int, fill term.Cell) int {
	if y < 0 || y >= len(rows) || len(rows[y]) >= width {
		return 0
	}
	added := width - len(rows[y])
	for range added {
		rows[y] = append(rows[y], fill)
	}
	return added
}
