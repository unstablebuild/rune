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
	"cmp"
	"fmt"
	"image"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
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

func TestWindowManagerFrameGraphics(t *testing.T) {
	pics := newFramePictures(t)
	every := func(name string) component.FrameCharSetGraphics {
		return pics.resolve(component.FrameCharSetGraphics{
			TopLeft: name, TopRight: name, BottomLeft: name, BottomRight: name,
			HorizontalTop: name, HorizontalBottom: name,
			VerticalLeft: name, VerticalRight: name,
		})
	}
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}

	tests := []struct {
		name      string
		content   tui.Component
		graphics  component.FrameCharSetGraphics
		scrollBar component.ScrollBarCharSetGraphics
		windowBar bool
		noStorage bool
		open      func(wm *WindowManager, tile Window)
		loads     int
		expected  string
	}{
		{
			name:     "tiles draw their own images",
			graphics: every("L"),
			open: func(wm *WindowManager, tile Window) {
				win, ok := wm.SplitVertical(tile, &component.TestComponent{Ch: 'D'})
				require.True(t, ok)
				cs := wm.config.FrameCharSet
				cs.Graphics = every("R")
				win.SetFrameCharSet(cs)
			},
			loads: 2,
			expected: `
LLLLLLRRRRRR
LCCCCLRDDDDR
LCCCCLRDDDDR
LCCCCLRDDDDR
LLLLLLRRRRRR`,
		},
		{
			name:      "a floating window's bar keeps its runes",
			graphics:  every("L"),
			windowBar: true,
			open: func(wm *WindowManager, _ Window) {
				wm.FloatingWindow(
					component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 1),
					FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}},
				)
			},
			loads: 1,
			expected: `
LLLLLLLLLLLL
L█●██CCCCCCL
LLBBLCCCCCCL
LLLLLCCCCCCL
LLLLLLLLLLLL`,
		},
		{
			name:     "the scroll bar keeps its rune",
			content:  scrollingContent{TestComponent: &component.TestComponent{Ch: 'C'}},
			graphics: every("R"),
			loads:    1,
			expected: `
RRRRRRRRRRRR
RCCCCCCCCCC┃
RCCCCCCCCCC┃
RCCCCCCCCCCR
RRRRRRRRRRRR`,
		},
		{
			name:      "the scroll bar draws its own images",
			content:   scrollingContent{TestComponent: &component.TestComponent{Ch: 'C'}},
			graphics:  every("R"),
			scrollBar: component.ScrollBarCharSetGraphics{Top: "L", Bottom: "L"},
			loads:     2,
			expected: `
RRRRRRRRRRRR
RCCCCCCCCCCL
RCCCCCCCCCCL
RCCCCCCCCCCR
RRRRRRRRRRRR`,
		},
		{
			name:      "frames without storage draw their runes",
			graphics:  every("L"),
			noStorage: true,
			expected: `
┌──────────┐
│CCCCCCCCCC│
│CCCCCCCCCC│
│CCCCCCCCCC│
└──────────┘`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testWindowManagerConfig()
			cfg.WindowBar = tt.windowBar
			cfg.FrameAttr = attr
			cfg.ScrollBarAttr = attr
			cfg.ScrollBarCharSet = component.ScrollBarCharSet{
				Middle:   '┃',
				Graphics: pics.resolveScrollBar(tt.scrollBar),
			}
			cfg.FrameCharSet.Graphics = tt.graphics
			if !tt.noStorage {
				cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
			}
			irq := make(interruptCounter, 16)
			cfg.Interrupter = irq
			content := tt.content
			if content == nil {
				content = &component.TestComponent{Ch: 'C'}
			}
			wm, tile := NewWindowManager(content, cfg)
			t.Cleanup(func() { assert.NoError(t, wm.Close()) })
			wm.Resize(12, 5)
			if tt.open != nil {
				tt.open(wm, tile)
			}
			wm.Draw(term.NewStringWriter(12, 5))
			irq.wait(t, tt.loads)

			s := newImageScreen(12, 5)
			wm.Draw(s)
			assert.Equal(t, tt.expected, s.render(t, pics, attr, false))
		})
	}
}

func TestWindowBarGraphics(t *testing.T) {
	pics := newFramePictures(t)
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	every := WindowBarGraphics{Left: "L", Horizontal: "R", Right: "L"}
	overlay := every
	overlay.Overlay = true
	solidIcon := term.Attributes{Fg: term.ColorRed, Bg: attr.Fg}
	solidTitle := term.Attributes{Fg: attr.Bg, Bg: attr.Fg}
	runes := `
┌──────────┐
│█●██ ab ██│
││BBBBBBBB││
│└────────┘│
└──────────┘`

	tests := []struct {
		name      string
		graphics  WindowBarGraphics
		noBar     bool
		noImages  bool
		notLoaded bool
		expected  string
		// underneath, when set, is what the screen shows under the images.
		underneath string
		// below are the columns of the images drawn under the bar's text.
		below []int
		// icon and title are the attributes of the close icon and of the
		// title's first letter.
		icon, title term.Attributes
	}{
		{
			name:     "every slot is drawn under the icon and title",
			graphics: every,
			expected: `
┌──────────┐
│L●RR ab RL│
││BBBBBBBB││
│└────────┘│
└──────────┘`,
			below: []int{2, 5, 6, 7, 8},
			icon:  term.Attributes{Fg: term.ColorRed, Bg: attr.Bg},
			title: attr,
		},
		{
			name:     "overlay keeps the bar's colors under the text",
			graphics: overlay,
			expected: `
┌──────────┐
│L●RR ab RL│
││BBBBBBBB││
│└────────┘│
└──────────┘`,
			underneath: runes,
			below:      []int{2, 5, 6, 7, 8},
			icon:       solidIcon,
			title:      solidTitle,
		},
		{
			name:     "only the ends leave the middle to its runes",
			graphics: WindowBarGraphics{Left: "L", Right: "L"},
			expected: `
┌──────────┐
│L●██ ab █L│
││BBBBBBBB││
│└────────┘│
└──────────┘`,
			icon:  solidIcon,
			title: solidTitle,
		},
		{
			name:     "a writer that cannot draw images gets the runes",
			graphics: every,
			noImages: true,
			expected: runes,
			icon:     solidIcon,
			title:    solidTitle,
		},
		{
			name:      "images that are not loaded yet leave the runes",
			graphics:  every,
			notLoaded: true,
			expected:  runes,
			icon:      solidIcon,
			title:     solidTitle,
		},
		{
			name:     "a window without a bar draws no bar images",
			graphics: every,
			noBar:    true,
			expected: `
┌──────────┐
│┌────────┐│
││BBBBBBBB││
│└────────┘│
└──────────┘`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testWindowManagerConfig()
			cfg.WindowBar = true
			cfg.FrameAttr = attr
			cfg.WindowBarCharSet.Graphics = pics.resolveWindowBar(tt.graphics)
			cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
			irq := make(interruptCounter, 16)
			cfg.Interrupter = irq
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
			t.Cleanup(func() { assert.NoError(t, wm.Close()) })
			wm.Resize(12, 5)
			wm.FloatingWindow(
				component.StaticFloating(&component.TestComponent{Ch: 'B'}, 8, 2),
				FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}, Title: "ab", NoBar: tt.noBar},
			)
			loads := pics.loadsWindowBar(tt.graphics)
			if tt.noBar {
				loads = 0
			}
			if !tt.notLoaded {
				wm.Draw(term.NewStringWriter(12, 5))
				irq.wait(t, loads)
			}

			s := newImageScreen(12, 5)
			s.noImages = tt.noImages
			wm.Draw(s)
			assert.Equal(t, tt.expected, s.render(t, pics, attr, tt.graphics.Overlay))
			if tt.underneath != "" {
				assert.Equal(t, tt.underneath, "\n"+s.String())
			}
			var below []int
			for _, img := range s.images {
				if img.Layer == term.ImageLayerBelowText {
					assert.Equal(t, 1, img.Pos.Y)
					below = append(below, img.Pos.X)
				}
			}
			slices.Sort(below)
			assert.Equal(t, tt.below, below)
			if !tt.noBar {
				assert.Equal(t, tt.icon, s.Cells()[1*12+2].Attributes(), "close icon")
				assert.Equal(t, tt.title, s.Cells()[1*12+6].Attributes(), "title")
			}

			if tt.notLoaded {
				irq.wait(t, loads)
			}
		})
	}
}

