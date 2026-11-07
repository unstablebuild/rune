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
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/sounds"
)

var _ tui.Handler = (*Tabs)(nil)

// Tabs add mouse handling to component.Tabs. A tab is clicked by a left
// press that began on it and is released over it: the tab takes focus
// and OnClick is called with its index. A left press past the tabs
// calls OnClick with -1 at once. The pointer over a tab is a link
// pointer, as over any other control a click activates, and the tab
// shows it as component.Tabs.SetPointer and SetPressed tell.
type Tabs struct {
	component.Tabs
	// held reports that the last event was a left press or drag, so a
	// left one that follows is a drag.
	held bool
	// pressed is the tab a left press is held on, -1 for none, and armed
	// reports that the pointer is still over it.
	pressed int
	armed   bool
	// iconPressed records that the left button went down on the icon of
	// the tab at pressedIcon and has not been released yet.
	iconPressed bool
	pressedIcon int
	// hovered is the tab not in focus the pointer was last on, -1 for
	// none, for the hover sound to play once per tab it moves onto.
	hovered int
	OnClick func(int) bool
	// OnIconClick, when set, receives clicks on a tab's icon instead of
	// OnClick, and the tab does not take focus. A click is a press and a
	// release that never leave the icon: dragging off it, even back onto
	// it, releasing elsewhere or any other mouse event in between cancels
	// the click.
	OnIconClick func(int)
	// Player plays Sounds. Nil plays none.
	Player sounds.Player
	Sounds TabSounds
}

// TabSounds names the sounds Tabs plays; "" plays none.
type TabSounds struct {
	// Hover plays when the pointer moves onto a tab not in focus.
	Hover string
	// Click plays when a tab, or its icon, is clicked.
	Click string
}

// NewTabs returns a Tabs component which handles mouse events.
func NewTabs() *Tabs {
	t := new(Tabs)
	t.Init()
	return t
}

// Init initializes this Tabs with the given underlying handler
// and frame attributes.
func (t *Tabs) Init() {
	t.Tabs.Init()
	t.pressed, t.armed, t.hovered = -1, false, -1
}

// Handle handles the mouse events that click the tabs and their icons,
// and tracks the pointer over them.
func (t *Tabs) Handle(ev term.Event) (quit, handled bool) {
	if ev.Type != term.EventMouse {
		return false, false
	}
	pos := term.Coordinates{X: ev.MouseX, Y: ev.MouseY}
	held := t.held
	t.held = ev.Key == term.MouseLeft
	if ev.Key == 0 {
		// The window reports a motion only while no button is held, so
		// any press has ended, even if its release went elsewhere.
		t.endPress()
		t.iconPressed = false
	}
	t.Tabs.SetPointer(pos)
	handled = t.handleMouse(ev, pos, held)
	// A drag shows the link pointer only while it would still click.
	idle := !t.held && t.pressed < 0
	if _, ok := t.Tabs.TabUnder(pos); ok && (idle || t.armed || t.iconPressed) {
		term.SetPointerShape(ev, term.PointerLink)
	}
	return false, handled
}

// handleMouse handles ev, a mouse event at pos that follows a left press
// or drag when held, and reports whether it was the tabs'.
func (t *Tabs) handleMouse(ev term.Event, pos term.Coordinates, held bool) bool {
	if t.iconPressed && t.followIconPress(ev, pos, held) {
		return true
	}
	switch ev.Key {
	case 0:
		t.hover(pos)
		return false
	case term.MouseLeft:
		if held {
			if t.pressed < 0 {
				return false
			}
			idx, ok := t.Tabs.TabUnder(pos)
			t.armed = ok && idx == t.pressed
			t.Tabs.SetPressed(t.pressed, t.armed)
			return true
		}
		if ev.Mod != 0 {
			return false
		}
		if t.OnIconClick != nil {
			if idx, ok := t.Tabs.TabIconAt(pos); ok {
				t.iconPressed, t.pressedIcon = true, idx
				return true
			}
		}
		idx, ok := t.Tabs.TabUnder(pos)
		if !ok {
			return t.OnClick != nil && t.OnClick(-1)
		}
		t.pressed, t.armed = idx, true
		t.Tabs.SetPressed(idx, true)
		return true
	case term.MouseRelease:
		pressed, armed := t.pressed, t.armed
		t.endPress()
		if idx, ok := t.Tabs.TabUnder(pos); armed && ok && idx == pressed {
			t.click(idx)
		} else {
			t.hover(pos)
		}
		return pressed >= 0
	}
	// Any other button or the wheel ends a press, as it does an icon's.
	t.endPress()
	return false
}

