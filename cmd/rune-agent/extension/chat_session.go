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
	"errors"
	"fmt"
	stdimage "image"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/cmd/rune-agent/hooks"
	"unstable.build/rune/internal/debug"
)

// Reasons the SessionEnd hook reports a chat session ended for.
const (
	sessionEndTabClose  = "tab_close"
	sessionEndChatClose = "chat_close"
)

// errChatClosed is what a chat session that ended answers with.
var errChatClosed = errors.New("the agent chat is closed")

// chatSession is a rune-agent chat: its agent, the turn it runs and the
// log of what it shows, which every view of the chat replays and then
// follows. Closing its last view ends an idle session, and leaves a busy
// one working, listed in the status inbox, until it is idle again. /exit
// and chatclose end it in every view.
type chatSession struct {
	h  *aiEditorHandler
	id string
	// uri is the URI the chat goes by when no tab shows it.
	uri    workspaceapi.URI
	ctx    context.Context
	cancel context.CancelFunc
	// done is closed once an ended session is no longer open, when
	// the dialogue can be opened again.
	done chan struct{}
	log  chatLog
	tab  *tabStatusReporter

	agent      *agent.Agent
	commands   *commandAdapter
	webBrowser *agentools.WebBrowser

	// tx carries what the agent loop shows to the log.
	tx chan dialoguetui.MessageEvent
	// submits carries the message the next turn answers. Turns run
	// one at a time, so it never holds more than one.
	submits  chan dialoguetui.SubmitMessage
	requests chan completionRequest

	// pubMu orders what the views show: the log grows, and the status
	// bar changes, while it is held, which is when the views are
	// shown the change. It must not be taken holding a view's lock.
	pubMu  sync.Mutex
	status dialoguetui.StatusBarState

	mu sync.Mutex
	// views show the chat, the last focused last.
	views []*chatView
	busy  bool
	queue []dialoguetui.SubmitMessage
	// prompts are the prompts pending, by ID.
	prompts map[int]sessionPrompt
	// interruptTurn cancels the running turn.
	interruptTurn context.CancelFunc
	ended         bool
}

// sessionPrompt is where the answer to a pending prompt goes.
type sessionPrompt struct {
	result   chan<- []string
	freeForm bool
}

func newChatSession(h *aiEditorHandler, id string, uri workspaceapi.URI) *chatSession {
	ctx, cancel := context.WithCancel(h.ctx)
	return &chatSession{
		h:        h,
		id:       id,
		uri:      uri,
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
		tab:      newTabStatusReporter(h.wm, uri, h.n),
		tx:       make(chan dialoguetui.MessageEvent),
		submits:  make(chan dialoguetui.SubmitMessage, 1),
		requests: make(chan completionRequest),
		prompts:  make(map[int]sessionPrompt),
	}
}

// run starts carrying messages to the agent loop and what it shows to
// the log.
func (s *chatSession) run() {
	go debug.CapturePanicReport(s.pump)
	go debug.CapturePanicReport(s.buildRequests)
}

// pump logs what the agent loop shows, and ends the turn it reports
// ended.
func (s *chatSession) pump() {
	for ev := range s.tx {
		switch ev.Type {
		case dialoguetui.MessageEventBusy:
			if !ev.Busy {
				s.turnEnded()
			}
		case dialoguetui.MessageEventPrompt, dialoguetui.MessageEventPromptDismiss,
			dialoguetui.MessageEventCommand, dialoguetui.MessageEventAttachment:
			slog.Warn("chat session drops an event its views own", "id", s.id, "type", ev.Type)
		default:
			s.publish(messageEvent(ev))
		}
	}
}

