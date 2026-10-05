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

package vte

import (
	"context"
	"math"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/term/vte/vtescreen"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/vi"
)

var _ tui.Handler = (*viHandler)(nil)

// viHandler serves both as a tui.Handler entrypoint to a limited vi tui.Handler
// implementation, and a cell.Editor, which intercepts user edits
// and transforms them into shell escape sequences to manipulate the content.
type viHandler struct {
	comp   parentComponent
	remote remote
	config Config
	width  int
	height int
	sync   struct {
		mu        sync.Locker
		vi        *vi.Vi
		scroll    *component.Scroll
		vteScroll *component.Scroll
		editor    cell.Editor
		selector  *cell.Buffer
	}

	edited          bool
	vteParserEdited bool

	// keeep a copy of vi and all the contents
	// so it can be accessed synchronously and provide
	// correct returned handled in Handle.
	copy struct {
		mu     sync.Mutex
		vi     *vi.Vi
		editor cell.Editor
	}
}

// for dependency injection purposes
type parentComponent interface {
	PrimaryScroll() (*component.Scroll, sync.Locker)
	URI() workspaceapi.URI
	cursorAtScroll() term.Coordinates
	scheduleBellCallback(callback func()) bool
	pendingCallbacks() int
}

func (v *viHandler) init(comp *Component, config Config) {
	v.doInit(comp, config)
	v.remote = ptyWriterRemote(comp, config.ScheduleNextTick)
	if log.IsLevelEnabled(log.TraceLevel) {
		v.remote = newLoggingRemote(v.remote)
	}
}

func (v *viHandler) doInit(comp parentComponent, config Config) {
	opts := []vi.Option{
		vi.WithResAttr(config.SelectionAttributes),
		vi.WithAttr(config.Attributes),
		vi.WithWrap(false),
		vi.WithCursorCorrections(false),
		vi.WithClipboard(config.Clipboard),
		vi.WithTabspaces(1),
	}
	copyBuffer := new(cell.Buffer)
	copyBuffer.InitPerformance(120, 80, vtescreen.DefaultChar)
	copyScroll := new(component.Scroll)
	copyScroll.InitPerformance(copyBuffer)
	copyScroll.InvertOffset = true
	v.copy.vi = new(vi.Vi)
	v.copy.vi.InitWithScroll(copyScroll, comp.URI(), text.IndentRuneTab, 0, opts...)
	v.copy.editor = copyScroll.Buffer().WithEditor(copyEditor{v: v})

	vi := new(vi.Vi)
	// do not share scroll (we don't want vi messing around with the offsets
	// of the vte parser, which gets complicated quickly to maintain and keep sync
	// but share buffer, so updates are synced.
	v.sync.scroll = new(component.Scroll)
	v.sync.vteScroll, v.sync.mu = comp.PrimaryScroll()
	v.sync.scroll.InitPerformance(v.sync.vteScroll.Buffer())
	v.sync.scroll.InvertOffset = true
	vi.InitWithScroll(v.sync.scroll, comp.URI(), text.IndentRuneTab, 0, opts...)
	v.sync.vi = vi
	v.sync.selector = v.sync.scroll.Buffer()
	v.sync.editor = v.sync.scroll.Buffer().WithEditor(v)

	v.comp = comp
	v.config = config
}

func (v *viHandler) Handle(ev term.Event) (exit, handled bool) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	exit, handled = v.handle(ev)
	if exit {
		if !(ev.Key == term.KeyEnter && ev.Mod == 0) {
			v.scheduleAfterBell(false, func() {
				pos := v.trimToLastValidColumn(v.sync.vi.CursorAtScroll())
				v.log(log.TraceLevel, "call scheduled cleanup of vi position to comp: %+v", pos)
				v.remoteMoveTo(pos)
				v.remoteFlush()
			})
		}
		v.sync.vi.Unselect()
		v.sync.vi.Search("")
		v.sync.vi.SetNormalMode()
		v.copy.vi.Unselect()
		v.copy.vi.Search("")
		v.copy.vi.SetNormalMode()
	}
	return
}

// SeekUp satisfies component.Scrollable.
func (v *viHandler) SeekUp() bool {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.sync.vi.SeekUp()
}

