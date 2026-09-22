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
	"fmt"
	"time"

	compapi "github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component"
	tterm "unstable.build/rune/internal/term"
)

// WindowManagerConfig represents a WindowManager's
// configuration properties.
type WindowManagerConfig struct {
	component.WindowManagerConfig

	Dim                bool
	BW                 bool
	FocusFrameAttr     term.Attributes
	FocusFrameCharSet  compapi.FrameCharSet
	ScrollBarHoverChar rune
	// FloatingBar receives the interactions produced by floating
	// window bars. It may be nil.
	FloatingBar FloatingBarHandler
}

// FloatingBarHandler groups the interactions produced by a floating
// window's bar. Positions are relative to the window manager's
// top-left corner.
type FloatingBarHandler interface {
	// OnBarClose reports whether the close-icon click was handled;
	// when false the window is closed via Window.Close.
	OnBarClose(win Window) bool
	// OnBarDrag is invoked on every mouse move of a bar move drag,
	// after the window has been repositioned.
	OnBarDrag(win Window, pos term.Coordinates)
	// OnBarDrop reports whether the release was consumed; when true
	// win may already have been closed by the handler.
	OnBarDrop(win Window, pos term.Coordinates) bool
	// OnBarDragCancel is invoked when a bar move drag ends without a
	// drop, including when win is closed mid-drag.
	OnBarDragCancel(win Window)
}

// NopFloatingBarHandler implements FloatingBarHandler with no-ops so
// partial implementors can embed it.
type NopFloatingBarHandler struct{}

// OnBarClose satisfies FloatingBarHandler.
func (NopFloatingBarHandler) OnBarClose(Window) bool { return false }

// OnBarDrag satisfies FloatingBarHandler.
func (NopFloatingBarHandler) OnBarDrag(Window, term.Coordinates) {}

// OnBarDrop satisfies FloatingBarHandler.
func (NopFloatingBarHandler) OnBarDrop(Window, term.Coordinates) bool { return false }

// OnBarDragCancel satisfies FloatingBarHandler.
func (NopFloatingBarHandler) OnBarDragCancel(Window) {}

// winDragMode is a bitmask describing an in-progress window drag
// started from a floating window's bar or a window's frame edge.
type winDragMode uint8

const (
	winDragMove winDragMode = 1 << iota
	winDragLeft
	winDragRight
	winDragBottom
	winDragTop
)

// windowBarDoubleClickTimeout is the maximum delay between two bar
// presses for them to count as a maximize/restore double click.
const windowBarDoubleClickTimeout = 500 * time.Millisecond

// WindowManager implements Handler as a tiled window manager.
type WindowManager struct {
	comp   component.WindowManager
	config WindowManagerConfig
	// best effort to set focus to prev win upon ShiftFocus
	prevFocus                Window
	focus                    Window
	subs                     []WindowSubscriber
	prevMouseScrollBarDrag   bool
	prevMouseLeftChild       component.Window
	prevMouseScrollBarOffset int
	prevMouseLeftDrag        bool
	winDrag                  winDragMode
	winDragWin               component.Window
	winDragGrab              term.Coordinates
	winDragMoved             bool
	winBarPressID            uint64
	winBarPressTime          time.Time
	// defAttr holds the live theme default attributes so the grayscale
	// dim writer can resolve a ColorDefault foreground before graying it.
	defAttr term.Attributes
}

// WindowSubscriber wraps the OnFocus callback used
// to subscribe to window focus. Upon calling Subscribe
// the first OnFocus is dispatched, but prevFocus will
// a zero Window, and so it should not be used.
type WindowSubscriber interface {
	OnFocus(prevFocus, newFocus Window)
}

// NewWindowManager allocates storage for a new WindowManager and initializes it with the
// given handler. If border is true, it will draw a border around every window.
func NewWindowManager(
	handler tui.Handler, cfg WindowManagerConfig,
) (wm *WindowManager) {
	wm = new(WindowManager)
	wm.Init(handler, cfg)
	return
}

func (wm *WindowManager) newNode(t component.Window) Window {
	return Window{wm: wm, Window: t}
}

