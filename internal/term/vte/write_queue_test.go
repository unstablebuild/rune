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
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// ptyQueueOverflow is a message the kernel cannot absorb in one write
// on any platform, so that the stage is parked in poll on it: the pty
// input queue measured 1022 bytes on macOS and 11776 on Linux.
const ptyQueueOverflow = 64 << 10

// newNonPollableMaster opens a pty whose master is built the way the
// macOS pty fork builds it: os.NewFile on a blocking descriptor, which
// Go never registers with the runtime poller. The gather stage on the
// original master then flips the shared open file description to
// O_NONBLOCK exactly as production does, so Write surfaces EAGAIN on
// every platform instead of parking in the poller. The slave is left
// in raw mode so the kernel does not rewrite the payload.
func newNonPollableMaster(t *testing.T) (master, slave *os.File) {
	t.Helper()
	orig, slave, err := pty.Open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = orig.Close() })

	_, err = term.MakeRaw(int(slave.Fd()))
	require.NoError(t, err)

	dup, err := unix.Dup(int(orig.Fd()))
	require.NoError(t, err)
	master = os.NewFile(uintptr(dup), orig.Name())
	require.NotNil(t, master)

	g, ok := newPtyGather(t.Context(), orig)
	require.True(t, ok)
	go debug.CapturePanicReport(func() {
		for batch := range g.ready {
			g.release(batch)
		}
	})
	return master, slave
}

func alphaPayload(n int) []byte {
	payload := make([]byte, n)
	for i := range payload {
		payload[i] = 'a' + byte(i%26)
	}
	return payload
}

func readExactly(t *testing.T, r *os.File, n, chunk int, pause time.Duration) []byte {
	t.Helper()
	var got bytes.Buffer
	buf := make([]byte, chunk)
	for got.Len() < n {
		r, err := r.Read(buf[:min(chunk, n-got.Len())])
		require.NoError(t, err, "read after %d of %d bytes", got.Len(), n)
		got.Write(buf[:r])
		if pause > 0 {
			time.Sleep(pause)
		}
	}
	return got.Bytes()
}

func TestPtyWriteQueueSkipsNonLocalMaster(t *testing.T) {
	t.Parallel()
	_, ok := newPtyWriteQueue(t.Context(), &workspacetest.File{})
	assert.False(t, ok)
}

func TestPtyWriteQueueDeliversLargeWriteToSlowReader(t *testing.T) {
	t.Parallel()
	master, slave := newNonPollableMaster(t)
	defer slave.Close()

	q, ok := newPtyWriteQueue(t.Context(), master)
	require.True(t, ok)

	payload := alphaPayload(ptyQueueOverflow)
	n, err := q.Write(payload)
	require.NoError(t, err)
	require.Equal(t, len(payload), n)

	got := readExactly(t, slave, len(payload), 512, 100*time.Microsecond)
	assert.Equal(t, payload, got)
}

func TestPtyWriteQueueKeepsMessagesWhole(t *testing.T) {
	t.Parallel()
	master, slave := newNonPollableMaster(t)
	defer slave.Close()

	q, ok := newPtyWriteQueue(t.Context(), master)
	require.True(t, ok)

	const large = ptyQueueOverflow
	const replies = 32
	reply := []byte("\x1b[1;1R")
	payload := alphaPayload(large)

	writers := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(writers)
		for range replies {
			_, _ = q.Write(reply)
			time.Sleep(50 * time.Microsecond)
		}
	})
	_, err := q.Write(payload)
	require.NoError(t, err)
	<-writers

	got := readExactly(t, slave, large+replies*len(reply), 256, 0)
	// Strip the replies wherever they landed; what remains must be the
	// large message intact, which proves no reply was spliced into it.
	assert.Equal(t, replies, bytes.Count(got, reply))
	assert.Equal(t, payload, bytes.ReplaceAll(got, reply, nil))
}

func TestPtyWriteQueueEnqueueDoesNotBlockOnStuckReader(t *testing.T) {
	t.Parallel()
	master, slave := newNonPollableMaster(t)
	defer slave.Close()

	q, ok := newPtyWriteQueue(t.Context(), master)
	require.True(t, ok)

	// Each message overflows the kernel queue on its own, so the stage
	// is stuck in poll on the first one while the rest are enqueued.
	msg := alphaPayload(ptyQueueOverflow)
	const count = 8
	enqueued := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(enqueued)
		for range count {
			_, _ = q.Write(msg)
		}
	})
	select {
	case <-enqueued:
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue blocked on a stuck reader")
	}

	got := readExactly(t, slave, count*len(msg), 1024, 0)
	assert.Equal(t, bytes.Repeat(msg, count), got)
}

func TestPtyWriteQueueBlocksWhenFullUntilCancel(t *testing.T) {
	t.Parallel()
	master, slave := newNonPollableMaster(t)
	defer slave.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q, ok := newPtyWriteQueue(ctx, master)
	require.True(t, ok)

	msg := alphaPayload(ptyQueueOverflow)
	// One message stalls the stage; the channel then takes depth more.
	for range ptyWriteQueueDepth + 1 {
		_, err := q.Write(msg)
		require.NoError(t, err)
	}

	result := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		_, err := q.Write(msg)
		result <- err
	})
	select {
	case err := <-result:
		t.Fatalf("write returned %v while the queue was full", err)
	case <-time.After(100 * time.Millisecond):
	}

	cancel()
	select {
	case err := <-result:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not unblock the writer")
	}
	select {
	case <-q.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stage did not stop on cancellation")
	}
}

func TestPtyWriteQueueDiesOnHangup(t *testing.T) {
	t.Parallel()
	master, slave := newNonPollableMaster(t)

	q, ok := newPtyWriteQueue(t.Context(), master)
	require.True(t, ok)

	// Fill the kernel queue so the stage is parked in poll when the
	// slave goes away, which is where a hangup must be noticed.
	_, err := q.Write(alphaPayload(ptyQueueOverflow))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, slave.Close())

	select {
	case <-q.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stage did not stop on hangup")
	}
	require.Error(t, q.err)
	assert.ErrorIs(t, q.err, errPtyHangup)

	_, err = q.Write([]byte("x"))
	assert.Equal(t, q.err, err)
}
