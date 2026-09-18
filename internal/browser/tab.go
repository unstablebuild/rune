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

package browser

import (
	"errors"
	"io"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

var _ component.Scrollable = (*Tab)(nil)

// Tab is a structure that represents a tab in a Browser.Component.
// It satisfies browser.Handler interface so it can be used
// with browser.Browser API. See browser.Component.NewTab for more details.
//
// If the underlying browser.Handler of a Tab satisfies component.Scrollable,
// then Tab satisfies it too; otherwise all the component.Scrollable methods
// are no-op.
type Tab struct {
	parent      *Component
	uri         workspaceapi.URI
	closer      io.Closer
	handler     browserapi.Handler
	free        bool
	win         Window
	subscribers []TabSubscriber
	prev        *Tab

	origName     string
	manualRename bool
}

// Resize satisfies tui.Component
func (b *Tab) Resize(width, height int) {
	b.handler.Resize(width, height)
}

// Draw satisfies tui.Component
func (b *Tab) Draw(w term.Writer) {
	b.handler.Draw(w)
}

// Handle satisfies tui.Handler
func (b *Tab) Handle(ev term.Event) (exit, handled bool) {
	innerExit, handled := b.handler.Handle(ev)
	if innerExit {
		b.parent.RemoveTab(b)
	}
	return false, handled
}

// Cursor satisfies tui.Handler.
func (b *Tab) Cursor() (pos term.Coordinates, style term.CursorStyle, show bool) {
	return b.handler.Cursor()
}

// Selection satisfies tui.Handler.
func (b *Tab) Selection() (string, bool) {
	return b.handler.Selection()
}

// Close satisfies browser.Handler.
func (b *Tab) Close() error {
	err1 := b.handler.Close()
	if b.closer != nil {
		err2 := b.closer.Close()
		b.closer = nil
		return err2
	}
	return err1
}

// URI returns the identifier of this tab.
func (b *Tab) URI() workspaceapi.URI {
	return b.uri
}

// SetAttrs sets this tab's name and tab name attributes.
func (b *Tab) SetAttrs(name string, attrs term.Attributes) {
	b.parent.SetTabAttrs(b.uri, attrs)
}

// ResetAttrs resets this tab's name and tab name attributes.
func (b *Tab) ResetAttrs() {
	b.parent.ResetTabNameAndAttrs(b.uri)
}

// Window returns this tab's Window and true or nil and false
// if this tab is not currently active on any window.
func (b *Tab) Window() (Window, bool) {
	return b.win, !b.free
}

// Handler returns the Handler responsible for drawing
// the contents of this tab.
func (b *Tab) Handler() browserapi.Handler {
	return b.handler
}

// Closer returns the closer passed to browser.Component.NewTab,
// which is used when tab is closed via
func (b *Tab) Closer() io.Closer {
	return b.closer
}

// SetHandler atomically replaces this tab's handler and closer.
// Both the previous handler and the previous closer (if non-nil)
// have Close called on them so the caller cannot leak the resources
// they owned. The tab's URI, window association, name and
// attributes are preserved.
func (b *Tab) SetHandler(newHandler browserapi.Handler, newCloser io.Closer) error {
	oldHandler := b.handler
	oldCloser := b.closer
	b.handler = newHandler
	b.closer = newCloser
	var ret error
	if oldHandler != nil {
		ret = oldHandler.Close()
	}
	if oldCloser != nil {
		if err := oldCloser.Close(); err != nil {
			ret = errors.Join(ret, err)
		}
	}
	return ret
}

// TabSubscriber is a subscriber of tab focus or free operations.
type TabSubscriber interface {
	OnFocus(*Tab)
	OnFree(*Tab)
}

// Subscribe subscribes sub to OnFocus and OnFree operations.
func (b *Tab) Subscribe(sub TabSubscriber) {
	b.subscribers = append(b.subscribers, sub)
	if b.free {
		sub.OnFree(b)
	} else {
		sub.OnFocus(b)
	}
}

// SeekUp satisfies component.Scrollable.
func (b *Tab) SeekUp() bool {
	scrollable, ok := b.handler.(component.Scrollable)
	if !ok {
		return false
	}
	return scrollable.SeekUp()
}

// SeekDown satisfies component.Scrollable.
func (b *Tab) SeekDown() bool {
	scrollable, ok := b.handler.(component.Scrollable)
	if !ok {
		return false
	}
	return scrollable.SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (b *Tab) SeekOffset() int {
	scrollable, ok := b.handler.(component.Scrollable)
	if !ok {
		return 0
	}
	return min(scrollable.SeekOffset(), scrollable.MaxSeekOffset())
}

// MaxSeekOffset satisfies component.Scrollable.
func (b *Tab) MaxSeekOffset() int {
	scrollable, ok := b.handler.(component.Scrollable)
	if !ok {
		return 0
	}
	return scrollable.MaxSeekOffset()
}

// newTab allocates storage for a new tab and initializes it.
func newTab(
	c *Component, uri workspaceapi.URI, h browserapi.Handler, f io.Closer,
) *Tab {
	ret := new(Tab)
	ret.init(c, uri, h, f)
	return ret
}

// Init initializes this tab with id, h as the Handler, and f as the
// io.Closer handle.
func (b *Tab) init(
	c *Component, uri workspaceapi.URI, h browserapi.Handler, f io.Closer,
) {
	b.parent = c
	b.uri = uri
	b.closer = f
	b.handler = h
	b.free = true
}

func (b *Tab) callOnFocus() {
	for _, sub := range b.subscribers {
		sub.OnFocus(b)
	}
}

// if prev is nil, then it means that we don't want
// to record it.
func (b *Tab) setWindow(prev *Tab, win Window) {
	b.free = false
	b.win = win
	if prev != nil {
		b.prev = prev
	}
}

func (b *Tab) setFree() {
	b.free = true
	b.win = nil
	for _, sub := range b.subscribers {
		sub.OnFree(b)
	}
}
