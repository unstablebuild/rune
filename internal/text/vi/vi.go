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

package vi

import (
	"context"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/text"
)

var _ text.Handler = (*Vi)(nil)

// Vi implements a basic vi-like text editor which satisfies tui.Handler
type Vi struct {
	resource  workspaceapi.URI
	handler   viHandler
	buf       *cell.Buffer
	cursor    *text.Cursor
	mouse     *mouse.Mouse
	less      *handler.Less
	clipboard clipboard.Register
	config    viConfig

	scheduleNextTick func(func()) bool

	repeating   int
	currEdited  bool
	evEdited    bool
	oob         bool // out-of-band edits (i.e. via CellEditor)
	oobEdited   bool
	currEdits   []term.Event
	repeatEdits []term.Event

	changeList          *changeList
	pendingChange       term.Coordinates
	pendingChangeExists bool

	resetting bool
}

// New allocates storage for a new Vi handler, initializes it and returns it.
func New(buf *cell.Buffer, resource workspaceapi.URI, opts ...Option) *Vi {
	return NewWithIndent(buf, resource, text.IndentRuneTab, 0, opts...)
}

// NewWithIndent allocates storage for a new Vi handler, initializes it and returns it.
// indentTabspaces is the number of spaces per indent level when indentRune is
// IndentRuneSpace; pass 0 to fall back to the editor's configured tabspaces.
func NewWithIndent(
	buf *cell.Buffer, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) *Vi {
	vi := new(Vi)
	vi.Init(buf, resource, indentRune, indentTabspaces, opts...)
	return vi
}

// Init initialies this vi handle with the given cell.Buffer.
func (vi *Vi) Init(
	buf *cell.Buffer, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) {
	vi.config = defaultviHandlerImplConfig()
	vi.config.indentRune = indentRune
	vi.config.indentTabspaces = indentTabspaces
	for _, o := range opts {
		o(&vi.config)
	}
	viHandler := new(viHandlerImpl)
	viHandler.init(buf, vi.config)

	vi.init(viHandler, buf, resource)

	text.WithCopyDelete(viHandler.config.defaultRegister,
		vi, vi.cursor, buf)
	vi.buf.Subscribe((*cellSubscriber)(vi))
}

// InitWithScroll initialies this vi handle with the given component.Scroll
// and its cell.Buffer. This does not initialize this Vi implementation with copy
// deletes to clipboard or undo/redo because we don't know if the given Scroll was initialized with
// Subscribe functionality or not. Init should be used in favor of this method for standard
// usage of Vi.
func (vi *Vi) InitWithScroll(
	scroll *component.Scroll, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) {
	vi.config = defaultviHandlerImplConfig()
	vi.config.indentRune = indentRune
	vi.config.indentTabspaces = indentTabspaces
	for _, o := range opts {
		o(&vi.config)
	}
	viHandler := new(viHandlerImpl)
	viHandler.initWithScroll(scroll, opts...)

	vi.init(viHandler, scroll.Buffer(), resource)
}

func (vi *Vi) init(
	viHandler *viHandlerImpl, buf *cell.Buffer,
	resource workspaceapi.URI,
) {
	vi.resource = resource
	vi.handler = viHandler
	vi.buf = buf
	vi.less = &viHandler.less
	vi.cursor = &viHandler.cursor
	vi.mouse = mouse.New(newDelegate(viHandler))
	vi.clipboard = viHandler.config.clipboard
	vi.scheduleNextTick = viHandler.config.scheduleNextTick
	if spec, ok := text.CommentSpecForURI(resource, viHandler.config.comments); ok {
		vi.cursor.SetCommentSpec(spec)
	}

	vi.repeatEdits = make([]term.Event, 0)
	vi.currEdits = make([]term.Event, 0)
	vi.changeList = newChangeList()
	vi.oob = true

	vi.snapshotContent()
	if viHandler.config.enableInitialFolds {
		vi.hideInitialFolds()
	}
}

