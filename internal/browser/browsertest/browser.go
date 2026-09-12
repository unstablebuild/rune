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

package browsertest

import (
	"context"
	"errors"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
)

// BrowserFromAPIBrowser wraps a browserapi.Browser and returns
// a browser.Browser.
func BrowserFromAPIBrowser(b browserapi.Browser) browser.Browser {
	return toBrowser{b: b}
}

type toBrowser struct {
	b browserapi.Browser
}

func (b toBrowser) Focus() (browser.Window, error) {
	win, err := b.b.Focus()
	if err != nil {
		return nil, err
	}
	return WindowFromAPIWindow{Browser: b.b, Win: win}, nil
}

func (b toBrowser) SetTabName(uri workspaceapi.URI, name string, attr term.Attributes) error {
	return nil
}

func (b toBrowser) OnTabExit(uri workspaceapi.URI) bool {
	return false
}

func (b toBrowser) SetFocus(browser.Window) (browser.Window, error) {
	return nil, errors.New("unimplemented")
}

func (b toBrowser) Split(
	o browserapi.Orientation, win browser.Window, h browserapi.Handler,
) (browser.Window, error) {
	var inWin browserapi.Window
	if a, ok := win.(WindowFromAPIWindow); ok {
		inWin = a.Win
	} else {
		inWin = WindowToAPIWindow{Win: win}
	}
	retWin, err := b.b.Split(o, inWin, h)
	if err != nil {
		return nil, err
	}
	return WindowFromAPIWindow{Browser: b.b, Win: retWin}, nil
}

func (b toBrowser) Floating(
	h browser.Floating, cfg browserapi.FloatingConfig,
) (browser.Window, error) {
	retWin, err := b.b.Floating(h, cfg)
	if err != nil {
		return nil, err
	}
	return WindowFromAPIWindow{Browser: b.b, Win: retWin}, nil
}

func (b toBrowser) Bar(o browserapi.BarConfig, h tui.Handler) error {
	return b.b.Bar(o, h)
}

func (b toBrowser) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (browserapi.Handler, error) {
	return b.b.Tab(uri, icon, name, h)
}

func (b toBrowser) Window(id uint64) (browser.Window, bool) {
	return NopWindow(), true
}

func (b toBrowser) IterateWindows(fn func(browser.Window)) {
	// browserapi.Browser does not expose iteration over windows.
	// The test stub leaves it as a no-op so callers that only
	// happen to satisfy WindowManager via this adapter still
	// compile.
}

func (b toBrowser) Notify(level browserapi.NotificationLevel, msg string, args ...any) (
	string, error,
) {
	return b.b.Notify(level, msg, args...)
}

func (b toBrowser) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return b.b.NotifyOnce(level, msg, args...)
}

func (b toBrowser) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return b.b.UpdateNotificationProgress(id, message, progress, total)
}

func (b toBrowser) Open(resource workspaceapi.URI) (browserapi.Handler, error) {
	return b.b.Open(resource)
}

func (b toBrowser) Resource(u workspaceapi.URI) (browserapi.Handler, bool) {
	return NewTestHandler(), true
}

func (b toBrowser) PublishEvent(ev term.Event) error {
	switch ev.Type {
	case term.EventInterrupt:
		return b.b.Interrupt(context.Background())
	case term.EventNone:
		return b.b.PublishEventNone()
	}
	return fmt.Errorf("cannot publish event type: %v", ev.Type)
}

// DragHover satisfies browser.DragTarget. browserapi.Browser has no
// drag surface, so the adapter reports no drop target.
func (b toBrowser) DragHover(term.Coordinates) bool { return false }

// DragCancel satisfies browser.DragTarget.
func (b toBrowser) DragCancel() {}

// DragDrop satisfies browser.DragTarget.
func (b toBrowser) DragDrop(term.Coordinates, []string) bool { return false }

func (b toBrowser) Close() error {
	return b.b.Close()
}