func TestWindowBarGraphicsOutset(t *testing.T) {
	pics := newFramePictures(t)
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	tests := []struct {
		name     string
		outset   WindowBarOutset
		noImages bool
		expected string
		// ends are the cell rectangles of the left and right images.
		ends []image.Rectangle
	}{
		{
			name:   "the ends reach past their cells and are clipped at the edges",
			outset: WindowBarOutset{Top: 1, Bottom: 1, Sides: 2},
			expected: `
┌────────────┐
│LLLCCCCCCCCLL
│LLL●RRRRRRRLL
│LLLBBBBBBBBLL
│CC│BBBBBBBB││
│CC│BBBBBBBB││
└──└────────┘┘`,
			ends: []image.Rectangle{image.Rect(1, 1, 4, 4), image.Rect(12, 1, 15, 4)},
		},
		{
			name:   "inward ends cover the bar, its close icon and the content",
			outset: WindowBarOutset{Top: 1, Bottom: 1, Inward: 2},
			expected: `
┌────────────┐
│CCLLLCCCCLLL│
│CCLLLRRRRLLL│
│CCLLLBBBBLLL│
│CC│BBBBBBBB││
│CC│BBBBBBBB││
└──└────────┘┘`,
			ends: []image.Rectangle{image.Rect(3, 1, 6, 4), image.Rect(10, 1, 13, 4)},
		},
		{
			name:   "the horizontal images cover the rows above, also over the bar's text",
			outset: WindowBarOutset{HorizontalTop: 1},
			expected: `
┌────────────┐
│CCCRRRRRRRRC│
│CCL●RRRRRRRL│
│CC│BBBBBBBB││
│CC│BBBBBBBB││
│CC│BBBBBBBB││
└──└────────┘┘`,
			ends: []image.Rectangle{image.Rect(3, 2, 4, 3), image.Rect(12, 2, 13, 3)},
		},
		{
			name:   "negative outsets keep the ends in their cells",
			outset: WindowBarOutset{Top: -1, Bottom: -1, Sides: -1, Inward: -1, HorizontalTop: -1},
			expected: `
┌────────────┐
│CCCCCCCCCCCC│
│CCL●RRRRRRRL│
│CC│BBBBBBBB││
│CC│BBBBBBBB││
│CC│BBBBBBBB││
└──└────────┘┘`,
			ends: []image.Rectangle{image.Rect(3, 2, 4, 3), image.Rect(12, 2, 13, 3)},
		},
		{
			name:     "a writer that cannot draw images keeps the runes",
			outset:   WindowBarOutset{Top: 1, Bottom: 1, Sides: 2},
			noImages: true,
			expected: `
┌────────────┐
│CCCCCCCCCCCC│
│CC█●████████│
│CC│BBBBBBBB││
│CC│BBBBBBBB││
│CC│BBBBBBBB││
└──└────────┘┘`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testWindowManagerConfig()
			cfg.WindowBar = true
			cfg.FrameAttr = attr
			cfg.WindowBarCharSet.Graphics = pics.resolveWindowBar(WindowBarGraphics{
				Left: "L", Horizontal: "R", Right: "L", Outset: tt.outset,
			})
			cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
			irq := make(interruptCounter, 16)
			cfg.Interrupter = irq
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
			t.Cleanup(func() { assert.NoError(t, wm.Close()) })
			wm.Resize(14, 7)
			wm.FloatingWindow(
				component.StaticFloating(&component.TestComponent{Ch: 'B'}, 8, 3),
				FloatingConfig{Offset: term.Coordinates{X: 3, Y: 2}},
			)
			wm.Draw(term.NewStringWriter(14, 7))
			irq.wait(t, 2)

			s := newImageScreen(14, 7)
			s.noImages = tt.noImages
			wm.Draw(s)
			assert.Equal(t, tt.expected, s.render(t, pics, attr, false))
			var ends []image.Rectangle
			for _, img := range s.images {
				if img.Width > 1 || img.Pos.X == 3 || img.Pos.X == 12 {
					ends = append(ends, img.Bounds())
				}
			}
			assert.Equal(t, tt.ends, ends)
			if !tt.noImages {
				for _, x := range []int{3, 12} {
					assert.Equal(t, term.NewCell(' ', 1, attr), s.Cells()[2*14+x],
						"the end's own cell at %d is blank under its image", x)
				}
			}
		})
	}
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

// floatGeometry is where a window sits and how large it is.
type floatGeometry struct {
	pos  term.Coordinates
	w, h int
}

func geometry(x, y, w, h int) floatGeometry {
	return floatGeometry{pos: term.Coordinates{X: x, Y: y}, w: w, h: h}
}

func geometryOf(win Window) floatGeometry {
	return floatGeometry{pos: win.Position(), w: win.Width(), h: win.Height()}
}

func xy(x, y int) term.Coordinates {
	return term.Coordinates{X: x, Y: y}
}

// newFloatTestManager returns a 20x8 manager holding a tile of 'C' and
// a float of w by h cells of 'B', which the default config frames into
// a window two cells wider and taller.
func newFloatTestManager(
	t *testing.T, config func(*WindowManagerConfig), w, h int, cfg FloatingConfig,
) (wm *WindowManager, tile, float Window) {
	t.Helper()
	wmCfg := DefaultWindowManagerConfig()
	if config != nil {
		config(&wmCfg)
	}
	wm, tile = NewWindowManager(&component.TestComponent{Ch: 'C'}, wmCfg)
	wm.Resize(20, 8)
	float = wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, w, h), cfg)
	return wm, tile, float
}

// newMinimizedFloat opens a 2x2 float of 'm' and minimizes it.
func newMinimizedFloat(
	t *testing.T, wm *WindowManager, minimize func(Window, int) bool,
) Window {
	t.Helper()
	win := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'm'}, 2, 2),
		FloatingConfig{})
	require.True(t, minimize(win, 0))
	return win
}

func TestWindowManagerMoveWindow(t *testing.T) {
	unframed := func(cfg *WindowManagerConfig) { cfg.Frame = false }
	fullSize := func(cfg *WindowManagerConfig) { cfg.NoMaxSize = false }
	// Unless a case says otherwise, 2x2 content is framed into a 4x4
	// window in a 20x8 manager, which caps windows two cells short of
	// its own size.
	suite := []struct {
		name   string
		config func(*WindowManagerConfig)
		w, h   int
		to     term.Coordinates
		want   floatGeometry
	}{
		{name: "inside the manager", to: xy(3, 2), want: geometry(3, 2, 4, 4)},
		{name: "at the top-left corner", to: xy(0, 0), want: geometry(0, 0, 4, 4)},
		{name: "flush with the bottom-right corner",
			to: xy(16, 4), want: geometry(16, 4, 4, 4)},
		{name: "partly past the left edge", to: xy(-1, 3), want: geometry(-1, 3, 4, 4)},
		{name: "far past the left edge keeps three bar cells on screen",
			to: xy(-40, 3), want: geometry(-1, 3, 4, 4)},
		{name: "partly past the right edge", to: xy(17, 3), want: geometry(17, 3, 4, 4)},
		{name: "far past the right edge keeps three bar cells on screen",
			to: xy(40, 3), want: geometry(17, 3, 4, 4)},
		{name: "partly past the bottom edge", to: xy(5, 6), want: geometry(5, 6, 4, 4)},
		{name: "with only the bar row above the bottom edge",
			to: xy(5, 7), want: geometry(5, 7, 4, 4)},
		{name: "far past the bottom edge keeps the bar row on screen",
			to: xy(5, 40), want: geometry(5, 7, 4, 4)},
		{name: "past the top edge keeps the bar row on screen",
			to: xy(5, -40), want: geometry(5, 0, 4, 4)},
		{name: "past the top-left corner", to: xy(-40, -40), want: geometry(-1, 0, 4, 4)},
		{name: "past the top-right corner", to: xy(40, -40), want: geometry(17, 0, 4, 4)},
		{name: "past the bottom-left corner", to: xy(-40, 40), want: geometry(-1, 7, 4, 4)},
		{name: "past the bottom-right corner", to: xy(40, 40), want: geometry(17, 7, 4, 4)},
		{name: "a wide window past the left edge",
			w: 10, to: xy(-40, 1), want: geometry(-9, 1, 12, 4)},
		{name: "a wide window past the right edge",
			w: 10, to: xy(40, 1), want: geometry(17, 1, 12, 4)},
		{name: "a tall window past the bottom edge",
			h: 4, to: xy(3, 6), want: geometry(3, 6, 4, 6)},
		{name: "a window capped two cells short of the manager",
			w: 30, to: xy(-40, 0), want: geometry(-15, 0, 18, 4)},
		{name: "a window wider than the manager is trimmed to it",
			config: fullSize, w: 30, to: xy(-40, 0), want: geometry(-17, 0, 20, 4)},
		{name: "a window taller than the manager is trimmed to it",
			config: fullSize, h: 30, to: xy(3, 40), want: geometry(3, 7, 4, 8)},
		{name: "a window three cells wide stays fully on screen",
			config: unframed, w: 3, to: xy(-40, 1), want: geometry(0, 1, 3, 2)},
		{name: "a window narrower than three cells stays on screen at the left edge",
			config: unframed, to: xy(-40, 1), want: geometry(0, 1, 2, 2)},
		{name: "a window narrower than three cells stays on screen at the right edge",
			config: unframed, to: xy(40, 1), want: geometry(18, 1, 2, 2)},
		{name: "a one-cell window past the bottom-right corner",
			config: unframed, w: 1, h: 1, to: xy(40, 40), want: geometry(19, 7, 1, 1)},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			wm, _, float := newFloatTestManager(t, test.config,
				cmp.Or(test.w, 2), cmp.Or(test.h, 2),
				FloatingConfig{Alignment: component.AlignmentCentered})
			require.True(t, wm.MoveWindow(float, test.to))
			assert.Equal(t, test.want, geometryOf(float))
		})
	}
}

