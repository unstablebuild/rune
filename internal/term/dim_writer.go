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

package term

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/term/graphemecluster"
)

type dimWriter struct {
	w term.Writer
}

// DimWriter returns a Writer that sets term.AttrDim
// and removes term.AttrBold to all cells by default.
func DimWriter(w term.Writer) term.Writer {
	return dimWriter{w: w}
}

func (w dimWriter) SetCell(pos term.Coordinates, c term.Cell) {
	if !graphemecluster.IsBackground(c.Ch) {
		c.Attrs |= term.AttrDim
	}
	w.w.SetCell(pos, c)
}

func (w dimWriter) UnionAttributes(pos term.Coordinates, attr term.Attributes) {
	w.w.UnionAttributes(pos, attr)
}

func (w dimWriter) Context() context.Context {
	return w.w.Context()
}

func (w dimWriter) DrawImage(img term.Image) bool {
	return w.w.DrawImage(img)
}
