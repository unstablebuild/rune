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
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
	"unstable.build/rune/internal/llm/anthropic"
)

func newStatusBarSyncComponent() syncComponent {
	comp := dialoguetui.NewComponent(dialoguetui.ComponentConfig{
		StatusBar: dialoguetui.StatusBarConfig{Enabled: true},
	})
	return syncComponent{
		mu:   new(sync.Mutex),
		comp: comp,
		h:    &aiEditorHandler{n: stubNotifications{}},
	}
}

// Reopening a conversation must fill the context gauge from the
// replayed history; until the first completion reports usage the bar
// would otherwise read empty, which is indistinguishable from a chat
// that has not started yet.
func TestSeedContextTokens(t *testing.T) {
	entry := llmapi.ModelEntry{
		Provider: "test", Name: "test-model", ContextWindow: 200_000,
	}
	msgs := []llmapi.Message{
		{Role: llmapi.RoleUser, Content: "hello"},
		{Role: llmapi.RoleAssistant, Content: "hi"},
	}

	svc := llmtest.New([]llmapi.ModelEntry{entry})
	svc.CountTokensFn = func(_ llmapi.ModelEntry, m []llmapi.Message) (int, error) {
		assert.Equal(t, msgs, m)
		return 1234, nil
	}

	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	s.seedContextTokens(svc, entry, msgs)

	got := s.comp.StatusBarState()
	assert.Equal(t, 1234, got.ContextTokens)
	assert.Equal(t, 200_000, got.ContextWindow)
}

func TestSeedContextTokensSkipped(t *testing.T) {
	entry := llmapi.ModelEntry{Provider: "test", Name: "test-model"}

	tests := []struct {
		name  string
		msgs  []llmapi.Message
		count int
		err   error
	}{
		{name: "no history", msgs: nil, count: 99},
		{
			name: "count fails", msgs: []llmapi.Message{{Content: "x"}},
			err: errors.New("boom"),
		},
		{
			name: "count returns nothing",
			msgs: []llmapi.Message{{Content: "x"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := llmtest.New([]llmapi.ModelEntry{entry})
			svc.CountTokensFn = func(llmapi.ModelEntry, []llmapi.Message) (int, error) {
				return tt.count, tt.err
			}

			s := newStatusBarSyncComponent()
			t.Cleanup(func() { _ = s.comp.Close() })
			s.seedContextTokens(svc, entry, tt.msgs)

			assert.Zero(t, s.comp.StatusBarState().ContextTokens)
		})
	}
}

// A usage report from a live turn is authoritative; a late estimate
// must not overwrite it.
func TestSeedContextTokensDoesNotOverwriteReportedUsage(t *testing.T) {
	entry := llmapi.ModelEntry{Provider: "test", Name: "test-model"}
	svc := llmtest.New([]llmapi.ModelEntry{entry})
	svc.CountTokensFn = func(llmapi.ModelEntry, []llmapi.Message) (int, error) {
		return 1234, nil
	}

	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
		st.ContextTokens = 77
	})
	s.seedContextTokens(svc, entry, []llmapi.Message{{Content: "x"}})

	assert.Equal(t, 77, s.comp.StatusBarState().ContextTokens)
}

// Context occupancy accumulates over a conversation, so starting a turn
// must not blank it. Clearing it left the gauge reading 0 until the
// first completion reported usage back, which is how a fresh chat
// reads.
func TestBeginTurnKeepsContextOccupancy(t *testing.T) {
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })

	s.setStatusBarState(func(st *dialoguetui.StatusBarState) {
		st.ContextTokens = 120_000
		st.ContextWindow = 1_000_000
		st.Usage = llmapi.DialogueUsage{TokensSent: 5_000, TokensCached: 4_000}
	})

	start := time.Now()
	s.beginTurn(start)

	got := s.comp.StatusBarState()
	assert.Equal(t, 120_000, got.ContextTokens,
		"what the conversation already occupies carries into the new turn")
	assert.Equal(t, 1_000_000, got.ContextWindow)
	assert.Zero(t, got.Usage.TokensSent, "per-turn usage starts fresh")
	assert.True(t, got.Active)
	assert.Equal(t, phaseSending, got.Phase)
	assert.Equal(t, start, got.TurnStart)
}

// A chat tab's activity follows its turn, so the host can show which
// chats are working while they are not focused.
func TestTurnMarksChatTabActivity(t *testing.T) {
	wm := &recordingWindowManager{}
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	s.h.wm = wm
	uri, err := getModelUri("rolling-fox", "gpt-5")
	require.NoError(t, err)
	s.uri = uri

	s.beginTurn(time.Now())
	assert.Equal(t, []tabActivity{{uri: uri, active: true}}, wm.tabActivity())
	s.endTurn()
	assert.Equal(t, []tabActivity{
		{uri: uri, active: true}, {uri: uri, active: false},
	}, wm.tabActivity())
}

// The indicator is best-effort: a host that cannot mark the tab must not
// keep the turn from driving the status bar.
func TestTurnSurvivesTabActivityErrors(t *testing.T) {
	wm := &recordingWindowManager{activityErr: errors.New("unknown tab")}
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	s.h.wm = wm
	uri, err := getModelUri("rolling-fox", "gpt-5")
	require.NoError(t, err)
	s.uri = uri

	s.beginTurn(time.Now())
	assert.True(t, s.comp.StatusBarState().Active)
	s.endTurn()
	assert.False(t, s.comp.StatusBarState().Active)
	assert.Len(t, wm.tabActivity(), 2)
}

