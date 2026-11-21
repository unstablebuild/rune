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
	"image"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// WindowManagerConfig represents the configuration for a WindowManager
// to be initialized.
type WindowManagerConfig struct {
	Frame            bool
	FrameAttr        term.Attributes
	ScrollBarAttr    term.Attributes
	ScrollBarCharSet component.ScrollBarCharSet
	// ScrollBarOutset stretches each image of ScrollBarCharSet.Graphics
	// over this many cells left of its own, covering them. Where the
	// frame's scroll bar charset names an image, those cells grab the
	// thumb as the bar does. Negative values are 0.
	ScrollBarOutset int
	component.FrameCharSet
	// FloatingFrameCharSet overrides the normal frame of floating windows only.
	// Nil keeps FrameCharSet; a non-nil value is used without merging empty slots.
	FloatingFrameCharSet *component.FrameCharSet
	NoMaxSize            bool
	// FloatingOwnBorder tells that floating windows without the frame
	// Frame gives them draw a border of their own around their content:
	// a window at least 3 cells wide and tall leaves the cells under its
	// outermost ring as they are, for the transparent pixels of that
	// border's images to show them, and its background treats the ring
	// as a border.
	FloatingOwnBorder bool

	// WindowBar replaces the top frame line of floating windows with a
	// solid bar drawn with WindowBarCharSet, and displays CloseIcon at
	// the top-left of the bar. It has no effect when Frame is false.
	WindowBar        bool
	WindowBarCharSet WindowBarCharSet
	CloseIcon        rune
	CloseIconAttr    term.Attributes

	// Background is the background of every window that
	// Window.SetBackground gives none of its own.
	Background WindowBackground

	// FrameGraphicsStorage caches the images of FrameCharSet.Graphics,
	// ScrollBarCharSet.Graphics, WindowBarCharSet.Graphics and the
	// windows' background graphics. Without it, or without Interrupter,
	// frames and window bars draw their runes only, and backgrounds their
	// colour only.
	FrameGraphicsStorage storageapi.Service
	// Interrupter is signalled each time an image of the frames'
	// graphics loads, so the host repaints with it.
	Interrupter term.Interrupter
}

// WindowBarCharSet is the set of characters used to draw the window
// bar over a floating window's top frame line.
type WindowBarCharSet struct {
	Left, Horizontal, Right rune

	// Graphics names an image for each slot, which writers that can draw
	// images show in place of its rune, under the close icon and title.
	Graphics WindowBarGraphics
}

// WindowBarGraphics names an image for each slot of a WindowBarCharSet:
// Left for the bar's first cell, Right for its last and Horizontal for
// every cell between. An empty URI leaves the slot to its rune.
type WindowBarGraphics struct {
	Left, Horizontal, Right string

	// Outset stretches the Left and Right images past their cells.
	Outset WindowBarOutset

	// Overlay draws each image over its slot's rune, which shows through
	// the image's transparent pixels, and keeps the bar's colors behind
	// the close icon and title. Otherwise each image replaces its rune,
	// and the icon and title sit on the frame's background.
	Overlay bool
}

// Empty reports whether g names no image, whatever its Outset and
// Overlay.
func (g WindowBarGraphics) Empty() bool {
	return g.Left == "" && g.Horizontal == "" && g.Right == ""
}

// WindowBarOutset is how far the images of a window bar's ends reach past
// their cells: Top rows above the bar, Bottom rows down the window's
// sides, Sides cells outward and Inward cells toward the bar's other end.
// HorizontalTop is how many rows above the bar each Horizontal image
// reaches, as one column. They cover whatever is drawn there before them,
// including the close icon, title and window content, are clipped at the
// window manager's edges, and change nothing the pointer reaches. Negative
// values are 0.
type WindowBarOutset struct {
	Top, Bottom, Sides, Inward, HorizontalTop int
}

// WindowBackground is what a window shows wherever its content, frame,
// scroll bar and window bar leave the default background. Graphics'
// images show through those cells, and the cells of the slices that
// show no image take Attr.Bg. Attr.Fg replaces the default foreground of
// those cells, image or not. A cell with AttrReverse keeps its colours.
// Attr's other attributes are ignored.
type WindowBackground struct {
	Attr     term.Attributes
	Graphics BackgroundGraphics
}

