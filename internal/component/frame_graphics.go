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
	"cmp"
	"errors"
	"image"
	"log/slog"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/imageuri"
	"unstable.build/rune/internal/component/template"
)

// SeparatorCharSet stitches an interior horizontal rule into a frame.
// The four runes occupy columns 0, 1, width-2 and width-1; the remaining
// columns use the frame's HorizontalBottom rune.
type SeparatorCharSet struct {
	Left, HorizontalLeft, HorizontalRight, Right rune
	Graphics                                     SeparatorGraphics
}

// SeparatorGraphics names the two junction images and the repeating bar.
// Empty junction slots preserve the frame's vertical images. An empty
// horizontal slot leaves the rule's characters visible.
type SeparatorGraphics struct {
	Left, Horizontal, Right string
	Overlay                 bool
	Outset                  SeparatorOutset
}

// SeparatorOutset extends junction images above, below and into the frame
// in whole cells. Outsets cover content without changing the layout.
type SeparatorOutset struct {
	Top, Bottom, Inward int
}

// Empty reports whether no image URIs are set, regardless of mode or outsets.
func (g SeparatorGraphics) Empty() bool {
	return g.Left == "" && g.Horizontal == "" && g.Right == ""
}

// LineGraphics names the images drawn over a line of cells: Start over
// its first cell, End over its last and Middle over each cell between.
// An empty Start or End takes Middle's image, and an empty Middle leaves
// its cells to their character. A line one cell long is drawn with
// Start. Overlay keeps the character under the images rather than
// blanking it.
type LineGraphics struct {
	Start, Middle, End string
	Overlay            bool
}

// Empty reports whether g names no image, whatever its mode.
func (g LineGraphics) Empty() bool {
	return g.Start == "" && g.Middle == "" && g.End == ""
}

// URIs returns the URIs g names, empty ones included.
func (g LineGraphics) URIs() []string {
	return []string{g.Start, g.Middle, g.End}
}

// DrawCells writes cell along a line of length cells of w from from,
// rightwards or, when vertical, downwards, but, unless g is Overlay, a
// blank cell with cell's colors and attributes in place of each cell an
// image of g goes over. As a cell covers the images drawn before it, the
// images that go under the line are drawn after it, and the images of g
// last, with FrameGraphics.DrawLine.
func (g LineGraphics) DrawCells(
	w term.Writer, from term.Coordinates, length int, vertical bool, cell term.Cell,
) {
	blank := cell
	blank.Ch = ' '
	blank.Extra = nil
	for i := range length {
		c := cell
		if !g.Overlay && g.uri(i, length) != "" {
			c = blank
		}
		w.SetCell(linePos(from, i, vertical), c)
	}
}

// uri returns the image of cell i of a line of length cells.
func (g LineGraphics) uri(i, length int) string {
	switch i {
	case 0:
		return cmp.Or(g.Start, g.Middle)
	case length - 1:
		return cmp.Or(g.End, g.Middle)
	}
	return g.Middle
}

func linePos(from term.Coordinates, i int, vertical bool) term.Coordinates {
	if vertical {
		from.Y += i
	} else {
		from.X += i
	}
	return from
}

// BackgroundGraphics names the images of a window's background panel, a
// nine-slice: each corner image covers its corner cell, each edge image
// the cells of its edge between the corners, one cell thick, and Center
// every cell inside them. In an area narrower or shorter than two cells,
// Center covers every cell alone. A slice whose URI is empty shows no
// image.
type BackgroundGraphics struct {
	TopLeft, TopRight, BottomLeft, BottomRight string
	HorizontalTop, HorizontalBottom            string
	VerticalLeft, VerticalRight                string
	Center                                     string
	// Fit is how every image covers its slice. ImageFitTile repeats it
	// unscaled from the slice's top-left corner.
	Fit term.ImageFit
}

// Empty reports whether g names no image, whatever its Fit.
func (g BackgroundGraphics) Empty() bool {
	for _, uri := range g.URIs() {
		if uri != "" {
			return false
		}
	}
	return true
}

// URIs returns the URIs g names, empty ones included.
func (g BackgroundGraphics) URIs() []string {
	return []string{
		g.TopLeft, g.TopRight, g.BottomLeft, g.BottomRight,
		g.HorizontalTop, g.HorizontalBottom, g.VerticalLeft, g.VerticalRight,
		g.Center,
	}
}

