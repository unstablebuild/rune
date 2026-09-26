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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
	"unstable.build/rune/internal/llm/anthropic"
	"unstable.build/rune/internal/llm/codex"
	"unstable.build/rune/internal/llm/gemini"
)

// newMaxTokensAdapter builds a commandAdapter whose agent is bound to a
// concrete model so handleMaxTokens can validate against its documented
// output ceiling.
func newMaxTokensAdapter(t *testing.T, model llmapi.ModelEntry) *commandAdapter {
	t.Helper()
	svc := llmtest.New([]llmapi.ModelEntry{model})
	registry := agent.NewRegistry()
	skillReg := skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil)
	store := newMemDialogueStore()
	ag := agent.NewAgent(svc, registry, skillReg, store, agent.NoMemory(), agent.Config{
		SystemPrompt: "test",
		Model:        model,
	})
	return &commandAdapter{agent: ag}
}

// /max_tokens above the bound model's documented ceiling is rejected and
// leaves the agent override unchanged; a value at/under the ceiling sets it.
func TestCommandAdapterMaxTokensValidatesAgainstModel(t *testing.T) {
	model := llmapi.ModelEntry{
		Provider: anthropic.LLMProvider, Name: anthropic.ClaudeFable5, ContextWindow: 1_000_000,
	}

	a := newMaxTokensAdapter(t, model)
	_, err := a.handleMaxTokens([]string{"200000"})
	require.Error(t, err)
	assert.Equal(t,
		"claude-fable-5 supports at most 128000 max output tokens; 200000 is too large",
		err.Error())
	assert.Equal(t, 0, a.agent.MaxOutputTokens(), "rejected value must not be applied")

	_, err = a.handleMaxTokens([]string{"64000"})
	require.NoError(t, err)
	assert.Equal(t, 64000, a.agent.MaxOutputTokens())
}

func TestCommandAdapterMaxTokensRejectsCodex(t *testing.T) {
	model := llmapi.ModelEntry{
		Provider: codex.LLMProvider, Name: codex.GPT5Dot6Sol, ContextWindow: 872_000,
	}

	a := newMaxTokensAdapter(t, model)
	_, err := a.handleMaxTokens([]string{"4096"})
	require.EqualError(t, err,
		"codex/gpt-5.6-sol does not support custom max output token limits")
	assert.Equal(t, 0, a.agent.MaxOutputTokens(), "rejected value must not be applied")
}

func TestCommandAdapterMaxTokensValidatesCurrentGeminiFlashModels(t *testing.T) {
	for _, name := range []string{gemini.Gemini_3_6_Flash, gemini.Gemini_3_5_FlashLite} {
		t.Run(name, func(t *testing.T) {
			model := llmapi.ModelEntry{
				Provider: gemini.LLMProvider, Name: name, ContextWindow: 1_048_576,
			}

			a := newMaxTokensAdapter(t, model)
			_, err := a.handleMaxTokens([]string{"65537"})
			require.EqualError(t, err,
				name+" supports at most 65536 max output tokens; 65537 is too large")
			assert.Equal(t, 0, a.agent.MaxOutputTokens(), "rejected value must not be applied")

			_, err = a.handleMaxTokens([]string{"65536"})
			require.NoError(t, err)
			assert.Equal(t, 65536, a.agent.MaxOutputTokens())
		})
	}
}

// A chat agent carries no injected effort default, so /effort with no
// argument must report that the model default applies rather than claiming
// a level the user never set.
func TestCommandAdapterEffortReportsModelDefault(t *testing.T) {
	model := llmapi.ModelEntry{
		Provider: anthropic.LLMProvider, Name: anthropic.ClaudeFable5, ContextWindow: 1_000_000,
	}
	a := newMaxTokensAdapter(t, model)
	require.Empty(t, a.agent.Effort(), "fresh agent must not carry an injected effort")

	res, err := a.handleEffort(nil)
	require.NoError(t, err)
	assert.Contains(t, renderDisplay(t, res.Display), "model default")

	_, err = a.handleEffort([]string{"low"})
	require.NoError(t, err)
	assert.Equal(t, llmapi.ReasoningEffortLow, a.agent.Effort())
}

