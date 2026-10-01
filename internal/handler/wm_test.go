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

package handler

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	compapi "github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/handler/handlertest"
)

func altEvent(ch rune) term.Event {
	return term.Event{
		Type: term.EventKey,
		Mod:  term.ModAlt,
		Ch:   ch,
	}
}

func prepareTest(width, height int, frame bool, root tui.Handler) (
	*term.StringWriter, *WindowManager,
) {
	writer := term.NewStringWriter(width, height)
	cfg := DefaultWindowManagerConfig()
	cfg.Frame = frame
	cfg.ScrollBarChar = '|'
	cfg.ScrollBarHoverChar = 'X'
	cfg.WindowBar = false
	handler := NewWindowManager(root, cfg)
	handler.Resize(width, height)

	return writer, handler
}

func TestWindowManagerSetFocusFrame(t *testing.T) {
	testWindowManagerSetFocus(t, true)
}

type recordingWMWriter struct {
	cell term.Cell
}

func (w *recordingWMWriter) SetCell(_ term.Coordinates, c term.Cell)           { w.cell = c }
func (w *recordingWMWriter) UnionAttributes(term.Coordinates, term.Attributes) {}
func (w *recordingWMWriter) Context() context.Context                          { return context.Background() }
func (w *recordingWMWriter) DrawImage(term.Image) bool                         { return false }

func TestWindowManagerDimmedWriter(t *testing.T) {
	red := term.NewRGBColor(255, 0, 0)

	t.Run("dim uses brightness dimming", func(t *testing.T) {
		wm := &WindowManager{config: WindowManagerConfig{BW: false}}
		rec := &recordingWMWriter{}
		wm.dimmedWriter(rec).SetCell(term.Coordinates{}, term.NewCell('x', 0,
			term.Attributes{Fg: red}))
		assert.Equal(t, red, rec.cell.Fg)
		assert.NotZero(t, rec.cell.Attrs&term.AttrDim)
	})

	t.Run("bw grayscales colors", func(t *testing.T) {
		wm := &WindowManager{config: WindowManagerConfig{BW: true}}
		rec := &recordingWMWriter{}
		wm.dimmedWriter(rec).SetCell(term.Coordinates{}, term.NewCell('x', 0,
			term.Attributes{Fg: red}))
		r, g, b := rec.cell.Fg.RGB()
		assert.Equal(t, r, g)
		assert.Equal(t, g, b)
		assert.Zero(t, rec.cell.Attrs&term.AttrDim)
	})

	t.Run("bw resolves default foreground to defAttr", func(t *testing.T) {
		tan := term.NewRGBColor(210, 180, 140)
		wm := &WindowManager{
			config:  WindowManagerConfig{BW: true},
			defAttr: term.Attributes{Fg: tan},
		}
		rec := &recordingWMWriter{}
		wm.dimmedWriter(rec).SetCell(term.Coordinates{}, term.NewCell('x', 0,
			term.Attributes{Fg: term.ColorDefault}))
		tr, tg, tb := tan.RGB()
		lum := int32(math.Round(0.299*float64(tr) + 0.587*float64(tg) + 0.114*float64(tb)))
		assert.Equal(t, term.NewRGBColor(lum, lum, lum), rec.cell.Fg)
	})
}
func TestWindowManagerSetFocusNoFrame(t *testing.T) {
	testWindowManagerSetFocus(t, false)
}

func testWindowManagerSetFocus(t *testing.T, frame bool) {
	width, height := 8, 4
	_, wm := prepareTest(width, height, frame, handler.NewTestHandler())
	right, ok := wm.SplitHorizontal(wm.Focus(), handler.NewTestHandler())
	require.True(t, ok)

	assert.False(t, right.Focus())
	wm.SetFocus(right)
	assert.True(t, right.Focus())

	if focus := wm.Focus(); focus != right {
		t.Errorf("focus should be %+v, instead of %+v", right, focus)
	}
	_, ok = wm.Focus().Content().(*handler.TestHandler)
	assert.True(t, ok)
}

// TestHandler signals that it's handling event by incrementing it's fill rune
func TestWindowManagerHandle(t *testing.T) {
	t.Run("passes correct mouse position", func(t *testing.T) {
		handler := handler.NewTestHandler()

		var actualEv term.Event
		handler.HandleOverride = func(ev term.Event) (bool, bool) {
			actualEv = ev
			return false, false
		}
		width, height := 8, 4
		cfg := DefaultWindowManagerConfig()
		cfg.Frame = true
		wm := NewWindowManager(handler, cfg)
		wm.Resize(width, height)

		wm.Handle(term.Event{Type: term.EventMouse})
		require.Equal(t, term.EventMouse, actualEv.Type)
		assert.Equal(t, 0, actualEv.MouseX)
		assert.Equal(t, 0, actualEv.MouseY)

		actualEv = term.Event{}
		wm.Handle(term.Event{Type: term.EventMouse, MouseX: 1, MouseY: 1})
		require.Equal(t, term.EventMouse, actualEv.Type)
		assert.Equal(t, 0, actualEv.MouseX)
		assert.Equal(t, 0, actualEv.MouseY)

		actualEv = term.Event{}
		wm.Handle(term.Event{Type: term.EventMouse, MouseX: 2, MouseY: 2})
		require.Equal(t, term.EventMouse, actualEv.Type)
		assert.Equal(t, 1, actualEv.MouseX)
		assert.Equal(t, 1, actualEv.MouseY)
	})
}

func TestWindowManagerHandleFrame(t *testing.T) {
	leftHandler := handler.NewTestHandler()
	width, height := 12, 4
	writer, h := prepareTest(width, height, true, leftHandler)

	rightHandler := handler.NewTestHandler()
	_, ok := h.SplitVertical(h.Focus(), rightHandler)
	require.True(t, ok)

	cases := []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌────┐┌────┐
│AAAA││BBBB│
│AAAA││BBBB│
└────┘└────┘`,
		},
	}

	require.True(t, h.FocusRight())

	handlertest.TestHandler(t, h, cases, writer)

	cases = []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌────┐┌────┐
│AAAA││CCCC│
│AAAA││CCCC│
└────┘└────┘`,
		},
	}

	handlertest.TestHandler(t, h, cases, writer)
}

func TestWindowFocusInitSplitVertical(t *testing.T) {
	leftHandler := handler.NewTestHandler()
	width, height := 12, 4
	_, m := prepareTest(width, height, true, leftHandler)

	_, ok := m.Focus().TileLeft()
	assert.False(t, ok)
	_, ok = m.Shiftable()
	assert.False(t, ok)
	assert.False(t, m.ShiftFocus())

	rightHandler := handler.NewTestHandler()
	_, ok = m.SplitVertical(m.Focus(), rightHandler)
	require.True(t, ok)

	assert.Equal(t, leftHandler, m.Focus().Content())
}

func TestSwapContent(t *testing.T) {
	t.Run("swaps content left", func(t *testing.T) {
		leftHandler := &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '1'}}
		width, height := 12, 4
		_, m := prepareTest(width, height, true, leftHandler)

		w1 := m.Focus()

		w2, ok := m.SplitVertical(w1,
			&handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '2'}})
		require.True(t, ok)

		m.SetFocus(w2)

		assert.True(t, m.SwapContentLeft())

		assert.Equal(t, '1', w2.Content().(*handler.TestHandler).TestComponent.Ch)
		assert.Equal(t, '2', w1.Content().(*handler.TestHandler).TestComponent.Ch)

		assert.True(t, m.SwapContentLeft())

		assert.Equal(t, '2', w2.Content().(*handler.TestHandler).TestComponent.Ch)
		assert.Equal(t, '1', w1.Content().(*handler.TestHandler).TestComponent.Ch)
	})

	t.Run("swaps content right", func(t *testing.T) {
		leftHandler := &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '1'}}
		width, height := 12, 4
		_, m := prepareTest(width, height, true, leftHandler)

		w1 := m.Focus()

		w2, ok := m.SplitVertical(w1,
			&handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '2'}})
		require.True(t, ok)

		assert.True(t, m.SwapContentRight())

		assert.Equal(t, '1', w2.Content().(*handler.TestHandler).TestComponent.Ch)
		assert.Equal(t, '2', w1.Content().(*handler.TestHandler).TestComponent.Ch)

		assert.True(t, m.SwapContentRight())

		assert.Equal(t, '2', w2.Content().(*handler.TestHandler).TestComponent.Ch)
		assert.Equal(t, '1', w1.Content().(*handler.TestHandler).TestComponent.Ch)
	})

	t.Run("swaps content down", func(t *testing.T) {
		leftHandler := &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '1'}}
		width, height := 12, 4
		_, m := prepareTest(width, height, true, leftHandler)

		w1 := m.Focus()

		w2, ok := m.SplitHorizontal(w1,
			&handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '2'}})
		require.True(t, ok)

		assert.True(t, m.SwapContentDown())

		assert.Equal(t, '1', w2.Content().(*handler.TestHandler).TestComponent.Ch)
		assert.Equal(t, '2', w1.Content().(*handler.TestHandler).TestComponent.Ch)

		assert.True(t, m.SwapContentDown())

		assert.Equal(t, '2', w2.Content().(*handler.TestHandler).TestComponent.Ch)
		assert.Equal(t, '1', w1.Content().(*handler.TestHandler).TestComponent.Ch)
	})

	t.Run("swaps content up", func(t *testing.T) {
		leftHandler := &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '1'}}
		width, height := 12, 4
		_, m := prepareTest(width, height, true, leftHandler)

		w1 := m.Focus()

		w2, ok := m.SplitHorizontal(w1,
			&handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '2'}})
		require.True(t, ok)

		m.SetFocus(w2)

		assert.True(t, m.SwapContentUp())

		assert.Equal(t, '1', w2.Content().(*handler.TestHandler).TestComponent.Ch)
		assert.Equal(t, '2', w1.Content().(*handler.TestHandler).TestComponent.Ch)

		assert.True(t, m.SwapContentUp())

		assert.Equal(t, '2', w2.Content().(*handler.TestHandler).TestComponent.Ch)
		assert.Equal(t, '1', w1.Content().(*handler.TestHandler).TestComponent.Ch)
	})

	t.Run("does not swap floating window content", func(t *testing.T) {
		leftHandler := &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '1'}}
		width, height := 12, 4
		_, m := prepareTest(width, height, true, leftHandler)

		w2 := m.FloatingWindow(handler.NewTestFloating(0, 0), component.FloatingConfig{})
		m.SetFocus(w2)

		assert.False(t, m.SwapContentUp())
		assert.False(t, m.SwapContentDown())
		assert.False(t, m.SwapContentLeft())
		assert.False(t, m.SwapContentRight())
	})

	t.Run("does not tile content into floating window", func(t *testing.T) {
		leftHandler := &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: '1'}}
		width, height := 12, 4
		_, m := prepareTest(width, height, true, leftHandler)

		_ = m.FloatingWindow(handler.NewTestFloating(0, 0), component.FloatingConfig{})

		assert.False(t, m.SwapContentUp())
		assert.False(t, m.SwapContentDown())
		assert.False(t, m.SwapContentLeft())
		assert.False(t, m.SwapContentRight())
	})
}

