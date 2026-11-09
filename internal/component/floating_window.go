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
	"unsafe"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

type floatingNode struct {
	desiredOffset               term.Coordinates    // desired offset
	alignment                   component.Alignment // desired alignment
	maxWidth, maxHeight         int                 // window space size
	desiredWidth, desiredHeight int                 // content desired Dimensions size
	userWidth, userHeight       int
	minimized                   component.Alignment
	minimizedPadding            int
	noBar                       bool
	title                       string
	moved                       bool
	maximized                   bool
	restoreAlignment            component.Alignment
	restoreOffset               term.Coordinates

	realWidth, realHeight int              // calculated upon Resize, considering trimming
	realOffset            term.Coordinates // calculated offset with alignment

	wm      *WindowManager
	content component.Virtual[component.Floating]
}

// reachableBarCells is how many cells of a moved window's bar stay on
// screen when it extends past an edge: one cell to grab it by, besides
// the corner and the close icon, which do not move the window.
const reachableBarCells = 3

func newFloatingNode(
	wm *WindowManager, content component.Floating,
	cfg FloatingConfig,
	maxWidth, maxHeight int,
) *floatingNode {
	ret := new(floatingNode)
	ret.desiredOffset = cfg.Offset
	ret.alignment = cfg.Alignment
	ret.noBar = cfg.NoBar
	ret.title = cfg.Title
	ret.maxWidth = maxWidth
	ret.maxHeight = maxHeight
	ret.wm = wm
	ret.SetContentResize(content, true)
	return ret
}

func (w *floatingNode) ID() uint64 {
	return uint64(uintptr(unsafe.Pointer(w)))
}

func (w *floatingNode) Width() int {
	if w.minimized == 0 {
		return w.realWidth
	}
	switch w.minimized {
	case component.AlignmentTop, component.AlignmentBottom:
		return w.wm.minimizedPos[w.ID()].length()
	case component.AlignmentLeft, component.AlignmentRight:
		return 1 + w.minimizedPadding
	default:
		panic("invalid minimize alignment")
	}
}

func (w *floatingNode) Height() int {
	if w.minimized == 0 {
		return w.realHeight
	}
	switch w.minimized {
	case component.AlignmentTop, component.AlignmentBottom:
		return 1 + w.minimizedPadding
	case component.AlignmentLeft, component.AlignmentRight:
		return w.wm.minimizedPos[w.ID()].length()
	default:
		panic("invalid minimize alignment")
	}
}

func (w *floatingNode) Content() tui.Component {
	if f, ok := w.content.C.(prevNodeFloating); ok {
		return f.Component
	}
	return w.content.C
}

func (w *floatingNode) Draw(wr term.Writer) {
	desiredWidth, desiredHeight := w.desiredWidth, w.desiredHeight
	w.updateDesiredDimensions()
	if desiredWidth != w.desiredWidth || desiredHeight != w.desiredHeight {
		w.resize()
	}
	// clear content
	for y := w.realOffset.Y; y < w.realOffset.Y+w.realHeight; y++ {
		for x := w.realOffset.X; x < w.realOffset.X+w.realWidth; x++ {
			wr.SetCell(term.Coordinates{Y: y, X: x}, term.Cell{})
		}
	}
	w.content.Draw(wr)
}

func (w *floatingNode) SetContentResize(c tui.Component, resize bool) (
	prev tui.Component,
) {
	var ok bool

	if w.wm.config.Frame {
		_, ok = c.(*component.Frame).Content().(component.Floating)
	} else {
		_, ok = c.(component.Floating)
	}

	if !ok {
		// Window.SetContent could pass a non Floating component
		// if that's the case, then set it to a static floating element which
		// uses the last Floating's desired dimensions
		c = prevNodeFloating{Component: c, width: w.desiredWidth, height: w.desiredHeight}
	}

	prev = w.content.C
	if f, ok := prev.(prevNodeFloating); ok {
		prev = f.Component // unwrap
	}
	w.content.C = c.(component.Floating)
	if resize {
		w.updateDesiredDimensions()
		w.resize()
	}
	return
}

func (w *floatingNode) Size() int {
	return 1
}

func (w *floatingNode) Close() {
	if w.wm == nil {
		return
	}

	wm := w.wm
	w.wm = nil
	wm.closeFloatingWindow(w)
}

func (w *floatingNode) layout() FloatingLayout {
	return FloatingLayout{
		WindowID:           w.ID(),
		Alignment:          w.alignment,
		Offset:             w.desiredOffset,
		MinimizedAlignment: w.minimized,
		MinimizedPadding:   w.minimizedPadding,
		NoBar:              w.noBar,
		Title:              w.title,
		Moved:              w.moved,
	}
}

func (w *floatingNode) Position() term.Coordinates {
	if w.minimized == 0 {
		return w.realOffset
	}
	return w.wm.minimizedPos[w.ID()].from()
}

