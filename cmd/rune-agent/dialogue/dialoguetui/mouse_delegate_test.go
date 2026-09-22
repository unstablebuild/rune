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
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	tterm "unstable.build/rune/internal/term"
)

var _ mouse.Delegate = (*mouseDelegate)(nil)

// drawGrid creates a SelectionWriter, resizes and draws the component into it.
func drawGrid(comp *Component, width, height int) *tterm.SelectionWriter {
	var g tterm.SelectionWriter
	g.Resize(width, height)
	comp.Draw(&g)
	return &g
}

func TestMouseDelegateSelection(t *testing.T) {
	const (
		width  = 30
		height = 10
	)

	suite := []struct {
		desc    string
		setup   func(*Component)
		input   string // mouse key sequence via term.ParseKeys
		coords  [][2]int
		wantSel string
		wantOK  bool
	}{
		{
			desc: "click-drag on send message",
			setup: func(c *Component) {
				c.AddSendMessage("Hello send")
			},
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{0, 0}, {4, 0}, {4, 0}},
			wantSel: "Hello",
			wantOK:  true,
		},
		{
			desc: "click-drag on received markdown message",
			setup: func(c *Component) {
				c.AddReceiveMessage("Hello markdown")
			},
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{0, 0}, {13, 0}, {13, 0}},
			wantSel: "Hello markdown",
			wantOK:  true,
		},
		{
			desc: "click-drag on empty area yields no selection",
			setup: func(c *Component) {
				c.AddSendMessage("Hello send")
			},
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{0, 5}, {5, 5}, {5, 5}},
			wantSel: "",
			wantOK:  false,
		},
		{
			desc: "negative coordinates do not panic",
			setup: func(c *Component) {
				c.AddSendMessage("Hello send")
			},
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{-12, -4}, {-1, -1}, {-1, -1}},
			wantSel: "",
			wantOK:  false,
		},
		{
			desc: "drag up off the top selects from press to first line",
			setup: func(c *Component) {
				c.AddSendMessage("msg1")
				c.AddSendMessage("msg2")
				c.AddSendMessage("msg3")
			},
			// Press on msg3, drag above the window: the selection must span
			// from the press up to the top of the conversation.
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{4, 2}, {2, -5}, {2, -5}},
			wantSel: "msg1\nmsg2\nmsg3",
			wantOK:  true,
		},
		{
			desc: "drag to negative X clamps to line start",
			setup: func(c *Component) {
				c.AddSendMessage("msg1")
				c.AddSendMessage("msg2")
			},
			// Press at end of msg2, drag left past column 0 on the same row.
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{3, 1}, {-6, 1}, {-6, 1}},
			wantSel: "msg2",
			wantOK:  true,
		},
		{
			desc: "press off-window above then drag onto content",
			setup: func(c *Component) {
				c.AddSendMessage("msg1")
				c.AddSendMessage("msg2")
			},
			// Press above the window, then drag down onto msg2: selection runs
			// from the off-window press down to the release cell.
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{0, -3}, {3, 1}, {3, 1}},
			wantSel: "msg1\nmsg2",
			wantOK:  true,
		},
		{
			desc: "drag fully off-window in both axes yields no content",
			setup: func(c *Component) {
				c.AddSendMessage("msg1")
			},
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{-4, -4}, {-8, -8}, {-8, -8}},
			wantSel: "",
			wantOK:  false,
		},
		{
			desc: "cross-element selection across two send messages",
			setup: func(c *Component) {
				c.AddSendMessage("msg1")
				c.AddSendMessage("msg2")
			},
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{0, 0}, {3, 1}, {3, 1}},
			wantSel: "msg1\nmsg2",
			wantOK:  true,
		},
		{
			desc: "cross-element drag upward",
			setup: func(c *Component) {
				c.AddSendMessage("msg1")
				c.AddSendMessage("msg2")
			},
			input:   "<mouse-left><mouse-left><mouse-release>",
			coords:  [][2]int{{3, 1}, {0, 0}, {0, 0}},
			wantSel: "msg1\nmsg2",
			wantOK:  true,
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{})
			tc.setup(comp)
			comp.Resize(width, height)

			grid := drawGrid(comp, width, height)
			d := newMouseDelegate(grid, comp)
			m := mouse.New(d)

			keys, err := term.ParseKeys(tc.input)
			require.NoError(t, err)
			require.Len(t, keys, len(tc.coords), "coords must match parsed keys")

			for i, kc := range keys {
				m.Handle(term.Event{
					Type:   term.EventMouse,
					Key:    kc.Key,
					Mod:    kc.Mod,
					MouseX: tc.coords[i][0],
					MouseY: tc.coords[i][1],
				})
			}

			text, ok := d.Selection()
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantSel, text)
		})
	}
}

