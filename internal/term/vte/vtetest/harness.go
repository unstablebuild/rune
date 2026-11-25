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

package vtetest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// Case is a vte test case.
type Case struct {
	InputSequence string
	Expected      string
}

// TestSequence tests the given handler with the given test cases.
// This differs from the default testing tui.Handler harness' in that
// it waits up to drawTimeout for interruptChan to stop sending requests
// to advance to the test case's draw and assertions.
//
// This is mostly useful for asynchronous tui.Handler that are 'ready'
// for assertion when idle for drawTimeout, as defined by not sending interrupt
// requests.
func TestSequence(
	t *testing.T, handler tui.Handler, width, height int,
	drawTimeout time.Duration, interruptChan chan struct{},
	cases []Case,
) {
	writer := term.NewStringWriter(width, height)
	handler.Resize(width, height)

	// wait for first handler to initialize,
	// first interrupt should be a good indicator
	<-interruptChan

	for i, tcase := range cases {
		handleTestCase(t, i, writer, handler, tcase,
			width, height, drawTimeout, interruptChan)
	}
}

// TestCases tests the given handler with the given test cases,
// but assumes that it's an already initialized handler,
// so it doesn't Resize or wait for initial interrupt.
func TestCases(
	t *testing.T, handler tui.Handler, width, height int,
	drawTimeout time.Duration, interruptChan chan struct{},
	cases []Case,
) {
	writer := term.NewStringWriter(width, height)
	for i, tcase := range cases {
		handleTestCase(t, i, writer, handler, tcase,
			width, height, drawTimeout, interruptChan)
	}
}

func handleTestCase(
	t *testing.T, i int, w *term.StringWriter,
	h tui.Handler, tcase Case, width, height int,
	drawTimeout time.Duration,
	interruptChan chan struct{},
) {
	t.Helper()
	err := w.Clear(term.Attributes{})
	require.NoError(t, err)

	// vte needs Raw field set
	callHandle := func(ev term.Event) {
		switch ev.Mod {
		case 0:
			switch ev.Key {
			case term.KeyBackspace:
				ev.Raw = []byte("\x7f")
			case term.KeyEnter:
				ev.Raw = []byte("\x0A")
			case term.KeyEsc:
				ev.Raw = []byte("\x1b")
			case term.KeyTab:
				ev.Raw = []byte("\x09")
			case term.KeyArrowDown:
				ev.Raw = []byte("\x50")
			case term.KeyArrowUp:
				ev.Raw = []byte("\x48")
			case term.KeyF1:
				ev.Raw = []byte("\x1bOP")
			case term.KeyF2:
				ev.Raw = []byte("\x1bOQ")
			case term.KeyF3:
				ev.Raw = []byte("\x1bOR")
			case term.KeyF4:
				ev.Raw = []byte("\x1bOS")
			case term.KeyF5:
				ev.Raw = []byte("\x1b[15~")
			case term.KeyF6:
				ev.Raw = []byte("\x1b[17~")
			case term.KeyF7:
				ev.Raw = []byte("\x1b[18~")
			case term.KeyF8:
				ev.Raw = []byte("\x1b[19~")
			case term.KeyF9:
				ev.Raw = []byte("\x1b[20~")
			case term.KeyF10:
				ev.Raw = []byte("\x1b[21~")
			case term.KeyF11:
				ev.Raw = []byte("\x1b[22~")
			case term.KeyF12:
				ev.Raw = []byte("\x1b[23~")
			case term.KeyInsert:
				ev.Raw = []byte("\x1b[2~")
			case term.KeyDelete:
				ev.Raw = []byte("\x1b[3~")
			default:
				ev.Raw = []byte(string(ev.Ch))
			}
		case term.ModCtrl:
			switch ev.Ch {
			case '\\':
				ev.Raw = []byte("\x1C")
			case 'v':
				ev.Raw = []byte("\x16")
			case 'l':
				ev.Raw = []byte("\x0c")
			default:
				t.Logf("WARNING: could not find raw vte sequence for input event: %+v", ev)
			}
		}
		h.Handle(ev)
	}

	var escapeNext bool
	for _, r := range tcase.InputSequence {
		if escapeNext {
			escapeNext = false
			callHandle(term.Event{Ch: r, Type: term.EventKey})
			continue
		}
		switch r {
		case '^':
			callHandle(term.Event{Key: term.KeyBackspace, Type: term.EventKey})
		case '#':
			callHandle(term.Event{Mod: term.ModCtrl, Ch: 'c', Type: term.EventKey})
		case '$':
			callHandle(term.Event{Mod: term.ModCtrl, Ch: 'l', Type: term.EventKey})
		case '>':
			callHandle(term.Event{Key: term.KeyEnter, Type: term.EventKey})
		case '<':
			callHandle(term.Event{Key: term.KeyEsc, Type: term.EventKey})
		case '⎋':
			callHandle(term.Event{Key: term.KeyEsc, Mod: term.ModShift, Type: term.EventKey})
		case '✌':
			callHandle(term.Event{Key: term.KeyTab, Type: term.EventKey})
		case '⌦':
			callHandle(term.Event{Key: term.KeyDelete, Type: term.EventKey})
		case '⬇':
			callHandle(term.Event{Key: term.KeyArrowDown, Type: term.EventKey})
		case '⬆':
			callHandle(term.Event{Key: term.KeyArrowUp, Type: term.EventKey})
		case '\\':
			escapeNext = true
		default:
			callHandle(term.Event{Ch: r, Type: term.EventKey})
		}

		timer := time.NewTimer(drawTimeout)
	loop:
		for {
			select {
			case <-timer.C:
				break loop
			case <-interruptChan:
				if !timer.Stop() {
					<-timer.C
				}
				timer.Reset(drawTimeout)
			}
		}
	}

	render := func() string {
		require.NoError(t, w.Clear(term.Attributes{}))
		h.Draw(w)
		if cursor, _, ok := h.Cursor(); ok {
			w.SetCursor(cursor)
		}
		require.NoError(t, w.Flush())
		return w.String()
	}

	// Quiescence is a best-effort settle signal: a loaded host can
	// echo the last keystrokes after drawTimeout has already elapsed,
	// and sampling the screen once then asserts on a half-drawn
	// frame. Converge on the expectation instead. A screen that never
	// converges still fails with the same diff, just later.
	out := render()
	deadline := time.Now().Add(convergeTimeoutFactor * drawTimeout)
	for out != tcase.Expected && time.Now().Before(deadline) {
		select {
		case <-interruptChan:
		case <-time.After(drawTimeout):
		}
		out = render()
	}
	assert.Equal(t, tcase.Expected, out, "test case %d", i)
}

// convergeTimeoutFactor bounds how long a test case waits for the
// screen to match its expectation, as a multiple of drawTimeout.
const convergeTimeoutFactor = 20
