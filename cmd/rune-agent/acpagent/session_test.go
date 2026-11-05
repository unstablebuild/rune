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

package acpagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/taskstore"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
)

type fakeTool struct {
	name string
	run  func(ctx context.Context, args string) agent.ToolResult
}

func (f fakeTool) Definition() llmapi.Tool {
	return llmapi.Tool{Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionDefinition{
		Name: f.name, Parameters: map[string]any{"type": "object"},
	}}
}

func (f fakeTool) Execute(ctx context.Context, args string) agent.ToolResult { return f.run(ctx, args) }
func (fakeTool) Summary(args string) string                                  { return "with " + args }
func (fakeTool) NeedsDeterministicOrder() bool                               { return true }

func call(id, name, args string) llmapi.ToolCall {
	return llmapi.ToolCall{ID: id, Type: llmapi.ToolTypeFunction, Function: llmapi.FunctionCall{Name: name, Arguments: args}}
}

func text(s string) []acp.ContentBlock { return []acp.ContentBlock{acp.TextBlock(s)} }

func TestSessionPrompt(t *testing.T) {
	tools := func(AgentRequest) []agent.Tool {
		return []agent.Tool{
			fakeTool{name: "echo", run: func(_ context.Context, args string) agent.ToolResult {
				return agent.ToolResult{Content: "echo " + args}
			}},
			fakeTool{name: "fail", run: func(context.Context, string) agent.ToolResult {
				return agent.ToolResult{Content: "boom", IsError: true}
			}},
			fakeTool{name: "ask", run: func(ctx context.Context, _ string) agent.ToolResult {
				_, err := agent.PrompterFromContext(ctx).Prompt(ctx, agent.PromptRequest{
					Title: "Which one?", Kind: agent.PromptKindQuestion,
				})
				return agent.ToolResult{Content: err.Error(), IsError: true}
			}},
		}
	}
	for _, tc := range []struct {
		name      string
		responses []llmtest.Response
		want      []string
	}{
		{
			name: "text",
			responses: []llmtest.Response{{
				ReasoningChunks: []string{"hm"},
				Chunks:          []string{"hel", "lo"},
				Usage:           llmapi.Usage{TokensSent: 10, TokensReceived: 5},
			}},
			want: []string{"prompt hi", "thought hm", "text hel", "text lo", "usage 15/1000", "done end_turn"},
		},
		{
			name: "tool call",
			responses: []llmtest.Response{
				{FinishReason: llmapi.FinishReasonToolCall, ToolCalls: []llmapi.ToolCall{call("c1", "echo", `{"x":1}`)}},
				{Chunks: []string{"ok"}},
			},
			want: []string{
				"prompt hi",
				"usage 0/1000",
				`tool c1 "echo with {\"x\":1}" map[runeAgent:map[toolName:echo toolSummary:with {"x":1}]] other in_progress {"x":1} `,
				`update c1 completed echo {"x":1}`,
				"text ok",
				"usage 0/1000",
				"done end_turn",
			},
		},
		{
			name: "failed tool",
			responses: []llmtest.Response{
				{FinishReason: llmapi.FinishReasonToolCall, ToolCalls: []llmapi.ToolCall{call("c1", "fail", `{}`)}},
				{Chunks: []string{"sorry"}},
			},
			want: []string{
				"prompt hi",
				"usage 0/1000",
				`tool c1 "fail with {}" map[runeAgent:map[toolName:fail toolSummary:with {}]] other in_progress {} `,
				"update c1 failed boom",
				"text sorry",
				"usage 0/1000",
				"done end_turn",
			},
		},
		{
			name: "questions are refused",
			responses: []llmtest.Response{
				{FinishReason: llmapi.FinishReasonToolCall, ToolCalls: []llmapi.ToolCall{call("c1", "ask", `{}`)}},
				{Chunks: []string{"fine"}},
			},
			want: []string{
				"prompt hi",
				"usage 0/1000",
				`tool c1 "ask with {}" map[runeAgent:map[toolName:ask toolSummary:with {}]] other in_progress {} `,
				"update c1 failed acp: rune-agent cannot ask questions over ACP",
				"text fine",
				"usage 0/1000",
				"done end_turn",
			},
		},
		{
			name:      "max tokens",
			responses: []llmtest.Response{{Chunks: []string{"trunc"}, FinishReason: llmapi.FinishReasonLength}},
			want:      []string{"prompt hi", "text trunc", "usage 0/1000", "done max_tokens"},
		},
		{
			name:      "refusal",
			responses: []llmtest.Response{{Chunks: []string{"no"}, FinishReason: llmapi.FinishReasonRefusal}},
			want:      []string{"prompt hi", "text no", "usage 0/1000", "done refusal"},
		},
		{
			name:      "error",
			responses: []llmtest.Response{{Err: errors.New("provider down")}},
			want:      []string{"prompt hi", "done error create completion: provider down"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tools, tc.responses...)
			s := h.newSession(t)
			it := s.Events()
			defer func() { _ = it.Close() }()
			read(t, it, isCommands)

			require.NoError(t, s.Prompt(t.Context(), text("hi")))

			assert.Equal(t, tc.want, read(t, it, isDone))
			assert.False(t, logged(s).Busy)
		})
	}
}