// backgroundSlice is the image of a BackgroundGraphics slot and the
// cells it covers.
type backgroundSlice struct {
	uri  string
	area image.Rectangle
}

// slices returns the slices of g over area, empty ones included.
func (g BackgroundGraphics) slices(area image.Rectangle) [9]backgroundSlice {
	var ret [9]backgroundSlice
	if area.Dx() < 2 || area.Dy() < 2 {
		ret[8] = backgroundSlice{g.Center, area}
		return ret
	}
	x0, y0 := area.Min.X, area.Min.Y
	x1, y1 := area.Max.X-1, area.Max.Y-1
	ret = [9]backgroundSlice{
		{g.TopLeft, image.Rect(x0, y0, x0+1, y0+1)},
		{g.TopRight, image.Rect(x1, y0, x1+1, y0+1)},
		{g.BottomLeft, image.Rect(x0, y1, x0+1, y1+1)},
		{g.BottomRight, image.Rect(x1, y1, x1+1, y1+1)},
		{g.HorizontalTop, image.Rect(x0+1, y0, x1, y0+1)},
		{g.HorizontalBottom, image.Rect(x0+1, y1, x1, y1+1)},
		{g.VerticalLeft, image.Rect(x0, y0+1, x0+1, y1)},
		{g.VerticalRight, image.Rect(x1, y0+1, x1+1, y1)},
		{g.Center, image.Rect(x0+1, y0+1, x1, y1)},
	}
	return ret
}

// FrameGraphics draws the images that FrameCharSet.Graphics names over
// the border of a component.Frame. It keeps one image per URI, so a URI
// is fetched and decoded once and shares one ImageID across every frame
// and slot it is drawn in. The images of window backgrounds are kept per
// URI and fit, apart from those of frames.
type FrameGraphics struct {
	storage     storageapi.Service
	interrupter term.Interrupter
	images      map[string]*imageuri.Component
	backgrounds map[backgroundKey]*imageuri.Component
	// reloaded holds the images of the last Reload, which backgrounds
	// first drawn since are made from instead of fetched.
	reloaded map[string]image.Image
}

type backgroundKey struct {
	uri string
	fit term.ImageFit
}

var frameImageStyle = imageuri.Style{
	Fit:         term.ImageFitFill,
	Layer:       term.ImageLayerAboveText,
	HideLoading: true,
}

// NewFrameGraphics returns a FrameGraphics that caches the downloaded
// images in storage and signals interrupter when one settles.
func NewFrameGraphics(
	storage storageapi.Service, interrupter term.Interrupter,
) *FrameGraphics {
	return &FrameGraphics{
		storage:     storage,
		interrupter: interrupter,
		images:      make(map[string]*imageuri.Component),
		backgrounds: make(map[backgroundKey]*imageuri.Component),
	}
}

// Draw draws, over the border that f draws when it is width x height at
// 0,0 of w, the images of f.FrameCharSet.Graphics, each cell of the
// border its part of the image component.FrameCharSetGraphics says it
// shows, over a blank cell with f.Attributes, or over the rune f drew
// there when the graphics are Overlay. Cells under the scroll bar, and
// the cells of an image that is not loaded yet, failed to load or that w
// cannot draw keep their rune. It draws nothing when width or height is
// below 3, where f draws no border.
func (g *FrameGraphics) Draw(
	w term.Writer, f *component.Frame, width, height int,
) {
	g.draw(w, f, width, height, false)
}

// DrawOver draws the images of f's graphics as Draw does, but over a
// blank cell that keeps the background of the cell under it rather than
// taking f's, so that each image's transparent pixels show what is under
// the frame. The cells whose image does not draw take f's rune and
// colours. Drawing the frame through a FrameUnderWriter first keeps that
// background in the cells under the images.
func (g *FrameGraphics) DrawOver(
	w term.Writer, f *component.Frame, width, height int,
) {
	g.draw(w, f, width, height, true)
}

