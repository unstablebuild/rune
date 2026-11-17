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

package vteregistry

import (
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtewire"
)

// attachment is one view of a canonical terminal. Close detaches it.
type attachment struct {
	c *canonical
	// preview is whether this is a view PreviewVTE returned.
	preview bool
	// gone is set once the terminal was destroyed: the attachment then
	// shows nothing and asks to be dropped.
	gone     atomic.Bool
	detached atomic.Bool

	// guarded by c.mu
	seq           uint64
	focusedAt     uint64
	width, height int
	// cell is the size of a cell on the display of a wire member, which
	// the pty takes with its size when the member owns it. An in-process
	// view leaves it zero: its display is the registry's.
	cell cellSize
}

var _ vte.View = (*attachment)(nil)

// ID satisfies vte.View.
func (a *attachment) ID() vte.ID { return a.c.id }

// Close satisfies vte.View: it detaches this view only.
func (a *attachment) Close() error {
	if a.detached.Swap(true) {
		return nil
	}
	a.c.detach(a)
	return nil
}

// live locks the terminal for a call into its emulator, and reports
// false, unlocked, when there is no emulator to call.
func (a *attachment) live() bool {
	if a.gone.Load() || a.detached.Load() {
		return false
	}
	a.c.mu.Lock()
	if a.c.destroyed {
		a.c.mu.Unlock()
		return false
	}
	return true
}

// Resize satisfies tui.Component. The pty only follows the resize
// owner's size.
func (a *attachment) Resize(width, height int) {
	a.resize(vtewire.Resize{Width: width, Height: height})
}

// resize records the size this view asks for, with the cell size of its
// display when it names one, and gives it to the pty when this view
// owns the pty's size. It reports the size in force, and false when
// the terminal is gone.
func (a *attachment) resize(req vtewire.Resize) (vtewire.Resize, bool) {
	if !a.live() {
		return vtewire.Resize{}, false
	}
	defer a.c.mu.Unlock()
	if req.Width > 0 && req.Height > 0 {
		a.width, a.height = req.Width, req.Height
	}
	if cell := cellSizeOf(req.CellPixelWidth, req.CellPixelHeight); cell != (cellSize{}) {
		a.cell = cell
	}
	if a.c.resizeOwner == a {
		a.c.followLocked(a)
	}
	return a.c.geometryLocked(), true
}

// offsetLocked is where the grid's top-left corner shows in this view,
// and whether the view crops the grid at all.
func (a *attachment) offsetLocked() (term.Coordinates, bool) {
	c := a.c
	if c.resizeOwner == a || c.width == 0 || c.height == 0 ||
		(a.width == c.width && a.height == c.height) {
		return term.Coordinates{}, false
	}
	return term.Coordinates{Y: -max(c.height-a.height, 0)}, true
}

// Draw satisfies tui.Component.
func (a *attachment) Draw(w term.Writer) {
	if !a.live() {
		return
	}
	defer a.c.mu.Unlock()
	c := a.c
	off, crops := a.offsetLocked()
	if !crops {
		c.h.Draw(w)
		return
	}
	blank := term.NewCell(' ', 1, c.defAttr)
	for y := range a.height {
		for x := range a.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, blank)
		}
	}
	view := &component.VirtualWriter{Writer: w, Width: a.width, Height: a.height}
	c.h.Draw(&component.VirtualWriter{
		Writer: view, Offset: off, Width: c.width, Height: c.height,
	})
}

// Handle satisfies tui.Handler. A view that crops the grid moves the
// pointer onto the cell it shows; a pointer outside the view is left
// outside the grid, so it still reads as leaving it.
func (a *attachment) Handle(ev term.Event) (exit, handled bool) {
	if !a.live() {
		return a.gone.Load(), false
	}
	defer a.c.mu.Unlock()
	if off, crops := a.offsetLocked(); crops && ev.Type == term.EventMouse &&
		ev.MouseX >= 0 && ev.MouseY >= 0 && ev.MouseX < a.width && ev.MouseY < a.height {
		ev.MouseX -= off.X
		ev.MouseY -= off.Y
	}
	return a.c.h.Handle(ev)
}

// Cursor satisfies tui.Handler.
func (a *attachment) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	if !a.live() {
		return term.Coordinates{}, 0, false
	}
	defer a.c.mu.Unlock()
	pos, style, show := a.c.h.Cursor()
	off, crops := a.offsetLocked()
	if !crops {
		return pos, style, show
	}
	pos = term.Coordinates{X: pos.X + off.X, Y: pos.Y + off.Y}
	if pos.X < 0 || pos.Y < 0 || pos.X >= a.width || pos.Y >= a.height {
		return pos, style, false
	}
	return pos, style, show
}

