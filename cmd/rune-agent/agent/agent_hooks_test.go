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
	"errors"
	"os"
	osexec "os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/hooks"
)

func TestHooks_PostToolUseBlock(t *testing.T) {
	t.Parallel()
	// Hook returns exit 2 with a reason on stderr → must replace the
	// tool result content the model sees and mark IsError.
	cfg := hookCfg(hooks.EventPostToolUse, "my_tool",
		`printf "hooked: not allowed" 1>&2; exit 2`)
	runner := hooks.NewRunner(cfg, hooksTestExec{}, nil, "")

	svc := &mockService{responses: []mockResponse{
		toolCallResponse("my_tool", `{}`, "c1"),
		stopResponse("done"),
	}}
	store := newMockStore()
	tool := &mockTool{name: "my_tool", result: ToolResult{Content: "original"}}
	reg := NewRegistry(tool)
	ag := NewAgent(svc, reg, noSkills(), store, NoMemory(), Config{
		SystemPrompt: "t",
		Hooks:        runner,
	})

	events := collectEvents(t, ag.Run(context.Background(), "d", "hi"))
	require.True(t, hasEventType(events, EventDone))

	// The model's second turn must see the replaced content.
	d, ok := store.getDialogue("d")
	require.True(t, ok)
	var lastTool *llmapi.Message
	for i := range d.Messages {
		if d.Messages[i].Role == llmapi.RoleTool {
			lastTool = &d.Messages[i]
		}
	}
	require.NotNil(t, lastTool)
	assert.Equal(t, "hooked: not allowed", lastTool.Content)
}

func TestHooks_StopContinuationGuard(t *testing.T) {
	t.Parallel()
	// First Stop block fires; the agent should append a continuation
	// message and run a second turn. The hook fires again with
	// stop_hook_active=true; we'd block again, but the guard ignores
	// it. The agent thus exits cleanly with two stop responses.
	cfg := hookCfg(hooks.EventStop, "*",
		`printf "keep going" 1>&2; exit 2`)
	runner := hooks.NewRunner(cfg, hooksTestExec{}, nil, "")

	svc := &mockService{responses: []mockResponse{
		stopResponse("first"),
		stopResponse("second"),
	}}
	store := newMockStore()
	ag := NewAgent(svc, NewRegistry(), noSkills(), store, NoMemory(), Config{
		SystemPrompt: "t",
		Hooks:        runner,
	})

	events := collectEvents(t, ag.Run(context.Background(), "d", "hi"))
	require.True(t, hasEventType(events, EventDone))

	// Two assistant turns must have been produced.
	d, ok := store.getDialogue("d")
	require.True(t, ok)
	var assistantCount int
	for _, m := range d.Messages {
		if m.Role == llmapi.RoleAssistant {
			assistantCount++
		}
	}
	assert.Equal(t, 2, assistantCount, "Stop continuation should append a second assistant turn exactly once")
	// The injected continuation reason must show up as a user message.
	var foundCont bool
	for _, m := range d.Messages {
		if m.Role == llmapi.RoleUser && strings.Contains(m.Content, "keep going") {
			foundCont = true
		}
	}
	assert.True(t, foundCont, "expected continuation user message")
}

func TestHooks_SessionStartAdditionalContext(t *testing.T) {
	t.Parallel()
	cfg := hookCfg(hooks.EventSessionStart, "*",
		`printf "context-from-hook"`)
	runner := hooks.NewRunner(cfg, hooksTestExec{}, nil, "")

	svc := &mockService{responses: []mockResponse{stopResponse("ok")}}
	store := newMockStore()
	ag := NewAgent(svc, NewRegistry(), noSkills(), store, NoMemory(), Config{
		SystemPrompt: "t",
		Hooks:        runner,
	})

	events := collectEvents(t, ag.Run(context.Background(), "d", "hello"))
	require.True(t, hasEventType(events, EventDone))

	d, ok := store.getDialogue("d")
	require.True(t, ok)
	var userMsg *llmapi.Message
	for i := range d.Messages {
		if d.Messages[i].Role == llmapi.RoleUser {
			userMsg = &d.Messages[i]
			break
		}
	}
	require.NotNil(t, userMsg)
	assert.Equal(t, "hello", userMsg.Content,
		"session hook context is request-only and must not pollute persisted text")
	require.Len(t, svc.requests, 1)
	requestUser := svc.requests[0].Messages[len(svc.requests[0].Messages)-1]
	if len(requestUser.MultiContent) > 0 {
		assert.Contains(t, requestUser.MultiContent[0].Text, "context-from-hook")
	} else {
		assert.Contains(t, requestUser.Content, "context-from-hook")
	}
}

func TestHooks_PreCompactManualBlocked(t *testing.T) {
	t.Parallel()
	cfg := hookCfg(hooks.EventPreCompact, "*",
		`printf "no compact" 1>&2; exit 2`)
	runner := hooks.NewRunner(cfg, hooksTestExec{}, nil, "")

	store := newMockStore()
	d := dialoguemanager.Dialogue{
		ID: "d-pre",
		Messages: []llmapi.Message{
			{Role: llmapi.RoleSystem, Content: "sys"},
			{Role: llmapi.RoleUser, Content: "hi"},
			{Role: llmapi.RoleAssistant, Content: "hello"},
		},
	}
	require.NoError(t, store.Create(context.Background(), d))
	svc := &mockService{responses: []mockResponse{stopResponse("Summary")}}

	_, _, err := CompactDialogue(context.Background(), svc, llmapi.ModelEntry{}, store, d, WithCompactHooks(runner))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no compact")
}

// --- helpers ---

// hooksTestExec executes commands locally so the hook runner has a
// real shell to dispatch to in these integration tests.
type hooksTestExec struct{}

func (hooksTestExec) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := osexec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	c.Env = append(os.Environ(), cmd.Env...)
	if err := c.Start(); err != nil {
		return 0, err
	}
	pid := workspaceapi.Pid(c.Process.Pid)
	go func() {
		err := c.Wait()
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- err
		}
	}()
	return pid, nil
}

func (hooksTestExec) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (hooksTestExec) Close() error                                  { return nil }

// hookCfg is a small helper that registers a single command hook for
// the given event with the matcher provided.
func hookCfg(ev hooks.Event, matcher, command string) hooks.Config {
	g := hooks.Group{
		Matcher: matcher,
		Hooks: []hooks.Hook{{
			Type:    hooks.HookTypeCommand,
			Command: command,
			Timeout: 2 * time.Second,
		}},
	}
	cfg := hooks.Config{}
	switch ev {
	case hooks.EventPostToolUse:
		cfg.PostToolUse = []hooks.Group{g}
	case hooks.EventStop:
		cfg.Stop = []hooks.Group{g}
	case hooks.EventUserPromptSubmit:
		cfg.UserPromptSubmit = []hooks.Group{g}
	case hooks.EventPreCompact:
		cfg.PreCompact = []hooks.Group{g}
	case hooks.EventSessionStart:
		cfg.SessionStart = []hooks.Group{g}
	}
	return cfg
}

func (hooksTestExec) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}
