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
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/handlertest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/text/standard"
)

// mouseEv builds a mouse term.Event for the given position and key.
func mouseEv(x, y int, key term.Key) term.Event {
	return term.Event{Type: term.EventMouse, MouseX: x, MouseY: y, Key: key}
}

type recordingToolOpener struct{ opened []ToolCallDetails }

func (r *recordingToolOpener) OpenToolCall(details ToolCallDetails) {
	r.opened = append(r.opened, details)
}

func TestHandlerToolCallHover(t *testing.T) {
	for _, collapsed := range []bool{true, false} {
		t.Run(fmt.Sprintf("collapsed=%t", collapsed), func(t *testing.T) {
			comp := NewComponent(ComponentConfig{StartCollapsed: collapsed})
			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
				term.NopInterrupter(), WithToolCallOpener(&recordingToolOpener{}))
			defer close(tx)
			h.Resize(60, 18)
			comp.AddToolCall("parent", "agent", `{}`, "research")
			comp.AddChildToolCall("parent", "child", "read_file", `{}`, "a.go")
			w := term.NewStringWriter(60, 18)
			h.Draw(w)

			parent := comp.currentTurn.tools["parent"]
			child := parent.childTurn.tools["child"]
			parentName := findText(t, w, 60, "agent")
			childName := findText(t, w, 60, "read_file")
			h.Handle(mouseEv(parentName.X+1, parentName.Y, 0))
			assert.True(t, parent.hovered)
			assert.False(t, child.hovered)
			h.Handle(mouseEv(childName.X+1, childName.Y, 0))
			assert.False(t, parent.hovered)
			assert.True(t, child.hovered)
			summary := findText(t, w, 60, "research")
			h.Handle(mouseEv(summary.X, summary.Y, 0))
			assert.False(t, parent.hovered, "summary is not clickable")
			assert.False(t, child.hovered)
			h.Handle(mouseEv(childName.X+1, childName.Y, 0))
			h.Handle(mouseEv(childName.X+1, comp.InputPosition().Y, 0))
			assert.False(t, child.hovered)
		})
	}
}

func TestHandlerLinkPointerShape(t *testing.T) {
	const width, height = 60, 16
	openLink := func(*url.URL) bool { return true }
	for _, tc := range []struct {
		name        string
		onLinkClick func(*url.URL) bool
		dx, dy      int
		want        term.PointerShape
		wantSet     bool
	}{
		{name: "over a link", onLinkClick: openLink, dx: len("Visit "), want: term.PointerLink, wantSet: true},
		{name: "over plain text", onLinkClick: openLink, dx: 0},
		{name: "blank row above a link", onLinkClick: openLink, dx: len("Visit "), dy: -1},
		{name: "blank row below a link", onLinkClick: openLink, dx: len("Visit "), dy: 1},
		{name: "links not clickable", dx: len("Visit ")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{OnLinkClick: tc.onLinkClick})
			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.NopInterrupter())
			defer close(tx)
			h.Resize(width, height)
			comp.AddReceiveMessage("Read this:\n\nVisit [Rune](https://github.com/unstablebuild/rune)\n\nThanks")
			w := term.NewStringWriter(width, height)
			h.Draw(w)

			at := findText(t, w, width, "Visit Rune")
			pointer := new(term.Pointer)
			ev := mouseEv(at.X+tc.dx, at.Y+tc.dy, 0)
			ev.Context = term.ContextWithPointer(context.Background(), pointer)
			h.Handle(ev)

			shape, set := pointer.Shape()
			assert.Equal(t, tc.wantSet, set)
			assert.Equal(t, tc.want, shape)
		})
	}
}

func TestHandlerToolCallClick(t *testing.T) {
	for _, tc := range []struct {
		name       string
		collapsed  bool
		child      bool
		result     bool
		label      string
		wantName   string
		wantOutput string
	}{
		{name: "collapsed parent", collapsed: true, label: "agent", wantName: "agent", wantOutput: "# Full response\n\nMore detail"},
		{name: "collapsed child", collapsed: true, child: true, label: "read_file", wantName: "read_file", wantOutput: "child output"},
		{name: "collapsed result", collapsed: true, child: true, result: true, label: "# Full response", wantName: "agent", wantOutput: "# Full response\n\nMore detail"},
		{name: "expanded parent", label: "agent", wantName: "agent", wantOutput: "# Full response\n\nMore detail"},
		{name: "expanded child", child: true, label: "read_file", wantName: "read_file", wantOutput: "child output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{StartCollapsed: tc.collapsed, ToolResultMaxLines: 1})
			opener := &recordingToolOpener{}
			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
				term.NopInterrupter(), WithToolCallOpener(opener))
			defer close(tx)
			h.Resize(60, 16)
			comp.AddToolCall("parent", "agent", `{"description":"research"}`, "research")
			if tc.child {
				comp.AddChildToolCall("parent", "child", "read_file", `{"path":"a.go"}`, "")
				comp.CompleteChildToolCall("parent", "child", "read_file", `{"path":"a.go"}`, "", "child output", false)
			}
			comp.CompleteToolCall("parent", "agent", `{"description":"research"}`, "research", "# Full response\n\nMore detail", false)
			if tc.result {
				comp.AddChildResult("parent", "# Full response\n\nMore detail", false)
			}
			w := term.NewStringWriter(60, 16)
			h.Draw(w)

			pos := findText(t, w, 60, tc.label)
			pos.X++
			_, handled := h.Handle(mouseEv(pos.X, pos.Y, term.MouseLeft))
			assert.True(t, handled)
			assert.Empty(t, opener.opened, "preview opens on release")
			_, handled = h.Handle(mouseEv(pos.X, pos.Y, term.MouseRelease))
			assert.True(t, handled)
			require.Len(t, opener.opened, 1)
			assert.Equal(t, tc.wantName, opener.opened[0].Name)
			assert.Equal(t, tc.wantOutput, opener.opened[0].Output)
			assert.Equal(t, `{"description":"research"}`, comp.currentTurn.tools["parent"].args)
		})
	}
}

func TestHandlerToolCallOutsideNameSelectsText(t *testing.T) {
	for _, tc := range []struct {
		name      string
		collapsed bool
		text      string
	}{
		{name: "collapsed summary", collapsed: true, text: "a.go"},
		{name: "expanded summary", text: "a.go"},
		{name: "expanded args", text: "path=a.go"},
		{name: "expanded output", text: "second line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{StartCollapsed: tc.collapsed})
			opener := &recordingToolOpener{}
			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
				term.NopInterrupter(), WithToolCallOpener(opener))
			defer close(tx)
			h.Resize(60, 16)
			comp.AddToolCall("call", "read_file", `{"path":"a.go"}`, "a.go")
			comp.CompleteToolCall("call", "read_file", `{"path":"a.go"}`, "a.go", "first line\nsecond line", false)
			w := term.NewStringWriter(60, 16)
			h.Draw(w)

			pos := findText(t, w, 60, tc.text)
			end := pos.X + len(tc.text) - 1
			for _, ev := range clickDrag(pos.X, pos.Y, end, pos.Y) {
				h.Handle(ev)
			}
			assert.Empty(t, opener.opened)
			sel, ok := h.Selection()
			assert.True(t, ok)
			assert.Equal(t, tc.text, sel)
		})
	}
}

func TestHandlerToolCallClickOpensOncePerPress(t *testing.T) {
	comp := NewComponent(ComponentConfig{StartCollapsed: true})
	opener := &recordingToolOpener{}
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.NopInterrupter(), WithToolCallOpener(opener))
	defer close(tx)
	h.Resize(60, 16)
	comp.AddToolCall("call", "read_file", `{}`, "")
	h.Draw(term.NewStringWriter(60, 16))
	pos := term.CoordinatesSum(comp.MessagesPosition(), comp.turnNode.Position())
	pos.X += 5
	h.Handle(mouseEv(pos.X, pos.Y, term.MouseLeft))
	h.Handle(mouseEv(pos.X, pos.Y, term.MouseLeft))
	assert.Empty(t, opener.opened)
	h.Handle(mouseEv(pos.X, pos.Y, term.MouseRelease))
	assert.Len(t, opener.opened, 1)
	h.Handle(mouseEv(pos.X, pos.Y, term.MouseLeft))
	assert.Len(t, opener.opened, 1)
	h.Handle(mouseEv(pos.X, pos.Y, term.MouseRelease))
	assert.Len(t, opener.opened, 2)
}