func TestMouseDelegateSelectWordAt(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.AddSendMessage("Hello send")
	comp.Resize(30, 10)

	grid := drawGrid(comp, 30, 10)
	d := newMouseDelegate(grid, comp)

	// SelectWordAt on X=2 selects the word "Hello".
	d.SelectWordAt(term.Coordinates{X: 2, Y: 0})

	text, ok := d.Selection()
	assert.True(t, ok)
	assert.Equal(t, "Hello", text)
}

func TestMouseDelegateSelectLine(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.AddSendMessage("Hello send")
	comp.Resize(30, 10)

	grid := drawGrid(comp, 30, 10)
	d := newMouseDelegate(grid, comp)

	// SelectLine on row 0 selects the full line content.
	d.SelectLine(0)

	text, ok := d.Selection()
	assert.True(t, ok)
	assert.Equal(t, "Hello send", text)
}

func TestMouseDelegateScrollN(t *testing.T) {
	const (
		width  = 30
		height = 5
	)

	comp := NewComponent(ComponentConfig{})
	// Add enough messages to make the list scrollable.
	for i := range 20 {
		comp.AddSendMessage("message " + string(rune('A'+i)))
	}
	comp.Resize(width, height)

	grid := drawGrid(comp, width, height)
	d := newMouseDelegate(grid, comp)

	// Scroll to the top so we can measure downward scrolls.
	for d.ScrollUp(1) {
	}

	// ScrollDown(1) three times should move the same distance as ScrollDown(3) once.
	for range 3 {
		d.ScrollDown(1)
	}

	// Count how many ScrollUp(1) calls to get back to the top.
	countAfterThreeSingles := 0
	for d.ScrollUp(1) {
		countAfterThreeSingles++
	}

	// Now test ScrollDown(3) — a single call should move the same distance.
	ok := d.ScrollDown(3)
	require.True(t, ok)

	countAfterOneBatch := 0
	for d.ScrollUp(1) {
		countAfterOneBatch++
	}
	assert.Equal(t, countAfterThreeSingles, countAfterOneBatch,
		"ScrollDown(3) should scroll the same distance as three ScrollDown(1) calls")
}

type dragStep struct {
	endY   int
	scroll int // >0 scrolls up N rows, <0 scrolls down N rows, 0 = no scroll
	// mutate models scroll sources the delegate does not drive itself
	// (streamed content via restoreScroll, keyboard SeekUp); it runs after
	// SetSelectionEnd so the anchor must survive the resulting scroll.
	mutate func(*Component)
}

type scrollSelectCase struct {
	desc          string
	cfg           ComponentConfig
	setup         func(*Component)
	width, height int
	preScroll     int
	// pressText is the message the click lands on; the harness resolves its
	// rendered row at press time so cases avoid bottom-aligned row math.
	pressText  string
	steps      []dragStep
	wantActive bool
}

