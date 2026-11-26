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
	"bytes"
	"cmp"
	"context"
	"fmt"
	"image"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
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
	"unstable.build/rune/internal/cell"
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

func TestWindowManagerRestyle(t *testing.T) {
	plusCharSet := component.FrameCharSet{
		TopLeft: '+', TopRight: '+', BottomLeft: '+', BottomRight: '+',
		HorizontalTop: '-', HorizontalBottom: '-', VerticalLeft: '|', VerticalRight: '|',
	}
	blue := term.Attributes{Fg: term.ColorBlue}
	red := term.Attributes{Fg: term.ColorRed}
	yellow := term.Attributes{Fg: term.ColorYellow}
	scrollBarAttr := term.Attributes{Fg: term.ColorGreen}
	scrollBarCharSet := component.ScrollBarCharSet{Middle: '#'}
	openFloating := func(wm *WindowManager) {
		wm.FloatingWindow(
			component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2),
			FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}},
		)
	}
	const restyled = `
+------------------+
|=x==CCCCCCCCCCCCCC|
||BB|CCCCCCCCCCCCCC|
||BB|CCCCCCCCCCCCCC|
|+--+CCCCCCCCCCCCCC|
|CCCCCCCCCCCCCCCCCC|
|CCCCCCCCCCCCCCCCCC|
+------------------+`

	tests := []struct {
		name      string
		noFrame   bool
		before    func(wm *WindowManager, root Window)
		frameAttr term.Attributes
		after     func(wm *WindowManager)
		expected  string
		// wantRootAttr is the root tile's frame attribute after Restyle.
		wantRootAttr term.Attributes
	}{
		{
			name:         "tiles and floating windows with a bar",
			before:       func(wm *WindowManager, _ Window) { openFloating(wm) },
			frameAttr:    blue,
			expected:     restyled,
			wantRootAttr: blue,
		},
		{
			name:      "no frame changes no window",
			noFrame:   true,
			before:    func(wm *WindowManager, _ Window) { openFloating(wm) },
			frameAttr: blue,
			expected: `
CCCCCCCCCCCCCCCCCCCC
CBBCCCCCCCCCCCCCCCCC
CBBCCCCCCCCCCCCCCCCC
CCCCCCCCCCCCCCCCCCCC
CCCCCCCCCCCCCCCCCCCC
CCCCCCCCCCCCCCCCCCCC
CCCCCCCCCCCCCCCCCCCC
CCCCCCCCCCCCCCCCCCCC`,
		},
		{
			name: "an unchanged frame attribute keeps a window's own",
			before: func(wm *WindowManager, root Window) {
				openFloating(wm)
				root.SetFrameAttr(red)
			},
			expected:     restyled,
			wantRootAttr: red,
		},
		{
			name: "a changed frame attribute replaces a window's own",
			before: func(wm *WindowManager, root Window) {
				openFloating(wm)
				root.SetFrameAttr(red)
			},
			frameAttr:    yellow,
			expected:     restyled,
			wantRootAttr: yellow,
		},
		{
			name:         "a window opened after Restyle uses the new style",
			after:        openFloating,
			frameAttr:    blue,
			expected:     restyled,
			wantRootAttr: blue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultWindowManagerConfig()
			cfg.NoMaxSize = true
			cfg.Frame = !tt.noFrame
			wm, root := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
			wm.Resize(20, 8)
			if tt.before != nil {
				tt.before(wm, root)
			}

			style := cfg
			style.FrameAttr = tt.frameAttr
			style.FrameCharSet = plusCharSet
			style.ScrollBarAttr = scrollBarAttr
			style.ScrollBarCharSet = scrollBarCharSet
			style.ScrollBarOutset = 2
			style.WindowBarCharSet = WindowBarCharSet{Left: '=', Horizontal: '=', Right: '='}
			style.CloseIcon = 'x'
			style.CloseIconAttr = yellow
			// What changes the layout is not style.
			style.Frame = !cfg.Frame
			style.NoMaxSize = false
			wm.Restyle(style)
			if tt.after != nil {
				tt.after(wm)
			}

			want := cfg
			want.FrameAttr = tt.frameAttr
			want.FrameCharSet = plusCharSet
			want.ScrollBarAttr = scrollBarAttr
			want.ScrollBarCharSet = scrollBarCharSet
			want.ScrollBarOutset = 2
			want.WindowBarCharSet = style.WindowBarCharSet
			want.CloseIcon = 'x'
			want.CloseIconAttr = yellow
			assert.Equal(t, want, wm.config)

			w := term.NewStringWriter(20, 8)
			wm.Draw(w)
			require.NoError(t, w.Flush())
			assert.Equal(t, tt.expected, "\n"+w.String())
			if tt.noFrame {
				return
			}
			attr, ok := root.FrameAttr()
			require.True(t, ok)
			assert.Equal(t, tt.wantRootAttr, attr)
			wm.Iterate(func(win Window) {
				f := win.node.Content().(*component.Frame)
				assert.Equal(t, scrollBarAttr, f.ScrollBarAttributes)
				assert.Equal(t, scrollBarCharSet, f.ScrollBarCharSet)
			})
		})
	}
}

func TestWindowManagerRestyleWindowBar(t *testing.T) {
	const withBar = `
┌──────────────────┐
│█●██CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
│└──┘CCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`
	const withoutBar = `
┌──────────────────┐
│┌──┐CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
││BB│CCCCCCCCCCCCCC│
│└──┘CCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
│CCCCCCCCCCCCCCCCCC│
└──────────────────┘`
	openFloating := func(wm *WindowManager) Window {
		return wm.FloatingWindow(
			component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 2),
			FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}},
		)
	}
	tests := []struct {
		name       string
		bar        bool
		restyled   bool
		openBefore bool
		expected   string
	}{
		{name: "turned off in an open window", bar: true, openBefore: true, expected: withoutBar},
		{name: "turned on in an open window", restyled: true, openBefore: true, expected: withBar},
		{name: "turned off in a window opened afterwards", bar: true, expected: withoutBar},
		{name: "turned on in a window opened afterwards", restyled: true, expected: withBar},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultWindowManagerConfig()
			cfg.WindowBar = tt.bar
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
			wm.Resize(20, 8)
			var win Window
			if tt.openBefore {
				win = openFloating(wm)
			}
			var pos term.Coordinates
			var width, height int
			if tt.openBefore {
				pos, width, height = win.Position(), win.Width(), win.Height()
			}

			style := cfg
			style.WindowBar = tt.restyled
			wm.Restyle(style)
			if !tt.openBefore {
				win = openFloating(wm)
			}

			assert.Equal(t, tt.restyled, win.HasWindowBar())
			if tt.openBefore {
				assert.Equal(t, pos, win.Position(), "the bar does not move the window")
				assert.Equal(t, [2]int{width, height}, [2]int{win.Width(), win.Height()},
					"the bar does not resize the window")
			}
			w := term.NewStringWriter(20, 8)
			wm.Draw(w)
			require.NoError(t, w.Flush())
			assert.Equal(t, tt.expected, "\n"+w.String())
		})
	}
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
		// over is the floating window's area, whose images go over the
		// cells under the window.
		over image.Rectangle
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
			over:  image.Rect(1, 1, 5, 4),
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
			s.over = tt.over
			wm.Draw(s)
			assert.Equal(t, tt.expected, s.render(t, pics, attr, false))
		})
	}
}

