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

func TestThunderstorm_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Thunderstorm(shader.DefaultThunderstormParams(), term.Attributes{}))
}

func TestThunderstorm_EdgeCases(t *testing.T) {
	testLoopEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Thunderstorm(shader.DefaultThunderstormParams(), defaultAttr)
	})
}

func TestThunderstorm_RainsAndStrikes(t *testing.T) {
	params := shader.DefaultThunderstormParams()
	params.RaindropSymbols = []rune{'r'}
	params.SparkSymbols = []rune{'s'}
	sh := shader.Thunderstorm(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 300
	in := makeBlankCells(40, 20)
	in[10][20] = term.NewCell('A', 1, term.Attributes{Fg: term.NewRGBColor(200, 200, 200)})

	rain, bolts, sparks, darkened := 0, 0, 0, 0
	for frame := range total {
		got := cloneCells(in)
		sh.Shade(frame, total, got)
		rain += countChar(got, 'r')
		bolts += countChar(got, '|') + countChar(got, '/') + countChar(got, '\\')
		sparks += countChar(got, 's')
		if r, _, _ := got[10][20].Fg.RGB(); r < 150 && got[10][20].Ch == 'A' {
			darkened++
		}
	}
	assert.NotZero(t, rain, "raindrops fall during the storm")
	assert.NotZero(t, bolts, "lightning strikes during the storm")
	assert.NotZero(t, sparks, "strikes scatter sparks")
	assert.NotZero(t, darkened, "the text darkens during the storm")

	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestThunderstorm_StormsOnALoop(t *testing.T) {
	// Without rain or bolts, what is left of the storm is the dark text.
	calm := shader.DefaultThunderstormParams()
	calm.StrikeCount, calm.RainDensity = 0, 0
	sh := shader.Thunderstorm(calm, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	in := makeCharCells(40, 20)
	for _, total := range []int{10, 300} {
		first, last := cloneCells(in), cloneCells(in)
		sh.Shade(0, total, first)
		sh.Shade(total-1, total, last)
		assert.NotEqual(t, in, last, "the text is still dark on the last of %d frames", total)
		assert.Equal(t, first, last, "the text is as dark on the last of %d frames as on the first", total)
	}
}

func TestThunderstorm_Params(t *testing.T) {
	type P = shader.ThunderstormParams
	d := shader.DefaultThunderstormParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(with(d, func(p *P) { p.StrikeCount = 0; p.RainDensity = 0 }))},
		{name: "negative strike count", params: with(d, func(p *P) { p.StrikeCount = -1 })},
		{name: "huge strike count is capped", params: with(d, func(p *P) { p.StrikeCount = math.MaxInt }), sameAs: same(with(d, func(p *P) { p.StrikeCount = 64 }))},
		{name: "huge rain density is capped", params: with(d, func(p *P) { p.RainDensity = 1e300 }), sameAs: same(with(d, func(p *P) { p.RainDensity = 4 }))},
		accepted("a dry calm", with(d, func(p *P) { p.StrikeCount, p.RainDensity = 0, 0 })),
	}
	tcases = append(tcases, invalidFloats(d, "StormBrightness", func(p *P, v float64) { p.StormBrightness = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "StrikeDuration", func(p *P, v float64) { p.StrikeDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "RainDensity", func(p *P, v float64) { p.RainDensity = v }, -0.1)...)
	tcases = append(tcases, invalidSymbols(d, "RaindropSymbols", func(p *P, v []rune) { p.RaindropSymbols = v })...)
	tcases = append(tcases, invalidSymbols(d, "SparkSymbols", func(p *P, v []rune) { p.SparkSymbols = v })...)
	testParams(t, shader.Thunderstorm, tcases)
}

func FuzzThunderstorm(f *testing.F) {
	d := shader.DefaultThunderstormParams()
	fuzzSeed(
		f,
		uint32(d.LightningColor), uint32(d.GlowColor), string(d.RaindropSymbols),
		uint32(d.RaindropColor), string(d.SparkSymbols), uint32(d.SparkColor),
		d.StormBrightness, d.StrikeCount, d.StrikeDuration, d.RainDensity, d.Seed,
	)
	fuzzSeed(
		f,
		uint32(0), uint32(0), "", uint32(0), "", uint32(0), 0.0, 0, 0.0, 0.0,
		int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		lightningColor, glowColor uint32,
		raindropSymbols string,
		raindropColor uint32,
		sparkSymbols string,
		sparkColor uint32,
		stormBrightness float64,
		strikeCount int,
		strikeDuration, rainDensity float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Thunderstorm(shader.ThunderstormParams{
				LightningColor:  term.Color(lightningColor),
				GlowColor:       term.Color(glowColor),
				RaindropSymbols: fuzzRunes(raindropSymbols),
				RaindropColor:   term.Color(raindropColor),
				SparkSymbols:    fuzzRunes(sparkSymbols),
				SparkColor:      term.Color(sparkColor),
				StormBrightness: stormBrightness,
				StrikeCount:     strikeCount,
				StrikeDuration:  strikeDuration,
				RainDensity:     rainDensity,
				Seed:            seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzLoopShade(t, sh, data, frame, total)
		}
	})
}
