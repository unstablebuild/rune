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

package shader

import (
	"context"
	"image"
	"image/draw"
	"math"
	"slices"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// imageCutter cuts placements to the cells of a screen that show them.
type imageCutter struct {
	buf        *cell.BufferWriter
	cols, rows int
}

// cut appends to dst the parts of images over the cells of a cols by rows
// screen for which shows reports true, merging the cells of neighboring
// rows into as few placements as it can. It reads all of images before it
// appends to dst, so the two may share their backing array.
func (c *imageCutter) cut(
	dst, images []term.Image, cols, rows int, shows func(x, y int) bool,
) []term.Image {
	if len(images) == 0 || cols <= 0 || rows <= 0 {
		return dst
	}
	if c.buf == nil || c.cols != cols || c.rows != rows {
		c.buf = cell.NewBufferWriter(context.Background(), cols, rows)
		c.cols, c.rows = cols, rows
	}
	_ = c.buf.Clear(term.Attributes{})
	var area image.Rectangle
	for _, img := range images {
		c.buf.DrawImage(img)
		area = area.Union(img.Visible())
	}
	// A cell written over a placement hides it, which is how the buffer
	// cuts the placements to the cells left showing them.
	area = area.Intersect(image.Rect(0, 0, cols, rows))
	hide := term.NewCell(' ', 1, term.Attributes{})
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if !shows(x, y) {
				c.buf.SetCell(term.Coordinates{X: x, Y: y}, hide)
			}
		}
	}
	return append(dst, c.buf.Images()...)
}

// rgbaPixels returns the part of img's source it shows, with its origin
// at 0, 0, sharing the source's pixels when they already are.
func rgbaPixels(img term.Image) *image.RGBA {
	crop := cropOf(img)
	if rgba, ok := img.Src.(*image.RGBA); ok && crop == rgba.Rect && crop.Min == (image.Point{}) {
		return rgba
	}
	dst := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	draw.Draw(dst, dst.Rect, img.Src, crop.Min, draw.Src)
	return dst
}

// copyImage returns img with a copy of the pixels it shows, under an ID
// of its own, for placements kept past the frame they were drawn in.
func copyImage(img term.Image) term.Image {
	crop := cropOf(img)
	dst := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
	draw.Draw(dst, dst.Rect, img.Src, crop.Min, draw.Src)
	img.Src, img.Crop, img.ID, img.Version = dst, image.Rectangle{}, term.NewImageID(), 0
	return img
}

// copyImages returns images copied by copyImage, sharing a copy among
// the placements of a picture, such as the parts of one cut by a window.
func copyImages(images []term.Image) []term.Image {
	if len(images) == 0 {
		return nil
	}
	type picture struct {
		id   term.ImageID
		crop image.Rectangle
	}
	copies := make(map[picture]term.Image, len(images))
	out := make([]term.Image, len(images))
	for i, img := range images {
		key := picture{img.ID, cropOf(img)}
		c, ok := copies[key]
		// Placements without an ID cannot tell their pictures apart.
		if !ok || img.ID == 0 {
			c = copyImage(img)
			copies[key] = c
		}
		img.Src, img.Crop, img.ID, img.Version = c.Src, c.Crop, c.ID, c.Version
		out[i] = img
	}
	return out
}

// cropOf returns the rectangle of img's source it shows.
func cropOf(img term.Image) image.Rectangle {
	if img.Src == nil {
		return image.Rectangle{}
	}
	if img.Crop.Empty() {
		return img.Src.Bounds()
	}
	return img.Crop.Intersect(img.Src.Bounds())
}