func TestWindowShiftFocusFocusPrev(t *testing.T) {
	leftHandler := handler.NewTestHandler()
	width, height := 12, 4
	_, m := prepareTest(width, height, true, leftHandler)
	orig := m.Focus()

	rightHandler := handler.NewTestHandler()
	_, ok := m.SplitVertical(m.Focus(), rightHandler)
	require.True(t, ok)

	_, ok = m.SplitVertical(orig, handler.NewTestHandler())
	require.True(t, ok)

	assert.True(t, m.ShiftFocus())
	assert.Equal(t, rightHandler, m.Focus().Content())
}

func TestWindowManagerSetFocusContent(t *testing.T) {
	leftHandler := handler.NewTestHandler()
	width, height := 8, 4
	writer, wm := prepareTest(width, height, true, leftHandler)

	rightHandler := handler.NewTestHandler()
	_, ok := wm.SplitVertical(wm.Focus(), rightHandler)
	require.True(t, ok)

	prev := wm.Focus().SetContent(rightHandler)
	assert.Equal(t, prev, leftHandler)

	cases := []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌──┐┌──┐
│BB││BB│
│BB││BB│
└──┘└──┘`,
		},
	}

	require.True(t, wm.FocusRight())

	handlertest.TestHandler(t, wm, cases, writer)

	fb := compapi.FrameCharSet{}
	fb.TopLeft = '╔'
	fb.BottomRight = '╝'
	fb.BottomLeft = '╚'
	fb.TopRight = '╗'

	fb.VerticalLeft = '║'
	fb.VerticalRight = '║'
	fb.HorizontalTop = '═'
	fb.HorizontalBottom = '═'

	wm.SetFrameCharSet(compapi.FrameCharSetDefault(), fb)

	cases = []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌──┐╔══╗
│CC│║CC║
│CC│║CC║
└──┘╚══╝`,
		},
	}

	handlertest.TestHandler(t, wm, cases, writer)
}

// TestWindowManagerMouseDrag exercises WindowManager.Handle mouse
// routing across drag-capture, focus-switch, and non-drag paths.
func TestWindowManagerMouseDrag(t *testing.T) {
	cases := []dragCase{
		{
			desc:   "single window: press then drag stays in window",
			layout: singleLayout("A"),
			frame:  true,
			width:  24, height: 8,
			events: []mouseEvent{
				press(5, 3),
				drag(7, 4),
				release(7, 4),
			},
			expect: []dispatch{
				to("A", term.MouseLeft, 4, 2),
				to("A", term.MouseLeft, 6, 3),
				to("A", term.MouseRelease, 6, 3),
			},
		},
		{
			desc:   "single window no frame: coords pass through unchanged",
			layout: singleLayout("A"),
			frame:  false,
			width:  24, height: 8,
			events: []mouseEvent{
				press(0, 0),
				drag(23, 7),
				release(23, 7),
			},
			expect: []dispatch{
				to("A", term.MouseLeft, 0, 0),
				to("A", term.MouseLeft, 23, 7),
				to("A", term.MouseRelease, 23, 7),
			},
		},

		{
			desc:   "press in focused left pane stays local",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(2, 2),
				release(2, 2),
			},
			expect: []dispatch{
				to("L", term.MouseLeft, 1, 1),
				to("L", term.MouseRelease, 1, 1),
			},
		},
		{
			desc:   "press at first content cell of focused left pane reaches (0,0)",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(1, 1),
			},
			expect: []dispatch{
				to("L", term.MouseLeft, 0, 0),
			},
		},
		{
			desc:   "drag from right pane into left pane stays captured by right",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "R",
			events: []mouseEvent{
				press(15, 2),
				drag(3, 2), // into left pane
				drag(0, 0),
				release(0, 0),
			},
			expect: []dispatch{
				to("R", term.MouseLeft, 2, 1),
				to("R", term.MouseLeft, 0, 1), // clamped to R's left edge
				to("R", term.MouseLeft, 0, 0),
				to("R", term.MouseRelease, 0, 0),
			},
		},
		{
			desc:   "drag from left pane into right pane stays captured by left",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(2, 2),
				drag(20, 5), // into right pane
				release(20, 5),
			},
			expect: []dispatch{
				to("L", term.MouseLeft, 1, 1),
				to("L", term.MouseLeft, 9, 4), // clamped to L's bottom-right
				to("L", term.MouseRelease, 9, 4),
			},
		},
		{
			desc:   "drag past screen edges clamps to content bounds",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(2, 2),
				drag(0, 0),
				drag(11, 7),
				release(11, 7),
			},
			expect: []dispatch{
				to("L", term.MouseLeft, 1, 1),
				to("L", term.MouseLeft, 0, 0),
				to("L", term.MouseLeft, 9, 5),
				to("L", term.MouseRelease, 9, 5),
			},
		},
		{
			desc:   "release that ends drag reaches press window even when over sibling",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(2, 2),
				drag(15, 2),
				release(15, 2),
			},
			expect: []dispatch{
				to("L", term.MouseLeft, 1, 1),
				to("L", term.MouseLeft, 9, 1),
				to("L", term.MouseRelease, 9, 1),
			},
		},

		{
			desc:   "press on unfocused window switches focus AND dispatches the press",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(15, 2),
				release(15, 2),
			},
			expect: []dispatch{
				to("R", term.MouseLeft, 2, 1),
				to("R", term.MouseRelease, 2, 1),
			},
		},
		{
			desc:   "press at first content cell of unfocused right pane reaches (0,0)",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(13, 1),
			},
			expect: []dispatch{
				to("R", term.MouseLeft, 0, 0),
			},
		},

		{
			desc:   "hover over unfocused window reaches it without taking focus",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				hover(15, 2),
			},
			expect: []dispatch{
				to("R", 0, 2, 1),
			},
			expectFocus: "L",
		},
		{
			desc:   "wheel event in focused window dispatches locally",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				{key: term.MouseWheelUp, x: 5, y: 3},
			},
			expect: []dispatch{
				to("L", term.MouseWheelUp, 4, 2),
			},
		},
		{
			desc:   "wheel over unfocused window scrolls it without taking focus",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				{key: term.MouseWheelDown, x: 15, y: 3},
			},
			expect: []dispatch{
				to("R", term.MouseWheelDown, 2, 2),
			},
			expectFocus: "L",
		},
		{
			desc:   "release over unfocused window without a drag is dropped",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				release(15, 2),
			},
			expect: []dispatch{
				dropped(),
			},
			expectFocus: "L",
		},

		{
			desc:   "press, release in place, then press in sibling: each press is its own gesture",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(2, 2),
				release(2, 2),
				press(15, 2),
				release(15, 2),
			},
			expect: []dispatch{
				to("L", term.MouseLeft, 1, 1),
				to("L", term.MouseRelease, 1, 1),
				to("R", term.MouseLeft, 2, 1),
				to("R", term.MouseRelease, 2, 1),
			},
		},
		{
			desc:   "second press without release in between starts a fresh drag in the new window",
			layout: vsplitLayout("L", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "L",
			events: []mouseEvent{
				press(2, 2),
				drag(15, 2),
				release(15, 2),
				press(20, 4),
			},
			expect: []dispatch{
				to("L", term.MouseLeft, 1, 1),
				to("L", term.MouseLeft, 9, 1),
				to("L", term.MouseRelease, 9, 1),
				to("R", term.MouseLeft, 7, 3),
			},
		},

		{
			desc:   "three vsplits: drag from rightmost across both siblings stays captured",
			layout: threeVsplitLayout("L", "M", "R"),
			frame:  true,
			width:  24, height: 8,
			initial: "R",
			events: []mouseEvent{
				press(20, 2),
				drag(14, 2),
				drag(2, 2),
				release(2, 2),
			},
			expect: []dispatch{
				// localX reflects R's empirical position after two vsplits.
				to("R", term.MouseLeft, 3, 1),
				to("R", term.MouseLeft, 0, 1),
				to("R", term.MouseLeft, 0, 1),
				to("R", term.MouseRelease, 0, 1),
			},
		},

		{
			desc:   "hsplit: drag from top into bottom stays captured by top",
			layout: hsplitLayout("T", "B"),
			frame:  true,
			width:  24, height: 8,
			initial: "T",
			events: []mouseEvent{
				press(5, 1),
				drag(5, 6),
				release(5, 6),
			},
			expect: []dispatch{
				to("T", term.MouseLeft, 4, 0),
				to("T", term.MouseLeft, 4, 1), // clamped to T's bottom
				to("T", term.MouseRelease, 4, 1),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			runDragCase(t, tc)
		})
	}
}

// TestWindowManagerMouseDragSurvivesPinnedWindowClose reproduces a nil
// pointer dereference: a MouseLeft press pins the pressed window as the
// drag target, that window is then closed (e.g. programmatically by a
// runner), and a follow-up drag event re-routes to the now-removed
// window. Calling Position on a removed tile dereferenced a nil tree.
func TestWindowManagerMouseDragSurvivesPinnedWindowClose(t *testing.T) {
	width, height := 24, 8
	lh := handler.NewTestHandler()
	rh := handler.NewTestHandler()
	_, wm := prepareTest(width, height, true, lh)
	lw := wm.Focus()
	rw, ok := wm.SplitVertical(lw, rh)
	require.True(t, ok)
	wm.SetFocus(rw)

	// Press inside the right pane to pin it as the drag target.
	wm.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 15, MouseY: 2})

	// Close the pinned window out from under the in-progress drag.
	require.NoError(t, rw.Close())

	// A follow-up drag must not panic on the stale, removed window.
	require.NotPanics(t, func() {
		wm.Handle(term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 3, MouseY: 2})
	})
}

