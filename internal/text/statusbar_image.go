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

package text

import (
	"image"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/imageuri"
	"unstable.build/rune/internal/component/template"
)

var _ component.Floating = (*statusBarImage)(nil)

// statusBarImage is a StatusBarImage element. As a component.Floating
// it is the element's literals and reserved cells in the bar's flow;
// the image itself is drawn by drawImage, centered on the cell after
// the leading literal, so that it can extend over the rest of the bar
// and the editor.
type statusBarImage struct {
	spec template.Image
	slot component.Floating
	// lead is the width of the literal before the reserved cells.
	lead int
	view *imageuri.Component
	// w places view where place laid it out.
	w placementWriter
}

func newStatusBarImage(
	comp StatusBarComponent, bg term.Color, storage storageapi.Service,
	interrupter term.Interrupter,
) (*statusBarImage, error) {
	spec := comp.Image
	view, err := imageuri.New(spec.URI, spec.TTL, storage, interrupter, imageuri.Style{
		Fit: spec.Fit, Layer: spec.Layer(), ProblemArt: spec.Alt,
	})
	if err != nil {
		return nil, err
	}
	before, after, _ := strings.Cut(comp.Template, "%s")
	lead := statusBarLiteral(before, comp.Attributes, bg)
	leadWidth, _ := component.Inline(lead, component.AlignmentLeft).Dimensions()
	parts := append(lead[:len(lead):len(lead)],
		statusBarLiteral(strings.Repeat(" ", spec.Reserve), comp.Attributes, bg)...)
	parts = append(parts, statusBarLiteral(after, comp.Attributes, bg)...)
	return &statusBarImage{
		spec: spec,
		slot: component.Inline(parts, component.AlignmentLeft),
		lead: leadWidth,
		view: view,
		w: placementWriter{
			pixels:   image.Pt(spec.XOffsetPixels, spec.YOffsetPixels),
			overflow: spec.Overflow,
		},
	}, nil
}

func statusBarLiteral(text string, attrs term.Attributes, bg term.Color) []component.Floating {
	if text == "" {
		return nil
	}
	return template.Build(text, "", term.Attributes{}, attrs, bg)
}

// Dimensions satisfies component.Floating.
func (i *statusBarImage) Dimensions() (width, height int) {
	return i.slot.Dimensions()
}

// Resize satisfies tui.Component.
func (i *statusBarImage) Resize(width, height int) {
	i.slot.Resize(width, height)
}

// Draw satisfies tui.Component.
func (i *statusBarImage) Draw(w term.Writer) {
	i.slot.Draw(w)
}

// place lays the image out over a bar of width x rows cells, whose own
// row is the last one, for an element laid out from cell x, as
// template.Image describes. No rows hide the image.
func (i *statusBarImage) place(x, width, rows int) {
	w, h := i.spec.Width.Cells(width), i.spec.Height
	if rows <= 0 || w <= 0 {
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

// drawImage draws the image, moved with the bar when the bar is shifted.
func (i *statusBarImage) drawImage(w term.Writer, shifted bool) {
	i.w.Writer, i.w.shifted = w, shifted
	i.view.Draw(&i.w)
}

func (i *statusBarImage) Close() error {
	return i.view.Close()
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

// DrawImage satisfies term.Writer.
func (w *placementWriter) DrawImage(img term.Image) bool {
	img = img.Translated(w.VirtualWriter.Offset)
	img.Offset = w.pixels
	img.Overflow = w.overflow
	img.VerticalRenderOffset = w.shifted
	return w.Writer.DrawImage(img)
}
