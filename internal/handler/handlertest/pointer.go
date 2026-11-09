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
	"context"
	"fmt"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// RunPointerLeaves drives handler, sized width by height, through the
// motions a parent that routes mouse events by position sends it when
// the pointer moves off it: a plain motion with no Context, at a position
// outside the handler, next to it or a screen away on any side. Each leave
// follows a gesture that leaves state behind inside the handler, a hover,
// a click, a drag, a wheel or a press whose release went elsewhere, and
// is followed by the Draw, Cursor and Selection calls of a frame.
// RunPointerLeaves fails t with the events sent if handler panics.
func RunPointerLeaves(t testing.TB, handler tui.Handler, width, height int) {
	t.Helper()
	handler.Resize(width, height)
	w := term.NewStringWriter(max(width, 1), max(height, 1))
	for _, gesture := range pointerGestures(width, height) {
		for _, in := range pointerInside(width, height) {
			for _, out := range pointerOutside(width, height) {
				events := append(gesture.events(in), term.Event{
					Type: term.EventMouse, MouseX: out.X, MouseY: out.Y,
				})
				if msg, ok := handlePointerEvents(handler, w, events); !ok {
					t.Fatalf("%s at %v, then a leave to %v:\n%s",
						gesture.name, in, out, msg)
					return
				}
			}
		}
	}
}

type pointerGesture struct {
	name   string
	events func(at term.Coordinates) []term.Event
}

func pointerGestures(width, height int) []pointerGesture {
	mouse := func(key term.Key, at term.Coordinates) term.Event {
		return term.Event{
			Type: term.EventMouse, Key: key, MouseX: at.X, MouseY: at.Y,
			Context: term.ContextWithPointer(context.Background(), new(term.Pointer)),
		}
	}
	far := term.Coordinates{X: max(width-1, 0), Y: max(height-1, 0)}
	return []pointerGesture{
		{name: "nothing", events: func(term.Coordinates) []term.Event { return nil }},
		{name: "a hover", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouse(0, at)}
		}},
		{name: "a click", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouse(0, at), mouse(term.MouseLeft, at), mouse(term.MouseRelease, at)}
		}},
		{name: "a drag", events: func(at term.Coordinates) []term.Event {
			return []term.Event{
				mouse(term.MouseLeft, at), mouse(term.MouseLeft, far), mouse(term.MouseRelease, far),
			}
		}},
		{name: "a wheel", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouse(term.MouseWheelDown, at), mouse(term.MouseWheelUp, at)}
		}},
		{name: "a press released elsewhere", events: func(at term.Coordinates) []term.Event {
			return []term.Event{mouse(term.MouseLeft, at)}
		}},
	}
}

func pointerInside(width, height int) []term.Coordinates {
	return []term.Coordinates{
		{},
		{X: width / 2, Y: height / 2},
		{X: max(width-1, 0), Y: max(height-1, 0)},
	}
}

func pointerOutside(width, height int) []term.Coordinates {
	far := 4*max(width, height) + 100
	midX, midY := width/2, height/2
	return []term.Coordinates{
		{X: -1, Y: midY}, {X: width, Y: midY}, {X: midX, Y: -1}, {X: midX, Y: height},
		{X: -1, Y: -1}, {X: width, Y: -1}, {X: -1, Y: height}, {X: width, Y: height},
		{X: -far, Y: midY}, {X: far, Y: midY}, {X: midX, Y: -far}, {X: midX, Y: far},
		{X: -far, Y: -far}, {X: far, Y: -far}, {X: -far, Y: far}, {X: far, Y: far},
	}
}

// handlePointerEvents sends events to h, drawing a frame after each, and
// reports the panic it recovered, if any, with the events sent so far.
func handlePointerEvents(h tui.Handler, w *term.StringWriter, events []term.Event) (msg string, ok bool) {
	var sent strings.Builder
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprintf("panic: %v\nafter:\n%s\n%s", r, sent.String(), debug.Stack())
			ok = false
		}
	}()
	for _, ev := range events {
		fmt.Fprintf(&sent, "  key=%d x=%d y=%d\n", ev.Key, ev.MouseX, ev.MouseY)
		_, _ = h.Handle(ev)
		_ = w.Clear(term.Attributes{})
		h.Draw(w)
		_, _, _ = h.Cursor()
		_, _ = h.Selection()
	}
	return "", true
}
