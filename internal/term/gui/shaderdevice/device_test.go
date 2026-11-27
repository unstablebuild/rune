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

package shaderdevice

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
	"unstable.build/rune/internal/component/gshader"
)

const generatorSrc = `//kage:unit pixels

package main

var Progress float
var Resolution vec2
var Tint vec4

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	return Tint * Progress * dstPos.x / Resolution.x
}
`

const blendSrc = `//kage:unit pixels

package main

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	return imageSrc0At(srcPos) + imageSrc1At(srcPos)
}
`

var (
	px      = cell.PixelSize{Width: 10, Height: 20}
	black   = Theme{Background: color.RGBA{A: 0xff}, Foreground: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}}
	white   = Theme{Background: color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}, Foreground: color.RGBA{A: 0xff}}
	area    = image.Pt(8, 4)
	gen     = gshader.MustProgram("generator.kage", []byte(generatorSrc))
	blend   = gshader.MustProgram("blend.kage", []byte(blendSrc))
	tintRed = gshader.Uniforms{"Tint": term.ColorRed, "Progress": float32(1)}
)

// frame runs fn between a Begin and End of d, in a benchdraw frame.
func frame(t testing.TB, d *Device, px cell.PixelSize, theme Theme, fn func()) {
	t.Helper()
	benchdraw.BeginFrame(t)
	defer benchdraw.EndFrame(t)
	d.Begin(px, theme)
	defer d.End()
	fn()
}

func newDevice(t *testing.T) *Device {
	d := New(nil)
	t.Cleanup(d.Close)
	return d
}

func render(t *testing.T, d *Device, key term.ImageID, size image.Point, n gshader.Node) gshader.Frame {
	t.Helper()
	f, err := d.Render(key, size, nil, n)
	require.NoError(t, err)
	return f
}

func TestDeviceReusesEqualTrees(t *testing.T) {
	d := newDevice(t)
	key := term.NewImageID()
	var first gshader.Frame
	frame(t, d, px, black, func() {
		first = render(t, d, key, area, gshader.Run(gen, tintRed))
		assert.Equal(t, image.Rect(0, 0, 80, 80), first.Src.Bounds())
		assert.Equal(t, uint64(1), first.Version)
		again := render(t, d, key, area, gshader.Run(gen, gshader.Uniforms{"Tint": term.ColorRed, "Progress": float32(1)}))
		assert.Equal(t, first, again, "an Equal tree returns the same frame")
	})
	assert.Equal(t, stats{compiles: 1, renders: 1, passes: 1}, d.stats)

	frame(t, d, px, black, func() {
		assert.Equal(t, first, render(t, d, key, area, gshader.Run(gen, tintRed)),
			"and does no GPU work in later draws")
		next := render(t, d, key, area, gshader.Run(gen, gshader.Uniforms{"Tint": term.ColorBlue}))
		assert.Equal(t, first.ID, next.ID, "the same target")
		assert.Equal(t, uint64(2), next.Version, "a new version")
	})
	assert.Equal(t, stats{compiles: 1, renders: 2, passes: 2}, d.stats, "one compile per program")
}

func TestDeviceRerenders(t *testing.T) {
	tree := gshader.Run(gen, tintRed)
	tests := []struct {
		name      string
		px        cell.PixelSize
		theme     Theme
		size      image.Point
		key       bool
		invalid   bool
		want      int
		wantNewID bool
	}{
		{name: "nothing changed", px: px, theme: black, size: area, want: 1},
		{name: "the theme changed", px: px, theme: white, size: area, want: 2},
		{name: "invalidated", px: px, theme: black, size: area, invalid: true, want: 2},
		{name: "the pixel size changed", px: cell.PixelSize{Width: 8, Height: 16}, theme: black, size: area, want: 2, wantNewID: true},
		{name: "the size changed", px: px, theme: black, size: image.Pt(9, 4), want: 2, wantNewID: true},
		{name: "another key", px: px, theme: black, size: area, key: true, want: 2, wantNewID: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDevice(t)
			key := term.NewImageID()
			var first gshader.Frame
			frame(t, d, px, black, func() { first = render(t, d, key, area, tree) })
			if tt.invalid {
				d.Invalidate()
			}
			if tt.key {
				key = term.NewImageID()
			}
			var second gshader.Frame
			frame(t, d, tt.px, tt.theme, func() { second = render(t, d, key, tt.size, tree) })
			assert.Equal(t, tt.want, d.stats.renders)
			assert.Equal(t, tt.wantNewID, first.ID != second.ID)
		})
	}
}

