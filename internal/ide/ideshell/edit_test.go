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

package ideshell

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
)

// stubEditor is a minimal command.Editor for tests. It records every
// event delivered to its EditHandler and lets the test invoke an
// optional handle function to mutate the buffer (e.g. append a rune)
// the way a real editor would.
type stubEditor struct {
	seen *[]term.Event
	// initialCursor is recorded by SetCursorAtScroll so tests can
	// assert the host seeded the cursor correctly.
	initialCursor *term.Coordinates
}

func (s stubEditor) Edit(buf *cell.Buffer) command.EditHandler {
	return &stubEditHandler{
		buf:           buf,
		seen:          s.seen,
		initialCursor: s.initialCursor,
	}
}

type stubEditHandler struct {
	buf           *cell.Buffer
	seen          *[]term.Event
	initialCursor *term.Coordinates
	width         int
}

func TestHistoryDirSupportsArrowAndControlAliases(t *testing.T) {
	tests := []struct {
		name string
		ev   term.Event
		up   bool
	}{
		{"arrow up", term.Event{Type: term.EventKey, Key: term.KeyArrowUp}, true},
		{"ctrl-k", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'k'}, true},
		{"ctrl-p", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'p'}, true},
		{"arrow down", term.Event{Type: term.EventKey, Key: term.KeyArrowDown}, false},
		{"ctrl-j", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'j'}, false},
		{"ctrl-n", term.Event{Type: term.EventKey, Mod: term.ModCtrl, Ch: 'n'}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up, ok := historyDir(tt.ev)
			require.True(t, ok)
			require.Equal(t, tt.up, up)
		})
	}
}

func (s *stubEditHandler) Resize(width, _ int) { s.width = width }
func (s *stubEditHandler) Draw(w term.Writer) {
	x, y := 0, 0
	for _, r := range s.buf.String() {
		if s.width > 0 && x >= s.width {
			x = 0
			y++
		}
		w.SetCell(term.Coordinates{X: x, Y: y}, term.Cell{Ch: r})
		x++
	}
}
func (s *stubEditHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{X: s.buf.Columns(0)},
		term.CursorStyleSteadyBar, true
}
func (s *stubEditHandler) CursorAtScroll() term.Coordinates {
	return term.Coordinates{X: s.buf.Columns(0)}
}
func (s *stubEditHandler) SetCursorAtScroll(pos term.Coordinates) bool {
	if s.initialCursor != nil {
		*s.initialCursor = pos
	}
	return true
}
func (s *stubEditHandler) Selection() (string, bool) { return "", false }
func (s *stubEditHandler) Handle(ev term.Event) (bool, bool) {
	if s.seen != nil {
		*s.seen = append(*s.seen, ev)
	}
	if ev.Type != term.EventKey {
		return false, false
	}
	// A real vi/modeless editor consumes <tab> (it inserts
	// indentation). The shell wrapper must therefore intercept <tab>
	// before delegating, the same way a VTE owns <tab> and never
	// forwards it to the running program.
	if ev.Key == term.KeyTab && ev.Mod == 0 {
		s.buf.WriteString("\t")
		return false, true
	}
	// vi's insert mode consumes <c-r> (insert register). The shell
	// wrapper must therefore intercept <c-r> for reverse-history
	// search before delegating, the same way a VTE owns <c-r>.
	if ev.Mod == term.ModCtrl && ev.Ch == 'r' {
		return false, true
	}
	// A real editor consumes arrow keys and control-key aliases as
	// cursor motion. The shell wrapper must intercept these for
	// history cycling before the editor sees them.
	if ev.Mod == 0 &&
		(ev.Key == term.KeyArrowUp || ev.Key == term.KeyArrowDown) {
		return false, true
	}
	if ev.Mod == term.ModCtrl &&
		(ev.Ch == 'j' || ev.Ch == 'k' || ev.Ch == 'n' || ev.Ch == 'p') {
		return false, true
	}
	switch ev.Key {
	case term.KeyBackspace:
		cols := s.buf.Columns(0)
		if cols > 0 {
			s.buf.DeleteCell(term.Coordinates{X: cols - 1})
		}
		return false, true
	case term.KeySpace:
		s.buf.WriteString(" ")
		return false, true
	}
	if ev.Ch != 0 {
		s.buf.WriteString(string(ev.Ch))
		return false, true
	}
	return false, false
}

var _ command.EditHandler = (*stubEditHandler)(nil)

func newEditTestHandler(t *testing.T, editor command.Editor) *Handler {
	t.Helper()
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		editor,
		Config{
			MaxHistory: 100,
		},
	)
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func feedRunes(h *Handler, seq string) {
	for _, c := range seq {
		h.Handle(term.Event{Type: term.EventKey, Ch: c})
	}
}

