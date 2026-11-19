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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

const defaultBackgroundCursorAtRoot = `<pre style="background:#000000;color:#FFFFFF;"><span style="background: red;">%s</span>%s</pre>`
const defaultBackgroundNoCursor = `<pre style="background:#000000;color:#FFFFFF;">%s</pre>`

func expectInnerHTMLWithCursor(t *testing.T, writer *HTMLWriter, expectedHTML string) {
	require.NoError(t, writer.Flush())
	assert.Equal(t, expectedHTML, writer.HTML())
}

func expectInnerHTML(t *testing.T, writer *HTMLWriter, onCursor string, html string) {
	expectedHTML := fmt.Sprintf(defaultBackgroundCursorAtRoot, onCursor, html)
	expectInnerHTMLWithCursor(t, writer, expectedHTML)
}

func newWriterNoCursor() *HTMLWriter {
	writer := NewHTMLWriter(1, 1)
	writer.cursor = -1
	return writer
}

func TestHTMLWriter(t *testing.T) {
	t.Run("writes a cell", func(t *testing.T) {
		writer := NewHTMLWriter(1, 1)
		writer.SetCell(term.Coordinates{}, term.Cell{Ch: 'a'})
		expectInnerHTML(t, writer, "a", "")
	})

	t.Run("overwrites cells", func(t *testing.T) {
		writer := NewHTMLWriter(1, 1)
		writer.SetCell(term.Coordinates{}, term.Cell{Ch: 'a'})
		writer.SetCell(term.Coordinates{}, term.Cell{Ch: 'b'})
		expectInnerHTML(t, writer, "b", "")
	})

	t.Run("ignores out-of-bounds writes", func(t *testing.T) {
		writer := NewHTMLWriter(1, 1)
		writer.SetCell(term.Coordinates{Y: 1}, term.Cell{Ch: 'X'})
		expectInnerHTML(t, writer, " ", "")
	})

	t.Run("escapes &,>,< runes", func(t *testing.T) {
		needEscape := []rune{'&', '<', '>'}
		writer := NewHTMLWriter(len(needEscape), 1)
		for i, c := range needEscape {
			writer.SetCell(term.Coordinates{X: i}, term.Cell{Ch: c})
		}
		expectInnerHTML(t, writer, "&amp", "&lt&gt")
	})

	t.Run("writes cell attributes in CSS", func(t *testing.T) {
		// NOTE: fix if webasm build is ever relevant again
		t.SkipNow()

		tsuite := []struct {
			attr        term.Attributes
			expectedCSS string
		}{
			{term.Attributes{}, "X"}, /* no style */
			{term.Attributes{Fg: term.ColorWhite, Bg: term.ColorWhite}, "<span style=\"background:#FFFFFF;\">X</span>"}, /*white is default foreground */
			{term.Attributes{Fg: term.ColorBlack, Bg: term.ColorBlack}, "<span style=\"color:#000000;\">X</span>"},      /* black is default background */
			{term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlue}, "<span style=\"background:#0000FF;color:#0000FF;\">X</span>"},
			{term.Attributes{Fg: term.ColorAqua, Bg: term.ColorAqua}, "<span style=\"background:#00FFFF;color:#00FFFF;\">X</span>"},
			{term.Attributes{Fg: term.ColorGreen, Bg: term.ColorGreen}, "<span style=\"background:#00FF00;color:#00FF00;\">X</span>"},
			{term.Attributes{Fg: term.ColorFuchsia, Bg: term.ColorFuchsia}, "<span style=\"background:#FF00FF;color:#FF00FF;\">X</span>"},
			{term.Attributes{Fg: term.ColorRed, Bg: term.ColorRed}, "<span style=\"background:#FF0000;color:#FF0000;\">X</span>"},
			{term.Attributes{Fg: term.ColorYellow, Bg: term.ColorYellow}, "<span style=\"background:#FFFF00;color:#FFFF00;\">X</span>"},
			{term.Attributes{Attrs: term.AttrBold}, "<span style=\"font-weight:bold;\">X</span>"},
			{term.Attributes{Attrs: term.AttrUnderline}, "<span style=\"text-decoration:underline;\">X</span>"},
			{term.Attributes{Attrs: term.AttrReverse}, "<span style=\"background:#FFFFFF;color:#000000;\">X</span>"},
			{term.Attributes{Attrs: term.AttrBold | term.AttrReverse | term.AttrUnderline},
				"<span style=\"font-weight:bold;text-decoration:underline;background:#FFFFFF;color:#000000;\">X</span>"},
			{term.Attributes{Fg: term.ColorAqua, Bg: term.ColorAqua, Attrs: term.AttrBold},
				"<span style=\"font-weight:bold;background:#00FFFF;color:#00FFFF;\">X</span>"},
		}

		for i, tcase := range tsuite {
			t.Run(fmt.Sprintf("%d", i), func(t *testing.T) {
				writer := newWriterNoCursor()
				writer.SetCell(term.Coordinates{}, term.NewCell('X', 0, tcase.attr))
				expectedHTML := fmt.Sprintf(defaultBackgroundNoCursor, tcase.expectedCSS)
				expectInnerHTMLWithCursor(t, writer, expectedHTML)
			})
		}
	})
}

