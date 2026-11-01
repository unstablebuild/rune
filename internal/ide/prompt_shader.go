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
	"time"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/glslshader"
	"unstable.build/rune/internal/component/shader/timeshader"
)

const (
	promptShaderFPS      = 30
	promptShaderDuration = time.Minute
	promptShaderLoop     = 1200 * time.Millisecond
)

type drawFunc func(term.Writer)

func (f drawFunc) Draw(w term.Writer) { f(w) }
func (drawFunc) Resize(int, int)      {}

func newPromptShader(
	charSet component.FrameCharSet, frameAttr term.Attributes,
	separator commandPromptSeparatorCharset,
	win browser.Window, offset term.Coordinates,
	root tui.Component, interrupter term.Interrupter,
	cfg commandPromptShaderConfig,
) *shader.Component {
	params := glslshader.DefaultRadarFrameParams(charSet)
	params.Runes = []rune{
		separator.Left, separator.HorizontalLeft,
		separator.HorizontalRight, separator.Right,
	}
	if cfg.colorSet {
		params.Color = cfg.color
	}
	if cfg.angularWidth > 0 {
		params.AngularWidth = cfg.angularWidth
	}
	if cfg.cycles > 0 {
		params.Cycles = cfg.cycles
	}
	inner := glslshader.RadarFrame(params, frameAttr)
	cycles := int(promptShaderDuration / promptShaderLoop)
	virt := dynamicVirtual(timeshader.Loop(inner, cycles), win, offset)
	return shader.New(
		root, virt, interrupter, promptShaderFPS, promptShaderDuration,
	)
}

type dynamicVirtualShader struct {
	inner  shader.Shader
	win    browser.Window
	offset term.Coordinates
}

func dynamicVirtual(
	inner shader.Shader, win browser.Window, offset term.Coordinates,
) shader.Shader {
	return &dynamicVirtualShader{inner: inner, win: win, offset: offset}
}

func (s *dynamicVirtualShader) Shade(frame, total int, cells [][]term.Cell) {
	if view, _, ok := s.view(cells); ok {
		s.inner.Shade(frame, total, view)
	}
}

// ShadeImages satisfies shader.ImageShader. It hands inner the images in
// the coordinates of the window's cells, and returns them in the
// screen's.
func (s *dynamicVirtualShader) ShadeImages(
	frame, total int, cells [][]term.Cell, images []term.Image,
) []term.Image {
	view, pos, ok := s.view(cells)
	if !ok {
		return images
	}
	for i := range images {
		images[i] = images[i].Translated(term.Coordinates{X: -pos.X, Y: -pos.Y})
	}
	images = shader.ShadeImages(s.inner, frame, total, view, images)
	for i := range images {
		images[i] = images[i].Translated(pos)
	}
	return images
}

// view returns the cells of the window, and where they start, or false
// if none of them are on the screen.
func (s *dynamicVirtualShader) view(
	cells [][]term.Cell,
) ([][]term.Cell, term.Coordinates, bool) {
	if s.win == nil || s.win.Closed() {
		return nil, term.Coordinates{}, false
	}
	width, height := s.win.Width(), s.win.Height()
	if width <= 0 || height <= 0 {
		return nil, term.Coordinates{}, false
	}
	pos := s.win.Position()
	pos.X += s.offset.X
	pos.Y += s.offset.Y
	if pos.X < 0 || pos.Y < 0 || pos.Y >= len(cells) {
		return nil, term.Coordinates{}, false
	}
	maxY := min(pos.Y+height, len(cells))
	view := make([][]term.Cell, 0, maxY-pos.Y)
	for y := pos.Y; y < maxY; y++ {
		row := cells[y]
		if pos.X >= len(row) {
			view = append(view, nil)
			continue
		}
		end := min(pos.X+width, len(row))
		view = append(view, row[pos.X:end])
	}
	return view, pos, true
}