// SeekDown satisfies component.Scrollable.
func (v *viHandler) SeekDown() bool {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.sync.vi.SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (v *viHandler) SeekOffset() int {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return max(0, v.sync.vi.MaxSeekOffset()-v.sync.vi.SeekOffset())
}

// MaxSeekOffset satisfies component.Scrollable.
func (v *viHandler) MaxSeekOffset() int {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.sync.vi.MaxSeekOffset()
}

func (v *viHandler) Resize(width, height int) {
	v.copy.mu.Lock()
	v.copy.vi.Resize(width, height)
	v.copy.mu.Unlock()

	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	v.width = width
	v.height = height
	v.sync.vi.Resize(width, height)
}

func (v *viHandler) cursorAtScroll() term.Coordinates {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	return v.sync.vi.CursorAtScroll()
}

func (v *viHandler) setCursorAtScroll(pos term.Coordinates) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	v.viSetCursorAtScroll(pos)
}

func (v *viHandler) Draw(w term.Writer) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	v.sync.vi.Draw(w)

	if v.config.Debug {
		v.drawPromptLine(w)
	}
}

func (v *viHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.sync.vi.Cursor()
}

// Selection satisfies tui.Handler.
func (v *viHandler) Selection() (string, bool) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.sync.vi.Selection()
}