// TestWindowManagerMouseUnderFocusedFloat pins that a focused floating
// window keeps the wheel and pointer motion from reaching the tiles it
// floats over, which would move content out from under it.
func TestWindowManagerMouseUnderFocusedFloat(t *testing.T) {
	lh := handler.NewTestHandler()
	rh := handler.NewTestHandler()
	_, wm := prepareTest(24, 8, true, lh)
	_, ok := wm.SplitVertical(wm.Focus(), rh)
	require.True(t, ok)
	fh := handler.NewTestHandler()
	wm.SetFocus(wm.FloatingWindow(handler.StaticFloating(fh, 2, 2), component.FloatingConfig{}))

	var seen []string
	for name, h := range map[string]*handler.TestHandler{"L": lh, "R": rh, "F": fh} {
		h.HandleOverride = func(ev term.Event) (bool, bool) {
			seen = append(seen, name)
			return false, true
		}
	}

	for _, key := range []term.Key{0, term.MouseWheelUp} {
		wm.Handle(term.Event{Type: term.EventMouse, Key: key, MouseX: 15, MouseY: 3})
	}
	assert.Empty(t, seen, "tiles under a focused float must not see the wheel or hover")

	wm.Handle(term.Event{Type: term.EventMouse, Key: term.MouseWheelUp, MouseX: 1, MouseY: 1})
	assert.Equal(t, []string{"F"}, seen)
}

// TestWindowManagerMouseExitFromUnfocusedWindow pins that a tile asking
// to exit on a wheel event it received without focus is closed, and
// that focus stays on the tile the user was working in.
func TestWindowManagerMouseExitFromUnfocusedWindow(t *testing.T) {
	lh := handler.NewTestHandler()
	rh := handler.NewTestHandler()
	_, wm := prepareTest(24, 8, true, lh)
	lw := wm.Focus()
	rw, ok := wm.SplitVertical(lw, rh)
	require.True(t, ok)
	wm.SetFocus(lw)
	rh.HandleOverride = func(term.Event) (bool, bool) { return true, true }

	exit, handled := wm.Handle(term.Event{
		Type: term.EventMouse, Key: term.MouseWheelDown, MouseX: 15, MouseY: 3,
	})
	assert.False(t, exit)
	assert.True(t, handled)
	assert.True(t, rw.Closed())
	assert.Equal(t, lw, wm.Focus())
}

func TestWindowManagerInit(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	cfg.Frame = false
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	require.NotNil(t, wm.Focus())
}

func TestWindowManagerSetAttr(t *testing.T) {
	wm := NewWindowManager(handler.NewTestHandler(), DefaultWindowManagerConfig())
	cyan := term.ColorNavy
	red := term.ColorRed

	wm.SplitHorizontal(wm.Focus(), handler.NewTestHandler())
	wm.SetAttr(term.Attributes{Bg: cyan, Fg: red}, term.Attributes{Bg: red, Fg: cyan})

	w1 := wm.Focus()
	b, ok := w1.FrameAttr()
	require.True(t, ok)
	assert.Equal(t, red, b.Bg)
	assert.Equal(t, cyan, b.Fg)

	w1.SetContent(handler.NewTestHandler())
	b, ok = w1.FrameAttr()
	require.True(t, ok)
	assert.Equal(t, red, b.Bg)
	assert.Equal(t, cyan, b.Fg)

	wm.ShiftFocus()
	b, ok = w1.FrameAttr()
	require.True(t, ok)
	assert.Equal(t, cyan, b.Bg)
	assert.Equal(t, red, b.Fg)

	w1.SetContent(handler.NewTestHandler())
	b, ok = w1.FrameAttr()
	require.True(t, ok)
	assert.Equal(t, cyan, b.Bg)
	assert.Equal(t, red, b.Fg)
}

func testWindowManagerClose(
	t *testing.T,
	frame bool,
	split func(*WindowManager, Window, tui.Handler) (Window, bool),
) {
	h1 := handler.NewTestHandler()
	h1.Ch = 'C'
	h2 := handler.NewTestHandler()
	h2.Ch = 'D'
	cfg := DefaultWindowManagerConfig()
	cfg.Frame = frame
	wm := NewWindowManager(h1, cfg)
	node2, ok := split(wm, wm.Focus(), h2)
	require.True(t, ok)

	assert.NotEqual(t, node2, wm.Focus())
	require.NoError(t, wm.Focus().Close())
	assert.Equal(t, node2, wm.Focus())
}

func testWindowManagerCloseLast(
	t *testing.T,
	frame bool,
	split func(*WindowManager, Window, tui.Handler) (Window, bool),
) {
	h1 := handler.NewTestHandler()
	h1.Ch = 'C'
	h2 := handler.NewTestHandler()
	h2.Ch = 'D'
	cfg := DefaultWindowManagerConfig()
	cfg.Frame = frame
	wm := NewWindowManager(h1, cfg)
	node2, ok := split(wm, wm.Focus(), h2)
	require.True(t, ok)

	node1 := wm.Focus()
	assert.NotEqual(t, node2, node1)
	require.NotNil(t, wm.SetFocus(node2))
	assert.Equal(t, node2, wm.Focus())
	require.NoError(t, node2.Close())
	assert.Equal(t, node1, wm.Focus())
}

func TestWindowManagerClose(t *testing.T) {
	suite := []struct {
		description string
		split       func(*WindowManager, Window, tui.Handler) (Window, bool)
		frame       bool
	}{
		{"split horizontal with frame", (*WindowManager).SplitHorizontal, true},
		{"split horizontal without frame", (*WindowManager).SplitHorizontal, false},
		{"split vertical with frame", (*WindowManager).SplitVertical, true},
		{"split vertical without frame", (*WindowManager).SplitVertical, false},
	}

	for _, test := range suite {
		t.Run("Close "+test.description, func(t *testing.T) {
			testWindowManagerClose(t, test.frame, test.split)
			testWindowManagerCloseLast(t, test.frame, test.split)
		})
	}
}

func TestWindowManagerCloseFloatingFocusesFrontmostRemainingFloating(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	wm.Resize(20, 8)

	f1 := wm.FloatingWindow(
		handler.StaticFloating(handler.NewTestHandler(), 4, 4),
		component.FloatingConfig{Alignment: compapi.AlignmentCentered},
	)
	f2 := wm.FloatingWindow(
		handler.StaticFloating(handler.NewTestHandler(), 4, 4),
		component.FloatingConfig{Alignment: compapi.AlignmentCentered},
	)
	f3 := wm.FloatingWindow(
		handler.StaticFloating(handler.NewTestHandler(), 4, 4),
		component.FloatingConfig{Alignment: compapi.AlignmentCentered},
	)

	wm.SetFocus(f3)
	require.NoError(t, f3.Close())

	assert.Equal(t, f2.ID(), wm.Focus().ID())
	assert.NotEqual(t, f1.ID(), wm.Focus().ID())
}

func TestWindowManagerCloseFloatingSkipsMinimizedFloatingFocus(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	wm.Resize(20, 8)
	tile := wm.Focus()

	minimized := wm.FloatingWindow(
		handler.StaticFloating(handler.NewTestHandler(), 4, 4),
		component.FloatingConfig{Alignment: compapi.AlignmentCentered},
	)
	require.True(t, minimized.MinimizeRight(0))

	focus := wm.FloatingWindow(
		handler.StaticFloating(handler.NewTestHandler(), 4, 4),
		component.FloatingConfig{Alignment: compapi.AlignmentCentered},
	)
	wm.SetFocus(focus)
	require.NoError(t, focus.Close())

	assert.Equal(t, tile.ID(), wm.Focus().ID())
	_, ok := minimized.IsMinimized()
	assert.True(t, ok)
}

// TestWindowManagerCloseSkipsClosedPrevFocus exercises the case where, after
// successive window closes, prevFocus points to a window that is no longer
// alive. Close must not select such a stale window as the next focus, or
// WindowManager.Focus() will return a closed Window and downstream
// browser.Component.findWindow will fail to resolve it.
func TestWindowManagerCloseSkipsClosedPrevFocus(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	wm.Resize(20, 8)

	// a is the initial focus window.
	a := wm.Focus()
	b, ok := wm.SplitHorizontal(a, handler.NewTestHandler())
	require.True(t, ok)
	c, ok := wm.SplitHorizontal(b, handler.NewTestHandler())
	require.True(t, ok)
	// focus is still a.
	require.Equal(t, a.ID(), wm.Focus().ID())
	_ = c

	// Close the focused window a; focus shifts to a sibling (b or c). This
	// also sets prevFocus to the now-closed a.
	require.NoError(t, a.Close())
	require.NotEqual(t, a.ID(), wm.Focus().ID())
	after := wm.Focus()

	// Close the new focus. prevFocus still points to the closed a, so
	// Close should NOT select a as the next focus.
	require.NoError(t, after.Close())

	assert.False(t, wm.Focus().Closed(),
		"focus should point to a live window, not a closed one")
}

// TestWindowManagerRestoreTileLayoutResetsPrevFocus ensures that a layout
// restore does not leave prevFocus pointing at a closed window. Before this
// fix, SetFocus stored the stale pre-restore focus into prevFocus; once the
// user closed the new focus, Close.prevFocus fallback path restored the
// stale window, causing WindowManager.Focus() to return a closed Window and
// downstream browser.Component.findWindow to panic with
// "corrupted browser: cannot find focus window".
func TestWindowManagerRestoreTileLayoutResetsPrevFocus(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	wm.Resize(20, 8)

	// Replace the tree with a fresh two-window layout. The old focus
	// window is no longer part of the tree after RestoreTileLayout.
	restored := wm.RestoreTileLayout(component.TileLayout{
		Split: component.SplitOrientationVertical,
		Children: []component.TileLayout{
			{WindowID: 1},
			{WindowID: 2},
		},
	}, func(windowID uint64) tui.Handler {
		return handler.NewTestHandler()
	})
	require.Len(t, restored, 2)

	// Close the current focus window. prevFocus must not point at the
	// old, pre-restore window, which is closed and no longer in the tree.
	current := wm.Focus()
	require.False(t, current.Closed())
	require.NoError(t, current.Close())

	// Verify the current focus ID is actually present in the tree. We
	// cannot rely on Closed() alone because TileNode.Closed() is based on
	// parent != nil and RestoreTileLayout rewrites the root in place,
	// leaving the old focus' parent pointer dangling instead of nil.
	focusID := wm.Focus().ID()
	var found bool
	wm.Iterate(func(w Window) {
		if w.ID() == focusID {
			found = true
		}
	})
	assert.True(t, found,
		"WindowManager.Focus() must resolve to a window that is still in the tree")
}

