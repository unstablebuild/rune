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

	"github.com/stretchr/testify/assert"
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
}

func TestWaitParserHandlerDrop(t *testing.T) {
	t.Parallel()
	ph := newWaitParserHandler(t.Context(), newMockBellHandler())
	var triggers int
	ph.useTrigger(func() { triggers++ })

	var dropped int
	for range 3 {
		ph.scheduleBellCallback(func() { dropped++ })
	}
	assert.Equal(t, 1, triggers)
	ph.drop()
	assert.Zero(t, ph.pendingCallbacks())
	ph.Bell()
	assert.Zero(t, dropped, "a late bell ran a dropped callback")

	var called bool
	ph.scheduleBellCallback(func() { called = true })
	assert.Equal(t, 2, triggers, "the next callback must ring the shell again")
	ph.Bell()
	assert.True(t, called)
	assert.Zero(t, dropped)
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
