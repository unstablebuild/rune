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
	"slices"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/gui/font"
)

// nextSource hands out the source IDs that tie a key action to the text the
// platform committed for it. Ebiten never reuses a source, so tests must not
// either.
var nextSource ebiten.InputSource

func keyEvent(key ebiten.Key, act ebiten.KeyAction, mods []ebiten.KeyModifier) ebiten.InputEvent {
	var m ebiten.KeyModifier
	for _, mod := range mods {
		m |= mod
	}
	nextSource++
	return ebiten.InputEvent{
		Kind:   ebiten.InputEventKindKey,
		Key:    key,
		Action: act,
		Mods:   m,
		Source: nextSource,
	}
}

func press(key ebiten.Key, mods ...ebiten.KeyModifier) ebiten.InputEvent {
	return keyEvent(key, ebiten.KeyActionPress, mods)
}

func repeat(key ebiten.Key, mods ...ebiten.KeyModifier) ebiten.InputEvent {
	return keyEvent(key, ebiten.KeyActionRepeat, mods)
}

func release(key ebiten.Key, mods ...ebiten.KeyModifier) ebiten.InputEvent {
	return keyEvent(key, ebiten.KeyActionRelease, mods)
}

// on stamps the characters the active keyboard layout produces for the key,
// as the platform reports them. The other helpers leave both at 0, which is
// what a platform with no layout data for the key reports.
func on(ev ebiten.InputEvent, char, shiftChar rune) ebiten.InputEvent {
	ev.Char, ev.ShiftChar = char, shiftChar
	return ev
}

func committed(key ebiten.InputEvent, normalText bool, runes []rune) []ebiten.InputEvent {
	events := make([]ebiten.InputEvent, 0, len(runes)+1)
	events = append(events, key)
	for _, r := range runes {
		events = append(events, ebiten.InputEvent{
			Kind:       ebiten.InputEventKindText,
			Mods:       key.Mods,
			Rune:       r,
			NormalText: normalText,
			Source:     key.Source,
		})
	}
	return events
}

// action returns the observations of one key action: the key transition
// followed by the code points the platform translated from it and classified
// as ordinary typed text.
func action(key ebiten.InputEvent, runes ...rune) []ebiten.InputEvent {
	return committed(key, true, runes)
}

// shortcut returns the observations of one key action whose committed code
// points the platform did not classify as ordinary typed text, as X11 reports
// anything typed while Ctrl or Alt is held.
func shortcut(key ebiten.InputEvent, runes ...rune) []ebiten.InputEvent {
	return committed(key, false, runes)
}

// text returns code points with no originating key action, as an input method
// commit or a paste delivers them.
func text(runes ...rune) []ebiten.InputEvent {
	events := make([]ebiten.InputEvent, 0, len(runes))
	for _, r := range runes {
		events = append(events, ebiten.InputEvent{
			Kind:       ebiten.InputEventKindText,
			Rune:       r,
			NormalText: true,
		})
	}
	return events
}

// commit returns only the code points a key action committed, as a platform
// that translates text late delivers them in a later update than the key
// transition itself.
func commit(key ebiten.InputEvent, runes ...rune) []ebiten.InputEvent {
	return committed(key, true, runes)[1:]
}

