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
	"unstable.build/rune/internal/component/imageuri"
	"unstable.build/rune/internal/component/template"
)

// FrameGraphics draws the images that FrameCharSet.Graphics names over
// the border of a component.Frame. It keeps one image per URI, so a URI
// is fetched and decoded once and shares one ImageID across every frame
// and slot it is drawn in.
type FrameGraphics struct {
	storage     storageapi.Service
	interrupter term.Interrupter
	images      map[string]*imageuri.Component
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

// image returns the image for uri, or nil if it cannot be displayed.
func (g *FrameGraphics) image(uri string) *imageuri.Component {
	img, ok := g.images[uri]
	if ok {
		return img
	}
	img, err := imageuri.New(uri, template.DefaultImageTTL, g.storage,
		g.interrupter, imageuri.Style{
			Fit:         term.ImageFitFill,
			Layer:       term.ImageLayerAboveText,
			HideLoading: true,
		})
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
) {
	img := g.image(uri)
	if img == nil || area.Empty() {
		return
	}
	cw.at = pos
	cw.area = component.VirtualWriter{
		Writer: cw,
		Offset: term.Coordinates{X: area.Min.X, Y: area.Min.Y},
		Width:  area.Dx(),
		Height: area.Dy(),
	}
	img.Resize(area.Dx(), area.Dy())
	img.Draw(&cw.area)
}

// cellImageWriter draws an image in place of the cell at at: over under,
// or over the cell as it is when keep is set, and fallback back when
// Writer cannot draw the image. The image is drawn in layer on at's row
// and above the text of any other row it reaches, whose text is not at's.
type cellImageWriter struct {
	term.Writer
	under, fallback term.Cell
	keep            bool
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
