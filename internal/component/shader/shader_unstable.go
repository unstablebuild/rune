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
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// Unstable shows the text with its characters shuffled among their
// places, rumbling ever faster as they heat up to UnstableColor, until
// it blows apart to the edges of the canvas, hangs there, then
// reassembles in place as the characters cool down to their own color.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults, except where noted; params out of
// their documented range panic with an error wrapping
// [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "unstable" effect.
func Unstable(params UnstableParams, defaultAttr term.Attributes) Shader {
	d := DefaultUnstableParams()
	params.UnstableColor = orDefault(params.UnstableColor, d.UnstableColor)
	params.ExplosionEasing = checkEasing("UnstableParams.ExplosionEasing", params.ExplosionEasing, d.ExplosionEasing)
	params.ReassemblyEasing = checkEasing("UnstableParams.ReassemblyEasing", params.ReassemblyEasing, d.ReassemblyEasing)
	params.RumbleDuration = checkDuration("UnstableParams.RumbleDuration", orDefault(params.RumbleDuration, d.RumbleDuration))
	params.ExplosionDuration = checkDuration("UnstableParams.ExplosionDuration",
		orDefault(params.ExplosionDuration, d.ExplosionDuration))
	checkFloat("UnstableParams.HoldDuration", params.HoldDuration, 0, 1, false, true)
	checkPhases("UnstableParams.RumbleDuration+ExplosionDuration+HoldDuration",
		params.RumbleDuration+params.ExplosionDuration+params.HoldDuration)
	params.Seed = orDefault(params.Seed, d.Seed)
	return &unstable{UnstableParams: params, defaultAttr: defaultAttr}
}

// UnstableParams configures [Unstable].
type UnstableParams struct {
	// UnstableColor is the color the characters heat up to.
	UnstableColor term.Color
	// ExplosionEasing shapes the flight to the edges, ReassemblyEasing
	// the flight back.
	ExplosionEasing  shaderutils.Easing
	ReassemblyEasing shaderutils.Easing
	// RumbleDuration, ExplosionDuration and HoldDuration are the
	// fractions of the animation spent rumbling, blowing apart and
	// hanging at the edges; reassembly takes the rest, so their sum
	// must be below 1. A zero HoldDuration does not hang.
	// (ranges (0, 1), (0, 1) and [0, 1))
	RumbleDuration    float64
	ExplosionDuration float64
	HoldDuration      float64
	// Seed picks the shuffle, the rumble and where characters fly to.
	// Defaults to a fixed value if zero.
	Seed int64
}

// DefaultUnstableParams returns the terminaltexteffects defaults: an
// orange unstable color and out expo easings.
func DefaultUnstableParams() UnstableParams {
	return UnstableParams{
		UnstableColor:     term.NewRGBColor(0xff, 0x92, 0x00),
		ExplosionEasing:   shaderutils.EaseOutExpo,
		ReassemblyEasing:  shaderutils.EaseOutExpo,
		RumbleDuration:    0.4,
		ExplosionDuration: 0.15,
		HoldDuration:      0.1,
		Seed:              1,
	}
}

type unstable struct {
	UnstableParams
	defaultAttr term.Attributes
}

func (s *unstable) Shade(frame, total int, cells [][]term.Cell) {
	t, ok := progress(frame, total)
	if !ok {
		return
	}
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return
	}
	glyphs := collectContent(cells, s.defaultAttr.Bg)
	if len(glyphs) == 0 {
		return
	}
	// Shuffling along one cycle of a random order moves every character
	// to another's place.
	ranks := shuffledRanks(s.Seed, 1, glyphs)
	order := make([]int, len(glyphs))
	for i, rank := range ranks {
		order[rank] = i
	}
	explosionStart := s.RumbleDuration
	holdStart := explosionStart + s.ExplosionDuration
	reassemblyStart := holdStart + s.HoldDuration
	reassemblyDur := 1 - reassemblyStart
	span := max(1, cellDistance(0, 0, float64(cols-1), float64(rows-1)))

	// The rumble shakes the text for a third of each of its ticks,
	// which come ever faster.
	jx, jy := 0, 0
	if u := window(t, 0, s.RumbleDuration); u > 0.2 && t < explosionStart {
		ramp := (u - 0.2) / 0.8
		ticks := ramp * ramp * 40
		if tick := int(ticks); ticks-float64(tick) < 0.3 {
			jx = pick3(s.Seed, tick, 0, 1, 3) - 1
			jy = pick3(s.Seed, tick, 0, 2, 3) - 1
		}
	}

	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		jumbled := glyphs[order[(ranks[i]+1)%len(glyphs)]]
		jumbledX, jumbledY := float64(jumbled.x), float64(jumbled.y)
		edgeX, edgeY := edgeCoord(s.Seed, g.x, g.y, rows, cols)
		gx, gy := float64(g.x), float64(g.y)
		fg := resolveFg(g.cell.Fg, s.defaultAttr.Fg)
		switch {
		case t < explosionStart:
			heat := clamp01(window(t, 0, s.RumbleDuration) * 1.25)
			places[i] = placement{
				x:  jumbled.x + jx,
				y:  jumbled.y + jy,
				fg: shaderutils.InterpolateColor(heat, fg, s.UnstableColor, s.defaultAttr.Fg),
			}
		case t < reassemblyStart:
			dur := s.ExplosionDuration * cellDistance(jumbledX, jumbledY, edgeX, edgeY) / span
			e := s.ExplosionEasing(window(t, explosionStart, dur))
			places[i] = placement{
				x:  roundInt(lerp(jumbledX, edgeX, e)),
				y:  roundInt(lerp(jumbledY, edgeY, e)),
				fg: s.UnstableColor,
			}
		default:
			dur := reassemblyDur * cellDistance(edgeX, edgeY, gx, gy) / span
			p := window(t, reassemblyStart, dur)
			e := s.ReassemblyEasing(p)
			places[i] = placement{
				x:  roundInt(lerp(edgeX, gx, e)),
				y:  roundInt(lerp(edgeY, gy, e)),
				fg: fadeFg(window(t, reassemblyStart, reassemblyDur*0.6), s.UnstableColor, g.cell.Fg, s.defaultAttr.Fg),
			}
		}
	}
	place(cells, glyphs, places, s.defaultAttr.Bg)
}

// edgeCoord returns a cell on one of the four edges of a rows by cols
// canvas, picked by seed for the character at gx, gy.
func edgeCoord(seed int64, gx, gy, rows, cols int) (x, y float64) {
	switch pick3(seed, gx, gy, 1, 4) {
	case 0:
		return 0, float64(pick3(seed, gx, gy, 2, rows))
	case 1:
		return float64(cols - 1), float64(pick3(seed, gx, gy, 2, rows))
	case 2:
		return float64(pick3(seed, gx, gy, 2, cols)), float64(rows - 1)
	default:
		return float64(pick3(seed, gx, gy, 2, cols)), 0
	}
}
