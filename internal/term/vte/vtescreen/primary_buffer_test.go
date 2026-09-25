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
	"testing"

	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

const testHistory = 1000

func TestPrimaryReset(t *testing.T) {
	t.Run("exactly screen height compared to amount of rows", func(t *testing.T) {
		b := NewPrimaryBuffer(0, testHistory)
		b.Resize(5, 5)
		resetPrimaryBuffer(b, "0\n1\n2\n3\n4")

		assert.Equal(t, 5, b.Rows())
		assert.Equal(t, 5, b.Columns(0))
		assert.Equal(t, 5, b.Columns(1))
		assert.Equal(t, 5, b.Columns(2))
		assert.Equal(t, 5, b.Columns(3))
		assert.Equal(t, 5, b.Columns(4))
		assert.Equal(t, 0, b.Columns(5))

		b.Reset()
		assert.Equal(t, 5, b.Rows())
		assert.Equal(t, 5, b.Columns(0))
		assert.Equal(t, 5, b.Columns(1))
		assert.Equal(t, 5, b.Columns(2))
		assert.Equal(t, 5, b.Columns(3))
		assert.Equal(t, 5, b.Columns(4))
		assert.Equal(t, 0, b.Columns(5))
	})

	t.Run("smaller screen height compared to amount of rows", func(t *testing.T) {
		b := NewPrimaryBuffer(0, testHistory)
		b.Resize(5, 2)
		assert.Equal(t, 2, b.Rows())
		resetPrimaryBuffer(b, "0\n1\n2\n3\n4")

		assert.Equal(t, 5, b.Rows())
		assert.Equal(t, 5, b.Columns(0))
		assert.Equal(t, 5, b.Columns(1))
		assert.Equal(t, 5, b.Columns(2))
		assert.Equal(t, 5, b.Columns(3))
		assert.Equal(t, 5, b.Columns(4))
		assert.Equal(t, 0, b.Columns(5))

		b.Reset()
		assert.Equal(t, 2, b.Rows())
		assert.Equal(t, 5, b.Columns(0))
		assert.Equal(t, 5, b.Columns(1))
		assert.Equal(t, 0, b.Columns(2))
	})
}

func TestPrimaryScrollUpHistory(t *testing.T) {
	t.Run("appends blank rows while under max history", func(t *testing.T) {
		b := NewPrimaryBuffer(0, 20)
		b.SetDefaultChar(' ')
		b.Resize(3, 3)
		resetPrimaryBuffer(b, "0\n1\n2")
		require.Equal(t, 3, b.Rows())

		b.ScrollUpHistory(1)
		assert.Equal(t, 4, b.Rows())
		assert.Equal(t, "0  ", cellsRowString(b, 0))
		assert.Equal(t, "   ", cellsRowString(b, 3))
		assert.Equal(t, 3, b.Columns(3))
	})

	t.Run("recycles oldest rows past max history", func(t *testing.T) {
		history := 5
		b := NewPrimaryBuffer(0, history)
		b.SetDefaultChar(' ')
		b.Resize(3, 3)
		resetPrimaryBuffer(b, "0\n1\n2")

		for b.Rows() < history {
			b.ScrollUpHistory(1)
		}
		assert.Equal(t, history, b.Rows())
		assert.Equal(t, "0  ", cellsRowString(b, 0))
		recycled := &b.Cells.RawCells()[0][0]

		for range history + 1 {
			b.ScrollUpHistory(1)
		}
		assert.Equal(t, history, b.Rows())
		assert.Same(t, recycled, &b.Cells.RawCells()[history-1][0])
		for y := range history {
			assert.Equal(t, "   ", cellsRowString(b, y))
		}
	})

	t.Run("trims rows already beyond max history", func(t *testing.T) {
		b := NewPrimaryBuffer(0, 3)
		b.SetDefaultChar(' ')
		b.Restore(term.StringToCells("0\n1\n2\n3\n4"), term.Coordinates{Y: 4}, 1, 2)
		require.Equal(t, 5, b.Rows())

		b.ScrollUpHistory(1)
		assert.Equal(t, 3, b.Rows())
		assert.Equal(t, "3", cellsRowString(b, 0))
		assert.Equal(t, "4", cellsRowString(b, 1))
		assert.Equal(t, " ", cellsRowString(b, 2))
	})

	t.Run("rotates in place when history is disabled", func(t *testing.T) {
		b := NewPrimaryBuffer(0, 0)
		b.SetDefaultChar(' ')
		b.Resize(3, 3)
		resetPrimaryBuffer(b, "0\n1\n2")
		require.Equal(t, 3, b.Rows())

		b.ScrollUpHistory(1)
		assert.Equal(t, 3, b.Rows())
		assert.Equal(t, "1  ", cellsRowString(b, 0))
		assert.Equal(t, "2  ", cellsRowString(b, 1))
		assert.Equal(t, "   ", cellsRowString(b, 2))
	})
}

