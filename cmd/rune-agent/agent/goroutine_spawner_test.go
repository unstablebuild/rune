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

package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
)

// nopFileSystem and osFileSystem are defined in agent_test.go (same package).

// noopPrompter is a Prompter that never asks the user anything and
// returns an error if anything tries to. NewGoroutineSpawner requires
// a non-nil prompter; tests that do not exercise prompting use this.
type noopPrompter struct{}

func (noopPrompter) Prompt(context.Context, PromptRequest) (PromptResponse, error) {
	return PromptResponse{}, fmt.Errorf("noopPrompter: prompt not supported in tests")
}

func TestNewGoroutineSpawner_panics_on_nil_prompter(t *testing.T) {
	assert.PanicsWithValue(t,
		"agent.NewGoroutineSpawner: prompter must not be nil",
		func() {
			NewGoroutineSpawner(
				newMockStore(),
				func(string) (llmapi.Service, llmapi.ModelEntry, error) { return nil, llmapi.ModelEntry{}, nil },
				NewConfig(nil),
				skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
				NoMemory(), "", "s", "a", workspaceapi.URI{},
				nil,
			)
		},
	)
}

func TestGoroutineSpawner_Run(t *testing.T) {
	tests := []struct {
		name     string
		defs     []Definition
		agentID  string
		req      RunRequest
		wantErr  string
		assertFn func(t *testing.T, handle RunHandle)
	}{
		{
			name: "run returns handle with events",
			defs: []Definition{
				{
					ID:       "agent",
					Name:     "Agent",
					Model:    "test-model",
					AllowAny: true,
				},
			},
			agentID: "agent",
			req: RunRequest{
				AgentID: "agent",
				Message: "hello",
			},
			assertFn: func(t *testing.T, handle RunHandle) {
				assert.NotEmpty(t, handle.SessionKey)
				reply := consumeReply(t, handle)
				assert.Equal(t, "reply text", reply)
			},
		},
		{
			name: "run unknown agent returns error",
			defs: []Definition{
				{
					ID:    "agent",
					Name:  "Agent",
					Model: "test-model",
				},
			},
			agentID: "agent",
			req: RunRequest{
				AgentID: "unknown",
				Message: "hello",
			},
			wantErr: "no agent or agent skill",
		},
		{
			name: "run disallowed agent returns error",
			defs: []Definition{
				{
					ID:         "parent",
					Name:       "Parent",
					Model:      "test-model",
					AllowSpawn: []string{"allowed"},
				},
				{
					ID:    "forbidden",
					Name:  "Forbidden",
					Model: "test-model",
				},
			},
			agentID: "parent",
			req: RunRequest{
				AgentID: "forbidden",
				Message: "hello",
			},
			wantErr: "no agent or agent skill",
		},
		{
			name: "run with invalid cleanup returns error",
			defs: []Definition{
				{
					ID:       "agent",
					Name:     "Agent",
					Model:    "test-model",
					AllowAny: true,
				},
			},
			agentID: "agent",
			req: RunRequest{
				Message: "hello",
				Cleanup: "invalid",
			},
			wantErr: "invalid cleanup value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewConfig(tt.defs)
			svc := &mockService{
				responses: []mockResponse{
					stopResponse("reply text"),
				},
			}
			factory := func(model string) (llmapi.Service, llmapi.ModelEntry, error) {
				return svc, llmapi.ModelEntry{Name: "test"}, nil
			}
			spawner := NewGoroutineSpawner(
				newMockStore(), factory, cfg,
				skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
				NoMemory(), "",
				"session-1", tt.agentID, workspaceapi.URI{}, noopPrompter{},
			)

			handle, err := spawner.Run(
				context.Background(), tt.req,
			)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.assertFn != nil {
				tt.assertFn(t, handle)
			}
		})
	}
}

