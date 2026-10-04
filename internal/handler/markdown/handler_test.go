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

package markdown

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/handler/handlertest"
)

func TestNew(t *testing.T) {
	comp, err := markdown.New("# Hello")
	require.NoError(t, err)

	h := New(comp)
	require.NotNil(t, h)
	assert.NotNil(t, h.comp)
	assert.NotNil(t, h.mouse)
}

func TestNewWithOptions(t *testing.T) {
	comp, err := markdown.New("# Hello")
	require.NoError(t, err)

	var clicked *url.URL
	h := New(comp,
		WithOnLinkClick(func(u *url.URL) bool {
			clicked = u
			return true
		}),
		WithSelectionAttrs(term.Attributes{Attrs: term.AttrBold}),
	)

	require.NotNil(t, h)
	assert.Equal(t, term.AttrBold, h.selectionAttrs.Attrs)
	assert.NotNil(t, h.onLinkClick)

	testURL, _ := url.Parse("http://example.com")
	handled := h.onLinkClick(testURL)
	assert.True(t, handled)
	assert.Equal(t, "http://example.com", clicked.String())
}

func TestResize(t *testing.T) {
	comp, err := markdown.New("# Hello")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(80, 24)

	assert.Equal(t, 80, h.Width())
}

func TestHeight(t *testing.T) {
	comp, err := markdown.New("# Hello\n\nWorld")
	require.NoError(t, err)

	h := New(comp)
	ht := h.Height(40)
	assert.Greater(t, ht, 0)
	assert.Equal(t, ht, h.Height(40))
}

func TestResponsiveInterface(t *testing.T) {
	comp, err := markdown.New("# Hello\n\nWorld")
	require.NoError(t, err)

	h := New(comp)
	var _ handler.Responsive = h
}

func TestSelectionStartEnd(t *testing.T) {
	comp, err := markdown.New("Hello World")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	text, ok := h.Selection()
	assert.False(t, ok)
	assert.Empty(t, text)

	h.SetSelectionStart(term.Coordinates{X: 0, Y: 0})
	assert.True(t, h.hasSelection)

	h.SetSelectionEnd(term.Coordinates{X: 5, Y: 0})

	text, ok = h.Selection()
	assert.True(t, ok)
	assert.Equal(t, "Hello", text)
}

func TestClearSelection(t *testing.T) {
	comp, err := markdown.New("Hello World")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	h.SetSelectionStart(term.Coordinates{X: 0, Y: 0})
	h.SetSelectionEnd(term.Coordinates{X: 5, Y: 0})
	assert.True(t, h.hasSelection)

	h.ClearSelection()
	assert.False(t, h.hasSelection)

	text, ok := h.Selection()
	assert.False(t, ok)
	assert.Empty(t, text)
}

func TestSelectWordAt(t *testing.T) {
	comp, err := markdown.New("Hello World")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	h.SelectWordAt(term.Coordinates{X: 2, Y: 0})

	text, ok := h.Selection()
	assert.True(t, ok)
	assert.Equal(t, "Hello", text)
}

func TestSelectLine(t *testing.T) {
	comp, err := markdown.New("Hello World")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	h.SelectLine(0)

	assert.True(t, h.hasSelection)
	assert.Equal(t, 0, h.selStart.X)
	assert.Equal(t, 20, h.selEnd.X)
}

func TestScrolling(t *testing.T) {
	content := "# H1\n\n# H2\n\n# H3\n\n# H4\n\n# H5"
	comp, err := markdown.New(content)
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 3)

	assert.Equal(t, 0, h.SeekOffset())

	assert.True(t, h.ScrollDown(3))
	assert.Equal(t, 3, h.SeekOffset())

	assert.True(t, h.ScrollUp(2))
	assert.Equal(t, 1, h.SeekOffset())

	assert.True(t, h.ScrollUp(10))
	assert.Equal(t, 0, h.SeekOffset())
}

func TestDimensions(t *testing.T) {
	comp, err := markdown.New("# Hello\n\nWorld")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(40, 10)

	w, ht := h.Dimensions()
	assert.Greater(t, w, 0)
	assert.Greater(t, ht, 0)
}

func TestCursor(t *testing.T) {
	comp, err := markdown.New("# Hello")
	require.NoError(t, err)

	h := New(comp)
	_, _, show := h.Cursor()
	assert.False(t, show)
}

