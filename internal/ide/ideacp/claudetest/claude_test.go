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

//go:build e2e

package claudetest

import (
	"context"
	"strings"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
)

func TestChat(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	c := h.newChat(t, "chat")

	c.useHaiku()
	c.waitScreen(valueName(c.option(acp.SessionConfigOptionCategoryModel), "haiku"),
		valueName(c.option(acp.SessionConfigOptionCategoryThoughtLevel), "low"))
	c.send("/model gpt-0")
	c.waitScreen(`unknown model "gpt-0"`)

	phases := c.watchStatus()
	tn := c.prompt("Reply with exactly the word pineapple and nothing else.")

	assert.Equal(t, acp.StopReasonEndTurn, tn.done().StopReason)
	assert.Contains(t, strings.ToLower(tn.text()), "pineapple")
	c.waitScreen("Reply with exactly the word pineapple and nothing else.", tn.text())
	c.waitStatus("IDLE")
	seen := phases()
	assert.Subset(t, seen, []string{"SENDING", "RECEIVING"})
	assert.Equal(t, "IDLE", seen[len(seen)-1])
}

func TestReasoning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		display string
		want    bool
	}{
		{name: "summarized, as the claude-code package sets it", display: "summarized", want: true},
		{name: "omitted", display: "omitted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, map[string]string{thinkingDisplay: tt.display})
			c := h.newChat(t, "reasoning")
			c.set("model", acp.SessionConfigOptionCategoryModel, "haiku")

			phases := c.watchStatus()
			tn := c.prompt("How many integers from 1 to 1000 are divisible by 3 or 7 but not by 21? " +
				"Think it through, then answer with just the number.")
			c.waitStatus("IDLE")

			assert.Equal(t, acp.StopReasonEndTurn, tn.done().StopReason)
			if !tt.want {
				assert.Empty(t, tn.thought())
				assert.NotContains(t, phases(), "REASONING")
				return
			}
			require.NotEmpty(t, tn.thought())
			assert.Contains(t, phases(), "REASONING")
		})
	}
}

func TestPermissions(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	tests := []struct {
		name string
		// mode is the session's permission mode, Manual when empty.
		mode   string
		prompt string
		file   string
		// answer is the option picked on each permission prompt, which is
		// dismissed when answer is empty.
		answer   string
		wantAsk  bool
		wantFile bool
		// wantBody is shown with the prompt.
		wantBody string
	}{
		{
			name:   "allowed",
			prompt: "Use the Bash tool to run `touch allowed.txt`, then reply done.",
			file:   "allowed.txt", answer: "Yes", wantAsk: true, wantFile: true,
			wantBody: "touch allowed.txt",
		},
		{
			name:   "rejected",
			prompt: "Use the Bash tool to run `touch rejected.txt`, then reply done. Do not retry if it is denied.",
			file:   "rejected.txt", answer: "No", wantAsk: true,
		},
		{
			name:   "dismissed",
			prompt: "Use the Bash tool to run `touch dismissed.txt`, then reply done. Do not retry if it is denied.",
			file:   "dismissed.txt", wantAsk: true,
		},
		{
			name:   "edit shows its diff",
			prompt: "Use the Write tool to create edited.txt containing the single line: hello from claude",
			file:   "edited.txt", answer: "Yes", wantAsk: true, wantFile: true,
			wantBody: "+hello from claude",
		},
		{
			name:   "accept edits mode",
			mode:   "acceptEdits",
			prompt: "Use the Write tool to create accepted.txt containing the single line: hello from claude",
			file:   "accepted.txt", wantFile: true,
		},
		{
			name:   "bypass permissions mode",
			mode:   "bypassPermissions",
			prompt: "Use the Bash tool to run `touch bypassed.txt`, then reply done.",
			file:   "bypassed.txt", wantFile: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := h.newChat(t, strings.ReplaceAll(tt.name, " ", "-"))
			c.useHaiku()
			if tt.mode != "" {
				c.set("mode", acp.SessionConfigOptionCategoryMode, tt.mode)
			}

			from := c.log.len()
			c.send(tt.prompt)
			tn, prompts := c.answering(from, tt.answer)

			assert.Equal(t, acp.StopReasonEndTurn, tn.done().StopReason)
			assert.Equal(t, tt.wantFile, h.exists(tt.file), "%s exists", tt.file)
			perms := tn.permissions()
			if !tt.wantAsk {
				assert.Empty(t, perms)
				return
			}
			require.NotEmpty(t, perms)
			p := perms[0]
			assert.Empty(t, p.Session, "the session's own agent asks")
			want := acp.PermissionOptionKindRejectOnce
			if tt.answer == "Yes" {
				want = acp.PermissionOptionKindAllowOnce
			}
			got := tn.outcome(p.ID)
			require.NotNil(t, got.Selected, "permission %d was cancelled", p.ID)
			assert.Equal(t, want, optionKind(p, got.Selected.OptionId))
			assert.True(t, containsAll(prompts[0], tt.wantBody), "prompt shows %q:\n%s", tt.wantBody, prompts[0])
		})
	}
}

