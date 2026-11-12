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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
)

func TestAgentTool(t *testing.T) {
	t.Run("successful run returns reply", func(t *testing.T) {
		spawner := &mockSpawner{
			handle: agent.RunHandle{
				SessionKey: "sk-123",
				Label:      "search code",
				Events:     newMockEventIterator(agent.Event{Type: agent.EventText, Text: "result"}),
			},
		}
		childEvents := make(chan agent.ChildEvent, 64)
		tool := NewAgentTool(spawner, nil, childEvents, nil)
		ctx := agent.WithParentToolCallID(context.Background(), "parent-1")
		result := tool.Execute(
			ctx,
			`{"description":"search code","prompt":"find all tests"}`,
		)

		assert.False(t, result.IsError)
		assert.Equal(t, "result", result.Content)
	})

	t.Run("run error", func(t *testing.T) {
		spawner := &mockSpawner{
			runErr: errors.New("not allowed"),
		}
		tool := NewAgentTool(spawner, nil, nil, nil)
		result := tool.Execute(
			context.Background(),
			`{"description":"do work","prompt":"do work"}`,
		)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content, "not allowed")
	})

	t.Run("invalid JSON", func(t *testing.T) {
		tool := NewAgentTool(&mockSpawner{}, nil, nil, nil)
		result := tool.Execute(
			context.Background(), `not json`,
		)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content,
			"invalid arguments")
	})

	t.Run("empty prompt", func(t *testing.T) {
		tool := NewAgentTool(&mockSpawner{}, nil, nil, nil)
		result := tool.Execute(
			context.Background(),
			`{"description":"x","prompt":""}`,
		)

		assert.True(t, result.IsError)
		assert.Contains(t, result.Content,
			"prompt is required")
	})

	t.Run("definition", func(t *testing.T) {
		tool := NewAgentTool(&mockSpawner{}, nil, nil, nil)
		def := tool.Definition()

		assert.Equal(t, llmapi.ToolTypeFunction, def.Type)
		assert.Equal(t, "agent", def.Function.Name)
		assert.NotEmpty(t, def.Function.Description)
		assert.Contains(t, def.Function.Description,
			"substantial, self-contained work")
		assert.Contains(t, def.Function.Description,
			"evidence to verify, not as authoritative")
		assert.NotContains(t, def.Function.Description,
			"Launch multiple agents concurrently whenever possible")
		assert.NotContains(t, def.Function.Description,
			"outputs should generally be trusted")
		assert.NotNil(t, def.Function.Parameters)
	})

	t.Run("description includes agent types", func(t *testing.T) {
		agents := []agent.AgentSummary{
			{ID: "coder", Name: "Coder Agent"},
			{ID: "reviewer", Name: "Review Agent"},
		}
		tool := NewAgentTool(&mockSpawner{}, agents, nil, nil)
		def := tool.Definition()

		assert.Contains(t, def.Function.Description,
			"coder: Coder Agent")
		assert.Contains(t, def.Function.Description,
			"reviewer: Review Agent")
		assert.Contains(t, def.Function.Description,
			"subagent_type")
	})

	t.Run("description without agents omits types section",
		func(t *testing.T) {
			tool := NewAgentTool(&mockSpawner{}, nil, nil, nil)
			def := tool.Definition()

			assert.NotContains(t,
				def.Function.Description,
				"Available agent types")
		},
	)

	t.Run("description includes agent-type skills from registry",
		func(t *testing.T) {
			dir := t.TempDir()
			skillDir := filepath.Join(dir, "my-planner")
			require.NoError(t, os.MkdirAll(skillDir, 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(skillDir, "SKILL.md"),
				[]byte("---\nname: my-planner\ndescription: Plans things\ntype: agent\n---\nbody"),
				0o644,
			))
			reg := skills.NewRegistry(localFS{}, dirURI(""), []string{dir}, nil)

			tool := NewAgentTool(&mockSpawner{}, nil, nil, reg)
			def := tool.Definition()

			assert.Contains(t, def.Function.Description,
				"my-planner: Plans things")
			assert.Contains(t, def.Function.Description,
				"Available agent types")
		},
	)

	t.Run("description combines agents and agent-type skills",
		func(t *testing.T) {
			dir := t.TempDir()
			skillDir := filepath.Join(dir, "my-planner")
			require.NoError(t, os.MkdirAll(skillDir, 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(skillDir, "SKILL.md"),
				[]byte("---\nname: my-planner\ndescription: Plans things\ntype: agent\n---\nbody"),
				0o644,
			))
			reg := skills.NewRegistry(localFS{}, dirURI(""), []string{dir}, nil)

			agents := []agent.AgentSummary{
				{ID: "coder", Name: "Coder Agent"},
			}
			tool := NewAgentTool(&mockSpawner{}, agents, nil, reg)
			def := tool.Definition()

			assert.Contains(t, def.Function.Description, "coder: Coder Agent")
			assert.Contains(t, def.Function.Description, "my-planner: Plans things")
		},
	)

	t.Run("only description and prompt are required",
		func(t *testing.T) {
			tool := NewAgentTool(&mockSpawner{}, nil, nil, nil)
			def := tool.Definition()
			params := def.Function.Parameters.(map[string]any)
			required := params["required"].([]string)

			assert.Equal(t,
				[]string{"description", "prompt"},
				required,
			)
		},
	)

	t.Run("passes all parameters to spawner", func(t *testing.T) {
		spawner := &mockSpawner{
			handle: agent.RunHandle{
				Events: newMockEventIterator(),
			},
		}
		childEvents := make(chan agent.ChildEvent, 64)
		tool := NewAgentTool(spawner, nil, childEvents, nil)
		ctx := agent.WithParentToolCallID(context.Background(), "parent-params")
		tool.Execute(
			ctx,
			`{
				"description":"search code",
				"prompt":"find all tests",
				"subagent_type":"coder",
				"model":"gpt-4",
				"cleanup":"delete"
			}`,
		)

		req := spawner.lastRunReq
		assert.Equal(t, "find all tests", req.Message)
		assert.Equal(t, "search code", req.Label)
		assert.Equal(t, "coder", req.AgentID)
		assert.Equal(t, "gpt-4", req.Model)
		assert.Equal(t, "delete", req.Cleanup)
	})

	t.Run("reasoning-only response is captured", func(t *testing.T) {
		spawner := &mockSpawner{
			handle: agent.RunHandle{
				Events: newMockEventIterator(
					agent.Event{Type: agent.EventReasoning, Reasoning: "The answer is 42."},
				),
			},
		}
		childEvents := make(chan agent.ChildEvent, 64)
		tool := NewAgentTool(spawner, nil, childEvents, nil)
		ctx := agent.WithParentToolCallID(context.Background(), "parent-reason")
		result := tool.Execute(
			ctx,
			`{"description":"think","prompt":"what is the answer?"}`,
		)

		assert.False(t, result.IsError)
		assert.Equal(t, "The answer is 42.", result.Content,
			"reasoning-only sub-agent output must not be lost")
	})

	t.Run("text preferred over reasoning", func(t *testing.T) {
		spawner := &mockSpawner{
			handle: agent.RunHandle{
				Events: newMockEventIterator(
					agent.Event{Type: agent.EventReasoning, Reasoning: "thinking..."},
					agent.Event{Type: agent.EventText, Text: "final answer"},
				),
			},
		}
		childEvents := make(chan agent.ChildEvent, 64)
		tool := NewAgentTool(spawner, nil, childEvents, nil)
		ctx := agent.WithParentToolCallID(context.Background(), "parent-text")
		result := tool.Execute(
			ctx,
			`{"description":"think","prompt":"what is the answer?"}`,
		)

		assert.False(t, result.IsError)
		assert.Equal(t, "final answer", result.Content,
			"text output must be preferred over reasoning")
	})

	t.Run("sub-agent error is propagated", func(t *testing.T) {
		spawner := &mockSpawner{
			handle: agent.RunHandle{
				Events: newMockEventIterator(
					agent.Event{
						Type:  agent.EventError,
						Error: errors.New("get dialogue: context canceled"),
					},
				),
			},
		}
		childEvents := make(chan agent.ChildEvent, 64)
		tool := NewAgentTool(spawner, nil, childEvents, nil)
		ctx := agent.WithParentToolCallID(context.Background(), "parent-err")
		result := tool.Execute(
			ctx,
			`{"description":"research","prompt":"do research"}`,
		)

		assert.True(t, result.IsError, "sub-agent error must be reported as error")
		assert.Contains(t, result.Content, "context canceled",
			"sub-agent error message must be in tool result content")
	})

	t.Run("forwards child events", func(t *testing.T) {
		spawner := &mockSpawner{
			handle: agent.RunHandle{
				Events: newMockEventIterator(
					agent.Event{Type: agent.EventToolCall, ToolCallID: "tc1", ToolName: "read_file"},
					agent.Event{Type: agent.EventToolResult, ToolCallID: "tc1"},
					agent.Event{Type: agent.EventText, Text: "done"},
				),
			},
		}
		childEvents := make(chan agent.ChildEvent, 64)
		tool := NewAgentTool(spawner, nil, childEvents, nil)
		ctx := agent.WithParentToolCallID(context.Background(), "parent-42")
		result := tool.Execute(ctx, `{"description":"test","prompt":"test"}`)

		assert.False(t, result.IsError)
		assert.Equal(t, "done", result.Content)

		// Drain child events
		close(childEvents)
		var events []agent.ChildEvent
		for ev := range childEvents {
			events = append(events, ev)
		}
		assert.NotEmpty(t, events)
		for _, ev := range events {
			assert.Equal(t, "parent-42", ev.ParentToolCallID)
		}
	})

	t.Run("a policy restricts the agent types", func(t *testing.T) {
		dir := t.TempDir()
		writeTestSkill(t, dir, "acme-triage", "---\nname: acme-triage\ndescription: Triage\ntype: agent\n---\nbody")
		registry := skills.NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
		agents := []agent.AgentSummary{{ID: "default", Name: "Default Agent"}}
		run := func(p toolpolicy.Policy, args string) (agent.ToolResult, *mockSpawner) {
			spawner := &mockSpawner{handle: agent.RunHandle{
				Events: newMockEventIterator(agent.Event{Type: agent.EventText, Text: "done"}),
			}}
			tool := NewAgentTool(spawner, agents, make(chan agent.ChildEvent, 64), registry).(agent.PolicyTool).WithPolicy(p)
			return tool.Execute(agent.WithParentToolCallID(context.Background(), "p"), args), spawner
		}
		exploreOnly := mustPolicy(t, []string{"agent(explore)"}, nil)

		desc := NewAgentTool(&mockSpawner{}, agents, nil, registry).(agent.PolicyTool).
			WithPolicy(exploreOnly).Definition().Function.Description
		assert.Contains(t, desc, "- explore: ")
		assert.NotContains(t, desc, "default: Default Agent")
		assert.NotContains(t, desc, "acme-triage")
		assert.NotContains(t, desc, "- plan: ")
		assert.Contains(t, desc, "It is required.")

		result, spawner := run(exploreOnly, `{"description":"x","prompt":"x","subagent_type":"default"}`)
		assert.True(t, result.IsError)
		assert.Equal(t, `error: this agent may not spawn "default" sub-agents; use one of: explore`, result.Content)
		assert.Empty(t, spawner.lastRunReq.Message, "the spawner is never called")

		result, spawner = run(exploreOnly, `{"description":"x","prompt":"x"}`)
		assert.True(t, result.IsError)
		assert.Equal(t, "error: subagent_type is required; use one of: explore", result.Content)
		assert.Empty(t, spawner.lastRunReq.Message)

		result, spawner = run(exploreOnly, `{"description":"x","prompt":"x","subagent_type":"Explore"}`)
		assert.False(t, result.IsError, result.Content)
		assert.Equal(t, "Explore", spawner.lastRunReq.AgentID)

		result, _ = run(mustPolicy(t, nil, []string{"agent(*)"}), `{"description":"x","prompt":"x"}`)
		assert.Equal(t, "error: subagent_type is required; this agent may not spawn sub-agents", result.Content)

		result, spawner = run(mustPolicy(t, []string{"agent"}, nil), `{"description":"x","prompt":"x"}`)
		assert.False(t, result.IsError, "an unconstrained agent tool keeps the caller's own type")
		assert.Equal(t, "x", spawner.lastRunReq.Message)
	})
}

var _ agent.Tool = (*agentTool)(nil)

type mockSpawner struct {
	handle     agent.RunHandle
	runErr     error
	lastRunReq agent.RunRequest
}

func (m *mockSpawner) Run(
	_ context.Context, req agent.RunRequest,
) (agent.RunHandle, error) {
	m.lastRunReq = req
	return m.handle, m.runErr
}

// mockEventIterator is a simple iterator that yields preset events.
type mockEventIterator struct {
	events []agent.Event
	idx    int
	err    error
}

func newMockEventIterator(events ...agent.Event) iterator.Iterator[agent.Event] {
	return &mockEventIterator{events: events}
}

func (m *mockEventIterator) Next(_ context.Context) (agent.Event, bool) {
	if m.idx >= len(m.events) {
		return agent.Event{}, false
	}
	ev := m.events[m.idx]
	m.idx++
	// Mirror channelIterator: set err on EventError.
	if ev.Type == agent.EventError {
		m.err = ev.Error
	}
	return ev, true
}

func (m *mockEventIterator) Err() error   { return m.err }
func (m *mockEventIterator) Close() error { return nil }