func (g *FrameGraphics) draw(
	w term.Writer, f *component.Frame, width, height int, over bool,
) {
	gr := f.Graphics
	if width < 3 || height < 3 || gr.Empty() {
		return
	}
	limitX, limitY := width-1, height-1
	sbPos, sbHeight, sb := f.ScrollBar()

	cw := cellImageWriter{
		Writer: w,
		under:  term.NewCell(' ', 1, f.Attributes),
		keep:   gr.Overlay,
	}
	if over {
		cw.under.Attrs |= term.AttrKeepBackground
	}
	draw := func(ch rune, x, y int) {
		c := frameCellAt(gr, x, y, width, height)
		if c.uri == "" ||
			(sb && x == limitX && y >= sbPos.Y && y < sbPos.Y+sbHeight) {
			return
		}
		cw.fallback = term.NewCell(ch, 1, f.Attributes)
		g.drawFrameCell(&cw, c, term.Coordinates{X: x, Y: y})
	}

	// Each side is drawn on its own, so that a renderer drawing images
	// of one picture together draws a side at once.
	for x := 1; x < limitX; x++ {
		draw(f.HorizontalTop, x, 0)
	}
	for x := 1; x < limitX; x++ {
		draw(f.HorizontalBottom, x, limitY)
	}
	for y := 1; y < limitY; y++ {
		draw(f.VerticalLeft, 0, y)
	}
	for y := 1; y < limitY; y++ {
		draw(f.VerticalRight, limitX, y)
	}
	draw(f.TopLeft, 0, 0)
	draw(f.TopRight, limitX, 0)
	draw(f.BottomLeft, 0, limitY)
	draw(f.BottomRight, limitX, limitY)
}

// FrameUnderWriter forwards everything to Writer, and writes the cells
// of the border of a frame Width by Height at 0,0 that Graphics draws an
// image over keeping the background of the cell under them, for
// FrameGraphics.DrawOver to draw the images over it. The cells in Keep
// keep it too, whatever Graphics draws, for images drawn over the border
// other than the frame's, such as a separator's junctions.
type FrameUnderWriter struct {
	term.Writer
	Graphics      component.FrameCharSetGraphics
	Width, Height int
	Keep          []term.Coordinates
}

// SetCell satisfies term.Writer.
func (w *FrameUnderWriter) SetCell(pos term.Coordinates, c term.Cell) {
	if frameCellAt(w.Graphics, pos.X, pos.Y, w.Width, w.Height).uri != "" || slices.Contains(w.Keep, pos) {
		c.Attrs |= term.AttrKeepBackground
	}
	w.Writer.SetCell(pos, c)
}

// frameCell is what a cell of a frame's border shows: the part at cell
// of the picture of uri cut into size cells, or all of it when size is
// at most 1 by 1. An empty uri shows no image.
type frameCell struct {
	uri        string
	cell, size image.Point
}

// frameCellAt returns what gr draws over the cell at x, y of the border
// of a frame width by height, as component.FrameCharSetGraphics
// describes it, and no image for a cell off the border.
func frameCellAt(gr component.FrameCharSetGraphics, x, y, width, height int) frameCell {
	if width < 3 || height < 3 || x < 0 || y < 0 || x >= width || y >= height ||
		(x > 0 && y > 0 && x < width-1 && y < height-1) {
		return frameCell{}
	}
	right, bottom := width-1-x < x, height-1-y < y
	dx, dy := x, y
	if right {
		dx = width - 1 - x
	}
	if bottom {
		dy = height - 1 - y
	}
	var corner string
	switch {
	case !right && !bottom:
		corner = gr.TopLeft
	case right && !bottom:
		corner = gr.TopRight
	case !right && bottom:
		corner = gr.BottomLeft
	default:
		corner = gr.BottomRight
	}
	cw, ch := max(1, gr.CornerWidth), max(1, gr.CornerHeight)
	if corner != "" && dx < cw && dy < ch {
		col, row := dx, dy
		if right {
			col = cw - 1 - dx
		}
		if bottom {
			row = ch - 1 - dy
		}
		return frameCell{uri: corner, cell: image.Pt(col, row), size: image.Pt(cw, ch)}
	}
	switch {
	case dx == 0 && dy == 0:
		return frameCell{}
	case y == 0:
		return frameCell{uri: gr.HorizontalTop}
	case y == height-1:
		return frameCell{uri: gr.HorizontalBottom}
	case x == 0:
		return frameCell{uri: gr.VerticalLeft}
	}
	return frameCell{uri: gr.VerticalRight}
}

