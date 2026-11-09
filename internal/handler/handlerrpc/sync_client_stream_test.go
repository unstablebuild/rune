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

package handlerrpc

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkhandler "github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/handlerrpc/handlerrpctest"
	"unstable.build/rune/internal/handler/handlertest"
)

// pipeEnd is one end of an in-memory stream: a grpc.ServerStream for the
// host and a grpc.ClientStream for the extension.
type pipeEnd struct {
	ctx  context.Context
	send chan<- proto.Message
	recv <-chan proto.Message
}

func (p pipeEnd) SetHeader(metadata.MD) error  { return nil }
func (p pipeEnd) SendHeader(metadata.MD) error { return nil }
func (p pipeEnd) SetTrailer(metadata.MD)       {}
func (p pipeEnd) Header() (metadata.MD, error) { return nil, nil }
func (p pipeEnd) Trailer() metadata.MD         { return nil }
func (p pipeEnd) CloseSend() error             { return nil }
func (p pipeEnd) Context() context.Context     { return p.ctx }

// SendMsg copies m, as gRPC encodes it, since senders may reuse what it
// refers to once SendMsg returns.
func (p pipeEnd) SendMsg(m any) error {
	select {
	case p.send <- proto.Clone(m.(proto.Message)):
		return nil
	case <-p.ctx.Done():
		return io.EOF
	}
}

func (p pipeEnd) RecvMsg(m any) error {
	select {
	case msg := <-p.recv:
		proto.Reset(m.(proto.Message))
		proto.Merge(m.(proto.Message), msg)
		return nil
	case <-p.ctx.Done():
		return io.EOF
	}
}

// newSyncExtension serves h, as an extension does, to the
// SyncClientStream it returns.
func newSyncExtension(
	t *testing.T, h handlerrpc.Handler, opts ...Option,
) *SyncClientStream[*handlerrpctest.TestMessage] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	toExt, toHost := make(chan proto.Message, 16), make(chan proto.Message, 16)
	client := NewSyncClientStream(ctx, pipeEnd{ctx: ctx, send: toExt, recv: toHost},
		func() *handlerrpctest.TestMessage { return new(handlerrpctest.TestMessage) }, opts...)
	server := handlerrpc.NewServerStream(pipeEnd{ctx: ctx, send: toHost, recv: toExt}, h,
		func() *handlerrpctest.TestMessage { return new(handlerrpctest.TestMessage) })
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		server.ReceiveMessages()
	})
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return client
}

func TestSyncClientStreamDrawsImages(t *testing.T) {
	tests := []struct {
		name     string
		graphics func(t *testing.T) GraphicsConfig
	}{
		{name: "streamed", graphics: func(*testing.T) GraphicsConfig { return streamGraphics(0) }},
		{name: "arena", graphics: arenaGraphics},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := handlerrpctest.NewRasterHandler()
			client := newSyncExtension(t, h, WithGraphics(tt.graphics(t)))
			client.Resize(4, 2)
			// Every version cycles the extension through its upload files,
			// which it may only rewrite once the host acknowledges reading
			// them.
			for version := uint64(1); version <= 3; version++ {
				h.SetVersion(version)
				w := frameAt(context.Background(), 9, 18)
				client.Draw(w)
				require.Len(t, w.Images, 1, "version %d", version)
				assert.Equal(t, handlerrpctest.Gradient(36, 36, version).Pix, w.Images[0].Pix, "version %d", version)
				assert.Equal(t, 4, w.Images[0].Width)
				assert.Equal(t, 2, w.Images[0].Height)
				assert.Zero(t, w.CellsAfterImage, "the cells must be written before the images")
			}
			assert.Equal(t, []bool{true, true, true}, h.Shown())
			assert.NoError(t, client.Close())
		})
	}
}

func TestSyncClientStreamFollowsTheCellSize(t *testing.T) {
	h := handlerrpctest.NewRasterHandler()
	client := newSyncExtension(t, h, WithGraphics(streamGraphics(0)))
	client.Resize(4, 2)

	client.Draw(frameAt(context.Background(), 9, 18))
	larger := frameAt(context.Background(), 12, 24)
	client.Draw(larger)
	require.Len(t, larger.Images, 1)
	assert.Equal(t, handlerrpctest.Gradient(48, 48, 1).Pix, larger.Images[0].Pix)

	untold := &handlerrpctest.ImageRecorder{}
	client.Draw(untold)
	assert.Empty(t, untold.Images, "a frame that does not tell the cell size draws no images")
	assert.Equal(t, []bool{true, true, false}, h.Shown(), "but the extension's fallback")
	assert.Equal(t, []handlerrpctest.CellPixelSize{{Width: 9, Height: 18}, {Width: 12, Height: 24}, {}},
		h.CellPixelSizes())
}

func TestSyncClientStreamWithoutGraphics(t *testing.T) {
	h := handlerrpctest.NewRasterHandler()
	client := newSyncExtension(t, h)
	client.Resize(4, 2)
	w := frameAt(context.Background(), 9, 18)
	client.Draw(w)
	assert.Empty(t, w.Images)
	assert.Equal(t, 8, w.Cells)
	assert.Equal(t, []handlerrpctest.CellPixelSize{{}}, h.CellPixelSizes(), "the extension is not told the cell size")
	assert.Equal(t, []bool{false}, h.Shown())
}

// mouseRecorder is an extension's handler that records the mouse events
// it is sent.
type mouseRecorder struct {
	*sdkhandler.TestHandler
	mu     sync.Mutex
	events []term.Event
}

func newMouseRecorder() *mouseRecorder {
	r := &mouseRecorder{TestHandler: sdkhandler.NewTestHandler()}
	r.HandleOverride = func(ev term.Event) (bool, bool) {
		if ev.Type == term.EventMouse {
			r.mu.Lock()
			r.events = append(r.events, ev)
			r.mu.Unlock()
		}
		return false, true
	}
	return r
}

func (r *mouseRecorder) Close() error { return nil }

func (r *mouseRecorder) last() (term.Event, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.events) == 0 {
		return term.Event{}, false
	}
	return r.events[len(r.events)-1], true
}

func TestSyncClientStreamPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		h := newMouseRecorder()
		client := newSyncExtension(t, h)
		handlertest.RunPointerLeaves(t, client, size[0], size[1])

		_, handled := client.Handle(term.Event{Type: term.EventMouse, MouseX: -7, MouseY: size[1] + 3})
		assert.True(t, handled)
		got, ok := h.last()
		require.True(t, ok)
		assert.Equal(t, term.Key(0), got.Key)
		assert.Equal(t, -7, got.MouseX, "the leave reaches the extension outside it")
		assert.Equal(t, size[1]+3, got.MouseY)
		assert.NoError(t, client.Close())
	}
}
