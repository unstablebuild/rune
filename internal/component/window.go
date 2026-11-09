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
	"errors"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

var errCalledZeroValuedWin = "called method on zero-valued Window"

type windowNode interface {
	ID() uint64
	Width() int
	Height() int
	Content() tui.Component
	SetContentResize(tui.Component, bool) tui.Component
	Size() int
	Close()
	Closed() bool
	Position() term.Coordinates
}

// Window represents a tiled window in a WindowManager.
type Window struct {
	wm   *WindowManager
	node windowNode
}

// Position returns the position of this Window, or false
// if this Window is a zero-valued Window.
func (w Window) Position() term.Coordinates {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}

	if w.wm.minimizedDirty {
		w.wm.Resize(w.wm.width, w.wm.height)
	}

	fnode, ok := w.node.(*floatingNode)
	if ok && fnode.minimized != 0 {
		return fnode.Position()
	}

	pos := w.node.Position()
	pos.Y += w.wm.minimizedOffset.Y
	pos.X += w.wm.minimizedOffset.X
	return pos
}

// Width returns the width of this Window.
func (w Window) Width() int {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	return w.node.Width()
}

// Height returns the width of this Window.
func (w Window) Height() int {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	return w.node.Height()
}

// MaxWidth returns the max fixed width that this window can be set, based on the
// available space and siblings.
func (w Window) MaxWidth() int {
	_, ok := w.node.(*floatingNode)
	if ok {
		return w.wm.minimizedWidth
	}
	if w.wm.minimizedDirty {
		w.wm.Resize(w.wm.width, w.wm.height)
	}
	return w.node.(*TileNode).MaxWidth()
}

// MaxHeight returns the max fixed height that this window can be set, based on the
// available space and siblings.
func (w Window) MaxHeight() int {
	_, ok := w.node.(*floatingNode)
	if ok {
		return w.wm.minimizedHeight
	}
	if w.wm.minimizedDirty {
		w.wm.Resize(w.wm.width, w.wm.height)
	}
	return w.node.(*TileNode).MaxHeight()
}

// MinWidth returns the min fixed width that this window can be set.
func (w Window) MinWidth() int {
	n, ok := w.node.(*floatingNode)
	if ok {
		compWidth, _ := n.compDimensions()
		return compWidth
	}
	if w.wm.config.Frame {
		return 3
	}
	return 1
}

// MinHeight returns the min fixed height that this window can be set.
func (w Window) MinHeight() int {
	n, ok := w.node.(*floatingNode)
	if ok {
		_, compHeight := n.compDimensions()
		return compHeight
	}
	if w.wm.config.Frame {
		return 3
	}
	return 1
}

// Content returns the content of this Window, or false
// if this Window is a zero-valued Window.
func (w Window) Content() (c tui.Component) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}

	if w.wm.config.Frame {
		c = w.node.Content().(*component.Frame).Content()
	} else {
		c = w.node.Content()
	}

	return
}

// Frame returns this window's frame and true or nil and
// false if this window belongs to a window manager configured
// without frames.
func (w Window) Frame() (*component.Frame, bool) {
	frame, ok := w.node.Content().(*component.Frame)
	return frame, ok
}

// ContentSize returns the size the content of this Window was last
// resized to: the window less its frame, or zero before it was ever
// resized.
func (w Window) ContentSize() (width, height int) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if f, ok := w.Frame(); ok {
		return f.ContentSize()
	}
	return w.node.Width(), w.node.Height()
}

// SetContent sets the content of this Window to content and
// returns the previous content.
func (w Window) SetContent(content tui.Component) (
	prev tui.Component,
) {
	return w.SetContentResize(content, true)
}