func TestWindowManagerMoveWindowRefuses(t *testing.T) {
	suite := []struct {
		name   string
		target func(t *testing.T, tile, float Window) Window
	}{
		{"a tile", func(_ *testing.T, tile, _ Window) Window { return tile }},
		{"a minimized float", func(t *testing.T, _, float Window) Window {
			require.True(t, float.MinimizeDown(0))
			return float
		}},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			wm, tile, float := newFloatTestManager(t, nil, 2, 2,
				FloatingConfig{Alignment: component.AlignmentCentered})
			win := test.target(t, tile, float)
			before := geometryOf(win)

			assert.False(t, wm.MoveWindow(win, xy(-1, 6)))
			assert.Equal(t, before, geometryOf(win))
			float.Unminimize()
			assert.Equal(t, geometry(8, 2, 4, 4), geometryOf(float),
				"a refused move must leave the float laid out as never moved")
		})
	}
}

func TestFloatingWindowTrimsUntilMoved(t *testing.T) {
	// 2x2 content is framed into a 4x4 window in a 20x8 manager. Moving
	// the window to where it was placed restores its size.
	suite := []struct {
		name   string
		cfg    FloatingConfig
		placed floatGeometry
		moved  floatGeometry
	}{
		{name: "inside the manager", cfg: FloatingConfig{Offset: xy(3, 2)},
			placed: geometry(3, 2, 4, 4), moved: geometry(3, 2, 4, 4)},
		{name: "centered", cfg: FloatingConfig{Alignment: component.AlignmentCentered},
			placed: geometry(8, 2, 4, 4), moved: geometry(8, 2, 4, 4)},
		{name: "partly past the right edge", cfg: FloatingConfig{Offset: xy(17, 2)},
			placed: geometry(17, 2, 3, 4), moved: geometry(17, 2, 4, 4)},
		{name: "on the last column", cfg: FloatingConfig{Offset: xy(19, 2)},
			placed: geometry(19, 2, 1, 4), moved: geometry(17, 2, 4, 4)},
		{name: "partly past the bottom edge", cfg: FloatingConfig{Offset: xy(5, 6)},
			placed: geometry(5, 6, 4, 2), moved: geometry(5, 6, 4, 4)},
		{name: "on the last row", cfg: FloatingConfig{Offset: xy(5, 7)},
			placed: geometry(5, 7, 4, 1), moved: geometry(5, 7, 4, 4)},
		{name: "past the bottom-right corner", cfg: FloatingConfig{Offset: xy(40, 40)},
			placed: geometry(40, 40, 0, 0), moved: geometry(17, 7, 4, 4)},
		{name: "before the left edge", cfg: FloatingConfig{Offset: xy(-3, 2)},
			placed: geometry(0, 2, 4, 4), moved: geometry(0, 2, 4, 4)},
		{name: "above the top edge", cfg: FloatingConfig{Offset: xy(3, -3)},
			placed: geometry(3, 0, 4, 4), moved: geometry(3, 0, 4, 4)},
		{name: "right-aligned past the right edge",
			cfg:    FloatingConfig{Alignment: component.AlignmentRight, Offset: xy(-3, 2)},
			placed: geometry(19, 2, 1, 4), moved: geometry(17, 2, 4, 4)},
		{name: "bottom-aligned past the bottom edge",
			cfg:    FloatingConfig{Alignment: component.AlignmentBottom, Offset: xy(3, -3)},
			placed: geometry(3, 7, 4, 1), moved: geometry(3, 7, 4, 4)},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			wm, _, float := newFloatTestManager(t, nil, 2, 2, test.cfg)
			assert.Equal(t, test.placed, geometryOf(float))

			require.True(t, wm.MoveWindow(float, float.Position()))
			assert.Equal(t, test.moved, geometryOf(float))
		})
	}
}

func TestMovedFloatingWindowFollowsManager(t *testing.T) {
	type step struct {
		name string
		do   func(t *testing.T, wm *WindowManager, float Window)
		want floatGeometry
	}
	resize := func(w, h int) func(*testing.T, *WindowManager, Window) {
		return func(_ *testing.T, wm *WindowManager, _ Window) { wm.Resize(w, h) }
	}
	minimizeAnother := func(
		minimize func(Window, int) bool,
	) func(*testing.T, *WindowManager, Window) {
		return func(t *testing.T, wm *WindowManager, _ Window) {
			newMinimizedFloat(t, wm, minimize)
		}
	}
	setSize := func(w, h int) func(*testing.T, *WindowManager, Window) {
		return func(t *testing.T, wm *WindowManager, float Window) {
			require.True(t, wm.SetWidth(float, w))
			require.True(t, wm.SetHeight(float, h))
			wm.Resize(20, 8)
		}
	}
	toggleMaximize := func(t *testing.T, wm *WindowManager, float Window) {
		require.True(t, wm.ToggleMaximize(float))
	}
	// 2x2 content is framed into a 4x4 window in a 20x8 manager, which
	// caps windows two cells short of its own size.
	suite := []struct {
		name  string
		to    term.Coordinates
		steps []step
	}{
		{name: "the manager grows", to: xy(5, 3), steps: []step{
			{"grown", resize(30, 10), geometry(5, 3, 4, 4)},
		}},
		{name: "the manager shrinks under a window past the bottom-right corner",
			to: xy(17, 7), steps: []step{
				{"shrunk", resize(10, 4), geometry(7, 3, 4, 2)},
				{"grown back", resize(20, 8), geometry(17, 7, 4, 4)},
			}},
		{name: "the manager shrinks under a window past the left edge",
			to: xy(-1, 3), steps: []step{
				{"shrunk", resize(10, 6), geometry(-1, 3, 4, 4)},
			}},
		{name: "the manager shrinks below the window size", to: xy(2, 1), steps: []step{
			{"shrunk", resize(3, 3), geometry(2, 1, 1, 1)},
			{"grown back", resize(20, 8), geometry(2, 1, 4, 4)},
		}},
		{name: "the manager is emptied", to: xy(5, 3), steps: []step{
			{"emptied", resize(0, 0), geometry(0, 0, 0, 0)},
			{"grown back", resize(20, 8), geometry(5, 3, 4, 4)},
		}},
		{name: "the window is maximized", to: xy(-1, 6), steps: []step{
			{"maximized", toggleMaximize, geometry(0, 0, 20, 8)},
			{"restored", toggleMaximize, geometry(-1, 6, 4, 4)},
		}},
		{name: "the window is minimized", to: xy(-1, 6), steps: []step{
			{"minimized and restored", func(t *testing.T, _ *WindowManager, float Window) {
				require.True(t, float.MinimizeDown(0))
				require.True(t, float.Unminimize())
			}, geometry(-1, 6, 4, 4)},
		}},
		{name: "another window is minimized to the left", to: xy(-1, 6), steps: []step{
			{"minimized", minimizeAnother(Window.MinimizeLeft), geometry(0, 6, 4, 4)},
		}},
		{name: "another window is minimized below", to: xy(5, 7), steps: []step{
			{"minimized", minimizeAnother(Window.MinimizeDown), geometry(5, 6, 4, 4)},
		}},
		{name: "the user widens the window past the right edge", to: xy(15, 2), steps: []step{
			{"widened", setSize(10, 4), geometry(15, 2, 10, 4)},
		}},
		{name: "the user widens the window beyond the manager", to: xy(15, 2), steps: []step{
			{"widened", setSize(30, 4), geometry(15, 2, 18, 4)},
		}},
		{name: "the user shrinks a window past the bottom-right corner",
			to: xy(17, 7), steps: []step{
				{"shrunk", setSize(3, 3), geometry(17, 7, 3, 3)},
			}},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			wm, _, float := newFloatTestManager(t, nil, 2, 2, FloatingConfig{})
			require.True(t, wm.MoveWindow(float, test.to))
			for _, step := range test.steps {
				step.do(t, wm, float)
				assert.Equal(t, step.want, geometryOf(float), step.name)
			}
		})
	}
}

