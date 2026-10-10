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
	"runtime"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

// immediateFlusherTarget completes every async op from its own
// goroutine so the flusher's awaiter stays in flight long enough for a
// concurrent wait to block, which is the interleaving the workspace
// handler hits when the FS watcher starts a reload mid-drain.
type immediateFlusherTarget struct{}

func (immediateFlusherTarget) done() (<-chan error, error) {
	ch := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		runtime.Gosched()
		ch <- nil
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
