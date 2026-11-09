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

	"unstable.build/rune/internal/component/shader/shaderutils"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestSlide_Reveal(t *testing.T) {
	testReveal(t, textMoves, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultSlideParams()
		params.From = from
		return shader.Slide(params, defaultAttr)
	})
}

func TestSlide_Suite(t *testing.T) {
	for _, grouping := range []shader.SlideGrouping{shader.SlideRows, shader.SlideColumns, shader.SlideDiagonals} {
		for _, merge := range []bool{false, true} {
			for _, reverse := range []bool{false, true} {
				params := shader.DefaultSlideParams()
				params.Grouping = grouping
				params.Merge = merge
				params.ReverseDirection = reverse
				shadertest.TestShader(t, shader.Slide(params, term.Attributes{}))
			}
		}
	}
}

func TestSlide_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Slide(shader.DefaultSlideParams(), defaultAttr)
	})
}

func TestSlide_RowsEnterTopFirst(t *testing.T) {
	sh := shader.Slide(shader.DefaultSlideParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 100
	in := makeCharCells(10, 10)

	mid := cloneCells(in)
	sh.Shade(total/2, total, mid)
	assert.Equal(t, 'A', mid[0][9].Ch, "the top row has slid in by half time")
	assert.Equal(t, ' ', mid[9][9].Ch, "the bottom row has not entered at half time")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestSlide_BackgroundTravels(t *testing.T) {
	sh := shader.Slide(shader.DefaultSlideParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestSlide_Params(t *testing.T) {
	type P = shader.SlideParams
	d := shader.DefaultSlideParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(d)},
		{name: "unknown grouping", params: with(d, func(p *P) { p.Grouping = shader.SlideDiagonals + 1 })},
		{name: "negative gap", params: with(d, func(p *P) { p.Gap = -1 })},
		{name: "huge gap is capped", params: with(d, func(p *P) { p.Gap = math.MaxInt }), sameAs: same(with(d, func(p *P) { p.Gap = 1024 }))},
		accepted("merging columns", with(d, func(p *P) { p.Grouping, p.Merge = shader.SlideColumns, true })),
		accepted("reversed diagonals", with(d, func(p *P) { p.Grouping, p.ReverseDirection = shader.SlideDiagonals, true })),
	}
	tcases = append(tcases, invalidFloats(d, "SlideDuration", func(p *P, v float64) { p.SlideDuration = v }, -0.1, 1)...)
	tcases = append(tcases, invalidEasings(d, "Easing", func(p *P, v shaderutils.Easing) { p.Easing = v })...)
	testParams(t, shader.Slide, tcases)
}

func FuzzSlide(f *testing.F) {
	d := shader.DefaultSlideParams()
	fuzzSeed(
		f,
		uint8(d.Grouping), d.Gap, d.ReverseDirection, d.Merge, uint8(0),
		uint32(d.StartColor), d.SlideDuration,
	)
	fuzzSeed(
		f,
		uint8(0), 0, false, false, uint8(0), uint32(0), 0.0,
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		grouping uint8,
		gap int,
		reverseDirection, merge bool,
		easing uint8,
		startColor uint32,
		slideDuration float64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Slide(shader.SlideParams{
				Grouping:         shader.SlideGrouping(grouping),
				Gap:              gap,
				ReverseDirection: reverseDirection,
				Merge:            merge,
				Easing:           fuzzEasing(easing),
				StartColor:       term.Color(startColor),
				SlideDuration:    slideDuration,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
