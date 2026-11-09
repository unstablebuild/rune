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

package vte

import (
	"context"
	"errors"
	"os"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/sys/unix"
	"unstable.build/rune/internal/debug"
)

const (
	// ptyWriteQueueDepth is how many messages callers may run ahead
	// of the stage before enqueueing blocks
	ptyWriteQueueDepth = 64

	// ptyWritePollTimeoutMs bounds one wait for the program to drain
	// its input so that cancellation is noticed while it never reads.
	ptyWritePollTimeoutMs = 100
)

// errPtyHangup reports that the program closed its side of the pty
// while input was pending, so what remains will never be read.
var errPtyHangup = errors.New("pty hung up")

type ptyWriteQueue struct {
	ctx    context.Context
	master workspaceapi.File
	fd     int
	ch     chan []byte
	// done is closed when the stage exits; err is set before then and
	// must only be read after done is closed.
	done chan struct{}
	err  error
}

func newPtyWriteQueue(ctx context.Context, master workspaceapi.File) (*ptyWriteQueue, bool) {
	fd := int(master.Fd())
	if fd <= 2 {
		return nil, false
	}
	if _, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ); err != nil {
		return nil, false
	}
	q := &ptyWriteQueue{
		ctx:    ctx,
		master: master,
		fd:     fd,
		ch:     make(chan []byte, ptyWriteQueueDepth),
		done:   make(chan struct{}),
	}
	go debug.CapturePanicReport(q.run)
	return q, true
}

func (q *ptyWriteQueue) Write(p []byte) (int, error) {
	select {
	case <-q.done:
		return 0, q.err
	default:
	}
	// Callers reuse their buffers as soon as Write returns.
	msg := make([]byte, len(p))
	copy(msg, p)
	select {
	case q.ch <- msg:
		return len(p), nil
	case <-q.done:
		return 0, q.err
	case <-q.ctx.Done():
		return 0, q.ctx.Err()
	}
}

func (q *ptyWriteQueue) run() {
	defer close(q.done)
	for {
		var msg []byte
		select {
		case msg = <-q.ch:
		case <-q.ctx.Done():
			q.err = q.ctx.Err()
			return
		}
		if err := q.writeAll(msg); err != nil {
			q.err = err
			// A hangup is the program exiting, which the read side
			// reports; only a failure of the write itself is an error.
			level := log.ErrorLevel
			if q.ctx.Err() != nil || errors.Is(err, errPtyHangup) {
				level = log.DebugLevel
			}
			log.WithField(logging.KeyClass, "vte.ptyWriteQueue").
				Logf(level, "write to pty: %v", err)
			return
		}
	}
}

func (q *ptyWriteQueue) writeAll(p []byte) error {
	for len(p) > 0 {
		n, err := q.master.Write(p)
		p = p[n:]
		if err == nil {
			continue
		}
		if !errors.Is(err, unix.EAGAIN) {
			return err
		}
		if err := q.waitWritable(); err != nil {
			return err
		}
	}
	return nil
}

func (q *ptyWriteQueue) waitWritable() error {
	fds := [1]unix.PollFd{{Fd: int32(q.fd), Events: unix.POLLOUT}}
	for {
		if err := q.ctx.Err(); err != nil {
			return err
		}
		_, err := unix.Poll(fds[:], ptyWritePollTimeoutMs)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return &os.SyscallError{Syscall: "poll", Err: err}
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return errPtyHangup
		}
		if fds[0].Revents&unix.POLLOUT != 0 {
			return nil
		}
	}
}
