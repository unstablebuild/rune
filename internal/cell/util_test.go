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
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestConvertByteOffset(t *testing.T) {
	tsuite := []struct {
		cells    [][]term.Cell
		offset   int
		expected term.Coordinates
		ok       bool
	}{
		{nil, 0, term.Coordinates{}, false},
		{[][]term.Cell{}, 0, term.Coordinates{}, false},
		{[][]term.Cell{{}}, 0, term.Coordinates{}, true},
		{[][]term.Cell{{}}, 1, term.Coordinates{Y: 1}, true},
		{[][]term.Cell{{{Ch: 'a'}}}, 0, term.Coordinates{}, true},
		{[][]term.Cell{{{Ch: 'a'}}}, 1, term.Coordinates{X: 1}, true},
		{[][]term.Cell{{{Ch: 'a'}}}, 2, term.Coordinates{Y: 1}, true},
		{[][]term.Cell{{{Ch: 'a'}}, {{Ch: 'b'}}}, 2, term.Coordinates{Y: 1, X: 0}, true},
		{[][]term.Cell{{{Ch: 'a'}}, {{Ch: 'b'}}}, 3, term.Coordinates{Y: 1, X: 1}, true},
		{[][]term.Cell{{{Ch: 'a'}}, {{Ch: 'b'}}}, 4, term.Coordinates{Y: 2}, true},
		{[][]term.Cell{{{Ch: '\t'}, {Ch: 'a'}}}, 1, term.Coordinates{X: 1}, true},
		{[][]term.Cell{{}, {{Ch: '\t'}, {Ch: 'a'}}}, 2, term.Coordinates{Y: 1, X: 1}, true},
		{[][]term.Cell{{}, {{Ch: '\t'}, {Ch: 'a'}}}, 3, term.Coordinates{Y: 1, X: 2}, true},
		{[][]term.Cell{{}, {{Ch: '\t'}, {Ch: 'a'}}}, 1, term.Coordinates{Y: 1, X: 0}, true},
		{[][]term.Cell{{}, {{Ch: '\t'}, {Ch: 'a'}}}, 4, term.Coordinates{Y: 2}, true},
		{
			[][]term.Cell{{{Ch: '💥'}, {Ch: 0}, {Ch: 'a'}}},
			4, term.Coordinates{Y: 0, X: 1}, true,
		},
		{
			[][]term.Cell{{{Ch: '💥'}, {Ch: 0}, {Ch: 'a'}}},
			len([]byte(string('💥'))), term.Coordinates{Y: 0, X: 1}, true,
		},
		{
			[][]term.Cell{{{Ch: '💥'}, {Ch: 0}, {Ch: 'a'}}},
			0, term.Coordinates{Y: 0, X: 0}, true,
		},
		{
			[][]term.Cell{{{Ch: '👨', Extra: &term.CellExtra{Combining: []rune{
				rune(8205),
				rune(128103),
				rune(8205),
				rune(128102),
			}}, Width: 2, Bytes: 18}, {Ch: 0}, {Ch: 'a'}}},
			18, term.Coordinates{Y: 0, X: 1}, true,
		},
		{
			[][]term.Cell{{{Ch: '👨', Extra: &term.CellExtra{Combining: []rune{
				rune(8205),
				rune(128103),
				rune(8205),
				rune(128102),
			}}, Width: 2, Bytes: 18}, {Ch: 'a'}}},
			19, term.Coordinates{Y: 0, X: 2}, true,
		},
	}

	for i, tcase := range tsuite {
		t.Run(fmt.Sprintf("ConvertByteOffsetToCoordinates, test case %d", i),
			func(t *testing.T) {
				// use cells coming from buffer, rather than fixture cells
				// so Bytes is calculcated for us, but don't do it for the cases
				// where raw cells is un-initialized or nil, we also want to test those
				cells := tcase.cells
				if len(cells) != 0 {
					buf := NewBuffer()
					str := term.CellsToString(tcase.cells)
					_, err := buf.ReadFrom(strings.NewReader(str))
					require.NoError(t, err)
					cells = buf.RawCells()
				}
				actual, ok := ConvertByteOffsetToCoordinates(cells, tcase.offset)
				require.Equal(t, tcase.ok, ok, i)
				if ok {
					assert.Equal(t, tcase.expected, actual, i)
				}
			})
		t.Run(fmt.Sprintf("ConvertCoordinatesToByteOffset, test case %d", i),
			func(t *testing.T) {
				cells := tcase.cells
				if len(cells) != 0 {
					buf := NewBuffer()
					var bbuf bytes.Buffer
					CellsToBytesBuffer(&bbuf, tcase.cells)
					str := bbuf.String()
					_, err := buf.ReadFrom(strings.NewReader(str))
					require.NoError(t, err)
					cells = buf.RawCells()
				}
				actual, ok := ConvertCoordinatesToByteOffset(cells, tcase.expected)
				require.Equal(t, tcase.ok, ok, i)
				if ok {
					assert.Equal(t, tcase.offset, actual, i)
				}
			})
	}
}