// Selection returns the text currently selected by Vi's visual mode,
// or false if there's no text selected.
func (vi *Vi) Selection() (string, bool) {
	return vi.handler.Selection()
}

// SelectionBounds returns the sorted, half-open [from, to) buffer
// range highlighted by the active visual-mode selection, if any.
func (vi *Vi) SelectionBounds() (from, to term.Coordinates, ok bool) {
	return vi.cursor.SelectionRange()
}

// Cursor satisfies tui.Handler
func (vi *Vi) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return vi.handler.Cursor()
}

// Draw satisfies tui.Component
func (vi *Vi) Draw(w term.Writer) {
	vi.handler.Draw(w)
}

func isEditMode(mode viMode) bool {
	switch mode {
	case normalMode, zMode, gMode, yankMode, searchMode, caseChangeMode,
		commentMode, visualMode, visualLineMode, visualBlockMode:
		return false
	case insertMode, deleteMode, replaceMode, replaceOneMode, shiftMode:
		return true
	default:
		panic(fmt.Sprintf("unknown vi mode: %v", mode))
	}
}

func isSelectMode(mode viMode) bool {
	return mode == visualMode || mode == visualLineMode || mode == visualBlockMode
}

type changeList struct {
	locations []term.Coordinates
	cursor    int
}

func newChangeList() *changeList {
	return &changeList{cursor: -1}
}

func (l *changeList) append(pos term.Coordinates) {
	if l.cursor >= 0 && l.cursor < len(l.locations)-1 {
		l.locations = l.locations[:l.cursor+1]
	}
	if len(l.locations) > 0 && l.locations[len(l.locations)-1] == pos {
		l.cursor = len(l.locations) - 1
		return
	}
	l.locations = append(l.locations, pos)
	l.cursor = len(l.locations) - 1
}

func (l *changeList) older() (term.Coordinates, bool) {
	if l.cursor <= 0 {
		return term.Coordinates{}, false
	}
	l.cursor--
	return l.locations[l.cursor], true
}

func (l *changeList) newer() (term.Coordinates, bool) {
	if l.cursor < 0 || l.cursor+1 >= len(l.locations) {
		return term.Coordinates{}, false
	}
	l.cursor++
	return l.locations[l.cursor], true
}

func (vi *Vi) resetEdits() {
	// Zero the slots before truncating: term.Event contains heap pointers
	// (Err, Raw, UserFunc, Context) and a bare [:0] reslice would keep them
	// reachable until overwritten.
	clear(vi.currEdits)
	vi.currEdits = vi.currEdits[:0]
	vi.currEdited = false
}

func (vi *Vi) appendLastEdit(ev term.Event) {
	vi.currEdits = append(vi.currEdits, ev)
}

func (vi *Vi) copyRepeat() {
	if !vi.currEdited {
		return
	}
	clear(vi.repeatEdits)
	vi.repeatEdits = vi.repeatEdits[:0]
	vi.repeatEdits = append(vi.repeatEdits, vi.currEdits...)
}

func (vi *Vi) beginChange(pos term.Coordinates) {
	if vi.pendingChangeExists {
		return
	}
	vi.pendingChange = pos
	vi.pendingChangeExists = true
}

func (vi *Vi) commitChange() {
	if !vi.pendingChangeExists {
		return
	}
	vi.changeList.append(vi.pendingChange)
	vi.pendingChangeExists = false
}

func (vi *Vi) moveToOlderChange() bool {
	pos, ok := vi.changeList.older()
	if !ok {
		return false
	}
	return vi.handler.setCursorAtScroll(pos)
}

func (vi *Vi) moveToNewerChange() bool {
	pos, ok := vi.changeList.newer()
	if !ok {
		return false
	}
	return vi.handler.setCursorAtScroll(pos)
}

// Paste satisfies text.Clipboard. See Copy.
func (vi *Vi) Paste(registerID string) (clipboard.Data, error) {
	return vi.clipboard.Paste(registerID)
}

