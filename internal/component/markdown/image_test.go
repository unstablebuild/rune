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

package markdown

import (
	"context"
	"image"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/workspace"
)

func TestImageCells(t *testing.T) {
	sample := image.Pt(194, 179)
	tests := []struct {
		name     string
		natural  image.Point
		px       cell.PixelSize
		width    int
		size     imageSize
		wantCols int
		wantRows int
	}{
		{
			name:    "smaller than the column",
			natural: sample, px: cell.PixelSize{Width: 10, Height: 20}, width: 80,
			wantCols: 16, wantRows: 7,
		},
		{
			name:    "the same box on a denser screen",
			natural: sample, px: cell.PixelSize{Width: 17, Height: 34}, width: 80,
			wantCols: 16, wantRows: 7,
		},
		{
			name:    "the same box when the font zooms",
			natural: sample, px: cell.PixelSize{Width: 15, Height: 30}, width: 80,
			wantCols: 16, wantRows: 7,
		},
		{
			name:    "wider with taller cells",
			natural: sample, px: cell.PixelSize{Width: 10, Height: 30}, width: 80,
			wantCols: 24, wantRows: 7,
		},
		{
			name:    "shrunk to the column with the aspect kept",
			natural: sample, px: cell.PixelSize{Width: 10, Height: 20}, width: 10,
			wantCols: 10, wantRows: 5,
		},
		{
			name:    "a tiny image takes a cell",
			natural: image.Pt(4, 4), px: cell.PixelSize{Width: 10, Height: 20}, width: 80,
			wantCols: 1, wantRows: 1,
		},
		{
			name:    "rounds each side to the nearest cell",
			natural: image.Pt(100, 36), px: cell.PixelSize{Width: 10, Height: 20}, width: 80,
			// 83.3x30 px: 8.3x1.5 cells.
			wantCols: 8, wantRows: 2,
		},
		{
			name:    "no column",
			natural: sample, px: cell.PixelSize{Width: 10, Height: 20},
		},
		{
			name:    "cols with the aspect kept",
			natural: sample, px: px10x20, width: 80, size: imageSize{cols: 8},
			// 80x73.8 px.
			wantCols: 8, wantRows: 4,
		},
		{
			name:    "rows with the aspect kept",
			natural: sample, px: px10x20, width: 80, size: imageSize{rows: 4},
			// 86.7x80 px.
			wantCols: 9, wantRows: 4,
		},
		{
			name:    "cols and rows box the picture",
			natural: sample, px: px10x20, width: 80, size: imageSize{cols: 20, rows: 4},
			wantCols: 20, wantRows: 4,
		},
		{
			name:    "cols and rows stretched",
			natural: sample, px: px10x20, width: 80,
			size:     imageSize{cols: 20, rows: 4, stretch: true},
			wantCols: 20, wantRows: 4,
		},
		{
			name:    "cols stretched keep GitHub's rows",
			natural: sample, px: px10x20, width: 80, size: imageSize{cols: 20, stretch: true},
			wantCols: 20, wantRows: 7,
		},
		{
			name:    "cols shrunk to the column with the aspect kept",
			natural: sample, px: px10x20, width: 10, size: imageSize{cols: 20},
			// 100x92.3 px.
			wantCols: 10, wantRows: 5,
		},
		{
			name:    "a box shrunk to the column with its proportions kept",
			natural: sample, px: px10x20, width: 10, size: imageSize{cols: 20, rows: 4},
			wantCols: 10, wantRows: 2,
		},
		{
			name:    "a stretched box shrunk to the column keeps its rows",
			natural: sample, px: px10x20, width: 10,
			size:     imageSize{cols: 20, rows: 4, stretch: true},
			wantCols: 10, wantRows: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cols, rows := imageCells(tt.natural, tt.px, tt.width, tt.size)
			assert.Equal(t, tt.wantCols, cols, "cols")
			assert.Equal(t, tt.wantRows, rows, "rows")
		})
	}
}

// px10x20 is the cell size the image tests draw with. An image of
// cells(c, r) lays out as c columns by r rows in it.
var px10x20 = cell.PixelSize{Width: 10, Height: 20}

func cells(cols, rows int) image.Point {
	return image.Pt(cols*12, rows*githubLineHeight)
}

