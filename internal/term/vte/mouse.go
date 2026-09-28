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
	"unicode/utf8"

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
	hookRawBytes          []byte
	clipboard             clipboard.Register
	// held is the button the program saw pressed; pressed guards it
	// because the GUI repeats a held button's event on every move.
	held    int
	pressed bool
	// last is the cell of the previous event; motion is only reported
	// when it changes, as kitty does outside of SGR-pixels.
	last     term.Coordinates
	lastSeen bool
}

func (e *mouseDriver) OnAction(
	ev term.Event, pos term.Coordinates, action mouse.Action,
) bool {
	switch action {
	case mouse.MiddleClick:
		paste, _ := e.clipboard.Paste(clipboard.DefaultRegisterID)
		e.hookRawBytes = []byte(paste.Text)
		return true
	default:
		return false
	}
}

type mouseAction uint8

const (
	mousePress mouseAction = iota
	mouseRelease
	mouseDrag
	mouseMove
)

type mouseTracking uint8

const (
	mouseTrackingNone mouseTracking = iota
	// mouseTrackingClick is DECSET 1000.
	mouseTrackingClick
	// mouseTrackingButton is DECSET 1002.
	mouseTrackingButton
	// mouseTrackingAny is DECSET 1003.
	mouseTrackingAny
)

// reports mirrors kitty's filter in send_mouse_event (kitty
// mouse.c:1672-1673).
func (m mouseTracking) reports(action mouseAction) bool {
	switch m {
	case mouseTrackingAny:
		return true
	case mouseTrackingButton:
		return action != mouseMove
	case mouseTrackingClick:
		return action == mousePress || action == mouseRelease
	default:
		return false
	}
}

// mouseEncoding is the X10 byte encoding unless the program picked an
// extension.
type mouseEncoding uint8

const (
	// mouseEncodingUTF8 is DECSET 1005.
	mouseEncodingUTF8 mouseEncoding = iota + 1
	// mouseEncodingSGR is DECSET 1006.
	mouseEncodingSGR
)

type mouseModes struct {
	tracking        mouseTracking
	encoding        mouseEncoding
	alternateScroll bool
}

// Bits of the reported button byte (kitty mouse.c:33-37).
const (
	mouseShiftBit  = 1 << 2
	mouseAltBit    = 1 << 3
	mouseCtrlBit   = 1 << 4
	mouseMotionBit = 1 << 5
	mouseWheelBit  = 1 << 6
	// mouseNoButton is what a release reports in the byte encodings,
	// which cannot say which button went up, and what a move reports.
	mouseNoButton = 3
	// mouseMaxByteCell is the last cell the byte encoding can address:
	// 1-based, offset by 32 and capped at 255.
	mouseMaxByteCell = 223
)

// report encodes ev for the program when it has turned on mouse
// tracking. tracking reports whether it has, in which case ev belongs to
// the program even when there is nothing to send, and must not fall
// back to selection or scrollback.
func (e *mouseDriver) report(ev term.Event) (raw []byte, tracking bool) {
	modes := e.t.mouseModes()
	pos := term.Coordinates{X: ev.MouseX, Y: ev.MouseY}
	moved := !e.lastSeen || pos != e.last
	e.last, e.lastSeen = pos, true
	if modes.tracking == mouseTrackingNone {
		e.pressed = false
		return nil, false
	}

	var cb int
	var action mouseAction
	switch ev.Key {
	case term.MouseWheelUp:
		cb = mouseWheelBit
	case term.MouseWheelDown:
		cb = mouseWheelBit | 1
	case term.MouseLeft, term.MouseMiddle, term.MouseRight:
		cb = mouseButton(ev.Key)
		if e.pressed && e.held == cb {
			if !moved {
				return nil, true
			}
			action = mouseDrag
		}
		e.held, e.pressed = cb, true
	case term.MouseRelease:
		if !e.pressed {
			return nil, true
		}
		cb, action = e.held, mouseRelease
		e.pressed = false
	default:
		if !moved {
			return nil, true
		}
		cb, action = mouseNoButton, mouseMove
	}
	if !modes.tracking.reports(action) {
		return nil, true
	}
	switch action {
	case mouseDrag, mouseMove:
		cb |= mouseMotionBit
	case mouseRelease:
		if modes.encoding != mouseEncodingSGR {
			cb = mouseNoButton
		}
	}
	if ev.Mod&term.ModShift != 0 {
		cb |= mouseShiftBit
	}
	if ev.Mod&term.ModAlt != 0 {
		cb |= mouseAltBit
	}
	if ev.Mod&term.ModCtrl != 0 {
		cb |= mouseCtrlBit
	}
	return encodeMouse(modes.encoding, cb, action == mouseRelease, pos), true
}

func mouseButton(key term.Key) int {
	switch key {
	case term.MouseMiddle:
		return 1
	case term.MouseRight:
		return 2
	default:
		return 0
	}
}

// encodeMouse follows kitty's encode_mouse_event_impl (kitty
// mouse.c:93-120).
func encodeMouse(enc mouseEncoding, cb int, release bool, pos term.Coordinates) []byte {
	x, y := pos.X+1, pos.Y+1
	switch enc {
	case mouseEncodingSGR:
		final := 'M'
		if release {
			final = 'm'
		}
		return fmt.Appendf(nil, "\x1b[<%d;%d;%d%c", cb, x, y, final)
	case mouseEncodingUTF8:
		raw := append([]byte("\x1b[M"), byte(cb+32))
		raw = utf8.AppendRune(raw, rune(x+32))
		return utf8.AppendRune(raw, rune(y+32))
	default:
		if x > mouseMaxByteCell || y > mouseMaxByteCell {
			return nil
		}
		return []byte{0x1b, '[', 'M', byte(cb + 32), byte(x + 32), byte(y + 32)}
	}
}

// alternateScroll turns the wheel into cursor keys over the alternate
// screen, which has no scrollback of its own (DECSET 1007).
func (e *mouseDriver) alternateScroll(ev term.Event) []byte {
	var key term.Key
	switch ev.Key {
	case term.MouseWheelUp:
		key = term.KeyArrowUp
	case term.MouseWheelDown:
		key = term.KeyArrowDown
	default:
		return nil
	}
	if !e.t.mouseModes().alternateScroll {
		return nil
	}
	raw, _ := mapKeyToEscapeSequence(e.t, term.Event{Type: term.EventKey, Key: key})
	return raw
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
}

func (e *mouseDriver) SetSelectionStart(pos term.Coordinates) {
	e.selectionStart = pos
	e.selectionStartScrollY = e.t.scrollY()
}

func (e *mouseDriver) SetSelectionEnd(pos term.Coordinates) {
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
	if end.Y < start.Y || (end.Y == start.Y && end.X < start.X) {
		start, end = end, start
	}
	e.t.Select(start)
	e.t.SelectEnd(end)
	e.copySelectionToClipboard()
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
