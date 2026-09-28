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

package extension

import (
	"errors"
	"io"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/rpc"
	"unstable.build/rune/internal/text"
	ttextrpc "unstable.build/rune/internal/text/textrpc"
)

// EditorResources returns a map of Permission to a ResourceServer
// capable of serving each of the b Editor's resources.
func EditorResources(
	b browser.Notifications, ed text.Editor,
	publishEvent func(term.Event) bool,
) map[extensionapi.Permission]ResourceRegistrar {
	s := newEditorResourceServer(b, ed, publishEvent)
	return map[extensionapi.Permission]ResourceRegistrar{
		extensionapi.PermissionEditor: s,
	}
}

type editorResourceServer struct {
	b            browser.Notifications
	ed           text.Editor
	publishEvent func(term.Event) bool
}

func newEditorResourceServer(
	b browser.Notifications, ed text.Editor,
	publishEvent func(term.Event) bool,
) *editorResourceServer {
	ret := new(editorResourceServer)
	ret.ed = ed
	ret.b = b
	ret.publishEvent = publishEvent
	return ret
}

func (s *editorResourceServer) Register(
	registrar rpc.ServiceRegistrar, lock sync.Locker,
) (io.Closer, error) {
	server := ttextrpc.NewServer(s.b, s.ed, lock)
	server.SetEventPublisher(func(ev term.Event) error {
		if !s.publishEvent(ev) {
			return errors.New("event stream not ready")
		}
		return nil
	})
	textrpc.RegisterEditorServer(registrar,
		interruptEditorServer(server, func() {
			s.publishEvent(term.Event{Type: term.EventInterrupt})
		}))
	return server, nil
}