// SetContentResize sets the content of this Window to content and
// returns the previous content, without resizing Content to the
// size and width of this window. This is useful for optimizing
// hot-swapping content between windows that are of the same size.
func (w Window) SetContentResize(content tui.Component, resize bool) (
	prev tui.Component,
) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}

	if w.wm.config.Frame && !resize {
		// frame needs a Resize but avoid resizing content
		// as per resize arg.
		frame := w.wm.withFrame(component.Nop())
		frame.Resize(w.Width(), w.Height())
		frame.SetContentResize(content, false)
		content = frame
	} else if w.wm.config.Frame {
		content = w.wm.withFrame(content)
	}
	if f, ok := content.(*component.Frame); ok && w.HasWindowBar() {
		f.FrameCharSet = w.wm.barCharSet(f.FrameCharSet)
	}
	prev = w.node.SetContentResize(content, resize)

	if w.wm.config.Frame {
		prev = prev.(*component.Frame).Content()
	}
	return
}

// SetFrameAttr sets a Window's FrameCharSet default attributes.
// Any Window's frame attributes can be reset by calling SetDefaultAttr
// which sets the default attributes for all windows.
func (w Window) SetFrameAttr(attr term.Attributes) (term.Attributes, bool) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if !w.wm.config.Frame {
		return term.Attributes{}, false
	}
	f := w.node.Content().(*component.Frame)
	ret := f.Attributes
	f.Attributes = attr
	return ret, true
}

// SetFrameCharSet sets a Window's Frame attributes. This can be reset by calling
// SetDefaultFrameCharSet which sets the default attributes for all windows.
func (w Window) SetFrameCharSet(b component.FrameCharSet) bool {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if !w.wm.config.Frame {
		return false
	}
	if w.HasWindowBar() {
		b = w.wm.barCharSet(b)
	}
	w.node.Content().(*component.Frame).FrameCharSet = b
	return true
}

// HasWindowBar returns true if this is a floating window rendered
// with the window bar over its top frame line.
func (w Window) HasWindowBar() bool {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	fn, ok := w.node.(*floatingNode)
	return ok && w.wm.hasWindowBar(fn)
}

// Title returns the title rendered on a floating window's bar, or an
// empty string for tiles and untitled windows.
func (w Window) Title() string {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if fn, ok := w.node.(*floatingNode); ok {
		return fn.title
	}
	return ""
}

// FrameAttr return this Window's default FrameCharSet attributes or false
// if this Window belongs to a WindowManager configured to not use borders.
func (w Window) FrameAttr() (attr term.Attributes, ok bool) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if !w.wm.config.Frame {
		return
	}
	ok = true
	attr = w.node.Content().(*component.Frame).Attributes
	return
}

// FrameCharSet return this Window's configured FrameCharSet or false
// if this Window belongs to a WindowManager configured to not use borders.
func (w Window) FrameCharSet() (b component.FrameCharSet, ok bool) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if !w.wm.config.Frame {
		return
	}
	ok = true
	b = w.node.Content().(*component.Frame).FrameCharSet
	return
}

// Size returns the total number of win under this Window.
func (w Window) Size() int {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	return w.node.Size()
}

// TileDown returns the window in the bottom of t or false if t is the
// bottom-most window in the WindowManager.
func (w Window) TileDown() (ret Window, ok bool) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if w.wm.minimizedDirty {
		w.wm.Resize(w.wm.width, w.wm.height)
	}
	// when floating is "pinned" to the top
	// check top most tile
	pos := w.Position()
	_, isFloating := w.node.(*floatingNode)
	if isFloating && !w.isMinimized() {
		topMost := w.wm.topMostTile()
		bottomMost := w.wm.bottomMostTile()
		if topMost.ID() != w.ID() && pos.Y == topMost.Position().Y &&
			pos.Y+w.Height() != bottomMost.Position().Y+bottomMost.Height() {
			return topMost, true
		}
	}
	pos.Y += w.Height()
	pos.X += w.Width() / 2
	ret, ok = w.wm.WindowAt(pos)
	if ok {
		return
	}
	// fallback to using the TileTree methods, so if component is of
	// an awkward size, we still return a tile
	if t, tok := w.node.(*TileNode); tok {
		node := t.TileDown()
		if node == nil {
			return
		}
		return w.wm.nodeToWindow(node), true
	}
	return
}