// desaturatePixels desaturates the pixels of dst by amount, from 0 for
// none to 1 for gray, the way [shaderutils.DesaturateColor] does colors.
func desaturatePixels(dst *image.RGBA, amount float64) {
	if amount <= 0 {
		return
	}
	amount = math.Min(1, amount)
	for y := dst.Rect.Min.Y; y < dst.Rect.Max.Y; y++ {
		row := dst.Pix[dst.PixOffset(dst.Rect.Min.X, y):dst.PixOffset(dst.Rect.Max.X, y)]
		for i := 0; i < len(row); i += 4 {
			// The channels are premultiplied, which leaves the blend
			// toward their luminance unchanged.
			r, g, b := float64(row[i]), float64(row[i+1]), float64(row[i+2])
			lum := 0.299*r + 0.587*g + 0.114*b
			row[i] = uint8(math.Round(r + (lum-r)*amount))
			row[i+1] = uint8(math.Round(g + (lum-g)*amount))
			row[i+2] = uint8(math.Round(b + (lum-b)*amount))
		}
	}
}

// sampleCell is the cell size pictures are painted at, as shaders are not
// told the real one.
var sampleCell = cell.PixelSize{Width: 10, Height: 10 * rowScale}

// sampleGrid is how many samples, at most, a side of a cell takes of the
// picture over it.
const sampleGrid = 4

// pictureLayers are the layers pictures are composited in, from the
// bottom up.
var pictureLayers = [...]term.ImageLayer{
	term.ImageLayerBelowBackground, term.ImageLayerBelowText, term.ImageLayerAboveText,
}

func paintPictures(cells [][]term.Cell, images []term.Image) {
	rows, cols := canvasSize(cells)
	screen := image.Rect(0, 0, cols, rows)
	var defaultBg [][]bool
	for _, layer := range pictureLayers {
		for _, img := range images {
			if img.Layer != layer {
				continue
			}
			if layer == term.ImageLayerBelowBackground && defaultBg == nil {
				defaultBg = defaultBackgrounds(cells)
			}
			var shows [][]bool
			if layer == term.ImageLayerBelowBackground {
				shows = defaultBg
			}
			paintPicture(cells, img, screen, shows)
		}
	}
}

func paintPicture(cells [][]term.Cell, img term.Image, screen image.Rectangle, shows [][]bool) {
	crop := cropOf(img)
	covered := sampleCell.Covered(img).Intersect(screen)
	if crop.Empty() || covered.Empty() {
		return
	}
	x0, y0, x1, y1 := rasterArea(img, crop)
	scaleX, scaleY := float64(crop.Dx())/(x1-x0), float64(crop.Dy())/(y1-y0)
	cols := sampleCount(sampleCell.Width * scaleX)
	rows := sampleCount(sampleCell.Height * scaleY)
	samples := float64(cols * rows)
	for y := covered.Min.Y; y < covered.Max.Y; y++ {
		row := cells[y]
		for x := covered.Min.X; x < min(covered.Max.X, len(row)); x++ {
			if shows != nil && !shows[y][x] {
				continue
			}
			var r, g, b, a uint32
			for j := range rows {
				py := (float64(y) + (float64(j)+0.5)/float64(rows)) * sampleCell.Height
				if py < y0 || py >= y1 {
					continue
				}
				sy := crop.Min.Y + min(int((py-y0)*scaleY), crop.Dy()-1)
				for i := range cols {
					px := (float64(x) + (float64(i)+0.5)/float64(cols)) * sampleCell.Width
					if px < x0 || px >= x1 {
						continue
					}
					sx := crop.Min.X + min(int((px-x0)*scaleX), crop.Dx()-1)
					pr, pg, pb, pa := premultipliedAt(img.Src, sx, sy)
					r, g, b, a = r+pr, g+pg, b+pb, a+pa
				}
			}
			paintCell(row, x, img.Layer,
				float64(r)/samples, float64(g)/samples, float64(b)/samples, float64(a)/samples)
		}
	}
}

// rasterArea returns the pixels img's raster spans on cells the size of
// sampleCell, as its left, top, right and bottom edges.
func rasterArea(img term.Image, crop image.Rectangle) (x0, y0, x1, y1 float64) {
	b := img.Bounds()
	x0, y0 = float64(b.Min.X)*sampleCell.Width, float64(b.Min.Y)*sampleCell.Height
	w, h := float64(b.Dx())*sampleCell.Width, float64(b.Dy())*sampleCell.Height
	if img.Fit == term.ImageFitContain {
		scale := math.Min(w/float64(crop.Dx()), h/float64(crop.Dy()))
		cw, ch := float64(crop.Dx())*scale, float64(crop.Dy())*scale
		x0, y0 = x0+(w-cw)/2, y0+(h-ch)/2
		w, h = cw, ch
	}
	d := img.Offset.Add(img.RasterOffset)
	x0, y0 = x0+float64(d.X), y0+float64(d.Y)
	return x0, y0, x0 + w, y0 + h
}

