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
	"context"
	"fmt"
	"image"
	"runtime"
	"slices"
	"strings"
	"testing"
	"weak"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	tcomponent "unstable.build/rune/internal/component"
	thandler "unstable.build/rune/internal/handler"
	rhandlertest "unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/term/programstatus"
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

	t.Run("does nothing if content is the wallpaper", func(t *testing.T) {
		cases := []struct {
			name string
			win  func(t *testing.T, b *Component) Window
		}{
			{"initial window", func(_ *testing.T, b *Component) Window {
				return b.Focus()
			}},
			{"split without content", func(t *testing.T, b *Component) Window {
				win, ok := b.Split(browserapi.OrientationRight, b.Focus(), nil)
				require.True(t, ok)
				return win
			}},
			{"last tab removed", func(t *testing.T, b *Component) Window {
				h := newTestHandler()
				tab := b.NewTab(workspaceapi.URI{}, 'o', "d", h, h)
				require.NoError(t, b.Focus().SetContent(tab))
				require.False(t, b.RemoveWindowContent(b.Focus()))
				return b.Focus()
			}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				b := NewComponent(DefaultConfig())
				win := tc.win(t, b)

				tab, ok := b.NewTabFromContent('o', "tab", win)
				assert.False(t, ok)
				assert.Nil(t, tab)
				assert.Empty(t, b.Tabs())
			})
		}
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

func TestSetTabIconAttr(t *testing.T) {
	const offset = term.AttrNegativeVerticalRenderOffset
	focusIconAttr := term.Attributes{Bg: term.ColorGreen, Attrs: term.AttrBold}
	red := term.Attributes{Fg: term.ColorRed}

	suite := []struct {
		desc string
		// override is set on tab a before the first draw, unless nil.
		override *term.Attributes
		// focusA moves focus from b's window to a's before drawing.
		focusA  bool
		wantA   term.Attributes
		wantB   term.Attributes
		unknown bool
	}{
		{
			desc:  "no override",
			wantA: term.Attributes{Attrs: offset},
			wantB: term.Attributes{Bg: term.ColorGreen, Attrs: term.AttrBold | offset},
		},
		{
			desc:     "override on an unfocused tab",
			override: &red,
			wantA:    term.Attributes{Fg: term.ColorRed, Attrs: offset},
			wantB:    term.Attributes{Bg: term.ColorGreen, Attrs: term.AttrBold | offset},
		},
		{
			desc:     "override survives the tab gaining focus",
			override: &red,
			focusA:   true,
			wantA: term.Attributes{
				Fg: term.ColorRed, Bg: term.ColorGreen, Attrs: term.AttrBold | offset,
			},
			wantB: term.Attributes{Attrs: offset},
		},
		{
			desc:     "override colors replace the configured ones",
			override: &term.Attributes{Bg: term.ColorBlue},
			focusA:   true,
			wantA:    term.Attributes{Bg: term.ColorBlue, Attrs: term.AttrBold | offset},
			wantB:    term.Attributes{Attrs: offset},
		},
		{
			desc:     "override flags add to the configured ones",
			override: &term.Attributes{Attrs: term.AttrUnderline},
			focusA:   true,
			wantA: term.Attributes{
				Bg: term.ColorGreen, Attrs: term.AttrBold | term.AttrUnderline | offset,
			},
			wantB: term.Attributes{Attrs: offset},
		},
		{
			desc:     "unknown tab",
			override: &red,
			unknown:  true,
			wantA:    term.Attributes{Attrs: offset},
			wantB:    term.Attributes{Bg: term.ColorGreen, Attrs: term.AttrBold | offset},
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Frame = false
			cfg.FrameUnion = false
			cfg.Dim = false
			cfg.FocusTabIconAttr = focusIconAttr
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
			draw := func() []term.Cell {
				w := term.NewStringWriter(width, height)
				b.Draw(w)
				return w.Cells()
			}
			// A first draw settles the focus attributes the override
			// must then be layered over.
			draw()

			if tc.override != nil {
				target := uriA
				if tc.unknown {
					target, err = workspaceapi.ParseURI("file:///missing")
					require.NoError(t, err)
				}
				assert.Equal(t, !tc.unknown, b.SetTabIconAttr(target, *tc.override))
			}
			if tc.focusA {
				require.True(t, b.FocusLeft())
			}

			cells := draw()
			require.Equal(t, 'A', cells[0].Ch)
			assert.Equal(t, tc.wantA, cells[0].Attributes())
			require.Equal(t, 'B', cells[5].Ch)
			assert.Equal(t, tc.wantB, cells[5].Attributes())
		})
	}
}

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

