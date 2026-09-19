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
	"fmt"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	log "github.com/sirupsen/logrus"
)

// x11WMClass is the ICCCM WM_CLASS instance/class reported by the window.
// It must match StartupWMClass in deploy/rune-linux/rune.desktop so X11
// desktop environments (e.g. KDE Plasma) group the running window under the
// pinned launcher instead of showing a second, generic-icon task.
const x11WMClass = "rune"

func (g *GUI) buildRunGameOptions() ebiten.RunGameOptions {
	return ebiten.RunGameOptions{
		SingleThread:      true,
		ScreenTransparent: g.enableTransparent,
		X11ClassName:      x11WMClass,
		X11InstanceName:   x11WMClass,
	}
}

// Draw satisfies ebiten.Game. It renders the terminal GUI to the ebtien window.
func (g *GUI) Draw(screen *ebiten.Image) {
	if !g.needsRender {
		return
	}
	screen.Clear()
	cells := g.writer.RawCells()
	if g.links.armed() {
		cells = g.links.overlay(cells)
	}
	g.renderer.Draw(screen, cells, g.cursor.show,
		g.cursor.pos, g.cursor.style, float64(g.renderOffset.X),
		float64(g.renderOffset.Y))
	g.needsRender = false
	if g.printFPS {
		ebitenutil.DebugPrint(screen, fmt.Sprintf("FPS: %0.2f", ebiten.ActualFPS()))
	}
}

// NeedsRender reports whether the next Draw will repaint the screen.
func (g *GUI) NeedsRender() bool {
	return g.needsRender
}

// SetForceFullRepaint selects whether Draw repaints the entire retained frame.
func (g *GUI) SetForceFullRepaint(force bool) {
	g.forceFullRepaint = force
	if g.renderer != nil {
		g.renderer.forceFullRepaint = force
	}
	g.needsRender = true
}

// Layout satisfies ebiten.Game. It provides the terminal gui size in pixels.
func (g *GUI) Layout(width, height int) (int, int) {
	s := g.fontManager.DeviceScale()

	if g.width != width || g.height != height || g.deviceScale != s {
		if g.deviceScale != s {
			g.log(log.DebugLevel, "reloading font due to "+
				"device scale change: %f vs %f", g.deviceScale, s)
			_ = g.fontManager.ReloadFont()
		}
		g.mu.Lock()
		g.resize(width, height, s)
		g.mu.Unlock()
	}

	return int(float64(width) * s), int(float64(height) * s)
}
