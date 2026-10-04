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

package shaderutils

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/unstablebuild/rune-go-sdk/term"
)

func TestInterpolateColor(t *testing.T) {
	tsuite := []struct {
		name                string
		colA                term.Color
		colB                term.Color
		resolveColorDefault term.Color
		factor              float64
		expectRGB           []int32
	}{
		{
			name:      "begins with colA",
			colA:      term.NewRGBColor(0, 10, 100),
			colB:      term.NewRGBColor(10, 0, 200),
			factor:    0.0,
			expectRGB: []int32{0, 10, 100},
		},
		{
			name:      "ends with colA",
			colA:      term.NewRGBColor(0, 10, 100),
			colB:      term.NewRGBColor(10, 0, 200),
			factor:    1.0,
			expectRGB: []int32{10, 0, 200},
		},
		{
			name:      "linearly interpolates each channel",
			colA:      term.NewRGBColor(0, 10, 100),
			colB:      term.NewRGBColor(10, 0, 200),
			factor:    0.2,
			expectRGB: []int32{2, 8, 120},
		},
		{
			name:      "negative factor",
			colA:      term.NewRGBColor(0, 10, 100),
			colB:      term.NewRGBColor(10, 0, 200),
			factor:    -0.5,
			expectRGB: []int32{251, 15, 50},
		},
		{
			name:      "factor over 1",
			colA:      term.NewRGBColor(0, 10, 100),
			colB:      term.NewRGBColor(10, 0, 200),
			factor:    1.5,
			expectRGB: []int32{15, 251, 250},
		},
		{
			name:                "interpolating default color",
			colA:                term.ColorDefault,
			colB:                term.NewRGBColor(0, 0, 0),
			factor:              0.5,
			resolveColorDefault: term.NewRGBColor(200, 0, 0),
			expectRGB:           []int32{100, 0, 0},
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			res := InterpolateColor(
				tcase.factor, tcase.colA, tcase.colB, tcase.resolveColorDefault,
			)
			r, g, b := res.RGB()
			assert.Equal(t, tcase.expectRGB, []int32{r, g, b})
		})
	}
}

func TestInterpolateColorPreservesIdentityWhenColorAndTargetMatch(t *testing.T) {
	tsuite := []struct {
		name   string
		color  term.Color
		factor float64
	}{
		{name: "named color blue", color: term.ColorBlue, factor: 0.5},
		{name: "named color red at factor 0", color: term.ColorRed, factor: 0.0},
		{name: "named color green at factor 1", color: term.ColorGreen, factor: 1.0},
		{name: "default color preserved", color: term.ColorDefault, factor: 0.5},
		{name: "rgb color round-trips", color: term.NewRGBColor(12, 34, 56), factor: 0.7},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			res := InterpolateColor(
				tcase.factor, tcase.color, tcase.color, term.ColorDefault,
			)
			assert.Equal(t, tcase.color, res)
		})
	}
}

func TestInterpolateColorWithUnresolvableDefault(t *testing.T) {
	tsuite := []struct {
		name   string
		factor float64
	}{
		{name: "factor 0", factor: 0.0},
		{name: "factor near 0 (would yield dark gray)", factor: 0.1},
		{name: "factor 0.5 (would yield mid gray)", factor: 0.5},
		{name: "factor 1", factor: 1.0},
	}
	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			res := InterpolateColor(
				tcase.factor,
				term.ColorDefault,
				term.NewRGBColor(255, 255, 255),
				term.ColorDefault, // also unresolvable
			)
			assert.Equal(t, term.ColorDefault, res)
		})
	}
}

func TestColorBrightness(t *testing.T) {
	tsuite := []struct {
		name                string
		col                 term.Color
		resolveColorDefault term.Color
		expect              float64
	}{
		{
			name:   "black",
			col:    term.NewRGBColor(0, 0, 0),
			expect: 0.0,
		},
		{
			name:   "white",
			col:    term.NewRGBColor(255, 255, 255),
			expect: 1.0,
		},
		{
			name:   "high red",
			col:    term.NewRGBColor(255, 10, 10),
			expect: 0.35947712418300654,
		},
		{
			name:   "high green",
			col:    term.NewRGBColor(10, 255, 10),
			expect: 0.35947712418300654,
		},
		{
			name:   "high blue",
			col:    term.NewRGBColor(10, 10, 255),
			expect: 0.35947712418300654,
		},
		{
			name:   "negative values should not panic",
			col:    term.NewRGBColor(-10, -10, -255),
			expect: 0.6444444444444445, // strange result but can stimulate creativity
		},
		{
			name:                "default color",
			col:                 term.ColorDefault,
			resolveColorDefault: term.NewRGBColor(255, 255, 255),
			expect:              1.0,
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			res := ColorBrightness(tcase.col, tcase.resolveColorDefault)
			assert.Equal(t, res, tcase.expect)
		})
	}
}

