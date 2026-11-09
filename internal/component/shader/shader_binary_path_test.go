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
	"unstable.build/rune/internal/component/shader/shaderutils"
)

func TestBinaryPath_Reveal(t *testing.T) {
	testReveal(t, textStays, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultBinaryPathParams()
		params.From = from
		return shader.BinaryPath(params, defaultAttr)
	})
}

func TestBinaryPath_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.BinaryPath(shader.DefaultBinaryPathParams(), term.Attributes{}))
}

func TestBinaryPath_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.BinaryPath(shader.DefaultBinaryPathParams(), defaultAttr)
	})
}

func TestBinaryPath_DigitsDeliverThenWipeBrightens(t *testing.T) {
	params := shader.DefaultBinaryPathParams()
	white := term.NewRGBColor(0xff, 0xff, 0xff)
	sh := shader.BinaryPath(params, term.Attributes{Fg: white})
	const total = 1000
	in := makeBlankCells(60, 20)
	for y := 5; y < 15; y++ {
		for x := 10; x < 50; x++ {
			in[y][x] = term.NewCell('a', 1, term.Attributes{Fg: white})
		}
	}

	first := cloneCells(in)
	sh.Shade(0, total, first)
	assert.Zero(t, countChar(first, 'a'), "characters wait for their digits")

	travelling := cloneCells(in)
	sh.Shade(int(params.TravelDuration*(total-1)/2), total, travelling)
	digits := countChar(travelling, '0') + countChar(travelling, '1')
	assert.NotZero(t, digits, "binary digits travel the canvas")
	for y := range travelling {
		for x := range travelling[y] {
			switch travelling[y][x].Ch {
			case '0', '1':
				assert.Contains(t, params.BinaryColors, travelling[y][x].Fg, "digit at %d,%d", x, y)
			}
		}
	}
	delivered := countChar(travelling, 'a')
	assert.NotZero(t, delivered, "some characters have been delivered")
	assert.Less(t, delivered, 400, "some characters are still to come")

	// Once delivered, every character shows dim until the wipe.
	dim := shaderutils.AdjustBrightness(white, params.DimBrightness, white)
	wiping := cloneCells(in)
	sh.Shade(int(params.TravelDuration*(total-1))+1, total, wiping)
	assert.Equal(t, 400, countChar(wiping, 'a'), "all characters are delivered")
	for y := 5; y < 15; y++ {
		for x := 10; x < 50; x++ {
			assert.Equal(t, dim, wiping[y][x].Fg, "cell %d,%d is dim", x, y)
		}
	}
	midWipe := cloneCells(in)
	sh.Shade(int((params.TravelDuration+(1-params.TravelDuration)/2)*(total-1)), total, midWipe)
	assert.Equal(t, white, midWipe[5][49].Fg, "the top right corner is wiped first")
	assert.Equal(t, dim, midWipe[14][10].Fg, "the bottom left corner is wiped last")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestBinaryPath_BackgroundTravels(t *testing.T) {
	sh := shader.BinaryPath(shader.DefaultBinaryPathParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestBinaryPath_Params(t *testing.T) {
	type P = shader.BinaryPathParams
	d := shader.DefaultBinaryPathParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(d)},
		accepted("every character at once", with(d, func(p *P) { p.ActiveGroups, p.DimBrightness = 1, 1 })),
	}
	tcases = append(tcases, invalidFloats(d, "DimBrightness", func(p *P, v float64) { p.DimBrightness = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "ActiveGroups", func(p *P, v float64) { p.ActiveGroups = v }, -0.1, 1.1)...)
	tcases = append(tcases, invalidFloats(d, "TravelDuration", func(p *P, v float64) { p.TravelDuration = v }, -0.1, 1)...)
	testParams(t, shader.BinaryPath, tcases)
}

func FuzzBinaryPath(f *testing.F) {
	d := shader.DefaultBinaryPathParams()
	fuzzSeed(
		f,
		fuzzColorSeed(d.BinaryColors), uint32(d.CollapseColor), d.DimBrightness,
		d.ActiveGroups, d.TravelDuration, d.Seed,
	)
	fuzzSeed(
		f,
		[]byte(nil), uint32(0), 0.0, 0.0, 0.0, int64(0),
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		binaryColors []byte,
		collapseColor uint32,
		dimBrightness, activeGroups, travelDuration float64,
		seed int64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.BinaryPath(shader.BinaryPathParams{
				BinaryColors:   fuzzColors(binaryColors),
				CollapseColor:  term.Color(collapseColor),
				DimBrightness:  dimBrightness,
				ActiveGroups:   activeGroups,
				TravelDuration: travelDuration,
				Seed:           seed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
