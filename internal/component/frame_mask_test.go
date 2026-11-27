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
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// spritePixels draws art, one row of pixels a line: # is opaque, + is
// a faint antialiased pixel and . is transparent.
func spritePixels(art string) *image.NRGBA {
	rows := strings.Fields(art)
	img := image.NewNRGBA(image.Rect(0, 0, len(rows[0]), len(rows)))
	for y, r := range rows {
		for x, ch := range r {
			switch ch {
			case '#':
				img.SetNRGBA(x, y, color.NRGBA{R: 0x80, A: 0xff})
			case '+':
				img.SetNRGBA(x, y, color.NRGBA{R: 0x80, A: 0x40})
			}
		}
	}
	return img
}

// maskArt returns the pixels of mask as spritePixels takes them, x for
// a pixel the mask covers.
func maskArt(mask image.Image) string {
	var b strings.Builder
	r := mask.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		b.WriteByte('\n')
		for x := r.Min.X; x < r.Max.X; x++ {
			if _, _, _, a := mask.At(x, y).RGBA(); a == 0xffff {
				b.WriteByte('x')
			} else {
				b.WriteByte('.')
			}
		}
	}
	return b.String()
}

func TestRingSides(t *testing.T) {
	area := image.Rect(2, 1, 6, 5)
	tests := []struct {
		p    image.Point
		area image.Rectangle
		want Sides
	}{
		{p: image.Pt(2, 1), area: area, want: SideTop | SideLeft},
		{p: image.Pt(5, 1), area: area, want: SideTop | SideRight},
		{p: image.Pt(2, 4), area: area, want: SideBottom | SideLeft},
		{p: image.Pt(5, 4), area: area, want: SideBottom | SideRight},
		{p: image.Pt(3, 1), area: area, want: SideTop},
		{p: image.Pt(3, 4), area: area, want: SideBottom},
		{p: image.Pt(2, 2), area: area, want: SideLeft},
		{p: image.Pt(5, 3), area: area, want: SideRight},
		{p: image.Pt(3, 2), area: area},
		{p: image.Pt(6, 1), area: area},
		{p: image.Pt(2, 1), area: image.Rect(2, 1, 4, 5)},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, RingSides(tt.p, tt.area), "%v in %v", tt.p, tt.area)
	}
}

func TestFrameMasksInside(t *testing.T) {
	sprite := func(art string) term.Image {
		return term.Image{
			Src: spritePixels(art), ID: term.NewImageID(), Width: 1, Height: 1,
			Pos: term.Coordinates{X: 3, Y: 2}, Offset: image.Pt(1, 2),
			Clip: image.Rect(0, 0, 9, 9), VerticalRenderOffset: true,
		}
	}
	top := `
....
####
+...
....`
	tests := []struct {
		name    string
		sprite  term.Image
		outside Sides
		// want is the mask, or empty for none.
		want string
	}{
		{
			name:    "below the line of a top edge",
			sprite:  sprite(top),
			outside: SideTop,
			want: `
....
xxxx
xxxx
xxxx`,
		},
		{
			name: "above the line of a bottom edge",
			sprite: sprite(`
....
....
.###
#...`),
			outside: SideBottom,
			want: `
xxxx
xxxx
xxxx
x...`,
		},
		{
			name: "left of the line of a right edge",
			sprite: sprite(`
..#.
..#.
..#.
..#.`),
			outside: SideRight,
			want: `
xxx.
xxx.
xxx.
xxx.`,
		},
		{
			name: "inside the bend of a top-left corner",
			sprite: sprite(`
....
.###
.#..
.#..`),
			outside: SideTop | SideLeft,
			want: `
....
..xx
.xxx
.xxx`,
		},
		{
			name: "inside the bend of a bottom-right corner",
			sprite: sprite(`
..#.
..#.
###.
....`),
			outside: SideBottom | SideRight,
			want: `
xxx.
xxx.
xx..
....`,
		},
		{name: "none for a line across the other way", sprite: sprite(top), outside: SideLeft},
		{name: "none for a sprite without a line", sprite: sprite("....\n....\n....\n...."), outside: SideTop},
		{
			name: "none for a line with a gap",
			sprite: sprite(`
....
##.#
....
....`),
			outside: SideTop,
		},
		{name: "none for an opaque sprite", sprite: sprite("####\n####\n####\n####"), outside: SideTop},
		{name: "none for three sides", sprite: sprite(top), outside: SideTop | SideLeft | SideRight},
		{
			name:    "none for a sprite over more than a cell",
			sprite:  func() term.Image { s := sprite(top); s.Width = 2; return s }(),
			outside: SideTop,
		},
		{
			name:    "none for a tiled sprite",
			sprite:  func() term.Image { s := sprite(top); s.Fit = term.ImageFitTile; return s }(),
			outside: SideTop,
		},
		{
			name:    "none for a fill",
			sprite:  func() term.Image { s := sprite(top); s.Fill = true; return s }(),
			outside: SideTop,
		},
		{name: "none for a sprite without pixels", sprite: term.Image{Width: 1, Height: 1}, outside: SideTop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m FrameMasks
			got, ok := m.Inside(tt.sprite, tt.outside, term.ColorNavy)
			if tt.want == "" {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.want, maskArt(got.Src))
			want := tt.sprite
			want.Src, want.ID, want.Crop = got.Src, got.ID, image.Rectangle{}
			want.Fill, want.FillColor, want.Layer = true, term.ColorNavy, term.ImageLayerBelowText
			assert.Equal(t, want, got, "the fill goes where the sprite does, under the text")
			assert.NotEqual(t, tt.sprite.ID, got.ID)
		})
	}
}

