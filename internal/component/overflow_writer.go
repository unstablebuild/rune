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

package component

import "github.com/unstablebuild/rune-go-sdk/term"

// overflowDeferWriter holds back the overflowing images drawn through it
// until flush, and forwards everything else.
type overflowDeferWriter struct {
	term.Writer
	images []term.Image
	// probed is set once draws holds whether Writer draws images.
	probed, draws bool
}

// DrawImage satisfies term.Writer.
func (w *overflowDeferWriter) DrawImage(img term.Image) bool {
	if !img.Overflow {
		return w.Writer.DrawImage(img)
	}
	if !w.probed {
		// No writer clips an empty overflowing image on its way to the
		// one that owns the surface, which drops it and reports whether
		// it draws images at all.
		w.draws = w.Writer.DrawImage(term.Image{Overflow: true})
		w.probed = true
	}
	if w.draws {
		w.images = append(w.images, img)
	}
	return w.draws
}

// flush draws the images held back so far.
func (w *overflowDeferWriter) flush() {
	for _, img := range w.images {
		w.Writer.DrawImage(img)
	}
	clear(w.images)
	w.images = w.images[:0]
}
