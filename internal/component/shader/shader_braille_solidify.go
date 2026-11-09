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
	"container/heap"
	"image"
	"math"
	"math/rand/v2"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// BrailleSolidifyParams configures [BrailleSolidify]. Times are fractions
// of the animation's length.
type BrailleSolidifyParams struct {
	// Seed fixes the path the front takes and the sparks it throws, so
	// equal params replay the same animation.
	Seed uint64
	// Lead is how long cells flicker before the front reaches them.
	Lead float64
	// Melt is how long a cell takes to turn into the wallpaper once the
	// front reaches it. When Lead and Melt add up to more than the whole
	// animation, both shrink in proportion to fit.
	Melt float64
	// Roughness, from 0 to 1, is how much the time the front takes to
	// cross a cell varies. An even front sweeps the art, while a rough one
	// branches and races ahead along the quickest cells.
	Roughness float64
	// Glow is the color cells flash as the front reaches them.
	Glow term.Color
}

// DefaultBrailleSolidifyParams returns the params of the Rune intro.
func DefaultBrailleSolidifyParams() BrailleSolidifyParams {
	return BrailleSolidifyParams{
		Seed:      1,
		Lead:      0.06,
		Melt:      0.2,
		Roughness: 0.85,
		Glow:      term.NewRGBColor(255, 255, 255),
	}
}

const (
	// brailleGapCost is how many times slower the front crosses cells
	// without dots than cells of the art.
	brailleGapCost = 12
	// brailleSparks is how far through its melt a cell stops throwing
	// sparks and starts cooling down in its final shape.
	brailleSparks = 0.35
	// brailleFlash is how far through its cooling a cell turning into a
	// block flashes a full block.
	brailleFlash         = 0.12
	brailleFullBlock     = '█'
	brailleLeadFlicker   = 0.3
	brailleLeadHeat      = 0.3
	brailleMaxRoughness  = 0.95
	brailleShortestMelt  = 1e-3
	brailleFrontSeedSalt = 0x6a09e667f3bcc909
)

// BrailleSolidify is a Shader that turns the braille rendering of the
// wallpaper drawn by art into the wallpaper on screen. The wallpaper and
// art are as for [BrailleSwap].
//
// A glowing front enters the art at its topmost cell and spreads through
// it by first-passage percolation: every cell takes a random time to
// cross, and the front reaches each cell along the quickest path there.
// It follows the shape of the art with a ragged, branching edge, and only
// jumps the gaps between parts of the art, which are far slower to cross,
// where the art offers no path. Cells flicker as the front nears, throw
// sparks and flash Glow as it reaches them, then cool down into the
// wallpaper.
//
// The first frame shows the braille rendering and the last the wallpaper
// as drawn. Cells drawn over the wallpaper, such as a prompt, are left
// untouched, as are screens without blank braille cells.
func BrailleSolidify(
	params BrailleSolidifyParams, defaultAttr term.Attributes, art tui.Component,
) Shader {
	return &brailleSolidify{
		BrailleSolidifyParams: params,
		defaultAttr:           defaultAttr,
		art:                   brailleArtCache{art: art},
	}
}

type brailleSolidify struct {
	BrailleSolidifyParams
	defaultAttr term.Attributes
	art         brailleArtCache

	// region is the area of the wallpaper the fields below describe.
	region image.Rectangle
	// arrival is when the front reaches each cell of region, row by row,
	// from 0 at the topmost cell of the art to 1 at the last cell of the
	// art it reaches.
	arrival []float64
	// seeds gives each cell of region its own sparks.
	seeds []uint32
}

func (s *brailleSolidify) Shade(frame, total int, cells [][]term.Cell) {
	if frame < 0 || frame >= total-1 {
		return
	}
	region, art, ok := s.art.find(cells)
	if !ok {
		return
	}
	if region != s.region || s.arrival == nil {
		s.spread(region, art)
	}
	progress := float64(frame) / float64(total-1)
	lead := math.Max(0, s.Lead)
	melt := math.Max(brailleShortestMelt, s.Melt)
	if sum := lead + melt; sum > 1 {
		lead, melt = lead/sum, melt/sum
	}

	cols := region.Dx()
	for y := region.Min.Y; y < region.Max.Y && y < len(cells); y++ {
		for x := region.Min.X; x < region.Max.X && x < len(cells[y]); x++ {
			c := &cells[y][x]
			a, ok := brailleArtCell(art, x-region.Min.X, y-region.Min.Y)
			if !ok || !isBrailleWallpaper(*c) || a.Ch == c.Ch && a.Ch == braille.Blank {
				continue
			}
			i := (y-region.Min.Y)*cols + x - region.Min.X
			start := lead + s.arrival[i]*(1-lead-melt)
			s.drawCell(c, a, (progress-start)/melt, lead/melt, frame, s.seeds[i])
		}
	}
}

