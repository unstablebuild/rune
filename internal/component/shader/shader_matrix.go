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

// Matrix rains streaks of RainSymbols down the columns of the canvas,
// each led by a HighlightColor head and trailing RainGradient colors,
// over [MatrixParams.From], or a blank screen without it. The rain then
// fills every column from the top, covering it, and the filled canvas
// resolves cell by cell in a random order into the text, each character
// flashing HighlightColor before settling into its own color.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults, except where noted; params out of
// their documented range panic with an error wrapping
// [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "matrix" effect.
func Matrix(params MatrixParams, defaultAttr term.Attributes) ImageShader {
	d := DefaultMatrixParams()
	params.HighlightColor = orDefault(params.HighlightColor, d.HighlightColor)
	params.RainGradient = orDefaults(params.RainGradient, d.RainGradient)
	params.RainSymbols = checkSymbols("MatrixParams.RainSymbols", params.RainSymbols, d.RainSymbols)
	params.RainDuration = checkDuration("MatrixParams.RainDuration", orDefault(params.RainDuration, d.RainDuration))
	params.FillDuration = checkDuration("MatrixParams.FillDuration", orDefault(params.FillDuration, d.FillDuration))
	checkPhases("MatrixParams.RainDuration+FillDuration", params.RainDuration+params.FillDuration)
	// Each streak takes at least a row's fall, so the step bounds how
	// many streaks a column replays per frame.
	params.FallStep = max(checkFloat("MatrixParams.FallStep",
		orDefault(params.FallStep, d.FallStep), 0, 1, true, false), 1e-3)
	checkFloat("MatrixParams.StreakGap", params.StreakGap, 0, 1, false, false)
	checkFloat("MatrixParams.SymbolSwapChance", params.SymbolSwapChance, 0, 1, false, false)
	checkFloat("MatrixParams.ColorSwapChance", params.ColorSwapChance, 0, 1, false, false)
	params.Seed = orDefault(params.Seed, d.Seed)
	from := newBackdrop(params.From, defaultAttr)
	params.From = Snapshot{}
	return &matrix{MatrixParams: params, defaultAttr: defaultAttr, from: from}
}

// MatrixParams configures [Matrix].
type MatrixParams struct {
	// HighlightColor is the color of the head of each streak, and of a
	// character as it resolves.
	HighlightColor term.Color
	// RainGradient colors the body of the streaks.
	RainGradient []term.Color
	// RainSymbols are the glyphs the rain is picked from.
	RainSymbols []rune
	// RainDuration and FillDuration are the fractions of the animation
	// spent raining and filling the columns; resolving takes the rest.
	// Their sum must be below 1. (range (0, 1))
	RainDuration float64
	FillDuration float64
	// FallStep is the fraction of the animation the fastest streak
	// takes to fall one row; slower ones take up to seven times that.
	// (range (0, 1], capped below at 1e-3)
	FallStep float64
	// StreakGap is the fraction of the animation a column waits, at
	// most, before raining again; zero rains again at once.
	// (range [0, 1])
	StreakGap float64
	// SymbolSwapChance and ColorSwapChance are the chances per frame a
	// rain cell changes its symbol or color; zero never changes it.
	// (range [0, 1])
	SymbolSwapChance float64
	ColorSwapChance  float64
	// Seed picks the streaks, the glyphs and the resolve order.
	// Defaults to a fixed value if zero.
	Seed int64
	// From is the screen the rain falls on instead of a blank one.
	From Snapshot
}

// DefaultMatrixParams returns the terminaltexteffects defaults: a pale
// green highlight, a green rain gradient and its mix of ASCII and
// half-width katakana symbols.
func DefaultMatrixParams() MatrixParams {
	return MatrixParams{
		HighlightColor: term.NewRGBColor(0xdb, 0xff, 0xdb),
		RainGradient: []term.Color{
			term.NewRGBColor(0x92, 0xbe, 0x92),
			term.NewRGBColor(0x18, 0x53, 0x18),
		},
		RainSymbols:      []rune("2598Z*):.\"=+-¦|_ｦｱｳｴｵｶｷｹｺｻｼｽｾｿﾀﾂﾃﾅﾆﾇﾈﾊﾋﾎﾏﾐﾑﾒﾓﾔﾕﾗﾘﾜ"),
		RainDuration:     0.55,
		FillDuration:     0.15,
		FallStep:         0.004,
		StreakGap:        0.2,
		SymbolSwapChance: 0.005,
		ColorSwapChance:  0.001,
		Seed:             1,
	}
}

type matrix struct {
	MatrixParams
	defaultAttr term.Attributes
	from        backdrop
}

// streak is a run of rain down a column.
type streak struct {
	start, step float64
	length      int
	index       int
	// holds keeps the streak from trimming its tail, so it fills the
	// column and stays.
	holds bool
}

