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

package browser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	tcomponent "unstable.build/rune/internal/component"
	thandler "unstable.build/rune/internal/handler"
)

func TestNewTabFromContent(t *testing.T) {
	t.Run("returns false if content is already a tab", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		uri, err := workspaceapi.ParseURI("file:///" + "d")
		require.NoError(t, err)
		h := newTestHandler()
		tab := b.NewTab(uri, 'o', "d", h, h)
		b.Focus().SetContent(tab)

		expectedTab, ok := b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///d", expectedTab.URI().String())

		actualTab, ok := b.NewTabFromContent('o', "tab", b.Focus())
		assert.False(t, ok)
		assert.Equal(t, tab, actualTab)
	})

	t.Run("converts scrollable into a tab", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		h := newTestScrollableHandler()
		b.Focus().SetContent(h)

		tab, ok := b.NewTabFromContent('o', "tab", b.Focus())
		assert.True(t, ok)

		h.maxSeekOffset = 10
		assert.Equal(t, 10, tab.MaxSeekOffset())
		assert.True(t, tab.SeekDown())
		assert.True(t, tab.SeekUp())
		assert.NotZero(t, tab.URI())
		win, ok := tab.Window()
		assert.True(t, ok)
		assert.Equal(t, b.Focus(), win)
	})

	t.Run("converts non-scrollable into a tab", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		h := newTestHandler()
		b.Focus().SetContent(h)

		tab, ok := b.NewTabFromContent('o', "tab", b.Focus())
		assert.True(t, ok)
		assert.False(t, tab.SeekDown())
		assert.NotZero(t, tab.URI())
		win, ok := tab.Window()
		assert.True(t, ok)
		assert.Equal(t, b.Focus(), win)

		require.NoError(t, tab.Close())
		assert.True(t, h.closed)
	})

	t.Run("converts floating window content into a tab", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		h := newTestHandler()
		floating := b.Floating(h, browserapi.FloatingConfig{
			Alignment: component.AlignmentHorizontallyCentered,
		})

		tab, ok := b.NewTabFromContent('o', "tab", floating)
		assert.True(t, ok)
		assert.False(t, tab.SeekDown())
		assert.NotZero(t, tab.URI())
		win, ok := tab.Window()
		assert.True(t, ok)
		assert.Equal(t, floating, win)

		require.NoError(t, floating.Close())
		assert.False(t, h.closed)

		win, ok = tab.Window()
		assert.False(t, ok)

		b.Focus().SetContent(tab)
		win, ok = tab.Window()
		assert.True(t, ok)
		assert.Equal(t, b.Focus(), win)
	})

	t.Run("converts content with URI into a tab, maintains URI", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		uri1, err := workspaceapi.ParseURI("file:///a")
		require.NoError(t, err)

		h := newTestHandlerURI(uri1)
		b.Focus().SetContent(h)

		tab, ok := b.NewTabFromContent('o', "tab", b.Focus())
		assert.True(t, ok)
		assert.Equal(t, uri1, tab.URI())
	})

	t.Run("multiple windows, multiple tabs", func(t *testing.T) {
		b := NewComponent(DefaultConfig())
		win0 := b.Focus()

		tabs := []string{"A", "b", "C", "d"}
		for i, name := range tabs {
			uri, err := workspaceapi.ParseURI("file:///" + name)
			require.NoError(t, err)
			h := newTestHandler()
			tab := b.NewTab(uri, 'o', name, h, h)
			if i%2 == 0 {
				b.Split(browserapi.OrientationRight, b.Focus(), tab)
			}
		}

		// window 0 has nothing
		// window 1 has A
		// window 2 has C
		h := newTestHandler()
		require.True(t, b.FocusLeft())
		require.True(t, b.FocusLeft())
		b.Focus().SetContent(h)
		tab, ok := b.NewTabFromContent('o', "tab", b.Focus())
		assert.True(t, ok)

		actualWin, ok := tab.Window()
		assert.True(t, ok)
		assert.Equal(t, win0, actualWin)

		actualTabs := b.Tabs()
		require.Len(t, actualTabs, 5)

		assert.True(t, b.SetContentToTab(win0, 1))

		actualWin, ok = tab.Window()
		assert.False(t, ok)
		assert.Nil(t, actualWin)
	})

	t.Run("subscribes content to tab focus changes, if applicable", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		uri1, err := workspaceapi.ParseURI("file:///a")
		require.NoError(t, err)

		h := newTestHandlerURI(uri1)
		b.Focus().SetContent(h)

		assert.Equal(t, 0, h.onFocus)
		assert.Equal(t, 0, h.onFree)

		tab1, ok := b.NewTabFromContent('o', "tab", b.Focus())
		assert.True(t, ok)
		assert.Equal(t, uri1, tab1.URI())

		assert.Equal(t, 1, h.onFocus)
		assert.Equal(t, 0, h.onFree)

		uri, err := workspaceapi.ParseURI("file:///b")
		require.NoError(t, err)
		tab2 := b.NewTab(uri, 'o', "b", newTestHandler(), nil)

		b.Focus().SetContent(tab2)
		assert.Equal(t, 1, h.onFocus)
		assert.Equal(t, 1, h.onFree)

		b.Focus().SetContent(tab1)
		assert.Equal(t, 2, h.onFocus)
		assert.Equal(t, 1, h.onFree)
	})
}

func TestWindowDraw(t *testing.T) {
	noFrameNoDim := DefaultConfig()
	noFrameNoDim.Dim = false
	noFrameNoDim.Frame = false

	noFrameDim := DefaultConfig()
	noFrameDim.Dim = true
	noFrameDim.Frame = false

	noFrameBW := DefaultConfig()
	noFrameBW.Dim = true
	noFrameBW.BW = true
	noFrameBW.Frame = false
	suite := []Config{
		noFrameNoDim,
		noFrameDim,
		noFrameBW,
	}

	for _, cfg := range suite {
		t.Run(fmt.Sprintf("%#v", cfg), func(t *testing.T) {
			// create a decent mix of components and UI elements
			b := NewComponent(cfg)
			b.Split(browserapi.OrientationRight, b.Focus(), newTestHandler())
			b.Split(browserapi.OrientationBottom, b.Focus(), newTestHandler())
			b.Split(browserapi.OrientationTop, b.Focus(), newTestHandler())
			b.Split(browserapi.OrientationLeft, b.Focus(), newTestHandler())
			uri1, err := workspaceapi.ParseURI("file:///a")
			require.NoError(t, err)
			h := newTestHandler()
			b.NewTab(uri1, 'a', "a", h, h)
			cfg := browserapi.BarConfig{Size: 1, Orientation: browserapi.OrientationTop}
			b.Bar(cfg, newTestHandler())
			cfg.Orientation = browserapi.OrientationBottom
			b.Bar(cfg, newTestHandler())
			cfg.Orientation = browserapi.OrientationLeft
			b.Bar(cfg, newTestHandler())
			cfg.Orientation = browserapi.OrientationRight
			b.Bar(cfg, newTestHandler())
			b.Floating(newTestHandler(), browserapi.FloatingConfig{
				Alignment: component.AlignmentHorizontallyCentered,
			})

			width, height := 12, 8
			writer1 := term.NewStringWriter(width, height)
			writer2 := term.NewStringWriter(width, height)

			b.Resize(width, height)
			b.Draw(writer1)

			// draw first union (everything), and then windows on top
			// and it matches Draw, then DrawWindow is correct.
			b.tabs.ResetFocus()
			for id, t := range b.buffers {
				b.tabs.SetIconAttr(id, term.Attributes{})
				if !t.free {
					b.tabs.SetFocus(id)
				}
			}
			if b.focusWindow != (thandler.Window{}) {
				win, ok := b.findWindow(b.focusWindow.ID())
				if ok {
					tab, ok := browserTabAtWindow(win)
					if ok {
						b.tabs.SetIconAttr(b.mustFindTabID(tab), term.Attributes{})
					}
				}
			}
			b.union.Draw(writer2)
			b.wm.Iterate(func(w thandler.Window) {
				win, _ := b.findWindow(w.ID())
				b.DrawWindow(win, writer2)
			})
			b.overwriteFocusWindowUnion(writer2)

			writer1.Flush()
			writer2.Flush()
			assert.Equal(t, writer1.String(), writer2.String())
		})
	}
}

func TestWindowFocusTabIconCueFollowsFocus(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	windowFocusIconAttr := term.Attributes{Bg: term.ColorGreen, Attrs: term.AttrBold}
	cfg.FocusTabIconAttr = windowFocusIconAttr

	b := NewComponent(cfg)

	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	tabA := b.NewTab(uriA, 'A', "a", newTestHandler(), nil)
	b.Focus().SetContent(tabA)

	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)
	tabB := b.NewTab(uriB, 'B', "b", newTestHandler(), nil)
	_, ok := b.Split(browserapi.OrientationRight, b.Focus(), tabB)
	require.True(t, ok)

	width, height := 20, 5
	b.Resize(width, height)
	writer := term.NewStringWriter(width, height)
	b.Draw(writer)
	cells := writer.Cells()

	expectedIconAttr := windowFocusIconAttr
	expectedIconAttr.Attrs |= term.AttrNegativeVerticalRenderOffset
	expectedFocusTabAttr := cfg.FocusTabAttr
	expectedFocusTabAttr.Attrs |= term.AttrNegativeVerticalRenderOffset
	expectedDefaultIconAttr := term.Attributes{Attrs: term.AttrNegativeVerticalRenderOffset}
	assert.Equal(t, 'A', cells[0].Ch)
	assert.Equal(t, expectedDefaultIconAttr, cells[0].Attributes())
	assert.Equal(t, 'a', cells[2].Ch)
	assert.Equal(t, expectedFocusTabAttr, cells[2].Attributes())
	assert.Equal(t, 'B', cells[5].Ch)
	assert.Equal(t, expectedIconAttr, cells[5].Attributes())
	assert.Equal(t, 'b', cells[7].Ch)
	assert.Equal(t, expectedFocusTabAttr, cells[7].Attributes())

	require.True(t, b.FocusLeft())
	writer = term.NewStringWriter(width, height)
	b.Draw(writer)
	cells = writer.Cells()

	assert.Equal(t, 'A', cells[0].Ch)
	assert.Equal(t, expectedIconAttr, cells[0].Attributes())
	assert.Equal(t, 'a', cells[2].Ch)
	assert.Equal(t, expectedFocusTabAttr, cells[2].Attributes())
	assert.Equal(t, 'B', cells[5].Ch)
	assert.Equal(t, expectedDefaultIconAttr, cells[5].Attributes())
	assert.Equal(t, 'b', cells[7].Ch)
	assert.Equal(t, expectedFocusTabAttr, cells[7].Attributes())
}

// TestNewTabHonorsTabOverrideIcon verifies that when
// Config.TabOverrideIcon is set, every tab icon rendered in the tab
// bar uses that single rune regardless of the icon argument passed to
// NewTab.
func TestNewTabHonorsTabOverrideIcon(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	cfg.TabOverrideIcon = '●'

	b := NewComponent(cfg)

	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	tabA := b.NewTab(uriA, 'A', "a", newTestHandler(), nil)
	require.NoError(t, b.Focus().SetContent(tabA))

	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)
	tabB := b.NewTab(uriB, 'B', "b", newTestHandler(), nil)
	_, ok := b.Split(browserapi.OrientationRight, b.Focus(), tabB)
	require.True(t, ok)

	width, height := 20, 5
	b.Resize(width, height)
	writer := term.NewStringWriter(width, height)
	b.Draw(writer)
	cells := writer.Cells()

	assert.Equal(t, '●', cells[0].Ch, "first tab icon must be overridden")
	assert.Equal(t, 'a', cells[2].Ch)
	assert.Equal(t, '●', cells[5].Ch, "second tab icon must be overridden")
	assert.Equal(t, 'b', cells[7].Ch)
}

