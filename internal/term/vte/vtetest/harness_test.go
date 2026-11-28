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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
)

// slowEchoHandler models a shell whose echo lands later than the
// harness' quiescence timeout, which is what a saturated host does to
// the vte integration tests.
type slowEchoHandler struct {
	delay     time.Duration
	interrupt chan struct{}

	mu    sync.Mutex
	typed []rune
}

func (h *slowEchoHandler) Handle(ev term.Event) (exit, handled bool) {
	go debug.CapturePanicReport(func() {
		time.Sleep(h.delay)
		h.mu.Lock()
		h.typed = append(h.typed, ev.Ch)
		h.mu.Unlock()
		h.interrupt <- struct{}{}
	})
	return false, true
}

func (h *slowEchoHandler) Draw(w term.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for x, r := range h.typed {
		w.SetCell(term.Coordinates{X: x}, term.Cell{Ch: r, Width: 1, Bytes: 1})
	}
}

func (h *slowEchoHandler) Resize(int, int)           {}
func (h *slowEchoHandler) Selection() (string, bool) { return "", false }
func (h *slowEchoHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func TestHandleTestCaseConvergesOnLateEcho(t *testing.T) {
	const (
		drawTimeout   = 10 * time.Millisecond
		width, height = 4, 1
	)
	h := &slowEchoHandler{
		delay:     8 * drawTimeout,
		interrupt: make(chan struct{}, 8),
	}
	cases := []Case{{
		InputSequence: "ab",
		Expected:      "ab" + strings.Repeat(" ", width-2),
	}}

	settled := t.Run("settle", func(t *testing.T) {
		TestCases(t, h, width, height, drawTimeout, h.interrupt, cases)
	})
	assert.True(t, settled,
		"harness must converge on an echo slower than drawTimeout")
}

// unsettledHandler asks for a redraw on every key, and records, for each,
// how many of those requests the harness had yet to read: it reads them
// all while it waits for the handler to settle.
type unsettledHandler struct {
	interrupt chan struct{}
	keys      []rune
	unread    []int
}

func (h *unsettledHandler) Handle(ev term.Event) (exit, handled bool) {
	h.keys = append(h.keys, ev.Ch)
	h.unread = append(h.unread, len(h.interrupt))
	h.interrupt <- struct{}{}
	return false, true
}

func (h *unsettledHandler) Draw(term.Writer)          {}
func (h *unsettledHandler) Resize(int, int)           {}
func (h *unsettledHandler) Selection() (string, bool) { return "", false }
func (h *unsettledHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}

func TestHandleTestCaseSettlesBetweenKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		input    string
		wantKeys string
		// wantUnread is, for each key, how many keys before it the
		// harness pressed without the handler settling since.
		wantUnread []int
	}{
		{
			name: "settles after every key", input: "abc",
			wantKeys: "abc", wantUnread: []int{0, 0, 0},
		},
		{
			name: "presses the keys after ⏩ without settling", input: "a⏩bcd",
			wantKeys: "abcd", wantUnread: []int{0, 0, 1, 2},
		},
		{
			name: "settles after an escaped key", input: `\<a\>b`,
			wantKeys: "<a>b", wantUnread: []int{0, 0, 0, 0},
		},
		{
			name: "presses an escaped key after ⏩ without settling", input: `⏩\<a\\`,
			wantKeys: `<a\`, wantUnread: []int{0, 1, 2},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			h := &unsettledHandler{interrupt: make(chan struct{}, 8)}
			TestCases(t, h, 1, 1, 5*time.Millisecond, h.interrupt,
				[]Case{{InputSequence: tc.input, Expected: " "}})
			assert.Equal(t, tc.wantKeys, string(h.keys))
			assert.Equal(t, tc.wantUnread, h.unread)
			assert.Empty(t, h.interrupt, "the harness settles after the last key")
		})
	}
}