// Edit implements the following behavior (B is content not allowed to edit,
// A is content allowed to edit, E is edit):
//
//	┌──────┐     ┌──────┐     ┌──────┐
//	│BBBBB │  ∩  │EEEE  │  =  │      │
//	│ $ AA │     │      │     │      │ -> bell
//	└──────┘     └──────┘     └──────┘
//	┌──────┐     ┌──────┐     ┌──────┐
//	│BBBBBB│  ∩  │      │  =  │      │
//	│ $ AA │     │   EE │     │ $ EE │
//	└──────┘     └──────┘     └──────┘
//	┌──────┐     ┌──────┐     ┌──────┐
//	│BBBBBB│  ∩  │EEEEEE│  =  │      │
//	│ $ AA │     │   EE │     │ $ EE │
//	└──────┘     └──────┘     └──────┘
//	┌──────┐     ┌──────┐     ┌──────┐
//	│ $ AAA│  ∩  │   EEE│  =  │ $ EEE│
//	│AA    │     │EEEEE │     │EEEEE │
//	└──────┘     └──────┘     └──────┘
//	┌──────┐     ┌──────┐     ┌──────┐
//	│ $ A  │  ∩  │   EEE│  =  │ $ EEE│
//	│      │     │EEEEE │     │EEEEE │
//	└──────┘     └──────┘     └──────┘
//	┌──────┐     ┌──────┐     ┌──────┐
//	│ $ AAA│  ∩  │      │  =  │ $ AAA│
//	│AAAA  │     │ EEEEE│     │AEEEEE│
//	└──────┘     └──────┘     └──────┘
//	┌──────┐     ┌──────┐     ┌──────┐
//	│ $ AA │  ∩  │      │  =  │      │ -> bell
//	│      │     │   EE │     │      │
//	└──────┘     └──────┘     └──────┘
func (v *viHandler) Edit(ctx context.Context, start, end term.Coordinates, str string) (
	from, to term.Coordinates, old string,
) {
	v.editCopy(ctx, start, end, str)
	if screenContext := vtescreen.IsScreenContext(ctx); screenContext {
		v.vteParserEdited = true
		v.sync.vi.OnWillEdit(ctx, start, end, str)
		from, to, old = v.sync.editor.Edit(ctx, start, end, str)
		v.sync.vi.OnDidEdit(ctx, from, to, old)
		return
	}
	insert := start == end

	lastLineStart, lastLineEnd := v.lastPromptLine()
	var ok bool
	endForIntersection := end
	// inserts start==end so to calculate intersection we need to end.X++
	// otherwise there's never an intersection
	if insert {
		endForIntersection.X = 0
		endForIntersection.Y++
		lastLineEnd.X++
		if lastLineEnd.X >= v.width {
			lastLineEnd.Y++
		}
	}
	intersectionStart, intersectionEnd, ok := term.CoordinatesIntersection(
		start, endForIntersection, lastLineStart, lastLineEnd)
	if !ok {
		from = start
		to = start
		v.log(log.DebugLevel, "edit not allowed: last line: [%+v, %+v), "+
			"start: %+v, end: %+v, endForIntersection: %+v, str: %q",
			lastLineStart, lastLineEnd, start, end, endForIntersection, str)
		v.config.RingBell()
		return
	}

	/*v.log(log.TraceLevel, "edit: "+
	"start: %+v, end: %+v, str: %q, last line: [%+v, %+v), intersection: [%+v, %+v)",
	start, end, str, lastLineStart, lastLineEnd, intersectionStart, intersectionEnd) */

	oldStart := start
	if insert {
		start = intersectionStart
		end = start
	} else {
		start = intersectionStart
		end = intersectionEnd
	}

	// trim start to avoid editing prompt, only relevant for deletes
	start.X = v.remoteMoveTo(start)
	if insert {
		end.X = start.X
		endForIntersection.X = end.X + 1
	}
	from = start
	to = from

	defer func() {
		v.remoteFlush()
		v.sync.vi.OnDidEdit(ctx, from, to, old)
		/*v.log(log.TraceLevel, "edit return: from %+v to %+v", from, to)*/
	}()

	if start == end && str == "" {
		v.log(log.DebugLevel, "edit not allowed after prompt correction: last line: [%+v, %+v), "+
			"start: %+v, end: %+v, endForIntersection: %+v, str: %q",
			lastLineStart, lastLineEnd, start, end, endForIntersection, str)
		v.config.RingBell()
		return
	}

	v.sync.vi.OnWillEdit(ctx, start, end, str)
	oldCells, _, ok := v.sync.selector.Select(start, end)
	if ok {
		old = term.CellsToString(oldCells)
		if old != "" && strings.Count(old, " ") != len(old) {
			// Edit must maintain reversibility. Since shell
			// wraps lines automatically, we must remove newlines.
			old = strings.ReplaceAll(old, "\n", "")
			// copy user deletes to clipboard
			err := v.config.Clipboard.Copy(v.config.ClipboardRegister,
				clipboard.Data{Text: old})
			if err != nil {
				v.log(log.ErrorLevel, "copy data to clipboard: %v", err)
			}
		}
		//v.log(log.TraceLevel, "edit: delete effective old %q", old)
	}

	//v.log(log.TraceLevel, "edit: effective start %+v end %+v", start, end)
	view := v.sync.vi.CellView()
	rows := view.Rows()
	rowsToDelete := end.Y - start.Y
	for i := 0; i <= rowsToDelete; i++ {
		first := i == 0
		last := i == rowsToDelete
		line := start.Y + i

		var endX int
		if last {
			endX = end.X
		} else if line < rows {
			endX = view.Columns(line)
		}

		var startX int
		if first {
			startX = start.X
			to.X = from.X
		} else {
			to.X = 0
		}

		for j := 0; j < endX-startX; j++ {
			v.remote.deleteChar()
			to.X++
		}
		if !last {
			v.remote.conflate()
			to.Y++
		}
	}

	if str == "" {
		return
	}

	var prevWrapped bool
	to.X = from.X
	to.Y = from.Y
	for _, ch := range str {
		// skip parts of str that were skipped before due to prompt start or not
		// in allowed range
		_, _, ok := term.CoordinatesIntersection(start, endForIntersection, oldStart,
			term.Coordinates{Y: oldStart.Y, X: oldStart.X + 1})
		/*v.log(log.TraceLevel, "edit: check if str ch (%c) is part of allowed coordinates: start=%+v, "+
		  "end=%+v, oldStart=%+v, ok=%t", ch, start, endForIntersection, oldStart, ok)*/
		if !ok {
			if ch == '\n' {
				oldStart.Y++
				oldStart.X = 0
			} else {
				oldStart.X++
			}
			continue
		}
		if ch == '\n' {
			if prevWrapped {
				continue
			}
		}
		v.remote.insertChar(ch)
		if ch == '\n' {
			to.X = 0
			// see comment below
			if to.Y < v.height-1 {
				to.Y++
			}
		} else {
			to.X++
			if to.X == v.width {
				prevWrapped = true
				v.remote.wrapLine()
				to.X = 0
				// if we return the "correct" to.Y after wrapping the last line
				// vi's text.Cursor sets the window coordinates at height, so then
				// when parser handler scrolls up the content, since cursor uses
				// window coordinates, the cursor stays there, preventing further updates.
				// This doesn't happen on text files, because text.Cursor is initialized with Init
				// rather than InitPerformance, and so it automatically seeks **and corrects
				// coordinates**, if cursor is out of bounds.
				// If this method doesn't work well, we can always subscribe to scroll
				// and correct vi's cursor coordinates.
				if to.Y < v.height-1 {
					to.Y++
				}
				continue
			}
		}
		prevWrapped = false
	}
	return
}

