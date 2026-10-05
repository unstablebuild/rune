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
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text/standard"
	"unstable.build/rune/internal/text/vi"
)

func TestComposeEditorNilUsesModelessEditor(t *testing.T) {
	comp := NewComponent(ComponentConfig{})

	h, tx, rx := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	typeText(h, "hello")
	assert.Equal(t, "hello", comp.Input().Text())

	_, ok := comp.Input().(*textHandlerInput)
	require.True(t, ok, "nil Editor must build an editor-backed compose input")

	go func() {
		msg := <-rx
		assert.Equal(t, "hello", msg.Text)
	}()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	assert.Equal(t, "", comp.Input().Text(), "submit must clear the input")
}

func TestComposeEditorModelessAcceptsInput(t *testing.T) {
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed})

	h, tx, rx := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	typeText(h, "from editor")
	assert.Equal(t, "from editor", comp.Input().Text())

	go func() {
		msg := <-rx
		assert.Equal(t, "from editor", msg.Text)
	}()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	assert.Equal(t, "", comp.Input().Text(), "submit must clear the compose editor")
}

func TestComposeEditorSetTextClear(t *testing.T) {
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed})
	comp.Resize(30, 10)

	comp.Input().SetText("recalled message")
	assert.Equal(t, "recalled message", comp.Input().Text())

	comp.Input().SetText("replaced")
	assert.Equal(t, "replaced", comp.Input().Text(),
		"SetText must replace, not append")

	comp.Input().Clear()
	assert.Equal(t, "", comp.Input().Text())
}

func TestComposeEditorHeightGrowsWithWrap(t *testing.T) {
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed})
	comp.Resize(30, 10)
	in := comp.Input()

	const width = 10
	assert.Equal(t, 1, in.Height(width), "empty input occupies one line")

	in.SetText("0123456789ABCDEFGHIJ") // 20 cols wraps to 2 lines at width 10
	assert.Equal(t, 2, in.Height(width),
		"a line longer than width must wrap and grow the box")
}

func TestComposeEditorResizeResetsScrollAfterWrap(t *testing.T) {
	ed := standard.Editor(
		standard.WithClipboard(clipboard.NewInMemory()),
		standard.WithWrap(true),
	)
	comp := NewComponent(ComponentConfig{Editor: ed})
	comp.Resize(30, 10)

	in, ok := comp.Input().(*textHandlerInput)
	require.True(t, ok)

	const width = 10

	// One visible row: type a line long enough to wrap to two visual
	// lines. Typing moves the cursor onto the second visual line, so the
	// editor scrolls down to keep it visible while still one row tall.
	in.Resize(width, 1)
	for _, ch := range "0123456789ABCDEFGHIJ" {
		in.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	in.Resize(width, 1)
	require.Equal(t, 1, in.SeekOffset(),
		"editor scrolls down to keep the cursor visible at one row")

	cursorBefore := in.Handler.CursorAtScroll()

	// The box grows to fit both wrapped lines; the offset must reset so
	// the first line is visible again.
	in.Resize(width, in.Height(width))
	assert.Equal(t, 0, in.SeekOffset(),
		"growing the box must re-clamp the scroll offset to the top")
	assert.Equal(t, cursorBefore, in.Handler.CursorAtScroll(),
		"re-clamping the offset must not move the cursor in the buffer")
}

func TestComposeEditorResizeKeepsScrollWhenContentOverflows(t *testing.T) {
	ed := standard.Editor(
		standard.WithClipboard(clipboard.NewInMemory()),
		standard.WithWrap(true),
	)
	comp := NewComponent(ComponentConfig{Editor: ed})
	comp.Resize(30, 10)

	in, ok := comp.Input().(*textHandlerInput)
	require.True(t, ok)

	const width = 10

	// Five wrapped visual lines, cursor at the end, in a 2-row viewport.
	in.Resize(width, 2)
	for _, ch := range "0123456789ABCDEFGHIJabcdefghijKLMNOPQRST" {
		in.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	cursorBefore := in.Handler.CursorAtScroll()
	in.Resize(width, 2)

	// Content (4 visual lines) overflows the 2-row viewport, so the box
	// must remain scrolled down to keep the cursor visible rather than
	// snapping to the top.
	assert.Greater(t, in.SeekOffset(), 0,
		"overflowing content must stay scrolled to keep the cursor visible")
	assert.Equal(t, in.MaxSeekOffset(), in.SeekOffset(),
		"offset must be clamped to the maximum for the current height")
	assert.Equal(t, cursorBefore, in.Handler.CursorAtScroll(),
		"resize must not move the cursor in the buffer")
}

func TestComposeEditorHeightCappedLeavesMessagesVisible(t *testing.T) {
	const width, height = 80, 30
	ed := standard.Editor(
		standard.WithClipboard(clipboard.NewInMemory()),
		standard.WithWrap(true),
	)
	comp := NewComponent(ComponentConfig{Editor: ed})

	comp.AddToolCall("tc1", "bash", `{"command":"ls -l"}`, "List directory")
	comp.CompleteToolCall("tc1", "bash", `{"command":"ls -l"}`, "List directory", "total 0\n", false)

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(width, height)

	var lines []string
	for range 60 {
		lines = append(lines, "package workspaceapi import errors")
	}
	comp.Input().SetText(strings.Join(lines, "\n"))
	h.Resize(width, height)

	boxW := comp.boxWidth(width)
	require.Greater(t, comp.box.Height(boxW), height,
		"precondition: the raw editor height must exceed the viewport")

	capped := comp.boxHeight(boxW)
	assert.LessOrEqual(t, capped, height-minMessagesRows,
		"capped box must leave at least minMessagesRows for the conversation")

	messagesHeight := height - capped
	assert.GreaterOrEqual(t, messagesHeight, minMessagesRows,
		"the messages row must stay visible above the input box")
}

func TestComposeEditorModelessShiftEnterInsertsNewline(t *testing.T) {
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed})

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	typeText(h, "line1")
	_, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter, Mod: term.ModShift,
	})
	assert.True(t, handled)
	typeText(h, "line2")

	assert.Equal(t, "line1\nline2", comp.Input().Text(),
		"shift-enter must insert a newline, not submit")
}

