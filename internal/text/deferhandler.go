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

package text

import (
	"context"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text/streamload"
)

var (
	_ Handler              = (*deferHandler)(nil)
	_ component.Scrollable = (*deferHandler)(nil)
)

// deferHandler is the text.Handler wrapper installed on streaming
// tabs in place of a bare *streamload.Handler. Without it, IDE call
// sites that type-assert tab handlers to text.Handler (session
// restore, ex commands, idecursor navigation, syntax jumps, RPC)
// silently fail for the whole async-load window. Mutating calls
// received before Swap are queued and replayed on the real handler;
// calls received after Swap forward directly.
type deferHandler struct {
	*streamload.Handler

	mu      sync.Mutex
	real    Handler
	pending deferPendingOps
}

// cursor / wrap / showCommandBar / defaultAttrs follow last-wins
// semantics; locationLists preserves insertion order so replay
// matches the caller's intent.
type deferPendingOps struct {
	cursor         *term.Coordinates
	wrap           *bool
	showCommandBar *bool
	defaultAttrs   *term.Attributes
	locationLists  []deferPendingLocList
	edits          []deferPendingEdit
}

type deferPendingLocList struct {
	priority textapi.LocationPriority
	id       string
	list     LocationList
}

type deferPendingEdit struct {
	start, end term.Coordinates
	text       string
}

func newDeferHandler(sh *streamload.Handler) *deferHandler {
	if sh == nil {
		panic("text: nil *streamload.Handler passed to newDeferHandler")
	}
	return &deferHandler{Handler: sh}
}

// Swap installs real as the destination for subsequent text.Handler
// calls and replays queued mutations on it. Cursor is replayed last
// so it lands on top of any location-list recompute the real handler
// triggers.
func (h *deferHandler) Swap(real Handler) {
	if real == nil {
		panic("text: deferHandler.Swap called with nil Handler")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.real = real
	pending := h.pending
	h.pending = deferPendingOps{}
	if pending.defaultAttrs != nil {
		real.SetDefaultAttributes(*pending.defaultAttrs)
	}
	if pending.wrap != nil {
		real.SetWrap(*pending.wrap)
	}
	if pending.showCommandBar != nil {
		real.ShowCommandBar(*pending.showCommandBar)
	}
	if len(pending.edits) > 0 {
		ce := real.CellEditor()
		for _, e := range pending.edits {
			ce.Edit(context.Background(), e.start, e.end, e.text)
		}
	}
	for _, op := range pending.locationLists {
		real.SetLocationList(op.priority, op.id, op.list)
	}
	if pending.cursor != nil {
		real.SetCursorAtScroll(*pending.cursor)
	}
}

func (h *deferHandler) Resource() workspaceapi.URI { return h.Handler.URI() }

func (h *deferHandler) SetWrap(wrap bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		h.real.SetWrap(wrap)
		return
	}
	h.pending.wrap = &wrap
}

func (h *deferHandler) ShowCommandBar(show bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		h.real.ShowCommandBar(show)
		return
	}
	h.pending.showCommandBar = &show
}

// SetCursorAtScroll returns true even when queued so callers do not
// fall back to alternative cursor-setting paths during the
// async-load window.
func (h *deferHandler) SetCursorAtScroll(pos term.Coordinates) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		return h.real.SetCursorAtScroll(pos)
	}
	h.pending.cursor = &pos
	return true
}

func (h *deferHandler) CursorAtScroll() term.Coordinates {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		return h.real.CursorAtScroll()
	}
	if h.pending.cursor != nil {
		return *h.pending.cursor
	}
	return term.Coordinates{}
}

func (h *deferHandler) SetLocationList(
	pri textapi.LocationPriority, id string, l LocationList,
) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		h.real.SetLocationList(pri, id, l)
		return
	}
	h.pending.locationLists = append(h.pending.locationLists,
		deferPendingLocList{priority: pri, id: id, list: l})
}

func (h *deferHandler) LocationLists() []LocationSet {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		return h.real.LocationLists()
	}
	return nil
}

func (h *deferHandler) MoveToNextLocation(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		return h.real.MoveToNextLocation(id)
	}
	return false
}

func (h *deferHandler) MoveToPrevLocation(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		return h.real.MoveToPrevLocation(id)
	}
	return false
}

// The streaming buffer is not exposed as the pre-swap CellView
// because coordinates produced against it would not survive the
// swap to the real editor buffer.
var deferEmptyView = cell.NewBuffer().View()

func (h *deferHandler) CellView() cell.View {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		return h.real.CellView()
	}
	return deferEmptyView
}

func (h *deferHandler) CellEditor() cell.Editor {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		return h.real.CellEditor()
	}
	return deferQueueingCellEditor{h: h}
}

func (h *deferHandler) SetDefaultAttributes(attrs term.Attributes) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.real != nil {
		h.real.SetDefaultAttributes(attrs)
		return
	}
	h.pending.defaultAttrs = &attrs
}

func (h *deferHandler) Dimensions() (width, height int) {
	h.mu.Lock()
	real := h.real
	h.mu.Unlock()
	if real != nil {
		return real.Dimensions()
	}
	return h.Handler.Dimensions()
}

func (h *deferHandler) IsSearchMode() bool {
	h.mu.Lock()
	real := h.real
	h.mu.Unlock()
	if real != nil {
		return real.IsSearchMode()
	}
	return h.Handler.InSearchMode()
}

func (h *deferHandler) IsNormalMode() bool {
	h.mu.Lock()
	real := h.real
	h.mu.Unlock()
	if real != nil {
		return real.IsNormalMode()
	}
	return false
}

// deferQueueingCellEditor records edits issued before Swap so they can
// be replayed on the real handler's buffer. Without this, a
// server-driven workspace/applyEdit that force-opens a closed file
// during its async streaming load is silently dropped (e.g. gopls
// go.mod vuln upgrades never landing).
type deferQueueingCellEditor struct{ h *deferHandler }

func (e deferQueueingCellEditor) Edit(
	_ context.Context, start, end term.Coordinates, str string,
) (from, to term.Coordinates, old string) {
	e.h.mu.Lock()
	defer e.h.mu.Unlock()
	if e.h.real != nil {
		return e.h.real.CellEditor().Edit(context.Background(), start, end, str)
	}
	e.h.pending.edits = append(e.h.pending.edits,
		deferPendingEdit{start: start, end: end, text: str})
	return start, end, ""
}

type deferNoopCellEditor struct{}

func (deferNoopCellEditor) Edit(
	_ context.Context, start, _ term.Coordinates, _ string,
) (from, to term.Coordinates, old string) {
	return start, start, ""
}