// Empty reports whether b leaves its window as the window draws itself.
func (b WindowBackground) Empty() bool {
	return b.Attr.Fg == term.ColorDefault && b.Attr.Bg == term.ColorDefault &&
		b.Graphics.Empty()
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

	graphics *FrameGraphics

	// bgWriter and bgBare are kept to draw backgrounds without
	// allocating.
	bgWriter backgroundWriter
	bgBare   []image.Rectangle
	masks    FrameMasks
	// barText are the images under the text of the window bar being
	// drawn, which drawWindowBar leaves to drawBarText.
	barText []deferredImage
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
		win.moved = floatingLayout.Moved
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
// A window with a background, or a floating window with images in its
// border, is cleared first and draws it last.
func (wm *WindowManager) DrawWindow(win Window, w term.Writer) {
	if wm.minimizedDirty {
		wm.Resize(wm.width, wm.height)
	}

	t, ok := wm.backgroundTarget(win)
	if !ok {
		wm.drawWindow(win, w)
		wm.drawBarText(w)
		return
	}
	wm.bgWriter.begin(w, t, &wm.masks)
	wm.drawWindow(win, &wm.bgWriter)
	wm.drawBackground(t)
}

// backgroundTarget is where a window draws its background, in cells of
// the window manager's writer.
type backgroundTarget struct {
	bg WindowBackground
	// area is the cells the window covers, and interior those of them
	// it clears, which leaves the cells under a floating window's border
	// showing through its images.
	area, interior image.Rectangle
	// framed reports that a border takes the outermost ring of area.
	framed bool
	// under reports that the cells under the border show what is under
	// the window, as those of a floating window do.
	under bool
}

// backgroundTarget returns where win draws its background, or false if
// it has none to draw: no background, and no images in a border over
// what is under it, whose lines take the default background inside.
func (wm *WindowManager) backgroundTarget(win Window) (backgroundTarget, bool) {
	t := backgroundTarget{bg: win.Background()}
	switch n := win.node.(type) {
	case *floatingNode:
		if n.minimized != 0 {
			return backgroundTarget{}, false
		}
		t.area = image.Rect(0, 0, n.realWidth, n.realHeight).Add(
			image.Pt(n.realOffset.X, n.realOffset.Y))
		t.interior = t.area
		if frame, ok := n.bordered(); ok {
			pos := frame.ContentPosition()
			w, h := frame.ContentSize()
			t.interior = image.Rect(pos.X, pos.Y, pos.X+w, pos.Y+h).Add(t.area.Min)
			t.framed, t.under = true, true
		} else if inner, ok := n.ownBorderInterior(); ok {
			t.interior, t.framed, t.under = inner, true, true
		}
	case *TileNode:
		pos := n.Position()
		t.area = image.Rect(pos.X, pos.Y, pos.X+n.width, pos.Y+n.height)
		t.interior = t.area
		t.framed = wm.config.Frame
	}
	if t.bg.Empty() && (!t.under || wm.graphics == nil) {
		return backgroundTarget{}, false
	}
	screen := image.Rect(0, 0, wm.minimizedWidth, wm.minimizedHeight)
	offset := image.Pt(wm.minimizedOffset.X, wm.minimizedOffset.Y)
	t.area = t.area.Intersect(screen).Add(offset)
	t.interior = t.interior.Intersect(screen).Add(offset)
	return t, !t.area.Empty()
}

// drawBackground draws t.bg over t.area of the writer wm.bgWriter
// recorded a window on: its images under every cell's background, and
// its colours over the cells left with the default ones, under the gaps
// shifted cells leave and inside the lines of its border's images. The
// images under the window bar's text go over its images.
func (wm *WindowManager) drawBackground(t backgroundTarget) {
	bg, area := t.bg, t.area
	w := wm.bgWriter.Writer
	bare := wm.bgBare[:0]
	if wm.graphics != nil && !bg.Graphics.Empty() {
		bare = wm.graphics.DrawBackground(w, bg.Graphics, area, bare)
	} else {
		bare = append(bare, area)
	}
	wm.bgBare = bare
	// The bar's text keeps what is under the window, which a writer
	// shows under the text along with the images drawn over it after.
	wm.drawBarText(&wm.bgWriter)
	// The default background is what a tile's border shows outside its
	// lines already.
	if bg.Attr.Bg != term.ColorDefault || t.under {
		wm.fillBorderInsides(bg.Attr.Bg, area, bare)
		wm.fillSpills(area, bare)
	}
	if bg.Attr.Fg == term.ColorDefault && bg.Attr.Bg == term.ColorDefault {
		return
	}
	if bg.Attr.Bg != term.ColorDefault {
		wm.fillShiftedGaps(bg.Attr.Bg, area, bare)
	}
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			d := wm.bgWriter.cells[(y-area.Min.Y)*area.Dx()+x-area.Min.X]
			if d&cellDefaultBg == 0 || d&(cellSprite|cellMasked) == cellSprite|cellMasked {
				continue
			}
			var attr term.Attributes
			if d&cellDefaultFg != 0 {
				attr.Fg = bg.Attr.Fg
			}
			p := image.Pt(x, y)
			if slices.ContainsFunc(bare, p.In) {
				attr.Bg = bg.Attr.Bg
			}
			if attr != (term.Attributes{}) {
				w.UnionAttributes(term.Coordinates{X: x, Y: y}, attr)
			}
		}
	}
}

