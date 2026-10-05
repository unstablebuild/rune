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

func TestPulseFrame(t *testing.T) {
	shadertest.TestShader(t, PulseFrame(
		DefaultPulseFrameParams(guiFrameCharset()),
		term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
	))
}

func TestPulseFrameOnlyAffectsFrameChars(t *testing.T) {
	fc := guiFrameCharset()
	origFg := term.NewRGBColor(10, 20, 30)
	mk := func(ch rune) term.Cell {
		return term.NewCell(ch, 1, term.Attributes{Fg: origFg})
	}

	// 3x3 with frame chars on the border and 'a' in the middle.
	makeCells := func() [][]term.Cell {
		return [][]term.Cell{
			{mk(fc.TopLeft), mk(fc.HorizontalTop), mk(fc.TopRight)},
			{mk(fc.VerticalLeft), mk('a'), mk(fc.VerticalRight)},
			{mk(fc.BottomLeft), mk(fc.HorizontalBottom), mk(fc.BottomRight)},
		}
	}

	sh := PulseFrame(
		DefaultPulseFrameParams(fc),
		term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
	)

	// At the trough (frame 0) the foreground is unchanged; at the peak
	// (mid-animation) every frame cell shares the same blended color
	// while the inner 'a' is left untouched.
	const total = 10
	cells := makeCells()
	sh.Shade(0, total, cells)
	for _, row := range cells {
		for _, cell := range row {
			assert.Equal(t, origFg, cell.Fg,
				"trough: cell %q must be untouched", string(cell.Ch))
		}
	}

	cells = makeCells()
	sh.Shade(total/2, total, cells)
	var peakFrameFg term.Color
	first := true
	for y, row := range cells {
		for x, cell := range row {
			if y == 1 && x == 1 {
				assert.Equal(t, origFg, cell.Fg,
					"peak: inner non-frame cell must be untouched")
				continue
			}
			if first {
				peakFrameFg = cell.Fg
				first = false
				assert.NotEqual(t, origFg, peakFrameFg,
					"peak: frame cells should blend away from original")
				continue
			}
			assert.Equal(t, peakFrameFg, cell.Fg,
				"peak: every frame cell shares the same color")
		}
	}
}

func TestPulseFrameMinIntensityClampsTrough(t *testing.T) {
	fc := guiFrameCharset()
	origFg := term.NewRGBColor(10, 20, 30)
	cells := [][]term.Cell{{term.NewCell(fc.TopLeft, 1, term.Attributes{Fg: origFg})}}
	params := DefaultPulseFrameParams(fc)
	params.MinIntensity = 0.5
	sh := PulseFrame(params, term.Attributes{Fg: term.NewRGBColor(80, 80, 80)})

	sh.Shade(0, 10, cells)
	assert.NotEqual(t, origFg, cells[0][0].Fg,
		"trough with MinIntensity > 0 should still blend the color")
}
