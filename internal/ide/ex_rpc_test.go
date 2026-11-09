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
	"image"
	"net"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"unstable.build/rune/internal/browser"
	tbrowserrpc "unstable.build/rune/internal/browser/browserrpc"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/handler/handlerrpc"
	thandlerrpc "unstable.build/rune/internal/handler/handlerrpc"
	"unstable.build/rune/internal/handler/handlerrpc/handlerrpctest"
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
			nopPublishEvent, 0, clip, nil, nil, ex.Browser(), nil, testPromptEditor(), testNameGenerator, opts...)
		if err != nil {
			return nil, nil, err
		}
		ex.subscribeCommands()
		lis, err := net.Listen("tcp", ":0")
		require.NoError(t, err)

		var serverMutex sync.Mutex
		grpcServer := grpc.NewServer()
		server := tbrowserrpc.NewServer(ex.Browser(), &serverMutex, handlerrpc.GraphicsConfig{})
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

func TestRPCShow(t *testing.T) {
	b := newExForAliasRun(t, nil)
	defer b.Close()
	var system []string
	b.ex.metaOpenURL = metaOpenURLConfig{system: func(u *url.URL) error {
		system = append(system, u.String())
		return nil
	}}
	var interrupts atomic.Int32
	res := extension.BrowserResources(b.Browser(), func(ev term.Event) bool {
		if ev.Type == term.EventInterrupt {
			interrupts.Add(1)
		}
		return true
	}, thandlerrpc.GraphicsConfig{})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	defer grpcServer.Stop()
	closer, err := res[extensionapi.PermissionBrowserResourceOpener].
		Register(grpcServer, new(sync.Mutex))
	require.NoError(t, err)
	defer closer.Close()
	go debug.CapturePanicReport(func() { _ = grpcServer.Serve(lis) })
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	client := browserrpc.NewClient(context.Background(), conn)
	defer client.Close()

	file, err := workspaceapi.ParseURI("file:///shown.go")
	require.NoError(t, err)
	require.NoError(t, client.Show(file))
	tab, ok := b.comp.FocusTab()
	require.True(t, ok, "the shown file must be in focus")
	assert.Equal(t, file.String(), tab.URI().String())

	web, err := workspaceapi.ParseURI("https://example.com/docs")
	require.NoError(t, err)
	require.NoError(t, client.Show(web))
	assert.Equal(t, []string{"https://example.com/docs"}, system)
	assert.EqualValues(t, 2, interrupts.Load(), "every Show must redraw")

	b.ex.metaOpenURL.system = nil
	require.ErrorContains(t, client.Show(web), "no system browser")
}

func TestRPCSplitDrawsExtensionImages(t *testing.T) {
	tests := []struct {
		name   string
		stream bool
	}{
		{name: "through the arena"},
		{name: "over the stream", stream: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testRPCSplitDrawsExtensionImages(t, tt.stream)
		})
	}
}

func testRPCSplitDrawsExtensionImages(t *testing.T, stream bool) {
	frames := make(chan []byte, 64)
	b := newExForTestingWithWorkspace(t, &testLoader{}, texttest.NopEditor(),
		vte.DefaultConfig(), nopPublishEvent, clipboard.NewInMemory(),
		text.WithEventPublisher(func(ev term.Event) bool {
			if ev.Type == term.EventInterrupt &&
				strings.HasPrefix(string(ev.Raw), "AsyncClient") {
				frames <- ev.Raw
			}
			return true
		}))
	defer b.Close()
	arenaRoot := t.TempDir()
	res := extension.BrowserResources(b.Browser(), nopPublishEvent, thandlerrpc.GraphicsConfig{
		ArenaRoot:    arenaRoot,
		StreamPixels: stream,
	})

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	grpcServer := grpc.NewServer()
	defer grpcServer.Stop()
	ui := new(sync.Mutex)
	closer, err := res[extensionapi.PermissionBrowserWindowManager].Register(grpcServer, ui)
	require.NoError(t, err)
	defer closer.Close()
	go debug.CapturePanicReport(func() { _ = grpcServer.Serve(lis) })
	conn, err := grpc.NewClient(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	client := browserrpc.NewClient(context.Background(), conn)
	defer client.Close()

	const width, height = 120, 40
	ui.Lock()
	b.Resize(width, height)
	ui.Unlock()
	focus, err := client.Focus()
	require.NoError(t, err)
	ext := handlerrpctest.NewRasterHandler()
	_, err = client.Split(browserapi.OrientationRight, focus, ext)
	require.NoError(t, err)

	cellSize := cell.PixelSize{Width: 8.5, Height: 17.25}
	draw := func(ctx context.Context) *cell.BufferWriter {
		ui.Lock()
		defer ui.Unlock()
		w := cell.NewBufferWriter(cell.ContextWithPixelSize(ctx, cellSize), width, height)
		b.Draw(w)
		return w
	}
	frame := func() *cell.BufferWriter {
		t.Helper()
		draw(context.Background())
		select {
		case payload := <-frames:
			return draw(term.ContextWithPayload(context.Background(), payload))
		case <-time.After(5 * time.Second):
			t.Fatal("the extension's frame did not arrive")
			return nil
		}
	}
	uploads := func() []string {
		t.Helper()
		arenas, err := os.ReadDir(arenaRoot)
		require.NoError(t, err)
		if stream {
			require.Empty(t, arenas, "streamed pixels must not touch the disk")
			return nil
		}
		require.Len(t, arenas, 1)
		entries, err := os.ReadDir(filepath.Join(arenaRoot, arenas[0].Name()))
		require.NoError(t, err)
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}
	placed := func(w *cell.BufferWriter) *image.RGBA {
		t.Helper()
		images := w.Images()
		require.Len(t, images, 1)
		assert.Equal(t, images[0].Bounds(), images[0].Visible(),
			"the extension's cells must be drawn before its image")
		return images[0].Src.(*image.RGBA)
	}

	first := placed(frame())
	assert.Equal(t, ext.Raster().Pix, first.Pix)
	if stream {
		// Streamed responses carry at most 1 MiB of pixels.
		require.Greater(t, len(first.Pix), 1<<20,
			"the raster must take several responses to stream")
	}
	files := func(names ...string) []string {
		if stream {
			return nil
		}
		return names
	}
	assert.Equal(t, files("1-0"), uploads())

	second := placed(frame())
	assert.Same(t, first, second, "an unchanged image must be placed without an upload")
	assert.Equal(t, ext.Raster().Pix, second.Pix)
	assert.Equal(t, files("1-0"), uploads())

	ext.SetVersion(2)
	bumped := placed(frame())
	assert.Equal(t, ext.Raster().Pix, bumped.Pix)
	assert.NotEqual(t, first.Pix, bumped.Pix)
	assert.Equal(t, files("1-0", "1-1"), uploads())
	assert.Equal(t, []bool{true, true, true}, ext.Shown())
	want := handlerrpctest.CellPixelSize{Width: cellSize.Width, Height: cellSize.Height}
	assert.Equal(t, []handlerrpctest.CellPixelSize{want, want, want}, ext.CellPixelSizes(),
		"the extension must draw with the cell size of the IDE's frames")
}