func TestInputFireOnce(t *testing.T) {
	suite := []struct {
		description    string
		events         []ebiten.InputEvent
		chars          []rune
		expectedEvents []term.Event
	}{
		{
			description:    "dispatches a single non-char key",
			events:         action(press(ebiten.KeyEnter)),
			expectedEvents: []term.Event{{Type: term.EventKey, Key: term.KeyEnter, Raw: []byte{0x0d, 0x0a}}},
		},
		{
			description: "plain printable key alone dispatches nothing (text comes from its commit)",
			events:      action(press(ebiten.KeyA)),
		},
		{
			description:    "dispatches a single key char, via committed text",
			chars:          []rune{'a'},
			expectedEvents: []term.Event{{Type: term.EventKey, Ch: 'a', Raw: []byte("a")}},
		},
		{
			description:    "dispatches a space key",
			events:         action(press(ebiten.KeySpace)),
			expectedEvents: []term.Event{{Type: term.EventKey, Key: term.KeySpace, Raw: []byte(" ")}},
		},
		{
			description:    "dispatches only one space when the space key also commits text",
			events:         action(press(ebiten.KeySpace), ' '),
			expectedEvents: []term.Event{{Type: term.EventKey, Key: term.KeySpace, Raw: []byte(" ")}},
		},
		{
			description:    "a space with no originating key action still inserts",
			chars:          []rune{' '},
			expectedEvents: []term.Event{{Type: term.EventKey, Ch: ' ', Raw: []byte(" ")}},
		},
		{
			description:    "dispatches a shift+space like a space key",
			events:         action(press(ebiten.KeySpace, ebiten.KeyModShift)),
			expectedEvents: []term.Event{{Type: term.EventKey, Key: term.KeySpace, Raw: []byte(" ")}},
		},
		{
			description:    "dispatches a meta+space as meta+space key",
			events:         action(press(ebiten.KeySpace, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModMeta, Key: term.KeySpace, Raw: []byte(" ")}},
		},
		{
			description:    "dispatches a single key ctrl + char",
			events:         action(press(ebiten.KeyA, ebiten.KeyModControl)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'a', Raw: []byte{0x01}}},
		},
		{
			description:    "dispatches a single key shift + ctrl + char",
			events:         action(press(ebiten.KeyA, ebiten.KeyModShift, ebiten.KeyModControl)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'A', Raw: []byte{0x01}}},
		},
		{
			description:    "dispatches a single key meta + char",
			events:         action(press(ebiten.KeyA, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModMeta, Ch: 'a'}},
		},
		{
			// Text with no originating key action carries no modifier: it is
			// an input method or paste commit, where modifiers are not
			// meaningful.
			description: "text with no key action carries no modifier",
			events:      nil,
			chars:       []rune{'a'},
			expectedEvents: []term.Event{
				{Type: term.EventKey, Ch: 'a', Raw: []byte("a")},
			},
		},
		{
			description:    "dispatches a single key alt + char, via key",
			events:         action(press(ebiten.KeyA, ebiten.KeyModAlt)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 'a', Raw: []byte{0x1b, 'a'}}},
		},
		{
			description:    "dispatches a single key alt + char, undoes macos special chars",
			events:         action(press(ebiten.KeyA, ebiten.KeyModAlt), 'å'),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 'a', Raw: []byte{0x1b, 'a'}}},
		},
		{
			description: "shift + printable key alone dispatches nothing (text comes from its commit)",
			events:      action(press(ebiten.KeyA, ebiten.KeyModShift)),
		},
		{
			description:    "shifted char committed by the platform dispatches the shifted rune",
			chars:          []rune{'A'},
			expectedEvents: []term.Event{{Type: term.EventKey, Ch: 'A', Raw: []byte("A")}},
		},
		{
			description: "dispatches a single key ctrl + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModControl)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrl,
				Key: term.KeyF1, Raw: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x35, 0x50}}},
		},
		{
			description: "dispatches a single key meta + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModMeta,
				Key: term.KeyF1, Raw: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x39, 0x50}}},
		},
		{
			description:    "dispatches a single key alt + char, via key",
			events:         action(press(ebiten.KeyA, ebiten.KeyModAlt)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModAlt, Ch: 'a', Raw: []byte{0x1b, 'a'}}},
		},
		{
			description: "unhandled key dispatches no event",
			events:      action(press(ebiten.KeyF24)),
		},
		{
			description: "dispatches a single key ctrl + alt + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModControl, ebiten.KeyModAlt)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrlAlt,
				Key: term.KeyF1, Raw: []byte("\x1b[1;7P")}},
		},
		{
			description: "dispatches a single key ctrl + meta + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModControl, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrlMeta,
				Key: term.KeyF1, Raw: []byte("\x1b[1;13P")}},
		},
		{
			description: "dispatches a single key ctrl + shift + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModControl, ebiten.KeyModShift)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrlShift,
				Key: term.KeyF1, Raw: []byte("\x1b[1;6P")}},
		},
		{
			description: "dispatches a single key ctrl + alt + shift + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModControl, ebiten.KeyModAlt, ebiten.KeyModShift)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrlShiftAlt,
				Key: term.KeyF1, Raw: []byte("\x1b[1;8P")}},
		},
		{
			description: "dispatches a single key ctrl + shift + meta + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModControl, ebiten.KeyModSuper, ebiten.KeyModShift)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrlShiftMeta,
				Key: term.KeyF1, Raw: []byte("\x1b[1;14P")}},
		},
		{
			description: "dispatches a single key ctrl + alt + meta + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModControl, ebiten.KeyModSuper, ebiten.KeyModAlt)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrlAltMeta,
				Key: term.KeyF1, Raw: []byte("\x1b[1;15P")}},
		},
		{
			description: "dispatches a single key ctrl + shift + alt + meta + key",
			events: action(press(ebiten.KeyArrowRight, ebiten.KeyModControl,
				ebiten.KeyModShift, ebiten.KeyModAlt, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey,
				Mod: term.ModCtrlShiftAlt | term.ModMeta,
				Key: term.KeyArrowRight, Raw: []byte("\x1b[1;16C")}},
		},
		{
			description: "dispatches a tilde key with ctrl + shift + alt + meta",
			events: action(press(ebiten.KeyDelete, ebiten.KeyModControl,
				ebiten.KeyModShift, ebiten.KeyModAlt, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey,
				Mod: term.ModCtrlShiftAlt | term.ModMeta,
				Key: term.KeyDelete, Raw: []byte("\x1b[3;16~")}},
		},
		{
			description: "dispatches a single key shift + meta + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModShift, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModShiftMeta,
				Key: term.KeyF1, Raw: []byte("\x1b[1;10P")}},
		},
		{
			description: "dispatches a single key alt + meta + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModAlt, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModAltMeta,
				Key: term.KeyF1, Raw: []byte("\x1b[1;11P")}},
		},
		{
			description: "dispatches a single key alt + shift + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModAlt, ebiten.KeyModShift)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModAltShift,
				Key: term.KeyF1, Raw: []byte("\x1b[1;4P")}},
		},
		{
			description: "dispatches a single key alt + shift + meta + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModAlt, ebiten.KeyModShift, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModAltShiftMeta,
				Key: term.KeyF1, Raw: []byte("\x1b[1;12P")}},
		},
		{
			description: "does not dispatch event with raw for meta + enter",
			events:      action(press(ebiten.KeyEnter, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModMeta, Key: term.KeyEnter},
			},
		},
		{
			description: "dispatches modifier ctrl + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModControl)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModCtrl,
				Key: term.KeyF1, Raw: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x35, 0x50}}},
		},
		{
			description: "dispatches modifier meta + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModMeta,
				Key: term.KeyF1, Raw: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x39, 0x50}}},
		},
		{
			description: "dispatches modifier alt + key",
			events:      action(press(ebiten.KeyF1, ebiten.KeyModAlt)),
			expectedEvents: []term.Event{{Type: term.EventKey, Mod: term.ModAlt,
				Key: term.KeyF1, Raw: []byte{0x1b, 0x5b, 0x31, 0x3b, 0x33, 0x50}}},
		},
		{
			description: "unhandled key dispatches no event (duplicate)",
			events:      action(press(ebiten.KeyF24)),
		},
		{
			description: "does not dispatch modifier ctrl + alt",
			events:      slices.Concat(action(press(ebiten.KeyControl)), action(press(ebiten.KeyAlt))),
		},
		{
			description: "does not dispatch modifier ctrl + meta",
			events:      slices.Concat(action(press(ebiten.KeyControl)), action(press(ebiten.KeyMeta))),
		},
		{
			description: "does not dispatch modifier ctrl + shift",
			events:      slices.Concat(action(press(ebiten.KeyControl)), action(press(ebiten.KeyShift))),
		},
		{
			description: "does not dispatch modifier ctrl + alt + shift",
			events:      slices.Concat(action(press(ebiten.KeyControl)), action(press(ebiten.KeyAlt)), action(press(ebiten.KeyShift))),
		},
		{
			description: "does not dispatch modifier ctrl + shift + meta",
			events:      slices.Concat(action(press(ebiten.KeyControl)), action(press(ebiten.KeyMeta)), action(press(ebiten.KeyShift))),
		},
		{
			description: "does not dispatch modifier ctrl + alt + meta",
			events:      slices.Concat(action(press(ebiten.KeyControl)), action(press(ebiten.KeyMeta)), action(press(ebiten.KeyAlt))),
		},
		{
			description: "does not dispatch modifier shift + meta",
			events:      slices.Concat(action(press(ebiten.KeyShift)), action(press(ebiten.KeyMeta))),
		},
		{
			description: "does not dispatch modifier alt + meta",
			events:      slices.Concat(action(press(ebiten.KeyAlt)), action(press(ebiten.KeyMeta))),
		},
		{
			description: "does not dispatch modifier alt + shift",
			events:      slices.Concat(action(press(ebiten.KeyAlt)), action(press(ebiten.KeyShift))),
		},
		{
			description: "does not dispatch modifier alt + shift + meta",
			events:      slices.Concat(action(press(ebiten.KeyAlt)), action(press(ebiten.KeyShift)), action(press(ebiten.KeyMeta))),
		},
		{
			description: "dispatches non-alt modifier with arrow key with raw set",
			events:      action(press(ebiten.KeyArrowUp, ebiten.KeyModShift, ebiten.KeyModSuper)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Key: term.KeyArrowUp, Mod: term.ModShiftMeta,
					Raw: []byte("\x1b[1;10A")},
			},
		},
		{
			description: "dispatches arrow key with raw set",
			events:      action(press(ebiten.KeyArrowUp)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Key: term.KeyArrowUp,
					Raw: []byte("\x1b[A")},
			},
		},
		{
			description: "dispatches alt arrow key with raw set",
			events:      action(press(ebiten.KeyArrowUp, ebiten.KeyModAlt)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Key: term.KeyArrowUp,
					Raw: []byte("\x1b[1;3A")},
			},
		},
		{
			description: "shift+2 key alone dispatches nothing (@ comes from chars)",
			events:      action(press(ebiten.KeyDigit2, ebiten.KeyModShift)),
		},
		{
			description:    "shifted symbol committed by the platform dispatches the symbol",
			chars:          []rune{'@'},
			expectedEvents: []term.Event{{Type: term.EventKey, Ch: '@', Raw: []byte("@")}},
		},
		{
			description: "OS repeat of a printable key alone dispatches nothing",
			events:      action(repeat(ebiten.KeyA)),
		},
		{
			description: "does not dispatch releases",
			events:      action(release(ebiten.KeyA)),
		},
		{
			// AZERTY: the key labelled M sits where the US keyboard has ';'.
			description: "alt chord is named by the layout, not the US key position",
			events:      action(on(press(ebiten.KeySemicolon, ebiten.KeyModAlt), 'm', 'M')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: 'm', Raw: []byte{0x1b, 'm'}},
			},
		},
		{
			// AZERTY: the key labelled A sits where the US keyboard has 'q'.
			description: "alt chord on a swapped letter follows the layout",
			events:      action(on(press(ebiten.KeyQ, ebiten.KeyModAlt), 'a', 'A')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: 'a', Raw: []byte{0x1b, 'a'}},
			},
		},
		{
			// Colemak: P sits where the US keyboard has 'r'.
			description: "ctrl+shift chord takes the shifted level from the layout",
			events: action(on(press(ebiten.KeyR,
				ebiten.KeyModControl, ebiten.KeyModShift), 'p', 'P')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'P', Raw: []byte{0x10}},
			},
		},
		{
			// Colemak: ';' sits where the US keyboard has 'p'.
			description: "ctrl+shift chord on a layout symbol takes its shifted level",
			events: action(on(press(ebiten.KeyP,
				ebiten.KeyModControl, ebiten.KeyModShift), ';', ':')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: ':'},
			},
		},
		{
			// Nordic: '+' sits where the US keyboard has '-'.
			description: "meta chord is named by the layout symbol",
			events:      action(on(press(ebiten.KeyMinus, ebiten.KeyModSuper), '+', '?')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModMeta, Ch: '+'},
			},
		},
		{
			// Nordic: '-' sits where the US keyboard has '/'.
			description: "meta chord on the layout's minus key is not the US slash",
			events:      action(on(press(ebiten.KeySlash, ebiten.KeyModSuper), '-', '_')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModMeta, Ch: '-'},
			},
		},
		{
			// Nordic: '/' is Shift+7, so <alt-/> is reached as Alt+Shift+7.
			description: "alt+shift chord on a digit takes the layout's shifted symbol",
			events: action(on(press(ebiten.KeyDigit7,
				ebiten.KeyModAlt, ebiten.KeyModShift), '7', '/')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: '/', Raw: []byte{0x1b, '/'}},
			},
		},
		{
			// Nordic: '¨' sits where the US keyboard has ']'.
			description: "chord on a non-ASCII layout character dispatches that character",
			events:      action(on(press(ebiten.KeyBracketRight, ebiten.KeyModAlt), '¨', 0)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: '¨',
					Raw: append([]byte{0x1b}, "¨"...)},
			},
		},
		{
			// Nordic: '=' is Shift+0, not Shift+'='.
			description: "shift+meta chord takes the layout's shifted level",
			events: action(on(press(ebiten.KeyDigit0,
				ebiten.KeyModSuper, ebiten.KeyModShift), '0', '=')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModMeta, Ch: '='},
			},
		},
		{
			// Dvorak: X sits where the US keyboard has 'b'.
			description: "alt chord on a dvorak letter follows the layout",
			events:      action(on(press(ebiten.KeyB, ebiten.KeyModAlt), 'x', 'X')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: 'x', Raw: []byte{0x1b, 'x'}},
			},
		},
		{
			description: "ctrl control byte follows the layout letter",
			events:      action(on(press(ebiten.KeySemicolon, ebiten.KeyModControl), 'm', 'M')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'm', Raw: []byte{0x0d}},
			},
		},
		{
			description: "the layout character wins over the key enum",
			events:      action(on(press(ebiten.KeyA, ebiten.KeyModControl), 'q', 'Q')),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'q', Raw: []byte{0x11}},
			},
		},
		{
			description: "a printable key with no layout character falls back to the key enum",
			events:      action(press(ebiten.KeySemicolon, ebiten.KeyModAlt)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: ';', Raw: []byte{0x1b, ';'}},
			},
		},
		{
			description: "a key with no shifted level folds Shift onto its only character",
			events: action(on(press(ebiten.KeyNumpad1,
				ebiten.KeyModControl, ebiten.KeyModShift), '1', 0)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: '1'},
			},
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			mock, input := newTestInput(t)
			mock.events = slices.Concat(test.events, text(test.chars...))

			events := input.processEvents(nil)
			if len(test.expectedEvents) == 0 {
				assert.Empty(t, events)
				return
			}
			require.Equal(t, len(test.expectedEvents), len(events))
			assert.Equal(t, test.expectedEvents, events)
		})
	}
}

