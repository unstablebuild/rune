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

package kageshader_test

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/kageshader"
	"unstable.build/rune/internal/term/gui/shaderdevice/shaderdevicetest"
)

var filters = []struct {
	name string
	sh   gshader.Shader
}{
	{name: "desaturate", sh: kageshader.Desaturate(kageshader.DefaultDesaturateParams())},
	{name: "vignette", sh: kageshader.Vignette(kageshader.DefaultVignetteParams())},
	{name: "scanlines", sh: kageshader.Scanlines(kageshader.ScanlinesParams{Strength: 0.25, Spacing: 3, Speed: 2})},
	{name: "shine", sh: kageshader.Shine(kageshader.DefaultShineParams())},
}

// picture is a shader that draws a picture of every colour, for filters to
// shade.
type picture struct{ src *image.RGBA }

func newPicture() picture {
	p := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := range 16 {
		for x := range 16 {
			p.SetRGBA(x, y, color.RGBA{R: uint8(x * 16), G: uint8(y * 16), B: 0x80, A: 0xff})
		}
	}
	return picture{src: p}
}

func (p picture) Shade(int, int, gshader.Node) gshader.Node {
	return gshader.Picture(p.src, 1, 1, term.ImageFitFill)
}

func TestFiltersShadeWithTheTimeAlone(t *testing.T) {
	const total = 8
	for _, f := range filters {
		t.Run(f.name, func(t *testing.T) {
			sh := gshader.Mix(newPicture(), f.sh)
			shaderdevicetest.AssertDeterministic(t, sh, total, size)
			shaderdevicetest.AssertOrderIndependent(t, sh, total, size)
			shaderdevicetest.AssertSeamless(t, sh, total, size)
		})
	}
}

func TestFiltersRunOverCells(t *testing.T) {
	cells := [][]term.Cell{{{Ch: 'a', Bg: term.ColorRed}, {Ch: 'b'}}}
	for _, f := range filters {
		t.Run(f.name, func(t *testing.T) {
			_, err := shaderdevicetest.New(t).RenderOver(f.sh, 0, 8, cells)
			assert.NoError(t, err)
		})
	}
}

func TestFiltersOnTheGPU(t *testing.T) {
	shaderdevicetest.RequireGPU(t)
	red := color.RGBA{R: 0xff, A: 0xff}
	white := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	grid := func(c term.Color) [][]term.Cell {
		cells := make([][]term.Cell, 4)
		for y := range cells {
			cells[y] = make([]term.Cell, 8)
			for x := range cells[y] {
				cells[y][x].Bg = c
			}
		}
		return cells
	}
	tests := []struct {
		name   string
		sh     gshader.Shader
		cells  [][]term.Cell
		frame  int
		checks func(t *testing.T, px *image.RGBA)
	}{
		{
			name: "desaturate turns red grey", sh: kageshader.Desaturate(kageshader.DefaultDesaturateParams()), cells: grid(term.ColorRed),
			checks: func(t *testing.T, px *image.RGBA) {
				assertNear(t, color.RGBA{R: 54, G: 54, B: 54, A: 0xff}, px.RGBAAt(5, 5))
			},
		},
		{
			name: "half desaturated", sh: kageshader.Desaturate(kageshader.DesaturateParams{Amount: 0.5}), cells: grid(term.ColorRed),
			checks: func(t *testing.T, px *image.RGBA) {
				assertNear(t, color.RGBA{R: 155, G: 27, B: 27, A: 0xff}, px.RGBAAt(5, 5))
			},
		},
		{
			name: "vignette darkens the corners alone", sh: kageshader.Vignette(kageshader.DefaultVignetteParams()), cells: grid(term.ColorWhite),
			checks: func(t *testing.T, px *image.RGBA) {
				assertNear(t, white, px.RGBAAt(16, 16), "the centre")
				assert.Less(t, px.RGBAAt(0, 0).R, uint8(0xd0), "a corner")
			},
		},
		{
			name: "scanlines darken every few rows", sh: kageshader.Scanlines(kageshader.DefaultScanlinesParams()), cells: grid(term.ColorWhite),
			checks: func(t *testing.T, px *image.RGBA) {
				assertNear(t, color.RGBA{R: 207, G: 207, B: 207, A: 0xff}, px.RGBAAt(3, 0), "on a line")
				assertNear(t, white, px.RGBAAt(3, 1), "between lines")
			},
		},
		{
			name: "shine is off at the start", sh: kageshader.Shine(kageshader.DefaultShineParams()), cells: grid(term.ColorRed),
			checks: func(t *testing.T, px *image.RGBA) {
				assertNear(t, red, px.RGBAAt(16, 16))
			},
		},
		{
			name: "shine crosses the middle half way", sh: kageshader.Shine(kageshader.DefaultShineParams()), cells: grid(term.ColorRed), frame: 4,
			checks: func(t *testing.T, px *image.RGBA) {
				assert.Greater(t, px.RGBAAt(16, 16).G, uint8(0xc0), "the middle shines")
			},
		},
		{
			name: "shine leaves the background alone", sh: kageshader.Shine(kageshader.DefaultShineParams()), cells: grid(term.ColorDefault), frame: 4,
			checks: func(t *testing.T, px *image.RGBA) {
				assertNear(t, color.RGBA{A: 0xff}, px.RGBAAt(16, 16))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := shaderdevicetest.New(t)
			h.Theme.Resolve = func(c term.Color) color.RGBA {
				switch c {
				case term.ColorRed:
					return red
				case term.ColorWhite:
					return white
				case term.ColorDefault:
					return h.Theme.Background
				default:
					r, g, b := c.RGB()
					return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xff}
				}
			}
			px, err := h.RenderOver(tt.sh, tt.frame, 8, tt.cells)
			require.NoError(t, err)
			tt.checks(t, px)
		})
	}
}

func assertNear(t *testing.T, want, got color.RGBA, msgAndArgs ...any) {
	t.Helper()
	for _, d := range []int{
		int(want.R) - int(got.R), int(want.G) - int(got.G),
		int(want.B) - int(got.B), int(want.A) - int(got.A),
	} {
		if d > shaderdevicetest.Tolerance || d < -shaderdevicetest.Tolerance {
			assert.Equal(t, want, got, msgAndArgs...)
			return
		}
	}
}