func TestComponentRestoreTileLayoutFreesDisplayedTabs(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	b := NewComponent(cfg)
	b.Resize(40, 10)

	uri, err := workspaceapi.ParseURI("file:///displayed")
	require.NoError(t, err)
	h := newTestHandlerURI(uri)
	tab := b.NewTab(uri, 'd', "displayed", h, nil)
	tab.Subscribe(h)
	discarded := b.Focus()
	require.NoError(t, discarded.SetContent(tab))
	freed := h.onFree

	restored := b.RestoreTileLayout(b.TileLayout(),
		func(uint64) browserapi.Handler { return nil })
	require.Len(t, restored, 1)

	assert.True(t, discarded.Closed(),
		"a window discarded by the restore must report closed")
	_, bound := tab.Window()
	assert.False(t, bound,
		"a tab whose window was discarded by the restore must be free")
	assert.Equal(t, freed+1, h.onFree)
	assert.False(t, h.closed,
		"restoring a layout must not close the handler of an open tab")

	id, ok := b.findTabID(tab)
	require.True(t, ok, "tab must still be present in the bar")
	require.True(t, b.tabs.OnClick(id))
	require.NotPanics(t, func() { b.Focus() })
	assert.Equal(t, restored[discarded.WindowID()], b.Focus())
	focused, ok := b.FocusTab()
	require.True(t, ok)
	assert.Equal(t, tab, focused,
		"clicking the freed tab must load it into the focused window")
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

func TestWindowManagerCloseDropsWindow(t *testing.T) {
	esc := term.Event{Type: term.EventKey, Key: term.KeyEsc}
	tests := []struct {
		name string
		open func(t *testing.T, b *Component) Window
	}{
		{"floating content exits", func(t *testing.T, b *Component) Window {
			h := newTestHandler()
			h.Exit = true
			return b.Floating(h, browserapi.FloatingConfig{})
		}},
		{"tiled content exits", func(t *testing.T, b *Component) Window {
			h := newTestHandler()
			h.Exit = true
			win, ok := b.Split(browserapi.OrientationLeft, b.Focus(), h)
			require.True(t, ok)
			return win
		}},
		{"prompt is dismissed", func(t *testing.T, b *Component) Window {
			return b.Prompt("Proceed?", []string{"Yes", "No"}, nil, handler.NopPromptHandler())
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := NewComponent(DefaultConfig())
			b.Resize(60, 20)
			windows := len(b.windows)
			ref := weak.Make(tt.open(t, b).(*browserWindow))
			require.Equal(t, ref.Value(), b.Focus(), "the new window must take the focus")

			b.Handle(esc)

			assert.Len(t, b.windows, windows,
				"a window the window manager closed must leave the browser's bookkeeping")
			runtime.GC()
			runtime.GC()
			assert.Nil(t, ref.Value(), "a closed window must not stay reachable")
			runtime.KeepAlive(b)
		})
	}
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

func TestComponentContentSize(t *testing.T) {
	b := NewComponent(DefaultConfig())
	t.Cleanup(func() { require.NoError(t, b.Close()) })
	b.Resize(40, 20)
	other := NewComponent(DefaultConfig())
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	other.Resize(40, 20)

	raw := b.Focus()
	require.NoError(t, raw.SetContent(newTestHandler()))
	b.Resize(40, 20)
	w, h := b.ContentSize(raw)
	assert.Positive(t, w, "the content of a window has the size it was given")
	assert.Positive(t, h)
	assert.True(t, b.Owns(raw))
	assert.False(t, other.Owns(raw), "a window of another browser")
	w, h = other.ContentSize(raw)
	assert.Zero(t, w+h)

	tab := b.NewTab(workspaceapi.URI{}, 'x', "tab", newTestHandler(), nil)
	split, ok := b.Split(browserapi.OrientationDefault, raw, newTestHandler())
	require.True(t, ok)
	require.True(t, b.SetContentToTab(split, slices.Index(b.Tabs(), tab)))
	tabW, tabH := tab.ContentSize()
	require.Positive(t, tabW)
	w, h = b.ContentSize(split)
	assert.Equal(t, []int{tabW, tabH}, []int{w, h}, "a tab has its own size")

	require.NoError(t, split.Close())
	assert.False(t, b.Owns(split), "a closed window")
	w, h = b.ContentSize(split)
	assert.Zero(t, w+h)
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
	if onActivity != nil {
		cfg.OnTabStatus = func(workspaceapi.URI, programstatus.Summary, programstatus.Summary) {
			onActivity()
		}
	}
	cfg.Interrupter = term.NopInterrupter()
	b := NewComponent(cfg)
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

func TestSetTabStatus(t *testing.T) {
	var calls int
	b, tabs := newActivityComponent(t, func() { calls++ })
	working := programstatus.Report{State: programstatus.Working, Progress: -1}
	clearAll := programstatus.Report{State: programstatus.Clear}

	unknown, err := workspaceapi.ParseURI("file:///unknown")
	require.NoError(t, err)
	assert.False(t, b.SetTabStatus(unknown, working))
	assert.False(t, b.HasWorkingTabs())
	assert.False(t, b.shadedTabs.Running())
	assert.Zero(t, calls)

	require.True(t, b.SetTabStatus(tabs[1].uri, working))
	assert.True(t, b.HasWorkingTabs())
	assert.True(t, b.shadedTabs.Running())
	assert.Equal(t, []int{1}, b.activeTabIndices())
	assert.Equal(t, 1, calls)

	require.True(t, b.SetTabStatus(tabs[1].uri, working))
	assert.Equal(t, 1, calls, "reporting the current state again is a no-op")

	require.True(t, b.SetTabStatus(tabs[2].uri, working))
	assert.Equal(t, []int{1, 2}, b.activeTabIndices())

	require.True(t, b.SetTabStatus(tabs[1].uri, clearAll))
	assert.True(t, b.shadedTabs.Running(), "another tab is still active")
	require.True(t, b.SetTabStatus(tabs[2].uri, clearAll))
	assert.False(t, b.HasWorkingTabs())
	assert.False(t, b.shadedTabs.Running())
	assert.Equal(t, 4, calls)
}

func TestTabStatusFollowsMovedTab(t *testing.T) {
	b, tabs := newActivityComponent(t, nil)
	working := programstatus.Report{State: programstatus.Working, Progress: -1}
	require.True(t, b.SetTabStatus(tabs[0].uri, working))
	require.True(t, b.SetContentToTab(b.Focus(), 0))
	require.NoError(t, b.MoveTabRight(b.Focus()))
	assertTabNames(t, b, []string{"b", "a", "c"})
	assert.Equal(t, []int{1}, b.activeTabIndices())
}

func TestOnTabStatusReportsTransitions(t *testing.T) {
	type change struct {
		uri          workspaceapi.URI
		prev, status programstatus.State
		msg          string
	}
	var changes []change
	cfg := DefaultConfig()
	cfg.OnTabStatus = func(uri workspaceapi.URI, prev, status programstatus.Summary) {
		changes = append(changes, change{uri, prev.State, status.State, status.Msg})
	}
	b := NewComponent(cfg)
	t.Cleanup(func() { _ = b.Close() })
	uri, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	h := newTestHandler()
	tab := b.NewTab(uri, 'o', "a", h, h)

	working := programstatus.Report{State: programstatus.Working, Progress: -1}
	done := programstatus.Report{State: programstatus.Done, Progress: -1, Msg: "ok"}
	require.True(t, b.SetTabStatus(uri, working))
	require.True(t, b.SetTabStatus(uri, working))
	require.True(t, b.SetTabStatus(uri, done))
	require.True(t, b.RemoveTab(tab))
	assert.Equal(t, []change{
		{uri, programstatus.Idle, programstatus.Working, ""},
		{uri, programstatus.Working, programstatus.Done, "ok"},
		{uri, programstatus.Done, programstatus.Idle, ""},
	}, changes)
}

func TestOnTabRemoved(t *testing.T) {
	// Every tab of the fixture is in a different place: "focus" in the
	// tile in focus, "split" in another tile, "float" in a floating
	// window, and "free1" and "free2" in no window.
	type fixture struct {
		b        *Component
		tabs     map[string]*Tab
		handlers map[string]*nopHandler
		wins     map[string]Window
		events   *[]string
	}
	names := []string{"focus", "split", "float", "free1", "free2"}
	name := func(uri workspaceapi.URI) string { return strings.TrimPrefix(uri.String(), "file:///") }
	setup := func(t *testing.T, reporting bool) fixture {
		events := new([]string)
		cfg := DefaultConfig()
		cfg.OnTabStatus = func(uri workspaceapi.URI, _, status programstatus.Summary) {
			*events = append(*events, "status "+name(uri)+" "+status.State.String())
		}
		cfg.OnTabRemoved = func(uri workspaceapi.URI) {
			*events = append(*events, "removed "+name(uri))
		}
		cfg.Interrupter = term.NopInterrupter()
		b := NewComponent(cfg)
		b.Resize(80, 24)
		t.Cleanup(func() { _ = b.Close() })
		f := fixture{
			b: b, tabs: map[string]*Tab{}, handlers: map[string]*nopHandler{},
			wins: map[string]Window{}, events: events,
		}
		for _, n := range names {
			uri, err := workspaceapi.ParseURI("file:///" + n)
			require.NoError(t, err)
			f.handlers[n] = newTestHandler()
			f.tabs[n] = b.NewTab(uri, 'o', n, f.handlers[n], f.handlers[n])
		}
		f.wins["focus"] = b.Focus()
		require.NoError(t, f.wins["focus"].SetContent(f.tabs["focus"]))
		var ok bool
		f.wins["split"], ok = b.Split(browserapi.OrientationRight, f.wins["focus"], f.tabs["split"])
		require.True(t, ok)
		f.wins["float"] = b.Floating(newTestHandler(), browserapi.FloatingConfig{})
		require.NoError(t, f.wins["float"].SetContent(f.tabs["float"]))
		b.SetFocus(f.wins["focus"])
		if reporting {
			for _, n := range names {
				// Working, as being seen does not drop it: a tab coming in
				// sight as another goes reports no change.
				require.True(t, b.SetTabStatus(f.tabs[n].URI(),
					programstatus.Report{State: programstatus.Working, Progress: -1}))
			}
		}
		*events = nil
		return f
	}
	exit := func(f fixture, n string) {
		f.handlers[n].Exit = true
		f.b.SetFocus(f.wins[n])
		f.b.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	}
	suite := []struct {
		name   string
		remove func(t *testing.T, f fixture)
		// removed lists the tabs removed, in any order.
		removed []string
	}{
		{
			name:    "RemoveTab of a free tab",
			remove:  func(t *testing.T, f fixture) { require.True(t, f.b.RemoveTab(f.tabs["free1"])) },
			removed: []string{"free1"},
		},
		{
			name:    "RemoveTab of the tab in focus",
			remove:  func(t *testing.T, f fixture) { require.True(t, f.b.RemoveTab(f.tabs["focus"])) },
			removed: []string{"focus"},
		},
		{
			name:    "RemoveTab of a tab in a split",
			remove:  func(t *testing.T, f fixture) { require.True(t, f.b.RemoveTab(f.tabs["split"])) },
			removed: []string{"split"},
		},
		{
			name:    "RemoveTab of a tab in a floating window",
			remove:  func(t *testing.T, f fixture) { require.True(t, f.b.RemoveTab(f.tabs["float"])) },
			removed: []string{"float"},
		},
		{
			name: "RemoveTab of every tab, the last ones with no free tab to take their place",
			remove: func(t *testing.T, f fixture) {
				for _, n := range []string{"free1", "free2", "focus", "split", "float"} {
					require.True(t, f.b.RemoveTab(f.tabs[n]), n)
				}
			},
			removed: names,
		},
		{
			name: "RemoveTab of a tab removed already",
			remove: func(t *testing.T, f fixture) {
				require.True(t, f.b.RemoveTab(f.tabs["free1"]))
				require.False(t, f.b.RemoveTab(f.tabs["free1"]))
			},
			removed: []string{"free1"},
		},
		{
			name:   "RemoveTab of a handler that is no tab",
			remove: func(t *testing.T, f fixture) { require.False(t, f.b.RemoveTab(newTestHandler())) },
		},
		{
			name: "RemoveWindowContent of the window in focus",
			remove: func(t *testing.T, f fixture) {
				require.True(t, f.b.RemoveWindowContent(f.wins["focus"]))
			},
			removed: []string{"focus"},
		},
		{
			name: "RemoveWindowContent of a split",
			remove: func(t *testing.T, f fixture) {
				require.True(t, f.b.RemoveWindowContent(f.wins["split"]))
			},
			removed: []string{"split"},
		},
		{
			name: "RemoveWindowContent of a floating window",
			remove: func(t *testing.T, f fixture) {
				require.True(t, f.b.RemoveWindowContent(f.wins["float"]))
			},
			removed: []string{"float"},
		},
		{
			name: "RemoveWindowContent of a window until no tab is left to show",
			remove: func(t *testing.T, f fixture) {
				require.True(t, f.b.RemoveWindowContent(f.wins["focus"]))
				require.True(t, f.b.RemoveWindowContent(f.wins["focus"]))
				require.False(t, f.b.RemoveWindowContent(f.wins["focus"]))
				require.False(t, f.b.RemoveWindowContent(f.wins["focus"]))
			},
			removed: []string{"focus", "free1", "free2"},
		},
		{
			name: "RemoveWindowContent of a window showing no tab",
			remove: func(t *testing.T, f fixture) {
				win := f.b.Floating(newTestHandler(), browserapi.FloatingConfig{})
				require.True(t, f.b.RemoveWindowContent(win))
			},
		},
		{
			name:    "RemoveAllTabs",
			remove:  func(t *testing.T, f fixture) { f.b.RemoveAllTabs() },
			removed: names,
		},
		{
			name:    "RemoveInactiveTabs",
			remove:  func(t *testing.T, f fixture) { require.True(t, f.b.RemoveInactiveTabs()) },
			removed: []string{"free1", "free2"},
		},
		{
			name:    "OnTabExit of a free tab",
			remove:  func(t *testing.T, f fixture) { require.True(t, f.b.OnTabExit(f.tabs["free1"].URI())) },
			removed: []string{"free1"},
		},
		{
			name:    "OnTabExit of the tab in focus",
			remove:  func(t *testing.T, f fixture) { require.True(t, f.b.OnTabExit(f.tabs["focus"].URI())) },
			removed: []string{"focus"},
		},
		{
			name:    "OnTabExit of a tab in a floating window",
			remove:  func(t *testing.T, f fixture) { require.True(t, f.b.OnTabExit(f.tabs["float"].URI())) },
			removed: []string{"float"},
		},
		{
			name: "OnTabExit of a uri no tab is at",
			remove: func(t *testing.T, f fixture) {
				uri, err := workspaceapi.ParseURI("file:///nowhere")
				require.NoError(t, err)
				require.False(t, f.b.OnTabExit(uri))
			},
		},
		{
			name:    "the handler of the tab in focus exiting",
			remove:  func(_ *testing.T, f fixture) { exit(f, "focus") },
			removed: []string{"focus"},
		},
		{
			name:    "the handler of a tab in a split exiting",
			remove:  func(_ *testing.T, f fixture) { exit(f, "split") },
			removed: []string{"split"},
		},
		{
			name:    "the handler of a tab in a floating window exiting",
			remove:  func(_ *testing.T, f fixture) { exit(f, "float") },
			removed: []string{"float"},
		},
		{
			name:   "closing the window of a tab",
			remove: func(t *testing.T, f fixture) { require.NoError(t, f.wins["split"].Close()) },
		},
		{
			name:   "closing the floating window of a tab",
			remove: func(t *testing.T, f fixture) { require.NoError(t, f.wins["float"].Close()) },
		},
		{
			name: "closing the window of a tab from its bar",
			remove: func(t *testing.T, f fixture) {
				require.True(t, f.b.OnBarClose(f.wins["float"].(*browserWindow).win))
			},
		},
		{
			name:   "closing the other windows",
			remove: func(t *testing.T, f fixture) { require.NoError(t, f.b.CloseOtherWindows(f.wins["focus"])) },
		},
		{
			name: "restoring a tile layout",
			remove: func(t *testing.T, f fixture) {
				f.b.RestoreTileLayout(f.b.TileLayout(), func(uint64) browserapi.Handler { return newTestHandler() })
			},
		},
		{
			name: "showing something else in the window of a tab",
			remove: func(t *testing.T, f fixture) {
				require.NoError(t, f.wins["split"].SetContent(newTestHandler()))
			},
		},
		{
			name: "showing another tab in the window of a tab",
			remove: func(t *testing.T, f fixture) {
				require.NoError(t, f.wins["focus"].SetContent(f.tabs["free1"]))
			},
		},
		{
			name:   "moving a tab",
			remove: func(t *testing.T, f fixture) { require.NoError(t, f.b.MoveTabRight(f.wins["focus"])) },
		},
		{
			name: "replacing the handler of a tab",
			remove: func(t *testing.T, f fixture) {
				require.NoError(t, f.tabs["focus"].SetHandler(newTestHandler(), nil))
			},
		},
	}
	for _, tcase := range suite {
		for _, reporting := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, reporting %v", tcase.name, reporting), func(t *testing.T) {
				f := setup(t, reporting)
				tcase.remove(t, f)

				var left []string
				for _, tab := range f.b.Tabs() {
					left = append(left, name(tab.URI()))
				}
				var gone []string
				for _, n := range names {
					if !slices.Contains(left, n) {
						gone = append(gone, n)
					}
				}
				assert.ElementsMatch(t, tcase.removed, gone, "the tabs removed")

				// Each tab is reported removed once, and only then, if it
				// had a status, its status goes.
				var removed, want []string
				for _, ev := range *f.events {
					if n, ok := strings.CutPrefix(ev, "removed "); ok {
						removed = append(removed, n)
						want = append(want, ev)
						if reporting {
							want = append(want, "status "+n+" idle")
						}
					}
				}
				assert.ElementsMatch(t, tcase.removed, removed, "the tabs reported removed")
				assert.Equal(t, want, *f.events)
			})
		}
	}

	t.Run("closing the component reports no tab, as its tabs go along with it", func(t *testing.T) {
		f := setup(t, true)
		require.NoError(t, f.b.Close())
		assert.Empty(t, f.b.Tabs())
		assert.Empty(t, *f.events)
	})
}

