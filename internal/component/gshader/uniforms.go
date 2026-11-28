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

package gshader

import (
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Uniforms are the values of a program's uniforms by name. A value is a
// float32, float64, int, [2]float32, [3]float32, [4]float32, []float32 or
// term.Color, which the device resolves to a premultiplied vec4 so that
// palette colours follow the theme. Values for names the program does not
// declare are ignored.
type Uniforms map[string]any

// The built-in uniforms. FromProgram sets Frame, Total and Progress. The
// device sets the others a program declares and its Uniforms leave unset:
// Resolution, the size in pixels of the node being run, CellSize, the size
// of a cell in pixels, and Background and Foreground, the theme's default
// colours, premultiplied. Seed is a convention for generators, which the
// configuration sets.
const (
	UniformFrame      = "Frame"
	UniformTotal      = "Total"
	UniformProgress   = "Progress"
	UniformResolution = "Resolution"
	UniformCellSize   = "CellSize"
	UniformBackground = "Background"
	UniformForeground = "Foreground"
	UniformSeed       = "Seed"
)

// builtinUniformTypes are the Kage types the built-in uniforms must be
// declared with.
var builtinUniformTypes = map[string]string{
	UniformFrame:      "float",
	UniformTotal:      "float",
	UniformProgress:   "float",
	UniformResolution: "vec2",
	UniformCellSize:   "vec2",
	UniformBackground: "vec4",
	UniformForeground: "vec4",
	UniformSeed:       "float",
}

// BuiltinUniformType returns the Kage type the built-in uniform name must
// be declared with, reporting false when name is not built in.
func BuiltinUniformType(name string) (string, bool) {
	typ, ok := builtinUniformTypes[name]
	return typ, ok
}

// Defaults are the values a device gives the built-in uniforms that a
// program declares and its Uniforms leave unset.
type Defaults struct {
	Resolution, CellSize   [2]float32
	Background, Foreground color.RGBA
}

// Equal reports whether u and v hold the same values.
func (u Uniforms) Equal(v Uniforms) bool {
	return maps.EqualFunc(u, v, uniformValueEqual)
}

func uniformValueEqual(a, b any) bool {
	switch a := a.(type) {
	case []float32:
		b, ok := b.([]float32)
		return ok && slices.Equal(a, b)
	case float32, float64, int, [2]float32, [3]float32, [4]float32, term.Color:
		return a == b
	default:
		return false
	}
}

// Values returns the values of u for the uniforms p declares, in the form
// ebiten takes them: a []float32 for float types and an int for int, with
// the built-in uniforms u leaves unset taken from d, and colours resolved
// by resolve. It returns an error, rather than letting ebiten panic, when a
// value has the wrong type or number of components.
func (u Uniforms) Values(p *Program, d Defaults, resolve func(term.Color) color.RGBA) (map[string]any, error) {
	out := make(map[string]any, len(p.uniforms))
	for name, typ := range p.uniforms {
		v, ok := u[name]
		if !ok {
			if v, ok = d.value(name); !ok {
				continue
			}
		}
		value, err := uniformValue(typ, v, resolve)
		if err != nil {
			return nil, fmt.Errorf("%s: uniform %s: %w", p.Name(), name, err)
		}
		out[name] = value
	}
	return out, nil
}

func (d Defaults) value(name string) (any, bool) {
	switch name {
	case UniformResolution:
		return d.Resolution, true
	case UniformCellSize:
		return d.CellSize, true
	case UniformBackground:
		return premultiplied(d.Background), true
	case UniformForeground:
		return premultiplied(d.Foreground), true
	default:
		return nil, false
	}
}

func premultiplied(c color.RGBA) [4]float32 {
	return [4]float32{
		float32(c.R) / 0xff, float32(c.G) / 0xff, float32(c.B) / 0xff, float32(c.A) / 0xff,
	}
}

// uniformValue converts v for a uniform of Kage type typ.
func uniformValue(typ string, v any, resolve func(term.Color) color.RGBA) (any, error) {
	n, isInt, ok := kageComponents(typ)
	if !ok {
		return nil, fmt.Errorf("unsupported type %s", typ)
	}
	if isInt {
		i, ok := v.(int)
		if !ok || n != 1 {
			return nil, fmt.Errorf("a %s cannot be set to a %T", typ, v)
		}
		return i, nil
	}
	var floats []float32
	switch v := v.(type) {
	case float32:
		floats = []float32{v}
	case float64:
		floats = []float32{float32(v)}
	case int:
		floats = []float32{float32(v)}
	case [2]float32:
		floats = v[:]
	case [3]float32:
		floats = v[:]
	case [4]float32:
		floats = v[:]
	case []float32:
		floats = slices.Clone(v)
	case term.Color:
		if typ != "vec4" {
			return nil, fmt.Errorf("a %s cannot be set to a colour, only a vec4 can", typ)
		}
		c := premultiplied(resolve(v))
		floats = c[:]
	default:
		return nil, fmt.Errorf("a %s cannot be set to a %T", typ, v)
	}
	if len(floats) != n {
		return nil, fmt.Errorf("a %s takes %d values, got %d", typ, n, len(floats))
	}
	return floats, nil
}

// kageComponents returns how many values a uniform of Kage type typ holds
// and whether they are integers, reporting false for types Rune cannot
// set.
func kageComponents(typ string) (n int, isInt, ok bool) {
	if rest, found := strings.CutPrefix(typ, "["); found {
		length, elem, found := strings.Cut(rest, "]")
		count, err := strconv.Atoi(length)
		if !found || err != nil || count < 1 {
			return 0, false, false
		}
		n, isInt, ok := kageComponents(elem)
		return count * n, isInt, ok && !isInt
	}
	switch typ {
	case "float":
		return 1, false, true
	case "vec2":
		return 2, false, true
	case "vec3":
		return 3, false, true
	case "vec4", "mat2":
		return 4, false, true
	case "mat3":
		return 9, false, true
	case "mat4":
		return 16, false, true
	case "int":
		return 1, true, true
	default:
		return 0, false, false
	}
}
