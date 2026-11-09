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
	"fmt"
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// Rings gathers the characters into concentric rings around the center
// of the canvas, which alternately disperse into a loose cloud around
// each ring and condense back to spin, each ring in its own color and
// direction, before the characters fly home. Characters that do not fit
// a ring leave the canvas and return for the finale.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults, except where noted; params out of
// their documented range panic with an error wrapping
// [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "rings" effect.
func Rings(params RingsParams, defaultAttr term.Attributes) Shader {
	d := DefaultRingsParams()
	params.RingColors = orDefaults(params.RingColors, d.RingColors)
	params.RingGap = checkFloat("RingsParams.RingGap", orDefault(params.RingGap, d.RingGap), 0, 1, true, false)
	checkFloat("RingsParams.HoldDuration", params.HoldDuration, 0, 1, false, true)
	params.DisperseDuration = checkDuration("RingsParams.DisperseDuration",
		orDefault(params.DisperseDuration, d.DisperseDuration))
	params.SpinDuration = checkDuration("RingsParams.SpinDuration", orDefault(params.SpinDuration, d.SpinDuration))
	params.Cycles = capCount("RingsParams.Cycles", orDefault(params.Cycles, d.Cycles), 1, math.MaxInt)
	checkPhases("RingsParams.HoldDuration+Cycles*(DisperseDuration+SpinDuration)",
		params.HoldDuration+float64(params.Cycles)*(params.DisperseDuration+params.SpinDuration))
	params.MinSpin = capPositive("RingsParams.MinSpin", orDefault(params.MinSpin, d.MinSpin), 0, 1e4)
	params.MaxSpin = capPositive("RingsParams.MaxSpin", orDefault(params.MaxSpin, d.MaxSpin), 0, 1e4)
	if params.MaxSpin < params.MinSpin {
		invalidParam("RingsParams.MaxSpin", params.MaxSpin, fmt.Sprintf("at least MinSpin, %v", params.MinSpin))
	}
	params.Seed = orDefault(params.Seed, d.Seed)
	return &rings{RingsParams: params, defaultAttr: defaultAttr}
}

// RingsParams configures [Rings].
type RingsParams struct {
	// RingColors are cycled through the rings, innermost first.
	RingColors []term.Color
	// RingGap is the space between rings as a fraction of the shorter
	// canvas side. (range (0, 1])
	RingGap float64
	// HoldDuration is the fraction of the animation the text stays put
	// before the rings form. DisperseDuration and SpinDuration are the
	// fractions each disperse and spin phase lasts, repeated Cycles
	// times; the flight home takes the rest, so the sum of all phases
	// must be below 1. A zero HoldDuration does not hold.
	// (ranges [0, 1), (0, 1), (0, 1) and [1, ∞))
	HoldDuration     float64
	DisperseDuration float64
	SpinDuration     float64
	Cycles           int
	// MinSpin and MaxSpin bound how far, in cells, a character travels
	// along its ring during a spin phase; each ring draws its own speed
	// in between, so inner rings turn faster. MaxSpin must be at least
	// MinSpin. (range (0, ∞), capped at 1e4)
	MinSpin float64
	MaxSpin float64
	// Seed picks which characters join which ring, the spin speeds and
	// the dispersal. Defaults to a fixed value if zero.
	Seed int64
}

// DefaultRingsParams returns the terminaltexteffects defaults: purple,
// pink and cream rings, a 10% gap and three spin cycles.
func DefaultRingsParams() RingsParams {
	return RingsParams{
		RingColors: []term.Color{
			term.NewRGBColor(0xab, 0x48, 0xff),
			term.NewRGBColor(0xe7, 0xb2, 0xb2),
			term.NewRGBColor(0xff, 0xfe, 0xbd),
		},
		RingGap:          0.1,
		HoldDuration:     0.06,
		DisperseDuration: 0.14,
		SpinDuration:     0.14,
		Cycles:           3,
		MinSpin:          50,
		MaxSpin:          200,
		Seed:             1,
	}
}

type rings struct {
	RingsParams
	defaultAttr term.Attributes
}

// ring is one of the circles the characters gather on.
type ring struct {
	radius float64
	// angles are the slots on the ring, in radians.
	angles []float64
	// spin is the angle, in radians, the ring turns during a spin phase,
	// signed by its direction.
	spin  float64
	color term.Color
}