func TestLinkClickCallback(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		clickX      int
		clickY      int
		expectCall  bool
		expectedURL string
	}{
		{
			name:        "click on link",
			content:     "[Click me](http://example.com)",
			clickX:      3,
			clickY:      0,
			expectCall:  true,
			expectedURL: "http://example.com",
		},
		{
			name:       "click on non-link",
			content:    "Plain text",
			clickX:     3,
			clickY:     0,
			expectCall: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp, err := markdown.New(tt.content)
			require.NoError(t, err)

			var clickedURL *url.URL
			h := New(comp, WithOnLinkClick(func(u *url.URL) bool {
				clickedURL = u
				return true
			}))
			h.Resize(40, 5)

			pos := term.Coordinates{X: tt.clickX, Y: tt.clickY}
			handled := h.OnAction(term.Event{}, pos, mouse.LeftClick)

			if tt.expectCall {
				assert.True(t, handled)
				assert.Equal(t, tt.expectedURL, clickedURL.String())
			} else {
				assert.False(t, handled)
				assert.Nil(t, clickedURL)
			}
		})
	}
}

func TestLocalAnchorScrolling(t *testing.T) {
	content := "# First Header\n\nSome text with [link](#second-header)\n\n# Second Header\n\nMore text"
	comp, err := markdown.New(content)
	require.NoError(t, err)

	h := New(comp) // no custom onLinkClick, uses default anchor handling
	h.Resize(40, 5)

	initialOffset := h.SeekOffset()

	pos := term.Coordinates{X: 17, Y: 3}
	handled := h.OnAction(term.Event{}, pos, mouse.LeftClick)

	if handled {
		assert.NotEqual(t, initialOffset, h.SeekOffset(), "Anchor should scroll to header")
	}
}

func TestDrawWithSelection(t *testing.T) {
	comp, err := markdown.New("Hello World")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	h.SetSelectionStart(term.Coordinates{X: 0, Y: 0})
	h.SetSelectionEnd(term.Coordinates{X: 5, Y: 0})

	w := term.NewStringWriter(20, 5)
	require.NotPanics(t, func() {
		h.Draw(w)
	})
}

func TestHandleMouseEvent(t *testing.T) {
	comp, err := markdown.New("Hello World")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	ev := term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseX: 2,
		MouseY: 0,
	}

	_, handled := h.Handle(ev)
	assert.True(t, handled)
}

func TestHandleNonMouseEvent(t *testing.T) {
	comp, err := markdown.New("Hello World")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	ev := term.Event{
		Type: term.EventKey,
		Key:  term.KeyEnter,
	}

	_, handled := h.Handle(ev)
	assert.False(t, handled)
}

func TestHandleModifiedCharKeysFallThrough(t *testing.T) {
	// Ctrl scrolling shortcuts (Ctrl-F/B/D/U/E/Y/N/P) are handled by the
	// viewer itself, so they are excluded from the Ctrl fall-through
	// set below and covered by the scrolling tests.
	ctrlScrolling := map[rune]bool{
		'f': true, 'b': true, 'd': true, 'u': true,
		'e': true, 'y': true, 'n': true, 'p': true,
	}
	for _, mod := range []term.Modifier{term.ModCtrl, term.ModAlt, term.ModMeta} {
		for _, ch := range []rune{'q', 'n', 'p', 'N', 'j', 'k', 'g', 'G', 'b', 'f', 'd', 'u', 'e', 'y', '/'} {
			if mod == term.ModCtrl && ctrlScrolling[ch] {
				continue
			}
			comp, err := markdown.New("Hello World")
			require.NoError(t, err)
			h := New(comp)
			h.Resize(20, 5)

			ev := term.Event{Type: term.EventKey, Ch: ch, Mod: mod}
			exit, handled := h.Handle(ev)
			assert.False(t, handled,
				"modified char %q (mod %d) must fall through unhandled", ch, mod)
			assert.False(t, exit,
				"modified char %q (mod %d) must not request exit", ch, mod)
		}
	}
}

func TestScrollDownAtMax(t *testing.T) {
	comp, err := markdown.New("Short")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 10)

	assert.Equal(t, 0, h.MaxSeekOffset())

	assert.False(t, h.ScrollDown(1))
}

func TestScrollUpAtMin(t *testing.T) {
	comp, err := markdown.New("Short")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 10)

	assert.False(t, h.ScrollUp(1))
}

