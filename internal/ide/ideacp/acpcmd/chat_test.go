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

package acpcmd

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

// fakeSession replays events, then ends with err unless it follows, in
// which case it waits for more until its viewer leaves.
type fakeSession struct {
	info      acpapi.SessionInfo
	events    []acpapi.Event
	err       error
	follow    bool
	promptErr error

	mu        sync.Mutex
	prompts   [][]acp.ContentBlock
	responses map[acpapi.PermissionID]acp.RequestPermissionOutcome
	configs   []acp.SetSessionConfigOptionRequest
	cancels   int
	closed    bool
}

func newFakeSession(events ...acpapi.Event) *fakeSession {
	for i := range events {
		events[i].Seq = i
	}
	return &fakeSession{
		info:      acpapi.SessionInfo{Name: "brave-fox", Agent: "claude", ID: "s1"},
		events:    events,
		responses: make(map[acpapi.PermissionID]acp.RequestPermissionOutcome),
	}
}

func (s *fakeSession) Info() acpapi.SessionInfo { return s.info }

func (s *fakeSession) Events() iterator.Iterator[acpapi.Event] {
	return &fakeEvents{s: s}
}

func (s *fakeSession) Prompt(_ context.Context, content []acp.ContentBlock) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, content)
	return s.promptErr
}

func (s *fakeSession) Cancel(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels++
	return nil
}

func (s *fakeSession) Respond(_ context.Context, id acpapi.PermissionID, o acp.RequestPermissionOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[id] = o
	return nil
}

func (s *fakeSession) SetConfigOption(_ context.Context, req acp.SetSessionConfigOptionRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configs = append(s.configs, req)
	return nil
}

func (s *fakeSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// sessionCalls is what a fakeSession was asked to do.
type sessionCalls struct {
	prompts   [][]acp.ContentBlock
	responses map[acpapi.PermissionID]acp.RequestPermissionOutcome
	configs   []acp.SetSessionConfigOptionRequest
	cancels   int
	closed    bool
}

func (s *fakeSession) snapshot() sessionCalls {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sessionCalls{
		prompts: slices.Clone(s.prompts), configs: slices.Clone(s.configs),
		cancels: s.cancels, closed: s.closed, responses: maps.Clone(s.responses),
	}
}

type fakeEvents struct {
	s *fakeSession
	i int
}

func (it *fakeEvents) Next(ctx context.Context) (acpapi.Event, bool) {
	if it.i < len(it.s.events) {
		it.i++
		return it.s.events[it.i-1], true
	}
	if it.s.follow {
		<-ctx.Done()
	}
	return acpapi.Event{}, false
}

func (it *fakeEvents) Err() error {
	if it.s.follow {
		return nil
	}
	return it.s.err
}

func (it *fakeEvents) Close() error { return nil }

// newTestChat starts a chat on what open returns, with the events it sends
// to the dialogue and the channel the dialogue submits on in the test's
// hands.
func newTestChat(
	t *testing.T, open func(context.Context) (acpapi.Session, error),
) (*chat, <-chan dialoguetui.MessageEvent, chan<- dialoguetui.SubmitMessage) {
	t.Helper()
	cfg := dialoguetui.DefaultComponentConfig()
	cfg.StatusBar = dialoguetui.DefaultStatusBarConfig()
	c := initChat(chatConfig{
		Name:        "brave-fox",
		Open:        open,
		Component:   cfg,
		Interrupter: term.NopInterrupter(),
		Root:        "/work",
	})
	dialogueTx := c.tx
	events := make(chan dialoguetui.MessageEvent, 256)
	submits := make(chan dialoguetui.SubmitMessage)
	c.tx, c.rx = events, submits
	c.start()
	t.Cleanup(func() {
		_ = c.content(nil).Close()
		close(dialogueTx)
	})
	return c, events, submits
}

func openSession(s acpapi.Session) func(context.Context) (acpapi.Session, error) {
	return func(context.Context) (acpapi.Session, error) { return s, nil }
}

// drain returns what the chat sent until it ended, without prompt result
// channels.
func drain(t *testing.T, events <-chan dialoguetui.MessageEvent) []dialoguetui.MessageEvent {
	t.Helper()
	var out []dialoguetui.MessageEvent
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return out
			}
			ev.PromptResult = nil
			out = append(out, ev)
		case <-timeout:
			t.Fatalf("chat did not end; got %+v", out)
		}
	}
}

// next returns the next event the chat sends of type typ.
func next(
	t *testing.T, events <-chan dialoguetui.MessageEvent, typ dialoguetui.MessageEventType,
) dialoguetui.MessageEvent {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			require.True(t, ok, "chat ended before sending event type %d", typ)
			if ev.Type == typ {
				return ev
			}
		case <-timeout:
			t.Fatalf("chat did not send event type %d", typ)
		}
	}
}

func update(u acp.SessionUpdate) acpapi.Event { return acpapi.Event{Update: &u} }

func prompt(text string) acpapi.Event {
	return acpapi.Event{Prompt: &acpapi.Prompt{Content: []acp.ContentBlock{acp.TextBlock(text)}}}
}

