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

package standard

import (
	"context"
	"strings"

	"github.com/ernestrc/logd-go/logging"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/registerhistory"
	"unstable.build/rune/internal/text/registerset"
)

var _ component.Scrollable = (*standardHandler)(nil)

const standardMarkLocationListID = "mark"

type standardHandler struct {
	cfg              standardConfig
	buf              *cell.Buffer
	less             handler.Less
	statusBar        statusBar
	pasteBuf         strings.Builder
	pasteStarted     bool
	resource         workspaceapi.URI
	cursor           text.Cursor
	anchor           term.Coordinates
	lastCursor       term.Coordinates
	height           int
	mouse            *mouse.Mouse
	clipboard        clipboard.Register
	macroRecorder    MacroRecorder
	macroPlayer      MacroPlayer
	pendingSetCursor *term.Coordinates
	lastIterateWord  term.Coordinates
	setLocations     bool
	metaK            bool
	lastPaste        bool
	historyIdx       int
	find             findState
}

// NewHandler returns a standard, simple-to-use text.Handler. indentTabspaces
// is the number of spaces per indent level when indentRune is IndentRuneSpace;
// pass 0 to fall back to the editor's configured tabspaces.
func NewHandler(
	buf *cell.Buffer, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) text.Handler {
	ret := new(standardHandler)
	ret.Init(buf, resource, indentRune, indentTabspaces, opts...)
	return ret
}

func (h *standardHandler) Init(
	buf *cell.Buffer, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) {
	h.cfg = defaultConfig()
	h.cfg.indentRune = indentRune
	h.cfg.indentTabspaces = indentTabspaces
	for _, o := range opts {
		o(&h.cfg)
	}
	h.buf = buf
	h.resource = resource
	h.less.InitWithBuffer(buf, handler.LessConfig{
		Wrap:               h.cfg.wrap,
		NoBar:              !h.cfg.commandBar,
		SuperimposeMessage: true,
		ResAttr:            h.cfg.search.MatchAttr,
		Attributes:         h.cfg.attr,
	})
	h.less.Scroll().SetTabspaces(h.cfg.tabspaces)
	h.cursor.Init(h.less.Scroll(), h.cfg.scheduleNextTick)
	h.anchor = h.cursor.CursorAtScroll()
	h.lastCursor = h.anchor
	if spec, ok := text.CommentSpecForURI(resource, h.cfg.comments); ok {
		h.cursor.SetCommentSpec(spec)
	}
	h.mouse = mouse.New(text.CursorMouseDelegate(&h.cursor))
	h.clipboard = h.cfg.clipboard
	h.macroRecorder = h.cfg.macroRecorder
	h.macroPlayer = h.cfg.macroPlayer
	h.statusBar = nopBar{}
	h.lastIterateWord.X = -1
	if h.cfg.enableInitialFolds {
		h.hideInitialFolds()
	}
}

// Resize satisfies tui.Component
func (h *standardHandler) Resize(width, height int) {
	h.height = height
	h.less.Resize(width, height)
	if h.pendingSetCursor != nil {
		h.SetCursorAtScroll(*h.pendingSetCursor)
		h.pendingSetCursor = nil
	}
}

func (h *standardHandler) setActiveLocationListMessage(locs []textapi.Location) {
	// NOTE: if there are multiple location lists with a message
	// in current cursor position, then there's no guarantee of which one
	// is going to be rendered.
	for _, loc := range locs {
		if loc.Message != "" {
			h.less.SetMessage("%s", loc.Message)
			return
		}
	}
	h.less.SetMessage("")
}

func (h *standardHandler) drawLocationMessage() {
	locs, ok := h.cursor.LocationsAtCursor()
	if ok {
		h.setActiveLocationListMessage(locs)
		h.setLocations = true
	} else if h.setLocations {
		h.less.SetMessage("")
		h.setLocations = false
	}
}

// Draw satisfies tui.Component
func (h *standardHandler) Draw(w term.Writer) {
	h.drawLocationMessage()
	h.less.Draw(w)
	text.DrawLocations(h.cursor.SortedLocations(), h.less.Scroll(), w)
}

func (h *standardHandler) setMarkLocation() bool {
	locs := h.markLocations()
	pos := h.cursor.CursorAtScroll()
	locs = append(locs, h.newMarkLocation(pos))
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, standardMarkLocationListID,
		text.LocationSlice(locs))
	return true
}

func (h *standardHandler) newMarkLocation(pos term.Coordinates) textapi.Location {
	return textapi.Location{
		From: pos,
		To:   term.Coordinates{Y: pos.Y, X: pos.X + 1},
		Attr: term.Attributes{Bg: term.ColorGray},
	}
}

func (h *standardHandler) markLocations() []textapi.Location {
	for _, list := range h.cursor.LocationLists() {
		if list.ID != standardMarkLocationListID {
			continue
		}
		locs := make([]textapi.Location, len(list.Locations))
		copy(locs, list.Locations)
		return locs
	}
	return nil
}

func (h *standardHandler) markLocation() (textapi.Location, bool) {
	locs := h.markLocations()
	if len(locs) == 0 {
		return textapi.Location{}, false
	}
	return locs[len(locs)-1], true
}

func (h *standardHandler) clearMarkLocation() bool {
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, standardMarkLocationListID, nil)
	return true
}

func (h *standardHandler) selectToMark(delete bool) bool {
	loc, ok := h.markLocation()
	if !ok {
		return false
	}
	from := loc.From
	to := h.cursor.CursorAtScroll()
	if delete {
		from, to = to, from
	}
	if !h.cursor.SelectRange(from, to) {
		return false
	}
	if !delete {
		return true
	}
	if !h.cursor.DeleteSelection() {
		return false
	}
	h.popMarkLocation()
	return true
}

func (h *standardHandler) popMarkLocation() bool {
	locs := h.markLocations()
	if len(locs) == 0 {
		return false
	}
	locs = locs[:len(locs)-1]
	if len(locs) == 0 {
		return h.clearMarkLocation()
	}
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, standardMarkLocationListID,
		text.LocationSlice(locs))
	return true
}

func (h *standardHandler) swapWithMark() bool {
	locs := h.markLocations()
	if len(locs) == 0 {
		return false
	}
	loc := locs[len(locs)-1]
	prev := h.cursor.CursorAtScroll()
	if _, ok := h.cursor.MoveToScroll(loc.From); !ok {
		return false
	}
	locs[len(locs)-1] = h.newMarkLocation(prev)
	h.cursor.SetLocationList(textapi.LocationPriorityInfo, standardMarkLocationListID,
		text.LocationSlice(locs))
	return true
}

