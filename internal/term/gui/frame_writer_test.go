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
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	sdkcomponent "github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/component/comptest"
	"github.com/unstablebuild/rune-go-sdk/term"
	"go.uber.org/mock/gomock"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
	"unstable.build/rune/internal/term/gui/font"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestFrameWriterDrawImage(t *testing.T) {
	tests := []struct {
		name    string
		img     term.Image
		want    bool
		wantLen int
		wantVis image.Rectangle
	}{
		{
			name:    "inside the grid is kept unclipped",
			img:     term.Image{Pos: term.Coordinates{X: 1, Y: 1}, Width: 3, Height: 2},
			want:    true,
			wantLen: 1,
			wantVis: image.Rect(1, 1, 4, 3),
		},
		{
			name:    "overflowing the grid is clipped to it",
			img:     term.Image{Pos: term.Coordinates{X: 8, Y: 3}, Width: 5, Height: 5},
			want:    true,
			wantLen: 1,
			wantVis: image.Rect(8, 3, 10, 5),
		},
		{
			name:    "fully outside the grid is dropped",
			img:     term.Image{Pos: term.Coordinates{X: 10, Y: 0}, Width: 2, Height: 2},
			want:    true,
			wantLen: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newFrameWriter(context.Background(), 10, 5)
			assert.Equal(t, tt.want, w.DrawImage(tt.img))
			require.Len(t, w.Images(), tt.wantLen)
			if tt.wantLen > 0 {
				assert.Equal(t, tt.wantVis, w.Images()[0].Visible())
			}
		})
	}
}