// renderFrame draws the handler to a string buffer of its current size
// and returns the rendered text, one row per line.
func renderFrame(t *testing.T, h *Handler) string {
	t.Helper()
	w := term.NewStringWriter(h.width, h.height)
	h.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

func TestSignatureHelpHintOnOpenParen(t *testing.T) {
	helper := &sigHelperCmd{label: "Println(a ...any) (n int, err error)", ok: true}
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{MaxHistory: 100, DisableShellInterpreter: helper},
	)
	t.Cleanup(func() { _ = h.Close() })
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "fmt.Println(")
	require.True(t, h.sigActive, "hint should activate after typing (")
	require.Equal(t, "fmt.Println(", helper.gotLine)
	require.Equal(t, len("fmt.Println("), helper.gotCol)
	require.Contains(t, renderFrame(t, h), "Println(a ...any)")

	// Typing ) dismisses the hint immediately.
	h.Handle(term.Event{Type: term.EventKey, Ch: ')'})
	require.False(t, h.sigActive)
	require.NotContains(t, renderFrame(t, h), "Println(a ...any)")
}

func TestSignatureHelpHintNeverStealsKeys(t *testing.T) {
	helper := &sigHelperCmd{label: "f(x int)", ok: true}
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{MaxHistory: 100, DisableShellInterpreter: helper},
	)
	t.Cleanup(func() { _ = h.Close() })
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "f(")
	// The "(" was consumed by the editor as a normal rune, not stolen
	// by the hint machinery, so it appears in the buffer.
	require.Equal(t, "f(", h.editBuf.String())
	require.False(t, h.searching, "signature help must not enter search/overlay mode")
}

// newSigHelpHandler returns a handler whose fallback answers signature
// help, sized so the hint band renders.
func newSigHelpHandler(t *testing.T) *Handler {
	t.Helper()
	helper := &sigHelperCmd{label: "Println(a ...any) (n int, err error)", ok: true}
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{MaxHistory: 100, DisableShellInterpreter: helper},
	)
	t.Cleanup(func() { _ = h.Close() })
	h.Resize(testWidthH, testHeight)
	return h
}

func TestSignatureHelpHintClearsOnSubmit(t *testing.T) {
	h := newSigHelpHandler(t)
	feedRunes(h, "fmt.Println(")
	require.True(t, h.sigActive)

	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.False(t, h.sigActive, "submitting must clear the hint")
	require.NotContains(t, renderFrame(t, h), "Println(a ...any)")
}

func TestSignatureHelpHintClearsOnCtrlC(t *testing.T) {
	h := newSigHelpHandler(t)
	feedRunes(h, "fmt.Println(")
	require.True(t, h.sigActive)

	h.Handle(term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl})
	require.False(t, h.sigActive, "<c-c> must clear the hint")
	require.NotContains(t, renderFrame(t, h), "Println(a ...any)")
}

func TestSignatureHelpHintClearsWhenLineEmptied(t *testing.T) {
	h := newSigHelpHandler(t)
	feedRunes(h, "f(")
	require.True(t, h.sigActive)

	for range len("f(") {
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
	}
	require.Empty(t, h.editBuf.String())
	require.False(t, h.sigActive, "emptying the line must clear the hint")
	require.NotContains(t, renderFrame(t, h), "Println(a ...any)")
}

func TestEditorReceivesTypedRunes(t *testing.T) {
	var seen []term.Event
	h := newEditTestHandler(t, stubEditor{seen: &seen})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "hello")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeySpace})
	feedRunes(h, "world")

	// Every key reached the editor and mutated its buffer.
	assert.Equal(t, "hello world", h.editBuf.String())
	assert.Len(t, seen, 11)
}

func countInsertKeys(events []term.Event) int {
	n := 0
	for _, ev := range events {
		if ev.Type == term.EventKey && ev.Ch == 'i' && ev.Mod == 0 {
			n++
		}
	}
	return n
}

func TestModalStartInsertForwardsInsertKey(t *testing.T) {
	tests := []struct {
		name        string
		modal       bool
		startInsert bool
		wantInserts int
	}{
		{name: "modal start insert", modal: true, startInsert: true, wantInserts: 1},
		{name: "modal no start insert", modal: true, startInsert: false, wantInserts: 0},
		{name: "modeless start insert", modal: false, startInsert: true, wantInserts: 0},
		{name: "modeless no start insert", modal: false, startInsert: false, wantInserts: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen []term.Event
			h, _ := New(
				func(func()) bool { return false },
				term.NopInterrupter(),
				stubEditor{seen: &seen},
				Config{
					MaxHistory:       100,
					Modal:            tt.modal,
					ModalStartInsert: tt.startInsert,
				},
			)
			t.Cleanup(func() { _ = h.Close() })
			assert.Equal(t, tt.wantInserts, countInsertKeys(seen))
		})
	}
}

