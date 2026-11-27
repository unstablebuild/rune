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
	"slices"
	"strconv"
	"testing"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/benchdraw"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/term/gui/drawrect"
	"unstable.build/rune/internal/term/gui/drawtext"
	"unstable.build/rune/internal/term/gui/font"
)

func TestRendererDeallocateReleasesGraphicsStorage(t *testing.T) {
	r := &renderer{
		frame:  ebiten.NewImage(32, 32),
		drawer: drawtext.New(),
	}

	r.deallocate()

	assert.Nil(t, r.frame)
	assert.Nil(t, r.drawer)
	assert.NotPanics(t, r.deallocate)
}

func TestRendererCursorColorsOnMatchingBackground(t *testing.T) {
	red := color.RGBA{R: 202, G: 124, B: 94, A: 255}
	black := color.RGBA{A: 255}
	redCell := term.NewColor(202, 124, 94)
	creamCell := term.NewColor(255, 255, 234)
	cursorAttr := term.Attributes{Bg: redCell}
	defaultAttr := term.Attributes{Fg: term.NewColor(0, 0, 0), Bg: creamCell}
	manager, err := font.NewManager(1, 1)
	require.NoError(t, err)

	for _, opacity := range []float64{1, 0.9, 0} {
		t.Run(strconv.FormatFloat(opacity, 'f', 1, 64), func(t *testing.T) {
			textBlack := color.RGBA{A: uint8(255 * opacity)}
			for _, tt := range []struct {
				name           string
				cell           term.Cell
				defaults       term.Attributes
				wantFG, wantBG color.RGBA
			}{
				{"Pike error background", term.Cell{Bg: redCell}, defaultAttr, red, black},
				{"ordinary background", term.Cell{}, defaultAttr, textBlack, red},
				{"reversed matching background",
					term.Cell{Fg: redCell, Attrs: term.AttrReverse}, defaultAttr, red, black},
				{"reversed different background",
					term.Cell{Fg: creamCell, Bg: redCell, Attrs: term.AttrReverse},
					defaultAttr, textBlack, red},
				{"matching default background", term.Cell{}, term.Attributes{Bg: redCell}, red, black},
				{"reversed default foreground", term.Cell{Attrs: term.AttrReverse},
					term.Attributes{Fg: redCell}, red, black},
			} {
				t.Run(tt.name, func(t *testing.T) {
					r := newRenderer(20, 40, 1, manager, opacity, opacity, false,
						cursorAttr, tt.defaults)
					t.Cleanup(r.deallocate)
					fg, bg := r.cursorColors(tt.cell)
					assert.Equal(t, tt.wantFG, fg, "text inside the block cursor")
					assert.Equal(t, tt.wantBG, bg, "cursor fill")
				})
			}
		})
	}
}

// cellSpy records the cells whose glyphs a renderer redraws, by giving
// every cell its own rune and observing the runes the renderer asks the
// colour emoji face about.
type cellSpy struct {
	cols  int
	drawn []term.Coordinates
}

const cellSpyBase = 0x100

func (s *cellSpy) Has(cluster []rune) bool {
	i := int(cluster[0]) - cellSpyBase
	s.drawn = append(s.drawn, term.Coordinates{X: i % s.cols, Y: i / s.cols})
	return false
}

func (s *cellSpy) Glyph([]rune, int, int) (*image.RGBA, bool) {
	return nil, false
}

func (s *cellSpy) grid(rows int) [][]term.Cell {
	cells := make([][]term.Cell, rows)
	for y := range cells {
		cells[y] = make([]term.Cell, s.cols)
		for x := range cells[y] {
			cells[y][x] = term.Cell{Ch: rune(cellSpyBase + y*s.cols + x), Width: 1}
		}
	}
	return cells
}