// drawBorder draws the border that f draws when it is width x height at
// 0,0 of w, in place of f, which must not draw it: the part of an image
// of f.Graphics a cell shows goes over the cell as w holds it, so that
// its transparent pixels show what was drawn there before, and every
// other cell takes its slot's rune, as do the cells whose image is
// not loaded yet, failed to load or that w cannot draw, and those under
// Overlay images, which keep the background under the window so that
// the image's transparent pixels show it. The cells of f's scroll bar
// take the bar's runes, and with bar the top row is left to a window
// bar. A nil g draws runes only. It draws nothing when width or height
// is below 3, where f draws no border.
func (g *FrameGraphics) drawBorder(
	w term.Writer, f *component.Frame, width, height int, bar bool,
) {
	if width < 3 || height < 3 {
		return
	}
	limitX, limitY := width-1, height-1
	sbPos, sbHeight, sb := f.ScrollBar()
	gr := f.Graphics
	// cw escapes, so it is allocated by the first image only.
	var cw *cellImageWriter
	draw := func(ch rune, x, y int) {
		pos := term.Coordinates{X: x, Y: y}
		cell := term.NewCell(ch, 1, f.Attributes)
		c := frameCellAt(gr, x, y, width, height)
		if g == nil || c.uri == "" {
			w.SetCell(pos, cell)
			return
		}
		if gr.Overlay {
			under := cell
			under.Attrs |= term.AttrKeepBackground
			w.SetCell(pos, under)
		}
		if cw == nil {
			cw = &cellImageWriter{Writer: w, keep: true}
		}
		if !g.drawFrameCell(cw, c, pos) && !gr.Overlay {
			w.SetCell(pos, cell)
		}
	}

	if !bar {
		for x := 1; x < limitX; x++ {
			draw(f.HorizontalTop, x, 0)
		}
	}
	for x := 1; x < limitX; x++ {
		draw(f.HorizontalBottom, x, limitY)
	}
	// The right side is drawn after the left, as Draw does.
	for y := 1; y < limitY; y++ {
		draw(f.VerticalLeft, 0, y)
	}
	for y := 1; y < limitY; y++ {
		if !sb || y < sbPos.Y || y >= sbPos.Y+sbHeight {
			draw(f.VerticalRight, limitX, y)
			continue
		}
		ch, _ := f.ScrollBarCharSet.At(y-sbPos.Y, sbHeight)
		if ch == 0 {
			ch = f.VerticalRight
		}
		w.SetCell(term.Coordinates{X: limitX, Y: y}, term.NewCell(ch, 1, f.ScrollBarAttributes))
	}
	if !bar {
		draw(f.TopLeft, 0, 0)
		draw(f.TopRight, limitX, 0)
	}
	draw(f.BottomLeft, 0, limitY)
	draw(f.BottomRight, limitX, limitY)
}

// DrawSeparator draws images over an already drawn separator at y inside
// the frame. Unavailable junctions keep the frame's side images; writers
// without image support keep the separator's runes. Junction outsets are
// clipped to the frame without reserving space or rescaling the images.
func (g *FrameGraphics) DrawSeparator(
	w term.Writer, f *component.Frame, cs SeparatorCharSet, width, height, y int,
) {
	if width < 3 || height < 3 || y <= 0 || y >= height-1 {
		return
	}
	w = &component.VirtualWriter{Writer: w, Width: width, Height: height}
	cw := cellImageWriter{
		Writer: w,
		under:  term.NewCell(' ', 1, f.Attributes),
		layer:  term.ImageLayerAboveText,
	}
	draw := func(c frameCell, ch rune, x int, area image.Rectangle, overlay bool) bool {
		if c.uri == "" {
			return false
		}
		cw.keep = overlay
		cw.fallback = term.NewCell(ch, 1, f.Attributes)
		cw.cell, cw.size = c.cell, c.size
		return g.drawArea(&cw, c.uri, term.Coordinates{X: x, Y: y}, area)
	}
	gr := cs.Graphics
	for x := 1; x < width-1; x++ {
		ch := f.HorizontalBottom
		if x == 1 {
			ch = cs.HorizontalLeft
		}
		if x == width-2 {
			ch = cs.HorizontalRight
		}
		draw(frameCell{uri: gr.Horizontal}, ch, x, image.Rect(x, y, x+1, y+1), gr.Overlay)
	}
	// Junctions cover the ends of the repeated bar, including their outsets.
	top, bottom, inward := max(0, gr.Outset.Top), max(0, gr.Outset.Bottom), max(0, gr.Outset.Inward)
	if !draw(frameCell{uri: gr.Left}, cs.Left, 0, image.Rect(0, y-top, 1+inward, y+1+bottom), gr.Overlay) {
		draw(frameCellAt(f.Graphics, 0, y, width, height), cs.Left, 0, image.Rect(0, y, 1, y+1), f.Graphics.Overlay)
	}
	if !draw(frameCell{uri: gr.Right}, cs.Right, width-1, image.Rect(width-1-inward, y-top, width, y+1+bottom), gr.Overlay) {
		draw(frameCellAt(f.Graphics, width-1, y, width, height), cs.Right, width-1, image.Rect(width-1, y, width, y+1), f.Graphics.Overlay)
	}
}

