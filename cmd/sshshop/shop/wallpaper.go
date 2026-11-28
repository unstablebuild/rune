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

package shop

import (
	"bytes"
	_ "embed"
	"image"
	"image/png"

	"github.com/unstablebuild/rune-go-sdk/component"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/component/asciiart"
)

//go:embed unstable_build_logo.png
var logoBytes []byte

var logo image.Image

func init() {
	img, err := png.Decode(bytes.NewReader(logoBytes))
	if err != nil {
		panic("shop: decode logo: " + err.Error())
	}
	logo = img
}

// makeWallpaper returns the Unstable Build wallpaper used for empty
// browser windows. Mirrors cmd/rune/wallpaper.go so the storefront
// shares the editor's look.
func makeWallpaper() browser.Wallpaper {
	return browser.Wallpaper{
		NewComponent: func() browser.ComponentCloser {
			cfg := asciiart.DefaultConfig()
			cfg.Color = true
			cfg.MaintainAspectRatio = true
			cfg.DensityCharacters = "\u2009▓▓▓▓▓▓▓▓▓"
			img := asciiart.NewComponent(logo, cfg)
			return browser.NopCloser(component.NewSpan(img, component.SpanConfig{
				PadHorizontalPerc: 0.4,
				PadVerticalPerc:   0.2,
				ContentAlignment:  component.AlignmentCentered,
			}))
		},
	}
}
