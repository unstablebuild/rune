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

package glslshader

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestShadeGLSL(t *testing.T) {
	sh := newFakeShader()

	t.Run("receives correct progress and duration", func(t *testing.T) {
		defer sh.cr.reset()
		cells := shadertest.MakeCellMatrix(4, 3)
		sh.Shade(20, 100, cells)
		assert.Equal(t, 20, sh.cr.frame)
		assert.Equal(t, 100, sh.cr.total)
	})

	t.Run("shades frames before the animation as its first", func(t *testing.T) {
		defer sh.cr.reset()
		cells := shadertest.MakeCellMatrix(4, 3)
		sh.Shade(-5, 100, cells)
		assert.Equal(t, 0, sh.cr.frame)
		assert.Len(t, sh.cr.coordsExecuted, 4*3)
	})

	t.Run("shades nothing out of the animation", func(t *testing.T) {
		for _, tc := range []struct {
			name         string
			frame, total int
		}{
			{"frame at total", 100, 100},
			{"frame past total", 101, 100},
			{"zero total", 0, 0},
			{"negative total", 0, -1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				defer sh.cr.reset()
				sh.Shade(tc.frame, tc.total, shadertest.MakeCellMatrix(4, 3))
				assert.Empty(t, sh.cr.coordsExecuted)
			})
		}
	})

	t.Run("receives horizontal resolution as the number of cell columns", func(t *testing.T) {
		defer sh.cr.reset()
		cells := shadertest.MakeCellMatrix(4, 3)
		sh.Shade(20, 100, cells)
		assert.Equal(t, 4, sh.cr.resolutionX)
	})

	t.Run("runs once per cell", func(t *testing.T) {
		defer sh.cr.reset()
		cells := shadertest.MakeCellMatrix(4, 3)
		sh.Shade(20, 100, cells)
		assert.Len(t, sh.cr.coordsExecuted, 4*3)
		for _, row := range cells {
			for _, cell := range row {
				assert.Equal(t, uint8(1), cell.Width)
			}
		}
	})

	t.Run("adjusts vertical resolution considering the cell aspect ratio", func(t *testing.T) {
		defer sh.cr.reset()
		cells := shadertest.MakeCellMatrix(4, 3)
		sh.Shade(20, 100, cells)
		assert.NotEqual(t, 3, sh.cr.resolutionY)
		assert.Equal(t, int(math.Round(3*asciiart.HeightToWidthCellAspectRatio)), sh.cr.resolutionY)
	})

	t.Run("receives rows in reverse order (vertical axis is flipped)", func(t *testing.T) {
		defer sh.cr.reset()
		cells := shadertest.MakeCellMatrix(4, 3)
		sh.Shade(20, 100, cells)

		// as you can see the vertical range is not 0-3 but 0-5, this is
		// because aspect ratio correction (term cells are not square! they are
		// taller, so this is adjusted for the GLSL-like runner)
		expect := []coord{
			{0, 5}, {1, 5}, {2, 5}, {3, 5},
			{0, 2}, {1, 2}, {2, 2}, {3, 2},
			{0, 0}, {1, 0}, {2, 0}, {3, 0},
		}
		assert.Equal(t, expect, sh.cr.coordsExecuted)
	})

	t.Run("receives vertical coordinates adjusted to cell aspect ratio", func(t *testing.T) {
		defer sh.cr.reset()

		cells := shadertest.MakeCellMatrix(15, 25)
		sh.Shade(20, 100, cells)

		topLeftCoord := sh.cr.coordsExecuted[0]
		assert.Equal(t, topLeftCoord.y, int(math.Round((25-1)*asciiart.HeightToWidthCellAspectRatio)))
	})
}

func TestCoordinateSpaceTransform(t *testing.T) {
	t.Run("pixel shader frag coords from cell coords", func(t *testing.T) {
		x := 2
		y := 10
		cols := 100
		rows := 30
		fragX, fragY, resX, resY := cellCoordToFragCoords(x, y, cols, rows)
		assert.Equal(t, 2, fragX)
		assert.Equal(t, 44, fragY)
		assert.Equal(t, 100, resX)
		assert.Equal(t, 69, resY)
	})

	t.Run("cell coords from pixel shader frag coords", func(t *testing.T) {
		fragX := 2
		fragY := 44
		resX := 100
		resY := 69
		x, y, cols, rows := fragCoordsToCellCoords(fragX, fragY, resX, resY)
		assert.Equal(t, 2, x)
		assert.Equal(t, 10, y)
		assert.Equal(t, 100, cols)
		assert.Equal(t, 30, rows)
	})

	t.Run(
		"check we can generally convert from cell coords to fragment coords and viceversa",
		func(t *testing.T) {
			inX := 2
			inY := 10
			inRows := 30
			inCols := 100

			fragX, fragY, resX, resY := cellCoordToFragCoords(inX, inY, inCols, inRows)
			outX, outY, outCols, outRows := fragCoordsToCellCoords(fragX, fragY, resX, resY)

			assert.Equal(t, inX, outX)
			assert.Equal(t, inY, outY)
			assert.Equal(t, inCols, outCols)
			assert.Equal(t, inRows, outRows)
		})

	// Reflect the current behaviour where due to truncated divisions of floats
	// the transformed values can't be transformed back into their originals.
	// Because of this limitation we introduced the cellCoords as an argument
	// to cellRunner.runCell...
	t.Run(
		"converting from cell to frag coord and back might produce inaccurate results",
		func(t *testing.T) {
			rows := 30
			cols := 100
			inaccurate := false
			for y := range rows {
				for x := range cols {
					fragX, fragY, resX, resY := cellCoordToFragCoords(x, y, cols, rows)
					outX, outY, outCols, outRows := fragCoordsToCellCoords(fragX, fragY, resX, resY)
					if x != outX || y != outY || cols != outCols || rows != outRows {
						inaccurate = true
					}
				}
			}
			assert.True(t, inaccurate)
		})
}

func newFakeShader() *fakeShader {
	cr := &fakeCellRunner{}
	cr.coordsExecuted = []coord{}
	helper := newHelper()
	// last row processed might not be the last row, so asserts might not work
	helper.workers = 1
	return &fakeShader{helper: helper, cr: cr}
}

type fakeShader struct {
	cr     *fakeCellRunner
	helper *glslHelper
}

func (s *fakeShader) Shade(frame, total int, in [][]term.Cell) {
	s.helper.shadeGLSL(frame, total, in, s.cr)
}

type coord struct {
	x int
	y int
}

type fakeCellRunner struct {
	frame                    int
	total                    int
	resolutionX, resolutionY int
	coordsExecuted           []coord
}

func (s *fakeCellRunner) runCell(
	frame, total int,
	cellCoords term.Coordinates,
	fragCoordX, fragCoordY int,
	resolutionX, resolutionY int,
	inChar rune, inFg, inBg term.Color,
) (char rune, fg, bg term.Color) {
	s.frame = frame
	s.total = total
	s.resolutionX = resolutionX
	s.resolutionY = resolutionY
	s.coordsExecuted = append(s.coordsExecuted, coord{fragCoordX, fragCoordY})
	return inChar, inFg, inBg
}

func (s *fakeCellRunner) reset() {
	s.frame = 0
	s.total = 0
	s.resolutionX = 0
	s.resolutionY = 0
	s.coordsExecuted = nil
}
