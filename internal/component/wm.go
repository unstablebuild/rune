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
	"slices"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// WindowManagerConfig represents the configuration for a WindowManager
// to be initialized.
type WindowManagerConfig struct {
	Frame         bool
	FrameAttr     term.Attributes
	ScrollBarAttr term.Attributes
	ScrollBarChar rune
	component.FrameCharSet
	NoMaxSize bool

	// WindowBar replaces the top frame line of floating windows with a
	// solid bar drawn with WindowBarCharSet, and displays CloseIcon at
	// the top-left of the bar. It has no effect when Frame is false.
	WindowBar        bool
	WindowBarCharSet WindowBarCharSet
	CloseIcon        rune
	CloseIconAttr    term.Attributes
}

// WindowBarCharSet is the set of characters used to draw the window
// bar over a floating window's top frame line.
type WindowBarCharSet struct {
	Left, Horizontal, Right rune
}

// WindowBarCloseIconX is the bar-local X offset at which the close
// icon is rendered on a floating window's bar.
const WindowBarCloseIconX = 1

// WindowManager wraps a TileTree to provide an easier API.
type WindowManager struct {
	tree          TileTree
	width, height int
	float         []*floatingNode
	config        WindowManagerConfig

	// this is cached and calculated to figure out
	// how to offset windows when there are minimized floating windows.
	minimizedDirty  bool
	minimizedOffset term.Coordinates
	minimizedHeight int
	minimizedWidth  int
	minimizedPos    map[uint64]windowPos
}

// Draw satisfies tui.Component
func (wm *WindowManager) Draw(w term.Writer) {
	wm.DrawWindows(w, nil)
}

// DrawWindows draws every window like Draw, each through the writer
// style returns for it unless style is nil. Images that overflow a tile
// are drawn once every tile is, so that no tile covers them, and before
// the floating windows, which do.
func (wm *WindowManager) DrawWindows(
	w term.Writer, style func(win Window, w term.Writer) term.Writer,
) {
	tiles := &overflowDeferWriter{Writer: w}
	wm.Iterate(func(win Window) {
		var target term.Writer = tiles
		if win.IsFloating() {
			tiles.flush()
			target = w
		}
		if style != nil {
			target = style(win, target)
		}
		wm.DrawWindow(win, target)
	})
	tiles.flush()
}

// FloatingWindows return a slice of all the open floating windows.
func (wm *WindowManager) FloatingWindows() (ret []Window) {
	for _, w := range wm.float {
		ret = append(ret, wm.nodeToWindow(w))
	}
	return
}

// TileTree returns a TileTree representing all the open tiles.
func (wm *WindowManager) TileTree() *TileTree {
	return &wm.tree
}

// TileLayout returns the current tiled window tree layout.
func (wm *WindowManager) TileLayout() TileLayout {
	layout := wm.tree.Layout()
	layout.Floating = make([]FloatingLayout, 0, len(wm.float))
	for _, win := range wm.float {
		layout.Floating = append(layout.Floating, win.layout())
	}
	return layout
}

// RestoreTileLayout replaces the current tiled layout and returns a map from
// old layout window IDs to newly allocated windows.
func (wm *WindowManager) RestoreTileLayout(
	layout TileLayout,
	content func(windowID uint64) tui.Component,
) map[uint64]Window {
	wm.tree.Iterate(func(node *TileNode) {
		closeTileLayoutContent(node.content)
	})
	for _, win := range wm.float {
		closeTileLayoutContent(win.Content())
		win.wm = nil
	}
	wm.float = make([]*floatingNode, 0)
	nodes := wm.tree.RestoreLayout(layout, func(windowID uint64) tui.Component {
		c := content(windowID)
		if c == nil {
			c = component.Nop()
		}
		if wm.config.Frame {
			c = wm.withFrame(c)
		}
		return c
	})
	ret := make(map[uint64]Window, len(nodes))
	for id, node := range nodes {
		ret[id] = wm.nodeToWindow(node)
	}
	for _, floatingLayout := range layout.Floating {
		c := content(floatingLayout.WindowID)
		if c == nil {
			c = component.Nop()
		}
		floating, ok := c.(component.Floating)
		if !ok {
			if h, ok := c.(tui.Handler); ok {
				floating = staticFloatingHandler{Handler: h, width: wm.width, height: wm.height}
			} else {
				floating = component.StaticFloating(c, wm.width, wm.height)
			}
		}
		if wm.config.Frame {
			floating = wm.withFrame(floating)
		}
		win := newFloatingNode(wm, floating, FloatingConfig{
			Alignment: floatingLayout.Alignment,
			Offset:    floatingLayout.Offset,
			NoBar:     floatingLayout.NoBar,
			Title:     floatingLayout.Title,
		}, wm.minimizedWidth, wm.minimizedHeight)
		wm.applyWindowBar(win)
		wm.float = append(wm.float, win)
		ret[floatingLayout.WindowID] = wm.nodeToWindow(win)
		if floatingLayout.MinimizedAlignment != 0 {
			win.minimized = floatingLayout.MinimizedAlignment
			win.minimizedPadding = floatingLayout.MinimizedPadding
		}
	}
	wm.Resize(wm.width, wm.height)
	return ret
}