func (s *rings) Shade(frame, total int, cells [][]term.Cell) {
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
	cx, cy := float64(cols-1)/2, float64(rows-1)/2
	ringList := s.rings(rows, cols, cx, cy)
	ranks := shuffledRanks(s.Seed, 1, glyphs)
	gap := max(roundInt(float64(min(rows, cols))*s.RingGap), 1)
	span := max(1, cellDistance(0, 0, float64(cols), float64(rows)))

	holdEnd := s.HoldDuration
	cycleLen := s.DisperseDuration + s.SpinDuration
	finalStart := holdEnd + float64(s.Cycles)*cycleLen

	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		gx, gy := float64(g.x), float64(g.y)
		fg := resolveFg(g.cell.Fg, s.defaultAttr.Fg)
		places[i] = placement{x: g.x, y: g.y, fg: g.cell.Fg}
		if t < holdEnd {
			continue
		}
		r, slot := ringSlot(ringList, ranks[i])
		if r < 0 {
			ox, oy := outsideCoord(s.Seed, g.y*cols+g.x, rows, cols)
			ox64, oy64 := float64(ox), float64(oy)
			if t >= finalStart {
				e := shaderutils.EaseOutQuad(window(t, finalStart, (1-finalStart)*math.Min(1, cellDistance(ox64, oy64, gx, gy)/span)))
				places[i].x, places[i].y = roundInt(lerp(ox64, gx, e)), roundInt(lerp(oy64, gy, e))
				continue
			}
			dur := s.DisperseDuration * math.Max(0.1, math.Min(1, cellDistance(gx, gy, ox64, oy64)/span))
			u := window(t, holdEnd, dur)
			if u >= 1 {
				places[i].hidden = true
				continue
			}
			e := shaderutils.EaseOutSine(u)
			places[i].x, places[i].y = roundInt(lerp(gx, ox64, e)), roundInt(lerp(gy, oy64, e))
			continue
		}
		rg := ringList[r]
		anchor := func(cycle int) (x, y float64) {
			return ellipsePoint(cx, cy, rg.radius, rg.angles[slot]+rg.spin*float64(cycle))
		}
		// The dispersal wanders among five random spots within a ring
		// gap of where the character sits on the ring.
		waypoint := func(cycle, w int) (x, y float64) {
			ax, ay := anchor(cycle)
			salt := cycle*16 + w*2
			return ax + float64(pick3(s.Seed, g.x, g.y, 100+salt, 2*gap+1)-gap),
				ay + float64(pick3(s.Seed, g.x, g.y, 101+salt, 2*gap+1)-gap)
		}
		if t >= finalStart {
			ax, ay := anchor(s.Cycles)
			u := window(t, finalStart, 1-finalStart)
			e := shaderutils.EaseOutQuad(window(u, 0, math.Min(1, cellDistance(ax, ay, gx, gy)/span)))
			places[i].x, places[i].y = roundInt(lerp(ax, gx, e)), roundInt(lerp(ay, gy, e))
			places[i].fg = fadeFg(window(u, 0, 0.6), rg.color, g.cell.Fg, s.defaultAttr.Fg)
			continue
		}
		cycle := min(int((t-holdEnd)/cycleLen), s.Cycles-1)
		cycleStart := holdEnd + float64(cycle)*cycleLen
		var x, y float64
		if t < cycleStart+s.DisperseDuration {
			u := window(t, cycleStart, s.DisperseDuration)
			px, py := anchor(cycle)
			if cycle == 0 {
				px, py = gx, gy
			}
			if u < 0.25 {
				wx, wy := waypoint(cycle, 0)
				e := shaderutils.EaseOutCubic(u / 0.25)
				x, y = lerp(px, wx, e), lerp(py, wy, e)
			} else {
				v := (u - 0.25) / 0.75 * 4
				seg := min(int(v), 3)
				x0, y0 := waypoint(cycle, seg)
				x1, y1 := waypoint(cycle, seg+1)
				x, y = lerp(x0, x1, v-float64(seg)), lerp(y0, y1, v-float64(seg))
			}
			places[i].fg = fadeFg(window(u, 0, 0.4), rg.color, g.cell.Fg, s.defaultAttr.Fg)
		} else {
			u := window(t, cycleStart+s.DisperseDuration, s.SpinDuration)
			if u < 0.2 {
				wx, wy := waypoint(cycle, 4)
				ax, ay := anchor(cycle)
				x, y = lerp(wx, ax, u/0.2), lerp(wy, ay, u/0.2)
				places[i].fg = shaderutils.InterpolateColor(window(u, 0, 0.12), fg, rg.color, s.defaultAttr.Fg)
			} else {
				x, y = ellipsePoint(cx, cy, rg.radius, rg.angles[slot]+rg.spin*(float64(cycle)+(u-0.2)/0.8))
				places[i].fg = rg.color
			}
		}
		places[i].x, places[i].y = roundInt(x), roundInt(y)
	}
	place(cells, glyphs, places, s.defaultAttr.Bg)
}

// rings lays out the rings that fit a rows by cols canvas centered at
// cx, cy, innermost first, stopping at the first with less than a
// quarter of its slots on the canvas.
func (s *rings) rings(rows, cols int, cx, cy float64) []ring {
	gap := max(roundInt(float64(min(rows, cols))*s.RingGap), 1)
	var out []ring
	for scale := 1; scale < max(rows, cols); scale += gap {
		radius := float64(scale) * rowScale
		limit := 7 * scale
		seen := make(map[[2]int]bool, limit)
		var angles []float64
		inside := 0
		for i := range limit {
			angle := 2 * math.Pi * float64(i) / float64(limit)
			x, y := ellipsePoint(cx, cy, radius, angle)
			key := [2]int{roundInt(x), roundInt(y)}
			if seen[key] {
				continue
			}
			seen[key] = true
			angles = append(angles, angle)
			if key[0] >= 0 && key[0] < cols && key[1] >= 0 && key[1] < rows {
				inside++
			}
		}
		if float64(inside)/float64(len(angles)) < 0.25 {
			break
		}
		n := len(out)
		spin := lerp(s.MinSpin, s.MaxSpin, unit3(s.Seed, n, 0, 7)) / radius
		if n%2 == 1 {
			spin = -spin
		}
		out = append(out, ring{
			radius: radius,
			angles: angles,
			spin:   spin,
			color:  s.RingColors[n%len(s.RingColors)],
		})
	}
	return out
}

// ringSlot returns the ring and the slot on it that the character of
// rank gets, filling rings innermost first, or -1 when the rings are
// full before rank.
func ringSlot(rings []ring, rank int) (ring, slot int) {
	for i, r := range rings {
		if rank < len(r.angles) {
			return i, rank
		}
		rank -= len(r.angles)
	}
	return -1, 0
}