func TestFrameMasksInsideReusesMasks(t *testing.T) {
	src := spritePixels(`
..#.
..#.
.##.
..#.`)
	sprite := term.Image{Src: src, ID: term.NewImageID(), Width: 1, Height: 1}
	var m FrameMasks
	first, ok := m.Inside(sprite, SideRight, term.ColorNavy)
	require.True(t, ok)
	again, ok := m.Inside(sprite, SideRight, term.ColorTeal)
	require.True(t, ok)
	assert.Same(t, first.Src, again.Src, "a sprite has one mask whatever its colour")
	assert.Equal(t, first.ID, again.ID)

	sprite.Version++
	changed, ok := m.Inside(sprite, SideRight, term.ColorNavy)
	require.True(t, ok)
	assert.NotEqual(t, first.ID, changed.ID, "a new version of the sprite has a new mask")

	cropped := sprite
	cropped.Crop = image.Rect(0, 0, 4, 2)
	part, ok := m.Inside(cropped, SideRight, term.ColorNavy)
	require.True(t, ok)
	assert.Equal(t, "\nxxx.\nxxx.", maskArt(part.Src), "the mask is of the crop")
}

func TestFrameMasksOutside(t *testing.T) {
	sprite := func(art string) term.Image {
		return term.Image{
			Src: spritePixels(art), ID: term.NewImageID(), Width: 1, Height: 1,
			Pos: term.Coordinates{X: 3, Y: 2},
		}
	}
	tests := []struct {
		name    string
		sprite  term.Image
		outside Sides
		// want is the mask, or empty for none.
		want string
	}{
		{
			name: "above the line of a top edge and the line, but for its inner edge",
			sprite: sprite(`
....
####
####
....`),
			outside: SideTop,
			want: `
xxxx
xxxx
....
....`,
		},
		{
			name: "outside the bend of a bottom-right corner",
			sprite: sprite(`
.#..
.#..
##..
....`),
			outside: SideBottom | SideRight,
			want: `
..xx
..xx
.xxx
xxxx`,
		},
		{
			name: "none for a line across the other way",
			sprite: sprite(`
....
####
....
....`),
			outside: SideLeft,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m FrameMasks
			got, ok := m.Outside(tt.sprite, tt.outside, term.ColorDefault)
			if tt.want == "" {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.want, maskArt(got.Src))
			want := tt.sprite
			want.Src, want.ID = got.Src, got.ID
			want.Fill, want.FillColor, want.Layer = true, term.ColorDefault, term.ImageLayerBelowText
			assert.Equal(t, want, got, "the fill goes where the sprite does, under the text")

			inside, ok := m.Inside(tt.sprite, tt.outside, term.ColorDefault)
			require.True(t, ok)
			assert.NotEqual(t, inside.ID, got.ID, "the outside has a mask of its own")
		})
	}
}

