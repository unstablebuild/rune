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

package agentools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
	"unstable.build/rune/cmd/rune-agent/hooks"
)

// NewAgentTool creates an "agent" tool backed by the given
// Spawner. The agents slice is used to populate the tool
// description with available agent types. skillRegistry, when
// non-nil, adds agent-type skills to the description so the
// LLM can spawn them via subagent_type. childEvents receives
// sub-agent events tagged with the parent tool call ID for
// TUI rendering.
func NewAgentTool(
	spawner agent.Spawner, agents []agent.AgentSummary,
	childEvents chan<- agent.ChildEvent,
	skillRegistry *skills.SkillRegistry,
) agent.Tool {
	return &agentTool{
		spawner:       spawner,
		agents:        agents,
		childEvents:   childEvents,
		skillRegistry: skillRegistry,
	}
}

type agentTool struct {
	spawner       agent.Spawner
	agents        []agent.AgentSummary
	childEvents   chan<- agent.ChildEvent
	skillRegistry *skills.SkillRegistry
	// policy hides and forbids the sub-agent types the agent may not
	// spawn.
	policy toolpolicy.Policy
}

// WithPolicy implements agent.PolicyTool: the copy only lists and spawns
// the sub-agent types p allows.
func (t *agentTool) WithPolicy(p toolpolicy.Policy) agent.Tool {
	c := *t
	c.policy = p
	return &c
}

type agentArgs struct {
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
	SubagentType string `json:"subagent_type"`
	Model        string `json:"model"`
	Cleanup      string `json:"cleanup"`
}

func (t *agentTool) Definition() llmapi.Tool {
	return llmapi.Tool{
		Type: llmapi.ToolTypeFunction,
		Function: llmapi.FunctionDefinition{
			Name:        "agent",
			Description: t.buildDescription(),
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description": map[string]any{
						"type":        "string",
						"description": "A short (3-5 word) description of the task.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "The task for the agent to perform.",
					},
					"subagent_type": map[string]any{
						"type":        "string",
						"description": "The type of specialized agent to use for this task.",
					},
					"model": map[string]any{
						"type":        []string{"string", "null"},
						"description": "Optional model override for the sub-agent.",
					},
					"cleanup": map[string]any{
						"type":        []string{"string", "null"},
						"enum":        []any{"delete", "keep", nil},
						"description": "Whether to delete or keep the dialogue after completion.",
					},
				},
				"required":             []string{"description", "prompt"},
				"additionalProperties": false,
			},
		},
	}
}

// availableAgents returns the agents and agent skills the tool may spawn.
func (t *agentTool) availableAgents() []agent.AgentSummary {
	var ret []agent.AgentSummary
	for _, a := range t.agents {
		if t.policy.AllowsAgent(a.ID) {
			ret = append(ret, a)
		}
	}
	if t.skillRegistry != nil {
		for _, s := range t.skillRegistry.List() {
			if s.Type == "agent" && t.policy.AllowsAgent(s.Name) {
				ret = append(ret, agent.AgentSummary{ID: s.Name, Name: s.Description})
			}
		}
	}
	return ret
}

func (t *agentTool) buildDescription() string {
	var b strings.Builder
	b.WriteString(`Launch a new agent to handle complex, multi-step tasks autonomously.

The agent tool launches specialized agents that autonomously handle complex tasks. Each agent type has specific capabilities and tools available to it.`)

	if available := t.availableAgents(); len(available) > 0 {
		b.WriteString("\n\nAvailable agent types:\n")
		for _, a := range available {
			fmt.Fprintf(&b, "- %s: %s\n", a.ID, a.Name)
		}
		if t.policy.ConstrainsAgents() {
			b.WriteString("\nWhen using the agent tool, specify a subagent_type parameter " +
				"to select which agent type to use. It is required.")
		} else {
			b.WriteString("\nWhen using the agent tool, specify a subagent_type parameter " +
				"to select which agent type to use. If omitted, the caller's own " +
				"agent type is used.")
		}
	}

	b.WriteString(`

Usage notes:
- Always include a short description (3-5 words) summarizing what the agent will do.
- Use a sub-agent only for substantial, self-contained work that benefits from an isolated context, or for independent substantial tasks that benefit from parallel execution. Do not spawn one for work you can complete with a few targeted tool calls, and do not send multiple agents to explore the same area redundantly.
- When parallel sub-agents are warranted, launch them in a single message with multiple tool calls.
- When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.
- Each invocation starts fresh and you should provide a detailed task description with all necessary context.
- Provide clear, detailed prompts so the agent can work autonomously and return exactly the information you need.
- Treat sub-agent output as evidence to verify, not as authoritative. Check important code citations and conclusions yourself, and validate any code changes with relevant tests or diagnostics before reporting completion.
- Clearly tell the agent whether you expect it to write code or just to do research (search, file reads, etc.), since it is not aware of the user's intent.
- A description that recommends proactive use is a signal, not a requirement; still apply the threshold above before spawning the agent.
- If the user specifies that they want you to run agents "in parallel", you MUST send a single message with multiple agent tool use content blocks.`)
	return b.String()
}

func (t *agentTool) Summary(arguments string) string {
	var args agentArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return ""
	}
	if args.Description != "" {
		return args.Description
	}
	const maxPrompt = 60
	if len(args.Prompt) > maxPrompt {
		return args.Prompt[:maxPrompt] + "..."
	}
	return args.Prompt
}

