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

var (
	//go:embed desaturate.kage
	desaturateKage []byte
	//go:embed vignette.kage
	vignetteKage []byte
	//go:embed scanlines.kage
	scanlinesKage []byte
	//go:embed shine.kage
	shineKage []byte

	desaturateProgram = program("desaturate.kage", desaturateKage)
	vignetteProgram   = program("vignette.kage", vignetteKage)
	scanlinesProgram  = program("scanlines.kage", scanlinesKage)
	shineProgram      = program("shine.kage", shineKage)
)

// DesaturateParams are the parameters of Desaturate.
type DesaturateParams struct {
	// Amount is how far the colours turn grey, from 0 to 1.
	Amount float64
}

// DefaultDesaturateParams returns DesaturateParams that turn everything
// grey.
func DefaultDesaturateParams() DesaturateParams {
	return DesaturateParams{Amount: 1}
}

// Desaturate returns a filter that turns what it shades grey.
func Desaturate(params DesaturateParams) gshader.Shader {
	return gshader.FromProgram(desaturateProgram, gshader.Uniforms{"Amount": float32(params.Amount)})
}

// VignetteParams are the parameters of Vignette.
type VignetteParams struct {
	// Strength is how dark the corners get, from 0 to 1.
	Strength float64
	// Radius is how far from the centre the darkening starts: 0 at the
	// centre, 1 at the middle of the edges.
	Radius float64
}

// DefaultVignetteParams returns VignetteParams that darken the corners by
// half.
func DefaultVignetteParams() VignetteParams {
	return VignetteParams{Strength: 0.5, Radius: 0.6}
}

// Vignette returns a filter that darkens the edges of what it shades.
func Vignette(params VignetteParams) gshader.Shader {
	return gshader.FromProgram(vignetteProgram, gshader.Uniforms{
		"Strength": float32(params.Strength),
		"Radius":   float32(params.Radius),
	})
}

// ScanlinesParams are the parameters of Scanlines.
type ScanlinesParams struct {
	// Strength is how dark the lines are, from 0 to 1.
	Strength float64
	// Spacing is how many pixels apart the lines are.
	Spacing float64
	// Speed is how many lines roll past over a run, rounded to a whole
	// number; 0 keeps them still.
	Speed float64
}

// DefaultScanlinesParams returns ScanlinesParams for faint, still lines
// three pixels apart.
func DefaultScanlinesParams() ScanlinesParams {
	return ScanlinesParams{Strength: 0.25, Spacing: 3}
}

// Scanlines returns a filter that draws the lines of a cathode-ray tube
// over what it shades.
func Scanlines(params ScanlinesParams) gshader.Shader {
	return gshader.FromProgram(scanlinesProgram, gshader.Uniforms{
		"Strength": float32(params.Strength),
		"Spacing":  float32(params.Spacing),
		"Speed":    float32(params.Speed),
	})
}

// ShineParams are the parameters of Shine.
type ShineParams struct {
	// Color is the colour of the shine.
	Color term.Color
	// Angle is the direction the band sweeps in, in degrees clockwise from
	// left to right.
	Angle float64
	// BandWidth is how wide the band is, as a fraction of the sweep.
	BandWidth float64
	// Cycles is how many times the band sweeps over a run, rounded to a
	// whole number of at least 1.
	Cycles int
	// Intensity is how far what the band passes over turns to Color, from
	// 0 to 1.
	Intensity float64
}

// DefaultShineParams returns ShineParams for a white band that sweeps once
// a run from the bottom left to the top right, as glslshader.Shine does.
func DefaultShineParams() ShineParams {
	return ShineParams{Color: term.NewRGBColor(255, 255, 255), Angle: -45, BandWidth: 0.25, Cycles: 1, Intensity: 1}
}

// Shine returns a filter that sweeps a band of light over what it shades,
// shining on what is drawn over the background. The band enters after the
// start of a run and leaves before its end, so a run ends where it starts.
func Shine(params ShineParams) gshader.Shader {
	return gshader.FromProgram(shineProgram, gshader.Uniforms{
		"Color":     params.Color,
		"Angle":     float32(params.Angle),
		"BandWidth": float32(params.BandWidth),
		"Cycles":    float32(params.Cycles),
		"Intensity": float32(params.Intensity),
	})
}