// buildRequests turns each message submitted into the request the agent
// loop answers.
func (s *chatSession) buildRequests() {
	for {
		var msg dialoguetui.SubmitMessage
		select {
		case <-s.ctx.Done():
			return
		case msg = <-s.submits:
		}
		ctx, cancel := context.WithCancel(s.ctx)
		s.mu.Lock()
		s.interruptTurn = cancel
		s.mu.Unlock()
		select {
		case s.requests <- s.h.newCompletionRequest(ctx, msg):
		case <-s.ctx.Done():
			return
		}
	}
}

// interrupt cancels the running turn and reports whether one was.
func (s *chatSession) interrupt() bool {
	s.mu.Lock()
	cancel := s.interruptTurn
	s.interruptTurn = nil
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// publish logs ev and shows it in every view.
func (s *chatSession) publish(ev chatEvent) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	s.publishLocked(ev)
}

// publishLocked is publish for callers holding pubMu. It returns ev as
// logged, or false once the session ended.
func (s *chatSession) publishLocked(ev chatEvent) (chatEvent, bool) {
	ev, ok := s.log.append(ev)
	if !ok {
		return ev, false
	}
	views := s.shownIn()
	for _, v := range views {
		v.show(ev)
	}
	if len(views) > 0 {
		_ = s.h.p.Interrupt(context.Background())
	}
	return ev, true
}

// shownIn returns the views of the chat.
func (s *chatSession) shownIn() []*chatView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.views)
}

// replace replaces the transcript with msgs.
func (s *chatSession) replace(msgs []llmapi.Message) {
	s.publish(chatEvent{Kind: chatEventHistory, History: msgs})
}

// setStatusBarState applies fn to the status bar every view shows.
func (s *chatSession) setStatusBarState(fn func(*dialoguetui.StatusBarState)) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	fn(&s.status)
	for _, v := range s.shownIn() {
		v.setStatusBarState(s.status)
	}
}

// submit sends msg, or queues it while a turn runs.
func (s *chatSession) submit(msg dialoguetui.SubmitMessage) error {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return errChatClosed
	}
	if s.busy {
		s.queue = append(s.queue, msg)
		s.mu.Unlock()
		s.publishLocked(chatEvent{Kind: chatEventQueued, Text: submittedText(msg)})
		return nil
	}
	s.busy = true
	s.mu.Unlock()
	s.startLocked(msg)
	return nil
}

// startLocked shows msg sent and hands it to the agent loop. It must
// be called holding pubMu, with busy set.
func (s *chatSession) startLocked(msg dialoguetui.SubmitMessage) {
	s.publishLocked(messageEvent(dialoguetui.MessageEvent{
		Type:        dialoguetui.MessageEventUserMessage,
		Text:        submittedText(msg),
		Attachments: msg.Attachments,
	}))
	s.submits <- msg
}

// submittedText is how msg reads in the transcript: a skill command
// reads as its command line, as when its turn is replayed.
func submittedText(msg dialoguetui.SubmitMessage) string {
	if msg.SkillName != "" {
		if line, ok := parseStoredCommandMessage(msg.Text); ok {
			return line
		}
	}
	return msg.Text
}

// TakeLast satisfies dialoguetui.SubmitQueue.
func (s *chatSession) TakeLast() (dialoguetui.SubmitMessage, bool) {
	s.mu.Lock()
	n := len(s.queue)
	if n == 0 {
		s.mu.Unlock()
		return dialoguetui.SubmitMessage{}, false
	}
	msg := s.queue[n-1]
	s.queue = s.queue[:n-1]
	s.mu.Unlock()
	// The view taking it back holds its lock, which showing it the
	// event needs.
	go debug.CapturePanicReport(func() {
		s.publish(chatEvent{Kind: chatEventUnqueued})
	})
	return msg, true
}

