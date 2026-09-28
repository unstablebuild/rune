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

package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/search"
)

// NewPrompt allocates storage for a new Prompt and initializes it.
func NewPrompt(
	storage storageapi.Service, completer Completer,
	dispatcher Dispatcher, interrupter term.Interrupter,
	commands []Manual, config Config,
) *Prompt {
	ret := new(Prompt)
	ret.Init(storage, completer, dispatcher, interrupter, commands, config)
	return ret
}

// Prompt is a tui.Handler that presents a command browsing prompt
// with history scrolling and argument completion.
type Prompt struct {
	mode        commandPromptMode
	config      Config
	dispatcher  Dispatcher
	completer   Completer
	interrupter term.Interrupter

	height, width int
	// overlay buffer over the search list so we can
	// stop the search for multiple argument commands
	// but we can display arguments
	buf        cell.Buffer
	responsive component.Responsive
	list       search.List
	history    search.History

	commandAndArgs []string
	commandsBackup []Manual
	userScrolling  bool
	bracketedPaste bool

	// editSession drives the prompt's modal edit mode (toggled via
	// Config.EditModeKey). When active, all input events are routed
	// to its spawned EditHandler instead of the regular command /
	// argument handlers, and the prompt's completion / search /
	// manual machinery is frozen. Exiting edit mode replays the
	// buffer through the regular handlers as if it had been pasted.
	editSession *EditSession

	// used to signal across Handle calls that user
	// is cyclying through commands, in particular
	// when command key is a character that could be interpreted
	// as a character to be inserted in command input buffer
	prevCommandCycle bool
	// showingHistory tracks whether the prompt is currently
	// showing the command history list (toggled via HistoryToggleKey).
	showingHistory bool

	inputString atomic.Value
	animation   atomic.Pointer[component.Virtual[tui.Component]]

	shownWidth            int
	manualComponent       component.Responsive
	previewComponent      component.Responsive
	previewMatch          []byte
	preview               func()
	ctx                   context.Context
	cancelCtx             func()
	completionCtx         context.Context // children of ctx
	completionCancel      func()
	completingWithHistory atomic.Bool
}

var _ component.Floating = (*Prompt)(nil)

type commandPromptMode uint

const (
	modeCommandPromptCommand commandPromptMode = iota
	// modeCommandPromptArgs1
	// modeCommandPromptArgs2
	// ...
)

const (
	animationWidth          = 3
	defaultSeparatorHeight  = 1
	minWidthManualComponent = 100
	minListHeight           = 3
	maxManualHeight         = 20
)

// Init initializes this handler with the given storage, completer,
// dispatcher, interrupter and config.
func (h *Prompt) Init(
	storage storageapi.Service, completer Completer,
	dispatcher Dispatcher, interrupter term.Interrupter,
	commands []Manual, config Config,
) {
	cfg := search.ListConfig{
		Algo:             search.FuzzyMatch,
		Interrupter:      interrupter,
		CaseSensitive:    false,
		SyncSearch:       config.Sync,
		MatchedTextAttr:  &config.MatchedTextAttr,
		FocusElementAttr: &config.FocusElementAttr,
		ElementAttr:      &config.ElementAttr,
	}
	h.doInit(storage, completer, interrupter, dispatcher, commands, config, cfg)
}

func (h *Prompt) doInit(
	storage storageapi.Service, completer Completer, interrupter term.Interrupter,
	dispatcher Dispatcher, commands []Manual, config Config,
	listCfg search.ListConfig,
) {
	if config.Editor == nil {
		panic("command.Config.Editor is required")
	}
	h.editSession = NewEditSession(config.Editor, config.EditModeKey)
	h.mode = modeCommandPromptCommand
	h.config = config
	h.dispatcher = dispatcher
	h.completer = completer
	h.interrupter = interrupter
	h.animation.Store(&component.Virtual[tui.Component]{C: newNopAnimation(config)})
	h.ctx, h.cancelCtx = context.WithCancel(context.Background())

	h.buf.Init()
	h.inputString.Store("")
	h.responsive = tcomponent.Buffer(&h.buf,
		component.StringResponsiveConfig{
			StringConfig: component.StringConfig{
				Attributes:           config.ElementAttr,
				BackgroundAttributes: config.ElementAttr,
			},
		})

	h.list.Init(listCfg)

	for {
		h.history.Init(storage, config.DocumentID, config.MaxHistory)
		err := h.history.Load()
		if err == nil {
			break
		}
		h.log(log.ErrorLevel, "load history: %v", err)
		storage = storagestub.NewInMemoryService()
	}

	// add a canceled cancelCtx so Wait never needs to check if cancelFn is nil
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	h.completionCtx = ctx
	h.completionCancel = func() {}

	h.Reset(commands)
	h.setManualComponent(h.buildManualComponent(""))
}

func (h *Prompt) getCommandOverlayHeight(width int) int {
	leftWidgetWidth := width - animationWidth
	if leftWidgetWidth <= 0 {
		leftWidgetWidth = width
	}
	height := h.responsive.Height(leftWidgetWidth)
	// set to min 1, as it's being used as input field
	// and max to the height of the overlayed component
	height = int(math.Max(1, float64(height)))
	return height
}

// Resize satisfies tui.Handler
func (h *Prompt) Resize(width, height int) {
	h.width = width
	h.height = height
	h.editSession.Resize(width, height)
	if !h.config.ShowManual || h.manualComponent == nil {
		h.list.Resize(width, height)
		return
	}
	_, _, listHeight := h.calculateSplitHeights(width, height)
	h.list.Resize(width, listHeight)
}

func (h *Prompt) calculateSplitHeights(width, height int) (int, int, int) {
	separatorHeight := h.getSeparatorHeight()
	manHeight := min(maxManualHeight, h.manualComponent.Height(width))
	listHeight := height - manHeight - separatorHeight
	if listHeight < minListHeight {
		listHeight = minListHeight
		manHeight = height - listHeight - separatorHeight
	}
	if manHeight < 0 {
		return 0, 0, height
	}
	return manHeight, separatorHeight, listHeight
}

// SeparatorY returns the Y coordinate of the separator row in the
// prompt's local writer space, or false if no separator is currently
// drawn (ShowManual disabled or no manual attached).
func (h *Prompt) SeparatorY() (int, bool) {
	if !h.config.ShowManual || h.manualComponent == nil {
		return 0, false
	}
	_, separatorHeight, listHeight := h.calculateSplitHeights(h.width, h.height)
	if separatorHeight == 0 {
		return 0, false
	}
	return listHeight, true
}

// Draw satisfies tui.Handler
func (h *Prompt) Draw(w term.Writer) {
	if h.config.ShowManual && h.manualComponent != nil {
		manHeight, separatorHeight, listHeight := h.calculateSplitHeights(h.width, h.height)
		if separatorHeight != 0 {
			separatorOffset := term.Coordinates{Y: listHeight}
			separatorWriter := component.VirtualWriter{
				Writer: w,
				Offset: separatorOffset,
				Height: separatorHeight,
				Width:  h.width,
			}
			comp := component.TestComponent{
				Ch:         h.config.FrameCharSet.HorizontalBottom,
				Attributes: h.config.FrameAttr,
			}
			separator := handler.NopFromComponent(&comp)
			separator.Resize(h.width, separatorHeight)
			separator.Draw(&separatorWriter)
		}

		if manHeight != 0 {
			manOffset := term.Coordinates{Y: listHeight + separatorHeight}
			manWriter := component.VirtualWriter{
				Writer: w,
				Offset: manOffset,
				Height: manHeight,
				Width:  h.width,
			}
			h.manualComponent.Resize(h.width, manHeight)
			h.manualComponent.Draw(&manWriter)
		}
	}

	h.drawPrompt(w)
}

