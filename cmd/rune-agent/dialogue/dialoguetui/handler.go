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

package dialoguetui

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/search"
	tterm "unstable.build/rune/internal/term"
)

// SubmitMessage is a user message sent through the tx channel.
type SubmitMessage struct {
	Text      string
	SkillName string // non-empty when the message was triggered by a slash-command skill
	// Attachments are the files pending in the chat when the message
	// was submitted. They are sent alongside Text as content parts.
	Attachments []Attachment
	// Links are the ranges of Text that still referred to an
	// attachment when the message was submitted.
	Links []InlineAttachmentLink
}

// Draft returns the compose state this message was submitted from.
func (m SubmitMessage) Draft() Draft {
	return Draft{Text: m.Text, Attachments: m.Attachments, Links: m.Links}
}

// CommandResult is the outcome of a handled /command.
type CommandResult struct {
	// Display is the output to render in the chat. Nil means no display output.
	Display iterator.Iterator[component.Responsive]
	// UserMessage, when non-empty, is sent through the tx channel as a user
	// message to the LLM instead of being displayed.
	UserMessage string
	// SkillName, when non-empty, identifies the skill that generated UserMessage.
	SkillName string
	// Exit, when true, signals the handler to close the chat.
	Exit bool
	// Phase, when non-empty, is the status the bar reports while Display
	// drains. Commands that block on an LLM call need it: they run off
	// the turn loop, so nothing else would move the bar off IDLE.
	Phase string
}

// CommandHandler handles /commands typed in the chat input.
// Any repl.CommandHandler can be adapted to this interface.
type CommandHandler interface {
	HandleCommand(ctx context.Context, name string, args []string) (CommandResult, error)
	Complete(ctx context.Context, name string, args []string) (iterator.Iterator[string], error)
}

// HandlerOption configures optional behavior of Handler.
type HandlerOption func(*dialogueHandler)

// WithCommands sets a CommandHandler for intercepting /commands.
func WithCommands(h CommandHandler) HandlerOption {
	return func(dh *dialogueHandler) { dh.commands = h }
}

// AttachmentOpener previews an attachment the user clicked in a sent
// message's chip grid.
type AttachmentOpener interface {
	OpenAttachment(a Attachment)
}

// WithAttachmentOpener makes the attachment chips above sent messages
// clickable.
func WithAttachmentOpener(o AttachmentOpener) HandlerOption {
	return func(dh *dialogueHandler) { dh.attachmentOpener = o }
}

// WithCloseFunc sets a function called when a command returns
// CommandResult.Exit == true.
func WithCloseFunc(fn func()) HandlerOption {
	return func(dh *dialogueHandler) { dh.closeFn = fn }
}

// Handler wraps a dialogue.Component and provides a simple-to-use
// tui.Handler which sends input messages via rx and can
// receive messages into the dialogue history via tx.
//
// Closing the tx channel effecively closes the returned handler.
//
// locker is used to synchronize access to c.
func Handler(
	ctx context.Context,
	locker sync.Locker, c *Component,
	interrupter term.Interrupter,
	opts ...HandlerOption,
) (h tui.Handler, tx chan<- MessageEvent, rx <-chan SubmitMessage) {
	c.interrupter = interrupter
	c.mu = locker
	if c.statusBar != nil {
		c.statusBar.startTicker(interrupter)
	}
	ch1 := make(chan SubmitMessage)
	ch2 := make(chan MessageEvent)
	sh := &dialogueHandler{
		interrupter:  interrupter,
		comp:         c,
		rx:           ch2,
		tx:           ch1,
		mu:           locker,
		ctx:          ctx,
		inputFocused: true,
	}
	for _, o := range opts {
		o(sh)
	}
	sh.mouseDelegate = newMouseDelegate(&sh.grid, &c.messages)
	mouse := mouse.New(sh.mouseDelegate)
	sh.mouse = mouse
	go debug.CapturePanicReport(sh.consumeIncoming)

	return sh, ch2, ch1
}

