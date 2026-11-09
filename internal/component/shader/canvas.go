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
	"errors"
	"fmt"
	"math"
	"sort"
	"unicode"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// ErrInvalidParams is wrapped by the error the shader constructors
// panic with when their params break the contract documented on their
// fields: a value that is NaN, infinite or out of its range, phases
// that do not fit in the animation, an unknown enum value, a symbol
// that does not draw as a single cell, or an easing that does not run
// from 0 to 1. Such params are programmer errors; values within range
// that would only make a shader costly are capped instead.
var ErrInvalidParams = errors.New("invalid shader params")

// invalidParam panics with an error wrapping ErrInvalidParams for a
// field set to v, short of want.
func invalidParam(field string, v any, want string) {
	panic(fmt.Errorf("%w: %s is %v, want %s", ErrInvalidParams, field, v, want))
}

// orDefault returns def when v is the zero value, and v otherwise.
func orDefault[T comparable](v, def T) T {
	var zero T
	if v == zero {
		return def
	}
	return v
}

// orDefaults returns def when v is empty, and v otherwise.
func orDefaults[T any](v, def []T) []T {
	if len(v) == 0 {
		return def
	}
	return v
}

// checkFloat returns v, panicking unless it is finite and between lo
// and hi, each included unless open.
func checkFloat(field string, v, lo, hi float64, loOpen, hiOpen bool) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < lo || v > hi || (loOpen && v == lo) || (hiOpen && v == hi) {
		lb, hb := "[", "]"
		if loOpen {
			lb = "("
		}
		if hiOpen {
			hb = ")"
		}
		invalidParam(field, v, fmt.Sprintf("a value in %s%v, %v%s", lb, lo, hi, hb))
	}
	return v
}

// checkDuration returns v, a fraction of the animation that a phase
// takes, panicking unless it is in (0, 1).
func checkDuration(field string, v float64) float64 {
	return checkFloat(field, v, 0, 1, true, true)
}

// checkPhases panics unless phases taking fractions summing to sum of
// the animation fit in it, leaving room for the last one.
func checkPhases(fields string, sum float64) {
	if !(sum < 1) {
		invalidParam(fields, sum, "a sum below 1")
	}
}

// checkBrightness returns v, a factor dimming a color, panicking unless
// it is in (0, 1].
func checkBrightness(field string, v float64) float64 {
	return checkFloat(field, v, 0, 1, true, false)
}

// capPositive returns v clamped to [lo, hi], panicking unless it is
// finite and positive.
func capPositive(field string, v, lo, hi float64) float64 {
	checkFloat(field, v, 0, math.MaxFloat64, true, false)
	return math.Max(lo, math.Min(v, hi))
}

// capCount returns n capped at hi, panicking when it is below lo.
func capCount(field string, n, lo, hi int) int {
	if n < lo {
		invalidParam(field, n, fmt.Sprintf("at least %d", lo))
	}
	return min(n, hi)
}

// checkSymbol returns ch, or def when ch is zero, panicking unless it is
// a printable character drawn in a single cell.
func checkSymbol(field string, ch, def rune) rune {
	ch = orDefault(ch, def)
	if !utf8.ValidRune(ch) || !unicode.IsPrint(ch) || graphemecluster.StringWidth(string(ch)) != 1 {
		invalidParam(field, fmt.Sprintf("%U", ch), "a printable character a single cell wide")
	}
	return ch
}

// checkSymbols returns symbols, or def when there are none, panicking
// unless each is a printable character drawn in a single cell.
func checkSymbols(field string, symbols, def []rune) []rune {
	symbols = orDefaults(symbols, def)
	for _, ch := range symbols {
		checkSymbol(field, ch, ch)
	}
	return symbols
}

// checkEasing returns e, or def when e is nil, panicking unless it
// honors the shaderutils.Easing contract at evenly spaced points.
func checkEasing(field string, e, def shaderutils.Easing) shaderutils.Easing {
	if e == nil {
		e = def
	}
	const samples, tolerance = 64, 1e-9
	for i := 0; i <= samples; i++ {
		t := float64(i) / samples
		v := e(t)
		switch {
		case math.IsNaN(v) || v < -1 || v > 2:
			invalidParam(field, v, fmt.Sprintf("an eased value in [-1, 2] at %v", t))
		case i == 0 && math.Abs(v) > tolerance:
			invalidParam(field, v, "0 at 0")
		case i == samples && math.Abs(v-1) > tolerance:
			invalidParam(field, v, "1 at 1")
		}
	}
	return e
}