// fillBorderInsides paints c inside the lines the images of the border
// draw through the cells of area in bare. The cells under them are left
// out of the window's colour, so that the side of each line away from
// the window shows what is under it.
func (wm *WindowManager) fillBorderInsides(c term.Color, area image.Rectangle, bare []image.Rectangle) {
	w := &wm.bgWriter
	for i, sprite := range w.sprites {
		if w.cells[i]&(cellSprite|cellMasked) != cellSprite {
			continue
		}
		p := image.Pt(area.Min.X+i%area.Dx(), area.Min.Y+i/area.Dx())
		if !slices.ContainsFunc(bare, p.In) {
			continue
		}
		sides := RingSides(p, area)
		fill, ok := wm.masks.Inside(sprite, sides, c)
		if !ok || !w.Writer.DrawImage(fill) {
			continue
		}
		w.cells[i] |= cellMasked
	}
}

// fillSpills paints again the background of the window's own cells that
// the renderer moves half a cell into the top or bottom row of area,
// where what goes under the text covers every cell's background: the
// insides fillBorderInsides paints, and the window's background images
// over the cells that keep what is under the window, which go under the
// text over the pictures those cells keep. The background stays on top,
// as the moved cells' text does.
func (wm *WindowManager) fillSpills(area image.Rectangle, bare []image.Rectangle) {
	w := &wm.bgWriter
	for y := area.Min.Y; y < area.Max.Y; y += max(1, area.Dy()-1) {
		for x := area.Min.X; x < area.Max.X; x++ {
			p := image.Pt(x, y)
			i, _ := w.index(term.Coordinates{X: x, Y: y})
			if w.cells[i]&cellMasked == 0 &&
				(w.cells[i]&cellKept == 0 || slices.ContainsFunc(bare, p.In)) {
				continue
			}
			sides := RingSides(p, area)
			n, ok := w.spill(i, sides)
			if !ok || w.cells[n]&cellDefaultBg != 0 || w.bg[n] == term.ColorDefault {
				continue
			}
			w.Writer.DrawImage(term.Image{
				Fill: true, FillColor: w.bg[n],
				Pos:   term.Coordinates{X: area.Min.X + n%area.Dx(), Y: area.Min.Y + n/area.Dx()},
				Width: 1, Height: 1,
				Layer:                        term.ImageLayerBelowText,
				VerticalRenderOffset:         sides == SideBottom,
				NegativeVerticalRenderOffset: sides == SideTop,
			})
		}
	}
}

// fillShiftedGaps paints c under the runs of cells of area in bare that
// the renderer moves half a cell up or down. Moving a cell moves its
// background with it, and the part of its row it leaves would show the
// writer's default background rather than the window's. The fill goes
// under every background, so it only shows in that gap.
func (wm *WindowManager) fillShiftedGaps(c term.Color, area image.Rectangle, bare []image.Rectangle) {
	w := wm.bgWriter.Writer
	for y := area.Min.Y; y < area.Max.Y; y++ {
		start := -1
		for x := area.Min.X; x <= area.Max.X; x++ {
			var d cellDefaults
			if x < area.Max.X {
				d = wm.bgWriter.cells[(y-area.Min.Y)*area.Dx()+x-area.Min.X]
			}
			shifted := d&(cellShifted|cellFilled) == cellShifted &&
				slices.ContainsFunc(bare, image.Pt(x, y).In)
			switch {
			case shifted && start < 0:
				start = x
			case !shifted && start >= 0:
				w.DrawImage(term.Image{
					Fill: true, FillColor: c,
					Pos:   term.Coordinates{X: start, Y: y},
					Width: x - start, Height: 1,
					Layer: term.ImageLayerBelowBackground,
				})
				start = -1
			}
		}
	}
}

// cellDefaults records which of a cell's colours are the default, and
// whether the renderer moves the cell and which way, its content fills
// under it, or an image of the border covers it, whether a fill paints
// the inside of that image's line, whether the cell still shows what is
// under the window, untouched, and whether it keeps it under a cell
// written with AttrKeepBackground.
type cellDefaults uint16

const (
	cellDefaultBg cellDefaults = 1 << iota
	cellDefaultFg
	cellShifted
	// cellShiftedUp is set along with cellShifted for a cell moved up.
	cellShiftedUp
	cellFilled
	cellSprite
	cellMasked
	cellUnder
	cellKept
)

// shiftFlags returns how the renderer moves a cell that was moved as
// prev records and now carries attrs: down when attrs moves it down, as
// that wins over moving up for a cell that carries both.
func shiftFlags(prev cellDefaults, attrs term.AttrMask) cellDefaults {
	prev &= cellShifted | cellShiftedUp
	switch {
	case attrs&term.AttrVerticalRenderOffset != 0:
		return cellShifted
	case attrs&term.AttrNegativeVerticalRenderOffset != 0 && prev != cellShifted:
		return cellShifted | cellShiftedUp
	}
	return prev
}

// backgroundWriter forwards everything to Writer, and records, for each
// cell of area, which of its colours were last left the default and its
// background, and the images over the cells of its outermost ring when a
// border takes it.
type backgroundWriter struct {
	term.Writer
	area    image.Rectangle
	cells   []cellDefaults
	bg      []term.Color
	framed  bool
	sprites map[int]term.Image
	masks   *FrameMasks
}