// TestWindowFocusTabHighlightCueFollowsFocus verifies that when multiple
// tabs are bound to different tiles, only the focused window's tab
// renders the focus-frame highlight; switching window focus moves the
// highlight to the newly focused tab.
func TestWindowFocusTabHighlightCueFollowsFocus(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	cfg.FocusTabHighlightChar = '━'
	cfg.TabBarHeight = 2

	b := NewComponent(cfg)

	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	tabA := b.NewTab(uriA, 'A', "a", newTestHandler(), nil)
	require.NoError(t, b.Focus().SetContent(tabA))

	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)
	tabB := b.NewTab(uriB, 'B', "b", newTestHandler(), nil)
	_, ok := b.Split(browserapi.OrientationRight, b.Focus(), tabB)
	require.True(t, ok)

	width, height := 20, 5
	b.Resize(width, height)
	writer := term.NewStringWriter(width, height)
	b.Draw(writer)
	cells := writer.Cells()

	// After Split, the new right tile is focused, so tab B carries
	// the highlight (cells 5..7) and tab A does not (cells 0..2).
	for x := 0; x < 3; x++ {
		assert.NotEqual(t, '━', cells[x].Ch,
			"tab A must not be highlighted at x=%d", x)
	}
	for x := 5; x < 8; x++ {
		assert.Equal(t, '━', cells[x].Ch,
			"tab B must be highlighted at x=%d", x)
	}

	require.True(t, b.FocusLeft())
	writer = term.NewStringWriter(width, height)
	b.Draw(writer)
	cells = writer.Cells()

	// After focusing left, the highlight moves to tab A.
	for x := 0; x < 3; x++ {
		assert.Equal(t, '━', cells[x].Ch,
			"tab A must be highlighted after focus left at x=%d", x)
	}
	for x := 5; x < 8; x++ {
		assert.NotEqual(t, '━', cells[x].Ch,
			"tab B must not be highlighted after focus left at x=%d", x)
	}
}

// TestTabClickFocusesOwningWindow verifies that clicking a tab bound to
// a non-focused window switches focus to that window (instead of
// returning ErrTabNotFree silently).
func TestTabClickFocusesOwningWindow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	cfg.FocusTabHighlightChar = '━'
	cfg.TabBarHeight = 2

	b := NewComponent(cfg)

	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	tabA := b.NewTab(uriA, 'A', "a", newTestHandler(), nil)
	leftWin := b.Focus()
	require.NoError(t, leftWin.SetContent(tabA))

	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)
	tabB := b.NewTab(uriB, 'B', "b", newTestHandler(), nil)
	rightWin, ok := b.Split(browserapi.OrientationRight, leftWin, tabB)
	require.True(t, ok)
	require.Equal(t, rightWin, b.Focus())

	width, height := 20, 5
	b.Resize(width, height)
	writer := term.NewStringWriter(width, height)
	b.Draw(writer)

	// Click tab A (id 0) — it is bound to the left, non-focused window.
	require.True(t, b.tabs.OnClick(0))
	assert.Equal(t, leftWin, b.Focus(),
		"clicking a tab bound to another window must focus that window")

	writer = term.NewStringWriter(width, height)
	b.Draw(writer)
	cells := writer.Cells()
	for x := 0; x < 3; x++ {
		assert.Equal(t, '━', cells[x].Ch,
			"tab A must be highlighted after click at x=%d", x)
	}
	for x := 5; x < 8; x++ {
		assert.NotEqual(t, '━', cells[x].Ch,
			"tab B must not be highlighted after click at x=%d", x)
	}
}

// TestSplitAlreadyBoundTabFocusesOwningWindow reproduces RUNE-236's
// sibling crash: `runectl wm split <focus> <file>` with the file already
// open resolves to the existing, already-bound *Tab. Binding that one
// tab to a second window corrupts the tab/window bookkeeping and later
// panics in Draw when the tab is removed. Split must instead focus the
// window already showing the tab, like the tab-click path.
func TestSplitAlreadyBoundTabFocusesOwningWindow(t *testing.T) {
	b := NewComponent(DefaultConfig())

	uri, err := workspaceapi.ParseURI("file:///CHANGELOG.md")
	require.NoError(t, err)
	h := newTestHandler()
	tab := b.NewTab(uri, 'o', "CHANGELOG.md", h, h)
	leftWin := b.Focus()
	require.NoError(t, leftWin.SetContent(tab))

	got, ok := b.Split(browserapi.OrientationRight, leftWin, tab)
	require.True(t, ok)
	assert.Equal(t, leftWin, got,
		"splitting with an already-open tab must return its existing window")
	assert.Equal(t, leftWin, b.Focus(),
		"splitting with an already-open tab must focus its existing window")

	assert.Len(t, b.buffers, 1, "the tab must not be duplicated in buffers")
	refs := 0
	for _, w := range b.windows {
		if bt, ok := browserTabAtWindow(w); ok && bt == tab {
			refs++
		}
	}
	assert.Equal(t, 1, refs, "exactly one window may reference the tab")
}

// TestSplitInvertedUsesSplitWindowArgument guards against splitInverted
// swapping the handler.Window of the *focused* window instead of the one
// passed as the split target. Splitting a non-focused tile must not move
// the focused window's tile.
func TestSplitInvertedUsesSplitWindowArgument(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	b := NewComponent(cfg)

	handlerA := newTestHandler()
	winA := b.Focus()
	require.NoError(t, winA.SetContent(handlerA))

	handlerB := newTestHandler()
	winB, ok := b.Split(browserapi.OrientationRight, winA, handlerB)
	require.True(t, ok)
	require.Equal(t, winB, b.SetFocus(winA))

	b.Resize(60, 10)

	handlerC := newTestHandler()
	winC, ok := b.Split(browserapi.OrientationLeft, winB, handlerC)
	require.True(t, ok)

	b.Resize(60, 10)

	contentA, err := winA.Content()
	require.NoError(t, err)
	assert.Equal(t, handlerA, contentA, "the focused window's content must be untouched")
	contentB, err := winB.Content()
	require.NoError(t, err)
	assert.Equal(t, handlerB, contentB)
	contentC, err := winC.Content()
	require.NoError(t, err)
	assert.Equal(t, handlerC, contentC)

	assert.Equal(t, 0, winA.Position().X, "the focused window must remain the leftmost tile")
	assert.Greater(t, winC.Position().X, winA.Position().X,
		"the new window must be placed to the right of the untouched focused window")
	assert.Greater(t, winB.Position().X, winC.Position().X,
		"the new window must be placed left of the split target")
}

// TestTabClickOnFocusedTabIsNoOp verifies that clicking the tab of the
// currently-focused window does not change focus or surface an error.
func TestTabClickOnFocusedTabIsNoOp(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false

	b := NewComponent(cfg)

	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	tabA := b.NewTab(uriA, 'A', "a", newTestHandler(), nil)
	leftWin := b.Focus()
	require.NoError(t, leftWin.SetContent(tabA))

	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)
	tabB := b.NewTab(uriB, 'B', "b", newTestHandler(), nil)
	rightWin, ok := b.Split(browserapi.OrientationRight, leftWin, tabB)
	require.True(t, ok)
	require.Equal(t, rightWin, b.Focus())

	b.Resize(20, 5)

	// Click tab B (id 1) — already bound to the focused window.
	require.True(t, b.tabs.OnClick(1))
	assert.Equal(t, rightWin, b.Focus(),
		"clicking the tab of the focused window must not change focus")
}

// TestTabClickFreeTabLoadsIntoFocusedWindow guards the unchanged
// free-tab path: clicking a free tab swaps it into the focused window.
func TestTabClickFreeTabLoadsIntoFocusedWindow(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false

	b := NewComponent(cfg)

	// Open two tabs in the same window so the first ends up free.
	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	tabA := b.NewTab(uriA, 'A', "a", newTestHandler(), nil)
	require.NoError(t, b.Focus().SetContent(tabA))

	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)
	tabB := b.NewTab(uriB, 'B', "b", newTestHandler(), nil)
	require.NoError(t, b.Focus().SetContent(tabB))

	// tabA is now free; tabB occupies the focused window.
	_, bound := tabA.Window()
	require.False(t, bound, "tabA must be free before click")

	b.Resize(20, 5)

	require.True(t, b.tabs.OnClick(0))

	focused, ok := b.FocusTab()
	require.True(t, ok)
	assert.Equal(t, tabA, focused,
		"clicking a free tab must load it into the focused window")
}

// TestSwapContentRebindsTabs reproduces a crash where swapping window
// contents left each tab bound to the window it moved out of. Closing
// one of the windows then freed the wrong tab, and clicking the other
// tab focused the closed window and panicked in SetFocus.
func TestSwapContentRebindsTabs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		split     browserapi.Orientation
		fromFirst bool
		swap      func(*Component) bool
	}{
		{"left", browserapi.OrientationRight, false, (*Component).SwapContentLeft},
		{"right", browserapi.OrientationRight, true, (*Component).SwapContentRight},
		{"up", browserapi.OrientationBottom, false, (*Component).SwapContentUp},
		{"down", browserapi.OrientationBottom, true, (*Component).SwapContentDown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Frame = false
			cfg.FrameUnion = false
			b := NewComponent(cfg)

			uriA, err := workspaceapi.ParseURI("file:///a")
			require.NoError(t, err)
			tabA := b.NewTab(uriA, 'A', "a", newTestHandler(), nil)
			first := b.Focus()
			require.NoError(t, first.SetContent(tabA))

			uriB, err := workspaceapi.ParseURI("file:///b")
			require.NoError(t, err)
			tabB := b.NewTab(uriB, 'B', "b", newTestHandler(), nil)
			second, ok := b.Split(tc.split, first, tabB)
			require.True(t, ok)
			b.Resize(40, 20)
			if tc.fromFirst {
				b.SetFocus(first)
			}

			require.True(t, tc.swap(b))

			for _, win := range []Window{first, second} {
				content, err := win.Content()
				require.NoError(t, err)
				tab := content.(*Tab)
				bound, ok := tab.Window()
				require.True(t, ok)
				assert.Equal(t, win, bound,
					"tab %q must be bound to the window showing it", tab.URI())
			}

			require.NoError(t, second.Close())
			_, ok = tabA.Window()
			assert.False(t, ok, "closing the window showing tab A must free it")
			bound, ok := tabB.Window()
			require.True(t, ok)
			assert.Equal(t, first, bound)

			require.NotPanics(t, func() { b.tabs.OnClick(1) })
			assert.Equal(t, first, b.Focus())
		})
	}
}

