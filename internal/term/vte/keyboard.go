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
	"strconv"
	"sync/atomic"
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteparser"
)

// definedKittyFlags are the progressive enhancements the kitty keyboard
// protocol defines; any other bit a program sends is dropped.
const definedKittyFlags = vteparser.KeyboardModeDisambiguateEscCodes |
	vteparser.KeyboardModeReportEventTypes |
	vteparser.KeyboardModeReportAlternateKeys |
	vteparser.KeyboardModeReportAllKeysAsEsc |
	vteparser.KeyboardModeReportAssociatedText

// Modifier bits of the kitty keyboard protocol and of modifyOtherKeys,
// whose sequences carry them plus one.
const (
	keyModShift = 1 << iota
	keyModAlt
	keyModCtrl
	keyModSuper
)

// functionalKeys holds the number and final byte the kitty keyboard
// protocol reports each key that types no text with.
var functionalKeys = map[term.Key]struct {
	code  rune
	final byte
}{
	term.KeyEsc:        {27, 'u'},
	term.KeyEnter:      {13, 'u'},
	term.KeyTab:        {9, 'u'},
	term.KeyBackspace:  {127, 'u'},
	term.KeyInsert:     {2, '~'},
	term.KeyDelete:     {3, '~'},
	term.KeyArrowLeft:  {1, 'D'},
	term.KeyArrowRight: {1, 'C'},
	term.KeyArrowUp:    {1, 'A'},
	term.KeyArrowDown:  {1, 'B'},
	term.KeyPgup:       {5, '~'},
	term.KeyPgdn:       {6, '~'},
	term.KeyHome:       {1, 'H'},
	term.KeyEnd:        {1, 'F'},
	term.KeyF1:         {1, 'P'},
	term.KeyF2:         {1, 'Q'},
	term.KeyF3:         {13, '~'},
	term.KeyF4:         {1, 'S'},
	term.KeyF5:         {15, '~'},
	term.KeyF6:         {17, '~'},
	term.KeyF7:         {18, '~'},
	term.KeyF8:         {19, '~'},
	term.KeyF9:         {20, '~'},
	term.KeyF10:        {21, '~'},
	term.KeyF11:        {23, '~'},
	term.KeyF12:        {24, '~'},
}

// kittyFlagStack is a kitty keyboard protocol flags stack. A push onto
// a full stack evicts its oldest entry, and an empty stack sets no
// flags.
type kittyFlagStack struct {
	entries [8]vteparser.KeyboardMode
	depth   int
}

func (s *kittyFlagStack) current() vteparser.KeyboardMode {
	if s.depth == 0 {
		return 0
	}
	return s.entries[s.depth-1]
}

func (s *kittyFlagStack) push(flags vteparser.KeyboardMode) {
	if s.depth == len(s.entries) {
		copy(s.entries[:], s.entries[1:])
		s.depth--
	}
	s.entries[s.depth] = flags
	s.depth++
}

func (s *kittyFlagStack) pop(n int) {
	s.depth = max(s.depth-n, 0)
}

func (s *kittyFlagStack) set(
	flags vteparser.KeyboardMode, how vteparser.KeyboardModesApplyBehavior,
) {
	if s.depth == 0 {
		s.push(0)
	}
	top := &s.entries[s.depth-1]
	switch how {
	case vteparser.KeyboardModesApplyBehaviorReplace:
		*top = flags
	case vteparser.KeyboardModesApplyBehaviorUnion:
		*top |= flags
	case vteparser.KeyboardModesApplyBehaviorDifference:
		*top &^= flags
	}
}

// keyboardState holds the key encodings programs select with the kitty
// keyboard protocol and xterm's modifyOtherKeys. The parser goroutine
// changes it and publishes the encoding in effect, which the input path
// loads without taking the terminal lock.
type keyboardState struct {
	// kitty holds the primary and alternate screen stacks, which the
	// protocol keeps independent.
	kitty [2]kittyFlagStack

	activeKitty     atomic.Uint32
	modifyOtherKeys atomic.Uint32
}

func (k *keyboardState) stack(alt bool) *kittyFlagStack {
	if alt {
		return &k.kitty[1]
	}
	return &k.kitty[0]
}

