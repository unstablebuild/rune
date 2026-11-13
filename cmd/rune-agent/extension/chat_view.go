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
	stdimage "image"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/debug"
)

// chatClosedText is what the views of a chat that ended tell.
const chatClosedText = "Chat closed."

// chatView shows a chat session in a tab. It has a dialoguetui component
// and handler of its own, so its size, scroll, selection, compose input,
// history, collapse mode and hovers are its own, and shows the rest as
// the session's log tells it.
type chatView struct {
	s   *chatSession
	uri workspaceapi.URI
	// ctx ends when the view closes. The view outlives its session,
	// which may end before.
	ctx    context.Context
	cancel context.CancelFunc
	closed sync.Once
	drawn  sync.Once

	// mu guards comp, and the fields below.
	mu      sync.Mutex
	comp    *dialoguetui.Component
	handler tui.Handler
	rx      <-chan dialoguetui.SubmitMessage
	// asking is the ID of the prompt the view shows, or -1. It shows
	// one at a time, the oldest pending first.
	asking      int
	askingEvent *dialoguetui.MessageEvent
	// answered are the prompts answered in this view, which shows
	// their answer already.
	answered map[int]bool

	// txMu guards tx against its close: a sender holds it shared for
	// the send, and close takes it exclusively once ctx ended, which
	// lets the sender go.
	txMu     sync.RWMutex
	tx       chan<- dialoguetui.MessageEvent
	txClosed bool
}

// chatSender takes the events workspace commands inject into a chat,
// as the view of it last focused does.
type chatSender interface {
	// send hands ev to the chat and reports whether it took it, which
	// a view that closed does not.
	send(ev dialoguetui.MessageEvent) bool
}

func newChatView(s *chatSession, uri workspaceapi.URI) *chatView {
	h := s.h
	ctx, cancel := context.WithCancel(h.ctx)
	v := &chatView{
		s:        s,
		uri:      uri,
		ctx:      ctx,
		cancel:   cancel,
		asking:   -1,
		answered: make(map[int]bool),
	}
	v.comp = dialoguetui.NewComponent(h.dialogueConfig(&v.mu))
	v.handler, v.tx, v.rx = dialoguetui.Handler(ctx, &v.mu, v.comp, h.p,
		dialoguetui.WithCommands(s.commands),
		dialoguetui.WithContextCompleter(newContextCompleter(h.fs, h.parser)),
		dialoguetui.WithAttachmentOpener(attachmentPreviewer{h}),
		dialoguetui.WithToolCallOpener(newToolCallPreviewer(h)),
		dialoguetui.WithCloseFunc(func() { s.end(sessionEndChatClose) }),
		dialoguetui.WithoutSubmitEcho(),
		dialoguetui.WithCommandEcho(),
		dialoguetui.WithSubmitQueue(s),
	)
	go debug.CapturePanicReport(v.submits)
	return v
}

// send satisfies chatSender.
func (v *chatView) send(ev dialoguetui.MessageEvent) bool {
	v.txMu.RLock()
	defer v.txMu.RUnlock()
	if v.txClosed {
		return false
	}
	select {
	case v.tx <- ev:
		return true
	case <-v.ctx.Done():
		return false
	}
}

// content returns the view as the content of its tab. Closing it closes
// the view.
func (v *chatView) content() browserapi.Handler {
	return browserapi.FuncHandler(chatViewHandler{Handler: handler.Wrap(v.handler, v.handle), v: v}, v.close)
}

// chatViewHandler is a view as the content of its tab, which the host
// shows once it draws it.
type chatViewHandler struct {
	tui.Handler
	v *chatView
}

func (h chatViewHandler) Draw(w term.Writer) {
	// Reporting at the tab needs the lock the host draws under.
	h.v.drawn.Do(func() { go debug.CapturePanicReport(h.v.reportTab) })
	h.Handler.Draw(w)
}

// reportTab has the chat report at the view's tab until the view closes.
func (v *chatView) reportTab() {
	v.s.tab.attach(v.uri)
	<-v.ctx.Done()
	v.s.tab.detach(v.uri)
}

// handle makes Ctrl-C cancel the running turn once the chat had its say:
// it first dismisses a prompt or clears the compose input. An open '#'
// completion band owns Ctrl-C. The view of a session that ended closes
// its tab on the next event, as an extension cannot close a tab itself.
func (v *chatView) handle(ev term.Event) (exit, handled bool) {
	s := v.s
	if s.isEnded() {
		return true, true
	}
	if ev.Type != term.EventResize {
		// Handle only runs while this view has browser focus, so it is
		// the signal workspace commands use to find the target chat.
		s.h.recordFocusedChat(s.id)
		s.focus(v)
	}
	if ev.Ch != 'c' || ev.Mod != term.ModCtrl {
		return v.handler.Handle(ev)
	}
	v.mu.Lock()
	completing := v.comp.CompletionOpen()
	v.mu.Unlock()
	exit, _ = v.handler.Handle(ev)
	if completing {
		return exit, true
	}
	if s.interrupt() {
		_, _ = s.h.n.Notify(browserapi.LevelInfo, "canceled completion request")
	}
	return exit, true
}

// submits sends the session what the user submits in the view.
func (v *chatView) submits() {
	for {
		var msg dialoguetui.SubmitMessage
		select {
		case <-v.ctx.Done():
			return
		case msg = <-v.rx:
		}
		if err := v.s.submit(msg); err != nil {
			v.mu.Lock()
			v.comp.AddErrorMessage(err.Error())
			if v.comp.Draft().Empty() {
				v.comp.RestoreDraft(msg.Draft())
			}
			v.mu.Unlock()
			_ = v.s.h.p.Interrupt(v.ctx)
		}
	}
}