// TestLayoutAliasSwitchingThenTabClick reproduces the user's crash: open
// a file (a tab in the focused window), repeatedly switch window layouts
// via aliases that run `windowcloseall` followed by one or more
// `windownew right`, then click the tab. `windownew right` focuses the
// new empty window, so `windowcloseall` closes the tab's original window.
// CloseOtherWindows closed the raw handler window without going through
// the browser's closeWindow path, so the tab's window binding was never
// released: it stayed "stuck" pointing at a removed window. Clicking it
// focused that detached tile and the next cursor pass panicked in
// TileTree.TilePosition.
func TestLayoutAliasSwitchingThenTabClick(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	cfg.TabBarHeight = 2

	b := NewComponent(cfg)
	width, height := 40, 10
	b.Resize(width, height)

	// Open a file: a tab in the initially focused window.
	uri, err := workspaceapi.ParseURI("file:///main.go")
	require.NoError(t, err)
	tab := b.NewTab(uri, 'o', "main.go", newTestHandler(), nil)
	require.NoError(t, b.Focus().SetContent(tab))

	// windownew right: split focus to a new empty window and focus it.
	newRight := func() {
		_, ok := b.Split(browserapi.OrientationRight, b.Focus(), nil)
		require.True(t, ok)
	}
	// windowcloseall: keep the focused window, close the rest.
	closeAll := func() {
		if err := b.CloseOtherWindows(b.Focus()); err != nil &&
			err.Error() != "no windows to close" {
			t.Fatalf("CloseOtherWindows: %v", err)
		}
	}
	draw := func() {
		require.NotPanics(t, func() {
			_, _, _ = b.Cursor()
			b.Draw(term.NewStringWriter(width, height))
		})
	}

	auxScreen := func() { closeAll(); newRight(); newRight(); draw() }
	laptop := func() { closeAll(); newRight(); draw() }

	for range 4 {
		auxScreen()
		laptop()
	}

	// The first windowcloseall closed the tab's original window, so the
	// tab must be released, not left bound to a removed window.
	_, bound := tab.Window()
	assert.False(t, bound,
		"tab whose window was closed by windowcloseall must be free")

	// The tab is still in the bar; click it the way the user did. This
	// must not crash the next cursor/draw pass.
	id, ok := b.findTabID(tab)
	require.True(t, ok, "tab must still be present in the bar")
	b.tabs.OnClick(id)
	draw()
}

// TestNonFocusTabAttrRespectedWithFrameFg is a regression test for
// non_focus_tab_attr being overridden by the window manager's frame_attr
// foreground. The tabs Scroll background used to share frame_attr (gray
// in the production rune.star), and any tab name whose configured fg
// was ColorDefault picked up that gray fg instead of the configured
// non_focus_tab_attr default. Verify that a free (non-focused) tab
// renders with NonFocusTabAttr (Fg=ColorDefault) even when frame_attr
// has a non-default Fg.
func TestNonFocusTabAttrRespectedWithFrameFg(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	// simulate user-configured frame_attr with a non-default foreground
	// (the production rune.star sets this to gray).
	cfg.WindowManagerConfig.FrameAttr = term.Attributes{Fg: term.ColorGray}
	cfg.FocusTabAttr = term.Attributes{Fg: term.ColorBlue}
	cfg.NonFocusTabAttr = term.Attributes{} // i.e. fg=default,bg=default

	b := NewComponent(cfg)

	// open two tabs in the same window so the first ends up free.
	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	tabA := b.NewTab(uriA, 0, "a", newTestHandler(), nil)
	require.NoError(t, b.Focus().SetContent(tabA))

	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)
	tabB := b.NewTab(uriB, 0, "b", newTestHandler(), nil)
	require.NoError(t, b.Focus().SetContent(tabB))

	width, height := 20, 5
	b.Resize(width, height)
	writer := term.NewStringWriter(width, height)
	b.Draw(writer)
	cells := writer.Cells()

	expectedFocus := cfg.FocusTabAttr
	expectedFocus.Attrs |= term.AttrNegativeVerticalRenderOffset
	expectedNonFocus := cfg.NonFocusTabAttr
	expectedNonFocus.Attrs |= term.AttrNegativeVerticalRenderOffset

	// "a" is free (not bound to any window) and must render with
	// NonFocusTabAttr; "b" is bound to the focused window and renders
	// with FocusTabAttr.
	assert.Equal(t, 'a', cells[0].Ch)
	assert.Equal(t, expectedNonFocus, cells[0].Attributes(),
		"non-focused tab name must render with NonFocusTabAttr, not frame fg")
	assert.Equal(t, 'b', cells[3].Ch)
	assert.Equal(t, expectedFocus, cells[3].Attributes(),
		"focused tab name must render with FocusTabAttr")
}

// TestFocusedLastTabVisibleWithTabBarOffset is a regression test for a bug
// where the focused tab (the rightmost one) was being clipped off-screen
// because the underlying component.Tabs was being resized to the full
// component width while a TabBarOffset visually shifted the bar to the
// right, leaving the trailing portion of the bar outside the visible
// viewport. The fix is to subtract TabBarOffset from the width passed to
// the Tabs component so the resize algorithm operates on the actual
// viewport width and the focused tab is rendered in the visible area.
func TestFocusedLastTabVisibleWithTabBarOffset(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	cfg.TabBarOffset = 8

	b := NewComponent(cfg)

	names := []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta"}
	tabs := make([]*Tab, len(names))
	for i, n := range names {
		uri, err := workspaceapi.ParseURI("file:///" + n)
		require.NoError(t, err)
		tabs[i] = b.NewTab(uri, 0, n, newTestHandler(), nil)
	}
	// Focus the last tab.
	require.NoError(t, b.Focus().SetContent(tabs[len(tabs)-1]))

	// Width is intentionally smaller than the sum of every tab at full
	// width, so the resize algorithm has to shrink non-focused tabs to
	// keep the focused (last) tab fully visible inside the available
	// inner width: width 40 minus offset 8 = 32 cells of bar.
	const width = 40
	const height = 5
	b.Resize(width, height)
	writer := term.NewStringWriter(width, height)
	b.Draw(writer)
	require.NoError(t, writer.Flush())
	rows := strings.Split(writer.String(), "\n")
	require.GreaterOrEqual(t, len(rows), 1)

	// The focused tab label must be fully visible somewhere on the top
	// bar — not clipped at the right edge.
	bar := rows[0]
	require.Contains(t, bar, "zeta",
		"focused (last) tab label must be visible inside the viewport: %q", bar)

	// Sanity: at least one preceding tab must have been truncated for
	// "zeta" to fit (otherwise the algorithm wasn't exercised).
	truncated := 0
	for _, n := range names[:len(names)-1] {
		if !strings.Contains(bar, n) {
			truncated++
		}
	}
	assert.Greater(t, truncated, 0,
		"expected at least one non-focused tab to be shrunk: %q", bar)
}

// TestRightInsetReservesWindowColumn asserts Config.RightInset narrows
// only the window manager. The tab bar is a top union member, so it is
// laid out before the reserved column and keeps the full width, which
// is what lets a native overlay float below it.
func TestRightInsetReservesWindowColumn(t *testing.T) {
	const width, height, inset = 40, 8, 5

	draw := func(rightInset int) []string {
		cfg := DefaultConfig()
		cfg.Frame = false
		cfg.FrameUnion = false
		cfg.Dim = false
		cfg.RightInset = rightInset

		b := NewComponent(cfg)
		uri, err := workspaceapi.ParseURI("file:///alpha")
		require.NoError(t, err)
		h := newTestHandler()
		h.Ch = 'x'
		require.NoError(t, b.Focus().SetContent(b.NewTab(uri, 0, "alpha", h, nil)))

		b.Resize(width, height)
		writer := term.NewStringWriter(width, height)
		b.Draw(writer)
		require.NoError(t, writer.Flush())
		return strings.Split(writer.String(), "\n")
	}

	full, narrowed := draw(0), draw(inset)
	require.Equal(t, len(full), len(narrowed))

	var windowRows int
	for y := range full {
		if full[y] != strings.Repeat("x", width) {
			assert.Equal(t, full[y], narrowed[y],
				"row %d is above the reserved column and must not move", y)
			continue
		}
		windowRows++
		assert.Equal(t,
			strings.Repeat("x", width-inset)+strings.Repeat(" ", inset),
			narrowed[y], "row %d", y)
	}
	require.Positive(t, windowRows, "expected the window to fill some rows")
}

// TestSetRightInsetRelaysOut asserts the reserved column can be widened
// and collapsed after construction. The union only ever appends members,
// so this exercises the rebuild path, including replaying bars added
// through the public Bar API.
func TestSetRightInsetRelaysOut(t *testing.T) {
	const width, height, inset = 40, 8, 5

	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false

	b := NewComponent(cfg)
	uri, err := workspaceapi.ParseURI("file:///alpha")
	require.NoError(t, err)
	h := newTestHandler()
	h.Ch = 'x'
	require.NoError(t, b.Focus().SetContent(b.NewTab(uri, 0, "alpha", h, nil)))

	bar := newTestHandler()
	bar.Ch = 'b'
	b.Bar(browserapi.BarConfig{
		Orientation: browserapi.OrientationBottom,
		Size:        1,
		Frame:       browserapi.BarFrameNever,
	}, bar)

	draw := func() []string {
		b.Resize(width, height)
		writer := term.NewStringWriter(width, height)
		b.Draw(writer)
		require.NoError(t, writer.Flush())
		return strings.Split(writer.String(), "\n")
	}

	before := draw()
	require.Contains(t, before, strings.Repeat("b", width),
		"the bar must span the full width before any inset change")

	b.SetRightInset(inset)
	narrowed := draw()
	require.Contains(t, narrowed,
		strings.Repeat("x", width-inset)+strings.Repeat(" ", inset),
		"the window must be narrowed by the reserved column")
	require.Contains(t, narrowed, strings.Repeat("b", width),
		"the bar must survive the union rebuild")

	b.SetRightInset(0)
	assert.Equal(t, before, draw(),
		"collapsing the reserved column must restore the original layout")
}

func TestComponentRestoreTileLayout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	b := NewComponent(cfg)
	root := b.Focus()
	right, ok := b.Split(browserapi.OrientationRight, root, newTestHandler())
	require.True(t, ok)
	bottom, ok := b.Split(browserapi.OrientationBottom, right, newTestHandler())
	require.True(t, ok)

	uri, err := workspaceapi.ParseURI("file:///restored")
	require.NoError(t, err)
	tabHandler := newTestHandlerURI(uri)
	tab := b.NewTab(uri, 'r', "restored", tabHandler, tabHandler)
	tab.Subscribe(tabHandler)
	layout := b.TileLayout()
	restored := b.RestoreTileLayout(layout, func(windowID uint64) browserapi.Handler {
		switch windowID {
		case root.WindowID():
			return newTestHandler()
		case right.WindowID():
			return tab
		case bottom.WindowID():
			return nil
		default:
			return nil
		}
	})

	require.Len(t, restored, 3)
	for _, oldID := range []uint64{root.WindowID(), right.WindowID(), bottom.WindowID()} {
		require.Contains(t, restored, oldID)
		require.NotEqual(t, oldID, restored[oldID].WindowID())
	}
	content, err := restored[right.WindowID()].Content()
	require.NoError(t, err)
	require.Equal(t, tab, content)
	tabWin, ok := tab.Window()
	require.True(t, ok)
	require.Equal(t, restored[right.WindowID()], tabWin)
	require.Equal(t, 1, tabHandler.onFocus)

	content, err = restored[bottom.WindowID()].Content()
	require.NoError(t, err)
	require.NotNil(t, content)
}

func TestWindowClosedOnClose(t *testing.T) {
	b := NewComponent(DefaultConfig())
	h := newTestHandler()
	var win Window
	win = b.Floating(FuncFloatingHandler(h, func() error {
		if !win.Closed() {
			_ = win.Close()
		}
		return h.Close()
	}), browserapi.FloatingConfig{})
	assert.NoError(t, win.Close())
}

