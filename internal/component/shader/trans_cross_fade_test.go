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

package shader

import (
	"image"
	"image/color"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

func TestTransitionCrossFade(t *testing.T) {
	t.Run("noise is applied only within cross fade area", func(t *testing.T) {
		shRedAAAs := newConstantShader('A', term.GetColor("darkred"), term.ColorRed)
		shGreenBBBs := newConstantShader('B', term.GetColor("darkgreen"), term.ColorGreen)

		type frRange struct {
			start, end int
		}

		overlapPerc := 0.4
		total := 10
		onlyShaderARenders := frRange{0, 4}
		bothShadersRenders := frRange{4, 7}
		onlyShaderBRenders := frRange{7, 10}

		transition := TransitionCrossFade(
			TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: overlapPerc},
			term.Attributes{},
			shRedAAAs,
			shGreenBBBs,
		)

		// only 'A' before crossing area (between 0% and 30% of the animation)
		for fr := onlyShaderARenders.start; fr < onlyShaderARenders.end; fr++ {
			matrix := makeCellMatrix(60, 30)
			transition.Shade(fr, total, matrix)
			assert.True(t,
				assertMatrixAllCells(matrix, func(c term.Cell) bool {
					return c.Ch == 'A'
				}),
				"(frame %d) matrix doen't have char 'A' on all cells", fr,
			)
		}

		//'A's and 'B's are mixed during crossing area (between 30% and 70% of the animation)
		for fr := bothShadersRenders.start; fr < bothShadersRenders.end; fr++ {
			matrix := makeCellMatrix(60, 30)
			transition.Shade(fr, total, matrix)
			assert.True(t,
				assertMatrixSomeCells(matrix, func(c term.Cell) bool {
					return c.Ch == 'A'
				}) &&
					assertMatrixSomeCells(matrix, func(c term.Cell) bool {
						return c.Ch == 'B'
					}),
				"(frame %d) matrix does not have a mix of 'A' and 'B'", fr,
			)
		}

		// only 'B' after crossing area (between 70% and 100% of the animation)
		for fr := onlyShaderBRenders.start; fr < onlyShaderBRenders.end; fr++ {
			matrix := makeCellMatrix(60, 30)
			transition.Shade(fr, total, matrix)
			assert.True(t,
				assertMatrixAllCells(matrix, func(c term.Cell) bool {
					return c.Ch == 'B'
				}),
				"(frame %d) matrix doen't have char 'A' on all cells", fr,
			)
		}
	})

	t.Run("colors are interpolated only within cross fade area", func(t *testing.T) {
		shRedAAAs := newConstantShader('A', term.GetColor("darkred"), term.ColorRed)
		shGreenBBBs := newConstantShader('B', term.GetColor("darkgreen"), term.ColorGreen)
		transition := TransitionCrossFade(
			TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.4},
			term.Attributes{},
			shRedAAAs,
			shGreenBBBs,
		)

		frames := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

		lerpFg := func(factor float64) term.Color {
			return shaderutils.InterpolateColor(
				factor, term.GetColor("darkred"), term.GetColor("darkgreen"), 0,
			)
		}
		expectFgs := []term.Color{
			term.GetColor("darkred"),
			term.GetColor("darkred"),
			term.GetColor("darkred"),
			term.GetColor("darkred"),
			lerpFg(0.25),
			lerpFg(0.5),
			lerpFg(0.75),
			term.GetColor("darkgreen"),
			term.GetColor("darkgreen"),
			term.GetColor("darkgreen"),
		}

		lerpBg := func(factor float64) term.Color {
			return shaderutils.InterpolateColor(
				factor, term.ColorRed, term.ColorGreen, 0,
			)
		}
		expectBgs := []term.Color{
			term.ColorRed,
			term.ColorRed,
			term.ColorRed,
			term.ColorRed,
			lerpBg(0.25),
			lerpBg(0.5),
			lerpBg(0.75),
			term.ColorGreen,
			term.ColorGreen,
			term.ColorGreen,
		}

		require.True(t, len(frames) == len(expectBgs), // && len(expectBgs) == len(expectFgs),
			"invalid test, expected bgs and fgs must have the same dimension of the "+
				"amount of frames to test it for",
		)

		for i, frame := range frames {
			matrix := makeCellMatrix(6, 3)
			transition.Shade(frame, len(frames), matrix)
			assert.True(t,
				assertMatrixAllCells(matrix, func(c term.Cell) bool {
					return c.Fg.Hex() == expectFgs[i].Hex()
				}),
				"(frame %d) matrix doen't have fg %v on all cells", frame, expectFgs[i],
			)
			assert.True(t,
				assertMatrixAllCells(matrix, func(c term.Cell) bool {
					return c.Bg.Hex() == expectBgs[i].Hex()
				}),
				"(frame %d) matrix doen't have bg %v on all cells", frame, expectBgs[i],
			)
		}
	})

	// establish the convention that this crazy number means no frame has been
	// set because the int zero-value "0" is not suitable for us.
	const x = -770077007700
	const noFrame = x // visually helpful for marking unrendered frames in table tests

	defaultParams := DefaultTransitionCrossFadeParams()

	tsuite := []struct {
		name               string
		params             TransitionCrossFadeParams
		panicsConstructing bool
		panicsShading      bool
		total              int
		shader1Total       int
		shader2Total       int
		globalFrames       []int
		shader1Frames      []int
		shader2Frames      []int
	}{
		{
			name:          "frames are distributed evenly in halves by default and they overlap in quarters",
			params:        defaultParams,
			total:         20,
			shader1Total:  13,
			shader2Total:  13,
			globalFrames:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, x, x, x, x, x, x, x},
			shader2Frames: []int{x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
		},
		{
			name:          "overlap percentage very big",
			params:        TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.75},
			total:         20,
			shader1Total:  18,
			shader2Total:  18,
			globalFrames:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, x, x},
			shader2Frames: []int{x, x, x, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17},
		},
		{
			name:          "overlap percentage very small",
			params:        TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.1},
			total:         20,
			shader1Total:  11,
			shader2Total:  11,
			globalFrames:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, x, x, x, x, x, x, x, x, x},
			shader2Frames: []int{x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		},
		{
			name:               "OverlapPerc is out of range: 0.0 (start) doesn't panics; it behaves like TransitionCut",
			params:             TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.0},
			panicsConstructing: false,
			total:              20,
			shader1Total:       10,
			shader2Total:       10,
			globalFrames:       []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, x, x, x, x, x, x, x, x, x, x},
			shader2Frames:      []int{x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		},
		{
			name:               "OverlapPerc is out of range: < 0.0 (start) panics",
			params:             TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: -999.0},
			panicsConstructing: true,
		},
		{
			name:               "OverlapPerc is out of range: 1.0 (end) doesn't panic; it blends from first to last frame",
			params:             TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 1.0},
			panicsConstructing: false,
			total:              20,
			shader1Total:       20,
			shader2Total:       20,
			globalFrames:       []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader2Frames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
		},
		{
			name:               "OverlapPerc is out of range: > 1.0 (end) panics",
			params:             TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 999.0},
			panicsConstructing: true,
		},
		{
			name:          "shifting change point before halfway (< 0.5) gives more frames to second shader",
			params:        TransitionCrossFadeParams{ChangeAtPerc: 0.3, OverlapPerc: 0.1},
			total:         20,
			shader1Total:  7,
			shader2Total:  15,
			globalFrames:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames: []int{0, 1, 2, 3, 4, 5, 6, x, x, x, x, x, x, x, x, x, x, x, x, x},
			shader2Frames: []int{x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14},
		},
		{
			name:          "shifting change point past halfway (> 0.5) gives less frames to second shader",
			params:        TransitionCrossFadeParams{ChangeAtPerc: 0.7, OverlapPerc: 0.1},
			total:         20,
			shader1Total:  15,
			shader2Total:  7,
			globalFrames:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, x, x, x, x, x},
			shader2Frames: []int{x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6},
		},
		{
			name:          "in first frames only shader 1 is run",
			params:        defaultParams,
			total:         20,
			shader1Total:  13,
			shader2Total:  10,
			globalFrames:  []int{0, 1, 2},
			shader1Frames: []int{0, 1, 2},
			shader2Frames: []int{x, x, x},
		},
		{
			name:          "in last frames only shader 2 is run",
			params:        defaultParams,
			total:         20,
			shader1Total:  13,
			shader2Total:  13,
			globalFrames:  []int{17, 18, 19},
			shader1Frames: []int{x, x, x},
			shader2Frames: []int{10, 11, 12},
		},
		{
			name:          "change point renders frames from both shaders",
			params:        defaultParams,
			total:         20,
			shader1Total:  13,
			shader2Total:  13,
			globalFrames:  []int{6, 8, 12, 14},
			shader1Frames: []int{6, 8, 12, x},
			shader2Frames: []int{x, 1, 5, 7},
		},
		{
			name:               "ChangeAtPerc at smallest value 0.0 doesn't panic",
			params:             TransitionCrossFadeParams{ChangeAtPerc: 0.3, OverlapPerc: 0.0},
			panicsConstructing: false,
			panicsShading:      false,
			total:              20,
			shader1Total:       6,
			shader2Total:       14,
			globalFrames:       []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames:      []int{0, 1, 2, 3, 4, 5, x, x, x, x, x, x, x, x, x, x, x, x, x, x},
			shader2Frames:      []int{x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13},
		},
		{
			name:               "ChangeAtPerc is out of range: < 0.0 panics",
			params:             TransitionCrossFadeParams{ChangeAtPerc: -999.0},
			panicsConstructing: true,
		},
		{
			name:               "ChangeAtPerc at biggest value 1.0 doesn't panic",
			params:             TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 1.0},
			panicsConstructing: false,
			panicsShading:      false,
			total:              20,
			shader1Total:       20,
			shader2Total:       20,
			globalFrames:       []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader2Frames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
		},
		{
			name:               "ChangeAtPerc is out of range: > 1.0 panics",
			params:             TransitionCrossFadeParams{ChangeAtPerc: 999.0},
			panicsConstructing: true,
		},
		{
			name:          "non linear time jumping before, after, before, after change point doesn't panic",
			params:        defaultParams,
			total:         10,
			shader1Total:  6,
			shader2Total:  6,
			globalFrames:  []int{5, 1, 9, 1, 9},
			shader1Frames: []int{5, 1, x, 1, x},
			shader2Frames: []int{1, x, 5, x, 5},
		},
		{
			name:          "frames beyond total",
			params:        defaultParams,
			total:         10,
			shader1Total:  6,
			shader2Total:  6,
			globalFrames:  []int{11, 12, 13},
			shader1Frames: []int{x, x, x},
			shader2Frames: []int{7, 8, 9},
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			sh1 := new(mockShader)
			sh2 := new(mockShader)

			var transition Shader
			if tcase.panicsConstructing {
				assert.Panics(t, func() {
					transition = TransitionCrossFade(
						tcase.params, term.Attributes{},
						sh1, sh2,
					)
				})
			} else {
				transition = TransitionCrossFade(
					tcase.params, term.Attributes{},
					sh1, sh2,
				)
			}

			testTransitionFrames(
				t, tcase.panicsShading, tcase.total,
				transition, []*mockShader{sh1, sh2},
				tcase.globalFrames,
				[][]int{tcase.shader1Frames, tcase.shader2Frames},
				[]int{tcase.shader1Total, tcase.shader2Total},
			)
		})
	}
}

