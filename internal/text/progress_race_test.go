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

package text

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
)

// loopRunner serializes scheduled callbacks onto a single goroutine,
// mimicking the host event loop where workspaceManagerHandler.focus
// and .workspaces are mutated and read.
type loopRunner struct {
	ch   chan func()
	done chan struct{}
	wg   sync.WaitGroup
}

func newLoopRunner() *loopRunner {
	l := &loopRunner{
		ch:   make(chan func(), 64),
		done: make(chan struct{}),
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		for {
			select {
			case fn := <-l.ch:
				fn()
			case <-l.done:
				return
			}
		}
	}()
	return l
}

func (l *loopRunner) schedule(fn func()) bool {
	l.ch <- fn
	return true
}

func (l *loopRunner) stop() {
	close(l.done)
	l.wg.Wait()
}

// focusNotifications models notis: every Notify and
// UpdateNotificationProgress reads a focus value that the loop runner
// mutates. Calls off the loop race that read.
type focusNotifications struct {
	focus int
	calls int
}

func (f *focusNotifications) Notify(
	_ browserapi.NotificationLevel, _ string, _ ...any,
) (string, error) {
	_ = f.focus
	f.calls++
	return "id", nil
}

func (f *focusNotifications) NotifyOnce(
	level browserapi.NotificationLevel, msg string, args ...any,
) (string, error) {
	return f.Notify(level, msg, args...)
}

func (f *focusNotifications) UpdateNotificationProgress(
	_, _ string, _, _ int64,
) error {
	_ = f.focus
	f.calls++
	return nil
}

func TestSchedNotifyProgressWriterAvoidsRace(t *testing.T) {
	t.Parallel()

	loop := newLoopRunner()
	defer loop.stop()

	fn := &focusNotifications{}
	pw := NewNotifyProgressWriter(fn, nil, "install pkg@1.0", loop.schedule)

	var producerWG sync.WaitGroup
	producerWG.Add(1)
	go func() {
		defer producerWG.Done()
		for i := int64(1); i <= 50; i++ {
			pw.Progress(i, 100, "B")
		}
		pw.Progress(100, 100, "B")
	}()

	// Concurrent mutator on the loop, simulating switchToWorkspace.
	var mutWG sync.WaitGroup
	mutWG.Add(1)
	go func() {
		defer mutWG.Done()
		for i := 0; i < 50; i++ {
			loop.schedule(func() { fn.focus++ })
		}
	}()

	producerWG.Wait()
	mutWG.Wait()

	done := make(chan struct{})
	loop.schedule(func() { close(done) })
	<-done

	require.Greater(t, fn.calls, 0)
}
