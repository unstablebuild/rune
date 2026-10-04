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
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestRadarFrame(t *testing.T) {
	shadertest.TestShader(t, RadarFrame(
		DefaultRadarFrameParams(guiFrameCharset()),
		term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
	))
}

func TestRadarFrameLeavesNonFrameCharsUntouched(t *testing.T) {
	fc := guiFrameCharset()
	origFg := term.NewRGBColor(10, 20, 30)
	mk := func(ch rune) term.Cell {
		return term.NewCell(ch, 1, term.Attributes{Fg: origFg})
	}

	cells := [][]term.Cell{
		{mk('x'), mk('y'), mk('z')},
		{mk('a'), mk('b'), mk('c')},
		{mk('1'), mk('2'), mk('3')},
	}

	sh := RadarFrame(
		DefaultRadarFrameParams(fc),
		term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
	)
	for f := range 12 {
		sh.Shade(f, 12, cells)
	}

	for _, row := range cells {
		for _, cell := range row {
			assert.Equal(t, origFg, cell.Fg)
		}
	}
}

func TestRadarFramePeakIsFullColor(t *testing.T) {
	fc := guiFrameCharset()
	target := term.NewRGBColor(255, 0, 0)
	mk := func(ch rune) term.Cell {
		return term.NewCell(ch, 1, term.Attributes{Fg: term.NewRGBColor(10, 20, 30)})
	}

	// Rectangular 11x5 frame. At frame 0 of 1 cycle the wedge points
	// along the positive-x axis (angle=0), so the right-middle vertical
	// edge cell sits exactly at the wedge peak.
	const (
		w = 11
		h = 5
	)
	cells := make([][]term.Cell, h)
	for y := range h {
		cells[y] = make([]term.Cell, w)
		for x := range w {
			switch {
			case y == 0 && x == 0:
				cells[y][x] = mk(fc.TopLeft)
			case y == 0 && x == w-1:
				cells[y][x] = mk(fc.TopRight)
			case y == h-1 && x == 0:
				cells[y][x] = mk(fc.BottomLeft)
			case y == h-1 && x == w-1:
				cells[y][x] = mk(fc.BottomRight)
			case y == 0:
				cells[y][x] = mk(fc.HorizontalTop)
			case y == h-1:
				cells[y][x] = mk(fc.HorizontalBottom)
			case x == 0:
				cells[y][x] = mk(fc.VerticalLeft)
			case x == w-1:
				cells[y][x] = mk(fc.VerticalRight)
			default:
				cells[y][x] = mk(' ')
			}
		}
	}

	params := DefaultRadarFrameParams(fc)
	params.Color = target
	params.AngularWidth = 0.5
	sh := RadarFrame(
		params,
		term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
	)
	sh.Shade(0, 1, cells)

	assert.Equal(t, target, cells[h/2][w-1].Fg,
		"right-middle frame cell sits at the wedge peak and must be fully overridden")
}

func TestRadarFrameFadesAtEdges(t *testing.T) {
	fc := guiFrameCharset()
	origFg := term.NewRGBColor(0, 0, 0)
	target := term.NewRGBColor(255, 255, 255)
	mk := func(ch rune) term.Cell {
		return term.NewCell(ch, 1, term.Attributes{Fg: origFg})
	}

	// Tall narrow frame so the top and bottom edges are at clearly
	// distinct angles relative to the right-middle peak.
	const (
		w = 11
		h = 5
	)
	makeCells := func() [][]term.Cell {
		cells := make([][]term.Cell, h)
		for y := range h {
			cells[y] = make([]term.Cell, w)
			for x := range w {
				switch {
				case y == 0 && x == 0:
					cells[y][x] = mk(fc.TopLeft)
				case y == 0 && x == w-1:
					cells[y][x] = mk(fc.TopRight)
				case y == h-1 && x == 0:
					cells[y][x] = mk(fc.BottomLeft)
				case y == h-1 && x == w-1:
					cells[y][x] = mk(fc.BottomRight)
				case y == 0:
					cells[y][x] = mk(fc.HorizontalTop)
				case y == h-1:
					cells[y][x] = mk(fc.HorizontalBottom)
				case x == 0:
					cells[y][x] = mk(fc.VerticalLeft)
				case x == w-1:
					cells[y][x] = mk(fc.VerticalRight)
				default:
					cells[y][x] = mk(' ')
				}
			}
		}
		return cells
	}

	params := DefaultRadarFrameParams(fc)
	params.Color = target
	params.AngularWidth = 0.6
	sh := RadarFrame(
		params,
		term.Attributes{Fg: origFg},
	)

	cells := makeCells()
	sh.Shade(0, 1, cells)

	peakCell := cells[h/2][w-1]
	edgeCell := cells[0][w-1]

	peakR, _, _ := peakCell.Fg.RGB()
	edgeR, _, _ := edgeCell.Fg.RGB()

	assert.Greater(t, int(peakR), int(edgeR),
		"the peak of the wedge must be brighter than its angular edge")
	assert.Greater(t, int(edgeR), int(0),
		"the edge of the wedge must still be partially blended (not the original)")
}

func TestRadarFrameRotates(t *testing.T) {
	fc := guiFrameCharset()
	origFg := term.NewRGBColor(0, 0, 0)
	target := term.NewRGBColor(255, 255, 255)
	mk := func(ch rune) term.Cell {
		return term.NewCell(ch, 1, term.Attributes{Fg: origFg})
	}
	makeCells := func() [][]term.Cell {
		return [][]term.Cell{
			{mk(fc.TopLeft), mk(fc.HorizontalTop), mk(fc.TopRight)},
			{mk(fc.VerticalLeft), mk(' '), mk(fc.VerticalRight)},
			{mk(fc.BottomLeft), mk(fc.HorizontalBottom), mk(fc.BottomRight)},
		}
	}

	params := DefaultRadarFrameParams(fc)
	params.Color = target
	params.AngularWidth = 0.25

	sh := RadarFrame(params, term.Attributes{Fg: origFg})

	// At frame 0 (angle 0, pointing right) the right-middle edge is at
	// the peak; at frame 2 of 4 (angle 0.5, pointing left) it is on the
	// far side and should be unaffected.
	cellsA := makeCells()
	sh.Shade(0, 4, cellsA)
	cellsB := makeCells()
	sh.Shade(2, 4, cellsB)

	assert.NotEqual(t, origFg, cellsA[1][2].Fg,
		"right-middle edge should be blended at frame 0 (wedge faces right)")
	assert.Equal(t, origFg, cellsB[1][2].Fg,
		"right-middle edge should be untouched at frame 2 (wedge faces left)")
}
