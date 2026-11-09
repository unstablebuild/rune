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
	"sort"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// SlideGrouping is the lines [Slide] slides the text in by.
type SlideGrouping uint8

const (
	// SlideRows slides rows in from the left, top row first.
	SlideRows SlideGrouping = iota
	// SlideColumns slides columns in from the top, left column first.
	SlideColumns
	// SlideDiagonals slides diagonals in from below the bottom-left
	// corner, the one nearest the top-left corner first.
	SlideDiagonals
)

// Slide slides the text in line by line: each line's characters enter
// from an edge one after the other, farthest first, and glide to their
// place with an easing, their color warming from StartColor to their
// own. The first of each line wipes [SlideParams.From] away as it
// glides in.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults; params out of their documented range
// panic with an error wrapping [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "slide" effect.
func Slide(params SlideParams, defaultAttr term.Attributes) ImageShader {
	d := DefaultSlideParams()
	if params.Grouping > SlideDiagonals {
		invalidParam("SlideParams.Grouping", params.Grouping, "a SlideGrouping")
	}
	params.Gap = capCount("SlideParams.Gap", orDefault(params.Gap, d.Gap), 1, 1024)
	params.Easing = checkEasing("SlideParams.Easing", params.Easing, d.Easing)
	params.StartColor = orDefault(params.StartColor, d.StartColor)
	params.SlideDuration = checkDuration("SlideParams.SlideDuration", orDefault(params.SlideDuration, d.SlideDuration))
	from := newBackdrop(params.From, defaultAttr)
	params.From = Snapshot{}
	return &slide{SlideParams: params, defaultAttr: defaultAttr, from: from}
}

// SlideParams configures [Slide].
type SlideParams struct {
	// Grouping is one of the SlideGrouping constants.
	Grouping SlideGrouping
	// Gap is how many characters a line lets in before the next line
	// starts. (range [1, ∞), capped at 1024)
	Gap int
	// ReverseDirection slides the lines in from the opposite edge.
	ReverseDirection bool
	// Merge alternates the edge lines slide in from, so they meet in
	// the middle. It overrides ReverseDirection.
	Merge bool
	// Easing shapes each character's glide.
	Easing shaderutils.Easing
	// StartColor is the color every character has as it enters.
	StartColor term.Color
	// SlideDuration is the fraction of the animation a character takes
	// to cross the whole canvas; the lines are let in over the rest.
	// (range (0, 1))
	SlideDuration float64
	// From is the screen the text slides in over instead of a blank one.
	From Snapshot
}

// DefaultSlideParams returns the terminaltexteffects defaults: rows
// from the left, two characters apart, an in-out quad easing and the
// first stop of its final gradient as start color.
func DefaultSlideParams() SlideParams {
	return SlideParams{
		Grouping:      SlideRows,
		Gap:           2,
		Easing:        shaderutils.EaseInOutQuad,
		StartColor:    term.NewRGBColor(0x83, 0x3a, 0xb4),
		SlideDuration: 0.3,
	}
}

type slide struct {
	SlideParams
	defaultAttr term.Attributes
	from        backdrop
}

func (s *slide) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *slide) ShadeImages(frame, total int, cells [][]term.Cell, images []term.Image) []term.Image {
	return s.from.reveal(frame, total, cells, images, s.shade)
}

