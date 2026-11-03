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

package template

import (
	"cmp"
	"image"
	"slices"
	"strings"
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/imageuri"
)

var _ component.Floating = (*ImageElement)(nil)

// ImageElement is an ImageField element of a bar whose elements are laid
// out by component.Inline. As a component.Floating it is the element's
// literals and the cells it reserves in the bar. The image itself is
// drawn by DrawImage, where PlaceImages put it as Image describes, so
// that it can extend over the rest of the bar and whatever surrounds it.
type ImageElement struct {
	spec Image
	slot component.Floating
	// lead is the width of the literal before the reserved cells.
	lead int
	view *imageuri.Component
	// w places view where place laid it out.
	w placementWriter
}

// NewImageElement returns the element for spec. tmpl holds the literals
// drawn before and after the reserved cells, on either side of its first
// %s verb, or before them when it has none. They are styled with attrs
// over bg as Build styles them, but drawn as they are: further verbs are
// not formatted, and control characters, which cannot be drawn, are
// drawn as spaces. storage caches the downloaded image and is used
// as-is: the caller owns its partitioning and lifetime. interrupter is
// signalled whenever the image changes what it draws. It fails when
// spec.Validate does, or when imageuri.New fails for spec, storage or
// interrupter.
func NewImageElement(
	spec Image, tmpl string, attrs term.Attributes, bg term.Color,
	storage storageapi.Service, interrupter term.Interrupter,
) (*ImageElement, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	view, err := imageuri.New(spec.URI, spec.TTL, storage, interrupter, imageuri.Style{
		Fit: spec.Fit, Layer: spec.Layer(), ProblemArt: spec.Alt,
	})
	if err != nil {
		return nil, err
	}
	return newImageElement(spec, view, tmpl, attrs, bg), nil
}

// NewLoadedImageElement returns the element NewImageElement would for
// spec, tmpl, attrs and bg, but showing img as if it had just loaded
// spec.URI: it never fetches, so it draws img from its first frame. It
// fails when spec.Validate does.
func NewLoadedImageElement(
	spec Image, img image.Image, tmpl string, attrs term.Attributes, bg term.Color,
) (*ImageElement, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	view, err := imageuri.NewLoaded(spec.URI, img, imageuri.Style{
		Fit: spec.Fit, Layer: spec.Layer(), ProblemArt: spec.Alt,
	})
	if err != nil {
		return nil, err
	}
	return newImageElement(spec, view, tmpl, attrs, bg), nil
}

func newImageElement(
	spec Image, view *imageuri.Component, tmpl string, attrs term.Attributes, bg term.Color,
) *ImageElement {
	tmpl = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, tmpl)
	before, after, _ := strings.Cut(tmpl, "%s")
	lead := literal(before, attrs, bg)
	leadWidth, _ := component.Inline(lead, component.AlignmentLeft).Dimensions()
	parts := append(lead[:len(lead):len(lead)],
		literal(strings.Repeat(" ", spec.Reserve), attrs, bg)...)
	parts = append(parts, literal(after, attrs, bg)...)
	return &ImageElement{
		spec: spec,
		slot: component.Inline(parts, component.AlignmentLeft),
		lead: leadWidth,
		view: view,
		w: placementWriter{
			pixels:   image.Pt(spec.XOffsetPixels, spec.YOffsetPixels),
			overflow: spec.Overflow,
		},
	}
}

func literal(text string, attrs term.Attributes, bg term.Color) []component.Floating {
	if text == "" {
		return nil
	}
	return build(text, func(text string) string { return text }, term.Attributes{}, attrs, bg)
}

// Dimensions satisfies component.Floating.
func (i *ImageElement) Dimensions() (width, height int) {
	return i.slot.Dimensions()
}

// Resize satisfies tui.Component.
func (i *ImageElement) Resize(width, height int) {
	i.slot.Resize(width, height)
}

// Draw satisfies tui.Component. It draws the literals and the reserved
// cells, not the image.
func (i *ImageElement) Draw(w term.Writer) {
	i.slot.Draw(w)
}

// place lays the image out over a layout of width x rows cells, whose
// bar is the last row, for an element laid out from cell x that would
// end cut cells past the end of the bar. No rows hide the image.
func (i *ImageElement) place(x, width, rows, cut int) {
	w, h := i.spec.Width.Cells(width), i.spec.Height
	if rows <= 0 || w <= 0 || cut > i.spec.MaxCut {
		w, h = 0, 0
	}
	anchor := term.Coordinates{X: x + i.lead, Y: rows - 1}
	i.w.VirtualWriter.Offset = term.Coordinates{
		X: anchor.X - w/2 + i.spec.XOffset.Cells(width),
		Y: anchor.Y - h/2 + i.spec.YOffset,
	}
	i.w.Width, i.w.Height = w, h
	i.view.Resize(w, h)
}

// DrawImage draws the image, or its alt text when w cannot draw images,
// to w in the coordinates of the layout PlaceImages last placed it in.
// shifted moves the image with the bar's row when it is drawn with
// term.AttrVerticalRenderOffset. The image is not cut off: w has to cut
// it off where it must not show.
func (i *ImageElement) DrawImage(w term.Writer, shifted bool) {
	i.w.Writer, i.w.shifted = w, shifted
	i.view.Draw(&i.w)
}

// Close stops loading the image.
func (i *ImageElement) Close() error {
	return i.view.Close()
}

// PlaceImages places the ImageElements among items, the elements of a
// bar laid out from cell x in width cells as component.Inline lays them
// out: left to right, each cut to the width left. The bar is the last
// row of a layout total cells wide and rows tall, which the images are
// placed in. No rows hide the images, and so does a bar that would need
// more than Image.MaxCut more cells to fit an image's element and the
// elements before it. A negative width is no width.
func PlaceImages(items []component.Floating, x, width, total, rows int) {
	width = max(width, 0)
	need, left := 0, width
	for _, item := range items {
		w, _ := item.Dimensions()
		need += w
		if img, ok := item.(*ImageElement); ok {
			img.place(x, total, rows, need-width)
		}
		w = min(w, left)
		x += w
		left -= w
	}
}

// SortImages sorts imgs into the order they are drawn in: by ZIndex, and
// by their order in imgs where it ties.
func SortImages(imgs []*ImageElement) {
	slices.SortStableFunc(imgs, func(a, b *ImageElement) int {
		return cmp.Compare(a.spec.ZIndex, b.spec.ZIndex)
	})
}

// placementWriter moves what is drawn through it to the cells the image
// is placed on, where it cuts the cells off. It does not cut the image
// off there, because pixels moves the image off those cells, and it sets
// how the image is placed: whether it extends past the writers it is
// forwarded to, and whether it moves with the cells shifted by
// term.AttrVerticalRenderOffset.
type placementWriter struct {
	component.VirtualWriter
	pixels            image.Point
	overflow, shifted bool
}

// SetCell satisfies term.Writer. A wide cell that does not fit in the
// image's cells is not drawn, because it would spill over the cell next
// to them.
func (w *placementWriter) SetCell(pos term.Coordinates, c term.Cell) {
	if pos.X+int(c.Width) > w.Width {
		return
	}
	w.VirtualWriter.SetCell(pos, c)
}

// DrawImage satisfies term.Writer.
func (w *placementWriter) DrawImage(img term.Image) bool {
	img = img.Translated(w.VirtualWriter.Offset)
	img.Offset = w.pixels
	img.Overflow = w.overflow
	img.VerticalRenderOffset = w.shifted
	return w.Writer.DrawImage(img)
}
