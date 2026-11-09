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
	"slices"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// RadarFrame produces a rotating radar-sweep effect that orbits around the
// center of the cell matrix.
//
// The effect is applied exclusively to cells whose character matches one of
// the runes in the configured [component.FrameCharSet]; every other cell is
// left untouched. The sweep is shaped like a triangular wedge whose leading
// edge rotates clockwise. The cell's foreground is interpolated towards
// [RadarFrameParams.Color] across the wedge: cells near the center of the
// wedge receive the full [RadarFrameParams.Color] and cells near either
// edge are blended only slightly.
func RadarFrame(params RadarFrameParams, defaultAttr term.Attributes) shader.Shader {
	return &radarFrame{RadarFrameParams: params, defaultAttr: defaultAttr}
}

// RadarFrameParams allows you to customize the [RadarFrame] effect.
type RadarFrameParams struct {
	// FrameCharSet is the set of frame characters the radar effect is
	// applied to. Cells whose character is not part of this set are left
	// untouched, unless it is one of Runes.
	FrameCharSet component.FrameCharSet
	// Runes are further characters the effect applies to, such as the
	// junctions that join a frame to a divider within it.
	Runes []rune
	// Color is the color blended into the foreground of matching cells at
	// the center of the sweep wedge.
	Color term.Color
	// AngularWidth is the angular size of the radar wedge, expressed as a
	// fraction of a full revolution. The wedge peaks at its center and
	// fades symmetrically to the original foreground at both edges.
	//
	// (range 0..1 clamped, must be > 0)
	AngularWidth float
	// Cycles is the number of full revolutions the sweep completes across
	// the animation. Values less than 1 are treated as 1.
	Cycles int
}

// DefaultRadarFrameParams returns a sane set of [RadarFrameParams] using the
// given [component.FrameCharSet] as the set of cells the radar applies to.
func DefaultRadarFrameParams(fc component.FrameCharSet) RadarFrameParams {
	return RadarFrameParams{
		FrameCharSet: fc,
		Color:        term.ColorBlue,
		AngularWidth: 0.55,
		Cycles:       1,
	}
}

type radarFrame struct {
	RadarFrameParams
	defaultAttr term.Attributes
}

// ShadeImages satisfies shader.ImageShader. It shades in like Shade, the
// frame runes under pictures included, and returns images as they are:
// it only recolors text, which pictures show or hide whatever its color.
func (s *radarFrame) ShadeImages(
	frame, total int, in [][]term.Cell, images []term.Image,
) []term.Image {
	s.Shade(frame, total, in)
	return images
}

func (s *radarFrame) Shade(frame, total int, in [][]term.Cell) {
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
	wedge := clamp(s.AngularWidth, 0.0, 1.0)
	if wedge <= 0 {
		return
	}

	// Wedge center angle in [0, 1) revolutions, sweeping clockwise from
	// the positive-x axis. Using fract here means Cycles>1 just repeats
	// the sweep without any direction reversal.
	center := fract(float(frame) / float(total) * float(cycles))
	halfWedge := wedge / 2.0
	// Containment test in cos-space avoids a per-cell atan2: a cell is
	// inside the wedge iff cos(cellAngle - centerAngle) >= cos(halfWedge),
	// which expands to (cosC*dx + sinC*dy) / |d| >= edgeCos.
	cosC := math.Cos(2.0 * math.Pi * center)
	sinC := math.Sin(2.0 * math.Pi * center)
	edgeCos := math.Cos(2.0 * math.Pi * halfWedge)

	// Aspect-ratio correction so the wedge looks circular on screen
	// rather than vertically squashed. Pulse/Shine frame shaders use the
	// same constant for the same reason.
	cx := float(cols-1) / 2.0
	cy := float(rows-1) / 2.0

	for y, row := range in {
		dy := (float(y) - cy) * asciiart.HeightToWidthCellAspectRatio
		for x, cell := range row {
			if !s.isFrameChar(cell.Ch) {
				continue
			}
			dx := float(x) - cx
			if dx == 0 && dy == 0 {
				// Center cell has no defined angle; treat it as at the
				// wedge peak so it pulses with the sweep.
				in[y][x].Fg = shaderutils.InterpolateColor(
					1.0, cell.Fg, s.Color, s.defaultAttr.Fg,
				)
				continue
			}
			lenSq := dx*dx + dy*dy
			dot := cosC*dx + sinC*dy
			// Cull cells outside the wedge before paying for a sqrt.
			// When halfWedge < 0.25 rev the wedge sits entirely in the
			// half-plane facing the center, so dot <= 0 means the cell
			// is on the wrong side. dot²/lenSq < edgeCos²·sign(edgeCos)
			// then narrows to cells whose angle is within halfWedge of
			// the center. For halfWedge >= 0.25 rev (edgeCos <= 0) the
			// wedge wraps past the half-plane so this fast cull is
			// skipped and we rely on the sqrt comparison below.
			if edgeCos > 0 {
				if dot <= 0 || dot*dot < edgeCos*edgeCos*lenSq {
					continue
				}
			}
			cosA := dot / math.Sqrt(lenSq)
			if cosA <= edgeCos {
				continue
			}
			// Peak at the center of the wedge (cosA=1, intensity=1),
			// smoothly fading to 0 at either edge (cosA=edgeCos).
			intensity := smoothstep(edgeCos, 1.0, cosA)
			if intensity <= 0 {
				continue
			}
			in[y][x].Fg = shaderutils.InterpolateColor(
				intensity, cell.Fg, s.Color, s.defaultAttr.Fg,
			)
		}
	}
}

func (s *radarFrame) isFrameChar(ch rune) bool {
	if ch == 0 {
		return false
	}
	fc := s.FrameCharSet
	return ch == fc.HorizontalTop ||
		ch == fc.HorizontalBottom ||
		ch == fc.VerticalLeft ||
		ch == fc.VerticalRight ||
		ch == fc.TopLeft ||
		ch == fc.TopRight ||
		ch == fc.BottomLeft ||
		ch == fc.BottomRight ||
		slices.Contains(s.Runes, ch)
}