func TestDeviceEndFreesWhatADrawDidNotUse(t *testing.T) {
	d := newDevice(t)
	kept, dropped := term.NewImageID(), term.NewImageID()
	other := gshader.MustProgram("other.kage", []byte(generatorSrc))
	pic := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var keptFrame, droppedFrame gshader.Frame
	frame(t, d, px, black, func() {
		keptFrame = render(t, d, kept, area, gshader.Run(gen, tintRed))
		droppedFrame = render(t, d, dropped, area, gshader.Run(other, nil, gshader.Picture(pic, 1, 1, term.ImageFitFill)))
	})
	require.Len(t, d.targets, 2)
	require.Len(t, d.programs, 2)
	require.Len(t, d.pictures, 1)

	frame(t, d, px, black, func() { render(t, d, kept, area, gshader.Run(gen, tintRed)) })
	assert.Len(t, d.targets, 1)
	assert.Len(t, d.programs, 1, "a cached rendering keeps its program")
	assert.Contains(t, d.programs, gen)
	assert.Empty(t, d.pictures)
	_, ok := Texture(keptFrame.Src)
	assert.True(t, ok)
	_, ok = Texture(droppedFrame.Src)
	assert.False(t, ok, "a freed rendering has no texture")
	assert.Equal(t, color.RGBA{}, droppedFrame.Src.At(0, 0), "and reads as transparent")

	frame(t, d, px, black, func() {})
	assert.Empty(t, d.targets)
	assert.Empty(t, d.programs)
	assert.Empty(t, d.scratch, "unused scratch textures are freed")
}

func TestDeviceErrors(t *testing.T) {
	bad := gshader.MustProgram("bad.kage", []byte("//kage:unit pixels\n\npackage main\n\n"+
		"func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\treturn undefined\n}\n"))
	tests := []struct {
		name    string
		size    image.Point
		src     *gshader.Snapshot
		tree    gshader.Node
		wantErr string
		wantIs  error
	}{
		{name: "a uniform of the wrong type", size: area, tree: gshader.Run(gen, gshader.Uniforms{"Tint": float32(1)}),
			wantErr: "generator.kage: uniform Tint: a vec4 takes 4 values, got 1"},
		{name: "a uniform deep down", size: area, tree: gshader.Run(blend, nil, gshader.Source(), gshader.Run(gen, gshader.Uniforms{"Resolution": term.ColorRed})),
			wantErr: "a vec2 cannot be set to a colour"},
		{name: "a compile error", size: area, tree: gshader.Run(bad, nil), wantErr: "bad.kage: "},
		{name: "an empty area", size: image.Pt(0, 4), tree: gshader.Source(), wantErr: "cannot render 0x80 pixels"},
		{name: "a huge area", size: image.Pt(2000, 1), tree: gshader.Source(), wantErr: "20000x20 pixels is larger than 16384x16384"},
		{name: "an empty crop", size: area, tree: gshader.Crop(gshader.Source(), image.Rect(2, 2, 2, 3)), wantErr: "cannot render 0x20 pixels"},
		{name: "a source with nothing to render it", size: area, src: &gshader.Snapshot{}, tree: gshader.Source(), wantIs: gshader.ErrUnsupported},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDevice(t)
			frame(t, d, px, black, func() {
				var err error
				require.NotPanics(t, func() { _, err = d.Render(1, tt.size, tt.src, tt.tree) })
				if tt.wantIs != nil {
					require.ErrorIs(t, err, tt.wantIs)
				} else {
					require.ErrorContains(t, err, tt.wantErr)
				}
			})
		})
	}
}

func TestDeviceCachesCompileErrors(t *testing.T) {
	bad := gshader.MustProgram("bad.kage", []byte("//kage:unit pixels\n\npackage main\n\n"+
		"func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\treturn 1\n}\n"))
	d := newDevice(t)
	for range 3 {
		frame(t, d, px, black, func() {
			_, err := d.Render(1, area, nil, gshader.Run(bad, nil))
			require.ErrorContains(t, err, "bad.kage")
		})
	}
	assert.Equal(t, 1, d.stats.compiles)
}

