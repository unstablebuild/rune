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

// Thunderstorm darkens the text to StormBrightness and lets a storm
// loose over it: RaindropSymbols stream down diagonally, and bolts of
// LightningColor fork down the canvas, flashing the text, scattering
// SparkSymbols where they strike and leaving the characters they hit
// glowing GlowColor.
//
// The storm never lets up: its last frame leads back into its first, so
// that it plays on a loop without a seam. It is up to the caller to fade
// it in from the text and out to it.
//
// Zero params take their defaults, except where noted; params out of
// their documented range panic with an error wrapping
// [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "thunderstorm" effect.
func Thunderstorm(params ThunderstormParams, defaultAttr term.Attributes) Shader {
	d := DefaultThunderstormParams()
	params.LightningColor = orDefault(params.LightningColor, d.LightningColor)
	params.GlowColor = orDefault(params.GlowColor, d.GlowColor)
	params.RaindropSymbols = checkSymbols("ThunderstormParams.RaindropSymbols", params.RaindropSymbols, d.RaindropSymbols)
	params.RaindropColor = orDefault(params.RaindropColor, d.RaindropColor)
	params.SparkSymbols = checkSymbols("ThunderstormParams.SparkSymbols", params.SparkSymbols, d.SparkSymbols)
	params.SparkColor = orDefault(params.SparkColor, d.SparkColor)
	params.StormBrightness = checkBrightness("ThunderstormParams.StormBrightness",
		orDefault(params.StormBrightness, d.StormBrightness))
	params.StrikeCount = capCount("ThunderstormParams.StrikeCount", params.StrikeCount, 0, 64)
	params.StrikeDuration = checkDuration("ThunderstormParams.StrikeDuration", orDefault(params.StrikeDuration, d.StrikeDuration))
	params.RainDensity = min(checkFloat("ThunderstormParams.RainDensity",
		params.RainDensity, 0, math.MaxFloat64, false, false), 4)
	params.Seed = orDefault(params.Seed, d.Seed)
	return &thunderstorm{ThunderstormParams: params, defaultAttr: defaultAttr}
}

// ThunderstormParams configures [Thunderstorm].
type ThunderstormParams struct {
	// LightningColor is the color of the bolts.
	LightningColor term.Color
	// GlowColor is the color characters hit by a bolt glow in.
	GlowColor term.Color
	// RaindropSymbols are the glyphs rain is picked from, colored
	// RaindropColor.
	RaindropSymbols []rune
	RaindropColor   term.Color
	// SparkSymbols are the glyphs sparks are picked from, colored
	// SparkColor as they fly.
	SparkSymbols []rune
	SparkColor   term.Color
	// StormBrightness scales the brightness of the text during the
	// storm. (range (0, 1])
	StormBrightness float64
	// StrikeCount is how many bolts strike during the storm; zero
	// strikes none. (range [0, ∞), capped at 64)
	StrikeCount int
	// StrikeDuration is the fraction of the animation a strike lasts,
	// from the bolt forking down to the glow it leaves fading. Every
	// strike is over by the end of the animation. (range (0, 1))
	StrikeDuration float64
	// RainDensity is how many raindrops fall at once for each column
	// and row of the canvas; zero does not rain. (range [0, ∞), capped
	// at 4)
	RainDensity float64
	// Seed picks the rain, the bolts and the sparks. Defaults to a
	// fixed value if zero.
	Seed int64
}

// DefaultThunderstormParams returns the terminaltexteffects defaults.
func DefaultThunderstormParams() ThunderstormParams {
	return ThunderstormParams{
		LightningColor:  term.NewRGBColor(0x68, 0xA3, 0xE8),
		GlowColor:       term.NewRGBColor(0xEF, 0x54, 0x11),
		RaindropSymbols: []rune{'\\', '.', ','},
		RaindropColor:   term.NewRGBColor(0xaa, 0xaa, 0xff),
		SparkSymbols:    []rune{'*', '.', '\''},
		SparkColor:      term.NewRGBColor(0xff, 0x4d, 0x00),
		StormBrightness: 0.5,
		StrikeCount:     4,
		StrikeDuration:  0.12,
		RainDensity:     0.3,
		Seed:            1,
	}
}

