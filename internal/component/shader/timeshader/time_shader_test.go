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
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
)

// nopShader is useful to assert the frame it has been given upon Shade()
type nopShader struct {
	shadeCalled  bool
	frame, total int
}

func (s *nopShader) Shade(frame, total int, in [][]term.Cell) {
	s.shadeCalled = true
	s.frame = frame
	s.total = total
}

func (s *nopShader) reset() {
	s.shadeCalled = false
	s.frame = 0
}

// imageShader records what it shades, and replaces the placements it is
// given with images.
type imageShader struct {
	nopShader
	images, got []term.Image
}

func (s *imageShader) ShadeImages(frame, total int, in [][]term.Cell, images []term.Image) []term.Image {
	s.Shade(frame, total, in)
	s.got = images
	return s.images
}

func TestTimeShaderShadesImagesWithTheBaseShader(t *testing.T) {
	images := []term.Image{{ID: 1, Width: 1, Height: 1}}
	moved := []term.Image{{ID: 2, Width: 1, Height: 1}}
	base := &imageShader{images: moved}

	got := shader.ShadeImages(Reverse(base), 6, 10, [][]term.Cell{}, images)

	assert.Equal(t, moved, got)
	assert.Equal(t, images, base.got)
	assert.Equal(t, 3, base.frame)
	assert.Equal(t, 10, base.total)
}

func TestTimeShader(t *testing.T) {
	sh := new(nopShader)

	remapCalled := false

	plusoneFrameMap := func(frame, total int) int {
		remapCalled = true
		return frame + 1
	}

	plusOneShader := &timeShader{sh, funcTimeRemapper(plusoneFrameMap)}

	tearDown := func() {
		remapCalled = false
		sh.reset()
	}

	t.Run("shade on base shader is called", func(t *testing.T) {
		defer tearDown()
		plusOneShader.Shade(1, 20, [][]term.Cell{})
		assert.True(t, sh.shadeCalled)
	})

	t.Run("remap time is called", func(t *testing.T) {
		defer tearDown()
		plusOneShader.Shade(1, 20, [][]term.Cell{})
		assert.True(t, remapCalled)
	})

	t.Run("base shader receives mapped frame", func(t *testing.T) {
		defer tearDown()
		plusOneShader.Shade(1, 20, [][]term.Cell{})
		assert.Equal(t, 2, sh.frame)
	})

	nopFrameMapping := func(frame, total int) int {
		remapCalled = true
		return frame
	}

	tsuite := []struct {
		name              string
		frame             int
		total             int
		duration          int
		mapper            func(frame, total int) int
		panics            bool
		expectFrame       int
		expectRemapCalled bool
		expectShadeCalled bool
	}{
		{
			name:              "zero duration no panic",
			total:             0,
			panics:            false,
			expectRemapCalled: true,
			expectShadeCalled: true,
		},
		{
			name:              "negative duration no panic",
			total:             -10,
			panics:            false,
			expectRemapCalled: true,
			expectShadeCalled: true,
		},
		{
			name:  "transform produces an integer that overflows no panic",
			frame: 9223372036854775807,
			mapper: func(frame, total int) int {
				remapCalled = true
				res := frame * 9223372036854775807
				return res
			},
			panics:            false,
			expectFrame:       1, // strange it gives you this when operation overflows...
			expectRemapCalled: true,
			expectShadeCalled: true,
		},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			defer tearDown()

			mapper := nopFrameMapping
			if tcase.mapper != nil {
				mapper = tcase.mapper
			}

			ts := &timeShader{sh, funcTimeRemapper(mapper)}
			shade := func() {
				ts.Shade(tcase.frame, tcase.total, [][]term.Cell{})
			}

			if tcase.panics {
				assert.Panics(t, shade)
			} else {
				assert.NotPanics(t, shade)
				assert.Equal(t, tcase.expectFrame, sh.frame)
				assert.Equal(t, tcase.expectRemapCalled, remapCalled)
				assert.Equal(t, tcase.expectShadeCalled, sh.shadeCalled)
			}
		})
	}
}

// genPlot generates a printable 74x35 matrix showing the shape of the time
// transformation and tests it against an input plot you give it.
//
// The domain (horizontal axis) is between frame=0 and frame=100.
//
// You can shift the graph vertically with offsetYPerc (0.5 means shifting up
// the graph half the height) or scale the visual output (happens after
// offsetYPerc) with scaleY.
//
// It also adds the newlines so in your testing code you define it like:
//
//	myPlot = `
//	  ········x
//	  ······x··
//	  ····x····
//	  ··x······
//	  x++++++++
//	`
//
// The origin (output frame == 0) is painted with "+" characters so you can
// know if an "x" is there time would be frame=0.
func genPlot(ts *timeShader, scaleY float64, offsetYPerc float64) string {
	sh := new(nopShader)
	ts.baseShader = sh

	w := 74
	h := 35

	plot := make([]string, w*h)
	for col := range w {
		for row := range h {
			plot[row*w+col] = "·"
			if row == h-1-int(math.Round(float64(h)*offsetYPerc)) {
				plot[row*w+col] = "+"
			}
		}
	}

	for col := range w {
		ts.Shade(int(math.Round(float64(col)*100.0/float64(w))), 100, [][]term.Cell{})
		y := sh.frame
		yy := int(math.Max(0.0, float64(h)-1.0-math.Round(
			(scaleY*(float64(y)/100.0)+offsetYPerc)*float64(h),
		)))
		if yy*w+col < len(plot) {
			plot[yy*w+col] = "x"
		}
	}

	// insert newline characters after every row
	for i := w; i < len(plot); i += w + 1 {
		plot = append(plot[:i], append([]string{"\n"}, plot[i:]...)...)
	}

	ret := strings.Join(plot, "")
	ret = "\n" + ret + "\n"
	return ret
}