func sampleCount(pixels float64) int {
	return max(1, min(sampleGrid, int(math.Ceil(pixels))))
}

// premultipliedAt returns the color of src at x, y, with 8 bits a channel
// premultiplied by its alpha.
func premultipliedAt(src image.Image, x, y int) (r, g, b, a uint32) {
	if rgba, ok := src.(*image.RGBA); ok {
		p := rgba.Pix[rgba.PixOffset(x, y):]
		return uint32(p[0]), uint32(p[1]), uint32(p[2]), uint32(p[3])
	}
	r, g, b, a = src.At(x, y).RGBA()
	return r >> 8, g >> 8, b >> 8, a >> 8
}

// paintCell paints the cell at x of row with the part of a picture on
// layer over it, whose color r, g, b is premultiplied by its alpha a,
// all in [0, 255].
func paintCell(row []term.Cell, x int, layer term.ImageLayer, r, g, b, a float64) {
	const half = 255 / 2.0
	if a <= 0 {
		return
	}
	c := &row[x]
	switch {
	case c.Bg.IsRGB():
		br, bg, bb := c.Bg.RGB()
		k := 1 - a/255
		c.Bg = roundRGB(r+float64(br)*k, g+float64(bg)*k, b+float64(bb)*k)
	case a >= half:
		c.Bg = roundRGB(r*255/a, g*255/a, b*255/a)
	default:
		return
	}
	if layer == term.ImageLayerAboveText && a >= half {
		claim([][]term.Cell{row}, x, 0)
		c.Ch, c.Width, c.Extra, c.Attrs = ' ', 1, nil, term.AttrNone
	}
}

func roundRGB(r, g, b float64) term.Color {
	return term.NewRGBColor(int32(math.Round(r)), int32(math.Round(g)), int32(math.Round(b)))
}

func defaultBackgrounds(cells [][]term.Cell) [][]bool {
	out := make([][]bool, len(cells))
	for y, row := range cells {
		out[y] = make([]bool, len(row))
		for x, c := range row {
			out[y][x] = c.Bg == term.ColorDefault
		}
	}
	return out
}

// looksSame reports whether a picture over a and b looks the same, which
// a text color does not change.
func looksSame(a, b term.Cell) bool {
	a.Fg = b.Fg
	return a == b
}

// painting is the state [ShadeImages] keeps across frames for shaders of
// cells.
type painting struct {
	drawn, painted [][]term.Cell
	kept           [][]bool
	cutter         imageCutter
}

var paintings = sync.Pool{New: func() any { return new(painting) }}

// shade returns the parts of images over the cells s leaves as painted.
func (p *painting) shade(s Shader, frame, total int, in [][]term.Cell, images []term.Image) []term.Image {
	p.drawn = copyCellsInto(p.drawn, in)
	paintPictures(in, images)
	p.painted = copyCellsInto(p.painted, in)
	s.Shade(frame, total, in)
	p.kept = slices.Grow(p.kept[:0], len(in))[:len(in)]
	for y, row := range in {
		p.kept[y] = slices.Grow(p.kept[y][:0], len(row))[:len(row)]
		for x, c := range row {
			painted, drawn := p.painted[y][x], p.drawn[y][x]
			p.kept[y][x] = looksSame(c, painted)
			if p.kept[y][x] && painted != drawn {
				// s colored the drawn glyph unless the picture hid it.
				if painted.Ch == drawn.Ch {
					drawn.Fg = c.Fg
				}
				row[x] = drawn
			}
		}
	}
	rows, cols := canvasSize(in)
	return p.cutter.cut(nil, images, cols, rows, func(x, y int) bool {
		return x < len(p.kept[y]) && p.kept[y][x]
	})
}