func TestRemoveTabClearsStatus(t *testing.T) {
	var calls int
	b, tabs := newActivityComponent(t, func() { calls++ })
	working := programstatus.Report{State: programstatus.Working, Progress: -1}
	require.True(t, b.SetTabStatus(tabs[0].uri, working))
	require.True(t, b.RemoveTab(tabs[0]))
	assert.False(t, b.HasWorkingTabs())
	assert.False(t, b.shadedTabs.Running())
	assert.Equal(t, 2, calls)

	// Removing an idle tab does not report a change.
	require.True(t, b.RemoveTab(tabs[1]))
	assert.Equal(t, 2, calls)

	// A tab whose program reported idle has a status to drop.
	require.True(t, b.SetTabStatus(tabs[2].uri, programstatus.Report{State: programstatus.Idle, Progress: -1}))
	require.Equal(t, 3, calls)
	require.True(t, b.RemoveTab(tabs[2]))
	assert.Equal(t, 4, calls)

	// A tab reopened under the same uri starts idle.
	h := newTestHandler()
	b.NewTab(tabs[0].uri, 'o', "a", h, h)
	assert.False(t, b.HasWorkingTabs())
}

func TestCloseStopsTabActivity(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ActiveTabShader = "pulse"
	cfg.Interrupter = term.NopInterrupter()
	b := NewComponent(cfg)
	uri, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	h := newTestHandler()
	b.NewTab(uri, 'o', "a", h, h)

	working := programstatus.Report{State: programstatus.Working, Progress: -1}
	require.True(t, b.SetTabStatus(uri, working))
	require.True(t, b.shadedTabs.Running())
	require.NotNil(t, b.spinner)

	require.NoError(t, b.Close())
	assert.False(t, b.shadedTabs.Running())
	assert.Nil(t, b.spinner)
}

