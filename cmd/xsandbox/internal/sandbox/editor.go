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

package sandbox

import (
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/rpc"
	"unstable.build/rune/internal/text"
	ttextrpc "unstable.build/rune/internal/text/textrpc"
	"unstable.build/rune/internal/text/texttest"
)

// recordingEditor is the sandbox's host-side text.Editor. It records
// command and resource opener registrations so the spec can wait for
// them and invoke the registered handlers, routing invocations back to
// the extension over its streams.
type recordingEditor struct {
	*texttest.TestEditor

	mu       sync.Mutex
	commands map[string]text.CommandHandler
	repls    map[string]textapi.REPLHandler
	openers  map[string]textapi.ResourceOpenHandler
	// changed is closed and replaced whenever a registration is added
	// so waiters can block instead of polling.
	changed chan struct{}
}

func newRecordingEditor() *recordingEditor {
	return &recordingEditor{
		TestEditor: texttest.NopEditor(),
		commands:   make(map[string]text.CommandHandler),
		repls:      make(map[string]textapi.REPLHandler),
		openers:    make(map[string]textapi.ResourceOpenHandler),
		changed:    make(chan struct{}),
	}
}

func (e *recordingEditor) signalLocked() {
	close(e.changed)
	e.changed = make(chan struct{})
}

// resources returns the ResourceRegistrar that serves this editor over
// gRPC, notifying through n. The server drives the content resource
// openers return synchronously, the prerequisite for rendering it, as the
// browser server does for the handlers extensions install.
func (e *recordingEditor) resources(
	n browser.Notifications,
) map[extensionapi.Permission]extension.ResourceRegistrar {
	return map[extensionapi.Permission]extension.ResourceRegistrar{
		extensionapi.PermissionEditor: editorRegistrar{editor: e, notifications: n},
	}
}

type editorRegistrar struct {
	editor        *recordingEditor
	notifications browser.Notifications
}

func (r editorRegistrar) Register(
	registrar rpc.ServiceRegistrar, lock sync.Locker,
) (io.Closer, error) {
	server := ttextrpc.NewServer(r.notifications, r.editor, lock)
	server.SetSyncMode()
	textrpc.RegisterEditorServer(registrar, server)
	return server, nil
}

// SubscribeCommand satisfies text.Editor.
func (e *recordingEditor) SubscribeCommand(
	cmd textapi.CommandManual, h text.CommandHandler,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.commands[cmd.Name] = h
	e.signalLocked()
	return nil
}

// UnsubscribeCommand satisfies text.Editor.
func (e *recordingEditor) UnsubscribeCommand(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.commands[name]; !ok {
		return text.ErrCommandNotRegistered
	}
	delete(e.commands, name)
	return nil
}

// RegisterREPLCommand satisfies text.Editor.
func (e *recordingEditor) RegisterREPLCommand(
	cmd textapi.CommandManual, h textapi.REPLHandler,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.repls[cmd.Name] = h
	e.signalLocked()
	return nil
}

// UnregisterREPLCommand satisfies text.Editor.
func (e *recordingEditor) UnregisterREPLCommand(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.repls[name]; !ok {
		return text.ErrCommandNotRegistered
	}
	delete(e.repls, name)
	return nil
}

// RegisterResourceOpener satisfies text.Editor.
func (e *recordingEditor) RegisterResourceOpener(
	scheme string, h textapi.ResourceOpenHandler,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.openers[scheme] = h
	e.signalLocked()
	return nil
}

// UnregisterResourceOpener satisfies text.Editor.
func (e *recordingEditor) UnregisterResourceOpener(scheme string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.openers[scheme]; !ok {
		return text.ErrResourceOpenerNotRegistered
	}
	delete(e.openers, scheme)
	return nil
}

func (e *recordingEditor) lookupOpener(scheme string) (textapi.ResourceOpenHandler, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, ok := e.openers[scheme]
	return h, ok
}

// waitOpener blocks until the extension registers a resource opener for
// scheme, or the timeout expires.
func (e *recordingEditor) waitOpener(
	scheme string, timeout time.Duration, done <-chan struct{},
) (textapi.ResourceOpenHandler, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		e.mu.Lock()
		h, ok := e.openers[scheme]
		changed := e.changed
		e.mu.Unlock()
		if ok {
			return h, nil
		}
		select {
		case <-changed:
		case <-deadline.C:
			return nil, fmt.Errorf(
				"resource opener for %q was not registered within %s (registered: %v)",
				scheme, timeout, e.openerSchemes())
		case <-done:
			return nil, fmt.Errorf(
				"sandbox stopped while waiting for the resource opener for %q", scheme)
		}
	}
}

func (e *recordingEditor) openerSchemes() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	schemes := make([]string, 0, len(e.openers))
	for scheme := range e.openers {
		schemes = append(schemes, scheme)
	}
	return schemes
}

func (e *recordingEditor) lookup(name string, repl bool) (any, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if repl {
		h, ok := e.repls[name]
		return h, ok
	}
	h, ok := e.commands[name]
	return h, ok
}

// waitRegistered blocks until a command with the given name has been
// registered by the extension, or the timeout expires.
func (e *recordingEditor) waitRegistered(
	name string, repl bool, timeout time.Duration, done <-chan struct{},
) (any, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		e.mu.Lock()
		var h any
		var ok bool
		if repl {
			h, ok = e.repls[name]
		} else {
			h, ok = e.commands[name]
		}
		changed := e.changed
		e.mu.Unlock()
		if ok {
			return h, nil
		}
		select {
		case <-changed:
		case <-deadline.C:
			return nil, fmt.Errorf(
				"command %q was not registered within %s (registered: %v)",
				name, timeout, e.registeredNames(repl))
		case <-done:
			return nil, fmt.Errorf("sandbox stopped while waiting for command %q", name)
		}
	}
}

func (e *recordingEditor) registeredNames(repl bool) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var names []string
	if repl {
		for name := range e.repls {
			names = append(names, name)
		}
		return names
	}
	for name := range e.commands {
		names = append(names, name)
	}
	return names
}
