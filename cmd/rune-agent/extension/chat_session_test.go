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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/llm/llmtest"
)

const sessionTestID = "rolling-fox"

var sessionTestModels = []llmapi.ModelEntry{{Provider: "test", Name: "test-model", ContextWindow: 128_000}}

// openChat opens a view of the chat id and sizes it width by height.
// The view closes with the test unless the test closes it.
func openChat(t *testing.T, h *aiEditorHandler, id string, width, height int) *chat {
	t.Helper()
	c, err := h.newChat(textapi.Command{Name: commandChat, Args: []string{id}}, workspaceapi.URI{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, c.content.Close()) })
	c.content.Resize(width, height)
	return c
}

// typeInto has c handle the keys of seq, spelled as term.ParseKeys reads
// them but for spaces, which stand for themselves.
func typeInto(t *testing.T, c *chat, seq string) {
	t.Helper()
	keys, err := term.ParseKeys(strings.ReplaceAll(seq, " ", "<space>"))
	require.NoError(t, err)
	for _, k := range keys {
		c.content.Handle(term.Event{Type: term.EventKey, Mod: k.Mod, Key: k.Key, Ch: k.Ch})
	}
}

// drawChat draws c, sized width by height, and returns the screen.
func drawChat(c *chat, width, height int) string {
	w := term.NewStringWriter(width, height)
	c.content.Draw(w)
	_ = w.Flush()
	return w.String()
}

// awaitScreen waits for c, sized width by height, to show every one of
// want, and returns the screen.
func awaitScreen(t *testing.T, c *chat, width, height int, want ...string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		screen := drawChat(c, width, height)
		if !slices.ContainsFunc(want, func(w string) bool { return !strings.Contains(screen, w) }) {
			return screen
		}
		if time.Now().After(deadline) {
			require.Failf(t, "the chat never showed it", "want %q in:\n%s", want, screen)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// awaitClosed waits for the session s to end.
func awaitClosed(t *testing.T, s *chatSession) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		require.Fail(t, "the chat session never ended")
	}
}

func TestChatSessionShowsSubmitInEveryView(t *testing.T) {
	const msg = "a message long enough to wrap"
	svc := llmtest.New(sessionTestModels, llmtest.Response{Chunks: []string{"Hi there."}})
	h, _ := newChatTestHandler(t, svc, newMemDialogueStore())
	narrow := openChat(t, h, sessionTestID, 24, 12)
	wide := openChat(t, h, sessionTestID, 60, 12)
	assert.Equal(t, narrow.uri.String()+"?view=2", wide.uri.String(), "the second view is a tab of its own")

	typeInto(t, narrow, msg+"<enter>")

	narrowScreen := awaitScreen(t, narrow, 24, 12, "Hi there.")
	wideScreen := awaitScreen(t, wide, 60, 12, "Hi there.")
	assert.Equal(t, 1, strings.Count(wideScreen, msg), "the view it was not typed in shows it once:\n%s", wideScreen)
	assert.NotContains(t, narrowScreen, msg, "the narrow view wraps it:\n%s", narrowScreen)
	assert.Equal(t, 1, strings.Count(narrowScreen, "a message"),
		"the view it was typed in shows it once:\n%s", narrowScreen)
	require.Len(t, svc.Requests(), 1, "the session answers it once")
}

func TestChatSessionLateViewReplaysTurnInFlight(t *testing.T) {
	args := mustJSON(t, map[string]any{
		"questions": []map[string]any{{
			"question": "Pick one", "header": "Choice",
			"options": []map[string]any{
				{"label": "A", "description": ""},
				{"label": "B", "description": ""},
			},
			"multiSelect": false,
		}},
	})
	svc := llmtest.New(sessionTestModels,
		llmtest.Response{
			Chunks: []string{"Let me ask."},
			ToolCalls: []llmapi.ToolCall{{
				ID: "call-1", Type: llmapi.ToolTypeFunction,
				Function: llmapi.FunctionCall{Name: "ask_user_question", Arguments: args},
			}},
			FinishReason: llmapi.FinishReasonToolCall,
		},
		llmtest.Response{Chunks: []string{"You picked A."}},
	)
	h, _ := newChatTestHandler(t, svc, newMemDialogueStore())
	layout, err := dialoguetui.ParseStatusBarLayout(`{{ .Model }}`)
	require.NoError(t, err)
	h.cfg.StatusBar = dialoguetui.StatusBarConfig{Enabled: true, Layout: layout}
	const width, height = 60, 24
	first := openChat(t, h, sessionTestID, width, height)
	typeInto(t, first, "ask me<enter>")
	awaitScreen(t, first, width, height, "Let me ask.", "Pick one", "> A")

	late := openChat(t, h, sessionTestID, width, height)
	awaitScreen(t, late, width, height, "ask me", "Let me ask.", "Pick one", "> A", "test-model")

	typeInto(t, late, "<enter>")
	for name, c := range map[string]*chat{"first": first, "late": late} {
		screen := awaitScreen(t, c, width, height, "You picked A.")
		assert.NotContains(t, screen, "> A", "the %s view takes the prompt down:\n%s", name, screen)
	}
	requests := svc.Requests()
	require.Len(t, requests, 2)
	got, ok := findToolResult(requests[1].Request.Messages, "call-1")
	require.True(t, ok)
	assert.Contains(t, got, "A")

	typeInto(t, first, "<enter>")
	time.Sleep(50 * time.Millisecond)
	assert.Len(t, svc.Requests(), 2, "the prompt is answered once")
}

