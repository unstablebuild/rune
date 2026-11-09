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

func TestBeams_Reveal(t *testing.T) {
	testReveal(t, textStays, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultBeamsParams()
		params.From = from
		return shader.Beams(params, defaultAttr)
	})
}

func TestBeams_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Beams(shader.DefaultBeamsParams(), term.Attributes{}))
}

func TestBeams_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Beams(shader.DefaultBeamsParams(), defaultAttr)
	})
}

func TestBeams_SweepsBeamSymbolsOverTheCanvas(t *testing.T) {
	params := shader.DefaultBeamsParams()
	params.RowSymbols = []rune{'-'}
	params.ColumnSymbols = []rune{'|'}
	sh := shader.Beams(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 200
	in := makeCharCells(20, 10)

	first := cloneCells(in)
	sh.Shade(0, total, first)
	assert.Equal(t, 0, countChar(first, 'A'), "the text is hidden until a beam reveals it")

	rowBeams, colBeams := 0, 0
	for frame := range total {
		got := cloneCells(in)
		sh.Shade(frame, total, got)
		rowBeams += countChar(got, '-')
		colBeams += countChar(got, '|')
	}
	assert.NotZero(t, rowBeams, "row beams sweep the canvas")
	assert.NotZero(t, colBeams, "column beams sweep the canvas")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestBeams_BackgroundTravels(t *testing.T) {
	sh := shader.Beams(shader.DefaultBeamsParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestBeams_Params(t *testing.T) {
	type P = shader.BeamsParams
	d := shader.DefaultBeamsParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(d)},
		{name: "trail capped to half the wipe", params: with(d, func(p *P) { p.TrailDuration = 0.5 }), sameAs: same(with(d, func(p *P) { p.TrailDuration = 0.125 }))},
		{name: "row sweep capped to the beams phase", params: with(d, func(p *P) { p.RowSweep = 1 }), sameAs: same(with(d, func(p *P) { p.RowSweep = 0.75 }))},
		{name: "column sweep capped to the beams phase", params: with(d, func(p *P) { p.ColumnSweep = 0.9 }), sameAs: same(with(d, func(p *P) { p.ColumnSweep = 0.75 }))},
	}
	tcases = append(tcases, invalidFloats(d, "DimBrightness", func(p *P, v float64) { p.DimBrightness = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "BeamsDuration", func(p *P, v float64) { p.BeamsDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "TrailDuration", func(p *P, v float64) { p.TrailDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidFloats(d, "RowSweep", func(p *P, v float64) { p.RowSweep = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "ColumnSweep", func(p *P, v float64) { p.ColumnSweep = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidSymbols(d, "RowSymbols", func(p *P, v []rune) { p.RowSymbols = v })...)
	tcases = append(tcases, invalidSymbols(d, "ColumnSymbols", func(p *P, v []rune) { p.ColumnSymbols = v })...)
	testParams(t, shader.Beams, tcases)
}

func FuzzBeams(f *testing.F) {
	d := shader.DefaultBeamsParams()
	fuzzSeed(
		f,
		string(d.RowSymbols), string(d.ColumnSymbols), fuzzColorSeed(d.BeamGradient),
		d.DimBrightness, d.BeamsDuration, d.TrailDuration, d.RowSweep, d.ColumnSweep,
		d.Seed,
	)
	fuzzSeed(
		f,
		"", "", []byte(nil), 0.0, 0.0, 0.0, 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		rowSymbols, columnSymbols string,
		beamGradient []byte,
		dimBrightness, beamsDuration, trailDuration, rowSweep, columnSweep float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Beams(shader.BeamsParams{
				RowSymbols:    fuzzRunes(rowSymbols),
				ColumnSymbols: fuzzRunes(columnSymbols),
				BeamGradient:  fuzzColors(beamGradient),
				DimBrightness: dimBrightness,
				BeamsDuration: beamsDuration,
				TrailDuration: trailDuration,
				RowSweep:      rowSweep,
				ColumnSweep:   columnSweep,
				Seed:          seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