// newStatusComponent returns a frameless Component with the tabs
// "abcdefghij" (icon 'a') and "b" (icon 'B') open, the second one in
// the focused window, so the first tab's icon lands on cell 0 and its
// name on cells 2 to 11.
func newStatusComponent(t *testing.T) (*Component, []*Tab, func() []term.Cell) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Frame = false
	cfg.FrameUnion = false
	cfg.Dim = false
	cfg.ActiveTabShader = "pulse"
	cfg.Interrupter = term.NopInterrupter()
	b := NewComponent(cfg)
	t.Cleanup(func() { _ = b.Close() })

	uriA, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	h := newTestHandler()
	tabA := b.NewTab(uriA, 'a', "abcdefghij", h, h)
	uriB, err := workspaceapi.ParseURI("file:///b")
	require.NoError(t, err)
	h = newTestHandler()
	tabB := b.NewTab(uriB, 'B', "b", h, h)
	require.NoError(t, b.Focus().SetContent(tabB))

	width, height := 40, 5
	b.Resize(width, height)
	draw := func() []term.Cell {
		w := term.NewStringWriter(width, height)
		b.Draw(w)
		return w.Cells()
	}
	draw()
	return b, []*Tab{tabA, tabB}, draw
}

func TestSetTabStatusDecorations(t *testing.T) {
	const offset = term.AttrNegativeVerticalRenderOffset
	green := term.Attributes{Fg: term.ColorGreen}
	red := term.Attributes{Fg: term.ColorRed}
	yellow := term.Attributes{Fg: term.ColorYellow}
	cyan := term.Attributes{Fg: term.ColorAqua}
	bell := term.Attributes{Attrs: term.AttrBlink}
	// The shader is a no-op on the first frame, so the unfocused name
	// attribute is what the tab bar paints.
	nonFocus := DefaultConfig().NonFocusTabAttr
	nonFocus.Attrs |= offset

	suite := []struct {
		desc   string
		status programstatus.Report
		// bell sets the needs-attention name attribute before the status.
		bell bool
		// Expected decorations of tab a, which is not focused.
		icon     rune
		iconAttr term.Attributes
		nameAttr term.Attributes
		// painted is how many leading name cells carry the progress color.
		painted int
		shaded  bool
	}{
		{
			desc:     "idle",
			status:   programstatus.Report{State: programstatus.Clear},
			icon:     'a',
			iconAttr: term.Attributes{Attrs: offset},
			nameAttr: nonFocus,
		},
		{
			desc:     "idle as reported",
			status:   programstatus.Report{State: programstatus.Idle, Progress: -1},
			icon:     'a',
			iconAttr: term.Attributes{Fg: term.ColorGray, Attrs: offset},
			nameAttr: nonFocus,
		},
		{
			desc:     "working",
			status:   programstatus.Report{State: programstatus.Working, Progress: -1},
			icon:     '⠋',
			iconAttr: term.Attributes{Fg: term.ColorAqua, Attrs: offset},
			nameAttr: nonFocus,
			shaded:   true,
		},
		{
			desc:     "working with progress",
			status:   programstatus.Report{State: programstatus.Working, Progress: 40},
			icon:     '⠋',
			iconAttr: term.Attributes{Fg: term.ColorAqua, Attrs: offset},
			nameAttr: nonFocus,
			painted:  4,
			shaded:   true,
		},
		{
			desc: "blocked on permission",
			status: programstatus.Report{
				State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
			},
			icon:     '󰹇',
			iconAttr: term.Attributes{Fg: term.ColorYellow, Attrs: offset},
			nameAttr: term.AttributesUnion(nonFocus, yellow),
		},
		{
			desc: "blocked on a question",
			status: programstatus.Report{
				State: programstatus.Blocked, Kind: programstatus.KindQuestion, Progress: -1,
			},
			icon:     '󰋗',
			iconAttr: term.Attributes{Fg: term.ColorYellow, Attrs: offset},
			nameAttr: term.AttributesUnion(nonFocus, yellow),
		},
		{
			desc: "blocked on auth",
			status: programstatus.Report{
				State: programstatus.Blocked, Kind: programstatus.KindAuth, Progress: -1,
			},
			icon:     '󰗒',
			iconAttr: term.Attributes{Fg: term.ColorYellow, Attrs: offset},
			nameAttr: term.AttributesUnion(nonFocus, yellow),
		},
		{
			desc:     "blocked without a kind",
			status:   programstatus.Report{State: programstatus.Blocked, Progress: -1},
			icon:     '󰹇',
			iconAttr: term.Attributes{Fg: term.ColorYellow, Attrs: offset},
			nameAttr: term.AttributesUnion(nonFocus, yellow),
		},
		{
			desc:     "done",
			status:   programstatus.Report{State: programstatus.Done, Progress: -1},
			icon:     '󰗠',
			iconAttr: term.Attributes{Fg: term.ColorGreen, Attrs: offset},
			nameAttr: term.AttributesUnion(nonFocus, green),
		},
		{
			desc:     "error",
			status:   programstatus.Report{State: programstatus.Error, Progress: -1},
			icon:     '󰅙',
			iconAttr: term.Attributes{Fg: term.ColorRed, Attrs: offset},
			nameAttr: term.AttributesUnion(nonFocus, red),
		},
		{
			desc:     "bell alone",
			bell:     true,
			status:   programstatus.Report{State: programstatus.Clear},
			icon:     'a',
			iconAttr: term.Attributes{Attrs: offset},
			nameAttr: term.AttributesUnion(nonFocus, bell),
		},
		{
			desc:     "bell yields to the status color",
			bell:     true,
			status:   programstatus.Report{State: programstatus.Error, Progress: -1},
			icon:     '󰅙',
			iconAttr: term.Attributes{Fg: term.ColorRed, Attrs: offset},
			nameAttr: term.AttributesUnion(nonFocus, term.Attributes{
				Fg: term.ColorRed, Attrs: term.AttrBlink,
			}),
		},
	}

	for _, tc := range suite {
		t.Run(tc.desc, func(t *testing.T) {
			b, tabs, draw := newStatusComponent(t)
			b.config.TabStatus.ContentTab.Progress = true
			a := tabs[0]
			if tc.bell {
				require.True(t, b.SetTabDefaultNameAndAttrs(a.uri, "abcdefghij", bell))
				require.True(t, b.SetTabNameAndAttrs(a.uri, "abcdefghij", bell))
			}
			require.True(t, b.SetTabStatus(a.uri, tc.status))
			cells := draw()

			assert.Equal(t, tc.icon, cells[0].Ch)
			assert.Equal(t, tc.iconAttr, cells[0].Attributes())
			// Wide icons push the name one cell to the right.
			name := slices.IndexFunc(cells[1:], func(c term.Cell) bool { return c.Ch == 'a' }) + 1
			require.Positive(t, name)
			for i := range 10 {
				want := tc.nameAttr
				if i < tc.painted {
					want = term.AttributesUnion(want, cyan)
				}
				assert.Equalf(t, want, cells[name+i].Attributes(), "name cell %d", i)
			}
			assert.Equal(t, tc.shaded, b.shadedTabs.Running())
			assert.Equal(t, tc.shaded, b.spinner != nil)
			if tc.painted > 0 {
				offset, width, ok := b.tabs.TabRect(0)
				require.True(t, ok)
				shadeOffset, shadeWidth, ok := b.tabs.TabShadeRect(0)
				require.True(t, ok)
				assert.Equal(t, offset.X+tc.painted, shadeOffset.X)
				assert.Equal(t, width-tc.painted, shadeWidth)
			}

			attrs, ok := b.TabAttrs(a.uri)
			require.True(t, ok)
			if tc.bell {
				assert.Equal(t, bell, attrs, "TabAttrs reports the caller's attribute")
			} else {
				assert.Equal(t, term.Attributes{}, attrs)
			}

			// Back to idle restores the tab as it was.
			require.True(t, b.SetTabStatus(a.uri, programstatus.Report{State: programstatus.Clear}))
			cells = draw()
			assert.Equal(t, 'a', cells[0].Ch)
			assert.Equal(t, term.Attributes{Attrs: offset}, cells[0].Attributes())
			want := nonFocus
			if tc.bell {
				want = term.AttributesUnion(nonFocus, bell)
			}
			for i := range 10 {
				assert.Equalf(t, want, cells[2+i].Attributes(), "name cell %d", i)
			}
			assert.False(t, b.shadedTabs.Running())
			assert.Nil(t, b.spinner)
		})
	}
}