// Chats that are not tabs, such as queries, have no tab to mark.
func TestTurnWithoutTabSkipsActivity(t *testing.T) {
	wm := &recordingWindowManager{}
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	s.h.wm = wm

	s.beginTurn(time.Now())
	s.endTurn()
	assert.Empty(t, wm.tabActivity())
}

// An unset session effort must name the level the provider applies,
// not leave the bar reading "default".
func TestSyncStatusBarModelResolvesProviderDefaultEffort(t *testing.T) {
	tests := []struct {
		name    string
		entry   llmapi.ModelEntry
		session llmapi.ReasoningEffort
		want    string
	}{
		{
			name: "provider default named",
			entry: llmapi.ModelEntry{
				Provider: anthropic.LLMProvider, Name: anthropic.ClaudeOpus5,
				ContextWindow: 1_000_000,
			},
			want: "high",
		},
		{
			name: "session override wins",
			entry: llmapi.ModelEntry{
				Provider: anthropic.LLMProvider, Name: anthropic.ClaudeOpus5,
			},
			session: llmapi.ReasoningEffortLow,
			want:    "low",
		},
		{
			name: "no published default",
			entry: llmapi.ModelEntry{
				Provider: "whatever", Name: "some-model",
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newStatusBarSyncComponent()
			t.Cleanup(func() { _ = s.comp.Close() })

			svc := llmtest.New([]llmapi.ModelEntry{tt.entry})
			fs := nopFileSystem{}
			ag := agent.NewAgent(svc, agent.NewRegistry(),
				skills.NewRegistry(fs, dirURI(""), nil, nil),
				newMemDialogueStore(), agent.NoMemory(),
				agent.Config{Model: tt.entry})
			ag.SetEffort(tt.session)

			a := &commandAdapter{agent: ag, statusBarFn: s.setStatusBarState}
			a.syncStatusBarModel()

			got := s.comp.StatusBarState()
			require.Equal(t, tt.entry.Name, got.Model)
			assert.Equal(t, tt.want, got.Effort)
		})
	}
}

// /compact runs through the agentshell rather than the agent's event
// stream, so it emits no EventCompacting. Declaring the phase on the
// command result is the only thing that moves the bar off IDLE for the
// duration of the summarisation call.
func TestHandleCompactReportsCompactingPhase(t *testing.T) {
	a := &commandAdapter{dialogueID: "d1"}
	res, err := a.handleCompact(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, phaseCompacting, res.Phase)
	assert.NotNil(t, res.Display)
}

// The three compaction paths must all reach the same status. Auto
// compaction and the compact tool report it through the run loop's
// events, so this drives a real agent whose model calls the compact
// tool and samples the bar while the summarisation call is in flight.
func TestAgentToolCompactReportsCompactingPhase(t *testing.T) {
	entry := llmapi.ModelEntry{
		Provider: "test", Name: "test-model", ContextWindow: 128_000,
	}
	svc := llmtest.New([]llmapi.ModelEntry{entry},
		llmtest.Response{ToolCalls: []llmapi.ToolCall{{
			ID:       "c1",
			Type:     llmapi.ToolTypeFunction,
			Function: llmapi.FunctionCall{Name: "compact", Arguments: "{}"},
		}}, FinishReason: llmapi.FinishReasonToolCall},
		llmtest.Response{Chunks: []string{"summary of the work so far"}},
		llmtest.Response{Chunks: []string{"continuing"}},
	)

	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })

	// The summarisation call is the one that happens between
	// EventCompacting and EventCompacted, so it is where the bar must
	// already read COMPACTING.
	var duringSummarize string
	svc.BeforeCompletion = func() {
		if svc.CallCount() != 1 {
			return
		}
		// The bar is moved by the event consumer, which runs
		// concurrently with this call, so sampling it outright would
		// race the phase it is about to reach.
		deadline := time.Now().Add(5 * time.Second)
		for {
			duringSummarize = s.comp.StatusBarState().Phase
			if duringSummarize == phaseCompacting || time.Now().After(deadline) {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}

	store := newMemDialogueStore()
	ag := agent.NewAgent(svc, agent.NewRegistry(compactStubTool{}),
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		store, agent.NoMemory(),
		agent.Config{SystemPrompt: "test", Model: entry, SessionKey: "d1"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tx := make(chan dialoguetui.MessageEvent, 64)
	rx := make(chan completionRequest, 1)
	rx <- completionRequest{displayText: "go", modelText: "go", ctx: ctx}
	go func() {
		for range tx { //nolint:revive
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		createAgentCompletions(ctx, cancel, tx, rx, ag, nopSpawner{},
			nil, skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
			"d1", s, stubNotifications{}, func(string) {}, store)
	}()

	require.Eventually(t, func() bool { return svc.CallCount() >= 3 },
		5*time.Second, 10*time.Millisecond)
	cancel()
	<-done

	assert.Equal(t, phaseCompacting, duringSummarize)
}

// compactStubTool stands in for agentools' compact tool, whose only
// relevant behaviour here is asking the run loop to compact.
type compactStubTool struct{}

func (compactStubTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type:     llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{Name: "compact"},
	}
}

func (compactStubTool) Execute(context.Context, string) agent.ToolResult {
	return agent.ToolResult{Content: "compacting", Compact: true}
}

func (compactStubTool) Summary(string) string         { return "" }
func (compactStubTool) NeedsDeterministicOrder() bool { return false }