func cellsRowString(b *PrimaryBuffer, y int) string {
	row := b.Cells.RawCells()[y]
	runes := make([]rune, len(row))
	for x, c := range row {
		runes[x] = c.Ch
	}
	return string(runes)
}

func TestPrimaryCoordinates(t *testing.T) {
	b := NewPrimaryBuffer(0, testHistory)
	assert.Equal(t, term.Coordinates{}, b.CursorAtScreen())
	assert.Equal(t, term.Coordinates{}, b.CursorAtScroll())

	b.Resize(5, 5)
	assert.Equal(t, term.Coordinates{}, b.CursorAtScroll())
	assert.Equal(t, term.Coordinates{}, b.CursorAtScreen())

	b.SetCursorAtScreen(term.Coordinates{Y: 4, X: 4})
	assert.Equal(t, term.Coordinates{Y: 4, X: 4}, b.CursorAtScroll())
	assert.Equal(t, term.Coordinates{Y: 4, X: 4}, b.CursorAtScreen())

	b.SetCursorAtScreen(term.Coordinates{Y: 5, X: 5})
	assert.Equal(t, term.Coordinates{Y: 4, X: 4}, b.CursorAtScroll())
	assert.Equal(t, term.Coordinates{Y: 4, X: 4}, b.CursorAtScreen())
}

func TestPrimaryRestorePreservesCursorAtScreen(t *testing.T) {
	b := NewPrimaryBuffer(0, testHistory)
	b.Restore(term.StringToCells("0\n1\n2\n3\n4\n5\n6\n7\n8\n9"),
		term.Coordinates{Y: 2, X: 1}, 5, 5)

	assert.Equal(t, term.Coordinates{Y: 2, X: 1}, b.CursorAtScreen())
	assert.Equal(t, term.Coordinates{Y: 7, X: 1}, b.CursorAtScroll())
}

func TestPrimarySelection(t *testing.T) {
	t.Run("select with scroll", func(t *testing.T) {
		b := makePrimaryBufferForTesting(1, 5)
		resetPrimaryBuffer(b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")

		b.Select(term.Coordinates{Y: 1})
		b.SelectEnd(term.Coordinates{Y: 2})
		actual, ok := b.Selection()
		require.True(t, ok)
		expected := [][]term.Cell{{{Ch: '6', Width: 1}}, {{Ch: '7', Width: 1}}}
		assert.Equal(t, expected, actual)
	})
	t.Run("word selection", func(t *testing.T) {
		b := makePrimaryBufferForTesting(2, 10)
		resetPrimaryBuffer(b, "00\n11\n22\n33\n44\n55\n66\n77\n88\n99")

		b.SelectWordAt(term.Coordinates{Y: 99})
		_, ok := b.Selection()
		require.False(t, ok)

		b.SelectWordAt(term.Coordinates{Y: 2})
		actual, ok := b.Selection()
		require.True(t, ok)
		expected := [][]term.Cell{{{Ch: '2', Width: 1}, {Ch: '2', Width: 1}}}
		assert.Equal(t, expected, actual)
	})

	t.Run("coordinates with scroll", func(t *testing.T) {
		b := makePrimaryBufferForTesting(1, 5)
		resetPrimaryBuffer(b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")

		b.Select(term.Coordinates{Y: 1})
		b.SelectEnd(term.Coordinates{Y: 2})

		mode, from, to, ok := b.SelectionCoordinatesAtScreen()
		require.True(t, ok)
		assert.Equal(t, mode, text.StandardSelection)
		assert.Equal(t, term.Coordinates{Y: 1}, from)
		assert.Equal(t, term.Coordinates{Y: 2, X: 1}, to)

		_, from, to, ok = b.SelectionCoordinatesAtScroll()
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{Y: 6}, from)
		assert.Equal(t, term.Coordinates{Y: 7, X: 1}, to)
	})
}

func TestPrimaryClear(t *testing.T) {
	suite := []struct {
		width, height   int
		content         string
		expectedContent string
		expectedView    string
	}{
		{2, 2, "", "  \n  ", "  \n  "},
		{2, 2, "$ \n  ", "$ \n  \n  ", "  \n  "},
		{2, 2, "a \n$ ", "a \n$ \n  \n  ", "  \n  "},
		{2, 2, "b \na \n$ ", "b \na \n$ \n  \n  ", "  \n  "},
		{2, 2, "c \nb \na \n$ ", "c \nb \na \n$ \n  \n  ", "  \n  "},
		{2, 2, "c \nb \na \n$ \n  \n  \n  ", "c \nb \na \n$ \n  \n  \n  ", "  \n  "},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			b := makePrimaryBufferForTesting(test.width, test.height)
			resetPrimaryBuffer(b, test.content)

			initialCursor := b.CursorAtScreen()
			cleared := b.Clear() > 0
			writer := term.NewStringWriter(test.width, test.height)
			b.Draw(writer)
			writer.Flush()
			assert.Equal(t, test.expectedView, writer.String(), "view is incorrect")
			if cleared {
				assert.Equal(t, term.Coordinates{}, b.CursorAtScreen(), "cursor is incorrect")
			} else {
				assert.Equal(t, initialCursor, b.CursorAtScreen(), "cursor is incorrect")
			}
			assert.Equal(t, test.expectedContent, b.Cells.String(), "content is incorrect")
		})
	}
}

