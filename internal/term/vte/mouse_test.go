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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vteparser"
)

func newMouseHarness(t *testing.T, modes ...vteparser.PrivateMode) *mouseDriver {
	t.Helper()
	comp, err := NewComponent(&testExecutor{}, &testExecutor{}, &mockTabManager{}, DefaultConfig())
	require.NoError(t, err)
	comp.Resize(300, 300)
	for _, mode := range modes {
		comp.parserHandler.SetPrivateMode(mode)
	}
	return &mouseDriver{t: comp}
}

func mouseEv(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

type mouseStep struct {
	ev       term.Event
	want     string
	tracking bool
}

// TestMouseDriverReport pins the encodings to kitty's
// encode_mouse_event_impl and the per-mode filter to its
// send_mouse_event (kitty/mouse.c:73-121, :1672-1673).
func TestMouseDriverReport(t *testing.T) {
	t.Parallel()
	const (
		anyEvent = vteparser.PrivateModeReportAllMouseMotion
		button   = vteparser.PrivateModeReportCellMouseMotion
		click    = vteparser.PrivateModeReportMouseClicks
		sgr      = vteparser.PrivateModeSgrMouse
		utf8     = vteparser.PrivateModeUtf8Mouse
	)
	cases := []struct {
		name  string
		modes []vteparser.PrivateMode
		steps []mouseStep
	}{
		{
			// the modes terminal-browser enables: 1003 on its own,
			// without 1000 or 1002
			name:  "any-event sgr",
			modes: []vteparser.PrivateMode{anyEvent, sgr},
			steps: []mouseStep{
				{mouseEv(0, 4, 2), "\x1b[<35;5;3M", true},
				{mouseEv(0, 4, 2), "", true},
				{mouseEv(term.MouseLeft, 4, 2), "\x1b[<0;5;3M", true},
				{mouseEv(term.MouseLeft, 4, 2), "", true},
				{mouseEv(term.MouseLeft, 6, 2), "\x1b[<32;7;3M", true},
				{mouseEv(term.MouseRelease, 6, 2), "\x1b[<0;7;3m", true},
				{mouseEv(term.MouseRight, 6, 2), "\x1b[<2;7;3M", true},
				{mouseEv(term.MouseRelease, 6, 2), "\x1b[<2;7;3m", true},
				{mouseEv(term.MouseMiddle, 6, 2), "\x1b[<1;7;3M", true},
				{mouseEv(term.MouseRelease, 6, 2), "\x1b[<1;7;3m", true},
				{mouseEv(term.MouseWheelUp, 1, 1), "\x1b[<64;2;2M", true},
				{mouseEv(term.MouseWheelUp, 1, 1), "\x1b[<64;2;2M", true},
				{mouseEv(term.MouseWheelDown, 1, 1), "\x1b[<65;2;2M", true},
			},
		},
		{
			name:  "button-event sgr drops hover",
			modes: []vteparser.PrivateMode{button, sgr},
			steps: []mouseStep{
				{mouseEv(0, 4, 2), "", true},
				{mouseEv(term.MouseLeft, 4, 2), "\x1b[<0;5;3M", true},
				{mouseEv(term.MouseLeft, 5, 3), "\x1b[<32;6;4M", true},
				{mouseEv(term.MouseRelease, 5, 3), "\x1b[<0;6;4m", true},
				{mouseEv(term.MouseWheelDown, 5, 3), "\x1b[<65;6;4M", true},
			},
		},
		{
			name:  "click sgr drops motion",
			modes: []vteparser.PrivateMode{click, sgr},
			steps: []mouseStep{
				{mouseEv(0, 4, 2), "", true},
				{mouseEv(term.MouseLeft, 4, 2), "\x1b[<0;5;3M", true},
				{mouseEv(term.MouseLeft, 5, 3), "", true},
				{mouseEv(term.MouseRelease, 5, 3), "\x1b[<0;6;4m", true},
				{mouseEv(term.MouseWheelUp, 5, 3), "\x1b[<64;6;4M", true},
			},
		},
		{
			name:  "legacy encoding",
			modes: []vteparser.PrivateMode{anyEvent},
			steps: []mouseStep{
				{mouseEv(term.MouseLeft, 4, 2), "\x1b[M %#", true},
				{mouseEv(term.MouseLeft, 5, 2), "\x1b[M@&#", true},
				{mouseEv(term.MouseRelease, 5, 2), "\x1b[M#&#", true},
				{mouseEv(term.MouseWheelUp, 5, 2), "\x1b[M`&#", true},
				{mouseEv(0, 6, 2), "\x1b[MC'#", true},
				// kitty drops what the byte encoding cannot address
				{mouseEv(term.MouseLeft, 223, 2), "", true},
			},
		},
		{
			name:  "utf8 encoding",
			modes: []vteparser.PrivateMode{click, utf8},
			steps: []mouseStep{
				{mouseEv(term.MouseLeft, 250, 2), "\x1b[M " + string(rune(283)) + "#", true},
				{mouseEv(term.MouseRelease, 250, 2), "\x1b[M#" + string(rune(283)) + "#", true},
			},
		},
		{
			name:  "modifiers",
			modes: []vteparser.PrivateMode{click, sgr},
			steps: []mouseStep{
				{term.Event{
					Type: term.EventMouse, Key: term.MouseLeft,
					Mod: term.ModCtrl | term.ModShift, MouseX: 0, MouseY: 0,
				}, "\x1b[<20;1;1M", true},
				{term.Event{
					Type: term.EventMouse, Key: term.MouseWheelUp,
					Mod: term.ModAlt, MouseX: 0, MouseY: 0,
				}, "\x1b[<72;1;1M", true},
			},
		},
		{
			name:  "no tracking",
			modes: []vteparser.PrivateMode{sgr},
			steps: []mouseStep{
				{mouseEv(term.MouseLeft, 4, 2), "", false},
				{mouseEv(term.MouseWheelUp, 4, 2), "", false},
				{mouseEv(0, 4, 2), "", false},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newMouseHarness(t, tc.modes...)
			for i, step := range tc.steps {
				raw, tracking := d.report(step.ev)
				assert.Equal(t, step.want, string(raw), "step %d", i)
				assert.Equal(t, step.tracking, tracking, "step %d", i)
			}
		})
	}
}

// TestMouseDriverReportIgnoresPixelMode keeps SGR cell coordinates when
// a program also asks for SGR-pixels (1016): mouse events carry no
// pixel position, and DECRQM reports 1016 as unrecognized so programs
// such as terminal-browser fall back to cells.
func TestMouseDriverReportIgnoresPixelMode(t *testing.T) {
	t.Parallel()
	d := newMouseHarness(t,
		vteparser.PrivateModeReportAllMouseMotion,
		vteparser.PrivateModeSgrMouse,
		vteparser.PrivateMode(1016),
	)
	raw, tracking := d.report(mouseEv(term.MouseLeft, 9, 9))
	assert.True(t, tracking)
	assert.Equal(t, "\x1b[<0;10;10M", string(raw))
}

// TestMouseDriverAlternateScroll pins DECSET 1007: without mouse
// tracking the wheel over the alternate screen, which has no
// scrollback, becomes cursor keys (kitty keys.c:363 fake_scroll).
func TestMouseDriverAlternateScroll(t *testing.T) {
	t.Parallel()
	alt := vteparser.PrivateModeSwapScreenAndSetRestoreCursor
	cases := []struct {
		name  string
		set   []vteparser.PrivateMode
		unset []vteparser.PrivateMode
		ev    term.Event
		want  string
	}{
		{"wheel up", []vteparser.PrivateMode{alt}, nil,
			mouseEv(term.MouseWheelUp, 0, 0), "\x1b[A"},
		{"wheel down", []vteparser.PrivateMode{alt}, nil,
			mouseEv(term.MouseWheelDown, 0, 0), "\x1b[B"},
		{"application cursor keys",
			[]vteparser.PrivateMode{alt, vteparser.PrivateModeCursorKeys}, nil,
			mouseEv(term.MouseWheelUp, 0, 0), "\x1bOA"},
		{"primary screen scrolls the scrollback instead", nil, nil,
			mouseEv(term.MouseWheelUp, 0, 0), ""},
		{"disabled", []vteparser.PrivateMode{alt},
			[]vteparser.PrivateMode{vteparser.PrivateModeAlternateScroll},
			mouseEv(term.MouseWheelUp, 0, 0), ""},
		{"not a wheel", []vteparser.PrivateMode{alt}, nil,
			mouseEv(term.MouseLeft, 0, 0), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newMouseHarness(t, tc.set...)
			for _, mode := range tc.unset {
				d.t.parserHandler.UnsetPrivateMode(mode)
			}
			assert.Equal(t, tc.want, string(d.alternateScroll(tc.ev)))
		})
	}
}