// begin records t.area of dst from now on, after clearing t.interior to
// blank cells with the default colours. The cells of t.area outside it
// are recorded with the default colours and left as they are, showing
// what is under the window when t.under says they do. masks derive the
// inside of the lines of the border's images.
func (w *backgroundWriter) begin(dst term.Writer, t backgroundTarget, masks *FrameMasks) {
	w.Writer, w.area, w.framed, w.masks = dst, t.area, t.framed, masks
	n := t.area.Dx() * t.area.Dy()
	w.cells = slices.Grow(w.cells[:0], n)[:n]
	w.bg = slices.Grow(w.bg[:0], n)[:n]
	clear(w.bg)
	clear(w.sprites)
	ring := cellDefaultBg | cellDefaultFg
	if t.under {
		ring |= cellUnder
	}
	for y := t.area.Min.Y; y < t.area.Max.Y; y++ {
		for x := t.area.Min.X; x < t.area.Max.X; x++ {
			if image.Pt(x, y).In(t.interior) {
				w.SetCell(term.Coordinates{X: x, Y: y}, term.Cell{})
				continue
			}
			i, _ := w.index(term.Coordinates{X: x, Y: y})
			w.cells[i] = ring
		}
	}
}

func (w *backgroundWriter) index(pos term.Coordinates) (int, bool) {
	if !image.Pt(pos.X, pos.Y).In(w.area) {
		return 0, false
	}
	return (pos.Y-w.area.Min.Y)*w.area.Dx() + pos.X - w.area.Min.X, true
}

// spill returns the cell next to the cell at i of the outermost ring,
// whose sides face away from area, that the renderer moves half a cell
// into it: the one above a cell of the bottom row moved down, or the one
// below a cell of the top row moved up.
func (w *backgroundWriter) spill(i int, sides Sides) (int, bool) {
	switch sides {
	case SideBottom:
		n := i - w.area.Dx()
		return n, w.cells[n]&(cellShifted|cellShiftedUp) == cellShifted
	case SideTop:
		n := i + w.area.Dx()
		return n, w.cells[n]&(cellShifted|cellShiftedUp) == cellShifted|cellShiftedUp
	}
	return 0, false
}

// SetCell satisfies term.Writer. A cell with AttrKeepBackground keeps
// what was recorded of the background it keeps.
func (w *backgroundWriter) SetCell(pos term.Coordinates, c term.Cell) {
	if i, ok := w.index(pos); ok {
		d := w.cells[i] & cellDefaultBg
		if c.Attrs&term.AttrKeepBackground != 0 {
			if w.cells[i]&(cellUnder|cellKept) != 0 {
				d |= cellKept
			}
		} else {
			d = 0
			if c.Bg == term.ColorDefault {
				d = cellDefaultBg
			}
			w.bg[i] = c.Bg
		}
		if c.Fg == term.ColorDefault {
			d |= cellDefaultFg
		}
		if c.Attrs&term.AttrReverse != 0 {
			d = 0
			w.bg[i] = c.Fg
		}
		w.cells[i] = d | shiftFlags(0, c.Attrs)
	}
	w.Writer.SetCell(pos, c)
}

// UnionAttributes satisfies term.Writer.
func (w *backgroundWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	if i, ok := w.index(pos); ok {
		if attr.Bg != term.ColorDefault {
			w.cells[i] &^= cellDefaultBg
			w.bg[i] = attr.Bg
		}
		if attr.Fg != term.ColorDefault {
			w.cells[i] &^= cellDefaultFg
		}
		if attr.Attrs&term.AttrReverse != 0 {
			// The background is now the foreground, which is not recorded.
			w.cells[i] &= cellShifted | cellShiftedUp | cellUnder
			w.bg[i] = term.ColorDefault
		}
		w.cells[i] = w.cells[i]&^(cellShifted|cellShiftedUp) | shiftFlags(w.cells[i], attr.Attrs)
	}
	w.Writer.UnionAttributes(pos, attr)
}