func TestImageBlock(t *testing.T) {
	tests := []struct {
		name    string
		content string
		width   int
		// noCell draws without a cell size in the writer's context.
		noCell bool
		// images are the pictures the resolver returns by destination;
		// it declines every other one.
		images     map[string]*fakeImage
		want       string
		wantStarts int
	}{
		{
			name:    "labels without a cell size",
			content: "![a](a.png) ![b](b.png)",
			width:   12, noCell: true,
			images: map[string]*fakeImage{
				"a.png": loaded(cells(4, 2)), "b.png": loaded(cells(3, 1)),
			},
			want: `
a b         
            `,
		},
		{
			name:    "flows images with gaps, bottom-aligned",
			content: "![a](a.png) ![b](b.png)![c](c.png)",
			width:   12,
			images: map[string]*fakeImage{
				"a.png": loaded(cells(4, 2)), "b.png": loaded(cells(3, 1)),
				"c.png": loaded(cells(2, 3)),
			},
			want: `
        ██  
████    ██  
████ █████  
            `,
			wantStarts: 3,
		},
		{
			name:    "wraps an image that does not fit",
			content: "![a](a.png) ![b](b.png) ![c](c.png)",
			width:   8,
			images: map[string]*fakeImage{
				"a.png": loaded(cells(4, 2)), "b.png": loaded(cells(3, 1)),
				"c.png": loaded(cells(2, 1)),
			},
			want: `
████    
████ ███
██      
        `,
			wantStarts: 3,
		},
		{
			name:    "a hard break starts a row",
			content: "![a](a.png)\\\n![b](b.png)",
			width:   8,
			images: map[string]*fakeImage{
				"a.png": loaded(cells(2, 1)), "b.png": loaded(cells(3, 1)),
			},
			want: `
██      
███     
        `,
			wantStarts: 2,
		},
		{
			name:    "shrinks an image to the width",
			content: "![a](a.png)",
			width:   10,
			images:  map[string]*fakeImage{"a.png": loaded(image.Pt(194, 179))},
			want: `
██████████
██████████
██████████
██████████
██████████
          `,
			wantStarts: 1,
		},
		{
			name:    "labels images that failed, are loading or were declined",
			content: "![failed](a.png) ![loading](b.png) ![declined](c.png)",
			width:   20,
			images: map[string]*fakeImage{
				"a.png": {settled: true}, "b.png": {},
			},
			want: `
failed loading      
declined            
                    `,
			wantStarts: 2,
		},
		{
			name:    "labels an image without alt text with its destination",
			content: "![](a.png) ![b](b.png)",
			width:   12,
			images:  map[string]*fakeImage{"b.png": loaded(cells(2, 2))},
			want: `
      ██    
a.png ██    
            `,
			wantStarts: 1,
		},
		{
			name:    "cuts a label to the width",
			content: "![a long label](a.png)",
			width:   6,
			want: `
a long
      `,
		},
		{
			name:    "centers a picture over a wider caption",
			content: `![a](a.png "Acme")`,
			width:   8,
			images:  map[string]*fakeImage{"a.png": loaded(cells(2, 2))},
			want: `
 ██     
 ██     
Acme    
        `,
			wantStarts: 1,
		},
		{
			name:    "centers a caption under a wider picture",
			content: `[![a](a.png "A")](https://example.com) ![b](b.png "Bo")`,
			width:   8,
			images: map[string]*fakeImage{
				"a.png": loaded(cells(4, 1)), "b.png": loaded(cells(1, 2)),
			},
			want: `
     █  
████ █  
 A   Bo 
        `,
			wantStarts: 2,
		},
		{
			name:    "cuts a caption to the width",
			content: `![a](a.png "Acme")`,
			width:   3,
			images:  map[string]*fakeImage{"a.png": loaded(cells(2, 1))},
			want: `
██ 
Acm
   `,
			wantStarts: 1,
		},
		{
			name:    "a picture that is not drawn shows its label without its caption",
			content: `![a](a.png "Acme")`,
			width:   6,
			images:  map[string]*fakeImage{"a.png": {}},
			want: `
a     
      `,
			wantStarts: 1,
		},
		{
			name:    "sizes pictures by their attributes",
			content: "![a](a.png){cols=3} ![b](b.png){rows=2} ![c](c.png){cols=2 rows=1}",
			width:   10,
			images: map[string]*fakeImage{
				"a.png": loaded(cells(6, 2)), "b.png": loaded(cells(2, 2)),
				"c.png": loaded(cells(1, 1)),
			},
			want: `
    ██    
███ ██ ██ 
          `,
			wantStarts: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md := newImageComponent(t, tt.content, tt.images)
			lines := 1
			for _, r := range tt.want[1:] {
				if r == '\n' {
					lines++
				}
			}
			w := newImageWriter(tt.width, lines, !tt.noCell)
			comptest.TestComponent(t, md, w, []comptest.TestCase{
				{Action: func() { md.Resize(tt.width, lines) }, Expected: tt.want},
			})
			starts := 0
			for _, img := range tt.images {
				starts += img.starts
			}
			assert.Equal(t, tt.wantStarts, starts)
		})
	}
}

