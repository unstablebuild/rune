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
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/sounds"
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

	// active is set while the vi view is on screen, in modal mode or
	// showing search results, which is when its cursor has to follow the
	// content that the parser scrolls into history.
	active bool

	// probe is how far this modal session trusts the program with the
	// keys that reach it.
	probe viProbe
	// pending holds the keys pressed while the probe waits for the bell.
	pending []viKey
	// entry is where the cursor was put on entering modal mode.
	entry term.Coordinates
	// resumed is set while a session entered before the key that left
	// the last one reached the shell has yet to send it a key: the
	// shell's cursor goes where the keys typed after that key put it, so
	// leaving again from entry sends nothing.
	resumed bool
	// bellAnswered is set while the keys that waited for the probe are
	// sent: the bell that answered it left the shell's cursor at the
	// start of its input, which is what the first of them waits for.
	bellAnswered bool
	// reading is set while a key that must not reach the shell runs.
	reading bool
	// refused is told, on the event loop, that the session turned
	// read-only; probed is whether the program was rung first.
	refused func(probed bool)
	// syncedCommand is the Component.ShellCommand seq of the last command
	// a probe succeeded in, zero for none.
	syncedCommand uint64

	// typed holds what the keys pressed outside modal mode send the
	// program while a key that left modal mode has yet to reach it, such
	// as i, whose cursor move they must follow. It has a lock of its own:
	// the tick that writes them may run while sync.mu is held.
	typed struct {
		mu   sync.Mutex
		keys []*typedKeys
		// holding is set while keys is not empty, so that typing
		// outside modal mode takes no lock otherwise.
		holding atomic.Bool
	}

	// keeep a copy of vi, which reads the same contents but edits
	// nothing, so it can be accessed synchronously and provide
	// correct returned handled in Handle, and tell which keys edit.
	copy struct {
		mu sync.Mutex
		vi *vi.Vi
	}

	// frozen is the vi over a copy of the alternate screen, which modal
	// mode shows over a full-screen program. The program keeps drawing,
	// and nothing Rune could send it would edit that screen, so the copy
	// never changes and no key reaches the program.
	frozen struct {
		on     bool
		vi     *vi.Vi
		scroll *component.Scroll
		buf    *cell.Buffer
	}
}

// for dependency injection purposes
type parentComponent interface {
	PrimaryScroll() (*component.Scroll, sync.Locker)
	URI() workspaceapi.URI
	cursorAtScroll() term.Coordinates
	// scheduleBellCallback calls dropped instead of callback if it
	// discards callback.
	scheduleBellCallback(callback, dropped func()) bool
	pendingCallbacks() int
	systemCanDispatchBell(callback func(error))
	// shellCommand is Component.ShellCommand for callers that hold the
	// PrimaryScroll Locker.
	shellCommand() (seq uint64, running, reportedStatus bool)
	WriteToPty(data []byte) error
}

// viProbe is how far a modal session trusts the program with the keys
// that reach it. The first such key rings the bell: until a shell
// answers it, its cursor is not known to be at the start of its input,
// where every key that reaches it is sent from.
type viProbe int

const (
	// viUnprobed sent the program nothing yet: the session is
	// read-only, and the first key that would reach it probes it.
	viUnprobed viProbe = iota
	// viProbing waits for the bell, and so do the keys pressed since.
	viProbing
	// viSynced got the bell: every key that reaches the shell waits for
	// one of its own.
	viSynced
	// viRefused got no bell, or must not ring one: the session stays
	// read-only.
	viRefused
)

// viKey is a key pressed in modal mode and what it does.
type viKey struct {
	ev   term.Event
	kind viKeyKind
	// typed, for a key that leaves modal mode toward the program, is
	// what the keys pressed after it send.
	typed *typedKeys
}

// typedKeys is what the keys pressed after a key that left modal mode
// send the program, in the order pressed.
type typedKeys struct {
	data []byte
	// sent is set once the key that left reached the program, or once
	// it never will.
	sent bool
}

type viKeyKind int