func TestFloatingLayoutMovedRoundTrip(t *testing.T) {
	moveTo := func(x, y int) func(*testing.T, *WindowManager, Window) {
		return func(t *testing.T, wm *WindowManager, float Window) {
			require.True(t, wm.MoveWindow(float, xy(x, y)))
		}
	}
	// 2x2 content is framed into a 4x4 window in a 20x8 manager.
	suite := []struct {
		name  string
		cfg   FloatingConfig
		place func(t *testing.T, wm *WindowManager, float Window)
		// restoreIn is the manager size to restore the layout in, or
		// zero to keep 20x8.
		restoreIn  term.Coordinates
		unminimize bool
		wantLayout FloatingLayout
		want       floatGeometry
	}{
		{
			name:       "a window never moved stays trimmed",
			cfg:        FloatingConfig{Offset: xy(17, 6)},
			wantLayout: FloatingLayout{Offset: xy(17, 6)},
			want:       geometry(17, 6, 3, 2),
		},
		{
			name:       "an aligned window moved inside the manager",
			cfg:        FloatingConfig{Alignment: component.AlignmentCentered},
			place:      moveTo(5, 3),
			wantLayout: FloatingLayout{Offset: xy(5, 3), Moved: true},
			want:       geometry(5, 3, 4, 4),
		},
		{
			name:       "a window moved past the left edge",
			place:      moveTo(-1, 6),
			wantLayout: FloatingLayout{Offset: xy(-1, 6), Moved: true},
			want:       geometry(-1, 6, 4, 4),
		},
		{
			name:       "a window moved past the bottom-right corner",
			place:      moveTo(40, 40),
			wantLayout: FloatingLayout{Offset: xy(17, 7), Moved: true},
			want:       geometry(17, 7, 4, 4),
		},
		{
			name:       "a moved window restored in a smaller manager",
			place:      moveTo(17, 7),
			restoreIn:  xy(10, 4),
			wantLayout: FloatingLayout{Offset: xy(17, 7), Moved: true},
			want:       geometry(7, 3, 4, 2),
		},
		{
			name: "a moved window that is minimized",
			place: func(t *testing.T, wm *WindowManager, float Window) {
				moveTo(-1, 6)(t, wm, float)
				require.True(t, float.MinimizeDown(0))
			},
			unminimize: true,
			wantLayout: FloatingLayout{
				Offset: xy(-1, 6), Moved: true,
				MinimizedAlignment: component.AlignmentBottom,
			},
			want: geometry(-1, 6, 4, 4),
		},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			wm, _, float := newFloatTestManager(t, nil, 2, 2, test.cfg)
			if test.place != nil {
				test.place(t, wm, float)
			}
			layout := wm.TileLayout()
			require.Len(t, layout.Floating, 1)
			got := layout.Floating[0]
			id := got.WindowID
			got.WindowID = 0
			assert.Equal(t, test.wantLayout, got)

			if test.restoreIn != (term.Coordinates{}) {
				wm.Resize(test.restoreIn.X, test.restoreIn.Y)
			}
			restored := wm.RestoreTileLayout(layout, func(windowID uint64) tui.Component {
				if windowID == id {
					return component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2)
				}
				return &component.TestComponent{Ch: 'C'}
			})
			win, ok := restored[id]
			require.True(t, ok)
			if test.unminimize {
				require.True(t, win.Unminimize())
			}
			assert.Equal(t, test.want, geometryOf(win))
		})
	}
}

func TestMovedFloatingWindowDraw(t *testing.T) {
	moveTo := func(x, y int) func(*testing.T, *WindowManager, Window) {
		return func(t *testing.T, wm *WindowManager, float Window) {
			require.True(t, wm.MoveWindow(float, xy(x, y)))
		}
	}
	minimizeThenMoveTo := func(
		minimize func(Window, int) bool, x, y int,
	) func(*testing.T, *WindowManager, Window) {
		return func(t *testing.T, wm *WindowManager, float Window) {
			newMinimizedFloat(t, wm, minimize)
			wm.ForegroundFloating(float)
			moveTo(x, y)(t, wm, float)
		}
	}
	// Unless a case says otherwise, 4x2 content is framed into a 6x4
	// window in a 20x8 manager.
	suite := []struct {
		name     string
		w        int
		cfg      FloatingConfig
		place    func(t *testing.T, wm *WindowManager, float Window)
		expected string
	}{
		{
			name:  "past the left edge",
			place: moveTo(-2, 2),
			expected: `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
████CCCCCCCCCCCCCCC│
BBB│CCCCCCCCCCCCCCC│
BBB│CCCCCCCCCCCCCCC│
───┘CCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
		{
			name:  "past the right edge",
			place: moveTo(16, 2),
			expected: `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCC█●██
│CCCCCCCCCCCCCCC│BBB
│CCCCCCCCCCCCCCC│BBB
│CCCCCCCCCCCCCCC└───
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
		{
			name:  "past the bottom edge",
			place: moveTo(5, 6),
			expected: `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCC█●████CCCCCCCC│
└────│BBBB│────────┘`,
		},
		{
			name:  "with only the bar row above the bottom edge",
			place: moveTo(5, 7),
			expected: `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└────█●████────────┘`,
		},
		{
			name:  "past the bottom-right corner",
			place: moveTo(17, 6),
			expected: `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCC█●█
└────────────────│BB`,
		},
		{
			name:  "with its title clipped past the left edge",
			w:     10,
			cfg:   FloatingConfig{Title: "abc"},
			place: moveTo(-6, 2),
			expected: `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
bc ███CCCCCCCCCCCCC│
BBBBB│CCCCCCCCCCCCC│
BBBBB│CCCCCCCCCCCCC│
─────┘CCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`,
		},
		{
			name: "never moved at the same spot is trimmed instead",
			cfg:  FloatingConfig{Offset: xy(16, 5)},
			expected: `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCC█●██
│CCCCCCCCCCCCCCC│BB│
└───────────────└──┘`,
		},
		{
			name:  "beside a window minimized to the left",
			place: minimizeThenMoveTo(Window.MinimizeLeft, 0, 2),
			expected: `
┌┌─────────────────┐
││CCCCCCCCCCCCCCCCC│
│●████CCCCCCCCCCCCC│
│BBBB│CCCCCCCCCCCCC│
│BBBB│CCCCCCCCCCCCC│
│────┘CCCCCCCCCCCCC│
││CCCCCCCCCCCCCCCCC│
└└─────────────────┘`,
		},
		{
			name:  "beside a window minimized to the right",
			place: minimizeThenMoveTo(Window.MinimizeRight, 15, 2),
			expected: `
┌─────────────────┐┐
│CCCCCCCCCCCCCCCCC││
│CCCCCCCCCCCCCC█●██│
│CCCCCCCCCCCCCC│BBB│
│CCCCCCCCCCCCCC│BBB│
│CCCCCCCCCCCCCC└───│
│CCCCCCCCCCCCCCCCC││
└─────────────────┘┘`,
		},
		{
			name:  "above a window minimized below",
			place: minimizeThenMoveTo(Window.MinimizeDown, 8, 6),
			expected: `
┌──────────────────┐
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└───────█●████─────┘
└──────────────────┘`,
		},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			wm, _, float := newFloatTestManager(t, nil, cmp.Or(test.w, 4), 2, test.cfg)
			comptest.TestComponent(t, wm, term.NewStringWriter(20, 8), []comptest.TestCase{
				{Action: func() {
					if test.place != nil {
						test.place(t, wm, float)
					}
				}, Expected: test.expected},
			})
		})
	}
}

func TestWindowAtMovedFloatingWindow(t *testing.T) {
	wm, tile, float := newFloatTestManager(t, nil, 4, 2, FloatingConfig{})
	left := newMinimizedFloat(t, wm, Window.MinimizeLeft)
	bottom := newMinimizedFloat(t, wm, Window.MinimizeDown)
	wm.ForegroundFloating(float)
	// The 6x4 window spans columns 0 to 5 and rows 5 to 8, but the
	// floating area only spans columns 1 to 19 and rows 0 to 6.
	require.True(t, wm.MoveWindow(float, xy(0, 5)))
	require.Equal(t, geometry(0, 5, 6, 4), geometryOf(float))
	windows := map[string]Window{
		"float": float, "tile": tile, "left": left, "bottom": bottom,
	}

	suite := []struct {
		name string
		at   term.Coordinates
		// want names the window at the position, or is empty when no
		// window is there.
		want string
	}{
		{"the bar row", xy(3, 5), "float"},
		{"the first visible column", xy(1, 6), "float"},
		{"the last visible cell", xy(5, 6), "float"},
		{"right of the window", xy(6, 6), "tile"},
		{"above the window", xy(3, 4), "tile"},
		{"under the left minimized bar", xy(0, 5), "left"},
		{"under the left minimized bar, a row down", xy(0, 6), "left"},
		{"under the bottom minimized bar", xy(3, 7), "bottom"},
		{"under the bottom minimized bar, at the window's edge", xy(5, 7), "bottom"},
		{"left of the manager", xy(-1, 5), ""},
		{"below the manager", xy(3, 8), ""},
		{"right of the manager", xy(20, 5), ""},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			at, ok := wm.WindowAt(test.at)
			if test.want == "" {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, windows[test.want].ID(), at.ID())
		})
	}
}

