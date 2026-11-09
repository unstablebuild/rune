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

// MiddleOut grows the text out of the center of the canvas in two
// moves: every character first spreads from the center along the middle
// row (or column, when Horizontal), then rises or sinks to its place,
// its color warming from StartColor to its own during the second move,
// which uncovers [MiddleOutParams.From] from the middle line out.
//
// The last frame of the animation matches the input exactly.
//
// Zero params take their defaults; params out of their documented range
// panic with an error wrapping [ErrInvalidParams].
//
// Inspired by terminaltexteffects' "middleout" effect.
func MiddleOut(params MiddleOutParams, defaultAttr term.Attributes) ImageShader {
	d := DefaultMiddleOutParams()
	params.StartColor = orDefault(params.StartColor, d.StartColor)
	params.CenterEasing = checkEasing("MiddleOutParams.CenterEasing", params.CenterEasing, d.CenterEasing)
	params.FullEasing = checkEasing("MiddleOutParams.FullEasing", params.FullEasing, d.FullEasing)
	params.CenterDuration = checkDuration("MiddleOutParams.CenterDuration",
		orDefault(params.CenterDuration, d.CenterDuration))
	from := newBackdrop(params.From, defaultAttr)
	params.From = Snapshot{}
	return &middleOut{MiddleOutParams: params, defaultAttr: defaultAttr, from: from}
}

// MiddleOutParams configures [MiddleOut].
type MiddleOutParams struct {
	// StartColor is the color every character has until it leaves the
	// middle line.
	StartColor term.Color
	// Horizontal spreads the characters along the middle column first,
	// so the second move is horizontal, instead of along the middle row.
	Horizontal bool
	// CenterEasing shapes the first move, FullEasing the second.
	CenterEasing shaderutils.Easing
	FullEasing   shaderutils.Easing
	// CenterDuration is the fraction of the animation the first move
	// takes; the second takes the rest. (range (0, 1))
	CenterDuration float64
	// From is the screen the text grows out over instead of a blank one.
	From Snapshot
}

// DefaultMiddleOutParams returns the terminaltexteffects defaults: a
// white start, vertical expansion and in-out sine easings.
func DefaultMiddleOutParams() MiddleOutParams {
	return MiddleOutParams{
		StartColor:     term.NewRGBColor(255, 255, 255),
		CenterEasing:   shaderutils.EaseInOutSine,
		FullEasing:     shaderutils.EaseInOutSine,
		CenterDuration: 0.5,
	}
}

type middleOut struct {
	MiddleOutParams
	defaultAttr term.Attributes
	from        backdrop
}

func (s *middleOut) Shade(frame, total int, cells [][]term.Cell) {
	s.ShadeImages(frame, total, cells, nil)
}

func (s *middleOut) ShadeImages(frame, total int, cells [][]term.Cell, images []term.Image) []term.Image {
	return s.from.reveal(frame, total, cells, images, s.shade)
}

func (s *middleOut) shade(t float64, cells [][]term.Cell) uncover {
	rows, cols := canvasSize(cells)
	if rows == 0 || cols == 0 {
		return nil
	}
	glyphs := collectContent(cells, s.defaultAttr.Bg)
	cx, cy := float64(cols-1)/2, float64(rows-1)/2
	firstSpan, secondSpan := cx, cy*rowScale
	if s.Horizontal {
		firstSpan, secondSpan = secondSpan, firstSpan
	}
	// middle is the point of the middle line a character at gx, gy
	// reaches after the first move.
	middle := func(gx, gy float64) (mx, my float64) {
		if s.Horizontal {
			return cx, gy
		}
		return gx, cy
	}
	// second is how long the second move to gx, gy takes.
	second := func(gx, gy float64) float64 {
		dur := 1 - s.CenterDuration
		if secondSpan > 0 {
			mx, my := middle(gx, gy)
			dur *= cellDistance(mx, my, gx, gy) / secondSpan
		}
		return dur
	}
	places := make([]placement, len(glyphs))
	for i, g := range glyphs {
		gx, gy := float64(g.x), float64(g.y)
		mx, my := middle(gx, gy)
		dur := s.CenterDuration
		if firstSpan > 0 {
			dur *= cellDistance(cx, cy, mx, my) / firstSpan
		}
		p1 := s.CenterEasing(window(t, 0, dur))
		p2 := window(t, s.CenterDuration, second(gx, gy))
		e2 := s.FullEasing(p2)
		x := lerp(lerp(cx, mx, p1), gx, e2)
		y := lerp(lerp(cy, my, p1), gy, e2)
		places[i] = placement{
			x:  roundInt(x),
			y:  roundInt(y),
			fg: fadeFg(p2, s.StartColor, g.cell.Fg, s.defaultAttr.Fg),
		}
	}
	s.from.place(cells, glyphs, places)
	// The backdrop gives way from the middle line out as the second move
	// settles the characters.
	return func(x, y int) float64 {
		return s.CenterDuration + second(float64(x), float64(y))
	}
}
