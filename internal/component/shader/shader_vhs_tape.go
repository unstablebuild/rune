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
)

// VHSTape plays the text back like a worn tape: a band of three rows
// tears sideways in GlitchWaveColors as it drifts down the screen,
// random rows jump aside in GlitchLineColors and snap back, and now
// and then the picture dissolves into NoiseSymbols snow. The tape then
// loses the picture to snow altogether, and redraws it row by row from
// the top, each row flashing RedrawColor.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults, except where noted; params out of
// their documented range panic with an error wrapping
// [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "vhstape" effect.
func VHSTape(params VHSTapeParams, defaultAttr term.Attributes) Shader {
	d := DefaultVHSTapeParams()
	params.GlitchLineColors = orDefaults(params.GlitchLineColors, d.GlitchLineColors)
	params.GlitchWaveColors = orDefaults(params.GlitchWaveColors, d.GlitchWaveColors)
	params.NoiseColors = orDefaults(params.NoiseColors, d.NoiseColors)
	params.NoiseSymbols = checkSymbols("VHSTapeParams.NoiseSymbols", params.NoiseSymbols, d.NoiseSymbols)
	checkFloat("VHSTapeParams.GlitchLineChance", params.GlitchLineChance, 0, 1, false, false)
	checkFloat("VHSTapeParams.NoiseChance", params.NoiseChance, 0, 1, false, false)
	params.GlitchDuration = checkDuration("VHSTapeParams.GlitchDuration", orDefault(params.GlitchDuration, d.GlitchDuration))
	params.RedrawColor = orDefault(params.RedrawColor, d.RedrawColor)
	params.Seed = orDefault(params.Seed, d.Seed)
	return &vhsTape{VHSTapeParams: params, defaultAttr: defaultAttr}
}

// VHSTapeParams configures [VHSTape].
type VHSTapeParams struct {
	// GlitchLineColors are cycled by a row as it jumps aside and back.
	GlitchLineColors []term.Color
	// GlitchWaveColors are cycled by the rows of the tearing band.
	GlitchWaveColors []term.Color
	// NoiseColors and NoiseSymbols are what snow is picked from.
	NoiseColors  []term.Color
	NoiseSymbols []rune
	// GlitchLineChance is the chance per frame that a row jumps aside;
	// zero never does. (range [0, 1])
	GlitchLineChance float64
	// NoiseChance is the chance per frame that the picture dissolves
	// into snow for a moment; zero never does. (range [0, 1])
	NoiseChance float64
	// GlitchDuration is the fraction of the animation the tape glitches
	// for; losing the picture and redrawing it take the rest.
	// (range (0, 1))
	GlitchDuration float64
	// RedrawColor is the color a row flashes as it is redrawn.
	RedrawColor term.Color
	// Seed picks the glitches. Defaults to a fixed value if zero.
	Seed int64
}

// DefaultVHSTapeParams returns the terminaltexteffects defaults.
func DefaultVHSTapeParams() VHSTapeParams {
	rgb := []term.Color{
		term.NewRGBColor(0xff, 0xff, 0xff),
		term.NewRGBColor(0xff, 0x00, 0x00),
		term.NewRGBColor(0x00, 0xff, 0x00),
		term.NewRGBColor(0x00, 0x00, 0xff),
		term.NewRGBColor(0xff, 0xff, 0xff),
	}
	return VHSTapeParams{
		GlitchLineColors: rgb,
		GlitchWaveColors: rgb,
		NoiseColors: []term.Color{
			term.NewRGBColor(0x1e, 0x1e, 0x1f),
			term.NewRGBColor(0x3c, 0x3b, 0x3d),
			term.NewRGBColor(0x6d, 0x6c, 0x70),
			term.NewRGBColor(0xa2, 0xa1, 0xa6),
			term.NewRGBColor(0xcb, 0xc9, 0xcf),
			term.NewRGBColor(0xff, 0xff, 0xff),
		},
		NoiseSymbols:     []rune{'#', '*', '.', ':'},
		GlitchLineChance: 0.05,
		NoiseChance:      0.004,
		GlitchDuration:   0.75,
		RedrawColor:      term.NewRGBColor(255, 255, 255),
		Seed:             1,
	}
}

type vhsTape struct {
	VHSTapeParams
	defaultAttr term.Attributes
}

// vhsWaveStep is how many frames the tearing band takes to drift one
// step; the longest tear, 14 cells at 2 cells a frame, takes that.
const vhsWaveStep = 7

// vhsSnowFrames is how long a moment of snow lasts, and vhsSnowHold how
// many frames each snow glyph stays.
const vhsSnowFrames, vhsSnowHold = 50, 2

// vhsMaxFrames is the most frames the glitches are timed over. Working
// out a frame replays the glitches before it, so a longer animation
// runs them at this resolution to keep frames cheap.
const vhsMaxFrames = 1 << 14

// vhsRowShift is how a row is displaced and colored on a frame.
type vhsRowShift struct {
	dx     int
	color  term.Color
	shifts bool
}

