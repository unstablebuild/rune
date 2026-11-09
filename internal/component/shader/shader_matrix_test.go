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

func TestMatrix_Reveal(t *testing.T) {
	testReveal(t, textStays, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultMatrixParams()
		params.From = from
		return shader.Matrix(params, defaultAttr)
	})
}

func TestMatrix_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Matrix(shader.DefaultMatrixParams(), term.Attributes{}))
}

func TestMatrix_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Matrix(shader.DefaultMatrixParams(), defaultAttr)
	})
}

func TestMatrix_RainsThenFillsThenResolves(t *testing.T) {
	params := shader.DefaultMatrixParams()
	params.RainSymbols = []rune{'#'}
	sh := shader.Matrix(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 300
	in := makeCharCells(20, 10)

	first := cloneCells(in)
	sh.Shade(0, total, first)
	assert.Zero(t, countChar(first, 'A'), "the text is hidden under the rain")

	raining := 0
	for frame := range int(params.RainDuration * total) {
		got := cloneCells(in)
		sh.Shade(frame, total, got)
		raining += countChar(got, '#')
	}
	assert.NotZero(t, raining, "rain streaks fall")

	full := cloneCells(in)
	sh.Shade(int((params.RainDuration+params.FillDuration)*(total-1)), total, full)
	assert.Equal(t, 200, countChar(full, '#'), "the rain fills the canvas before resolving")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestMatrix_BackgroundTravels(t *testing.T) {
	sh := shader.Matrix(shader.DefaultMatrixParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestMatrix_Params(t *testing.T) {
	type P = shader.MatrixParams
	d := shader.DefaultMatrixParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(with(d, func(p *P) { p.StreakGap = 0; p.SymbolSwapChance = 0; p.ColorSwapChance = 0 }))},
		{name: "phases not fitting", params: with(d, func(p *P) { p.RainDuration, p.FillDuration = 0.7, 0.3 })},
		{name: "tiny fall step is capped", params: with(d, func(p *P) { p.FallStep = 1e-12 }), sameAs: same(with(d, func(p *P) { p.FallStep = 1e-3 }))},
		{name: "vanishing swap chances never swap", params: with(d, func(p *P) { p.SymbolSwapChance, p.ColorSwapChance = 1e-300, 1e-300 }), sameAs: same(with(d, func(p *P) { p.SymbolSwapChance, p.ColorSwapChance = 0, 0 }))},
		accepted("certain swaps and no gap", with(d, func(p *P) { p.SymbolSwapChance, p.ColorSwapChance, p.StreakGap = 1, 1, 0 })),
	}
	tcases = append(tcases, invalidFloats(d, "RainDuration", func(p *P, v float64) { p.RainDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "FillDuration", func(p *P, v float64) { p.FillDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "FallStep", func(p *P, v float64) { p.FallStep = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "StreakGap", func(p *P, v float64) { p.StreakGap = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "SymbolSwapChance", func(p *P, v float64) { p.SymbolSwapChance = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "ColorSwapChance", func(p *P, v float64) { p.ColorSwapChance = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidSymbols(d, "RainSymbols", func(p *P, v []rune) { p.RainSymbols = v })...)
	testParams(t, shader.Matrix, tcases)
}

func FuzzMatrix(f *testing.F) {
	d := shader.DefaultMatrixParams()
	fuzzSeed(
		f,
		uint32(d.HighlightColor), fuzzColorSeed(d.RainGradient),
		string(d.RainSymbols), d.RainDuration, d.FillDuration, d.FallStep,
		d.StreakGap, d.SymbolSwapChance, d.ColorSwapChance, d.Seed,
	)
	fuzzSeed(
		f,
		uint32(0), []byte(nil), "", 0.0, 0.0, 0.0, 0.0, 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		highlightColor uint32,
		rainGradient []byte,
		rainSymbols string,
		rainDuration, fillDuration, fallStep, streakGap, symbolSwapChance, colorSwapChance float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Matrix(shader.MatrixParams{
				HighlightColor:   term.Color(highlightColor),
				RainGradient:     fuzzColors(rainGradient),
				RainSymbols:      fuzzRunes(rainSymbols),
				RainDuration:     rainDuration,
				FillDuration:     fillDuration,
				FallStep:         fallStep,
				StreakGap:        streakGap,
				SymbolSwapChance: symbolSwapChance,
				ColorSwapChance:  colorSwapChance,
				Seed:             seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
