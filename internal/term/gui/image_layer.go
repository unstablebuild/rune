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

package gui

import (
	"image"
	"image/draw"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/gui/font"
)

// texture is the GPU-side copy of a picture's pixels. It is keyed by
// term.ImageID and re-uploaded only when the placement's Version or the
// source bounds change, so a static picture costs a single draw call
// per frame.
type texture struct {
	img     *ebiten.Image
	version uint64
	bounds  image.Rectangle
	used    bool
}

// imageLayer composites image placements over the cell frame. It
// retains one texture per term.ImageID and evicts the textures that
// were not placed in the frame just drawn.
type imageLayer struct {
	textures map[term.ImageID]*texture

	// scratch backs the conversion of sources that are not already a
	// tightly packed RGBA. It is reused across frames and across
	// pictures within a frame.
	scratch *image.RGBA
}

// placement is the pixel geometry of one image placement: src selects
// the texels in the picture's texture, area is where the whole picture
// would land, and clip narrows the painting to the cells the placement
// still covers.
type placement struct {
	src, area, clip image.Rectangle
}

// resolvePlacement computes img's pixel geometry within a dst of
// bounds, reporting false when the placement paints nothing.
func resolvePlacement(
	img term.Image, m *font.Manager, offX, offY float64,
	bounds image.Rectangle,
) (placement, bool) {
	visible := img.Visible()
	src := cropRect(img)
	if visible.Empty() || src.Empty() {
		return placement{}, false
	}
	// Crop is expressed in Src's coordinate space; the texture always
	// starts at the origin.
	src = src.Sub(img.Src.Bounds().Min)

	area := cellRectToPixels(img.Bounds(), m, offX, offY)
	if img.Fit == term.ImageFitContain {
		area = containRect(src, area)
	}
	area = area.Add(img.Offset)
	if area.Empty() {
		return placement{}, false
	}
	clip := cellRectToPixels(visible, m, offX, offY).
		Intersect(area).Intersect(bounds)
	if clip.Empty() {
		return placement{}, false
	}
	return placement{src: src, area: area, clip: clip}, true
}

// drawOne paints one placement and reports the pixel rectangle it
// covered, which is empty when the placement painted nothing.
func (l *imageLayer) drawOne(
	dst *ebiten.Image, img term.Image,
	m *font.Manager, offX, offY float64,
) image.Rectangle {
	p, ok := resolvePlacement(img, m, offX, offY, dst.Bounds())
	if !ok {
		return image.Rectangle{}
	}
	tex := l.texture(img)
	if tex == nil {
		return image.Rectangle{}
	}

	var op ebiten.DrawImageOptions
	op.GeoM.Scale(
		float64(p.area.Dx())/float64(p.src.Dx()),
		float64(p.area.Dy())/float64(p.src.Dy()),
	)
	op.GeoM.Translate(float64(p.area.Min.X), float64(p.area.Min.Y))
	op.Filter = ebiten.FilterLinear

	target := dst
	if !p.clip.Eq(dst.Bounds()) {
		target = dst.SubImage(p.clip).(*ebiten.Image)
	}
	target.DrawImage(tex.SubImage(p.src).(*ebiten.Image), &op)
	return p.clip
}

// texture returns the uploaded pixels for img, uploading them when the
// picture is new or its Version changed, and marks it as used this
// frame.
func (l *imageLayer) texture(img term.Image) *ebiten.Image {
	if img.Src == nil {
		return nil
	}
	bounds := img.Src.Bounds()
	if bounds.Empty() {
		return nil
	}
	if l.textures == nil {
		l.textures = make(map[term.ImageID]*texture)
	}
	tex, ok := l.textures[img.ID]
	if ok && tex.version == img.Version && tex.bounds.Eq(bounds) {
		tex.used = true
		return tex.img
	}
	if ok {
		tex.img.Deallocate()
	} else {
		tex = new(texture)
		l.textures[img.ID] = tex
	}
	tex.img = ebiten.NewImage(bounds.Dx(), bounds.Dy())
	tex.img.WritePixels(l.pixels(img.Src, bounds))
	tex.version = img.Version
	tex.bounds = bounds
	tex.used = true
	return tex.img
}

// pixels returns b's worth of RGBA bytes for src. The returned slice
// may alias src or the layer's scratch buffer; it is only valid until
// the next call.
func (l *imageLayer) pixels(src image.Image, b image.Rectangle) []byte {
	w, h := b.Dx(), b.Dy()
	if rgba, ok := src.(*image.RGBA); ok &&
		rgba.Rect.Min == (image.Point{}) && rgba.Stride == 4*w {
		return rgba.Pix[:4*w*h]
	}
	if l.scratch == nil || l.scratch.Rect.Dx() != w || l.scratch.Rect.Dy() != h {
		l.scratch = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	draw.Draw(l.scratch, l.scratch.Rect, src, b.Min, draw.Src)
	return l.scratch.Pix
}

func (l *imageLayer) evictUnused() {
	for id, tex := range l.textures {
		if tex.used {
			tex.used = false
			continue
		}
		tex.img.Deallocate()
		delete(l.textures, id)
	}
}

func (l *imageLayer) deallocate() {
	for id, tex := range l.textures {
		tex.img.Deallocate()
		delete(l.textures, id)
	}
	l.scratch = nil
}

// cropRect resolves a placement's source rectangle in Src's coordinate
// space. A zero Crop selects all of Src.
func cropRect(img term.Image) image.Rectangle {
	if img.Src == nil {
		return image.Rectangle{}
	}
	b := img.Src.Bounds()
	if img.Crop.Empty() {
		return b
	}
	return img.Crop.Intersect(b)
}

// cellRectToPixels converts a right-exclusive cell rectangle to pixels.
// It goes through the font manager rather than multiplying by the cell
// size so that the cell overlap the glyph renderer applies is respected.
func cellRectToPixels(
	r image.Rectangle, m *font.Manager, offX, offY float64,
) image.Rectangle {
	return image.Rect(
		int(math.Round(m.PixelX(r.Min.X)+offX)),
		int(math.Round(m.PixelY(r.Min.Y)+offY)),
		int(math.Round(m.PixelX(r.Max.X)+offX)),
		int(math.Round(m.PixelY(r.Max.Y)+offY)),
	)
}

// containRect scales src uniformly to the largest rectangle that fits
// in area and centres it there.
func containRect(src, area image.Rectangle) image.Rectangle {
	sw, sh := src.Dx(), src.Dy()
	if sw <= 0 || sh <= 0 || area.Empty() {
		return area
	}
	scale := math.Min(
		float64(area.Dx())/float64(sw),
		float64(area.Dy())/float64(sh),
	)
	w := int(math.Round(float64(sw) * scale))
	h := int(math.Round(float64(sh) * scale))
	if w <= 0 || h <= 0 {
		return image.Rectangle{}
	}
	x := area.Min.X + (area.Dx()-w)/2
	y := area.Min.Y + (area.Dy()-h)/2
	return image.Rect(x, y, x+w, y+h)
}