func TestGoroutineSpawner_Run_uses_request_model(t *testing.T) {
	cfg := NewConfig([]Definition{
		{
			ID:       "agent",
			Name:     "Agent",
			Model:    "default-model",
			AllowAny: true,
		},
	})

	var mu sync.Mutex
	var requestedModels []string
	factory := func(model string) (llmapi.Service, llmapi.ModelEntry, error) {
		mu.Lock()
		requestedModels = append(requestedModels, model)
		mu.Unlock()
		return &mockService{
			responses: []mockResponse{stopResponse("ok")},
		}, llmapi.ModelEntry{Name: "test", Provider: "openai"}, nil
	}
	spawner := NewGoroutineSpawner(
		newMockStore(), factory, cfg,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "",
		"session-1", "agent", workspaceapi.URI{},
		noopPrompter{},
	)

	// Run without Model: should use the agent definition's model.
	handle, err := spawner.Run(context.Background(), RunRequest{
		AgentID: "agent",
		Message: "hello",
	})
	require.NoError(t, err)
	consumeReply(t, handle)

	// Run with Model override: should use the requested model.
	handle, err = spawner.Run(context.Background(), RunRequest{
		AgentID: "agent",
		Message: "hello",
		Model:   "claude-3-haiku",
	})
	require.NoError(t, err)
	consumeReply(t, handle)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requestedModels, 2)
	assert.Equal(t, "default-model", requestedModels[0],
		"should use agent definition model when RunRequest.Model is empty")
	assert.Equal(t, "claude-3-haiku", requestedModels[1],
		"should use RunRequest.Model when set")
}

func TestGoroutineSpawner_Run_inherits_context_model_qualified(t *testing.T) {
	cfg := NewConfig([]Definition{
		{
			ID:       "agent",
			Name:     "Agent",
			AllowAny: true,
		},
	})

	var mu sync.Mutex
	var requestedModels []string
	factory := func(model string) (llmapi.Service, llmapi.ModelEntry, error) {
		mu.Lock()
		requestedModels = append(requestedModels, model)
		mu.Unlock()
		return &mockService{
			responses: []mockResponse{stopResponse("ok")},
		}, llmapi.ModelEntry{Name: "test", Provider: "openai"}, nil
	}
	spawner := NewGoroutineSpawner(
		newMockStore(), factory, cfg,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "",
		"session-1", "agent", workspaceapi.URI{},
		noopPrompter{},
	)

	ctx := WithCurrentModel(context.Background(),
		llmapi.ModelEntry{Name: "claude-opus-4-8", Provider: "anthropic"})
	handle, err := spawner.Run(ctx, RunRequest{
		AgentID: "agent",
		Message: "hello",
	})
	require.NoError(t, err)
	consumeReply(t, handle)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, requestedModels, 1)
	assert.Equal(t, "anthropic/claude-opus-4-8", requestedModels[0],
		"should qualify the inherited context model with its provider")
}

func TestGoroutineSpawner_RunWithCleanup(t *testing.T) {
	cfg := NewConfig([]Definition{
		{
			ID:       "agent",
			Name:     "Agent",
			Model:    "m",
			AllowAny: true,
		},
	})
	svc := &mockService{
		responses: []mockResponse{stopResponse("ok")},
	}
	store := newMockStore()
	spawner := NewGoroutineSpawner(
		store,
		func(string) (llmapi.Service, llmapi.ModelEntry, error) {
			return svc, llmapi.ModelEntry{Name: "test"}, nil
		},
		cfg,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "",
		"psession",
		"agent", workspaceapi.URI{},
		noopPrompter{},
	)
	spawner.GenerateDialogueID = func(_ context.Context, _ string) string {
		return "sub-agent-agent-fixed-id"
	}

	handle, err := spawner.Run(context.Background(), RunRequest{
		Message: "task",
		Cleanup: "delete",
	})
	require.NoError(t, err)
	consumeReply(t, handle)

	// Dialogue should have been deleted by Close().
	_, dialogueExists := store.getDialogue("sub-agent-agent-fixed-id")
	assert.False(t, dialogueExists,
		"dialogue should be deleted after cleanup")
}

