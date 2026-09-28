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

package textrpc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
)

type commandClientStream struct {
	log           *slog.Logger
	ctx           context.Context
	handleCommand chan string
	stream        serverStream
	completers    sync.Map
	counter       int64
	// unlike counter, reqCounter is bumped from whichever goroutine
	// dispatches the command.
	reqCounter atomic.Int64
	pendingMu  sync.Mutex
	pending    []pendingHandle
	sendMu     sync.Mutex
	// whether the extension understands CompleteCancel messages; older
	// SDKs terminate the stream when they receive an unknown message type.
	supportsCompleteCancel bool
	// whether the extension understands HandleCancel messages and echoes
	// request ids back. Same caveat as supportsCompleteCancel.
	supportsHandleCancel bool
}

// pendingHandle is a dispatched command awaiting its reply. A nil ch
// means a fire-and-forget dispatch, whose result only gets logged.
type pendingHandle struct {
	id int64
	ch chan error
	// closed once the reply lands, so a cancel watcher stops waiting
	done chan struct{}
}

// subset of Editor_SubscribeCommandServer
type serverStream interface {
	RecvMsg(any) error
	Send(*textrpc.ServerCommandMessage) error
}

func newCommandClientStream(
	ctx context.Context, stream serverStream,
	supportsCompleteCancel, supportsHandleCancel bool,
) *commandClientStream {
	log := slog.Default().With("struct", "textrpc.commandClientStream")
	return &commandClientStream{
		log:                    log,
		stream:                 stream,
		ctx:                    ctx,
		handleCommand:          make(chan string),
		supportsCompleteCancel: supportsCompleteCancel,
		supportsHandleCancel:   supportsHandleCancel,
	}
}

func (c *commandClientStream) send(msg *textrpc.ServerCommandMessage) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.stream.Send(msg)
}

func (c *commandClientStream) receiveMessages() error {
	for {
		var msg textrpc.ClientCommandMessage
		err := c.stream.RecvMsg(&msg)
		if err != nil {
			return fmt.Errorf("receive stream message: %w", err)
		}

		switch msg.GetType() {
		case textrpc.ClientCommandMessage_Handle:
			handle := msg.GetHandle()
			errStr := handle.GetError()
			waitCh, claimed := c.takePending(handle.GetId())
			if claimed && waitCh != nil {
				var err error
				if errStr != "" {
					err = errors.New(errStr)
				}
				select {
				case waitCh <- err:
				case <-c.ctx.Done():
					return c.ctx.Err()
				}
				continue
			}
			select {
			case c.handleCommand <- errStr:
			case <-c.ctx.Done():
				return c.ctx.Err()
			}
		case textrpc.ClientCommandMessage_CompleteValue:
			complete := msg.GetCompleteValue()
			id := complete.GetId()
			if id == 0 {
				c.log.Warn("received complete value message with invalid id")
				continue
			}
			val, ok := c.completers.Load(id)
			if !ok {
				c.log.Warn("received complete message for unknown stream")
				continue
			}
			chanCtx := val.(chanCtx)
			if chanCtx.cancelled {
				continue
			}
			chanValue := chanValue{
				val: complete.GetValue(),
			}
			select {
			case chanCtx.ch <- chanValue:
			case <-chanCtx.ctx.Done():
				continue
			case <-c.ctx.Done():
				return c.ctx.Err()
			}
		case textrpc.ClientCommandMessage_CompleteDone:
			done := msg.GetCompleteDone()
			id := done.GetId()
			if id == 0 {
				c.log.Warn("received complete done message with invalid id")
				continue
			}
			val, ok := c.completers.LoadAndDelete(id)
			if !ok {
				continue
			}
			chanCtx := val.(chanCtx)
			if chanCtx.cancelled {
				continue
			}
			errStr := done.GetError()
			if errStr == "" {
				close(chanCtx.ch)
				continue
			}

			chanValue := chanValue{
				err: errors.New(errStr),
			}
			select {
			case chanCtx.ch <- chanValue:
				continue
			case <-chanCtx.ctx.Done():
				continue
			case <-c.ctx.Done():
				return c.ctx.Err()
			}
		default:
			c.log.Warn("received extraneous message type", "type", msg.GetType())
		}
	}
}

func (c *commandClientStream) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) error {
	// Every send reserves a slot for its reply, held across Send so that
	// slot order matches wire order for extensions that predate request
	// ids. A caller that attaches a Waiter reserves its channel and is
	// told, via Claimed, to wait for the result on it; otherwise the
	// slot carries no channel and the reply takes the fire-and-forget
	// log path. Claim must happen before this returns.
	var replyCh chan error
	if w, ok := WaiterFromContext(ctx); ok {
		replyCh = w.Ch
		w.Claimed = true
	}
	pending := pendingHandle{
		id:   c.reqCounter.Add(1), // start at 1, so 0 means "no id"
		ch:   replyCh,
		done: make(chan struct{}),
	}
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	c.pending = append(c.pending, pending)
	if err := c.send(c.buildHandleRequest(cmd, pending.id)); err != nil {
		c.pending = c.pending[:len(c.pending)-1]
		return fmt.Errorf("send complete request: %w", err)
	}
	if replyCh != nil && c.supportsHandleCancel {
		go c.cancelOnDone(ctx, pending)
	}
	return nil
}