// rowScale is how many columns a row spans visually, used to keep
// circles round and distances isotropic on the cell grid.
const rowScale = asciiart.HeightToWidthCellAspectRatio

// progress returns how far into the animation frame is, in [0, 1], and
// false when the frame is past it (frame >= total) or there is no
// animation (total <= 0). Frames before the first clamp to it and the
// last frame, total-1, maps to 1 so the final frame shows the finished
// effect; a single frame animation is all last frame.
func progress(frame, total int) (float64, bool) {
	if total <= 0 || frame >= total {
		return 0, false
	}
	if total == 1 {
		return 1, true
	}
	if frame < 0 {
		return 0, true
	}
	return float64(frame) / float64(total-1), true
}

// canvasSize returns the number of rows in cells and the width of its
// widest row.
func canvasSize(cells [][]term.Cell) (rows, cols int) {
	for _, row := range cells {
		cols = max(cols, len(row))
	}
	return len(cells), cols
}

// glyph is a cell of content of the shaded cells and where it is: a
// character, or a blank painted with a background of its own.
type glyph struct {
	x, y int
	cell term.Cell
}

// hasContent tells whether c shows something against a canvas whose
// background is bg: a character, or a background of its own.
func hasContent(c term.Cell, bg term.Color) bool {
	return hasOriginalChar(c) || (c.Width != 0 && c.Bg != term.ColorDefault && c.Bg != bg)
}

// collectContent returns the cells of cells with content against the
// canvas background bg in reading order: the characters and the blank
// cells painted with a background of their own, so an effect that moves
// or hides the characters carries the painted backgrounds along rather
// than leaving them behind.
func collectContent(cells [][]term.Cell, bg term.Color) []glyph {
	var out []glyph
	for y, row := range cells {
		for x, c := range row {
			if hasContent(c, bg) {
				out = append(out, glyph{x: x, y: y, cell: c})
			}
		}
	}
	return out
}

// inCanvas tells whether x, y addresses a cell of cells.
func inCanvas(cells [][]term.Cell, x, y int) bool {
	return y >= 0 && y < len(cells) && x >= 0 && x < len(cells[y])
}

// claim readies the cell at x, y to be overwritten by a single cell
// character, so that no wide character is left half drawn: a wide
// character there loses its continuation cells, and one whose
// continuation is there is blanked. Both keep their backgrounds.
func claim(cells [][]term.Cell, x, y int) {
	row := cells[y]
	lead := x
	if row[x].Width == 0 {
		lead = -1
		for i := x - 1; i >= max(0, x-math.MaxUint8+1); i-- {
			if row[i].Width != 0 {
				if i+int(row[i].Width) > x {
					lead = i
				}
				break
			}
		}
		if lead < 0 {
			return
		}
	}
	width := int(row[lead].Width)
	if width < 2 {
		return
	}
	for i := range width {
		if lead+i >= len(row) || (i > 0 && row[lead+i].Width != 0) {
			break
		}
		c := &row[lead+i]
		c.Ch = ' '
		c.Width = 1
		c.Extra = nil
	}
}

// blankCell clears the cell at x, y, and the continuation cells of a
// wide character there, down to the canvas background bg.
func blankCell(cells [][]term.Cell, x, y int, bg term.Color) {
	if !inCanvas(cells, x, y) {
		return
	}
	row := cells[y]
	n := 1
	for n < int(row[x].Width) && x+n < len(row) && row[x+n].Width == 0 {
		n++
	}
	claim(cells, x, y)
	for i := range n {
		c := &row[x+i]
		c.Ch = ' '
		c.Width = 1
		c.Extra = nil
		c.Attrs = term.AttrNone
		c.Bg = bg
	}
}