func (h *standardHandler) playMacro() bool {
	if h.macroPlayer == nil {
		return false
	}
	if err := h.macroPlayer.Play(registerset.UnnamedRegisterID, 1); err != nil {
		h.log(log.ErrorLevel, "macro playback: %v", err)
	}
	return true
}

func (h *standardHandler) handleMetaK(ev term.Event) (handled bool) {
	h.log(log.TraceLevel, "handle metak, event: %#v", ev)
	if ev.Mod != term.ModMeta {
		return
	}
	switch ev.Key {
	case term.KeyBackspace:
		if h.cursor.Select() {
			h.cursor.MoveStartLine()
			handled = h.cursor.DeleteSelection()
		}
	case term.KeySpace:
		handled = h.setMarkLocation()
	}
	if handled {
		return
	}
	switch ev.Ch {
	case 'a':
		handled = h.selectToMark(false)
	case 'w':
		handled = h.selectToMark(true)
	case 'x':
		handled = h.swapWithMark()
	case 'g':
		handled = h.clearMarkLocation()
	case 'u':
		handled = h.cursor.UppercaseSelection()
	case 'l':
		handled = h.cursor.LowercaseSelection()
	case 'k':
		if h.cursor.Select() {
			h.cursor.MoveEndLine()
			handled = h.cursor.DeleteSelection()
		}
	case 'j':
		handled = h.cursor.ExpandAllFolds(context.Background())
	case '1':
		handled = h.cursor.CollapseAllFolds(context.Background())
	}
	return
}

// linuxChord rewrites a Linux chord into the macOS chord for the same action
// so both keymaps share one set of actions. It reports false for a <meta>
// chord, which the Linux keymap leaves to Rune's command layer.
func (h *standardHandler) linuxChord(ev term.Event) (term.Event, bool) {
	if ev.Mod&term.ModMeta != 0 {
		return ev, false
	}
	if h.metaK {
		if ev.Mod != term.ModCtrl {
			return ev, true
		}
		// The macOS keymap reaches these three without the prefix.
		switch ev.Ch {
		case 'c':
			h.metaK = false
			ev.Ch = 'l'
		case 'v', 'q':
			h.metaK = false
			ev.Mod = term.ModAltMeta
		default:
			ev.Mod = term.ModMeta
		}
		return ev, true
	}
	switch ev.Mod {
	case term.ModCtrl:
		switch ev.Ch {
		case 'k', 'l', 'j', 'J', 'u', 'U', 'D', 'V':
			ev.Mod = term.ModMeta
		case '?':
			ev.Mod, ev.Ch = term.ModAltMeta, '/'
		case 'W':
			ev.Mod = term.ModCtrlShift
		}
	case term.ModCtrlAlt:
		if ev.Key == 0 && ev.Ch == 'd' {
			ev.Mod = term.ModCtrlMeta
		}
	case term.ModCtrlShift:
		switch ev.Key {
		case term.KeySpace:
			ev.Mod = term.ModShiftMeta
		case term.KeyBackspace, term.KeyDelete:
			ev.Mod = term.ModMeta
		}
	}
	return ev, true
}

