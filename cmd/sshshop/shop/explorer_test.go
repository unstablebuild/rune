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

package shop

import (
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/handler/handlertest"
)

func newTestPageExplorer(t *testing.T) *pageExplorerHandler {
	t.Helper()
	r := NewRoot(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = r.Close() })
	r.Resize(testWidth, testHeight)
	feed(t, r, "<tab>j")
	h, ok := r.fileExplorer.(*pageExplorerHandler)
	require.True(t, ok)
	require.Equal(t, 1, h.selected)
	return h
}

func TestPageExplorerHandlerPointerLeaves(t *testing.T) {
	t.Parallel()
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		h := newTestPageExplorer(t)
		handlertest.RunPointerLeaves(t, h, size[0], size[1])
	}
}

func TestPageExplorerHandlerIgnoresPointerLeave(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		at   term.Coordinates
	}{
		{name: "left", at: term.Coordinates{X: -1, Y: 1}},
		{name: "below", at: term.Coordinates{X: 5, Y: 15}},
		{name: "far", at: term.Coordinates{X: 400, Y: -400}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newTestPageExplorer(t)
			h.Resize(60, 15)
			before := handlertest.DrawHandler(h, 60, 15)
			exit, handled := h.Handle(term.Event{
				Type: term.EventMouse, MouseX: tt.at.X, MouseY: tt.at.Y,
			})
			require.False(t, exit)
			require.False(t, handled)
			require.Equal(t, 1, h.selected, "a leave must not move the selection")
			require.Equal(t, before, handlertest.DrawHandler(h, 60, 15))
		})
	}
}
