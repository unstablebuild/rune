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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte/vtesnap"
)

// TestTeeStreamJoinsMidStream is the load-bearing property of the
// attach path: a replica that joins while output is flowing converges
// on the original. Every byte must land in exactly one of the snapshot
// or the tee, so a cut that is off by even one byte diverges here.
func TestTeeStreamJoinsMidStream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc  string
		chunk func(i int) string
	}{
		{
			desc:  "plain lines",
			chunk: func(i int) string { return fmt.Sprintf("line %d\r\n", i) },
		},
		{
			desc: "styled output",
			chunk: func(i int) string {
				return fmt.Sprintf("\x1b[%dm styled %d \x1b[0m\r\n", 31+i%7, i)
			},
		},
		{
			desc: "cursor addressing",
			chunk: func(i int) string {
				return fmt.Sprintf("\x1b[%d;%dHcell%d", 1+i%10, 1+i%20, i)
			},
		},
		{
			desc: "synchronized updates",
			chunk: func(i int) string {
				return fmt.Sprintf("\x1b[?2026h\x1b[H frame %d \x1b[?2026l", i)
			},
		},
		{
			desc: "wide and combining glyphs",
			chunk: func(i int) string {
				return fmt.Sprintf("한국어 e\u0301 %d\r\n", i)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			origin := newFeedComponent(t, 40, 12)
			replica := newFeedComponent(t, 40, 12)
			pump := newTeePump(t, replica)
			origin.comp.TeeStream(pump.forward)

			// Output before the cut only exists in the snapshot.
			for i := range 20 {
				origin.pty.feed(t, tc.chunk(i))
			}
			origin.settle(t)

			require.NoError(t, origin.comp.SnapshotStream(
				func(snapshot []byte, history [][]term.Cell) error {
					decoder, err := vtesnap.NewDecoder(bytes.NewReader(snapshot))
					if err != nil {
						return err
					}
					snap, err := decoder.Ready()
					if err != nil {
						return err
					}
					if err := replica.comp.ApplySnapshot(snap); err != nil {
						return err
					}
					replica.comp.PrependHistory(history)
					if len(snap.Continuation) > 0 {
						pump.forward(snap.Continuation)
					}
					pump.start()
					return nil
				}))

			// Output after the cut only reaches the replica through
			// the tee.
			for i := 20; i < 60; i++ {
				origin.pty.feed(t, tc.chunk(i))
			}
			origin.settle(t)

			pump.drain(t, replica)
			assertSameScreen(t, origin, replica)
		})
	}
}

// TestTeeStreamCutIsAtomic pins that a batch is never both snapshotted
// and teed. The cut runs while a writer floods the emulator, so any
// window between "state captured" and "tee installed" duplicates or
// drops output and shows up as a diverged screen.
func TestTeeStreamCutIsAtomic(t *testing.T) {
	t.Parallel()

	origin := newFeedComponent(t, 40, 12)
	replica := newFeedComponent(t, 40, 12)
	pump := newTeePump(t, replica)
	origin.comp.TeeStream(pump.forward)

	stop := make(chan struct{})
	var writer sync.WaitGroup
	writer.Add(1)
	go debug.CapturePanicReport(func() {
		defer writer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if !assert.NoError(t, origin.pty.put(fmt.Sprintf("busy %d\r\n", i))) {
				return
			}
		}
	})

	// Let the flood get going so the cut has to interrupt it.
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, origin.comp.SnapshotStream(
		func(snapshot []byte, history [][]term.Cell) error {
			decoder, err := vtesnap.NewDecoder(bytes.NewReader(snapshot))
			if err != nil {
				return err
			}
			snap, err := decoder.Ready()
			if err != nil {
				return err
			}
			if err := replica.comp.ApplySnapshot(snap); err != nil {
				return err
			}
			replica.comp.PrependHistory(history)
			if len(snap.Continuation) > 0 {
				pump.forward(snap.Continuation)
			}
			pump.start()
			return nil
		}))

	time.Sleep(20 * time.Millisecond)
	close(stop)
	writer.Wait()

	origin.settle(t)
	pump.drain(t, replica)
	assertSameScreen(t, origin, replica)
}

