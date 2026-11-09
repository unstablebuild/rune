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
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

type agentSkillEvents struct {
	inner iterator.Iterator[agent.Event]
	tx    chan<- dialoguetui.MessageEvent
	tab   *tabStatusReporter
	// record saves the replies to the user's message.
	record func(ctx context.Context, replies []llmapi.Message) error
	call   agent.Event
	// pending are the events Next returns before reading inner again.
	pending []agent.Event
	// end are the sub-agent's terminal events, held back until the
	// result of call.
	end []agent.Event
	// reply and reasoning hold the output of the latest inference.
	reply, reasoning strings.Builder
	lastErr          error
	done             bool
}

func newAgentSkillEvents(
	skill skills.Skill, handle agent.RunHandle,
	tx chan<- dialoguetui.MessageEvent, tab *tabStatusReporter,
	record func(ctx context.Context, replies []llmapi.Message) error,
) *agentSkillEvents {
	args, _ := json.Marshal(map[string]string{"name": skill.Name})
	call := agent.Event{
		Type:          agent.EventToolCall,
		ToolCallID:    skillToolCallID(handle.DialogueID),
		ToolName:      "skill",
		ToolArgs:      string(args),
		ToolSummary:   skill.Name,
		ToolStartTime: time.Now(),
	}
	return &agentSkillEvents{
		inner:   handle.Events,
		tx:      tx,
		tab:     tab,
		record:  record,
		call:    call,
		pending: []agent.Event{{Type: agent.EventToolsStart}, call},
	}
}

func skillToolCallID(dialogueID string) string {
	return "skill-" + strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, dialogueID)
}

func (e *agentSkillEvents) Next(ctx context.Context) (agent.Event, bool) {
	for len(e.pending) == 0 {
		if e.done {
			return agent.Event{}, false
		}
		ev, ok := e.inner.Next(ctx)
		if !ok {
			e.finish(ctx)
			continue
		}
		e.observe(ctx, ev)
	}
	ev := e.pending[0]
	e.pending = e.pending[1:]
	return ev, true
}

func (e *agentSkillEvents) Err() error { return e.inner.Err() }

func (e *agentSkillEvents) Close() error { return e.inner.Close() }

func (e *agentSkillEvents) observe(ctx context.Context, ev agent.Event) {
	switch ev.Type {
	case agent.EventInferenceStart:
		e.reply.Reset()
		e.reasoning.Reset()
	case agent.EventText:
		e.reply.WriteString(ev.Text)
	case agent.EventReasoning:
		e.reasoning.WriteString(ev.Reasoning)
	case agent.EventToolCall:
		e.tab.childToolCall(e.call.ToolCallID, ev.ToolName, ev.ToolSummary)
		e.send(ctx, childToolMessage(e.call.ToolCallID, ev))
	case agent.EventToolResult:
		e.send(ctx, childToolMessage(e.call.ToolCallID, ev))
	case agent.EventToolsDropped:
		e.send(ctx, dialoguetui.MessageEvent{
			Type:               dialoguetui.MessageEventToolsDropped,
			DroppedToolCallIDs: ev.DroppedToolCallIDs,
		})
	case agent.EventError:
		if ev.Error != nil {
			e.lastErr = ev.Error
		}
		e.end = append(e.end, ev)
	case agent.EventDone, agent.EventRefusal:
		e.end = append(e.end, ev)
	}
}

func (e *agentSkillEvents) finish(ctx context.Context) {
	e.done = true
	result := e.call
	result.Type = agent.EventToolResult
	result.ToolArgs, result.ToolStartTime = "", time.Time{}
	result.ToolDuration = time.Since(e.call.ToolStartTime)
	result.ToolOutput = cmp.Or(e.reply.String(), e.reasoning.String())
	switch {
	case ctx.Err() != nil:
		result.ToolOutput, result.IsError = "sub-agent cancelled", true
	case result.ToolOutput != "":
	case e.lastErr != nil:
		result.ToolOutput, result.IsError = e.lastErr.Error(), true
	case e.inner.Err() != nil:
		result.ToolOutput, result.IsError = e.inner.Err().Error(), true
	}
	e.send(ctx, dialoguetui.MessageEvent{
		Type:             dialoguetui.MessageEventChildResult,
		ParentToolCallID: e.call.ToolCallID,
		ToolOutput:       result.ToolOutput,
		IsError:          result.IsError,
	})
	e.pending = append(e.pending, result)
	replies := []llmapi.Message{
		{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{{
			ID: e.call.ToolCallID, Type: llmapi.ToolTypeFunction,
			Function: llmapi.FunctionCall{Name: e.call.ToolName, Arguments: e.call.ToolArgs},
		}}},
		{Role: llmapi.RoleTool, Content: result.ToolOutput, Name: e.call.ToolName, ToolCallID: e.call.ToolCallID},
	}
	switch {
	case result.IsError:
	case e.reply.Len() > 0:
		e.pending = append(e.pending, agent.Event{Type: agent.EventText, Text: e.reply.String()})
		replies = append(replies, llmapi.Message{Role: llmapi.RoleAssistant, Content: e.reply.String()})
	case e.reasoning.Len() > 0:
		e.pending = append(e.pending, agent.Event{Type: agent.EventReasoning, Reasoning: e.reasoning.String()})
		replies = append(replies, llmapi.Message{Role: llmapi.RoleAssistant, ReasoningContent: e.reasoning.String()})
	}
	if err := e.record(ctx, replies); err != nil {
		e.pending = append(e.pending, agent.Event{Type: agent.EventError, Error: fmt.Errorf("persist messages: %w", err)})
	}
	e.pending = append(e.pending, e.end...)
}

func (e *agentSkillEvents) send(ctx context.Context, msg dialoguetui.MessageEvent) {
	select {
	case e.tx <- msg:
	case <-ctx.Done():
	}
}
