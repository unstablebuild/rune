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
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/gui/font"
)

func testFontManager(t testing.TB) *font.Manager {
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
	got := cellRectToPixels(image.Rect(1, 2, 4, 5), m)
	want := image.Rect(
		int(m.PixelX(1)), int(m.PixelY(2)),
		int(m.PixelX(4)), int(m.PixelY(5)),
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
		l.drawOne(dst, img, m, 0)
	}
	l.evictUnused()
}

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
			_, ok := resolvePlacement(tt.img, m, dst.Bounds(), 0)
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

func TestResolvePlacement(t *testing.T) {
	m := testFontManager(t)
	bounds := image.Rect(0, 0, 2000, 2000)
	src := solidRGBA(8, 4, color.RGBA{A: 255})

	cellRect := func(x0, y0, x1, y1 int) image.Rectangle {
		return cellRectToPixels(image.Rect(x0, y0, x1, y1), m)
	}

	tests := []struct {
		name     string
		img      term.Image
		shift    int
		wantSrc  image.Rectangle
		wantArea image.Rectangle
		wantClip image.Rectangle
	}{
		{
			name: "fill covers the whole cell rectangle",
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 1, Y: 1},
				Width: 4, Height: 3,
			},
			wantSrc:  image.Rect(0, 0, 8, 4),
			wantArea: cellRect(1, 1, 5, 4),
			wantClip: cellRect(1, 1, 5, 4),
		},
		{
			name: "crop selects a sub-rectangle of the texture",
			img: term.Image{
				Src: src, Crop: image.Rect(2, 1, 6, 3),
				Width: 4, Height: 3,
			},
			wantSrc:  image.Rect(2, 1, 6, 3),
			wantArea: cellRect(0, 0, 4, 3),
			wantClip: cellRect(0, 0, 4, 3),
		},
		{
			name: "the pixel offset shifts the raster inside its cells",
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 1, Y: 1},
				RasterOffset: image.Pt(3, 5), Width: 4, Height: 3,
			},
			wantSrc:  image.Rect(0, 0, 8, 4),
			wantArea: cellRect(1, 1, 5, 4).Add(image.Pt(3, 5)),
			wantClip: cellRect(1, 1, 5, 4).Add(image.Pt(3, 5)).
				Intersect(cellRect(1, 1, 5, 4)),
		},
		{
			name: "the shift moves the raster along with its cells",
			img: term.Image{
				Src: src, Pos: term.Coordinates{X: 1, Y: 1},
				Width: 4, Height: 3,
			},
			shift:    7,
			wantSrc:  image.Rect(0, 0, 8, 4),
			wantArea: cellRect(1, 1, 5, 4).Add(image.Pt(0, 7)),
			wantClip: cellRect(1, 1, 5, 4).Add(image.Pt(0, 7)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := resolvePlacement(tt.img, m, bounds, tt.shift)
			require.True(t, ok)
			assert.Equal(t, tt.wantSrc, p.src, "src")
			assert.Equal(t, tt.wantArea, p.area, "area")
			assert.Equal(t, tt.wantClip, p.clip, "clip")
		})
	}
}

func TestResolvePlacementClipNarrowsPainting(t *testing.T) {
	m := testFontManager(t)
	bounds := image.Rect(0, 0, 2000, 2000)

	img, ok := term.Image{
		Src: solidRGBA(8, 8, color.RGBA{A: 255}), Width: 4, Height: 4,
	}.Clipped(image.Rect(0, 0, 4, 2))
	require.True(t, ok)

	p, ok := resolvePlacement(img, m, bounds, 0)
	require.True(t, ok)
	assert.Equal(t, cellRectToPixels(image.Rect(0, 0, 4, 4), m), p.area,
		"scaling ignores the clip")
	assert.Equal(t, cellRectToPixels(image.Rect(0, 0, 4, 2), m), p.clip,
		"painting is confined to the visible cells")
	assert.Equal(t, image.Rect(0, 0, 8, 8), p.src, "the crop is unchanged")
}