func TestWindowManagerInFloatingArea(t *testing.T) {
	type point struct {
		at   term.Coordinates
		want bool
	}
	suite := []struct {
		name     string
		size     term.Coordinates
		minimize func(t *testing.T, wm *WindowManager)
		points   []point
	}{
		{
			name: "no minimized windows",
			size: xy(20, 8),
			points: []point{
				{xy(0, 0), true}, {xy(19, 7), true}, {xy(10, 4), true},
				{xy(-1, 0), false}, {xy(0, -1), false},
				{xy(20, 0), false}, {xy(0, 8), false},
			},
		},
		{
			name: "a window minimized on every side",
			size: xy(20, 8),
			minimize: func(t *testing.T, wm *WindowManager) {
				for _, minimize := range []func(Window, int) bool{
					Window.MinimizeUp, Window.MinimizeDown,
					Window.MinimizeLeft, Window.MinimizeRight,
				} {
					newMinimizedFloat(t, wm, minimize)
				}
			},
			points: []point{
				{xy(1, 1), true}, {xy(18, 6), true}, {xy(10, 4), true},
				{xy(0, 3), false}, {xy(19, 3), false},
				{xy(5, 0), false}, {xy(5, 7), false}, {xy(0, 0), false},
			},
		},
		{
			name: "a padded window minimized below",
			size: xy(20, 8),
			minimize: func(t *testing.T, wm *WindowManager) {
				win := wm.FloatingWindow(
					component.StaticFloating(&component.TestComponent{Ch: 'm'}, 2, 2),
					FloatingConfig{})
				require.True(t, win.MinimizeDown(2))
			},
			points: []point{
				{xy(5, 4), true}, {xy(5, 5), false}, {xy(5, 7), false},
			},
		},
		{
			name:   "an empty manager",
			points: []point{{xy(0, 0), false}},
		},
	}
	for _, test := range suite {
		t.Run(test.name, func(t *testing.T) {
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'},
				DefaultWindowManagerConfig())
			wm.Resize(test.size.X, test.size.Y)
			if test.minimize != nil {
				test.minimize(t, wm)
			}
			for _, p := range test.points {
				assert.Equal(t, p.want, wm.InFloatingArea(p.at), "at %+v", p.at)
			}
		})
	}
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

// nestedTileLayout lays out [[a|b] / c] | d, in that order, so that b's right edge is
// the outer divider and a's bottom edge is [a|b]'s. sizes relayouts and returns the
// {width, height} of each.
func nestedTileLayout(t *testing.T) (wm *WindowManager, wins [4]Window, sizes func() [4][2]int) {
	t.Helper()
	wm, a := NewWindowManager(&component.TestComponent{Ch: 'A'}, testWindowManagerConfig())
	wm.Resize(40, 12)
	d, ok := wm.SplitVertical(a, &component.TestComponent{Ch: 'D'})
	require.True(t, ok)
	c, ok := wm.SplitHorizontal(a, &component.TestComponent{Ch: 'C'})
	require.True(t, ok)
	b, ok := wm.SplitVertical(a, &component.TestComponent{Ch: 'B'})
	require.True(t, ok)
	wins = [4]Window{a, b, c, d}
	sizes = func() (ret [4][2]int) {
		wm.Draw(term.NewStringWriter(40, 12))
		for i, win := range wins {
			ret[i] = [2]int{win.Width(), win.Height()}
		}
		return ret
	}
	require.Equal(t, nestedTileSizes, sizes())
	return wm, wins, sizes
}

var nestedTileSizes = [4][2]int{{10, 6}, {10, 6}, {20, 6}, {20, 12}}

func nestedTile(i int) func(*WindowManager, [4]Window) Window {
	return func(_ *WindowManager, wins [4]Window) Window { return wins[i] }
}

func newFloatingTarget(wm *WindowManager, _ [4]Window) Window {
	return wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'F'}, 4, 2),
		FloatingConfig{},
	)
}

func TestWindowManagerMoveTileEdge(t *testing.T) {
	tests := []struct {
		name string
		win  func(*WindowManager, [4]Window) Window
		edge component.Alignment
		to   int
		ok   bool
		want [4][2]int
	}{
		{
			name: "divider shared with a sibling",
			win:  nestedTile(0), edge: component.AlignmentRight, to: 14, ok: true,
			want: [4][2]int{{15, 6}, {5, 6}, {20, 6}, {20, 12}},
		},
		{
			name: "divider shared by ancestors",
			win:  nestedTile(1), edge: component.AlignmentRight, to: 24, ok: true,
			want: [4][2]int{{12, 6}, {13, 6}, {25, 6}, {15, 12}},
		},
		{
			name: "bottom divider shared by ancestors",
			win:  nestedTile(0), edge: component.AlignmentBottom, to: 8, ok: true,
			want: [4][2]int{{10, 9}, {10, 9}, {20, 3}, {20, 12}},
		},
		{
			name: "right edge of the manager",
			win:  nestedTile(3), edge: component.AlignmentRight, to: 30,
			want: nestedTileSizes,
		},
		{
			name: "bottom edge of the manager",
			win:  nestedTile(2), edge: component.AlignmentBottom, to: 8,
			want: nestedTileSizes,
		},
		{
			name: "unsupported edge",
			win:  nestedTile(1), edge: component.AlignmentLeft, to: 5,
			want: nestedTileSizes,
		},
		{
			name: "too small for a frame",
			win:  nestedTile(0), edge: component.AlignmentRight, to: 0,
			want: nestedTileSizes,
		},
		{
			name: "floating window",
			win:  newFloatingTarget, edge: component.AlignmentRight, to: 10,
			want: nestedTileSizes,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wm, wins, sizes := nestedTileLayout(t)
			assert.Equal(t, tt.ok, wm.MoveTileEdge(tt.win(wm, wins), tt.edge, tt.to))
			assert.Equal(t, tt.want, sizes())
		})
	}
}