func (h *Prompt) drawPrompt(w term.Writer) {
	// resize on every draw because search.List uses a responsive
	// input so local buffer changes must consider potential resize
	// of search.List
	bufHeight := h.getCommandOverlayHeight(h.width)

	// propagate local cmd+args buffer height to
	// search list, which only has cmd, in case args alone span
	// multiple lines
	h.list.SetMinInputHeight(bufHeight)

	listHeight := h.listRegionHeight()

	leftWidgetWidth := h.width - animationWidth
	if leftWidgetWidth <= 0 {
		h.list.Draw(w)
		h.drawKeyBindingHints(w, listHeight)
		h.responsive.Resize(h.width, bufHeight)
		h.responsive.Draw(w)
		h.drawArgHint(w, h.width)
		return
	}

	// needs to be dynamic because buffer can change height if prompt input
	// exceeds max width.
	var union component.FrameUnion
	union.Init(h.responsive)
	union.Frame = false

	union.UnionRight(h.animation.Load(), animationWidth)
	union.Resize(h.width, bufHeight)

	h.list.Draw(w)
	union.Draw(w)
	h.drawKeyBindingHints(w, listHeight)
	h.drawArgHint(w, leftWidgetWidth)

	// Overlay the active edit-session selection highlight in the
	// prompt's wrap geometry. The responsive renderer above does not
	// preserve per-cell attributes, and the editor's own selection
	// highlight is drawn via DrawLocations in its (unused here) Draw
	// pipeline, so without this overlay visual selections inside
	// modal edit mode would be invisible to the user.
	h.drawSelectionOverlay(w, leftWidgetWidth, bufHeight)
}

// drawArgHint echoes the placeholder of the argument the prompt is
// waiting for right after the cursor, as shadow text that is not part
// of the input buffer. It runs after the buffer overlay so it paints
// over the blank cells the renderer just emitted.
func (h *Prompt) drawArgHint(w term.Writer, wrapWidth int) {
	hint := h.pendingArgHint()
	if hint == "" {
		return
	}
	pos, _, ok := h.Cursor()
	if !ok {
		return
	}
	x := pos.X
	for _, ch := range hint {
		if x >= wrapWidth {
			return
		}
		w.SetCell(term.Coordinates{X: x, Y: pos.Y}, term.NewCell(ch, 1, h.config.ArgHintAttr))
		x++
	}
}

// pendingArgHint returns the placeholder for the argument the prompt
// is waiting for, or "" when the prompt is not sitting on an empty
// argument slot of a known command.
func (h *Prompt) pendingArgHint() string {
	if !h.config.ShowArgHint || h.showingHistory || h.editSession.Active() {
		return ""
	}
	// mode 0 means the command itself is still being typed, and a
	// non-empty list buffer means the argument is already started.
	if h.mode < 1 || len(h.commandAndArgs) == 0 || h.list.Buffer().Size() != 0 {
		return ""
	}
	man, ok := h.getManualForCommand(h.commandAndArgs[0])
	if !ok {
		return ""
	}
	path := []string{man.Name}
	args := h.commandAndArgs[1:]
	for len(args) > 0 {
		sub, ok := getSubcommandManual(man, args[:1])
		if !ok {
			break
		}
		man = sub
		path = append(path, sub.Name)
		args = args[1:]
	}
	return argHint(strings.Join(path, " "), man.Synopsis, args)
}

func (h *Prompt) handleLastCommand() {
	cmd := h.history.Next()
	if cmd == "" {
		h.log(log.TraceLevel, "ignoring next command in history: empty")
		return
	}
	h.reset()
	h.bracketedPaste = true
	for _, ch := range cmd {
		h.handle(term.Event{Type: term.EventKey, Ch: ch}, true)
	}
	h.bracketedPaste = false
	h.setManualComponent(h.newManualComponent(h.buf.String()))
	h.log(log.TraceLevel, "done pushing history events")
}

func (h *Prompt) trimmedCommandAndArgs(cmd string, args ...string) []string {
	ret := make([]string, 0, len(args)+1)
	ret = append(ret, cmd)
	for _, argi := range args {
		if argi != "" {
			ret = append(ret, argi)
		}
	}
	return ret
}

func (h *Prompt) dispatchPreviewArgument() {
	comp, data, cancel := h.doDispatchPreviewArgument()
	h.setPreview(comp, data, cancel)
}

func (h *Prompt) doDispatchPreviewArgument() (component.Responsive, []byte, func()) {
	match, ok := h.list.Focus()
	if !ok {
		return nil, nil, nil
	}
	cmdAndArgsStr := h.buf.String()
	cmdAndArgs := SplitCommandLine(cmdAndArgsStr)
	if len(cmdAndArgs) == 0 {
		return nil, nil, nil
	}
	// last argument is the one we want to preview. If the buffer ends in
	// a separator space, the user has not started typing the new argument
	// yet, so the focused match becomes a brand new trailing argument
	// rather than replacing the previously typed one.
	if !strings.HasSuffix(cmdAndArgsStr, " ") || LastTokenIsIncomplete(cmdAndArgsStr) {
		cmdAndArgs = cmdAndArgs[:len(cmdAndArgs)-1]
	}
	previewArg := string(match.Data())
	cmdAndArgs = append(cmdAndArgs, previewArg)

	if len(cmdAndArgs) == 1 {
		return nil, nil, nil
	}
	h.log(log.DebugLevel, "previewing command %#v", cmdAndArgs)
	comp, cancel, ok := h.dispatcher.Preview(cmdAndArgs[0], cmdAndArgs[1:]...)
	if !ok {
		return nil, nil, nil
	}
	return comp, match.Data(), cancel
}

func (h *Prompt) dispatchCommand() (
	quit, handled bool,
) {

	var commandAndArgsString string
	if len(h.commandAndArgs) != 0 {
		// add what's currently in the buffer; if user wanted to auto-complete
		// then Tab should be expected first
		if buf := h.list.Buffer().String(); buf != "" {
			h.commandAndArgs = append(h.commandAndArgs, buf)
		}
		// trim empty args (i.e. client added more spaces than required between args)
		h.commandAndArgs = h.trimmedCommandAndArgs(h.commandAndArgs[0], h.commandAndArgs[1:]...)
		// A trailing separator marks an argument the completer left in
		// progress, so it is not part of the value. History keeps the
		// canonical form, which is what makes an entry recorded before
		// the partial-candidate convention existed match one recorded
		// after it.
		historyArgs := make([]string, len(h.commandAndArgs))
		historyArgs[0] = h.commandAndArgs[0]
		for i, a := range h.commandAndArgs[1:] {
			historyArgs[i+1] = ShellQuote(
				TrimPartialCandidateSuffix(UnquoteToken(a)))
		}
		commandAndArgsString = strings.Join(historyArgs, " ")

		h.log(log.TraceLevel, "dispatching command and args %#v", h.commandAndArgs)
		// Strip shell-style quoting/escaping from each argument before
		// handing them to the dispatcher so subscribers receive the
		// unescaped literal value (e.g. ~/Unstable\ Build → ~/Unstable Build).
		// The command itself is dispatched verbatim.
		dispatchArgs := make([]string, len(h.commandAndArgs)-1)
		for i, a := range h.commandAndArgs[1:] {
			dispatchArgs[i] = UnquoteToken(a)
		}
		h.dispatcher.Dispatch(h.commandAndArgs[0], dispatchArgs...)
	} else {
		match, _ := h.list.Focus()
		// if no args, then it means that we are in command mode, in which case
		// what's in the match list takes preference.
		commandAndArgsString = string(match.Data())
		// no match, use what's in buffer
		if commandAndArgsString == "" {
			commandAndArgsString = h.buf.String()
		}

		h.log(log.TraceLevel, "dispatching command %#v", commandAndArgsString)
		h.dispatcher.Dispatch(commandAndArgsString)
	}

	if commandAndArgsString != "" {
		err := h.history.Add(commandAndArgsString)
		if err != nil {
			h.log(log.ErrorLevel, "add history %q: %v", commandAndArgsString, err)
		} else {
			h.log(log.TraceLevel, "add history %q: ok", commandAndArgsString)
		}
	}

	return true, true
}

