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

package handler

import (
	"errors"

	compapi "github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component"
)

var errCalledZeroValuedWin = "called method on zero-valued Window"

// Window represents a tiled window in a WindowManager.
type Window struct {
	component.Window
	wm *WindowManager
}

// Content returns the content of t.
func (w Window) Content() tui.Handler {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	c := w.Window.Content()
	if f, ok := c.(interface{ Content() tui.Handler }); ok {
		return f.Content()
	}
	return c.(tui.Handler)
}

// Frame returns this window's frame component and true or nil and
// false if this window belongs to a window manager configured
// without frames.
func (w Window) Frame() (*compapi.Frame, bool) {
	return w.Window.Frame()
}

// Focus returns true if window is in focus.
func (w Window) Focus() bool {
	return w.wm.focus == w
}

func (w Window) setContentResize(h tui.Handler, resize bool) (
	prev tui.Handler,
) {
	comp := w.Window.Content()
	if f, ok := comp.(interface{ Content() tui.Handler }); ok {
		prev = f.Content()
	} else {
		prev = comp.(tui.Handler)
	}
	w.Window.SetContentResize(h, resize)
	// SetContent creates a new frame if necessary
	// make sure that the frame created is set with
	// the focus attr if this Window is in focus
	if w.wm.focus.ID() == w.ID() {
		w.wm.setFocusAttr(w)
	}
	return prev
}

// SetContent sets the content of the window to h.
func (w Window) SetContent(h tui.Handler) (
	prev tui.Handler,
) {
	return w.setContentResize(h, true)
}

// Size returns the total number of win under this Window.
func (w Window) Size() (size int) {
	return w.Window.Size()
}

// TileDown returns the tile in the bottom of t or false if t is the
// bottom-most tile in the tree.
func (w Window) TileDown() (Window, bool) {
	win, ok := w.Window.TileDown()
	return w.wm.newNode(win), ok
}

// TileLeft returns the tile left-adjacent to t or false if t is the
// left-most tile in the tree.
func (w Window) TileLeft() (Window, bool) {
	win, ok := w.Window.TileLeft()
	return w.wm.newNode(win), ok
}

// TileRight returns the tile right-adjacent to t or false if t is the
// right-most tile in the tree.
func (w Window) TileRight() (Window, bool) {
	win, ok := w.Window.TileRight()
	return w.wm.newNode(win), ok
}

// TileUp returns the tile on top of t or false if t is the
// top-most tile in the tree.
func (w Window) TileUp() (Window, bool) {
	win, ok := w.Window.TileUp()
	return w.wm.newNode(win), ok
}

// Position returns this Window's position offset from the window
// manager's relative position.
func (w Window) Position() term.Coordinates {
	return w.Window.Position()
}

// Width returns the width of this window.
func (w Window) Width() int {
	return w.Window.Width()
}

// Height returns the width of this window.
func (w Window) Height() int {
	return w.Window.Height()
}

// MaxWidth returns the max fixed width that this window can be set, based on the
// available space and siblings.
func (w Window) MaxWidth() int {
	return w.Window.MaxWidth()
}

// MaxHeight returns the max fixed height that this window can be set, based on the
// available space and siblings.
func (w Window) MaxHeight() int {
	return w.Window.MaxHeight()
}

// MinWidth returns the min fixed width that this window can be set.
func (w Window) MinWidth() int {
	return w.Window.MinWidth()
}

// MinHeight returns the min fixed height that this window can be set.
func (w Window) MinHeight() int {
	return w.Window.MinHeight()
}

// Close removes this window from the tree.
// It returns an error if window is last window on the WindowManager.
func (w Window) Close() error {
	if w.wm == nil {
		return nil
	}

	if w.wm.SizeTiles() == 1 && !w.IsFloating() {
		return errors.New("cannot close last tiled window")
	}

	if w.wm.prevFocus == w {
		w.wm.prevFocus = Window{}
	}

	// first find a candidate to be the next
	// window in focus. If another floating window is open, prefer the frontmost
	// one so focus stays aligned with the visible z-order. Otherwise, prevFocus
	// takes priority, and then we find a candidate via wm.
	isFocus := w.wm.focus == w
	var tile Window
	var ok bool
	if isFocus {
		if floating, floatingOK := w.wm.findOtherFloatingWindow(w); floatingOK {
			ok = true
			tile = floating
		}
	}
	if isFocus && !ok && w.wm.prevFocus != (Window{}) {
		ok = true
		tile = w.wm.prevFocus
	}
	if isFocus && !ok {
		tile, ok = w.wm.Shiftable()
	}

	// then close the window, so parent's other
	// window's are resized, and properties are reflected
	// on dispatched OnFocus
	err := w.Window.Close()
	if err != nil {
		return err
	}
	if h := w.wm.config.WindowClose; h != nil {
		h.OnWindowClose(w)
	}

	if isFocus && ok {
		// finally change the focus, which triggers the OnFocus
		// this should always
		w.wm.SetFocus(w.wm.newNode(tile.Window))
		w.wm.prevFocus = Window{}
	} else if isFocus && !ok {
		// this could be a floating window and width/height might be 0 so Shiftable
		// might not yield the correct results. Prefer any remaining floating window,
		// otherwise just pick any window to focus to.
		focus, floatingOK := w.wm.findOtherFloatingWindow(w)
		if !floatingOK {
			w.wm.Iterate(func(candidate Window) {
				if focus != (Window{}) {
					return
				}
				focus = candidate
			})
		}
		if focus == (Window{}) {
			panic("cannot find window to focus to, but this is not last window")
		}
		w.wm.SetFocus(w.wm.newNode(focus.Window))
		w.wm.prevFocus = Window{}
	}

	// make sure that focus attrs are "reset" if wm size is 1
	w.wm.setFocusAttr(w.wm.Focus())

	return nil
}

// Closed returns if this Window has been closed.
func (w Window) Closed() bool {
	return w.wm == nil || w.Window.Closed()
}

// SetFrameAttr sets a Window's FrameCharSet default attributes.
// Any Window's frame attributes can be reset by calling SetDefaultAttr
// which sets the default attributes for all windows.
func (w Window) SetFrameAttr(attr term.Attributes) (term.Attributes, bool) {
	return w.Window.SetFrameAttr(attr)
}

func (wm *WindowManager) findOtherFloatingWindow(w Window) (Window, bool) {
	var focus Window
	wm.Iterate(func(candidate Window) {
		if !candidate.IsFloating() || candidate.ID() == w.ID() {
			return
		}
		if _, minimized := candidate.IsMinimized(); minimized {
			return
		}
		// Floating windows are iterated in draw order, so the last matching
		// candidate is the frontmost remaining floating window.
		focus = candidate
	})
	return focus, focus != (Window{})
}