type dialogueHandler struct {
	grid          tterm.SelectionWriter
	mouseDelegate *mouseDelegate
	comp          *Component
	interrupter   term.Interrupter
	mouse         *mouse.Mouse
	tx            chan SubmitMessage // user messages
	rx            chan MessageEvent  // assistant messages
	mu            sync.Locker
	commands      CommandHandler
	completer     ContextCompleter
	// attachmentOpener previews attachments clicked in the transcript.
	attachmentOpener AttachmentOpener
	closeFn          func()
	ctx              context.Context
	inputFocused     bool // true when last mouse interaction was in the input area
	// messagesDragging is true while a left-button selection drag that
	// started in the messages area is in progress. While set, mouse events
	// keep routing to the messages selection even when the pointer crosses
	// into the input area, so the drag keeps selecting and auto-scrolls
	// instead of handing focus to the input mid-drag.
	messagesDragging bool

	// busy is true while an agent completion is active. When busy,
	// follow-up user messages are queued instead of being sent directly.
	busy bool

	// queue holds follow-up messages submitted while the agent is busy.
	// Messages are injected FIFO when the handler becomes idle.
	queue []SubmitMessage

	// history holds previously submitted drafts (sent, queued, and
	// command lines), oldest first. ArrowUp/ArrowDown walk it to recall
	// earlier input once the compose editor's cursor is at the top/bottom
	// edge and the queue is exhausted.
	history []Draft
	// historyIdx is the cursor into history for recall. It equals
	// len(history) when not actively browsing; ArrowUp decrements it and
	// ArrowDown increments it.
	historyIdx int
	// scratch is the draft that was being composed when history
	// browsing started, restored when the user walks back past the
	// newest entry.
	scratch Draft

	// pasting is true between EventPasteStart and EventPasteEnd. While
	// set, key events accumulate in pasteBuf instead of reaching the
	// compose input, so a paste that turns out to be a list of file
	// paths becomes attachments instead of text.
	pasting  bool
	pasteBuf []term.Event

	// compList is the '#' context completion band, non-nil only while
	// the band is open. compQuery mirrors the characters typed after the
	// '#' into the list's filter; the compose box keeps showing them.
	compList              *search.List
	compQuery             []rune
	compCancel            context.CancelFunc
	compDone              chan struct{}
	compLoadingGeneration atomic.Uint64
	// compSyncSearch makes completion filtering synchronous. Testing only.
	compSyncSearch bool
}

func (h *dialogueHandler) publishInterrupt(ctx context.Context) {
	err := h.interrupter.Interrupt(ctx)
	if err != nil {
		slog.Error("publish interrupt",
			"struct", "dialogue.handler",
			"error", err,
		)
	}
}

func (s *dialogueHandler) Draw(w term.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.grid.Clear()
	s.grid.SetContext(w.Context())
	s.comp.Draw(&s.grid)
	s.mouseDelegate.offset = s.comp.MessagesPosition()

	var sel *tterm.SelRange
	if win, ok := s.mouseDelegate.WindowSelection(); ok {
		sel = &tterm.SelRange{
			Start:  term.CoordinatesSum(win.Start, s.mouseDelegate.offset),
			End:    term.CoordinatesSum(win.End, s.mouseDelegate.offset),
			Active: true,
		}
	}
	s.grid.Dump(w, sel)
}

func (s *dialogueHandler) Resize(width, height int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.grid.Resize(width, height)
	s.comp.Resize(width, height)
}

