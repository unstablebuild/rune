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
	"io"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// Floating is a Handler used for Floating windows.
// See handler.Floating for more details.
type Floating interface {
	browserapi.Handler
	handler.Floating
}

// Scrollable is a Handler that can scroll content up and down.
type Scrollable interface {
	browserapi.Handler
	component.Scrollable
}

// ScrollableFloating is a Floating that can scroll content up and down.
type ScrollableFloating interface {
	browserapi.Floating
	component.Scrollable
}

// Window is the interface that represents
// a closeable window in a WindowManager.
type Window interface {
	// SetContent sets the content of this window to the given handler.
	SetContent(browserapi.Handler) error

	// Focus returns whether this window is in focus.
	Focus() (bool, error)

	// Close closes the window. This method is idempotent.
	Close() error

	// Content returns the content of this window.
	Content() (browserapi.Handler, error)

	// WindowID is the window identifier.
	WindowID() uint64

	// Closed returns true if this window has already been closed.
	Closed() bool

	// IsFloating returns true if window is a floating window,
	// or false if window is a tiled window.
	IsFloating() bool

	// IsMinimized returns true if this is a floating window and it's minimized.
	IsMinimized() (component.Alignment, bool)

	// MinimizeUp minimizes this window and displays it above the window manager,
	// if this window is a floating window.
	MinimizeUp(padding int) bool

	// MinimizeDown minimizes this window and displays it below the window manager,
	// if this window is a floating window.
	MinimizeDown(padding int) bool

	// MinimizeLeft minimizes this window and displays it left of the window manager,
	// if this window is a floating window.
	MinimizeLeft(padding int) bool

	// MinimizeRight minimizes this window and displays it left of the window manager,
	// if this window is a floating window.
	MinimizeRight(padding int) bool

	// Unminimize un-minimizes this window and displays it at the back at the front.
	Unminimize() bool

	// SetFrameAttr sets a Window's FrameCharSet default attributes.
	SetFrameAttr(attr term.Attributes) (term.Attributes, bool)

	// Position returns the top-left coordinate of this window within
	// its WindowManager.
	Position() term.Coordinates

	// Width returns the current rendered width of this window.
	Width() int

	// Height returns the current rendered height of this window.
	Height() int
}

// WindowManager is the interface that groups window and tab management methods.
type WindowManager interface {
	TabManager

	// Focus returns the current Window in focus.
	Focus() (Window, error)

	// Split splits the current window in focus in two, and installs
	// Handler in the new window.
	Split(browserapi.Orientation, Window, browserapi.Handler) (Window, error)

	// Floating creates a new floating window at coordinates,
	// with static width and height.
	Floating(h Floating, cfg browserapi.FloatingConfig) (Window, error)

	// Bar creates a status bar with Orientation and Handler.
	// Bars differ from Split and Floating windows in that they can't
	// be in focus and can only receive mouse events.
	Bar(browserapi.BarConfig, tui.Handler) error

	// Window returns a window with the given window ID or returns false
	// if now window with that ID exists.
	Window(uint64) (Window, bool)

	// SetFocus sets the window in focus and returns the previous window in focus.
	// It satisfies browser.Browser.
	SetFocus(win Window) (Window, error)

	// IterateWindows applies fn to each open window managed by this
	// WindowManager.
	IterateWindows(fn func(Window))
}

// TabManager is the interface that groups tab management methods.
type TabManager interface {
	// Tab creates a new tab with h and returns a handle that can be
	// used with the rest of methods that take a browser.Handler.
	// URI is used to uniquely identify a tab and name is used as a label
	// to display it in the tab bar.
	Tab(uri workspaceapi.URI, icon rune, name string, h browserapi.Handler) (
		browserapi.Handler, error,
	)

	// SetTabName sets the title and attributes of the title of the given tab.
	// If the given browserapi.Handler is not a tab, then this method returns an error.
	SetTabName(workspaceapi.URI, string, term.Attributes) error

	// OnTabExit drops the tab or window content identified by uri,
	// used by terminals whose child process exited. It must be called
	// on the host event loop.
	OnTabExit(uri workspaceapi.URI) bool

	// SetTabActivity marks the tab identified by uri as having work in
	// progress or as idle. See browserapi.WindowManager.SetTabActivity.
	SetTabActivity(uri workspaceapi.URI, active bool) error
}