func (h *standardHandler) Handle(ev term.Event) (exit, handled bool) {
	ctx := context.Background()

	// A buttonless motion, a hover or the pointer leaving the window, is not
	// a command: it must not end a paste-history cycle or drop a pending
	// cursor position.
	if ev.Type == term.EventMouse && ev.Key == 0 {
		term.SetPointerShape(ev, term.PointerText)
		return h.mouse.Handle(ev)
	}

	// Track whether the current event is a paste-related action.
	// Reset lastPaste at the end unless the handler explicitly sets it.
	pastedThisTurn := false
	defer func() {
		if !pastedThisTurn {
			h.lastPaste = false
			h.historyIdx = 0
		}
	}()

	// only a user event clears a pending set cursor
	h.pendingSetCursor = nil

	switch ev.Type {
	case term.EventMouse:
		term.SetPointerShape(ev, term.PointerText)
		return h.mouse.Handle(ev)
	case term.EventPasteStart:
		h.pasteBuf.Reset()
		h.pasteStarted = true
		handled = true
		return
	case term.EventPasteEnd:
		str := h.pasteBuf.String()
		h.selectLinesAsRange()
		if _, ok := h.cursor.SelectionMode(); ok {
			// Pasting over a selection replaces it with the pasted text.
			h.buf.MarkStartUndo()
			h.cursor.DeleteSelection()
			h.cursor.Unselect()
			h.cursor.InsertString(str)
			h.buf.GroupUndo()
		} else {
			h.cursor.InsertString(str)
		}
		handled = true
		h.pasteStarted = false
		return
	case term.EventKey:
	default:
		return
	}

	if h.pasteStarted {
		if ev.Ch != 0 {
			h.pasteBuf.WriteRune(ev.Ch)
			handled = true
		}
		return
	}
	if current := h.cursor.CursorAtScroll(); current != h.lastCursor {
		h.anchor = current
	}

	moveToBoundsPending := true
	defer func() {
		if moveToBoundsPending {
			_ = h.doMoveToBounds()
		}
	}()
	if h.find.active {
		if !h.handleFindKey(ev) {
			return false, true
		}
	}

	if ev.Mod == term.ModShift {
		switch ev.Key {
		case term.KeyTab:
			if _, ok := h.cursor.SelectionMode(); ok {
				if handled = h.cursor.ShiftSelectionLeft(h.cfg.indentRune, h.cfg.indentTabspaces); handled {
					h.cursor.Unselect()
				}
			} else {
				handled = h.cursor.ShiftLineLeft(h.cfg.indentRune, h.cfg.indentTabspaces)
			}
			return
		}
	}

	var shift bool
	if ev.Mod&term.ModShift != 0 && ev.Mod == term.ModShift {
		if _, ok := h.cursor.SelectionMode(); !ok {
			h.cursor.Select()
		}
		ev.Mod = ev.Mod &^ term.ModShift
		shift = true
	}

	if h.cfg.keymap == KeymapLinux {
		var ok bool
		if ev, ok = h.linuxChord(ev); !ok {
			return
		}
	}

	// if only Mod is pressed, then user might be
	// preparing to fire next key/ch.
	if h.metaK && (ev.Key != 0 || ev.Ch != 0) {
		h.metaK = false
		handled = h.handleMetaK(ev)
		if handled {
			return
		}
	}

	cursorAt := h.cursor.CursorAtScroll()
	switch ev.Mod {
	case term.ModAltShift:
		switch ev.Key {
		case term.KeyArrowDown:
			handled = h.duplicateLine(false /* down */)
			return
		case term.KeyArrowUp:
			handled = h.duplicateLine(true /* up */)
			return
		case term.KeyArrowLeft:
			handled = h.moveWord(false /* left */, true /* extend */)
			return
		case term.KeyArrowRight:
			handled = h.moveWord(true /* right */, true /* extend */)
			return
		case 0:
			switch ev.Ch {
			case 'B':
				handled = h.moveWord(false /* left */, true /* extend */)
				return
			case 'F':
				handled = h.moveWord(true /* right */, true /* extend */)
				return
			}
		}
	case term.ModAltMeta:
		switch ev.Key {
		case 0:
			switch ev.Ch {
			case '[':
				handled = h.cursor.CollapseFold(context.Background())
			case ']':
				handled = h.cursor.ExpandFold(context.Background())
			case '/':
				handled = h.cursor.ToggleBlockComment()
			case 'q':
				handled = h.cursor.WrapParagraph(h.cfg.ruler)
			case 'v':
				if handled = h.pasteFromHistory(); handled {
					pastedThisTurn = true
				}
			}
		}
	case term.ModCtrlMeta:
		switch ev.Key {
		case 0:
			switch ev.Ch {
			case 'd':
				handled = h.selectPrevWordAtCursor()
				return
			}
		}
	case term.ModShiftMeta:
		switch ev.Key {
		case term.KeySpace:
			handled = h.cursor.ExpandSelection(ctx)
		case term.KeyArrowLeft:
			if _, ok := h.cursor.SelectionMode(); !ok {
				h.cursor.Select()
			}
			handled = h.cursor.MoveStartLine()
			return
		case term.KeyArrowRight:
			if _, ok := h.cursor.SelectionMode(); !ok {
				h.cursor.Select()
			}
			handled = h.cursor.MoveEndLine()
			return
		case term.KeyArrowUp:
			if _, ok := h.cursor.SelectionMode(); !ok {
				h.cursor.Select()
			}
			h.restoreDesiredColumn()
			handled = h.cursor.MoveFirstLine()
			return
		case term.KeyArrowDown:
			if _, ok := h.cursor.SelectionMode(); !ok {
				h.cursor.Select()
			}
			h.restoreDesiredColumn()
			handled = h.cursor.MoveLastLine()
			return
		}
	case term.ModAlt:
		switch ev.Key {
		case term.KeyArrowDown:
			handled = h.moveLine(false /* down */)
			return
		case term.KeyArrowUp:
			handled = h.moveLine(true /* up */)
			return
		case term.KeyArrowLeft:
			handled = h.moveWord(false /* left */, false /* extend */)
		case term.KeyArrowRight:
			handled = h.moveWord(true /* right */, false /* extend */)
		case term.KeyBackspace:
			handled = h.deleteWord(false /* left */)
		case term.KeyDelete:
			handled = h.deleteWord(true /* right */)
		case 0:
			switch ev.Ch {
			case 'z':
				h.SetWrap(!h.less.Scroll().Wrap)
				handled = true
			}
		}
	case term.ModMeta:
		switch ev.Key {
		case term.KeyArrowLeft:
			handled = h.cursor.MoveStartLine()
		case term.KeyArrowRight:
			handled = h.cursor.MoveEndLine()
		case term.KeyArrowUp:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveFirstLine()
		case term.KeyArrowDown:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveLastLine()
		case term.KeyBackspace:
			if h.cursor.Select() {
				h.cursor.MoveStartLine()
				handled = h.cursor.DeleteSelection()
			}
			return
		case term.KeyDelete:
			if ok := h.cursor.Select(); ok {
				h.cursor.MoveEndLine()
				handled = h.cursor.DeleteSelection()
			}
		case 0:
			switch ev.Ch {
			case 'k':
				h.log(log.TraceLevel, "waiting for metaK event")
				h.metaK = true
				handled = true
				// The prefix must not end a paste-history cycle.
				pastedThisTurn = h.lastPaste
			case 'u':
				handled = h.cursor.UndoSelection()
				return
			case 'U':
				handled = h.cursor.RedoSelection()
				return
			case 'j':
				handled = h.cursor.Conflate()
			case '/':
				handled = h.cursor.ToggleLineComment()
				// the toggle carries the selection along with the caret
				return
			case ']':
				if _, ok := h.cursor.SelectionMode(); ok {
					h.cursor.ShiftSelectionRight(h.cfg.indentRune, h.cfg.indentTabspaces)
				} else {
					h.cursor.ShiftLineRight(h.cfg.indentRune, h.cfg.indentTabspaces)
				}
				handled = true
			case '[':
				if _, ok := h.cursor.SelectionMode(); ok {
					handled = h.cursor.ShiftSelectionLeft(h.cfg.indentRune, h.cfg.indentTabspaces)
				} else {
					handled = h.cursor.ShiftLineLeft(h.cfg.indentRune, h.cfg.indentTabspaces)
				}
			case 'l':
				handled = h.expandLineSelection()
				// avoid unselect due to not shift
				return
			case 'D':
				handled = h.duplicateLine(false /* down */)
				return
			case 'J':
				handled = h.cursor.SelectIndentationLevel(h.cfg.indentTabspaces)
				return
			case 'd':
				handled = h.selectNextWordAtCursor()
				return
			case 'x':
				handled = h.cut()
			case 'f':
				handled = h.startFind()
			case 'a':
				handled = h.selectAll()
				return
			// the following two are defined here in case we're not capturing them at the command level
			case 'c':
				handled = h.copySelectionOrLine()
				// copying a line selection moves the caret to the end of the
				// range it becomes, which must not drop the selection
				return
			case 'v':
				paste, err := h.clipboard.Paste(clipboard.DefaultRegisterID)
				if err != nil {
					h.log(log.ErrorLevel, "clipboard paste: %v", err)
				} else {
					h.paste(paste)
					handled = true
					h.lastPaste = true
					h.historyIdx = 0
					pastedThisTurn = true
				}
			case 'V':
				handled = h.pasteAndReindent()
			case 'z':
				handled = h.cursor.Undo()
			case 'y':
				handled = h.cursor.Redo()
			case 'Z':
				handled = h.cursor.Redo()
			case 'K':
				handled = h.deleteLines()
			}
		}
	// no modifier
	case 0:
		switch ev.Key {
		case term.KeyEsc:
			handled = h.cursor.Unselect()
			h.cursor.Search("")
		case term.KeyEnd:
			handled = h.cursor.MoveEndLine()
		case term.KeyHome:
			handled = h.cursor.MoveStartLine()
		case term.KeyPgup:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveUpLines(h.less.Scroll().SizeHeight())
			if handled {
				h.cursor.RepositionBottom()
			}
		case term.KeyPgdn:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveDownLines(h.less.Scroll().SizeHeight())
			if handled {
				h.cursor.RepositionTop()
			}
		case term.KeyArrowLeft:
			handled = h.cursor.MoveLeft()
		case term.KeyArrowRight:
			handled = h.cursor.MoveRight()
		case term.KeyArrowUp:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveUp()
			if shift && !handled {
				handled = h.cursor.MoveStartLine()
			}
		case term.KeyArrowDown:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveDown()
			if shift && !handled {
				handled = h.cursor.MoveEndLine()
			}
		case term.KeyEnter:
			h.selectLinesAsRange()
			if _, ok := h.cursor.SelectionMode(); ok {
				h.cursor.DeleteSelection()
				h.cursor.Unselect()
			}
			if h.cfg.autoPair {
				h.cursor.InsertAutoPairNewline(h.cfg.indentRune, h.cfg.indentTabspaces)
			} else {
				h.cursor.InsertWithIndentRune('\n', h.cfg.indentRune, h.cfg.indentTabspaces)
			}
			handled = true
		case term.KeySpace:
			h.selectLinesAsRange()
			if _, ok := h.cursor.SelectionMode(); ok {
				h.cursor.DeleteSelection()
				h.cursor.Unselect()
			}
			h.cursor.InsertWithIndentRune(' ', h.cfg.indentRune, h.cfg.indentTabspaces)
			handled = true
		case term.KeyTab:
			if _, ok := h.cursor.SelectionMode(); ok {
				h.cursor.ShiftSelectionRight(h.cfg.indentRune, h.cfg.indentTabspaces)
				h.cursor.Unselect()
			} else {
				if !h.cursor.TryIndent(h.cfg.indentRune, h.cfg.indentTabspaces) {
					h.cursor.InsertWithIndentRune(
						h.cfg.indentRune, h.cfg.indentRune, h.cfg.indentTabspaces)
				}
			}
			handled = true
		case term.KeyBackspace:
			h.selectLinesAsRange()
			if _, ok := h.cursor.SelectionMode(); ok {
				handled = h.cursor.DeleteSelection()
				h.cursor.Unselect()
			} else if h.cfg.autoPair {
				handled = h.cursor.BackspaceAutoPair()
			} else {
				handled = h.cursor.Backspace()
			}
		case term.KeyDelete:
			h.selectLinesAsRange()
			if _, ok := h.cursor.SelectionMode(); ok {
				handled = h.cursor.DeleteSelection()
				h.cursor.Unselect()
			} else {
				handled = h.cursor.Delete()
			}
		default:
			if ev.Ch != 0 {
				h.selectLinesAsRange()
				if _, ok := h.cursor.SelectionMode(); ok {
					// Like RET, SPC and paste, a typed character replaces
					// the selection rather than just deleting it.
					h.cursor.DeleteSelection()
					h.cursor.Unselect()
				}
				if h.cfg.autoPair {
					handled = h.cursor.InsertWithAutoPair(ev.Ch, h.cfg.indentRune, h.cfg.indentTabspaces)
				} else {
					h.cursor.InsertWithIndentRune(ev.Ch, h.cfg.indentRune, h.cfg.indentTabspaces)
					handled = true
				}
			}
		}
	case term.ModCtrl:
		switch ev.Key {
		case term.KeyEnter:
			h.cursor.InsertLineBelow(h.cfg.indentRune, h.cfg.indentTabspaces)
			handled = true
			return
		case term.KeyArrowLeft:
			handled = h.moveWord(false /* left */, false /* extend */)
			return
		case term.KeyArrowRight:
			handled = h.moveWord(true /* right */, false /* extend */)
			return
		case term.KeyArrowUp:
			h.restoreDesiredColumn()
			handled = h.cursor.MovePrevParagraph()
			return
		case term.KeyArrowDown:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveNextParagraph()
			return
		case term.KeyHome:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveFirstLine()
			return
		case term.KeyEnd:
			h.restoreDesiredColumn()
			handled = h.cursor.MoveLastLine()
			return
		case term.KeyBackspace:
			handled = h.deleteWord(false /* left */)
			return
		case term.KeyDelete:
			handled = h.deleteWord(true /* right */)
			return
		}
		switch ev.Ch {
		case 'f':
			handled = h.startFind()
		case 'c':
			handled = h.copySelectionOrLine()
			// see the macOS copy above
			return
		case 'x':
			handled = h.cut()
		case 'v':
			paste, err := h.clipboard.Paste(clipboard.DefaultRegisterID)
			if err != nil {
				h.log(log.ErrorLevel, "clipboard paste: %v", err)
			} else {
				h.paste(paste)
				handled = true
				h.lastPaste = true
				h.historyIdx = 0
				pastedThisTurn = true
			}
		case 'l':
			handled = h.cursor.Center()
			return
		case 'm':
			handled = h.cursor.MoveToMatchingRune()
		case 't':
			handled = h.cursor.TransposeChars()
		case 'k':
			handled = h.cutToEndOfLine()
			return
		case 'M':
			handled = h.cursor.SelectABlockClose('(', ')') ||
				h.cursor.SelectABlockClose('{', '}') ||
				h.cursor.SelectABlockClose('[', ']')
		case 'd':
			handled = h.selectNextWordAtCursor()
			return
		case 'a':
			handled = h.selectAll()
			return
		case 'q':
			if h.macroRecorder != nil {
				if h.macroRecorder.IsRecording() {
					h.macroRecorder.Stop()
				} else {
					h.macroRecorder.Start(registerset.UnnamedRegisterID)
				}
				handled = true
			}
		case 'Q':
			handled = h.playMacro()
		case 'z':
			handled = h.cursor.Undo()
		case 'y':
			handled = h.cursor.Redo()
		case '/':
			handled = h.cursor.ToggleLineComment()
			// see the macOS toggle above
			return
		case '[':
			if _, ok := h.cursor.SelectionMode(); ok {
				handled = h.cursor.ShiftSelectionLeft(h.cfg.indentRune, h.cfg.indentTabspaces)
			} else {
				handled = h.cursor.ShiftLineLeft(h.cfg.indentRune, h.cfg.indentTabspaces)
			}
		case ']':
			if _, ok := h.cursor.SelectionMode(); ok {
				h.cursor.ShiftSelectionRight(h.cfg.indentRune, h.cfg.indentTabspaces)
			} else {
				h.cursor.ShiftLineRight(h.cfg.indentRune, h.cfg.indentTabspaces)
			}
			handled = true
		case '{':
			handled = h.cursor.CollapseFold(ctx)
		case '}':
			handled = h.cursor.ExpandFold(ctx)
		case 'A':
			handled = h.cursor.ToggleAllFolds(ctx)
		case 'Z':
			handled = h.cursor.Redo()
		case 'J':
			handled = h.cursor.Conflate()
		case 'K':
			handled = h.deleteLines()
		case '-':
			handled = h.cursor.MoveToNextMatch()
		case '_':
			handled = h.cursor.MoveToPrevMatch()
		case 'w':
			handled = h.cursor.ExpandSelection(ctx)
		}
	case term.ModCtrlShift:
		switch ev.Key {
		case term.KeyEnter:
			h.cursor.InsertLineAbove(h.cfg.indentRune, h.cfg.indentTabspaces)
			handled = true
			return
		case term.KeyArrowLeft:
			handled = h.cursor.ShrinkSelection()
			return
		case term.KeyArrowRight:
			handled = h.cursor.ExpandSelection(ctx)
			return
		case term.KeyArrowUp:
			if _, ok := h.cursor.SelectionMode(); !ok {
				h.cursor.Select()
			}
			h.restoreDesiredColumn()
			handled = h.cursor.MovePrevParagraph()
			return
		case term.KeyArrowDown:
			if _, ok := h.cursor.SelectionMode(); !ok {
				h.cursor.Select()
			}
			h.restoreDesiredColumn()
			handled = h.cursor.MoveNextParagraph()
			return
		case term.KeyHome:
			if _, ok := h.cursor.SelectionMode(); !ok {
				h.cursor.Select()
			}
			h.restoreDesiredColumn()
			handled = h.cursor.MoveFirstLine()
			return
		case term.KeyEnd:
			if _, ok := h.cursor.SelectionMode(); !ok {
				h.cursor.Select()
			}
			h.restoreDesiredColumn()
			handled = h.cursor.MoveLastLine()
			return
		}
		switch ev.Ch {
		case 'Q':
			handled = h.playMacro()
		case 'V':
			handled = h.pasteAndReindent()
		case 'M':
			handled = h.cursor.SelectABlockClose('(', ')') ||
				h.cursor.SelectABlockClose('{', '}') ||
				h.cursor.SelectABlockClose('[', ']')
		case 'W':
			handled = h.cursor.ShrinkSelection()
		}
	case term.ModCtrlAlt:
		switch ev.Key {
		case term.KeyArrowUp:
			pos := h.cursor.CursorAtScroll()
			if handled = h.less.Scroll().SeekUp(); handled {
				win, ok := h.cursor.WindowCoordinates(pos)
				if ok && win.Y >= 0 {
					h.cursor.MoveToScroll(pos)
				}
			}
		case term.KeyArrowDown:
			pos := h.cursor.CursorAtScroll()
			if handled = h.less.Scroll().SeekDown(); handled {
				win, ok := h.cursor.WindowCoordinates(pos)
				if ok && win.Y >= 0 {
					h.cursor.MoveToScroll(pos)
				}
			}
		case 0:
			switch ev.Ch {
			case 'h':
				handled = h.cursor.HideSelection()
				h.cursor.Unselect()
			case 'v':
				handled = h.cursor.Unhide()
				h.cursor.Unselect()
			}
		}
	}

	cursorCorrected := h.doMoveToBounds()
	moveToBoundsPending = false

	// if moved and shift is not pressed
	if (cursorAt != h.cursor.CursorAtScroll() || cursorCorrected) && !shift {
		h.cursor.Unselect()
		h.cursor.Search("")
	}
	return
}