// TestMouseDelegateSelectionStartPinnedAcrossAutoScroll asserts that the
// selection start stays pinned to the originally pressed content row while
// the message list scrolls under it from any source.
func TestMouseDelegateSelectionStartPinnedAcrossAutoScroll(t *testing.T) {
	const (
		defWidth  = 30
		defHeight = 10
	)

	manyMessages := func(prefix string, n int) func(*Component) {
		return func(c *Component) {
			for i := range n {
				c.AddSendMessage(prefix + " " + string(rune('A'+i)))
			}
		}
	}

	upwardDrag := func(scrolls int) []dragStep {
		steps := make([]dragStep, 0, scrolls+1)
		for range scrolls {
			steps = append(steps, dragStep{endY: 0, scroll: 1})
		}
		return append(steps, dragStep{endY: 0})
	}

	suite := []scrollSelectCase{
		{
			desc:       "single upward auto-scroll keeps pressed row",
			setup:      manyMessages("message", 12),
			preScroll:  1,
			pressText:  "message G",
			steps:      upwardDrag(2),
			wantActive: true,
		},
		{
			desc:       "no scroll leaves pressed row selected",
			setup:      manyMessages("message", 12),
			preScroll:  1,
			pressText:  "message G",
			steps:      []dragStep{{endY: 0}},
			wantActive: true,
		},
		{
			desc:      "drag down then auto-scroll back up returns to pressed row",
			setup:     manyMessages("message", 14),
			preScroll: 2,
			pressText: "message G",
			steps: append(
				[]dragStep{{endY: 4, scroll: -1}},
				upwardDrag(3)...,
			),
			wantActive: true,
		},
		{
			desc:       "auto-scroll past top of history clamps and still pins row",
			setup:      manyMessages("message", 12),
			preScroll:  1,
			pressText:  "message F",
			steps:      upwardDrag(8),
			wantActive: true,
		},
		{
			desc: "wrapped message stays selected across auto-scroll",
			setup: func(c *Component) {
				c.AddSendMessage("alpha")
				c.AddSendMessage("bravo")
				// Wider than the 20-col viewport: wraps onto two rows.
				c.AddSendMessage("this-message-is-long-enough-to-wrap")
				c.AddSendMessage("charlie")
				c.AddSendMessage("delta")
				c.AddSendMessage("echo")
				c.AddSendMessage("foxtrot")
				c.AddSendMessage("golf")
			},
			width:      20,
			height:     9,
			preScroll:  2,
			pressText:  "this-message-is-long",
			steps:      upwardDrag(2),
			wantActive: true,
		},
		{
			desc: "padded send messages pin row across auto-scroll",
			cfg:  ComponentConfig{SendMessageBottomPad: 1},
			setup: func(c *Component) {
				for i := range 10 {
					c.AddSendMessage("padded " + string(rune('A'+i)))
				}
			},
			preScroll:  2,
			pressText:  "padded G",
			steps:      upwardDrag(2),
			wantActive: true,
		},
		{
			desc:      "content streamed in mid-drag keeps pressed row pinned",
			setup:     manyMessages("message", 12),
			preScroll: 3,
			pressText: "message F",
			steps: []dragStep{
				{endY: 1, mutate: func(c *Component) { c.AddSendMessage("streamed one") }},
				{endY: 0, scroll: 1, mutate: func(c *Component) { c.AddSendMessage("streamed two") }},
				{endY: 0},
			},
			wantActive: true,
		},
		{
			desc:      "keyboard scroll mid-drag keeps pressed row pinned",
			setup:     manyMessages("message", 14),
			preScroll: 4,
			pressText: "message F",
			steps: []dragStep{
				{endY: 1, mutate: func(c *Component) { c.SeekUp() }},
				{endY: 0, mutate: func(c *Component) { c.SeekUp() }},
				{endY: 0},
			},
			wantActive: true,
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			width, height := tc.width, tc.height
			if width == 0 {
				width = defWidth
			}
			if height == 0 {
				height = defHeight
			}

			comp := NewComponent(tc.cfg)
			tc.setup(comp)
			comp.Resize(width, height)

			grid := drawGrid(comp, width, height)
			d := newMouseDelegate(grid, comp)

			redraw := func() {
				grid.Clear()
				comp.Draw(grid)
			}

			for i := range tc.preScroll {
				require.Truef(t, d.ScrollUp(1), "pre-scroll %d should move the list", i)
				redraw()
			}

			pressRow := gridRowOf(grid, tc.pressText)
			require.GreaterOrEqualf(t, pressRow, 0,
				"press text %q should be visible after pre-scroll", tc.pressText)

			// Press at the end of the row so an upward drag's reverse-ordered
			// selection spans the whole pressed row.
			press := term.Coordinates{X: len(tc.pressText) - 1, Y: pressRow}
			d.SetSelectionStart(press)
			for _, step := range tc.steps {
				d.SetSelectionEnd(term.Coordinates{Y: step.endY})
				if step.mutate != nil {
					step.mutate(comp)
				}
				switch {
				case step.scroll > 0:
					d.ScrollUp(step.scroll)
				case step.scroll < 0:
					d.ScrollDown(-step.scroll)
				}
				redraw()
			}

			data, ok := d.Selection()
			require.Equal(t, tc.wantActive, ok)
			if tc.wantActive {
				assert.Contains(t, data, tc.pressText,
					"selection must keep the originally pressed row after auto-scroll")
			}
		})
	}
}

