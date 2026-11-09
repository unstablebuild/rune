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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestTransitionCut(t *testing.T) {
	t.Run("cells content from shader 1 before change point, shader 2 thereafter",
		func(t *testing.T) {
			shRedAAAs := newConstantShader('A', term.GetColor("darkred"), term.ColorRed)
			shGreenBBBs := newConstantShader('B', term.GetColor("darkgreen"), term.ColorGreen)
			transition := TransitionCut(TransitionCutParams{ChangeAtPerc: 0.5},
				shRedAAAs,
				shGreenBBBs,
			)

			frames := []int{0, 1, 2, 3, 4, 5, 6, 7}
			expectChars := []rune{'A', 'A', 'A', 'A', 'B', 'B', 'B', 'B'}
			expectFgs := []term.Color{
				term.GetColor("darkred"), term.GetColor("darkred"), term.GetColor("darkred"), term.GetColor("darkred"),
				term.GetColor("darkgreen"), term.GetColor("darkgreen"), term.GetColor("darkgreen"), term.GetColor("darkgreen"),
			}
			expectBgs := []term.Color{
				term.ColorRed, term.ColorRed, term.ColorRed, term.ColorRed,
				term.ColorGreen, term.ColorGreen, term.ColorGreen, term.ColorGreen,
			}

			require.True(t, len(frames) == len(expectChars) &&
				len(expectChars) == len(expectFgs) &&
				len(expectFgs) == len(expectBgs),
				"invalid test, expected chars, fgs and bgs must have the same dimension of the "+
					"amount of frames to test it for",
			)

			for i, frame := range frames {
				matrix := makeCellMatrix(6, 3)
				transition.Shade(frame, len(frames), matrix)
				fmt.Printf("%+v", matrix[0][0])
				assert.True(t,
					assertMatrixAllCells(matrix, func(c term.Cell) bool { return c.Ch == expectChars[i] }),
					"(frame %d) matrix doen't have char %c on all cells", frame, expectChars[i],
				)
				assert.True(t,
					assertMatrixAllCells(matrix, func(c term.Cell) bool { return c.Fg == expectFgs[i] }),
					"(frame %d) matrix doen't have fg %v on all cells", frame, expectFgs[i],
				)
				assert.True(t,
					assertMatrixAllCells(matrix, func(c term.Cell) bool { return c.Bg == expectBgs[i] }),
					"(frame %d) matrix doen't have bg %v on all cells", frame, expectBgs[i],
				)
			}
		})

	// establish the convention that this crazy number means no frame has been
	// set because the int zero-value "0" is not suitable for us.
	const x = -770077007700
	const noFrame = x // visually helpful for marking unrendered frames in table tests

	defaultParams := DefaultTransitionCutParams()

	tsuite := []struct {
		name               string
		params             TransitionCutParams
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
			name:          "frames are distributed evenly in halves by default",
			params:        defaultParams,
			total:         20,
			shader1Total:  10,
			shader2Total:  10,
			globalFrames:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, x, x, x, x, x, x, x, x, x, x},
			shader2Frames: []int{x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
		},
		{
			name:          "shifting change point before halfway (< 0.5) gives more frames to second shader",
			params:        TransitionCutParams{ChangeAtPerc: 0.3},
			total:         20,
			shader1Total:  6,
			shader2Total:  14,
			globalFrames:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames: []int{0, 1, 2, 3, 4, 5, x, x, x, x, x, x, x, x, x, x, x, x, x, x},
			shader2Frames: []int{x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13},
		},
		{
			name:          "shifting change point past halfway (> 0.5) gives less frames to second shader",
			params:        TransitionCutParams{ChangeAtPerc: 0.8},
			total:         20,
			shader1Total:  16,
			shader2Total:  4,
			globalFrames:  []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shader1Frames: []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, x, x, x, x},
			shader2Frames: []int{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3},
		},
		{
			name:          "in first frames only shader 1 is run",
			params:        defaultParams,
			total:         20,
			shader1Total:  10,
			shader2Total:  10,
			globalFrames:  []int{0, 1, 2},
			shader1Frames: []int{0, 1, 2},
			shader2Frames: []int{x, x, x},
		},
		{
			name:          "in last frames only shader 2 is run",
			params:        defaultParams,
			total:         20,
			shader1Total:  10,
			shader2Total:  10,
			globalFrames:  []int{17, 18, 19},
			shader1Frames: []int{x, x, x},
			shader2Frames: []int{7, 8, 9},
		},
		{
			name:          "change point marks the point after which shader 1 stops rendering and shader 2 starts",
			params:        defaultParams,
			total:         20,
			shader1Total:  10,
			shader2Total:  10,
			globalFrames:  []int{6, 8, 12, 14},
			shader1Frames: []int{6, 8, x, x},
			shader2Frames: []int{x, x, 2, 4},
		},
		{
			name:          "change point mark on even total",
			params:        defaultParams,
			total:         10,
			shader1Total:  5,
			shader2Total:  5,
			globalFrames:  []int{4, 5},
			shader1Frames: []int{4, x},
			shader2Frames: []int{x, 0},
		},
		{
			name:          "change point mark on uneven total is given to first shader",
			params:        defaultParams,
			total:         11,
			shader1Total:  6,
			shader2Total:  5,
			globalFrames:  []int{5, 6},
			shader1Frames: []int{5, x},
			shader2Frames: []int{x, 0},
		},
		{
			name:               "ChangeAtPerc is out of range: 0.0 (start) doesn't panic; only plays shader 2",
			params:             TransitionCutParams{ChangeAtPerc: 0.0},
			panicsConstructing: false,
			total:              3,
			shader1Total:       0,
			shader2Total:       3,
			globalFrames:       []int{0, 1, 2},
			shader1Frames:      []int{x, x, x},
			shader2Frames:      []int{0, 1, 2},
		},
		{
			name:               "ChangeAtPerc is out of range: < 0.0 (start) panics",
			params:             TransitionCutParams{ChangeAtPerc: -999.0},
			panicsConstructing: true,
		},
		{
			name:               "ChangeAtPerc is out of range: 1.0 (end) doesn't panic; only plays shader 1",
			params:             TransitionCutParams{ChangeAtPerc: 1.0},
			panicsConstructing: false,
			total:              3,
			shader1Total:       3,
			shader2Total:       0,
			globalFrames:       []int{0, 1, 2},
			shader1Frames:      []int{0, 1, 2},
			shader2Frames:      []int{x, x, x},
		},
		{
			name:               "ChangeAtPerc is out of range: > 1.0 (end) panics",
			params:             TransitionCutParams{ChangeAtPerc: 999.0},
			panicsConstructing: true,
		},
		{
			name:          "non linear time jumping before, after, before, after change point doesn't panic",
			params:        defaultParams,
			total:         10,
			shader1Total:  5,
			shader2Total:  5,
			globalFrames:  []int{5, 4, 6, 3, 7},
			shader1Frames: []int{x, 4, x, 3, x},
			shader2Frames: []int{0, x, 1, x, 2},
		},
		{
			name:          "frames beyond total",
			params:        defaultParams,
			total:         10,
			shader1Total:  5,
			shader2Total:  5,
			globalFrames:  []int{11, 12, 13},
			shader1Frames: []int{x, x, x},
			shader2Frames: []int{6, 7, 8},
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			sh1 := new(mockShader)
			sh2 := new(mockShader)

			var transition Shader
			if tcase.panicsConstructing {
				assert.Panics(t, func() {
					transition = TransitionCut(tcase.params, sh1, sh2)
				})
			} else {
				transition = TransitionCut(tcase.params, sh1, sh2)
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

func TestStackingTransitionCut(t *testing.T) {
	// establish the convention that this crazy number means no frame has been
	// set because the int zero-value "0" is not suitable for us.
	const x = -770077007700
	const noFrame = x // visually helpful for marking unrendered frames in table tests

	defaultParams := DefaultTransitionCutParams()

	shA := new(mockShader)
	shB := new(mockShader)
	shC := new(mockShader)
	shD := new(mockShader)
	shE := new(mockShader)

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
			name:              "frames partitioned in halves recursively T(A, T(B, C))",
			total:             20,
			transition:        TransitionCut(defaultParams, shA, TransitionCut(defaultParams, shB, shC)),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{10, 5, 5},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, x, x, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4}, // shC
			},
		},
		{
			name:              "frames partitioned in halves respecting transition pairs T(T(A, B), C)",
			total:             20,
			transition:        TransitionCut(defaultParams, TransitionCut(defaultParams, shA, shB), shC),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{5, 5, 10},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, 0, 1, 2, 3, 4, x, x, x, x, x, x, x, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, // shC
			},
		},
		{
			name:  "frames partitioned unevenly outer ChangeAtPercs=0.8 T(T(A, B), C)",
			total: 20,
			transition: TransitionCut(
				TransitionCutParams{ChangeAtPerc: 0.8},
				TransitionCut(defaultParams, shA, shB),
				shC,
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{8, 8, 4},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3}, // shC
			},
		},
		{
			name:  "frames partitioned unevenly outer ChangeAtPercs=0.8 T(A, T(B, C))",
			total: 20,
			transition: TransitionCut(
				TransitionCutParams{ChangeAtPerc: 0.8},
				shA,
				TransitionCut(defaultParams, shB, shC),
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{16, 2, 2},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, x, x},       // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1},       // shC
			},
		},
		{
			name:  "frames partitioned unevenly inner ChangeAtPercs=0.8 T(T(A, B), C)",
			total: 20,
			transition: TransitionCut(
				defaultParams,
				TransitionCut(TransitionCutParams{ChangeAtPerc: 0.8}, shA, shB),
				shC,
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{8, 2, 10},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, 0, 1, x, x, x, x, x, x, x, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, // shC
			},
		},
		{
			name:  "frames partitioned unevenly outer ChangeAtPercs=0.7 inner ChangeAtPercs=0.1 T(A, T(B, C))",
			total: 20,
			transition: TransitionCut(TransitionCutParams{ChangeAtPerc: 0.7},
				shA,
				TransitionCut(TransitionCutParams{ChangeAtPerc: 0.1},
					shB,
					shC,
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{14, 1, 5},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, x, x, x, x, x},     // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4},     // shC
			},
		},
		{
			name:  "frames partitioned evenly in transition duplets T(T(A, B) T(C, D))",
			total: 20,
			transition: TransitionCut(defaultParams,
				TransitionCut(defaultParams, shA, shB),
				TransitionCut(defaultParams, shC, shD),
			),
			transitionShaders: []*mockShader{shA, shB, shC, shD},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19},
			shaderTotals:      []int{5, 5, 5, 5},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, 0, 1, 2, 3, 4, x, x, x, x, x, x, x, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4, x, x, x, x, x}, // shC
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, 4}, // shD
			},
		},
		{
			name:  "frames partitioned evenly T(A, T(B, T(C, D))",
			total: 16,
			transition: TransitionCut(defaultParams,
				shA,
				TransitionCut(defaultParams,
					shB,
					TransitionCut(defaultParams,
						shC,
						shD,
					),
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC, shD},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
			shaderTotals:      []int{8, 4, 2, 2},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, 0, 1, 2, 3, x, x, x, x}, // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, x, x}, // shC
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1}, // shD
			},
		},
		{
			name:  "frames partitioned mix duplets T(A, T(T(B, C), T(D, E)))",
			total: 30,
			transition: TransitionCut(defaultParams,
				shA,
				TransitionCut(defaultParams,
					TransitionCut(defaultParams,
						shB,
						shC,
					),
					TransitionCut(defaultParams,
						shD,
						shE,
					),
				),
			),
			transitionShaders: []*mockShader{shA, shB, shC, shD, shE},
			globalFrames:      []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29},
			shaderTotals:      []int{15, 4, 4, 4, 3},
			shaderFrames: [][]int{
				{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x}, // shA
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, x, x, x, x, x, x, x, x, x, x, x},      // shB
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, x, x, x, x, x, x, x},      // shC
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2, 3, x, x, x},      // shD
				{x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, x, 0, 1, 2},      // shE
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

func TestTransitionCutShadesImagesWithTheShaderRunning(t *testing.T) {
	hide := &imageShader{images: func([]term.Image) []term.Image { return nil }}
	keep := &imageShader{images: keepImages}
	transition := TransitionCut(DefaultTransitionCutParams(), hide, keep).(ImageShader)
	images := []term.Image{{ID: 1, Width: 1, Height: 1}}

	assert.Empty(t, transition.ShadeImages(4, 10, makeCellMatrix(2, 2), images))
	assert.Equal(t, []int{4}, hide.frames)
	assert.Equal(t, images, transition.ShadeImages(5, 10, makeCellMatrix(2, 2), images))
	assert.Equal(t, []int{0}, keep.frames)
}