func TestEditorSubmitDispatchesAndClears(t *testing.T) {
	var dispatched []string
	h, registry := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{MaxHistory: 100},
	)
	t.Cleanup(func() { _ = h.Close() })
	registry.Register("e", "echo", echoCmd{out: &dispatched})

	h.Resize(testWidthH, testHeight)
	feedRunes(h, "e")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	h.inner.Wait()

	require.Equal(t, []string{"e"}, dispatched)
	// Submit clears the editor buffer so the next prompt is empty.
	assert.Equal(t, "", h.editBuf.String())
}

func TestModalEnterSubmitsAndClears(t *testing.T) {
	var dispatched []string
	h, registry := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{MaxHistory: 100, Modal: true},
	)
	t.Cleanup(func() { _ = h.Close() })
	registry.Register("e", "echo", echoCmd{out: &dispatched})

	h.Resize(testWidthH, testHeight)
	feedRunes(h, "e")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	h.inner.Wait()

	require.Equal(t, []string{"e"}, dispatched)
	assert.Equal(t, "", h.editBuf.String(),
		"modal insert-mode enter must submit and clear")
}

func TestShiftEnterInsertsNewlineNotSubmit(t *testing.T) {
	var dispatched []string
	var seen []term.Event
	h, registry := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{seen: &seen},
		Config{MaxHistory: 100},
	)
	t.Cleanup(func() { _ = h.Close() })
	registry.Register("e", "echo", echoCmd{out: &dispatched})

	h.Resize(testWidthH, testHeight)
	feedRunes(h, "e")
	exit, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter, Mod: term.ModShift,
	})
	h.inner.Wait()

	assert.False(t, exit)
	assert.True(t, handled, "shell owns shift-enter")
	assert.Empty(t, dispatched, "shift-enter must not submit the line")
	assert.Equal(t, "e", h.editBuf.String(),
		"shift-enter must not clear the input buffer")
	last := seen[len(seen)-1]
	assert.Equal(t, term.KeyEnter, last.Key,
		"shift-enter must forward a plain enter to the editor")
	assert.Equal(t, term.Modifier(0), last.Mod,
		"the shift modifier must be stripped before forwarding")
}

// realModelessEditor adapts a real modeless text editor to
// command.Editor, matching what production wires into the shell. The
// stub editors elsewhere in this file do not model newline insertion,
// so a real editor is required to exercise <shift-enter>.
type realModelessEditor struct{}

func (realModelessEditor) Edit(buf *cell.Buffer) command.EditHandler {
	return standard.NewHandler(buf, workspaceapi.RandomURI("memory"),
		text.IndentRuneTab, 4, standard.WithCommandBar(false))
}

func TestShiftEnterInsertsLiteralNewline(t *testing.T) {
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		realModelessEditor{},
		Config{MaxHistory: 100},
	)
	t.Cleanup(func() { _ = h.Close() })
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "a")
	h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter, Mod: term.ModShift,
	})
	feedRunes(h, "b")

	assert.Equal(t, "a\nb", h.editBuf.String(),
		"shift-enter must insert a literal newline, not a placeholder rune")
}

func TestNewPanicsWithoutEditor(t *testing.T) {
	assert.Panics(t, func() {
		_, _ = New(
			func(func()) bool { return false },
			term.NopInterrupter(),
			nil,
			Config{},
		)
	})
}

// arrowUp/arrowDown/ctrlJ/ctrlK are the history-cycling keys the shell
// wrapper must own (the editor would otherwise consume them as cursor
// motion).
var (
	arrowUp   = term.Event{Type: term.EventKey, Key: term.KeyArrowUp}
	arrowDown = term.Event{Type: term.EventKey, Key: term.KeyArrowDown}
	ctrlJ     = term.Event{Type: term.EventKey, Ch: 'j', Mod: term.ModCtrl}
	ctrlK     = term.Event{Type: term.EventKey, Ch: 'k', Mod: term.ModCtrl}
)

func TestArrowUpCyclesHistoryIntoEditor(t *testing.T) {
	h := newTestHandler(t, []string{"oldest", "middle", "newest"})
	h.Resize(testWidthH, testHeight)

	h.Handle(arrowUp)
	assert.Equal(t, "newest", h.editBuf.String())
	h.Handle(arrowUp)
	assert.Equal(t, "middle", h.editBuf.String())
	h.Handle(arrowUp)
	assert.Equal(t, "oldest", h.editBuf.String())
}

func TestArrowDownCyclesHistoryIntoEditor(t *testing.T) {
	h := newTestHandler(t, []string{"oldest", "middle", "newest"})
	h.Resize(testWidthH, testHeight)

	h.Handle(arrowUp)
	h.Handle(arrowUp)
	require.Equal(t, "middle", h.editBuf.String())

	h.Handle(arrowDown)
	assert.Equal(t, "newest", h.editBuf.String())
}