// DrawScrollBar draws, over the scroll bar that f draws when it is
// width x height at 0,0 of w, the image that f.ScrollBarCharSet names for
// each of the bar's cells. Each image fills its cell and the outset cells
// left of it, covering whatever they show, over a blank cell with
// f.ScrollBarAttributes, or over the rune f drew there when the graphics
// are Overlay. The cells of an image that is not loaded yet, failed to
// load or that w cannot draw keep their rune. It draws nothing where f
// draws no scroll bar.
func (g *FrameGraphics) DrawScrollBar(
	w term.Writer, f *component.Frame, width, height, outset int,
) {
	if width < 3 || height < 3 {
		return
	}
	pos, sbHeight, ok := f.ScrollBar()
	if !ok {
		return
	}
	cs := f.ScrollBarCharSet
	outset = max(0, min(outset, pos.X))
	cw := cellImageWriter{
		Writer: w,
		under:  term.NewCell(' ', 1, f.ScrollBarAttributes),
		keep:   cs.Graphics.Overlay,
	}
	for i := range sbHeight {
		ch, uri := cs.At(i, sbHeight)
		if uri == "" {
			continue
		}
		if ch == 0 {
			ch = f.VerticalRight
		}
		cw.fallback = term.NewCell(ch, 1, f.ScrollBarAttributes)
		at := term.Coordinates{X: pos.X, Y: pos.Y + i}
		g.drawArea(&cw, uri, at,
			image.Rect(at.X-outset, at.Y, at.X+1, at.Y+1))
	}
}

// DrawCell draws the image of uri in place of cell at pos of w, over a
// blank cell with cell's colors and attributes that keeps the background
// of the cell under it, so that the image's transparent pixels show what
// was drawn there before. The cell at pos takes cell itself while the
// image is not loaded yet, failed to load or w cannot draw it. It draws
// nothing when uri is empty.
func (g *FrameGraphics) DrawCell(
	w term.Writer, uri string, pos term.Coordinates, cell term.Cell,
) {
	if uri == "" {
		return
	}
	under := cell
	under.Ch = ' '
	under.Extra = nil
	under.Attrs |= term.AttrKeepBackground
	cw := cellImageWriter{Writer: w, under: under, fallback: cell}
	if !g.drawCell(&cw, uri, pos) {
		w.SetCell(pos, cell)
	}
}

// DrawLine draws the images of lg over the line of length cells that
// lg.DrawCells wrote with cell from from, rightwards or, when vertical,
// downwards, each filling its cell in layer on its row. It leaves the
// cells as they are, for the images drawn under them since to stay. A
// cell whose image is not loaded yet, failed to load or that w cannot
// draw is written cell again, unless lg is Overlay, which kept it.
func (g *FrameGraphics) DrawLine(
	w term.Writer, lg LineGraphics, from term.Coordinates, length int,
	vertical bool, cell term.Cell, layer term.ImageLayer,
) {
	if length <= 0 || lg.Empty() {
		return
	}
	cw := cellImageWriter{Writer: w, keep: true, layer: layer}
	for i := range length {
		uri := lg.uri(i, length)
		if uri == "" {
			continue
		}
		pos := linePos(from, i, vertical)
		if !g.drawCell(&cw, uri, pos) && !lg.Overlay {
			w.SetCell(pos, cell)
		}
	}
}

