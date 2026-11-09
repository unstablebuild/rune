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
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
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
	h := &aiEditorHandler{n: stubNotifications{}}
	return syncComponent{
		mu:   new(sync.Mutex),
		comp: comp,
		h:    h,
		tab:  newTabStatusReporter(nil, workspaceapi.URI{}, h.n),
	}
}

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

func TestTurnReportsChatTabStatus(t *testing.T) {
	wm := &recordingWindowManager{}
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	uri, err := getModelUri("rolling-fox", "gpt-5")
	require.NoError(t, err)
	s.tab = newTabStatusReporter(wm, uri, s.h.n)

	s.beginTurn(time.Now())
	assert.Equal(t, uri, wm.statusURI)
	assert.Equal(t, []browserapi.TabStatus{
		{State: browserapi.TabClear, Progress: -1},
		{State: browserapi.TabWorking, Progress: -1, Title: statusTitleSending},
	}, wm.tabStatuses())
	s.endTurn()
	assert.Len(t, wm.tabStatuses(), 2, "the outcome, not the end of the turn, settles the status")
}

func TestTurnSurvivesTabStatusErrors(t *testing.T) {
	wm := &recordingWindowManager{statusErr: errors.New("unknown tab")}
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	uri, err := getModelUri("rolling-fox", "gpt-5")
	require.NoError(t, err)
	s.tab = newTabStatusReporter(wm, uri, s.h.n)

	s.beginTurn(time.Now())
	assert.True(t, s.comp.StatusBarState().Active)
	s.endTurn()
	assert.False(t, s.comp.StatusBarState().Active)
	assert.Len(t, wm.tabStatuses(), 2)
}

func TestTurnWithoutTabSkipsStatus(t *testing.T) {
	wm := &recordingWindowManager{}
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	s.tab = newTabStatusReporter(wm, workspaceapi.URI{}, s.h.n)

	s.beginTurn(time.Now())
	s.endTurn()
	assert.Empty(t, wm.tabStatuses())
}

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

func TestHandleCompactReportsCompactingPhase(t *testing.T) {
	a := &commandAdapter{dialogueID: "d1"}
	res, err := a.handleCompact(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, phaseCompacting, res.Phase)
	assert.NotNil(t, res.Display)
}

func TestAgentToolCompactReportsCompactingPhase(t *testing.T) {
	tests := []struct {
		name        string
		onCompacted func(string)
	}{
		{name: "chat", onCompacted: func(string) {}},
		{name: "query without onCompacted", onCompacted: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testAgentToolCompactReportsCompactingPhase(t, tc.onCompacted)
		})
	}
}

func testAgentToolCompactReportsCompactingPhase(t *testing.T, onCompacted func(string)) {
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
			"d1", s, onCompacted, store)
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

// summaryStubTool is a tool whose summary the chat's status reports
// while it runs.
type summaryStubTool struct{}

func (summaryStubTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type:     llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{Name: "bash"},
	}
}

func (summaryStubTool) Execute(context.Context, string) agent.ToolResult {
	return agent.ToolResult{Content: "ok"}
}

func (summaryStubTool) Summary(string) string         { return "go test ./..." }
func (summaryStubTool) NeedsDeterministicOrder() bool { return false }

