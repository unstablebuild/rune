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
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/handlerrpc/handlerrpctest"
	"unstable.build/rune/internal/handler/handlertest"
)

const testTimeout = 5 * time.Second

type fakeExtension struct {
	t         *testing.T
	ctx       context.Context
	sent      chan *handlerrpc.ServerMessage
	recv      chan *handlerrpctest.TestMessage
	published chan term.Event
	closeOnce sync.Once
}

func (f *fakeExtension) SetHeader(metadata.MD) error  { return nil }
func (f *fakeExtension) SendHeader(metadata.MD) error { return nil }
func (f *fakeExtension) SetTrailer(metadata.MD)       {}
func (f *fakeExtension) Context() context.Context     { return f.ctx }

func (f *fakeExtension) SendMsg(m any) error {
	f.sent <- proto.Clone(m.(*handlerrpc.ServerMessage)).(*handlerrpc.ServerMessage)
	return nil
}

func (f *fakeExtension) RecvMsg(m any) error {
	select {
	case msg, ok := <-f.recv:
		if !ok {
			return io.EOF
		}
		proto.Merge(m.(*handlerrpctest.TestMessage), msg)
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

func newFakeExtension(
	t *testing.T, opts ...Option,
) (*fakeExtension, *ClientStream[*handlerrpctest.TestMessage]) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeExtension{
		t:         t,
		ctx:       ctx,
		sent:      make(chan *handlerrpc.ServerMessage, 64),
		recv:      make(chan *handlerrpctest.TestMessage),
		published: make(chan term.Event, 64),
	}
	client := NewClientStream(ctx, f, func() *handlerrpctest.TestMessage {
		return new(handlerrpctest.TestMessage)
	}, func(ev term.Event) error {
		f.published <- ev
		return nil
	}, opts...)
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		_ = client.ReceiveMessages()
	})
	t.Cleanup(func() {
		f.close()
		cancel()
		<-done
	})
	return f, client
}

func (f *fakeExtension) close() {
	f.closeOnce.Do(func() { close(f.recv) })
}

func (f *fakeExtension) next(typ handlerrpc.MessageType) *handlerrpc.ServerMessage {
	f.t.Helper()
	for {
		select {
		case msg := <-f.sent:
			if msg.GetType() == typ {
				return msg
			}
		case <-time.After(testTimeout):
			f.t.Fatalf("no %v message was sent", typ)
		}
	}
}

func (f *fakeExtension) assertNoDrawRequest() {
	f.t.Helper()
	for {
		select {
		case msg := <-f.sent:
			assert.NotEqual(f.t, handlerrpc.MessageType_Draw, msg.GetType(),
				"unexpected draw request")
		default:
			return
		}
	}
}

func (f *fakeExtension) respond(resp *handlerrpc.DrawStreamResponse) context.Context {
	f.t.Helper()
	f.recv <- &handlerrpctest.TestMessage{Type: handlerrpc.MessageType_Draw, Draw: resp}
	for {
		select {
		case ev := <-f.published:
			if ev.Type == term.EventInterrupt {
				return term.ContextWithPayload(context.Background(), ev.Raw)
			}
		case <-time.After(testTimeout):
			f.t.Fatal("the response did not trigger a redraw")
		}
	}
}

func (f *fakeExtension) stream(records ...*handlerrpc.ImageUpload) {
	for _, rec := range records {
		f.recv <- &handlerrpctest.TestMessage{
			Type: handlerrpc.MessageType_Draw,
			Draw: &handlerrpc.DrawStreamResponse{More: true, Uploads: []*handlerrpc.ImageUpload{rec}},
		}
	}
}

func (f *fakeExtension) sync() {
	f.recv <- &handlerrpctest.TestMessage{Type: handlerrpc.MessageType_Resize}
}

func (f *fakeExtension) start(
	client *ClientStream[*handlerrpctest.TestMessage], w term.Writer,
) (*handlerrpc.ResizeStreamRequest, *handlerrpc.DrawStreamRequest) {
	f.t.Helper()
	client.Resize(4, 2)
	resize := f.next(handlerrpc.MessageType_Resize).GetResize()
	assert.Nil(f.t, resize.GetGraphics(), "no frame has told the cell size yet")
	client.Draw(w)
	for {
		select {
		case msg := <-f.sent:
			switch msg.GetType() {
			case handlerrpc.MessageType_Resize:
				resize = msg.GetResize()
			case handlerrpc.MessageType_Draw:
				return resize, msg.GetDraw()
			}
		case <-time.After(testTimeout):
			f.t.Fatal("the first frame did not request a draw")
		}
	}
}