func TestGoroutineSpawner_RunWithTools(t *testing.T) {
	mustPolicy := func(allowed, denied []string) toolpolicy.Policy {
		p, err := toolpolicy.Parse(allowed, denied)
		require.NoError(t, err)
		return p
	}
	readOnly := mustPolicy([]string{"read_file", "agent"}, nil)
	noAgents := mustPolicy(nil, []string{"agent(*)"})
	tests := []struct {
		name string
		// parent is the policy of the agent whose tool spawns the child.
		parent toolpolicy.Policy
		tools  toolpolicy.Policy
		want   toolpolicy.Policy
	}{
		{name: "the request restricts the child", tools: readOnly, want: readOnly},
		{name: "the child of a restricted parent keeps its restrictions", parent: readOnly,
			want: readOnly},
		{name: "both restrict the child", parent: readOnly, tools: noAgents,
			want: toolpolicy.Intersect(readOnly, noAgents)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := NewConfig([]Definition{{ID: "agent", Name: "Agent", Model: "m", AllowAny: true}})
			svc := &mockService{
				responses: []mockResponse{
					toolCallResponse("read_file", `{"path":"a.go"}`, "tc1"),
					toolCallResponse("bash", `{"command":"rm -rf x"}`, "tc2"),
					stopResponse("done reading"),
				},
			}
			var seen toolpolicy.Policy
			readTool := &mockTool{name: "read_file", executeFn: func(ctx context.Context, _ string) ToolResult {
				seen = ToolPolicyFromContext(ctx)
				return ToolResult{Content: "contents"}
			}}
			bashTool := &mockTool{name: "bash", result: ToolResult{Content: "cmd"}}
			spawner := NewGoroutineSpawner(
				newMockStore(),
				func(string) (llmapi.Service, llmapi.ModelEntry, error) {
					return svc, llmapi.ModelEntry{Name: "test"}, nil
				},
				cfg,
				skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
				NoMemory(), "",
				"session", "agent", workspaceapi.URI{},
				noopPrompter{},
			)
			spawner.SetRegistry(NewRegistry(readTool, bashTool))

			handle, err := spawner.Run(WithToolPolicy(context.Background(), tc.parent), RunRequest{
				AgentID:      "agent",
				Message:      "read a file",
				Tools:        tc.tools,
				SystemPrompt: "You are a reader.",
			})
			require.NoError(t, err)
			assert.Equal(t, "done reading", consumeReply(t, handle))
			assert.Equal(t, int32(1), readTool.execCount.Load())
			assert.Equal(t, int32(0), bashTool.execCount.Load())
			assert.Equal(t, tc.want, seen, "the child's tools spawn grandchildren with its policy")
			for _, tool := range svc.requests[0].Tools {
				assert.NotEqual(t, "bash", tool.Function.Name)
			}
		})
	}
}

