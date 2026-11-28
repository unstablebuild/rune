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

package kageshader

import (
	_ "embed"

	"unstable.build/rune/internal/component/gshader"
)

//go:embed inferno.kage
var infernoKage []byte

var infernoProgram = program("inferno.kage", infernoKage, noiseInc)

// InfernoParams are the parameters of Inferno.
type InfernoParams = FireParams

// DefaultInfernoParams returns InfernoParams for a red fire, half way to
// the background, whose noise changes four times a run.
func DefaultInfernoParams() InfernoParams {
	return InfernoParams{Speed: 4, Strength: 0.5}
}

// Inferno returns a generator of a fire, remixed from codevinsky's Fire
// (https://www.shadertoy.com/view/XsXXRN) as glslshader.Inferno is. A run
// is one cycle of the fire, which ends where it starts.
func Inferno(params InfernoParams) gshader.Shader {
	return gshader.FromProgram(infernoProgram, params.uniforms())
}
