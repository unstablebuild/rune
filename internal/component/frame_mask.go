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
	"image/color"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Sides is a set of the sides of a cell.
type Sides uint8

// The sides of a cell, combined with |.
const (
	SideTop Sides = 1 << iota
	SideRight
	SideBottom
	SideLeft
)

// RingSides returns the sides of the cell at p that face away from the
// inside of area, for a cell on area's outermost ring, and 0 for any
// other cell. A ring whose cells would face both ways, in an area less
// than 3 cells wide or tall, has no inside and also returns 0.
func RingSides(p image.Point, area image.Rectangle) Sides {
	if !p.In(area) || area.Dx() < 3 || area.Dy() < 3 {
		return 0
	}
	var s Sides
	if p.Y == area.Min.Y {
		s |= SideTop
	}
	if p.Y == area.Max.Y-1 {
		s |= SideBottom
	}
	if p.X == area.Min.X {
		s |= SideLeft
	}
	if p.X == area.Max.X-1 {
		s |= SideRight
	}
	return s
}

// maskOpaque is the alpha from which a sprite's pixel is part of its
// line rather than a pixel either side of it shows through.
const maskOpaque = 0x80

// maskClear is the alpha below which a sprite's pixel is clear: what is
// behind it shows through it nearly unchanged, as it does not through a
// translucent shadow.
const maskClear = 0x20

// maxFrameMasks bounds how many masks FrameMasks keeps. Frame sprites
// are few and shared, so reaching it means the sprites were reloaded and
// the masks of the old ones are no longer drawn.
const maxFrameMasks = 256

// FrameMasks paints the inside of the line a frame sprite draws: a frame
// cell has a single background, which shows on both sides of a line
// drawn through the middle of it, and the mask lets the side facing the
// frame's content take the content's colour while the cell's background
// shows outside. It keeps one mask per sprite and set of outside sides.
// It is not safe for concurrent use.
type FrameMasks struct {
	masks map[frameMaskKey]frameMask
}

type frameMaskKey struct {
	id       term.ImageID
	version  uint64
	crop     image.Rectangle
	outside  Sides
	enclosed bool
}

// frameMask is the mask of a sprite, or a nil src when its line does not
// part its inside from its outside.
type frameMask struct {
	src *image.RGBA
	id  term.ImageID
}

// Inside returns a fill of c over the part of sprite, the image of a
// frame cell whose outside sides face away from the frame, on the
// frame's side of the line the sprite draws, placed where sprite is and
// under the text. It reports false when sprite is not a picture
// stretched over a single cell, when outside is not one side or two
// adjacent ones, when no transparent pixel lies on the frame's side of
// the line, and when the sprite's transparent pixels connect that side
// to an outside one, as those of a sprite that draws no line across its
// cell do.
func (m *FrameMasks) Inside(sprite term.Image, outside Sides, c term.Color) (term.Image, bool) {
	return m.fill(sprite, outside, false, c)
}

// Enclosed returns a fill of c over the clear pixels of sprite, the image
// of a frame cell whose outside sides face away from the frame, that no
// clear pixel joins to an outside side, placed where sprite is and under
// the text: those inside the frame's line, or between its face and a
// translucent shadow outside it, such as the channel a progress bar's
// image leaves for the bar. It reports false when sprite is not a
// picture stretched over a single cell, when outside is empty and when
// sprite encloses no clear pixel.
func (m *FrameMasks) Enclosed(sprite term.Image, outside Sides, c term.Color) (term.Image, bool) {
	return m.fill(sprite, outside, true, c)
}

