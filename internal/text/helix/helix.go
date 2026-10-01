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

package helix

import (
	"context"

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

var _ text.Handler = (*Helix)(nil)

// Helix implements a Helix-like modal text editor which satisfies
// text.Handler. Motions create or extend a set of selections, one of
// them primary, and operators act on all of them.
type Helix struct {
	resource  workspaceapi.URI
	handler   helixHandler
	buf       *cell.Buffer
	cursor    *text.Cursor
	mouse     *mouse.Mouse
	less      *handler.Less
	clipboard clipboard.Register
	config    helixConfig

	scheduleNextTick func(func()) bool

	repeating  int
	currEdited bool
	oob        bool // out-of-band edits (i.e. via CellEditor)
	oobEdited  bool

	// repeatEdits holds the keystrokes of the last insert session so `.`
	// can replay them. Helix's `.` repeats the insert, not the whole
	// change, so nothing outside insert mode is recorded.
	currEdits   []term.Event
	repeatEdits []term.Event

	changeList          *changeList
	pendingChange       term.Coordinates
	pendingChangeExists bool

	// selAt is the selection set the buffer had at each undo version
	// it has rested at, which is what Helix's history stores with
	// every revision so undo and redo bring the selection back with
	// the text.
	selAt map[int]selection

	resetting bool
}

// New allocates storage for a new Helix handler, initializes it and returns it.
func New(buf *cell.Buffer, resource workspaceapi.URI, opts ...Option) *Helix {
	return NewWithIndent(buf, resource, text.IndentRuneTab, 0, opts...)
}

// NewWithIndent allocates storage for a new Helix handler, initializes it and
// returns it. indentTabspaces is the number of spaces per indent level when
// indentRune is IndentRuneSpace; pass 0 to fall back to the editor's
// configured tabspaces.
func NewWithIndent(
	buf *cell.Buffer, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) *Helix {
	hx := new(Helix)
	hx.Init(buf, resource, indentRune, indentTabspaces, opts...)
	return hx
}

// Init initializes this Helix handler with the given cell.Buffer.
func (hx *Helix) Init(
	buf *cell.Buffer, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) {
	hx.config = defaultHelixConfig()
	hx.config.indentRune = indentRune
	hx.config.indentTabspaces = indentTabspaces
	for _, o := range opts {
		o(&hx.config)
	}
	impl := new(helixHandlerImpl)
	impl.init(buf, hx.config)

	hx.init(impl, buf, resource)

	text.WithCopyDelete(hx.config.defaultRegister, hx, hx.cursor, buf)
	hx.buf.Subscribe((*cellSubscriber)(hx))
}

// InitWithScroll initializes this Helix handler with the given
// component.Scroll and its cell.Buffer. Unlike Init it installs neither
// copy-on-delete nor undo grouping, because the caller's Scroll may not
// have been initialized with subscription support.
func (hx *Helix) InitWithScroll(
	scroll *component.Scroll, resource workspaceapi.URI,
	indentRune rune, indentTabspaces int, opts ...Option,
) {
	hx.config = defaultHelixConfig()
	hx.config.indentRune = indentRune
	hx.config.indentTabspaces = indentTabspaces
	for _, o := range opts {
		o(&hx.config)
	}
	impl := new(helixHandlerImpl)
	impl.initWithScroll(scroll, hx.config)

	hx.init(impl, scroll.Buffer(), resource)
}

func (hx *Helix) init(
	impl *helixHandlerImpl, buf *cell.Buffer, resource workspaceapi.URI,
) {
	hx.resource = resource
	hx.handler = impl
	hx.buf = buf
	hx.less = &impl.less
	hx.cursor = &impl.cursor
	hx.mouse = mouse.New(newDelegate(impl))
	hx.clipboard = impl.config.clipboard
	hx.scheduleNextTick = impl.config.scheduleNextTick
	if spec, ok := text.CommentSpecForURI(resource, impl.config.comments); ok {
		hx.cursor.SetCommentSpec(spec)
	}

	hx.repeatEdits = make([]term.Event, 0)
	hx.currEdits = make([]term.Event, 0)
	hx.changeList = newChangeList()
	hx.selAt = make(map[int]selection)
	hx.oob = true

	hx.snapshotContent()
	if impl.config.enableInitialFolds {
		hx.hideInitialFolds()
	}
}

// Selection returns the text currently selected, or false when the
// selection is empty.
func (hx *Helix) Selection() (string, bool) {
	return hx.handler.Selection()
}

// SelectionBounds returns the sorted, half-open [from, to) buffer range
// highlighted by the active selection, if any.
func (hx *Helix) SelectionBounds() (from, to term.Coordinates, ok bool) {
	return hx.cursor.SelectionRange()
}

// Cursor satisfies tui.Handler.
func (hx *Helix) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return hx.handler.Cursor()
}

