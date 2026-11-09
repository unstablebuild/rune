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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
)

var models = []llmapi.ModelEntry{
	{Name: "small", Provider: "fake", ContextWindow: 1000},
	{Name: "large", Provider: "fake", ContextWindow: 2000},
}

type harness struct {
	llm   *llmtest.Service
	store dialoguemanager.Store
	agent *Agent
	cwd   string
}

// newHarness serves sessions whose agents run on scripted responses with
// the tools returns, and offer the skills greet (a prompt skill) and scout
// (an agent skill).
func newHarness(t *testing.T, tools func(AgentRequest) []agent.Tool, responses ...llmtest.Response) *harness {
	t.Helper()
	h := &harness{
		llm:   llmtest.New(models, responses...),
		store: dialoguemanager.NewStore(storagestub.NewInMemoryService(), t.TempDir()),
		cwd:   t.TempDir(),
	}
	skillsDir := t.TempDir()
	writeSkill(t, skillsDir, "greet", "Say hello", "", "Greet the user warmly.")
	writeSkill(t, skillsDir, "scout", "Explore", "agent", "Look around.")
	h.agent = New(Config{
		Info:  acp.Implementation{Name: "rune-agent", Version: "test"},
		LLM:   h.llm,
		Store: h.store,
		Model: "fake/small",
		NewAgent: func(_ context.Context, req AgentRequest) (*agent.Agent, *skills.SkillRegistry, error) {
			cwd, err := workspaceapi.CurrentUserHostURI(req.Cwd)
			if err != nil {
				return nil, nil, err
			}
			var ts []agent.Tool
			if tools != nil {
				ts = tools(req)
			}
			reg := skills.NewRegistry(osFS{}, cwd, []string{skillsDir}, nil)
			return agent.NewAgent(h.llm, agent.NewRegistry(ts...), reg, h.store, agent.NoMemory(), agent.Config{
				SystemPrompt: "You are a test.",
				Model:        req.Model,
				Workspace:    cwd,
				Prompter:     req.Prompter,
			}), reg, nil
		},
	})
	t.Cleanup(func() { _ = h.agent.Close() })
	return h
}

func (h *harness) newSession(t *testing.T) acpapi.Session {
	t.Helper()
	s, err := h.agent.NewSession(t.Context(), acp.NewSessionRequest{Cwd: h.cwd})
	require.NoError(t, err)
	return s
}

func writeSkill(t *testing.T, dir, name, description, kind, body string) {
	t.Helper()
	front := fmt.Sprintf("---\nname: %s\ndescription: %s\n", name, description)
	if kind != "" {
		front += "type: " + kind + "\n"
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, name), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(front+"---\n"+body+"\n"), 0o644))
}

type osFS struct{}

func (osFS) URI(path string) (workspaceapi.URI, error) { return workspaceapi.CurrentUserHostURI(path) }
func (osFS) OpenFile(path string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(path, flag, mode)
}
func (osFS) Remove(path string) error                     { return os.Remove(path) }
func (osFS) Stat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func (osFS) ReadDir(name string) ([]os.DirEntry, error)   { return os.ReadDir(name) }
func (osFS) MkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }

// read describes it's events up to and including the first stop matches.
func read(t *testing.T, it iterator.Iterator[acpapi.Event], stop func(acpapi.Event) bool) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var out []string
	for {
		ev, ok := it.Next(ctx)
		require.True(t, ok, "events ended after %q: %v", out, it.Err())
		out = append(out, describe(ev))
		if stop(ev) {
			return out
		}
	}
}

func isDone(ev acpapi.Event) bool { return ev.Done != nil }

func isCommands(ev acpapi.Event) bool {
	return ev.Update != nil && ev.Update.AvailableCommandsUpdate != nil
}

