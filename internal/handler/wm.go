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

	Dim               bool
	BW                bool
	FocusFrameAttr    term.Attributes
	FocusFrameCharSet compapi.FrameCharSet
	// ScrollBarHoverCharSet draws the scroll bar under the pointer, and
	// the one it drags. Each rune or image it leaves empty is
	// ScrollBarCharSet's, and its graphics are Overlay when
	// ScrollBarCharSet's are.
	ScrollBarHoverCharSet compapi.ScrollBarCharSet
	// FloatingBar receives the interactions produced by floating
	// window bars. It may be nil.
	FloatingBar FloatingBarHandler
	// WindowClose is notified of every window that leaves the layout,
	// including those the window manager closes itself when their
	// content exits. It may be nil.
	WindowClose WindowCloseHandler
}

// WindowCloseHandler lets the owner of a WindowManager's content drop
// what it keeps for a window, whoever closed the window.
type WindowCloseHandler interface {
	// OnWindowClose is invoked once win has left the layout. The window
	// manager never closes win's content.
	OnWindowClose(win Window)
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

// frameDoubleClickTimeout is the maximum delay between two presses of a
// window's bar or tile edge for them to count as a double click.
const frameDoubleClickTimeout = 500 * time.Millisecond

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
	// winPress* describe the last frame press, which a second press on the
	// same part of the same window's frame turns into a double click unless
	// the pointer left the pressed cell. winPressPos is in root coordinates.
	winPressID   uint64
	winPressMode winDragMode
	winPressPos  term.Coordinates
	winPressTime time.Time
	// pointerWin is the window whose content last got a mouse event,
	// the zero Window for none. It is told the pointer left once the
	// pointer's events go elsewhere.
	pointerWin component.Window
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
	target := wm.focus
	mousePos := term.Coordinates{X: ev.MouseX, Y: ev.MouseY}
	trackPointer := ev.Type == term.EventMouse && (ev.Key == 0 ||
		wm.winDrag == 0 && !wm.prevMouseLeftDrag && !wm.prevMouseScrollBarDrag)
	pointerDispatched := false
	if trackPointer {
		defer func() {
			if !pointerDispatched {
				wm.pointerTo(component.Window{}, mousePos)
			}
		}()
	}
	if ev.Type == term.EventMouse {
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
				f.ScrollBarCharSet = wm.config.ScrollBarCharSet
			}
		})

		// set scroll bar hover charset, if applicable
		if frame, ok := childAtMouse.Frame(); ok {
			position, height, ok := frame.ScrollBar()
			if ok {
				if wm.onScrollBar(frame, position, height, ev.MouseX, ev.MouseY) ||
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
			if exit, handled, done := wm.handleWindowFramePress(childAtMouse, ev, mousePos); done {
				if wm.winDrag != 0 {
					term.SetPointerShape(ev, dragShape(wm.winDragWin, wm.winDrag))
				}
				return exit, handled
			}
		}

		// Frame motion reaches the child clamped into its content, so the
		// frame shape is set after dispatch to override the child's, and
		// the child gets a Pointer of its own so that a shape it sets
		// asynchronously cannot replace it either.
		if wm.config.Frame && !wm.prevMouseLeftDrag {
			mode, closeIcon := wm.frameDragModeAt(childAtMouse, ev.MouseX, ev.MouseY)
			if closeIcon || mode != 0 {
				shape := term.PointerLink
				if !closeIcon {
					shape = dragShape(childAtMouse, mode)
				}
				defer term.SetPointerShape(ev, shape)
				if _, ok := term.PointerFromContext(ev.Context); ok {
					ev.Context = term.ContextWithPointer(ev.Context, new(term.Pointer))
				}
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

		if target.Window != childAtMouse {
			switch ev.Key {
			case term.MouseLeft:
				wm.SetFocus(wm.newNode(childAtMouse))
				target = wm.focus
				// Fall through: the inner mouse.Mouse must see the
				// press to anchor a selection at the pressed cell.
			case term.MouseRelease:
				if !endDragAfter {
					return
				}
				// Captured-drag release: dispatch without changing focus.
			case 0, term.MouseWheelUp, term.MouseWheelDown:
				// As in kitty, pointer motion and the wheel reach the
				// window under the pointer without focusing it, but not
				// past a focused float, which can be anchored to the
				// content that would move beneath it.
				if target.IsFloating() {
					return
				}
				target = wm.newNode(childAtMouse)
			default:
				return
			}
		}

		if ev.Key == term.MouseLeft {
			wm.prevMouseLeftDrag = true
			wm.prevMouseLeftChild = childAtMouse
		}
	}

	var hexit bool
	focused := target == wm.focus
	size := wm.comp.SizeTiles()
	if trackPointer {
		wm.pointerTo(target.Window, mousePos)
		pointerDispatched = true
	}
	hexit, handled = target.Content().Handle(ev)
	if endDragAfter {
		wm.prevMouseLeftDrag = false
	}

	// if the dispatched handler wants to exit, close the window,
	// or signal exit to upstream handler if it was last window
	if hexit {
		if target.IsFloating() {
			target.Close()
			return
		}

		if exit = size == 1; exit {
			return
		}

		// Close the original tile that produced the exit signal. The
		// conditions below must distinguish these cases:
		//   1) Handle did not change the layout: wm.focus still equals
		//      target. Shift focus to a sibling and close the tile.
		//   2) Handle opened a floating window (e.g. a recovery prompt
		//      via Component.Prompt) that took over wm.focus mid-Handle.
		//      In that case the original tile must still be closed so
		//      the user is not left with a stranded non-tab split (see
		//      RUNE-139). Do not call ShiftFocus because the floating
		//      prompt is the intended new focus.
		//   3) Handle itself swapped focus to another tile (e.g. opened
		//      a new tile and shifted focus). Skip closing.
		//   4) The tile never had focus: it received the wheel or
		//      pointer motion. Close it and leave focus alone.
		size := wm.comp.SizeTiles()
		if target.Closed() || size == 1 {
			return
		}
		curr := wm.focus
		switch {
		case curr.ID() == target.ID():
			wm.ShiftFocus()
			target.Close()
		case curr.IsFloating(), !focused:
			target.Close()
		}
	}

	return
}

// pointerTo records that the pointer at pos is over win, the zero
// Window for none, and sends the window it was over before a motion at
// pos, outside it, so it drops whatever hover it shows. The motion
// carries no term.Pointer, so the shape set by win stands.
func (wm *WindowManager) pointerTo(win component.Window, pos term.Coordinates) {
	prev := wm.pointerWin
	wm.pointerWin = win
	if prev == win || prev.Closed() {
		return
	}
	offset := prev.Position()
	if wm.config.Frame {
		offset = term.CoordinatesSum(offset, term.Coordinates{X: 1, Y: 1})
	}
	pos = term.CoordinatesDiff(pos, offset)
	_, _ = wm.newNode(prev).Content().Handle(term.Event{
		Type: term.EventMouse, MouseX: pos.X, MouseY: pos.Y,
	})
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
	pos := term.CoordinatesSum(cursor, offset)
	if show && window.IsFloating() {
		// a float moved past an edge is clipped there
		show = wm.comp.InFloatingArea(pos)
	}
	return pos, style, show
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
	wm.comp.DrawWindows(w, func(win component.Window, w term.Writer) term.Writer {
		if win == focusWin {
			return w
		}
		return wm.dimmedWriter(w)
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
// Calling this method with height=0 resets the height, as
// component.WindowManager.SetHeight does.
func (wm *WindowManager) SetHeight(win Window, height int) bool {
	return wm.comp.SetHeight(win.Window, height)
}

// SetWidth fixes the width of the given window and returns true if possible,
// or returns false if not.
//
// Calling this method with width=0 resets the width to the window's default, as
// component.WindowManager.SetWidth does.
func (wm *WindowManager) SetWidth(win Window, width int) bool {
	return wm.comp.SetWidth(win.Window, width)
}

// SetDefaultWidth sets the width the tile win is reset to, by SetWidth with 0 or by
// double clicking a divider on its edge, and fixes it to that width; see
// component.WindowManager.SetDefaultWidth.
func (wm *WindowManager) SetDefaultWidth(win Window, width int) bool {
	return wm.comp.SetDefaultWidth(win.Window, width)
}

// Resize : tui.Component
func (wm *WindowManager) Resize(width, height int) {
	wm.comp.Resize(width, height)
}

// Close releases the frame images, as component.WindowManager.Close
// does. It does not close the windows.
func (wm *WindowManager) Close() error {
	return wm.comp.Close()
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
		if mouse != wm.winPressPos {
			wm.winPressID = 0
		}
		term.SetPointerShape(ev, dragShape(wm.winDragWin, wm.winDrag))
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
		term.SetPointerShape(ev, dragShape(wm.winDragWin, wm.winDrag))
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
	if !win.IsFloating() {
		// SetWidth and SetHeight resize win within its nearest split,
		// which need not be the one whose divider lies on this edge.
		if wm.winDrag&winDragRight != 0 && mouse.X-pos.X+1 >= minWindowDragSize {
			_ = wm.comp.MoveTileEdge(win, compapi.AlignmentRight, mouse.X)
		}
		if wm.winDrag&winDragBottom != 0 && mouse.Y-pos.Y+1 >= minWindowDragSize {
			_ = wm.comp.MoveTileEdge(win, compapi.AlignmentBottom, mouse.Y)
		}
		return
	}
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
	wm.comp.MoveWindow(win, term.Coordinates{X: newX, Y: newY})
}

// frameDragModeAt returns the drag a press at the window-local (wx, wy)
// starts: a resize from a frame edge or from a floating window's bar
// corner cells, or a move from the rest of the bar. It is zero inside
// the content, on minimized windows and on the tile edges no other tile
// shares, which have nothing to give space to or take it from. closeIcon
// reports a press on the bar's close icon, which starts no drag.
func (wm *WindowManager) frameDragModeAt(
	win component.Window, wx, wy int,
) (mode winDragMode, closeIcon bool) {
	if _, minimized := win.IsMinimized(); minimized {
		return 0, false
	}
	w, h := win.Width(), win.Height()
	if !win.IsFloating() {
		pos := win.Position()
		if _, ok := wm.comp.TileAt(term.Coordinates{X: pos.X + w, Y: pos.Y}); ok && wx == w-1 {
			mode |= winDragRight
		}
		if _, ok := wm.comp.TileAt(term.Coordinates{X: pos.X, Y: pos.Y + h}); ok && wy == h-1 {
			mode |= winDragBottom
		}
		return mode, false
	}
	if win.HasWindowBar() && wy == 0 {
		switch wx {
		case component.WindowBarCloseIconX:
			return 0, true
		case 0:
			return winDragTop | winDragLeft, false
		case w - 1:
			return winDragTop | winDragRight, false
		}
		return winDragMove, false
	}
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
	return mode, false
}

// dragShape returns the pointer shape of a drag of win in mode. Tile
// edges are dividers shared with the neighboring tiles.
func dragShape(win component.Window, mode winDragMode) term.PointerShape {
	horizontal := mode&(winDragLeft|winDragRight) != 0
	vertical := mode&(winDragTop|winDragBottom) != 0
	switch {
	case mode == winDragMove:
		return term.PointerMove
	case horizontal && vertical:
		left, top := mode&winDragLeft != 0, mode&winDragTop != 0
		if left == top {
			return term.PointerResizeNWSE
		}
		return term.PointerResizeNESW
	case horizontal && !win.IsFloating():
		return term.PointerColResize
	case horizontal:
		return term.PointerResizeEW
	case vertical && !win.IsFloating():
		return term.PointerRowResize
	case vertical:
		return term.PointerResizeNS
	}
	return term.PointerDefault
}

// handleWindowFramePress starts the drag of frameDragModeAt or closes
// the window from its bar's close icon. A double click starts no drag: on
// a bar it toggles maximize, and on a tile edge it resets the sizes of the
// tiles that meet there. Event coordinates are window-local and mouse is
// in root coordinates. done reports whether the press was consumed.
func (wm *WindowManager) handleWindowFramePress(
	win component.Window, ev term.Event, mouse term.Coordinates,
) (exit, handled, done bool) {
	mode, closeIcon := wm.frameDragModeAt(win, ev.MouseX, ev.MouseY)
	if closeIcon {
		return wm.closeFromBar(win)
	}
	if mode == 0 {
		return
	}
	if win.IsFloating() {
		wm.SetFocus(wm.newNode(win))
	}
	doubleClick := win.ID() == wm.winPressID && mode == wm.winPressMode &&
		time.Since(wm.winPressTime) < frameDoubleClickTimeout
	wm.winPressID = 0
	switch {
	case doubleClick && mode == winDragMove:
		wm.comp.ToggleMaximize(win)
		return false, true, true
	case doubleClick && !win.IsFloating():
		if mode&winDragRight != 0 {
			wm.comp.ResetTileEdge(win, compapi.AlignmentRight)
		}
		if mode&winDragBottom != 0 {
			wm.comp.ResetTileEdge(win, compapi.AlignmentBottom)
		}
		term.SetPointerShape(ev, dragShape(win, mode))
		return false, true, true
	}
	wm.winPressID = win.ID()
	wm.winPressMode = mode
	wm.winPressPos = mouse
	wm.winPressTime = time.Now()
	if mode == winDragMove {
		wm.winDragGrab = term.Coordinates{X: ev.MouseX, Y: 0}
	}
	wm.winDrag = mode
	wm.winDragWin = win
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
	frame.ScrollBarCharSet = wm.scrollBarHoverCharSet()
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

// onScrollBar reports whether x, y, relative to frame, is on its scroll
// bar at position, height cells high, or on the cells left of it that
// the bar's images reach.
func (wm *WindowManager) onScrollBar(
	frame *compapi.Frame, position term.Coordinates, height, x, y int,
) bool {
	var outset int
	if g := frame.ScrollBarCharSet.Graphics; g.Top != "" || g.Middle != "" || g.Bottom != "" {
		outset = max(0, min(wm.config.ScrollBarOutset, position.X))
	}
	return x >= position.X-outset && x <= position.X &&
		y >= position.Y && y < position.Y+height
}

func (wm *WindowManager) scrollBarHoverCharSet() compapi.ScrollBarCharSet {
	cs, hover := wm.config.ScrollBarCharSet, wm.config.ScrollBarHoverCharSet
	for _, slot := range []struct {
		dst *rune
		src rune
	}{
		{&cs.Top, hover.Top},
		{&cs.Middle, hover.Middle},
		{&cs.Bottom, hover.Bottom},
	} {
		if slot.src != 0 {
			*slot.dst = slot.src
		}
	}
	for _, slot := range []struct {
		dst *string
		src string
	}{
		{&cs.Graphics.Top, hover.Graphics.Top},
		{&cs.Graphics.Middle, hover.Graphics.Middle},
		{&cs.Graphics.Bottom, hover.Graphics.Bottom},
	} {
		if slot.src != "" {
			*slot.dst = slot.src
		}
	}
	return cs
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
