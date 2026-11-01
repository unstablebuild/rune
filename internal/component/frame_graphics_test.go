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
	"image"
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

// framePictures holds two pictures, named L and R, on disk. The
// graphics of a test case name them, or "missing" for a file that does
// not exist and "invalid" for a URI imageuri rejects.
type framePictures struct {
	uris map[string]string
	pix  map[rune][]byte
}

func newFramePictures(t *testing.T) framePictures {
	t.Helper()
	dir := t.TempDir()
	p := framePictures{
		uris: map[string]string{"invalid": "ftp://example.com/x.png"},
		pix:  make(map[rune][]byte),
	}
	fileURI := func(name string) string {
		return (&url.URL{Scheme: "file", Path: filepath.ToSlash(filepath.Join(dir, name))}).String()
	}
	p.uris["missing"] = fileURI("missing.png")
	for name, data := range map[rune][]byte{
		'L': imageuritest.StatusBarLeftPNG,
		'R': imageuritest.StatusBarRightPNG,
	} {
		file := string(name) + ".png"
		require.NoError(t, os.WriteFile(filepath.Join(dir, file), data, 0o600))
		p.uris[string(name)] = fileURI(file)

		src, err := png.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		rgba := image.NewRGBA(image.Rect(0, 0, src.Bounds().Dx(), src.Bounds().Dy()))
		draw.Draw(rgba, rgba.Bounds(), src, src.Bounds().Min, draw.Src)
		p.pix[name] = rgba.Pix
	}
	return p
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
	noImages      bool
}

func newImageScreen(width, height int) *imageScreen {
	return &imageScreen{
		StringWriter: term.NewStringWriter(width, height),
		width:        width,
		height:       height,
	}
}

func (s *imageScreen) DrawImage(img term.Image) bool {
	if s.noImages {
		return false
	}
	s.images = append(s.images, img)
	return true
}

// render returns the screen with the cells of each image above the text
// replaced by the name of its picture, and those of an image below the
// text as they are, since their text covers it. It checks that every
// image fills its cells, that the cell under each one-cell image above
// the text has attr and is blank unless overlay, and that each picture
// has a single ImageID.
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
		if above && img.Width == 1 && img.Height == 1 {
			cell := s.Cells()[img.Pos.Y*s.width+img.Pos.X]
			if !overlay {
				assert.Equal(t, ' ', cell.Ch, "the cell under the image at %v", img.Pos)
			}
			assert.Equal(t, attr, cell.Attributes(), "the cell under the image at %v", img.Pos)
		}

		name := '?'
		for n, pix := range pics.pix {
			if rgba, ok := img.Src.(*image.RGBA); ok && bytes.Equal(rgba.Pix, pix) {
				name = n
			}
		}
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

// interruptCounter is a term.Interrupter that queues every interrupt.
type interruptCounter chan struct{}

func (c interruptCounter) Interrupt(context.Context) error {
	c <- struct{}{}
	return nil
}

func (c interruptCounter) wait(t *testing.T, n int) {
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
