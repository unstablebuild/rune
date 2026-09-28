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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestWriterContext(t *testing.T) {
	type myKey string

	writer := NewBufferWriter(
		context.WithValue(context.Background(), myKey("a"), "a"), 10, 10)

	value := writer.Context().Value(myKey("a"))
	require.NotNil(t, value)
	str, ok := value.(string)
	require.True(t, ok)
	assert.Equal(t, "a", str)

	writer.SetContext(
		context.WithValue(context.Background(), myKey("b"), "b"),
	)

	value = writer.Context().Value(myKey("a"))
	require.Nil(t, value)

	value = writer.Context().Value(myKey("b"))
	require.NotNil(t, value)
	str, ok = value.(string)
	require.True(t, ok)
	assert.Equal(t, "b", str)
}

func TestWriteFlush(t *testing.T) {
	width, height := 5, 6
	writer := NewBufferWriter(context.Background(), width, height)

	c := 'E'
	for i := width - 1; i >= 0; i-- {
		for j := height - 1; j >= 0; j-- {
			if i > j-1 {
				writer.SetCell(term.Coordinates{X: j, Y: i}, term.Cell{Ch: c})
			}
		}
		c--
	}

	// should be fine to wtry to write out of bounds
	writer.SetCell(term.Coordinates{X: width + 1, Y: height + 1}, term.Cell{Ch: '='})

	require.NoError(t, writer.Flush())

	expected := "A\x00\x00\x00\x00\nBB\x00\x00\x00\n" +
		"CCC\x00\x00\nDDDD\x00\nEEEEE\n\x00\x00\x00\x00\x00"
	assert.Equal(t, expected, term.CellsToString(writer.RawCells()))
}

func TestBufferWriterSetCellOutOfBounds(t *testing.T) {
	const width, height = 3, 2
	for _, pos := range []term.Coordinates{
		{X: -1}, {Y: -1}, {X: width}, {Y: height}, {X: width, Y: height}, {X: -1, Y: -1},
	} {
		writer := NewBufferWriter(context.Background(), width, height)
		assert.NotPanics(t, func() { writer.SetCell(pos, term.Cell{Ch: 'x'}) }, "%v", pos)
		assert.Equal(t, "\x00\x00\x00\n\x00\x00\x00", term.CellsToString(writer.RawCells()), "%v", pos)
	}
}

func TestBufferWriterClear(t *testing.T) {
	tests := []struct {
		name string
		attr term.Attributes
	}{
		{name: "default attributes"},
		{name: "colours", attr: term.Attributes{Fg: term.ColorRed, Bg: term.ColorBlue, Attrs: term.AttrBold}},
		{name: "underline colour", attr: term.Attributes{Attrs: term.AttrUnderline, Underline: term.ColorGreen}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const width, height = 7, 3
			writer := NewBufferWriter(context.Background(), width, height)
			for y := range height {
				for x := range width {
					writer.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('漢', 2, term.Attributes{Fg: term.ColorRed}))
				}
			}
			require.NoError(t, writer.Clear(tt.attr))
			want := term.NewCell(0, 0, tt.attr)
			for y, row := range writer.RawCells() {
				require.Len(t, row, width)
				for x, c := range row {
					assert.Equal(t, want, c, "(%d,%d)", x, y)
				}
			}
		})
	}
}

func benchBufferWriter(b *testing.B, n int) {
	width, height := n, n
	writer := NewBufferWriter(context.Background(), width, height)
	for i := 0; i < b.N; i++ {
		writer.Clear(term.Attributes{})
		c := 'E'
		for i := width - 1; i >= 0; i-- {
			for j := height - 1; j >= 0; j-- {
				if i > j-1 {
					writer.SetCell(term.Coordinates{X: j, Y: i}, term.Cell{Ch: c})
				}
			}
			c--
		}
	}
}

func BenchmarkBufferWriter10(b *testing.B) {
	benchBufferWriter(b, 10)
}
func BenchmarkBufferWriter100(b *testing.B) {
	benchBufferWriter(b, 100)
}
func BenchmarkBufferWriter1000(b *testing.B) {
	benchBufferWriter(b, 1000)
}

// BenchmarkBufferWriterFrame measures the writer as the GUI drives it
// every frame: Clear with the theme's attributes, then a handler setting
// every cell through term.Writer.
func BenchmarkBufferWriterFrame(b *testing.B) {
	themed := term.Attributes{Fg: term.NewColor(200, 200, 200), Bg: term.NewColor(30, 30, 30)}
	underlined := term.Attributes{Attrs: term.AttrUnderline, Underline: term.ColorGreen}
	glyph := term.NewCell('x', 1, themed)
	sizes := []struct {
		name          string
		width, height int
	}{
		{name: "1080p", width: 213, height: 60},
		{name: "2160p", width: 426, height: 120},
	}
	for _, size := range sizes {
		run := func(name string, frame func(w *BufferWriter)) {
			b.Run(size.name+"/"+name, func(b *testing.B) {
				w := NewBufferWriter(context.Background(), size.width, size.height)
				b.ReportAllocs()
				for b.Loop() {
					frame(w)
				}
			})
		}
		run("clear", func(w *BufferWriter) {
			_ = w.Clear(themed)
		})
		run("clear-underlined", func(w *BufferWriter) {
			_ = w.Clear(underlined)
		})
		run("set-cells", func(w *BufferWriter) {
			setEveryCell(w, size.width, size.height, glyph)
		})
		run("frame", func(w *BufferWriter) {
			_ = w.Clear(themed)
			setEveryCell(w, size.width, size.height, glyph)
		})
	}
}

// setEveryCell writes c to every cell of a width x height grid.
// Handlers reach SetCell through term.Writer, so it must not be inlined
// into a caller that knows the concrete writer and devirtualizes the call.
//
//go:noinline
func setEveryCell(w term.Writer, width, height int, c term.Cell) {
	for y := range height {
		for x := range width {
			w.SetCell(term.Coordinates{X: x, Y: y}, c)
		}
	}
}