// replay shows events, which the session logged so far, and status.
func (v *chatView) replay(events []chatEvent, status dialoguetui.StatusBarState) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, ev := range events {
		v.showLocked(ev)
	}
	v.askNextLocked()
	v.comp.SetStatusBarState(func(s *dialoguetui.StatusBarState) { *s = status })
}

// show shows ev, which the session just logged.
func (v *chatView) show(ev chatEvent) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.showLocked(ev)
}

func (v *chatView) showLocked(ev chatEvent) {
	switch ev.Kind {
	case chatEventHistory:
		// The prompt is taken down before its rows go, and shown again
		// below the history.
		var asking chan<- []string
		if v.asking >= 0 {
			asking = v.comp.PreparePromptDismiss()
		}
		v.comp.Reset()
		pending := make(map[string]llmapi.ToolCall)
		for _, msg := range ev.History {
			addMessage(v.comp, msg, pending)
		}
		if asking != nil {
			v.addPromptLocked(v.askingEvent, asking)
		}
	case chatEventMessage:
		v.comp.ApplyEvent(*ev.Message)
	case chatEventText:
		v.comp.AddReceiveMessageChunk(ev.Text)
	case chatEventReasoning:
		v.comp.AddReasoningChunk(ev.Text)
	case chatEventPrompt:
		v.askNextLocked()
	case chatEventResolved:
		id := ev.Prompt.ID
		if v.asking == id {
			v.asking = -1
			dismiss(v.comp.PreparePromptDismiss())
		}
		if ev.Text != "" && !v.answered[id] {
			v.comp.AddSendMessage(ev.Text)
		}
		delete(v.answered, id)
		v.askNextLocked()
	case chatEventQueued:
		v.comp.AddQueuedMessage(ev.Text)
	case chatEventUnqueued:
		v.comp.RemoveLastQueuedMessage()
	case chatEventDequeued:
		v.comp.PromoteFirstQueuedMessage()
	}
}

// askNextLocked shows the oldest prompt pending unless one is shown.
func (v *chatView) askNextLocked() {
	if v.asking >= 0 {
		return
	}
	pending := v.s.log.State().Pending
	if len(pending) == 0 {
		return
	}
	p := pending[0]
	// Buffered, as the view takes a prompt down under its lock.
	ch := make(chan []string, 1)
	v.asking, v.askingEvent = p.ID, p.Event
	v.addPromptLocked(p.Event, ch)
	go debug.CapturePanicReport(func() { v.answer(p.ID, ch) })
}

func (v *chatView) addPromptLocked(ev *dialoguetui.MessageEvent, ch chan<- []string) {
	dismiss(v.comp.AddPrompt(ev.PromptTitle, ev.PromptHeader, ev.PromptBody,
		ev.PromptOptions, ev.PromptMultiSelect, ch))
}

// dismiss tells the prompt that answers on ch, if any, it was taken down.
func dismiss(ch chan<- []string) {
	if ch == nil {
		return
	}
	select {
	case ch <- nil:
	default:
	}
}

// answer gives the session the answer to the prompt id, shown with ch,
// unless the view closes first.
func (v *chatView) answer(id int, ch <-chan []string) {
	var vals []string
	select {
	case vals = <-ch:
	case <-v.ctx.Done():
		return
	}
	v.mu.Lock()
	if v.asking == id {
		v.asking = -1
		v.answered[id] = true
	}
	v.mu.Unlock()
	v.s.answer(id, vals)
}

// setStatusBarState shows state in the view's status bar.
func (v *chatView) setStatusBarState(state dialoguetui.StatusBarState) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.comp.SetStatusBarState(func(s *dialoguetui.StatusBarState) { *s = state })
}

// upsertAttachment adds a to the view's pending attachments.
func (v *chatView) upsertAttachment(a dialoguetui.Attachment) {
	v.mu.Lock()
	v.comp.UpsertAttachment(a)
	v.mu.Unlock()
	_ = v.s.h.p.Interrupt(v.ctx)
}

// restyle draws the view with the status bar, compose box frame and
// compose box colours of style, whose frame images by URI are
// frameImages.
func (v *chatView) restyle(style dialoguetui.ComponentConfig, frameImages map[string]stdimage.Image) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.comp.SetStatusBarConfig(style.StatusBar)
	v.comp.SetInputBoxFrameCharSet(style.InputBox.FrameCharSet, frameImages)
	v.comp.SetInputBoxColors(style.InputBox.FrameAttr, style.InputBox.ContentAttr, style.InputBackgroundColor)
}

// end tells the view its session ended.
func (v *chatView) end() {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.asking >= 0 {
		v.asking = -1
		dismiss(v.comp.PreparePromptDismiss())
	}
	v.comp.AddWarningMessage(chatClosedText)
}

// close closes the view, which the session forgets first: ending with
// its last view, the session closes its reporter before the view's tab
// is detached, which would otherwise hand the chat to the inbox.
func (v *chatView) close() error {
	v.closed.Do(func() {
		v.s.detach(v)
		v.cancel()
		v.mu.Lock()
		_ = v.comp.Close()
		v.mu.Unlock()
		v.txMu.Lock()
		v.txClosed = true
		close(v.tx)
		v.txMu.Unlock()
	})
	return nil
}