// DrawImage satisfies term.Writer. It records the cells a fill under
// every background covers, which already paints the gap a shifted cell
// leaves, and the images of a border over a cell of the outermost ring,
// along with the fills the window draws inside their lines itself.
// Before an image of the border whose line has an inside, over a cell
// that still shows what is under the window, it blanks the cell, keeping
// its background and the pictures under it: the text and the pictures
// above it under the window would otherwise go over the inside of the
// line and any of the window's content moved into the cell.
func (w *backgroundWriter) DrawImage(img term.Image) bool {
	var ring Sides
	if w.framed && img.Width == 1 && img.Height == 1 {
		ring = RingSides(image.Pt(img.Pos.X, img.Pos.Y), w.area)
	}
	if ring != 0 && !img.Fill && img.Layer == term.ImageLayerAboveText {
		if i, _ := w.index(img.Pos); w.cells[i]&cellUnder != 0 {
			if _, ok := w.masks.Inside(img, ring, term.ColorDefault); ok {
				w.SetCell(img.Pos, term.NewCell(' ', 1, term.Attributes{Attrs: term.AttrKeepBackground}))
			}
		}
	}
	ok := w.Writer.DrawImage(img)
	if !ok {
		return ok
	}
	if ring != 0 {
		i, _ := w.index(img.Pos)
		switch {
		case !img.Fill && img.Layer == term.ImageLayerAboveText:
			w.cells[i] |= cellSprite
			if w.sprites == nil {
				w.sprites = make(map[int]term.Image)
			}
			w.sprites[i] = img
		case img.Fill && img.Src != nil && img.Layer == term.ImageLayerBelowText &&
			w.cells[i]&cellSprite != 0:
			w.cells[i] |= cellMasked
		}
	}
	if !img.Fill || img.Layer != term.ImageLayerBelowBackground {
		return ok
	}
	covered := img.Visible().Intersect(w.area)
	for y := covered.Min.Y; y < covered.Max.Y; y++ {
		for x := covered.Min.X; x < covered.Max.X; x++ {
			i, _ := w.index(term.Coordinates{X: x, Y: y})
			w.cells[i] |= cellFilled
		}
	}
	return ok
}

