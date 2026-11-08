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

package acpcmd

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdimage "image"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/sergi/go-diff/diffmatchpatch"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi/acpstdio"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/cmd/rune-agent/dialogue/dialoguetui"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/debug"
	mdhandler "unstable.build/rune/internal/handler/markdown"
)

// Status bar phases, matching dialoguetui.DefaultStatuses.
const (
	phaseSending   = "SENDING"
	phaseReasoning = "REASONING"
	phaseReceiving = "RECEIVING"
	phaseExecuting = "EXECUTING"
)

const (
	// diffContext is how many unchanged lines a tool's diff keeps around
	// each change.
	diffContext = 3
	// previewWidth and previewHeight bound a tool call's preview.
	previewWidth  = 100
	previewHeight = 45
)

// chatConfig is what a chat is built from.
type chatConfig struct {
	// Name is the session's name.
	Name string
	// Open returns the session to show. It runs once, off the event loop,
	// with a context that ends when the chat closes.
	Open          func(context.Context) (acpapi.Session, error)
	Component     dialoguetui.ComponentConfig
	WindowManager browserapi.WindowManager
	Interrupter   term.Interrupter
	Parser        syntaxapi.Parser
	// Root is the workspace root path, which relative attachment paths
	// are in.
	Root string
}

// chat shows a session in a dialoguetui chat. Every viewer of a session
// folds the same events, so chats on one session stay in sync, and the
// session outlives its chats.
type chat struct {
	cfg    chatConfig
	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once

	// mu guards comp.
	mu   sync.Mutex
	comp *dialoguetui.Component
	h    tui.Handler
	// tx is only sent to, and closed, by run.
	tx chan<- dialoguetui.MessageEvent
	rx <-chan dialoguetui.SubmitMessage

	// ready is closed once session or openErr is set.
	ready   chan struct{}
	session acpapi.Session
	openErr error

	stateMu sync.Mutex
	state   acpapi.SessionState
	// resolved are the permissions prompted for that were answered or
	// withdrawn, so dismissing their prompt must not answer them.
	resolved map[acpapi.PermissionID]bool

	// The rest is owned by run.
	tools     map[toolKey]*toolCall
	subagents map[acp.SessionId]*subagent
	messages  map[messageKey]*sessionMessage
	// running are the turn's top-level tool calls and subagents yet to
	// finish, by node. The agent is executing while any runs, and
	// otherwise waits on its model.
	running map[string]bool
	// user is the replayed user message being received, if any.
	user    []acp.ContentBlock
	planLen int
	// asking is the permission prompted for, if any. The chat prompts
	// for one permission at a time, oldest first.
	asking *acpapi.PermissionID
}

// newChat starts a chat. The chat ends when its content closes.
func newChat(cfg chatConfig) *chat {
	c := initChat(cfg)
	c.start()
	return c
}

func initChat(cfg chatConfig) *chat {
	ctx, cancel := context.WithCancel(context.Background())
	c := &chat{
		cfg:       cfg,
		ctx:       ctx,
		cancel:    cancel,
		comp:      dialoguetui.NewComponent(cfg.Component),
		ready:     make(chan struct{}),
		resolved:  make(map[acpapi.PermissionID]bool),
		tools:     make(map[toolKey]*toolCall),
		subagents: make(map[acp.SessionId]*subagent),
		messages:  make(map[messageKey]*sessionMessage),
		running:   make(map[string]bool),
	}
	c.h, c.tx, c.rx = dialoguetui.Handler(ctx, &c.mu, c.comp, cfg.Interrupter,
		dialoguetui.WithCommands(c),
		dialoguetui.WithToolCallOpener(c),
		dialoguetui.WithoutSubmitEcho(),
	)
	c.comp.SetStatusBarState(func(s *dialoguetui.StatusBarState) {
		s.Conversation = cfg.Name
	})
	return c
}

func (c *chat) start() {
	go debug.CapturePanicReport(c.run)
	go debug.CapturePanicReport(c.submits)
}

// content returns the chat as a tab's content. Closing it ends the chat
// and calls onClose; the session keeps running.
func (c *chat) content(onClose func()) browserapi.Handler {
	return browserapi.FuncHandler(handler.Wrap(c.h, c.handle), func() error {
		c.once.Do(func() {
			c.cancel()
			c.mu.Lock()
			_ = c.comp.Close()
			c.mu.Unlock()
			if onClose != nil {
				onClose()
			}
		})
		return nil
	})
}

// handle makes Ctrl-C cancel the running turn once the chat had its say:
// it first dismisses a prompt or clears the compose input.
func (c *chat) handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey || ev.Mod != term.ModCtrl || ev.Ch != 'c' {
		return c.h.Handle(ev)
	}
	c.mu.Lock()
	completing := c.comp.CompletionOpen()
	c.mu.Unlock()
	exit, _ = c.h.Handle(ev)
	if completing || !c.busy() {
		return exit, true
	}
	go debug.CapturePanicReport(func() {
		s, err := c.waitSession(c.ctx)
		if err != nil {
			return
		}
		if err := s.Cancel(c.ctx); err != nil {
			c.showError("cancel: " + acpstdio.ErrorText(err))
		}
	})
	return exit, true
}

func (c *chat) busy() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.Busy
}

func (c *chat) sessionState() acpapi.SessionState {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state
}