// frame represents one frame of input for multi-frame tests. events holds the
// ordered observations of key actions and the text they committed; chars holds
// text the platform could not attribute to any key action.
type frame struct {
	events         []ebiten.InputEvent
	chars          []rune
	expectedEvents []term.Event
}

func TestInputMultiFrame(t *testing.T) {
	suite := []struct {
		description string
		frames      []frame
	}{
		{
			description: "multiple unhandled key dispatches no events",
			frames: []frame{
				{events: action(press(ebiten.KeyF24))},
				{events: action(press(ebiten.KeyF24))},
			},
		},
		{
			description: "press dispatches once, held key with no repeat produces nothing",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyEnter)),
					expectedEvents: []term.Event{{Key: term.KeyEnter}},
				},
				{
					// Key held, no OS repeat yet → no events
				},
			},
		},
		{
			description: "different key on next frame dispatches new event",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyEnter)),
					expectedEvents: []term.Event{{Key: term.KeyEnter}},
				},
				{
					// Key held, no OS repeat yet
				},
				{
					events:         action(press(ebiten.KeySpace)),
					expectedEvents: []term.Event{{Key: term.KeySpace}},
				},
			},
		},
		{
			description: "char key press alone dispatches nothing; text arrives with the commit",
			frames: []frame{
				{
					// Printable key event only; the char commit lands next frame.
					events: action(press(ebiten.KeyA)),
				},
				{
					chars:          []rune{'a'},
					expectedEvents: []term.Event{{Ch: 'a'}},
				},
			},
		},
		{
			description: "committed text dispatches one event per frame in order",
			frames: []frame{
				{chars: []rune{'a'}, expectedEvents: []term.Event{{Ch: 'a'}}},
				{chars: []rune{'b'}, expectedEvents: []term.Event{{Ch: 'b'}}},
				{chars: []rune{'a'}, expectedEvents: []term.Event{{Ch: 'a'}}},
				{chars: []rune{'b'}, expectedEvents: []term.Event{{Ch: 'b'}}},
			},
		},
		{
			description: "ctrl + char dispatches once, held produces nothing",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA, ebiten.KeyModControl)),
					expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'a'}},
				},
				{
					// Key held, no OS repeat yet
				},
			},
		},
		{
			description: "alternating modifier-only events produce nothing",
			frames: []frame{
				{events: action(press(ebiten.KeyControl))},
				{events: action(press(ebiten.KeyMeta))},
				{events: action(press(ebiten.KeyControl))},
				{events: action(press(ebiten.KeyMeta))},
			},
		},
		{
			description: "different ctrl+char dispatches a new event",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA, ebiten.KeyModControl)),
					expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'a'}},
				},
				{
					// Key held
				},
				{
					events:         action(press(ebiten.KeyB, ebiten.KeyModControl)),
					expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'b'}},
				},
			},
		},
		{
			description: "shift + ctrl + char dispatches once, held produces nothing",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA, ebiten.KeyModShift, ebiten.KeyModControl)),
					expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'A'}},
				},
				{
					// Key held
				},
			},
		},
		{
			description: "different shift+ctrl+char dispatches a new event",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA, ebiten.KeyModShift, ebiten.KeyModControl)),
					expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'A'}},
				},
				{
					// Key held
				},
				{
					events:         action(press(ebiten.KeyB, ebiten.KeyModShift, ebiten.KeyModControl)),
					expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'B'}},
				},
			},
		},
		{
			description: "alt + char via key on both frames",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA, ebiten.KeyModAlt)),
					expectedEvents: []term.Event{{Mod: term.ModAlt, Ch: 'a'}},
				},
				{
					// Key held
				},
			},
		},
		{
			description: "alt + shift + char via key on both frames",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA, ebiten.KeyModAlt, ebiten.KeyModShift)),
					expectedEvents: []term.Event{{Mod: term.ModAlt, Ch: 'A'}},
				},
				{
					// Key held
				},
			},
		},
		{
			description: "joining chord keys dispatches only the new chord each frame",
			frames: []frame{
				{
					// Shift only → no event
					events: action(press(ebiten.KeyShift)),
				},
				{
					// Shift still held
				},
				{
					// Shift+A is plain printable text → delivered via chars, not
					// the key path.
					events: action(press(ebiten.KeyA, ebiten.KeyModShift)),
				},
				{
					// Ctrl added, B still held with shift+ctrl
					// (no new press event from OS, just modifier change)
				},
				{
					// B held with shift+ctrl, no repeat
				},
				{
					// C pressed with shift+ctrl
					events:         action(press(ebiten.KeyC, ebiten.KeyModShift, ebiten.KeyModControl)),
					expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'C'}},
				},
				{
					// Modifiers held, no char keys
				},
				{
					// All released
				},
			},
		},
		{
			// Rune does not claim a plain printable key, so the text that key
			// action committed is the only dispatch.
			description: "printable key with same-frame commit dispatches the text only",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA), 'a'),
					expectedEvents: []term.Event{{Ch: 'a'}},
				},
			},
		},
		{
			// Under IBus the key transition is reported with no text, and the
			// commit lands a frame later carrying the same source.
			description: "IBus delayed commit dispatches once, key path stays silent",
			frames: []frame{
				{
					events: action(press(ebiten.KeyA)),
				},
				{
					chars:          []rune{'a'}, // delayed IBus commit
					expectedEvents: []term.Event{{Ch: 'a'}},
				},
			},
		},
		{
			// Each OS repeat is a distinct key action with its own commit.
			description: "held printable key repeats emit their own committed text",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA), 'a'),
					expectedEvents: []term.Event{{Ch: 'a'}},
				},
				{
					events:         action(repeat(ebiten.KeyA), 'a'),
					expectedEvents: []term.Event{{Ch: 'a'}},
				},
				{
					// Repeat with no accompanying commit → nothing.
					events: action(repeat(ebiten.KeyA)),
				},
				{
					events: action(release(ebiten.KeyA)),
				},
			},
		},
		{
			// Interleaving a second key while the first is held: each action's
			// text dispatches in native order.
			description: "interleaving a second held key dispatches text in order",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA), 'a'),
					expectedEvents: []term.Event{{Ch: 'a'}},
				},
				{
					events:         action(press(ebiten.KeyB), 'b'),
					expectedEvents: []term.Event{{Ch: 'b'}},
				},
				{
					events: action(release(ebiten.KeyB)),
				},
				{
					events: action(release(ebiten.KeyA)),
				},
			},
		},
		{
			// A composed character (dead key, CJK) is delivered verbatim.
			description: "composed IME char passes through untouched",
			frames: []frame{
				{
					events: action(press(ebiten.KeyA)),
				},
				{
					chars:          []rune{'é'},
					expectedEvents: []term.Event{{Ch: 'é'}},
				},
			},
		},
		{
			// Alt+printable is a chord; macOS also commits the Option-composed
			// rune for the same action, which must not double the key press.
			description: "macOS Alt chord drops the text its own action committed",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyA, ebiten.KeyModAlt), 'å'),
					expectedEvents: []term.Event{{Mod: term.ModAlt, Ch: 'a'}},
				},
			},
		},
		{
			// X11 classifies anything typed with Ctrl or Alt held as not being
			// normal text, so a Ctrl+Alt action that commits a code point is a
			// chord and its commit is that chord's echo.
			description: "Ctrl+Alt chord drops the text its own action committed",
			frames: []frame{
				{
					events: shortcut(
						press(ebiten.KeyPeriod, ebiten.KeyModControl, ebiten.KeyModAlt), '.'),
					expectedEvents: []term.Event{{Mod: term.ModCtrlAlt, Ch: '.'}},
				},
			},
		},
		{
			// Windows reports AltGr as Ctrl+Alt but classifies what the layout
			// produces as normal text. The mask alone must not turn an
			// international layout character into a chord.
			description: "AltGr layout text is inserted rather than treated as a Ctrl+Alt chord",
			frames: []frame{
				{
					events: action(
						press(ebiten.KeyE, ebiten.KeyModControl, ebiten.KeyModAlt), '€'),
					expectedEvents: []term.Event{{Ch: '€'}},
				},
			},
		},
		{
			// A Ctrl+Alt action that commits nothing is unambiguously a chord.
			description: "Ctrl+Alt with no committed text dispatches the chord",
			frames: []frame{
				{
					events:         action(press(ebiten.KeyPeriod, ebiten.KeyModControl, ebiten.KeyModAlt)),
					expectedEvents: []term.Event{{Mod: term.ModCtrlAlt, Ch: '.'}},
				},
			},
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			mock, input := newTestInput(t)
			for fi, f := range test.frames {
				mock.events = slices.Concat(f.events, text(f.chars...))

				events := input.processEvents(nil)
				if len(f.expectedEvents) == 0 {
					assert.Empty(t, events, "frame %d", fi)
				} else {
					require.Equal(t, len(f.expectedEvents), len(events), "frame %d", fi)
					for ei, expected := range f.expectedEvents {
						actual := events[ei]
						// Strip Raw and Type for multi-frame tests;
						// those are covered in TestInputFireOnce.
						actual.Raw = nil
						actual.Type = 0
						assert.Equal(t, expected, actual, "frame %d event %d", fi, ei)
					}
				}
			}
		})
	}
}