func renderDisplay(t *testing.T, it iterator.Iterator[component.Responsive]) string {
	t.Helper()
	w := term.NewStringWriter(60, 4)
	for {
		comp, ok := it.Next(t.Context())
		if !ok {
			break
		}
		comp.Resize(60, comp.Height(60))
		comp.Draw(w)
	}
	require.NoError(t, w.Flush())
	return w.String()
}

// captureCommandHandler records the last repl.Command it received and
// yields no output components.
type captureCommandHandler struct {
	last repl.Command
}

func (c *captureCommandHandler) HandleCommand(
	_ context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	c.last = cmd
	return iterator.FromSlice[component.Responsive](nil), nil
}

func (c *captureCommandHandler) Complete(
	_ context.Context, _ string, _ []string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice[string](nil), nil
}

func newCaptureAdapter(h repl.CommandHandler) *commandAdapter {
	return &commandAdapter{
		handler:       h,
		dialogueID:    "rolling-fox",
		skillRegistry: skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil),
	}
}

// Chat commands act on the open chat only: each must dispatch the
// equivalent shell subcommand with the adapter's own dialogue id and no
// caller-supplied positional id.
func TestCommandAdapterScopesToOpenChat(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantName string
		wantArgs []string
	}{
		{"history", nil, "chats", []string{"show", "rolling-fox"}},
		{"log", nil, "chats", []string{"log", "rolling-fox"}},
		{"fork", nil, "chats", []string{"fork", "rolling-fox"}},
		{"export", nil, "chats", []string{"export", "rolling-fox"}},
		{"export", []string{"--audit"}, "chats", []string{"export", "--audit", "rolling-fox"}},
		{"rename", nil, "chats", []string{"rename", "rolling-fox"}},
		{"rename", []string{"my", "title"}, "chats", []string{"rename", "rolling-fox", "my", "title"}},
	}
	for _, tc := range tests {
		t.Run(tc.name+strings.Join(tc.args, ""), func(t *testing.T) {
			h := &captureCommandHandler{}
			a := newCaptureAdapter(h)
			_, err := a.HandleCommand(context.Background(), tc.name, tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.wantName, h.last.Name)
			assert.Equal(t, tc.wantArgs, h.last.Args)
		})
	}
}

func TestCommandAdapterCompactScopesToOpenChat(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantArgs []string
	}{
		{"default", nil, []string{"compact", "rolling-fox"}},
		{"model", []string{"openai/gpt-5.5"}, []string{"compact", "rolling-fox", "openai/gpt-5.5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &captureCommandHandler{}
			a := newCaptureAdapter(h)
			res, err := a.HandleCommand(context.Background(), "compact", tc.args)
			require.NoError(t, err)
			require.NotNil(t, res.Display)
			_, _ = res.Display.Next(context.Background())
			assert.Equal(t, "chats", h.last.Name)
			assert.Equal(t, tc.wantArgs, h.last.Args)
		})
	}

	a := newCaptureAdapter(&captureCommandHandler{})
	_, err := a.HandleCommand(context.Background(), "compact", []string{"openai/gpt-5", "codex/gpt-5"})
	require.Error(t, err)
}

// Any positional dialogue id is rejected; chat commands no longer
// target other dialogues.
func TestCommandAdapterRejectsPositionalID(t *testing.T) {
	for _, name := range []string{
		"clear", "history", "export", "log", "fork", "reviewchanges",
	} {
		t.Run(name, func(t *testing.T) {
			h := &captureCommandHandler{}
			a := newCaptureAdapter(h)
			_, err := a.HandleCommand(context.Background(), name, []string{"other-chat"})
			require.Error(t, err)
		})
	}
}

// Bare names must not appear in model-argument completions: two providers
// can ship the same Name (e.g. openai/gpt-5.5 vs codex/gpt-5.5),
// and a bare candidate would silently dispatch to whichever provider
// Models() iterates first.
func TestCommandAdapterModelArgumentCompleters(t *testing.T) {
	svc := llmtest.New([]llmapi.ModelEntry{
		{Provider: "openai", Name: "gpt-5.5"},
		{Provider: "codex", Name: "gpt-5.5"},
		{Provider: "openai", Name: "gpt-4o"},
	})
	a := &commandAdapter{llmSvc: svc}
	ctx := context.Background()
	for _, name := range []string{"model", "compact"} {
		t.Run(name, func(t *testing.T) {
			it, err := a.Complete(ctx, name, nil)
			require.NoError(t, err)
			got, err := iterator.ToSlice(ctx, it)
			require.NoError(t, err)
			assert.Equal(t, []string{
				"openai/gpt-5.5",
				"codex/gpt-5.5",
				"openai/gpt-4o",
			}, got)
		})
	}
}

