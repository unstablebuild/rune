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
	"context"
	"image"
	"image/color"
	"image/draw"
	"math"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/gui/font"
	"unstable.build/rune/internal/term/gui/shaderdevice"
)

// texture is the GPU-side copy of a picture's pixels. It is keyed by
// term.ImageID and re-uploaded only when the placement's Version or the
// source bounds change, so a static picture costs no upload per frame.
// A new Version of the same size is uploaded into the same GPU image: a
// client streaming frames replaces its picture hundreds of times a
// second, and allocating a texture each time costs more than the
// upload.
type texture struct {
	img     *ebiten.Image
	version uint64
	bounds  image.Rectangle
	used    bool
	// crops are the parts of img placements with a Crop draw from.
	crops subImages
}

// textureKey keys the upload of a picture, which is uploaded once as
// itself and once as the coverage of a mask, so that each placement
// samples the pixels its Fill asks for.
type textureKey struct {
	id   term.ImageID
	mask bool
}

// imageLayer composites image placements over the cell frame. It
// retains one texture per term.ImageID and evicts the textures that
// were not placed in the frame just drawn. Placements are queued into
// batch and drawn when it is flushed.
type imageLayer struct {
	textures map[textureKey]*texture

	// scratch backs the conversion of sources that are not already a
	// tightly packed RGBA, and of masks. It is reused across frames and
	// across pictures within a frame.
	scratch *image.RGBA

	// white is the opaque picture solid fills are drawn from, in their
	// colour.
	white *ebiten.Image

	batch imageBatch
}

// placement is the pixel geometry of one image placement: src selects
// the texels in the picture's texture, area is where the whole picture
// would land, or where its tiles repeat across, and clip narrows the
// painting to the cells the placement still covers. src is empty for a
// fill without a mask.
type placement struct {
	src, area, clip image.Rectangle
}

// resolvePlacement computes img's pixel geometry within a dst of
// bounds, moved down by shift pixels along with the cells it covers,
// reporting false when the placement paints nothing.
func resolvePlacement(
	img term.Image, m *font.Manager, bounds image.Rectangle, shift int,
) (placement, bool) {
	visible := pixelSize(m).Covered(img)
	if visible.Empty() {
		return placement{}, false
	}
	var src image.Rectangle
	if !solidFill(img) {
		if src = cropRect(img); src.Empty() {
			return placement{}, false
		}
		// Crop is expressed in Src's coordinate space; the texture
		// always starts at the origin.
		src = src.Sub(img.Src.Bounds().Min)
	}

	area := cellRectToPixels(img.Bounds(), m)
	if img.Fit == term.ImageFitContain && !src.Empty() {
		area = containRect(src, area)
	}
	down := image.Pt(0, shift)
	area = area.Add(img.Offset).Add(img.RasterOffset).Add(down)
	if area.Empty() {
		return placement{}, false
	}
	clip := cellRectToPixels(visible, m).Add(down).
		Intersect(area).Intersect(bounds)
	if clip.Empty() {
		return placement{}, false
	}
	return placement{src: src, area: area, clip: clip}, true
}

// solidFill reports whether img paints its whole placement in its fill
// colour.
func solidFill(img term.Image) bool {
	return img.Fill && img.Src == nil
}

// fillScale is the vertex colour that paints fill, a premultiplied
// colour, over a picture of opaque white or a mask's coverage.
func fillScale(fill color.RGBA) [4]float32 {
	return [4]float32{
		float32(fill.R) / 0xff, float32(fill.G) / 0xff,
		float32(fill.B) / 0xff, float32(fill.A) / 0xff,
	}
}

// pixelSize is the size of the font manager's cell, which is what the
// frame's writer places images moved by pixels with, so that they land
// on the cells the renderer paints them on.
func pixelSize(m *font.Manager) cell.PixelSize {
	return cell.PixelSize{Width: m.PixelX(1), Height: m.PixelY(1)}
}

// frameContext returns ctx for the writer a frame is drawn into.
func frameContext(ctx context.Context, m *font.Manager) context.Context {
	return cell.ContextWithPixelSize(ctx, pixelSize(m))
}