func TestDeviceRendersSnapshots(t *testing.T) {
	cells := func(ch rune) [][]term.Cell {
		return [][]term.Cell{{{Ch: ch}, {Ch: 'b'}}, {{Ch: 'c'}, {Ch: 'd'}}}
	}
	pic := image.NewRGBA(image.Rect(0, 0, 1, 1))
	placed := func(version uint64, src image.Image) []term.Image {
		return []term.Image{{Src: src, ID: 7, Version: version, Width: 1, Height: 1}}
	}
	filter := gshader.Run(blend, nil, gshader.Source(), gshader.Source())
	type step struct {
		src         *gshader.Snapshot
		tree        gshader.Node
		wantSources int
		wantRenders int
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{
			name:  "rendered once for every source a tree reads",
			steps: []step{{src: &gshader.Snapshot{Cells: cells('a')}, tree: filter, wantSources: 1, wantRenders: 1}},
		},
		{
			name:  "not rendered for a tree that does not read it",
			steps: []step{{src: &gshader.Snapshot{Cells: cells('a')}, tree: gshader.Run(gen, tintRed), wantRenders: 1}},
		},
		{
			name: "equal cells and pictures reuse the rendering",
			steps: []step{
				{src: &gshader.Snapshot{Cells: cells('a'), Images: placed(1, pic)}, tree: filter, wantSources: 1, wantRenders: 1},
				{src: &gshader.Snapshot{Cells: cells('a'), Images: placed(1, image.NewRGBA(pic.Rect))}, tree: filter, wantSources: 1, wantRenders: 1},
			},
		},
		{
			name: "changed cells render again",
			steps: []step{
				{src: &gshader.Snapshot{Cells: cells('a')}, tree: filter, wantSources: 1, wantRenders: 1},
				{src: &gshader.Snapshot{Cells: cells('z')}, tree: filter, wantSources: 2, wantRenders: 2},
			},
		},
		{
			name: "a changed picture renders again",
			steps: []step{
				{src: &gshader.Snapshot{Cells: cells('a'), Images: placed(1, pic)}, tree: filter, wantSources: 1, wantRenders: 1},
				{src: &gshader.Snapshot{Cells: cells('a'), Images: placed(2, pic)}, tree: filter, wantSources: 2, wantRenders: 2},
			},
		},
		{
			name: "losing the snapshot renders again",
			steps: []step{
				{src: &gshader.Snapshot{Cells: cells('a')}, tree: filter, wantSources: 1, wantRenders: 1},
				{tree: filter, wantSources: 1, wantRenders: 2},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := New(func(dst *ebiten.Image, _ [][]term.Cell, _ []term.Image) {
				assert.Equal(t, image.Rect(0, 0, 20, 40), dst.Bounds(), "the size of the cells")
			})
			t.Cleanup(d.Close)
			for i, s := range tt.steps {
				frame(t, d, px, black, func() {
					_, err := d.Render(1, image.Pt(2, 2), s.src, s.tree)
					require.NoError(t, err)
				})
				assert.Equal(t, s.wantSources, d.stats.sources, "step %d: sources", i)
				assert.Equal(t, s.wantRenders, d.stats.renders, "step %d: renders", i)
			}
		})
	}
}

func TestDeviceKeepsACopyOfTheSnapshot(t *testing.T) {
	d := New(func(*ebiten.Image, [][]term.Cell, []term.Image) {})
	t.Cleanup(d.Close)
	filter := gshader.Run(blend, nil, gshader.Source(), gshader.Source())
	cells := [][]term.Cell{{{Ch: 'a'}}}
	frame(t, d, px, black, func() {
		render := func() {
			_, err := d.Render(1, image.Pt(1, 1), &gshader.Snapshot{Cells: cells}, filter)
			require.NoError(t, err)
		}
		render()
		cells[0][0].Ch = 'b'
		render()
	})
	assert.Equal(t, 2, d.stats.sources, "a writer reusing its cells changes them under the device")
}

func TestDeviceRenderOutsideADraw(t *testing.T) {
	d := newDevice(t)
	_, err := d.Render(1, area, nil, gshader.Source())
	require.ErrorIs(t, err, errOutsideDraw)
	frame(t, d, px, black, func() {})
	_, err = d.Render(1, area, nil, gshader.Source())
	require.ErrorIs(t, err, errOutsideDraw, "nor after End")
}

