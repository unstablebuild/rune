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

type recordingShader struct {
	gotRows, gotCols int
	mark             rune
}

func (s *recordingShader) Shade(_, _ int, cells [][]term.Cell) {
	s.gotRows = len(cells)
	for _, row := range cells {
		if len(row) > s.gotCols {
			s.gotCols = len(row)
		}
		for x := range row {
			row[x].Ch = s.mark
		}
	}
}

func TestVirtualClipsAndOffsets(t *testing.T) {
	t.Parallel()
	cells := make([][]term.Cell, 4)
	for y := range cells {
		cells[y] = make([]term.Cell, 6)
		for x := range cells[y] {
			cells[y][x].Ch = '.'
		}
	}

	rec := &recordingShader{mark: '#'}
	Virtual(rec, term.Coordinates{X: 2, Y: 1}, 3, 2).Shade(0, 1, cells)

	assert.Equal(t, 2, rec.gotRows,
		"inner shader should see Height rows")
	assert.Equal(t, 3, rec.gotCols,
		"inner shader should see Width cols")

	for y := 1; y < 3; y++ {
		for x := 2; x < 5; x++ {
			assert.Equal(t, '#', cells[y][x].Ch,
				"cell (%d, %d) inside sub-rect should be mutated", x, y)
		}
	}
	for y := range cells {
		for x := range cells[y] {
			if y >= 1 && y < 3 && x >= 2 && x < 5 {
				continue
			}
			assert.Equal(t, '.', cells[y][x].Ch,
				"cell (%d, %d) outside sub-rect must be untouched", x, y)
		}
	}
}

func TestVirtualOutOfRangeIsNoOp(t *testing.T) {
	t.Parallel()
	cells := [][]term.Cell{
		{{Ch: 'a'}, {Ch: 'b'}},
		{{Ch: 'c'}, {Ch: 'd'}},
	}
	clone := [][]term.Cell{
		{{Ch: 'a'}, {Ch: 'b'}},
		{{Ch: 'c'}, {Ch: 'd'}},
	}

	rec := &recordingShader{mark: '#'}
	for _, sh := range []Shader{
		Virtual(rec, term.Coordinates{X: 0, Y: 0}, 0, 0),
		Virtual(rec, term.Coordinates{X: -1, Y: 0}, 2, 2),
		Virtual(rec, term.Coordinates{X: 0, Y: 99}, 2, 2),
	} {
		sh.Shade(0, 1, cells)
	}
	assert.Equal(t, clone, cells,
		"out-of-range Virtual must not mutate any cell")
}

func TestVirtualClampsWidthHeightToMatrix(t *testing.T) {
	t.Parallel()
	cells := [][]term.Cell{
		{{Ch: 'a'}, {Ch: 'b'}},
		{{Ch: 'c'}, {Ch: 'd'}},
	}
	rec := &recordingShader{mark: '#'}
	Virtual(rec, term.Coordinates{X: 1, Y: 1}, 10, 10).Shade(0, 1, cells)
	assert.Equal(t, 1, rec.gotRows)
	assert.Equal(t, 1, rec.gotCols)
	assert.Equal(t, '#', cells[1][1].Ch)
	assert.Equal(t, 'a', cells[0][0].Ch)
	assert.Equal(t, 'b', cells[0][1].Ch)
	assert.Equal(t, 'c', cells[1][0].Ch)
}