func TestWindowManagerResetTileEdge(t *testing.T) {
	three := doAll(splitRight('A', 'B'), splitRight('B', 'C'))
	four := doAll(splitRight('A', 'B'), splitRight('B', 'C'), splitRight('C', 'D'))
	// [[A|B] / C] | D
	nested := doAll(splitRight('A', 'D'), splitBelow('A', 'C'), splitRight('A', 'B'))
	runTileEdgeCases(t, tileAxes, []tileEdgeCase{
		{
			name: "an untouched border stays where it is",
			steps: []tileEdgeStep{
				{splitRight('A', 'B'), "8 A12 B12"},
				{resetRight('A'), "8 A12 B12"},
			},
		},
		{
			name: "two tiles share evenly again",
			steps: []tileEdgeStep{
				{splitRight('A', 'B'), "8 A12 B12"},
				{dragRight('A', 4), "8 A16 B8"},
				{resetRight('A'), "8 A12 B12"},
				{resetRight('A'), "8 A12 B12"},
				{dragRight('A', -7), "8 A5 B19"},
				{resetRight('A'), "8 A12 B12"},
			},
		},
		{
			name: "the first of two borders, then the second",
			steps: []tileEdgeStep{
				{three, "8 A8 B8 C8"},
				{dragRight('A', 4), "8 A12 B6 C6"},
				{resetRight('A'), "8 A8 B8 C8"},
				{dragRight('B', 4), "8 A6 B12 C6"},
				{resetRight('B'), "8 A8 B8 C8"},
			},
		},
		{
			name: "the second of two moved borders keeps the first",
			steps: []tileEdgeStep{
				{three, "8 A8 B8 C8"},
				{dragRight('A', 4), "8 A12 B6 C6"},
				{dragRight('B', 2), "8 A12 B8 C4"},
				{resetRight('B'), "8 A12 B6 C6"},
				{resetRight('A'), "8 A8 B8 C8"},
			},
		},
		{
			name: "the first of two moved borders also resets the tile the second sized",
			steps: []tileEdgeStep{
				{three, "8 A8 B8 C8"},
				{dragRight('A', 4), "8 A12 B6 C6"},
				{dragRight('B', 2), "8 A12 B8 C4"},
				{resetRight('A'), "8 A8 B8 C8"},
			},
		},
		{
			name: "the last of three moved borders keeps the first",
			steps: []tileEdgeStep{
				{four, "8 A6 B6 C6 D6"},
				{dragRight('A', 3), "8 A9 B5 C5 D5"},
				{dragRight('C', 3), "8 A9 B3 C8 D4"},
				{resetRight('C'), "8 A9 B5 C5 D5"},
				{resetRight('A'), "8 A6 B6 C6 D6"},
			},
		},
		{
			name: "the first of three moved borders keeps the last",
			steps: []tileEdgeStep{
				{four, "8 A6 B6 C6 D6"},
				{dragRight('A', 3), "8 A9 B5 C5 D5"},
				{dragRight('C', 3), "8 A9 B3 C8 D4"},
				{resetRight('A'), "8 A5 B5 C8 D6"},
				{resetRight('C'), "8 A6 B6 C6 D6"},
			},
		},
		{
			name: "the middle of three borders also resets the tile the last one sized",
			steps: []tileEdgeStep{
				{four, "8 A6 B6 C6 D6"},
				{dragRight('A', 3), "8 A9 B5 C5 D5"},
				{dragRight('C', 3), "8 A9 B3 C8 D4"},
				{resetRight('B'), "8 A9 B5 C5 D5"},
				{resetRight('A'), "8 A6 B6 C6 D6"},
			},
		},
		{
			name: "a border between tiles that were not moved",
			steps: []tileEdgeStep{
				{three, "8 A8 B8 C8"},
				{dragRight('A', 4), "8 A12 B6 C6"},
				{resetRight('B'), "8 A12 B6 C6"},
			},
		},
		{
			name: "the edges of the manager",
			steps: []tileEdgeStep{
				{refuseRight('A'), "8 A24"},
				{refuseBottom('A'), "8 A24"},
				{splitRight('A', 'B'), "8 A12 B12"},
				{refuseRight('B'), "8 A12 B12"},
				{refuseBottom('A'), "8 A12 B12"},
				{refuseBottom('B'), "8 A12 B12"},
			},
		},
		{
			name: "a border across the whole manager",
			steps: []tileEdgeStep{
				{
					doAll(splitBelow('A', 'C'), splitRight('A', 'B'), splitRight('C', 'D')),
					"4 A12 B12 / 4 C12 D12",
				},
				{dragBottom('A', 1), "5 A12 B12 / 3 C12 D12"},
				{dragRight('A', -4), "5 A8 B16 / 3 C12 D12"},
				{dragRight('C', 4), "5 A8 B16 / 3 C16 D8"},
				{resetBottom('B'), "4 A8 B16 / 4 C16 D8"},
				{resetRight('C'), "4 A8 B16 / 4 C12 D12"},
				{resetRight('A'), "4 A12 B12 / 4 C12 D12"},
				{refuseBottom('C'), "4 A12 B12 / 4 C12 D12"},
				{refuseRight('B'), "4 A12 B12 / 4 C12 D12"},
			},
		},
		{
			name: "nested borders reset independently",
			steps: []tileEdgeStep{
				{nested, "4 A6 B6 D12 / 4 C12 D12"},
				{dragRight('B', 4), "4 A8 B8 D8 / 4 C16 D8"},
				{dragRight('A', -2), "4 A6 B10 D8 / 4 C16 D8"},
				{dragBottom('A', 1), "5 A6 B10 D8 / 3 C16 D8"},
				{resetRight('A'), "5 A8 B8 D8 / 3 C16 D8"},
				{resetBottom('B'), "4 A8 B8 D8 / 4 C16 D8"},
				{resetRight('C'), "4 A6 B6 D12 / 4 C12 D12"},
			},
		},
		{
			name: "an outer border keeps the nested ones",
			steps: []tileEdgeStep{
				{nested, "4 A6 B6 D12 / 4 C12 D12"},
				{dragRight('B', 4), "4 A8 B8 D8 / 4 C16 D8"},
				{dragRight('A', -2), "4 A6 B10 D8 / 4 C16 D8"},
				{dragBottom('A', 1), "5 A6 B10 D8 / 3 C16 D8"},
				{resetRight('B'), "5 A6 B6 D12 / 3 C12 D12"},
				{dragRight('C', 4), "5 A6 B10 D8 / 3 C16 D8"},
			},
		},
		{
			name: "three levels of nesting",
			steps: []tileEdgeStep{
				{
					// [[[A / B] | C] / D] | E
					doAll(splitRight('A', 'E'), splitBelow('A', 'D'), splitRight('A', 'C'),
						splitBelow('A', 'B')),
					"2 A6 C6 E12 / 2 B6 C6 E12 / 4 D12 E12",
				},
				{dragRight('A', 3), "2 A9 C3 E12 / 2 B9 C3 E12 / 4 D12 E12"},
				{dragRight('C', 4), "2 A9 C7 E8 / 2 B9 C7 E8 / 4 D16 E8"},
				{dragBottom('B', 1), "2 A9 C7 E8 / 3 B9 C7 E8 / 3 D16 E8"},
				{resetRight('D'), "2 A9 C3 E12 / 3 B9 C3 E12 / 3 D12 E12"},
				{resetBottom('C'), "2 A9 C3 E12 / 2 B9 C3 E12 / 4 D12 E12"},
				{resetRight('B'), "2 A6 C6 E12 / 2 B6 C6 E12 / 4 D12 E12"},
				{resetBottom('A'), "2 A6 C6 E12 / 2 B6 C6 E12 / 4 D12 E12"},
				{refuseBottom('D'), "2 A6 C6 E12 / 2 B6 C6 E12 / 4 D12 E12"},
				{refuseRight('E'), "2 A6 C6 E12 / 2 B6 C6 E12 / 4 D12 E12"},
			},
		},
		{
			name: "a nested split of three",
			steps: []tileEdgeStep{
				{
					// [[A|B|C] / D] | E
					doAll(splitRight('A', 'E'), splitBelow('A', 'D'), splitRight('A', 'B'),
						splitRight('B', 'C')),
					"4 A4 B4 C4 E12 / 4 D12 E12",
				},
				{dragRight('C', 6), "4 A6 B6 C6 E6 / 4 D18 E6"},
				{dragRight('A', 3), "4 A9 B4 C5 E6 / 4 D18 E6"},
				{dragRight('B', 2), "4 A9 B6 C3 E6 / 4 D18 E6"},
				{dragBottom('C', 1), "5 A9 B6 C3 E6 / 3 D18 E6"},
				{resetRight('B'), "5 A9 B4 C5 E6 / 3 D18 E6"},
				{resetBottom('A'), "4 A9 B4 C5 E6 / 4 D18 E6"},
				{resetRight('A'), "4 A6 B6 C6 E6 / 4 D18 E6"},
				{resetRight('D'), "4 A4 B4 C4 E12 / 4 D12 E12"},
			},
		},
		{
			name: "a split that closing nests in one of the same direction",
			steps: []tileEdgeStep{
				{
					doAll(splitRight('A', 'B'), splitBelow('B', 'D'), splitRight('B', 'C'),
						closeWindow('D')),
					"8 A12 B6 C6",
				},
				{dragRight('A', 3), "8 A15 B4 C5"},
				{dragRight('B', 1), "8 A15 B5 C4"},
				{resetRight('A'), "8 A12 B5 C7"},
				{resetRight('B'), "8 A12 B6 C6"},
				{refuseRight('C'), "8 A12 B6 C6"},
			},
		},
		{
			name: "closing a split hands its border to the tile left",
			steps: []tileEdgeStep{
				{doAll(splitRight('A', 'C'), splitBelow('A', 'B')), "4 A12 C12 / 4 B12 C12"},
				{dragRight('B', 4), "4 A16 C8 / 4 B16 C8"},
				{closeWindow('B'), "8 A16 C8"},
				{resetRight('A'), "8 A12 C12"},
			},
		},
		{
			name: "closing a tile on a moved border",
			steps: []tileEdgeStep{
				{three, "8 A8 B8 C8"},
				{dragRight('A', 6), "8 A14 B5 C5"},
				{closeWindow('B'), "8 A14 C10"},
				{resetRight('A'), "8 A12 C12"},
			},
		},
		{
			name: "splitting next to a moved border",
			steps: []tileEdgeStep{
				{splitRight('A', 'B'), "8 A12 B12"},
				{dragRight('A', 4), "8 A16 B8"},
				{splitRight('A', 'C'), "8 A16 C4 B4"},
				{resetRight('C'), "8 A16 C4 B4"},
				{resetRight('A'), "8 A8 C8 B8"},
			},
		},
		{
			name: "splitting a moved tile below keeps its border",
			steps: []tileEdgeStep{
				{splitRight('A', 'B'), "8 A12 B12"},
				{dragRight('A', 4), "8 A16 B8"},
				{splitBelow('A', 'C'), "4 A16 B8 / 4 C16 B8"},
				{resetRight('C'), "4 A12 B12 / 4 C12 B12"},
			},
		},
		{
			name: "a tile added in front of a moved border",
			steps: []tileEdgeStep{
				{splitRight('A', 'B'), "8 A12 B12"},
				{dragRight('A', 4), "8 A16 B8"},
				{splitRoot('C', component.AlignmentLeft), "8 C4 A16 B4"},
				{resetRight('C'), "8 C8 A8 B8"},
			},
		},
		{
			name: "a manager turned around a moved border",
			steps: []tileEdgeStep{
				{splitRight('A', 'B'), "8 A12 B12"},
				{dragRight('A', 4), "8 A16 B8"},
				{splitRoot('C', component.AlignmentBottom), "4 A16 B8 / 4 C24"},
				{dragBottom('A', 1), "5 A16 B8 / 3 C24"},
				{resetRight('A'), "5 A12 B12 / 3 C24"},
				{resetBottom('B'), "4 A12 B12 / 4 C24"},
			},
		},
		{
			name: "the second border, while the manager is too small for the moved ones",
			steps: []tileEdgeStep{
				{three, "8 A8 B8 C8"},
				{dragRight('A', 4), "8 A12 B6 C6"},
				{dragRight('B', 2), "8 A12 B8 C4"},
				{resizeManager(18), "8 A10 B8"},
				{resetRight('B'), "8 A12 B3 C3"},
				{resizeManager(24), "8 A12 B6 C6"},
				{resetRight('A'), "8 A8 B8 C8"},
			},
		},
		{
			name: "the first border, while the manager is too small for the moved ones",
			steps: []tileEdgeStep{
				{three, "8 A8 B8 C8"},
				{dragRight('A', 4), "8 A12 B6 C6"},
				{dragRight('B', 2), "8 A12 B8 C4"},
				{resizeManager(18), "8 A10 B8"},
				{resetRight('A'), "8 A6 B6 C6"},
				{resizeManager(24), "8 A8 B8 C8"},
			},
		},
		{
			name: "a window reset resets only its window",
			steps: []tileEdgeStep{
				{three, "8 A8 B8 C8"},
				{dragRight('A', 4), "8 A12 B6 C6"},
				{dragRight('B', 2), "8 A12 B8 C4"},
				{resetWindow('B'), "8 A12 B6 C6"},
				{resetWindow('A'), "8 A8 B8 C8"},
			},
		},
		{
			name: "edges other than right and bottom, floating and closed windows",
			steps: []tileEdgeStep{
				{splitRight('A', 'B'), "8 A12 B12"},
				{func(f *tileEdgeFixture) {
					for _, edge := range []component.Alignment{
						component.AlignmentLeft, component.AlignmentTop,
					} {
						assert.False(f.t, f.wm.ResetTileEdge(f.tiles['B'], edge), "edge %v", edge)
					}
				}, "8 A12 B12"},
				{func(f *tileEdgeFixture) {
					float := f.wm.FloatingWindow(
						component.StaticFloating(&component.TestComponent{Ch: 'F'}, 4, 2),
						FloatingConfig{},
					)
					assert.False(f.t, f.wm.ResetTileEdge(float, component.AlignmentRight))
					assert.False(f.t, f.wm.ResetTileEdge(float, component.AlignmentBottom))
				}, "8 A12 B12"},
				{func(f *tileEdgeFixture) {
					splitRight('A', 'C')(f)
					closed := f.tiles['C']
					closeWindow('C')(f)
					assert.False(f.t, f.wm.ResetTileEdge(closed, f.side(component.AlignmentRight)))
				}, "8 A12 B12"},
			},
		},
	})
}

