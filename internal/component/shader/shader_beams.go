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
	"sort"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// Beams sweeps beams of light along the rows and columns of the canvas
// in a random order and direction: each beam leaves a glowing trail of
// RowSymbols or ColumnSymbols colored by BeamGradient, and reveals the
// text behind it dimmed. Once the beams are done, a diagonal wipe from
// the top-left corner brightens the text back to its own colors. The
// beams sweep [BeamsParams.From] away as they cross it.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults; params out of their documented range
// panic with an error wrapping [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "beams" effect.
func Beams(params BeamsParams, defaultAttr term.Attributes) ImageShader {
	d := DefaultBeamsParams()
	params.RowSymbols = checkSymbols("BeamsParams.RowSymbols", params.RowSymbols, d.RowSymbols)
	params.ColumnSymbols = checkSymbols("BeamsParams.ColumnSymbols", params.ColumnSymbols, d.ColumnSymbols)
	params.BeamGradient = orDefaults(params.BeamGradient, d.BeamGradient)
	params.DimBrightness = checkBrightness("BeamsParams.DimBrightness", orDefault(params.DimBrightness, d.DimBrightness))
	params.BeamsDuration = checkDuration("BeamsParams.BeamsDuration", orDefault(params.BeamsDuration, d.BeamsDuration))
	// The last trail and its fade must be over by the end of the wipe,
	// and the beams must be done sweeping by the end of their phase.
	params.TrailDuration = min(checkDuration("BeamsParams.TrailDuration",
		orDefault(params.TrailDuration, d.TrailDuration)), (1-params.BeamsDuration)/2)
	params.RowSweep = min(checkFloat("BeamsParams.RowSweep",
		orDefault(params.RowSweep, d.RowSweep), 0, 1, true, false), params.BeamsDuration)
	params.ColumnSweep = min(checkFloat("BeamsParams.ColumnSweep",
		orDefault(params.ColumnSweep, d.ColumnSweep), 0, 1, true, false), params.BeamsDuration)
	params.Seed = orDefault(params.Seed, d.Seed)
	from := newBackdrop(params.From, defaultAttr)
	params.From = Snapshot{}
	return &beams{BeamsParams: params, defaultAttr: defaultAttr, from: from}
}

// BeamsParams configures [Beams].
type BeamsParams struct {
	// RowSymbols and ColumnSymbols are the glyph sequences a cell cycles
	// through, in order, as a row or column beam passes over it.
	RowSymbols    []rune
	ColumnSymbols []rune
	// BeamGradient colors the trail from the beam's head to its tail.
	BeamGradient []term.Color
	// DimBrightness scales the brightness of the text the beams reveal
	// until the final wipe. (range (0, 1])
	DimBrightness float64
	// BeamsDuration is the fraction of the animation the beams sweep
	// for; the final wipe takes the rest. (range (0, 1))
	BeamsDuration float64
	// TrailDuration is the fraction of the animation a cell glows for
	// after a beam passes over it. (range (0, 1), capped at half the
	// wipe)
	TrailDuration float64
	// RowSweep and ColumnSweep are the fractions of the animation the
	// slowest row and column beams take to cross the canvas; faster
	// ones take down to a quarter and two thirds of that, respectively,
	// as terminaltexteffects spreads its speeds. (range (0, 1], capped
	// at BeamsDuration)
	RowSweep    float64
	ColumnSweep float64
	// Seed picks the beams' order, direction and speed. Defaults to a
	// fixed value if zero.
	Seed int64
	// From is the screen the beams start from instead of a blank one.
	From Snapshot
}

// DefaultBeamsParams returns the terminaltexteffects defaults.
func DefaultBeamsParams() BeamsParams {
	return BeamsParams{
		RowSymbols:    []rune{'▂', '▁', '_'},
		ColumnSymbols: []rune{'▌', '▍', '▎', '▏'},
		BeamGradient: []term.Color{
			term.NewRGBColor(0xff, 0xff, 0xff),
			term.NewRGBColor(0x00, 0xD1, 0xFF),
			term.NewRGBColor(0x8A, 0x00, 0x8A),
		},
		DimBrightness: 0.3,
		BeamsDuration: 0.75,
		TrailDuration: 0.04,
		RowSweep:      0.12,
		ColumnSweep:   0.16,
		Seed:          1,
	}
}

type beams struct {
	BeamsParams
	defaultAttr term.Attributes
	from        backdrop
}