// kittyFlags returns the kitty flags of the given screen.
func (k *keyboardState) kittyFlags(alt bool) vteparser.KeyboardMode {
	return k.stack(alt).current()
}

func (k *keyboardState) pushKitty(alt bool, flags vteparser.KeyboardMode) {
	k.stack(alt).push(flags & definedKittyFlags)
	k.activate(alt)
}

func (k *keyboardState) popKitty(alt bool, n int) {
	k.stack(alt).pop(n)
	k.activate(alt)
}

func (k *keyboardState) setKitty(
	alt bool, flags vteparser.KeyboardMode, how vteparser.KeyboardModesApplyBehavior,
) {
	k.stack(alt).set(flags&definedKittyFlags, how)
	k.activate(alt)
}

// activate puts the kitty flags of the given screen in effect.
func (k *keyboardState) activate(alt bool) {
	k.activeKitty.Store(uint32(k.kittyFlags(alt)))
}

func (k *keyboardState) setModifyOtherKeys(mode vteparser.ModifyOtherKeysMode) {
	k.modifyOtherKeys.Store(uint32(mode))
}

func (k *keyboardState) reset() {
	k.kitty = [2]kittyFlagStack{}
	k.activate(false)
	k.setModifyOtherKeys(vteparser.ModifyOtherKeysReset)
}

// encoding returns the key encoding in effect.
func (k *keyboardState) encoding() keyEncoding {
	return keyEncoding{
		kitty:           vteparser.KeyboardMode(k.activeKitty.Load()),
		modifyOtherKeys: vteparser.ModifyOtherKeysMode(k.modifyOtherKeys.Load()),
	}
}

// keyEncoding is the key encoding a program selected.
type keyEncoding struct {
	kitty           vteparser.KeyboardMode
	modifyOtherKeys vteparser.ModifyOtherKeysMode
}

// reportsAllKeys reports whether keys that type text are escape
// encoded too.
func (e keyEncoding) reportsAllKeys() bool {
	return e.kitty&vteparser.KeyboardModeReportAllKeysAsEsc != 0
}

// encode returns ev as the selected encoding reports it, or false when
// ev keeps its legacy encoding.
func (e keyEncoding) encode(ev term.Event) ([]byte, bool) {
	k, ok := newKeyCode(ev)
	switch {
	case !ok:
		return nil, false
	case e.kitty != 0:
		return k.kitty(e.kitty)
	case e.modifyOtherKeys == vteparser.ModifyOtherKeysEnableAll:
		return k.modifyOtherKeys()
	}
	return nil, false
}

// keyCode is a key press as the kitty keyboard protocol and
// modifyOtherKeys report it.
type keyCode struct {
	// key is the key pressed when it types no text.
	key term.Key
	// code is the codepoint the key types without shift, or the number
	// of a key that types no text.
	code rune
	// final is the final byte of the key's CSI sequence.
	final byte
	// shifted is the codepoint shift turned code into.
	shifted rune
	// mods holds the modifier bits.
	mods int
	// text is the codepoint the key types, zero when a modifier other
	// than shift keeps it from typing.
	text rune
}

func newKeyCode(ev term.Event) (keyCode, bool) {
	var k keyCode
	if ev.Mod&term.ModShift != 0 {
		k.mods |= keyModShift
	}
	if ev.Mod&term.ModAlt != 0 {
		k.mods |= keyModAlt
	}
	if ev.Mod&term.ModCtrl != 0 {
		k.mods |= keyModCtrl
	}
	if ev.Mod&term.ModMeta != 0 {
		k.mods |= keyModSuper
	}
	ch := ev.Ch
	if ch == 0 && ev.Key == term.KeySpace {
		ch = ' '
	}
	if ch == 0 {
		fk, ok := functionalKeys[ev.Key]
		k.key, k.code, k.final = ev.Key, fk.code, fk.final
		return k, ok
	}
	if unicode.IsControl(ch) {
		return k, false
	}
	k.code, k.final = ch, 'u'
	// Shift goes into the character typed, so only the case of a letter
	// tells it was pressed.
	if lower := unicode.ToLower(ch); lower != ch {
		k.code, k.shifted = lower, ch
		k.mods |= keyModShift
	}
	if k.mods&^keyModShift == 0 {
		k.text = ch
	}
	return k, true
}

