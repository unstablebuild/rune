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

//go:embed plasma.kage
var plasmaKage []byte

var plasmaProgram = program("plasma.kage", plasmaKage)

// PlasmaParams are the parameters of Plasma.
type PlasmaParams struct {
	// Scale is how many waves span the height.
	Scale float64
	// Speed is how many times the waves turn over a run, rounded to a
	// whole number; 0 keeps them still.
	Speed float64
	// Hue turns the colours, in turns.
	Hue float64
	// Strength is how far the colours stand out from the background, from
	// 0 to 1.
	Strength float64
}

// DefaultPlasmaParams returns PlasmaParams for faint waves that turn once
// a run.
func DefaultPlasmaParams() PlasmaParams {
	return PlasmaParams{Scale: 4, Speed: 1, Strength: 0.3}
}

// Plasma returns a generator of waves of colour that turn over a run and
// end where they start.
func Plasma(params PlasmaParams) gshader.Shader {
	return gshader.FromProgram(plasmaProgram, params.uniforms())
}

func (p PlasmaParams) uniforms() gshader.Uniforms {
	return gshader.Uniforms{
		"Scale":    float32(p.Scale),
		"Speed":    float32(p.Speed),
		"Hue":      float32(p.Hue),
		"Strength": float32(p.Strength),
	}
}
