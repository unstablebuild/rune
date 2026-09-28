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

package fileexplorer

import (
	"context"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// Window represents a closeable window in a WindowManager.
type Window interface {
	// SetContent sets the content of this window.
	SetContent(browserapi.Handler) error

	// Focus returns whether this window is in focus.
	Focus() (bool, error)

	// Close closes the window. This method is idempotent.
	Close() error

	// Content returns the content of this window.
	Content() (browserapi.Handler, error)

	// WindowID is the window identifier.
	WindowID() uint64

	// Closed returns true if this window has been closed.
	Closed() bool

	// IsFloating returns true if window is floating.
	IsFloating() bool

	// IsMinimized returns true if this floating window
	// is minimized.
	IsMinimized() (component.Alignment, bool)

	// MinimizeUp minimizes this floating window above
	// the window manager.
	MinimizeUp(padding int) bool

	// MinimizeDown minimizes this floating window below
	// the window manager.
	MinimizeDown(padding int) bool

	// MinimizeLeft minimizes this floating window to the
	// left of the window manager.
	MinimizeLeft(padding int) bool

	// MinimizeRight minimizes this floating window to
	// the right of the window manager.
	MinimizeRight(padding int) bool

	// Unminimize restores this floating window.
	Unminimize() bool

	// SetFrameAttr sets the window frame default
	// attributes.
	SetFrameAttr(
		term.Attributes,
	) (term.Attributes, bool)
}

// Browser is an interface that groups methods to
// manipulate the user interface of a browser.
type Browser interface {
	// Tab creates a new tab.
	Tab(
		uri workspaceapi.URI,
		icon rune,
		name string,
		h browserapi.Handler,
	) (browserapi.Handler, error)

	// SetTabName sets the title of the given tab.
	SetTabName(
		workspaceapi.URI, string, term.Attributes,
	) error

	// SetTabActivity marks the given tab as active or idle.
	SetTabActivity(workspaceapi.URI, bool) error

	// Focus returns the current Window in focus.
	Focus() (Window, error)

	// Split splits the current window in focus in two.
	Split(
		browserapi.Orientation,
		Window,
		browserapi.Handler,
	) (Window, error)

	// Floating creates a new floating window.
	Floating(
		browserapi.Floating,
		browserapi.FloatingConfig,
	) (Window, error)

	// Bar creates a status bar.
	Bar(browserapi.BarConfig, tui.Handler) error

	// Window returns a window by ID.
	Window(uint64) (Window, bool)

	// SetFocus sets the window in focus.
	SetFocus(Window) (Window, error)

	// PublishEvent publishes a terminal event.
	PublishEvent(term.Event) error

	// Open opens a resource.
	Open(workspaceapi.URI) (browserapi.Handler, error)

	// Resource returns a handler for a resource if open.
	Resource(
		workspaceapi.URI,
	) (browserapi.Handler, bool)

	// Notify delivers a message to the user.
	Notify(
		browserapi.NotificationLevel, string, ...any,
	) (string, error)

	// NotifyOnce delivers a message once.
	NotifyOnce(
		browserapi.NotificationLevel, string, ...any,
	) (string, error)

	// UpdateNotificationProgress updates progress.
	UpdateNotificationProgress(
		id, message string, progress, total int64,
	) error

	// Close closes the browser.
	Close() error
}

var _ browserapi.Browser = (*browserAdapter)(nil)

// NewBrowserAdapter adapts a Browser to
// browserapi.Browser. SetWindowContent and CloseWindow
// resolve the browserapi.Window to a Window via
// Browser.Window(id).
func NewBrowserAdapter(b Browser) browserapi.Browser {
	return &browserAdapter{b: b}
}

type browserAdapter struct {
	b Browser
}

func (a *browserAdapter) Focus() (
	browserapi.Window, error,
) {
	return a.b.Focus()
}

func (a *browserAdapter) Split(
	o browserapi.Orientation,
	w browserapi.Window,
	h browserapi.Handler,
) (browserapi.Window, error) {
	bw, ok := a.b.Window(w.WindowID())
	if !ok {
		return nil, fmt.Errorf(
			"window %d not found", w.WindowID(),
		)
	}
	return a.b.Split(o, bw, h)
}

func (a *browserAdapter) Floating(
	h browserapi.Floating,
	cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	return a.b.Floating(h, cfg)
}

func (a *browserAdapter) Bar(
	cfg browserapi.BarConfig, h tui.Handler,
) error {
	return a.b.Bar(cfg, h)
}

func (a *browserAdapter) Tab(
	uri workspaceapi.URI,
	icon rune,
	name string,
	h browserapi.Handler,
) (browserapi.Handler, error) {
	return a.b.Tab(uri, icon, name, h)
}

func (a *browserAdapter) SetWindowContent(
	w browserapi.Window, h browserapi.Handler,
) error {
	bw, ok := a.b.Window(w.WindowID())
	if !ok {
		return fmt.Errorf(
			"window %d not found", w.WindowID(),
		)
	}
	return bw.SetContent(h)
}

func (a *browserAdapter) CloseWindow(
	w browserapi.Window,
) error {
	bw, ok := a.b.Window(w.WindowID())
	if !ok {
		return fmt.Errorf(
			"window %d not found", w.WindowID(),
		)
	}
	return bw.Close()
}

func (a *browserAdapter) SetTabActivity(
	uri workspaceapi.URI, active bool,
) error {
	return a.b.SetTabActivity(uri, active)
}

func (a *browserAdapter) Interrupt(
	ctx context.Context,
) error {
	return a.b.PublishEvent(term.Event{
		Type:    term.EventInterrupt,
		Context: ctx,
	})
}

func (a *browserAdapter) PublishEventNone() error {
	return a.b.PublishEvent(term.Event{
		Type: term.EventNone,
	})
}

func (a *browserAdapter) Open(
	uri workspaceapi.URI,
) (browserapi.Handler, error) {
	return a.b.Open(uri)
}

func (a *browserAdapter) Notify(
	level browserapi.NotificationLevel,
	msg string,
	args ...any,
) (string, error) {
	return a.b.Notify(level, msg, args...)
}

func (a *browserAdapter) NotifyOnce(
	level browserapi.NotificationLevel,
	msg string,
	args ...any,
) (string, error) {
	return a.b.NotifyOnce(level, msg, args...)
}

func (a *browserAdapter) UpdateNotificationProgress(
	id, message string,
	progress, total int64,
) error {
	return a.b.UpdateNotificationProgress(
		id, message, progress, total,
	)
}

func (a *browserAdapter) Close() error {
	return a.b.Close()
}
