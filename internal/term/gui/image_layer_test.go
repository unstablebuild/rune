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
	"image/color"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/gui/font"
)

func testFontManager(t *testing.T) *font.Manager {
	t.Helper()
	m, err := font.NewManager(0, 0)
	require.NoError(t, err)
	m.SetDeviceScale(1)
	require.NoError(t, m.SetSize(15))
	return m
}

func solidRGBA(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1] = c.R, c.G
		img.Pix[i+2], img.Pix[i+3] = c.B, c.A
	}
	return img
}

func TestCellRectToPixels(t *testing.T) {
	m := testFontManager(t)
	got := cellRectToPixels(image.Rect(1, 2, 4, 5), m, 3, 7)
	want := image.Rect(
		int(m.PixelX(1))+3, int(m.PixelY(2))+7,
		int(m.PixelX(4))+3, int(m.PixelY(5))+7,
	)
	// Rounding may differ by a pixel from truncation; compare loosely on
	// each edge so the test tracks the font manager, not the rounding.
	assert.InDelta(t, want.Min.X, got.Min.X, 1)
	assert.InDelta(t, want.Min.Y, got.Min.Y, 1)
	assert.InDelta(t, want.Max.X, got.Max.X, 1)
	assert.InDelta(t, want.Max.Y, got.Max.Y, 1)
}

func TestContainRect(t *testing.T) {
	tests := []struct {
		name string
		src  image.Rectangle
		area image.Rectangle
		want image.Rectangle
	}{
		{
			name: "same aspect fills the area",
			src:  image.Rect(0, 0, 10, 10),
			area: image.Rect(0, 0, 100, 100),
			want: image.Rect(0, 0, 100, 100),
		},
		{
			name: "wide source is letterboxed vertically",
			src:  image.Rect(0, 0, 20, 10),
			area: image.Rect(0, 0, 100, 100),
			want: image.Rect(0, 25, 100, 75),
		},
		{
			name: "tall source is pillarboxed horizontally",
			src:  image.Rect(0, 0, 10, 20),
			area: image.Rect(0, 0, 100, 100),
			want: image.Rect(25, 0, 75, 100),
		},
		{
			name: "offset area keeps the centring",
			src:  image.Rect(0, 0, 20, 10),
			area: image.Rect(10, 10, 110, 110),
			want: image.Rect(10, 35, 110, 85),
		},
		{
			name: "empty area stays empty",
			src:  image.Rect(0, 0, 10, 10),
			area: image.Rectangle{},
			want: image.Rectangle{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, containRect(tt.src, tt.area))
		})
	}
}

func TestCropRect(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	tests := []struct {
		name string
		img  term.Image
		want image.Rectangle
	}{
		{name: "nil source", img: term.Image{}, want: image.Rectangle{}},
		{
			name: "zero crop selects all of Src",
			img:  term.Image{Src: src},
			want: image.Rect(0, 0, 8, 8),
		},
		{
			name: "crop is honoured",
			img:  term.Image{Src: src, Crop: image.Rect(2, 2, 6, 6)},
			want: image.Rect(2, 2, 6, 6),
		},
		{
			name: "crop is confined to Src",
			img:  term.Image{Src: src, Crop: image.Rect(4, 4, 99, 99)},
			want: image.Rect(4, 4, 8, 8),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, cropRect(tt.img))
		})
	}
}

// TestImageLayerTextureReuse asserts pixel identity is (ID, Version):
// re-placing the same picture reuses the upload, and bumping Version
// replaces it.
func TestImageLayerTextureReuse(t *testing.T) {
	benchdraw.BeginFrame(t)
	defer benchdraw.EndFrame(t)

	var l imageLayer
	defer l.deallocate()

	src := solidRGBA(4, 4, color.RGBA{R: 255, A: 255})
	img := term.Image{Src: src, ID: term.NewImageID(), Width: 2, Height: 2}

	first := l.texture(img)
	require.NotNil(t, first)
	assert.Same(t, first, l.texture(img), "same version must reuse the upload")

	img.Version++
	second := l.texture(img)
	require.NotNil(t, second)
	assert.NotSame(t, first, second, "a new version must re-upload")

	img.Src = solidRGBA(8, 8, color.RGBA{B: 255, A: 255})
	third := l.texture(img)
	require.NotNil(t, third)
	assert.NotSame(t, second, third, "new source bounds must re-upload")
	assert.Equal(t, image.Rect(0, 0, 8, 8), third.Bounds())
}