func (h *standardHandler) restoreDesiredColumn() {
	if h.cfg.cursorCorrections {
		h.cursor.MoveToScroll(h.anchor)
	}
}

func (h *standardHandler) doMoveToBounds() bool {
	if !h.cfg.cursorCorrections {
		return false
	}
	before := h.cursor.CursorAtScroll()
	if h.cursor.CursorAtScroll().Y < h.less.Buffer().Rows() {
		h.anchor = h.cursor.CursorAtScroll()
	}
	prevCoords := h.cursor.Coordinates()
	h.cursor.MoveToBounds(1)
	if h.cursor.Coordinates().Y != prevCoords.Y {
		h.anchor = h.cursor.CursorAtScroll()
	}
	h.lastCursor = h.cursor.CursorAtScroll()
	return before != h.lastCursor
}

// Cursor satisfies tui.Handler
func (h *standardHandler) Cursor() (
	pos term.Coordinates, style term.CursorStyle, show bool,
) {
	return h.cursor.Coordinates(), term.CursorStyleSteadyBar, true
}

// Selection satisfies tui.Handler.
func (h *standardHandler) Selection() (string, bool) {
	text := h.cursor.Selection()
	return text, text != ""
}

// SelectionBounds returns the sorted, half-open [from, to) buffer
// range covered by the active selection highlight, if any.
func (h *standardHandler) SelectionBounds() (from, to term.Coordinates, ok bool) {
	return h.cursor.SelectionRange()
}

