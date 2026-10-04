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

package handlerrpctest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	sync "sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/mock/gomock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"unstable.build/rune/internal/browser/browsertest"
	thandlerrpc "unstable.build/rune/internal/handler/handlerrpc"
)

func TestClientServerStreamIntegration(t *testing.T) {
	t.Run("dimensions", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)

		expectedWidth, expectedHeight := 11, 19
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Dimensions().Return(expectedWidth, expectedHeight)
			mock.EXPECT().Cursor().Times(2)
			mock.EXPECT().Selection()
			mock.EXPECT().Draw(gomock.Any())
		}, nil)
		defer closeFn()

		actualWidth, actualHeight := client.Dimensions()

		assert.Equal(t, expectedHeight, actualHeight)
		assert.Equal(t, expectedWidth, actualWidth)

	})

	t.Run("selection returns selection", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)

		expectedSelection := "1234"
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Selection().Return(expectedSelection, true)
			mock.EXPECT().Dimensions()
			mock.EXPECT().Cursor().Times(2)
			mock.EXPECT().Draw(gomock.Any())
		}, nil)
		defer closeFn()

		actualSelection, actualOk := client.Selection()

		require.True(t, actualOk)
		assert.Equal(t, expectedSelection, actualSelection)

	})

	t.Run("selection does not return non-utf8 selection", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)

		expectedSelection := "\xF1\x01\x02"
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Selection().Return(expectedSelection, true)
			mock.EXPECT().Dimensions()
			mock.EXPECT().Cursor().Times(2)
			mock.EXPECT().Draw(gomock.Any())
		}, nil)
		defer closeFn()

		_, actualOk := client.Selection()
		require.False(t, actualOk)
	})

	t.Run("selection returns nothing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)

		var expectedSelection string
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Selection().Return(expectedSelection, false)
			mock.EXPECT().Dimensions()
			mock.EXPECT().Cursor().Times(2)
			mock.EXPECT().Draw(gomock.Any())
		}, nil)
		defer closeFn()

		actualSelection, actualOk := client.Selection()

		assert.False(t, actualOk)
		assert.Equal(t, expectedSelection, actualSelection)
	})

	t.Run("draw", func(t *testing.T) {
		var wg sync.WaitGroup
		wg.Add(1)
		client, closeFn := setupIntTest(t, browsertest.NewTestFloating(20, 10), func() {
		}, func(ev term.Event) error {
			wg.Done()
			return nil
		})
		defer closeFn()

		client.Resize(20, 10)
		w := term.NewStringWriter(21, 11)

		tests := []comptest.TestCase{
			{Expected: `
                     
                     
                     
                     
      LOADING        
                     
                     
                     
                     
                     
                     `,
			},
		}
		wg.Wait()
		wg.Add(1)
		comptest.TestComponent(t, client, w, tests)
		wg.Wait()

		tests = []comptest.TestCase{
			{Expected: `
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
AAAAAAAAAAAAAAAAAAAA 
                     `,
			},
		}
		wg.Add(1)
		comptest.TestComponent(t, client, w, tests)
		wg.Wait()
	})

	// The wire format is negotiated per draw request: a host that
	// advertises packed_ok gets columnar planes, an older host that
	// cannot set the field keeps getting per-cell rows. Both must render
	// the same screen.
	t.Run("draw frame format is negotiated per request", func(t *testing.T) {
		const width, height = 20, 10

		rows := drawFrame(t, width, height, false)
		assert.Nil(t, rows.GetPacked())
		require.Len(t, rows.GetRows(), height)

		packed := drawFrame(t, width, height, true)
		require.NotNil(t, packed.GetPacked())
		assert.Empty(t, packed.GetRows())
		assert.Equal(t, uint32(width), packed.GetPacked().GetWidth())
		assert.Equal(t, uint32(height), packed.GetPacked().GetHeight())

		rowsScreen := term.NewStringWriter(width, height)
		for y, row := range rows.GetRows() {
			for x, cell := range row.GetCells() {
				rowsScreen.SetCell(term.Coordinates{X: x, Y: y}, cell.ToModel())
			}
		}
		require.NoError(t, rowsScreen.Flush())

		packedScreen := term.NewStringWriter(width, height)
		packed.GetPacked().WriteTo(packedScreen)
		require.NoError(t, packedScreen.Flush())

		assert.Contains(t, packedScreen.String(), "AAAA")
		assert.Equal(t, rowsScreen.String(), packedScreen.String())
		assert.Equal(t, rowsScreen.Cells(), packedScreen.Cells())
	})

	t.Run("handle short circuits exit by sending close request", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)
		var wg sync.WaitGroup

		expectedHandled, expectedExit := true, true
		expectedEv := term.Event{Type: term.EventKey, Ch: 'a', Raw: []byte("a")}
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Handle(gomock.Any()).DoAndReturn(func(actualEv term.Event) (bool, bool) {
				assert.Equal(t, expectedEv, actualEv)
				return expectedExit, expectedHandled
			})
			mock.EXPECT().Selection()
			mock.EXPECT().Dimensions()
			mock.EXPECT().Cursor().Times(2)
			mock.EXPECT().Draw(gomock.Any())
			mock.EXPECT().Close().DoAndReturn(func() error {
				wg.Done()
				return nil
			})
		}, func(ev term.Event) error {
			if ev.Type == term.EventNone {
				wg.Done()
			}
			return nil
		})
		defer closeFn()

		// trigger handle
		wg.Add(2)
		_, _ = client.Handle(expectedEv)
		// wait for event none
		wg.Wait()
	})

	t.Run("handle re-dispatches prior event if handle response is handled=false", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)
		var wg sync.WaitGroup

		var actualRepublishedEvent term.Event
		expectedHandled, expectedExit := false, false
		ev := term.Event{Type: term.EventKey, Ch: 'a', Raw: []byte("a")}
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Handle(gomock.Any()).DoAndReturn(func(actualEv term.Event) (bool, bool) {
				assert.Equal(t, ev, actualEv)
				return expectedExit, expectedHandled
			})
			mock.EXPECT().Selection()
			mock.EXPECT().Dimensions()
			mock.EXPECT().Cursor().Times(2)
			mock.EXPECT().Draw(gomock.Any())
		}, func(_ev term.Event) error {
			if _ev.Type == term.EventKey {
				defer wg.Done()
				actualRepublishedEvent = _ev
			}
			return nil
		})
		defer closeFn()

		wg.Add(1)
		_, _ = client.Handle(ev)
		wg.Wait()
		// raw is overwritten for the re-publishing feature
		actualRepublishedEvent.Raw = nil
		ev.Raw = nil
		assert.Equal(t, ev, actualRepublishedEvent)
	})

	// Mouse events are routed by position and reach the stream in
	// pane-local coordinates, so a republished one lands in whatever
	// window sits at those coordinates of the root instead.
	t.Run("handle does not re-dispatch mouse event if handle response is handled=false", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var republished []term.Event

		mouseEv := term.Event{Type: term.EventMouse, MouseX: 5, MouseY: 3}
		keyEv := term.Event{Type: term.EventKey, Ch: 'a', Raw: []byte("a")}
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Handle(gomock.Any()).Return(false, false).Times(2)
			mock.EXPECT().Selection()
			mock.EXPECT().Dimensions()
			mock.EXPECT().Cursor().Times(2)
			mock.EXPECT().Draw(gomock.Any())
		}, func(ev term.Event) error {
			if ev.Type != term.EventMouse && ev.Type != term.EventKey {
				return nil
			}
			mu.Lock()
			republished = append(republished, ev)
			mu.Unlock()
			if ev.Type == term.EventKey {
				wg.Done()
			}
			return nil
		})
		defer closeFn()

		// The stream is ordered, so once the key comes back the mouse
		// response has been processed too.
		wg.Add(1)
		_, _ = client.Handle(mouseEv)
		_, _ = client.Handle(keyEv)
		wg.Wait()

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, republished, 1)
		assert.Equal(t, term.EventKey, republished[0].Type)
	})

	t.Run("cursor returns nothing", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)

		expectedCoordinates := term.Coordinates{}
		var expectedStyle term.CursorStyle
		expectedCursor := false
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Cursor().Return(expectedCoordinates, expectedStyle, expectedCursor).Times(2)
			mock.EXPECT().Selection()
			mock.EXPECT().Dimensions()
			mock.EXPECT().Draw(gomock.Any())
		}, nil)
		defer closeFn()

		actualCoordinates, actualStyle, actualCursor := client.Cursor()

		assert.Equal(t, expectedCoordinates, actualCoordinates)
		assert.Equal(t, expectedStyle, actualStyle)
		assert.Equal(t, expectedCursor, actualCursor)
	})

	t.Run("cursor returns cursor, style", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)

		expectedCoordinates := term.Coordinates{X: 99, Y: 11}
		expectedStyle := term.CursorStyleSteadyBlock
		expectedCursor := true
		client, closeFn := setupIntTest(t, mock, func() {
			mock.EXPECT().Cursor().Return(expectedCoordinates, expectedStyle, expectedCursor).Times(2)
			mock.EXPECT().Selection()
			mock.EXPECT().Dimensions()
			mock.EXPECT().Draw(gomock.Any())
		}, nil)
		defer closeFn()

		actualCoordinates, actualStyle, actualCursor := client.Cursor()

		assert.Equal(t, expectedCoordinates, actualCoordinates)
		assert.Equal(t, expectedStyle, actualStyle)
		assert.Equal(t, expectedCursor, actualCursor)
	})

	// The host asks for the cursor before it asks for the frame, so a
	// handler whose cursor moves while drawing would report a position
	// belonging to the previous frame. SDKs piggyback the cursor on the
	// draw response to keep the two atomic.
	t.Run("cursor from the draw response supersedes the pre-draw cursor", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		mock := browsertest.NewMockFloating(ctrl)

		stale := term.Coordinates{X: 1, Y: 1}
		fresh := term.Coordinates{X: 7, Y: 3}
		client, closeFn := setupIntTest(t, mock, func() {
			gomock.InOrder(
				mock.EXPECT().Cursor().Return(stale, term.CursorStyleSteadyBlock, true),
				mock.EXPECT().Cursor().Return(fresh, term.CursorStyleSteadyBar, true),
			)
			mock.EXPECT().Selection()
			mock.EXPECT().Dimensions()
			mock.EXPECT().Draw(gomock.Any())
		}, nil)
		defer closeFn()

		actualCoordinates, actualStyle, actualShow := client.Cursor()

		assert.True(t, actualShow)
		assert.Equal(t, fresh, actualCoordinates)
		assert.Equal(t, term.CursorStyleSteadyBar, actualStyle)
	})
}