// fill returns the fill of c over the mask of sprite that Inside, or
// Enclosed when enclosed is set, derives.
func (m *FrameMasks) fill(sprite term.Image, outside Sides, enclosed bool, c term.Color) (term.Image, bool) {
	if sprite.Src == nil || sprite.Fill || sprite.Fit != term.ImageFitFill ||
		sprite.Width != 1 || sprite.Height != 1 {
		return term.Image{}, false
	}
	crop := sprite.Src.Bounds()
	if !sprite.Crop.Empty() {
		crop = sprite.Crop.Intersect(crop)
	}
	key := frameMaskKey{id: sprite.ID, version: sprite.Version, crop: crop, outside: outside, enclosed: enclosed}
	mask, ok := m.masks[key]
	if !ok || sprite.ID == 0 {
		if enclosed {
			mask = frameMask{src: enclosedMask(sprite.Src, crop, outside)}
		} else {
			mask = frameMask{src: insideMask(sprite.Src, crop, outside)}
		}
		if mask.src != nil {
			mask.id = term.NewImageID()
		}
		if m.masks == nil || len(m.masks) >= maxFrameMasks {
			m.masks = make(map[frameMaskKey]frameMask)
		}
		m.masks[key] = mask
	}
	if mask.src == nil {
		return term.Image{}, false
	}
	fill := sprite
	fill.Src, fill.ID, fill.Version, fill.Crop = mask.src, mask.id, 0, image.Rectangle{}
	fill.Fill, fill.FillColor = true, c
	fill.Layer = term.ImageLayerBelowText
	return fill, true
}

// FrameInsideWriter forwards everything to Writer and, after each image
// drawn over a cell of the outermost ring of Area, as those of a frame's
// border are, a fill of Inside over the part of it on the frame's side
// of its line, as FrameMasks.Inside derives it with Masks, or over the
// clear pixels it encloses, as FrameMasks.Enclosed does, when Enclosed.
type FrameInsideWriter struct {
	term.Writer
	Area     image.Rectangle
	Inside   term.Color
	Masks    *FrameMasks
	Enclosed bool
}

// DrawImage satisfies term.Writer.
func (w *FrameInsideWriter) DrawImage(img term.Image) bool {
	if !w.Writer.DrawImage(img) {
		return false
	}
	if img.Fill || img.Layer != term.ImageLayerAboveText {
		return true
	}
	sides := RingSides(image.Pt(img.Pos.X, img.Pos.Y), w.Area)
	if sides == 0 {
		return true
	}
	if fill, ok := w.Masks.fill(img, sides, w.Enclosed, w.Inside); ok {
		w.Writer.DrawImage(fill)
	}
	return true
}

// insideMask returns the mask of the pixels of crop of src on the side
// of its line away from outside, or nil if there are none or they reach
// an outside side. The flood starts from the side opposite a single
// outside one, or from the corner opposite two adjacent ones, and
// spreads through the pixels a line does not cover. The mask also takes
// the pixels around the flood, under the edge of the line, for its
// antialiased pixels to blend with the inside colour.
func insideMask(src image.Image, crop image.Rectangle, outside Sides) *image.RGBA {
	w, h := crop.Dx(), crop.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	transparent := func(x, y int) bool {
		_, _, _, a := src.At(crop.Min.X+x, crop.Min.Y+y).RGBA()
		return a>>8 < maskOpaque
	}
	var seeds []image.Point
	switch outside {
	case SideTop:
		seeds = pixelRow(w, h-1)
	case SideBottom:
		seeds = pixelRow(w, 0)
	case SideLeft:
		seeds = pixelColumn(h, w-1)
	case SideRight:
		seeds = pixelColumn(h, 0)
	case SideTop | SideLeft:
		seeds = []image.Point{{w - 1, h - 1}}
	case SideTop | SideRight:
		seeds = []image.Point{{0, h - 1}}
	case SideBottom | SideLeft:
		seeds = []image.Point{{w - 1, 0}}
	case SideBottom | SideRight:
		seeds = []image.Point{{0, 0}}
	default:
		return nil
	}
	onOutside := func(p image.Point) bool {
		return outside&SideTop != 0 && p.Y == 0 || outside&SideBottom != 0 && p.Y == h-1 ||
			outside&SideLeft != 0 && p.X == 0 || outside&SideRight != 0 && p.X == w-1
	}

	inside := make([]bool, w*h)
	stack := seeds[:0:0]
	for _, p := range seeds {
		if transparent(p.X, p.Y) && !inside[p.Y*w+p.X] {
			inside[p.Y*w+p.X] = true
			stack = append(stack, p)
		}
	}
	if len(stack) == 0 {
		return nil
	}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if onOutside(p) {
			return nil
		}
		stack = spread(stack, p, w, h, inside, transparent)
	}
	return dilatedMask(inside, w, h)
}

