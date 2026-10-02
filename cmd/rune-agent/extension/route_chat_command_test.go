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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
)

func mustURI(t *testing.T, s string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI(s)
	require.NoError(t, err)
	return u
}

func TestDialogueIDFromURI(t *testing.T) {
	tests := []struct {
		name   string
		uri    string
		wantID string
		wantOK bool
	}{
		{"valid", "rune-agent://openai_gpt-5/rolling-fox", "rolling-fox", true},
		{"escaped model", "rune-agent://hf.co_org_model/quiet-owl", "quiet-owl", true},
		{"no id", "rune-agent://openai_gpt-5/", "", false},
		{"missing path", "rune-agent://openai_gpt-5", "", false},
		{"other scheme", "file:///tmp/foo", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var uri workspaceapi.URI
			if tc.uri != "" {
				uri = mustURI(t, tc.uri)
			}
			id, ok := dialogueIDFromURI(uri)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantID, id)
		})
	}
}

func TestRouteChatCommandDelivers(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		args     []string
		wantName string
		wantArgs []string
	}{
		{"model", commandModel, []string{"openai/gpt-5"}, "model", []string{"openai/gpt-5"}},
		{"effort", commandEffort, []string{"high"}, "effort", []string{"high"}},
		{"maxtokens", commandMaxTokens, []string{"4096"}, "max_tokens", []string{"4096"}},
		{"skill", commandSkill, []string{"review", "foo"}, "review", []string{"foo"}},
		{"clear", commandClear, nil, "clear", nil},
		{"compact", commandCompact, nil, "compact", nil},
		{"compact model", commandCompact, []string{"openai/gpt-5"}, "compact", []string{"openai/gpt-5"}},
		{"fork", commandFork, nil, "fork", nil},
		{"reviewchanges", commandReviewChanges, nil, "reviewchanges", nil},
		{"export", commandExport, []string{"--audit"}, "export", []string{"--audit"}},
		{"log", commandLog, nil, "log", nil},
		{"rename", commandRename, nil, "rename", nil},
		{"rename title", commandRename, []string{"my", "title"}, "rename", []string{"my", "title"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			ch := make(chan dialoguetui.MessageEvent, 1)
			h := &aiEditorHandler{ctx: ctx}
			h.openChatTx.Store("rolling-fox", (chan<- dialoguetui.MessageEvent)(ch))

			cmd := textapi.Command{
				Name: tc.command,
				Args: tc.args,
				URI:  mustURI(t, "rune-agent://openai_gpt-5/rolling-fox"),
			}
			require.NoError(t, h.routeChatCommand(cmd))

			select {
			case ev := <-ch:
				assert.Equal(t, dialoguetui.MessageEventCommand, ev.Type)
				assert.Equal(t, tc.wantName, ev.CommandName)
				assert.Equal(t, tc.wantArgs, ev.CommandArgs)
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for injected command event")
			}
		})
	}
}

func TestRouteChatCommandErrors(t *testing.T) {
	ctx := context.Background()
	h := &aiEditorHandler{ctx: ctx}

	// No focused chat tab.
	err := h.routeChatCommand(textapi.Command{Name: commandModel})
	require.Error(t, err)

	// Chat URI that is not an open chat.
	err = h.routeChatCommand(textapi.Command{
		Name: commandModel,
		URI:  mustURI(t, "rune-agent://openai_gpt-5/missing-chat"),
	})
	require.Error(t, err)

	// chatrename outside a chat tab reports the standard error.
	err = h.routeChatCommand(textapi.Command{Name: commandRename})
	assert.EqualError(t, err, "chatrename must be run from an open agent chat tab")
	err = h.routeChatCommand(textapi.Command{
		Name: commandRename,
		URI:  mustURI(t, "file:///tmp/foo"),
	})
	assert.EqualError(t, err, "chatrename must be run from an open agent chat tab")

	// skill without a name.
	h.openChatTx.Store("rolling-fox",
		(chan<- dialoguetui.MessageEvent)(make(chan dialoguetui.MessageEvent, 1)))
	err = h.routeChatCommand(textapi.Command{
		Name: commandSkill,
		URI:  mustURI(t, "rune-agent://openai_gpt-5/rolling-fox"),
	})
	require.Error(t, err)

	// Store commands reject a positional dialogue id; they act on the
	// focused chat only.
	for _, name := range []string{
		commandClear, commandFork, commandReviewChanges,
		commandExport, commandLog,
	} {
		err = h.routeChatCommand(textapi.Command{
			Name: name,
			Args: []string{"other-chat"},
			URI:  mustURI(t, "rune-agent://openai_gpt-5/rolling-fox"),
		})
		require.Errorf(t, err, "%s with positional id should error", name)
	}

	err = h.routeChatCommand(textapi.Command{
		Name: commandCompact,
		Args: []string{"openai/gpt-5", "codex/gpt-5"},
		URI:  mustURI(t, "rune-agent://openai_gpt-5/rolling-fox"),
	})
	require.Error(t, err)
}