func TestWindowManagerResetTileEdgeDefaultWidth(t *testing.T) {
	three := doAll(splitRight('A', 'B'), splitRight('B', 'C'))
	four := doAll(splitRight('A', 'B'), splitRight('B', 'C'), splitRight('C', 'D'))
	// Only widths have defaults, so these run over columns alone.
	runTileEdgeCases(t, tileAxes[:1], []tileEdgeCase{
		{
			name: "a default width",
			steps: []tileEdgeStep{
				{doAll(splitRight('A', 'B'), setDefaultWidth('A', 6)), "8 A6 B18"},
				{dragRight('A', 10), "8 A16 B8"},
				{resetRight('A'), "8 A6 B18"},
				{dragRight('A', 10), "8 A16 B8"},
				{resetWindow('A'), "8 A6 B18"},
				{dragRight('A', 10), "8 A16 B8"},
				{setDefaultWidth('A', 6), "8 A6 B18"},
			},
		},
		{
			name: "a default on the far side of the border",
			steps: []tileEdgeStep{
				{doAll(splitRight('A', 'B'), setDefaultWidth('B', 6)), "8 A18 B6"},
				{dragRight('A', -6), "8 A12 B12"},
				{resetRight('A'), "8 A18 B6"},
			},
		},
		{
			name: "defaults on both sides of a border keep the first",
			steps: []tileEdgeStep{
				{
					doAll(splitRight('A', 'B'), setDefaultWidth('A', 6), setDefaultWidth('B', 5)),
					"8 A19 B5",
				},
				{resetRight('A'), "8 A6 B18"},
			},
		},
		{
			name: "a default in the middle, from either of its borders",
			steps: []tileEdgeStep{
				{doAll(three, setDefaultWidth('B', 6)), "8 A9 B6 C9"},
				{dragRight('A', 3), "8 A12 B6 C6"},
				{resetRight('A'), "8 A9 B6 C9"},
				{dragRight('B', 3), "8 A7 B9 C8"},
				{resetRight('B'), "8 A9 B6 C9"},
			},
		},
		{
			name: "a default elsewhere is kept",
			steps: []tileEdgeStep{
				{doAll(three, setDefaultWidth('C', 6)), "8 A9 B9 C6"},
				{dragRight('A', 3), "8 A12 B6 C6"},
				{resetRight('A'), "8 A9 B9 C6"},
			},
		},
		{
			name: "a default that only fits by unfixing a tile elsewhere is not taken",
			steps: []tileEdgeStep{
				{doAll(four, setDefaultWidth('A', 12)), "8 A12 B4 C4 D4"},
				{dragRight('C', 3), "8 A5 B6 C7 D6"},
				{resetRight('A'), "8 A5 B6 C7 D6"},
				{resetRight('C'), "8 A6 B6 C6 D6"},
				{resetRight('A'), "8 A12 B4 C4 D4"},
			},
		},
		{
			name: "a default that does not fit now is kept for later",
			steps: []tileEdgeStep{
				{doAll(three, setDefaultWidth('A', 14)), "8 A14 B5 C5"},
				{resizeManager(18), "8 A14 B2 C2"},
				{resetRight('A'), "8 A6 B6 C6"},
				{resizeManager(24), "8 A8 B8 C8"},
				{resetRight('A'), "8 A14 B5 C5"},
			},
		},
		{
			name: "a default on a column split below",
			steps: []tileEdgeStep{
				{
					doAll(splitRight('A', 'B'), setDefaultWidth('A', 6), splitBelow('A', 'C')),
					"4 A6 B18 / 4 C6 B18",
				},
				{dragRight('C', 6), "4 A12 B12 / 4 C12 B12"},
				{resetRight('A'), "4 A6 B18 / 4 C6 B18"},
				{setDefaultWidth('C', 8), "4 A8 B16 / 4 C8 B16"},
				{dragRight('A', 8), "4 A16 B8 / 4 C16 B8"},
				{closeWindow('C'), "8 A16 B8"},
				{resetRight('A'), "8 A8 B16"},
			},
		},
		{
			name: "a column with a default keeps its width when a border beside it is reset",
			steps: []tileEdgeStep{
				{
					doAll(splitRight('A', 'B'), setDefaultWidth('A', 6), splitBelow('B', 'C')),
					"4 A6 B18 / 4 A6 C18",
				},
				{dragBottom('B', 1), "5 A6 B18 / 3 A6 C18"},
				{dragRight('A', 6), "5 A12 B12 / 3 A12 C12"},
				{resetBottom('B'), "4 A12 B12 / 4 A12 C12"},
				{resetRight('A'), "4 A6 B18 / 4 A6 C18"},
			},
		},
		{
			name: "a tile left alone keeps its default for the next split beside it",
			steps: []tileEdgeStep{
				{doAll(splitRight('A', 'B'), setDefaultWidth('A', 5), closeWindow('B')), "8 A24"},
				{splitRight('A', 'C'), "8 A12 C12"},
				{resetRight('A'), "8 A5 C19"},
			},
		},
		{
			name: "a default width does not become a height when the manager turns",
			steps: []tileEdgeStep{
				{doAll(splitRight('A', 'B'), setDefaultWidth('A', 5), closeWindow('B')), "8 A24"},
				{splitRoot('C', component.AlignmentBottom), "4 A24 / 4 C24"},
				{resetBottom('A'), "4 A24 / 4 C24"},
				{resetWindow('A'), "4 A24 / 4 C24"},
			},
		},
		{
			name: "a default stays with its tile when the manager turns around its split",
			steps: []tileEdgeStep{
				{doAll(splitRight('A', 'B'), setDefaultWidth('A', 6)), "8 A6 B18"},
				{splitRoot('C', component.AlignmentBottom), "4 A6 B18 / 4 C24"},
				{dragRight('A', 10), "4 A16 B8 / 4 C24"},
				{resetRight('A'), "4 A6 B18 / 4 C24"},
			},
		},
		{
			name: "a default next to a new split",
			steps: []tileEdgeStep{
				{doAll(splitRight('A', 'B'), setDefaultWidth('A', 6)), "8 A6 B18"},
				{splitRight('A', 'C'), "8 A6 C9 B9"},
				{dragRight('A', 6), "8 A12 C6 B6"},
				{resetRight('A'), "8 A6 C9 B9"},
			},
		},
		{
			name: "a tile spanning the manager has no default",
			steps: []tileEdgeStep{
				{refuseDefaultWidth('A', 6), "8 A24"},
				{splitRight('A', 'B'), "8 A12 B12"},
				{dragRight('A', 4), "8 A16 B8"},
				{resetRight('A'), "8 A12 B12"},
			},
		},
	})
}