func done(r acp.StopReason) acpapi.Event {
	return acpapi.Event{Done: &acpapi.TurnEnd{StopReason: r}}
}

func permission(id acpapi.PermissionID, tool acp.ToolCallId, options ...acp.PermissionOption) acpapi.Event {
	return acpapi.Event{Permission: &acpapi.Permission{ID: id, Request: acp.RequestPermissionRequest{
		ToolCall: acp.ToolCallUpdate{ToolCallId: tool},
		Options:  options,
	}}}
}

func resolved(id acpapi.PermissionID, o acp.RequestPermissionOutcome) acpapi.Event {
	return acpapi.Event{Resolved: &acpapi.Resolution{ID: id, Outcome: o}}
}

var (
	allowOnce  = acp.PermissionOption{OptionId: "allow", Name: "Allow", Kind: acp.PermissionOptionKindAllowOnce}
	rejectOnce = acp.PermissionOption{OptionId: "reject", Name: "Reject", Kind: acp.PermissionOptionKindRejectOnce}
)

func selectOption(id, category, current string, values ...string) acp.SessionConfigOption {
	var opts acp.SessionConfigSelectOptionsUngrouped
	for i := 0; i+1 < len(values); i += 2 {
		opts = append(opts, acp.SessionConfigSelectOption{
			Value: acp.SessionConfigValueId(values[i]), Name: values[i+1],
		})
	}
	cat := acp.SessionConfigOptionCategory(category)
	return acp.SessionConfigOption{Select: &acp.SessionConfigOptionSelect{
		Id:           acp.SessionConfigId(id),
		Name:         id,
		Category:     &cat,
		CurrentValue: acp.SessionConfigValueId(current),
		Options:      acp.SessionConfigSelectOptions{Ungrouped: &opts},
	}}
}

var started = acpapi.Event{Started: &acpapi.Started{ConfigOptions: []acp.SessionConfigOption{
	selectOption("model", "model", "opus", "opus", "Opus 4", "sonnet", "Sonnet 4"),
	selectOption("effort", "thought_level", "high", "low", "Low", "high", "High"),
}}}

var (
	userEvent = func(text string, atts ...dialoguetui.Attachment) dialoguetui.MessageEvent {
		return dialoguetui.MessageEvent{Type: dialoguetui.MessageEventUserMessage, Text: text, Attachments: atts}
	}
	busy      = dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBusy, Busy: true}
	idle      = dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBusy}
	brk       = dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBreak}
	dismissed = dialoguetui.MessageEvent{Type: dialoguetui.MessageEventPromptDismiss}
	closed    = dialoguetui.MessageEvent{Type: dialoguetui.MessageEventWarning, Text: "Session closed."}
)

