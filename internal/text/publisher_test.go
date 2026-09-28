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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// nopPubHandler is a minimal Handler used to exercise the Publisher
// without instantiating a full editor. Every method returns the zero
// value; the type intentionally does not embed any nil interface.
type nopPubHandler struct{}

func (nopPubHandler) Draw(term.Writer)               {}
func (nopPubHandler) Resize(int, int)                {}
func (nopPubHandler) Handle(term.Event) (bool, bool) { return false, true }
func (nopPubHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}
func (nopPubHandler) Selection() (string, bool) { return "", false }
func (nopPubHandler) Close() error              { return nil }
func (nopPubHandler) SeekUp() bool              { return false }
func (nopPubHandler) SeekDown() bool            { return false }
func (nopPubHandler) SeekOffset() int           { return 0 }
func (nopPubHandler) MaxSeekOffset() int        { return 0 }
func (nopPubHandler) Resource() workspaceapi.URI {
	return workspaceapi.URI{}
}
func (nopPubHandler) SetWrap(bool)                            {}
func (nopPubHandler) ShowCommandBar(bool)                     {}
func (nopPubHandler) SetCursorAtScroll(term.Coordinates) bool { return false }
func (nopPubHandler) CursorAtScroll() term.Coordinates        { return term.Coordinates{} }
func (nopPubHandler) SetLocationList(textapi.LocationPriority, string, LocationList) {
}
func (nopPubHandler) LocationLists() []LocationSet         { return nil }
func (nopPubHandler) MoveToNextLocation(string) bool       { return false }
func (nopPubHandler) MoveToPrevLocation(string) bool       { return false }
func (nopPubHandler) CellView() cell.View                  { return cell.NewBuffer().View() }
func (nopPubHandler) CellEditor() cell.Editor              { return cell.NewBuffer().Editor() }
func (nopPubHandler) SetDefaultAttributes(term.Attributes) {}
func (nopPubHandler) Dimensions() (int, int)               { return 0, 0 }
func (nopPubHandler) IsSearchMode() bool                   { return false }
func (nopPubHandler) IsNormalMode() bool                   { return false }

// TestPublishEditPanicsOnNilCursor enforces the new contract that
// PublishEdit requires a non-nil cursor. Cursor-less editors must use
// PublishExternalEdit instead. A previous version silently skipped
// the scroll subscription on nil cursor, which only deferred the
// nil-pointer dereference to the next user event via
// cursorPublisher.Handle → RecordCursorChange.
func TestPublishEditPanicsOnNilCursor(t *testing.T) {
	var pub Publisher
	pub.Init()
	buf := cell.NewBuffer()
	uri, err := workspaceapi.ParseURI("memory:///tmp/nilcursor.txt")
	require.NoError(t, err)
	assert.Panics(t, func() {
		_ = pub.PublishEdit(uri, buf, nopPubHandler{}, nil)
	})
}

// captureSub records the textapi.Event types it observes so tests
// can assert which lifecycle events PublishExternalEdit dispatches.
type captureSub struct {
	events []textapi.EventType
}

func (c *captureSub) Handle(ctx context.Context, ev textapi.Event) bool {
	c.events = append(c.events, ev.Type)
	return false
}

// TestPublishExternalEditDispatchesOpenFocusAndEdit asserts that the
// cursor-less publishing path still fires the Open/Focus events
// downstream subscribers (LSP, indexers, tab manager) depend on, and
// that buffer edits surface as EventTypeEdit. It does NOT subscribe
// to scroll or cursor events because the external editor owns those.
func TestPublishExternalEditDispatchesOpenFocusAndEdit(t *testing.T) {
	var pub Publisher
	pub.Init()
	sub := &captureSub{}
	pub.SubscribeEvents([]textapi.EventType{
		textapi.EventTypeOpen,
		textapi.EventTypeFocus,
		textapi.EventTypeEdit,
		textapi.EventTypeScroll,
		textapi.EventTypeCursor,
	}, sub)

	buf := cell.NewBuffer()
	uri, err := workspaceapi.ParseURI("memory:///tmp/external.txt")
	require.NoError(t, err)

	h := pub.PublishExternalEdit(uri, buf, nopPubHandler{})
	require.NotNil(t, h)
	require.Equal(t, []textapi.EventType{
		textapi.EventTypeOpen, textapi.EventTypeFocus,
	}, sub.events, "open/focus must fire during publish")

	// Simulate the watcher driving a buffer replace.
	buf.WriteString("hello\n")
	require.Contains(t, sub.events, textapi.EventTypeEdit,
		"buffer writes must surface as EventTypeEdit")
	require.NotContains(t, sub.events, textapi.EventTypeScroll,
		"external publish must not subscribe to scroll")
	require.NotContains(t, sub.events, textapi.EventTypeCursor,
		"external publish must not subscribe to cursor")
}

// TestPublishExternalEditHandlerHandlesEventsWithoutPanic verifies the
// returned handler is the raw root (no cursor wrapper) so events
// dispatched to it do not crash on a missing *Cursor. This is the
// invariant that was actually broken when exo used PublishEdit with
// a nil cursor: even after Open/Focus dispatch, the very first user
// keypress would hit cursorPublisher.Handle → RecordCursorChange.
func TestPublishExternalEditHandlerHandlesEventsWithoutPanic(t *testing.T) {
	var pub Publisher
	pub.Init()
	buf := cell.NewBuffer()
	uri, err := workspaceapi.ParseURI("memory:///tmp/external_handle.txt")
	require.NoError(t, err)
	h := pub.PublishExternalEdit(uri, buf, nopPubHandler{})
	assert.NotPanics(t, func() {
		_, _ = h.Handle(term.Event{})
		// SetCursorAtScroll, MoveTo*Location etc. would all hit the
		// nil cursor through the cursorPublisher wrapper; ensure
		// none of them are reachable here.
		_ = h.SetCursorAtScroll(term.Coordinates{})
		_ = h.MoveToNextLocation("")
		_ = h.MoveToPrevLocation("")
	})
}
