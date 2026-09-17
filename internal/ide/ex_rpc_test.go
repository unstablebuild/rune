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
	"net"
	_ "net/http/pprof"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/browser"
	tbrowserrpc "unstable.build/rune/internal/browser/browserrpc"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/ide/plugin"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/exoeditor"
	"unstable.build/rune/internal/text/texttest"
)

const testingShutdownWait = 500 * time.Millisecond

func nopPublishEvent(term.Event) bool {
	return true
}

type groupEventHandler struct {
	h  *browsertest.TestHandler
	wg *sync.WaitGroup
}

func (h *groupEventHandler) Handle(ev term.Event) (handled bool) {
	defer h.wg.Done()
	h.h.Handle(ev)
	return
}

// used to emulate term event loop synchronization
type safeHandler struct {
	mu        sync.Locker
	Handler   browserapi.Handler
	Component tui.Component
	// quiesce, when set, runs after mu is released so async work
	// started by the handled event can re-acquire mu and finish
	// before the next test step. Wired to
	// testWorkspaceManagerHandler.quiesce for IDE-level tests and to
	// ex.waitInflight for the RPC browser tests.
	quiesce func()
	close   func()
}

func (h *safeHandler) Resize(width, height int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Component.Resize(width, height)
}
func (h *safeHandler) Draw(w term.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Component.Draw(w)
}
func (h *safeHandler) Handle(ev term.Event) (exit, handled bool) {
	h.mu.Lock()
	exit, handled = h.Handler.Handle(ev)
	// workaround search.List non-determinism
	if w, ok := h.Handler.(interface{ Wait() }); ok {
		w.Wait()
	}
	h.mu.Unlock()
	if h.quiesce != nil {
		h.quiesce()
	}
	return
}
func (h *safeHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Cursor()
}

func (h *safeHandler) Selection() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Selection()
}

func (h *safeHandler) Close() error {
	if h.close != nil {
		h.close()
		return nil
	}
	return h.Handler.Close()
}

func newTestRPCBrowser(t *testing.T,
	destructor *func(),
	clip clipboard.Register,
	otherOpts ...text.Option,
) browserConstructor {
	return func(ed text.Editor, opts ...text.Option) (
		tui.Handler, browser.Browser, error,
	) {
		ex := new(ex)
		notifications := newWorkspaceNotifications(
			storagestub.NewInMemoryService(), notificationsConfig(),
			&workspaceManagerMock{workspace: ex})
		uri, err := workspaceapi.ParseURI("memory:///")
		require.NoError(t, err)
		svc := storagestub.NewInMemoryService()
		opts = append(opts, text.WithCommandOverlayConfig(testCommandOverlayConfig()))
		opts = append(opts, text.WithCommandKeyBinding(term.KeyComb{Ch: 'w', Mod: term.ModCtrl},
			[][]string{{"tabclose"}}))
		opts = append(opts, text.WithCommandKeyBinding(term.KeyComb{Ch: 'l', Mod: term.ModCtrl},
			[][]string{{"tabnext"}}))
		opts = append(opts, text.WithCommandKeyBinding(term.KeyComb{Ch: 'h', Mod: term.ModCtrl},
			[][]string{{"tabprevious"}}))
		opts = append(opts, otherOpts...)
		ex.syncCommandPrompt = true
		err = ex.init(func(exoeditor.Reloader, schemeapi.Terminal) (text.Editor, error) { return ed, nil }, &testLoader{}, svc, notifications, uri,
			vte.DefaultConfig(), plugin.DefaultBarConfig(),
			nopPublishEvent, 0, clip, nil, nil, nil, nil, testPromptEditor(), &sync.Mutex{}, opts...)
		if err != nil {
			return nil, nil, err
		}
		ex.subscribeCommands()
		lis, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		var serverMutex sync.Mutex
		grpcServer := grpc.NewServer()
		server := tbrowserrpc.NewServer(ex.Browser(), &serverMutex)
		server.SetSyncMode()
		browserrpc.RegisterWindowManagerServer(grpcServer, server)
		browserrpc.RegisterNotificationsServer(grpcServer, server)
		browserrpc.RegisterResourceOpenerServer(grpcServer, server)

		go grpcServer.Serve(lis)

		conn, err := grpc.Dial(lis.Addr().String(), grpc.WithInsecure())
		require.NoError(t, err)

		bc := browserrpc.NewClient(context.Background(), conn)
		var closeOnce sync.Once
		close := func() {
			closeOnce.Do(func() {
				serverMutex.Lock()
				defer serverMutex.Unlock()
				bc.Close()
				server.Stop()
				grpcServer.Stop()
				ex.Close()
			})
		}
		h := &safeHandler{Component: ex, Handler: ex, mu: &serverMutex,
			quiesce: ex.waitInflight, close: close}
		*destructor = close
		return h, browsertest.BrowserFromAPIBrowser(bc), nil
	}
}

func TestIntegrationRPCBrowserDraw(t *testing.T) {
	var destructor func()
	constructor := newTestRPCBrowser(t, &destructor, clipboard.NewInMemory())
	testBrowserHandlerDraw(t, constructor)
	destructor()
}

func TestIntegrationCopyToClipboard(t *testing.T) {
	var destructor func()
	clip := clipboard.NewInMemory()
	constructor := newTestRPCBrowser(t, &destructor, clip,
		text.WithCommandKeyBinding(
			term.KeyComb{Ch: 'h', Mod: term.ModCtrl}, [][]string{{cmdClipboardPaste}}),
		text.WithFloatingNoMaxSize(false),
	)
	testCopyToClipboard(t, clip, constructor)
	destructor()
}

func TestRPCBrowserCloseLeak(t *testing.T) {
	var destructor func()
	_, b, err := newTestRPCBrowser(t,
		&destructor, clipboard.NewInMemory())(
		texttest.NopEditor())
	require.NoError(t, err)
	defer destructor()

	focus, err := b.Focus()
	require.NoError(t, err)

	win, err := b.Split(browserapi.OrientationLeft, focus, browsertest.NewTestHandler())
	require.NoError(t, err)

	require.NoError(t, win.Close())
}
