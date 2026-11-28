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

import "math"

// CutParams are the parameters of Cut.
type CutParams struct {
	// ChangeAtPerc is where in the run, within [0, 1], the second shader
	// takes over.
	ChangeAtPerc float64
}

// DefaultCutParams returns CutParams that change half way through.
func DefaultCutParams() CutParams {
	return CutParams{ChangeAtPerc: 0.5}
}

// Cut returns a Shader that plays first and then second, each over its own
// part of the run, with nothing in between. It panics when
// params.ChangeAtPerc is outside [0, 1].
func Cut(params CutParams, first, second Shader) Shader {
	if params.ChangeAtPerc < 0 || params.ChangeAtPerc > 1 {
		panic("gshader: ChangeAtPerc must be within [0, 1]")
	}
	return cutShader{params: params, first: first, second: second}
}

type cutShader struct {
	params        CutParams
	first, second Shader
}

func (s cutShader) Shade(frame, total int, src Node) Node {
	firstTotal := int(math.Round(float64(total) * s.params.ChangeAtPerc))
	if frame < firstTotal {
		return s.first.Shade(frame, firstTotal, src)
	}
	return s.second.Shade(frame-firstTotal, total-firstTotal, src)
}