type staticFloatingHandler struct {
	tui.Handler
	width, height int
}

func (s staticFloatingHandler) Dimensions() (int, int) {
	return s.width, s.height
}

func (s staticFloatingHandler) Content() tui.Handler {
	return s.Handler
}

func closeTileLayoutContent(c tui.Component) {
	if frame, ok := c.(*component.Frame); ok {
		c = frame.Content()
	}
	if closer, ok := c.(interface{ Close() error }); ok {
		_ = closer.Close()
	}
}

// DrawWindow can be used to arbitrarily draw floating windows returned
// by FloatingWindows. If win is not a floating window, this method will panic.
func (wm *WindowManager) DrawWindow(win Window, w term.Writer) {
	if wm.minimizedDirty {
		wm.Resize(wm.width, wm.height)
	}

	nonMinimizedW := component.VirtualWriter{
		Writer: w,
		Offset: wm.minimizedOffset,
		Height: wm.height,
		Width:  wm.width,
	}

	f, ok := win.node.(*floatingNode)
	if !ok {
		wm.tree.DrawTile(win.node.(*TileNode), &nonMinimizedW)
		return
	}

	if f.minimized == 0 {
		f.Draw(&nonMinimizedW)
		wm.drawWindowBar(f, &nonMinimizedW)
		return
	}

	winPos, ok := wm.minimizedPos[f.ID()]
	if !ok {
		panic("corrupt WindowManager: no pre-calculated minimized position for floating node")
	}

	if wm.minimizedOffset.Y < 0 || wm.minimizedOffset.X < 0 ||
		wm.minimizedWidth <= 0 || wm.minimizedHeight <= 0 {
		return
	}

	pos := winPos.from()
	length := winPos.length()
	switch f.minimized {
	case component.AlignmentTop:
		wm.drawMinimizedTop(w, f, pos, length)
	case component.AlignmentBottom:
		wm.drawMinimizedBottom(w, f, pos, length)
	case component.AlignmentLeft:
		wm.drawMinimizedLeft(w, f, pos, length)
	case component.AlignmentRight:
		wm.drawMinimizedRight(w, f, pos, length)
	}
}

// NewWindowManager allocates storage for a new WindowManager and initializes it.
func NewWindowManager(
	content tui.Component, config WindowManagerConfig,
) (*WindowManager, Window) {
	ret := new(WindowManager)
	win := ret.Init(content, config)
	return ret, win
}

// Init initializes this WindowManager with content and config.
func (wm *WindowManager) Init(
	content tui.Component, config WindowManagerConfig,
) (n Window) {
	wm.float = make([]*floatingNode, 0)
	wm.minimizedDirty = true
	wm.config = config
	wm.minimizedPos = make(map[uint64]windowPos)

	if wm.config.Frame {
		content = wm.withFrame(content)
	}
	node := wm.tree.Init(content)
	return wm.nodeToWindow(node)
}

// Iterate applies op to the content of all widnows of this WindowManager.
func (wm *WindowManager) Iterate(op func(Window)) {
	wm.tree.Iterate(func(node *TileNode) {
		op(wm.nodeToWindow(node))
	})
	// op may close the visited window, which calls closeFloatingWindow
	// and removes the entry from wm.float via slices.Delete. Re-read the
	// slice each step and advance only when the current slot is still the
	// same node, otherwise the deletion shifted a new node into this
	// index and we must visit it next.
	for i := 0; i < len(wm.float); {
		fw := wm.float[i]
		op(wm.nodeToWindow(fw))
		if i < len(wm.float) && wm.float[i] == fw {
			i++
		}
	}
}