func (wm *WindowManager) drawWindow(win Window, w term.Writer) {
	nonMinimizedW := wm.nonMinimized(w)

	f, ok := win.node.(*floatingNode)
	if !ok {
		wm.tree.DrawTile(win.node.(*TileNode), &nonMinimizedW)
		wm.drawFrameGraphics(win, &nonMinimizedW)
		wm.stitchFrame(win, &nonMinimizedW, false)
		return
	}

	if f.minimized == 0 {
		f.Draw(&nonMinimizedW)
		wm.drawFloatingBorder(f, &nonMinimizedW)
		wm.stitchFrame(win, &nonMinimizedW, wm.hasWindowBar(f))
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

// nonMinimized returns a writer over the part of w that the windows
// minimized at its edges leave to the others.
func (wm *WindowManager) nonMinimized(w term.Writer) component.VirtualWriter {
	return component.VirtualWriter{
		Writer: w,
		Offset: wm.minimizedOffset,
		Height: wm.minimizedHeight,
		Width:  wm.minimizedWidth,
	}
}

// drawFrameGraphics draws the images of win's frame over its border and
// scroll bar.
func (wm *WindowManager) drawFrameGraphics(win Window, w term.Writer) {
	if wm.graphics == nil || !wm.config.Frame {
		return
	}
	width, height := win.node.Width(), win.node.Height()
	vw := &component.VirtualWriter{
		Writer: w, Offset: win.node.Position(), Width: width, Height: height,
	}
	frame := win.node.Content().(*component.Frame)
	wm.graphics.Draw(vw, frame, width, height)
	wm.graphics.DrawScrollBar(vw, frame, width, height, wm.config.ScrollBarOutset)
}

// drawFloatingBorder draws the border of f's frame, which f leaves to it,
// with its images over whatever is drawn under the window, and the
// images of its scroll bar. The top row is left to the window bar.
func (wm *WindowManager) drawFloatingBorder(f *floatingNode, w term.Writer) {
	frame, ok := f.bordered()
	if !ok {
		return
	}
	f.borderWriter = component.VirtualWriter{
		Writer: w, Offset: f.realOffset, Width: f.realWidth, Height: f.realHeight,
	}
	vw := &f.borderWriter
	wm.graphics.drawBorder(vw, frame, f.realWidth, f.realHeight, wm.hasWindowBar(f))
	if wm.graphics != nil {
		wm.graphics.DrawScrollBar(vw, frame, f.realWidth, f.realHeight, wm.config.ScrollBarOutset)
	}
}

// FrameGraphics returns the images that the frames' graphics are drawn
// with, to draw the same images elsewhere. It is nil when the config
// has no FrameGraphicsStorage or Interrupter, which draws no images.
func (wm *WindowManager) FrameGraphics() *FrameGraphics {
	return wm.graphics
}

// FrameStitcher is the content of a window that joins rules of its own
// to the frame the WindowManager draws around it.
type FrameStitcher interface {
	// StitchFrame draws over the frame f drew width by height at 0,0 of
	// w, after the frame's images, which g draws unless it is nil. bar
	// reports that a window bar takes the frame's top row, which
	// StitchFrame must then leave alone.
	StitchFrame(w term.Writer, f *component.Frame, g *FrameGraphics, width, height int, bar bool)
}

// stitchFrame lets win's content join its rules to win's frame.
func (wm *WindowManager) stitchFrame(win Window, w term.Writer, bar bool) {
	if !wm.config.Frame {
		return
	}
	frame := win.node.Content().(*component.Frame)
	s, ok := frame.Content().(FrameStitcher)
	if !ok {
		return
	}
	width, height := win.node.Width(), win.node.Height()
	s.StitchFrame(&component.VirtualWriter{
		Writer: w, Offset: win.node.Position(), Width: width, Height: height,
	}, frame, wm.graphics, width, height, bar)
}

// Close releases the images of the frames' graphics. It does not close
// the windows.
func (wm *WindowManager) Close() error {
	if wm.graphics == nil {
		return nil
	}
	return wm.graphics.Close()
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
	if config.FrameGraphicsStorage != nil && config.Interrupter != nil {
		wm.graphics = NewFrameGraphics(config.FrameGraphicsStorage, config.Interrupter)
	}

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
// Calling this method with height=0 resets the height: a floating window takes its
// content's height, and a tile shares the height evenly with its siblings.
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
// Calling this method with width=0 resets the width: a floating window takes its
// content's width, and a tile its default width, set with SetDefaultWidth, or an even
// share with its siblings when it has none or the default does not fit.
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

// SetDefaultWidth sets the width the tile win is reset to, by SetWidth with 0 or by
// ResetTileEdge, and fixes it to that width as SetWidth does. The default is kept even
// when the width cannot be fixed now, and a width of 0 clears it. It returns false if
// win is floating, whose default is its content's width, or if the width cannot be
// fixed.
func (wm *WindowManager) SetDefaultWidth(win Window, width int) bool {
	t, ok := win.node.(*TileNode)
	if !ok || wm.config.Frame && width < 3 && width > 0 {
		return false
	}
	return t.SetDefaultWidth(width)
}

// MoveTileEdge moves the divider along the right edge of the tile win, or along its
// bottom edge for component.AlignmentBottom, onto the column or row to, in the coordinate
// space of Window.Position. It returns false if win is floating, edge is neither
// AlignmentRight nor AlignmentBottom, the edge is the manager's, or the move would leave
// a tile too small.
func (wm *WindowManager) MoveTileEdge(win Window, edge component.Alignment, to int) bool {
	t, ok := win.node.(*TileNode)
	if !ok {
		return false
	}
	dir, ok := edgeSplit(edge)
	if !ok {
		return false
	}
	split, idx, ok := t.divider(dir)
	if !ok {
		return false
	}
	// win's edge is the one owner ends at, so owner changes size by as
	// much as the edge moves.
	owner := split.children[idx].C
	pos := win.Position()
	size := owner.width + to - (pos.X + t.width - 1)
	if dir == horizontal {
		size = owner.height + to - (pos.Y + t.height - 1)
	}
	if size <= 0 || wm.config.Frame && size < 3 {
		return false
	}
	if dir == horizontal {
		return owner.SetFixedHeight(size)
	}
	return owner.SetFixedWidth(size)
}

// ResetTileEdge resets, as SetWidth or SetHeight with 0 do, the sizes of the two tiles
// that meet at the divider along the right edge of the tile win, or along its bottom
// edge for component.AlignmentBottom. As in MoveTileEdge, the divider may separate
// ancestors of win. It returns false if win is floating, edge is neither AlignmentRight
// nor AlignmentBottom, or the edge is the manager's.
func (wm *WindowManager) ResetTileEdge(win Window, edge component.Alignment) bool {
	t, ok := win.node.(*TileNode)
	if !ok {
		return false
	}
	dir, ok := edgeSplit(edge)
	if !ok {
		return false
	}
	split, idx, ok := t.divider(dir)
	if !ok {
		return false
	}
	split.resetDivider(idx)
	return true
}

func edgeSplit(edge component.Alignment) (splitDir, bool) {
	switch edge {
	case component.AlignmentRight:
		return vertical, true
	case component.AlignmentBottom:
		return horizontal, true
	}
	return noSplit, false
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
	inFloatingArea := wm.inFloatingArea(pos)
	for i, fw := range wm.float {
		if fw.minimized != 0 || !inFloatingArea {
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

// InFloatingArea reports whether pos, in the coordinate space of
// Window.Position, lies where floating windows are drawn: inside the
// manager and off the bars of minimized windows. Floating windows
// moved past an edge are clipped to this area.
func (wm *WindowManager) InFloatingArea(pos term.Coordinates) bool {
	if wm.minimizedDirty {
		wm.Resize(wm.width, wm.height)
	}
	return wm.inFloatingArea(pos)
}

func (wm *WindowManager) inFloatingArea(pos term.Coordinates) bool {
	pos.X -= wm.minimizedOffset.X
	pos.Y -= wm.minimizedOffset.Y
	return pos.X >= 0 && pos.Y >= 0 &&
		pos.X < wm.minimizedWidth && pos.Y < wm.minimizedHeight
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
		w.SetFrameCharSet(wm.normalFrameCharSet(w))
	})
}

func (wm *WindowManager) normalFrameCharSet(w Window) component.FrameCharSet {
	if w.IsFloating() && wm.config.FloatingFrameCharSet != nil {
		return *wm.config.FloatingFrameCharSet
	}
	return wm.config.FrameCharSet
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
	// Offset is to be applied to the position of the window after
	// alignment has been determined. A position before the manager's
	// top-left corner is clamped to it, and the window is trimmed to
	// the space left after its position.
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
	if wm.config.Frame {
		f.Content().(*component.Frame).FrameCharSet = wm.normalFrameCharSet(wm.nodeToWindow(f))
	}
	wm.applyWindowBar(f)
	wm.float = append(wm.float, f)
	wm.minimizedDirty = true
	return wm.nodeToWindow(f)
}

// Restyled returns c with the style of s: its frame, scroll bar and
// window bar attributes and charsets, ScrollBarOutset, close icon,
// Background and whether floating windows draw the window bar, which
// takes their top frame row either way. What changes the layout or how
// images load, Frame, NoMaxSize, FrameGraphicsStorage and Interrupter,
// stays c's.
func (c WindowManagerConfig) Restyled(s WindowManagerConfig) WindowManagerConfig {
	c.FrameAttr = s.FrameAttr
	c.FrameCharSet = s.FrameCharSet
	c.FloatingFrameCharSet = s.FloatingFrameCharSet
	c.ScrollBarAttr = s.ScrollBarAttr
	c.ScrollBarCharSet = s.ScrollBarCharSet
	c.ScrollBarOutset = s.ScrollBarOutset
	c.WindowBar = s.WindowBar
	c.WindowBarCharSet = s.WindowBarCharSet
	c.CloseIcon = s.CloseIcon
	c.CloseIconAttr = s.CloseIconAttr
	c.Background = s.Background
	return c
}

// Restyle draws wm, from the next Draw on, with the style of cfg, as
// WindowManagerConfig.Restyled takes it, and opens new windows with it.
// A window whose frame attribute was set with Window.SetFrameAttr keeps
// it unless cfg changes FrameAttr.
func (wm *WindowManager) Restyle(cfg WindowManagerConfig) {
	prev := wm.config
	wm.config = wm.config.Restyled(cfg)
	if !wm.config.Frame {
		return
	}
	wm.Iterate(func(w Window) {
		w.SetFrameCharSet(wm.normalFrameCharSet(w))
		f := w.node.Content().(*component.Frame)
		f.ScrollBarAttributes = wm.config.ScrollBarAttr
		f.ScrollBarCharSet = wm.config.ScrollBarCharSet
		if wm.config.FrameAttr != prev.FrameAttr {
			f.Attributes = wm.config.FrameAttr
		}
	})
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
// configured window bar characters, which the bar keeps over any
// graphics.
func (wm *WindowManager) barCharSet(cs component.FrameCharSet, f *floatingNode) component.FrameCharSet {
	bar := wm.windowBarCharSet(f)
	cs.TopLeft = bar.Left
	cs.HorizontalTop = bar.Horizontal
	cs.TopRight = bar.Right
	cs.Graphics.TopLeft = ""
	cs.Graphics.HorizontalTop = ""
	cs.Graphics.TopRight = ""
	return cs
}

func (wm *WindowManager) windowBarCharSet(f *floatingNode) WindowBarCharSet {
	if f.barCharSet != nil {
		return *f.barCharSet
	}
	return wm.config.WindowBarCharSet
}

// applyWindowBar overrides the top frame chars of a floating node's
// frame with the window bar charset when the bar is enabled.
func (wm *WindowManager) applyWindowBar(f *floatingNode) {
	if !wm.hasWindowBar(f) {
		return
	}
	frame := f.Content().(*component.Frame)
	frame.FrameCharSet = wm.barCharSet(frame.FrameCharSet, f)
}

func (wm *WindowManager) hasWindowBar(f *floatingNode) bool {
	return wm.config.Frame && wm.config.WindowBar && !f.noBar
}

// drawWindowBar draws a floating window's bar over the top row of its
// frame, which drawFloatingBorder leaves to it: the close icon and the
// window title over the bar's images, and each other cell's image over
// whatever is drawn under the window, or its rune. It leaves the images
// under the text to drawBarText.
func (wm *WindowManager) drawWindowBar(f *floatingNode, w term.Writer) {
	if !wm.hasWindowBar(f) || f.realWidth < 3 || f.realHeight < 3 {
		return
	}
	frame := f.Content().(*component.Frame)
	text := wm.windowBarText(f, frame.Attributes)
	gr := wm.windowBarCharSet(f).Graphics
	if wm.graphics == nil {
		gr = WindowBarGraphics{}
	}
	limitX := f.realWidth - 1
	// cw escapes, so it is allocated by the first image only.
	var cw *cellImageWriter
	draw := func(uri string, ch rune, x int, area image.Rectangle) {
		at := term.Coordinates{X: f.realOffset.X + x, Y: f.realOffset.Y}
		area = area.Add(image.Pt(at.X, at.Y))
		cell := term.NewCell(ch, 1, frame.Attributes)
		i := slices.IndexFunc(text, func(c windowBarCell) bool { return c.x == x })
		if uri != "" && cw == nil {
			cw = &cellImageWriter{Writer: w, deferred: &wm.barText}
		}
		switch {
		case uri == "" && i >= 0:
			w.SetCell(at, text[i].solid)
		case uri == "":
			w.SetCell(at, cell)
		case i >= 0:
			cw.under, cw.fallback = text[i].onImage, text[i].solid
			if gr.Overlay {
				cw.under = text[i].solid
			}
			cw.keep = false
			cw.layer = term.ImageLayerBelowText
			if !wm.graphics.drawArea(cw, uri, at, area) {
				w.SetCell(at, text[i].solid)
			}
		default:
			if gr.Overlay {
				under := cell
				under.Attrs |= term.AttrKeepBackground
				w.SetCell(at, under)
			}
			cw.keep = true
			cw.layer = term.ImageLayerAboveText
			if !wm.graphics.drawArea(cw, uri, at, area) && !gr.Overlay {
				w.SetCell(at, cell)
			}
		}
	}
	o := gr.Outset
	column := image.Rect(0, -max(0, o.HorizontalTop), 1, 1)
	for x := 1; x < limitX; x++ {
		draw(gr.Horizontal, frame.HorizontalTop, x, column)
	}
	// The ends go last so that no cell of the bar covers their outsets.
	top, bottom := max(0, o.Top), max(0, o.Bottom)
	sides, inward := max(0, o.Sides), max(0, o.Inward)
	draw(gr.Left, frame.TopLeft, 0, image.Rect(-sides, -top, 1+inward, 1+bottom))
	draw(gr.Right, frame.TopRight, limitX, image.Rect(-inward, -top, 1+sides, 1+bottom))
}

// drawBarText draws through w the images drawWindowBar left under the
// text of a bar, and the cells they replace where w draws no image.
func (wm *WindowManager) drawBarText(w term.Writer) {
	vw := wm.nonMinimized(w)
	for _, d := range wm.barText {
		if !vw.DrawImage(d.img) {
			vw.SetCell(d.at, d.fallback)
		}
	}
	// Drop the Src references along with the placements.
	clear(wm.barText)
	wm.barText = wm.barText[:0]
}

// windowBarCell is a cell of a window bar's close icon or title, x cells
// from the bar's left end.
type windowBarCell struct {
	x int
	// solid is the cell over the bar's runes, which takes the bar's
	// foreground as its background so it reads as part of the bar.
	solid term.Cell
	// onImage is the cell over an image that replaces the bar's runes,
	// on whatever is under the window, so that the image's transparent
	// pixels show it like those of the rest of the bar.
	onImage term.Cell
}

// windowBarText returns the cells of f's close icon and title, from left
// to right.
func (wm *WindowManager) windowBarText(
	f *floatingNode, barAttr term.Attributes,
) (ret []windowBarCell) {
	if wm.config.CloseIcon != 0 {
		solid, onImage := wm.config.CloseIconAttr, wm.config.CloseIconAttr
		solid.Bg = barAttr.Fg
		onImage.Bg, onImage.Attrs = term.ColorDefault, onImage.Attrs|term.AttrKeepBackground
		ret = append(ret, windowBarCell{
			x:       WindowBarCloseIconX,
			solid:   term.NewCell(wm.config.CloseIcon, 1, solid),
			onImage: term.NewCell(wm.config.CloseIcon, 1, onImage),
		})
	}
	if f.title == "" {
		return ret
	}
	// interior bar cells span [1, realWidth-2]; keep one bar cell
	// after the close icon so the icon stays distinguishable.
	minX := WindowBarCloseIconX + 2
	maxX := f.realWidth - 2
	avail := maxX - minX + 1
	if avail < 3 {
		return ret
	}
	title := []rune(" " + f.title + " ")
	if len(title) > avail {
		title = title[:avail]
	}
	solid := term.Attributes{Fg: barAttr.Bg, Bg: barAttr.Fg}
	onImage := term.Attributes{Fg: barAttr.Fg, Attrs: term.AttrKeepBackground}
	start := minX + (avail-len(title))/2
	for i, r := range title {
		ret = append(ret, windowBarCell{
			x:       start + i,
			solid:   term.NewCell(r, 1, solid),
			onImage: term.NewCell(r, 1, onImage),
		})
	}
	return ret
}

// MoveWindow moves a floating window so its top-left corner sits at
// pos, given in the same coordinate space as Window.Position. From then
// on the window keeps its size, trimmed only to the manager's, and may
// extend past the manager's edges, where it is clipped; pos is clamped
// so that its top row stays on screen with at least three cells of it
// showing. It returns false if win is not a floating window or is
// minimized.
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
	fn.moved = true
	fn.updateDesiredDimensions()
	// Store the clamped position, so that a window dragged against an
	// edge stays there when the manager grows instead of jumping to
	// wherever the drag overshot.
	fn.desiredOffset = fn.reachableOffset(pos)
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
	f.ScrollBarCharSet = wm.config.ScrollBarCharSet
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
