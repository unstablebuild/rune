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

package gshader

import (
	_ "embed"
	"math"
)

//go:embed blend.kage
var blendKage []byte

// blendProgram mixes imageSrc0 with imageSrc1, which a Weight of 1 shows
// alone.
var blendProgram = MustProgram("blend.kage", blendKage)

// CrossFadeParams are the parameters of CrossFade.
type CrossFadeParams struct {
	// ChangeAtPerc is where in the run, within [0, 1], the fade is half
	// way through.
	ChangeAtPerc float64
	// OverlapPerc is how much of the run, within [0, 1], the fade lasts.
	OverlapPerc float64
}

// DefaultCrossFadeParams returns CrossFadeParams that fade over a quarter
// of the run, around its middle.
func DefaultCrossFadeParams() CrossFadeParams {
	return CrossFadeParams{ChangeAtPerc: 0.5, OverlapPerc: 0.25}
}

// CrossFade returns a Shader that plays first and then second, fading the
// pixels of one into the other's while both play. Each plays over its part
// of the run, the fade included. It panics when a parameter is outside
// [0, 1].
func CrossFade(params CrossFadeParams, first, second Shader) Shader {
	if params.ChangeAtPerc < 0 || params.ChangeAtPerc > 1 {
		panic("gshader: ChangeAtPerc must be within [0, 1]")
	}
	if params.OverlapPerc < 0 || params.OverlapPerc > 1 {
		panic("gshader: OverlapPerc must be within [0, 1]")
	}
	return crossFadeShader{params: params, first: first, second: second}
}

type crossFadeShader struct {
	params        CrossFadeParams
	first, second Shader
}

// Shade splits the run as shader.TransitionCrossFade does, so the two
// change at the same frames.
func (s crossFadeShader) Shade(frame, total int, src Node) Node {
	change := s.params.ChangeAtPerc * float64(total)
	fade := s.params.OverlapPerc * float64(total)
	start, end := change-fade/2, change+fade/2
	at := float64(frame)

	firstTotal := int(math.Round(end))
	if at < start {
		return s.first.Shade(frame, firstTotal, src)
	}
	secondTotal := int(math.Round(float64(total) - start))
	secondFrame := int(math.Round(at - start))
	if at > end || frame >= firstTotal {
		return s.second.Shade(secondFrame, secondTotal, src)
	}
	return Run(blendProgram, Uniforms{"Weight": float32((at - start) / fade)},
		s.first.Shade(frame, firstTotal, src),
		s.second.Shade(secondFrame, secondTotal, src))
}
