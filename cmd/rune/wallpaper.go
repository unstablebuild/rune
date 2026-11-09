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
	"image"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/asciiart"
	"unstable.build/rune/internal/component/braille"
	"unstable.build/rune/internal/ide"
)

// makeRuneLogo returns the Rune logo of the GUI, drawn for the live GUI
// theme themeName reports and the font cellPixelSize reports, with its
// intro.
func makeRuneLogo(
	themeName func() string, cellPixelSize func() (width, height int),
) ide.Logo {
	return ide.Logo{
		Wallpaper:      makeThemedWallpaper(themeName, cellPixelSize),
		Shader:         makeInitShader(defaultIntroParams(), logoDots(themeName, cellPixelSize)),
		ShaderFPS:      initShaderFPS,
		ShaderDuration: initShaderDuration,
	}
}

// makeUnstableBuildLogo returns the Unstable Build logo of the terminal,
// which has no intro.
func makeUnstableBuildLogo() ide.Logo {
	return ide.Logo{
		Wallpaper: makeUnstableBuildWallpaper(func() string { return logoThemeDefault }),
	}
}

// newLogoSpan builds the centered art used to render a logo as a
// wallpaper, drawn in style, or as picture where the screen draws images
// when picture is not nil (see [braille.Component.WithPicture]). The
// wallpapers draw it with blocks or its picture, and the intro with dots
// on the same layout so it can turn one into the other.
//
// The padding around the logo is filled with blank braille, which tells
// the intro where the wallpaper is (see [shader.BrailleSwap]).
//
// cellPixelSize keeps the logo's proportions on the font in use; see
// [braille.NewComponent].
func newLogoSpan(
	img image.Image, picture func() image.Image,
	cellPixelSize func() (width, height int), style braille.Style,
) tui.Component {
	art := braille.NewComponent(img, cellPixelSize, style)
	if picture != nil {
		art.WithPicture(picture)
	}
	span := component.NewSpan(art, component.SpanConfig{
		PadHorizontalPerc: 0.52,
		PadVerticalPerc:   0.52,
		ContentAlignment:  component.AlignmentCentered,
	})
	return component.NewBackground(span, term.NewCell(braille.Blank, 1, term.Attributes{}))
}

// logoDots draws the logo wallpaper that makeThemedWallpaper builds from
// themeName and cellPixelSize with dots instead of blocks, for the intro.
func logoDots(themeName func() string, cellPixelSize func() (width, height int)) tui.Component {
	return &themedWallpaper{themeName: themeName, newLogo: func(theme string) tui.Component {
		return newLogoSpan(logoForTheme(theme), nil, cellPixelSize, braille.Dots)
	}}
}

// makeThemedWallpaper builds a wallpaper whose logo art follows the live GUI
// theme reported by themeName. When themeName is nil or returns an empty or
// unknown theme, the default logo is used. cellPixelSize is passed on to
// [newLogoSpan], and the logo is drawn as a picture where the screen draws
// images.
func makeThemedWallpaper(
	themeName func() string, cellPixelSize func() (width, height int),
) browser.Wallpaper {
	return browser.Wallpaper{
		NewComponent: func() tui.Component {
			return &themedWallpaper{themeName: themeName, newLogo: func(theme string) tui.Component {
				return newLogoSpan(logoForTheme(theme), logoPictureForTheme(theme), cellPixelSize, braille.Blocks)
			}}
		},
	}
}

// makeUnstableBuildWallpaper is makeThemedWallpaper for the Unstable Build
// logo, which is drawn as ascii art.
func makeUnstableBuildWallpaper(themeName func() string) browser.Wallpaper {
	return browser.Wallpaper{
		NewComponent: func() tui.Component {
			return &themedWallpaper{themeName: themeName, newLogo: func(theme string) tui.Component {
				return newUnstableBuildSpan(unstableBuildLogoForTheme(theme))
			}}
		},
	}
}

// newUnstableBuildSpan builds the centered ascii art that renders img as a
// wallpaper, leaving its darkest cells blank.
func newUnstableBuildSpan(img image.Image) tui.Component {
	cfg := asciiart.DefaultConfig()
	cfg.Color = true
	cfg.MaintainAspectRatio = true
	cfg.DensityCharacters = "\u2009▓▓▓▓▓▓▓▓▓"
	art := asciiart.NewComponent(img, cfg)
	return component.NewSpan(art, component.SpanConfig{
		PadHorizontalPerc: 0.4,
		PadVerticalPerc:   0.2,
		ContentAlignment:  component.AlignmentCentered,
	})
}

// themedWallpaper renders the logo newLogo builds for the current GUI
// theme, rebuilding it only when the resolved theme changes.
type themedWallpaper struct {
	themeName func() string
	newLogo   func(theme string) tui.Component
	current   string
	child     tui.Component
	width     int
	height    int
	drawn     bool
}

func (w *themedWallpaper) resolveTheme() string {
	return resolveLogoTheme(w.themeName)
}

func resolveLogoTheme(themeName func() string) string {
	if themeName == nil {
		return logoThemeDefault
	}
	if name := themeName(); name != "" {
		return name
	}
	return logoThemeDefault
}

func (w *themedWallpaper) Resize(width, height int) {
	w.width, w.height = width, height
	if w.child != nil {
		w.child.Resize(width, height)
	}
}

func (w *themedWallpaper) Draw(writer term.Writer) {
	theme := w.resolveTheme()
	if w.child == nil || !w.drawn || theme != w.current {
		w.current = theme
		w.child = w.newLogo(theme)
		w.child.Resize(w.width, w.height)
		w.drawn = true
	}
	w.child.Draw(writer)
}
