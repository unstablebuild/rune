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
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"maps"
	"strings"
)

// pixelsDirective selects the pixel unit, in which srcPos and dstPos are
// pixels, which every program must use.
const pixelsDirective = "//kage:unit pixels"

// Program is the Kage source of a fragment shader.
type Program struct {
	name string
	src  []byte
	// uniforms are the Kage types of the uniforms the source declares,
	// by name.
	uniforms map[string]string
}

// NewProgram returns the program of src, named name in errors. It checks
// what it can without a GPU: that src is valid Go syntax, has the
// //kage:unit pixels directive, declares func Fragment and declares the
// built-in uniforms it uses (see [BuiltinUniformType]) with their types.
// Other Kage errors are only reported by the device that compiles it.
func NewProgram(name string, src []byte) (*Program, error) {
	f, err := parser.ParseFile(token.NewFileSet(), name, src,
		parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	if !hasPixelsDirective(f) {
		return nil, fmt.Errorf("%s: missing the %s directive", name, pixelsDirective)
	}
	p := &Program{name: name, src: src, uniforms: map[string]string{}}
	hasFragment := false
	for _, decl := range f.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			if decl.Recv == nil && decl.Name.Name == "Fragment" {
				hasFragment = true
			}
		case *ast.GenDecl:
			if decl.Tok != token.VAR {
				continue
			}
			for _, spec := range decl.Specs {
				if err := p.declare(spec.(*ast.ValueSpec)); err != nil {
					return nil, err
				}
			}
		}
	}
	if !hasFragment {
		return nil, fmt.Errorf("%s: missing func Fragment", name)
	}
	return p, nil
}

// MustProgram is NewProgram for sources known to be valid, such as
// embedded built-ins. It panics on error.
func MustProgram(name string, src []byte) *Program {
	p, err := NewProgram(name, src)
	if err != nil {
		panic(err)
	}
	return p
}

// declare records the uniforms vs declares, which Kage takes to be its
// exported top-level variables.
func (p *Program) declare(vs *ast.ValueSpec) error {
	for _, name := range vs.Names {
		if !name.IsExported() {
			continue
		}
		if vs.Type == nil {
			return fmt.Errorf("%s: uniform %s must declare its type", p.name, name.Name)
		}
		typ := types.ExprString(vs.Type)
		if want, ok := builtinUniformTypes[name.Name]; ok && typ != want {
			return fmt.Errorf("%s: uniform %s must be a %s", p.name, name.Name, want)
		}
		p.uniforms[name.Name] = typ
	}
	return nil
}

func hasPixelsDirective(f *ast.File) bool {
	for _, group := range f.Comments {
		if group.Pos() > f.Package {
			return false
		}
		for _, c := range group.List {
			if strings.TrimSpace(c.Text) == pixelsDirective {
				return true
			}
		}
	}
	return false
}

// Name returns the name p was made with.
func (p *Program) Name() string { return p.name }

// Source returns the Kage source of p, which must not be modified.
func (p *Program) Source() []byte { return p.src }

// Uniforms returns the Kage types of the uniforms p declares, by name.
func (p *Program) Uniforms() map[string]string { return maps.Clone(p.uniforms) }

// Declares reports whether p declares the uniform name.
func (p *Program) Declares(name string) bool {
	_, ok := p.uniforms[name]
	return ok
}
