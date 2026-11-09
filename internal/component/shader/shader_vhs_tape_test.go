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
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestVHSTape_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.VHSTape(shader.DefaultVHSTapeParams(), term.Attributes{}))
}

func TestVHSTape_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.VHSTape(shader.DefaultVHSTapeParams(), defaultAttr)
	})
}

func TestVHSTape_GlitchesThenSnowsThenRedraws(t *testing.T) {
	params := shader.DefaultVHSTapeParams()
	params.NoiseSymbols = []rune{'#'}
	sh := shader.VHSTape(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 400
	in := makeCharCells(20, 10)

	shifted := 0
	for frame := range int(params.GlitchDuration * total) {
		got := cloneCells(in)
		sh.Shade(frame, total, got)
		if got[0][0].Ch == ' ' || got[0][19].Ch == ' ' {
			shifted++
		}
	}
	assert.NotZero(t, shifted, "rows jump aside while glitching")

	snow := cloneCells(in)
	sh.Shade(int(params.GlitchDuration*total)+1, total, snow)
	assert.Equal(t, 200, countChar(snow, '#'), "the picture is lost to snow after glitching")

	redrawing := cloneCells(in)
	sh.Shade(int((params.GlitchDuration+(1-params.GlitchDuration)*0.3)*total)+total/40, total, redrawing)
	assert.Equal(t, 'A', redrawing[0][0].Ch, "the top row is redrawn first")
	assert.Equal(t, '#', redrawing[9][0].Ch, "the bottom row is still snow")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestVHSTape_BackgroundTravels(t *testing.T) {
	sh := shader.VHSTape(shader.DefaultVHSTapeParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestVHSTape_Params(t *testing.T) {
	type P = shader.VHSTapeParams
	d := shader.DefaultVHSTapeParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(with(d, func(p *P) { p.GlitchLineChance = 0; p.NoiseChance = 0 }))},
		accepted("certain glitches", with(d, func(p *P) { p.GlitchLineChance, p.NoiseChance = 1, 1 })),
		accepted("no glitches", with(d, func(p *P) { p.GlitchLineChance, p.NoiseChance = 0, 0 })),
	}
	tcases = append(tcases, invalidFloats(d, "GlitchLineChance", func(p *P, v float64) { p.GlitchLineChance = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "NoiseChance", func(p *P, v float64) { p.NoiseChance = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "GlitchDuration", func(p *P, v float64) { p.GlitchDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidSymbols(d, "NoiseSymbols", func(p *P, v []rune) { p.NoiseSymbols = v })...)
	testParams(t, shader.VHSTape, tcases)
}

func FuzzVHSTape(f *testing.F) {
	d := shader.DefaultVHSTapeParams()
	fuzzSeed(
		f,
		fuzzColorSeed(d.GlitchLineColors), fuzzColorSeed(d.GlitchWaveColors),
		fuzzColorSeed(d.NoiseColors), string(d.NoiseSymbols), d.GlitchLineChance,
		d.NoiseChance, d.GlitchDuration, uint32(d.RedrawColor), d.Seed,
	)
	fuzzSeed(
		f,
		[]byte(nil), []byte(nil), []byte(nil), "", 0.0, 0.0, 0.0, uint32(0),
		int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		glitchLineColors, glitchWaveColors, noiseColors []byte,
		noiseSymbols string,
		glitchLineChance, noiseChance, glitchDuration float64,
		redrawColor uint32,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.VHSTape(shader.VHSTapeParams{
				GlitchLineColors: fuzzColors(glitchLineColors),
				GlitchWaveColors: fuzzColors(glitchWaveColors),
				NoiseColors:      fuzzColors(noiseColors),
				NoiseSymbols:     fuzzRunes(noiseSymbols),
				GlitchLineChance: glitchLineChance,
				NoiseChance:      noiseChance,
				GlitchDuration:   glitchDuration,
				RedrawColor:      term.Color(redrawColor),
				Seed:             seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