func TestComposeEditorInputBackgroundColorAppliesToFrameAndEditor(t *testing.T) {
	const width, height = 40, 12
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{
		Editor:               ed,
		InputBackgroundColor: term.ColorGray,
	})

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(width, height)

	assert.Equal(t, term.ColorGray, comp.box.Bg,
		"frame background must use the configured input color")

	typeText(h, "hello")

	sw := term.NewStringWriter(width, height)
	h.Draw(sw)

	// The editor content sits inside the frame border; the typed text
	// cells must carry the configured background color.
	inputPos := comp.InputPosition()
	cells := sw.Cells()
	var found bool
	for x := inputPos.X + 1; x < width; x++ {
		c := cells[(inputPos.Y+1)*width+x]
		if c.Ch == 'h' {
			found = true
			assert.Equal(t, term.ColorGray, c.Bg,
				"editor content background must use the configured input color")
			break
		}
	}
	assert.True(t, found, "typed text must be rendered in the input area")
}

func TestComposeEditorInputBackgroundDefaultLeftUnset(t *testing.T) {
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed})

	assert.Equal(t, term.ColorDefault, comp.box.Bg,
		"frame background stays at terminal default when unset")
}

func TestComposeEditorFrameCharSetAppliesToFrame(t *testing.T) {
	const width, height = 40, 12
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	charset := component.FrameCharSetHighlight()
	comp := NewComponent(ComponentConfig{
		Editor: ed,
		InputBox: InputBoxConfig{
			FrameCharSet: charset,
		},
	})

	assert.Equal(t, charset, comp.box.FrameCharSet,
		"frame must use the configured frame charset")

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(width, height)

	sw := term.NewStringWriter(width, height)
	h.Draw(sw)

	inputPos := comp.InputPosition()
	cells := sw.Cells()
	topLeft := cells[inputPos.Y*width+inputPos.X]
	assert.Equal(t, charset.TopLeft, topLeft.Ch,
		"rendered top-left corner must use the configured frame charset rune")
}

func TestComposeEditorModalEnterSubmitsInNormalMode(t *testing.T) {
	ed := vi.Editor(vi.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed, EditorModal: true})

	h, tx, rx := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	// Enter insert mode, type, then return to normal mode.
	h.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
	typeText(h, "modal msg")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.Equal(t, "modal msg", comp.Input().Text())

	go func() {
		msg := <-rx
		assert.Equal(t, "modal msg", msg.Text)
	}()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	assert.Equal(t, "", comp.Input().Text(),
		"normal-mode enter must submit and clear")
}

func TestComposeEditorModalEnterSubmitsInInsertMode(t *testing.T) {
	ed := vi.Editor(vi.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed, EditorModal: true})

	h, tx, rx := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	h.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
	typeText(h, "modal msg")
	assert.Equal(t, "modal msg", comp.Input().Text())

	go func() {
		msg := <-rx
		assert.Equal(t, "modal msg", msg.Text)
	}()
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	assert.True(t, handled)
	assert.Equal(t, "", comp.Input().Text(),
		"insert-mode enter must submit and clear, not insert a newline")
}