func upload(t *testing.T, dir, name string, pix []byte) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), pix, 0o600))
}

func cells(width, height int) *termrpc.PackedCells {
	n := width * height
	p := &termrpc.PackedCells{
		Width: uint32(width), Height: uint32(height),
		Chars: make([]uint32, n), Fg: make([]uint32, n), Bg: make([]uint32, n),
		Attrs: make([]uint32, n), Widths: make([]uint32, n), Bytes: make([]uint32, n),
	}
	for i := range n {
		p.Chars[i], p.Widths[i], p.Bytes[i] = 'x', 1, 1
	}
	return p
}

func frameAt(ctx context.Context, width, height float64) *handlerrpctest.ImageRecorder {
	return &handlerrpctest.ImageRecorder{Ctx: term.ContextWithCellPixelSize(ctx, width, height)}
}

func frame(ctx context.Context) *handlerrpctest.ImageRecorder {
	return frameAt(ctx, 8.5, 17.25)
}

func arenaGraphics(t *testing.T) GraphicsConfig {
	return GraphicsConfig{ArenaRoot: t.TempDir()}
}

func streamGraphics(maxBytes uint64) GraphicsConfig {
	return GraphicsConfig{StreamPixels: true, MaxBytes: maxBytes}
}

func TestClientStreamAdvertisesGraphics(t *testing.T) {
	tests := []struct {
		name         string
		opts         []Option
		unsized      bool
		wantGraphics bool
		wantArena    bool
	}{
		{name: "without graphics"},
		{
			name:    "for frames without a cell pixel size, as in the TUI",
			opts:    []Option{WithGraphics(GraphicsConfig{})},
			unsized: true,
		},
		{
			name:         "with an arena",
			opts:         []Option{WithGraphics(arenaGraphics(t))},
			wantGraphics: true,
			wantArena:    true,
		},
		{
			name:         "streaming pixels",
			opts:         []Option{WithGraphics(streamGraphics(0))},
			wantGraphics: true,
		},
		{
			name: "when the arena cannot be created",
			opts: []Option{WithGraphics(GraphicsConfig{
				ArenaRoot: filepath.Join(t.TempDir(), "missing"),
			})},
		},
		{
			name:         "by default, with an arena only where memory backs it",
			opts:         []Option{WithGraphics(GraphicsConfig{})},
			wantGraphics: true,
			wantArena:    runtime.GOOS == "linux",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ext, client := newFakeExtension(t, tt.opts...)
			w := frame(context.Background())
			if tt.unsized {
				w = new(handlerrpctest.ImageRecorder)
			}
			resize, _ := ext.start(client, w)
			assert.EqualValues(t, 4, resize.GetWidth())
			assert.EqualValues(t, 2, resize.GetHeight())
			g := resize.GetGraphics()
			if !tt.wantGraphics {
				assert.Nil(t, g)
				return
			}
			require.NotNil(t, g)
			assert.Equal(t, 8.5, g.GetCellWidth(), "the frame's cell size must be sent unrounded")
			assert.Equal(t, 17.25, g.GetCellHeight())
			assert.EqualValues(t, DefaultMaxImageBytes, g.GetMaxBytes())
			if !tt.wantArena {
				assert.Empty(t, g.GetArenaDir())
				return
			}
			info, err := os.Stat(g.GetArenaDir())
			require.NoError(t, err)
			assert.True(t, info.IsDir())
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm(),
				"the arena must be private to the user")

			ext.close()
			assert.Eventually(t, func() bool {
				_, err := os.Stat(g.GetArenaDir())
				return os.IsNotExist(err)
			}, testTimeout, 10*time.Millisecond,
				"the end of the stream must delete the arena")
		})
	}
}