func TestOnActionNonLeftClick(t *testing.T) {
	comp, err := markdown.New("[link](http://example.com)")
	require.NoError(t, err)

	var clicked bool
	h := New(comp, WithOnLinkClick(func(u *url.URL) bool {
		clicked = true
		return true
	}))
	h.Resize(20, 5)

	pos := term.Coordinates{X: 2, Y: 0}
	handled := h.OnAction(term.Event{}, pos, mouse.RightClick)

	assert.False(t, handled)
	assert.False(t, clicked)
}

func TestSelectionPersistsThroughScroll(t *testing.T) {
	content := "# H1\n\n# H2\n\n# H3\n\n# H4\n\n# H5"
	comp, err := markdown.New(content)
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 3)

	h.SetSelectionStart(term.Coordinates{X: 0, Y: 0})
	h.SetSelectionEnd(term.Coordinates{X: 5, Y: 0})
	assert.True(t, h.hasSelection)

	h.ScrollDown(3)

	assert.True(t, h.hasSelection)
}

func TestKeyboardScrolling(t *testing.T) {
	content := "Line1\n\nLine2\n\nLine3\n\nLine4\n\nLine5\n\nLine6"
	_, err := markdown.New(content)
	require.NoError(t, err)

	width, height := 10, 3

	t.Run("j scrolls down", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "", Expected: "Line1     \n          \nLine2     "},
			{InputSequence: "j", Expected: "          \nLine2     \n          "},
			{InputSequence: "j", Expected: "Line2     \n          \nLine3     "},
		}
		handlertest.RunHandlerSequence(t, h, width, height, cases)
	})

	t.Run("k scrolls up", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		h.Resize(width, height)
		h.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		h.Handle(term.Event{Type: term.EventKey, Ch: 'j'})

		cases := []handlertest.SequenceTestCase{
			{InputSequence: "", Expected: "Line2     \n          \nLine3     "},
			{InputSequence: "k", Expected: "          \nLine2     \n          "},
			{InputSequence: "k", Expected: "Line1     \n          \nLine2     "},
		}
		handlertest.RunHandlerSequence(t, h, width, height, cases)
	})

	t.Run("arrow keys scroll", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "", Expected: "Line1     \n          \nLine2     "},
			{InputSequence: "<down>", Expected: "          \nLine2     \n          "},
		}
		handlertest.RunHandlerSequence(t, h, width, height, cases)

		cases = []handlertest.SequenceTestCase{
			{InputSequence: "<up>", Expected: "Line1     \n          \nLine2     "},
		}
		handlertest.RunHandlerSequence(t, h, width, height, cases)
	})

	t.Run("ctrl-n and ctrl-p scroll", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "", Expected: "Line1     \n          \nLine2     "},
			{InputSequence: "<c-n>", Expected: "          \nLine2     \n          "},
			{InputSequence: "<c-p>", Expected: "Line1     \n          \nLine2     "},
		}
		handlertest.RunHandlerSequence(t, h, width, height, cases)
	})

	t.Run("g goes to top", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		h.Resize(width, height)
		for range 5 {
			h.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		}

		cases := []handlertest.SequenceTestCase{
			{InputSequence: "g", Expected: "Line1     \n          \nLine2     "},
		}
		handlertest.RunHandlerSequence(t, h, width, height, cases)
	})

	t.Run("G goes to bottom", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		cases := []handlertest.SequenceTestCase{
			{InputSequence: "", Expected: "Line1     \n          \nLine2     "},
		}
		handlertest.RunHandlerSequence(t, h, width, height, cases)

		h.Handle(term.Event{Type: term.EventKey, Ch: 'G'})
		assert.Equal(t, h.MaxSeekOffset(), h.SeekOffset())
	})
}