// Close satisfies editor.Handler.
func (h *standardHandler) Close() error {
	return nil
}

// Resource satisfies editor.Handler.
func (h *standardHandler) Resource() workspaceapi.URI {
	return h.resource
}

// SetWrap satisfies editor.Handler.
func (h *standardHandler) SetWrap(wrap bool) {
	h.less.Scroll().Wrap = wrap
}

func (h *standardHandler) IsSearchMode() bool {
	return h.find.active
}

func (h *standardHandler) IsNormalMode() bool { return false }

func (t *standardHandler) ShowCommandBar(show bool) {
	t.less.ShowCommandBar(show)
}

func (h *standardHandler) SetCursorAtScroll(pos term.Coordinates) bool {
	// setCursor should be robust against resizes, etc.
	// only the first client interaction should clear this position
	if h.less.Scroll().Width() == 0 || h.less.Scroll().SizeHeight() == 0 {
		h.pendingSetCursor = new(term.Coordinates)
		*h.pendingSetCursor = pos
		return false
	}

	pos = h.clampToBuffer(pos)
	_, ok := h.cursor.MoveToScroll(pos)
	h.anchor = h.cursor.CursorAtScroll()
	h.lastCursor = h.anchor
	if !ok || !h.cfg.autoCenter {
		return ok
	}
	h.cursor.Center()
	return true
}

// clampToBuffer pulls a requested scroll position inside the buffer so an
// out-of-range request (e.g. a restored session cursor pointing past a file that
// shrank on disk) lands on the nearest valid cell instead of stranding the
// caret on a non-existent row or column.
func (h *standardHandler) clampToBuffer(pos term.Coordinates) term.Coordinates {
	view := h.buf.View()
	rows := view.Rows()
	if rows == 0 {
		return term.Coordinates{}
	}
	pos.Y = max(0, min(pos.Y, rows-1))
	pos.X = max(0, min(pos.X, view.Columns(pos.Y)))
	return pos
}

func (h *standardHandler) SeekUp() bool {
	return h.less.Scroll().SeekUp()
}

func (h *standardHandler) SeekDown() bool {
	return h.less.Scroll().SeekDown()
}

