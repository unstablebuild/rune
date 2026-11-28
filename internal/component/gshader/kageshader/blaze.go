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

//go:embed blaze.kage
var blazeKage []byte

var blazeProgram = program("blaze.kage", blazeKage, noiseInc)

// BlazeParams are the parameters of Blaze.
type BlazeParams = FireParams

// DefaultBlazeParams returns BlazeParams for a red fire, half way to the
// background, whose noise changes three times a run.
func DefaultBlazeParams() BlazeParams {
	return BlazeParams{Speed: 3, Strength: 0.5}
}

// Blaze returns a generator of a swaying fire, remixed from
// CaliCoastReplay's 301's Fire Shader - Remix 2
// (https://www.shadertoy.com/view/MtcGD7) as glslshader.Blaze is. A run is
// one cycle of the fire, which ends where it starts.
func Blaze(params BlazeParams) gshader.Shader {
	return gshader.FromProgram(blazeProgram, params.uniforms())
}