// gridRowOf returns the first grid row whose rendered text equals want, or
// -1 when none match.
func gridRowOf(g *tterm.SelectionWriter, want string) int {
	for y := range g.Height() {
		row := g.TextBetween(
			term.Coordinates{X: 0, Y: y},
			term.Coordinates{X: g.Width() - 1, Y: y},
		)
		if row == want {
			return y
		}
	}
	return -1
}

// TestMouseDelegateSelectionCopiesOffscreenContent asserts that copying a
// selection returns the full selected text even when the selection's start or
// end rows have scrolled off the visible viewport. The grid backing the
// on-screen highlight only holds the visible viewport, so Selection must
// render the entire list into a private full-height grid to extract the
// off-screen rows.
func TestMouseDelegateSelectionCopiesOffscreenContent(t *testing.T) {
	manyMessages := func(prefix string, n int) func(*Component) {
		return func(c *Component) {
			for i := range n {
				c.AddSendMessage(prefix + " " + string(rune('A'+i)))
			}
		}
	}

	suite := []struct {
		desc          string
		cfg           ComponentConfig
		setup         func(*Component)
		width, height int
		// pressText/releaseText are resolved to rendered rows at the time of the
		// press and the release, respectively, so cases avoid hardcoding
		// bottom-aligned row math.
		pressText string
		// preScroll scrolls the list toward the start (older messages) before
		// the press so the pressed row is visible in the bottom-aligned
		// viewport.
		preScroll int
		// scrollUpBeforeRelease scrolls the list toward the start (older
		// messages) between the press and the release, pushing the pressed row
		// off the bottom of the viewport.
		scrollUpBeforeRelease int
		releaseText           string
		wantContains          []string
	}{
		{
			desc:                  "start scrolls off bottom before release",
			setup:                 manyMessages("message", 30),
			width:                 30,
			height:                8,
			pressText:             "message T",
			preScroll:             8,
			scrollUpBeforeRelease: 6,
			releaseText:           "message N",
			wantContains: []string{
				"message N", "message O", "message S", "message T",
			},
		},
		{
			desc:                  "selection spanning many off-screen rows",
			setup:                 manyMessages("message", 30),
			width:                 30,
			height:                6,
			pressText:             "message T",
			preScroll:             8,
			scrollUpBeforeRelease: 9,
			releaseText:           "message K",
			wantContains: []string{
				"message K", "message P", "message T",
			},
		},
		{
			desc: "wrapped message off-screen is fully copied",
			setup: func(c *Component) {
				c.AddSendMessage("alpha")
				c.AddSendMessage("this-message-is-long-enough-to-wrap-twice-over")
				for i := range 20 {
					c.AddSendMessage("tail " + string(rune('A'+i)))
				}
			},
			width:                 20,
			height:                6,
			pressText:             "alpha",
			preScroll:             40,
			scrollUpBeforeRelease: 0,
			releaseText:           "alpha",
			wantContains:          []string{"alpha"},
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			comp := NewComponent(tc.cfg)
			tc.setup(comp)
			comp.Resize(tc.width, tc.height)

			grid := drawGrid(comp, tc.width, tc.height)
			d := newMouseDelegate(grid, comp)

			redraw := func() {
				grid.Clear()
				comp.Draw(grid)
			}

			for range tc.preScroll {
				if !d.ScrollUp(1) {
					break
				}
				redraw()
			}

			pressRow := gridRowOf(grid, tc.pressText)
			require.GreaterOrEqualf(t, pressRow, 0,
				"press text %q should be visible after pre-scroll", tc.pressText)
			// Press at the end of the (newer, lower) line; it scrolls off the
			// bottom before release. Releasing at column 0 of the (older, upper)
			// line makes the sorted selection span both full lines.
			d.SetSelectionStart(term.Coordinates{X: len(tc.pressText) - 1, Y: pressRow})

			for i := range tc.scrollUpBeforeRelease {
				require.Truef(t, d.ScrollUp(1), "scroll %d should move the list", i)
				redraw()
			}

			releaseRow := gridRowOf(grid, tc.releaseText)
			require.GreaterOrEqualf(t, releaseRow, 0,
				"release text %q should be visible at release time", tc.releaseText)
			d.SetSelectionEnd(term.Coordinates{X: 0, Y: releaseRow})
			redraw()

			text, ok := d.Selection()
			require.True(t, ok)
			for _, want := range tc.wantContains {
				assert.Contains(t, text, want,
					"copied selection must include off-screen content")
			}
		})
	}
}

