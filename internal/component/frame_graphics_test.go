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
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/imageuri/imageuritest"
)

func TestFrameGraphicsDraw(t *testing.T) {
	pics := newFramePictures(t)
	everySlot := component.FrameCharSetGraphics{
		TopLeft: "L", TopRight: "L", BottomLeft: "L", BottomRight: "L",
		HorizontalTop: "R", HorizontalBottom: "R",
		VerticalLeft: "R", VerticalRight: "R",
	}
	overlay := everySlot
	overlay.Overlay = true
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}

	tests := []struct {
		name          string
		graphics      component.FrameCharSetGraphics
		width, height int
		scrollable    bool
		noImages      bool
		notLoaded     bool
		expected      string
		// underneath, when set, is what the screen shows under the images.
		underneath string
	}{
		{
			name:  "no graphics draws the runes",
			width: 6, height: 4,
			expected: `
┌────┐
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:     "every slot is drawn with its picture",
			graphics: everySlot,
			width:    6, height: 4,
			expected: `
LRRRRL
RxxxxR
RxxxxR
LRRRRL`,
		},
		{
			name: "only the corners leave the edges to their runes",
			graphics: component.FrameCharSetGraphics{
				TopLeft: "L", TopRight: "R", BottomLeft: "R", BottomRight: "L",
			},
			width: 6, height: 4,
			expected: `
L────R
│xxxx│
│xxxx│
R────L`,
		},
		{
			name:     "a writer that cannot draw images gets the runes",
			graphics: everySlot,
			width:    6, height: 4,
			noImages: true,
			expected: `
┌────┐
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:     "images that are not loaded yet leave the runes",
			graphics: everySlot,
			width:    6, height: 4,
			notLoaded: true,
			expected: `
┌────┐
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name: "images that fail to load leave the runes",
			graphics: component.FrameCharSetGraphics{
				TopLeft: "missing", HorizontalTop: "invalid", TopRight: "L",
			},
			width: 6, height: 4,
			expected: `
┌────L
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:     "the scroll bar keeps its rune",
			graphics: everySlot,
			width:    6, height: 6,
			scrollable: true,
			expected: `
LRRRRL
Rxxxx┃
Rxxxx┃
RxxxxR
RxxxxR
LRRRRL`,
		},
		{
			name:     "a frame too small for a border draws nothing",
			graphics: everySlot,
			width:    2, height: 2,
			expected: `
xx
xx`,
		},
		{
			name:     "overlay draws the pictures over the runes",
			graphics: overlay,
			width:    6, height: 4,
			expected: `
LRRRRL
RxxxxR
RxxxxR
LRRRRL`,
			underneath: `
┌────┐
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:     "overlay to a writer that cannot draw images gets the runes",
			graphics: overlay,
			width:    6, height: 4,
			noImages: true,
			expected: `
┌────┐
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:     "overlay alone draws the runes",
			graphics: component.FrameCharSetGraphics{Overlay: true},
			width:    6, height: 4,
			expected: `
┌────┐
│xxxx│
│xxxx│
└────┘`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var content tui.Component = &component.TestComponent{Ch: 'x'}
			if tt.scrollable {
				content = scrollingContent{TestComponent: &component.TestComponent{Ch: 'x'}}
			}
			f := component.NewFrame(content)
			f.Attributes = attr
			f.ScrollBarCharSet = component.ScrollBarCharSet{Middle: '┃'}
			f.Graphics = pics.resolve(tt.graphics)
			f.Resize(tt.width, tt.height)

			irq := make(interruptCounter, 16)
			g := NewFrameGraphics(storagestub.NewInMemoryService(), irq)
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			loads := pics.loads(tt.graphics)
			if tt.width < 3 || tt.height < 3 {
				loads = 0
			}
			if !tt.notLoaded {
				g.Draw(term.NewStringWriter(tt.width, tt.height), f, tt.width, tt.height)
				irq.wait(t, loads)
			}

			s := newImageScreen(tt.width, tt.height)
			s.noImages = tt.noImages
			f.Draw(s)
			g.Draw(s, f, tt.width, tt.height)
			assert.Equal(t, tt.expected, s.render(t, pics, attr, tt.graphics.Overlay))
			if tt.underneath != "" {
				assert.Equal(t, tt.underneath, "\n"+s.String())
			}

			if tt.notLoaded {
				irq.wait(t, loads)
			}
		})
	}
}

func TestFrameGraphicsDrawCornerParts(t *testing.T) {
	const corner, edge = "file:///corner.png", "file:///edge.png"
	every := component.FrameCharSetGraphics{
		TopLeft: corner, TopRight: corner, BottomLeft: corner, BottomRight: corner,
		HorizontalTop: edge, HorizontalBottom: edge, VerticalLeft: edge, VerticalRight: edge,
		CornerWidth: 2, CornerHeight: 2,
	}
	noTopLeft := every
	noTopLeft.TopLeft = ""
	oneCell := every
	oneCell.CornerWidth, oneCell.CornerHeight = 0, 0
	wide := every
	wide.CornerWidth, wide.CornerHeight = 3, 1
	tests := []struct {
		name          string
		graphics      component.FrameCharSetGraphics
		width, height int
		// want shows the part of the corner picture each border cell
		// shows, as its column and row, -- for the edge picture, ## for a
		// whole corner picture and xx for no image.
		want string
	}{
		{
			name: "each corner shows its parts over the edge cells next to it", graphics: every,
			width: 6, height: 5,
			want: `
00 10 -- -- 00 10
01 .. .. .. .. 11
-- .. .. .. .. --
00 .. .. .. .. 10
01 11 -- -- 01 11`,
		},
		{
			name: "the nearer corner draws a cell, the top or left one halfway", graphics: every,
			width: 3, height: 3,
			want: `
00 10 10
01 .. 11
01 11 11`,
		},
		{
			name: "a corner without an image leaves its cells to the edges", graphics: noTopLeft,
			width: 6, height: 5,
			want: `
xx -- -- -- 00 10
-- .. .. .. .. 11
-- .. .. .. .. --
00 .. .. .. .. 10
01 11 -- -- 01 11`,
		},
		{
			name: "a corner of one cell shows its whole picture", graphics: oneCell,
			width: 4, height: 3,
			want: `
## -- -- ##
-- .. .. --
## -- -- ##`,
		},
		{
			name: "a corner as wide as half the frame", graphics: wide,
			width: 6, height: 3,
			want: `
00 10 20 00 10 20
-- .. .. .. .. --
00 10 20 00 10 20`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Each part of the corner picture is 2 by 1 pixels.
			cw, ch := max(1, tt.graphics.CornerWidth), max(1, tt.graphics.CornerHeight)
			g := NewFrameGraphics(storagestub.NewInMemoryService(), term.NopInterrupter())
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			g.Reload(map[string]image.Image{
				corner: image.NewRGBA(image.Rect(0, 0, 2*cw, ch)),
				edge:   image.NewRGBA(image.Rect(0, 0, 1, 1)),
			})
			f := component.NewFrame(&component.TestComponent{Ch: 'x'})
			f.Graphics = tt.graphics
			f.Resize(tt.width, tt.height)
			s := newImageScreen(tt.width, tt.height)
			f.Draw(s)
			g.Draw(s, f, tt.width, tt.height)

			area := image.Rect(0, 0, tt.width, tt.height)
			grid := make([][]string, tt.height)
			for y := range grid {
				grid[y] = make([]string, tt.width)
				for x := range grid[y] {
					grid[y][x] = ".."
					if RingSides(image.Pt(x, y), area) != 0 {
						grid[y][x] = "xx"
					}
				}
			}
			for _, img := range s.images {
				require.Equal(t, image.Pt(1, 1), image.Pt(img.Width, img.Height), "the image at %v", img.Pos)
				part := "--"
				switch {
				case img.Src.Bounds().Dx() == 1:
				case img.Crop.Empty():
					part = "##"
				default:
					assert.Equal(t, image.Pt(2, 1), img.Crop.Size(), "the part at %v", img.Pos)
					part = fmt.Sprintf("%d%d", img.Crop.Min.X/2, img.Crop.Min.Y)
				}
				grid[img.Pos.Y][img.Pos.X] = part
			}
			var b strings.Builder
			for _, row := range grid {
				b.WriteByte('\n')
				b.WriteString(strings.Join(row, " "))
			}
			assert.Equal(t, tt.want, b.String())
		})
	}
}

