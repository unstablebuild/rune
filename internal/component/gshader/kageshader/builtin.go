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

package kageshader

import (
	"bytes"
	_ "embed"
	"maps"
	"slices"

	"unstable.build/rune/internal/component/gshader"
)

//go:embed noise.kage.inc
var noiseInc []byte

// Builtin is a shader of the library as configuration knows it: its
// program, and the uniforms of its default parameters to run it with.
type Builtin struct {
	Program  *gshader.Program
	Uniforms gshader.Uniforms
}

var builtins = map[string]func() Builtin{
	"gradient": func() Builtin { return Builtin{gradientProgram, DefaultGradientParams().uniforms()} },
	"noise":    func() Builtin { return Builtin{noiseProgram, DefaultNoiseParams().uniforms()} },
	"plasma":   func() Builtin { return Builtin{plasmaProgram, DefaultPlasmaParams().uniforms()} },
	"blaze":    func() Builtin { return Builtin{blazeProgram, DefaultBlazeParams().uniforms()} },
	"inferno":  func() Builtin { return Builtin{infernoProgram, DefaultInfernoParams().uniforms()} },
}

// Names returns the names of the built-in shaders, sorted.
func Names() []string {
	return slices.Sorted(maps.Keys(builtins))
}

// Lookup returns the built-in shader called name, with uniforms of its
// own for the caller to change.
func Lookup(name string) (Builtin, bool) {
	b, ok := builtins[name]
	if !ok {
		return Builtin{}, false
	}
	return b(), true
}

// program returns the built-in program name, made of srcs one after the
// other.
func program(name string, srcs ...[]byte) *gshader.Program {
	return gshader.MustProgram(name, bytes.Join(srcs, []byte("\n")))
}

func flag(b bool) float32 {
	if b {
		return 1
	}
	return 0
}
