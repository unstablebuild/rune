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

// Noise shades simplex noise patterns on screen.
//
// This is intended to be used as an exploration of that noise space.
func Noise(params NoiseParams, fps float) shader.Shader {
	return &noise{
		NoiseParams: params,
		fps:         fps,
		helper:      newHelper(),
	}
}

// DefaultNoiseParams return a set of sane NoiseParams.
func DefaultNoiseParams() NoiseParams {
	return NoiseParams{
		Animated:  true,
		Amplitude: 1.0,
		ScaleX:    5.0,
		ScaleY:    5.0,
		Speed:     1.0,
	}
}

// NoiseParams defines the parameters used by the Noise shader.
type NoiseParams struct {
	Animated  bool
	Amplitude float
	ScaleX    float
	ScaleY    float
	Speed     float
	// PaintForeground shades the characters with the noise field rather
	// than filling the cell background and scattering glyphs, so the
	// effect is only visible through existing text. Blank cells and
	// glyphs that render as background, such as fade blocks, are left
	// untouched.
	PaintForeground bool
}

type noise struct {
	NoiseParams
	helper *glslHelper
	fps    float
}

func (s *noise) Shade(frame, total int, in [][]term.Cell) {
	s.helper.shadeGLSL(frame, total, s.fps, in, s)
}

func (s *noise) runCell(
	frame, total int, fps float, time float,
	cellCoords term.Coordinates,
	fragCoordX, fragCoordY int,
	resolutionX, resolutionY int,
	inChar rune, inFg, inBg term.Color,
) (char rune, fg, bg term.Color) {
	if s.PaintForeground && !paintsText(inChar) {
		return inChar, inFg, inBg
	}

	spedTime := s.Speed * time

	ar := float(resolutionX) / float(resolutionY)

	y := float(fragCoordY) / float(resolutionY)
	x := float(fragCoordX) / float(resolutionX)

	if s.Animated {
		y += 0.2 * spedTime
		x += 0.2 * spedTime
	}

	if s.Animated {
		x *= s.ScaleX + 3.0*cos(0.5*spedTime)
		y *= s.ScaleY + 3.0*sin(0.5*spedTime)
	} else {
		x *= s.ScaleX
		y *= s.ScaleY

	}
	y /= ar

	out := noiseSimplex(vec2(x, y))

	plane := 0.0
	if s.Animated {
		plane = sin(spedTime)
	}

	out = plane + s.Amplitude*(0.5*out)

	fgBase := int32(255.0 * (1.0 - out) * (1.0 - out))
	bgBase := int32(255 * out)

	if s.PaintForeground {
		return inChar, term.NewRGBColor(bgBase, bgBase, bgBase), inBg
	}

	char = inChar
	if out > 0.5 {
		char = 'x'
	}

	fg = term.NewRGBColor(fgBase, fgBase, fgBase)
	bg = term.NewRGBColor(bgBase, bgBase, bgBase)
	return
}
