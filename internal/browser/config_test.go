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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	tcomponent "unstable.build/rune/internal/component"
	thandler "unstable.build/rune/internal/handler"
)

func TestConfigRestyled(t *testing.T) {
	red := term.Attributes{Fg: term.ColorRed}
	blue := term.Attributes{Fg: term.ColorBlue}
	plus := component.FrameCharSet{TopLeft: '+', HorizontalTop: '-', VerticalLeft: '|'}
	storage := storagestub.NewInMemoryService()

	c := DefaultConfig()
	c.FocusTabAttr = red
	c.TabBarHeight = 3
	c.FrameUnion = true
	c.Frame = true
	c.WindowBar = true
	c.NoMaxSize = true
	c.Dim = true
	c.FrameGraphicsStorage = storage
	c.FloatingBar = thandler.NopFloatingBarHandler{}
	c.PromptConfig.MinWidth = 42
	c.PromptConfig.Images = PromptImages{Idle: PromptButtonImage{Middle: "file:///old.png"}}

	s := Style{
		WindowManager: thandler.WindowManagerConfig{
			WindowManagerConfig: tcomponent.WindowManagerConfig{
				FrameAttr:        blue,
				FrameCharSet:     plus,
				ScrollBarAttr:    blue,
				ScrollBarCharSet: component.ScrollBarCharSet{Middle: '#'},
				ScrollBarOutset:  2,
				WindowBarCharSet: tcomponent.WindowBarCharSet{Left: '='},
				CloseIcon:        'x',
				CloseIconAttr:    blue,
			},
			FocusFrameAttr:        blue,
			FocusFrameCharSet:     plus,
			ScrollBarHoverCharSet: component.ScrollBarCharSet{Middle: 'H'},
		},
		FrameUnionCharSet: component.FrameUnionCharSet{FrameCharSet: plus, Left: 'L', Right: 'R', Top: 'T', Bottom: 'B'},
		Prompt: PromptConfig{
			TextAttr:       blue,
			HighlightAttr:  blue,
			HoverAttr:      blue,
			ClickedAttr:    blue,
			BackgroundAttr: blue,
			MinWidth:       7,
			Images:         PromptImages{Idle: PromptButtonImage{Middle: "file:///new.png"}},
		},
		Wallpaper: Wallpaper{BackgroundAttr: blue},
		Tabs: TabStyle{
			FocusTabAttr:          blue,
			NonFocusTabAttr:       blue,
			FocusTabIconAttr:      blue,
			NonFocusTabIconAttr:   blue,
			FocusTabHighlightAttr: blue,
			FocusTabHighlightChar: '=',
			Images: TabImages{
				PromptImages: PromptImages{Selected: PromptButtonImage{Middle: "file:///tab.png"}},
				FocusWindow:  PromptButtonImage{Middle: "file:///window.png"},
			},
			HighlightGraphics: tcomponent.LineGraphics{Middle: "file:///highlight.png", Overlay: true},
		},
	}

	want := c
	want.FrameAttr = blue
	want.WindowManagerConfig.FrameCharSet = plus
	want.ScrollBarAttr = blue
	want.ScrollBarCharSet = s.WindowManager.ScrollBarCharSet
	want.ScrollBarOutset = 2
	want.WindowBar = false
	want.WindowBarCharSet = s.WindowManager.WindowBarCharSet
	want.CloseIcon = 'x'
	want.CloseIconAttr = blue
	want.FocusFrameAttr = blue
	want.FocusFrameCharSet = plus
	want.ScrollBarHoverCharSet = s.WindowManager.ScrollBarHoverCharSet
	want.FrameUnionCharSet = s.FrameUnionCharSet
	want.PromptConfig.TextAttr = blue
	want.PromptConfig.HighlightAttr = blue
	want.PromptConfig.HoverAttr = blue
	want.PromptConfig.ClickedAttr = blue
	want.PromptConfig.BackgroundAttr = blue
	want.PromptConfig.Images = s.Prompt.Images
	want.Wallpaper = s.Wallpaper
	want.FocusTabAttr = blue
	want.NonFocusTabAttr = blue
	want.FocusTabIconAttr = blue
	want.NonFocusTabIconAttr = blue
	want.FocusTabHighlightAttr = blue
	want.FocusTabHighlightChar = '='
	want.TabImages = s.Tabs.Images
	want.FocusTabHighlightGraphics = s.Tabs.HighlightGraphics

	assert.Equal(t, want, c.Restyled(s))
}

func TestTabImagesImages(t *testing.T) {
	g := tcomponent.NewFrameGraphics(storagestub.NewInMemoryService(), term.NopInterrupter())
	t.Cleanup(func() { assert.NoError(t, g.Close()) })
	button := func(name string) PromptButtonImage {
		return PromptButtonImage{
			Left:   "file:///" + name + "-left.png",
			Middle: "file:///" + name + "-middle.png",
			Right:  "file:///" + name + "-right.png",
		}
	}
	outset := component.ButtonOutset{Top: 1, Bottom: 2, Sides: 3}
	all := TabImages{
		PromptImages: PromptImages{
			Idle:     button("idle"),
			Selected: button("selected"),
			Hover:    button("hover"),
			Pressed:  button("pressed"),
			Outset:   outset,
		},
		FocusWindow: button("window"),
	}
	tests := []struct {
		name   string
		images TabImages
		g      *tcomponent.FrameGraphics
		// want pairs each drawn image with the state it is expected from.
		want func(*tcomponent.TabImages) map[*component.ButtonImage]PromptButtonImage
	}{
		{name: "no graphics", images: all},
		{name: "no images", images: TabImages{PromptImages: PromptImages{Outset: outset}}, g: g},
		{
			name: "only the focus window", g: g,
			images: TabImages{
				PromptImages: PromptImages{Outset: outset},
				FocusWindow:  button("window"),
			},
			want: func(got *tcomponent.TabImages) map[*component.ButtonImage]PromptButtonImage {
				return map[*component.ButtonImage]PromptButtonImage{
					&got.Idle:        {},
					&got.Selected:    {},
					&got.Hover:       {},
					&got.Pressed:     {},
					&got.Highlighted: button("window"),
				}
			},
		},
		{
			name: "every state", images: all, g: g,
			want: func(got *tcomponent.TabImages) map[*component.ButtonImage]PromptButtonImage {
				return map[*component.ButtonImage]PromptButtonImage{
					&got.Idle:        all.Idle,
					&got.Selected:    all.Selected,
					&got.Hover:       all.Hover,
					&got.Pressed:     all.Pressed,
					&got.Highlighted: all.FocusWindow,
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.images.Images(tt.g)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			if !assert.NotNil(t, got) {
				return
			}
			assert.Equal(t, outset, got.Outset)
			for img, uris := range tt.want(got) {
				for _, side := range []struct {
					got tui.Component
					uri string
				}{
					{img.Left, uris.Left},
					{img.Middle, uris.Middle},
					{img.Right, uris.Right},
				} {
					if side.uri == "" {
						assert.Nil(t, side.got)
						continue
					}
					assert.Same(t, g.Image(side.uri), side.got)
				}
			}
		})
	}
}