// TestWindowManagerRestoreTileLayoutEmptyLayout exercises the case where
// RestoreTileLayout is invoked with an empty layout (a leaf layout
// with WindowID == 0). Without a fallback, the old pre-restore
// wm.focus would survive and point at a now-discarded node,
// causing Iterate to skip it and downstream callers (such as
// browser.Component.focus) to panic with "cannot find focus window".
func TestWindowManagerRestoreTileLayoutEmptyLayout(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	wm.Resize(20, 8)

	wm.RestoreTileLayout(component.TileLayout{}, func(windowID uint64) tui.Handler {
		return handler.NewTestHandler()
	})

	focusID := wm.Focus().ID()
	var found bool
	wm.Iterate(func(w Window) {
		if w.ID() == focusID {
			found = true
		}
	})
	assert.True(t, found,
		"WindowManager.Focus() must resolve to a window that is still in the tree")
}

func testWindowManagerContent(t *testing.T, frame bool) {
	cfg := DefaultWindowManagerConfig()
	cfg.Frame = frame
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	node2, ok := wm.SplitHorizontal(wm.Focus(), handler.NewTestHandler())
	require.True(t, ok)

	c := node2.Content()
	_, ok = c.(*handler.TestHandler)
	require.True(t, ok)

	prev := node2.SetContent(handler.NewTestHandler())
	_, ok = prev.(*handler.TestHandler)
	require.True(t, ok)

	c = node2.Content()
	_, ok = c.(*handler.TestHandler)
	require.True(t, ok)
}

func TestWindowManagerContent(t *testing.T) {
	t.Run("Content with frame", func(t *testing.T) {
		testWindowManagerContent(t, true)
	})
	t.Run("Content without frame", func(t *testing.T) {
		testWindowManagerContent(t, false)
	})
}

func testWindowManagerCursorShow(
	t *testing.T, frame bool,
	input, expected term.Coordinates,
) {
	handler := handler.NewTestHandler()
	handler.CursorPos = input
	cfg := DefaultWindowManagerConfig()
	cfg.Frame = frame
	wm := NewWindowManager(handler, cfg)
	wm.Resize(10, 10)

	pos, _, ok := wm.Cursor()
	require.True(t, ok)
	assert.Equal(t, expected, pos)
}

func testWindowManagerCursorHide(
	t *testing.T, frame bool,
	input term.Coordinates,
) {
	handler := handler.NewTestHandler()
	handler.CursorPos = input
	cfg := DefaultWindowManagerConfig()
	cfg.Frame = frame
	wm := NewWindowManager(handler, cfg)
	wm.Resize(10, 10)

	_, _, ok := wm.Cursor()
	require.False(t, ok)
}

func TestWindowManagerCursor(t *testing.T) {
	t.Run("Cursor with frame", func(t *testing.T) {
		input := term.Coordinates{X: 1, Y: 2}
		testWindowManagerCursorShow(t, true, input,
			term.Coordinates{X: 2, Y: 3})
	})

	t.Run("Cursor with frame at bounds - 1", func(t *testing.T) {
		testWindowManagerCursorShow(t, true, term.Coordinates{X: 7, Y: 7},
			term.Coordinates{X: 8, Y: 8})
	})

	t.Run("Cursor without frame", func(t *testing.T) {
		input := term.Coordinates{X: 1, Y: 2}
		testWindowManagerCursorShow(t, false, input, input)
	})

	t.Run("Cursor without frame at bounds - 1", func(t *testing.T) {
		testWindowManagerCursorShow(t, false, term.Coordinates{X: 9, Y: 9},
			term.Coordinates{X: 9, Y: 9})
	})

	t.Run("overrides show to off if out of bounds, with frame", func(t *testing.T) {
		testWindowManagerCursorHide(t, true, term.Coordinates{X: 8, Y: 8})
	})

	t.Run("overrides show to off if out of bounds, with frame", func(t *testing.T) {
		testWindowManagerCursorHide(t, false, term.Coordinates{X: 10, Y: 10})
	})

	t.Run("overrides show to off if negative out of bounds, with frame", func(t *testing.T) {
		testWindowManagerCursorHide(t, true, term.Coordinates{X: -1, Y: -1})
	})

	t.Run("overrides show to off if negative out of bounds, with frame", func(t *testing.T) {
		testWindowManagerCursorHide(t, false, term.Coordinates{X: -1, Y: -1})
	})
}

func TestHandlerWindowZeroValue(t *testing.T) {
	t.Run("Close", func(t *testing.T) {
		var win Window
		assert.NotPanics(t, func() {
			win.Close()
		})
	})
	t.Run("Content", func(t *testing.T) {
		var win Window
		assert.PanicsWithValue(t, errCalledZeroValuedWin, func() {
			_ = win.Content()
		})
	})
	t.Run("SetContent", func(t *testing.T) {
		var win Window
		assert.PanicsWithValue(t, errCalledZeroValuedWin, func() {
			win.SetContent(handler.NewTestHandler())
		})
	})
	t.Run("Size", func(t *testing.T) {
		var win Window
		assert.PanicsWithValue(t, errCalledZeroValuedWin, func() {
			win.Size()
		})
	})
	t.Run("TileDirection", func(t *testing.T) {
		var win Window
		assert.PanicsWithValue(t, errCalledZeroValuedWin, func() {
			win.TileDown()
		})
	})
}

type testWindowSubscriber struct {
	lastPrev  Window
	lastFocus Window
}

func (w *testWindowSubscriber) reset() {
	w.lastPrev = Window{}
	w.lastFocus = Window{}
}

func (w *testWindowSubscriber) OnFocus(prev, focus Window) {
	w.lastPrev = prev
	w.lastFocus = focus
}

func TestWindowManagerSubscribe(t *testing.T) {
	h1 := handler.NewTestHandler()
	h2 := handler.NewTestHandler()
	wm := NewWindowManager(h1, DefaultWindowManagerConfig())
	mock := new(testWindowSubscriber)

	w1 := wm.Focus()
	wm.Subscribe(mock)
	assert.Zero(t, mock.lastPrev)
	assert.Equal(t, w1.ID(), mock.lastFocus.ID())

	w2, ok := wm.SplitVertical(wm.Focus(), h2)
	require.True(t, ok)
	wm.SetFocus(w2)
	assert.Equal(t, w1.ID(), mock.lastPrev.ID())
	assert.Equal(t, w2.ID(), mock.lastFocus.ID())

	wm.SetFocus(w1)
	assert.Equal(t, w2.ID(), mock.lastPrev.ID())
	assert.Equal(t, w1.ID(), mock.lastFocus.ID())

	mock.reset()
	wm.SetFocus(w1)
	assert.Zero(t, mock.lastPrev)
	assert.Zero(t, mock.lastFocus)

	mock.reset()
	wm.UnsubscribeAll()
	wm.SetFocus(w1)
	assert.Zero(t, mock.lastPrev)
	assert.Zero(t, mock.lastFocus)
}

func TestWindowManagerRestoreTileLayout(t *testing.T) {
	tests := []struct {
		name        string
		frame       bool
		layout      component.TileLayout
		content     map[uint64]rune
		wantMapped  []uint64
		assertExtra func(*testing.T, *WindowManager, map[uint64]Window, *testWindowSubscriber)
	}{
		{
			name:       "single leaf",
			layout:     component.TileLayout{WindowID: 1},
			content:    map[uint64]rune{1: 'A'},
			wantMapped: []uint64{1},
		},
		{
			name:       "single leaf framed",
			frame:      true,
			layout:     component.TileLayout{WindowID: 1},
			content:    map[uint64]rune{1: 'B'},
			wantMapped: []uint64{1},
		},
		{
			name: "deep mixed layout",
			layout: component.TileLayout{
				Split: component.SplitOrientationVertical,
				Children: []component.TileLayout{
					{WindowID: 1},
					{
						Split: component.SplitOrientationHorizontal,
						Children: []component.TileLayout{
							{WindowID: 2},
							{WindowID: 3},
							{
								Split:    component.SplitOrientationVertical,
								Children: []component.TileLayout{{WindowID: 4}, {WindowID: 5}},
							},
						},
					},
				},
			},
			content:    map[uint64]rune{1: 'A', 2: 'B', 3: 'C', 4: 'D', 5: 'E'},
			wantMapped: []uint64{1, 2, 3, 4, 5},
		},
		{
			name: "nil content fallback",
			layout: component.TileLayout{
				Split:    component.SplitOrientationHorizontal,
				Children: []component.TileLayout{{WindowID: 1}, {WindowID: 2}},
			},
			content:    map[uint64]rune{2: 'Z'},
			wantMapped: []uint64{1, 2},
			assertExtra: func(t *testing.T, wm *WindowManager, restored map[uint64]Window, sub *testWindowSubscriber) {
				assert.NotPanics(t, func() {
					writer := term.NewStringWriter(20, 8)
					wm.Draw(writer)
				})
				require.IsType(t, &handler.TestHandler{}, restored[2].Content())
			},
		},
		{
			name: "restore sets focus and notifies subscribers",
			layout: component.TileLayout{
				Split:    component.SplitOrientationVertical,
				Children: []component.TileLayout{{WindowID: 1}, {WindowID: 2}},
			},
			content:    map[uint64]rune{1: 'L', 2: 'R'},
			wantMapped: []uint64{1, 2},
			assertExtra: func(t *testing.T, wm *WindowManager, restored map[uint64]Window, sub *testWindowSubscriber) {
				require.Contains(t, restored, uint64(1))
				require.Equal(t, restored[uint64(1)].ID(), wm.Focus().ID())
				require.Equal(t, restored[uint64(1)].ID(), sub.lastFocus.ID())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultWindowManagerConfig()
			cfg.Frame = tt.frame
			wm := NewWindowManager(handler.NewTestHandler(), cfg)
			wm.Resize(20, 8)
			sub := new(testWindowSubscriber)
			wm.Subscribe(sub)
			sub.reset()

			restored := wm.RestoreTileLayout(tt.layout, func(windowID uint64) tui.Handler {
				ch, ok := tt.content[windowID]
				if !ok {
					return nil
				}
				return &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: ch}}
			})

			require.Len(t, restored, len(tt.wantMapped))
			for _, id := range tt.wantMapped {
				require.Contains(t, restored, id)
				require.NotEqual(t, id, restored[id].ID())
				if ch, ok := tt.content[id]; ok {
					assertHandlerWindowTile(t, restored[id], ch)
				}
			}
			assertHandlerWindowManagerLayoutShape(t, tt.layout, wm.TileLayout(), restored)
			if tt.assertExtra != nil {
				tt.assertExtra(t, wm, restored, sub)
			}
		})
	}
}

func assertHandlerWindowTile(t *testing.T, win Window, expected rune) {
	t.Helper()
	h, ok := win.Content().(*handler.TestHandler)
	require.True(t, ok)
	require.Equal(t, string(expected), string(h.Ch))
}