func TestSetTabStatusProgressDisabled(t *testing.T) {
	b, tabs, draw := newStatusComponent(t)
	uri := tabs[0].uri
	b.tabs.SetTabProgress(0, 25, term.Attributes{Fg: term.ColorGreen})
	for _, progress := range []int{0, 50, 100} {
		require.True(t, b.SetTabStatus(uri, programstatus.Report{
			State: programstatus.Working, Progress: progress,
		}))
		draw()
		pct, _ := b.tabs.TabProgress(0)
		assert.Equal(t, -1, pct, "reported progress %d must not split the label", progress)
		offset, width, ok := b.tabs.TabRect(0)
		require.True(t, ok)
		shadeOffset, shadeWidth, ok := b.tabs.TabShadeRect(0)
		require.True(t, ok)
		assert.Equal(t, offset, shadeOffset)
		assert.Equal(t, width, shadeWidth)
		assert.True(t, b.shadedTabs.Running())
		assert.NotNil(t, b.spinner)
		status, ok := b.TabStatus(uri)
		require.True(t, ok)
		assert.Equal(t, progress, status.Progress, "presentation must not change reported progress")
	}
}

func TestSetTabStatusSpinnerCycles(t *testing.T) {
	b, tabs := newActivityComponent(t, nil)
	working := programstatus.Report{State: programstatus.Working, Progress: -1}
	require.True(t, b.SetTabStatus(tabs[0].uri, working))
	require.NotNil(t, b.spinner)
	frames := DefaultTabStatusConfig().Spinner

	b.Draw(term.NewStringWriter(40, 10))
	assert.Equal(t, frames[0], b.tabs.TabIcon(0))
	assert.Equal(t, 'o', b.tabs.TabIcon(1), "idle tabs keep their icon")

	b.spinner.epoch.Store(3)
	b.Draw(term.NewStringWriter(40, 10))
	assert.Equal(t, frames[3], b.tabs.TabIcon(0))

	// A tab that starts working joins at the current frame.
	require.True(t, b.SetTabStatus(tabs[1].uri, working))
	assert.Equal(t, frames[3], b.tabs.TabIcon(1))

	b.spinner.epoch.Store(int64(len(frames) + 1))
	b.Draw(term.NewStringWriter(40, 10))
	assert.Equal(t, frames[1], b.tabs.TabIcon(0))
	assert.Equal(t, frames[1], b.tabs.TabIcon(1))
}

func TestSetTabStatusWithoutSpinnerKeepsIcon(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TabStatus.Spinner = nil
	cfg.Interrupter = term.NopInterrupter()
	b := NewComponent(cfg)
	t.Cleanup(func() { _ = b.Close() })
	uri, err := workspaceapi.ParseURI("file:///a")
	require.NoError(t, err)
	h := newTestHandler()
	b.NewTab(uri, 'o', "a", h, h)

	working := programstatus.Report{State: programstatus.Working, Progress: -1}
	require.True(t, b.SetTabStatus(uri, working))
	assert.Nil(t, b.spinner)
	b.Draw(term.NewStringWriter(40, 10))
	assert.Equal(t, 'o', b.tabs.TabIcon(0))
}

func TestSetTabStatusWithoutIconRestoresUnderlyingIcon(t *testing.T) {
	suite := []struct {
		name string
		from programstatus.State
		to   programstatus.State
	}{
		{"blocked to working", programstatus.Blocked, programstatus.Working},
		{"done to working", programstatus.Done, programstatus.Working},
		{"error to working", programstatus.Error, programstatus.Working},
		{"idle to working", programstatus.Idle, programstatus.Working},
		{"error to blocked without icons", programstatus.Error, programstatus.Blocked},
		{"blocked to done without icon", programstatus.Blocked, programstatus.Done},
		{"done to error without icon", programstatus.Done, programstatus.Error},
		{"blocked to idle", programstatus.Blocked, programstatus.Idle},
		{"error to clear", programstatus.Error, programstatus.Clear},
	}
	for _, tc := range suite {
		t.Run(tc.name, func(t *testing.T) {
			b, tabs, draw := newStatusComponent(t)
			b.config.TabStatus.Spinner = nil
			switch tc.to {
			case programstatus.Blocked:
				b.config.TabStatus.BlockedIcons = nil
			case programstatus.Done:
				b.config.TabStatus.DoneIcon = 0
			case programstatus.Error:
				b.config.TabStatus.ErrorIcon = 0
			}
			uri := tabs[0].uri
			require.True(t, b.SetTabStatus(uri, programstatus.Report{State: tc.from, Progress: -1}))
			if tc.from != programstatus.Idle {
				require.NotEqual(t, 'a', draw()[0].Ch)
			}

			require.True(t, b.SetTabStatus(uri, programstatus.Report{State: tc.to, Progress: -1}))
			assert.Equal(t, 'a', b.tabs.TabIcon(0))
			assert.Equal(t, 'a', draw()[0].Ch)
			assert.Nil(t, b.spinner)
		})
	}
}

func TestStatusRollup(t *testing.T) {
	b, tabs := newActivityComponent(t, nil)

	sum, uri := b.StatusRollup()
	assert.Equal(t, programstatus.Summary{Progress: -1}, sum)
	assert.Equal(t, workspaceapi.URI{}, uri)

	working := programstatus.Report{State: programstatus.Working, Progress: -1}
	done := programstatus.Report{State: programstatus.Done, Progress: -1, Msg: "ok"}
	blocked := programstatus.Report{State: programstatus.Blocked, Progress: -1}
	clearAll := programstatus.Report{State: programstatus.Clear}
	require.True(t, b.SetTabStatus(tabs[0].uri, working))
	require.True(t, b.SetTabStatus(tabs[2].uri, done))
	sum, uri = b.StatusRollup()
	assert.Equal(t, programstatus.Done, sum.State)
	assert.Equal(t, "ok", sum.Msg)
	assert.Equal(t, tabs[2].uri, uri)

	require.True(t, b.SetTabStatus(tabs[1].uri, blocked))
	sum, uri = b.StatusRollup()
	assert.Equal(t, programstatus.Blocked, sum.State)
	assert.Equal(t, tabs[1].uri, uri)

	require.True(t, b.SetTabStatus(tabs[1].uri, clearAll))
	require.True(t, b.SetTabStatus(tabs[2].uri, clearAll))
	sum, uri = b.StatusRollup()
	assert.Equal(t, programstatus.Working, sum.State)
	assert.Equal(t, tabs[0].uri, uri)

	require.True(t, b.SetTabStatus(tabs[0].uri, clearAll))
	require.True(t, b.SetTabStatus(tabs[2].uri, programstatus.Report{State: programstatus.Idle, Progress: -1}))
	sum, uri = b.StatusRollup()
	assert.Equal(t, programstatus.Idle, sum.State)
	assert.Len(t, sum.Records, 1, "a reported idle outranks silence")
	assert.Equal(t, tabs[2].uri, uri)
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
			term.Coordinates{X: 0, Y: 1}, 40, 20, winDropLeft},
		{"bottom-right corner favors the closer edge",
			term.Coordinates{X: 38, Y: 19}, 40, 20, winDropBottom},
		{"large tile top band is two cells",
			term.Coordinates{X: 80, Y: 1}, 160, 55, winDropTop},
		{"large tile below the top band is a dead zone",
			term.Coordinates{X: 80, Y: 2}, 160, 55, winDropNone},
		{"large tile left band is two cells",
			term.Coordinates{X: 1, Y: 27}, 160, 55, winDropLeft},
		{"large tile past the left band is a dead zone",
			term.Coordinates{X: 2, Y: 27}, 160, 55, winDropNone},
		{"large tile above the bottom band is a dead zone",
			term.Coordinates{X: 80, Y: 52}, 160, 55, winDropNone},
		{"large tile past the right band is a dead zone",
			term.Coordinates{X: 157, Y: 27}, 160, 55, winDropNone},
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
			return term.Coordinates{X: pos.X + winDropBand + 1, Y: pos.Y + h/2}
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
			return term.Coordinates{X: pos.X + winDropBand + 1, Y: pos.Y + h/2}
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