func TestCtrlKCtrlJCycleHistory(t *testing.T) {
	h := newTestHandler(t, []string{"oldest", "middle", "newest"})
	h.Resize(testWidthH, testHeight)

	h.Handle(ctrlK)
	assert.Equal(t, "newest", h.editBuf.String())
	h.Handle(ctrlK)
	assert.Equal(t, "middle", h.editBuf.String())
	h.Handle(ctrlJ)
	assert.Equal(t, "newest", h.editBuf.String())
}

func TestEditingDuringHistoryCycleKeepsPosition(t *testing.T) {
	h := newTestHandler(t, []string{"oldest", "middle", "newest"})
	h.Resize(testWidthH, testHeight)

	h.Handle(arrowUp)
	h.Handle(arrowUp)
	require.Equal(t, "middle", h.editBuf.String())

	// Edit the recalled line.
	feedRunes(h, "X")
	require.Equal(t, "middleX", h.editBuf.String())

	// <up> walks to the entry above the edited line.
	h.Handle(arrowUp)
	assert.Equal(t, "oldest", h.editBuf.String())
}

func TestHistoryDownRestoresLiveEdit(t *testing.T) {
	h := newTestHandler(t, []string{"newest"})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "draft")
	h.Handle(arrowUp)
	require.Equal(t, "newest", h.editBuf.String())

	h.Handle(arrowDown)
	assert.Equal(t, "draft", h.editBuf.String())
}

// multilineStubEditor is a command.Editor whose handler tracks a cursor
// row and moves it within the buffer on <up>/<down>, mirroring how a
// real editor navigates a multi-row buffer. It lets tests assert that
// the shell forwards cursor motion to the editor until a vertical edge
// is reached, only then cycling history.
type multilineStubEditor struct{ h *multilineStubHandler }

func (s *multilineStubEditor) Edit(buf *cell.Buffer) command.EditHandler {
	s.h = &multilineStubHandler{buf: buf}
	return s.h
}

type multilineStubHandler struct {
	buf   *cell.Buffer
	cy    int
	width int
}

func (s *multilineStubHandler) Resize(width, _ int) { s.width = width }
func (s *multilineStubHandler) Draw(term.Writer)    {}
func (s *multilineStubHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{Y: s.cy}, term.CursorStyleSteadyBar, true
}
func (s *multilineStubHandler) CursorAtScroll() term.Coordinates {
	return term.Coordinates{Y: s.cy}
}
func (s *multilineStubHandler) SetCursorAtScroll(pos term.Coordinates) bool {
	s.cy = pos.Y
	return true
}
func (s *multilineStubHandler) Selection() (string, bool) { return "", false }
func (s *multilineStubHandler) Handle(ev term.Event) (bool, bool) {
	if ev.Type != term.EventKey {
		return false, false
	}
	switch {
	case ev.Mod == 0 && ev.Key == term.KeyArrowUp:
		if s.cy > 0 {
			s.cy--
		}
		return false, true
	case ev.Mod == 0 && ev.Key == term.KeyArrowDown:
		if s.cy < s.buf.Rows()-1 {
			s.cy++
		}
		return false, true
	}
	return false, false
}

var _ command.EditHandler = (*multilineStubHandler)(nil)

// cursorStubEditor builds an edit handler whose buffer-relative cursor
// position is fixed by the test, so cursor-geometry assertions do not
// depend on a real editor's wrap/scroll behavior.
type cursorStubEditor struct{ cursor term.Coordinates }

func (s cursorStubEditor) Edit(buf *cell.Buffer) command.EditHandler {
	return &cursorStubHandler{buf: buf, cursor: s.cursor}
}

type cursorStubHandler struct {
	buf    *cell.Buffer
	cursor term.Coordinates
}

func (s *cursorStubHandler) Resize(int, int)  {}
func (s *cursorStubHandler) Draw(term.Writer) {}
func (s *cursorStubHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return s.cursor, term.CursorStyleSteadyBar, true
}
func (s *cursorStubHandler) CursorAtScroll() term.Coordinates        { return s.cursor }
func (s *cursorStubHandler) SetCursorAtScroll(term.Coordinates) bool { return true }
func (s *cursorStubHandler) Selection() (string, bool)               { return "", false }
func (s *cursorStubHandler) Handle(term.Event) (bool, bool)          { return false, false }

var _ command.EditHandler = (*cursorStubHandler)(nil)

