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
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	tbrowserrpc "unstable.build/rune/internal/browser/browserrpc"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/rpc"
)

// browserHost is the sandbox's real headless browser backend. It wraps
// a live browser.Component and adapts it to the browser.Browser
// interface that browserrpc.Server drives, mapping the component's
// (Window, bool) returns to (Window, error). Unlike stubBrowser it
// installs the handlers extensions open through Split/Bar/Tab/Floating
// into a real tiling component, so a spec can render them to a string
// and send them key events.
//
// The component is guarded by mu, the same lock browserrpc.Server
// unlocks around blocking stream I/O. render and send_key acquire mu
// before driving the component so they never race the RPC handlers.
type browserHost struct {
	comp *browser.Component
	mu   sync.Mutex

	notifier *recordingNotifier

	width, height int
}

var _ browser.Browser = (*browserHost)(nil)

// newBrowserHost returns a headless browser backend sized to width x
// height. The size is fixed so installs resize their new child
// synchronously, which flushes the install-response handshake back to
// the extension without the host having to render first.
func newBrowserHost(width, height int) *browserHost {
	h := &browserHost{
		notifier: &recordingNotifier{},
		width:    width,
		height:   height,
	}
	cfg := browser.DefaultConfig()
	cfg.Notifications = h.notifier
	h.comp = browser.NewComponent(cfg)
	h.comp.Resize(width, height)
	return h
}

func (h *browserHost) Focus() (browser.Window, error) {
	return h.comp.Focus(), nil
}

// focusedWindow returns the browser's currently focused window under
// h.mu. Command dispatch passes it as textapi.Command.Window so
// window-manager operations like Split resolve a real target instead
// of window id 0.
func (h *browserHost) focusedWindow() browser.Window {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.comp.Focus()
}

func (h *browserHost) SetFocus(win browser.Window) (browser.Window, error) {
	return h.comp.SetFocus(win), nil
}

func (h *browserHost) Window(id uint64) (browser.Window, bool) {
	return h.comp.Window(id)
}

func (h *browserHost) IterateWindows(fn func(browser.Window)) {
	h.comp.IterateWindows(fn)
}

func (h *browserHost) Split(
	o browserapi.Orientation, win browser.Window, handler browserapi.Handler,
) (browser.Window, error) {
	out, ok := h.comp.Split(o, win, handler)
	if !ok {
		return nil, errors.New("invalid split")
	}
	return out, nil
}

func (h *browserHost) Floating(
	handler browser.Floating, cfg browserapi.FloatingConfig,
) (browser.Window, error) {
	return h.comp.Floating(handler, cfg), nil
}

func (h *browserHost) Bar(cfg browserapi.BarConfig, handler tui.Handler) error {
	if cfg.Size <= 0 {
		return fmt.Errorf("invalid bar size: %d", cfg.Size)
	}
	h.comp.Bar(cfg, handler)
	return nil
}

func (h *browserHost) Tab(
	uri workspaceapi.URI, icon rune, name string, handler browserapi.Handler,
) (browserapi.Handler, error) {
	if t, ok := h.comp.Tab(uri); ok {
		return t, nil
	}
	return h.comp.NewTab(uri, icon, name, handler, nil), nil
}

func (h *browserHost) SetTabName(
	uri workspaceapi.URI, name string, attr term.Attributes,
) error {
	if !h.comp.SetTabDefaultNameAndAttrs(uri, name, attr) {
		return errors.New("set tab name called on unknown tab")
	}
	_ = h.comp.SetTabNameAndAttrs(uri, name, attr)
	return nil
}

func (h *browserHost) OnTabExit(uri workspaceapi.URI) bool {
	return h.comp.OnTabExit(uri)
}

func (h *browserHost) Open(uri workspaceapi.URI) (browserapi.Handler, error) {
	if t, ok := h.comp.Tab(uri); ok {
		return t, nil
	}
	return h.comp.NewTab(uri, 0, uri.Name(), browsertest.NewTestHandler(), nil), nil
}

func (h *browserHost) Resource(uri workspaceapi.URI) (browserapi.Handler, bool) {
	return h.comp.Tab(uri)
}

func (h *browserHost) PublishEvent(term.Event) error { return nil }

func (h *browserHost) DragHover(pos term.Coordinates) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.comp.DragHover(pos)
}

