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

package ide

import (
	"context"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
)

var _ browserapi.Browser = (*browserAdapter)(nil)

func newBrowserAdapter(b browser.Browser) browserapi.Browser {
	return browserAdapter{b: b}
}

type browserAdapter struct {
	b browser.Browser
}

func (a browserAdapter) Focus() (
	browserapi.Window, error,
) {
	return a.b.Focus()
}

func (a browserAdapter) Split(
	o browserapi.Orientation,
	w browserapi.Window,
	h browserapi.Handler,
) (browserapi.Window, error) {
	bw, ok := a.b.Window(w.WindowID())
	if !ok {
		return nil, fmt.Errorf("window %d not found", w.WindowID())
	}
	return a.b.Split(o, bw, h)
}

func (a browserAdapter) Floating(
	h browserapi.Floating,
	cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	return a.b.Floating(h, cfg)
}

func (a browserAdapter) Bar(
	cfg browserapi.BarConfig, h tui.Handler,
) error {
	return a.b.Bar(cfg, h)
}

func (a browserAdapter) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (browserapi.Handler, error) {
	return a.b.Tab(uri, icon, name, h)
}

func (a browserAdapter) SetWindowContent(
	w browserapi.Window, h browserapi.Handler,
) error {
	bw, ok := a.b.Window(w.WindowID())
	if !ok {
		return fmt.Errorf("window %d not found", w.WindowID())
	}
	return bw.SetContent(h)
}

func (a browserAdapter) CloseWindow(
	w browserapi.Window,
) error {
	bw, ok := a.b.Window(w.WindowID())
	if !ok {
		return fmt.Errorf("window %d not found", w.WindowID())
	}
	return bw.Close()
}

func (a browserAdapter) SetTabActivity(
	uri workspaceapi.URI, active bool,
) error {
	return a.b.SetTabActivity(uri, active)
}

func (a browserAdapter) Interrupt(
	ctx context.Context,
) error {
	return a.b.PublishEvent(term.Event{
		Type:    term.EventInterrupt,
		Context: ctx,
	})
}

func (a browserAdapter) PublishEventNone() error {
	return a.b.PublishEvent(term.Event{
		Type: term.EventNone,
	})
}

func (a browserAdapter) Open(
	uri workspaceapi.URI,
) (browserapi.Handler, error) {
	return a.b.Open(uri)
}

func (a browserAdapter) Notify(
	level browserapi.NotificationLevel,
	msg string,
	args ...any,
) (string, error) {
	return a.b.Notify(level, msg, args...)
}

func (a browserAdapter) NotifyOnce(
	level browserapi.NotificationLevel,
	msg string,
	args ...any,
) (string, error) {
	return a.b.NotifyOnce(level, msg, args...)
}

func (a browserAdapter) UpdateNotificationProgress(
	id, message string,
	progress, total int64,
) error {
	return a.b.UpdateNotificationProgress(
		id, message, progress, total,
	)
}

func (a browserAdapter) Close() error {
	return a.b.Close()
}
