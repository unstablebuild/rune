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

// Package wingfx applies native Windows window-graphics attributes to
// the Ebiten/Direct3D surface Rune already presents on GOOS=windows.
//
// Linux talks to the GPU through OpenGL. macOS talks through Metal.
// Windows talks through Ebiten's Direct3D 11 driver. This package is
// the missing compositor half of that path: per-monitor DPI, a dark
// title bar, rounded corners, and DWM backdrop (Mica on Windows 11,
// Acrylic blur on Windows 10) so transparent themes match the other
// platforms instead of rendering as an opaque Win32 frame.
package wingfx
