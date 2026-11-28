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

//go:embed noise.kage
var noiseKage []byte

var noiseProgram = program("noise.kage", noiseKage, noiseInc)

// NoiseParams are the parameters of Noise.
type NoiseParams struct {
	// From and To are the colours of the lowest and the highest noise.
	From, To term.Color
	// Scale is how many features of the noise span the height.
	Scale float64
	// Speed is how many times the noise changes over a run, rounded to a
	// whole number; 0 keeps it still.
	Speed float64
	// Seed picks the noise: each whole number draws different noise.
	Seed int
}

// DefaultNoiseParams returns NoiseParams for clouds of blue over the
// background that change twice a run.
func DefaultNoiseParams() NoiseParams {
	return NoiseParams{From: term.ColorDefault, To: term.ColorBlue, Scale: 3, Speed: 2}
}

// Noise returns a generator of fractal noise that changes over a run and
// ends where it starts.
func Noise(params NoiseParams) gshader.Shader {
	return gshader.FromProgram(noiseProgram, params.uniforms())
}

func (p NoiseParams) uniforms() gshader.Uniforms {
	return gshader.Uniforms{
		"From":              p.From,
		"To":                p.To,
		"Scale":             float32(p.Scale),
		"Speed":             float32(p.Speed),
		gshader.UniformSeed: float32(p.Seed),
	}
}
