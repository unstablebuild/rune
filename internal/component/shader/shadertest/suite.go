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

package shadertest

import (
	"math/rand"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/timeshader"
)

// TestShader runs an exhaustive suite of tests against a shader.Shader.
func TestShader(t *testing.T, sh shader.Shader) {
	testFrameRanges(t, sh)
	testFedIntoSingleTimeShader(t, sh)
	testFedIntoMultipleTimeShaders(t, sh)
	testCellMatrix(t, sh)
}

// BenchmarkShader runs a benchmark against shader.Shader.
func BenchmarkShader(
	b *testing.B, sh shader.Shader, width, height int, cells [][]term.Cell,
) {
	copy := MakeCellMatrix(width, height)

	b.ResetTimer()
	totalFrames := 30 * 5 // 5 seconds at 30fps
	for i := 0; i < b.N; i++ {
		for i := range totalFrames {
			term.CopyCells(copy, cells)
			sh.Shade(i, totalFrames, copy)
		}
	}
}

// AssertProgressOnly checks that the shaders build makes see the frame
// and total they are given only through frame/total: frame f of total
// T shades exactly as frame 3f of 3T. Every frame is shaded by a fresh
// shader, so nothing one remembers can make up for the other.
func AssertProgressOnly(t *testing.T, build func() shader.Shader, in [][]term.Cell) {
	t.Helper()
	for _, total := range []int{7, 20} {
		for frame := range total {
			want := shadeCopy(build(), frame, total, in)
			got := shadeCopy(build(), 3*frame, 3*total, in)
			if !assert.Equal(t, want, got, "frame %d of %d against frame %d of %d",
				frame, total, 3*frame, 3*total) {
				return
			}
		}
	}
}

// AssertOrderIndependent checks that a shader build makes shades every
// frame of total as a fresh shader does, whatever it shaded before:
// frames played forwards, backwards or shuffled, or interleaved with
// frames of a smaller canvas.
func AssertOrderIndependent(t *testing.T, build func() shader.Shader, in [][]term.Cell, total int) {
	t.Helper()
	small := cropCanvas(in)
	want := make([][][]term.Cell, total)
	wantSmall := make([][][]term.Cell, total)
	forward := make([]int, total)
	backward := make([]int, total)
	for f := range total {
		want[f] = shadeCopy(build(), f, total, in)
		wantSmall[f] = shadeCopy(build(), f, total, small)
		forward[f] = f
		backward[total-1-f] = f
	}
	for _, order := range []struct {
		name   string
		frames []int
	}{
		{"forwards", forward},
		{"backwards", backward},
		{"shuffled", rand.New(rand.NewSource(1)).Perm(total)},
	} {
		sh := build()
		for _, f := range order.frames {
			if !assert.Equal(t, want[f], shadeCopy(sh, f, total, in),
				"%s: frame %d of %d", order.name, f, total) {
				break
			}
		}
	}
	sh := build()
	for _, f := range forward {
		if !assert.Equal(t, want[f], shadeCopy(sh, f, total, in),
			"resized: frame %d of %d", f, total) ||
			!assert.Equal(t, wantSmall[f], shadeCopy(sh, f, total, small),
				"resized: frame %d of %d on the smaller canvas", f, total) {
			break
		}
	}
}

func shadeCopy(sh shader.Shader, frame, total int, in [][]term.Cell) [][]term.Cell {
	out := term.CloneCells(in)
	sh.Shade(frame, total, out)
	return out
}

// AssertSeamless checks that the shaders build makes end as they start,
// so that played on a loop they show no seam: on every canvas, the step
// from the last frame of total back to the first is at most half again
// as big as the steps either side of it. The longer total is, the
// smaller those steps, and the finer the seam this can tell.
func AssertSeamless(t *testing.T, build func() shader.Shader, total int, canvases ...[][]term.Cell) {
	t.Helper()
	for _, in := range canvases {
		sh := build()
		before := shadeCopy(sh, total-2, total, in)
		last := shadeCopy(sh, total-1, total, in)
		first := shadeCopy(sh, 0, total, in)
		after := shadeCopy(sh, 1, total, in)
		near := max(Distance(before, last), Distance(first, after))
		assert.LessOrEqual(t, 2*Distance(last, first), 3*near,
			"%dx%d canvas: the step from the last frame back to the first "+
				"stands out from the steps either side of it", len(in[0]), len(in))
	}
}

