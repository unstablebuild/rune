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
	"testing"

	"github.com/rivo/uniseg"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/vte/vteparser"

	"github.com/stretchr/testify/assert"
)

func TestNewAltBuffer(t *testing.T) {
	b := NewAltBuffer()
	assert.Equal(t, 1, b.Width())
	assert.Equal(t, 1, b.Height())
	assert.Equal(t, 0, b.TopScrollableRegion())
	assert.Equal(t, 1, b.BottomScrollableRegion())
	assert.Equal(t, 0, b.CursorAtScreen().X)
	assert.Equal(t, 0, b.CursorAtScreen().Y)
	require.NotNil(t, b.Cells.RawCells())
	assert.NotNil(t, b.Cursor().Charsets)

	assert.Equal(t, [][]term.Cell{
		{{Ch: DefaultChar, Width: 1}},
	}, b.Cells.RawCells())

	// rendering corresponds to stored cells
	// this is important so writes to buffer are correct

	b.Resize(4, 4)
	b.Insert('\t', 1, 0)
	b.SetCursorAtScreen(term.Coordinates{X: 1})
	b.Insert('a', 1, 0)
	w := term.NewStringWriter(4, 4)
	b.Draw(w)
	w.Flush()

	assert.Equal(t, " a  \n    \n    \n    ", w.String())
}

func TestResize(t *testing.T) {
	t.Run("resize up", func(t *testing.T) {
		b := NewAltBuffer()
		b.defaultChar = 'X'
		b.Resize(2, 2)
		assert.Equal(t, 2, b.Width())
		assert.Equal(t, 2, b.Height())
		assert.Equal(t, 0, b.TopScrollableRegion())
		assert.Equal(t, 2, b.BottomScrollableRegion())
		require.NotNil(t, b.Cells.RawCells())

		assert.Equal(t, [][]term.Cell{
			{{Ch: 'X', Width: 1}, {Ch: 'X', Width: 1}},
			{{Ch: 'X', Width: 1}, {Ch: 'X', Width: 1}},
		}, b.Cells.RawCells())
	})
	t.Run("resize down", func(t *testing.T) {
		b := NewAltBuffer()
		b.Resize(3, 3)
		b.defaultChar = 'X'
		b.Resize(2, 2)
		assert.Equal(t, 2, b.Width())
		assert.Equal(t, 2, b.Height())
		assert.Equal(t, 0, b.TopScrollableRegion())
		assert.Equal(t, 2, b.BottomScrollableRegion())
		require.NotNil(t, b.Cells.RawCells())

		assert.Equal(t, [][]term.Cell{
			{{Ch: 'X', Width: 1}, {Ch: 'X', Width: 1}},
			{{Ch: 'X', Width: 1}, {Ch: 'X', Width: 1}},
		}, b.Cells.RawCells())
	})
}

func TestResetLines(t *testing.T) {
	t.Run("all", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb")
		assertEqualBuf(t, b, "a    \nb    \n     \n     \n     ")

		b.ResetLines(0, 5)
		assertEqualBuf(t, b, "     \n     \n     \n     \n     ")
	})
	t.Run("subset of lines at start", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb")
		assertEqualBuf(t, b, "a    \nb    \n     \n     \n     ")

		b.ResetLines(0, 1)
		assertEqualBuf(t, b, "     \nb    \n     \n     \n     ")
	})
	t.Run("subset of lines in the middle", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb")
		assertEqualBuf(t, b, "a    \nb    \n     \n     \n     ")

		b.ResetLines(1, 2)
		assertEqualBuf(t, b, "a    \n     \n     \n     \n     ")
	})
	t.Run("subset of lines at the end", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb\nc\nd\ne")
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")

		b.ResetLines(4, 5)
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \n     ")
	})
	t.Run("past last line does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb\nc\nd\ne")
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")

		b.ResetLines(5, 6)
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")
	})
	t.Run("start > end does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb\nc\nd\ne")
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")

		b.ResetLines(3, 2)
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")
	})
	t.Run("start == end does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb\nc\nd\ne")
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")

		b.ResetLines(0, 0)
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")
	})
	t.Run("negative start does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb\nc\nd\ne")
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")

		b.ResetLines(-1, 2)
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")
	})
	t.Run("negative end does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb\nc\nd\ne")
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")

		b.ResetLines(1, -2)
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")
	})
}

func TestResetCells(t *testing.T) {
	t.Run("reset after content does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb")
		assertEqualBuf(t, b, "a    \nb    \n     \n     \n     ")

		b.ResetCells(1, 5)
		assertEqualBuf(t, b, "a    \nb    \n     \n     \n     ")
	})

	t.Run("reset one", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb")
		assertEqualBuf(t, b, "a    \nb    \n     \n     \n     ")

		b.ResetCells(0, 1)
		assertEqualBuf(t, b, "a    \n     \n     \n     \n     ")
	})

	t.Run("reset with cursor after height does not panic", func(t *testing.T) {
		b := makeAltBufferForTesting(5, 5)
		writeToAltBuffer(b, "a\nb\nc\nd\ne\n")
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    ")

		// this shouldn't happen if AltBuffer is used correctly
		// but be resilient against misuse
		b.cursor.position.Y = 5
		b.ResetCells(0, 1)
		assertEqualBuf(t, b, "a    \nb    \nc    \nd    \ne    \n     ")
	})
}

func TestScrollUp(t *testing.T) {
	t.Run("count zero does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 10, 0)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
	})

	t.Run("all once", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 10, 1)
		assertEqualBuf(t, b, "1\n2\n3\n4\n5\n6\n7\n8\n9\n ")
	})
	t.Run("all multi ", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 10, 2)
		assertEqualBuf(t, b, "2\n3\n4\n5\n6\n7\n8\n9\n \n ")
	})
	t.Run("all exactly length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 10, 10)
		assertEqualBuf(t, b, " \n \n \n \n \n \n \n \n \n ")
	})
	t.Run("all more than length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 10, 11)
		assertEqualBuf(t, b, " \n \n \n \n \n \n \n \n \n ")
	})

	t.Run("top once ", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 3, 1)
		assertEqualBuf(t, b, "1\n2\n \n3\n4\n5\n6\n7\n8\n9")
	})
	t.Run("top multi", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 3, 2)
		assertEqualBuf(t, b, "2\n \n \n3\n4\n5\n6\n7\n8\n9")
	})
	t.Run("top exactly length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 3, 3)
		assertEqualBuf(t, b, " \n \n \n3\n4\n5\n6\n7\n8\n9")
	})
	t.Run("top more than length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(0, 3, 4)
		assertEqualBuf(t, b, " \n \n \n3\n4\n5\n6\n7\n8\n9")
	})

	t.Run("mid once ", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(1, 4, 1)
		assertEqualBuf(t, b, "0\n2\n3\n \n4\n5\n6\n7\n8\n9")
	})
	t.Run("mid multi", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(1, 4, 2)
		assertEqualBuf(t, b, "0\n3\n \n \n4\n5\n6\n7\n8\n9")
	})
	t.Run("mid exactly length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(1, 4, 3)
		assertEqualBuf(t, b, "0\n \n \n \n4\n5\n6\n7\n8\n9")
	})
	t.Run("mid more than length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(1, 4, 4)
		assertEqualBuf(t, b, "0\n \n \n \n4\n5\n6\n7\n8\n9")
	})

	t.Run("bottom once ", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(7, 10, 1)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n8\n9\n ")
	})
	t.Run("bottom multi", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(7, 10, 2)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n9\n \n ")
	})
	t.Run("bottom exactly length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(7, 10, 3)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n \n \n ")
	})
	t.Run("bottom more than length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollUp(7, 10, 4)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n \n \n ")
	})

	t.Run("once all but top line", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "a\nb\n \n \n \n \n \n \n \n ")
		b.ScrollUp(1, 10, 1)
		assertEqualBuf(t, b, "a\n \n \n \n \n \n \n \n \n ")
	})
}