// Handle satisfies tui.Handler
func (h *Prompt) Handle(ev term.Event) (quit, handled bool) {
	if isStart := ev.Type == term.EventPasteStart; isStart || ev.Type == term.EventPasteEnd {
		h.bracketedPaste = isStart
		handled = true
		return
	}

	quit, handled = h.handle(ev, h.config.Sync)
	if handled {
		bufString := h.buf.String()
		h.inputString.Store(bufString)
		f, ok := h.list.Focus()
		dispatchPreview := h.previewComponent != nil &&
			h.previewComponent == h.manualComponent &&
			(!ok || !bytes.Equal(f.Data(), h.previewMatch))
		h.setManualComponent(h.newManualComponent(bufString))
		// if we moved focus after showing preview, reset
		if dispatchPreview {
			h.dispatchPreviewArgument()
		}
	}
	return
}

func (h *Prompt) handle(ev term.Event, sync bool) (quit, handled bool) {
	if ev.Type != term.EventKey {
		return
	}
	if h.editSession.Active() {
		return h.handleEditMode(ev, sync)
	}
	if h.editSession.Enabled() && ev.KeyComb() == h.config.EditModeKey {
		h.enterEditMode()
		return false, true
	}
	switch h.mode {
	case modeCommandPromptCommand:
		return h.handleCommand(ev, sync)
	default:
		return h.handleCompleteArgs(ev, sync)
	}
}

func (h *Prompt) handleCommon(ev *term.Event, sync bool) (quit, handled bool) {
	key := ev.KeyComb()

	if h.config.HistoryToggleKey != (term.KeyComb{}) && key == h.config.HistoryToggleKey {
		h.toggleHistoryList()
		return false, true
	}

	if (key == h.config.HistoryCycleKey && h.config.HistoryCycleKey.Ch == 0) ||
		(key == h.config.HistoryCycleKey && h.buf.Columns(0) == 0) ||
		(key == h.config.HistoryCycleKey && h.prevCommandCycle) {
		h.handleLastCommand()
		h.prevCommandCycle = true
		return false, true
	}

	h.prevCommandCycle = false
	switch ev.Mod {
	case 0:
		handled = true
		switch ev.Key {
		case term.KeyEnter:
			if h.userScrolling {
				h.incArgsCompleteMode(!h.bracketedPaste, sync)
			}
			h.cancelPreview()
			// Cancel any in-flight completion before dispatching: the
			// dispatcher may run synchronously on the UI goroutine
			// (e.g. opening a workspace) and we don't want a still
			// running completer (e.g. a recursive walkdir) thrashing
			// the FS while the dispatcher does its work.
			h.cancelCompletionPush("dispatch")
			quit, handled = h.dispatchCommand()
			h.reset()
		case term.KeyEsc:
			quit = true
			handled = true
			h.cancelPreview()
			h.Cancel()
		case term.KeyArrowDown:
			if h.userScrolling {
				handled = h.list.FocusDown()
			} else {
				h.setUserScrolling(true)
			}
			h.dispatchPreviewArgument()
		case term.KeyArrowUp:
			handled = h.list.FocusUp()
			if !handled {
				handled = h.setUserScrolling(false)
				h.cancelPreview()
				h.resetManualComponent()
			} else {
				h.dispatchPreviewArgument()
			}
		case term.KeyTab:
			h.incArgsCompleteMode(!h.bracketedPaste, sync)
		case term.KeySpace:
			ev.Ch = ' '
			handled = false
		default:
			handled = false
		}
	case term.ModCtrl:
		handled = true
		if ev.Key == term.KeyBackspace {
			h.deleteWordBack(sync)
			return
		}
		switch ev.Ch {
		// 'h' is bound because terminals without the kitty keyboard
		// protocol collapse <c-backspace> onto ^H before Rune sees it.
		case 'w', 'h':
			h.deleteWordBack(sync)
		case 'u':
			h.deleteLineBack(sync)
		case 'j', 'n':
			if h.userScrolling {
				handled = h.list.FocusDown()
			} else {
				handled = h.setUserScrolling(true)
			}
			h.dispatchPreviewArgument()
		case 'k', 'p':
			ok := h.list.FocusUp()
			if !ok {
				handled = h.setUserScrolling(false)
				h.cancelPreview()
				h.resetManualComponent()
			} else {
				h.dispatchPreviewArgument()
			}
		case 'c':
			select {
			case <-h.completionCtx.Done():
				// completion already canceled
				h.cancelPreview()
				quit = true
			default:
			}
			h.cancelCompletionPush("received ctrl-c")
		default:
			handled = false
		}
	case term.ModAlt:
		if ev.Key != term.KeyBackspace {
			return
		}
		handled = true
		h.deleteWordBack(sync)
	}
	return
}

// deleteCellBack removes the last cell of the prompt buffer, keeping
// the fuzzy-match token buffer in sync and unwinding one level of
// argument completion when that token buffer drains. It reports
// whether there was anything left to remove.
func (h *Prompt) deleteCellBack(sync bool) bool {
	cols := h.buf.Columns(0)
	if cols != 0 {
		h.buf.DeleteCell(term.Coordinates{X: cols - 1})
	}
	if h.mode == modeCommandPromptCommand {
		if cols == 0 {
			return false
		}
		h.list.Buffer().Replace(h.buf.String())
		return true
	}
	if buf := h.list.Buffer(); buf.Size() != 0 {
		buf.DeleteCell(term.Coordinates{X: buf.Columns(0) - 1})
		return true
	}
	if !h.decArgsCompleteMode(sync) {
		h.setCommandMode()
	}
	return true
}

// isWordRune classifies a rune for the prompt's shell-style word
// deletion. The boundary is narrower than whitespace on purpose so a
// single <c-w> walks back one path component while completing a file
// argument, matching the inputbox handler in the SDK.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// lastCellRune returns the leading rune of the prompt buffer's final
// cell. Word boundaries are classified per cell rather than per rune
// so wide characters and combining marks are never split.
func (h *Prompt) lastCellRune() (rune, bool) {
	cols := h.buf.Columns(0)
	if cols == 0 {
		return 0, false
	}
	c, ok := h.buf.Cell(term.Coordinates{X: cols - 1})
	if !ok {
		return 0, false
	}
	return c.Ch, true
}

// deleteWordBack drops the run of separators before the cursor and
// then the word preceding them. Unlike <backspace> it never closes
// the prompt when the line drains.
func (h *Prompt) deleteWordBack(sync bool) {
	h.cancelPreview()
	for _, word := range [...]bool{false, true} {
		for {
			r, ok := h.lastCellRune()
			if !ok || isWordRune(r) != word {
				break
			}
			if !h.deleteCellBack(sync) {
				return
			}
		}
	}
}