func TestPrimaryClearHistory(t *testing.T) {
	suite := []struct {
		width, height   int
		content         string
		expectedContent string
		expectedView    string
	}{
		{2, 2, "", "  \n  ", "  \n  "},
		{2, 2, "$ \n  ", "$ \n  ", "$ \n  "},
		{2, 2, "a \n$ ", "a \n$ ", "a \n$ "},
		{2, 2, "b \na \n$ ", "a \n$ ", "a \n$ "},
		{2, 2, "c \nb \na \n$ ", "a \n$ ", "a \n$ "},
		{2, 2, "c \nb \na \n$ \n  \n  \n  ", "  \n  ", "  \n  "},
	}

	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			b := makePrimaryBufferForTesting(test.width, test.height)
			resetPrimaryBuffer(b, test.content)

			initialCursor := b.CursorAtScreen()
			b.ClearHistory()
			writer := term.NewStringWriter(test.width, test.height)
			b.Draw(writer)
			writer.Flush()
			assert.Equal(t, test.expectedView, writer.String(), "view is incorrect")
			assert.Equal(t, initialCursor, b.CursorAtScreen(), "cursor is incorrect")
			assert.Equal(t, test.expectedContent, b.Cells.String(), "content is incorrect")
		})
	}
}

func TestPrimaryResize(t *testing.T) {
	t.Run("maintains number of rows if resize is equal height", func(t *testing.T) {

		t.Run("buffer with more lines than height", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 10)
			assert.Equal(t, '9', cells[9][0].Ch)

			b.Resize(2, 5)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 10)
			assert.Equal(t, '9', cells[9][0].Ch)
		})

		t.Run("buffer with less lines than height", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2\n \n ")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '2', cells[2][0].Ch)

			b.Resize(2, 5)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '2', cells[2][0].Ch)
		})

		t.Run("buffer with exactly height lines", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2\n3\n4")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '4', cells[4][0].Ch)

			b.Resize(2, 5)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '4', cells[4][0].Ch)
		})
	})

	t.Run("potentially trims number of rows if height is decreased", func(t *testing.T) {

		t.Run("buffer with more lines than height", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 10)
			assert.Equal(t, '9', cells[9][0].Ch)

			b.Resize(2, 4)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 10)
			assert.Equal(t, '9', cells[9][0].Ch)
		})

		t.Run("buffer with less lines than height", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '2', cells[2][0].Ch)

			b.Resize(2, 4)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 4, b.cursor.position)
			assert.Equal(t, '2', cells[2][0].Ch)
		})

		t.Run("buffer with exactly height lines", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2\n3\n4")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '4', cells[4][0].Ch)

			b.Resize(2, 4)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '4', cells[4][0].Ch)
		})
	})

	t.Run("extends rows if height is increased", func(t *testing.T) {

		t.Run("buffer with more lines than height", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 10)
			assert.Equal(t, '9', cells[9][0].Ch)

			b.Resize(2, 11)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 11)
			assert.Equal(t, '9', cells[9][0].Ch)
			assert.Equal(t, ' ', cells[10][0].Ch)
		})

		t.Run("buffer with less lines than height", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2\n \n ")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '2', cells[2][0].Ch)

			b.Resize(2, 11)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 11)
			assert.Equal(t, '2', cells[2][0].Ch)
			assert.Equal(t, ' ', cells[10][0].Ch)
		})

		t.Run("buffer with exactly height lines", func(t *testing.T) {
			b := makePrimaryBufferForTesting(1, 5)
			resetPrimaryBuffer(b, "0\n1\n2\n3\n4")
			cells := b.Cells.RawCells()
			require.Len(t, cells, 5)
			assert.Equal(t, '4', cells[4][0].Ch)

			b.Resize(2, 11)
			cells = b.Cells.RawCells()
			require.Len(t, cells, 11)
			assert.Equal(t, '4', cells[4][0].Ch)
			assert.Equal(t, ' ', cells[10][0].Ch)
		})
	})

	t.Run("wraps rows", func(t *testing.T) {
		suite := []struct {
			initialWidth     int
			initialHeight    int
			finalWidth       int
			finalHeight      int
			input            string
			expectedOutput   string
			expectedCursorAt term.Coordinates
		}{
			{0, 0, 5, 5, "", "     \n     \n     \n     \n     ", term.Coordinates{}},
			{2, 2, 5, 5, "a\nb", "a    \nb    \n     \n     \n     ", term.Coordinates{Y: 1, X: 1}},
			{10, 5, 5, 5, "aaaaaa\nbbb", "aaaaa\na    \nbbb  \n     \n     ", term.Coordinates{Y: 2, X: 3}},
			{10, 5, 9, 5, "aaaaaa\nbbb", "aaaaaa   \nbbb      \n         \n         \n         ", term.Coordinates{Y: 1, X: 3}},
			{10, 10, 5, 5, "aaaaaa\nbbb", "aaaaa\na    \nbbb  \n     \n     ", term.Coordinates{Y: 2, X: 3}},
			{
				10,
				5,
				1,
				10,
				strings.TrimSuffix(strings.Repeat("aaaaaa\nbbb\n$\n", 10), "\n"),
				"a\na\na\na\na\na\nb\nb\nb\n$",
				term.Coordinates{Y: 99, X: 1},
			},
			{10, 5, 2, 5, "aaaaaa\nbbb\n$", "aa\naa\nbb\nb \n$ ", term.Coordinates{Y: 5, X: 1}},
			{10, 5, 2, 2, "aaaaaa\nbbb\n$", "b \n$ ", term.Coordinates{Y: 5, X: 1}},
			{10, 5, 2, 8, "aaaaaa\nbbb\n$", "aa\naa\naa\nbb\nb \n$ \n  \n  ", term.Coordinates{Y: 5, X: 1}},
			{5, 5, 10, 2, "aaaa\nbbb\n$", "bbb       \n$         ", term.Coordinates{Y: 2, X: 1}},
			{5, 5, 0, 0, "aaaa\nbbb\n$", "", term.Coordinates{Y: 2, X: 1}},
		}

		for i, test := range suite {
			t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
				b := NewPrimaryBuffer(0, testHistory)
				b.Resize(test.initialWidth, test.initialHeight)
				writeToPrimaryBuffer(b, test.input)

				writer := term.NewStringWriter(test.initialWidth, test.initialHeight)
				b.Draw(writer)
				writer.Flush()
				initialContent := writer.String()
				initialCursor := b.CursorAtScroll()

				b.Resize(test.finalWidth, test.finalHeight)
				writer = term.NewStringWriter(test.finalWidth, test.finalHeight)
				b.Draw(writer)
				writer.Flush()
				assert.Equal(t, test.expectedOutput, writer.String(), "%q", b.Cells.String())
				assert.Equal(t, test.expectedCursorAt, b.CursorAtScroll())

				b.Resize(test.initialWidth, test.initialHeight)
				writer = term.NewStringWriter(test.initialWidth, test.initialHeight)
				b.Draw(writer)
				writer.Flush()
				assert.Equal(t, initialContent, writer.String(), "shrink back to original size: %q", b.Cells.String())
				assert.Equal(t, initialCursor, b.CursorAtScroll())
			})
		}
	})
}