// Resize satisfies tui.Component.
func (wm *WindowManager) Resize(width, height int) {
	wm.width, wm.height = width, height
	wm.calculateMinimizedOffsets()
	wm.tree.Resize(wm.minimizedWidth, wm.minimizedHeight)
	for _, fw := range wm.float {
		if fw.minimized == 0 || fw.minimizedPadding == 0 {
			fw.SetMaxSize(wm.minimizedWidth, wm.minimizedHeight)
		}
	}
	wm.minimizedDirty = false
}

// SizeTiles returns the number of tiled windows of this WindowManager.
func (wm *WindowManager) SizeTiles() int {
	return wm.tree.Size()
}

// SizeFloating returns the number of floating windows of this
// WindowManager.
func (wm *WindowManager) SizeFloating() int {
	return len(wm.float)
}

// SplitHorizontal creates a new Window by splitting the height of win in two and
// initializes it with content. It returns true if it succeeds or false if
// this window is a floating window created via FloatingWindow and so it's not a tile.
func (wm *WindowManager) SplitHorizontal(win Window, content tui.Component) (Window, bool) {
	t, ok := win.node.(*TileNode)
	if !ok {
		return Window{}, false
	}
	if wm.config.Frame {
		content = wm.withFrame(content)
	}
	node := wm.tree.SplitHorizontal(t, content)
	return wm.nodeToWindow(node), true
}

// SplitVertical creates a new Window by splitting the width of win in two and
// initializes it with content.
func (wm *WindowManager) SplitVertical(win Window, content tui.Component) (Window, bool) {
	t, ok := win.node.(*TileNode)
	if !ok {
		return Window{}, false
	}
	if wm.config.Frame {
		content = wm.withFrame(content)
	}
	node := wm.tree.SplitVertical(t, content)
	return wm.nodeToWindow(node), true
}

// SplitRoot inserts a new top-level tiled window at the given side of the root layout.
// Left/right create full-height columns; top/bottom create full-width rows.
func (wm *WindowManager) SplitRoot(alignment component.Alignment, content tui.Component) (Window, bool) {
	if wm.config.Frame {
		content = wm.withFrame(content)
	}
	var (
		direction splitDir
		idx       int
	)
	switch alignment {
	case component.AlignmentLeft:
		direction = vertical
		idx = 0
	case component.AlignmentRight:
		direction = vertical
		idx = len(wm.tree.root.children)
	case component.AlignmentTop:
		direction = horizontal
		idx = 0
	case component.AlignmentBottom:
		direction = horizontal
		idx = len(wm.tree.root.children)
	default:
		return Window{}, false
	}
	node := wm.tree.SplitRoot(direction, idx, content)
	return wm.nodeToWindow(node), true
}

// SetHeight fixes the height of the given window and returns true if possible,
// or returns false if not.
//
// Calling this method with height=0 effectively reverts back to the height
// being automatically distributed between windows.
func (wm *WindowManager) SetHeight(win Window, height int) bool {
	fn, ok := win.node.(*floatingNode)
	if ok {
		return fn.setHeight(height)
	}
	t := win.node.(*TileNode)
	// 0 resets, but less than 3 would make the window almost disappear
	if wm.config.Frame && height < 3 && height > 0 {
		return false
	}
	return t.SetFixedHeight(height)
}

// SetWidth fixes the width of the given window and returns true if possible,
// or returns false if not.
//
// Calling this method with width=0 effectively reverts back to the width
// being automatically distributed between windows.
func (wm *WindowManager) SetWidth(win Window, width int) bool {
	fn, ok := win.node.(*floatingNode)
	if ok {
		return fn.setWidth(width)
	}
	t := win.node.(*TileNode)
	// 0 resets, but less than 3 would make the window almost disappear
	if wm.config.Frame && width < 3 && width > 0 {
		return false
	}
	return t.SetFixedWidth(width)
}

