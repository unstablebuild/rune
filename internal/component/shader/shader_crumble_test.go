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

func TestCrumble_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Crumble(shader.DefaultCrumbleParams(), term.Attributes{}))
}

func TestCrumble_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Crumble(shader.DefaultCrumbleParams(), defaultAttr)
	})
}

func TestCrumble_DustPilesAtTheBottom(t *testing.T) {
	params := shader.DefaultCrumbleParams()
	params.DustSymbols = []rune{'.'}
	sh := shader.Crumble(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 100
	in := makeBlankCells(10, 10)
	for x := range 10 {
		in[0][x] = term.NewCell('A', 1, term.Attributes{Fg: term.ColorWhite})
	}

	crumbled := cloneCells(in)
	sh.Shade(int(params.FallDuration*total)-1, total, crumbled)
	for x := range 10 {
		assert.Equal(t, '.', crumbled[9][x].Ch, "col %d of the bottom row holds dust", x)
		assert.Equal(t, ' ', crumbled[0][x].Ch, "col %d of the top row is empty", x)
	}

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestCrumble_BackgroundTravels(t *testing.T) {
	sh := shader.Crumble(shader.DefaultCrumbleParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestCrumble_Params(t *testing.T) {
	type P = shader.CrumbleParams
	d := shader.DefaultCrumbleParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(d)},
		{name: "phases not fitting", params: with(d, func(p *P) { p.FallDuration, p.VacuumDuration = 0.7, 0.3 })},
		accepted("full brightness", with(d, func(p *P) { p.WeakBrightness, p.DustBrightness = 1, 1 })),
	}
	tcases = append(tcases, invalidFloats(d, "WeakBrightness", func(p *P, v float64) { p.WeakBrightness = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "DustBrightness", func(p *P, v float64) { p.DustBrightness = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "FallDuration", func(p *P, v float64) { p.FallDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "VacuumDuration", func(p *P, v float64) { p.VacuumDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidSymbols(d, "DustSymbols", func(p *P, v []rune) { p.DustSymbols = v })...)
	tcases = append(tcases, invalidEasings(d, "FallEasing", func(p *P, v shaderutils.Easing) { p.FallEasing = v })...)
	tcases = append(tcases, invalidEasings(d, "VacuumEasing", func(p *P, v shaderutils.Easing) { p.VacuumEasing = v })...)
	testParams(t, shader.Crumble, tcases)
}

func FuzzCrumble(f *testing.F) {
	d := shader.DefaultCrumbleParams()
	fuzzSeed(
		f,
		string(d.DustSymbols), d.WeakBrightness, d.DustBrightness,
		uint32(d.FlashColor), uint8(0), uint8(0), d.FallDuration, d.VacuumDuration,
		d.Seed,
	)
	fuzzSeed(
		f,
		"", 0.0, 0.0, uint32(0), uint8(0), uint8(0), 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		dustSymbols string,
		weakBrightness, dustBrightness float64,
		flashColor uint32,
		fallEasing, vacuumEasing uint8,
		fallDuration, vacuumDuration float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Crumble(shader.CrumbleParams{
				DustSymbols:    fuzzRunes(dustSymbols),
				WeakBrightness: weakBrightness,
				DustBrightness: dustBrightness,
				FlashColor:     term.Color(flashColor),
				FallEasing:     fuzzEasing(fallEasing),
				VacuumEasing:   fuzzEasing(vacuumEasing),
				FallDuration:   fallDuration,
				VacuumDuration: vacuumDuration,
				Seed:           seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
