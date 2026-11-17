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

package vteregistry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtewire"
)

// dropGrace is how long a member dropped for falling behind is given
// to read why before its connection is closed under it. A client that
// is reading at all drains a socket in far less; one that is not is
// not waited on.
const dropGrace = time.Second

// Resolver names the registry owning the terminals of the workspace
// rooted at root, as the HELLO of an attachment names it.
type Resolver func(ctx context.Context, root string) (*Registry, error)

// Serve accepts attachment connections on lis until it is closed,
// serving each under ctx by the registry resolve names for the root its
// HELLO carries. ctx does not unblock Accept: a caller stops Serve by
// ending ctx and then closing lis, which returns nil. Any other accept
// failure is returned.
//
// Attachments are a raw byte stream between a client's emulator and a
// pty, so they get a listener of their own rather than sharing the
// request/response surface, where terminal lifecycle stays.
func Serve(ctx context.Context, lis net.Listener, resolve Resolver) error {
	for {
		conn, err := lis.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go debug.CapturePanicReport(func() {
			if err := Accept(ctx, conn, resolve); err != nil &&
				!errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				log.WithField(logging.KeyClass, "vteregistry").
					Warnf("serve terminal attachment: %v", err)
			}
		})
	}
}

// Accept serves one attachment over rw, which is how a host that
// already has a byte stream to a client hands it to the terminals the
// registry resolve names owns. It returns when the attachment ends,
// and closes rw.
func Accept(ctx context.Context, rw io.ReadWriteCloser, resolve Resolver) error {
	conn := vtewire.NewConn(rw)
	defer func() { _ = conn.Close() }()

	hello, err := vtewire.Accept(conn)
	if err != nil {
		return err
	}
	r, err := resolve(ctx, hello.Root)
	if err != nil {
		_ = conn.WriteFrame(&vtewire.HelloErr{
			Code: vtewire.CodeUnknown, Message: err.Error(),
		})
		return err
	}
	return r.attach(ctx, conn, hello)
}

// Serve accepts attachment connections on lis until it is closed and
// serves each from this registry, whatever root its HELLO carries. See
// [Serve].
func (r *Registry) Serve(ctx context.Context, lis net.Listener) error {
	return Serve(ctx, lis, r.resolve)
}

// Accept serves one attachment to this registry's terminals over rw.
// See [Accept].
func (r *Registry) Accept(ctx context.Context, rw io.ReadWriteCloser) error {
	return Accept(ctx, rw, r.resolve)
}

func (r *Registry) resolve(context.Context, string) (*Registry, error) {
	return r, nil
}

// attach serves the attachment hello asks for over conn: it creates the
// terminal when hello names none, and otherwise attaches to or previews
// the one it names.
func (r *Registry) attach(
	ctx context.Context, conn *vtewire.Conn, hello *vtewire.Hello,
) error {
	defer r.serving()()
	var (
		a   *attachment
		err error
	)
	switch {
	case hello.VTEID == "":
		opts := vte.CreateOptions{CommandAndArgs: hello.Create.CommandAndArgs}
		if hello.Width > 0 && hello.Height > 0 {
			opts.Width, opts.Height = clampDimensions(hello.Width, hello.Height)
		}
		a, err = r.createVTE(ctx, opts)
	case hello.Preview:
		a, err = r.previewVTE(vte.ID(hello.VTEID))
	default:
		a, err = r.attachVTE(vte.ID(hello.VTEID))
	}
	if err != nil {
		code := vtewire.CodeUnknown
		if errors.Is(err, vte.ErrNotFound) {
			code = vtewire.CodeNotFound
		}
		_ = conn.WriteFrame(&vtewire.HelloErr{Code: code, Message: err.Error()})
		return err
	}
	defer func() { _ = a.Close() }()
	return newSession(conn, a).run(hello)
}

// session serves one wire member: it hands over a snapshot, tees live
// output, and applies the member's input, resizes and focus to the
// attachment it holds like any in-process view's.
type session struct {
	conn   *vtewire.Conn
	a      *attachment
	member *member
	// ended is set once this side ends the attachment: the member
	// fell behind, its socket failed, or its terminal ended.
	ended atomic.Bool
}

func newSession(conn *vtewire.Conn, a *attachment) *session {
	s := &session{conn: conn, a: a}
	s.member = &member{
		sender: vtewire.NewSender(conn, 0),
		// A member that fell behind is told so, but the close is not
		// left to it: a member that stopped reading would otherwise
		// keep its attachment, and the pty's size, forever.
		onDrop: func() {
			s.ended.Store(true)
			time.AfterFunc(dropGrace, func() { _ = conn.Close() })
		},
	}
	return s
}

