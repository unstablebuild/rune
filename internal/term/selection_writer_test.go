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

package term

import (
	"context"
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/asciiart"
)

func TestSelectionWriterImages(t *testing.T) {
	picture := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for x := range 4 {
		picture.SetNRGBA(x, 0, color.NRGBA{R: 255, G: 255, B: 255, A: 255})
		picture.SetNRGBA(x, 1, color.NRGBA{A: 255})
	}
	for _, tc := range []struct {
		name     string
		x        int
		after    func(*SelectionWriter)
		expected string
	}{
		{name: "pixels and caption", x: 2, expected: "caption \n  ####  \n  ....  \n        "},
		{name: "clipped", x: -2, expected: "caption \n##      \n..      \n        "},
		{name: "later cells cover image", x: 2, after: func(g *SelectionWriter) {
			g.SetCell(term.Coordinates{X: 3, Y: 1}, term.NewCell('X', 1, term.Attributes{}))
		}, expected: "caption \n  #X##  \n  ....  \n        "},
		{name: "clear releases image", x: 2, after: func(g *SelectionWriter) { g.Clear() }, expected: "        \n        \n        \n        "},
		{name: "resize releases image", x: 2, after: func(g *SelectionWriter) { g.Resize(8, 4) }, expected: "        \n        \n        \n        "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewSelectionWriter(8, 4)
			g.SetContext(term.ContextWithCellPixelSize(t.Context(), 10, 23))
			writeRow(g, 0, "caption")
			require.True(t, g.DrawImage(term.Image{Src: picture, Pos: term.Coordinates{X: tc.x, Y: 1}, Width: 4, Height: 2, Fit: term.ImageFitFill}))
			require.Equal(t, "caption", g.TextBetween(term.Coordinates{}, term.Coordinates{X: 7, Y: 3}))
			if tc.after != nil {
				tc.after(g)
			}
			cfg := asciiart.DefaultConfig()
			cfg.DensityCharacters = ".#"
			w := asciiart.NewStringWriter(8, 4, cfg)
			for range 2 {
				require.NoError(t, w.Clear(term.Attributes{}))
				g.Dump(w, &SelRange{Start: term.Coordinates{}, End: term.Coordinates{X: 6}, Active: true})
				require.NoError(t, w.Flush())
				assert.Equal(t, tc.expected, w.String())
				assert.NotZero(t, w.Cells()[0].Attributes().Attrs&term.AttrReverse)
			}
		})
	}
}

// writeRow paints text starting at column 0 of row y, ignoring runes
// that would overflow the grid width.
func writeRow(g *SelectionWriter, y int, text string) {
	x := 0
	for _, ch := range text {
		if x >= g.Width() {
			break
		}
		g.SetCell(term.Coordinates{X: x, Y: y}, term.Cell{Ch: ch})
		x++
	}
}

func TestSelectionWriterResize(t *testing.T) {
	t.Run("dimensions and clear", func(t *testing.T) {
		g := NewSelectionWriter(8, 3)
		assert.Equal(t, 8, g.Width())
		assert.Equal(t, 3, g.Height())
		writeRow(g, 0, "abc")
		g.Resize(4, 2)
		assert.Equal(t, 4, g.Width())
		assert.Equal(t, 2, g.Height())
		// Resize clears: nothing selectable remains.
		_, _, ok := g.LineBounds(0)
		assert.False(t, ok)
	})

	t.Run("shrinking clears the frame", func(t *testing.T) {
		g := NewSelectionWriter(10, 10)
		writeRow(g, 0, "x")
		g.Resize(5, 5)
		writeRow(g, 0, "hello")
		assert.Equal(t, "hello", g.TextBetween(
			term.Coordinates{X: 0, Y: 0}, term.Coordinates{X: 4, Y: 0}))
	})

	t.Run("zero size does not panic", func(t *testing.T) {
		assert.NotPanics(t, func() {
			g := NewSelectionWriter(0, 0)
			g.SetCell(term.Coordinates{}, term.Cell{Ch: 'a'})
			g.UnionAttributes(term.Coordinates{}, term.Attributes{Attrs: term.AttrReverse})
			_, _, ok := g.WordBoundsAt(term.Coordinates{})
			assert.False(t, ok)
			_, _, ok = g.LineBounds(0)
			assert.False(t, ok)
			assert.Equal(t, "", g.TextBetween(term.Coordinates{}, term.Coordinates{}))
			g.Dump(term.NewStringWriter(1, 1), nil)
		})
	})
}