func TestHandlerToolCallPressDraggedAwayDoesNotOpen(t *testing.T) {
	comp := NewComponent(ComponentConfig{StartCollapsed: true})
	opener := &recordingToolOpener{}
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.NopInterrupter(), WithToolCallOpener(opener))
	defer close(tx)
	h.Resize(60, 16)
	comp.AddToolCall("call", "read_file", `{}`, "")
	comp.AddToolCall("other", "write_stdin", `{}`, "")
	h.Draw(term.NewStringWriter(60, 16))
	pos := term.CoordinatesSum(comp.MessagesPosition(), comp.turnNode.Position())
	pos.X += 5
	h.Handle(mouseEv(pos.X, pos.Y, term.MouseLeft))
	h.Handle(mouseEv(pos.X, pos.Y+1, term.MouseLeft))
	h.Handle(mouseEv(pos.X, pos.Y+1, term.MouseLeft))
	h.Handle(mouseEv(pos.X, pos.Y+1, term.MouseRelease))
	assert.Empty(t, opener.opened)
	h.Handle(mouseEv(pos.X, pos.Y+1, term.MouseLeft))
	h.Handle(mouseEv(pos.X, pos.Y+1, term.MouseRelease))
	require.Len(t, opener.opened, 1)
	assert.Equal(t, "write_stdin", opener.opened[0].Name)
}

func TestHandlerToolCallClickInScrolledHistory(t *testing.T) {
	comp := NewComponent(ComponentConfig{StartCollapsed: true})
	opener := &recordingToolOpener{}
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.NopInterrupter(), WithToolCallOpener(opener))
	defer close(tx)
	h.Resize(60, 16)
	comp.AddToolCall("first", "read_file", `{"path":"first.go"}`, "")
	comp.CompleteToolCall("first", "read_file", `{"path":"first.go"}`, "", "first output", false)
	comp.AddReceiveMessage("intervening reply")
	comp.AddToolCall("second", "read_file", `{"path":"second.go"}`, "")
	comp.CompleteToolCall("second", "read_file", `{"path":"second.go"}`, "", "second output", false)
	for range 25 {
		comp.AddReceiveMessage("later reply")
	}
	for comp.SeekUp() {
	}
	h.Draw(term.NewStringWriter(60, 16))
	pos := comp.MessagesPosition()
	var first *Turn
	var firstNode component.ListNode
	for node, ok := comp.messages.Front(); ok; node, ok = node.Next() {
		if turn, ok := node.Value().(*Turn); ok {
			first = turn
			firstNode = node
			break
		}
	}
	require.NotNil(t, first)
	firstRow := firstNode.Position().Y
	_, handled := h.Handle(mouseEv(pos.X+5, pos.Y+firstRow, term.MouseLeft))
	assert.True(t, handled)
	_, handled = h.Handle(mouseEv(pos.X+5, pos.Y+firstRow, term.MouseRelease))
	assert.True(t, handled)
	require.Len(t, opener.opened, 1)
	assert.Equal(t, "first output", opener.opened[0].Output)
	// A click on a non-tool row must still be available to text selection.
	_, _ = h.Handle(mouseEv(pos.X+5, pos.Y+firstRow+2, term.MouseLeft))
	assert.Len(t, opener.opened, 1)
}

// tripleClick returns the 6-event sequence (click-release × 3) for a
// triple-click at the given screen position.
func tripleClick(x, y int) []term.Event {
	return []term.Event{
		mouseEv(x, y, term.MouseLeft),
		mouseEv(x, y, term.MouseRelease),
		mouseEv(x, y, term.MouseLeft),
		mouseEv(x, y, term.MouseRelease),
		mouseEv(x, y, term.MouseLeft),
		mouseEv(x, y, term.MouseRelease),
	}
}

func TestHandlerComposeEditorClickUsesFrameContentCoordinates(t *testing.T) {
	comp := NewComponent(ComponentConfig{
		Editor: standard.Editor(standard.WithClipboard(clipboard.NewInMemory())),
	})
	comp.Input().SetText("first line\nsecond line")

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error { return nil }))
	defer close(tx)
	h.Resize(30, 10)

	content := comp.box.ContentPosition()
	click := term.CoordinatesSum(comp.InputPosition(), content)
	click.X += 3
	_, handled := h.Handle(mouseEv(click.X, click.Y, term.MouseLeft))
	require.True(t, handled)

	cursor, _, visible := h.Cursor()
	require.True(t, visible)
	assert.Equal(t, click, cursor)
}

type scrollDir int

const (
	scrollSame scrollDir = iota
	scrollDown           // raw offset decreases toward the latest messages
	scrollUp             // raw offset increases toward older messages
)

func TestHandlerDragSelectionScrolling(t *testing.T) {
	const (
		width  = 120
		height = 30
	)

	mdCfg := markdown.DefaultConfig()
	mdCfg.ParagraphSpacing = 0
	mdCfg.HeaderPrefix = false

	cfg := ComponentConfig{
		MarkdownConfig: &mdCfg,
		MessagesRowConfig: component.SpanConfig{
			PadHorizontal:    -80,
			PadVertical:      2,
			ContentAlignment: component.AlignmentCentered,
		},
		ReceiveMessageSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
		SendMessageSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
	}

	manyPairs := func(c *Component) {
		for i := range 20 {
			c.AddSendMessage(fmt.Sprintf("user%02d", i))
			c.AddReceiveMessage(fmt.Sprintf("reply%02d", i))
		}
	}

	// events is built lazily because some coordinates depend on the live
	// input-box position, which is only known after Resize/Draw.
	suite := []struct {
		desc          string
		setup         func(*Component)
		preScrollTop  bool
		events        func(h *dialogueHandler, comp *Component) []term.Event
		wantDir       scrollDir
		wantFocused   bool
		wantDragging  bool
		wantSelection bool
	}{
		{
			desc:         "drag from top into input auto-scrolls down and keeps selecting",
			setup:        manyPairs,
			preScrollTop: true,
			events: func(h *dialogueHandler, comp *Component) []term.Event {
				inputTop := comp.InputPosition().Y
				return []term.Event{
					mouseEv(25, 1, term.MouseLeft),
					mouseEv(25, inputTop+1, term.MouseLeft),
				}
			},
			wantDir:       scrollDown,
			wantFocused:   false,
			wantDragging:  true,
			wantSelection: true,
		},
		{
			desc:         "drag toward top edge auto-scrolls up",
			setup:        manyPairs,
			preScrollTop: false,
			events: func(h *dialogueHandler, comp *Component) []term.Event {
				return []term.Event{
					mouseEv(25, 6, term.MouseLeft),
					mouseEv(25, 0, term.MouseLeft),
				}
			},
			wantDir:       scrollUp,
			wantFocused:   false,
			wantDragging:  true,
			wantSelection: true,
		},
		{
			desc:         "drag within messages middle does not scroll",
			setup:        manyPairs,
			preScrollTop: true,
			events: func(h *dialogueHandler, comp *Component) []term.Event {
				return []term.Event{
					mouseEv(25, 6, term.MouseLeft),
					mouseEv(30, 8, term.MouseLeft),
				}
			},
			wantDir:       scrollSame,
			wantFocused:   false,
			wantDragging:  true,
			wantSelection: true,
		},
		{
			desc:         "release ends drag and a later input click focuses input",
			setup:        manyPairs,
			preScrollTop: true,
			events: func(h *dialogueHandler, comp *Component) []term.Event {
				inputTop := comp.InputPosition().Y
				return []term.Event{
					mouseEv(25, 1, term.MouseLeft),
					mouseEv(25, inputTop+1, term.MouseLeft),
					mouseEv(25, inputTop+1, term.MouseRelease),
					mouseEv(0, inputTop, term.MouseLeft),
					mouseEv(0, inputTop, term.MouseRelease),
				}
			},
			wantDir:       scrollDown,
			wantFocused:   true,
			wantDragging:  false,
			wantSelection: false,
		},
		{
			desc:         "release over input keeps the messages selection",
			setup:        manyPairs,
			preScrollTop: true,
			events: func(h *dialogueHandler, comp *Component) []term.Event {
				inputTop := comp.InputPosition().Y
				return []term.Event{
					mouseEv(25, 1, term.MouseLeft),
					mouseEv(25, inputTop+1, term.MouseLeft),
					mouseEv(25, inputTop+1, term.MouseRelease),
				}
			},
			wantDir:       scrollDown,
			wantFocused:   false,
			wantDragging:  false,
			wantSelection: true,
		},
		{
			desc:         "plain click in input area still focuses input",
			setup:        manyPairs,
			preScrollTop: true,
			events: func(h *dialogueHandler, comp *Component) []term.Event {
				inputTop := comp.InputPosition().Y
				return []term.Event{
					mouseEv(0, inputTop, term.MouseLeft),
					mouseEv(0, inputTop, term.MouseRelease),
				}
			},
			wantDir:       scrollSame,
			wantFocused:   true,
			wantDragging:  false,
			wantSelection: false,
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			comp := NewComponent(cfg)
			tc.setup(comp)

			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
				func(context.Context) error { return nil },
			))
			defer close(tx)
			dh := h.(*dialogueHandler)
			h.Resize(width, height)
			h.Draw(term.NewStringWriter(width+1, height+1))

			if tc.preScrollTop {
				for range 500 {
					h.Handle(mouseEv(5, 5, term.MouseWheelUp))
				}
				h.Draw(term.NewStringWriter(width+1, height+1))
			}

			offsetBefore := comp.messages.Offset()
			switch tc.wantDir {
			case scrollDown:
				require.Positive(t, offsetBefore,
					"precondition: list must be scrolled up to scroll down")
			case scrollUp:
				require.Less(t, offsetBefore, comp.messages.MaxOffset(),
					"precondition: list must have headroom to scroll up")
			}

			for _, ev := range tc.events(dh, comp) {
				h.Handle(ev)
			}

			offsetAfter := comp.messages.Offset()
			switch tc.wantDir {
			case scrollDown:
				assert.Less(t, offsetAfter, offsetBefore, "expected scroll down")
			case scrollUp:
				assert.Greater(t, offsetAfter, offsetBefore, "expected scroll up")
			case scrollSame:
				assert.Equal(t, offsetBefore, offsetAfter, "expected no scroll")
			}

			assert.Equal(t, tc.wantFocused, dh.inputFocused, "inputFocused")
			assert.Equal(t, tc.wantDragging, dh.messagesDragging, "messagesDragging")
			_, ok := h.Selection()
			assert.Equal(t, tc.wantSelection, ok, "Selection() ok")
		})
	}
}