func TestImageLayerTextureRejectsEmptySource(t *testing.T) {
	var l imageLayer
	defer l.deallocate()

	assert.Nil(t, l.texture(term.Image{Width: 2, Height: 2}), "nil Src")
	assert.Nil(t, l.texture(term.Image{
		Src: image.NewRGBA(image.Rectangle{}), Width: 2, Height: 2,
	}), "empty Src bounds")
	assert.Empty(t, l.textures)
}

// TestImageLayerEvictsUnplacedPictures asserts a picture that stops
// being placed releases its texture on the next frame, so retained
// pixels do not outlive the content that drew them.
func TestImageLayerEvictsUnplacedPictures(t *testing.T) {
	m := testFontManager(t)
	dst := ebiten.NewImage(200, 200)
	t.Cleanup(dst.Deallocate)

	var l imageLayer
	defer l.deallocate()

	src := solidRGBA(4, 4, color.RGBA{G: 255, A: 255})
	a := term.Image{Src: src, ID: term.NewImageID(), Width: 2, Height: 2}
	b := term.Image{
		Src: src, ID: term.NewImageID(),
		Pos: term.Coordinates{X: 4}, Width: 2, Height: 2,
	}

	benchdraw.BeginFrame(t)
	drawFrame(&l, dst, m, a, b)
	benchdraw.EndFrame(t)
	require.Len(t, l.textures, 2)

	benchdraw.BeginFrame(t)
	drawFrame(&l, dst, m, a)
	benchdraw.EndFrame(t)
	assert.Len(t, l.textures, 1)
	assert.Contains(t, l.textures, a.ID)

	benchdraw.BeginFrame(t)
	drawFrame(&l, dst, m)
	benchdraw.EndFrame(t)
	assert.Empty(t, l.textures, "an empty frame releases every texture")
}

// drawFrame paints one frame's worth of placements and runs the layer's
// end-of-frame eviction, as the renderer does across its layers.
func drawFrame(
	l *imageLayer, dst *ebiten.Image, m *font.Manager, images ...term.Image,
) {
	for _, img := range images {
		l.drawOne(dst, img, m, 0, 0)
	}
	l.evictUnused()
}

// TestImageLayerDrawSkipsInvisiblePlacements asserts placements that
// cover nothing never reach the GPU, so they cost no upload.
func TestImageLayerDrawSkipsInvisiblePlacements(t *testing.T) {
	m := testFontManager(t)
	dst := ebiten.NewImage(200, 200)
	t.Cleanup(dst.Deallocate)

	src := solidRGBA(4, 4, color.RGBA{A: 255})
	clipped, ok := term.Image{
		Src: src, ID: term.NewImageID(), Width: 2, Height: 2,
	}.Clipped(image.Rect(50, 50, 60, 60))
	require.False(t, ok)

	tests := []struct {
		name string
		img  term.Image
	}{
		{name: "fully clipped", img: clipped},
		{
			name: "zero-sized placement",
			img:  term.Image{Src: src, ID: term.NewImageID()},
		},
		{
			name: "empty crop",
			img: term.Image{
				Src: src, ID: term.NewImageID(), Width: 2, Height: 2,
				Crop: image.Rect(9, 9, 12, 12),
			},
		},
		{
			name: "nil source",
			img:  term.Image{ID: term.NewImageID(), Width: 2, Height: 2},
		},
		{
			name: "entirely outside the destination",
			img: term.Image{
				Src: src, ID: term.NewImageID(),
				Pos: term.Coordinates{X: 400, Y: 400}, Width: 2, Height: 2,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := resolvePlacement(tt.img, m, 0, 0, dst.Bounds())
			assert.False(t, ok, "placement must resolve to nothing")

			var l imageLayer
			defer l.deallocate()
			benchdraw.BeginFrame(t)
			drawFrame(&l, dst, m, tt.img)
			benchdraw.EndFrame(t)
			assert.Empty(t, l.textures, "nothing must be uploaded")
		})
	}
}