// TestScrollUpRecycledRowFill pins the content of the row a scroll
// recycles: it must carry the fill character, unit width and the cursor
// background in effect at the time of the scroll. A blank-row template
// that is not invalidated when the background, the fill character or the
// width changes regresses exactly here.
func TestScrollUpRecycledRowFill(t *testing.T) {
	red := term.NewRGBColor(200, 10, 10)
	blue := term.NewRGBColor(10, 10, 200)

	t.Run("adopts current cursor background", func(t *testing.T) {
		b := makeAltBufferForTesting(4, 3)
		resetAltBuffer(t, b, "abcd\nefgh\nijkl")

		b.SetCursorAttributes(term.Attributes{Bg: red})
		b.ScrollUp(0, 3, 1)
		requireBlankRow(t, b, 2, ' ', red)

		b.SetCursorAttributes(term.Attributes{Bg: blue})
		b.ScrollUp(0, 3, 1)
		requireBlankRow(t, b, 2, ' ', blue)

		b.SetCursorAttributes(term.Attributes{})
		b.ScrollUp(0, 3, 1)
		requireBlankRow(t, b, 2, ' ', term.Color(0))
	})

	t.Run("multi line region", func(t *testing.T) {
		b := makeAltBufferForTesting(4, 6)
		resetAltBuffer(t, b, "abcd\nefgh\nijkl\nmnop\nqrst\nuvwx")

		b.SetCursorAttributes(term.Attributes{Bg: red})
		b.ScrollUp(1, 5, 2)
		requireBlankRow(t, b, 3, ' ', red)
		requireBlankRow(t, b, 4, ' ', red)
	})

	t.Run("widens with the buffer", func(t *testing.T) {
		b := makeAltBufferForTesting(4, 3)
		b.SetCursorAttributes(term.Attributes{Bg: red})
		b.ScrollUp(0, 3, 1)
		requireBlankRow(t, b, 2, ' ', red)

		b.Resize(9, 3)
		b.ScrollUp(0, 3, 1)
		requireBlankRow(t, b, 2, ' ', red)
	})

	t.Run("follows the default char", func(t *testing.T) {
		b := makeAltBufferForTesting(4, 3)
		b.ScrollUp(0, 3, 1)
		requireBlankRow(t, b, 2, ' ', term.Color(0))

		b.SetDefaultChar('.')
		b.ScrollUp(0, 3, 1)
		requireBlankRow(t, b, 2, '.', term.Color(0))
	})

	t.Run("decaln fill does not leak into later scrolls", func(t *testing.T) {
		b := makeAltBufferForTesting(4, 3)
		b.ResetLinesWith(0, 3, 'E')
		requireBlankRow(t, b, 0, 'E', term.Color(0))

		b.ScrollUp(0, 3, 1)
		requireBlankRow(t, b, 2, ' ', term.Color(0))
	})
}

func requireBlankRow(t *testing.T, b *AltBuffer, y int, with rune, bg term.Color) {
	t.Helper()
	row := b.Cells.RawCells()[y]
	require.Len(t, row, b.Width())
	for x, c := range row {
		require.Equalf(t, with, c.Ch, "row %d col %d char", y, x)
		require.Equalf(t, uint8(1), c.Width, "row %d col %d width", y, x)
		require.Equalf(t, bg, c.Bg, "row %d col %d bg", y, x)
	}
}

func TestScrollDown(t *testing.T) {
	t.Run("count zero does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 10, 0)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
	})

	t.Run("all once", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 10, 1)
		assertEqualBuf(t, b, " \n0\n1\n2\n3\n4\n5\n6\n7\n8")
	})
	t.Run("all multi", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 10, 2)
		assertEqualBuf(t, b, " \n \n0\n1\n2\n3\n4\n5\n6\n7")
	})
	t.Run("all exactly length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 10, 10)
		assertEqualBuf(t, b, " \n \n \n \n \n \n \n \n \n ")
	})
	t.Run("all more than length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 10, 11)
		assertEqualBuf(t, b, " \n \n \n \n \n \n \n \n \n ")
	})

	t.Run("top once", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 3, 1)
		assertEqualBuf(t, b, " \n0\n1\n3\n4\n5\n6\n7\n8\n9")
	})
	t.Run("top multi", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 3, 2)
		assertEqualBuf(t, b, " \n \n0\n3\n4\n5\n6\n7\n8\n9")
	})
	t.Run("top exactly length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 3, 3)
		assertEqualBuf(t, b, " \n \n \n3\n4\n5\n6\n7\n8\n9")
	})
	t.Run("top more than length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(0, 3, 4)
		assertEqualBuf(t, b, " \n \n \n3\n4\n5\n6\n7\n8\n9")
	})

	t.Run("mid once", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(1, 4, 1)
		assertEqualBuf(t, b, "0\n \n1\n2\n4\n5\n6\n7\n8\n9")
	})
	t.Run("mid multi", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(1, 4, 2)
		assertEqualBuf(t, b, "0\n \n \n1\n4\n5\n6\n7\n8\n9")
	})
	t.Run("mid exactly length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(1, 4, 3)
		assertEqualBuf(t, b, "0\n \n \n \n4\n5\n6\n7\n8\n9")
	})
	t.Run("mid more than length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(1, 4, 4)
		assertEqualBuf(t, b, "0\n \n \n \n4\n5\n6\n7\n8\n9")
	})

	t.Run("bottom once ", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(7, 10, 1)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n \n7\n8")
	})
	t.Run("bottom multi", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(7, 10, 2)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n \n \n7")
	})
	t.Run("bottom exactly length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(7, 10, 3)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n \n \n ")
	})
	t.Run("bottom more than length", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")
		b.ScrollDown(7, 10, 4)
		assertEqualBuf(t, b, "0\n1\n2\n3\n4\n5\n6\n \n \n ")
	})
	t.Run("once all but top line", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "a\nb\n \n \n \n \n \n \n \n ")
		b.ScrollDown(1, 10, 1)
		assertEqualBuf(t, b, "a\n \nb\n \n \n \n \n \n \n ")
	})
}

