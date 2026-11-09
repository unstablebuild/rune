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

package main

import (
	"time"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/shader"
)

const (
	initShaderFPS     = 30
	shutdownShaderFPS = 30
)

const initShaderDuration = 3000 * time.Millisecond

// introParams are the knobs of the Rune intro, which runs for
// initShaderDuration at initShaderFPS.
type introParams struct {
	// Assembly is the share of the intro spent on the dots: flying in, then
	// holding the assembled logo before it turns into the wallpaper. The
	// times of Assemble are fractions of this share, and those of Bloom and
	// Solidify fractions of the rest.
	Assembly float64
	Assemble shader.BrailleAssembleParams
	// Bloom turns the dots into the logo's picture where the screen draws
	// images, and Solidify into the wallpaper elsewhere.
	Bloom    shader.BrailleBloomParams
	Solidify shader.BrailleSolidifyParams
	// SampleColor draws each dot in the color of the logo where it lands.
	// Otherwise every dot is NonSampleColor until it blooms or solidifies
	// into the logo. A palette color, such as term.ColorRed, is the
	// theme's.
	SampleColor    bool
	NonSampleColor term.Color
}

func defaultIntroParams() introParams {
	assemble := shader.DefaultBrailleAssembleParams()
	bloom := shader.DefaultBrailleBloomParams()
	solidify := shader.DefaultBrailleSolidifyParams()
	assemble.Launch = 0.28
	assemble.Travel = 0.60
	assemble.Swirl = 0
	//bloom.Lead = 0.08
	bloom.Bloom = 0.30
	bloom.Roughness = 0.8
	//bloom.Glow = term.ColorWhite
	assemble.Lines = []shader.BrailleLine{
		{X0: 0.45, Y0: 0, X1: 0.55, Y1: 0},
		{X0: 0.45, Y0: 1, X1: 0.55, Y1: 1},
		{X0: 0, Y0: 0, X1: 0, Y1: 1},
		{X0: 1, Y0: 0, X1: 1, Y1: 1},
		{X0: 0.5, Y0: -0.5, X1: 1.5, Y1: 0.5},
		{X0: 1.5, Y0: 0.5, X1: 0.5, Y1: 1.5},
		{X0: 0.5, Y0: 1.5, X1: -0.5, Y1: 0.5},
		{X0: -0.5, Y0: 0.5, X1: 0.5, Y1: -0.5},
	}
	return introParams{
		Assembly:       0.65,
		Assemble:       assemble,
		Bloom:          bloom,
		Solidify:       solidify,
		SampleColor:    true,
		NonSampleColor: term.ColorRed,
	}
}

// makeInitShader returns the intro: the dots of the wallpaper's braille
// rendering, drawn by dots, fly in and assemble it, then they bloom into
// the wallpaper's picture, or solidify into the wallpaper on screens that
// do not draw images.
func makeInitShader(
	params introParams, dots tui.Component,
) func(term.Attributes, component.FrameCharSet) shader.Shader {
	if !params.SampleColor {
		colored := tcomponent.WithAttrSetter(dots)
		colored.SetAttr(term.Attributes{Fg: params.NonSampleColor})
		dots = colored
	}
	return func(defaultAttr term.Attributes, _ component.FrameCharSet) shader.Shader {
		return shader.TransitionCut(
			shader.TransitionCutParams{ChangeAtPerc: params.Assembly},
			shader.Mix(
				shader.BrailleSwap(dots),
				shader.BrailleAssemble(params.Assemble, defaultAttr),
			),
			shader.BrailleBloom(params.Bloom, defaultAttr, dots,
				shader.BrailleSolidify(params.Solidify, defaultAttr, dots)),
		)
	}
}
