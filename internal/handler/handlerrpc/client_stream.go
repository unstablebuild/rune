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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlerrpc"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
	"github.com/unstablebuild/rune-go-sdk/tui"
	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
	"unstable.build/rune/internal/debug"
)

// ClientStream implements a tui.Handler (+handler.Floating) server over
// a grpc.ServerStream.
type ClientStream[T handlerrpc.StreamMessage] struct {
	ctx       context.Context
	cancel    func()
	stream    grpc.ServerStream
	closeChan chan error
	publisher func(ev term.Event) error
	exit      atomic.Bool
	closed    atomic.Bool
	newT      func() T

	respPending    atomic.Bool
	respPendingMsg *handlerrpc.ServerMessage

	// these are all cached when client calls Draw
	mu             sync.Mutex
	contextPayload string
	height         int
	width          int
	state          asyncState
	cursor         *handlerrpc.CursorStreamResponse
	selection      *handlerrpc.SelectionStreamResponse
	dimensions     *handlerrpc.DimensionsStreamResponse
	// cursorInDraw records that the peer piggybacks the cursor on the draw
	// response, which makes the separate cursor request redundant.
	cursorInDraw bool

	req struct {
		height, width int
		ctx           context.Context
	}
	resp struct {
		*handlerrpc.DrawStreamResponse
		ctx           context.Context
		height, width int
	}
}

// NewClientStream allocates storage for a new ClientStream and initializes it
// with the given grpc.ServerStream and type parameter constructor.
func NewClientStream[T handlerrpc.StreamMessage](
	ctx context.Context, srv grpc.ServerStream, newT func() T,
	publisher func(ev term.Event) error,
) *ClientStream[T] {
	ctx, cancel := context.WithCancel(ctx)
	s := &ClientStream[T]{
		ctx:       ctx,
		cancel:    cancel,
		stream:    srv,
		closeChan: make(chan error),
		newT:      newT,
		publisher: publisher,
	}
	s.contextPayload = makeContextPayload(s)
	s.resp.ctx = context.Background()
	return s
}

// ScheduleResponse schedules the install response to be sent
// on the next tui.Handler method call to this ClientStream.
//
// This method must only be called once.
func (s *ClientStream[T]) ScheduleResponse(resp *handlerrpc.ServerMessage) {
	if !s.respPending.CompareAndSwap(false, true) {
		panic("cannot call ScheduleResponse twice")
	}
	s.respPendingMsg = resp
}

// ReceiveMessages blocks until all messages have been received and the stream
// is ready to be closed.
func (s *ClientStream[T]) ReceiveMessages() (ret error) {
	defer func() {
		s.log(log.DebugLevel, "receive messages returned: error: %v", ret)
	}()

	go debug.CapturePanicReport(func() {
		s.log(log.TraceLevel, "streaming messages")
		s.closeStream(s.receiveMessages())
	})

	select {
	case err := <-s.closeChan:
		ret = err
		return
	case <-s.ctx.Done():
		ret = fmt.Errorf("context is done: %w", s.ctx.Err())
		return
	}
}

var clientStreamRepublishKey = []byte("__handlerrpc.ClientStreamRepublish")

// Handle satisfies Handler.
func (s *ClientStream[T]) Handle(ev term.Event) (exit, handled bool) {
	if s.exit.Load() {
		exit = true
		return
	}
	if s.closed.Load() {
		return
	}
	// republished event that's ignored because
	// handler server responded with handled=false.
	if bytes.Equal(ev.Raw, clientStreamRepublishKey) {
		return false, false
	}
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
		return
	}
	req := handlerrpc.HandleStreamRequest{Event: &tev}
	sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Handle, Handle: &req}
	if err := s.stream.SendMsg(&sendMsg); err != nil {
		s.closeStream(fmt.Errorf("send handle message: %w", err))
		return
	}

	// it's important for Handle to remain asynchronous:
	// it allows for extensions to call API and avoid deadlocks
	// due to extensions needing the global lock to be open to call the host
	// at the same time the host is locked waiting for Handle to return.

	handled = true
	return
}

// Cursor satisfies Handler. This method returns the last cursor collected by Draw.
func (s *ClientStream[T]) Cursor() (c term.Coordinates, cs term.CursorStyle, show bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cursor := s.cursor
	show = cursor.GetShow()
	if !show {
		return
	}
	cs = term.CursorStyle(cursor.GetStyle())
	c.X = int(cursor.GetPosition().GetX())
	c.Y = int(cursor.GetPosition().GetY())
	return
}

