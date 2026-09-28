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
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

// Inferno shader remixed from codevinsky's Fire (https://www.shadertoy.com/view/XsXXRN)
func Inferno(params InfernoParams, fps float) shader.Shader {
	return &inferno{
		helper:        newHelper(),
		InfernoParams: params,
		fps:           fps,
		c1:            vec3(0.5, 0.0, 0.1),
		c2:            vec3(0.9, 0.1, 0.0),
		c3:            vec3(0.2, 0.1, 0.7),
		c4:            vec3(1.0, 0.9, 0.1),
		c5:            vec3(0.1, 0.1, 0.1),
		c6:            vec3(0.9, 0.9, 0.9),
		K1:            vec4(0.0, -1.0/3.0, 2.0/3.0, -1.0),
		K2:            vec4(1.0, 2.0/3.0, 1.0/3.0, 3.0),
	}
}

// InfernoParams defines the parameters used by the Inferno shader.
type InfernoParams struct {
	Speed       vec2D
	SwapRedBlue bool
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

type inferno struct {
	helper *glslHelper
	InfernoParams
	fps float
	c1  vec3D
	c2  vec3D
	c3  vec3D
	c4  vec3D
	c5  vec3D
	c6  vec3D
	K1  vec4D
	K2  vec4D
}

func (s *inferno) runCell(
	frame, total int, fps float, time float,
	cellCoords term.Coordinates,
	fragCoordX, fragCoordY int,
	resolutionX, resolutionY int,
	inChar rune, inFg, inBg term.Color,
) (char rune, fg, bg term.Color) {
	if s.PaintForeground && !paintsText(inChar) {
		return inChar, inFg, inBg
	}

	fragCoord := vec2(float(fragCoordX), float(fragCoordY))

	iTime := time
	iResolution := vec2(float(resolutionX), float(resolutionY))

	shift := 1.6
	p := fragCoord.multSc(8.0).div(iResolution.xx())
	q := s.fbm(p.subSc(iTime * 0.1))
	r := vec2(s.fbm(p.addSc(q+iTime*s.Speed.x-p.x-p.y)), s.fbm(p.addSc(q-iTime*s.Speed.y)))
	c := mix3D(s.c1, s.c2, s.fbm(p.add(r))).add(mix3D(s.c3, s.c4, r.x)).sub(mix3D(s.c5, s.c6, r.y))
	col := c.mult(vec3FromScalar(cos(shift * fragCoord.y / iResolution.y)))
	col = clamp3D(col, vec3FromScalar(0.0), vec3FromScalar(1.0))
	col = col.multSc(255.0)

	if s.SwapRedBlue {
		col = vec3(col.z, col.y, col.x) // blue flames insteead of red
	}

	fire := term.NewRGBColor(int32(col.x), int32(col.y), int32(col.z))
	if s.PaintForeground {
		fg, bg = fire, inBg
	} else {
		fg, bg = inFg, fire
	}
	char = inChar
	return
}

func (s *inferno) rand(n vec2D) float {
	return fract(cos(dot2D(n, vec2(12.9898, 4.1414))) * 43758.5453)
}

func (s *inferno) noise(n vec2D) float {
	d := vec2(0.0, 1.0)
	b := n.floor()
	f := smoothstep2D(vec2FromScalar(0.0), vec2FromScalar(1.0), fract2D(n))
	return mix(
		mix(s.rand(b), s.rand(b.add(d.yx())), f.x),
		mix(s.rand(b.add(d.yx())), s.rand(b.add(d.yy())), f.x),
		f.y,
	)
}

func (s *inferno) fbm(n vec2D) float {
	total := 0.0
	amplitude := 1.0
	for range 4 {
		total += s.noise(n) * amplitude
		n = n.add(n)
		amplitude *= 0.5
	}
	return total
}

func (s *inferno) Shade(frame, total int, in [][]term.Cell) {
	s.helper.shadeGLSL(frame, total, s.fps, in, s)
}
