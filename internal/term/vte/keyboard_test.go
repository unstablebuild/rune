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
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/sounds"
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

// modalHarness is a keyboardHarness in modal mode whose event loop is the
// test goroutine: ticks only run when it drains them.
type modalHarness struct {
	*keyboardHarness
	ticks  chan func()
	notes  *recordingNotifications
	played []string
}

// newModalHarness returns a modalHarness showing the prompt "$ echo hi",
// whose probe times out after 50ms.
func newModalHarness(t *testing.T, cfg Config) *modalHarness {
	t.Helper()
	h := &modalHarness{ticks: make(chan func(), 1000), notes: new(recordingNotifications)}
	cfg.Modal = true
	cfg.ScheduleNextTick = func(cb func()) bool {
		select {
		case h.ticks <- cb:
			return true
		default:
			return false
		}
	}
	cfg.Sounds = sounds.PlayerFunc(func(name string) { h.played = append(h.played, name) })
	comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &mockTabManager{}, cfg)
	require.NoError(t, err)
	comp.bellProbeTimeout = 50 * time.Millisecond
	pty, ok := comp.pty.Master.(*workspacetest.File)
	require.True(t, ok)
	handler := &Handler{
		comp: comp, ctx: context.Background(), notifications: h.notes,
		modalEnabled: true, viEnabled: true,
	}
	handler.vi.init(comp, cfg)
	handler.vi.refused = handler.modalRefused
	handler.Resize(80, 24)
	h.keyboardHarness = &keyboardHarness{Handler: handler, pty: pty}
	h.output("$ echo hi")
	h.written()
	return h
}

// press handles keys, written as in term.ParseKeys, and runs the ticks
// they schedule.
func (h *modalHarness) press(t *testing.T, keys string) {
	t.Helper()
	combs, err := term.ParseKeys(keys)
	require.NoError(t, err)
	for _, k := range combs {
		ev := term.Event{Type: term.EventKey, Key: k.Key, Ch: k.Ch, Mod: k.Mod}
		// the GUI gives a plain key its bytes
		switch {
		case k.Mod != 0:
		case k.Ch != 0:
			ev.Raw = []byte(string(k.Ch))
		case k.Key == term.KeyEsc:
			ev.Raw = []byte{0x1b}
		}
		h.Handle(ev)
	}
	h.drain()
}

func (h *modalHarness) drain() {
	for {
		select {
		case cb := <-h.ticks:
			cb()
		default:
			return
		}
	}
}

// waitTick runs the next tick, which only a timer schedules, and the
// ticks it schedules.
func (h *modalHarness) waitTick(t *testing.T) {
	t.Helper()
	select {
	case cb := <-h.ticks:
		cb()
	case <-time.After(5 * time.Second):
		t.Fatal("nothing was scheduled")
	}
	h.drain()
}

func (h *modalHarness) errorSounds() int {
	var n int
	for _, name := range h.played {
		if name == sounds.Error {
			n++
		}
	}
	return n
}

// screen returns what the terminal draws, with the rows trimmed.
func (h *modalHarness) screen(t *testing.T) []string {
	t.Helper()
	w := term.NewStringWriter(80, 24)
	h.Draw(w)
	require.NoError(t, w.Flush())
	rows := strings.Split(w.String(), "\n")
	for i, row := range rows {
		rows[i] = strings.TrimRight(row, " ")
	}
	return rows
}

// rows returns the text of the primary screen and its history, which only
// the program writes.
func (h *modalHarness) rows() string {
	scroll, mu := h.comp.PrimaryScroll()
	mu.Lock()
	defer mu.Unlock()
	return term.CellsToString(scroll.Buffer().RawCells())
}

// frozenRows returns the text of the screen modal mode froze, and whether
// modal mode is over it.
func (h *modalHarness) frozenRows() (string, bool) {
	h.vi.sync.mu.Lock()
	defer h.vi.sync.mu.Unlock()
	return h.vi.frozen.buf.String(), h.vi.frozen.on
}

// probing reports whether a probe waits for the program's bell.
func (h *modalHarness) probing() bool {
	h.vi.sync.mu.Lock()
	defer h.vi.sync.mu.Unlock()
	return h.vi.probe == viProbing
}

