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
	"image"
	"log/slog"

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

// FrameGraphics draws the images that FrameCharSet.Graphics names over
// the border of a component.Frame. It keeps one image per URI, so a URI
// is fetched and decoded once and shares one ImageID across every frame
// and slot it is drawn in.
type FrameGraphics struct {
	storage     storageapi.Service
	interrupter term.Interrupter
	images      map[string]*imageuri.Component
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
	}
}

// Draw draws, over the border that f draws when it is width x height at
// 0,0 of w, the image of every slot of f.FrameCharSet.Graphics that has a
// URI. Each image fills its cell over a blank cell with f.Attributes, or
// over the rune f drew there when the graphics are Overlay. Cells under
// the scroll bar, and the cells of an image that is not loaded yet,
// failed to load or that w cannot draw keep their rune. It draws nothing
// when width or height is below 3, where f draws no border.
func (g *FrameGraphics) Draw(
	w term.Writer, f *component.Frame, width, height int,
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
	draw := func(uri string, ch rune, x, y int) {
		if uri == "" ||
			(sb && x == limitX && y >= sbPos.Y && y < sbPos.Y+sbHeight) {
			return
		}
		cw.fallback = term.NewCell(ch, 1, f.Attributes)
		g.drawCell(&cw, uri, term.Coordinates{X: x, Y: y})
	}

	for x := 1; x < limitX; x++ {
		draw(gr.HorizontalTop, f.HorizontalTop, x, 0)
		draw(gr.HorizontalBottom, f.HorizontalBottom, x, limitY)
	}
	for y := 1; y < limitY; y++ {
		draw(gr.VerticalLeft, f.VerticalLeft, 0, y)
		draw(gr.VerticalRight, f.VerticalRight, limitX, y)
	}
	draw(gr.TopLeft, f.TopLeft, 0, 0)
	draw(gr.TopRight, f.TopRight, limitX, 0)
	draw(gr.BottomLeft, f.BottomLeft, 0, limitY)
	draw(gr.BottomRight, f.BottomRight, limitX, limitY)
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
	draw := func(uri string, ch rune, x int, area image.Rectangle, overlay bool) bool {
		if uri == "" {
			return false
		}
		cw.keep = overlay
		cw.fallback = term.NewCell(ch, 1, f.Attributes)
		return g.drawArea(&cw, uri, term.Coordinates{X: x, Y: y}, area)
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
		draw(gr.Horizontal, ch, x, image.Rect(x, y, x+1, y+1), gr.Overlay)
	}
	// Junctions cover the ends of the repeated bar, including their outsets.
	top, bottom, inward := max(0, gr.Outset.Top), max(0, gr.Outset.Bottom), max(0, gr.Outset.Inward)
	if !draw(gr.Left, cs.Left, 0, image.Rect(0, y-top, 1+inward, y+1+bottom), gr.Overlay) {
		draw(f.Graphics.VerticalLeft, cs.Left, 0, image.Rect(0, y, 1, y+1), f.Graphics.Overlay)
	}
	if !draw(gr.Right, cs.Right, width-1, image.Rect(width-1-inward, y-top, width, y+1+bottom), gr.Overlay) {
		draw(f.Graphics.VerticalRight, cs.Right, width-1, image.Rect(width-1, y, width, y+1), f.Graphics.Overlay)
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

// Close closes every image.
func (g *FrameGraphics) Close() error {
	var errs []error
	for _, img := range g.images {
		if img != nil {
			errs = append(errs, img.Close())
		}
	}
	return errors.Join(errs...)
}

// Reload replaces every image g holds with images: g draws each without
// fetching it, and fetches any other URI again, through storage, the
// next time it draws it. The images it held are closed.
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

// drawCell draws the image of uri over the cell at pos.
func (g *FrameGraphics) drawCell(
	cw *cellImageWriter, uri string, pos term.Coordinates,
) {
	g.drawArea(cw, uri, pos, image.Rect(pos.X, pos.Y, pos.X+1, pos.Y+1))
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
type cellImageWriter struct {
	term.Writer
	under, fallback term.Cell
	keep            bool
	drawn           bool
	layer           term.ImageLayer
	at              term.Coordinates
	area            component.VirtualWriter
}

// DrawImage satisfies term.Writer. The cell is written before the image
// because writers treat a cell written after an image as covering it.
func (w *cellImageWriter) DrawImage(img term.Image) bool {
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
		drawn = w.Writer.DrawImage(p) || drawn
	}
	return drawn
}