// WindowAt returns the window rendered at pos, which is relative to the
// window manager's top-left corner, or false when pos falls outside
// every window.
func (wm *WindowManager) WindowAt(pos term.Coordinates) (Window, bool) {
	win, ok := wm.comp.WindowAt(pos)
	if !ok {
		return Window{}, false
	}
	return wm.newNode(win), true
}

// TileAt returns the tiled window rendered at pos, which is relative to
// the window manager's top-left corner, ignoring floating and minimized
// windows drawn over the tiled layout.
func (wm *WindowManager) TileAt(pos term.Coordinates) (Window, bool) {
	win, ok := wm.comp.TileAt(pos)
	if !ok {
		return Window{}, false
	}
	return wm.newNode(win), true
}

// Init initializes this WindowManager with the given handler. If border is true, it will draw
// a border around every tile.
func (wm *WindowManager) Init(handler tui.Handler, cfg WindowManagerConfig) {
	win := wm.comp.Init(handler, cfg.WindowManagerConfig)
	wm.config = cfg
	wm.focus = wm.newNode(win)
	wm.SetFocus(wm.focus)
}

// SetFrameCharSet sets the frame border cells used to draw borders around tiles.
// Note that this has no effect if WindowManager was
// initialized with border == false.
func (wm *WindowManager) SetFrameCharSet(def, focus compapi.FrameCharSet) {
	if !wm.config.Frame {
		return
	}
	wm.config.FocusFrameCharSet = focus
	wm.config.FrameCharSet = def
	wm.comp.SetFrameCharSet(def)
	wm.setFocusAttr(wm.focus)
}

func (wm *WindowManager) setFocusAttr(win Window) {
	// only set focus attr+charset if there's more than one window
	if wm.SizeTiles()+wm.SizeFloating() > 1 {
		win.SetFrameAttr(wm.config.FocusFrameAttr)
		win.SetFrameCharSet(wm.config.FocusFrameCharSet)
	} else {
		win.SetFrameAttr(wm.config.FrameAttr)
		win.SetFrameCharSet(wm.config.FrameCharSet)
	}
}

// SetAttr sets the default and focus window border attributes. Note that
// this has no effect if WindowManager was initialized with border == false.
func (wm *WindowManager) SetAttr(def, focus term.Attributes) {
	if !wm.config.Frame {
		return
	}
	wm.config.FocusFrameAttr = focus
	wm.config.FrameAttr = def
	wm.comp.SetFrameAttr(def)
	wm.setFocusAttr(wm.focus)
}