// Selection satisfies Handler. This method returns the last selection collected by Draw.
func (s *ClientStream[T]) Selection() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	selection := s.selection
	return selection.GetText(), selection.GetOk()
}

// Resize satisfies Handler.
func (s *ClientStream[T]) Resize(width, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// store in any case for error displaying
	s.height = height
	s.width = width

	if s.closed.Load() {
		return
	}

	pending := s.respPending.CompareAndSwap(true, false)
	if pending {
		if err := s.stream.SendMsg(s.respPendingMsg); err != nil {
			s.closeStream(fmt.Errorf("send install response: %w", err))
			return
		}
	}

	var req handlerrpc.ResizeStreamRequest
	req.Width = int32(width)
	req.Height = int32(height)
	sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Resize, Resize: &req}
	err := s.stream.SendMsg(&sendMsg)
	if err != nil {
		err := fmt.Errorf("send resize message: %w", err)
		s.closeStream(err)
		return
	}
}

// Dimensions satisfies Handler. This method returns the last
// dimensions collected by Draw.
func (s *ClientStream[T]) Dimensions() (width int, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	dim := s.dimensions
	width = int(dim.GetWidth())
	height = int(dim.GetHeight())
	return
}

// Draw satisfies Handler.
func (s *ClientStream[T]) Draw(w term.Writer) {
	pending := s.respPending.CompareAndSwap(true, false)
	if pending {
		if err := s.stream.SendMsg(s.respPendingMsg); err != nil {
			s.closeStream(fmt.Errorf("send install response: %w", err))
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := w.Context()
	iterationID, reqIsTick := tui.IterationFromContext(ctx)

	switch s.state {
	case stateAsyncIdle:
		readySameDimensions := s.width == s.resp.width &&
			s.height == s.resp.height
		respIterationID, respIsTick := tui.IterationFromContext(s.resp.ctx)
		olderIterationID := respIsTick && iterationID <= respIterationID
		reqIsSameIterationID := (reqIsTick && olderIterationID)
		selfInterrupt := !reqIsTick && !respIsTick && s.contextPayloadIsSelf(ctx)

		if readySameDimensions && (reqIsSameIterationID || selfInterrupt ||
			s.contextPayloadIsOtherAsyncClient(ctx)) {
			s.drawReady(w)
		} else if s.scheduleDrawRequest(ctx, reqIsTick) {
			s.drawPending(w)
		} else {
			s.log(log.TraceLevel, "could not schedule draw request")
			s.drawError(w)
		}
	case stateAsyncPending:
		pendingRespSameDimensions := s.width == s.req.width &&
			s.height == s.req.height
		pendingResponseIterationID, pendingResponseIsTick := tui.IterationFromContext(s.req.ctx)
		olderIterationID := pendingResponseIsTick && iterationID <= pendingResponseIterationID
		pendingResponseIsSameIterationID := (reqIsTick && olderIterationID)

		if pendingRespSameDimensions && (pendingResponseIsSameIterationID ||
			s.contextPayloadIsOtherAsyncClient(ctx)) {
			s.drawPending(w)
		} else if s.scheduleDrawRequest(ctx, reqIsTick) {
			s.drawPending(w)
		} else {
			s.log(log.TraceLevel, "could not schedule draw request")
			s.drawError(w)
		}
	case stateAsyncCircuitBreak:
		s.log(log.TraceLevel, "received draw but state is circuit break")
		s.drawError(w)
	default:
		panic("unknown state")
	}
}

// Close satisfies Handler.
func (s *ClientStream[T]) Close() error {
	if s.closed.Load() {
		return nil
	}
	s.log(log.TraceLevel, "Close")
	var req handlerrpc.CloseStreamRequest
	msg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Close, Close: &req}
	err := s.stream.SendMsg(&msg)
	if err != nil {
		err = fmt.Errorf("send close message: %w", err)
		s.closeStream(err)
		return err
	}

	return nil
}

const (
	smtgWrongCopy = `

          ___
         /___/\_               
        _\   \/_/\__           
      __\       \/_/\          
      \   __    __ \ \         
     __\  \_\   \_\ \ \   __   
    /_/\\   __   __  \ \_/_/\  
    \_\/_\__\/\__\/\__\/_\_\/  
       \_\/_/\       /_\_\/    
          \_\/       \_\/      
    

Uh, Houston, we've had a problem
`
)

var _ tui.Handler = (*ClientStream[handlerrpc.StreamMessage])(nil)

type asyncState uint8

const (
	stateAsyncIdle asyncState = iota
	stateAsyncPending
	stateAsyncCircuitBreak
)

func (s *ClientStream[T]) scheduleDrawRequest(ctx context.Context, reqIsTick bool) bool {
	// make sure that all requests that we schedule interrupts for have
	// either an iteration ID or a payload that we can recognize.
	if !reqIsTick {
		ctx = s.contextWithSelfPayload(ctx)
	}

	s.state = stateAsyncPending
	s.req.width = s.width
	s.req.height = s.height
	s.req.ctx = ctx

	// SDKs that piggyback the cursor on the draw response make this
	// request redundant, and answering it before the draw is what makes
	// the cursor lag the frame it belongs to. Old SDKs never set it, so
	// keep asking until the peer proves otherwise.
	if !s.cursorInDraw {
		var req handlerrpc.CursorStreamRequest
		sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Cursor, Cursor: &req}
		err := s.stream.SendMsg(&sendMsg)
		if err != nil {
			err := fmt.Errorf("send cursor message: %w", err)
			s.closeStream(err)
			return false
		}
	}
	{
		var req handlerrpc.SelectionStreamRequest
		sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Selection, Selection: &req}
		err := s.stream.SendMsg(&sendMsg)
		if err != nil {
			err := fmt.Errorf("send selection message: %w", err)
			s.closeStream(err)
			return false
		}
	}
	{
		var req handlerrpc.DimensionsStreamRequest
		sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Dimensions, Dimensions: &req}
		err := s.stream.SendMsg(&sendMsg)
		if err != nil {
			err := fmt.Errorf("send dimensions message: %w", err)
			s.closeStream(err)
			return false
		}
	}

	// finally send a draw request, which will trigger the final interrupt
	{
		req := handlerrpc.DrawStreamRequest{PackedOk: true}
		sendMsg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Draw, Draw: &req}
		err := s.stream.SendMsg(&sendMsg)
		if err != nil {
			err := fmt.Errorf("send draw message: %w", err)
			s.closeStream(err)
			return false
		}
	}

	return true
}

