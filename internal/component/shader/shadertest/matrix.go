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

package shadertest

import "github.com/unstablebuild/rune-go-sdk/term"

// MakeCellMatrix creates an two-dimensional array of zero-value cells
// representing a grid of w rows and h cols.
func MakeCellMatrix(w int, h int) [][]term.Cell {
	cells := make([][]term.Cell, h)
	for y := range h {
		cells[y] = make([]term.Cell, w)
	}
	return cells
}

// MakeTextCanvas creates a grid of w cols and h rows mixing coloured
// text with blanks and character-less gaps, the three kinds of cell a
// shader has to tell apart.
func MakeTextCanvas(w int, h int) [][]term.Cell {
	attr := term.Attributes{Fg: term.NewRGBColor(200, 210, 220), Bg: term.NewRGBColor(10, 20, 30)}
	cells := make([][]term.Cell, h)
	for y := range h {
		cells[y] = make([]term.Cell, w)
		for x := range w {
			switch (x + 2*y) % 5 {
			case 3:
				cells[y][x] = term.NewCell(' ', 1, attr)
			case 4:
				cells[y][x] = term.NewCell(0, 0, attr)
			default:
				cells[y][x] = term.NewCell('a'+rune((x+y)%26), 1, attr)
			}
		}
	}
	return cells
}