func describe(ev acpapi.Event) string {
	switch {
	case ev.Started != nil:
		return "started " + describeOptions(ev.Started.ConfigOptions)
	case ev.Prompt != nil:
		return "prompt " + blockText(ev.Prompt.Content...)
	case ev.Permission != nil:
		var opts []string
		for _, o := range ev.Permission.Request.Options {
			opts = append(opts, fmt.Sprintf("%s=%s:%s", o.OptionId, o.Name, o.Kind))
		}
		call := ev.Permission.Request.ToolCall
		return fmt.Sprintf("permission %s %q %s [%s]",
			call.ToolCallId, *call.Title, toolText(call.Content), strings.Join(opts, " "))
	case ev.Resolved != nil:
		if o := ev.Resolved.Outcome.Selected; o != nil {
			return "resolved " + string(o.OptionId)
		}
		return "resolved cancelled"
	case ev.Done != nil:
		if ev.Done.Error != "" {
			return "done error " + ev.Done.Error
		}
		return "done " + string(ev.Done.StopReason)
	}
	u := ev.Update
	switch {
	case u.UserMessageChunk != nil:
		return "user " + blockText(u.UserMessageChunk.Content)
	case u.AgentMessageChunk != nil:
		return "text " + blockText(u.AgentMessageChunk.Content)
	case u.AgentThoughtChunk != nil:
		return "thought " + blockText(u.AgentThoughtChunk.Content)
	case u.ToolCall != nil:
		return fmt.Sprintf("tool %s %q %v %s %s %v %s", u.ToolCall.ToolCallId, u.ToolCall.Title, u.ToolCall.Meta,
			u.ToolCall.Kind, u.ToolCall.Status, rawText(u.ToolCall.RawInput), toolText(u.ToolCall.Content))
	case u.ToolCallUpdate != nil:
		return fmt.Sprintf("update %s %s %s", u.ToolCallUpdate.ToolCallId,
			*u.ToolCallUpdate.Status, toolText(u.ToolCallUpdate.Content))
	case u.UsageUpdate != nil:
		return fmt.Sprintf("usage %d/%d", u.UsageUpdate.Used, u.UsageUpdate.Size)
	case u.Plan != nil:
		var entries []string
		for _, e := range u.Plan.Entries {
			entries = append(entries, e.Content+":"+string(e.Status))
		}
		return "plan " + strings.Join(entries, ",")
	case u.AvailableCommandsUpdate != nil:
		return "commands"
	case u.ConfigOptionUpdate != nil:
		return "options " + describeOptions(u.ConfigOptionUpdate.ConfigOptions)
	}
	return "unknown"
}

func describeOptions(options []acp.SessionConfigOption) string {
	var out []string
	for _, o := range options {
		out = append(out, fmt.Sprintf("%s=%s", o.Select.Id, o.Select.CurrentValue))
	}
	return strings.Join(out, " ")
}

func blockText(blocks ...acp.ContentBlock) string {
	var out []string
	for _, b := range blocks {
		if b.Text != nil {
			out = append(out, b.Text.Text)
		}
	}
	return strings.Join(out, "|")
}

func toolText(content []acp.ToolCallContent) string {
	var out []string
	for _, c := range content {
		if c.Content != nil {
			out = append(out, blockText(c.Content.Content))
		}
	}
	return strings.Join(out, "|")
}

func rawText(v any) string {
	if raw, ok := v.([]byte); ok {
		return string(raw)
	}
	if raw, ok := v.(interface{ MarshalJSON() ([]byte, error) }); ok {
		b, _ := raw.MarshalJSON()
		return string(b)
	}
	return fmt.Sprint(v)
}

func TestAgentInfo(t *testing.T) {
	h := newHarness(t, nil)

	info := h.agent.Info()

	assert.Equal(t, acp.ProtocolVersion(acp.ProtocolVersionNumber), info.ProtocolVersion)
	assert.Equal(t, &acp.Implementation{Name: "rune-agent", Version: "test"}, info.AgentInfo)
	assert.True(t, info.AgentCapabilities.LoadSession)
	assert.True(t, info.AgentCapabilities.PromptCapabilities.Image)
	assert.True(t, info.AgentCapabilities.PromptCapabilities.EmbeddedContext)
	assert.NotNil(t, info.AgentCapabilities.SessionCapabilities.List)
	assert.NotNil(t, info.AgentCapabilities.SessionCapabilities.Close)
}