func TestWinDropPastEdgesThroughMouseEvents(t *testing.T) {
	suite := []struct {
		name string
		zone winDropZone
		// float is where the float hangs while the cursor is on the band.
		float term.Coordinates
		// docked reports whether the new tile is on the band's side of
		// the tile it split.
		docked func(win, tile Window) bool
	}{
		{"past the left edge", winDropLeft, term.Coordinates{X: -4, Y: 9},
			func(win, tile Window) bool { return win.Position().X < tile.Position().X }},
		{"past the right edge", winDropRight, term.Coordinates{X: 55, Y: 9},
			func(win, tile Window) bool { return win.Position().X > tile.Position().X }},
		{"past the bottom edge", winDropBottom, term.Coordinates{X: 26, Y: 18},
			func(win, tile Window) bool { return win.Position().Y > tile.Position().Y }},
		{"inside the top edge", winDropTop, term.Coordinates{X: 26, Y: 0},
			func(win, tile Window) bool { return win.Position().Y < tile.Position().Y }},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			b, tile, float, zoneAt := framedDragBrowser(t, 60, 20)
			content, err := float.Content()
			require.NoError(t, err)
			at := zoneAt(test.zone)

			b.Handle(mouseAt(b, term.MouseLeft, term.Coordinates{X: 4, Y: 0}))
			b.Handle(mouseAt(b, term.MouseLeft, at))
			assert.Equal(t, test.float, float.Position(), "the float follows the cursor")
			assert.Equal(t, 14, float.Width(), "the float keeps its size")
			assert.Equal(t, 10, float.Height(), "the float keeps its size")
			assert.Equal(t, test.zone, b.winDrop.zone)
			assert.Equal(t, 2, b.Tiles(), "the hover must pre-split the tile")

			b.Handle(mouseAt(b, term.MouseRelease, at))
			assert.Equal(t, 0, b.FloatingWindows())
			require.Len(t, b.buffers, 1)
			assert.Equal(t, content, b.buffers[0].Handler())
			win, ok := b.buffers[0].Window()
			require.True(t, ok)
			assert.True(t, test.docked(win, tile), "the tab must dock on the band's side")
		})
	}
}

func TestWinDropDeadZonePastEdgesThroughMouseEvents(t *testing.T) {
	suite := []struct {
		name  string
		at    term.Coordinates
		float term.Coordinates
	}{
		{"past the left edge", term.Coordinates{X: 3, Y: 5}, term.Coordinates{X: -1, Y: 5}},
		{"past the right edge", term.Coordinates{X: 55, Y: 5}, term.Coordinates{X: 51, Y: 5}},
		{"past the bottom edge", term.Coordinates{X: 10, Y: 15}, term.Coordinates{X: 6, Y: 15}},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			b, tile, float, _ := framedDragBrowser(t, 60, 20)
			local := term.Coordinates{
				X: test.at.X - tile.Position().X, Y: test.at.Y - tile.Position().Y,
			}
			require.Equal(t, winDropNone, dropZoneAt(local, tile.Width(), tile.Height()),
				"the release must land outside every band")

			b.Handle(mouseAt(b, term.MouseLeft, term.Coordinates{X: 4, Y: 0}))
			b.Handle(mouseAt(b, term.MouseLeft, test.at))
			b.Handle(mouseAt(b, term.MouseRelease, test.at))

			assert.Equal(t, 1, b.Tiles())
			assert.Equal(t, 1, b.FloatingWindows(), "the float survives")
			assert.Empty(t, b.buffers)
			assert.Equal(t, test.float, float.Position(), "the float stays past the edge")
			assert.Equal(t, 14, float.Width(), "the float keeps its size")
			assert.Equal(t, 10, float.Height(), "the float keeps its size")
		})
	}
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

	t.Run("a float showing the wallpaper is a plain move", func(t *testing.T) {
		b, tile, _, zoneAt := winDragBrowser(t, 60, 20)
		h := newTestHandler()
		tab := b.NewTab(workspaceapi.URI{}, 'm', "gone", h, nil)
		float := b.Floating(newTestHandler(), browserapi.FloatingConfig{})
		require.NoError(t, float.SetContent(tab))
		require.False(t, b.RemoveWindowContent(float))
		b.Resize(60, 20)
		floats := b.FloatingWindows()
		before, err := tile.Content()
		require.NoError(t, err)

		pos := zoneAt(winDropCenter)
		barDrag(b, float, pos)
		assert.False(t, b.OnBarDrop(float.(*browserWindow).win, pos))

		assert.Equal(t, floats, b.FloatingWindows(), "the float survives a plain move")
		assert.Empty(t, b.buffers)
		after, err := tile.Content()
		require.NoError(t, err)
		assert.Equal(t, before, after)
		assert.NotContains(t, drawString(b, 60, 20), winDropConvertLabel)
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

func TestTabStatusRecords(t *testing.T) {
	var changes []programstatus.Summary
	cfg := DefaultConfig()
	cfg.OnTabStatus = func(_ workspaceapi.URI, _, status programstatus.Summary) {
		changes = append(changes, status)
	}
	cfg.Interrupter = term.NopInterrupter()
	b := NewComponent(cfg)
	b.Resize(40, 10)
	t.Cleanup(func() { _ = b.Close() })
	uri, err := workspaceapi.ParseURI("rune-agent://model/chat")
	require.NoError(t, err)
	h := newTestHandler()
	tab := b.NewTab(uri, 'o', "chat", h, h)
	other, err := workspaceapi.ParseURI("file:///other")
	require.NoError(t, err)

	assert.False(t, b.SetTabStatus(other, programstatus.Report{State: programstatus.Working, Progress: -1}),
		"reporting on an unknown tab fails")

	require.True(t, b.SetTabStatus(uri, programstatus.Report{
		State: programstatus.Working, Progress: -1, Title: "Thinking",
	}))
	status, ok := b.TabStatus(uri)
	require.True(t, ok)
	assert.Equal(t, programstatus.Working, status.State)
	assert.Equal(t, "Thinking", status.Title)
	assert.True(t, b.HasWorkingTabs())

	require.True(t, b.SetTabStatus(uri, programstatus.Report{
		ID: "agent/1", State: programstatus.Blocked, Kind: programstatus.KindQuestion,
		Progress: -1, Msg: "Which one?",
	}))
	status, _ = b.TabStatus(uri)
	assert.Equal(t, programstatus.Blocked, status.State, "records roll up")
	assert.Equal(t, programstatus.KindQuestion, status.Kind)
	require.Len(t, status.Records, 2)

	require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "agent/1", State: programstatus.Clear}))
	status, _ = b.TabStatus(uri)
	assert.Equal(t, programstatus.Working, status.State, "clear removes the record")
	require.Len(t, status.Records, 1)

	require.True(t, b.SetTabStatus(uri, programstatus.Report{
		State: programstatus.Working, Progress: -1, Title: "Thinking",
	}))
	n := len(changes)
	require.True(t, b.SetTabStatus(uri, programstatus.Report{
		State: programstatus.Working, Progress: -1, Title: "Thinking",
	}))
	assert.Len(t, changes, n, "a repeated report is not a change")

	require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Done, Progress: -1, Msg: "ok"}))
	status, _ = b.TabStatus(uri)
	assert.Equal(t, programstatus.Done, status.State)
	assert.False(t, b.HasWorkingTabs())

	t.Run("a key event acknowledges done", func(t *testing.T) {
		tab.Handle(term.Event{Type: term.EventKey, Ch: 'j'})
		status, _ := b.TabStatus(uri)
		assert.Equal(t, programstatus.Idle, status.State)
		assert.Empty(t, status.Records)
	})

	t.Run("AckStatus keeps working and blocked", func(t *testing.T) {
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Working, Progress: -1}))
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "x", State: programstatus.Error, Progress: -1}))
		tab.AckStatus()
		status, _ := b.TabStatus(uri)
		assert.Equal(t, programstatus.Working, status.State)
		require.Len(t, status.Records, 1)
	})

	t.Run("AckStatus drops idle", func(t *testing.T) {
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Clear}))
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Idle, Progress: -1, Msg: "make test"}))
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "x", State: programstatus.Blocked, Progress: -1}))
		tab.AckStatus()
		status, _ := b.TabStatus(uri)
		assert.Equal(t, programstatus.Blocked, status.State)
		require.Len(t, status.Records, 1)
		assert.Equal(t, "x", status.Records[0].ID)
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Clear}))
	})

	t.Run("EndTabStatus keeps done and error", func(t *testing.T) {
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Clear}))
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "a", State: programstatus.Working, Progress: -1}))
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "b", State: programstatus.Blocked, Progress: -1}))
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "c", State: programstatus.Idle, Progress: -1}))
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "d", State: programstatus.Done, Progress: -1}))
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "e", State: programstatus.Error, Progress: -1}))
		require.True(t, b.EndTabStatus(uri))
		status, _ := b.TabStatus(uri)
		assert.Equal(t, programstatus.Error, status.State)
		require.Len(t, status.Records, 2)
		assert.Equal(t, "d", status.Records[0].ID)
		assert.Equal(t, "e", status.Records[1].ID)

		n := len(changes)
		require.True(t, b.EndTabStatus(uri))
		assert.Len(t, changes, n, "nothing left to end is not a change")
		assert.False(t, b.EndTabStatus(other), "ending on an unknown tab fails")
	})

	t.Run("a mouse event does not acknowledge", func(t *testing.T) {
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Error, Progress: -1}))
		tab.Handle(term.Event{Type: term.EventMouse})
		status, _ := b.TabStatus(uri)
		assert.Equal(t, programstatus.Error, status.State)
	})

	t.Run("showing the tab acknowledges", func(t *testing.T) {
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "interrupted", State: programstatus.Idle, Progress: -1}))
		win := b.Focus()
		require.True(t, b.SetContentToTab(win, 0))
		status, _ := b.TabStatus(uri)
		assert.Equal(t, programstatus.Idle, status.State)
		assert.Empty(t, status.Records, "done, error and idle are all acknowledged")
	})

	t.Run("focusing the window showing the tab acknowledges", func(t *testing.T) {
		shown := b.Focus()
		other, ok := b.Split(browserapi.OrientationRight, shown, newTestHandler())
		require.True(t, ok)
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Done, Progress: -1}))
		status, _ := b.TabStatus(uri)
		require.Equal(t, programstatus.Done, status.State, "reported out of focus")

		b.SetFocus(shown)
		status, _ = b.TabStatus(uri)
		assert.Equal(t, programstatus.Idle, status.State)
		require.NoError(t, other.Close())
	})

	t.Run("done reported in sight is acknowledged right away", func(t *testing.T) {
		n := len(changes)
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Done, Progress: -1, Msg: "ok"}))
		status, _ := b.TabStatus(uri)
		assert.Equal(t, programstatus.Idle, status.State)
		assert.Empty(t, status.Records)
		require.Len(t, changes, n+2)
		assert.Equal(t, "ok", changes[n].Msg, "the done record is reported before it is acknowledged")
	})

	t.Run("working reported in sight stays", func(t *testing.T) {
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Working, Progress: -1}))
		status, _ := b.TabStatus(uri)
		assert.Equal(t, programstatus.Working, status.State)
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Clear}))
	})

	t.Run("out of focus nothing is in sight until focus returns", func(t *testing.T) {
		shown := b.Focus()
		other, ok := b.Split(browserapi.OrientationRight, shown, newTestHandler())
		require.True(t, ok)
		b.OnFocusChange(false)
		require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Error, Progress: -1}))
		status, _ := b.TabStatus(uri)
		require.Equal(t, programstatus.Error, status.State)

		b.SetFocus(shown)
		status, _ = b.TabStatus(uri)
		require.Equal(t, programstatus.Error, status.State, "focusing its window out of focus is not seeing it")
		require.True(t, b.SetTabStatus(uri, programstatus.Report{ID: "x", State: programstatus.Done, Progress: -1}))
		status, _ = b.TabStatus(uri)
		require.Len(t, status.Records, 2, "reporting on the focus window out of focus is not seeing it")

		b.OnFocusChange(true)
		status, _ = b.TabStatus(uri)
		assert.Equal(t, programstatus.Idle, status.State)
		assert.Empty(t, status.Records)
		require.NoError(t, other.Close())
	})

	t.Run("removing the tab drops the records", func(t *testing.T) {
		require.True(t, b.RemoveTab(tab))
		h := newTestHandler()
		b.NewTab(uri, 'o', "chat", h, h)
		status, ok := b.TabStatus(uri)
		require.True(t, ok)
		assert.Equal(t, programstatus.Idle, status.State)
		assert.Empty(t, status.Records)
	})
}