func TestSessionPromptBusy(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(AgentRequest) []agent.Tool {
		return []agent.Tool{fakeTool{name: "wait", run: func(context.Context, string) agent.ToolResult {
			<-release
			return agent.ToolResult{Content: "done"}
		}}}
	}, llmtest.Response{FinishReason: llmapi.FinishReasonToolCall, ToolCalls: []llmapi.ToolCall{call("c1", "wait", "{}")}}, llmtest.Response{Chunks: []string{"ok"}})
	s := h.newSession(t)
	it := s.Events()
	defer func() { _ = it.Close() }()
	require.NoError(t, s.Prompt(t.Context(), text("first")))

	assert.ErrorIs(t, s.Prompt(t.Context(), text("second")), acpapi.ErrBusy)
	close(release)
	assert.Equal(t, "done end_turn", lastOf(read(t, it, isDone)))
}

func TestSessionPromptContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content []acp.ContentBlock
		want    string
		images  []string
		// system is text the request must carry beyond the prompt.
		system string
	}{
		{
			name:    "text and resource link",
			content: []acp.ContentBlock{acp.TextBlock("fix"), acp.ResourceLinkBlock("b.go", "file:///a/b.go")},
			want:    "fix\n@/a/b.go",
		},
		{
			name: "embedded resource",
			content: []acp.ContentBlock{acp.TextBlock("review"), acp.ResourceBlock(acp.EmbeddedResourceResource{
				TextResourceContents: &acp.TextResourceContents{Uri: "file:///a.go", Text: "package a"},
			})},
			want: "review\n<resource uri=\"file:///a.go\">\npackage a\n</resource>",
		},
		{
			name:    "image",
			content: []acp.ContentBlock{acp.TextBlock("what is this?"), acp.ImageBlock("AAAA", "image/png")},
			want:    "what is this?",
			images:  []string{"data:image/png;base64,AAAA"},
		},
		{
			name:    "prompt skill",
			content: text("/greet bob"),
			want:    "/greet bob",
			system:  "Greet the user warmly.",
		},
		{
			name:    "agent skills are plain text",
			content: text("/scout here"),
			want:    "/scout here",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, llmtest.Response{Chunks: []string{"ok"}})
			s := h.newSession(t)
			it := s.Events()
			defer func() { _ = it.Close() }()

			require.NoError(t, s.Prompt(t.Context(), tc.content))
			read(t, it, isDone)

			messages := h.llm.Requests()[0].Request.Messages
			i := slices.IndexFunc(messages, func(m llmapi.Message) bool {
				return m.Role == llmapi.RoleUser && strings.Contains(messageText(m), tc.want)
			})
			require.GreaterOrEqual(t, i, 0, "no user message %q in %+v", tc.want, messages)
			var images []string
			for _, p := range messages[i].MultiContent {
				if p.Type == llmapi.ContentPartTypeImageURL {
					images = append(images, p.ImageURL)
				}
			}
			assert.Equal(t, tc.images, images)
			all, err := json.Marshal(messages)
			require.NoError(t, err)
			if tc.system != "" {
				assert.Contains(t, string(all), tc.system)
			} else {
				assert.NotContains(t, string(all), "Greet the user warmly.")
				assert.NotContains(t, string(all), "Look around.")
			}
		})
	}
}

