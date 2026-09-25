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

// Package shaderloop is the catalog of shader effects that can run for
// as long as some work of unknown length is in progress, such as an
// agent turn. It lives apart from package shader because the catalog
// draws on glslshader and timeshader, both of which import shader.
package shaderloop

import (
	"time"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/glslshader"
	"unstable.build/rune/internal/component/shader/timeshader"
)

const (
	// DefaultFPS is the cadence an effect is redrawn at when the config
	// names none.
	DefaultFPS = 30
	// DefaultLoop is how long one visual loop of an effect lasts when
	// the config names none.
	DefaultLoop = 1200 * time.Millisecond
	// Duration is the lifetime to hand shader.New. The work has no
	// known length, so the animation is built long enough to outlast
	// any of it and looped within that span.
	Duration = 24 * time.Hour
	// Effects run over UI that carries meaning in colour, and the stock
	// pulse washes a dark one most of the way to white so it reads as a
	// different colour.
	pulseIntensity = 0.25
	// shineBandWidth widens the stock shine band, which on a single row
	// of text reads as a narrow glint rather than a sweep.
	shineBandWidth = 0.75
	// shineIntensity keeps the shine from washing text out to white, for
	// the same reason pulseIntensity is lowered.
	shineIntensity = 0.3
)

// loopFrames is one visual loop at the given cadence.
func loopFrames(fps int, loop time.Duration) int {
	return int(float64(fps) * loop.Seconds())
}

// Names lists the effects New accepts. The frame shaders are
// deliberately absent: they only paint box-drawing characters, of which
// a row of text has none. Incendium costs too much to run for the
// length of every turn, and embers, flames and risingChars garble a
// single row of text rather than dress it. Fade and grayFade step
// visibly when looped, so they read as jerky rather than as activity.
func Names() []string {
	return []string{
		"blaze", "burn", "inferno", "noise", "pulse", "shine", "trippy",
	}
}

// New resolves a name from Names into an effect that keeps painting
// for as long as the work runs, either by looping its animation every
// loop or by running its own clock. It reports false for unknown names.
func New(
	name string, defAttr term.Attributes, fps int, loop time.Duration,
) (shader.Shader, bool) {
	fpsf := float64(fps)
	var inner shader.Shader
	// An effect that runs its clock off the frame index alone rather
	// than against total is already continuous, and Loop would replay
	// the whole Duration inside one loop window and run it thousands of
	// times too fast.
	var continuous bool
	switch name {
	case "blaze":
		blazeParams := glslshader.DefaultBlazeParams()
		blazeParams.PaintForeground = true
		inner, continuous = glslshader.Blaze(blazeParams, fpsf), true
	case "burn":
		burnParams := shader.DefaultBurnParams()
		burnParams.PaintForeground = true
		inner = shader.Burn(burnParams, defAttr)
	case "inferno":
		infernoParams := glslshader.DefaultInfernoParams()
		infernoParams.PaintForeground = true
		inner, continuous = glslshader.Inferno(infernoParams, fpsf), true
	case "noise":
		noiseParams := glslshader.DefaultNoiseParams()
		noiseParams.PaintForeground = true
		inner, continuous = glslshader.Noise(noiseParams, fpsf), true
	case "pulse":
		params := shader.DefaultPulseParams()
		params.PeriodFrames = loopFrames(fps, loop)
		params.Intensity = pulseIntensity
		inner, continuous = shader.Pulse(params, defAttr), true
	case "shine":
		shineParams := glslshader.DefaultShineParams()
		shineParams.BandWidth = shineBandWidth
		shineParams.Intensity = shineIntensity
		inner = glslshader.Shine(shineParams, defAttr)
	case "trippy":
		trippyParams := glslshader.DefaultTrippyParams()
		trippyParams.PaintForeground = true
		inner, continuous = glslshader.Trippy(trippyParams, fpsf), true
	default:
		return nil, false
	}
	if continuous {
		return inner, true
	}
	return timeshader.Loop(inner, int(Duration/loop)), true
}
