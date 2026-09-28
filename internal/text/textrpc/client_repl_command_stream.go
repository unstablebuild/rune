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

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi/textrpc"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/termrpc"
)

const defaultREPLResponsiveWidth = 80

var _ textapi.REPLHandler = (*replCommandClientStream)(nil)

type replCommandClientStream struct {
	log       *slog.Logger
	ctx       context.Context
	cancelCtx func()
	stream    replServerStream

	mu     sync.Mutex
	handle *responsiveCtx
	help   *responsiveCtx

	completers sync.Map
	counter    int64
	// unlike counter, reqCounter is bumped off the event loop, from
	// whichever goroutine dispatches the command.
	reqCounter atomic.Int64

	sendMu sync.Mutex
	// whether the extension understands CompleteCancel messages; older
	// SDKs terminate the stream when they receive an unknown message type.
	supportsCompleteCancel bool
	// whether the extension understands HandleCancel/HelpCancel messages
	// and echoes request ids back. Same caveat as supportsCompleteCancel.
	supportsHandleCancel bool
}

// subset of Editor_SubscribeREPLCommandServer
type replServerStream interface {
	RecvMsg(any) error
	Send(*textrpc.ServerREPLCommandMessage) error
}

type responsiveCtx struct {
	id     int64
	ctx    context.Context
	cancel func()
	ch     chan responsiveValue
	pw     repl.ProgressWriter
}

type responsiveValue struct {
	val component.Responsive
	err error
}

func newREPLCommandClientStream(
	ctx context.Context, stream replServerStream,
	supportsCompleteCancel, supportsHandleCancel bool,
) *replCommandClientStream {
	ctx, cancelCtx := context.WithCancel(ctx)
	return &replCommandClientStream{
		log:                    slog.Default().With("struct", "textrpc.replCommandClientStream"),
		ctx:                    ctx,
		cancelCtx:              cancelCtx,
		stream:                 stream,
		supportsCompleteCancel: supportsCompleteCancel,
		supportsHandleCancel:   supportsHandleCancel,
	}
}

func (c *replCommandClientStream) send(msg *textrpc.ServerREPLCommandMessage) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return c.stream.Send(msg)
}

func (c *replCommandClientStream) receiveMessages() error {
	defer c.cancelCtx()
	for {
		var msg textrpc.ClientREPLCommandMessage
		err := c.stream.RecvMsg(&msg)
		if err != nil {
			return fmt.Errorf("receive repl stream message: %w", err)
		}

		switch msg.GetType() {
		case textrpc.ClientREPLCommandMessage_HandleValue:
			val := msg.GetHandleValue()
			c.sendResponsiveValue(c.activeHandle(val.GetId()), responsiveFromProtoRows(
				val.GetRows(),
			), "handle")
		case textrpc.ClientREPLCommandMessage_HandleProgress:
			if prw := msg.GetHandleProgress(); prw != nil {
				c.forwardProgress(prw)
			}
		case textrpc.ClientREPLCommandMessage_HandleDone:
			done := msg.GetHandleDone()
			c.finishResponsive(c.takeHandle(done.GetId()), done.GetError())
		case textrpc.ClientREPLCommandMessage_HelpValue:
			val := msg.GetHelpValue()
			c.sendResponsiveValue(c.activeHelp(val.GetId()), responsiveFromProtoRows(
				val.GetRows(),
			), "help")
		case textrpc.ClientREPLCommandMessage_HelpDone:
			done := msg.GetHelpDone()
			c.finishResponsive(c.takeHelp(done.GetId()), done.GetError())
		case textrpc.ClientREPLCommandMessage_CompleteValue:
			complete := msg.GetCompleteValue()
			id := complete.GetId()
			if id == 0 {
				c.log.Warn("received repl complete value message with invalid id")
				continue
			}
			val, ok := c.completers.Load(id)
			if !ok {
				c.log.Warn("received repl complete message for unknown stream")
				continue
			}
			chanCtx := val.(chanCtx)
			if chanCtx.cancelled {
				continue
			}
			chanValue := chanValue{val: complete.GetValue()}
			select {
			case chanCtx.ch <- chanValue:
			case <-chanCtx.ctx.Done():
				continue
			case <-c.ctx.Done():
				return c.ctx.Err()
			}
		case textrpc.ClientREPLCommandMessage_CompleteDone:
			done := msg.GetCompleteDone()
			id := done.GetId()
			if id == 0 {
				c.log.Warn("received repl complete done message with invalid id")
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

			chanValue := chanValue{err: errors.New(errStr)}
			select {
			case chanCtx.ch <- chanValue:
			case <-chanCtx.ctx.Done():
				continue
			case <-c.ctx.Done():
				return c.ctx.Err()
			}
		default:
			c.log.Warn("received extraneous repl message type", "type", msg.GetType())
		}
	}
}

func (c *replCommandClientStream) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	respCtx, err := c.installResponsiveRequest(true, ctx, pw)
	if err != nil {
		return nil, err
	}

	req := textrpc.HandleREPLCommandRequest{
		Name:  cmd.Name,
		Args:  cmd.Args,
		Width: defaultREPLResponsiveWidth,
		Id:    respCtx.id,
	}
	msg := textrpc.ServerREPLCommandMessage{
		Type:   textrpc.ServerREPLCommandMessage_Handle,
		Handle: &req,
	}
	if err := c.send(&msg); err != nil {
		c.clearResponsiveRequest(true, respCtx.ch)
		return nil, fmt.Errorf("send repl handle request: %w", err)
	}

	return c.responsiveIterator(respCtx, true), nil
}