// Selection satisfies tui.Handler.
func (a *attachment) Selection() (string, bool) {
	if !a.live() {
		return "", false
	}
	defer a.c.mu.Unlock()
	return a.c.h.Selection()
}

// SeekUp satisfies component.Scrollable.
func (a *attachment) SeekUp() bool {
	if !a.live() {
		return false
	}
	defer a.c.mu.Unlock()
	return a.c.h.SeekUp()
}

// SeekDown satisfies component.Scrollable.
func (a *attachment) SeekDown() bool {
	if !a.live() {
		return false
	}
	defer a.c.mu.Unlock()
	return a.c.h.SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (a *attachment) SeekOffset() int {
	if !a.live() {
		return 0
	}
	defer a.c.mu.Unlock()
	return a.c.h.SeekOffset()
}

// MaxSeekOffset satisfies component.Scrollable.
func (a *attachment) MaxSeekOffset() int {
	if !a.live() {
		return 0
	}
	defer a.c.mu.Unlock()
	return a.c.h.MaxSeekOffset()
}

// OnFocusChange satisfies vte.View. A focused view takes over the
// pty's size.
func (a *attachment) OnFocusChange(inFocus bool) {
	if !a.live() {
		return
	}
	defer a.c.mu.Unlock()
	c := a.c
	if inFocus {
		c.focusSeq++
		a.focusedAt = c.focusSeq
		c.resizeOwner = a
		c.followLocked(a)
	}
	c.h.OnFocusChange(inFocus)
}

// SetDefaultAttributes satisfies vte.View.
func (a *attachment) SetDefaultAttributes(attr term.Attributes) {
	if !a.live() {
		return
	}
	defer a.c.mu.Unlock()
	a.c.defAttr = attr
	a.c.h.SetDefaultAttributes(attr)
}

// Snapshot satisfies vte.View.
func (a *attachment) Snapshot() (vte.Snapshot, error) {
	if !a.live() {
		return vte.Snapshot{}, ErrClosed
	}
	defer a.c.mu.Unlock()
	return a.c.h.Snapshot()
}

// RestoreFromSnapshot satisfies vte.View.
func (a *attachment) RestoreFromSnapshot(s vte.Snapshot) error {
	if !a.live() {
		return ErrClosed
	}
	defer a.c.mu.Unlock()
	return a.c.h.RestoreFromSnapshot(s)
}

// IsComplete satisfies vte.View.
func (a *attachment) IsComplete() bool {
	if !a.live() {
		return true
	}
	defer a.c.mu.Unlock()
	return a.c.h.Component().IsComplete()
}

// URI satisfies vte.View. Every attachment of a terminal has the
// terminal's URI.
func (a *attachment) URI() workspaceapi.URI { return a.c.uri }

// Title satisfies vte.View.
func (a *attachment) Title() string {
	if !a.live() {
		return ""
	}
	defer a.c.mu.Unlock()
	return a.c.h.Component().Title()
}

// UsedAlternateBuffer satisfies vte.View.
func (a *attachment) UsedAlternateBuffer() bool {
	if !a.live() {
		return false
	}
	defer a.c.mu.Unlock()
	return a.c.h.Component().UsedAlternateBuffer()
}

// ClearPrimaryBuffer satisfies vte.View.
func (a *attachment) ClearPrimaryBuffer() bool {
	if !a.live() {
		return false
	}
	defer a.c.mu.Unlock()
	return a.c.h.ClearPrimaryBuffer()
}

// ExitErr satisfies vte.View.
func (a *attachment) ExitErr() error {
	if !a.live() {
		return nil
	}
	defer a.c.mu.Unlock()
	return a.c.h.ExitErr()
}

// CursorAtScroll satisfies vte.View.
func (a *attachment) CursorAtScroll() term.Coordinates {
	if !a.live() {
		return term.Coordinates{}
	}
	defer a.c.mu.Unlock()
	return a.c.h.CursorAtScroll()
}

// SystemCanDispatchBell calls callback with nil once the terminal's
// program answers a bell, and with an error when it does not in time,
// as [vte.Handler.SystemCanDispatchBell] does.
func (a *attachment) SystemCanDispatchBell(callback func(error)) {
	if !a.live() {
		callback(ErrClosed)
		return
	}
	defer a.c.mu.Unlock()
	a.c.h.SystemCanDispatchBell(callback)
}