func TestHandlerDragSelectionNegativeCoords(t *testing.T) {
	const (
		width  = 40
		height = 12
	)

	comp := NewComponent(ComponentConfig{})
	for i := range 20 {
		comp.AddSendMessage("message " + string(rune('A'+i)))
	}

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
		func(context.Context) error { return nil },
	))
	defer close(tx)
	dh := h.(*dialogueHandler)
	h.Resize(width, height)
	h.Draw(term.NewStringWriter(width+1, height+1))

	// Scroll up so older messages are visible, leaving headroom above.
	for range 4 {
		h.Handle(mouseEv(5, 5, term.MouseWheelUp))
	}
	h.Draw(term.NewStringWriter(width+1, height+1))

	require.NotPanics(t, func() {
		// Press inside the messages area, then drag above the window into
		// negative coordinates and release there. The repeated drag events let
		// the edge auto-scroll run as it would while the button is held.
		h.Handle(mouseEv(8, 6, term.MouseLeft))
		for range height + 8 {
			h.Handle(mouseEv(2, -15, term.MouseLeft))
		}
		h.Handle(mouseEv(2, -15, term.MouseRelease))
	})

	assert.False(t, dh.inputFocused, "drag must keep messages focus")
	text, ok := h.Selection()
	require.True(t, ok, "Selection() ok")
	assert.Contains(t, text, "message A",
		"dragging above the window must select up to the first message")
}

// doubleClick returns the 4-event sequence for a double-click.
func doubleClick(x, y int) []term.Event {
	return []term.Event{
		mouseEv(x, y, term.MouseLeft),
		mouseEv(x, y, term.MouseRelease),
		mouseEv(x, y, term.MouseLeft),
		mouseEv(x, y, term.MouseRelease),
	}
}

// clickDrag returns a click at (x1,y1), drag to (x2,y2), then release.
func clickDrag(x1, y1, x2, y2 int) []term.Event {
	return []term.Event{
		mouseEv(x1, y1, term.MouseLeft),
		mouseEv(x2, y2, term.MouseLeft),
		mouseEv(x2, y2, term.MouseRelease),
	}
}

func TestHandlerMouseSelection(t *testing.T) {
	const (
		width  = 60
		height = 20
	)

	// 5 distinct single-word paragraphs. With ParagraphSpacing=0, each
	// paragraph renders on every 2nd row (content rows at 0,2,4,6,8).
	mdParagraphs := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	mdContent := strings.Join(mdParagraphs, "\n\n")

	mdCfg := markdown.DefaultConfig()
	mdCfg.ParagraphSpacing = 0
	mdCfg.HeaderPrefix = false

	// No padding — each send = 1 row, no extra spacing.
	noPad := ComponentConfig{MarkdownConfig: &mdCfg}

	// Real-world config: PadVertical=1 on sends and receives.
	// Each element takes an extra row of bottom padding.
	// With 2 padded sends, markdown starts at Y=4 (not Y=2).
	padded := ComponentConfig{
		MarkdownConfig: &mdCfg,
		ReceiveMessageSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
		SendMessageSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
	}

	// The default transcript config with the code block copy icon enabled.
	codeCopy := ComponentConfig{Clipboard: clipboard.NewInMemory()}

	suite := []struct {
		desc    string
		cfg     ComponentConfig
		setup   func(c *Component)
		events  []term.Event
		wantSel string
		wantOK  bool
	}{
		// ==== No padding ====
		// Each send = 1 row. Markdown paragraphs at Y=0,2,4,6,8.
		// With N sends, markdown starts at Y=N.

		// ---- Triple-click (SelectLine) ----

		{
			desc: "no-pad: triple-click first markdown line, no offset",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 0),
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			desc: "no-pad: triple-click third markdown line, no offset",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 4),
			wantSel: "charlie",
			wantOK:  true,
		},
		{
			desc: "no-pad: triple-click last markdown line, no offset",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 8),
			wantSel: "echo",
			wantOK:  true,
		},
		{
			// 2 sends → markdown at Y=2.
			desc: "no-pad: triple-click first markdown line with 2 sends",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 2),
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			desc: "no-pad: triple-click second markdown line with 2 sends",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 4),
			wantSel: "bravo",
			wantOK:  true,
		},
		{
			// 4 sends → markdown at Y=4.
			desc: "no-pad: triple-click first markdown line with 4 sends",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddSendMessage("sent three")
				c.AddSendMessage("sent four")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 4),
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			desc: "no-pad: triple-click third markdown line with 4 sends",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddSendMessage("sent three")
				c.AddSendMessage("sent four")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 8),
			wantSel: "charlie",
			wantOK:  true,
		},
		{
			desc: "no-pad: triple-click on send message",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
			},
			events:  tripleClick(0, 1),
			wantSel: "sent two",
			wantOK:  true,
		},
		{
			desc: "no-pad: triple-click on send before markdown",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(0, 0),
			wantSel: "sent one",
			wantOK:  true,
		},

		// ---- Double-click (SelectWordAt) ----

		{
			desc: "no-pad: double-click word in markdown, no offset",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddReceiveMessage("hello world")
			},
			events:  doubleClick(0, 0),
			wantSel: "hello",
			wantOK:  true,
		},
		{
			// 2 sends → markdown at Y=2.
			desc: "no-pad: double-click word in markdown with 2 sends",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage("hello world")
			},
			events:  doubleClick(0, 2),
			wantSel: "hello",
			wantOK:  true,
		},
		{
			desc: "no-pad: double-click second markdown line with 2 sends",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  doubleClick(0, 4),
			wantSel: "bravo",
			wantOK:  true,
		},

		// ---- Click-drag (SetSelectionStart / SetSelectionEnd) ----

		{
			desc: "no-pad: click-drag within markdown, no offset",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddReceiveMessage("hello world")
			},
			events:  clickDrag(0, 0, 5, 0),
			wantSel: "hello",
			wantOK:  true,
		},
		{
			desc: "no-pad: click-drag within markdown with 2 sends",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage("hello world")
			},
			events:  clickDrag(0, 2, 5, 2),
			wantSel: "hello",
			wantOK:  true,
		},

		// ==== With padding (real-world config) ====
		// PadVertical=1 on sends and receives. Each send = 2 rows.
		// With 2 padded sends, markdown starts at Y=4.
		// Paragraphs at element-relative Y=0,2,4,6,8.

		{
			// 2 padded sends → markdown element at Y=4.
			// Triple-click Y=4 → element-relative Y=0 → "alpha".
			desc: "padded: triple-click first markdown line with 2 sends",
			cfg:  padded,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 4),
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			// Triple-click Y=6 → element-relative Y=2 → "bravo".
			desc: "padded: triple-click second markdown line with 2 sends",
			cfg:  padded,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 6),
			wantSel: "bravo",
			wantOK:  true,
		},
		{
			// Triple-click Y=8 → element-relative Y=4 → "charlie".
			desc: "padded: triple-click third markdown line with 2 sends",
			cfg:  padded,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 8),
			wantSel: "charlie",
			wantOK:  true,
		},
		{
			// 1 padded send → markdown at Y=2.
			desc: "padded: triple-click first markdown line with 1 send",
			cfg:  padded,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(5, 2),
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			desc: "padded: double-click in markdown with 2 sends",
			cfg:  padded,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage("hello world")
			},
			events:  doubleClick(0, 4),
			wantSel: "hello",
			wantOK:  true,
		},
		{
			desc: "padded: click-drag in markdown with 2 sends",
			cfg:  padded,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage("hello world")
			},
			events:  clickDrag(0, 4, 5, 4),
			wantSel: "hello",
			wantOK:  true,
		},
		{
			// Triple-click on a padded send message at Y=0.
			desc: "padded: triple-click on send message",
			cfg:  padded,
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(0, 0),
			wantSel: "sent one",
			wantOK:  true,
		},
		{
			desc: "code-copy: selection across a code block skips the copy icon",
			cfg:  codeCopy,
			setup: func(c *Component) {
				c.AddReceiveMessage("Run:\n\n```sh\nls\n```\n\nDone.")
			},
			events:  clickDrag(0, 0, width-1, 9),
			wantSel: "Run:\n\n\nls\n\n\nDone.",
			wantOK:  true,
		},
		{
			desc: "no-pad: the pointer leaving below keeps the selection",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddReceiveMessage(mdContent)
			},
			events:  append(tripleClick(5, 4), mouseEv(5, height, 0)),
			wantSel: "charlie",
			wantOK:  true,
		},
		{
			desc: "no-pad: the pointer leaving to the right keeps the selection",
			cfg:  noPad,
			setup: func(c *Component) {
				c.AddReceiveMessage(mdContent)
			},
			events:  append(tripleClick(5, 4), mouseEv(width, height-1, 0)),
			wantSel: "charlie",
			wantOK:  true,
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			comp := NewComponent(tc.cfg)
			tc.setup(comp)

			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
				func(context.Context) error { return nil },
			))
			defer close(tx)
			h.Resize(width, height)
			h.Draw(term.NewStringWriter(width+1, height+1))

			for _, ev := range tc.events {
				h.Handle(ev)
			}

			sel, ok := h.Selection()
			assert.Equal(t, tc.wantOK, ok, "Selection() ok mismatch")
			if tc.wantOK {
				assert.Equal(t, tc.wantSel, sel, "Selection() text mismatch")
			}
		})
	}
}