// deleteLineBack clears the whole input, returning the prompt to
// command mode without closing it.
func (h *Prompt) deleteLineBack(sync bool) {
	h.cancelPreview()
	for h.deleteCellBack(sync) {
	}
}

// deleteHistoryCompletionFocusItem will remove the entry from the history as well as
// removing the node from the focus list.
func (h *Prompt) deleteFocusFromHistory() (ret error) {
	match, ok := h.list.Focus()
	if !ok {
		return errors.New("get item in focus")
	}

	cmd := strings.Join(
		[]string{
			strings.Join(h.commandAndArgs, " "),
			string(match.Data()),
		},
		" ",
	)

	if ok := h.list.RemoveFocus(); !ok {
		ret = multierror.Append(ret, errors.New("search list remove focus"))
	}

	if h.list.TotalCount() == 0 {
		h.userScrolling = false
	}

	err := h.history.Remove(cmd)
	if err != nil {
		ret = multierror.Append(ret, fmt.Errorf("history remove cmd: %w", err))
	}

	return ret
}

func (h *Prompt) handleCommand(ev term.Event, sync bool) (quit, handled bool) {
	quit, handled = h.handleCommon(&ev, sync)
	if handled {
		return
	}

	if ev.Mod != 0 {
		return
	}

	switch ev.Key {
	case term.KeyBackspace:
		handled = true
		if !h.deleteCellBack(sync) {
			quit = true
			h.cancelPreview()
			h.Cancel()
		}
		return
	}

	if ev.Ch == 0 {
		return
	}

	handled = true
	if ev.Ch == ' ' {
		// check the buffer state before appending the incoming space —
		// once written, a pending `\` would already have consumed it.
		incomplete := LastTokenIsIncomplete(h.buf.String())
		h.buf.WriteString(string(ev.Ch))
		if incomplete {
			// space belongs inside a quoted/escaped argument, not a separator.
			h.list.Buffer().WriteString(string(ev.Ch))
			return
		}
		// wait as commands are finite and muscle memory could beat
		// the completing logic
		h.Wait()
		h.incArgsCompleteMode(!h.bracketedPaste, sync)
		return
	}
	h.buf.WriteString(string(ev.Ch))
	h.list.Buffer().WriteString(string(ev.Ch))
	return
}

func (h *Prompt) handleCompleteArgs(ev term.Event, sync bool) (quit, handled bool) {
	quit, handled = h.handleCommon(&ev, sync)
	if handled {
		return
	}

	if ev.Mod != 0 {
		return
	}

	switch ev.Key {
	case term.KeyBackspace:
		if h.userScrolling && h.completingWithHistory.Load() {
			if err := h.deleteFocusFromHistory(); err != nil {
				h.log(log.ErrorLevel, "delete focus from history: %v", err)
			}
			handled = true
			return
		}

		h.cancelPreview()
		handled = true
		h.deleteCellBack(sync)
		return
	}

	if ev.Ch == 0 {
		return
	}

	h.cancelPreview()
	handled = true
	if ev.Ch == ' ' {
		incomplete := LastTokenIsIncomplete(h.buf.String())
		h.buf.WriteString(string(ev.Ch))
		if incomplete {
			// space belongs inside a quoted/escaped argument, not a separator.
			isEmpty := h.list.Buffer().Size() == 0
			h.list.Buffer().WriteString(string(ev.Ch))
			if isEmpty {
				h.setCompletionList(false, sync, h.commandAndArgs[0], h.completionArgs()...)
			}
			return
		}
		// do not wait here, as args are expected to be dynamic
		// and fuzzy search is a guide for user to complete
		h.incArgsCompleteMode(false, sync)
		return
	}
	isEmpty := h.list.Buffer().Size() == 0
	h.buf.WriteString(string(ev.Ch))
	h.list.Buffer().WriteString(string(ev.Ch))
	if isEmpty || (ev.Ch == '/' && !h.bracketedPaste) {
		h.setCompletionList(false, sync, h.commandAndArgs[0], h.completionArgs()...)
	}
	return
}

func (h *Prompt) completionArgs() []string {
	args := make([]string, 0, len(h.commandAndArgs))
	args = append(args, h.commandAndArgs[1:]...)
	return append(args, h.list.Buffer().String())
}

// enterEditMode opens the EditSession seeded at the end of the
// prompt buffer (where the prompt's cursor was) so the user can keep
// typing without first having to reposition.
func (h *Prompt) enterEditMode() {
	h.cancelPreview()
	h.editSession.Begin(&h.buf, term.Coordinates{X: h.buf.Columns(0)})
	h.log(log.TraceLevel, "entering command prompt edit mode")
}

// exitEditMode commits the edited buffer back through the regular
// command/argument handlers as if it had been pasted. This re-runs
// completion and rebuilds h.commandAndArgs without any
// cherry-picking logic.
func (h *Prompt) exitEditMode(sync bool) {
	if !h.editSession.Active() {
		return
	}
	final := h.buf.String()
	h.editSession.End()
	h.log(log.TraceLevel, "exiting command prompt edit mode: %q", final)

	h.reset()
	h.bracketedPaste = true
	for _, ch := range final {
		h.handle(term.Event{Type: term.EventKey, Ch: ch}, sync)
	}
	h.bracketedPaste = false
}

// handleEditMode forwards events to the EditSession. On Exit it
// silently drops back to command mode; on Submit it additionally
// re-dispatches the original <enter> through the regular handler so
// the now-replayed command is executed.
func (h *Prompt) handleEditMode(ev term.Event, sync bool) (quit, handled bool) {
	done, handled := h.editSession.Handle(ev)
	switch done {
	case EditDoneExit:
		h.exitEditMode(sync)
		return false, true
	case EditDoneSubmit:
		h.exitEditMode(sync)
		switch h.mode {
		case modeCommandPromptCommand:
			return h.handleCommand(ev, sync)
		default:
			return h.handleCompleteArgs(ev, sync)
		}
	}
	return false, handled
}

// completeTopList accepts the focused candidate. It reports whether a
// candidate was accepted and whether that candidate was partial, i.e.
// it advances the current argument without terminating it.
func (h *Prompt) completeTopList() (completed, partial bool) {
	match, ok := h.list.Focus()
	if !ok {
		h.log(log.TraceLevel, "completeTopList: no matches on search list with %q",
			h.list.Buffer().String())
		return false, false
	}
	candidate := string(match.Data())
	if h.mode != modeCommandPromptCommand && IsPartialCandidate(candidate) {
		h.list.Buffer().Replace(candidate)
		h.buf.Replace(strings.Join(h.commandAndArgs, " ") + " " + candidate)
		return true, true
	}
	// could have completed multiple arguments
	parts := SplitCommandLine(candidate)
	h.commandAndArgs = append(h.commandAndArgs, parts...)
	newCmdAndArgs := strings.Join(h.commandAndArgs, " ")
	h.buf.Replace(newCmdAndArgs + " ")

	return true, false
}

func (h *Prompt) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "command.Prompt").
		Logf(level, msg, args...)
}

