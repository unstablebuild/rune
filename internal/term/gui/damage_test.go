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

package gui

import (
	"image"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/gui/drawrect"
	"unstable.build/rune/internal/term/gui/font"
)

func newTestRenderer(t *testing.T, cols, rows int) (*renderer, int, int) {
	t.Helper()
	drawrect.Init()
	m, err := font.NewManager(0, 0)
	require.NoError(t, err)
	m.SetDeviceScale(1)
	require.NoError(t, m.SetSize(15))
	// Size the frame to hold at least the requested grid.
	px := int(m.CharSize().X*float64(cols)) + 4
	py := int(m.CharSize().Y*float64(rows)) + 4
	r := newRenderer(px, py, 1, m, 1, 1, false, term.Attributes{}, term.Attributes{})
	return r, m.CellsWidth(px), m.CellsHeight(py)
}

func filledGrid(rows, cols int, ch rune) [][]term.Cell {
	g := make([][]term.Cell, rows)
	for y := range g {
		g[y] = make([]term.Cell, cols)
		for x := range g[y] {
			g[y][x] = term.Cell{Ch: ch, Fg: term.NewColor(200, 200, 200)}
		}
	}
	return g
}

// cloneGrid deep-copies a grid so a mutation of one does not alias the
// other, matching how the writer hands the renderer a fresh grid.
func cloneGrid(src [][]term.Cell) [][]term.Cell {
	dst := make([][]term.Cell, len(src))
	for y := range src {
		dst[y] = make([]term.Cell, len(src[y]))
		copy(dst[y], src[y])
	}
	return dst
}

func TestRowsEqual(t *testing.T) {
	a := []term.Cell{{Ch: 'a'}, {Ch: 'b'}}
	b := []term.Cell{{Ch: 'a'}, {Ch: 'b'}}
	assert.True(t, rowsEqual(a, b))

	b[1].Ch = 'c'
	assert.False(t, rowsEqual(a, b))

	assert.False(t, rowsEqual(a, a[:1]), "different lengths are unequal")
}

// TestComputeDirtyRowsFirstPaintIsFull asserts the first paint after
// construction (or any invalidation) repaints the whole frame.
func TestComputeDirtyRowsFirstPaintIsFull(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 10, 6)
	grid := filledGrid(rows, cols, 'x')
	full := r.computeDirtyRows(grid, cursorState{})
	assert.True(t, full, "first paint must be a full repaint")
}

// TestComputeDirtyRowsSingleCellChange asserts that changing one cell
// dirties only that row and its immediate neighbours (the vertical
// render-offset guard band).
func TestComputeDirtyRowsSingleCellChange(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 12, 8)
	grid := filledGrid(rows, cols, 'x')
	r.snapshot(grid, cursorState{})

	next := cloneGrid(grid)
	next[4][3].Ch = 'y'
	full := r.computeDirtyRows(next, cursorState{})
	require.False(t, full)

	for y := range rows {
		want := y >= 3 && y <= 5
		assert.Equalf(t, want, r.dirtyRows[y], "row %d dirty", y)
	}
}

// TestComputeDirtyRowsCursorMove asserts that moving the cursor between
// two rows dirties both the old and new cursor rows even though the
// underlying cells are unchanged.
func TestComputeDirtyRowsCursorMove(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 12, 10)
	grid := filledGrid(rows, cols, 'x')
	r.snapshot(grid, cursorState{pos: term.Coordinates{X: 1, Y: 2}, show: true})

	same := cloneGrid(grid)
	full := r.computeDirtyRows(same, cursorState{pos: term.Coordinates{X: 1, Y: 7}, show: true})
	require.False(t, full)

	// Old cursor row 2 (+/-1) and new cursor row 7 (+/-1) are dirty;
	// rows in between are not.
	dirty := map[int]bool{1: true, 2: true, 3: true, 6: true, 7: true, 8: true}
	for y := range rows {
		assert.Equalf(t, dirty[y], r.dirtyRows[y], "row %d dirty", y)
	}
}

// TestComputeDirtyRowsDimensionChangeIsFull asserts a change in grid
// height or a row's width forces a full repaint.
func TestComputeDirtyRowsDimensionChangeIsFull(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 10, 6)
	grid := filledGrid(rows, cols, 'x')
	r.snapshot(grid, cursorState{})

	taller := filledGrid(rows+1, cols, 'x')
	assert.True(t, r.computeDirtyRows(taller, cursorState{}), "height change is full")

	r.snapshot(grid, cursorState{})
	wider := cloneGrid(grid)
	wider[0] = append(wider[0], term.Cell{Ch: 'z'})
	assert.True(t, r.computeDirtyRows(wider, cursorState{}), "row width change is full")
}