func TestChatEvents(t *testing.T) {
	t.Parallel()

	readTool := update(acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
		ToolCallId: "t1", Kind: acp.ToolKindRead, Title: "Read a.go",
		Status: acp.ToolCallStatusPending, RawInput: map[string]any{"path": "a.go"},
	}})
	runTool := update(acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
		ToolCallId: "t1", Kind: acp.ToolKindExecute, Title: "Run ls",
		Status: acp.ToolCallStatusPending, RawInput: map[string]any{"command": "ls"},
	}})
	runCall := dialoguetui.MessageEvent{
		Type: dialoguetui.MessageEventToolCall, ToolCallID: "t1", ToolName: "execute",
		ToolSummary: "Run ls", ToolArgs: `{"command":"ls"}`,
	}
	askRun := dialoguetui.MessageEvent{
		Type: dialoguetui.MessageEventPrompt, PromptTitle: "Run ls", PromptHeader: "Permission",
		PromptBody:    "```json\n{\n  \"command\": \"ls\"\n}\n```",
		PromptOptions: []dialoguetui.PromptEventOption{{Label: "Allow"}, {Label: "Reject"}},
	}
	allowed := acp.NewRequestPermissionOutcomeSelected("allow")
	subagentState := func(st acpapi.SubagentWorkState, reason acp.StopReason, err string) acpapi.Nullable[acpapi.SubagentState] {
		s := acpapi.SubagentState{State: st, StopReason: reason}
		if err != "" {
			s.Error = &acp.RequestError{Message: err}
		}
		return acpapi.Some(s)
	}
	text := func(s string) acpapi.Nullable[[]acp.ContentBlock] {
		return acpapi.Some([]acp.ContentBlock{acp.TextBlock(s)})
	}
	from := func(id acp.SessionId) acpapi.Nullable[acp.SessionId] { return acpapi.Some(id) }

	tests := []struct {
		name   string
		events []acpapi.Event
		err    error
		want   []dialoguetui.MessageEvent
		status func(*testing.T, dialoguetui.StatusBarState)
	}{
		{
			name: "turn",
			events: []acpapi.Event{
				started,
				{Prompt: &acpapi.Prompt{Content: []acp.ContentBlock{
					acp.TextBlock("hi"), acp.ResourceLinkBlock("a.go", "file:///work/a.go"),
				}}},
				update(acp.SessionUpdate{AgentThoughtChunk: &acp.SessionUpdateAgentThoughtChunk{
					Content: acp.TextBlock("hmm"),
				}}),
				update(acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.TextBlock("hello"),
				}}),
				readTool,
				update(acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{
					ToolCallId: "t1", Status: new(acp.ToolCallStatusCompleted),
					Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock("package a"))},
				}}),
				update(acp.SessionUpdate{Plan: &acp.SessionUpdatePlan{Entries: []acp.PlanEntry{
					{Content: "step", Status: acp.PlanEntryStatusInProgress},
				}}}),
				update(acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{Used: 10, Size: 100}}),
				done(acp.StopReasonEndTurn),
			},
			want: []dialoguetui.MessageEvent{
				userEvent("hi", dialoguetui.NewAttachment("/work/a.go")),
				busy,
				{Type: dialoguetui.MessageEventReasoning, Text: "hmm"},
				{Type: dialoguetui.MessageEventText, Text: "hello"},
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "t1", ToolName: "read",
					ToolSummary: "Read a.go", ToolArgs: `{"path":"a.go"}`,
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "t1", ToolName: "read",
					ToolSummary: "Read a.go", ToolArgs: `{"path":"a.go"}`, ToolOutput: "package a",
				},
				{
					Type: dialoguetui.MessageEventTaskProgress,
					TaskProgress: dialoguetui.ProgressTaskEntry{
						ID: "0", Subject: "step", Status: "in_progress",
					},
				},
				brk, idle, closed, idle,
			},
			status: func(t *testing.T, s dialoguetui.StatusBarState) {
				assert.Equal(t, "Opus 4", s.Model)
				assert.Equal(t, "High", s.Effort)
				assert.Equal(t, 10, s.ContextTokens)
				assert.Equal(t, 100, s.ContextWindow)
				assert.Equal(t, "brave-fox", s.Conversation)
				assert.False(t, s.Active)
			},
		},
		{
			name: "config option update",
			events: []acpapi.Event{
				started,
				update(acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{
					ConfigOptions: []acp.SessionConfigOption{
						selectOption("model", "model", "sonnet", "opus", "Opus 4", "sonnet", "Sonnet 4"),
					},
				}}),
			},
			want: []dialoguetui.MessageEvent{closed, idle},
			status: func(t *testing.T, s dialoguetui.StatusBarState) {
				assert.Equal(t, "Sonnet 4", s.Model)
				assert.Empty(t, s.Effort)
			},
		},
		{
			name: "replayed session",
			events: []acpapi.Event{
				update(acp.SessionUpdate{UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
					Content: acp.TextBlock("fix "),
				}}),
				update(acp.SessionUpdate{UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
					Content: acp.TextBlock("it"),
				}}),
				update(acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.TextBlock("done"),
				}}),
				update(acp.SessionUpdate{UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
					Content: acp.TextBlock("thanks"),
				}}),
			},
			want: []dialoguetui.MessageEvent{
				userEvent("fix it"),
				{Type: dialoguetui.MessageEventText, Text: "done"},
				userEvent("thanks"),
				closed, idle,
			},
		},
		{
			name: "user message echoed during a turn",
			events: []acpapi.Event{
				prompt("hi"),
				update(acp.SessionUpdate{UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
					Content: acp.TextBlock("hi"),
				}}),
				done(acp.StopReasonEndTurn),
			},
			want: []dialoguetui.MessageEvent{userEvent("hi"), busy, brk, idle, closed, idle},
		},
		{
			name: "permission resolved elsewhere",
			events: []acpapi.Event{
				prompt("go"),
				runTool,
				permission(2, "t1", allowOnce, rejectOnce),
				resolved(2, allowed),
				done(acp.StopReasonEndTurn),
			},
			want: []dialoguetui.MessageEvent{
				userEvent("go"), busy, runCall, askRun, dismissed, brk, idle, closed, idle,
			},
		},
		{
			name: "permissions asked one at a time",
			events: []acpapi.Event{
				prompt("go"),
				runTool,
				permission(2, "t1", allowOnce, rejectOnce),
				permission(3, "t1", allowOnce, rejectOnce),
				resolved(2, allowed),
				resolved(3, allowed),
				done(acp.StopReasonEndTurn),
			},
			want: []dialoguetui.MessageEvent{
				userEvent("go"), busy, runCall,
				askRun, dismissed, askRun, dismissed,
				brk, idle, closed, idle,
			},
		},
		{
			name: "permission pending when the turn ends",
			events: []acpapi.Event{
				prompt("go"),
				runTool,
				permission(2, "t1", allowOnce, rejectOnce),
				done(acp.StopReasonCancelled),
			},
			want: []dialoguetui.MessageEvent{
				userEvent("go"), busy, runCall, askRun, dismissed, brk,
				{Type: dialoguetui.MessageEventWarning, Text: "Cancelled."},
				idle, closed, idle,
			},
		},
		{
			name: "subagent",
			events: []acpapi.Event{
				prompt("go"),
				readTool,
				{Subagent: &acpapi.SubagentUpdate{
					SessionID: "s1", Title: acpapi.Some("List files"),
					Description: acpapi.Some("general-purpose"),
					State:       subagentState(acpapi.SubagentRunning, "", ""),
				}},
				{Message: &acpapi.SessionMessage{
					MessageID: "toolu_1", Sender: from("root"), Recipient: from("s1"),
					Content: text("List the files"),
				}},
				{Session: "s1", Message: &acpapi.SessionMessage{
					MessageID: "m1", Sender: from("root"), Recipient: from("s1"),
					Content: text("List the files"),
				}},
				{Session: "s1", Update: &acp.SessionUpdate{ToolCall: runTool.Update.ToolCall}},
				{Permission: &acpapi.Permission{ID: 2, Session: "s1", Request: acp.RequestPermissionRequest{
					ToolCall: acp.ToolCallUpdate{ToolCallId: "t1"},
					Options:  []acp.PermissionOption{allowOnce, rejectOnce},
				}}},
				resolved(2, allowed),
				{Session: "s1", Update: &acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{
					ToolCallId: "t1", Status: new(acp.ToolCallStatusCompleted),
					Content: []acp.ToolCallContent{acp.ToolContent(acp.TextBlock("a.go"))},
				}}},
				{Session: "s1", Update: &acp.SessionUpdate{UsageUpdate: &acp.SessionUsageUpdate{Used: 5, Size: 50}}},
				{Session: "s1", Update: &acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.TextBlock("Just a.go"),
				}}},
				{Subagent: &acpapi.SubagentUpdate{
					SessionID: "s1", State: subagentState(acpapi.SubagentIdle, acp.StopReasonEndTurn, ""),
				}},
				{Message: &acpapi.SessionMessage{
					MessageID: "toolu_1:reply", Sender: from("s1"), Recipient: from("root"),
					Content: text("Just a.go"),
				}},
				update(acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.TextBlock("One file"),
				}}),
				done(acp.StopReasonEndTurn),
			},
			want: []dialoguetui.MessageEvent{
				userEvent("go"), busy,
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "t1", ToolName: "read",
					ToolSummary: "Read a.go", ToolArgs: `{"path":"a.go"}`,
				},
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "subagent:s1", ToolName: "agent",
					ToolSummary: "List files", ToolArgs: `{"description":"general-purpose"}`,
				},
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "s1/t1", ParentToolCallID: "subagent:s1",
					ToolName: "execute", ToolSummary: "Run ls", ToolArgs: `{"command":"ls"}`,
				},
				{
					Type: dialoguetui.MessageEventPrompt, PromptTitle: "Run ls", PromptHeader: "Permission",
					PromptBody:    "Asked by subagent List files.\n\n" + askRun.PromptBody,
					PromptOptions: askRun.PromptOptions,
				},
				dismissed,
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "s1/t1", ParentToolCallID: "subagent:s1",
					ToolName: "execute", ToolSummary: "Run ls", ToolArgs: `{"command":"ls"}`,
					ToolOutput: "a.go",
				},
				{
					Type: dialoguetui.MessageEventChildResult, ParentToolCallID: "subagent:s1",
					ToolOutput: "Just a.go",
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "subagent:s1", ToolName: "agent",
					ToolSummary: "List files", ToolOutput: "Just a.go",
					ToolArgs: `{"description":"general-purpose","prompt":"List the files"}`,
				},
				{Type: dialoguetui.MessageEventText, Text: "One file"},
				brk, idle, closed, idle,
			},
			status: func(t *testing.T, s dialoguetui.StatusBarState) {
				assert.Zero(t, s.ContextTokens)
			},
		},
		{
			name: "nested subagent and runs",
			events: []acpapi.Event{
				{Subagent: &acpapi.SubagentUpdate{
					SessionID: "s1", Title: acpapi.Some("Explore"),
					State: subagentState(acpapi.SubagentRunning, "", ""),
				}},
				{Session: "s1", Subagent: &acpapi.SubagentUpdate{
					SessionID: "s2", Description: acpapi.Some("helper"),
					State: subagentState(acpapi.SubagentRunning, "", ""),
				}},
				{Session: "s2", Update: &acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
					ToolCallId: "t1", Kind: acp.ToolKindRead, Title: "Read a.go",
					Status: acp.ToolCallStatusPending,
				}}},
				{Session: "s1", Subagent: &acpapi.SubagentUpdate{
					SessionID: "s2", State: subagentState(acpapi.SubagentIdle, acpapi.StopReasonError, "boom"),
				}},
				{Subagent: &acpapi.SubagentUpdate{
					SessionID: "s1", State: subagentState(acpapi.SubagentIdle, acp.StopReasonCancelled, ""),
				}},
				{Subagent: &acpapi.SubagentUpdate{
					SessionID: "s1", State: subagentState(acpapi.SubagentRunning, "", ""),
				}},
				{Session: "s1", Update: &acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
					Content: acp.TextBlock("again"),
				}}},
				{Subagent: &acpapi.SubagentUpdate{
					SessionID: "s1", State: subagentState(acpapi.SubagentIdle, acp.StopReasonEndTurn, ""),
				}},
			},
			want: []dialoguetui.MessageEvent{
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "subagent:s1", ToolName: "agent",
					ToolSummary: "Explore",
				},
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "subagent:s2",
					ParentToolCallID: "subagent:s1", ToolName: "agent", ToolSummary: "helper",
					ToolArgs: `{"description":"helper"}`,
				},
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "s2/t1",
					ParentToolCallID: "subagent:s1", ToolName: "read", ToolSummary: "Read a.go",
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "subagent:s2",
					ParentToolCallID: "subagent:s1", ToolName: "agent", ToolSummary: "helper",
					ToolArgs: `{"description":"helper"}`, ToolOutput: "boom", IsError: true,
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "subagent:s1", ToolName: "agent",
					ToolSummary: "Explore",
				},
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "subagent:s1:2", ToolName: "agent",
					ToolSummary: "Explore",
				},
				{
					Type: dialoguetui.MessageEventChildResult, ParentToolCallID: "subagent:s1:2",
					ToolOutput: "again",
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "subagent:s1:2", ToolName: "agent",
					ToolSummary: "Explore", ToolOutput: "again",
				},
				closed, idle,
			},
		},
		{
			name: "permission from an unnamed subagent",
			events: []acpapi.Event{
				prompt("go"),
				{Subagent: &acpapi.SubagentUpdate{SessionID: "s1"}},
				{Permission: &acpapi.Permission{ID: 2, Session: "s1", Request: acp.RequestPermissionRequest{
					ToolCall: acp.ToolCallUpdate{ToolCallId: "t1"},
					Options:  []acp.PermissionOption{allowOnce},
				}}},
				resolved(2, allowed),
				done(acp.StopReasonEndTurn),
			},
			want: []dialoguetui.MessageEvent{
				userEvent("go"), busy,
				{Type: dialoguetui.MessageEventToolCall, ToolCallID: "subagent:s1", ToolName: "agent"},
				{
					Type: dialoguetui.MessageEventPrompt, PromptTitle: "Allow the agent to continue?",
					PromptHeader: "Permission", PromptBody: "Asked by a subagent.",
					PromptOptions: []dialoguetui.PromptEventOption{{Label: "Allow"}},
				},
				dismissed, brk, idle, closed, idle,
			},
		},
		{
			name:   "max tokens",
			events: []acpapi.Event{prompt("go"), done(acp.StopReasonMaxTokens)},
			want: []dialoguetui.MessageEvent{
				userEvent("go"), busy, brk,
				{Type: dialoguetui.MessageEventWarning, Text: "The agent ran out of tokens for this turn."},
				idle, closed, idle,
			},
		},
		{
			name: "failed turn",
			events: []acpapi.Event{
				prompt("go"),
				{Done: &acpapi.TurnEnd{Error: "agent claude exited"}},
			},
			want: []dialoguetui.MessageEvent{
				userEvent("go"), busy, brk,
				{Type: dialoguetui.MessageEventError, Text: "agent claude exited"},
				idle, closed, idle,
			},
		},
		{
			name: "agent exited",
			err:  errors.New("agent claude exited: exit status 1\nboom"),
			want: []dialoguetui.MessageEvent{
				{Type: dialoguetui.MessageEventError, Text: "agent claude exited: exit status 1\nboom"},
				idle,
			},
		},
		{
			name: "diff",
			events: []acpapi.Event{
				update(acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
					ToolCallId: "t1", Kind: acp.ToolKindEdit, Title: "Edit a.go",
					Status:  acp.ToolCallStatusFailed,
					Content: []acp.ToolCallContent{acp.ToolDiffContent("a.go", "b\n", "a\n")},
				}}),
			},
			want: []dialoguetui.MessageEvent{
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "t1", ToolName: "edit",
					ToolSummary: "Edit a.go",
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "t1", ToolName: "edit",
					ToolSummary: "Edit a.go", ToolOutput: "--- a.go\n+++ a.go\n-a\n+b", IsError: true,
				},
				closed, idle,
			},
		},
		{
			name: "tool call update before its call",
			events: []acpapi.Event{
				update(acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{
					ToolCallId: "t1", Title: new("Search"), RawOutput: map[string]any{"hits": 2},
					Status: new(acp.ToolCallStatusCompleted),
				}}),
			},
			want: []dialoguetui.MessageEvent{
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "t1", ToolName: "tool",
					ToolSummary: "Search",
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "t1", ToolName: "tool",
					ToolSummary: "Search", ToolOutput: `{"hits":2}`,
				},
				closed, idle,
			},
		},
		{
			name: "tool named by Rune Agent",
			events: []acpapi.Event{
				update(acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
					ToolCallId: "t1", Kind: acp.ToolKindSearch, Title: `find_files "*.go"`,
					Status: acp.ToolCallStatusCompleted, RawInput: map[string]any{"pattern": "*.go"},
					Meta: map[string]any{"runeAgent": map[string]any{
						"toolName": "find_files", "toolSummary": `"*.go"`,
					}},
				}}),
			},
			want: []dialoguetui.MessageEvent{
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "t1", ToolName: "find_files",
					ToolSummary: `"*.go"`, ToolArgs: `{"pattern":"*.go"}`,
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "t1", ToolName: "find_files",
					ToolSummary: `"*.go"`, ToolArgs: `{"pattern":"*.go"}`,
				},
				closed, idle,
			},
		},
		{
			name: "tool named by Claude Code",
			events: []acpapi.Event{
				update(acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
					ToolCallId: "t1", Kind: acp.ToolKindExecute, Title: "ls -la",
					Status: acp.ToolCallStatusPending, RawInput: map[string]any{"command": "ls -la"},
					Meta: map[string]any{"claudeCode": map[string]any{"toolName": "Bash"}},
				}}),
				update(acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{
					ToolCallId: "t1", Status: new(acp.ToolCallStatusCompleted),
					Meta: map[string]any{"claudeCode": map[string]any{"toolResponse": map[string]any{}}},
				}}),
			},
			want: []dialoguetui.MessageEvent{
				{
					Type: dialoguetui.MessageEventToolCall, ToolCallID: "t1", ToolName: "Bash",
					ToolArgs: `{"command":"ls -la"}`,
				},
				{
					Type: dialoguetui.MessageEventToolResult, ToolCallID: "t1", ToolName: "Bash",
					ToolArgs: `{"command":"ls -la"}`,
				},
				closed, idle,
			},
		},
		{
			name: "plan shrinks",
			events: []acpapi.Event{
				update(acp.SessionUpdate{Plan: &acp.SessionUpdatePlan{Entries: []acp.PlanEntry{
					{Content: "a", Status: acp.PlanEntryStatusCompleted},
					{Content: "b", Status: acp.PlanEntryStatusPending},
				}}}),
				update(acp.SessionUpdate{Plan: &acp.SessionUpdatePlan{Entries: []acp.PlanEntry{
					{Content: "a", Status: acp.PlanEntryStatusCompleted},
				}}}),
			},
			want: []dialoguetui.MessageEvent{
				{Type: dialoguetui.MessageEventTaskProgress, TaskProgress: dialoguetui.ProgressTaskEntry{
					ID: "0", Subject: "a", Status: "completed",
				}},
				{Type: dialoguetui.MessageEventTaskProgress, TaskProgress: dialoguetui.ProgressTaskEntry{
					ID: "1", Subject: "b", Status: "pending",
				}},
				{Type: dialoguetui.MessageEventTaskProgress, TaskProgress: dialoguetui.ProgressTaskEntry{
					ID: "0", Subject: "a", Status: "completed",
				}},
				{Type: dialoguetui.MessageEventTaskProgress, TaskProgress: dialoguetui.ProgressTaskEntry{
					ID: "1", Status: "deleted",
				}},
				closed, idle,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newFakeSession(tt.events...)
			s.err = tt.err
			c, events, _ := newTestChat(t, openSession(s))
			assert.Equal(t, tt.want, drain(t, events))
			if tt.status != nil {
				c.mu.Lock()
				state := c.comp.StatusBarState()
				c.mu.Unlock()
				tt.status(t, state)
			}
		})
	}
}

