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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/pty"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// fdFile is a workspaceapi.File that is not a syscall.Conn but reports
// an arbitrary descriptor number, the way a scheme-allocated file
// whose number collides with a host descriptor would.
type fdFile struct {
	workspaceapi.File
	fd uintptr
}

func (f *fdFile) Fd() uintptr { return f.fd }

// fileSchemePty returns a pty from the file scheme, the one a local
// terminal runs on, so that the fast paths are checked against the
// master the scheme hands out rather than a bare *os.File.
func fileSchemePty(t *testing.T) workspaceapi.Pty {
	t.Helper()
	uri, err := workspaceapi.CurrentUserHostURI(t.TempDir())
	require.NoError(t, err)
	s, err := workspace.NewFileScheme(context.Background(), config.NopConfig(), uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	p, err := s.NewPty(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = p.Master.Close()
		_ = p.Slave.Close()
	})
	return p
}

func TestGatherBatchSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		start    int
		free     int
		expected int
	}{
		{name: "parser caught up grows payload", start: 64 << 10, free: 3, expected: 72 << 10},
		{name: "one free buffer is balanced", start: 8 << 10, free: 1, expected: 8 << 10},
		{name: "two free buffers are balanced", start: 8 << 10, free: 2, expected: 8 << 10},
		{name: "parser behind shrinks payload", start: 128 << 10, free: 0, expected: (128 << 10) * 8 / 9},
		{name: "growth is capped", start: gatherMaxBatchSize, free: 3, expected: gatherMaxBatchSize},
		{name: "shrink is bounded", start: gatherMinBatchSize, free: 0, expected: gatherMinBatchSize},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			g := ptyGather{batchSize: tc.start}
			g.adjustBatchSize(tc.free)
			assert.Equal(t, tc.expected, g.batchSize)
		})
	}
}

func TestGatherFillPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		target       int
		wait         time.Duration
		expected     int
		expectedWait time.Duration
		expectedIdle bool
	}{
		{
			name:     "writer refill gap preserves target",
			target:   gatherMaxBatchSize,
			wait:     gatherIdleThreshold - time.Nanosecond,
			expected: gatherMaxBatchSize,
		},
		{
			name:         "idle transition resets target and restores budget",
			target:       gatherMaxBatchSize,
			wait:         gatherIdleThreshold,
			expected:     gatherMinBatchSize,
			expectedWait: gatherBudget,
			expectedIdle: true,
		},
		{
			name:         "floor keeps budget across refill gap",
			target:       gatherMinBatchSize,
			wait:         gatherIdleThreshold - time.Nanosecond,
			expected:     gatherMinBatchSize,
			expectedWait: gatherBudget,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			target, budget, idle := gatherFillPolicy(tc.target, tc.wait)
			assert.Equal(t, tc.expected, target)
			assert.Equal(t, tc.expectedWait, budget)
			assert.Equal(t, tc.expectedIdle, idle)
		})
	}
}