func TestMouseDelegateClearAndReselect(t *testing.T) {
	comp := NewComponent(ComponentConfig{})
	comp.AddSendMessage("Hello send")
	comp.Resize(30, 10)

	grid := drawGrid(comp, 30, 10)
	d := newMouseDelegate(grid, comp)

	// Select, then clear, then re-select.
	d.SetSelectionStart(term.Coordinates{X: 0, Y: 0})
	d.SetSelectionEnd(term.Coordinates{X: 4, Y: 0})

	text, ok := d.Selection()
	require.True(t, ok)
	require.Equal(t, "Hello", text)

	d.ClearSelection()
	text, ok = d.Selection()
	assert.False(t, ok)
	assert.Equal(t, "", text)

	// Re-select should work without issues.
	d.SetSelectionStart(term.Coordinates{X: 0, Y: 0})
	d.SetSelectionEnd(term.Coordinates{X: 4, Y: 0})

	text, ok = d.Selection()
	assert.True(t, ok)
	assert.Equal(t, "Hello", text)
}

// TestMouseDelegateSelectionEndPinnedAcrossScroll asserts that once a selection
// end is set, scrolling the list (without moving the pointer) keeps both
// endpoints anchored to their content rows. The end must not follow the scroll
// offset; the copied text must stay identical across the scroll.
func TestMouseDelegateSelectionEndPinnedAcrossScroll(t *testing.T) {
	const (
		width  = 30
		height = 8
	)

	suite := []struct {
		desc string
		// scrollAfter is applied after SetSelectionEnd: >0 scrolls up (toward
		// older messages), <0 scrolls down.
		scrollAfter int
	}{
		{desc: "scroll up after selecting end", scrollAfter: 3},
		{desc: "scroll down after selecting end", scrollAfter: -3},
		{desc: "scroll up far enough to push both endpoints off", scrollAfter: 6},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{})
			for i := range 30 {
				comp.AddSendMessage("message " + string(rune('A'+i)))
			}
			comp.Resize(width, height)

			grid := drawGrid(comp, width, height)
			d := newMouseDelegate(grid, comp)

			redraw := func() {
				grid.Clear()
				comp.Draw(grid)
			}

			// Scroll up a bit so a span of older messages is visible, then
			// select two adjacent full lines.
			for range 5 {
				require.True(t, d.ScrollUp(1))
				redraw()
			}

			startRow := gridRowOf(grid, "message V")
			endRow := gridRowOf(grid, "message W")
			require.GreaterOrEqual(t, startRow, 0)
			require.GreaterOrEqual(t, endRow, 0)

			d.SetSelectionStart(term.Coordinates{X: 0, Y: startRow})
			d.SetSelectionEnd(term.Coordinates{X: len("message W") - 1, Y: endRow})
			redraw()

			before, ok := d.Selection()
			require.True(t, ok)
			require.Equal(t, "message V\nmessage W", before)

			// Scroll without issuing a new SetSelectionEnd. Both endpoints must
			// stay pinned to their content rows, so the copy is unchanged.
			switch {
			case tc.scrollAfter > 0:
				for range tc.scrollAfter {
					d.ScrollUp(1)
					redraw()
				}
			case tc.scrollAfter < 0:
				for range -tc.scrollAfter {
					d.ScrollDown(1)
					redraw()
				}
			}

			after, ok := d.Selection()
			require.True(t, ok)
			assert.Equal(t, before, after,
				"selection end must stay anchored to its content row across scroll")
		})
	}
}