func TestResolvePlacementContainPreservesAspect(t *testing.T) {
	m := testFontManager(t)
	bounds := image.Rect(0, 0, 2000, 2000)

	img := term.Image{
		Src: solidRGBA(8, 8, color.RGBA{A: 255}),
		// A square source in a wide cell rectangle must pillarbox.
		Width: 10, Height: 2, Fit: term.ImageFitContain,
	}
	area := cellRectToPixels(image.Rect(0, 0, 10, 2), m)

	p, ok := resolvePlacement(img, m, bounds, 0)
	require.True(t, ok)
	assert.Equal(t, p.area.Dx(), p.area.Dy(), "a square source stays square")
	assert.Equal(t, area.Dy(), p.area.Dy(), "the short axis fills the rectangle")
	assert.True(t, p.area.In(area), "the fitted rectangle stays inside the cells")
	assert.Equal(t,
		area.Min.X+area.Max.X, p.area.Min.X+p.area.Max.X,
		"the fitted rectangle is centred horizontally")
}

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

	rects := r.drawImageLayer(screen, images, term.ImageLayerBelowText)
	require.Len(t, rects, 1, "only the below-text placement is painted")
	assert.Equal(t, cellRectToPixels(below.Bounds(), r.fontManager), rects[0])

	rects = r.drawImageLayer(screen, images, term.ImageLayerAboveText)
	require.Len(t, rects, 1)
	assert.Equal(t, cellRectToPixels(above.Bounds(), r.fontManager), rects[0])

	assert.Empty(t, r.drawImageLayer(screen, images, term.ImageLayerBelowBackground))
	assert.Len(t, r.images.textures, 2, "each placement uploaded its own picture")
}

func TestRowsCovering(t *testing.T) {
	r, _, rows := newTestRenderer(t, 16, 10)
	m := r.fontManager

	first, last := r.rowsCovering(cellRectToPixels(image.Rect(0, 3, 2, 5), m), rows)
	assert.Equal(t, 2, first, "the row above can paint into the placement")
	assert.Equal(t, 5, last, "and so can the row below")

	first, last = r.rowsCovering(cellRectToPixels(image.Rect(0, 0, 2, 1), m), rows)
	assert.Equal(t, 0, first, "clamped to the first row")
	assert.Equal(t, 1, last)

	first, last = r.rowsCovering(cellRectToPixels(image.Rect(0, rows-1, 2, rows), m), rows)
	assert.Equal(t, rows-2, first)
	assert.Equal(t, rows-1, last, "clamped to the last row")
}