func TestFrameGraphicsDrawSeparator(t *testing.T) {
	pics := newFramePictures(t)
	const width, height, y = 7, 5, 2
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	full := SeparatorGraphics{Left: "L", Horizontal: "R", Right: "L"}
	overlay := full
	overlay.Overlay = true
	sides := component.FrameCharSetGraphics{VerticalLeft: "L", VerticalRight: "L"}
	overlaySides := sides
	overlaySides.Overlay = true
	for _, tt := range []struct {
		name       string
		graphics   SeparatorGraphics
		frame      component.FrameCharSetGraphics
		noImages   bool
		row, under string
	}{
		{name: "characters only", row: "├l───r┤", under: "├l───r┤"},
		{name: "junctions and tiled bar", graphics: full, row: "LRRRRRL", under: "       "},
		{name: "overlay keeps separator glyphs", graphics: overlay, row: "LRRRRRL", under: "├l───r┤"},
		{name: "terminal fallback", graphics: full, frame: sides, noImages: true, row: "├l───r┤", under: "├l───r┤"},
		{name: "overlay terminal fallback", graphics: overlay, frame: sides, noImages: true, row: "├l───r┤", under: "├l───r┤"},
		{name: "bar without side images", graphics: SeparatorGraphics{Horizontal: "R"}, row: "├RRRRR┤", under: "├     ┤"},
		{name: "missing junctions preserve rails", graphics: SeparatorGraphics{Horizontal: "R"}, frame: sides, row: "LRRRRRL", under: "       "},
		{name: "no separator images preserves rails", frame: sides, row: "Ll───rL", under: " l───r "},
		{name: "failed junctions preserve rails", graphics: SeparatorGraphics{Left: "missing", Horizontal: "R", Right: "invalid"}, frame: sides, row: "LRRRRRL", under: "       "},
		{name: "failed images keep glyphs", graphics: SeparatorGraphics{Left: "missing", Horizontal: "invalid", Right: "missing"}, row: "├l───r┤", under: "├l───r┤"},
		{name: "frame overlay is independent", graphics: SeparatorGraphics{Horizontal: "R"}, frame: overlaySides, row: "LRRRRRL", under: "├     ┤"},
		{name: "separator overlay is independent", graphics: SeparatorGraphics{Horizontal: "R", Overlay: true}, frame: sides, row: "LRRRRRL", under: " l───r "},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := component.NewFrame(&component.TestComponent{Ch: 'x'})
			f.Attributes = attr
			f.Graphics = pics.resolve(tt.frame)
			f.Resize(width, height)
			cs := SeparatorCharSet{Left: '├', HorizontalLeft: 'l', HorizontalRight: 'r', Right: '┤', Graphics: tt.graphics}
			for _, uri := range []*string{&cs.Graphics.Left, &cs.Graphics.Horizontal, &cs.Graphics.Right} {
				*uri = pics.uris[*uri]
			}
			irq := make(interruptCounter, 16)
			g := NewFrameGraphics(storagestub.NewInMemoryService(), irq)
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			uris := map[string]bool{}
			for _, uri := range []string{cs.Graphics.Left, cs.Graphics.Horizontal, cs.Graphics.Right, f.Graphics.VerticalLeft, f.Graphics.VerticalRight} {
				if !uris[uri] {
					if img := g.Image(uri); img != nil {
						img.Resize(1, 1)
						img.Draw(term.NewStringWriter(1, 1))
						irq.wait(t, 1)
					}
				}
				uris[uri] = true
			}
			s := newImageScreen(width, height)
			s.noImages = tt.noImages
			f.Draw(s)
			for x, ch := range []rune("├l───r┤") {
				s.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell(ch, 1, attr))
			}
			g.DrawSeparator(s, f, cs, width, height, y)
			assert.Equal(t, tt.row, strings.Split(s.render(t, pics, attr, tt.graphics.Overlay || tt.frame.Overlay), "\n")[y+1])
			assert.Equal(t, tt.under, strings.Split(s.String(), "\n")[y])
		})
	}
}

