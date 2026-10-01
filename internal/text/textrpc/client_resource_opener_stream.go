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
	"sync"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

var _ textapi.ResourceOpenHandler = (*resourceOpenerClientStream)(nil)

// subset of Editor_SubscribeResourceOpenerServer
type resourceOpenerServerStream interface {
	RecvMsg(any) error
	Send(*textrpc.ServerResourceOpenerMessage) error
}

type openResult struct {
	h   browserapi.Handler
	err error
}

// pendingOpens is the table of open requests still waiting for their
// answer. An answer is either an error on the opener stream or a handler
// stream naming the request, so the table is shared by every stream of a
// Server and ids are unique across them.
type pendingOpens struct {
	counter atomic.Int64
	// id -> chan openResult, buffered so answering never blocks
	m sync.Map
}

func (p *pendingOpens) add() (int64, chan openResult) {
	id := p.counter.Add(1)
	reply := make(chan openResult, 1)
	p.m.Store(id, reply)
	return id, reply
}

// claim takes the request out of the table. Whoever claims it must send
// exactly one result on the returned channel.
func (p *pendingOpens) claim(id int64) (chan openResult, bool) {
	reply, ok := p.m.LoadAndDelete(id)
	if !ok {
		return nil, false
	}
	return reply.(chan openResult), true
}

// abandon gives up waiting for the request. A handler that was already
// on its way is closed, since nobody else will.
func (p *pendingOpens) abandon(id int64, reply chan openResult) {
	if _, ok := p.claim(id); ok {
		return
	}
	if res := <-reply; res.h != nil {
		_ = res.h.Close()
	}
}

// resourceOpenerClientStream is the extension's resource opener, reached
// over its SubscribeResourceOpener stream.
type resourceOpenerClientStream struct {
	log    *slog.Logger
	ctx    context.Context
	stream resourceOpenerServerStream
	sendMu sync.Mutex
	opens  *pendingOpens
}

func newResourceOpenerClientStream(
	ctx context.Context, stream resourceOpenerServerStream, opens *pendingOpens,
) *resourceOpenerClientStream {
	return &resourceOpenerClientStream{
		log:    slog.Default().With("struct", "textrpc.resourceOpenerClientStream"),
		ctx:    ctx,
		stream: stream,
		opens:  opens,
	}
}

func (c *resourceOpenerClientStream) send(msg *textrpc.ServerResourceOpenerMessage) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.stream.Send(msg)
}

// OpenResource satisfies textapi.ResourceOpenHandler. The handler it
// returns is served by the extension over the stream that answered the
// request; closing it ends that stream.
func (c *resourceOpenerClientStream) OpenResource(
	ctx context.Context, uri workspaceapi.URI,
) (browserapi.Handler, error) {
	id, reply := c.opens.add()

	err := c.send(&textrpc.ServerResourceOpenerMessage{
		Type: textrpc.ServerResourceOpenerMessage_Open,
		Open: &textrpc.OpenResourceRequest{
			Id:  id,
			Uri: NewURI(uri),
		},
	})
	if err != nil {
		c.opens.abandon(id, reply)
		return nil, fmt.Errorf("send open resource request: %w", err)
	}

	select {
	case res := <-reply:
		return res.h, res.err
	case <-ctx.Done():
		c.opens.abandon(id, reply)
		err := c.send(&textrpc.ServerResourceOpenerMessage{
			Type:       textrpc.ServerResourceOpenerMessage_OpenCancel,
			OpenCancel: &textrpc.RequestCancel{Id: id},
		})
		if err != nil {
			c.log.Warn("send open resource cancel", "error", err)
		}
		return nil, ctx.Err()
	case <-c.ctx.Done():
		c.opens.abandon(id, reply)
		return nil, fmt.Errorf("open %s: extension disconnected", uri)
	}
}

func (c *resourceOpenerClientStream) receiveMessages() error {
	for {
		var msg textrpc.ClientResourceOpenerMessage
		if err := c.stream.RecvMsg(&msg); err != nil {
			return fmt.Errorf("receive stream message: %w", err)
		}
		switch msg.GetType() {
		case textrpc.ClientResourceOpenerMessage_Open:
			open := msg.GetOpen()
			reply, ok := c.opens.claim(open.GetId())
			if !ok {
				// the editor stopped waiting for it
				continue
			}
			err := errors.New(open.GetError())
			if open.GetError() == "" {
				err = errors.New("extension reported no error and no handler")
			}
			reply <- openResult{err: err}
		default:
			c.log.Warn("received extraneous message type", "type", msg.GetType())
		}
	}
}