// TestResolvePlacement asserts the pixel geometry a placement resolves
// to: where the picture lands, which texels it samples, and how the
// clip and the render offset narrow or move it.
func TestResolvePlacement(t *testing.T) {
	m := testFontManager(t)
	bounds := image.Rect(0, 0, 2000, 2000)
	src := solidRGBA(8, 4, color.RGBA{A: 255})

	cellRect := func(x0, y0, x1, y1 int, offX, offY float64) image.Rectangle {
		return cellRectToPixels(image.Rect(x0, y0, x1, y1), m, offX, offY)
	}

	tests := []struct {
		name       string
		img        term.Image
		offX, offY float64
		wantSrc    image.Rectangle
		wantArea   image.Rectangle
		wantClip   image.Rectangle
	}{
		{
			name: "fill covers the whole cell rectangle",
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 1, Y: 1},
				Width: 4, Height: 3,
			},
			wantSrc:  image.Rect(0, 0, 8, 4),
			wantArea: cellRect(1, 1, 5, 4, 0, 0),
			wantClip: cellRect(1, 1, 5, 4, 0, 0),
		},
		{
			name: "crop selects a sub-rectangle of the texture",
			img: term.Image{
				Src: src, Crop: image.Rect(2, 1, 6, 3),
				Width: 4, Height: 3,
			},
			wantSrc:  image.Rect(2, 1, 6, 3),
			wantArea: cellRect(0, 0, 4, 3, 0, 0),
			wantClip: cellRect(0, 0, 4, 3, 0, 0),
		},
		{
			name: "the render offset moves the placement",
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 2, Y: 1},
				Width: 3, Height: 2,
			},
			offX:     30,
			offY:     20,
			wantSrc:  image.Rect(0, 0, 8, 4),
			wantArea: cellRect(2, 1, 5, 3, 30, 20),
			wantClip: cellRect(2, 1, 5, 3, 30, 20),
		},
		{
			name: "the pixel offset shifts the raster inside its cells",
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 1, Y: 1},
				Offset: image.Pt(3, 5), Width: 4, Height: 3,
			},
			wantSrc:  image.Rect(0, 0, 8, 4),
			wantArea: cellRect(1, 1, 5, 4, 0, 0).Add(image.Pt(3, 5)),
			wantClip: cellRect(1, 1, 5, 4, 0, 0).Add(image.Pt(3, 5)).
				Intersect(cellRect(1, 1, 5, 4, 0, 0)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := resolvePlacement(tt.img, m, tt.offX, tt.offY, bounds)
			require.True(t, ok)
			assert.Equal(t, tt.wantSrc, p.src, "src")
			assert.Equal(t, tt.wantArea, p.area, "area")
			assert.Equal(t, tt.wantClip, p.clip, "clip")
		})
	}
}

// TestResolvePlacementClipNarrowsPainting asserts a clipped placement
// still scales as if unclipped and only narrows what is painted. That
// is what lets a scrolled pane hide part of a picture without the
// caller re-cropping it.
func TestResolvePlacementClipNarrowsPainting(t *testing.T) {
	m := testFontManager(t)
	bounds := image.Rect(0, 0, 2000, 2000)

	img, ok := term.Image{
		Src: solidRGBA(8, 8, color.RGBA{A: 255}), Width: 4, Height: 4,
	}.Clipped(image.Rect(0, 0, 4, 2))
	require.True(t, ok)

	p, ok := resolvePlacement(img, m, 0, 0, bounds)
	require.True(t, ok)
	assert.Equal(t, cellRectToPixels(image.Rect(0, 0, 4, 4), m, 0, 0), p.area,
		"scaling ignores the clip")
	assert.Equal(t, cellRectToPixels(image.Rect(0, 0, 4, 2), m, 0, 0), p.clip,
		"painting is confined to the visible cells")
	assert.Equal(t, image.Rect(0, 0, 8, 8), p.src, "the crop is unchanged")
}