func TestKeyboardPageScrolling(t *testing.T) {
	content := "L1\n\nL2\n\nL3\n\nL4\n\nL5\n\nL6\n\nL7\n\nL8\n\nL9"
	width, height := 5, 3

	t.Run("f scrolls page down", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		h.Resize(width, height)

		offsetBefore := h.SeekOffset()
		h.Handle(term.Event{Type: term.EventKey, Ch: 'f'})
		offsetAfter := h.SeekOffset()

		assert.Greater(t, offsetAfter, offsetBefore)
		assert.LessOrEqual(t, offsetAfter-offsetBefore, height)
	})

	t.Run("b scrolls page up", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		h.Resize(width, height)

		h.Handle(term.Event{Type: term.EventKey, Ch: 'G'})
		offsetBefore := h.SeekOffset()

		h.Handle(term.Event{Type: term.EventKey, Ch: 'b'})
		offsetAfter := h.SeekOffset()

		assert.Less(t, offsetAfter, offsetBefore)
	})

	t.Run("d scrolls half page down", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		h.Resize(width, height)

		offsetBefore := h.SeekOffset()
		h.Handle(term.Event{Type: term.EventKey, Ch: 'd'})
		offsetAfter := h.SeekOffset()

		assert.Greater(t, offsetAfter, offsetBefore)
	})

	t.Run("u scrolls half page up", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		h.Resize(width, height)

		h.Handle(term.Event{Type: term.EventKey, Ch: 'G'})
		offsetBefore := h.SeekOffset()

		h.Handle(term.Event{Type: term.EventKey, Ch: 'u'})
		offsetAfter := h.SeekOffset()

		assert.Less(t, offsetAfter, offsetBefore)
	})

	t.Run("space scrolls page down", func(t *testing.T) {
		comp, _ := markdown.New(content)
		h := New(comp)
		h.Resize(width, height)

		offsetBefore := h.SeekOffset()
		h.Handle(term.Event{Type: term.EventKey, Key: term.KeySpace})
		offsetAfter := h.SeekOffset()

		assert.Greater(t, offsetAfter, offsetBefore)
	})
}

// TestCtrlPageScrolling verifies the vim-style Ctrl paging shortcuts:
// Ctrl-F/Ctrl-B page down/up, Ctrl-D/Ctrl-U half-page down/up, and
// Ctrl-E/Ctrl-Y scroll a single line down/up.
// pagingContent is a document tall enough (many blank-line-separated
// paragraphs) to scroll past several viewports at the small heights
// used by the paging tests.
const pagingContent = "L1\n\nL2\n\nL3\n\nL4\n\nL5\n\nL6\n\nL7\n\nL8\n\nL9\n\nL10\n\nL11\n\nL12"

// startPos selects where the viewport is parked before a key is sent.
type startPos int

const (
	startTop    startPos = iota // top of the document (offset 0)
	startBottom                 // bottom of the document (offset == max)
	startMiddle                 // roughly the middle of the document
)

// newPagingHandler builds a handler over content, resizes it, and parks
// the viewport at the requested start position.
func newPagingHandler(t *testing.T, content string, width, height int, pos startPos) *Handler {
	t.Helper()
	comp, err := markdown.New(content)
	require.NoError(t, err)
	h := New(comp)
	h.Resize(width, height)
	switch pos {
	case startBottom:
		h.Handle(term.Event{Type: term.EventKey, Ch: 'G'})
	case startMiddle:
		target := h.MaxSeekOffset() / 2
		for h.SeekOffset() < target {
			if !h.SeekDown() {
				break
			}
		}
	}
	return h
}

func TestCtrlPageScrolling(t *testing.T) {
	const width, height = 5, 3

	// dir reports the expected sign of (afterOffset - beforeOffset):
	// +1 down, -1 up.
	tests := []struct {
		name string
		ch   rune
		dir  int
		// maxStep bounds how far a single keypress may move when it is
		// not clamped by a boundary. 0 means "unbounded (full page)".
		maxStep int
	}{
		{name: "ctrl-f page down", ch: 'f', dir: 1, maxStep: height},
		{name: "ctrl-b page up", ch: 'b', dir: -1, maxStep: height},
		{name: "ctrl-d half page down", ch: 'd', dir: 1, maxStep: max(1, height/2)},
		{name: "ctrl-u half page up", ch: 'u', dir: -1, maxStep: max(1, height/2)},
		{name: "ctrl-e line down", ch: 'e', dir: 1, maxStep: 1},
		{name: "ctrl-y line up", ch: 'y', dir: -1, maxStep: 1},
	}

	for _, tt := range tests {
		for _, start := range []struct {
			name string
			pos  startPos
			// clamped is true when a keypress in tt.dir cannot move from
			// this start position (already at the relevant boundary).
			clamped bool
		}{
			{name: "from top", pos: startTop, clamped: tt.dir < 0},
			{name: "from middle", pos: startMiddle, clamped: false},
			{name: "from bottom", pos: startBottom, clamped: tt.dir > 0},
		} {
			t.Run(tt.name+" "+start.name, func(t *testing.T) {
				h := newPagingHandler(t, pagingContent, width, height, start.pos)
				before := h.SeekOffset()

				exit, handled := h.Handle(term.Event{Type: term.EventKey, Ch: tt.ch, Mod: term.ModCtrl})
				after := h.SeekOffset()

				assert.False(t, exit, "scroll key must not request exit")
				assert.True(t, handled, "ctrl scroll key must be handled")
				assert.GreaterOrEqual(t, after, 0, "offset must never go negative")
				assert.LessOrEqual(t, after, h.MaxSeekOffset(), "offset must never exceed max")

				if start.clamped {
					assert.Equal(t, before, after, "at boundary the offset must not change")
					return
				}

				delta := after - before
				switch tt.dir {
				case 1:
					assert.Greater(t, delta, 0, "down key must increase offset")
					assert.LessOrEqual(t, delta, tt.maxStep, "must not overshoot one step")
				case -1:
					assert.Less(t, delta, 0, "up key must decrease offset")
					assert.GreaterOrEqual(t, -delta, 1)
					assert.LessOrEqual(t, -delta, tt.maxStep, "must not overshoot one step")
				}
			})
		}
	}
}

