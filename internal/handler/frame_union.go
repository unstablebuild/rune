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

package handler

import (
	"slices"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// FrameUnion wraps a component.FrameUnion to satisfy tui.Handler by
// dispatching mouse events to FrameUnion nodes and returning the
// correct main Cursor offset.
//
// See component.FrameUnion for more details.
type FrameUnion struct {
	main tui.Handler
	component.FrameUnion

	// capturing are the members registered with CaptureDrags.
	capturing []tui.Handler
	// leftHeld reports that a left press came and no release or hover
	// has since. pressed is the member it came on when that member
	// captures drags.
	leftHeld        bool
	pressedCaptures bool
	pressed         component.Virtual[tui.Component]
}

// NewFrameUnion allocates storage for a new FrameUnion and initializes it.
func NewFrameUnion(main tui.Handler) *FrameUnion {
	ret := new(FrameUnion)
	ret.Init(main)
	return ret
}

// Init initializes this frame union with main.
func (u *FrameUnion) Init(main tui.Handler) {
	u.main = main
	u.FrameUnion.Init(main)
}

// UnionTop stacks top on top of the main handler. This
// method panics if top is nil.
func (u *FrameUnion) UnionTop(top tui.Component, height int) {
	u.FrameUnion.UnionTop(top, height)
}

// UnionTopFrame stacks top on top of the main handler, and if
// u.Frame is set to true and the given frame argument too, it will
// union the frames of the adjacent handlers with the configured
// union charset. This method panics if top is nil.
func (u *FrameUnion) UnionTopFrame(top tui.Component, height int, frame bool) {
	u.FrameUnion.UnionTopFrame(top, height, frame)
}

// UnionBottom stacks bottom under of the main handler. This
// method panics if bottom is nil.
func (u *FrameUnion) UnionBottom(bottom tui.Component, height int) {
	u.FrameUnion.UnionBottom(bottom, height)
}

// UnionBottomFrame stacks bottom under of the main handler, and if
// u.Frame is set to true and the given frame argument too, it will
// union the frames of the adjacent handlers with the configured
// union charset. This method panics if bottom is nil.
func (u *FrameUnion) UnionBottomFrame(bottom tui.Component, height int, frame bool) {
	u.FrameUnion.UnionBottomFrame(bottom, height, frame)
}

// UnionLeft stacks left to the left of the main handler. This
// method panics if left is nil.
func (u *FrameUnion) UnionLeft(left tui.Component, width int) {
	u.FrameUnion.UnionLeft(left, width)
}

// UnionLeftFrame stacks left to the left of the main handler, and if
// u.Frame is set to true and the given frame argument too, it will
// union the frames of the adjacent handlers with the configured
// union charset. This method panics if left is nil.
func (u *FrameUnion) UnionLeftFrame(left tui.Component, width int, frame bool) {
	u.FrameUnion.UnionLeftFrame(left, width, frame)
}

// UnionRight stacks right to the right of the main handler. This
// method panics if right is nil.
func (u *FrameUnion) UnionRight(right tui.Component, width int) {
	u.FrameUnion.UnionRight(right, width)
}

// UnionRightFrame stacks right to the right of the main handler, and if
// u.Frame is set to true and the given frame argument too, it will
// union the frames of the adjacent handlers with the configured
// union charset. This method panics if right is nil.
func (u *FrameUnion) UnionRightFrame(right tui.Component, width int, frame bool) {
	u.FrameUnion.UnionRightFrame(right, width, frame)
}

// CaptureDrags makes member, which must already be part of this union,
// own the whole gesture of every left press on it: the drags and the
// release reach member wherever the pointer goes, with coordinates past
// its bounds once the pointer leaves it, and drags that start on other
// members never reach it. Members that tell clicks from drags need both.
func (u *FrameUnion) CaptureDrags(member tui.Handler) {
	u.capturing = append(u.capturing, member)
}

// Resize satisfies tui.Handler.
func (u *FrameUnion) Resize(width, height int) {
	u.FrameUnion.Resize(width, height)
}

// Draw satisfies tui.Handler.
func (u *FrameUnion) Draw(w term.Writer) {
	u.FrameUnion.Draw(w)
}

// Handle delegates ev to main handler, unless event is a mouse event,
// in which case it's delegated to the component at ev.MouseX and ev.MouseY,
// except for the drags and releases CaptureDrags reserves.
func (u *FrameUnion) Handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventMouse {
		return u.main.Handle(ev)
	}

	c, ok := u.FrameUnion.ComponentAt(term.Coordinates{X: ev.MouseX, Y: ev.MouseY})
	dragging := u.leftHeld && (ev.Key == term.MouseLeft || ev.Key == term.MouseRelease)
	switch ev.Key {
	case term.MouseLeft:
		if !u.leftHeld {
			u.leftHeld = true
			u.pressedCaptures = ok && u.captures(c.C)
			u.pressed = c
		}
	case term.MouseRelease, 0:
		// Hovering means no button is held, so a release that never
		// reached this union cannot hold on to the next press.
		u.leftHeld = false
	}
	if dragging {
		if u.pressedCaptures {
			ev.MouseX -= u.pressed.Position().X
			ev.MouseY -= u.pressed.Position().Y
			return u.pressed.C.(tui.Handler).Handle(ev)
		}
		if ok && u.captures(c.C) {
			return
		}
	}
	if !ok {
		return
	}
	handler := c.C.(tui.Handler)
	ev.MouseX -= c.Position().X
	ev.MouseY -= c.Position().Y
	if ev.MouseX < 0 {
		ev.MouseX = 0
	}
	if ev.MouseY < 0 {
		ev.MouseY = 0
	}
	return handler.Handle(ev)
}

func (u *FrameUnion) captures(c tui.Component) bool {
	return slices.ContainsFunc(u.capturing, func(h tui.Handler) bool {
		return tui.Component(h) == c
	})
}

// Cursor returns the main component's cursor position.
func (u *FrameUnion) Cursor() (pos term.Coordinates, style term.CursorStyle, show bool) {
	o := u.FrameUnion.MainPosition()
	pos, style, show = u.main.Cursor()
	pos.X += o.X
	pos.Y += o.Y
	return
}

// Selection returns the main component's selection.
func (u *FrameUnion) Selection() (string, bool) {
	return u.main.Selection()
}
