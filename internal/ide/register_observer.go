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
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

var _ text.Editor = (*commandRegisterObserver)(nil)

// commandRegisterObserver wraps a text.Editor and observes the command
// and resource opener registrations made through it.
type commandRegisterObserver struct {
	ed text.Editor

	// onResourceOpener, if set, is called after a resource opener is
	// registered, with its scheme, while the caller of the registration
	// may still hold the editor lock.
	onResourceOpener func(scheme string)

	mu         sync.Mutex
	registered map[string]struct{}
	waiters    map[string][]chan struct{}
}

func newCommandRegisterObserver(ed text.Editor) *commandRegisterObserver {
	return &commandRegisterObserver{
		ed:         ed,
		registered: map[string]struct{}{},
		waiters:    map[string][]chan struct{}{},
	}
}

func (o *commandRegisterObserver) Edit(
	ctx context.Context, file workspaceapi.URI, buf *cell.Buffer,
	readOnly, recovered bool,
) (text.Handler, error) {
	return o.ed.Edit(ctx, file, buf, readOnly, recovered)
}

func (o *commandRegisterObserver) Editor(
	uri workspaceapi.URI,
) (text.Handler, error) {
	return o.ed.Editor(uri)
}

func (o *commandRegisterObserver) UnsubscribeCommand(cmd string) error {
	return o.ed.UnsubscribeCommand(cmd)
}

func (o *commandRegisterObserver) UnregisterREPLCommand(cmd string) error {
	return o.ed.UnregisterREPLCommand(cmd)
}

func (o *commandRegisterObserver) IsExternal() bool {
	return o.ed.IsExternal()
}

func (o *commandRegisterObserver) SubscribeEvents(
	types []textapi.EventType, h text.EventHandler,
) error {
	return o.ed.SubscribeEvents(types, h)
}

func (o *commandRegisterObserver) UnsubscribeEvents(
	h text.EventHandler,
) (bool, error) {
	return o.ed.UnsubscribeEvents(h)
}

func (o *commandRegisterObserver) SubscribeCommand(
	cmd textapi.CommandManual, h text.CommandHandler,
) error {
	if err := o.ed.SubscribeCommand(cmd, h); err != nil {
		return err
	}
	o.markRegistered(cmd.Name)
	return nil
}

func (o *commandRegisterObserver) RegisterREPLCommand(
	cmd textapi.CommandManual, h textapi.REPLHandler,
) error {
	if err := o.ed.RegisterREPLCommand(cmd, h); err != nil {
		return err
	}
	o.markRegistered(cmd.Name)
	return nil
}

func (o *commandRegisterObserver) RegisterResourceOpener(
	scheme string, h textapi.ResourceOpenHandler,
) error {
	if err := o.ed.RegisterResourceOpener(scheme, h); err != nil {
		return err
	}
	if o.onResourceOpener != nil {
		o.onResourceOpener(scheme)
	}
	return nil
}

func (o *commandRegisterObserver) UnregisterResourceOpener(scheme string) error {
	return o.ed.UnregisterResourceOpener(scheme)
}

func (o *commandRegisterObserver) markRegistered(cmd string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.registered[cmd] = struct{}{}
	for _, ch := range o.waiters[cmd] {
		close(ch)
	}
	delete(o.waiters, cmd)
}

// Wait blocks until cmd has been registered through this editor or ctx
// is done. It returns immediately if cmd is already registered.
func (o *commandRegisterObserver) Wait(ctx context.Context, cmd string) error {
	o.mu.Lock()
	if _, ok := o.registered[cmd]; ok {
		o.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	o.waiters[cmd] = append(o.waiters[cmd], ch)
	o.mu.Unlock()

	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		o.removeWaiter(cmd, ch)
		return ctx.Err()
	}
}

func (o *commandRegisterObserver) removeWaiter(cmd string, ch chan struct{}) {
	o.mu.Lock()
	defer o.mu.Unlock()
	waiters := o.waiters[cmd]
	for i, w := range waiters {
		if w == ch {
			o.waiters[cmd] = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
	if len(o.waiters[cmd]) == 0 {
		delete(o.waiters, cmd)
	}
}
