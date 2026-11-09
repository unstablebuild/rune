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
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader"
)

var solidifyRed = term.NewRGBColor(255, 0, 0)

func newSolidify(arts ...image.Rectangle) shader.Shader {
	return shader.BrailleSolidify(shader.DefaultBrailleSolidifyParams(),
		term.Attributes{Fg: term.ColorWhite, Bg: term.NewRGBColor(0, 0, 0)},
		blockWallpaperDots(solidifyRed, nil, arts...))
}

// settled reports whether every cell of cells within r is drawn as on
// live, and none is when none is true.
func settled(cells, live [][]term.Cell, r image.Rectangle, none bool) bool {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if (cells[y][x] == live[y][x]) == none {
				return false
			}
		}
	}
	return true
}

func TestBrailleSolidify_StartsOnTheBrailleArtAndEndsOnTheWallpaper(t *testing.T) {
	arts := []image.Rectangle{image.Rect(10, 2, 14, 6), image.Rect(4, 7, 9, 10)}
	live := blockWallpaper(24, 12, solidifyRed, arts...)
	swapped := shadeBraille(shader.BrailleSwap(blockWallpaperDots(solidifyRed, nil, arts...)), 0, live)
	sh := newSolidify(arts...)

	assert.Equal(t, swapped, shadeBraille(sh, 0, live))
	for frame := 1; frame < brailleTotal-1; frame++ {
		require.NotEqual(t, live, shadeBraille(sh, frame, live), "frame %d", frame)
	}
	assert.Equal(t, live, shadeBraille(sh, brailleTotal-1, live))
}

func TestBrailleSolidify_SpreadsDownTheArtFromItsTop(t *testing.T) {
	art := image.Rect(10, 1, 13, 11)
	live := blockWallpaper(24, 12, solidifyRed, art)
	sh := newSolidify(art)
	top := image.Rect(art.Min.X, art.Min.Y, art.Max.X, art.Min.Y+1)
	bottom := image.Rect(art.Min.X, art.Max.Y-1, art.Max.X, art.Max.Y)

	topFirst := false
	for frame := range brailleTotal {
		cells := shadeBraille(sh, frame, live)
		require.False(t, settled(cells, live, bottom, false) && !settled(cells, live, top, false),
			"frame %d: the bottom settled before the top", frame)
		topFirst = topFirst || settled(cells, live, top, false) && settled(cells, live, bottom, true)
	}
	assert.True(t, topFirst, "the top never settled while the bottom was still braille")
}

func TestBrailleSolidify_JumpsGapsOnlyAfterCrossingTheArt(t *testing.T) {
	// Crossing the gap is far shorter than following the upper art to
	// its end.
	upper := image.Rect(2, 1, 14, 2)
	lower := image.Rect(2, 3, 5, 5)
	live := blockWallpaper(24, 12, solidifyRed, upper, lower)
	sh := newSolidify(upper, lower)

	inOrder := false
	for frame := range brailleTotal {
		cells := shadeBraille(sh, frame, live)
		inOrder = inOrder || settled(cells, live, upper, false) && settled(cells, live, lower, true)
	}
	assert.True(t, inOrder, "the upper art never settled before the lower one started")
}

func TestBrailleSolidify_GlowsAsTheFrontPasses(t *testing.T) {
	art := image.Rect(10, 2, 14, 9)
	live := blockWallpaper(24, 12, solidifyRed, art)
	sh := newSolidify(art)

	glowed, flashed := false, false
	for frame := range brailleTotal {
		for _, row := range shadeBraille(sh, frame, live) {
			for _, c := range row {
				if _, g, _ := c.Fg.RGB(); g > 200 && c.Ch != 't' && c.Ch != 'P' {
					glowed = true
				}
				flashed = flashed || c.Ch == '█'
			}
		}
	}
	assert.True(t, glowed, "no cell glowed")
	assert.True(t, flashed, "no cell flashed a full block")
}

func TestBrailleSolidify_OnlyChangesTheWallpapersCharactersAndColors(t *testing.T) {
	art := image.Rect(10, 2, 14, 9)
	live := blockWallpaper(24, 12, solidifyRed, art)
	sh := newSolidify(art)
	for frame := range brailleTotal {
		cells := shadeBraille(sh, frame, live)
		for y, row := range cells {
			for x, c := range row {
				want := live[y][x]
				if want.Ch != braille.Blank && want.Ch != braille.Block {
					require.Equal(t, want, c, "frame %d cell %d,%d", frame, x, y)
					continue
				}
				require.Equal(t, want.Bg, c.Bg, "frame %d cell %d,%d", frame, x, y)
			}
		}
	}
}

func TestBrailleSolidify_LeavesScreensWithoutBlankBrailleUntouched(t *testing.T) {
	live := makeCharCells(20, 8)
	live[3][3].Ch = braille.Block
	sh := newSolidify(image.Rect(2, 2, 5, 5))
	for frame := range brailleTotal {
		require.Equal(t, live, shadeBraille(sh, frame, live), "frame %d", frame)
	}
}

func TestBrailleSolidify_ReplaysTheSameSeed(t *testing.T) {
	art := image.Rect(4, 2, 18, 10)
	live := blockWallpaper(24, 12, solidifyRed, art)
	params := shader.DefaultBrailleSolidifyParams()
	dots := blockWallpaperDots(solidifyRed, nil, art)
	a := shader.BrailleSolidify(params, term.Attributes{}, dots)
	b := shader.BrailleSolidify(params, term.Attributes{}, dots)
	params.Seed++
	other := shader.BrailleSolidify(params, term.Attributes{}, dots)

	differs := false
	for frame := range brailleTotal {
		want := shadeBraille(a, frame, live)
		require.Equal(t, want, shadeBraille(b, frame, live), "frame %d", frame)
		differs = differs || !assert.ObjectsAreEqual(want, shadeBraille(other, frame, live))
	}
	assert.True(t, differs, "another seed replays the same animation")
}