func assertHandlerWindowManagerLayoutShape(
	t *testing.T,
	want component.TileLayout,
	got component.TileLayout,
	restored map[uint64]Window,
) {
	t.Helper()
	require.Equal(t, want.Split, got.Split)
	require.Len(t, got.Children, len(want.Children))
	if len(want.Children) == 0 {
		if want.WindowID == 0 {
			require.Equal(t, uint64(0), got.WindowID)
			return
		}
		require.Equal(t, restored[want.WindowID].ID(), got.WindowID)
		return
	}
	for i := range want.Children {
		assertHandlerWindowManagerLayoutShape(t, want.Children[i], got.Children[i], restored)
	}
}

func TestWindowManagerSplit(t *testing.T) {
	w := term.NewStringWriter(20, 8)

	h1 := handler.TestHandler{TestComponent: compapi.TestComponent{Ch: 'A'}}
	wmCfg := DefaultWindowManagerConfig()
	wmCfg.WindowBar = false
	wm := NewWindowManager(&h1, wmCfg)
	w1 := wm.Focus()
	wm.Resize(20, 8)

	cfg := DefaultWindowManagerConfig()
	cfg.FocusFrameCharSet.TopLeft = 'A'
	cfg.FocusFrameCharSet.TopRight = 'B'
	cfg.FocusFrameCharSet.BottomLeft = 'C'
	cfg.FocusFrameCharSet.BottomRight = 'D'
	wm.SetFrameCharSet(cfg.FrameCharSet, cfg.FocusFrameCharSet)

	var w2 Window
	var w3 Window
	var ok bool
	h2 := handler.TestHandler{TestComponent: compapi.TestComponent{Ch: 'B'}}
	h3 := handler.TestHandler{TestComponent: compapi.TestComponent{Ch: 'C'}}

	tests := []comptest.TestCase{
		{
			nil, `
┌──────────────────┐
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`,
		}, {func() {
			w2, ok = wm.SplitHorizontal(wm.Focus(), &h2)
			require.True(t, ok)
		}, `
A──────────────────B
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
C──────────────────D
┌──────────────────┐
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`,
		}, {func() {
			assert.True(t, wm.FocusDown())
			w3, ok = wm.SplitVertical(wm.Focus(), &h3)
			require.True(t, ok)
		}, `
┌──────────────────┐
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘
A────────B┌────────┐
│BBBBBBBB││CCCCCCCC│
│BBBBBBBB││CCCCCCCC│
C────────D└────────┘`,
		}, {func() {
			h2.HandleOverride = func(ev term.Event) (bool, bool) {
				assert.NoError(t, w2.Close())
				return true, true
			}
			exit, handled := wm.Handle(term.Event{})
			assert.False(t, exit)
			assert.True(t, handled)
			assert.True(t, w2.Closed())
		}, `
A──────────────────B
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
C──────────────────D
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			wm.FocusUp()
			h1.HandleOverride = func(ev term.Event) (bool, bool) {
				assert.NoError(t, w1.Close())
				return true, true
			}
			exit, handled := wm.Handle(term.Event{})
			assert.False(t, exit)
			assert.True(t, handled)
			assert.True(t, w1.Closed())
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			hf := &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: 'F'}}
			wfloat := wm.FloatingWindow(handler.StaticFloating(hf, 2, 2), component.FloatingConfig{})
			wm.SetFocus(wfloat)
			hf.HandleOverride = func(ev term.Event) (bool, bool) {
				// test that tiled window doesn't attempt to close last node
				// when there's a floating window
				require.Equal(t, 1, wm.SizeTiles())
				require.Equal(t, 1, wm.SizeFloating())
				assert.Error(t, w3.Close())
				assert.NoError(t, wfloat.Close())
				return true, true
			}
			exit, handled := wm.Handle(term.Event{})
			assert.False(t, exit)
			assert.True(t, handled)
			assert.False(t, w3.Closed())
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			h3.HandleOverride = func(ev term.Event) (bool, bool) {
				assert.Error(t, w3.Close())
				return true, true
			}
			exit, handled := wm.Handle(term.Event{})
			assert.True(t, exit)
			assert.True(t, handled)
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			hf := &handler.TestHandler{TestComponent: compapi.TestComponent{Ch: 'F'}}
			wfloat := wm.FloatingWindow(handler.StaticFloating(hf, 2, 2), component.FloatingConfig{})
			wm.SetFocus(wfloat)
			hf.HandleOverride = func(ev term.Event) (bool, bool) {
				// test close itself and focus left
				assert.NoError(t, wm.Focus().Close())
				prevf := wm.SetFocus(w3)
				assert.Equal(t, w3, prevf)
				assert.False(t, wm.FocusLeft())
				return true, true
			}
			exit, handled := wm.Handle(term.Event{})
			assert.False(t, exit)
			assert.True(t, handled)
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			// test that Dimensions are updated for a floating win
			buf := cell.NewBuffer()
			buf.WriteString("1234")
			hf := handler.NopFromComponent(component.Buffer(buf, compapi.StringResponsiveConfig{}))
			wfloat := wm.FloatingWindow(FloatingBuffer(hf, buf), component.FloatingConfig{})
			wm.SetFocus(wfloat)
			buf.WriteString("1234")
		}, `
A────────B─────────┐
│12341234│CCCCCCCCC│
C────────DCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
	}

	comptest.TestComponent(t, wm, w, tests)
}

type scrollableHandler struct {
	compapi.Scrollable
}

// --- TestWindowManagerMouseDrag harness ---

type layout func(t *testing.T, width, height int, frame bool) (
	wm *WindowManager,
	handlers map[string]*handler.TestHandler,
	windows map[string]Window,
)

// mouseEvent's (x, y) are absolute screen-cell coordinates.
type mouseEvent struct {
	key  term.Key
	x, y int
}

func press(x, y int) mouseEvent {
	return mouseEvent{key: term.MouseLeft, x: x, y: y}
}

func drag(x, y int) mouseEvent {
	// A drag is another MouseLeft with no intervening MouseRelease,
	// matching what term/gui/mouse.go emits while the button is held.
	return mouseEvent{key: term.MouseLeft, x: x, y: y}
}

func release(x, y int) mouseEvent {
	return mouseEvent{key: term.MouseRelease, x: x, y: y}
}

func hover(x, y int) mouseEvent {
	// Key=0 is what term/gui/mouse.go emits on a pure cursor move.
	return mouseEvent{key: 0, x: x, y: y}
}

// dispatch is the expected delivery of one mouseEvent: owner is the
// receiving handler name (empty => dropped) and localX/localY are the
// coordinates the handler should observe after WM translation/clamp.
type dispatch struct {
	owner          string
	key            term.Key
	localX, localY int
}

func to(owner string, key term.Key, x, y int) dispatch {
	return dispatch{owner: owner, key: key, localX: x, localY: y}
}

func dropped() dispatch {
	return dispatch{owner: ""}
}

type dragCase struct {
	desc          string
	layout        layout
	frame         bool
	width, height int
	initial       string // window to focus before the sequence; "" keeps default
	events        []mouseEvent
	expect        []dispatch
	expectFocus   string // optional post-sequence focus assertion
}

func singleLayout(name string) layout {
	return func(t *testing.T, width, height int, frame bool) (
		*WindowManager, map[string]*handler.TestHandler, map[string]Window,
	) {
		h := handler.NewTestHandler()
		_, wm := prepareTest(width, height, frame, h)
		handlers := map[string]*handler.TestHandler{name: h}
		windows := map[string]Window{name: wm.Focus()}
		return wm, handlers, windows
	}
}

func vsplitLayout(left, right string) layout {
	return func(t *testing.T, width, height int, frame bool) (
		*WindowManager, map[string]*handler.TestHandler, map[string]Window,
	) {
		lh := handler.NewTestHandler()
		rh := handler.NewTestHandler()
		_, wm := prepareTest(width, height, frame, lh)
		lw := wm.Focus()
		rw, ok := wm.SplitVertical(lw, rh)
		require.True(t, ok)
		handlers := map[string]*handler.TestHandler{left: lh, right: rh}
		windows := map[string]Window{left: lw, right: rw}
		return wm, handlers, windows
	}
}

func threeVsplitLayout(a, b, c string) layout {
	return func(t *testing.T, width, height int, frame bool) (
		*WindowManager, map[string]*handler.TestHandler, map[string]Window,
	) {
		ah := handler.NewTestHandler()
		bh := handler.NewTestHandler()
		ch := handler.NewTestHandler()
		_, wm := prepareTest(width, height, frame, ah)
		aw := wm.Focus()
		bw, ok := wm.SplitVertical(aw, bh)
		require.True(t, ok)
		cw, ok := wm.SplitVertical(bw, ch)
		require.True(t, ok)
		handlers := map[string]*handler.TestHandler{a: ah, b: bh, c: ch}
		windows := map[string]Window{a: aw, b: bw, c: cw}
		return wm, handlers, windows
	}
}

func hsplitLayout(top, bottom string) layout {
	return func(t *testing.T, width, height int, frame bool) (
		*WindowManager, map[string]*handler.TestHandler, map[string]Window,
	) {
		th := handler.NewTestHandler()
		bh := handler.NewTestHandler()
		_, wm := prepareTest(width, height, frame, th)
		tw := wm.Focus()
		bw, ok := wm.SplitHorizontal(tw, bh)
		require.True(t, ok)
		handlers := map[string]*handler.TestHandler{top: th, bottom: bh}
		windows := map[string]Window{top: tw, bottom: bw}
		return wm, handlers, windows
	}
}

func runDragCase(t *testing.T, tc dragCase) {
	t.Helper()
	wm, handlers, windows := tc.layout(t, tc.width, tc.height, tc.frame)

	if tc.initial != "" {
		win, ok := windows[tc.initial]
		require.Truef(t, ok, "unknown initial window %q", tc.initial)
		wm.SetFocus(win)
	}

	type record struct {
		owner string
		ev    term.Event
	}
	var seen []record
	for name, h := range handlers {
		h.HandleOverride = func(ev term.Event) (bool, bool) {
			if ev.Type == term.EventMouse {
				seen = append(seen, record{owner: name, ev: ev})
			}
			return false, false
		}
	}

	for _, ev := range tc.events {
		wm.Handle(term.Event{
			Type:   term.EventMouse,
			Key:    ev.key,
			MouseX: ev.x,
			MouseY: ev.y,
		})
	}

	actual := make([]dispatch, 0, len(tc.events))
	si := 0
	for _, ev := range tc.events {
		if si < len(seen) && eventMatchesIntent(ev, seen[si].ev) {
			s := seen[si]
			actual = append(actual, dispatch{
				owner:  s.owner,
				key:    s.ev.Key,
				localX: s.ev.MouseX,
				localY: s.ev.MouseY,
			})
			si++
		} else {
			actual = append(actual, dropped())
		}
	}
	// Surface surplus records so the failure diff names them.
	for ; si < len(seen); si++ {
		s := seen[si]
		actual = append(actual, dispatch{
			owner:  s.owner + "(unexpected)",
			key:    s.ev.Key,
			localX: s.ev.MouseX,
			localY: s.ev.MouseY,
		})
	}

	assert.Equal(t, tc.expect, actual,
		"dispatch sequence mismatch for case %q", tc.desc)

	if tc.expectFocus != "" {
		want, ok := windows[tc.expectFocus]
		require.Truef(t, ok, "unknown expectFocus window %q", tc.expectFocus)
		assert.Equal(t, want.Window, wm.Focus().Window,
			"focus mismatch for case %q", tc.desc)
	}
}

// Match by key only; absolute → local coords are not directly predictable.
func eventMatchesIntent(intent mouseEvent, seen term.Event) bool {
	return seen.Key == intent.key
}

func (t scrollableHandler) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, 0, false
}