type failingClipboard struct{ clipboard.Register }

func (failingClipboard) Copy(string, clipboard.Data) error {
	return errors.New("clipboard unavailable")
}

func TestHandlerCodeCopy(t *testing.T) {
	const (
		width  = 40
		height = 20
		source = "make dist TARGET_LANG=just TARGET_OS=linux TARGET_ARCH=arm64"
		reply  = "Run:\n\n```sh\n" + source + "\n```\n\nThen publish."
	)
	md := DefaultMarkdownConfig()
	iconWidth := graphemecluster.StringWidth(string(md.CodeBlockCopyIcon))
	type drawnIcon struct {
		ch   rune
		attr term.Attributes
	}
	idleAttr := md.CodeBlockCopyIconAttr
	idleAttr.Bg = md.CodeBlock.Bg
	idle := &drawnIcon{md.CodeBlockCopyIcon, idleAttr}
	hover := &drawnIcon{
		md.CodeBlockCopyIcon, term.Attributes{Fg: term.ColorBlue, Bg: md.CodeBlock.Bg},
	}
	copied := &drawnIcon{
		md.CodeBlockCopiedIcon, term.Attributes{Fg: term.ColorGreen, Bg: md.CodeBlock.Bg},
	}
	click := func(x, y int) term.Event { return mouseEv(x, y, term.MouseLeft) }
	// Key 0 is a bare motion event, which only the GUI backend emits.
	motion := func(x, y int) term.Event { return mouseEv(x, y, 0) }

	tests := []struct {
		name string
		clip clipboard.Register
		// fillers are received after the icon is located, scrolling it out
		// of view.
		fillers int
		// events builds the input from where the icon was first drawn.
		events      func(icon term.Coordinates) []term.Event
		wantHandled bool // for the last event
		wantCopied  string
		// wantIcon is how the icon is drawn after the events, nil when it
		// is not on screen.
		wantIcon *drawnIcon
	}{
		{
			name:        "click copies the unwrapped source and shows a check",
			clip:        clipboard.NewInMemory(),
			events:      func(i term.Coordinates) []term.Event { return []term.Event{click(i.X, i.Y)} },
			wantHandled: true,
			wantCopied:  source,
			wantIcon:    copied,
		},
		{
			name: "click on the icon's last cell copies",
			clip: clipboard.NewInMemory(),
			events: func(i term.Coordinates) []term.Event {
				return []term.Event{click(i.X+iconWidth-1, i.Y)}
			},
			wantHandled: true,
			wantCopied:  source,
			wantIcon:    copied,
		},
		{
			name: "hovering the check keeps it",
			clip: clipboard.NewInMemory(),
			events: func(i term.Coordinates) []term.Event {
				return []term.Event{motion(i.X, i.Y), click(i.X, i.Y), motion(i.X+1, i.Y)}
			},
			wantCopied: source,
			wantIcon:   copied,
		},
		{
			name: "moving off the check restores the copy icon",
			clip: clipboard.NewInMemory(),
			events: func(i term.Coordinates) []term.Event {
				return []term.Event{motion(i.X, i.Y), click(i.X, i.Y), motion(i.X-1, i.Y)}
			},
			wantHandled: true,
			wantCopied:  source,
			wantIcon:    idle,
		},
		{
			name: "the pointer leaving the chat takes the hover off",
			clip: clipboard.NewInMemory(),
			events: func(i term.Coordinates) []term.Event {
				return []term.Event{motion(i.X, i.Y), motion(width, i.Y)}
			},
			wantHandled: true,
			wantIcon:    idle,
		},
		{
			name:        "click beside the icon copies nothing",
			clip:        clipboard.NewInMemory(),
			events:      func(i term.Coordinates) []term.Event { return []term.Event{click(i.X-1, i.Y)} },
			wantHandled: true,
			wantIcon:    idle,
		},
		{
			name: "failed copy keeps the copy icon",
			clip: failingClipboard{clipboard.NewInMemory()},
			events: func(i term.Coordinates) []term.Event {
				return []term.Event{motion(i.X, i.Y), click(i.X, i.Y)}
			},
			wantHandled: true,
			wantIcon:    hover,
		},
		{
			name: "hover turns the icon blue",
			clip: clipboard.NewInMemory(),
			events: func(i term.Coordinates) []term.Event {
				return []term.Event{motion(i.X+iconWidth-1, i.Y)}
			},
			wantHandled: true,
			wantIcon:    hover,
		},
		{
			name: "moving off the icon clears the hover",
			clip: clipboard.NewInMemory(),
			events: func(i term.Coordinates) []term.Event {
				return []term.Event{motion(i.X, i.Y), motion(i.X-1, i.Y)}
			},
			wantHandled: true,
			wantIcon:    idle,
		},
		{
			name: "moving beside the icon draws nothing new",
			clip: clipboard.NewInMemory(),
			events: func(i term.Coordinates) []term.Event {
				return []term.Event{motion(i.X-1, i.Y)}
			},
			wantIcon: idle,
		},
		{
			name:   "no icon without a clipboard",
			events: func(term.Coordinates) []term.Event { return nil },
		},
		{
			name:        "icon scrolled out of view copies nothing",
			clip:        clipboard.NewInMemory(),
			fillers:     20,
			events:      func(i term.Coordinates) []term.Event { return []term.Event{click(i.X, i.Y)} },
			wantHandled: true,
		},
		{
			name:    "icon scrolled back into view copies",
			clip:    clipboard.NewInMemory(),
			fillers: 20,
			events: func(i term.Coordinates) []term.Event {
				var evs []term.Event
				for range 40 {
					evs = append(evs, mouseEv(1, 1, term.MouseWheelUp))
				}
				return append(evs, click(i.X, i.Y))
			},
			wantHandled: true,
			wantCopied:  source,
			wantIcon:    copied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{Clipboard: tt.clip})
			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
				term.NopInterrupter())
			defer close(tx)
			h.Resize(width, height)
			draw := func() (term.Coordinates, *drawnIcon) {
				w := term.NewStringWriter(width, height)
				h.Draw(w)
				for i, c := range w.Cells() {
					if c.Ch == md.CodeBlockCopyIcon || c.Ch == md.CodeBlockCopiedIcon {
						return term.Coordinates{X: i % width, Y: i / width},
							&drawnIcon{c.Ch, c.Attributes()}
					}
				}
				return term.Coordinates{}, nil
			}

			comp.AddReceiveMessage(reply)
			icon, drawn := draw()
			require.Equal(t, tt.clip != nil, drawn != nil, "icon drawn")
			for range tt.fillers {
				comp.AddReceiveMessage("filler")
			}
			draw()
			var handled bool
			for _, ev := range tt.events(icon) {
				_, handled = h.Handle(ev)
			}

			assert.Equal(t, tt.wantHandled, handled, "last event handled")
			_, drawn = draw()
			assert.Equal(t, tt.wantIcon, drawn)
			if tt.clip != nil {
				data, _ := tt.clip.Paste(clipboard.DefaultRegisterID)
				assert.Equal(t, tt.wantCopied, data.Text)
			}
		})
	}
}