func TestAgentNewSession(t *testing.T) {
	h := newHarness(t, nil)
	s := h.newSession(t)
	it := s.Events()
	defer func() { _ = it.Close() }()

	assert.Equal(t, []string{"started model=fake/small effort=default", "commands"}, read(t, it, isCommands))
	state := logged(s)
	require.Len(t, state.ConfigOptions, 2)
	model := state.ConfigOptions[0].Select
	assert.Equal(t, acp.SessionConfigOptionCategoryModel, *model.Category)
	assert.Equal(t, acp.SessionConfigSelectOptionsUngrouped{
		{Value: "fake/small", Name: "small", Description: new("fake")},
		{Value: "fake/large", Name: "large", Description: new("fake")},
	}, *model.Options.Ungrouped)
	effort := state.ConfigOptions[1].Select
	assert.Equal(t, acp.SessionConfigOptionCategoryThoughtLevel, *effort.Category)
	var efforts []acp.SessionConfigValueId
	for _, o := range *effort.Options.Ungrouped {
		efforts = append(efforts, o.Value)
	}
	assert.Equal(t, []acp.SessionConfigValueId{
		"default", "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra",
	}, efforts)

	names := map[string]acp.AvailableCommand{}
	for _, c := range state.Commands {
		names[c.Name] = c
	}
	require.Contains(t, names, "greet")
	assert.Equal(t, "Say hello", names["greet"].Description)
	assert.Equal(t, "instructions", names["greet"].Input.Unstructured.Hint)
	assert.NotContains(t, names, "scout", "agent skills need subagents")
}

func TestAgentNewSessionErrors(t *testing.T) {
	h := newHarness(t, nil)

	_, err := h.agent.NewSession(t.Context(), acp.NewSessionRequest{Cwd: "relative"})
	assert.ErrorContains(t, err, `cwd "relative" is not an absolute path`)

	h.agent.cfg.Model = "fake/huge"
	_, err = h.agent.NewSession(t.Context(), acp.NewSessionRequest{Cwd: h.cwd})
	assert.ErrorContains(t, err, `model "fake/huge" is not available`)
}

func TestAgentLoadSession(t *testing.T) {
	h := newHarness(t, nil, llmtest.Response{Chunks: []string{"welcome back"}})
	cwd, err := workspaceapi.CurrentUserHostURI(h.cwd)
	require.NoError(t, err)
	require.NoError(t, h.store.Create(t.Context(), dialoguemanager.Dialogue{
		ID:           "s1",
		WorkspaceURI: cwd.String(),
		Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "You are a test."},
			{Role: llmapi.RoleUser, Content: "hello"},
			{Role: llmapi.RoleAssistant, ReasoningContent: "hm", ToolCalls: []llmapi.ToolCall{{
				ID: "c1", Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
			}}},
			{Role: llmapi.RoleTool, ToolCallID: "c1", Content: "package a"},
			{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{
				llmapi.NewContentPartFromImageURL("data:image/png;base64,AA=="),
			}},
			{Role: llmapi.RoleAssistant, Content: "hi"},
			{Role: llmapi.RoleUser, Content: "again"},
			{Role: llmapi.RoleAssistant, Content: "sure"},
		},
	}))

	s, err := h.agent.LoadSession(t.Context(), acp.LoadSessionRequest{SessionId: "s1", Cwd: h.cwd})
	require.NoError(t, err)
	it := s.Events()
	defer func() { _ = it.Close() }()

	assert.Equal(t, acpapi.SessionInfo{ID: "s1"}, s.Info())
	assert.Equal(t, []string{
		"prompt hello",
		"thought hm",
		`tool c1 "read_file" map[runeAgent:map[toolName:read_file]] read completed {"path":"a.go"} package a`,
		"text hi",
		"done end_turn",
		"prompt again",
		"text sure",
		"done end_turn",
		"started model=fake/small effort=default",
		"commands",
	}, read(t, it, isCommands))

	require.NoError(t, s.Prompt(t.Context(), []acp.ContentBlock{acp.TextBlock("still there?")}))
	assert.Equal(t, []string{"prompt still there?", "text welcome back", "usage 0/1000", "done end_turn"},
		read(t, it, isDone))
	var history []string
	for _, m := range h.llm.Requests()[0].Request.Messages {
		if m.Content != "" {
			history = append(history, string(m.Role)+":"+m.Content)
		}
	}
	assert.Subset(t, history, []string{"user:hello", "assistant:hi", "user:again", "assistant:sure"},
		"the agent continues the stored dialogue")
}

