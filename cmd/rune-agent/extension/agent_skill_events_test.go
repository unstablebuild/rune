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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

// scriptedEvents replays events and then reports err.
type scriptedEvents struct {
	iterator.Iterator[agent.Event]
	err error
}

func (s scriptedEvents) Err() error { return s.err }

func TestAgentSkillEvents(t *testing.T) {
	const parent = "skill-sub-agent-research-1"
	call := agent.Event{
		Type: agent.EventToolCall, ToolCallID: parent,
		ToolName: "skill", ToolArgs: `{"name":"research"}`, ToolSummary: "research",
	}
	result := func(output string, isError bool) agent.Event {
		ev := call
		ev.Type, ev.ToolArgs, ev.ToolOutput, ev.IsError = agent.EventToolResult, "", output, isError
		return ev
	}
	read := agent.Event{
		Type: agent.EventToolCall, ToolCallID: "c1",
		ToolName: "read_file", ToolArgs: `{"path":"a.go"}`, ToolSummary: "a.go",
	}
	readDone := agent.Event{
		Type: agent.EventToolResult, ToolCallID: "c1",
		ToolName: "read_file", ToolArgs: `{"path":"a.go"}`, ToolSummary: "a.go", ToolOutput: "package a",
	}
	nestedRead := dialoguetui.MessageEvent{
		Type: dialoguetui.MessageEventToolCall, ToolCallID: "c1",
		ToolName: "read_file", ToolArgs: `{"path":"a.go"}`, ToolSummary: "a.go", ParentToolCallID: parent,
	}
	nestedReadDone := dialoguetui.MessageEvent{
		Type: dialoguetui.MessageEventToolResult, ToolCallID: "c1",
		ToolName: "read_file", ToolArgs: `{"path":"a.go"}`, ToolSummary: "a.go", ToolOutput: "package a",
		ParentToolCallID: parent,
	}
	stop := agent.Event{Type: agent.EventDone, FinishReason: llmapi.FinishReasonStop}
	inference := agent.Event{Type: agent.EventInferenceStart}
	failure := agent.Event{Type: agent.EventError, Error: errors.New("provider down")}
	started := func(end ...agent.Event) []agent.Event {
		return append([]agent.Event{{Type: agent.EventToolsStart}, call}, end...)
	}
	// shown returns the result row of the sub-agent that output output.
	shown := func(output string, isError bool) dialoguetui.MessageEvent {
		return dialoguetui.MessageEvent{
			Type: dialoguetui.MessageEventChildResult, ParentToolCallID: parent, ToolOutput: output, IsError: isError,
		}
	}
	saveErr := errors.New("disk full")
	// saved returns the turn recorded for a skill call that output
	// output, followed by end.
	saved := func(output string, end ...llmapi.Message) []llmapi.Message {
		return append([]llmapi.Message{
			{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{{
				ID: parent, Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{Name: "skill", Arguments: `{"name":"research"}`},
			}}},
			{Role: llmapi.RoleTool, Content: output, Name: "skill", ToolCallID: parent},
		}, end...)
	}
	replyEvents := []agent.Event{
		inference, {Type: agent.EventText, Text: "Let me look."},
		{Type: agent.EventToolsStart}, read, readDone,
		{Type: agent.EventUsageUpdate},
		inference, {Type: agent.EventReasoning, Reasoning: "It is a package."},
		{Type: agent.EventText, Text: "It declares "}, {Type: agent.EventText, Text: "package a."},
		stop,
	}

	tests := []struct {
		name       string
		events     []agent.Event
		err        error
		canceled   bool
		saveErr    error
		wantNested []dialoguetui.MessageEvent
		want       []agent.Event
		wantSaved  []llmapi.Message
	}{
		{
			name:       "reply after tool calls",
			events:     replyEvents,
			wantNested: []dialoguetui.MessageEvent{nestedRead, nestedReadDone, shown("It declares package a.", false)},
			want: started(result("It declares package a.", false),
				agent.Event{Type: agent.EventText, Text: "It declares package a."}, stop),
			wantSaved: saved("It declares package a.",
				llmapi.Message{Role: llmapi.RoleAssistant, Content: "It declares package a."}),
		},
		{
			name:       "reply only in reasoning",
			events:     []agent.Event{inference, {Type: agent.EventReasoning, Reasoning: "Nothing to do."}, stop},
			wantNested: []dialoguetui.MessageEvent{shown("Nothing to do.", false)},
			want: started(result("Nothing to do.", false),
				agent.Event{Type: agent.EventReasoning, Reasoning: "Nothing to do."}, stop),
			wantSaved: saved("Nothing to do.",
				llmapi.Message{Role: llmapi.RoleAssistant, ReasoningContent: "Nothing to do."}),
		},
		{
			name:       "no reply",
			events:     []agent.Event{inference, stop},
			wantNested: []dialoguetui.MessageEvent{shown("", false)},
			want:       started(result("", false), stop),
			wantSaved:  saved(""),
		},
		{
			name:       "error",
			events:     []agent.Event{inference, read, failure},
			wantNested: []dialoguetui.MessageEvent{nestedRead, shown("provider down", true)},
			want:       started(result("provider down", true), failure),
			wantSaved:  saved("provider down"),
		},
		{
			name:       "stream error",
			events:     []agent.Event{inference},
			err:        errors.New("stream: reset"),
			wantNested: []dialoguetui.MessageEvent{shown("stream: reset", true)},
			want:       started(result("stream: reset", true)),
			wantSaved:  saved("stream: reset"),
		},
		{
			name: "refusal",
			events: []agent.Event{
				inference, {Type: agent.EventRefusal, FinishReason: llmapi.FinishReasonRefusal},
			},
			wantNested: []dialoguetui.MessageEvent{shown("", false)},
			want: started(result("", false),
				agent.Event{Type: agent.EventRefusal, FinishReason: llmapi.FinishReasonRefusal}),
			wantSaved: saved(""),
		},
		{
			name:      "canceled",
			events:    []agent.Event{inference, {Type: agent.EventText, Text: "partial"}},
			err:       context.Canceled,
			canceled:  true,
			want:      started(result("sub-agent cancelled", true)),
			wantSaved: saved("sub-agent cancelled"),
		},
		{
			name:       "turn not saved",
			events:     replyEvents,
			saveErr:    saveErr,
			wantNested: []dialoguetui.MessageEvent{nestedRead, nestedReadDone, shown("It declares package a.", false)},
			want: started(result("It declares package a.", false),
				agent.Event{Type: agent.EventText, Text: "It declares package a."},
				agent.Event{Type: agent.EventError, Error: fmt.Errorf("persist messages: %w", saveErr)},
				stop),
			wantSaved: saved("It declares package a.",
				llmapi.Message{Role: llmapi.RoleAssistant, Content: "It declares package a."}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			tx := make(chan dialoguetui.MessageEvent, 16)
			if tt.canceled {
				cancel()
				// Nothing reads the chat once its turn is canceled.
				tx = make(chan dialoguetui.MessageEvent)
			}
			tab := newTabStatusReporter(nil, workspaceapi.URI{}, stubNotifications{})
			var gotSaved []llmapi.Message
			saves := 0
			record := func(_ context.Context, replies []llmapi.Message) error {
				saves++
				gotSaved = replies
				return tt.saveErr
			}
			it := newAgentSkillEvents(skills.Skill{Name: "research"}, agent.RunHandle{
				DialogueID: "sub-agent-research-1",
				Events:     scriptedEvents{Iterator: iterator.FromSlice(tt.events), err: tt.err},
			}, tx, tab, record)
			defer func() { require.NoError(t, it.Close()) }()

			var got []agent.Event
			for {
				ev, ok := it.Next(ctx)
				if !ok {
					break
				}
				if ev.Type == agent.EventToolCall {
					assert.False(t, ev.ToolStartTime.IsZero(), "the skill call must be timed")
				}
				if ev.Type == agent.EventToolResult {
					assert.Positive(t, ev.ToolDuration, "the skill result must report its duration")
				}
				ev.ToolStartTime, ev.ToolDuration = time.Time{}, 0
				got = append(got, ev)
			}
			_, ok := it.Next(ctx)
			assert.False(t, ok, "a finished run must stay finished")
			close(tx)
			var nested []dialoguetui.MessageEvent
			for msg := range tx {
				nested = append(nested, msg)
			}
			assert.Equal(t, tt.want, got)
			assert.Equal(t, tt.wantNested, nested)
			assert.Equal(t, tt.err, it.Err())
			assert.Equal(t, 1, saves, "the turn must be saved once")
			assert.Equal(t, tt.wantSaved, gotSaved)
		})
	}
}

func TestAgentSkillToolCallID(t *testing.T) {
	tests := []struct {
		dialogueID string
		want       string
	}{
		{dialogueID: "sub-agent-research-many-terrier", want: "skill-sub-agent-research-many-terrier"},
		{dialogueID: "sub-agent-code_search-2", want: "skill-sub-agent-code_search-2"},
		// Skill names that break the naming advice still load, but
		// providers reject tool call IDs with such characters.
		{dialogueID: "sub-agent-My Skill.v2-ox", want: "skill-sub-agent-My_Skill_v2-ox"},
		{dialogueID: "sub-agent-résumé-ox", want: "skill-sub-agent-r_sum_-ox"},
	}
	for _, tc := range tests {
		t.Run(tc.dialogueID, func(t *testing.T) {
			it := newAgentSkillEvents(skills.Skill{Name: "research"}, agent.RunHandle{
				DialogueID: tc.dialogueID, Events: iterator.Empty[agent.Event](),
			}, nil, nil, nil)
			assert.Equal(t, tc.want, it.call.ToolCallID)
		})
	}
}