// WindowAt returns the window at pos.
func (wm *WindowManager) WindowAt(pos term.Coordinates) (Window, bool) {
	if pos.X < 0 || pos.Y < 0 || pos.X >= wm.width || pos.Y >= wm.height {
		return Window{}, false
	}

	if wm.minimizedDirty {
		wm.Resize(wm.width, wm.height)
	}

	// first return any floating window that might be rendered over everything else
	floating := -1
	for i, fw := range wm.float {
		if fw.minimized != 0 {
			continue
		}
		fwpos := wm.nodeToWindow(fw).Position()
		fwidth, fheight := fw.Width(), fw.Height()
		if pos.X >= fwpos.X && pos.Y >= fwpos.Y &&
			pos.X < fwpos.X+fwidth && pos.Y < fwpos.Y+fheight {
			floating = i
		}
	}
	if floating >= 0 {
		return wm.nodeToWindow(wm.float[floating]), true
	}

	for _, winPos := range wm.minimizedPos {
		from, to := winPos.position()
		if pos.X >= from.X && pos.X < to.X && pos.Y >= from.Y && pos.Y < to.Y {
			return winPos.win, true
		}
	}

	pos.Y -= wm.minimizedOffset.Y
	pos.X -= wm.minimizedOffset.X
	return wm.nodeToWindow(wm.tree.TileAt(pos)), true
}

// TileAt returns the tiled window at pos, ignoring floating and minimized
// windows rendered over the tiled layout.
func (wm *WindowManager) TileAt(pos term.Coordinates) (Window, bool) {
	if pos.X < 0 || pos.Y < 0 || pos.X >= wm.width || pos.Y >= wm.height {
		return Window{}, false
	}

	if wm.minimizedDirty {
		wm.Resize(wm.width, wm.height)
	}

	pos.Y -= wm.minimizedOffset.Y
	pos.X -= wm.minimizedOffset.X
	if pos.X < 0 || pos.Y < 0 ||
		pos.X >= wm.tree.root.width || pos.Y >= wm.tree.root.height {
		return Window{}, false
	}
	return wm.nodeToWindow(wm.tree.TileAt(pos)), true
}

// SetFrameCharSet sets the defaultframe border cells used
// to draw borders around tiles.  Note that this has no effect if WindowManager was
// initialized with border == false.
func (wm *WindowManager) SetFrameCharSet(b component.FrameCharSet) {
	if !wm.config.Frame {
		return
	}

	wm.config.FrameCharSet = b
	wm.Iterate(func(w Window) {
		w.SetFrameCharSet(b)
	})
}

// SetFrameAttr sets the default and focus window border attributes. Note that
// this has no effect if WindowManager was initialized with border == false.
func (wm *WindowManager) SetFrameAttr(attr term.Attributes) {
	if !wm.config.Frame {
		return
	}

	wm.config.FrameAttr = attr
	wm.Iterate(func(w Window) {
		w.node.Content().(*component.Frame).Attributes = attr
	})
}

// FloatingConfig abstracts configuration for
// creating floating windows.
type FloatingConfig struct {
	// Sets the alignment of the window.
	component.Alignment
	// Offset is to be applied to the position of the window
	// after alignment has been determined.
	Offset term.Coordinates
	// NoBar keeps the plain window frame instead of the window bar
	// when the WindowManager is configured with WindowBar.
	NoBar bool
	// Title is rendered centered on the window bar.
	Title string
}

// FloatingWindow creates a floating window.
func (wm *WindowManager) FloatingWindow(
	content component.Floating, cfg FloatingConfig,
) Window {
	if wm.config.Frame {
		content = wm.withFrame(content)
	}
	f := newFloatingNode(wm, content, cfg, wm.minimizedWidth, wm.minimizedHeight)
	wm.applyWindowBar(f)
	wm.float = append(wm.float, f)
	wm.minimizedDirty = true
	return wm.nodeToWindow(f)
}

// DefaultWindowManagerConfig returns a sane WindowManagerConfig.
func DefaultWindowManagerConfig() WindowManagerConfig {
	charset := component.FrameCharSetDefault()
	return WindowManagerConfig{
		Frame:         true,
		FrameAttr:     term.Attributes{},
		FrameCharSet:  charset,
		NoMaxSize:     true,
		ScrollBarAttr: term.Attributes{Attrs: term.AttrBold},
		WindowBar:     true,
		WindowBarCharSet: WindowBarCharSet{
			Left:       '█',
			Horizontal: '█',
			Right:      '█',
		},
		CloseIcon:     '●',
		CloseIconAttr: term.Attributes{Fg: term.ColorRed},
	}
}

