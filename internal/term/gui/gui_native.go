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

import "unstable.build/rune/internal/term/gui/wingfx"

// prepareNativeWindow records the window title and asks the OS for
// per-monitor DPI v2 before Ebiten creates the HWND. Safe on every OS:
// wingfx.EnableProcessDPI is a no-op off Windows.
func prepareNativeWindow(title string) {
	nativeWindowTitle = title
	wingfx.EnableProcessDPI()
}

var (
	nativeWindowTitle string
	nativeWindowReady bool
)

// applyNativeWindowEffects attaches DWM compositor settings once the
// HWND exists. Returns immediately after the first success, and is a
// no-op on Linux and macOS (wingfx.Supported is false there).
func applyNativeWindowEffects(transparent bool, blurRadius int) {
	if nativeWindowReady || !wingfx.Supported() {
		return
	}
	if wingfx.ApplyWindowEffects(nativeWindowTitle, wingfx.Effects{
		Transparent: transparent,
		BlurRadius:  blurRadius,
	}) {
		nativeWindowReady = true
	}
}