func TestComposeEditorModalShiftEnterInsertsNewline(t *testing.T) {
	ed := vi.Editor(vi.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed, EditorModal: true})

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	h.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
	typeText(h, "line1")
	_, handled := h.Handle(term.Event{
		Type: term.EventKey, Key: term.KeyEnter, Mod: term.ModShift,
	})
	assert.True(t, handled)
	typeText(h, "line2")

	assert.Equal(t, "line1\nline2", comp.Input().Text(),
		"insert-mode shift-enter must insert a newline, not submit")
}

func TestComposeEditorModalStartInsert(t *testing.T) {
	ed := vi.Editor(vi.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{
		Editor:           ed,
		EditorModal:      true,
		ModalStartInsert: true,
	})

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	// No 'i' press: the composer must already be in insert mode.
	typeText(h, "hello")
	assert.Equal(t, "hello", comp.Input().Text(),
		"modal composer with ModalStartInsert must accept text without pressing i")
}

func TestComposeEditorModalStartNormalByDefault(t *testing.T) {
	ed := vi.Editor(vi.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{
		Editor:           ed,
		EditorModal:      true,
		ModalStartInsert: false,
	})

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	// Without ModalStartInsert the composer starts in normal mode, so a
	// bare letter key is consumed as a motion/command and inserts nothing.
	typeText(h, "x")
	assert.Equal(t, "", comp.Input().Text(),
		"modal composer without ModalStartInsert must start in normal mode")

	// Pressing 'i' enters insert mode; text typed afterwards is inserted.
	h.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
	typeText(h, "world")
	assert.Equal(t, "world", comp.Input().Text(),
		"text inserts after entering insert mode")
}

func TestComposeEditorSelectionSurvivesUnfocused(t *testing.T) {
	const width, height = 40, 12
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed})

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(width, height)

	typeText(h, "hello")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyHome})
	for range 3 {
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight, Mod: term.ModShift})
	}

	dh := h.(*dialogueHandler)
	dh.inputFocused = false

	sw := term.NewStringWriter(width, height)
	h.Draw(sw)
	require.NoError(t, sw.Flush())

	inputPos := comp.InputPosition()
	cells := sw.Cells()
	var reverseCells int
	for y := inputPos.Y; y < height; y++ {
		for x := inputPos.X; x < width; x++ {
			if cells[y*width+x].Attrs&term.AttrReverse != 0 {
				reverseCells++
			}
		}
	}
	assert.GreaterOrEqual(t, reverseCells, 3,
		"editor selection must keep its AttrReverse cells when the input is not focused")
}

func TestComposeEditorArrowUpNavigatesBeforeHistory(t *testing.T) {
	ed := standard.Editor(standard.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed})

	h, tx, rx := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	// Submit a message so history has an entry to recall.
	go func() { <-rx }()
	typeText(h, "old message")
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, handled)
	require.Equal(t, "", comp.Input().Text())

	// Compose a two-line draft; the cursor sits on the second line.
	typeText(h, "line1")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter, Mod: term.ModShift})
	typeText(h, "line2")
	require.Equal(t, "line1\nline2", comp.Input().Text())

	// First ArrowUp moves the cursor up within the editor; the draft is
	// untouched and history is not recalled.
	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)
	assert.Equal(t, "line1\nline2", comp.Input().Text(),
		"ArrowUp inside a multi-line draft must move the cursor, not recall history")

	// Second ArrowUp is at the top edge: the editor leaves it unhandled,
	// so history recall replaces the draft with the previous message.
	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)
	assert.Equal(t, "old message", comp.Input().Text(),
		"ArrowUp at the top edge must recall the previous history entry")
}

func TestComposeEditorModalArrowUpRecallsQueuedMessage(t *testing.T) {
	ed := vi.Editor(vi.WithClipboard(clipboard.NewInMemory()))
	comp := NewComponent(ComponentConfig{Editor: ed, EditorModal: true})

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	dh := h.(*dialogueHandler)
	dh.busy = true

	h.Handle(term.Event{Type: term.EventKey, Ch: 'i'})
	typeText(h, "queued msg")
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	_, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	require.True(t, handled)
	require.Equal(t, "", comp.Input().Text())
	require.Equal(t, 1, comp.QueueLen())

	_, handled = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowUp})
	assert.True(t, handled)
	assert.Equal(t, "queued msg", comp.Input().Text(),
		"ArrowUp on an empty modal buffer must recall the queued message")
	assert.Equal(t, 0, comp.QueueLen())
}
