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

package component

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

func TestSetWidthHeight(t *testing.T) {
	w := term.NewStringWriter(20, 8)

	h1 := component.TestComponent{Ch: 'A'}
	wm, win1 := NewWindowManager(&h1, testWindowManagerConfig())
	wm.Resize(20, 8)

	var win2, win3 Window
	tests := []comptest.TestCase{
		{
			Action: func() {
				var ok bool
				win2, ok = wm.SplitVertical(win1, &component.TestComponent{Ch: 'B'})
				require.True(t, ok)
				win3, ok = wm.SplitHorizontal(win1, &component.TestComponent{Ch: 'C'})
				require.True(t, ok)
			}, Expected: `
┌────────┐┌────────┐
│AAAAAAAA││BBBBBBBB│
│AAAAAAAA││BBBBBBBB│
└────────┘│BBBBBBBB│
┌────────┐│BBBBBBBB│
│CCCCCCCC││BBBBBBBB│
│CCCCCCCC││BBBBBBBB│
└────────┘└────────┘`,
		},
		{
			Action: func() {
				assert.True(t, wm.SetHeight(win1, win1.MaxHeight()))
				assert.True(t, wm.SetWidth(win1, win1.MaxWidth()))
				assert.True(t, wm.SetHeight(win2, win2.MaxHeight()))
				assert.True(t, wm.SetWidth(win2, win2.MaxWidth()))
				assert.True(t, wm.SetHeight(win3, win3.MaxHeight()))
				assert.True(t, wm.SetWidth(win3, win3.MaxWidth()))
			}, Expected: `
┌───────────────┐┌─┐
│AAAAAAAAAAAAAAA││B│
└───────────────┘│B│
┌───────────────┐│B│
│CCCCCCCCCCCCCCC││B│
│CCCCCCCCCCCCCCC││B│
│CCCCCCCCCCCCCCC││B│
└───────────────┘└─┘`,
		},
	}

	comptest.TestComponent(t, wm, w, tests)
}

// splitRootLayout is one of the three initial layouts exercised by
// TestSplitRoot: a single tile (empty), a horizontal-then-vertical
// decomposition, or a vertical-then-horizontal decomposition.
type splitRootLayout int

const (
	splitRootLayoutEmpty splitRootLayout = iota
	splitRootLayoutHorizontalThenVertical
	splitRootLayoutVerticalThenHorizontal
)

// buildSplitRootLayout populates wm/win1 with the layout identified
// by l. The returned siblings are returned so the test can interact
// with them if needed; callers that don't care may discard them.
func buildSplitRootLayout(
	t *testing.T, wm *WindowManager, win1 Window, l splitRootLayout,
) (win2, win3 Window) {
	t.Helper()
	switch l {
	case splitRootLayoutEmpty:
		return
	case splitRootLayoutHorizontalThenVertical:
		var ok bool
		win2, ok = wm.SplitHorizontal(win1, &component.TestComponent{Ch: 'B'})
		require.True(t, ok)
		win3, ok = wm.SplitVertical(win1, &component.TestComponent{Ch: 'C'})
		require.True(t, ok)
		return
	case splitRootLayoutVerticalThenHorizontal:
		var ok bool
		win2, ok = wm.SplitVertical(win1, &component.TestComponent{Ch: 'B'})
		require.True(t, ok)
		win3, ok = wm.SplitHorizontal(win1, &component.TestComponent{Ch: 'C'})
		require.True(t, ok)
		return
	}
	t.Fatalf("unknown layout: %d", l)
	return
}

func TestSplitRoot(t *testing.T) {
	tests := []struct {
		name      string
		layout    splitRootLayout
		alignment component.Alignment
		expected  string
	}{
		{
			name:      "empty_left",
			layout:    splitRootLayoutEmpty,
			alignment: component.AlignmentLeft,
			expected: `
┌────────┐┌────────┐
│DDDDDDDD││AAAAAAAA│
│DDDDDDDD││AAAAAAAA│
│DDDDDDDD││AAAAAAAA│
│DDDDDDDD││AAAAAAAA│
│DDDDDDDD││AAAAAAAA│
│DDDDDDDD││AAAAAAAA│
└────────┘└────────┘`,
		},
		{
			name:      "empty_right",
			layout:    splitRootLayoutEmpty,
			alignment: component.AlignmentRight,
			expected: `
┌────────┐┌────────┐
│AAAAAAAA││DDDDDDDD│
│AAAAAAAA││DDDDDDDD│
│AAAAAAAA││DDDDDDDD│
│AAAAAAAA││DDDDDDDD│
│AAAAAAAA││DDDDDDDD│
│AAAAAAAA││DDDDDDDD│
└────────┘└────────┘`,
		},
		{
			name:      "empty_top",
			layout:    splitRootLayoutEmpty,
			alignment: component.AlignmentTop,
			expected: `
┌──────────────────┐
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
┌──────────────────┐
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`,
		},
		{
			name:      "empty_bottom",
			layout:    splitRootLayoutEmpty,
			alignment: component.AlignmentBottom,
			expected: `
┌──────────────────┐
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘
┌──────────────────┐
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘`,
		},
		{
			name:      "horizontal_then_vertical_left",
			layout:    splitRootLayoutHorizontalThenVertical,
			alignment: component.AlignmentLeft,
			expected: `
┌────────┐┌───┐┌───┐
│DDDDDDDD││AAA││CCC│
│DDDDDDDD││AAA││CCC│
│DDDDDDDD│└───┘└───┘
│DDDDDDDD│┌────────┐
│DDDDDDDD││BBBBBBBB│
│DDDDDDDD││BBBBBBBB│
└────────┘└────────┘`,
		},
		{
			name:      "horizontal_then_vertical_right",
			layout:    splitRootLayoutHorizontalThenVertical,
			alignment: component.AlignmentRight,
			expected: `
┌───┐┌───┐┌────────┐
│AAA││CCC││DDDDDDDD│
│AAA││CCC││DDDDDDDD│
└───┘└───┘│DDDDDDDD│
┌────────┐│DDDDDDDD│
│BBBBBBBB││DDDDDDDD│
│BBBBBBBB││DDDDDDDD│
└────────┘└────────┘`,
		},
		{
			name:      "horizontal_then_vertical_top",
			layout:    splitRootLayoutHorizontalThenVertical,
			alignment: component.AlignmentTop,
			// Note: existing tiles (A/B/C) lose their frame chrome
			// in this top/bottom flip because each ends up too
			// short to fit a top+bottom border plus a content row.
			expected: `
┌──────────────────┐
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
AAAAAAAAAACCCCCCCCCC
AAAAAAAAAACCCCCCCCCC
BBBBBBBBBBBBBBBBBBBB
BBBBBBBBBBBBBBBBBBBB`,
		},
		{
			name:      "horizontal_then_vertical_bottom",
			layout:    splitRootLayoutHorizontalThenVertical,
			alignment: component.AlignmentBottom,
			// Note: same frame-loss caveat as horizontal_then_vertical_top.
			expected: `
AAAAAAAAAACCCCCCCCCC
AAAAAAAAAACCCCCCCCCC
BBBBBBBBBBBBBBBBBBBB
BBBBBBBBBBBBBBBBBBBB
┌──────────────────┐
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘`,
		},
		{
			name:      "vertical_then_horizontal_left",
			layout:    splitRootLayoutVerticalThenHorizontal,
			alignment: component.AlignmentLeft,
			expected: `
┌────┐┌─────┐┌─────┐
│DDDD││AAAAA││BBBBB│
│DDDD││AAAAA││BBBBB│
│DDDD│└─────┘│BBBBB│
│DDDD│┌─────┐│BBBBB│
│DDDD││CCCCC││BBBBB│
│DDDD││CCCCC││BBBBB│
└────┘└─────┘└─────┘`,
		},
		{
			name:      "vertical_then_horizontal_right",
			layout:    splitRootLayoutVerticalThenHorizontal,
			alignment: component.AlignmentRight,
			expected: `
┌────┐┌─────┐┌─────┐
│AAAA││BBBBB││DDDDD│
│AAAA││BBBBB││DDDDD│
└────┘│BBBBB││DDDDD│
┌────┐│BBBBB││DDDDD│
│CCCC││BBBBB││DDDDD│
│CCCC││BBBBB││DDDDD│
└────┘└─────┘└─────┘`,
		},
		{
			name:      "vertical_then_horizontal_top",
			layout:    splitRootLayoutVerticalThenHorizontal,
			alignment: component.AlignmentTop,
			// Note: the existing A/C column loses its frame chrome
			// here for the same reason as horizontal_then_vertical_top —
			// each tile is too short for a full frame.
			expected: `
┌──────────────────┐
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
AAAAAAAAAA┌────────┐
AAAAAAAAAA│BBBBBBBB│
CCCCCCCCCC│BBBBBBBB│
CCCCCCCCCC└────────┘`,
		},
		{
			name:      "vertical_then_horizontal_bottom",
			layout:    splitRootLayoutVerticalThenHorizontal,
			alignment: component.AlignmentBottom,
			// Note: same frame-loss caveat as vertical_then_horizontal_top.
			expected: `
AAAAAAAAAA┌────────┐
AAAAAAAAAA│BBBBBBBB│
CCCCCCCCCC│BBBBBBBB│
CCCCCCCCCC└────────┘
┌──────────────────┐
│DDDDDDDDDDDDDDDDDD│
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := term.NewStringWriter(20, 8)
			wm, win1 := NewWindowManager(
				&component.TestComponent{Ch: 'A'}, testWindowManagerConfig(),
			)
			wm.Resize(20, 8)
			_, _ = buildSplitRootLayout(t, wm, win1, tc.layout)

			cases := []comptest.TestCase{
				{
					Action: func() {
						_, ok := wm.SplitRoot(
							tc.alignment,
							&component.TestComponent{Ch: 'D'},
						)
						require.True(t, ok)
					},
					Expected: tc.expected,
				},
			}

			comptest.TestComponent(t, wm, w, cases)
		})
	}
}

func TestWindowZeroValue(t *testing.T) {
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
			win.SetContent(component.NewString("ballz"))
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

func TestWindowManagerSplit(t *testing.T) {
	w := term.NewStringWriter(20, 8)

	h1 := component.TestComponent{Ch: 'A'}
	wm, w1 := NewWindowManager(&h1, testWindowManagerConfig())
	wm.Resize(20, 8)

	var w2, w3 Window
	var prevFloating tui.Component
	var ok bool
	h2 := component.TestComponent{Ch: 'B'}
	hnop := component.TestComponent{Ch: 0}
	h3 := component.TestComponent{Ch: 'C'}

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
			w2, ok = wm.SplitHorizontal(w1, &h2)
			assert.True(t, ok)
		}, `
┌──────────────────┐
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘
┌──────────────────┐
│BBBBBBBBBBBBBBBBBB│
│BBBBBBBBBBBBBBBBBB│
└──────────────────┘`,
		}, {func() {
			w3, ok = wm.SplitVertical(w2, &h3)
			assert.True(t, ok)
		}, `
┌──────────────────┐
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘
┌────────┐┌────────┐
│BBBBBBBB││CCCCCCCC│
│BBBBBBBB││CCCCCCCC│
└────────┘└────────┘`,
		}, {func() {
			assert.False(t, w2.Closed())
			assert.NoError(t, w2.Close())
			assert.True(t, w2.Closed())
		}, `
┌──────────────────┐
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			assert.False(t, w1.Closed())
			assert.NoError(t, w1.Close())
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
			assert.Error(t, w1.Close())
			assert.True(t, w1.Closed())
			floating := component.StaticFloating(&h2, 2, 2)
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentLeft | component.AlignmentTop,
					Offset:    term.Coordinates{X: 1, Y: 1},
				},
			)
		}, `
┌──────────────────┐
│┌──┐CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
│└──┘CCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentRight | component.AlignmentTop,
					Offset:    term.Coordinates{X: 1, Y: 1},
				},
			)
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCC┌──┐│
│CCCCCCCCCCCCCC│BB││
│CCCCCCCCCCCCCC│BB││
│CCCCCCCCCCCCCC└──┘│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			floating := component.StaticFloating(&h2, 2, 2)
			assert.False(t, w2.Closed())
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentRight | component.AlignmentBottom,
					Offset:    term.Coordinates{X: 1, Y: 1},
				},
			)
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCC┌──┐│
│CCCCCCCCCCCCCC│BB││
│CCCCCCCCCCCCCC│BB││
│CCCCCCCCCCCCCC└──┘│
└──────────────────┘`,
		}, {func() {
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentLeft | component.AlignmentBottom,
					Offset:    term.Coordinates{X: 1, Y: 1},
				},
			)
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│┌──┐CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
│└──┘CCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentHorizontallyCentered,
					Offset:    term.Coordinates{X: 1, Y: 1}, // offset.X is ignored
				},
			)
		}, `
┌──────────────────┐
│CCCCCCC┌──┐CCCCCCC│
│CCCCCCC│BB│CCCCCCC│
│CCCCCCC│BB│CCCCCCC│
│CCCCCCC└──┘CCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentVerticallyCentered,
					Offset:    term.Coordinates{X: 1, Y: 1}, // offset.Y is ignored
				},
			)
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│┌──┐CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
│└──┘CCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			floating := component.StaticFloating(&hnop, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentCentered,
					Offset:    term.Coordinates{X: 1, Y: 1}, // offset is ignored
				},
			)
		}, `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCC┌──┐CCCCCCC│
