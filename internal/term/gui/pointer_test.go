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
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// recordingPointer returns a pointer capturing every shape it pushes to
// the window.
func recordingPointer() (*pointer, *[]ebiten.CursorShapeType) {
	var shapes []ebiten.CursorShapeType
	p := newPointer()
	p.setShape = func(s ebiten.CursorShapeType) { shapes = append(shapes, s) }
	return p, &shapes
}

func TestCursorShape(t *testing.T) {
	tests := []struct {
		shape    term.PointerShape
		expected ebiten.CursorShapeType
	}{
		{shape: term.PointerDefault, expected: ebiten.CursorShapeDefault},
		{shape: term.PointerText, expected: ebiten.CursorShapeText},
		{shape: term.PointerLink, expected: ebiten.CursorShapePointer},
		{shape: term.PointerMove, expected: ebiten.CursorShapeMove},
		{shape: term.PointerResizeEW, expected: ebiten.CursorShapeEWResize},
		{shape: term.PointerResizeNS, expected: ebiten.CursorShapeNSResize},
		{shape: term.PointerResizeNESW, expected: ebiten.CursorShapeNESWResize},
		{shape: term.PointerResizeNWSE, expected: ebiten.CursorShapeNWSEResize},
		{shape: term.PointerColResize, expected: ebiten.CursorShapeEWResize},
		{shape: term.PointerRowResize, expected: ebiten.CursorShapeNSResize},
		{shape: term.PointerNotAllowed, expected: ebiten.CursorShapeNotAllowed},
		{shape: term.PointerShape(99), expected: ebiten.CursorShapeDefault},
	}
	for _, tt := range tests {
		t.Run(tt.shape.String(), func(t *testing.T) {
			assert.Equal(t, tt.expected, cursorShape(tt.shape))
		})
	}
}

func TestPointerSync(t *testing.T) {
	p, shapes := recordingPointer()

	p.sync()
	assert.Empty(t, *shapes, "the window starts with the default cursor")

	p.next().SetShape(term.PointerLink)
	p.sync()
	p.next().SetShape(term.PointerLink)
	p.sync()
	assert.Equal(t, []ebiten.CursorShapeType{ebiten.CursorShapePointer}, *shapes,
		"the same shape is pushed once")

	older := p.next()
	p.sync()
	assert.Equal(t, []ebiten.CursorShapeType{
		ebiten.CursorShapePointer, ebiten.CursorShapeDefault,
	}, *shapes, "an event whose handler set nothing shows the default")

	latest := p.next()
	p.sync()
	older.SetShape(term.PointerMove)
	p.sync()
	assert.Len(t, *shapes, 2, "a superseded slot is ignored")

	latest.SetShape(term.PointerText)
	p.sync()
	assert.Equal(t, ebiten.CursorShapeText, (*shapes)[len(*shapes)-1],
		"the latest slot may be written after its event was dispatched")
}

func TestPointerLinkOverridesTheHandlerShape(t *testing.T) {
	p, shapes := recordingPointer()
	p.next().SetShape(term.PointerText)
	p.sync()

	p.setLink(true)
	p.setLink(true)
	p.setLink(false)
	assert.Equal(t, []ebiten.CursorShapeType{
		ebiten.CursorShapeText, ebiten.CursorShapePointer, ebiten.CursorShapeText,
	}, *shapes)
}