func keyEv(key term.Key, mod term.Modifier) term.Event {
	return term.Event{Type: term.EventKey, Key: key, Mod: mod}
}

func chEv(ch rune, mod term.Modifier) term.Event {
	return term.Event{Type: term.EventKey, Ch: ch, Mod: mod}
}

// keyEvents returns the events of keys, written as in term.ParseKeys.
func keyEvents(t *testing.T, keys string) []term.Event {
	t.Helper()
	combs, err := term.ParseKeys(keys)
	require.NoError(t, err)
	events := make([]term.Event, len(combs))
	for i, k := range combs {
		events[i] = term.Event{Type: term.EventKey, Key: k.Key, Ch: k.Ch, Mod: k.Mod}
	}
	return events
}

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

func TestHandlerShiftEsc(t *testing.T) {
	t.Parallel()
	const (
		command = "\x1b]133;C\x1b\\"
		// a full-screen program draws two rows and leaves its cursor on
		// the second
		altScreen = "\x1b[?1049h\x1b[Hfirst row\r\nsecond row\x1b[2;3H"
		altCursor = "\x1b[2;3H"
	)
	cases := []struct {
		name       string
		output     string
		steps      []modalStep
		wantViMode bool
		wantCursor *term.Coordinates
		wantScreen []string
		wantErrors int
		// wantYanked is what yy copies after the steps, empty to not yank.
		wantYanked string
	}{
		{
			name:       "enters modal mode",
			steps:      []modalStep{{keys: "<s-esc>"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 8},
		},
		{
			name:       "enters modal mode under kitty",
			output:     "\x1b[>1u",
			steps:      []modalStep{{keys: "<s-esc>"}},
			wantViMode: true,
		},
		{
			name:       "stays in modal mode",
			steps:      []modalStep{{keys: "<s-esc>h<s-esc>"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 7},
		},
		{
			name:       "does not arm esc for the command",
			output:     command,
			steps:      []modalStep{{keys: "<s-esc><esc>"}},
			wantViMode: true,
		},
		{
			name:       "esc arms after it",
			output:     command,
			steps:      []modalStep{{keys: "<s-esc>i<esc><esc>", want: "\x1b"}},
			wantViMode: true,
		},
		{
			name:   "esc stays with a full-screen program",
			output: altScreen,
			steps:  []modalStep{{keys: "<esc>", want: "\x1b"}},
		},
		{
			name:       "enters modal mode over a full-screen program",
			output:     altScreen,
			steps:      []modalStep{{keys: "<s-esc>"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 2, Y: 1},
			wantScreen: []string{"first row", "second row"},
		},
		{
			name:   "no key reaches the full-screen program",
			output: altScreen,
			steps: []modalStep{
				{keys: "<s-esc>kx<enter>$a1<esc>dd<up>"},
			},
			wantViMode: true, wantErrors: 5, wantScreen: []string{"first row", "second row"},
		},
		{
			name:       "no line join reaches the full-screen program",
			output:     altScreen,
			steps:      []modalStep{{keys: "<s-esc>kgJ"}},
			wantViMode: true, wantErrors: 1, wantScreen: []string{"first row", "second row"},
		},
		{
			name:       "no delete at the end of a line reaches the full-screen program",
			output:     altScreen,
			steps:      []modalStep{{keys: "<s-esc>kA<delete>"}},
			wantViMode: true, wantErrors: 1, wantScreen: []string{"first row", "second row"},
		},
		{
			name:       "no backspace at the start of a line reaches the full-screen program",
			output:     altScreen,
			steps:      []modalStep{{keys: "<s-esc>I<backspace>"}},
			wantViMode: true, wantErrors: 1, wantScreen: []string{"first row", "second row"},
		},
		{
			name:       "no delete before the start of a line reaches the full-screen program",
			output:     altScreen,
			steps:      []modalStep{{keys: "<s-esc>0X"}},
			wantViMode: true, wantErrors: 1, wantScreen: []string{"first row", "second row"},
		},
		{
			name:       "A appends after the line",
			output:     altScreen,
			steps:      []modalStep{{keys: "<s-esc>kA"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 9},
			wantScreen: []string{"first row", "second row"},
		},
		{
			name:       "nothing A appends reaches the full-screen program",
			output:     altScreen,
			steps:      []modalStep{{keys: "<s-esc>kAx"}},
			wantViMode: true, wantErrors: 1, wantScreen: []string{"first row", "second row"},
		},
		{
			name:   "the screen does not follow the program",
			output: altScreen,
			steps: []modalStep{
				{keys: "<s-esc>", output: "\x1b[Hdrawn later"},
			},
			wantViMode: true, wantScreen: []string{"first row", "second row"},
		},
		{
			name:       "yanks from the screen",
			output:     altScreen,
			steps:      []modalStep{{keys: "<s-esc>k0"}},
			wantViMode: true, wantYanked: "first row",
		},
		{
			name:   "leaving shows the program again",
			output: altScreen,
			steps: []modalStep{
				{keys: "<s-esc>", output: "\x1b[Hdrawn later"},
				{keys: "i"},
			},
			wantScreen: []string{"drawn later", "second row"},
		},
		{
			name:   "leaving writes nothing",
			output: altScreen,
			steps:  []modalStep{{keys: "<s-esc>kk<c-c>"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newModalHarness(t, DefaultConfig())
			h.output(tc.output)
			h.written()
			for i, step := range tc.steps {
				h.press(t, step.keys)
				h.output(step.output)
				h.drain()
				assert.Equal(t, step.want, h.written(), "step %d", i)
			}
			assert.Equal(t, tc.wantViMode, h.viMode)
			if tc.wantCursor != nil {
				assert.Equal(t, *tc.wantCursor, h.vi.cursorAtScroll())
			}
			if tc.wantScreen != nil {
				assert.Equal(t, tc.wantScreen, h.screen(t)[:len(tc.wantScreen)])
			}
			assert.Equal(t, tc.wantErrors, h.errorSounds())
			if tc.wantYanked != "" {
				h.press(t, "yy")
				data, err := h.vi.config.Clipboard.Paste(h.vi.config.ClipboardRegister)
				require.NoError(t, err)
				assert.Equal(t, tc.wantYanked, strings.TrimRight(data.Text, " \x00\n"))
				assert.Empty(t, h.written())
			}
		})
	}
}

// modalStep handles events, presses keys, then the program prints
// output, then, with timeout, the probe times out; want is what reached
// the program.
type modalStep struct {
	events  []term.Event
	keys    string
	output  string
	timeout bool
	want    string
}

func TestHandlerModalEsc(t *testing.T) {
	t.Parallel()
	const (
		probe = "\x01\x07"
		// the shell's answer to the probe: ctrl-a put its cursor after "$ "
		answer = "\x1b[3G\a"
		right  = "\x1b[C"
		del    = "\x1b[3~"
		// ctrl-e
		endOfLine = "\x05"
		command   = "\x1b]133;C\x1b\\"
		working   = "\x1b]7501;state=working\x1b\\"
		prompt    = "\x1b]133;A\x1b\\"
		// a second prompt below the first
		secondPrompt = "\r\n$ two"
	)
	// more rows than the screen holds, then a prompt
	history := strings.Repeat("\r\nline", 100) + secondPrompt
	hover := term.Event{Type: term.EventMouse, MouseX: 3, MouseY: 0}
	cases := []struct {
		name       string
		output     string
		steps      []modalStep
		wantViMode bool
		// wantCursor is the vi cursor, nil to not check it.
		wantCursor *term.Coordinates
		wantErrors int
		wantNotes  int
		// wantHint is whether the notes tell how to send <esc>.
		wantHint bool
	}{
		{
			name:       "esc enters at once",
			steps:      []modalStep{{keys: "<esc>"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 8},
		},
		{
			name:       "motions write nothing",
			steps:      []modalStep{{keys: "<esc>hhh0"}},
			wantViMode: true, wantCursor: &term.Coordinates{},
		},
		{
			name:       "motions stay on the content",
			steps:      []modalStep{{keys: "<esc>hG"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 7},
		},
		{
			name: "an edit probes the shell",
			steps: []modalStep{
				{keys: "<esc>x", want: probe},
				{output: answer, want: strings.Repeat(right, 6) + del + probe},
			},
			wantViMode: true,
		},
		{
			name: "a shell that does not answer turns modal mode read-only",
			steps: []modalStep{
				{keys: "<esc>xh", want: probe},
				{timeout: true},
				{output: "\a"},
				{keys: "x"},
			},
			wantViMode: true, wantCursor: &term.Coordinates{X: 7}, wantErrors: 2, wantNotes: 1,
		},
		{
			name:       "a program that reported a status is not rung",
			output:     command + working,
			steps:      []modalStep{{keys: "<esc>x"}, {keys: "x"}},
			wantViMode: true, wantErrors: 2, wantNotes: 1, wantHint: true,
		},
		{
			name:   "a command that does not answer gets the hint",
			output: command,
			steps: []modalStep{
				{keys: "<esc>x", want: probe},
				{timeout: true},
			},
			wantViMode: true, wantErrors: 1, wantNotes: 1, wantHint: true,
		},
		{
			name:       "a program that reported no status is rung",
			output:     command,
			steps:      []modalStep{{keys: "<esc>x", want: probe}},
			wantViMode: true,
		},
		{
			name:  "leaving without moving writes nothing",
			steps: []modalStep{{keys: "<esc>i"}},
		},
		{
			name:  "leaving at the start of the line probes",
			steps: []modalStep{{keys: "<esc>0i", want: probe}},
		},
		{
			name: "leaving moves the shell's cursor to the vi cursor",
			steps: []modalStep{
				{keys: "<esc>hhi", want: probe},
				{output: answer, want: strings.Repeat(right, 4)},
			},
		},
		{
			name: "enter runs the line",
			steps: []modalStep{
				{keys: "<esc><enter>", want: probe},
				{output: answer, want: "\r"},
			},
		},
		{
			name:       "esc again sends esc to a running command",
			output:     command,
			steps:      []modalStep{{keys: "<esc><esc>", want: "\x1b"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 8},
		},
		{
			name:       "esc again sends esc in the command's encoding",
			output:     "\x1b[>1u" + command,
			steps:      []modalStep{{keys: "<esc><esc>", want: "\x1b[27u"}},
			wantViMode: true,
		},
		{
			name:       "every esc after the first is sent",
			output:     command,
			steps:      []modalStep{{keys: "<esc><esc><esc>", want: "\x1b\x1b"}},
			wantViMode: true,
		},
		{
			name:       "a key between keeps esc in modal mode",
			output:     command,
			steps:      []modalStep{{keys: "<esc>j<esc>"}},
			wantViMode: true,
		},
		{
			name:   "moving the pointer between sends esc",
			output: command,
			steps: []modalStep{
				{keys: "<esc>"},
				{events: []term.Event{hover}, keys: "<esc>", want: "\x1b"},
			},
			wantViMode: true,
		},
		{
			name:       "esc again at the prompt stays in modal mode",
			output:     command + prompt,
			steps:      []modalStep{{keys: "<esc><esc>"}},
			wantViMode: true,
		},
		{
			name:       "esc again without marks stays in modal mode",
			steps:      []modalStep{{keys: "<esc><esc>"}},
			wantViMode: true,
		},
		{
			name:   "a probe answered in the command keeps esc in modal mode",
			output: command,
			steps: []modalStep{
				{keys: "<esc>x", want: probe},
				{output: answer, want: strings.Repeat(right, 6) + del + probe},
				{output: "\a"},
				{keys: "i", want: probe},
				{output: answer, want: strings.Repeat(right, 6)},
				{keys: "<esc><esc>"},
				{output: command},
				{keys: "<esc>", want: "\x1b"},
			},
			wantViMode: true,
		},
		{
			name: "a line join probes the shell, which gets none",
			steps: []modalStep{
				{keys: "<esc>gJ", want: probe},
				{output: answer, want: probe},
			},
			wantViMode: true, wantErrors: 1,
		},
		{
			name:   "a line join above the prompt is refused",
			output: secondPrompt,
			steps: []modalStep{
				{keys: "<esc>kgJ", want: probe},
				{output: answer},
			},
			wantViMode: true, wantErrors: 1,
		},
		{
			name:   "a line join in the history is refused",
			output: history,
			steps: []modalStep{
				{keys: "<esc>kkkgJ", want: probe},
				{output: answer},
			},
			wantViMode: true, wantErrors: 1,
		},
		{
			name: "delete at the end of the line probes the shell, which gets none",
			steps: []modalStep{
				{keys: "<esc>A<delete>", want: probe},
				{output: answer, want: probe},
			},
			wantViMode: true, wantErrors: 1,
		},
		{
			name:   "backspace at the start of the line probes the shell, which gets none",
			output: secondPrompt,
			steps: []modalStep{
				{keys: "<esc>I<backspace>", want: probe},
				{output: answer, want: probe},
			},
			wantViMode: true, wantErrors: 1,
		},
		{
			name:   "backspace at the start of a line in the history is refused",
			output: history,
			steps: []modalStep{
				{keys: "<esc>kkkI<backspace>", want: probe},
				{output: answer},
			},
			wantViMode: true, wantErrors: 1,
		},
		{
			name:   "delete before the start of the line probes the shell, which gets none",
			output: secondPrompt,
			steps: []modalStep{
				{keys: "<esc>0X", want: probe},
				{output: answer, want: probe},
			},
			wantViMode: true, wantErrors: 1,
		},
		{
			name:       "a program that reported a status gets no line join",
			output:     secondPrompt + command + working,
			steps:      []modalStep{{keys: "<esc>gJ"}, {keys: "A<delete>"}, {keys: "0X"}},
			wantViMode: true, wantErrors: 3, wantNotes: 1, wantHint: true,
		},
		// the screen's rows are as wide as the screen: past what the
		// program wrote are cells it never wrote
		{
			name:       "A appends after the line",
			steps:      []modalStep{{keys: "<esc>A"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 9},
		},
		{
			name: "what A appends reaches the end of the shell's line",
			steps: []modalStep{
				{keys: "<esc>AX", want: probe},
				{output: answer, want: endOfLine + "X" + probe},
			},
			wantViMode: true,
		},
		{
			name:       "A appends after the spaces the program wrote",
			output:     "  ",
			steps:      []modalStep{{keys: "<esc>A"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 11},
		},
		{
			name:       "A appends after a line above the prompt",
			output:     secondPrompt,
			steps:      []modalStep{{keys: "<esc>kA"}},
			wantViMode: true, wantCursor: &term.Coordinates{X: 9},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newModalHarness(t, DefaultConfig())
			h.output(tc.output)
			h.written()
			rows := h.rows()
			for i, step := range tc.steps {
				for _, ev := range step.events {
					h.Handle(ev)
				}
				h.press(t, step.keys)
				h.output(step.output)
				h.drain()
				if step.timeout {
					h.waitTick(t)
				}
				assert.Equal(t, step.want, h.written(), "step %d", i)
			}
			assert.Equal(t, tc.wantViMode, h.viMode)
			if tc.wantCursor != nil {
				assert.Equal(t, *tc.wantCursor, h.vi.cursorAtScroll())
			}
			assert.Equal(t, rows, h.rows(), "only the program writes the rows")
			assert.Equal(t, tc.wantErrors, h.errorSounds())
			notes := h.notes.Messages()
			assert.Len(t, notes, tc.wantNotes)
			for _, note := range notes {
				assert.Equal(t, tc.wantHint, strings.Contains(note, modalRefusedEscHint), note)
			}
		})
	}
}

// modalFuzzKeys are the keys FuzzHandlerModalKeys presses, written as in
// term.ParseKeys.
const modalFuzzKeys = `abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789` +
	`$^~.;,/?%{}()[]*#'"=-+_|:!@&\<\>` +
	`<esc><s-esc><enter><backspace><delete><insert><up><down><left><right>` +
	`<tab><space><home><end><pgup><pgdn>` +
	`<c-a><c-b><c-c><c-d><c-e><c-f><c-h><c-j><c-k><c-n><c-o><c-p><c-r><c-u><c-v><c-w><c-x><c-y>`

var modalFuzzKey = regexp.MustCompile(`<[^>]+>|\\.|.`)

func FuzzHandlerModalKeys(f *testing.F) {
	const (
		// the shell's answer to a probe: ctrl-a put its cursor after "$ "
		answer    = "\x1b[3G\a"
		command   = "\x1b]133;C\x1b\\"
		working   = "\x1b]7501;state=working\x1b\\"
		altScreen = "\x1b[?1049h\x1b[Hfirst row\r\nsecond row\x1b[2;3H"
	)
	const (
		promptOnly = iota
		twoPrompts
		historyAbove
		fullScreen
		layouts
	)
	const (
		answers = iota
		silent
		reportedStatus
		shells
	)
	alphabet := modalFuzzKey.FindAllString(modalFuzzKeys, -1)
	require.LessOrEqual(f, len(alphabet), 256)
	encode := func(keys string) []byte {
		var encoded []byte
		for _, key := range modalFuzzKey.FindAllString(keys, -1) {
			i := slices.Index(alphabet, key)
			require.GreaterOrEqual(f, i, 0, "seed key %q is not fuzzed", key)
			encoded = append(encoded, byte(i))
		}
		return encoded
	}
	for _, seed := range []struct {
		layout, shell uint8
		keys          string
	}{
		{promptOnly, answers, "gJ"},
		{promptOnly, answers, "A<delete>"},
		{promptOnly, answers, "xhx"},
		{promptOnly, answers, "hhi"},
		{promptOnly, answers, "0Cecho<esc>u"},
		{promptOnly, answers, "0vey$p<enter>"},
		{promptOnly, silent, "xhx"},
		{promptOnly, reportedStatus, "xx<esc><esc>"},
		{twoPrompts, answers, "kgJ"},
		{twoPrompts, answers, "VkgJ"},
		{twoPrompts, answers, "I<backspace>"},
		{twoPrompts, answers, "0X"},
		{twoPrompts, answers, "cc<backspace>"},
		{twoPrompts, answers, "S<backspace>x"},
		{twoPrompts, answers, "o<backspace>"},
		{twoPrompts, answers, "kA<delete><esc>jA<delete>"},
		{twoPrompts, silent, "gJA<delete>0X"},
		{twoPrompts, reportedStatus, "gJA<delete>0X"},
		{historyAbove, answers, "kkkgJ"},
		{historyAbove, answers, "kkkI<backspace>"},
		{historyAbove, answers, "ggjgJ0XjI<backspace><esc>jA<delete>"},
		{historyAbove, answers, "/line<enter>nNgJ"},
		{historyAbove, answers, "<c-u><c-u>3gJG"},
		{historyAbove, silent, "ggVGgJ"},
		{fullScreen, answers, "kgJ"},
		{fullScreen, answers, "kA<delete>"},
		{fullScreen, answers, "I<backspace>"},
		{fullScreen, answers, "0X"},
		{fullScreen, answers, "kx<enter>$a1<esc>dd<up>"},
		{fullScreen, silent, "ggVGgJ"},
	} {
		f.Add(seed.layout, seed.shell, encode(seed.keys))
	}
	history := strings.Repeat("\r\nline", 100) + "\r\n$ two"

	f.Fuzz(func(t *testing.T, layout, shell uint8, keys []byte) {
		layout %= layouts
		shell %= shells
		h := newModalHarness(t, DefaultConfig())
		// a probe times out on a timer: at once for a shell that never
		// answers, never for one that does
		h.comp.bellProbeTimeout = time.Millisecond
		if shell == answers {
			h.comp.bellProbeTimeout = time.Minute
		}
		enter := "<esc>"
		switch layout {
		case twoPrompts:
			h.output("\r\n$ two")
		case historyAbove:
			h.output(history)
		case fullScreen:
			h.output(altScreen)
			enter = "<s-esc>"
		}
		if shell == reportedStatus {
			h.output(command + working)
		}
		h.written()
		rows := h.rows()

		// press presses key and returns what reached the program, which
		// only rings back when it answers probes
		press := func(key string) string {
			h.press(t, key)
			var written strings.Builder
			for {
				out := h.written()
				written.WriteString(out)
				bells := strings.Count(out, "\a")
				if bells == 0 || shell != answers {
					break
				}
				for range bells {
					h.output(answer)
					h.drain()
				}
			}
			if h.probing() {
				h.waitTick(t)
			}
			return written.String()
		}
		press(enter)
		var digits int
		for i, k := range keys[:min(len(keys), 64)] {
			key := alphabet[int(k)%len(alphabet)]
			// a count repeats what the shell refuses once for every
			// one of its units: a long one takes the engine's time
			if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
				if digits++; digits > 2 {
					continue
				}
			} else {
				digits = 0
			}
			frozenBefore, wasFrozen := h.frozenRows()
			written := press(key)
			frozenAfter, isFrozen := h.frozenRows()

			require.Equal(t, rows, h.rows(), "key %d (%s): only the program writes the rows", i, key)
			if wasFrozen && isFrozen {
				require.Equal(t, frozenBefore, frozenAfter, "key %d (%s): the frozen screen stays", i, key)
				require.Empty(t, written, "key %d (%s): no key reaches the program under the frozen screen", i, key)
			}
			pos := h.vi.cursorAtScroll()
			require.False(t, pos.X < 0 || pos.Y < 0, "key %d (%s): the cursor left the content: %+v", i, key, pos)
			h.screen(t)
			h.Cursor()
		}
	})
}

func TestHandlerLeavesWhitelistedKeysUnhandled(t *testing.T) {
	t.Parallel()
	ctrlBacktick := term.Event{Type: term.EventKey, Ch: '`', Mod: term.ModCtrl, Raw: []byte("`")}
	for _, tc := range []struct {
		name   string
		output string
		ev     term.Event
		want   string
	}{
		{name: "legacy", ev: ctrlBacktick},
		{name: "modifyOtherKeys", output: "\x1b[>4;2m", ev: ctrlBacktick},
		{name: "kitty", output: "\x1b[>1u", ev: ctrlBacktick},
		{name: "kitty reports all keys", output: "\x1b[>8u", ev: ctrlBacktick},
		{
			name: "backtick stays with the shell",
			ev:   term.Event{Type: term.EventKey, Ch: '`', Raw: []byte("`")},
			want: "`",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newKeyboardHarness(t)
			h.output(tc.output)
			h.written()
			_, handled := h.Handle(tc.ev)
			assert.Equal(t, tc.want != "", handled)
			assert.Equal(t, tc.want, h.written())
		})
	}
}

func TestHandlerCopyPassThrough(t *testing.T) {
	t.Parallel()
	cmdC := chEv('c', term.ModMeta)
	cases := []struct {
		name     string
		output   string
		selected bool
		ev       term.Event
		want     string
	}{
		{name: "kitty", output: "\x1b[>1u", ev: cmdC, want: "\x1b[99;9u"},
		{name: "kitty reports all keys", output: "\x1b[>27u", ev: cmdC, want: "\x1b[99;9u"},
		{name: "legacy has no cmd", ev: cmdC},
		{name: "selection", output: "\x1b[>1u", selected: true, ev: cmdC},
		{name: "cmd v", output: "\x1b[>1u", ev: chEv('v', term.ModMeta)},
		{name: "cmd shift c", output: "\x1b[>1u", ev: chEv('C', term.ModMeta)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newKeyboardHarness(t)
			h.output(tc.output + "hello")
			h.written()
			if tc.selected {
				h.comp.Select(term.Coordinates{})
				h.comp.SelectEnd(term.Coordinates{X: 4})
				_, ok := h.comp.Selection()
				require.True(t, ok)
			}

			_, handled := h.Handle(tc.ev)

			assert.Equal(t, tc.want != "", handled)
			assert.Equal(t, tc.want, h.written())
		})
	}
}

func TestHandlerBracketedPaste(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		output     string
		start, end string
		paste      string
		want       string
	}{
		{
			name:   "rune paste",
			output: "\x1b[?2004h",
			paste:  "a\x1bb\n",
			want:   "\x1b[200~ab\n\x1b[201~",
		},
		{
			name:   "terminal paste",
			output: "\x1b[?2004h",
			start:  "\x1b[200~",
			end:    "\x1b[201~",
			paste:  "a\x1bb\n",
			want:   "\x1b[200~ab\n\x1b[201~",
		},
		{name: "program without bracketed paste", paste: "ab\n", want: "ab\r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := newKeyboardHarness(t)
			h.output(tc.output)
			h.written()

			h.Handle(term.Event{Type: term.EventPasteStart, Raw: []byte(tc.start)})
			for _, ch := range tc.paste {
				h.Handle(term.Event{Type: term.EventKey, Ch: ch, Raw: []byte(string(ch))})
			}
			h.Handle(term.Event{Type: term.EventPasteEnd, Raw: []byte(tc.end)})

			assert.Equal(t, tc.want, h.written())
		})
	}
}