func (h *browserHost) DragCancel() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.comp.DragCancel()
}

func (h *browserHost) DragDrop(pos term.Coordinates, paths []string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.comp.DragDrop(pos, paths)
}

func (h *browserHost) Close() error { return h.comp.Close() }

func (h *browserHost) Notify(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return h.notifier.Notify(level, msg, args...)
}

func (h *browserHost) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return h.notifier.NotifyOnce(level, msg, args...)
}

func (h *browserHost) UpdateNotificationProgress(
	id, message string, progress, total int64,
) error {
	return h.notifier.UpdateNotificationProgress(id, message, progress, total)
}

// recordingNotifier is a browser.Notifications that returns a stable
// non-empty id for every notification. Extensions that gate follow-up
// calls (e.g. UpdateNotificationProgress) on a non-empty id therefore
// still exercise those paths, mirroring stubBrowser's old behavior.
type recordingNotifier struct {
	seq atomic.Int64
}

var _ browser.Notifications = (*recordingNotifier)(nil)

func (n *recordingNotifier) Notify(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return fmt.Sprintf("notification-%d", n.seq.Add(1)), nil
}

func (n *recordingNotifier) NotifyOnce(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	return fmt.Sprintf("notification-%d", n.seq.Add(1)), nil
}

func (n *recordingNotifier) UpdateNotificationProgress(
	string, string, int64, int64,
) error {
	return nil
}

// resources returns the ResourceRegistrars that serve this browser
// host's window manager, resource opener, notifications, and event
// publisher over gRPC. All four services share a single sync-mode
// browserrpc.Server so they operate on the same live component, and
// the server drives installed handler streams synchronously — the
// prerequisite for rendering them and sending them key events.
//
// The server is guarded by h.mu, the same lock render and send_key
// take, so host-driven renders never race the RPC handlers.
func (h *browserHost) resources() map[extensionapi.Permission]extension.ResourceRegistrar {
	server := tbrowserrpc.NewServer(h, &h.mu)
	server.SetSyncMode()
	reg := &browserServerRegistrar{server: server}
	return map[extensionapi.Permission]extension.ResourceRegistrar{
		extensionapi.PermissionBrowserWindowManager:  reg.forService(registerWindowManager),
		extensionapi.PermissionBrowserResourceOpener: reg.forService(registerResourceOpener),
		extensionapi.PermissionNotifications:         reg.forService(registerNotifications),
		extensionapi.PermissionInterrupt:             reg.forService(registerEventPublisher),
	}
}

type registerFn func(rpc.ServiceRegistrar, *tbrowserrpc.Server)

func registerWindowManager(r rpc.ServiceRegistrar, s *tbrowserrpc.Server) {
	browserrpc.RegisterWindowManagerServer(r, s)
}

func registerResourceOpener(r rpc.ServiceRegistrar, s *tbrowserrpc.Server) {
	browserrpc.RegisterResourceOpenerServer(r, s)
}

func registerNotifications(r rpc.ServiceRegistrar, s *tbrowserrpc.Server) {
	browserrpc.RegisterNotificationsServer(r, s)
}

func registerEventPublisher(r rpc.ServiceRegistrar, s *tbrowserrpc.Server) {
	browserrpc.RegisterEventPublisherServer(r, s)
}

type browserServerRegistrar struct {
	server *tbrowserrpc.Server
	closed sync.Once
}

func (b *browserServerRegistrar) forService(register registerFn) extension.ResourceRegistrar {
	return browserServiceRegistrar{parent: b, register: register}
}

type browserServiceRegistrar struct {
	parent   *browserServerRegistrar
	register registerFn
}

// Register satisfies extension.ResourceRegistrar. The runner-supplied
// locker is ignored: the server is bound to h.mu so RPC handlers and
// host renders serialize on the same lock.
func (b browserServiceRegistrar) Register(
	registrar rpc.ServiceRegistrar, _ sync.Locker,
) (io.Closer, error) {
	b.register(registrar, b.parent.server)
	return browserServerCloser{b.parent}, nil
}

type browserServerCloser struct {
	parent *browserServerRegistrar
}

func (c browserServerCloser) Close() error {
	c.parent.closed.Do(func() { _ = c.parent.server.Stop() })
	return nil
}
