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
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
)

// routes calls to WindowManager to the current open workspace
type currentWorkspaceWindowManager struct {
	root *workspaceManagerHandler
}

var _ browserapi.WindowManager = currentWorkspaceWindowManager{}

func (c currentWorkspaceWindowManager) Focus() (browserapi.Window, error) {
	return c.root.focusBrowser().Focus()
}

// Split splits the current window in focus in two, and installs
// Handler in the new window.
func (c currentWorkspaceWindowManager) Split(
	o browserapi.Orientation, win browserapi.Window, h browserapi.Handler,
) (browserapi.Window, error) {
	return c.root.focusBrowser().Split(o, win.(browser.Window), h)
}

// Floating creates a new floating window at coordinates,
// with static width and height.
func (c currentWorkspaceWindowManager) Floating(
	h browserapi.Floating, cfg browserapi.FloatingConfig,
) (browserapi.Window, error) {
	return c.root.focusBrowser().Floating(h, cfg)
}

// Bar creates a status bar with Orientation and Handler.
// Bars differ from Split and Floating windows in that they can't
// be in focus and can only receive mouse events.
func (c currentWorkspaceWindowManager) Bar(
	cfg browserapi.BarConfig, h tui.Handler,
) error {
	return c.root.focusBrowser().Bar(cfg, h)
}

// Tab creates a new tab with h and returns a handle that can be
// used with the rest of methods that take a browser.Handler.
// URI is used to uniquely identify a tab and name is used as a label
// to display it in the tab bar.
func (c currentWorkspaceWindowManager) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler) (
	browserapi.Handler, error,
) {
	return c.root.focusBrowser().Tab(uri, icon, name, h)
}

// SetWindowContent sets the content of the given window to the given handler.
func (c currentWorkspaceWindowManager) SetWindowContent(
	win browserapi.Window, h browserapi.Handler,
) error {
	return win.(browser.Window).SetContent(h)
}

// Close closes the given window.
func (c currentWorkspaceWindowManager) CloseWindow(win browserapi.Window) error {
	return win.(browser.Window).Close()
}

// SetTabActivity marks the tab identified by uri in the current
// workspace as active or idle.
func (c currentWorkspaceWindowManager) SetTabActivity(
	uri workspaceapi.URI, active bool,
) error {
	return c.root.focusBrowser().SetTabActivity(uri, active)
}
