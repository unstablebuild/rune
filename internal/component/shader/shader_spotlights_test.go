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
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestSpotlights_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Spotlights(shader.DefaultSpotlightsParams(), term.Attributes{}))
}

func TestSpotlights_EdgeCases(t *testing.T) {
	testLoopEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Spotlights(shader.DefaultSpotlightsParams(), defaultAttr)
	})
}

func TestSpotlights_SearchFromAndBackToADarkPoint(t *testing.T) {
	sh := shader.Spotlights(shader.DefaultSpotlightsParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 300
	in := makeBlankCells(40, 20)
	for y := range in {
		for x := range in[y] {
			in[y][x] = term.NewCell('A', 1, term.Attributes{Fg: term.NewRGBColor(200, 200, 200)})
		}
	}
	shade := func(frame int) (out [][]term.Cell, lit int) {
		out = cloneCells(in)
		sh.Shade(frame, total, out)
		for y := range out {
			for x := range out[y] {
				if r, _, _ := out[y][x].Fg.RGB(); r >= 100 {
					lit++
				}
			}
		}
		return out, lit
	}

	mid, lit := shade(total / 2)
	assert.NotZero(t, lit, "text inside the beams is lit")
	assert.Less(t, lit, len(mid)*len(mid[0])/2, "the beams light a small part of the canvas")

	first, lit := shade(0)
	assert.Zero(t, lit, "the search starts from a dark point")
	last, lit := shade(total - 1)
	assert.Zero(t, lit, "the search ends in a dark point")
	for y := range first {
		for x := range first[y] {
			a, _, _ := first[y][x].Fg.RGB()
			b, _, _ := last[y][x].Fg.RGB()
			assert.InDelta(t, a, b, 2, "the last frame leads back into the first at %d,%d", x, y)
		}
	}
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestSpotlights_Params(t *testing.T) {
	type P = shader.SpotlightsParams
	d := shader.DefaultSpotlightsParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(with(d, func(p *P) { p.BeamFalloff = 0 }))},
		{name: "negative spotlight count", params: with(d, func(p *P) { p.SpotlightCount = -1 })},
		{name: "huge spotlight count is capped", params: with(d, func(p *P) { p.SpotlightCount = math.MaxInt }), sameAs: same(with(d, func(p *P) { p.SpotlightCount = 32 }))},
		{name: "huge beam width ratio is capped", params: with(d, func(p *P) { p.BeamWidthRatio = 1e300 }), sameAs: same(with(d, func(p *P) { p.BeamWidthRatio = 1000 }))},
		{name: "beam width ratio below 1 is capped", params: with(d, func(p *P) { p.BeamWidthRatio = 1e-300 }), sameAs: same(with(d, func(p *P) { p.BeamWidthRatio = 1 }))},
		accepted("sharp beams", with(d, func(p *P) { p.BeamFalloff, p.DarkBrightness = 0, 1 })),
	}
	tcases = append(tcases, invalidFloats(d, "BeamWidthRatio", func(p *P, v float64) { p.BeamWidthRatio = v }, -1)...)
	tcases = append(tcases, invalidFloats(d, "BeamFalloff", func(p *P, v float64) { p.BeamFalloff = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "SearchDuration", func(p *P, v float64) { p.SearchDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "DarkBrightness", func(p *P, v float64) { p.DarkBrightness = v }, -0.1, 1.1)...)
	testParams(t, shader.Spotlights, tcases)
}

func FuzzSpotlights(f *testing.F) {
	d := shader.DefaultSpotlightsParams()
	fuzzSeed(
		f,
		d.BeamWidthRatio, d.BeamFalloff, d.SpotlightCount, d.SearchDuration,
		d.DarkBrightness, d.Seed,
	)
	fuzzSeed(
		f,
		0.0, 0.0, 0, 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		beamWidthRatio, beamFalloff float64,
		spotlightCount int,
		searchDuration, darkBrightness float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Spotlights(shader.SpotlightsParams{
				BeamWidthRatio: beamWidthRatio,
				BeamFalloff:    beamFalloff,
				SpotlightCount: spotlightCount,
				SearchDuration: searchDuration,
				DarkBrightness: darkBrightness,
				Seed:           seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzLoopShade(t, sh, data, frame, total)
		}
	})
}