func TestWindowBarGraphics(t *testing.T) {
	pics := newFramePictures(t)
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	tileAttr := term.Attributes{Fg: term.ColorWhite, Bg: term.ColorTeal}
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
		// title's first letter, and bar those of the bar cell right of
		// the icon.
		icon, title, bar term.Attributes
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
			underneath: `
┌──────────┐
│C●CC ab CC│
││BBBBBBBB││
│└────────┘│
└──────────┘`,
			below: []int{2, 5, 6, 7, 8},
			icon:  term.Attributes{Fg: term.ColorRed, Bg: tileAttr.Bg},
			title: term.Attributes{Fg: attr.Fg, Bg: tileAttr.Bg},
			bar:   tileAttr,
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
			bar:        term.Attributes{Fg: attr.Fg, Bg: tileAttr.Bg},
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
			underneath: `
┌──────────┐
│C●██ ab █C│
││BBBBBBBB││
│└────────┘│
└──────────┘`,
			icon:  solidIcon,
			title: solidTitle,
			bar:   attr,
		},
		{
			name:     "a writer that cannot draw images gets the runes",
			graphics: every,
			noImages: true,
			expected: runes,
			icon:     solidIcon,
			title:    solidTitle,
			bar:      attr,
		},
		{
			name:      "images that are not loaded yet leave the runes",
			graphics:  every,
			notLoaded: true,
			expected:  runes,
			icon:      solidIcon,
			title:     solidTitle,
			bar:       attr,
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
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C', Attributes: tileAttr}, cfg)
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
			s.over = image.Rect(1, 1, 11, 5)
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
				assert.Equal(t, tt.bar, s.Cells()[1*12+3].Attributes(), "bar")
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
			s.over = image.Rect(3, 2, 13, 7)
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
					assert.Equal(t, term.NewCell('C', 1, term.Attributes{}), s.Cells()[2*14+x],
						"the end's own cell at %d shows the window under it", x)
				}
			}
		})
	}
}

func TestWindowManagerFloatingGraphicsShowWhatIsUnder(t *testing.T) {
	pics := newFramePictures(t)
	every := func(name string) component.FrameCharSetGraphics {
		return pics.resolve(component.FrameCharSetGraphics{
			TopLeft: name, TopRight: name, BottomLeft: name, BottomRight: name,
			HorizontalTop: name, HorizontalBottom: name,
			VerticalLeft: name, VerticalRight: name,
		})
	}
	overlay := every("t")
	overlay.Overlay = true
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	tileAttr := term.Attributes{Fg: term.ColorWhite, Bg: term.ColorTeal}
	runes := `
┌──────────┐
│C┌────┐CCC│
│C│BBBB│CCC│
│C│BBBB│CCC│
│C└────┘CCC│
└──────────┘`
	transparent := `
┌──────────┐
│CCCCCCCCCC│
│CCBBBBCCCC│
│CCBBBBCCCC│
│CCCCCCCCCC│
└──────────┘`

	tests := []struct {
		name      string
		graphics  component.FrameCharSetGraphics
		windowBar bool
		bar       WindowBarGraphics
		content   component.Floating
		loads     int
		noImages  bool
		notLoaded bool
		expected  string
	}{
		{
			name:     "transparent images show the window under them",
			graphics: every("t"),
			loads:    1,
			expected: transparent,
		},
		{
			name:     "opaque images go over the window under them",
			graphics: every("L"),
			loads:    1,
			expected: `
┌──────────┐
│CLLLLLLCCC│
│CLBBBBLCCC│
│CLBBBBLCCC│
│CLLLLLLCCC│
└──────────┘`,
		},
		{
			name: "slots without images keep their runes",
			graphics: pics.resolve(component.FrameCharSetGraphics{
				TopLeft: "t", TopRight: "t", BottomLeft: "t", BottomRight: "t",
			}),
			loads: 1,
			expected: `
┌──────────┐
│CC────CCCC│
│C│BBBB│CCC│
│C│BBBB│CCC│
│CC────CCCC│
└──────────┘`,
		},
		{
			name:     "overlay images go over their runes",
			graphics: overlay,
			loads:    1,
			expected: runes,
		},
		{
			name:     "a writer that cannot draw images gets the runes",
			graphics: every("t"),
			loads:    1,
			noImages: true,
			expected: runes,
		},
		{
			name:      "images that are not loaded yet leave the runes",
			graphics:  every("t"),
			loads:     1,
			notLoaded: true,
			expected:  runes,
		},
		{
			name:     "the scroll bar keeps its runes",
			graphics: every("t"),
			content:  scrollingFloating{scrollingContent{&component.TestComponent{Ch: 'B'}}},
			loads:    1,
			expected: `
┌──────────┐
│CCCCCCCCCC│
│CCBBBB┃CCC│
│CCBBBBCCCC│
│CCCCCCCCCC│
└──────────┘`,
		},
		{
			name:      "transparent bar images show the window under them",
			graphics:  every("t"),
			windowBar: true,
			bar:       WindowBarGraphics{Left: "t", Horizontal: "t", Right: "t"},
			loads:     1,
			expected: `
┌──────────┐
│CC●CCCCCCC│
│CCBBBBCCCC│
│CCBBBBCCCC│
│CCCCCCCCCC│
└──────────┘`,
		},
		{
			name:      "a bar without images keeps its runes",
			graphics:  every("t"),
			windowBar: true,
			loads:     1,
			expected: `
┌──────────┐
│C█●████CCC│
│CCBBBBCCCC│
│CCBBBBCCCC│
│CCCCCCCCCC│
└──────────┘`,
		},
		{
			name:      "a writer that cannot draw images gets the bar's runes",
			graphics:  every("t"),
			windowBar: true,
			bar:       WindowBarGraphics{Left: "t", Horizontal: "t", Right: "t"},
			loads:     1,
			noImages:  true,
			expected: `
┌──────────┐
│C█●████CCC│
│C│BBBB│CCC│
│C│BBBB│CCC│
│C└────┘CCC│
└──────────┘`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testWindowManagerConfig()
			cfg.WindowBar = tt.windowBar
			cfg.WindowBarCharSet.Graphics = pics.resolveWindowBar(tt.bar)
			cfg.FrameAttr = attr
			cfg.ScrollBarAttr = attr
			cfg.ScrollBarCharSet = component.ScrollBarCharSet{Top: '┃', Middle: '┃', Bottom: '┃'}
			floating := cfg.FrameCharSet
			floating.Graphics = tt.graphics
			cfg.FloatingFrameCharSet = &floating
			cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
			irq := make(interruptCounter, 16)
			cfg.Interrupter = irq
			wm, _ := NewWindowManager(&component.TestComponent{Ch: 'C', Attributes: tileAttr}, cfg)
			t.Cleanup(func() { assert.NoError(t, wm.Close()) })
			wm.Resize(12, 6)
			content := tt.content
			if content == nil {
				content = component.StaticFloating(&component.TestComponent{Ch: 'B'}, 4, 2)
			}
			wm.FloatingWindow(content, FloatingConfig{Offset: term.Coordinates{X: 2, Y: 1}})
			if !tt.notLoaded {
				wm.Draw(term.NewStringWriter(12, 6))
				irq.wait(t, tt.loads)
			}

			s := newImageScreen(12, 6)
			s.noImages = tt.noImages
			wm.Draw(s)
			assert.Equal(t, tt.expected, s.composite(t, pics))
			for _, img := range s.images {
				cell := s.Cells()[img.Pos.Y*12+img.Pos.X]
				if img.Layer == term.ImageLayerBelowText {
					assert.Equal(t, tileAttr.Bg, cell.Bg, "the text over the image at %v", img.Pos)
					continue
				}
				if tt.graphics.Overlay {
					assert.NotEqual(t, 'C', cell.Ch, "the rune under the overlay image at %v", img.Pos)
					assert.Equal(t, term.Attributes{Fg: attr.Fg, Bg: tileAttr.Bg}, cell.Attributes(),
						"the rune under the overlay image at %v", img.Pos)
					continue
				}
				assert.Equal(t, 'C', cell.Ch, "the cell under the image at %v", img.Pos)
				assert.Equal(t, tileAttr, cell.Attributes(), "the cell under the image at %v", img.Pos)
			}

			if tt.notLoaded {
				irq.wait(t, tt.loads)
			}
		})
	}
}

