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
	"math"
	"math/bits"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader"
)

const brailleTotal = 100

// brailleScreen draws a text row on top of a cols x rows wallpaper of
// blank braille, with a block of fully raised art cells at artX, artY and
// a non-braille cell over the wallpaper at 2, rows-2.
func brailleScreen(cols, rows, artX, artY int, artFg term.Color) [][]term.Cell {
	text := term.Attributes{Fg: term.ColorWhite, Bg: term.ColorNavy}
	out := make([][]term.Cell, rows)
	for y := range out {
		out[y] = make([]term.Cell, cols)
		for x := range out[y] {
			switch {
			case y == 0:
				out[y][x] = term.NewCell('t', 1, text)
			case x == 2 && y == rows-2:
				out[y][x] = term.NewCell('P', 1, text)
			case x >= artX && x < artX+4 && y >= artY && y < artY+3:
				out[y][x] = term.NewCell('⣿', 1, term.Attributes{Fg: artFg})
			default:
				out[y][x] = term.NewCell(braille.Blank, 1, term.Attributes{})
			}
		}
	}
	return out
}

func shadeBraille(sh shader.Shader, frame int, live [][]term.Cell) [][]term.Cell {
	cells := cloneCells(live)
	sh.Shade(frame, brailleTotal, cells)
	return cells
}

func settleFrame(p shader.BrailleAssembleParams) int {
	return int(math.Ceil((p.Hold + p.Launch + p.Travel) * brailleTotal))
}

func raisedDots(cells [][]term.Cell) int {
	n := 0
	for _, row := range cells {
		for _, c := range row {
			if braille.IsPattern(c.Ch) {
				n += bits.OnesCount32(uint32(c.Ch - braille.Blank))
			}
		}
	}
	return n
}

func TestBrailleAssemble_LeavesScreensWithoutBlankBrailleUntouched(t *testing.T) {
	live := makeCharCells(20, 8)
	live[3][3].Ch = '⣿'
	sh := shader.BrailleAssemble(shader.DefaultBrailleAssembleParams(), term.Attributes{})
	for frame := range brailleTotal {
		require.Equal(t, live, shadeBraille(sh, frame, live), "frame %d", frame)
	}
}

func TestBrailleAssemble_EndsOnTheScreenAsDrawn(t *testing.T) {
	params := shader.DefaultBrailleAssembleParams()
	live := brailleScreen(24, 12, 10, 5, term.NewRGBColor(255, 0, 0))
	sh := shader.BrailleAssemble(params, term.Attributes{Bg: term.NewRGBColor(0, 0, 0)})

	for frame := settleFrame(params); frame < brailleTotal; frame++ {
		require.Equal(t, live, shadeBraille(sh, frame, live), "frame %d", frame)
	}
}

func TestBrailleAssemble_ScattersTheArtBeforeItLands(t *testing.T) {
	live := brailleScreen(24, 12, 10, 5, term.NewRGBColor(255, 0, 0))
	sh := shader.BrailleAssemble(shader.DefaultBrailleAssembleParams(), term.Attributes{})
	art := raisedDots(live)

	for _, frame := range []int{0, 10, 30, 50} {
		cells := shadeBraille(sh, frame, live)
		dots := raisedDots(cells)
		assert.Positive(t, dots, "frame %d shows no dots", frame)
		assert.Less(t, dots, art, "frame %d shows the art at rest", frame)
	}
}

func TestBrailleAssemble_OnlyChangesBrailleCells(t *testing.T) {
	live := brailleScreen(24, 12, 10, 5, term.NewRGBColor(255, 0, 0))
	sh := shader.BrailleAssemble(shader.DefaultBrailleAssembleParams(), term.Attributes{})
	for frame := range brailleTotal {
		cells := shadeBraille(sh, frame, live)
		for y, row := range cells {
			for x, c := range row {
				want := live[y][x]
				if !braille.IsPattern(want.Ch) {
					require.Equal(t, want, c, "frame %d cell %d,%d", frame, x, y)
					continue
				}
				require.True(t, braille.IsPattern(c.Ch), "frame %d cell %d,%d", frame, x, y)
				require.Equal(t, want.Bg, c.Bg, "frame %d cell %d,%d", frame, x, y)
			}
		}
	}
}

func TestBrailleAssemble_DimsIdleDots(t *testing.T) {
	tests := []struct {
		name        string
		artFg       term.Color
		defaultAttr term.Attributes
		want        term.Color
	}{
		{
			name:        "toward the default background",
			artFg:       term.NewRGBColor(255, 0, 0),
			defaultAttr: term.Attributes{Bg: term.NewRGBColor(0, 0, 0)},
			want:        term.NewRGBColor(89, 0, 0),
		},
		{
			name:  "art in the default foreground",
			artFg: term.ColorDefault,
			defaultAttr: term.Attributes{
				Fg: term.NewRGBColor(200, 100, 0),
				Bg: term.NewRGBColor(0, 0, 0),
			},
			want: term.NewRGBColor(70, 35, 0),
		},
		{
			name:        "not without an RGB background",
			artFg:       term.NewRGBColor(255, 0, 0),
			defaultAttr: term.Attributes{},
			want:        term.NewRGBColor(255, 0, 0),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			live := brailleScreen(24, 12, 10, 5, tt.artFg)
			sh := shader.BrailleAssemble(shader.DefaultBrailleAssembleParams(), tt.defaultAttr)
			// Past the line's fade in and before the first departure.
			cells := shadeBraille(sh, 8, live)
			shown := 0
			for _, row := range cells {
				for _, c := range row {
					if braille.IsPattern(c.Ch) && c.Ch != braille.Blank {
						shown++
						assert.Equal(t, tt.want, c.Fg)
					}
				}
			}
			assert.Positive(t, shown)
		})
	}
}