func (h *standardHandler) SeekOffset() int {
	return h.less.Scroll().SeekOffset()
}

func (h *standardHandler) MaxSeekOffset() int {
	return h.less.Scroll().MaxSeekOffset()
}

func (h standardHandler) SetLocationList(
	pri textapi.LocationPriority, ID string, loc text.LocationList,
) {
	h.cursor.SetLocationList(pri, ID, loc)
}

func (h *standardHandler) MoveToNextLocation(ID string) bool {
	return h.cursor.MoveToNextLocation(ID)
}

func (h *standardHandler) MoveToPrevLocation(ID string) bool {
	return h.cursor.MoveToPrevLocation(ID)
}

func (h *standardHandler) CellView() cell.View {
	return h.buf.View()
}

func (h *standardHandler) CellEditor() cell.Editor {
	return text.ExternalEditor(&h.cursor, h.buf.Editor())
}

// Dimensions satisfies text.Handler (and component.Floating).
// standardHandler is a leaf handler with no sidebar chrome, so the
// ideal size is the widest row in the underlying buffer by the
// buffer row count. Bar-wrapping handlers add their own
// contribution on top. See text.ViewDimensions for why this must
// sum each cell's Width rather than use View.Columns.
func (h *standardHandler) Dimensions() (int, int) {
	return text.ViewDimensions(h.buf.View())
}

func (e *standardHandler) SetDefaultAttributes(attr term.Attributes) {
	e.less.Scroll().Attributes = attr
}

func (h *standardHandler) CursorAtScroll() term.Coordinates {
	return h.cursor.CursorAtScroll()
}

func (h *standardHandler) LocationLists() []text.LocationSet {
	return h.cursor.LocationLists()
}

// copySelectionOrLine copies the active selection, keeping it, or the current
// line when there is no selection, matching the copy behavior of VS Code,
// Sublime Text, and Zed. It copies what cut would remove so the two operations
// stay symmetric.
func (h *standardHandler) copySelectionOrLine() (handled bool) {
	h.selectLinesAsRange()
	if _, ok := h.cursor.SelectionMode(); !ok {
		at, _ := h.caret()
		return h.copyLine(at.Y)
	}
	_, err := h.cursor.CopySelectionNoUnselect(clipboard.DefaultRegisterID, h.clipboard)
	if err != nil {
		h.log(log.ErrorLevel, "cursor copy selection: %v", err)
		return
	}
	return true
}

// copyLine copies line y as a whole line, the way VS Code copies with an
// empty selection: with its line break even when it is the last line or
// empty, so pasting it always adds a line.
func (h *standardHandler) copyLine(y int) bool {
	var line string
	if y < h.buf.Rows() {
		line = h.lineString(y)
	}
	err := h.clipboard.Copy(clipboard.DefaultRegisterID,
		clipboard.Data{Text: line + "\n", Metadata: text.LineSelection})
	if err != nil {
		h.log(log.ErrorLevel, "clipboard copy: %v", err)
		return false
	}
	return true
}

// cut is VS Code's cut: it copies and removes the selection or, without one,
// the caret's line together with the line break after it, or the one before
// it when it is the last line.
func (h *standardHandler) cut() bool {
	h.selectLinesAsRange()
	if _, ok := h.cursor.SelectionMode(); ok {
		_, err := h.cursor.CopySelectionNoUnselect(clipboard.DefaultRegisterID, h.clipboard)
		if err != nil {
			h.log(log.ErrorLevel, "cursor copy selection: %v", err)
			return false
		}
		return h.cursor.DeleteSelection()
	}
	at, _ := h.caret()
	if !h.copyLine(at.Y) {
		return false
	}
	rows := h.buf.Rows()
	if rows == 0 {
		return true
	}
	from := term.Coordinates{Y: at.Y}
	to := term.Coordinates{X: h.buf.Columns(at.Y), Y: at.Y}
	switch {
	case at.Y < rows-1:
		to = term.Coordinates{Y: at.Y + 1}
	case at.Y > 0:
		from = term.Coordinates{X: h.buf.Columns(at.Y - 1), Y: at.Y - 1}
	}
	h.replaceRange(from, to, "")
	return true
}

// deleteLines is VS Code's editor.action.deleteLines: it removes the lines
// the caret or selection is on together with the line break after them, or
// the one before them at the end of the buffer, and keeps the caret's column.
func (h *standardHandler) deleteLines() bool {
	start, end, ok := h.lineSpan()
	rows := h.buf.Rows()
	if !ok || (rows == 1 && h.buf.Columns(0) == 0) {
		return false
	}
	at, _ := h.caret()
	from := term.Coordinates{Y: start}
	to := term.Coordinates{X: h.buf.Columns(end), Y: end}
	switch {
	case end < rows-1:
		to = term.Coordinates{Y: end + 1}
	case start > 0:
		start--
		from = term.Coordinates{X: h.buf.Columns(start), Y: start}
	}
	h.replaceRange(from, to, "")
	y := min(start, h.buf.Rows()-1)
	h.cursor.MoveToScroll(term.Coordinates{X: min(at.X, h.buf.Columns(y)), Y: y})
	return true
}

// moveLine is VS Code's editor.action.moveLinesUpAction and
// moveLinesDownAction: the lines the caret or selection is on trade places
// with the line above or below, carrying the caret and selection along. It
// does nothing at the first or last line.
func (h *standardHandler) moveLine(up bool) bool {
	start, end, ok := h.lineSpan()
	if !ok || (up && start == 0) || (!up && end == h.buf.Rows()-1) {
		return false
	}
	block := h.linesString(start, end)
	dy := 1
	from := term.Coordinates{Y: start}
	to := term.Coordinates{X: h.buf.Columns(end + 1), Y: end + 1}
	swapped := h.lineString(end+1) + "\n" + block
	if up {
		dy = -1
		from = term.Coordinates{Y: start - 1}
		to = term.Coordinates{X: h.buf.Columns(end), Y: end}
		swapped = block + "\n" + h.lineString(start-1)
	}
	restore := h.shiftSelection(dy)
	h.replaceRange(from, to, swapped)
	restore()
	return true
}

// duplicateLine is VS Code's editor.action.copyLinesUpAction and
// copyLinesDownAction: the lines the caret or selection is on are repeated
// below themselves, and copying down carries the caret and selection to the
// copy. The clipboard is left alone.
func (h *standardHandler) duplicateLine(up bool) bool {
	start, end, ok := h.lineSpan()
	if !ok {
		return false
	}
	dy := end - start + 1
	if up {
		dy = 0
	}
	restore := h.shiftSelection(dy)
	eol := term.Coordinates{X: h.buf.Columns(end), Y: end}
	h.replaceRange(eol, eol, "\n"+h.linesString(start, end))
	restore()
	return true
}

