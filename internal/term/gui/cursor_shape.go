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
	"sync"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"unstable.build/rune/internal/handler"
)

// Arbiter exposes an interface for graphical components to send messages to it.
type Arbiter interface {
	SendToArbiter(CursorShapeMessage)
	arbitrate()
}

// cursorShapeArbiter is an Arbiter singleton. cursorShapeChan is shared across
// callers of SendToArbiter, but is hidden from them.
type cursorShapeArbiter struct {
	cursorShapeChan chan CursorShapeMessage
}

var (
	cursorShapeOnce            sync.Once
	cursorShapeArbiterInstance cursorShapeArbiter
)

// CursorShapeArbiter returns the same instance to the graphical components.
func CursorShapeArbiter() *cursorShapeArbiter {
	cursorShapeOnce.Do(func() {
		// Channel size is arbitrarily large
		cursorShapeArbiterInstance = cursorShapeArbiter{make(chan (CursorShapeMessage), 100)}
	})
	return &cursorShapeArbiterInstance
}

// CursorShapeMessage defines a message that is sent to the cursorShapeArbiter.
type CursorShapeMessage struct {
	object      ObjectUnderCursor
	cursorShape ebiten.CursorShapeType
}

// ObjectUnderCursor indicates what a graphical component believes is under
// the cursor; it may have false beliefs, meaning there may be other objects
// under the cursor that it doesn't know about.
// ObjectUnderCursors are usually represent GUI objects, like links, scrollbars,
// and resize borders. However, there are 3 special values that do not represent
// GUI objects: none, Empty, and NotInWindow.
type ObjectUnderCursor uint8

const (
	// none is a special value only used for a new and empty CursorShapeMessage.
	none ObjectUnderCursor = iota
	// Empty is a special value representing a cell that has no object.
	Empty
	// NotInWindow is a special value used when the cursor is outside a window.
	NotInWindow
	// Link represents a link
	Link
	// ScrollBar represents a scroll bar
	ScrollBar
	// ResizeBorder represents a resize border
	ResizeBorder
	// CloseButton represents a window's close button
	CloseButton
)

// SendToArbiter is used by multiple graphical components to defer the decision
// of which cursor shape to set to the Arbiter.
func (a cursorShapeArbiter) SendToArbiter(msg CursorShapeMessage) {
	a.cursorShapeChan <- msg
}

// arbitrate tells Arbtier to review all messages and decide which cursor shape
// to set. It assumes that there is only one object under the cursor at a time,
// and the graphical components tell the arbiter which cursor shape to set, so
// actually the arbiter doesn't need to differentiate between the different
// kinds of objects. But ObjectUnderCursor still exports different objects for
// caller's clarity.
// arbitrate also assumes that all CursorShapeMessages were sent when the cursor
// was at the same position, over the same cell.
func (a cursorShapeArbiter) arbitrate() {
	winningMsg := CursorShapeMessage{}

	// Process all messages in cursorShapeChan
	cursorShapeChanLength := len(a.cursorShapeChan)
	for i := 0; i < cursorShapeChanLength; i++ {
		msg := <-a.cursorShapeChan
		// Consider the previously winningMsg
		switch winningMsg.object {
		case none:
			// There is no winningMsg yet, so the incoming msg auto wins
			winningMsg = msg

		case NotInWindow:
			// A graphical component sent an NotInWindow object because the mouse went
			// outside a window. There is no contest here because there are no
			// objects outside windows, or at least the arbitrator assumes so.
			// The winningMsg stays the winner.
			break

		case Empty:
			// The previously winning graphical component sent an Empty object because
			// it believes there is truly nothing in its cell. However, it may not know
			// about other components that may have objects at that cell. Those
			// components win instead.
			// We then only need to consider the special objects: none cannot be sent
			// as it is not exported, NotInWindow wins, and Empty wins (arbitrary tie).
			// In every case, the incoming message wins.
			winningMsg = msg

		default:
			// Here the previously winningMsg is an actual object that exists, like a
			// Link, a ResizeBorder, or a ScrollBar.
			// Consider what the incoming object is:
			switch msg.object {
			case Empty:
				// Empty object doesn't win against an actual object; the actual object stays the winner.
				break

			case NotInWindow:
				// The incoming message is the NotInWindow object, so it wins (see comment above).
				winningMsg = msg

			default:
				// All other objects overwrite the winner. This is not a problem if there
				// is only one object under the cursor at a time.
				winningMsg = msg
			}
		}
	}

	// If winningMsg is not brand new, meaning it is one of the received messages,
	// set the winning cursor shape. If there are no messages received, do nothing.
	if winningMsg.object != none {
	    fmt.Printf("DEBUG: winning: object=%v, cursorShape=%v\n",
	                       winningMsg.object, 
	                       winningMsg.cursorShape)
		ebiten.SetCursorShape(winningMsg.cursorShape)
	}
}

// ResizeBorderHandler satisfies handler.ResizeBorderInterface.
type ResizeBorderHandler struct {
	// lastCursorShape tracks what the ResizeBorderHandler's last cursor shape was.
	// This is internal to ResizeBorderHandler, it does not track cursor shapes set by
	// other graphical components.
	lastCursorShape ebiten.CursorShapeType
}

// sendIfDifferentShape is a helper function to send the cursor shape to
// the arbiter only if the newShape is different from the lastCursorShape.
func (h *ResizeBorderHandler) sendIfDifferentShape(object ObjectUnderCursor, newShape ebiten.CursorShapeType) {
	if newShape != h.lastCursorShape {
		h.lastCursorShape = newShape
		CursorShapeArbiter().SendToArbiter(CursorShapeMessage{object, newShape})
	}
}

// OnMouseover sets the ebiten.CursorShape when the cursor is over
// handler.MouseoverArea.
// It tracks the last cursor shape and only sets the cursor shape when it's different.
// hasWindowBar indicates whether the window has a bar.
func (h *ResizeBorderHandler) OnMouseover(area handler.MouseoverArea, hasWindowBar bool) {
	// If mouse is not over any area, then it must be outside the window.
	// Send a NotInWindow message to the CursorShapeArbiter to reset the cursor shape.
	if area == 0 {
		h.sendIfDifferentShape(NotInWindow, ebiten.CursorShapeDefault)
	}
	left := area&handler.FrameLeft != 0
	right := area&handler.FrameRight != 0
	top := area&handler.FrameTop != 0
	bottom := area&handler.FrameBottom != 0
	center := area&handler.FrameCenter != 0
	switch {
	case (left && top) || (right && bottom):
		h.sendIfDifferentShape(ResizeBorder, ebiten.CursorShapeNWSEResize)
	case (right && top) || (left && bottom):
		h.sendIfDifferentShape(ResizeBorder, ebiten.CursorShapeNESWResize)
	case left || right:
		h.sendIfDifferentShape(ResizeBorder, ebiten.CursorShapeEWResize)
	case bottom || (!hasWindowBar && top):
		h.sendIfDifferentShape(ResizeBorder, ebiten.CursorShapeNSResize)
	case center:
		// If area has FrameCenter, the cell under the cursor "has an Empty object";
		// there could be other objects in the "Empty" cell, but the ResizeBorderHandler
		// doesn't know about them.
		h.sendIfDifferentShape(Empty, ebiten.CursorShapeDefault)
	case area&handler.WindowBarCloseIcon != 0:
		// If area has WindowBarCloseIcon, the cell under the cursor is a close button.
		h.sendIfDifferentShape(CloseButton, ebiten.CursorShapeDefault)
	}
}