│CCCCCCC│  │CCCCCCC│
│CCCCCCC│  │CCCCCCC│
│CCCCCCC└──┘CCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentBottom,
					Offset:    term.Coordinates{X: 400, Y: 500},
				},
			)
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
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentTop,
					Offset:    term.Coordinates{X: 400, Y: 500},
				},
			)
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
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentLeft,
					Offset:    term.Coordinates{X: 400, Y: 500},
				},
			)
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
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{
					Alignment: component.AlignmentRight,
					Offset:    term.Coordinates{X: 400, Y: 500},
				},
			)
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
			floating := component.StaticFloating(&h2, 2, 2)
			w2.Close()
			assert.True(t, w2.Closed())
			w2 = wm.FloatingWindow(floating,
				FloatingConfig{},
			)
		}, `
┌──┐───────────────┐
│BB│CCCCCCCCCCCCCCC│
│BB│CCCCCCCCCCCCCCC│
└──┘CCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			// test setting non floating component
			prevFloating = w2.SetContent(&component.TestComponent{Ch: '5'})
		}, `
┌──┐───────────────┐
│55│CCCCCCCCCCCCCCC│
│55│CCCCCCCCCCCCCCC│
└──┘CCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			// test return of SetContent is always what we expect
			prevFloating = w2.SetContent(prevFloating)
			assert.Equal(t, '5', prevFloating.(*component.TestComponent).Ch)
		}, `
┌──┐───────────────┐
│BB│CCCCCCCCCCCCCCC│
│BB│CCCCCCCCCCCCCCC│
└──┘CCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		}, {func() {
			wx, ok := wm.WindowAt(term.Coordinates{})
			assert.True(t, ok)
			assert.Equal(t, w2, wx)

			wx, ok = wm.WindowAt(term.Coordinates{X: 6})
			assert.True(t, ok)
			assert.Equal(t, w3, wx)

			wx, ok = wm.WindowAt(term.Coordinates{Y: 7})
			assert.True(t, ok)
			assert.Equal(t, w3, wx)

			assert.NoError(t, w2.Close())
			wx, ok = wm.WindowAt(term.Coordinates{})
			assert.True(t, ok)
			assert.Equal(t, w3, wx)
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
			wx, ok := wm.WindowAt(term.Coordinates{})
			require.True(t, ok)
			comp := component.TestComponent{Ch: '#'}
			comp.Resize(3, 3)
			wx.SetContentResize(&comp, true)
		}, `
┌──────────────────┐
│##################│
│##################│
│##################│
│##################│
│##################│
│##################│
└──────────────────┘`,
		}, {func() {
			wx, ok := wm.WindowAt(term.Coordinates{})
			require.True(t, ok)
			comp := component.TestComponent{Ch: '$'}
			comp.Resize(3, 3)
			wx.SetContentResize(&comp, false)
		}, `
┌──────────────────┐
│$$$               │
│$$$               │
│$$$               │
│                  │
│                  │
│                  │
└──────────────────┘`,
		},
	}

	comptest.TestComponent(t, wm, w, tests)
}

func TestWindowManagerRestoreTileLayout(t *testing.T) {
	tests := []struct {
		name        string
		frame       bool
		layout      TileLayout
		content     map[uint64]rune
		wantMapped  []uint64
		wantFloats  int
		assertExtra func(*testing.T, *WindowManager, map[uint64]Window)
	}{
		{
			name:       "single leaf no frame",
			layout:     TileLayout{WindowID: 1},
			content:    map[uint64]rune{1: 'A'},
			wantMapped: []uint64{1},
		},
		{
			name:       "single leaf with frame",
			frame:      true,
			layout:     TileLayout{WindowID: 1},
			content:    map[uint64]rune{1: 'B'},
			wantMapped: []uint64{1},
		},
		{
			name: "deep nested layout",
			layout: TileLayout{
				Split: SplitOrientationVertical,
				Children: []TileLayout{
					{WindowID: 1},
					{
						Split: SplitOrientationHorizontal,
						Children: []TileLayout{
							{WindowID: 2},
							{
								Split:    SplitOrientationVertical,
								Children: []TileLayout{{WindowID: 3}, {WindowID: 4}},
							},
						},
					},
				},
			},
			content:    map[uint64]rune{1: 'A', 2: 'B', 3: 'C', 4: 'D'},
			wantMapped: []uint64{1, 2, 3, 4},
		},
		{
			name: "nil content fallback is drawable",
			layout: TileLayout{
				Split:    SplitOrientationHorizontal,
				Children: []TileLayout{{WindowID: 1}, {WindowID: 2}},
			},
			content:    map[uint64]rune{2: 'Z'},
			wantMapped: []uint64{1, 2},
			assertExtra: func(t *testing.T, wm *WindowManager, restored map[uint64]Window) {
				assert.NotPanics(t, func() {
					writer := term.NewStringWriter(20, 8)
					wm.Draw(writer)
				})
				assertEqualTile(t, restored[2], 'Z')
			},
		},
		{
			name: "floating windows cleared",
			layout: TileLayout{
				Split:    SplitOrientationHorizontal,
				Children: []TileLayout{{WindowID: 1}, {WindowID: 2}},
			},
			content:    map[uint64]rune{1: 'L', 2: 'R'},
			wantMapped: []uint64{1, 2},
			wantFloats: 2,
			assertExtra: func(t *testing.T, wm *WindowManager, restored map[uint64]Window) {
				require.Equal(t, 0, wm.SizeFloating())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testWindowManagerConfig()
			cfg.Frame = tt.frame
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'O'}, cfg)
			wm.Resize(20, 8)
			for i := 0; i < tt.wantFloats; i++ {
				wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'F'}, 4, 4),
					FloatingConfig{Alignment: component.AlignmentCentered})
			}

			restored := wm.RestoreTileLayout(tt.layout, func(windowID uint64) tui.Component {
				ch, ok := tt.content[windowID]
				if !ok {
					return nil
				}
				return &component.TestComponent{Ch: ch}
			})

			require.Len(t, restored, len(tt.wantMapped))
			for _, id := range tt.wantMapped {
				require.Contains(t, restored, id)
				require.NotEqual(t, id, restored[id].ID())
				if ch, ok := tt.content[id]; ok {
					assertEqualTile(t, restored[id], ch)
				}
			}
			assertWindowManagerLayoutShape(t, tt.layout, wm.TileLayout(), restored)
			if tt.assertExtra != nil {
				tt.assertExtra(t, wm, restored)
			}
		})
	}
}

func assertWindowManagerLayoutShape(
	t *testing.T,
	want TileLayout,
	got TileLayout,
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
		assertWindowManagerLayoutShape(t, want.Children[i], got.Children[i], restored)
	}
}

func TestWindowManagerMinimize(t *testing.T) {
	w := term.NewStringWriter(20, 8)

	h1 := component.TestComponent{Ch: 'A'}
	wm, _ := NewWindowManager(&h1, testWindowManagerConfig())
	wm.Resize(20, 8)

	var fwin Window
	tests := []comptest.TestCase{
		{
			Action: func() {
				floating := component.StaticFloating(&component.TestComponent{Ch: 'u'}, 2, 2)
				fwin = wm.FloatingWindow(floating,
					FloatingConfig{
						Alignment: component.AlignmentCentered,
					},
				)
				assert.True(t, fwin.MinimizeUp(0))
			}, Expected: `