// TestInputSourceOwnership covers the ownership rule that ties a key action to
// the text the platform translated from it: whatever Rune turns into a
// terminal event owns exactly the code points carrying its own source, and
// nothing else.
func TestInputSourceOwnership(t *testing.T) {
	metaL := press(ebiten.KeyL, ebiten.KeyModSuper)
	altA := press(ebiten.KeyA, ebiten.KeyModAlt)
	repeatedA := repeat(ebiten.KeyA, ebiten.KeyModSuper)

	suite := []struct {
		description string
		mapping     map[term.KeyComb]term.KeyComb
		frames      []frame
	}{
		{
			// The Linux/X11 regression: Super+L reports a key transition and a
			// plain 'l' commit for the same action. Only the chord may survive.
			description: "meta chord consumes the text its own action committed",
			frames: []frame{{
				events:         action(metaL, 'l'),
				expectedEvents: []term.Event{{Mod: term.ModMeta, Ch: 'l'}},
			}},
		},
		{
			description: "ctrl chord consumes the text its own action committed",
			frames: []frame{{
				events:         action(press(ebiten.KeyL, ebiten.KeyModControl), 'l'),
				expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'l'}},
			}},
		},
		{
			description: "alt chord consumes the text its own action committed",
			frames: []frame{{
				events:         action(press(ebiten.KeyL, ebiten.KeyModAlt), 'l'),
				expectedEvents: []term.Event{{Mod: term.ModAlt, Ch: 'l'}},
			}},
		},
		{
			description: "ctrl+shift chord consumes the text its own action committed",
			frames: []frame{{
				events: action(press(ebiten.KeyL,
					ebiten.KeyModControl, ebiten.KeyModShift), 'L'),
				expectedEvents: []term.Event{{Mod: term.ModCtrl, Ch: 'L'}},
			}},
		},
		{
			description: "ctrl+alt+meta chord consumes the text its own action committed",
			frames: []frame{{
				events: action(press(ebiten.KeyL,
					ebiten.KeyModControl, ebiten.KeyModAlt, ebiten.KeyModSuper), 'l'),
				expectedEvents: []term.Event{{Mod: term.ModCtrlAltMeta, Ch: 'l'}},
			}},
		},
		{
			description: "ctrl+shift+alt+meta chord consumes the text its own action committed",
			frames: []frame{{
				events: action(press(ebiten.KeyL, ebiten.KeyModControl,
					ebiten.KeyModShift, ebiten.KeyModAlt, ebiten.KeyModSuper), 'L'),
				expectedEvents: []term.Event{
					{Mod: term.ModCtrlAltMeta, Ch: 'L'},
				},
			}},
		},
		{
			description: "a chord preserves unrelated text that follows it",
			frames: []frame{{
				events: slices.Concat(action(metaL, 'l'), text('x')),
				expectedEvents: []term.Event{
					{Mod: term.ModMeta, Ch: 'l'},
					{Ch: 'x'},
				},
			}},
		},
		{
			description: "a chord preserves unrelated text that precedes it",
			frames: []frame{{
				events: slices.Concat(text('x'), action(metaL, 'l')),
				expectedEvents: []term.Event{
					{Ch: 'x'},
					{Mod: term.ModMeta, Ch: 'l'},
				},
			}},
		},
		{
			// The old frame-global Alt suppression lost every code point in an
			// update that contained one Alt chord.
			description: "an alt chord does not swallow another action's text",
			frames: []frame{{
				events: slices.Concat(
					action(altA, 'å'),
					action(press(ebiten.KeyB), 'b'),
				),
				expectedEvents: []term.Event{
					{Mod: term.ModAlt, Ch: 'a'},
					{Ch: 'b'},
				},
			}},
		},
		{
			description: "a named key consumes only the text of its own action",
			frames: []frame{{
				events: slices.Concat(action(press(ebiten.KeySpace), ' '), text(' ')),
				expectedEvents: []term.Event{
					{Key: term.KeySpace},
					{Ch: ' '},
				},
			}},
		},
		{
			// macOS composes Option chords into text. On AZERTY the key
			// labelled M sits on the US semicolon and Option+M commits 'µ',
			// which is the chord's own echo and must not reach the terminal.
			description: "an alt chord named by the layout still consumes its own echo",
			frames: []frame{{
				events: action(on(press(ebiten.KeySemicolon, ebiten.KeyModAlt), 'm', 'M'), 'µ'),
				expectedEvents: []term.Event{
					{Mod: term.ModAlt, Ch: 'm'},
				},
			}},
		},
		{
			description: "press and repeat each emit once and consume their own text",
			frames: []frame{
				{
					events:         action(metaL, 'l'),
					expectedEvents: []term.Event{{Mod: term.ModMeta, Ch: 'l'}},
				},
				{
					events:         action(repeatedA, 'a'),
					expectedEvents: []term.Event{{Mod: term.ModMeta, Ch: 'a'}},
				},
			},
		},
		{
			description: "text delivered after its key action is still consumed by source",
			frames: []frame{
				{
					events:         action(metaL),
					expectedEvents: []term.Event{{Mod: term.ModMeta, Ch: 'l'}},
				},
				{
					events: commit(metaL, 'l'),
				},
				{
					events:         text('l'),
					expectedEvents: []term.Event{{Ch: 'l'}},
				},
			},
		},
		{
			description: "a printable key mapped to a named key consumes only its own text",
			mapping: map[term.KeyComb]term.KeyComb{
				{Ch: 'a'}: {Key: term.KeyEsc},
			},
			frames: []frame{{
				events: slices.Concat(action(press(ebiten.KeyA), 'a'), text('b')),
				expectedEvents: []term.Event{
					{Key: term.KeyEsc},
					{Ch: 'b'},
				},
			}},
		},
		{
			description: "a printable key mapped to another printable consumes only its own text",
			mapping: map[term.KeyComb]term.KeyComb{
				{Ch: 'a'}: {Ch: 'b'},
			},
			frames: []frame{{
				events: slices.Concat(action(press(ebiten.KeyA), 'a'), text('c')),
				expectedEvents: []term.Event{
					{Ch: 'b'},
					{Ch: 'c'},
				},
			}},
		},
		{
			description: "a bare modifier remap owns the text of the action it rewrites",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl}: {Mod: term.ModMeta},
			},
			frames: []frame{{
				events: slices.Concat(
					action(press(ebiten.KeyL, ebiten.KeyModControl), 'l'),
					text('x'),
				),
				expectedEvents: []term.Event{
					{Mod: term.ModMeta, Ch: 'l'},
					{Ch: 'x'},
				},
			}},
		},
		{
			description: "dead key, multi-rune IME and non-ASCII commits survive verbatim",
			frames: []frame{{
				events: text('é', '漢', '字', 'ñ', '€'),
				expectedEvents: []term.Event{
					{Ch: 'é'}, {Ch: '漢'}, {Ch: '字'}, {Ch: 'ñ'}, {Ch: '€'},
				},
			}},
		},
		{
			description: "an emoji sequence survives a chord in the same update",
			frames: []frame{{
				events: slices.Concat(
					action(metaL, 'l'),
					text('\U0001F468', '\u200d', '\U0001F469'),
				),
				expectedEvents: []term.Event{
					{Mod: term.ModMeta, Ch: 'l'},
					{Ch: '\U0001F468'}, {Ch: '\u200d'}, {Ch: '\U0001F469'},
				},
			}},
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			mock, input := newTestInput(t)
			input.setKeyMapping(test.mapping)
			for fi, f := range test.frames {
				mock.events = slices.Concat(f.events, text(f.chars...))

				events := input.processEvents(nil)
				require.Equal(t, len(f.expectedEvents), len(events), "frame %d", fi)
				for ei, expected := range f.expectedEvents {
					actual := events[ei]
					actual.Raw = nil
					actual.Type = 0
					assert.Equal(t, expected, actual, "frame %d event %d", fi, ei)
				}
			}
		})
	}
}