func TestImageBlockPositions(t *testing.T) {
	// a is drawn at 0,0-3,1; b's label at 5,1-11,1.
	md := newImageComponent(t, "[![a](a.png)](https://example.com) ![loading](b.png)",
		map[string]*fakeImage{"a.png": loaded(cells(4, 2)), "b.png": {}})
	md.Resize(12, 3)
	md.Draw(newImageWriter(12, 3, true))

	tests := []struct {
		x, y          int
		wantText      string
		wantURL       string
		wantSpan      bool
		wantChar      rune
		wantCharFound bool
	}{
		{x: 0, y: 0, wantText: "a", wantURL: "https://example.com", wantSpan: true},
		{x: 3, y: 1, wantText: "a", wantURL: "https://example.com", wantSpan: true},
		{x: 4, y: 1},
		{x: 5, y: 0},
		{x: 5, y: 1, wantText: "loading", wantURL: "b.png", wantSpan: true,
			wantChar: 'l', wantCharFound: true},
		{x: 11, y: 1, wantText: "loading", wantURL: "b.png", wantSpan: true,
			wantChar: 'g', wantCharFound: true},
		{x: 12, y: 1},
		{x: 0, y: 2},
	}
	for _, tt := range tests {
		text, url, ok := md.SpanAt(tt.x, tt.y)
		assert.Equal(t, tt.wantSpan, ok, "SpanAt(%d, %d)", tt.x, tt.y)
		assert.Equal(t, tt.wantText, text, "SpanAt(%d, %d)", tt.x, tt.y)
		assert.Equal(t, tt.wantURL, url, "SpanAt(%d, %d)", tt.x, tt.y)
		ch, ok := md.blocks[0].CharAt(tt.x, tt.y)
		assert.Equal(t, tt.wantCharFound, ok, "CharAt(%d, %d)", tt.x, tt.y)
		assert.Equal(t, tt.wantChar, ch, "CharAt(%d, %d)", tt.x, tt.y)
	}
	assert.Equal(t, "\nloading\n", md.TextRange(term.Coordinates{}, term.Coordinates{X: 12, Y: 2}))
}

func TestImageBlockCaptionPositions(t *testing.T) {
	// The picture is drawn at 1,0-2,0 and its caption at 0,1-3,1.
	md := newImageComponent(t, `[![a](a.png "Acme")](https://example.com)`,
		map[string]*fakeImage{"a.png": loaded(cells(2, 1))})
	md.Resize(8, 3)
	md.Draw(newImageWriter(8, 3, true))

	tests := []struct {
		x, y          int
		wantSpan      bool
		wantChar      rune
		wantCharFound bool
	}{
		{x: 0, y: 0, wantSpan: true},
		{x: 1, y: 0, wantSpan: true},
		{x: 0, y: 1, wantSpan: true, wantChar: 'A', wantCharFound: true},
		{x: 3, y: 1, wantSpan: true, wantChar: 'e', wantCharFound: true},
		{x: 4, y: 1},
		{x: 0, y: 2},
	}
	for _, tt := range tests {
		_, url, ok := md.SpanAt(tt.x, tt.y)
		assert.Equal(t, tt.wantSpan, ok, "SpanAt(%d, %d)", tt.x, tt.y)
		if ok {
			assert.Equal(t, "https://example.com", url, "SpanAt(%d, %d)", tt.x, tt.y)
		}
		ch, ok := md.blocks[0].CharAt(tt.x, tt.y)
		assert.Equal(t, tt.wantCharFound, ok, "CharAt(%d, %d)", tt.x, tt.y)
		assert.Equal(t, tt.wantChar, ch, "CharAt(%d, %d)", tt.x, tt.y)
	}
	assert.Equal(t, "\nAcme\n", md.TextRange(term.Coordinates{}, term.Coordinates{X: 8, Y: 2}))
}