func TestBrowserScrollable(t *testing.T) {
	t.Run("create floating window", func(t *testing.T) {
		b := NewComponent(DefaultConfig())
		scrollable := newTestScrollableHandler()

		win := b.Floating(scrollable, browserapi.FloatingConfig{})
		content, err := win.Content()
		require.NoError(t, err)

		_, ok := content.(component.Scrollable)
		assert.True(t, ok)
		assert.NoError(t, win.Close())
	})

	t.Run("create split window", func(t *testing.T) {
		b := NewComponent(DefaultConfig())
		scrollable := newTestScrollableHandler()

		win, ok := b.Split(browserapi.OrientationLeft, b.Focus(), scrollable)
		require.True(t, ok)

		content, err := win.Content()
		require.NoError(t, err)

		_, ok = content.(*nopScrollableHandler)
		assert.True(t, ok)
		assert.NoError(t, win.Close())
	})

	t.Run("update content of window", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		win, ok := b.Split(browserapi.OrientationLeft, b.Focus(), newTestHandler())
		require.True(t, ok)

		scrollable := newTestScrollableHandler()
		err := win.SetContent(scrollable)
		require.NoError(t, err)

		content, err := win.Content()
		require.NoError(t, err)

		_, ok = content.(component.Scrollable)
		assert.True(t, ok)
		assert.NoError(t, win.Close())
	})

	t.Run("new tab", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		uri1, err := workspaceapi.ParseURI("file:///a")
		require.NoError(t, err)
		h := newTestScrollableHandler()
		tab := b.NewTab(uri1, 'x', "a", h, h)

		content := tab.Handler()
		_, ok := content.(component.Scrollable)
		assert.True(t, ok)
	})
}

func TestBrowserFloating(t *testing.T) {
	t.Run("create floating window", func(t *testing.T) {
		b := NewComponent(DefaultConfig())
		h := newTestHandler()

		win := b.Floating(h, browserapi.FloatingConfig{})
		content, err := win.Content()
		require.NoError(t, err)

		_, ok := content.(*nopHandler)
		assert.True(t, ok)
		assert.NoError(t, win.Close())
	})

	t.Run("update content of floating window with non floating uses static dimensions",
		func(t *testing.T) {
			b := NewComponent(DefaultConfig())
			var h Floating
			h = newTestHandler()
			win := b.Floating(h, browserapi.FloatingConfig{})

			notFloating := browserapi.NopHandler(handler.NewTestHandler())
			err := win.SetContent(notFloating)
			require.NoError(t, err)
		})

	t.Run("floating and scrollable maintains interfaces",
		func(t *testing.T) {
			b := NewComponent(DefaultConfig())
			var h Floating
			h = newTestScrollableHandler()
			win := b.Floating(h, browserapi.FloatingConfig{})

			content, err := win.Content()
			require.NoError(t, err)
			_, fok := content.(Floating)
			assert.True(t, fok)
			_, sok := content.(Scrollable)
			assert.True(t, sok)

			// also internally
			internal := win.(*browserWindow).win.Content()

			_, fok = internal.(Floating)
			assert.True(t, fok)
			_, sok = internal.(Scrollable)
			assert.True(t, sok)
		})
}

// TestComponentCloseFloatingReentrantWindowClose reproduces a shutdown crash
// where a floating handler's Close callback closes its own captured window
// (as the cheatsheet does). Component.Close must not create a duplicate
// browserWindow that defeats the double-close guard and panics with
// "window not found".
func TestComponentCloseFloatingReentrantWindowClose(t *testing.T) {
	b := NewComponent(DefaultConfig())

	var win Window
	floating := FuncFloatingHandler(newTestHandler(), func() error {
		return win.Close()
	})
	win = b.Floating(floating, browserapi.FloatingConfig{
		Alignment: component.AlignmentCentered,
	})

	assert.NotPanics(t, func() {
		_ = b.Close()
	})
}

// TestWindowBarCloseIconClick covers the window-bar close routing: a
// press on the close icon must run through Component.closeWindow so
// the windows map is cleaned and the handler is released.
func TestWindowBarCloseIconClick(t *testing.T) {
	b := NewComponent(DefaultConfig())
	b.Resize(30, 14)
	h := newTestHandler()
	win := b.Floating(h, browserapi.FloatingConfig{})
	require.Equal(t, 1, b.FloatingWindows())
	require.Len(t, b.windows, 2)

	pos := win.(*browserWindow).win.Position()
	wmPos := b.WindowManagerPosition()
	_, handled := b.Handle(term.Event{
		Type:   term.EventMouse,
		Key:    term.MouseLeft,
		MouseX: wmPos.X + pos.X + tcomponent.WindowBarCloseIconX,
		MouseY: wmPos.Y + pos.Y,
	})
	assert.True(t, handled)
	assert.Equal(t, 0, b.FloatingWindows())
	assert.Len(t, b.windows, 1, "windows map must be cleaned")
	assert.True(t, h.closed, "handler must be released")
}

// TestFloatingWindowBarOptOut covers that FloatingConfig.NoWindowBar
// opens bar-less floating windows even though the browser wraps
// handlers in browserContent adapters.
func TestFloatingWindowBarOptOut(t *testing.T) {
	b := NewComponent(DefaultConfig())
	b.Resize(30, 14)

	optOut := b.Floating(newTestHandler(), browserapi.FloatingConfig{
		NoWindowBar: true,
	})
	assert.False(t, optOut.(*browserWindow).win.HasWindowBar())

	regular := b.Floating(newTestHandler(), browserapi.FloatingConfig{})
	assert.True(t, regular.(*browserWindow).win.HasWindowBar())
}

// TestFloatingWindowTitle covers that FloatingConfig.Title sets the
// floating window's bar title even though the browser wraps handlers
// in browserContent adapters.
func TestFloatingWindowTitle(t *testing.T) {
	b := NewComponent(DefaultConfig())
	b.Resize(30, 14)

	titled := b.Floating(newTestHandler(), browserapi.FloatingConfig{
		Title: "hi",
	})
	assert.Equal(t, "hi", titled.(*browserWindow).win.Title())

	regular := b.Floating(newTestHandler(), browserapi.FloatingConfig{})
	assert.Empty(t, regular.(*browserWindow).win.Title())
}

func TestComponentCloseOtherWindows(t *testing.T) {
	t.Run("fails if there's only one window", func(t *testing.T) {
		b := NewComponent(DefaultConfig())
		require.Error(t, b.CloseOtherWindows(b.Focus()))
		assert.Equal(t, 1, b.Tiles())
		assert.Equal(t, 0, b.FloatingWindows())
	})
	// A floating window takes focus when it opens, so "close every
	// other window" has to keep a tile rather than refuse: the
	// alternative leaves the float that prompted the call on screen.
	t.Run("closes the focused floating window and keeps a tile", func(t *testing.T) {
		b := NewComponent(DefaultConfig())
		win := b.Floating(newTestHandler(), browserapi.FloatingConfig{
			Alignment: component.AlignmentHorizontallyCentered,
		})
		require.True(t, b.Focus().IsFloating())
		require.NoError(t, b.CloseOtherWindows(win))
		assert.Equal(t, 1, b.Tiles())
		assert.Equal(t, 0, b.FloatingWindows())
	})
	t.Run("closes all floating and non-floating windows except focus", func(t *testing.T) {
		b := NewComponent(DefaultConfig())
		orig := b.Focus()
		_ = b.Floating(newTestHandler(), browserapi.FloatingConfig{
			Alignment: component.AlignmentHorizontallyCentered,
		})
		win, ok := b.Split(browserapi.OrientationDefault, orig, newTestHandler())
		require.True(t, ok)
		require.NoError(t, b.CloseOtherWindows(win))
		assert.Equal(t, 1, b.Tiles())
		assert.Equal(t, 0, b.FloatingWindows())

		require.Error(t, b.CloseOtherWindows(win))
		assert.Equal(t, 1, b.Tiles())
		assert.Equal(t, 0, b.FloatingWindows())
	})
	t.Run("does not panic closing floating whose Close callback closes its own window",
		func(t *testing.T) {
			b := NewComponent(DefaultConfig())
			focus := b.Focus()

			var floatWin Window
			floating := FuncFloatingHandler(newTestHandler(), func() error {
				return floatWin.Close()
			})
			floatWin = b.Floating(floating, browserapi.FloatingConfig{
				Alignment: component.AlignmentCentered,
			})

			assert.NotPanics(t, func() {
				_ = b.CloseOtherWindows(focus)
			})
			assert.Equal(t, 0, b.FloatingWindows())
		})
}

func TestRemoveInactiveTabs(t *testing.T) {
	b := NewComponent(DefaultConfig())

	tabDefs := []struct {
		name   string
		active bool
	}{
		{"a", false}, {"b", true}, {"c", true}, {"d", false}}

	for _, tabDef := range tabDefs {
		uri, err := workspaceapi.ParseURI("file:///" + tabDef.name)
		require.NoError(t, err)
		h := newTestHandler()
		tab := b.NewTab(uri, 'o', tabDef.name, h, h)
		if tabDef.active {
			b.Split(browserapi.OrientationRight, b.Focus(), tab)
		}
	}

	assertTabNames(t, b, []string{"a", "b", "c", "d"})
	b.RemoveInactiveTabs()
	assertTabNames(t, b, []string{"b", "c"})
}

func TestRemoveTab(t *testing.T) {
	b := NewComponent(DefaultConfig())

	tabDefs := []struct {
		name  string
		split bool
	}{
		{"a", false}, {"b", true}, {"c", true}, {"d", false}}

	tabs := make([]*Tab, 0)
	for _, tabDef := range tabDefs {
		uri, err := workspaceapi.ParseURI("file:///" + tabDef.name)
		require.NoError(t, err)
		h := newTestHandler()
		tab := b.NewTab(uri, 'o', tabDef.name, h, h)
		if tabDef.split {
			b.Split(browserapi.OrientationRight, b.Focus(), tab)
		}
		tabs = append(tabs, tab)
	}

	assertTabNames(t, b, []string{"a", "b", "c", "d"})
	assert.True(t, b.RemoveTab(tabs[0]))
	assert.True(t, b.RemoveTab(tabs[1]))
	assert.True(t, b.RemoveTab(tabs[3]))
	assertTabNames(t, b, []string{"c"})
}

// TestRemoveTabStale asserts that removing a tab whose handle is already
// gone from the component returns false without panicking. A non-modal
// prompt can capture a *Tab, the user closes that tab, and the prompt's
// discard later calls RemoveTab on the now-stale handle.
func TestRemoveTabStale(t *testing.T) {
	b := NewComponent(DefaultConfig())

	uri, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	h := newTestHandler()
	tab := b.NewTab(uri, 'o', "a", h, h)

	assert.True(t, b.RemoveTab(tab))
	assert.NotPanics(t, func() {
		assert.False(t, b.RemoveTab(tab))
	})
}

// TestRemoveTabDoesNotRetainPointersInTail asserts that after RemoveTab the
// dropped *Tab pointers are not retained past len(c.buffers) in the slice's
// backing array. A naive append(s[:i], s[i+1:]...) leaves the previous tail
// duplicate behind, which transitively pins the file's *cell.Buffer and
// produces multi-GB workspace-close leaks on large files.
func TestRemoveTabDoesNotRetainPointersInTail(t *testing.T) {
	b := NewComponent(DefaultConfig())

	names := []string{"a", "b", "c", "d"}
	tabs := make([]*Tab, 0, len(names))
	for _, name := range names {
		uri, err := workspaceapi.ParseURI("file:///" + name)
		require.NoError(t, err)
		h := newTestHandler()
		tabs = append(tabs, b.NewTab(uri, 'o', name, h, h))
	}

	// Remove every tab in order. After each removal the slot at index
	// len(c.buffers) inside the backing array must be nil; otherwise the
	// removed tab (and its transitive cell.Buffer) stays GC-reachable.
	for range names {
		require.True(t, b.RemoveTab(tabs[0]))
		tabs = tabs[1:]
		tail := b.buffers[len(b.buffers) : len(b.buffers)+1]
		assert.Nilf(t, tail[0],
			"buffers[%d] not cleared after RemoveTab; tail still pins *Tab",
			len(b.buffers))
	}
}