// barCharSet overrides the top frame characters of cs with the
// configured window bar characters.
func (wm *WindowManager) barCharSet(cs component.FrameCharSet) component.FrameCharSet {
	cs.TopLeft = wm.config.WindowBarCharSet.Left
	cs.HorizontalTop = wm.config.WindowBarCharSet.Horizontal
	cs.TopRight = wm.config.WindowBarCharSet.Right
	return cs
}

// applyWindowBar overrides the top frame chars of a floating node's
// frame with the window bar charset when the bar is enabled.
func (wm *WindowManager) applyWindowBar(f *floatingNode) {
	if !wm.hasWindowBar(f) {
		return
	}
	frame := f.Content().(*component.Frame)
	frame.FrameCharSet = wm.barCharSet(frame.FrameCharSet)
}

func (wm *WindowManager) hasWindowBar(f *floatingNode) bool {
	return wm.config.Frame && wm.config.WindowBar && !f.noBar
}

// drawWindowBar draws the close icon and the window title over a
// floating window's bar. The icon and title cells take the bar's
// foreground as their background so they read as part of the bar.
func (wm *WindowManager) drawWindowBar(f *floatingNode, w term.Writer) {
	if !wm.hasWindowBar(f) || f.realWidth < 3 || f.realHeight < 3 {
		return
	}
	barAttr := f.Content().(*component.Frame).Attributes
	if wm.config.CloseIcon != 0 {
		attr := wm.config.CloseIconAttr
		attr.Bg = barAttr.Fg
		pos := f.realOffset
		pos.X += WindowBarCloseIconX
		w.SetCell(pos, term.NewCell(wm.config.CloseIcon, 1, attr))
	}
	wm.drawWindowTitle(f, barAttr, w)
}

func (wm *WindowManager) drawWindowTitle(
	f *floatingNode, barAttr term.Attributes, w term.Writer,
) {
	if f.title == "" {
		return
	}
	// interior bar cells span [1, realWidth-2]; keep one bar cell
	// after the close icon so the icon stays distinguishable.
	minX := WindowBarCloseIconX + 2
	maxX := f.realWidth - 2
	avail := maxX - minX + 1
	if avail < 3 {
		return
	}
	title := []rune(" " + f.title + " ")
	if len(title) > avail {
		title = title[:avail]
	}
	attr := term.Attributes{Fg: barAttr.Bg, Bg: barAttr.Fg}
	start := f.realOffset.X + minX + (avail-len(title))/2
	for i, r := range title {
		w.SetCell(term.Coordinates{X: start + i, Y: f.realOffset.Y},
			term.NewCell(r, 1, attr))
	}
}

// MoveWindow moves a floating window so its top-left corner sits at
// pos, given in the same coordinate space as Window.Position. The
// position is clamped so the window remains fully visible. It returns
// false if win is not a floating window or is minimized.
func (wm *WindowManager) MoveWindow(win Window, pos term.Coordinates) bool {
	fn, ok := win.node.(*floatingNode)
	if !ok || fn.minimized != 0 {
		return false
	}
	if wm.minimizedDirty {
		wm.Resize(wm.width, wm.height)
	}
	pos.X -= wm.minimizedOffset.X
	pos.Y -= wm.minimizedOffset.Y
	fn.maximized = false
	fn.alignment = 0
	fn.updateDesiredDimensions()
	pos.X = max(0, min(pos.X, fn.maxWidth-fn.desiredWidth))
	pos.Y = max(0, min(pos.Y, fn.maxHeight-fn.desiredHeight))
	fn.desiredOffset = pos
	fn.resize()
	return true
}