func newTestInput(t *testing.T) (*mockInputManager, *input) {
	mock := &mockInputManager{}
	f, err := font.NewManager(1, 1)
	require.NoError(t, err)
	f.SetFontByFamilyName("")
	f.SetDeviceScale(1)
	ret := newInput(f)
	ret.input = mock
	return mock, ret
}

type mockInputManager struct {
	events []ebiten.InputEvent
}

func (m *mockInputManager) AppendInputEvents(buf []ebiten.InputEvent) []ebiten.InputEvent {
	return append(buf, m.events...)
}

func TestInputKeyMapping(t *testing.T) {
	suite := []struct {
		description    string
		mapping        map[term.KeyComb]term.KeyComb
		events         []ebiten.InputEvent
		chars          []rune
		expectedEvents []term.Event
	}{
		{
			description: "remaps CapsLock to Esc",
			mapping: map[term.KeyComb]term.KeyComb{
				{Key: term.KeyCapsLock}: {Key: term.KeyEsc},
			},
			events: action(press(ebiten.KeyCapsLock)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Key: term.KeyEsc, Raw: []byte{0x1b}},
			},
		},
		{
			description: "unmapped CapsLock dispatches no event",
			mapping: map[term.KeyComb]term.KeyComb{
				{Key: term.KeyNumLock}: {Key: term.KeyEsc},
			},
			events: action(press(ebiten.KeyCapsLock)),
		},
		{
			description:    "CapsLock with no mapping table dispatches no event",
			events:         action(press(ebiten.KeyCapsLock)),
			expectedEvents: nil,
		},
		{
			description: "remaps NumLock to a char target",
			mapping: map[term.KeyComb]term.KeyComb{
				{Key: term.KeyNumLock}: {Ch: 'a'},
			},
			events: action(press(ebiten.KeyNumLock)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Ch: 'a', Raw: []byte("a")},
			},
		},
		{
			description: "remaps ContextMenu (menu) to Esc",
			mapping: map[term.KeyComb]term.KeyComb{
				{Key: term.KeyMenu}: {Key: term.KeyEsc},
			},
			events: action(press(ebiten.KeyContextMenu)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Key: term.KeyEsc, Raw: []byte{0x1b}},
			},
		},
		{
			description: "remaps a char source to a named key",
			mapping: map[term.KeyComb]term.KeyComb{
				{Ch: 'a'}: {Key: term.KeyEsc},
			},
			events: action(press(ebiten.KeyA)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Key: term.KeyEsc, Raw: []byte{0x1b}},
			},
		},
		{
			description: "remaps a named key source to a char",
			mapping: map[term.KeyComb]term.KeyComb{
				{Key: term.KeyEsc}: {Ch: 'a'},
			},
			events: action(press(ebiten.KeyEscape)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Ch: 'a', Raw: []byte("a")},
			},
		},
		{
			// A key remapped to a printable target commits no text of its own,
			// so it must be emitted from the key path even though plain
			// printable keys are otherwise skipped. Text from another source
			// still flows through independently.
			description: "remapped key-to-char emits alongside independent text",
			mapping: map[term.KeyComb]term.KeyComb{
				{Key: term.KeyNumLock}: {Ch: 'a'},
			},
			events: action(press(ebiten.KeyNumLock)),
			chars:  []rune{'b'},
			expectedEvents: []term.Event{
				{Type: term.EventKey, Ch: 'a', Raw: []byte("a")},
				{Type: term.EventKey, Ch: 'b', Raw: []byte("b")},
			},
		},
		{
			description: "passes through keys absent from a non-empty table",
			mapping: map[term.KeyComb]term.KeyComb{
				{Key: term.KeyCapsLock}: {Key: term.KeyEsc},
			},
			events: action(press(ebiten.KeyEnter)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Key: term.KeyEnter, Raw: []byte{0x0d, 0x0a}},
			},
		},
		{
			description: "remap matches modifier in source",
			mapping: map[term.KeyComb]term.KeyComb{
				{Key: term.KeyArrowLeft, Mod: term.ModCtrl}: {Key: term.KeyHome},
			},
			events: action(press(ebiten.KeyArrowLeft, ebiten.KeyModControl)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Key: term.KeyHome, Raw: []byte("\x1b[H")},
			},
		},
		{
			description: "remaps ctrl-a to meta-a",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl, Ch: 'a'}: {Mod: term.ModMeta, Ch: 'a'},
			},
			events: action(press(ebiten.KeyA, ebiten.KeyModControl)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModMeta, Ch: 'a'},
			},
		},
		{
			description: "ctrl-b passes through when only ctrl-a is remapped",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl, Ch: 'a'}: {Mod: term.ModMeta, Ch: 'a'},
			},
			events: action(press(ebiten.KeyB, ebiten.KeyModControl)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'b', Raw: []byte{0x02}},
			},
		},
		{
			description: "ctrl-a to meta-a leaves other modifier+char combos untouched",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl, Ch: 'a'}: {Mod: term.ModMeta, Ch: 'a'},
			},
			events: slices.Concat(action(press(ebiten.KeyB, ebiten.KeyModControl)), action(press(ebiten.KeyB, ebiten.KeyModSuper)), action(press(ebiten.KeyB, ebiten.KeyModAlt))),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'b', Raw: getCharEscapeSequence('b', term.ModCtrl)},
				{Type: term.EventKey, Mod: term.ModMeta, Ch: 'b', Raw: getCharEscapeSequence('b', term.ModMeta)},
				{Type: term.EventKey, Mod: term.ModAlt, Ch: 'b', Raw: getCharEscapeSequence('b', term.ModAlt)},
			},
		},
		{
			description: "ctrl-a to alt-a with ctrl to meta remaps ctrl-a to alt-a and ctrl-b to meta-b",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl, Ch: 'a'}: {Mod: term.ModAlt, Ch: 'a'},
				{Mod: term.ModCtrl}:          {Mod: term.ModMeta},
			},
			events: slices.Concat(action(press(ebiten.KeyA, ebiten.KeyModControl)), action(press(ebiten.KeyB, ebiten.KeyModControl))),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: 'a', Raw: getCharEscapeSequence('a', term.ModAlt)},
				{Type: term.EventKey, Mod: term.ModMeta, Ch: 'b', Raw: getCharEscapeSequence('b', term.ModMeta)},
			},
		},
		{
			description: "remaps bare ctrl to meta and emits nothing",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl}: {Mod: term.ModMeta},
			},
			events:         action(press(ebiten.KeyControl)),
			expectedEvents: nil,
		},
		{
			description: "remaps bare ctrl to meta with self-bit set and emits nothing",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl}: {Mod: term.ModMeta},
			},
			events:         action(press(ebiten.KeyControl, ebiten.KeyModControl)),
			expectedEvents: nil,
		},
		{
			description: "remaps bare ctrl to esc",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl}: {Key: term.KeyEsc},
			},
			events: action(press(ebiten.KeyControl, ebiten.KeyModControl)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Key: term.KeyEsc, Raw: []byte{0x1b}},
			},
		},
		{
			description: "remaps shift-ctrl-a to shift-meta-a",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl, Ch: 'A'}: {Mod: term.ModMeta, Ch: 'A'},
			},
			events: action(press(ebiten.KeyA, ebiten.KeyModControl, ebiten.KeyModShift)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModMeta, Ch: 'A', Raw: getCharEscapeSequence('A', term.ModMeta)},
			},
		},
		{
			description: "shift-ctrl-b passes through when only shift-ctrl-a is remapped",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl, Ch: 'A'}: {Mod: term.ModMeta, Ch: 'A'},
			},
			events: action(press(ebiten.KeyB, ebiten.KeyModControl, ebiten.KeyModShift)),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'B', Raw: getCharEscapeSequence('B', term.ModCtrl)},
			},
		},
		{
			description: "shift-ctrl-a to shift-meta-a leaves other shifted modifier+char combos untouched",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl, Ch: 'A'}: {Mod: term.ModMeta, Ch: 'A'},
			},
			events: slices.Concat(action(press(ebiten.KeyB, ebiten.KeyModControl, ebiten.KeyModShift)), action(press(ebiten.KeyB, ebiten.KeyModSuper, ebiten.KeyModShift)), action(press(ebiten.KeyB, ebiten.KeyModAlt, ebiten.KeyModShift))),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'B', Raw: getCharEscapeSequence('B', term.ModCtrl)},
				{Type: term.EventKey, Mod: term.ModMeta, Ch: 'B', Raw: getCharEscapeSequence('B', term.ModMeta)},
				{Type: term.EventKey, Mod: term.ModAlt, Ch: 'B', Raw: getCharEscapeSequence('B', term.ModAlt)},
			},
		},
		{
			description: "shift-ctrl-a to shift-alt-a with ctrl to meta remaps shift-ctrl-a to shift-alt-a and shift-ctrl-b to shift-meta-b",
			mapping: map[term.KeyComb]term.KeyComb{
				{Mod: term.ModCtrl, Ch: 'A'}: {Mod: term.ModAlt, Ch: 'A'},
				{Mod: term.ModCtrl}:          {Mod: term.ModMeta},
			},
			events: slices.Concat(action(press(ebiten.KeyA, ebiten.KeyModControl, ebiten.KeyModShift)), action(press(ebiten.KeyB, ebiten.KeyModControl, ebiten.KeyModShift))),
			expectedEvents: []term.Event{
				{Type: term.EventKey, Mod: term.ModAlt, Ch: 'A', Raw: getCharEscapeSequence('A', term.ModAlt)},
				{Type: term.EventKey, Mod: term.ModMeta, Ch: 'B', Raw: getCharEscapeSequence('B', term.ModMeta)},
			},
		},
	}

	for _, test := range suite {
		t.Run(test.description, func(t *testing.T) {
			mock, input := newTestInput(t)
			input.setKeyMapping(test.mapping)
			mock.events = slices.Concat(test.events, text(test.chars...))

			events := input.processEvents(nil)
			if len(test.expectedEvents) == 0 {
				assert.Empty(t, events)
				return
			}
			require.Equal(t, len(test.expectedEvents), len(events))
			assert.Equal(t, test.expectedEvents, events)
		})
	}
}