func (v *viHandler) remoteFlush() {
	err := v.remote.flush()
	if err != nil {
		v.log(log.WarnLevel, "flush: %v", err)
	}
}

func (v *viHandler) handle(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		_, _ = v.copy.vi.Handle(ev)
		return v.sync.vi.Handle(ev)
	}

	switch ev.Mod {
	case 0:
		switch ev.Ch {
		case '$':
			if !v.sync.vi.IsEditMode() && !v.sync.vi.IsSearchMode() {
				// manage manually to avoid confusing shell blank cells
				// with end of line.
				v.scheduleAfterBell(false, v.remoteMoveToEndOfLine)
				handled = true
				return
			}
		case 'i':
			if !v.sync.vi.IsEditMode() && !v.sync.vi.IsSearchMode() {
				exit = true
				handled = true
				return
			}
		// ensure that repeat is not invoked, as underlying
		// buffer was not initialized with subscribe functionality,
		// which repeat is dependent upon
		case '.':
			if !v.sync.vi.IsEditMode() && !v.sync.vi.IsSearchMode() {
				return
			}
		}
		handled = true
		switch ev.Key {
		case term.KeyEnter:
			if !v.sync.vi.IsSearchMode() {
				v.scheduleAfterBell(false, func() {
					v.remote.linefeed()
					v.remoteFlush()
				})
				exit = true
				return
			}
		case term.KeyArrowUp:
			v.scheduleAfterBell(false, func() {
				v.remote.keyArrowUp()
				v.remoteFlush()
			})
			v.scheduleAfterBell(false, v.moveViToLastLineCharacter)
			return
		case term.KeyArrowDown:
			v.scheduleAfterBell(false, func() {
				v.remote.keyArrowDown()
				v.remoteFlush()
			})
			v.scheduleAfterBell(false, v.moveViToLastLineCharacter)
			return
		default:
		}
	case term.ModCtrl:
		handled = true
		switch ev.Ch {
		case 'k':
			v.scheduleAfterBell(false, func() {
				v.remote.keyArrowUp()
				v.remoteFlush()
			})
			v.scheduleAfterBell(false, v.moveViToLastLineCharacter)
			return
		case 'j':
			v.scheduleAfterBell(false, func() {
				v.remote.keyArrowDown()
				v.remoteFlush()
			})
			v.scheduleAfterBell(false, v.moveViToLastLineCharacter)
			return
		case 'c':
			exit = true
			return
		}
	}

	// use a copy of vi to know if event would be handled, and if it would be an edit
	v.edited = false
	v.copy.mu.Lock()
	_, handled = v.copy.vi.Handle(ev)
	v.copy.mu.Unlock()

	// schedule any potential edits after finding the start of the prompt:
	// this avoids race conditions when serializing handle with bell callbacks
	// and also prevents the main loop goroutine to not deadlock with the vte parser
	// goroutine. This cannot be performed during a call to Edit, because
	// the cursor logic heavily depends on the correct return values of Edit.
	v.scheduleAfterBell(v.edited, func() {
		v.vteParserEdited = false
		v.sync.vi.Handle(ev)
	})

	// correct cursor coordinates beyond last line so
	// when moving through graphical windows doesn't
	// leave cursor in an non-useful coordinate.
	//
	// After edits, content might have changed
	// use bell to synchronize to the last state change
	// and then move cursor to bounds.
	//
	// It's imperative that we serialize this with v.sync.vi.Handle
	// or else moveViToBounds will use a buffer
	// that has not been updated yet.
	//
	// This might put a lot of pressure on the event loop's
	// event processing, so let's keep an eye on it for now.
	v.scheduleAfterBell(false, func() {
		v.moveViToBounds()
	})
	return
}