func TestRepaintOverRedrawsTheCellsThatReachTheRects(t *testing.T) {
	const cols, rows = 12, 8
	block := func(x0, y0, x1, y1 int) []term.Coordinates {
		var cells []term.Coordinates
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				cells = append(cells, term.Coordinates{X: x, Y: y})
			}
		}
		return cells
	}
	without := func(cells []term.Coordinates, drop ...term.Coordinates) []term.Coordinates {
		var kept []term.Coordinates
		for _, c := range cells {
			if !slices.Contains(drop, c) {
				kept = append(kept, c)
			}
		}
		return kept
	}
	tests := []struct {
		name      string
		ligatures bool
		edit      func(cells [][]term.Cell)
		// rects are the cells the placements cover.
		rects []image.Rectangle
		want  []term.Coordinates
	}{
		{
			name:  "the cells of a one-cell placement and its neighbours",
			rects: []image.Rectangle{image.Rect(5, 4, 6, 5)},
			want:  block(4, 3, 6, 5),
		},
		{
			name:  "clamped at the corner of the grid",
			rects: []image.Rectangle{image.Rect(0, 0, 1, 1)},
			want:  block(0, 0, 1, 1),
		},
		{
			name: "a wide cell reaching the neighbours, but not its second column",
			edit: func(cells [][]term.Cell) {
				cells[4][3].Width = 2
			},
			rects: []image.Rectangle{image.Rect(5, 4, 6, 5)},
			want:  append(without(block(4, 3, 6, 5), term.Coordinates{X: 4, Y: 4}), term.Coordinates{X: 3, Y: 4}),
		},
		{
			name:      "a ligature reaching the neighbours, but not its second column",
			ligatures: true,
			edit: func(cells [][]term.Cell) {
				cells[4][3].Ch, cells[4][4].Ch = '<', '-'
			},
			rects: []image.Rectangle{image.Rect(5, 4, 6, 5)},
			want:  without(block(4, 3, 6, 5), term.Coordinates{X: 4, Y: 4}),
		},
		{
			name: "once for placements side by side",
			rects: []image.Rectangle{
				image.Rect(5, 4, 6, 5), image.Rect(6, 4, 7, 5),
			},
			want: block(4, 3, 7, 5),
		},
		{
			name: "once for placements stacked",
			rects: []image.Rectangle{
				image.Rect(5, 2, 6, 3), image.Rect(5, 3, 6, 4), image.Rect(5, 4, 6, 5),
			},
			want: block(4, 1, 6, 5),
		},
		{
			name: "once for placements that overlap",
			rects: []image.Rectangle{
				image.Rect(2, 4, 6, 5), image.Rect(4, 4, 8, 5), image.Rect(2, 4, 6, 5),
			},
			want: block(1, 3, 8, 5),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _ := newTestRenderer(t, cols, rows)
			t.Cleanup(r.deallocate)
			r.enableLigatures = tt.ligatures
			spy := &cellSpy{cols: cols}
			r.emojiFace = spy
			cells := spy.grid(rows)
			if tt.edit != nil {
				tt.edit(cells)
			}
			screen := ebiten.NewImage(r.frame.Bounds().Dx(), r.frame.Bounds().Dy())
			t.Cleanup(screen.Deallocate)
			var rects []image.Rectangle
			for _, rect := range tt.rects {
				rects = append(rects, cellRectToPixels(rect, r.fontManager))
			}

			benchdraw.BeginFrame(t)
			defer benchdraw.EndFrame(t)
			r.repaintOver(screen, &r.clips, cells, rects, passGlyphs)
			assert.ElementsMatch(t, tt.want, spy.drawn)
		})
	}
}

