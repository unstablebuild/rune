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

package helix

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// TestMouse pins click and drag, which is the only way into select mode
// that does not go through v.
func TestMouse(t *testing.T) {
	click := func(x, y int) term.Event {
		return term.Event{Type: term.EventMouse, Key: term.MouseLeft,
			MouseX: x, MouseY: y}
	}
	release := func(x, y int) term.Event {
		return term.Event{Type: term.EventMouse, Key: term.MouseRelease,
			MouseX: x, MouseY: y}
	}

	t.Run("a click moves the caret", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{})
		send(t, hx, click(2, 1), release(2, 1))
		assert.Equal(t, term.Coordinates{X: 2, Y: 1}, hx.CursorAtScroll())
		assert.False(t, hx.IsSelectMode())
	})

	t.Run("a drag enters select mode", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one\ntwo\nthree", term.Coordinates{})
		send(t, hx, click(0, 0), click(2, 0), release(2, 0))
		assert.True(t, hx.IsSelectMode(), "a drag leaves the editor extending")
		assert.Equal(t, "one", sel(t, hx))
	})

	t.Run("a following motion extends the drag selection", func(t *testing.T) {
		hx, _, _ := newHelix(t, "one two", term.Coordinates{})
		send(t, hx, click(0, 0), click(2, 0), release(2, 0))
		send(t, hx, key('l'))
		assert.Equal(t, "one ", sel(t, hx))
	})

	t.Run("a wheel event is handled", func(t *testing.T) {
		hx, _, _ := newHelix(t, strings.Repeat("line\n", 100), term.Coordinates{})
		_, handled := hx.Handle(term.Event{Type: term.EventMouse,
			Key: term.MouseWheelDown})
		assert.True(t, handled)
	})
}

// TestMouseInInsertMode pins the click path that bypasses the selection
// delegate.
func TestMouseInInsertMode(t *testing.T) {
	hx, buf, _ := newHelix(t, "one\ntwo", term.Coordinates{})
	send(t, hx, key('i'))
	require.True(t, hx.IsEditMode())

	send(t, hx, term.Event{Type: term.EventMouse, Key: term.MouseLeft,
		MouseX: 1, MouseY: 1})
	send(t, hx, term.Event{Type: term.EventMouse, Key: term.MouseRelease,
		MouseX: 1, MouseY: 1})
	assert.True(t, hx.IsEditMode(), "a click does not leave insert mode")
	send(t, hx, key('X'))
	assert.Equal(t, "one\ntXwo", buf.String())
}