func TestHTMLWriterClear(t *testing.T) {
	writer := newWriterNoCursor()
	writer.Clear(term.Attributes{Bg: term.ColorAqua, Fg: term.ColorFuchsia})
	expectedHTML := `<pre style="background:#00FFFF;color:#FF00FF;"> </pre>`
	expectInnerHTMLWithCursor(t, writer, expectedHTML)
}

func TestHTMLWriterSetCellKeepsBackground(t *testing.T) {
	pos := term.Coordinates{}
	keep := term.NewCell('x', 1, term.Attributes{Fg: term.ColorFuchsia, Bg: term.ColorRed, Attrs: term.AttrBold | term.AttrKeepBackground})

	t.Run("set cell", func(t *testing.T) {
		writer := newWriterNoCursor()
		writer.SetCell(pos, term.NewCell('C', 1, term.Attributes{Fg: term.ColorRed, Bg: term.ColorAqua}))
		writer.SetCell(pos, keep)
		assert.Equal(t, term.NewCell('x', 1, term.Attributes{Fg: term.ColorFuchsia, Bg: term.ColorAqua, Attrs: term.AttrBold}), writer.cellbuf[0])
	})

	t.Run("union keeps its own background semantics", func(t *testing.T) {
		writer := newWriterNoCursor()
		writer.SetCell(pos, term.NewCell('C', 1, term.Attributes{Bg: term.ColorAqua}))
		writer.UnionAttributes(pos, term.Attributes{Fg: term.ColorFuchsia, Attrs: term.AttrKeepBackground})
		assert.Equal(t, term.NewCell('C', 1, term.Attributes{Fg: term.ColorFuchsia, Bg: term.ColorAqua}), writer.cellbuf[0])
	})
}

func TestHTMLWriterSetCursor(t *testing.T) {
	t.Run("sets cursor", func(t *testing.T) {
		writer := NewHTMLWriter(2, 2)
		writer.SetCursor(term.Coordinates{Y: 1, X: 1})
		expectedHTML := "<pre style=\"background:#000000;color:#FFFFFF;\">  \n <span style=\"background: red;\"> </span></pre>"
		expectInnerHTMLWithCursor(t, writer, expectedHTML)
	})

	t.Run("ignores out-of-bounds coordinates", func(t *testing.T) {
		writer := NewHTMLWriter(2, 2)
		writer.SetCursor(term.Coordinates{Y: 10, X: 1})
		expectedHTML := "<pre style=\"background:#000000;color:#FFFFFF;\">  \n  </pre>"
		expectInnerHTMLWithCursor(t, writer, expectedHTML)
	})

	t.Run("ignores negative coordinates", func(t *testing.T) {
		writer := NewHTMLWriter(2, 2)
		writer.SetCursor(term.Coordinates{X: -1})
		expectedHTML := "<pre style=\"background:#000000;color:#FFFFFF;\">  \n  </pre>"
		expectInnerHTMLWithCursor(t, writer, expectedHTML)
	})
}
