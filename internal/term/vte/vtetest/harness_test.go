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