func TestFrameWriterClearDropsPlacements(t *testing.T) {
	w := newFrameWriter(context.Background(), 10, 5)
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

func TestFrameWriterComposite(t *testing.T) {
	const width, height = 12, 4
	grid := image.Rect(0, 0, width, height)
	window := image.Rect(4, 1, 7, 3)
	dot := term.NewCell('.', 1, term.Attributes{})
	ex := term.NewCell('x', 1, term.Attributes{})
	han := term.NewCell('漢', 2, term.Attributes{})
	thumbsUp := term.NewCell('👍', 2, term.Attributes{})
	thumbsUp.SetCombining([]rune{'🏽'})
	stripes := picture(
		"RGBRGBRG",
		"GBRGBRGB",
		"BRGBRGBR",
	)
	holes := picture(
		"R..B",
		"RRBB",
	)
	striped := func(w term.Writer) {
		fill(w, grid, dot)
		place(w, stripes, term.Coordinates{X: 2})
	}
	holed := func(w term.Writer) {
		fill(w, grid, dot)
		writeText(w, term.Coordinates{Y: 1}, "abcdefghijkl")
		place(w, holes, term.Coordinates{X: 2, Y: 1})
	}

	tests := []struct {
		name string
		draw func(w term.Writer)
		want string
	}{
		{
			name: "placement over the cells",
			draw: striped,
			want: rows(
				"..RGBRGBRG..",
				"..GBRGBRGB..",
				"..BRGBRGBR..",
				"............",
			),
		},
		{
			name: "placement over a cleared frame",
			draw: func(w term.Writer) {
				place(w, stripes, term.Coordinates{X: 2})
			},
			want: rows(
				"  RGBRGBRG  ",
				"  GBRGBRGB  ",
				"  BRGBRGBR  ",
				"            ",
			),
		},
		{
			name: "window inside the placement",
			draw: func(w term.Writer) {
				striped(w)
				fill(w, window, ex)
			},
			want: rows(
				"..RGBRGBRG..",
				"..GBxxxRGB..",
				"..BRxxxGBR..",
				"............",
			),
		},
		{
			name: "window across the placement's edge",
			draw: func(w term.Writer) {
				striped(w)
				fill(w, image.Rect(8, 1, 12, 4), ex)
			},
			want: rows(
				"..RGBRGBRG..",
				"..GBRGBRxxxx",
				"..BRGBRGxxxx",
				"........xxxx",
			),
		},
		{
			name: "window over the whole placement",
			draw: func(w term.Writer) {
				striped(w)
				fill(w, grid, ex)
			},
			want: rows(
				"xxxxxxxxxxxx",
				"xxxxxxxxxxxx",
				"xxxxxxxxxxxx",
				"xxxxxxxxxxxx",
			),
		},
		{
			name: "stacked windows",
			draw: func(w term.Writer) {
				striped(w)
				fill(w, window, ex)
				fill(w, image.Rect(5, 0, 9, 2), term.NewCell('o', 1, term.Attributes{}))
			},
			want: rows(
				"..RGBooooG..",
				"..GBxooooB..",
				"..BRxxxGBR..",
				"............",
			),
		},
		{
			name: "window of cells identical to the ones below",
			draw: func(w term.Writer) {
				striped(w)
				fill(w, window, dot)
			},
			want: rows(
				"..RGBRGBRG..",
				"..GB...RGB..",
				"..BR...GBR..",
				"............",
			),
		},
		{
			name: "window of null cells",
			draw: func(w term.Writer) {
				striped(w)
				fill(w, window, term.Cell{})
			},
			want: rows(
				"..RGBRGBRG..",
				"..GB   RGB..",
				"..BR   GBR..",
				"............",
			),
		},
		{
			name: "window of zero-width background cells",
			draw: func(w term.Writer) {
				striped(w)
				fill(w, window, term.NewCell(0, 0, term.Attributes{Bg: term.ColorRed}))
			},
			want: rows(
				"..RGBRGBRG..",
				"..GB   RGB..",
				"..BR   GBR..",
				"............",
			),
		},
		{
			name: "attributes alone leave the placement showing",
			draw: func(w term.Writer) {
				striped(w)
				for y := window.Min.Y; y < window.Max.Y; y++ {
					for x := window.Min.X; x < window.Max.X; x++ {
						w.UnionAttributes(term.Coordinates{X: x, Y: y},
							term.Attributes{Attrs: term.AttrReverse})
					}
				}
			},
			want: rows(
				"..RGBRGBRG..",
				"..GBRGBRGB..",
				"..BRGBRGBR..",
				"............",
			),
		},
		{
			name: "cells written before the placement stay under it",
			draw: func(w term.Writer) {
				fill(w, grid, dot)
				fill(w, window, ex)
				place(w, stripes, term.Coordinates{X: 2})
			},
			want: rows(
				"..RGBRGBRG..",
				"..GBRGBRGB..",
				"..BRGBRGBR..",
				"............",
			),
		},
		{
			name: "wide cell over the placement hides the column its glyph spills into",
			draw: func(w term.Writer) {
				striped(w)
				w.SetCell(term.Coordinates{X: 4, Y: 1}, han)
			},
			want: rows(
				"..RGBRGBRG..",
				"..GB漢.BRGB..",
				"..BRGBRGBR..",
				"............",
			),
		},
		{
			name: "emoji over the placement hides the column its glyph spills into",
			draw: func(w term.Writer) {
				striped(w)
				w.SetCell(term.Coordinates{X: 4, Y: 1}, thumbsUp)
			},
			want: rows(
				"..RGBRGBRG..",
				"..GB👍🏽.BRGB..",
				"..BRGBRGBR..",
				"............",
			),
		},
		{
			name: "wide cell left of the placement spills into its first column",
			draw: func(w term.Writer) {
				striped(w)
				w.SetCell(term.Coordinates{X: 1, Y: 1}, han)
			},
			want: rows(
				"..RGBRGBRG..",
				".漢.BRGBRGB..",
				"..BRGBRGBR..",
				"............",
			),
		},
		{
			name: "wide cells under the placement are covered along with their spill",
			draw: func(w term.Writer) {
				fill(w, grid, dot)
				w.SetCell(term.Coordinates{X: 1, Y: 1}, han)
				w.SetCell(term.Coordinates{X: 4, Y: 2}, thumbsUp)
				place(w, stripes, term.Coordinates{X: 2})
			},
			want: rows(
				"..RGBRGBRG..",
				".漢GBRGBRGB..",
				"..BRGBRGBR..",
				"............",
			),
		},
		{
			name: "transparent texels show the cells below",
			draw: holed,
			want: rows(
				"............",
				"abRdeBghijkl",
				"..RRBB......",
				"............",
			),
		},
		{
			name: "window over transparent and opaque texels",
			draw: func(w term.Writer) {
				holed(w)
				fill(w, image.Rect(3, 1, 5, 3), ex)
			},
			want: rows(
				"............",
				"abRxxBghijkl",
				"..RxxB......",
				"............",
			),
		},
		{
			name: "later placement leaves a window over an earlier one",
			draw: func(w term.Writer) {
				striped(w)
				fill(w, window, ex)
				place(w, picture("G.G"), term.Coordinates{X: 4, Y: 1})
			},
			want: rows(
				"..RGBRGBRG..",
				"..GBGxGRGB..",
				"..BRxxxGBR..",
				"............",
			),
		},
		{
			name: "later placement leaves a wide cell's spill over an earlier one",
			draw: func(w term.Writer) {
				striped(w)
				w.SetCell(term.Coordinates{X: 4, Y: 1}, han)
				place(w, picture(".."), term.Coordinates{X: 4, Y: 1})
			},
			want: rows(
				"..RGBRGBRG..",
				"..GB漢.BRGB..",
				"..BRGBRGBR..",
				"............",
			),
		},
		{
			name: "same picture placed twice",
			draw: func(w term.Writer) {
				fill(w, grid, dot)
				pic, id := picture("RG", "BR"), term.NewImageID()
				for _, pos := range []term.Coordinates{{}, {X: 6}} {
					w.DrawImage(term.Image{Src: pic, ID: id, Pos: pos, Width: 2, Height: 2})
				}
				fill(w, image.Rect(0, 0, 2, 1), ex)
			},
			want: rows(
				"xx....RG....",
				"BR....BR....",
				"............",
				"............",
			),
		},
		{
			name: "cropped placement",
			draw: func(w term.Writer) {
				fill(w, grid, dot)
				w.DrawImage(term.Image{
					Src: stripes, ID: term.NewImageID(), Crop: image.Rect(3, 0, 6, 3),
					Width: 3, Height: 3,
				})
				w.SetCell(term.Coordinates{X: 1, Y: 1}, ex)
			},
			want: rows(
				"RGB.........",
				"GxR.........",
				"BRG.........",
				"............",
			),
		},
		{
			name: "placement cut by the frame's edge",
			draw: func(w term.Writer) {
				fill(w, grid, dot)
				place(w, picture("RGBR", "BRGB", "GGGG"), term.Coordinates{X: 10, Y: 2})
				w.SetCell(term.Coordinates{X: 11, Y: 3}, ex)
			},
			want: rows(
				"............",
				"............",
				"..........RG",
				"..........Bx",
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			screen := newFrameScreen(t, width, height)
			comptest.TestComponent(t, drawing(tt.draw), screen,
				[]comptest.TestCase{{Expected: tt.want}})

			written := cell.NewBufferWriter(context.Background(), width, height)
			tt.draw(written)
			assert.Equal(t, written.RawCells(), screen.RawCells(),
				"cells read back as written")
		})
	}
}

func TestFrameWriterWindowsOverTerminalGraphics(t *testing.T) {
	const width, height = 24, 8
	terminal, feed := newTerminal(t)
	wm, _ := component.NewWindowManager(terminal, component.DefaultWindowManagerConfig())
	wm.Resize(width, height)
	feed(t,
		"abcdefghijklmnopqrst\r\n",
		"abcdefghijklmnopqrst\r\n",
		"abcdefghijklmnopqrst\r\n",
		"abcdefghijklmnopqrst\r\n",
		"abcdefghijklmnopqrst\r\n",
		"abcdefghijklmnopqrst",
		"\x1b[2;3H",
		kittyPicture(t, picture(
			"RRGGBB",
			"R....B",
			"RRGGBB",
		), "c=6,r=3,C=1"),
	)

	comptest.TestComponent(t, wm, newFrameScreen(t, width, height), []comptest.TestCase{
		{
			Expected: rows(
				"┌──────────────────────┐",
				"│abcdefghijklmnopqrst  │",
				"│abRRGGBBijklmnopqrst  │",
				"│abRdefgBijklmnopqrst  │",
				"│abRRGGBBijklmnopqrst  │",
				"│abcdefghijklmnopqrst  │",
				"│abcdefghijklmnopqrst  │",
				"└──────────────────────┘",
			),
		},
		{
			Action: func() {
				terminal.Component().Select(term.Coordinates{Y: 1})
				terminal.Component().SelectEnd(term.Coordinates{X: 12, Y: 3})
			},
			Expected: rows(
				"┌──────────────────────┐",
				"│abcdefghijklmnopqrst  │",
				"│abRRGGBBijklmnopqrst  │",
				"│abRdefgBijklmnopqrst  │",
				"│abRRGGBBijklmnopqrst  │",
				"│abcdefghijklmnopqrst  │",
				"│abcdefghijklmnopqrst  │",
				"└──────────────────────┘",
			),
		},
		{
			Action: func() {
				wm.FloatingWindow(sdkcomponent.StaticFloating(newCheatsheet(), 8, 2),
					component.FloatingConfig{Offset: term.Coordinates{X: 5, Y: 2}, Title: "help"})
			},
			Expected: rows(
				"┌──────────────────────┐",
				"│abcdefghijklmnopqrst  │",
				"│abRR█●█ help █opqrst  │",
				"│abRd│漢 字  key│opqrst  │",
				"│abRR│👍🏽  ok   │opqrst  │",
				"│abcd└────────┘opqrst  │",
				"│abcdefghijklmnopqrst  │",
				"└──────────────────────┘",
			),
		},
	})
}

func TestFrameWriterFloatingTerminalStacking(t *testing.T) {
	const width, height = 28, 10
	terminal, feed := newTerminal(t)
	editor := component.NewScroll(textBuffer(strings.Repeat("the quick brown fox jumps\n", height)))
	wm, _ := component.NewWindowManager(editor, component.DefaultWindowManagerConfig())
	browser := wm.FloatingWindow(sdkcomponent.StaticFloating(terminal, 10, 3),
		component.FloatingConfig{Offset: term.Coordinates{X: 2, Y: 1}, Title: "browser"})
	wm.Resize(width, height)
	feed(t,
		"0123456789\r\n",
		"abcdefghij\r\n",
		"klmnopqrst",
		"\x1b[H",
		kittyPicture(t, picture(
			"RRRRRGGGGG",
			"R..BBBBB.G",
			"RRRRRGGGGG",
		), "c=10,r=3,C=1"),
	)

	var cheatsheet component.Window
	comptest.TestComponent(t, wm, newFrameScreen(t, width, height), []comptest.TestCase{
		{
			Expected: rows(
				"┌──────────────────────────┐",
				"│t█●█ browser█wn fox jumps │",
				"│t│RRRRRGGGGG│wn fox jumps │",
				"│t│RbcBBBBBiG│wn fox jumps │",
				"│t│RRRRRGGGGG│wn fox jumps │",
				"│t└──────────┘wn fox jumps │",
				"│the quick brown fox jumps │",
				"│the quick brown fox jumps │",
				"│the quick brown fox jumps │",
				"└──────────────────────────┘",
			),
		},
		{
			Action: func() {
				cheatsheet = wm.FloatingWindow(sdkcomponent.StaticFloating(newCheatsheet(), 8, 2),
					component.FloatingConfig{Offset: term.Coordinates{X: 8, Y: 3}, Title: "help"})
			},
			Expected: rows(
				"┌──────────────────────────┐",
				"│t█●█ browser█wn fox jumps │",
				"│t│RRRRRGGGGG│wn fox jumps │",
				"│t│RbcBB█●█ help █ox jumps │",
				"│t│RRRRR│漢 字  key│ox jumps │",
				"│t└─────│👍🏽  ok   │ox jumps │",
				"│the qui└────────┘ox jumps │",
				"│the quick brown fox jumps │",
				"│the quick brown fox jumps │",
				"└──────────────────────────┘",
			),
		},
		{
			Action: func() { wm.ForegroundFloating(browser) },
			Expected: rows(
				"┌──────────────────────────┐",
				"│t█●█ browser█wn fox jumps │",
				"│t│RRRRRGGGGG│wn fox jumps │",
				"│t│RbcBBBBBiG│lp █ox jumps │",
				"│t│RRRRRGGGGG│key│ox jumps │",
				"│t└──────────┘   │ox jumps │",
				"│the qui└────────┘ox jumps │",
				"│the quick brown fox jumps │",
				"│the quick brown fox jumps │",
				"└──────────────────────────┘",
			),
		},
		{
			Action: func() { wm.ForegroundFloating(cheatsheet) },
			Expected: rows(
				"┌──────────────────────────┐",
				"│t█●█ browser█wn fox jumps │",
				"│t│RRRRRGGGGG│wn fox jumps │",
				"│t│RbcBB█●█ help █ox jumps │",
				"│t│RRRRR│漢 字  key│ox jumps │",
				"│t└─────│👍🏽  ok   │ox jumps │",
				"│the qui└────────┘ox jumps │",
				"│the quick brown fox jumps │",
				"│the quick brown fox jumps │",
				"└──────────────────────────┘",
			),
		},
	})
}

// BenchmarkFrameWriter measures a frame's trip through the writer: a
// handler drawing through the term.Writer API, then the renderer reading
// the cells and placements back.
func BenchmarkFrameWriter(b *testing.B) {
	ex := term.NewCell('x', 1, term.Attributes{})
	pic := picture("RGB", "GBR")
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
				fill(w, screen, ex)
			},
		},
		{
			name: "picture",
			draw: func(w term.Writer, screen image.Rectangle, ids []term.ImageID) {
				fill(w, screen, ex)
				placeOver(w, ids[0], screen)
			},
		},
		{
			name: "picture-under-window",
			draw: func(w term.Writer, screen image.Rectangle, ids []term.ImageID) {
				fill(w, screen, ex)
				placeOver(w, ids[0], screen)
				fill(w, middle(screen), ex)
			},
		},
		{
			name: "thumbnails-under-windows",
			draw: func(w term.Writer, screen image.Rectangle, ids []term.ImageID) {
				fill(w, screen, ex)
				cw, ch := screen.Dx()/8, screen.Dy()/8
				for i, id := range ids[:64] {
					x, y := i%8*cw, i/8*ch
					placeOver(w, id, image.Rect(x, y, x+cw-1, y+ch-1))
				}
				fill(w, middle(screen), ex)
				fill(w, middle(screen).Add(image.Pt(screen.Dx()/6, screen.Dy()/6)), ex)
			},
		},
		{
			name: "pictures-between-windows",
			draw: func(w term.Writer, screen image.Rectangle, ids []term.ImageID) {
				fill(w, screen, ex)
				for i, id := range ids[:8] {
					placeOver(w, id, screen)
					fill(w, middle(screen).Add(image.Pt(i-4, i-4)), ex)
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
				w := newFrameWriter(context.Background(), size.width, size.height)
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

// frameScreen is a comptest.StringerWriter over the writer the GUI draws
// its handler into. Flush prints the frame the renderer composites, one
// cell at a time: a cell where a placement paints an opaque texel shows
// that texel's palette letter, any other cell its grapheme as
// term.StringWriter prints it.
type frameScreen struct {
	*frameWriter
	fonts *font.Manager
	out   strings.Builder
}

func newFrameScreen(t *testing.T, width, height int) *frameScreen {
	return &frameScreen{
		frameWriter: newFrameWriter(context.Background(), width, height),
		fonts:       testFontManager(t),
	}
}

func (s *frameScreen) Flush() error {
	cells := s.RawCells()
	screen := make([][]string, len(cells))
	for y, row := range cells {
		screen[y] = make([]string, len(row))
		for x, c := range row {
			screen[y][x] = grapheme(c)
		}
	}
	bounds := cellRectToPixels(image.Rect(0, 0, s.width, s.height), s.fonts)
	for _, img := range s.Images() {
		p, ok := resolvePlacement(img, s.fonts, bounds)
		if !ok {
			continue
		}
		for y := range screen {
			for x := range screen[y] {
				px := cellRectToPixels(image.Rect(x, y, x+1, y+1), s.fonts)
				center := px.Min.Add(px.Size().Div(2))
				if !center.In(p.clip) {
					continue
				}
				if letter, ok := paletteLetter(sampleTexel(img, p, center)); ok {
					screen[y][x] = letter
				}
			}
		}
	}
	s.out.Reset()
	for y, row := range screen {
		if y > 0 {
			s.out.WriteByte('\n')
		}
		for _, g := range row {
			s.out.WriteString(g)
		}
	}
	return nil
}

func (s *frameScreen) String() string {
	return s.out.String()
}

func grapheme(c term.Cell) string {
	switch c.Ch {
	case 0, '\t', '\n':
		return " "
	}
	return string(c.Ch) + string(c.CombiningRunes())
}

// sampleTexel returns the texel p paints at the screen pixel pt, as the
// renderer maps the source rectangle onto the placement's area.
func sampleTexel(img term.Image, p placement, pt image.Point) color.Color {
	origin := img.Src.Bounds().Min
	x := p.src.Min.X + (pt.X-p.area.Min.X)*p.src.Dx()/p.area.Dx()
	y := p.src.Min.Y + (pt.Y-p.area.Min.Y)*p.src.Dy()/p.area.Dy()
	return img.Src.At(origin.X+x, origin.Y+y)
}

// palette names the colours test pictures are painted with; '.' is a
// transparent texel.
var palette = map[rune]color.NRGBA{
	'R': {R: 255, A: 255},
	'G': {G: 255, A: 255},
	'B': {B: 255, A: 255},
	'.': {},
}

// paletteLetter names c, reporting false when it is transparent and so
// paints nothing.
func paletteLetter(c color.Color) (string, bool) {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	if n.A == 0 {
		return "", false
	}
	for letter, p := range palette {
		if p == n {
			return string(letter), true
		}
	}
	return "?", true
}

// picture paints one texel per palette letter of rows.
func picture(rows ...string) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, utf8.RuneCountInString(rows[0]), len(rows)))
	for y, row := range rows {
		for x, letter := range []rune(row) {
			img.SetNRGBA(x, y, palette[letter])
		}
	}
	return img
}

