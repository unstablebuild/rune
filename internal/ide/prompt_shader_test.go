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

package ide

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
)

type recordingShader struct {
	gotRows, gotCols int
	mark             rune
}

func (s *recordingShader) Shade(_, _ int, cells [][]term.Cell) {
	s.gotRows = len(cells)
	for _, row := range cells {
		if len(row) > s.gotCols {
			s.gotCols = len(row)
		}
		for x := range row {
			row[x].Ch = s.mark
		}
	}
}

func newCells(rows, cols int, fill rune) [][]term.Cell {
	cells := make([][]term.Cell, rows)
	for y := range cells {
		cells[y] = make([]term.Cell, cols)
		for x := range cells[y] {
			cells[y][x].Ch = fill
		}
	}
	return cells
}

// fakeWindow is a minimal browser.Window used to drive
// dynamicVirtualShader in tests. Only Position/Width/Height/Closed
// are consulted by the shader; the remainder are zero stubs.
type fakeWindow struct {
	pos    term.Coordinates
	w, h   int
	closed bool
}

func (f *fakeWindow) Position() term.Coordinates               { return f.pos }
func (f *fakeWindow) Width() int                               { return f.w }
func (f *fakeWindow) Height() int                              { return f.h }
func (f *fakeWindow) Closed() bool                             { return f.closed }
func (f *fakeWindow) Content() (browserapi.Handler, error)     { return nil, nil }
func (f *fakeWindow) SetContent(browserapi.Handler) error      { return nil }
func (f *fakeWindow) Close() error                             { return nil }
func (f *fakeWindow) WindowID() uint64                         { return 1 }
func (f *fakeWindow) Focus() (bool, error)                     { return false, nil }
func (f *fakeWindow) IsFloating() bool                         { return true }
func (f *fakeWindow) IsMinimized() (component.Alignment, bool) { return 0, false }
func (f *fakeWindow) MinimizeUp(int) bool                      { return false }
func (f *fakeWindow) MinimizeDown(int) bool                    { return false }
func (f *fakeWindow) MinimizeLeft(int) bool                    { return false }
func (f *fakeWindow) MinimizeRight(int) bool                   { return false }
func (f *fakeWindow) Unminimize() bool                         { return false }
func (f *fakeWindow) SetFrameAttr(term.Attributes) (term.Attributes, bool) {
	return term.Attributes{}, false
}

var _ browser.Window = (*fakeWindow)(nil)

func TestDynamicVirtualTranslatesByOffset(t *testing.T) {
	t.Parallel()

	win := &fakeWindow{pos: term.Coordinates{X: 1, Y: 1}, w: 2, h: 2}
	rec := &recordingShader{mark: '#'}
	sh := dynamicVirtual(rec, win, term.Coordinates{X: 1, Y: 1})

	cells := newCells(5, 5, '.')
	sh.Shade(0, 1, cells)
	assert.Equal(t, 2, rec.gotRows)
	assert.Equal(t, 2, rec.gotCols)
	// pos(1,1) + offset(1,1) = (2,2), 2x2 region.
	for y := 2; y < 4; y++ {
		for x := 2; x < 4; x++ {
			assert.Equal(t, '#', cells[y][x].Ch,
				"cell (%d,%d) should be marked", x, y)
		}
	}
	assert.Equal(t, '.', cells[1][1].Ch, "outside region should be untouched")
	assert.Equal(t, '.', cells[4][4].Ch, "outside region should be untouched")
}

func TestDynamicVirtualClosedWindowIsNoOp(t *testing.T) {
	t.Parallel()
	win := &fakeWindow{pos: term.Coordinates{}, w: 2, h: 2, closed: true}
	rec := &recordingShader{mark: '#'}
	sh := dynamicVirtual(rec, win, term.Coordinates{})
	cells := newCells(2, 2, '.')
	sh.Shade(0, 1, cells)
	assert.Equal(t, 0, rec.gotRows)
	for y := range cells {
		for x := range cells[y] {
			assert.Equal(t, '.', cells[y][x].Ch)
		}
	}
}

func TestDynamicVirtualZeroDimsIsNoOp(t *testing.T) {
	t.Parallel()
	win := &fakeWindow{}
	rec := &recordingShader{mark: '#'}
	sh := dynamicVirtual(rec, win, term.Coordinates{})
	cells := newCells(2, 2, '.')
	sh.Shade(0, 1, cells)
	assert.Equal(t, 0, rec.gotRows)
	for y := range cells {
		for x := range cells[y] {
			assert.Equal(t, '.', cells[y][x].Ch)
		}
	}
}

func TestDynamicVirtualClampsToMatrix(t *testing.T) {
	t.Parallel()
	win := &fakeWindow{pos: term.Coordinates{X: 1, Y: 1}, w: 10, h: 10}
	rec := &recordingShader{mark: '#'}
	sh := dynamicVirtual(rec, win, term.Coordinates{})
	cells := newCells(2, 2, '.')
	sh.Shade(0, 1, cells)
	assert.Equal(t, 1, rec.gotRows)
	assert.Equal(t, 1, rec.gotCols)
	assert.Equal(t, '#', cells[1][1].Ch)
	assert.Equal(t, '.', cells[0][0].Ch)
	assert.Equal(t, '.', cells[0][1].Ch)
	assert.Equal(t, '.', cells[1][0].Ch)
}