func (s *ClientStream[T]) processDraw(resp *handlerrpc.DrawStreamResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()

	payload, _ := term.PayloadFromContext(s.req.ctx)

	//nolint:errcheck
	defer s.publisher(term.Event{Type: term.EventInterrupt, Raw: payload})

	s.resp.DrawStreamResponse = resp

	if cursor := resp.GetCursor(); cursor != nil {
		// The cursor was computed against the frame we just received, so
		// it supersedes any answer to the separate cursor request.
		s.cursorInDraw = true
		s.cursor = &handlerrpc.CursorStreamResponse{
			Position: cursor.GetPosition(),
			Style:    cursor.GetStyle(),
			Show:     cursor.GetShow(),
		}
	}

	s.resp.ctx = s.req.ctx
	s.resp.height = int(s.req.height)
	s.resp.width = int(s.req.width)

	s.state = stateAsyncIdle
}

func (s *ClientStream[T]) receiveMessages() (ret error) {
	defer func() {
		s.cancel()
		s.log(log.DebugLevel, "done receiving messages: %v", ret)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.state = stateAsyncCircuitBreak
	}()

	for {
		recvMsg := s.newT()
		err := s.stream.RecvMsg(recvMsg)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				ret = fmt.Errorf("context is canceled: %w", ret)
			} else if s, ok := status.FromError(err); ok && s.Code() == codes.Canceled {
				ret = fmt.Errorf("received status code: %v", s)
			} else {
				ret = err
			}
			return
		}

		switch recvMsg.GetType() {
		case handlerrpc.MessageType_Draw:
			draw := recvMsg.GetDraw()
			if draw == nil {
				ret = fmt.Errorf("receive draw message: missing data")
			}
			s.processDraw(draw)
		case handlerrpc.MessageType_Cursor:
			cursor := recvMsg.GetCursor()
			if cursor == nil {
				ret = fmt.Errorf("receive cursor message: missing data")
				return
			}
			s.mu.Lock()
			s.cursor = cursor
			s.mu.Unlock()
		case handlerrpc.MessageType_Selection:
			selection := recvMsg.GetSelection()
			if selection == nil {
				ret = fmt.Errorf("receive selection message: missing data")
				return
			}
			s.mu.Lock()
			s.selection = selection
			s.mu.Unlock()
		case handlerrpc.MessageType_Dimensions:
			dimensions := recvMsg.GetDimensions()
			if dimensions == nil {
				ret = fmt.Errorf("receive dimensions message: missing data")
				return
			}
			s.mu.Lock()
			s.dimensions = dimensions
			s.mu.Unlock()
		case handlerrpc.MessageType_Handle:
			handle := recvMsg.GetHandle()
			if handle.GetQuit() {
				s.exit.Store(true)
				// handle response might arrive late, and EventNone
				// dispatched to a different Handler. This is an acceptable
				// risk: when user focuses back on handler and sends an event
				// then Handle will retur exit. This should generally not happen.
				err := s.publisher(term.Event{Type: term.EventNone})
				if err != nil {
					ret = fmt.Errorf("publish event none: %w", err)
					return

				}
				// shortcircuit calling Close so there aren't unintended
				// side effects from returning exit=false in the call that
				// originated this response. Publishing term.EventNone,
				// might not be enough, if focus changed.
				if s.closed.CompareAndSwap(false, true) {
					var req handlerrpc.CloseStreamRequest
					msg := handlerrpc.ServerMessage{Type: handlerrpc.MessageType_Close, Close: &req}
					ret = s.stream.SendMsg(&msg)
				}
			} else if !handle.GetHandled() {
				// re-publish event that wasn't handled, so we can return handled=false to Handle
				ev, err := handle.GetRequest().ToModel()
				if err != nil {
					s.log(log.ErrorLevel, "convert handle event back into model: %v", err)
					continue
				}
				// Mouse events are routed by position and ev carries
				// coordinates local to this handler, so the root would
				// deliver it to whichever window sits there instead.
				if ev.Type == term.EventMouse {
					continue
				}
				ev.Raw = clientStreamRepublishKey
				err = s.publisher(ev)
				if err != nil {
					ret = fmt.Errorf("publish event none: %w", err)
					return
				}
			}
		case handlerrpc.MessageType_Resize:
			/* nothing to do*/
		case handlerrpc.MessageType_Close:
			s.log(log.TraceLevel, "received close message")
			return
		default:
			/* case MessageType_Request, MessageType_Response: */
			ret = fmt.Errorf("received extraneous message type: %d", recvMsg.GetType())
			return
		}
	}
}

