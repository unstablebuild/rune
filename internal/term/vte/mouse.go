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

import (
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
)

type mouseDriver struct {
	t              *Component
	selectionStart term.Coordinates
	// selectionStartScrollY is the buffer scroll offset captured when
	// selectionStart was anchored. Select/SelectEnd translate window
	// coordinates by the *current* scroll offset, so an upward drag that
	// auto-scrolls the buffer would otherwise drag the start anchor along
	// with the content. Recording the offset lets SetSelectionEnd keep the
	// anchor pinned to the originally pressed content cell.
	selectionStartScrollY int
	// dragging is set once a drag leaves the pressed cell. The SDK calls
	// SetSelectionEnd for every held-button event, including jitter
	// inside the pressed cell, so without it a plain click (for example
	// to focus the window) would highlight one cell and copy it over
	// whatever the clipboard held.
	dragging     bool
	hookRawBytes []byte
	clipboard    clipboard.Register
	lastButton   rune // last pressed button (0=left, 1=middle, 2=right)
}

func (e *mouseDriver) OnAction(
	ev term.Event, pos term.Coordinates, action mouse.Action,
) bool {
	if e.appTracksMouse() {
		return e.reportAction(ev, pos, action)
	}
	switch action {
	case mouse.MiddleClick:
		paste, _ := e.clipboard.Paste(clipboard.DefaultRegisterID)
		e.hookRawBytes = []byte(paste.Text)
		return true
	case mouse.Release:
		// Copy once the gesture is finished. Copying on every drag
		// tick spawns a clipboard process synchronously, and a slow
		// one stalls the UI on the first cell that leaves the anchor,
		// so the highlight never grows past those two cells.
		if e.dragging {
			e.copySelectionToClipboard()
		}
		return false
	default:
		return false
	}
}

func (e *mouseDriver) reportAction(
	ev term.Event, pos term.Coordinates, action mouse.Action,
) bool {
	tx, ty := pos.X, pos.Y
	var button rune
	switch action {
	case mouse.WheelUp:
		e.hookRawBytes = ev.Raw
		return true
	case mouse.WheelDown:
		e.hookRawBytes = ev.Raw
		return true
	case mouse.LeftClick:
		button = 0
		e.lastButton = 0
	case mouse.MiddleClick:
		button = 1
		e.lastButton = 1
	case mouse.RightClick:
		button = 2
		e.lastButton = 2
	case mouse.Release:
		if e.t.MouseModeSgrMouse() {
			button = e.lastButton
		} else {
			button = 3
		}
	default:
		return false
	}

	if e.t.MouseModeSgrMouse() {
		final := 'M'
		if action == mouse.Release {
			final = 'm'
		}
		e.hookRawBytes = fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", button, tx+1, ty+1, final)
	} else {
		e.hookRawBytes = fmt.Appendf(nil, "\x1b[M%c%c%c", button+32, tx+33, ty+33)
	}
	return true
}

func (e *mouseDriver) ScrollUp(n int) (ok bool) {
	e.t.ScrollUp(n)
	return
}

func (e *mouseDriver) ScrollDown(n int) (ok bool) {
	e.t.ScrollDown(n)
	return
}

func (e *mouseDriver) ClearSelection() {
	e.t.Unselect()
	e.selectionStart = term.Coordinates{}
	e.selectionStartScrollY = 0
	e.dragging = false
}

func (e *mouseDriver) SetSelectionStart(pos term.Coordinates) {
	e.selectionStart = pos
	e.selectionStartScrollY = e.t.scrollY()
	e.dragging = false
}

func (e *mouseDriver) SetSelectionEnd(pos term.Coordinates) {
	// When the running program tracks the mouse, the press was reported
	// to it and never anchored a selection here. Highlighting on the
	// following drag events would paint a stale selection over the
	// program's own (e.g. vim's visual mode) and copy it. Motion while
	// a button is held still belongs to the program, so report it.
	if e.appTracksMouse() {
		e.reportMotion(pos)
		return
	}
	// The start was captured at selectionStartScrollY, but Select translates
	// window->buffer coordinates by subtracting the current scroll offset.
	// Shift the stored start into the current window coordinate system by the
	// scroll delta so it maps back to the same content cell as the buffer
	// scrolls (scrollY grows as the view scrolls up toward older rows).
	start := e.selectionStart
	start.Y += e.t.scrollY() - e.selectionStartScrollY
	// Select/SelectEnd assume reading order: from is the top-left and
	// to is one past the bottom-right. A leftward drag would otherwise
	// produce from > to, and the buffer's internal sort then drops the
	// press cell and the drag-end cell from the selection.
	end := pos
	if !e.dragging {
		if end == start {
			return
		}
		e.dragging = true
	}
	if end.Y < start.Y || (end.Y == start.Y && end.X < start.X) {
		start, end = end, start
	}
	e.t.Select(start)
	e.t.SelectEnd(end)
}

func (e *mouseDriver) SelectWordAt(pos term.Coordinates) {
	e.t.SelectWordAt(pos)
	e.copySelectionToClipboard()
}

func (e *mouseDriver) SelectLine(y int) {
	pos := term.Coordinates{Y: y}
	e.t.SelectLine(pos)
	e.copySelectionToClipboard()
}

// appTracksMouse reports whether the running program asked for mouse
// events, in which case clicks and drags belong to it.
func (e *mouseDriver) appTracksMouse() bool {
	return e.t.MouseModeReportMouseClicks() || e.tracksMotion()
}

// tracksMotion reports whether the program wants motion while a button
// is held (1002) or all motion (1003). Click-only tracking (1000) does
// not.
func (e *mouseDriver) tracksMotion() bool {
	return e.t.MouseModeReportCellMouseMotion() || e.t.MouseModeReportAllMouseMotion()
}

// reportMotion encodes a button-held move for the program. The legacy
// protocol stores the motion flag (32) on top of the 32 already added
// when the button byte is written; SGR carries that flag in the button
// field itself.
func (e *mouseDriver) reportMotion(pos term.Coordinates) {
	if !e.tracksMotion() {
		return
	}
	button := e.lastButton + 32
	tx, ty := pos.X, pos.Y
	if e.t.MouseModeSgrMouse() {
		e.hookRawBytes = fmt.Appendf(nil, "\x1b[<%d;%d;%dM", button, tx+1, ty+1)
	} else {
		e.hookRawBytes = fmt.Appendf(nil, "\x1b[M%c%c%c", button+32, tx+33, ty+33)
	}
}

func (e *mouseDriver) Width() int {
	return e.t.MaxWidth()
}

func (e *mouseDriver) Height() int {
	return e.t.Height()
}

func (e *mouseDriver) copySelectionToClipboard() {
	data, _ := e.t.Selection()
	clipdata := clipboard.Data{Text: data}
	err := e.clipboard.Copy(clipboard.DefaultRegisterID, clipdata)
	if err != nil {
		e.log(log.ErrorLevel, "copy clipboard data to register: %v", err)
	}
}

func (e *mouseDriver) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "vte.mouseDriver").
		Logf(level, msg, args...)
}
