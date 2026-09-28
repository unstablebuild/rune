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

package shader_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestBurn_Suite(t *testing.T) {
	sh := shader.Burn(shader.DefaultBurnParams(), term.Attributes{})
	shadertest.TestShader(t, sh)
}

// With PaintForeground the wave recolours text in place: no glyph is
// swapped for a fire block, no particle rises, and blanks and glyphs
// that render as background are never touched.
func TestBurn_PaintForegroundOnlyRecolorsText(t *testing.T) {
	params := shader.DefaultBurnParams()
	params.PaintForeground = true

	t.Run("suite", func(t *testing.T) {
		shadertest.TestShader(t, shader.Burn(params, term.Attributes{}))
	})

	fg := term.NewRGBColor(10, 20, 30)
	bg := term.NewRGBColor(40, 50, 60)
	glyphs := []rune{'R', 'E', ' ', '█', '▓', '▒', '░', 'x', 0, 'y'}
	in := [][]term.Cell{make([]term.Cell, len(glyphs))}
	for x, ch := range glyphs {
		width := uint8(1)
		if ch == 0 {
			width = 0
		}
		in[0][x] = term.Cell{Ch: ch, Width: width, Fg: fg, Bg: bg}
	}

	sh := shader.Burn(params, term.Attributes{})
	const total = 30
	var recolored bool
	for frame := range total + 1 {
		cells := cloneCells(in)
		sh.Shade(frame, total, cells)
		for x, cell := range cells[0] {
			want := in[0][x]
			isText := want.Ch != 0 && want.Ch != ' ' &&
				!graphemecluster.IsBackground(want.Ch)
			if !isText {
				assert.Equal(t, want, cell, "frame %d col %d", frame, x)
				continue
			}
			assert.Equal(t, want.Ch, cell.Ch, "frame %d col %d", frame, x)
			assert.Equal(t, want.Bg, cell.Bg, "frame %d col %d", frame, x)
			recolored = recolored || cell.Fg != want.Fg
		}
	}
	assert.True(t, recolored, "the wave must recolour the text")

	// The bar redraws every frame, so a snapshot taken on the first draw
	// would freeze the spinner and the elapsed time wherever the wave has
	// not reached yet.
	t.Run("shows live content", func(t *testing.T) {
		sh := shader.Burn(params, term.Attributes{})
		sh.Shade(0, total, cloneCells(in))
		for frame := range total + 1 {
			live := cloneCells(in)
			live[0][0].Ch = 'Z'
			sh.Shade(frame, total, live)
			assert.Equal(t, 'Z', live[0][0].Ch, "frame %d", frame)
		}
	})
}

func TestBurn_FramesAfterTotalRestoreInput(t *testing.T) {
	// After the shader's frame budget has elapsed, calls should leave the
	// input untouched. The shader component itself stops invoking Shade at
	// this point and draws the wrapped component directly.
	sh := shader.Burn(shader.DefaultBurnParams(), term.Attributes{})

	const total = 30
	in := makeCharCells(7, 5)
	want := cloneCells(in)

	sh.Shade(total+1, total, in)
	assert.Equal(t, want, in)
}

func TestBurn_TouchesCells(t *testing.T) {
	sh := shader.Burn(shader.DefaultBurnParams(), term.Attributes{})

	const total = 30
	in := makeCharCells(11, 7)
	orig := cloneCells(in)

	sh.Shade(total/2, total, in)

	changed := 0
	for y := range in {
		for x := range in[y] {
			if in[y][x].Ch != orig[y][x].Ch {
				changed++
			}
		}
	}
	assert.NotZero(t, changed, "expected at least one cell to be burning")
}

func TestBurn_TextSeedsIgnitionOnBlankCanvas(t *testing.T) {
	// Even though Rune burns blank cells too, existing text should seed the
	// Prim walk so a small text region on a large blank canvas starts
	// burning promptly.
	params := shader.DefaultBurnParams()
	params.SmokeChance = 0
	sh := shader.Burn(params, term.Attributes{})

	const total = 30
	in := makeBlankCells(40, 12)
	in[8][31].Ch = 'A'
	orig := cloneCells(in)

	touched := false
	for frame := 1; frame <= total/5; frame++ {
		cells := cloneCells(in)
		sh.Shade(frame, total, cells)
		if cells[8][31].Ch != orig[8][31].Ch {
			touched = true
			break
		}
	}
	assert.True(t, touched, "single-cell text boundary should ignite near the start")
}