// Handle satisfies tui.Handler.
func (wm *WindowManager) Handle(ev term.Event) (exit bool, handled bool) {
	// Cleared after dispatch so MouseRelease ending a drag still
	// reaches the press window instead of being re-routed by position.
	var endDragAfter bool
	if ev.Type == term.EventMouse {
		mousePos := term.Coordinates{X: ev.MouseX, Y: ev.MouseY}
		// The cancel hook may close windows, so nothing may be
		// resolved from the layout before it runs.
		if wm.winDrag != 0 && wm.winDragWin.Closed() {
			wm.cancelWindowDrag()
		}
		childAtMouse, ok := wm.comp.WindowAt(mousePos)
		// A pinned drag target can be closed mid-drag (e.g. by a
		// runner or by the inner handler), leaving prevMouseLeftChild
		// referencing a removed window whose Position would deref a
		// nil tile tree. Drop the stale pin and route by position.
		if (wm.prevMouseScrollBarDrag || wm.prevMouseLeftDrag) &&
			wm.prevMouseLeftChild.Closed() {
			wm.resetScrollBarMouse()
			wm.prevMouseLeftDrag = false
			wm.prevMouseLeftChild = component.Window{}
		}
		if wm.winDrag != 0 {
			return wm.handleWindowDrag(mousePos, ev)
		}
		if wm.prevMouseScrollBarDrag {
			childAtMouse = wm.prevMouseLeftChild
			ok = true
		}
		// Pin MouseLeft/Release to the press window for the duration
		// of a drag so the inner mouse.Mouse sees a matched
		// press/release pair instead of a fresh press in a sibling.
		if wm.prevMouseLeftDrag &&
			(ev.Key == term.MouseLeft || ev.Key == term.MouseRelease) {
			childAtMouse = wm.prevMouseLeftChild
			ok = true
			if ev.Key == term.MouseRelease {
				endDragAfter = true
			}
		}
		if !ok {
			return
		}
		offset := childAtMouse.Position()
		ev.MouseX -= offset.X
		ev.MouseY -= offset.Y

		// reset scroll bars
		wm.comp.Iterate(func(win component.Window) {
			f, ok := win.Frame()
			if ok {
				f.ScrollBarChar = wm.config.ScrollBarChar
			}
		})

		// set scroll bar hover char, if applicable
		if frame, ok := childAtMouse.Frame(); ok {
			position, height, ok := frame.ScrollBar()
			if ok {
				end := position.Y + height
				if ev.MouseX == position.X && ev.MouseY >= position.Y && ev.MouseY < end ||
					wm.prevMouseScrollBarDrag {
					return wm.handleScrollBarMouse(childAtMouse, position.Y, height, frame, ev)
				}
				// lost drag; reset if mouse is now outside of scroll bar
				wm.resetScrollBarMouse()
			} else {
				// lost scroll bar; content could have changed
				wm.resetScrollBarMouse()
			}
		}

		if ev.Key == term.MouseLeft && !wm.prevMouseLeftDrag && wm.config.Frame {
			if exit, handled, done := wm.handleWindowFramePress(childAtMouse, ev); done {
				return exit, handled
			}
		}

		if wm.config.Frame {
			ev.MouseY--
			ev.MouseX--
		}

		// Upper-bound clamp is required for drag capture: events
		// re-routed to the press window must land inside its content.
		maxX, maxY := contentBounds(childAtMouse, wm.config.Frame)
		if ev.MouseX < 0 {
			ev.MouseX = 0
		} else if ev.MouseX > maxX {
			ev.MouseX = maxX
		}
		if ev.MouseY < 0 {
			ev.MouseY = 0
		} else if ev.MouseY > maxY {
			ev.MouseY = maxY
		}

		if wm.Focus().Window != childAtMouse {
			if ev.Key == term.MouseLeft {
				wm.SetFocus(wm.newNode(childAtMouse))
				// Fall through: the inner mouse.Mouse must see the
				// press to anchor a selection at the pressed cell.
			} else if ev.Key == term.MouseRelease && endDragAfter {
				// Captured-drag release: dispatch without changing focus.
			} else {
				return
			}
		}

		if ev.Key == term.MouseLeft {
			wm.prevMouseLeftDrag = true
			wm.prevMouseLeftChild = childAtMouse
		}
	}

	var hexit bool
	focus := wm.focus
	size := wm.comp.SizeTiles()
	hexit, handled = focus.Content().Handle(ev)
	if endDragAfter {
		wm.prevMouseLeftDrag = false
	}

	// if handler in focus wants to exit, close the window,
	// or signal exit to upstream handler if it was last window
	if hexit {
		if focus.IsFloating() {
			focus.Close()
			return
		}

		if exit = size == 1; exit {
			return
		}

		// Close the original tile that produced the exit signal. There
		// are two cases the conditions below must distinguish:
		//   1) Handle did not change the layout: wm.focus still equals
		//      focus. Shift focus to a sibling and close the tile.
		//   2) Handle opened a floating window (e.g. a recovery prompt
		//      via Component.Prompt) that took over wm.focus mid-Handle.
		//      In that case the original tile must still be closed so
		//      the user is not left with a stranded non-tab split (see
		//      RUNE-139). Do not call ShiftFocus because the floating
		//      prompt is the intended new focus.
		//   3) Handle itself swapped focus to another tile (e.g. opened
		//      a new tile and shifted focus). Skip closing.
		size := wm.comp.SizeTiles()
		if focus.Closed() || size == 1 {
			return
		}
		curr := wm.focus
		switch {
		case curr.ID() == focus.ID():
			wm.ShiftFocus()
			focus.Close()
		case curr.IsFloating():
			focus.Close()
		}
	}

	return
}

// SplitVertical creates a new vertical split over the tile currently in focus.
func (wm *WindowManager) SplitVertical(win Window, h tui.Handler) (Window, bool) {
	w, ok := wm.comp.SplitVertical(win.Window, h)
	if !ok {
		return Window{}, false
	}
	ret := wm.newNode(w)
	wm.setFocusAttr(wm.focus)
	return ret, true
}

