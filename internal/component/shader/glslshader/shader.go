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
	"runtime"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/debug"
)

// paintsText reports whether a foreground-only effect should touch a
// cell. A blank has no foreground to show, and a glyph that renders as
// background, such as a fade block, is scenery rather than text.
func paintsText(ch rune) bool {
	return ch != 0 && ch != ' ' && !graphemecluster.IsBackground(ch)
}

type cellRunner interface {
	// runCell adapts the shader interface to be closer to pixel-shader.
	//
	// For instance Shadertoy's fragCoord would be fragCoordX and fragCoordY,
	// iResolution would be resolutionX and resolutionY and iTime would be
	// time (which is in seconds too).
	//
	// The original cell indices are passed along (cellCoords).
	runCell(
		frame, total int, fps float, time float,
		cellCoords term.Coordinates,
		fragCoordX, fragCoordY int,
		resolutionX, resolutionY int,
		inChar rune, inFg, inBg term.Color,
	) (char rune, fg, bg term.Color)
}

// glslHelper is a helper structure which parallelizes computation
type glslHelper struct {
	workers     int
	workersChan chan shadeRequest
}

func newHelper() *glslHelper {
	// must return a pointer, so we can use a runtime.Finalizer below
	// to cleanup worker goroutines upon garbage collection.
	return &glslHelper{workers: runtime.NumCPU()}
}

type shadeRequest struct {
	wg            *sync.WaitGroup
	y, rows, cols int
	row           []term.Cell
	time          float
	frame, total  int
	fps           float
	in            [][]term.Cell
	shader        cellRunner
}

// shadeGLSL adapts the input space to look like common pixel shader's input
// interface.
//
// To make it look like a pixel shader the Y axis is flipped and terminal cell
// aspect is taken into account, since pixels are squared and cells aren't.
//
// Intended to be run by the Shade() method of any pixel shader.
func (g *glslHelper) shadeGLSL(
	frame, total int, fps float, in [][]term.Cell, shader cellRunner,
) {
	if frame >= total {
		return
	}

	if g.workersChan == nil {
		g.initWorkers()
	}

	rows := len(in)
	cols := len(in[0])

	time := float(frame) / fps

	var wg sync.WaitGroup
	wg.Add(len(in))
	for y, row := range in {
		g.workersChan <- shadeRequest{
			wg: &wg,
			y:  y, rows: rows, cols: cols,
			row:   row,
			time:  time,
			frame: frame, total: total,
			fps:    fps,
			in:     in,
			shader: shader,
		}
	}
	wg.Wait()
}

func (g *glslHelper) initWorkers() {
	ch := make(chan shadeRequest)
	for i := 0; i < g.workers; i++ {
		go debug.CapturePanicReport(func() {
			for {
				req, ok := <-ch
				if !ok {
					return
				}
				shadeRow(req.wg, req.y, req.rows, req.cols,
					req.row, req.time, req.frame, req.total,
					req.fps, req.in, req.shader)
			}
		})
	}

	// cleanup workers when helper/shader is no longer in use
	runtime.SetFinalizer(g, func(g *glslHelper) {
		close(g.workersChan)
	})

	g.workersChan = ch
}

func shadeRow(
	wg *sync.WaitGroup, y, rows, cols int, row []term.Cell, time float,
	frame, total int, fps float, in [][]term.Cell, shader cellRunner,
) {
	defer wg.Done()

	_, fragCoordY, resolutionX, resolutionY := cellCoordToFragCoords(
		0, y, cols, rows,
	)

	for x := range row {
		char, fg, bg := shader.runCell(
			frame, total, fps, time,
			term.Coordinates{X: x, Y: y},
			x, fragCoordY,
			resolutionX, resolutionY,
			in[y][x].Ch, in[y][x].Fg, in[y][x].Bg,
		)
		in[y][x].Ch = char
		in[y][x].Width = 1
		in[y][x].Fg = fg
		in[y][x].Bg = bg
	}
}

func cellCoordToFragCoords(x, y, cols, rows int) (
	fragCoordX, fragCoordY int, resolutionX, resolutionY int,
) {
	yFlipARCorrect := int(math.Round(float(rows-y-1) *
		asciiart.HeightToWidthCellAspectRatio))
	rowsFlipARCorrect := int(math.Round(float(rows) *
		asciiart.HeightToWidthCellAspectRatio))

	fragCoordX = x
	fragCoordY = yFlipARCorrect
	resolutionX = cols
	resolutionY = rowsFlipARCorrect
	return
}

func fragCoordsToCellCoords(
	fragCoordX, fragCoordY, resolutionX, resolutionY int,
) (x, y int, rows, cols int) {
	x = fragCoordX
	y = int(
		math.Round(
			float(resolutionY-fragCoordY-1) /
				asciiart.HeightToWidthCellAspectRatio,
		),
	)
	cols = int(float(resolutionY) / asciiart.HeightToWidthCellAspectRatio)
	rows = resolutionX
	return

}