// waitSession returns the session once it is open.
func (c *chat) waitSession(ctx context.Context) (acpapi.Session, error) {
	select {
	case <-c.ready:
		return c.session, c.openErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// run opens the session and folds its events into the chat until either
// ends.
func (c *chat) run() {
	defer close(c.tx)
	s, err := c.cfg.Open(c.ctx)
	c.session, c.openErr = s, err
	close(c.ready)
	if err != nil {
		if c.ctx.Err() == nil {
			c.send(dialoguetui.MessageEvent{
				Type: dialoguetui.MessageEventError,
				Text: "start session: " + acpstdio.ErrorText(err),
			})
		}
		return
	}
	it := s.Events()
	defer func() { _ = it.Close() }()
	for {
		ev, ok := it.Next(c.ctx)
		if !ok {
			break
		}
		c.stateMu.Lock()
		acpapi.Apply(&c.state, ev)
		state := c.state
		c.stateMu.Unlock()
		var o output
		c.apply(ev, state, &o)
		if !c.flush(&o) {
			return
		}
	}
	if c.ctx.Err() != nil {
		return
	}
	var o output
	c.flushUser(&o)
	if err := it.Err(); err != nil {
		o.send(dialoguetui.MessageEvent{
			Type: dialoguetui.MessageEventError, Text: acpstdio.ErrorText(err),
		})
	} else {
		o.send(dialoguetui.MessageEvent{
			Type: dialoguetui.MessageEventWarning, Text: "Session closed.",
		})
	}
	o.send(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBusy})
	o.set(func(s *dialoguetui.StatusBarState) { s.Active, s.Phase = false, "" })
	c.flush(&o)
}

// output is what folding an event produces.
type output struct {
	events []dialoguetui.MessageEvent
	status []func(*dialoguetui.StatusBarState)
}

func (o *output) send(ev dialoguetui.MessageEvent) { o.events = append(o.events, ev) }

func (o *output) set(fn func(*dialoguetui.StatusBarState)) { o.status = append(o.status, fn) }

// flush shows o, reporting false once the chat closed.
func (c *chat) flush(o *output) bool {
	for _, ev := range o.events {
		if !c.send(ev) {
			return false
		}
	}
	if len(o.status) == 0 {
		return true
	}
	c.mu.Lock()
	for _, fn := range o.status {
		c.comp.SetStatusBarState(fn)
	}
	c.mu.Unlock()
	c.interrupt()
	return true
}

func (c *chat) send(ev dialoguetui.MessageEvent) bool {
	select {
	case c.tx <- ev:
		return true
	case <-c.ctx.Done():
		return false
	}
}

func (c *chat) interrupt() {
	_ = c.cfg.Interrupter.Interrupt(c.ctx)
}

func (c *chat) showError(msg string) {
	c.mu.Lock()
	c.comp.AddErrorMessage(msg)
	c.mu.Unlock()
	c.interrupt()
}

// restyle draws the chat with the status bar, compose box frame and
// compose box colours of style, whose frame images by URI are frameImages,
// unless the chat closed.
func (c *chat) restyle(style dialoguetui.ComponentConfig, frameImages map[string]stdimage.Image) {
	c.mu.Lock()
	if c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.comp.SetStatusBarConfig(style.StatusBar)
	c.comp.SetInputBoxFrameCharSet(style.InputBox.FrameCharSet, frameImages)
	c.comp.SetInputBoxColors(style.InputBox.FrameAttr, style.InputBox.ContentAttr, style.InputBackgroundColor)
	c.mu.Unlock()
	c.interrupt()
}

// apply folds ev, which state already includes, into o.
func (c *chat) apply(ev acpapi.Event, state acpapi.SessionState, o *output) {
	if ev.Session != "" || ev.Update == nil || ev.Update.UserMessageChunk == nil {
		c.flushUser(o)
	}
	switch {
	case ev.Started != nil:
		o.set(configStatus(state.ConfigOptions))
	case ev.Subagent != nil:
		c.subagentUpdate(ev.Subagent.SessionID, state, o)
	case ev.Message != nil:
		c.message(ev.Session, *ev.Message, state)
	case ev.MessageChunk != nil:
		c.messageChunk(ev.Session, *ev.MessageChunk)
	case ev.Update != nil && ev.Session != "":
		c.subagentTraffic(ev.Session, *ev.Update, state, o)
	case ev.Update != nil:
		c.applyUpdate(*ev.Update, state, o)
	case ev.Prompt != nil:
		o.send(userMessage(ev.Prompt.Content))
		o.send(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBusy, Busy: true})
		o.set(func(s *dialoguetui.StatusBarState) {
			s.Active, s.Phase, s.TurnStart = true, phaseSending, time.Now()
		})
	case ev.Permission != nil:
		c.ask(state, o)
	case ev.Resolved != nil:
		if c.asking != nil && *c.asking == ev.Resolved.ID {
			c.dismiss(o)
			phase := c.workPhase()
			o.set(func(s *dialoguetui.StatusBarState) { s.Phase = phase })
		}
		c.ask(state, o)
	case ev.Done != nil:
		clear(c.running)
		if c.asking != nil {
			c.dismiss(o)
		}
		o.send(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBreak})
		if msg := stopWarning(ev.Done.StopReason); msg != "" {
			o.send(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventWarning, Text: msg})
		}
		if ev.Done.Error != "" {
			o.send(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventError, Text: ev.Done.Error})
		}
		o.send(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventBusy})
		o.set(func(s *dialoguetui.StatusBarState) { s.Active, s.Phase = false, "" })
	}
}