func (c *replCommandClientStream) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	c.counter++ // start with 1, so 0 is a missing ID error
	id := c.counter

	var req textrpc.CompleteCommandRequest
	req.Id = id
	req.Name = cmd
	req.Args = args

	var reqMsg textrpc.ServerREPLCommandMessage
	reqMsg.Type = textrpc.ServerREPLCommandMessage_Complete
	reqMsg.Complete = &req

	if err := c.send(&reqMsg); err != nil {
		return nil, fmt.Errorf("send repl complete request: %w", err)
	}

	ctx, cancelCtx := context.WithCancel(ctx)
	ch := make(chan chanValue)
	c.completers.Store(id, chanCtx{ctx: ctx, ch: ch})

	return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
		select {
		case next, ok := <-ch:
			if !ok {
				return "", false, nil
			}
			if next.err != nil {
				close(ch)
				return "", false, next.err
			}
			return next.val, true, nil
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-c.ctx.Done():
			return "", false, c.ctx.Err()
		}
	}, func() error {
		cancelCtx()
		// keep a tombstone so values still in flight for this abandoned
		// completion are dropped silently; CompleteDone clears it.
		c.completers.Store(id, chanCtx{cancelled: true})
		if !c.supportsCompleteCancel {
			return nil
		}
		cancelMsg := textrpc.ServerREPLCommandMessage{
			Type:           textrpc.ServerREPLCommandMessage_CompleteCancel,
			CompleteCancel: &textrpc.CompleteCommandCancel{Id: id},
		}
		if err := c.send(&cancelMsg); err != nil {
			c.log.Warn("send repl complete cancel", "error", err)
		}
		return nil
	}), nil
}

func (c *replCommandClientStream) Help(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	respCtx, err := c.installResponsiveRequest(false, ctx, nil)
	if err != nil {
		return nil, err
	}

	req := textrpc.HelpCommandRequest{
		Args:  args,
		Width: defaultREPLResponsiveWidth,
		Id:    respCtx.id,
	}
	msg := textrpc.ServerREPLCommandMessage{
		Type: textrpc.ServerREPLCommandMessage_Help,
		Help: &req,
	}
	if err := c.send(&msg); err != nil {
		c.clearResponsiveRequest(false, respCtx.ch)
		return nil, fmt.Errorf("send repl help request: %w", err)
	}

	return c.responsiveIterator(respCtx, false), nil
}