func TestGoroutineSpawner_RunAgentSkillWithItsPolicy(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/acme-triage", 0o755))
	require.NoError(t, os.WriteFile(dir+"/acme-triage/SKILL.md", []byte(`---
name: acme-triage
description: Triage
type: agent
allowed-tools: [read_file, "bash(git log *)"]
---
You triage.`), 0o644))
	skillRegistry := skills.NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
	skill, ok := skillRegistry.Get("acme-triage")
	require.True(t, ok)

	// An agent with the skill's name must not stand in for the skill,
	// which would run it without the skill's restrictions.
	shadow := NewConfig([]Definition{{ID: "acme-triage", SystemPrompt: "You do anything.", AllowAny: true}})
	tests := []struct {
		name string
		cfg  *Cfg
		req  RunRequest
	}{
		{name: "agent ID naming the skill", cfg: NewConfig(nil), req: RunRequest{AgentID: "Acme-Triage"}},
		{name: "skill", cfg: NewConfig(nil), req: RunRequest{Skill: "acme-triage"}},
		{name: "skill named like an agent", cfg: shadow, req: RunRequest{Skill: "acme-triage"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockService{responses: []mockResponse{
				toolCallResponse("read_file", `{"path":"a.go"}`, "tc1"),
				stopResponse("done"),
			}}
			var seen toolpolicy.Policy
			readTool := &mockTool{name: "read_file", executeFn: func(ctx context.Context, _ string) ToolResult {
				seen = ToolPolicyFromContext(ctx)
				return ToolResult{Content: "contents"}
			}}
			store := newMockStore()
			spawner := NewGoroutineSpawner(
				store,
				func(string) (llmapi.Service, llmapi.ModelEntry, error) {
					return svc, llmapi.ModelEntry{Name: "test"}, nil
				},
				tc.cfg, skillRegistry, NoMemory(), "",
				"session", "agent", workspaceapi.URI{},
				noopPrompter{},
			)
			spawner.SetRegistry(NewRegistry(readTool, &mockTool{name: "skill"}, &mockTool{name: "bash"}))

			req := tc.req
			req.Message = "go"
			handle, err := spawner.Run(context.Background(), req)
			require.NoError(t, err)
			assert.Equal(t, "done", consumeReply(t, handle))
			assert.True(t, strings.HasPrefix(handle.DialogueID, "sub-agent-acme-triage-"), handle.DialogueID)
			assert.Equal(t, skillRegistry.AgentPolicy(skill), seen)
			var names []string
			for _, tool := range svc.requests[0].Tools {
				names = append(names, tool.Function.Name)
			}
			assert.Equal(t, []string{"bash", "read_file"}, names)
			assert.Equal(t, "You triage.", svc.requests[0].Messages[0].Content)
		})
	}
}

func TestGoroutineSpawner_RunMissingSkill(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(dir+"/notes", 0o755))
	require.NoError(t, os.WriteFile(dir+"/notes/SKILL.md", []byte("---\nname: notes\ndescription: Notes\n---\nTake notes."), 0o644))
	skillRegistry := skills.NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
	spawner := NewGoroutineSpawner(
		newMockStore(),
		func(string) (llmapi.Service, llmapi.ModelEntry, error) {
			t.Fatal("no sub-agent may start")
			return nil, llmapi.ModelEntry{}, nil
		},
		NewConfig([]Definition{{ID: "default", AllowAny: true}}), skillRegistry, NoMemory(), "",
		"session", "default", workspaceapi.URI{},
		noopPrompter{},
	)
	for _, name := range []string{"default", "notes", "missing"} {
		t.Run(name, func(t *testing.T) {
			_, err := spawner.Run(context.Background(), RunRequest{Skill: name, Message: "go"})
			assert.EqualError(t, err, fmt.Sprintf("no agent skill named %q exists", name))
		})
	}
}

func TestGoroutineSpawner_Run_streams_events(t *testing.T) {
	cfg := NewConfig([]Definition{
		{
			ID:       "agent",
			Name:     "Agent",
			Model:    "test-model",
			AllowAny: true,
		},
	})

	// Sub-agent does: tool_call → tool_result → text → done
	svc := &mockService{
		responses: []mockResponse{
			toolCallResponse("read_file", `{"path":"a.go"}`, "tc1"),
			stopResponse("done reading"),
		},
	}
	readTool := &mockTool{
		name:   "read_file",
		result: ToolResult{Content: "file contents"},
	}
	spawner := NewGoroutineSpawner(
		newMockStore(),
		func(string) (llmapi.Service, llmapi.ModelEntry, error) {
			return svc, llmapi.ModelEntry{Name: "test", Provider: "openai"}, nil
		},
		cfg,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "",
		"session", "agent", workspaceapi.URI{},
		noopPrompter{},
	)
	spawner.SetRegistry(NewRegistry(readTool))

	handle, err := spawner.Run(context.Background(), RunRequest{
		AgentID: "agent",
		Message: "read a file",
	})
	require.NoError(t, err)
	defer handle.Events.Close() //nolint:errcheck

	var events []Event
	for {
		ev, ok := handle.Events.Next(context.Background())
		if !ok {
			break
		}
		events = append(events, ev)
	}

	// Should include tool call, tool result, text, and done events.
	var hasToolCall, hasToolResult, hasText, hasDone bool
	for _, ev := range events {
		switch ev.Type {
		case EventToolCall:
			hasToolCall = true
			assert.Equal(t, "read_file", ev.ToolName)
		case EventToolResult:
			hasToolResult = true
		case EventText:
			hasText = true
		case EventDone:
			hasDone = true
		}
	}
	assert.True(t, hasToolCall, "should stream EventToolCall")
	assert.True(t, hasToolResult, "should stream EventToolResult")
	assert.True(t, hasText, "should stream EventText")
	assert.True(t, hasDone, "should stream EventDone")
}

