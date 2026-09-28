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

package vte

import "unstable.build/rune/internal/term/vte/vtegraphics"

// graphicsState is the parser handler's side of the kitty graphics
// protocol: one store per screen buffer (spec §15) and the cell size
// the footprints were computed with.
type graphicsState struct {
	// cellPixelSize reports the cell size in pixels. nil, or a zero
	// size, disables the protocol, which is what a cells-only display
	// wants (spec §18.4): clients probing with a=q get no answer and
	// fall back.
	cellPixelSize func() (width, height int)
	prim, alt     *vtegraphics.Storage
	lastCell      vtegraphics.CellSize
	// runs is scratch for the placeholder scan of one draw.
	runs []vtegraphics.PlaceholderRun
}

func (g *graphicsState) init(cellPixelSize func() (int, int)) {
	g.cellPixelSize = cellPixelSize
	g.prim = vtegraphics.NewStorage()
	g.alt = vtegraphics.NewStorage()
}

// cell reports the current cell size and whether the protocol is
// active.
func (g *graphicsState) cell() (vtegraphics.CellSize, bool) {
	if g.cellPixelSize == nil {
		return vtegraphics.CellSize{}, false
	}
	w, h := g.cellPixelSize()
	if w <= 0 || h <= 0 {
		return vtegraphics.CellSize{}, false
	}
	return vtegraphics.CellSize{Width: w, Height: h}, true
}