func TestCtrlPageScrollingBoundaryIdempotent(t *testing.T) {
	const width, height = 5, 3

	tests := []struct {
		name  string
		ch    rune
		start startPos
		want  func(h *Handler) int // expected pinned offset
	}{
		{name: "ctrl-f at bottom stays at max", ch: 'f', start: startBottom, want: func(h *Handler) int { return h.MaxSeekOffset() }},
		{name: "ctrl-d at bottom stays at max", ch: 'd', start: startBottom, want: func(h *Handler) int { return h.MaxSeekOffset() }},
		{name: "ctrl-e at bottom stays at max", ch: 'e', start: startBottom, want: func(h *Handler) int { return h.MaxSeekOffset() }},
		{name: "ctrl-b at top stays at zero", ch: 'b', start: startTop, want: func(*Handler) int { return 0 }},
		{name: "ctrl-u at top stays at zero", ch: 'u', start: startTop, want: func(*Handler) int { return 0 }},
		{name: "ctrl-y at top stays at zero", ch: 'y', start: startTop, want: func(*Handler) int { return 0 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newPagingHandler(t, pagingContent, width, height, tt.start)
			want := tt.want(h)
			for range 5 {
				exit, handled := h.Handle(term.Event{Type: term.EventKey, Ch: tt.ch, Mod: term.ModCtrl})
				assert.False(t, exit)
				assert.True(t, handled)
				assert.Equal(t, want, h.SeekOffset())
			}
		})
	}
}

func TestCtrlPageScrollingNonScrollableContent(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "empty document", content: ""},
		{name: "single word", content: "Short"},
		{name: "single line fits", content: "one line only"},
		{name: "exactly viewport height", content: "L1\n\nL2"},
	}

	for _, tc := range tests {
		for _, ch := range []rune{'f', 'b', 'd', 'u', 'e', 'y'} {
			t.Run(tc.name+" ctrl-"+string(ch), func(t *testing.T) {
				comp, err := markdown.New(tc.content)
				require.NoError(t, err)
				h := New(comp)
				h.Resize(20, 5)

				require.Equal(t, 0, h.MaxSeekOffset(), "content must fit viewport")

				exit, handled := h.Handle(term.Event{Type: term.EventKey, Ch: ch, Mod: term.ModCtrl})
				assert.False(t, exit)
				assert.True(t, handled)
				assert.Equal(t, 0, h.SeekOffset(), "no scrolling possible")
			})
		}
	}
}

func TestCtrlPageScrollingBeforeResize(t *testing.T) {
	for _, ch := range []rune{'f', 'b', 'd', 'u', 'e', 'y'} {
		t.Run("ctrl-"+string(ch), func(t *testing.T) {
			comp, err := markdown.New(pagingContent)
			require.NoError(t, err)
			h := New(comp) // no Resize call

			exit, handled := h.Handle(term.Event{Type: term.EventKey, Ch: ch, Mod: term.ModCtrl})
			assert.False(t, exit)
			assert.True(t, handled)
			assert.GreaterOrEqual(t, h.SeekOffset(), 0)
			assert.LessOrEqual(t, h.SeekOffset(), h.MaxSeekOffset())
		})
	}
}