func TestCompleteChatPromptCommands(t *testing.T) {
	svc := llmtest.New([]llmapi.ModelEntry{
		{Provider: "openai", Name: "gpt-5"},
		{Provider: "codex", Name: "gpt-5"},
	})
	skillReg := skills.NewRegistry(nopFileSystem{}, dirURI(""), nil, nil)
	h := &aiEditorHandler{llmSvc: svc, skillRegistry: skillReg}
	ctx := context.Background()

	t.Run("model", func(t *testing.T) {
		got := completeToSlice(t, ctx, h, commandModel)
		assert.Equal(t, []string{"openai/gpt-5", "codex/gpt-5"}, got)
	})
	t.Run("compact", func(t *testing.T) {
		got := completeToSlice(t, ctx, h, commandCompact)
		assert.Equal(t, []string{"openai/gpt-5", "codex/gpt-5"}, got)
	})
	t.Run("effort", func(t *testing.T) {
		got := completeToSlice(t, ctx, h, commandEffort)
		assert.Equal(t, []string{
			"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra",
		}, got)
	})
	t.Run("skill", func(t *testing.T) {
		got := completeToSlice(t, ctx, h, commandSkill)
		assert.Equal(t, []string{"explore", "plan"}, got)
	})
	t.Run("maxtokens", func(t *testing.T) {
		got := completeToSlice(t, ctx, h, commandMaxTokens)
		assert.Empty(t, got)
	})
}

func TestCompleteChatPromptCommandsDialogues(t *testing.T) {
	// Store commands act on the open chat only, so they offer no
	// dialogue-id completion candidates.
	h := &aiEditorHandler{}
	ctx := context.Background()

	for _, name := range []string{
		commandClear, commandFork, commandReviewChanges,
		commandExport, commandLog, commandRename,
	} {
		t.Run(name, func(t *testing.T) {
			got := completeToSlice(t, ctx, h, name)
			assert.Empty(t, got)
		})
	}
}

func TestParseDialogueIDNamedCandidates(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"bare id", "rolling-fox", "rolling-fox"},
		{"named", "fix the flaky test (rolling-fox)", "rolling-fox"},
		{"named with worktree", "worktree-b:fix the flaky test (rolling-fox)", "rolling-fox"},
		{"named legacy", "<legacy>:fix the flaky test (rolling-fox)", "rolling-fox"},
		{"worktree bare", "worktree-b:rolling-fox", "rolling-fox"},
		{"title with parens", "fix (auth) bug (rolling-fox)", "rolling-fox"},
		{"unclosed paren", "no close (paren", "no close (paren"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := textapi.Command{Args: []string{tc.in}}
			assert.Equal(t, tc.want, parseDialogueID(cmd))
		})
	}
}

func completeToSlice(
	t *testing.T, ctx context.Context, h *aiEditorHandler, name string,
) []string {
	t.Helper()
	it, err := h.Complete(ctx, name, nil)
	require.NoError(t, err)
	got, err := iterator.ToSlice(ctx, it)
	require.NoError(t, err)
	return got
}