func TestImageBlockFit(t *testing.T) {
	md := newImageComponent(t,
		"![a](a.png){cols=4 rows=2 maintain_aspect_ratio=false} ![b](b.png){cols=4 rows=2}",
		map[string]*fakeImage{"a.png": loaded(cells(1, 1)), "b.png": loaded(cells(1, 1))})
	w := newImageWriter(10, 3, true)
	md.Resize(10, 3)
	md.Draw(w)
	assert.Equal(t, []term.ImageFit{term.ImageFitFill, term.ImageFitContain}, w.fits)
}

func TestImageBlockDimensions(t *testing.T) {
	md := newImageComponent(t, "![a](a.png) ![b](b.png)\\\n![c](c.png)",
		map[string]*fakeImage{
			"a.png": loaded(cells(4, 2)), "b.png": loaded(cells(3, 1)),
			"c.png": loaded(cells(9, 1)),
		})
	md.Resize(12, 4)
	md.Draw(newImageWriter(12, 4, true))
	width, height := md.Dimensions()
	assert.Equal(t, 9, width, "rows broken only by the hard break")
	assert.Equal(t, 4, height)
}

// newImageComponent renders content with images, drawing the fakes in
// place of the pictures it opens by destination. Every other image is not
// shown.
func newImageComponent(
	t *testing.T, content string, fakes map[string]*fakeImage,
) *Component {
	t.Helper()
	cfg := DefaultConfig()
	cfg.ImagesConfig = ImagesConfig{
		Storage:     storagestub.NewInMemoryService(),
		Interrupter: term.NopInterrupter(),
		FS:          pictureFS{},
	}
	md, err := NewWithConfig(content, cfg)
	require.NoError(t, err)
	useFakes(md, fakes)
	return md
}

// useFakes replaces the pictures md opened with the fakes for their
// destinations.
func useFakes(md *Component, fakes map[string]*fakeImage) {
	for _, i := range md.imageBlocks {
		ib := md.blocks[i].(*imageBlock)
		for j := range ib.items {
			it := &ib.items[j]
			if it.img != nil {
				_ = it.img.Close()
				it.img = nil
			}
			if fake, ok := fakes[it.dest]; ok {
				it.img = fake
			}
		}
	}
}

// newImageWriter returns a writer whose context carries the cell size
// px10x20 when withCell is set.
func newImageWriter(width, height int, withCell bool) *imageWriter {
	w := term.NewStringWriter(width, height)
	if withCell {
		w.SetContext = cell.ContextWithPixelSize(context.Background(), px10x20)
	}
	return &imageWriter{StringWriter: w}
}

// imageWriter draws pictures as █ over the cells they cover and records
// their fit.
type imageWriter struct {
	*term.StringWriter
	fits []term.ImageFit
}

func (w *imageWriter) DrawImage(img term.Image) bool {
	w.fits = append(w.fits, img.Fit)
	r := img.Visible()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			w.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('█', 1, term.Attributes{}))
		}
	}
	return true
}

// pictureFS serves files from a map by path.
type pictureFS map[string][]byte

func (p pictureFS) OpenFile(path string, _ int, _ os.FileMode) (workspaceapi.File, error) {
	data, ok := p[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return workspace.NewMemoryFile(path, 1, 0, data, new(sync.Mutex)), nil
}

func loaded(size image.Point) *fakeImage {
	return &fakeImage{size: size, settled: true}
}

// fakeImage is a picture of a fixed size, contained in its box.
type fakeImage struct {
	size          image.Point
	settled       bool
	starts        int
	closes        int
	width, height int
}

func (f *fakeImage) Resize(width, height int) {
	f.width, f.height = width, height
}

func (f *fakeImage) Draw(w term.Writer) {
	w.DrawImage(term.Image{Width: f.width, Height: f.height, Fit: term.ImageFitContain})
}

func (f *fakeImage) Close() error {
	f.closes++
	return nil
}

func (f *fakeImage) Start() {
	f.starts++
}

func (f *fakeImage) Size() (image.Point, bool) {
	return f.size, f.settled
}