func TestDrawImageLayerVerticalRenderOffset(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 16, 10)
	half := int(r.halfCell())
	require.Positive(t, half)
	// up is how far a cell shifted up moves, unless it is on the top row,
	// which does not move.
	up := -half - 1
	screen := ebiten.NewImage(r.frame.Bounds().Dx(), r.frame.Bounds().Dy())
	t.Cleanup(screen.Deallocate)
	src := solidRGBA(4, 4, color.RGBA{R: 255, A: 255})
	// bar is shifted down, as a status bar with a window under it is, and
	// tabs and the top row are shifted up, as a tab bar is.
	bar, tabs := rows-3, 2
	tests := []struct {
		name string
		// y and height are the rows the placement covers.
		y, height    int
		down, upward bool
		// over are the cells written over the placement.
		over []term.Coordinates
		want int
	}{
		{name: "on the bar", y: bar, height: 1, down: true, want: half},
		{name: "over the bar and the rows around it", y: bar - 1, height: 3, down: true, want: half},
		{name: "above the bar", y: bar - 3, height: 2, down: true, want: half},
		{name: "below the bar", y: bar + 1, height: 2, down: true, want: half},
		{name: "past the bottom of the frame", y: bar, height: 6, down: true, want: half},
		{
			name: "cut into pieces", y: bar - 1, height: 3, down: true,
			over: []term.Coordinates{{X: 2, Y: bar + 1}, {X: 3, Y: bar}, {X: 4, Y: bar - 1}},
			want: half,
		},
		{name: "on the bar without the offset", y: bar, height: 1},
		{name: "over the bar and the rows around it without the offset", y: bar - 1, height: 3},
		{name: "on the tabs", y: tabs, height: 1, upward: true, want: up},
		{name: "over the tabs and the rows around them", y: tabs - 1, height: 3, upward: true, want: up},
		{name: "below the tabs", y: tabs + 2, height: 2, upward: true, want: up},
		{
			name: "cut into pieces over the tabs", y: tabs - 1, height: 3, upward: true,
			over: []term.Coordinates{{X: 2, Y: tabs + 1}, {X: 3, Y: tabs}, {X: 4, Y: tabs - 1}},
			want: up,
		},
		{name: "on the top row, whose cells do not move up", y: 0, height: 1, upward: true},
		{name: "from the top row", y: 0, height: 3, upward: true},
		{name: "from above the frame", y: -1, height: 3, upward: true},
		{name: "past the bottom of the frame moved up", y: rows - 2, height: 4, upward: true, want: up},
		{name: "moved down when asked both ways, as a cell is", y: tabs, height: 1, down: true, upward: true, want: half},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := cell.NewBufferWriter(frameContext(t.Context(), r.fontManager), cols, rows)
			w.DrawImage(term.Image{
				Src: src, ID: term.NewImageID(),
				Pos: term.Coordinates{X: 2, Y: tt.y}, Width: 4, Height: tt.height,
				VerticalRenderOffset: tt.down, NegativeVerticalRenderOffset: tt.upward,
			})
			for _, pos := range tt.over {
				w.SetCell(pos, term.NewCell('x', 1, term.Attributes{}))
			}
			for x := range cols {
				w.UnionAttributes(term.Coordinates{X: x, Y: bar},
					term.Attributes{Attrs: term.AttrVerticalRenderOffset})
				for _, y := range []int{0, tabs} {
					w.UnionAttributes(term.Coordinates{X: x, Y: y},
						term.Attributes{Attrs: term.AttrNegativeVerticalRenderOffset})
				}
			}
			images := w.Images()
			if len(tt.over) > 0 {
				require.Greater(t, len(images), 1, "the cells cut the placement")
			}
			var want []image.Rectangle
			for _, piece := range images {
				want = append(want, cellRectToPixels(piece.Visible(), r.fontManager).
					Add(image.Pt(0, tt.want)).Intersect(screen.Bounds()))
			}

			benchdraw.BeginFrame(t)
			defer benchdraw.EndFrame(t)
			assert.Equal(t, want, r.drawImageLayer(screen, images, term.ImageLayerAboveText))
		})
	}
}