// putGlyph draws g at x, y showing ch, or its own character when ch is
// zero, colored fg over its own background, when x, y is within cells.
// A wide character claims its continuation cells.
func putGlyph(cells [][]term.Cell, x, y int, g glyph, ch rune, fg term.Color) {
	if !inCanvas(cells, x, y) {
		return
	}
	row := cells[y]
	width := 1
	if ch == 0 {
		width = max(1, int(g.cell.Width))
	}
	for i := 0; i < width && x+i < len(row); i++ {
		claim(cells, x+i, y)
	}
	c := &row[x]
	if ch != 0 {
		c.Ch = ch
		c.Width = 1
		c.Extra = nil
	} else {
		c.Ch = g.cell.Ch
		c.Width = g.cell.Width
		c.Extra = g.cell.Extra
	}
	c.Fg = fg
	c.Bg = g.cell.Bg
	c.Attrs = g.cell.Attrs &^ burnGlyphStripAttrs
	for i := 1; i < width && x+i < len(row); i++ {
		row[x+i] = term.Cell{Bg: g.cell.Bg}
	}
}

// putChar draws ch at x, y colored fg over the background there, when
// x, y is within cells.
func putChar(cells [][]term.Cell, x, y int, ch rune, fg term.Color) {
	if !inCanvas(cells, x, y) {
		return
	}
	claim(cells, x, y)
	c := &cells[y][x]
	c.Ch = ch
	c.Width = 1
	c.Extra = nil
	c.Fg = fg
	c.Attrs &^= burnGlyphStripAttrs
}

// paintFg recolors the character at x, y, when x, y is within cells.
func paintFg(cells [][]term.Cell, x, y int, fg term.Color) {
	if inCanvas(cells, x, y) {
		cells[y][x].Fg = fg
	}
}

// placement is where a glyph shows on a frame, and how. Its background
// goes wherever it shows.
type placement struct {
	x, y int
	// ch replaces the glyph's character when not zero.
	ch rune
	fg term.Color
	// hidden leaves the glyph out of the frame.
	hidden bool
}

// place redraws glyphs at their placements on a canvas whose background
// is bg. Glyphs hidden or away from their position are cleared from it
// first, down to bg; those in place are only recolored, then the others
// are drawn in order, over them.
func place(cells [][]term.Cell, glyphs []glyph, places []placement, bg term.Color) {
	for i, g := range glyphs {
		p := places[i]
		if p.hidden || p.x != g.x || p.y != g.y || p.ch != 0 {
			blankCell(cells, g.x, g.y, bg)
		} else {
			paintFg(cells, g.x, g.y, p.fg)
		}
	}
	for i, g := range glyphs {
		p := places[i]
		if p.hidden || (p.x == g.x && p.y == g.y && p.ch == 0) {
			continue
		}
		putGlyph(cells, p.x, p.y, g, p.ch, p.fg)
	}
}

// fadeFg blends from toward a character's color fg by p, resolving a
// default fg with defaultFg. At p >= 1 it returns fg itself so the
// character keeps rendering natively once the blend is over.
func fadeFg(p float64, from, fg, defaultFg term.Color) term.Color {
	if p >= 1 {
		return fg
	}
	target := fg
	if target == term.ColorDefault {
		target = defaultFg
	}
	return shaderutils.InterpolateColor(p, from, target, from)
}

// resolveFg returns fg, or defaultFg when fg is the default color.
func resolveFg(fg, defaultFg term.Color) term.Color {
	if fg == term.ColorDefault {
		return defaultFg
	}
	return fg
}

// mix32 is a 32-bit integer finalizer with good avalanche.
func mix32(h uint32) uint32 {
	h ^= h >> 16
	h *= 0x7feb352d
	h ^= h >> 15
	h *= 0x846ca68b
	h ^= h >> 16
	return h
}

// hash3 returns a deterministic pseudo-random value for a seed and three
// integers, stable across frames so per-cell choices do not flicker.
func hash3(seed int64, a, b, c int) uint32 {
	h := uint32(seed) ^ uint32(seed>>32) ^ 0x9e3779b9
	h = mix32(h ^ uint32(a)*0x85ebca6b)
	h = mix32(h ^ uint32(b)*0xc2b2ae35)
	h = mix32(h ^ uint32(c)*0x27d4eb2f)
	return h
}