func (c *chat) applyUpdate(u acp.SessionUpdate, state acpapi.SessionState, o *output) {
	phase := func(p string) {
		if state.Busy {
			o.set(func(s *dialoguetui.StatusBarState) { s.Phase = p })
		}
	}
	switch {
	case u.UserMessageChunk != nil:
		// Agents only send user messages to replay a session; one sent
		// during a turn echoes the Prompt already shown.
		if !state.Busy {
			c.user = append(c.user, u.UserMessageChunk.Content)
		}
	case u.AgentMessageChunk != nil:
		o.send(dialoguetui.MessageEvent{
			Type: dialoguetui.MessageEventText, Text: contentText(u.AgentMessageChunk.Content),
		})
		phase(phaseReceiving)
	case u.AgentThoughtChunk != nil:
		o.send(dialoguetui.MessageEvent{
			Type: dialoguetui.MessageEventReasoning, Text: contentText(u.AgentThoughtChunk.Content),
		})
		phase(phaseReasoning)
	case u.ToolCall != nil:
		c.updateToolCall("", "", u, o)
		phase(c.workPhase())
	case u.ToolCallUpdate != nil:
		c.updateToolCall("", "", u, o)
		phase(c.workPhase())
	case u.Plan != nil:
		c.plan(u.Plan.Entries, o)
	case u.UsageUpdate != nil:
		used, size := u.UsageUpdate.Used, u.UsageUpdate.Size
		o.set(func(s *dialoguetui.StatusBarState) { s.ContextTokens, s.ContextWindow = used, size })
	case u.ConfigOptionUpdate != nil:
		o.set(configStatus(state.ConfigOptions))
	}
}

func (c *chat) flushUser(o *output) {
	if len(c.user) == 0 {
		return
	}
	o.send(userMessage(c.user))
	c.user = nil
}

// updateToolCall applies u, a tool_call or tool_call_update session sent,
// to its tool call, which shows in the tool call parent, or at the top
// level when parent is empty.
func (c *chat) updateToolCall(session acp.SessionId, parent string, u acp.SessionUpdate, o *output) {
	switch {
	case u.ToolCall != nil:
		t := u.ToolCall
		c.updateTool(toolKey{session, t.ToolCallId}, parent, o, func(tc *toolCall) {
			tc.kind, tc.title, tc.status = t.Kind, t.Title, t.Status
			tc.input, tc.output, tc.content = t.RawInput, t.RawOutput, t.Content
			tc.name, tc.summary = metaTool(t.Meta)
		})
	case u.ToolCallUpdate != nil:
		t := u.ToolCallUpdate
		c.updateTool(toolKey{session, t.ToolCallId}, parent, o, func(tc *toolCall) {
			if t.Kind != nil {
				tc.kind = *t.Kind
			}
			if t.Title != nil {
				tc.title = *t.Title
			}
			if name, summary := metaTool(t.Meta); name != "" {
				tc.name, tc.summary = name, summary
			}
			if t.Status != nil {
				tc.status = *t.Status
			}
			if t.RawInput != nil {
				tc.input = t.RawInput
			}
			if t.RawOutput != nil {
				tc.output = t.RawOutput
			}
			if t.Content != nil {
				tc.content = t.Content
			}
		})
	}
}

// updateTool applies fn to the tool call key, announcing it in parent the
// first time and completing it once it ends.
func (c *chat) updateTool(key toolKey, parent string, o *output, fn func(*toolCall)) {
	tc, known := c.tools[key]
	if !known {
		tc = &toolCall{key: key, parent: parent}
		c.tools[key] = tc
	}
	fn(tc)
	if !known {
		o.send(tc.event(dialoguetui.MessageEventToolCall))
	}
	switch {
	case tc.done:
	case tc.status == acp.ToolCallStatusCompleted || tc.status == acp.ToolCallStatusFailed:
		tc.done = true
		delete(c.running, tc.node())
		o.send(tc.event(dialoguetui.MessageEventToolResult))
	case tc.parent == "":
		c.running[tc.node()] = true
	}
}

// workPhase is the phase of a turn between the agent's messages.
func (c *chat) workPhase() string {
	if len(c.running) > 0 {
		return phaseExecuting
	}
	return phaseSending
}

// plan shows entries, which replace the previous plan.
func (c *chat) plan(entries []acp.PlanEntry, o *output) {
	for i, e := range entries {
		o.send(dialoguetui.MessageEvent{
			Type: dialoguetui.MessageEventTaskProgress,
			TaskProgress: dialoguetui.ProgressTaskEntry{
				ID: strconv.Itoa(i), Subject: e.Content, Status: string(e.Status),
			},
		})
	}
	for i := len(entries); i < c.planLen; i++ {
		o.send(dialoguetui.MessageEvent{
			Type:         dialoguetui.MessageEventTaskProgress,
			TaskProgress: dialoguetui.ProgressTaskEntry{ID: strconv.Itoa(i), Status: "deleted"},
		})
	}
	c.planLen = len(entries)
}