func (t scrollableHandler) Selection() (string, bool) {
	return "", false
}

func (t scrollableHandler) Handle(ev term.Event) (bool, bool) {
	return false, false
}

func TestWindowManagerScrollBar(t *testing.T) {
	buf2 := cell.NewBuffer()
	buf2.WriteString("a\nb\nc\n")
	comp2 := component.NewScroll(buf2)
	rightHandler := scrollableHandler{Scrollable: comp2}

	buf1 := cell.NewBuffer()
	buf1.WriteString("A\nB\nC\n")
	comp1 := component.NewScroll(buf1)
	leftHandler := scrollableHandler{Scrollable: comp1}

	width, height := 12, 4
	writer, wm := prepareTest(width, height, true, leftHandler)

	_, ok := wm.SplitVertical(wm.Focus(), rightHandler)
	require.True(t, ok)

	cases := []handlertest.SingleTestCase{
		{
			term.Event{}, `
┌────┐┌────┐
│A   |│a   |
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 5, MouseY: 1}, `
┌────┐┌────┐
│A   X│a   |
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 6, MouseY: 1}, `
┌────┐┌────┐
│A   |│a   |
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 5, MouseY: 1}, `
┌────┐┌────┐
│A   X│a   |
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 11, MouseY: 1}, `
┌────┐┌────┐
│A   |│a   X
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 11, MouseY: 2}, `
┌────┐┌────┐
│A   |│a   |
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 11, MouseY: 1}, `
┌────┐┌────┐
│A   |│a   X
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 11, MouseY: 2}, `
┌────┐┌────┐
│A   |│b   X
│B   ││c   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 11, MouseY: 3}, `
┌────┐┌────┐
│A   |│c   │
│B   ││    X
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 1, MouseY: 0}, `
┌────┐┌────┐
│A   |│a   X
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 1, MouseY: 0}, `
┌────┐┌────┐
│A   |│a   X
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseRelease, MouseX: 1, MouseY: 0}, `
┌────┐┌────┐
│A   |│a   |
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 5, MouseY: 1}, `
┌────┐┌────┐
│A   X│a   |
│B   ││b   │
└────┘└────┘`,
		},
		{
			term.Event{Type: term.EventMouse, Key: term.MouseLeft, MouseX: 25, MouseY: 25}, `
┌────┐┌────┐
│C   ││a   |
│    X│b   │
└────┘└────┘`,
		},
	}

	handlertest.TestHandler(t, wm, cases, writer)
}

func mouseEv(key term.Key, x, y int) term.Event {
	return term.Event{Type: term.EventMouse, Key: key, MouseX: x, MouseY: y}
}

// barEvent is one floating-bar interaction dispatched by the window
// manager. pos is meaningless for close and cancel.
type barEvent struct {
	kind string
	pos  term.Coordinates
}

func barClose() barEvent        { return barEvent{kind: "close"} }
func barCancel() barEvent       { return barEvent{kind: "cancel"} }
func barDrag(x, y int) barEvent { return barEvent{"drag", term.Coordinates{X: x, Y: y}} }
func barDrop(x, y int) barEvent { return barEvent{"drop", term.Coordinates{X: x, Y: y}} }

// testFloatingBar records the floating-bar interactions dispatched by
// the window manager, in dispatch order.
type testFloatingBar struct {
	NopFloatingBarHandler
	log         []barEvent
	windows     []Window
	handleClose bool
	handleDrop  bool
}

func (b *testFloatingBar) record(ev barEvent, win Window) {
	b.log = append(b.log, ev)
	b.windows = append(b.windows, win)
}

func (b *testFloatingBar) OnBarClose(win Window) bool {
	b.record(barClose(), win)
	return b.handleClose
}

func (b *testFloatingBar) OnBarDrag(win Window, pos term.Coordinates) {
	b.record(barEvent{"drag", pos}, win)
}

func (b *testFloatingBar) OnBarDrop(win Window, pos term.Coordinates) bool {
	b.record(barEvent{"drop", pos}, win)
	return b.handleDrop
}

func (b *testFloatingBar) OnBarDragCancel(win Window) {
	b.record(barCancel(), win)
}

// prepareWindowBarTest builds a 30x12 framed manager with a focused
// floating window of 6x3 content (8x5 framed) at the top-left corner.
func prepareWindowBarTest(t *testing.T) (
	wm *WindowManager, root, float *handler.TestHandler, rootWin, floatWin Window,
) {
	t.Helper()
	root = handler.NewTestHandler()
	wm = NewWindowManager(root, DefaultWindowManagerConfig())
	wm.Resize(30, 12)
	rootWin = wm.Focus()
	float = handler.NewTestHandler()
	floatWin = wm.FloatingWindow(
		handler.StaticFloating(float, 6, 3), component.FloatingConfig{},
	)
	wm.SetFocus(floatWin)
	require.Equal(t, term.Coordinates{}, floatWin.Position())
	require.Equal(t, 8, floatWin.Width())
	require.Equal(t, 5, floatWin.Height())
	return
}

func TestWindowBarCloseClick(t *testing.T) {
	t.Run("default path closes the floating window", func(t *testing.T) {
		wm, _, float, _, floatWin := prepareWindowBarTest(t)
		var contentEvents int
		float.HandleOverride = func(ev term.Event) (bool, bool) {
			if ev.Type == term.EventMouse {
				contentEvents++
			}
			return false, false
		}
		require.Equal(t, 1, wm.SizeFloating())
		_, handled := wm.Handle(mouseEv(term.MouseLeft, component.WindowBarCloseIconX, 0))
		assert.True(t, handled)
		assert.Equal(t, 0, wm.SizeFloating())
		assert.True(t, floatWin.Closed())
		assert.Zero(t, contentEvents, "close press must not reach content")
	})
	t.Run("OnBarClose short-circuits the default close", func(t *testing.T) {
		root := handler.NewTestHandler()
		cfg := DefaultWindowManagerConfig()
		bar := &testFloatingBar{handleClose: true}
		cfg.FloatingBar = bar
		wm := NewWindowManager(root, cfg)
		wm.Resize(30, 12)
		floatWin := wm.FloatingWindow(
			handler.StaticFloating(handler.NewTestHandler(), 6, 3),
			component.FloatingConfig{},
		)
		wm.SetFocus(floatWin)
		_, handled := wm.Handle(mouseEv(term.MouseLeft, component.WindowBarCloseIconX, 0))
		assert.True(t, handled)
		require.Equal(t, []barEvent{barClose()}, bar.log)
		assert.Equal(t, floatWin.ID(), bar.windows[0].ID())
		assert.Equal(t, 1, wm.SizeFloating(), "callback handled the close")
		assert.False(t, floatWin.Closed())
	})
	t.Run("OnBarClose returning false falls back to the default close",
		func(t *testing.T) {
			bar := &testFloatingBar{}
			wm, floatWin := prepareFloatingBarTest(t, bar)
			require.Equal(t, 1, wm.SizeFloating())
			_, handled := wm.Handle(mouseEv(term.MouseLeft, component.WindowBarCloseIconX, 0))
			assert.True(t, handled)
			assert.Equal(t, []barEvent{barClose()}, bar.log)
			assert.True(t, floatWin.Closed(), "the manager must close the window itself")
			assert.Equal(t, 0, wm.SizeFloating())
		})
	t.Run("minimized floats are unaffected", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		require.True(t, floatWin.MinimizeDown(0))
		pos := floatWin.Position()
		wm.Handle(mouseEv(term.MouseLeft, pos.X+component.WindowBarCloseIconX, pos.Y))
		assert.False(t, floatWin.Closed())
		assert.Equal(t, 1, wm.SizeFloating())
	})
}

func TestWindowBarMoveDrag(t *testing.T) {
	wm, root, float, _, floatWin := prepareWindowBarTest(t)
	var contentEvents, rootEvents int
	float.HandleOverride = func(ev term.Event) (bool, bool) {
		if ev.Type == term.EventMouse {
			contentEvents++
		}
		return false, false
	}
	root.HandleOverride = func(ev term.Event) (bool, bool) {
		if ev.Type == term.EventMouse {
			rootEvents++
		}
		return false, false
	}

	// press on the bar, away from the close icon
	_, handled := wm.Handle(mouseEv(term.MouseLeft, 4, 0))
	assert.True(t, handled)
	// drag: the window follows, keeping the grab offset
	_, handled = wm.Handle(mouseEv(term.MouseLeft, 10, 3))
	assert.True(t, handled)
	assert.Equal(t, term.Coordinates{X: 6, Y: 3}, floatWin.Position())
	// drag across the root tile: the pin must survive crossing windows
	// and the position is clamped to keep the window fully visible
	_, handled = wm.Handle(mouseEv(term.MouseLeft, 20, 8))
	assert.True(t, handled)
	assert.Equal(t, term.Coordinates{X: 16, Y: 7}, floatWin.Position())
	_, handled = wm.Handle(mouseEv(term.MouseRelease, 20, 8))
	assert.True(t, handled)
	assert.Equal(t, term.Coordinates{X: 16, Y: 7}, floatWin.Position())

	assert.Zero(t, contentEvents, "drag events must not leak into float content")
	assert.Zero(t, rootEvents, "drag events must not leak into tile content")

	// after release, a fresh press inside content dispatches normally
	wm.Handle(mouseEv(term.MouseLeft, 17, 8))
	assert.Equal(t, 1, contentEvents)
}