func TestAgentLoadSessionErrors(t *testing.T) {
	h := newHarness(t, nil)

	_, err := h.agent.LoadSession(t.Context(), acp.LoadSessionRequest{SessionId: "nope", Cwd: h.cwd})
	assert.ErrorIs(t, err, acpapi.ErrNotFound)

	s := h.newSession(t)
	_, err = h.agent.LoadSession(t.Context(), acp.LoadSessionRequest{SessionId: s.Info().ID, Cwd: h.cwd})
	assert.ErrorIs(t, err, acpapi.ErrNotFound, "a session is stored once it has a turn")
}

func TestAgentListSessions(t *testing.T) {
	h := newHarness(t, nil)
	for _, d := range []dialoguemanager.Dialogue{
		{ID: "a", WorkspaceURI: "file:///work"},
		{ID: "b", WorkspaceURI: "file:///elsewhere"},
		{ID: "a-archived", WorkspaceURI: "file:///work"},
		{ID: "child", WorkspaceURI: "file:///work", SubAgent: true},
	} {
		d.Messages = []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi"}}
		require.NoError(t, h.store.Create(t.Context(), d))
	}

	for _, tc := range []struct {
		name string
		cwd  *string
		want []string
	}{
		{name: "all", want: []string{"/elsewhere:b", "/work:a"}},
		{name: "in cwd", cwd: new("/work"), want: []string{"/work:a"}},
		{name: "none in cwd", cwd: new("/nowhere")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions, err := iterator.ToSlice(t.Context(),
				h.agent.ListSessions(t.Context(), acp.ListSessionsRequest{Cwd: tc.cwd}))
			require.NoError(t, err)

			var got []string
			for _, s := range sessions {
				require.NotNil(t, s.UpdatedAt)
				_, err := time.Parse(time.RFC3339, *s.UpdatedAt)
				require.NoError(t, err)
				got = append(got, s.Cwd+":"+string(s.SessionId))
			}
			assert.ElementsMatch(t, tc.want, got)
		})
	}
}

func TestAgentClose(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(AgentRequest) []agent.Tool {
		return []agent.Tool{fakeTool{name: "wait", run: func(ctx context.Context, _ string) agent.ToolResult {
			close(release)
			<-ctx.Done()
			return agent.ToolResult{Content: "stopped"}
		}}}
	}, llmtest.Response{FinishReason: llmapi.FinishReasonToolCall, ToolCalls: []llmapi.ToolCall{call("c1", "wait", "{}")}})
	s := h.newSession(t)
	it := s.Events()
	defer func() { _ = it.Close() }()
	require.NoError(t, s.Prompt(t.Context(), []acp.ContentBlock{acp.TextBlock("wait")}))
	read(t, it, func(ev acpapi.Event) bool { return ev.Update != nil && ev.Update.ToolCall != nil })
	<-release

	require.NoError(t, h.agent.Close())

	select {
	case <-h.agent.Done():
	default:
		t.Fatal("Done is open after Close")
	}
	_, err := iterator.ToSlice(t.Context(), it)
	require.NoError(t, err, "a closed session ends its events cleanly")
	d, err := h.store.Get(t.Context(), string(s.Info().ID))
	require.NoError(t, err, "Close waits for the turn to be stored")
	assert.Equal(t, "wait", d.Messages[1].Content)
	_, err = h.agent.NewSession(t.Context(), acp.NewSessionRequest{Cwd: h.cwd})
	assert.ErrorIs(t, err, acpapi.ErrClosed)
	assert.ErrorIs(t, s.Prompt(t.Context(), []acp.ContentBlock{acp.TextBlock("again")}), acpapi.ErrClosed)
	assert.NoError(t, h.agent.Close())
}

// logged folds the events s has logged so far.
func logged(s acpapi.Session) acpapi.SessionState {
	// An expired context makes Next return only what is already logged.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	it := s.Events()
	defer func() { _ = it.Close() }()
	var state acpapi.SessionState
	for {
		ev, ok := it.Next(ctx)
		if !ok {
			return state
		}
		acpapi.Apply(&state, ev)
	}
}