func (v *viHandler) enterViMode(pos term.Coordinates) {
	pos.X = int(math.Max(float64(pos.X-1), float64(0)))

	// reset offset
	v.sync.scroll.SetOffset(term.Coordinates{})
	v.sync.scroll.Attributes = v.sync.vteScroll.Attributes

	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	v.viSetCursorAtScroll(pos)
	v.scheduleAfterBell(false, v.moveViToBounds)
}

func (v *viHandler) log(level log.Level, line string, params ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "vte.viHandler").
		Logf(level, line, params...)
}

func (v *viHandler) scheduleAfterBell(forceSchedule bool, cb func()) {
	// if cursor is above prompt line, then we shouldn't need to synchronize via bell;
	// the exception is if it would break callbacks execution order.
	lastPromptLineStart, _ := v.lastPromptLine()
	if !forceSchedule && v.sync.vi.CursorAtScroll().Y < lastPromptLineStart.Y &&
		v.comp.pendingCallbacks() == 0 {
		cb()
		return
	}

	ok := v.comp.scheduleBellCallback(func() {
		// bell handler does not lock because bell
		// is usually implemented as synchronous I/O
		v.sync.mu.Lock()
		defer v.sync.mu.Unlock()

		// NOTE: this is scheduled as a user callback on the
		// next event loop tick.
		cb()
	})
	if !ok {
		v.log(log.WarnLevel, "could not schedule sync trigger: too many events")
	}
}

func (v *viHandler) trimToLastValidColumn(pos term.Coordinates) term.Coordinates {
	lastLineStart, lastLineEnd := v.lastPromptLine()
	if pos.Y > lastLineEnd.Y || pos.Y < lastLineStart.Y {
		return lastLineEnd
	}
	if pos.Y == lastLineEnd.Y {
		pos.X = int(math.Min(float64(pos.X), float64(lastLineEnd.X)))
		return pos
	}
	return pos
}

func (v *viHandler) remoteMoveTo(target term.Coordinates) (actual int) {
	// it's assumed that prompt is at valid start of row
	start := v.comp.cursorAtScroll()

	// lastPromptLine's end is right exclusive, so it is the position a
	// new character is appended at. moveRight cannot reach it when the
	// shell's line editor is in vi mode: readline clamps forward-char to
	// the last character and rings the bell instead of moving, which
	// would land the insert one column too early.
	if _, lineEnd := v.lastPromptLine(); target == lineEnd && target != start {
		v.remote.moveEndOfLine()
		actual = target.X
		return
	}

	if start.Y == target.Y {
		// use current cursor x position, to take $ or other
		// shell prefixes into consideration
		if start.X >= target.X {
			actual = start.X
			return
		}
		for i := start.X; i < target.X; i++ {
			v.remote.moveRight()
		}
		actual = target.X
		return
	}

	offset := start.X
	for i := start.Y; i < target.Y; i++ {
		for j := 0; j < v.width-offset; j++ {
			v.remote.moveRight()
		}
		v.remote.cursorCRLF()
		offset = 0
	}

	for i := 0; i < target.X; i++ {
		v.remote.moveRight()
	}
	actual = target.X

	return
}

func (v *viHandler) moveViToBounds() {
	promptStart := v.comp.cursorAtScroll()
	pos := v.sync.vi.CursorAtScroll()

	_, endOfPromptLine := v.lastPromptLine()
	// correct past last line + 1
	if pos.Y > endOfPromptLine.Y+1 {
		endOfPromptLine.X++
		v.viSetCursorAtScroll(endOfPromptLine)
		return
	}

	// correct past last line + 1 to be at most x = 0
	if pos.Y == endOfPromptLine.Y+1 && pos.X != 0 {
		pos = term.Coordinates{Y: endOfPromptLine.Y + 1, X: 0}
		v.viSetCursorAtScroll(pos)
		return
	}

	// correct past last column, at last line, only if not in edit mode
	if !v.sync.vi.IsEditMode() {
		lastValidCol := endOfPromptLine.X - 1
		if promptStart.Y == pos.Y {
			lastValidCol = int(math.Max(float64(lastValidCol), float64(promptStart.X)))
		}
		if pos.Y == endOfPromptLine.Y && pos.X > lastValidCol {
			pos = term.Coordinates{Y: pos.Y, X: lastValidCol}
			v.viSetCursorAtScroll(pos)
			return
		}
	}

	// correct prior to prompt start
	if pos.Y == promptStart.Y && pos.X < promptStart.X {
		pos = term.Coordinates{Y: pos.Y, X: promptStart.X}
		v.viSetCursorAtScroll(pos)
		return
	}
}

