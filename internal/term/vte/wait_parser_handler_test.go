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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vte/vteparser"
)

func TestWaitParserHandler(t *testing.T) {
	t.Parallel()
	t.Run("schedules a callback", func(t *testing.T) {
		t.Parallel()
		mock := newMockBellHandler()
		ctx := t.Context()
		ph := newWaitParserHandler(ctx, mock)

		var callbackCalled, triggerCalled bool
		ph.useTrigger(func() {
			triggerCalled = true
		})
		ph.scheduleBellCallback(func() {
			callbackCalled = true
		})

		ph.Bell()
		assert.True(t, callbackCalled)
		assert.True(t, triggerCalled)
	})

	t.Run("schedules multiple callbacks, preserving order", func(t *testing.T) {
		t.Parallel()
		mock := newMockBellHandler()
		ctx := t.Context()
		ph := newWaitParserHandler(ctx, mock)

		n := 10
		ch := make(chan struct{}, n)

		var callbackCalled, triggerCalled int
		var wg sync.WaitGroup
		wg.Add(10)
		ph.useTrigger(func() {
			triggerCalled++
			ch <- struct{}{}
		})

		for i := range n {
			i := i
			ph.scheduleBellCallback(func() {
				defer wg.Done()
				assert.Equal(t, i, callbackCalled)
				callbackCalled++
			})
		}

		go func() {
			for {
				<-ch
				ph.Bell()
			}
		}()

		wg.Wait()
		assert.Equal(t, n, callbackCalled)
		assert.Equal(t, n, triggerCalled)
	})

	t.Run("is goroutine safe", func(t *testing.T) {
		t.Parallel()
		mock := newMockBellHandler()
		ctx := t.Context()
		ph := newWaitParserHandler(ctx, mock)

		n := 10000

		var callbackCalled, triggerCalled atomic.Int32
		ph.useTrigger(func() {
			triggerCalled.Add(1)
			ph.Bell()
		})

		var wg sync.WaitGroup
		wg.Add(n)
		go func() {
			for i := range n {
				i := i
				ph.scheduleBellCallback(func() {
					defer wg.Done()
					assert.Equal(t, i+1, int(callbackCalled.Add(1)))
				})
			}
		}()

		wg.Wait()
		assert.Equal(t, n, int(callbackCalled.Load()))
	})

	t.Run("runs callbacks scheduled while the parser rings the bell", func(t *testing.T) {
		t.Parallel()
		ph := newWaitParserHandler(t.Context(), newMockBellHandler())

		const n = 20000
		// The trigger only asks for a bell; the parser rings it on its
		// own goroutine, the way the shell's answer comes back.
		bells := make(chan struct{}, n)
		ph.useTrigger(func() { bells <- struct{}{} })
		go debug.CapturePanicReport(func() {
			for {
				select {
				case <-bells:
					ph.Bell()
				case <-t.Context().Done():
					return
				}
			}
		})

		var called atomic.Int32
		done := make(chan struct{})
		go debug.CapturePanicReport(func() {
			defer close(done)
			for range n / 2 {
				// A vi key schedules its edit and then the cursor fix.
				for range 2 {
					for !ph.scheduleBellCallback(func() { called.Add(1) }) {
						if t.Context().Err() != nil {
							return
						}
						time.Sleep(time.Millisecond)
					}
				}
			}
		})
		require.Eventually(t, func() bool { return called.Load() == n }, 10*time.Second,
			time.Millisecond, "a callback was left without a bell to run it")
		<-done
	})

	t.Run("asks for a bell again after dropping unanswered callbacks", func(t *testing.T) {
		t.Parallel()
		ph := newWaitParserHandler(t.Context(), newMockBellHandler())
		var triggers atomic.Int32
		ph.useTrigger(func() { triggers.Add(1) })

		require.True(t, ph.scheduleBellCallback(func() { t.Error("ran a dropped callback") }))
		require.Eventually(t, func() bool { return ph.pendingCallbacks() == 0 }, 5*time.Second,
			10*time.Millisecond, "the unanswered callback was not dropped")

		var ran bool
		require.True(t, ph.scheduleBellCallback(func() { ran = true }))
		assert.Equal(t, int32(2), triggers.Load())
		ph.Bell()
		assert.True(t, ran)
	})

	t.Run("tells the owner of a callback dropped for want of a bell", func(t *testing.T) {
		t.Parallel()
		ph := newWaitParserHandler(t.Context(), newMockBellHandler())
		ph.useTrigger(func() {})

		var told atomic.Int32
		require.True(t, ph.scheduleBellCallbackOrDrop(
			func() { t.Error("ran a dropped callback") }, func() { told.Add(1) }))
		require.Eventually(t, func() bool { return told.Load() == 1 }, 5*time.Second,
			10*time.Millisecond, "the owner of the unanswered callback was not told")
		ph.Bell()
		assert.Equal(t, int32(1), told.Load())
	})

	t.Run("tells nobody of a callback it could not queue", func(t *testing.T) {
		t.Parallel()
		ph := newWaitParserHandler(t.Context(), newMockBellHandler())
		ph.useTrigger(func() {})

		var queued, told atomic.Int32
		for ph.scheduleBellCallbackOrDrop(func() {}, func() { told.Add(1) }) {
			queued.Add(1)
		}
		assert.False(t, ph.scheduleBellCallbackOrDrop(
			func() { t.Error("ran a callback it could not queue") },
			func() { t.Error("told of a callback it could not queue") }))
		ph.drop()
		assert.Equal(t, queued.Load(), told.Load(), "every queued callback's owner is told once")
	})
}

func TestWaitParserHandlerDrop(t *testing.T) {
	t.Parallel()
	ph := newWaitParserHandler(t.Context(), newMockBellHandler())
	var triggers int
	ph.useTrigger(func() { triggers++ })

	var dropped, told int
	for range 3 {
		ph.scheduleBellCallback(func() { dropped++ })
	}
	ph.scheduleBellCallbackOrDrop(func() { dropped++ }, func() { told++ })
	assert.Equal(t, 1, triggers)
	ph.drop()
	assert.Zero(t, ph.pendingCallbacks())
	assert.Equal(t, 1, told, "drop must tell the callback's owner")
	ph.Bell()
	assert.Zero(t, dropped, "a late bell ran a dropped callback")

	var called bool
	ph.scheduleBellCallbackOrDrop(func() { called = true }, func() { told++ })
	assert.Equal(t, 2, triggers, "the next callback must ring the shell again")
	ph.Bell()
	assert.True(t, called)
	assert.Zero(t, dropped)
	assert.Equal(t, 1, told, "a callback that ran was not dropped")
}

type mockBellHandler struct {
	vteparser.Handler
}

func newMockBellHandler() *mockBellHandler {
	ret := new(mockBellHandler)
	ret.Handler = vteparser.NopHandler()
	return ret
}

func (m *mockBellHandler) Bell() {
}