func TestCursorVisualHonorsBufferLineForMultilineInput(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		cursor term.Coordinates // buffer line/column of the caret
		wantX  int
		wantY  int // visual row within the editor band
	}{
		{
			name:   "single line keeps prompt offset",
			text:   "hello",
			cursor: term.Coordinates{X: 5, Y: 0},
			wantX:  7, // len("> ") + 5
			wantY:  0,
		},
		{
			name:   "second line drops prompt offset",
			text:   "hello\nworld",
			cursor: term.Coordinates{X: 5, Y: 1},
			wantX:  5,
			wantY:  1,
		},
		{
			name:   "third line accumulates preceding rows",
			text:   "a\nbb\nccc",
			cursor: term.Coordinates{X: 3, Y: 2},
			wantX:  3,
			wantY:  2,
		},
		{
			name:   "long first line wraps before continuation",
			text:   "0123456789012345678901234567\nx", // 28 cols + prompt = 30
			cursor: term.Coordinates{X: 1, Y: 1},
			wantX:  1,
			wantY:  2, // first line occupies rows 0-1, continuation on row 2
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandlerFull(t, nil, 100, nil)
			h.editHandler = cursorStubEditor{cursor: tt.cursor}.Edit(h.editBuf)
			h.editBuf.WriteString(tt.text)
			h.Resize(testWidthH, testHeight)

			pos, _, ok := h.Cursor()
			require.True(t, ok)
			innerH := h.editInnerH()
			assert.Equal(t,
				term.Coordinates{X: tt.wantX, Y: innerH + tt.wantY}, pos)
		})
	}
}

func TestCursorVisualClampsLineBeyondBuffer(t *testing.T) {
	h := newTestHandlerFull(t, nil, 100, nil)
	// Cursor reports line 5 while the buffer only has one row ("hi").
	h.editHandler = cursorStubEditor{
		cursor: term.Coordinates{X: 0, Y: 5},
	}.Edit(h.editBuf)
	h.editBuf.WriteString("hi")
	h.Resize(testWidthH, testHeight)

	assert.NotPanics(t, func() {
		_, _, _ = h.Cursor()
	})
}

func TestArrowKeysMoveWithinMultilineItemBeforeCyclingHistory(t *testing.T) {
	editor := &multilineStubEditor{}
	h := newTestHandlerFull(t, []string{"old", "a\nb\nc"}, 100, nil)
	// Swap in the multiline-aware editor and rebind to the live buffer.
	h.editHandler = editor.Edit(h.editBuf)
	h.Resize(testWidthH, testHeight)

	// Recall the newest (multi-line) entry; cursor lands on its last row.
	h.Handle(arrowUp)
	require.Equal(t, "a\nb\nc", h.editBuf.String())
	require.Equal(t, 2, editor.h.cy)

	// <up> moves the cursor up within the recalled item, not into history.
	h.Handle(arrowUp)
	assert.Equal(t, 1, editor.h.cy)
	assert.Equal(t, "a\nb\nc", h.editBuf.String())

	h.Handle(arrowUp)
	assert.Equal(t, 0, editor.h.cy)
	assert.Equal(t, "a\nb\nc", h.editBuf.String())

	// At the top row, a further <up> cycles to the older entry.
	h.Handle(arrowUp)
	assert.Equal(t, "old", h.editBuf.String())
}

func TestArrowDownMovesWithinMultilineItemBeforeCyclingHistory(t *testing.T) {
	editor := &multilineStubEditor{}
	h := newTestHandlerFull(t, []string{"a\nb\nc", "newest"}, 100, nil)
	h.editHandler = editor.Edit(h.editBuf)
	h.Resize(testWidthH, testHeight)

	// Recall "newest", then the older multi-line entry; cursor on row 2.
	h.Handle(arrowUp)
	require.Equal(t, "newest", h.editBuf.String())
	h.Handle(arrowUp)
	require.Equal(t, "a\nb\nc", h.editBuf.String())
	require.Equal(t, 2, editor.h.cy)

	// Climb to the top row of the recalled entry.
	h.Handle(arrowUp)
	h.Handle(arrowUp)
	require.Equal(t, 0, editor.h.cy)
	require.Equal(t, "a\nb\nc", h.editBuf.String())

	// <down> walks back down the entry's rows, not into newer history.
	h.Handle(arrowDown)
	assert.Equal(t, 1, editor.h.cy)
	assert.Equal(t, "a\nb\nc", h.editBuf.String())
	h.Handle(arrowDown)
	assert.Equal(t, 2, editor.h.cy)
	assert.Equal(t, "a\nb\nc", h.editBuf.String())

	// At the bottom row, a further <down> cycles to the newer entry.
	h.Handle(arrowDown)
	assert.Equal(t, "newest", h.editBuf.String())
}

