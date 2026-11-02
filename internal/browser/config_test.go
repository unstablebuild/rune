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
	}

	want := c
	want.FrameAttr = blue
	want.WindowManagerConfig.FrameCharSet = plus
	want.ScrollBarAttr = blue
	want.ScrollBarCharSet = s.WindowManager.ScrollBarCharSet
	want.ScrollBarOutset = 2
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

	assert.Equal(t, want, c.Restyled(s))
}