func TestDelete(t *testing.T) {
	t.Run("deletes one character", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "a\nb\n \n \n \n \n \n \n \n ")
		b.SetCursorAtScreen(term.Coordinates{})
		b.Delete(1)
		assertEqualBuf(t, b, " \nb\n \n \n \n \n \n \n \n ")
	})

	t.Run("deletes partial start of row", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{})
		b.Delete(1)
		assertEqualBuf(t, b, "a \nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("deletes partial end of row", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{X: 1})
		b.Delete(1)
		assertEqualBuf(t, b, "a \nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("deletes multiple characters", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{})
		b.Delete(2)
		assertEqualBuf(t, b, "  \nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("past last column does nothing past last column", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{})
		b.Delete(3)
		assertEqualBuf(t, b, "  \nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("count + pos.X oob", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{X: 1})
		b.Delete(2)
		assertEqualBuf(t, b, "a \nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("zero count does nothing", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{})
		b.Delete(0)
		assertEqualBuf(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("deletes first few characters in long line", func(t *testing.T) {
		b := makeAltBufferForTesting(10, 2)
		resetAltBuffer(t, b, "0123456789\nabcdefghij")
		b.SetCursorAtScreen(term.Coordinates{X: 2})
		b.Delete(3)
		assertEqualBuf(t, b, "0156789   \nabcdefghij")
	})
}

func TestWriteInsert(t *testing.T) {
	t.Run("maps character to ' ' if SetHiddenCursor(true) was called", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{})
		b.SetHiddenCursor(true)
		b.Write('X', 1, 0)
		assertEqualBuf(t, b, " a\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetHiddenCursor(false)
		b.Write('X', 1, 0)
		assertEqualBuf(t, b, "Xa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("copies the attributes last set via SetCursorAttributes", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{})

		attrs := term.Attributes{
			Fg:    term.ColorYellow,
			Bg:    term.ColorBlue,
			Attrs: term.AttrItalic,
		}
		b.SetCursorAttributes(attrs)
		b.Write('X', 1, 0)
		assertEqualBuf(t, b, "Xa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")

		cell := b.Cells.RawCells()[0][0]
		assert.Equal(t, attrs, cell.Attributes())
	})

	t.Run("maps the character using the given charset index", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{})

		b.ConfigureCharset(vteparser.CharsetIndexG1, vteparser.StandardCharsetSpecialCharacterAndLineDrawing)
		b.Write('`', 1, vteparser.CharsetIndexG1)
		assertEqualBuf(t, b, "◆a\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("does nothing if passed charset has not been configured", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{})

		b.Write('`', 1, vteparser.CharsetIndexG1)
		assertEqualBuf(t, b, "`a\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("writes character at max column if if out of bounds (x)", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{X: 4})

		b.Write('X', 1, 0)
		assertEqualBuf(t, b, "aX\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("inserts character at max column if writing out of bounds (x)", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{X: 4})

		b.Write('X', 1, 0)
		assertEqualBuf(t, b, "aX\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("inserts character at max line if writing out of bounds (y)", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{Y: 12, X: 6})

		b.Write('X', 1, 0)
		assertEqualBuf(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n X")
	})

	t.Run("inserts character in bounds", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
		b.SetCursorAtScreen(term.Coordinates{X: 1})

		b.Insert('X', 1, 0)
		assertEqualBuf(t, b, "aX\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")
	})

	t.Run("insert at pos guarantees that CellAt does not return nil", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")

		b.SetCursorAtScreen(term.Coordinates{Y: 10})
		// invalidate cursor position
		b.Resize(2, 9)

		require.NotPanics(t, func() {
			b.Insert('X', 1, 0)
		})
		assertEqualBuf(t, b, "  \n  \n  \n  \n  \n  \n  \n  \n  \nX")
	})

	t.Run("write at pos guarantees that CellAt does not return nil", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "aa\nbb\n  \n  \n  \n  \n  \n  \n  \n  ")

		b.SetCursorAtScreen(term.Coordinates{Y: 10})
		// invalidate cursor position
		b.Resize(2, 9)

		require.NotPanics(t, func() {
			b.Write('X', 1, 0)
		})
		assertEqualBuf(t, b, "  \n  \n  \n  \n  \n  \n  \n  \n  \nX")
	})
}

func TestAltSelection(t *testing.T) {
	t.Run("no selection returns false", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")

		_, ok := b.Selection()
		require.False(t, ok)
	})
	t.Run("select start end are zero select one cell", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")

		b.Select(term.Coordinates{})
		b.SelectEnd(term.Coordinates{})
		cells, ok := b.Selection()
		require.True(t, ok)
		assert.Equal(t, [][]term.Cell{{{Ch: '0', Width: 1}}}, cells)
	})
	t.Run("multiline select standard single character lines", func(t *testing.T) {
		b := makeAltBufferForTesting(1, 10)
		resetAltBuffer(t, b, "0\n1\n2\n3\n4\n5\n6\n7\n8\n9")

		b.Select(term.Coordinates{})
		b.SelectEnd(term.Coordinates{Y: 1})
		cells, ok := b.Selection()
		require.True(t, ok)
		assert.Equal(t, [][]term.Cell{{{Ch: '0', Width: 1}}, {{Ch: '1', Width: 1}}}, cells)
	})
	t.Run("multiline select standard multiple character lines", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "00\n11\n22\n33\n44\n55\n66\n77\n88\n99")

		b.Select(term.Coordinates{})
		b.SelectEnd(term.Coordinates{Y: 1})
		cells, ok := b.Selection()
		require.True(t, ok)
		assert.Equal(t, [][]term.Cell{{{Ch: '0', Width: 1}, {Ch: '0', Width: 1}}, {{Ch: '1', Width: 1}}}, cells)
	})
	t.Run("multiline select line", func(t *testing.T) {
		b := makeAltBufferForTesting(2, 10)
		resetAltBuffer(t, b, "00\n11\n22\n33\n44\n55\n66\n77\n88\n99")

		b.SelectLine(term.Coordinates{})
		b.SelectEnd(term.Coordinates{Y: 1})
		cells, ok := b.Selection()
		require.True(t, ok)
		assert.Equal(t, [][]term.Cell{{{Ch: '0', Width: 1}, {Ch: '0', Width: 1}},
			{{Ch: '1', Width: 1}, {Ch: '1', Width: 1}}, {}}, cells)
	})
}

func makeAltBufferForTesting(width, height int) *AltBuffer {
	ret := NewAltBuffer()
	ret.defaultChar = ' '
	// re-init cells with default char set to space
	ret.Cells.InitPerformance(120, 80, ret.defaultChar)
	ret.Resize(width, height)
	return ret
}

