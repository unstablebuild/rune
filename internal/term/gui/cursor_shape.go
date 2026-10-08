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
	"sync"
	
	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component"
)

// Arbiter exposes an interface for graphical components to send messages to it.
type Arbiter interface {
	SendToArbiter(CursorShapeMessage)
	arbitrate()
}

// CursorShapeArbiter is an Arbiter singleton. cursorShapeChan is shared across
// callers of SendToArbiter, but is hidden from them.
type CursorShapeArbiter struct {
	cursorShapeChan chan CursorShapeMessage
}

var (
	cursorShapeOnce sync.Once
	cursorShapeArbiterInstance CursorShapeArbiter
)

// GetCursorShapeArbiter returns the same instance to the graphical components.
func GetCursorShapeArbiter() *CursorShapeArbiter {
	cursorShapeOnce.Do(func () {
		// Channel size is arbitrarily large
		cursorShapeArbiterInstance = CursorShapeArbiter{make(chan(CursorShapeMessage), 100)}
	})
	return &cursorShapeArbiterInstance
}

// CursorShapeMessage defines a message that is sent to the CursorShapeArbiter.
type CursorShapeMessage struct {
	object ObjectUnderCursor
	cursorShape ebiten.CursorShapeType
}

// ObjectUnderCursor indicates what a graphical component believes is under
// the cursor; it may have false beliefs, meaning there may be other objects
// under the cursor that it doesn't know about. Neighbors are cells next to objects.
type ObjectUnderCursor uint8

const (
	// None is a special value only used for a new and empty CursorShapeMessage.
	None ObjectUnderCursor = 0
	// Link represents a link
	Link ObjectUnderCursor = 1 << iota
	// LinkNeighbor is a cell neighboring a link
	LinkNeighbor
	// ScrollBar represents a scroll bar
	ScrollBar
	// ScrollBarNeighbor is a cell neighboring a scroll bar
	ScrollBarNeighbor
	// ResizeBorder represents a resize border
	ResizeBorder
	// ResizeBorderNeighbor is a cell neighboring a resize border
	ResizeBorderNeighbor
	// Empty is a cell that has no object, and is not even a neighbor.
	Empty
)

// SendToArbiter is used by multiple graphical components to defer the decision
// of which cursor shape to set to the Arbiter.
func (a CursorShapeArbiter) SendToArbiter(msg CursorShapeMessage) {
	a.cursorShapeChan <- msg
}

// arbitrate tells Arbtier to review all messages and decide which cursor shape
// to set. It assumes that all CursorShapeMessages were sent when the cursor
// was at the same position, over the same cell.
func (a CursorShapeArbiter) arbitrate() {
	winningMsg := CursorShapeMessage{}
	for {
		select {
		case msg := <-a.cursorShapeChan:
			// Compare previously winningMsg to incoming msg
			switch winningMsg.object {
			case None:
				// There is no winningMsg yet, so the incoming msg auto wins
				winningMsg = msg
				
			case Empty:
				// A graphical component sends an Empty object when it believes there is
				// truly nothing in its cell, not even a neighbor. However, it may not
				// know about other components, and those components may have objects or
				// neighbors at that cell. If there are any, they win.
				switch msg.object {
				case Empty:
					break
				default:
					// If other components have objects or neighbors at that cell, they win.
					winningMsg = msg
				}
				
			case ResizeBorderNeighbor:
				// Cursor is over a ResizeBorderNeighbor
				switch msg.object {
				case Link:
					// Cursor is also over a Link
					// Link's wins
					winningMsg = msg
				default:
					// In all other cases, ResizeBorderNeighbor wins
					break
				}
			case LinkNeighbor:
				// Cursor is over a LinkNeighbor
				switch msg.object {
				case ResizeBorder:
					// Cursor is also over a ResizeBorder
					// ResizeBorder wins
					winningMsg = msg
				default:
					// In all other cases, LinkNeighbor wins
					break
				}
			}
			
		default:
			// If winningMsg is not brand new, meaning it is one of the received messages,
			// set the winning cursor shape. If there are no messages received, do nothing.
			if winningMsg.object != None {
				ebiten.SetCursorShape(winningMsg.cursorShape)
			}
			return
		}
	}
}

