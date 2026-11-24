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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
)

func TestEventLoopRunsCallbacksInOrder(t *testing.T) {
	loop := NewEventLoop(t)
	var got []int
	for i := range 3 {
		require.True(t, loop.Schedule(func() {
			got = append(got, i)
			if i == 0 {
				loop.Schedule(func() { got = append(got, 3) })
			}
		}))
	}
	loop.Run(func() {})
	loop.Run(func() {})
	assert.Equal(t, []int{0, 1, 2, 3}, got)
}

func TestEventLoopRejectsCallbacksOnceStopped(t *testing.T) {
	loop := NewEventLoop(t)
	loop.stop()
	assert.False(t, loop.Schedule(func() {}))
}

func TestEventLoopReportsABlockedCallback(t *testing.T) {
	tb := &recordingTB{TB: t}
	loop := NewEventLoop(tb)
	loop.timeout = 50 * time.Millisecond
	release := make(chan struct{})
	defer close(release)

	loop.Schedule(func() { <-release })
	ran := false
	loop.Run(func() { ran = true })

	require.Len(t, tb.failures, 1)
	assert.Contains(t, tb.failures[0], "blocked")
	assert.Contains(t, tb.failures[0], "TestEventLoopReportsABlockedCallback",
		"the report carries the stack of the blocked callback")
	assert.False(t, ran)
}

func TestLoopGatedIterator(t *testing.T) {
	t.Run("drained off the loop yields everything", func(t *testing.T) {
		loop := NewEventLoop(t)
		got, err := iterator.ToSlice(context.Background(),
			LoopGatedIterator(loop, []int{1}, []int{2, 3}))
		require.NoError(t, err)
		assert.Equal(t, []int{1, 2, 3}, got)
	})
	t.Run("drained on the loop blocks it", func(t *testing.T) {
		tb := &recordingTB{TB: t}
		loop := NewEventLoop(tb)
		loop.timeout = 50 * time.Millisecond

		it := LoopGatedIterator(loop, []int{1}, []int{2})
		loop.Run(func() { _, _ = iterator.ToSlice(t.Context(), it) })

		require.Len(t, tb.failures, 1)
		assert.Contains(t, tb.failures[0], "blocked")
	})
}

// recordingTB records the failures reported through it rather than failing
// the test.
type recordingTB struct {
	testing.TB

	mu       sync.Mutex
	failures []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Failed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.failures) > 0
}

func (r *recordingTB) Fatal(args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.failures = append(r.failures, fmt.Sprint(args...))
}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.Fatal(fmt.Sprintf(format, args...))
}
