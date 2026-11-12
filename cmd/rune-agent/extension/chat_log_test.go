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
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

// describeChatEvent spells ev as seq:kind:text.
func describeChatEvent(ev chatEvent) string {
	kind := map[chatEventKind]string{
		chatEventHistory: "history", chatEventMessage: "message", chatEventText: "text",
		chatEventReasoning: "reasoning", chatEventPrompt: "prompt", chatEventResolved: "resolved",
	}[ev.Kind]
	text := ev.Text
	if ev.Kind == chatEventPrompt {
		text = ev.Prompt.Event.PromptTitle
	}
	return fmt.Sprintf("%d:%s:%s", ev.Seq, kind, text)
}

func TestChatLog(t *testing.T) {
	hello := []llmapi.Message{{Role: llmapi.RoleUser, Content: "hello"}}
	text := func(s string) chatEvent {
		return messageEvent(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventText, Text: s})
	}
	reasoning := func(s string) chatEvent {
		return messageEvent(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventReasoning, Text: s})
	}
	brk := messageEvent(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBreak})
	prompt := func(title string) chatEvent {
		return chatEvent{Kind: chatEventPrompt, Prompt: chatPrompt{Event: &dialoguetui.MessageEvent{
			Type: dialoguetui.MessageEventPrompt, PromptTitle: title,
		}}}
	}
	resolved := func(id int) chatEvent {
		return chatEvent{Kind: chatEventResolved, Prompt: chatPrompt{ID: id}}
	}
	tests := []struct {
		name   string
		events []chatEvent
		// close closes the log before the events past it.
		close int
		// wantLogged is the Seq append returns for each event.
		wantLogged []int
		// want describes the events a view replays, as seq:kind:text.
		want        []string
		wantPending []string
	}{
		{
			name:       "events are numbered in order",
			events:     []chatEvent{{Kind: chatEventHistory, History: hello}, text("a"), brk, text("b")},
			close:      -1,
			wantLogged: []int{0, 1, 2, 3},
			want:       []string{"0:history:", "1:text:a", "2:message:", "3:text:b"},
		},
		{
			name: "the chunks of a stream make one event",
			events: []chatEvent{
				reasoning("th"), reasoning("ink"), text("a"), text("b"), text("c"), brk, text("d"),
			},
			close:      -1,
			wantLogged: []int{0, 0, 1, 1, 1, 2, 3},
			want:       []string{"0:reasoning:think", "1:text:abc", "2:message:", "3:text:d"},
		},
		{
			name: "history forgets what came before but keeps numbering",
			events: []chatEvent{
				{Kind: chatEventHistory, History: hello}, text("a"), {Kind: chatEventHistory}, text("b"),
			},
			close:      -1,
			wantLogged: []int{0, 1, 2, 3},
			want:       []string{"2:history:", "3:text:b"},
		},
		{
			name:        "a prompt is pending until resolved",
			events:      []chatEvent{prompt("first"), prompt("second"), resolved(0)},
			close:       -1,
			wantLogged:  []int{0, 1, 2},
			want:        []string{"0:prompt:first", "1:prompt:second", "2:resolved:"},
			wantPending: []string{"second"},
		},
		{
			name:        "a prompt outlives the history that replaces the one it was asked in",
			events:      []chatEvent{text("a"), prompt("asked"), {Kind: chatEventHistory}},
			close:       -1,
			wantLogged:  []int{0, 1, 2},
			want:        []string{"2:history:"},
			wantPending: []string{"asked"},
		},
		{
			name:       "a closed log takes nothing",
			events:     []chatEvent{text("a"), brk, text("b"), text("c")},
			close:      3,
			wantLogged: []int{0, 1, 2},
			want:       []string{"0:text:a", "1:message:", "2:text:b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var l chatLog
			var logged []int
			for i, ev := range tt.events {
				if i == tt.close {
					l.close()
				}
				got, ok := l.append(ev)
				require.Equal(t, tt.close < 0 || i < tt.close, ok)
				if !ok {
					continue
				}
				logged = append(logged, got.Seq)
				if ev.Kind.streams() {
					assert.Equal(t, ev.Text, got.Text, "a view following the chat is shown the chunk")
				}
			}
			assert.Equal(t, tt.wantLogged, logged)
			events, state := l.snapshot()
			got := make([]string, len(events))
			for i, ev := range events {
				got[i] = describeChatEvent(ev)
			}
			assert.Equal(t, tt.want, got)
			var pending []string
			for _, p := range state.Pending {
				assert.Equal(t, chatEventPrompt, tt.events[p.ID].Kind, "a prompt goes by the Seq that asked it")
				pending = append(pending, p.Event.PromptTitle)
			}
			assert.Equal(t, tt.wantPending, pending)
			assert.Equal(t, state, l.State())
		})
	}
}

func TestChatLogSnapshotHoldsWhileAStreamGrows(t *testing.T) {
	var l chatLog
	chunk := func(s string) chatEvent {
		return messageEvent(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventText, Text: s})
	}
	_, ok := l.append(chunk("Hello"))
	require.True(t, ok)
	events, _ := l.snapshot()
	require.Len(t, events, 1)

	const chunks = 10_000
	var wg sync.WaitGroup
	wg.Go(func() {
		for range chunks {
			_, ok := l.append(chunk(" world"))
			assert.True(t, ok)
		}
	})
	for range 1000 {
		assert.Equal(t, "Hello", events[0].Text, "what a view replays does not change under it")
	}
	wg.Wait()

	events, _ = l.snapshot()
	require.Len(t, events, 1, "a stream is one event however many chunks it has")
	assert.Equal(t, "Hello"+strings.Repeat(" world", chunks), events[0].Text)
}
