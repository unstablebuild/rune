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
	"math"
	"math/rand"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// TransitionCrossFade plays the first shader and before ending fades it
// out while fading in the second shader simultaneously for some frames.
//
// Colors are linearly interpolated and characters are randomly changed
// based on time-independent position-based random noise.
//
//	    Transition Diagram
//	|     S·····C·····E     | S: transStartFrame
//	|     [·c·r·o·s·s·] █4█ | C: changePoint
//	|     [·····|·····] ▀▀▀ | E: transEndFrame
//	|     [·····|·█3█·]     |
//	|     [·····|·▀▀▀·]     |
//	|     [·█2█·|·····]     |
//	|     [·▀▀▀·|·····]     |
//	| █1█ [·····|·····]     |
//	| ▀▀▀ [·····|·····]     |
//	|--shader1--|--shader2--|time->
//	|     [·c·r·o·s·s·]
//
// Images are shaded by the first shader until the change point and by
// the second from then on, as [ShadeImages] does. While they fade into
// each other, pictures they do not both draw fade with the cells.
func TransitionCrossFade(
	params TransitionCrossFadeParams,
	defaultAttr term.Attributes,
	shader1, shader2 Shader,
) Shader {
	if params.ChangeAtPerc < 0.0 || params.ChangeAtPerc > 1.0 {
		panic("ChangeAtPerc must be within the closed interval [0,1]")
	}

	if params.OverlapPerc < 0.0 || params.OverlapPerc > 1.0 {
		panic("OverlapPerc must be within the closed interval [0,1]")
	}

	return &transCrossFade{
		TransitionCrossFadeParams: params,
		shader1:                   shader1,
		shader2:                   shader2,
		defaultAttr:               defaultAttr,
	}
}

// DefaultTransitionCrossFadeParams return a set of sane TransitionCrossFadeParams.
func DefaultTransitionCrossFadeParams() TransitionCrossFadeParams {
	return TransitionCrossFadeParams{
		ChangeAtPerc: 0.5,
		OverlapPerc:  0.25,
	}
}

// TransitionCrossFadeParams defines the parameters used by the TransitionCrossFade shader.
type TransitionCrossFadeParams struct {
	// Point within closed interval [0.0,1.0] at which the shader change happens.
	ChangeAtPerc float64
	// Point within closed interval [0.0,1.0] at which the shaders overlap at
	// cross fading area.
	OverlapPerc float64
}

type transCrossFade struct {
	TransitionCrossFadeParams
	shader1     Shader
	shader2     Shader
	buf         [][]term.Cell
	activations [][]bool
	defaultAttr term.Attributes
}

func (t *transCrossFade) Shade(frame, total int, in [][]term.Cell) {
	t.ShadeImages(frame, total, in, nil)
}

func (t *transCrossFade) ShadeImages(
	frame, total int, in [][]term.Cell, images []term.Image,
) []term.Image {
	changePointFrame := t.ChangeAtPerc * float64(total)
	transDurationInFrames := float64(total) * t.OverlapPerc

	transStartFrame := changePointFrame - (transDurationInFrames / 2.0)
	transEndFrame := transStartFrame + transDurationInFrames

	transHalfDur := transDurationInFrames / 2.0

	totalShader1 := math.Round(changePointFrame + transHalfDur)
	frameShader1 := math.Round(float64(frame))
	shader1FrameOverflow := frameShader1 >= totalShader1

	// TransitionCrossFade docstring diagram case 1:
	// no need for transition, it's 100% shader1
	if float64(frame) < transStartFrame {
		return ShadeImages(t.shader1, int(frameShader1), int(totalShader1), in, images)
	}

	totalShader2 := math.Round(float64(total) - changePointFrame + transHalfDur)
	frameShader2 := math.Round((float64(frame) - (changePointFrame - transHalfDur)))

	// TransitionCrossFade docstring diagram case 4:
	// no need for transition, it's 100% shader2
	if float64(frame) > transEndFrame || shader1FrameOverflow {
		return ShadeImages(t.shader2, int(frameShader2), int(totalShader2), in, images)
	}

	// TransitionCrossFade docstring diagram case 2 or case 3: we are in
	// transition zone from here on, depending if we are before or after
	// changePointFrame we will write the first or second shader to buf

	t.copyCells(in)
	// The shader writing to buf shades its own copy, whose result is
	// dropped along with its cells.
	behind := slices.Clone(images)

	midPointFramePassed := float64(frame) >= changePointFrame
	var back []term.Image
	if midPointFramePassed {
		// TransitionCrossFade docstring diagram case 3:
		images = ShadeImages(t.shader2, int(frameShader2), int(totalShader2), in, images)
		back = ShadeImages(t.shader1, int(frameShader1), int(totalShader1), t.buf, behind)
	} else {
		// TransitionCrossFade docstring diagram case 2:
		images = ShadeImages(t.shader1, int(frameShader1), int(totalShader1), in, images)
		back = ShadeImages(t.shader2, int(frameShader2), int(totalShader2), t.buf, behind)
	}
	// Drawn over the fade, pictures would hide it.
	if !slices.Equal(images, back) {
		paintPictures(in, images)
		paintPictures(t.buf, back)
		images = nil
	}

	t.interpolateBuffer(
		frame, midPointFramePassed,
		transStartFrame, transEndFrame, transDurationInFrames,
		in,
	)
	return images
}