// TileLeft returns the window left-adjacent to t or false if t is the
// left-most window in the WindowManager.
func (w Window) TileLeft() (ret Window, ok bool) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if w.wm.minimizedDirty {
		w.wm.Resize(w.wm.width, w.wm.height)
	}
	// when floating is "pinned" to the right
	// check right most tile
	pos := w.Position()
	_, isFloating := w.node.(*floatingNode)
	if isFloating && !w.isMinimized() {
		rightMost := w.wm.rightMostTile()
		leftMost := w.wm.leftMostTile()
		if rightMost.ID() != w.ID() &&
			pos.X+w.Width() == rightMost.Position().X+rightMost.Width() &&
			pos.X != leftMost.Position().X {
			return rightMost, true
		}
	}
	pos.X--
	pos.Y += w.Height() / 2
	ret, ok = w.wm.WindowAt(pos)
	if ok {
		return
	}
	// fallback to using the TileTree methods, so if component is of
	// an awkward size, we still return a tile
	if t, tok := w.node.(*TileNode); tok {
		node := t.TileLeft()
		if node == nil {
			return
		}
		return w.wm.nodeToWindow(node), true
	}
	return
}

// TileRight returns the window right-adjacent to t or false if t is the
// right-most window in the tree.
func (w Window) TileRight() (ret Window, ok bool) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if w.wm.minimizedDirty {
		w.wm.Resize(w.wm.width, w.wm.height)
	}
	// when floating is "pinned" to the left
	// check left most tile
	pos := w.Position()
	_, isFloating := w.node.(*floatingNode)
	if isFloating && !w.isMinimized() {
		leftMost := w.wm.leftMostTile()
		rightMost := w.wm.rightMostTile()
		if leftMost.ID() != w.ID() && pos.X == leftMost.Position().X &&
			pos.X+w.Width() != rightMost.Position().X+rightMost.Width() {
			return leftMost, true
		}
	}
	pos.X += w.Width()
	pos.Y += w.Height() / 2
	ret, ok = w.wm.WindowAt(pos)
	if ok {
		return
	}
	// fallback to using the TileTree methods, so if component is of
	// an awkward size, we still return a tile
	if t, tok := w.node.(*TileNode); tok {
		node := t.TileRight()
		if node == nil {
			return
		}
		return w.wm.nodeToWindow(node), true
	}
	return
}

// TileUp returns the window on top of t or false if t is the
// top-most window in the tree.
func (w Window) TileUp() (ret Window, ok bool) {
	if w.wm == nil {
		panic(errCalledZeroValuedWin)
	}
	if w.wm.minimizedDirty {
		w.wm.Resize(w.wm.width, w.wm.height)
	}
	// when floating is "pinned" to the bottom
	// check bottom most tile
	pos := w.Position()
	_, isFloating := w.node.(*floatingNode)
	if isFloating && !w.isMinimized() {
		bottomMost := w.wm.bottomMostTile()
		topMost := w.wm.topMostTile()
		if bottomMost.ID() != w.ID() &&
			pos.Y+w.Height() == bottomMost.Position().Y+bottomMost.Height() &&
			pos.Y != topMost.Position().Y {
			return bottomMost, true
		}
	}
	pos.Y--
	pos.X += w.Width() / 2
	ret, ok = w.wm.WindowAt(pos)
	if ok {
		return
	}
	// fallback to using the TileTree methods, so if component is of
	// an awkward size, we still return a tile
	if t, tok := w.node.(*TileNode); tok {
		node := t.TileUp()
		if node == nil {
			return
		}
		return w.wm.nodeToWindow(node), true
	}
	return
}