func writeToAltBuffer(b *AltBuffer, str string) {
	for _, ch := range str {
		pos := b.CursorAtScreen()
		if ch == '\n' {
			pos.Y++
			pos.X = 0
			b.SetCursorAtScreen(pos)
		} else {
			b.Write(ch, uniseg.StringWidth(string(ch)), 0)
			pos.X++
			b.SetCursorAtScreen(pos)
		}
	}
}

func assertEqualBuf(t *testing.T, p *AltBuffer, expected string) {
	assert.Equal(t, expected, term.CellsToString(p.Cells.RawCells()))
}

func resetAltBuffer(t *testing.T, b *AltBuffer, to string) {
	b.ResetLines(0, b.Height())
	b.SetCursorAtScreen(term.Coordinates{})
	writeToAltBuffer(b, to)
	require.Equal(t, to, term.CellsToString(b.Cells.RawCells()))
}

func TestAltBufferWriteRunTable(t *testing.T) {
	styled := screenTestCell('s', 1)
	wide := screenTestCell('漢', 2)
	tests := []struct {
		name      string
		pos       term.Coordinates
		run       []byte
		hidden    bool
		charset   vteparser.StandardCharset
		seed      func([][]term.Cell)
		wantN     int
		wantCells map[term.Coordinates]term.Cell
	}{
		{name: "nil run", run: nil},
		{name: "empty run", run: []byte{}},
		{
			name:  "writes complete attributes",
			pos:   term.Coordinates{X: 1, Y: 1},
			run:   []byte("xy"),
			wantN: 2,
			wantCells: map[term.Coordinates]term.Cell{
				{X: 1, Y: 1}: screenTestWrittenCell('x'),
				{X: 2, Y: 1}: screenTestWrittenCell('y'),
			},
		},
		{
			name:  "spaces and tab are literal low level bytes",
			pos:   term.Coordinates{X: 2},
			run:   []byte{' ', '\t', ' '},
			wantN: 3,
			wantCells: map[term.Coordinates]term.Cell{
				{X: 2}: screenTestWrittenCell(' '),
				{X: 3}: screenTestWrittenCell('\t'),
				{X: 4}: screenTestWrittenCell(' '),
			},
		},
		{
			name:  "exact suffix fit",
			pos:   term.Coordinates{X: 3, Y: 2},
			run:   []byte("xyz"),
			wantN: 3,
		},
		{name: "one past suffix", pos: term.Coordinates{X: 3}, run: []byte("wxyz")},
		{name: "negative x", pos: term.Coordinates{X: -1}, run: []byte("x")},
		{name: "negative y", pos: term.Coordinates{Y: -1}, run: []byte("x")},
		{name: "y at rows", pos: term.Coordinates{Y: 3}, run: []byte("x")},
		{name: "concealed cursor", run: []byte("x"), hidden: true},
		{
			name:    "mapped charset",
			run:     []byte("lq"),
			charset: vteparser.StandardCharsetSpecialCharacterAndLineDrawing,
		},
		{
			name: "wide target first",
			run:  []byte("ab"),
			seed: func(rows [][]term.Cell) { rows[0][0] = wide },
		},
		{
			name: "wide target last",
			run:  []byte("ab"),
			seed: func(rows [][]term.Cell) { rows[0][1] = wide },
		},
		{
			name:  "wide cell immediately after target does not block",
			run:   []byte("ab"),
			wantN: 2,
			seed:  func(rows [][]term.Cell) { rows[0][2] = wide },
		},
		{
			name:  "zero and stale cells are fully replaced",
			pos:   term.Coordinates{X: 1},
			run:   []byte("x"),
			wantN: 1,
			seed: func(rows [][]term.Cell) {
				rows[0][1] = styled
				rows[0][1].SetCombining([]rune{'\u0301'})
			},
			wantCells: map[term.Coordinates]term.Cell{{X: 1}: screenTestWrittenCell('x')},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newScreenTestAltBuffer(6, 3)
			rows := b.Cells.CopyRows(nil)
			if tt.seed != nil {
				tt.seed(rows)
			}
			b.Cells.ResetCells(rows)
			b.SetCursorAtScreen(tt.pos)
			if tt.pos.X < 0 || tt.pos.Y < 0 || tt.pos.Y >= b.Rows() {
				b.cursor.position = tt.pos
			}
			b.SetCursorAttributes(screenTestAttributes())
			b.SetHiddenCursor(tt.hidden)
			if tt.charset != vteparser.StandardCharsetASCII {
				b.ConfigureCharset(vteparser.CharsetIndexG0, tt.charset)
			}
			before := b.Cells.CopyRows(nil)
			beforeCursor := b.CursorAtScreen()

			got := b.WriteRun(tt.run, vteparser.CharsetIndexG0)

			assert.Equal(t, tt.wantN, got)
			assert.Equal(t, beforeCursor, b.CursorAtScreen())
			if tt.wantN == 0 {
				assert.Equal(t, before, b.Cells.CopyRows(nil))
			}
			for pos, want := range tt.wantCells {
				cell := b.CellAt(pos)
				require.NotNil(t, cell, "%+v", pos)
				assert.Equal(t, want, *cell, "%+v", pos)
			}
		})
	}
}