func TestConvertCoordinatesToRunePos(t *testing.T) {
	tsuite := []struct {
		cells [][]term.Cell
		y, x  int
		out   term.Coordinates
		ok    bool
	}{
		{nil, 0, 0, term.Coordinates{}, false},
		{[][]term.Cell{}, 0, 0, term.Coordinates{}, false},
		{[][]term.Cell{{{Ch: 'a'}}}, 0, 0, term.Coordinates{}, true},
		{[][]term.Cell{{{Ch: 'a'}}}, 1, 0, term.Coordinates{Y: 1}, true},
		{[][]term.Cell{{{Ch: '\t'}, {Ch: 'a'}}}, 0, 1, term.Coordinates{X: 1}, true},
		{[][]term.Cell{{}, {{Ch: '\t'}, {Ch: 'a'}, {Ch: 0}}}, 1, 1, term.Coordinates{Y: 1, X: 1}, true},
		{[][]term.Cell{{}, {{Ch: '\t'}, {Ch: 'a'}, {Ch: 0}}}, 1, 0, term.Coordinates{Y: 1, X: 0}, true},
		{[][]term.Cell{{}, {{Ch: '\t'}, {Ch: 'a'}, {Ch: 0}}}, 1, 2, term.Coordinates{Y: 1, X: 2}, true},
		{
			[][]term.Cell{{{Ch: '💥'}, {Ch: 0}, {Ch: 'a'}}},
			0, len([]byte(string('💥'))), term.Coordinates{Y: 0, X: 1}, true,
		},
		{
			[][]term.Cell{{{Ch: '💥'}, {Ch: 'a'}}},
			0, len([]byte(string('💥'))) + 1, term.Coordinates{Y: 0, X: 2}, true,
		},
		{
			[][]term.Cell{{{Ch: '💥'}, {Ch: 'a'}}},
			0, 0, term.Coordinates{Y: 0, X: 0}, true,
		},
		{
			[][]term.Cell{{{Ch: '👨', Extra: &term.CellExtra{Combining: []rune{
				rune(8205),
				rune(128103),
				rune(8205),
				rune(128102),
			}}, Width: 2, Bytes: 18}, {Ch: 'a'}}},
			0, 18, term.Coordinates{Y: 0, X: 1}, true,
		},
		{
			[][]term.Cell{{{Ch: '👨', Extra: &term.CellExtra{Combining: []rune{
				rune(8205),
				rune(128103),
				rune(8205),
				rune(128102),
			}}, Width: 2, Bytes: 18}, {Ch: 'a'}}},
			0, 19, term.Coordinates{Y: 0, X: 2}, true,
		},
	}

	for i, tcase := range tsuite {
		t.Run("ConvertRunePosToCoordinates", func(t *testing.T) {
			cells := tcase.cells
			if len(cells) != 0 {
				buf := NewBuffer()
				str := term.CellsToString(tcase.cells)
				_, err := buf.ReadFrom(strings.NewReader(str))
				require.NoError(t, err)
				cells = buf.RawCells()
			}
			out, ok := ConvertRunePosToCoordinates(cells, tcase.y, tcase.x)
			require.Equal(t, tcase.ok, ok, i)
			assert.Equal(t, tcase.out, out, i)
		})
	}
	for i, tcase := range tsuite {
		t.Run("ConvertCoordinatesToRunePos", func(t *testing.T) {
			cells := tcase.cells
			if len(cells) != 0 {
				buf := NewBuffer()
				str := term.CellsToString(tcase.cells)
				_, err := buf.ReadFrom(strings.NewReader(str))
				require.NoError(t, err)
				cells = buf.RawCells()
			}
			y, x, ok := ConvertCoordinatesToRunePos(cells, tcase.out)
			require.Equal(t, tcase.ok, ok, i)
			assert.Equal(t, tcase.x, x, i)
			assert.Equal(t, tcase.y, y, i)
		})
	}
}

func TestCellsToBufferZero(t *testing.T) {
	t.Run("returned buffer should always have at least one row", func(t *testing.T) {
		buf := CellsToBuffer(nil)
		require.Equal(t, 1, buf.Rows())
		assert.Equal(t, 0, buf.Columns(0))
	})
}