func BenchmarkPrimaryBufferResize(b *testing.B) {
	const (
		narrowWidth = 20
		wideWidth   = 40
		height      = 200
		lineWidth   = 36
	)

	suite := []struct {
		name         string
		lineCount    int
		initialWidth int
		finalWidth   int
		expectedRows int
	}{
		{
			name:         "unwrap/fits_height",
			lineCount:    height,
			initialWidth: narrowWidth,
			finalWidth:   wideWidth,
			expectedRows: height,
		},
		{
			name:         "unwrap/double_height",
			lineCount:    2 * height,
			initialWidth: narrowWidth,
			finalWidth:   wideWidth,
			expectedRows: 2 * height,
		},
		{
			name:         "wrap/fits_height",
			lineCount:    height / 2,
			initialWidth: wideWidth,
			finalWidth:   narrowWidth,
			expectedRows: height,
		},
		{
			name:         "wrap/double_height",
			lineCount:    height,
			initialWidth: wideWidth,
			finalWidth:   narrowWidth,
			expectedRows: 2 * height,
		},
	}

	for _, test := range suite {
		b.Run(test.name, func(b *testing.B) {
			b.Helper()
			prepared := makePrimaryBufferForTesting(max(test.initialWidth, test.finalWidth), height)
			resetPrimaryBuffer(prepared,
				makeBenchmarkPrimaryBufferContent(test.lineCount, lineWidth))
			if test.initialWidth != prepared.Width() {
				prepared.Resize(test.initialWidth, height)
			}
			snapshot := term.CloneCells(prepared.Cells.RawCells())
			cursor := prepared.CursorAtScreen()
			savedCursor := prepared.savedCursor
			wraps := prepared.wraps
			probe := NewPrimaryBuffer(0, testHistory)
			probe.SetDefaultChar(' ')
			probe.Restore(snapshot, cursor, test.initialWidth, height)
			probe.SetSavedCursor(savedCursor)
			probe.wraps = wraps
			probe.Resize(test.finalWidth, height)
			if probe.Rows() != test.expectedRows {
				b.Fatalf("unexpected row count after resize: got %d, want %d", probe.Rows(), test.expectedRows)
			}
			buf := NewPrimaryBuffer(0, testHistory)
			buf.SetDefaultChar(' ')
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				buf.Restore(snapshot, cursor, test.initialWidth, height)
				buf.SetSavedCursor(savedCursor)
				buf.wraps = wraps
				b.StartTimer()
				buf.Resize(test.finalWidth, height)
			}
		})
	}
}

