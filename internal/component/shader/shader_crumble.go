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

// Crumble weakens the text and lets it crumble: in a random order that
// gathers pace, characters fade to dust and bounce down to the bottom
// of the canvas, which is then vacuumed up through the center to the
// top, before the dust drops back in place and flashes back into the
// text.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults; params out of their documented range
// panic with an error wrapping [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "crumble" effect.
func Crumble(params CrumbleParams, defaultAttr term.Attributes) Shader {
	d := DefaultCrumbleParams()
	params.DustSymbols = checkSymbols("CrumbleParams.DustSymbols", params.DustSymbols, d.DustSymbols)
	params.WeakBrightness = checkBrightness("CrumbleParams.WeakBrightness", orDefault(params.WeakBrightness, d.WeakBrightness))
	params.DustBrightness = checkBrightness("CrumbleParams.DustBrightness", orDefault(params.DustBrightness, d.DustBrightness))
	params.FlashColor = orDefault(params.FlashColor, d.FlashColor)
	params.FallEasing = checkEasing("CrumbleParams.FallEasing", params.FallEasing, d.FallEasing)
	params.VacuumEasing = checkEasing("CrumbleParams.VacuumEasing", params.VacuumEasing, d.VacuumEasing)
	params.FallDuration = checkDuration("CrumbleParams.FallDuration", orDefault(params.FallDuration, d.FallDuration))
	params.VacuumDuration = checkDuration("CrumbleParams.VacuumDuration", orDefault(params.VacuumDuration, d.VacuumDuration))
	checkPhases("CrumbleParams.FallDuration+VacuumDuration", params.FallDuration+params.VacuumDuration)
	params.Seed = orDefault(params.Seed, d.Seed)
	return &crumble{CrumbleParams: params, defaultAttr: defaultAttr}
}

// CrumbleParams configures [Crumble].
type CrumbleParams struct {
	// DustSymbols are the glyphs crumbled characters are picked from.
	DustSymbols []rune
	// WeakBrightness scales the brightness of the text before it
	// crumbles, DustBrightness that of its dust. (range (0, 1])
	WeakBrightness float64
	DustBrightness float64
	// FlashColor is the color the text flashes to as it reforms.
	FlashColor term.Color
	// FallEasing shapes the fall of the dust, VacuumEasing its ascent.
	FallEasing   shaderutils.Easing
	VacuumEasing shaderutils.Easing
	// FallDuration and VacuumDuration are the fractions of the
	// animation spent crumbling and vacuuming; reforming takes the rest.
	// Their sum must be below 1. (range (0, 1))
	FallDuration   float64
	VacuumDuration float64
	// Seed picks the orders and the dust. Defaults to a fixed value if
	// zero.
	Seed int64
}

// DefaultCrumbleParams returns the terminaltexteffects defaults.
func DefaultCrumbleParams() CrumbleParams {
	return CrumbleParams{
		DustSymbols:    []rune{'*', '.', ','},
		WeakBrightness: 0.65,
		DustBrightness: 0.55,
		FlashColor:     term.NewRGBColor(255, 255, 255),
		FallEasing:     shaderutils.EaseOutBounce,
		VacuumEasing:   shaderutils.EaseOutQuint,
		FallDuration:   0.5,
		VacuumDuration: 0.2,
		Seed:           1,
	}
}

type crumble struct {
	CrumbleParams
	defaultAttr term.Attributes
}

func (s *crumble) Shade(frame, total int, cells [][]term.Cell) {
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
	fallRanks := shuffledRanks(s.Seed, 1, glyphs)
	vacuumRanks := shuffledRanks(s.Seed, 2, glyphs)
	n := float64(len(glyphs))
	bottom := float64(rows - 1)
	cx, cy := float64(cols-1)/2, float64(rows-1)/2
	// Each character weakens, then falls; the fall of the last one must
	// end with the phase.
	weakenDur, fallDur := s.FallDuration*0.1, s.FallDuration*0.3
	fallWindow := s.FallDuration - weakenDur - fallDur
	vacuumStart := s.FallDuration
	vacuumDur := s.VacuumDuration * 0.5
	reformStart := vacuumStart + s.VacuumDuration
	reformDur := 1 - reformStart
	dropDur, flashDur := reformDur*0.4, reformDur*0.25
	fadeDur := reformDur - dropDur - flashDur

	dust := func(g glyph, step int) rune {
		return s.DustSymbols[pick3(s.Seed, g.x, g.y, 10+step, len(s.DustSymbols))]
	}
	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		gx, gy := float64(g.x), float64(g.y)
		fg := resolveFg(g.cell.Fg, s.defaultAttr.Fg)
		weak := shaderutils.AdjustBrightness(fg, s.WeakBrightness, s.defaultAttr.Fg)
		dustFg := shaderutils.AdjustBrightness(fg, s.DustBrightness, s.defaultAttr.Fg)
		switch {
		case t < vacuumStart:
			// Crumbling gathers pace, so the ranks' square root spreads
			// the starts densest at the end.
			start := math.Sqrt(float64(fallRanks[i])/n) * fallWindow
			weaken := window(t, start, weakenDur)
			fall := window(t, start+weakenDur, fallDur*(bottom-gy)/max(1, bottom))
			if t < start+weakenDur {
				places[i] = placement{
					x: g.x, y: g.y,
					fg: shaderutils.InterpolateColor(weaken, weak, dustFg, s.defaultAttr.Fg),
				}
				continue
			}
			places[i] = placement{
				x:  g.x,
				y:  roundInt(lerp(gy, bottom, s.FallEasing(fall))),
				ch: dust(g, int(fall*4.99)),
				fg: dustFg,
			}
		case t < reformStart:
			start := vacuumStart + float64(vacuumRanks[i])/n*(s.VacuumDuration-vacuumDur)
			p := s.VacuumEasing(window(t, start, vacuumDur))
			// The dust is sucked up along a curve bent through the
			// center of the canvas.
			x := (1-p)*(1-p)*gx + 2*(1-p)*p*cx + p*p*gx
			y := (1-p)*(1-p)*bottom + 2*(1-p)*p*cy
			places[i] = placement{
				x: roundInt(x), y: roundInt(y),
				ch: dust(g, 4), fg: dustFg,
			}
		default:
			drop := window(t, reformStart, dropDur*gy/max(1, bottom))
			if drop < 1 {
				places[i] = placement{
					x: g.x, y: roundInt(lerp(0, gy, drop)),
					ch: dust(g, 4), fg: dustFg,
				}
				continue
			}
			flashStart := reformStart + dropDur
			if flash := window(t, flashStart, flashDur); flash < 1 {
				places[i] = placement{
					x: g.x, y: g.y,
					fg: shaderutils.InterpolateColor(flash, fg, s.FlashColor, s.defaultAttr.Fg),
				}
				continue
			}
			fade := window(t, flashStart+flashDur, fadeDur)
			places[i] = placement{
				x: g.x, y: g.y,
				fg: fadeFg(fade, s.FlashColor, g.cell.Fg, s.defaultAttr.Fg),
			}
		}
	}
	place(cells, glyphs, places, s.defaultAttr.Bg)
}