// ToggleMaximize grows a floating window to cover the whole window
// manager area, or restores its previous geometry when it is already
// maximized. It returns false if win is not a floating window or is
// minimized. Any later move or resize of the window drops the
// maximized state and keeps the new geometry.
func (wm *WindowManager) ToggleMaximize(win Window) bool {
	fn, ok := win.node.(*floatingNode)
	if !ok || fn.minimized != 0 {
		return false
	}
	if wm.minimizedDirty {
		wm.Resize(wm.width, wm.height)
	}
	if fn.maximized {
		fn.maximized = false
		fn.alignment = fn.restoreAlignment
		fn.desiredOffset = fn.restoreOffset
	} else {
		fn.restoreAlignment = fn.alignment
		fn.restoreOffset = fn.desiredOffset
		fn.maximized = true
		fn.alignment = 0
		fn.desiredOffset = term.Coordinates{}
	}
	fn.updateDesiredDimensions()
	fn.resize()
	return true
}

func (wm *WindowManager) withFrame(handler tui.Component) *component.Frame {
	f := component.NewFrame(handler)
	f.FrameCharSet = wm.config.FrameCharSet
	f.Attributes = wm.config.FrameAttr
	f.ScrollBarAttributes = wm.config.ScrollBarAttr
	f.ScrollBarChar = wm.config.ScrollBarChar
	return f
}

func (wm *WindowManager) closeFloatingWindow(w *floatingNode) {
	for i, f := range wm.float {
		if f == w {
			wm.float = slices.Delete(wm.float, i, i+1)
			break
		}
	}
}

// ForegroundFloating moves win to the top of the floating window z-order so
// it is drawn last (above other floating windows). It is a no-op for tiled
// windows or windows not managed here.
func (wm *WindowManager) ForegroundFloating(win Window) {
	fn, ok := win.node.(*floatingNode)
	if !ok {
		return
	}
	for i, f := range wm.float {
		if f == fn {
			if i == len(wm.float)-1 {
				return
			}
			wm.float = append(slices.Delete(wm.float, i, i+1), fn)
			return
		}
	}
}

func (wm *WindowManager) nodeToWindow(node windowNode) Window {
	return Window{node: node, wm: wm}
}

func (wm *WindowManager) calculateMinimizedOffsets() {
	clear(wm.minimizedPos)

	var offsetLeft, offsetTop, offsetBottom, offsetRight int
	for _, win := range wm.FloatingWindows() {
		fn := win.node.(*floatingNode)
		switch fn.minimized {
		case component.AlignmentTop:
			wm.minimizedPos[win.ID()] = windowPos{pos: offsetTop, win: win}
			offsetTop++
			offsetTop += fn.minimizedPadding
		case component.AlignmentBottom:
			wm.minimizedPos[win.ID()] = windowPos{pos: offsetBottom, win: win}
			offsetBottom++
			offsetBottom += fn.minimizedPadding
		case component.AlignmentLeft:
			wm.minimizedPos[win.ID()] = windowPos{pos: offsetLeft, win: win}
			offsetLeft++
			offsetLeft += fn.minimizedPadding
		case component.AlignmentRight:
			wm.minimizedPos[win.ID()] = windowPos{pos: offsetRight, win: win}
			offsetRight++
			offsetRight += fn.minimizedPadding
		default:
		}
	}
	wm.minimizedOffset = term.Coordinates{Y: offsetTop, X: offsetLeft}
	wm.minimizedHeight = max(0, wm.height-offsetBottom-offsetTop)
	wm.minimizedWidth = max(0, wm.width-offsetRight-offsetLeft)

	// now that we know where everything is positioned,
	// resize floating nodes with padding, aka that will
	// be partially rendered in calls to Draw
	for _, fn := range wm.float {
		if fn.minimizedPadding == 0 || fn.minimized == 0 {
			continue
		}
		switch fn.minimized {
		case component.AlignmentTop, component.AlignmentBottom:
			width := wm.width
			if wm.config.Frame {
				width -= 2
			}
			width = max(0, width)
			fn.Content().Resize(width, fn.minimizedPadding)
		case component.AlignmentLeft, component.AlignmentRight:
			height := wm.minimizedHeight
			if wm.config.Frame {
				height -= 2
			}
			height = max(0, height)
			fn.Content().Resize(fn.minimizedPadding, height)
		}
	}
}

func (wm *WindowManager) drawMinimizedContent(
	w term.Writer, f *floatingNode, at term.Coordinates,
) {
	vw := component.VirtualWriter{
		Writer: w,
		Offset: at,
		Height: wm.height,
		Width:  wm.width,
	}
	f.Content().Draw(&vw)
}