func TestSelectionWriterSetCellBounds(t *testing.T) {
	g := NewSelectionWriter(4, 2)
	cases := []term.Coordinates{
		{X: -1, Y: 0}, {X: 0, Y: -1}, {X: 4, Y: 0}, {X: 0, Y: 2},
		{X: -5, Y: -5}, {X: 100, Y: 100},
	}
	for _, pos := range cases {
		assert.NotPanics(t, func() {
			g.SetCell(pos, term.Cell{Ch: 'z'})
			g.UnionAttributes(pos, term.Attributes{Attrs: term.AttrReverse})
		}, "pos %+v", pos)
	}
	// No in-bounds cell was touched.
	_, _, ok := g.LineBounds(0)
	assert.False(t, ok)
}

func TestSelectionWriterTextBetween(t *testing.T) {
	type tc struct {
		name       string
		w, h       int
		rows       map[int]string
		start, end term.Coordinates
		want       string
	}
	cases := []tc{
		{
			name: "single line partial",
			w:    20, h: 3, rows: map[int]string{0: "Hello World"},
			start: term.Coordinates{X: 0, Y: 0}, end: term.Coordinates{X: 4, Y: 0},
			want: "Hello",
		},
		{
			name: "multi line",
			w:    20, h: 3, rows: map[int]string{0: "line one", 1: "line two"},
			start: term.Coordinates{X: 0, Y: 0}, end: term.Coordinates{X: 7, Y: 1},
			want: "line one\nline two",
		},
		{
			name: "trailing whitespace trimmed",
			w:    20, h: 3, rows: map[int]string{0: "hello   "},
			start: term.Coordinates{X: 0, Y: 0}, end: term.Coordinates{X: 19, Y: 0},
			want: "hello",
		},
		{
			name: "trailing empty rows trimmed",
			w:    10, h: 5, rows: map[int]string{0: "top"},
			start: term.Coordinates{X: 0, Y: 0}, end: term.Coordinates{X: 9, Y: 4},
			want: "top",
		},
		{
			name: "null cells skipped within a row",
			w:    10, h: 2, rows: map[int]string{0: "ab"}, // then a gap, then more
			start: term.Coordinates{X: 0, Y: 0}, end: term.Coordinates{X: 9, Y: 0},
			want: "ab",
		},
		{
			name: "reversed coordinates are sorted",
			w:    20, h: 2, rows: map[int]string{0: "abcdef"},
			start: term.Coordinates{X: 4, Y: 0}, end: term.Coordinates{X: 0, Y: 0},
			want: "abcde",
		},
		{
			name: "out-of-bounds rows skipped",
			w:    10, h: 2, rows: map[int]string{0: "row0", 1: "row1"},
			start: term.Coordinates{X: 0, Y: -3}, end: term.Coordinates{X: 9, Y: 9},
			want: "row0\nrow1",
		},
		{
			name: "negative start X clamps to row start",
			w:    10, h: 1, rows: map[int]string{0: "abc"},
			start: term.Coordinates{X: -5, Y: 0}, end: term.Coordinates{X: 2, Y: 0},
			want: "abc",
		},
		{
			name: "end X beyond width clamps",
			w:    10, h: 1, rows: map[int]string{0: "abc"},
			start: term.Coordinates{X: 0, Y: 0}, end: term.Coordinates{X: 999, Y: 0},
			want: "abc",
		},
		{
			name: "all empty yields empty string",
			w:    10, h: 3, rows: nil,
			start: term.Coordinates{X: 0, Y: 0}, end: term.Coordinates{X: 9, Y: 2},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := NewSelectionWriter(c.w, c.h)
			for y, s := range c.rows {
				writeRow(g, y, s)
			}
			assert.Equal(t, c.want, g.TextBetween(c.start, c.end))
		})
	}
}

