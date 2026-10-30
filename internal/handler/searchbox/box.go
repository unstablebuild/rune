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

package searchbox

import (
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/text"
)

// Mode selects which inputs the search box displays.
type Mode uint8

const (
	// ModeFind displays the query input only.
	ModeFind Mode = iota
	// ModeReplace additionally displays the replacement input.
	ModeReplace
)

// WindowManager manages the floating window a Box is presented in. Hosts
// that draw Content themselves do not need one.
type WindowManager interface {
	Floating(browserapi.Floating, browserapi.FloatingConfig) (browserapi.Window, error)
	CloseWindow(browserapi.Window) error
}

// Controller searches the content a Box is attached to. Coordinates are
// expressed in the controller's own content space; the box only relays them.
//
// Selection reports the selection of the controller's own content. Hosts
// report the selection of whatever holds the focus, which is the box while
// it is open, so the box hands the question over whenever its own inputs
// hold no selection.
type Controller interface {
	BeginSearch(query []rune, origin term.Coordinates)
	SetSearchQuery(query string)
	AdvanceSearch()
	FinishSearch()
	SearchLast() string
	SearchOrigin() term.Coordinates
	Selection() (string, bool)
}

// ReplaceController is a Controller that can also rewrite matches. The
// replacement input and the find-to-replace affordance are only rendered
// for controllers that implement it.
type ReplaceController interface {
	Controller
	ReplaceNext(replacement string)
	ReplaceAll(replacement string)
}

// Config configures Box.
type Config struct {
	WindowManager   WindowManager
	Editor          text.Editor
	Title           string
	FindKey         term.KeyComb
	FindKeyAliases  []term.KeyComb
	ReplaceKey      term.KeyComb
	PaddingTop      int
	Attr            term.Attributes
	InputAttr       term.Attributes
	PlaceholderAttr term.Attributes
	FrameAttr       term.Attributes
	FocusFrameAttr  term.Attributes
	ButtonAttr      term.Attributes
	ButtonHoverAttr term.Attributes
	ButtonImages    func() *component.ButtonImages
	OnOpenError     func(Mode, error)
}

// Box is a floating find and replace prompt driven by a Controller.
type Box struct {
	controller Controller
	replace    ReplaceController
	config     Config
	floating   *floating
	window     browserapi.Window
}

// New allocates storage for a new Box and initializes it. If controller also satisfies
// ReplaceController then replace functionality is added to the search box.
func New(controller Controller, config Config) *Box {
	b := &Box{controller: controller, config: config}
	b.replace, _ = controller.(ReplaceController)
	return b
}

// Active reports whether the box is currently open.
func (b *Box) Active() bool { return b.floating != nil }

// Content returns the open box, for hosts that present it themselves. It
// returns nil while the box is closed.
func (b *Box) Content() browserapi.Floating {
	if b.floating == nil {
		return nil
	}
	return b.floating
}

// HandleKey opens, focuses or advances the search box and reports whether
// ev was consumed.
func (b *Box) HandleKey(ev term.Event) bool {
	if b.matchesFind(ev) {
		if b.floating == nil {
			b.open(ModeFind)
		} else {
			b.floating.setFocus(focusQuery)
			b.controller.AdvanceSearch()
			b.floating.ensureFocusVisible()
		}
		return true
	}
	if b.replace != nil && keyMatches(ev, b.config.ReplaceKey) {
		if b.floating == nil {
			b.open(ModeReplace)
		} else if b.floating.mode == ModeFind {
			b.floating.upgrade()
		} else {
			b.floating.setFocus(focusReplacement)
			b.floating.ensureFocusVisible()
		}
		return true
	}
	return false
}

// Finish tears down the search, optionally closing the floating window.
func (b *Box) Finish(closeWindow bool) error {
	if b.floating == nil {
		return nil
	}
	b.floating = nil
	win := b.window
	b.window = nil
	b.controller.FinishSearch()
	if closeWindow && win != nil {
		return b.config.WindowManager.CloseWindow(win)
	}
	return nil
}

// Close satisfies tui.Component.
func (b *Box) Close() error { return b.Finish(true) }

func (b *Box) matchesFind(ev term.Event) bool {
	if keyMatches(ev, b.config.FindKey) {
		return true
	}
	for _, alias := range b.config.FindKeyAliases {
		if keyMatches(ev, alias) {
			return true
		}
	}
	return false
}

func (b *Box) open(mode Mode) {
	origin := b.controller.SearchOrigin()
	query := b.controller.SearchLast()
	f := newFloating(b, mode, query)
	if b.config.WindowManager != nil {
		win, err := b.config.WindowManager.Floating(f, browserapi.FloatingConfig{
			Alignment: component.AlignmentTop | component.AlignmentHorizontallyCentered,
			Offset:    term.Coordinates{Y: 2},
			Title:     b.title(),
		})
		if err != nil {
			if b.config.OnOpenError != nil {
				b.config.OnOpenError(mode, err)
			}
			return
		}
		b.window = win
	}
	b.floating = f
	b.controller.BeginSearch([]rune(query), origin)
}

func (b *Box) title() string {
	if b.config.Title != "" {
		return b.config.Title
	}
	if b.replace != nil {
		return "Find / Replace"
	}
	return "Find"
}

func keyMatches(ev term.Event, key term.KeyComb) bool {
	return ev.Type == term.EventKey && ev.Mod == key.Mod && ev.Key == key.Key && ev.Ch == key.Ch
}
