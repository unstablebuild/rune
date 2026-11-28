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

// Package kageshader is a library of gshader shaders written in Kage, and
// the names configuration knows them by.
//
// # Writing shaders
//
// A program draws in pixels, so it starts with the //kage:unit pixels
// directive, and its Fragment returns premultiplied colours. A generator
// shades nothing, so it ignores imageSrc0. Programs that run over what
// they shade read it with imageSrc0At(srcPos).
//
//   - Read the time from Progress, which goes from 0 at the first frame of
//     a run towards 1 at its last, and from nothing else: time shaders
//     remap it, and the same frame must draw the same pixels whatever
//     came before it.
//   - Make a run end where it starts, so that it loops seamlessly: move
//     things a whole number of turns, as sin(2π·n·Progress) does, or a
//     whole number of steps through noise that repeats along the way it
//     moves, as the noise these shaders share does.
//   - Size things by Resolution, the size of what is drawn in pixels, and
//     place them by dstPos, which starts at its top-left corner, so they
//     look the same in any window.
//   - Take colours as vec4 uniforms set to term.Color: the device
//     resolves them in the theme, so palette colours follow it, and
//     Background and Foreground are the theme's own.
//   - Draw over Background, by a Strength, rather than at full strength:
//     a wallpaper sits under text.
package kageshader
