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

package shaderdevicetest

import (
	"fmt"
	"image"
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/gshadertest"
)

// Tolerance is how far apart, per channel, the pixels the assertions
// compare may be, for the GPU's rounding.
const Tolerance = 2

// AssertDeterministic asserts that sh describes and renders each frame of
// a run of total over size cells the same on two devices.
func AssertDeterministic(t testing.TB, sh gshader.Shader, total int, size image.Point) bool {
	t.Helper()
	a, b := New(t), New(t)
	for frame := range total {
		if !gshadertest.AssertEqual(t, sh.Shade(frame, total, gshader.Source()),
			sh.Shade(frame, total, gshader.Source()), "frame %d", frame) {
			return false
		}
		want, ok := render(t, a, sh, frame, total, size)
		if !ok {
			return false
		}
		got, ok := render(t, b, sh, frame, total, size)
		if !ok || !assertPixels(t, want, got, "frame %d", frame) {
			return false
		}
	}
	return true
}

// AssertOrderIndependent asserts that sh describes and renders each frame
// of a run of total over size cells the same whether the run is rendered
// in order or shuffled, as shaders must keep no state between frames for
// time shaders to remap them.
func AssertOrderIndependent(t testing.TB, sh gshader.Shader, total int, size image.Point) bool {
	t.Helper()
	ordered := make([]gshader.Node, total)
	for frame := range total {
		ordered[frame] = sh.Shade(frame, total, gshader.Source())
	}
	rng := rand.New(rand.NewPCG(1, 2))
	frames := rng.Perm(total)
	for _, frame := range frames {
		if !gshadertest.AssertEqual(t, ordered[frame], sh.Shade(frame, total, gshader.Source()),
			"frame %d, shuffled", frame) {
			return false
		}
	}
	a, b := New(t), New(t)
	want := make([]*image.RGBA, total)
	for frame := range total {
		var ok bool
		if want[frame], ok = render(t, a, sh, frame, total, size); !ok {
			return false
		}
	}
	for _, frame := range frames {
		got, ok := render(t, b, sh, frame, total, size)
		if !ok || !assertPixels(t, want[frame], got, "frame %d, shuffled", frame) {
			return false
		}
	}
	return true
}

// AssertSeamless asserts that a run of total over size cells ends where it
// starts: frame total, the one a loop shows frame 0 in place of, renders
// the pixels of frame 0.
func AssertSeamless(t testing.TB, sh gshader.Shader, total int, size image.Point) bool {
	t.Helper()
	h := New(t)
	first, ok := render(t, h, sh, 0, total, size)
	if !ok {
		return false
	}
	end, ok := render(t, h, sh, total, total, size)
	if !ok {
		return false
	}
	return assertPixels(t, first, end, "frame %d against frame 0", total)
}

// render renders frame of total with h, failing t when it cannot.
func render(t testing.TB, h *Harness, sh gshader.Shader, frame, total int, size image.Point) (*image.RGBA, bool) {
	t.Helper()
	pixels, err := h.Render(sh, frame, total, size)
	return pixels, assert.NoError(t, err, "frame %d", frame)
}

// assertPixels asserts that want and got are within Tolerance of each
// other.
func assertPixels(t testing.TB, want, got *image.RGBA, msg string, args ...any) bool {
	t.Helper()
	if want.Rect != got.Rect {
		return assert.Fail(t, fmt.Sprintf("bounds %v, want %v", got.Rect, want.Rect), fmt.Sprintf(msg, args...))
	}
	for i := range want.Pix {
		d := int(want.Pix[i]) - int(got.Pix[i])
		if d > Tolerance || d < -Tolerance {
			p := i / 4
			x, y := p%want.Rect.Dx(), p/want.Rect.Dx()
			return assert.Fail(t, fmt.Sprintf("pixel %d,%d is %v, want %v", x, y,
				got.RGBAAt(x, y), want.RGBAAt(x, y)), fmt.Sprintf(msg, args...))
		}
	}
	return true
}
