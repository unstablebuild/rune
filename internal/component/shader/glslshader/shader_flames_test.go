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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestFlames(t *testing.T) {
	sh := Flames(DefaultFlamesParams(), term.Attributes{})
	shadertest.TestShader(t, sh)
}

func TestFlamesDependsOnProgressOnly(t *testing.T) {
	build := func() shader.Shader { return Flames(DefaultFlamesParams(), term.Attributes{}) }
	shadertest.AssertProgressOnly(t, build, shadertest.MakeTextCanvas(12, 6))
	shadertest.AssertOrderIndependent(t, build, shadertest.MakeTextCanvas(12, 6), 20)
}

func TestFlamesFrontAdvancesEveryFrame(t *testing.T) {
	for _, total := range []int{90, 300, 900} {
		sh := Flames(DefaultFlamesParams(), term.Attributes{})
		assert.Zero(t, frontStall(sh, shadertest.MakeTextCanvas(40, 14), total),
			"frames in a row the front stands still over %d frames", total)
	}
}

func TestFlamesEndAsTheyStart(t *testing.T) {
	canvases := append(seamCanvases(),
		shadertest.MakeTextCanvas(80, 100), shadertest.MakeTextCanvas(200, 80),
		shadertest.MakeTextCanvas(20, 200))
	const total = 40
	for _, in := range canvases {
		sh := Flames(DefaultFlamesParams(), term.Attributes{})
		assert.Equal(t, in, shadeCopy(sh, 0, total, in), "%dx%d: first frame", len(in[0]), len(in))
		assert.Equal(t, in, shadeCopy(sh, total-1, total, in),
			"%dx%d: the flames must be over by the last frame", len(in[0]), len(in))
	}
}

// frontStall shades every frame of total with sh and returns the most
// frames in a row that catch no new cell, from the first that does
// until three quarters of the cells that ever catch fire have. The
// last few cells straggle in too far apart to show whether the front
// moves smoothly.
func frontStall(sh shader.Shader, in [][]term.Cell, total int) int {
	caught := make(map[[2]int]bool)
	var spread []int
	for frame := range total {
		out := shadeCopy(sh, frame, total, in)
		n := 0
		for y := range out {
			for x := range out[y] {
				if out[y][x] != in[y][x] && !caught[[2]int{x, y}] {
					caught[[2]int{x, y}] = true
					n++
				}
			}
		}
		spread = append(spread, n)
	}
	first := slices.IndexFunc(spread, func(n int) bool { return n > 0 })
	stall, longest, sum := 0, 0, 0
	for _, n := range spread[first:] {
		sum += n
		if 4*sum >= 3*len(caught) {
			break
		}
		if n > 0 {
			stall = 0
			continue
		}
		stall++
		if stall > longest {
			longest = stall
		}
	}
	return longest
}

func BenchmarkFlames(b *testing.B) {
	sh := Flames(DefaultFlamesParams(), term.Attributes{})
	shadertest.BenchmarkShader(b, sh, 120, 40, shadertest.MakeTextCanvas(120, 40))
}