// TestResolvePlacementContainPreservesAspect asserts ImageFitContain
// scales uniformly and centres the result in the cell rectangle.
func TestResolvePlacementContainPreservesAspect(t *testing.T) {
	m := testFontManager(t)
	bounds := image.Rect(0, 0, 2000, 2000)

	img := term.Image{
		Src: solidRGBA(8, 8, color.RGBA{A: 255}),
		// A square source in a wide cell rectangle must pillarbox.
		Width: 10, Height: 2, Fit: term.ImageFitContain,
	}
	area := cellRectToPixels(image.Rect(0, 0, 10, 2), m, 0, 0)

	p, ok := resolvePlacement(img, m, 0, 0, bounds)
	require.True(t, ok)
	assert.Equal(t, p.area.Dx(), p.area.Dy(), "a square source stays square")
	assert.Equal(t, area.Dy(), p.area.Dy(), "the short axis fills the rectangle")
	assert.True(t, p.area.In(area), "the fitted rectangle stays inside the cells")
	assert.Equal(t,
		area.Min.X+area.Max.X, p.area.Min.X+p.area.Max.X,
		"the fitted rectangle is centred horizontally")
}

// TestImageLayerDeallocateReleasesTextures asserts a resize, which
// rebuilds the renderer, does not leak the retained uploads.
func TestImageLayerDeallocateReleasesTextures(t *testing.T) {
	benchdraw.BeginFrame(t)
	var l imageLayer
	require.NotNil(t, l.texture(term.Image{
		Src: solidRGBA(4, 4, color.RGBA{A: 255}),
		ID:  term.NewImageID(), Width: 2, Height: 2,
	}))
	benchdraw.EndFrame(t)

	l.deallocate()
	assert.Empty(t, l.textures)
	assert.Nil(t, l.scratch)
}

// TestDrawImageLayerPartitions asserts each layer paints only its own
// placements and reports the pixels they covered, which is what the
// renderer repaints the cell geometry over.
func TestDrawImageLayerPartitions(t *testing.T) {
	r, _, _ := newTestRenderer(t, 16, 10)
	screen := ebiten.NewImage(r.frame.Bounds().Dx(), r.frame.Bounds().Dy())
	t.Cleanup(screen.Deallocate)

	src := solidRGBA(4, 4, color.RGBA{G: 255, A: 255})
	image := func(layer term.ImageLayer, x int) term.Image {
		return term.Image{
			Src: src, ID: term.NewImageID(),
			Pos: term.Coordinates{X: x}, Width: 2, Height: 2, Layer: layer,
		}
	}
	below := image(term.ImageLayerBelowText, 0)
	above := image(term.ImageLayerAboveText, 4)
	images := []term.Image{above, below}

	benchdraw.BeginFrame(t)
	defer benchdraw.EndFrame(t)

	rects := r.drawImageLayer(screen, images, term.ImageLayerBelowText, 0, 0)
	require.Len(t, rects, 1, "only the below-text placement is painted")
	assert.Equal(t, cellRectToPixels(below.Bounds(), r.fontManager, 0, 0), rects[0])

	rects = r.drawImageLayer(screen, images, term.ImageLayerAboveText, 0, 0)
	require.Len(t, rects, 1)
	assert.Equal(t, cellRectToPixels(above.Bounds(), r.fontManager, 0, 0), rects[0])

	assert.Empty(t, r.drawImageLayer(screen, images, term.ImageLayerBelowBackground, 0, 0))
	assert.Len(t, r.images.textures, 2, "each placement uploaded its own picture")
}

// TestRowsCovering asserts the rows repainted over a placement include
// the neighbours of the rows it covers, because vertical-offset cells
// paint outside their own strip.
func TestRowsCovering(t *testing.T) {
	r, _, rows := newTestRenderer(t, 16, 10)
	m := r.fontManager

	first, last := r.rowsCovering(cellRectToPixels(image.Rect(0, 3, 2, 5), m, 0, 0), rows)
	assert.Equal(t, 2, first, "the row above can paint into the placement")
	assert.Equal(t, 5, last, "and so can the row below")

	first, last = r.rowsCovering(cellRectToPixels(image.Rect(0, 0, 2, 1), m, 0, 0), rows)
	assert.Equal(t, 0, first, "clamped to the first row")
	assert.Equal(t, 1, last)

	first, last = r.rowsCovering(cellRectToPixels(image.Rect(0, rows-1, 2, rows), m, 0, 0), rows)
	assert.Equal(t, rows-2, first)
	assert.Equal(t, rows-1, last, "clamped to the last row")
}