// SplitHorizontal creates a new horizontal split over the tile currently in focus.
func (wm *WindowManager) SplitHorizontal(win Window, h tui.Handler) (Window, bool) {
	w, ok := wm.comp.SplitHorizontal(win.Window, h)
	if !ok {
		return Window{}, false
	}
	ret := wm.newNode(w)
	wm.setFocusAttr(wm.focus)
	return ret, true
}

// SplitRoot creates a new top-level split in the root layout.
func (wm *WindowManager) SplitRoot(alignment compapi.Alignment, h tui.Handler) (Window, bool) {
	w, ok := wm.comp.SplitRoot(alignment, h)
	if !ok {
		return Window{}, false
	}
	ret := wm.newNode(w)
	wm.setFocusAttr(wm.focus)
	return ret, true
}

// FloatingWindow creates a floating window.
func (wm *WindowManager) FloatingWindow(
	content handler.Floating, cfg component.FloatingConfig,
) Window {
	ret := wm.newNode(wm.comp.FloatingWindow(content, cfg))
	wm.setFocusAttr(wm.focus)
	return ret
}

// SwapContentLeft swaps the content of the tile on the left side of the tile in focus.
// If the tile in focus is the left-most tile in this window manager, then this method does nothing.
func (wm *WindowManager) SwapContentLeft() bool {
	return wm.swapContent((Window).TileLeft)
}

// SwapContentRight switches the focus to the tile on the right side of the tile in focus.
// If the tile in focus is the right-most tile in this window manager, then this method does nothing.
func (wm *WindowManager) SwapContentRight() bool {
	return wm.swapContent((Window).TileRight)
}

// SwapContentUp switches the focus to the tile above the tile in focus.
// If the tile in focus is the up-most tile in this window manager, then this method does nothing.
func (wm *WindowManager) SwapContentUp() bool {
	return wm.swapContent((Window).TileUp)
}

// SwapContentDown switches the focus to the tile beneath the tile in focus.
// If the tile in focus is the down-most tile in this window manager, then this method does nothing.
func (wm *WindowManager) SwapContentDown() bool {
	return wm.swapContent((Window).TileDown)
}

// FocusLeft switches the focus to the tile on the left side of the tile in focus.
// If the tile in focus is the left-most tile in this window manager, then this method does nothing.
func (wm *WindowManager) FocusLeft() bool {
	return wm.switchFocus((Window).TileLeft)
}

// FocusRight switches the focus to the tile on the right side of the tile in focus.
// If the tile in focus is the right-most tile in this window manager, then this method does nothing.
func (wm *WindowManager) FocusRight() bool {
	return wm.switchFocus((Window).TileRight)
}

// FocusUp switches the focus to the tile above the tile in focus.
// If the tile in focus is the up-most tile in this window manager, then this method does nothing.
func (wm *WindowManager) FocusUp() bool {
	return wm.switchFocus((Window).TileUp)
}

// FocusDown switches the focus to the tile beneath the tile in focus.
// If the tile in focus is the down-most tile in this window manager, then this method does nothing.
func (wm *WindowManager) FocusDown() bool {
	return wm.switchFocus((Window).TileDown)
}

// Focus returns the tile currently in focus.
func (wm *WindowManager) Focus() Window {
	return wm.focus
}

// ShiftFocus attempts to shift to focus to another tile. It returns
// false if focus did not shift to another tile because there aren't any tiles left.
func (wm *WindowManager) ShiftFocus() (ok bool) {
	if wm.prevFocus != (Window{}) {
		ok = wm.Focus() != wm.prevFocus
		if ok {
			wm.SetFocus(wm.prevFocus)
			return
		}
	}

	ok = wm.FocusLeft()
	if ok {
		return
	}
	ok = wm.FocusUp()
	if ok {
		return
	}
	ok = wm.FocusRight()
	if ok {
		return
	}
	ok = wm.FocusDown()
	return
}

// Shiftable returns whether next call to ShiftFocus would return true.
func (wm *WindowManager) Shiftable() (w Window, ok bool) {
	focus := wm.Focus()
	w, ok = focus.TileLeft()
	if ok {
		return
	}
	w, ok = focus.TileUp()
	if ok {
		return
	}
	w, ok = focus.TileRight()
	if ok {
		return
	}
	w, ok = focus.TileDown()
	return
}