func TestSampleGradient(t *testing.T) {
	tsuite := []struct {
		name     string
		factor   float64
		gradient []term.Color
		expect   term.Color
	}{
		{
			name:   "start",
			factor: 0.0,
			gradient: []term.Color{
				term.NewRGBColor(255, 0, 0),
				term.NewRGBColor(0, 255, 0),
				term.NewRGBColor(0, 0, 255),
			},
			expect: term.NewRGBColor(255, 0, 0),
		},
		{
			name:   "inbetween, first half",
			factor: 0.22,
			gradient: []term.Color{
				term.NewRGBColor(255, 0, 0),
				term.NewRGBColor(0, 255, 0),
				term.NewRGBColor(0, 0, 255),
			},
			expect: term.NewRGBColor(142, 112, 0),
		},
		{
			name:   "inbetween, middle",
			factor: 0.5,
			gradient: []term.Color{
				term.NewRGBColor(255, 0, 0),
				term.NewRGBColor(0, 255, 0),
				term.NewRGBColor(0, 0, 255),
			},
			expect: term.NewRGBColor(0, 255, 0),
		},
		{
			name:   "inbetween second half",
			factor: 0.864,
			gradient: []term.Color{
				term.NewRGBColor(255, 0, 0),
				term.NewRGBColor(0, 255, 0),
				term.NewRGBColor(0, 0, 255),
			},
			expect: term.NewRGBColor(0, 69, 185),
		},
		{
			name:   "end",
			factor: 0.0,
			gradient: []term.Color{
				term.NewRGBColor(255, 0, 0),
				term.NewRGBColor(0, 255, 0),
				term.NewRGBColor(0, 0, 255),
			},
			expect: term.NewRGBColor(255, 0, 0),
		},
		{
			name:   "beyond start",
			factor: -0.5,
			gradient: []term.Color{
				term.NewRGBColor(255, 0, 0),
				term.NewRGBColor(0, 255, 0),
				term.NewRGBColor(0, 0, 255),
			},
			expect: term.NewRGBColor(255, 0, 0),
		},
		{
			name:   "beyond end",
			factor: 1.5,
			gradient: []term.Color{
				term.NewRGBColor(255, 0, 0),
				term.NewRGBColor(0, 255, 0),
				term.NewRGBColor(0, 0, 255),
			},
			expect: term.NewRGBColor(0, 0, 255),
		},
		{
			name:     "empty gradient",
			factor:   0.5,
			gradient: []term.Color{},
			expect:   term.ColorDefault,
		},
		{
			name:     "nil gradient",
			factor:   0.5,
			gradient: nil,
			expect:   term.ColorDefault,
		},
		{
			// Shader knobs can divide by a zero the caller cannot
			// rule out, so a NaN factor reaches here. Converting it
			// to an index is implementation-defined: amd64 yields
			// MinInt64 and indexes out of range.
			name:   "NaN factor",
			factor: math.NaN(),
			gradient: []term.Color{
				term.NewRGBColor(255, 0, 0),
				term.NewRGBColor(0, 255, 0),
				term.NewRGBColor(0, 0, 255),
			},
			expect: term.NewRGBColor(255, 0, 0),
		},
	}

	for _, tcase := range tsuite {
		t.Run(tcase.name, func(t *testing.T) {
			result := SampleGradient(tcase.factor, tcase.gradient)
			assert.Equal(t, tcase.expect, result)
		})
	}
}

func TestDesaturateColor(t *testing.T) {
	// amount=0: passthrough.
	c := term.NewRGBColor(200, 50, 10)
	assert.Equal(t, c, DesaturateColor(c, 0, term.ColorDefault))

	// amount=1: each cell collapses to its own luminance gray, not a
	// fixed shared gray. Cells with different RGB stay distinct.
	red := DesaturateColor(term.NewRGBColor(255, 0, 0), 1, term.ColorDefault)
	green := DesaturateColor(term.NewRGBColor(0, 255, 0), 1, term.ColorDefault)
	rr, rg, rb := red.RGB()
	gr, gg, gb := green.RGB()
	assert.Equal(t, rr, rg)
	assert.Equal(t, rr, rb)
	assert.Equal(t, gr, gg)
	assert.Equal(t, gr, gb)
	assert.NotEqual(t, rr, gr, "different sources should not collapse to the same gray")

	// Amount clamped above 1 behaves like amount=1.
	clamped := DesaturateColor(term.NewRGBColor(255, 0, 0), 2, term.ColorDefault)
	assert.Equal(t, red, clamped)

	// ColorDefault is resolved before computing luminance.
	resolved := DesaturateColor(term.ColorDefault, 1, term.NewRGBColor(255, 255, 255))
	r, g, b := resolved.RGB()
	assert.Equal(t, int32(255), r)
	assert.Equal(t, int32(255), g)
	assert.Equal(t, int32(255), b)

	// Unresolvable defaults are returned unchanged so the terminal keeps
	// rendering them natively.
	assert.Equal(t,
		term.ColorDefault,
		DesaturateColor(term.ColorDefault, 1, term.ColorDefault),
	)
}
