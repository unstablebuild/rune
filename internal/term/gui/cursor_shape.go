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
	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// CursorShapeHandler is the interface defined in handler/wm.go.
// The implementation lives here in gui package.
type CursorShapeHandler struct{}

// ChangeCursorShape sets the ebiten.CursorShape when the cursor is at the
// positions mentioned in handleWindowFramePress. Currently
// handleWindowFramePress only handles presses at positions 0, w-1, and h-1
// -- a single cell.
// Args:
// - `pos` is the transformed `ev.MouseX`, `ev.MouseY` changed in
//   `wm.Handle(term.Event)`. It positions the mouse relative to the window's
//   top left, and accounts for the window frame. It is unclamped, meaning
//   it is the mouse coordinates that are not clamped to window bounds.
// - `minPos` is the top left of the box where cursor changes.
// - `maxPos` is the bottom right of the box where cursor changes.
// - `isFloating` indicates whether the window under the cursor is floating.
func (CursorShapeHandler) ChangeCursorShape(
	pos term.Coordinates, minPos term.Coordinates,
	maxPos term.Coordinates, isFloating bool) {
	minX, minY := minPos.X, minPos.Y
	maxX, maxY := maxPos.X, maxPos.Y
	
	if !isFloating {
		// Tiled windows resize from their right and bottom edges.
		right := pos.X >= maxX && pos.X < maxX+1
		bottom := pos.Y >= maxY && pos.Y < maxY+1
		switch {
		case right && bottom:
			ebiten.SetCursorShape(ebiten.CursorShapeNWSEResize)
		case right:
			ebiten.SetCursorShape(ebiten.CursorShapeEWResize)
		case bottom:
			ebiten.SetCursorShape(ebiten.CursorShapeNSResize)
		default:
			ebiten.SetCursorShape(ebiten.CursorShapeDefault)
		}
	} else {
		// Cursor for floating windows.
		// handleWindowFramePress implements drag to resize for windows with bars like this:
		// - dragging on the top side just move-drags the window,
		// - dragging on top left corner resizes both ways,
		// - dragging on top right corner resizes both ways,
		// - dragging on the other sides happen the conventional way.
		// This impacts the implementation here in that the cursor never changes to
		// CursorShapeNSResize when hovering over the top bar.
		left := pos.X >= minX && pos.X < minX+1
		right := pos.X >= maxX && pos.X < maxX+1
		top := pos.Y >= minY && pos.Y < minY+1
		bottom := pos.Y >= maxY && pos.Y < maxY+1
		switch {
		case (left && top) || (right && bottom):
			ebiten.SetCursorShape(ebiten.CursorShapeNWSEResize)
		case (right && top) || (left && bottom):
			ebiten.SetCursorShape(ebiten.CursorShapeNESWResize)
		case left || right:
			ebiten.SetCursorShape(ebiten.CursorShapeEWResize)
		case bottom:
			ebiten.SetCursorShape(ebiten.CursorShapeNSResize)
		default:
			ebiten.SetCursorShape(ebiten.CursorShapeDefault)
		}
	}
}
