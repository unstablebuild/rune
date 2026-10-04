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

// Package imageuritest provides a real 194x179 picture, encoded in every
// format imageuri decodes, for tests that display images.
//
// SampleWebP is lossless, so it decodes to the same pixels as SamplePNG.
// SampleJPEG and SampleGIF are lossy and quantized respectively, so they
// render slightly differently.
package imageuritest

import _ "embed"

// SamplePNG is the sample picture as a PNG.
//
//go:embed testdata/sample.png
var SamplePNG []byte

// SampleJPEG is the sample picture as a JPEG.
//
//go:embed testdata/sample.jpg
var SampleJPEG []byte

// SampleGIF is the sample picture as a GIF.
//
//go:embed testdata/sample.gif
var SampleGIF []byte

// SampleWebP is the sample picture as a lossless WebP.
//
//go:embed testdata/sample.webp
var SampleWebP []byte
