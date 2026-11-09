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

// Package appicon replaces the icon that the macOS Dock and application
// switcher show for the running process.
package appicon

import "image"

// Set shows img as the application icon until the process exits or Set runs
// again. A nil img restores the icon of the application bundle. It must run
// on the main thread, and does nothing outside macOS.
func Set(img image.Image) {
	nrgba := toNRGBA(img)
	if nrgba == nil {
		setApplicationIcon(nil, 0, 0)
		return
	}
	size := nrgba.Rect.Size()
	setApplicationIcon(nrgba.Pix, size.X, size.Y)
}

// toNRGBA returns the straight-alpha pixels of img, tightly packed in rows
// from its top-left corner, or nil when img is nil.
func toNRGBA(img image.Image) *image.NRGBA {
	if img == nil {
		return nil
	}
	b := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	// Set converts per pixel without the premultiplied round trip of
	// draw.Draw, which would alter the color of translucent pixels.
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dst.Set(x-b.Min.X, y-b.Min.Y, img.At(x, y))
		}
	}
	return dst
}