func (t *agentTool) NeedsDeterministicOrder() bool { return false }

func (t *agentTool) Execute(
	ctx context.Context, arguments string,
) agent.ToolResult {
	var args agentArgs
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf(
				"error: invalid arguments: %v", err),
			IsError: true,
		}
	}
	if args.Prompt == "" {
		return agent.ToolResult{
			Content: "error: prompt is required",
			IsError: true,
		}
	}
	if t.policy.ConstrainsAgents() && (args.SubagentType == "" || !t.policy.AllowsAgent(args.SubagentType)) {
		var ids []string
		for _, a := range t.availableAgents() {
			ids = append(ids, a.ID)
		}
		msg := "error: subagent_type is required"
		if args.SubagentType != "" {
			msg = fmt.Sprintf("error: this agent may not spawn %q sub-agents", args.SubagentType)
		}
		if len(ids) == 0 {
			return agent.ToolResult{Content: msg + "; this agent may not spawn sub-agents", IsError: true}
		}
		return agent.ToolResult{
			Content: fmt.Sprintf("%s; use one of: %s", msg, strings.Join(ids, ", ")),
			IsError: true,
		}
	}

	handle, err := t.spawner.Run(ctx, agent.RunRequest{
		Message: args.Prompt,
		Label:   args.Description,
		AgentID: args.SubagentType,
		Model:   args.Model,
		Cleanup: args.Cleanup,
	})
	if err != nil {
		return agent.ToolResult{
			Content: fmt.Sprintf("error: %v", err),
			IsError: true,
		}
	}

	tr := consumeSubAgent(ctx, handle.Events, t.childEvents)

	// SubagentStop hook: parent's hook runner observes (and may
	// block) sub-agent completion. A blocked decision turns into an
	// error result returned to the parent's LLM.
	res := agent.HooksFromContext(ctx).Run(ctx, hooks.Payload{
		SessionID:     agent.DialogueIDFromContext(ctx),
		Cwd:           agent.WorkspaceURIFromContext(ctx),
		HookEventName: hooks.EventSubagentStop,
	})
	if res.Blocked() {
		reason := res.Reason
		if reason == "" {
			reason = "blocked by SubagentStop hook"
		}
		tr = agent.ToolResult{Content: reason, IsError: true}
	}
	return tr
}

// consumeSubAgent drains a sub-agent event iterator, forwarding
// interesting events to childEvents and collecting reply text.
// Returns the collected reply as a ToolResult.
func consumeSubAgent(
	ctx context.Context,
	it iterator.Iterator[agent.Event],
	childEvents chan<- agent.ChildEvent,
) agent.ToolResult {
	defer it.Close() //nolint:errcheck

	parentCallID := agent.ParentToolCallID(ctx)
	if parentCallID == "" {
		panic("consumeSubAgent called without ParentToolCallID")
	}
	if childEvents == nil {
		panic("child events is nil")
	}

	var reply strings.Builder
	var reasoning strings.Builder
	var lastErr error
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			break
		}

		switch ev.Type {
		case agent.EventText:
			reply.WriteString(ev.Text)
		case agent.EventReasoning:
			reasoning.WriteString(ev.Reasoning)
		case agent.EventError:
			if ev.Error != nil {
				lastErr = ev.Error
			}
		}

		switch ev.Type {
		case agent.EventToolCall, agent.EventToolResult, agent.EventText,
			agent.EventReasoning, agent.EventError, agent.EventToolsDropped,
			agent.EventCompacted:
		default:
			continue
		}

		select {
		case childEvents <- agent.ChildEvent{
			ParentToolCallID: parentCallID,
			Event:            ev,
		}:
		case <-ctx.Done():
			return agent.ToolResult{
				Content: "sub-agent cancelled",
				IsError: true,
			}
		}
	}

	if ctx.Err() != nil {
		tr := agent.ToolResult{Content: "sub-agent timed out or was cancelled", IsError: true}
		forwardResult(ctx, childEvents, parentCallID, tr)
		return tr
	}

	// Prefer text output; fall back to reasoning (extended thinking
	// models may produce reasoning-only responses).
	result := reply.String()
	if result == "" {
		result = reasoning.String()
	}

	// If the sub-agent produced no output at all, check for errors
	// that would otherwise be silently swallowed (the LLM would
	// see an empty tool result with no indication of failure).
	if result == "" {
		if lastErr != nil {
			tr := agent.ToolResult{Content: lastErr.Error(), IsError: true}
			forwardResult(ctx, childEvents, parentCallID, tr)
			return tr
		}
		if err := it.Err(); err != nil {
			tr := agent.ToolResult{Content: err.Error(), IsError: true}
			forwardResult(ctx, childEvents, parentCallID, tr)
			return tr
		}
	}

	tr := agent.ToolResult{Content: result}
	forwardResult(ctx, childEvents, parentCallID, tr)
	return tr
}

// forwardResult sends an EventDone child event carrying the sub-agent's
// final result so the TUI can display a result leaf node.
func forwardResult(
	ctx context.Context, childEvents chan<- agent.ChildEvent,
	parentCallID string, tr agent.ToolResult,
) {
	select {
	case childEvents <- agent.ChildEvent{
		ParentToolCallID: parentCallID,
		Event: agent.Event{
			Type:    agent.EventDone,
			Text:    tr.Content,
			IsError: tr.IsError,
		},
	}:
	case <-ctx.Done():
	}
}