// Copy satisfies text.Clipboard to make sure undo/redo deletes
// are not being copied to the clipboard or backspace deletes within insert.
func (vi *Vi) Copy(registerID string, data clipboard.Data) error {
	if vi.resetting || vi.handler.mode() == insertMode || vi.handler.copySuppressed() {
		return nil
	}
	return vi.clipboard.Copy(registerID, data)
}

// Handle satisfies tui.Handler
func (vi *Vi) Handle(ev term.Event) (quit, handled bool) {
	mode := vi.handler.mode()

	if ev.Type == term.EventMouse {
		return vi.mouse.Handle(ev)
	}

	oobEdited := vi.oobEdited
	if oobEdited {
		vi.snapshotContent()
		vi.resetEdits()
		vi.oobEdited = false
	}

	if vi.repeating == 0 {
		switch mode {
		case normalMode:
			switch ev.Type {
			case term.EventKey:
				switch ev.Ch {
				case '.':
					if ev.Mod == 0 {
						handled = vi.repeat()
						return
					}
				case 'u':
					if ev.Mod == 0 {
						handled = vi.undo()
						return
					}
				case 'r':
					if ev.Mod == term.ModCtrl {
						handled = vi.redo()
						return
					}
				}
			}
		case gMode:
			if ev.Type == term.EventKey && ev.Mod == 0 {
				switch ev.Ch {
				case ';':
					vi.moveToOlderChange()
					vi.handler.setNormalMode()
					handled = true
					return
				case ',':
					vi.moveToNewerChange()
					vi.handler.setNormalMode()
					handled = true
					return
				}
			}
		}
	}

	vi.evEdited = false
	vi.oob = false
	prevMode := vi.handler.mode()
	quit, handled = vi.handler.Handle(ev)
	nextMode := vi.handler.mode()
	vi.oob = true

	if oobEdited {
		return
	}

	if prevMode == nextMode {
		if !isEditMode(prevMode) && vi.evEdited {
			vi.appendLastEdit(ev)
			vi.copyRepeat()
			vi.snapshotContent()
			vi.resetEdits()
		} else if isSelectMode(prevMode) || isEditMode(prevMode) {
			vi.appendLastEdit(ev)
		}
		return
	}

	if !isEditMode(prevMode) && isEditMode(nextMode) {
		vi.appendLastEdit(ev)
	} else if isEditMode(prevMode) && !isEditMode(nextMode) {
		vi.appendLastEdit(ev)
		vi.copyRepeat()
		vi.snapshotContent()
		vi.resetEdits()
	} else if isEditMode(prevMode) && isEditMode(nextMode) {
		vi.appendLastEdit(ev)
	} else {
		vi.appendLastEdit(ev)
		if vi.currEdited {
			vi.copyRepeat()
			vi.snapshotContent()
			vi.resetEdits()
		} else if !isSelectMode(nextMode) {
			// reset always if going back to normal
			vi.resetEdits()
		}
	}
	return quit, handled
}

// Resize satisfies tui.Component
func (vi *Vi) Resize(width, height int) {
	vi.handler.Resize(width, height)
}

// MoveToNextLocation moves the cursor to the next location
// in the location list identified by ID.
func (vi *Vi) MoveToNextLocation(ID string) bool {
	return vi.handler.moveToNextLocation(ID)
}

// MoveToPrevLocation moves the cursor to the previous location
// in the location list identified by ID.
func (vi *Vi) MoveToPrevLocation(ID string) bool {
	return vi.handler.moveToPrevLocation(ID)
}

// SetLocationList sets a location list of this handler. See Cursor.SetLocationList
func (vi *Vi) SetLocationList(
	pri textapi.LocationPriority, ID string, l text.LocationList,
) {
	vi.handler.setLocationList(pri, ID, l)
}

// SetCursorAtScroll sets the cursor of this Vi handler at content pos.
func (vi *Vi) SetCursorAtScroll(pos term.Coordinates) bool {
	ok := vi.handler.setCursorAtScroll(pos)
	if !ok || !vi.config.autoCenter {
		return ok
	}
	if !vi.cursor.Center() {
		return true
	}
	// after calling center, the free cursor (used for repositioning
	// after out of bounds repositioning when moving up/down),
	// needs to be reset
	if vh, ok := vi.handler.(*viHandlerImpl); ok {
		vh.anchor = vi.handler.cursorAtScroll()
	}
	return true
}