func TestCtrlROpensReverseHistorySearch(t *testing.T) {
	h := newEditTestHandler(t, stubEditor{})
	h.Resize(testWidthH, testHeight)

	// Ordinary runes are consumed by the editor.
	feedRunes(h, "ab")
	assert.Equal(t, "ab", h.editBuf.String())
	assert.False(t, h.searching)

	// <c-r> is intercepted by the host (never delegated to the editor)
	// and opens the reverse-history search overlay.
	h.Handle(term.Event{Type: term.EventKey, Ch: 'r', Mod: term.ModCtrl})
	require.True(t, h.searching)
	assert.Equal(t, modeHistory, h.mode)
	h.cancelSearch()
}

func TestCtrlRNeverReachesEditor(t *testing.T) {
	var seen []term.Event
	h := newEditTestHandler(t, stubEditor{seen: &seen})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "ab")
	h.Handle(term.Event{Type: term.EventKey, Ch: 'r', Mod: term.ModCtrl})

	for _, ev := range seen {
		require.False(t, ev.Mod == term.ModCtrl && ev.Ch == 'r',
			"editor must never receive <c-r>")
	}
	assert.True(t, h.searching)
	assert.Equal(t, modeHistory, h.mode)
	h.cancelSearch()
}

func TestTabTriggersCompletion(t *testing.T) {
	h, registry := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		Config{MaxHistory: 100},
	)
	t.Cleanup(func() { _ = h.Close() })
	registry.Register("foo", "foo", echoCmd{out: new([]string)})
	registry.Register("foobar", "foobar", echoCmd{out: new([]string)})

	h.Resize(testWidthH, testHeight)
	feedRunes(h, "foo")
	require.Equal(t, "foo", h.editBuf.String())

	// <tab> is intercepted by the host (never delegated to the editor)
	// and forwarded to the completion machinery, which opens the
	// overlay for the two matching commands.
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	require.True(t, h.searching)
	assert.Equal(t, modeCompletion, h.mode)
	h.cancelSearch()
}

func TestTabNeverReachesEditor(t *testing.T) {
	var seen []term.Event
	h, registry := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{seen: &seen},
		Config{MaxHistory: 100},
	)
	t.Cleanup(func() { _ = h.Close() })
	registry.Register("foo", "foo", echoCmd{out: new([]string)})
	registry.Register("foobar", "foobar", echoCmd{out: new([]string)})

	h.Resize(testWidthH, testHeight)
	feedRunes(h, "foo")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})

	for _, ev := range seen {
		require.False(t, ev.Key == term.KeyTab && ev.Mod == 0,
			"editor must never receive <tab>")
	}
	// The editor buffer keeps the typed prefix; no tab was inserted.
	assert.Equal(t, "foo", h.editBuf.String())
	assert.True(t, h.searching)
	h.cancelSearch()
}

func TestCtrlCCancelsRunningCommandBeforeEditor(t *testing.T) {
	var mu sync.Mutex
	var ticks []func()
	sched := func(fn func()) bool {
		mu.Lock()
		ticks = append(ticks, fn)
		mu.Unlock()
		return true
	}
	drain := func() {
		for {
			mu.Lock()
			if len(ticks) == 0 {
				mu.Unlock()
				return
			}
			fn := ticks[0]
			ticks = ticks[1:]
			mu.Unlock()
			fn()
		}
	}

	cmd := &blockingCmd{
		line:    "running-marker",
		release: make(chan struct{}),
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
	var seen []term.Event
	h, registry := New(
		sched,
		term.NopInterrupter(),
		stubEditor{seen: &seen},
		Config{MaxHistory: 100},
	)
	t.Cleanup(func() {
		select {
		case <-cmd.release:
		default:
			close(cmd.release)
		}
		_ = h.Close()
	})
	registry.Register("block", "blocks", cmd)

	feedRunes(h, "block")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.Eventually(t, func() bool {
		drain()
		select {
		case <-cmd.started:
			return h.shim.running()
		default:
			return false
		}
	}, time.Second, 5*time.Millisecond, "command should be in-flight")

	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: 'c', Mod: term.ModCtrl})
	require.True(t, handled, "<c-c> must be handled")

	require.Eventually(t, func() bool {
		drain()
		select {
		case <-cmd.closed:
			return true
		default:
			return false
		}
	}, time.Second, 5*time.Millisecond, "<c-c> should cancel the command")
	h.Wait()
	assert.False(t, h.shim.running(), "command should no longer be in-flight")
	assert.Equal(t, "", h.editBuf.String(), "<c-c> should not be typed into the editor")
	for _, ev := range seen {
		require.False(t, ev.Mod == term.ModCtrl && ev.Ch == 'c',
			"editor must never receive <c-c>")
	}
}

