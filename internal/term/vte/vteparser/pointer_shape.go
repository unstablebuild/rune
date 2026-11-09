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
	"strings"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// PointerShape is a mouse pointer shape a program sets with OSC 22, as the
// CSS cursor name its name stands for. The empty shape lets the terminal
// choose.
type PointerShape string

// Shape returns the closest shape the terminal can show for s.
func (s PointerShape) Shape() term.PointerShape {
	return pointerShapes[s]
}

// ParsePointerShape returns the shape an OSC 22 name stands for, case
// insensitively. Besides the CSS cursor names, it accepts the X11 cursor
// font names kitty and Ghostty take as aliases; kitty's mapping wins where
// the two disagree.
func ParsePointerShape(name string) (PointerShape, bool) {
	shape := PointerShape(strings.ToLower(name))
	if _, ok := pointerShapes[shape]; ok {
		return shape, true
	}
	shape, ok := pointerShapeAliases[string(shape)]
	return shape, ok
}

// pointerShapes maps every CSS cursor name to the closest shape the
// terminal has.
var pointerShapes = map[PointerShape]term.PointerShape{
	"default":       term.PointerDefault,
	"context-menu":  term.PointerDefault,
	"help":          term.PointerDefault,
	"progress":      term.PointerDefault,
	"wait":          term.PointerDefault,
	"cell":          term.PointerDefault,
	"crosshair":     term.PointerDefault,
	"alias":         term.PointerDefault,
	"copy":          term.PointerDefault,
	"zoom-in":       term.PointerDefault,
	"zoom-out":      term.PointerDefault,
	"text":          term.PointerText,
	"vertical-text": term.PointerText,
	"pointer":       term.PointerLink,
	"move":          term.PointerMove,
	"all-scroll":    term.PointerMove,
	"grab":          term.PointerMove,
	"grabbing":      term.PointerMove,
	"e-resize":      term.PointerResizeEW,
	"w-resize":      term.PointerResizeEW,
	"ew-resize":     term.PointerResizeEW,
	"n-resize":      term.PointerResizeNS,
	"s-resize":      term.PointerResizeNS,
	"ns-resize":     term.PointerResizeNS,
	"ne-resize":     term.PointerResizeNESW,
	"sw-resize":     term.PointerResizeNESW,
	"nesw-resize":   term.PointerResizeNESW,
	"nw-resize":     term.PointerResizeNWSE,
	"se-resize":     term.PointerResizeNWSE,
	"nwse-resize":   term.PointerResizeNWSE,
	"col-resize":    term.PointerColResize,
	"row-resize":    term.PointerRowResize,
	"no-drop":       term.PointerNotAllowed,
	"not-allowed":   term.PointerNotAllowed,
}

var pointerShapeAliases = map[string]PointerShape{
	"left_ptr":            "default",
	"question_arrow":      "help",
	"whats_this":          "help",
	"half-busy":           "progress",
	"left_ptr_watch":      "progress",
	"clock":               "wait",
	"watch":               "wait",
	"plus":                "cell",
	"cross":               "cell",
	"tcross":              "crosshair",
	"dnd-link":            "alias",
	"dnd-copy":            "copy",
	"zoom_in":             "zoom-in",
	"zoom_out":            "zoom-out",
	"xterm":               "text",
	"ibeam":               "text",
	"pointing_hand":       "pointer",
	"hand":                "pointer",
	"hand2":               "pointer",
	"fleur":               "move",
	"pointer-move":        "move",
	"dnd-move":            "move",
	"openhand":            "grab",
	"hand1":               "grab",
	"closedhand":          "grabbing",
	"dnd-none":            "grabbing",
	"right_side":          "e-resize",
	"left_side":           "w-resize",
	"sb_h_double_arrow":   "ew-resize",
	"split_h":             "ew-resize",
	"top_side":            "n-resize",
	"bottom_side":         "s-resize",
	"sb_v_double_arrow":   "ns-resize",
	"split_v":             "ns-resize",
	"top_right_corner":    "ne-resize",
	"bottom_left_corner":  "sw-resize",
	"size_bdiag":          "nesw-resize",
	"size-bdiag":          "nesw-resize",
	"top_left_corner":     "nw-resize",
	"bottom_right_corner": "se-resize",
	"size_fdiag":          "nwse-resize",
	"size-fdiag":          "nwse-resize",
	"forbidden":           "not-allowed",
	"crossed_circle":      "not-allowed",
	"dnd-no-drop":         "no-drop",
}