func TestRectUnion(t *testing.T) {
	bounds := image.Rect(0, 0, 20, 20)
	tests := []struct {
		name  string
		rects []image.Rectangle
		want  []image.Rectangle
	}{
		{name: "nothing"},
		{
			name:  "one rectangle",
			rects: []image.Rectangle{image.Rect(1, 2, 3, 4)},
			want:  []image.Rectangle{image.Rect(1, 2, 3, 4)},
		},
		{
			name:  "cut to the bounds",
			rects: []image.Rectangle{image.Rect(-5, 18, 3, 25), image.Rect(30, 0, 40, 5)},
			want:  []image.Rectangle{image.Rect(0, 18, 3, 20)},
		},
		{
			name:  "side by side",
			rects: []image.Rectangle{image.Rect(3, 0, 5, 2), image.Rect(0, 0, 3, 2)},
			want:  []image.Rectangle{image.Rect(0, 0, 5, 2)},
		},
		{
			name:  "stacked",
			rects: []image.Rectangle{image.Rect(0, 2, 3, 4), image.Rect(0, 0, 3, 2), image.Rect(0, 4, 3, 5)},
			want:  []image.Rectangle{image.Rect(0, 0, 3, 5)},
		},
		{
			name:  "the same rectangle twice",
			rects: []image.Rectangle{image.Rect(1, 1, 4, 4), image.Rect(1, 1, 4, 4)},
			want:  []image.Rectangle{image.Rect(1, 1, 4, 4)},
		},
		{
			name:  "apart",
			rects: []image.Rectangle{image.Rect(0, 0, 2, 2), image.Rect(3, 0, 5, 2)},
			want:  []image.Rectangle{image.Rect(0, 0, 2, 2), image.Rect(3, 0, 5, 2)},
		},
		{
			name:  "overlapping",
			rects: []image.Rectangle{image.Rect(0, 0, 4, 4), image.Rect(2, 2, 6, 6)},
			want: []image.Rectangle{
				image.Rect(0, 0, 4, 2), image.Rect(0, 2, 6, 4), image.Rect(2, 4, 6, 6),
			},
		},
		{
			name: "a frame's borders",
			rects: []image.Rectangle{
				image.Rect(0, 0, 1, 1), image.Rect(1, 0, 2, 1), image.Rect(2, 0, 3, 1),
				image.Rect(0, 1, 1, 2), image.Rect(2, 1, 3, 2),
				image.Rect(0, 2, 1, 3), image.Rect(2, 2, 3, 3),
				image.Rect(0, 3, 1, 4), image.Rect(1, 3, 2, 4), image.Rect(2, 3, 3, 4),
			},
			want: []image.Rectangle{
				image.Rect(0, 0, 3, 1),
				image.Rect(0, 1, 1, 3), image.Rect(2, 1, 3, 3),
				image.Rect(0, 3, 3, 4),
			},
		},
	}
	var u rectUnion
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := u.of(tt.rects, bounds)
			assert.ElementsMatch(t, tt.want, got)
		})
	}
}

func TestRendererRenderCellsKeepsItsPicturesForDraw(t *testing.T) {
	manager, err := font.NewManager(1, 1)
	require.NoError(t, err)
	r := newRenderer(200, 100, 1, manager, 1, 1, false, term.Attributes{}, term.Attributes{})
	t.Cleanup(r.deallocate)
	cells := [][]term.Cell{{{Ch: 'a', Bg: term.ColorRed}, {Ch: 'b'}}}
	pic := image.NewRGBA(image.Rect(0, 0, 2, 2))
	images := []term.Image{{Src: pic, ID: 9, Version: 1, Width: 1, Height: 1}}
	dst := ebiten.NewImage(manager.ImageWidth(2), manager.ImageHeight(1))
	t.Cleanup(dst.Deallocate)
	screen := ebiten.NewImage(200, 100)
	t.Cleanup(screen.Deallocate)
	draw := func(source bool) {
		benchdraw.BeginFrame(t)
		defer benchdraw.EndFrame(t)
		if source {
			r.renderCells(dst, cells, images)
		}
		r.Draw(screen, cells, nil, false, term.Coordinates{}, term.CursorStyleDefault)
	}

	draw(true)
	assert.Contains(t, r.images.textures, textureKey{id: 9}, "a draw that rendered it keeps its picture")
	draw(false)
	assert.NotContains(t, r.images.textures, textureKey{id: 9}, "and the first draw that did not frees it")
}

func BenchmarkRenderLigaturesHD(b *testing.B) {
	benchmarkRenderLigatures(b, 1920, 1080)
}

func BenchmarkRenderLigaturesHD2(b *testing.B) {
	benchmarkRenderLigatures(b, 2560, 1440)
}

func BenchmarkRenderLigatures4k(b *testing.B) {
	benchmarkRenderLigatures(b, 3840, 2160)
}

func BenchmarkRendererDrawOpaqueHD(b *testing.B) {
	benchmarkRendererContent(b, 1920, 1080, 1)
}

func BenchmarkRendererDrawOpaqueHD2(b *testing.B) {
	benchmarkRendererContent(b, 2560, 1440, 1)
}

