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

package vteregistry

import "sync/atomic"

// maxDimension bounds a pty's columns and rows. The grid is allocated
// eagerly, so an attachment asking for an implausible size would make
// the owner of every terminal in this workspace allocate until it dies.
// No display comes near this, and a client that asks for more is told
// the size it actually got, as it is when it loses the size to another
// attachment.
const maxDimension = 2048

// clampDimensions keeps a requested size inside what a terminal can
// be. Zero and negative sizes reach here from clients that have not
// laid out a window yet.
func clampDimensions(width, height int) (int, int) {
	return min(max(width, 1), maxDimension), min(max(height, 1), maxDimension)
}

// maxCellPixels bounds the size of a cell in pixels a client may ask
// for. No display has cells anywhere near this large.
const maxCellPixels = 1024

// cellSize is the size of a cell in pixels. The zero value is none: a
// display that cannot draw images, for which graphics are off.
type cellSize struct{ width, height int }

// cellSizeOf is the cell size a peer named, which is none unless both
// of its dimensions are.
func cellSizeOf(width, height int) cellSize {
	if width <= 0 || height <= 0 {
		return cellSize{}
	}
	return cellSize{
		width:  min(width, maxCellPixels),
		height: min(height, maxCellPixels),
	}
}

// atomicCellSize is a cell size an emulator reads from its parse
// goroutine while whoever changes it may be waiting on that
// goroutine's feed.
type atomicCellSize struct{ v atomic.Uint64 }

func (a *atomicCellSize) load() cellSize {
	v := a.v.Load()
	return cellSize{width: int(v >> 32), height: int(uint32(v))}
}

func (a *atomicCellSize) store(c cellSize) {
	a.v.Store(uint64(uint32(c.width))<<32 | uint64(uint32(c.height)))
}

// pixels satisfies [vte.Config.CellPixelSize].
func (a *atomicCellSize) pixels() (width, height int) {
	c := a.load()
	return c.width, c.height
}