// newActivityComponent returns a Component that shades active tabs,
// with tabs a, b and c open.
func newActivityComponent(t *testing.T, onActivity func()) (*Component, []*Tab) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ActiveTabShader = "pulse"
	cfg.OnTabActivity = onActivity
	b := NewComponent(cfg)
	b.SetInterrupter(term.NopInterrupter())
	b.Resize(40, 10)
	t.Cleanup(func() { _ = b.Close() })
	var tabs []*Tab
	for _, name := range []string{"a", "b", "c"} {
		uri, err := workspaceapi.ParseURI("file:///" + name)
		require.NoError(t, err)
		h := newTestHandler()
		tabs = append(tabs, b.NewTab(uri, 'o', name, h, h))
	}
	return b, tabs
}

// Activity is a per-tab level: the shader runs while any tab is active,
// setting the current state again changes nothing and the listener only
// hears about actual changes.
func TestSetTabActivity(t *testing.T) {
	var calls int
	b, tabs := newActivityComponent(t, func() { calls++ })

	unknown, err := workspaceapi.ParseURI("file:///unknown")
	require.NoError(t, err)
	assert.False(t, b.SetTabActivity(unknown, true))
	assert.False(t, b.HasActiveTabs())
	assert.False(t, b.shadedTabs.Running())
	assert.Zero(t, calls)

	require.True(t, b.SetTabActivity(tabs[1].uri, true))
	assert.True(t, b.HasActiveTabs())
	assert.True(t, b.shadedTabs.Running())
	assert.Equal(t, []int{1}, b.activeTabIndices())
	assert.Equal(t, 1, calls)

	require.True(t, b.SetTabActivity(tabs[1].uri, true))
	assert.Equal(t, 1, calls, "setting the current state again is a no-op")

	require.True(t, b.SetTabActivity(tabs[2].uri, true))
	assert.Equal(t, []int{1, 2}, b.activeTabIndices())

	require.True(t, b.SetTabActivity(tabs[1].uri, false))
	assert.True(t, b.shadedTabs.Running(), "another tab is still active")
	require.True(t, b.SetTabActivity(tabs[2].uri, false))
	assert.False(t, b.HasActiveTabs())
	assert.False(t, b.shadedTabs.Running())
	assert.Equal(t, 4, calls)
}

// Activity follows the tab rather than its position in the bar.
func TestTabActivityFollowsMovedTab(t *testing.T) {
	b, tabs := newActivityComponent(t, nil)
	require.True(t, b.SetTabActivity(tabs[0].uri, true))
	require.True(t, b.SetContentToTab(b.Focus(), 0))
	require.NoError(t, b.MoveTabRight(b.Focus()))
	assertTabNames(t, b, []string{"b", "a", "c"})
	assert.Equal(t, []int{1}, b.activeTabIndices())
}

// A tab's activity dies with it, so a crashed extension cannot leave a
// stale mark behind.
func TestRemoveTabClearsActivity(t *testing.T) {
	var calls int
	b, tabs := newActivityComponent(t, func() { calls++ })
	require.True(t, b.SetTabActivity(tabs[0].uri, true))
	require.True(t, b.RemoveTab(tabs[0]))
	assert.False(t, b.HasActiveTabs())
	assert.False(t, b.shadedTabs.Running())
	assert.Equal(t, 2, calls)

	// Removing an idle tab does not report a change.
	require.True(t, b.RemoveTab(tabs[1]))
	assert.Equal(t, 2, calls)

	// A tab reopened under the same uri starts idle.
	h := newTestHandler()
	b.NewTab(tabs[0].uri, 'o', "a", h, h)
	assert.False(t, b.HasActiveTabs())
}

// Activity marked before the host installs an interrupter is shown as
// soon as it does, and closing the browser stops the effect.
func TestSetInterrupterStartsPendingActivity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ActiveTabShader = "pulse"
	b := NewComponent(cfg)
	uri, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	h := newTestHandler()
	b.NewTab(uri, 'o', "a", h, h)

	require.True(t, b.SetTabActivity(uri, true))
	assert.False(t, b.shadedTabs.Running())
	b.SetInterrupter(term.NopInterrupter())
	assert.True(t, b.shadedTabs.Running())

	require.NoError(t, b.Close())
	assert.False(t, b.shadedTabs.Running())
}

func TestTabAttrs(t *testing.T) {
	b := NewComponent(DefaultConfig())

	tabDefs := []struct {
		name string
	}{
		{"a"}, {"b"},
	}

	expectedAttr := term.Attributes{Fg: term.ColorYellow, Bg: term.ColorGreen}
	uris := make([]workspaceapi.URI, 0)
	for i, tabDef := range tabDefs {
		uri, err := workspaceapi.ParseURI("file:///" + tabDef.name)
		require.NoError(t, err)
		h := newTestHandler()
		_ = b.NewTab(uri, 'o', tabDef.name, h, h)
		if i%2 == 0 {
			b.SetTabNameAndAttrs(uri, "NAME", expectedAttr)
		}
		uris = append(uris, uri)
	}

	actualAttr, ok := b.TabAttrs(uris[0])
	require.True(t, ok)
	assert.Equal(t, expectedAttr, actualAttr)

	actualAttr, ok = b.TabAttrs(uris[1])
	require.True(t, ok)
	assert.Equal(t, term.Attributes{}, actualAttr)
}

// TestSetTabIcon verifies SetTabIcon overrides the tab icon for a
// known URI and returns false for an unknown URI, and that
// ResetTabIcon restores the icon the tab was created with.
func TestSetTabIcon(t *testing.T) {
	b := NewComponent(DefaultConfig())

	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)

	hA := newTestHandler()
	hB := newTestHandler()
	_ = b.NewTab(uriA, 'A', "a", hA, hA)
	_ = b.NewTab(uriB, 'B', "b", hB, hB)

	// override the icon for tab A
	require.True(t, b.SetTabIcon(uriA, '★'))
	assert.Equal(t, '★', b.tabs.TabIcon(0))
	// tab B is unaffected
	assert.Equal(t, 'B', b.tabs.TabIcon(1))

	// unknown URI returns false and is a no-op
	missing, err := workspaceapi.ParseURI("file:///missing")
	require.NoError(t, err)
	assert.False(t, b.SetTabIcon(missing, '?'))
	assert.Equal(t, '★', b.tabs.TabIcon(0))
	assert.Equal(t, 'B', b.tabs.TabIcon(1))

	// ResetTabIcon restores the original icon
	require.True(t, b.ResetTabIcon(uriA))
	assert.Equal(t, 'A', b.tabs.TabIcon(0))
	assert.False(t, b.ResetTabIcon(missing))
}

// TestSetTabIconHonorsTabOverrideIcon verifies that when
// Config.TabOverrideIcon is set, ResetTabIcon restores to the
// overridden glyph (not the icon argument originally passed to
// NewTab).
func TestSetTabIconHonorsTabOverrideIcon(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TabOverrideIcon = '●'
	b := NewComponent(cfg)

	uri, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	h := newTestHandler()
	_ = b.NewTab(uri, 'A', "a", h, h)
	assert.Equal(t, '●', b.tabs.TabIcon(0))

	require.True(t, b.SetTabIcon(uri, '★'))
	assert.Equal(t, '★', b.tabs.TabIcon(0))

	require.True(t, b.ResetTabIcon(uri))
	assert.Equal(t, '●', b.tabs.TabIcon(0))
}

func TestMoveTabs(t *testing.T) {
	b := NewComponent(DefaultConfig())

	tabs := []string{"A", "b", "C", "d"}
	for i, name := range tabs {
		uri, err := workspaceapi.ParseURI("file:///" + name)
		require.NoError(t, err)
		h := newTestHandler()
		tab := b.NewTab(uri, 'o', name, h, h)
		if i%2 == 0 {
			b.Split(browserapi.OrientationRight, b.Focus(), tab)
		}
	}

	tab, ok := b.FocusTab()
	require.True(t, ok)
	assert.Equal(t, "file:///C", tab.URI().String())

	assert.NoError(t, b.MoveTabLeft(b.Focus()))
	assertTabNames(t, b, []string{"A", "C", "b", "d"})
	assert.NoError(t, b.MoveTabLeft(b.Focus()))
	assertTabNames(t, b, []string{"C", "A", "b", "d"})
	assert.Error(t, b.MoveTabLeft(b.Focus()))
	assertTabNames(t, b, []string{"C", "A", "b", "d"})
	assert.NoError(t, b.MoveTabRight(b.Focus()))
	assertTabNames(t, b, []string{"A", "C", "b", "d"})
	assert.NoError(t, b.MoveTabRight(b.Focus()))
	assertTabNames(t, b, []string{"A", "b", "C", "d"})
	assert.NoError(t, b.MoveTabRight(b.Focus()))
	assertTabNames(t, b, []string{"A", "b", "d", "C"})
	assert.Error(t, b.MoveTabRight(b.Focus()))
	assertTabNames(t, b, []string{"A", "b", "d", "C"})
	assert.NoError(t, b.MoveTabTo(b.Focus(), 1))
	assertTabNames(t, b, []string{"A", "C", "b", "d"})
	assert.NoError(t, b.MoveTabTo(b.Focus(), 0))
	assertTabNames(t, b, []string{"C", "A", "b", "d"})
	assert.NoError(t, b.MoveTabTo(b.Focus(), 10))
	assertTabNames(t, b, []string{"A", "b", "d", "C"})
}

