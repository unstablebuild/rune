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

package shader_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/shader"
	"unstable.build/rune/internal/component/shader/shadertest"
)

func TestPulseLeavesCycleBoundariesUntouched(t *testing.T) {
	t.Parallel()
	defaultAttr := term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack}
	in := [][]term.Cell{{
		term.NewCell('x', 0, term.Attributes{Fg: term.NewRGBColor(10, 20, 30), Bg: term.NewRGBColor(40, 50, 60)}),
	}}
	want := [][]term.Cell{{
		term.NewCell('x', 0, term.Attributes{Fg: term.NewRGBColor(10, 20, 30), Bg: term.NewRGBColor(40, 50, 60)}),
	}}

	params := shader.DefaultPulseParams()
	params.Pulses = 2
	sh := shader.Pulse(params, defaultAttr)
	sh.Shade(0, 1000, in)
	assert.Equal(t, want, in, "Pulse must not mutate cells at frame=0")

	sh.Shade(500, 1000, in)
	assert.Equal(t, want, in,
		"Pulse must not mutate cells at cycle boundary "+
			"(frame == total/Pulses)")
}

func TestPulsePeakBlendsTowardColor(t *testing.T) {
	t.Parallel()
	defaultAttr := term.Attributes{Fg: term.ColorWhite, Bg: term.ColorBlack}
	in := [][]term.Cell{{
		term.NewCell('x', 0, term.Attributes{
			Fg: term.NewRGBColor(10, 10, 10),
			Bg: term.NewRGBColor(20, 20, 20),
		}),
	}}
	params := shader.DefaultPulseParams()
	params.Color = term.NewRGBColor(255, 0, 0)
	params.Intensity = 1.0
	shader.Pulse(params, defaultAttr).Shade(500, 1000, in)

	got := uint32(in[0][0].Fg) & 0x00FFFFFF
	r := int(got >> 16 & 0xff)
	assert.Greater(t, r, 10,
		"at peak intensity Fg.R should move toward Color.R")
}

func TestPulseSkipsCellsOutsideAnimation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		frame, total int
	}{
		{"before", -1, 1000},
		{"at the end", 1000, 1000},
		{"after", 1001, 1000},
		{"without frames", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := [][]term.Cell{{
				term.NewCell('x', 0, term.Attributes{Fg: term.ColorRed}),
			}}
			want := [][]term.Cell{{
				term.NewCell('x', 0, term.Attributes{Fg: term.ColorRed}),
			}}
			shader.Pulse(shader.DefaultPulseParams(), term.Attributes{}).Shade(tc.frame, tc.total, in)
			assert.Equal(t, want, in)
		})
	}
}

func TestPulseInvalidPulsesFallBack(t *testing.T) {
	t.Parallel()
	in := shadertest.MakeTextCanvas(6, 3)
	for _, pulses := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		p := shader.DefaultPulseParams()
		p.Pulses = pulses
		for frame := range 20 {
			want, got := term.CloneCells(in), term.CloneCells(in)
			shader.Pulse(shader.DefaultPulseParams(), term.Attributes{}).Shade(frame, 20, want)
			shader.Pulse(p, term.Attributes{}).Shade(frame, 20, got)
			assert.Equal(t, want, got, "Pulses %v frame %d", pulses, frame)
		}
	}
}

func TestPulseDependsOnProgressOnly(t *testing.T) {
	build := func() shader.Shader {
		return shader.Pulse(shader.DefaultPulseParams(), term.Attributes{})
	}
	shadertest.AssertProgressOnly(t, build, shadertest.MakeTextCanvas(6, 3))
	shadertest.AssertOrderIndependent(t, build, shadertest.MakeTextCanvas(6, 3), 20)
}