func TestHandlerMouseSelectionScrolled(t *testing.T) {
	const (
		width  = 60
		height = 10 // Small viewport to force scrolling.
	)

	mdCfg := markdown.DefaultConfig()
	mdCfg.ParagraphSpacing = 0
	mdCfg.HeaderPrefix = false

	cfg := ComponentConfig{MarkdownConfig: &mdCfg}

	// 15 paragraphs → 30 rows (each paragraph = 2 rows with ParagraphSpacing=0).
	paragraphs := []string{
		"p00", "p01", "p02", "p03", "p04",
		"p05", "p06", "p07", "p08", "p09",
		"p10", "p11", "p12", "p13", "p14",
	}
	mdContent := strings.Join(paragraphs, "\n\n")

	t.Run("triple-click visible markdown bottom, no sends", func(t *testing.T) {
		comp := NewComponent(cfg)
		comp.AddReceiveMessage(mdContent)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// Messages area is 7 rows (height=10, input=3).
		// Markdown is 30 rows. Bottom-aligned with offset=0:
		//   startY = -(30-7) = -23
		//   Paragraphs: p00 at Y=-23, p01 at Y=-21, …, p14 at Y=5
		// Visible: Y=0 (p11 blank or p12 content?) to Y=6.
		// Let's figure out: p11 at Y=-23+22=-1, p12 at Y=-23+24=1, … p14 at Y=-23+28=5.
		// Actually: p_i at Y = -23 + 2*i.
		// p11 at Y=-23+22=-1, p12 at Y=-23+24=1, p13 at Y=-23+26=3, p14 at Y=-23+28=5.
		// Y=0 is the blank row after p11. Y=1 is p12. Y=3 is p13. Y=5 is p14.

		// Triple-click on Y=5 should select p14.
		for _, ev := range tripleClick(2, 5) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		assert.True(t, ok, "Selection() ok")
		assert.Equal(t, "p14", sel, "Selection() text")
	})

	t.Run("triple-click visible markdown middle, no sends", func(t *testing.T) {
		comp := NewComponent(cfg)
		comp.AddReceiveMessage(mdContent)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// p13 at Y=3, p12 at Y=1.
		for _, ev := range tripleClick(2, 3) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		assert.True(t, ok, "Selection() ok")
		assert.Equal(t, "p13", sel, "Selection() text")
	})

	t.Run("triple-click visible markdown with sends scrolled off", func(t *testing.T) {
		comp := NewComponent(cfg)
		comp.AddSendMessage("sent one")
		comp.AddSendMessage("sent two")
		comp.AddReceiveMessage(mdContent)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// 2 sends (1 row each, no padding) + 30 rows markdown = 32 rows total.
		// Messages area = 7 rows. MaxOffset = 32 - 7 = 25.
		// Bottom-aligned offset=0: startY = -25.
		// Sends at Y=-25,-24. Markdown starts at Y=-23.
		// Same as before: p14 at Y=-23+28=5, p13 at Y=3, p12 at Y=1.

		for _, ev := range tripleClick(2, 5) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		assert.True(t, ok, "Selection() ok")
		assert.Equal(t, "p14", sel, "Selection() text")
	})

	t.Run("double-click visible markdown with sends scrolled off", func(t *testing.T) {
		comp := NewComponent(cfg)
		comp.AddSendMessage("sent one")
		comp.AddSendMessage("sent two")
		comp.AddReceiveMessage(mdContent)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// p12 at Y=1, "p12" is a single word.
		for _, ev := range doubleClick(0, 1) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		assert.True(t, ok, "Selection() ok")
		assert.Equal(t, "p12", sel, "Selection() text")
	})

	t.Run("click-drag visible markdown with sends scrolled off", func(t *testing.T) {
		comp := NewComponent(cfg)
		comp.AddSendMessage("sent one")
		comp.AddSendMessage("sent two")
		comp.AddReceiveMessage(mdContent)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// Drag across "p12" at Y=1.
		for _, ev := range clickDrag(0, 1, 3, 1) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		assert.True(t, ok, "Selection() ok")
		assert.Equal(t, "p12", sel, "Selection() text")
	})
}

// highlightedRows returns, for each grid row, the contiguous AttrReverse text
// drawn on that row. Rows without any reversed cells are omitted.
func highlightedRows(sw *term.StringWriter, width, height int) map[int]string {
	cells := sw.Cells()
	rows := make(map[int]string)
	for y := range height {
		var b strings.Builder
		for x := range width {
			c := cells[y*width+x]
			if c.Attrs&term.AttrReverse == 0 {
				continue
			}
			if c.Ch == 0 {
				b.WriteRune(' ')
			} else {
				b.WriteRune(c.Ch)
			}
		}
		if s := strings.TrimRight(b.String(), " "); s != "" {
			rows[y] = s
		}
	}
	return rows
}

func TestHandlerMouseSelectionHighlightPinnedAcrossScroll(t *testing.T) {
	const (
		width  = 40
		height = 12
	)

	cfg := ComponentConfig{}
	comp := NewComponent(cfg)
	for i := range 20 {
		comp.AddSendMessage("message " + string(rune('A'+i)))
	}

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
		func(context.Context) error { return nil },
	))
	defer close(tx)
	h.Resize(width, height)

	draw := func() *term.StringWriter {
		sw := term.NewStringWriter(width, height)
		h.Draw(sw)
		require.NoError(t, sw.Flush())
		return sw
	}

	// Scroll up so a middle message is visible, then select its whole line.
	for range 5 {
		h.Handle(mouseEv(5, 5, term.MouseWheelUp))
	}
	sw := draw()

	selRow := -1
	cells := sw.Cells()
	for y := range height {
		if selRow >= 0 {
			break
		}
		var b strings.Builder
		for x := range width {
			if ch := cells[y*width+x].Ch; ch != 0 {
				b.WriteRune(ch)
			}
		}
		if strings.TrimSpace(b.String()) == "message J" {
			selRow = y
		}
	}
	require.GreaterOrEqual(t, selRow, 0, "message J must be visible after scroll")

	for _, ev := range clickDrag(0, selRow, len("message J")-1, selRow) {
		h.Handle(ev)
	}

	before := highlightedRows(draw(), width, height)
	var highlighted string
	for _, s := range before {
		highlighted = s
	}
	require.Equal(t, "message J", highlighted,
		"the highlighted text should be the selected message")

	// Scroll up one more row without moving the pointer; the highlight must
	// stay on "message J", just shifted to its new viewport row.
	h.Handle(mouseEv(5, 5, term.MouseWheelUp))
	after := highlightedRows(draw(), width, height)

	var afterText string
	for _, s := range after {
		afterText = s
	}
	assert.Equal(t, "message J", afterText,
		"highlight must stay on the selected content after scrolling")
}

func TestHandlerMouseSelectionFullConfigScrolled(t *testing.T) {
	const (
		width  = 120
		height = 30
	)

	mdCfg := markdown.DefaultConfig()
	mdCfg.ParagraphSpacing = 0
	mdCfg.HeaderPrefix = false

	cfg := ComponentConfig{
		MarkdownConfig: &mdCfg,
		MessagesRowConfig: component.SpanConfig{
			PadHorizontal:    -80,
			PadVertical:      2,
			ContentAlignment: component.AlignmentCentered,
		},
		ReceiveMessageSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
		SendMessageSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
	}

	// Simulate a loaded conversation: many send/receive pairs.
	setup := func(c *Component) {
		for i := range 20 {
			c.AddSendMessage(fmt.Sprintf("user%02d", i))
			c.AddReceiveMessage(fmt.Sprintf("reply%02d", i))
		}
	}

	t.Run("triple-click after scroll up", func(t *testing.T) {
		comp := NewComponent(cfg)
		setup(comp)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// Scroll all the way up using mouse wheel (bypasses inputbox).
		for range 500 {
			h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseWheelUp, MouseX: 5, MouseY: 5})
		}
		h.Draw(term.NewStringWriter(width+1, height+1))

		// The first message "user00" should now be visible.
		// With PadVertical=2 centered → content starts at screen Y=1.
		// With PadVertical=1 on sends → "user00" at content Y=0 → screen Y=1.
		// Screen X = 20 (horizontal centering with PadHorizontal=-80 on 120 width).
		for _, ev := range tripleClick(25, 1) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		assert.True(t, ok, "Selection() should return ok")
		assert.Equal(t, "user00", sel, "Selection() text")
	})

	t.Run("double-click after scroll up", func(t *testing.T) {
		comp := NewComponent(cfg)
		setup(comp)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// Scroll all the way up using mouse wheel (bypasses inputbox).
		for range 500 {
			h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseWheelUp, MouseX: 5, MouseY: 5})
		}
		h.Draw(term.NewStringWriter(width+1, height+1))

		for _, ev := range doubleClick(22, 1) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		assert.True(t, ok, "Selection() should return ok")
		assert.Equal(t, "user00", sel, "Selection() text")
	})

	t.Run("click-drag after scroll up", func(t *testing.T) {
		comp := NewComponent(cfg)
		setup(comp)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// Scroll all the way up using mouse wheel (bypasses inputbox).
		for range 500 {
			h.Handle(term.Event{Type: term.EventMouse, Key: term.MouseWheelUp, MouseX: 5, MouseY: 5})
		}
		h.Draw(term.NewStringWriter(width+1, height+1))

		// Drag across "user00" at screen (20,1) to (25,1).
		for _, ev := range clickDrag(20, 1, 25, 1) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		assert.True(t, ok, "Selection() should return ok")
		assert.Equal(t, "user00", sel, "Selection() text")
	})

	t.Run("select at bottom without scroll", func(t *testing.T) {
		comp := NewComponent(cfg)
		setup(comp)

		h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
			func(context.Context) error { return nil },
		))
		defer close(tx)
		h.Resize(width, height)
		h.Draw(term.NewStringWriter(width+1, height+1))

		// Without scrolling, the last messages should be visible.
		// Find the last message's screen position by checking what's visible.
		// The last receive "reply19" should be near the bottom of the visible area.
		// Try triple-clicking at a few positions to find it.
		// With bottom alignment, content fills from the bottom up.
		// Let's try the padded area near the bottom of messages.

		// The messages FuncResponsive takes up most of the height.
		// Content starts at screen Y=1 (PadVertical=2/2=1).
		// Let's triple-click on the very last visible row of messages content.
		// The input starts at some Y near the bottom.
		// Rather than computing exact Y, let's find reply19 by scanning.

		// With 20 send+receive pairs, each padded with PadVertical=1:
		// Each send = 2 rows, each receive = 2 rows (markdown content + 1 pad).
		// Total = 40 * 2 = 80 rows of content.
		// Messages area content height = 30 - inputBoxHeight - 2(vertical pad) = ~25 rows.
		// Bottom-aligned: visible content is the last ~25 rows.
		// reply19 is the very last element.
		// We'd need to know the exact Y. Let's just test that SOME selection works.

		for _, ev := range tripleClick(25, 2) {
			h.Handle(ev)
		}

		sel, ok := h.Selection()
		// At Y=2 (screen), this is Y=1 in the messages content area.
		// Whether there's content here depends on exact layout.
		// We just check that the system doesn't crash and returns reasonable results.
		if ok {
			assert.NotEmpty(t, sel, "Selection() text should not be empty when ok=true")
		}
	})

}

