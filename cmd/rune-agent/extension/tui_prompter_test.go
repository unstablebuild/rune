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
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

func statusPhase(s syncComponent) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.comp.StatusBarState().Phase
}

func TestPromptReportsAskingPhase(t *testing.T) {
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
		st.Active = true
		st.Phase = phaseToolCalling
	})

	wm := &recordingWindowManager{}
	uri, err := getModelUri("rolling-fox", "gpt-5")
	require.NoError(t, err)
	s.tab = newTabStatusReporter(wm, uri, s.h.n)
	s.tab.beginTurn()
	s.tab.setTitle(statusTitleTools)
	wm.reset()

	tx := make(chan dialoguetui.MessageEvent, 1)
	prompter := &tuiPrompter{tx: tx, chat: s}

	type result struct {
		resp agent.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := prompter.Prompt(context.Background(), agent.PromptRequest{
			Title:   "Pick one",
			Options: []agent.PromptOption{{Label: "a", Value: "a"}},
			Kind:    agent.PromptKindQuestion,
		})
		done <- result{resp, err}
	}()

	ev := <-tx
	require.Equal(t, dialoguetui.MessageEventPrompt, ev.Type)
	// Prompt sets the phase and reports the tab one after the other.
	require.Eventually(t, func() bool {
		return statusPhase(s) == phaseAsking && len(wm.tabStatuses()) > 0
	}, time.Second, time.Millisecond)
	assert.Equal(t, []browserapi.TabStatus{{
		State: browserapi.TabBlocked, Kind: browserapi.TabKindQuestion, Progress: -1,
		Title: "Pick one", Message: "Pick one",
	}}, wm.tabStatuses(), "the tab reports the chat blocked on the prompt")

	ev.PromptResult <- []string{"a"}
	got := <-done
	require.NoError(t, got.err)
	assert.Equal(t, []string{"a"}, got.resp.Values)
	assert.Equal(t, phaseToolCalling, statusPhase(s))
	assert.Equal(t, browserapi.TabStatus{State: browserapi.TabWorking, Progress: -1, Title: statusTitleTools},
		wm.tabStatuses()[len(wm.tabStatuses())-1], "the tab goes back to working once answered")
}