func (s *dialogueHandler) Handle(ev term.Event) (exit, handled bool) {
	switch ev.Mod {
	case 0, term.ModCtrl, term.ModAlt, term.ModShift, term.ModCtrlAlt:
	default:
		return
	}
	if ev.Type == term.EventMouse {
		// The mouse delegate reads and seeks the messages list, which the
		// consumeIncoming goroutine mutates under s.mu. Hold the lock so
		// selection/scroll stays consistent with concurrent streamed
		// content updates.
		s.mu.Lock()
		defer s.mu.Unlock()
		pos := s.comp.InputPosition()
		// A held drag that began in the messages area keeps selecting even
		// over the input box: route it (and its terminating release) to the
		// mouse handler so selection continues and the bottom edge
		// auto-scrolls instead of the input stealing focus mid-drag.
		dragging := s.messagesDragging
		if ev.Key == term.MouseRelease {
			s.messagesDragging = false
		}
		if apos, ok := s.comp.AttachmentsPosition(); ok &&
			ev.MouseY == apos.Y && !dragging {
			if ev.Key == term.MouseLeft {
				idx, found := s.comp.AttachmentRemoveAt(
					term.Coordinates{X: ev.MouseX - apos.X})
				if found {
					s.comp.RemoveAttachment(idx)
				}
			}
			return false, true
		}
		if ev.MouseY >= pos.Y && !dragging {
			if !s.inputFocused {
				s.inputFocused = true
				s.mouseDelegate.ClearSelection()
			}
			ev.MouseY -= pos.Y
			ev.MouseX -= pos.X
			return s.comp.box.Handle(ev)
		}
		if s.inputFocused {
			s.inputFocused = false
		}
		if exit, handled := s.handleAttachmentChip(ev, dragging); handled {
			return exit, true
		}
		if ev.Key == term.MouseLeft {
			s.messagesDragging = true
		}
		pos = s.comp.MessagesPosition()
		ev.MouseY -= pos.Y
		ev.MouseX -= pos.X
		return s.mouse.Handle(ev)
	}

	if ev.Type != term.EventKey &&
		ev.Type != term.EventPasteStart && ev.Type != term.EventPasteEnd {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.handlePaste(ev) {
		return false, true
	}
	if ev.Type != term.EventKey {
		return
	}

	if s.comp.PromptInputMode() {
		// Text input mode: route keys to the prompt inputbox.
		handled = true
		switch ev.Key {
		case term.KeyEnter:
			ch, label, text := s.comp.PreparePromptInputSubmit()
			s.mu.Unlock()
			if ch != nil {
				ch <- []string{label, text}
			}
			s.mu.Lock()
		default:
			if ev.Mod == term.ModCtrl && ev.Ch == 'c' {
				// Ctrl-C dismisses the entire prompt while in text input mode.
				ch := s.comp.PreparePromptDismiss()
				s.mu.Unlock()
				if ch != nil {
					ch <- nil
				}
				s.mu.Lock()
				return
			}
			if ib := s.comp.PromptInput(); ib != nil {
				ib.Handle(ev)
			}
		}
		return
	} else if s.comp.HasActivePrompt() {
		handled = true
		switch ev.Key {
		case term.KeyArrowUp:
			s.comp.PromptMoveUp()
		case term.KeyArrowDown:
			s.comp.PromptMoveDown()
		case term.KeyEnter:
			// If the selected option requires input, transition to text input mode.
			if s.comp.StartPromptInput() {
				// Switched to input mode; wait for user to type.
			} else {
				ch, vals := s.comp.PreparePromptSelect()
				s.mu.Unlock()
				if ch != nil {
					ch <- vals
				}
				s.mu.Lock()
			}
		case term.KeyEsc:
			ch := s.comp.PreparePromptDismiss()
			s.mu.Unlock()
			if ch != nil {
				ch <- nil
			}
			s.mu.Lock()
		default:
			if ev.Mod == term.ModCtrl {
				switch ev.Ch {
				case 'k', 'p':
					s.comp.PromptMoveUp()
					return
				case 'j', 'n':
					s.comp.PromptMoveDown()
					return
				case 'c':
					// Ctrl-C dismisses the active selection prompt.
					ch := s.comp.PreparePromptDismiss()
					s.mu.Unlock()
					if ch != nil {
						ch <- nil
					}
					s.mu.Lock()
					return
				}
			}
			// The space bar arrives as Key=KeySpace; Ch=' ' is also
			// accepted for synthetic and legacy events.
			if ev.Key == term.KeySpace || ev.Ch == ' ' {
				s.comp.PromptToggle()
			}
			// absorb all other keys
		}
		return
	} else if s.comp.HasFreeInputPrompt() {
		// Free-form prompt: the user types into the main inputbox.
		// Enter submits the text; Esc dismisses.
		switch ev.Key {
		case term.KeyEnter:
			ch, text := s.comp.PrepareFreeInputSubmit()
			if ch != nil {
				handled = true
				s.mu.Unlock()
				ch <- []string{text}
				s.mu.Lock()
			}
			// If text was empty, do nothing (don't submit empty).
		case term.KeyEsc:
			handled = true
			ch := s.comp.PreparePromptDismiss()
			s.mu.Unlock()
			if ch != nil {
				ch <- nil
			}
			s.mu.Lock()
		default:
			if ev.Mod == term.ModCtrl && ev.Ch == 'c' {
				// Ctrl-C dismisses the active free-form prompt without
				// submitting any typed text.
				handled = true
				ch := s.comp.PreparePromptDismiss()
				s.mu.Unlock()
				if ch != nil {
					ch <- nil
				}
				s.mu.Lock()
				return
			}
			// Route all other keys to the main inputbox.
			_, handled = s.comp.Input().Handle(ev)
		}
		return
	}

	if s.compList != nil && s.handleCompletionKey(ev) {
		return false, true
	}
	if s.completer != nil && s.compList == nil &&
		ev.Ch == '#' && ev.Mod == 0 && s.atWordBoundary() {
		s.comp.Input().Handle(ev)
		s.openCompletion()
		return false, true
	}

	switch {
	case ev.Key == term.KeyEnter && (ev.Mod == 0 || ev.Mod == term.ModShift):
		// The compose input decides whether this Enter submits or just
		// inserts a newline. When it inserts, the event is already
		// consumed by the input, so we must not submit or re-forward it.
		handled = true
		if !s.comp.Input().EnterSubmits(ev) {
			return
		}
		text := s.comp.Input().Text()
		if s.commands != nil && isCommand(text) {
			item, ok := s.comp.InputSubmit()
			if ok {
				s.appendHistory(Draft{Text: item})
				name, args := parseCommand(item)
				go debug.CapturePanicReport(func() {
					s.executeCommand(name, args)
				})
			}
			return
		}
		if s.comp.Draft().Empty() {
			return
		}
		d := s.comp.TakeDraft()
		s.submitMessage(SubmitMessage{
			Text:        d.Text,
			Attachments: d.Attachments,
			Links:       d.Links,
		}, d.Text, true)
		return
	case ev.Mod == term.ModCtrl && ev.Ch == 'c':
		// Ctrl-C clears the compose input. The editor backend consumes
		// Ctrl-C itself, so this must run before the event is forwarded
		// to the input handler below.
		handled = true
		s.comp.Input().Clear()
		return
	}

	if !handled {
		_, handled = s.comp.Input().Handle(ev)
	}

	if handled {
		return
	}

	// Up/down navigation is delegated to the compose editor first; only
	// when it leaves the event unhandled (cursor at the top/bottom edge)
	// do we recall queued/history messages, then fall back to scrolling
	// the messages view.
	switch ev.Mod {
	case 0:
		switch ev.Key {
		case term.KeyArrowDown:
			if s.recallDown() {
				handled = true
				return
			}
			handled = s.comp.SeekDown()
			return
		case term.KeyArrowUp:
			if s.recallUp() {
				handled = true
				return
			}
			handled = s.comp.SeekUp()
			return
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'k', 'p':
			if s.recallUp() {
				handled = true
				return
			}
			handled = s.comp.SeekUp()
		case 'j', 'n':
			if s.recallDown() {
				handled = true
				return
			}
			handled = s.comp.SeekDown()
		case 'o':
			handled = true
			s.comp.ToggleContracted()
		}
	}

	return
}

func (s *dialogueHandler) Selection() (string, bool) {
	if s.inputFocused {
		return s.comp.Input().Selection()
	}
	return s.mouseDelegate.Selection()
}

// handlePaste buffers a bracketed paste so it can be classified as file
// paths or ordinary text once complete. It reports whether the event was
// consumed. Pastes into an active prompt are left alone: the prompt owns
// every keystroke while it is up.
func (s *dialogueHandler) handlePaste(ev term.Event) bool {
	if s.comp.PromptInputMode() || s.comp.HasActivePrompt() ||
		s.comp.HasFreeInputPrompt() {
		s.pasting = false
		s.pasteBuf = s.pasteBuf[:0]
		return false
	}
	switch ev.Type {
	case term.EventPasteStart:
		s.pasting = true
		s.pasteBuf = s.pasteBuf[:0]
		return true
	case term.EventPasteEnd:
		if !s.pasting {
			return false
		}
		s.pasting = false
		s.finishPaste()
		s.pasteBuf = s.pasteBuf[:0]
		return true
	}
	if !s.pasting {
		return false
	}
	// Editors ignore key events without a rune while pasting; mirror
	// that so the replayed sequence matches what the input would have
	// received.
	if ev.Ch != 0 {
		s.pasteBuf = append(s.pasteBuf, ev)
	}
	return true
}

// finishPaste turns the buffered paste into attachments when every line
// names an existing file, and otherwise replays it into the compose
// input verbatim.
func (s *dialogueHandler) finishPaste() {
	var sb strings.Builder
	for _, ev := range s.pasteBuf {
		sb.WriteRune(ev.Ch)
	}
	if paths, ok := pastedFilePaths(sb.String()); ok {
		for _, p := range paths {
			s.comp.AddAttachment(NewAttachment(p))
		}
		return
	}
	for _, ev := range s.pasteBuf {
		s.comp.Input().Handle(ev)
	}
}

func (s *dialogueHandler) Cursor() (
	cursor term.Coordinates, style term.CursorStyle, ok bool,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.comp.PromptInputMode() {
		// Show cursor inside the prompt input box.
		cursor, style, ok = s.comp.PromptInputCursor()
		if !ok {
			return
		}
		cursor = term.CoordinatesSum(cursor, s.comp.MessagesPosition())
		return
	} else if s.comp.HasActivePrompt() {
		return cursor, style, false
	}
	cursor, style, ok = s.comp.Cursor()
	if !ok {
		return
	}
	cursor = term.CoordinatesSum(cursor, s.comp.InputPosition())
	return
}

func (s *dialogueHandler) consumeIncoming() {
	ctx := context.Background()
	for ev := range s.rx {
		s.mu.Lock()
		switch ev.Type {
		case MessageEventReasoning:
			s.comp.AddReasoningChunk(ev.Text)
		case MessageEventText:
			s.comp.AddReceiveMessageChunk(ev.Text)
		case MessageEventBreak:
			s.comp.AddReceiveMessageBreak()
		case MessageEventToolCall:
			if IsTaskTool(ev.ToolName) {
				break
			}
			if ev.ParentToolCallID != "" {
				s.comp.AddChildToolCall(ev.ParentToolCallID, ev.ToolCallID, ev.ToolName, ev.ToolArgs, ev.ToolSummary)
			} else {
				s.comp.AddToolCall(ev.ToolCallID, ev.ToolName, ev.ToolArgs, ev.ToolSummary)
			}
			if !ev.ToolStartTime.IsZero() {
				s.comp.SetToolStartTime(ev.ToolCallID, ev.ToolStartTime)
			}
		case MessageEventToolResult:
			if IsTaskTool(ev.ToolName) {
				break
			}
			if ev.ToolDuration > 0 {
				s.comp.SetToolDuration(ev.ToolCallID, ev.ToolDuration)
			}
			if ev.ParentToolCallID != "" {
				s.comp.CompleteChildToolCall(ev.ParentToolCallID, ev.ToolCallID, ev.ToolName, ev.ToolArgs, ev.ToolSummary, ev.ToolOutput, ev.IsError)
			} else {
				s.comp.CompleteToolCall(ev.ToolCallID, ev.ToolName, ev.ToolArgs, ev.ToolSummary, ev.ToolOutput, ev.IsError)
			}
		case MessageEventChildResult:
			s.comp.AddChildResult(ev.ParentToolCallID, ev.ToolOutput, ev.IsError)
		case MessageEventError:
			s.comp.AddErrorMessage(ev.Text)
		case MessageEventWarning:
			s.comp.AddWarningMessage(ev.Text)
		case MessageEventToolsDropped:
			s.comp.MarkToolsDropped(ev.DroppedToolCallIDs)
		case MessageEventMemoryRecall:
			s.comp.AddMemoryRecall(ev.Memories, ev.MemoryDuration)
		case MessageEventPrompt:
			oldCh := s.comp.AddPrompt(ev.PromptTitle, ev.PromptHeader, ev.PromptBody,
				ev.PromptOptions, ev.PromptMultiSelect, ev.PromptResult)
			if oldCh != nil {
				s.mu.Unlock()
				oldCh <- nil
				s.mu.Lock()
			}
		case MessageEventTaskProgress:
			s.comp.UpdateTaskProgress(ev.TaskProgress)
		case MessageEventAttachment:
			s.comp.AddAttachment(ev.Attachment)
		case MessageEventPromptDismiss:
			ch := s.comp.PreparePromptDismiss()
			if ch != nil {
				s.mu.Unlock()
				ch <- nil
				s.mu.Lock()
			}
		case MessageEventBusy:
			s.busy = ev.Busy
			if !s.busy {
				s.drainQueue()
			}
		case MessageEventCommand:
			name, args := ev.CommandName, ev.CommandArgs
			go debug.CapturePanicReport(func() {
				s.executeCommand(name, args)
			})
		}
		s.mu.Unlock()
		s.publishInterrupt(ctx)
	}
}

func isCommand(text string) bool {
	return len(text) >= 2 && text[0] == '/' && text[1] != ' '
}

func parseCommand(text string) (string, []string) {
	parts := strings.Fields(text[1:])
	if len(parts) == 0 {
		return "", nil
	}
	return parts[0], parts[1:]
}

func (s *dialogueHandler) executeCommand(name string, args []string) {
	if s.commands == nil {
		return
	}
	result, err := s.commands.HandleCommand(s.ctx, name, args)
	defer s.publishInterrupt(s.ctx)
	if err != nil {
		s.mu.Lock()
		s.comp.AddErrorMessage(err.Error())
		s.mu.Unlock()
		return
	}
	if result.Exit {
		if s.closeFn != nil {
			s.closeFn()
		}
		return
	}
	if result.UserMessage != "" {
		s.mu.Lock()
		s.submitMessage(
			SubmitMessage{Text: result.UserMessage, SkillName: result.SkillName},
			"/"+name,
			false,
		)
		s.mu.Unlock()
		return
	}
	if result.Display != nil {
		s.comp.AddCommand(s.ctx, result.Phase, result.Display)
	}
}

// submitMessage either queues a message while busy or forwards it immediately.
// Must be called with s.mu held. If displayNow is true and the handler is idle,
// the message is rendered as a sent message before being forwarded. queuedLabel is
// used for the visible queued indicator when the message is deferred.
func (s *dialogueHandler) submitMessage(msg SubmitMessage, queuedLabel string, displayNow bool) {
	s.appendHistory(msg.Draft())
	if s.busy {
		s.queue = append(s.queue, msg)
		s.comp.AddQueuedMessage(queuedLabel)
		return
	}
	if displayNow {
		s.comp.AddSendMessageAttachments(msg.Text, msg.Attachments)
	}
	s.mu.Unlock()
	s.tx <- msg
	s.mu.Lock()
}

// appendHistory records d as the most recent recall entry and resets
// the recall cursor so the next ArrowUp starts from the newest entry.
// Consecutive duplicates are collapsed to avoid stuttering recall.
func (s *dialogueHandler) appendHistory(d Draft) {
	if d.Empty() {
		return
	}
	if n := len(s.history); n == 0 || !s.history[n-1].Equal(d) {
		s.history = append(s.history, d.Clone())
	}
	s.historyIdx = len(s.history)
}

// recallUp is the editor-edge fallback for upward navigation: it first
// pops the newest queued message into the compose input, then walks back
// through the submit history. It reports whether anything was recalled.
func (s *dialogueHandler) recallUp() bool {
	if s.comp.Draft().Empty() && len(s.queue) > 0 {
		msg := s.queue[len(s.queue)-1]
		s.queue = s.queue[:len(s.queue)-1]
		s.comp.RemoveLastQueuedMessage()
		s.comp.RestoreDraft(msg.Draft())
		return true
	}
	if s.historyIdx <= 0 {
		return false
	}
	if s.historyIdx == len(s.history) {
		s.scratch = s.comp.Draft()
	}
	s.historyIdx--
	s.comp.RestoreDraft(s.history[s.historyIdx])
	return true
}

// recallDown is the editor-edge fallback for downward navigation: it
// walks forward through the submit history, restoring the draft that
// was being composed once it moves past the newest entry. It reports
// whether anything was recalled.
func (s *dialogueHandler) recallDown() bool {
	if s.historyIdx >= len(s.history) {
		return false
	}
	s.historyIdx++
	if s.historyIdx == len(s.history) {
		s.comp.RestoreDraft(s.scratch)
		s.scratch = Draft{}
	} else {
		s.comp.RestoreDraft(s.history[s.historyIdx])
	}
	return true
}

// drainQueue sends the first queued message (FIFO) through tx and
// promotes its visual representation from "queued" to "sent".
// Must be called with s.mu held. It unlocks around the channel send
// and re-locks on return.
func (s *dialogueHandler) drainQueue() {
	if len(s.queue) == 0 {
		return
	}
	msg := s.queue[0]
	s.queue = s.queue[1:]
	s.comp.PromoteFirstQueuedMessage()
	s.comp.AddSendMessageAttachments(msg.Text, msg.Attachments)
	s.mu.Unlock()
	s.tx <- msg
	s.mu.Lock()
}