func BenchmarkRendererDrawOpaque4k(b *testing.B) {
	benchmarkRendererContent(b, 3840, 2160, 1)
}

func BenchmarkRendererDrawTransparentHD(b *testing.B) {
	benchmarkRendererContent(b, 1920, 1080, 0.5)
}

func BenchmarkRendererDrawTransparentHD2(b *testing.B) {
	benchmarkRendererContent(b, 2560, 1440, 0.5)
}

func BenchmarkRendererDrawTransparent4k(b *testing.B) {
	benchmarkRendererContent(b, 3840, 2160, 0.5)
}

// BenchmarkRendererDrawThemedFrame draws the placements of a theme with
// image frames around three tiles, two side by side with the right one
// split, while the cursor moves a row per frame. The mix is what such a
// theme placed in one frame: a picture above the text over each cell of
// each border, and below the text, a fill through a mask inside each of
// them, solid one-cell fills and a few pictures.
func BenchmarkRendererDrawThemedFrame(b *testing.B) {
	const cols, rows = 142, 35
	drawrect.Init()
	m := testFontManager(b)
	px := int(m.CharSize().X*cols) + 1
	py := int(m.CharSize().Y*rows) + 1
	r := newRenderer(px, py, 1, m, 1, 1, false, term.Attributes{}, term.Attributes{})
	b.Cleanup(r.deallocate)
	screen := ebiten.NewImage(px, py)
	b.Cleanup(screen.Deallocate)

	const text = "func (r *renderer) Draw(screen *ebiten.Image, cells [][]term.Cell) { "
	cells := make([][]term.Cell, rows)
	for y := range cells {
		cells[y] = make([]term.Cell, cols)
		for x := range cells[y] {
			cells[y][x] = term.Cell{Ch: rune(text[(x+y)%len(text)]), Width: 1}
		}
	}
	for x := range cells[rows-1] {
		cells[rows-1][x].Bg = term.NewColor(28, 28, 31)
	}

	// A sprite draws a line through its cell, transparent either side.
	sprite := image.NewRGBA(image.Rect(0, 0, 56, 136))
	for y := range 136 {
		for x := 24; x < 32; x++ {
			sprite.SetRGBA(x, y, color.RGBA{R: 90, G: 100, B: 120, A: 255})
		}
	}
	frameColor := term.NewColor(20, 22, 28)
	// The focused tile's border has pictures of its own.
	type border struct{ sprites, masks [8]term.ImageID }
	newBorder := func() (s border) {
		for i := range s.sprites {
			s.sprites[i], s.masks[i] = term.NewImageID(), term.NewImageID()
		}
		return s
	}
	focused, other := newBorder(), newBorder()
	tiles := []struct {
		area   image.Rectangle
		border border
	}{
		{image.Rect(0, 0, cols/2, rows), focused},
		{image.Rect(cols/2, 0, cols, rows/2), other},
		{image.Rect(cols/2, rows/2, cols, rows), other},
	}
	var images []term.Image
	for _, tile := range tiles {
		a := tile.area
		var ring []term.Image
		add := func(kind, x, y int) {
			ring = append(ring, term.Image{
				Src: sprite, ID: tile.border.sprites[kind], Pos: term.Coordinates{X: x, Y: y},
				Width: 1, Height: 1,
			})
		}
		// The pictures are, in the order the window manager draws them,
		// the top, the bottom, the left, the right and the four corners.
		for _, y := range []int{a.Min.Y, a.Max.Y - 1} {
			for x := a.Min.X + 1; x < a.Max.X-1; x++ {
				add(min(y-a.Min.Y, 1), x, y)
			}
		}
		for i, x := range []int{a.Min.X, a.Max.X - 1} {
			for y := a.Min.Y + 1; y < a.Max.Y-1; y++ {
				add(2+i, x, y)
			}
		}
		last := a.Max.Sub(image.Pt(1, 1))
		for i, corner := range []image.Point{a.Min, {X: last.X, Y: a.Min.Y}, {X: a.Min.X, Y: last.Y}, last} {
			add(4+i, corner.X, corner.Y)
		}
		images = append(images, ring...)
		for _, s := range ring {
			mask := s
			mask.ID = tile.border.masks[slices.Index(tile.border.sprites[:], s.ID)]
			mask.Fill, mask.FillColor, mask.Layer = true, frameColor, term.ImageLayerBelowText
			images = append(images, mask)
		}
		for x := a.Min.X + 1; x < a.Min.X+23; x++ {
			images = append(images, term.Image{
				Fill: true, FillColor: frameColor, Pos: term.Coordinates{X: x, Y: a.Max.Y - 2},
				Width: 1, Height: 1, Layer: term.ImageLayerBelowText,
			})
		}
	}
	tab := solidRGBA(56, 272, color.RGBA{R: 35, G: 37, B: 42, A: 255})
	tabID := term.NewImageID()
	for x := 1; x < 12; x++ {
		images = append(images, term.Image{
			Src: tab, ID: tabID, Pos: term.Coordinates{X: x, Y: 1},
			Width: 1, Height: 2, Layer: term.ImageLayerBelowText,
		})
	}
	images = append(images,
		term.Image{
			Src: solidRGBA(322, 510, color.RGBA{R: 60, G: 30, B: 90, A: 128}), ID: term.NewImageID(),
			Pos: term.Coordinates{X: cols - 30, Y: rows/2 + 2}, Width: 23, Height: 15,
		},
		term.Image{
			Fill: true, FillColor: frameColor, Pos: term.Coordinates{X: 1, Y: rows - 1},
			Width: 66, Height: 1, Layer: term.ImageLayerBelowBackground,
		},
	)
	b.ReportMetric(float64(len(images)), "placements")

	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		benchdraw.BeginFrame(b)
		r.Draw(screen, cells, images, true, term.Coordinates{X: 4, Y: 2 + i%(rows-4)},
			term.CursorStyleDefault)
		benchdraw.EndFrame(b)
	}
}