func TestTransitionCrossFadeShadesImagesWithTheShaderInFront(t *testing.T) {
	replacing := func(id term.ImageID) *imageShader {
		return &imageShader{images: func(images []term.Image) []term.Image {
			return append(images[:0], term.Image{ID: id})
		}}
	}
	transition := TransitionCrossFade(
		TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.4},
		term.Attributes{}, replacing(1), replacing(2),
	).(ImageShader)

	// While they fade into each other, they draw different pictures,
	// which are painted onto their cells rather than drawn.
	for frame, want := range []term.ImageID{1, 1, 1, 0, 0, 0, 0, 2, 2, 2} {
		got := transition.ShadeImages(frame, 10, makeCellMatrix(3, 2), []term.Image{{ID: 9}})
		if want == 0 {
			assert.Empty(t, got, "frame %d", frame)
			continue
		}
		assert.Equal(t, []term.Image{{ID: want}}, got, "frame %d", frame)
	}
}

func TestTransitionCrossFadeDrawsThePicturesBothShadersDraw(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, A: 255})
	picture := term.Image{Src: src, ID: term.NewImageID(), Width: 1, Height: 1}
	gray := term.NewRGBColor(40, 40, 40)
	transition := TransitionCrossFade(
		TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.4},
		term.Attributes{}, Nop(), Nop(),
	).(ImageShader)

	for frame := range 10 {
		cells := [][]term.Cell{{term.NewCell('x', 1, term.Attributes{Bg: gray})}}
		got := transition.ShadeImages(frame, 10, cells, []term.Image{picture})
		assert.Equal(t, []term.Image{picture}, got, "frame %d", frame)
		assert.Equal(t, gray, cells[0][0].Bg, "frame %d", frame)
	}
}

