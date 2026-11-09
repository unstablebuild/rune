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

package main

import (
	"context"
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/braille"
)

var allLogoThemes = []string{
	"default", "furman", "romero", "carmack", "reynolds", "brevik",
	"thompson", "pike", "hopper", "kernighan", "wozniak", "auge",
	"ritchie", "kelleher", "sanfilippo", "greig", "mullen",
}

func TestLogoForThemeReturnsVariant(t *testing.T) {
	for _, theme := range allLogoThemes {
		t.Run(theme, func(t *testing.T) {
			img := logoForTheme(theme)
			require.NotNil(t, img)
			require.False(t, img.Bounds().Empty())
			require.Equal(t, logoImageBase.Bounds(), img.Bounds(),
				"variants must lay out like the base logo")
		})
	}
}

func TestLogoForThemeFallsBackToBase(t *testing.T) {
	for _, theme := range []string{"", "default", "carmack", "does-not-exist"} {
		require.Same(t, logoImageBase, logoForTheme(theme))
	}
}

func TestLogoForThemeHopperIsDistinctVariant(t *testing.T) {
	require.Same(t, logoImageHopper, logoForTheme("hopper"))
	require.NotSame(t, logoImageBase, logoForTheme("hopper"))
}

func TestLogoForThemeReturnsStableGlobal(t *testing.T) {
	for _, theme := range allLogoThemes {
		require.Same(t, logoForTheme(theme), logoForTheme(theme))
	}
}

func TestLogoPictureForThemeIsASharperLogo(t *testing.T) {
	for _, theme := range allLogoThemes {
		t.Run(theme, func(t *testing.T) {
			picture := logoPictureForTheme(theme)()
			logo := logoForTheme(theme).Bounds()
			require.Greater(t, picture.Bounds().Dy(), logo.Dy())
			assert.InDelta(t,
				float64(logo.Dx())/float64(logo.Dy()),
				float64(picture.Bounds().Dx())/float64(picture.Bounds().Dy()), 0.005,
				"the picture is stretched over the logo's layout")
		})
	}
	assert.NotEqual(t, logoPictureForTheme("default")(), logoPictureForTheme("hopper")())
}

func drawLogoSpan(
	img image.Image, cellPixelSize func() (int, int), style braille.Style, w, h int,
) [][]term.Cell {
	return drawCells(newLogoSpan(img, nil, cellPixelSize, style), w, h)
}

// drawCells returns the cells c draws when it is w by h cells.
func drawCells(c tui.Component, w, h int) [][]term.Cell {
	c.Resize(w, h)
	buf := cell.NewBufferWriter(context.Background(), w, h)
	c.Draw(buf)
	return buf.RawCells()
}

func TestLogoWallpaperIsBlocksOnBlankBraille(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {200, 60}, {30, 10}} {
		w, h := size[0], size[1]
		cells := drawLogoSpan(logoImageBase, nil, braille.Blocks, w, h)

		blocks := 0
		for y, row := range cells {
			for x, c := range row {
				require.Contains(t, []rune{braille.Blank, braille.Block}, c.Ch,
					"%dx%d: cell %d,%d", w, h, x, y)
				require.Equal(t, term.ColorDefault, c.Bg)
				if c.Ch == braille.Block {
					blocks++
				}
			}
		}
		assert.Equal(t, braille.Blank, cells[0][0].Ch, "%dx%d: padding is blank", w, h)
		assert.Equal(t, braille.Blank, cells[h-1][w-1].Ch, "%dx%d: padding is blank", w, h)
		assert.Positive(t, blocks, "%dx%d: no logo drawn", w, h)
	}
}

func TestLogoWallpaperTakesUpTheCentral48Percent(t *testing.T) {
	cellSize := func() (int, int) { return 9, 20 }
	for _, size := range [][2]int{{200, 60}, {120, 40}, {80, 24}} {
		w, h := size[0], size[1]
		var logo image.Rectangle
		for y, row := range drawLogoSpan(logoImageBase, cellSize, braille.Blocks, w, h) {
			for x, c := range row {
				if c.Ch != braille.Blank {
					logo = logo.Union(image.Rect(x, y, x+1, y+1))
				}
			}
		}
		// Windows this wide fit the logo to their height.
		assert.InDelta(t, 0.48*float64(h), float64(logo.Dy()), 1, "%dx%d: %v", w, h, logo)
		assert.InDelta(t, float64(w)/2, float64(logo.Min.X+logo.Max.X)/2, 1,
			"%dx%d: %v is centered", w, h, logo)
	}
}

