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
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// Direction selects along which axis the shine band sweeps. The aspect
// ratio of terminal cells is corrected internally so diagonal directions
// look visually balanced.
type Direction int

const (
	// DirectionBottomLeftToTopRight makes the band travel diagonally from
	// the bottom-left corner toward the top-right corner.
	DirectionBottomLeftToTopRight Direction = iota
	// DirectionTopLeftToBottomRight makes the band travel diagonally from
	// the top-left corner toward the bottom-right corner.
	DirectionTopLeftToBottomRight
	// DirectionLeftToRight makes the band travel horizontally from the
	// left edge toward the right edge.
	DirectionLeftToRight
	// DirectionRightToLeft makes the band travel horizontally from the
	// right edge toward the left edge.
	DirectionRightToLeft
	// DirectionTopToBottom makes the band travel vertically from the top
	// edge toward the bottom edge.
	DirectionTopToBottom
	// DirectionBottomToTop makes the band travel vertically from the
	// bottom edge toward the top edge.
	DirectionBottomToTop
)

// Shine produces a diagonal shining effect that sweeps from the bottom left
// of the screen to the top right.
//
// The effect is applied to every cell: as the band moves over a cell its
// foreground color is interpolated towards [ShineParams.Color] giving a
// glint impression that travels across the whole screen. A glyph that
// renders as background, such as a fade block or a powerline separator,
// is left untouched: its foreground is what paints the cell, so glinting
// it would recolour scenery rather than text. Blank cells are left
// untouched too.
//
// See [ShineFrame] for a variant that only applies the effect to cells
// matching a [github.com/unstablebuild/rune-go-sdk/component.FrameCharSet].
func Shine(params ShineParams, defaultAttr term.Attributes) shader.Shader {
	return &shine{ShineParams: params, defaultAttr: defaultAttr}
}

// ShineParams allows you to customize the [Shine] effect.
type ShineParams struct {
	// Direction along which the shine band sweeps.
	Direction Direction
	// Color is the color blended into the foreground of each cell when the
	// shine band passes over it.
	Color term.Color
	// BandWidth controls how wide the shine band is, expressed as a fraction
	// of the (aspect-ratio corrected) diagonal length.
	//
	// (range 0..1 clamped, must be > 0)
	BandWidth float
	// Cycles is the number of times the shine band sweeps across the
	// animation. Values less than 1 are treated as 1.
	Cycles int
	// Intensity scales how far a cell's foreground is blended towards
	// Color at the centre of the band. Zero means full strength, so
	// existing callers are unaffected.
	//
	// (range 0..1 clamped)
	Intensity float
}

// DefaultShineParams returns a sane set of [ShineParams].
func DefaultShineParams() ShineParams {
	return ShineParams{
		Direction: DirectionBottomLeftToTopRight,
		Color:     term.NewRGBColor(255, 255, 255),
		BandWidth: 0.25,
		Cycles:    1,
	}
}

type shine struct {
	ShineParams
	defaultAttr term.Attributes
}

func (s *shine) Shade(frame, total int, in [][]term.Cell) {
	if total <= 0 || frame < 0 || frame >= total {
		return
	}
	rows := len(in)
	if rows == 0 {
		return
	}

	cols := 0
	for _, row := range in {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols == 0 {
		return
	}

	cycles := s.Cycles
	if cycles < 1 {
		cycles = 1
	}
	bandWidth := clamp(s.BandWidth, 0.0, 1.0)
	if bandWidth <= 0 {
		return
	}
	strength := 1.0
	if s.Intensity > 0 {
		strength = clamp(s.Intensity, 0.0, 1.0)
	}

	// Sweep "pulse" from -bandWidth to 1+bandWidth so the band fully enters
	// from the bottom-left corner and fully exits past the top-right corner.
	//
	// The product is rounded explicitly: arm64 fuses it with the
	// subtraction into an FMA and keeps the extra precision, which lands
	// the band edge on a different cell than amd64 does.
	pulse := fract(float(frame) / float(total) * float(cycles))
	pos := float(pulse*(1.0+2.0*bandWidth)) - bandWidth

	maxX := float(cols - 1)
	if maxX <= 0 {
		maxX = 1
	}
	maxY := float(rows-1) * asciiart.HeightToWidthCellAspectRatio
	if maxY <= 0 {
		maxY = 1
	}

	for y, row := range in {
		// y axis is flipped (terminal y=0 at top) and scaled by the cell
		// aspect ratio so the diagonal looks visually balanced.
		yNorm := (float(rows-1-y) * asciiart.HeightToWidthCellAspectRatio) / maxY
		for x, cell := range row {
			if !paintsText(cell.Ch) {
				continue
			}
			xNorm := float(x) / maxX
			t := directionT(s.Direction, xNorm, yNorm)
			intensity := smoothstep(bandWidth, 0.0, abs(t-pos)) * strength
			if intensity <= 0 {
				continue
			}
			in[y][x].Fg = shaderutils.InterpolateColor(
				intensity, cell.Fg, s.Color, s.defaultAttr.Fg,
			)
		}
	}
}

// directionT projects the cell's normalized position (xNorm in 0..1 from
// left to right, yNorm in 0..1 from bottom to top after aspect-ratio
// correction) onto the given sweep direction. The result is normalized to
// 0..1 along the sweep axis so the same pulse logic works for all
// directions.
func directionT(d Direction, xNorm, yNorm float) float {
	switch d {
	case DirectionTopLeftToBottomRight:
		return (xNorm + (1.0 - yNorm)) / 2.0
	case DirectionLeftToRight:
		return xNorm
	case DirectionRightToLeft:
		return 1.0 - xNorm
	case DirectionTopToBottom:
		return 1.0 - yNorm
	case DirectionBottomToTop:
		return yNorm
	case DirectionBottomLeftToTopRight:
		fallthrough
	default:
		return (xNorm + yNorm) / 2.0
	}
}
