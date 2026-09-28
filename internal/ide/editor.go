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
	"errors"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"

	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

type currentEditor struct {
	root *workspaceManagerHandler
}

var _ text.Editor = currentEditor{}

func (c currentEditor) editor() text.Editor {
	if c.root == nil {
		return nil
	}
	ex := c.root.focusEx()
	if ex == nil {
		return nil
	}
	return ex.ed
}

var errNoEditor = errors.New("no focused editor")

func (c currentEditor) Edit(
	ctx context.Context, file workspaceapi.URI, buf *cell.Buffer,
	readOnly, recovered bool,
) (text.Handler, error) {
	ed := c.editor()
	if ed == nil {
		return nil, errNoEditor
	}
	return ed.Edit(ctx, file, buf, readOnly, recovered)
}

func (c currentEditor) Editor(uri workspaceapi.URI) (text.Handler, error) {
	ed := c.editor()
	if ed == nil {
		return nil, errNoEditor
	}
	return ed.Editor(uri)
}

func (c currentEditor) SubscribeCommand(
	m textapi.CommandManual, h text.CommandHandler,
) error {
	ed := c.editor()
	if ed == nil {
		return errNoEditor
	}
	return ed.SubscribeCommand(m, h)
}

func (c currentEditor) RegisterREPLCommand(
	m textapi.CommandManual, h textapi.REPLHandler,
) error {
	ed := c.editor()
	if ed == nil {
		return errNoEditor
	}
	return ed.RegisterREPLCommand(m, h)
}

func (c currentEditor) UnsubscribeCommand(name string) error {
	ed := c.editor()
	if ed == nil {
		return errNoEditor
	}
	return ed.UnsubscribeCommand(name)
}

func (c currentEditor) UnregisterREPLCommand(name string) error {
	ed := c.editor()
	if ed == nil {
		return errNoEditor
	}
	return ed.UnregisterREPLCommand(name)
}

func (c currentEditor) RegisterResourceOpener(
	scheme string, h textapi.ResourceOpenHandler,
) error {
	ed := c.editor()
	if ed == nil {
		return errNoEditor
	}
	return ed.RegisterResourceOpener(scheme, h)
}

func (c currentEditor) UnregisterResourceOpener(scheme string) error {
	ed := c.editor()
	if ed == nil {
		return errNoEditor
	}
	return ed.UnregisterResourceOpener(scheme)
}

func (c currentEditor) IsExternal() bool {
	ed := c.editor()
	if ed == nil {
		return false
	}
	return ed.IsExternal()
}

func (c currentEditor) SubscribeEvents(
	types []textapi.EventType, h text.EventHandler,
) error {
	ed := c.editor()
	if ed == nil {
		return errNoEditor
	}
	return ed.SubscribeEvents(types, h)
}

func (c currentEditor) UnsubscribeEvents(h text.EventHandler) (bool, error) {
	ed := c.editor()
	if ed == nil {
		return false, errNoEditor
	}
	return ed.UnsubscribeEvents(h)
}
