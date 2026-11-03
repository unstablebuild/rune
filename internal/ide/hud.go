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

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/component"
	"unstable.build/rune/internal/browser"
	tcomponent "unstable.build/rune/internal/component"
	"unstable.build/rune/internal/component/template"
	"unstable.build/rune/internal/ide/idestatus"
	"unstable.build/rune/internal/text"
)

// hudStyle is what a HUD reload re-applies to a running workspace.
type hudStyle struct {
	browser browser.Style
	// commandPromptFrame is nil when command.frame_charset is not set,
	// which draws the command prompt with the window frame charset.
	commandPromptFrame     *component.FrameCharSet
	commandPromptSeparator tcomponent.SeparatorCharSet
	commandShowManual      bool
	statusBar              text.StatusBarStyle
	bellSound              string
	// notifyStatus applies to every workspace, from the user's config
	// alone.
	notifyStatus idestatus.NotifyConfig
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

// statusBarImages returns the images of the editor status bar layout of
// s, in layout order.
func (s hudStyle) statusBarImages() []template.Image {
	var ret []template.Image
	for _, comp := range s.statusBar.Layout {
		if comp.Type == text.StatusBarImage {
			ret = append(ret, comp.Image)
		}
	}
	return ret
}

// HUD holds what IDE.LoadHUD read from the config file for ApplyHUD to
// install: the keys of window frames, scroll bars, floating window bars,
// frame unions, the command prompt frame, separator and manual, prompt
// buttons, the editor status bar, the wallpaper, the terminal bell and
// the program status notifications, and the images they name, and the
// config extensions fetch.
type HUD struct {
	// load orders HUDs by when their load started.
	load uint64
	base hudStyle
	// workspaces holds, by URI, the style of the workspaces whose own
	// config overlays the user's.
	workspaces map[string]hudStyle
	// skip holds the workspaces whose overlay failed to decode, which
	// keep their style.
	skip   map[string]struct{}
	images map[string]image.Image
	// statusBarImages holds, by URI, the images of the status bar
	// layouts, which are cached apart from images.
	statusBarImages map[string]image.Image
	configErrs      map[string]error
	// cfg is the user's config the HUD was read from.
	cfg map[string]any
	// workspaceCfgs holds, by URI, the config of the workspaces in
	// workspaces: the user's with their own overlaid.
	workspaceCfgs map[string]map[string]any
}

// Config returns the user's config h was read from, without the config
// of any workspace. Its errors are reported when h is applied.
func (h *HUD) Config() config.Config {
	return config.MapConfig(h.cfg)
}
