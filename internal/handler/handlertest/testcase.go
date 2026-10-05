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

package handlertest

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// SequenceTestCase represents an input sequence and
// the result expected draw string representation.
type SequenceTestCase struct {
	InputSequence string
	Expected      string
}

// SingleTestCase represents an event and the result
// expected draw string representation.
type SingleTestCase struct {
	Event    term.Event
	Expected string
}

// Writer is a term.Writer whose frame can be compared as a string, such
// as *term.StringWriter.
type Writer interface {
	term.Writer
	fmt.Stringer
	Flush() error
	Clear(term.Attributes) error
	SetCursor(term.Coordinates)
}

// TestHandlerIsolated is a helper function that drives
// a set of SequenceTestCase and its results in an isolated fashion:
// fn will be called on every test case.
//
// Deprecated: use RunHandlerIsolated.
func TestHandlerIsolated(
	t *testing.T, fn func(t *testing.T) tui.Handler, width, height int,
	cases []SequenceTestCase,
) {
	writer := term.NewStringWriter(width, height)

	for i, tcase := range cases {
		t.Run(
			fmt.Sprintf("test case %d (input: %s)", i, tcase.InputSequence),
			func(t *testing.T) {
				handler := fn(t)
				handler.Resize(width, height)
				handleTestCase(t, i, writer, handler, tcase, width, height)
			})
	}
}

// TestHandlerSequence is akin to calling TestHandlerSequenceWriter
// with a default term.StringWriter.
//
// Deprecated: use RunHandlerSequence.
func TestHandlerSequence(
	t *testing.T, handler tui.Handler, width, height int,
	cases []SequenceTestCase,
) {
	writer := term.NewStringWriter(width, height)
	TestHandlerSequenceWriter(t, writer, handler, width, height, cases)
}

// TestHandlerSequenceWriter is a helper function that drives
// a set of SequenceTestCase and its results.
//
// Certain key events are encoded in characters. For instance, a '>' character
// signals term.KeyEnter and '<' character signals term.KeyEsc.
func TestHandlerSequenceWriter(
	t *testing.T, writer *term.StringWriter, handler tui.Handler, width, height int,
	cases []SequenceTestCase,
) {
	handler.Resize(width, height)
	for i, tcase := range cases {
		handleTestCase(t, i, writer, handler, tcase, width, height)
	}
}

// TestHandler is a helper function that tests a handler against
// a sequence of SingleTestCase.
//
// Deprecated: use RunHandlerSequence/RunHandlerIsolated instead.
func TestHandler(
	t *testing.T, handler tui.Handler,
	cases []SingleTestCase, w *term.StringWriter,
) {
	var err error

	for i, tcase := range cases {
		if err = w.Clear(term.Attributes{}); err != nil {
			t.Fatal(err)
		}

		handler.Handle(tcase.Event)

		handler.Draw(w)

		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}

		// for readability, we expected strings are written starting with \n
		expected := strings.TrimLeft(tcase.Expected, "\n")
		assert.Equal(t, expected, w.String(), "test %d: ch <%c> key <%v> ",
			i, tcase.Event.Ch, tcase.Event.Key)
	}
}

// DrawHandler is a helper function that renders the handler into a string.
//
// It can be beneficial for print-debugging your tests:
//
//	fmt.Printf("\n%s", handlertest.DrawHandler(vi, 20, 20))
//
// Extracted from [handleTestCase].
func DrawHandler(handler tui.Handler, width, height int) string {
	w := term.NewStringWriter(width, height)
	handler.Draw(w)
	cursor, _, ok := handler.Cursor()
	if ok {
		w.SetCursor(cursor)
	}
	w.Flush()
	out := w.String()
	return out
}

// RunHandlerIsolated is a helper function that runs a set of test cases,
// by calling fn for every test case, resizing the given handler with
// the given width and height and comparing the results of Draw against it.
func RunHandlerIsolated(
	t *testing.T, fn func(t *testing.T) tui.Handler, width, height int,
	cases []SequenceTestCase,
) {
	writer := term.NewStringWriter(width, height)

	for i, tcase := range cases {
		t.Run(fmt.Sprintf("test case %d (input: %s)", i, tcase.InputSequence),
			func(t *testing.T) {
				handler := fn(t)
				handler.Resize(width, height)
				runTestCase(t, i, writer, handler, tcase)
			})
	}
}