type nopLocker struct{}

func (n nopLocker) Lock()   {}
func (n nopLocker) Unlock() {}

type testServer struct {
	UnimplementedTestServiceServer
	windowID  uint64
	client    *thandlerrpc.ClientStream[*TestMessage]
	publisher func(term.Event) error
}

func (t *testServer) TestStream(srv TestService_TestStreamServer) error {
	if t.client != nil {
		return errors.New("cannot re-use test server")
	}
	t.client = thandlerrpc.NewClientStream[*TestMessage](context.Background(), srv,
		func() *TestMessage {
			return new(TestMessage)

		}, t.publisher)
	msg, err := srv.Recv()
	if err != nil {
		return fmt.Errorf("receive initial request: %w", err)
	}
	req := msg.GetRequest()
	if msg.GetType() != handlerrpc.MessageType_Request || req == nil {
		return errors.New("receive initial request: missing request")
	}

	resp := handlerrpc.InstallResourceResponse{WindowId: uint64(t.windowID)}
	respMsg := handlerrpc.ServerMessage{Response: &resp}
	if err := srv.SendMsg(&respMsg); err != nil {
		return fmt.Errorf("send install response: %w", err)
	}

	return t.client.ReceiveMessages()
}

func setupIntTest(
	t *testing.T, mock handlerrpc.Handler,
	setup func(), publisher func(term.Event) error,
) (
	*thandlerrpc.ClientStream[*TestMessage], func(),
) {

	const windowID = 99
	var firstPublish atomic.Bool
	var wg sync.WaitGroup
	ts := &testServer{windowID: windowID, publisher: func(ev term.Event) error {
		if firstPublish.CompareAndSwap(false, true) {
			wg.Done()
		}
		if publisher != nil {
			publisher(ev)
		}
		return nil
	}}

	conn, closeFn := doSetupIntTest(t, func(grpcServer *grpc.Server) {
		RegisterTestServiceServer(grpcServer, ts)
	})

	serverStream, err := NewTestServiceClient(conn).TestStream(context.Background())
	require.NoError(t, err)

	server := handlerrpc.NewServerStream[*TestMessage](serverStream,
		mock, func() *TestMessage { return new(TestMessage) })

	req := TestRequest{}
	sendMsg := TestMessage{
		Type:    handlerrpc.MessageType_Request,
		Request: &req,
	}

	err = serverStream.SendMsg(&sendMsg)
	require.NoError(t, err)

	var recvMsg handlerrpc.ServerMessage
	err = serverStream.RecvMsg(&recvMsg)
	require.NoError(t, err)

	require.NotNil(t, recvMsg.GetResponse())

	go server.ReceiveMessages()

	// trigger the first call
	wg.Add(1)
	if setup != nil {
		setup()
	}
	ts.client.Draw(term.NoopWriter{})
	wg.Wait()

	return ts.client, func() {
		closeFn()
	}
}