func TestGoroutineSpawner_Run_no_tool_deadline(t *testing.T) {
	cfg := NewConfig([]Definition{
		{
			ID:       "agent",
			Name:     "Agent",
			Model:    "test-model",
			AllowAny: true,
		},
	})

	svc := &mockService{
		responses: []mockResponse{
			toolCallResponse("block", `{}`, "tc1"),
			stopResponse("done"),
		},
	}

	var capturedDeadlineOK bool
	var capturedHasDeadline bool
	blockTool := &mockTool{
		name:   "block",
		result: ToolResult{Content: "ok"},
		executeFn: func(ctx context.Context, _ string) ToolResult {
			_, capturedHasDeadline = ctx.Deadline()
			capturedDeadlineOK = true
			return ToolResult{Content: "ok"}
		},
	}
	spawner := NewGoroutineSpawner(
		newMockStore(),
		func(string) (llmapi.Service, llmapi.ModelEntry, error) {
			return svc, llmapi.ModelEntry{Name: "test"}, nil
		},
		cfg,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "",
		"session", "agent", workspaceapi.URI{},
		noopPrompter{},
	)
	spawner.SetRegistry(NewRegistry(blockTool))

	handle, err := spawner.Run(context.Background(), RunRequest{
		AgentID: "agent",
		Message: "call tool",
	})
	require.NoError(t, err)
	consumeReply(t, handle)

	require.True(t, capturedDeadlineOK, "tool must have been executed")
	assert.False(t, capturedHasDeadline,
		"sub-agent tool context must not carry a rune-agent-imposed deadline")
}

func TestGoroutineSpawner_Run_caller_cancel_propagates(t *testing.T) {
	cfg := NewConfig([]Definition{
		{
			ID:       "agent",
			Name:     "Agent",
			Model:    "test-model",
			AllowAny: true,
		},
	})

	// Tool blocks on ctx.Done so we can observe cancellation plumbing.
	toolEntered := make(chan struct{})
	toolErr := make(chan error, 1)
	blockTool := &mockTool{
		name: "block",
		executeFn: func(ctx context.Context, _ string) ToolResult {
			close(toolEntered)
			<-ctx.Done()
			toolErr <- ctx.Err()
			return ToolResult{Content: "cancelled", IsError: true}
		},
	}

	svc := &mockService{
		responses: []mockResponse{
			toolCallResponse("block", `{}`, "tc1"),
			stopResponse("done"),
		},
	}
	spawner := NewGoroutineSpawner(
		newMockStore(),
		func(string) (llmapi.Service, llmapi.ModelEntry, error) {
			return svc, llmapi.ModelEntry{Name: "test"}, nil
		},
		cfg,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "",
		"session", "agent", workspaceapi.URI{},
		noopPrompter{},
	)
	spawner.SetRegistry(NewRegistry(blockTool))

	ctx, cancel := context.WithCancel(context.Background())
	handle, err := spawner.Run(ctx, RunRequest{
		AgentID: "agent",
		Message: "call tool",
	})
	require.NoError(t, err)

	// Drain events in the background until the iterator closes.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, ok := handle.Events.Next(context.Background()); !ok {
				return
			}
		}
	}()

	// Once the tool is running, cancel the caller context.
	<-toolEntered
	cancel()

	select {
	case err := <-toolErr:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("tool was not notified of cancellation")
	}
	<-done
}

