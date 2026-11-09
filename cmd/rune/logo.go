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
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/png"
	"sync"
)

//go:generate go run ./logogen

const logoThemeDefault = "default"

// logoVariants maps a GUI theme name to its logo art. Only themes with art
// that differs from the base logo need an entry; every other theme falls back
// to the shared base logo via logoForTheme.
var logoVariants = map[string]image.Image{
	"hopper": logoImageHopper,
}

// logoForTheme returns the logo art for the given theme name, falling back to
// the base logo when the theme has no dedicated art.
func logoForTheme(theme string) image.Image {
	if img, ok := logoVariants[theme]; ok {
		return img
	}
	return logoImageBase
}

var (
	//go:embed logo_base.gen.png
	logoPictureBasePNG []byte
	//go:embed logo_hopper.gen.png
	logoPictureHopperPNG []byte
)

// The pictures are the logo art at full resolution, which only the GUI
// draws, so they are decoded on first use.
var (
	logoPictureBase   = sync.OnceValue(func() image.Image { return decodeLogoPicture(logoPictureBasePNG) })
	logoPictureHopper = sync.OnceValue(func() image.Image { return decodeLogoPicture(logoPictureHopperPNG) })
)

// logoPictureVariants is logoVariants for the pictures.
var logoPictureVariants = map[string]func() image.Image{
	"hopper": logoPictureHopper,
}

// logoPictureForTheme returns the picture of the logo art logoForTheme
// returns for theme.
func logoPictureForTheme(theme string) func() image.Image {
	if picture, ok := logoPictureVariants[theme]; ok {
		return picture
	}
	return logoPictureBase
}

var (
	//go:embed logo_unstable_build.gen.png
	logoUnstableBuildPNG []byte
	//go:embed logo_unstable_build_hopper.gen.png
	logoUnstableBuildHopperPNG []byte
)

// The Unstable Build logos are only drawn in a terminal, so they are
// decoded on first use.
var (
	logoUnstableBuild = sync.OnceValue(func() image.Image {
		return decodeLogoPicture(logoUnstableBuildPNG)
	})
	logoUnstableBuildHopper = sync.OnceValue(func() image.Image {
		return decodeLogoPicture(logoUnstableBuildHopperPNG)
	})
)

// unstableBuildLogoVariants is logoVariants for the Unstable Build logo.
var unstableBuildLogoVariants = map[string]func() image.Image{
	"hopper": logoUnstableBuildHopper,
}

// unstableBuildLogoForTheme is logoForTheme for the Unstable Build logo.
func unstableBuildLogoForTheme(theme string) image.Image {
	if img, ok := unstableBuildLogoVariants[theme]; ok {
		return img()
	}
	return logoUnstableBuild()
}

func decodeLogoPicture(data []byte) image.Image {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		panic(fmt.Sprintf("decode embedded logo picture: %v", err))
	}
	return img
}
