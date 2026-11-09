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
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestRings_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Rings(shader.DefaultRingsParams(), term.Attributes{}))
}

func TestRings_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Rings(shader.DefaultRingsParams(), defaultAttr)
	})
}

func TestRings_GathersCharactersIntoRings(t *testing.T) {
	params := shader.DefaultRingsParams()
	sh := shader.Rings(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 1000
	in := makeBlankCells(60, 20)
	for y := 5; y < 15; y++ {
		for x := 10; x < 50; x++ {
			in[y][x] = term.NewCell('a', 1, term.Attributes{Fg: term.ColorWhite})
		}
	}

	held := cloneCells(in)
	sh.Shade(int(params.HoldDuration*total/2), total, held)
	assert.Equal(t, in, held, "the text holds still before the rings form")

	// Deep into a spin, every character on a ring sits on a circle
	// around the center and shows its ring's color.
	spinning := cloneCells(in)
	spinFrame := int((params.HoldDuration + params.DisperseDuration + params.SpinDuration*0.9) * (total - 1))
	sh.Shade(spinFrame, total, spinning)
	ringColored := 0
	for y := range spinning {
		for x := range spinning[y] {
			if spinning[y][x].Ch == 'a' && slices.Contains(params.RingColors, spinning[y][x].Fg) {
				ringColored++
			}
		}
	}
	assert.Greater(t, ringColored, 100, "the spinning rings show the ring colors")
	assert.Less(t, countChar(spinning, 'a'), 400, "characters that fit no ring are off the canvas while the rings spin")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestRings_BackgroundTravels(t *testing.T) {
	sh := shader.Rings(shader.DefaultRingsParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestRings_Params(t *testing.T) {
	type P = shader.RingsParams
	d := shader.DefaultRingsParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(with(d, func(p *P) { p.HoldDuration = 0 }))},
		{name: "negative cycles", params: with(d, func(p *P) { p.Cycles = -1 })},
		{name: "cycles not fitting", params: with(d, func(p *P) { p.Cycles = 4 })},
		{name: "huge cycles", params: with(d, func(p *P) { p.Cycles = math.MaxInt })},
		{name: "maximum spin below the minimum", params: with(d, func(p *P) { p.MinSpin, p.MaxSpin = 20, 10 })},
		{name: "huge spins are capped", params: with(d, func(p *P) { p.MinSpin, p.MaxSpin = 1e300, 1e300 }), sameAs: same(with(d, func(p *P) { p.MinSpin, p.MaxSpin = 1e4, 1e4 }))},
		accepted("one tight ring cycle", with(d, func(p *P) { p.RingGap, p.HoldDuration, p.Cycles = 1, 0, 1 })),
	}
	tcases = append(tcases, invalidFloats(d, "RingGap", func(p *P, v float64) { p.RingGap = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "HoldDuration", func(p *P, v float64) { p.HoldDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "DisperseDuration", func(p *P, v float64) { p.DisperseDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "SpinDuration", func(p *P, v float64) { p.SpinDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "MinSpin", func(p *P, v float64) { p.MinSpin = v }, -1)...)
	tcases = append(tcases, invalidFloats(d, "MaxSpin", func(p *P, v float64) { p.MaxSpin = v }, -1)...)
	testParams(t, shader.Rings, tcases)
}

func FuzzRings(f *testing.F) {
	d := shader.DefaultRingsParams()
	fuzzSeed(
		f,
		fuzzColorSeed(d.RingColors), d.RingGap, d.HoldDuration, d.DisperseDuration,
		d.SpinDuration, d.Cycles, d.MinSpin, d.MaxSpin, d.Seed,
	)
	fuzzSeed(
		f,
		[]byte(nil), 0.0, 0.0, 0.0, 0.0, 0, 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		ringColors []byte,
		ringGap, holdDuration, disperseDuration, spinDuration float64,
		cycles int,
		minSpin, maxSpin float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Rings(shader.RingsParams{
				RingColors:       fuzzColors(ringColors),
				RingGap:          ringGap,
				HoldDuration:     holdDuration,
				DisperseDuration: disperseDuration,
				SpinDuration:     spinDuration,
				Cycles:           cycles,
				MinSpin:          minSpin,
				MaxSpin:          maxSpin,
				Seed:             seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
