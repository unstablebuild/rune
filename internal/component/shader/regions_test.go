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

package shader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func dotCells(width, height int) [][]term.Cell {
	cells := make([][]term.Cell, height)
	for y := range cells {
		cells[y] = make([]term.Cell, width)
		for x := range cells[y] {
			cells[y][x].Ch = '.'
		}
	}
	return cells
}

func rowString(row []term.Cell) string {
	out := make([]rune, len(row))
	for x, c := range row {
		out[x] = c.Ch
	}
	return string(out)
}

// Every region is shaded and cells outside all of them are untouched.
func TestRegionsShadesEachRect(t *testing.T) {
	t.Parallel()
	cells := dotCells(10, 2)
	rects := []Rect{
		{Offset: term.Coordinates{X: 1, Y: 1}, Width: 2, Height: 1},
		{Offset: term.Coordinates{X: 6, Y: 1}, Width: 3, Height: 1},
	}
	sh := Regions(&recordingShader{mark: '#'}, func() []Rect { return rects })
	sh.Shade(0, 1, cells)

	assert.Equal(t, "..........", rowString(cells[0]))
	assert.Equal(t, ".##...###.", rowString(cells[1]))
}

// The rectangles are resolved on every frame so the shaded cells follow
// layout changes, and an empty set shades nothing.
func TestRegionsReevaluatesEachFrame(t *testing.T) {
	t.Parallel()
	var calls int
	rects := []Rect{{Offset: term.Coordinates{X: 0}, Width: 2, Height: 1}}
	sh := Regions(&recordingShader{mark: '#'}, func() []Rect {
		calls++
		return rects
	})

	cells := dotCells(6, 1)
	sh.Shade(0, 1, cells)
	assert.Equal(t, "##....", rowString(cells[0]))

	rects = []Rect{{Offset: term.Coordinates{X: 4}, Width: 2, Height: 1}}
	cells = dotCells(6, 1)
	sh.Shade(1, 1, cells)
	assert.Equal(t, "....##", rowString(cells[0]))

	rects = nil
	cells = dotCells(6, 1)
	sh.Shade(2, 1, cells)
	assert.Equal(t, "......", rowString(cells[0]))
	assert.Equal(t, 3, calls)
}
