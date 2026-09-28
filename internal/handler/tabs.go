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
)

var _ tui.Handler = (*Tabs)(nil)

// Tabs add mouse handling to component.Tabs.
type Tabs struct {
	component.Tabs
	mousePressedLeft bool
	// iconPressed records that the left button went down on the icon of
	// the tab at pressedIcon and has not been released yet.
	iconPressed bool
	pressedIcon int
	OnClick     func(int) bool
	// OnIconClick, when set, receives clicks on a tab's icon instead of
	// OnClick, and the tab does not take focus. A click is a press and a
	// release that never leave the icon: dragging off it, even back onto
	// it, releasing elsewhere or any other mouse event in between cancels
	// the click.
	OnIconClick func(int)
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
}

// Handle delegates the event to the underlying handler.
func (t *Tabs) Handle(ev term.Event) (quit, handled bool) {
	defer func() {
		// if button is released then MouseRelease is dispatched
		// so this is reset
		t.mousePressedLeft = ev.Key == term.MouseLeft
	}()

	if ev.Type != term.EventMouse {
		return
	}
	mousePos := term.Coordinates{X: ev.MouseX, Y: ev.MouseY}
	if t.iconPressed && t.followIconPress(ev, mousePos) {
		return false, true
	}
	if ev.Key != term.MouseLeft || ev.Mod != 0 {
		return
	}

	// do not dispatch drags as multiple click events:
	// MouseRelease must be dispatched between MouseLeft for
	// events to be considered multiple mouse clicks.
	if t.mousePressedLeft {
		return
	}
	if t.OnIconClick != nil {
		if idx, ok := t.Tabs.TabIconAt(mousePos); ok {
			t.iconPressed, t.pressedIcon = true, idx
			return false, true
		}
	}
	idx, ok := t.Tabs.TabAt(mousePos)
	if !ok {
		if t.OnClick != nil {
			handled = t.OnClick(-1)
		}
		return
	}

	handled = true

	t.SetFocus(idx)
	if t.OnClick != nil {
		_ = t.OnClick(idx)
	}
	return
}

// followIconPress tracks a press that landed on an icon and reports
// whether ev belongs to it: the release, which clicks the icon when it
// is still under the pointer, or a drag that has not left the icon yet.
// Any other event cancels the press and is left to the caller.
func (t *Tabs) followIconPress(ev term.Event, pos term.Coordinates) bool {
	idx, ok := t.Tabs.TabIconAt(pos)
	onIcon := ok && idx == t.pressedIcon
	switch {
	case ev.Key == term.MouseRelease:
		t.iconPressed = false
		if onIcon {
			t.OnIconClick(idx)
		}
		return true
	case ev.Key == term.MouseLeft && t.mousePressedLeft && onIcon:
		return true
	}
	t.iconPressed = false
	return false
}

// Remove removes the tab at idx. A press held on an icon is dropped, as
// the index it recorded may now belong to another tab.
func (t *Tabs) Remove(idx int) bool {
	t.iconPressed = false
	return t.Tabs.Remove(idx)
}

// RemoveAll removes all tabs and drops a press held on an icon.
func (t *Tabs) RemoveAll() bool {
	t.iconPressed = false
	return t.Tabs.RemoveAll()
}

// MoveLeft moves the tab at idx to the left. A press held on an icon is
// dropped, as the index it recorded may now belong to another tab.
func (t *Tabs) MoveLeft(idx int) bool {
	t.iconPressed = false
	return t.Tabs.MoveLeft(idx)
}

// MoveRight moves the tab at idx to the right. A press held on an icon
// is dropped, as the index it recorded may now belong to another tab.
func (t *Tabs) MoveRight(idx int) bool {
	t.iconPressed = false
	return t.Tabs.MoveRight(idx)
}

// MoveTo moves the tab at curridx to idx. A press held on an icon is
// dropped, as the index it recorded may now belong to another tab.
func (t *Tabs) MoveTo(curridx, idx int) bool {
	t.iconPressed = false
	return t.Tabs.MoveTo(curridx, idx)
}

// Cursor satisfies tui.Handler but always returns false.
func (t *Tabs) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

// Selection satisfies tui.Handler but always returns false.
func (t *Tabs) Selection() (string, bool) {
	return "", false
}
