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

func TestPour_Reveal(t *testing.T) {
	testReveal(t, textMoves, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultPourParams()
		params.From = from
		return shader.Pour(params, defaultAttr)
	})
}

func TestPour_Suite(t *testing.T) {
	for _, dir := range []shader.PourDirection{shader.PourDown, shader.PourUp, shader.PourLeft, shader.PourRight} {
		params := shader.DefaultPourParams()
		params.Direction = dir
		shadertest.TestShader(t, shader.Pour(params, term.Attributes{}))
	}
}

func TestPour_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Pour(shader.DefaultPourParams(), defaultAttr)
	})
}

func TestPour_FillsTheFarRowsFirst(t *testing.T) {
	sh := shader.Pour(shader.DefaultPourParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 100
	in := makeCharCells(10, 10)

	mid := cloneCells(in)
	sh.Shade(total/2, total, mid)
	assert.Equal(t, 'A', mid[9][0].Ch, "the bottom row is filled by half time")
	assert.Equal(t, ' ', mid[0][0].Ch, "the top row is still empty at half time")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestPour_BackgroundTravels(t *testing.T) {
	sh := shader.Pour(shader.DefaultPourParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestPour_Params(t *testing.T) {
	type P = shader.PourParams
	d := shader.DefaultPourParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(with(d, func(p *P) { p.SpeedJitter = 0 }))},
		{name: "unknown direction", params: with(d, func(p *P) { p.Direction = shader.PourRight + 1 })},
		accepted("pouring up", with(d, func(p *P) { p.Direction = shader.PourUp })),
		accepted("pouring left", with(d, func(p *P) { p.Direction = shader.PourLeft })),
		accepted("pouring right", with(d, func(p *P) { p.Direction = shader.PourRight })),
		accepted("no jitter", with(d, func(p *P) { p.SpeedJitter = 0 })),
	}
	tcases = append(tcases, invalidFloats(d, "FallDuration", func(p *P, v float64) { p.FallDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "SpeedJitter", func(p *P, v float64) { p.SpeedJitter = v }, -0.1, 1)...)
	tcases = append(tcases, invalidEasings(d, "Easing", func(p *P, v shaderutils.Easing) { p.Easing = v })...)
	testParams(t, shader.Pour, tcases)
}

func FuzzPour(f *testing.F) {
	d := shader.DefaultPourParams()
	fuzzSeed(
		f,
		uint8(d.Direction), uint32(d.StartColor), uint8(0), d.FallDuration,
		d.SpeedJitter, d.Seed,
	)
	fuzzSeed(
		f,
		uint8(0), uint32(0), uint8(0), 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		direction uint8,
		startColor uint32,
		easing uint8,
		fallDuration, speedJitter float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Pour(shader.PourParams{
				Direction:    shader.PourDirection(direction),
				StartColor:   term.Color(startColor),
				Easing:       fuzzEasing(easing),
				FallDuration: fallDuration,
				SpeedJitter:  speedJitter,
				Seed:         seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