func (h *Prompt) incArgsCompleteMode(complete bool, sync bool) {
	defer h.resetManualComponent()
	completed, partial := false, false
	if complete {
		completed, partial = h.completeTopList()
	}
	if partial {
		h.log(log.TraceLevel, "partial argument completion (sync=%v): %+v",
			sync, h.commandAndArgs)
		h.setCompletionList(false, sync, h.commandAndArgs[0], h.completionArgs()...)
		return
	}
	if !completed {
		h.commandAndArgs = append(h.commandAndArgs, h.list.Buffer().String())
	}

	h.log(log.TraceLevel, "increment args complete mode (complete=%v, sync=%v): %+v",
		complete, sync, h.commandAndArgs)

	// mode needs to be at least the number of command and arguments that have
	// been completed as per user request
	for int(h.mode) < len(h.commandAndArgs) {
		h.mode++
	}
	h.list.Buffer().Reset()
	h.setCompletionList(true, sync, h.commandAndArgs[0], h.commandAndArgs[1:]...)
}

func (h *Prompt) decArgsCompleteMode(sync bool) bool {
	defer h.resetManualComponent()
	if h.mode == 1 {
		return false
	}
	h.mode--
	lastIdx := len(h.commandAndArgs) - 1
	last := h.commandAndArgs[lastIdx]
	h.commandAndArgs = h.commandAndArgs[:lastIdx]
	h.list.Buffer().Replace(last)

	h.log(log.TraceLevel, "decrement args complete mode (sync=%v): %+v",
		sync, h.commandAndArgs)

	h.setCompletionList(false, sync, h.commandAndArgs[0], h.completionArgs()...)
	return true
}

func (h *Prompt) setCommandMode() {
	h.mode = modeCommandPromptCommand
	h.list.Buffer().Replace(h.buf.String())
	h.commandAndArgs = h.commandAndArgs[:0]
	h.resetListWith(h.commandsBackup)
	h.setUserScrolling(true)
}

func (h *Prompt) setCompletionList(
	persistLastArgUpdates, sync bool, cmd string, args ...string,
) {
	h.completingWithHistory.Store(false)
	// in bracketed paste we trust the cancel completion
	// will do its job, and only the last pushed completer will
	// remain.
	if h.bracketedPaste {
		// override but only if global sync mode is not on (i.e. tests)
		sync = h.config.Sync
	}
	h.log(log.TraceLevel, "setCompletionList: %s %#v", cmd, args)

	ctx, cancel := context.WithCancel(h.ctx)

	h.cancelCompletionPush("re set completion list")
	h.completionCancel = cancel
	h.completionCtx = ctx

	h.setUserScrolling(false)

	// if user added any extra spaces, do not pass to internal completer
	cmdAndArgs := h.trimmedCommandAndArgs(cmd, args...)

	// BUT always add the empty last argument for external completers
	// to simplify logic and avoid having to share anything else like the internal state.
	bufStrArgs := h.buf.String()
	var externalCmdAndArgs []string
	// only add one extra argument at the end, if there are multiple spaces
	// AND the trailing space is a separator (i.e. not inside a quote or
	// after a backslash escape).
	if strings.HasSuffix(bufStrArgs, " ") && !LastTokenIsIncomplete(bufStrArgs) {
		externalCmdAndArgs = SplitCommandLine(strings.TrimRight(bufStrArgs, " "))
		externalCmdAndArgs = append(externalCmdAndArgs, "")
	} else {
		externalCmdAndArgs = SplitCommandLine(bufStrArgs)
	}

	// also do not add intermediate spaces
	finalExternalCmdAndArgs := make([]string, 0, len(externalCmdAndArgs))
	for i, cmd := range externalCmdAndArgs {
		if cmd != "" || i == len(externalCmdAndArgs)-1 {
			finalExternalCmdAndArgs = append(finalExternalCmdAndArgs, cmd)
		}
	}

	it, newLastArg, err := h.completer.Complete(ctx, finalExternalCmdAndArgs)
	if err != nil {
		if it != nil {
			_ = it.Close()
		}
		it = iterator.Empty[string]()
	}
	if newLastArg != "" {
		newCmdAndArgs := make([]string, len(cmdAndArgs))
		copy(newCmdAndArgs, cmdAndArgs)
		h.log(log.TraceLevel, "completer returned updated last arg %v: %q -> %q",
			cmdAndArgs, newCmdAndArgs[len(newCmdAndArgs)-1], newLastArg)
		newCmdAndArgs[len(newCmdAndArgs)-1] = newLastArg
		// NOTE: this method is prone to expose errors if newLastArg is incorrect
		// ensure that all completer implementation use newLastArg sparingly
		// and at some point a Delete+Insert option should be explored
		h.buf.Replace(strings.Join(newCmdAndArgs, " "))
		if persistLastArgUpdates {
			h.commandAndArgs[len(h.commandAndArgs)-1] = newLastArg
		} else {
			h.list.Buffer().Replace(newLastArg)
		}
	}

	if sync {
		it, isEmpty := iterator.IsEmpty(ctx, it)
		if isEmpty {
			it = h.manualCompleter(ctx, cmdAndArgs[0], cmdAndArgs[1:]...)
		}
		h.list.Wait()
		h.list.DataReset()
		h.pushCompletionListSync(ctx, cancel, cmdAndArgs, it)
	} else {
		h.list.Cancel()
		h.list.DataReset()
		ch := h.list.Push(ctx)
		mode := h.mode
		commandsBackup := h.commandsBackup
		history := append([]string(nil), h.history.Slice()...)
		// IsEmpty could be performing I/O under the hood
		// via Next, so do not block
		go debug.CapturePanicReport(func() {
			var virtual *component.Virtual[tui.Component]
			var animationCloser interface{ Close() error }
			if h.config.ShowProgressHint {
				// draw progress animation while iterator is still returning results
				frames, seq := component.ProgressAnimationFrames()
				anim := component.NewAnimation(h.interrupter, frames, seq, 10)
				virtual = &component.Virtual[tui.Component]{C: anim}
				animationCloser = anim
				h.animation.Store(virtual)
			}
			defer func() {
				if virtual != nil {
					if animationCloser != nil {
						_ = animationCloser.Close()
					}
					nop := &component.Virtual[tui.Component]{C: newNopAnimation(h.config)}
					h.animation.CompareAndSwap(virtual, nop)
				}
				_ = h.interrupter.Interrupt(ctx)
			}()

			it, isEmpty := iterator.IsEmpty(ctx, it)
			if isEmpty {
				it = manualCompleter(ctx,
					commandsBackup, mode,
					cmdAndArgs[0], cmdAndArgs[1:]...)
			}
			pushCompletionList(ctx, h.log, &h.completingWithHistory,
				history, mode, ch, cancel, cmdAndArgs, it)
		})
	}
}

func (h *Prompt) pushCompletionListSync(
	ctx context.Context,
	cancel func(),
	cmdAndArgs []string,
	it iterator.Iterator[string],
) {
	defer cancel()
	push := func(it iterator.Iterator[string]) bool {
		els, err := iterator.ToSlice(ctx, it)
		_ = it.Close()
		if err != nil {
			if err := it.Err(); err != nil && !errors.Is(err, context.Canceled) {
				h.log(log.ErrorLevel, "completion iterator error: %v", err)
			}
		}
		sort.Strings(els)
		for _, el := range els {
			h.list.PushSync([]byte(el))
		}
		return len(els) != 0
	}

	if push(it) {
		return
	}
	// push args history if default completion iterator is empty
	it, ok := commandArgsHistoryIterator(ctx, h.log, &h.completingWithHistory,
		h.history.Slice(), h.mode, cmdAndArgs)
	if !ok {
		return
	}
	push(it)
}