// TestInputForwardsEmojiSequenceRunes asserts that committed text is forwarded
// rune for rune, including the zero-width joiner that composes a family emoji.
// The emoji picker delivers a ZWJ sequence as a standalone commit; the runtime
// must emit a key event for every rune so the buffer can coalesce them into one
// grapheme cluster. Dropping the joiner here (as the upstream ebiten IsPrint
// filter used to) would split the emoji.
func TestInputForwardsEmojiSequenceRunes(t *testing.T) {
	mock, input := newTestInput(t)
	family := []rune{'\U0001F468', '\u200d', '\U0001F469', '\u200d', '\U0001F467'}
	mock.events = text(family...)

	events := input.processEvents(nil)

	require.Len(t, events, len(family))
	for i, r := range family {
		assert.Equal(t, term.Event{
			Type: term.EventKey,
			Ch:   r,
			Raw:  getCharEscapeSequence(r, 0),
		}, events[i], "rune %d (%#U) must be forwarded as a key event", i, r)
	}
}

// TestResolveCharKeyStripsShift asserts that Shift selects the shifted rune and
// leaves the modifier set for every combination of the remaining modifiers. A
// terminal chord names the character the layout produced, so reporting Shift
// alongside an already-shifted rune would encode the same intent twice.
func TestResolveCharKeyStripsShift(t *testing.T) {
	cases := []struct {
		name string
		mod  term.Modifier
	}{
		{"none", 0},
		{"ctrl", term.ModCtrl},
		{"alt", term.ModAlt},
		{"meta", term.ModMeta},
		{"ctrl+alt", term.ModCtrlAlt},
		{"ctrl+meta", term.ModCtrlMeta},
		{"alt+meta", term.ModAltMeta},
		{"ctrl+alt+meta", term.ModCtrlAltMeta},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch, mod := resolveCharKey('a', 'A', tc.mod)
			assert.Equal(t, 'a', ch)
			assert.Equal(t, tc.mod, mod)
		})
		t.Run(tc.name+"+shift", func(t *testing.T) {
			ch, mod := resolveCharKey('a', 'A', tc.mod|term.ModShift)
			assert.Equal(t, 'A', ch)
			assert.Equal(t, tc.mod, mod)
		})
	}
}