┌──────────────────┐
┌──────────────────┐
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
│AAAAAAAAAAAAAAAAAA│
└──────────────────┘`,
		}, {Action: func() {
			w := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'l'}, 2, 2),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			assert.True(t, w.MinimizeLeft(0))
			assert.False(t, w.MinimizeLeft(0))

			w = wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'r'}, 2, 2),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			assert.True(t, w.MinimizeRight(0))
			assert.False(t, w.MinimizeRight(0))

			w = wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'd'}, 2, 2),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			assert.True(t, w.MinimizeDown(0))
			assert.False(t, w.MinimizeDown(0))
			assert.False(t, w.MinimizeLeft(0))
			assert.False(t, w.MinimizeRight(0))
			assert.False(t, w.MinimizeUp(0))
		}, Expected: `

┌──────────────────┐
┌┌────────────────┐┐
││AAAAAAAAAAAAAAAA││
││AAAAAAAAAAAAAAAA││
││AAAAAAAAAAAAAAAA││
││AAAAAAAAAAAAAAAA││
└└────────────────┘┘
└──────────────────┘`,
		}, {Action: func() {
			assert.True(t, fwin.Unminimize())
			assert.False(t, fwin.Unminimize())
		}, Expected: `
┌┌────────────────┐┐
││AAAAAA┌──┐AAAAAA││
││AAAAAA│uu│AAAAAA││
││AAAAAA│uu│AAAAAA││
││AAAAAA└──┘AAAAAA││
││AAAAAAAAAAAAAAAA││
└└────────────────┘┘
└──────────────────┘`,
		}, {Action: func() {
			assert.True(t, fwin.MinimizeUp(0))
			w := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'U'}, 2, 2),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			assert.True(t, w.MinimizeUp(1))
			w = wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'L'}, 2, 2),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			assert.True(t, w.MinimizeLeft(1))

			w = wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'R'}, 2, 2),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			assert.True(t, w.MinimizeRight(1))

			w = wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'D'}, 2, 2),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			assert.True(t, w.MinimizeDown(1))
		}, Expected: `
┌──────────────────┐
┌──────────────────┐
│UUUUUUUUUUUUUUUUUU│
┌┌─AAAAAAAAAAAAAA─┐┐
└└─AAAAAAAAAAAAAA─┘┘
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
└──────────────────┘`,
		}, {Action: func() {
			require.NoError(t, fwin.Close())
		}, Expected: `
┌──────────────────┐
│UUUUUUUUUUUUUUUUUU│
┌┌─┌────────────┐─┐┐
││L│AAAAAAAAAAAA│R││
└└─└────────────┘─┘┘
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
└──────────────────┘`,
		}, {Action: func() {
			win, ok := wm.WindowAt(term.Coordinates{X: 3, Y: 2})
			require.True(t, ok)
			assert.Equal(t, 'A', win.Content().(*component.TestComponent).Ch)

			wof, ok := win.TileLeft()
			require.True(t, ok)
			assertEqualTile(t, wof, 'L')

			wof, ok = win.TileRight()
			require.True(t, ok)
			assertEqualTile(t, wof, 'R')

			wof, ok = win.TileUp()
			require.True(t, ok)
			assertEqualTile(t, wof, 'U')

			wof, ok = win.TileDown()
			require.True(t, ok)
			assertEqualTile(t, wof, 'D')

			win, ok = wm.WindowAt(term.Coordinates{X: 0, Y: 0})
			require.True(t, ok)
			assertEqualTile(t, win, 'U')

			wof, ok = win.TileLeft()
			assert.False(t, ok)

			wof, ok = win.TileRight()
			assert.False(t, ok)

			wof, ok = win.TileUp()
			assert.False(t, ok)

			wof, ok = win.TileDown()
			require.True(t, ok)
			assert.Equal(t, 'A', wof.Content().(*component.TestComponent).Ch)

			win, ok = wm.WindowAt(term.Coordinates{X: 0, Y: 2})
			assert.True(t, ok)
			assertEqualTile(t, win, 'l')

			wof, ok = win.TileLeft()
			assert.False(t, ok)

			wof, ok = win.TileRight()
			require.True(t, ok)
			assertEqualTile(t, wof, 'L')

			wof, ok = win.TileUp()
			require.True(t, ok)
			assertEqualTile(t, wof, 'U')

			wof, ok = win.TileDown()
			require.True(t, ok)
			assertEqualTile(t, wof, 'D')

			win, ok = wm.WindowAt(term.Coordinates{X: 19, Y: 2})
			require.True(t, ok)
			assertEqualTile(t, win, 'r')

			wof, ok = win.TileLeft()
			require.True(t, ok)
			assertEqualTile(t, wof, 'R')

			wof, ok = win.TileRight()
			assert.False(t, ok)

			wof, ok = win.TileUp()
			require.True(t, ok)
			assertEqualTile(t, wof, 'U')

			wof, ok = win.TileDown()
			require.True(t, ok)
			assertEqualTile(t, wof, 'D')

			win, ok = wm.WindowAt(term.Coordinates{X: 19, Y: 7})
			require.True(t, ok)
			assertEqualTile(t, win, 'd')

			wof, ok = win.TileLeft()
			assert.False(t, ok)

			wof, ok = win.TileRight()
			assert.False(t, ok)

			wof, ok = win.TileUp()
			require.True(t, ok)
			assertEqualTile(t, wof, 'D')

			wof, ok = win.TileDown()
			assert.False(t, ok)

		}, Expected: `
┌──────────────────┐
│UUUUUUUUUUUUUUUUUU│
┌┌─┌────────────┐─┐┐
││L│AAAAAAAAAAAA│R││
└└─└────────────┘─┘┘
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
└──────────────────┘`,
		}, {Action: func() {
			win, ok := wm.WindowAt(term.Coordinates{Y: 6})
			require.True(t, ok)
			assertEqualTile(t, win, 'D')

			require.True(t, win.Unminimize())
		}, Expected: `
┌──────────────────┐
│UUUUUUUUUUUUUUUUUU│
┌┌─┌────────────┐─┐┐
││L│AAAA┌──┐AAAA│R││
││L│AAAA│DD│AAAA│R││
││L│AAAA└──┘AAAA│R││
└└─└────────────┘─┘┘
└──────────────────┘`,
		}, {Action: func() {
			win, ok := wm.WindowAt(term.Coordinates{Y: 3, X: 9})
			require.True(t, ok)
			assertEqualTile(t, win, 'D')
			require.True(t, win.MinimizeDown(1))

			w := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'X'}, 7, 100),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			wof, ok := w.TileRight()
			require.True(t, ok)
			assert.Equal(t, 'A', wof.Content().(*component.TestComponent).Ch)
		}, Expected: `
┌──────────────────┐
│UUUUUUUUUUUUUUUUUU│
┌┌─┌────────────┐─┐┐
││L│AXXXXXXXXXAA│R││
└└─└────────────┘─┘┘
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
└──────────────────┘`,
		}, {Action: func() {
			win, ok := wm.WindowAt(term.Coordinates{Y: 3, X: 6})
			require.True(t, ok)
			assertEqualTile(t, win, 'X')
			require.NoError(t, win.Close())

			w := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'X'}, 100, 100),
				FloatingConfig{
					Alignment: component.AlignmentCentered,
				},
			)
			wof, ok := w.TileRight()
			require.True(t, ok)
			assertEqualTile(t, wof, 'A')

			wof, ok = w.TileLeft()
			require.True(t, ok)
			assertEqualTile(t, wof, 'A')

			wof, ok = w.TileUp()
			require.True(t, ok)
			assertEqualTile(t, wof, 'A')

			wof, ok = w.TileDown()
			require.True(t, ok)
			assertEqualTile(t, wof, 'A')
		}, Expected: `
┌──────────────────┐
│UUUUUUUUUUUUUUUUUU│
┌┌─┌────────────┐─┐┐
││L│XXXXXXXXXXXX│R││
└└─└────────────┘─┘┘
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
└──────────────────┘`,
		}, {Action: func() {
			assert.NotPanics(t, func() {
				wm.Resize(2, 2)
				wm.WindowAt(term.Coordinates{X: 0, Y: 0})
				wm.WindowAt(term.Coordinates{X: 0, Y: 1})
				wm.WindowAt(term.Coordinates{X: 1, Y: 0})
				wm.WindowAt(term.Coordinates{X: 1, Y: 1})
			})
		}, Expected: `

                    
                    
                    
    X               
                    
                    
                    
                    `,
		}, {Action: func() {
			wm.Resize(20, 8)
			win, ok := wm.WindowAt(term.Coordinates{Y: 3, X: 6})
			require.True(t, ok)
			assertEqualTile(t, win, 'X')
			require.NoError(t, win.Close())

			win, ok = wm.WindowAt(term.Coordinates{Y: 3, X: 6})
			require.True(t, ok)
			assert.Equal(t, 'A', win.Content().(*component.TestComponent).Ch)

			assert.True(t, win.MinimizeUp(0))
			_, ok = wm.SplitVertical(win, &component.TestComponent{Ch: 'a'})
			require.True(t, ok)

			require.True(t, win.MinimizeLeft(0))

			win, ok = wm.WindowAt(term.Coordinates{Y: 2, X: 6})
			require.True(t, ok)
			assert.Equal(t, 'a', win.Content().(*component.TestComponent).Ch)

			wof, ok := win.TileRight()
			require.True(t, ok)
			assertEqualTile(t, wof, 'R')

			wof, ok = win.TileLeft()
			require.True(t, ok)
			assert.Equal(t, 'A', wof.Content().(*component.TestComponent).Ch)

			wof, ok = win.TileUp()
			require.True(t, ok)
			assertEqualTile(t, wof, 'U')

			wof, ok = win.TileDown()
			require.True(t, ok)
			assertEqualTile(t, wof, 'D')
		}, Expected: `
