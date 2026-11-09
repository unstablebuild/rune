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

// Rain rains the text down from the top edge: the characters drop in
// row by row from the top, in a random order within each row, as
// raindrops of random symbol and color that accelerate to their place
// and fade into the character they carry. They fall in front of
// [RainParams.From], which the rain washes away row by row as it
// reaches them.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults, except where noted; params out of
// their documented range panic with an error wrapping
// [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "rain" effect.
func Rain(params RainParams, defaultAttr term.Attributes) ImageShader {
	d := DefaultRainParams()
	params.RainColors = orDefaults(params.RainColors, d.RainColors)
	params.RainSymbols = checkSymbols("RainParams.RainSymbols", params.RainSymbols, d.RainSymbols)
	params.Easing = checkEasing("RainParams.Easing", params.Easing, d.Easing)
	params.FallDuration = checkDuration("RainParams.FallDuration", orDefault(params.FallDuration, d.FallDuration))
	checkFloat("RainParams.FadeDuration", params.FadeDuration, 0, 1, false, true)
	checkPhases("RainParams.FallDuration+FadeDuration", params.FallDuration+params.FadeDuration)
	checkFloat("RainParams.SpeedJitter", params.SpeedJitter, 0, 1, false, true)
	params.Seed = orDefault(params.Seed, d.Seed)
	from := newBackdrop(params.From, defaultAttr)
	params.From = Snapshot{}
	return &rain{RainParams: params, defaultAttr: defaultAttr, from: from}
}

// RainParams configures [Rain].
type RainParams struct {
	// RainColors are the colors raindrops are picked from.
	RainColors []term.Color
	// RainSymbols are the glyphs raindrops are picked from.
	RainSymbols []rune
	// Easing shapes each raindrop's fall.
	Easing shaderutils.Easing
	// FallDuration is the fraction of the animation a raindrop takes to
	// fall across the whole canvas. (range (0, 1))
	FallDuration float64
	// FadeDuration is the fraction of the animation a landed raindrop
	// takes to fade into its character; zero shows it at once. It must
	// leave room for FallDuration. (range [0, 1-FallDuration))
	FadeDuration float64
	// SpeedJitter varies each raindrop's fall time by up to this
	// fraction; zero does not vary it. (range [0, 1))
	SpeedJitter float64
	// Seed picks the raindrops' symbols, colors, order and jitter.
	// Defaults to a fixed value if zero.
	Seed int64
	// From is the screen the rain starts from instead of a blank one.
	From Snapshot
}

// DefaultRainParams returns the terminaltexteffects defaults: blue
// raindrops, an in quart easing and speeds spread by a quarter.
func DefaultRainParams() RainParams {
	return RainParams{
		RainColors: []term.Color{
			term.NewRGBColor(0x00, 0x31, 0x5C),
			term.NewRGBColor(0x00, 0x4C, 0x8F),
			term.NewRGBColor(0x00, 0x75, 0xDB),
			term.NewRGBColor(0x3F, 0x91, 0xD9),
			term.NewRGBColor(0x78, 0xB9, 0xF2),
			term.NewRGBColor(0x9A, 0xC8, 0xF5),
			term.NewRGBColor(0xB8, 0xD8, 0xF8),
			term.NewRGBColor(0xE3, 0xEF, 0xFC),
		},
		RainSymbols:  []rune{'o', '.', ',', '*', '|'},
		Easing:       shaderutils.EaseInQuart,
		FallDuration: 0.3,
		FadeDuration: 0.1,
		SpeedJitter:  0.25,
		Seed:         1,
	}
}

type rain struct {
	RainParams
	defaultAttr term.Attributes
	from        backdrop
}

func (s *rain) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *rain) ShadeImages(frame, total int, cells [][]term.Cell, images []term.Image) []term.Image {
	return s.from.reveal(frame, total, cells, images, s.shade)
}

func (s *rain) shade(t float64, cells [][]term.Cell) uncover {
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return nil
	}
	glyphs := collectContent(cells, s.defaultAttr.Bg)
	ranks := ranksBy(glyphs, func(g glyph) (int, int) {
		return g.y, int(hash3(s.Seed, g.x, g.y, 1) >> 1)
	})
	trickle := 1 - s.FallDuration - s.FadeDuration
	// reached is when the rain reaches each row: when the drop that starts
	// first in it would land unhurried, or sooner if one of its drops does.
	reached := make([]float64, rows)
	above := 0
	for y := range reached {
		start := trickle * float64(y) / float64(rows)
		if len(glyphs) > 0 {
			start = trickle * float64(above) / float64(len(glyphs))
		}
		reached[y] = start + s.FallDuration*float64(y)/float64(max(1, rows-1))
		for above < len(glyphs) && glyphs[above].y == y {
			above++
		}
	}
	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		start := trickle * float64(ranks[i]) / float64(len(glyphs))
		dur := s.FallDuration * float64(g.y) / float64(max(1, rows-1))
		dur *= 1 + s.SpeedJitter*(2*unit3(s.Seed, g.x, g.y, 2)-1)
		// A slow drop still has to land and fade in by the end.
		dur = math.Min(dur, 1-s.FadeDuration-start)
		reached[g.y] = min(reached[g.y], start+dur)
		if t < start {
			places[i] = placement{hidden: true}
			continue
		}
		p := window(t, start, dur)
		drop := s.RainColors[pick3(s.Seed, g.x, g.y, 3, len(s.RainColors))]
		if p < 1 {
			places[i] = placement{
				x:  g.x,
				y:  roundInt(lerp(0, float64(g.y), s.Easing(p))),
				ch: s.RainSymbols[pick3(s.Seed, g.x, g.y, 4, len(s.RainSymbols))],
				fg: drop,
			}
			continue
		}
		fade := window(t, start+dur, s.FadeDuration)
		places[i] = placement{
			x: g.x, y: g.y,
			fg: fadeFg(fade, drop, g.cell.Fg, s.defaultAttr.Fg),
		}
	}
	s.from.place(cells, glyphs, places)
	return func(_, y int) float64 { return reached[y] }
}