// takePending removes the slot the reply belongs to and reports whether
// there was one. An id of zero comes from an extension that predates
// request ids, whose replies still arrive in dispatch order.
func (c *commandClientStream) takePending(id int64) (chan error, bool) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	idx := 0
	if id != 0 {
		idx = slices.IndexFunc(c.pending, func(p pendingHandle) bool {
			return p.id == id
		})
		if idx < 0 {
			return nil, false
		}
	} else if len(c.pending) == 0 {
		return nil, false
	}
	slot := c.pending[idx]
	c.pending = slices.Delete(c.pending, idx, idx+1)
	close(slot.done)
	return slot.ch, true
}

func (c *commandClientStream) cancelOnDone(ctx context.Context, p pendingHandle) {
	select {
	case <-p.done:
	case <-c.ctx.Done():
	case <-ctx.Done():
		msg := textrpc.ServerCommandMessage{
			Type:         textrpc.ServerCommandMessage_HandleCancel,
			HandleCancel: &textrpc.RequestCancel{Id: p.id},
		}
		if err := c.send(&msg); err != nil {
			c.log.Warn("send handle cancel", "error", err)
		}
	}
}

func (c *commandClientStream) buildHandleRequest(
	cmd textapi.Command, id int64,
) *textrpc.ServerCommandMessage {
	var cursorContent, cursorWindow termrpc.Coordinates
	cursorContent.FromModel(cmd.Cursor.Content)
	cursorWindow.FromModel(cmd.Cursor.Window)

	req := textrpc.HandleCommandRequest{
		Name:          cmd.Name,
		Args:          cmd.Args,
		CursorContent: &cursorContent,
		CursorWindow:  &cursorWindow,
		Id:            id,
	}
	if cmd.Window != nil {
		req.WindowId = cmd.Window.WindowID()
	}
	if cmd.URI != (workspaceapi.URI{}) {
		req.ResourceName = NewURI(cmd.URI)
	}

	var reqMsg textrpc.ServerCommandMessage
	reqMsg.Type = textrpc.ServerCommandMessage_Handle
	reqMsg.Handle = &req
	return &reqMsg
}

func (c *commandClientStream) Complete(ctx context.Context, cmd textapi.Command) (
	iterator.Iterator[string], string, error,
) {
	c.counter++ // start with 1, so 0 is a missing ID error
	id := c.counter

	var req textrpc.CompleteCommandRequest
	req.Id = id
	req.Name = cmd.Name
	req.Args = cmd.Args
	// the rest of fields are not propagated, since textapi.CommandHandler
	// doesn't have he same signature as text.CommandHandler.

	var reqMsg textrpc.ServerCommandMessage
	reqMsg.Type = textrpc.ServerCommandMessage_Complete
	reqMsg.Complete = &req

	ctx, cancelCtx := context.WithCancel(ctx)
	ch := make(chan chanValue)
	c.completers.Store(id, chanCtx{ctx: ctx, ch: ch})
	if err := c.send(&reqMsg); err != nil {
		cancelCtx()
		c.completers.Delete(id)
		return nil, "", fmt.Errorf("send complete request: %w", err)
	}

	return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
		select {
		case next, ok := <-ch:
			if !ok {
				return "", false, nil
			}
			if next.err != nil {
				close(ch) // force next to return !ok, in case of poor impls
				return "", false, next.err
			}
			return next.val, true, nil
		case <-ctx.Done():
			return "", false, ctx.Err()
		}
	}, func() error {
		cancelCtx()
		// keep a tombstone so values still in flight for this abandoned
		// completion are dropped silently; CompleteDone clears it.
		c.completers.Store(id, chanCtx{cancelled: true})
		if !c.supportsCompleteCancel {
			return nil
		}
		cancelMsg := textrpc.ServerCommandMessage{
			Type:           textrpc.ServerCommandMessage_CompleteCancel,
			CompleteCancel: &textrpc.CompleteCommandCancel{Id: id},
		}
		if err := c.send(&cancelMsg); err != nil {
			c.log.Warn("send complete cancel", "error", err)
		}
		return nil
	}), "", nil
}

// enables canceling the iterator from two different places:
// iterator.Close, which produces a ctx.Err() error
// and when the producer is done sending values
// via the corresponding proto message, in which case the channel
// is closed and we gracefully terminate the iterator.
type chanCtx struct {
	ctx context.Context
	ch  chan chanValue
	// tombstone for a completion abandoned by the editor: its id stays
	// known until the extension reports the completion done.
	cancelled bool
}

type chanValue struct {
	val string
	err error
}
