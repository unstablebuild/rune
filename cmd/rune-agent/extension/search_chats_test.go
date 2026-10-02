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
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueiterator "github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/skills"
	"unstable.build/rune/cmd/rune-agent/configedit"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguemanager"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
	"unstable.build/rune/internal/handler/finder"
)

func newSearchChatsStore(t *testing.T, dialogues ...dialoguemanager.Dialogue) dialoguemanager.Store {
	t.Helper()
	store := dialoguemanager.NewStore(storagestub.NewInMemoryService(), t.TempDir())
	for _, d := range dialogues {
		require.NoError(t, store.Create(context.Background(), d))
	}
	return store
}

func collectChatSearch(t *testing.T, it blueiterator.Iterator[string]) []string {
	t.Helper()
	var lines []string
	for {
		line, ok := it.Next(context.Background())
		if !ok {
			break
		}
		lines = append(lines, line)
	}
	require.NoError(t, it.Err())
	return lines
}

func TestChatSearchIteratorLines(t *testing.T) {
	const wsURI = "file:///my/workspace"
	store := newSearchChatsStore(t,
		dialoguemanager.Dialogue{
			ID:           "local-chat",
			WorkspaceURI: wsURI,
			UpdatedAt:    time.Now(),
			Messages: []llmapi.Message{
				{Role: llmapi.RoleUser, Content: "why does\nthe workspace\t scan loop"},
				// No text content: tool-call plumbing must not produce a line.
				{Role: llmapi.RoleAssistant, ToolCalls: []llmapi.ToolCall{{ID: "c1"}}},
				{Role: llmapi.RoleAssistant, Content: "it scans $dirs"},
				{Role: llmapi.RoleUser, MultiContent: []llmapi.ContentPart{
					{Type: llmapi.ContentPartTypeText, Text: "via parts"},
				}},
			},
		},
		dialoguemanager.Dialogue{
			ID:           "audit:local-chat",
			WorkspaceURI: wsURI,
			UpdatedAt:    time.Now(),
			Messages:     []llmapi.Message{{Role: llmapi.RoleUser, Content: "audit entry"}},
		},
		dialoguemanager.Dialogue{
			ID:           "sub-chat",
			WorkspaceURI: wsURI,
			SubAgent:     true,
			UpdatedAt:    time.Now(),
			Messages:     []llmapi.Message{{Role: llmapi.RoleUser, Content: "sub-agent msg"}},
		},
	)

	h := &aiEditorHandler{dialogueStore: store, cwd: mustURI(t, wsURI)}
	targets := new(sync.Map)
	it, err := h.newChatSearchIterator(context.Background(), false, targets)
	require.NoError(t, err)

	want := []string{
		"local-chat:0:user: why does the workspace scan loop",
		"local-chat:2:assistant: it scans $dirs",
		"local-chat:3:user: via parts",
	}
	assert.Equal(t, want, collectChatSearch(t, it))

	for _, line := range want {
		id, ok := targets.Load(line)
		require.True(t, ok, "select target missing for %q", line)
		assert.Equal(t, "local-chat", id)
	}
}

func TestChatSearchIteratorOrdering(t *testing.T) {
	now := time.Now()
	msg := func(id string) []llmapi.Message {
		return []llmapi.Message{{Role: llmapi.RoleUser, Content: "hi from " + id}}
	}
	store := newSearchChatsStore(t,
		dialoguemanager.Dialogue{ID: "foreign-chat", WorkspaceURI: "file:///other",
			UpdatedAt: now, Messages: msg("foreign-chat")},
		dialoguemanager.Dialogue{ID: "wt-chat", WorkspaceURI: "file:///my/worktree-b",
			UpdatedAt: now.Add(-time.Minute), Messages: msg("wt-chat")},
		dialoguemanager.Dialogue{ID: "local-chat", WorkspaceURI: "file:///my/workspace",
			UpdatedAt: now.Add(-2 * time.Minute), Messages: msg("local-chat")},
		dialoguemanager.Dialogue{ID: "legacy-chat",
			UpdatedAt: now.Add(-3 * time.Minute), Messages: msg("legacy-chat")},
	)
	h := &aiEditorHandler{
		dialogueStore: store,
		cwd:           mustURI(t, "file:///my/workspace"),
		gitID: gitIdentity{worktrees: map[string]string{
			"file:///my/workspace":  "workspace",
			"file:///my/worktree-b": "worktree-b",
		}},
	}

	it, err := h.newChatSearchIterator(context.Background(), false, new(sync.Map))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"local-chat:0:user: hi from local-chat",
		"worktree-b:wt-chat:0:user: hi from wt-chat",
		"<legacy>:legacy-chat:0:user: hi from legacy-chat",
	}, collectChatSearch(t, it))

	it, err = h.newChatSearchIterator(context.Background(), true, new(sync.Map))
	require.NoError(t, err)
	assert.Equal(t, []string{
		"local-chat:0:user: hi from local-chat",
		"worktree-b:wt-chat:0:user: hi from wt-chat",
		"<legacy>:legacy-chat:0:user: hi from legacy-chat",
		"file:///other:foreign-chat:0:user: hi from foreign-chat",
	}, collectChatSearch(t, it))
}