// subagentUpdate shows the subagent id as state now has it: announced,
// done with its work, or given more.
func (c *chat) subagentUpdate(id acp.SessionId, state acpapi.SessionState, o *output) {
	info := state.Subagents[id]
	sub := c.subagents[id]
	ran := len(c.running)
	if sub == nil {
		sub = &subagent{id: id}
		c.subagents[id] = sub
		c.showSubagent(sub, state, o)
	}
	st := info.State
	switch {
	case st == nil:
	case st.State == acpapi.SubagentIdle && sub.running:
		sub.running = false
		delete(c.running, sub.node)
		ev := sub.event(dialoguetui.MessageEventToolResult, info)
		if sub.parent == "" && ev.ToolOutput != "" {
			o.send(dialoguetui.MessageEvent{
				Type: dialoguetui.MessageEventChildResult, ParentToolCallID: sub.node,
				ToolOutput: ev.ToolOutput, IsError: ev.IsError,
			})
		}
		o.send(ev)
	case st.State == acpapi.SubagentRunning && !sub.running:
		sub.runs++
		sub.text.Reset()
		sub.task, sub.reply = nil, nil
		c.showSubagent(sub, state, o)
	}
	if state.Busy && len(c.running) != ran {
		phase := c.workPhase()
		o.set(func(s *dialoguetui.StatusBarState) { s.Phase = phase })
	}
}

// showSubagent shows sub's current run as a running agent tool call.
func (c *chat) showSubagent(sub *subagent, state acpapi.SessionState, o *output) {
	sub.node = "subagent:" + string(sub.id)
	if sub.runs > 0 {
		sub.node += ":" + strconv.Itoa(sub.runs+1)
	}
	sub.parent = ""
	if top := topSubagent(sub.id, state.Subagents); top != sub.id && c.subagents[top] != nil {
		sub.parent = c.subagents[top].node
	}
	if sub.parent == "" {
		c.running[sub.node] = true
	}
	sub.running = true
	o.send(sub.event(dialoguetui.MessageEventToolCall, state.Subagents[sub.id]))
}

// subagentTraffic shows a session/update the subagent id sent on its own
// session: its tool calls inside it, and its text as its report.
func (c *chat) subagentTraffic(id acp.SessionId, u acp.SessionUpdate, state acpapi.SessionState, o *output) {
	sub, top := c.subagents[id], c.subagents[topSubagent(id, state.Subagents)]
	if sub == nil || top == nil {
		return
	}
	switch {
	case u.ToolCall != nil || u.ToolCallUpdate != nil:
		c.updateToolCall(id, top.node, u, o)
	case u.AgentMessageChunk != nil:
		sub.text.WriteString(contentText(u.AgentMessageChunk.Content))
	}
}

// message records a message between sessions in the transcript of session
// on: a parent's message to its subagent is the subagent's task, and the
// subagent's message back is its reply.
func (c *chat) message(on acp.SessionId, m acpapi.SessionMessage, state acpapi.SessionState) {
	msg := c.sessionMessage(messageKey{on, m.MessageID})
	if m.Content.Set {
		msg.content = nil
		if v := m.Content.Value; v != nil {
			msg.content = *v
		}
	}
	if r := m.Recipient.Value; r != nil && state.Subagents[*r].Parent == on {
		if sub := c.subagents[*r]; sub != nil && sub.task == nil {
			sub.task = msg
		}
	}
	if s := m.Sender.Value; s != nil && state.Subagents[*s].Parent == on {
		if sub := c.subagents[*s]; sub != nil {
			sub.reply = msg
		}
	}
}

func (c *chat) messageChunk(on acp.SessionId, chunk acpapi.SessionMessageChunk) {
	msg := c.sessionMessage(messageKey{on, chunk.MessageID})
	msg.content = append(msg.content, chunk.Content)
}

func (c *chat) sessionMessage(key messageKey) *sessionMessage {
	msg := c.messages[key]
	if msg == nil {
		msg = &sessionMessage{}
		c.messages[key] = msg
	}
	return msg
}

// ask prompts for the oldest pending permission unless a prompt is up.
func (c *chat) ask(state acpapi.SessionState, o *output) {
	if c.asking != nil || len(state.Pending) == 0 {
		return
	}
	p := state.Pending[0]
	c.asking = &p.ID
	// Buffered, as the chat sends its answer under its lock.
	ch := make(chan []string, 1)
	go debug.CapturePanicReport(func() { c.answer(p, ch) })
	options := make([]dialoguetui.PromptEventOption, len(p.Request.Options))
	for i, opt := range p.Request.Options {
		options[i] = dialoguetui.PromptEventOption{Label: opt.Name}
	}
	tc := c.tools[toolKey{p.Session, p.Request.ToolCall.ToolCallId}]
	title := "Allow the agent to continue?"
	switch {
	case p.Request.ToolCall.Title != nil:
		title = *p.Request.ToolCall.Title
	case tc != nil && tc.title != "":
		title = tc.title
	}
	body := permissionBody(p.Request.ToolCall, tc)
	if p.Session != "" {
		by := "a subagent"
		if name := subagentTitle(state.Subagents[p.Session]); name != "" {
			by = "subagent " + name
		}
		body = strings.TrimSpace("Asked by " + by + ".\n\n" + body)
	}
	o.send(dialoguetui.MessageEvent{
		Type:          dialoguetui.MessageEventPrompt,
		PromptTitle:   title,
		PromptHeader:  "Permission",
		PromptBody:    body,
		PromptOptions: options,
		PromptResult:  ch,
	})
	o.set(func(s *dialoguetui.StatusBarState) { s.Phase = dialoguetui.AskingStatusText })
}

