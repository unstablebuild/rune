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

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// BinaryPath has the binary representation of each character snake in
// from outside the canvas along a path of orthogonal legs to its place,
// where the digits collapse into the character, flashing bright and
// settling dim, until a final diagonal wipe brings the text to its own
// colors. The text is delivered over [BinaryPathParams.From], which the
// wipe sweeps away.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults; params out of their documented range
// panic with an error wrapping [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "binarypath" effect.
func BinaryPath(params BinaryPathParams, defaultAttr term.Attributes) ImageShader {
	d := DefaultBinaryPathParams()
	params.BinaryColors = orDefaults(params.BinaryColors, d.BinaryColors)
	params.CollapseColor = orDefault(params.CollapseColor, d.CollapseColor)
	params.DimBrightness = checkBrightness("BinaryPathParams.DimBrightness", orDefault(params.DimBrightness, d.DimBrightness))
	params.ActiveGroups = checkFloat("BinaryPathParams.ActiveGroups",
		orDefault(params.ActiveGroups, d.ActiveGroups), 0, 1, true, false)
	params.TravelDuration = checkDuration("BinaryPathParams.TravelDuration", orDefault(params.TravelDuration, d.TravelDuration))
	params.Seed = orDefault(params.Seed, d.Seed)
	from := newBackdrop(params.From, defaultAttr)
	params.From = Snapshot{}
	return &binaryPath{BinaryPathParams: params, defaultAttr: defaultAttr, from: from}
}

// BinaryPathParams configures [BinaryPath].
type BinaryPathParams struct {
	// BinaryColors color the travelling digits, each picking one.
	BinaryColors []term.Color
	// CollapseColor is the flash of a character as its digits arrive,
	// from which it dims to DimBrightness of its own color until the
	// wipe. (range (0, 1])
	CollapseColor term.Color
	DimBrightness float64
	// ActiveGroups is the fraction of characters whose digits travel at
	// once. (range (0, 1])
	ActiveGroups float64
	// TravelDuration is the fraction of the animation the digits take to
	// deliver every character; the wipe takes the rest. (range (0, 1))
	TravelDuration float64
	// Seed picks the paths, their order and the digit colors. Defaults to
	// a fixed value if zero.
	Seed int64
	// From is the screen the digits travel over instead of a blank one.
	From Snapshot
}

// DefaultBinaryPathParams returns the terminaltexteffects defaults:
// green digits, a white collapse to half brightness and 8% of the
// characters in flight at once.
func DefaultBinaryPathParams() BinaryPathParams {
	return BinaryPathParams{
		BinaryColors: []term.Color{
			term.NewRGBColor(0x04, 0x4e, 0x29),
			term.NewRGBColor(0x15, 0x7e, 0x38),
			term.NewRGBColor(0x45, 0xbf, 0x55),
			term.NewRGBColor(0x95, 0xed, 0x87),
		},
		CollapseColor:  term.ColorWhite,
		DimBrightness:  0.5,
		ActiveGroups:   0.08,
		TravelDuration: 0.85,
		Seed:           1,
	}
}

type binaryPath struct {
	BinaryPathParams
	defaultAttr term.Attributes
	from        backdrop
}

// Steps, in cells travelled per digit, that the collapse and the wipe
// brightening take, matching terminaltexteffects' frame counts.
const (
	binaryCollapseSteps = 21
	binaryBrightenSteps = 20
)

// binaryRoute is the path a character's digits take to it.
type binaryRoute struct {
	// legs are the corners of the path, ending at the character.
	legs [][2]int
	// length is the distance along the path, in cells.
	length int
}

func (s *binaryPath) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *binaryPath) ShadeImages(frame, total int, cells [][]term.Cell, images []term.Image) []term.Image {
	return s.from.reveal(frame, total, cells, images, s.shade)
}