// beam is a sweep along one row or column.
type beam struct {
	start, duration float64
	reversed        bool
}

func (s *beams) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *beams) ShadeImages(frame, total int, cells [][]term.Cell, images []term.Image) []term.Image {
	return s.from.reveal(frame, total, cells, images, s.shade)
}

func (s *beams) shade(t float64, cells [][]term.Cell) uncover {
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return nil
	}
	rowBeams, colBeams := s.schedule(rows, cols)
	// hitTime is when the beam reaches the cell at index i along it.
	hitTime := func(b beam, i, length int) float64 {
		if b.reversed {
			i = length - 1 - i
		}
		return b.start + b.duration*float64(i)/float64(length)
	}
	wipe := window(t, s.BeamsDuration, 1-s.BeamsDuration)
	maxDiagonal := float64(rows + cols - 2)
	for y, row := range cells {
		for x := range row {
			rowHit := hitTime(rowBeams[y], x, cols)
			colHit := hitTime(colBeams[x], y, rows)
			isText := hasContent(row[x], s.defaultAttr.Bg)
			fg := resolveFg(row[x].Fg, s.defaultAttr.Fg)
			dim := shaderutils.AdjustBrightness(fg, s.DimBrightness, s.defaultAttr.Fg)
			// The most recent beam over the cell decides what it shows.
			last, symbols := -1.0, s.RowSymbols
			if rowHit <= t {
				last = rowHit
			}
			if colHit <= t && colHit > last {
				last, symbols = colHit, s.ColumnSymbols
			}
			if last < 0 && wipe == 0 {
				if isText {
					blankCell(cells, x, y, s.defaultAttr.Bg)
				}
				continue
			}
			age := t - last
			switch {
			case last >= 0 && age < s.TrailDuration:
				p := age / s.TrailDuration
				putChar(cells, x, y, symbols[min(len(symbols)-1, int(p*float64(len(symbols))))],
					shaderutils.SampleGradient(p, s.BeamGradient))
			case !isText:
				continue
			case last >= 0 && age < 2*s.TrailDuration:
				p := (age - s.TrailDuration) / s.TrailDuration
				paintFg(cells, x, y, shaderutils.InterpolateColor(p, fg, dim, s.defaultAttr.Fg))
			default:
				// The wipe brightens diagonal by diagonal, each over a
				// fifth of its window.
				bright := window(wipe, float64(x+y)/max(1, maxDiagonal)*0.8, 0.2)
				paintFg(cells, x, y, fadeFg(bright, dim, row[x].Fg, s.defaultAttr.Fg))
			}
		}
	}
	// The first beam over a cell sweeps the backdrop away.
	return func(x, y int) float64 {
		return min(hitTime(rowBeams[y], x, cols), hitTime(colBeams[x], y, rows))
	}
}

// schedule returns the beams of every row and column: they start in a
// random order spread over the beams phase, each going one way or the
// other at its own speed.
func (s *beams) schedule(rows, cols int) (rowBeams, colBeams []beam) {
	type group struct {
		key   uint32
		index int
	}
	groups := make([]group, 0, rows+cols)
	for y := range rows {
		groups = append(groups, group{hash3(s.Seed, 0, y, 1), y})
	}
	for x := range cols {
		groups = append(groups, group{hash3(s.Seed, 1, x, 1), rows + x})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].key != groups[j].key {
			return groups[i].key < groups[j].key
		}
		return groups[i].index < groups[j].index
	})
	rowBeams = make([]beam, rows)
	colBeams = make([]beam, cols)
	slowest := max(s.RowSweep, s.ColumnSweep)
	starts := max(0, s.BeamsDuration-slowest)
	for rank, g := range groups {
		start := starts * float64(rank) / float64(len(groups))
		if g.index < rows {
			y := g.index
			rowBeams[y] = beam{
				start:    start,
				duration: s.RowSweep * lerp(0.25, 1, unit3(s.Seed, 0, y, 2)),
				reversed: hash3(s.Seed, 0, y, 3)&1 == 1,
			}
			continue
		}
		x := g.index - rows
		colBeams[x] = beam{
			start:    start,
			duration: s.ColumnSweep * lerp(0.6, 1, unit3(s.Seed, 1, x, 2)),
			reversed: hash3(s.Seed, 1, x, 3)&1 == 1,
		}
	}
	return rowBeams, colBeams
}