┌──────────────────┐
│UUUUUUUUUUUUUUUUUU│
┌┌─┌─┐┌─────────┐─┐┐
││L│A││aaaaaaaaa│R││
└└─└─┘└─────────┘─┘┘
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
└──────────────────┘`,
		}, {Action: func() {
			win, ok := wm.WindowAt(term.Coordinates{Y: 4, X: 16})
			require.True(t, ok)
			assert.Equal(t, 'a', win.Content().(*component.TestComponent).Ch)

			win, ok = wm.SplitHorizontal(win, &component.TestComponent{Ch: 'b'})
			require.True(t, ok)

			require.True(t, win.MinimizeDown(0))

			win, ok = wm.WindowAt(term.Coordinates{Y: 2, X: 6})
			require.True(t, ok)
			assert.Equal(t, 'A', win.Content().(*component.TestComponent).Ch)

			wof, ok := win.TileRight()
			require.True(t, ok)
			assert.Equal(t, 'b', wof.Content().(*component.TestComponent).Ch)

			wof, ok = win.TileLeft()
			require.True(t, ok)
			assertEqualTile(t, wof, 'L')

			wof, ok = win.TileUp()
			require.True(t, ok)
			assertEqualTile(t, wof, 'U')

			wof, ok = win.TileDown()
			require.True(t, ok)
			assertEqualTile(t, wof, 'D')
		}, Expected: `
┌──────────────────┐
│UUUUUUUUUUUUUUUUUU│
┌┌─┌─────────┐aaa─┐┐
││L│AAAAAAAAA│bbbR││
└└─└─────────┘bbb─┘┘
│DDDDDDDDDDDDDDDDDD│
└──────────────────┘
└──────────────────┘`,
		},
	}

	comptest.TestComponent(t, wm, w, tests)
}

func TestExposedRootTileAt(t *testing.T) {
	h1 := component.TestComponent{Ch: 'A'}
	wm, w1 := NewWindowManager(&h1, testWindowManagerConfig())
	wm.Resize(20, 8)

	_, found := w1.TileUp()
	require.False(t, found)
	_, found = w1.TileLeft()
	require.False(t, found)
	_, found = w1.TileRight()
	require.False(t, found)
	_, found = w1.TileDown()
	require.False(t, found)

	h2 := component.TestComponent{Ch: 'B'}
	w2, ok := wm.SplitVertical(w1, &h2)
	require.True(t, ok)

	_, found = w2.TileUp()
	assert.False(t, found)

	require.NoError(t, w1.Close())

	_, found = w2.TileUp()
	assert.False(t, found)
	_, found = w2.TileLeft()
	assert.False(t, found)
	_, found = w2.TileRight()
	assert.False(t, found)
	_, found = w2.TileDown()
	assert.False(t, found)
}

func TestWindowManagerTileFocusFloating(t *testing.T) {
	w := term.NewStringWriter(20, 8)

	wm, w1 := NewWindowManager(&component.TestComponent{Ch: 'A'}, testWindowManagerConfig())
	wm.Resize(20, 8)

	_, ok := w1.TileUp()
	require.False(t, ok)

	w2, ok := wm.SplitVertical(w1, &component.TestComponent{Ch: 'B'})
	require.True(t, ok)
	wm.SplitHorizontal(w1, &component.TestComponent{Ch: 'C'})
	wm.SplitHorizontal(w2, &component.TestComponent{Ch: 'D'})

	var f1 Window
	tests := []comptest.TestCase{
		{
			Action: func() {
				floating := component.StaticFloating(&component.TestComponent{Ch: 'a'}, 2, 4)
				f1 = wm.FloatingWindow(floating,
					FloatingConfig{
						Alignment: component.AlignmentHorizontallyCentered,
					},
				)
				_, ok := f1.TileUp()
				require.False(t, ok)

				actual, ok := f1.TileLeft()
				require.True(t, ok)
				assertEqualTile(t, actual, 'A')

				actual, ok = f1.TileRight()
				require.True(t, ok)
				assertEqualTile(t, actual, 'B')

				actual, ok = f1.TileDown()
				require.True(t, ok)
				assertEqualTile(t, actual, 'A')
			}, Expected: `
┌───────┌──┐───────┐
│AAAAAAA│aa│BBBBBBB│
│AAAAAAA│aa│BBBBBBB│
└───────│aa│───────┘
┌───────│aa│───────┐
│CCCCCCC└──┘DDDDDDD│
│CCCCCCCC││DDDDDDDD│
└────────┘└────────┘`,
		},
		{
			Action: func() {
				require.NoError(t, f1.Close())

				floating := component.StaticFloating(&component.TestComponent{Ch: 'a'}, 2, 4)
				f1 = wm.FloatingWindow(floating,
					FloatingConfig{
						Alignment: component.AlignmentHorizontallyCentered | component.AlignmentBottom,
					},
				)
				_, ok := f1.TileDown()
				require.False(t, ok)

				actual, ok := f1.TileLeft()
				require.True(t, ok)
				assertEqualTile(t, actual, 'C')

				actual, ok = f1.TileRight()
				require.True(t, ok)
				assertEqualTile(t, actual, 'D')

				actual, ok = f1.TileUp()
				require.True(t, ok)
				assertEqualTile(t, actual, 'D')
			}, Expected: `
┌────────┐┌────────┐
│AAAAAAAA││BBBBBBBB│
│AAAAAAA┌──┐BBBBBBB│
└───────│aa│───────┘
┌───────│aa│───────┐
│CCCCCCC│aa│DDDDDDD│
│CCCCCCC│aa│DDDDDDD│
└───────└──┘───────┘`,
		},
		{
			Action: func() {
				require.NoError(t, f1.Close())

				floating := component.StaticFloating(&component.TestComponent{Ch: 'a'}, 12, 2)
				f1 = wm.FloatingWindow(floating,
					FloatingConfig{
						Alignment: component.AlignmentVerticallyCentered,
					},
				)
				_, ok := f1.TileLeft()
				require.False(t, ok)

				actual, ok := f1.TileUp()
				require.True(t, ok)
				assertEqualTile(t, actual, 'A')

				actual, ok = f1.TileDown()
				require.True(t, ok)
				assertEqualTile(t, actual, 'C')

				actual, ok = f1.TileRight()
				require.True(t, ok)
				assertEqualTile(t, actual, 'A')
			}, Expected: `
┌────────┐┌────────┐
│AAAAAAAA││BBBBBBBB│
┌────────────┐BBBBB│
│aaaaaaaaaaaa│─────┘
│aaaaaaaaaaaa│─────┐
└────────────┘DDDDD│
│CCCCCCCC││DDDDDDDD│
└────────┘└────────┘`,
		},
		{
			Action: func() {
				require.NoError(t, f1.Close())

				floating := component.StaticFloating(&component.TestComponent{Ch: 'a'}, 12, 2)
				f1 = wm.FloatingWindow(floating,
					FloatingConfig{
						Alignment: component.AlignmentVerticallyCentered | component.AlignmentRight,
					},
				)
				_, ok := f1.TileRight()
				require.False(t, ok)

				actual, ok := f1.TileUp()
				require.True(t, ok)
				assertEqualTile(t, actual, 'B')

				actual, ok = f1.TileDown()
				require.True(t, ok)
				assertEqualTile(t, actual, 'D')

				actual, ok = f1.TileLeft()
				require.True(t, ok)
				assertEqualTile(t, actual, 'D')
			}, Expected: `
