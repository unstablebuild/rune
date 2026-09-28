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

package glslshader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestInferno(t *testing.T) {
	sh := Inferno(DefaultInfernoParams(), 30)
	shadertest.TestShader(t, sh)
}

func TestInfernoPaintForeground(t *testing.T) {
	params := DefaultInfernoParams()
	params.PaintForeground = true

	t.Run("suite", func(t *testing.T) {
		shadertest.TestShader(t, Inferno(params, 30))
	})

	t.Run("paints the channel it was asked for", func(t *testing.T) {
		for _, tc := range []struct {
			name            string
			paintForeground bool
		}{
			{name: "background by default"},
			{name: "foreground when enabled", paintForeground: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				p := DefaultInfernoParams()
				p.PaintForeground = tc.paintForeground
				assertPaintsChannel(t, Inferno(p, 30), tc.paintForeground)
			})
		}
	})

	t.Run("leaves background glyphs bare", func(t *testing.T) {
		assertPaintsOnlyText(t, Inferno(params, 30))
	})
}

// assertPaintsChannel shades a matrix of glyph-carrying cells and checks
// that the effect landed on the character colour when asked to, and on
// the cell background otherwise, leaving the other channel untouched.
func assertPaintsChannel(t *testing.T, sh shader.Shader, paintForeground bool) {
	t.Helper()

	in := paintTestCells()
	sh.Shade(3, 20, in)

	var fgChanged, bgChanged bool
	for y, row := range in {
		for x, cell := range row {
			want := paintTestCells()[y][x]
			if cell.Fg != want.Fg {
				fgChanged = true
			}
			if cell.Bg != want.Bg {
				bgChanged = true
			}
			if paintForeground {
				assert.Equal(t, want.Bg, cell.Bg, "background must stay bare")
			} else {
				assert.Equal(t, want.Fg, cell.Fg, "foreground must stay untouched")
			}
		}
	}
	assert.Equal(t, paintForeground, fgChanged, "foreground painted")
	assert.Equal(t, !paintForeground, bgChanged, "background painted")
}

// assertPaintsOnlyText sweeps a whole animation over a row mixing text,
// blanks and glyphs that render as background, such as the status bar's
// fade blocks, and checks the foreground-only contract on every frame:
// cells not carrying text are never touched, and text cells keep their
// character and background while their colour changes at some point.
func assertPaintsOnlyText(t *testing.T, sh shader.Shader) {
	t.Helper()

	const total = 20
	want := backgroundGlyphCells()
	var textPainted bool
	for frame := range total {
		in := backgroundGlyphCells()
		sh.Shade(frame, total, in)
		for y, row := range in {
			for x, cell := range row {
				w := want[y][x]
				isText := w.Ch != 0 && w.Ch != ' ' &&
					!graphemecluster.IsBackground(w.Ch)
				if !isText {
					assert.Equal(t, []any{w.Ch, w.Fg, w.Bg},
						[]any{cell.Ch, cell.Fg, cell.Bg},
						"frame %d cell %d,%d must stay bare", frame, x, y)
					continue
				}
				assert.Equal(t, []any{w.Ch, w.Bg}, []any{cell.Ch, cell.Bg},
					"frame %d cell %d,%d text and background", frame, x, y)
				textPainted = textPainted || cell.Fg != w.Fg
			}
		}
	}
	assert.True(t, textPainted, "cells carrying text must still be painted")
}

func backgroundGlyphCells() [][]term.Cell {
	const rows = 4
	// The fade blocks the shipped status bar layout draws and the blanks
	// between its elements, plus real text so the effect still has
	// something to paint.
	glyphs := []rune{'x', '░', '▒', '▓', '█', ' ', 0, 'y', 'z'}
	fg := term.NewRGBColor(10, 20, 30)
	bg := term.NewRGBColor(40, 50, 60)
	cells := make([][]term.Cell, rows)
	for y := range cells {
		cells[y] = make([]term.Cell, len(glyphs))
		for x, ch := range glyphs {
			cells[y][x] = term.Cell{Ch: ch, Width: 1, Fg: fg, Bg: bg}
		}
	}
	return cells
}

func paintTestCells() [][]term.Cell {
	const (
		rows = 4
		cols = 6
	)
	fg := term.NewRGBColor(10, 20, 30)
	bg := term.NewRGBColor(40, 50, 60)
	cells := make([][]term.Cell, rows)
	for y := range cells {
		cells[y] = make([]term.Cell, cols)
		for x := range cells[y] {
			cells[y][x] = term.Cell{Ch: 'x', Width: 1, Fg: fg, Bg: bg}
		}
	}
	return cells
}
