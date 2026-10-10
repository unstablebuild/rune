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

package cell

import (
	"context"
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestWriterContext(t *testing.T) {
	type myKey string

	writer := NewBufferWriter(
		context.WithValue(context.Background(), myKey("a"), "a"), 10, 10)

	value := writer.Context().Value(myKey("a"))
	require.NotNil(t, value)
	str, ok := value.(string)
	require.True(t, ok)
	assert.Equal(t, "a", str)

	writer.SetContext(
		context.WithValue(context.Background(), myKey("b"), "b"),
	)

	value = writer.Context().Value(myKey("a"))
	require.Nil(t, value)

	value = writer.Context().Value(myKey("b"))
	require.NotNil(t, value)
	str, ok = value.(string)
	require.True(t, ok)
	assert.Equal(t, "b", str)
}

func TestWriteFlush(t *testing.T) {
	width, height := 5, 6
	writer := NewBufferWriter(context.Background(), width, height)

	c := 'E'
	for i := width - 1; i >= 0; i-- {
		for j := height - 1; j >= 0; j-- {
			if i > j-1 {
				writer.SetCell(term.Coordinates{X: j, Y: i}, term.Cell{Ch: c})
			}
		}
		c--
	}

	// should be fine to wtry to write out of bounds
	writer.SetCell(term.Coordinates{X: width + 1, Y: height + 1}, term.Cell{Ch: '='})

	require.NoError(t, writer.Flush())

	expected := "A\x00\x00\x00\x00\nBB\x00\x00\x00\n" +
		"CCC\x00\x00\nDDDD\x00\nEEEEE\n\x00\x00\x00\x00\x00"
	assert.Equal(t, expected, term.CellsToString(writer.RawCells()))
}

func TestBufferWriterSetCellOutOfBounds(t *testing.T) {
	const width, height = 3, 2
	for _, pos := range []term.Coordinates{
		{X: -1}, {Y: -1}, {X: width}, {Y: height}, {X: width, Y: height}, {X: -1, Y: -1},
	} {
		writer := NewBufferWriter(context.Background(), width, height)
		assert.NotPanics(t, func() { writer.SetCell(pos, term.Cell{Ch: 'x'}) }, "%v", pos)
		assert.Equal(t, "\x00\x00\x00\n\x00\x00\x00", term.CellsToString(writer.RawCells()), "%v", pos)
	}
}

func TestBufferWriterClear(t *testing.T) {
	tests := []struct {
		name string
		attr term.Attributes
	}{
		{name: "default attributes"},
		{name: "colours", attr: term.Attributes{Fg: term.ColorRed, Bg: term.ColorBlue, Attrs: term.AttrBold}},
		{name: "underline colour", attr: term.Attributes{Attrs: term.AttrUnderline, Underline: term.ColorGreen}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const width, height = 7, 3
			writer := NewBufferWriter(context.Background(), width, height)
			for y := range height {
				for x := range width {
					writer.SetCell(term.Coordinates{X: x, Y: y}, term.NewCell('漢', 2, term.Attributes{Fg: term.ColorRed}))
				}
			}
			require.NoError(t, writer.Clear(tt.attr))
			want := term.NewCell(0, 0, tt.attr)
			for y, row := range writer.RawCells() {
				require.Len(t, row, width)
				for x, c := range row {
					assert.Equal(t, want, c, "(%d,%d)", x, y)
				}
			}
		})
	}
}

func TestBufferWriterDrawImage(t *testing.T) {
	tests := []struct {
		name    string
		img     term.Image
		wantLen int
		wantVis image.Rectangle
	}{
		{
			name:    "inside the grid is kept unclipped",
			img:     term.Image{Pos: term.Coordinates{X: 1, Y: 1}, Width: 3, Height: 2},
			wantLen: 1,
			wantVis: image.Rect(1, 1, 4, 3),
		},
		{
			name:    "overflowing the grid is clipped to it",
			img:     term.Image{Pos: term.Coordinates{X: 8, Y: 3}, Width: 5, Height: 5},
			wantLen: 1,
			wantVis: image.Rect(8, 3, 10, 5),
		},
		{
			name:    "fully outside the grid is dropped",
			img:     term.Image{Pos: term.Coordinates{X: 10, Y: 0}, Width: 2, Height: 2},
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewBufferWriter(context.Background(), 10, 5)
			assert.True(t, w.DrawImage(tt.img))
			require.Len(t, w.Images(), tt.wantLen)
			if tt.wantLen > 0 {
				assert.Equal(t, tt.wantVis, w.Images()[0].Visible())
			}
		})
	}
}