func TestBrailleAssemble_StartsTheDotsOnTheLines(t *testing.T) {
	// The wallpaper of brailleScreen is 24x11 cells, or 48x44 dots, below
	// the text row.
	art := image.Rect(20, 16, 28, 28)
	tests := []struct {
		name  string
		lines []shader.BrailleLine
		// on reports whether the dot at x, y, from the wallpaper's top
		// left, is where a dot may start.
		on        func(x, y int) bool
		wantShown bool
	}{
		{
			name:      "along a line",
			lines:     []shader.BrailleLine{{X0: 0.1, Y0: 0.5, X1: 0.9, Y1: 0.5}},
			on:        func(x, y int) bool { return y == 22 && x >= 4 && x <= 44 },
			wantShown: true,
		},
		{
			name:      "out of a point",
			lines:     []shader.BrailleLine{{X0: 0.25, Y0: 0.25, X1: 0.25, Y1: 0.25}},
			on:        func(x, y int) bool { return x == 12 && y == 11 },
			wantShown: true,
		},
		{
			name:  "off the wallpaper",
			lines: []shader.BrailleLine{{X0: -0.5, Y0: -0.5, X1: -0.1, Y1: -0.2}},
			on:    func(int, int) bool { return false },
		},
		{
			name:      "where they land without lines",
			on:        func(x, y int) bool { return image.Pt(x, y).In(art) },
			wantShown: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := shader.DefaultBrailleAssembleParams()
			params.Lines = tt.lines
			live := brailleScreen(24, 12, 10, 5, term.NewRGBColor(255, 0, 0))
			sh := shader.BrailleAssemble(params, term.Attributes{})

			shown := 0
			for frame := 0; float64(frame) < params.Hold*brailleTotal; frame++ {
				for y, row := range shadeBraille(sh, frame, live)[1:] {
					for x, c := range row {
						if !braille.IsPattern(c.Ch) {
							continue
						}
						for dy := range braille.CellDotsY {
							for dx := range braille.CellDotsX {
								if c.Ch&braille.Bit(dx, dy) == 0 {
									continue
								}
								shown++
								px, py := x*braille.CellDotsX+dx, y*braille.CellDotsY+dy
								require.True(t, tt.on(px, py), "frame %d: dot %d,%d", frame, px, py)
							}
						}
					}
				}
			}
			assert.Equal(t, tt.wantShown, shown > 0, "shown %d", shown)
			assert.Equal(t, live, shadeBraille(sh, brailleTotal-1, live),
				"the dots land on the art")
		})
	}
}

func TestBrailleAssemble_ReplaysTheSameSeed(t *testing.T) {
	live := brailleScreen(24, 12, 10, 5, term.NewRGBColor(255, 0, 0))
	params := shader.DefaultBrailleAssembleParams()
	a := shader.BrailleAssemble(params, term.Attributes{})
	b := shader.BrailleAssemble(params, term.Attributes{})
	params.Seed++
	other := shader.BrailleAssemble(params, term.Attributes{})

	differs := false
	for frame := range brailleTotal {
		want := shadeBraille(a, frame, live)
		require.Equal(t, want, shadeBraille(b, frame, live), "frame %d", frame)
		differs = differs || !assert.ObjectsAreEqual(want, shadeBraille(other, frame, live))
	}
	assert.True(t, differs, "another seed replays the same animation")
}

func TestBrailleAssemble_FollowsTheArtAcrossResizes(t *testing.T) {
	params := shader.DefaultBrailleAssembleParams()
	small := brailleScreen(24, 12, 10, 5, term.NewRGBColor(255, 0, 0))
	large := brailleScreen(40, 20, 3, 14, term.NewRGBColor(0, 0, 255))
	sh := shader.BrailleAssemble(params, term.Attributes{})

	for frame := range 30 {
		shadeBraille(sh, frame, small)
	}
	assert.NotEqual(t, large, shadeBraille(sh, 30, large))
	assert.Equal(t, large, shadeBraille(sh, settleFrame(params), large))
}

func TestBrailleAssemble_ToleratesRaggedScreens(t *testing.T) {
	live := brailleScreen(24, 12, 10, 5, term.NewRGBColor(255, 0, 0))
	live[3] = nil
	live[6] = live[6][:12]
	live[7] = live[7][:0]
	sh := shader.BrailleAssemble(shader.DefaultBrailleAssembleParams(), term.Attributes{})
	assert.NotPanics(t, func() {
		for frame := range brailleTotal {
			shadeBraille(sh, frame, live)
		}
	})
}
