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
	// blazeLoops and infernoLoops are how many loops one cycle of each
	// fire spans, which at DefaultLoop keeps the pace it was tuned to.
	blazeLoops   = 26
	infernoLoops = 83
	// BlazeCycle is how long one cycle of the blaze fire lasts at the
	// pace it was tuned to, for those that play it for a set time rather
	// than while work runs.
	BlazeCycle = blazeLoops * DefaultLoop
	// InfernoCycle is BlazeCycle for the inferno fire.
	InfernoCycle = infernoLoops * DefaultLoop
)

// Params tunes the effects New builds, each under its own name; an
// effect only reads its own. Zero values keep the catalog's own tuning,
// which is deliberately subtle so the effect does not repaint text
// whose colour carries meaning, and paints the text alone so the label
// stays readable.
type Params struct {
	Blaze   FireParams
	Burn    BurnParams
	Inferno FireParams
	Pulse   PulseParams
	Shine   ShineParams
}

// DefaultParams spells out the tuning the zero Params stands for. New
// paints the same with either.
func DefaultParams() Params {
	burn := shader.DefaultBurnParams()
	shine := glslshader.DefaultShineParams()
	// The fire's red: a hue it is turned no way from.
	fire := FireParams{Color: term.NewRGBColor(255, 0, 0)}
	return Params{
		Blaze: fire,
		Burn: BurnParams{
			Gradient: burn.BurnGradient,
			Duration: burn.BurnDuration,
			Seed:     burn.Seed,
		},
		Inferno: fire,
		Pulse: PulseParams{
			Color:     shader.DefaultPulseParams().Color,
			Intensity: pulseIntensity,
		},
		Shine: ShineParams{
			Color:     shine.Color,
			Intensity: shineIntensity,
			BandWidth: shineBandWidth,
			Direction: shine.Direction,
		},
	}
}

// FireParams tunes the blaze and inferno effects.
type FireParams struct {
	// AnimateBackground paints the fire on the cell background rather
	// than on the text.
	AnimateBackground bool
	// Color, unless term.ColorDefault, is the hue the fire burns in: its
	// red turns to the hue of Color, keeping the flames' light and shade.
	// A grey leaves the fire red.
	Color term.Color
	// SwapRedBlue burns blue rather than red, which does not read as an
	// error the way red does. It applies after Color.
	SwapRedBlue bool
}

// BurnParams tunes the burn effect, a wave that crosses the label once
// per loop.
type BurnParams struct {
	// AnimateBackground burns the label, each cell cycling through fire
	// glyphs as the wave crosses it, rather than only recolouring the
	// text.
	AnimateBackground bool
	// Gradient replaces the colours a cell goes through while it burns,
	// from ignition to ash. Empty keeps the fire colours.
	Gradient []term.Color
	// Duration, in (0, 1], is the fraction of a loop each cell burns
	// for. Shorter reads as a narrow wave front.
	Duration float64
	// Seed picks another, equally repeatable, burn order. Zero keeps the
	// default one.
	Seed int64
	// FixedOrigin starts the wave at Origin, in [0, 1] across the label
	// from its left edge, rather than at a random character of it.
	FixedOrigin bool
	Origin      float64
}

// PulseParams tunes the pulse effect.
type PulseParams struct {
	// Color replaces the white text is blended toward, unless it is
	// term.ColorDefault.
	Color term.Color
	// Intensity, in (0, 1], is how far text is blended toward Color at
	// the peak of the pulse: 1 reaches it.
	Intensity float64
}

// ShineParams tunes the shine effect.
type ShineParams struct {
	// Color replaces the white text is blended toward, unless it is
	// term.ColorDefault.
	Color term.Color
	// Intensity, in (0, 1], is how far text is blended toward Color at
	// the centre of the band: 1 reaches it.
	Intensity float64
	// BandWidth, in (0, 1], is how much of the label the band spans.
	// Narrower reads as a glint sweeping across, wider as a wash.
	BandWidth float64
	// Direction is the way the band sweeps across the label. The zero
	// value is the default, bottom-left to top-right.
	Direction glslshader.Direction
}

// Names lists the effects New accepts. The frame shaders are
// deliberately absent: they only paint box-drawing characters, of which
// a row of text has none. Incendium costs too much to run for the
// length of every turn, and embers, flames and risingChars garble a
// single row of text rather than dress it. Fade and grayFade step
// visibly when looped, so they read as jerky rather than as activity.
func Names() []string {
	return []string{
		"blaze", "burn", "inferno", "pulse", "shine",
	}
}

// New resolves a name from Names into an effect that keeps painting
// for as long as the work runs, repeating seamlessly every loop or, for
// the fire, every few dozen loops. It reports false for unknown names.
func New(
	name string, defAttr term.Attributes, p Params, loop time.Duration,
) (shader.Shader, bool) {
	var inner shader.Shader
	// span is how many loops one cycle of the effect lasts.
	span := 1
	switch name {
	case "blaze":
		blazeParams := glslshader.DefaultBlazeParams()
		blazeParams.PaintForeground = !p.Blaze.AnimateBackground
		blazeParams.Color = p.Blaze.Color
		blazeParams.SwapRedBlue = p.Blaze.SwapRedBlue
		inner, span = glslshader.Blaze(blazeParams), blazeLoops
	case "burn":
		burnParams := shader.DefaultBurnParams()
		burnParams.PaintForeground = !p.Burn.AnimateBackground
		if len(p.Burn.Gradient) > 0 {
			burnParams.BurnGradient = p.Burn.Gradient
		}
		burnParams.BurnDuration = fractionOr(p.Burn.Duration, burnParams.BurnDuration)
		if p.Burn.Seed != 0 {
			burnParams.Seed = p.Burn.Seed
		}
		if p.Burn.FixedOrigin {
			burnParams.FixedOrigin = true
			burnParams.OriginX = p.Burn.Origin
		}
		inner = shader.Burn(burnParams, defAttr)
	case "inferno":
		infernoParams := glslshader.DefaultInfernoParams()
		infernoParams.PaintForeground = !p.Inferno.AnimateBackground
		infernoParams.Color = p.Inferno.Color
		infernoParams.SwapRedBlue = p.Inferno.SwapRedBlue
		inner, span = glslshader.Inferno(infernoParams), infernoLoops
	case "pulse":
		params := shader.DefaultPulseParams()
		params.Intensity = fractionOr(p.Pulse.Intensity, pulseIntensity)
		if p.Pulse.Color != term.ColorDefault {
			params.Color = p.Pulse.Color
		}
		inner = shader.Pulse(params, defAttr)
	case "shine":
		shineParams := glslshader.DefaultShineParams()
		shineParams.BandWidth = fractionOr(p.Shine.BandWidth, shineBandWidth)
		shineParams.Intensity = fractionOr(p.Shine.Intensity, shineIntensity)
		if p.Shine.Color != term.ColorDefault {
			shineParams.Color = p.Shine.Color
		}
		shineParams.Direction = p.Shine.Direction
		inner = glslshader.Shine(shineParams, defAttr)
	default:
		return nil, false
	}
	return timeshader.Loop(inner, max(1, int(Duration/(loop*time.Duration(span))))), true
}

// fractionOr returns v, or def when v is not in (0, 1].
func fractionOr(v, def float64) float64 {
	if v <= 0 || v > 1 {
		return def
	}
	return v
}
