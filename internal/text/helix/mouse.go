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
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

type mouseDelegate struct {
	mouse.Delegate
	h *helixHandlerImpl
}

func newDelegate(h *helixHandlerImpl) mouse.Delegate {
	return mouseDelegate{Delegate: text.CursorMouseDelegate(&h.cursor), h: h}
}

func (d mouseDelegate) SetSelectionStart(pos term.Coordinates) {
	if d.h.mode() == insertMode {
		d.h.cursor.MoveToScroll(d.h.cursor.ScrollCoordinates(pos))
		d.h.anchor = d.h.cursorAtScroll()
		return
	}
	d.Delegate.SetSelectionStart(pos)
	d.h.anchor = d.h.cursorAtScroll()
	d.h.explicitSel = false
	d.h.markMatchingBrace()
}

// SetSelectionEnd enters select mode: a drag in Helix leaves the
// editor extending the selection, which is what the sticky extend flag
// models.
func (d mouseDelegate) SetSelectionEnd(pos term.Coordinates) {
	if d.h.mode() == normalMode {
		d.h.extend = true
		d.h.setMode(normalMode)
	}
	d.Delegate.SetSelectionEnd(pos)
}

func (d mouseDelegate) ClearSelection() {
	if d.h.extend {
		d.h.setNormalMode()
	}
	d.Delegate.ClearSelection()
	d.h.anchorHere()
}