func (s *binaryPath) shade(t float64, cells [][]term.Cell) uncover {
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return nil
	}
	glyphs := collectContent(cells, s.defaultAttr.Bg)
	lanes := max(1, int(s.ActiveGroups*float64(len(glyphs))))
	ranks := shuffledRanks(s.Seed, 2, glyphs)
	order := make([]int, len(glyphs))
	for i, rank := range ranks {
		order[rank] = i
	}
	// Each lane delivers its characters back to back; a delivery lasts
	// the path length plus one step per digit of the train.
	routes := make([]binaryRoute, len(glyphs))
	digits := make([]string, len(glyphs))
	startAt := make([]float64, len(glyphs))
	laneEnd := make([]float64, lanes)
	for rank, i := range order {
		g := glyphs[i]
		routes[i] = s.route(g, rows, cols)
		digits[i] = fmt.Sprintf("%08b", g.cell.Ch)
		lane := rank % lanes
		startAt[i] = laneEnd[lane]
		laneEnd[lane] += float64(routes[i].length + len(digits[i]))
	}
	travelEnd := 0.0
	for _, e := range laneEnd {
		travelEnd = max(travelEnd, e)
	}
	travelEnd += binaryCollapseSteps
	step := window(t, 0, s.TravelDuration) * travelEnd
	wipeSteps := float64((rows+cols-1+1)/2 + binaryBrightenSteps)
	wipe := window(t, s.TravelDuration, 1-s.TravelDuration) * wipeSteps

	delivered := make(map[[2]int]float64, len(glyphs))
	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		fg := resolveFg(g.cell.Fg, s.defaultAttr.Fg)
		dim := shaderutils.AdjustBrightness(fg, s.DimBrightness, s.defaultAttr.Fg)
		done := startAt[i] + float64(len(digits[i])-1+routes[i].length)
		delivered[[2]int{g.x, g.y}] = s.TravelDuration * (done + 1) / travelEnd
		if step < done+1 {
			places[i] = placement{hidden: true}
			continue
		}
		places[i] = placement{x: g.x, y: g.y}
		collapse := shaderutils.EaseInQuad(window(step, done+1, binaryCollapseSteps))
		places[i].fg = shaderutils.InterpolateColor(collapse, s.CollapseColor, dim, s.defaultAttr.Fg)
		if wipe > 0 {
			brighten := window(wipe, float64((cols-1-g.x+g.y)/2), binaryBrightenSteps)
			places[i].fg = fadeFg(brighten, dim, g.cell.Fg, s.defaultAttr.Fg)
		}
	}
	s.from.place(cells, glyphs, places)
	for i, g := range glyphs {
		done := startAt[i] + float64(len(digits[i])-1+routes[i].length)
		if step < startAt[i] || step >= done+1 {
			continue
		}
		for d, digit := range digits[i] {
			travelled := step - startAt[i] - float64(d)
			if travelled < 0 {
				break
			}
			x, y := routes[i].at(min(int(travelled), routes[i].length))
			fg := s.BinaryColors[pick3(s.Seed, g.x, g.y, 200+d, len(s.BinaryColors))]
			putChar(cells, x, y, digit, fg)
		}
	}
	// The characters show over the backdrop as they are delivered, and
	// the wipe sweeps away what is left of it.
	return func(x, y int) float64 {
		if at, ok := delivered[[2]int{x, y}]; ok {
			return at
		}
		return s.TravelDuration + (1-s.TravelDuration)*float64((cols-1-x+y)/2)/wipeSteps
	}
}

// route plots the digits' path to g: from a cell outside the canvas,
// legs alternate between long vertical and short horizontal moves
// toward g until it is reached.
func (s *binaryPath) route(g glyph, rows, cols int) binaryRoute {
	x, y := outsideCoord(s.Seed, g.y*cols+g.x, rows, cols)
	r := binaryRoute{legs: [][2]int{{x, y}}}
	maxVertical := max(10, int(float64(cols)*0.2))
	vertical := pick3(s.Seed, g.x, g.y, 99, 2) == 0
	for draw := 100; x != g.x || y != g.y; draw++ {
		dx, dy := g.x-x, g.y-y
		switch {
		case vertical && dy != 0:
			n := 1 + pick3(s.Seed, g.x, g.y, draw, min(abs(dy), maxVertical))
			y += n * sign(dy)
			r.length += n
		case !vertical && dx != 0:
			n := 1 + pick3(s.Seed, g.x, g.y, draw, min(abs(dx), 4))
			x += n * sign(dx)
			r.length += n
		default:
			r.length += abs(dx) + abs(dy)
			x, y = g.x, g.y
		}
		vertical = !vertical
		r.legs = append(r.legs, [2]int{x, y})
	}
	return r
}

// at returns the cell dist cells along the route, which must be within
// its length.
func (r binaryRoute) at(dist int) (x, y int) {
	x, y = r.legs[0][0], r.legs[0][1]
	for _, leg := range r.legs[1:] {
		n := abs(leg[0]-x) + abs(leg[1]-y)
		if dist <= n {
			x += sign(leg[0]-x) * min(dist, abs(leg[0]-x))
			y += sign(leg[1]-y) * min(dist, abs(leg[1]-y))
			return x, y
		}
		dist -= n
		x, y = leg[0], leg[1]
	}
	return x, y
}

// sign returns -1, 0 or 1 by the sign of v.
func sign(v int) int {
	switch {
	case v < 0:
		return -1
	case v > 0:
		return 1
	}
	return 0
}
