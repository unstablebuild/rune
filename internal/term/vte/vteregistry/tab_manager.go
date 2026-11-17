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

package vteregistry

import (
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
)

// tabManager hands the emulator's tab and status calls to the host,
// but for the exit of a detached terminal's process, which deletes it.
type tabManager struct{ c *canonical }

func (t tabManager) Tab(
	uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
) (browserapi.Handler, error) {
	return t.c.r.tm.Tab(uri, icon, name, h)
}

func (t tabManager) SetTabName(uri workspaceapi.URI, name string, attr term.Attributes) error {
	return t.c.r.tm.SetTabName(uri, name, attr)
}

func (t tabManager) OnTabExit(uri workspaceapi.URI) bool {
	if t.c.exited() {
		return true
	}
	t.c.mu.Lock()
	destroyed := t.c.destroyed
	t.c.mu.Unlock()
	// A deleted terminal's process exiting is no news: its deletion was
	// the last change told of it.
	if !destroyed {
		t.c.changed(vte.LivenessUnknown)
	}
	return t.c.r.tm.OnTabExit(uri)
}

func (t tabManager) SetTabStatus(uri workspaceapi.URI, report programstatus.Report) error {
	return t.c.r.tm.SetTabStatus(uri, report)
}

func (t tabManager) EndTabStatus(uri workspaceapi.URI) error {
	return t.c.r.tm.EndTabStatus(uri)
}