// CursorAtScroll sets the cursor of this Vi handler at content pos.
func (vi *Vi) CursorAtScroll() term.Coordinates {
	return vi.handler.cursorAtScroll()
}

// CellView returns the underlying cell.View.
func (vi *Vi) CellView() cell.View {
	return vi.buf.View()
}

// CellEditor returns the underlying cell.Editor.
func (vi *Vi) CellEditor() cell.Editor {
	return text.ExternalEditor(vi.cursor, vi.buf.Editor())
}

// Dimensions satisfies text.Handler (and component.Floating): Vi is
// a leaf handler with no sidebar chrome, so the ideal size is the
// widest row in the underlying buffer by the buffer row count.
// Bar-wrapping handlers add their own contribution on top.
//
// Width is the VISUAL width of the widest row, not the cell count:
// cell.View.Columns(y) returns len(cells[y]) which is one entry per
// grapheme cluster regardless of that cluster's monospace width. Wide
// glyphs (e.g. Nerd Font icons treated as two cells, CJK) occupy more
// cells on screen than they do in the buffer, so we have to sum each
// cell's Width. Falling back to Columns(y) would under-report width
// for any row containing a width-2 glyph, causing the hosting window
// to be sized one cell too narrow and truncating the last character.
func (vi *Vi) Dimensions() (int, int) {
	return text.ViewDimensions(vi.buf.View())
}

// Resource satisfies editor.Handler.
func (vi *Vi) Resource() workspaceapi.URI {
	return vi.resource
}

// SetWrap satisfies editor.Handler.
func (vi *Vi) SetWrap(wrap bool) {
	vi.less.Scroll().Wrap = wrap
}

// ShowCommandBar satisfies editor.Handler.
func (vi *Vi) ShowCommandBar(show bool) {
	/* handled by StatusBar */
}

// SetMessage sets a message on the status bar.
func (vi *Vi) SetMessage(msg string) {
	/* handled by StatusBar */
}

// IsEditMode returns whether the current mode is one of
// the edit modes: insert, delete or replace.
func (vi *Vi) IsEditMode() bool {
	return isEditMode(vi.handler.mode())
}

// IsSearchMode returns whether the current mode is the search mode.
func (vi *Vi) IsSearchMode() bool {
	return vi.handler.mode() == searchMode
}

// IsNormalMode returns whether the current mode is normal mode.
func (vi *Vi) IsNormalMode() bool {
	return vi.handler.mode() == normalMode
}

// Search runs a text search on the underlying scroll content.
func (vi *Vi) Search(target string) {
	vi.handler.search(target)
}

// SetNormalMode switches the mode to normal.
// It returns false if the current mode was already normal mode.
func (vi *Vi) SetNormalMode() bool {
	return vi.handler.setNormalMode()
}

// Unselect unselects any text that's been previously selected.
func (vi *Vi) Unselect() bool {
	return vi.handler.unselect()
}

// SetDefaultAttributes sets the underlying's Scroll's default Attributes.
func (vi *Vi) SetDefaultAttributes(attrs term.Attributes) {
	vi.less.Scroll().Attributes = attrs
}

// Close satisfies editor.Handler.
func (vi *Vi) Close() error {
	return nil
}

// SeekUp satisfies component.Scrollable.
func (vi *Vi) SeekUp() bool {
	return vi.less.Scroll().SeekUp()
}