// Cursor returns the cursor coordinates of the tile in focus.
func (wm *WindowManager) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	window := wm.focus.Window
	offset := window.Position()
	content := wm.focus.Content()
	bounds := term.Coordinates{X: window.Width(), Y: window.Height()}
	if wm.config.Frame {
		offset.Y++
		offset.X++
		bounds.X -= 2
		bounds.Y -= 2
	}
	cursor, style, show := content.Cursor()
	if show {
		show = term.CoordinatesInBounds(cursor, bounds)
	}
	return term.CoordinatesSum(cursor, offset), style, show
}

// Selection satisfies tui.Handler.
func (wm *WindowManager) Selection() (string, bool) {
	content := wm.focus.Content()
	return content.Selection()
}

// Draw : tui.Component
func (wm *WindowManager) Draw(w term.Writer) {
	if wm.comp.SizeTiles()+wm.comp.SizeFloating() == 1 || !wm.config.Dim {
		wm.comp.Draw(w)
		return
	}

	focusWin := wm.focus.Window
	dimWriter := wm.dimmedWriter(w)
	wm.comp.Iterate(func(win component.Window) {
		if win == focusWin {
			wm.comp.DrawWindow(win, w)
		} else {
			wm.comp.DrawWindow(win, dimWriter)
		}
	})
}

// dimmedWriter returns the writer used to de-emphasize unfocused
// windows: a grayscale writer when BW is set, otherwise a brightness
// dimming writer.
func (wm *WindowManager) dimmedWriter(w term.Writer) term.Writer {
	if wm.config.BW {
		return tterm.BWWriter(w, wm.defAttr)
	}
	return tterm.DimWriter(w)
}

// DrawWindow draws target with the given term.Writer.
func (wm *WindowManager) DrawWindow(target Window, w term.Writer) {
	wm.comp.Iterate(func(win component.Window) {
		if win == target.Window {
			wm.comp.DrawWindow(win, w)
		}
	})
}

// SetDim sets whether next call to draw should use
// non-focus window diming feature.
func (wm *WindowManager) SetDim(to bool) (prev bool) {
	prev = wm.config.Dim
	wm.config.Dim = to
	return
}

// SetDefaultAttr stores the live theme default attributes used to resolve
// a ColorDefault foreground before grayscaling unfocused windows.
func (wm *WindowManager) SetDefaultAttr(attr term.Attributes) {
	wm.defAttr = attr
}

// SetHeight fixes the height of the given window and returns true if possible,
// or returns false if not.
//
// Calling this method with height=0 effectively reverts back to the height
// being automatically distributed between windows.
func (wm *WindowManager) SetHeight(win Window, height int) bool {
	return wm.comp.SetHeight(win.Window, height)
}

// SetWidth fixes the width of the given window and returns true if possible,
// or returns false if not.
//
// Calling this method with width=0 effectively reverts back to the width
// being automatically distributed between windows.
func (wm *WindowManager) SetWidth(win Window, width int) bool {
	return wm.comp.SetWidth(win.Window, width)
}

// Resize : tui.Component
func (wm *WindowManager) Resize(width, height int) {
	wm.comp.Resize(width, height)
}

// SetFocus sets the passed tile in focus. It returns the previous tile in focus.
// The behaviour is undefined if the given tile is not part of this WindowManager.
func (wm *WindowManager) SetFocus(tile Window) (
	prev Window,
) {
	if tile.wm != wm {
		panic(fmt.Sprintf("Tile does not belong to"+
			"this window manager: %p vs %p", wm, tile.wm))
	}
	if wm.config.Frame {
		wm.focus.Window.SetFrameAttr(wm.config.FrameAttr)
		wm.focus.Window.SetFrameCharSet(wm.config.FrameCharSet)
		wm.setFocusAttr(tile)
	}
	prev = wm.focus
	wm.focus = tile
	wm.comp.ForegroundFloating(tile.Window)
	if prev != tile {
		wm.dispatchOnFocus(prev, wm.focus)
		wm.prevFocus = prev
	}
	return
}

