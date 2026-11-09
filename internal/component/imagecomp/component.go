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

// Package imagecomp provides a tui.Component that draws an image as a
// picture, in a box sized in cells or as a share of the component.
package imagecomp

import (
	"image"
	"math"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"golang.org/x/image/draw"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/asciiart"
)

// Fit selects how an image is scaled into its box.
type Fit uint8

const (
	// Contain scales the image uniformly to the largest size that fits
	// the box, and shrinks the box to it.
	Contain Fit = iota
	// Cover scales the image uniformly to the smallest size that covers
	// the box, cutting off evenly what overflows it.
	Cover
	// Fill stretches the image over the box.
	Fill
)

// Length is a number of cells, or a percentage of the component's width
// or height.
type Length struct {
	N       int
	Percent bool
}

// cells resolves l in a component total cells long, up to total.
func (l Length) cells(total int) int {
	if l.Percent {
		return total * min(l.N, 100) / 100
	}
	return min(l.N, total)
}

// Config controls how a Component draws its image.
type Config struct {
	// Width and Height size the box the image is drawn in, centered in
	// the component.
	Width, Height Length
	Fit           Fit
	// ASCII encodes the image where the writer does not draw images. Its
	// MaintainAspectRatio is ignored in favor of Fit.
	ASCII asciiart.Config
}

// Component draws an image centered in its cells. Where the writer draws
// images, that is when its context carries the size of a cell in pixels
// (see [cell.PixelSizeFromContext]) and DrawImage succeeds, the image is
// a picture laid out for that size. Elsewhere it is ASCII art laid out
// for cells [asciiart.HeightToWidthCellAspectRatio] times taller than
// wide.
type Component struct {
	img           image.Image
	id            term.ImageID
	cfg           Config
	width, height int
	// ascii is the ASCII art drawn at asciiBox, nil until it is first
	// drawn after a resize.
	ascii    [][]term.Cell
	asciiBox image.Rectangle
}

var _ tui.Component = (*Component)(nil)

// New returns a Component that draws img with cfg. id identifies img's
// pixels to the writer, so components that draw the same img can share
// it for the writer to keep a single copy of them.
func New(img image.Image, id term.ImageID, cfg Config) *Component {
	cfg.ASCII.MaintainAspectRatio = false
	return &Component{img: img, id: id, cfg: cfg}
}

// Resize satisfies tui.Component.
func (c *Component) Resize(width, height int) {
	c.width, c.height = width, height
	c.ascii = nil
}

// Draw satisfies tui.Component.
func (c *Component) Draw(w term.Writer) {
	if size, ok := cell.PixelSizeFromContext(w.Context()); ok && c.drawPicture(w, size) {
		return
	}
	c.drawASCII(w)
}

// drawPicture draws the image for cells of size pixels, reporting false
// when w does not draw images.
func (c *Component) drawPicture(w term.Writer, size cell.PixelSize) bool {
	box, crop := c.layout(size)
	if box.Empty() {
		return true
	}
	fit := term.ImageFitFill
	if c.cfg.Fit == Contain {
		// The box is whole cells, so it is a little off the image's
		// aspect ratio.
		fit = term.ImageFitContain
	}
	return w.DrawImage(term.Image{
		Src:    c.img,
		ID:     c.id,
		Crop:   crop,
		Pos:    term.Coordinates{X: box.Min.X, Y: box.Min.Y},
		Width:  box.Dx(),
		Height: box.Dy(),
		Fit:    fit,
	})
}

func (c *Component) drawASCII(w term.Writer) {
	if c.ascii == nil {
		c.asciiBox, c.ascii = c.encodeASCII()
	}
	for y, row := range c.ascii {
		for x, ch := range row {
			w.SetCell(term.Coordinates{X: c.asciiBox.Min.X + x, Y: c.asciiBox.Min.Y + y}, ch)
		}
	}
}

func (c *Component) encodeASCII() (image.Rectangle, [][]term.Cell) {
	box, crop := c.layout(cell.PixelSize{Width: 1, Height: asciiart.HeightToWidthCellAspectRatio})
	if box.Empty() {
		return box, [][]term.Cell{}
	}
	shown := image.NewNRGBA(image.Rect(0, 0, box.Dx(), box.Dy()))
	c.cfg.ASCII.Scaler.Scale(shown, shown.Rect, c.img, crop, draw.Over, nil)
	buf := cell.NewBuffer()
	asciiart.Encode(buf, box.Dx(), box.Dy(), shown, c.cfg.ASCII)
	return box, buf.RawCells()
}

// layout returns the cells of the box the image is drawn in, for cells of
// size pixels, and the part of the image that shows in it. The box is
// empty when the component or its configured box is.
func (c *Component) layout(size cell.PixelSize) (box, crop image.Rectangle) {
	crop = c.img.Bounds()
	width, height := c.cfg.Width.cells(c.width), c.cfg.Height.cells(c.height)
	if width <= 0 || height <= 0 || crop.Empty() {
		return image.Rectangle{}, crop
	}
	boxWidth, boxHeight := float64(width)*size.Width, float64(height)*size.Height
	imgWidth, imgHeight := float64(crop.Dx()), float64(crop.Dy())
	switch c.cfg.Fit {
	case Contain:
		scale := min(boxWidth/imgWidth, boxHeight/imgHeight)
		width = max(1, min(width, int(math.Round(imgWidth*scale/size.Width))))
		height = max(1, min(height, int(math.Round(imgHeight*scale/size.Height))))
	case Cover:
		scale := max(boxWidth/imgWidth, boxHeight/imgHeight)
		shown := image.Pt(
			max(1, min(crop.Dx(), int(math.Round(boxWidth/scale)))),
			max(1, min(crop.Dy(), int(math.Round(boxHeight/scale)))))
		crop.Min = crop.Min.Add(crop.Size().Sub(shown).Div(2))
		crop.Max = crop.Min.Add(shown)
	}
	at := image.Pt((c.width-width)/2, (c.height-height)/2)
	return image.Rectangle{Min: at, Max: at.Add(image.Pt(width, height))}, crop
}