func TestWriteGlyphRunTable(t *testing.T) {
	tests := []struct {
		name              string
		glyphs            []Glyph
		pos               term.Coordinates
		hidden            bool
		mappedCharset     bool
		seedWideAt        int
		wantAltN          int
		wantPrimaryN      int
		wantAltColumns    int
		wantPrimaryColumn int
	}{
		{name: "nil", glyphs: nil, wantAltColumns: 8, wantPrimaryColumn: 8},
		{name: "empty", glyphs: []Glyph{}, wantAltColumns: 8, wantPrimaryColumn: 8},
		{
			name:              "all narrow",
			glyphs:            []Glyph{{Ch: 'a', Width: 1}, {Ch: 'b', Width: 1}},
			wantAltN:          2,
			wantPrimaryN:      2,
			wantAltColumns:    8,
			wantPrimaryColumn: 8,
		},
		{
			name:              "mixed widths and combining width",
			glyphs:            []Glyph{{Ch: 'a', Width: 1}, {Ch: '漢', Width: 2}, {Ch: '\u0301', Width: 0}, {Ch: 'z', Width: 1}},
			wantAltN:          4,
			wantPrimaryN:      4,
			wantAltColumns:    8,
			wantPrimaryColumn: 7,
		},
		{
			name:              "exact primary boundary",
			glyphs:            []Glyph{{Ch: '漢', Width: 2}, {Ch: '字', Width: 2}},
			pos:               term.Coordinates{X: 4},
			wantAltN:          2,
			wantPrimaryN:      2,
			wantAltColumns:    8,
			wantPrimaryColumn: 6,
		},
		{
			name:              "insufficient primary columns",
			glyphs:            []Glyph{{Ch: '漢', Width: 2}, {Ch: '字', Width: 2}},
			pos:               term.Coordinates{X: 5},
			wantAltN:          2,
			wantAltColumns:    8,
			wantPrimaryColumn: 8,
		},
		{
			name:              "wide target is atomic fallback",
			glyphs:            []Glyph{{Ch: 'a', Width: 1}, {Ch: 'b', Width: 1}},
			seedWideAt:        1,
			wantAltColumns:    8,
			wantPrimaryColumn: 8,
		},
		{
			name:              "concealed cursor",
			glyphs:            []Glyph{{Ch: '漢', Width: 2}},
			hidden:            true,
			wantAltColumns:    8,
			wantPrimaryColumn: 8,
		},
		{
			name:              "mapped charset",
			glyphs:            []Glyph{{Ch: '漢', Width: 2}},
			mappedCharset:     true,
			wantAltColumns:    8,
			wantPrimaryColumn: 8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, primary := range []bool{false, true} {
				kind := "alternate"
				if primary {
					kind = "primary"
				}
				t.Run(kind, func(t *testing.T) {
					var b screenTestBuffer
					if primary {
						b = newScreenTestPrimaryBuffer(8, 3)
					} else {
						b = newScreenTestAltBuffer(8, 3)
					}
					b.SetCursorAtScreen(tt.pos)
					b.SetCursorAttributes(screenTestAttributes())
					b.SetHiddenCursor(tt.hidden)
					if tt.mappedCharset {
						b.ConfigureCharset(vteparser.CharsetIndexG0, vteparser.StandardCharsetSpecialCharacterAndLineDrawing)
					}
					if tt.seedWideAt > 0 {
						rows := b.cellBuffer().CopyRows(nil)
						rows[0][tt.seedWideAt] = screenTestCell('界', 2)
						b.cellBuffer().ResetCells(rows)
					}
					before := b.cellBuffer().CopyRows(nil)
					cursor := b.CursorAtScreen()

					got := b.WriteGlyphRun(tt.glyphs, vteparser.CharsetIndexG0)
					wantN, wantColumns := tt.wantAltN, tt.wantAltColumns
					if primary {
						wantN, wantColumns = tt.wantPrimaryN, tt.wantPrimaryColumn
					}
					assert.Equal(t, wantN, got)
					assert.Equal(t, wantColumns, b.Columns(0))
					assert.Equal(t, cursor, b.CursorAtScreen())
					if wantN == 0 {
						assert.Equal(t, before, b.cellBuffer().CopyRows(nil))
						return
					}
					row := b.cellBuffer().Row(0)
					for i, glyph := range tt.glyphs {
						at := tt.pos.X + i
						require.Less(t, at, len(row))
						assert.Equal(t, screenTestGlyphCell(glyph), row[at])
					}
				})
			}
		})
	}
}

func TestAdvanceColumnsTable(t *testing.T) {
	alt := NewAltBuffer()
	primary := NewPrimaryBuffer(0, testHistory)
	for _, width := range []int{-1, 0, 1, 2, 3} {
		t.Run("alternate/"+string(rune('0'+width+1)), func(t *testing.T) {
			assert.Equal(t, 1, alt.AdvanceColumns(width))
		})
		want := max(1, width)
		t.Run("primary/"+string(rune('0'+width+1)), func(t *testing.T) {
			assert.Equal(t, want, primary.AdvanceColumns(width))
		})
	}
}

func TestPrevCellAtCursorTable(t *testing.T) {
	tests := []struct {
		name    string
		primary bool
		cursor  term.Coordinates
		wantX   int
		want    *term.Cell
	}{
		{name: "alternate start", cursor: term.Coordinates{}, wantX: -1},
		{name: "alternate previous wide", cursor: term.Coordinates{X: 2}, wantX: 1, want: screenTestCellPtr('漢', 2)},
		{name: "primary start", primary: true, cursor: term.Coordinates{}, wantX: -1},
		{name: "primary previous combining", primary: true, cursor: term.Coordinates{X: 4}, wantX: 2, want: screenTestCombiningCellPtr()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b screenTestBuffer
			if tt.primary {
				b = newScreenTestPrimaryBuffer(6, 3)
			} else {
				b = newScreenTestAltBuffer(6, 3)
			}
			rows := b.cellBuffer().CopyRows(nil)
			rows[0][0] = screenTestCell('a', 1)
			rows[0][1] = screenTestCell('漢', 2)
			rows[0][2] = *screenTestCombiningCellPtr()
			b.cellBuffer().ResetCells(rows)
			b.SetCursorAtScreen(tt.cursor)

			got := b.PrevCellAtCursor()
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tt.want, *got)
			got.Bg = term.ColorRed
			cell, ok := b.cellBuffer().Cell(term.Coordinates{X: tt.wantX})
			require.True(t, ok)
			assert.Equal(t, term.ColorRed, cell.Bg)
		})
	}
}