// DrawBackground draws the images of gr over area of w, under the
// background of its cells, so that they show only through the cells
// whose background is the default. It appends to bare the slices of area
// that show no image, which name none, whose image is not loaded yet or
// failed to load, or that w cannot draw, and returns it.
func (g *FrameGraphics) DrawBackground(
	w term.Writer, gr BackgroundGraphics, area image.Rectangle, bare []image.Rectangle,
) []image.Rectangle {
	dw := drawnWriter{Writer: w}
	for _, s := range gr.slices(area) {
		if s.area.Empty() {
			continue
		}
		img := g.background(s.uri, gr.Fit)
		if img == nil {
			bare = append(bare, s.area)
			continue
		}
		dw.drawn = false
		img.Resize(s.area.Dx(), s.area.Dy())
		img.Draw(&component.VirtualWriter{
			Writer: &dw,
			Offset: term.Coordinates{X: s.area.Min.X, Y: s.area.Min.Y},
			Width:  s.area.Dx(),
			Height: s.area.Dy(),
		})
		if !dw.drawn {
			bare = append(bare, s.area)
		}
	}
	return bare
}

// background returns the image of a background slice, or nil if uri is
// empty or cannot be displayed.
func (g *FrameGraphics) background(uri string, fit term.ImageFit) *imageuri.Component {
	if uri == "" {
		return nil
	}
	key := backgroundKey{uri: uri, fit: fit}
	img, ok := g.backgrounds[key]
	if ok {
		return img
	}
	style := imageuri.Style{Fit: fit, Layer: term.ImageLayerBelowBackground, HideLoading: true}
	var err error
	if src, ok := g.reloaded[uri]; ok {
		img, err = imageuri.NewLoaded(uri, src, style)
	} else {
		img, err = imageuri.New(uri, template.DefaultImageTTL, g.storage, g.interrupter, style)
	}
	if err != nil {
		slog.Warn("frame graphics: new background image", "error", err)
	}
	g.backgrounds[key] = img
	return img
}

// drawnWriter records whether Writer drew any image.
type drawnWriter struct {
	term.Writer
	drawn bool
}

// DrawImage satisfies term.Writer.
func (w *drawnWriter) DrawImage(img term.Image) bool {
	ok := w.Writer.DrawImage(img)
	w.drawn = w.drawn || ok
	return ok
}

// Close closes every image.
func (g *FrameGraphics) Close() error {
	var errs []error
	for _, img := range g.images {
		if img != nil {
			errs = append(errs, img.Close())
		}
	}
	for _, img := range g.backgrounds {
		if img != nil {
			errs = append(errs, img.Close())
		}
	}
	return errors.Join(errs...)
}

// Reload replaces every image g holds with images: g draws each without
// fetching it, and fetches any other URI again, through storage, the
// next time it draws it, in frames and backgrounds alike. The images it
// held are closed.
func (g *FrameGraphics) Reload(images map[string]image.Image) {
	old := g.images
	g.images = make(map[string]*imageuri.Component, len(images))
	for uri, src := range images {
		img, err := imageuri.NewLoaded(uri, src, frameImageStyle)
		if err != nil {
			slog.Warn("frame graphics: reload image", "error", err)
		}
		g.images[uri] = img
	}
	for _, img := range old {
		if img != nil {
			_ = img.Close()
		}
	}
	for _, img := range g.backgrounds {
		if img != nil {
			_ = img.Close()
		}
	}
	clear(g.backgrounds)
	g.reloaded = images
}

// Image returns the image of uri, the one that g draws wherever uri is
// named, so it shares one ImageID with them. It is nil when uri is empty
// or cannot be displayed. The image draws nothing until it has loaded.
func (g *FrameGraphics) Image(uri string) tui.Component {
	if uri == "" {
		return nil
	}
	if img := g.image(uri); img != nil {
		return img
	}
	return nil
}

// image returns the image for uri, or nil if it cannot be displayed.
func (g *FrameGraphics) image(uri string) *imageuri.Component {
	img, ok := g.images[uri]
	if ok {
		return img
	}
	img, err := imageuri.New(uri, template.DefaultImageTTL, g.storage,
		g.interrupter, frameImageStyle)
	if err != nil {
		slog.Warn("frame graphics: new image", "error", err)
	}
	g.images[uri] = img
	return img
}

// drawCell draws the image of uri over the cell at pos, and reports
// whether it drew it.
func (g *FrameGraphics) drawCell(
	cw *cellImageWriter, uri string, pos term.Coordinates,
) bool {
	return g.drawArea(cw, uri, pos, image.Rect(pos.X, pos.Y, pos.X+1, pos.Y+1))
}

