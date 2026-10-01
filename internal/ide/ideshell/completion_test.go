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

package ideshell

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// iterCmd is a CommandHandler whose argument completion is backed by a
// caller-supplied iterator, letting tests drive streaming and blocking
// behavior precisely.
type iterCmd struct {
	iter func() iterator.Iterator[string]
}

func (iterCmd) HandleCommand(
	context.Context, repl.Command, repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

func (c iterCmd) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return c.iter(), nil
}

func (iterCmd) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

// blockingStringIter yields its first candidate immediately, then
// blocks on subsequent Next calls until the context is cancelled. This
// models a completer that starts slow background I/O after surfacing an
// initial result: emitting one element first lets layers that probe
// emptiness (e.g. term/sh's IsEmpty file fallback) proceed, while the
// feeder still blocks mid-stream. It records that the blocking Next
// started and that Close ran so tests can assert no leak.
type blockingStringIter struct {
	first   string
	sent    bool
	started chan struct{}
	once    sync.Once
	closed  chan struct{}
	closeOK sync.Once
}

func (b *blockingStringIter) Next(ctx context.Context) (string, bool) {
	if !b.sent {
		b.sent = true
		return b.first, true
	}
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return "", false
}

func (b *blockingStringIter) Err() error { return nil }

func (b *blockingStringIter) Close() error {
	b.closeOK.Do(func() { close(b.closed) })
	return nil
}

// TestCompleteDoesNotDrainIterator asserts that the shim returns from
// Complete without draining the candidate iterator, even when that
// iterator blocks indefinitely. A synchronous drain on the event loop
// would freeze the prompt.
func TestCompleteDoesNotDrainIterator(t *testing.T) {
	blk := &blockingStringIter{
		first: "alpha", started: make(chan struct{}), closed: make(chan struct{}),
	}
	shim := &completionShim{
		underlying: iterCmd{iter: func() iterator.Iterator[string] { return blk }},
	}

	done := make(chan iterator.Iterator[string], 1)
	go func() {
		it, err := shim.Complete(context.Background(), "g", []string{""})
		require.NoError(t, err)
		done <- it
	}()

	var ret iterator.Iterator[string]
	select {
	case ret = <-done:
	case <-time.After(time.Second):
		t.Fatal("Complete blocked draining the iterator")
	}

	// The SDK inputbox must see an empty iterator so it does not enter
	// inline completion; the real candidates are stashed for the overlay.
	_, ok := ret.Next(context.Background())
	assert.False(t, ok, "returned iterator must be empty")

	captured, has := shim.consume()
	require.True(t, has)
	assert.Same(t, iterator.Iterator[string](blk), captured.iter)
}

// TestOpenCompletionStreamsIncrementally feeds several candidates
// through the overlay and asserts they all land in the list.
func TestOpenCompletionStreamsIncrementally(t *testing.T) {
	h := newTestHandlerFull(t, nil, 100, func(r *CommandRegistry) {
		r.Register("g", "", iterCmd{iter: func() iterator.Iterator[string] {
			return iterator.FromSlice([]string{"alpha", "beta", "gamma"})
		}})
	})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "g ")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	h.WaitCompletion()
	drainTicks(h)

	assert.True(t, h.searching)
	assert.Equal(t, modeCompletion, h.mode)
	assert.Equal(t, 3, h.list.MatchCount())
}

// TestOpenCompletionCancelMidStreamClosesIterator cancels the overlay
// while the feeder is blocked on a slow iterator and asserts the
// goroutine exits and the iterator is closed (no leak).
func TestOpenCompletionCancelMidStreamClosesIterator(t *testing.T) {
	blk := &blockingStringIter{
		first: "alpha", started: make(chan struct{}), closed: make(chan struct{}),
	}
	h := newTestHandlerFull(t, nil, 100, func(r *CommandRegistry) {
		r.Register("g", "", iterCmd{iter: func() iterator.Iterator[string] { return blk }})
	})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "g ")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})

	select {
	case <-blk.started:
	case <-time.After(time.Second):
		t.Fatal("feeder did not start pulling from the iterator")
	}

	h.cancelSearch()

	assert.False(t, h.searching)
	assert.Nil(t, h.compDone, "feeder must be torn down")
	select {
	case <-blk.closed:
	case <-time.After(time.Second):
		t.Fatal("iterator was not closed after cancel")
	}
}

