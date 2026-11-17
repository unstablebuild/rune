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

package handlertest

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"unstable.build/rune/internal/debug"
)

const eventLoopTimeout = 5 * time.Second

// EventLoop stands in for the host event loop: it runs the callbacks given
// to Schedule one at a time, in the order they were scheduled, on a
// goroutine of its own. A callback that blocks holds up every callback
// queued behind it, as it does in production, so a callback that waits on
// work which itself needs the loop deadlocks here too. Schedulers that run
// callbacks inline, or each on a goroutine of its own, hide that.
type EventLoop struct {
	t       testing.TB
	timeout time.Duration

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []func()
	stopped bool
}

// NewEventLoop starts an EventLoop that stops once t and the cleanups
// registered after it are done. A callback blocked when it stops is
// abandoned.
func NewEventLoop(t testing.TB) *EventLoop {
	l := &EventLoop{t: t, timeout: eventLoopTimeout}
	l.cond = sync.NewCond(&l.mu)
	go debug.CapturePanicReport(l.run)
	t.Cleanup(l.stop)
	return l
}

func (l *EventLoop) run() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		for len(l.queue) == 0 && !l.stopped {
			l.cond.Wait()
		}
		if l.stopped {
			return
		}
		fn := l.queue[0]
		l.queue = l.queue[1:]
		l.mu.Unlock()
		fn()
		l.mu.Lock()
	}
}

func (l *EventLoop) stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	l.cond.Signal()
}

// Schedule queues fn to run on the loop, from any goroutine including the
// loop's. Like the host's ScheduleNextTick, it reports false, dropping fn,
// once the loop has stopped.
func (l *EventLoop) Schedule(fn func()) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopped {
		return false
	}
	l.queue = append(l.queue, fn)
	l.cond.Signal()
	return true
}

// Run runs fn on the loop and waits for it to return. It fails the test,
// reporting every goroutine's stack, when the loop does not finish fn in
// time: fn, or a callback queued before it, is blocked.
func (l *EventLoop) Run(fn func()) {
	l.t.Helper()
	done := make(chan struct{})
	if !l.Schedule(func() {
		defer close(done)
		fn()
	}) {
		l.t.Fatal("the event loop has stopped")
		return
	}
	select {
	case <-done:
	case <-time.After(l.timeout):
		l.t.Fatalf("the event loop has been blocked for %s:\n%s", l.timeout, goroutines())
	}
}

// Eventually runs cond on the loop until it reports true, failing the test
// with msg if it does not in time, or as Run does if the loop blocks.
func (l *EventLoop) Eventually(cond func() bool, msg string) {
	l.t.Helper()
	deadline := time.Now().Add(l.timeout)
	for {
		var ok bool
		l.Run(func() { ok = cond() })
		if ok || l.t.Failed() {
			return
		}
		if time.Now().After(deadline) {
			l.t.Fatalf("%s: not true after %s", msg, l.timeout)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// LoopGatedIterator returns an iterator that yields ready at once, but
// pending only once loop has run a callback the iterator schedules when it
// is first asked past ready. It stands for a source whose rest is produced
// on the event loop, such as a composite syntax tree whose host is parsed
// while its sections still wait for their parse to finish on the loop:
// draining it on loop deadlocks the loop. Next gives up when its context is
// done.
func LoopGatedIterator[T any](loop *EventLoop, ready, pending []T) iterator.Iterator[T] {
	var served chan struct{}
	return iterator.FromFunc(func(ctx context.Context) (next T, ok bool, err error) {
		if len(ready) > 0 {
			next, ready = ready[0], ready[1:]
			return next, true, nil
		}
		if served == nil {
			ch := make(chan struct{})
			served = ch
			if !loop.Schedule(func() { close(ch) }) {
				return next, false, errors.New("the event loop has stopped")
			}
		}
		select {
		case <-served:
		case <-ctx.Done():
			return next, false, ctx.Err()
		}
		if len(pending) == 0 {
			return next, false, nil
		}
		next, pending = pending[0], pending[1:]
		return next, true, nil
	}, func() error { return nil })
}

func goroutines() string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return string(buf[:n])
		}
		buf = make([]byte, 2*len(buf))
	}
}