func (t *transCrossFade) interpolateBuffer(
	frame int,
	midPointPassed bool,
	transStartFrame float64,
	transEndFrame float64,
	transDurationInFrames float64,
	final [][]term.Cell,
) {
	var frameWithinTransition float64
	if !midPointPassed {
		frameWithinTransition = float64(frame) - transStartFrame
	} else {
		frameWithinTransition = (transEndFrame - float64(frame))
	}

	// -- Color Transition

	prog0to05 := frameWithinTransition / transDurationInFrames // range: 0.0..0.5

	for y, row := range final {
		for x := range row {
			// Avoid blending cells where both shaders produced the same
			// foreground/background. Calling InterpolateColor on identical
			// values is a no-op for the math but converts named/palette
			// colors into literal TrueColor RGB which terminals may render
			// with a slightly different hue, leaving a visible "wake" on
			// untouched cells across the transition window.
			if final[y][x].Fg != t.buf[y][x].Fg {
				final[y][x].Fg = shaderutils.InterpolateColor(
					prog0to05, final[y][x].Fg, t.buf[y][x].Fg, t.defaultAttr.Fg,
				)
			}
			if final[y][x].Bg != t.buf[y][x].Bg {
				final[y][x].Bg = shaderutils.InterpolateColor(
					prog0to05, final[y][x].Bg, t.buf[y][x].Bg, t.defaultAttr.Bg,
				)
			}
			t.activations[y][x] = false
		}
	}

	if len(final) == 0 || len(final[0]) == 0 {
		return
	}

	// -- Character Transition

	// During transition at max reveal that amount of characters, which is
	// adaptive to window resolution and amount of transitioning frame. If it
	// doesn't generalize well, then rework the arithmetics!
	maxCharRevealPerFrame := int(7.0 * float64(len(final)*len(final[0])) / transDurationInFrames)

	// NOTE: performance sacrifice here since we can't garantee we will receive
	// the frames linearly we run the simulation up to the frame we want so
	// this shader can be sampled at any point in time without having to run
	// the previous frames
	rng := rand.New(rand.NewSource(0))
	for f := 0; f < int(math.Round((float64(frame) - transStartFrame))); f++ {
		for range maxCharRevealPerFrame {
			rowIdx := rng.Intn(len(t.activations))
			row := t.activations[rowIdx]
			colIdx := rng.Intn(len(row))
			t.activations[rowIdx][colIdx] = true
		}
	}

	for y, row := range final {
		for x := range row {
			if !midPointPassed {
				if t.activations[y][x] {
					final[y][x].Ch = t.buf[y][x].Ch
				}
			} else {
				if !t.activations[y][x] {
					final[y][x].Ch = t.buf[y][x].Ch
				}
			}
		}
	}

}

func (t *transCrossFade) copyCells(in [][]term.Cell) {
	t.ensureBufCap(in)
	for i, row := range in {
		copy(t.buf[i], row)
	}
}

func (t *transCrossFade) ensureBufCap(in [][]term.Cell) {
	if len(t.buf) < len(in) {
		t.buf = append(t.buf, make([][]term.Cell, len(in)-len(t.buf))...)
		t.activations = append(t.activations, make([][]bool, len(in)-len(t.activations))...)
	}
	for i, row := range in {
		if len(t.buf[i]) < len(row) {
			t.buf[i] = append(t.buf[i], make([]term.Cell, len(row)-len(t.buf[i]))...)
		}
		if len(t.activations[i]) < len(row) {
			t.activations[i] = append(t.activations[i], make([]bool, len(row)-len(t.activations[i]))...)
		}
	}
}