// TestSnapshotStreamInsideSynchronizedUpdate pins that a replica
// joining while the program holds a synchronized update shows the
// state it joined with rather than an empty frame, and lets go of it
// like the emulator it replicates: at the program's ESU, or once the
// update times out.
func TestSnapshotStreamInsideSynchronizedUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		desc string
		// after is what the program writes once the replica joined.
		after string
	}{
		{desc: "program ends the update", after: "!\x1b[?2026l"},
		{desc: "update times out", after: "!"},
	}

	draw := func(c *Component) string {
		w := term.NewStringWriter(c.width, c.height)
		c.Draw(w)
		w.Flush()
		return w.String()
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			origin, _ := newSyncOutputComponent(t)
			replica, _ := newSyncOutputComponent(t)
			replica.mu.Lock()
			replica.syncTimeout = 10 * time.Millisecond
			replica.mu.Unlock()

			origin.feed([]byte("\x1b[?2026h\x1b[Hnew"))
			var teed []byte
			origin.TeeStream(func(b []byte) { teed = append(teed, b...) })
			require.NoError(t, origin.SnapshotStream(
				func(snapshot []byte, _ [][]term.Cell) error {
					decoder, err := vtesnap.NewDecoder(bytes.NewReader(snapshot))
					if err != nil {
						return err
					}
					snap, err := decoder.Ready()
					if err != nil {
						return err
					}
					return replica.ApplySnapshot(snap)
				}))
			joined := liveDraw(origin)
			require.Contains(t, joined, "new")
			assert.Equal(t, joined, draw(replica))

			origin.feed([]byte(tc.after))
			replica.feed(teed)
			want := liveDraw(origin)
			require.Contains(t, want, "new!")
			require.Eventually(t, func() bool { return draw(replica) == want },
				time.Second, time.Millisecond)
		})
	}
}

// TestContinuationRequiresTee pins that continuation tracking is tied
// to teeing: an emulator nobody observes pays nothing for it.
func TestContinuationRequiresTee(t *testing.T) {
	t.Parallel()

	c := newFeedComponent(t, 40, 12)
	c.feed(t, "text\x1b[31m")

	got, err := c.comp.Continuation()
	require.NoError(t, err)
	assert.Nil(t, got, "no tee means no continuation tracking")

	c.comp.TeeStream(func([]byte) {})
	c.feed(t, "more\x1b[3")
	got, err = c.comp.Continuation()
	require.NoError(t, err)
	assert.Equal(t, "\x1b[3", string(got))
}

// TestContinuationLostWhenTeeJoinsMidSequence pins that a tee
// installed partway through an escape sequence refuses to describe it
// rather than inventing a suffix from bytes whose start it never saw.
func TestContinuationLostWhenTeeJoinsMidSequence(t *testing.T) {
	t.Parallel()

	c := newFeedComponent(t, 40, 12)
	c.feed(t, "text\x1b[3")
	c.comp.TeeStream(func([]byte) {})

	_, err := c.comp.Continuation()
	assert.ErrorIs(t, err, ErrContinuationLost)
	assert.ErrorIs(t,
		c.comp.SnapshotStream(func([]byte, [][]term.Cell) error {
			return nil
		}),
		ErrContinuationLost)

	// The parser reaching a fresh start heals tracking.
	c.feed(t, "1m done")
	got, err := c.comp.Continuation()
	require.NoError(t, err)
	assert.Empty(t, got)
}

// teePump stands in for the server's per-attachment send queue: the
// tee runs on the parse goroutine and must never block, so bytes are
// handed to a pump that writes them to the replica.
//
// It also stands in for the server's fan-out: the sink is installed
// before the cut and only starts delivering once the attachment joins,
// because installing a sink from inside the cut would deadlock on the
// feed the cut is holding still.
type teePump struct {
	mu      sync.Mutex
	started bool
	pending [][]byte
	// queued and written count the batches forwarded and those put to
	// the replica, so drain can tell when none is in flight.
	queued, written int
	wake            chan struct{}
	done            chan struct{}
	stop            chan struct{}
}

func newTeePump(t *testing.T, replica *feedComponent) *teePump {
	t.Helper()

	p := &teePump{
		wake: make(chan struct{}, 1),
		done: make(chan struct{}),
		stop: make(chan struct{}),
	}
	go debug.CapturePanicReport(func() {
		defer close(p.done)
		for {
			select {
			case <-p.wake:
			case <-p.stop:
				p.flush(t, replica)
				return
			}
			p.flush(t, replica)
		}
	})
	t.Cleanup(func() {
		close(p.stop)
		<-p.done
	})
	return p
}

func (p *teePump) start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.started = true
}

func (p *teePump) forward(batch []byte) {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return
	}
	p.pending = append(p.pending, bytes.Clone(batch))
	p.queued++
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *teePump) flush(t *testing.T, replica *feedComponent) {
	for {
		p.mu.Lock()
		batch := p.pending
		p.pending = nil
		p.mu.Unlock()
		if len(batch) == 0 {
			return
		}
		for _, b := range batch {
			assert.NoError(t, replica.pty.put(string(b)))
			p.mu.Lock()
			p.written++
			p.mu.Unlock()
		}
	}
}

// drain waits until everything the tee handed over has reached the
// replica's parser.
func (p *teePump) drain(t *testing.T, replica *feedComponent) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		p.mu.Lock()
		empty := p.written == p.queued
		p.mu.Unlock()
		if empty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("tee pump never drained")
		}
		time.Sleep(time.Millisecond)
	}
	replica.settle(t)
}