// TestComputeDirtyRowsNoChange asserts an identical grid with no cursor
// dirties no rows at all.
func TestComputeDirtyRowsNoChange(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 10, 6)
	grid := filledGrid(rows, cols, 'x')
	r.snapshot(grid, cursorState{})

	full := r.computeDirtyRows(cloneGrid(grid), cursorState{})
	require.False(t, full)
	for y := range rows {
		assert.Falsef(t, r.dirtyRows[y], "row %d must be clean", y)
	}
}

// TestComputeDirtyRowsVerticalOffsetNeighbours asserts that a change in
// a row carrying a vertical render offset repaints the neighbour it
// paints into.
func TestComputeDirtyRowsVerticalOffsetNeighbours(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 12, 8)
	grid := filledGrid(rows, cols, 'x')
	grid[4][0].Attrs = term.AttrVerticalRenderOffset
	r.snapshot(grid, cursorState{})

	next := cloneGrid(grid)
	next[4][0].Ch = 'q'
	require.False(t, r.computeDirtyRows(next, cursorState{}))
	assert.True(t, r.dirtyRows[3], "row above the offset row repaints")
	assert.True(t, r.dirtyRows[4])
	assert.True(t, r.dirtyRows[5], "row below the offset row repaints")
}

// TestComputeDirtyRowsNeighbourRepaintSpillsUp asserts that an
// unchanged row carrying a negative vertical render offset, repainted
// only because its neighbour changed, dirties the row above it: its
// cells paint half a cell up into that row's strip, and without a
// clear the spill re-composites there on every repaint.
func TestComputeDirtyRowsNeighbourRepaintSpillsUp(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 12, 8)
	grid := filledGrid(rows, cols, 'x')
	for x := range grid[4] {
		grid[4][x].Attrs = term.AttrNegativeVerticalRenderOffset
	}
	r.snapshot(grid, cursorState{})

	next := cloneGrid(grid)
	next[5][0].Ch = 'q'
	require.False(t, r.computeDirtyRows(next, cursorState{}))
	assert.True(t, r.dirtyRows[3], "spill target of the repainted offset row")
	assert.True(t, r.dirtyRows[4])
	assert.True(t, r.dirtyRows[5])
	assert.True(t, r.dirtyRows[6])
	assert.False(t, r.dirtyRows[2], "no spill beyond the offset row's reach")
}

// TestComputeDirtyRowsNeighbourRepaintSpillsDown mirrors the spill-up
// case for the positive vertical render offset, which paints half a
// cell down into the row below.
func TestComputeDirtyRowsNeighbourRepaintSpillsDown(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 12, 8)
	grid := filledGrid(rows, cols, 'x')
	for x := range grid[4] {
		grid[4][x].Attrs = term.AttrVerticalRenderOffset
	}
	r.snapshot(grid, cursorState{})

	next := cloneGrid(grid)
	next[3][0].Ch = 'q'
	require.False(t, r.computeDirtyRows(next, cursorState{}))
	assert.True(t, r.dirtyRows[2])
	assert.True(t, r.dirtyRows[3])
	assert.True(t, r.dirtyRows[4])
	assert.True(t, r.dirtyRows[5], "spill target of the repainted offset row")
	assert.False(t, r.dirtyRows[6], "no spill beyond the offset row's reach")
}

// TestComputeDirtyRowsOffsetSpillCascades asserts the spill closure
// iterates: a spill-target row that itself carries an offset spills
// onward into its own neighbour.
func TestComputeDirtyRowsOffsetSpillCascades(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 12, 8)
	grid := filledGrid(rows, cols, 'x')
	for x := range grid[4] {
		grid[4][x].Attrs = term.AttrNegativeVerticalRenderOffset
	}
	for x := range grid[3] {
		grid[3][x].Attrs = term.AttrNegativeVerticalRenderOffset
	}
	r.snapshot(grid, cursorState{})

	next := cloneGrid(grid)
	next[5][0].Ch = 'q'
	require.False(t, r.computeDirtyRows(next, cursorState{}))
	assert.True(t, r.dirtyRows[3], "first spill target")
	assert.True(t, r.dirtyRows[2], "cascaded spill target")
	assert.False(t, r.dirtyRows[1], "cascade stops at a row without offsets")
}

// TestComputeDirtyRowsRepaintedRowReceivesSpillFromAbove asserts that
// when a row is repainted, a clean row above it whose offset cells
// paint down into it is repainted too: clearing the strip erases the
// spilled glyph halves, so without a repaint of the source row its
// glyphs are left cut in half (e.g. the editor status bar above a
// periodically updating bottom bar).
func TestComputeDirtyRowsRepaintedRowReceivesSpillFromAbove(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 12, 8)
	grid := filledGrid(rows, cols, 'x')
	for x := range grid[3] {
		grid[3][x].Attrs = term.AttrVerticalRenderOffset
	}
	r.snapshot(grid, cursorState{})

	// A change two rows below the offset row dirties rows 4-6 but not
	// the offset row itself.
	next := cloneGrid(grid)
	next[5][0].Ch = 'q'
	require.False(t, r.computeDirtyRows(next, cursorState{}))
	assert.True(t, r.dirtyRows[3], "offset row spilling into a repainted row")
	assert.True(t, r.dirtyRows[4])
	assert.True(t, r.dirtyRows[5])
	assert.True(t, r.dirtyRows[6])
	assert.False(t, r.dirtyRows[2], "no repaint beyond the spill source")
}