func TestClientStreamDrawsImages(t *testing.T) {
	ext, client := newFakeExtension(t, WithGraphics(arenaGraphics(t)))
	resize, req := ext.start(client, frame(context.Background()))
	arena := resize.GetGraphics().GetArenaDir()
	require.NotEmpty(t, arena)
	assert.True(t, req.GetCellsUnchangedOk())
	assert.Zero(t, req.GetImageAck())

	v1 := handlerrpctest.Gradient(8, 4, 1)
	upload(t, arena, "1-0", v1.Pix)
	placement := &handlerrpc.ImagePlacement{Id: 5, Version: 1, Width: 4, Height: 2}
	self := ext.respond(&handlerrpc.DrawStreamResponse{
		Packed:  cells(4, 2),
		Uploads: []*handlerrpc.ImageUpload{{Id: 5, Version: 1, Seq: 1, File: "1-0", Width: 8, Height: 4}},
		Images:  []*handlerrpc.ImagePlacement{placement},
	})

	first := frame(self)
	client.Draw(first)
	assert.Equal(t, 8, first.Cells)
	assert.Zero(t, first.CellsAfterImage, "the cells must be written before the images")
	require.Len(t, first.Images, 1)
	got := first.Images[0]
	assert.Equal(t, uint64(1), got.Version)
	assert.Equal(t, 4, got.Width)
	assert.Equal(t, 2, got.Height)
	assert.Equal(t, v1.Pix, got.Pix)
	assert.Equal(t, v1.Rect, got.Src.Bounds())

	cached := frame(self)
	client.Draw(cached)
	require.Len(t, cached.Images, 1, "a redraw of the same frame must place the image again")
	assert.Equal(t, got.ID, cached.Images[0].ID)
	assert.Same(t, got.Src, cached.Images[0].Src)
	ext.assertNoDrawRequest()

	client.Draw(frame(context.Background()))
	req = ext.next(handlerrpc.MessageType_Draw).GetDraw()
	assert.EqualValues(t, 1, req.GetImageAck(), "the next request must ack the upload read")

	self = ext.respond(&handlerrpc.DrawStreamResponse{
		CellsUnchanged: true,
		Images:         []*handlerrpc.ImagePlacement{placement},
	})
	unchanged := frame(self)
	client.Draw(unchanged)
	assert.Equal(t, 8, unchanged.Cells, "unchanged cells must be drawn from the last frame")
	require.Len(t, unchanged.Images, 1)
	assert.Equal(t, got.ID, unchanged.Images[0].ID)
	assert.Equal(t, v1.Pix, unchanged.Images[0].Pix)

	client.Draw(frame(context.Background()))
	ext.next(handlerrpc.MessageType_Draw)
	self = ext.respond(&handlerrpc.DrawStreamResponse{
		Packed:   cells(4, 2),
		Released: []uint64{5},
	})
	released := frame(self)
	client.Draw(released)
	assert.Empty(t, released.Images)
	client.mu.Lock()
	assert.Empty(t, client.images.images, "a released image must free its pixels")
	client.mu.Unlock()
}

func TestClientStreamDrawsStreamedImages(t *testing.T) {
	ext, client := newFakeExtension(t, WithGraphics(streamGraphics(0)))
	ext.start(client, frame(context.Background()))
	record := func(version, seq uint64, pix []byte, from, to int) *handlerrpc.ImageUpload {
		return &handlerrpc.ImageUpload{
			Id: 5, Version: version, Seq: seq, Width: 8, Height: 4,
			Offset: uint64(from), Data: pix[from:to],
		}
	}

	v1 := handlerrpctest.Gradient(8, 4, 1)
	ext.stream(record(1, 1, v1.Pix, 0, 50), record(1, 1, v1.Pix, 50, len(v1.Pix)))
	self := ext.respond(&handlerrpc.DrawStreamResponse{
		Packed: cells(4, 2),
		Images: []*handlerrpc.ImagePlacement{{Id: 5, Version: 1, Width: 4, Height: 2}},
	})
	ext.sync()
	assert.Empty(t, ext.published, "streamed records must wait for the draw's response")

	first := frame(self)
	client.Draw(first)
	assert.Equal(t, 8, first.Cells)
	assert.Zero(t, first.CellsAfterImage, "the cells must be written before the images")
	require.Len(t, first.Images, 1)
	assert.Equal(t, v1.Pix, first.Images[0].Pix)
	assert.Equal(t, v1.Rect, first.Images[0].Src.Bounds())

	cached := frame(self)
	client.Draw(cached)
	require.Len(t, cached.Images, 1, "a redraw of the same frame must place the image again")
	assert.Same(t, first.Images[0].Src, cached.Images[0].Src)
	ext.assertNoDrawRequest()

	client.Draw(frame(context.Background()))
	ext.next(handlerrpc.MessageType_Draw)
	v2 := handlerrpctest.Gradient(8, 4, 2)
	ext.stream(record(2, 2, v2.Pix, 0, len(v2.Pix)))
	self = ext.respond(&handlerrpc.DrawStreamResponse{
		CellsUnchanged: true,
		Images:         []*handlerrpc.ImagePlacement{{Id: 5, Version: 2, Width: 4, Height: 2}},
	})
	bumped := frame(self)
	client.Draw(bumped)
	assert.Equal(t, 8, bumped.Cells)
	require.Len(t, bumped.Images, 1)
	assert.Equal(t, first.Images[0].ID, bumped.Images[0].ID)
	assert.Equal(t, v2.Pix, bumped.Images[0].Pix)
}

