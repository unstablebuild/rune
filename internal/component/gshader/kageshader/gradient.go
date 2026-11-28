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

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/gshader"
)

//go:embed gradient.kage
var gradientKage []byte

var gradientProgram = program("gradient.kage", gradientKage)

// GradientParams are the parameters of Gradient.
type GradientParams struct {
	// From and To are the colours at either end.
	From, To term.Color
	// Angle is the direction it runs in, in degrees clockwise from left to
	// right.
	Angle float64
}

// DefaultGradientParams returns GradientParams that run from the
// background at the top to blue at the bottom.
func DefaultGradientParams() GradientParams {
	return GradientParams{From: term.ColorDefault, To: term.ColorBlue, Angle: 90}
}

// Gradient returns a generator of a still gradient.
func Gradient(params GradientParams) gshader.Shader {
	return gshader.FromProgram(gradientProgram, params.uniforms())
}

func (p GradientParams) uniforms() gshader.Uniforms {
	return gshader.Uniforms{"From": p.From, "To": p.To, "Angle": float32(p.Angle)}
}