func TestFrameGraphicsSeparatorOutsets(t *testing.T) {
	pics := newFramePictures(t)
	for _, tt := range []struct {
		name          string
		width, height int
		y             int
		outset        SeparatorOutset
		want          []image.Rectangle
	}{
		{name: "ornaments cover the bar ends", width: 7, height: 5, y: 2, outset: SeparatorOutset{Top: 1, Bottom: 1, Inward: 1}, want: []image.Rectangle{image.Rect(0, 1, 2, 4), image.Rect(5, 1, 7, 4)}},
		{name: "oversized ornaments are clipped", width: 7, height: 5, y: 1, outset: SeparatorOutset{Top: 3, Bottom: 6, Inward: 8}, want: []image.Rectangle{image.Rect(0, 0, 7, 5), image.Rect(0, 0, 7, 5)}},
		{name: "narrow frame", width: 3, height: 3, y: 1, outset: SeparatorOutset{Inward: 1}, want: []image.Rectangle{image.Rect(0, 1, 2, 2), image.Rect(1, 1, 3, 2)}},
		{name: "negative outsets are zero", width: 5, height: 3, y: 1, outset: SeparatorOutset{Top: -1, Bottom: -1, Inward: -1}, want: []image.Rectangle{image.Rect(0, 1, 1, 2), image.Rect(4, 1, 5, 2)}},
		{name: "too narrow", width: 2, height: 3, y: 1},
		{name: "too short", width: 3, height: 2, y: 1},
		{name: "top border is not a separator", width: 5, height: 3, y: 0},
		{name: "bottom border is not a separator", width: 5, height: 3, y: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := component.NewFrame(&component.TestComponent{Ch: 'x'})
			f.Resize(tt.width, tt.height)
			cs := SeparatorCharSet{Graphics: SeparatorGraphics{Left: pics.uris["L"], Horizontal: pics.uris["R"], Right: pics.uris["L"], Outset: tt.outset}}
			irq := make(interruptCounter, 4)
			g := NewFrameGraphics(storagestub.NewInMemoryService(), irq)
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			for _, uri := range []string{cs.Graphics.Left, cs.Graphics.Horizontal} {
				img := g.Image(uri)
				img.Resize(1, 1)
				img.Draw(term.NewStringWriter(1, 1))
			}
			irq.wait(t, 2)
			s := newImageScreen(tt.width, tt.height)
			f.Draw(s)
			g.DrawSeparator(s, f, cs, tt.width, tt.height, tt.y)
			if tt.want == nil {
				assert.Empty(t, s.images)
				return
			}
			require.Len(t, s.images, tt.width)
			for i, img := range s.images[:tt.width-2] {
				assert.Equal(t, image.Rect(i+1, tt.y, i+2, tt.y+1), img.Visible())
			}
			for i, img := range s.images[tt.width-2:] {
				assert.Equal(t, tt.want[i], img.Visible(), "junction %d", i)
				assert.Equal(t, 1+max(0, tt.outset.Inward), img.Width, "clipping must not rescale the image")
				assert.Equal(t, 1+max(0, tt.outset.Top)+max(0, tt.outset.Bottom), img.Height)
			}
		})
	}
}

func TestFrameGraphicsSharesOneImagePerURI(t *testing.T) {
	pics := newFramePictures(t)
	irq := make(interruptCounter, 16)
	g := NewFrameGraphics(storagestub.NewInMemoryService(), irq)
	t.Cleanup(func() { assert.NoError(t, g.Close()) })

	frames := make([]*component.Frame, 2)
	for i := range frames {
		frames[i] = component.NewFrame(&component.TestComponent{Ch: 'x'})
		frames[i].Graphics = pics.resolve(component.FrameCharSetGraphics{
			TopLeft: "L", BottomRight: "L",
		})
		frames[i].Resize(3, 3)
	}
	g.Draw(term.NewStringWriter(3, 3), frames[0], 3, 3)
	irq.wait(t, 1)

	s := newImageScreen(3, 3)
	for _, f := range frames {
		g.Draw(s, f, 3, 3)
	}
	require.Len(t, s.images, 4)
	for _, img := range s.images {
		assert.Equal(t, s.images[0].ID, img.ID)
	}
}

func TestFrameGraphicsDrawBackground(t *testing.T) {
	pics := newFramePictures(t)
	type placement struct {
		name  rune
		area  image.Rectangle
		fit   term.ImageFit
		layer term.ImageLayer
	}
	below := term.ImageLayerBelowBackground
	tests := []struct {
		name     string
		graphics BackgroundGraphics
		area     image.Rectangle
		noImages bool
		loads    int
		images   []placement
		bare     []image.Rectangle
	}{
		{
			name: "each slice covers its cells",
			graphics: BackgroundGraphics{
				TopLeft: "L", HorizontalTop: "R", VerticalRight: "i", Center: "s",
			},
			area:  image.Rect(1, 1, 5, 4),
			loads: 4,
			images: []placement{
				{'L', image.Rect(1, 1, 2, 2), term.ImageFitFill, below},
				{'R', image.Rect(2, 1, 4, 2), term.ImageFitFill, below},
				{'i', image.Rect(4, 2, 5, 3), term.ImageFitFill, below},
				{'s', image.Rect(2, 2, 4, 3), term.ImageFitFill, below},
			},
			bare: []image.Rectangle{
				image.Rect(4, 1, 5, 2), image.Rect(1, 3, 2, 4), image.Rect(4, 3, 5, 4),
				image.Rect(2, 3, 4, 4), image.Rect(1, 2, 2, 3),
			},
		},
		{
			name:     "tiles keep their fit",
			graphics: BackgroundGraphics{Center: "s", Fit: term.ImageFitTile},
			area:     image.Rect(0, 0, 4, 4),
			loads:    1,
			images:   []placement{{'s', image.Rect(1, 1, 3, 3), term.ImageFitTile, below}},
			bare: []image.Rectangle{
				image.Rect(0, 0, 1, 1), image.Rect(3, 0, 4, 1), image.Rect(0, 3, 1, 4),
				image.Rect(3, 3, 4, 4), image.Rect(1, 0, 3, 1), image.Rect(1, 3, 3, 4),
				image.Rect(0, 1, 1, 3), image.Rect(3, 1, 4, 3),
			},
		},
		{
			name:     "an area one cell high takes the center alone",
			graphics: BackgroundGraphics{TopLeft: "L", Center: "s"},
			area:     image.Rect(0, 2, 5, 3),
			loads:    1,
			images:   []placement{{'s', image.Rect(0, 2, 5, 3), term.ImageFitFill, below}},
		},
		{
			name:     "a missing image leaves its slice bare",
			graphics: BackgroundGraphics{Center: "missing"},
			area:     image.Rect(0, 0, 3, 1),
			loads:    1,
			bare:     []image.Rectangle{image.Rect(0, 0, 3, 1)},
		},
		{
			name:     "a writer that cannot draw images leaves every slice bare",
			graphics: BackgroundGraphics{Center: "s"},
			area:     image.Rect(0, 0, 3, 1),
			noImages: true,
			loads:    1,
			bare:     []image.Rectangle{image.Rect(0, 0, 3, 1)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gr := tt.graphics
			for _, slot := range []*string{
				&gr.TopLeft, &gr.TopRight, &gr.BottomLeft, &gr.BottomRight,
				&gr.HorizontalTop, &gr.HorizontalBottom, &gr.VerticalLeft,
				&gr.VerticalRight, &gr.Center,
			} {
				*slot = pics.uris[*slot]
			}
			irq := make(interruptCounter, 16)
			g := NewFrameGraphics(storagestub.NewInMemoryService(), irq)
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			g.DrawBackground(term.NewStringWriter(6, 5), gr, tt.area, nil)
			irq.wait(t, tt.loads)

			s := newImageScreen(6, 5)
			s.noImages = tt.noImages
			bare := g.DrawBackground(s, gr, tt.area, nil)
			var images []placement
			for _, img := range s.images {
				images = append(images, placement{pics.name(img), img.Bounds(), img.Fit, img.Layer})
			}
			assert.ElementsMatch(t, tt.images, images)
			assert.ElementsMatch(t, tt.bare, bare)
			for _, c := range s.Cells() {
				assert.Zero(t, c.Ch, "no cell may be written")
			}
		})
	}
}