// scrollingFloating is floating content past whose bottom there is more,
// so that its frame draws a scroll bar.
type scrollingFloating struct {
	scrollingContent
}

func (scrollingFloating) Dimensions() (int, int) { return 4, 2 }

// stripedContent draws S on a teal background in its first row and C
// with the default colours in the others. shifted moves the first row
// down half a cell, as a status bar is, and gap is the colour it fills
// the gap that leaves with, if any.
type stripedContent struct {
	width, height int
	shifted       bool
	gap           term.Color
}

func (c *stripedContent) Resize(width, height int) { c.width, c.height = width, height }

func (c *stripedContent) Draw(w term.Writer) {
	for y := range c.height {
		for x := range c.width {
			cell := term.NewCell('C', 1, term.Attributes{})
			if y == 0 {
				attr := term.Attributes{Bg: term.ColorTeal}
				if c.shifted {
					attr.Attrs = term.AttrVerticalRenderOffset
				}
				cell = term.NewCell('S', 1, attr)
			}
			w.SetCell(term.Coordinates{X: x, Y: y}, cell)
		}
	}
	if c.gap != term.ColorDefault {
		w.DrawImage(term.Image{
			Fill: true, FillColor: c.gap, Width: c.width, Height: 1,
			Layer: term.ImageLayerBelowBackground,
		})
	}
}

// colorMap returns the colour of each cell of s, as fg or bg picks it:
// n for navy, t for teal, w for white and . for the default.
func colorMap(s *imageScreen, pick func(term.Cell) term.Color) string {
	var b strings.Builder
	for i, c := range s.Cells() {
		if i%s.width == 0 {
			b.WriteByte('\n')
		}
		switch pick(c) {
		case term.ColorDefault:
			b.WriteByte('.')
		case term.ColorNavy:
			b.WriteByte('n')
		case term.ColorTeal:
			b.WriteByte('t')
		case term.ColorWhite:
			b.WriteByte('w')
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

func TestWindowManagerBackground(t *testing.T) {
	pics := newFramePictures(t)
	navy := term.Attributes{Bg: term.ColorNavy}
	panel := func(names map[string]string) BackgroundGraphics {
		var g BackgroundGraphics
		for slot, dst := range map[string]*string{
			"tl": &g.TopLeft, "tr": &g.TopRight, "bl": &g.BottomLeft, "br": &g.BottomRight,
			"t": &g.HorizontalTop, "b": &g.HorizontalBottom, "l": &g.VerticalLeft,
			"r": &g.VerticalRight, "c": &g.Center,
		} {
			*dst = pics.uris[names[slot]]
		}
		return g
	}
	every := panel(map[string]string{
		"tl": "i", "tr": "i", "bl": "i", "br": "i", "t": "i", "b": "i", "l": "i", "r": "i", "c": "i",
	})
	edges := panel(map[string]string{
		"tl": "i", "tr": "i", "bl": "i", "br": "i", "t": "i", "b": "i", "l": "i", "r": "i",
	})
	center := panel(map[string]string{"c": "i"})
	colored := `
nnnnnnnn
nttttttn
nnnnnnnn
nnnnnnnn
nnnnnnnn`
	bg := func(c term.Cell) term.Color { return c.Bg }
	fg := func(c term.Cell) term.Color { return c.Fg }
	type placement struct {
		area  image.Rectangle
		layer term.ImageLayer
	}
	type fill struct {
		area  image.Rectangle
		color term.Color
	}

	tests := []struct {
		name       string
		background WindowBackground
		noFrame    bool
		noImages   bool
		notLoaded  bool
		shifted    bool
		gap        term.Color
		loads      int
		pick       func(term.Cell) term.Color
		expected   string
		images     []placement
		// fills are the cells painted in a colour under the rows of
		// shifted cells.
		fills []fill
	}{
		{
			name:       "the colour fills the cells left with the default background",
			background: WindowBackground{Attr: navy},
			pick:       bg,
			expected:   colored,
		},
		{
			name:       "the colour fills a window without a frame",
			background: WindowBackground{Attr: navy},
			noFrame:    true,
			pick:       bg,
			expected: `
tttttttt
nnnnnnnn
nnnnnnnn
nnnnnnnn
nnnnnnnn`,
		},
		{
			name:       "the foreground replaces the default one on default backgrounds",
			background: WindowBackground{Attr: term.Attributes{Fg: term.ColorWhite}},
			pick:       fg,
			expected: `
wwwwwwww
w......w
wwwwwwww
wwwwwwww
wwwwwwww`,
		},
		{
			name:       "the panel shows under the default backgrounds and the colour around it",
			background: WindowBackground{Attr: navy, Graphics: center},
			loads:      1,
			pick:       bg,
			expected: `
nnnnnnnn
nttttttn
n......n
n......n
nnnnnnnn`,
			images: []placement{{image.Rect(1, 1, 7, 4), term.ImageLayerBelowBackground}},
		},
		{
			name:       "a panel over every slice leaves the default backgrounds",
			background: WindowBackground{Attr: navy, Graphics: every},
			loads:      1,
			pick:       bg,
			expected: `
........
.tttttt.
........
........
........`,
			images: []placement{
				{image.Rect(0, 0, 1, 1), term.ImageLayerBelowBackground},
				{image.Rect(7, 0, 8, 1), term.ImageLayerBelowBackground},
				{image.Rect(0, 4, 1, 5), term.ImageLayerBelowBackground},
				{image.Rect(7, 4, 8, 5), term.ImageLayerBelowBackground},
				{image.Rect(1, 0, 7, 1), term.ImageLayerBelowBackground},
				{image.Rect(1, 4, 7, 5), term.ImageLayerBelowBackground},
				{image.Rect(0, 1, 1, 4), term.ImageLayerBelowBackground},
				{image.Rect(7, 1, 8, 4), term.ImageLayerBelowBackground},
				{image.Rect(1, 1, 7, 4), term.ImageLayerBelowBackground},
			},
		},
		{
			name:       "a panel of edges only leaves the colour in the interior",
			background: WindowBackground{Attr: navy, Graphics: edges},
			loads:      1,
			pick:       bg,
			expected: `
........
.tttttt.
.nnnnnn.
.nnnnnn.
........`,
			images: []placement{
				{image.Rect(0, 0, 1, 1), term.ImageLayerBelowBackground},
				{image.Rect(7, 0, 8, 1), term.ImageLayerBelowBackground},
				{image.Rect(0, 4, 1, 5), term.ImageLayerBelowBackground},
				{image.Rect(7, 4, 8, 5), term.ImageLayerBelowBackground},
				{image.Rect(1, 0, 7, 1), term.ImageLayerBelowBackground},
				{image.Rect(1, 4, 7, 5), term.ImageLayerBelowBackground},
				{image.Rect(0, 1, 1, 4), term.ImageLayerBelowBackground},
				{image.Rect(7, 1, 8, 4), term.ImageLayerBelowBackground},
			},
		},
		{
			name:       "a writer that cannot draw images gets the colour",
			background: WindowBackground{Attr: navy, Graphics: every},
			loads:      1,
			noImages:   true,
			pick:       bg,
			expected:   colored,
		},
		{
			name:       "a panel that is not loaded yet leaves the colour",
			background: WindowBackground{Attr: navy, Graphics: every},
			loads:      1,
			notLoaded:  true,
			pick:       bg,
			expected:   colored,
		},
		{
			name:       "the colour fills the gap a shifted row leaves",
			background: WindowBackground{Attr: navy},
			shifted:    true,
			pick:       bg,
			expected:   colored,
			fills:      []fill{{image.Rect(1, 1, 7, 2), term.ColorNavy}},
		},
		{
			name:       "the colour fills the gap a shifted row leaves without a frame",
			background: WindowBackground{Attr: navy},
			noFrame:    true,
			shifted:    true,
			pick:       bg,
			expected: `
tttttttt
nnnnnnnn
nnnnnnnn
nnnnnnnn
nnnnnnnn`,
			fills: []fill{{image.Rect(0, 0, 8, 1), term.ColorNavy}},
		},
		{
			name:       "the content's own fill of the gap wins",
			background: WindowBackground{Attr: navy},
			shifted:    true,
			gap:        term.ColorTeal,
			pick:       bg,
			expected:   colored,
			fills:      []fill{{image.Rect(1, 1, 7, 2), term.ColorTeal}},
		},
		{
			name:       "a panel under a shifted row fills its gap",
			background: WindowBackground{Attr: navy, Graphics: center},
			loads:      1,
			shifted:    true,
			pick:       bg,
			expected: `
nnnnnnnn
nttttttn
n......n
n......n
nnnnnnnn`,
			images: []placement{{image.Rect(1, 1, 7, 4), term.ImageLayerBelowBackground}},
		},
		{
			name:       "a shifted row without a colour leaves its gap",
			background: WindowBackground{Attr: term.Attributes{Fg: term.ColorWhite}},
			shifted:    true,
			pick:       fg,
			expected: `
wwwwwwww
w......w
wwwwwwww
wwwwwwww
wwwwwwww`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testWindowManagerConfig()
			cfg.Frame = !tt.noFrame
			cfg.Background = tt.background
			cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
			irq := make(interruptCounter, 16)
			cfg.Interrupter = irq
			wm, _ := NewWindowManager(&stripedContent{shifted: tt.shifted, gap: tt.gap}, cfg)
			t.Cleanup(func() { assert.NoError(t, wm.Close()) })
			wm.Resize(8, 5)
			if !tt.notLoaded {
				wm.Draw(term.NewStringWriter(8, 5))
				irq.wait(t, tt.loads)
			}

			s := newImageScreen(8, 5)
			s.noImages = tt.noImages
			wm.Draw(s)
			require.NoError(t, s.Flush())
			assert.Equal(t, tt.expected, colorMap(s, tt.pick))
			var images []placement
			var fills []fill
			for _, img := range s.images {
				if img.Fill {
					assert.Nil(t, img.Src, "a fill has no pixels")
					assert.Equal(t, term.ImageLayerBelowBackground, img.Layer)
					fills = append(fills, fill{img.Visible(), img.FillColor})
					continue
				}
				assert.Equal(t, term.ImageFitFill, img.Fit)
				images = append(images, placement{img.Bounds(), img.Layer})
			}
			assert.ElementsMatch(t, tt.images, images)
			assert.Equal(t, tt.fills, fills)

			if tt.notLoaded {
				irq.wait(t, tt.loads)
			}
		})
	}
}

func TestWindowManagerFloatingBackground(t *testing.T) {
	pics := newFramePictures(t)
	cfg := testWindowManagerConfig()
	cfg.Background = WindowBackground{Graphics: BackgroundGraphics{Center: pics.uris["i"]}}
	cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
	irq := make(interruptCounter, 16)
	cfg.Interrupter = irq
	wm, tile := NewWindowManager(&component.TestComponent{Ch: ' '}, cfg)
	t.Cleanup(func() { assert.NoError(t, wm.Close()) })
	wm.Resize(8, 5)
	float := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 1),
		FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}},
	)
	float.SetBackground(&WindowBackground{Attr: term.Attributes{Bg: term.ColorNavy}})
	wm.Draw(term.NewStringWriter(8, 5))
	irq.wait(t, 1)

	s := newImageScreen(8, 5)
	wm.Draw(s)
	assert.Equal(t, `
┌──────┐
│┌──┐ii│
││BB│ii│
│└──┘ii│
└──────┘`, s.composite(t, pics), "the floating window must cut the tile's panel")
	assert.Equal(t, `
........
.nnnn...
.nnnn...
.nnnn...
........`, colorMap(s, func(c term.Cell) term.Color { return c.Bg }),
		"the floating window must take its own background")

	assert.Equal(t, cfg.Background, tile.Background())
	float.SetBackground(nil)
	assert.Equal(t, cfg.Background, float.Background(), "nil restores the manager's")
}

