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

package shaderdevicetest_test

import (
	"image"
	"testing"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/component/gshader"
	"unstable.build/rune/internal/term/gui/shaderdevice/shaderdevicetest"
)

const wavesSrc = `//kage:unit pixels

package main

var Progress float
var Resolution vec2

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	v := 0.5 + 0.5*sin(6.2831853*(Progress+dstPos.x/Resolution.x))
	return vec4(v, v, v, 1)
}
`

const rampSrc = `//kage:unit pixels

package main

var Progress float

func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {
	return vec4(Progress, Progress, Progress, 1)
}
`

// recorder is a testing.TB that records failures rather than failing.
type recorder struct {
	testing.TB
	failed bool
}

func (r *recorder) Errorf(string, ...any) { r.failed = true }

// counter is a shader that keeps state: each Shade is brighter.
type counter struct {
	p *gshader.Program
	n int
}

func (c *counter) Shade(int, int, gshader.Node) gshader.Node {
	c.n++
	return gshader.Run(c.p, gshader.Uniforms{gshader.UniformProgress: float32(c.n) / 10})
}

type assertion func(testing.TB, gshader.Shader, int, image.Point) bool

var assertions = []struct {
	name   string
	assert assertion
}{
	{name: "deterministic", assert: shaderdevicetest.AssertDeterministic},
	{name: "order independent", assert: shaderdevicetest.AssertOrderIndependent},
	{name: "seamless", assert: shaderdevicetest.AssertSeamless},
}

func TestAssertionsPass(t *testing.T) {
	waves := gshader.FromProgram(gshader.MustProgram("waves.kage", []byte(wavesSrc)), nil)
	for _, a := range assertions {
		t.Run(a.name, func(t *testing.T) {
			assert.True(t, a.assert(t, waves, 6, image.Pt(4, 2)))
		})
	}
}

func TestAssertionsFail(t *testing.T) {
	ramp := gshader.MustProgram("ramp.kage", []byte(rampSrc))
	broken := gshader.MustProgram("broken.kage", []byte("//kage:unit pixels\n\npackage main\n\n"+
		"func Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\treturn undefined\n}\n"))
	tests := []struct {
		name string
		sh   func() gshader.Shader
		gpu  bool
		want map[string]bool
	}{
		{
			name: "a program that does not compile",
			sh:   func() gshader.Shader { return gshader.FromProgram(broken, nil) },
			want: map[string]bool{"deterministic": true, "order independent": true, "seamless": true},
		},
		{
			name: "a shader that keeps state",
			sh:   func() gshader.Shader { return &counter{p: ramp} },
			// Its frames all look alike without a GPU.
			want: map[string]bool{"deterministic": true, "order independent": true, "seamless": shaderdevicetest.GPU()},
		},
		{
			name: "a run that does not end where it starts",
			sh:   func() gshader.Shader { return gshader.FromProgram(ramp, nil) },
			gpu:  true,
			want: map[string]bool{"seamless": true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.gpu {
				shaderdevicetest.RequireGPU(t)
			}
			for _, a := range assertions {
				r := &recorder{TB: t}
				ok := a.assert(r, tt.sh(), 6, image.Pt(4, 2))
				assert.Equal(t, tt.want[a.name], r.failed, a.name)
				assert.Equal(t, !r.failed, ok, a.name)
			}
		})
	}
}
