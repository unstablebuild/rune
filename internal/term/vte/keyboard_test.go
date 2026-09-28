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
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteparser"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// keyboardHarness types keys into a terminal whose program output went
// through the parser, the way both reach it in production.
type keyboardHarness struct {
	*Handler
	pty *workspacetest.File
}

func newKeyboardHarness(t *testing.T) *keyboardHarness {
	t.Helper()
	comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	comp.Resize(80, 24)
	pty, ok := comp.pty.Master.(*workspacetest.File)
	require.True(t, ok)
	return &keyboardHarness{
		Handler: &Handler{comp: comp, ctx: context.Background()},
		pty:     pty,
	}
}

// output feeds program output to the terminal.
func (h *keyboardHarness) output(s string) {
	h.comp.parser.AdvanceBytes([]byte(s))
}

// written returns what reached the program since the last call.
func (h *keyboardHarness) written() string {
	var sb strings.Builder
	for _, w := range h.pty.Writes {
		sb.Write(w)
	}
	h.pty.Writes = nil
	return sb.String()
}

func keyEv(key term.Key, mod term.Modifier) term.Event {
	return term.Event{Type: term.EventKey, Key: key, Mod: mod}
}

func chEv(ch rune, mod term.Modifier) term.Event {
	return term.Event{Type: term.EventKey, Ch: ch, Mod: mod}
}

// TestKittyKeyboardFlags pins the flag stacks to kitty's
// screen_{push,pop,set,report}_key_encoding_flags (kitty/screen.c).
func TestKittyKeyboardFlags(t *testing.T) {
	t.Parallel()
	var overflow strings.Builder
	for flags := 1; flags <= 9; flags++ {
		fmt.Fprintf(&overflow, "\x1b[>%du", flags)
	}
	overflow.WriteString("\x1b[<7u")

	cases := []struct {
		name   string
		output string
		want   string
	}{
		{name: "none", want: "\x1b[?0u"},
		{name: "push", output: "\x1b[>1u", want: "\x1b[?1u"},
		{name: "push without flags", output: "\x1b[>1u\x1b[>u", want: "\x1b[?0u"},
		{name: "pop", output: "\x1b[>1u\x1b[>5u\x1b[<u", want: "\x1b[?1u"},
		{name: "pop count", output: "\x1b[>1u\x1b[>5u\x1b[<2u", want: "\x1b[?0u"},
		{name: "pop past the bottom", output: "\x1b[>1u\x1b[<9u", want: "\x1b[?0u"},
		{name: "set replaces", output: "\x1b[>1u\x1b[=12u", want: "\x1b[?12u"},
		{name: "set union", output: "\x1b[>1u\x1b[=4;2u", want: "\x1b[?5u"},
		{name: "set difference", output: "\x1b[>7u\x1b[=2;3u", want: "\x1b[?5u"},
		{name: "set on an empty stack", output: "\x1b[=3u", want: "\x1b[?3u"},
		{name: "undefined flags dropped", output: "\x1b[>255u", want: "\x1b[?31u"},
		{name: "full stack evicts the oldest", output: overflow.String(), want: "\x1b[?2u"},
		{
			name:   "alternate screen has its own stack",
			output: "\x1b[>1u\x1b[?1049h",
			want:   "\x1b[?0u",
		},
		{
			name:   "primary screen stack survives the alternate screen",
			output: "\x1b[>1u\x1b[?1049h\x1b[>3u\x1b[?1049l",
			want:   "\x1b[?1u",
		},
		{name: "full reset", output: "\x1b[>1u\x1bc", want: "\x1b[?0u"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newKeyboardHarness(t)
			h.output(tc.output)
			h.written()
			h.output("\x1b[?u")
			assert.Equal(t, tc.want, h.written())
		})
	}
}