func messageText(m llmapi.Message) string {
	out := m.Content
	for _, p := range m.MultiContent {
		out += p.Text
	}
	return out
}

func TestSessionPermission(t *testing.T) {
	const permission = `permission permission-1 "Allow grep via shell?" The model wants to grep. ` +
		`[yes=Yes:allow_once always=Always:allow_always session_yes=Yes, this session:allow_once ` +
		`no=No:reject_once never=Never:reject_always session_no=Not this session:reject_once]`
	// The agent reports tool calls asynchronously, so the tool asks only
	// once its call is logged.
	tools := func(asked <-chan struct{}) func(AgentRequest) []agent.Tool {
		return func(AgentRequest) []agent.Tool {
			return []agent.Tool{fakeTool{name: "grep", run: func(ctx context.Context, _ string) agent.ToolResult {
				<-asked
				resp, err := agent.PrompterFromContext(ctx).Prompt(ctx, agent.PromptRequest{
					Title: "Allow grep via shell?",
					Body:  "The model wants to grep.",
					Kind:  agent.PromptKindPermission,
					Options: []agent.PromptOption{
						{Value: "yes", Label: "Yes"},
						{Value: "always", Label: "Always"},
						{Value: "session_yes", Label: "Yes, this session"},
						{Value: "no", Label: "No"},
						{Value: "never", Label: "Never"},
						{Value: "session_no", Label: "Not this session"},
					},
				})
				if err != nil {
					return agent.ToolResult{Content: "dismissed: " + err.Error()}
				}
				return agent.ToolResult{Content: "picked " + resp.Values[0]}
			}}}
		}
	}
	for _, tc := range []struct {
		name   string
		answer func(t *testing.T, s acpapi.Session, id acpapi.PermissionID)
		want   []string
	}{
		{
			name: "selected",
			answer: func(t *testing.T, s acpapi.Session, id acpapi.PermissionID) {
				require.NoError(t, s.Respond(t.Context(), id, acp.NewRequestPermissionOutcomeSelected("session_yes")))
			},
			want: []string{
				"resolved session_yes",
				"update c1 completed picked session_yes",
				"text ok",
				"usage 0/1000",
				"done end_turn",
			},
		},
		{
			name: "cancelled by the client",
			answer: func(t *testing.T, s acpapi.Session, id acpapi.PermissionID) {
				require.NoError(t, s.Respond(t.Context(), id, acp.NewRequestPermissionOutcomeCancelled()))
			},
			want: []string{
				"resolved cancelled",
				"update c1 completed dismissed: acp: permission cancelled",
				"text ok",
				"usage 0/1000",
				"done end_turn",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := make(chan struct{})
			h := newHarness(t, tools(asked),
				llmtest.Response{FinishReason: llmapi.FinishReasonToolCall, ToolCalls: []llmapi.ToolCall{call("c1", "grep", "{}")}},
				llmtest.Response{Chunks: []string{"ok"}})
			s := h.newSession(t)
			it := s.Events()
			defer func() { _ = it.Close() }()
			read(t, it, isCommands)
			require.NoError(t, s.Prompt(t.Context(), text("search")))
			assert.Equal(t, []string{"prompt search", "usage 0/1000", `tool c1 "grep with {}" map[runeAgent:map[toolName:grep toolSummary:with {}]] other in_progress {} `},
				read(t, it, func(ev acpapi.Event) bool { return ev.Update != nil && ev.Update.ToolCall != nil }))
			close(asked)

			assert.Equal(t, []string{permission}, read(t, it, func(ev acpapi.Event) bool { return ev.Permission != nil }))
			pending := logged(s).Pending
			require.Len(t, pending, 1)
			tc.answer(t, s, pending[0].ID)

			assert.Equal(t, tc.want, read(t, it, isDone))
		})
	}
}

