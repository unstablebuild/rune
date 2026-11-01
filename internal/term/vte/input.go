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

	"github.com/unstablebuild/rune-go-sdk/term"
)

func getModifierStr(ev term.Event) string {
	switch ev.Mod {
	case term.ModAlt:
		return ";3"
	default:
		return ""
	}
}

// return an escape sequence for what requires state terminal, otherwise
// the raw bytes coming from the event are already correct
func mapKeyToEscapeSequence(comp *Component, ev term.Event) ([]byte, bool) {
	switch ev.Mod {
	case term.ModAlt:
		switch ev.Key {
		case term.KeyArrowLeft:
			return []byte("\x1bb"), true
		case term.KeyArrowRight:
			return []byte("\x1bf"), true
		}
		return nil, false
	case term.ModMeta:
		switch ev.Key {
		case term.KeyArrowLeft:
			return []byte{0x01}, true
		case term.KeyArrowRight:
			return []byte{0x05}, true
		}
		return nil, false
	case 0:
		switch ev.Key {
		case term.KeyArrowUp:
			if comp.IsApplicationCursorKeysMode() {
				return []byte{0x1b, 'O', 'A'}, true
			}
			return fmt.Appendf(nil, "\x1b[%sA", getModifierStr(ev)), true
		case term.KeyArrowDown:
			if comp.IsApplicationCursorKeysMode() {
				return []byte{0x1b, 'O', 'B'}, true
			}
			return fmt.Appendf(nil, "\x1b[%sB", getModifierStr(ev)), true
		case term.KeyArrowRight:
			if comp.IsApplicationCursorKeysMode() {
				return []byte{0x1b, 'O', 'C'}, true
			}
			return fmt.Appendf(nil, "\x1b[%sC", getModifierStr(ev)), true
		case term.KeyArrowLeft:
			if comp.IsApplicationCursorKeysMode() {
				return []byte{0x1b, 'O', 'D'}, true
			}
			return fmt.Appendf(nil, "\x1b[%sD", getModifierStr(ev)), true
		case term.KeyEnter:
			if ev.Mod == 0 {
				if comp.IsNewLineMode() {
					return []byte{0x0d, 0x0a}, true
				}
				return []byte{0x0d}, true
			}
			return nil, false
		case term.KeyHome:
			if comp.IsApplicationCursorKeysMode() {
				return fmt.Appendf(nil, "\x1b[1%s~", getModifierStr(ev)), true
			}
			return []byte("\x1b[H"), true
		default:
			return nil, false
		}
	default:
		return nil, false
	}
}
