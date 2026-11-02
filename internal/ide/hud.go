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

package ide

import (
	"image"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/component"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
)

// hudStyle is what a HUD reload re-applies to a running workspace.
type hudStyle struct {
	browser browser.Style
	// commandPromptFrame is nil when command.frame_charset is not set,
	// which draws the command prompt with the window frame charset.
	commandPromptFrame     *component.FrameCharSet
	commandPromptSeparator tcomponent.SeparatorCharSet
}

// imageURIs returns, sorted and once each, the URI of every image the
// frames, scroll bars, window bar, command prompt and prompt buttons of
// s name.
func (s hudStyle) imageURIs() []string {
	wm := s.browser.WindowManager
	var uris []string
	frame := func(cs component.FrameCharSet) {
		g := cs.Graphics
		uris = append(uris, g.TopLeft, g.TopRight, g.BottomLeft, g.BottomRight,
			g.HorizontalTop, g.HorizontalBottom, g.VerticalLeft, g.VerticalRight)
	}
	scrollBar := func(cs component.ScrollBarCharSet) {
		uris = append(uris, cs.Graphics.Top, cs.Graphics.Middle, cs.Graphics.Bottom)
	}
	frame(wm.FrameCharSet)
	frame(wm.FocusFrameCharSet)
	frame(s.browser.FrameUnionCharSet.FrameCharSet)
	if s.commandPromptFrame != nil {
		frame(*s.commandPromptFrame)
	}
	scrollBar(wm.ScrollBarCharSet)
	scrollBar(wm.ScrollBarHoverCharSet)
	bar, sep := wm.WindowBarCharSet.Graphics, s.commandPromptSeparator.Graphics
	uris = append(uris, bar.Left, bar.Horizontal, bar.Right, sep.Left, sep.Horizontal, sep.Right)
	images := s.browser.Prompt.Images
	for _, b := range []browser.PromptButtonImage{
		images.Idle, images.Selected, images.Hover, images.Pressed,
	} {
		uris = append(uris, b.Left, b.Middle, b.Right)
	}
	slices.Sort(uris)
	uris = slices.Compact(uris)
	return slices.DeleteFunc(uris, func(uri string) bool { return uri == "" })
}

// HUD holds what IDE.LoadHUD read from the config file for ApplyHUD to
// install: the keys of window frames, scroll bars, floating window bars,
// frame unions, the command prompt frame and separator, prompt buttons and
// the wallpaper, and the images they name.
type HUD struct {
	// load orders HUDs by when their load started.
	load uint64
	base hudStyle
	// workspaces holds, by URI, the style of the workspaces whose own
	// config overlays the user's.
	workspaces map[string]hudStyle
	// skip holds the workspaces whose overlay failed to decode, which
	// keep their style.
	skip       map[string]struct{}
	images     map[string]image.Image
	configErrs map[string]error
}
