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

package exoeditor

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestKeyCombToEvent(t *testing.T) {
	cases := []struct {
		name string
		k    term.KeyComb
		raw  []byte
	}{
		// ----- printable runes, no modifier -----
		{name: "rune a", k: term.KeyComb{Ch: 'a'}, raw: []byte("a")},
		{name: "rune Z", k: term.KeyComb{Ch: 'Z'}, raw: []byte("Z")},
		{name: "rune 0", k: term.KeyComb{Ch: '0'}, raw: []byte("0")},
		{name: "rune 9", k: term.KeyComb{Ch: '9'}, raw: []byte("9")},
		{name: "rune colon", k: term.KeyComb{Ch: ':'}, raw: []byte(":")},
		{name: "rune pipe", k: term.KeyComb{Ch: '|'}, raw: []byte("|")},
		{name: "rune comma", k: term.KeyComb{Ch: ','}, raw: []byte(",")},
		{name: "rune slash", k: term.KeyComb{Ch: '/'}, raw: []byte("/")},
		{name: "rune dollar", k: term.KeyComb{Ch: '$'}, raw: []byte("$")},
		{name: "rune tilde", k: term.KeyComb{Ch: '~'}, raw: []byte("~")},
		// multi-byte rune
		{name: "rune é", k: term.KeyComb{Ch: 'é'}, raw: []byte("é")},
		{name: "rune emoji", k: term.KeyComb{Ch: '🚀'}, raw: []byte("🚀")},

		// ----- Shift-modified runes preserve the already-shifted glyph -----
		{name: "Shift+A", k: term.KeyComb{Ch: 'A', Mod: term.ModShift}, raw: []byte("A")},
		{name: "Shift+!", k: term.KeyComb{Ch: '!', Mod: term.ModShift}, raw: []byte("!")},

		// ----- Ctrl+letter (C0 codes) -----
		{name: "Ctrl+a", k: term.KeyComb{Ch: 'a', Mod: term.ModCtrl}, raw: []byte{0x01}},
		{name: "Ctrl+b", k: term.KeyComb{Ch: 'b', Mod: term.ModCtrl}, raw: []byte{0x02}},
		{name: "Ctrl+c", k: term.KeyComb{Ch: 'c', Mod: term.ModCtrl}, raw: []byte{0x03}},
		{name: "Ctrl+d", k: term.KeyComb{Ch: 'd', Mod: term.ModCtrl}, raw: []byte{0x04}},
		{name: "Ctrl+e", k: term.KeyComb{Ch: 'e', Mod: term.ModCtrl}, raw: []byte{0x05}},
		{name: "Ctrl+f", k: term.KeyComb{Ch: 'f', Mod: term.ModCtrl}, raw: []byte{0x06}},
		{name: "Ctrl+g", k: term.KeyComb{Ch: 'g', Mod: term.ModCtrl}, raw: []byte{0x07}},
		{name: "Ctrl+h", k: term.KeyComb{Ch: 'h', Mod: term.ModCtrl}, raw: []byte{0x08}},
		{name: "Ctrl+i", k: term.KeyComb{Ch: 'i', Mod: term.ModCtrl}, raw: []byte{0x09}},
		{name: "Ctrl+j", k: term.KeyComb{Ch: 'j', Mod: term.ModCtrl}, raw: []byte{0x0a}},
		{name: "Ctrl+k", k: term.KeyComb{Ch: 'k', Mod: term.ModCtrl}, raw: []byte{0x0b}},
		{name: "Ctrl+l", k: term.KeyComb{Ch: 'l', Mod: term.ModCtrl}, raw: []byte{0x0c}},
		{name: "Ctrl+m", k: term.KeyComb{Ch: 'm', Mod: term.ModCtrl}, raw: []byte{0x0d}},
		{name: "Ctrl+n", k: term.KeyComb{Ch: 'n', Mod: term.ModCtrl}, raw: []byte{0x0e}},
		{name: "Ctrl+o", k: term.KeyComb{Ch: 'o', Mod: term.ModCtrl}, raw: []byte{0x0f}},
		{name: "Ctrl+p", k: term.KeyComb{Ch: 'p', Mod: term.ModCtrl}, raw: []byte{0x10}},
		{name: "Ctrl+q", k: term.KeyComb{Ch: 'q', Mod: term.ModCtrl}, raw: []byte{0x11}},
		{name: "Ctrl+r", k: term.KeyComb{Ch: 'r', Mod: term.ModCtrl}, raw: []byte{0x12}},
		{name: "Ctrl+s", k: term.KeyComb{Ch: 's', Mod: term.ModCtrl}, raw: []byte{0x13}},
		{name: "Ctrl+t", k: term.KeyComb{Ch: 't', Mod: term.ModCtrl}, raw: []byte{0x14}},
		{name: "Ctrl+u", k: term.KeyComb{Ch: 'u', Mod: term.ModCtrl}, raw: []byte{0x15}},
		{name: "Ctrl+v", k: term.KeyComb{Ch: 'v', Mod: term.ModCtrl}, raw: []byte{0x16}},
		{name: "Ctrl+w", k: term.KeyComb{Ch: 'w', Mod: term.ModCtrl}, raw: []byte{0x17}},
		{name: "Ctrl+x", k: term.KeyComb{Ch: 'x', Mod: term.ModCtrl}, raw: []byte{0x18}},
		{name: "Ctrl+y", k: term.KeyComb{Ch: 'y', Mod: term.ModCtrl}, raw: []byte{0x19}},
		{name: "Ctrl+z", k: term.KeyComb{Ch: 'z', Mod: term.ModCtrl}, raw: []byte{0x1a}},

		// Upper-case letter behaves like lower-case under Ctrl.
		{name: "Ctrl+C upper", k: term.KeyComb{Ch: 'C', Mod: term.ModCtrl}, raw: []byte{0x03}},
		{name: "Ctrl+Z upper", k: term.KeyComb{Ch: 'Z', Mod: term.ModCtrl}, raw: []byte{0x1a}},

		// Ctrl+<punctuation> -> additional C0 codes.
		{name: "Ctrl+[", k: term.KeyComb{Ch: '[', Mod: term.ModCtrl}, raw: []byte{0x1b}},
		{name: "Ctrl+\\", k: term.KeyComb{Ch: '\\', Mod: term.ModCtrl}, raw: []byte{0x1c}},
		{name: "Ctrl+]", k: term.KeyComb{Ch: ']', Mod: term.ModCtrl}, raw: []byte{0x1d}},
		{name: "Ctrl+_", k: term.KeyComb{Ch: '_', Mod: term.ModCtrl}, raw: []byte{0x1f}},
		{name: "Ctrl+`", k: term.KeyComb{Ch: '`', Mod: term.ModCtrl}, raw: []byte{0x60}},

		// ----- Alt+<rune>: ESC prefix -----
		{name: "Alt+a", k: term.KeyComb{Ch: 'a', Mod: term.ModAlt}, raw: []byte{0x1b, 'a'}},
		{name: "Alt+x", k: term.KeyComb{Ch: 'x', Mod: term.ModAlt}, raw: []byte{0x1b, 'x'}},
		{name: "Alt+.", k: term.KeyComb{Ch: '.', Mod: term.ModAlt}, raw: []byte{0x1b, '.'}},

		// ----- Ctrl+Alt+letter: ESC + C0 -----
		{name: "CtrlAlt+a", k: term.KeyComb{Ch: 'a', Mod: term.ModCtrlAlt}, raw: []byte{0x1b, 0x01}},
		{name: "CtrlAlt+z", k: term.KeyComb{Ch: 'z', Mod: term.ModCtrlAlt}, raw: []byte{0x1b, 0x1a}},

		// ----- ESC / Enter / Tab / Backspace / Space -----
		{name: "Esc", k: term.KeyComb{Key: term.KeyEsc}, raw: []byte{0x1b}},
		{name: "Esc+Shift -> nil", k: term.KeyComb{Key: term.KeyEsc, Mod: term.ModShift}, raw: nil},
		{name: "Enter", k: term.KeyComb{Key: term.KeyEnter}, raw: []byte{0x0d, 0x0a}},
		{name: "Shift+Enter (kitty)", k: term.KeyComb{Key: term.KeyEnter, Mod: term.ModShift}, raw: []byte{0x1b, '[', '1', '3', ';', '2', 'u'}},
		{name: "Tab", k: term.KeyComb{Key: term.KeyTab}, raw: []byte{0x09}},
		{name: "Shift+Tab", k: term.KeyComb{Key: term.KeyTab, Mod: term.ModShift}, raw: []byte{0x1b, '[', 'Z'}},
		{name: "Backspace", k: term.KeyComb{Key: term.KeyBackspace}, raw: []byte{0x7f}},
		{name: "Alt+Backspace", k: term.KeyComb{Key: term.KeyBackspace, Mod: term.ModAlt}, raw: []byte{0x1b, 0x7f}},
		{name: "Space", k: term.KeyComb{Key: term.KeySpace}, raw: []byte{' '}},
		{name: "Shift+Space", k: term.KeyComb{Key: term.KeySpace, Mod: term.ModShift}, raw: []byte{' '}},

		// ----- Arrows (no mod) -----
		{name: "ArrowUp", k: term.KeyComb{Key: term.KeyArrowUp}, raw: []byte("\x1b[A")},
		{name: "ArrowDown", k: term.KeyComb{Key: term.KeyArrowDown}, raw: []byte("\x1b[B")},
		{name: "ArrowRight", k: term.KeyComb{Key: term.KeyArrowRight}, raw: []byte("\x1b[C")},
		{name: "ArrowLeft", k: term.KeyComb{Key: term.KeyArrowLeft}, raw: []byte("\x1b[D")},

		// ----- Arrows with modifiers -----
		{name: "Shift+ArrowUp", k: term.KeyComb{Key: term.KeyArrowUp, Mod: term.ModShift}, raw: []byte("\x1b[1;2A")},
		{name: "Alt+ArrowUp", k: term.KeyComb{Key: term.KeyArrowUp, Mod: term.ModAlt}, raw: []byte("\x1b[1;3A")},
		{name: "Ctrl+ArrowUp", k: term.KeyComb{Key: term.KeyArrowUp, Mod: term.ModCtrl}, raw: []byte("\x1b[1;5A")},
		{name: "CtrlShift+ArrowDown", k: term.KeyComb{Key: term.KeyArrowDown, Mod: term.ModCtrlShift}, raw: []byte("\x1b[1;6B")},
		{name: "CtrlAlt+ArrowRight", k: term.KeyComb{Key: term.KeyArrowRight, Mod: term.ModCtrlAlt}, raw: []byte("\x1b[1;7C")},

		// ----- Home / End -----
		{name: "Home", k: term.KeyComb{Key: term.KeyHome}, raw: []byte("\x1b[H")},
		{name: "End", k: term.KeyComb{Key: term.KeyEnd}, raw: []byte("\x1b[F")},
		{name: "Shift+Home", k: term.KeyComb{Key: term.KeyHome, Mod: term.ModShift}, raw: []byte("\x1b[1;2H")},

		// ----- PageUp / PageDown / Insert / Delete -----
		{name: "PgUp", k: term.KeyComb{Key: term.KeyPgup}, raw: []byte("\x1b[5~")},
		{name: "PgDn", k: term.KeyComb{Key: term.KeyPgdn}, raw: []byte("\x1b[6~")},
		{name: "Insert", k: term.KeyComb{Key: term.KeyInsert}, raw: []byte("\x1b[2~")},
		{name: "Delete", k: term.KeyComb{Key: term.KeyDelete}, raw: []byte("\x1b[3~")},
		{name: "Shift+PgUp", k: term.KeyComb{Key: term.KeyPgup, Mod: term.ModShift}, raw: []byte("\x1b[5;2~")},
		{name: "Ctrl+Delete", k: term.KeyComb{Key: term.KeyDelete, Mod: term.ModCtrl}, raw: []byte("\x1b[3;5~")},

		// ----- F1..F4 (SS3) -----
		{name: "F1", k: term.KeyComb{Key: term.KeyF1}, raw: []byte("\x1bOP")},
		{name: "F2", k: term.KeyComb{Key: term.KeyF2}, raw: []byte("\x1bOQ")},
		{name: "F3", k: term.KeyComb{Key: term.KeyF3}, raw: []byte("\x1bOR")},
		{name: "F4", k: term.KeyComb{Key: term.KeyF4}, raw: []byte("\x1bOS")},
		// ----- F1..F4 modified (CSI) -----
		{name: "Shift+F1", k: term.KeyComb{Key: term.KeyF1, Mod: term.ModShift}, raw: []byte("\x1b[1;2P")},
		{name: "Ctrl+F4", k: term.KeyComb{Key: term.KeyF4, Mod: term.ModCtrl}, raw: []byte("\x1b[1;5S")},
		// ----- F5..F12 -----
		{name: "F5", k: term.KeyComb{Key: term.KeyF5}, raw: []byte("\x1b[15~")},
		{name: "F6", k: term.KeyComb{Key: term.KeyF6}, raw: []byte("\x1b[17~")},
		{name: "F7", k: term.KeyComb{Key: term.KeyF7}, raw: []byte("\x1b[18~")},
		{name: "F8", k: term.KeyComb{Key: term.KeyF8}, raw: []byte("\x1b[19~")},
		{name: "F9", k: term.KeyComb{Key: term.KeyF9}, raw: []byte("\x1b[20~")},
		{name: "F10", k: term.KeyComb{Key: term.KeyF10}, raw: []byte("\x1b[21~")},
		{name: "F11", k: term.KeyComb{Key: term.KeyF11}, raw: []byte("\x1b[23~")},
		{name: "F12", k: term.KeyComb{Key: term.KeyF12}, raw: []byte("\x1b[24~")},
		{name: "Shift+F12", k: term.KeyComb{Key: term.KeyF12, Mod: term.ModShift}, raw: []byte("\x1b[24;2~")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := keyCombToEvent(tc.k)
			assert.Equal(t, term.EventKey, ev.Type)
			assert.Equal(t, tc.k.Mod, ev.Mod, "Mod must be preserved")
			assert.Equal(t, tc.k.Key, ev.Key, "Key must be preserved")
			assert.Equal(t, tc.k.Ch, ev.Ch, "Ch must be preserved")
			assert.Equal(t, tc.raw, ev.Raw,
				"Raw byte sequence for %s does not match the "+
					"vt100/xterm expectation",
				renderKeyComb(tc.k))
		})
	}
}