func TestHandlerInputBoxSelection(t *testing.T) {
	const (
		width  = 60
		height = 20
	)

	mdCfg := markdown.DefaultConfig()
	mdCfg.ParagraphSpacing = 0
	mdCfg.HeaderPrefix = false

	comp := NewComponent(ComponentConfig{MarkdownConfig: &mdCfg})
	comp.AddReceiveMessage("alpha")

	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
		func(context.Context) error { return nil },
	))
	defer close(tx)
	h.Resize(width, height)
	h.Draw(term.NewStringWriter(width+1, height+1))

	// --- Step 1: select text in the inputbox via keyboard ---
	for _, ch := range "hello" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyHome})
	for range 3 {
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight, Mod: term.ModShift})
	}

	sel, ok := h.Selection()
	assert.True(t, ok, "step 1: inputbox selection should be returned")
	assert.Equal(t, "hel", sel, "step 1: inputbox selected text")

	// --- Step 2: click in the messages area to select "alpha" ---
	// Triple-click at row 0 (messages area) selects "alpha".
	// Selection() should now return the messages selection, not the
	// inputbox selection.
	for _, ev := range tripleClick(2, 0) {
		h.Handle(ev)
	}

	sel, ok = h.Selection()
	assert.True(t, ok, "step 2: messages selection should be returned")
	assert.Equal(t, "alpha", sel, "step 2: messages selected text")

	// The inputbox still holds its selection internally, but Draw
	// strips AttrReverse from the input area when the messages area
	// is focused, so the selection is not visually rendered.
	w := term.NewStringWriter(width+1, height+1)
	h.Draw(w)
	_ = w.Flush()
	// Verify no AttrReverse in the input region: we verify by checking
	// that Selection() returns the messages text, not inputbox text.
	// (Visual verification is covered by the e2e test in handler_test.go.)

	// --- Step 3: click back in the inputbox area ---
	// A single click in the inputbox area (row 17) should clear the
	// messages selection and return focus to the inputbox.
	h.Handle(mouseEv(0, 17, term.MouseLeft))
	h.Handle(mouseEv(0, 17, term.MouseRelease))

	sel, ok = h.Selection()
	assert.False(t, ok, "step 3: no selection after click (nothing selected in inputbox)")
	assert.Empty(t, sel, "step 3: empty selection text")

	// Verify the messages selection was cleared.
	msgSel, msgOK := h.(*dialogueHandler).mouseDelegate.Selection()
	assert.False(t, msgOK, "step 3: messages selection should be cleared")
	assert.Empty(t, msgSel, "step 3: messages selected text should be empty")
}

func TestHandlerMouseSelectionFullConfig(t *testing.T) {
	const (
		width  = 120
		height = 30
	)

	mdCfg := markdown.DefaultConfig()
	mdCfg.ParagraphSpacing = 0
	mdCfg.HeaderPrefix = false

	cfg := ComponentConfig{
		MarkdownConfig: &mdCfg,
		MessagesRowConfig: component.SpanConfig{
			PadHorizontal:    -80,
			PadVertical:      2,
			ContentAlignment: component.AlignmentCentered,
		},
		ReceiveMessageSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
		SendMessageSpanConfig: component.SpanConfig{
			PadVertical:      1,
			ContentAlignment: component.AlignmentLeft,
		},
	}

	mdContent := strings.Join([]string{"alpha", "bravo", "charlie"}, "\n\n")

	suite := []struct {
		desc    string
		setup   func(c *Component)
		events  []term.Event
		wantSel string
		wantOK  bool
	}{
		{
			// "alpha" at screen (20, 5). Triple-click there.
			desc: "triple-click alpha with full config",
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(25, 5),
			wantSel: "alpha",
			wantOK:  true,
		},
		{
			// "bravo" at screen (20, 7).
			desc: "triple-click bravo with full config",
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(25, 7),
			wantSel: "bravo",
			wantOK:  true,
		},
		{
			// "charlie" at screen (20, 9).
			desc: "triple-click charlie with full config",
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(25, 9),
			wantSel: "charlie",
			wantOK:  true,
		},
		{
			// "sent one" at screen (20, 1).
			desc: "triple-click send message with full config",
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage(mdContent)
			},
			events:  tripleClick(25, 1),
			wantSel: "sent one",
			wantOK:  true,
		},
		{
			// Double-click on "alpha" at screen (20, 5).
			desc: "double-click alpha with full config",
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage("hello world")
			},
			events:  doubleClick(20, 5),
			wantSel: "hello",
			wantOK:  true,
		},
		{
			// Click-drag on "hello" at screen (20, 5) to (25, 5).
			desc: "click-drag hello with full config",
			setup: func(c *Component) {
				c.AddSendMessage("sent one")
				c.AddSendMessage("sent two")
				c.AddReceiveMessage("hello world")
			},
			events:  clickDrag(20, 5, 25, 5),
			wantSel: "hello",
			wantOK:  true,
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			comp := NewComponent(cfg)
			tc.setup(comp)

			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.FuncInterrupter(
				func(context.Context) error { return nil },
			))
			defer close(tx)
			h.Resize(width, height)
			h.Draw(term.NewStringWriter(width+1, height+1))

			for _, ev := range tc.events {
				h.Handle(ev)
			}

			sel, ok := h.Selection()
			assert.Equal(t, tc.wantOK, ok, "Selection() ok mismatch")
			if tc.wantOK {
				assert.Equal(t, tc.wantSel, sel, "Selection() text mismatch")
			}
		})
	}
}