func TestWindowManagerWithoutBackgroundRecordsNothing(t *testing.T) {
	wm, _ := NewWindowManager(&stripedContent{}, testWindowManagerConfig())
	wm.Resize(8, 5)
	wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 2, 1),
		FloatingConfig{},
	)
	wm.Draw(term.NewStringWriter(8, 5))
	assert.Nil(t, wm.bgWriter.Writer)
	assert.Nil(t, wm.bgWriter.cells)
}

// lineSprites writes the sprites of a frame drawn with lines through
// the middle of its cells, transparent either side, and returns its
// graphics: a cross in the corners, so that each corner's bend is a
// quarter of it.
func lineSprites(t *testing.T) component.FrameCharSetGraphics {
	t.Helper()
	h := spriteFile(t, "....\n####\n....\n....")
	v := spriteFile(t, ".#..\n.#..\n.#..\n.#..")
	cross := spriteFile(t, ".#..\n####\n.#..\n.#..")
	return component.FrameCharSetGraphics{
		TopLeft: cross, TopRight: cross, BottomLeft: cross, BottomRight: cross,
		HorizontalTop: h, HorizontalBottom: h, VerticalLeft: v, VerticalRight: v,
	}
}

// spriteFile writes the sprite of art, as spritePixels draws it, and
// returns its URI.
func spriteFile(t *testing.T, art string) string {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, spritePixels(art)))
	path := filepath.Join(t.TempDir(), "sprite.png")
	require.NoError(t, os.WriteFile(path, b.Bytes(), 0o600))
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

// fillMap returns where s holds a fill, f for one in navy and ? for one
// in another colour, and checks that each is a mask under the text.
func fillMap(t *testing.T, s *imageScreen) string {
	t.Helper()
	grid := make([][]byte, s.height)
	for y := range grid {
		grid[y] = []byte(strings.Repeat(".", s.width))
	}
	for _, img := range s.images {
		if !img.Fill {
			continue
		}
		assert.NotNil(t, img.Src, "the fill of %v has a mask", img.Pos)
		assert.Equal(t, term.ImageLayerBelowText, img.Layer)
		ch := byte('f')
		if img.FillColor != term.ColorNavy {
			ch = '?'
		}
		grid[img.Pos.Y][img.Pos.X] = ch
	}
	var b strings.Builder
	for _, row := range grid {
		b.WriteByte('\n')
		b.Write(row)
	}
	return b.String()
}