// renderKeyComb returns a short debugging description of k. Used in
// table-test failure messages so a Raw mismatch points at the exact
// modifier+key combination that regressed.
func renderKeyComb(k term.KeyComb) string {
	if k.Key != 0 {
		return fmt.Sprintf("KeyComb{Mod:%d, Key:%d}", k.Mod, k.Key)
	}
	return fmt.Sprintf("KeyComb{Mod:%d, Ch:%q}", k.Mod, k.Ch)
}

func TestKeyCombToEventCoversAllKeys(t *testing.T) {
	allKeys := []struct {
		name string
		key  term.Key
	}{
		{"F1", term.KeyF1}, {"F2", term.KeyF2}, {"F3", term.KeyF3},
		{"F4", term.KeyF4}, {"F5", term.KeyF5}, {"F6", term.KeyF6},
		{"F7", term.KeyF7}, {"F8", term.KeyF8}, {"F9", term.KeyF9},
		{"F10", term.KeyF10}, {"F11", term.KeyF11}, {"F12", term.KeyF12},
		{"Insert", term.KeyInsert}, {"Delete", term.KeyDelete},
		{"Home", term.KeyHome}, {"End", term.KeyEnd},
		{"PgUp", term.KeyPgup}, {"PgDn", term.KeyPgdn},
		{"ArrowUp", term.KeyArrowUp}, {"ArrowDown", term.KeyArrowDown},
		{"ArrowLeft", term.KeyArrowLeft}, {"ArrowRight", term.KeyArrowRight},
		{"Tab", term.KeyTab}, {"Enter", term.KeyEnter},
		{"Esc", term.KeyEsc}, {"Space", term.KeySpace},
		{"Backspace", term.KeyBackspace},
	}

	for _, tc := range allKeys {
		t.Run(tc.name, func(t *testing.T) {
			ev := keyCombToEvent(term.KeyComb{Key: tc.key})
			assert.NotEmpty(t, ev.Raw,
				"keyCombToEvent must emit a non-empty Raw byte "+
					"sequence for %s; a zero-byte pty write "+
					"silently drops the keystroke in the "+
					"external editor", tc.name)
		})
	}
}

func TestKeyCombToEventCoversCtrlAlpha(t *testing.T) {
	for r := 'a'; r <= 'z'; r++ {
		t.Run(fmt.Sprintf("Ctrl+%c", r), func(t *testing.T) {
			ev := keyCombToEvent(term.KeyComb{Ch: r, Mod: term.ModCtrl})
			require := assert.New(t)
			require.Len(ev.Raw, 1,
				"Ctrl+<letter> must collapse to a single C0 byte")
			require.Equal(byte(r-'a'+1), ev.Raw[0],
				"Ctrl+%c expected 0x%02x", r, r-'a'+1)
		})
	}
}