func TestBufferWriterClearDropsPlacements(t *testing.T) {
	w := NewBufferWriter(context.Background(), 10, 5)
	src := image.NewRGBA(image.Rect(0, 0, 2, 2))
	require.True(t, w.DrawImage(term.Image{
		Src: src, ID: 1, Width: 2, Height: 2,
	}))
	require.Len(t, w.Images(), 1)

	released := w.Images()[:1]
	require.NoError(t, w.Clear(term.Attributes{}))
	assert.Empty(t, w.Images())
	assert.Nil(t, released[0].Src, "Clear must release the pixel reference")

	require.True(t, w.DrawImage(term.Image{ID: 2, Width: 1, Height: 1}))
	require.Len(t, w.Images(), 1)
	assert.Equal(t, term.ImageID(2), w.Images()[0].ID)
}

func TestBufferWriterImagesCutToUncoveredCells(t *testing.T) {
	const width, height = 12, 4
	grid := image.Rect(0, 0, width, height)
	placement := image.Rect(2, 0, 10, 3)
	window := image.Rect(4, 1, 7, 3)
	dot := term.NewCell('.', 1, term.Attributes{})
	ex := term.NewCell('x', 1, term.Attributes{})
	han := term.NewCell('漢', 2, term.Attributes{})
	place := func(w term.Writer, r image.Rectangle) {
		w.DrawImage(term.Image{
			ID: term.NewImageID(), Pos: term.Coordinates{X: r.Min.X, Y: r.Min.Y},
			Width: r.Dx(), Height: r.Dy(),
		})
	}
	placed := func(w term.Writer) {
		fillRect(w, grid, dot)
		place(w, placement)
	}

	tests := []struct {
		name string
		draw func(w term.Writer)
		want []image.Rectangle
	}{
		{
			name: "nothing written after the placement",
			draw: placed,
			want: []image.Rectangle{placement},
		},
		{
			name: "window inside the placement",
			draw: func(w term.Writer) {
				placed(w)
				fillRect(w, window, ex)
			},
			want: []image.Rectangle{
				image.Rect(2, 0, 10, 1),
				image.Rect(2, 1, 4, 3),
				image.Rect(7, 1, 10, 3),
			},
		},
		{
			name: "window over the whole placement",
			draw: func(w term.Writer) {
				placed(w)
				fillRect(w, grid, ex)
			},
		},
		{
			name: "cells identical to the ones below still cover",
			draw: func(w term.Writer) {
				placed(w)
				fillRect(w, window, dot)
			},
			want: []image.Rectangle{
				image.Rect(2, 0, 10, 1),
				image.Rect(2, 1, 4, 3),
				image.Rect(7, 1, 10, 3),
			},
		},
		{
			name: "attributes alone leave the placement showing",
			draw: func(w term.Writer) {
				placed(w)
				for y := window.Min.Y; y < window.Max.Y; y++ {
					for x := window.Min.X; x < window.Max.X; x++ {
						w.UnionAttributes(term.Coordinates{X: x, Y: y},
							term.Attributes{Attrs: term.AttrReverse})
					}
				}
			},
			want: []image.Rectangle{placement},
		},
		{
			name: "cells written before the placement stay under it",
			draw: func(w term.Writer) {
				fillRect(w, grid, dot)
				fillRect(w, window, ex)
				place(w, placement)
			},
			want: []image.Rectangle{placement},
		},
		{
			name: "wide cell over the placement covers the column its glyph spills into",
			draw: func(w term.Writer) {
				placed(w)
				w.SetCell(term.Coordinates{X: 4, Y: 1}, han)
			},
			want: []image.Rectangle{
				image.Rect(2, 0, 10, 1),
				image.Rect(2, 1, 4, 2),
				image.Rect(6, 1, 10, 2),
				image.Rect(2, 2, 10, 3),
			},
		},
		{
			name: "wide cell left of the placement spills into its first column",
			draw: func(w term.Writer) {
				placed(w)
				w.SetCell(term.Coordinates{X: 1, Y: 1}, han)
			},
			want: []image.Rectangle{
				image.Rect(2, 0, 10, 1),
				image.Rect(3, 1, 10, 2),
				image.Rect(2, 2, 10, 3),
			},
		},
		{
			name: "later placement leaves an earlier one whole",
			draw: func(w term.Writer) {
				placed(w)
				place(w, window)
			},
			want: []image.Rectangle{placement, window},
		},
		{
			name: "placement cut by the buffer's edge",
			draw: func(w term.Writer) {
				fillRect(w, grid, dot)
				place(w, image.Rect(10, 2, 14, 5))
				w.SetCell(term.Coordinates{X: 11, Y: 3}, ex)
			},
			want: []image.Rectangle{
				image.Rect(10, 2, 12, 3),
				image.Rect(10, 3, 11, 4),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewBufferWriter(context.Background(), width, height)
			tt.draw(w)
			var got []image.Rectangle
			for _, img := range w.Images() {
				got = append(got, img.Visible())
			}
			assert.Equal(t, tt.want, got)

			plain := cellsOnly{NewBufferWriter(context.Background(), width, height)}
			tt.draw(plain)
			assert.Equal(t, plain.RawCells(), w.RawCells(),
				"cells read back as written")
		})
	}
}

