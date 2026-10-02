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
	"context"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
)

type browserServer interface {
	browserrpc.WindowManagerServer
	browserrpc.EventPublisherServer
	browserrpc.NotificationsServer
	browserrpc.ResourceOpenerServer
}

// this structure wraps a browser.Browser to
// provide interrupt on write requests coming from the wire
type interruptBrowser struct {
	browserrpc.UnimplementedEventPublisherServer
	browserrpc.UnimplementedNotificationsServer
	browserrpc.UnimplementedResourceOpenerServer
	browserrpc.UnimplementedWindowManagerServer
	browserServer browserServer
	interruptDraw func()
}

func interruptBrowserServer(srv browserServer, interruptDraw func()) browserServer {
	return &interruptBrowser{browserServer: srv, interruptDraw: interruptDraw}
}

// Focus satisfies browserrpc.BrowserServer
func (s *interruptBrowser) Publish(
	ctx context.Context, req *browserrpc.PublishRequest,
) (*browserrpc.PublishResponse, error) {
	res, err := s.browserServer.Publish(ctx, req)
	return res, err
}

// Focus satisfies browserrpc.BrowserServer
func (s *interruptBrowser) Focus(
	ctx context.Context, req *browserrpc.FocusRequest,
) (*browserrpc.FocusResponse, error) {
	res, err := s.browserServer.Focus(ctx, req)
	return res, err
}

// Floating satisfies browserrpc.BrowserServer
func (s *interruptBrowser) Floating(srv browserrpc.WindowManager_FloatingServer) error {
	err := s.browserServer.Floating(srv)
	s.interruptDraw()
	return err
}

// Tab satisfies browserrpc.BrowserServer
func (s *interruptBrowser) Tab(srv browserrpc.WindowManager_TabServer) error {
	err := s.browserServer.Tab(srv)
	s.interruptDraw()
	return err
}

// Split satisfies browserrpc.BrowserServer
func (s *interruptBrowser) Split(srv browserrpc.WindowManager_SplitServer) error {
	err := s.browserServer.Split(srv)
	s.interruptDraw()
	return err
}

// Bar satisfies browserrpc.BrowserServer
func (s *interruptBrowser) Bar(srv browserrpc.WindowManager_BarServer) error {
	err := s.browserServer.Bar(srv)
	s.interruptDraw()
	return err
}

// Notify satisfies browserrpc.BrowserServer
func (s *interruptBrowser) Notify(
	ctx context.Context, req *browserrpc.NotifyRequest,
) (*browserrpc.NotifyResponse, error) {
	res, err := s.browserServer.Notify(ctx, req)
	s.interruptDraw()
	return res, err
}

// UpdateNotificationProgress satisfies browserrpc.BrowserServer
func (s *interruptBrowser) UpdateNotificationProgress(
	ctx context.Context, req *browserrpc.UpdateNotificationProgressRequest,
) (*browserrpc.UpdateNotificationProgressResponse, error) {
	res, err := s.browserServer.UpdateNotificationProgress(ctx, req)
	s.interruptDraw()
	return res, err
}

// Open satisfies browserrpc.BrowserServer
func (s *interruptBrowser) Open(
	ctx context.Context, req *browserrpc.OpenResourceRequest,
) (*browserrpc.OpenResourceResponse, error) {
	res, err := s.browserServer.Open(ctx, req)
	s.interruptDraw()
	return res, err
}

// SetContent satisfies browserrpc.BrowserServer
func (s *interruptBrowser) SetContent(srv browserrpc.WindowManager_SetContentServer) error {
	err := s.browserServer.SetContent(srv)
	s.interruptDraw()
	return err
}

// CloseWindow satisfies browserrpc.BrowserServer
func (s *interruptBrowser) CloseWindow(
	ctx context.Context, req *browserrpc.WindowCloseRequest,
) (*browserrpc.WindowCloseResponse, error) {
	res, err := s.browserServer.CloseWindow(ctx, req)
	s.interruptDraw()
	return res, err
}

// SetTabActivity satisfies browserrpc.BrowserServer
func (s *interruptBrowser) SetTabActivity(
	ctx context.Context, req *browserrpc.SetTabActivityRequest,
) (*browserrpc.SetTabActivityResponse, error) {
	res, err := s.browserServer.SetTabActivity(ctx, req)
	s.interruptDraw()
	return res, err
}

// SetTabName satisfies browserrpc.BrowserServer
func (s *interruptBrowser) SetTabName(
	ctx context.Context, req *browserrpc.SetTabNameRequest,
) (*browserrpc.SetTabNameResponse, error) {
	res, err := s.browserServer.SetTabName(ctx, req)
	s.interruptDraw()
	return res, err
}
