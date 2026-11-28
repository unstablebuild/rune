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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/component/gshader"
)

func kage(decls string) string {
	return "//kage:unit pixels\n\npackage main\n\n" + decls +
		"\n\nfunc Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\treturn vec4(0)\n}\n"
}

func TestNewProgram(t *testing.T) {
	tests := []struct {
		name         string
		src          string
		wantUniforms map[string]string
		wantErr      string
	}{
		{name: "no uniforms", src: kage(""), wantUniforms: map[string]string{}},
		{
			name: "built-in and user uniforms",
			src: kage("var Progress float\nvar Resolution vec2\n" +
				"var (\n\tTint vec4\n\tStops [4]vec4\n)\nvar A, B float\nvar private float"),
			wantUniforms: map[string]string{
				"Progress": "float", "Resolution": "vec2",
				"Tint": "vec4", "Stops": "[4]vec4", "A": "float", "B": "float",
			},
		},
		{
			name:    "missing directive",
			src:     "package main\n\nfunc Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\treturn vec4(0)\n}\n",
			wantErr: "t.kage: missing the //kage:unit pixels directive",
		},
		{
			name:    "directive after the package clause",
			src:     "package main\n\n//kage:unit pixels\n\nfunc Fragment(dstPos vec4, srcPos vec2, color vec4) vec4 {\n\treturn vec4(0)\n}\n",
			wantErr: "missing the //kage:unit pixels directive",
		},
		{
			name:    "missing Fragment",
			src:     "//kage:unit pixels\n\npackage main\n\nfunc fragment() {}\n",
			wantErr: "t.kage: missing func Fragment",
		},
		{
			name:    "syntax error",
			src:     "//kage:unit pixels\n\npackage main\n\nfunc Fragment( {",
			wantErr: "t.kage:5",
		},
		{
			name:    "built-in uniform of another type",
			src:     kage("var Progress vec2"),
			wantErr: "t.kage: uniform Progress must be a float",
		},
		{
			name:    "built-in colour of another type",
			src:     kage("var (\n\tBackground vec3\n)"),
			wantErr: "uniform Background must be a vec4",
		},
		{
			name:    "uniform without a type",
			src:     kage("var Tint = vec4(1)"),
			wantErr: "uniform Tint must declare its type",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := gshader.NewProgram("t.kage", []byte(tt.src))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				assert.Nil(t, p)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "t.kage", p.Name())
			assert.Equal(t, tt.src, string(p.Source()))
			assert.Equal(t, tt.wantUniforms, p.Uniforms())
			for name := range tt.wantUniforms {
				assert.True(t, p.Declares(name), name)
			}
			assert.False(t, p.Declares("private"))
		})
	}
}

func TestMustProgramPanicsOnError(t *testing.T) {
	assert.Panics(t, func() { gshader.MustProgram("bad", []byte("package main")) })
	assert.NotPanics(t, func() { gshader.MustProgram("good", []byte(kage(""))) })
}
