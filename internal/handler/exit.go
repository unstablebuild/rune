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

package handler

import (
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// KeyExit wraps a tui.Component which exits upon receiveing key.
func KeyExit(c tui.Component, key term.KeyComb) tui.Handler {
	return &keyExit{Component: c, key: key}
}

// KeyExitCallback wraps a tui.Component which exits and calls cb upon receiveing key.
func KeyExitCallback(c tui.Component, key term.KeyComb, cb func()) tui.Handler {
	return &keyExit{Component: c, key: key, cb: cb}
}

type keyExit struct {
	tui.Component
	key term.KeyComb
	cb  func()
}

func (e *keyExit) Handle(ev term.Event) (exit, handled bool) {
	exit = ev.Type == term.EventKey && e.key == ev.KeyComb()
	handled = exit
	if exit && e.cb != nil {
		e.cb()
	}
	return
}

func (e *keyExit) Cursor() (pos term.Coordinates, style term.CursorStyle, show bool) {
	return
}

func (e *keyExit) Selection() (string, bool) {
	return "", false
}