const (
	// viKeyNone is not handled.
	viKeyNone viKeyKind = iota
	// viKeyRead moves, scrolls, searches, selects or yanks.
	viKeyRead
	viKeyEdit
	viKeyEndOfLine
	// viKeyAppend appends after the line, as A does.
	viKeyAppend
	viKeyEnter
	viKeyUp
	viKeyDown
	// viKeyExit leaves modal mode, moving the shell's cursor to the vi
	// cursor.
	viKeyExit
)

func (v *viHandler) init(comp *Component, config Config) {
	v.doInit(comp, config)
	v.remote = ptyWriterRemote(comp, config.ScheduleNextTick)
	if log.IsLevelEnabled(log.TraceLevel) {
		v.remote = newLoggingRemote(v.remote)
	}
	comp.parserHandler.historyScrolled = v.historyScrolled
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
	copyBuffer.WithEditor(copyEditor{v: v})

	v.frozen.buf = new(cell.Buffer)
	v.frozen.buf.InitPerformance(80, 80, vtescreen.DefaultChar)
	v.frozen.scroll = new(component.Scroll)
	v.frozen.scroll.InitPerformance(v.frozen.buf)
	v.frozen.scroll.InvertOffset = true
	v.frozen.vi = new(vi.Vi)
	v.frozen.vi.InitWithScroll(v.frozen.scroll, comp.URI(), text.IndentRuneTab, 0, opts...)
	v.frozen.buf.WithEditor(frozenEditor{v: v})

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
	// the parser writes the cells without going through any editor,
	// so the copy must read them where the sync vi does
	copyBuffer.WithView(v.sync.selector)

	v.comp = comp
	v.config = config
}

func (v *viHandler) Handle(ev term.Event) (exit, handled bool) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	exit, handled = v.handle(ev)
	if exit {
		for _, vi := range []*vi.Vi{v.sync.vi, v.copy.vi, v.frozen.vi} {
			vi.Unselect()
			vi.Search("")
			vi.SetNormalMode()
		}
	}
	return
}

// cur returns the vi on screen. v.sync.mu must be held.
func (v *viHandler) cur() *vi.Vi {
	if v.frozen.on {
		return v.frozen.vi
	}
	return v.sync.vi
}

// SeekUp satisfies component.Scrollable.
func (v *viHandler) SeekUp() bool {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.cur().SeekUp()
}

// SeekDown satisfies component.Scrollable.
func (v *viHandler) SeekDown() bool {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.cur().SeekDown()
}

// SeekOffset satisfies component.Scrollable.
func (v *viHandler) SeekOffset() int {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	vi := v.cur()
	return max(0, vi.MaxSeekOffset()-vi.SeekOffset())
}

// MaxSeekOffset satisfies component.Scrollable.
func (v *viHandler) MaxSeekOffset() int {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.cur().MaxSeekOffset()
}

func (v *viHandler) Resize(width, height int) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	// the copy reads the cells the parser writes under sync.mu
	v.copy.mu.Lock()
	v.copy.vi.Resize(width, height)
	v.copy.mu.Unlock()

	v.width = width
	v.height = height
	v.sync.vi.Resize(width, height)
	v.frozen.vi.Resize(width, height)
}

func (v *viHandler) cursorAtScroll() term.Coordinates {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	return v.cur().CursorAtScroll()
}

func (v *viHandler) setCursorAtScroll(pos term.Coordinates) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	if v.frozen.on {
		v.frozen.vi.SetCursorAtScroll(pos)
		return
	}
	v.viSetCursorAtScroll(pos)
}

func (v *viHandler) Draw(w term.Writer) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	v.cur().Draw(w)

	if v.config.Debug && !v.frozen.on {
		v.drawPromptLine(w)
	}
}

func (v *viHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.cur().Cursor()
}