┌────────┐┌────────┐
│AAAAAAAA││BBBBBBBB│
│AAAAA┌────────────┐
└─────│aaaaaaaaaaaa│
┌─────│aaaaaaaaaaaa│
│CCCCC└────────────┘
│CCCCCCCC││DDDDDDDD│
└────────┘└────────┘`,
		},
	}

	comptest.TestComponent(t, wm, w, tests)
}

func TestComponentWindowAt(t *testing.T) {
	h1 := &component.TestComponent{Ch: '1'}
	wm, w1 := NewWindowManager(h1, testWindowManagerConfig())
	wm.Resize(20, 8)

	h2 := &component.TestComponent{Ch: '2'}
	w2, ok := wm.SplitVertical(w1, h2)
	require.True(t, ok)

	h3 := &component.TestComponent{Ch: '3'}
	w3, ok := wm.SplitHorizontal(w2, h3)
	require.True(t, ok)

	h4 := &component.TestComponent{Ch: '4'}
	w4, ok := wm.SplitVertical(w3, h4)
	require.True(t, ok)

	wf := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'f'}, 100, 100),
		FloatingConfig{
			Alignment: component.AlignmentCentered,
		},
	)
	require.True(t, wf.MinimizeDown(1))

	wF := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'F'}, 2, 2),
		FloatingConfig{
			Alignment: component.AlignmentCentered,
		},
	)

	/*
			  ┌────────┐┌────────┐
		      │1111111┌──┐2222222│
		      │1111111│FF│───────┘
		      │1111111│FF│──┐┌───┐
		      │1111111└──┘33││444│
		      └────────┘└───┘└───┘
		      │ffffffffffffffffff│
		      └──────────────────┘
	*/

	suite := []struct {
		at               term.Coordinates
		expectedOut      Window
		expectedNotFound bool
	}{
		{
			at:          term.Coordinates{X: 10, Y: 3},
			expectedOut: wF,
		},
		{
			at:          term.Coordinates{X: 14, Y: 5},
			expectedOut: w3,
		},
		{
			at:          term.Coordinates{X: 12, Y: 3},
			expectedOut: w3,
		},
		{
			at:          term.Coordinates{X: 19, Y: 2},
			expectedOut: w2,
		},
		{
			at:          term.Coordinates{X: 10, Y: 0},
			expectedOut: w2,
		},
		{
			at:          term.Coordinates{X: 0, Y: 6},
			expectedOut: wf,
		},
		{
			at:          term.Coordinates{X: 0, Y: 7},
			expectedOut: wf,
		},
		{
			at:          term.Coordinates{X: 19, Y: 7},
			expectedOut: wf,
		},
		{
			at:          term.Coordinates{X: 19, Y: 6},
			expectedOut: wf,
		},
		{
			at:          term.Coordinates{X: 0, Y: 5},
			expectedOut: w1,
		},
		{
			at:          term.Coordinates{X: 9, Y: 0},
			expectedOut: w1,
		},
		{
			at:          term.Coordinates{X: 15, Y: 3},
			expectedOut: w4,
		},
		{
			at:          term.Coordinates{X: 19, Y: 5},
			expectedOut: w4,
		},
		{
			at:          term.Coordinates{X: 8, Y: 1},
			expectedOut: wF,
		},
		{
			at:          term.Coordinates{X: 11, Y: 4},
			expectedOut: wF,
		},
	}
	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			actualOut, actualOk := wm.WindowAt(test.at)
			require.Equal(t, !test.expectedNotFound, actualOk)
			assert.Equal(t, test.expectedOut, actualOut)
		})
	}

	wx := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'x'}, 100, 100),
		FloatingConfig{
			Alignment: component.AlignmentCentered,
		},
	)
	require.True(t, wx.MinimizeUp(2))
	wy := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'y'}, 100, 100),
		FloatingConfig{
			Alignment: component.AlignmentCentered,
		},
	)
	require.True(t, wy.MinimizeLeft(2))

	/*
		┌──────────────────┐
		│xxxxxxxxxxxxxxxxxx│
		│xxxxxxxxxxxxxxxxxx│
		┌──┌─────┌──┐2222222
		│yy│11111│FF│3344444
		└──└─────└──┘3344444
		│ffffffffffffffffff│
		└──────────────────┘
	*/

	suite = []struct {
		at               term.Coordinates
		expectedOut      Window
		expectedNotFound bool
	}{
		{
			at:          term.Coordinates{X: 10, Y: 4},
			expectedOut: wF,
		},
		{
			at:          term.Coordinates{X: 13, Y: 4},
			expectedOut: w3,
		},
		{
			at:          term.Coordinates{X: 14, Y: 5},
			expectedOut: w3,
		},
		{
			at:          term.Coordinates{X: 13, Y: 3},
			expectedOut: w2,
		},
		{
			at:          term.Coordinates{X: 19, Y: 3},
			expectedOut: w2,
		},
		{
			at:          term.Coordinates{X: 0, Y: 6},
			expectedOut: wf,
		},
		{
			at:          term.Coordinates{X: 0, Y: 7},
			expectedOut: wf,
		},
		{
			at:          term.Coordinates{X: 19, Y: 7},
			expectedOut: wf,
		},
		{
			at:          term.Coordinates{X: 19, Y: 6},
			expectedOut: wf,
		},
		{
			at:          term.Coordinates{X: 3, Y: 3},
			expectedOut: w1,
		},
		{
			at:          term.Coordinates{X: 8, Y: 5},
			expectedOut: w1,
		},
		{
			at:          term.Coordinates{X: 15, Y: 4},
			expectedOut: w4,
		},
		{
			at:          term.Coordinates{X: 19, Y: 5},
			expectedOut: w4,
		},
		{
			at:          term.Coordinates{X: 9, Y: 4},
			expectedOut: wF,
		},
		{
			at:          term.Coordinates{X: 12, Y: 4},
			expectedOut: wF,
		},
	}
	for i, test := range suite {
		t.Run(fmt.Sprintf("test case %d", i), func(t *testing.T) {
			actualOut, actualOk := wm.WindowAt(test.at)
			require.Equal(t, !test.expectedNotFound, actualOk)
			assert.Equal(t, test.expectedOut, actualOut)
		})
	}
}

// tileAtLayout builds a window manager layout for the TileAt suite and
// returns the windows the expectations refer to by name.
type tileAtLayout struct {
	name  string
	size  term.Coordinates
	build func(t *testing.T, wm *WindowManager, root Window) map[string]Window
	cases []tileAtCase
}

type tileAtCase struct {
	at term.Coordinates
	// want names a window returned by build, or is empty when TileAt
	// must report no tile at all.
	want string
	why  string
}

func TestComponentTileAt(t *testing.T) {
	newTile := func(ch rune) *component.TestComponent {
		return &component.TestComponent{Ch: ch}
	}
	newFloat := func(ch rune, w, h int) component.Floating {
		return component.StaticFloating(newTile(ch), w, h)
	}

	layouts := []tileAtLayout{
		{
			name: "single tile covers the whole surface",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(_ *testing.T, _ *WindowManager, root Window) map[string]Window {
				return map[string]Window{"root": root}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "root"},
				{at: term.Coordinates{X: 19, Y: 7}, want: "root"},
				{at: term.Coordinates{X: 10, Y: 4}, want: "root"},
				{at: term.Coordinates{X: -1, Y: 0}, why: "negative x"},
				{at: term.Coordinates{X: 0, Y: -1}, why: "negative y"},
				{at: term.Coordinates{X: 20, Y: 0}, why: "x past the right edge"},
				{at: term.Coordinates{X: 0, Y: 8}, why: "y past the bottom edge"},
			},
		},
		{
			name: "vertical split",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				right, ok := wm.SplitVertical(root, newTile('r'))
				require.True(t, ok)
				return map[string]Window{"left": root, "right": right}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "left"},
				{at: term.Coordinates{X: 9, Y: 7}, want: "left"},
				{at: term.Coordinates{X: 10, Y: 0}, want: "right"},
				{at: term.Coordinates{X: 19, Y: 7}, want: "right"},
			},
		},
		{
			name: "horizontal split",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				bottom, ok := wm.SplitHorizontal(root, newTile('b'))
				require.True(t, ok)
				return map[string]Window{"top": root, "bottom": bottom}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "top"},
				{at: term.Coordinates{X: 19, Y: 3}, want: "top"},
				{at: term.Coordinates{X: 0, Y: 4}, want: "bottom"},
				{at: term.Coordinates{X: 19, Y: 7}, want: "bottom"},
			},
		},
		{
			name: "nested splits",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				right, ok := wm.SplitVertical(root, newTile('r'))
				require.True(t, ok)
				rightBottom, ok := wm.SplitHorizontal(right, newTile('B'))
				require.True(t, ok)
				leftBottom, ok := wm.SplitHorizontal(root, newTile('L'))
				require.True(t, ok)
				return map[string]Window{
					"leftTop":     root,
					"leftBottom":  leftBottom,
					"rightTop":    right,
					"rightBottom": rightBottom,
				}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "leftTop"},
				{at: term.Coordinates{X: 9, Y: 3}, want: "leftTop"},
				{at: term.Coordinates{X: 0, Y: 4}, want: "leftBottom"},
				{at: term.Coordinates{X: 9, Y: 7}, want: "leftBottom"},
				{at: term.Coordinates{X: 10, Y: 0}, want: "rightTop"},
				{at: term.Coordinates{X: 19, Y: 3}, want: "rightTop"},
				{at: term.Coordinates{X: 10, Y: 4}, want: "rightBottom"},
				{at: term.Coordinates{X: 19, Y: 7}, want: "rightBottom"},
			},
		},
		{
			name: "three vertical siblings",
			size: term.Coordinates{X: 30, Y: 6},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				mid, ok := wm.SplitVertical(root, newTile('m'))
				require.True(t, ok)
				last, ok := wm.SplitVertical(mid, newTile('l'))
				require.True(t, ok)
				return map[string]Window{"first": root, "mid": mid, "last": last}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "first"},
				{at: term.Coordinates{X: 9, Y: 5}, want: "first"},
				{at: term.Coordinates{X: 10, Y: 0}, want: "mid"},
				{at: term.Coordinates{X: 19, Y: 5}, want: "mid"},
				{at: term.Coordinates{X: 20, Y: 0}, want: "last"},
				{at: term.Coordinates{X: 29, Y: 5}, want: "last"},
			},
		},
		{
			name: "a float never shadows the tile underneath",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				right, ok := wm.SplitVertical(root, newTile('r'))
				require.True(t, ok)
				float := wm.FloatingWindow(newFloat('F', 20, 8), FloatingConfig{
					Alignment: component.AlignmentCentered,
				})
				return map[string]Window{"left": root, "right": right, "float": float}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "left"},
				{at: term.Coordinates{X: 9, Y: 4}, want: "left",
					why: "covered by the float, but TileAt ignores floats"},
				{at: term.Coordinates{X: 10, Y: 4}, want: "right",
					why: "covered by the float, but TileAt ignores floats"},
				{at: term.Coordinates{X: 19, Y: 7}, want: "right"},
			},
		},
		{
			name: "stacked floats never shadow tiles",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				bottom, ok := wm.SplitHorizontal(root, newTile('b'))
				require.True(t, ok)
				wm.FloatingWindow(newFloat('F', 10, 4), FloatingConfig{})
				wm.FloatingWindow(newFloat('G', 6, 2), FloatingConfig{
					Alignment: component.AlignmentCentered,
				})
				return map[string]Window{"top": root, "bottom": bottom}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "top"},
				{at: term.Coordinates{X: 10, Y: 4}, want: "bottom"},
			},
		},
		{
			name: "a minimized float shifts the tiled area down",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				bottom, ok := wm.SplitHorizontal(root, newTile('b'))
				require.True(t, ok)
				float := wm.FloatingWindow(newFloat('F', 20, 8), FloatingConfig{})
				require.True(t, float.MinimizeUp(2))
				return map[string]Window{"top": root, "bottom": bottom}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, why: "inside the minimized strip"},
				{at: term.Coordinates{X: 19, Y: 2}, why: "inside the minimized strip"},
				{at: term.Coordinates{X: 0, Y: 3}, want: "top",
					why: "the tiled area starts below the strip"},
				{at: term.Coordinates{X: 19, Y: 7}, want: "bottom"},
			},
		},
		{
			name: "a minimized float shifts the tiled area right",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				right, ok := wm.SplitVertical(root, newTile('r'))
				require.True(t, ok)
				float := wm.FloatingWindow(newFloat('F', 20, 8), FloatingConfig{})
				require.True(t, float.MinimizeLeft(3))
				return map[string]Window{"left": root, "right": right}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, why: "inside the minimized strip"},
				{at: term.Coordinates{X: 3, Y: 7}, why: "inside the minimized strip"},
				{at: term.Coordinates{X: 4, Y: 0}, want: "left"},
				{at: term.Coordinates{X: 19, Y: 7}, want: "right"},
			},
		},
		{
			name: "a bottom-minimized float leaves the trailing rows tileless",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				float := wm.FloatingWindow(newFloat('F', 20, 8), FloatingConfig{})
				require.True(t, float.MinimizeDown(2))
				return map[string]Window{"root": root}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "root"},
				{at: term.Coordinates{X: 19, Y: 4}, want: "root"},
				{at: term.Coordinates{X: 0, Y: 5}, why: "inside the minimized strip"},
				{at: term.Coordinates{X: 19, Y: 7}, why: "inside the minimized strip"},
			},
		},
		{
			name: "fixed-size tiles",
			size: term.Coordinates{X: 30, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				right, ok := wm.SplitVertical(root, newTile('r'))
				require.True(t, ok)
				require.True(t, wm.SetWidth(root, 5))
				return map[string]Window{"left": root, "right": right}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "left"},
				{at: term.Coordinates{X: 4, Y: 7}, want: "left"},
				{at: term.Coordinates{X: 5, Y: 0}, want: "right"},
				{at: term.Coordinates{X: 29, Y: 7}, want: "right"},
			},
		},
		{
			name: "root split spanning the full height",
			size: term.Coordinates{X: 20, Y: 8},
			build: func(t *testing.T, wm *WindowManager, root Window) map[string]Window {
				bottom, ok := wm.SplitHorizontal(root, newTile('b'))
				require.True(t, ok)
				side, ok := wm.SplitRoot(component.AlignmentRight, newTile('s'))
				require.True(t, ok)
				return map[string]Window{"top": root, "bottom": bottom, "side": side}
			},
			cases: []tileAtCase{
				{at: term.Coordinates{X: 0, Y: 0}, want: "top"},
				{at: term.Coordinates{X: 0, Y: 7}, want: "bottom"},
				{at: term.Coordinates{X: 19, Y: 0}, want: "side"},
				{at: term.Coordinates{X: 19, Y: 7}, want: "side",
					why: "a root split spans the full height"},
			},
		},
	}

	for _, layout := range layouts {
		t.Run(layout.name, func(t *testing.T) {
			wm, root := NewWindowManager(&component.TestComponent{Ch: '0'},
				testWindowManagerConfig())
			wm.Resize(layout.size.X, layout.size.Y)
			windows := layout.build(t, wm, root)
			wm.Resize(layout.size.X, layout.size.Y)

			for _, test := range layout.cases {
				t.Run(fmt.Sprintf("%d,%d", test.at.X, test.at.Y), func(t *testing.T) {
					got, ok := wm.TileAt(test.at)
					if test.want == "" {
						assert.False(t, ok, test.why)
						assert.Equal(t, Window{}, got,
							"a missing tile must return the zero Window")
						return
					}
					want, found := windows[test.want]
					require.True(t, found, "unknown window %q", test.want)
					require.True(t, ok, test.why)
					assert.Equal(t, want, got, test.why)
				})
			}
		})
	}
}

func TestComponentTileAtNeverReturnsOverlays(t *testing.T) {
	wm, root := NewWindowManager(&component.TestComponent{Ch: '1'},
		testWindowManagerConfig())
	wm.Resize(24, 10)

	right, ok := wm.SplitVertical(root, &component.TestComponent{Ch: '2'})
	require.True(t, ok)
	bottom, ok := wm.SplitHorizontal(right, &component.TestComponent{Ch: '3'})
	require.True(t, ok)

	float := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'F'}, 8, 4),
		FloatingConfig{Alignment: component.AlignmentCentered},
	)
	strip := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'm'}, 24, 10),
		FloatingConfig{},
	)
	require.True(t, strip.MinimizeDown(2))
	wm.Resize(24, 10)

	tiles := []Window{root, right, bottom}
	overlays := []Window{float, strip}

	resolved := 0
	for y := range 10 {
		for x := range 24 {
			at := term.Coordinates{X: x, Y: y}
			got, ok := wm.TileAt(at)
			if !ok {
				continue
			}
			resolved++
			assert.NotContains(t, overlays, got,
				"TileAt(%v) resolved to an overlay window", at)
			require.Contains(t, tiles, got, "TileAt(%v) resolved to an unknown window", at)

			pos := got.Position()
			assert.True(t,
				x >= pos.X && x < pos.X+got.Width() &&
					y >= pos.Y && y < pos.Y+got.Height(),
				"TileAt(%v) returned a tile at %v sized %dx%d that does not contain it",
				at, pos, got.Width(), got.Height())
		}
	}
	assert.Positive(t, resolved, "the sweep must resolve at least one tile")
}

func TestFixedSizeWindows(t *testing.T) {
	h1 := &component.TestComponent{Ch: '1'}
	wm, w1 := NewWindowManager(h1, testWindowManagerConfig())
	wm.Resize(20, 8)

	var w2, w3, w4, wf, wF Window
	tests := []comptest.TestCase{
		{
			Action: func() {

				assert.False(t, wm.SetHeight(w1, 3))
				// it's the only window so it should fail
				require.False(t, wm.SetWidth(w1, 3))
				assert.Equal(t, 20, w1.MaxWidth())
				assert.Equal(t, 8, w1.MaxHeight())
				assert.Equal(t, 3, w1.MinWidth())
				assert.Equal(t, 3, w1.MinHeight())

				h2 := &component.TestComponent{Ch: '2'}
				var ok bool
				w2, ok = wm.SplitVertical(w1, h2)
				require.True(t, ok)

				assert.Equal(t, 17, w1.MaxWidth())
				assert.Equal(t, 8, w1.MaxHeight())
				assert.Equal(t, 17, w2.MaxWidth())
				assert.Equal(t, 8, w2.MaxHeight())

				assert.False(t, wm.SetHeight(w2, 3))
				require.True(t, wm.SetWidth(w1, 3))
				// should reset the fixed width of w1
				require.True(t, wm.SetWidth(w2, 3))

				// cannot resize to less than content size 1
				// with frame, that is less than 3.
				require.False(t, wm.SetWidth(w2, 2))

			}, Expected: `