// kitty encodes k the way kitty's key_encoding.c does under flags, or
// reports false when kitty keeps the legacy encoding.
func (k keyCode) kitty(flags vteparser.KeyboardMode) ([]byte, bool) {
	disambiguate := flags&vteparser.KeyboardModeDisambiguateEscCodes != 0
	reportAll := flags&vteparser.KeyboardModeReportAllKeysAsEsc != 0
	if k.key != 0 {
		if !disambiguate && !reportAll && flags&vteparser.KeyboardModeReportEventTypes == 0 {
			return nil, false
		}
		if k.mods == 0 {
			switch k.key {
			case term.KeyEsc:
				if !disambiguate && !reportAll {
					return nil, false
				}
			case term.KeyEnter, term.KeyTab, term.KeyBackspace:
				// Kept legacy so that a shell stays usable after a
				// program exits without popping its flags.
				if !reportAll {
					return nil, false
				}
			}
		}
		return k.csi(), true
	}
	if k.text != 0 && !reportAll {
		return nil, false
	}
	if flags&vteparser.KeyboardModeReportAlternateKeys == 0 {
		k.shifted = 0
	}
	if flags&vteparser.KeyboardModeReportAssociatedText == 0 {
		k.text = 0
	}
	if k.shifted == 0 && k.text == 0 && !disambiguate && !reportAll {
		return nil, false
	}
	return k.csi(), true
}

// csi returns kitty's CSI code[:shifted][;mods[;text]] final, which
// leaves out empty trailing fields and a code of 1.
func (k keyCode) csi() []byte {
	b := []byte("\x1b[")
	if k.code != 1 || k.shifted != 0 || k.mods != 0 || k.text != 0 {
		b = strconv.AppendInt(b, int64(k.code), 10)
	}
	if k.shifted != 0 {
		b = append(b, ':')
		b = strconv.AppendInt(b, int64(k.shifted), 10)
	}
	if k.mods != 0 || k.text != 0 {
		b = append(b, ';')
	}
	if k.mods != 0 {
		b = strconv.AppendInt(b, int64(k.mods+1), 10)
	}
	if k.text != 0 {
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(k.text), 10)
	}
	return append(b, k.final)
}

// modifyOtherKeys encodes k the way xterm's modifyOtherKeys mode 2 does, except that, a
// ctrl key keeps its C0 byte so that a shell stays usable after a program exits without
// resetting the mode.
func (k keyCode) modifyOtherKeys() ([]byte, bool) {
	if k.mods == 0 {
		return nil, false
	}
	code := k.code
	switch k.key {
	case 0:
		if k.shifted != 0 {
			code = k.shifted
		}
		if c0, ok := ctrlByte(code); ok && k.mods == keyModCtrl {
			return []byte{c0}, true
		}
		// Like xterm, shift alone only modifies space and the
		// characters ctrl turns into control codes.
		if k.mods == keyModShift && code != ' ' && (code < 0x40 || code > 0x7f) {
			return nil, false
		}
	case term.KeyBackspace:
		if k.mods == keyModCtrl {
			return []byte{0x08}, true
		}
	case term.KeyEnter, term.KeyTab, term.KeyEsc:
	default:
		// Cursor, editing and function keys already carry their
		// modifiers in the legacy encoding.
		return nil, false
	}
	return fmt.Appendf(nil, "\x1b[27;%d;%d~", k.mods+1, code), true
}

// ctrlByte returns the C0 byte ctrl makes of ch, from kitty's
// ctrled_key, leaving out i, m and [: their C0 bytes are those of tab,
// enter and esc, which modifyOtherKeys exists to tell apart.
func ctrlByte(ch rune) (byte, bool) {
	switch {
	case ch == 'i' || ch == 'm':
		return 0, false
	case ch >= 'a' && ch <= 'z':
		return byte(ch-'a') + 1, true
	}
	switch ch {
	case ' ', '2', '@':
		return 0x00, true
	case '3':
		return 0x1b, true
	case '4', '\\':
		return 0x1c, true
	case '5', ']':
		return 0x1d, true
	case '6', '^', '~':
		return 0x1e, true
	case '7', '/', '_':
		return 0x1f, true
	case '8', '?':
		return 0x7f, true
	}
	return 0, false
}