// paste is VS Code's paste: it replaces the selection with the text, and
// without a selection a whole line goes above the caret's line, the caret
// staying on the same text.
func (h *standardHandler) paste(data clipboard.Data) {
	mode, _ := data.Metadata.(text.SelectMode)
	h.selectLinesAsRange()
	if _, ok := h.cursor.SelectionMode(); ok {
		h.buf.MarkStartUndo()
		h.cursor.DeleteSelection()
		h.cursor.InsertString(data.Text)
		h.buf.GroupUndo()
		return
	}
	at, _ := h.caret()
	h.cursor.Paste(data.Text, mode, false)
	if mode == text.LineSelection ||
		(mode == text.NoSelection && strings.HasSuffix(data.Text, "\n")) {
		h.cursor.MoveToScroll(term.Coordinates{X: at.X, Y: at.Y + strings.Count(data.Text, "\n")})
	}
}

// moveWord moves the caret to the start of the previous or the end of the
// next word, as VS Code's cursorWordStartLeft and cursorWordEndRight do,
// extending the selection when extend is set.
func (h *standardHandler) moveWord(right, extend bool) bool {
	at, ok := h.caret()
	if !ok {
		return false
	}
	if _, selected := h.cursor.SelectionMode(); extend && !selected {
		h.cursor.Select()
	}
	to := h.words().moveWordLeft(at)
	if right {
		to = h.words().moveWordRight(at)
	}
	_, moved := h.cursor.MoveToScroll(to)
	return moved
}

// deleteWord is VS Code's deleteWordLeft and deleteWordRight: it deletes the
// selection or, without one, from the caret to where the word deletion ends.
func (h *standardHandler) deleteWord(right bool) bool {
	h.selectLinesAsRange()
	if _, ok := h.cursor.SelectionMode(); ok {
		return h.cursor.DeleteSelection()
	}
	at, ok := h.caret()
	if !ok {
		return false
	}
	var to term.Coordinates
	if right {
		to, ok = h.words().deleteWordRight(at)
	} else {
		to, ok = h.words().deleteWordLeft(at)
	}
	if !ok {
		return false
	}
	h.replaceRange(at, to, "")
	return true
}

// selectLinesAsRange turns a line selection into the text VS Code's
// expandLineSelection covers: whole lines through the start of the line
// after them, or through the end of the last line, with the caret at that
// end. Replacing or cutting it then leaves the line break above the selection
// alone, where deleting the lines would take it. A selection of just the empty
// last line covers nothing and is dropped.
func (h *standardHandler) selectLinesAsRange() {
	if mode, ok := h.cursor.SelectionMode(); !ok || mode != text.LineSelection {
		return
	}
	from, to, ok := h.cursor.SelectionBounds()
	if !ok {
		return
	}
	from, to = term.CoordinatesSort(from, to)
	h.selectSpan(term.Coordinates{Y: from.Y}, h.lineAfter(to.Y))
}

// expandLineSelection is VS Code's expandLineSelection: it selects from the
// start of the first line the caret or selection is on through the start of
// the line after the last one, or the end of the buffer, with the caret at
// that end.
func (h *standardHandler) expandLineSelection() bool {
	from, ok := h.caret()
	if !ok {
		return false
	}
	to := from
	h.selectLinesAsRange()
	if f, t, selected := h.cursor.SelectionRange(); selected {
		from, to = h.clampToBuffer(f), h.clampToBuffer(t)
	}
	h.selectSpan(term.Coordinates{Y: from.Y}, h.lineAfter(to.Y))
	return true
}

// selectAll is VS Code's selectAll: the whole buffer, with the caret at its
// end.
func (h *standardHandler) selectAll() bool {
	rows := h.buf.Rows()
	if rows == 0 {
		return false
	}
	h.selectSpan(term.Coordinates{}, h.lineAfter(rows-1))
	return true
}

// selectSpan selects from anchor to head with the caret at head, as VS Code's
// selections are; an empty span leaves just the caret there.
func (h *standardHandler) selectSpan(anchor, head term.Coordinates) {
	if anchor == head {
		h.cursor.Unselect()
		h.cursor.MoveToScroll(head)
		return
	}
	h.cursor.SelectSpan(anchor, head)
}

// lineAfter is where a selection of whole lines ending with line y ends in VS
// Code: the start of the next line, or the end of the buffer after the last.
func (h *standardHandler) lineAfter(y int) term.Coordinates {
	if last := h.buf.Rows() - 1; y >= last {
		return term.Coordinates{X: h.buf.Columns(last), Y: last}
	}
	return term.Coordinates{Y: y + 1}
}

// lineSpan is the range of lines VS Code's line commands act on: the caret's
// line, or every line the selection covers except a last one it only reaches
// the start of.
func (h *standardHandler) lineSpan() (start, end int, ok bool) {
	at, ok := h.caret()
	if !ok {
		return
	}
	start, end = at.Y, at.Y
	mode, selected := h.cursor.SelectionMode()
	from, to, bounded := h.cursor.SelectionBounds()
	if !selected || !bounded {
		return
	}
	from, to = term.CoordinatesSort(h.clampToBuffer(from), h.clampToBuffer(to))
	start, end = from.Y, to.Y
	if mode != text.LineSelection && end > start && to.X == 0 {
		end--
	}
	return
}

// shiftSelection returns a function that puts the caret and selection, as
// they are now, back dy lines lower, so line commands carry them along with
// the lines they move.
func (h *standardHandler) shiftSelection(dy int) func() {
	at, _ := h.caret()
	mode, selected := h.cursor.SelectionMode()
	anchor, head, bounded := h.cursor.SelectionBounds()
	shift := func(pos term.Coordinates) term.Coordinates {
		pos.Y += dy
		return pos
	}
	return func() {
		h.cursor.Unselect()
		if !selected || !bounded {
			h.cursor.MoveToScroll(shift(at))
			return
		}
		h.cursor.MoveToScroll(shift(anchor))
		switch mode {
		case text.LineSelection:
			h.cursor.SelectLine()
		case text.BlockSelection:
			h.cursor.SelectBlock()
		default:
			h.cursor.Select()
		}
		h.cursor.MoveToScroll(shift(head))
	}
}

// replaceRange replaces [from, to) with s as one undo step, leaving the caret
// after s and no selection.
func (h *standardHandler) replaceRange(from, to term.Coordinates, s string) {
	from, to = term.CoordinatesSort(from, to)
	h.cursor.Unselect()
	h.buf.MarkStartUndo()
	if from == to || !h.cursor.SelectRange(from, to) {
		h.cursor.MoveToScroll(from)
	} else {
		h.cursor.DeleteSelection()
	}
	if s != "" {
		h.cursor.InsertString(s)
	}
	h.buf.GroupUndo()
}