func pushCompletionList(
	ctx context.Context,
	logf func(log.Level, string, ...any),
	completingWithHistory *atomic.Bool,
	history []string, mode commandPromptMode,
	ch chan<- []byte, cancel func(),
	cmdAndArgs []string,
	it iterator.Iterator[string],
) {
	defer close(ch)
	defer cancel()

	var i int
	push := func(it iterator.Iterator[string]) {
		defer func() {
			_ = it.Close()
		}()
		for ; ; i++ {
			next, ok := it.Next(ctx)
			if !ok {
				break
			}
			select {
			case <-ctx.Done():
				logf(log.TraceLevel, "context canceled for ch %p before completed push", ch)
				return
			case ch <- []byte(next):
				logf(log.TraceLevel, "pushed %q onto search list for ch %p", next, ch)
			}
		}

		err := it.Err()
		if err != nil && !errors.Is(err, context.Canceled) {
			logf(log.ErrorLevel, "completion iterator error: %v", err)
		}
	}

	push(it)
	if i != 0 {
		return
	}

	it, ok := commandArgsHistoryIterator(ctx, logf, completingWithHistory,
		history, mode, cmdAndArgs)
	if !ok {
		return
	}
	push(it)
}

func commandArgsHistoryIterator(
	ctx context.Context, logf func(log.Level, string, ...any),
	completingWithHistory *atomic.Bool,
	history []string, mode commandPromptMode, cmdAndArgs []string,
) (iterator.Iterator[string], bool) {
	it := iterator.FromSlice(history)

	// cmdAndArgs is not orthogonal to how we want to handle them here
	// essentially, we don't know by simply inspecting them, if we are
	// at the start of a new arg, or at the end of the previous command
	// as last space is handled ambigously.
	cmdAndArgs = SplitCommandLine(strings.Join(cmdAndArgs, " "))
	m := len(cmdAndArgs)
	if int(mode) < len(cmdAndArgs) {
		m = len(cmdAndArgs) - 1
	}
	queryMatch := strings.Join(cmdAndArgs[:m], " ")

	logf(log.TraceLevel, "filtering data with mode %d and cmdAndArgs: %+v, len(%d), filter: %+v",
		mode, cmdAndArgs, len(cmdAndArgs), queryMatch)

	filterNoArgs := iterator.Filter(it, func(query string) bool {
		return strings.HasPrefix(query, queryMatch)
	})
	mapArgs := iterator.Map(filterNoArgs, func(query string) (args string) {
		storedAndArgs := SplitCommandLine(query)
		ret := strings.Join(storedAndArgs[m:], " ")
		return ret
	})
	nonEmptyArgs := iterator.Filter(mapArgs, func(args string) bool {
		return args != ""
	})
	it, isEmpty := iterator.IsEmpty(ctx, nonEmptyArgs)

	if !isEmpty {
		completingWithHistory.Store(true)
	}

	return it, !isEmpty
}

// Reset resets the commands listed in this Prompt.
// It should be called after initialization and every time
// new commands are available.
func (h *Prompt) Reset(commands []Manual) {
	h.setCommandMode()
	h.buf.Reset()
	h.list.Buffer().Reset()
	h.commandsBackup = commands
	h.showingHistory = false
	h.resetListWith(commands)
}

// ResetHistory resets the prompt list with entries from the
// command history, showing previously executed commands.
func (h *Prompt) ResetHistory() {
	h.setCommandMode()
	h.buf.Reset()
	h.list.Buffer().Reset()
	h.showingHistory = true
	h.resetListWith(h.historyManuals())
}

// toggleHistoryList toggles the prompt between showing
// the regular command list and the command history list.
func (h *Prompt) toggleHistoryList() {
	h.setCommandMode()
	h.buf.Reset()
	h.list.Buffer().Reset()
	if h.showingHistory {
		h.showingHistory = false
		h.resetListWith(h.commandsBackup)
	} else {
		h.showingHistory = true
		h.resetListWith(h.historyManuals())
	}
}

func (h *Prompt) historyManuals() []Manual {
	entries := h.history.Slice()
	manuals := make([]Manual, len(entries))
	for i, entry := range entries {
		manuals[i] = Manual{Name: entry}
	}
	return manuals
}

// assumes holding lock
func (h *Prompt) cancelCompletionPush(reason string) {
	h.log(log.DebugLevel, "completion push to search list: %s", reason)
	h.completionCancel()
	h.list.Cancel()
}

func (h *Prompt) resetListWith(items []Manual) {
	h.cancelCompletionPush("reset list")

	h.list.DataReset()
	for _, item := range items {
		h.list.PushSync([]byte(item.Name))
	}
}

func (h *Prompt) reset() {
	h.Cancel()
	h.setCommandMode()
	h.buf.Reset()
	h.list.Buffer().Reset()
}

// Selection satisfies tui.Handler.
func (h *Prompt) Selection() (string, bool) {
	if sel, ok := h.editSession.Selection(); ok {
		return sel, true
	}
	return "", false
}

// Cursor satisfies tui.Handler
func (h *Prompt) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	if h.width == 0 {
		return term.Coordinates{}, 0, false
	}
	leftWidgetWidth := h.width - animationWidth
	if leftWidgetWidth <= 0 {
		leftWidgetWidth = h.width
	}
	bufHeight := min(h.getCommandOverlayHeight(h.width), h.height)
	rows := h.buf.RawCells()
	// In edit mode, translate the editor's buffer-relative cursor
	// through the same wrap geometry the prompt uses to render the
	// buffer; the editor's own Cursor() returns window coordinates
	// shaped by its own (typically wrap=false) layout and would
	// otherwise drift from the visible wrapped text.
	if bufPos, style, ok := h.editSession.CursorAtScroll(); ok {
		pos := visualCursorAtBufferPos(rows, bufPos, leftWidgetWidth, bufHeight)
		return pos, style, true
	}
	// Outside edit mode the cursor sits one cell past the last
	// rune of the last buffer row.
	bufPos := term.Coordinates{Y: max(0, len(rows)-1)}
	if len(rows) > 0 {
		bufPos.X = len(rows[len(rows)-1])
	}
	pos := visualCursorAtBufferPos(rows, bufPos, leftWidgetWidth, bufHeight)
	return pos, term.CursorStyleBlinkingBar, true
}

// chunkDisplayWidth sums the cell display widths of one wrapped
// chunk. Tabs and NULs deserialize as cells with Width=0 yet still
// occupy one column on screen, so clamp Width to a minimum of 1.
func chunkDisplayWidth(chunk []term.Cell) int {
	var total int
	for _, c := range chunk {
		w := int(c.Width)
		if w == 0 {
			w = 1
		}
		total += w
	}
	return total
}

// drawSelectionOverlay paints reverse-video attributes on every
// visible cell that falls within the active edit-session
// selection. The responsive renderer used by the prompt does not
// preserve per-cell attributes, and the editor's own selection
// rendering is tied to its DrawLocations pipeline that the prompt
// bypasses, so without this overlay visual selections in modal
// edit mode would be invisible.
func (h *Prompt) drawSelectionOverlay(w term.Writer, wrapWidth, visibleHeight int) {
	from, to, ok := h.editSession.SelectionBounds()
	if !ok {
		return
	}
	from, to = term.CoordinatesSort(from, to)
	rows := h.buf.RawCells()
	attr := term.Attributes{Attrs: term.AttrReverse}
	for y := from.Y; y <= to.Y && y < len(rows); y++ {
		row := rows[y]
		startX := 0
		endX := len(row)
		if y == from.Y {
			startX = from.X
		}
		if y == to.Y {
			endX = min(to.X, len(row))
		}
		for x := startX; x < endX; x++ {
			pos := visualCursorAtBufferPos(rows,
				term.Coordinates{Y: y, X: x},
				wrapWidth, visibleHeight)
			cw := int(row[x].Width)
			if cw <= 0 {
				cw = 1
			}
			for dx := 0; dx < cw; dx++ {
				w.UnionAttributes(
					term.Coordinates{X: pos.X + dx, Y: pos.Y}, attr)
			}
		}
	}
}