func (s *vhsTape) Shade(frame, total int, cells [][]term.Cell) {
	t, ok := progress(frame, total)
	if !ok {
		return
	}
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return
	}
	frame = max(0, min(frame, total-1))
	if total > vhsMaxFrames {
		frame = min(int(float64(frame)/float64(total)*vhsMaxFrames), vhsMaxFrames-1)
		total = vhsMaxFrames
	}
	glitchFrames := int(s.GlitchDuration * float64(total))
	snowStart := s.GlitchDuration
	snowDur := (1 - s.GlitchDuration) * 0.3
	redrawStart := snowStart + snowDur
	redrawDur := 1 - redrawStart
	snowGlyph := func(x, y, step int) (rune, term.Color) {
		return s.NoiseSymbols[pick3(s.Seed, x, y, 3000+step, len(s.NoiseSymbols))],
			s.NoiseColors[pick3(s.Seed, x, y, 4000+step, len(s.NoiseColors))]
	}

	if t >= snowStart {
		for y, row := range cells {
			slot := redrawDur / float64(rows)
			redraw := window(t, redrawStart+float64(y)*slot, slot)
			// A row flashes for most of its slot, then shows.
			if redraw >= 0.85 {
				continue
			}
			for x, cell := range row {
				if !hasContent(cell, s.defaultAttr.Bg) {
					continue
				}
				if redraw > 0 {
					putChar(cells, x, y, '█', s.RedrawColor)
					continue
				}
				ch, fg := snowGlyph(x, y, frame/vhsSnowHold)
				putChar(cells, x, y, ch, fg)
			}
		}
		return
	}

	shifts := make([]vhsRowShift, rows)
	s.waveShifts(frame, rows, shifts)
	s.lineShifts(frame, glitchFrames, rows, shifts)
	snowing, snowStep := s.snowing(frame, glitchFrames)
	glyphs := collectContent(cells, s.defaultAttr.Bg)
	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		shift := shifts[g.y]
		places[i] = placement{x: g.x + shift.dx, y: g.y, fg: g.cell.Fg}
		if shift.shifts {
			places[i].fg = shift.color
		}
		if snowing {
			places[i].ch, places[i].fg = snowGlyph(g.x, g.y, snowStep)
		}
	}
	place(cells, glyphs, places, s.defaultAttr.Bg)
}

// waveShifts applies the tearing band to shifts: three rows torn 8, 14
// and 8 cells to the right, drifting down a row now and then from a
// start in the upper half of the screen, and starting over when it
// reaches the bottom.
func (s *vhsTape) waveShifts(frame, rows int, shifts []vhsRowShift) {
	if rows < 3 {
		return
	}
	tears := [3]int{8, 14, 8}
	steps := frame / vhsWaveStep
	u := float64(frame%vhsWaveStep) / vhsWaveStep
	episode, top := 0, pick3(s.Seed, 0, 0, 100, max(1, rows/2))
	prevTop := -1
	for step := 1; step <= steps; step++ {
		prevTop = top
		if unit3(s.Seed, episode, step, 101) < 0.3 {
			if unit3(s.Seed, episode, step, 102) < 0.3 {
				top--
			} else {
				top++
			}
		}
		top = max(0, min(top, rows-3))
		if top >= rows-3 {
			// The band fell off the bottom: tear a new one.
			episode++
			top = pick3(s.Seed, episode, 0, 100, max(1, rows/2))
			prevTop = -1
		}
	}
	offsetAt := func(top, y int) int {
		if top >= 0 && y >= top && y < top+3 {
			return tears[y-top]
		}
		return 0
	}
	for y := max(0, min(prevTop, top)); y < min(rows, max(prevTop, top)+3); y++ {
		from, to := offsetAt(prevTop, y), offsetAt(top, y)
		if from == 0 && to == 0 {
			continue
		}
		shifts[y] = vhsRowShift{
			dx:     roundInt(lerp(float64(from), float64(to), u)),
			color:  s.GlitchWaveColors[min(len(s.GlitchWaveColors)-1, int(u*float64(len(s.GlitchWaveColors))))],
			shifts: true,
		}
	}
}

// lineShifts applies the rows jumping aside to shifts: as many jumps as
// GlitchLineChance gives over the glitching frames, each on a random
// row, going 4 to 25 cells either way at 1 to 2 cells a frame, holding
// for 20 to 75 frames, and coming back.
func (s *vhsTape) lineShifts(frame, glitchFrames, rows int, shifts []vhsRowShift) {
	events := int(s.GlitchLineChance * float64(glitchFrames))
	for e := range events {
		y := pick3(s.Seed, e, 0, 200, rows)
		if shifts[y].shifts {
			continue
		}
		offset := 4 + pick3(s.Seed, e, 0, 201, 22)
		if hash3(s.Seed, e, 0, 202)&1 == 1 {
			offset = -offset
		}
		start := int(unit3(s.Seed, e, 0, 203) * float64(glitchFrames))
		out := float64(abs(offset)) / lerp(1, 2, unit3(s.Seed, e, 0, 204))
		hold := 20 + float64(pick3(s.Seed, e, 0, 205, 56))
		back := float64(abs(offset)) / lerp(1, 2, unit3(s.Seed, e, 0, 206))
		age := float64(frame - start)
		if age < 0 || age >= out+hold+back {
			continue
		}
		colors := s.GlitchLineColors
		var u float64
		switch {
		case age < out:
			u = age / out
			shifts[y].dx = roundInt(float64(offset) * u)
		case age < out+hold:
			u = 1
			shifts[y].dx = offset
		default:
			u = (age - out - hold) / back
			shifts[y].dx = roundInt(float64(offset) * (1 - u))
			u = 1 - u
		}
		shifts[y].color = colors[min(len(colors)-1, int(u*float64(len(colors))))]
		shifts[y].shifts = true
	}
}

// snowing tells whether the picture is dissolved into snow on frame,
// and which snow glyph step shows: as many moments of snow as
// NoiseChance gives over the glitching frames.
func (s *vhsTape) snowing(frame, glitchFrames int) (bool, int) {
	events := int(s.NoiseChance*float64(glitchFrames) + 0.5)
	for e := range events {
		start := int(unit3(s.Seed, e, 0, 300) * float64(max(1, glitchFrames-vhsSnowFrames)))
		if frame >= start && frame < start+vhsSnowFrames {
			return true, (frame - start) / vhsSnowHold
		}
	}
	return false, 0
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