// A corrupt session must be skipped, not abort the scan.
func TestChatSearchIteratorSkipsCorruptSession(t *testing.T) {
	const wsURI = "file:///my/workspace"
	store := newSearchChatsStore(t,
		dialoguemanager.Dialogue{ID: "corrupt-chat", WorkspaceURI: wsURI,
			UpdatedAt: time.Now(),
			Messages:  []llmapi.Message{{Role: llmapi.RoleUser, Content: "lost"}}},
		dialoguemanager.Dialogue{ID: "good-chat", WorkspaceURI: wsURI,
			UpdatedAt: time.Now().Add(-time.Minute),
			Messages:  []llmapi.Message{{Role: llmapi.RoleUser, Content: "kept"}}},
	)

	d, err := store.Get(context.Background(), "corrupt-chat")
	require.NoError(t, err)
	require.NotEmpty(t, d.MessagesPath)
	require.NoError(t, os.WriteFile(d.MessagesPath, []byte("{"), 0o600))

	h := &aiEditorHandler{dialogueStore: store, cwd: mustURI(t, wsURI)}
	it, err := h.newChatSearchIterator(context.Background(), false, new(sync.Map))
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"good-chat:0:user: kept"},
		collectChatSearch(t, it))
}

// The old conversation grep capped its line buffer; streaming decode must not
// drop multi-megabyte single-line sessions.
func TestChatSearchIteratorReadsLargeMessage(t *testing.T) {
	const wsURI = "file:///my/workspace"
	big := "big" + strings.Repeat("x", 2<<20)
	store := newSearchChatsStore(t, dialoguemanager.Dialogue{
		ID:           "big-chat",
		WorkspaceURI: wsURI,
		UpdatedAt:    time.Now(),
		Messages:     []llmapi.Message{{Role: llmapi.RoleUser, Content: big}},
	})
	h := &aiEditorHandler{dialogueStore: store, cwd: mustURI(t, wsURI)}
	it, err := h.newChatSearchIterator(context.Background(), false, new(sync.Map))
	require.NoError(t, err)
	lines := collectChatSearch(t, it)
	require.Len(t, lines, 1)
	assert.True(t, strings.HasSuffix(lines[0], big))
}

func TestSearchChatsDispatchOpensFinder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h := &aiEditorHandler{
		ctx:           ctx,
		dialogueStore: newMemDialogueStore(),
		wm:            &recordingWindowManager{},
		p:             term.NopInterrupter(),
		n:             stubNotifications{},
		cwd:           dirURI(""),
	}
	require.NoError(t, h.HandleCommand(ctx, textapi.Command{Name: commandSearchChats}))
	require.NotNil(t, h.searchChats, "searchchats must build its split-window host")
}

// TestSearchChatsSelectOpensDialogue verifies that Enter on a result opens
// the dialogue as a chat tab, equivalent to `agent <id>`.
func TestSearchChatsSelectOpensDialogue(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	store := newSearchChatsStore(t, dialoguemanager.Dialogue{
		ID:        "rolling-fox",
		UpdatedAt: time.Now(),
		Messages:  []llmapi.Message{{Role: llmapi.RoleUser, Content: "hello"}},
	})

	fs := nopFileSystem{}
	wm := &recordingWindowManager{}
	h := &aiEditorHandler{
		ctx:            ctx,
		llmSvc:         llmtest.New([]llmapi.ModelEntry{{Provider: "test", Name: "test-model"}}),
		defaultModel:   "test-model",
		dialogueStore:  store,
		wm:             wm,
		p:              term.NopInterrupter(),
		n:              stubNotifications{},
		config:         configedit.NopConfig(),
		skillRegistry:  skills.NewRegistry(fs, dirURI(""), nil, nil),
		toolRegistry:   agent.NewRegistry(),
		agentsConfig:   agent.NewConfig([]agent.Definition{{ID: "default", AllowAny: true}}),
		cwd:            dirURI(""),
		fs:             fs,
		memoryDataPath: t.TempDir(),
	}

	rh, err := h.newSearchChatsFinder(
		ctx, textapi.Command{Name: commandSearchChats},
		finder.Clients{
			Interrupter:   term.NopInterrupter(),
			Notifications: stubNotifications{},
		},
		testInvokeWindow(7), config.NopConfig(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rh.Close() })

	sw, ok := rh.(finder.ScanWaiter)
	require.True(t, ok, "finder handler must implement ScanWaiter")
	<-sw.ScanDone()
	sw.DrainList()

	_, handled := rh.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, handled)

	require.NotNil(t, wm.gotHandler, "selecting a result must open a chat tab")
	t.Cleanup(func() { _ = wm.gotHandler.Close() })
	assert.Equal(t, "rolling-fox", wm.gotName)
	assert.True(t, strings.HasPrefix(wm.gotURI.String(), "rune-agent://"))
	// handleChat sets content on the invoking window; nil panics in the RPC client.
	assert.Equal(t, testInvokeWindow(7), wm.gotWindow)
}

type testInvokeWindow uint64

func (w testInvokeWindow) WindowID() uint64 { return uint64(w) }