// Draw satisfies tui.Component.
func (hx *Helix) Draw(w term.Writer) {
	hx.handler.Draw(w)
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

func (hx *Helix) resetEdits() {
	// Zero the slots before truncating: term.Event contains heap
	// pointers (Err, Raw, UserFunc, Context) and a bare [:0] reslice
	// would keep them reachable until overwritten.
	clear(hx.currEdits)
	hx.currEdits = hx.currEdits[:0]
	hx.currEdited = false
}

func (hx *Helix) copyRepeat() {
	if len(hx.currEdits) == 0 {
		return
	}
	clear(hx.repeatEdits)
	hx.repeatEdits = hx.repeatEdits[:0]
	hx.repeatEdits = append(hx.repeatEdits, hx.currEdits...)
}

func (hx *Helix) beginChange(pos term.Coordinates) {
	if hx.pendingChangeExists {
		return
	}
	hx.pendingChange = pos
	hx.pendingChangeExists = true
}

func (hx *Helix) commitChange() {
	if !hx.pendingChangeExists {
		return
	}
	hx.changeList.append(hx.pendingChange)
	hx.pendingChangeExists = false
}

// Paste satisfies text.Clipboard. See Copy.
func (hx *Helix) Paste(registerID string) (clipboard.Data, error) {
	return hx.clipboard.Paste(registerID)
}

// Copy satisfies text.Clipboard so undo/redo deletes and insert-mode
// backspaces are not copied to the clipboard.
func (hx *Helix) Copy(registerID string, data clipboard.Data) error {
	if hx.resetting || hx.handler.mode() == insertMode || hx.handler.copySuppressed() {
		return nil
	}
	return hx.clipboard.Copy(registerID, data)
}

// Handle satisfies tui.Handler.
func (hx *Helix) Handle(ev term.Event) (quit, handled bool) {
	if ev.Type == term.EventMouse {
		quit, handled = hx.mouse.Handle(ev)
		hx.handler.resetSelection()
		return quit, handled
	}

	oobEdited := hx.oobEdited
	if oobEdited {
		hx.snapshotContent()
		hx.resetEdits()
		hx.oobEdited = false
	}

	// Buffer-level history lives on the wrapper, so u/U and Helix's
	// A-u/A-U (earlier/later) are claimed before the state machine sees
	// them. A-u/A-U differ from u/U only in taking a count.
	if hx.repeating == 0 && hx.handler.mode() == normalMode && ev.Type == term.EventKey {
		switch ev.Mod {
		case 0:
			switch ev.Ch {
			case '.':
				return false, hx.repeat()
			case 'u':
				return false, hx.undoCount(hx.handler.takeCount())
			case 'U':
				return false, hx.redoCount(hx.handler.takeCount())
			}
		case term.ModAlt:
			switch ev.Ch {
			case 'u':
				return false, hx.undoCount(hx.handler.takeCount())
			case 'U':
				return false, hx.redoCount(hx.handler.takeCount())
			}
		}
	}

	hx.oob = false
	prevMode := hx.handler.mode()
	quit, handled = hx.handler.Handle(ev)
	nextMode := hx.handler.mode()
	hx.oob = true

	if hx.handler.takeUndoCheckpoint() {
		hx.snapshotContent()
		// currEdited gates the MarkStartUndo in OnWillEdit, so it has
		// to be cleared for the next edit to open a fresh undo group.
		hx.currEdited = false
	}

	if oobEdited {
		return quit, handled
	}

	switch {
	case prevMode == insertMode && nextMode == insertMode:
		hx.currEdits = append(hx.currEdits, ev)
	case prevMode != insertMode && nextMode == insertMode:
		// The entering command (i, a, o, c, ...) is not replayed: `.`
		// re-inserts at the current selection. The edit it made, if
		// any, stays in the open undo group: Helix commits history
		// only once insert mode is left, so c and o undo together
		// with what was typed after them.
		edited := hx.currEdited
		hx.resetEdits()
		hx.currEdited = edited
	case prevMode == insertMode && nextMode != insertMode:
		hx.currEdits = append(hx.currEdits, ev)
		hx.copyRepeat()
		hx.snapshotContent()
		hx.resetEdits()
	default:
		if hx.currEdited {
			hx.snapshotContent()
			hx.resetEdits()
		}
	}
	return quit, handled
}

// Resize satisfies tui.Component.
func (hx *Helix) Resize(width, height int) {
	hx.handler.Resize(width, height)
}

// MoveToNextLocation moves the cursor to the next location in the
// location list identified by ID.
func (hx *Helix) MoveToNextLocation(ID string) bool {
	return hx.handler.moveToNextLocation(ID)
}

// MoveToPrevLocation moves the cursor to the previous location in the
// location list identified by ID.
func (hx *Helix) MoveToPrevLocation(ID string) bool {
	return hx.handler.moveToPrevLocation(ID)
}

// SetLocationList sets a location list of this handler. See Cursor.SetLocationList.
func (hx *Helix) SetLocationList(
	pri textapi.LocationPriority, ID string, l text.LocationList,
) {
	hx.handler.setLocationList(pri, ID, l)
}

// SetCursorAtScroll sets the cursor of this Helix handler at content pos.
func (hx *Helix) SetCursorAtScroll(pos term.Coordinates) bool {
	ok := hx.handler.setCursorAtScroll(pos)
	if !ok || !hx.config.autoCenter {
		return ok
	}
	hx.cursor.Center()
	return true
}

// CursorAtScroll returns the position of this handler's cursor in the
// underlying content buffer.
func (hx *Helix) CursorAtScroll() term.Coordinates {
	return hx.handler.cursorAtScroll()
}

// CellView returns the underlying cell.View.
func (hx *Helix) CellView() cell.View {
	return hx.buf.View()
}

// CellEditor returns the underlying cell.Editor.
func (hx *Helix) CellEditor() cell.Editor {
	return text.ExternalEditor(hx.cursor, hx.buf.Editor())
}

// Dimensions satisfies text.Handler: Helix is a leaf handler with no
// sidebar chrome, so the ideal size is the widest row in the underlying
// buffer by the buffer row count.
func (hx *Helix) Dimensions() (int, int) {
	return text.ViewDimensions(hx.buf.View())
}

// Resource satisfies text.Handler.
func (hx *Helix) Resource() workspaceapi.URI {
	return hx.resource
}

// SetWrap satisfies text.Handler.
func (hx *Helix) SetWrap(wrap bool) {
	hx.less.Scroll().Wrap = wrap
}

// ShowCommandBar satisfies text.Handler.
func (hx *Helix) ShowCommandBar(show bool) {
	/* handled by StatusBar */
}

// SetMessage sets a message on the status bar.
func (hx *Helix) SetMessage(msg string) {
	/* handled by StatusBar */
}

// IsEditMode reports whether keystrokes are inserted as text.
func (hx *Helix) IsEditMode() bool {
	return hx.handler.mode() == insertMode
}

// IsSearchMode satisfies text.Handler.
func (hx *Helix) IsSearchMode() bool {
	return hx.handler.mode() == searchMode
}

// IsNormalMode satisfies text.Handler. Helix's select mode is reported
// as normal mode: keystrokes there are commands, not text.
func (hx *Helix) IsNormalMode() bool {
	return hx.handler.mode() == normalMode
}

// IsSelectMode reports whether Helix's sticky extend mode is active.
func (hx *Helix) IsSelectMode() bool {
	return hx.handler.mode() == normalMode && hx.handler.extending()
}

// Search runs a text search on the underlying scroll content.
func (hx *Helix) Search(target string) {
	hx.handler.search(target)
}

// SetNormalMode switches the mode to normal.
func (hx *Helix) SetNormalMode() bool {
	return hx.handler.setNormalMode()
}

// Unselect unselects any text that has been previously selected.
func (hx *Helix) Unselect() bool {
	return hx.handler.unselect()
}

// SetDefaultAttributes sets the underlying Scroll's default Attributes.
func (hx *Helix) SetDefaultAttributes(attrs term.Attributes) {
	hx.less.Scroll().Attributes = attrs
}

// Close satisfies text.Handler.
func (hx *Helix) Close() error {
	return nil
}

// SeekUp satisfies component.Scrollable.
func (hx *Helix) SeekUp() bool {
	return hx.less.Scroll().SeekUp()
}

// SeekDown satisfies component.Scrollable.
func (hx *Helix) SeekDown() bool {
	return hx.less.Scroll().SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (hx *Helix) SeekOffset() int {
	return hx.less.Scroll().SeekOffset()
}

// MaxSeekOffset satisfies component.Scrollable.
func (hx *Helix) MaxSeekOffset() int {
	return hx.less.Scroll().MaxSeekOffset()
}

// LocationLists satisfies text.Handler.
func (hx *Helix) LocationLists() []text.LocationSet {
	return hx.cursor.LocationLists()
}

// repeat implements Helix's `.`: re-enter insert at the selection and
// replay the keystrokes of the last insert session.
func (hx *Helix) repeat() (handled bool) {
	if len(hx.repeatEdits) == 0 {
		return false
	}
	hx.repeating++
	hx.handler.insertBeforeSelection()
	for _, ev := range hx.repeatEdits {
		handled = true
		hx.Handle(ev)
	}
	hx.repeating--
	if handled {
		hx.handler.moveToBounds()
	}
	return handled
}

// undoCount and redoCount implement u/U and Helix's A-u/A-U, which walk
// the history count steps at a time.
func (hx *Helix) undoCount(count int) bool {
	moved := false
	for range max(1, count) {
		if !hx.undo() {
			break
		}
		moved = true
	}
	return moved
}

func (hx *Helix) redoCount(count int) bool {
	moved := false
	for range max(1, count) {
		if !hx.redo() {
			break
		}
		moved = true
	}
	return moved
}

func (hx *Helix) redo() bool {
	if hx.resetToSnapshot(hx.buf.Redo) {
		hx.handler.moveToBounds()
		return true
	}
	return false
}

func (hx *Helix) undo() bool {
	if hx.resetToSnapshot(hx.buf.Undo) {
		hx.handler.moveToBounds()
		return true
	}
	return false
}

func (hx *Helix) resetToSnapshot(op func() (bool, term.Coordinates)) bool {
	hx.resetting = true
	ok, at := op()
	if ok {
		if sel, found := hx.selAt[hx.buf.Version()]; found {
			hx.handler.restoreSelection(sel)
		} else {
			hx.handler.setCursorAtScroll(at)
		}
	}
	hx.resetting = false
	return ok
}

func (hx *Helix) setStatusBar(bar statusBar) {
	hx.handler.setStatusBar(bar)
}

func (hx *Helix) snapshotContent() {
	hx.commitChange()
	hx.buf.GroupUndo()
	hx.selAt[hx.buf.Version()] = hx.handler.selectionAfter()
}

type cellSubscriber = Helix

// OnWillEdit satisfies cell.Subscriber.
func (hx *cellSubscriber) OnWillEdit(
	ctx context.Context, from, to term.Coordinates, str string,
) {
	if (!hx.oob && hx.oobEdited) || (!hx.currEdited && !hx.resetting) {
		hx.buf.MarkStartUndo()
		if !hx.resetting {
			hx.rememberSelectionBefore()
		}
	}
	if !hx.resetting {
		hx.beginChange(from)
	}
}

// rememberSelectionBefore stores the selection the new undo group
// starts from. The undoer has already counted this edit, so the
// version the buffer rests at before it is one less; every version
// beyond it is now unreachable, as a fresh edit discards the redo
// timeline.
func (hx *Helix) rememberSelectionBefore() {
	version := hx.buf.Version() - 1
	for v := range hx.selAt {
		if v > version {
			delete(hx.selAt, v)
		}
	}
	hx.selAt[version] = hx.handler.selectionBefore()
}

// OnDidEdit satisfies cell.Subscriber.
func (hx *cellSubscriber) OnDidEdit(
	ctx context.Context, start, end term.Coordinates, old string,
) {
	if !hx.resetting {
		hx.currEdited = true
		if hx.pendingChangeExists && hx.pendingChange.Y < start.Y {
			hx.pendingChange = start
		}
		// Records the last edit position for goto-last-modification.
		hx.handler.setLocationList(textapi.LocationPriorityInfo, lastChangeLocationListID,
			textapi.LocationSlice([]textapi.Location{{
				From: start,
				To:   term.Coordinates{X: start.X + 1, Y: start.Y},
			}}))
	}
	hx.oobEdited = hx.oob
}

var _ foldsService = (*syntax.Tree)(nil)

type foldsService interface {
	FoldsFrom(pos term.Coordinates) (iterator.Iterator[term.Range], bool)
	Folds() (iterator.Iterator[term.Range], bool)
	InitialFolds() (iterator.Iterator[term.Range], bool)
}

func (hx *Helix) hideInitialFolds() {
	svc, ok := hx.less.Buffer().View().(foldsService)
	if !ok {
		hx.log(log.DebugLevel, "folds service not available for resource: %s", hx.resource)
		return
	}
	folds, ok := svc.InitialFolds()
	if !ok {
		hx.log(log.DebugLevel, "initial folds returned false")
		return
	}

	scroll := hx.less.Scroll()

	go debug.CapturePanicReport(func() {
		folds, isEmpty := iterator.IsEmpty(context.Background(), folds)
		if isEmpty {
			folds.Close()
			return
		}
		hx.scheduleNextTick(func() {
			defer folds.Close()
			cursor := hx.handler.cursorAtScroll()
			for {
				fold, ok := folds.Next(context.Background())
				if !ok {
					break
				}
				scroll.MarkHidden(fold.Start.Y, fold.End.Y)
			}
			if err := folds.Err(); err != nil {
				hx.log(log.ErrorLevel, "error hiding initial folds: %v", err)
			}
			hx.handler.setCursorAtScroll(cursor)
		})
	})
}

func (hx *Helix) log(level log.Level, msg string, args ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "helix.Helix").Logf(level, msg, args...)
}
