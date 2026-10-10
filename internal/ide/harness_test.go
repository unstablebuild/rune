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

package ide_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/localstorage"
)

func newE2EStorage(t *testing.T, dataDir string) storageapi.Service {
	t.Helper()
	return localstorage.New(context.Background(), dataDir, docbson.Marshaler())
}

// newHostScheduler returns a scheduleNextTick stub bound to mu, a
// start that begins dispatching, and a drain that blocks until every
// queued callback has run.
//
// A single consumer goroutine drains a FIFO queue, running each
// callback under mu in enqueue order, mirroring the host event loop's
// UserFunc serialization. Enqueuing never touches mu, so a caller
// holding mu does not deadlock against the consumer.
//
// Dispatch stays parked until start is called because the host loop
// only pumps UserFunc events once it is running: callbacks scheduled
// while the IDE is still being constructed are queued, not run
// alongside the constructor.
func newHostScheduler(t *testing.T, mu sync.Locker) (
	sched func(func()) bool, start func(), drain func(),
) {
	var schedMu sync.Mutex
	schedCond := sync.NewCond(&schedMu)
	var queue []func()
	running := false
	stopped := false
	started := false
	go debug.CapturePanicReport(func() {
		for {
			schedMu.Lock()
			for (len(queue) == 0 || !started) && !stopped {
				schedCond.Wait()
			}
			if stopped {
				schedMu.Unlock()
				return
			}
			fn := queue[0]
			queue = queue[1:]
			running = true
			schedMu.Unlock()

			mu.Lock()
			fn()
			mu.Unlock()

			schedMu.Lock()
			running = false
			schedCond.Broadcast()
			schedMu.Unlock()
		}
	})
	// Registered before any caller cleanup so it runs last (LIFO):
	// teardown that still schedules callbacks keeps a live consumer.
	t.Cleanup(func() {
		schedMu.Lock()
		stopped = true
		schedCond.Broadcast()
		schedMu.Unlock()
	})
	sched = func(fn func()) bool {
		schedMu.Lock()
		queue = append(queue, fn)
		schedCond.Broadcast()
		schedMu.Unlock()
		return true
	}
	start = func() {
		schedMu.Lock()
		started = true
		schedCond.Broadcast()
		schedMu.Unlock()
	}
	drain = func() {
		schedMu.Lock()
		defer schedMu.Unlock()
		for len(queue) > 0 || running {
			schedCond.Wait()
		}
	}
	return sched, start, drain
}

// newHostIDE builds an IDE the way the host does: nothing dispatches
// the callbacks the constructor schedules until construction has
// finished, so they cannot interleave with it.
func newHostIDE(t *testing.T, mu *sync.Mutex, dir, cfgPath string,
	opts ...ide.Option,
) (*ide.IDE, func()) {
	t.Helper()
	sched, start, drain := newHostScheduler(t, mu)
	opts = append([]ide.Option{
		ide.WithLocker(mu),
		ide.WithScheduleNextTick(sched),
		ide.WithPublishEvent(func(term.Event) bool { return true }),
	}, opts...)
	i, err := ide.New(dir, cfgPath, dir, pkgtrust.NewStore(dir, nil),
		newE2EStorage(t, dir), opts...)
	require.NoError(t, err)
	start()
	return i, drain
}

// e2eLockedHandler mirrors the way the production event loop drives
// the IDE handler: every Handle/Draw acquires mu before delegating
// and releases it afterwards so scheduled callbacks (spawned by
// hostScheduleNextTick) can run in between. After each Handle, the
// wrapper waits for in-flight async saves/reloads so the next
// Draw/Handle observes the post-completion state.
type e2eLockedHandler struct {
	tui.Handler
	mu    *sync.Mutex
	ide   *ide.IDE
	drain func()
}

func (h e2eLockedHandler) Handle(ev term.Event) (bool, bool) {
	h.mu.Lock()
	quit, handled := h.Handler.Handle(ev)
	h.mu.Unlock()
	h.ide.WaitInflight()
	// WaitInflight only waits for the IDE flusher's awaiter
	// goroutines. text/component.dispatchFlush is scheduled
	// independently through the same scheduler and would otherwise
	// race the next Draw — wait for the scheduler to drain too.
	if h.drain != nil {
		h.drain()
	}
	return quit, handled
}

func (h e2eLockedHandler) Draw(w term.Writer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Handler.Draw(w)
}

func (h e2eLockedHandler) Resize(width, height int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.Handler.Resize(width, height)
}

func (h e2eLockedHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Cursor()
}

func (h e2eLockedHandler) Selection() (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.Handler.Selection()
}