// ID returns a unique identifier for this window.
func (w Window) ID() uint64 {
	return w.node.ID()
}

// IsFloating returns true if this is a floating window.
func (w Window) IsFloating() bool {
	_, ok := w.node.(*floatingNode)
	return ok
}

// IsMinimized returns true if this is a floating window and it's minimized.
func (w Window) IsMinimized() (component.Alignment, bool) {
	if !w.IsFloating() {
		return 0, false
	}
	alignment := w.node.(*floatingNode).minimized
	return alignment, alignment != 0
}

// MinimizeUp minimizes this window and displays it above the window manager,
// if this window is a floating window.
func (w Window) MinimizeUp(padding int) bool {
	n, ok := w.node.(*floatingNode)
	if !ok {
		return w.minimizeViaSize()
	}
	if n.minimized != 0 {
		return false
	}
	n.wm.minimizedDirty = true
	n.minimized = component.AlignmentTop
	n.minimizedPadding = padding
	return ok
}

// MinimizeDown minimizes this window and displays it below the window manager,
// if this window is a floating window.
func (w Window) MinimizeDown(padding int) bool {
	n, ok := w.node.(*floatingNode)
	if !ok {
		return w.minimizeViaSize()
	}
	if n.minimized != 0 {
		return false
	}
	n.wm.minimizedDirty = true
	n.minimized = component.AlignmentBottom
	n.minimizedPadding = padding
	return ok
}

// MinimizeLeft minimizes this window and displays it left of the window manager,
// if this window is a floating window.
func (w Window) MinimizeLeft(padding int) bool {
	n, ok := w.node.(*floatingNode)
	if !ok {
		return w.minimizeViaSize()
	}
	if n.minimized != 0 {
		return false
	}
	n.wm.minimizedDirty = true
	n.minimized = component.AlignmentLeft
	n.minimizedPadding = padding
	return ok
}

// MinimizeRight minimizes this window and displays it left of the window manager,
// if this window is a floating window.
func (w Window) MinimizeRight(padding int) bool {
	n, ok := w.node.(*floatingNode)
	if !ok {
		return w.minimizeViaSize()
	}
	if n.minimized != 0 {
		return false
	}
	n.wm.minimizedDirty = true
	n.minimized = component.AlignmentRight
	n.minimizedPadding = padding
	return ok
}

// Unminimize un-minimizes this window and displays it at the back at the front.
func (w Window) Unminimize() bool {
	n, ok := w.node.(*floatingNode)
	if !ok {
		return w.unminimizeViaSize()
	}
	if n.minimized == 0 {
		return false
	}
	n.wm.minimizedDirty = true
	n.minimized = 0
	n.minimizedPadding = 0
	return ok
}

// Close removes this Window from the WindowManager
// It returns an error if window is last window in the WindowManager.
func (w Window) Close() error {
	// zero-valued Window
	if w.wm == nil {
		return nil
	}

	if _, ok := w.node.(*TileNode); ok && w.wm.SizeTiles() == 1 {
		return errors.New("cannot close last node")
	}

	w.wm.minimizedDirty = true
	w.node.Close()
	return nil
}

// Closed returns if this Window has been closed.
func (w Window) Closed() bool {
	return w.wm == nil || w.node.Closed()
}

func (w Window) minimizeViaSize() (ok bool) {
	okh := w.wm.SetHeight(w, w.MinHeight())
	okw := w.wm.SetWidth(w, w.MinWidth())
	ok = okh || okw
	if ok {
		w.wm.minimizedDirty = true
	}
	return
}

func (w Window) unminimizeViaSize() (ok bool) {
	okh := w.wm.SetHeight(w, 0)
	okw := w.wm.SetWidth(w, 0)
	ok = okh || okw
	if ok {
		w.wm.minimizedDirty = true
	}
	return
}

func (w Window) isMinimized() bool {
	fn, ok := w.node.(*floatingNode)
	return ok && fn.minimized != 0
}
