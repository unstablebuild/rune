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
	"io"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	tbrowserrpc "unstable.build/rune/internal/browser/browserrpc"
	thandlerrpc "unstable.build/rune/internal/handler/handlerrpc"
	"unstable.build/rune/internal/rpc"
)

// BrowserResources returns a map of Permission to a ResourceServer
// capable of serving each of the b Browser's resources.
func BrowserResources(
	b browser.Browser, publishEvent func(term.Event) bool,
	graphics thandlerrpc.GraphicsConfig,
) map[extensionapi.Permission]ResourceRegistrar {
	s := newBrowserResourceServer(b, publishEvent, graphics)
	return map[extensionapi.Permission]ResourceRegistrar{
		extensionapi.PermissionBrowserWindowManager: s.forPermission(
			extensionapi.PermissionBrowserWindowManager),
		extensionapi.PermissionBrowserResourceOpener: s.forPermission(
			extensionapi.PermissionBrowserResourceOpener),
		extensionapi.PermissionNotifications: s.forPermission(
			extensionapi.PermissionNotifications),
		extensionapi.PermissionInterrupt: s.forPermission(
			extensionapi.PermissionInterrupt),
	}
}

type browserResourceServer struct {
	b            browser.Browser
	publishEvent func(term.Event) bool
	graphics     thandlerrpc.GraphicsConfig
}

type browserResourcePermissionServer struct {
	p extensionapi.Permission
	*browserResourceServer
}

func newBrowserResourceServer(
	b browser.Browser, publishEvent func(term.Event) bool,
	graphics thandlerrpc.GraphicsConfig,
) *browserResourceServer {
	ret := new(browserResourceServer)
	ret.b = b
	ret.publishEvent = publishEvent
	ret.graphics = graphics
	return ret
}

func (s *browserResourceServer) forPermission(p extensionapi.Permission) ResourceRegistrar {
	return browserResourcePermissionServer{p: p, browserResourceServer: s}
}

func (s browserResourcePermissionServer) Register(
	registrar rpc.ServiceRegistrar, lock sync.Locker,
) (io.Closer, error) {
	server := tbrowserrpc.NewServer(s.b, lock, s.graphics)
	rpcServer := interruptBrowserServer(server, func() {
		s.publishEvent(term.Event{Type: term.EventInterrupt})
	})
	switch s.p {
	case extensionapi.PermissionBrowserWindowManager:
		browserrpc.RegisterWindowManagerServer(registrar, rpcServer)
	case extensionapi.PermissionBrowserResourceOpener:
		browserrpc.RegisterResourceOpenerServer(registrar, rpcServer)
	case extensionapi.PermissionNotifications:
		browserrpc.RegisterNotificationsServer(registrar, rpcServer)
	case extensionapi.PermissionInterrupt:
		browserrpc.RegisterEventPublisherServer(registrar, rpcServer)
	}
	return browserCloser{server}, nil
}

type browserCloser struct {
	server *tbrowserrpc.Server
}

func (b browserCloser) Close() error {
	return b.server.Stop()
}