// TestMouseDelegateSelectionNegativeCoords exercises drags whose pointer leaves
// the window into negative coordinates, which the terminal reports while the
// mouse is dragged above or to the left of the viewport. The selection must not
// panic and must resolve to sensible content. Events are driven through the
// real mouse.Mouse so the drag/auto-scroll path matches production.
func TestMouseDelegateSelectionNegativeCoords(t *testing.T) {
	const (
		width  = 30
		height = 8
	)

	suite := []struct {
		desc string
		// preScroll scrolls toward older messages before the press so the
		// pressed row sits inside the bottom-aligned viewport.
		preScroll int
		// pressText is resolved to its rendered row at press time.
		pressText string
		pressX    int
		// dragTo is the pointer position reported during the drag; negative
		// values model the pointer leaving the window.
		dragTo [2]int
		// wantContains lists substrings the copied selection must include.
		wantContains []string
		// wantNotContains lists substrings the selection must not include.
		wantNotContains []string
	}{
		{
			desc:         "drag above the top while scrolled selects off-screen content above",
			preScroll:    5,
			pressText:    "message O",
			pressX:       len("message O") - 1,
			dragTo:       [2]int{2, -12},
			wantContains: []string{"message A", "message H", "message O"},
		},
		{
			desc:            "drag to negative X stays on pressed row",
			preScroll:       5,
			pressText:       "message O",
			pressX:          len("message O") - 1,
			dragTo:          [2]int{-20, 0},
			wantContains:    []string{"message", "message N", "message O"},
			wantNotContains: []string{"message P"},
		},
		{
			desc:         "drag far above the top clamps to first message",
			preScroll:    5,
			pressText:    "message N",
			pressX:       len("message N") - 1,
			dragTo:       [2]int{0, -100},
			wantContains: []string{"message A", "message N"},
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			comp := NewComponent(ComponentConfig{})
			for i := range 20 {
				comp.AddSendMessage("message " + string(rune('A'+i)))
			}
			comp.Resize(width, height)

			grid := drawGrid(comp, width, height)
			d := newMouseDelegate(grid, comp)
			m := mouse.New(d)

			redraw := func() {
				grid.Clear()
				comp.Draw(grid)
			}
			send := func(x, y int, k term.Key) {
				m.Handle(term.Event{Type: term.EventMouse, Key: k, MouseX: x, MouseY: y})
				redraw()
			}

			for range tc.preScroll {
				require.True(t, d.ScrollUp(1))
				redraw()
			}

			pressRow := gridRowOf(grid, tc.pressText)
			require.GreaterOrEqualf(t, pressRow, 0,
				"press text %q must be visible after pre-scroll", tc.pressText)

			require.NotPanics(t, func() {
				send(tc.pressX, pressRow, term.MouseLeft)
				// Repeat the off-window drag so the edge auto-scroll runs to
				// completion, as it would while the button is held.
				for range height + tc.preScroll + 2 {
					send(tc.dragTo[0], tc.dragTo[1], term.MouseLeft)
				}
				send(tc.dragTo[0], tc.dragTo[1], term.MouseRelease)
			})

			text, ok := d.Selection()
			require.True(t, ok)
			for _, want := range tc.wantContains {
				assert.Containsf(t, text, want,
					"selection must include %q", want)
			}
			for _, notWant := range tc.wantNotContains {
				assert.NotContainsf(t, text, notWant,
					"selection must not include %q", notWant)
			}
		})
	}
}

