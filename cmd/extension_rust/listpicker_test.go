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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/handler/handlertest"
)

func newTestListPicker() (*listPicker, chan int) {
	ch := make(chan int, 1)
	return newListPicker([]string{
		"Run cargo test",
		"Run cargo check",
		"Run cargo build --release",
		"Debug main",
		"Run doctest for `Parser::new`",
	}, ch), ch
}

func TestListPickerPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		p, _ := newTestListPicker()
		handlertest.RunPointerLeaves(t, p, size[0], size[1])
	}
}

func TestListPickerPointerLeaveIsInert(t *testing.T) {
	t.Parallel()
	var ch chan int
	requirePointerLeaveInert(t, func(*testing.T) tui.Handler {
		var p *listPicker
		p, ch = newTestListPicker()
		p.list.FocusDown()
		return p
	}, 60, 15, func(t *testing.T) {
		assert.Empty(t, ch, "a leave neither picks nor cancels")
	})
}

// pointerFrame is everything a handler shows: its drawn cells, its
// cursor and its selection.
type pointerFrame struct {
	cells     []term.Cell
	cursor    term.Coordinates
	showCur   bool
	selection string
	selected  bool
}

func snapshotPointerFrame(h tui.Handler, width, height int) pointerFrame {
	w := term.NewStringWriter(width, height)
	h.Draw(w)
	f := pointerFrame{cells: w.Cells()}
	f.cursor, _, f.showCur = h.Cursor()
	f.selection, f.selected = h.Selection()
	return f
}

// requirePointerLeaveInert asserts that a leave neither exits newH's
// handler nor changes what it shows: after a hover the handler shows
// what it did before the pointer arrived, and after a click, wheel or
// press it keeps showing what that gesture left. check, if set, runs
// after each leave for handler-specific state.
func requirePointerLeaveInert(
	t *testing.T, newH func(*testing.T) tui.Handler, width, height int,
	check func(*testing.T),
) {
	t.Helper()
	mid := term.Coordinates{X: width / 2, Y: height / 2}
	mouse := func(key term.Key) term.Event {
		return term.Event{
			Type: term.EventMouse, Key: key, MouseX: mid.X, MouseY: mid.Y,
			Context: term.ContextWithPointer(context.Background(), new(term.Pointer)),
		}
	}
	tests := []struct {
		name    string
		gesture []term.Event
		// restores reports that the leave must restore the frame drawn
		// before the gesture rather than keep the one it left.
		restores bool
	}{
		{name: "hover", gesture: []term.Event{mouse(0)}, restores: true},
		{name: "click", gesture: []term.Event{
			mouse(0), mouse(term.MouseLeft), mouse(term.MouseRelease),
		}},
		{name: "wheel", gesture: []term.Event{mouse(term.MouseWheelDown)}},
		{name: "press", gesture: []term.Event{mouse(term.MouseLeft)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newH(t)
			h.Resize(width, height)
			before := snapshotPointerFrame(h, width, height)
			for _, ev := range tt.gesture {
				h.Handle(ev)
			}
			if !tt.restores {
				before = snapshotPointerFrame(h, width, height)
			}
			for _, out := range []term.Coordinates{
				{X: -1, Y: mid.Y}, {X: width, Y: mid.Y}, {X: mid.X, Y: -1},
				{X: mid.X, Y: height}, {X: 4 * width, Y: 4 * height},
			} {
				exit, _ := h.Handle(term.Event{Type: term.EventMouse, MouseX: out.X, MouseY: out.Y})
				require.False(t, exit, "a leave to %v exits", out)
				assert.Equal(t, before, snapshotPointerFrame(h, width, height),
					"a leave to %v changes what is shown", out)
				if check != nil {
					check(t)
				}
			}
		})
	}
}