func TestAcceptHistoryMatchReplacesEditorBuffer(t *testing.T) {
	h := newTestHandler(t, []string{"deploy --prod"})
	h.Resize(testWidthH, testHeight)

	// Open reverse-history search; the single entry is focused.
	h.Handle(term.Event{Type: term.EventKey, Ch: 'r', Mod: term.ModCtrl})
	require.True(t, h.searching)
	require.Equal(t, modeHistory, h.mode)

	// Accept the focused match.
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	assert.False(t, h.searching)
	assert.Equal(t, "deploy --prod", h.editBuf.String())
}

func TestAcceptCompletionCandidateReplacesEditorBuffer(t *testing.T) {
	h := newTestHandlerFull(t, nil, 100, func(r *CommandRegistry) {
		r.Register("foobar", "foobar", stubCmd{})
		r.Register("foobaz", "foobaz", stubCmd{})
	})
	h.Resize(testWidthH, testHeight)

	feedRunes(h, "foob")
	require.Equal(t, "foob", h.editBuf.String())

	// <tab> opens the completion overlay with the two matches; the
	// first is focused.
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	// Candidates stream into the overlay off the event loop; wait for
	// them to settle before asserting and accepting.
	h.WaitCompletion()
	drainTicks(h)
	require.True(t, h.searching)
	require.Equal(t, modeCompletion, h.mode)

	// Accept the focused candidate.
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	assert.False(t, h.searching)
	// The chosen candidate fully replaces the editor buffer.
	assert.Contains(t, []string{"foobar", "foobaz"}, h.editBuf.String())
}

// echoCmd is a CommandHandler that records its dispatched name into
// a caller-supplied slice. It is used to verify that edit-mode
// submit forwards the original <enter> through to the inner repl.
type echoCmd struct{ out *[]string }

func (e echoCmd) HandleCommand(
	_ context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	*e.out = append(*e.out, cmd.Name)
	return iterator.Empty[component.Responsive](), nil
}

func (e echoCmd) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (e echoCmd) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return iterator.Empty[component.Responsive](), nil
}

// promptStandardEditor mirrors the production prompt editor wired by
// ide.standardPromptEditor: bare, unwrapped, horizontally scrolled.
type promptStandardEditor struct{}

func (promptStandardEditor) Edit(buf *cell.Buffer) command.EditHandler {
	return standard.NewHandler(buf, workspaceapi.RandomURI("memory"),
		text.IndentRuneTab, 4,
		standard.WithCommandBar(false),
		standard.WithWrap(false),
	)
}

// newPromptEditorHandler builds a shell handler backed by
// promptStandardEditor and seeded with history items.
func newPromptEditorHandler(t *testing.T, items []string) *Handler {
	t.Helper()
	svc := storagestub.NewInMemoryService()
	require.NoError(t, svc.Create(
		context.Background(), testDocID,
		&historyDoc{Items: items, Version: 1},
	))
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		promptStandardEditor{},
		Config{
			Storage:           svc,
			HistoryDocumentID: testDocID,
			MaxHistory:        100,
		},
	)
	t.Cleanup(func() { _ = h.Close() })
	h.Resize(testWidthH, testHeight)
	return h
}

// longHistoryItem is 100 cells wide: with the 2-cell prompt it wraps
// over four rows at testWidthH (30/30/30/12).
var longHistoryItem = strings.Repeat("0123456789", 10)

func TestInputBandMouseMapsToBufferCoordinates(t *testing.T) {
	tests := []struct {
		name    string
		click   term.Coordinates
		want    term.Coordinates
		wantCur term.Coordinates
	}{
		{
			name:    "end of text on last wrapped row",
			click:   term.Coordinates{X: 12, Y: 11},
			want:    term.Coordinates{X: 100},
			wantCur: term.Coordinates{X: 12, Y: 11},
		},
		{
			name:    "middle of an interior wrapped row",
			click:   term.Coordinates{X: 5, Y: 10},
			want:    term.Coordinates{X: 63},
			wantCur: term.Coordinates{X: 5, Y: 10},
		},
		{
			name:    "prompt prefix clamps to line start",
			click:   term.Coordinates{X: 1, Y: 8},
			want:    term.Coordinates{},
			wantCur: term.Coordinates{X: 2, Y: 8},
		},
		{
			name:    "past end of text clamps to line end",
			click:   term.Coordinates{X: 20, Y: 11},
			want:    term.Coordinates{X: 100},
			wantCur: term.Coordinates{X: 12, Y: 11},
		},
		{
			name:    "below all content rows clamps to last line end",
			click:   term.Coordinates{X: 4, Y: 50},
			want:    term.Coordinates{X: 100},
			wantCur: term.Coordinates{X: 12, Y: 11},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newPromptEditorHandler(t, []string{longHistoryItem})
			h.Handle(arrowUp)
			require.Equal(t, longHistoryItem, h.editBuf.String())

			bandH := h.editEditorH()
			require.Equal(t, 4, bandH, "the recalled line must wrap over 4 rows")
			before := renderFrame(t, h)

			h.Handle(mouseEv(tt.click.X, tt.click.Y, term.MouseLeft))
			h.Handle(mouseEv(tt.click.X, tt.click.Y, term.MouseRelease))

			assert.Equal(t, tt.want, h.editHandler.CursorAtScroll(),
				"click must land on the buffer position under the pointer")
			cur, _, ok := h.Cursor()
			require.True(t, ok)
			assert.Equal(t, tt.wantCur, cur)
			assert.Equal(t, bandH, h.editEditorH(),
				"the input band must not grow because of a click")
			assert.Equal(t, before, renderFrame(t, h),
				"a click must not shift the rendered content")
		})
	}
}