func TestFrameGraphicsBackgroundImages(t *testing.T) {
	pics := newFramePictures(t)
	irq := make(interruptCounter, 16)
	g := NewFrameGraphics(storagestub.NewInMemoryService(), irq)
	t.Cleanup(func() { assert.NoError(t, g.Close()) })
	area := image.Rect(0, 0, 1, 1)
	filled := BackgroundGraphics{Center: pics.uris["s"]}
	tiled := BackgroundGraphics{Center: pics.uris["s"], Fit: term.ImageFitTile}
	draw := func(gr BackgroundGraphics) term.Image {
		t.Helper()
		s := newImageScreen(1, 1)
		g.DrawBackground(s, gr, area, nil)
		require.Len(t, s.images, 1)
		return s.images[0]
	}
	g.DrawBackground(term.NewStringWriter(1, 1), filled, area, nil)
	g.DrawBackground(term.NewStringWriter(1, 1), tiled, area, nil)
	irq.wait(t, 2)

	before := draw(filled)
	assert.Equal(t, before.ID, draw(filled).ID, "a URI and fit share one ImageID")
	assert.NotEqual(t, before.ID, draw(tiled).ID, "another fit gets its own")

	g.Reload(map[string]image.Image{pics.uris["s"]: pics.imgs['p']})
	after := draw(filled)
	assert.NotEqual(t, before.ID, after.ID, "a reload replaces the image")
	assert.Equal(t, 'p', pics.name(after), "the reloaded picture draws at once")
	assert.Equal(t, 'p', pics.name(draw(tiled)))
	select {
	case <-irq:
		assert.Fail(t, "a reloaded background was fetched")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestFrameGraphicsDrawCell(t *testing.T) {
	pics := newFramePictures(t)
	under := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorTeal}
	cell := term.NewCell('c', 1, term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack})
	pos := term.Coordinates{X: 1}

	tests := []struct {
		name       string
		uri        string
		noImages   bool
		expected   string
		underneath string
	}{
		{name: "an image goes over a blank cell keeping what is under it", uri: "L", expected: "\n.L.", underneath: "\n. ."},
		{name: "no image draws nothing", expected: "\n.u."},
		{name: "an image that fails to load writes the cell", uri: "invalid", expected: "\n.c."},
		{name: "a writer that cannot draw images writes the cell", uri: "L", noImages: true, expected: "\n.c."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewFrameGraphics(storagestub.NewInMemoryService(), term.NopInterrupter())
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			g.Reload(map[string]image.Image{pics.uris["L"]: pics.imgs['L']})

			s := newImageScreen(3, 1)
			s.noImages = tt.noImages
			for x := range 3 {
				s.SetCell(term.Coordinates{X: x}, term.NewCell('.', 1, under))
			}
			s.SetCell(pos, term.NewCell('u', 1, under))
			g.DrawCell(s, pics.uris[tt.uri], pos, cell)
			assert.Equal(t, tt.expected, s.render(t, pics, under, false))
			if tt.underneath != "" {
				assert.Equal(t, tt.underneath, "\n"+s.String())
			}
		})
	}
}

func TestFrameGraphicsDrawOver(t *testing.T) {
	pics := newFramePictures(t)
	navy := term.Attributes{Fg: term.ColorWhite, Bg: term.ColorNavy}
	teal := term.Attributes{Bg: term.ColorTeal}
	gr := component.FrameCharSetGraphics{TopLeft: pics.uris["L"], HorizontalTop: pics.uris["L"]}
	wideCorner := component.FrameCharSetGraphics{TopLeft: pics.uris["L"], CornerWidth: 2, CornerHeight: 2}
	tests := []struct {
		name     string
		graphics *component.FrameCharSetGraphics
		noImages bool
		keep     []term.Coordinates
		// bg is the background of each cell: t for teal, under the
		// frame, and n for navy, the frame's.
		bg string
	}{
		{
			name: "the cells under images keep the background under the frame",
			bg: `
tttn
n..n
nnnn`,
		},
		{
			name:     "the cells whose images do not draw take the frame's",
			noImages: true,
			bg: `
nnnn
n..n
nnnn`,
		},
		{
			name: "kept cells keep the background under the frame without an image",
			keep: []term.Coordinates{{X: 0, Y: 1}, {X: 3, Y: 2}},
			bg: `
tttn
t..n
nnnt`,
		},
		{
			name:     "the cells a corner's image covers keep the background under the frame",
			graphics: &wideCorner,
			bg: `
ttnn
t..n
nnnn`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gr := gr
			if tt.graphics != nil {
				gr = *tt.graphics
			}
			g := NewFrameGraphics(storagestub.NewInMemoryService(), term.NopInterrupter())
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			g.Reload(map[string]image.Image{pics.uris["L"]: pics.imgs['L']})

			s := newImageScreen(4, 3)
			s.noImages = tt.noImages
			for y := range 3 {
				for x := range 4 {
					s.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('u', 1, teal))
				}
			}
			f := component.NewFrame(&component.TestComponent{Ch: '.'})
			f.Attributes = navy
			f.Graphics = gr
			f.Resize(4, 3)
			f.Draw(&FrameUnderWriter{Writer: s, Graphics: gr, Width: 4, Height: 3, Keep: tt.keep})
			g.DrawOver(s, f, 4, 3)
			require.NoError(t, s.Flush())

			var b strings.Builder
			for i, c := range s.Cells() {
				if i%4 == 0 {
					b.WriteByte('\n')
				}
				switch c.Bg {
				case term.ColorTeal:
					b.WriteByte('t')
				case term.ColorNavy:
					b.WriteByte('n')
				default:
					b.WriteByte('.')
				}
			}
			assert.Equal(t, tt.bg, b.String())
		})
	}
}