// enclosedMask returns the mask of the clear pixels of crop of src that
// no clear pixel joins to an outside side, or nil if there are none. The
// mask also takes the pixels around them, as insideMask's does.
func enclosedMask(src image.Image, crop image.Rectangle, outside Sides) *image.RGBA {
	w, h := crop.Dx(), crop.Dy()
	if w <= 0 || h <= 0 || outside == 0 {
		return nil
	}
	isClear := func(x, y int) bool {
		_, _, _, a := src.At(crop.Min.X+x, crop.Min.Y+y).RGBA()
		return a>>8 < maskClear
	}
	var seeds []image.Point
	if outside&SideTop != 0 {
		seeds = append(seeds, pixelRow(w, 0)...)
	}
	if outside&SideBottom != 0 {
		seeds = append(seeds, pixelRow(w, h-1)...)
	}
	if outside&SideLeft != 0 {
		seeds = append(seeds, pixelColumn(h, 0)...)
	}
	if outside&SideRight != 0 {
		seeds = append(seeds, pixelColumn(h, w-1)...)
	}
	open := make([]bool, w*h)
	var stack []image.Point
	for _, p := range seeds {
		if isClear(p.X, p.Y) && !open[p.Y*w+p.X] {
			open[p.Y*w+p.X] = true
			stack = append(stack, p)
		}
	}
	for len(stack) > 0 {
		p := stack[len(stack)-1]
		stack = spread(stack[:len(stack)-1], p, w, h, open, isClear)
	}
	enclosed := make([]bool, w*h)
	found := false
	for y := range h {
		for x := range w {
			if isClear(x, y) && !open[y*w+x] {
				enclosed[y*w+x], found = true, true
			}
		}
	}
	if !found {
		return nil
	}
	return dilatedMask(enclosed, w, h)
}

// spread marks in marked the neighbours of p, in a picture w by h, that
// pass reports and are not marked yet, and pushes them onto stack.
func spread(stack []image.Point, p image.Point, w, h int, marked []bool, pass func(x, y int) bool) []image.Point {
	for _, n := range [4]image.Point{{p.X + 1, p.Y}, {p.X - 1, p.Y}, {p.X, p.Y + 1}, {p.X, p.Y - 1}} {
		if n.X < 0 || n.Y < 0 || n.X >= w || n.Y >= h || marked[n.Y*w+n.X] || !pass(n.X, n.Y) {
			continue
		}
		marked[n.Y*w+n.X] = true
		stack = append(stack, n)
	}
	return stack
}

// dilatedMask returns the mask of the pixels of a picture w by h that in
// holds and of those next to them.
func dilatedMask(in []bool, w, h int) *image.RGBA {
	mask := image.NewRGBA(image.Rect(0, 0, w, h))
	white := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	for y := range h {
		for x := range w {
			if in[y*w+x] ||
				x > 0 && in[y*w+x-1] || x < w-1 && in[y*w+x+1] ||
				y > 0 && in[(y-1)*w+x] || y < h-1 && in[(y+1)*w+x] {
				mask.SetRGBA(x, y, white)
			}
		}
	}
	return mask
}

// pixelRow returns the points of row y of a picture w pixels wide.
func pixelRow(w, y int) []image.Point {
	pts := make([]image.Point, w)
	for x := range pts {
		pts[x] = image.Pt(x, y)
	}
	return pts
}

// pixelColumn returns the points of column x of a picture h pixels tall.
func pixelColumn(h, x int) []image.Point {
	pts := make([]image.Point, h)
	for y := range pts {
		pts[y] = image.Pt(x, y)
	}
	return pts
}