// Distance is how different two frames of the same canvas look: the sum
// over their cells of how far each colour channel of the foreground and
// background moved, a changed character counting as a whole channel.
func Distance(a, b [][]term.Cell) int {
	d := 0
	for y := range a {
		for x := range a[y] {
			ca, cb := a[y][x], b[y][x]
			if ca.Ch != cb.Ch {
				d += 255
			}
			d += colorDistance(ca.Fg, cb.Fg) + colorDistance(ca.Bg, cb.Bg)
		}
	}
	return d
}

func colorDistance(a, b term.Color) int {
	if a == b {
		return 0
	}
	if !a.IsRGB() || !b.IsRGB() {
		return 3 * 255
	}
	ar, ag, ab := a.RGB()
	br, bg, bb := b.RGB()
	return int(abs(ar-br) + abs(ag-bg) + abs(ab-bb))
}

func abs(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// cropCanvas returns a copy of in less its last row and column.
func cropCanvas(in [][]term.Cell) [][]term.Cell {
	out := term.CloneCells(in[:max(1, len(in)-1)])
	for y, row := range out {
		out[y] = row[:max(0, len(row)-1)]
	}
	return out
}

func testFrameRanges(t *testing.T, sh shader.Shader) {
	tsuite := []struct {
		name       string
		frameStart int
		frameEnd   int
		total      int
	}{
		{
			name:       "shading continuously forward doesn't panic",
			frameStart: 0,
			frameEnd:   19,
			total:      20,
		},
		{
			name:       "running mid-frame continuously doesn't panic",
			frameStart: 9,
			frameEnd:   12,
			total:      20,
		},
		{
			name:       "shading continuously backwards doesn't panic",
			frameStart: 19,
			frameEnd:   0,
			total:      20,
		},
		{
			name:       "shading frames out of total range doesn't panic",
			frameStart: 30,
			frameEnd:   50,
			total:      20,
		},
		{
			name:       "negative frame range doesn't panic",
			frameStart: -10,
			frameEnd:   10,
			total:      20,
		},
		{
			name:       "total is zero doesn't panic",
			frameStart: 0,
			frameEnd:   20,
			total:      0,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				cells := MakeCellMatrix(7, 4)
				if tcase.frameStart == tcase.frameEnd {
					sh.Shade(tcase.frameStart, tcase.total, cells)
				} else if tcase.frameStart < tcase.frameEnd {
					for f := tcase.frameStart; f < tcase.frameEnd; f++ {
						sh.Shade(f, tcase.total, cells)
					}
				} else {
					// tcase.frameStart > tcase.frameEnd
					for f := tcase.frameStart; f > tcase.frameEnd; f-- {
						sh.Shade(f, tcase.total, cells)
					}
				}
			})
		})
	}

}

func testFedIntoSingleTimeShader(t *testing.T, sh shader.Shader) {
	for timeShaderName, timeShaderFn := range timeShaderFns {
		t.Run("when fed into "+timeShaderName+" time shader doesn't panic", func(t *testing.T) {
			tsh := timeShaderFn.fn(sh)
			cells := MakeCellMatrix(7, 4)
			for frame := range 10 {
				assert.NotPanics(t, func() {
					tsh.Shade(frame, 10, cells)
				})
			}
		})
	}
}
func testFedIntoMultipleTimeShaders(t *testing.T, sh shader.Shader) {
	t.Run("doesn't panic when stacking all time shaders", func(t *testing.T) {
		tsh := sh
		cells := MakeCellMatrix(7, 4)
		for _, timeShaderFn := range timeShaderFns {
			tsh = timeShaderFn.fn(tsh)
		}
		for frame := range 10 {
			assert.NotPanics(t, func() {
				tsh.Shade(frame, 10, cells)
			})
		}
	})
}