// drawFrameCell draws over the cell at pos its part of the image of c,
// and reports whether it drew it.
func (g *FrameGraphics) drawFrameCell(
	cw *cellImageWriter, c frameCell, pos term.Coordinates,
) bool {
	cw.cell, cw.size = c.cell, c.size
	return g.drawCell(cw, c.uri, pos)
}

// drawArea draws the image of uri stretched over the cells of area, in
// place of the cell at pos.
func (g *FrameGraphics) drawArea(
	cw *cellImageWriter, uri string, pos term.Coordinates, area image.Rectangle,
) bool {
	img := g.image(uri)
	if img == nil || area.Empty() {
		return false
	}
	cw.at = pos
	cw.drawn = false
	cw.area = component.VirtualWriter{
		Writer: cw,
		Offset: term.Coordinates{X: area.Min.X, Y: area.Min.Y},
		Width:  area.Dx(),
		Height: area.Dy(),
	}
	img.Resize(area.Dx(), area.Dy())
	img.Draw(&cw.area)
	return cw.drawn
}

// cellImageWriter draws an image in place of the cell at at: over under,
// or over the cell as it is when keep is set, and fallback back when
// Writer cannot draw the image. The image is drawn in layer on at's row
// and above the text of any other row it reaches, whose text is not at's.
// When deferred is set, the part on at's row under the text goes to it
// instead, counted as drawn, for the caller to draw later. Only the part
// at cell of the image's picture cut into size cells is drawn, or all of
// it when size is at most 1 by 1.
type cellImageWriter struct {
	term.Writer
	under, fallback term.Cell
	keep            bool
	drawn           bool
	layer           term.ImageLayer
	at              term.Coordinates
	cell, size      image.Point
	area            component.VirtualWriter
	deferred        *[]deferredImage
}

// deferredImage is the part of an image a cellImageWriter left to draw
// in place of the cell at at, which takes fallback where the writer
// draws no image.
type deferredImage struct {
	img      term.Image
	at       term.Coordinates
	fallback term.Cell
}

// DrawImage satisfies term.Writer. The cell is written before the image
// because writers treat a cell written after an image as covering it.
func (w *cellImageWriter) DrawImage(img term.Image) bool {
	img = croppedToCell(img, w.cell, w.size)
	if !w.keep {
		w.Writer.SetCell(w.at, w.under)
	}
	if w.drawImage(img) {
		w.drawn = true
		return true
	}
	if !w.keep {
		w.Writer.SetCell(w.at, w.fallback)
	}
	return false
}

// drawImage draws img in layer on at's row and above the text elsewhere,
// reporting whether Writer drew it.
func (w *cellImageWriter) drawImage(img term.Image) bool {
	if w.layer == term.ImageLayerAboveText {
		img.Layer = w.layer
		return w.Writer.DrawImage(img)
	}
	b := img.Bounds()
	drawn := false
	for _, part := range []struct {
		rows  image.Rectangle
		layer term.ImageLayer
	}{
		{image.Rect(b.Min.X, b.Min.Y, b.Max.X, w.at.Y), term.ImageLayerAboveText},
		{image.Rect(b.Min.X, w.at.Y, b.Max.X, w.at.Y+1), w.layer},
		{image.Rect(b.Min.X, w.at.Y+1, b.Max.X, b.Max.Y), term.ImageLayerAboveText},
	} {
		p, ok := img.Clipped(part.rows)
		if !ok {
			continue
		}
		p.Layer = part.layer
		if w.deferred != nil && part.layer != term.ImageLayerAboveText {
			*w.deferred = append(*w.deferred, deferredImage{img: p, at: w.at, fallback: w.fallback})
			drawn = true
			continue
		}
		drawn = w.Writer.DrawImage(p) || drawn
	}
	return drawn
}

// croppedToCell returns img cropped to the part at cell of its picture
// cut into size cells, or img itself when size is at most 1 by 1.
func croppedToCell(img term.Image, cell, size image.Point) term.Image {
	if img.Src == nil || size.X <= 1 && size.Y <= 1 {
		return img
	}
	b := img.Src.Bounds()
	if !img.Crop.Empty() {
		b = img.Crop.Intersect(b)
	}
	w, h := b.Dx(), b.Dy()
	img.Crop = image.Rect(
		b.Min.X+cell.X*w/size.X, b.Min.Y+cell.Y*h/size.Y,
		b.Min.X+(cell.X+1)*w/size.X, b.Min.Y+(cell.Y+1)*h/size.Y,
	)
	return img
}