func makeBenchmarkPrimaryBufferContent(lines, lineWidth int) string {
	line := strings.Repeat("x", lineWidth)
	var builder strings.Builder
	builder.Grow(lines * (lineWidth + 1))
	for i := range lines {
		if i > 0 {
			_ = builder.WriteByte('\n')
		}
		_, _ = builder.WriteString(line)
	}
	return builder.String()
}

func makePrimaryBufferForTesting(width, height int) *PrimaryBuffer {
	ret := NewPrimaryBuffer(0, testHistory)
	ret.SetDefaultChar(' ')
	ret.Resize(width, height)
	return ret
}

func writeToPrimaryBuffer(b *PrimaryBuffer, str string) {
	for _, ch := range str {
		pos := b.CursorAtScreen()
		if ch == '\n' {
			pos.X = 0
			if pos.Y+1 >= b.BottomScrollableRegion() {
				b.InsertLines(1, term.Coordinates{})
				start := 0
				end := b.Rows()
				count := max(0, min(1, end-start))
				if count > 0 {
					b.ScrollUp(start, end, count)
				}
			} else {
				pos.Y++
			}
			b.SetCursorAtScreen(pos)
		} else {
			b.Write(ch, uniseg.StringWidth(string(ch)), 0)
			pos.X++
			b.SetCursorAtScreen(pos)
		}
	}
}

func resetPrimaryBuffer(b *PrimaryBuffer, to string) {
	b.ResetLines(0, b.Height())
	b.SetCursorAtScreen(term.Coordinates{})
	writeToPrimaryBuffer(b, to)
}