func (wm *WindowManager) drawMinimizedTop(
	w term.Writer, f *floatingNode, pos term.Coordinates, length int,
) {
	frameAttr := wm.config.FrameAttr
	if attr, ok := f.content.C.(component.WithAttributes); ok {
		frameAttr = attr.SetAttr(term.Attributes{})
		attr.SetAttr(frameAttr)
	}
	w.SetCell(pos, term.NewCell(wm.config.TopLeft, 1, frameAttr))
	for x := 1; x < length-1; x++ {
		w.SetCell(term.Coordinates{X: x, Y: pos.Y}, term.NewCell(wm.config.HorizontalTop, 1, frameAttr))
	}
	w.SetCell(term.Coordinates{X: length - 1, Y: pos.Y}, term.NewCell(wm.config.TopRight, 1, frameAttr))
	if f.minimizedPadding == 0 {
		return
	}
	for y := pos.Y + 1; y < pos.Y+1+f.minimizedPadding; y++ {
		w.SetCell(term.Coordinates{X: length - 1, Y: y}, term.NewCell(wm.config.VerticalRight, 1, frameAttr))
		w.SetCell(term.Coordinates{X: pos.X, Y: y}, term.NewCell(wm.config.VerticalLeft, 1, frameAttr))
	}
	at := pos
	if wm.config.Frame {
		at.X++
		at.Y++
	}
	wm.drawMinimizedContent(w, f, at)
}

func (wm *WindowManager) drawMinimizedBottom(
	w term.Writer, f *floatingNode, pos term.Coordinates, length int,
) {
	frameAttr := wm.config.FrameAttr
	if attr, ok := f.content.C.(component.WithAttributes); ok {
		frameAttr = attr.SetAttr(term.Attributes{})
		attr.SetAttr(frameAttr)
	}
	bottomFramePos := pos.Y + f.minimizedPadding
	w.SetCell(term.Coordinates{X: pos.X, Y: bottomFramePos}, term.NewCell(wm.config.BottomLeft, 1, frameAttr))
	for x := 1; x < length-1; x++ {
		w.SetCell(term.Coordinates{X: x, Y: bottomFramePos}, term.NewCell(wm.config.HorizontalBottom, 1, frameAttr))
	}
	w.SetCell(term.Coordinates{X: length - 1, Y: bottomFramePos}, term.NewCell(wm.config.BottomRight, 1, frameAttr))
	if f.minimizedPadding == 0 {
		return
	}
	for y := pos.Y; y < pos.Y+f.minimizedPadding; y++ {
		w.SetCell(term.Coordinates{X: length - 1, Y: y}, term.NewCell(wm.config.VerticalRight, 1, frameAttr))
		w.SetCell(term.Coordinates{X: pos.X, Y: y}, term.NewCell(wm.config.VerticalLeft, 1, frameAttr))
	}
	at := pos
	if wm.config.Frame {
		at.X++
		// no Y++ because top frame of bottom minimized window
		// is omitted.
	}
	wm.drawMinimizedContent(w, f, at)
}

func (wm *WindowManager) drawMinimizedLeft(
	w term.Writer, f *floatingNode, pos term.Coordinates, length int,
) {
	frameAttr := wm.config.FrameAttr
	if attr, ok := f.content.C.(component.WithAttributes); ok {
		frameAttr = attr.SetAttr(term.Attributes{})
		attr.SetAttr(frameAttr)
	}
	yOffset := wm.minimizedOffset.Y
	w.SetCell(pos, term.NewCell(wm.config.TopLeft, 1, frameAttr))
	for y := 1 + yOffset; y < yOffset+length-1; y++ {
		w.SetCell(term.Coordinates{X: pos.X, Y: y}, term.NewCell(wm.config.VerticalLeft, 1, frameAttr))
	}
	w.SetCell(term.Coordinates{X: pos.X, Y: yOffset + length - 1}, term.NewCell(wm.config.BottomLeft, 1, frameAttr))
	if f.minimizedPadding == 0 {
		return
	}
	for x := pos.X + 1; x < pos.X+1+f.minimizedPadding; x++ {
		w.SetCell(term.Coordinates{X: x, Y: pos.Y}, term.NewCell(wm.config.HorizontalTop, 1, frameAttr))
		w.SetCell(term.Coordinates{X: x, Y: yOffset + length - 1}, term.NewCell(wm.config.HorizontalBottom, 1, frameAttr))
	}
	at := pos
	if wm.config.Frame {
		at.X++
		at.Y++
	}
	wm.drawMinimizedContent(w, f, at)
}

