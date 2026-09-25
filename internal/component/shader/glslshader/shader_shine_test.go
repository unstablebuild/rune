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
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestShine(t *testing.T) {
	shadertest.TestShader(t, Shine(
		DefaultShineParams(),
		term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
	))

	t.Run("paints only text", func(t *testing.T) {
		assertPaintsOnlyText(t, Shine(
			DefaultShineParams(),
			term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
		))
	})
}

// TestShineSweep renders the entire sweep of the shine effect over a filled
// rectangle, one frame of animation per test case. Cells whose foreground was
// touched by the shine band render as '#' (via term.StringWriter.ForegroundCh);
// untouched cells render as the original 'x'.
func TestShineSweep(t *testing.T) {
	const (
		w     = 11
		h     = 5
		total = 8
	)

	mk := func(ch rune) term.Cell {
		// Untouched cells leave Fg as ColorDefault (zero) so the
		// StringWriter's ForegroundCh substitution only fires for cells
		// the band actually shaded.
		return term.Cell{Ch: ch, Width: 1}
	}

	makeCells := func() [][]term.Cell {
		cells := make([][]term.Cell, h)
		for y := range h {
			cells[y] = make([]term.Cell, w)
			for x := range w {
				cells[y][x] = mk('x')
			}
		}
		return cells
	}

	tsuite := []struct {
		name   string
		frame  int
		expect string
	}{
		{
			name:  "frame 0 of 8: band entering at bottom-left corner",
			frame: 0,
			expect: `
xxxxxxxxxxx
xxxxxxxxxxx
xxxxxxxxxxx
xxxxxxxxxxx
xxxxxxxxxxx`,
		},
		{
			name:  "frame 1 of 8",
			frame: 1,
			expect: `
xxxxxxxxxxx
xxxxxxxxxxx
xxxxxxxxxxx
##xxxxxxxxx
####xxxxxxx`,
		},
		{
			name:  "frame 2 of 8",
			frame: 2,
			expect: `
xxxxxxxxxxx
#xxxxxxxxxx
###xxxxxxxx
######xxxxx
########xxx`,
		},
		{
			name:  "frame 3 of 8",
			frame: 3,
			expect: `
###xxxxxxxx
#####xxxxxx
########xxx
##########x
x##########`,
		},
		{
			name:  "frame 4 of 8",
			frame: 4,
			expect: `
######xxxxx
#########xx
###########
xx#########
xxxxx######`,
		},
		{
			name:  "frame 5 of 8",
			frame: 5,
			expect: `
##########x
x##########
xxx########
xxxxxx#####
xxxxxxxx###`,
		},
		{
			name:  "frame 6 of 8",
			frame: 6,
			expect: `
xxx########
xxxxx######
xxxxxxxx###
xxxxxxxxxx#
xxxxxxxxxxx`,
		},
		{
			name:  "frame 7 of 8: band exiting at top-right corner",
			frame: 7,
			expect: `
xxxxxxx####
xxxxxxxxx##
xxxxxxxxxxx
xxxxxxxxxxx
xxxxxxxxxxx`,
		},
	}

	sh := Shine(
		ShineParams{
			Color:     term.NewRGBColor(255, 255, 255),
			BandWidth: 0.3,
			Cycles:    1,
		},
		// A non-default Fg fallback so the shine has something to blend
		// from on cells whose Fg is ColorDefault.
		term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
	)

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			cells := makeCells()
			sh.Shade(tcase.frame, total, cells)

			tw := term.NewStringWriter(w, h)
			tw.ForegroundCh = '#'
			for y, row := range cells {
				for x, cell := range row {
					tw.SetCell(term.Coordinates{X: x, Y: y}, cell)
				}
			}
			require.NoError(t, tw.Flush())
			assert.Equal(t, tcase.expect, "\n"+tw.String())
		})
	}
}