// drawOne queues one placement to be painted into dst by the next
// flush, and reports the pixel rectangle it covers, which is empty when
// the placement paints nothing. shift moves it down as resolvePlacement
// does, and fill is the premultiplied colour of a Fill placement.
func (l *imageLayer) drawOne(
	dst *ebiten.Image, img term.Image, m *font.Manager, shift int, fill color.RGBA,
) image.Rectangle {
	p, ok := resolvePlacement(img, m, dst.Bounds(), shift)
	if !ok {
		return image.Rectangle{}
	}
	if solidFill(img) {
		// Fill replaces the pixels rather than blending over them, as a
		// cell's background does, so a translucent window background is
		// not composited twice.
		l.batch.add(dst, batchRun{src: l.whitePixel(), solid: true}, solidQuad(p.clip, fill))
		return p.clip
	}
	src, ok := l.source(img, p.src)
	if !ok {
		return image.Rectangle{}
	}
	tile := img.Fit == term.ImageFitTile
	var quad [4]ebiten.Vertex
	if tile {
		quad = tileQuad(p)
	} else {
		quad = placementQuad(p)
	}
	if img.Fill {
		setColor(&quad, fillScale(fill))
	}
	l.batch.add(dst, batchRun{src: src, tile: tile}, quad)
	return p.clip
}

// source returns the region crop of the GPU image img is drawn from, or
// false when it has none.
func (l *imageLayer) source(img term.Image, crop image.Rectangle) (*ebiten.Image, bool) {
	// A device's rendering is already on the GPU: it is drawn as it is,
	// with nothing to upload or to evict. Components place renderings
	// whole, so a crop is rare enough not to keep its sub-image.
	if src, ok := shaderdevice.Texture(img.Src); ok {
		if !crop.Eq(src.Bounds()) {
			src = src.SubImage(crop).(*ebiten.Image)
		}
		return src, true
	}
	tex := l.upload(img)
	if tex == nil {
		return nil, false
	}
	if !crop.Eq(tex.img.Bounds()) {
		return tex.crops.of(tex.img, crop), true
	}
	return tex.img, true
}

// flush paints the placements queued since the last flush.
func (l *imageLayer) flush() {
	l.batch.flush()
}

// whitePixel returns the image solid fills are drawn from, whose middle
// pixel is opaque white and surrounded by more of it, so that sampling
// there takes no other colour.
func (l *imageLayer) whitePixel() *ebiten.Image {
	if l.white == nil {
		l.white = ebiten.NewImage(3, 3)
		l.white.Fill(color.White)
	}
	return l.white
}

// corners returns r's corners, top-left, top-right, bottom-left and
// bottom-right, which is the order imageBatch.add joins them in.
func corners(r image.Rectangle) [4]image.Point {
	return [4]image.Point{r.Min, {X: r.Max.X, Y: r.Min.Y}, {X: r.Min.X, Y: r.Max.Y}, r.Max}
}

// placementQuad returns the corners of p.clip, each sampling the texel
// of p.src that stretching p.src over p.area puts there. Clipping the
// quad rather than the destination draws placements clipped to
// different pixels in one call.
func placementQuad(p placement) [4]ebiten.Vertex {
	sx := float64(p.src.Dx()) / float64(p.area.Dx())
	sy := float64(p.src.Dy()) / float64(p.area.Dy())
	var quad [4]ebiten.Vertex
	for i, corner := range corners(p.clip) {
		quad[i] = ebiten.Vertex{
			DstX: float32(corner.X), DstY: float32(corner.Y),
			SrcX:   float32(float64(p.src.Min.X) + float64(corner.X-p.area.Min.X)*sx),
			SrcY:   float32(float64(p.src.Min.Y) + float64(corner.Y-p.area.Min.Y)*sy),
			ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1,
		}
	}
	return quad
}

// tileQuad returns the corners of p.clip, each sampling the texel of
// p.src that tiles repeated one texel to a pixel from p.area's top-left
// corner put there. The texels run past p.src for the sampler to wrap
// them, so placements of one area clipped to adjacent pixels continue
// each other's tiles.
func tileQuad(p placement) [4]ebiten.Vertex {
	var quad [4]ebiten.Vertex
	for i, corner := range corners(p.clip) {
		texel := corner.Sub(p.area.Min).Add(p.src.Min)
		quad[i] = ebiten.Vertex{
			DstX: float32(corner.X), DstY: float32(corner.Y),
			SrcX: float32(texel.X), SrcY: float32(texel.Y),
			ColorR: 1, ColorG: 1, ColorB: 1, ColorA: 1,
		}
	}
	return quad
}

