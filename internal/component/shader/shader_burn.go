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
	"math/rand"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"unstable.build/rune/internal/component/shader/shaderutils"
)

// Burn ignites the canvas character by character following a
// Prim-style minimum spanning tree walk seeded at a random cell, so
// the resulting fire front spreads in an organic, scattered pattern
// rather than as a strict vertical or radial sweep.
//
// Each cell briefly cycles through a sequence of "burn" glyphs colored
// by a fire gradient, even if the original cell was blank. Pre-ignition
// and post-burn cells render unchanged (their original text and colors
// show through), so only a thin wave is on fire at any moment. As some
// original text cells finish burning, the original character rises into
// higher cells, fading toward grey.
//
// The last frame of the animation matches the input exactly.
//
// Inspired by terminaltexteffects' "burn" effect, which spreads its
// scenes through the canvas with a PrimsSimple algorithm and emits
// smoke particles from each burning character.
func Burn(params BurnParams, defaultAttr term.Attributes) Shader {
	defaults := DefaultBurnParams()
	if len(params.BurnSymbols) == 0 {
		params.BurnSymbols = defaults.BurnSymbols
	}
	if len(params.BurnGradient) == 0 {
		params.BurnGradient = defaults.BurnGradient
	}
	if len(params.SmokeSymbols) == 0 {
		params.SmokeSymbols = defaults.SmokeSymbols
	}
	if params.BurnDuration <= 0 {
		params.BurnDuration = defaults.BurnDuration
	}
	if params.BurnDuration > 1 {
		params.BurnDuration = 1
	}
	if params.SmokeRise <= 0 {
		params.SmokeRise = defaults.SmokeRise
	}
	if params.SmokeMaxRise <= 0 {
		params.SmokeMaxRise = defaults.SmokeMaxRise
	}
	if params.IgniteBatchMin <= 0 {
		params.IgniteBatchMin = defaults.IgniteBatchMin
	}
	if params.IgniteBatchMax <= 0 {
		params.IgniteBatchMax = defaults.IgniteBatchMax
	}
	if params.IgniteBatchMax < params.IgniteBatchMin {
		params.IgniteBatchMax = params.IgniteBatchMin
	}
	if params.SmokeChance < 0 {
		params.SmokeChance = 0
	}
	if params.SmokeChance > 1 {
		params.SmokeChance = 1
	}
	return &burn{
		BurnParams:  params,
		defaultAttr: defaultAttr,
	}
}

// SetInitialCells sets the snapshot Burn uses as its base layer while the
// shader is active. When unset, Burn falls back to capturing the first cell
// matrix passed to Shade. The slice is retained as-is, so callers that may
// mutate it should pass a clone (e.g. via [term.CloneCells]).
func (s *burn) SetInitialCells(cells [][]term.Cell) {
	s.initialCells = cells
	s.igniteFrame = nil
	s.rootSet = false
}

// SetInitialDesaturation desaturates every cell of the snapshot used as
// the base layer (see [SetInitialCells]) toward its per-cell luminance
// gray by the given amount. amount is clamped to [0, 1].
//
// This is used to chain Burn with a preceding [GrayFade] loading shader:
// by passing the previous shader's progress, the snapshot starts at the
// same desaturation amount the loading shader left off at, so the two
// effects compose without a visible color blip at the handover.
func (s *burn) SetInitialDesaturation(amount float64) {
	if amount <= 0 {
		return
	}
	if amount > 1 {
		amount = 1
	}
	for y, row := range s.initialCells {
		for x, cell := range row {
			s.initialCells[y][x].Fg = shaderutils.DesaturateColor(
				cell.Fg, amount, s.defaultAttr.Fg,
			)
		}
	}
}