func TestWindowManagerBorderInsides(t *testing.T) {
	lines := lineSprites(t)
	pics := newFramePictures(t)
	// A corner two cells wide whose picture draws a line in its left cell
	// only, unmirrored at every corner: its right cell's part has no
	// inside to fill.
	wideCorner := lines
	half := spriteFile(t, ".#......\n####....\n.#......\n.#......")
	wideCorner.TopLeft, wideCorner.TopRight, wideCorner.BottomLeft, wideCorner.BottomRight = half, half, half, half
	wideCorner.CornerWidth = 2
	navy := term.Attributes{Bg: term.ColorNavy}
	ring := `
ffffffff
f......f
f......f
f......f
ffffffff`
	none := `
........
........
........
........
........`
	tests := []struct {
		name       string
		background WindowBackground
		graphics   component.FrameCharSetGraphics
		noImages   bool
		loads      int
		fills      string
		bg         string
	}{
		{
			name:       "the colour fills inside the lines and leaves the cells under them",
			background: WindowBackground{Attr: navy},
			graphics:   lines,
			loads:      3,
			fills:      ring,
			bg: `
........
.tttttt.
.nnnnnn.
.nnnnnn.
........`,
		},
		{
			name:       "opaque images take the colour under them",
			background: WindowBackground{Attr: navy},
			graphics: component.FrameCharSetGraphics{
				TopLeft: pics.uris["i"], TopRight: pics.uris["i"],
				BottomLeft: pics.uris["i"], BottomRight: pics.uris["i"],
				HorizontalTop: pics.uris["i"], HorizontalBottom: pics.uris["i"],
				VerticalLeft: pics.uris["i"], VerticalRight: pics.uris["i"],
			},
			loads: 1,
			fills: none,
			bg: `
nnnnnnnn
nttttttn
nnnnnnnn
nnnnnnnn
nnnnnnnn`,
		},
		{
			name:       "each cell of a corner fills inside its part of the line",
			background: WindowBackground{Attr: navy},
			graphics:   wideCorner,
			loads:      3,
			fills: `
f.fffff.
f......f
f......f
f......f
f.fffff.`,
			bg: `
.n.....n
.tttttt.
.nnnnnn.
.nnnnnn.
.n.....n`,
		},
		{
			name:       "a panel draws the cells it covers",
			background: WindowBackground{Attr: navy, Graphics: BackgroundGraphics{TopLeft: pics.uris["i"]}},
			graphics:   lines,
			loads:      4,
			fills: `
.fffffff
f......f
f......f
f......f
ffffffff`,
			bg: `
........
.tttttt.
.nnnnnn.
.nnnnnn.
........`,
		},
		{
			name:       "no colour, no fill",
			background: WindowBackground{Attr: term.Attributes{Fg: term.ColorWhite}},
			graphics:   lines,
			loads:      3,
			fills:      none,
			bg: `
........
.tttttt.
........
........
........`,
		},
		{
			name:       "a writer that cannot draw images gets the colour",
			background: WindowBackground{Attr: navy},
			graphics:   lines,
			loads:      3,
			noImages:   true,
			fills:      none,
			bg: `
nnnnnnnn
nttttttn
nnnnnnnn
nnnnnnnn
nnnnnnnn`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testWindowManagerConfig()
			cfg.Background = tt.background
			cfg.FrameCharSet.Graphics = tt.graphics
			cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
			irq := make(interruptCounter, 16)
			cfg.Interrupter = irq
			wm, _ := NewWindowManager(&stripedContent{}, cfg)
			t.Cleanup(func() { assert.NoError(t, wm.Close()) })
			wm.Resize(8, 5)
			wm.Draw(term.NewStringWriter(8, 5))
			irq.wait(t, tt.loads)

			s := newImageScreen(8, 5)
			s.noImages = tt.noImages
			wm.Draw(s)
			require.NoError(t, s.Flush())
			assert.Equal(t, tt.fills, fillMap(t, s))
			assert.Equal(t, tt.bg, colorMap(s, func(c term.Cell) term.Color { return c.Bg }))
		})
	}
}

// ownBorderContent draws a border of line sprites of its own around
// blank cells, over the background under it, and fills the inside of
// its top-left corner in teal itself.
type ownBorderContent struct {
	width, height int
	masks         FrameMasks
}

func (c *ownBorderContent) Resize(width, height int) { c.width, c.height = width, height }

func (c *ownBorderContent) Dimensions() (int, int) { return 5, 4 }

func (c *ownBorderContent) Draw(w term.Writer) {
	area := image.Rect(0, 0, c.width, c.height)
	h := spritePixels("....\n####\n....\n....")
	v := spritePixels(".#..\n.#..\n.#..\n.#..")
	cross := spritePixels(".#..\n####\n.#..\n.#..")
	for y := range c.height {
		for x := range c.width {
			p := term.Coordinates{X: x, Y: y}
			sides := RingSides(image.Pt(x, y), area)
			if sides == 0 {
				w.SetCell(p, term.NewCell('B', 1, term.Attributes{}))
				continue
			}
			w.SetCell(p, term.NewCell(' ', 1, term.Attributes{Attrs: term.AttrKeepBackground}))
			src := image.Image(cross)
			switch sides {
			case SideTop, SideBottom:
				src = h
			case SideLeft, SideRight:
				src = v
			}
			sprite := term.Image{Src: src, ID: term.NewImageID(), Pos: p, Width: 1, Height: 1}
			w.DrawImage(sprite)
			if x == 0 && y == 0 {
				fill, ok := c.masks.Inside(sprite, sides, term.ColorTeal)
				if ok {
					w.DrawImage(fill)
				}
			}
		}
	}
}

func TestWindowManagerFloatingOwnBorder(t *testing.T) {
	cfg := testWindowManagerConfig()
	cfg.Frame = false
	cfg.FloatingOwnBorder = true
	wm, _ := NewWindowManager(&component.TestComponent{
		Ch: 'T', Attributes: term.Attributes{Bg: term.ColorWhite},
	}, cfg)
	wm.Resize(9, 6)
	float := wm.FloatingWindow(&ownBorderContent{}, FloatingConfig{Offset: term.Coordinates{X: 2, Y: 1}})

	s := newImageScreen(9, 6)
	wm.Draw(s)
	require.NoError(t, s.Flush())
	assert.Equal(t, `
wwwwwwwww
wwwwwwwww
www...www
www...www
wwwwwwwww
wwwwwwwww`, colorMap(s, func(c term.Cell) term.Color { return c.Bg }),
		"the window clears only inside its border")

	float.SetBackground(&WindowBackground{Attr: term.Attributes{Bg: term.ColorNavy}})
	s = newImageScreen(9, 6)
	wm.Draw(s)
	require.NoError(t, s.Flush())
	assert.Equal(t, `
wwwwwwwww
wwwwwwwww
wwwnnnwww
wwwnnnwww
wwwwwwwww
wwwwwwwww`, colorMap(s, func(c term.Cell) term.Color { return c.Bg }),
		"the cells under the border keep the window under it")
	assert.Equal(t, `
.........
..?ffff..
..f...f..
..f...f..
..fffff..
.........`, fillMap(t, s), "the window's own fill stays, and the colour fills the others")
}

func TestWindowManagerFloatingBorderInsides(t *testing.T) {
	cfg := testWindowManagerConfig()
	cfg.FrameCharSet.Graphics = lineSprites(t)
	cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
	irq := make(interruptCounter, 16)
	cfg.Interrupter = irq
	wm, _ := NewWindowManager(&component.TestComponent{
		Ch: 'T', Attributes: term.Attributes{Bg: term.ColorTeal},
	}, cfg)
	t.Cleanup(func() { assert.NoError(t, wm.Close()) })
	wm.Resize(10, 6)
	float := wm.FloatingWindow(
		component.StaticFloating(&component.TestComponent{Ch: 'B'}, 3, 2),
		FloatingConfig{Offset: term.Coordinates{X: 2, Y: 1}},
	)
	float.SetBackground(&WindowBackground{Attr: term.Attributes{Bg: term.ColorNavy}})
	wm.Draw(term.NewStringWriter(10, 6))
	irq.wait(t, 3)

	s := newImageScreen(10, 6)
	wm.Draw(s)
	require.NoError(t, s.Flush())
	assert.Equal(t, `
..........
..fffff...
..f...f...
..f...f...
..fffff...
..........`, fillMap(t, s))
	assert.Equal(t, `
..........
.tttttttt.
.ttnnnttt.
.ttnnnttt.
.tttttttt.
..........`, colorMap(s, func(c term.Cell) term.Color { return c.Bg }),
		"the cells under the border show the window under it")

	float.SetBackground(&WindowBackground{})
	s = newImageScreen(10, 6)
	wm.Draw(s)
	require.NoError(t, s.Flush())
	assert.Equal(t, `
..........
..?????...
..?...?...
..?...?...
..?????...
..........`, fillMap(t, s), "without a colour the inside takes the default background")
	for _, img := range s.images {
		if img.Fill {
			assert.Equal(t, term.ColorDefault, img.FillColor)
		}
	}
	assert.Equal(t, `
..........
.tttttttt.
.tt...ttt.
.tt...ttt.
.tttttttt.
..........`, colorMap(s, func(c term.Cell) term.Color { return c.Bg }))
}

