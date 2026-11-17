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

package vtewire

import (
	"errors"
	"sync"
)

// DefaultQueueBytes bounds one attachment's unsent backlog. It is
// several screens' worth of output: enough to ride out a client that
// stalls briefly, small enough that a client which stops reading is
// noticed rather than accumulated.
const DefaultQueueBytes = 4 << 20

// ErrSlowClient means an attachment fell far enough behind that its
// backlog exceeded the queue bound.
//
// The attachment is dropped rather than throttled: blocking would stall
// the emulator for every other client, and skipping frames would leave
// this client's replica permanently out of sync. A dropped client
// reattaches and resyncs wholesale from a fresh snapshot.
var ErrSlowClient = errors.New("vtewire: client too slow, attachment dropped")

// ErrSenderClosed means Send was called after the sender stopped.
var ErrSenderClosed = errors.New("vtewire: sender closed")

// Sender serializes frames onto a connection through a bounded queue.
//
// Send encodes on the caller's goroutine and only queues the bytes, so
// frames leave in exactly the order they were submitted even though a
// separate goroutine performs the writes. That is what lets the server
// hand a client its output, resizes and exit in the same order its own
// emulator saw them, while never blocking the emulator on a socket.
type Sender struct {
	conn     *Conn
	maxBytes int

	mu       sync.Mutex
	wake     sync.Cond
	pending  [][]byte
	queued   int
	closed   bool
	overflow bool
	// allowance is the extra budget granted to let a single frame
	// larger than the bound drain. Without it the frames that follow
	// an oversized snapshot would overflow against a queue the client
	// never had a chance to read.
	allowance int
}

// NewSender returns a sender queuing at most maxBytes of unsent
// frames. Zero uses DefaultQueueBytes.
func NewSender(conn *Conn, maxBytes int) *Sender {
	if maxBytes <= 0 {
		maxBytes = DefaultQueueBytes
	}
	s := &Sender{conn: conn, maxBytes: maxBytes}
	s.wake.L = &s.mu
	return s
}

// Send queues one frame. It never blocks.
//
// It returns ErrSlowClient once the backlog exceeds the bound, after
// which every later Send fails and Run returns the same error.
func (s *Sender) Send(f Frame) error {
	encoded := EncodeFrame(f)

	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case s.overflow:
		return ErrSlowClient
	case s.closed:
		return ErrSenderClosed
	// A frame larger than the whole bound is still admitted onto an
	// empty queue: a big snapshot must not look like a slow client.
	case s.queued > 0 && s.queued+len(encoded) > s.maxBytes+s.allowance:
		s.overflow = true
		s.wake.Signal()
		return ErrSlowClient
	}

	// The exemption covers the frames queued behind the oversized one
	// too, until it has actually been written: dropping them would
	// send the client back for a fresh snapshot just as large.
	if s.queued == 0 && len(encoded) > s.maxBytes {
		s.allowance = len(encoded)
	}
	s.pending = append(s.pending, encoded)
	s.queued += len(encoded)
	s.wake.Signal()
	return nil
}

// Run writes queued frames until the sender is closed, the connection
// fails, or the client falls too far behind. It must be called from
// exactly one goroutine.
func (s *Sender) Run() error {
	for {
		s.mu.Lock()
		for len(s.pending) == 0 && !s.closed && !s.overflow {
			s.wake.Wait()
		}
		if s.overflow {
			// Writing a backlog the client is not reading would block
			// here forever, so the drop is immediate.
			s.pending = nil
			s.queued = 0
			s.mu.Unlock()
			return ErrSlowClient
		}
		batch := s.pending
		s.pending = nil
		closed := s.closed
		s.mu.Unlock()

		written := 0
		for _, frame := range batch {
			written += len(frame)
		}
		if len(batch) > 0 {
			if err := s.conn.writeRaw(batch); err != nil {
				return err
			}
		}
		// The bound covers bytes still in flight, not just queued
		// ones, so a client that stops reading is noticed even while
		// one batch is stuck in the socket.
		s.mu.Lock()
		s.queued -= written
		if s.queued <= 0 {
			s.allowance = 0
		}
		s.mu.Unlock()

		if closed {
			return nil
		}
	}
}

// Close stops Run once the frames already queued have been written.
func (s *Sender) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.wake.Signal()
}
