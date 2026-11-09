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

func TestExpand_Reveal(t *testing.T) {
	testReveal(t, textMoves, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultExpandParams()
		params.From = from
		return shader.Expand(params, defaultAttr)
	})
}

func TestExpand_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Expand(shader.DefaultExpandParams(), term.Attributes{}))
}

func TestExpand_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Expand(shader.DefaultExpandParams(), defaultAttr)
	})
}

func TestExpand_StartsAtTheCenterAndEndsInPlace(t *testing.T) {
	sh := shader.Expand(shader.DefaultExpandParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 40
	in := makeBlankCells(21, 11)
	in[0][0] = term.NewCell('A', 1, term.Attributes{Fg: term.ColorRed})
	in[10][20] = term.NewCell('B', 1, term.Attributes{Fg: term.ColorBlue})

	first := cloneCells(in)
	sh.Shade(0, total, first)
	assert.Equal(t, ' ', first[0][0].Ch, "A left its place")
	assert.Equal(t, ' ', first[10][20].Ch, "B left its place")
	assert.Contains(t, []rune{'A', 'B'}, first[5][10].Ch, "the characters pile up at the center")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestExpand_BackgroundTravels(t *testing.T) {
	sh := shader.Expand(shader.DefaultExpandParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestExpand_Params(t *testing.T) {
	type P = shader.ExpandParams
	d := shader.DefaultExpandParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(d)},
		accepted("travel over the whole animation", with(d, func(p *P) { p.TravelDuration = 1 })),
	}
	tcases = append(tcases, invalidFloats(d, "TravelDuration", func(p *P, v float64) { p.TravelDuration = v }, -0.1, 1.01)...)
	tcases = append(tcases, invalidEasings(d, "Easing", func(p *P, v shaderutils.Easing) { p.Easing = v })...)
	testParams(t, shader.Expand, tcases)
}

func FuzzExpand(f *testing.F) {
	d := shader.DefaultExpandParams()
	fuzzSeed(
		f,
		uint32(d.StartColor), uint8(0), d.TravelDuration,
	)
	fuzzSeed(
		f,
		uint32(0), uint8(0), 0.0,
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		startColor uint32,
		easing uint8,
		travelDuration float64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Expand(shader.ExpandParams{
				StartColor:     term.Color(startColor),
				Easing:         fuzzEasing(easing),
				TravelDuration: travelDuration,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