func TestFrameMasksEnclosed(t *testing.T) {
	sprite := func(art string) term.Image {
		return term.Image{
			Src: spritePixels(art), ID: term.NewImageID(), Width: 1, Height: 1,
			Pos: term.Coordinates{X: 3, Y: 2}, Offset: image.Pt(1, 2),
			Clip: image.Rect(0, 0, 9, 9), VerticalRenderOffset: true,
		}
	}
	channel := `
####
....
++++
....`
	tests := []struct {
		name    string
		sprite  term.Image
		outside Sides
		// want is the mask, or empty for none.
		want string
	}{
		{
			name:    "the channel between a face and a shadow",
			sprite:  sprite(channel),
			outside: SideBottom,
			want: `
xxxx
xxxx
xxxx
....`,
		},
		{
			name: "the side of a line away from the outside",
			sprite: sprite(`
....
....
####
....`),
			outside: SideBottom,
			want: `
xxxx
xxxx
xxxx
....`,
		},
		{name: "none for a channel open to an outside side", sprite: sprite(channel), outside: SideBottom | SideLeft},
		{name: "none for a sprite without a line", sprite: sprite("....\n....\n....\n...."), outside: SideBottom},
		{name: "none for an opaque sprite", sprite: sprite("####\n####\n####\n####"), outside: SideBottom},
		{name: "none off the ring", sprite: sprite(channel)},
		{
			name:    "none for a sprite over more than a cell",
			sprite:  func() term.Image { s := sprite(channel); s.Width = 2; return s }(),
			outside: SideBottom,
		},
		{
			name:    "none for a fill",
			sprite:  func() term.Image { s := sprite(channel); s.Fill = true; return s }(),
			outside: SideBottom,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m FrameMasks
			// The inside of the same sprite must not stand in for it.
			m.Inside(tt.sprite, tt.outside, term.ColorNavy)
			got, ok := m.Enclosed(tt.sprite, tt.outside, term.ColorYellow)
			if tt.want == "" {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tt.want, maskArt(got.Src))
			want := tt.sprite
			want.Src, want.ID, want.Crop = got.Src, got.ID, image.Rectangle{}
			want.Fill, want.FillColor, want.Layer = true, term.ColorYellow, term.ImageLayerBelowText
			assert.Equal(t, want, got, "the fill goes where the sprite does, under the text")
		})
	}
}

// imageRecorder is a string writer that records the images drawn into
// it, or reports that it cannot draw them when noImages is set.
type imageRecorder struct {
	*term.StringWriter
	images   []term.Image
	noImages bool
}

func (w *imageRecorder) DrawImage(img term.Image) bool {
	if w.noImages {
		return false
	}
	w.images = append(w.images, img)
	return true
}

func TestFrameInsideWriter(t *testing.T) {
	line := spritePixels("....\n####\n....\n....")
	sprite := func(x, y int, layer term.ImageLayer) term.Image {
		return term.Image{
			Src: line, ID: term.NewImageID(), Width: 1, Height: 1,
			Pos: term.Coordinates{X: x, Y: y}, Layer: layer,
		}
	}
	tests := []struct {
		name     string
		img      term.Image
		noImages bool
		enclosed bool
		// fill reports that a fill follows the image.
		fill bool
	}{
		{name: "a sprite on the top of the frame", img: sprite(3, 1, term.ImageLayerAboveText), fill: true},
		{name: "the clear pixels a sprite encloses", img: sprite(3, 1, term.ImageLayerAboveText), enclosed: true, fill: true},
		{name: "a sprite inside the frame", img: sprite(3, 2, term.ImageLayerAboveText)},
		{name: "a sprite outside the frame", img: sprite(3, 0, term.ImageLayerAboveText)},
		{name: "a sprite under the text", img: sprite(3, 1, term.ImageLayerBelowText)},
		{name: "a sprite whose line does not part the cell", img: sprite(1, 2, term.ImageLayerAboveText)},
		{name: "a writer that cannot draw it", img: sprite(3, 1, term.ImageLayerAboveText), noImages: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &imageRecorder{StringWriter: term.NewStringWriter(8, 6), noImages: tt.noImages}
			var masks FrameMasks
			w := FrameInsideWriter{
				Writer: rec, Area: image.Rect(1, 1, 7, 5), Inside: term.ColorNavy, Masks: &masks,
				Enclosed: tt.enclosed,
			}
			assert.Equal(t, !tt.noImages, w.DrawImage(tt.img))
			if tt.noImages {
				assert.Empty(t, rec.images)
				return
			}
			require.NotEmpty(t, rec.images)
			assert.Equal(t, tt.img, rec.images[0])
			if !tt.fill {
				assert.Len(t, rec.images, 1)
				return
			}
			require.Len(t, rec.images, 2)
			fill := masks.Inside
			if tt.enclosed {
				fill = masks.Enclosed
			}
			want, ok := fill(tt.img, SideTop, term.ColorNavy)
			require.True(t, ok)
			assert.Equal(t, want, rec.images[1])
		})
	}
}
