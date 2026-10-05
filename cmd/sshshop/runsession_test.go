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

package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// fakeScreen mirrors the SDK's RunScreen tests. We intentionally avoid
// tcell.SimulationScreen because its PostEvent implementation recurses.
type fakeScreen struct {
	mu     sync.Mutex
	width  int
	height int
	evch   chan term.Event
}

func newFakeScreen(w, h int) *fakeScreen {
	return &fakeScreen{width: w, height: h, evch: make(chan term.Event, 16)}
}

func (s *fakeScreen) SetContent(int, int, rune, []rune, uint8, term.Style) {}
func (s *fakeScreen) UnionStyle(int, int, term.Style)                      {}
func (s *fakeScreen) Fill(rune, term.Style)                                {}
func (s *fakeScreen) ShowCursor(int, int)                                  {}
func (s *fakeScreen) HideCursor()                                          {}
func (s *fakeScreen) SetCursorStyle(term.CursorStyle)                      {}
func (s *fakeScreen) Size() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.width, s.height
}
func (s *fakeScreen) Show()                   {}
func (s *fakeScreen) Poll() <-chan term.Event { return s.evch }
func (s *fakeScreen) PostEvent(ev term.Event) error {
	select {
	case s.evch <- ev:
		return nil
	default:
		return term.ErrEventQFull
	}
}
func (s *fakeScreen) Bell() {}

type drawCounterHandler struct{ draws atomic.Int32 }

func (h *drawCounterHandler) Resize(int, int)  {}
func (h *drawCounterHandler) Draw(term.Writer) { h.draws.Add(1) }
func (h *drawCounterHandler) Handle(ev term.Event) (bool, bool) {
	return ev.Type == term.EventKey && ev.Key == term.KeyEsc, true
}
func (h *drawCounterHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}
func (h *drawCounterHandler) Selection() (string, bool) { return "", false }

func TestWriterInterrupterPublishesMultipleInterrupts(t *testing.T) {
	s := newFakeScreen(20, 8)

	h := &drawCounterHandler{}
	w := term.NewScreenWriter(s)
	done := make(chan error, 1)
	go func() { done <- tui.RunWriter(h, w) }()

	interrupter := writerInterrupter{w: w}

	require.Eventually(t, func() bool {
		return h.draws.Load() >= 1
	}, time.Second, 20*time.Millisecond)
	base := h.draws.Load()

	require.NoError(t, interrupter.Interrupt(context.Background()))
	require.Eventually(t, func() bool {
		return h.draws.Load() > base
	}, time.Second, 20*time.Millisecond)
	afterFirst := h.draws.Load()

	require.NoError(t, interrupter.Interrupt(context.Background()))
	require.Eventually(t, func() bool {
		return h.draws.Load() > afterFirst
	}, time.Second, 20*time.Millisecond,
		"second interrupt should schedule another redraw without any keypress")

	require.True(t, w.PublishEvent(term.Event{Type: term.EventKey, Key: term.KeyEsc}))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("RunScreen did not exit after Esc")
	}
}
