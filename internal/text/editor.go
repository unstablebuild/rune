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
	"errors"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// ErrCommandNotRegistered is returned by UnsubscribeCommand and
// UnregisterREPLCommand when the named command is not registered on the
// editor. Callers that want to tolerate the absence of a prior
// registration should compare against this error with errors.Is.
var ErrCommandNotRegistered = errors.New("command not registered")

// ErrResourceOpenerNotRegistered is returned by UnregisterResourceOpener
// when no resource opener is registered for the scheme.
var ErrResourceOpenerNotRegistered = errors.New("resource opener not registered")

// Handler just wraps a tui.Handler to indicate that this API's handlers might
// not be compatible with other APIs.
type Handler interface {
	browserapi.Handler
	component.Scrollable

	// This is only used to differentiate editor.Handler from the rest
	// of tui.Handler in a browser.Component.
	Resource() workspaceapi.URI

	// SetWraps defines wheter editor handler should wrap that are longer than
	// available width into the next line or simply truncate them in the view.
	SetWrap(wrap bool)

	// ShowCommandBar hides or shows the editor's command bar.
	ShowCommandBar(show bool)

	// SetCursorAtScroll sets the cursor of this handler at scroll coordinates
	// determined by pos.
	SetCursorAtScroll(pos term.Coordinates) bool

	// CursorAtScroll gets the position of Handler's cursor in the underlying
	// content buffer.
	CursorAtScroll() term.Coordinates

	// SetLocationList sets the Handler's location list for users to
	// navigate the code. See LocationList for more details.
	// In order to remove a location list, SetLocationList must be called
	// with an empty (or nil) LocationList.
	// Locations are removed if underlying buffer is updated. It is the
	// responsibility of the caller to recompute the list of locations
	// and call SetLocationList with the new list of locations after
	// every update. Check cell.Buffer.Subscribe for more details.
	SetLocationList(textapi.LocationPriority, string, LocationList)

	// LocationLists returns a map of location list IDs to their
	// respective locations.
	LocationLists() []LocationSet

	// MoveToNextLocation cursor to the next location on list with ID.
	MoveToNextLocation(ID string) bool

	// MoveToPrevLocation cursor to the previous location on list with ID.
	MoveToPrevLocation(ID string) bool

	// CellView returns a cell.View which allows to read the editor's internal buffer.
	CellView() cell.View

	// CellEditor returns a cell.Editor which allows for direct write access
	// to the editor's internal buffer.
	CellEditor() cell.Editor

	// SetDefaultAttributes sets the default attributes of the given Handler
	// before any LocationList overwrites.
	SetDefaultAttributes(term.Attributes)

	// Dimensions reports the ideal cell width/height this handler
	// would claim to render its buffer without clipping, including
	// any auxiliary chrome (line numbers, folds, git icons, …) the
	// handler draws on top of the buffer. Parents wanting to avoid
	// truncation should size the hosting window to at least these
	// dimensions. Satisfies component.Floating.
	Dimensions() (width, height int)

	// IsSearchMode reports whether the handler is currently
	// consuming keystrokes for an interactive search prompt
	// (e.g. `/` or `?` in vi, Cmd-F in modeless). When true,
	// outer handlers that wrap a Handler must delegate <Enter>
	// (and other search-completing keys) to the inner Handler
	// so the search can be committed instead of being captured
	// for an unrelated shortcut.
	IsSearchMode() bool

	// IsNormalMode reports whether the handler is in a modal editor's
	// normal mode, where keystrokes are commands rather than text.
	// Modeless editors are never in it. Outer handlers that repurpose
	// <Enter> may only do so while this is true; in every other mode
	// the key keeps its editor meaning, such as inserting a newline.
	IsNormalMode() bool
}

// EventPublisher wraps subscribing and unsubscribing to file events.
type EventPublisher interface {
	// SubscribeEvents subscribes EventHandler to events of type EventType.
	SubscribeEvents([]textapi.EventType, EventHandler) error

	// UnsubscribeEvents unsubscribes the given event handler from
	// all events.
	UnsubscribeEvents(EventHandler) (bool, error)
}