func TestCtrlPageScrollingContentChange(t *testing.T) {
	const width, height = 5, 3

	tests := []struct {
		name       string
		newContent string
		ch         rune
		// wantScroll is true when the key is expected to move the offset
		// on the new content from the top.
		wantScroll bool
	}{
		{name: "swap to tall content then page down", newContent: pagingContent, ch: 'f', wantScroll: true},
		{name: "swap to tall content then half page down", newContent: pagingContent, ch: 'd', wantScroll: true},
		{name: "swap to tall content then line down", newContent: pagingContent, ch: 'e', wantScroll: true},
		{name: "swap to short content then page down", newContent: "tiny", ch: 'f', wantScroll: false},
		{name: "swap to short content then line down", newContent: "tiny", ch: 'e', wantScroll: false},
		{name: "swap to empty content then page down", newContent: "", ch: 'f', wantScroll: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Start scrolled to the bottom of a tall document.
			h := newPagingHandler(t, pagingContent, width, height, startBottom)
			require.Positive(t, h.SeekOffset(), "precondition: scrolled away from top")

			// Swap the underlying content under the hood.
			comp, err := markdown.New(tt.newContent)
			require.NoError(t, err)
			h.SetComponent(comp)

			assert.Equal(t, 0, h.SeekOffset(), "SetComponent must reset the offset")
			before := h.SeekOffset()

			exit, handled := h.Handle(term.Event{Type: term.EventKey, Ch: tt.ch, Mod: term.ModCtrl})
			after := h.SeekOffset()

			assert.False(t, exit)
			assert.True(t, handled)
			assert.GreaterOrEqual(t, after, 0)
			assert.LessOrEqual(t, after, h.MaxSeekOffset())
			if tt.wantScroll {
				assert.Greater(t, after, before, "should scroll on new tall content")
			} else {
				assert.Equal(t, before, after, "nothing to scroll on new content")
			}
		})
	}
}

func TestWrappedCodeBlockRendering(t *testing.T) {
	tests := []struct {
		name    string
		content string
		width   int
		height  int
		cases   []handlertest.SequenceTestCase
	}{
		{
			name:    "single wrapped code block vertical scroll",
			content: "```\n0123456789\nabcdefghij\n```",
			width:   5,
			height:  3,
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "", Expected: ".....\n.....\n....."},
				{InputSequence: "<down>", Expected: ".....\n.....\n....."},
				{InputSequence: "<down>", Expected: ".....\n.....\n     "},
			},
		},
		{
			name:    "wrapped code block after paragraph spacing",
			content: "Intro\n\n```\nabcdef\n```",
			width:   3,
			height:  4,
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "", Expected: "Int\nro \n   \n..."},
				{InputSequence: "<down><down>", Expected: "   \n...\n...\n   "},
			},
		},
		{
			name:    "wrapped partial code row shows background fill",
			content: "```\nabcd\n```",
			width:   3,
			height:  3,
			cases: []handlertest.SequenceTestCase{
				{InputSequence: "", Expected: "...\n...\n   "},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp, err := markdown.New(tt.content)
			require.NoError(t, err)

			h := New(comp)
			h.Resize(tt.width, tt.height)

			writer := term.NewStringWriter(tt.width, tt.height)
			writer.BackgroundCh = '.'
			handlertest.RunHandlerSequenceWriter(t, writer, h, tt.width, tt.height, tt.cases)
		})
	}
}

func TestKeyboardExit(t *testing.T) {
	comp, err := markdown.New("Hello")
	require.NoError(t, err)
	h := New(comp)
	h.Resize(10, 3)

	t.Run("q exits", func(t *testing.T) {
		exit, handled := h.Handle(term.Event{Type: term.EventKey, Ch: 'q'})
		assert.True(t, exit)
		assert.True(t, handled)
	})

	t.Run("Esc exits", func(t *testing.T) {
		exit, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
		assert.True(t, exit)
		assert.True(t, handled)
	})
}

func TestMouseSelectionIntegration(t *testing.T) {
	comp, err := markdown.New("Hello World")
	require.NoError(t, err)
	h := New(comp)
	width, height := 20, 5
	h.Resize(width, height)

	h.Handle(term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseX: 0,
		MouseY: 0,
	})

	w := term.NewStringWriter(width, height)
	h.Draw(w)
	_ = w.Flush()

	h.Handle(term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseX: 5,
		MouseY: 0,
	})

	text, ok := h.Selection()
	assert.True(t, ok)
	assert.Equal(t, "Hello", text)

	cases := []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: "Hello World         \n                    \n                    \n                    \n                    "},
	}
	handlertest.RunHandlerSequence(t, h, width, height, cases)
}

