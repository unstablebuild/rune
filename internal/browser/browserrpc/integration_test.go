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

package browserrpc

import (
	context "context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi/browserrpc"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"go.uber.org/goleak"
	gomock "go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/debug"
	thandlerrpc "unstable.build/rune/internal/handler/handlerrpc"
	"unstable.build/rune/internal/handler/handlerrpc/handlerrpctest"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/workspace/processctx"
)

// integrationExtensionID tags every call as extensionv2's interceptor
// does for an authenticated extension.
const integrationExtensionID = "test-extension"

func newClientServerIntegration(
	t *testing.T, h browser.Browser,
) (*browserrpc.Client, func()) {
	server, client, cleanup := newClientServerIntegrationWith(t, h, thandlerrpc.GraphicsConfig{})
	server.SetSyncMode()
	return client, cleanup
}

func newClientServerIntegrationWith(
	t *testing.T, h browser.Browser, graphics thandlerrpc.GraphicsConfig,
) (*Server, *browserrpc.Client, func()) {
	lis, err := net.Listen("tcp", ":0")
	require.NoError(t, err)
	mutex := new(sync.Mutex)

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(func(
		ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler,
	) (any, error) {
		return handler(processctx.ContextWithExtensionID(ctx, integrationExtensionID), req)
	}))
	rpcServer := NewServer(h, mutex, graphics)
	browserrpc.RegisterWindowManagerServer(grpcServer, rpcServer)
	browserrpc.RegisterResourceOpenerServer(grpcServer, rpcServer)
	browserrpc.RegisterNotificationsServer(grpcServer, rpcServer)
	browserrpc.RegisterEventPublisherServer(grpcServer, rpcServer)

	go grpcServer.Serve(lis)

	conn, err := grpc.Dial(lis.Addr().String(), grpc.WithInsecure())
	require.NoError(t, err)

	client := browserrpc.NewClient(context.Background(), conn)

	closeFn := func() {
		client.Close()
		rpcServer.browser.Lock()
		rpcServer.Stop()
		rpcServer.browser.Unlock()
		grpcServer.Stop()
		conn.Close()
	}

	return rpcServer, client, closeFn
}

