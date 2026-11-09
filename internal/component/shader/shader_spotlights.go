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

// Spotlights dims the text to DarkBrightness and sweeps SpotlightCount
// spotlights over it, each lighting the characters within its beam at
// their own brightness, fading off at the edge. The spotlights spread
// out of a dark point at the center of the canvas, and once the search
// is over they gather back into it, dimming as they concentrate.
//
// The text never lights up again: the last frame leads back into the
// first, so that the search plays on a loop without a seam. It is up to
// the caller to fade it in from the text and out to it.
//
// Zero params take their defaults, except where noted; params out of
// their documented range panic with an error wrapping
// [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "spotlights" effect.
func Spotlights(params SpotlightsParams, defaultAttr term.Attributes) Shader {
	d := DefaultSpotlightsParams()
	params.BeamWidthRatio = capPositive("SpotlightsParams.BeamWidthRatio",
		orDefault(params.BeamWidthRatio, d.BeamWidthRatio), 1, 1000)
	checkFloat("SpotlightsParams.BeamFalloff", params.BeamFalloff, 0, 1, false, true)
	params.SpotlightCount = capCount("SpotlightsParams.SpotlightCount",
		orDefault(params.SpotlightCount, d.SpotlightCount), 1, 32)
	params.SearchDuration = checkDuration("SpotlightsParams.SearchDuration", orDefault(params.SearchDuration, d.SearchDuration))
	params.DarkBrightness = checkBrightness("SpotlightsParams.DarkBrightness", orDefault(params.DarkBrightness, d.DarkBrightness))
	params.Seed = orDefault(params.Seed, d.Seed)
	return &spotlights{SpotlightsParams: params, defaultAttr: defaultAttr}
}

// SpotlightsParams configures [Spotlights].
type SpotlightsParams struct {
	// BeamWidthRatio divides the smallest canvas dimension to size the
	// beams: higher ratios make narrower beams. (range (0, ∞), capped
	// to [1, 1000])
	BeamWidthRatio float64
	// BeamFalloff is the fraction of a beam's radius over which its
	// light fades off at the edge; zero cuts it off sharply.
	// (range [0, 1))
	BeamFalloff float64
	// SpotlightCount is how many spotlights search the canvas.
	// (range [1, ∞), capped at 32)
	SpotlightCount int
	// SearchDuration is the fraction of the animation the spotlights
	// search for; spreading out of the center and gathering back into
	// it take half of the rest each. (range (0, 1))
	SearchDuration float64
	// DarkBrightness scales the brightness of the text outside the
	// beams. (range (0, 1])
	DarkBrightness float64
	// Seed picks the spotlights' tours. Defaults to a fixed value if
	// zero.
	Seed int64
}

// DefaultSpotlightsParams returns the terminaltexteffects defaults.
func DefaultSpotlightsParams() SpotlightsParams {
	return SpotlightsParams{
		BeamWidthRatio: 2,
		BeamFalloff:    0.3,
		SpotlightCount: 3,
		SearchDuration: 0.7,
		DarkBrightness: 0.2,
		Seed:           1,
	}
}

type spotlights struct {
	SpotlightsParams
	defaultAttr term.Attributes
}

// spotlightStops is how many places a spotlight visits during its
// search.
const spotlightStops = 11

func (s *spotlights) Shade(frame, total int, cells [][]term.Cell) {
	if total <= 0 || frame >= total {
		return
	}
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return
	}
	// Frame total, which would follow the last, is frame 0 again.
	t := float64(max(0, frame)) / float64(total)
	cx, cy := float64(cols-1)/2, float64(rows-1)/2
	smallest := float64(min(rows, cols))
	spread := (1 - s.SearchDuration) / 2
	searchEnd := spread + s.SearchDuration
	// out is how far the spotlights are out of the dark point at the
	// center: none at the first frame, all of the way while searching.
	out := 1.0
	switch {
	case t < spread:
		out = shaderutils.EaseInOutSine(window(t, 0, spread))
	case t >= searchEnd:
		out = 1 - shaderutils.EaseInOutSine(window(t, searchEnd, spread))
	}
	radius := lerp(1, max(1, math.Floor(min(smallest/s.BeamWidthRatio, smallest))), out)
	search := window(t, spread, s.SearchDuration)
	centers := make([][2]float64, s.SpotlightCount)
	for i := range centers {
		x, y := s.position(search, i, rows, cols)
		centers[i] = [2]float64{lerp(cx, x, out), lerp(cy, y, out)}
	}
	for y, row := range cells {
		for x, cell := range row {
			if !hasOriginalChar(cell) {
				continue
			}
			nearest := math.Inf(1)
			for _, c := range centers {
				nearest = min(nearest, cellDistance(c[0], c[1], float64(x), float64(y)))
			}
			lit := radius * (1 - s.BeamFalloff)
			factor := 1.0
			switch {
			case nearest > radius:
				factor = s.DarkBrightness
			case nearest > lit:
				factor = max(1-(nearest-lit)/(radius-lit), s.DarkBrightness)
			}
			factor = lerp(s.DarkBrightness, factor, out)
			if factor < 1 {
				cells[y][x].Fg = shaderutils.AdjustBrightness(cell.Fg, factor, s.defaultAttr.Fg)
			}
		}
	}
}

// position returns where spotlight i is at fraction t of its search: it
// tours random places at least a quarter of the canvas apart, curving
// along the way, at its own speed on each leg.
func (s *spotlights) position(t float64, i, rows, cols int) (x, y float64) {
	var stops [spotlightStops + 1][2]float64
	stops[0] = [2]float64{float64(pick3(s.Seed, i, 0, 1, cols)), float64(pick3(s.Seed, i, 0, 2, rows))}
	minDist := float64(cols / 4)
	for k := 1; k <= spotlightStops; k++ {
		// Try a few places for one far enough from the last stop.
		for try := range 10 {
			x = float64(pick3(s.Seed, i, k*16+try, 1, cols))
			y = float64(pick3(s.Seed, i, k*16+try, 2, rows))
			if cellDistance(stops[k-1][0], stops[k-1][1], x, y) >= minDist {
				break
			}
		}
		stops[k] = [2]float64{x, y}
	}
	var legs [spotlightStops]float64
	tour := 0.0
	for k := range legs {
		speed := lerp(0.35, 0.75, unit3(s.Seed, i, k, 3))
		legs[k] = cellDistance(stops[k][0], stops[k][1], stops[k+1][0], stops[k+1][1]) / speed
		tour += legs[k]
	}
	if tour <= 0 {
		return stops[0][0], stops[0][1]
	}
	elapsed := t * tour
	for k, leg := range legs {
		if elapsed > leg && k < len(legs)-1 {
			elapsed -= leg
			continue
		}
		p := shaderutils.EaseInOutQuad(clamp01(elapsed / max(leg, 1e-9)))
		bx, by := outsideCoord(s.Seed, 2000+i*spotlightStops+k, rows, cols)
		from, to := stops[k], stops[k+1]
		x = (1-p)*(1-p)*from[0] + 2*(1-p)*p*float64(bx) + p*p*to[0]
		y = (1-p)*(1-p)*from[1] + 2*(1-p)*p*float64(by) + p*p*to[1]
		return x, y
	}
	return stops[spotlightStops][0], stops[spotlightStops][1]
}