func TestSelectionWriterTextBetweenCombiningRunes(t *testing.T) {
	g := NewSelectionWriter(5, 1)
	cell := term.Cell{Ch: 'e'}
	cell.SetCombining([]rune{0x0301}) // combining acute accent
	g.SetCell(term.Coordinates{X: 0, Y: 0}, cell)
	g.SetCell(term.Coordinates{X: 1, Y: 0}, term.Cell{Ch: 'x'})
	got := g.TextBetween(term.Coordinates{X: 0, Y: 0}, term.Coordinates{X: 1, Y: 0})
	assert.Equal(t, "e\u0301x", got)
}

func TestSelectionWriterWordBoundsAt(t *testing.T) {
	type tc struct {
		name              string
		row               string
		pos               term.Coordinates
		wantOK            bool
		wantStartX, wantE int
	}
	cases := []tc{
		{"middle of word", "alpha bravo", term.Coordinates{X: 2, Y: 0}, true, 0, 4},
		{"start of word", "alpha bravo", term.Coordinates{X: 6, Y: 0}, true, 6, 10},
		{"end of word", "alpha bravo", term.Coordinates{X: 10, Y: 0}, true, 6, 10},
		{"underscore is word char", "foo_bar baz", term.Coordinates{X: 1, Y: 0}, true, 0, 6},
		{"digits are word chars", "abc123 x", term.Coordinates{X: 4, Y: 0}, true, 0, 5},
		{"symbol run stops at letters", "a==b", term.Coordinates{X: 1, Y: 0}, true, 1, 2},
		{"whitespace selects nothing", "alpha bravo", term.Coordinates{X: 5, Y: 0}, false, 0, 0},
		{"null cell selects nothing", "ab", term.Coordinates{X: 5, Y: 0}, false, 0, 0},
		{"negative coords select nothing", "ab", term.Coordinates{X: -1, Y: 0}, false, 0, 0},
		{"row out of bounds", "ab", term.Coordinates{X: 0, Y: 9}, false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := NewSelectionWriter(20, 2)
			writeRow(g, 0, c.row)
			start, end, ok := g.WordBoundsAt(c.pos)
			require.Equal(t, c.wantOK, ok)
			if c.wantOK {
				assert.Equal(t, term.Coordinates{X: c.wantStartX, Y: c.pos.Y}, start)
				assert.Equal(t, term.Coordinates{X: c.wantE, Y: c.pos.Y}, end)
			}
		})
	}
}

func TestSelectionWriterWordBoundsAtRowEdges(t *testing.T) {
	g := NewSelectionWriter(5, 1)
	writeRow(g, 0, "abcde") // fills the whole row
	start, end, ok := g.WordBoundsAt(term.Coordinates{X: 2, Y: 0})
	require.True(t, ok)
	assert.Equal(t, term.Coordinates{X: 0, Y: 0}, start)
	assert.Equal(t, term.Coordinates{X: 4, Y: 0}, end)
}

func TestSelectionWriterLineBounds(t *testing.T) {
	type tc struct {
		name           string
		w, h           int
		row            string
		atX            int // starting column for the row content
		y              int
		wantOK         bool
		wantFirst, end int
	}
	cases := []tc{
		{"full row", 10, 2, "hello", 0, 0, true, 0, 4},
		{"leading gap", 10, 2, "hi", 2, 0, true, 2, 3},
		{"empty row", 10, 2, "", 0, 0, false, 0, 0},
		{"row out of bounds high", 10, 2, "x", 0, 9, false, 0, 0},
		{"row out of bounds negative", 10, 2, "x", 0, -1, false, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := NewSelectionWriter(c.w, c.h)
			for i, ch := range c.row {
				g.SetCell(term.Coordinates{X: c.atX + i, Y: 0}, term.Cell{Ch: ch})
			}
			start, end, ok := g.LineBounds(c.y)
			require.Equal(t, c.wantOK, ok)
			if c.wantOK {
				assert.Equal(t, term.Coordinates{X: c.wantFirst, Y: c.y}, start)
				assert.Equal(t, term.Coordinates{X: c.end, Y: c.y}, end)
			}
		})
	}

	t.Run("trailing gap with interior content", func(t *testing.T) {
		g := NewSelectionWriter(10, 1)
		g.SetCell(term.Coordinates{X: 2, Y: 0}, term.Cell{Ch: 'a'})
		g.SetCell(term.Coordinates{X: 5, Y: 0}, term.Cell{Ch: 'b'})
		start, end, ok := g.LineBounds(0)
		require.True(t, ok)
		assert.Equal(t, term.Coordinates{X: 2, Y: 0}, start)
		assert.Equal(t, term.Coordinates{X: 5, Y: 0}, end)
	})
}