// place draws pic with its top-left texel at pos and one texel per cell.
func place(w term.Writer, pic image.Image, pos term.Coordinates) {
	b := pic.Bounds()
	w.DrawImage(term.Image{
		Src: pic, ID: term.NewImageID(), Pos: pos, Width: b.Dx(), Height: b.Dy(),
	})
}

func fill(w term.Writer, r image.Rectangle, c term.Cell) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			w.SetCell(term.Coordinates{X: x, Y: y}, c)
		}
	}
}

// writeText writes s from pos, one single-width cell per rune.
func writeText(w term.Writer, pos term.Coordinates, s string) {
	for i, r := range []rune(s) {
		w.SetCell(term.Coordinates{X: pos.X + i, Y: pos.Y}, term.NewCell(r, 1, term.Attributes{}))
	}
}

func rows(r ...string) string {
	return strings.Join(r, "\n")
}

// drawing is a tui.Component whose Draw is a sequence of term.Writer
// calls.
type drawing func(w term.Writer)

func (drawing) Resize(int, int) {}

func (d drawing) Draw(w term.Writer) {
	d(w)
}

func textBuffer(s string) *cell.Buffer {
	buf := cell.NewBuffer()
	buf.WriteString(s)
	return buf
}

// newCheatsheet is a window's worth of text with wide and emoji
// graphemes over a themed background, which the scroll paints with
// zero-width null cells.
func newCheatsheet() *component.Scroll {
	s := component.NewScroll(textBuffer("漢字 keys\n👍🏽 ok"))
	s.Attributes = term.Attributes{Bg: term.ColorRed}
	return s
}

