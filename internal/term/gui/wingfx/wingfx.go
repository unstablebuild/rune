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

package wingfx

// Effects describes the compositor treatment to apply to the Rune
// window once the native HWND exists.
type Effects struct {
	// Transparent requests an alpha-aware DWM backdrop so
	// ebiten.RunGameOptions.ScreenTransparent is visible through the
	// window instead of being composited against an opaque frame.
	Transparent bool
	// BlurRadius is the requested backdrop blur in pixels. Windows
	// maps this onto Acrylic (Windows 10) or Mica (Windows 11);
	// the compositor, not Rune, owns the actual kernel radius.
	BlurRadius int
}

// EnableProcessDPI turns on per-monitor DPI awareness for the process.
func EnableProcessDPI() { enableProcessDPI() }

// ApplyWindowEffects finds the HWND titled title and applies DWM effects.
func ApplyWindowEffects(title string, fx Effects) bool {
	return applyWindowEffects(title, fx)
}

// Supported reports whether this build can talk to the Windows compositor.
func Supported() bool { return supported }