// unit3 is hash3 scaled to [0, 1).
func unit3(seed int64, a, b, c int) float64 {
	return float64(hash3(seed, a, b, c)) / 4294967296.0
}

// pick3 is hash3 reduced to [0, n). n must be positive.
func pick3(seed int64, a, b, c, n int) int {
	return int(hash3(seed, a, b, c) % uint32(n))
}

// shuffledRanks returns, for each glyph, its position in a random
// permutation of the glyphs decided by seed and salt and the glyphs'
// positions, so a glyph keeps its rank across frames while the text
// around it changes.
func shuffledRanks(seed int64, salt int, glyphs []glyph) []int {
	order := make([]int, len(glyphs))
	keys := make([]uint32, len(glyphs))
	for i, g := range glyphs {
		order[i] = i
		keys[i] = hash3(seed, g.x, g.y, salt)
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if keys[a] != keys[b] {
			return keys[a] < keys[b]
		}
		return a < b
	})
	ranks := make([]int, len(glyphs))
	for rank, i := range order {
		ranks[i] = rank
	}
	return ranks
}

// ranksBy returns, for each glyph, its position in the glyphs sorted by
// the group and the index within it that key returns, ties broken by
// reading order.
func ranksBy(glyphs []glyph, key func(g glyph) (group, index int)) []int {
	type sortKey struct{ group, index, i int }
	keys := make([]sortKey, len(glyphs))
	for i, g := range glyphs {
		group, index := key(g)
		keys[i] = sortKey{group, index, i}
	}
	sort.Slice(keys, func(a, b int) bool {
		if keys[a].group != keys[b].group {
			return keys[a].group < keys[b].group
		}
		if keys[a].index != keys[b].index {
			return keys[a].index < keys[b].index
		}
		return keys[a].i < keys[b].i
	})
	ranks := make([]int, len(glyphs))
	for rank, k := range keys {
		ranks[k.i] = rank
	}
	return ranks
}

// lerp linearly interpolates from a to b by t.
func lerp(a, b, t float64) float64 {
	return a + (b-a)*t
}

// clamp01 clamps t to [0, 1].
func clamp01(t float64) float64 {
	return math.Max(0, math.Min(1, t))
}

// window returns how far t is through [start, start+length], clamped
// to [0, 1]. A non-positive length is already complete at start. The
// end is reached within a small tolerance, so phases chained by sums of
// fractions complete on the last frame despite rounding.
func window(t, start, length float64) float64 {
	const epsilon = 1e-9
	if t+epsilon >= start+length {
		return 1
	}
	if length <= 0 || t <= start {
		return 0
	}
	return clamp01((t - start) / length)
}

// roundInt rounds to the nearest integer, halves away from zero.
func roundInt(v float64) int {
	return int(math.Round(v))
}

// cellDistance is the visual distance between two cells, with rows
// scaled by rowScale.
func cellDistance(x0, y0, x1, y1 float64) float64 {
	return math.Hypot(x1-x0, (y1-y0)*rowScale)
}

// ellipsePoint returns the cell at angle radians on the circle of
// radius columns centered at cx, cy, drawn round on the cell grid.
func ellipsePoint(cx, cy, radius, angle float64) (x, y float64) {
	return cx + radius*math.Cos(angle), cy + radius*math.Sin(angle)/rowScale
}

// outsideCoord returns a cell just outside a rows by cols canvas,
// picked by seed and salt, the way terminaltexteffects' random_coord
// with outside_scope picks the start of characters that fly in.
func outsideCoord(seed int64, salt, rows, cols int) (x, y int) {
	switch pick3(seed, salt, 0, 1, 4) {
	case 0:
		return pick3(seed, salt, 0, 2, max(1, cols)), -1
	case 1:
		return pick3(seed, salt, 0, 2, max(1, cols)), rows
	case 2:
		return -1, pick3(seed, salt, 0, 2, max(1, rows))
	default:
		return cols, pick3(seed, salt, 0, 2, max(1, rows))
	}
}
