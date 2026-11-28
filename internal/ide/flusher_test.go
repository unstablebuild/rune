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

package ide

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/text/textrpc"
)

// immediateFlusherTarget completes every async op from its own
// goroutine so the flusher's awaiter stays in flight long enough for a
// concurrent wait to block, which is the interleaving the workspace
// handler hits when the FS watcher starts a reload mid-drain. An op fails
// to start with startErr, and completes with err.
type immediateFlusherTarget struct {
	startErr, err error
}

func (t immediateFlusherTarget) done() (<-chan error, error) {
	if t.startErr != nil {
		return nil, t.startErr
	}
	ch := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		runtime.Gosched()
		ch <- t.err
		close(ch)
	})
	return ch, nil
}

func (t immediateFlusherTarget) FlushTab(
	context.Context, browserapi.Handler,
) (<-chan error, error) {
	return t.done()
}

func (t immediateFlusherTarget) ForceFlushTab(
	context.Context, browserapi.Handler,
) (<-chan error, error) {
	return t.done()
}

func (t immediateFlusherTarget) OverwriteTab(
	context.Context, browserapi.Handler,
) (<-chan error, error) {
	return t.done()
}

func (t immediateFlusherTarget) ReloadTab(
	context.Context, browserapi.Handler,
) (<-chan error, error) {
	return t.done()
}

func (immediateFlusherTarget) Resource(
	workspaceapi.URI,
) (browserapi.Handler, bool) {
	return nil, false
}

func TestFlusherWaitConcurrentWithStart(t *testing.T) {
	f := newFlusher(immediateFlusherTarget{}, &fakeNotifications{},
		func(func()) bool { return true })
	uri, err := workspaceapi.ParseURI("memory:///race.go")
	require.NoError(t, err)

	const iterations = 500
	var done sync.WaitGroup
	done.Add(2)
	go debug.CapturePanicReport(func() {
		defer done.Done()
		for range iterations {
			_ = f.reloadAsync(uri, nil, nil)
		}
	})
	go debug.CapturePanicReport(func() {
		defer done.Done()
		for range iterations {
			f.wait()
		}
	})
	done.Wait()

	f.wait()
	assert.Zero(t, f.inFlightCount(),
		"every awaiter must be accounted for once wait returns")
}

func TestFlusherReportsASaveToItsWaiter(t *testing.T) {
	saveErr := errors.New("disk full")
	cases := []struct {
		name   string
		target immediateFlusherTarget
		waiter bool
		// wantErr is what flush returns.
		wantErr error
		// wantResult is what the claimed waiter receives.
		wantResult error
		wantNotes  int
	}{
		{name: "saved", waiter: true},
		{name: "failed", target: immediateFlusherTarget{err: saveErr}, waiter: true,
			wantResult: saveErr},
		{name: "failed with no waiter", target: immediateFlusherTarget{err: saveErr},
			wantNotes: 1},
		{name: "not started", target: immediateFlusherTarget{startErr: textapi.ErrInvalidSave},
			waiter: true, wantErr: textapi.ErrInvalidSave},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			notes := &fakeNotifications{}
			s := newQueueSched()
			f := newFlusher(tc.target, notes, s.sched)
			uri, err := workspaceapi.ParseURI("memory:///saved.go")
			require.NoError(t, err)
			var w *textrpc.Waiter
			if tc.waiter {
				w = &textrpc.Waiter{Ch: make(chan error, 1)}
			}

			err = f.flush(uri, nil, w)
			require.ErrorIs(t, err, tc.wantErr)
			if err == nil {
				s.drain(t)
			}
			f.wait()

			assert.Len(t, notes.notes, tc.wantNotes)
			if w == nil {
				return
			}
			require.Equal(t, err == nil, w.Claimed, "the waiter is claimed when the save starts")
			if w.Claimed {
				assert.Equal(t, tc.wantResult, <-w.Ch)
			}
			assert.Empty(t, w.Ch)
		})
	}
}