// caret is the cursor clamped into the buffer: VS Code never leaves its
// cursor past a line end or below the last line, so its commands act from
// there. It reports false for a buffer without rows.
func (h *standardHandler) caret() (term.Coordinates, bool) {
	if h.buf.Rows() == 0 {
		return term.Coordinates{}, false
	}
	return h.clampToBuffer(h.cursor.CursorAtScroll()), true
}

func (h *standardHandler) lineString(y int) string {
	return cell.RowsToString([][]term.Cell{h.buf.Row(y)})
}

func (h *standardHandler) linesString(start, end int) string {
	lines := make([]string, 0, end-start+1)
	for y := start; y <= end; y++ {
		lines = append(lines, h.lineString(y))
	}
	return strings.Join(lines, "\n")
}

func (h *standardHandler) words() wordDoc {
	return wordDoc{rows: h.buf.Rows(), line: func(y int) []rune {
		row := h.buf.Row(y)
		line := make([]rune, len(row))
		for i := range row {
			line[i] = row[i].Ch
		}
		return line
	}}
}

func (h *standardHandler) pasteAndReindent() (handled bool) {
	paste, err := h.clipboard.Paste(clipboard.DefaultRegisterID)
	if err != nil {
		h.log(log.ErrorLevel, "clipboard paste: %v", err)
		return
	}
	str := paste.Text
	mode, _ := paste.Metadata.(text.SelectMode)
	startY := h.cursor.CursorAtScroll().Y
	h.cursor.Paste(str, mode, false)
	handled = true
	endPos := h.cursor.CursorAtScroll()
	if !h.cursor.SelectRange(
		term.Coordinates{Y: startY},
		term.Coordinates{Y: endPos.Y},
	) {
		h.cursor.MoveToScroll(endPos)
		return
	}
	h.cursor.ReindentSelection(h.cfg.indentRune, h.cfg.indentTabspaces)
	h.cursor.MoveToScroll(endPos)
	return
}

// pasteFromHistory pastes from clipboard history. If the last action was a
// paste, it replaces that paste with the next older history entry.
func (h *standardHandler) pasteFromHistory() (handled bool) {
	history, ok := registerhistory.AsHistory(h.clipboard)
	if !ok {
		return h.lastPaste
	}
	next := 0
	if h.lastPaste {
		next = h.historyIdx + 1
	}
	if next >= history.HistoryLen() {
		return h.lastPaste
	}
	data, ok := history.HistoryAt(next)
	if !ok {
		return h.lastPaste
	}
	if h.lastPaste {
		// Undo the previous paste before replacing it with an older entry.
		h.cursor.Undo()
	}
	mode, _ := data.Metadata.(text.SelectMode)
	h.cursor.Paste(data.Text, mode, false)
	h.historyIdx = next
	h.lastPaste = true
	handled = true
	return
}

func (h *standardHandler) setStatusBar(bar statusBar) {
	h.statusBar = bar
}

func (h *standardHandler) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "standard.handler").Logf(level, msg, args...)
}

type foldsService interface {
	FoldsFrom(pos term.Coordinates) (iterator.Iterator[term.Range], bool)
	Folds() (iterator.Iterator[term.Range], bool)
	InitialFolds() (iterator.Iterator[term.Range], bool)
}

func (h *standardHandler) hideInitialFolds() {
	svc, ok := h.less.Buffer().View().(foldsService)
	if !ok {
		h.log(log.DebugLevel, "folds service not available for resource: %s", h.resource)
		return
	}
	folds, ok := svc.InitialFolds()
	if !ok {
		h.log(log.DebugLevel, "initial folds returned false")
		return
	}

	scroll := h.less.Scroll()

	go debug.CapturePanicReport(func() {
		folds, isEmpty := iterator.IsEmpty(context.Background(), folds)
		if isEmpty {
			folds.Close()
			return
		}
		h.cfg.scheduleNextTick(func() {
			defer folds.Close()
			cursor := h.CursorAtScroll()
			for {
				fold, ok := folds.Next(context.Background())
				if !ok {
					break
				}
				scroll.MarkHidden(fold.Start.Y, fold.End.Y)
			}
			if err := folds.Err(); err != nil {
				h.log(log.ErrorLevel, "error hiding initial folds: %v", err)
			}
			h.SetCursorAtScroll(cursor)
		})
	})
}

func (h *standardHandler) selectNextWordAtCursor() bool {
	h.cursor.Unselect()
	if h.cursor.IsEndWord() && !h.cursor.IsStartWord() {
		h.cursor.MoveLeft()
		word := h.cursor.Word()
		h.cursor.SearchWord(word)
		h.cursor.MoveToNextMatch()
	} else {
		word := h.cursor.Word()
		h.cursor.SearchWord(word)
	}
	if !h.cursor.IsStartWord() {
		h.cursor.MoveLeftStartWordNoWrap()
	}
	h.cursor.Select()
	h.cursor.MoveRightEndWordNoWrap()
	return true
}

func (h *standardHandler) selectPrevWordAtCursor() bool {
	h.cursor.Unselect()
	if h.cursor.IsEndWord() && !h.cursor.IsStartWord() {
		h.cursor.MoveLeft()
	}
	word := h.cursor.Word()
	h.cursor.SearchWord(word)
	// Anchor at the start of the current occurrence so MoveToPrevMatch
	// skips it and lands on the truly previous occurrence.
	if !h.cursor.IsStartWord() {
		h.cursor.MoveLeftStartWordNoWrap()
	}
	h.cursor.MoveToPrevMatch()
	if !h.cursor.IsStartWord() {
		h.cursor.MoveLeftStartWordNoWrap()
	}
	h.cursor.Select()
	h.cursor.MoveRightEndWordNoWrap()
	return true
}

// cutToEndOfLine copies from the caret to the end of the current line to the
// clipboard and deletes it, matching Zed's editor::CutToEndOfLine. When the
// caret is already at the end of the line, it cuts the trailing newline so the
// next line is joined, mirroring Emacs kill-line.
func (h *standardHandler) cutToEndOfLine() bool {
	start := h.cursor.CursorAtScroll()
	if !h.cursor.Select() {
		return false
	}
	h.cursor.MoveEndLine()
	if h.cursor.CursorAtScroll() == start {
		// Already at end of line: join the next line up instead.
		h.cursor.Unselect()
		return h.cursor.Conflate()
	}
	if _, err := h.cursor.CopySelectionNoUnselect(
		clipboard.DefaultRegisterID, h.clipboard); err != nil {
		h.log(log.ErrorLevel, "cursor copy selection: %v", err)
		return false
	}
	return h.cursor.DeleteSelection()
}