// dismiss takes down the prompt for a permission that no longer needs an
// answer.
func (c *chat) dismiss(o *output) {
	c.stateMu.Lock()
	c.resolved[*c.asking] = true
	c.stateMu.Unlock()
	c.asking = nil
	o.send(dialoguetui.MessageEvent{Type: dialoguetui.MessageEventPromptDismiss})
}

// answer responds to p with the option picked on its prompt. Dismissing
// the prompt rejects p, unless it was resolved elsewhere first.
func (c *chat) answer(p acpapi.Permission, ch <-chan []string) {
	var picked []string
	select {
	case picked = <-ch:
	case <-c.ctx.Done():
		return
	}
	c.stateMu.Lock()
	resolved := c.resolved[p.ID]
	delete(c.resolved, p.ID)
	c.stateMu.Unlock()
	if resolved {
		return
	}
	err := c.session.Respond(c.ctx, p.ID, permissionOutcome(p.Request.Options, picked))
	if err != nil && !errors.Is(err, acpapi.ErrResolved) && c.ctx.Err() == nil {
		c.showError("answer permission: " + acpstdio.ErrorText(err))
	}
}

// submits prompts the session with the messages the user sends.
func (c *chat) submits() {
	for {
		var msg dialoguetui.SubmitMessage
		select {
		case msg = <-c.rx:
		case <-c.ctx.Done():
			return
		}
		s, err := c.waitSession(c.ctx)
		if err != nil {
			// run shows why the session did not open.
			continue
		}
		if err := s.Prompt(c.ctx, c.promptContent(msg)); err != nil {
			if c.ctx.Err() != nil {
				return
			}
			c.mu.Lock()
			c.comp.AddErrorMessage(acpstdio.ErrorText(err))
			if c.comp.Draft().Empty() {
				c.comp.RestoreDraft(msg.Draft())
			}
			c.mu.Unlock()
			c.interrupt()
		}
	}
}

// promptContent is msg as ACP content: its text, then a resource link per
// attached file.
func (c *chat) promptContent(msg dialoguetui.SubmitMessage) []acp.ContentBlock {
	var blocks []acp.ContentBlock
	if msg.Text != "" {
		blocks = append(blocks, acp.TextBlock(msg.Text))
	}
	for _, a := range msg.Attachments {
		switch {
		case a.Content != "":
			blocks = append(blocks, acp.TextBlock(a.Content))
		case a.Path != "":
			p := a.Path
			if !path.IsAbs(p) {
				p = path.Join(c.cfg.Root, p)
			}
			name := a.Label
			if name == "" {
				name = path.Base(p)
			}
			blocks = append(blocks, acp.ResourceLinkBlock(name, (&url.URL{Scheme: "file", Path: p}).String()))
		}
	}
	return blocks
}

// configCommands are the chat's own commands; the rest go to the agent.
var configCommands = map[string]acp.SessionConfigOptionCategory{
	"model":  acp.SessionConfigOptionCategoryModel,
	"effort": acp.SessionConfigOptionCategoryThoughtLevel,
	"mode":   acp.SessionConfigOptionCategoryMode,
}

const closeCommand = "close"

// HandleCommand satisfies dialoguetui.CommandHandler.
func (c *chat) HandleCommand(
	ctx context.Context, name string, args []string,
) (dialoguetui.CommandResult, error) {
	if category, ok := configCommands[name]; ok {
		return c.setOption(ctx, name, category, args)
	}
	if name == closeCommand {
		s, err := c.waitSession(ctx)
		if err != nil {
			return dialoguetui.CommandResult{}, err
		}
		return dialoguetui.CommandResult{}, s.Close()
	}
	return dialoguetui.CommandResult{
		UserMessage: "/" + strings.Join(append([]string{name}, args...), " "),
	}, nil
}

