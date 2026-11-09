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

package timeshader

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

func TestPeriod(t *testing.T) {
	for _, tc := range []struct {
		name         string
		frames       int
		frame, total int
		want         int
	}{
		{name: "plays the first run as it is", frames: 30, frame: 10, total: 300, want: 10},
		{name: "ends a run on its last frame", frames: 30, frame: 29, total: 300, want: 29},
		{name: "starts the next run over", frames: 30, frame: 30, total: 300, want: 0},
		{name: "keeps its pace runs later", frames: 30, frame: 250, total: 300, want: 10},
		{name: "keeps its pace over a shorter total", frames: 30, frame: 5, total: 10, want: 5},
		{name: "a run of one frame", frames: 1, frame: 7, total: 10, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := new(nopShader)

			Period(base, tc.frames).Shade(tc.frame, tc.total, [][]term.Cell{})

			assert.Equal(t, tc.want, base.frame)
			assert.Equal(t, tc.frames, base.total)
		})
	}
}

func TestPeriodShadesImagesWithTheBaseShader(t *testing.T) {
	images := []term.Image{{ID: 1, Width: 1, Height: 1}}
	moved := []term.Image{{ID: 2, Width: 1, Height: 1}}
	base := &imageShader{images: moved}

	got := shader.ShadeImages(Period(base, 4), 6, 10, [][]term.Cell{}, images)

	assert.Equal(t, moved, got)
	assert.Equal(t, images, base.got)
	assert.Equal(t, 2, base.frame)
	assert.Equal(t, 4, base.total)
}