// solidQuad returns the corners of clip painted in fill, a premultiplied
// colour, from the middle pixel of whitePixel.
func solidQuad(clip image.Rectangle, fill color.RGBA) [4]ebiten.Vertex {
	var quad [4]ebiten.Vertex
	for i, corner := range corners(clip) {
		quad[i] = ebiten.Vertex{
			DstX: float32(corner.X), DstY: float32(corner.Y),
			SrcX: 1.5, SrcY: 1.5,
		}
	}
	setColor(&quad, fillScale(fill))
	return quad
}

func setColor(quad *[4]ebiten.Vertex, c [4]float32) {
	for i := range quad {
		quad[i].ColorR, quad[i].ColorG, quad[i].ColorB, quad[i].ColorA = c[0], c[1], c[2], c[3]
	}
}

// upload returns the texture for img, uploading its pixels when the
// picture is new or its Version changed, and marks it as used this
// frame. The texture of a Fill placement holds its mask's coverage.
func (l *imageLayer) upload(img term.Image) *texture {
	if img.Src == nil {
		return nil
	}
	bounds := img.Src.Bounds()
	if bounds.Empty() {
		return nil
	}
	if l.textures == nil {
		l.textures = make(map[textureKey]*texture)
	}
	key := textureKey{id: img.ID, mask: img.Fill}
	tex, ok := l.textures[key]
	if ok && tex.version == img.Version && tex.bounds.Eq(bounds) {
		tex.used = true
		return tex
	}
	if !ok {
		tex = new(texture)
		l.textures[key] = tex
	} else if !tex.bounds.Eq(bounds) {
		tex.img.Deallocate()
		tex.img = nil
	}
	if tex.img == nil {
		tex.img = ebiten.NewImage(bounds.Dx(), bounds.Dy())
	}
	if img.Fill {
		tex.img.WritePixels(l.coverage(img.Src, bounds))
	} else {
		tex.img.WritePixels(l.pixels(img.Src, bounds))
	}
	tex.version = img.Version
	tex.bounds = bounds
	tex.used = true
	return tex
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
	return l.copyPixels(src, b)
}

// coverage returns b's worth of src as the coverage of a mask: RGBA
// bytes holding each pixel's alpha in all four channels, which painted
// in a premultiplied vertex colour is that colour at the pixel's alpha.
// The returned slice is the layer's scratch buffer, valid until the
// next call.
func (l *imageLayer) coverage(src image.Image, b image.Rectangle) []byte {
	pix := l.copyPixels(src, b)
	for i := 0; i < len(pix); i += 4 {
		a := pix[i+3]
		pix[i], pix[i+1], pix[i+2] = a, a, a
	}
	return pix
}