// statusRowContent draws B in every row but the last, which the renderer
// moves half a cell down into the bottom of a frame, as a program's
// status row is: blank cells with the default colours, and an S on white
// last, as its timer is.
type statusRowContent struct {
	width, height int
}

func (c *statusRowContent) Resize(width, height int) { c.width, c.height = width, height }

func (c *statusRowContent) Dimensions() (int, int) { return 4, 2 }

func (c *statusRowContent) Draw(w term.Writer) {
	for y := range c.height {
		for x := range c.width {
			cell := term.NewCell('B', 1, term.Attributes{})
			if y == c.height-1 {
				attr := term.Attributes{Attrs: term.AttrVerticalRenderOffset}
				ch := ' '
				if x == c.width-1 {
					attr.Bg, ch = term.ColorWhite, 'S'
				}
				cell = term.NewCell(ch, 1, attr)
			}
			w.SetCell(term.Coordinates{X: x, Y: y}, cell)
		}
	}
}

// wallpaperContent draws T on teal under a picture over all of it, as a
// window showing a wallpaper does.
type wallpaperContent struct {
	width, height int
	pic           term.Image
}

func (c *wallpaperContent) Resize(width, height int) { c.width, c.height = width, height }

func (c *wallpaperContent) Draw(w term.Writer) {
	for y := range c.height {
		for x := range c.width {
			w.SetCell(term.Coordinates{X: x, Y: y},
				term.NewCell('T', 1, term.Attributes{Bg: term.ColorTeal}))
		}
	}
	pic := c.pic
	pic.Width, pic.Height = c.width, c.height
	w.DrawImage(pic)
}

func TestWindowManagerFloatingBorderOverAPicture(t *testing.T) {
	navy := WindowBackground{Attr: term.Attributes{Bg: term.ColorNavy}}
	pics := newFramePictures(t)
	p := pics.uris["i"]
	panel := WindowBackground{Attr: navy.Attr, Graphics: BackgroundGraphics{
		TopLeft: p, TopRight: p, BottomLeft: p, BottomRight: p,
		HorizontalTop: p, HorizontalBottom: p, VerticalLeft: p, VerticalRight: p, Center: p,
	}}
	tests := []struct {
		name       string
		ownBorder  bool
		background WindowBackground
		inside     term.Color
	}{
		{name: "the window manager's border, unfocused", inside: term.ColorDefault},
		{name: "the window manager's border, focused", background: navy, inside: term.ColorNavy},
		{name: "the window manager's border over a panel", background: panel},
		{name: "the window's own border, unfocused", ownBorder: true, inside: term.ColorDefault},
		{name: "the window's own border, focused", ownBorder: true, background: navy, inside: term.ColorNavy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testWindowManagerConfig()
			cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
			irq := make(interruptCounter, 16)
			cfg.Interrupter = irq
			var content component.Floating = &statusRowContent{}
			loads := 3
			// The wallpaper fills the tile, inside the tile's frame when
			// the window manager draws frames.
			tile := image.Rect(1, 1, 9, 5)
			if tt.ownBorder {
				cfg.Frame, cfg.FloatingOwnBorder = false, true
				content, loads, tile = &ownBorderContent{}, 0, image.Rect(0, 0, 10, 6)
			} else {
				cfg.FrameCharSet.Graphics = lineSprites(t)
			}
			hasPanel := !tt.background.Graphics.Empty()
			if hasPanel {
				loads++
			}
			wallpaper := term.Image{Src: image.NewRGBA(image.Rect(0, 0, 1, 1)), ID: term.NewImageID()}
			wm, _ := NewWindowManager(&wallpaperContent{pic: wallpaper}, cfg)
			t.Cleanup(func() { assert.NoError(t, wm.Close()) })
			wm.Resize(10, 6)
			float := wm.FloatingWindow(content, FloatingConfig{Offset: term.Coordinates{X: 2, Y: 1}})
			float.SetBackground(&tt.background)
			wm.Draw(term.NewStringWriter(10, 6))
			irq.wait(t, loads)

			w := cell.NewBufferWriter(context.Background(), 10, 6)
			wm.Draw(w)
			images, cells := w.Images(), w.RawCells()
			pos := float.Position()
			area := image.Rect(pos.X, pos.Y, pos.X+float.Width(), pos.Y+float.Height())
			for y := tile.Min.Y; y < tile.Max.Y; y++ {
				for x := tile.Min.X; x < tile.Max.X; x++ {
					p := image.Pt(x, y)
					picture, inside, slice := -1, -1, -1
					for k, img := range images {
						if !p.In(img.Visible()) {
							continue
						}
						switch {
						case img.ID == wallpaper.ID:
							picture = k
						case img.Fill && img.Src != nil:
							inside = k
						case img.Layer != term.ImageLayerAboveText:
							slice = k
						}
					}
					switch {
					case !p.In(area):
						require.GreaterOrEqual(t, picture, 0, "the wallpaper at %v", p)
						assert.Equal(t, term.ImageLayerAboveText, images[picture].Layer,
							"the wallpaper away from the window at %v", p)
					case RingSides(p, area) == 0:
						assert.Negative(t, picture, "the wallpaper inside the window at %v", p)
					default:
						require.GreaterOrEqual(t, picture, 0,
							"the wallpaper shows outside the line of the border at %v", p)
						assert.Equal(t, term.ImageLayerBelowText, images[picture].Layer,
							"the wallpaper at %v goes under the border", p)
						switch {
						case hasPanel:
							require.Greater(t, slice, picture,
								"the panel at %v goes over the wallpaper", p)
							assert.Equal(t, term.ImageLayerBelowText, images[slice].Layer,
								"the panel at %v goes under the border with the wallpaper", p)
						case !tt.ownBorder || p != area.Min:
							require.Greater(t, inside, picture,
								"the inside of the line at %v goes over the wallpaper", p)
							assert.Equal(t, tt.inside, images[inside].FillColor,
								"the inside of the line at %v takes the window's background", p)
						default:
							require.Greater(t, inside, picture,
								"the inside of the line at %v goes over the wallpaper", p)
						}
						assert.Equal(t, ' ', cells[y][x].Ch, "the text under the border at %v", p)
						assert.Equal(t, term.ColorTeal, cells[y][x].Bg,
							"the border at %v keeps the background under the window", p)
					}
				}
			}
			if tt.ownBorder {
				return
			}

			// The status row is moved half a cell into the bottom of the
			// border, timer included, over the inside of the line or the
			// panel there.
			inside := slices.IndexFunc(images, func(img term.Image) bool {
				return img.Src != nil && img.ID != wallpaper.ID &&
					img.Layer == term.ImageLayerBelowText && image.Pt(6, 4).In(img.Visible())
			})
			timer := slices.IndexFunc(images, func(img term.Image) bool {
				return img.Fill && img.Src == nil && img.FillColor == term.ColorWhite &&
					img.Visible() == image.Rect(6, 3, 7, 4) &&
					img.Layer == term.ImageLayerBelowText && img.VerticalRenderOffset
			})
			require.GreaterOrEqual(t, inside, 0)
			assert.Greater(t, timer, inside,
				"the timer's background moves into the border with it, over the inside of the line")
		})
	}
}

