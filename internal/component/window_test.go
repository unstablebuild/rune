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
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestWindowContentSize(t *testing.T) {
	frameless := testWindowManagerConfig()
	frameless.Frame = false
	withBar := DefaultWindowManagerConfig()
	withBar.NoMaxSize = true
	for _, tc := range []struct {
		desc         string
		cfg          WindowManagerConfig
		open         func(*WindowManager, Window) Window
		wantW, wantH int
	}{
		{
			desc: "a framed tile less its frame", cfg: testWindowManagerConfig(),
			open:  func(_ *WindowManager, root Window) Window { return root },
			wantW: 18, wantH: 8,
		},
		{
			desc: "a frameless tile", cfg: frameless,
			open:  func(_ *WindowManager, root Window) Window { return root },
			wantW: 20, wantH: 10,
		},
		{
			desc: "a split tile", cfg: testWindowManagerConfig(),
			open: func(wm *WindowManager, root Window) Window {
				win, _ := wm.SplitVertical(root, &component.TestComponent{Ch: 'R'})
				return win
			},
			wantW: 8, wantH: 8,
		},
		{
			desc: "a floating window at the size it asks for", cfg: testWindowManagerConfig(),
			open: func(wm *WindowManager, _ Window) Window {
				return wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'F'}, 6, 3),
					FloatingConfig{Alignment: component.AlignmentCentered})
			},
			wantW: 6, wantH: 3,
		},
		{
			desc: "a floating window with a bar", cfg: withBar,
			open: func(wm *WindowManager, _ Window) Window {
				return wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'F'}, 6, 3),
					FloatingConfig{Alignment: component.AlignmentCentered, Title: "cmd"})
			},
			wantW: 6, wantH: 3,
		},
		{
			desc: "a floating window larger than the screen", cfg: testWindowManagerConfig(),
			open: func(wm *WindowManager, _ Window) Window {
				return wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'F'}, 100, 100),
					FloatingConfig{Alignment: component.AlignmentCentered})
			},
			wantW: 16, wantH: 6,
		},
	} {
		t.Run(tc.desc, func(t *testing.T) {
			wm, root := NewWindowManager(&component.TestComponent{Ch: 'C'}, tc.cfg)
			wm.Resize(20, 10)
			win := tc.open(wm, root)
			wm.Draw(term.NewStringWriter(20, 10))
			w, h := win.ContentSize()
			assert.Equal(t, []int{tc.wantW, tc.wantH}, []int{w, h})
		})
	}
}

func TestWindowSetWindowBarCharSet(t *testing.T) {
	pics := newFramePictures(t)
	cfg := testWindowManagerConfig()
	cfg.WindowBar = true
	cfg.CloseIcon = 0
	cfg.WindowBarCharSet.Graphics = pics.resolveWindowBar(WindowBarGraphics{Left: "L", Horizontal: "L", Right: "L"})
	cfg.FrameGraphicsStorage = storagestub.NewInMemoryService()
	irq := make(interruptCounter, 8)
	cfg.Interrupter = irq
	wm, root := NewWindowManager(&component.TestComponent{Ch: 'C'}, cfg)
	t.Cleanup(func() { assert.NoError(t, wm.Close()) })
	wm.Resize(12, 5)
	floating := wm.FloatingWindow(component.StaticFloating(&component.TestComponent{Ch: 'B'}, 8, 2), FloatingConfig{Offset: term.Coordinates{X: 1, Y: 1}})
	override := WindowBarCharSet{Left: '^', Horizontal: '^', Right: '^', Graphics: pics.resolveWindowBar(WindowBarGraphics{Left: "R", Horizontal: "R", Right: "R"})}
	assert.False(t, root.SetWindowBarCharSet(&override))
	check := func(row string, loads int) {
		t.Helper()
		wm.Draw(term.NewStringWriter(12, 5))
		irq.wait(t, loads)
		screen := newImageScreen(12, 5)
		screen.over = image.Rect(1, 1, 11, 5)
		wm.Draw(screen)
		assert.Contains(t, screen.render(t, pics, cfg.FrameAttr, false), "\n│"+row+"│\n")
	}
	check("LLLLLLLLLL", 1)
	require.True(t, floating.SetWindowBarCharSet(&override))
	check("RRRRRRRRRR", 1)
	floating.SetContent(&component.TestComponent{Ch: 'X'})
	check("RRRRRRRRRR", 0)
	override.Graphics = WindowBarGraphics{}
	require.True(t, floating.SetWindowBarCharSet(&override))
	check("^^^^^^^^^^", 0)
	require.True(t, floating.SetWindowBarCharSet(nil))
	check("LLLLLLLLLL", 0)
	cfg.WindowBarCharSet = WindowBarCharSet{Left: 'd', Horizontal: 'd', Right: 'd'}
	wm.Restyle(cfg)
	check("dddddddddd", 0)
}
