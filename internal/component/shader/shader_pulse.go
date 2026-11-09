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

package shader

import (
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/component/shader/shaderutils"
)

// Pulse returns a Shader that ping-pongs every cell's Fg and Bg
// toward params.Color params.Pulses times over the animation. Zero or
// invalid fields in params fall back to [DefaultPulseParams].
// defaultAttr resolves [term.ColorDefault] cells for the blend;
// when a cell color and defaultAttr counterpart are both default,
// that channel is left unchanged.
//
// Pulse shades every cell of its input matrix. To restrict to a
// sub-grid, wrap with [Virtual].
func Pulse(params PulseParams, defaultAttr term.Attributes) Shader {
	defaults := DefaultPulseParams()
	if params.Color == term.ColorDefault {
		params.Color = defaults.Color
	}
	if !(params.Pulses > 0) || math.IsInf(params.Pulses, 0) {
		params.Pulses = defaults.Pulses
	}
	if params.Intensity <= 0 {
		params.Intensity = defaults.Intensity
	}
	return &pulse{PulseParams: params, defaultAttr: defaultAttr}
}

// PulseParams configures [Pulse].
type PulseParams struct {
	// Color is the highlight color cells fade toward at peak
	// intensity.
	Color term.Color
	// Pulses is the number of full pulses over the animation; each
	// starts and ends with the cells untouched. Values at or below
	// zero, and infinite ones, are replaced by the default.
	Pulses float64
	// Intensity caps the peak blend weight in [0, 1]. Values
	// at or below zero are replaced by the default.
	Intensity float64
}

// DefaultPulseParams returns [PulseParams] with Color=white,
// Pulses=1, and Intensity=0.6.
func DefaultPulseParams() PulseParams {
	return PulseParams{
		Color:     term.NewRGBColor(255, 255, 255),
		Pulses:    1,
		Intensity: 0.6,
	}
}

type pulse struct {
	PulseParams
	defaultAttr term.Attributes
}

func (s *pulse) Shade(frame, total int, cells [][]term.Cell) {
	if total <= 0 || frame < 0 || frame >= total {
		return
	}
	cyclePos := float64(frame) / float64(total) * s.Pulses
	cyclePos -= math.Floor(cyclePos)
	// sin(pi*x) peaks at cyclePos=0.5 and returns to zero at
	// cycle boundaries — softer than a triangle wave.
	weight := math.Sin(math.Pi*cyclePos) * s.Intensity
	if weight <= 0 {
		return
	}
	for y, row := range cells {
		for x, cell := range row {
			cells[y][x].Fg = shaderutils.InterpolateColor(
				weight, cell.Fg, s.Color, s.defaultAttr.Fg,
			)
			cells[y][x].Bg = shaderutils.InterpolateColor(
				weight, cell.Bg, s.Color, s.defaultAttr.Bg,
			)
		}
	}
}