// coversRow tells whether the streak shows on row y at time t, and
// whether y is its head.
func (k streak) coversRow(t float64, y int) (covers, head bool) {
	fall := (t - k.start) / k.step
	fy := float64(y)
	return fy <= fall && (k.holds || fy > fall-float64(k.length)), int(fall) == y
}

func (s *matrix) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *matrix) ShadeImages(frame, total int, cells [][]term.Cell, images []term.Image) []term.Image {
	return s.from.reveal(frame, total, cells, images,
		func(t float64, cells [][]term.Cell) uncover { return s.shade(frame, t, cells) })
}

func (s *matrix) shade(frame int, t float64, cells [][]term.Cell) uncover {
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return nil
	}
	fillStart := s.RainDuration
	resolveStart := fillStart + s.FillDuration
	resolveDur := 1 - resolveStart
	for x := range cols {
		rain, raining := s.rainStreak(t, x, rows)
		fill := s.fill(x, rows)
		for y := range rows {
			if x >= len(cells[y]) {
				continue
			}
			cell := cells[y][x]
			isContent := hasContent(cell, s.defaultAttr.Bg)
			shown, head, which := false, false, fill
			if t >= fillStart {
				shown, head = fill.coversRow(t, y)
			}
			if !shown && raining {
				if covers, isHead := rain.coversRow(t, y); covers {
					shown, head, which = true, isHead, rain
				}
			}
			if t >= resolveStart {
				resolveAt := resolveStart + unit3(s.Seed, x, y, 60)*resolveDur*0.7
				if t >= resolveAt {
					if hasOriginalChar(cell) {
						p := window(t, resolveAt, resolveDur*0.3)
						paintFg(cells, x, y, fadeFg(p, s.HighlightColor, cell.Fg, s.defaultAttr.Fg))
					}
					continue
				}
			}
			// The rain falls on a bare canvas: content shows only once
			// resolved.
			if isContent {
				blankCell(cells, x, y, s.defaultAttr.Bg)
			}
			if !shown {
				continue
			}
			putChar(cells, x, y, s.rainSymbol(frame, x, y, which.index), s.rainColor(frame, x, y, which.index, head))
		}
	}
	// The fill covers the backdrop as it reaches it.
	return func(x, y int) float64 {
		fill := s.fill(x, rows)
		return fill.start + fill.step*float64(y)
	}
}

// fill returns the streak that fills column x of a canvas of rows from
// the top once the rain is over.
func (s *matrix) fill(x, rows int) streak {
	return streak{
		start:  s.RainDuration + unit3(s.Seed, x, 0, 50)*s.FillDuration*0.3,
		step:   s.FillDuration * 0.6 / float64(rows),
		length: rows,
		index:  -1,
		holds:  true,
	}
}

// rainStreak returns the streak raining down column x at time t, if
// any: streaks follow each other down the column, each with its own
// length and speed, a random gap apart.
func (s *matrix) rainStreak(t float64, x, rows int) (streak, bool) {
	start := unit3(s.Seed, x, 0, 10) * s.StreakGap
	for j := 0; start < s.RainDuration; j++ {
		k := streak{
			start:  start,
			step:   s.FallStep * lerp(1, 7.5, unit3(s.Seed, x, j, 11)),
			length: max(1, int(lerp(0.1, 1, unit3(s.Seed, x, j, 12))*float64(rows))),
			index:  j,
		}
		// The streak is over once its tail has left the bottom row.
		end := k.start + k.step*float64(rows+k.length)
		if t < end {
			return k, t >= k.start
		}
		start = end + unit3(s.Seed, x, j, 13)*s.StreakGap
	}
	return streak{}, false
}

// rainSymbol picks the glyph of a rain cell, which the cell swaps now
// and then at SymbolSwapChance per frame.
func (s *matrix) rainSymbol(frame, x, y, index int) rune {
	swaps := s.swapsUpTo(frame, x, y, 20, s.SymbolSwapChance)
	return s.RainSymbols[pick3(s.Seed, x, y, 1000*index+swaps, len(s.RainSymbols))]
}

// rainColor picks the color of a rain cell: the highlight at the head,
// a gradient color in the body, swapped now and then at
// ColorSwapChance per frame.
func (s *matrix) rainColor(frame, x, y, index int, head bool) term.Color {
	if head {
		return s.HighlightColor
	}
	swaps := s.swapsUpTo(frame, x, y, 30, s.ColorSwapChance)
	return shaderutils.SampleGradient(unit3(s.Seed, x, y, 1000*index+swaps), s.RainGradient)
}

// swapsUpTo counts the swaps a cell has made by frame when it swaps
// once every 1/chance frames, at a phase of its own. Chances so small a
// cell would swap less than once in 2^30 frames never swap.
func (s *matrix) swapsUpTo(frame, x, y, salt int, chance float64) int {
	if chance <= 0 || frame < 0 {
		return 0
	}
	if 1/chance > 1<<30 {
		return 0
	}
	period := max(1, int(1/chance))
	return frame/period + (frame%period+pick3(s.Seed, x, y, salt, period))/period
}
