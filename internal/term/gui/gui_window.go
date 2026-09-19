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

import ebiten "github.com/hajimehoshi/ebiten/v2"

// MinimizeWindow minimizes the native window.
func (g *GUI) MinimizeWindow() { ebiten.MinimizeWindow() }

// MaximizeWindow maximizes the native window.
func (g *GUI) MaximizeWindow() { ebiten.MaximizeWindow() }

// RestoreWindow restores the native window from min/max.
func (g *GUI) RestoreWindow() { ebiten.RestoreWindow() }

// SetFullscreen toggles exclusive fullscreen.
func (g *GUI) SetFullscreen(fullscreen bool) { ebiten.SetFullscreen(fullscreen) }

// SetWindowPosition moves the native window to x, y.
func (g *GUI) SetWindowPosition(x, y int) { ebiten.SetWindowPosition(x, y) }

// SetWindowSize resizes the native window to width and height in pixels.
func (g *GUI) SetWindowSize(width, height int) {
	ebiten.SetWindowSize(width, height)
}

// IncreaseFontSize bumps the active UI font one step and relayouts.
func (g *GUI) IncreaseFontSize() error {
	err := g.fontManager.IncreaseSize()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// DecreaseFontSize drops the active UI font one step and relayouts.
func (g *GUI) DecreaseFontSize() error {
	err := g.fontManager.DecreaseSize()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// IncreaseCellWidth widens each cell and relayouts the grid.
func (g *GUI) IncreaseCellWidth() error {
	err := g.fontManager.IncreaseCellWidth()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// DecreaseCellWidth narrows each cell and relayouts the grid.
func (g *GUI) DecreaseCellWidth() error {
	err := g.fontManager.DecreaseCellWidth()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// IncreaseLineHeight adds leading between rows and relayouts.
func (g *GUI) IncreaseLineHeight() error {
	err := g.fontManager.IncreaseLineHeight()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// DecreaseLineHeight reduces leading between rows and relayouts.
func (g *GUI) DecreaseLineHeight() error {
	err := g.fontManager.DecreaseLineHeight()
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}

// SetFont switches the UI typeface to family and relayouts.
func (g *GUI) SetFont(family string) error {
	err := g.fontManager.SetFontByFamilyName(family)
	if err == nil {
		g.resize(g.width, g.height, g.fontManager.DeviceScale())
	}
	return err
}