func TestSessionCancel(t *testing.T) {
	tools := func(AgentRequest) []agent.Tool {
		return []agent.Tool{
			fakeTool{name: "wait", run: func(ctx context.Context, _ string) agent.ToolResult {
				<-ctx.Done()
				return agent.ToolResult{Content: "stopped"}
			}},
			fakeTool{name: "grep", run: func(ctx context.Context, _ string) agent.ToolResult {
				_, err := agent.PrompterFromContext(ctx).Prompt(ctx, agent.PromptRequest{
					Title: "Allow?", Kind: agent.PromptKindPermission,
					Options: []agent.PromptOption{{Value: "yes", Label: "Yes"}},
				})
				return agent.ToolResult{Content: err.Error()}
			}},
		}
	}
	for _, tc := range []struct {
		name string
		tool string
		// started is the event after which the turn is cancelled.
		started func(acpapi.Event) bool
		want    []string
	}{
		{
			name: "running tool",
			tool: "wait",
			started: func(ev acpapi.Event) bool {
				return ev.Update != nil && ev.Update.ToolCall != nil
			},
		},
		{
			name:    "pending permission",
			tool:    "grep",
			started: func(ev acpapi.Event) bool { return ev.Permission != nil },
			want:    []string{"resolved cancelled"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, tools, llmtest.Response{FinishReason: llmapi.FinishReasonToolCall, ToolCalls: []llmapi.ToolCall{call("c1", tc.tool, "{}")}})
			s := h.newSession(t)
			it := s.Events()
			defer func() { _ = it.Close() }()
			require.NoError(t, s.Prompt(t.Context(), text("go")))
			read(t, it, tc.started)

			require.NoError(t, s.Cancel(t.Context()))

			got := read(t, it, isDone)
			assert.Equal(t, "done cancelled", lastOf(got))
			for _, want := range tc.want {
				assert.Contains(t, got, want)
			}
			assert.Empty(t, logged(s).Pending)
		})
	}
}

func TestSessionCancelIdle(t *testing.T) {
	h := newHarness(t, nil, llmtest.Response{Chunks: []string{"ok"}})
	s := h.newSession(t)
	it := s.Events()
	defer func() { _ = it.Close() }()
	read(t, it, isCommands)

	require.NoError(t, s.Cancel(t.Context()))
	require.NoError(t, s.Prompt(t.Context(), text("hi")))

	assert.Equal(t, "done end_turn", lastOf(read(t, it, isDone)), "an idle cancel does not end the next turn")
}