func TestMouseScrollIntegration(t *testing.T) {
	content := "Line1\n\nLine2\n\nLine3\n\nLine4\n\nLine5"
	comp, err := markdown.New(content)
	require.NoError(t, err)
	h := New(comp)
	width, height := 10, 3
	h.Resize(width, height)

	cases := []handlertest.SequenceTestCase{
		{InputSequence: "", Expected: "Line1     \n          \nLine2     "},
	}
	handlertest.RunHandlerSequence(t, h, width, height, cases)

	h.Handle(term.Event{
		Type: term.EventMouse,
		Key:  term.MouseWheelDown,
	})

	w := term.NewStringWriter(width, height)
	h.Draw(w)
	_ = w.Flush()
	assert.Equal(t, 1, h.SeekOffset())

	h.Handle(term.Event{
		Type: term.EventMouse,
		Key:  term.MouseWheelUp,
	})
	assert.Equal(t, 0, h.SeekOffset())
}

func TestHandlerSearch(t *testing.T) {
	comp, err := markdown.New("hello world hello")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	h.Search("hello")

	// LocationSlice starts at index 0. First Next moves to second result.
	ok := h.SeekToNextSearchResult()
	assert.True(t, ok)

	// End of results.
	ok = h.SeekToNextSearchResult()
	assert.False(t, ok)

	// Go back.
	ok = h.SeekToPrevSearchResult()
	assert.True(t, ok)
}

func TestHandlerSearchNoResults(t *testing.T) {
	comp, err := markdown.New("hello world")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	h.Search("xyz")

	assert.False(t, h.SeekToNextSearchResult())
	assert.False(t, h.SeekToPrevSearchResult())
}

func TestSlashSearch(t *testing.T) {
	content := "hello world hello"
	comp, err := markdown.New(content)
	require.NoError(t, err)

	h := New(comp)
	width, height := 20, 5
	h.Resize(width, height)

	// '/' opens search prompt.
	_, handled := h.Handle(term.Event{Type: term.EventKey, Ch: '/'})
	assert.True(t, handled)

	// Cursor should be visible (search prompt).
	_, _, show := h.Cursor()
	assert.True(t, show)

	// Type "hello".
	for _, ch := range "hello" {
		h.Handle(term.Event{Type: term.EventKey, Ch: ch})
	}

	// Enter confirms search.
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})

	// Cursor should hide again.
	_, _, show = h.Cursor()
	assert.False(t, show)

	// 'n' advances to next result.
	_, handled = h.Handle(term.Event{Type: term.EventKey, Ch: 'n'})
	assert.True(t, handled)

	// 'N' goes back.
	_, handled = h.Handle(term.Event{Type: term.EventKey, Ch: 'N'})
	assert.True(t, handled)
}

func TestSlashSearchEscCancels(t *testing.T) {
	comp, err := markdown.New("hello world")
	require.NoError(t, err)

	h := New(comp)
	h.Resize(20, 5)

	// '/' then type then Esc should cancel search.
	h.Handle(term.Event{Type: term.EventKey, Ch: '/'})
	h.Handle(term.Event{Type: term.EventKey, Ch: 'h'})

	exit, handled := h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.False(t, exit, "Esc during search should not exit handler")
	assert.True(t, handled)

	// Esc again should exit the handler (no active search prompt).
	exit, _ = h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	assert.True(t, exit)
}

func TestSlashSearchDraw(t *testing.T) {
	comp, err := markdown.New("Hi")
	require.NoError(t, err)

	h := New(comp)
	width, height := 10, 3
	h.Resize(width, height)

	// Open search prompt.
	h.Handle(term.Event{Type: term.EventKey, Ch: '/'})
	h.Handle(term.Event{Type: term.EventKey, Ch: 'h'})
	h.Handle(term.Event{Type: term.EventKey, Ch: 'i'})

	w := term.NewStringWriter(width, height)
	_ = w.Clear(term.Attributes{})
	h.Draw(w)
	_ = w.Flush()

	lines := w.String()
	// Last line should show the search prompt.
	assert.Contains(t, lines, "/hi")
}