func TestChatStartFails(t *testing.T) {
	t.Parallel()
	_, events, _ := newTestChat(t, func(context.Context) (acpapi.Session, error) {
		return nil, errors.New("agent claude exited: exit status 127")
	})
	assert.Equal(t, []dialoguetui.MessageEvent{{
		Type: dialoguetui.MessageEventError, Text: "start session: agent claude exited: exit status 127",
	}}, drain(t, events))
}

func TestChatPhase(t *testing.T) {
	t.Parallel()

	tool := func(session acp.SessionId, id acp.ToolCallId) acpapi.Event {
		return acpapi.Event{Session: session, Update: &acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{
			ToolCallId: id, Kind: acp.ToolKindExecute, Status: acp.ToolCallStatusPending,
		}}}
	}
	finish := func(session acp.SessionId, id acp.ToolCallId, status acp.ToolCallStatus) acpapi.Event {
		return acpapi.Event{Session: session, Update: &acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{
			ToolCallId: id, Status: &status,
		}}}
	}
	subagent := func(state acpapi.SubagentWorkState) acpapi.Event {
		return acpapi.Event{Subagent: &acpapi.SubagentUpdate{
			SessionID: "s1", State: acpapi.Some(acpapi.SubagentState{State: state}),
		}}
	}
	allowed := acp.NewRequestPermissionOutcomeSelected("allow")

	tests := []struct {
		name   string
		events []acpapi.Event
		// last is the type of the last event the chat sends for events.
		last dialoguetui.MessageEventType
		want string
	}{
		{
			name:   "tool call running",
			events: []acpapi.Event{prompt("go"), tool("", "t1")},
			last:   dialoguetui.MessageEventToolCall,
			want:   phaseExecuting,
		},
		{
			name:   "tool call done",
			events: []acpapi.Event{prompt("go"), tool("", "t1"), finish("", "t1", acp.ToolCallStatusCompleted)},
			last:   dialoguetui.MessageEventToolResult,
			want:   phaseSending,
		},
		{
			name:   "tool call failed",
			events: []acpapi.Event{prompt("go"), tool("", "t1"), finish("", "t1", acp.ToolCallStatusFailed)},
			last:   dialoguetui.MessageEventToolResult,
			want:   phaseSending,
		},
		{
			name: "one of two tool calls done",
			events: []acpapi.Event{
				prompt("go"), tool("", "t1"), tool("", "t2"), finish("", "t1", acp.ToolCallStatusCompleted),
			},
			last: dialoguetui.MessageEventToolResult,
			want: phaseExecuting,
		},
		{
			name: "permission answered for a tool call yet to run",
			events: []acpapi.Event{
				prompt("go"), tool("", "t1"), permission(2, "t1", allowOnce), resolved(2, allowed),
			},
			last: dialoguetui.MessageEventPromptDismiss,
			want: phaseExecuting,
		},
		{
			name: "permission answered for a tool call that ended",
			events: []acpapi.Event{
				prompt("go"), tool("", "t1"), permission(2, "t1", allowOnce),
				finish("", "t1", acp.ToolCallStatusFailed), resolved(2, allowed),
			},
			last: dialoguetui.MessageEventPromptDismiss,
			want: phaseSending,
		},
		{
			name:   "subagent running",
			events: []acpapi.Event{prompt("go"), subagent(acpapi.SubagentRunning)},
			last:   dialoguetui.MessageEventToolCall,
			want:   phaseExecuting,
		},
		{
			name: "subagent tool call done",
			events: []acpapi.Event{
				prompt("go"), subagent(acpapi.SubagentRunning),
				tool("s1", "t1"), finish("s1", "t1", acp.ToolCallStatusCompleted),
			},
			last: dialoguetui.MessageEventToolResult,
			want: phaseExecuting,
		},
		{
			name: "subagent done",
			events: []acpapi.Event{
				prompt("go"), subagent(acpapi.SubagentRunning), subagent(acpapi.SubagentIdle),
			},
			last: dialoguetui.MessageEventToolResult,
			want: phaseSending,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newFakeSession(tt.events...)
			s.follow = true
			c, events, _ := newTestChat(t, openSession(s))
			next(t, events, tt.last)
			phase := func() string {
				c.mu.Lock()
				defer c.mu.Unlock()
				return c.comp.StatusBarState().Phase
			}
			require.Eventually(t, func() bool { return phase() == tt.want }, 2*time.Second, 10*time.Millisecond,
				"phase is %s", phase())
		})
	}
}