┌───────────────┐┌─┐
│111111111111111││2│
│111111111111111││2│
│111111111111111││2│
│111111111111111││2│
│111111111111111││2│
│111111111111111││2│
└───────────────┘└─┘`,
		},
		{
			Action: func() {
				h3 := &component.TestComponent{Ch: '3'}
				var ok bool
				w3, ok = wm.SplitHorizontal(w2, h3)
				require.True(t, ok)
				assert.Equal(t, 17, w1.MaxWidth())
				assert.Equal(t, 8, w1.MaxHeight())
				assert.Equal(t, 17, w2.MaxWidth())
				assert.Equal(t, 5, w2.MaxHeight())
				assert.Equal(t, 17, w3.MaxWidth())
				assert.Equal(t, 5, w3.MaxHeight())
			}, Expected: `
┌───────────────┐┌─┐
│111111111111111││2│
│111111111111111││2│
│111111111111111│└─┘
│111111111111111│┌─┐
│111111111111111││3│
│111111111111111││3│
└───────────────┘└─┘`,
		},
		{
			Action: func() {
				assert.True(t, wm.SetHeight(w3, 3))
				assert.True(t, wm.SetWidth(w3, 13))
			}, Expected: `
┌─────┐┌───────────┐
│11111││22222222222│
│11111││22222222222│
│11111││22222222222│
│11111│└───────────┘
│11111│┌───────────┐
│11111││33333333333│
└─────┘└───────────┘`,
		},
		{
			Action: func() {
				h4 := &component.TestComponent{Ch: '4'}
				var ok bool
				w4, ok = wm.SplitVertical(w3, h4)
				require.True(t, ok)
				assert.Equal(t, 17, w1.MaxWidth()) // let's keep things simple
				assert.Equal(t, 8, w1.MaxHeight())
				assert.Equal(t, 17, w2.MaxWidth())
				assert.Equal(t, 5, w2.MaxHeight())
				assert.Equal(t, 10, w3.MaxWidth()) // ditto
				assert.Equal(t, 5, w3.MaxHeight())
				assert.Equal(t, 10, w4.MaxWidth()) // ditto
				assert.Equal(t, 5, w3.MaxHeight())
			}, Expected: `
┌─────┐┌───────────┐
│11111││22222222222│
│11111││22222222222│
│11111││22222222222│
│11111│└───────────┘
│11111│┌────┐┌─────┐
│11111││3333││44444│
└─────┘└────┘└─────┘`,
		},
		{
			Action: func() {
				wf = wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'f'}, 100, 100),
					FloatingConfig{
						Alignment: component.AlignmentCentered,
					},
				)
				require.True(t, wf.MinimizeDown(1))

				wF = wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'F'}, 2, 2),
					FloatingConfig{
						Alignment: component.AlignmentCentered,
					},
				)

				assert.Equal(t, 17, w1.MaxWidth())
				assert.Equal(t, 6, w1.MaxHeight())
				assert.Equal(t, 17, w2.MaxWidth())
				assert.Equal(t, 3, w2.MaxHeight())
				assert.Equal(t, 10, w3.MaxWidth())
				assert.Equal(t, 3, w3.MaxHeight())
				assert.Equal(t, 10, w4.MaxWidth())
				assert.Equal(t, 3, w3.MaxHeight())
			}, Expected: `