func TestFrameGraphicsDrawLine(t *testing.T) {
	pics := newFramePictures(t)
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	cell := term.NewCell('-', 1, attr)
	full := LineGraphics{Start: "L", Middle: "R", End: "L"}
	overlay := full
	overlay.Overlay = true
	const width, height = 6, 4

	tests := []struct {
		name     string
		graphics LineGraphics
		from     term.Coordinates
		length   int
		vertical bool
		layer    term.ImageLayer
		noImages bool
		expected string
		// underneath, when set, is what the screen shows under the images.
		underneath string
		// background, when set, draws picture s under the text of the line
		// between its cells and its images, for composite to show.
		background bool
		composite  string
	}{
		{
			name: "a horizontal line", graphics: full,
			from: term.Coordinates{X: 1, Y: 1}, length: 4,
			expected:   "\n......\n.LRRL.\n......\n......",
			underneath: "\n......\n.    .\n......\n......",
		},
		{
			name: "a vertical line", graphics: full,
			from: term.Coordinates{X: 2}, length: 4, vertical: true,
			expected: "\n..L...\n..R...\n..R...\n..L...",
		},
		{
			name: "the ends fall back to the middle", graphics: LineGraphics{Middle: "R"},
			from: term.Coordinates{X: 1, Y: 1}, length: 4,
			expected: "\n......\n.RRRR.\n......\n......",
		},
		{
			name:     "no middle leaves the cells between to their characters",
			graphics: LineGraphics{Start: "L", End: "L"},
			from:     term.Coordinates{X: 1, Y: 1}, length: 4,
			expected:   "\n......\n.L--L.\n......\n......",
			underneath: "\n......\n. -- .\n......\n......",
		},
		{
			name: "a single cell draws the start", graphics: LineGraphics{Start: "L", Middle: "R", End: "R"},
			from: term.Coordinates{X: 1, Y: 1}, length: 1,
			expected: "\n......\n.L....\n......\n......",
		},
		{
			name: "overlay keeps the characters under the images", graphics: overlay,
			from: term.Coordinates{X: 1, Y: 1}, length: 4,
			expected:   "\n......\n.LRRL.\n......\n......",
			underneath: "\n......\n.----.\n......\n......",
		},
		{
			name: "a writer that cannot draw images keeps the characters", graphics: full,
			from: term.Coordinates{X: 1, Y: 1}, length: 4, noImages: true,
			expected: "\n......\n.----.\n......\n......",
		},
		{
			name:     "an image that fails to load keeps its character",
			graphics: LineGraphics{Start: "L", Middle: "invalid", End: "L"},
			from:     term.Coordinates{X: 1, Y: 1}, length: 4,
			expected: "\n......\n.L--L.\n......\n......",
		},
		{
			name:     "an image still loading keeps its character",
			graphics: LineGraphics{Start: "L", Middle: "i", End: "L"},
			from:     term.Coordinates{X: 1, Y: 1}, length: 4,
			expected: "\n......\n.L--L.\n......\n......",
		},
		{
			name:     "the images under the line show through it in place of its characters",
			graphics: LineGraphics{Middle: "t"}, background: true,
			from: term.Coordinates{X: 1, Y: 1}, length: 4,
			composite: "\n......\n.ssss.\n......\n......",
		},
		{
			name:     "the images under the line stay under its characters",
			graphics: LineGraphics{Middle: "t", Overlay: true}, background: true,
			from: term.Coordinates{X: 1, Y: 1}, length: 4,
			composite: "\n......\n.----.\n......\n......",
		},
		{
			name: "an image below the text stays under the glyph of its row", graphics: overlay,
			from: term.Coordinates{X: 1, Y: 1}, length: 4, layer: term.ImageLayerBelowText,
			expected: "\n......\n.----.\n......\n......",
		},
		{
			name: "an empty line draws nothing", graphics: full,
			from:     term.Coordinates{X: 1, Y: 1},
			expected: "\n......\n......\n......\n......",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewFrameGraphics(storagestub.NewInMemoryService(), term.NopInterrupter())
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			g.Reload(map[string]image.Image{
				pics.uris["L"]: pics.imgs['L'], pics.uris["R"]: pics.imgs['R'],
				pics.uris["s"]: pics.imgs['s'], pics.uris["t"]: pics.imgs['t'],
			})
			lg := tt.graphics
			for _, uri := range []*string{&lg.Start, &lg.Middle, &lg.End} {
				*uri = pics.uris[*uri]
			}

			s := newImageScreen(width, height)
			s.noImages = tt.noImages
			for y := range height {
				for x := range width {
					s.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('.', 1, attr))
				}
			}
			lg.DrawCells(s, tt.from, tt.length, tt.vertical, cell)
			if tt.background {
				bg := g.Image(pics.uris["s"])
				images := &component.ButtonImages{
					Idle: component.ButtonImage{Left: bg, Middle: bg, Right: bg},
				}
				require.True(t, images.Draw(s, image.Rect(
					tt.from.X, tt.from.Y, tt.from.X+tt.length, tt.from.Y+1), component.ButtonIdle))
			}
			g.DrawLine(s, lg, tt.from, tt.length, tt.vertical, cell, tt.layer)
			if tt.background {
				assert.Equal(t, tt.composite, s.composite(t, pics))
				return
			}
			assert.Equal(t, tt.expected, s.render(t, pics, attr, tt.graphics.Overlay))
			if tt.underneath != "" {
				assert.Equal(t, tt.underneath, "\n"+s.String())
			}
			for _, img := range s.images {
				assert.Equal(t, tt.layer, img.Layer)
			}
		})
	}
}