func (w *floatingNode) compDimensions() (int, int) {
	return w.content.C.Dimensions()
}

func (w *floatingNode) setWidth(width int) bool {
	compWidth, _ := w.compDimensions()
	if w.minimized != 0 && width < compWidth && width != 0 { // width 0 resets
		return false
	}
	w.maximized = false
	w.userWidth = width
	return true
}

func (w *floatingNode) setHeight(height int) bool {
	_, compHeight := w.compDimensions()
	if w.minimized != 0 && height < compHeight && height != 0 { // height 0 resets
		return false
	}
	w.maximized = false
	w.userHeight = height
	return true
}

func (w *floatingNode) updateDesiredDimensions() {
	if w.maximized {
		w.desiredWidth, w.desiredHeight = w.maxWidth, w.maxHeight
		return
	}
	w.desiredWidth, w.desiredHeight = w.compDimensions()
	// a user-set size overrides the content's desired size in both
	// directions, so windows can also be resized below it
	if w.userHeight != 0 {
		w.desiredHeight = w.userHeight
	}
	if w.userWidth != 0 {
		w.desiredWidth = w.userWidth
	}
	if w.wm.config.NoMaxSize {
		w.desiredHeight = min(w.maxHeight-2, w.desiredHeight)
		w.desiredWidth = min(w.maxWidth-2, w.desiredWidth)
	}
}

func (w *floatingNode) resize() {
	if w.moved {
		w.layoutMoved()
	} else {
		w.layoutAligned()
	}
	if w.realOffset.Y >= w.maxHeight || w.realOffset.X >= w.maxWidth {
		return
	}
	w.content.Resize(w.realWidth, w.realHeight)
	w.content.Move(w.realOffset)
}

func (w *floatingNode) layoutAligned() {
	verticalDiff := max(0, w.maxHeight-w.desiredHeight)
	horizontalDiff := max(0, w.maxWidth-w.desiredWidth)

	var offset term.Coordinates
	if w.alignment&component.AlignmentVerticallyCentered != 0 {
		offset.Y = verticalDiff / 2
	} else if w.alignment&component.AlignmentBottom != 0 {
		offset.Y = verticalDiff
		offset.Y -= w.desiredOffset.Y
	} else {
		offset.Y += w.desiredOffset.Y
	}

	if w.alignment&component.AlignmentHorizontallyCentered != 0 {
		offset.X = horizontalDiff / 2
	} else if w.alignment&component.AlignmentRight != 0 {
		offset.X = horizontalDiff
		offset.X -= w.desiredOffset.X
	} else {
		offset.X += w.desiredOffset.X
	}

	w.realOffset.X = max(0, offset.X)
	w.realOffset.Y = max(0, offset.Y)

	w.realWidth = w.desiredWidth
	w.realHeight = w.desiredHeight
	if w.realOffset.Y+w.realHeight >= w.maxHeight {
		w.realHeight = max(0, w.maxHeight-w.realOffset.Y)
	}
	if w.realOffset.X+w.realWidth >= w.maxWidth {
		w.realWidth = max(0, w.maxWidth-w.realOffset.X)
	}
}

// layoutMoved keeps the window at its desired size, trimmed only to the
// manager's, so that it can always be brought back fully on screen.
func (w *floatingNode) layoutMoved() {
	w.realWidth, w.realHeight = w.movedSize()
	w.realOffset = w.reachableOffset(w.desiredOffset)
}

func (w *floatingNode) movedSize() (width, height int) {
	return max(0, min(w.desiredWidth, w.maxWidth)),
		max(0, min(w.desiredHeight, w.maxHeight))
}

// reachableOffset clamps pos so that the window's top row stays on
// screen with reachableBarCells of it showing, or all of it when the
// window is narrower than that.
func (w *floatingNode) reachableOffset(pos term.Coordinates) term.Coordinates {
	width, _ := w.movedSize()
	visible := min(reachableBarCells, width)
	return term.Coordinates{
		X: max(visible-width, min(pos.X, w.maxWidth-visible)),
		Y: max(0, min(pos.Y, w.maxHeight-1)),
	}
}

func (w *floatingNode) Resize(width, height int) {
	panic("called resize on a floating node")
}

func (w *floatingNode) SetMaxSize(width, height int) {
	w.maxWidth = width
	w.maxHeight = height
	w.updateDesiredDimensions()
	w.resize()
}

func (t *floatingNode) Closed() bool {
	return t.wm == nil
}

// Floating used to indicate that it should be unwrapped in calls to Content
// or as a return of SetContentResize
type prevNodeFloating struct {
	tui.Component
	width, height int
}

func (p prevNodeFloating) Dimensions() (width, height int) {
	return p.width, p.height
}
