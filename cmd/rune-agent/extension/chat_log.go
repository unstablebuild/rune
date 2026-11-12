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

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
)

// chatEventKind is what a chatEvent tells.
type chatEventKind uint8

const (
	// chatEventHistory replaces the transcript with History, the
	// committed messages of the dialogue.
	chatEventHistory chatEventKind = iota
	// chatEventMessage shows Message, which carries no channel and is
	// not a chunk of a stream.
	chatEventMessage
	// chatEventText shows Text as the next chunk of the reply streamed.
	chatEventText
	// chatEventReasoning shows Text as the next chunk of the reasoning
	// streamed.
	chatEventReasoning
	// chatEventPrompt asks Prompt, which waits for an answer.
	chatEventPrompt
	// chatEventResolved tells the prompt Prompt.ID names was answered,
	// with the text Text for a free-form one, or withdrawn.
	chatEventResolved
	// chatEventQueued queues the follow-up Text for when the turn ends.
	chatEventQueued
	// chatEventUnqueued takes back the follow-up queued last.
	chatEventUnqueued
	// chatEventDequeued submits the follow-up queued first.
	chatEventDequeued
)

// chatEvent is an entry of a chatLog. It is small, and what it points
// to is never changed once logged, so that the log can hold a long chat
// and hand its events to every view.
type chatEvent struct {
	// Seq numbers the event in its log, from 0. The chunks of one
	// stream go by the Seq of the event that holds them.
	Seq     int
	Kind    chatEventKind
	Text    string
	History []llmapi.Message
	Message *dialoguetui.MessageEvent
	Prompt  chatPrompt
}

// chatPrompt is a question the agent waits on the user for.
type chatPrompt struct {
	// ID is the Seq of the event that asked it.
	ID int
	// Event shows it. Its PromptResult is nil: the answer goes to the
	// session, which hands it to whoever asked.
	Event *dialoguetui.MessageEvent
}

// messageEvent is the event that shows ev as the agent loop sent it. A
// chunk of a stream goes by its text alone: a reply is thousands of
// them, and a view shows a run of chunks as it shows them one by one.
func messageEvent(ev dialoguetui.MessageEvent) chatEvent {
	switch ev.Type {
	case dialoguetui.MessageEventText:
		return chatEvent{Kind: chatEventText, Text: ev.Text}
	case dialoguetui.MessageEventReasoning:
		return chatEvent{Kind: chatEventReasoning, Text: ev.Text}
	}
	return chatEvent{Kind: chatEventMessage, Message: &ev}
}

// streams reports whether events of kind are chunks of a stream, which
// the log gathers into one event per run.
func (kind chatEventKind) streams() bool {
	return kind == chatEventText || kind == chatEventReasoning
}

// chatState is what the events of a chatLog fold into.
type chatState struct {
	// Pending are the prompts that wait for an answer, oldest first.
	Pending []chatPrompt
}

// chatLog is the append-only log of what a chat session shows, which a
// view replays to show the session as the views already open do. A
// history event replaces the transcript, so the log forgets what came
// before it, keeping numbering events on. Consecutive chunks of a stream
// are one event, whose Text grows. It is safe for concurrent use. Once
// closed, appends are ignored.
type chatLog struct {
	mu     sync.Mutex
	events []chatEvent
	// run holds the text of the last event while it is a stream, so
	// that a chunk costs its own bytes rather than a copy of the run.
	// The strings taken from it stay valid as it grows: a write only
	// ever lands past them.
	run    strings.Builder
	next   int
	state  chatState
	closed bool
}

// append logs ev and returns it numbered, or reports false when the log
// is closed. A chunk that continues the stream of the last event joins
// it, and is returned as the chunk, with that event's Seq, for the
// views that show the chat already.
func (l *chatLog) append(ev chatEvent) (chatEvent, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return chatEvent{}, false
	}
	if n := len(l.events); n > 0 && ev.Kind.streams() && l.events[n-1].Kind == ev.Kind {
		last := &l.events[n-1]
		l.run.WriteString(ev.Text)
		last.Text = l.run.String()
		ev.Seq = last.Seq
		return ev, true
	}
	ev.Seq = l.next
	l.next++
	switch ev.Kind {
	case chatEventHistory:
		l.events = []chatEvent{ev}
	case chatEventPrompt:
		ev.Prompt.ID = ev.Seq
		l.events = append(l.events, ev)
		l.state.Pending = append(l.state.Pending, ev.Prompt)
	case chatEventResolved:
		l.events = append(l.events, ev)
		l.state.Pending = slices.DeleteFunc(slices.Clone(l.state.Pending), func(p chatPrompt) bool {
			return p.ID == ev.Prompt.ID
		})
	default:
		if ev.Kind.streams() {
			l.run.Reset()
			l.run.WriteString(ev.Text)
			ev.Text = l.run.String()
		}
		l.events = append(l.events, ev)
	}
	return ev, true
}

// snapshot returns the events a view replays, in order, and the state
// they leave. The events are the caller's: the log goes on without
// touching them.
func (l *chatLog) snapshot() ([]chatEvent, chatState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events), l.state
}

// State returns the state folded from every event so far.
func (l *chatLog) State() chatState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// close ends the log. Only the first close has effect.
func (l *chatLog) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
}