// Complete satisfies dialoguetui.CommandHandler.
func (c *chat) Complete(
	_ context.Context, name string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) > 0 {
		return iterator.FromSlice[string](nil), nil
	}
	names := []string{closeCommand}
	for n := range configCommands {
		names = append(names, n)
	}
	for _, cmd := range c.sessionState().Commands {
		names = append(names, cmd.Name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	return iterator.FromSlice(slices.DeleteFunc(names, func(n string) bool {
		return !strings.HasPrefix(n, name)
	})), nil
}

// setOption sets the session's config option of category to the value
// args name, or lists its values when args is empty.
func (c *chat) setOption(
	ctx context.Context, cmd string, category acp.SessionConfigOptionCategory, args []string,
) (dialoguetui.CommandResult, error) {
	s, err := c.waitSession(ctx)
	if err != nil {
		return dialoguetui.CommandResult{}, err
	}
	opt := findOption(c.sessionState().ConfigOptions, category)
	if opt == nil {
		return dialoguetui.CommandResult{}, fmt.Errorf("%s does not offer a %s option", s.Info().Agent, cmd)
	}
	values := selectOptions(opt.Options)
	if len(args) == 0 {
		return dialoguetui.CommandResult{Display: c.display(optionList(opt, values))}, nil
	}
	want := strings.Join(args, " ")
	i := slices.IndexFunc(values, func(v acp.SessionConfigSelectOption) bool {
		return strings.EqualFold(string(v.Value), want) || strings.EqualFold(v.Name, want)
	})
	if i < 0 {
		return dialoguetui.CommandResult{}, fmt.Errorf("unknown %s %q, choose one of: %s",
			cmd, want, strings.Join(valueIDs(values), ", "))
	}
	err = s.SetConfigOption(ctx, acp.SetSessionConfigOptionRequest{
		ValueId: &acp.SetSessionConfigOptionValueId{ConfigId: opt.Id, Value: values[i].Value},
	})
	if err != nil {
		return dialoguetui.CommandResult{}, fmt.Errorf("set %s: %s", cmd, acpstdio.ErrorText(err))
	}
	return dialoguetui.CommandResult{}, nil
}

func (c *chat) display(text string) iterator.Iterator[component.Responsive] {
	return iterator.FromSlice([]component.Responsive{
		component.NewResponsiveString(text, component.StringResponsiveConfig{
			NoSplitWords: true,
			StringConfig: c.cfg.Component.ReceiveMessageStringConfig,
		}),
	})
}

// OpenToolCall satisfies dialoguetui.ToolCallOpener.
func (c *chat) OpenToolCall(details dialoguetui.ToolCallDetails) {
	cfg := dialoguetui.DefaultMarkdownConfig()
	cfg.Parser = c.cfg.Parser
	md, err := markdown.NewWithConfig(toolPreview(details), *cfg)
	if err != nil {
		c.showError("preview " + details.Name + ": " + err.Error())
		return
	}
	mdh := mdhandler.New(md)
	padded := handler.NewSpan(mdh, component.SpanConfig{
		PadHorizontal:    2,
		ContentAlignment: component.AlignmentHorizontallyCentered,
	})
	floating := browserapi.FuncFloating(browserapi.FuncHandler(padded, mdh.Close), func() (int, int) {
		return previewWidth, min(padded.Height(previewWidth), previewHeight)
	})
	_, err = c.cfg.WindowManager.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
		Title:     details.Summary,
	})
	if err != nil {
		c.showError("preview " + details.Name + ": " + err.Error())
	}
}

// toolKey identifies a tool call: tool call IDs are only unique within
// the session that made the call, which is "" for the chat's own.
type toolKey struct {
	session acp.SessionId
	id      acp.ToolCallId
}

// toolCall is what the chat knows of a tool call.
type toolCall struct {
	key toolKey
	// parent is the tool call it shows in, or "" at the top level.
	parent string
	kind   acp.ToolKind
	title  string
	// name and summary are what the agent said in _meta that its tool
	// is called and does, if it said so.
	name    string
	summary string
	status  acp.ToolCallStatus
	input   any
	output  any
	content []acp.ToolCallContent
	done    bool
}

// event shows the call as Rune Agent's chat does, by its tool's name and
// summary, or its arguments when it has no summary. ACP has no tool names,
// so a call whose agent says none is named by its kind instead, followed by
// its title.
func (tc *toolCall) event(typ dialoguetui.MessageEventType) dialoguetui.MessageEvent {
	name, summary := tc.name, tc.summary
	if name == "" {
		name, summary = cmp.Or(string(tc.kind), "tool"), tc.title
	}
	ev := dialoguetui.MessageEvent{
		Type:             typ,
		ToolCallID:       tc.node(),
		ParentToolCallID: tc.parent,
		ToolName:         name,
		ToolSummary:      summary,
		ToolArgs:         jsonText(tc.input),
	}
	if typ == dialoguetui.MessageEventToolResult {
		ev.ToolOutput = toolOutput(tc.content, tc.output)
		ev.IsError = tc.status == acp.ToolCallStatusFailed
	}
	return ev
}

func (tc *toolCall) node() string {
	if tc.key.session == "" {
		return string(tc.key.id)
	}
	return string(tc.key.session) + "/" + string(tc.key.id)
}

// metaKeys are the _meta keys agents name their tool calls under, as
// {"toolName": name, "toolSummary": summary}, most specific first. Claude
// Code's names the tool alone.
var metaKeys = []string{"runeAgent", "claudeCode"}

// metaTool returns the name and summary meta, a tool call's _meta, gives
// its tool. The name is empty when meta names none.
func metaTool(meta map[string]any) (name, summary string) {
	for _, key := range metaKeys {
		m, _ := meta[key].(map[string]any)
		if name, _ := m["toolName"].(string); name != "" {
			summary, _ := m["toolSummary"].(string)
			return name, summary
		}
	}
	return "", ""
}

// messageKey identifies a message between sessions in the transcript of
// one of them.
type messageKey struct {
	session acp.SessionId
	id      string
}

type sessionMessage struct {
	content []acp.ContentBlock
}

func (m *sessionMessage) text() string {
	var b strings.Builder
	for _, c := range m.content {
		b.WriteString(contentText(c))
	}
	return b.String()
}

// subagent is what the chat knows of a subagent session, which it shows
// as an agent tool call per run.
type subagent struct {
	id acp.SessionId
	// node is the tool call showing the current run, and parent the tool
	// call it shows in, or "" at the top level.
	node, parent string
	running      bool
	// runs counts the runs before the current one.
	runs int
	// text is what the subagent said during the current run.
	text strings.Builder
	// task is the message its parent sent it this run, and reply the one
	// it sent back.
	task, reply *sessionMessage
}