func TestModifyOtherKeysQuery(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		output string
		want   string
	}{
		{name: "reset", want: "\x1b[>4;0m"},
		{name: "all", output: "\x1b[>4;2m", want: "\x1b[>4;2m"},
		{name: "except well defined", output: "\x1b[>4;1m", want: "\x1b[>4;1m"},
		{name: "cleared", output: "\x1b[>4;2m\x1b[>4m", want: "\x1b[>4;0m"},
		{name: "full reset", output: "\x1b[>4;2m\x1bc", want: "\x1b[>4;0m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newKeyboardHarness(t)
			h.output(tc.output)
			h.written()
			h.output("\x1b[?4m")
			assert.Equal(t, tc.want, h.written())
		})
	}
}

// TestKeyEncodingKitty pins the kitty encoding to the vectors of
// kitty_tests/keys.py test_encode_key_event, as the terminal sees keys:
// only presses, and shift already applied to the character typed.
func TestKeyEncodingKitty(t *testing.T) {
	t.Parallel()
	const (
		disambiguate = vteparser.KeyboardModeDisambiguateEscCodes
		events       = vteparser.KeyboardModeReportEventTypes
		alternates   = vteparser.KeyboardModeReportAlternateKeys
		all          = vteparser.KeyboardModeReportAllKeysAsEsc
		text         = vteparser.KeyboardModeReportAssociatedText
	)
	cases := []struct {
		name  string
		flags vteparser.KeyboardMode
		ev    term.Event
		// want is empty when the key keeps its legacy encoding.
		want string
	}{
		{"disambiguate text", disambiguate, chEv('a', 0), ""},
		{"disambiguate esc", disambiguate, keyEv(term.KeyEsc, 0), "\x1b[27u"},
		{"disambiguate enter", disambiguate, keyEv(term.KeyEnter, 0), ""},
		{"disambiguate shift enter", disambiguate, keyEv(term.KeyEnter, term.ModShift), "\x1b[13;2u"},
		{"disambiguate tab", disambiguate, keyEv(term.KeyTab, 0), ""},
		{"disambiguate shift tab", disambiguate, keyEv(term.KeyTab, term.ModShift), "\x1b[9;2u"},
		{"disambiguate backspace", disambiguate, keyEv(term.KeyBackspace, 0), ""},
		{"disambiguate ctrl", disambiguate, chEv('a', term.ModCtrl), "\x1b[97;5u"},
		{"disambiguate alt", disambiguate, chEv('a', term.ModAlt), "\x1b[97;3u"},
		{"disambiguate ctrl shift", disambiguate, chEv('A', term.ModCtrl), "\x1b[97;6u"},
		{"disambiguate alt shift", disambiguate, chEv('A', term.ModAlt), "\x1b[97;4u"},
		{"disambiguate ctrl space", disambiguate, keyEv(term.KeySpace, term.ModCtrl), "\x1b[32;5u"},
		{"disambiguate ctrl space char", disambiguate, chEv(' ', term.ModCtrl), "\x1b[32;5u"},
		{"disambiguate up", disambiguate, keyEv(term.KeyArrowUp, 0), "\x1b[A"},
		{"disambiguate ctrl up", disambiguate, keyEv(term.KeyArrowUp, term.ModCtrl), "\x1b[1;5A"},
		{"disambiguate f1", disambiguate, keyEv(term.KeyF1, 0), "\x1b[P"},
		{"disambiguate f3", disambiguate, keyEv(term.KeyF3, 0), "\x1b[13~"},
		{"disambiguate ctrl f5", disambiguate, keyEv(term.KeyF5, term.ModCtrl), "\x1b[15;5~"},
		{"disambiguate shift end", disambiguate, keyEv(term.KeyEnd, term.ModShift), "\x1b[1;2F"},
		{"disambiguate delete", disambiguate, keyEv(term.KeyDelete, 0), "\x1b[3~"},
		{"disambiguate page up", disambiguate, keyEv(term.KeyPgup, 0), "\x1b[5~"},
		{"events text", events, chEv('a', 0), ""},
		{"events up", events, keyEv(term.KeyArrowUp, 0), "\x1b[A"},
		{"events ctrl", events, chEv('a', term.ModCtrl), ""},
		{"events esc", events, keyEv(term.KeyEsc, 0), ""},
		{"events backspace", disambiguate | events, keyEv(term.KeyBackspace, 0), ""},
		{"alternates text", alternates, chEv('A', 0), ""},
		{"alternates ctrl", alternates, chEv('a', term.ModCtrl), ""},
		{"alternates ctrl shift", alternates, chEv('A', term.ModCtrl), "\x1b[97:65;6u"},
		{"alternates up", alternates, keyEv(term.KeyArrowUp, 0), ""},
		{"all text", all, chEv('a', 0), "\x1b[97u"},
		{"all shifted text", all, chEv('A', 0), "\x1b[97;2u"},
		{"all non-ascii text", all, chEv('é', 0), "\x1b[233u"},
		{"all space", all, keyEv(term.KeySpace, 0), "\x1b[32u"},
		{"all ctrl", all, chEv('a', term.ModCtrl), "\x1b[97;5u"},
		{"all up", all, keyEv(term.KeyArrowUp, 0), "\x1b[A"},
		{"all enter", all, keyEv(term.KeyEnter, 0), "\x1b[13u"},
		{"all ctrl enter", all, keyEv(term.KeyEnter, term.ModCtrl), "\x1b[13;5u"},
		{"all tab", all, keyEv(term.KeyTab, 0), "\x1b[9u"},
		{"all backspace", all, keyEv(term.KeyBackspace, 0), "\x1b[127u"},
		{"text", all | text, chEv('a', 0), "\x1b[97;;97u"},
		{"shifted text", all | text, chEv('A', 0), "\x1b[97;2;65u"},
		{"no text under ctrl", all | text, chEv('a', term.ModCtrl), "\x1b[97;5u"},
		{"alternates and text", all | alternates | text, chEv('A', 0), "\x1b[97:65;2;65u"},
		{"control character", all, chEv('\x7f', 0), ""},
		{"no key", all, keyEv(0, 0), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := keyEncoding{kitty: tc.flags}.encode(tc.ev)
			assert.Equal(t, tc.want != "", ok)
			assert.Equal(t, tc.want, string(got))
		})
	}
}