func TestLogoDotsLineUpWithTheWallpaper(t *testing.T) {
	cellSize := func() (int, int) { return 9, 20 }
	for _, theme := range []string{"default", "hopper"} {
		themeName := func() string { return theme }
		wallpaper := makeThemedWallpaper(themeName, cellSize).NewComponent()
		wallpaper.Resize(120, 40)
		buf := cell.NewBufferWriter(context.Background(), 120, 40)
		wallpaper.Draw(buf)
		dots := drawCells(logoDots(themeName, cellSize), 120, 40)

		assert.Equal(t, drawLogoSpan(logoForTheme(theme), cellSize, braille.Dots, 120, 40), dots,
			"%s: dots draw the theme's logo on the wallpaper's layout", theme)
		for y, row := range buf.RawCells() {
			for x, c := range row {
				require.True(t, braille.IsPattern(dots[y][x].Ch), "%s: %d,%d", theme, x, y)
				if c.Ch == braille.Block {
					require.NotEqual(t, braille.Blank, dots[y][x].Ch,
						"%s: block at %d,%d has no dots", theme, x, y)
				}
			}
		}
	}
}

func newThemedWallpaper(t *testing.T, themeName func() string) *themedWallpaper {
	t.Helper()
	w, ok := makeThemedWallpaper(themeName, nil).NewComponent().(*themedWallpaper)
	require.True(t, ok)
	return w
}

func drawThemedWallpaper(t *testing.T, w *themedWallpaper) {
	t.Helper()
	w.Resize(80, 24)
	writer := cell.NewBufferWriter(context.Background(), 80, 24)
	w.Draw(writer)
}

func TestThemedWallpaperSwapsLogoOnThemeChange(t *testing.T) {
	theme := "carmack"
	w := newThemedWallpaper(t, func() string { return theme })

	drawThemedWallpaper(t, w)
	require.Equal(t, "carmack", w.current)
	first := w.child
	require.NotNil(t, first)

	drawThemedWallpaper(t, w)
	require.Same(t, first, w.child, "child should be reused when theme is unchanged")

	theme = "hopper"
	drawThemedWallpaper(t, w)
	require.Equal(t, "hopper", w.current)
	require.NotSame(t, first, w.child, "child should be rebuilt on theme change")
}

func TestThemedWallpaperNilThemeNameUsesDefault(t *testing.T) {
	w := newThemedWallpaper(t, nil)
	require.Equal(t, logoThemeDefault, w.resolveTheme())

	drawThemedWallpaper(t, w)
	require.Equal(t, logoThemeDefault, w.current)
	require.NotNil(t, w.child)
}

func TestThemedWallpaperDrawsForTheCellSize(t *testing.T) {
	cellSize := func() (int, int) { return 10, 10 }
	themed := makeThemedWallpaper(nil, cellSize).NewComponent()
	themed.Resize(80, 24)
	got := cell.NewBufferWriter(context.Background(), 80, 24)
	themed.Draw(got)

	assert.Equal(t, drawLogoSpan(logoImageBase, cellSize, braille.Blocks, 80, 24), got.RawCells())
	assert.NotEqual(t, drawLogoSpan(logoImageBase, nil, braille.Blocks, 80, 24), got.RawCells(),
		"square cells must draw the logo differently than tall ones")
}

func TestThemedWallpaperDrawsThePictureOverTheDots(t *testing.T) {
	cellSize := func() (int, int) { return 9, 20 }
	ctx := cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 9, Height: 20})
	for _, theme := range []string{"default", "hopper"} {
		themeName := func() string { return theme }
		wallpaper := makeThemedWallpaper(themeName, cellSize).NewComponent()
		wallpaper.Resize(120, 40)
		buf := cell.NewBufferWriter(ctx, 120, 40)
		wallpaper.Draw(buf)

		for y, row := range buf.RawCells() {
			for x, c := range row {
				require.Equal(t, braille.Blank, c.Ch, "%s: %d,%d", theme, x, y)
			}
		}
		var dotted image.Rectangle
		for y, row := range drawCells(logoDots(themeName, cellSize), 120, 40) {
			for x, c := range row {
				if c.Ch != braille.Blank {
					dotted = dotted.Union(image.Rect(x, y, x+1, y+1))
				}
			}
		}
		require.Len(t, buf.Images(), 1, theme)
		picture := buf.Images()[0]
		assert.True(t, dotted.In(picture.Visible()), "%s: the picture covers the dots", theme)
		assert.True(t, picture.Visible().In(dotted.Inset(-1)),
			"%s: the picture covers no more than the dots' layout", theme)
		assert.Equal(t, term.ImageFitFill, picture.Fit, theme)
	}
}