func TestMouseDelegate_LinkClick(t *testing.T) {
	const (
		width  = 50
		height = 10
	)

	t.Run("sent message markdown link invokes callback and suppresses selection", func(t *testing.T) {
		var clickedURL *url.URL
		comp := NewComponent(ComponentConfig{
			OnLinkClick: func(u *url.URL) bool {
				clickedURL = u
				return true
			},
		})
		comp.AddSendMessageMarkdown("Visit [Rune](https://github.com/unstablebuild/rune)")
		comp.Resize(width, height)

		grid := drawGrid(comp, width, height)
		d := newMouseDelegate(grid, comp)
		m := mouse.New(d)

		row := gridRowOf(grid, "Visit Rune")
		require.GreaterOrEqual(t, row, 0)

		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 7, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 7, MouseY: row})

		require.NotNil(t, clickedURL)
		assert.Equal(t, "https://github.com/unstablebuild/rune", clickedURL.String())
		_, ok := d.Selection()
		assert.False(t, ok, "selection must be suppressed when link click is handled")
	})

	t.Run("received message markdown link invokes callback and suppresses selection", func(t *testing.T) {
		var clickedURL *url.URL
		comp := NewComponent(ComponentConfig{
			OnLinkClick: func(u *url.URL) bool {
				clickedURL = u
				return true
			},
		})
		comp.AddReceiveMessage("Visit [Rune](https://github.com/unstablebuild/rune)")
		comp.Resize(width, height)

		grid := drawGrid(comp, width, height)
		d := newMouseDelegate(grid, comp)
		m := mouse.New(d)

		row := gridRowOf(grid, "Visit Rune")
		require.GreaterOrEqual(t, row, 0)

		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 7, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 7, MouseY: row})

		require.NotNil(t, clickedURL)
		assert.Equal(t, "https://github.com/unstablebuild/rune", clickedURL.String())
		_, ok := d.Selection()
		assert.False(t, ok, "selection must be suppressed when link click is handled")
	})

	t.Run("prompt body markdown link invokes callback and suppresses selection", func(t *testing.T) {
		var clickedURL *url.URL
		comp := NewComponent(ComponentConfig{
			OnLinkClick: func(u *url.URL) bool {
				clickedURL = u
				return true
			},
		})
		comp.AddPrompt("Pick", "Header", "See [Docs](https://docs.rune.build)", nil, false, nil)
		comp.Resize(width, height)

		grid := drawGrid(comp, width, height)
		d := newMouseDelegate(grid, comp)
		m := mouse.New(d)

		row := gridRowOf(grid, "See Docs")
		require.GreaterOrEqual(t, row, 0)

		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 5, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 5, MouseY: row})

		require.NotNil(t, clickedURL)
		assert.Equal(t, "https://docs.rune.build", clickedURL.String())
		_, ok := d.Selection()
		assert.False(t, ok, "selection must be suppressed when link click is handled")
	})

	t.Run("non-link text click falls through to text selection", func(t *testing.T) {
		var clickedURL *url.URL
		comp := NewComponent(ComponentConfig{
			OnLinkClick: func(u *url.URL) bool {
				clickedURL = u
				return true
			},
		})
		comp.AddSendMessageMarkdown("Visit [Rune](https://github.com/unstablebuild/rune)")
		comp.Resize(width, height)

		grid := drawGrid(comp, width, height)
		d := newMouseDelegate(grid, comp)
		m := mouse.New(d)

		row := gridRowOf(grid, "Visit Rune")
		require.GreaterOrEqual(t, row, 0)

		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 4, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 4, MouseY: row})

		assert.Nil(t, clickedURL, "callback must not be invoked for non-link text")
		sel, ok := d.Selection()
		assert.True(t, ok, "text selection must activate for non-link text")
		assert.Equal(t, "Visit", sel)
	})

	t.Run("unhandled bare anchor falls through to text selection", func(t *testing.T) {
		var clickedURL *url.URL
		_ = clickedURL
		comp := NewComponent(ComponentConfig{
			OnLinkClick: func(u *url.URL) bool {
				clickedURL = u
				return u.Scheme == "http" || u.Scheme == "https"
			},
		})
		comp.AddSendMessageMarkdown("[Section](#heading)")
		comp.Resize(width, height)

		grid := drawGrid(comp, width, height)
		d := newMouseDelegate(grid, comp)
		m := mouse.New(d)

		row := gridRowOf(grid, "Section")
		require.GreaterOrEqual(t, row, 0)

		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 0, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 6, MouseY: row})

		sel, ok := d.Selection()
		assert.True(t, ok, "text selection must activate when link callback returns false")
		assert.Equal(t, "Section", sel)
	})

	t.Run("nil callback allows all clicks to fall through to text selection", func(t *testing.T) {
		comp := NewComponent(ComponentConfig{
			OnLinkClick: nil,
		})
		comp.AddSendMessageMarkdown("Visit [Rune](https://github.com/unstablebuild/rune)")
		comp.Resize(width, height)

		grid := drawGrid(comp, width, height)
		d := newMouseDelegate(grid, comp)
		m := mouse.New(d)

		row := gridRowOf(grid, "Visit Rune")
		require.GreaterOrEqual(t, row, 0)

		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 6, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 9, MouseY: row})
		m.Handle(term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 9, MouseY: row})

		sel, ok := d.Selection()
		assert.True(t, ok, "text selection must activate when callback is nil")
		assert.Equal(t, "Rune", sel)
	})
}
