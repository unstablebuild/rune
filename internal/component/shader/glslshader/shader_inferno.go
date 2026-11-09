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

import (
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

// Inferno shader remixed from codevinsky's Fire (https://www.shadertoy.com/view/XsXXRN)
//
// A run is one cycle of the fire: its last frame leads back into its
// first, so it loops seamlessly, and on a canvas of any size. A longer
// run plays the same cycle slower.
func Inferno(params InfernoParams) shader.Shader {
	s := &inferno{
		helper:        newHelper(),
		InfernoParams: params,
		c1:            vec3(0.5, 0.0, 0.1),
		c2:            vec3(0.9, 0.1, 0.0),
		c3:            vec3(0.2, 0.1, 0.7),
		c4:            vec3(1.0, 0.9, 0.1),
		c5:            vec3(0.1, 0.1, 0.1),
		c6:            vec3(0.9, 0.9, 0.9),
		K1:            vec4(0.0, -1.0/3.0, 2.0/3.0, -1.0),
		K2:            vec4(1.0, 2.0/3.0, 1.0/3.0, 3.0),
		hue:           colorHue(params.Color),
	}
	for layer, diagonal := range []float{
		infernoQ:     -0.1,
		infernoRX:    params.Speed.x,
		infernoRY:    -params.Speed.y,
		infernoColor: 0,
	} {
		s.drifts[layer] = newDrifts(diagonal*infernoCycle, 0, vec2D{},
			infernoOctaves, infernoLacunarity)
	}
	return s
}

// InfernoParams defines the parameters used by the Inferno shader.
type InfernoParams struct {
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

// DefaultInfernoParams return a set of sane InfernoParams.
func DefaultInfernoParams() InfernoParams {
	return InfernoParams{
		Speed:       vec2(1.2, 0.1),
		SwapRedBlue: false,
	}
}

const (
	// infernoCycle is how far along the clock its speeds are given in
	// the fire plays in a run. Its slowest drift crosses more than the
	// width of the canvas in it, so the noise it drifts through repeats
	// out of sight.
	infernoCycle      = 100.0
	infernoOctaves    = 4
	infernoLacunarity = 2.0
	infernoGain       = 0.5
)

// The layers of noise inferno is made of: one that warps the canvas, two
// that warp it again, and the one its colour comes from.
const (
	infernoQ = iota
	infernoRX
	infernoRY
	infernoColor
	infernoLayers
)

type inferno struct {
	helper *glslHelper
	InfernoParams
	c1     vec3D
	c2     vec3D
	c3     vec3D
	c4     vec3D
	c5     vec3D
	c6     vec3D
	K1     vec4D
	K2     vec4D
	drifts [infernoLayers][]drift
	// hue is how far, as a fraction of a turn, Color turns the fire.
	hue float
}

func (s *inferno) runCell(
	frame, total int,
	cellCoords term.Coordinates,
	fragCoordX, fragCoordY int,
	resolutionX, resolutionY int,
	inChar rune, inFg, inBg term.Color,
) (char rune, fg, bg term.Color) {
	if s.PaintForeground && !paintsText(inChar) {
		return inChar, inFg, inBg
	}

	col := s.color(
		vec2(float(fragCoordX), float(fragCoordY)),
		vec2(float(resolutionX), float(resolutionY)),
		float(frame)/float(total),
	)
	fire := term.NewRGBColor(int32(col.x), int32(col.y), int32(col.z))
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
func (s *inferno) color(fragCoord, iResolution vec2D, phase float) vec3D {
	shift := 1.6
	p := fragCoord.multSc(8.0).div(iResolution.xx())
	q := s.fbm(infernoQ, p, phase)
	r := vec2(s.fbm(infernoRX, vec2(q-p.y, q-p.x), phase), s.fbm(infernoRY, p.addSc(q), phase))
	c := mix3D(s.c1, s.c2, s.fbm(infernoColor, p.add(r), phase)).
		add(mix3D(s.c3, s.c4, r.x)).
		sub(mix3D(s.c5, s.c6, r.y))
	col := c.mult(vec3FromScalar(cos(shift * fragCoord.y / iResolution.y)))
	col = clamp3D(col, vec3FromScalar(0.0), vec3FromScalar(1.0))
	if s.hue != 0 {
		hsv := s.rgb2hsv(col)
		hsv.x = fract(hsv.x + s.hue)
		col = s.hsv2rgb(hsv)
	}
	col = col.multSc(255.0)

	if s.SwapRedBlue {
		col = vec3(col.z, col.y, col.x) // blue flames insteead of red
	}
	return col
}

func (s *inferno) rgb2hsv(c vec3D) vec3D {
	p := mix4D(vec4(c.z, c.y, s.K1.w, s.K1.z), vec4(c.y, c.z, s.K1.x, s.K1.y), step(c.z, c.y))
	q := mix4D(vec4(p.x, p.y, p.w, c.x), vec4(c.x, p.y, p.z, p.x), step(p.x, c.x))
	d := q.x - math.Min(q.w, q.y)
	return vec3(abs(q.z+(q.w-q.y)/(6.0*d+mathE)), d/(q.x+mathE), q.x)
}

func (s *inferno) hsv2rgb(c vec3D) vec3D {
	p := abs3D(fract3D(c.xxx().add(s.K2.xyz())).multSc(6.0).sub(s.K2.www()))
	return mix3D(
		s.K2.xxx(),
		clamp3D(p.sub(s.K2.xxx()), vec3FromScalar(0.0), vec3FromScalar(1.0)),
		c.y,
	).multSc(c.z)
}

// fbm is the noise of layer at n, phase of the way through a run.
func (s *inferno) fbm(layer int, n vec2D, phase float) float {
	return fbm(n, phase, s.drifts[layer], infernoLacunarity, infernoGain)
}

func (s *inferno) Shade(frame, total int, in [][]term.Cell) {
	s.helper.shadeGLSL(frame, total, in, s)
}