type thunderstorm struct {
	ThunderstormParams
	defaultAttr term.Attributes
}

// boltCell is a cell of a lightning bolt.
type boltCell struct {
	x, y int
	ch   rune
}

func (s *thunderstorm) Shade(frame, total int, cells [][]term.Cell) {
	if total <= 0 || frame >= total {
		return
	}
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return
	}
	frame = max(0, frame)
	// Frame total, which would follow the last, is frame 0 again.
	t := float64(frame) / float64(total)
	// A strike in its flash lights the whole text up.
	flash := 0.0
	strike, strikeAt, striking := s.activeStrike(t)
	if striking && strikeAt >= 0.3 && strikeAt < 0.65 {
		flash = 1 - math.Abs((strikeAt-0.3)/0.35*2-1)
	}
	for y, row := range cells {
		for x, cell := range row {
			if !hasOriginalChar(cell) {
				continue
			}
			fg := resolveFg(cell.Fg, s.defaultAttr.Fg)
			color := shaderutils.AdjustBrightness(fg, s.StormBrightness, s.defaultAttr.Fg)
			if flash > 0 {
				bright := shaderutils.AdjustBrightness(fg, 1.7, s.defaultAttr.Fg)
				color = shaderutils.InterpolateColor(flash, color, bright, s.defaultAttr.Fg)
			}
			cells[y][x].Fg = color
		}
	}
	s.rain(frame, total, rows, cols, cells)
	if !striking {
		return
	}
	bolt := s.bolt(strike, rows, cols)
	bg := resolveFg(s.defaultAttr.Bg, term.NewRGBColor(0, 0, 0))
	switch {
	case strikeAt < 0.3:
		// The bolt forks down.
		reach := strikeAt / 0.3 * float64(rows)
		for _, c := range bolt {
			if float64(c.y) <= reach {
				putChar(cells, c.x, c.y, c.ch, s.LightningColor)
			}
		}
	case strikeAt < 0.65:
		p := 1 - math.Abs((strikeAt-0.3)/0.35*2-1)
		bright := shaderutils.AdjustBrightness(s.LightningColor, 1.7, s.defaultAttr.Fg)
		for _, c := range bolt {
			putChar(cells, c.x, c.y, c.ch, shaderutils.InterpolateColor(p, s.LightningColor, bright, s.defaultAttr.Fg))
		}
	case strikeAt < 0.75:
		p := (strikeAt - 0.65) / 0.1
		for _, c := range bolt {
			putChar(cells, c.x, c.y, c.ch, shaderutils.InterpolateColor(p, s.LightningColor, bg, s.defaultAttr.Fg))
		}
	default:
		// The characters the bolt hit glow.
		p := (strikeAt - 0.75) / 0.25
		for _, c := range bolt {
			if !inCanvas(cells, c.x, c.y) || !hasOriginalChar(cells[c.y][c.x]) {
				continue
			}
			fg := resolveFg(cells[c.y][c.x].Fg, s.defaultAttr.Fg)
			storm := shaderutils.AdjustBrightness(fg, s.StormBrightness, s.defaultAttr.Fg)
			cells[c.y][c.x].Fg = shaderutils.InterpolateColor(p, s.GlowColor, storm, s.defaultAttr.Fg)
		}
	}
	if strikeAt >= 0.3 && len(bolt) > 0 {
		s.sparks(strike, (strikeAt-0.3)/0.7, bolt[len(bolt)-1], rows, cells, bg)
	}
}

// activeStrike returns the strike under way at time t, how far along
// it is, and whether there is one. Strikes are spread over the storm,
// each at a random moment of its share of it, and are over by its end.
func (s *thunderstorm) activeStrike(t float64) (strike int, at float64, ok bool) {
	storm := 1 - s.StrikeDuration
	share := storm / float64(max(1, s.StrikeCount))
	for j := range s.StrikeCount {
		start := (float64(j) + unit3(s.Seed, j, 0, 500)) * share
		if t >= start && t < start+s.StrikeDuration {
			return j, (t - start) / s.StrikeDuration, true
		}
	}
	return 0, 0, false
}