func TestDrawImageLayerOffset(t *testing.T) {
	r, cols, rows := newTestRenderer(t, 16, 10)
	m := r.fontManager
	// The test font's cells span whole pixels, so their edges are exact.
	cw, ch := int(m.PixelX(1)), int(m.PixelY(1))
	require.Equal(t, m.PixelX(1), float64(cw))
	require.Equal(t, m.PixelY(1), float64(ch))
	half := int(r.halfCell())
	screen := ebiten.NewImage(r.frame.Bounds().Dx(), r.frame.Bounds().Dy())
	t.Cleanup(screen.Deallocate)
	px := func(c image.Rectangle) image.Rectangle {
		return image.Rect(c.Min.X*cw, c.Min.Y*ch, c.Max.X*cw, c.Max.Y*ch)
	}
	grid := px(image.Rect(0, 0, cols, rows))
	src := solidRGBA(4, 4, color.RGBA{R: 255, A: 255})
	box := image.Rect(4, 3, 8, 5)
	clip := image.Rect(0, 0, cols, 5)
	tests := []struct {
		name string
		// cells are the cells the placement is drawn on, box when empty.
		cells    image.Rectangle
		offset   image.Point
		down, up bool
		clip     image.Rectangle
		// over are the cells written over the placement.
		over []term.Coordinates
		// want are the pixels painted, piece by piece.
		want []image.Rectangle
	}{
		{
			name:   "right by a few pixels",
			offset: image.Pt(3, 0),
			want:   []image.Rectangle{px(box).Add(image.Pt(3, 0))},
		},
		{
			name:   "left and up by a few pixels",
			offset: image.Pt(-5, -7),
			want:   []image.Rectangle{px(box).Add(image.Pt(-5, -7))},
		},
		{
			name:   "by several cells and a few pixels",
			offset: image.Pt(3*cw+2, -2*ch),
			want:   []image.Rectangle{px(box).Add(image.Pt(3*cw+2, -2*ch))},
		},
		{
			name:   "onto the screen from cells outside it",
			cells:  image.Rect(-10, 3, -6, 5),
			offset: image.Pt(14*cw+1, 0),
			want:   []image.Rectangle{px(image.Rect(-10, 3, -6, 5)).Add(image.Pt(14*cw+1, 0))},
		},
		{
			name:   "off the screen",
			offset: image.Pt((cols-4)*cw, 0),
		},
		{
			name:   "past the bottom of the screen",
			offset: image.Pt(0, (rows-4)*ch+5),
			want:   []image.Rectangle{px(box).Add(image.Pt(0, (rows-4)*ch+5)).Intersect(grid)},
		},
		{
			name:   "confined to its clip on the cells it lands on",
			offset: image.Pt(0, ch+ch/2),
			clip:   clip,
			want:   []image.Rectangle{px(box).Add(image.Pt(0, ch+ch/2)).Intersect(px(clip))},
		},
		{
			name:   "dropped when it lands outside its clip",
			offset: image.Pt(0, 3*ch),
			clip:   clip,
		},
		{
			name:   "kept when its cells are outside its clip but it lands inside",
			cells:  image.Rect(4, 7, 8, 9),
			offset: image.Pt(0, -4*ch),
			clip:   clip,
			want:   []image.Rectangle{px(box)},
		},
		{
			name:   "cut by a cell written over a cell it lands on",
			offset: image.Pt(2*cw, 0),
			over:   []term.Coordinates{{X: 9, Y: 3}},
			want:   []image.Rectangle{px(image.Rect(6, 3, 9, 4)), px(image.Rect(6, 4, 10, 5))},
		},
		{
			name:   "cut by a written cell it reaches a pixel into",
			offset: image.Pt(2*cw+1, 0),
			over:   []term.Coordinates{{X: 10, Y: 4}},
			want: []image.Rectangle{
				image.Rect(6*cw+1, 3*ch, 10*cw+1, 4*ch),
				image.Rect(6*cw+1, 4*ch, 10*cw, 5*ch),
			},
		},
		{
			name:   "not cut by a cell written over a cell it moved off",
			offset: image.Pt(2*cw, 0),
			over:   []term.Coordinates{{X: 4, Y: 3}},
			want:   []image.Rectangle{px(box).Add(image.Pt(2*cw, 0))},
		},
		{
			name:   "and moved down with the shifted cells",
			offset: image.Pt(3, -ch),
			down:   true,
			want:   []image.Rectangle{px(box).Add(image.Pt(3, -ch+half))},
		},
		{
			name:   "and moved up with the shifted cells as far as the top of the screen",
			cells:  image.Rect(4, 1, 8, 3),
			offset: image.Pt(0, 3-ch),
			up:     true,
			want:   []image.Rectangle{px(image.Rect(4, 0, 8, 2))},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cells := tt.cells
			if cells.Empty() {
				cells = box
			}
			w := cell.NewBufferWriter(frameContext(t.Context(), m), cols, rows)
			require.True(t, w.DrawImage(term.Image{
				Src: src, ID: term.NewImageID(),
				Pos:   term.Coordinates{X: cells.Min.X, Y: cells.Min.Y},
				Width: cells.Dx(), Height: cells.Dy(), Fit: term.ImageFitFill,
				Clip: tt.clip, Offset: tt.offset,
				VerticalRenderOffset: tt.down, NegativeVerticalRenderOffset: tt.up,
			}))
			for _, pos := range tt.over {
				w.SetCell(pos, term.NewCell('x', 1, term.Attributes{}))
			}

			benchdraw.BeginFrame(t)
			defer benchdraw.EndFrame(t)
			assert.Equal(t, tt.want, r.drawImageLayer(screen, w.Images(), term.ImageLayerAboveText))
		})
	}
}