func TestTransitionCrossFadeFadesThePicturesOneShaderDrawsAlone(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, A: 255})
	picture := term.Image{Src: src, ID: term.NewImageID(), Width: 1, Height: 1}
	gray := term.NewRGBColor(40, 40, 40)
	for _, tt := range []struct {
		name             string
		shader1, shader2 Shader
		drawn            func(frame int) bool
	}{
		{"fading in", Nop(), halveBg{}, func(frame int) bool { return frame < 3 }},
		{"fading out", halveBg{}, Nop(), func(frame int) bool { return frame > 6 }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transition := TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.4},
				term.Attributes{}, tt.shader1, tt.shader2,
			).(ImageShader)
			for frame := range 10 {
				cells := [][]term.Cell{{term.NewCell('x', 1, term.Attributes{Bg: gray})}}
				got := transition.ShadeImages(frame, 10, cells, []term.Image{picture})
				if tt.drawn(frame) {
					assert.Equal(t, []term.Image{picture}, got, "frame %d", frame)
					assert.Equal(t, gray, cells[0][0].Bg, "frame %d", frame)
					continue
				}
				assert.Empty(t, got, "frame %d", frame)
				r, g, b := cells[0][0].Bg.RGB()
				assert.True(t, r >= 100 && r <= 200 && g >= 50 && g <= 100 && b == 0,
					"frame %d: the picture, halved or not, rather than %d, %d, %d", frame, r, g, b)
			}
		})
	}
}