// bolt returns the cells of a strike's bolt, top to bottom, the main
// bolt first, then its branches: each cell steps down a row, veering
// a column the way its symbol slants.
func (s *thunderstorm) bolt(strike, rows, cols int) []boltCell {
	var out []boltCell
	symbols := [3]rune{'\\', '/', '|'}
	var fork func(x, y, branch int, from rune)
	fork = func(x, y, branch int, from rune) {
		chance := 0.05
		for ; y < rows; y++ {
			var ch rune
			switch {
			case from == '/':
				x++
				ch = symbols[pick3(s.Seed, strike, y, 600+branch*2, 2)*2]
			case from == '\\':
				x--
				ch = symbols[1+pick3(s.Seed, strike, y, 600+branch*2, 2)]
			case from != 0:
				if pick3(s.Seed, strike, y, 600+branch*2, 2) == 0 {
					x, ch = x+1, '\\'
				} else {
					x, ch = x-1, '/'
				}
			default:
				ch = symbols[pick3(s.Seed, strike, y, 600+branch*2, 3)]
			}
			from = 0
			out = append(out, boltCell{x: x, y: y, ch: ch})
			switch ch {
			case '\\':
				x++
			case '/':
				x--
			}
			if branch == 0 && unit3(s.Seed, strike, y, 601) < chance {
				chance -= 0.01
				fork(x, y+1, len(out), ch)
			}
		}
	}
	fork(pick3(s.Seed, strike, 0, 602, cols), 0, 0, 0)
	return out
}

// sparks scatters the sparks of a strike from where it hit, at p
// through their flight: each arcs down to the bottom of the canvas,
// cooling from SparkColor to the background.
func (s *thunderstorm) sparks(strike int, p float64, impact boltCell, rows int, cells [][]term.Cell, bg term.Color) {
	count := 12 + pick3(s.Seed, strike, 0, 700, 7)
	for k := range count {
		dx := 4 + pick3(s.Seed, strike, k, 701, 17)
		if hash3(s.Seed, strike, k, 702)&1 == 1 {
			dx = -dx
		}
		dur := lerp(0.4, 1, unit3(s.Seed, strike, k, 703))
		u := shaderutils.EaseOutQuint(clamp01(p / dur))
		tx, ty := float64(impact.x+dx), float64(rows-1)
		cx, cy := float64(impact.x)+float64(dx)/2, float64(pick3(s.Seed, strike, k, 704, rows))
		x := (1-u)*(1-u)*float64(impact.x) + 2*(1-u)*u*cx + u*u*tx
		y := (1-u)*(1-u)*float64(impact.y) + 2*(1-u)*u*cy + u*u*ty
		glow := shaderutils.EaseInCirc(clamp01(p))
		putChar(cells, roundInt(x), roundInt(y),
			s.SparkSymbols[pick3(s.Seed, strike, k, 705, len(s.SparkSymbols))],
			shaderutils.InterpolateColor(glow, s.SparkColor, bg, s.defaultAttr.Fg))
	}
}

// rain streams raindrops down and to the right across the canvas, each
// on its own lane at its own pace, of about a row a frame. Each drop
// falls the length of its lane a whole number of times over total, so
// that it is back where it started when the storm loops.
func (s *thunderstorm) rain(frame, total, rows, cols int, cells [][]term.Cell) {
	drops := int(s.RainDensity * float64(rows+cols))
	span := float64(rows + 2)
	for d := range drops {
		lane := pick3(s.Seed, d, 0, 800, rows+cols) - rows
		speed := lerp(0.5, 1.5, unit3(s.Seed, d, 0, 801))
		falls := max(1, math.Round(float64(total)*speed/span))
		phase := unit3(s.Seed, d, 0, 802)
		fall := math.Mod((float64(frame)/float64(total)*falls+phase)*span, span)
		y := int(fall) - 1
		putChar(cells, lane+y, y,
			s.RaindropSymbols[pick3(s.Seed, d, 0, 803, len(s.RaindropSymbols))], s.RaindropColor)
	}
}