func TestFrameGraphicsReload(t *testing.T) {
	pics := newFramePictures(t)
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	const width, height = 3, 3

	tests := []struct {
		name string
		// before is the picture drawn, once loaded, before the reload.
		before string
		// held takes the corner's image before the reload without
		// loading it, and draws it after.
		held bool
		// slot is the picture the top-left corner names.
		slot string
		// reload maps the names of the URIs reloaded to their pictures.
		reload map[string]rune
		// wantLoads counts the images fetched after the reload.
		wantLoads int
		expected  string
		// wantNewID reports that the corner's ImageID changed.
		wantNewID bool
	}{
		{
			name:   "a reloaded URI draws at once without fetching",
			slot:   "missing",
			reload: map[string]rune{"missing": 'L'},
			expected: `
L─┐
│x│
└─┘`,
		},
		{
			name:      "a URI missing from the reload is fetched again",
			before:    "L",
			slot:      "L",
			reload:    map[string]rune{"R": 'R'},
			wantLoads: 1,
			expected: `
L─┐
│x│
└─┘`,
			wantNewID: true,
		},
		{
			name:      "an image held before the reload is closed",
			held:      true,
			slot:      "L",
			reload:    map[string]rune{"R": 'R'},
			wantLoads: 1,
			expected: `
L─┐
│x│
└─┘`,
		},
		{
			name:   "a reloaded URI gets a new ImageID",
			before: "L",
			slot:   "L",
			reload: map[string]rune{"L": 'L'},
			expected: `
L─┐
│x│
└─┘`,
			wantNewID: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := component.NewFrame(&component.TestComponent{Ch: 'x'})
			f.Attributes = attr
			f.Graphics = pics.resolve(component.FrameCharSetGraphics{TopLeft: tt.slot})
			f.Resize(width, height)
			irq := make(interruptCounter, 16)
			g := NewFrameGraphics(storagestub.NewInMemoryService(), irq)
			t.Cleanup(func() { assert.NoError(t, g.Close()) })

			var old tui.Component
			var oldID term.ImageID
			if tt.before != "" {
				g.Draw(term.NewStringWriter(width, height), f, width, height)
				irq.wait(t, 1)
				s := newImageScreen(width, height)
				g.Draw(s, f, width, height)
				require.Len(t, s.images, 1)
				oldID = s.images[0].ID
				old = g.Image(f.Graphics.TopLeft)
			}
			if tt.held {
				old = g.Image(f.Graphics.TopLeft)
			}

			images := make(map[string]image.Image, len(tt.reload))
			for name, pic := range tt.reload {
				images[pics.uris[name]] = pics.imgs[pic]
			}
			g.Reload(images)
			if old != nil {
				assert.NotSame(t, old, g.Image(f.Graphics.TopLeft), "the old image is replaced")
			}
			if tt.held {
				old.Resize(1, 1)
				old.Draw(term.NewStringWriter(1, 1))
			}

			if tt.wantLoads > 0 {
				g.Draw(term.NewStringWriter(width, height), f, width, height)
				irq.wait(t, tt.wantLoads)
			}
			s := newImageScreen(width, height)
			f.Draw(s)
			g.Draw(s, f, width, height)
			assert.Equal(t, tt.expected, s.render(t, pics, attr, false))
			require.Len(t, s.images, 1)
			if tt.wantNewID {
				assert.NotEqual(t, oldID, s.images[0].ID)
			}
			select {
			case <-irq:
				assert.Fail(t, "an image loaded after the reload settled")
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

func TestFrameGraphicsDrawScrollBar(t *testing.T) {
	pics := newFramePictures(t)
	attr := term.Attributes{Fg: term.ColorBlue, Bg: term.ColorBlack}
	runes := component.ScrollBarCharSet{Top: 't', Middle: 'm', Bottom: 'b'}
	withGraphics := func(g component.ScrollBarCharSetGraphics) component.ScrollBarCharSet {
		cs := runes
		cs.Graphics = g
		return cs
	}

	tests := []struct {
		name       string
		cs         component.ScrollBarCharSet
		outset     int
		notScroll  bool
		noImages   bool
		notLoaded  bool
		expected   string
		underneath string
	}{
		{
			name: "no graphics draws the runes",
			cs:   runes,
			expected: `
┌────┐
│xxxxt
│xxxxm
│xxxxb
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name: "every slot is drawn with its picture",
			cs: withGraphics(component.ScrollBarCharSetGraphics{
				Top: "L", Middle: "R", Bottom: "L",
			}),
			expected: `
┌────┐
│xxxxL
│xxxxR
│xxxxL
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name: "the ends default to the middle",
			cs:   withGraphics(component.ScrollBarCharSetGraphics{Middle: "R"}),
			expected: `
┌────┐
│xxxxR
│xxxxR
│xxxxR
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name: "an end without a middle leaves the rest to their runes",
			cs:   withGraphics(component.ScrollBarCharSetGraphics{Top: "L"}),
			expected: `
┌────┐
│xxxxL
│xxxxm
│xxxxb
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name: "an image without a rune falls back to the frame's",
			cs: component.ScrollBarCharSet{
				Graphics: component.ScrollBarCharSetGraphics{Middle: "missing"},
			},
			expected: `
┌────┐
│xxxx│
│xxxx│
│xxxx│
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:     "a writer that cannot draw images gets the runes",
			cs:       withGraphics(component.ScrollBarCharSetGraphics{Middle: "R"}),
			noImages: true,
			expected: `
┌────┐
│xxxxt
│xxxxm
│xxxxb
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:      "images that are not loaded yet leave the runes",
			cs:        withGraphics(component.ScrollBarCharSetGraphics{Middle: "R"}),
			notLoaded: true,
			expected: `
┌────┐
│xxxxt
│xxxxm
│xxxxb
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name: "overlay draws the pictures over the runes",
			cs: withGraphics(component.ScrollBarCharSetGraphics{
				Middle: "R", Overlay: true,
			}),
			expected: `
┌────┐
│xxxxR
│xxxxR
│xxxxR
│xxxx│
│xxxx│
└────┘`,
			underneath: `
┌────┐
│xxxxt
│xxxxm
│xxxxb
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:      "a frame without a scroll bar draws nothing",
			cs:        withGraphics(component.ScrollBarCharSetGraphics{Middle: "R"}),
			notScroll: true,
			expected: `
┌────┐
│xxxx│
│xxxx│
│xxxx│
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:   "an outset stretches each picture over the cells left of it",
			cs:     withGraphics(component.ScrollBarCharSetGraphics{Top: "L", Middle: "R"}),
			outset: 2,
			expected: `
┌────┐
│xxLLL
│xxRRR
│xxRRR
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:   "an outset stops at the left edge of the frame",
			cs:     withGraphics(component.ScrollBarCharSetGraphics{Middle: "R"}),
			outset: 10,
			expected: `
┌────┐
RRRRRR
RRRRRR
RRRRRR
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:   "a negative outset is none",
			cs:     withGraphics(component.ScrollBarCharSetGraphics{Middle: "R"}),
			outset: -1,
			expected: `
┌────┐
│xxxxR
│xxxxR
│xxxxR
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:   "an outset without pictures leaves the cells as they are",
			cs:     runes,
			outset: 2,
			expected: `
┌────┐
│xxxxt
│xxxxm
│xxxxb
│xxxx│
│xxxx│
└────┘`,
		},
		{
			name:      "the outset of pictures that are not loaded yet is left as it is",
			cs:        withGraphics(component.ScrollBarCharSetGraphics{Middle: "R"}),
			outset:    2,
			notLoaded: true,
			expected: `
┌────┐
│xxxxt
│xxxxm
│xxxxb
│xxxx│
│xxxx│
└────┘`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const width, height = 6, 7
			var content tui.Component = scrollingContent{TestComponent: &component.TestComponent{Ch: 'x'}}
			if tt.notScroll {
				content = &component.TestComponent{Ch: 'x'}
			}
			f := component.NewFrame(content)
			f.Attributes = attr
			f.ScrollBarAttributes = attr
			f.ScrollBarCharSet = tt.cs
			f.ScrollBarCharSet.Graphics = pics.resolveScrollBar(tt.cs.Graphics)
			f.Resize(width, height)

			irq := make(interruptCounter, 16)
			g := NewFrameGraphics(storagestub.NewInMemoryService(), irq)
			t.Cleanup(func() { assert.NoError(t, g.Close()) })
			loads := pics.loadsScrollBar(tt.cs.Graphics)
			if tt.notScroll {
				loads = 0
			}
			if !tt.notLoaded {
				g.DrawScrollBar(term.NewStringWriter(width, height), f, width, height, tt.outset)
				irq.wait(t, loads)
			}

			s := newImageScreen(width, height)
			s.noImages = tt.noImages
			f.Draw(s)
			g.DrawScrollBar(s, f, width, height, tt.outset)
			assert.Equal(t, tt.expected, s.render(t, pics, attr, tt.cs.Graphics.Overlay))
			if tt.underneath != "" {
				assert.Equal(t, tt.underneath, "\n"+s.String())
			}

			if tt.notLoaded {
				irq.wait(t, loads)
			}
		})
	}
}

// framePictures holds two pictures, named L and R, five of a single
// gray, named i, s, h, v and p, and a transparent one, named t, on disk.
// The graphics of a test case name them, or "missing" for a file that
// does not exist and "invalid" for a URI imageuri rejects.
type framePictures struct {
	uris map[string]string
	pix  map[rune][]byte
	imgs map[rune]*image.RGBA
}

func newFramePictures(t *testing.T) framePictures {
	t.Helper()
	dir := t.TempDir()
	p := framePictures{
		uris: map[string]string{"invalid": "ftp://example.com/x.png"},
		pix:  make(map[rune][]byte),
		imgs: make(map[rune]*image.RGBA),
	}
	fileURI := func(name string) string {
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, name))}).String()
	}
	p.uris["missing"] = fileURI("missing.png")
	for name, data := range map[rune][]byte{
		'L': imageuritest.StatusBarLeftPNG,
		'R': imageuritest.StatusBarRightPNG,
		'i': grayPNG(t, 128),
		's': grayPNG(t, 255),
		'h': grayPNG(t, 40),
		'v': grayPNG(t, 200),
		'p': grayPNG(t, 75),
		't': transparentPNG(t),
	} {
		file := string(name) + ".png"
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), data, 0o600))
		p.uris[string(name)] = fileURI(file)

		src, err := png.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		rgba := image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
		draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
		p.pix[name] = rgba.Pix
		p.imgs[name] = rgba
	}
	return p
}

