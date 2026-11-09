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
	"image"
	"testing"

	"unstable.build/rune/internal/component/shader/shaderutils"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestPrint_Reveal(t *testing.T) {
	testReveal(t, textMoves, func(from shader.Snapshot, defaultAttr term.Attributes) shader.ImageShader {
		params := shader.DefaultPrintParams()
		params.From = from
		return shader.Print(params, defaultAttr)
	})
}

func TestPrint_Suite(t *testing.T) {
	shadertest.TestShader(t, shader.Print(shader.DefaultPrintParams(), term.Attributes{}))
}

func TestPrint_ThePictureScrollsWithTheRows(t *testing.T) {
	const cols, rows, total = 24, 10, 60
	_, text, textImages := revealScreens(cols, rows)
	// A wallpaper's cells are all alike under its picture.
	wallpaper := make([][]term.Cell, rows)
	for y := range wallpaper {
		wallpaper[y] = make([]term.Cell, cols)
		for x := range wallpaper[y] {
			wallpaper[y][x] = term.NewCell(braille.Blank, 1, term.Attributes{Bg: wallpaperBg})
		}
	}
	for _, tc := range []struct {
		name   string
		cells  [][]term.Cell
		images []term.Image
	}{
		{"text", text, textImages},
		{"wallpaper", wallpaper, []term.Image{cellPicture(image.Rect(0, 0, cols, rows))}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sh := shader.Print(shader.DefaultPrintParams(), revealAttr)
			scrolled := false
			for frame := range total {
				images := sh.ShadeImages(frame, total, cloneCells(tc.cells), copyPictures(tc.images))
				shifts := map[image.Point]bool{}
				for y := range rows {
					for x := range cols {
						for _, c := range pixelsAt(images, x, y) {
							sx, sy, _ := pictureCell(c)
							shifts[image.Pt(x-sx, y-sy)] = true
						}
					}
				}
				require.LessOrEqual(t, len(shifts), 1, "frame %d tears the picture apart: %v", frame, shifts)
				scrolled = scrolled || len(shifts) == 1 && !shifts[image.Point{}]
			}
			assert.True(t, scrolled, "the picture never scrolled")
		})
	}
}

func TestPrint_EdgeCases(t *testing.T) {
	testShaderEdgeCases(t, func(defaultAttr term.Attributes) shader.Shader {
		return shader.Print(shader.DefaultPrintParams(), defaultAttr)
	})
}

func TestPrint_TypesRowsAtTheBottomAndScrollsThemUp(t *testing.T) {
	params := shader.DefaultPrintParams()
	params.HeadSymbol = 'H'
	sh := shader.Print(params, term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	const total = 200
	in := makeBlankCells(10, 4)
	for y, line := range []string{"aaaa", "bbbb", "cccc", "dddd"} {
		for x, ch := range line {
			in[y][x] = term.NewCell(ch, 1, term.Attributes{Fg: term.ColorWhite})
		}
	}

	head, scrolled := 0, 0
	for frame := range total {
		got := cloneCells(in)
		sh.Shade(frame, total, got)
		head += countChar(got, 'H')
		for y := range 3 {
			assert.Zero(t, countChar(got[y:y+1], 'd'), "frame %d: the last row only ever prints at the bottom", frame)
		}
		if got[3][0].Ch == 'b' && got[2][0].Ch == 'a' {
			scrolled++
		}
	}
	assert.NotZero(t, head, "the print head shows between rows")
	assert.NotZero(t, scrolled, "printed rows scroll up as the next one prints")

	assertLastFrameMatchesInput(t, sh, in, total)
	assertFramesAfterTotalLeaveInput(t, sh, in, total)
}

func TestPrint_BackgroundTravels(t *testing.T) {
	sh := shader.Print(shader.DefaultPrintParams(), term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack})
	assertBackgroundTravels(t, sh, 200, true)
}

func TestPrint_Params(t *testing.T) {
	type P = shader.PrintParams
	d := shader.DefaultPrintParams()
	tcases := []paramsCase[P]{
		accepted("defaults", d),
		{name: "zero values select the defaults", params: P{}, sameAs: same(d)},
		{name: "unprintable head symbol", params: with(d, func(p *P) { p.HeadSymbol = '\n' })},
		{name: "wide head symbol", params: with(d, func(p *P) { p.HeadSymbol = '世' })},
		{name: "huge speeds are capped", params: with(d, func(p *P) { p.ReturnSpeed, p.PrintSpeed = 1e300, 1e300 }), sameAs: same(with(d, func(p *P) { p.ReturnSpeed, p.PrintSpeed = 1000, 1000 }))},
		{name: "tiny speeds are capped", params: with(d, func(p *P) { p.ReturnSpeed, p.PrintSpeed = 1e-300, 1e-300 }), sameAs: same(with(d, func(p *P) { p.ReturnSpeed, p.PrintSpeed = 1e-3, 1e-3 }))},
	}
	tcases = append(tcases, invalidFloats(d, "ReturnSpeed", func(p *P, v float64) { p.ReturnSpeed = v }, -1)...)
	tcases = append(tcases, invalidFloats(d, "PrintSpeed", func(p *P, v float64) { p.PrintSpeed = v }, -1)...)
	tcases = append(tcases, invalidSymbols(d, "TypingSymbols", func(p *P, v []rune) { p.TypingSymbols = v })...)
	tcases = append(tcases, invalidEasings(d, "ReturnEasing", func(p *P, v shaderutils.Easing) { p.ReturnEasing = v })...)
	testParams(t, shader.Print, tcases)
}

func FuzzPrint(f *testing.F) {
	d := shader.DefaultPrintParams()
	fuzzSeed(
		f,
		d.HeadSymbol, uint32(d.HeadColor), uint32(d.TypedColor),
		string(d.TypingSymbols), uint8(0), d.ReturnSpeed, d.PrintSpeed,
	)
	fuzzSeed(
		f,
		rune(0), uint32(0), uint32(0), "", uint8(0), 0.0, 0.0,
	)
	f.Fuzz(func(
		t *testing.T, data []byte, frame, total int, attrs uint64,
		headSymbol rune,
		headColor, typedColor uint32,
		typingSymbols string,
		returnEasing uint8,
		returnSpeed, printSpeed float64,
	) {
		sh, ok := fuzzBuild(t, func() shader.Shader {
			return shader.Print(shader.PrintParams{
				HeadSymbol:    headSymbol,
				HeadColor:     term.Color(headColor),
				TypedColor:    term.Color(typedColor),
				TypingSymbols: fuzzRunes(typingSymbols),
				ReturnEasing:  fuzzEasing(returnEasing),
				ReturnSpeed:   returnSpeed,
				PrintSpeed:    printSpeed,
			}, fuzzAttrs(attrs))
		})
		if ok {
			fuzzShade(t, sh, data, frame, total)
		}
	})
}