func (s *subagent) event(typ dialoguetui.MessageEventType, info acpapi.Subagent) dialoguetui.MessageEvent {
	var args struct {
		Description string `json:"description,omitempty"`
		Prompt      string `json:"prompt,omitempty"`
	}
	args.Description = info.Description
	if s.task != nil {
		args.Prompt = s.task.text()
	}
	ev := dialoguetui.MessageEvent{
		Type:             typ,
		ToolCallID:       s.node,
		ParentToolCallID: s.parent,
		ToolName:         "agent",
		ToolSummary:      subagentTitle(info),
	}
	if args.Description != "" || args.Prompt != "" {
		ev.ToolArgs = jsonText(args)
	}
	if typ != dialoguetui.MessageEventToolResult {
		return ev
	}
	ev.ToolOutput = strings.TrimSpace(s.text.String())
	if s.reply != nil {
		ev.ToolOutput = strings.TrimSpace(s.reply.text())
	}
	if st := info.State; st != nil && st.StopReason == acpapi.StopReasonError {
		ev.IsError = true
		if st.Error != nil && st.Error.Message != "" {
			ev.ToolOutput = st.Error.Message
		}
	}
	return ev
}

// topSubagent is the subagent of the chat's own session that id descends
// from, or id itself when its parent is unknown.
func topSubagent(id acp.SessionId, subs map[acp.SessionId]acpapi.Subagent) acp.SessionId {
	// Bounded, as a cycle of parents would otherwise never end.
	for range len(subs) {
		parent := subs[id].Parent
		if _, ok := subs[parent]; !ok {
			break
		}
		id = parent
	}
	return id
}

// subagentTitle names a subagent by its title, or else its description.
func subagentTitle(info acpapi.Subagent) string {
	if info.Title != "" {
		return info.Title
	}
	return info.Description
}

// toolOutput renders a tool call's content, or its raw output when it has
// none.
func toolOutput(content []acp.ToolCallContent, raw any) string {
	var parts []string
	for _, c := range content {
		switch {
		case c.Content != nil:
			parts = append(parts, contentText(c.Content.Content))
		case c.Diff != nil:
			old := ""
			if c.Diff.OldText != nil {
				old = *c.Diff.OldText
			}
			parts = append(parts, unifiedDiff(c.Diff.Path, old, c.Diff.NewText))
		case c.Terminal != nil:
			parts = append(parts, "terminal "+c.Terminal.TerminalId)
		}
	}
	if len(parts) == 0 {
		return jsonText(raw)
	}
	return strings.Join(parts, "\n")
}

// permissionBody shows what a permission is for: the tool call's content,
// or its input when it has none.
func permissionBody(u acp.ToolCallUpdate, tc *toolCall) string {
	content, input := u.Content, u.RawInput
	if tc != nil {
		if content == nil {
			content = tc.content
		}
		if input == nil {
			input = tc.input
		}
	}
	var parts []string
	for _, c := range content {
		switch {
		case c.Content != nil:
			parts = append(parts, contentText(c.Content.Content))
		case c.Diff != nil:
			old := ""
			if c.Diff.OldText != nil {
				old = *c.Diff.OldText
			}
			parts = append(parts, fence("diff", unifiedDiff(c.Diff.Path, old, c.Diff.NewText)))
		}
	}
	if len(parts) == 0 && input != nil {
		parts = append(parts, fence("json", indentJSON(jsonText(input))))
	}
	return strings.Join(parts, "\n\n")
}

// permissionOutcome is the answer to a permission prompt on which picked
// was chosen. Nothing picked rejects once, or cancels when the agent
// offered no way to reject.
func permissionOutcome(options []acp.PermissionOption, picked []string) acp.RequestPermissionOutcome {
	match := func(ok func(acp.PermissionOption) bool) (acp.RequestPermissionOutcome, bool) {
		if i := slices.IndexFunc(options, ok); i >= 0 {
			return acp.NewRequestPermissionOutcomeSelected(options[i].OptionId), true
		}
		return acp.RequestPermissionOutcome{}, false
	}
	if len(picked) > 0 {
		if o, ok := match(func(o acp.PermissionOption) bool { return o.Name == picked[0] }); ok {
			return o
		}
	}
	if o, ok := match(func(o acp.PermissionOption) bool {
		return o.Kind == acp.PermissionOptionKindRejectOnce
	}); ok {
		return o
	}
	return acp.NewRequestPermissionOutcomeCancelled()
}

func stopWarning(r acp.StopReason) string {
	switch r {
	case acp.StopReasonMaxTokens:
		return "The agent ran out of tokens for this turn."
	case acp.StopReasonMaxTurnRequests:
		return "The agent reached its limit of requests for this turn."
	case acp.StopReasonRefusal:
		return "The agent refused to continue."
	case acp.StopReasonCancelled:
		return "Cancelled."
	}
	return ""
}

// userMessage shows content as the user's message, with file links as
// attachments.
func userMessage(content []acp.ContentBlock) dialoguetui.MessageEvent {
	var text strings.Builder
	var atts []dialoguetui.Attachment
	for _, b := range content {
		if l := b.ResourceLink; l != nil {
			if u, err := url.Parse(l.Uri); err == nil && u.Scheme == "file" {
				atts = append(atts, dialoguetui.NewAttachment(u.Path))
				continue
			}
		}
		text.WriteString(contentText(b))
	}
	return dialoguetui.MessageEvent{
		Type: dialoguetui.MessageEventUserMessage, Text: text.String(), Attachments: atts,
	}
}