func benchmarkRendererContent(
	b *testing.B, pixelsWidth, pixelsHeight int,
	opacity float64,
) {
	drawrect.Init()
	manager, err := font.NewManager(1, 1)
	if err != nil {
		b.Logf("new manager: %v", err)
		b.FailNow()
	}
	width := manager.CellsWidth(pixelsWidth)
	height := manager.CellsHeight(pixelsHeight)

	cells := make([][]term.Cell, height)
	for i := 0; i < height; i++ {
		cells[i] = make([]term.Cell, width)
		for j := 0; j < width; j++ {
			cells[i][j].Ch = []rune(strconv.Itoa(i))[0]
			cells[i][j].Fg = term.NewColor(255, 0, 255)
			cells[i][j].Bg = term.NewColor(0, 0, 255)
		}
	}
	var (
		doLigatures = false
		defAttr     = term.Attributes{}
		deviceScale = 1.0
	)
	image := ebiten.NewImage(pixelsWidth, pixelsHeight)
	r := newRenderer(pixelsWidth, pixelsHeight, deviceScale,
		manager, opacity, opacity, doLigatures, defAttr, defAttr)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchdraw.BeginFrame(b)
		r.Draw(image, cells, nil, true, term.Coordinates{X: 1, Y: 5},
			term.CursorStyleDefault)
		benchdraw.EndFrame(b)
	}
}

func benchmarkRenderLigatures(b *testing.B, pixelsWidth, pixelsHeight int) {
	manager, err := font.NewManager(1, 1)
	if err != nil {
		b.Logf("new manager: %v", err)
		b.FailNow()
	}
	width := manager.CellsWidth(pixelsWidth)
	height := manager.CellsHeight(pixelsHeight)

	cells := make([][]term.Cell, height)
	for i := 0; i < height; i++ {
		cells[i] = make([]term.Cell, width)
		for j := 0; j < width; j++ {
			if j%2 == 0 {
				cells[i][j].Ch = '='
			} else {
				cells[i][j].Ch = '>'
			}
		}
	}
	image := ebiten.NewImage(pixelsWidth, pixelsHeight)
	font := newFontFace(manager)
	colorBlack := color.RGBA{A: 255}
	drawer := drawtext.New()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if i%2 == 0 {
			handleLigatures(drawer, cells, 4, 10, font.Regular, colorBlack, font, image)
		} else {
			handleLigatures(drawer, cells, 5, 10, font.Regular, colorBlack, font, image)
		}
	}
}