// DefaultWindowManagerConfig returns a sane WindowManagerConfig ready to use.
func DefaultWindowManagerConfig() WindowManagerConfig {
	return WindowManagerConfig{
		Dim:                 true,
		WindowManagerConfig: component.DefaultWindowManagerConfig(),
		FocusFrameCharSet:   compapi.FrameCharSetDefault(),
		FocusFrameAttr: term.Attributes{
			Fg: term.ColorRed,
			Bg: term.ColorDefault,
		},
	}
}

// SizeTiles returns the number of tiled windows in this WindowManager.
func (wm *WindowManager) SizeTiles() int {
	return wm.comp.SizeTiles()
}

// SizeFloating returns the number of floating windows in this WindowManager.
func (wm *WindowManager) SizeFloating() int {
	return wm.comp.SizeFloating()
}

// TileLayout returns the current tiled window tree layout.
func (wm *WindowManager) TileLayout() component.TileLayout {
	return wm.comp.TileLayout()
}

// RestoreTileLayout replaces the current tiled layout and returns a map from
// old layout window IDs to newly allocated windows.
func (wm *WindowManager) RestoreTileLayout(
	layout component.TileLayout,
	content func(windowID uint64) tui.Handler,
) map[uint64]Window {
	components := wm.comp.RestoreTileLayout(layout, func(windowID uint64) tui.Component {
		return content(windowID)
	})
	wm.prevMouseScrollBarDrag = false
	wm.prevMouseLeftChild = component.Window{}
	wm.resetWindowDrag()
	ret := make(map[uint64]Window, len(components))
	for id, win := range components {
		ret[id] = wm.newNode(win)
	}
	// Always (re)set focus to a live tile in the new tree. The
	// pre-restore wm.focus points at a node that was just discarded;
	// if we left it in place, downstream callers (e.g.
	// browser.Component.focus) would look up a Window ID missing
	// from their bookkeeping and panic. Iterate visits tiles in
	// tree order, giving a deterministic fallback.
	var focus Window
	wm.Iterate(func(candidate Window) {
		if focus == (Window{}) && !candidate.IsFloating() {
			focus = candidate
		}
	})
	if focus != (Window{}) {
		wm.SetFocus(focus)
	}
	// SetFocus stored the pre-restore focus into prevFocus, but that
	// window is no longer part of the tree. Clear it so downstream
	// focus transitions (Window.Close falling back to prevFocus) do
	// not select a stale node.
	wm.prevFocus = Window{}
	return ret
}

// Iterate applies op to the content of all widnows of this WindowManager.
func (wm *WindowManager) Iterate(fn func(Window)) {
	wm.comp.Iterate(func(c component.Window) {
		fn(wm.newNode(c))
	})
}

func (wm *WindowManager) dispatchOnFocus(prev, focus Window) {
	for _, sub := range wm.subs {
		sub.OnFocus(prev, focus)
	}
}

// Subscribe subscribes sub to window focus events.
func (wm *WindowManager) Subscribe(sub WindowSubscriber) {
	wm.subs = append(wm.subs, sub)
	sub.OnFocus(Window{}, wm.focus)
}

// UnsubscribeAll unsubscribes all WindowSubscriber.
func (wm *WindowManager) UnsubscribeAll() {
	wm.subs = nil
}

func (wm *WindowManager) resetScrollBarMouse() {
	wm.prevMouseScrollBarDrag = false
}

func (wm *WindowManager) resetWindowDrag() {
	wm.winDrag = 0
	wm.winDragWin = component.Window{}
	wm.winDragMoved = false
}

// cancelWindowDrag ends an in-progress drag without a drop, notifying
// the floating bar handler when the drag was a bar move.
func (wm *WindowManager) cancelWindowDrag() {
	if wm.winDrag == winDragMove && wm.config.FloatingBar != nil {
		wm.config.FloatingBar.OnBarDragCancel(wm.newNode(wm.winDragWin))
	}
	wm.resetWindowDrag()
}