// contentText renders b as markdown.
func contentText(b acp.ContentBlock) string {
	switch {
	case b.Text != nil:
		return b.Text.Text
	case b.ResourceLink != nil:
		return fmt.Sprintf("[%s](%s)", b.ResourceLink.Name, b.ResourceLink.Uri)
	case b.Resource != nil:
		if t := b.Resource.Resource.TextResourceContents; t != nil {
			return "<" + t.Uri + ">"
		}
		if bl := b.Resource.Resource.BlobResourceContents; bl != nil {
			return "<" + bl.Uri + ">"
		}
	case b.Image != nil:
		return "[image]"
	case b.Audio != nil:
		return "[audio]"
	}
	return ""
}

// configStatus reports the current model and effort.
func configStatus(options []acp.SessionConfigOption) func(*dialoguetui.StatusBarState) {
	model := currentValue(findOption(options, acp.SessionConfigOptionCategoryModel))
	effort := currentValue(findOption(options, acp.SessionConfigOptionCategoryThoughtLevel))
	return func(s *dialoguetui.StatusBarState) { s.Model, s.Effort = model, effort }
}

func findOption(
	options []acp.SessionConfigOption, category acp.SessionConfigOptionCategory,
) *acp.SessionConfigOptionSelect {
	for _, o := range options {
		if o.Select != nil && o.Select.Category != nil && *o.Select.Category == category {
			return o.Select
		}
	}
	return nil
}

// currentValue names opt's current value.
func currentValue(opt *acp.SessionConfigOptionSelect) string {
	if opt == nil {
		return ""
	}
	for _, v := range selectOptions(opt.Options) {
		if v.Value == opt.CurrentValue {
			return v.Name
		}
	}
	return string(opt.CurrentValue)
}

func selectOptions(o acp.SessionConfigSelectOptions) []acp.SessionConfigSelectOption {
	switch {
	case o.Ungrouped != nil:
		return *o.Ungrouped
	case o.Grouped != nil:
		var out []acp.SessionConfigSelectOption
		for _, g := range *o.Grouped {
			out = append(out, g.Options...)
		}
		return out
	}
	return nil
}

func valueIDs(values []acp.SessionConfigSelectOption) []string {
	ids := make([]string, len(values))
	for i, v := range values {
		ids[i] = string(v.Value)
	}
	return ids
}

func optionList(opt *acp.SessionConfigOptionSelect, values []acp.SessionConfigSelectOption) string {
	var b strings.Builder
	b.WriteString(opt.Name)
	for _, v := range values {
		mark := "  "
		if v.Value == opt.CurrentValue {
			mark = "* "
		}
		fmt.Fprintf(&b, "\n%s%s", mark, v.Value)
		if v.Name != "" && v.Name != string(v.Value) {
			fmt.Fprintf(&b, " (%s)", v.Name)
		}
	}
	return b.String()
}

// jsonText is v as compact JSON, or v itself when it is a string.
func jsonText(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

func indentJSON(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	var v any
	if json.Unmarshal([]byte(s), &v) != nil || enc.Encode(v) != nil {
		return s
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// unifiedDiff renders the change from oldText to newText in path with
// diffContext lines of context around each change.
func unifiedDiff(path, oldText, newText string) string {
	dmp := diffmatchpatch.New()
	a, b, lines := dmp.DiffLinesToChars(oldText, newText)
	diffs := dmp.DiffCharsToLines(dmp.DiffMain(a, b, false), lines)
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s\n+++ %s\n", path, path)
	for i, d := range diffs {
		text := strings.Split(strings.TrimSuffix(d.Text, "\n"), "\n")
		switch d.Type {
		case diffmatchpatch.DiffDelete:
			writeLines(&out, "-", text)
		case diffmatchpatch.DiffInsert:
			writeLines(&out, "+", text)
		default:
			head, tail := diffContext, diffContext
			if i == 0 {
				head = 0
			}
			if i == len(diffs)-1 {
				tail = 0
			}
			if len(text) <= head+tail {
				writeLines(&out, " ", text)
				continue
			}
			writeLines(&out, " ", text[:head])
			out.WriteString("@@\n")
			writeLines(&out, " ", text[len(text)-tail:])
		}
	}
	return strings.TrimSuffix(out.String(), "\n")
}

func writeLines(b *strings.Builder, prefix string, lines []string) {
	for _, l := range lines {
		fmt.Fprintf(b, "%s%s\n", prefix, l)
	}
}

// toolPreview is the markdown a tool call's preview shows.
func toolPreview(d dialoguetui.ToolCallDetails) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", d.Name)
	if d.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", d.Summary)
	}
	if d.Args != "" {
		fmt.Fprintf(&b, "## Input\n\n%s\n\n", fence("json", indentJSON(d.Args)))
	}
	switch {
	case !d.Done:
		b.WriteString("Running…\n")
	case d.Output == "":
	case d.IsError:
		fmt.Fprintf(&b, "## Error\n\n%s\n", fence("", d.Output))
	default:
		fmt.Fprintf(&b, "## Output\n\n%s\n", fence("", d.Output))
	}
	return b.String()
}

// fence wraps s in a code fence longer than any backtick run in s.
func fence(lang, s string) string {
	n := 3
	for run, i := 0, 0; i < len(s); i++ {
		if s[i] != '`' {
			run = 0
			continue
		}
		run++
		n = max(n, run+1)
	}
	f := strings.Repeat("`", n)
	return f + lang + "\n" + s + "\n" + f
}
