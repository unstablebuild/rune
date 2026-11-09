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

package vteparser

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestParsePointerShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want PointerShape
		ok   bool
	}{
		{"pointer", "pointer", true},
		{"Not-Allowed", "not-allowed", true},
		{"context-menu", "context-menu", true},
		{"xterm", "text", true},
		{"hand2", "pointer", true},
		{"hand1", "grab", true},
		{"cross", "cell", true},
		{"fleur", "move", true},
		{"dnd-move", "move", true},
		{"auto", "", false},
		{"", "", false},
		{"nope", "", false},
	}
	for _, tc := range cases {
		shape, ok := ParsePointerShape(tc.name)
		assert.Equal(t, tc.ok, ok, tc.name)
		assert.Equal(t, tc.want, shape, tc.name)
	}
}

func TestPointerShapeShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		shape PointerShape
		want  term.PointerShape
	}{
		{"default", term.PointerDefault},
		{"wait", term.PointerDefault},
		{"crosshair", term.PointerDefault},
		{"text", term.PointerText},
		{"vertical-text", term.PointerText},
		{"pointer", term.PointerLink},
		{"move", term.PointerMove},
		{"all-scroll", term.PointerMove},
		{"grab", term.PointerMove},
		{"grabbing", term.PointerMove},
		{"w-resize", term.PointerResizeEW},
		{"n-resize", term.PointerResizeNS},
		{"sw-resize", term.PointerResizeNESW},
		{"se-resize", term.PointerResizeNWSE},
		{"col-resize", term.PointerColResize},
		{"row-resize", term.PointerRowResize},
		{"no-drop", term.PointerNotAllowed},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want.String(), tc.shape.Shape().String(), string(tc.shape))
	}
}

func TestPointerShapeNamesCoverKittyAndGhostty(t *testing.T) {
	t.Parallel()
	css := []string{
		"alias", "all-scroll", "cell", "col-resize", "context-menu", "copy", "crosshair",
		"default", "e-resize", "ew-resize", "grab", "grabbing", "help", "move", "n-resize",
		"ne-resize", "nesw-resize", "no-drop", "not-allowed", "ns-resize", "nw-resize",
		"nwse-resize", "pointer", "progress", "row-resize", "s-resize", "se-resize",
		"sw-resize", "text", "vertical-text", "w-resize", "wait", "zoom-in", "zoom-out",
	}
	for _, name := range css {
		shape, ok := ParsePointerShape(name)
		assert.True(t, ok, name)
		assert.Equal(t, PointerShape(name), shape)
	}
	x11 := []string{
		"left_ptr", "xterm", "ibeam", "pointing_hand", "hand2", "hand", "question_arrow",
		"whats_this", "clock", "watch", "half-busy", "left_ptr_watch", "tcross", "plus",
		"cross", "fleur", "pointer-move", "dnd-move", "right_side", "top_right_corner",
		"top_left_corner", "top_side", "bottom_right_corner", "bottom_left_corner",
		"bottom_side", "left_side", "sb_h_double_arrow", "split_h", "sb_v_double_arrow",
		"split_v", "size_bdiag", "size-bdiag", "size_fdiag", "size-fdiag", "zoom_in",
		"zoom_out", "dnd-link", "dnd-copy", "forbidden", "crossed_circle", "dnd-no-drop",
		"openhand", "hand1", "closedhand", "dnd-none",
	}
	for _, name := range x11 {
		_, ok := ParsePointerShape(name)
		assert.True(t, ok, name)
	}
}