func TestChatPermissionAnswer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options []acp.PermissionOption
		// resolve resolves the permission elsewhere before it is answered.
		resolve bool
		picked  []string
		want    *acp.RequestPermissionOutcome
	}{
		{
			name:    "picked",
			options: []acp.PermissionOption{allowOnce, rejectOnce},
			picked:  []string{"Allow"},
			want:    new(acp.NewRequestPermissionOutcomeSelected("allow")),
		},
		{
			name:    "dismissed rejects once",
			options: []acp.PermissionOption{allowOnce, rejectOnce},
			want:    new(acp.NewRequestPermissionOutcomeSelected("reject")),
		},
		{
			name:    "dismissed without a way to reject",
			options: []acp.PermissionOption{allowOnce},
			want:    new(acp.NewRequestPermissionOutcomeCancelled()),
		},
		{
			name:    "resolved elsewhere",
			options: []acp.PermissionOption{allowOnce, rejectOnce},
			resolve: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			events := []acpapi.Event{prompt("go"), permission(1, "t1", tt.options...)}
			if tt.resolve {
				events = append(events, resolved(1, acp.NewRequestPermissionOutcomeCancelled()))
			}
			s := newFakeSession(events...)
			s.follow = true
			_, out, _ := newTestChat(t, openSession(s))
			ask := next(t, out, dialoguetui.MessageEventPrompt)
			if tt.resolve {
				next(t, out, dialoguetui.MessageEventPromptDismiss)
			}
			ask.PromptResult <- tt.picked
			if tt.want == nil {
				assert.Never(t, func() bool { return len(s.snapshot().responses) > 0 },
					100*time.Millisecond, 10*time.Millisecond)
				return
			}
			require.Eventually(t, func() bool { return len(s.snapshot().responses) > 0 },
				10*time.Second, 10*time.Millisecond)
			assert.Equal(t, map[acpapi.PermissionID]acp.RequestPermissionOutcome{1: *tt.want},
				s.snapshot().responses)
		})
	}
}