// Editor is the interface that wraps an API to manage a text editor.
type Editor interface {
	EventPublisher
	// Edit opens a file and returns an editor.Handler to edit it or an error
	// if there was an error opening it.
	Edit(
		ctx context.Context,
		file workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
	) (Handler, error)

	// Editor returns the editor.Handler with name or returns
	// an error if no editor with name is open via Edit.
	Editor(workspaceapi.URI) (Handler, error)

	// SubscribeCommand registers command to be dispatched to CommandHandler.
	SubscribeCommand(textapi.CommandManual, CommandHandler) error

	// RegisterREPLCommand registers a REPL command to be dispatched to a
	// textapi.REPLHandler.
	RegisterREPLCommand(textapi.CommandManual, textapi.REPLHandler) error

	// UnsubscribeCommand un-registers command. Returns
	// ErrCommandNotRegistered if no such command is registered.
	UnsubscribeCommand(string) error

	// UnregisterREPLCommand un-registers a previously registered REPL
	// command. Returns ErrCommandNotRegistered if no such command is
	// registered.
	UnregisterREPLCommand(string) error

	// RegisterResourceOpener registers h as the opener of the resources
	// whose URI has the given scheme. It fails if the scheme already has
	// an opener.
	RegisterResourceOpener(scheme string, h textapi.ResourceOpenHandler) error

	// UnregisterResourceOpener un-registers the opener of scheme. Returns
	// ErrResourceOpenerNotRegistered if the scheme has no opener.
	UnregisterResourceOpener(scheme string) error

	// IsExternal reports whether this editor manages its buffer
	// contents out of band (e.g. via an external TUI editor
	// process). When true, callers must treat every file opened
	// through Edit as read-only on the Rune side and skip
	// Rune-level write/reload paths (auto-save, dirty-tab markers,
	// FS-event reload notifications). Editors that own their
	// buffer in-process return false.
	IsExternal() bool
}

// NewREPLHandler returns a REPL handler backed by router.
func NewREPLHandler(comp *Component) textapi.REPLHandler {
	return replHandler{router: comp}
}

// ViewDimensions returns the visual (width, height) needed to render
// the given cell.View without clipping. This is the canonical way for
// leaf text handlers to report their ideal dimensions: the width is
// the widest row's VISUAL width (summing each cell's monospace width
// — cells can hold wide glyphs like Nerd Font icons or CJK, which
// occupy two cells on screen but only one entry in the backing
// slice), and the height is the buffer's row count.
//
// Using View.Columns(y) directly would under-report width for any row
// containing a width-2 glyph, causing a hosting window to be sized
// one cell too narrow and clipping the last character.
func ViewDimensions(v cell.View) (width, height int) {
	height = v.Rows()
	for _, row := range v.RawCells() {
		w := 0
		for _, c := range row {
			if c.Width > 1 {
				w += int(c.Width)
				continue
			}
			w++
		}
		if w > width {
			width = w
		}
	}
	return
}

// replHandler routes REPL commands through a text.Component.
type replHandler struct {
	router *Component
}

// HandleCommand dispatches cmd to a registered REPL handler.
func (h replHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	handler, ok := h.router.REPLCommand(cmd.Name)
	if !ok {
		return nil, repl.ErrNotFound
	}
	return handler.HandleCommand(ctx, cmd, pw)
}

// Complete dispatches shell completion to a registered REPL handler.
func (h replHandler) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	handler, ok := h.router.REPLCommand(cmd)
	if !ok {
		return iterator.Empty[string](), nil
	}
	return handler.Complete(ctx, cmd, args)
}

// Help dispatches shell help requests to a registered REPL handler.
func (h replHandler) Help(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return iterator.Empty[component.Responsive](), nil
	}
	handler, ok := h.router.REPLCommand(args[0])
	if !ok {
		return nil, repl.ErrNotFound
	}
	return handler.Help(ctx, args[1:])
}