func TestFocusLastFocusOnCloseTab(t *testing.T) {
	t.Run("RemoveWindowContent", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		tabs := []string{"A", "b", "C", "d"}
		for i, name := range tabs {
			uri, err := workspaceapi.ParseURI("file:///" + name)
			require.NoError(t, err)
			h := newTestHandler()
			tab := b.NewTab(uri, 'o', name, h, h)
			if i == len(tabs)-1 {
				b.Focus().SetContent(tab)
			}
		}

		tab, ok := b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///d", tab.URI().String())

		assert.True(t, b.PreviousTab(b.Focus()))
		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///C", tab.URI().String())

		assert.True(t, b.PreviousTab(b.Focus()))
		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///b", tab.URI().String())

		require.True(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///C", tab.URI().String())

		require.True(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///d", tab.URI().String())

		require.True(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///A", tab.URI().String())

		require.False(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.False(t, ok)
	})

	t.Run("RemoveTab", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		tabs := []string{"A", "b", "C", "d"}
		for i, name := range tabs {
			uri, err := workspaceapi.ParseURI("file:///" + name)
			require.NoError(t, err)
			h := newTestHandler()
			tab := b.NewTab(uri, 'o', name, h, h)
			if i == len(tabs)-1 {
				b.Focus().SetContent(tab)
			}
		}

		tab, ok := b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///d", tab.URI().String())

		assert.True(t, b.PreviousTab(b.Focus()))
		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///C", tab.URI().String())

		assert.True(t, b.PreviousTab(b.Focus()))

		assert.True(t, b.RemoveTab(tab))

		require.True(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///A", tab.URI().String())

		assert.True(t, b.RemoveTab(tab))
	})

	t.Run("SetContentToTab", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		tabs := []string{"A", "b", "C", "d"}
		for i, name := range tabs {
			uri, err := workspaceapi.ParseURI("file:///" + name)
			require.NoError(t, err)
			h := newTestHandler()
			tab := b.NewTab(uri, 'o', name, h, h)
			if i == len(tabs)-1 {
				b.Focus().SetContent(tab)
			}
		}

		assert.False(t, b.SetContentToTab(b.Focus(), 3)) // already at d
		tab, ok := b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///d", tab.URI().String())

		assert.True(t, b.SetContentToTab(b.Focus(), 2))
		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///C", tab.URI().String())

		assert.True(t, b.SetContentToTab(b.Focus(), 1))
		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///b", tab.URI().String())

		require.True(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///C", tab.URI().String())

		require.True(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///d", tab.URI().String())

		require.True(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///A", tab.URI().String())

		require.False(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.False(t, ok)
	})

	t.Run("multiple windows", func(t *testing.T) {
		b := NewComponent(DefaultConfig())

		tabs := []string{"A", "b", "C", "d"}
		for i, name := range tabs {
			uri, err := workspaceapi.ParseURI("file:///" + name)
			require.NoError(t, err)
			h := newTestHandler()
			tab := b.NewTab(uri, 'o', name, h, h)
			if i%2 == 0 {
				b.Split(browserapi.OrientationRight, b.Focus(), tab)
			}
		}

		// window 0 has nothing
		// window 1 has A
		// window 2 has C

		assert.True(t, b.SetContentToTab(b.Focus(), 3))
		tab, ok := b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///d", tab.URI().String())

		// move to window 0, set C as content, so d.prev is invalid
		require.True(t, b.FocusLeft())
		require.True(t, b.FocusLeft())

		assert.True(t, b.SetContentToTab(b.Focus(), 2))
		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///C", tab.URI().String())

		require.True(t, b.RemoveWindowContent(b.Focus()))

		tab, ok = b.FocusTab()
		require.True(t, ok)
		assert.Equal(t, "file:///b", tab.URI().String())
	})
}

var _ component.Scrollable = (*nopScrollableHandler)(nil)

type nopScrollableHandler struct {
	maxSeekOffset int
	nopHandler
}

func (b *nopScrollableHandler) SeekUp() bool {
	return true
}

func (b *nopScrollableHandler) SeekDown() bool {
	return true
}

func (b *nopScrollableHandler) SeekOffset() int {
	return 0
}

func (b *nopScrollableHandler) MaxSeekOffset() int {
	return b.maxSeekOffset
}

func newTestScrollableHandler() *nopScrollableHandler {
	return &nopScrollableHandler{}
}

type nopHandler struct {
	closed bool
	handler.TestHandler
}

func (n *nopHandler) Dimensions() (int, int) {
	return 12, 8
}

func (n *nopHandler) Close() error {
	n.closed = true
	return nil
}

func newTestHandler() *nopHandler {
	return &nopHandler{}
}

type nopHandlerURI struct {
	onFocus int
	onFree  int
	uri     workspaceapi.URI
	nopHandler
}

func (n *nopHandlerURI) URI() workspaceapi.URI {
	return n.uri
}

func (n *nopHandlerURI) OnFocus(*Tab) {
	n.onFocus++
}

func (n *nopHandlerURI) OnFree(*Tab) {
	n.onFree++
}

func newTestHandlerURI(uri workspaceapi.URI) *nopHandlerURI {
	return &nopHandlerURI{uri: uri}
}

func assertTabNames(t *testing.T, b *Component, expected []string) {
	var actual []string
	for _, tab := range b.Tabs() {
		tabName, _, _ := b.TabName(tab.URI())
		actual = append(actual, tabName)
	}
	assert.Equal(t, expected, actual)
}

func TestDropZoneAt(t *testing.T) {
	suite := []struct {
		name  string
		local term.Coordinates
		w, h  int
		want  winDropZone
	}{
		{"left edge", term.Coordinates{X: 0, Y: 10}, 40, 20, winDropLeft},
		{"right edge", term.Coordinates{X: 39, Y: 10}, 40, 20, winDropRight},
		{"top edge", term.Coordinates{X: 20, Y: 0}, 40, 20, winDropTop},
		{"bottom edge", term.Coordinates{X: 20, Y: 19}, 40, 20, winDropBottom},
		{"center", term.Coordinates{X: 20, Y: 10}, 40, 20, winDropCenter},
		{"dead zone", term.Coordinates{X: 12, Y: 10}, 40, 20, winDropNone},
		{"top-left corner favors the closer edge",
			term.Coordinates{X: 0, Y: 3}, 40, 20, winDropLeft},
		{"bottom-right corner favors the closer edge",
			term.Coordinates{X: 33, Y: 19}, 40, 20, winDropBottom},
		{"out of bounds", term.Coordinates{X: 40, Y: 10}, 40, 20, winDropNone},
		{"negative", term.Coordinates{X: -1, Y: 0}, 40, 20, winDropNone},
		{"empty tile", term.Coordinates{}, 0, 0, winDropNone},
		{"1x1 tile is all center", term.Coordinates{}, 1, 1, winDropCenter},
		{"3x3 tile center", term.Coordinates{X: 1, Y: 1}, 3, 3, winDropCenter},
		{"3x3 tile left", term.Coordinates{X: 0, Y: 1}, 3, 3, winDropLeft},
		{"3x3 tile top", term.Coordinates{X: 1, Y: 0}, 3, 3, winDropTop},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, dropZoneAt(test.local, test.w, test.h))
		})
	}
}

// winDragBrowser returns a browser with a single tile and a focused
// floating window, plus zoneAt: the window-manager coordinates of each
// drop zone of the tile as it was *before* any pre-split.
func winDragBrowser(t *testing.T, width, height int) (
	b *Component, tile Window, float Window, zoneAt func(winDropZone) term.Coordinates,
) {
	t.Helper()
	b = NewComponent(dragConfig())
	tile = b.Focus()
	require.NoError(t, tile.SetContent(newTestHandler()))
	b.Resize(width, height)

	float = b.Floating(newTestHandler(), browserapi.FloatingConfig{
		Title: "float",
	})
	b.Resize(width, height)
	b.Draw(term.NewStringWriter(width, height))
	require.Equal(t, float, b.Focus())

	pos, w, h := tile.Position(), tile.Width(), tile.Height()
	zoneAt = func(zone winDropZone) term.Coordinates {
		switch zone {
		case winDropLeft:
			return term.Coordinates{X: pos.X, Y: pos.Y + h/2}
		case winDropRight:
			return term.Coordinates{X: pos.X + w - 1, Y: pos.Y + h/2}
		case winDropTop:
			return term.Coordinates{X: pos.X + w/2, Y: pos.Y}
		case winDropBottom:
			return term.Coordinates{X: pos.X + w/2, Y: pos.Y + h - 1}
		case winDropCenter:
			return term.Coordinates{X: pos.X + w/2, Y: pos.Y + h/2}
		default:
			// dead zone: just outside the left band and left of center
			return term.Coordinates{X: pos.X + w/winDropBandDiv + 1, Y: pos.Y + h/2}
		}
	}
	require.Equal(t, winDropNone, dropZoneAt(
		term.Coordinates{X: zoneAt(winDropNone).X - pos.X, Y: h / 2}, w, h),
		"the dead-zone probe must not land in a drop zone")
	return b, tile, float, zoneAt
}

func barDrag(b *Component, float Window, pos term.Coordinates) {
	b.OnBarDrag(float.(*browserWindow).win, pos)
}

// framedDragBrowser returns a browser whose floating windows carry a
// window bar, so mouse events can reach the bar drag machinery. The
// unframed fixtures used elsewhere have no bar at all.
func framedDragBrowser(t *testing.T, width, height int) (
	b *Component, tile Window, float Window, zoneAt func(winDropZone) term.Coordinates,
) {
	t.Helper()
	cfg := dragConfig()
	cfg.Frame = true
	cfg.WindowBar = true
	b = NewComponent(cfg)
	tile = b.Focus()
	require.NoError(t, tile.SetContent(newTestHandler()))
	b.Resize(width, height)

	float = b.Floating(newTestHandler(), browserapi.FloatingConfig{Title: "float"})
	b.Resize(width, height)
	b.Draw(term.NewStringWriter(width, height))
	require.Equal(t, float, b.Focus())
	require.True(t, float.(*browserWindow).win.HasWindowBar(),
		"the fixture must give the float a bar to drag")

	pos, w, h := tile.Position(), tile.Width(), tile.Height()
	zoneAt = func(zone winDropZone) term.Coordinates {
		switch zone {
		case winDropLeft:
			return term.Coordinates{X: pos.X, Y: pos.Y + h/2}
		case winDropRight:
			return term.Coordinates{X: pos.X + w - 1, Y: pos.Y + h/2}
		case winDropTop:
			return term.Coordinates{X: pos.X + w/2, Y: pos.Y}
		case winDropBottom:
			return term.Coordinates{X: pos.X + w/2, Y: pos.Y + h - 1}
		case winDropCenter:
			return term.Coordinates{X: pos.X + w/2, Y: pos.Y + h/2}
		default:
			return term.Coordinates{X: pos.X + w/winDropBandDiv + 1, Y: pos.Y + h/2}
		}
	}
	return b, tile, float, zoneAt
}

// mouseAt builds a component-relative mouse event. Callers pass window
// manager coordinates; the tab bar offset is added here.
func mouseAt(b *Component, key term.Key, pos term.Coordinates) term.Event {
	off := b.WindowManagerPosition()
	return term.Event{
		Type:   term.EventMouse,
		Key:    key,
		MouseX: pos.X + off.X,
		MouseY: pos.Y + off.Y,
	}
}

// TestWinDropThroughMouseEvents drives the whole feature the way the
// runtime does - a press on the float's bar, moves, then a release -
// instead of calling the FloatingBarHandler hooks directly, so the
// window manager wiring is covered end to end.
func TestWinDropThroughMouseEvents(t *testing.T) {
	t.Run("dragging onto a tile edge splits it and installs a tab",
		func(t *testing.T) {
			b, tile, float, zoneAt := framedDragBrowser(t, 60, 20)
			content, err := float.Content()
			require.NoError(t, err)
			bar := float.Position()

			_, handled := b.Handle(mouseAt(b, term.MouseLeft,
				term.Coordinates{X: bar.X + 4, Y: bar.Y}))
			require.True(t, handled, "the bar press must start a drag")
			require.Equal(t, 1, b.Tiles(), "the press alone must not split")

			right := zoneAt(winDropRight)
			b.Handle(mouseAt(b, term.MouseLeft, right))
			assert.Equal(t, 2, b.Tiles(), "the hover must pre-split the tile")
			assert.Equal(t, float, b.Focus())

			b.Handle(mouseAt(b, term.MouseRelease, right))
			assert.Equal(t, 2, b.Tiles())
			assert.Equal(t, 0, b.FloatingWindows())
			require.Len(t, b.buffers, 1)
			assert.Equal(t, content, b.buffers[0].Handler())
			win, ok := b.buffers[0].Window()
			require.True(t, ok)
			assert.Greater(t, win.Position().X, tile.Position().X)
			assert.Equal(t, winDropNone, b.winDrop.zone)
		})

	t.Run("dragging to the dead zone leaves a plain window move",
		func(t *testing.T) {
			b, _, float, zoneAt := framedDragBrowser(t, 60, 20)
			bar := float.Position()

			b.Handle(mouseAt(b, term.MouseLeft,
				term.Coordinates{X: bar.X + 4, Y: bar.Y}))
			b.Handle(mouseAt(b, term.MouseLeft, zoneAt(winDropLeft)))
			require.Equal(t, 2, b.Tiles())
			b.Handle(mouseAt(b, term.MouseLeft, zoneAt(winDropNone)))
			assert.Equal(t, 1, b.Tiles(), "leaving the zones removes the preview")

			b.Handle(mouseAt(b, term.MouseRelease, zoneAt(winDropNone)))
			assert.Equal(t, 1, b.Tiles())
			assert.Equal(t, 1, b.FloatingWindows(), "the float survives")
			assert.Empty(t, b.buffers)
			assert.NotEqual(t, bar, float.Position(), "the float was moved")
		})

	t.Run("an interrupted drag restores the layout", func(t *testing.T) {
		b, _, float, zoneAt := framedDragBrowser(t, 60, 20)
		bar := float.Position()

		b.Handle(mouseAt(b, term.MouseLeft, term.Coordinates{X: bar.X + 4, Y: bar.Y}))
		b.Handle(mouseAt(b, term.MouseLeft, zoneAt(winDropTop)))
		require.Equal(t, 2, b.Tiles())

		b.Handle(mouseAt(b, term.MouseWheelUp, zoneAt(winDropTop)))
		assert.Equal(t, 1, b.Tiles(), "the interrupted drag removes the preview")
		assert.Equal(t, 1, b.FloatingWindows())
		assert.Equal(t, float, b.Focus())
		assert.Equal(t, winDropNone, b.winDrop.zone)
	})

	t.Run("closing the float mid-drag restores the layout", func(t *testing.T) {
		b, _, float, zoneAt := framedDragBrowser(t, 60, 20)
		bar := float.Position()

		b.Handle(mouseAt(b, term.MouseLeft, term.Coordinates{X: bar.X + 4, Y: bar.Y}))
		b.Handle(mouseAt(b, term.MouseLeft, zoneAt(winDropBottom)))
		require.Equal(t, 2, b.Tiles())

		require.NoError(t, float.Close())
		b.Handle(mouseAt(b, term.MouseLeft, zoneAt(winDropBottom)))
		assert.Equal(t, 1, b.Tiles(), "the cancelled drag removes the preview")
		assert.Equal(t, winDropNone, b.winDrop.zone)
	})

	t.Run("the close icon still closes instead of dragging", func(t *testing.T) {
		b, _, float, _ := framedDragBrowser(t, 60, 20)
		bar := float.Position()
		b.Handle(mouseAt(b, term.MouseLeft, term.Coordinates{
			X: bar.X + tcomponent.WindowBarCloseIconX, Y: bar.Y,
		}))
		assert.Equal(t, 0, b.FloatingWindows())
		assert.Equal(t, 1, b.Tiles())
		assert.Equal(t, winDropNone, b.winDrop.zone)
		assert.Empty(t, b.buffers, "closing a float creates no tab")
	})

	t.Run("a click on the bar is not a drop", func(t *testing.T) {
		b, _, float, _ := framedDragBrowser(t, 60, 20)
		bar := float.Position()
		press := term.Coordinates{X: bar.X + 4, Y: bar.Y}

		b.Handle(mouseAt(b, term.MouseLeft, press))
		b.Handle(mouseAt(b, term.MouseRelease, press))

		assert.Equal(t, 1, b.Tiles())
		assert.Equal(t, 1, b.FloatingWindows(), "the float survives a click")
		assert.Empty(t, b.buffers, "a click creates no tab")
		assert.Equal(t, bar, float.Position(), "a click must not move the float")
		assert.Equal(t, winDropNone, b.winDrop.zone)
	})
}

// complexDragBrowser returns a browser with a deliberately awkward
// tiled layout - three vertical siblings, the last one split
// horizontally - plus a focused float. Splitting any of the vertical
// siblings inserts a fourth sibling, which redistributes all of them.
// A framed browser gives the float a window bar, which is what mouse
// events need to start a drag.
func complexDragBrowser(t *testing.T, width, height int, framed bool) (
	b *Component, float Window,
) {
	t.Helper()
	cfg := dragConfig()
	if framed {
		cfg.Frame = true
		cfg.WindowBar = true
	}
	b = NewComponent(cfg)
	first := b.Focus()
	require.NoError(t, first.SetContent(newTestHandler()))
	second, ok := b.Split(browserapi.OrientationRight, first, newTestHandler())
	require.True(t, ok)
	third, ok := b.Split(browserapi.OrientationRight, second, newTestHandler())
	require.True(t, ok)
	_, ok = b.Split(browserapi.OrientationBottom, third, newTestHandler())
	require.True(t, ok)
	b.Resize(width, height)

	float = b.Floating(newTestHandler(), browserapi.FloatingConfig{Title: "float"})
	b.Resize(width, height)
	b.Draw(term.NewStringWriter(width, height))
	require.Equal(t, float, b.Focus())
	require.Equal(t, 4, b.Tiles())
	require.Equal(t, framed, float.(*browserWindow).win.HasWindowBar())
	return b, float
}

// assertWinDropInvariants checks what must hold between two drag events,
// independently of which zone the cursor resolved to.
func assertWinDropInvariants(t *testing.T, b *Component, float Window, baseTiles int) {
	t.Helper()
	st := b.winDrop

	if st.target != nil {
		assert.False(t, st.target.Closed(), "the drop target must be live")
	}
	if st.preview != nil {
		assert.False(t, st.preview.Closed(), "the placeholder must be live")
	}
	switch st.zone {
	case winDropNone:
		assert.Nil(t, st.target, "no zone means no target")
		assert.Nil(t, st.preview, "no zone means no placeholder")
		assert.Equal(t, baseTiles, b.Tiles(), "no zone must not change the layout")
	case winDropCenter:
		assert.NotNil(t, st.target)
		assert.Nil(t, st.preview, "a center preview never splits")
		assert.Equal(t, baseTiles, b.Tiles(), "a center preview never splits")
	default:
		assert.NotNil(t, st.target)
		assert.NotNil(t, st.preview, "an edge preview must install a placeholder")
		assert.Equal(t, baseTiles+1, b.Tiles(),
			"an edge preview installs exactly one placeholder")
	}
	if st.zone != winDropNone {
		assert.Same(t, float.(*browserWindow), st.src)
		assert.Equal(t, float, b.Focus(),
			"the dragged float keeps focus while previewing")
	}
	for id, win := range b.windows {
		assert.False(t, win.Closed(), "window %d is closed but still registered", id)
	}
}

// TestWinDropSweepNeverCorruptsLayout drags the float's bar across every
// cell of a complex layout. The single-tile fixtures used by the other
// tests cannot reach the sibling redistribution that splitting one of
// several siblings triggers, which is where the layout and the recorded
// pre-split geometry drift apart.
func TestWinDropSweepNeverCorruptsLayout(t *testing.T) {
	const width, height = 60, 20
	b, float := complexDragBrowser(t, width, height, false)
	base := b.Tiles()

	for y := range height {
		for x := range width {
			at := term.Coordinates{X: x, Y: y}
			barDrag(b, float, at)
			assertWinDropInvariants(t, b, float, base)
			if t.Failed() {
				t.Fatalf("invariant broken while hovering %v", at)
			}
		}
	}

	b.OnBarDragCancel(float.(*browserWindow).win)
	assert.Equal(t, base, b.Tiles(), "the sweep must leave the layout as it found it")
	assert.Equal(t, float, b.Focus())
}

// TestWinDropSweepWithRelayout repeats the sweep while the surface is
// resized between events, so the geometry recorded when a placeholder
// was installed is stale by the time the next event arrives.
func TestWinDropSweepWithRelayout(t *testing.T) {
	b, float := complexDragBrowser(t, 60, 20, false)
	base := b.Tiles()

	sizes := []term.Coordinates{{X: 60, Y: 20}, {X: 120, Y: 20}, {X: 40, Y: 30}}
	for i, size := range sizes {
		b.Resize(size.X, size.Y)
		for y := 0; y < size.Y; y += 3 {
			for x := 0; x < size.X; x += 3 {
				at := term.Coordinates{X: x, Y: y}
				barDrag(b, float, at)
				assertWinDropInvariants(t, b, float, base)
				if t.Failed() {
					t.Fatalf("invariant broken at size %v hovering %v", size, at)
				}
			}
			// a relayout between two drag events, as a terminal
			// resize or a background window change would produce
			b.Resize(sizes[(i+1)%len(sizes)].X, sizes[(i+1)%len(sizes)].Y)
			b.Resize(size.X, size.Y)
		}
	}

	b.OnBarDragCancel(float.(*browserWindow).win)
	assert.Equal(t, base, b.Tiles())
}

// TestWinDropMouseSweep repeats the sweep through the runtime's own
// path - a press on the bar followed by mouse moves - so the window
// manager wiring, which the direct hook calls bypass entirely, is
// exercised over the whole surface.
func TestWinDropMouseSweep(t *testing.T) {
	const width, height = 45, 15
	b, float := complexDragBrowser(t, width, height, true)
	base := b.Tiles()

	bar := float.Position()
	_, handled := b.Handle(mouseAt(b, term.MouseLeft,
		term.Coordinates{X: bar.X + 4, Y: bar.Y}))
	require.True(t, handled, "the bar press must start a drag")

	for y := range height {
		for x := range width {
			at := term.Coordinates{X: x, Y: y}
			b.Handle(mouseAt(b, term.MouseLeft, at))
			assertWinDropInvariants(t, b, float, base)
			if t.Failed() {
				t.Fatalf("invariant broken while dragging over %v", at)
			}
		}
	}

	b.Handle(mouseAt(b, term.MouseWheelUp, term.Coordinates{X: 1, Y: 1}))
	assert.Equal(t, base, b.Tiles(), "the cancelled drag restores the layout")
	assert.Equal(t, 1, b.FloatingWindows())
}

// TestWinDropSweepDropsEverywhere releases the drag on every cell of a
// complex layout, each time on a fresh browser, and checks the drop
// leaves consistent tab and window bookkeeping behind.
func TestWinDropSweepDropsEverywhere(t *testing.T) {
	const width, height = 45, 15
	for y := 0; y < height; y += 2 {
		for x := 0; x < width; x += 2 {
			at := term.Coordinates{X: x, Y: y}
			b, float := complexDragBrowser(t, width, height, false)
			base := b.Tiles()

			barDrag(b, float, at)
			zone := b.winDrop.zone
			handled := b.OnBarDrop(float.(*browserWindow).win, at)

			if !handled {
				require.Equal(t, winDropNone, zone,
					"only a dead-zone release may decline the drop at %v", at)
				assert.Equal(t, base, b.Tiles(), "a declined drop keeps the layout")
				assert.Equal(t, 1, b.FloatingWindows(), "a declined drop keeps the float")
				assert.Empty(t, b.buffers, "a declined drop creates no tab")
				continue
			}

			require.NotEqual(t, winDropNone, zone)
			assert.Equal(t, 0, b.FloatingWindows(), "a drop closes the float")
			assert.True(t, float.Closed())
			require.Len(t, b.buffers, 1, "a drop creates exactly one tab")
			if zone == winDropCenter {
				assert.Equal(t, base, b.Tiles(), "a center drop replaces content")
			} else {
				assert.Equal(t, base+1, b.Tiles(), "an edge drop keeps the split")
			}

			win, ok := b.buffers[0].Window()
			require.True(t, ok, "the dropped tab must be bound to a window at %v", at)
			assert.Equal(t, win, b.Focus(), "the drop focuses the tab's window")
			assert.Equal(t, winDropNone, b.winDrop.zone, "the drop clears the preview")
			for id, w := range b.windows {
				assert.False(t, w.Closed(), "window %d is closed but still registered", id)
			}
			if t.Failed() {
				t.Fatalf("drop at %v (zone %d) left inconsistent state", at, zone)
			}
		}
	}
}

func TestWinDropPreviewLifecycle(t *testing.T) {
	t.Run("hovering an edge pre-splits once and keeps focus on the float",
		func(t *testing.T) {
			b, tile, float, zoneAt := winDragBrowser(t, 60, 20)
			tileX := tile.Position().X
			require.Equal(t, 1, b.Tiles())

			barDrag(b, float, zoneAt(winDropLeft))
			assert.Equal(t, 2, b.Tiles(), "the edge hover must pre-split the tile")
			assert.Equal(t, float, b.Focus(),
				"the dragged float must keep focus during the preview")
			require.NotNil(t, b.winDrop.preview)
			assert.Equal(t, tileX, b.winDrop.preview.Position().X,
				"a left drop previews the tile left of the target")
			assert.Greater(t, tile.Position().X, tileX,
				"the split target moves right of the placeholder")

			// a second move inside the same zone must not split again
			pos := zoneAt(winDropLeft)
			pos.Y++
			barDrag(b, float, pos)
			assert.Equal(t, 2, b.Tiles())
			assert.Equal(t, winDropLeft, b.winDrop.zone)
		})

	t.Run("moving to another edge replaces the preview", func(t *testing.T) {
		b, _, float, zoneAt := winDragBrowser(t, 60, 20)
		barDrag(b, float, zoneAt(winDropLeft))
		first := b.winDrop.preview
		require.NotNil(t, first)

		barDrag(b, float, zoneAt(winDropRight))
		assert.Equal(t, 2, b.Tiles(), "exactly one placeholder may exist")
		require.NotNil(t, b.winDrop.preview)
		assert.True(t, first.Closed(), "the stale placeholder must be closed")
		assert.Equal(t, winDropRight, b.winDrop.zone)
		assert.Equal(t, float, b.Focus())
	})

	t.Run("the dead zone removes the preview and restores the layout",
		func(t *testing.T) {
			b, tile, float, zoneAt := winDragBrowser(t, 60, 20)
			pos, w, h := tile.Position(), tile.Width(), tile.Height()

			barDrag(b, float, zoneAt(winDropLeft))
			require.Equal(t, 2, b.Tiles())

			barDrag(b, float, zoneAt(winDropNone))
			assert.Equal(t, 1, b.Tiles())
			assert.Equal(t, winDropNone, b.winDrop.zone)
			b.Resize(60, 20)
			assert.Equal(t, pos, tile.Position())
			assert.Equal(t, w, tile.Width())
			assert.Equal(t, h, tile.Height())
			assert.Equal(t, float, b.Focus())
		})

	t.Run("the center box previews without splitting", func(t *testing.T) {
		b, _, float, zoneAt := winDragBrowser(t, 60, 20)
		barDrag(b, float, zoneAt(winDropCenter))
		assert.Equal(t, 1, b.Tiles(), "a center drop replaces, it does not split")
		assert.Equal(t, winDropCenter, b.winDrop.zone)
		assert.Nil(t, b.winDrop.preview)
	})

	t.Run("cancelling tears the preview down", func(t *testing.T) {
		b, _, float, zoneAt := winDragBrowser(t, 60, 20)
		barDrag(b, float, zoneAt(winDropTop))
		require.Equal(t, 2, b.Tiles())

		b.OnBarDragCancel(float.(*browserWindow).win)
		assert.Equal(t, 1, b.Tiles())
		assert.Equal(t, winDropNone, b.winDrop.zone)
		assert.Equal(t, float, b.Focus())
	})

	// Inserting a placeholder as a new sibling redistributes every
	// tile in the parent, so the placeholder can end up straddling the
	// pre-split rect. Hovering the part that sticks out used to
	// resolve the placeholder itself as the next split target, which
	// then split a window the teardown had just closed.
	t.Run("hovering the placeholder outside the pre-split rect keeps the target",
		func(t *testing.T) {
			b, float, cWin := threeTileDragBrowser(t)

			edge := term.Coordinates{
				X: cWin.Position().X,
				Y: cWin.Position().Y + cWin.Height()/2,
			}
			barDrag(b, float, edge)
			require.Equal(t, 4, b.Tiles())
			preview := b.winDrop.preview
			require.NotNil(t, preview)
			b.Resize(60, 20)
			require.Less(t, preview.Position().X, edge.X,
				"the placeholder must stick out of the pre-split rect")

			over := term.Coordinates{X: preview.Position().X, Y: edge.Y}
			barDrag(b, float, over)
			assert.Equal(t, 4, b.Tiles())
			assert.Same(t, preview, b.winDrop.preview,
				"hovering the placeholder must not rebuild the preview")
			assert.Equal(t, cWin, Window(b.winDrop.target))
		})

	// Any relayout between two drag events - a terminal resize here,
	// but equally a window opening or closing - moves the placeholder,
	// so the previewed region has to be measured live.
	t.Run("a relayout under a live preview keeps the same placeholder",
		func(t *testing.T) {
			b, float, cWin := threeTileDragBrowser(t)

			edge := term.Coordinates{
				X: cWin.Position().X,
				Y: cWin.Position().Y + cWin.Height()/2,
			}
			barDrag(b, float, edge)
			preview := b.winDrop.preview
			require.NotNil(t, preview)

			b.Resize(120, 20)
			over := term.Coordinates{
				X: preview.Position().X,
				Y: preview.Position().Y + preview.Height()/2,
			}
			barDrag(b, float, over)
			assert.Same(t, preview, b.winDrop.preview,
				"hovering the placeholder must not rebuild the preview")
			assert.Equal(t, cWin, Window(b.winDrop.target))
			assert.False(t, b.winDrop.target.Closed(),
				"the preview must never target a closed window")

			assert.True(t, b.OnBarDrop(float.(*browserWindow).win, over),
				"releasing on the placeholder must complete the drop")
		})
}

// threeTileDragBrowser returns a browser with three side-by-side tiles
// and a focused floating window. Splitting the last tile inserts the
// placeholder as a fourth sibling, which redistributes all of them.
func threeTileDragBrowser(t *testing.T) (b *Component, float, last Window) {
	t.Helper()
	b = NewComponent(dragConfig())
	first := b.Focus()
	require.NoError(t, first.SetContent(newTestHandler()))
	_, ok := b.Split(browserapi.OrientationRight, first, newTestHandler())
	require.True(t, ok)
	last, ok = b.Split(browserapi.OrientationRight, b.Focus(), newTestHandler())
	require.True(t, ok)
	b.Resize(60, 20)

	float = b.Floating(newTestHandler(), browserapi.FloatingConfig{})
	b.Resize(60, 20)
	require.Equal(t, 3, b.Tiles())
	return b, float, last
}

func TestWinDropVeilLabels(t *testing.T) {
	b, _, float, zoneAt := winDragBrowser(t, 60, 20)

	barDrag(b, float, zoneAt(winDropBottom))
	assert.Contains(t, drawString(b, 60, 20), winDropSplitLabel)

	barDrag(b, float, zoneAt(winDropCenter))
	assert.Contains(t, drawString(b, 60, 20), winDropConvertLabel)

	b.OnBarDragCancel(float.(*browserWindow).win)
	out := drawString(b, 60, 20)
	assert.NotContains(t, out, winDropSplitLabel)
	assert.NotContains(t, out, winDropConvertLabel)
}

func TestWinDrop(t *testing.T) {
	t.Run("an edge drop splits and installs a tab", func(t *testing.T) {
		b, tile, float, zoneAt := winDragBrowser(t, 60, 20)
		tileX := tile.Position().X
		content, err := float.Content()
		require.NoError(t, err)

		pos := zoneAt(winDropRight)
		barDrag(b, float, pos)
		require.True(t, b.OnBarDrop(float.(*browserWindow).win, pos))

		assert.Equal(t, 2, b.Tiles())
		assert.Equal(t, 0, b.FloatingWindows(), "the float is closed on drop")
		require.Len(t, b.buffers, 1)
		tab := b.buffers[0]
		assert.Equal(t, content, tab.Handler())

		win, ok := tab.Window()
		require.True(t, ok)
		assert.Equal(t, win, b.Focus())
		assert.Greater(t, win.Position().X, tileX,
			"the tab lands in the tile right of the drop target")
	})

	t.Run("a center drop replaces the target's content", func(t *testing.T) {
		b, tile, float, zoneAt := winDragBrowser(t, 60, 20)
		content, err := float.Content()
		require.NoError(t, err)

		pos := zoneAt(winDropCenter)
		barDrag(b, float, pos)
		require.True(t, b.OnBarDrop(float.(*browserWindow).win, pos))

		assert.Equal(t, 1, b.Tiles(), "a center drop must not split")
		assert.Equal(t, 0, b.FloatingWindows())
		require.Len(t, b.buffers, 1)
		assert.Equal(t, content, b.buffers[0].Handler())
		win, ok := b.buffers[0].Window()
		require.True(t, ok)
		assert.Equal(t, tile, win)
		assert.Equal(t, tile, b.Focus())
	})

	t.Run("a dead-zone drop leaves the layout untouched", func(t *testing.T) {
		b, _, float, zoneAt := winDragBrowser(t, 60, 20)

		pos := zoneAt(winDropNone)
		barDrag(b, float, pos)
		assert.False(t, b.OnBarDrop(float.(*browserWindow).win, pos))

		assert.Equal(t, 1, b.Tiles())
		assert.Equal(t, 1, b.FloatingWindows(), "the float survives a plain move")
		assert.Empty(t, b.buffers)
	})

	t.Run("an existing tab is moved rather than duplicated", func(t *testing.T) {
		b, tile, _, zoneAt := winDragBrowser(t, 60, 20)
		h := newTestHandler()
		uri, err := workspaceapi.ParseURI("file:///moved.txt")
		require.NoError(t, err)
		tab := b.NewTab(uri, 'm', "moved.txt", h, nil)
		float := b.Floating(newTestHandler(), browserapi.FloatingConfig{})
		require.NoError(t, float.SetContent(tab))
		b.Resize(60, 20)

		pos := zoneAt(winDropCenter)
		barDrag(b, float, pos)
		require.True(t, b.OnBarDrop(float.(*browserWindow).win, pos))

		assert.Len(t, b.buffers, 1, "the tab must not be duplicated")
		got, err := tile.Content()
		require.NoError(t, err)
		assert.Equal(t, tab, got)
	})
}

func TestWinDropTabName(t *testing.T) {
	t.Run("the bar title names the tab", func(t *testing.T) {
		b, _, _, zoneAt := winDragBrowser(t, 60, 20)
		titled := b.Floating(newTestHandler(), browserapi.FloatingConfig{Title: "shell"})
		b.Resize(60, 20)

		pos := zoneAt(winDropCenter)
		barDrag(b, titled, pos)
		require.True(t, b.OnBarDrop(titled.(*browserWindow).win, pos))

		require.Len(t, b.buffers, 1)
		name, _, ok := b.TabName(b.buffers[0].URI())
		require.True(t, ok)
		assert.Equal(t, "shell", name)
	})
	t.Run("an untitled float falls back to the resource basename",
		func(t *testing.T) {
			b, _, _, zoneAt := winDragBrowser(t, 60, 20)
			uri, err := workspaceapi.ParseURI("file:///tmp/notes.md")
			require.NoError(t, err)
			float := b.Floating(newTestHandlerURI(uri), browserapi.FloatingConfig{})
			b.Resize(60, 20)

			pos := zoneAt(winDropCenter)
			barDrag(b, float, pos)
			require.True(t, b.OnBarDrop(float.(*browserWindow).win, pos))

			require.Len(t, b.buffers, 1)
			name, _, ok := b.TabName(b.buffers[0].URI())
			require.True(t, ok)
			assert.Equal(t, "notes.md", name)
		})
}