func TestChatSubmit(t *testing.T) {
	t.Parallel()

	msg := dialoguetui.SubmitMessage{
		Text: "look",
		Attachments: []dialoguetui.Attachment{
			dialoguetui.NewWorkspaceFileAttachment("a.go"),
			dialoguetui.NewAttachment("/tmp/b.png"),
		},
	}
	want := []acp.ContentBlock{
		acp.TextBlock("look"),
		acp.ResourceLinkBlock("a.go", "file:///work/a.go"),
		acp.ResourceLinkBlock("b.png", "file:///tmp/b.png"),
	}

	t.Run("prompts", func(t *testing.T) {
		t.Parallel()
		s := newFakeSession()
		s.follow = true
		_, _, submits := newTestChat(t, openSession(s))
		submits <- msg
		require.Eventually(t, func() bool { return len(s.snapshot().prompts) == 1 },
			10*time.Second, 10*time.Millisecond)
		assert.Equal(t, [][]acp.ContentBlock{want}, s.snapshot().prompts)
	})

	t.Run("failed prompt restores the draft", func(t *testing.T) {
		t.Parallel()
		s := newFakeSession()
		s.follow = true
		s.promptErr = acpapi.ErrBusy
		c, _, submits := newTestChat(t, openSession(s))
		submits <- msg
		require.Eventually(t, func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.comp.Draft().Text == "look"
		}, 10*time.Second, 10*time.Millisecond)
	})
}

func TestChatCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		args    []string
		want    dialoguetui.CommandResult
		wantErr string
		display bool
		check   func(*testing.T, sessionCalls)
	}{
		{
			name:    "set model by value",
			command: "model",
			args:    []string{"sonnet"},
			check: func(t *testing.T, s sessionCalls) {
				assert.Equal(t, []acp.SetSessionConfigOptionRequest{{
					ValueId: &acp.SetSessionConfigOptionValueId{ConfigId: "model", Value: "sonnet"},
				}}, s.configs)
			},
		},
		{
			name:    "set effort by name",
			command: "effort",
			args:    []string{"LOW"},
			check: func(t *testing.T, s sessionCalls) {
				assert.Equal(t, []acp.SetSessionConfigOptionRequest{{
					ValueId: &acp.SetSessionConfigOptionValueId{ConfigId: "effort", Value: "low"},
				}}, s.configs)
			},
		},
		{
			name:    "unknown model",
			command: "model",
			args:    []string{"gpt"},
			wantErr: `unknown model "gpt", choose one of: opus, sonnet`,
		},
		{
			name:    "list models",
			command: "model",
			display: true,
		},
		{
			name:    "no mode option",
			command: "mode",
			args:    []string{"plan"},
			wantErr: "claude does not offer a mode option",
		},
		{
			name:    "close",
			command: "close",
			check: func(t *testing.T, s sessionCalls) {
				assert.True(t, s.closed)
			},
		},
		{
			name:    "agent command",
			command: "compact",
			args:    []string{"now"},
			want:    dialoguetui.CommandResult{UserMessage: "/compact now"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := newFakeSession(started)
			s.follow = true
			c, _, _ := newTestChat(t, openSession(s))
			require.Eventually(t, func() bool { return len(c.sessionState().ConfigOptions) > 0 },
				10*time.Second, 10*time.Millisecond)
			got, err := c.HandleCommand(context.Background(), tt.command, tt.args)
			if tt.wantErr != "" {
				assert.EqualError(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.display {
				assert.NotNil(t, got.Display)
				return
			}
			assert.Equal(t, tt.want, got)
			if tt.check != nil {
				tt.check(t, s.snapshot())
			}
		})
	}
}

func TestChatComplete(t *testing.T) {
	t.Parallel()
	s := newFakeSession(update(acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
		AvailableCommands: []acp.AvailableCommand{{Name: "compact"}, {Name: "mcp"}},
	}}))
	s.follow = true
	c, _, _ := newTestChat(t, openSession(s))
	require.Eventually(t, func() bool { return len(c.sessionState().Commands) > 0 },
		10*time.Second, 10*time.Millisecond)

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "all", want: []string{"close", "compact", "effort", "mcp", "mode", "model"}},
		{name: "prefix", args: []string{"m"}, want: []string{"mcp", "mode", "model"}},
		{name: "arguments", args: []string{"model", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name := ""
			var args []string
			if len(tt.args) > 0 {
				name, args = tt.args[0], tt.args[1:]
			}
			it, err := c.Complete(context.Background(), name, args)
			require.NoError(t, err)
			got, err := collect(context.Background(), it)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestChatCancel(t *testing.T) {
	t.Parallel()
	ctrlC := term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'c'}

	t.Run("busy", func(t *testing.T) {
		t.Parallel()
		s := newFakeSession(prompt("go"))
		s.follow = true
		c, _, _ := newTestChat(t, openSession(s))
		require.Eventually(t, c.busy, 10*time.Second, 10*time.Millisecond)
		_, handled := c.handle(ctrlC)
		assert.True(t, handled)
		require.Eventually(t, func() bool { return s.snapshot().cancels == 1 },
			10*time.Second, 10*time.Millisecond)
	})

	t.Run("idle", func(t *testing.T) {
		t.Parallel()
		s := newFakeSession(prompt("go"), done(acp.StopReasonEndTurn))
		s.follow = true
		c, events, _ := newTestChat(t, openSession(s))
		next(t, events, dialoguetui.MessageEventBreak)
		require.Eventually(t, func() bool { return !c.busy() }, 10*time.Second, 10*time.Millisecond)
		_, handled := c.handle(ctrlC)
		assert.True(t, handled)
		assert.Never(t, func() bool { return s.snapshot().cancels > 0 },
			100*time.Millisecond, 10*time.Millisecond)
	})
}

func TestUnifiedDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		old, new string
		want     string
	}{
		{
			name: "new file",
			new:  "a\nb\n",
			want: "--- f\n+++ f\n+a\n+b",
		},
		{
			name: "context",
			old:  "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n",
			new:  "1\n2\n3\n4\n5\nfive\n7\n8\n9\n10\n",
			want: "--- f\n+++ f\n@@\n 3\n 4\n 5\n-6\n+five\n 7\n 8\n 9\n@@",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, unifiedDiff("f", tt.old, tt.new))
		})
	}
}