// Notifications is the interface that wraps methods to display
// messages to the user.
type Notifications interface {
	Notify(level browserapi.NotificationLevel, msg string, args ...any) (string, error)
	NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error)
	UpdateNotificationProgress(id, message string, progress, total int64) error
}

// ResourceOpener is the interface that wraps the method Open.
type ResourceOpener interface {
	Open(resource workspaceapi.URI) (browserapi.Handler, error)
	Resource(workspaceapi.URI) (browserapi.Handler, bool)
}

// EventPublisher is the interface that wraps the method PublishEvent.
//
// PublishEvent must be safe to call from any goroutine without holding
// the host UI lock. Extension RPC and vte goroutines publish redraws
// while the render loop holds that lock for the whole of a tick, so an
// implementation that needs it would deadlock them against the loop.
type EventPublisher interface {
	PublishEvent(term.Event) error
}

// DragTarget is the interface that groups the methods to preview and
// receive a file drag from the host windowing system. Positions are
// relative to the browser's top-left corner.
type DragTarget interface {
	// DragHover marks the window under pos as the pending drop target
	// and reports whether a window was found.
	DragHover(pos term.Coordinates) bool
	// DragCancel clears the pending drop target.
	DragCancel()
	// DragDrop delivers paths to the window under pos and reports
	// whether a window received them.
	DragDrop(pos term.Coordinates, paths []string) bool
}

// Browser is an interface that groups methods to manipulate
// the user interface of a browser.
type Browser interface {
	WindowManager
	EventPublisher
	ResourceOpener
	Notifications
	DragTarget
	io.Closer
}

// FuncHandler returns a Handler by wrapping a tui.Handler
// with an Close callback.
func FuncHandler(h tui.Handler, doClose func() error) browserapi.Handler {
	return &closeHandler{Handler: h, doClose: doClose}
}

// NopHandler returns a Handler by wrapping a tui.Handler
// with an nop Close callback.
func NopHandler(h tui.Handler) browserapi.Handler {
	return &closeHandler{Handler: h, doClose: func() error { return nil }}
}

// StaticFloating wraps a Handler and returns a Floating that always
// return the same Dimensions values.
func StaticFloating(h browserapi.Handler, width, height int) Floating {
	return staticFloating{width: width, height: height, Handler: h}
}

// NopFloatingHandler wraps a handler.Floating and returns a Floating
// that does nothing when Close is called.
func NopFloatingHandler(h handler.Floating) Floating {
	return nopFloating{Floating: h}
}

// FuncFloatingHandler wraps a handler.Floating and returns a Floating
// that calls calls closeFn when Close is called.
func FuncFloatingHandler(h handler.Floating, closeFn func() error) Floating {
	return funcFloatingHandler{Floating: h, fn: closeFn}
}

// NopScrollableFloatingHandler wraps a handler.ScrollableFloating and returns a
// ScrollableFloating that does nothing when Close is called.
func NopScrollableFloatingHandler(h handler.ScrollableFloating) ScrollableFloating {
	return funcScrollableFloating{ScrollableFloating: h}
}

// FuncScrollableFloatingHandler wraps a handler.ScrollableFloating and returns a
// ScrollableFloating that does nothing when Close is called.
func FuncScrollableFloatingHandler(h handler.ScrollableFloating, fn func() error) ScrollableFloating {
	return funcScrollableFloating{ScrollableFloating: h, fn: fn}
}

// FuncFloating wraps a Handler and returns a Floating that
// calls dimFn when Dimensions is called.
func FuncFloating(h browserapi.Handler, dimFn func() (int, int)) Floating {
	return funcFloating{Handler: h, fn: dimFn}
}

type funcFloatingHandler struct {
	handler.Floating
	fn func() error
}

func (f funcFloatingHandler) Close() error {
	return f.fn()
}

type funcFloating struct {
	browserapi.Handler
	fn func() (int, int)
}

func (f funcFloating) Dimensions() (width, height int) {
	return f.fn()
}

type closeHandler struct {
	tui.Handler
	doClose func() error
}

func (h *closeHandler) Close() error {
	return h.doClose()
}

type staticFloating struct {
	browserapi.Handler
	width, height int
}

func (s staticFloating) Dimensions() (int, int) {
	return s.width, s.height
}

type nopFloating struct {
	handler.Floating
}

func (n nopFloating) Close() error {
	return nil
}

type funcScrollableFloating struct {
	handler.ScrollableFloating
	fn func() error
}

func (n funcScrollableFloating) Close() error {
	if n.fn != nil {
		return n.fn()
	}
	return nil
}