func TestTransitionCrossFadeFadesThePicturesTheShadersDrawDifferently(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1, 1))
	src.SetRGBA(0, 0, color.RGBA{R: 200, G: 100, A: 255})
	picture := term.Image{Src: src, ID: term.NewImageID(), Width: 2, Height: 1}
	painted := term.NewRGBColor(200, 100, 0)
	gray := term.Attributes{Bg: term.NewRGBColor(40, 40, 40)}
	// halveBg{from: 1} leaves the picture showing over the first cell.
	for _, tt := range []struct {
		name             string
		shader1, shader2 Shader
	}{
		{"fading in", Nop(), halveBg{from: 1}},
		{"fading out", halveBg{from: 1}, Nop()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			transition := TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.4},
				term.Attributes{}, tt.shader1, tt.shader2,
			).(ImageShader)
			for frame := 3; frame < 7; frame++ {
				cells := [][]term.Cell{{term.NewCell('x', 1, gray), term.NewCell('x', 1, gray)}}
				got := transition.ShadeImages(frame, 10, cells, []term.Image{picture})
				assert.Empty(t, got, "frame %d: a picture drawn over the fade hides it", frame)
				assert.Equal(t, painted, cells[0][0].Bg, "frame %d", frame)
				r, g, b := cells[0][1].Bg.RGB()
				assert.True(t, r >= 100 && r <= 200 && g >= 50 && g <= 100 && b == 0,
					"frame %d: the picture, halved or not, rather than %d, %d, %d", frame, r, g, b)
			}
		})
	}
}