func runTurnForStatus(
	t *testing.T, svc llmapi.Service, entry llmapi.ModelEntry, registry *agent.Registry,
	turns int, cancelAfterCalls int,
) ([]browserapi.TabStatus, workspaceapi.URI) {
	t.Helper()
	wm := &recordingWindowManager{}
	s := newStatusBarSyncComponent()
	t.Cleanup(func() { _ = s.comp.Close() })
	uri, err := getModelUri("rolling-fox", "gpt-5")
	require.NoError(t, err)
	s.tab = newTabStatusReporter(wm, uri, s.h.n)

	store := newMemDialogueStore()
	ag := agent.NewAgent(svc, registry,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		store, agent.NoMemory(),
		agent.Config{SystemPrompt: "test", Model: entry, SessionKey: "d1"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	tx := make(chan dialoguetui.MessageEvent, 64)
	rx := make(chan completionRequest, turns)
	for range turns {
		rx <- completionRequest{displayText: "go", modelText: "go", ctx: ctx}
	}
	settled := make(chan struct{})
	go func() {
		idle := 0
		for ev := range tx {
			if ev.Type == dialoguetui.MessageEventBusy && !ev.Busy {
				idle++
				if idle == turns {
					close(settled)
				}
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		createAgentCompletions(ctx, cancel, tx, rx, ag, nopSpawner{},
			nil, skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
			"d1", s, nil, store)
	}()
	if cancelAfterCalls > 0 {
		require.Eventually(t, func() bool {
			return svc.(*llmtest.Service).CallCount() >= cancelAfterCalls
		}, 5*time.Second, time.Millisecond)
		cancel()
	} else {
		select {
		case <-settled:
		case <-time.After(5 * time.Second):
			t.Fatal("turns did not settle")
		}
		cancel()
	}
	<-done
	return wm.tabStatuses(), uri
}

func TestTurnStatusFollowsEvents(t *testing.T) {
	entry := llmapi.ModelEntry{Provider: "test", Name: "test-model", ContextWindow: 128_000}

	t.Run("tool round then reply", func(t *testing.T) {
		svc := llmtest.New([]llmapi.ModelEntry{entry},
			llmtest.Response{ToolCalls: []llmapi.ToolCall{{
				ID: "c1", Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{Name: "bash", Arguments: "{}"},
			}}, FinishReason: llmapi.FinishReasonToolCall},
			llmtest.Response{Chunks: []string{"**Fixed** the parser bug.\n\nTwo files changed."}},
		)
		statuses, _ := runTurnForStatus(t, svc, entry, agent.NewRegistry(summaryStubTool{}), 1, 0)
		assert.Equal(t, []browserapi.TabStatus{
			{State: browserapi.TabClear, Progress: -1},
			{State: browserapi.TabWorking, Progress: -1, Title: statusTitleSending},
			{State: browserapi.TabWorking, Progress: -1, Title: statusTitleReasoning},
			{State: browserapi.TabWorking, Progress: -1, Title: statusTitleTools},
			{State: browserapi.TabWorking, Progress: -1, Title: "bash: go test ./..."},
			{State: browserapi.TabWorking, Progress: -1, Title: statusTitleSending},
			{State: browserapi.TabWorking, Progress: -1, Title: statusTitleReasoning},
			{State: browserapi.TabWorking, Progress: -1, Title: statusTitleWriting},
			{State: browserapi.TabClear, Progress: -1},
			{State: browserapi.TabDone, Progress: -1, Message: "Fixed the parser bug."},
		}, statuses)
	})

	t.Run("a reply without text is summarized by a fixed message", func(t *testing.T) {
		svc := llmtest.New([]llmapi.ModelEntry{entry}, llmtest.Response{Chunks: []string{"  \n"}})
		statuses, _ := runTurnForStatus(t, svc, entry, agent.NewRegistry(), 1, 0)
		assert.Equal(t, browserapi.TabStatus{State: browserapi.TabDone, Progress: -1, Message: statusMsgCompleted},
			statuses[len(statuses)-1])
	})

	t.Run("a failed completion is an error", func(t *testing.T) {
		svc := llmtest.New([]llmapi.ModelEntry{entry}, llmtest.Response{Err: errors.New("boom")})
		statuses, _ := runTurnForStatus(t, svc, entry, agent.NewRegistry(), 1, 0)
		last := statuses[len(statuses)-1]
		assert.Equal(t, browserapi.TabError, last.State)
		assert.Contains(t, last.Message, statusMsgFailed)
		assert.Contains(t, last.Message, "boom")
	})

	t.Run("a truncated reply is an error", func(t *testing.T) {
		svc := llmtest.New([]llmapi.ModelEntry{entry},
			llmtest.Response{Chunks: []string{"cut"}, FinishReason: llmapi.FinishReasonLength})
		statuses, _ := runTurnForStatus(t, svc, entry, agent.NewRegistry(), 1, 0)
		assert.Equal(t, browserapi.TabStatus{State: browserapi.TabError, Progress: -1, Message: statusMsgTruncated},
			statuses[len(statuses)-1])
	})

	t.Run("a rate limit is told as it happens", func(t *testing.T) {
		svc := llmtest.New([]llmapi.ModelEntry{entry}, llmtest.Response{
			Chunks:            []string{"done"},
			RateLimitWarnings: []*llmapi.RateLimitInfo{{Message: "Rate limit reached, retrying"}},
		})
		statuses, _ := runTurnForStatus(t, svc, entry, agent.NewRegistry(), 1, 0)
		assert.Contains(t, statuses, browserapi.TabStatus{
			State: browserapi.TabWorking, Progress: -1, Title: statusTitleRateLimit,
			Message: "Rate limit reached, retrying",
		})
	})

	t.Run("a second turn starts over", func(t *testing.T) {
		svc := llmtest.New([]llmapi.ModelEntry{entry},
			llmtest.Response{Chunks: []string{"first"}},
			llmtest.Response{Chunks: []string{"second"}},
		)
		statuses, _ := runTurnForStatus(t, svc, entry, agent.NewRegistry(), 2, 0)
		var dones []string
		for _, st := range statuses {
			if st.State == browserapi.TabDone {
				dones = append(dones, st.Message)
			}
		}
		assert.Equal(t, []string{"first", "second"}, dones)
	})
}
