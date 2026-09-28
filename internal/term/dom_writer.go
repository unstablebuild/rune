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

//go:build js

package term

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

const (
	domPropertyWidth   = "clientWidth"
	domPropertyHeight  = "clientHeight"
	domEventResize     = "resize"
	domEventKeyUp      = "keyup"
	domEventKeyDown    = "keydown"
	domEventUnload     = "onunload"
	lineHeight         = 12.4
	effectiveFontWidth = 6.1
)

// for now uses global document/window but in the future
// we could virtualize the window and enable using a single element
// as the terminal
// (if mouse is over element, then dispatch to virtual window event listeners).
type domWriter struct {
	w       *HTMLWriter
	attr    Attributes
	emitter domEventEmitter
	body    jsValue
	term    jsValue
	mod     Modifier
	ch      chan Event
}

// stub for domWriter's body and term
type jsValue interface {
	Get(name string) jsValue
	Set(string, interface{})
	Int() int
}

// stub for domWriter's window
type domEventEmitter interface {
	AddEventListener(string, domEventHandler)
}

type domEventHandler func(jsEvent)

type jsEvent interface {
	KeyCode() int
	Type() string
}

func newDomWriter(
	emitter domEventEmitter, body jsValue, term jsValue, cursorStyle string,
) *domWriter {
	w := new(domWriter)
	w.ch = make(chan Event)
	w.attr = Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack}
	w.emitter = emitter
	w.body = body
	w.term = term
	width, height := w.Size()
	w.w = NewHTMLWriter(width, height)
	w.w.SetCursorStyle(cursorStyle)
	w.emitter.AddEventListener(domEventResize, w.handleDomEvent)
	w.emitter.AddEventListener(domEventKeyUp, w.handleDomEvent)
	w.emitter.AddEventListener(domEventKeyDown, w.handleDomEvent)
	w.emitter.AddEventListener(domEventUnload, w.handleDomEvent)
	// TODO subscribe and dispatch mouse events with ModMotion
	return w
}

func (w *domWriter) dispatchEvent(ev Event) {
	w.ch <- ev
}

func (w *domWriter) handleModifier(code int, setMod Modifier) bool {
	mod := Modifier(code)
	switch mod {
	case ModShift, ModCtrl, ModAlt:
		// modShift takes precedence over ModAlt
		// modCtrl takes precendence over any other mod
		if setMod == 0 || mod > w.mod {
			w.mod = setMod
		}
		return true
	default:
		return false
	}
}

func mapEventToKey(code int) (key Key, ok bool) {
	key = Key(code)
	switch key {
	case KeyF1, KeyF2, KeyF3, KeyF4, KeyF5, KeyF6, KeyF7,
		KeyF8, KeyF9, KeyF10, KeyF11, KeyF12, KeyInsert, KeyDelete,
		KeyHome, KeyEnd, KeyPgup, KeyPgdn, KeyArrowUp, KeyArrowDown, KeyArrowLeft,
		KeyArrowRight, KeyBackspace, KeyTab, KeyEnter, KeyEsc, KeySpace, KeyBackspace2:
		ok = true
	}
	return
}

func mapEventToKeyName(code int) (name string) {
	if code >= len(keyboardCharTable) {
		return strconv.Itoa(code)
	}

	return keyboardNameTable[code]
}

func mapEventToCharacter(code int, mod Modifier) (ch rune, ok bool) {
	if code >= len(keyboardCharTable) {
		return
	}

	res := keyboardCharTable[code]
	switch mod {
	case ModShift:
		ch = res.shifted
	default:
		ch = res.unshifted
	}

	if ch != 0 {
		ok = true
	}

	return
}