func TestPrimaryBulkWritesAfterHistoryRingWrapTable(t *testing.T) {
	tests := []struct {
		name        string
		cursor      term.Coordinates
		seed        func([][]term.Cell)
		write       func(*PrimaryBuffer) int
		wantN       int
		want        func([][]term.Cell)
		wantColumns int
	}{
		{
			name:   "ascii run targets first visible logical row",
			cursor: term.Coordinates{X: 2},
			seed: func(rows [][]term.Cell) {
				rows[2][2] = *screenTestCombiningCellPtr()
				rows[2][3] = screenTestCell('界', 1)
			},
			write: func(b *PrimaryBuffer) int {
				return b.WriteRun([]byte("XY"), vteparser.CharsetIndexG0)
			},
			wantN: 2,
			want: func(rows [][]term.Cell) {
				rows[2][2] = screenTestWrittenCell('X')
				rows[2][3] = screenTestWrittenCell('Y')
			},
			wantColumns: 8,
		},
		{
			name:   "wide glyph run drops covered cells in middle visible row",
			cursor: term.Coordinates{X: 1, Y: 1},
			write: func(b *PrimaryBuffer) int {
				return b.WriteGlyphRun([]Glyph{
					{Ch: '漢', Width: 2},
					{Ch: '字', Width: 2},
				}, vteparser.CharsetIndexG0)
			},
			wantN: 2,
			want: func(rows [][]term.Cell) {
				row := rows[3]
				row[1] = screenTestGlyphCell(Glyph{Ch: '漢', Width: 2})
				row[2] = screenTestGlyphCell(Glyph{Ch: '字', Width: 2})
				copy(row[3:], row[5:])
				rows[3] = row[:len(row)-2]
			},
			wantColumns: 6,
		},
		{
			name:   "zero width glyph occupies one primary column",
			cursor: term.Coordinates{X: 3, Y: 2},
			seed: func(rows [][]term.Cell) {
				rows[4][3] = *screenTestCombiningCellPtr()
			},
			write: func(b *PrimaryBuffer) int {
				return b.WriteGlyphRun([]Glyph{
					{Ch: '\u0301', Width: 0},
					{Ch: 'z', Width: 1},
				}, vteparser.CharsetIndexG0)
			},
			wantN: 2,
			want: func(rows [][]term.Cell) {
				rows[4][3] = screenTestGlyphCell(Glyph{Ch: '\u0301', Width: 0})
				rows[4][4] = screenTestGlyphCell(Glyph{Ch: 'z', Width: 1})
			},
			wantColumns: 8,
		},
		{
			name:   "ascii run falls back atomically on wide target",
			cursor: term.Coordinates{X: 2},
			seed: func(rows [][]term.Cell) {
				rows[2][2] = screenTestCell('漢', 2)
				rows[2] = append(rows[2][:3], rows[2][4:]...)
			},
			write: func(b *PrimaryBuffer) int {
				return b.WriteRun([]byte("XY"), vteparser.CharsetIndexG0)
			},
			wantColumns: 7,
		},
		{
			name:   "glyph run falls back atomically on covered wide cell",
			cursor: term.Coordinates{X: 1, Y: 1},
			seed: func(rows [][]term.Cell) {
				rows[3][3] = screenTestCell('界', 2)
				rows[3] = append(rows[3][:4], rows[3][5:]...)
			},
			write: func(b *PrimaryBuffer) int {
				return b.WriteGlyphRun([]Glyph{{Ch: '漢', Width: 2}, {Ch: '字', Width: 2}}, vteparser.CharsetIndexG0)
			},
			wantColumns: 7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := primaryRingWriteRows(8, 5)
			if tt.seed != nil {
				tt.seed(rows)
			}
			b := newHistoryWrappedPrimaryBuffer(t, 8, 3, rows)
			b.SetCursorAtScreen(tt.cursor)
			cursorScreen := b.CursorAtScreen()
			cursorScroll := b.CursorAtScroll()
			before := b.Cells.CopyRows(nil)
			want := cloneScreenRows(before)
			if tt.want != nil {
				tt.want(want)
			}

			got := tt.write(b)

			assert.Equal(t, tt.wantN, got)
			assert.Equal(t, cursorScreen, b.CursorAtScreen())
			assert.Equal(t, cursorScroll, b.CursorAtScroll())
			assert.Equal(t, want, b.Cells.CopyRows(nil))
			assert.Equal(t, tt.wantColumns, b.Columns(cursorScroll.Y))
			visualColumns := 0
			for _, cell := range b.Cells.Row(cursorScroll.Y) {
				visualColumns += b.AdvanceColumns(int(cell.Width))
			}
			assert.Equal(t, 8, visualColumns)
		})
	}
}

func newHistoryWrappedPrimaryBuffer(
	t *testing.T, width, height int, rows [][]term.Cell,
) *PrimaryBuffer {
	t.Helper()
	b := NewPrimaryBuffer(0, len(rows))
	b.SetDefaultChar(' ')
	b.Resize(width, height)
	b.SetCursorAttributes(screenTestAttributes())
	for b.Rows() < len(rows) {
		b.ScrollUpHistory(1)
	}
	recycled := &b.Cells.MutableRow(0, width)[0]
	b.ScrollUpHistory(1)
	assert.Same(t, recycled, &b.Cells.MutableRow(b.Rows()-1, width)[0])
	for y, row := range rows {
		b.Cells.SetRow(y, cloneScreenRow(row))
	}
	return b
}

func primaryRingWriteRows(width, count int) [][]term.Cell {
	rows := make([][]term.Cell, count)
	for y := range rows {
		rows[y] = make([]term.Cell, width)
		for x := range rows[y] {
			rows[y][x] = screenTestCell(rune('A'+y*width+x), 1)
		}
	}
	return rows
}

type screenTestBuffer interface {
	WriteGlyphRun([]Glyph, vteparser.CharsetIndex) int
	SetCursorAtScreen(term.Coordinates)
	CursorAtScreen() term.Coordinates
	SetCursorAttributes(term.Attributes)
	SetHiddenCursor(bool)
	ConfigureCharset(vteparser.CharsetIndex, vteparser.StandardCharset)
	Columns(int) int
	PrevCellAtCursor() *term.Cell
	cellBuffer() *cell.Buffer
}

func (b *AltBuffer) cellBuffer() *cell.Buffer { return &b.Cells }

func (b *PrimaryBuffer) cellBuffer() *cell.Buffer { return &b.Cells }

func newScreenTestAltBuffer(width, height int) *AltBuffer {
	b := NewAltBuffer()
	b.SetDefaultChar(' ')
	b.Resize(width, height)
	return b
}

func newScreenTestPrimaryBuffer(width, height int) *PrimaryBuffer {
	b := NewPrimaryBuffer(0, testHistory)
	b.SetDefaultChar(' ')
	b.Resize(width, height)
	return b
}

func screenTestAttributes() term.Attributes {
	return term.Attributes{
		Fg:    term.ColorRed,
		Bg:    term.ColorBlue,
		Attrs: term.AttrBold | term.AttrUnderline,
	}
}

func screenTestCell(ch rune, width uint8) term.Cell {
	return term.Cell{
		Ch:    ch,
		Width: width,
		Bytes: uint8(len(string(ch))),
		Fg:    term.ColorGreen,
		Bg:    term.ColorPurple,
		Attrs: term.AttrItalic,
	}
}

func screenTestWrittenCell(ch rune) term.Cell {
	cell := term.Cell{Ch: ch, Width: 1}
	cell.SetAttributes(screenTestAttributes())
	return cell
}

func screenTestGlyphCell(glyph Glyph) term.Cell {
	cell := term.Cell{Ch: glyph.Ch, Width: glyph.Width}
	cell.SetAttributes(screenTestAttributes())
	return cell
}

func screenTestCellPtr(ch rune, width uint8) *term.Cell {
	cell := screenTestCell(ch, width)
	return &cell
}

func screenTestCombiningCellPtr() *term.Cell {
	cell := screenTestCell('e', 1)
	cell.SetCombining([]rune{'\u0301'})
	return &cell
}

func TestAltBufferDeleteAtRingTable(t *testing.T) {
	tests := []struct {
		name  string
		pos   term.Coordinates
		count int
	}{
		{name: "middle", pos: term.Coordinates{X: 1, Y: 2}, count: 2},
		{name: "last cell", pos: term.Coordinates{X: 4, Y: 3}, count: 1},
		{name: "count clips at row end", pos: term.Coordinates{X: 2, Y: 1}, count: 99},
		{name: "zero count", pos: term.Coordinates{X: 1, Y: 1}},
		{name: "negative count", pos: term.Coordinates{X: 1, Y: 1}, count: -1},
		{name: "x at columns", pos: term.Coordinates{X: 5, Y: 1}, count: 1},
		{name: "x beyond columns", pos: term.Coordinates{X: 8, Y: 1}, count: 1},
		{name: "negative x", pos: term.Coordinates{X: -1, Y: 1}, count: 1},
		{name: "negative y", pos: term.Coordinates{X: 1, Y: -1}, count: 1},
		{name: "y at rows", pos: term.Coordinates{X: 1, Y: 4}, count: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := screenRingRows(5, 4)
			b := newWrappedScreenAltBuffer('.', term.ColorBlue, rows)
			b.SetCursorAtScreen(term.Coordinates{X: 4})
			cursor := b.CursorAtScreen()
			want := cloneScreenRows(rows)
			modelScreenDelete(want, tt.pos, tt.count, screenResetCell('.', term.ColorBlue))

			assert.NotPanics(t, func() { b.DeleteAt(tt.pos, tt.count) })

			assert.Equal(t, cursor, b.CursorAtScreen())
			assert.Equal(t, want, b.Cells.CopyRows(nil))
		})
	}
}

