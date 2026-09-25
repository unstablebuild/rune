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

package term

import (
	"context"
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
)

type bwWriter struct {
	w term.Writer
	// defaultFg resolves a ColorDefault foreground before grayscaling so
	// unfocused body text tracks the theme's default foreground instead of
	// keeping its native hue.
	defaultFg term.Color
}

// BWWriter returns a Writer that converts each cell's foreground and
// background colors to grayscale, stripping all color while preserving
// relative brightness. A ColorDefault foreground is resolved to defaultFg
// before grayscaling; a ColorDefault background is left untouched so the
// terminal keeps rendering it natively.
func BWWriter(w term.Writer, defaultAttr term.Attributes) term.Writer {
	return bwWriter{w: w, defaultFg: defaultAttr.Fg}
}

func (w bwWriter) SetCell(pos term.Coordinates, c term.Cell) {
	c.Fg = grayscaleFg(c.Fg, w.defaultFg)
	c.Bg = grayscale(c.Bg)
	w.w.SetCell(pos, c)
}

func (w bwWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	// A ColorDefault foreground in a union overlay means "leave the
	// foreground unchanged". Resolving it to defaultFg here would repaint
	// cells the caller meant to leave alone (e.g. the aux bar's
	// background-only overlay would overwrite the gray line numbers).
	attr.Fg = grayscale(attr.Fg)
	attr.Bg = grayscale(attr.Bg)
	w.w.UnionAttributes(pos, attr)
}

func (w bwWriter) Context() context.Context {
	return w.w.Context()
}

func (w bwWriter) DrawImage(img term.Image) bool {
	return w.w.DrawImage(img)
}

// grayscaleFg resolves a ColorDefault foreground to defaultFg before
// grayscaling so unfocused body text follows the theme default rather
// than keeping its native hue. It is only appropriate for SetCell, where
// every cell carries a concrete foreground; union overlays must leave a
// ColorDefault foreground untouched.
func grayscaleFg(c, defaultFg term.Color) term.Color {
	if c == term.ColorDefault {
		c = defaultFg
	}
	return grayscale(c)
}

// grayscale returns the ITU-R BT.601 luminance gray of c. Colors that
// are the terminal default or not RGB-expressible are returned unchanged
// so the terminal keeps rendering them natively.
func grayscale(c term.Color) term.Color {
	if c == term.ColorDefault {
		return c
	}
	if c.Hex() < 0 {
		return c
	}
	r, g, b := c.RGB()
	lum := int32(math.Round(0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)))
	return term.NewRGBColor(lum, lum, lum)
}