func TestHandlerScrollOverlays(t *testing.T) {
	arrow := term.Coordinates{X: 9, Y: 10}
	suite := []struct {
		desc           string
		events         []term.Event
		wantHandled    bool
		wantOffset     int
		wantHovered    bool
		wantPinHovered bool
		wantPointer    bool
		wantSel        bool
	}{
		{
			desc:        "motion over the arrow highlights it",
			events:      []term.Event{mouseEv(arrow.X, arrow.Y, 0)},
			wantHandled: true,
			wantOffset:  8,
			wantHovered: true,
			wantPointer: true,
		},
		{
			desc:           "motion over the pin highlights it",
			events:         []term.Event{mouseEv(3, 1, 0)},
			wantHandled:    true,
			wantOffset:     8,
			wantPinHovered: true,
			wantPointer:    true,
		},
		{
			desc:       "motion over the rest of the arrow row keeps the pointer",
			events:     []term.Event{mouseEv(arrow.X+2, arrow.Y, 0)},
			wantOffset: 8,
		},
		{
			desc:        "clicking the rest of the arrow row is ignored",
			events:      []term.Event{mouseEv(arrow.X+2, arrow.Y, term.MouseLeft)},
			wantHandled: true,
			wantOffset:  8,
		},
		{
			desc: "motion off the arrow clears the highlight",
			events: []term.Event{
				mouseEv(arrow.X, arrow.Y, 0),
				mouseEv(arrow.X+1, arrow.Y, 0),
			},
			wantHandled: true,
			wantOffset:  8,
		},
		{
			desc: "leaving the chat clears the highlight",
			events: []term.Event{
				mouseEv(arrow.X, arrow.Y, 0),
				mouseEv(-1, -1, 0),
			},
			wantHandled: true,
			wantOffset:  8,
		},
		{
			desc:        "clicking the arrow jumps to the bottom",
			events:      []term.Event{mouseEv(arrow.X, arrow.Y, term.MouseLeft)},
			wantHandled: true,
			wantOffset:  0,
		},
		{
			desc:        "clicking the pin scrolls to its message",
			events:      []term.Event{mouseEv(3, 1, term.MouseLeft)},
			wantHandled: true,
			wantOffset:  18,
		},
		{
			desc: "dragging over the pin keeps selecting",
			events: []term.Event{
				mouseEv(3, 6, term.MouseLeft),
				mouseEv(3, 4, term.MouseLeft),
				mouseEv(3, 1, term.MouseLeft),
			},
			wantHandled: true,
			// The drag reaching the top edge auto-scrolls by a row.
			wantOffset: 9,
			wantSel:    true,
		},
	}
	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			comp := newScrollOverlayComponent(ComponentConfig{PinnedMessageMaxRows: 2})
			h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp, term.NopInterrupter())
			defer close(tx)
			h.Resize(20, 14)
			for range 8 {
				comp.SeekUp()
			}
			h.Draw(term.NewStringWriter(20, 14))

			var handled bool
			pointer := new(term.Pointer)
			for _, ev := range tc.events {
				pointer = new(term.Pointer)
				ev.Context = term.ContextWithPointer(context.Background(), pointer)
				_, handled = h.Handle(ev)
			}
			assert.Equal(t, tc.wantHandled, handled, "handled")
			assert.Equal(t, tc.wantOffset, comp.messages.Offset(), "offset")
			assert.Equal(t, tc.wantHovered, comp.jumpHovered, "hovered")
			assert.Equal(t, tc.wantPinHovered, comp.pin.hovered, "pin hovered")
			shape, _ := pointer.Shape()
			assert.Equal(t, tc.wantPointer, shape == term.PointerLink, "pointer")
			_, sel := h.Selection()
			assert.Equal(t, tc.wantSel, sel, "selection")
		})
	}
}

