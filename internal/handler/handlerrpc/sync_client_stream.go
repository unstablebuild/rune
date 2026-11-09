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
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
	"github.com/unstablebuild/rune-go-sdk/tui"
	grpc "google.golang.org/grpc"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
)

var _ tui.Handler = (*SyncClientStream[handlerrpc.StreamMessage])(nil)

// SyncClientStream implements a tui.Handler (+handler.Floating) server over
// a grpc.ServerStream, synchronously. This should only be used in tests.
type SyncClientStream[T handlerrpc.StreamMessage] struct {
	ctx       context.Context
	stream    grpc.ServerStream
	closeChan chan error
	closed    atomic.Bool
	height    atomic.Int32
	width     atomic.Int32
	newT      func() T

	respPending    atomic.Bool
	respPendingMsg *handlerrpc.ServerMessage

	// The fields below are only used from the host's calls.
	images            imageStore
	cellPixelSize     pixelSize
	sentCellPixelSize pixelSize
	resized           bool
	last              *handlerrpc.DrawStreamResponse
}

// NewSyncClientStream allocates storage for a new SyncClientStream and initializes it
// with the given grpc.ServerStream and type parameter constructor. The given locker
// is used to unlock before I/O is performed; if no synchronization is needed
// then a nop locker should be used.
func NewSyncClientStream[T handlerrpc.StreamMessage](
	ctx context.Context, srv grpc.ServerStream, newT func() T, opts ...Option,
) *SyncClientStream[T] {
	var o clientStreamOptions
	for _, opt := range opts {
		opt(&o)
	}
	s := &SyncClientStream[T]{
		ctx:       ctx,
		stream:    srv,
		closeChan: make(chan error),
		newT:      newT,
	}
	s.images = imageStore{cfg: o.graphics, enabled: o.enabled, log: s.log}
	return s
}

// ScheduleResponse schedules the install response to be sent
// on the next tui.Handler method call to this ClientStream.
//
// This method must only be called once.
func (s *SyncClientStream[T]) ScheduleResponse(resp *handlerrpc.ServerMessage) {
	if !s.respPending.CompareAndSwap(false, true) {
		panic("cannot call ScheduleResponse twice")
	}
	s.respPendingMsg = resp
}

// ReceiveMessages blocks until all messages have been received and the stream
// is ready to be closed.
func (s *SyncClientStream[T]) ReceiveMessages() error {
	select {
	case err := <-s.closeChan:
		return err
	case <-s.ctx.Done():
		return s.ctx.Err()
	}
}

// Handle satisfies Handler.
func (s *SyncClientStream[T]) Handle(ev term.Event) (exit, handled bool) {
	pending := s.respPending.CompareAndSwap(true, false)
	if pending {
		if err := s.stream.SendMsg(s.respPendingMsg); err != nil {
			s.closeStream(fmt.Errorf("send install response: %w", err))
			return
		}
	}
	var tev termrpc.Event
	err := tev.FromModel(ev)
	if err != nil {
		s.closeStream(fmt.Errorf("convert ev to proto ev: %w", err))
		return false, false
	}
	req := handlerrpc.HandleStreamRequest{Event: &tev}
	sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Handle, Handle: &req}
	if err := s.stream.SendMsg(&sendMsg); err != nil {
		s.closeStream(fmt.Errorf("send handle message: %w", err))
		return
	}

	recvMsg := s.newT()
	if err := s.stream.RecvMsg(recvMsg); err != nil {
		s.closeStream(fmt.Errorf("receive handle message: %w", err))
		return
	}

	if tpe := recvMsg.GetType(); tpe != handlerrpc.MessageType_Handle {
		err := fmt.Errorf("receive handle message: received extraneous msg: %v", tpe)
		s.closeStream(err)
		return
	}

	handle := recvMsg.GetHandle()
	if ev.Type == term.EventMouse && handle.PointerShape != nil {
		term.SetPointerShape(ev, term.PointerShape(handle.GetPointerShape()))
	}
	exit = handle.GetQuit()
	handled = handle.GetHandled()
	return
}