// copyPixels copies b's worth of src into the layer's scratch buffer as
// RGBA bytes.
func (l *imageLayer) copyPixels(src image.Image, b image.Rectangle) []byte {
	w, h := b.Dx(), b.Dy()
	if l.scratch == nil || l.scratch.Rect.Dx() != w || l.scratch.Rect.Dy() != h {
		l.scratch = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	draw.Draw(l.scratch, l.scratch.Rect, src, b.Min, draw.Src)
	return l.scratch.Pix
}

func (l *imageLayer) evictUnused() {
	for key, tex := range l.textures {
		if tex.used {
			tex.used = false
			tex.crops.evictUnused()
			continue
		}
		tex.img.Deallocate()
		delete(l.textures, key)
	}
}

func (l *imageLayer) deallocate() {
	for key, tex := range l.textures {
		tex.img.Deallocate()
		delete(l.textures, key)
	}
	l.scratch = nil
	if l.white != nil {
		l.white.Deallocate()
		l.white = nil
	}
	l.batch = imageBatch{}
}

// triangleDrawer is what an imageBatch draws its runs into.
type triangleDrawer interface {
	DrawTriangles(
		vertices []ebiten.Vertex, indices []uint16, img *ebiten.Image,
		options *ebiten.DrawTrianglesOptions,
	)
}

// imageBatch accumulates the quads of consecutive placements that draw
// from one source in one way, and draws each such run in a single call
// once a placement of another run is added or it is flushed, so that
// placements are painted in the order they were added.
type imageBatch struct {
	dst      triangleDrawer
	run      batchRun
	vertices []ebiten.Vertex
	indices  []uint16
}

// batchRun is what the placements of a run share: their source, and
// whether it tiles across them or is the white of a solid fill, which
// replaces the pixels under it.
type batchRun struct {
	src   *ebiten.Image
	tile  bool
	solid bool
}

// maxBatchVertices is how many vertices uint16 indices address.
const maxBatchVertices = 1 << 16

var (
	// Vertex colours are premultiplied, which is what fills are, and
	// is the same as straight colours for the opaque white of pictures.
	pictureOptions = ebiten.DrawTrianglesOptions{
		ColorScaleMode: ebiten.ColorScaleModePremultipliedAlpha,
		Filter:         ebiten.FilterLinear,
	}
	tileOptions = ebiten.DrawTrianglesOptions{
		ColorScaleMode: ebiten.ColorScaleModePremultipliedAlpha,
		Address:        ebiten.AddressRepeat,
	}
	solidOptions = ebiten.DrawTrianglesOptions{
		ColorScaleMode: ebiten.ColorScaleModePremultipliedAlpha,
		Blend:          ebiten.BlendCopy,
	}
)

func (r batchRun) options() *ebiten.DrawTrianglesOptions {
	switch {
	case r.solid:
		return &solidOptions
	case r.tile:
		return &tileOptions
	default:
		return &pictureOptions
	}
}

// add queues quad to be drawn into dst as part of run.
func (b *imageBatch) add(dst triangleDrawer, run batchRun, quad [4]ebiten.Vertex) {
	if dst != b.dst || run != b.run || len(b.vertices)+len(quad) > maxBatchVertices {
		b.flush()
		b.dst, b.run = dst, run
	}
	n := uint16(len(b.vertices))
	b.vertices = append(b.vertices, quad[:]...)
	b.indices = append(b.indices, n, n+1, n+2, n+1, n+2, n+3)
}

// flush draws the run being accumulated.
func (b *imageBatch) flush() {
	if len(b.vertices) > 0 {
		b.dst.DrawTriangles(b.vertices, b.indices, b.run.src, b.run.options())
	}
	b.dst, b.run = nil, batchRun{}
	b.vertices, b.indices = b.vertices[:0], b.indices[:0]
}

// subImages keeps the sub-images of one image across frames, so that a
// region drawn into every frame reuses its sub-image, and the vertex
// buffer ebiten grows for each image, instead of allocating both again.
type subImages struct {
	parent  *ebiten.Image
	regions map[image.Rectangle]*subImage
}

type subImage struct {
	img  *ebiten.Image
	used bool
}

// of returns the sub-image of parent covering r. Asking for a region of
// another parent releases the regions of the previous one.
func (s *subImages) of(parent *ebiten.Image, r image.Rectangle) *ebiten.Image {
	if parent != s.parent {
		clear(s.regions)
		s.parent = parent
	}
	if s.regions == nil {
		s.regions = make(map[image.Rectangle]*subImage)
	}
	sub, ok := s.regions[r]
	if !ok {
		sub = &subImage{img: parent.SubImage(r).(*ebiten.Image)}
		s.regions[r] = sub
	}
	sub.used = true
	return sub.img
}

// evictUnused releases the regions not asked for since the last call.
func (s *subImages) evictUnused() {
	for r, sub := range s.regions {
		if !sub.used {
			delete(s.regions, r)
			continue
		}
		sub.used = false
	}
}

func (s *subImages) deallocate() {
	s.parent = nil
	s.regions = nil
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

// cellRectToPixels converts a right-exclusive cell rectangle to pixels,
// with the overlap the glyph renderer applies to cells.
func cellRectToPixels(r image.Rectangle, m *font.Manager) image.Rectangle {
	return pixelSize(m).Pixels(r)
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