func TestSelectionWriterDump(t *testing.T) {
	t.Run("fills null cells with space", func(t *testing.T) {
		g := NewSelectionWriter(3, 1)
		g.SetCell(term.Coordinates{X: 0, Y: 0}, term.Cell{Ch: 'a'})
		out := term.NewStringWriter(3, 1)
		g.Dump(out, nil)
		require.NoError(t, out.Flush())
		assert.Equal(t, "a  ", out.String())
	})

	t.Run("applies reverse over active selection only", func(t *testing.T) {
		g := NewSelectionWriter(5, 1)
		writeRow(g, 0, "abcde")
		sel := &SelRange{
			Start:  term.Coordinates{X: 1, Y: 0},
			End:    term.Coordinates{X: 3, Y: 0},
			Active: true,
		}
		out := term.NewStringWriter(5, 1)
		g.Dump(out, sel)
		require.NoError(t, out.Flush())
		cells := out.Cells()
		want := []bool{false, true, true, true, false}
		for x, w := range want {
			got := cells[x].Attrs&term.AttrReverse != 0
			assert.Equal(t, w, got, "cell %d reverse", x)
		}
	})

	t.Run("inactive selection applies no reverse", func(t *testing.T) {
		g := NewSelectionWriter(3, 1)
		writeRow(g, 0, "abc")
		out := term.NewStringWriter(3, 1)
		g.Dump(out, &SelRange{
			Start: term.Coordinates{X: 0, Y: 0}, End: term.Coordinates{X: 2, Y: 0},
		})
		require.NoError(t, out.Flush())
		for x, c := range out.Cells() {
			assert.Zero(t, c.Attrs&term.AttrReverse, "cell %d", x)
		}
	})
}

func TestSelRangeContains(t *testing.T) {
	type tc struct {
		name string
		sel  SelRange
		pos  term.Coordinates
		want bool
	}
	multi := SelRange{
		Start:  term.Coordinates{X: 3, Y: 1},
		End:    term.Coordinates{X: 2, Y: 3},
		Active: true,
	}
	single := SelRange{
		Start:  term.Coordinates{X: 2, Y: 0},
		End:    term.Coordinates{X: 5, Y: 0},
		Active: true,
	}
	cases := []tc{
		{"inactive contains nothing", SelRange{Start: single.Start, End: single.End}, term.Coordinates{X: 3, Y: 0}, false},
		{"single line inside", single, term.Coordinates{X: 3, Y: 0}, true},
		{"single line at start", single, term.Coordinates{X: 2, Y: 0}, true},
		{"single line at end", single, term.Coordinates{X: 5, Y: 0}, true},
		{"single line before", single, term.Coordinates{X: 1, Y: 0}, false},
		{"single line after", single, term.Coordinates{X: 6, Y: 0}, false},
		{"multi above range", multi, term.Coordinates{X: 5, Y: 0}, false},
		{"multi below range", multi, term.Coordinates{X: 0, Y: 4}, false},
		{"multi first row before startX", multi, term.Coordinates{X: 2, Y: 1}, false},
		{"multi first row at startX", multi, term.Coordinates{X: 3, Y: 1}, true},
		{"multi interior row any X", multi, term.Coordinates{X: 0, Y: 2}, true},
		{"multi last row within endX", multi, term.Coordinates{X: 2, Y: 3}, true},
		{"multi last row past endX", multi, term.Coordinates{X: 3, Y: 3}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.sel.Contains(c.pos))
		})
	}
}

func TestSelectionWriterContext(t *testing.T) {
	g := NewSelectionWriter(2, 2)
	assert.NotNil(t, g.Context())
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	g.SetContext(ctx)
	assert.Equal(t, "v", g.Context().Value(key{}))
}
