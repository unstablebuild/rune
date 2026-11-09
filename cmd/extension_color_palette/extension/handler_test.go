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

package extension

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestColorPaletteHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, dim := range []bool{false, true} {
		for _, size := range [][2]int{{60, 15}, {1, 1}} {
			handlertest.RunPointerLeaves(t, &colorPaletteHandler{dim: dim}, size[0], size[1])
		}
	}
}

func TestColorPaletteHandlerPointerLeaveIsInert(t *testing.T) {
	t.Parallel()
	const width, height = 60, 15
	mid := term.Coordinates{X: width / 2, Y: height / 2}
	mouse := func(key term.Key) term.Event {
		return term.Event{
			Type: term.EventMouse, Key: key, MouseX: mid.X, MouseY: mid.Y,
			Context: term.ContextWithPointer(context.Background(), new(term.Pointer)),
		}
	}
	draw := func(h *colorPaletteHandler) []term.Cell {
		w := term.NewStringWriter(width, height)
		h.Draw(w)
		return w.Cells()
	}
	tests := []struct {
		name    string
		gesture []term.Event
	}{
		{name: "hover", gesture: []term.Event{mouse(0)}},
		{name: "click", gesture: []term.Event{mouse(0), mouse(term.MouseLeft), mouse(term.MouseRelease)}},
		{name: "wheel", gesture: []term.Event{mouse(term.MouseWheelDown)}},
		{name: "press", gesture: []term.Event{mouse(term.MouseLeft)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := &colorPaletteHandler{dim: true}
			h.Resize(width, height)
			before := draw(h)
			for _, ev := range tt.gesture {
				h.Handle(ev)
			}
			for _, out := range []term.Coordinates{
				{X: -1, Y: mid.Y}, {X: width, Y: mid.Y}, {X: mid.X, Y: -1},
				{X: mid.X, Y: height}, {X: 4 * width, Y: 4 * height},
			} {
				exit, _ := h.Handle(term.Event{Type: term.EventMouse, MouseX: out.X, MouseY: out.Y})
				require.False(t, exit, "a leave to %v closes the palette", out)
				assert.True(t, h.dim, "a leave to %v toggles dimming", out)
				assert.Equal(t, before, draw(h), "a leave to %v changes the palette", out)
			}
		})
	}
}
