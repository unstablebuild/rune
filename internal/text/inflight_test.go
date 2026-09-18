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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

func newInflightComponent() *Component {
	return &Component{inflight: make(map[string]*inflightFileOps)}
}

func mustParseURI(t *testing.T, raw string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI(raw)
	require.NoError(t, err)
	return uri
}

func TestAfterSettledRunsOnceApplied(t *testing.T) {
	for _, applied := range []bool{true, false} {
		name := "applied"
		if !applied {
			name = "schedule refused"
		}
		t.Run(name, func(t *testing.T) {
			c := newInflightComponent()
			uri := mustParseURI(t, "memory:///settled.go")

			var calls []string
			assert.False(t, c.AfterSettled(uri, func() { calls = append(calls, "idle") }),
				"nothing in flight must not defer the caller")

			c.beginFileOp(uri)
			require.True(t, c.AfterSettled(uri, func() { calls = append(calls, "deferred") }))
			assert.Empty(t, calls, "a deferred callback must wait for the result")

			c.endFileOp(uri, applied)
			if applied {
				assert.Equal(t, []string{"deferred"}, calls)
			} else {
				assert.Empty(t, calls,
					"a refused schedule must not run event-loop work on the worker")
			}
			assert.False(t, c.AfterSettled(uri, func() { calls = append(calls, "late") }),
				"the file must be settled once its last operation ends")
		})
	}
}

func TestAfterSettledWaitsForEveryOperation(t *testing.T) {
	c := newInflightComponent()
	first := mustParseURI(t, "memory:///first.go")
	second := mustParseURI(t, "memory:///second.go")

	var calls []string
	c.beginFileOp(first)
	c.beginFileOp(first)
	c.beginFileOp(second)
	require.True(t, c.AfterSettled(first, func() { calls = append(calls, "first") }))
	require.True(t, c.AfterSettled(second, func() { calls = append(calls, "second") }))

	c.endFileOp(first, true)
	assert.Empty(t, calls, "a file with work left must stay pending")

	c.endFileOp(second, true)
	assert.Equal(t, []string{"second"}, calls, "files must settle independently")

	c.endFileOp(first, true)
	assert.Equal(t, []string{"second", "first"}, calls)
}

func TestAfterSettledCallbackCanStartNewWork(t *testing.T) {
	c := newInflightComponent()
	uri := mustParseURI(t, "memory:///reentrant.go")

	var stillPending, deferredAgain bool
	c.beginFileOp(uri)
	require.True(t, c.AfterSettled(uri, func() {
		// A watcher recheck reruns its own handling, which can start a
		// reload for the same file.
		stillPending = c.AfterSettled(uri, func() {})
		c.beginFileOp(uri)
		deferredAgain = c.AfterSettled(uri, func() { deferredAgain = true })
	}))

	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		c.endFileOp(uri, true)
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a deferred callback deadlocked while starting new work")
	}

	assert.False(t, stillPending, "the settled file must be cleared before callbacks run")
	assert.True(t, deferredAgain, "work started from a callback must be waitable again")
	c.endFileOp(uri, true)
	assert.False(t, c.AfterSettled(uri, func() {}))
}