func TestClientServerIntegrationSplit(t *testing.T) {
	tsuite := []browserapi.Orientation{
		browserapi.OrientationDefault,
		browserapi.OrientationTop,
		browserapi.OrientationBottom,
		browserapi.OrientationLeft,
		browserapi.OrientationRight,
	}

	for _, _tcase := range tsuite {
		tcase := _tcase
		t.Run(fmt.Sprintf("%+v", tcase), func(t *testing.T) {
			defer goleak.VerifyNone(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := browsertest.NewMockBrowser(ctrl)
			client, cleanup := newClientServerIntegration(t, mock)
			defer cleanup()

			win0 := browsertest.NopWindow()
			win1 := browsertest.NopWindow()

			mock.EXPECT().Window(gomock.Any()).Return(win1, true).AnyTimes()
			mock.EXPECT().Split(gomock.Any(), gomock.Any(), gomock.Any()).
				DoAndReturn(func(
					o browserapi.Orientation, win browserapi.Window, h browserapi.Handler,
				) (browser.Window, error) {
					assert.Equal(t, tcase, o)
					return win1, nil
				})
			resWin1, err := client.Split(tcase, win0, browsertest.NewTestHandler())
			require.NoError(t, err)

			mock.EXPECT().Window(gomock.Any()).Return(win1, true).AnyTimes()
			require.NoError(t, client.SetWindowContent(resWin1, browsertest.NewTestHandler()))
			require.NoError(t, client.CloseWindow(resWin1))
		})
	}
}

func TestClientServerIntegrationBar(t *testing.T) {
	tsuite := []browserapi.BarConfig{
		{Orientation: browserapi.OrientationDefault, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationTop, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationBottom, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationLeft, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationRight, Frame: browserapi.BarFrameAlways, Size: 1},
		{Orientation: browserapi.OrientationDefault, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationTop, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationBottom, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationLeft, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationRight, Frame: browserapi.BarFrameDefault, Size: 1},
		{Orientation: browserapi.OrientationDefault, Frame: browserapi.BarFrameNever, Size: 1},
		{Orientation: browserapi.OrientationTop, Frame: browserapi.BarFrameNever, Size: 1},
		{Orientation: browserapi.OrientationBottom, Frame: browserapi.BarFrameNever, Size: 1},
		{Orientation: browserapi.OrientationLeft, Frame: browserapi.BarFrameNever, Size: 1},
		{Orientation: browserapi.OrientationRight, Frame: browserapi.BarFrameNever, Size: 1},
	}

	for _, _tcase := range tsuite {
		tcase := _tcase
		t.Run(fmt.Sprintf("%+v", tcase), func(t *testing.T) {
			defer goleak.VerifyNone(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := browsertest.NewMockBrowser(ctrl)
			client, cleanup := newClientServerIntegration(t, mock)
			defer cleanup()

			win1 := browsertest.NopWindow()

			mock.EXPECT().Window(gomock.Any()).Return(win1, true).AnyTimes()
			mock.EXPECT().Bar(gomock.Any(), gomock.Any()).
				DoAndReturn(func(
					config browserapi.BarConfig, h tui.Handler,
				) error {
					assert.Equal(t, tcase.Orientation, config.Orientation)
					assert.Equal(t, tcase.Frame, config.Frame)
					assert.Equal(t, tcase.Size, config.Size)
					return nil
				})
			err := client.Bar(tcase, browsertest.NewTestHandler())
			require.NoError(t, err)
		})
	}
}

func TestClientServerIntegrationTab(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := browsertest.NewMockBrowser(ctrl)
	client, cleanup := newClientServerIntegration(t, mock)
	defer cleanup()

	expectedURI, err := workspaceapi.ParseURI("ABV://SanCarlos@2017/BlueLime")
	require.NoError(t, err)
	expectedIcon := 'X'
	expectedName := "Linduro"

	mock.EXPECT().Resource(gomock.Any()).Return(browsertest.NewTestHandler(), true)
	mock.EXPECT().Tab(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(
			uri workspaceapi.URI, icon rune, name string, h browserapi.Handler,
		) (browserapi.Handler, error) {
			assert.Equal(t, expectedURI, uri)
			assert.Equal(t, expectedIcon, icon)
			assert.Equal(t, expectedName, name)
			return browsertest.NewTestHandler(), nil
		})
	tab, err := client.Tab(expectedURI, expectedIcon,
		expectedName, browsertest.NewTestHandler())
	require.NoError(t, err)

	win1 := browsertest.NopWindow()
	mock.EXPECT().Focus().Return(win1, nil)

	win, err := client.Focus()
	require.NoError(t, err)

	mock.EXPECT().Window(gomock.Any()).Return(win1, true).AnyTimes()
	require.NoError(t, client.SetWindowContent(win, tab))
}

func TestClientServerIntegrationPrompt(t *testing.T) {
	cfg := browserapi.PromptConfig{
		Message:  "Proceed?",
		Options:  []string{"Yes", "No"},
		Bindings: []term.KeyComb{{Ch: 'y'}, {Key: term.KeyEsc, Mod: term.ModCtrl}},
		MaxWidth: 40,
	}
	for _, tcase := range []struct {
		name string
		// open reports false for a prompt already open for the message.
		open bool
		// user does what the user or the extension does with the prompt.
		user   func(h handler.PromptHandler, client *browserrpc.Client, win browserapi.Window)
		want   []string
		closed bool
	}{
		{
			name: "selected",
			open: true,
			user: func(h handler.PromptHandler, _ *browserrpc.Client, _ browserapi.Window) {
				h.OnSelect(1, "No")
				require.NoError(t, h.OnClose())
			},
			want: []string{"select 1 No", "close"},
		},
		{
			name: "dismissed",
			open: true,
			user: func(h handler.PromptHandler, _ *browserrpc.Client, _ browserapi.Window) {
				require.NoError(t, h.OnClose())
			},
			want: []string{"close"},
		},
		{
			name: "closed by the extension",
			open: true,
			user: func(_ handler.PromptHandler, client *browserrpc.Client, win browserapi.Window) {
				require.NoError(t, client.CloseWindow(win))
			},
			want:   []string{"close"},
			closed: true,
		},
		{
			name: "connection closed",
			open: true,
			user: func(_ handler.PromptHandler, client *browserrpc.Client, _ browserapi.Window) {
				require.NoError(t, client.Close())
			},
			want:   []string{"close"},
			closed: true,
		},
		{
			name: "already open",
			open: false,
		},
	} {
		t.Run(tcase.name, func(t *testing.T) {
			defer goleak.VerifyNone(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mock := browsertest.NewMockBrowser(ctrl)
			client, cleanup := newClientServerIntegration(t, mock)
			defer cleanup()

			win := &promptWindow{Window: browsertest.NopWindow()}
			mock.EXPECT().Window(gomock.Any()).Return(win, true).AnyTimes()
			mock.EXPECT().OpenPrompt(cfg, gomock.Any()).DoAndReturn(func(
				_ browserapi.PromptConfig, h handler.PromptHandler,
			) (browser.Window, bool, error) {
				win.h = h
				return win, tcase.open, nil
			})

			events := make(chan string, 2)
			got, err := client.Prompt(cfg, handler.FuncPromptHandler(
				func(i int, option string) { events <- fmt.Sprintf("select %d %s", i, option) },
				func() error {
					events <- "close"
					return nil
				}))
			require.NoError(t, err)
			assert.Equal(t, win.WindowID(), got.WindowID())

			if tcase.user != nil {
				tcase.user(win.h, client, got)
			}
			for _, want := range tcase.want {
				select {
				case ev := <-events:
					assert.Equal(t, want, ev)
				case <-time.After(5 * time.Second):
					require.FailNow(t, "prompt event not delivered", want)
				}
			}
			assert.Never(t, func() bool { return len(events) > 0 },
				asyncResultsSleepDuration, 10*time.Millisecond, "unexpected prompt event")
			assert.Equal(t, tcase.closed, win.closed.Load())
		})
	}
}

// promptWindow is a prompt's window, which tells its handler it closed.
type promptWindow struct {
	browser.Window
	h      handler.PromptHandler
	closed atomic.Bool
}

func (w *promptWindow) Close() error {
	w.closed.Store(true)
	return w.h.OnClose()
}

func (w *promptWindow) Closed() bool { return w.closed.Load() }

func TestClientServerIntegrationSetTabStatus(t *testing.T) {
	defer goleak.VerifyNone(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mock := browsertest.NewMockBrowser(ctrl)
	client, cleanup := newClientServerIntegration(t, mock)
	defer cleanup()

	uri, err := workspaceapi.ParseURI("rune-agent://model/rolling-fox")
	require.NoError(t, err)

	blocked := browserapi.TabStatus{
		ID: "tool/bash", State: browserapi.TabBlocked, Kind: browserapi.TabKindPermission,
		Progress: 40, Title: "make test", Message: "Approve apply?",
	}
	wantBlocked := programstatus.Report{
		ID: "tool/bash", State: programstatus.Blocked, Kind: programstatus.KindPermission,
		Progress: 40, App: integrationExtensionID, Title: "make test", Msg: "Approve apply?",
	}
	clear := browserapi.TabStatus{State: browserapi.TabClear, Progress: -1}
	gomock.InOrder(
		mock.EXPECT().SetTabStatus(uri, wantBlocked).Return(nil),
		mock.EXPECT().SetTabStatus(uri, programstatus.Report{
			State: programstatus.Clear, Progress: -1,
		}).Return(errors.New("unknown tab")),
	)
	require.NoError(t, client.SetTabStatus(uri, blocked))
	err = client.SetTabStatus(uri, clear)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tab")

	err = client.SetTabStatus(uri, browserapi.TabStatus{State: 99})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown tab state")

	err = client.SetTabStatus(uri, browserapi.TabStatus{State: browserapi.TabWorking, ID: "a//b"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid id")
}

func TestClientServerIntegrationImages(t *testing.T) {
	tabURI, err := workspaceapi.ParseURI("rune-agent://browser/1")
	require.NoError(t, err)
	tests := []struct {
		name    string
		install func(
			mock *browsertest.MockBrowser, ctrl *gomock.Controller,
			client *browserrpc.Client, ext browserapi.Handler,
			installed chan<- tui.Handler,
		) error
	}{
		{
			name: "split",
			install: func(
				mock *browsertest.MockBrowser, _ *gomock.Controller,
				client *browserrpc.Client, ext browserapi.Handler, installed chan<- tui.Handler,
			) error {
				mock.EXPECT().Window(gomock.Any()).Return(browsertest.NopWindow(), true)
				mock.EXPECT().Split(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ browserapi.Orientation, _ browser.Window, h browserapi.Handler) (browser.Window, error) {
						installed <- h
						return browsertest.NopWindow(), nil
					})
				_, err := client.Split(browserapi.OrientationRight, browsertest.NopWindow(), ext)
				return err
			},
		},
		{
			name: "floating",
			install: func(
				mock *browsertest.MockBrowser, _ *gomock.Controller,
				client *browserrpc.Client, ext browserapi.Handler, installed chan<- tui.Handler,
			) error {
				mock.EXPECT().Floating(gomock.Any(), gomock.Any()).DoAndReturn(
					func(h browser.Floating, _ browserapi.FloatingConfig) (browser.Window, error) {
						installed <- h
						return browsertest.NopWindow(), nil
					})
				_, err := client.Floating(floatingRaster{ext.(*handlerrpctest.RasterHandler)},
					browserapi.FloatingConfig{})
				return err
			},
		},
		{
			name: "tab",
			install: func(
				mock *browsertest.MockBrowser, _ *gomock.Controller,
				client *browserrpc.Client, ext browserapi.Handler, installed chan<- tui.Handler,
			) error {
				mock.EXPECT().Tab(tabURI, gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
					func(_ workspaceapi.URI, _ rune, _ string, h browserapi.Handler) (browserapi.Handler, error) {
						installed <- h
						return h, nil
					})
				_, err := client.Tab(tabURI, 'B', "browser", ext)
				return err
			},
		},
		{
			name: "set content",
			install: func(
				mock *browsertest.MockBrowser, ctrl *gomock.Controller,
				client *browserrpc.Client, ext browserapi.Handler, installed chan<- tui.Handler,
			) error {
				win := browsertest.NewMockWindow(ctrl)
				win.EXPECT().Closed().Return(false)
				win.EXPECT().WindowID().Return(uint64(1)).AnyTimes()
				win.EXPECT().SetContent(gomock.Any()).DoAndReturn(func(h browserapi.Handler) error {
					installed <- h
					return nil
				})
				mock.EXPECT().Window(gomock.Any()).Return(win, true)
				return client.SetWindowContent(browsertest.NopWindow(), ext)
			},
		},
	}
	for _, tt := range tests {
		for _, syncMode := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s sync=%t", tt.name, syncMode), func(t *testing.T) {
				ctrl := gomock.NewController(t)
				mock := browsertest.NewMockBrowser(ctrl)
				published := make(chan term.Event, 64)
				mock.EXPECT().PublishEvent(gomock.Any()).DoAndReturn(func(ev term.Event) error {
					published <- ev
					return nil
				}).AnyTimes()
				server, client, cleanup := newClientServerIntegrationWith(t, mock,
					thandlerrpc.GraphicsConfig{ArenaRoot: t.TempDir()})
				if syncMode {
					server.SetSyncMode()
				}
				defer cleanup()

				ext := handlerrpctest.NewRasterHandler()
				installed := make(chan tui.Handler, 1)
				done := make(chan error, 1)
				go debug.CapturePanicReport(func() {
					done <- tt.install(mock, ctrl, client, ext, installed)
				})
				var host tui.Handler
				select {
				case host = <-installed:
				case <-time.After(5 * time.Second):
					t.Fatal("the handler was not installed")
				}
				// The UI calls host handlers under the browser's lock.
				locked := func(f func()) {
					server.browser.Lock()
					defer server.browser.Unlock()
					f()
				}
				// The install response is only sent with the next message to the handler.
				require.Eventually(t, func() bool {
					locked(func() { host.Resize(4, 2) })
					select {
					case err := <-done:
						require.NoError(t, err)
						return true
					default:
						return false
					}
				}, 5*time.Second, 10*time.Millisecond)

				sized := func(ctx context.Context) context.Context {
					return term.ContextWithCellPixelSize(ctx, 8.5, 17.25)
				}
				w := &handlerrpctest.ImageRecorder{Ctx: sized(context.Background())}
				locked(func() { host.Draw(w) })
				// An asynchronous handler draws the frame the extension sent
				// once it is told it arrived.
				for !syncMode && len(w.Images) == 0 {
					select {
					case ev := <-published:
						if ev.Type == term.EventInterrupt && ev.Raw != nil {
							w = &handlerrpctest.ImageRecorder{
								Ctx: sized(term.ContextWithPayload(context.Background(), ev.Raw)),
							}
							locked(func() { host.Draw(w) })
						}
					case <-time.After(5 * time.Second):
						t.Fatal("the extension's frame did not arrive")
					}
				}
				require.Len(t, w.Images, 1)
				assert.Equal(t, ext.Raster().Pix, w.Images[0].Pix)
				assert.Equal(t, []bool{true}, ext.Shown())
				assert.Equal(t, []handlerrpctest.CellPixelSize{{Width: 8.5, Height: 17.25}},
					ext.CellPixelSizes(), "the extension must draw with the frame's cell size")
			})
		}
	}
}

type floatingRaster struct {
	*handlerrpctest.RasterHandler
}

func (floatingRaster) Dimensions() (int, int) { return 4, 2 }