// TestShineDirections runs the shine sweep at the same frame index against
// each Direction, demonstrating the band's orientation. The grid below is
// the matrix the shader sees (11 cols x 5 rows). Untouched cells render as
// 'x'; touched cells as '#'.
func TestShineDirections(t *testing.T) {
	const (
		w     = 11
		h     = 5
		total = 8
		// Frame 2 of 8 puts the band near the start of the sweep so the
		// orientation reads clearly and opposite directions look mirrored
		// (frame 4 would land mid-screen, indistinguishable across opposite
		// pairs).
		frame = 2
	)

	mk := func(ch rune) term.Cell {
		return term.Cell{Ch: ch, Width: 1}
	}
	makeCells := func() [][]term.Cell {
		cells := make([][]term.Cell, h)
		for y := range h {
			cells[y] = make([]term.Cell, w)
			for x := range w {
				cells[y][x] = mk('x')
			}
		}
		return cells
	}

	tsuite := []struct {
		name      string
		direction Direction
		expect    string
	}{
		{
			name:      "bottom-left to top-right",
			direction: DirectionBottomLeftToTopRight,
			expect: `
xxxxxxxxxxx
#xxxxxxxxxx
###xxxxxxxx
######xxxxx
########xxx`,
		},
		{
			name:      "top-left to bottom-right",
			direction: DirectionTopLeftToBottomRight,
			expect: `
########xxx
######xxxxx
###xxxxxxxx
#xxxxxxxxxx
xxxxxxxxxxx`,
		},
		{
			name:      "left to right",
			direction: DirectionLeftToRight,
			expect: `
####xxxxxxx
####xxxxxxx
####xxxxxxx
####xxxxxxx
####xxxxxxx`,
		},
		{
			name:      "right to left",
			direction: DirectionRightToLeft,
			expect: `
xxxxxxx####
xxxxxxx####
xxxxxxx####
xxxxxxx####
xxxxxxx####`,
		},
		{
			name:      "top to bottom",
			direction: DirectionTopToBottom,
			expect: `
###########
###########
xxxxxxxxxxx
xxxxxxxxxxx
xxxxxxxxxxx`,
		},
		{
			name:      "bottom to top",
			direction: DirectionBottomToTop,
			expect: `
xxxxxxxxxxx
xxxxxxxxxxx
xxxxxxxxxxx
###########
###########`,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			sh := Shine(
				ShineParams{
					Direction: tcase.direction,
					Color:     term.NewRGBColor(255, 255, 255),
					BandWidth: 0.3,
					Cycles:    1,
				},
				term.Attributes{Fg: term.NewRGBColor(80, 80, 80)},
			)

			cells := makeCells()
			sh.Shade(frame, total, cells)

			tw := term.NewStringWriter(w, h)
			tw.ForegroundCh = '#'
			for y, row := range cells {
				for x, cell := range row {
					tw.SetCell(term.Coordinates{X: x, Y: y}, cell)
				}
			}
			require.NoError(t, tw.Flush())
			assert.Equal(t, tcase.expect, "\n"+tw.String())
		})
	}
}

// Intensity scales the blend towards Color at the band's centre: zero
// keeps the stock full strength, and a fraction stops partway.
func TestShineIntensity(t *testing.T) {
	black := term.NewRGBColor(0, 0, 0)
	shade := func(intensity float) int32 {
		params := DefaultShineParams()
		params.Direction = DirectionLeftToRight
		params.Intensity = intensity
		cells := [][]term.Cell{{{Ch: 'x', Width: 1, Fg: black}}}
		// A single column sits at t=0; frame/total puts the band there.
		total := 1000
		frame := int(float(total) * params.BandWidth / (1 + 2*params.BandWidth))
		Shine(params, term.Attributes{Fg: black}).Shade(frame, total, cells)
		r, _, _ := cells[0][0].Fg.RGB()
		return r
	}
	full := shade(0)
	require.Greater(t, full, int32(200), "the band must peak on the cell")
	assert.Equal(t, full, shade(1))
	half := shade(0.5)
	assert.Greater(t, half, int32(0))
	assert.Less(t, half, full)
	assert.InDelta(t, float64(full)/2, float64(half), 10)
}
