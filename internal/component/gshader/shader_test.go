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

package gshader_test

import (
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/component/gshader/gshadertest"
)

func TestEqual(t *testing.T) {
	p := gshader.MustProgram("p", []byte(kage("")))
	q := gshader.MustProgram("p", []byte(kage("")))
	pic := image.NewRGBA(image.Rect(0, 0, 1, 1))
	id := term.NewImageID()
	r := image.Rect(1, 1, 3, 3)
	tests := []struct {
		name string
		a, b gshader.Node
		want bool
	}{
		{name: "nils", want: true},
		{name: "nil and source", b: gshader.Source()},
		{name: "sources", a: gshader.Source(), b: gshader.Source(), want: true},
		{name: "solids", a: gshader.Solid(term.ColorRed), b: gshader.Solid(term.ColorRed), want: true},
		{name: "different solids", a: gshader.Solid(term.ColorRed), b: gshader.Solid(term.ColorBlue)},
		{name: "source and solid", a: gshader.Source(), b: gshader.Solid(term.ColorDefault)},
		{
			name: "pictures by id and version, not pixels",
			a:    gshader.Picture(pic, id, 1, term.ImageFitFill),
			b:    gshader.Picture(image.NewRGBA(image.Rect(0, 0, 2, 2)), id, 1, term.ImageFitFill),
			want: true,
		},
		{name: "picture versions", a: gshader.Picture(pic, id, 1, term.ImageFitFill), b: gshader.Picture(pic, id, 2, term.ImageFitFill)},
		{name: "picture ids", a: gshader.Picture(pic, id, 1, term.ImageFitFill), b: gshader.Picture(pic, id+1, 1, term.ImageFitFill)},
		{name: "picture fits", a: gshader.Picture(pic, id, 1, term.ImageFitFill), b: gshader.Picture(pic, id, 1, term.ImageFitContain)},
		{
			name: "runs",
			a:    gshader.Run(p, gshader.Uniforms{"A": float32(1)}, gshader.Source()),
			b:    gshader.Run(p, gshader.Uniforms{"A": float32(1)}, gshader.Source()),
			want: true,
		},
		{name: "programs by pointer", a: gshader.Run(p, nil), b: gshader.Run(q, nil)},
		{
			name: "uniform values",
			a:    gshader.Run(p, gshader.Uniforms{"A": float32(1)}),
			b:    gshader.Run(p, gshader.Uniforms{"A": float32(2)}),
		},
		{
			name: "run inputs",
			a:    gshader.Run(p, nil, gshader.Source()),
			b:    gshader.Run(p, nil, gshader.Source(), gshader.Source()),
		},
		{name: "crops", a: gshader.Crop(gshader.Source(), r), b: gshader.Crop(gshader.Source(), r), want: true},
		{name: "crop rects", a: gshader.Crop(gshader.Source(), r), b: gshader.Crop(gshader.Source(), r.Add(image.Pt(1, 0)))},
		{
			name: "nested places",
			a:    gshader.Place(gshader.Run(p, nil, gshader.Crop(gshader.Source(), r)), gshader.Source(), r),
			b:    gshader.Place(gshader.Run(p, nil, gshader.Crop(gshader.Source(), r)), gshader.Source(), r),
			want: true,
		},
		{
			name: "a difference deep down",
			a:    gshader.Place(gshader.Run(p, nil, gshader.Crop(gshader.Source(), r)), gshader.Source(), r),
			b:    gshader.Place(gshader.Run(p, nil, gshader.Crop(gshader.Solid(term.ColorRed), r)), gshader.Source(), r),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, gshader.Equal(tt.a, tt.b))
			assert.Equal(t, tt.want, gshader.Equal(tt.b, tt.a))
		})
	}
}

func TestRunPanicsWithMoreThanFourInputs(t *testing.T) {
	p := gshader.MustProgram("p", []byte(kage("")))
	src := gshader.Source()
	assert.NotPanics(t, func() { gshader.Run(p, nil, src, src, src, src) })
	assert.PanicsWithValue(t, "gshader: p runs over 5 inputs, more than 4", func() {
		gshader.Run(p, nil, src, src, src, src, src)
	})
}

func TestSize(t *testing.T) {
	p := gshader.MustProgram("p", []byte(kage("")))
	area := image.Pt(20, 10)
	r := image.Rect(2, 2, 7, 5)
	tests := []struct {
		name string
		n    gshader.Node
		want image.Point
	}{
		{name: "source", n: gshader.Source(), want: area},
		{name: "solid", n: gshader.Solid(term.ColorRed), want: area},
		{name: "picture", n: gshader.Picture(image.NewRGBA(image.Rect(0, 0, 3, 3)), 1, 1, 0), want: area},
		{name: "run without inputs", n: gshader.Run(p, nil), want: area},
		{name: "run takes its first input's", n: gshader.Run(p, nil, gshader.Crop(gshader.Source(), r), gshader.Source()), want: r.Size()},
		{name: "crop", n: gshader.Crop(gshader.Source(), r), want: r.Size()},
		{name: "place takes under's", n: gshader.Place(gshader.Source(), gshader.Crop(gshader.Source(), r), image.Rect(0, 0, 1, 1)), want: r.Size()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, gshader.Size(tt.n, area))
		})
	}
}

func TestWalkVisitsParentsFirst(t *testing.T) {
	p := gshader.MustProgram("p", []byte(kage("")))
	r := image.Rect(0, 0, 1, 1)
	tree := gshader.Place(gshader.Run(p, nil, gshader.Crop(gshader.Source(), r)), gshader.Solid(term.ColorRed), r)
	var got []string
	gshader.Walk(tree, func(n gshader.Node) {
		got = append(got, gshadertest.Format(n)[:4])
	})
	assert.Equal(t, []string{"Plac", "Run(", "Crop", "Sour", "Soli"}, got)
}

func TestNop(t *testing.T) {
	src := gshader.Solid(term.ColorRed)
	assert.Equal(t, src, gshader.Nop().Shade(3, 10, src))
}

func TestFromProgram(t *testing.T) {
	p := gshader.MustProgram("p", []byte(kage("var Progress float")))
	u := gshader.Uniforms{"Seed": float32(7), "Frame": float32(-1)}
	sh := gshader.FromProgram(p, u)
	tests := []struct {
		name         string
		frame, total int
		want         gshader.Node
	}{
		{
			name: "sets the time over the uniforms", frame: 3, total: 12,
			want: gshader.Run(p, gshader.Uniforms{
				"Seed": float32(7), "Frame": float32(3), "Total": float32(12), "Progress": float32(0.25),
			}, gshader.Source()),
		},
		{
			name: "no progress without frames", frame: 0, total: 0,
			want: gshader.Run(p, gshader.Uniforms{
				"Seed": float32(7), "Frame": float32(0), "Total": float32(0), "Progress": float32(0),
			}, gshader.Source()),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gshadertest.AssertEqual(t, tt.want, sh.Shade(tt.frame, tt.total, gshader.Source()))
		})
	}
	assert.Equal(t, float32(-1), u["Frame"], "the shader's uniforms are left alone")
	gshadertest.AssertEqual(t, sh.Shade(5, 10, gshader.Source()), sh.Shade(5, 10, gshader.Source()),
		"equal arguments make equal trees")
}

// timed returns a shader whose trees record the frame and total it shades
// at, under a program of its own.
func timed(name string) gshader.Shader {
	return gshader.FromProgram(gshader.MustProgram(name, []byte(kage(""))), nil)
}