// listRegionHeight returns the height of the result-list region in the
// prompt's local writer space, i.e. the area h.list.Draw occupies. When
// the manual side panel is shown it is the split list height; otherwise
// the full prompt height.
func (h *Prompt) listRegionHeight() int {
	if !h.config.ShowManual || h.manualComponent == nil {
		return h.height
	}
	_, _, listHeight := h.calculateSplitHeights(h.width, h.height)
	return listHeight
}

// commandLinePrefix returns the already-committed command words the
// visible rows are completing (all whole tokens before the in-progress
// last token), mirroring buildManualComponent's trimming rule.
func (h *Prompt) commandLinePrefix() []string {
	cmdAndArgs := SplitCommandLine(strings.TrimSpace(h.inputString.Load().(string)))
	if len(cmdAndArgs) > 0 && int(h.mode) < len(cmdAndArgs) {
		cmdAndArgs = cmdAndArgs[:len(cmdAndArgs)-1]
	}
	return cmdAndArgs
}

// isKnownCommandLine reports whether every token in the line names a
// command or nested subcommand in the command tree. Dynamic completer
// arguments (file paths, workspace indices, etc.) do not name commands,
// so their rows are excluded from key hints even when the raw line
// happens to match a user binding like `<c-x>2 -> workspacefocus 2`.
func (h *Prompt) isKnownCommandLine(tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	man, ok := h.getManualForCommand(tokens[0])
	if !ok {
		return false
	}
	if len(tokens) == 1 {
		return true
	}
	_, ok = getSubcommandManual(man, tokens[1:])
	return ok
}

// drawKeyBindingHints overlays a right-aligned key label onto each
// visible result row whose full command line has a bound key. It runs
// on the single-threaded draw path right after h.list.Draw and uses
// only public search.List accessors. History rows and rows whose line
// is unbound draw nothing.
func (h *Prompt) drawKeyBindingHints(w term.Writer, listHeight int) {
	if h.config.KeyBindingHint == nil || h.showingHistory {
		return
	}
	elemHeight := h.list.ElementHeight()
	if elemHeight <= 0 {
		return
	}
	inputHeight := h.list.InputHeight()
	prefix := h.commandLinePrefix()
	focusOffset := h.list.FocusOffset()
	scrollOffset := h.list.Offset()
	row := 0
	h.list.IterateVisible(func(m search.Match) {
		defer func() { row++ }()
		y := inputHeight + row*elemHeight
		if y >= listHeight {
			return
		}
		tokens := append(append([]string{}, prefix...), string(m.Data()))
		if !h.isKnownCommandLine(tokens) {
			return
		}
		label := h.config.KeyBindingHint(strings.Join(tokens, " "))
		if label == "" {
			return
		}
		runes := []rune(label)
		labelW := len(runes)
		dataW := len(m.Data())
		startX := h.width - labelW
		// require at least one blank cell between the row text and the hint
		if labelW >= h.width || startX <= dataW {
			return
		}
		attr := h.config.KeyBindingHintAttr
		if scrollOffset+row == focusOffset {
			attr = h.config.KeyBindingHintFocusAttr
		}
		for i, ch := range runes {
			w.SetCell(term.Coordinates{X: startX + i, Y: y}, term.NewCell(ch, 1, attr))
		}
	})
}

// wrappedRows returns the number of visual rows a buffer row of the
// given cell count occupies when wrapped to wrapWidth cells, matching
// component.ResponsiveString.massageInput's row count. An empty row
// still occupies one visual row (the renderer emits one blank line).
func wrappedRows(cellCount, wrapWidth int) int {
	if cellCount == 0 {
		return 1
	}
	rows := cellCount / wrapWidth
	if cellCount%wrapWidth != 0 {
		rows++
	}
	return rows
}

// visualCursorAtBufferPos maps a buffer (row, col) coordinate, where
// col is a cell index within the buffer row, to the prompt's wrapped
// visual coordinates. It mirrors component.ResponsiveString.
// massageInput: source rows are sliced into chunks of wrapWidth cells
// each; the visual Y is the cumulative chunk count of preceding rows
// plus the chunk index within the current row, and visual X is the
// display width of the cells before col within the current chunk
// (clamped to wrapWidth-1 so the cursor never escapes the visible
// area). When the resulting Y would fall past visibleHeight the
// cursor is clamped to the last visible cell, matching the
// renderer's vertical truncation.
func visualCursorAtBufferPos(
	rows [][]term.Cell, bufPos term.Coordinates,
	wrapWidth, visibleHeight int,
) term.Coordinates {
	if wrapWidth <= 0 {
		return term.Coordinates{}
	}
	by := max(0, bufPos.Y)
	if by >= len(rows) {
		by = max(0, len(rows)-1)
	}
	var y int
	for i := 0; i < by; i++ {
		y += wrappedRows(len(rows[i]), wrapWidth)
	}
	var chunk []term.Cell
	if by < len(rows) {
		row := rows[by]
		bx := min(len(row), max(0, bufPos.X))
		chunkIdx := bx / wrapWidth
		y += chunkIdx
		chunk = row[chunkIdx*wrapWidth : bx]
	}
	x := chunkDisplayWidth(chunk)
	if x >= wrapWidth {
		x = wrapWidth - 1
	}
	if visibleHeight > 0 && y >= visibleHeight {
		return term.Coordinates{X: wrapWidth - 1, Y: visibleHeight - 1}
	}
	return term.Coordinates{X: x, Y: y}
}

// Wait waits for any asynchronous completion
// or search to finish before it returns.
//
// Cancel should be used before Wait to prevent
// it from blocking indefinitely, in case a completion
// list takes a long time to process.
func (h *Prompt) Wait() {
	<-h.completionCtx.Done()
	h.list.Wait()
}

// Cancel cancels any asynchronous completion or search currently ongoing
// or does nothing if there's currently no ongoing completion or search.
func (h *Prompt) Cancel() {
	h.cancelCompletionPush("cancel")
	h.list.Cancel()
}

// Dimensions satisfies component.Floating.
func (h *Prompt) Dimensions() (width, height int) {
	const (
		matchPadding = 1
		maxWidth     = 100
		maxHeight    = 20
	)

	minWidth := 50
	if h.config.ShowManual && h.manualComponent != nil {
		minWidth = minWidthManualComponent
	}
	// always set min width if show manual was triggered
	// so user doesn't get confused if width goes back and forth
	// as its tipying.
	minWidth = int(math.Max(float64(minWidth), float64(h.shownWidth)))

	maxWidthItems := minWidth
	h.list.IterateVisible(func(m search.Match) {
		if mlen := len(m.Data()) + matchPadding; mlen > maxWidthItems {
			maxWidthItems = mlen
		}
	})
	width = int(math.Max(float64(h.list.Buffer().MaxColumns()), float64(maxWidthItems)))
	width = int(math.Min(float64(width), maxWidth))

	bufHeight := h.getCommandOverlayHeight(width)
	height = int(math.Min(math.Max(float64(h.list.MatchCount()+bufHeight), minListHeight), maxHeight))

	if h.config.ShowManual && h.manualComponent != nil {
		height += h.manualComponent.Height(width)
		height += h.getSeparatorHeight()
	}
	h.shownWidth = width
	return
}