func (s *session) run(hello *vtewire.Hello) error {
	c := s.a.c
	comp := c.h.Component()

	sendDone := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(sendDone)
		err := s.member.sender.Run()
		// A dropped or broken attachment must not leave the read loop
		// waiting on a socket nobody is answering, and neither must a
		// member whose terminal ended under it.
		if err != nil {
			s.member.drop()
		}
		if errors.Is(err, vtewire.ErrSlowClient) {
			// Told why, the member attaches again at once rather than
			// once it finds its connection dead.
			_ = s.conn.WriteFrame(&vtewire.Dropped{
				Code:    vtewire.CodeSlow,
				Message: "attachment fell behind the terminal's output",
			})
			log.WithField(logging.KeyClass, "vteregistry").
				Infof("terminal %s: dropped an attachment that fell behind", c.id)
		}
		s.ended.Store(true)
		_ = s.conn.Close()
	})
	defer func() {
		c.hub.remove(s.member)
		s.member.sender.Close()
		_ = s.conn.Close()
		<-sendDone
	}()

	// The member's size only takes effect if it owns the pty's size,
	// so the reply carries whatever size is actually in force.
	size, ok := s.a.resize(vtewire.Resize{})
	if hello.Width > 0 && hello.Height > 0 {
		w, h := clampDimensions(hello.Width, hello.Height)
		size, ok = s.a.resize(vtewire.Resize{
			Width:           w,
			Height:          h,
			CellPixelWidth:  hello.CellPixelWidth,
			CellPixelHeight: hello.CellPixelHeight,
		})
	}
	if !ok {
		err := vte.ErrNotFound
		_ = s.conn.WriteFrame(&vtewire.HelloErr{
			Code: vtewire.CodeNotFound, Message: err.Error(),
		})
		return err
	}

	var history [][]term.Cell
	err := comp.SnapshotStream(
		func(snapshot []byte, scrollback [][]term.Cell) error {
			// Queued inside the cut so the first OUTPUT the member
			// receives is the byte immediately after the snapshot.
			s.member.send(&vtewire.HelloOK{
				VTEID:    string(c.id),
				URI:      c.uri.String(),
				Width:    size.Width,
				Height:   size.Height,
				Complete: comp.IsComplete(),
			})
			s.member.send(&vtewire.Snapshot{Records: snapshot})
			history = scrollback
			c.hub.add(s.member)
			return nil
		})
	if err != nil {
		_ = s.conn.WriteFrame(&vtewire.HelloErr{
			Code:    vtewire.CodeUnknown,
			Message: err.Error(),
		})
		return fmt.Errorf("snapshot terminal: %w", err)
	}

	// Scrollback is queued after the member is already live, so a deep
	// history never delays the screen it belongs to.
	s.member.sendHistory(history)

	exited := make(chan struct{})
	defer close(exited)
	go debug.CapturePanicReport(func() {
		select {
		case <-comp.Exited():
			exit := &vtewire.Exit{}
			if err := comp.ExitErr(); err != nil {
				exit.Err = err.Error()
			}
			s.member.send(exit)
		case <-exited:
		}
	})

	err = s.readLoop(comp)
	if s.ended.Load() {
		// The read failing is this side ending the attachment, not a
		// fault of the member's.
		return nil
	}
	return err
}

func (s *session) readLoop(comp *vte.Component) error {
	for {
		frame, err := s.conn.ReadFrame()
		if err != nil {
			return err
		}
		switch f := frame.(type) {
		case *vtewire.Input:
			// The frame's bytes belong to the read buffer, and the pty
			// write is asynchronous, so this must own its copy.
			if err := comp.WriteToPty(append([]byte(nil), f.Data...)); err != nil {
				return fmt.Errorf("write to pty: %w", err)
			}
		case *vtewire.Resize:
			w, h := clampDimensions(f.Width, f.Height)
			size, ok := s.a.resize(vtewire.Resize{
				Width:           w,
				Height:          h,
				CellPixelWidth:  f.CellPixelWidth,
				CellPixelHeight: f.CellPixelHeight,
			})
			if ok && !grants(size, f) {
				// The request lost to another view's ownership; tell
				// this member what the size really is, which is also
				// how a clamped request is reported back.
				s.member.send(&size)
			}
		case *vtewire.Focus:
			s.a.OnFocusChange(f.Focused)
		case *vtewire.Detach:
			return nil
		default:
			return fmt.Errorf("%w: unexpected %s from client",
				vtewire.ErrMalformed, frame.Type())
		}
	}
}

// grants reports whether the size in force is what req asked for.
func grants(size vtewire.Resize, req *vtewire.Resize) bool {
	if size.Width != req.Width || size.Height != req.Height {
		return false
	}
	if cellSizeOf(req.CellPixelWidth, req.CellPixelHeight) == (cellSize{}) {
		return true
	}
	return size.CellPixelWidth == req.CellPixelWidth &&
		size.CellPixelHeight == req.CellPixelHeight
}