// SeekDown satisfies component.Scrollable.
func (vi *Vi) SeekDown() bool {
	return vi.less.Scroll().SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (vi *Vi) SeekOffset() int {
	return vi.less.Scroll().SeekOffset()
}

// MaxSeekOffset satisfies component.Scrollable.
func (vi *Vi) MaxSeekOffset() int {
	return vi.less.Scroll().MaxSeekOffset()
}

// LocationLists satisfies text.Handler.
func (h *Vi) LocationLists() []text.LocationSet {
	return h.cursor.LocationLists()
}

func (vi *Vi) repeat() (handled bool) {
	vi.repeating++
	for _, ev := range vi.repeatEdits {
		handled = true
		vi.Handle(ev)
	}
	if handled {
		vi.handler.moveToBounds()
	}
	vi.repeating--
	return
}

func (vi *Vi) redo() bool {
	if vi.resetToSnapshot(vi.buf.Redo) {
		vi.handler.moveToBounds()
		return true
	}
	return false
}

func (vi *Vi) undo() bool {
	if vi.resetToSnapshot(vi.buf.Undo) {
		vi.handler.moveToBounds()
		return true
	}
	return false
}

func (vi *Vi) resetToSnapshot(op func() (bool, term.Coordinates)) bool {
	vi.resetting = true
	ok, at := op()
	if ok {
		vi.handler.setCursorAtScroll(at)
	}
	vi.resetting = false
	return ok
}

func (vi *Vi) setStatusBar(bar statusBar) {
	vi.handler.setStatusBar(bar)
}

func (vi *Vi) snapshotContent() {
	vi.commitChange()
	vi.buf.GroupUndo()
}

type cellSubscriber = Vi

// OnWillEdit satisfies cell.Subscriber.
func (vi *cellSubscriber) OnWillEdit(
	ctx context.Context, from, to term.Coordinates, str string,
) {
	if (!vi.oob && vi.oobEdited) || (!vi.currEdited && !vi.resetting) {
		vi.buf.MarkStartUndo()
	}
	if !vi.resetting {
		vi.beginChange(from)
	}
}

// OnDidEdit satisfies cell.Subscriber.
func (vi *cellSubscriber) OnDidEdit(
	ctx context.Context, start, end term.Coordinates, old string,
) {
	if !vi.resetting {
		vi.evEdited = true
		vi.currEdited = true
		if vi.pendingChangeExists && vi.pendingChange.Y < start.Y {
			vi.pendingChange = start
		}
		// Record last edit position for `. and '. marks.
		vi.handler.setLocationList(textapi.LocationPriorityInfo, lastChangeLocationListID,
			textapi.LocationSlice([]textapi.Location{{
				From: start,
				To:   term.Coordinates{X: start.X + 1, Y: start.Y},
			}}))
	}
	vi.oobEdited = vi.oob
}

var _ foldsService = (*syntax.Tree)(nil)

type foldsService interface {
	FoldsFrom(pos term.Coordinates) (iterator.Iterator[term.Range], bool)
	Folds() (iterator.Iterator[term.Range], bool)
	InitialFolds() (iterator.Iterator[term.Range], bool)
}

func (vi *Vi) hideInitialFolds() {
	svc, ok := vi.less.Buffer().View().(foldsService)
	if !ok {
		vi.log(log.DebugLevel, "folds service not available for resource: %s", vi.resource)
		return
	}
	folds, ok := svc.InitialFolds()
	if !ok {
		vi.log(log.DebugLevel, "initial folds returned false")
		return
	}

	scroll := vi.less.Scroll()

	go debug.CapturePanicReport(func() {
		folds, isEmpty := iterator.IsEmpty(context.Background(), folds)
		if isEmpty {
			folds.Close()
			return
		}
		vi.scheduleNextTick(func() {
			defer folds.Close()
			cursor := vi.handler.cursorAtScroll()
			for {
				fold, ok := folds.Next(context.Background())
				if !ok {
					break
				}
				scroll.MarkHidden(fold.Start.Y, fold.End.Y)
			}
			if err := folds.Err(); err != nil {
				vi.log(log.ErrorLevel, "error hiding initial folds: %v", err)
			}
			vi.handler.setCursorAtScroll(cursor)
		})
	})
}

func (e *Vi) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "vi.Vi").Logf(level, msg, args...)
}