// BurnParams configures the [Burn] effect.
type BurnParams struct {
	// BurnSymbols is the ordered sequence of glyphs each cell cycles
	// through while it is burning. Sampled linearly over the cell's
	// local burn time (0..1). The TTE defaults look like a small ember
	// growing into a full block and tapering back into an ember.
	BurnSymbols []rune
	// BurnGradient is the fire color gradient sampled to colorize the
	// burning glyphs over the cell's local burn time.
	BurnGradient []term.Color
	// BurnDuration is the fraction of the total animation each cell
	// spends actively on fire. Smaller values yield a narrower wave
	// front. (range 0..1, clamped)
	BurnDuration float64
	// SmokeSymbols is the fallback set of glyphs used if a burned source
	// cell cannot supply an original character for its rising particle.
	SmokeSymbols []rune
	// SmokeChance is the probability that a given burned cell emits a
	// post-burn rising-character trail. (range 0..1, clamped)
	SmokeChance float64
	// SmokeRise is the fraction of the total animation it takes for a
	// post-burn character particle to rise [SmokeMaxRise] rows above its
	// source.
	SmokeRise float64
	// SmokeMaxRise is the maximum vertical distance (in cells) a
	// post-burn character particle travels above its source.
	SmokeMaxRise int
	// IgniteBatchMin and IgniteBatchMax are the inclusive range of graph
	// nodes consumed per frame. TTE activates random batches of 2-4
	// characters each frame; Rune normalizes those batches into the
	// configured frame budget and burns blank cells too.
	IgniteBatchMin int
	IgniteBatchMax int
	// FixedOrigin makes the Prim walk start from OriginX/OriginY instead
	// of picking a random/text root. Coordinates are normalized within the
	// active bounds. This is useful for transition shaders that should
	// always bloom from the center of the screen.
	FixedOrigin bool
	OriginX     float64
	OriginY     float64
	// Seed deterministically picks the graph root, graph choices, smoke
	// symbols and smoke chances so repeated runs at the same canvas size
	// are visually identical. Defaults to a fixed value if zero.
	Seed int64
	// PaintForeground makes the wave recolour text in place with
	// BurnGradient instead of burning it: characters keep their glyph and
	// background, no particle rises, and blank cells and glyphs that
	// render as background, such as fade blocks, are left untouched.
	// Cells show their live content throughout rather than the snapshot
	// Burn otherwise reveals from.
	PaintForeground bool
}

// DefaultBurnParams returns a sane set of [BurnParams].
func DefaultBurnParams() BurnParams {
	return BurnParams{
		// Matches TTE's burn_char_order verbatim.
		BurnSymbols: []rune{
			'▖', '▙', '█', '▜', '▀', '▝',
		},
		// Matches TTE's default burn_colors.
		BurnGradient: []term.Color{
			term.NewRGBColor(255, 255, 255),
			term.NewRGBColor(255, 247, 93),
			term.NewRGBColor(254, 101, 13),
			term.NewRGBColor(138, 0, 60),
			term.NewRGBColor(81, 1, 0),
		},
		BurnDuration: 0.15,
		SmokeSymbols: []rune{'.', ',', '\'', '`', '#', '*'},
		SmokeChance:  0.5,
		SmokeRise:    0.2,
		SmokeMaxRise: 6,
		// Matches TTE's `for _ in range(random.randint(2, 4))`.
		IgniteBatchMin: 2,
		IgniteBatchMax: 4,
		OriginX:        0.5,
		OriginY:        0.5,
		Seed:           1,
	}
}

type burn struct {
	BurnParams
	defaultAttr term.Attributes

	// Cache of per-cell ignition frames keyed by (rows, cols, total,
	// bounds). Rebuilt whenever the matrix is resized or the total frame
	// count changes. Source text changes must not rebuild the Prim order:
	// keypresses would otherwise make the burn center jump.
	cacheRows, cacheCols, cacheTotal int
	cacheBounds                      burnBounds
	igniteFrame                      [][]int
	// rootX/rootY hold the selected Prim root for the lifetime of the
	// shader instance. The root is chosen from original text when possible
	// on the first draw, then reused across content changes.
	rootSet      bool
	rootX, rootY int
	// initialCells is the first cell matrix seen by the shader for the
	// current shape. Until the burn has crossed a cell, that snapshot is
	// drawn over the live cells so the burn acts as a reveal transition.
	initialCells [][]term.Cell
}

type burnBounds struct {
	minX, minY int
	maxX, maxY int
}