func TestClientStreamDropsStreamedPixelsPastTheBudget(t *testing.T) {
	pix := handlerrpctest.Gradient(2, 2, 1).Pix
	ext, client := newFakeExtension(t, WithGraphics(streamGraphics(uint64(len(pix)))))
	ext.start(client, frame(context.Background()))
	upload := func(version, seq uint64) *handlerrpc.ImageUpload {
		return &handlerrpc.ImageUpload{
			Id: 1, Version: version, Seq: seq, Width: 2, Height: 2, Data: pix,
		}
	}
	placed := func(version uint64) []*handlerrpc.ImagePlacement {
		return []*handlerrpc.ImagePlacement{{Id: 1, Version: version, Width: 4, Height: 2}}
	}

	// Two uploads in one frame exceed the budget, though only the last is kept.
	ext.stream(upload(1, 1), upload(2, 2))
	self := ext.respond(&handlerrpc.DrawStreamResponse{Packed: cells(4, 2), Images: placed(2)})
	w := frame(self)
	client.Draw(w)
	assert.Equal(t, 8, w.Cells, "the frame's cells must still be drawn")
	assert.Empty(t, w.Images, "pixels past the budget must not be held")

	client.Draw(frame(context.Background()))
	ext.next(handlerrpc.MessageType_Draw)
	ext.stream(upload(3, 3))
	self = ext.respond(&handlerrpc.DrawStreamResponse{Packed: cells(4, 2), Images: placed(3)})
	w = frame(self)
	client.Draw(w)
	require.Len(t, w.Images, 1, "the next frame within the budget must be shown")
	assert.Equal(t, pix, w.Images[0].Pix)
}

func TestClientStreamImageIDsAreUniqueAcrossStreams(t *testing.T) {
	pix := handlerrpctest.Gradient(2, 2, 1).Pix
	var ids []term.ImageID
	for range 2 {
		ext, client := newFakeExtension(t, WithGraphics(streamGraphics(0)))
		ext.start(client, frame(context.Background()))
		ext.stream(&handlerrpc.ImageUpload{Id: 1, Version: 1, Seq: 1, Width: 2, Height: 2, Data: pix})
		self := ext.respond(&handlerrpc.DrawStreamResponse{
			Packed: cells(4, 2),
			Images: []*handlerrpc.ImagePlacement{{Id: 1, Version: 1, Width: 4, Height: 2}},
		})
		w := frame(self)
		client.Draw(w)
		require.Len(t, w.Images, 1)
		ids = append(ids, w.Images[0].ID)
	}
	assert.NotEqual(t, ids[0], ids[1],
		"writers key retained pixels by id, which extensions number from scratch")
}

func TestClientStreamRequestsAgain(t *testing.T) {
	t.Run("when the extension has a newer frame", func(t *testing.T) {
		ext, client := newFakeExtension(t, WithGraphics(GraphicsConfig{}))
		ext.start(client, frame(context.Background()))

		self := ext.respond(&handlerrpc.DrawStreamResponse{Packed: cells(4, 2), Redraw: true})
		w := frame(self)
		client.Draw(w)
		assert.Equal(t, 8, w.Cells, "the frame received must still be drawn")
		ext.next(handlerrpc.MessageType_Draw)

		self = ext.respond(&handlerrpc.DrawStreamResponse{Packed: cells(4, 2)})
		client.Draw(frame(self))
		ext.assertNoDrawRequest()
	})

	t.Run("when the cell pixel size changes without the size in cells", func(t *testing.T) {
		ext, client := newFakeExtension(t, WithGraphics(GraphicsConfig{}))
		ext.start(client, frameAt(context.Background(), 8, 16))
		self := ext.respond(&handlerrpc.DrawStreamResponse{Packed: cells(4, 2)})
		client.Draw(frameAt(self, 8, 16))
		ext.assertNoDrawRequest()

		client.Draw(frameAt(self, 10.5, 21))
		resize := ext.next(handlerrpc.MessageType_Resize).GetResize()
		assert.EqualValues(t, 4, resize.GetWidth())
		assert.EqualValues(t, 2, resize.GetHeight())
		assert.Equal(t, 10.5, resize.GetGraphics().GetCellWidth())
		assert.Equal(t, 21.0, resize.GetGraphics().GetCellHeight())
		ext.next(handlerrpc.MessageType_Draw)
	})

	t.Run("when frames stop carrying the cell pixel size", func(t *testing.T) {
		ext, client := newFakeExtension(t, WithGraphics(GraphicsConfig{}))
		ext.start(client, frame(context.Background()))
		self := ext.respond(&handlerrpc.DrawStreamResponse{Packed: cells(4, 2)})

		client.Draw(&handlerrpctest.ImageRecorder{Ctx: self})
		resize := ext.next(handlerrpc.MessageType_Resize).GetResize()
		assert.Nil(t, resize.GetGraphics(), "the extension must stop placing images")
		ext.next(handlerrpc.MessageType_Draw)
	})

	t.Run("not for a new cell pixel size without graphics", func(t *testing.T) {
		ext, client := newFakeExtension(t)
		ext.start(client, frame(context.Background()))
		self := ext.respond(&handlerrpc.DrawStreamResponse{Packed: cells(4, 2)})

		client.Draw(frameAt(self, 10.5, 21))
		ext.assertNoDrawRequest()
	})
}

