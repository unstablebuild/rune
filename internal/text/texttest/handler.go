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

package texttest

import (
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

var _ text.Handler = (*TestHandler)(nil)

// TestHandler is a handler used to test composite handlers. See
// handler.TestHandler for more details.
type TestHandler struct {
	handler.TestHandler
	URI workspaceapi.URI
}

// NewTestHandler allocates storage for a new TestHandler and initializes it.
func NewTestHandler() (t *TestHandler) {
	t = new(TestHandler)
	t.TestHandler.Ch = 'A'
	return t
}

// Resource satisfies text.Editor.
func (t *TestHandler) Resource() workspaceapi.URI {
	return t.URI
}

// SetWrap satisfies text.Editor.
func (t *TestHandler) SetWrap(wrap bool) {
}

// ShowCommandBar satisfies text.Handler.
func (t *TestHandler) ShowCommandBar(show bool) {
}

// SetCursorAtScroll satisfies text.Handler.
func (t *TestHandler) SetCursorAtScroll(term.Coordinates) bool {
	return false
}

// Close satisfies text.Handler.
func (t *TestHandler) Close() error {
	return nil
}

// LocationLists satisfies text.Handler.
func (h *TestHandler) LocationLists() []text.LocationSet {
	return nil
}

// SeekUp satisfies component.Scrollable.
func (t *TestHandler) SeekUp() bool {
	return false
}

// SeekDown satisfies component.Scrollable.
func (t *TestHandler) SeekDown() bool {
	return false
}

// SeekOffset satisfies component.Scrollable.
func (t *TestHandler) SeekOffset() int {
	return 0
}

// MaxSeekOffset satisfies component.Scrollable.
func (t *TestHandler) MaxSeekOffset() int {
	return 0
}

// SetLocationList satisfies text.Handler.
func (t *TestHandler) SetLocationList(
	pri textapi.LocationPriority, ID string, loc text.LocationList,
) {
}

// MoveToNextLocation satisfies text.Handler.
func (t *TestHandler) MoveToNextLocation(ID string) bool {
	return false
}

// MoveToPrevLocation satisfies text.Handler.
func (t *TestHandler) MoveToPrevLocation(ID string) bool {
	return false
}

// CellView satisfies text.Handler.
func (t *TestHandler) CellView() cell.View {
	return nil
}

// CellEditor satisfies text.Handler.
func (t *TestHandler) CellEditor() cell.Editor {
	return nil
}

// SetDefaultAttributes satisfies text.Handler.
func (t *TestHandler) SetDefaultAttributes(attr term.Attributes) {
}

// Dimensions satisfies text.Handler. TestHandler has no buffer
// so it reports (0, 0).
func (t *TestHandler) Dimensions() (int, int) { return 0, 0 }

// IsSearchMode satisfies text.Handler.
func (t *TestHandler) IsSearchMode() bool { return false }

// IsNormalMode satisfies text.Handler.
func (t *TestHandler) IsNormalMode() bool { return false }

// CursorAtScroll satisfies text.Handler.
func (t *TestHandler) CursorAtScroll() term.Coordinates {
	return term.Coordinates{}
}