func (s *burn) Shade(frame, total int, cells [][]term.Cell) {
	if total <= 0 {
		return
	}
	rows := len(cells)
	if rows == 0 {
		return
	}
	cols := 0
	for _, row := range cells {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols == 0 {
		return
	}
	s.ensureInitialCells(cells)
	if frame > total {
		return
	}
	if frame < 0 {
		frame = 0
	}
	// Frame 0: paint the pre-burn snapshot over the live cells so the
	// transition starts from the captured screen instead of flashing the
	// new workspace for a frame before the wave begins. The chained
	// loading→open transition needs this: if the loading shader stopped
	// part-way through desaturating, SetInitialDesaturation has already
	// applied that amount to s.initialCells.
	if frame == 0 && !s.PaintForeground {
		for y := range cells {
			for x := range cells[y] {
				cells[y][x] = s.initialCell(y, x, cells[y][x])
			}
		}
		return
	}

	bounds, ok := s.activeBounds(rows, cols)
	if !ok {
		return
	}
	s.ensureCache(s.initialCells, rows, cols, total, bounds)

	burnFrames := s.burnFrameCount(total)
	if s.PaintForeground {
		s.recolorText(frame, burnFrames, bounds, cells)
		return
	}
	smokeFrames := s.smokeFrameCount(total)
	activeBurn := make([][]bool, rows)
	for y := range activeBurn {
		activeBurn[y] = make([]bool, cols)
	}

	// Pass 1: cells progress through three states based on their per-cell
	// ignition frame:
	//   - pre-burn  : show the initial snapshot so the previous screen
	//                 remains visible until the wave reaches the cell;
	//   - active    : render the fire glyph even when the source cell was
	//                 blank so empty panes still visibly burn;
	//   - post-burn : leave the live cell untouched so the new content is
	//                 progressively revealed behind the receding wave.
	for y := bounds.minY; y <= bounds.maxY && y < len(cells); y++ {
		row := cells[y]
		maxX := min(bounds.maxX, len(row)-1)
		for x := bounds.minX; x <= maxX; x++ {
			start := s.igniteFrame[y][x]
			if start < 0 {
				cells[y][x] = s.initialCell(y, x, cells[y][x])
				continue
			}
			localFrame := frame - start
			if localFrame < 0 {
				cells[y][x] = s.initialCell(y, x, cells[y][x])
				continue
			}
			if localFrame >= burnFrames {
				continue
			}
			frac := float64(localFrame) / float64(max(1, burnFrames-1))
			symIdx := int(frac * float64(len(s.BurnSymbols)))
			if symIdx >= len(s.BurnSymbols) {
				symIdx = len(s.BurnSymbols) - 1
			}
			cells[y][x].Ch = s.BurnSymbols[symIdx]
			cells[y][x].Width = 1
			cells[y][x].Fg = shaderutils.SampleGradient(frac, s.BurnGradient)
			// Burn-glyph cells overwrite whatever the wrapped
			// component drew. Clear the renderer's vertical
			// half-cell offset hints that were inherited from the
			// source (e.g. tabbar/statusbar rows): the burn glyph
			// is its own visual layer and should sit on the cell
			// grid, not on the chrome's render offset.
			cells[y][x].Attrs &^= burnGlyphStripAttrs
			activeBurn[y][x] = true
		}
	}

	// Pass 2: post-burn character particles. TTE emits one particle from
	// a character when its burn scene completes and animates it upward. In
	// Rune, use the original source character for that particle so the
	// motion reads as pieces of text floating upward rather than as spark
	// punctuation.
	if smokeFrames <= 0 {
		return
	}
	for srcY := bounds.minY; srcY <= bounds.maxY && srcY < rows; srcY++ {
		maxX := bounds.maxX
		if srcY < len(cells) && maxX >= len(cells[srcY]) {
			maxX = len(cells[srcY]) - 1
		}
		for srcX := bounds.minX; srcX <= maxX; srcX++ {
			start := s.igniteFrame[srcY][srcX]
			if start < 0 {
				continue
			}
			if !s.emitsRisingChar(srcX, srcY) {
				continue
			}
			sourceCell := s.initialCell(srcY, srcX, cells[srcY][srcX])
			age := frame - (start + burnFrames)
			if age < 0 || age >= smokeFrames {
				continue
			}
			frac := float64(age) / float64(max(1, smokeFrames-1))
			rise := min(s.SmokeMaxRise, srcY+1)
			if rise <= 0 {
				continue
			}
			dy := 1 + int(frac*float64(rise-1))
			targetY := srcY - dy
			targetX := srcX + int(math.Round(float64(s.risingCharOffset(srcX, srcY))*frac))
			if targetY < 0 || targetY >= rows || targetX < 0 || targetX >= cols {
				continue
			}
			if targetX >= len(cells[targetY]) || activeBurn[targetY][targetX] {
				continue
			}

			// Cells with an original character lift that character up so
			// the motion reads as text floating off the screen. Cells
			// without one (e.g. burned blank panes) emit a smoke glyph
			// instead so the post-burn trail is still visually present.
			particleCh := sourceCell.Ch
			particleWidth := sourceCell.Width
			if !hasOriginalChar(sourceCell) {
				if len(s.SmokeSymbols) == 0 {
					continue
				}
				particleCh = s.SmokeSymbols[s.smokeSymbolIndex(srcX, srcY)]
				particleWidth = 1
			}
			cells[targetY][targetX].Ch = particleCh
			cells[targetY][targetX].Width = particleWidth
			cells[targetY][targetX].Fg = shaderutils.InterpolateColor(
				frac,
				sourceCell.Fg,
				term.NewRGBColor(80, 79, 79),
				s.defaultAttr.Fg,
			)
			// Rising-particle cells overwrite the target cell;
			// see the active-burn site for rationale.
			cells[targetY][targetX].Attrs &^= burnGlyphStripAttrs
		}
	}
}

// burnGlyphStripAttrs is the set of cell attrs cleared on cells the
// burn shader actively overwrites. These hints are meant for the
// glyph drawn by the wrapped component (tabbars, statusbars, etc.);
// when burn replaces that glyph with a fire symbol or a rising
// character, leaving the hints in place renders the new glyph
// shifted by half a cell on chrome rows.
const burnGlyphStripAttrs = term.AttrVerticalRenderOffset |
	term.AttrNegativeVerticalRenderOffset

func (s *burn) recolorText(
	frame, burnFrames int, bounds burnBounds, cells [][]term.Cell,
) {
	for y := bounds.minY; y <= bounds.maxY && y < len(cells); y++ {
		row := cells[y]
		for x := bounds.minX; x <= min(bounds.maxX, len(row)-1); x++ {
			if !hasOriginalChar(row[x]) || graphemecluster.IsBackground(row[x].Ch) {
				continue
			}
			localFrame := frame - s.igniteFrame[y][x]
			if s.igniteFrame[y][x] < 0 || localFrame < 0 || localFrame >= burnFrames {
				continue
			}
			frac := float64(localFrame) / float64(max(1, burnFrames-1))
			row[x].Fg = shaderutils.SampleGradient(frac, s.BurnGradient)
		}
	}
}

func (s *burn) ensureInitialCells(cells [][]term.Cell) {
	if sameCellShape(s.initialCells, cells) {
		return
	}
	s.initialCells = cloneCellMatrix(cells)
	// A shape change invalidates the cached traversal and root. Content
	// changes with the same shape intentionally do not update the snapshot.
	s.igniteFrame = nil
	s.rootSet = false
}

func (s *burn) initialCell(y, x int, fallback term.Cell) term.Cell {
	if y < 0 || y >= len(s.initialCells) || x < 0 || x >= len(s.initialCells[y]) {
		return fallback
	}
	return s.initialCells[y][x]
}

func sameCellShape(a, b [][]term.Cell) bool {
	if len(a) != len(b) {
		return false
	}
	for y := range a {
		if len(a[y]) != len(b[y]) {
			return false
		}
	}
	return true
}

func cloneCellMatrix(in [][]term.Cell) [][]term.Cell {
	out := make([][]term.Cell, len(in))
	for y, row := range in {
		out[y] = make([]term.Cell, len(row))
		copy(out[y], row)
	}
	return out
}

func (s *burn) activeBounds(rows, cols int) (burnBounds, bool) {
	bounds := burnBounds{minX: 0, minY: 0, maxX: cols - 1, maxY: rows - 1}
	return bounds, true
}

func hasOriginalChar(cell term.Cell) bool {
	return cell.Width != 0 && cell.Ch != 0 && cell.Ch != ' '
}

func (s *burn) burnFrameCount(total int) int {
	frames := int(math.Round(s.BurnDuration * float64(total)))
	frames = max(frames, 2)
	if total > 2 && frames > total-2 {
		frames = total - 2
	}
	return frames
}

func (s *burn) smokeFrameCount(total int) int {
	frames := int(math.Round(s.SmokeRise * float64(total)))
	frames = max(frames, 1)
	if total > 2 && frames > total-2 {
		frames = total - 2
	}
	return frames
}

func (s *burn) emitsRisingChar(x, y int) bool {
	if s.SmokeChance <= 0 {
		return false
	}
	if s.SmokeChance >= 1 {
		return true
	}
	h := burnHash32(uint32(x)^uint32(s.Seed), uint32(y)+0x9e3779b9)
	return float64(h&0xFFFF)/float64(0x10000) < s.SmokeChance
}

func (s *burn) risingCharOffset(x, y int) int {
	h := burnHash32(uint32(x)+0x85ebca6b, uint32(y)^uint32(s.Seed))
	return int(h%9) - 4
}

// smokeSymbolIndex picks a deterministic index into [BurnParams.SmokeSymbols]
// from a coordinate pair. Stable across frames so a given source cell
// emits the same smoke glyph throughout its post-burn trail.
func (s *burn) smokeSymbolIndex(x, y int) int {
	h := burnHash32(uint32(x)+0xa24baed4, uint32(y)^uint32(s.Seed)+0xc6ef3720)
	return int(h % uint32(len(s.SmokeSymbols)))
}

func (s *burn) lastIgniteFrame(total int) int {
	// The shader component stops drawing the shader after epoch==total,
	// so schedule the final ignition early enough that it has a full burn
	// scene by frame==total. This keeps direct Shade(frame==total) calls
	// intuitive while still looking complete in the component's final
	// visible frames.
	return max(1, total-s.burnFrameCount(total)+1)
}

// ensureCache (re)builds the per-cell ignition frame grid using the
// same simplified Prim-style graph walk as TTE's PrimsSimple, then
// normalizes it into the available frame budget so frame/total means
// "how much of the full traversal has been reached". Rune assigns an
// ignition frame to blank cells too so empty regions visibly burn; cells
// with original characters are preferred as graph roots so text catches
// early when present.
func (s *burn) ensureCache(
	cells [][]term.Cell,
	rows int,
	cols int,
	total int,
	bounds burnBounds,
) {
	if s.igniteFrame != nil &&
		s.cacheRows == rows &&
		s.cacheCols == cols &&
		s.cacheTotal == total &&
		s.cacheBounds == bounds {
		return
	}
	s.cacheRows = rows
	s.cacheCols = cols
	s.cacheTotal = total
	s.cacheBounds = bounds
	s.igniteFrame = make([][]int, rows)
	for y := range s.igniteFrame {
		s.igniteFrame[y] = make([]int, cols)
		for x := range s.igniteFrame[y] {
			s.igniteFrame[y][x] = -1
		}
	}

	seed := s.Seed
	if seed == 0 {
		seed = 1
	}
	rng := rand.New(rand.NewSource(seed + int64(rows*1_000_003+cols*97+total*13)))
	order := s.primsSimpleOrder(cells, bounds, rows, cols, rng)
	if len(order) == 0 {
		return
	}

	batchMin := s.IgniteBatchMin
	batchMax := s.IgniteBatchMax
	if batchMin <= 0 {
		batchMin = 2
	}
	if batchMax < batchMin {
		batchMax = batchMin
	}
	nextBatch := func() int {
		return batchMin + rng.Intn(batchMax-batchMin+1)
	}

	graphStep := 0
	graphSteps := make([]int, len(order))
	maxGraphStep := 0
	batchRemaining := nextBatch()
	for i := range order {
		graphSteps[i] = graphStep
		maxGraphStep = graphStep
		batchRemaining--
		if batchRemaining <= 0 {
			graphStep++
			batchRemaining = nextBatch()
		}
	}

	firstFrame := 1
	lastFrame := s.lastIgniteFrame(total)
	frameSpan := lastFrame - firstFrame
	for pos, cellIdx := range order {
		y := cellIdx / cols
		x := cellIdx % cols
		if x < len(cells[y]) {
			startFrame := firstFrame
			if maxGraphStep > 0 && frameSpan > 0 {
				startFrame += int(math.Round(float64(graphSteps[pos]) / float64(maxGraphStep) * float64(frameSpan)))
			}
			s.igniteFrame[y][x] = startFrame
		}
	}
}

func (s *burn) primsSimpleOrder(cells [][]term.Cell, bounds burnBounds, rows int, cols int, rng *rand.Rand) []int {
	idx := func(x, y int) int { return y*cols + x }
	visited := make([]bool, rows*cols)
	rootCandidates := make([]burnCoord, 0)
	for y := bounds.minY; y <= bounds.maxY && y < len(cells); y++ {
		maxX := min(bounds.maxX, len(cells[y])-1)
		for x := bounds.minX; x <= maxX; x++ {
			if hasOriginalChar(cells[y][x]) {
				rootCandidates = append(rootCandidates, burnCoord{x: x, y: y})
			}
		}
	}
	root := s.rootForBounds(rootCandidates, bounds, rng)
	visited[idx(root.x, root.y)] = true
	order := []int{idx(root.x, root.y)}
	edges := []burnCoord{root}

	for len(edges) > 0 {
		edgeIdx := rng.Intn(len(edges))
		current := edges[edgeIdx]
		edges[edgeIdx] = edges[len(edges)-1]
		edges = edges[:len(edges)-1]

		unlinked := burnUnlinkedNeighbors(current, bounds, cols, visited)
		if len(unlinked) == 0 {
			continue
		}

		nextIdx := rng.Intn(len(unlinked))
		next := unlinked[nextIdx]
		visited[idx(next.x, next.y)] = true
		order = append(order, idx(next.x, next.y))

		if len(unlinked) > 1 {
			edges = append(edges, current)
		}
		if len(burnUnlinkedNeighbors(next, bounds, cols, visited)) > 0 {
			edges = append(edges, next)
		}
	}

	return order
}

func (s *burn) rootForBounds(rootCandidates []burnCoord, bounds burnBounds, rng *rand.Rand) burnCoord {
	if !s.rootSet {
		root := burnCoord{
			x: bounds.minX + rng.Intn(bounds.maxX-bounds.minX+1),
			y: bounds.minY + rng.Intn(bounds.maxY-bounds.minY+1),
		}
		if s.FixedOrigin {
			boundCols := bounds.maxX - bounds.minX + 1
			boundRows := bounds.maxY - bounds.minY + 1
			root = burnCoord{
				x: bounds.minX + clampCoord(int(s.OriginX*float64(boundCols-1)), boundCols),
				y: bounds.minY + clampCoord(int(s.OriginY*float64(boundRows-1)), boundRows),
			}
		} else if len(rootCandidates) > 0 {
			root = rootCandidates[rng.Intn(len(rootCandidates))]
		}
		s.rootX = root.x
		s.rootY = root.y
		s.rootSet = true
	}
	return burnCoord{
		x: clampRange(s.rootX, bounds.minX, bounds.maxX),
		y: clampRange(s.rootY, bounds.minY, bounds.maxY),
	}
}

type burnCoord struct {
	x, y int
}

func burnUnlinkedNeighbors(c burnCoord, bounds burnBounds, cols int, visited []bool) []burnCoord {
	idx := func(x, y int) int { return y*cols + x }
	out := make([]burnCoord, 0, 4)
	for _, n := range [4]burnCoord{
		{x: c.x + 1, y: c.y},
		{x: c.x - 1, y: c.y},
		{x: c.x, y: c.y + 1},
		{x: c.x, y: c.y - 1},
	} {
		if n.x < bounds.minX || n.y < bounds.minY || n.x > bounds.maxX || n.y > bounds.maxY {
			continue
		}
		if visited[idx(n.x, n.y)] {
			continue
		}
		out = append(out, n)
	}
	return out
}

// burnHash32 returns a deterministic pseudo-random uint32 for a pair
// of coordinates. Used for smoke-glyph selection so the result is
// stable across frames.
func burnHash32(a, b uint32) uint32 {
	h := a*73856093 ^ b*19349663
	h ^= h >> 13
	h *= 0x5bd1e995
	h ^= h >> 15
	return h
}

func clampCoord(v, n int) int {
	if v < 0 {
		return 0
	}
	if v >= n {
		return n - 1
	}
	return v
}

func clampRange(v, minV, maxV int) int {
	if v < minV {
		return minV
	}
	if v > maxV {
		return maxV
	}
	return v
}