func TestInputBandMouseOnSecondLogicalLine(t *testing.T) {
	h := newPromptEditorHandler(t, []string{"unused"})

	feedRunes(h, "abc")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter, Mod: term.ModShift})
	feedRunes(h, "defghij")
	require.Equal(t, "abc\ndefghij", h.editBuf.String())
	require.Equal(t, 2, h.editEditorH())

	h.Handle(mouseEv(2, 11, term.MouseLeft))
	h.Handle(mouseEv(2, 11, term.MouseRelease))

	assert.Equal(t, term.Coordinates{X: 2, Y: 1}, h.editHandler.CursorAtScroll())
}

func TestInputBandMouseDragSelectsAcrossWrappedRows(t *testing.T) {
	h := newPromptEditorHandler(t, []string{longHistoryItem})
	h.Handle(arrowUp)
	require.Equal(t, longHistoryItem, h.editBuf.String())

	// (2,8) is buffer column 0; (5,9) is buffer column 33.
	for _, ev := range clickDrag(2, 8, 5, 9) {
		h.Handle(ev)
	}

	sel, ok := h.Selection()
	require.True(t, ok)
	assert.Equal(t, longHistoryItem[:33], sel)

	w := term.NewStringWriter(testWidthH, testHeight)
	h.Draw(w)
	require.NoError(t, w.Flush())
	cells := w.Cells()
	reversed := func(x, y int) bool {
		return cells[y*testWidthH+x].Attrs&term.AttrReverse != 0
	}
	// buffer cols 0..32 map to band cells (2,8)..(29,8) and
	// (0,9)..(4,9); col 33 is the caret.
	for x := 2; x < testWidthH; x++ {
		assert.True(t, reversed(x, 8), "cell (%d,8) must be highlighted", x)
	}
	for x := range 5 {
		assert.True(t, reversed(x, 9), "cell (%d,9) must be highlighted", x)
	}
	assert.False(t, reversed(1, 8), "the prompt prefix must not be highlighted")
	assert.False(t, reversed(5, 9),
		"the caret cell past the exclusive selection must not be highlighted")
	assert.False(t, reversed(6, 9), "cells past the selection must not be highlighted")
}

func TestInputBandLeftwardSelectionExcludesAnchorCell(t *testing.T) {
	h := newPromptEditorHandler(t, []string{longHistoryItem})
	h.Handle(arrowUp)
	require.Equal(t, longHistoryItem, h.editBuf.String())

	// (11,11) is buffer column 99; (10,11) is column 98.
	for _, ev := range clickDrag(11, 11, 10, 11) {
		h.Handle(ev)
	}

	sel, ok := h.Selection()
	require.True(t, ok)
	assert.Equal(t, "8", sel)

	w := term.NewStringWriter(testWidthH, testHeight)
	h.Draw(w)
	require.NoError(t, w.Flush())
	cells := w.Cells()
	reversed := func(x, y int) bool {
		return cells[y*testWidthH+x].Attrs&term.AttrReverse != 0
	}
	assert.True(t, reversed(10, 11), "the selected cell must be highlighted")
	assert.False(t, reversed(11, 11),
		"the anchor cell is not part of the exclusive selection")
	assert.False(t, reversed(9, 11), "cells before the selection must not be highlighted")
}

func TestInputBandDoubleClickSelectsWholeWord(t *testing.T) {
	for _, tt := range []struct {
		name  string
		click term.Coordinates
	}{
		{"last wrapped row", term.Coordinates{X: 11, Y: 11}},
		{"interior wrapped row", term.Coordinates{X: 5, Y: 10}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := newPromptEditorHandler(t, []string{longHistoryItem})
			h.Handle(arrowUp)
			require.Equal(t, longHistoryItem, h.editBuf.String())

			for _, ev := range doubleClick(tt.click.X, tt.click.Y) {
				h.Handle(ev)
			}

			sel, ok := h.Selection()
			require.True(t, ok)
			assert.Equal(t, longHistoryItem, sel)
		})
	}
}