// cellsOnly drops placements, to read back the cells as they are
// without any.
type cellsOnly struct{ *BufferWriter }

func (cellsOnly) DrawImage(term.Image) bool { return false }

func TestBufferWriterImagesMovedByPixels(t *testing.T) {
	const cols, rows = 16, 10
	const cw, ch = 10, 20
	size := PixelSize{Width: cw, Height: ch}
	ex := term.NewCell('x', 1, term.Attributes{})
	box := image.Rect(4, 3, 8, 5)
	clip := image.Rect(0, 0, cols, 5)
	tests := []struct {
		name string
		// cells are the cells the placement is drawn on, box when empty.
		cells  image.Rectangle
		offset image.Point
		clip   image.Rectangle
		// over are the cells written over the placement.
		over []term.Coordinates
		// sized reports whether the writer knows the cell size.
		unsized bool
		// want are the cells each piece ends up clipped to.
		want []image.Rectangle
	}{
		{
			name:   "right by a few pixels reaches into the next column",
			offset: image.Pt(3, 0),
			want:   []image.Rectangle{image.Rect(4, 3, 9, 5)},
		},
		{
			name:   "left and up by a few pixels",
			offset: image.Pt(-5, -7),
			want:   []image.Rectangle{image.Rect(3, 2, 8, 5)},
		},
		{
			name:   "by whole cells",
			offset: image.Pt(3*cw, -2*ch),
			want:   []image.Rectangle{image.Rect(7, 1, 11, 3)},
		},
		{
			name:   "onto the grid from cells outside it",
			cells:  image.Rect(-10, 3, -6, 5),
			offset: image.Pt(14*cw+1, 0),
			want:   []image.Rectangle{image.Rect(4, 3, 9, 5)},
		},
		{
			name:   "off the grid",
			offset: image.Pt((cols-4)*cw, 0),
		},
		{
			name:   "past the bottom of the grid",
			offset: image.Pt(0, (rows-4)*ch+5),
			want:   []image.Rectangle{image.Rect(4, 9, 8, 10)},
		},
		{
			name:   "confined to its clip on the cells it lands on",
			offset: image.Pt(0, ch+ch/2),
			clip:   clip,
			want:   []image.Rectangle{image.Rect(4, 4, 8, 5)},
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
			want:   []image.Rectangle{image.Rect(4, 3, 8, 5)},
		},
		{
			name:   "cut by a cell written over a cell it lands on",
			offset: image.Pt(2*cw, 0),
			over:   []term.Coordinates{{X: 9, Y: 3}},
			want:   []image.Rectangle{image.Rect(6, 3, 9, 4), image.Rect(6, 4, 10, 5)},
		},
		{
			name:   "cut by a written cell it reaches a pixel into",
			offset: image.Pt(2*cw+1, 0),
			over:   []term.Coordinates{{X: 10, Y: 4}},
			want:   []image.Rectangle{image.Rect(6, 3, 11, 4), image.Rect(6, 4, 10, 5)},
		},
		{
			name:   "not cut by a cell written over a cell it moved off",
			offset: image.Pt(2*cw, 0),
			over:   []term.Coordinates{{X: 4, Y: 3}},
			want:   []image.Rectangle{image.Rect(6, 3, 10, 5)},
		},
		{
			name:    "without the cell size it stays on its own cells",
			offset:  image.Pt(2*cw, 0),
			over:    []term.Coordinates{{X: 4, Y: 3}},
			unsized: true,
			want:    []image.Rectangle{image.Rect(5, 3, 8, 4), image.Rect(4, 4, 8, 5)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cells := tt.cells
			if cells.Empty() {
				cells = box
			}
			ctx := context.Background()
			if !tt.unsized {
				ctx = ContextWithPixelSize(ctx, size)
			}
			w := NewBufferWriter(ctx, cols, rows)
			require.True(t, w.DrawImage(term.Image{
				ID:    term.NewImageID(),
				Pos:   term.Coordinates{X: cells.Min.X, Y: cells.Min.Y},
				Width: cells.Dx(), Height: cells.Dy(), Clip: tt.clip, Offset: tt.offset,
			}))
			for _, pos := range tt.over {
				w.SetCell(pos, ex)
			}
			var got []image.Rectangle
			for _, img := range w.Images() {
				assert.Equal(t, tt.offset, img.Offset, "the pieces keep the offset")
				got = append(got, img.Clip)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

func fillRect(w term.Writer, r image.Rectangle, c term.Cell) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			w.SetCell(term.Coordinates{X: x, Y: y}, c)
		}
	}
}

func benchBufferWriter(b *testing.B, n int) {
	width, height := n, n
	writer := NewBufferWriter(context.Background(), width, height)
	for i := 0; i < b.N; i++ {
		writer.Clear(term.Attributes{})
		c := 'E'
		for i := width - 1; i >= 0; i-- {
			for j := height - 1; j >= 0; j-- {
				if i > j-1 {
					writer.SetCell(term.Coordinates{X: j, Y: i}, term.Cell{Ch: c})
				}
			}
			c--
		}
	}
}

func BenchmarkBufferWriter10(b *testing.B) {
	benchBufferWriter(b, 10)
}
func BenchmarkBufferWriter100(b *testing.B) {
	benchBufferWriter(b, 100)
}
func BenchmarkBufferWriter1000(b *testing.B) {
	benchBufferWriter(b, 1000)
}

// BenchmarkBufferWriterFrame measures the writer as the GUI drives it
// every frame: Clear with the theme's attributes, then a handler setting
// every cell through term.Writer.
func BenchmarkBufferWriterFrame(b *testing.B) {
	themed := term.Attributes{Fg: term.NewColor(200, 200, 200), Bg: term.NewColor(30, 30, 30)}
	underlined := term.Attributes{Attrs: term.AttrUnderline, Underline: term.ColorGreen}
	glyph := term.NewCell('x', 1, themed)
	sizes := []struct {
		name          string
		width, height int
	}{
		{name: "1080p", width: 213, height: 60},
		{name: "2160p", width: 426, height: 120},
	}
	for _, size := range sizes {
		run := func(name string, frame func(w *BufferWriter)) {
			b.Run(size.name+"/"+name, func(b *testing.B) {
				w := NewBufferWriter(context.Background(), size.width, size.height)
				b.ReportAllocs()
				for b.Loop() {
					frame(w)
				}
			})
		}
		run("clear", func(w *BufferWriter) {
			_ = w.Clear(themed)
		})
		run("clear-underlined", func(w *BufferWriter) {
			_ = w.Clear(underlined)
		})
		run("set-cells", func(w *BufferWriter) {
			setEveryCell(w, size.width, size.height, glyph)
		})
		run("frame", func(w *BufferWriter) {
			_ = w.Clear(themed)
			setEveryCell(w, size.width, size.height, glyph)
		})
	}
}

// setEveryCell writes c to every cell of a width x height grid.
// Handlers reach SetCell through term.Writer, so it must not be inlined
// into a caller that knows the concrete writer and devirtualizes the call.
//
//go:noinline
func setEveryCell(w term.Writer, width, height int, c term.Cell) {
	for y := range height {
		for x := range width {
			w.SetCell(term.Coordinates{X: x, Y: y}, c)
		}
	}
}

// BenchmarkBufferWriterImages measures a frame's trip through the
// writer: a handler drawing cells and placements through term.Writer,
// then the cells and placements read back.
func BenchmarkBufferWriterImages(b *testing.B) {
	ex := term.NewCell('x', 1, term.Attributes{})
	pic := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	placeOver := func(w term.Writer, id term.ImageID, r image.Rectangle) {
		w.DrawImage(term.Image{
			Src: pic, ID: id, Pos: term.Coordinates{X: r.Min.X, Y: r.Min.Y},
			Width: r.Dx(), Height: r.Dy(),
		})
	}
	middle := func(r image.Rectangle) image.Rectangle {
		return image.Rect(r.Dx()/3, r.Dy()/3, 2*r.Dx()/3, 2*r.Dy()/3)
	}
	scenes := []struct {
		name string
		draw func(w term.Writer, screen image.Rectangle, ids []term.ImageID)
	}{
		{
			name: "text",
			draw: func(w term.Writer, screen image.Rectangle, _ []term.ImageID) {
				fillRect(w, screen, ex)
			},
		},
		{
			name: "picture",
			draw: func(w term.Writer, screen image.Rectangle, ids []term.ImageID) {
				fillRect(w, screen, ex)
				placeOver(w, ids[0], screen)
			},
		},
		{
			name: "picture-under-window",
			draw: func(w term.Writer, screen image.Rectangle, ids []term.ImageID) {
				fillRect(w, screen, ex)
				placeOver(w, ids[0], screen)
				fillRect(w, middle(screen), ex)
			},
		},
		{
			name: "thumbnails-under-windows",
			draw: func(w term.Writer, screen image.Rectangle, ids []term.ImageID) {
				fillRect(w, screen, ex)
				cw, ch := screen.Dx()/8, screen.Dy()/8
				for i, id := range ids[:64] {
					x, y := i%8*cw, i/8*ch
					placeOver(w, id, image.Rect(x, y, x+cw-1, y+ch-1))
				}
				fillRect(w, middle(screen), ex)
				fillRect(w, middle(screen).Add(image.Pt(screen.Dx()/6, screen.Dy()/6)), ex)
			},
		},
		{
			name: "pictures-between-windows",
			draw: func(w term.Writer, screen image.Rectangle, ids []term.ImageID) {
				fillRect(w, screen, ex)
				for i, id := range ids[:8] {
					placeOver(w, id, screen)
					fillRect(w, middle(screen).Add(image.Pt(i-4, i-4)), ex)
				}
			},
		},
	}
	sizes := []struct {
		name          string
		width, height int
	}{
		{name: "1080p", width: 213, height: 60},
		{name: "2160p", width: 426, height: 120},
	}
	ids := make([]term.ImageID, 64)
	for i := range ids {
		ids[i] = term.NewImageID()
	}
	for _, size := range sizes {
		for _, scene := range scenes {
			b.Run(size.name+"/"+scene.name, func(b *testing.B) {
				w := NewBufferWriter(context.Background(), size.width, size.height)
				screen := image.Rect(0, 0, size.width, size.height)
				b.ReportAllocs()
				for b.Loop() {
					_ = w.Clear(term.Attributes{})
					scene.draw(w, screen, ids)
					_ = w.RawCells()
					_ = w.Images()
				}
			})
		}
	}
}
