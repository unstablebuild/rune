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
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/clipboard/sysclip"
	"unstable.build/rune/internal/debug"
)

// NewAsyncSystemClipboard returns the system clipboard with a Copy that does
// not wait for the OS write, which can stall the caller for as long as the
// platform's clipboard utility takes (see #170). Copy reports only a clipboard
// that could not be opened; a failed write is lost. Create one per process and
// share it: a Paste through another instance can miss a Copy that has not
// reached the OS yet.
func NewAsyncSystemClipboard() clipboard.Register {
	return newAsyncSystemClipboard(sysclip.NewRegister())
}

func newAsyncSystemClipboard(sys clipboard.Register, err error) clipboard.Register {
	ret := newSystemClipboard(sys, err)
	if err != nil {
		// Copy never reaches the OS, so it cannot block, and staying
		// synchronous hands the open error to every caller.
		return ret
	}
	return newAsyncRegister(ret)
}

// asyncRegister adds asynchronous, last-copy-wins Copy semantics to another
// clipboard.Register for the default register. Named registers are in-memory
// writes on the system clipboard, so they are forwarded synchronously and
// stay in order; the queue therefore only ever holds default-register
// payloads and cannot discard across registers. Paste is forwarded
// synchronously, except while a write is queued or in flight: then the OS
// clipboard is not authoritative and the newest copy is returned instead.
type asyncRegister struct {
	other clipboard.Register
	ch    chan clipboard.Data

	pending atomic.Int32
	last    atomic.Value // clipboard.Data
}

var _ clipboard.Register = (*asyncRegister)(nil)

func newAsyncRegister(other clipboard.Register) *asyncRegister {
	ret := &asyncRegister{other: other, ch: make(chan clipboard.Data, 1)}
	go debug.CapturePanicReport(ret.startWorker)
	return ret
}

// Copy satisfies clipboard.Register. The channel has a single slot: when the
// worker is busy the pending payload is consumed and replaced, so the newest
// copy always wins, worthless intermediate writes are discarded, and no lock
// is needed for concurrent publishers to never block on the worker.
func (r *asyncRegister) Copy(registerID string, data clipboard.Data) error {
	if registerID != clipboard.DefaultRegisterID {
		return r.other.Copy(registerID, data)
	}
	r.last.Store(data)
	r.pending.Add(1)
	for {
		select {
		case r.ch <- data:
			return nil
		default:
		}
		select {
		case <-r.ch:
			r.pending.Add(-1)
		default:
		}
	}
}

// Paste satisfies clipboard.Register. A pending count above zero means a copy
// is queued or its write is in flight, so the OS clipboard would answer with
// stale content.
func (r *asyncRegister) Paste(registerID string) (clipboard.Data, error) {
	if registerID == clipboard.DefaultRegisterID && r.pending.Load() > 0 {
		return r.last.Load().(clipboard.Data), nil
	}
	return r.other.Paste(registerID)
}

// Errors from the worker are dropped: callers ignore Copy errors, and Paste
// still surfaces a broken OS clipboard.
func (r *asyncRegister) startWorker() {
	for data := range r.ch {
		_ = r.other.Copy(clipboard.DefaultRegisterID, data)
		r.pending.Add(-1)
	}
}
