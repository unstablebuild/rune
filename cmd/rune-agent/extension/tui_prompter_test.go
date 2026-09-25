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
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

func statusPhase(s syncComponent) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comp.StatusBarState().Phase
}

// A pending prompt blocks the turn on the user, so the bar reports
// ASKING for as long as one is open and hands the phase back to the
// tool call that opened it once the user answers.
func TestPromptReportsAskingPhase(t *testing.T) {
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
		st.Active = true
		st.Phase = phaseToolCalling
	})

	tx := make(chan dialoguetui.MessageEvent, 1)
	prompter := &tuiPrompter{tx: tx, noti: stubNotifications{}, status: s}

	type result struct {
		resp agent.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := prompter.Prompt(context.Background(), agent.PromptRequest{
			Title:   "Pick one",
			Options: []agent.PromptOption{{Label: "a", Value: "a"}},
		})
		done <- result{resp, err}
	}()

	ev := <-tx
	require.Equal(t, dialoguetui.MessageEventPrompt, ev.Type)
	require.Eventually(t, func() bool {
		return statusPhase(s) == phaseAsking
	}, time.Second, time.Millisecond)

	ev.PromptResult <- []string{"a"}
	got := <-done
	require.NoError(t, got.err)
	assert.Equal(t, []string{"a"}, got.resp.Values)
	assert.Equal(t, phaseToolCalling, statusPhase(s))
}
