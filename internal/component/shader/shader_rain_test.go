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

func TestRain_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Rain(shader.DefaultRainParams(), term.Attributes{}))
}

func TestRain_Reveal(t *testing.T) {
	testReveal(t, textStays, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultRainParams()
		params.From = from
		return shader.Rain(params, defaultAttr)
	})
}

func TestRain_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Rain(shader.DefaultRainParams(), defaultAttr)
	})
}

func TestRain_DropsCarryRainSymbols(t *testing.T) {
	params := shader.DefaultRainParams()
	params.RainSymbols = []rune{'|'}
	sh := shader.Rain(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 100
	in := makeBlankCells(10, 10)
	for x := range 10 {
		in[9][x] = term.NewCell('A', 1, term.Attributes{Fg: term.ColorWhite})
	}

	drops := 0
	for frame := range total {
		got := cloneCells(in)
		sh.Shade(frame, total, got)
		for y := range 9 {
			for x := range 10 {
				if got[y][x].Ch == '|' {
					drops++
				}
			}
		}
	}
	assert.NotZero(t, drops, "raindrops fall through the rows above the text")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestRain_BackgroundTravels(t *testing.T) {
	sh := shader.Rain(shader.DefaultRainParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestRain_Params(t *testing.T) {
	type P = shader.RainParams
	d := shader.DefaultRainParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(with(d, func(p *P) { p.FadeDuration = 0; p.SpeedJitter = 0 }))},
		{name: "fall and fade not fitting", params: with(d, func(p *P) { p.FallDuration, p.FadeDuration = 0.6, 0.4 })},
		accepted("no fade nor jitter", with(d, func(p *P) { p.FadeDuration, p.SpeedJitter = 0, 0 })),
	}
	tcases = append(tcases, invalidFloats(d, "FallDuration", func(p *P, v float64) { p.FallDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "FadeDuration", func(p *P, v float64) { p.FadeDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "SpeedJitter", func(p *P, v float64) { p.SpeedJitter = v }, -0.1, 1)...)
	tcases = append(tcases, invalidSymbols(d, "RainSymbols", func(p *P, v []rune) { p.RainSymbols = v })...)
	tcases = append(tcases, invalidEasings(d, "Easing", func(p *P, v shaderutils.Easing) { p.Easing = v })...)
	testParams(t, shader.Rain, tcases)
}

func FuzzRain(f *testing.F) {
	d := shader.DefaultRainParams()
	fuzzSeed(
		f,
		fuzzColorSeed(d.RainColors), string(d.RainSymbols), uint8(0), d.FallDuration,
		d.FadeDuration, d.SpeedJitter, d.Seed,
	)
	fuzzSeed(
		f,
		[]byte(nil), "", uint8(0), 0.0, 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		rainColors []byte,
		rainSymbols string,
		easing uint8,
		fallDuration, fadeDuration, speedJitter float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Rain(shader.RainParams{
				RainColors:   fuzzColors(rainColors),
				RainSymbols:  fuzzRunes(rainSymbols),
				Easing:       fuzzEasing(easing),
				FallDuration: fallDuration,
				FadeDuration: fadeDuration,
				SpeedJitter:  speedJitter,
				Seed:         seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