func (c *replCommandClientStream) installResponsiveRequest(
	isHandle bool, ctx context.Context, pw repl.ProgressWriter,
) (responsiveCtx, error) {
	ctx, cancel := context.WithCancel(ctx)
	respCtx := responsiveCtx{
		id:     c.reqCounter.Add(1), // start at 1, so 0 means "no id"
		ctx:    ctx,
		cancel: cancel,
		ch:     make(chan responsiveValue),
		pw:     pw,
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if isHandle {
		if c.handle != nil {
			return responsiveCtx{}, errors.New("repl command already in progress")
		}
		c.handle = &respCtx
		return respCtx, nil
	}
	if c.help != nil {
		return responsiveCtx{}, errors.New("repl help already in progress")
	}
	c.help = &respCtx
	return respCtx, nil
}

// clearResponsiveRequest drops the request still reading from ch, if it
// is the installed one, and reports whether it did.
func (c *replCommandClientStream) clearResponsiveRequest(
	isHandle bool, ch chan responsiveValue,
) bool {
	var cancel func()
	c.mu.Lock()
	slot := &c.help
	if isHandle {
		slot = &c.handle
	}
	if *slot != nil && (*slot).ch == ch {
		cancel = (*slot).cancel
		*slot = nil
	}
	c.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// activeHandle returns the in-flight command request when id names it,
// and nil for a message belonging to an abandoned request. An id of zero
// comes from an extension that predates request ids; such an extension is
// never sent a cancel, so its replies can only belong to the live one.
func (c *replCommandClientStream) activeHandle(id int64) *responsiveCtx {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handle == nil || (id != 0 && c.handle.id != id) {
		return nil
	}
	return c.handle
}

func (c *replCommandClientStream) forwardProgress(
	p *textrpc.HandleREPLCommandProgress,
) {
	respCtx := c.activeHandle(p.GetId())
	if respCtx == nil || respCtx.pw == nil {
		return
	}
	respCtx.pw.Progress(p.GetProgress(), p.GetTotal(), p.GetUnits())
}

func (c *replCommandClientStream) takeHandle(id int64) *responsiveCtx {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.handle == nil || (id != 0 && c.handle.id != id) {
		return nil
	}
	ret := c.handle
	c.handle = nil
	return ret
}

func (c *replCommandClientStream) activeHelp(id int64) *responsiveCtx {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.help == nil || (id != 0 && c.help.id != id) {
		return nil
	}
	return c.help
}

func (c *replCommandClientStream) takeHelp(id int64) *responsiveCtx {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.help == nil || (id != 0 && c.help.id != id) {
		return nil
	}
	ret := c.help
	c.help = nil
	return ret
}

func (c *replCommandClientStream) sendResponsiveValue(
	respCtx *responsiveCtx, val component.Responsive, kind string,
) {
	if respCtx == nil {
		c.log.Warn("received repl responsive message for inactive stream", "kind", kind)
		return
	}
	resp := responsiveValue{val: val}
	select {
	case respCtx.ch <- resp:
	case <-respCtx.ctx.Done():
	case <-c.ctx.Done():
	}
}

func (c *replCommandClientStream) finishResponsive(respCtx *responsiveCtx, errStr string) {
	if respCtx == nil {
		return
	}
	if errStr == "" {
		close(respCtx.ch)
		return
	}
	resp := responsiveValue{err: errors.New(errStr)}
	select {
	case respCtx.ch <- resp:
	case <-respCtx.ctx.Done():
	case <-c.ctx.Done():
	}
}

func (c *replCommandClientStream) responsiveIterator(
	respCtx responsiveCtx, isHandle bool,
) iterator.Iterator[component.Responsive] {
	return iterator.FromFunc(func(ctx context.Context) (component.Responsive, bool, error) {
		select {
		case next, ok := <-respCtx.ch:
			if !ok {
				return nil, false, nil
			}
			if next.err != nil {
				close(respCtx.ch)
				return nil, false, next.err
			}
			return next.val, true, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-c.ctx.Done():
			return nil, false, c.ctx.Err()
		}
	}, func() error {
		if c.clearResponsiveRequest(isHandle, respCtx.ch) {
			c.sendRequestCancel(isHandle, respCtx.id)
		}
		return nil
	})
}

func (c *replCommandClientStream) sendRequestCancel(isHandle bool, id int64) {
	if !c.supportsHandleCancel {
		return
	}
	var msg textrpc.ServerREPLCommandMessage
	if isHandle {
		msg.Type = textrpc.ServerREPLCommandMessage_HandleCancel
		msg.HandleCancel = &textrpc.RequestCancel{Id: id}
	} else {
		msg.Type = textrpc.ServerREPLCommandMessage_HelpCancel
		msg.HelpCancel = &textrpc.RequestCancel{Id: id}
	}
	if err := c.send(&msg); err != nil {
		c.log.Warn("send repl request cancel", "error", err, "handle", isHandle)
	}
}

func responsiveFromProtoRows(rows []*termrpc.CellRow) component.Responsive {
	if len(rows) == 0 {
		return component.NopResponsive()
	}
	cells := make([][]term.Cell, len(rows))
	for y, row := range rows {
		cells[y] = make([]term.Cell, len(row.Cells))
		for x, c := range row.Cells {
			cells[y][x] = c.ToModel()
		}
	}
	return &protoRowsResponsive{cells: cells}
}

type protoRowsResponsive struct {
	cells [][]term.Cell
	width int
}

var _ component.Responsive = (*protoRowsResponsive)(nil)

func (r *protoRowsResponsive) Height(int) int { return len(r.cells) }

func (r *protoRowsResponsive) Resize(width, _ int) { r.width = width }

func (r *protoRowsResponsive) Draw(w term.Writer) {
	for y, row := range r.cells {
		var offset int
		for x, c := range row {
			xi := x + offset
			if c.Width > 1 {
				offset += int(c.Width) - 1
			}
			if xi >= r.width {
				break
			}
			if c.Ch == 0 {
				continue
			}
			w.SetCell(term.Coordinates{X: xi, Y: y}, c)
		}
	}
}