func TestStackingTransitionCrossFade(t *testing.T) {
	// establish the convention that this crazy number means no frame has been
	// set because the int zero-value "0" is not suitable for us.
	const x = -770077007700
	const noFrame = x // visually helpful for marking unrendered frames in table tests

	shA := new(mockShader)
	shB := new(mockShader)
	shC := new(mockShader)
	shD := new(mockShader)
	shE := new(mockShader)

	defaultParams := DefaultTransitionCrossFadeParams()

	tsuite := []struct {
		name              string
		panics            bool
		total             int
		transition        Shader
		transitionShaders []*mockShader
		globalFrames      []int
		shaderTotals      []int
		shaderFrames      [][]int
	}{
		{
			name:  "last frame of each shader to be within inner shader total (at most shaderTotal[i].total-1)",
			total: 20,
			transition: TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.4, OverlapPerc: 0.3},
				term.Attributes{},
				shA,
				shB,
			),
			transitionShaders: []*mockShader{shA, shB},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{11, 15},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, x, x, x, x, x, x, x, x, x}, // shA
				//                                 ^ tests this to not get called with 11!
				{x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}, // shA
			},
		},
		{
			name:  "frames partitioned in halves recursively T(A, T(B, C))",
			total: 20,
			transition: TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.3},
				term.Attributes{},
				shA,
				TransitionCrossFade(
					TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.3},
					term.Attributes{},
					shB,
					shC,
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{13, 8, 8},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x},    // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7},    // shC
			},
		},
		{
			name:  "frames partitioned in halves respecting transition pairs T(T(A, B), C)",
			total: 20,
			transition: TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.3},
				term.Attributes{},
				TransitionCrossFade(
					TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.3},
					term.Attributes{},
					shA,
					shB,
				),
				shC,
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{8, 8, 13},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x, x, x, x, x, x},    // shA
				{x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x},    // shB
				{x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, // shC
			},
		},
		{
			name:  "frames partitioned unevenly outer ChangeAtPercs=0.8 T(T(A, B), C)",
			total: 21,
			transition: TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.8, OverlapPerc: 0.25},
				term.Attributes{},
				TransitionCrossFade(defaultParams, term.Attributes{},
					shA,
					shB,
				),
				shC,
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21},
			shaderTotals:      []int{12, 12, 7},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7},   // shC
			},
		},
		{
			name:  "frames partitioned unevenly outer ChangeAtPercs=0.8 T(A, T(B, C))",
			total: 19,
			transition: TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.8, OverlapPerc: 0.25},
				term.Attributes{},
				shA,
				TransitionCrossFade(defaultParams, term.Attributes{},
					shB,
					shC,
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18},
			shaderTotals:      []int{18, 4, 4},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, x}, // shA
				{x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, x, x},         // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 1, 2, 3},         // shC
			},
		},
		{
			name:  "frames partitioned unevenly inner ChangeAtPercs=0.8 T(T(A, B), C)",
			total: 20,
			transition: TransitionCrossFade(
				defaultParams,
				term.Attributes{},
				TransitionCrossFade(
					TransitionCrossFadeParams{ChangeAtPerc: 0.8, OverlapPerc: 0.25},
					term.Attributes{},
					shA,
					shB,
				),
				shC,
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{12, 4, 13},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, x, x, x, x, x, x, x, x},  // shA
				{x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, x, x, x, x, x, x, x},    // shB
				{x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}, // shC
			},
		},
		{
			name:  "frames partitioned unevenly outer ChangeAtPercs=0.7 inner ChangeAtPercs=0.1 T(A, T(B, C))",
			total: 20,
			transition: TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.7, OverlapPerc: 0.25},
				term.Attributes{},
				shA,
				TransitionCrossFade(TransitionCrossFadeParams{
					ChangeAtPerc: 0.1, OverlapPerc: 0.25}, term.Attributes{},
					shB, shC,
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{17, 2, 9},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, x, x, x, x, 1, x, x, x, x, x, x, x},        // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, 8},        // shC
			},
		},
		{
			name:  "frames partitioned evenly in transition duplets OverlapPerc=0.25 T(T(A, B) T(C, D))",
			total: 20,
			transition: TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.25},
				term.Attributes{},
				TransitionCrossFade(
					TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.25},
					term.Attributes{},
					shA,
					shB,
				),
				TransitionCrossFade(
					TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.25},
					term.Attributes{},
					shC,
					shD,
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC, shD},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{8, 8, 8, 8},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x}, // shC
				{x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7}, // shD
			},
		},
		{
			name:  "frames partitioned evenly in transition duplets OverlapPerc=0.75 T(T(A, B) T(C, D))",
			total: 20,
			transition: TransitionCrossFade(
				TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.75},
				term.Attributes{},
				TransitionCrossFade(
					TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.75},
					term.Attributes{},
					shA,
					shB,
				),
				TransitionCrossFade(
					TransitionCrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.75},
					term.Attributes{},
					shC,
					shD,
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC, shD},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{16, 16, 16, 16},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, x, x, x, x}, // shA
				{x, x, x, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, x, x}, // shB
				{x, x, x, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, x, x}, // shC
				{x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}, // shD
			},
		},
		{
			name:  "frames partitioned evenly T(A, T(B, T(C, D))",
			total: 16,
			transition: TransitionCrossFade(defaultParams, term.Attributes{},
				shA,
				TransitionCrossFade(defaultParams, term.Attributes{},
					shB,
					TransitionCrossFade(defaultParams, term.Attributes{},
						shC,
						shD,
					),
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC, shD},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			shaderTotals:      []int{10, 6, 4, 4},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, x, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, x, x}, // shC
				{x, x, x, x, x, x, x, x, x, x, x, x, x, 1, 2, 3}, // shD
			},
		},
		{
			name:  "frames partitioned mix duplets T(A, T(T(B, C), T(D, E)))",
			total: 30,
			transition: TransitionCrossFade(defaultParams, term.Attributes{},
				shA,
				TransitionCrossFade(defaultParams, term.Attributes{},
					TransitionCrossFade(defaultParams, term.Attributes{},
						shB,
						shC,
					),
					TransitionCrossFade(defaultParams, term.Attributes{},
						shD,
						shE,
					),
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC, shD, shE},
			globalFrames: []int{
				0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29,
			},
			shaderTotals: []int{19, 8, 8, 8, 8},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, x, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x, x, x, x, x},          // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x},          // shC
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7, x, x, x, x},          // shD
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 1, 2, 3, 4, 5, 6, 7},          // shE
			},
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			testTransitionFrames(
				t, tcase.panics, tcase.total,
				tcase.transition, tcase.transitionShaders,
				tcase.globalFrames, tcase.shaderFrames, tcase.shaderTotals,
			)
		})

	}
}