func TestWindowManagerWindowBarOverWhatIsUnder(t *testing.T) {
	pics := newFramePictures(t)
	sprites := lineSprites(t)
	frames := []struct {
		name     string
		graphics component.FrameCharSetGraphics
	}{
		{name: "runes frames"},
		{name: "image frames", graphics: sprites},
	}
	images := WindowBarGraphics{Left: "s", Horizontal: "v", Right: "s"}
	outsets := images
	outsets.Outset = WindowBarOutset{Top: 1, Bottom: 1, Sides: 1, Inward: 1, HorizontalTop: 1}
	overlay := images
	overlay.Overlay = true
	bars := []struct {
		name     string
		graphics WindowBarGraphics
		// drawn reports that the bar's images load and show.
		drawn bool
	}{
		{name: "images", graphics: images, drawn: true},
		{name: "images with outsets", graphics: outsets, drawn: true},
		{name: "overlay images", graphics: overlay, drawn: true},
		{
			name:     "images that fail to load",
			graphics: WindowBarGraphics{Left: "missing", Horizontal: "missing", Right: "missing"},
		},
		{name: "runes"},
	}
	colours := []struct {
		name string
		attr term.Attributes
	}{
		{name: "bar colours", attr: term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}},
		{name: "default colours"},
	}
	writers := []struct {
		name   string
		images bool
	}{
		{name: "a writer of images", images: true},
		{name: "a writer of cells only"},
	}
	const width, height, title = 16, 7, "abcdef"
	icon := DefaultWindowManagerConfig().CloseIconAttr
	for _, frame := range frames {
		for _, under := range underWindows {
			for _, bg := range floatingBackgrounds(pics) {
				for _, bar := range bars {
					for _, colour := range colours {
						for _, writer := range writers {
							name := strings.Join([]string{
								frame.name, under.name, bg.name, bar.name, colour.name, writer.name,
							}, "/")
							t.Run(name, func(t *testing.T) {
								drawn, bare := bar.drawn && writer.images, bg.bare || !writer.images
								newWriter := func() (*cell.BufferWriter, term.Writer) {
									w := cell.NewBufferWriter(context.Background(), width, height)
									if !writer.images {
										return w, imagelessBufferWriter{w}
									}
									return w, w
								}
								cfg := testWindowManagerConfig()
								cfg.WindowBar = true
								cfg.FrameAttr = colour.attr
								cfg.FrameCharSet.Graphics = frame.graphics
								cfg.WindowBarCharSet.Graphics = pics.resolveWindowBar(bar.graphics)
								cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
								irq := make(interruptCounter, 16)
								cfg.Interrupter = irq
								pane := under.pane()
								wm, left := NewWindowManager(pane, cfg)
								t.Cleanup(func() { assert.NoError(t, wm.Close()) })
								// The border between the two tiles runs under the title.
								_, ok := wm.SplitVertical(left, under.pane())
								require.True(t, ok)
								wm.Resize(width, height)
								wm.Draw(term.NewStringWriter(width, height))
								irq.wait(t, pics.loads(frame.graphics))
								before, target := newWriter()
								wm.Draw(target)
								underImages, underCells := before.Images(), before.RawCells()

								float := wm.FloatingWindow(
									component.StaticFloating(&component.TestComponent{Ch: 'B'}, 10, 2),
									FloatingConfig{Offset: term.Coordinates{X: 2, Y: 1}, Title: title},
								)
								float.SetBackground(&bg.bg)
								wm.Draw(term.NewStringWriter(width, height))
								irq.wait(t, pics.loadsWindowBar(bar.graphics)+bg.loads)
								w, target := newWriter()
								wm.Draw(target)
								images, cells := w.Images(), w.RawCells()
								isEnd := func(k int) bool { return pics.name(images[k]) == 's' }
								isMiddle := func(k int) bool { return pics.name(images[k]) == 'v' }

								pos := float.Position()
								require.Equal(t, 12, float.Width())
								text := map[int]rune{WindowBarCloseIconX: wm.config.CloseIcon}
								for i, r := range " " + title + " " {
									text[3+i] = r
								}
								keeps := drawn && !bar.graphics.Overlay
								tileBorders := 0
								for x := pos.X; x < pos.X+float.Width(); x++ {
									p := image.Pt(x, pos.Y)
									cover := imagesOver(images, p)
									shown := shownOver(images, cover)
									ch, isText := text[x-pos.X]
									end := x == pos.X || x == pos.X+float.Width()-1
									if drawn && !end {
										middles := slices.DeleteFunc(slices.Clone(cover), func(k int) bool { return !isMiddle(k) })
										assert.Len(t, middles, 1, "the bar's image is drawn once at %v", p)
									}
									if !drawn && shown >= 0 {
										assert.Fail(t, "the bar's runes leave an image over them",
											"%s at %v", imageRole(pics, images[shown]), p)
									}
									if !bare {
										assert.True(t, slices.ContainsFunc(cover, func(k int) bool {
											return pics.name(images[k]) == 'p'
										}), "the window's background is drawn at %v", p)
									}
									if keeps || drawn && !isText {
										for _, k := range imagesOver(underImages, p) {
											if underImages[k].Layer == term.ImageLayerAboveText {
												tileBorders++
											}
											id := underImages[k].ID
											assert.True(t, slices.ContainsFunc(cover, func(j int) bool {
												return images[j].ID == id
											}), "%s still shows under the bar at %v", imageRole(pics, underImages[k]), p)
										}
									}
									if !isText {
										continue
									}
									for _, k := range cover {
										if !isEnd(k) {
											assert.NotEqual(t, term.ImageLayerAboveText, images[k].Layer,
												"%s goes over the text at %v", imageRole(pics, images[k]), p)
										}
									}

									// The cells of the text over images keep the background
									// under the window, as a floating window's border does,
									// which leaves them to the window's background.
									want := term.Attributes{Fg: colour.attr.Fg, Bg: underCells[p.Y][p.X].Bg}
									defaultBg := true
									if !keeps {
										want = term.Attributes{Fg: colour.attr.Bg, Bg: colour.attr.Fg}
										defaultBg = want.Bg == term.ColorDefault
									}
									if x-pos.X == WindowBarCloseIconX {
										want.Fg = icon.Fg
									}
									if defaultBg {
										if want.Fg == term.ColorDefault {
											want.Fg = bg.bg.Attr.Fg
										}
										if bare && bg.bg.Attr.Bg != term.ColorDefault {
											want.Bg = bg.bg.Attr.Bg
										}
									}
									assert.Equal(t, ch, cells[p.Y][p.X].Ch, "the text at %v", p)
									assert.Equal(t, want, cells[p.Y][p.X].Attributes(), "the colours of the text at %v", p)
								}
								if keeps && frame.graphics.HorizontalTop != "" {
									assert.Positive(t, tileBorders, "no border of a tile runs under the bar")
								}

								// The bar shows over all it covers, outsets included, and
								// its ends over the rest of it.
								for y := pos.Y - 1; y <= pos.Y+1; y++ {
									for x := pos.X - 1; x <= pos.X+float.Width(); x++ {
										p := image.Pt(x, y)
										cover := imagesOver(images, p)
										want := 'v'
										switch {
										case slices.ContainsFunc(cover, isEnd):
											want = 's'
										case !slices.ContainsFunc(cover, isMiddle):
											continue
										}
										shown := shownOver(images, cover)
										require.GreaterOrEqual(t, shown, 0, "nothing shows over the bar at %v", p)
										assert.Equal(t, want, pics.name(images[shown]),
											"%s shows over the bar at %v", imageRole(pics, images[shown]), p)
									}
								}
							})
						}
					}
				}
			}
		}
	}
}