func TestSessionSetConfigOption(t *testing.T) {
	selectValue := func(id acp.SessionConfigId, value acp.SessionConfigValueId) acp.SetSessionConfigOptionRequest {
		return acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{ConfigId: id, Value: value}}
	}
	for _, tc := range []struct {
		name    string
		reqs    []acp.SetSessionConfigOptionRequest
		want    []string
		wantErr string
		model   string
		effort  llmapi.ReasoningEffort
	}{
		{
			name:  "model",
			reqs:  []acp.SetSessionConfigOptionRequest{selectValue("model", "fake/large")},
			want:  []string{"options model=fake/large effort=default"},
			model: "large",
		},
		{
			name:   "effort",
			reqs:   []acp.SetSessionConfigOptionRequest{selectValue("effort", "high")},
			want:   []string{"options model=fake/small effort=high"},
			model:  "small",
			effort: "high",
		},
		{
			name: "default effort",
			reqs: []acp.SetSessionConfigOptionRequest{
				selectValue("effort", "high"), selectValue("effort", "default"),
			},
			want:  []string{"options model=fake/small effort=high", "options model=fake/small effort=default"},
			model: "small",
		},
		{
			name:    "unknown model",
			reqs:    []acp.SetSessionConfigOptionRequest{selectValue("model", "fake/huge")},
			wantErr: `model "fake/huge" is not available`,
		},
		{
			name:    "unknown effort",
			reqs:    []acp.SetSessionConfigOptionRequest{selectValue("effort", "turbo")},
			wantErr: `unknown effort "turbo"`,
		},
		{
			name:    "unknown option",
			reqs:    []acp.SetSessionConfigOptionRequest{selectValue("mode", "plan")},
			wantErr: acpapi.ErrNotFound.Error(),
		},
		{
			name: "boolean option",
			reqs: []acp.SetSessionConfigOptionRequest{{Boolean: &acp.SetSessionConfigOptionBoolean{
				ConfigId: "fast", Value: true,
			}}},
			wantErr: "no boolean options",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, llmtest.Response{Chunks: []string{"ok"}})
			s := h.newSession(t)
			it := s.Events()
			defer func() { _ = it.Close() }()
			read(t, it, isCommands)

			var got []string
			for _, req := range tc.reqs {
				err := s.SetConfigOption(t.Context(), req)
				if tc.wantErr != "" {
					require.ErrorContains(t, err, tc.wantErr)
					return
				}
				require.NoError(t, err)
				got = append(got, read(t, it, func(acpapi.Event) bool { return true })...)
			}

			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.effort, s.(*session).ag.Effort())
			require.NoError(t, s.Prompt(t.Context(), text("hi")))
			read(t, it, isDone)
			assert.Equal(t, tc.model, h.llm.Requests()[0].Model.Name)
		})
	}
}

func TestSessionPlan(t *testing.T) {
	type track struct{ ID, Subject, Status string }
	tools := func(req AgentRequest) []agent.Tool {
		return []agent.Tool{fakeTool{name: "track", run: func(ctx context.Context, args string) agent.ToolResult {
			var tr track
			if err := json.Unmarshal([]byte(args), &tr); err != nil {
				return agent.ToolResult{Content: err.Error(), IsError: true}
			}
			req.Progress.UpdateTaskProgress(ctx, taskstore.Task{ID: tr.ID, Subject: tr.Subject, Status: tr.Status})
			return agent.ToolResult{Content: "ok"}
		}}}
	}
	h := newHarness(t, tools, llmtest.Response{FinishReason: llmapi.FinishReasonToolCall, ToolCalls: []llmapi.ToolCall{
		call("c1", "track", `{"ID":"1","Subject":"write tests","Status":"pending"}`),
		call("c2", "track", `{"ID":"2","Subject":"fix bug","Status":"in_progress"}`),
		call("c3", "track", `{"ID":"1","Subject":"write tests","Status":"completed"}`),
		call("c4", "track", `{"ID":"2","Subject":"fix bug","Status":"deleted"}`),
	}}, llmtest.Response{Chunks: []string{"done"}})
	s := h.newSession(t)
	it := s.Events()
	defer func() { _ = it.Close() }()
	require.NoError(t, s.Prompt(t.Context(), text("plan")))

	var plans []string
	for _, ev := range read(t, it, isDone) {
		if strings.HasPrefix(ev, "plan ") {
			plans = append(plans, ev)
		}
	}

	assert.Equal(t, []string{
		"plan write tests:pending",
		"plan write tests:pending,fix bug:in_progress",
		"plan write tests:completed,fix bug:in_progress",
		"plan write tests:completed",
	}, plans)
}

func TestSessionClose(t *testing.T) {
	h := newHarness(t, nil)
	s := h.newSession(t)
	it := s.Events()
	defer func() { _ = it.Close() }()

	require.NoError(t, s.Close())

	events, err := iterator.ToSlice(t.Context(), it)
	require.NoError(t, err)
	assert.Len(t, events, 2, "the session ends after its opening events")
	assert.ErrorIs(t, s.Prompt(t.Context(), text("hi")), acpapi.ErrClosed)
	h.agent.mu.Lock()
	assert.Empty(t, h.agent.sessions)
	h.agent.mu.Unlock()
	assert.NoError(t, s.Close())
}

func lastOf(events []string) string {
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1]
}