func TestKeyEncodingModifyOtherKeys(t *testing.T) {
	t.Parallel()
	all := keyEncoding{modifyOtherKeys: vteparser.ModifyOtherKeysEnableAll}
	ctrlI := chEv('i', term.ModCtrl)
	cases := []struct {
		name string
		enc  keyEncoding
		ev   term.Event
		// want is empty when the key keeps its legacy encoding.
		want string
	}{
		{"text", all, chEv('a', 0), ""},
		{"ctrl letter keeps its c0 byte", all, chEv('a', term.ModCtrl), "\x01"},
		{"ctrl i is not tab", all, ctrlI, "\x1b[27;5;105~"},
		{"ctrl m is not enter", all, chEv('m', term.ModCtrl), "\x1b[27;5;109~"},
		{"ctrl [ is not esc", all, chEv('[', term.ModCtrl), "\x1b[27;5;91~"},
		{"ctrl shift letter", all, chEv('H', term.ModCtrl), "\x1b[27;6;72~"},
		{"ctrl digit", all, chEv('1', term.ModCtrl), "\x1b[27;5;49~"},
		{"ctrl 2 is nul", all, chEv('2', term.ModCtrl), "\x00"},
		{"ctrl period", all, chEv('.', term.ModCtrl), "\x1b[27;5;46~"},
		{"ctrl space", all, keyEv(term.KeySpace, term.ModCtrl), "\x00"},
		{"alt digit", all, chEv('8', term.ModAlt), "\x1b[27;3;56~"},
		{"shift symbol", all, chEv('!', term.ModShift), ""},
		{"shift space", all, keyEv(term.KeySpace, term.ModShift), "\x1b[27;2;32~"},
		{"shift tab", all, keyEv(term.KeyTab, term.ModShift), "\x1b[27;2;9~"},
		{"ctrl tab", all, keyEv(term.KeyTab, term.ModCtrl), "\x1b[27;5;9~"},
		{"shift enter", all, keyEv(term.KeyEnter, term.ModShift), "\x1b[27;2;13~"},
		{"ctrl enter", all, keyEv(term.KeyEnter, term.ModCtrl), "\x1b[27;5;13~"},
		{"shift esc", all, keyEv(term.KeyEsc, term.ModShift), "\x1b[27;2;27~"},
		{"ctrl backspace", all, keyEv(term.KeyBackspace, term.ModCtrl), "\x08"},
		{"shift backspace", all, keyEv(term.KeyBackspace, term.ModShift), "\x1b[27;2;127~"},
		{"ctrl shift backspace", all, keyEv(term.KeyBackspace, term.ModCtrlShift), "\x1b[27;6;127~"},
		{"ctrl up", all, keyEv(term.KeyArrowUp, term.ModCtrl), ""},
		{"enter", all, keyEv(term.KeyEnter, 0), ""},
		{
			name: "mode 1 keeps legacy keys",
			enc:  keyEncoding{modifyOtherKeys: vteparser.ModifyOtherKeysEnableExceptWellDefined},
			ev:   ctrlI,
		},
		{
			name: "kitty flags take precedence",
			enc: keyEncoding{
				kitty:           vteparser.KeyboardModeDisambiguateEscCodes,
				modifyOtherKeys: vteparser.ModifyOtherKeysEnableAll,
			},
			ev:   ctrlI,
			want: "\x1b[105;5u",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := tc.enc.encode(tc.ev)
			assert.Equal(t, tc.want != "", ok)
			assert.Equal(t, tc.want, string(got))
		})
	}
}