// spread works out when the front reaches each cell of region, given the
// braille rendering of the wallpaper there.
func (s *brailleSolidify) spread(region image.Rectangle, art [][]term.Cell) {
	s.region = region
	cols, rows := region.Dx(), region.Dy()
	n := cols * rows
	rng := rand.New(rand.NewPCG(s.Seed, brailleFrontSeedSalt))
	roughness := math.Min(brailleMaxRoughness, math.Max(0, s.Roughness))

	cost := make([]float64, n)
	inArt := make([]bool, n)
	s.seeds = make([]uint32, n)
	source := -1
	for i := range n {
		a, ok := brailleArtCell(art, i%cols, i/cols)
		inArt[i] = ok && braille.IsPattern(a.Ch) && a.Ch != braille.Blank
		cost[i] = 1 + roughness*(2*rng.Float64()-1)
		if !inArt[i] {
			cost[i] = brailleGapCost
		} else if source < 0 {
			source = i
		}
		s.seeds[i] = rng.Uint32()
	}

	s.arrival = make([]float64, n)
	if source < 0 {
		return
	}
	reach := frontReach(cost, cols, rows, source, asciiart.HeightToWidthCellAspectRatio)
	var last float64
	for i, t := range reach {
		if inArt[i] {
			last = math.Max(last, t)
		}
	}
	if last == 0 {
		return
	}
	for i, t := range reach {
		s.arrival[i] = math.Min(1, t/last)
	}
}

// drawCell draws over c, a cell of the wallpaper whose braille rendering
// is art, the cell as of u, how far it is through its melt. Below -lead
// the cell shows art, and from 1 on it is left as drawn.
func (s *brailleSolidify) drawCell(
	c *term.Cell, art term.Cell, u, lead float64, frame int, seed uint32,
) {
	switch {
	case u <= -lead:
		c.Ch, c.Fg = art.Ch, art.Fg
	case u >= 1:
	case u < 0:
		k := 1 + u/lead
		c.Ch = art.Ch &^ brailleSparksAt(seed, frame, brailleLeadFlicker*k)
		c.Fg = s.heat(art.Fg, brailleLeadHeat*k)
	case u < brailleSparks:
		k := u / brailleSparks
		c.Ch = art.Ch | brailleSparksAt(seed, frame, k)
		c.Fg = s.heat(art.Fg, brailleLeadHeat+(1-brailleLeadHeat)*k)
	default:
		k := (u - brailleSparks) / (1 - brailleSparks)
		glow := (1 - k) * (1 - k)
		if c.Ch == braille.Block {
			if k < brailleFlash {
				c.Ch = brailleFullBlock
			}
			c.Fg = s.heat(c.Fg, glow)
			return
		}
		c.Ch = art.Ch &^ brailleDotsOut(seed, k)
		c.Fg = s.heat(art.Fg, glow)
	}
}

// heat blends color toward Glow by factor, from 0 for color to 1 for Glow.
func (s *brailleSolidify) heat(color term.Color, factor float64) term.Color {
	return shaderutils.InterpolateColor(factor, color, s.Glow, s.defaultAttr.Fg)
}

// brailleSparksAt returns the dots a cell sparks in a frame, each raised
// with the given chance and changing every frame.
func brailleSparksAt(seed uint32, frame int, chance float64) rune {
	var dots rune
	for bit := range braille.CellDotsX * braille.CellDotsY {
		if brailleHash(seed^uint32(frame)*0x9e3779b1^uint32(bit+1)*0x85ebca6b) < chance {
			dots |= 1 << bit
		}
	}
	return dots
}

// brailleDotsOut returns the dots of a cell that went out by k, from none
// at 0 to all at 1. A dot that goes out stays out as k grows.
func brailleDotsOut(seed uint32, k float64) rune {
	var dots rune
	for bit := range braille.CellDotsX * braille.CellDotsY {
		if brailleHash(seed^uint32(bit+1)*0xc2b2ae35) < k {
			dots |= 1 << bit
		}
	}
	return dots
}

// frontReach returns how long a front starting at source takes to reach
// every cell of a cols by rows grid, crossing each cell in its cost per
// cell width. The front moves between neighboring cells, diagonals
// included, and cells are aspect times taller than wide.
func frontReach(cost []float64, cols, rows, source int, aspect float64) []float64 {
	diagonal := math.Hypot(1, aspect)
	steps := [...]struct {
		dx, dy int
		length float64
	}{
		{-1, 0, 1}, {1, 0, 1}, {0, -1, aspect}, {0, 1, aspect},
		{-1, -1, diagonal}, {1, -1, diagonal}, {-1, 1, diagonal}, {1, 1, diagonal},
	}

	reach := make([]float64, len(cost))
	for i := range reach {
		reach[i] = math.Inf(1)
	}
	reach[source] = 0
	queue := frontQueue{{cell: source}}
	for queue.Len() > 0 {
		cur := heap.Pop(&queue).(frontStop)
		if cur.time > reach[cur.cell] {
			continue
		}
		x, y := cur.cell%cols, cur.cell/cols
		for _, step := range steps {
			nx, ny := x+step.dx, y+step.dy
			if nx < 0 || ny < 0 || nx >= cols || ny >= rows {
				continue
			}
			next := ny*cols + nx
			t := cur.time + step.length*(cost[cur.cell]+cost[next])/2
			if t < reach[next] {
				reach[next] = t
				heap.Push(&queue, frontStop{cell: next, time: t})
			}
		}
	}
	return reach
}

type frontStop struct {
	cell int
	time float64
}

// frontQueue is a [heap.Interface] of the cells the front reached,
// earliest first.
type frontQueue []frontStop

func (q frontQueue) Len() int           { return len(q) }
func (q frontQueue) Less(i, j int) bool { return q[i].time < q[j].time }
func (q frontQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }

func (q *frontQueue) Push(x any) { *q = append(*q, x.(frontStop)) }

func (q *frontQueue) Pop() any {
	old := *q
	last := old[len(old)-1]
	*q = old[:len(old)-1]
	return last
}
