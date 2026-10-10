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

package treesitter

import (
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/syntax"
)

// notifyReadsState mirrors the real production chain
// notis.Notify -> notis.inFocus -> workspaceManagerHandler.focusURI,
// which reads workspace-handler state on the assumption that the
// caller is on the event loop. The fake reads `state` without
// synchronization so the race detector can flag any caller that
// invokes Notify off the event-loop goroutine.
type notifyReadsState struct {
	state *int
}

func (n notifyReadsState) Notify(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	_ = *n.state
	return "", nil
}

func (n notifyReadsState) NotifyOnce(
	browserapi.NotificationLevel, string, ...any,
) (string, error) {
	_ = *n.state
	return "", nil
}

func (notifyReadsState) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

func TestNotifyNotAvailRunsOnEventLoop(t *testing.T) {
	const iterations = 200

	ticks := make(chan func(), 256)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		for fn := range ticks {
			fn()
		}
	}()

	state := 0
	tree := &Tree{
		n:           notifyReadsState{state: &state},
		interrupter: term.NopInterrupter(),
		config: syntax.Config{
			ScheduleNextTick: func(fn func()) bool {
				ticks <- fn
				return true
			},
		},
	}

	mutDone := make(chan struct{})
	go func() {
		defer close(mutDone)
		for i := range iterations {
			ticks <- func() { state = i }
		}
	}()

	callerDone := make(chan struct{})
	go func() {
		defer close(callerDone)
		for range iterations {
			tree.config.ScheduleNextTick(func() { tree.notifyNotAvail("go") })
		}
	}()

	<-mutDone
	<-callerDone

	// Drain any remaining scheduled callbacks before tearing down
	// the simulated event loop.
	drained := make(chan struct{})
	ticks <- func() { close(drained) }
	<-drained
	close(ticks)
	<-loopDone

	// Reference state once on the test goroutine after the loop
	// has stopped so the compiler does not eliminate the writes.
	_ = state
}

// Ensure notifyReadsState satisfies browserapi.Notifications.
var _ browserapi.Notifications = notifyReadsState{}