func TestGoroutineSpawner_RunWithLabel(t *testing.T) {
	cfg := NewConfig([]Definition{
		{
			ID:       "agent",
			Name:     "Agent",
			Model:    "test-model",
			AllowAny: true,
		},
	})

	var idCounter int
	svc := &mockService{
		responses: []mockResponse{stopResponse("done")},
	}
	spawner := NewGoroutineSpawner(
		newMockStore(),
		func(string) (llmapi.Service, llmapi.ModelEntry, error) {
			return svc, llmapi.ModelEntry{Name: "test"}, nil
		},
		cfg,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "",
		"parent-session", "agent", workspaceapi.URI{},
		noopPrompter{},
	)
	spawner.GenerateDialogueID = func(_ context.Context, _ string) string {
		idCounter++
		return fmt.Sprintf("sub-agent-agent-test-id-%d", idCounter)
	}

	// With label
	handle, err := spawner.Run(context.Background(), RunRequest{
		Message: "task",
		Label:   "my-label",
	})
	require.NoError(t, err)
	assert.Equal(t, "my-label", handle.Label)
	consumeReply(t, handle)

	// Without label — uses dialogue ID
	svc.responses = []mockResponse{stopResponse("done")}
	handle, err = spawner.Run(context.Background(), RunRequest{
		Message: "task",
	})
	require.NoError(t, err)
	assert.Equal(t, handle.SessionKey, handle.Label)
	consumeReply(t, handle)
}

