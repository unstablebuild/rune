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

package text

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestMouseDelegateSelectWordAtInvertedScrollAboveBuffer(t *testing.T) {
	scroll := component.NewScroll(cell.NewBuffer())
	scroll.InvertOffset = true
	_, _ = scroll.Buffer().ReadFrom(strings.NewReader("hello\n"))
	// Window is much taller than the populated buffer rows, so
	// convertedOffset().Y is negative and clicks near the top of the
	// window translate to negative scroll coordinates.
	scroll.Resize(80, 24)

	cur := NewCursor(scroll, nil)
	delegate := CursorMouseDelegate(cur)

	assert.NotPanics(t, func() {
		delegate.SelectWordAt(term.Coordinates{X: 0, Y: 0})
	})
	_, hasSelection := cur.SelectionMode()
	assert.False(t, hasSelection,
		"clicks above the buffer must not start a selection")
}

func TestScrollWordAtOutOfBounds(t *testing.T) {
	scroll := component.NewScroll(cell.NewBuffer())
	_, _ = scroll.Buffer().ReadFrom(strings.NewReader("hello world\n"))
	scroll.Resize(80, 24)

	cases := []struct {
		name string
		at   term.Coordinates
	}{
		{"negative Y", term.Coordinates{X: 0, Y: -3}},
		{"negative X", term.Coordinates{X: -1, Y: 0}},
		{"negative both", term.Coordinates{X: -2, Y: -5}},
		{"Y past last row", term.Coordinates{X: 0, Y: 1 << 20}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotPanics(t, func() {
				_, _, got := scroll.WordAt(tc.at)
				assert.Equal(t, "", got)
			})
		})
	}
}

func TestMouseDelegateSelectWordAtWiderThanWindow(t *testing.T) {
	const content = "aa bbbbbbbbbbbbbbbbbb"
	tests := []struct {
		name    string
		cursorX int
		clickX  int
	}{
		{"viewport scrolled to end of line", len(content), 4},
		{"viewport scrolled mid word", 15, 2},
		{"viewport at start of line", 0, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scroll := component.NewScroll(cell.NewBuffer())
			_, _ = scroll.Buffer().ReadFrom(strings.NewReader(content))
			scroll.Resize(8, 1)

			cur := NewCursor(scroll, nil)
			delegate := CursorMouseDelegate(cur)
			cur.MoveToScroll(term.Coordinates{X: tt.cursorX})

			delegate.SelectWordAt(term.Coordinates{X: tt.clickX})

			assert.Equal(t, "bbbbbbbbbbbbbbbbbb", cur.Selection())
			assert.Equal(t, term.Coordinates{X: len(content)}, cur.CursorAtScroll())
		})
	}
}

func TestMouseDelegatePointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ width, height int }{{40, 10}, {1, 1}} {
		handlertest.RunPointerLeaves(t, newPointerEditor(t, workspaceapi.URI{}),
			size.width, size.height)
	}
	requirePointerLeavesKeepState(t, newPointerEditor(t, workspaceapi.URI{}), 40, 10,
		term.Coordinates{X: 3, Y: 2}, term.Coordinates{X: 12, Y: 4})
}

// pointerContent has more rows and columns than the windows the pointer
// leave tests draw it in, and wide runes whose cells a pointer can split.
const pointerContent = `package main

// 日本語のコメント と 絵文字 🙂🎉 in a line wider than any window drawn here
import (
	"fmt"
	"strings"
)

func main() {
	s := "日本語🙂"
	for i := range 3 {
		fmt.Println(strings.Repeat(s, i), "a long line that goes past the right edge")
	}
}
`

// pointerEditor is a Handler that handles the mouse the way the editors
// do: a mouse.Mouse drives its Cursor through CursorMouseDelegate.
type pointerEditor struct {
	indentTestHandler
	cursor *Cursor
	mouse  *mouse.Mouse
}

func newPointerEditor(t *testing.T, uri workspaceapi.URI) *pointerEditor {
	c := setupCursorContent(t, 40, 10, pointerContent, false)
	return &pointerEditor{
		indentTestHandler: indentTestHandler{uri: uri},
		cursor:            c,
		mouse:             mouse.New(CursorMouseDelegate(c)),
	}
}

func (e *pointerEditor) Handle(ev term.Event) (bool, bool) { return e.mouse.Handle(ev) }
func (e *pointerEditor) Draw(w term.Writer)                { e.cursor.scroll.Draw(w) }
func (e *pointerEditor) Resize(width, height int)          { e.cursor.scroll.Resize(width, height) }
func (e *pointerEditor) CursorAtScroll() term.Coordinates  { return e.cursor.CursorAtScroll() }
func (e *pointerEditor) CellView() cell.View               { return e.cursor.buffer().View() }
func (e *pointerEditor) CellEditor() cell.Editor           { return e.cursor.buffer().Editor() }

func (e *pointerEditor) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return e.cursor.Coordinates(), term.CursorStyleDefault, true
}

func (e *pointerEditor) Selection() (string, bool) {
	s := e.cursor.Selection()
	return s, s != ""
}

// pointerState is what a handler draws and reports to the frame.
type pointerState struct {
	Screen    string
	Cursor    term.Coordinates
	Style     term.CursorStyle
	Shown     bool
	Selection string
	Selected  bool
}

func pointerStateOf(h tui.Handler, width, height int) pointerState {
	w := term.NewStringWriter(width, height)
	h.Draw(w)
	_ = w.Flush()
	s := pointerState{Screen: w.String()}
	s.Cursor, s.Style, s.Shown = h.Cursor()
	s.Selection, s.Selected = h.Selection()
	return s
}

// requirePointerLeavesKeepState selects from press to drag in h, sized
// width by height, then requires that the pointer leaving h to any side
// changes nothing h draws or reports, both once the button is released
// and while it is still held.
func requirePointerLeavesKeepState(
	t *testing.T, h tui.Handler, width, height int, press, drag term.Coordinates,
) {
	t.Helper()
	h.Resize(width, height)
	ctx := term.ContextWithPointer(context.Background(), new(term.Pointer))
	mouseAt := func(key term.Key, at term.Coordinates) term.Event {
		return term.Event{
			Type: term.EventMouse, Key: key, MouseX: at.X, MouseY: at.Y, Context: ctx,
		}
	}
	h.Handle(mouseAt(term.MouseLeft, press))
	h.Handle(mouseAt(term.MouseLeft, drag))
	h.Handle(mouseAt(term.MouseRelease, drag))
	requireLeavesKeepState(t, h, width, height, "released")
	h.Handle(mouseAt(term.MouseLeft, press))
	requireLeavesKeepState(t, h, width, height, "held")
}

func requireLeavesKeepState(t *testing.T, h tui.Handler, width, height int, phase string) {
	t.Helper()
	far := 4*max(width, height) + 100
	for _, out := range []term.Coordinates{
		{X: -1, Y: height / 2}, {X: width, Y: height / 2},
		{X: width / 2, Y: -1}, {X: width / 2, Y: height},
		{X: -1, Y: -1}, {X: width, Y: height}, {X: -far, Y: -far}, {X: far, Y: far},
	} {
		before := pointerStateOf(h, width, height)
		h.Handle(term.Event{Type: term.EventMouse, MouseX: out.X, MouseY: out.Y})
		require.Equal(t, before, pointerStateOf(h, width, height),
			"button %s, then a leave to %v", phase, out)
	}
}