func makeCharEvent(ch rune, mod Modifier) Event {
	switch mod {
	case ModCtrl:
	case ModShift:
		return Event{Type: EventKey, Ch: ch}
	default:
		return Event{Type: EventKey, Ch: ch, Mod: mod}
	}

	var key Key
	switch ch {
	case '`':
		key = KeyCtrlTilde
	case '[':
		key = KeyCtrlLsqBracket
	case ']':
		key = KeyCtrlRsqBracket
	case '\\':
		key = KeyCtrlBackslash
	case '/':
		key = KeyCtrlSlash
	case '-':
		key = KeyCtrlUnderscore
	case '2':
		key = KeyCtrl2
	case 'a', 'A':
		key = KeyCtrlA
	case 'b', 'B':
		key = KeyCtrlB
	case 'c', 'C':
		key = KeyCtrlC
	case 'd', 'D':
		key = KeyCtrlD
	case 'e', 'E':
		key = KeyCtrlE
	case 'f', 'F':
		key = KeyCtrlF
	case 'g', 'G':
		key = KeyCtrlG
	case 'h', 'H':
		key = KeyCtrlH
	case 'i', 'I':
		key = KeyCtrlI
	case 'j', 'J':
		key = KeyCtrlJ
	case 'k', 'K':
		key = KeyCtrlK
	case 'l', 'L':
		key = KeyCtrlL
	case 'm', 'M':
		key = KeyCtrlM
	case 'n', 'N':
		key = KeyCtrlN
	case 'o', 'O':
		key = KeyCtrlO
	case 'p', 'P':
		key = KeyCtrlP
	case 'q', 'Q':
		key = KeyCtrlQ
	case 'r', 'R':
		key = KeyCtrlR
	case 's', 'S':
		key = KeyCtrlS
	case 't', 'T':
		key = KeyCtrlT
	case 'u', 'U':
		key = KeyCtrlU
	case 'v', 'V':
		key = KeyCtrlV
	case 'w', 'W':
		key = KeyCtrlW
	case 'x', 'X':
		key = KeyCtrlX
	case 'y', 'Y':
		key = KeyCtrlY
	case 'z', 'Z':
		key = KeyCtrlZ
	case '3':
		key = KeyCtrl3
	case '4':
		key = KeyCtrl4
	case '5':
		key = KeyCtrl5
	case '6':
		key = KeyCtrl6
	case '7':
		key = KeyCtrl7
	case '8':
		key = KeyCtrl8
	default:
		return Event{Type: EventKey, Ch: ch}
	}

	return Event{Type: EventKey, Key: key}
}

func makeKeyEvent(key Key, mod Modifier) Event {
	switch mod {
	case ModCtrl:
	case ModShift:
		return Event{Type: EventKey, Key: key}
	default:
		return Event{Type: EventKey, Key: key, Mod: mod}
	}

	switch key {
	case KeySpace:
		key = KeyCtrlSpace
	}

	return Event{Type: EventKey, Key: key}
}

func makeKeyDownEvent(code int, mod Modifier) (Event, error) {
	ch, ok := mapEventToCharacter(code, mod)
	if ok {
		return makeCharEvent(ch, mod), nil
	}

	key, ok := mapEventToKey(code)
	if ok {
		return makeKeyEvent(key, mod), nil
	}

	name := mapEventToKeyName(code)
	return Event{}, fmt.Errorf("could not map event key code %d: %s", code, name)
}

func (w *domWriter) handleDomEvent(ev jsEvent) {
	code, tpe := ev.KeyCode(), ev.Type()

	switch tpe {
	case domEventKeyUp:
		w.handleModifier(code, Modifier(0))
	case domEventKeyDown:
		if w.handleModifier(code, Modifier(code)) {
			return
		}
		termEv, err := makeKeyDownEvent(code, w.mod)
		if err != nil {
			// log.Println(err)
			return
		}
		w.dispatchEvent(termEv)
	case domEventResize:
		width, height := w.Size()
		w.w = NewHTMLWriter(width, height)
		termEv := Event{Type: EventResize, Width: width, Height: height}
		w.dispatchEvent(termEv)
	case domEventUnload:
		w.Close()
	}
}

func (w *domWriter) SetCell(pos Coordinates, c Cell) {
	w.w.SetCell(pos, c)
}

func (w *domWriter) UnionAttributes(pos Coordinates, attr Attributes) {
	w.w.UnionAttributes(pos, attr)
}

func (w *domWriter) DrawImage(img Image) bool {
	return w.w.DrawImage(img)
}

func (w *domWriter) Flush() error {
	err := w.w.Flush()
	if err != nil {
		return err
	}
	str := w.w.HTML()
	w.term.Set("innerHTML", str)
	return nil
}

func (w *domWriter) Clear(attr Attributes) (err error) {
	err = w.w.Clear(attr)
	return
}

func (w *domWriter) SetCursor(pos Coordinates) {
	w.w.SetCursor(pos)
}
func (w *domWriter) Attr() Attributes {
	return w.attr
}

func (w *domWriter) Size() (width int, height int) {
	clientWidth := w.body.Get(domPropertyWidth).Int()
	clientHeight := w.body.Get(domPropertyHeight).Int()

	width = int(math.Max(1, math.Floor(float64(clientWidth)/effectiveFontWidth)))
	height = int(math.Max(1, math.Floor(float64(clientHeight)/lineHeight)))
	return
}

func (w *domWriter) SetAttr(newAttr Attributes) {
	w.attr = newAttr
}

func (w *domWriter) PollEvent() (ev Event) {
	if w.ch == nil {
		return Event{Type: EventError, Err: errors.New("channel was closed")}
	}
	return <-w.ch
}

func (w *domWriter) Close() error {
	close(w.ch)
	w.ch = nil
	return nil
}

func (w *domWriter) Interrupt() {
	if w.ch == nil {
		panic("Interrupt called after Close was called")
	}
	w.ch <- Event{Type: EventInterrupt}
}

func (w *domWriter) SendNoneEvent() {
	if w.ch == nil {
		panic("SendNoneEvent called after Close was called")
	}
	w.ch <- Event{Type: EventNone}
}