func TestChatSessionQueuesFollowUpsForEveryView(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	svc := llmtest.New(sessionTestModels,
		llmtest.Response{Chunks: []string{"First reply."}},
		llmtest.Response{Chunks: []string{"Second reply."}},
	)
	svc.BeforeCompletion = func() {
		once.Do(func() {
			close(started)
			<-release
		})
	}
	h, _ := newChatTestHandler(t, svc, newMemDialogueStore())
	const width, height = 60, 16
	first := openChat(t, h, sessionTestID, width, height)
	second := openChat(t, h, sessionTestID, width, height)
	typeInto(t, first, "one<enter>")
	<-started

	typeInto(t, second, "two<enter>")
	awaitScreen(t, first, width, height, "one", "two")
	close(release)

	for _, c := range []*chat{first, second} {
		screen := awaitScreen(t, c, width, height, "First reply.", "Second reply.")
		assert.Less(t, strings.Index(screen, "First reply."), strings.Index(screen, "two"), screen)
	}
	requests := svc.Requests()
	require.Len(t, requests, 2)
	msgs := requests[1].Request.Messages
	assert.Equal(t, "two", msgs[len(msgs)-1].Content)
}

func TestChatSessionOutlivesItsLastViewWhileBusy(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	svc := llmtest.New(sessionTestModels, llmtest.Response{Chunks: []string{"Done at last."}})
	svc.BeforeCompletion = func() {
		once.Do(func() {
			close(started)
			<-release
		})
	}
	store := newMemDialogueStore()
	h, wm := newChatTestHandler(t, svc, store)
	const width, height = 60, 12
	c, err := h.newChat(textapi.Command{Name: commandChat, Args: []string{sessionTestID}}, workspaceapi.URI{})
	require.NoError(t, err)
	c.content.Resize(width, height)
	drawChat(c, width, height)
	typeInto(t, c, "work hard<enter>")
	<-started

	require.NoError(t, c.content.Close())
	s, ok := h.runningChat(sessionTestID)
	require.True(t, ok, "a busy chat outlives its tab")
	assert.True(t, s.detached())
	assert.Equal(t, []string{sessionTestID}, h.runningChats())

	close(release)
	awaitClosed(t, s)
	_, ok = h.runningChat(sessionTestID)
	assert.False(t, ok, "the chat ends once idle with no view")
	d, err := store.Get(t.Context(), sessionTestID)
	require.NoError(t, err)
	require.NotEmpty(t, d.Messages)
	assert.Equal(t, "Done at last.", d.Messages[len(d.Messages)-1].Content, "the turn completes")
	statuses := wm.tabStatuses()
	require.NotEmpty(t, statuses)
	assert.Equal(t, browserapi.TabDone, statuses[len(statuses)-1].State)
	assert.Equal(t, s.uri, wm.statusURI, "the chat no tab shows reports at its own URI")

	reopened := openChat(t, h, sessionTestID, width, height)
	awaitScreen(t, reopened, width, height, "work hard", "Done at last.")
}