func TestClientStreamSurvivesABadUpload(t *testing.T) {
	ext, client := newFakeExtension(t, WithGraphics(arenaGraphics(t)))
	resize, _ := ext.start(client, frame(context.Background()))
	arena := resize.GetGraphics().GetArenaDir()

	good := handlerrpctest.Gradient(2, 2, 3)
	upload(t, arena, "2-0", good.Pix)
	self := ext.respond(&handlerrpc.DrawStreamResponse{
		Packed: cells(4, 2),
		Uploads: []*handlerrpc.ImageUpload{
			{Id: 1, Version: 1, Seq: 1, File: "../escape", Width: 2, Height: 2},
			{Id: 2, Version: 1, Seq: 2, File: "2-0", Width: 2, Height: 2},
		},
		Images: []*handlerrpc.ImagePlacement{
			{Id: 1, Version: 1, Width: 1, Height: 1},
			{Id: 2, Version: 1, Width: 1, Height: 1},
		},
	})
	w := frame(self)
	client.Draw(w)
	require.Len(t, w.Images, 1, "only the valid upload must be placed")
	assert.Equal(t, good.Pix, w.Images[0].Pix)

	client.Draw(frame(context.Background()))
	req := ext.next(handlerrpc.MessageType_Draw).GetDraw()
	assert.EqualValues(t, 2, req.GetImageAck(), "a dropped upload is acked like any other")
}

// newServedClientStream serves h, as an extension does, to the
// ClientStream it returns.
func newServedClientStream(
	t *testing.T, h handlerrpc.Handler,
) *ClientStream[*handlerrpctest.TestMessage] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	// Draw sends while holding the lock the receiver needs, so a pipe
	// that fills up would deadlock where gRPC's buffering does not.
	toExt, toHost := make(chan proto.Message, 1<<16), make(chan proto.Message, 1<<16)
	newT := func() *handlerrpctest.TestMessage { return new(handlerrpctest.TestMessage) }
	client := NewClientStream(ctx, pipeEnd{ctx: ctx, send: toExt, recv: toHost}, newT,
		func(term.Event) error { return nil })
	server := handlerrpc.NewServerStream(pipeEnd{ctx: ctx, send: toHost, recv: toExt}, h, newT)
	var wg sync.WaitGroup
	wg.Add(2)
	go debug.CapturePanicReport(func() {
		defer wg.Done()
		server.ReceiveMessages()
	})
	go debug.CapturePanicReport(func() {
		defer wg.Done()
		_ = client.ReceiveMessages()
	})
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	return client
}

func TestClientStreamPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		h := newMouseRecorder()
		client := newServedClientStream(t, h)
		handlertest.RunPointerLeaves(t, client, size[0], size[1])

		_, handled := client.Handle(term.Event{Type: term.EventMouse, MouseX: -7, MouseY: size[1] + 3})
		assert.True(t, handled)
		require.Eventually(t, func() bool {
			got, ok := h.last()
			return ok && got.Key == 0 && got.MouseX == -7 && got.MouseY == size[1]+3
		}, testTimeout, time.Millisecond, "the leave reaches the extension outside it")
	}
}
