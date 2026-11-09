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

// Print prints the text like a line printer: each row is typed out on
// the bottom row of the canvas, left to right, its characters blocking
// in through TypingSymbols from TypedColor to their own, and the rows
// printed so far scroll up a row as the print head carries back for
// the next one, over [PrintParams.From].
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults; params out of their documented range
// panic with an error wrapping [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "print" effect.
func Print(params PrintParams, defaultAttr term.Attributes) ImageShader {
	d := DefaultPrintParams()
	params.HeadSymbol = checkSymbol("PrintParams.HeadSymbol", params.HeadSymbol, d.HeadSymbol)
	params.HeadColor = orDefault(params.HeadColor, d.HeadColor)
	params.TypedColor = orDefault(params.TypedColor, d.TypedColor)
	params.TypingSymbols = checkSymbols("PrintParams.TypingSymbols", params.TypingSymbols, d.TypingSymbols)
	params.ReturnEasing = checkEasing("PrintParams.ReturnEasing", params.ReturnEasing, d.ReturnEasing)
	params.ReturnSpeed = capPositive("PrintParams.ReturnSpeed", orDefault(params.ReturnSpeed, d.ReturnSpeed), 1e-3, 1e3)
	params.PrintSpeed = capPositive("PrintParams.PrintSpeed", orDefault(params.PrintSpeed, d.PrintSpeed), 1e-3, 1e3)
	from := newBackdrop(params.From, defaultAttr)
	params.From = Snapshot{}
	return &printer{PrintParams: params, defaultAttr: defaultAttr, from: from}
}

// PrintParams configures [Print].
type PrintParams struct {
	// HeadSymbol is the print head, colored HeadColor, shown as it
	// carries back between rows.
	HeadSymbol rune
	HeadColor  term.Color
	// TypedColor is the color a character is typed in.
	TypedColor term.Color
	// TypingSymbols is the sequence a character blocks in through as it
	// is typed.
	TypingSymbols []rune
	// ReturnEasing shapes the print head's carriage return.
	ReturnEasing shaderutils.Easing
	// ReturnSpeed and PrintSpeed are how many cells the head carries
	// back, and how many characters it types, in the time it takes to
	// type one character at a speed of one; only their ratio matters.
	// (range (0, ∞), capped to [1e-3, 1e3])
	ReturnSpeed float64
	PrintSpeed  float64
	// From is the screen the printout scrolls up over instead of a blank one.
	From Snapshot
}

// DefaultPrintParams returns the terminaltexteffects defaults: a block
// head, white typing and an in-out quad carriage return.
func DefaultPrintParams() PrintParams {
	return PrintParams{
		HeadSymbol:    '█',
		HeadColor:     term.NewRGBColor(255, 255, 255),
		TypedColor:    term.NewRGBColor(255, 255, 255),
		TypingSymbols: []rune{'█', '▓', '▒', '░'},
		ReturnEasing:  shaderutils.EaseInOutQuad,
		ReturnSpeed:   1.5,
		PrintSpeed:    2,
	}
}

type printer struct {
	PrintParams
	defaultAttr term.Attributes
	from        backdrop
}

// printRow is the extent of a row to print and when it prints.
type printRow struct {
	first, last int
	// start is when the head starts carrying back to the row, typing
	// when it starts typing, and end when the row is typed.
	start, typing, end float64
}

// printTypeAnim is the fraction of the animation a typed character
// takes to block in.
const printTypeAnim = 0.02

func (s *printer) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *printer) ShadeImages(frame, total int, cells [][]term.Cell, images []term.Image) []term.Image {
	return s.from.reveal(frame, total, cells, images, s.shade)
}

func (s *printer) shade(t float64, cells [][]term.Cell) uncover {
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return nil
	}
	glyphs := collectContent(cells, s.defaultAttr.Bg)
	plan := s.plan(glyphs, rows)
	current := rows - 1
	for r, row := range plan {
		if t < row.end || r == rows-1 {
			current = r
			break
		}
	}
	bottom := rows - 1
	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		row := plan[g.y]
		if g.y > current {
			places[i] = placement{hidden: true}
			continue
		}
		typed := row.typing
		if row.last > row.first {
			typed += (row.end - row.typing) * float64(g.x-row.first) / float64(row.last-row.first+1)
		}
		if t < typed {
			places[i] = placement{hidden: true}
			continue
		}
		p := window(t, typed, printTypeAnim)
		step := int(p * float64(len(s.TypingSymbols)+1))
		places[i] = placement{x: g.x, y: bottom - (current - g.y), fg: g.cell.Fg}
		if step < len(s.TypingSymbols) {
			places[i].ch = s.TypingSymbols[step]
			places[i].fg = fadeFg(float64(step)/float64(len(s.TypingSymbols)), s.TypedColor, g.cell.Fg, s.defaultAttr.Fg)
		}
	}
	// The rows printed so far show bottom-current rows below their place.
	// The rows printed so far show bottom-current rows below their place.
	s.from.shift(bottom - current)
	s.from.place(cells, glyphs, places)
	// The head shows carrying back to the current row.
	row := plan[current]
	if current > 0 && t < row.typing {
		from := float64(plan[current-1].last)
		p := s.ReturnEasing(window(t, row.start, row.typing-row.start))
		putChar(cells, roundInt(lerp(from, float64(row.first), p)), bottom, s.HeadSymbol, s.HeadColor)
	}
	// The printout covers the backdrop a row higher each time the head
	// carries back to a new row.
	return func(_, y int) float64 { return plan[bottom-y].start }
}

// plan returns the extent and timing of every row: a row prints from
// its first character to its last, or one cell when blank, and its
// share of the animation is the time the head takes to carry back to
// it and type it.
func (s *printer) plan(glyphs []glyph, rows int) []printRow {
	plan := make([]printRow, rows)
	for r := range plan {
		plan[r].first = -1
	}
	for _, g := range glyphs {
		row := &plan[g.y]
		if row.first < 0 || g.x < row.first {
			row.first = g.x
		}
		row.last = max(row.last, g.x)
	}
	weights := make([]float64, rows)
	sum, lastCol := 0.0, 0
	for r := range plan {
		if plan[r].first < 0 {
			plan[r].first = 0
		}
		carry := float64(abs(lastCol-plan[r].first)) / s.ReturnSpeed
		typing := float64(plan[r].last-plan[r].first+1) / s.PrintSpeed
		if r == 0 {
			carry = 0
		}
		weights[r] = carry + typing
		sum += weights[r]
		lastCol = plan[r].last
	}
	// The last character typed must have blocked in by the end.
	scale := (1 - printTypeAnim) / sum
	start, lastCol := 0.0, 0
	for r := range plan {
		carry := float64(abs(lastCol-plan[r].first)) / s.ReturnSpeed
		if r == 0 {
			carry = 0
		}
		plan[r].start = start
		plan[r].typing = start + carry*scale
		start += weights[r] * scale
		plan[r].end = start
		lastCol = plan[r].last
	}
	return plan
}