func doSetupIntTest(t *testing.T, register func(*grpc.Server)) (
	conn *grpc.ClientConn, closeFn func(),
) {
	lis, err := net.Listen("tcp", ":0")
	require.NoError(t, err)

	grpcServer := grpc.NewServer()
	register(grpcServer)

	go grpcServer.Serve(lis)

	conn, err = grpc.Dial(lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)

	closeFn = func() {
		grpcServer.Stop()
		lis.Close()
	}
	return
}

func nopPublisher(term.Event) error { return nil }

// scriptedStream is a grpc.ClientStream that replays a fixed script of
// server messages and records what the ServerStream sends back, so a draw
// request can be issued with an arbitrary packed_ok.
type scriptedStream struct {
	script []*handlerrpc.ServerMessage
	sent   []*TestMessage
}

func (s *scriptedStream) Header() (metadata.MD, error) { return nil, nil }
func (s *scriptedStream) Trailer() metadata.MD         { return nil }
func (s *scriptedStream) CloseSend() error             { return nil }
func (s *scriptedStream) Context() context.Context     { return context.Background() }

func (s *scriptedStream) SendMsg(m any) error {
	s.sent = append(s.sent, m.(*TestMessage))
	return nil
}

func (s *scriptedStream) RecvMsg(m any) error {
	if len(s.script) == 0 {
		return io.EOF
	}
	next := s.script[0]
	s.script = s.script[1:]
	msg := m.(*handlerrpc.ServerMessage)
	proto.Reset(msg)
	proto.Merge(msg, next)
	return nil
}

// drawFrame runs a ServerStream against a handler that fills the screen and
// returns the frame it answers a draw request with the given packed_ok with.
func drawFrame(
	t *testing.T, width, height int, packedOK bool,
) *handlerrpc.DrawStreamResponse {
	t.Helper()

	stream := &scriptedStream{script: []*handlerrpc.ServerMessage{{
		Type: handlerrpc.MessageType_Resize,
		Resize: &handlerrpc.ResizeStreamRequest{
			Width:  int32(width),
			Height: int32(height),
		},
	}, {
		Type: handlerrpc.MessageType_Draw,
		Draw: &handlerrpc.DrawStreamRequest{PackedOk: packedOK},
	}}}

	server := handlerrpc.NewServerStream(stream,
		browsertest.NewTestFloating(width, height),
		func() *TestMessage { return new(TestMessage) })
	server.ReceiveMessages()

	require.Len(t, stream.sent, 1)
	draw := stream.sent[0].GetDraw()
	require.NotNil(t, draw)
	return draw
}