func TestSubagent(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	c := h.newChat(t, "subagent")
	c.useHaiku()

	from := c.log.len()
	c.send("Use the Agent tool to launch one general-purpose subagent in the foreground. " +
		"Tell it to use the Bash tool to run `touch from-subagent.txt` and report back. " +
		"Wait for it, then reply with just: finished.")
	tn, prompts := c.answering(from, "Yes")

	assert.Equal(t, acp.StopReasonEndTurn, tn.done().StopReason)
	assert.True(t, h.exists("from-subagent.txt"))
	subs := tn.subagents()
	require.NotEmpty(t, subs)
	id := subs[0]
	sub := c.log.state().Subagents[id]
	require.NotNil(t, sub.State)
	assert.Equal(t, acpapi.SubagentIdle, sub.State.State)
	assert.Empty(t, sub.Parent, "the session's own agent started it")
	calls := tn.toolCalls(id)
	require.NotEmpty(t, calls, "the subagent's tool calls arrive on its session")
	perms := tn.permissions()
	require.NotEmpty(t, perms)
	assert.Equal(t, id, perms[0].Session)
	assert.True(t, containsAll(prompts[0], "Asked by subagent "+sub.Title),
		"prompt names the subagent %q:\n%s", sub.Title, prompts[0])
	agentAt := column(prompts[0], "agent "+sub.Title)
	callAt := column(prompts[0], "Bash command=touch from-subagent.txt")
	require.GreaterOrEqual(t, agentAt, 0, "the subagent shows as an agent tool call:\n%s", prompts[0])
	assert.Greater(t, callAt, agentAt, "its tool calls show inside it:\n%s", prompts[0])
	c.waitScreen(sub.Title, tn.text())
}

func TestCancel(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	c := h.newChat(t, "cancel")
	c.useHaiku()
	c.set("mode", acp.SessionConfigOptionCategoryMode, "bypassPermissions")

	from := c.log.len()
	c.send("Use the Bash tool to run `sleep 120`, then reply done.")
	c.log.waitFor(from, "the sleep to start", func(ev acpapi.Event) bool {
		u := ev.Update
		return u != nil && u.ToolCallUpdate != nil && u.ToolCallUpdate.Title != nil &&
			strings.Contains(*u.ToolCallUpdate.Title, "sleep 120")
	})
	c.waitStatus("EXECUTING")
	start := time.Now()
	c.press(ctrlC)
	tn := c.log.turn(from)

	assert.Equal(t, acp.StopReasonCancelled, tn.done().StopReason)
	assert.Less(t, time.Since(start), time.Minute, "the turn ran on")
	c.waitScreen("Cancelled.")
	c.waitStatus("IDLE")

	tn = c.prompt("Reply with exactly: still here")
	assert.Equal(t, acp.StopReasonEndTurn, tn.done().StopReason)
	assert.Contains(t, strings.ToLower(tn.text()), "still here")
}

func TestSessions(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	first := h.newChat(t, "first")
	first.useHaiku()
	tn := first.prompt("Remember the code word walrus. Reply with exactly: noted")
	require.Equal(t, acp.StopReasonEndTurn, tn.done().StopReason)
	second := h.newChat(t, "second")
	second.useHaiku()
	assert.Equal(t, []acpapi.AgentInfo{{Name: agent, Running: true}}, h.agents())
	assert.Equal(t, []string{"first", "second"}, h.sessions())

	h.closeTab(first)
	assert.Equal(t, []string{"first", "second"}, h.sessions(), "closing a tab keeps its session")

	reopened := h.openChat(t, "first")
	reopened.waitScreen("Remember the code word walrus. Reply with exactly: noted", tn.text())
	tn = reopened.prompt("What was the code word? Reply with just the word.")
	assert.Contains(t, strings.ToLower(tn.text()), "walrus")

	require.NoError(t, h.run("close", "first"))
	reopened.waitScreen("Session closed.")
	waitUntil(t, uiTimeout, "session first to close", func() bool {
		return len(h.sessions()) == 1
	}, func() string { return strings.Join(h.sessions(), ", ") })
	assert.Equal(t, []string{"second"}, h.sessions())
	assert.Empty(t, h.notes.all())

	tn = second.prompt("Reply with exactly: still serving")
	assert.Equal(t, acp.StopReasonEndTurn, tn.done().StopReason)
	assert.Contains(t, strings.ToLower(tn.text()), "still serving")
}

func TestExtensionSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	svc := h.extension()
	ctx := context.Background()
	s, err := svc.NewSession(ctx, acpapi.NewSessionRequest{Agent: agent, Name: "extension"})
	require.NoError(t, err)
	for id, value := range map[string]string{"model": "haiku", "effort": "low"} {
		require.NoError(t, s.SetConfigOption(ctx, acp.SetSessionConfigOptionRequest{
			ValueId: &acp.SetSessionConfigOptionValueId{
				ConfigId: acp.SessionConfigId(id), Value: acp.SessionConfigValueId(value),
			},
		}))
	}
	ext := follow(t, s)
	c := h.openChat(t, "extension")

	from := ext.len()
	prompt := "Use the Bash tool to run `touch from-extension.txt`, then reply done."
	require.NoError(t, s.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(prompt)}))
	_, _ = c.answering(from, "Yes")
	tn := ext.turn(from)

	assert.Equal(t, acp.StopReasonEndTurn, tn.done().StopReason)
	assert.True(t, h.exists("from-extension.txt"))
	perms := tn.permissions()
	require.NotEmpty(t, perms)
	got := tn.outcome(perms[0].ID)
	require.NotNil(t, got.Selected, "the extension sees the answer the chat gave")
	assert.Equal(t, acp.PermissionOptionKindAllowOnce, optionKind(perms[0], got.Selected.OptionId))
	c.waitScreen(prompt, tn.text())
}

// optionKind is the kind of p's option id.
func optionKind(p acpapi.Permission, id acp.PermissionOptionId) acp.PermissionOptionKind {
	for _, o := range p.Request.Options {
		if o.OptionId == id {
			return o.Kind
		}
	}
	return ""
}