// Selection satisfies tui.Handler.
func (v *viHandler) Selection() (string, bool) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	return v.cur().Selection()
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
	if screenContext := vtescreen.IsScreenContext(ctx); screenContext {
		v.vteParserEdited = true
		v.sync.vi.OnWillEdit(ctx, start, end, str)
		// through the copy vi's editor, so that its cursor follows the
		// edit of the cells it reads
		v.copy.mu.Lock()
		from, to, old = v.copy.vi.CellEditor().Edit(ctx, start, end, str)
		v.copy.mu.Unlock()
		v.sync.vi.OnDidEdit(ctx, from, to, old)
		return
	}
	if v.reading {
		// the copy vi took the key for one that does not edit
		from, to = start, start
		v.log(log.DebugLevel, "edit not allowed before the shell answered: start: %+v, end: %+v, str: %q",
			start, end, str)
		v.config.Sounds.Play(sounds.Error)
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
		v.config.Sounds.Play(sounds.Error)
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
		v.config.Sounds.Play(sounds.Error)
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
			to.Y++
		} else {
			to.X++
			if to.X == v.width {
				prevWrapped = true
				v.remote.wrapLine()
				to.X = 0
				// to is in content coordinates even when this wrap scrolls the
				// screen: historyScrolled moves the cursor with the content.
				to.Y++
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
	if v.frozen.on {
		return v.handleFrozen(ev)
	}
	if ev.Type != term.EventKey {
		_, _ = v.copy.vi.Handle(ev)
		return v.sync.vi.Handle(ev)
	}

	k, exit, handled := v.classify(ev)
	if k.kind == viKeyNone {
		return
	}
	if v.resumed {
		if k.kind == viKeyExit && !v.reachesShell(k) {
			// what is typed next joins what was typed after the key
			// that left, behind its cursor move
			return
		}
		v.resumed = false
	}
	switch v.probe {
	case viSynced:
		v.send(v.leave(k))
	case viProbing:
		v.pending = append(v.pending, v.leave(k))
	case viUnprobed:
		if !v.reachesShell(k) {
			v.read(k)
			return
		}
		if _, running, reported := v.comp.shellCommand(); running && reported {
			// it is no shell that reads the keys, and the bell's ctrl-g
			// may do something else to it: Claude Code opens $EDITOR
			v.refuse(false)
			v.cancelCommand()
			return k.kind == viKeyExit, handled
		}
		v.probe = viProbing
		v.pending = append(v.pending[:0], v.leave(k))
		v.comp.systemCanDispatchBell(v.probed)
	case viRefused:
		if k.kind == viKeyExit || !v.reachesShell(k) {
			v.read(k)
			return
		}
		v.config.Sounds.Play(sounds.Error)
		v.cancelCommand()
		return false, handled
	}
	return
}

// leave makes the keys pressed after k, if k leaves modal mode, wait for
// k to reach the program. v.sync.mu must be held.
func (v *viHandler) leave(k viKey) viKey {
	if k.kind != viKeyExit && k.kind != viKeyEnter {
		return k
	}
	k.typed = new(typedKeys)
	v.typed.mu.Lock()
	defer v.typed.mu.Unlock()
	v.typed.keys = append(v.typed.keys, k.typed)
	v.typed.holding.Store(true)
	return k
}

// hold holds raw, what a key pressed outside modal mode sends the
// program, behind the keys that left modal mode and have yet to reach
// it, and reports whether it did.
func (v *viHandler) hold(raw []byte) bool {
	if !v.typed.holding.Load() {
		return false
	}
	v.typed.mu.Lock()
	defer v.typed.mu.Unlock()
	last := len(v.typed.keys) - 1
	if last < 0 {
		return false
	}
	v.typed.keys[last].data = append(v.typed.keys[last].data, raw...)
	return true
}

// sent tells that the key that left modal mode with typed reached the
// program, or never will. What was typed after it is written on a tick,
// which runs after those that write what the key sent, once what was
// typed after the keys that left before it is.
func (v *viHandler) sent(typed *typedKeys) {
	v.typed.mu.Lock()
	typed.sent = true
	v.typed.mu.Unlock()
	if !v.config.ScheduleNextTick(v.release) {
		v.release()
	}
}

// release writes, oldest first, what was typed after the keys that left
// modal mode, up to the first of them yet to reach the program.
func (v *viHandler) release() {
	v.typed.mu.Lock()
	defer v.typed.mu.Unlock()
	for len(v.typed.keys) > 0 && v.typed.keys[0].sent {
		if data := v.typed.keys[0].data; len(data) > 0 {
			if err := v.comp.WriteToPty(data); err != nil {
				v.log(log.ErrorLevel, "write to pty: %v", err)
			}
		}
		v.typed.keys[0] = nil
		v.typed.keys = v.typed.keys[1:]
	}
	v.typed.holding.Store(len(v.typed.keys) > 0)
}

// handleFrozen runs ev on the frozen screen, which nothing edits and
// whose program gets no key.
func (v *viHandler) handleFrozen(ev term.Event) (exit, handled bool) {
	if ev.Type != term.EventKey {
		return v.frozen.vi.Handle(ev)
	}
	k, exit, handled := v.classifyOn(v.frozen.vi, ev)
	switch k.kind {
	case viKeyEndOfLine:
		cells := v.frozen.vi.CellView().RawCells()
		v.frozen.vi.SetCursorAtScroll(lastValidColumn(cells, v.frozen.vi.CursorAtScroll().Y))
	case viKeyEdit:
		// the editor refused it; drop what the key left pending
		v.frozen.vi.SetNormalMode()
		fallthrough
	case viKeyEnter, viKeyUp, viKeyDown:
		v.config.Sounds.Play(sounds.Error)
		return false, handled
	}
	return exit, handled
}

// classify tells what the key ev does. Only the copy vi can tell whether
// it edits, so it runs ev to find out. Its mode is the one ev meets: the
// sync vi runs keys behind bells.
func (v *viHandler) classify(ev term.Event) (k viKey, exit, handled bool) {
	v.copy.mu.Lock()
	defer v.copy.mu.Unlock()
	if v.probe == viUnprobed || v.probe == viRefused {
		// the sync vi ran every key the copy did here, but the copy's
		// edits, which do nothing, did not move its cursor
		v.copy.vi.SetCursorAtScroll(v.sync.vi.CursorAtScroll())
	}
	return v.classifyOn(v.copy.vi, ev)
}

// classifyOn tells what the key ev does by running it on vi, whose editor
// must record the edits it gets in v.edited instead of making them.
func (v *viHandler) classifyOn(vi *vi.Vi, ev term.Event) (k viKey, exit, handled bool) {
	k.ev = ev
	search := vi.IsSearchMode()
	normal := !vi.IsEditMode() && !search
	switch ev.Mod {
	case 0:
		switch {
		case ev.Ch == '$' && normal:
			k.kind = viKeyEndOfLine
			return k, false, true
		case ev.Ch == 'i' && normal:
			k.kind = viKeyExit
			return k, true, true
		// ensure that repeat is not invoked, as underlying
		// buffer was not initialized with subscribe functionality,
		// which repeat is dependent upon
		case ev.Ch == '.' && normal:
			return k, false, false
		case ev.Key == term.KeyEnter && !search:
			k.kind = viKeyEnter
			return k, true, true
		case ev.Key == term.KeyArrowUp:
			k.kind = viKeyUp
			return k, false, true
		case ev.Key == term.KeyArrowDown:
			k.kind = viKeyDown
			return k, false, true
		}
	case term.ModCtrl:
		switch ev.Ch {
		case 'k':
			k.kind = viKeyUp
			return k, false, true
		case 'j':
			k.kind = viKeyDown
			return k, false, true
		case 'c':
			k.kind = viKeyExit
			return k, true, true
		}
	}

	appends := ev.Mod == 0 && ev.Ch == 'A' && vi.IsNormalMode()
	v.edited = false
	_, handled = vi.Handle(ev)
	k.kind = viKeyRead
	if appends {
		k.kind = viKeyAppend
		appendAfterLine(vi)
	}
	if v.edited {
		k.kind = viKeyEdit
	}
	return k, false, handled
}

// reachesShell reports whether k needs the shell's cursor at the start of
// its input.
func (v *viHandler) reachesShell(k viKey) bool {
	switch k.kind {
	case viKeyRead, viKeyEndOfLine, viKeyAppend:
		return false
	case viKeyExit:
		// leaving only moves the shell's cursor when the vi cursor was
		// moved along its input
		pos := v.sync.vi.CursorAtScroll()
		from, to := v.lastPromptLine()
		return pos != v.entry && pos.Y >= from.Y && pos.Y <= to.Y
	}
	return true
}

// read runs k, which must not reach the shell, on the vi alone.
func (v *viHandler) read(k viKey) {
	switch k.kind {
	case viKeyRead, viKeyAppend:
		v.reading = true
		v.sync.vi.Handle(k.ev)
		v.reading = false
		if k.kind == viKeyAppend {
			appendAfterLine(v.sync.vi)
		}
		v.keepInContent()
	case viKeyEndOfLine:
		// unlike remoteMoveToEndOfLine, which clamps to the start of the
		// prompt, only known once the shell answered
		v.viSetCursorAtScroll(v.lastValidLineColumn(v.sync.vi.CursorAtScroll().Y))
	}
}

// keepInContent keeps the cursor off the blank rows below the content,
// such as those under a shell's prompt, which a motion like G or j
// reaches.
func (v *viHandler) keepInContent() {
	cells := v.sync.vi.CellView().RawCells()
	last := len(cells) - 1
	for last > 0 && blankRow(cells[last]) {
		last--
	}
	if pos := v.sync.vi.CursorAtScroll(); pos.Y > last {
		end := v.lastValidLineColumn(last)
		v.viSetCursorAtScroll(term.Coordinates{Y: last, X: min(pos.X, end.X)})
	}
}

func blankRow(row []term.Cell) bool {
	for _, c := range row {
		if c.Ch != vtescreen.DefaultChar && c.Ch != ' ' {
			return false
		}
	}
	return true
}

// appendAfterLine puts the cursor of vi, which A left at the end of its
// row, after the last cell a program wrote there: past it, the cells that
// make the row as wide as the screen are no part of the line, and a shell
// appends nothing there.
func appendAfterLine(vi *vi.Vi) {
	pos := vi.CursorAtScroll()
	cells := vi.CellView().RawCells()
	if pos.Y >= len(cells) {
		return
	}
	row := cells[pos.Y]
	x := min(pos.X, len(row))
	for x > 0 && row[x-1].Ch == vtescreen.DefaultChar {
		x--
	}
	vi.SetCursorAtScroll(term.Coordinates{Y: pos.Y, X: x})
}

// send runs k once the shell answered the probe: whatever reaches it
// waits for a bell, which puts its cursor at the start of its input.
func (v *viHandler) send(k viKey) {
	switch k.kind {
	case viKeyEndOfLine:
		// manage manually to avoid confusing shell blank cells
		// with end of line.
		v.scheduleAfterBell(false, v.remoteMoveToEndOfLine)
	case viKeyExit:
		v.leaveAfterBell(k, func() {
			pos := v.trimToLastValidColumn(v.sync.vi.CursorAtScroll())
			v.log(log.TraceLevel, "call scheduled cleanup of vi position to comp: %+v", pos)
			v.remoteMoveTo(pos)
			v.remoteFlush()
		})
	case viKeyEnter:
		v.leaveAfterBell(k, func() {
			v.remote.linefeed()
			v.remoteFlush()
		})
	case viKeyUp:
		v.scheduleAfterBell(false, func() {
			v.remote.keyArrowUp()
			v.remoteFlush()
		})
		v.scheduleAfterBell(false, v.moveViToLastLineCharacter)
	case viKeyDown:
		v.scheduleAfterBell(false, func() {
			v.remote.keyArrowDown()
			v.remoteFlush()
		})
		v.scheduleAfterBell(false, v.moveViToLastLineCharacter)
	case viKeyRead, viKeyEdit, viKeyAppend:
		// schedule any potential edits after finding the start of the prompt:
		// this avoids race conditions when serializing handle with bell callbacks
		// and also prevents the main loop goroutine to not deadlock with the vte parser
		// goroutine. This cannot be performed during a call to Edit, because
		// the cursor logic heavily depends on the correct return values of Edit.
		v.scheduleAfterBell(k.kind == viKeyEdit, func() {
			v.vteParserEdited = false
			v.sync.vi.Handle(k.ev)
			if k.kind == viKeyAppend {
				appendAfterLine(v.sync.vi)
			}
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
		v.scheduleAfterBell(false, v.moveViToBounds)
	}
}

// probed runs on the event loop once the shell answered the probe's
// bell, or did not in time.
func (v *viHandler) probed(err error) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()

	pending := v.pending
	v.pending = nil
	if err != nil {
		v.log(log.WarnLevel, "modal mode is read-only: %v", err)
		v.refuse(true)
		// the copy ran the keys that waited, edits included: run again
		// those that do not edit, from where the sync vi is
		v.cancelCommand()
		for _, k := range pending {
			if k.typed != nil {
				v.sent(k.typed)
			}
			k, _, _ = v.classify(k.ev)
			if k.kind == viKeyExit || !v.reachesShell(k) {
				v.read(k)
			} else {
				v.cancelCommand()
			}
		}
		return
	}
	v.probe = viSynced
	if seq, running, _ := v.comp.shellCommand(); running {
		v.syncedCommand = seq
	}
	// the bell left the shell's cursor at the start of its input: keep
	// the vi cursor off the prompt before it, as entering always did
	v.moveViToBounds()
	v.bellAnswered = true
	for _, k := range pending {
		v.send(k)
	}
	v.bellAnswered = false
}

// refuse turns the session read-only. The user is told on the next
// tick, since sync.mu is held.
func (v *viHandler) refuse(probed bool) {
	v.probe = viRefused
	v.config.Sounds.Play(sounds.Error)
	if v.refused != nil {
		v.config.ScheduleNextTick(func() { v.refused(probed) })
	}
}

// cancelCommand puts both vis in normal mode at the sync vi's cursor,
// dropping what a refused key left pending on either.
func (v *viHandler) cancelCommand() {
	v.sync.vi.SetNormalMode()
	v.copy.mu.Lock()
	v.copy.vi.SetNormalMode()
	v.copy.mu.Unlock()
	v.viSetCursorAtScroll(v.sync.vi.CursorAtScroll())
}

// escReachesCommand reports whether a lone <esc> may go to the command
// running at the shell's prompt: no probe succeeded in it, which proves
// that a line editor reads the keys, where a lone <esc> is a Meta
// prefix.
func (v *viHandler) escReachesCommand() bool {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	seq, running, _ := v.comp.shellCommand()
	return running && seq != v.syncedCommand
}

func (v *viHandler) enterViMode(pos term.Coordinates) {
	pos.X = int(math.Max(float64(pos.X-1), float64(0)))

	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	v.active = true
	v.sync.scroll.Attributes = v.sync.vteScroll.Attributes
	v.resumed = v.leaving()
	if v.resumed {
		// pos is where the shell's cursor was before that key moves it
		// to the vi cursor: the session goes on from there instead, and
		// its keys wait for that key's as they did before it
		v.entry = v.sync.vi.CursorAtScroll()
		return
	}
	v.sync.scroll.SetOffset(term.Coordinates{})
	v.viSetCursorAtScroll(pos)
	v.entry = v.sync.vi.CursorAtScroll()
	// a probe still waiting for its bell answers for this session too
	if v.probe != viProbing {
		v.probe = viUnprobed
	}
}

// leaving reports whether a key that left modal mode, and the keys typed
// after it, have yet to reach the shell, whose cursor it moves to the vi
// cursor unless the session turned read-only. v.sync.mu must be held.
func (v *viHandler) leaving() bool {
	return (v.probe == viProbing || v.probe == viSynced) && v.typed.holding.Load()
}

// freeze enters modal mode over cells, a copy of the screen a full-screen
// program draws on, with the cursor at pos, the program's. The copy
// stays as it is until thaw, and no key reaches the program.
func (v *viHandler) freeze(cells [][]term.Cell, pos term.Coordinates) {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	if len(cells) == 0 {
		cells = [][]term.Cell{{}}
	}
	v.frozen.buf.ResetCells(cells)
	v.frozen.scroll.SetOffset(term.Coordinates{})
	v.frozen.scroll.Attributes = v.sync.vteScroll.Attributes
	v.frozen.on = true
	v.frozen.vi.SetCursorAtScroll(pos)
}

// thaw leaves the frozen screen, if modal mode is over one.
func (v *viHandler) thaw() {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	v.frozen.on = false
}

// deactivate stops the vi cursor from following scrolled content. It must
// not go through scheduleAfterBell: another bell handshake would send the
// shell a ctrl-a after the cursor move that leaving modal mode schedules.
func (v *viHandler) deactivate() {
	v.sync.mu.Lock()
	defer v.sync.mu.Unlock()
	v.active = false
}

// historyScrolled moves the vi cursor count rows up the content, to account
// for the screen having scrolled count rows up into history, but never above
// the top row of the window. It does nothing while the vi view is not on
// screen. v.sync.mu must be held.
func (v *viHandler) historyScrolled(count int) {
	if !v.active {
		return
	}
	// Invariant: once the shell's redraw has been parsed, the vi cursor sits
	// on the content where the user is typing, inside lastPromptLine.
	// Otherwise the next Edit is rejected with a bell, or reaches the shell
	// at the wrong row. text.Cursor stores window coordinates, and with
	// InvertOffset the window stays anchored to the bottom of the buffer, so
	// every scroll into history moves the cursor count rows down the content.
	// Edit cannot compensate in advance: whether and when the screen scrolls
	// depends on the shell's redraw, which is parsed after Edit returns.
	pos := v.sync.vi.CursorAtScroll()
	pos.Y -= count
	if win, _ := v.sync.scroll.ScrollToWindowCoordinates(pos); win.Y < 0 {
		// like vi, keep the cursor on screen when its row scrolls away
		pos.Y -= win.Y
	}
	v.viSetCursorAtScroll(pos)
}

func (v *viHandler) log(level log.Level, line string, params ...any) {
	if !log.IsLevelEnabled(level) {
		return
	}
	log.WithField(logging.KeyClass, "vte.viHandler").
		Logf(level, line, params...)
}

func (v *viHandler) scheduleAfterBell(forceSchedule bool, cb func()) {
	v.scheduleAfterBellOrDrop(forceSchedule, cb, nil)
}

// leaveAfterBell runs cb, which sends k, a key that leaves modal mode, as
// scheduleAfterBell does, then writes what was typed after k, which it
// also writes if cb never runs.
func (v *viHandler) leaveAfterBell(k viKey, cb func()) {
	sent := func() { v.sent(k.typed) }
	v.scheduleAfterBellOrDrop(false, func() {
		cb()
		sent()
	}, sent)
}

// scheduleAfterBellOrDrop is scheduleAfterBell, but calls dropped, if
// set, instead of cb if cb never runs.
func (v *viHandler) scheduleAfterBellOrDrop(forceSchedule bool, cb, dropped func()) {
	if v.bellAnswered && v.comp.pendingCallbacks() == 0 {
		v.bellAnswered = false
		cb()
		return
	}
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
	}, dropped)
	if !ok {
		v.log(log.WarnLevel, "could not schedule sync trigger: too many events")
		if dropped != nil {
			dropped()
		}
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
	return lastValidColumn(v.sync.vi.CellView().RawCells(), y)
}

// lastValidColumn returns the last cell of row y that is not blank, or
// its first one.
func lastValidColumn(cells [][]term.Cell, y int) term.Coordinates {
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
	v.frozen.vi.SetDefaultAttributes(attr)
}

// copyEditor is the copy vi's editor. It only records that the key being
// run would edit, before v.sync.vi runs it, and passes on the parser's
// edits.
type copyEditor struct {
	v *viHandler
}

func (v copyEditor) Edit(ctx context.Context, start, end term.Coordinates, str string) (
	from, to term.Coordinates, old string,
) {
	if vtescreen.IsScreenContext(ctx) {
		return v.v.sync.editor.Edit(ctx, start, end, str)
	}
	v.v.edited = true
	return start, start, ""
}

// frozenEditor is the frozen screen's editor, which edits nothing: like
// copyEditor, it records that the key being run would.
type frozenEditor struct {
	v *viHandler
}

func (v frozenEditor) Edit(_ context.Context, start, _ term.Coordinates, _ string) (
	from, to term.Coordinates, old string,
) {
	v.v.edited = true
	return start, start, ""
}