// grayPNG encodes a picture of a single pixel of luminance y.
func grayPNG(t *testing.T, y uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, 1, 1))
	img.SetGray(0, 0, color.Gray{Y: y})
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, img))
	return b.Bytes()
}

// transparentPNG encodes a picture of a single transparent pixel.
func transparentPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	require.NoError(t, png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 1, 1))))
	return b.Bytes()
}

// name returns the name of the picture img shows, or '?' for another.
func (p framePictures) name(img term.Image) rune {
	rgba, ok := img.Src.(*image.RGBA)
	if !ok {
		return '?'
	}
	for n, pix := range p.pix {
		if bytes.Equal(rgba.Pix, pix) {
			return n
		}
	}
	return '?'
}

// resolve replaces the picture names in g with their URIs.
func (p framePictures) resolve(g component.FrameCharSetGraphics) component.FrameCharSetGraphics {
	for _, slot := range graphicsSlots(&g) {
		*slot = p.uris[*slot]
	}
	return g
}

// loads counts the images g starts loading, which each interrupt once.
func (p framePictures) loads(g component.FrameCharSetGraphics) int {
	names := make(map[string]bool)
	for _, slot := range graphicsSlots(&g) {
		if *slot != "" && *slot != "invalid" {
			names[*slot] = true
		}
	}
	return len(names)
}

func graphicsSlots(g *component.FrameCharSetGraphics) []*string {
	return []*string{
		&g.TopLeft, &g.TopRight, &g.BottomLeft, &g.BottomRight,
		&g.HorizontalTop, &g.HorizontalBottom, &g.VerticalLeft, &g.VerticalRight,
	}
}

// resolveScrollBar replaces the picture names in g with their URIs.
func (p framePictures) resolveScrollBar(
	g component.ScrollBarCharSetGraphics,
) component.ScrollBarCharSetGraphics {
	for _, slot := range []*string{&g.Top, &g.Middle, &g.Bottom} {
		*slot = p.uris[*slot]
	}
	return g
}

// loadsScrollBar counts the images g starts loading, which each
// interrupt once.
func (p framePictures) loadsScrollBar(g component.ScrollBarCharSetGraphics) int {
	return p.loads(component.FrameCharSetGraphics{
		TopLeft: g.Top, HorizontalTop: g.Middle, TopRight: g.Bottom,
	})
}

