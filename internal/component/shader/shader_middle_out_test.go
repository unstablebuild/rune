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

	"unstable.build/rune/internal/component/shader/shaderutils"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestMiddleOut_Reveal(t *testing.T) {
	testReveal(t, textMoves, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultMiddleOutParams()
		params.From = from
		return shader.MiddleOut(params, defaultAttr)
	})
}

func TestMiddleOut_Suite(t *testing.T) {
	for _, horizontal := range []bool{false, true} {
		params := shader.DefaultMiddleOutParams()
		params.Horizontal = horizontal
		shadertest.TestShader(t, shader.MiddleOut(params, term.Attributes{}))
	}
}

func TestMiddleOut_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.MiddleOut(shader.DefaultMiddleOutParams(), defaultAttr)
	})
}

func TestMiddleOut_SpreadsAlongTheMiddleRowFirst(t *testing.T) {
	sh := shader.MiddleOut(shader.DefaultMiddleOutParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 40
	in := makeCharCells(21, 11)

	mid := cloneCells(in)
	sh.Shade(total/2, total, mid)
	for y := range mid {
		for x := range mid[y] {
			if y == 5 {
				assert.Equal(t, 'A', mid[y][x].Ch, "col %d of the middle row is filled", x)
			} else {
				assert.Equal(t, ' ', mid[y][x].Ch, "cell %d,%d outside the middle row is empty", x, y)
			}
		}
	}
	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestMiddleOut_BackgroundTravels(t *testing.T) {
	sh := shader.MiddleOut(shader.DefaultMiddleOutParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestMiddleOut_Params(t *testing.T) {
	type P = shader.MiddleOutParams
	d := shader.DefaultMiddleOutParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(d)},
		accepted("horizontal", with(d, func(p *P) { p.Horizontal = true })),
	}
	tcases = append(tcases, invalidFloats(d, "CenterDuration", func(p *P, v float64) { p.CenterDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidEasings(d, "CenterEasing", func(p *P, v shaderutils.Easing) { p.CenterEasing = v })...)
	tcases = append(tcases, invalidEasings(d, "FullEasing", func(p *P, v shaderutils.Easing) { p.FullEasing = v })...)
	testParams(t, shader.MiddleOut, tcases)
}

func FuzzMiddleOut(f *testing.F) {
	d := shader.DefaultMiddleOutParams()
	fuzzSeed(
		f,
		uint32(d.StartColor), d.Horizontal, uint8(0), uint8(0), d.CenterDuration,
	)
	fuzzSeed(
		f,
		uint32(0), false, uint8(0), uint8(0), 0.0,
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		startColor uint32,
		horizontal bool,
		centerEasing, fullEasing uint8,
		centerDuration float64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.MiddleOut(shader.MiddleOutParams{
				StartColor:     term.Color(startColor),
				Horizontal:     horizontal,
				CenterEasing:   fuzzEasing(centerEasing),
				FullEasing:     fuzzEasing(fullEasing),
				CenterDuration: centerDuration,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