// TestComputeDirtyRowsRepaintedRowReceivesSpillFromBelow mirrors the
// spill-from-above case: a clean row below a repainted row whose offset
// cells paint up into it must be repainted too.
func TestComputeDirtyRowsRepaintedRowReceivesSpillFromBelow(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 12, 8)
	grid := filledGrid(rows, cols, 'x')
	for x := range grid[5] {
		grid[5][x].Attrs = term.AttrNegativeVerticalRenderOffset
	}
	r.snapshot(grid, cursorState{})

	// A change two rows above the offset row dirties rows 2-4 but not
	// the offset row itself.
	next := cloneGrid(grid)
	next[3][0].Ch = 'q'
	require.False(t, r.computeDirtyRows(next, cursorState{}))
	assert.True(t, r.dirtyRows[5], "offset row spilling into a repainted row")
	assert.True(t, r.dirtyRows[2])
	assert.True(t, r.dirtyRows[3])
	assert.True(t, r.dirtyRows[4])
	assert.False(t, r.dirtyRows[6], "no repaint beyond the spill source")
}

// TestDrawPartialRepaintAfterFull drives the renderer through the real
// Draw path (headless via benchdraw semantics is not needed here since
// DrawTriangles/DrawImage only enqueue) and asserts the counters
// reflect a full repaint followed by a scoped partial repaint.
func TestDrawPartialRepaintAfterFull(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 16, 10)
	grid := filledGrid(rows, cols, 'x')
	screen := ebiten.NewImage(r.frame.Bounds().Dx(), r.frame.Bounds().Dy())

	benchdraw.BeginFrame(t)
	r.Draw(screen, grid, nil, false, term.Coordinates{}, term.CursorStyleDefault, 0, 0)
	benchdraw.EndFrame(t)
	require.True(t, r.prevValid)

	next := cloneGrid(grid)
	next[5][2].Ch = 'y'
	benchdraw.BeginFrame(t)
	r.Draw(screen, next, nil, false, term.Coordinates{}, term.CursorStyleDefault, 0, 0)
	benchdraw.EndFrame(t)

	// After the second Draw the snapshot matches the latest grid, so a
	// third identical Draw dirties nothing.
	third := cloneGrid(next)
	full := r.computeDirtyRows(third, cursorState{})
	assert.False(t, full)
	for y := range rows {
		assert.Falsef(t, r.dirtyRows[y], "row %d clean on identical redraw", y)
	}
}

// TestDrawImageDoesNotDirtyRows asserts the image layer composites over
// the cell frame rather than through it: placing, changing and removing
// a picture never repaints a row, so an animated image costs an upload
// and a draw call instead of a full-frame repaint.
func TestDrawImageDoesNotDirtyRows(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 16, 10)
	grid := filledGrid(rows, cols, 'x')
	screen := ebiten.NewImage(r.frame.Bounds().Dx(), r.frame.Bounds().Dy())
	t.Cleanup(screen.Deallocate)

	pixels := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img := term.Image{Src: pixels, ID: term.NewImageID(), Width: 2, Height: 2}

	benchdraw.BeginFrame(t)
	r.Draw(screen, grid, nil, false, term.Coordinates{}, term.CursorStyleDefault, 0, 0)
	benchdraw.EndFrame(t)
	require.True(t, r.prevValid)

	assertNoDirtyRows := func(t *testing.T, images []term.Image, why string) {
		t.Helper()
		benchdraw.BeginFrame(t)
		r.Draw(screen, cloneGrid(grid), images, false,
			term.Coordinates{}, term.CursorStyleDefault, 0, 0)
		benchdraw.EndFrame(t)
		for y := range rows {
			assert.Falsef(t, r.dirtyRows[y], "row %d clean %s", y, why)
		}
	}

	assertNoDirtyRows(t, []term.Image{img}, "when a picture appears")
	require.Len(t, r.images.textures, 1, "the picture is uploaded once")

	img.Version++
	assertNoDirtyRows(t, []term.Image{img}, "when a picture changes")
	require.Len(t, r.images.textures, 1, "a new version replaces the upload")

	assertNoDirtyRows(t, nil, "when a picture disappears")
	assert.Empty(t, r.images.textures, "an unplaced picture is released")
}