// TestOpenCompletionSingleMatchAutoAccepts asserts a stream that yields
// exactly one candidate fills the editor line inline and closes the
// overlay, preserving the pre-streaming single-completion UX.
func TestOpenCompletionSingleMatchAutoAccepts(t *testing.T) {
	h := newTestHandlerFull(t, nil, 100, func(r *CommandRegistry) {
		r.Register("g", "", iterCmd{iter: func() iterator.Iterator[string] {
			return iterator.FromSlice([]string{"alpha"})
		}})
	})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "g al")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	h.WaitCompletion()
	drainTicks(h)

	assert.False(t, h.searching, "overlay must close on single match")
	assert.Equal(t, "g alpha", h.editBuf.String())
}

// TestOpenCompletionZeroMatchClosesOverlay asserts an empty stream
// closes the overlay and leaves the editor line unchanged.
func TestOpenCompletionZeroMatchClosesOverlay(t *testing.T) {
	h := newTestHandlerFull(t, nil, 100, func(r *CommandRegistry) {
		r.Register("g", "", iterCmd{iter: func() iterator.Iterator[string] {
			return iterator.Empty[string]()
		}})
	})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "g zz")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	h.WaitCompletion()
	drainTicks(h)

	assert.False(t, h.searching, "overlay must close with no candidates")
	assert.Equal(t, "g zz", h.editBuf.String())
}

// slowScanIter models the debugger-launch program completer: its Next
// blocks until either a result is produced or the context is
// cancelled, mirroring streamEntrypointPaths' tree-sitter scan over a
// large repo that yields nothing for a while. Close cancels the scan.
type slowScanIter struct {
	cancelled chan struct{}
	once      sync.Once
}

func newSlowScanIter() *slowScanIter {
	return &slowScanIter{cancelled: make(chan struct{})}
}

func (s *slowScanIter) Next(ctx context.Context) (string, bool) {
	select {
	case <-ctx.Done():
		return "", false
	case <-s.cancelled:
		return "", false
	}
}

func (s *slowScanIter) Err() error { return nil }

func (s *slowScanIter) Close() error {
	s.once.Do(func() { close(s.cancelled) })
	return nil
}

// TestTabOnSlowScanDoesNotFreeze reproduces the debugger-launch freeze:
// pressing <tab> while the program completer is still scanning must
// return promptly (overlay opens, prompt stays responsive) rather than
// blocking the event loop until the scan settles.
func TestTabOnSlowScanDoesNotFreeze(t *testing.T) {
	scan := newSlowScanIter()
	h := newTestHandlerFull(t, nil, 100, func(r *CommandRegistry) {
		r.Register("debugger", "", iterCmd{
			iter: func() iterator.Iterator[string] { return scan },
		})
	})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "debugger launch ")

	done := make(chan struct{})
	go func() {
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tab froze the event loop while the scan was in flight")
	}

	assert.True(t, h.searching, "overlay should open even while scanning")

	// A subsequent key (here <esc> to cancel) must also return
	// promptly, tearing the in-flight feeder down without deadlock.
	cancelled := make(chan struct{})
	go func() {
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		close(cancelled)
	}()
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the overlay froze the event loop")
	}
	assert.False(t, h.searching)
}

// TestCompletion_ArrowKeyNavigationPreservedOnAccept asserts that navigating
// completion candidates with arrow keys preserves the selection upon
// acceptance (via Enter or Tab) rather than resetting back to the first
// candidate.
func TestCompletion_ArrowKeyNavigationPreservedOnAccept(t *testing.T) {
	tests := []struct {
		name     string
		navKeys  []term.Key
		accept   term.Key
		expected string
	}{
		{
			name:     "arrow down then enter accepts second candidate",
			navKeys:  []term.Key{term.KeyArrowDown},
			accept:   term.KeyEnter,
			expected: "g beta",
		},
		{
			name:     "arrow down then tab accepts second candidate",
			navKeys:  []term.Key{term.KeyArrowDown},
			accept:   term.KeyTab,
			expected: "g beta",
		},
		{
			name:     "arrow down twice then arrow up accepts second candidate",
			navKeys:  []term.Key{term.KeyArrowDown, term.KeyArrowDown, term.KeyArrowUp},
			accept:   term.KeyEnter,
			expected: "g beta",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHandlerFull(t, nil, 100, func(r *CommandRegistry) {
				r.Register("g", "", iterCmd{iter: func() iterator.Iterator[string] {
					return iterator.FromSlice([]string{"alpha", "beta", "gamma"})
				}})
			})
			h.Resize(testWidthH, testHeight)

			feedRunes(h, "g ")
			h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
			h.WaitCompletion()
			drainTicks(h)

			require.True(t, h.searching)

			for _, k := range tc.navKeys {
				h.Handle(term.Event{Type: term.EventKey, Key: k})
			}
			h.Handle(term.Event{Type: term.EventKey, Key: tc.accept})

			assert.False(t, h.searching)
			assert.Equal(t, tc.expected, h.editBuf.String())
		})
	}
}
