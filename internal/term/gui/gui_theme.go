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
	"context"
	"fmt"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/tcell/v3"
	"unstable.build/rune/internal/cell"
)

// SetTheme switches the active color theme by name.
func (g *GUI) SetTheme(name string) (Theme, error) {
	defer g.resize(g.width, g.height, g.fontManager.DeviceScale())
	g.resetTheme()
	if name == "" {
		return Theme{}, nil
	}
	theme, ok := g.colorThemes[name]
	if !ok {
		return Theme{}, fmt.Errorf("unkown theme '%s'", name)
	}
	g.setTheme(name, theme)
	return theme, nil
}

// Theme returns the name of the active color theme.
func (g *GUI) Theme() string { return g.theme }

// Themes lists available color theme names.
func (g *GUI) Themes() []string {
	var themes []string
	for name := range g.colorThemes {
		themes = append(themes, name)
	}
	return themes
}

// SetOpacity sets background and foreground opacity in 0..1.
func (g *GUI) SetOpacity(background, foreground float64) {
	g.bgOpacity = background
	g.fgOpacity = foreground
	g.resize(g.width, g.height, g.fontManager.DeviceScale())
}

// SetBackgroundBlur sets the compositor blur radius in pixels.
func (g *GUI) SetBackgroundBlur(radius int) {
	if radius == 0 {
		radius = 1
	}
	ebiten.SetWindowBackgroundBlur(radius)
	g.resize(g.width, g.height, g.fontManager.DeviceScale())
}

// AvailableFontFamilies lists installed UI font family names.
func (g *GUI) AvailableFontFamilies() (iterator.Iterator[string], error) {
	return g.fontManager.AvailableFontFamilies()
}

// Size returns the current window size in pixels.
func (g *GUI) Size() (width, height int) { return g.width, g.height }

// LastPosition returns the last known window origin in pixels.
func (g *GUI) LastPosition() (x, y int) { return g.lastPositionX, g.lastPositionY }

func (g *GUI) drawHandler(ctx context.Context) {
	g.writer.SetContext(ctx)
	_ = g.writer.Clear(g.defaultAttr)
	g.handler.Draw(g.writer)
	g.cursor.pos, g.cursor.style, g.cursor.show = g.handler.Cursor()
	g.needsDraw = false
	g.needsRender = true
	g.links.invalidate()
}

func (g *GUI) resize(width, height int, deviceScale float64) {
	g.width = width
	g.height = height
	g.deviceScale = deviceScale

	cellsWidth := g.fontManager.CellsWidth(g.width)
	cellsHeight := g.fontManager.CellsHeight(g.height)
	g.log(log.DebugLevel, "resize: pixels width: %d, height: %d, "+
		"device scale %f; cells width: %d, height: %d",
		width, height, deviceScale, cellsWidth, cellsHeight)

	g.handler.Resize(cellsWidth, cellsHeight)
	g.mouse.resize(cellsWidth, cellsHeight)
	g.writer = cell.NewBufferWriter(g.ctx, cellsWidth, cellsHeight)
	if g.renderer != nil {
		g.renderer.deallocate()
	}
	g.renderer = newRenderer(g.width, g.height, g.deviceScale,
		g.fontManager, g.bgOpacity, g.fgOpacity, g.enableLigatures,
		g.cursorAttributes, g.defaultAttr)
	g.renderer.forceFullRepaint = g.forceFullRepaint
	g.needsDraw = true
}

// CellRect maps a cell rectangle to pixel coordinates.
func (g *GUI) CellRect(x, y, width, height int) (px, py, pw, ph float64) {
	scale := g.deviceScale
	if scale <= 0 {
		scale = 1
	}
	return g.fontManager.PixelX(x) / scale, g.fontManager.PixelY(y) / scale,
		g.fontManager.PixelX(width) / scale, g.fontManager.PixelY(height) / scale
}

func (g *GUI) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "gui",
	}).Logf(level, msg, args...)
}

func (g *GUI) resetTheme() {
	tcell.SetColorValues(g.originalColorValues)
	g.defaultAttr.Fg = term.FromTcellColor(tcell.ColorWhite)
	g.defaultAttr.Bg = term.FromTcellColor(tcell.ColorBlack)
	g.cursorAttributes = term.Attributes{Bg: term.FromTcellColor(tcell.ColorRed)}
}

func (g *GUI) setTheme(name string, theme Theme) {
	m := make(map[tcell.Color]int32, len(theme.Colors))
	for color, value := range theme.Colors {
		m[color] = value.Hex()
	}
	tcell.MergeColorValues(m)
	g.theme = name
	g.defaultAttr.Fg = term.FromTcellColor(theme.Foreground)
	g.defaultAttr.Bg = term.FromTcellColor(theme.Background)
	g.cursorAttributes = term.Attributes{Bg: term.FromTcellColor(theme.Cursor)}
}