// prepareFloatingBarTest builds a 30x12 manager with one tile and a
// focused 8x5 floating window at the top-left corner, wired to bar.
func prepareFloatingBarTest(t *testing.T, bar *testFloatingBar) (*WindowManager, Window) {
	t.Helper()
	cfg := DefaultWindowManagerConfig()
	cfg.FloatingBar = bar
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	wm.Resize(30, 12)
	floatWin := wm.FloatingWindow(
		handler.StaticFloating(handler.NewTestHandler(), 6, 3),
		component.FloatingConfig{},
	)
	wm.SetFocus(floatWin)
	require.Equal(t, term.Coordinates{}, floatWin.Position())
	require.Equal(t, 8, floatWin.Width())
	require.Equal(t, 5, floatWin.Height())
	return wm, floatWin
}

func TestFloatingBarDragLifecycle(t *testing.T) {
	suite := []struct {
		name string
		// bar knobs applied before the sequence runs.
		handleClose bool
		handleDrop  bool
		// mid runs after the sequence's first event, so a test can
		// perturb the manager mid-drag.
		mid  func(t *testing.T, wm *WindowManager, float Window)
		seq  []term.Event
		want []barEvent
	}{
		{
			name: "a bar press alone reports nothing",
			seq:  []term.Event{mouseEv(term.MouseLeft, 4, 0)},
		},
		{
			name: "every move is reported, then exactly one drop",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseLeft, 12, 5),
				mouseEv(term.MouseRelease, 12, 5),
			},
			want: []barEvent{barDrag(10, 3), barDrag(12, 5), barDrop(12, 5)},
		},
		{
			name: "a release without a move cancels instead of dropping",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseRelease, 4, 0),
			},
			want: []barEvent{barCancel()},
		},
		{
			name: "a move back to the press position still drops",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseRelease, 4, 0),
			},
			want: []barEvent{barDrag(10, 3), barDrag(4, 0), barDrop(4, 0)},
		},
		{
			name: "moves after the drop belong to no drag",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseRelease, 10, 3),
				mouseEv(term.MouseLeft, 15, 6),
			},
			want: []barEvent{barDrag(10, 3), barDrop(10, 3)},
		},
		{
			name: "an interrupting mouse event cancels instead of dropping",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseWheelUp, 10, 3),
				mouseEv(term.MouseRelease, 10, 3),
			},
			want: []barEvent{barDrag(10, 3), barCancel()},
		},
		{
			name: "a right press cancels instead of dropping",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseRight, 10, 3),
			},
			want: []barEvent{barDrag(10, 3), barCancel()},
		},
		{
			name: "closing the float mid-drag cancels",
			mid: func(t *testing.T, _ *WindowManager, float Window) {
				require.NoError(t, float.Close())
			},
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseRelease, 10, 3),
			},
			want: []barEvent{barCancel()},
		},
		{
			name:       "a handled drop still ends the drag",
			handleDrop: true,
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseRelease, 10, 3),
				mouseEv(term.MouseLeft, 12, 5),
			},
			want: []barEvent{barDrag(10, 3), barDrop(10, 3)},
		},
		{
			name: "right-edge resize drags report nothing",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 7, 2),
				mouseEv(term.MouseLeft, 12, 2),
				mouseEv(term.MouseRelease, 12, 2),
			},
		},
		{
			name: "bottom-edge resize drags report nothing",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 3, 4),
				mouseEv(term.MouseLeft, 3, 8),
				mouseEv(term.MouseRelease, 3, 8),
			},
		},
		{
			name: "bar-corner resize drags report nothing",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 0, 0),
				mouseEv(term.MouseLeft, 3, 3),
				mouseEv(term.MouseRelease, 3, 3),
			},
		},
		{
			name: "presses inside the float content report nothing",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 3, 2),
				mouseEv(term.MouseLeft, 4, 2),
				mouseEv(term.MouseRelease, 4, 2),
			},
		},
		{
			name: "presses on the tile report nothing",
			seq: []term.Event{
				mouseEv(term.MouseLeft, 20, 8),
				mouseEv(term.MouseLeft, 22, 9),
				mouseEv(term.MouseRelease, 22, 9),
			},
		},
		{
			name:        "the close icon reports a close, not a drag",
			handleClose: true,
			seq: []term.Event{
				mouseEv(term.MouseLeft, component.WindowBarCloseIconX, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseRelease, 10, 3),
			},
			want: []barEvent{barClose()},
		},
		{
			name: "a drag survives the float being maximized mid-drag",
			mid: func(_ *testing.T, wm *WindowManager, float Window) {
				wm.comp.ToggleMaximize(float.Window)
			},
			seq: []term.Event{
				mouseEv(term.MouseLeft, 4, 0),
				mouseEv(term.MouseLeft, 10, 3),
				mouseEv(term.MouseRelease, 10, 3),
			},
			want: []barEvent{barDrag(10, 3), barDrop(10, 3)},
		},
	}

	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			bar := &testFloatingBar{
				handleClose: test.handleClose,
				handleDrop:  test.handleDrop,
			}
			wm, float := prepareFloatingBarTest(t, bar)
			for i, ev := range test.seq {
				if i == 1 && test.mid != nil {
					test.mid(t, wm, float)
				}
				wm.Handle(ev)
			}
			assert.Equal(t, test.want, nilIfEmpty(bar.log))
			assertBarLogConsistent(t, bar)
		})
	}
}

// TestFloatingBarMinimizedFloat pins that a minimized float's strip is
// inert: it starts no drag, so no bar interaction is ever reported.
func TestFloatingBarMinimizedFloat(t *testing.T) {
	bar := &testFloatingBar{}
	wm, float := prepareFloatingBarTest(t, bar)
	require.True(t, float.MinimizeDown(0))
	pos := float.Position()

	wm.Handle(mouseEv(term.MouseLeft, pos.X+4, pos.Y))
	wm.Handle(mouseEv(term.MouseLeft, pos.X+10, pos.Y))
	wm.Handle(mouseEv(term.MouseRelease, pos.X+10, pos.Y))
	assert.Empty(t, bar.log)

	wm.Handle(mouseEv(term.MouseLeft, pos.X+component.WindowBarCloseIconX, pos.Y))
	assert.Empty(t, bar.log, "the close icon of a minimized float is inert too")
	assert.False(t, float.Closed())
}

func nilIfEmpty(log []barEvent) []barEvent {
	if len(log) == 0 {
		return nil
	}
	return log
}

// assertBarLogConsistent checks the contract the browser relies on: a
// drag is terminated by exactly one drop or one cancel, never both, and
// every interaction names a window.
func assertBarLogConsistent(t *testing.T, bar *testFloatingBar) {
	t.Helper()
	dragging := false
	for i, ev := range bar.log {
		assert.NotEqual(t, Window{}, bar.windows[i],
			"interaction %d must name a window", i)
		switch ev.kind {
		case "drag":
			dragging = true
		case "drop", "cancel":
			assert.True(t, dragging || i == 0 || bar.log[i-1].kind == "drag",
				"%s at %d must terminate a drag", ev.kind, i)
			dragging = false
		}
	}
}

// closingFloatingBar closes a tile from OnBarDragCancel, the way the
// browser tears its pre-split placeholder down.
type closingFloatingBar struct {
	NopFloatingBarHandler
	victim    Window
	cancelled int
}

func (b *closingFloatingBar) OnBarDragCancel(Window) {
	b.cancelled++
	if b.victim != (Window{}) && !b.victim.Closed() {
		_ = b.victim.Close()
		b.victim = Window{}
	}
}

// TestFloatingBarCancelMayCloseWindows pins that the cancel hook is
// allowed to change the layout: Handle resolves the window under the
// cursor before the hook runs, and reusing that resolution afterwards
// dereferences a detached tile.
func TestFloatingBarCancelMayCloseWindows(t *testing.T) {
	bar := &closingFloatingBar{}
	cfg := DefaultWindowManagerConfig()
	cfg.FloatingBar = bar
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	wm.Resize(30, 12)
	rootWin := wm.Focus()
	victim, ok := wm.SplitVertical(rootWin, handler.NewTestHandler())
	require.True(t, ok)
	bar.victim = victim

	floatWin := wm.FloatingWindow(
		handler.StaticFloating(handler.NewTestHandler(), 6, 3),
		component.FloatingConfig{},
	)
	wm.SetFocus(floatWin)

	wm.Handle(mouseEv(term.MouseLeft, 4, 0))
	require.NoError(t, floatWin.Close())

	// The cursor sits over the tile the cancel hook closes.
	pos := victim.Position()
	wm.Handle(mouseEv(term.MouseLeft, pos.X+1, pos.Y+1))
	assert.Equal(t, 1, bar.cancelled)
	assert.Equal(t, 1, wm.SizeTiles())
}

// TestFloatingBarNilIsInert guards the nil FloatingBar default: the
// drag machinery must not dereference the hook.
func TestFloatingBarNilIsInert(t *testing.T) {
	wm, _, _, _, floatWin := prepareWindowBarTest(t)
	require.Nil(t, wm.config.FloatingBar)
	wm.Handle(mouseEv(term.MouseLeft, 4, 0))
	wm.Handle(mouseEv(term.MouseLeft, 10, 3))
	wm.Handle(mouseEv(term.MouseWheelUp, 10, 3))
	wm.Handle(mouseEv(term.MouseLeft, 4, 0))
	wm.Handle(mouseEv(term.MouseRelease, 12, 5))
	assert.False(t, floatWin.Closed())
}

// TestNopFloatingBarHandler pins the embeddable default so partial
// implementors inherit inert, non-consuming behaviour.
func TestNopFloatingBarHandler(t *testing.T) {
	var nop NopFloatingBarHandler
	assert.False(t, nop.OnBarClose(Window{}),
		"the default must let the manager close the window")
	assert.False(t, nop.OnBarDrop(Window{}, term.Coordinates{}),
		"the default must leave the release a plain move")
	nop.OnBarDrag(Window{}, term.Coordinates{})
	nop.OnBarDragCancel(Window{})
}

// TestFloatingBarDragReportsPositionAfterMove pins that OnBarDrag is
// dispatched after the window has been repositioned, so a handler can
// measure the float against the cursor it was given.
func TestFloatingBarDragReportsPositionAfterMove(t *testing.T) {
	bar := &testFloatingBar{}
	wm, float := prepareFloatingBarTest(t, bar)

	var seen []term.Coordinates
	wm.Handle(mouseEv(term.MouseLeft, 4, 0))
	for _, at := range []term.Coordinates{{X: 10, Y: 3}, {X: 20, Y: 6}} {
		wm.Handle(mouseEv(term.MouseLeft, at.X, at.Y))
		seen = append(seen, float.Position())
	}
	assert.Equal(t,
		[]term.Coordinates{{X: 6, Y: 3}, {X: 16, Y: 6}}, seen,
		"the float keeps the grab offset while the drag reports positions")
	assert.Equal(t,
		[]barEvent{barDrag(10, 3), barDrag(20, 6)}, bar.log)
}