// ResizeBorderHandler This is the implementation, the interface lives in handler.
// TODO implement this in handler instead
type ResizeBorderHandler struct {}

// OnMouseover sets the ebiten.CursorShape when the cursor is at the positions
// mentioned in handler.handleWindowFramePress. This means it also inherits
// handler.handleWindowFramePress's conditional checks.
// mousePos is the mouse coordinates relative to the window's top left.
// win is the Window component used to determine where the resize borders are,
// if any.
func (ResizeBorderHandler) OnMouseover(mousePos term.Coordinates, win component.Window) {
	// Minimized windows cannot be resized.
	if _, minimized := win.IsMinimized(); minimized {
		return
	}
	minX, minY := 0, 0
	maxX, maxY := win.Width()-1, win.Height()-1
	
	if !win.IsFloating() {
		// Tiled windows resize from their right and bottom edges.
		right := mousePos.X == maxX
		bottom := mousePos.Y == maxY
		rightNeighbors := (mousePos.X == maxX-1 || mousePos.X == maxX+1) && !bottom
		bottomNeighbors := (mousePos.Y == maxY-1 || mousePos.Y == maxY+1) && !right
		switch {
		case right && bottom:
			ebiten.SetCursorShape(ebiten.CursorShapeNWSEResize)
		case right:
			ebiten.SetCursorShape(ebiten.CursorShapeEWResize)
		case bottom:
			ebiten.SetCursorShape(ebiten.CursorShapeNSResize)
		case rightNeighbors || bottomNeighbors :
			// Here the cursor is at a ResizeBorderNeighbor cell.
			// ResizeBorderHandler wants to reset the cursor shape to default, but the
			// cursor could be over another graphical component, so it defers to the
			// CursorShapeArbiter.
			GetCursorShapeArbiter().SendToArbiter(
				CursorShapeMessage{ResizeBorderNeighbor, ebiten.CursorShapeDefault})
		default:
			// All other cells "have Empty objects", these are treated differently by the Arbiter.
			GetCursorShapeArbiter().SendToArbiter(
				CursorShapeMessage{Empty, ebiten.CursorShapeNotAllowed})
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
		left := mousePos.X == minX
		right := mousePos.X == maxX
		top := mousePos.Y == minY
		bottom := mousePos.Y == maxY
		leftNeighbors := (mousePos.X == minX-1 || mousePos.X == minX+1) && !top && !bottom
		rightNeighbors := (mousePos.X == maxX-1 || mousePos.X == maxX+1) && !top && !bottom
		topNeighbors := (mousePos.Y == minY-1 || mousePos.Y == minY+1) && !left && !right
		bottomNeighbors := (mousePos.Y == maxY-1 || mousePos.Y == maxY+1) && !left && !right
		switch {
		case (left && top) || (right && bottom):
			ebiten.SetCursorShape(ebiten.CursorShapeNWSEResize)
		case (right && top) || (left && bottom):
			ebiten.SetCursorShape(ebiten.CursorShapeNESWResize)
		case left || right:
			ebiten.SetCursorShape(ebiten.CursorShapeEWResize)
		case bottom || (!win.HasWindowBar() && top):
			ebiten.SetCursorShape(ebiten.CursorShapeNSResize)
		case leftNeighbors || rightNeighbors || topNeighbors || bottomNeighbors:
			// Here the cursor is at a ResizeBorderNeighbor cell.
			// Same as before, defer to the CursorShapeArbiter.
			GetCursorShapeArbiter().SendToArbiter(
				CursorShapeMessage{ResizeBorderNeighbor, ebiten.CursorShapeDefault})
		default:
			// All other cells "have Empty objects", these are treated differently by the Arbiter.
			GetCursorShapeArbiter().SendToArbiter(
				CursorShapeMessage{Empty, ebiten.CursorShapeNotAllowed})
		}
	}
}