// Cursor satisfies Handler.
func (s *SyncClientStream[T]) Cursor() (c term.Coordinates, cs term.CursorStyle, show bool) {
	pending := s.respPending.CompareAndSwap(true, false)
	if pending {
		if err := s.stream.SendMsg(s.respPendingMsg); err != nil {
			s.closeStream(fmt.Errorf("send install response: %w", err))
			return
		}
	}
	var req handlerrpc.CursorStreamRequest
	sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Cursor, Cursor: &req}
	err := s.stream.SendMsg(&sendMsg)
	if err != nil {
		err := fmt.Errorf("send cursor message: %w", err)
		s.closeStream(err)
		return
	}

	recvMsg := s.newT()
	if err := s.stream.RecvMsg(recvMsg); err != nil {
		s.closeStream(fmt.Errorf("receive cursor message: %w", err))
		return
	}

	if tpe := recvMsg.GetType(); tpe != handlerrpc.MessageType_Cursor {
		err := fmt.Errorf("receive cursor message: received extraneous msg: %v", tpe)
		s.closeStream(err)
		return
	}

	cursor := recvMsg.GetCursor()
	show = cursor.GetShow()
	if !show {
		return
	}
	cs = term.CursorStyle(cursor.GetStyle())
	c.X = int(cursor.GetPosition().GetX())
	c.Y = int(cursor.GetPosition().GetY())
	return
}

// Selection satisfies Handler.
func (s *SyncClientStream[T]) Selection() (string, bool) {
	pending := s.respPending.CompareAndSwap(true, false)
	if pending {
		if err := s.stream.SendMsg(s.respPendingMsg); err != nil {
			s.closeStream(fmt.Errorf("send install response: %w", err))
			return "", false
		}
	}
	var req handlerrpc.SelectionStreamRequest
	sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Selection, Selection: &req}
	err := s.stream.SendMsg(&sendMsg)
	if err != nil {
		err := fmt.Errorf("send selection message: %w", err)
		s.closeStream(err)
		return "", false
	}

	recvMsg := s.newT()
	if err := s.stream.RecvMsg(recvMsg); err != nil {
		s.closeStream(fmt.Errorf("receive selection message: %w", err))
		return "", false
	}

	if tpe := recvMsg.GetType(); tpe != handlerrpc.MessageType_Selection {
		err := fmt.Errorf("receive selection message: received extraneous msg: %v", tpe)
		s.closeStream(err)
		return "", false
	}

	selection := recvMsg.GetSelection()
	return selection.GetText(), selection.GetOk()
}

// Resize satisfies Handler.
func (s *SyncClientStream[T]) Resize(width, height int) {
	pending := s.respPending.CompareAndSwap(true, false)
	if pending {
		if err := s.stream.SendMsg(s.respPendingMsg); err != nil {
			s.closeStream(fmt.Errorf("send install response: %w", err))
			return
		}
	}
	// store for error displaying
	s.height.Store(int32(height))
	s.width.Store(int32(width))
	s.sendResize()
}

// sendResize tells the extension the size of the handler and, while the
// frames tell the cell size, how to send images.
func (s *SyncClientStream[T]) sendResize() bool {
	s.resized = true
	s.sentCellPixelSize = s.cellPixelSize
	req := handlerrpc.ResizeStreamRequest{
		Width:    s.width.Load(),
		Height:   s.height.Load(),
		Graphics: s.images.graphics(s.cellPixelSize),
	}
	sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Resize, Resize: &req}
	if err := s.stream.SendMsg(&sendMsg); err != nil {
		s.closeStream(fmt.Errorf("send resize message: %w", err))
		return false
	}
	return true
}

