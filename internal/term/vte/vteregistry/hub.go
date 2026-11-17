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

package vteregistry

import (
	"sync"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/vte/vtesnap"
	"unstable.build/rune/internal/term/vte/vtewire"
)

// historyPageRows is how many scrollback rows one HISTORY_PAGE frame
// carries. Small enough that a client renders history progressively,
// large enough that framing is not the dominant cost.
const historyPageRows = 64

// teeHub fans one terminal's raw pty stream out to every wire member.
//
// Membership changes are made while the emulator's feed is held still
// by [vte.Component.CutStream], so a batch is either part of the
// snapshot a joining member received or delivered to it, never both.
// The hub's own mutex only protects the member set; it is never held
// while a member blocks on its socket, because members queue rather
// than write.
type teeHub struct {
	mu      sync.Mutex
	members map[*member]struct{}
}

// broadcast queues one frame for every member. Callers hold the
// emulator's feed still, so the frame lands at the same stream
// position for every member. It runs on the parse goroutine and must
// not block.
func (h *teeHub) broadcast(f vtewire.Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for m := range h.members {
		m.send(f)
	}
}

// add registers a member. It must be called while the feed is held
// still so the member's first byte is the one after its snapshot.
func (h *teeHub) add(m *member) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.members == nil {
		h.members = make(map[*member]struct{})
	}
	h.members[m] = struct{}{}
}

func (h *teeHub) remove(m *member) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.members, m)
}

// close ends every member with last as the reason, once whatever was
// queued before it is written.
func (h *teeHub) close(last vtewire.Frame) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for m := range h.members {
		m.send(last)
		m.sender.Close()
	}
}

// member is one wire attachment's slot in the hub. It owns the send
// queue, so a client that stops reading is dropped instead of stalling
// the terminal for everyone else.
type member struct {
	sender *vtewire.Sender
	// dropped is set once the queue overflowed or the socket failed,
	// so the hub stops paying to encode for a dead attachment. It is
	// atomic because the parse goroutine broadcasts while the session
	// goroutine sends frames of its own.
	dropped atomic.Bool
	// onDrop is called once, by whoever finds the member dropped first.
	onDrop func()
}

func (m *member) send(f vtewire.Frame) {
	if m.dropped.Load() {
		return
	}
	if err := m.sender.Send(f); err != nil {
		m.drop()
	}
}

func (m *member) drop() {
	if !m.dropped.Swap(true) && m.onDrop != nil {
		m.onDrop()
	}
}

// sendHistory queues the terminal's scrollback newest page first, so a
// client can prepend each page above what it already renders.
func (m *member) sendHistory(history [][]term.Cell) {
	for end := len(history); end > 0; end -= historyPageRows {
		start := max(end-historyPageRows, 0)
		m.send(&vtewire.HistoryPage{
			Remaining: start,
			Page: vtesnap.EncodePage(
				vtesnap.ScreenPrimary, history[start:end]),
		})
	}
}
