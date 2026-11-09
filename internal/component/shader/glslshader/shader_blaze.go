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

package glslshader

// shader remixed from CaliCoastReplay's 301's Fire Shader - Remix 2
// (https://www.shadertoy.com/view/MtcGD7)

import (
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

// Blaze shader remixed from CaliCoastReplay's 301's Fire Shader - Remix 2
// (https://www.shadertoy.com/view/MtcGD7)
//
// A run is one cycle of the fire: its last frame leads back into its
// first, so it loops seamlessly, and on a canvas of any size. A longer
// run plays the same cycle slower.
func Blaze(params BlazeParams) shader.Shader {
	s := &blaze{
		helper:      newHelper(),
		BlazeParams: params,
		c1:          vec3(0.5, 0.0, 0.1),
		c2:          vec3(0.9, 0.1, 0.0),
		c3:          vec3(0.2, 0.1, 0.7),
		c4:          vec3(1.0, 0.9, 0.1),
		c5:          vec3(0.1, 0.1, 0.1),
		c6:          vec3(0.9, 0.9, 0.9),
		k1:          vec4(0.0, -1.0/3.0, 2.0/3.0, -1.0),
		k2:          vec4(1.0, 2.0/3.0, 1.0/3.0, 3.0),
		hue:         colorHue(params.Color),
	}
	// Each layer of noise drifts diagonally at its own speed, and all
	// of them slide sideways with the fire. One warp reads the canvas
	// mirrored, so its sideways slide shows as a vertical one.
	left, up := vec2(-1, 0), vec2(0, 1)
	for layer, d := range []struct {
		diagonal float
		panDir   vec2D
	}{
		blazeQ:     {-0.01, left},
		blazeQB:    {0.002, left},
		blazeQ2:    {-0.44, left},
		blazeQ3:    {-0.9, left},
		blazeQ4:    {-2, left},
		blazeRX:    {params.Speed.x, up},
		blazeRY:    {-params.Speed.y, left},
		blazeColor: {0, left},
	} {
		s.drifts[layer] = newDrifts(d.diagonal*blazeCycle, blazePan*blazeCycle, d.panDir,
			blazeOctaves, blazeLacunarity)
	}
	return s
}

// BlazeParams defines the parameters used by the Bomb shader.
type BlazeParams struct {
	// Speed is how fast the fire's two warps drift, rounded so that the
	// fire repeats once a run.
	Speed       vec2D
	SwapRedBlue bool
	// Color, unless term.ColorDefault, turns the fire's hue so its red
	// burns in the hue of Color, keeping the flames' light and shade.
	// Only the hue counts, so a grey leaves the fire red. It applies
	// before SwapRedBlue.
	Color term.Color
	// PaintForeground colours the characters rather than the cell
	// background, so the fire is only visible through existing text.
	// Blank cells and glyphs that render as background, such as fade
	// blocks, are left untouched.
	PaintForeground bool
}

// DefaultBlazeParams return a set of sane BlazeParams.
func DefaultBlazeParams() BlazeParams {
	return BlazeParams{
		Speed:       vec2(1.2, 0.1),
		SwapRedBlue: false,
	}
}

const (
	// blazeCycle is how far along the clock its speeds are given in the
	// fire plays in a run. Its sway, its zoom and its wobble all turn a
	// whole number of times over it, and every drift slides far enough
	// sideways for the noise it crosses to repeat out of sight.
	blazeCycle = 10 * math.Pi
	// blazePan is how fast the whole fire slides sideways.
	blazePan        = 1 / 1.1
	blazeOctaves    = 5
	blazeLacunarity = 2.7
	blazeGain       = 0.47
)

// The layers of noise blaze is made of: five that warp the canvas, two
// that warp it again, and the one its colour comes from.
const (
	blazeQ = iota
	blazeQB
	blazeQ2
	blazeQ3
	blazeQ4
	blazeRX
	blazeRY
	blazeColor
	blazeLayers
)

type blaze struct {
	helper *glslHelper
	BlazeParams
	c1     vec3D
	c2     vec3D
	c3     vec3D
	c4     vec3D
	c5     vec3D
	c6     vec3D
	k1     vec4D
	k2     vec4D
	drifts [blazeLayers][]drift
	// hue is how far, as a fraction of a turn, Color turns the fire.
	hue float
}

func (s *blaze) runCell(
	frame, total int,
	cellCoords term.Coordinates,
	fragCoordX, fragCoordY int,
	resolutionX, resolutionY int,
	inChar rune, inFg, inBg term.Color,
) (char rune, fg, bg term.Color) {
	if s.PaintForeground && !paintsText(inChar) {
		return inChar, inFg, inBg
	}

	color := s.color(
		vec2(float(fragCoordX), float(fragCoordY)),
		vec2(float(resolutionX), float(resolutionY)),
		float(frame)/float(total),
	)
	fire := term.NewRGBColor(int32(color.x), int32(color.y), int32(color.z))
	if s.PaintForeground {
		fg, bg = fire, inBg
	} else {
		fg, bg = inFg, fire
	}
	char = inChar

	return
}

// color is the fire at fragCoord, in 0..255 per channel, phase of the
// way through a run. At phase 1 it is back where it was at 0.
func (s *blaze) color(fragCoord, iResolution vec2D, phase float) vec3D {
	// Only what turns over a run reads the clock. What drifts does so
	// through the drift of its layer.
	iTime := phase * blazeCycle

	shift := 1.327 + sin(iTime*2.0)/2.4

	// change the constant term for all kinds of cool distance versions,
	// make plus/minus to switch between
	// ground fire and fire rain!
	dist := 3.5 - sin(iTime*0.4)/1.89

	p := fragCoord.multSc(dist / iResolution.x)

	q := s.fbm(blazeQ, p.add(vec2FromScalar(1.0*sin(iTime)/10.0)), phase)
	qb := s.fbm(blazeQB, p.add(vec2FromScalar(0.1*cos(iTime)/5.0)), phase)
	q2 := s.fbm(blazeQ2, p.add(vec2FromScalar(-5.0*cos(iTime)/7.0)), phase) - 6.0
	q3 := s.fbm(blazeQ3, p.add(vec2FromScalar(-10.0*cos(iTime)/30.0)), phase) - 4.0
	q4 := s.fbm(blazeQ4, p.add(vec2FromScalar(-20.0*sin(iTime)/20.0)), phase) + 2.0

	q = (q + qb - 0.4*q2 - 2.0*q3 + 0.6*q4) / 3.8

	r := vec2(
		s.fbm(blazeRX, vec2(q/2.0-p.y, q/2.0-p.x), phase),
		s.fbm(blazeRY, p.addSc(q), phase),
	)
	c := mix3D(s.c1, s.c2, s.fbm(blazeColor, p.add(r), phase)).
		add(mix3D(s.c3, s.c4, r.x)).
		sub(mix3D(s.c5, s.c6, r.y))
	color := c.multSc(cos(shift * fragCoord.y / iResolution.y))
	color = color.addSc(0.05)
	color.x *= 0.8

	hsv := s.rgb2hsv(color)
	hsv.y *= hsv.z * 1.1
	hsv.z *= hsv.y * 1.13
	hsv.y = (2.2 - hsv.z*.9) * 1.20
	if s.hue != 0 {
		hsv.x = fract(hsv.x + s.hue)
	}
	color = s.hsv2rgb(hsv)
	color = clamp3D(color, vec3FromScalar(0.0), vec3FromScalar(1.0))

	if s.SwapRedBlue {
		color = vec3(color.z, color.y, color.x) // blue flames insteead of red
	}

	return color.multSc(255.0)
}

func (s *blaze) rgb2hsv(c vec3D) vec3D {
	p := mix4D(vec4(c.z, c.y, s.k1.w, s.k1.z), vec4(c.y, c.z, s.k1.x, s.k1.y), step(c.z, c.y))
	q := mix4D(vec4(p.x, p.y, p.w, c.x), vec4(c.x, p.y, p.z, p.x), step(p.x, c.x))
	d := q.x - math.Min(q.w, q.y)
	return vec3(abs(q.z+(q.w-q.y)/(6.0*d+mathE)), d/(q.x+mathE), q.x)
}

func (s *blaze) hsv2rgb(c vec3D) vec3D {
	p := abs3D(fract3D(c.xxx().add(s.k2.xyz())).multSc(6.0).sub(s.k2.www()))
	ret := mix3D(
		s.k2.xxx(),
		clamp3D(p.sub(s.k2.xxx()), vec3FromScalar(0.0), vec3FromScalar(1.0)),
		c.y,
	).multSc(c.z)
	return ret
}

// fbm is the noise of layer at n, phase of the way through a run.
func (s *blaze) fbm(layer int, n vec2D, phase float) float {
	return fbm(n, phase, s.drifts[layer], blazeLacunarity, blazeGain)
}

func (s *blaze) Shade(frame, total int, in [][]term.Cell) {
	s.helper.shadeGLSL(frame, total, in, s)
}
