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

func TestUnstable_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Unstable(shader.DefaultUnstableParams(), term.Attributes{}))
}

func TestUnstable_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Unstable(shader.DefaultUnstableParams(), defaultAttr)
	})
}

func TestUnstable_ShufflesThenBlowsApart(t *testing.T) {
	params := shader.DefaultUnstableParams()
	sh := shader.Unstable(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 100
	in := makeBlankCells(20, 10)
	for i, ch := range "abcdefghij" {
		in[i][i] = term.NewCell(ch, 1, term.Attributes{Fg: term.ColorWhite})
	}

	shuffled := cloneCells(in)
	sh.Shade(0, total, shuffled)
	var moved int
	for y := range in {
		for x := range in[y] {
			if in[y][x].Ch != ' ' && shuffled[y][x].Ch != in[y][x].Ch {
				moved++
			}
		}
	}
	assert.NotZero(t, moved, "the characters start shuffled among their places")

	exploded := cloneCells(in)
	sh.Shade(int((params.RumbleDuration+params.ExplosionDuration)*total), total, exploded)
	for y := range exploded {
		for x := range exploded[y] {
			if exploded[y][x].Ch != ' ' {
				onEdge := x == 0 || y == 0 || x == 19 || y == 9
				assert.True(t, onEdge, "character at %d,%d is on an edge", x, y)
			}
		}
	}

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestUnstable_BackgroundTravels(t *testing.T) {
	sh := shader.Unstable(shader.DefaultUnstableParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestUnstable_Params(t *testing.T) {
	type P = shader.UnstableParams
	d := shader.DefaultUnstableParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(with(d, func(p *P) { p.HoldDuration = 0 }))},
		{name: "phases not fitting", params: with(d, func(p *P) { p.RumbleDuration, p.ExplosionDuration, p.HoldDuration = 0.5, 0.3, 0.2 })},
		accepted("no hold", with(d, func(p *P) { p.HoldDuration = 0 })),
	}
	tcases = append(tcases, invalidFloats(d, "RumbleDuration", func(p *P, v float64) { p.RumbleDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "ExplosionDuration", func(p *P, v float64) { p.ExplosionDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "HoldDuration", func(p *P, v float64) { p.HoldDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidEasings(d, "ExplosionEasing", func(p *P, v shaderutils.Easing) { p.ExplosionEasing = v })...)
	tcases = append(tcases, invalidEasings(d, "ReassemblyEasing", func(p *P, v shaderutils.Easing) { p.ReassemblyEasing = v })...)
	testParams(t, shader.Unstable, tcases)
}

func FuzzUnstable(f *testing.F) {
	d := shader.DefaultUnstableParams()
	fuzzSeed(
		f,
		uint32(d.UnstableColor), uint8(0), uint8(0), d.RumbleDuration,
		d.ExplosionDuration, d.HoldDuration, d.Seed,
	)
	fuzzSeed(
		f,
		uint32(0), uint8(0), uint8(0), 0.0, 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		unstableColor uint32,
		explosionEasing, reassemblyEasing uint8,
		rumbleDuration, explosionDuration, holdDuration float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Unstable(shader.UnstableParams{
				UnstableColor:     term.Color(unstableColor),
				ExplosionEasing:   fuzzEasing(explosionEasing),
				ReassemblyEasing:  fuzzEasing(reassemblyEasing),
				RumbleDuration:    rumbleDuration,
				ExplosionDuration: explosionDuration,
				HoldDuration:      holdDuration,
				Seed:              seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