func (v *viHandler) moveViToLastLineCharacter() {
	promptStart := v.comp.cursorAtScroll()
	// consider spaces = true because shell fills deleted columns
	// with a space, so last content column should not consider tail spaces
	// as content.
	_, pos := lastPromptLine(v.sync.vi.CellView(), v.width, promptStart.Y, true)
	// end coordinates are right exclusive
	if pos.X > 0 {
		pos.X--
	}
	if pos.Y == promptStart.Y {
		pos.X = int(math.Max(float64(promptStart.X), float64(pos.X)))
	}
	v.viSetCursorAtScroll(pos)
}

func (v *viHandler) lastPromptLine() (term.Coordinates, term.Coordinates) {
	return lastPromptLine(v.sync.vi.CellView(), v.width, v.comp.cursorAtScroll().Y,
		false /* include at most one space with prompt line */)
}

func (v *viHandler) remoteMoveToEndOfLine() {
	promptStart := v.comp.cursorAtScroll()
	pos := v.lastValidLineColumn(v.sync.vi.CursorAtScroll().Y)
	if promptStart.Y == pos.Y {
		pos.X = int(math.Max(float64(promptStart.X), float64(pos.X)))
	}
	v.viSetCursorAtScroll(pos)
}

func (v *viHandler) lastValidLineColumn(y int) term.Coordinates {
	cells := v.sync.vi.CellView().RawCells()
	if y >= len(cells) || len(cells[y]) == 0 {
		return term.Coordinates{Y: y}
	}

	for x := len(cells[y]) - 1; x >= 0; x-- {
		ch := cells[y][x].Ch
		if ch != vtescreen.DefaultChar && ch != ' ' {
			return term.Coordinates{Y: y, X: x}
		}
	}

	return term.Coordinates{Y: y}
}

func (v *viHandler) editCopy(ctx context.Context, start, end term.Coordinates, str string) {
	v.copy.mu.Lock()
	defer v.copy.mu.Unlock()

	v.copy.vi.CellEditor().Edit(ctx, start, end, str)
}

func (v *viHandler) viSetCursorAtScroll(pos term.Coordinates) {
	v.sync.vi.SetCursorAtScroll(pos)
	v.copy.mu.Lock()
	defer v.copy.mu.Unlock()
	v.copy.vi.SetCursorAtScroll(v.sync.vi.CursorAtScroll())
}

func (v *viHandler) drawPromptLine(w term.Writer) {
	from, to := v.lastPromptLine()
	for y := from.Y; y <= to.Y; y++ {
		xStart, xEnd := 0, v.sync.selector.Columns(y)
		for x := xStart; x < xEnd; x++ {
			pos := term.Coordinates{X: x, Y: y}
			posAtScreen, _ := v.sync.scroll.ScrollToWindowCoordinates(pos)
			if posAtScreen.Y < 0 || posAtScreen.Y >= v.height ||
				posAtScreen.X < 0 || posAtScreen.X >= v.width {
				continue
			}
			w.UnionAttributes(posAtScreen, term.Attributes{Attrs: term.AttrUnderline})
		}
	}
}

func (v *viHandler) setDefaultAttributes(attr term.Attributes) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	v.sync.vi.SetDefaultAttributes(attr)

	v.copy.mu.Lock()
	defer v.copy.mu.Unlock()
	v.copy.vi.SetDefaultAttributes(attr)
}

// used to know before calling v.sync.vi's Handle whether change is going
type copyEditor struct {
	v *viHandler
}

func (v copyEditor) Edit(ctx context.Context, start, end term.Coordinates, str string) (
	from, to term.Coordinates, old string,
) {
	v.v.edited = true
	return v.v.copy.editor.Edit(ctx, start, end, str)
}