func TestBurn_PaintsBlankCanvas(t *testing.T) {
	// The burn must still be visible when the wrapped component has blank
	// cells. Otherwise screens or panes without text look empty while the
	// fire crosses them.
	params := shader.DefaultBurnParams()
	params.SmokeChance = 0
	sh := shader.Burn(params, term.Attributes{})

	const total = 18
	in := makeBlankCells(12, 6)
	orig := cloneCells(in)
	touched := make([][]bool, len(in))
	for y := range touched {
		touched[y] = make([]bool, len(in[y]))
	}

	for frame := 1; frame <= total; frame++ {
		cells := cloneCells(in)
		sh.Shade(frame, total, cells)
		for y := range cells {
			for x := range cells[y] {
				if cells[y][x].Ch != orig[y][x].Ch {
					touched[y][x] = true
				}
			}
		}
	}

	for y := range touched {
		for x := range touched[y] {
			assert.Truef(t, touched[y][x], "blank cell (%d,%d) was never painted with fire", x, y)
		}
	}
}

func TestBurn_CoversTextBoundaryByTotal(t *testing.T) {
	// Rune runs the open burn shader for a fixed short duration (600ms =
	// 18 frames at 30fps). The TTE traversal order still needs to be
	// normalized to that frame budget: by frame==total, every burnable
	// cell in the text boundary should have been reached at least once.
	params := shader.DefaultBurnParams()
	params.SmokeChance = 0
	sh := shader.Burn(params, term.Attributes{})

	const total = 18
	in := makeCharCells(30, 10)
	orig := cloneCells(in)
	touched := make([][]bool, len(in))
	for y := range touched {
		touched[y] = make([]bool, len(in[y]))
	}

	for frame := 1; frame <= total; frame++ {
		cells := cloneCells(in)
		sh.Shade(frame, total, cells)
		for y := range cells {
			for x := range cells[y] {
				if cells[y][x].Ch != orig[y][x].Ch {
					touched[y][x] = true
				}
			}
		}
	}

	for y := range touched {
		for x := range touched[y] {
			assert.Truef(t, touched[y][x], "cell (%d,%d) was never burned", x, y)
		}
	}
}

func TestBurn_PostBurnCharactersRise(t *testing.T) {
	// After the fire crosses a character, some emitted particles should
	// carry the original character upward over several frames. This keeps
	// the post-burn motion reading as floating text rather than sparks.
	params := shader.DefaultBurnParams()
	params.SmokeChance = 1
	params.SmokeRise = 0.5
	params.SmokeMaxRise = 5
	sh := shader.Burn(params, term.Attributes{})

	const total = 60
	in := makeBlankCells(12, 10)
	in[8][5].Ch = 'A'

	// For a single burnable cell the normalized traversal starts at
	// frame 1. BurnDuration=0.15 => 9 burn frames at total=60, so the
	// post-burn particle starts at frame 10 and reaches its top position
	// near frame 39.
	early := cloneCells(in)
	sh.Shade(10, total, early)
	assert.Equal(t, 'A', early[7][5].Ch, "original character should rise from the burned cell")

	late := cloneCells(in)
	sh.Shade(39, total, late)
	foundHigher := false
	for y := 0; y <= 4; y++ {
		for x := range late[y] {
			if late[y][x].Ch == 'A' {
				foundHigher = true
			}
		}
	}
	assert.True(t, foundHigher, "original character should continue rising to a higher cell")
}

func TestBurn_BlankCellsEmitSmokeSymbols(t *testing.T) {
	// When a burned cell has no original character to lift, the post-burn
	// particle should still be emitted using a glyph drawn from
	// SmokeSymbols. Cells that DO have an original character keep using
	// it (verified by TestBurn_PostBurnCharactersRise).
	smokeSymbols := []rune{'.', ',', '\'', '`', '#', '*'}
	smokeSet := map[rune]bool{}
	for _, r := range smokeSymbols {
		smokeSet[r] = true
	}

	params := shader.DefaultBurnParams()
	params.SmokeSymbols = smokeSymbols
	params.SmokeChance = 1
	params.SmokeRise = 0.5
	params.SmokeMaxRise = 5
	params.FixedOrigin = true
	params.OriginX = 0.5
	params.OriginY = 1
	sh := shader.Burn(params, term.Attributes{})

	const total = 60
	// Entirely blank canvas: every source cell falls into the smoke-glyph
	// branch because none of them has an original character.
	in := makeBlankCells(12, 10)

	found := false
	for frame := 10; frame <= 40 && !found; frame++ {
		cells := cloneCells(in)
		sh.Shade(frame, total, cells)
		for y := range cells {
			for x := range cells[y] {
				if smokeSet[cells[y][x].Ch] {
					found = true
				}
			}
		}
	}
	assert.True(t, found,
		"blank-cell post-burn particles must use a SmokeSymbols glyph")
}

func TestBurn_IgnitionStableWhenContentChanges(t *testing.T) {
	// Keypresses change the wrapped component's rendered text. That must
	// not cause Burn to rebuild a new random Prim order/root and jump to a
	// different ignition center.
	params := shader.DefaultBurnParams()
	params.SmokeChance = 0
	sh := shader.Burn(params, term.Attributes{})

	const total = 60
	base := makeBlankCells(16, 8)
	base[4][8].Ch = 'A'

	first := cloneCells(base)
	sh.Shade(1, total, first)
	firstBurn := burnGlyphCoords(first, params.BurnSymbols)
	if assert.NotEmpty(t, firstBurn, "initial ignition batch should burn on frame 1") {
		changed := cloneCells(base)
		changed[1][2].Ch = 'B'
		changed[6][13].Ch = 'C'

		second := cloneCells(changed)
		sh.Shade(1, total, second)
		secondBurn := burnGlyphCoords(second, params.BurnSymbols)
		assert.Equal(t, firstBurn, secondBurn, "ignition root must remain stable after content changes")
	}
}