func TestGoroutineSpawner_Run_skill_fallback(t *testing.T) {
	// Helper to write a SKILL.md into a temp dir.
	writeSkill := func(t *testing.T, dir, name, content string) {
		t.Helper()
		skillDir := dir + "/" + name
		require.NoError(t, os.MkdirAll(skillDir, 0o755))
		require.NoError(t, os.WriteFile(skillDir+"/SKILL.md", []byte(content), 0o644))
	}

	makeRegistry := func(t *testing.T, content string) *skills.SkillRegistry {
		t.Helper()
		dir := t.TempDir()
		writeSkill(t, dir, "planner", content)
		return skills.NewRegistry(osFileSystem{}, dirURI(""), []string{dir}, nil)
	}

	agentSkill := `---
name: planner
description: Plans things
type: agent
allowed-tools: read_file bash
---
You are a planner.`

	promptSkill := `---
name: planner
description: Plans things
---
You are a planner.`

	baseCfg := NewConfig([]Definition{
		{ID: "agent", Name: "Agent", Model: "test-model", AllowAny: true},
	})

	newSpawner := func(t *testing.T, reg *skills.SkillRegistry) *GoroutineSpawner {
		t.Helper()
		svc := &mockService{responses: []mockResponse{stopResponse("ok")}}
		return NewGoroutineSpawner(
			newMockStore(),
			func(string) (llmapi.Service, llmapi.ModelEntry, error) {
				return svc, llmapi.ModelEntry{Name: "test"}, nil
			},
			baseCfg, reg, NoMemory(), "", "s1", "agent", workspaceapi.URI{},
			noopPrompter{},
		)
	}

	t.Run("agent-type skill resolved by name", func(t *testing.T) {
		reg := makeRegistry(t, agentSkill)
		spawner := newSpawner(t, reg)
		handle, err := spawner.Run(context.Background(), RunRequest{
			AgentID: "planner",
			Message: "plan something",
		})
		require.NoError(t, err)
		reply := consumeReply(t, handle)
		assert.Equal(t, "ok", reply)
	})

	t.Run("case-insensitive match", func(t *testing.T) {
		reg := makeRegistry(t, agentSkill)
		spawner := newSpawner(t, reg)
		handle, err := spawner.Run(context.Background(), RunRequest{
			AgentID: "Planner",
			Message: "plan something",
		})
		require.NoError(t, err)
		reply := consumeReply(t, handle)
		assert.Equal(t, "ok", reply)
	})

	t.Run("non-agent skill is not matched", func(t *testing.T) {
		reg := makeRegistry(t, promptSkill)
		spawner := newSpawner(t, reg)
		_, err := spawner.Run(context.Background(), RunRequest{
			AgentID: "planner",
			Message: "plan something",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no agent or agent skill")
	})

	t.Run("unknown agent and no matching skill returns error", func(t *testing.T) {
		reg := skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil)
		spawner := newSpawner(t, reg)
		_, err := spawner.Run(context.Background(), RunRequest{
			AgentID: "nonexistent",
			Message: "hello",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no agent or agent skill")
	})

	t.Run("skill-based agent skips isAllowed check", func(t *testing.T) {
		// Use a restricted parent (no AllowAny, no AllowSpawn).
		restrictedCfg := NewConfig([]Definition{
			{ID: "parent", Name: "Parent", Model: "test-model"},
		})
		reg := makeRegistry(t, agentSkill)
		svc := &mockService{responses: []mockResponse{stopResponse("ok")}}
		spawner := NewGoroutineSpawner(
			newMockStore(),
			func(string) (llmapi.Service, llmapi.ModelEntry, error) {
				return svc, llmapi.ModelEntry{Name: "test"}, nil
			},
			restrictedCfg, reg, NoMemory(), "", "s1", "parent", workspaceapi.URI{},
			noopPrompter{},
		)
		handle, err := spawner.Run(context.Background(), RunRequest{
			AgentID: "planner",
			Message: "plan",
		})
		require.NoError(t, err, "skill-based agents must bypass isAllowed")
		consumeReply(t, handle)
	})

	t.Run("config-defined agent takes precedence over skill", func(t *testing.T) {
		// Register a config agent with same ID.
		cfgWithPlanner := NewConfig([]Definition{
			{ID: "agent", Name: "Agent", Model: "test-model", AllowAny: true},
			{ID: "planner", Name: "Config Planner", Model: "test-model"},
		})
		reg := makeRegistry(t, agentSkill)
		svc := &mockService{responses: []mockResponse{stopResponse("config-reply")}}
		spawner := NewGoroutineSpawner(
			newMockStore(),
			func(string) (llmapi.Service, llmapi.ModelEntry, error) {
				return svc, llmapi.ModelEntry{Name: "test"}, nil
			},
			cfgWithPlanner, reg, NoMemory(), "", "s1", "agent", workspaceapi.URI{},
			noopPrompter{},
		)
		handle, err := spawner.Run(context.Background(), RunRequest{
			AgentID: "planner",
			Message: "plan",
		})
		require.NoError(t, err)
		reply := consumeReply(t, handle)
		assert.Equal(t, "config-reply", reply)
	})
}

func TestGoroutineSpawner_Run_seeds_initial_messages(t *testing.T) {
	cfg := NewConfig([]Definition{
		{ID: "agent", Name: "Agent", Model: "test-model", AllowAny: true},
	})
	svc := &mockService{responses: []mockResponse{stopResponse("ok")}}
	store := newMockStore()

	spawner := NewGoroutineSpawner(
		store,
		func(string) (llmapi.Service, llmapi.ModelEntry, error) {
			return svc, llmapi.ModelEntry{Name: "test", Provider: "provider"}, nil
		},
		cfg,
		skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "",
		"session", "agent", workspaceapi.URI{},
		noopPrompter{},
	)
	spawner.GenerateDialogueID = func(_ context.Context, _ string) string {
		return "sub-agent-agent-seed"
	}

	parentMessages := []llmapi.Message{
		// The parent system prompt MUST be filtered by the caller before
		// handing messages to the spawner; the spawner itself does not
		// filter. This test passes only user/assistant turns.
		{Role: llmapi.RoleUser, Content: "Add feature X"},
		{Role: llmapi.RoleAssistant, Content: "Ok, I'll work on X"},
	}

	handle, err := spawner.Run(context.Background(), RunRequest{
		AgentID:         "agent",
		Message:         "now plan the implementation",
		SystemPrompt:    "you are a sub-agent",
		InitialMessages: parentMessages,
	})
	require.NoError(t, err)
	consumeReply(t, handle)

	d, ok := store.getDialogue("sub-agent-agent-seed")
	require.True(t, ok, "child dialogue should have been persisted")

	// Expected order: child system prompt, parent user, parent assistant,
	// then the current sub-agent user message.
	require.GreaterOrEqual(t, len(d.Messages), 4)
	assert.Equal(t, llmapi.RoleSystem, d.Messages[0].Role)
	assert.Equal(t, "you are a sub-agent", d.Messages[0].Content,
		"child system prompt must come from the skill/agent, not the parent")
	assert.Equal(t, llmapi.RoleUser, d.Messages[1].Role)
	assert.Equal(t, "Add feature X", d.Messages[1].Content)
	assert.Equal(t, llmapi.RoleAssistant, d.Messages[2].Role)
	assert.Equal(t, "Ok, I'll work on X", d.Messages[2].Content)
	assert.Equal(t, llmapi.RoleUser, d.Messages[3].Role)
	assert.Equal(t, "now plan the implementation", d.Messages[3].Content)
}

func TestGoroutineSpawnerRunPreservesDisplayModelAndAttachments(t *testing.T) {
	cfg := NewConfig([]Definition{
		{ID: "agent", Name: "Agent", Model: "test-model", AllowAny: true},
	})
	svc := &mockService{responses: []mockResponse{stopResponse("ok")}}
	store := newMockStore()
	spawner := NewGoroutineSpawner(
		store,
		func(string) (llmapi.Service, llmapi.ModelEntry, error) {
			return svc, llmapi.ModelEntry{Name: "test"}, nil
		},
		cfg, skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
		NoMemory(), "", "session", "agent", workspaceapi.URI{}, noopPrompter{},
	)
	spawner.GenerateDialogueID = func(context.Context, string) string {
		return "sub-agent-linked"
	}
	parts := []llmapi.ContentPart{{
		Type: llmapi.ContentPartTypeText, Text: "attachment body",
	}}

	handle, err := spawner.Run(context.Background(), RunRequest{
		AgentID:           "agent",
		Message:           "inspect <attachment-1>",
		DisplayMessage:    "inspect config.go",
		Attachments:       parts,
		AdditionalContext: "transient",
	})
	require.NoError(t, err)
	consumeReply(t, handle)

	d, ok := store.getDialogue("sub-agent-linked")
	require.True(t, ok)
	persisted := d.Messages[len(d.Messages)-2]
	assert.Equal(t, "inspect config.go", persisted.Content)
	require.Len(t, persisted.MultiContent, 2)
	assert.Equal(t, "inspect <attachment-1>", persisted.MultiContent[0].Text)
	assert.Equal(t, parts[0], persisted.MultiContent[1])

	request := svc.requests[0].Messages[len(svc.requests[0].Messages)-1]
	assert.Equal(t, "transient\n\ninspect <attachment-1>",
		request.MultiContent[0].Text)
	assert.Equal(t, parts[0], request.MultiContent[1])
}

// consumeReply drains the handle's events, collects EventText into
// a reply string, and closes the iterator.
func consumeReply(t *testing.T, handle RunHandle) string {
	t.Helper()
	defer handle.Events.Close() //nolint:errcheck
	var sb strings.Builder
	for {
		ev, ok := handle.Events.Next(context.Background())
		if !ok {
			break
		}
		if ev.Type == EventText {
			sb.WriteString(ev.Text)
		}
	}
	return sb.String()
}