func TestPtyGather(t *testing.T) {
	t.Parallel()

	t.Run("rejects non-local pty files", func(t *testing.T) {
		t.Parallel()
		_, ok := newPtyGather(t.Context(), &workspacetest.File{})
		assert.False(t, ok)
	})

	t.Run("rejects a non-local file whose descriptor number names a tty", func(t *testing.T) {
		t.Parallel()
		master, tty, err := pty.Open()
		require.NoError(t, err)
		defer master.Close()
		defer tty.Close()
		_, ok := newPtyGather(t.Context(), &fdFile{File: &workspacetest.File{}, fd: master.Fd()})
		assert.False(t, ok)
	})

	t.Run("accepts a file scheme pty master", func(t *testing.T) {
		t.Parallel()
		_, ok := newPtyGather(t.Context(), fileSchemePty(t).Master)
		assert.True(t, ok)
	})

	t.Run("preserves content across batches with a slow consumer", func(t *testing.T) {
		t.Parallel()
		master, tty, err := pty.Open()
		require.NoError(t, err)
		defer master.Close()

		g, ok := newPtyGather(t.Context(), master)
		require.True(t, ok)

		// Alphanumeric payload only: the tty output discipline rewrites
		// control bytes (e.g. ONLCR turns \n into \r\n).
		payload := make([]byte, 4<<20)
		for i := range payload {
			payload[i] = 'a' + byte(i%26)
		}
		drained := make(chan struct{})
		defer close(drained)
		go debug.CapturePanicReport(func() {
			defer tty.Close()
			for chunk := payload; len(chunk) > 0; {
				n := min(len(chunk), 8192)
				if _, err := tty.Write(chunk[:n]); err != nil {
					return
				}
				chunk = chunk[n:]
			}
			<-drained
		})

		var got bytes.Buffer
		for got.Len() < len(payload) {
			batch, ok := <-g.ready
			require.True(t, ok, "gather ended after %d of %d bytes",
				got.Len(), len(payload))
			require.LessOrEqual(t, len(batch), gatherMaxBatchSize)
			require.NotEmpty(t, batch)
			got.Write(batch)
			g.release(batch)
			// A stalled parse stage must not corrupt or drop data:
			// flow control pushes back through the ring and the
			// kernel queue.
			if got.Len() > len(payload)/2 {
				time.Sleep(time.Millisecond)
			}
		}
		require.Equal(t, len(payload), got.Len())
		assert.Equal(t, payload, got.Bytes())
	})

	t.Run("delivers an interactive trickle without further input", func(t *testing.T) {
		t.Parallel()
		master, tty, err := pty.Open()
		require.NoError(t, err)
		defer master.Close()
		defer tty.Close()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		g, ok := newPtyGather(ctx, master)
		require.True(t, ok)

		_, err = tty.WriteString("hello")
		require.NoError(t, err)

		select {
		case batch := <-g.ready:
			assert.Equal(t, "hello", string(batch))
			g.release(batch)
		case <-time.After(5 * time.Second):
			t.Fatal("trickle was not delivered")
		}
	})

	t.Run("context cancellation ends the stream", func(t *testing.T) {
		t.Parallel()
		master, tty, err := pty.Open()
		require.NoError(t, err)
		defer master.Close()
		defer tty.Close()

		ctx, cancel := context.WithCancel(context.Background())
		g, ok := newPtyGather(ctx, master)
		require.True(t, ok)

		cancel()
		select {
		case batch, open := <-g.ready:
			require.False(t, open, "expected closed ready channel, got batch %q", batch)
		case <-time.After(5 * time.Second):
			t.Fatal("gather did not stop on cancellation")
		}
		assert.True(t, isNormalPtyExit(g.err), "cancellation must read as a normal exit: %v", g.err)
	})
}

// BenchmarkPtyGatherDrain measures the gather stage's sustained drain
// rate through a real pty, the bound that `cat large_file` sees.
func BenchmarkPtyGatherDrain(b *testing.B) {
	payload := make([]byte, 8<<20)
	for i := range payload {
		payload[i] = 'a' + byte(i%26)
	}

	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()

	for range b.N {
		master, tty, err := pty.Open()
		if err != nil {
			b.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		g, ok := newPtyGather(ctx, master)
		if !ok {
			b.Fatal("gather rejected local pty")
		}
		// Closing the tty discards any bytes the kernel has not yet
		// handed to the master, so the writer must stay open until the
		// reader has drained the whole payload. It waits on drained
		// before closing, and the reader stops on the byte count rather
		// than on a close-driven EOF that would race the final bytes.
		drained := make(chan struct{})
		go debug.CapturePanicReport(func() {
			defer tty.Close()
			for chunk := payload; len(chunk) > 0; {
				n := min(len(chunk), 65536)
				if _, err := tty.Write(chunk[:n]); err != nil {
					return
				}
				chunk = chunk[n:]
			}
			<-drained
		})
		var total int
		for total < len(payload) {
			batch, ok := <-g.ready
			if !ok {
				b.Fatalf("drained %d of %d bytes: %v", total, len(payload), g.err)
			}
			total += len(batch)
			g.release(batch)
		}
		close(drained)
		cancel()
		master.Close()
	}
}