func TestWindowBarPressFocusesFloat(t *testing.T) {
	wm, _, _, rootWin, floatWin := prepareWindowBarTest(t)
	wm.SetFocus(rootWin)
	require.Equal(t, rootWin.ID(), wm.Focus().ID())
	_, handled := wm.Handle(mouseEv(term.MouseLeft, 4, 0))
	assert.True(t, handled)
	assert.Equal(t, floatWin.ID(), wm.Focus().ID())
	wm.Handle(mouseEv(term.MouseRelease, 4, 0))
}

func TestWindowEdgeResizeDrag(t *testing.T) {
	t.Run("right edge grows the float", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		_, handled := wm.Handle(mouseEv(term.MouseLeft, 7, 2))
		assert.True(t, handled)
		wm.Handle(mouseEv(term.MouseLeft, 12, 2))
		assert.Equal(t, 13, floatWin.Width())
		wm.Handle(mouseEv(term.MouseRelease, 12, 2))
	})
	t.Run("bottom edge grows the float", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		_, handled := wm.Handle(mouseEv(term.MouseLeft, 3, 4))
		assert.True(t, handled)
		wm.Handle(mouseEv(term.MouseLeft, 3, 8))
		assert.Equal(t, 9, floatWin.Height())
		wm.Handle(mouseEv(term.MouseRelease, 3, 8))
	})
	t.Run("bottom-right corner grows both dimensions", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		_, handled := wm.Handle(mouseEv(term.MouseLeft, 7, 4))
		assert.True(t, handled)
		wm.Handle(mouseEv(term.MouseLeft, 12, 8))
		assert.Equal(t, 13, floatWin.Width())
		assert.Equal(t, 9, floatWin.Height())
		wm.Handle(mouseEv(term.MouseRelease, 12, 8))
	})
	t.Run("left edge keeps the right edge fixed", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		require.True(t, wm.comp.MoveWindow(floatWin.Window, term.Coordinates{X: 10, Y: 2}))
		_, handled := wm.Handle(mouseEv(term.MouseLeft, 10, 4))
		assert.True(t, handled)
		wm.Handle(mouseEv(term.MouseLeft, 6, 4))
		assert.Equal(t, 12, floatWin.Width())
		assert.Equal(t, term.Coordinates{X: 6, Y: 2}, floatWin.Position())
		wm.Handle(mouseEv(term.MouseRelease, 6, 4))
	})
	t.Run("resize below the minimum size is ignored", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		_, handled := wm.Handle(mouseEv(term.MouseLeft, 7, 2))
		assert.True(t, handled)
		wm.Handle(mouseEv(term.MouseLeft, 0, 2))
		assert.Equal(t, 8, floatWin.Width())
		wm.Handle(mouseEv(term.MouseRelease, 0, 2))
	})
}

// TestWindowBarCornerResizeDrag covers the diagonal resize drags
// started from the bar's corner cells: both dimensions change and the
// opposite edges stay pinned.
func TestWindowBarCornerResizeDrag(t *testing.T) {
	t.Run("top-right corner grows both dimensions", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		require.True(t, wm.comp.MoveWindow(floatWin.Window, term.Coordinates{X: 10, Y: 4}))
		_, handled := wm.Handle(mouseEv(term.MouseLeft, 17, 4))
		assert.True(t, handled)
		assert.Equal(t, winDragTop|winDragRight, wm.winDrag)
		wm.Handle(mouseEv(term.MouseLeft, 19, 2))
		assert.Equal(t, 10, floatWin.Width())
		assert.Equal(t, 7, floatWin.Height())
		assert.Equal(t, term.Coordinates{X: 10, Y: 2}, floatWin.Position(),
			"left and bottom edges stay pinned")
		wm.Handle(mouseEv(term.MouseRelease, 19, 2))
	})
	t.Run("top-left corner grows both dimensions", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		require.True(t, wm.comp.MoveWindow(floatWin.Window, term.Coordinates{X: 10, Y: 4}))
		_, handled := wm.Handle(mouseEv(term.MouseLeft, 10, 4))
		assert.True(t, handled)
		assert.Equal(t, winDragTop|winDragLeft, wm.winDrag)
		wm.Handle(mouseEv(term.MouseLeft, 8, 2))
		assert.Equal(t, 10, floatWin.Width())
		assert.Equal(t, 7, floatWin.Height())
		assert.Equal(t, term.Coordinates{X: 8, Y: 2}, floatWin.Position(),
			"right and bottom edges stay pinned")
		wm.Handle(mouseEv(term.MouseRelease, 8, 2))
	})
	t.Run("bar-less float resizes from the top edge", func(t *testing.T) {
		cfg := DefaultWindowManagerConfig()
		cfg.WindowBar = false
		wm := NewWindowManager(handler.NewTestHandler(), cfg)
		wm.Resize(30, 12)
		floatWin := wm.FloatingWindow(handler.StaticFloating(
			handler.NewTestHandler(), 6, 3), component.FloatingConfig{})
		require.True(t, wm.comp.MoveWindow(floatWin.Window, term.Coordinates{X: 10, Y: 4}))
		_, handled := wm.Handle(mouseEv(term.MouseLeft, 13, 4))
		assert.True(t, handled)
		assert.Equal(t, winDragTop, wm.winDrag)
		wm.Handle(mouseEv(term.MouseLeft, 13, 2))
		assert.Equal(t, 7, floatWin.Height())
		assert.Equal(t, term.Coordinates{X: 10, Y: 2}, floatWin.Position())
		wm.Handle(mouseEv(term.MouseRelease, 13, 2))
	})
}

// TestWindowBarDoubleClickMaximize covers the bar double click
// toggling a float between maximized and its previous geometry.
func TestWindowBarDoubleClickMaximize(t *testing.T) {
	click := func(wm *WindowManager, x, y int) {
		wm.Handle(mouseEv(term.MouseLeft, x, y))
		wm.Handle(mouseEv(term.MouseRelease, x, y))
	}
	t.Run("double click maximizes and restores", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		click(wm, 4, 0)
		click(wm, 4, 0)
		assert.Equal(t, term.Coordinates{}, floatWin.Position())
		assert.Equal(t, 30, floatWin.Width())
		assert.Equal(t, 12, floatWin.Height())

		// double click the maximized bar again: restored
		click(wm, 4, 0)
		click(wm, 4, 0)
		assert.Equal(t, 8, floatWin.Width())
		assert.Equal(t, 5, floatWin.Height())
	})
	t.Run("slow clicks start a move drag instead", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		click(wm, 4, 0)
		wm.winBarPressTime = time.Now().Add(-time.Second)
		wm.Handle(mouseEv(term.MouseLeft, 4, 0))
		assert.Equal(t, winDragMove, wm.winDrag)
		assert.Equal(t, 8, floatWin.Width())
		wm.Handle(mouseEv(term.MouseRelease, 4, 0))
	})
	t.Run("close icon clicks do not maximize", func(t *testing.T) {
		wm, _, _, _, floatWin := prepareWindowBarTest(t)
		click(wm, component.WindowBarCloseIconX, 0)
		assert.True(t, floatWin.Closed())
	})
}

func TestTileEdgeResizeDrag(t *testing.T) {
	t.Run("right edge sets a fixed tile width", func(t *testing.T) {
		lh, rh := handler.NewTestHandler(), handler.NewTestHandler()
		wm := NewWindowManager(lh, DefaultWindowManagerConfig())
		wm.Resize(30, 12)
		lw := wm.Focus()
		_, ok := wm.SplitVertical(lw, rh)
		require.True(t, ok)
		require.Equal(t, 15, lw.Width())

		_, handled := wm.Handle(mouseEv(term.MouseLeft, 14, 5))
		assert.True(t, handled)
		wm.Handle(mouseEv(term.MouseLeft, 19, 5))
		wm.Handle(mouseEv(term.MouseRelease, 19, 5))
		// tiles relayout on draw
		wm.Draw(term.NewStringWriter(30, 12))
		assert.Equal(t, 20, lw.Width())
	})
	t.Run("bottom edge sets a fixed tile height", func(t *testing.T) {
		th, bh := handler.NewTestHandler(), handler.NewTestHandler()
		wm := NewWindowManager(th, DefaultWindowManagerConfig())
		wm.Resize(30, 12)
		tw := wm.Focus()
		_, ok := wm.SplitHorizontal(tw, bh)
		require.True(t, ok)
		require.Equal(t, 6, tw.Height())

		_, handled := wm.Handle(mouseEv(term.MouseLeft, 5, 5))
		assert.True(t, handled)
		wm.Handle(mouseEv(term.MouseLeft, 5, 8))
		wm.Handle(mouseEv(term.MouseRelease, 5, 8))
		wm.Draw(term.NewStringWriter(30, 12))
		assert.Equal(t, 9, tw.Height())
	})
}

// TestWindowBarScrollBarPrecedence pins that pressing the scroll bar
// thumb on a window's right edge starts a scroll drag rather than an
// edge resize.
func TestWindowBarScrollBarPrecedence(t *testing.T) {
	buf := cell.NewBuffer()
	buf.WriteString("a\nb\nc\nd\ne\nf\n")
	sh := scrollableHandler{Scrollable: component.NewScroll(buf)}
	cfg := DefaultWindowManagerConfig()
	cfg.ScrollBarChar = '|'
	wm := NewWindowManager(handler.NewTestHandler(), cfg)
	wm.Resize(12, 4)
	lw := wm.Focus()
	rw, ok := wm.SplitVertical(lw, sh)
	require.True(t, ok)
	wm.SetFocus(rw)
	// the writer materializes the scroll bar on the frame
	writer := term.NewStringWriter(12, 4)
	wm.Draw(writer)

	frame, ok := rw.Frame()
	require.True(t, ok)
	barPos, _, ok := frame.ScrollBar()
	require.True(t, ok)

	// press on the thumb: window-local coordinates of the right tile
	pos := rw.Position()
	wm.Handle(mouseEv(term.MouseLeft, pos.X+barPos.X, pos.Y+barPos.Y))
	assert.Equal(t, 0, int(wm.winDrag), "scroll bar press must not start a resize drag")
	assert.True(t, wm.prevMouseScrollBarDrag)
	wm.Handle(mouseEv(term.MouseRelease, pos.X+barPos.X, pos.Y+barPos.Y))
}