// turnEnded starts the turn of the follow-up queued first, or leaves
// the session idle, which ends it when no view shows it.
func (s *chatSession) turnEnded() {
	s.pubMu.Lock()
	s.mu.Lock()
	if len(s.queue) > 0 && !s.ended {
		msg := s.queue[0]
		s.queue = s.queue[1:]
		s.mu.Unlock()
		s.publishLocked(chatEvent{Kind: chatEventDequeued})
		s.startLocked(msg)
		s.pubMu.Unlock()
		return
	}
	s.busy = false
	end := s.endIdleLocked()
	s.mu.Unlock()
	s.pubMu.Unlock()
	if end {
		s.finish(sessionEndTabClose)
	}
}

// endIdleLocked marks the session ended when no view shows it and it
// has nothing left to do, and reports whether it did. It must be
// called holding mu.
func (s *chatSession) endIdleLocked() bool {
	if s.ended || len(s.views) > 0 || s.busy || len(s.prompts) > 0 {
		return false
	}
	s.ended = true
	return true
}

// ask shows the prompt ev in every view and returns its ID, which
// answer takes, or false once the session ended. The answer goes to
// ev.PromptResult, which must be buffered.
func (s *chatSession) ask(ev dialoguetui.MessageEvent) (int, bool) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	result := ev.PromptResult
	ev.PromptResult = nil
	logged, ok := s.publishLocked(chatEvent{Kind: chatEventPrompt, Prompt: chatPrompt{Event: &ev}})
	if !ok {
		return 0, false
	}
	s.mu.Lock()
	s.prompts[logged.Seq] = sessionPrompt{result: result, freeForm: len(ev.PromptOptions) == 0}
	s.mu.Unlock()
	return logged.Seq, true
}

// answer answers the prompt id with vals, nil to dismiss it. The first
// answer wins; the views that did not give it take the prompt down.
func (s *chatSession) answer(id int, vals []string) {
	s.pubMu.Lock()
	s.mu.Lock()
	p, ok := s.prompts[id]
	delete(s.prompts, id)
	s.mu.Unlock()
	if !ok {
		s.pubMu.Unlock()
		return
	}
	var text string
	if p.freeForm && len(vals) > 0 {
		text = vals[0]
	}
	s.publishLocked(chatEvent{Kind: chatEventResolved, Prompt: chatPrompt{ID: id}, Text: text})
	s.pubMu.Unlock()
	p.result <- vals
}

// attach opens a new view of the chat, shown at uri, or at a URI of its
// own when uri is zero. It fails with errChatClosed once the session
// ended.
func (s *chatSession) attach(uri workspaceapi.URI) (*chat, error) {
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return nil, errChatClosed
	}
	uri, err := s.viewURILocked(uri)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	v := newChatView(s, uri)
	events, _ := s.log.snapshot()
	v.replay(events, s.status)
	s.mu.Lock()
	s.views = append(s.views, v)
	s.mu.Unlock()
	s.h.focusedViews.Store(s.id, v)
	return &chat{content: v.content(), id: s.id, uri: uri}, nil
}

// viewURILocked returns the URI a new view is shown at: requested, or
// the chat's own URI unless a view is shown there, and a numbered one
// otherwise. It must be called holding mu.
func (s *chatSession) viewURILocked(requested workspaceapi.URI) (workspaceapi.URI, error) {
	shown := func(uri workspaceapi.URI) bool {
		return slices.ContainsFunc(s.views, func(v *chatView) bool { return sameURI(v.uri, uri) })
	}
	if requested != (workspaceapi.URI{}) {
		if shown(requested) {
			return workspaceapi.URI{}, fmt.Errorf("agent chat %q is already shown at %s", s.id, requested)
		}
		return requested, nil
	}
	if !shown(s.uri) {
		return s.uri, nil
	}
	for n := 2; ; n++ {
		uri, err := workspaceapi.ParseURI(fmt.Sprintf("%s?view=%d", s.uri, n))
		if err != nil {
			return workspaceapi.URI{}, err
		}
		if !shown(uri) {
			return uri, nil
		}
	}
}