func testCellMatrix(t *testing.T, sh shader.Shader) {
	wht := term.ColorWhite
	blk := term.ColorBlack

	// regular cell (char, fg and bg are set)
	cell := func(ch rune) term.Cell {
		return term.NewCell(ch, 1, term.Attributes{Fg: wht, Bg: blk, Attrs: term.AttrNone})
	}

	// zero-value cell
	cellZr0 := func() term.Cell {
		return term.Cell{}
	}

	// foreground-only cell (char and fg are set)
	cellFgO := func() term.Cell {
		c := term.Cell{}
		c.Ch = 'F'
		c.Fg = wht
		c.Width = 1
		return c
	}

	// background-only cell (bg is set)
	cellBgO := func() term.Cell {
		c := term.Cell{}
		c.Bg = blk
		return c
	}

	// italic cell (char, fg, bg and italic attr are set)
	itaCell := func() term.Cell {
		return term.NewCell('C', 1, term.Attributes{Fg: wht, Bg: blk, Attrs: term.AttrItalic})
	}

	// character-less cell, emulates empty spaces on the window
	cellGap := func() term.Cell {
		return term.NewCell(0, 0, term.Attributes{Fg: wht, Bg: blk, Attrs: term.AttrNone})
	}

	tsuite := []struct {
		name  string
		cells [][]term.Cell
	}{
		{
			name: "standard square size (5x5)",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h')},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j')},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o')},
			},
		},
		{
			name: "proportional resize down: smaller square size (3x3)",
			cells: [][]term.Cell{
				{cell('a'), cell('b')},
				{cell('c'), cell('d')},
			},
		},
		{
			name: "proportional resize up: larger square size (7x7)",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('8')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellGap()},
			},
		},
		{
			name: "resize: narrower: reduce columns (7x5)",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h')},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j')},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap()},
			},
		},
		{
			name: "resize: shorter: reduce rows (7x2)",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap()},
			},
		},
		{
			name: "resize: taller: increase columns (7x3)",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h')},
			},
		},
		{
			name: "resize: wider: incrase rows (10x3)",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap(), cell('1'), cellGap(), cell('a'), cell('a'), cell('a')},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap(), cellGap(), cellGap(), cell('a'), cellGap(), cell('a')},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h'), cellGap(), cellGap(), cell('a'), cell('a'), cell('a')},
			},
		},
		{
			name: "resize: rotate (3x10)",
			cells: [][]term.Cell{
				{cell('a'), cell('c'), cellGap()},
				{cell('b'), cell('d'), cellGap()},
				{cellGap(), cellGap(), cellGap()},
				{cellGap(), cell('e'), cell('f')},
				{cellGap(), cellGap(), cell('h')},
				{cell('1'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap()},
				{cell('a'), cell('a'), cell('a')},
				{cell('a'), cellGap(), cell('a')},
				{cell('a'), cell('a'), cell('a')},
			},
		},
		{
			name: "cells with different styles",
			cells: [][]term.Cell{
				{cell('a'), itaCell(), cellGap(), cellGap(), cellGap(), itaCell(), cellGap()},
				{cell('c'), cell('d'), cellBgO(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h'), cellGap(), cellGap()},
				{cellGap(), cellFgO(), cell('i'), cellGap(), cell('j'), cellGap(), cellFgO()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), itaCell(), cellGap(), cellBgO(), itaCell(), cellGap(), cell('8')},
				{cellGap(), cellGap(), cellGap(), cellBgO(), cellGap(), cell('4'), cellGap()},
			},
		},
		{
			name: "some cells with zero values",
			cells: [][]term.Cell{
				{cellZr0(), cell('b'), cellZr0(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellZr0(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellZr0(), cell('f'), cell('h'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellZr0(), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('8')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellZr0()},
			},
		},
		{
			name: "first row is nil",
			cells: [][]term.Cell{
				nil,
				{cellZr0(), cell('b'), cellZr0(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellZr0(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellZr0(), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellZr0()},
			},
		},
		{
			name: "all rows but last is nil",
			cells: [][]term.Cell{
				nil,
				nil,
				nil,
				nil,
				nil,
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellZr0()},
			},
		},
		{
			name: "last row is nil",
			cells: [][]term.Cell{
				{cellZr0(), cell('b'), cellZr0(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellZr0(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellZr0(), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellZr0()},
				nil,
			},
		},
		{
			name: "some non-contiguous rows are nil",
			cells: [][]term.Cell{
				{cellZr0(), cell('b'), cellZr0(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellZr0(), cell('e'), cellGap(), cellGap(), cellGap()},
				nil,
				{cellGap(), cellGap(), cellZr0(), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				nil,
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellZr0()},
			},
		},
		{
			name: "some contiguous rows are nil",
			cells: [][]term.Cell{
				{cellZr0(), cell('b'), cellZr0(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellZr0(), cell('e'), cellGap(), cellGap(), cellGap()},
				nil,
				nil,
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellZr0()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellZr0()},
			},
		},
		{
			name: "first row has less column cells",
			cells: [][]term.Cell{
				{cell('1'), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('8')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellGap()},
			},
		},
		{
			name: "last row has less column cells",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('8')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap()},
			},
		},
		{
			name: "some rows have less column cells",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('8')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellGap()},
			},
		},
		{
			name: "each row has more cells than it's previous",
			cells: [][]term.Cell{
				{cell('a')},
				{cell('c'), cell('d')},
				{cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cell('i'), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellGap()},
			},
		},
		{
			name: "each row has more cells than it's previous",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h')},
				{cellGap(), cellGap(), cell('i'), cellGap()},
				{cell('k'), cell('l'), cell('m')},
				{cellGap(), cellGap()},
				{cellGap()},
			},
		},
		{
			name: "first row is empty",
			cells: [][]term.Cell{
				{},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('8')},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellGap()},
			},
		},
		{
			name: "last row is empty",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap(), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cell('f'), cell('h'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('8')},
				{},
			},
		},
		{
			name: "some rows are empty",
			cells: [][]term.Cell{
				{cell('a'), cell('b'), cellGap(), cellGap(), cellGap(), cell('1'), cellGap()},
				{cell('c'), cell('d'), cellGap(), cell('e'), cellGap(), cellGap(), cellGap()},
				{},
				{cellGap(), cellGap(), cell('i'), cellGap(), cell('j'), cellGap(), cellGap()},
				{cell('k'), cell('l'), cell('m'), cell('n'), cell('o'), cellGap(), cellGap()},
				{},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellGap()},
			},
		},
		{
			name: "all rows but last are empty",
			cells: [][]term.Cell{
				{},
				{},
				{},
				{},
				{},
				{},
				{cellGap(), cellGap(), cellGap(), cellGap(), cellGap(), cell('4'), cellGap()},
			},
		},
		{
			name: "only gap cells",
			cells: [][]term.Cell{
				{cellGap(), cellGap()},
				{cellGap(), cellGap()},
			},
		},
		{
			name: "only zero-value cells",
			cells: [][]term.Cell{
				{cellZr0(), cellZr0()},
				{cellZr0(), cellZr0()},
			},
		},
		{
			name: "only cells with char and foreground",
			cells: [][]term.Cell{
				{cellFgO(), cellFgO()},
				{cellFgO(), cellFgO()},
			},
		},
		{
			name: "only cells with background and no char",
			cells: [][]term.Cell{
				{cellFgO(), cellFgO()},
				{cellFgO(), cellFgO()},
			},
		},
		{
			name: "only nil rows",
			cells: [][]term.Cell{
				nil,
				nil,
			},
		},
		{
			name: "1-cell matrix: gap cell",
			cells: [][]term.Cell{
				{cellGap()},
			},
		},
		{
			name: "1-cell matrix: zero-value cell",
			cells: [][]term.Cell{
				{cellZr0()},
			},
		},
		{
			name: "1-cell matrix: char and fg cell",
			cells: [][]term.Cell{
				{cellFgO()},
			},
		},
		{
			name: "1-cell matrix: bg no char",
			cells: [][]term.Cell{
				{cellFgO()},
			},
		},
		{
			name: "1-cell matrix: nil",
			cells: [][]term.Cell{
				nil,
			},
		},
	}

	t.Run("cell matrix sizes, resizes, malformed shape and empty cells", func(t *testing.T) {
		for _, tcase := range tsuite {
			total := 20
			for frame := range 20 {
				assert.NotPanics(t, func() {
					sh.Shade(frame, total, tcase.cells)
				}, tcase.name+" frame "+strconv.Itoa(frame))
			}
		}
	})
}

var timeShaderFns = map[string]timeShaderFn{
	"Abs":        {func(sh shader.Shader) shader.Shader { return timeshader.Abs(sh) }},
	"Accelerate": {func(sh shader.Shader) shader.Shader { return timeshader.Accelerate(sh) }},
	"Decelerate": {func(sh shader.Shader) shader.Shader { return timeshader.Decelerate(sh) }},
	"Loop":       {func(sh shader.Shader) shader.Shader { return timeshader.Loop(sh, 1) }},
	"PingPong":   {func(sh shader.Shader) shader.Shader { return timeshader.PingPong(sh) }},
	"Reverse":    {func(sh shader.Shader) shader.Shader { return timeshader.Reverse(sh) }},
	"Sine":       {func(sh shader.Shader) shader.Shader { return timeshader.Sine(sh, 1) }},
}

type timeShaderFn struct {
	fn func(shader.Shader) shader.Shader
}