func (wm *WindowManager) drawMinimizedRight(
	w term.Writer, f *floatingNode, pos term.Coordinates, length int,
) {
	frameAttr := wm.config.FrameAttr
	if attr, ok := f.content.C.(component.WithAttributes); ok {
		frameAttr = attr.SetAttr(term.Attributes{})
		attr.SetAttr(frameAttr)
	}
	yOffset := wm.minimizedOffset.Y
	rightFramePos := pos.X + f.minimizedPadding
	w.SetCell(term.Coordinates{X: rightFramePos, Y: pos.Y}, term.NewCell(wm.config.TopRight, 1, frameAttr))
	for y := 1 + yOffset; y < yOffset+length-1; y++ {
		w.SetCell(term.Coordinates{X: rightFramePos, Y: y}, term.NewCell(wm.config.VerticalRight, 1, frameAttr))
	}
	w.SetCell(term.Coordinates{X: rightFramePos, Y: yOffset + length - 1},
		term.NewCell(wm.config.BottomRight, 1, frameAttr))
	if f.minimizedPadding == 0 {
		return
	}
	for x := pos.X; x < rightFramePos; x++ {
		w.SetCell(term.Coordinates{X: x, Y: pos.Y}, term.NewCell(wm.config.HorizontalTop, 1, frameAttr))
		w.SetCell(term.Coordinates{X: x, Y: yOffset + length - 1}, term.NewCell(wm.config.HorizontalBottom, 1, frameAttr))
	}
	at := pos
	if wm.config.Frame {
		at.Y++
		// no X++ because left frame on right-side minimized window
		// is omitted.
	}
	wm.drawMinimizedContent(w, f, at)
}

func (wm *WindowManager) topMostTile() Window {
	return wm.nodeToWindow(wm.tree.root.leftMostChild())
}

func (wm *WindowManager) bottomMostTile() Window {
	return wm.nodeToWindow(wm.tree.root.rightMostChild())
}

func (wm *WindowManager) rightMostTile() Window {
	return wm.nodeToWindow(wm.tree.root.rightMostChild())
}

func (wm *WindowManager) leftMostTile() Window {
	return wm.nodeToWindow(wm.tree.root.leftMostChild())
}

// post-draw helper to find where minimized windows are positioned
type windowPos struct {
	win Window
	pos int
}

func (w windowPos) position() (term.Coordinates, term.Coordinates) {
	from := w.from()
	length := w.length()
	var to term.Coordinates
	fn := w.win.node.(*floatingNode)
	switch fn.minimized {
	case component.AlignmentTop, component.AlignmentBottom:
		to = from
		to.Y++
		to.Y += fn.minimizedPadding
		to.X += length
	case component.AlignmentLeft, component.AlignmentRight:
		to = from
		to.X++
		to.X += fn.minimizedPadding
		to.Y += length
	default:
		panic("invalid minimize alignment")
	}
	return from, to
}

func (w windowPos) from() term.Coordinates {
	fn := w.win.node.(*floatingNode)
	switch fn.minimized {
	case component.AlignmentTop:
		return term.Coordinates{X: 0, Y: w.pos}
	case component.AlignmentBottom:
		return term.Coordinates{X: 0, Y: w.win.wm.height - w.pos - 1 - fn.minimizedPadding}
	case component.AlignmentLeft:
		return term.Coordinates{X: w.pos, Y: w.win.wm.minimizedOffset.Y}
	case component.AlignmentRight:
		return term.Coordinates{
			X: w.win.wm.width - w.pos - 1 - fn.minimizedPadding,
			Y: w.win.wm.minimizedOffset.Y,
		}
	default:
		panic("invalid minimize alignment")
	}
}

func (w windowPos) length() int {
	switch w.win.node.(*floatingNode).minimized {
	case component.AlignmentTop:
		return w.win.wm.width
	case component.AlignmentBottom:
		return w.win.wm.width
	case component.AlignmentLeft:
		return w.win.wm.minimizedHeight
	case component.AlignmentRight:
		return w.win.wm.minimizedHeight
	default:
		panic("invalid minimize alignment")
	}
}