// chatBaseURI returns uri without the view number a second view of a
// chat is shown with.
func chatBaseURI(uri workspaceapi.URI) (workspaceapi.URI, error) {
	if base, _, ok := strings.Cut(uri.String(), "?"); ok {
		return workspaceapi.ParseURI(base)
	}
	return uri, nil
}

// sameURI reports whether a and b identify the same tab, which the
// host tells apart by the whole URI, unlike URI.Equal, which ignores the
// view number of a chat's other views.
func sameURI(a, b workspaceapi.URI) bool {
	return a.String() == b.String()
}

// shownURI returns the URI of the view last focused, or the chat's own
// URI when no view shows it.
func (s *chatSession) shownURI() workspaceapi.URI {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := len(s.views); n > 0 {
		return s.views[n-1].uri
	}
	return s.uri
}

// detached reports whether no view shows the chat.
func (s *chatSession) detached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.views) == 0
}

// isEnded reports whether the session ended.
func (s *chatSession) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

// focus records v as the view last focused, which workspace commands
// and attachments go to.
func (s *chatSession) focus(v *chatView) {
	s.mu.Lock()
	n := len(s.views)
	i := slices.Index(s.views, v)
	if i < 0 || i == n-1 {
		s.mu.Unlock()
		return
	}
	s.views = append(slices.Delete(s.views, i, i+1), v)
	s.mu.Unlock()
	s.h.focusedViews.Store(s.id, v)
}

// detach forgets v, which closed. The session ends when it was the
// last view and the session is idle.
func (s *chatSession) detach(v *chatView) {
	s.pubMu.Lock()
	s.mu.Lock()
	s.views = slices.DeleteFunc(s.views, func(o *chatView) bool { return o == v })
	var focused *chatView
	if n := len(s.views); n > 0 {
		focused = s.views[n-1]
	}
	end := s.endIdleLocked()
	s.mu.Unlock()
	s.pubMu.Unlock()
	if focused != nil {
		s.h.focusedViews.Store(s.id, focused)
	} else {
		s.h.focusedViews.CompareAndDelete(s.id, v)
	}
	if end {
		s.finish(sessionEndTabClose)
	}
}

// attachToFocused adds a to the pending attachments of the view last
// focused.
func (s *chatSession) attachToFocused(a dialoguetui.Attachment) {
	s.mu.Lock()
	var v *chatView
	if n := len(s.views); n > 0 {
		v = s.views[n-1]
	}
	s.mu.Unlock()
	if v == nil {
		slog.Warn("drop attachment for a chat shown nowhere", "id", s.id, "attachment", a.ID)
		return
	}
	v.upsertAttachment(a)
}

// restyle draws every view with the status bar, compose box frame and
// compose box colours of style, whose frame images by URI are
// frameImages.
func (s *chatSession) restyle(style dialoguetui.ComponentConfig, frameImages map[string]stdimage.Image) {
	for _, v := range s.shownIn() {
		v.restyle(style, frameImages)
	}
}

// end ends the session in every view, for reason.
func (s *chatSession) end(reason string) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.mu.Unlock()
	s.finish(reason)
}

// finish releases what the ended session holds and tells its views.
func (s *chatSession) finish(reason string) {
	s.cancel()
	s.tab.close()
	s.pubMu.Lock()
	s.log.close()
	views := s.shownIn()
	s.pubMu.Unlock()
	for _, v := range views {
		v.end()
		s.h.focusedViews.CompareAndDelete(s.id, v)
	}
	s.h.unsubscribeTools(s.id)
	s.h.openChats.CompareAndDelete(s.id, s)
	close(s.done)
	if s.webBrowser != nil {
		s.webBrowser.Close()
	}
	// Purely observational; no result fields are honored.
	s.h.hookRunner.Run(s.h.ctx, hooks.Payload{
		SessionID:     s.id,
		Cwd:           s.h.cwd,
		HookEventName: hooks.EventSessionEnd,
		Reason:        reason,
	})
	_ = s.h.p.Interrupt(context.Background())
}