// handleWindowDrag routes mouse events while a window move/resize drag
// is in progress. mouse is in root coordinates.
func (wm *WindowManager) handleWindowDrag(
	mouse term.Coordinates, ev term.Event,
) (exit, handled bool) {
	barMove := wm.winDrag == winDragMove && wm.config.FloatingBar != nil
	switch ev.Key {
	case term.MouseLeft:
		win := wm.newNode(wm.winDragWin)
		before := wm.winDragWin.Position()
		wm.applyWindowDrag(mouse)
		if wm.winDragWin.Position() != before {
			wm.winDragMoved = true
		}
		if barMove {
			wm.config.FloatingBar.OnBarDrag(win, mouse)
		}
		return false, true
	case term.MouseRelease:
		// A press and release with no motion in between is a click,
		// not a drop: docking the float there would silently turn it
		// into a tab.
		if barMove && wm.winDragMoved {
			wm.config.FloatingBar.OnBarDrop(wm.newNode(wm.winDragWin), mouse)
			wm.resetWindowDrag()
			return false, true
		}
		wm.cancelWindowDrag()
		return false, true
	default:
		wm.cancelWindowDrag()
		return false, false
	}
}

// windows smaller than this cannot fit a frame plus content.
const minWindowDragSize = 3

func (wm *WindowManager) applyWindowDrag(mouse term.Coordinates) {
	win := wm.winDragWin
	if wm.winDrag&winDragMove != 0 {
		wm.comp.MoveWindow(win, term.Coordinates{
			X: mouse.X - wm.winDragGrab.X,
			Y: mouse.Y - wm.winDragGrab.Y,
		})
		return
	}
	pos := win.Position()
	if wm.winDrag&winDragRight != 0 {
		if width := mouse.X - pos.X + 1; width >= minWindowDragSize {
			_ = wm.comp.SetWidth(win, width)
		}
	}
	if wm.winDrag&winDragBottom != 0 {
		if height := mouse.Y - pos.Y + 1; height >= minWindowDragSize {
			_ = wm.comp.SetHeight(win, height)
		}
	}
	newX := pos.X
	if wm.winDrag&winDragLeft != 0 {
		right := pos.X + win.Width()
		if width := right - mouse.X; width >= minWindowDragSize &&
			wm.comp.SetWidth(win, width) {
			// relayout so Width reflects the effective width (floats
			// grow to their content's desired size), then pin the
			// right edge in place.
			wm.comp.MoveWindow(win, pos)
			newX = right - win.Width()
		}
	}
	newY := pos.Y
	if wm.winDrag&winDragTop != 0 {
		bottom := pos.Y + win.Height()
		if height := bottom - mouse.Y; height >= minWindowDragSize &&
			wm.comp.SetHeight(win, height) {
			// same as the left edge: relayout, then pin the bottom
			// edge in place.
			wm.comp.MoveWindow(win, pos)
			newY = bottom - win.Height()
		}
	}
	// Pin the floating window's top-left corner so resizes keep the
	// window in place regardless of its alignment; this also forces a
	// relayout so Width/Height reflect the new size immediately.
	// MoveWindow is a no-op for tiles, which relayout on draw.
	wm.comp.MoveWindow(win, term.Coordinates{X: newX, Y: newY})
}

// handleWindowFramePress detects presses on a floating window's bar
// (close icon or start of a move drag) and on frame edges (start of a
// resize drag). Event coordinates are window-local. done reports
// whether the press was consumed.
func (wm *WindowManager) handleWindowFramePress(
	win component.Window, ev term.Event,
) (exit, handled, done bool) {
	if _, minimized := win.IsMinimized(); minimized {
		return
	}
	wx, wy := ev.MouseX, ev.MouseY
	w, h := win.Width(), win.Height()
	if !win.IsFloating() {
		var mode winDragMode
		if wx == w-1 {
			mode |= winDragRight
		}
		if wy == h-1 {
			mode |= winDragBottom
		}
		if mode == 0 {
			return
		}
		wm.winDrag = mode
		wm.winDragWin = win
		return false, true, true
	}
	if win.HasWindowBar() && wy == 0 {
		return wm.handleWindowBarPress(win, wx, w)
	}
	var mode winDragMode
	if wx == 0 {
		mode |= winDragLeft
	}
	if wx == w-1 {
		mode |= winDragRight
	}
	if wy == h-1 {
		mode |= winDragBottom
	}
	if wy == 0 {
		mode |= winDragTop
	}
	if mode == 0 {
		return
	}
	wm.SetFocus(wm.newNode(win))
	wm.winDrag = mode
	wm.winDragWin = win
	return false, true, true
}