┌─────┐┌───────────┐
│11111││┌──┐2222222│
│11111│└│FF│───────┘
│11111│┌│FF│┐┌─────┐
│11111││└──┘││44444│
└─────┘└────┘└─────┘
│ffffffffffffffffff│
└──────────────────┘`,
		},
		{
			Action: func() {
				assert.False(t, wm.SetHeight(w4, w4.Height()+1))
				assert.True(t, wm.SetWidth(w4, w4.Width()+2))
			}, Expected: `
┌─────┐┌───────────┐
│11111││┌──┐2222222│
│11111│└│FF│───────┘
│11111│┌│FF│───────┐
│11111││└──┘4444444│
└─────┘└──┘└───────┘
│ffffffffffffffffff│
└──────────────────┘`,
		},
		{
			Action: func() {
				assert.False(t, wm.SetHeight(w2, w2.Height()+1))
				assert.True(t, wm.SetWidth(w2, w2.Width()+2))
				assert.False(t, wm.SetWidth(w2, w2.Width()+8))
			}, Expected: `
┌───┐┌─────────────┐
│111││22┌──┐2222222│
│111│└──│FF│───────┘
│111│┌──│FF│───────┐
│111││33└──┘4444444│
└───┘└────┘└───────┘
│ffffffffffffffffff│
└──────────────────┘`,
		},
		{
			Action: func() {
				assert.False(t, wm.SetHeight(wf, 5)) // is minimized
				assert.False(t, wm.SetWidth(wf, 5))  // is minimized
				assert.True(t, wm.SetHeight(wF, wF.Height()+1))
				assert.True(t, wm.SetWidth(wF, wF.Width()+1))

				assert.Equal(t, 4, wF.MinHeight())
				assert.Equal(t, 4, wF.MinWidth())
				assert.Equal(t, 6, wF.MaxHeight())
				assert.Equal(t, 20, wF.MaxWidth())
			}, Expected: `
┌───┐┌─────────────┐
│111││2┌───┐2222222│
│111│└─│FFF│───────┘
│111│┌─│FFF│───────┐
│111││3└───┘4444444│
└───┘└────┘└───────┘
│ffffffffffffffffff│
└──────────────────┘`,
		},
		{
			Action: func() {
				assert.True(t, wm.SetHeight(wF, 0)) // reset
				assert.True(t, wm.SetWidth(wF, 0))  // reset
			}, Expected: `
┌───┐┌─────────────┐
│111││22┌──┐2222222│
│111│└──│FF│───────┘
│111│┌──│FF│───────┐
│111││33└──┘4444444│
└───┘└────┘└───────┘
│ffffffffffffffffff│
└──────────────────┘`,
		},
		{
			Action: func() {
				require.NoError(t, w3.Close())
				require.NoError(t, w1.Close())
			}, Expected: `
┌──────────────────┐
│2222222┌──┐2222222│
└───────│FF│───────┘
┌───────│FF│───────┐
│4444444└──┘4444444│
└──────────────────┘
│ffffffffffffffffff│
└──────────────────┘`,
		},
		{
			Action: func() {
				require.NoError(t, wf.Close())
				require.NoError(t, wF.Close())
				require.NoError(t, w2.Close())
				require.Error(t, w4.Close())
			}, Expected: `
┌──────────────────┐
│444444444444444444│
│444444444444444444│
│444444444444444444│
│444444444444444444│
│444444444444444444│
│444444444444444444│
└──────────────────┘`,
		},
	}

	w := term.NewStringWriter(20, 8)
	comptest.TestComponent(t, wm, w, tests)
}

func TestSetFrameAttr(t *testing.T) {
	h1 := &component.TestComponent{Ch: '1'}
	cfg := testWindowManagerConfig()
	wm, w1 := NewWindowManager(h1, cfg)
	wm.Resize(20, 8)

	newAttr := term.Attributes{Fg: term.ColorGreen, Bg: term.ColorBlue}
	prev, ok := w1.SetFrameAttr(newAttr)
	assert.True(t, ok)
	assert.Equal(t, cfg.FrameAttr, prev)

	actualAttr, ok := w1.SetFrameAttr(prev)
	assert.True(t, ok)
	assert.Equal(t, newAttr, actualAttr)
}

func assertEqualTile(t *testing.T, win Window, expected rune) {
	t.Helper()
	if f, ok := win.Content().(interface{ Content() tui.Component }); ok {
		assert.Equal(t, string(expected), string(f.Content().(*component.TestComponent).Ch))
	} else {
		assert.Equal(t, string(expected), string(win.Content().(*component.TestComponent).Ch))
	}

}

func testWindowManagerConfig() WindowManagerConfig {
	ret := DefaultWindowManagerConfig()
	ret.NoMaxSize = true
	// bar rendering is covered by the TestWindowBar* tests
	ret.WindowBar = false
	return ret
}

func TestWindowManagerIterateCloseDuringIteration(t *testing.T) {
	h := component.TestComponent{Ch: 'A'}
	wm, _ := NewWindowManager(&h, testWindowManagerConfig())
	wm.Resize(20, 8)

	for range 3 {
		wm.FloatingWindow(
			component.StaticFloating(&component.TestComponent{Ch: 'f'}, 2, 2),
			FloatingConfig{Alignment: component.AlignmentCentered},
		)
	}

	var visited int
	assert.NotPanics(t, func() {
		wm.Iterate(func(w Window) {
			visited++
			_ = w.Close()
		})
	})
	// 1 root tile + 3 floating windows
	assert.Equal(t, 4, visited)
	assert.Equal(t, 0, wm.SizeFloating())
}

func TestWindowManagerIterateCloseTilesDuringIteration(t *testing.T) {
	h := component.TestComponent{Ch: 'A'}
	wm, keep := NewWindowManager(&h, testWindowManagerConfig())
	wm.Resize(40, 8)

	// Flat vertical layout of four sibling tiles, mirroring repeated
	// `windownew right` over the focused window.
	for i := range 3 {
		_, ok := wm.SplitVertical(keep, &component.TestComponent{Ch: rune('B' + i)})
		require.True(t, ok)
	}
	require.Equal(t, 4, wm.SizeTiles())

	// Mimic CloseOtherWindows(keep): visit every tile and close all but
	// the kept one.
	var visited int
	assert.NotPanics(t, func() {
		wm.Iterate(func(w Window) {
			visited++
			if w.node == keep.node {
				return
			}
			_ = w.Close()
		})
	})

	assert.Equal(t, 4, visited, "every tile must be visited exactly once")
	assert.Equal(t, 1, wm.SizeTiles(), "all sibling tiles except the kept one must be closed")

	// The survivor must still be positionable; a detached tile here is
	// what crashed the host on the next cursor calculation.
	assert.NotPanics(t, func() { _ = keep.Position() })
}

func TestWindowBarDraw(t *testing.T) {
	newFloating := func() component.Floating {
		return component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2)
	}
	tests := []struct {
		name     string
		open     func(wm *WindowManager)
		expected string
	}{
		{
			name: "floating window draws bar and close icon",
			open: func(wm *WindowManager) {
				wm.FloatingWindow(newFloating(), FloatingConfig{
					Offset: term.Coordinates{X: 1, Y: 1},
				})
			},
			expected: `
┌──────────────────┐
│█●██CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
│└──┘CCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
		{
			name: "NoBar floating window keeps plain frame",
			open: func(wm *WindowManager) {
				wm.FloatingWindow(newFloating(), FloatingConfig{
					Offset: term.Coordinates{X: 1, Y: 1},
					NoBar:  true,
				})
			},
			expected: `
┌──────────────────┐
│┌──┐CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
│└──┘CCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := term.NewStringWriter(20, 8)
			cfg := DefaultWindowManagerConfig()
			cfg.NoMaxSize = true
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
			wm.Resize(20, 8)
			comptest.TestComponent(t, wm, w, []comptest.TestCase{
				{Action: func() { tt.open(wm) }, Expected: tt.expected},
			})
		})
	}
}

func TestWindowBarSetFrameCharSet(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	cfg.NoMaxSize = true
	wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
	wm.Resize(20, 8)
	win := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2),
		FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}},
	)

	win.SetFrameCharSet(component.FrameCharSetDefault())
	cs, ok := win.FrameCharSet()
	require.True(t, ok)
	assert.Equal(t, '█', cs.TopLeft)
	assert.Equal(t, '█', cs.HorizontalTop)
	assert.Equal(t, '█', cs.TopRight)
	assert.Equal(t, '─', cs.HorizontalBottom)

	wm.SetFrameCharSet(component.FrameCharSetDefault())
	cs, ok = win.FrameCharSet()
	require.True(t, ok)
	assert.Equal(t, '█', cs.TopLeft)

	// the tile keeps the plain charset
	wm.tree.Iterate(func(node *TileNode) {
		tileCS := node.Content().(*component.Frame).FrameCharSet
		assert.Equal(t, '┌', tileCS.TopLeft)
	})
}

func TestWindowBarTitleDraw(t *testing.T) {
	newFloating := func() component.Floating {
		return component.StaticFloating(&component.TestComponent{Ch: 'B'}, 10, 2)
	}
	tests := []struct {
		name     string
		open     func(wm *WindowManager)
		expected string
	}{
		{
			name: "config title is centered on the bar",
			open: func(wm *WindowManager) {
				wm.FloatingWindow(newFloating(), FloatingConfig{
					Offset: term.Coordinates{X: 1, Y: 1},
					Title:  "cmd",
				})
			},
			expected: `
┌──────────────────┐
│█●██ cmd ███CCCCCC│
││BBBBBBBBBB│CCCCCC│
││BBBBBBBBBB│CCCCCC│
│└──────────┘CCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
		{
			name: "long titles are truncated to the bar",
			open: func(wm *WindowManager) {
				wm.FloatingWindow(newFloating(), FloatingConfig{
					Offset: term.Coordinates{X: 1, Y: 1},
					Title:  "aVeryLongTitleThatOverflows",
				})
			},
			expected: `
┌──────────────────┐
│█●█ aVeryLo█CCCCCC│
││BBBBBBBBBB│CCCCCC│
││BBBBBBBBBB│CCCCCC│
│└──────────┘CCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
		{
			name: "NoBar windows draw no title",
			open: func(wm *WindowManager) {
				wm.FloatingWindow(newFloating(), FloatingConfig{
					Offset: term.Coordinates{X: 1, Y: 1},
					Title:  "cmd",
					NoBar:  true,
				})
			},
			expected: `
┌──────────────────┐
│┌──────────┐CCCCCC│
││BBBBBBBBBB│CCCCCC│
││BBBBBBBBBB│CCCCCC│
│└──────────┘CCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := term.NewStringWriter(20, 8)
			cfg := DefaultWindowManagerConfig()
			cfg.NoMaxSize = true
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
			wm.Resize(20, 8)
			comptest.TestComponent(t, wm, w, []comptest.TestCase{
				{Action: func() { tt.open(wm) }, Expected: tt.expected},
			})
		})
	}
}