func (s *ClientStream[T]) closeStream(err error) {
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
	// this forces ReceiveMessages to return, which in turn trickles server
	// to close stream, and receiveMessages RecvMsg returns with error.
	select {
	case s.closeChan <- err:
	default:
	}
}

func (s *ClientStream[T]) drawError(w term.Writer) {
	comp := component.NewStringWithConfig(smtgWrongCopy,
		component.StringConfig{Alignment: component.AlignmentCentered})
	comp.Resize(s.width, s.height)
	comp.Draw(w)
}

func (s *ClientStream[T]) drawPending(w term.Writer) {
	if s.width == s.resp.width && s.height == s.resp.height {
		s.drawReady(w)
	} else {
		loading := component.NewStringWithConfig("LOADING",
			component.StringConfig{Alignment: component.AlignmentCentered})
		loading.Resize(s.width, s.height)
		loading.Draw(w)
	}
}

func (s *ClientStream[T]) drawReady(w term.Writer) {
	doDraw(w, s.resp.DrawStreamResponse)
}

func (s *ClientStream[T]) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithFields(log.Fields{
		logging.KeyClass: "handlerrpc.ClientStream",
		"instance":       fmt.Sprintf("%p", s),
	}).Logf(level, msg, args...)
}

func (s *ClientStream[T]) contextPayloadIsSelf(ctx context.Context) bool {
	payload, ok := term.PayloadFromContext(ctx)
	return ok && s.contextPayload == string(payload)
}

func (s *ClientStream[T]) contextPayloadIsOtherAsyncClient(ctx context.Context) bool {
	payload, ok := term.PayloadFromContext(ctx)
	return ok && strings.HasPrefix(string(payload), ctxPayloadPrefix)
}

const ctxPayloadPrefix = "AsyncClient"

func (s *ClientStream[T]) contextWithSelfPayload(ctx context.Context) context.Context {
	return term.ContextWithPayload(ctx, []byte(s.contextPayload))
}

func makeContextPayload[T handlerrpc.StreamMessage](s *ClientStream[T]) string {
	return fmt.Sprintf("%s:%p", ctxPayloadPrefix, s)
}

// doDraw blits a draw frame into w. Extensions that negotiated the packed
// wire format send columnar planes, which blit without allocating a message
// per cell; older extensions still send per-cell rows.
func doDraw(w term.Writer, resp *handlerrpc.DrawStreamResponse) {
	if packed := resp.GetPacked(); packed != nil {
		packed.WriteTo(w)
		return
	}
	for y, row := range resp.GetRows() {
		for x, c := range row.Cells {
			cell := c.ToModel()
			w.SetCell(term.Coordinates{X: x, Y: y}, cell)
		}
	}
}
