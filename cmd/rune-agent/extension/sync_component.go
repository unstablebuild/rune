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

package extension

import (
	"log/slog"
	"sync"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

// Turn phases reported by the status bar's Status element. The bar
// draws them as-is and colours them by looking the phase up in its
// status palette, so these must match the palette's keys.
const (
	phaseSending     = "SENDING"
	phaseThinking    = "REASONING"
	phaseReceiving   = "RECEIVING"
	phaseToolCalling = "EXECUTING"
	phaseCompacting  = "COMPACTING"
	phaseAsking      = dialoguetui.AskingStatusText
	phaseRateLimited = "ERROR"
)

// syncComponent wraps a dialoguetui.Component with the mutex that guards
// every UI mutation. Helper methods take the lock so callers do not have
// to coordinate access manually.
type syncComponent struct {
	mu   *sync.Mutex
	comp *dialoguetui.Component
	h    *aiEditorHandler
	// uri identifies the chat's tab, whose activity follows the turn.
	// It is zero for chats that are not tabs, such as queries.
	uri workspaceapi.URI
}

// completionOpen reports whether the chat's '#' completion band is
// showing.
func (s syncComponent) completionOpen() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comp.CompletionOpen()
}

// setStatusBarState mutates the chat's status bar state.
func (s syncComponent) setStatusBarState(fn func(*dialoguetui.StatusBarState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comp.SetStatusBarState(fn)
}

// swapPhase reports a new status bar phase and returns the one it
// replaced, so a caller that owns the bar for the length of an
// operation can hand it back to whatever was running before.
func (s syncComponent) swapPhase(phase string) string {
	var prev string
	s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
		prev = st.Phase
		st.Phase = phase
	})
	return prev
}

// beginTurn moves the bar into the running state. Usage is per turn and
// starts over, but context occupancy is not: it accumulates across the
// conversation and only the agent can report it, so clearing it here
// would leave the gauge empty until the first completion came back.
func (s syncComponent) beginTurn(start time.Time) {
	s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
		st.Active = true
		st.Phase = phaseSending
		st.ActiveForm = ""
		st.TurnStart = start
		st.Usage = llmapi.DialogueUsage{}
	})
	s.setTabActivity(true)
}

// endTurn returns the bar to idle, keeping whatever the turn reported
// so its result stays readable until the next one starts.
func (s syncComponent) endTurn() {
	s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
		st.Active = false
		st.Phase = ""
		st.ActiveForm = ""
	})
	s.setTabActivity(false)
}

// setTabActivity marks the chat's tab as having a turn in flight, so the
// host can show it while the tab or its workspace is not focused. It is
// best-effort: a failure loses the indicator, never the turn. It must be
// called without mu held, because the host serves it under the UI lock
// it also holds while drawing this chat.
func (s syncComponent) setTabActivity(active bool) {
	if s.uri == (workspaceapi.URI{}) {
		return
	}
	if err := s.h.wm.SetTabActivity(s.uri, active); err != nil {
		slog.Debug("set chat tab activity",
			"uri", s.uri.String(), "active", active, "error", err)
	}
}

// seedContextTokens fills the context gauge from a reopened
// conversation's replayed history. Until the first completion reports
// usage the gauge would otherwise read empty, which is
// indistinguishable from a chat that has not started yet. The count is
// the same local estimate the agent falls back to on its first
// iteration, so it is superseded by the first real usage report.
func (s syncComponent) seedContextTokens(
	svc llmapi.Service, entry llmapi.ModelEntry, msgs []llmapi.Message,
) {
	if len(msgs) == 0 {
		return
	}
	n, err := svc.CountTokens(entry, msgs)
	if err != nil {
		slog.Warn("count tokens for status bar context gauge", "error", err)
		return
	}
	if n <= 0 {
		return
	}
	s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
		if st.ContextTokens == 0 {
			st.ContextTokens = n
		}
		if entry.ContextWindow > 0 {
			st.ContextWindow = entry.ContextWindow
		}
	})
}