// kittyPicture is the kitty graphics command a program prints to show
// pic as a PNG, with keys adding placement options.
func kittyPicture(t *testing.T, pic image.Image, keys string) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, pic))
	return "\x1b_Ga=T,f=100,q=2," + keys + ";" +
		base64.StdEncoding.EncodeToString(buf.Bytes()) + "\x1b\\"
}

// newTerminal starts a terminal emulator with no program attached. feed
// hands it a program's output, once the window manager has sized it,
// and returns when the output has been processed.
func newTerminal(t *testing.T) (*vte.Handler, func(t *testing.T, out ...string)) {
	t.Helper()
	ctrl := gomock.NewController(t)
	output, program := io.Pipe()
	pty := pipeTerminal{master: &pipeFile{output: output}}
	cfg := vte.DefaultConfig()
	cfg.CellPixelSize = func() (int, int) { return 10, 20 }
	terminal, err := vte.NewHandler(nopPublisher{}, browsertest.NewMockNotifications(ctrl),
		pty, pty, browsertest.NewMockTabManager(ctrl), cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = program.Close()
		_ = terminal.Close()
	})
	return terminal, func(t *testing.T, out ...string) {
		t.Helper()
		for _, s := range out {
			_, err := io.WriteString(program, s)
			require.NoError(t, err)
		}
		require.NoError(t, program.Close())
		require.Eventually(t, terminal.Component().IsComplete, 5*time.Second, time.Millisecond)
	}
}

type nopPublisher struct{}

func (nopPublisher) PublishEvent(term.Event) error {
	return nil
}

// pipeTerminal is a workspace with no processes, whose pty master reads
// what the test writes.
type pipeTerminal struct {
	master *pipeFile
}

func (p pipeTerminal) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{Master: p.master, Slave: &workspacetest.File{}}, nil
}

func (pipeTerminal) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error {
	return nil
}

func (pipeTerminal) StartCommand(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return 0, nil
}

func (pipeTerminal) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}

func (pipeTerminal) Close() error {
	return nil
}

type pipeFile struct {
	workspacetest.File
	output *io.PipeReader
}

func (f *pipeFile) Read(b []byte) (int, error) {
	return f.output.Read(b)
}