func TestCellsToBuffer(t *testing.T) {
	const (
		filecontent1 = `package me.drton.jmavsim;
public class Rotor {
     sta	tmtyp;


  myClass;
`
		insertStr = "\tmyClassVar\n"
	)
	insertAt := term.Coordinates{Y: 5, X: 9}

	abuf := NewBuffer()
	abuf.ReadFrom(strings.NewReader(filecontent1))
	astr0 := abuf.String()
	arcells0 := abuf.RawCells()

	bbuf := CellsToBuffer(arcells0)
	bstr0 := bbuf.String()
	brcells0 := bbuf.RawCells()
	require.Equal(t, arcells0, brcells0)

	afrom, ato, _ := abuf.editor.Edit(context.Background(), insertAt, insertAt, insertStr)
	astr1 := abuf.String()
	arcells1 := abuf.RawCells()

	abuf.Delete(afrom, ato)
	astr2 := abuf.String()
	arcells2 := abuf.RawCells()

	bfrom, bto, _ := bbuf.editor.Edit(context.Background(), insertAt, insertAt, insertStr)
	bstr1 := bbuf.String()
	brcells1 := bbuf.RawCells()

	bbuf.Delete(bfrom, bto)
	bstr2 := bbuf.String()
	brcells2 := bbuf.RawCells()

	require.Equal(t, filecontent1, astr0)
	require.Equal(t, filecontent1, bstr0)

	assert.Equal(t, astr1, bstr1)
	assert.Equal(t, arcells1, brcells1)

	assert.Equal(t, astr2, bstr2)
	assert.Equal(t, arcells2, brcells2)
}

func benchmarkCellToBuffer(b *testing.B, n int) {
	c := make([][]term.Cell, n)
	for i := range n {
		c[i] = make([]term.Cell, n)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = CellsToBuffer(c)
	}
}

func BenchmarkCellToBuffer10(b *testing.B) {
	benchmarkCellToBuffer(b, 10)
}

func BenchmarkCellToBuffer100(b *testing.B) {
	benchmarkCellToBuffer(b, 100)
}

func BenchmarkCellToBuffer1000(b *testing.B) {
	benchmarkCellToBuffer(b, 1000)
}

func BenchmarkCellToBuffer10000(b *testing.B) {
	benchmarkCellToBuffer(b, 10000)
}

// TestByteCountsMatchesCellConverters guards that the compact
// ByteCounts snapshot produces identical conversions to the full
// cell-based converters, including multi-byte and grapheme-cluster
// content where Cell.Bytes spans several codepoints.
func TestByteCountsMatchesCellConverters(t *testing.T) {
	buf := NewBuffer()
	_, err := buf.ReadFrom(strings.NewReader(
		"plain line\n\t中国 wide\n💥 emoji\n\nshort"))
	require.NoError(t, err)
	cells := buf.RawCells()
	counts := NewByteCounts(nil, cells)

	var total int
	for _, row := range cells {
		for _, c := range row {
			total += int(c.Bytes)
		}
		total++ // \n
	}

	for offset := 0; offset <= total; offset++ {
		want, wok := ConvertByteOffsetToCoordinates(cells, offset)
		got, gok := counts.ByteOffsetToCoordinates(offset)
		require.Equal(t, wok, gok, "offset %d", offset)
		require.Equal(t, want, got, "offset %d", offset)
	}

	for y := 0; y <= len(cells); y++ {
		maxX := 0
		if y < len(cells) {
			maxX = len(cells[y])
		}
		for x := 0; x <= maxX+1; x++ {
			pos := term.Coordinates{Y: y, X: x}

			wantOff, wok := ConvertCoordinatesToByteOffset(cells, pos)
			gotOff, gok := counts.CoordinatesToByteOffset(pos)
			require.Equal(t, wok, gok, "pos %v", pos)
			require.Equal(t, wantOff, gotOff, "pos %v", pos)

			wy, wx, wok2 := ConvertCoordinatesToRunePos(cells, pos)
			gy, gx, gok2 := counts.CoordinatesToRunePos(pos)
			require.Equal(t, wok2, gok2, "pos %v", pos)
			require.Equal(t, wy, gy, "pos %v", pos)
			require.Equal(t, wx, gx, "pos %v", pos)

			want, wok3 := ConvertRunePosToCoordinates(cells, y, x)
			got, gok3 := counts.RunePosToCoordinates(y, x)
			require.Equal(t, wok3, gok3, "rune pos y=%d x=%d", y, x)
			require.Equal(t, want, got, "rune pos y=%d x=%d", y, x)
		}
	}
}