// click focuses the tab at idx, which the pointer is on, and calls
// OnClick with it.
func (t *Tabs) click(idx int) {
	t.play(t.Sounds.Click)
	// The pointer is on it already, so moving off it and back is what
	// plays the hover sound next, whether or not it takes focus.
	t.hovered = idx
	t.SetFocus(idx)
	if t.OnClick != nil {
		_ = t.OnClick(idx)
	}
}

// hover plays the hover sound when the pointer at pos has moved onto a
// tab not in focus.
func (t *Tabs) hover(pos term.Coordinates) {
	idx, ok := t.Tabs.TabUnder(pos)
	if !ok || t.Tabs.Focused(idx) {
		idx = -1
	}
	if idx >= 0 && idx != t.hovered {
		t.play(t.Sounds.Hover)
	}
	t.hovered = idx
}

func (t *Tabs) play(name string) {
	if name != "" && t.Player != nil {
		t.Player.Play(name)
	}
}

// endPress drops the press held on a tab, if any.
func (t *Tabs) endPress() {
	t.pressed, t.armed = -1, false
	t.Tabs.SetPressed(-1, false)
}

// followIconPress tracks a press that landed on an icon and reports
// whether ev belongs to it: the release, which clicks the icon when it
// is still under the pointer, or a drag that has not left the icon yet.
// Any other event cancels the press and is left to the caller.
func (t *Tabs) followIconPress(ev term.Event, pos term.Coordinates, held bool) bool {
	idx, ok := t.Tabs.TabIconAt(pos)
	onIcon := ok && idx == t.pressedIcon
	switch {
	case ev.Key == term.MouseRelease:
		t.iconPressed = false
		if onIcon {
			t.play(t.Sounds.Click)
			t.OnIconClick(idx)
		}
		return true
	case ev.Key == term.MouseLeft && held && onIcon:
		return true
	}
	t.iconPressed = false
	return false
}

// Remove removes the tab at idx. A press held on a tab or an icon is
// dropped, as the index it recorded may now belong to another tab.
func (t *Tabs) Remove(idx int) bool {
	t.dropPresses()
	return t.Tabs.Remove(idx)
}

// RemoveAll removes all tabs and drops a press held on a tab or an icon.
func (t *Tabs) RemoveAll() bool {
	t.dropPresses()
	return t.Tabs.RemoveAll()
}

// MoveLeft moves the tab at idx to the left. A press held on a tab or an
// icon is dropped, as the index it recorded may now belong to another tab.
func (t *Tabs) MoveLeft(idx int) bool {
	t.dropPresses()
	return t.Tabs.MoveLeft(idx)
}

// MoveRight moves the tab at idx to the right. A press held on a tab or
// an icon is dropped, as the index it recorded may now belong to another
// tab.
func (t *Tabs) MoveRight(idx int) bool {
	t.dropPresses()
	return t.Tabs.MoveRight(idx)
}

// MoveTo moves the tab at curridx to idx. A press held on a tab or an
// icon is dropped, as the index it recorded may now belong to another
// tab.
func (t *Tabs) MoveTo(curridx, idx int) bool {
	t.dropPresses()
	return t.Tabs.MoveTo(curridx, idx)
}

func (t *Tabs) dropPresses() {
	t.iconPressed = false
	t.endPress()
}

// Cursor satisfies tui.Handler but always returns false.
func (t *Tabs) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

// Selection satisfies tui.Handler but always returns false.
func (t *Tabs) Selection() (string, bool) {
	return "", false
}