// cellRecorder captures the last cell written at each coordinate so
// tests can assert cell attributes, which string writers drop.
type cellRecorder struct {
	term.Writer
	cells map[term.Coordinates]term.Cell
}

func (r *cellRecorder) SetCell(pos term.Coordinates, c term.Cell) {
	r.cells[pos] = c
	r.Writer.SetCell(pos, c)
}

func TestWindowBarAttrs(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	cfg.NoMaxSize = true
	cfg.FrameAttr = term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
	wm.Resize(20, 8)
	wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 10, 2),
		FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}, Title: "cmd"},
	)

	rec := &cellRecorder{
		Writer: term.NewStringWriter(20, 8),
		cells:  make(map[term.Coordinates]term.Cell),
	}
	wm.Draw(rec)

	icon := rec.cells[term.Coordinates{X: 1 + WindowBarCloseIconX, Y: 1}]
	assert.Equal(t, '●', icon.Ch)
	assert.Equal(t, term.ColorRed, icon.Fg,
		"icon keeps the configured foreground")
	assert.Equal(t, term.ColorBlue, icon.Bg,
		"icon background is the bar foreground")

	title := rec.cells[term.Coordinates{X: 6, Y: 1}]
	assert.Equal(t, 'c', title.Ch)
	assert.Equal(t, term.ColorBlack, title.Fg,
		"title foreground is the frame background")
	assert.Equal(t, term.ColorBlue, title.Bg,
		"title background is the bar foreground")
}

func TestWindowManagerToggleMaximize(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	cfg.NoMaxSize = true
	wm, tile := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
	wm.Resize(20, 8)
	win := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2),
		FloatingConfig{Alignment: component.AlignmentCentered},
	)
	require.Equal(t, term.Coordinates{X: 8, Y: 2}, win.Position())

	assert.False(t, wm.ToggleMaximize(tile), "tiles cannot be maximized")

	require.True(t, wm.ToggleMaximize(win))
	assert.Equal(t, term.Coordinates{}, win.Position())
	assert.Equal(t, 20, win.Width())
	assert.Equal(t, 8, win.Height())

	// a maximized window tracks manager resizes
	wm.Resize(30, 10)
	assert.Equal(t, 30, win.Width())
	assert.Equal(t, 10, win.Height())

	// restore returns to the pre-maximize geometry
	require.True(t, wm.ToggleMaximize(win))
	assert.Equal(t, term.Coordinates{X: 13, Y: 3}, win.Position())
	assert.Equal(t, 4, win.Width())
	assert.Equal(t, 4, win.Height())

	// moving a maximized window drops the maximized state
	require.True(t, wm.ToggleMaximize(win))
	require.True(t, wm.MoveWindow(win, term.Coordinates{X: 2, Y: 2}))
	assert.Equal(t, term.Coordinates{X: 2, Y: 2}, win.Position())
	assert.Equal(t, 4, win.Width())
	assert.Equal(t, 4, win.Height())

	// resizing a maximized window drops the maximized state
	require.True(t, wm.ToggleMaximize(win))
	require.True(t, wm.SetWidth(win, 6))
	require.True(t, wm.MoveWindow(win, term.Coordinates{X: 0, Y: 0}))
	assert.Equal(t, 6, win.Width())
	assert.Equal(t, 4, win.Height())

	// minimized windows cannot be maximized
	require.True(t, win.MinimizeDown(0))
	assert.False(t, wm.ToggleMaximize(win))
}

func TestWindowManagerMoveWindow(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	cfg.NoMaxSize = true
	wm, tile := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
	wm.Resize(20, 8)
	// 2x2 content + frame = 4x4 window
	win := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2),
		FloatingConfig{Alignment: component.AlignmentCentered},
	)

	assert.False(t, wm.MoveWindow(tile, term.Coordinates{X: 1, Y: 1}),
		"tiles cannot be moved")

	require.True(t, wm.MoveWindow(win, term.Coordinates{X: 3, Y: 2}))
	assert.Equal(t, term.Coordinates{X: 3, Y: 2}, win.Position())

	at, ok := wm.WindowAt(term.Coordinates{X: 4, Y: 3})
	require.True(t, ok)
	assert.Equal(t, win.ID(), at.ID())
	at, ok = wm.WindowAt(term.Coordinates{X: 1, Y: 1})
	require.True(t, ok)
	assert.Equal(t, tile.ID(), at.ID())

	// clamp to full visibility: window is 4x4 in a 20x8 manager
	require.True(t, wm.MoveWindow(win, term.Coordinates{X: 100, Y: 100}))
	assert.Equal(t, term.Coordinates{X: 16, Y: 4}, win.Position())
	require.True(t, wm.MoveWindow(win, term.Coordinates{X: -3, Y: -3}))
	assert.Equal(t, term.Coordinates{X: 0, Y: 0}, win.Position())

	// a minimized floating window cannot be moved
	require.True(t, win.MinimizeDown(0))
	assert.False(t, wm.MoveWindow(win, term.Coordinates{X: 1, Y: 1}))
	require.True(t, win.Unminimize())

	// moved position round-trips through TileLayout/RestoreTileLayout
	require.True(t, wm.MoveWindow(win, term.Coordinates{X: 5, Y: 3}))
	layout := wm.TileLayout()
	require.Len(t, layout.Floating, 1)
	assert.Equal(t, term.Coordinates{X: 5, Y: 3}, layout.Floating[0].Offset)

	restored := wm.RestoreTileLayout(layout, func(windowID uint64) tui.Component {
		if windowID == layout.Floating[0].WindowID {
			return component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2)
		}
		return &component.TestComponent{Ch: 'C'}
	})
	restoredWin, ok := restored[layout.Floating[0].WindowID]
	require.True(t, ok)
	assert.Equal(t, term.Coordinates{X: 5, Y: 3}, restoredWin.Position())
}

func TestFloatingLayoutBarRoundTrip(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	cfg.NoMaxSize = true
	wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
	wm.Resize(20, 8)
	titled := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2),
		FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}, Title: "cmd"},
	)
	noBar := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2),
		FloatingConfig{Offset: term.Coordinates{X: 8, Y: 1}, NoBar: true},
	)

	layout := wm.TileLayout()
	require.Len(t, layout.Floating, 2)

	restored := wm.RestoreTileLayout(layout, func(windowID uint64) tui.Component {
		for _, fl := range layout.Floating {
			if windowID == fl.WindowID {
				return component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2)
			}
		}
		return &component.TestComponent{Ch: 'C'}
	})

	restoredTitled, ok := restored[titled.ID()]
	require.True(t, ok)
	assert.True(t, restoredTitled.HasWindowBar())
	assert.Equal(t, "cmd", restoredTitled.Title())

	restoredNoBar, ok := restored[noBar.ID()]
	require.True(t, ok)
	assert.False(t, restoredNoBar.HasWindowBar())
	assert.Empty(t, restoredNoBar.Title())
}

func TestFloatingWindowUserResizeShrinks(t *testing.T) {
	for _, noMaxSize := range []bool{false, true} {
		t.Run(fmt.Sprintf("NoMaxSize=%v", noMaxSize), func(t *testing.T) {
			cfg := DefaultWindowManagerConfig()
			cfg.NoMaxSize = noMaxSize
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
			wm.Resize(20, 10)
			// 6x4 content + frame = 8x6 desired
			win := wm.FloatingWindow(
				component.StaticFloating(&component.TestComponent{Ch: 'B'}, 6, 4),
				FloatingConfig{},
			)
			require.Equal(t, 8, win.Width())
			require.Equal(t, 6, win.Height())

			require.True(t, wm.SetWidth(win, 5))
			require.True(t, wm.SetHeight(win, 4))
			require.True(t, wm.MoveWindow(win, term.Coordinates{}))
			assert.Equal(t, 5, win.Width())
			assert.Equal(t, 4, win.Height())

			require.True(t, wm.SetWidth(win, 0))
			require.True(t, wm.SetHeight(win, 0))
			require.True(t, wm.MoveWindow(win, term.Coordinates{}))
			assert.Equal(t, 8, win.Width())
			assert.Equal(t, 6, win.Height())
		})
	}
}

func TestForegroundFloating(t *testing.T) {
	cfg := DefaultWindowManagerConfig()
	cfg.NoMaxSize = true
	wm, tiled := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
	wm.Resize(20, 10)

	a := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'A'}, 2, 2),
		FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}},
	)
	b := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2),
		FloatingConfig{Offset: term.Coordinates{X: 4, Y: 1}},
	)

	floatOrder := func() []uint64 {
		var ids []uint64
		wm.Iterate(func(win Window) {
			if _, ok := win.node.(*floatingNode); ok {
				ids = append(ids, win.ID())
			}
		})
		return ids
	}

	assert.Equal(t, []uint64{a.ID(), b.ID()}, floatOrder())

	wm.ForegroundFloating(a)
	assert.Equal(t, []uint64{b.ID(), a.ID()}, floatOrder())

	wm.ForegroundFloating(a)
	assert.Equal(t, []uint64{b.ID(), a.ID()}, floatOrder())

	wm.ForegroundFloating(tiled)
	assert.Equal(t, []uint64{b.ID(), a.ID()}, floatOrder())
}
