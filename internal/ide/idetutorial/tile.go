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

package idetutorial

import (
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"

	thandler "unstable.build/rune/internal/handler"
)

// MinTileWidth is the narrowest tile that still fits a readable
// lesson, frame excluded.
const MinTileWidth = 24

// footerHeight is the room the footer's buttons take: their own row
// and a row of air that keeps them off the lesson above.
const footerHeight = 2

// The middle footer button has two faces: it skips the step the lesson
// is on, but steps forward through copy the user paged back to, where
// skipping would mean skipping a step they cannot see.
const (
	skipLabel = " Skip "
	nextLabel = " Next "
)

// TileWidth returns the width of the tutorial tile, frame excluded,
// for a screen total cells wide: a quarter of the width, but never
// narrower than MinTileWidth.
func TileWidth(total int) int {
	return max(total/4, MinTileWidth)
}

// TileStyle is how the tile is framed: the same frame, focus frame and
// scroll bar the IDE's windows use, so it reads as one of them.
type TileStyle struct {
	// Frame draws a frame around the tile. False when the IDE draws
	// no window frames either.
	Frame             bool
	FrameCharSet      component.FrameCharSet
	FocusFrameCharSet component.FrameCharSet
	FrameAttr         term.Attributes
	FocusFrameAttr    term.Attributes
	ScrollBarAttr     term.Attributes
	ScrollBarChar     rune
	Prompt            PromptStyle
}

// Tile is the pane that hosts a running tutorial beside the
// workspaces: the tutorial's own screen above a footer with the Back,
// Skip and Stop buttons, framed like a window. It lives outside every
// window manager, so it tracks its own focus and keeps its own right
// inset clear.
type Tile struct {
	tut    Tutorial
	body   tileBody
	frame  *handler.Frame
	outer  tui.Handler
	footer *handler.Prompt
	// footerCfg is kept so the middle button can be relabelled: the
	// prompt bakes its labels in at Init time.
	footerCfg handler.PromptConfig
	style     TileStyle

	focused       bool
	inset         int
	width, height int
}

// tileBody stacks the tutorial over the footer and scrolls with the
// tutorial, so the frame around it draws the tutorial's scroll bar.
type tileBody struct {
	*thandler.FrameUnion
	tut Tutorial
}

var _ handler.Scrollable = tileBody{}

func (b tileBody) SeekUp() bool       { return b.tut.SeekUp() }
func (b tileBody) SeekDown() bool     { return b.tut.SeekDown() }
func (b tileBody) SeekOffset() int    { return b.tut.SeekOffset() }
func (b tileBody) MaxSeekOffset() int { return b.tut.MaxSeekOffset() }

// NewTile returns a Tile hosting tut. onBack, onAdvance and onStop run
// when the user clicks the matching footer button.
func NewTile(tut Tutorial, style TileStyle, onBack, onAdvance, onStop func()) *Tile {
	t := &Tile{tut: tut, style: style}
	t.footerCfg = handler.PromptConfig{
		PromptConfig: component.PromptConfig{
			Message: "tutorial",
			Options: []string{" Back ", skipLabel, " Stop "},
			NewMessage: func(string) component.Floating {
				return component.NopFloating()
			},
			BackgroundAttributes: style.Prompt.BackgroundAttr,
		},
		PromptHandler: handler.FuncPromptHandler(func(idx int, _ string) {
			switch idx {
			case 0:
				onBack()
			case 1:
				onAdvance()
			default:
				onStop()
			}
		}, func() error { return nil }),
		OptionAttr:    style.Prompt.TextAttr,
		HighlightAttr: style.Prompt.HighlightAttr,
	}
	t.footer = handler.NewPrompt(t.footerCfg)
	t.flattenFooter()
	t.body = tileBody{FrameUnion: thandler.NewFrameUnion(tut), tut: tut}
	// Neither member has a frame of its own to stitch.
	t.body.Frame = false
	t.body.UnionBottom(t.footer, footerHeight)
	t.outer = t.body
	if style.Frame {
		t.frame = handler.NewFrame(t.body)
		t.frame.ScrollBarAttributes = style.ScrollBarAttr
		t.frame.ScrollBarChar = style.ScrollBarChar
		t.outer = t.frame
		t.SetFocused(false)
	}
	return t
}

// flattenFooter undoes the prompt's standing highlight of its first
// option: the footer's buttons are mouse-only, so no option is the
// default and none should look like one.
func (t *Tile) flattenFooter() {
	t.footer.SetOptionAttr(0, t.style.Prompt.TextAttr)
}

// SetFocused marks the tile as the pane holding focus, which the frame
// shows the way the window manager shows its focused window.
func (t *Tile) SetFocused(focused bool) {
	t.focused = focused
	if t.frame == nil {
		return
	}
	t.frame.FrameCharSet = t.style.FrameCharSet
	t.frame.Attributes = t.style.FrameAttr
	if focused {
		t.frame.FrameCharSet = t.style.FocusFrameCharSet
		t.frame.Attributes = t.style.FocusFrameAttr
	}
}

// Focused reports whether the tile holds focus.
func (t *Tile) Focused() bool { return t.focused }

// SetRightInset reserves cells along the tile's right edge for a UI
// element floating over it, which the tile keeps clear of its frame.
func (t *Tile) SetRightInset(cells int) {
	t.inset = max(cells, 0)
	t.Resize(t.width, t.height)
}

// Resize satisfies tui.Component.
func (t *Tile) Resize(width, height int) {
	t.width, t.height = width, height
	t.outer.Resize(max(width-t.inset, 0), height)
}

// Draw satisfies tui.Component.
func (t *Tile) Draw(w term.Writer) {
	t.syncFooter()
	t.outer.Draw(w)
}

// syncFooter keeps the middle button's label honest about what
// clicking it does.
func (t *Tile) syncFooter() {
	label := skipLabel
	if t.tut.ViewingPast() {
		label = nextLabel
	}
	if label == t.footerCfg.Options[1] {
		return
	}
	t.footerCfg.Options[1] = label
	t.footer.Init(t.footerCfg)
	t.flattenFooter()
	t.Resize(t.width, t.height)
}

// Handle routes mouse events to whatever they landed on, the footer's
// buttons included, and every other event to the tutorial. exit is
// always false: the tile is closed by the host, never by its content.
func (t *Tile) Handle(ev term.Event) (bool, bool) {
	if ev.Type == term.EventMouse {
		if ev.MouseX >= t.width-t.inset {
			return false, false
		}
		if ev.Key != term.MouseLeft {
			defer t.flattenFooter()
		}
	}
	_, handled := t.outer.Handle(ev)
	return false, handled
}

// Cursor satisfies tui.Handler.
func (t *Tile) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return t.outer.Cursor()
}

// Selection satisfies tui.Handler.
func (t *Tile) Selection() (string, bool) {
	return t.outer.Selection()
}