func TestAltBufferScrollRingTable(t *testing.T) {
	tests := []struct {
		name      string
		down      bool
		start     int
		end       int
		count     int
		defaultCh rune
	}{
		{name: "up full", start: 0, end: 5, count: 1, defaultCh: '.'},
		{name: "down full", down: true, start: 0, end: 5, count: 1, defaultCh: '.'},
		{name: "up top region", start: 0, end: 3, count: 2, defaultCh: ' '},
		{name: "down middle region", down: true, start: 1, end: 4, count: 1, defaultCh: ' '},
		{name: "up bottom region", start: 2, end: 5, count: 1, defaultCh: '.'},
		{name: "singleton clears", start: 2, end: 3, count: 1, defaultCh: '.'},
		{name: "count equals region", start: 1, end: 4, count: 3, defaultCh: '.'},
		{name: "count exceeds region", down: true, start: 1, end: 4, count: 8, defaultCh: '.'},
		{name: "zero count", start: 0, end: 5, count: 0, defaultCh: '.'},
		{name: "negative count", start: 0, end: 5, count: -1, defaultCh: '.'},
		{name: "negative start", start: -1, end: 3, count: 1, defaultCh: '.'},
		{name: "end beyond rows", down: true, start: 1, end: 6, count: 1, defaultCh: '.'},
		{name: "reversed region", start: 4, end: 2, count: 1, defaultCh: '.'},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := screenRingRows(5, 5)
			b := newWrappedScreenAltBuffer(tt.defaultCh, term.ColorPurple, rows)
			b.SetCursorAtScreen(term.Coordinates{X: 3, Y: 2})
			cursor := b.CursorAtScreen()
			want := cloneScreenRows(rows)
			modelScreenScroll(
				want, tt.start, tt.end, tt.count, tt.down,
				screenResetCell(tt.defaultCh, term.ColorPurple),
			)

			assert.NotPanics(t, func() {
				if tt.down {
					b.ScrollDown(tt.start, tt.end, tt.count)
				} else {
					b.ScrollUp(tt.start, tt.end, tt.count)
				}
			})

			assert.Equal(t, cursor, b.CursorAtScreen())
			assert.Equal(t, want, b.Cells.CopyRows(nil))
		})
	}
}

func TestAltBufferResetCellsAtTable(t *testing.T) {
	tests := []struct {
		name  string
		y     int
		start int
		end   int
	}{
		{name: "partial", y: 2, start: 1, end: 4},
		{name: "whole row", y: 3, start: 0, end: 5},
		{name: "empty", y: 1, start: 3, end: 3},
		{name: "empty beyond width", y: 1, start: 20, end: 20},
		{name: "reversed", y: 1, start: 4, end: 2},
		{name: "negative start", y: 1, start: -1, end: 2},
		{name: "negative y", y: -1, start: 0, end: 2},
		{name: "y at rows", y: 4, start: 0, end: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows := screenRingRows(5, 4)
			b := newWrappedScreenAltBuffer('.', term.ColorTeal, rows)
			want := cloneScreenRows(rows)
			if tt.y == len(want) && tt.start >= 0 && tt.start < tt.end {
				row := repeatScreenCell(term.NewCell('.', 1, term.Attributes{}), 5)
				want = append(want, row)
			}
			modelScreenReset(want, tt.y, tt.start, tt.end, screenResetCell('.', term.ColorTeal))

			assert.NotPanics(t, func() { b.resetCellsAt(tt.y, tt.start, tt.end, '.') })

			assert.Equal(t, want, b.Cells.CopyRows(nil))
		})
	}
}

func TestPrimaryScrollUpHistoryRingTable(t *testing.T) {
	tests := []struct {
		name       string
		maxHistory int
		counts     []int
	}{
		{name: "grows then repeatedly recycles", maxHistory: 6, counts: []int{1, 2, 1, 3, 2}},
		{name: "history below screen height", maxHistory: 2, counts: []int{2, 1, 4}},
		{name: "zero and negative counts", maxHistory: 5, counts: []int{0, -1, 1, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const width, height = 4, 3
			b := NewPrimaryBuffer(0, tt.maxHistory)
			b.SetDefaultChar('.')
			b.Resize(width, height)
			rows := screenRingRows(width, height)
			b.Cells.ResetCells(cloneScreenRows(rows))
			want := cloneScreenRows(rows)
			blank := term.Cell{Ch: '.', Width: 1, Bytes: 1}
			limit := max(tt.maxHistory, height)

			for step, count := range tt.counts {
				b.ScrollUpHistory(count)
				want = modelScreenAppendBounded(want, count, width, limit, blank)
				assert.Equal(t, want, b.Cells.CopyRows(nil), "step=%d count=%d", step, count)
			}
		})
	}
}