// resolveWindowBar replaces the picture names in g with their URIs.
func (p framePictures) resolveWindowBar(g WindowBarGraphics) WindowBarGraphics {
	for _, slot := range []*string{&g.Left, &g.Horizontal, &g.Right} {
		*slot = p.uris[*slot]
	}
	return g
}

// loadsWindowBar counts the images g starts loading, which each
// interrupt once.
func (p framePictures) loadsWindowBar(g WindowBarGraphics) int {
	return p.loads(component.FrameCharSetGraphics{
		TopLeft: g.Left, HorizontalTop: g.Horizontal, TopRight: g.Right,
	})
}

// imageScreen is a string writer that records the images drawn into
// it, or reports that it cannot draw them when noImages is set.
type imageScreen struct {
	*term.StringWriter
	width, height int
	images        []term.Image
	// drawnAt holds when each image was drawn, and writtenAt when each
	// cell was last written, for a cell to cover the images drawn
	// before it, as compositing writers do.
	drawnAt   []int
	writtenAt []int
	writes    int
	noImages  bool
	// over is where images go over the cells as they are, as those of a
	// floating window's frame do, rather than over blank cells.
	over image.Rectangle
}

func newImageScreen(width, height int) *imageScreen {
	return &imageScreen{
		StringWriter: term.NewStringWriter(width, height),
		width:        width,
		height:       height,
		writtenAt:    make([]int, width*height),
	}
}

func (s *imageScreen) SetCell(pos term.Coordinates, c term.Cell) {
	s.StringWriter.SetCell(pos, c)
	s.writes++
	if pos.X >= 0 && pos.X < s.width && pos.Y >= 0 && pos.Y < s.height {
		s.writtenAt[pos.Y*s.width+pos.X] = s.writes
	}
}

func (s *imageScreen) DrawImage(img term.Image) bool {
	if s.noImages {
		return false
	}
	s.writes++
	s.images = append(s.images, img)
	s.drawnAt = append(s.drawnAt, s.writes)
	return true
}

// render returns the screen with the cells of each image above the text
// replaced by the name of its picture, and those of an image below the
// text as they are, since their text covers it. It checks that every
// image fills its cells, that the cell under each one-cell image above
// the text and outside over has attr and is blank unless overlay, and
// that each picture has a single ImageID.
func (s *imageScreen) render(
	t *testing.T, pics framePictures, attr term.Attributes, overlay bool,
) string {
	t.Helper()
	require.NoError(t, s.Flush())
	rows := strings.Split(s.String(), "\n")
	screen := make([][]rune, len(rows))
	for i, row := range rows {
		screen[i] = []rune(row)
	}
	ids := make(map[rune]term.ImageID)
	for _, img := range s.images {
		assert.Equal(t, term.ImageFitFill, img.Fit)
		above := img.Layer == term.ImageLayerAboveText
		at := image.Pt(img.Pos.X, img.Pos.Y)
		if above && img.Width == 1 && img.Height == 1 && !at.In(s.over) {
			cell := s.Cells()[img.Pos.Y*s.width+img.Pos.X]
			if !overlay {
				assert.Equal(t, ' ', cell.Ch, "the cell under the image at %v", img.Pos)
			}
			assert.Equal(t, attr, cell.Attributes(), "the cell under the image at %v", img.Pos)
		}

		name := pics.name(img)
		if id, ok := ids[name]; ok {
			assert.Equal(t, id, img.ID, "picture %c has one ImageID", name)
		}
		ids[name] = img.ID
		if !above {
			continue
		}
		visible := img.Visible().Intersect(image.Rect(0, 0, s.width, s.height))
		for y := visible.Min.Y; y < visible.Max.Y; y++ {
			for x := visible.Min.X; x < visible.Max.X; x++ {
				screen[y][x] = name
			}
		}
	}
	if len(ids) == 2 {
		assert.NotEqual(t, ids['L'], ids['R'])
	}
	var b strings.Builder
	for _, row := range screen {
		b.WriteByte('\n')
		b.WriteString(string(row))
	}
	return b.String()
}

// composite returns the screen with the cells each image shows replaced
// by the name of its picture, as a writer that composes the layers
// shows them: an image above the text over any cell, and one below the
// text only over a blank cell no image above the text covers. A later
// image goes over the earlier ones of its layer, a cell written after an
// image covers it, and the transparent picture shows what is under it.
func (s *imageScreen) composite(t *testing.T, pics framePictures) string {
	t.Helper()
	require.NoError(t, s.Flush())
	rows := strings.Split(s.String(), "\n")
	screen := make([][]rune, len(rows))
	for i, row := range rows {
		screen[i] = []rune(row)
	}
	cells := s.Cells()
	above := make([]bool, len(cells))
	for k, img := range s.images {
		name := pics.name(img)
		if name == 't' {
			continue
		}
		isAbove := img.Layer == term.ImageLayerAboveText
		visible := img.Visible().Intersect(image.Rect(0, 0, s.width, s.height))
		for y := visible.Min.Y; y < visible.Max.Y; y++ {
			for x := visible.Min.X; x < visible.Max.X; x++ {
				i := y*s.width + x
				if s.writtenAt[i] > s.drawnAt[k] {
					continue
				}
				blank := cells[i].Ch == 0 || cells[i].Ch == ' '
				if !isAbove && (above[i] || !blank) {
					continue
				}
				above[i] = above[i] || isAbove
				screen[y][x] = name
			}
		}
	}
	var b strings.Builder
	for _, row := range screen {
		b.WriteByte('\n')
		b.WriteString(string(row))
	}
	return b.String()
}

// interruptCounter is a term.Interrupter that queues every interrupt.
type interruptCounter chan struct{}

func (c interruptCounter) Interrupt(context.Context) error {
	c <- struct{}{}
	return nil
}

func (c interruptCounter) wait(t testing.TB, n int) {
	t.Helper()
	for range n {
		select {
		case <-c:
		case <-time.After(5 * time.Second):
			require.FailNow(t, "timed out waiting for the images to load")
		}
	}
}

// scrollingContent reports content past its bottom so that a frame
// draws a scroll bar.
type scrollingContent struct {
	*component.TestComponent
}

func (scrollingContent) SeekUp() bool       { return false }
func (scrollingContent) SeekDown() bool     { return false }
func (scrollingContent) SeekOffset() int    { return 0 }
func (scrollingContent) MaxSeekOffset() int { return 4 }