func TestChatCloseOfDetachedChatClearsItsStatus(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	svc := llmtest.New(sessionTestModels, llmtest.Response{Chunks: []string{"Never."}})
	svc.BeforeCompletion = func() {
		once.Do(func() {
			close(started)
			<-release
		})
	}
	// The backend never answers, as one that ignores its context.
	defer close(release)
	h, wm := newChatTestHandler(t, svc, newMemDialogueStore())
	const width, height = 60, 12
	c, err := h.newChat(textapi.Command{Name: commandChat, Args: []string{sessionTestID}}, workspaceapi.URI{})
	require.NoError(t, err)
	c.content.Resize(width, height)
	drawChat(c, width, height)
	typeInto(t, c, "work hard<enter>")
	<-started
	require.NoError(t, c.content.Close())
	s, ok := h.runningChat(sessionTestID)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		reports := wm.tabReports(s.uri)
		return len(reports) > 0 && reports[len(reports)-1].State == browserapi.TabWorking
	}, 5*time.Second, 10*time.Millisecond, "the chat no tab shows is listed as working")

	require.NoError(t, h.HandleCommand(t.Context(), textapi.Command{Name: commandClose, Args: []string{sessionTestID}}))
	awaitClosed(t, s)

	reports := wm.tabReports(s.uri)
	require.NotEmpty(t, reports)
	assert.Equal(t, browserapi.TabStatus{State: browserapi.TabClear, Progress: -1}, reports[len(reports)-1],
		"a chat that ended is not listed any more")
}

func TestChatSessionLastIdleViewClosesWithoutStatus(t *testing.T) {
	svc := llmtest.New(sessionTestModels, llmtest.Response{Chunks: []string{"Hi there."}})
	h, wm := newChatTestHandler(t, svc, newMemDialogueStore())
	const width, height = 60, 12
	c, err := h.newChat(textapi.Command{Name: commandChat, Args: []string{sessionTestID}}, workspaceapi.URI{})
	require.NoError(t, err)
	c.content.Resize(width, height)
	drawChat(c, width, height)
	typeInto(t, c, "hello<enter>")
	awaitScreen(t, c, width, height, "Hi there.")
	s, ok := h.runningChat(sessionTestID)
	require.True(t, ok)
	require.Eventually(t, func() bool {
		reports := wm.tabStatuses()
		return len(reports) > 0 && reports[len(reports)-1].State == browserapi.TabDone
	}, 5*time.Second, 10*time.Millisecond)
	before := len(wm.tabStatuses())

	require.NoError(t, c.content.Close())
	awaitClosed(t, s)

	assert.Equal(t, before, len(wm.tabStatuses()),
		"the outcome the tab showed is not listed again for a chat that ended")
}

func TestChatSessionEndsWithItsLastIdleView(t *testing.T) {
	svc := llmtest.New(sessionTestModels)
	h, _ := newChatTestHandler(t, svc, newMemDialogueStore())
	first, err := h.newChat(textapi.Command{Name: commandChat, Args: []string{sessionTestID}}, workspaceapi.URI{})
	require.NoError(t, err)
	second, err := h.newChat(textapi.Command{Name: commandChat, Args: []string{sessionTestID}}, workspaceapi.URI{})
	require.NoError(t, err)
	s, ok := h.runningChat(sessionTestID)
	require.True(t, ok)

	require.NoError(t, first.content.Close())
	_, ok = h.runningChat(sessionTestID)
	assert.True(t, ok, "a view still shows the chat")
	assert.Equal(t, second.uri, s.shownURI())

	require.NoError(t, second.content.Close())
	awaitClosed(t, s)
	_, ok = h.runningChat(sessionTestID)
	assert.False(t, ok)
}

func TestChatCloseEndsChatInEveryView(t *testing.T) {
	tests := []struct {
		name  string
		close func(t *testing.T, h *aiEditorHandler, first *chat)
	}{
		{
			name: "chatclose",
			close: func(t *testing.T, h *aiEditorHandler, _ *chat) {
				require.NoError(t, h.HandleCommand(t.Context(),
					textapi.Command{Name: commandClose, Args: []string{sessionTestID}}))
			},
		},
		{
			name: "chatclose in the chat's tab",
			close: func(t *testing.T, h *aiEditorHandler, first *chat) {
				require.NoError(t, h.HandleCommand(t.Context(), textapi.Command{Name: commandClose, URI: first.uri}))
			},
		},
		{
			name: "/exit in a view",
			close: func(t *testing.T, _ *aiEditorHandler, first *chat) {
				typeInto(t, first, "/exit<enter>")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := llmtest.New(sessionTestModels)
			h, _ := newChatTestHandler(t, svc, newMemDialogueStore())
			const width, height = 40, 10
			first := openChat(t, h, sessionTestID, width, height)
			second := openChat(t, h, sessionTestID, width, height)
			s, ok := h.runningChat(sessionTestID)
			require.True(t, ok)

			tt.close(t, h, first)
			awaitClosed(t, s)
			_, ok = h.runningChat(sessionTestID)
			assert.False(t, ok)
			for _, c := range []*chat{first, second} {
				awaitScreen(t, c, width, height, chatClosedText)
				exit, _ := c.content.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
				assert.True(t, exit, "the view closes its tab")
			}
		})
	}
}