func TestWindowManagerMovedRowOverFloatingBorder(t *testing.T) {
	pics := newFramePictures(t)
	sprites := lineSprites(t)
	rows := []struct {
		name string
		top  bool
	}{
		{name: "the bottom row moved down"},
		{name: "the top row moved up", top: true},
	}
	const width, height = 12, 7
	for _, row := range rows {
		for _, under := range underWindows {
			for _, bg := range floatingBackgrounds(pics) {
				t.Run(row.name+"/"+under.name+"/"+bg.name, func(t *testing.T) {
					cfg := testWindowManagerConfig()
					cfg.FrameCharSet.Graphics = sprites
					cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
					irq := make(interruptCounter, 16)
					cfg.Interrupter = irq
					wm, _ := NewWindowManager(under.pane(), cfg)
					t.Cleanup(func() { assert.NoError(t, wm.Close()) })
					wm.Resize(width, height)
					content := &movedRowContent{top: row.top}
					float := wm.FloatingWindow(content, FloatingConfig{Offset: term.Coordinates{X: 2, Y: 1}})
					float.SetBackground(&bg.bg)
					wm.Draw(term.NewStringWriter(width, height))
					irq.wait(t, pics.loads(sprites)+bg.loads)
					w := cell.NewBufferWriter(context.Background(), width, height)
					wm.Draw(w)
					images, cells := w.Images(), w.RawCells()

					pos := float.Position()
					moved, border := pos.Y+float.Height()-2, pos.Y+float.Height()-1
					if row.top {
						moved, border = pos.Y+1, pos.Y
					}
					for x := pos.X + 1; x < pos.X+float.Width()-1; x++ {
						n, p := term.Coordinates{X: x, Y: moved}, image.Pt(x, border)
						own := content.bg(x - pos.X - 1)
						var fills []int
						for k, img := range images {
							if img.Fill && img.Src == nil && img.Pos == n && img.Width == 1 && img.Height == 1 {
								fills = append(fills, k)
							}
						}
						if own == term.ColorDefault {
							assert.Empty(t, fills, "the cell at %v has no background of its own to move", n)
							continue
						}
						assert.Equal(t, own, cells[n.Y][n.X].Bg, "the cell at %v", n)
						require.Len(t, fills, 1, "the background of the cell at %v moves into the border", n)
						fill := images[fills[0]]
						assert.Equal(t, own, fill.FillColor, "the background moved from %v", n)
						assert.Equal(t, term.ImageLayerBelowText, fill.Layer, "the background moved from %v", n)
						assert.Equal(t, !row.top, fill.VerticalRenderOffset, "the background moved from %v", n)
						assert.Equal(t, row.top, fill.NegativeVerticalRenderOffset, "the background moved from %v", n)
						for _, k := range imagesOver(images, p) {
							if images[k].Layer == term.ImageLayerBelowText {
								assert.Less(t, k, fills[0], "the background moved from %v goes over %s at %v",
									n, imageRole(pics, images[k]), p)
							}
						}
					}
				})
			}
		}
	}
}

// underWindow is what a tile under a floating window shows: cells in the
// default colours, under or over a picture in layer when picture is set,
// as a pane showing a wallpaper or a program's graphics does.
type underWindow struct {
	name    string
	picture bool
	layer   term.ImageLayer
}

var underWindows = []underWindow{
	{name: "cells"},
	{name: "a picture over the text", picture: true, layer: term.ImageLayerAboveText},
	{name: "a picture under the text", picture: true, layer: term.ImageLayerBelowText},
	{name: "a picture under the background", picture: true, layer: term.ImageLayerBelowBackground},
}

// underPicture is the picture of the panes of underWindow.
var underPicture = term.Image{Src: image.NewRGBA(image.Rect(0, 0, 1, 1)), ID: term.NewImageID()}

func (u underWindow) pane() *paneContent {
	c := &paneContent{}
	if u.picture {
		c.pic = underPicture
		c.pic.Layer = u.layer
	}
	return c
}

// paneContent draws T in the default colours, and pic stretched over
// all of it when it has a picture.
type paneContent struct {
	pic           term.Image
	width, height int
}

func (c *paneContent) Resize(width, height int) { c.width, c.height = width, height }

func (c *paneContent) Draw(w term.Writer) {
	for y := range c.height {
		for x := range c.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('T', 1, term.Attributes{}))
		}
	}
	if c.pic.Src != nil {
		pic := c.pic
		pic.Width, pic.Height = c.width, c.height
		w.DrawImage(pic)
	}
}

// imagelessBufferWriter is a writer that draws no images, as a terminal
// without graphics is.
type imagelessBufferWriter struct{ *cell.BufferWriter }

func (imagelessBufferWriter) DrawImage(term.Image) bool { return false }

// floatingBackground is a background of a floating window, which starts
// loading loads images and leaves its cells bare of images, for them to
// take its colour, when bare is set.
type floatingBackground struct {
	name  string
	bg    WindowBackground
	loads int
	bare  bool
}

func floatingBackgrounds(pics framePictures) []floatingBackground {
	colour := term.Attributes{Fg: term.ColorYellow, Bg: term.ColorNavy}
	panel := func(name string) BackgroundGraphics {
		uri := pics.uris[name]
		return BackgroundGraphics{
			TopLeft: uri, TopRight: uri, BottomLeft: uri, BottomRight: uri,
			HorizontalTop: uri, HorizontalBottom: uri, VerticalLeft: uri, VerticalRight: uri,
			Center: uri,
		}
	}
	return []floatingBackground{
		{name: "no background", bare: true},
		{name: "a colour", bg: WindowBackground{Attr: colour}, bare: true},
		{name: "images", bg: WindowBackground{Graphics: panel("p")}, loads: 1},
		{name: "a colour and images", bg: WindowBackground{Attr: colour, Graphics: panel("p")}, loads: 1},
		{
			name:  "a colour and images that fail to load",
			bg:    WindowBackground{Attr: colour, Graphics: panel("missing")},
			loads: 1,
			bare:  true,
		},
	}
}

// movedRowContent draws B in every row but the one the renderer moves
// half a cell into the frame, its last or, when top, its first, as a
// program's status bar is: a blank in the default colours, a character
// on a background and a blank on another, over and over.
type movedRowContent struct {
	top           bool
	width, height int
}

func (c *movedRowContent) Resize(width, height int) { c.width, c.height = width, height }

func (c *movedRowContent) Dimensions() (int, int) { return 6, 3 }

// bg returns the background of the cell x of the moved row.
func (c *movedRowContent) bg(x int) term.Color {
	return [...]term.Color{term.ColorDefault, term.ColorWhite, term.ColorMaroon}[x%3]
}

func (c *movedRowContent) Draw(w term.Writer) {
	moved, offset := c.height-1, term.AttrVerticalRenderOffset
	if c.top {
		moved, offset = 0, term.AttrNegativeVerticalRenderOffset
	}
	for y := range c.height {
		for x := range c.width {
			cell := term.NewCell('B', 1, term.Attributes{})
			if y == moved {
				ch := ' '
				if x%3 == 1 {
					ch = 'S'
				}
				cell = term.NewCell(ch, 1, term.Attributes{Bg: c.bg(x), Attrs: offset})
			}
			w.SetCell(term.Coordinates{X: x, Y: y}, cell)
		}
	}
}

// imagesOver returns the indexes of the images that cover p.
func imagesOver(images []term.Image, p image.Point) []int {
	var ret []int
	for k, img := range images {
		if p.In(img.Visible()) {
			ret = append(ret, k)
		}
	}
	return ret
}

// shownOver returns which of the images at the indexes over shows on
// top of their cell, as a writer composing the layers shows them: the
// last above the text, or else the last under it, or -1 for none.
func shownOver(images []term.Image, over []int) int {
	shown := -1
	for _, k := range over {
		switch {
		case images[k].Layer == term.ImageLayerAboveText:
			shown = k
		case images[k].Layer == term.ImageLayerBelowText &&
			(shown < 0 || images[shown].Layer != term.ImageLayerAboveText):
			shown = k
		}
	}
	return shown
}

// imageRole describes what img is in the tests drawing a floating
// window over underWindows with floatingBackgrounds.
func imageRole(pics framePictures, img term.Image) string {
	switch {
	case img.ID == underPicture.ID:
		return "the picture under the window"
	case img.Fill:
		return "a fill"
	case pics.name(img) == 'v':
		return "the bar"
	case pics.name(img) == 's':
		return "an end of the bar"
	case pics.name(img) == 'p':
		return "the window's background"
	}
	return "a frame's image"
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