// Close closes all resources associated with this Prompt.
func (h *Prompt) Close() error {
	h.cancelCompletionPush("close")
	cancel := h.detachPreview()
	h.cancelCtx()
	h.editSession.End()
	if cancel != nil {
		cancel()
	}

	h.Wait()

	err := h.list.Close()

	h.commandAndArgs = nil
	h.commandsBackup = nil
	h.manualComponent = nil
	h.previewComponent = nil
	h.previewMatch = nil
	h.preview = nil
	return err
}

func (h *Prompt) setUserScrolling(scrolling bool) bool {
	ret := h.userScrolling != scrolling
	h.userScrolling = scrolling
	if scrolling {
		h.list.SetFocusAttr(h.config.FocusElementAttr)
	} else {
		h.list.SetFocusAttr(h.config.ElementAttr)
	}
	return ret
}

func (h *Prompt) newManualComponent(bufString string) component.Responsive {
	focus, ok := h.list.Focus()
	if ok && bytes.Equal(focus.Data(), h.previewMatch) && h.previewComponent != nil {
		return h.previewComponent
	}
	if h.previewComponent != nil {
		return h.previewComponent
	}
	return h.buildManualComponent(bufString)
}

func (h *Prompt) buildManualComponent(bufString string) component.Responsive {
	var man Manual
	var ok bool
	cmdAndArgs := SplitCommandLine(strings.TrimSpace(bufString))

	if len(cmdAndArgs) == 0 || (len(cmdAndArgs) == 1 && int(h.mode) < 1) {
		// if input is something like "ed" or "" then
		// find the manual of the top match of the search list.
		h.log(log.TraceLevel, "Length in words of input buffer is 0-1, "+
			"using top of the search list as desired command.")
		man, ok = h.manualForCommandInFocus()
	} else if len(cmdAndArgs) == 1 || (len(cmdAndArgs) == 2 && h.mode < 2) {
		// if input is something like "edit " or "edit m" or "edit myFile " then
		// find the manual of the first word in the input buffer
		cmd := cmdAndArgs[0]
		man, ok = h.getManualForCommand(cmd)
	} else {
		// if input is something like "edit myFile my" or "edit myFile myFile ..."
		// find the manual of the first word in the input buffer, then try to find
		// the manual of the last completed subcommand.
		h.log(log.TraceLevel, "Length in words of input buffer is >1, "+
			"using buffer to get man for command or sub-command.")
		cmd := cmdAndArgs[0]
		man, ok = h.getManualForCommand(cmd)
		if !ok {
			h.log(log.TraceLevel, "could not find manual for first "+
				"word in input buffer %q", cmd)
			return nil
		}
		// use mode to know if user has already completed
		// last arg or not.
		args := cmdAndArgs[1:]
		if int(h.mode) < len(cmdAndArgs) {
			// trim last argument, since it hasn't been completed yet
			args = args[:len(args)-1]
		}
		subcmd, foundSubcommand := getSubcommandManual(man, args)
		if foundSubcommand {
			man = subcmd
		} // else display parent command's manual
	}

	if ok {
		return h.makeManualComponent(man, h.config.FrameCharSet, h.config.ElementAttr)
	}
	return nil
}

func (h *Prompt) manualForCommandInFocus() (man Manual, ok bool) {
	// it's ok to wait here, since the list of
	// commands is short and pre-determined
	h.list.Wait()
	m, ok := h.list.Focus()
	if !ok {
		h.log(log.TraceLevel, "could not get search list focus")
		return
	}
	cmd := m.Data()
	man, ok = h.getManualForCommand(string(cmd))
	if !ok {
		h.log(log.TraceLevel, "could not find manual for top of search list command %q", cmd)
	}
	return
}

func (h *Prompt) getManualForCommand(cmd string) (Manual, bool) {
	return getManualForCommand(cmd, h.commandsBackup)
}

func (h *Prompt) manualCompleter(
	ctx context.Context, cmd string, args ...string,
) iterator.Iterator[string] {
	return manualCompleter(ctx, h.commandsBackup, h.mode, cmd, args...)
}

func (h *Prompt) getSeparatorHeight() int {
	if h.config.FrameCharSet == (component.FrameCharSet{}) {
		return 0
	}
	return defaultSeparatorHeight
}

func (h *Prompt) cancelPreview() {
	cancel := h.detachPreview()
	if cancel != nil {
		cancel()
	}
}

// detachPreview clears the preview fields and returns the pending
// cancel callback (if any). The cancel may synchronously re-enter
// the prompt through the GUI resize pipeline (e.g. theme preview),
// so callers invoke it after the prompt state has been reset.
func (h *Prompt) detachPreview() func() {
	cancel := h.preview
	h.preview = nil
	h.previewComponent = nil
	h.previewMatch = nil
	return cancel
}

func (h *Prompt) setPreview(comp component.Responsive, data []byte, cancel func()) {
	h.previewComponent = comp
	h.previewMatch = data
	if comp != nil && h.manualComponent != nil {
		h.setManualComponent(h.previewComponent)
	}
	if cancel == nil {
		return
	}
	if h.preview != nil {
		prev := h.preview
		h.preview = func() {
			cancel()
			prev()
		}
	} else {
		h.preview = cancel
	}
}

func (h *Prompt) setManualComponent(man component.Responsive) {
	h.manualComponent = man
	h.list.Resize(h.width, h.listRegionHeight())
}

func (h *Prompt) resetManualComponent() {
	h.previewComponent = nil
	h.previewMatch = nil
	if h.manualComponent == nil {
		return
	}
	h.setManualComponent(h.buildManualComponent(h.inputString.Load().(string)))
}

func newNopAnimation(cfg Config) tui.Component {
	return component.WithBackground(component.Nop(),
		term.NewCell(0, 0, cfg.ElementAttr))
}

func getManualForCommand(cmd string, commandsBackup []Manual) (Manual, bool) {
	for _, man := range commandsBackup {
		if man.Name == cmd {
			return man, true
		}
	}
	return Manual{}, false
}

// keep Prompt state as arguments, so we can
// better manage concurrent access to them
func manualCompleter(
	_ context.Context, commandsBackup []Manual,
	mode commandPromptMode, cmd string, args ...string,
) iterator.Iterator[string] {
	man, ok := getManualForCommand(cmd, commandsBackup)
	if !ok || cmd == "" {
		return iterator.FromSlice[string](nil)
	}

	args = SplitCommandLine(strings.TrimSpace(strings.Join(args, " ")))
	// return iterator with submcommands,
	// if first command hasn't been fully typed yet
	if len(args) == 0 || (len(args) == 1 && mode < 2) {
		return iterator.Map(iterator.FromSlice(man.Commands), manualToName)
	}

	// use mode to know if user has already completed
	// last arg or not.
	cmdAndArgsLen := len(args) + 1
	if int(mode) < cmdAndArgsLen {
		// trim last argument, since it hasn't been completed yet
		args = args[:len(args)-1]
	}

	// if we find the last argument's subcommand manual,
	// then use that as the completion args, otherwise just
	// do not return any completion args.
	lastArg := args[len(args)-1]
	if man, ok := getSubcommandManual(man, args); ok && man.Name == lastArg {
		return iterator.Map(iterator.FromSlice(man.Commands), manualToName)
	}
	return iterator.FromSlice[string](nil)
}