// aliasResolvingService resolves the bare name "default" to a
// fully-qualified entry, mirroring how the host router exposes alias
// resolution through GetModel with an empty Provider.
type aliasResolvingService struct {
	*llmtest.Service
	target llmapi.ModelEntry
}

func (s *aliasResolvingService) GetModel(
	_ context.Context, model llmapi.ModelEntry,
) (llmapi.ModelEntry, error) {
	if model.Provider == "" && model.Name == "default" {
		return s.target, nil
	}
	return s.Service.GetModel(context.Background(), model)
}

// /model with no args must show the resolved provider/model, not the
// bare alias the session was created with (e.g. "default").
func TestCommandAdapterModelResolvesAliasLabel(t *testing.T) {
	target := llmapi.ModelEntry{Provider: "openai", Name: "gpt-5.5"}
	svc := &aliasResolvingService{
		Service: llmtest.New([]llmapi.ModelEntry{target}),
		target:  target,
	}
	a := &commandAdapter{llmSvc: svc, currentModel: "default"}

	assert.Equal(t, "openai/gpt-5.5", a.resolvedModelLabel(context.Background()))
}

// Slash-command preloads ship the skill body as a separate system
// message, which the model reliably misses. formatSkillMessage must
// include an inline cue telling the model to invoke the skill tool so
// the body actually gets attended to.
func TestFormatSkillMessageIncludesSkillToolHint(t *testing.T) {
	skill := skills.Skill{Name: "perplexity-search"}

	msg := formatSkillMessage(skill, "pico go to line")

	assert.Contains(t, msg, "<command-message>perplexity-search</command-message>")
	assert.Contains(t, msg, "<command-name>/perplexity-search</command-name>")
	assert.Contains(t, msg, "pico go to line")
	assert.Contains(t, msg, "<command-hint>")
	assert.Contains(t, msg, "</command-hint>")
	assert.Contains(t, msg, "perplexity-search")
	// The hint must mention the skill tool by name so the model knows
	// what action to take.
	hintStart := strings.Index(msg, "<command-hint>")
	hintEnd := strings.Index(msg, "</command-hint>")
	require.Greater(t, hintEnd, hintStart)
	hint := msg[hintStart+len("<command-hint>") : hintEnd]
	assert.Contains(t, hint, "skill")
}

// parseStoredCommandMessage must continue to recover the slash-command
// name and args from history even when the message carries the new
// command-hint envelope.
func TestParseStoredCommandMessageRoundTripsWithHint(t *testing.T) {
	tests := []struct {
		name     string
		skill    skills.Skill
		args     string
		expected string
	}{
		{
			name:     "no args",
			skill:    skills.Skill{Name: "clear"},
			args:     "",
			expected: "/clear",
		},
		{
			name:     "single-line args",
			skill:    skills.Skill{Name: "perplexity-search"},
			args:     "pico go to line",
			expected: "/perplexity-search pico go to line",
		},
		{
			name:     "multi-line args",
			skill:    skills.Skill{Name: "issue-create"},
			args:     "line one\nline two",
			expected: "/issue-create\nline one\nline two",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg := formatSkillMessage(tc.skill, tc.args)
			got, ok := parseStoredCommandMessage(msg)
			require.True(t, ok, "parse should succeed for %q", msg)
			assert.Equal(t, tc.expected, got)
		})
	}
}

// Same hazard as TestCommandAdapterModelCompleter, for :chat / :query.
func TestHandlerModelCompleter(t *testing.T) {
	svc := llmtest.New([]llmapi.ModelEntry{
		{Provider: "openai", Name: "gpt-5.5"},
		{Provider: "codex", Name: "gpt-5.5"},
		{Provider: "openai", Name: "gpt-4o"},
	})
	h := &aiEditorHandler{llmSvc: svc}
	ctx := context.Background()
	it, err := h.completeWithModelsIterator(ctx)
	require.NoError(t, err)
	got, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	assert.Equal(t, []string{
		"openai/gpt-5.5",
		"codex/gpt-5.5",
		"openai/gpt-4o",
	}, got)
}