func TestThemedWallpaperDrawsThePictureOnAConfiguredBackground(t *testing.T) {
	const width, height = 120, 40
	cellSize := func() (int, int) { return 9, 20 }
	cfg := browser.DefaultConfig()
	cfg.Wallpaper = makeThemedWallpaper(nil, cellSize)
	cfg.Wallpaper.BackgroundAttr = term.Attributes{Bg: term.ColorBlue}
	b := browser.NewComponent(cfg)
	b.Resize(width, height)

	ctx := cell.ContextWithPixelSize(context.Background(), cell.PixelSize{Width: 9, Height: 20})
	buf := cell.NewBufferWriter(ctx, width, height)
	b.Draw(buf)

	require.Len(t, buf.Images(), 1, "the logo must be drawn as its picture")
	under := buf.Images()[0].Visible()
	for y := under.Min.Y; y < under.Max.Y; y++ {
		for x := under.Min.X; x < under.Max.X; x++ {
			c := buf.RawCells()[y][x]
			require.Equal(t, braille.Blank, c.Ch, "%d,%d", x, y)
			require.Equal(t, term.ColorBlue, c.Bg, "%d,%d", x, y)
		}
	}
}

func TestUnstableBuildLogoForTheme(t *testing.T) {
	base := unstableBuildLogoForTheme("default")
	for _, theme := range allLogoThemes {
		t.Run(theme, func(t *testing.T) {
			img := unstableBuildLogoForTheme(theme)
			require.Same(t, img, unstableBuildLogoForTheme(theme))
			require.Equal(t, base.Bounds(), img.Bounds(),
				"variants must lay out like the base logo")
			if theme == "hopper" {
				assert.NotEqual(t, base, img)
			} else {
				assert.Same(t, base, img)
			}
		})
	}
}

func drawUnstableBuildWallpaper(theme string, w, h int) [][]term.Cell {
	return drawCells(makeUnstableBuildWallpaper(func() string { return theme }).NewComponent(), w, h)
}

func TestUnstableBuildWallpaperIsAsciiArtOnThinSpaces(t *testing.T) {
	const w, h = 120, 40
	for _, theme := range []string{"default", "hopper"} {
		thin, blocks := 0, 0
		for y, row := range drawUnstableBuildWallpaper(theme, w, h) {
			for x, c := range row {
				switch c.Ch {
				case '\u2009':
					thin++
				case '▓':
					blocks++
				default:
					require.Contains(t, []rune{0, ' '}, c.Ch, "%s: %d,%d", theme, x, y)
				}
			}
		}
		assert.Positive(t, thin, "%s: no blank cells drawn", theme)
		assert.Positive(t, blocks, "%s: no logo drawn", theme)
	}
	assert.NotEqual(t, drawUnstableBuildWallpaper("default", w, h),
		drawUnstableBuildWallpaper("hopper", w, h), "hopper has its own logo")
}

func TestRuneLogoHasItsIntro(t *testing.T) {
	logo := makeRuneLogo(func() string { return "hopper" }, func() (int, int) { return 9, 20 })
	assert.NotNil(t, logo.Wallpaper.NewComponent)
	assert.NotNil(t, logo.Shader)
	assert.Equal(t, initShaderFPS, logo.ShaderFPS)
	assert.Equal(t, initShaderDuration, logo.ShaderDuration)
}

func TestUnstableBuildLogoHasNoIntro(t *testing.T) {
	logo := makeUnstableBuildLogo()
	assert.NotNil(t, logo.Wallpaper.NewComponent)
	assert.Nil(t, logo.Shader)
}