// TestHandlerKeyboardProtocols types keys after the program selected an
// encoding, so the parser's state must reach the input path.
func TestHandlerKeyboardProtocols(t *testing.T) {
	t.Parallel()
	ctrlC := term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl, Raw: []byte{0x03}}
	ctrlI := term.Event{Type: term.EventKey, Ch: 'i', Mod: term.ModCtrl, Raw: []byte{0x09}}
	a := term.Event{Type: term.EventKey, Ch: 'a', Raw: []byte("a")}
	cases := []struct {
		name   string
		output string
		ev     term.Event
		want   string
	}{
		{name: "legacy", ev: ctrlC, want: "\x03"},
		{name: "kitty", output: "\x1b[>1u", ev: ctrlC, want: "\x1b[99;5u"},
		{name: "kitty keeps text", output: "\x1b[>1u", ev: a, want: "a"},
		{name: "kitty reports all keys", output: "\x1b[>8u", ev: a, want: "\x1b[97u"},
		{name: "kitty popped", output: "\x1b[>1u\x1b[<u", ev: ctrlC, want: "\x03"},
		{name: "kitty on the other screen", output: "\x1b[>1u\x1b[?1049h", ev: ctrlC, want: "\x03"},
		{
			name:   "kitty back on its screen",
			output: "\x1b[>1u\x1b[?1049h\x1b[?1049l",
			ev:     ctrlC,
			want:   "\x1b[99;5u",
		},
		{name: "kitty full reset", output: "\x1b[>1u\x1bc", ev: ctrlC, want: "\x03"},
		{name: "modifyOtherKeys", output: "\x1b[>4;2m", ev: ctrlI, want: "\x1b[27;5;105~"},
		{
			name:   "kitty over modifyOtherKeys",
			output: "\x1b[>4;2m\x1b[>1u",
			ev:     ctrlI,
			want:   "\x1b[105;5u",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newKeyboardHarness(t)
			h.output(tc.output)
			h.written()
			_, handled := h.Handle(tc.ev)
			assert.True(t, handled)
			assert.Equal(t, tc.want, h.written())
		})
	}
}
