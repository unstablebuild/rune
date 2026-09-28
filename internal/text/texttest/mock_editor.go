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

//revive:disable:exported
package texttest

import (
	"context"
	"errors"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

// TestEditor is an editor that can be used in tests.
type TestEditor struct {
	uri  workspaceapi.URI
	buf  *cell.Buffer
	subs map[textapi.EventType][]text.EventHandler
	cb   func()
}

// NopEditor returns a editor suitable for testing.
// It doesn't return real tui.Handler upon Edit but mimics
// editor.Simple's subscription logic.
func NopEditor() *TestEditor {
	return &TestEditor{}
}

// NopEditorWithCallback returns a text.Editor that calls cb when
// SubscribeEvents is called.
func NopEditorWithCallback(cb func()) *TestEditor {
	return &TestEditor{cb: cb}
}

func (e *TestEditor) Handle(ctx context.Context, ev textapi.Event) bool {
	e.dispatchEvent(ctx, ev)
	return false
}

func (e *TestEditor) dispatchEvent(ctx context.Context, ev textapi.Event) {
	if len(e.subs) == 0 {
		return
	}
	subs, ok := e.subs[ev.Type]
	if !ok {
		return
	}

	remain := make([]text.EventHandler, 0, len(subs))
	for _, sub := range subs {
		exit := sub.Handle(ctx, ev)
		if !exit {
			remain = append(remain, sub)
		}
	}
	e.subs[ev.Type] = remain
}

type TestEditorHandler struct {
	browsertest.TestHandler
	LocationList  text.LocationList
	parent        *TestEditor
	uri           workspaceapi.URI
	Width, Height int
}

func (e *TestEditorHandler) Resource() workspaceapi.URI {
	return e.uri
}

func (e *TestEditorHandler) SetWrap(wrap bool) {
}

func (t *TestEditorHandler) ShowCommandBar(show bool) {
}

// SeekUp satisfies component.Scrollable.
func (t *TestEditorHandler) SeekUp() bool {
	return false
}

// SeekDown satisfies component.Scrollable.
func (t *TestEditorHandler) SeekDown() bool {
	return false
}

// SeekOffset satisfies component.Scrollable.
func (t *TestEditorHandler) SeekOffset() int {
	return 0
}

// MaxSeekOffset satisfies component.Scrollable.
func (t *TestEditorHandler) MaxSeekOffset() int {
	return 0
}

func (e *TestEditor) Edit(
	ctx context.Context,
	resource workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
) (text.Handler, error) {
	e.uri = resource
	e.buf = buf

	h := &TestEditorHandler{
		uri:         resource,
		parent:      e,
		TestHandler: *browsertest.NewTestHandler(),
	}
	e.dispatchEvent(context.Background(), textapi.Event{
		Type:     textapi.EventTypeOpen,
		URI:      resource,
		Resource: h,
		Content:  buf.String(),
	})

	subs := text.CellSubscriber(resource, h, e)
	buf.Subscribe(subs)
	return h, nil
}

func (t *TestEditorHandler) SetLocationList(
	pri textapi.LocationPriority, id string, loc text.LocationList,
) {
	t.LocationList = loc
}

func (e *TestEditorHandler) Handle(ev term.Event) (bool, bool) {
	e.parent.dispatchEvent(context.Background(), textapi.Event{
		Type:     textapi.EventTypeCursor,
		URI:      e.uri,
		Resource: e,
	})
	if ev.Ch == 'v' {
		e.parent.dispatchEvent(context.Background(), textapi.Event{
			Type:     textapi.EventTypeSelection,
			URI:      e.uri,
			Resource: e,
		})
	}
	return e.TestHandler.Handle(ev)
}

func (e *TestEditorHandler) Resize(width, height int) {
	e.Width, e.Height = width, height
	e.TestHandler.Resize(width, height)
}

func (e *TestEditorHandler) MoveToNextLocation(ID string) bool {
	return false
}

func (e *TestEditorHandler) MoveToPrevLocation(ID string) bool {
	return false
}

func (t *TestEditorHandler) SetCursorAtScroll(pos term.Coordinates) bool {
	t.CursorPos = pos
	return true
}

func (t *TestEditorHandler) CursorAtScroll() term.Coordinates {
	return t.CursorPos
}

func (t *TestEditorHandler) CellEditor() cell.Editor {
	return t.parent.buf.Editor()
}

func (t *TestEditorHandler) CellView() cell.View {
	return t.parent.buf.View()
}

// Dimensions satisfies text.Handler.
func (t *TestEditorHandler) Dimensions() (int, int) {
	return text.ViewDimensions(t.parent.buf.View())
}

// IsSearchMode satisfies text.Handler.
func (t *TestEditorHandler) IsSearchMode() bool { return false }

// IsNormalMode satisfies text.Handler.
func (t *TestEditorHandler) IsNormalMode() bool { return false }

func (e *TestEditor) SubscribeCommand(cmd textapi.CommandManual, h text.CommandHandler) error {
	return nil
}

func (e *TestEditor) RegisterREPLCommand(
	cmd textapi.CommandManual, h textapi.REPLHandler,
) error {
	return nil
}

func (e *TestEditor) REPLCommands() []textapi.CommandManual {
	return nil
}

func (e *TestEditor) UnsubscribeCommand(cmd string) error {
	return nil
}

func (e *TestEditor) UnregisterREPLCommand(cmd string) error {
	return nil
}

func (e *TestEditor) RegisterResourceOpener(string, textapi.ResourceOpenHandler) error {
	return nil
}

func (e *TestEditor) UnregisterResourceOpener(string) error {
	return text.ErrResourceOpenerNotRegistered
}

func (h *TestEditorHandler) LocationLists() []text.LocationSet {
	return nil
}

func (t *TestEditorHandler) SetDefaultAttributes(attr term.Attributes) {
	t.Attributes.Fg = attr.Fg
	t.Attributes.Bg = attr.Bg
}

func (e *TestEditor) UnsubscribeEvents(
	sub text.EventHandler,
) (ret bool, err error) {
	final := make(map[textapi.EventType][]text.EventHandler)
	for ev, subs := range e.subs {
		final[ev] = make([]text.EventHandler, 0, len(subs))
		for _, s := range subs {
			if s != sub {
				final[ev] = append(final[ev], s)
			} else {
				ret = true
			}
		}
	}
	e.subs = final
	return
}

func (e *TestEditor) SubscribeEvents(
	evs []textapi.EventType, sub text.EventHandler,
) error {
	if e.subs == nil {
		e.subs = make(map[textapi.EventType][]text.EventHandler)
	}
	for _, ev := range evs {
		if _, ok := e.subs[ev]; !ok {
			e.subs[ev] = []text.EventHandler{sub}
		} else {
			e.subs[ev] = append(e.subs[ev], sub)
		}
	}
	if e.cb != nil {
		e.cb()
	}
	return nil
}

func (e *TestEditor) Subscribers() map[textapi.EventType][]text.EventHandler {
	return e.subs
}

func (e *TestEditor) Editor(file workspaceapi.URI) (text.Handler, error) {
	return nil, errors.New("nope")
}

// IsExternal reports false. Tests that need true should embed
// TestEditor and override IsExternal locally.
func (*TestEditor) IsExternal() bool { return false }
