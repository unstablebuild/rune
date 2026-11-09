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

package gui

import (
	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// pointer is the single owner of the window's cursor shape. It shows the
// shape handlers set on the term.Pointer of the most recent mouse event,
// unless the link scanner is pointing at a link. Only the event loop
// calls its methods; the term.Pointer slots may be written from anywhere,
// and sync shows what was written on every tick.
type pointer struct {
	// setShape is the window's cursor-shape setter, replaced in tests.
	setShape func(ebiten.CursorShapeType)
	latest   *term.Pointer
	link     bool
	shown    ebiten.CursorShapeType
}

func newPointer() *pointer {
	return &pointer{setShape: ebiten.SetCursorShape}
}

// next returns the slot of a new mouse event, which supersedes every
// earlier one. The caller must sync once the event is dispatched.
func (p *pointer) next() *term.Pointer {
	p.latest = new(term.Pointer)
	return p.latest
}

// setLink overrides the handlers' shape with the link cursor while over
// is true.
func (p *pointer) setLink(over bool) {
	p.link = over
	p.sync()
}

// sync pushes the shape that should be showing to the window if it is
// not already showing.
func (p *pointer) sync() {
	shape := ebiten.CursorShapeDefault
	switch {
	case p.link:
		shape = ebiten.CursorShapePointer
	case p.latest != nil:
		s, _ := p.latest.Shape()
		shape = cursorShape(s)
	}
	if shape == p.shown {
		return
	}
	p.shown = shape
	p.setShape(shape)
}

// cursorShape maps s to the closest cursor the window system has; there
// are no dedicated column and row resize cursors.
func cursorShape(s term.PointerShape) ebiten.CursorShapeType {
	switch s {
	case term.PointerText:
		return ebiten.CursorShapeText
	case term.PointerLink:
		return ebiten.CursorShapePointer
	case term.PointerMove:
		return ebiten.CursorShapeMove
	case term.PointerResizeEW, term.PointerColResize:
		return ebiten.CursorShapeEWResize
	case term.PointerResizeNS, term.PointerRowResize:
		return ebiten.CursorShapeNSResize
	case term.PointerResizeNESW:
		return ebiten.CursorShapeNESWResize
	case term.PointerResizeNWSE:
		return ebiten.CursorShapeNWSEResize
	case term.PointerNotAllowed:
		return ebiten.CursorShapeNotAllowed
	default:
		return ebiten.CursorShapeDefault
	}
}
