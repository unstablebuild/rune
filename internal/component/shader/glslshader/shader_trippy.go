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

// Trippy layers animated colors with some noise.
//
// Tutorial file from: http://x.unstable.build/docs/tutorials/ox/pixel_shader#porting-from-shadertoy
func Trippy(params TrippyParams, fps float) shader.Shader {
	return &trippy{
		helper:       newHelper(),
		TrippyParams: params,
		fps:          fps,
		noiseD:       vec2(0.0, 1.0),
		randVec:      vec2(12.9898, 4.1414),
	}
}

// DefaultTrippyParams return a set of sane TrippyParams.
func DefaultTrippyParams() TrippyParams {
	return TrippyParams{Speed: 1.0}
}

// TrippyParams defines the parameters used by the Trippy shader.
type TrippyParams struct {
	Speed float // float is an alias of float64
	// PaintForeground colours the characters rather than the cell
	// background and keeps them, so the effect is only visible through
	// existing text. Blank cells and glyphs that render as background,
	// such as fade blocks, are left untouched. When off, the effect fills
	// the cell background and clears the character.
	PaintForeground bool
}

type trippy struct {
	TrippyParams
	helper  *glslHelper
	fps     float
	noiseD  vec2D
	randVec vec2D
}

func (s *trippy) Shade(frame, total int, in [][]term.Cell) {
	s.helper.shadeGLSL(frame, total, s.fps, in, s)
}

func (s *trippy) runCell(
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

	iTime := time // time in seconds
	iResolution := vec2(float64(resolutionX), float64(resolutionY))

	// glsl: vec2 uv = fragCoord/iResolution.xy; // normalized pixel coords (from 0 to 1)
	// normalized pixel coordinates (from 0 to 1)
	uv := fragCoord.div(iResolution)

	// glsl: uv.y /= iResolution.x / iResolution.y;
	// show always squares regardless of screen aspect ratio
	ar := float(resolutionX) / float(resolutionY)
	uv.y /= ar

	// glsl: vec3 col = 0.5 + 0.5*cos(speed*iTime+uv.xyx+vec3(0,2,4));
	col := vec3FromScalar(0.5).add(cos3D(
		vec3FromScalar(s.Speed * iTime).add(uv.xyx()).add(vec3(0.0, 2.0, 4.0)),
	).multSc(0.5))
	// glsl: col *= noise(5.0*(0.5+0.5*sin(iTime))*uv/0.3);
	col = col.multSc(
		s.noise(
			vec2FromScalar(
				5.0 * (0.5 + 0.5*sin(iTime)),
			).mult(uv.div(vec2FromScalar(0.3))),
		),
	)

	col = col.multSc(255.0)
	color := term.NewRGBColor(int32(col.x), int32(col.y), int32(col.z))
	if s.PaintForeground {
		return inChar, color, inBg
	}
	bg = color
	return
}

func (s *trippy) rand(n vec2D) float {
	return fract(cos(dot2D(n, s.randVec)) * 43758.5453)
}

func (s *trippy) noise(n vec2D) float {
	d := s.noiseD
	b := n.floor()
	f := smoothstep2D(vec2FromScalar(0.0), vec2FromScalar(1.0), fract2D(n))
	return mix(
		mix(s.rand(b), s.rand(b.add(d.yx())), f.x),
		mix(s.rand(b.add(d.yx())), s.rand(b.add(d.yy())), f.x),
		f.y,
	)
}