// handleWindowBarPress routes a press on a floating window's bar: the
// close icon closes the window, the corner cells start a diagonal
// resize drag, a double press toggles maximize, and any other cell
// starts a move drag.
func (wm *WindowManager) handleWindowBarPress(
	win component.Window, wx, w int,
) (exit, handled, done bool) {
	switch wx {
	case component.WindowBarCloseIconX:
		return wm.closeFromBar(win)
	case 0, w - 1:
		wm.SetFocus(wm.newNode(win))
		wm.winDrag = winDragTop | winDragLeft
		if wx == w-1 {
			wm.winDrag = winDragTop | winDragRight
		}
		wm.winDragWin = win
		return false, true, true
	}
	wm.SetFocus(wm.newNode(win))
	if win.ID() == wm.winBarPressID &&
		time.Since(wm.winBarPressTime) < windowBarDoubleClickTimeout {
		wm.winBarPressID = 0
		wm.comp.ToggleMaximize(win)
		return false, true, true
	}
	wm.winBarPressID = win.ID()
	wm.winBarPressTime = time.Now()
	wm.winDrag = winDragMove
	wm.winDragWin = win
	wm.winDragGrab = term.Coordinates{X: wx, Y: 0}
	return false, true, true
}

func (wm *WindowManager) closeFromBar(win component.Window) (
	exit, handled, done bool,
) {
	bw := wm.newNode(win)
	if h := wm.config.FloatingBar; h != nil && h.OnBarClose(bw) {
		return false, true, true
	}
	_ = bw.Close()
	return false, true, true
}

// contentBounds returns the maximum local (X, Y) inside win's content
// area, accounting for the one-cell frame on each side when frame is true.
func contentBounds(win component.Window, frame bool) (maxX, maxY int) {
	w, h := win.Width(), win.Height()
	if frame {
		w -= 2
		h -= 2
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w - 1, h - 1
}

func (wm *WindowManager) handleScrollBarMouse(
	win component.Window, barPos, barHeight int,
	frame *compapi.Frame, ev term.Event,
) (bool, bool) {
	frame.ScrollBarChar = wm.scrollBarHoverChar(frame)
	if ev.Key != term.MouseLeft {
		wm.resetScrollBarMouse()
		return false, false
	}

	prevDrag := wm.prevMouseScrollBarDrag
	if !prevDrag {
		wm.prevMouseScrollBarOffset = barPos - ev.MouseY
		wm.prevMouseScrollBarDrag = true
		wm.prevMouseLeftChild = win
		return false, false
	}

	scroll := frame.Content().(compapi.Scrollable)
	mouseOffset := wm.prevMouseScrollBarOffset
	if barPos-mouseOffset > ev.MouseY {
		for i := 0; i < barPos-mouseOffset-ev.MouseY && scroll.SeekUp(); i++ {
		}
		return false, true
	} else if barPos+mouseOffset < ev.MouseY {
		for i := 0; i < ev.MouseY-barPos+mouseOffset && scroll.SeekDown(); i++ {
		}
		return false, true
	}

	return false, false
}

func (wm *WindowManager) scrollBarHoverChar(f *compapi.Frame) (ch rune) {
	ch = wm.config.ScrollBarHoverChar
	if ch != 0 {
		return
	}
	ch = f.ScrollBarChar
	if ch != 0 {
		return
	}
	ch = f.FrameCharSet.VerticalRight
	return
}

func (wm *WindowManager) switchFocus(tileFn func(Window) (Window, bool)) bool {
	tile, ok := tileFn(wm.focus)
	if !ok {
		return false
	}
	wm.SetFocus(wm.newNode(tile.Window))
	return true
}

func (wm *WindowManager) swapContent(tileFn func(Window) (Window, bool)) bool {
	win, ok := tileFn(wm.focus)
	if !ok {
		return false
	}
	if win.IsFloating() || wm.focus.IsFloating() {
		return false
	}
	focusContent := wm.focus.Content()
	swapContent := win.SetContent(focusContent)
	wm.focus.SetContent(swapContent)
	return true
}