// RunHandlerSequenceWriter is a helper function that runs a set of test cases
// against the given handler in sequence.
func RunHandlerSequenceWriter(
	t *testing.T, writer Writer, handler tui.Handler, width, height int,
	cases []SequenceTestCase,
) {
	handler.Resize(width, height)
	for i, tcase := range cases {
		runTestCase(t, i, writer, handler, tcase)
	}
}

// RunHandlerSequence is a helper function that runs a set of test cases
// against the given handler in sequence, with a StringWriter set with the given
// width and height.
func RunHandlerSequence(
	t *testing.T, handler tui.Handler, width, height int,
	cases []SequenceTestCase,
) {
	writer := term.NewStringWriter(width, height)
	RunHandlerSequenceWriter(t, writer, handler, width, height, cases)
}

func runTestCase(
	t *testing.T, i int, w Writer,
	h tui.Handler, tcase SequenceTestCase,
) {
	err := w.Clear(term.Attributes{Fg: 0, Bg: 0})
	require.NoError(t, err)

	keys, err := term.ParseKeys(tcase.InputSequence)
	require.NoError(t, err)

	for _, key := range keys {
		h.Handle(term.Event{Ch: key.Ch, Mod: key.Mod, Key: key.Key, Type: term.EventKey})
	}
	h.Draw(w)

	cursor, _, ok := h.Cursor()
	if ok {
		w.SetCursor(cursor)
	}

	err = w.Flush()
	require.NoError(t, err)

	out := w.String()
	assert.Equal(t, tcase.Expected, out, "test case %d (input: %s)", i, tcase.InputSequence)
}

// Deprecated: use runTestCase
func handleTestCase(
	t *testing.T, i int, w *term.StringWriter,
	h tui.Handler, tcase SequenceTestCase,
	width, height int,
) {
	err := w.Clear(term.Attributes{Fg: 0, Bg: 0})
	require.NoError(t, err)

	var shouldSleep time.Duration
	var escapeNext bool
	for _, r := range tcase.InputSequence {
		if escapeNext {
			escapeNext = false
			h.Handle(term.Event{Ch: r, Type: term.EventKey})
			continue
		}
		switch r {
		case ':':
			h.Handle(term.Event{Mod: term.ModCtrl, Ch: '\\', Type: term.EventKey})
		case '`':
			h.Handle(term.Event{Mod: term.ModCtrl, Ch: 'v', Type: term.EventKey})
		case '_':
			shouldSleep += 100
		case ' ':
			h.Handle(term.Event{Key: term.KeySpace, Type: term.EventKey})
		case '🎉':
			h.Handle(term.Event{Mod: term.ModCtrl, Key: term.KeySpace, Type: term.EventKey})
		case '💋':
			h.Handle(term.Event{Mod: term.ModCtrl, Ch: 'c', Type: term.EventKey})
		case '^':
			h.Handle(term.Event{Key: term.KeyBackspace, Type: term.EventKey})
		case '#':
			h.Handle(term.Event{Mod: term.ModCtrl, Ch: 'h', Type: term.EventKey})
		case '$':
			h.Handle(term.Event{Mod: term.ModCtrl, Ch: 'l', Type: term.EventKey})
		case '>':
			h.Handle(term.Event{Key: term.KeyEnter, Type: term.EventKey})
		case '<':
			h.Handle(term.Event{Key: term.KeyEsc, Type: term.EventKey})
		case '✌':
			h.Handle(term.Event{Key: term.KeyTab, Type: term.EventKey})
		case '⬇':
			h.Handle(term.Event{Key: term.KeyArrowDown, Type: term.EventKey})
		case '⬆':
			h.Handle(term.Event{Key: term.KeyArrowUp, Type: term.EventKey})
		case '\\':
			escapeNext = true
		default:
			h.Handle(term.Event{Ch: r, Type: term.EventKey})
		}
	}

	// this is a hack for async handlers
	if shouldSleep != 0 {
		// wait until all events have been dispatched
		time.Sleep(shouldSleep * time.Millisecond)
		// force a draw and wait for the draw response to arrive
		h.Draw(term.NewStringWriter(width, height))
		time.Sleep(shouldSleep * time.Millisecond)
	}
	h.Draw(w)

	cursor, _, ok := h.Cursor()
	if ok {
		w.SetCursor(cursor)
	}

	err = w.Flush()
	require.NoError(t, err)

	out := w.String()
	assert.Equal(t, tcase.Expected, out, "test case %d (input: %s)", i, tcase.InputSequence)
}