func TestPrimaryWriteConsumesCoveredCellsTable(t *testing.T) {
	tests := []struct {
		name     string
		initial  []term.Cell
		write    Glyph
		want     []term.Cell
		wantText string
	}{
		{
			name:     "narrow over narrow",
			initial:  screenGlyphRow("ABCD..", []uint8{1, 1, 1, 1, 1, 1}),
			write:    Glyph{Ch: 'X', Width: 1},
			want:     screenGlyphRow("XBCD..", []uint8{1, 1, 1, 1, 1, 1}),
			wantText: "XBCD..",
		},
		{
			name:     "wide consumes one narrow",
			initial:  screenGlyphRow("ABCD..", []uint8{1, 1, 1, 1, 1, 1}),
			write:    Glyph{Ch: 'X', Width: 2},
			want:     screenGlyphRow("XCD..", []uint8{2, 1, 1, 1, 1}),
			wantText: "XCD..",
		},
		{
			name:     "triple width consumes two narrows",
			initial:  screenGlyphRow("ABCD..", []uint8{1, 1, 1, 1, 1, 1}),
			write:    Glyph{Ch: 'X', Width: 3},
			want:     screenGlyphRow("XD..", []uint8{3, 1, 1, 1}),
			wantText: "XD..",
		},
		{
			name:     "narrow over wide inserts compensation blank",
			initial:  screenGlyphRow("ABCD.", []uint8{2, 1, 1, 1, 1}),
			write:    Glyph{Ch: 'X', Width: 1},
			want:     append([]term.Cell{term.NewCell('X', 1, term.Attributes{}), {Ch: '.', Width: 1, Bytes: 1}}, screenGlyphRow("BCD.", []uint8{1, 1, 1, 1})...),
			wantText: "X.BCD.",
		},
		{
			name:     "consumed wide glyph leaves compensation blank",
			initial:  screenGlyphRow("ABCD.", []uint8{1, 2, 1, 1, 1}),
			write:    Glyph{Ch: 'X', Width: 2},
			want:     append([]term.Cell{term.NewCell('X', 2, term.Attributes{}), {Ch: '.', Width: 1, Bytes: 1}}, screenGlyphRow("CD.", []uint8{1, 1, 1})...),
			wantText: "X.CD.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewPrimaryBuffer(0, testHistory)
			b.SetDefaultChar('.')
			b.Resize(6, 3)
			rows := [][]term.Cell{
				cloneScreenRow(tt.initial),
				repeatScreenCell(term.Cell{Ch: '.', Width: 1, Bytes: 1}, 6),
				repeatScreenCell(term.Cell{Ch: '.', Width: 1, Bytes: 1}, 6),
			}
			b.Cells.ResetCells(rows)
			b.SetCursorAtScreen(term.Coordinates{})

			b.Write(tt.write.Ch, int(tt.write.Width), vteparser.CharsetIndexG0)

			got := b.Cells.Row(0)
			tt.want[0].Bytes = 0
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantText, term.CellsToString([][]term.Cell{got}))
			columns := 0
			for _, cell := range got {
				columns += b.AdvanceColumns(int(cell.Width))
			}
			assert.Equal(t, 6, columns)
		})
	}
}

func newWrappedScreenAltBuffer(
	defaultCh rune, background term.Color, rows [][]term.Cell,
) *AltBuffer {
	b := NewAltBuffer()
	b.SetDefaultChar(defaultCh)
	b.Resize(len(rows[0]), len(rows))
	b.SetCursorAttributes(term.Attributes{Bg: background})
	b.Cells.ResetCells(cloneScreenRows(rows))
	b.Cells.RotateRows(0, len(rows), 1)
	for y, row := range rows {
		b.Cells.SetRow(y, cloneScreenRow(row))
	}
	return b
}

func screenRingRows(width, height int) [][]term.Cell {
	rows := make([][]term.Cell, height)
	for y := range rows {
		rows[y] = make([]term.Cell, width)
		for x := range rows[y] {
			ch := rune('A' + y*width + x)
			rows[y][x] = screenTestCell(ch, 1)
			rows[y][x].Fg = term.Color(1 + y)
			rows[y][x].Bg = term.Color(10 + x)
			rows[y][x].Attrs = term.AttrBold | term.AttrItalic
		}
	}
	if height > 2 && width > 2 {
		rows[2][1] = screenTestCell('漢', 2)
		rows[2][2] = *screenTestCombiningCellPtr()
	}
	if height > 3 && width > 3 {
		rows[3][3] = term.Cell{Ch: '\t', Width: 1, Bytes: 1}
	}
	return rows
}

func cloneScreenRows(rows [][]term.Cell) [][]term.Cell {
	cloned := make([][]term.Cell, len(rows))
	for y, row := range rows {
		cloned[y] = cloneScreenRow(row)
	}
	return cloned
}

func cloneScreenRow(row []term.Cell) []term.Cell {
	if row == nil {
		return nil
	}
	cloned := append([]term.Cell(nil), row...)
	for x := range cloned {
		if combining := cloned[x].CombiningRunes(); combining != nil {
			cloned[x].SetCombining(append([]rune(nil), combining...))
		}
	}
	return cloned
}

func repeatScreenCell(fill term.Cell, count int) []term.Cell {
	row := make([]term.Cell, count)
	for x := range row {
		row[x] = fill
	}
	return row
}

func screenResetCell(ch rune, background term.Color) term.Cell {
	return term.NewCell(ch, 1, term.Attributes{Bg: background})
}

func modelScreenDelete(rows [][]term.Cell, pos term.Coordinates, count int, blank term.Cell) {
	if count <= 0 || pos.Y < 0 || pos.Y >= len(rows) || pos.X < 0 || pos.X >= len(rows[pos.Y]) {
		return
	}
	count = min(count, len(rows[pos.Y])-pos.X)
	copy(rows[pos.Y][pos.X:], rows[pos.Y][pos.X+count:])
	for x := len(rows[pos.Y]) - count; x < len(rows[pos.Y]); x++ {
		rows[pos.Y][x] = blank
	}
}

func modelScreenScroll(
	rows [][]term.Cell, start, end, count int, down bool, blank term.Cell,
) {
	if count <= 0 || start < 0 || end > len(rows) || start >= end {
		return
	}
	length := end - start
	if count >= length {
		for y := start; y < end; y++ {
			rows[y] = repeatScreenCell(blank, len(rows[y]))
		}
		return
	}
	if down {
		rotateScreenRows(rows, start, end, -count)
		for y := start; y < start+count; y++ {
			rows[y] = repeatScreenCell(blank, len(rows[y]))
		}
		return
	}
	rotateScreenRows(rows, start, end, count)
	for y := end - count; y < end; y++ {
		rows[y] = repeatScreenCell(blank, len(rows[y]))
	}
}

func rotateScreenRows(rows [][]term.Cell, start, end, count int) {
	length := end - start
	count %= length
	if count < 0 {
		count += length
	}
	if count == 0 {
		return
	}
	copyOfRows := append([][]term.Cell(nil), rows[start:end]...)
	copy(rows[start:end-count], copyOfRows[count:])
	copy(rows[end-count:end], copyOfRows[:count])
}

func modelScreenReset(rows [][]term.Cell, y, start, end int, blank term.Cell) {
	if y < 0 || y >= len(rows) || start < 0 || start >= end {
		return
	}
	end = min(end, len(rows[y]))
	if start >= end {
		return
	}
	for x := start; x < end; x++ {
		rows[y][x] = blank
	}
}

func modelScreenAppendBounded(
	rows [][]term.Cell, count, width, limit int, blank term.Cell,
) [][]term.Cell {
	if count <= 0 || width < 0 || limit <= 0 {
		return rows
	}
	if excess := len(rows) - limit; excess > 0 {
		rows = append([][]term.Cell(nil), rows[excess:]...)
	}
	for range count {
		row := repeatScreenCell(blank, width)
		if len(rows) < limit {
			rows = append(rows, row)
		} else {
			copy(rows, rows[1:])
			rows[len(rows)-1] = row
		}
	}
	return rows
}

func screenGlyphRow(chars string, widths []uint8) []term.Cell {
	runes := []rune(chars)
	if len(runes) != len(widths) {
		panic("screenGlyphRow: chars and widths differ")
	}
	row := make([]term.Cell, len(runes))
	for i, ch := range runes {
		row[i] = term.Cell{Ch: ch, Width: widths[i], Bytes: uint8(len(string(ch)))}
	}
	return row
}
