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

	"github.com/unstablebuild/rune-go-sdk/term"
)

// TransitionCut jumps from one shader to the other without an interpolation.
// Images are shaded by whichever is running, as [ShadeImages] does.
func TransitionCut(params TransitionCutParams, shader1, shader2 Shader) Shader {
	if params.ChangeAtPerc < 0.0 || params.ChangeAtPerc > 1.0 {
		panic("ChangeAtPerc must be within the closed interval [0,1]")
	}

	return &transitionCut{
		TransitionCutParams: params,
		shader1:             shader1,
		shader2:             shader2,
	}
}

// DefaultTransitionCutParams return a set of sane TransitionCutParams.
func DefaultTransitionCutParams() TransitionCutParams {
	return TransitionCutParams{
		ChangeAtPerc: 0.5,
	}
}

// TransitionCutParams defines the parameters used by the TransitionCut shader.
type TransitionCutParams struct {
	// Point within closed interval [0.0,1.0] at which the shader change happens.
	ChangeAtPerc float64
}

type transitionCut struct {
	TransitionCutParams
	shader1 Shader
	shader2 Shader
}

func (t *transitionCut) Shade(frame, total int, in [][]term.Cell) {
	t.ShadeImages(frame, total, in, nil)
}

func (t *transitionCut) ShadeImages(
	frame, total int, in [][]term.Cell, images []term.Image,
) []term.Image {
	shader1Total := int(math.Round(float64(total) * (t.ChangeAtPerc)))
	shader2Total := total - shader1Total
	if frame < shader1Total {
		return ShadeImages(t.shader1, frame, shader1Total, in, images)
	}
	return ShadeImages(t.shader2, frame-shader1Total, shader2Total, in, images)
}