func TestContentSeen(t *testing.T) {
	type windows struct {
		tab, float, other Window
	}
	for _, tc := range []struct {
		name   string
		before func(b *Component, w windows)
		act    func(b *Component, w windows)
		// focus names the handler of the focus window: "tab", "float"
		// or "other". seen names what comes in sight, or is empty when
		// nothing does.
		focus, seen string
	}{{
		name:  "focusing a window that is no tab",
		act:   func(b *Component, w windows) { b.SetFocus(w.float) },
		focus: "float", seen: "float",
	}, {
		name:  "focusing the window showing a tab",
		act:   func(b *Component, w windows) { b.SetFocus(w.tab) },
		focus: "tab", seen: "tab",
	}, {
		name:  "losing focus",
		act:   func(b *Component, _ windows) { b.OnFocusChange(false) },
		focus: "other",
	}, {
		name: "focusing a window out of focus",
		before: func(b *Component, _ windows) {
			b.OnFocusChange(false)
		},
		act:   func(b *Component, w windows) { b.SetFocus(w.float) },
		focus: "float",
	}, {
		name: "regaining focus",
		before: func(b *Component, w windows) {
			b.SetFocus(w.tab)
			b.OnFocusChange(false)
		},
		act:   func(b *Component, _ windows) { b.OnFocusChange(true) },
		focus: "tab", seen: "tab",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []browserapi.Handler
			cfg := DefaultConfig()
			cfg.OnContentSeen = func(content browserapi.Handler) { seen = append(seen, content) }
			cfg.Interrupter = term.NopInterrupter()
			b := NewComponent(cfg)
			b.Resize(60, 20)
			t.Cleanup(func() { _ = b.Close() })
			uri, err := workspaceapi.ParseURI("rune-agent://model/chat")
			require.NoError(t, err)
			handlers := map[string]*nopHandler{
				"tab": newTestHandler(), "float": newTestHandler(), "other": newTestHandler(),
			}
			b.NewTab(uri, 'o', "chat", handlers["tab"], handlers["tab"])
			w := windows{tab: b.Focus()}
			require.True(t, b.SetContentToTab(w.tab, 0))
			w.float = b.Floating(handlers["float"], browserapi.FloatingConfig{})
			var ok bool
			w.other, ok = b.Split(browserapi.OrientationRight, w.tab, handlers["other"])
			require.True(t, ok)
			b.SetFocus(w.other)
			if tc.before != nil {
				tc.before(b, w)
			}
			require.True(t, b.SetTabStatus(uri, programstatus.Report{State: programstatus.Error, Progress: -1}))
			seen = nil

			tc.act(b, w)

			assert.Same(t, handlers[tc.focus], b.FocusHandler())
			status, _ := b.TabStatus(uri)
			if tc.seen == "" {
				assert.Empty(t, seen)
				assert.Equal(t, programstatus.Error, status.State)
				return
			}
			require.Len(t, seen, 1)
			assert.Same(t, handlers[tc.seen], seen[0])
			wantState := programstatus.Error
			if tc.seen == "tab" {
				wantState = programstatus.Idle
			}
			assert.Equal(t, wantState, status.State, "only the tab in sight is acknowledged")
		})
	}
}

// pictureWallpaper writes two marks on its first row, coloring the second
// one by attributes, and draws its picture under them, or hashes where
// the writer cannot draw the picture, as the logo falls back to blocks.
type pictureWallpaper struct {
	pic           image.Image
	id            term.ImageID
	width, height int
}

func (p *pictureWallpaper) Resize(width, height int) {
	p.width, p.height = width, height
}

func (p *pictureWallpaper) Draw(w term.Writer) {
	green := term.Attributes{Bg: term.ColorGreen}
	w.SetCell(term.Coordinates{}, term.NewCell('W', 1,
		term.Attributes{Fg: term.ColorRed, Bg: term.ColorGreen}))
	w.SetCell(term.Coordinates{X: 1}, term.NewCell('U', 1, green))
	w.UnionAttributes(term.Coordinates{X: 1},
		term.Attributes{Fg: term.ColorYellow, Bg: term.ColorGreen})
	_, sized := cell.PixelSizeFromContext(w.Context())
	if sized && w.DrawImage(term.Image{
		Src: p.pic, ID: p.id, Pos: term.Coordinates{Y: 1},
		Width: p.width, Height: p.height - 1, Fit: term.ImageFitFill,
	}) {
		return
	}
	for y := 1; y < p.height; y++ {
		for x := range p.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('#', 1, term.Attributes{}))
		}
	}
}