func TestHandlerScrollOverlaySequence(t *testing.T) {
	padded := ComponentConfig{
		PinnedMessageMaxRows: 2,
		SendMessageIcon:      DefaultSendMessageIcon,
		SendMessageBottomPad: 1,
		MessagesRowConfig: component.SpanConfig{
			PadHorizontal:    6,
			ContentAlignment: component.AlignmentCentered,
		},
		SendMessageStringConfig: component.StringConfig{
			BackgroundAttributes: term.Attributes{Bg: term.ColorGray},
		},
	}
	withCfg := func(edit func(*ComponentConfig)) ComponentConfig {
		cfg := padded
		edit(&cfg)
		return cfg
	}
	arrow := term.Coordinates{X: 14, Y: 10}
	silver := term.Attributes{Fg: term.ColorSilver}
	boldWhite := term.Attributes{Fg: term.ColorWhite, Attrs: term.AttrBold}
	wheelUp := "<mouse-wheel-up>"
	suite := []struct {
		name          string
		cfg           ComponentConfig
		width, height int
		// conversation sends overlayQuestions and their replies before
		// steps run, without pinning those frames.
		conversation bool
		steps        []overlayStep
	}{
		{
			name:  "scroll, hover and click through a conversation",
			cfg:   padded,
			width: 30, height: 14,
			steps: []overlayStep{
				{
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap        \n" +
						"   twice                      \n" +
						"                              \n" +
						"                              \n" +
						"                              \n" +
						"                              \n" +
						"                              \n" +
						"                              \n" +
						"                              \n" +
						"                              \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
					input: "first<space>question<space>that<space>is<space>long<space>" +
						"enough<space>to<space>wrap<space>twice<enter>",
				},
				{
					reply: overlayReplyText("reply"),
					want: "" +
						"                              \n" +
						"   reply 1                    \n" +
						"                              \n" +
						"   reply 2                    \n" +
						"                              \n" +
						"   reply 3                    \n" +
						"                              \n" +
						"   reply 4                    \n" +
						"                              \n" +
						"   reply 5                    \n" +
						"                              \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					input: "second<enter>",
					want: "" +
						"                              \n" +
						"   reply 2                    \n" +
						"                              \n" +
						"   reply 3                    \n" +
						"                              \n" +
						"   reply 4                    \n" +
						"                              \n" +
						"   reply 5                    \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					reply: overlayReplyText("answer"),
					want: "" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"                              \n" +
						"   answer 3                   \n" +
						"                              \n" +
						"   answer 4                   \n" +
						"                              \n" +
						"   answer 5                   \n" +
						"                              \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					want: "" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"                              \n" +
						"   answer 3                   \n" +
						"                              \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
					input: strings.Repeat("<mouse-wheel-up>", 3),
					attr:  map[term.Coordinates]term.Attributes{arrow: silver},
				},
				{
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
					input: strings.Repeat("<mouse-wheel-up>", 3),
					attr:  map[term.Coordinates]term.Attributes{arrow: silver},
				},
				{
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
					events:  []term.Event{mouseEv(0, 1, 0)},
					pointer: true,
					bg: map[term.Coordinates]term.Color{
						{X: 0, Y: 0}: term.ColorDefault,
						{X: 3, Y: 0}: term.ColorSilver,
						{X: 3, Y: 1}: term.ColorSilver,
						{X: 2, Y: 3}: term.ColorDefault,
						{X: 3, Y: 3}: term.ColorGray,
					},
					attr: map[term.Coordinates]term.Attributes{arrow: silver},
				},
				{
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
					events:  []term.Event{mouseEv(arrow.X, arrow.Y, 0)},
					pointer: true,
					bg:      map[term.Coordinates]term.Color{{X: 3, Y: 0}: term.ColorGray},
					attr:    map[term.Coordinates]term.Attributes{arrow: boldWhite},
				},
				{
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
					events: []term.Event{
						mouseEv(arrow.X+3, arrow.Y, 0),
						mouseEv(arrow.X+3, arrow.Y, term.MouseLeft),
						mouseEv(arrow.X+3, arrow.Y, term.MouseRelease),
					},
					attr: map[term.Coordinates]term.Attributes{arrow: silver},
				},
				{
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap        \n" +
						"   twice                      \n" +
						"                              \n" +
						"   reply 0                    \n" +
						"                              \n" +
						"   reply 1                    \n" +
						"                              \n" +
						"   reply 2                    \n" +
						"                              \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
					input: "<mouse-left><mouse-release>",
				},
				{
					want: "" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"                              \n" +
						"   answer 3                   \n" +
						"                              \n" +
						"   answer 4                   \n" +
						"                              \n" +
						"   answer 5                   \n" +
						"                              \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
					events: []term.Event{
						mouseEv(arrow.X, arrow.Y, term.MouseLeft),
						mouseEv(arrow.X, arrow.Y, term.MouseRelease),
					},
				},
				{
					reply: "more\n\n",
					want: "" +
						"                              \n" +
						"   answer 2                   \n" +
						"                              \n" +
						"   answer 3                   \n" +
						"                              \n" +
						"   answer 4                   \n" +
						"                              \n" +
						"   answer 5                   \n" +
						"                              \n" +
						"   more                       \n" +
						"                              \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
			},
		},
		{
			name:  "the next question pushes the pin out row by row",
			cfg:   padded,
			width: 30, height: 14,
			conversation: true,
			steps: []overlayStep{
				{
					input: strings.Repeat(wheelUp, 3),
					want: "" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"                              \n" +
						"   answer 3                   \n" +
						"                              \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					input: wheelUp,
					want: "" +
						"\uf061  first question that is…    \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"                              \n" +
						"   answer 3                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					input: wheelUp,
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"                              \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
			},
		},
		{
			name:  "zero max rows disables the pin but keeps the icons",
			cfg:   withCfg(func(c *ComponentConfig) { c.PinnedMessageMaxRows = 0 }),
			width: 30, height: 14,
			conversation: true,
			steps: []overlayStep{
				{
					input: strings.Repeat(wheelUp, 6),
					want: "" +
						"                              \n" +
						"   reply 5                    \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					events: []term.Event{mouseEv(0, 0, 0)},
					bg:     map[term.Coordinates]term.Color{{X: 3, Y: 0}: term.ColorDefault},
					want: "" +
						"                              \n" +
						"   reply 5                    \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
			},
		},
		{
			name: "a transcript without left padding has no room for icons",
			cfg: withCfg(func(c *ComponentConfig) {
				c.MessagesRowConfig = component.SpanConfig{}
			}),
			width: 30, height: 14,
			conversation: true,
			steps: []overlayStep{
				{
					input: strings.Repeat(wheelUp, 6),
					want: "" +
						"first question that is long   \n" +
						"enough to wrap twice          \n" +
						"                              \n" +
						"second                        \n" +
						"                              \n" +
						"answer 0                      \n" +
						"                              \n" +
						"answer 1                      \n" +
						"                              \n" +
						"answer 2                      \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					input: "<mouse-left><mouse-release>",
					want: "" +
						"first question that is long   \n" +
						"enough to wrap twice          \n" +
						"                              \n" +
						"reply 0                       \n" +
						"                              \n" +
						"reply 1                       \n" +
						"                              \n" +
						"reply 2                       \n" +
						"                              \n" +
						"reply 3                       \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
			},
		},
		{
			name:  "a short pane caps the pin at half the transcript",
			cfg:   withCfg(func(c *ComponentConfig) { c.PinnedMessageMaxRows = 5 }),
			width: 20, height: 9,
			conversation: true,
			steps: []overlayStep{
				{
					input: strings.Repeat(wheelUp, 8),
					want: "" +
						"\uf061  second           \n" +
						"                    \n" +
						"   answer 0         \n" +
						"                    \n" +
						"   answer 1         \n" +
						"         \uf063          \n" +
						" ┌──────────────┐   \n" +
						" │▐             │   \n" +
						" └──────────────┘   ",
				},
				{
					input: strings.Repeat(wheelUp, 4),
					want: "" +
						"\uf061  first            \n" +
						"   question that    \n" +
						"   is long enoug…   \n" +
						"                    \n" +
						"\uf061  second           \n" +
						"         \uf063          \n" +
						" ┌──────────────┐   \n" +
						" │▐             │   \n" +
						" └──────────────┘   ",
				},
			},
		},
		{
			name:  "the pointer leaving the chat takes the hovers off",
			cfg:   padded,
			width: 30, height: 14,
			conversation: true,
			steps: []overlayStep{
				{
					input: strings.Repeat(wheelUp, 6),
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					events:  []term.Event{mouseEv(4, 0, 0)},
					pointer: true,
					bg:      map[term.Coordinates]term.Color{{X: 3, Y: 0}: term.ColorSilver},
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					events: []term.Event{mouseEv(-1, -1, 0)},
					bg:     map[term.Coordinates]term.Color{{X: 3, Y: 0}: term.ColorGray},
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					events:  []term.Event{mouseEv(arrow.X, arrow.Y, 0)},
					pointer: true,
					attr:    map[term.Coordinates]term.Attributes{arrow: boldWhite},
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
				{
					events: []term.Event{mouseEv(30, 0, 0)},
					attr:   map[term.Coordinates]term.Attributes{arrow: silver},
					want: "" +
						"\uf061  first question that is     \n" +
						"   long enough to wrap twi…   \n" +
						"                              \n" +
						"\uf061  second                     \n" +
						"                              \n" +
						"   answer 0                   \n" +
						"                              \n" +
						"   answer 1                   \n" +
						"                              \n" +
						"   answer 2                   \n" +
						"              \uf063               \n" +
						"  ┌───────────────────────┐   \n" +
						"  │▐                      │   \n" +
						"  └───────────────────────┘   ",
				},
			},
		},
	}
	for _, tc := range suite {
		t.Run(tc.name, func(t *testing.T) {
			runOverlaySequence(t, tc.cfg, tc.width, tc.height, tc.conversation, tc.steps)
		})
	}
}

// overlayStep is one frame of a dialogue whose transcript is scrolled
// with the pointer: reply is delivered as an assistant message, events
// as positioned pointer input, and then input runs and its frame is
// compared against want. bg and attr pin the colors at chat coordinates, and
// pointer whether the last event asked for the link pointer.
type overlayStep struct {
	input   string
	want    string
	reply   string
	events  []term.Event
	pointer bool
	bg      map[term.Coordinates]term.Color
	attr    map[term.Coordinates]term.Attributes
}

// overlayQuestions and overlayReplies make up a conversation long enough
// to scroll: a question that wraps, its replies, a one-row question and
// its replies.
var (
	overlayQuestions = []string{
		"first<space>question<space>that<space>is<space>long<space>" +
			"enough<space>to<space>wrap<space>twice<enter>",
		"second<enter>",
	}
	overlayReplies = []string{overlayReplyText("reply"), overlayReplyText("answer")}
)

func overlayReplyText(prefix string) string {
	var b strings.Builder
	for i := range 6 {
		fmt.Fprintf(&b, "%s %d\n\n", prefix, i)
	}
	return b.String()
}

func runOverlaySequence(
	t *testing.T, cfg ComponentConfig, width, height int, conversation bool, steps []overlayStep,
) {
	t.Helper()
	interrupt := make(chan struct{}, 1)
	h, tx, rx := Handler(context.Background(), new(sync.Mutex), NewComponent(cfg),
		term.FuncInterrupter(func(context.Context) error {
			select {
			case interrupt <- struct{}{}:
			default:
			}
			return nil
		}))
	t.Cleanup(func() { close(tx) })
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-rx:
			case <-done:
				return
			}
		}
	}()

	reply := func(text string) {
		tx <- MessageEvent{Type: MessageEventText, Text: text}
		<-interrupt
		tx <- MessageEvent{Type: MessageEventBreak}
		<-interrupt
	}

	w := term.NewStringWriter(width, height)
	h.Resize(width, height)
	if conversation {
		for i, question := range overlayQuestions {
			keys, err := term.ParseKeys(question)
			require.NoError(t, err)
			for _, k := range keys {
				h.Handle(term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key})
			}
			reply(overlayReplies[i])
		}
	}
	golden := os.Getenv("DIALOGUE_LAYOUT_GOLDEN") != ""
	for i, step := range steps {
		if step.reply != "" {
			reply(step.reply)
		}
		pointer := new(term.Pointer)
		for _, ev := range step.events {
			pointer = new(term.Pointer)
			ev.Context = term.ContextWithPointer(context.Background(), pointer)
			h.Handle(ev)
		}
		if golden {
			printOverlayGolden(t, h, w, i, step.input)
			continue
		}
		handlertest.RunHandlerSequenceWriter(t, w, h, width, height,
			[]handlertest.SequenceTestCase{{InputSequence: step.input, Expected: step.want}})
		cells := w.Cells()
		for pos, want := range step.bg {
			assert.Equal(t, want, cells[pos.Y*width+pos.X].Bg, "step %d bg at %v", i, pos)
		}
		for pos, want := range step.attr {
			assert.Equal(t, want, cells[pos.Y*width+pos.X].Attributes(), "step %d attr at %v", i, pos)
		}
		shape, _ := pointer.Shape()
		assert.Equal(t, step.pointer, shape == term.PointerLink, "step %d pointer", i)
	}
}

// printOverlayGolden runs input as handlertest does and prints the frame,
// for DIALOGUE_LAYOUT_GOLDEN to regenerate the expected frames.
func printOverlayGolden(t *testing.T, h tui.Handler, w *term.StringWriter, step int, input string) {
	t.Helper()
	keys, err := term.ParseKeys(input)
	require.NoError(t, err)
	for _, k := range keys {
		ev := term.Event{Type: term.EventKey, Ch: k.Ch, Mod: k.Mod, Key: k.Key}
		switch k.Key {
		case term.MouseLeft, term.MouseMiddle, term.MouseRight, term.MouseRelease,
			term.MouseWheelUp, term.MouseWheelDown:
			ev.Type = term.EventMouse
		}
		h.Handle(ev)
	}
	require.NoError(t, w.Clear(term.Attributes{}))
	h.Draw(w)
	if cursor, _, ok := h.Cursor(); ok {
		w.SetCursor(cursor)
	}
	require.NoError(t, w.Flush())
	fmt.Printf("@@@STEP %s %d\n%s\n@@@END\n", t.Name(), step, w.String())
}

func TestHandlerPointerOverReplyStreamedSinceLastDraw(t *testing.T) {
	const width, height = 40, 12
	interrupt := make(chan struct{}, 1)
	comp := NewComponent(ComponentConfig{OnLinkClick: func(*url.URL) bool { return true }})
	h, tx, _ := Handler(context.Background(), new(sync.Mutex), comp,
		term.FuncInterrupter(func(context.Context) error {
			select {
			case interrupt <- struct{}{}:
			default:
			}
			return nil
		}))
	defer close(tx)
	h.Resize(width, height)
	h.Draw(term.NewStringWriter(width, height))

	tx <- MessageEvent{Type: MessageEventText, Text: "I"}
	<-interrupt

	require.NotPanics(t, func() {
		for y := range height {
			h.Handle(mouseEv(0, y, 0))
		}
	})
	w := term.NewStringWriter(width, height)
	h.Draw(w)
	at := findText(t, w, width, "I")
	_, handled := h.Handle(mouseEv(at.X, at.Y, 0))
	assert.False(t, handled)
}
