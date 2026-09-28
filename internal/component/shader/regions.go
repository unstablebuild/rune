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

package shader

import (
	"github.com/unstablebuild/rune-go-sdk/term"
)

// Rect is a rectangle of cells, relative to the matrix being shaded.
type Rect struct {
	Offset        term.Coordinates
	Width, Height int
}

// Regions returns a Shader that applies inner to each rectangle
// returned by regions, as Virtual does for a single one. regions is
// called on every frame, so the shaded geometry follows layout changes
// without any bookkeeping. Cells outside every rectangle pass through
// unchanged.
func Regions(inner Shader, regions func() []Rect) Shader {
	return regionsShader{inner: inner, regions: regions}
}

type regionsShader struct {
	inner   Shader
	regions func() []Rect
}

func (s regionsShader) Shade(frame, total int, cells [][]term.Cell) {
	for _, r := range s.regions() {
		Virtual(s.inner, r.Offset, r.Width, r.Height).Shade(frame, total, cells)
	}
}
