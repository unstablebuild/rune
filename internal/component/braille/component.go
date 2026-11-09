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

package braille

import (
	"image"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
)

// Style is how a [Component] draws its image.
type Style int

const (
	// Dots draws the image with [Encode].
	Dots Style = iota
	// Blocks draws the image with [EncodeBlocks].
	Blocks
)

// Component draws an image over its whole area, so the cells the image
// leaves empty are [Blank].
type Component struct {
	img           image.Image
	cellPixelSize func() (width, height int)
	style         Style
	width, height int
	cellAspect    float64
	cells         [][]term.Cell

	picture func() image.Image
	shown   pictureShown
}

// pictureShown is the picture as last encoded, and what it was encoded
// for.
type pictureShown struct {
	src           image.Image
	id            term.ImageID
	version       uint64
	width, height int
	cellAspect    float64
	size          cell.PixelSize
	cells         image.Rectangle
	pixels        *image.RGBA
}

var _ tui.Component = (*Component)(nil)

// NewComponent returns a Component that draws img in style. cellPixelSize
// reports the size of a cell so the image keeps its proportions on the
// font in use, and is consulted on every draw since fonts can change.
// While it is nil or reports zero, cells are assumed to have the [Encode]
// default proportions.
func NewComponent(
	img image.Image, cellPixelSize func() (width, height int), style Style,
) *Component {
	return &Component{img: img, cellPixelSize: cellPixelSize, style: style}
}

// WithPicture makes c draw picture, a sharper copy of its image, where the
// writer draws images: one whose context carries the size of a cell in
// pixels (see [cell.PixelSizeFromContext]) and whose DrawImage succeeds.
// Every cell is then [Blank], under the picture laid out by
// [EncodePicture]. Other writers get c's style.
//
// picture is called on the first draw that shows it, so the picture can be
// decoded lazily. It returns c.
func (c *Component) WithPicture(picture func() image.Image) *Component {
	c.picture = picture
	return c
}

// Resize satisfies tui.Component.
func (c *Component) Resize(width, height int) {
	c.width, c.height = width, height
	c.cells = nil
}

// Draw satisfies tui.Component.
func (c *Component) Draw(w term.Writer) {
	aspect := c.currentCellAspect()
	if c.drawPicture(w, aspect) {
		return
	}
	if c.cells == nil || aspect != c.cellAspect {
		c.cellAspect = aspect
		encode := Encode
		if c.style == Blocks {
			encode = EncodeBlocks
		}
		c.cells = encode(c.img, c.width, c.height, aspect)
	}
	for y, row := range c.cells {
		for x, cell := range row {
			w.SetCell(term.Coordinates{X: x, Y: y}, cell)
		}
	}
}

// drawPicture draws the picture over blank cells, reporting false when w
// does not draw images.
func (c *Component) drawPicture(w term.Writer, aspect float64) bool {
	if c.picture == nil {
		return false
	}
	size, ok := cell.PixelSizeFromContext(w.Context())
	if !ok {
		return false
	}
	s := &c.shown
	if s.src == nil {
		s.src, s.id = c.picture(), term.NewImageID()
	}
	if s.pixels == nil || s.width != c.width || s.height != c.height ||
		s.cellAspect != aspect || s.size != size {
		s.width, s.height, s.cellAspect, s.size = c.width, c.height, aspect, size
		s.cells, s.pixels = EncodePicture(c.img, s.src, c.width, c.height, aspect, size)
		s.version++
	}

	blank := term.NewCell(Blank, 1, term.Attributes{})
	for y := range c.height {
		for x := range c.width {
			w.SetCell(term.Coordinates{X: x, Y: y}, blank)
		}
	}
	if s.cells.Empty() {
		return true
	}
	return w.DrawImage(term.Image{
		Src:     s.pixels,
		ID:      s.id,
		Version: s.version,
		Pos:     term.Coordinates{X: s.cells.Min.X, Y: s.cells.Min.Y},
		Width:   s.cells.Dx(),
		Height:  s.cells.Dy(),
		Fit:     term.ImageFitFill,
	})
}

func (c *Component) currentCellAspect() float64 {
	if c.cellPixelSize == nil {
		return 0
	}
	w, h := c.cellPixelSize()
	if w <= 0 || h <= 0 {
		return 0
	}
	return float64(h) / float64(w)
}