func (s *slide) shade(t float64, cells [][]term.Cell) uncover {
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return nil
	}
	glyphs := collectContent(cells, s.defaultAttr.Bg)
	// fromFar tells whether a line enters from the far edge: the right,
	// the bottom, or above the top-right corner.
	fromFar := func(group int) bool {
		if s.Merge {
			return group%2 == 0
		}
		return s.ReverseDirection
	}
	groupOf := func(x, y int) int {
		switch s.Grouping {
		case SlideColumns:
			return x
		case SlideDiagonals:
			return x + y
		default:
			return y
		}
	}
	// startOf is where the character of a line at x, y enters from.
	startOf := func(x, y int) (sx, sy float64) {
		far := fromFar(groupOf(x, y))
		switch s.Grouping {
		case SlideColumns:
			if far {
				return float64(x), float64(rows)
			}
			return float64(x), -1
		case SlideDiagonals:
			if far {
				return float64(x + y + 1), -1
			}
			return float64(x + y - rows), float64(rows)
		default:
			if far {
				return float64(cols), float64(y)
			}
			return -1, float64(y)
		}
	}
	// travel is how far the character of a line at x, y glides.
	travel := func(x, y int) float64 {
		sx, sy := startOf(x, y)
		return cellDistance(sx, sy, float64(x), float64(y))
	}
	// Characters enter farthest from the edge first. Ranking within a
	// line by the distance to its start does that for every grouping.
	ranks := ranksBy(glyphs, func(g glyph) (int, int) {
		return groupOf(g.x, g.y), -roundInt(travel(g.x, g.y) * 16)
	})
	groupSizes := make(map[int]int)
	for _, g := range glyphs {
		groupSizes[groupOf(g.x, g.y)]++
	}
	groupIDs := make([]int, 0, len(groupSizes))
	for id := range groupSizes {
		groupIDs = append(groupIDs, id)
	}
	sort.Ints(groupIDs)
	groupOrdinal := make(map[int]int, len(groupIDs))
	maxSize, before := 0, 0
	indexBase := make(map[int]int, len(groupIDs))
	for ordinal, id := range groupIDs {
		groupOrdinal[id] = ordinal
		indexBase[id] = before
		before += groupSizes[id]
		maxSize = max(maxSize, groupSizes[id])
	}
	// A line lets its characters in one step apart, the next line
	// starting Gap steps after it, and the last one in must still have
	// time to cross.
	steps := (len(groupIDs)-1)*s.Gap + maxSize - 1
	stepT := 0.0
	if steps > 0 {
		stepT = (1 - s.SlideDuration) / float64(steps)
	}
	// Every character crosses at the same speed, the longest way taking
	// SlideDuration. Diagonals entering above the top-right corner can
	// travel farther than the canvas is wide or tall.
	span := 0.0
	for _, g := range glyphs {
		span = max(span, travel(g.x, g.y))
	}
	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		id := groupOf(g.x, g.y)
		index := ranks[i] - indexBase[id]
		start := float64(groupOrdinal[id]*s.Gap+index) * stepT
		if t < start {
			places[i] = placement{hidden: true}
			continue
		}
		sx, sy := startOf(g.x, g.y)
		// Rounding must not push the last character in past the end.
		dur := math.Min(s.SlideDuration*travel(g.x, g.y)/span, 1-start)
		p := window(t, start, dur)
		e := s.Easing(p)
		places[i] = placement{
			x:  roundInt(lerp(sx, float64(g.x), e)),
			y:  roundInt(lerp(sy, float64(g.y), e)),
			fg: fadeFg(p, s.StartColor, g.cell.Fg, s.defaultAttr.Fg),
		}
	}
	s.from.place(cells, glyphs, places)
	// The first character of a line wipes the backdrop away as it glides
	// in, and a line without any as the next one does. The farthest
	// glide is from a corner.
	reach := span
	for _, c := range [][2]int{{0, 0}, {cols - 1, 0}, {0, rows - 1}, {cols - 1, rows - 1}} {
		reach = max(reach, travel(c[0], c[1]))
	}
	lines := float64(groupOf(cols-1, rows-1) + 1)
	return func(x, y int) float64 {
		id := groupOf(x, y)
		start := (1 - s.SlideDuration) * float64(id) / lines
		if len(groupIDs) > 0 {
			start = min(float64(sort.SearchInts(groupIDs, id)*s.Gap)*stepT, 1-s.SlideDuration)
		}
		return start + s.SlideDuration*travel(x, y)/reach
	}
}