func TestDeviceEvaluatesEveryNode(t *testing.T) {
	pic := image.NewRGBA(image.Rect(0, 0, 3, 5))
	small := image.Rect(1, 1, 4, 3)
	tests := []struct {
		name       string
		tree       gshader.Node
		wantPasses int
	}{
		{name: "a transparent source", tree: gshader.Source()},
		{name: "a solid", tree: gshader.Solid(term.ColorRed)},
		{name: "a filled picture", tree: gshader.Picture(pic, 1, 1, term.ImageFitFill)},
		{name: "a contained picture", tree: gshader.Picture(pic, 1, 1, term.ImageFitContain)},
		{name: "a tiled picture", tree: gshader.Picture(pic, 1, 1, term.ImageFitTile)},
		{name: "a picture without pixels", tree: gshader.Picture(nil, 1, 1, term.ImageFitFill)},
		{name: "a crop scaled to the area", tree: gshader.Crop(gshader.Solid(term.ColorRed), small)},
		{name: "a crop partly outside", tree: gshader.Crop(gshader.Source(), image.Rect(6, 2, 10, 6))},
		{name: "a place", tree: gshader.Place(gshader.Solid(term.ColorRed), gshader.Source(), small)},
		{name: "a place outside", tree: gshader.Place(gshader.Solid(term.ColorRed), gshader.Source(), image.Rect(20, 20, 30, 30))},
		{
			name:       "inputs of another size are scaled to the first",
			tree:       gshader.Run(blend, nil, gshader.Crop(gshader.Source(), small), gshader.Source()),
			wantPasses: 1,
		},
		{
			name:       "nested runs",
			tree:       gshader.Run(blend, nil, gshader.Run(gen, tintRed), gshader.Run(gen, tintRed, gshader.Crop(gshader.Source(), small))),
			wantPasses: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDevice(t)
			frame(t, d, px, black, func() {
				var f gshader.Frame
				require.NotPanics(t, func() { f = render(t, d, 1, area, tt.tree) })
				assert.Equal(t, image.Rect(0, 0, 80, 80), f.Src.Bounds(), "a root of another size is scaled to the area")
			})
			assert.Equal(t, tt.wantPasses, d.stats.passes)
		})
	}
}

func TestDeviceUploadsPicturesOnce(t *testing.T) {
	d := newDevice(t)
	pic := image.NewNRGBA(image.Rect(0, 0, 4, 4))
	id := term.NewImageID()
	frame(t, d, px, black, func() {
		render(t, d, 1, area, gshader.Picture(pic, id, 1, term.ImageFitFill))
		render(t, d, 2, area, gshader.Picture(pic, id, 1, term.ImageFitContain))
	})
	assert.Equal(t, 1, d.stats.uploads)
	frame(t, d, px, black, func() {
		render(t, d, 1, area, gshader.Picture(pic, id, 2, term.ImageFitFill))
	})
	assert.Equal(t, 2, d.stats.uploads, "a new version is uploaded")

	var gpu gshader.Frame
	frame(t, d, px, black, func() {
		gpu = render(t, d, 3, area, gshader.Solid(term.ColorRed))
		render(t, d, 4, area, gshader.Picture(gpu.Src, gpu.ID, gpu.Version, term.ImageFitFill))
	})
	assert.Equal(t, 2, d.stats.uploads, "a rendering is drawn from the GPU")
}

func TestDeviceDefaults(t *testing.T) {
	d := newDevice(t)
	d.Begin(px, black)
	d.End()
	assert.Equal(t, gshader.Defaults{
		Resolution: [2]float32{80, 60}, CellSize: [2]float32{10, 20},
		Background: black.Background, Foreground: black.Foreground,
	}, d.defaults(image.Pt(80, 60)))
}

func TestDeviceResolve(t *testing.T) {
	d := newDevice(t)
	d.Begin(px, black)
	d.End()
	assert.Equal(t, black.Background, d.resolve(term.ColorDefault))
	assert.Equal(t, color.RGBA{R: 0x12, G: 0x34, B: 0x56, A: 0xff}, d.resolve(term.NewHexColor(0x123456)))

	themed := black
	themed.Resolve = func(term.Color) color.RGBA { return color.RGBA{G: 1} }
	d.Begin(px, themed)
	d.End()
	assert.Equal(t, color.RGBA{G: 1}, d.resolve(term.ColorRed))
}

func TestContainRect(t *testing.T) {
	tests := []struct {
		name      string
		src, area image.Rectangle
		want      image.Rectangle
	}{
		{name: "same aspect", src: image.Rect(0, 0, 10, 10), area: image.Rect(0, 0, 100, 100), want: image.Rect(0, 0, 100, 100)},
		{name: "wide", src: image.Rect(0, 0, 20, 10), area: image.Rect(0, 0, 100, 100), want: image.Rect(0, 25, 100, 75)},
		{name: "tall", src: image.Rect(0, 0, 10, 20), area: image.Rect(10, 0, 110, 100), want: image.Rect(35, 0, 85, 100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, containRect(tt.src, tt.area))
		})
	}
}

func TestCompile(t *testing.T) {
	require.NoError(t, Compile([]byte(generatorSrc)))
	require.Error(t, Compile([]byte("//kage:unit pixels\n\npackage main\n\nfunc Fragment() {}")))
}
