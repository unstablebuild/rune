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

package vi

import (
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

// wraps text.CursorDelegate to set vi states
type mouseDelegate struct {
	mouse.Delegate
	vi   *viHandlerImpl
	drag dragState
	// pressedCell is in window coordinates. It stays comparable for the
	// whole gesture because the SDK auto-scrolls a drag only once the
	// pointer reaches a different row from the press — which has already
	// ended dragPressed — and wheel events reset drag through OnAction.
	pressedCell term.Coordinates
}

// dragState tracks a left-button gesture. The SDK reports held-button
// moves, including pointer jitter inside the pressed cell, through
// SetSelectionEnd, so a plain click — for example to focus the window —
// must neither highlight nor enter visual mode: vim only selects once
// the drag leaves the pressed cell.
type dragState uint8

const (
	dragIdle dragState = iota
	// dragPressed is a held left button still inside the pressed cell.
	dragPressed
	// dragSelecting is a held left button whose visual selection
	// follows the pointer.
	dragSelecting
)

func newDelegate(vi *viHandlerImpl) mouse.Delegate {
	return &mouseDelegate{Delegate: text.CursorMouseDelegate(&vi.cursor), vi: vi}
}

func (d *mouseDelegate) OnAction(
	ev term.Event, pos term.Coordinates, action mouse.Action,
) bool {
	// The SDK reports held-button moves without an action, so any action
	// ends the previous left-button gesture.
	d.drag = dragIdle
	// SelectLine only learns the row of a press, so the cell is captured
	// here for it to re-arm the gesture.
	if action == mouse.LeftClick {
		d.pressedCell = pos
	}
	return d.Delegate.OnAction(ev, pos, action)
}

func (d *mouseDelegate) SetSelectionStart(pos term.Coordinates) {
	d.drag = dragPressed
	if d.vi.mode() == insertMode {
		// vim's mouse=a: reposition and stay in insert. The anchor
		// must follow because insert-mode arrows snap back to it.
		d.vi.cursor.MoveToScroll(d.vi.cursor.ScrollCoordinates(pos))
		d.vi.anchor = d.vi.cursorAtScroll()
		return
	}
	// The SDK cleared any selection before this call, so a click only
	// repositions the caret; visual mode starts once the drag leaves
	// the pressed cell, in SetSelectionEnd.
	d.vi.cursor.MoveToScroll(d.vi.cursor.ScrollCoordinates(pos))
	d.vi.anchor = d.vi.cursorAtScroll()
	d.vi.markMatchingBrace()
}

func (d *mouseDelegate) SetSelectionEnd(pos term.Coordinates) {
	if d.drag == dragPressed {
		if pos == d.pressedCell {
			return
		}
		d.drag = dragSelecting
	}
	if !isSelectMode(d.vi.mode()) {
		// Drag from insert or a plain press: the cursor still sits on
		// the pressed cell, so setVisualMode anchors the selection
		// there.
		d.vi.setVisualMode()
		d.vi.anchor = d.vi.cursorAtScroll()
		d.vi.markMatchingBrace()
	}
	d.Delegate.SetSelectionEnd(pos)
}

// SelectWordAt is reached on the second click of a double-click, which
// vim answers with a charwise visual selection of the word. The gesture
// stays armed so jitter inside the pressed cell keeps the word.
func (d *mouseDelegate) SelectWordAt(pos term.Coordinates) {
	d.drag = dragPressed
	// The SDK does not clear the selection before SelectWordAt, so a
	// live selection here is left over from an earlier gesture and the
	// cursor must not be adjusted unless this click selects a word.
	if _, _, word := d.vi.less.Scroll().WordAt(
		d.vi.cursor.ScrollCoordinates(pos),
	); word == "" {
		return
	}
	d.Delegate.SelectWordAt(pos)
	if _, ok := d.vi.cursor.SelectionMode(); ok {
		// Scroll.WordAt's end is one cell past the word and visual mode
		// includes the cell under the cursor, so land on the last
		// character of the word.
		d.vi.cursor.MoveLeft()
		d.vi.setVisualMode()
		d.vi.anchor = d.vi.cursorAtScroll()
		d.vi.markMatchingBrace()
	}
}

// SelectLine is reached on a triple-click, which vim answers with a
// linewise visual selection. The gesture stays armed on the pressed cell
// captured by OnAction so jitter does not drift the cursor inside the
// line.
func (d *mouseDelegate) SelectLine(y int) {
	d.drag = dragPressed
	d.Delegate.SelectLine(y)
	if _, ok := d.vi.cursor.SelectionMode(); ok {
		d.vi.setVisualLineMode()
		d.vi.anchor = d.vi.cursorAtScroll()
		d.vi.markMatchingBrace()
	}
}

func (d *mouseDelegate) ClearSelection() {
	if isSelectMode(d.vi.mode()) {
		d.vi.setNormalMode()
	}
	d.Delegate.ClearSelection()
}