func TestWindowManagerSetDefaultWidth(t *testing.T) {
	tests := []struct {
		name  string
		win   func(*WindowManager, [4]Window) Window
		width int
		ok    bool
		want  [4][2]int
	}{
		{
			name: "fixes the width",
			win:  nestedTile(3), width: 6, ok: true,
			want: [4][2]int{{17, 6}, {17, 6}, {34, 6}, {6, 12}},
		},
		{
			name: "zero clears the default",
			win:  nestedTile(3), width: 0, ok: true,
			want: nestedTileSizes,
		},
		{
			name: "too small for a frame",
			win:  nestedTile(3), width: 2,
			want: nestedTileSizes,
		},
		{
			name: "floating window",
			win:  newFloatingTarget, width: 6,
			want: nestedTileSizes,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wm, wins, sizes := nestedTileLayout(t)
			win := tt.win(wm, wins)
			assert.Equal(t, tt.ok, wm.SetDefaultWidth(win, tt.width))
			assert.Equal(t, tt.want, sizes())

			require.True(t, wm.SetWidth(win, 10))
			sizes()
			require.True(t, wm.SetWidth(win, 0))
			assert.Equal(t, tt.want, sizes(), "a reset restores the default")
		})
	}
}

// tileEdgeCross is the length of the manager across the main axis of a tileEdgeCase.
const tileEdgeCross = 8

// tileEdgeCase lays tiles out over a manager 24 cells along the main axis, checking the
// screen that each step leaves. Cases are written for columns: over rows they run
// transposed, so that right becomes bottom.
type tileEdgeCase struct {
	name  string
	steps []tileEdgeStep
}

type tileEdgeStep struct {
	do tileEdgeAction
	// want lists the rows of the screen, separated by '/', each as how many times it
	// repeats and its tiles from left to right with their widths: "4 A6 B18 / 4 C24".
	want string
}

type tileEdgeAction func(*tileEdgeFixture)

type tileEdgeFixture struct {
	t     *testing.T
	axis  tileAxis
	main  int
	wm    *WindowManager
	tiles map[rune]Window
}

func runTileEdgeCases(t *testing.T, axes []tileAxis, tests []tileEdgeCase) {
	for _, axis := range axes {
		t.Run(axis.name, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					wm, a := NewWindowManager(&component.TestComponent{Ch: 'A'},
						testWindowManagerConfig())
					// A root with a single tile has no orientation of its own, and the
					// axis's makes the rows run the exact transpose of the columns one.
					wm.tree.root.childSplit = axis.dir
					f := &tileEdgeFixture{t: t, axis: axis, wm: wm, tiles: map[rune]Window{'A': a}}
					resizeManager(24)(f)
					for i, s := range tt.steps {
						s.do(f)
						require.Equal(t, f.expand(s.want), f.screen(), "step %d", i)
					}
				})
			}
		})
	}
}

func (f *tileEdgeFixture) relayout() {
	f.wm.Draw(term.NewStringWriter(f.axis.size(f.main, tileEdgeCross)))
}

// screen returns each cell as the name of the tile at it.
func (f *tileEdgeFixture) screen() string {
	f.relayout()
	names := make(map[uint64]rune, len(f.tiles))
	for ch, win := range f.tiles {
		names[win.ID()] = ch
	}
	w, h := f.axis.size(f.main, tileEdgeCross)
	var sb strings.Builder
	for y := range h {
		sb.WriteByte('\n')
		for x := range w {
			ch := '?'
			if win, ok := f.wm.TileAt(term.Coordinates{X: x, Y: y}); ok {
				if name, ok := names[win.ID()]; ok {
					ch = name
				}
			}
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// expand returns the screen that want, as tileEdgeStep.want, lists.
func (f *tileEdgeFixture) expand(want string) string {
	var sb strings.Builder
	for _, row := range strings.Split(want, "/") {
		fields := strings.Fields(row)
		require.NotEmpty(f.t, fields, "screen %q", want)
		count, err := strconv.Atoi(fields[0])
		require.NoError(f.t, err, "screen %q", want)
		var line strings.Builder
		for _, tile := range fields[1:] {
			width, err := strconv.Atoi(tile[1:])
			require.NoError(f.t, err, "screen %q", want)
			line.WriteString(strings.Repeat(tile[:1], width))
		}
		for range count {
			sb.WriteString("\n" + line.String())
		}
	}
	return f.axis.render(sb.String())
}

// side returns the side over f's axis that side is over columns.
func (f *tileEdgeFixture) side(side component.Alignment) component.Alignment {
	if f.axis.dir == vertical {
		return side
	}
	switch side {
	case component.AlignmentLeft:
		return component.AlignmentTop
	case component.AlignmentTop:
		return component.AlignmentLeft
	case component.AlignmentRight:
		return component.AlignmentBottom
	case component.AlignmentBottom:
		return component.AlignmentRight
	}
	return side
}

func doAll(actions ...tileEdgeAction) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		for _, do := range actions {
			do(f)
		}
	}
}

// splitRight adds ch to the right of over.
func splitRight(over, ch rune) tileEdgeAction {
	return splitTile(over, ch, component.AlignmentRight)
}

// splitBelow adds ch below over.
func splitBelow(over, ch rune) tileEdgeAction {
	return splitTile(over, ch, component.AlignmentBottom)
}

func splitTile(over, ch rune, side component.Alignment) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		split := f.wm.SplitVertical
		if f.side(side) == component.AlignmentBottom {
			split = f.wm.SplitHorizontal
		}
		win, ok := split(f.tiles[over], &component.TestComponent{Ch: ch})
		require.True(f.t, ok, "split %c", over)
		f.tiles[ch] = win
	}
}

// splitRoot adds ch along the side of the whole manager.
func splitRoot(ch rune, side component.Alignment) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		win, ok := f.wm.SplitRoot(f.side(side), &component.TestComponent{Ch: ch})
		require.True(f.t, ok, "split root %c", ch)
		f.tiles[ch] = win
	}
}

func closeWindow(ch rune) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		require.NoError(f.t, f.tiles[ch].Close())
		delete(f.tiles, ch)
	}
}

// resizeManager resizes the manager to main cells along the main axis.
func resizeManager(main int) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		f.main = main
		f.wm.Resize(f.axis.size(main, tileEdgeCross))
	}
}

// dragRight drags the right edge of ch by cells, leftwards when negative.
func dragRight(ch rune, by int) tileEdgeAction {
	return dragEdge(ch, component.AlignmentRight, by)
}

// dragBottom drags the bottom edge of ch by cells, upwards when negative.
func dragBottom(ch rune, by int) tileEdgeAction {
	return dragEdge(ch, component.AlignmentBottom, by)
}

func dragEdge(ch rune, side component.Alignment, by int) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		f.relayout()
		win, edge := f.tiles[ch], f.side(side)
		pos := win.Position()
		to := pos.X + win.Width() - 1 + by
		if edge == component.AlignmentBottom {
			to = pos.Y + win.Height() - 1 + by
		}
		require.True(f.t, f.wm.MoveTileEdge(win, edge, to), "drag %c by %d", ch, by)
	}
}

func resetRight(ch rune) tileEdgeAction {
	return resetEdge(ch, component.AlignmentRight, true)
}

func resetBottom(ch rune) tileEdgeAction {
	return resetEdge(ch, component.AlignmentBottom, true)
}

// refuseRight checks that the right edge of ch cannot be reset.
func refuseRight(ch rune) tileEdgeAction {
	return resetEdge(ch, component.AlignmentRight, false)
}

// refuseBottom checks that the bottom edge of ch cannot be reset.
func refuseBottom(ch rune) tileEdgeAction {
	return resetEdge(ch, component.AlignmentBottom, false)
}

func resetEdge(ch rune, side component.Alignment, ok bool) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		assert.Equal(f.t, ok, f.wm.ResetTileEdge(f.tiles[ch], f.side(side)), "reset %c", ch)
	}
}

// resetWindow resets ch as windowresize reset does.
func resetWindow(ch rune) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		win := f.tiles[ch]
		okh := f.wm.SetHeight(win, 0)
		okw := f.wm.SetWidth(win, 0)
		assert.True(f.t, okh || okw, "reset %c", ch)
	}
}

func setDefaultWidth(ch rune, width int) tileEdgeAction {
	return defaultWidth(ch, width, true)
}

// refuseDefaultWidth checks that ch cannot take width as its default.
func refuseDefaultWidth(ch rune, width int) tileEdgeAction {
	return defaultWidth(ch, width, false)
}

func defaultWidth(ch rune, width int, ok bool) tileEdgeAction {
	return func(f *tileEdgeFixture) {
		assert.Equal(f.t, ok, f.wm.SetDefaultWidth(f.tiles[ch], width), "default %c", ch)
	}
}
