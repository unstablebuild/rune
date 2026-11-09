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

package vte

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestSyncFrameReplay(t *testing.T) {
	t.Parallel()
	a := term.Image{ID: term.NewImageID(), Width: 1, Height: 1}
	b := term.Image{ID: term.NewImageID(), Pos: term.Coordinates{X: 1}, Width: 1, Height: 1}
	bold := term.Attributes{Attrs: term.AttrBold}
	draw := func(w term.Writer) {
		w.SetCell(term.Coordinates{}, term.NewCell('x', 1, term.Attributes{}))
		w.UnionAttributes(term.Coordinates{}, bold)
		w.SetCell(term.Coordinates{X: 1}, term.NewCell('y', 1, bold))
		w.SetCell(term.Coordinates{X: 1}, term.NewCell('z', 1, term.Attributes{}))
		w.DrawImage(a)
		w.DrawImage(b)
	}

	cases := []struct {
		desc       string
		maxImages  int
		wantImages []term.Image
	}{
		{desc: "writer renders graphics", maxImages: 2, wantImages: []term.Image{a, b}},
		{desc: "writer stops taking images", maxImages: 1, wantImages: []term.Image{a}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			want := &limitedImageWriter{Writer: term.NewStringWriter(3, 1), max: tc.maxImages}
			draw(want)

			var f syncFrame
			draw(&f)
			got := &limitedImageWriter{Writer: term.NewStringWriter(3, 1), max: tc.maxImages}
			f.replay(got)

			assert.Equal(t, want.Writer.(*term.StringWriter).Cells(),
				got.Writer.(*term.StringWriter).Cells())
			assert.Equal(t, tc.wantImages, got.images)
		})
	}
}

func TestSyncFrameReset(t *testing.T) {
	t.Parallel()
	var f syncFrame
	f.SetCell(term.Coordinates{}, term.NewCell('x', 1, term.Attributes{}))
	f.UnionAttributes(term.Coordinates{}, term.Attributes{Attrs: term.AttrBold})
	f.DrawImage(term.Image{ID: term.NewImageID()})

	f.reset()

	w := &limitedImageWriter{Writer: term.NewStringWriter(1, 1), max: 1}
	f.replay(w)
	assert.Equal(t, term.NewStringWriter(1, 1).Cells(), w.Writer.(*term.StringWriter).Cells())
	assert.Empty(t, w.images)
	assert.Empty(t, f.images[:cap(f.images)][0].ID, "pixels must not stay referenced")
}

// limitedImageWriter takes max images per draw, like a writer whose
// surface cannot place more.
type limitedImageWriter struct {
	term.Writer
	max    int
	images []term.Image
}

func (w *limitedImageWriter) DrawImage(img term.Image) bool {
	if len(w.images) == w.max {
		return false
	}
	w.images = append(w.images, img)
	return true
}