// Draw satisfies Handler.
func (s *SyncClientStream[T]) Draw(w term.Writer) {
	if s.closed.Load() {
		width := int(s.width.Load())
		height := int(s.height.Load())
		comp := component.NewStringWithConfig(tcomponent.ProblemArt,
			component.StringConfig{Alignment: component.AlignmentCentered})
		comp.Resize(width, height)
		draw := handlerrpc.NewDrawResponse(w.Context(), comp, width, height, true)
		doDraw(w, draw)
		return
	}
	pending := s.respPending.CompareAndSwap(true, false)
	if pending {
		if err := s.stream.SendMsg(s.respPendingMsg); err != nil {
			s.closeStream(fmt.Errorf("send install response: %w", err))
			return
		}
	}
	s.images.beginDraw()
	// Only frames tell the cell size, which can change without the size in
	// cells.
	s.cellPixelSize = pixelSizeOf(w.Context())
	if s.images.enabled && s.resized && s.cellPixelSize != s.sentCellPixelSize && !s.sendResize() {
		return
	}
	req := handlerrpc.DrawStreamRequest{
		PackedOk:         true,
		CellsUnchangedOk: true,
		ImageAck:         s.images.ack,
	}
	sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Draw, Draw: &req}
	err := s.stream.SendMsg(&sendMsg)
	if err != nil {
		err := fmt.Errorf("send draw message: %w", err)
		s.closeStream(err)
		return
	}

	var streamed streamedUploads
	var draw *handlerrpc.DrawStreamResponse
	for draw == nil {
		recvMsg := s.newT()
		if err := s.stream.RecvMsg(recvMsg); err != nil {
			s.closeStream(fmt.Errorf("receive draw message: %w", err))
			return
		}
		if tpe := recvMsg.GetType(); tpe != handlerrpc.MessageType_Draw {
			err := fmt.Errorf("receive draw message: received extraneous msg: %v", tpe)
			s.closeStream(err)
			return
		}
		if resp := recvMsg.GetDraw(); resp.GetMore() {
			streamed.add(resp, s.images.maxBytes())
		} else {
			draw = resp
		}
	}
	streamed.attach(draw)
	jobs := s.images.plan(draw)
	readUploads(jobs)
	if draw.GetCellsUnchanged() {
		draw.Rows, draw.Packed = s.last.GetRows(), s.last.GetPacked()
	}
	// Every upload is acknowledged with the next request, so the extension
	// never has to place an older version and ask for another draw.
	s.images.commit(draw, jobs)
	s.last = draw

	doDraw(w, draw)
	// Cells written after a placement cover it, so the placements go last.
	s.images.draw(w, draw.GetImages())
}

// Dimensions satisfies Handler.
func (s *SyncClientStream[T]) Dimensions() (width int, height int) {
	pending := s.respPending.CompareAndSwap(true, false)
	if pending {
		if err := s.stream.SendMsg(s.respPendingMsg); err != nil {
			s.closeStream(fmt.Errorf("send install response: %w", err))
			return
		}
	}
	var req handlerrpc.DimensionsStreamRequest
	sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Dimensions, Dimensions: &req}
	err := s.stream.SendMsg(&sendMsg)
	if err != nil {
		err := fmt.Errorf("send dimensions message: %w", err)
		s.closeStream(err)
		return
	}

	recvMsg := s.newT()
	if err := s.stream.RecvMsg(recvMsg); err != nil {
		s.closeStream(fmt.Errorf("receive dimensions message: %w", err))
		return
	}

	if tpe := recvMsg.GetType(); tpe != handlerrpc.MessageType_Dimensions {
		err := fmt.Errorf("receive dimensions message: received extraneous msg: %v", tpe)
		s.closeStream(err)
		return
	}

	width = int(recvMsg.GetDimensions().GetWidth())
	height = int(recvMsg.GetDimensions().GetHeight())
	return
}

// Close satisfies Handler.
func (s *SyncClientStream[T]) Close() error {
	s.images.close()
	var req handlerrpc.CloseStreamRequest
	msg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Close, Close: &req}
	err := s.stream.SendMsg(&msg)
	if err != nil {
		err = fmt.Errorf("send close message: %w", err)
		s.closeStream(err)
		return err
	}

	// Close might be called while Handle is still being processed
	// by ServerStream. This enables stream to gracefully close
	// at the same time we don't need to implement a multi-goroutine
	// stream client or server. Keeps things simple at the expense
	// of assuming that no other methods will be called by host
	// during the processing of some other method. A small price to pay.
	go debug.CapturePanicReport(func() {
		recvMsg := s.newT()
		err := s.stream.RecvMsg(recvMsg)
		if err != nil {
			err = fmt.Errorf("receive close message: %w", err)
		}
		s.closeStream(err)
	})

	return nil
}

func (s *SyncClientStream[T]) closeStream(err error) {
	if !s.closed.CompareAndSwap(false, true) {
		s.log(log.TraceLevel, "close stream called multiple times")
		return
	}
	if err != nil &&
		(errors.Is(err, io.EOF) || strings.Contains(err.Error(), "context canceled")) {
		err = nil
	}
	if err != nil {
		s.log(log.ErrorLevel, "closing stream due to error: %v", err)
	}
	select {
	case s.closeChan <- err:
	default:
	}
}

func (s *SyncClientStream[T]) log(level log.Level, msg string, args ...any) {
	log.WithField(logging.KeyClass, "handlerrpc.SyncClientStream").Logf(level, msg, args...)
}