func TestBurn_FixedOriginStartsAtConfiguredCenter(t *testing.T) {
	params := shader.DefaultBurnParams()
	params.FixedOrigin = true
	params.OriginX = 0.5
	params.OriginY = 0.5
	params.IgniteBatchMin = 1
	params.IgniteBatchMax = 1
	params.SmokeChance = 0
	sh := shader.Burn(params, term.Attributes{})

	const total = 60
	in := makeBlankCells(11, 7)
	first := cloneCells(in)
	sh.Shade(1, total, first)

	assert.Equal(t, []burnTestCoord{{x: 5, y: 3}}, changedBurnCoords(in, first))
}

func TestBurn_RevealsLiveCellsAfterBurnPasses(t *testing.T) {
	params := shader.DefaultBurnParams()
	params.FixedOrigin = true
	params.OriginX = 0.5
	params.OriginY = 0
	params.IgniteBatchMin = 1
	params.IgniteBatchMax = 1
	params.SmokeChance = 0
	sh := shader.Burn(params, term.Attributes{})

	const total = 20
	initial := makeCharCells(5, 1)
	for x := range initial[0] {
		initial[0][x].Ch = 'O'
	}
	sh.Shade(0, total, initial)

	live := makeCharCells(5, 1)
	for x := range live[0] {
		live[0][x].Ch = 'N'
	}
	sh.Shade(1, total, live)

	assert.Contains(t, params.BurnSymbols, live[0][2].Ch, "active burn cells should draw fire")
	assert.Equal(t, 'O', live[0][0].Ch, "cells outside the active burn should keep the initial snapshot")

	live = makeCharCells(5, 1)
	for x := range live[0] {
		live[0][x].Ch = 'N'
	}
	sh.Shade(4, total, live)

	assert.Equal(t, 'N', live[0][2].Ch, "cells the burn has finished crossing should reveal the live content")
	assert.Equal(t, 'O', live[0][0].Ch, "unreached cells should keep the initial snapshot")
}

func changedBurnCoords(before, after [][]term.Cell) []burnTestCoord {
	out := []burnTestCoord{}
	for y := range after {
		for x := range after[y] {
			if y >= len(before) || x >= len(before[y]) {
				continue
			}
			if after[y][x].Ch != before[y][x].Ch {
				out = append(out, burnTestCoord{x: x, y: y})
			}
		}
	}
	return out
}

type burnTestCoord struct {
	x, y int
}

func burnGlyphCoords(cells [][]term.Cell, symbols []rune) []burnTestCoord {
	symbolSet := map[rune]bool{}
	for _, symbol := range symbols {
		symbolSet[symbol] = true
	}
	out := []burnTestCoord{}
	for y := range cells {
		for x := range cells[y] {
			if symbolSet[cells[y][x].Ch] {
				out = append(out, burnTestCoord{x: x, y: y})
			}
		}
	}
	return out
}

// TestBurn_ClearsRenderOffsetAttrsOnBurnedCells guards that the Burn
// shader strips term.AttrVerticalRenderOffset/
// AttrNegativeVerticalRenderOffset from cells whose glyph it actively
// overwrites. The renderer applies these hints to whatever character
// is in the cell, so leaving them in place when burn replaces a
// chrome-row glyph with a fire symbol renders the new glyph shifted
// by half a cell. Cells the burn hasn't crossed yet must keep their
// original attrs.
func TestBurn_ClearsRenderOffsetAttrsOnBurnedCells(t *testing.T) {
	params := shader.DefaultBurnParams()
	params.SmokeChance = 0
	sh := shader.Burn(params, term.Attributes{})

	const (
		cols  = 8
		rows  = 4
		total = 20
	)
	in := makeCharCells(cols, rows)
	mask := term.AttrVerticalRenderOffset | term.AttrNegativeVerticalRenderOffset
	for y := range in {
		for x := range in[y] {
			in[y][x].Attrs |= mask
		}
	}

	// Drive the burn to a mid-frame where some cells should be
	// actively rendering the fire glyph.
	sh.Shade(total/2, total, in)

	burning := burnGlyphCoords(in, params.BurnSymbols)
	require.NotEmpty(t, burning,
		"expected at least one cell to be rendering a burn glyph at mid-frame")
	for _, c := range burning {
		assert.Zerof(t, in[c.y][c.x].Attrs&mask,
			"burning cell (%d,%d) must have render-offset attrs cleared",
			c.x, c.y)
	}
}