func TestComponentWallpaperOnABackgroundKeepsItsPictures(t *testing.T) {
	const width, height = 30, 10
	background := term.Attributes{Bg: term.ColorBlue}
	art := &pictureWallpaper{pic: image.NewRGBA(image.Rect(0, 0, 2, 2)), id: term.NewImageID()}
	cfg := DefaultConfig()
	cfg.Wallpaper = Wallpaper{
		NewComponent:   func() tui.Component { return art },
		BackgroundAttr: background,
	}
	b := NewComponent(cfg)
	b.Resize(width, height)

	ctx := cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 8, Height: 16})
	w := cell.NewBufferWriter(ctx, width, height)
	b.Draw(w)

	images := w.Images()
	require.Len(t, images, 1, "the wallpaper must draw its picture")
	assert.Equal(t, art.id, images[0].ID)

	var mark term.Coordinates
	found := false
	for y, row := range w.RawCells() {
		for x, c := range row {
			assert.NotEqual(t, '#', c.Ch, "the wallpaper must not fall back at %d,%d", x, y)
			if c.Ch == 'W' {
				mark, found = term.Coordinates{X: x, Y: y}, true
			}
		}
	}
	require.True(t, found, "the wallpaper must be drawn")
	cells := w.RawCells()
	set, united := cells[mark.Y][mark.X], cells[mark.Y][mark.X+1]
	assert.Equal(t, 'U', united.Ch)
	assert.Equal(t, term.ColorRed, set.Fg, "the wallpaper keeps the foreground it sets")
	assert.Equal(t, term.ColorYellow, united.Fg, "the wallpaper keeps the foreground it unites")
	assert.Equal(t, background.Bg, set.Bg, "the wallpaper is set on the background")
	assert.Equal(t, background.Bg, united.Bg, "the wallpaper is united on the background")
	assert.Equal(t, background.Bg, cells[mark.Y][mark.X+2].Bg,
		"the background fills what the wallpaper does not draw")
}

// newPointerLess returns a less over more lines than any test size, some
// longer than its width.
func newPointerLess(t *testing.T) *thandler.Less {
	t.Helper()
	l := thandler.NewLess(thandler.DefaultLessConfig())
	var b strings.Builder
	for i := range 200 {
		b.WriteString(strings.Repeat(string(rune('a'+i%26)), i%90))
		b.WriteString("\n")
	}
	_, err := l.Buffer().ReadFrom(strings.NewReader(b.String()))
	require.NoError(t, err)
	return l
}

// newPointerComponent returns a browser with several tabs in split
// windows, a floating window, a prompt, bars on two sides and a
// reserved right column.
func newPointerComponent(t *testing.T, tabBarOffset int) *Component {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ActiveTabShader = "pulse"
	cfg.RightInset = 2
	cfg.TabBarOffset = tabBarOffset
	cfg.WindowManagerConfig.WindowBar = true
	cfg.OnTabIconClick = func(*Tab) {}
	cfg.Interrupter = term.NopInterrupter()
	b := NewComponent(cfg)
	b.Resize(60, 15)
	t.Cleanup(func() { _ = b.Close() })

	var tabs []*Tab
	for i := range 8 {
		uri, err := workspaceapi.ParseURI(fmt.Sprintf("file:///tab-with-a-long-name-%d", i))
		require.NoError(t, err)
		h := NopHandler(newPointerLess(t))
		tabs = append(tabs, b.NewTab(uri, 'o', fmt.Sprintf("tab-with-a-long-name-%d", i), h, h))
	}
	left := b.Focus()
	require.NoError(t, left.SetContent(tabs[0]))
	right, ok := b.Split(browserapi.OrientationRight, left, tabs[1])
	require.True(t, ok)
	_, ok = b.Split(browserapi.OrientationBottom, right, NopHandler(newPointerLess(t)))
	require.True(t, ok)
	require.True(t, b.SetTabStatus(tabs[2].URI(), programstatus.Report{
		State: programstatus.Working, Progress: -1,
	}))

	b.Bar(browserapi.BarConfig{Orientation: browserapi.OrientationBottom, Size: 1},
		NopHandler(newPointerLess(t)))
	b.Bar(browserapi.BarConfig{Orientation: browserapi.OrientationLeft, Size: 3},
		NopHandler(newPointerLess(t)))
	b.Floating(StaticFloating(NopHandler(newPointerLess(t)), 20, 6), browserapi.FloatingConfig{})
	b.Prompt("Proceed with a rather long question?", []string{"Yes", "No"}, nil,
		handler.NopPromptHandler())
	b.SetFocus(left)
	return b
}

func TestComponentPointerLeaves(t *testing.T) {
	for _, offset := range []int{0, 4} {
		for _, size := range [][2]int{{60, 15}, {1, 1}} {
			b := newPointerComponent(t, offset)
			focus, floats := b.Focus(), b.FloatingWindows()
			for _, at := range []term.Coordinates{{X: 1, Y: 1}, {X: 30, Y: 7}, {X: 10, Y: 13}} {
				for _, out := range []term.Coordinates{{X: -1, Y: 7}, {X: 60, Y: 7}, {X: 30, Y: -50}} {
					b.Handle(term.Event{Type: term.EventMouse, MouseX: at.X, MouseY: at.Y,
						Context: term.ContextWithPointer(context.Background(), new(term.Pointer))})
					b.Handle(term.Event{Type: term.EventMouse, MouseX: out.X, MouseY: out.Y})
				}
			}
			assert.Equal(t, focus, b.Focus(), "a leave does not move the focus")
			assert.Equal(t, floats, b.FloatingWindows(), "a leave does not close a window")
			assert.Len(t, b.prompts, 1, "a leave does not answer the prompt")

			rhandlertest.RunPointerLeaves(t, b, size[0], size[1])
		}
	}
}

func TestComponentContentPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 15}, {1, 1}} {
		b := newPointerComponent(t, 0)
		prompt := b.Prompt("Another long question for the prompt?", []string{"Yes", "No", "Maybe"},
			nil, handler.NopPromptHandler())
		var contents []tui.Handler
		for _, win := range []Window{prompt, b.Focus()} {
			h, err := win.Content()
			require.NoError(t, err)
			contents = append(contents, h)
		}
		for _, h := range contents {
			rhandlertest.RunPointerLeaves(t, h, size[0], size[1])
		}
	}
}

func TestOffsetTabsPointerLeaves(t *testing.T) {
	for _, size := range [][2]int{{60, 1}, {60, 15}, {1, 1}} {
		var tabs thandler.Tabs
		tabs.Init()
		for i := range 12 {
			tabs.Add('A'+rune(i), fmt.Sprintf("tab number %d", i))
		}
		tabs.OnClick = func(int) bool { return true }
		tabs.OnIconClick = func(int) {}
		v := newOffsetTabs(thandler.NewShadedTabs(&tabs, thandler.ShadedTabsConfig{}), 4)
		rhandlertest.RunPointerLeaves(t, v, size[0], size[1])
	}
}

func TestWindowDefaultWidth(t *testing.T) {
	edgeAt := func(win Window) term.Coordinates {
		return term.Coordinates{X: win.Position().X + win.Width() - 1, Y: win.Position().Y + 2}
	}
	tests := []struct {
		name  string
		reset func(b *Component, win Window)
	}{
		{
			name: "double click on the divider",
			reset: func(b *Component, win Window) {
				edge := edgeAt(win)
				for range 2 {
					b.Handle(mouseAt(b, term.MouseLeft, edge))
					b.Handle(mouseAt(b, term.MouseRelease, edge))
				}
			},
		},
		{
			name: "reset window size",
			reset: func(b *Component, win Window) {
				b.SetFocus(win)
				require.True(t, b.ResetWindowSize())
			},
		},
		{
			name: "setting the default again",
			reset: func(b *Component, win Window) {
				require.True(t, b.SetWindowDefaultWidth(win, 12))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := dragConfig()
			cfg.Frame = true
			b := NewComponent(cfg)
			require.NoError(t, b.Focus().SetContent(newTestHandler()))
			win, ok := b.SplitRoot(component.AlignmentLeft, newTestHandler())
			require.True(t, ok)
			b.Resize(60, 20)
			require.True(t, b.SetWindowDefaultWidth(win, 12))
			b.Draw(term.NewStringWriter(60, 20))
			require.Equal(t, 12, win.Width())

			edge := edgeAt(win)
			b.Handle(mouseAt(b, term.MouseLeft, edge))
			b.Handle(mouseAt(b, term.MouseLeft, term.Coordinates{X: 29, Y: edge.Y}))
			b.Handle(mouseAt(b, term.MouseRelease, term.Coordinates{X: 29, Y: edge.Y}))
			b.Draw(term.NewStringWriter(60, 20))
			require.Equal(t, 30, win.Width())

			tt.reset(b, win)
			b.Draw(term.NewStringWriter(60, 20))
			assert.Equal(t, 12, win.Width())
		})
	}
}
