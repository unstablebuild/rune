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

// Package gshader runs GPU shaders, written in Kage, over components, or
// over nothing, as generators.
//
// A [Shader] describes a frame as a tree of [Node]s rather than drawing it:
// the writer context of a Draw carries a [Device], which only the GUI
// provides, that evaluates the tree on the GPU and returns its pixels as an
// ordinary picture. Where there is no device, such as in the terminal or in
// an extension process, [Component] draws a fallback component instead.
package gshader

import (
	"fmt"
	"image"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// Shader describes the picture for frame of total made from src, the
// rendering of what is being shaded (transparent for a generator). Shade
// must return Equal trees for equal arguments, and must derive time from
// frame and total alone so time shaders can remap it.
type Shader interface {
	Shade(frame, total int, src Node) Node
}

// Node is a texture, described rather than drawn. Rectangles are in cells,
// relative to the top-left corner of the node they apply to, which for a
// node the size of the shaded area is the shaded area's.
//
// Each node has a size, worked out from the leaves up from the shaded area
// A (see [Size]):
//   - SourceNode, SolidNode and PictureNode are A. A PictureNode is drawn
//     into A with its Fit.
//   - RunNode is the size of its first input, or A when it has none. Inputs
//     of another size are scaled to it.
//   - CropNode is the size of its Rect.
//   - PlaceNode is the size of Under, with Over scaled into Rect.
//
// A root of another size than A is scaled to A.
type Node interface{ isNode() }

// SourceNode stands for the rendering of what is being shaded.
type SourceNode struct{}

// SolidNode is a single colour, which the device resolves, so palette
// colours and ColorDefault, the background, follow the theme.
type SolidNode struct{ Color term.Color }

// PictureNode is a picture. ID identifies its pixels to the device, which
// uploads them again only when Version changes.
type PictureNode struct {
	Src     image.Image
	ID      term.ImageID
	Version uint64
	Fit     term.ImageFit
}

// RunNode is the output of Program run with Uniforms over Inputs, which it
// reads as imageSrc0 to imageSrc3.
type RunNode struct {
	Program  *Program
	Uniforms Uniforms
	Inputs   []Node
}

// CropNode is the part of Node inside Rect.
type CropNode struct {
	Node Node
	Rect image.Rectangle
}

// PlaceNode is Under with Over scaled into Rect, where Over replaces
// Under's pixels, transparent ones included.
type PlaceNode struct {
	Over, Under Node
	Rect        image.Rectangle
}

func (SourceNode) isNode()  {}
func (SolidNode) isNode()   {}
func (PictureNode) isNode() {}
func (RunNode) isNode()     {}
func (CropNode) isNode()    {}
func (PlaceNode) isNode()   {}

// MaxInputs is how many inputs a RunNode reads at most.
const MaxInputs = 4

// Source returns a SourceNode.
func Source() Node { return SourceNode{} }

// Solid returns a SolidNode of c.
func Solid(c term.Color) Node { return SolidNode{Color: c} }

// Picture returns a PictureNode.
func Picture(src image.Image, id term.ImageID, version uint64, fit term.ImageFit) Node {
	return PictureNode{Src: src, ID: id, Version: version, Fit: fit}
}

// Run returns a RunNode of p with u over inputs. It panics with more than
// MaxInputs inputs.
func Run(p *Program, u Uniforms, inputs ...Node) Node {
	if len(inputs) > MaxInputs {
		panic(fmt.Sprintf("gshader: %s runs over %d inputs, more than %d",
			p.Name(), len(inputs), MaxInputs))
	}
	return RunNode{Program: p, Uniforms: u, Inputs: inputs}
}

// Crop returns a CropNode of n inside r.
func Crop(n Node, r image.Rectangle) Node { return CropNode{Node: n, Rect: r} }

// Place returns a PlaceNode of over scaled into r of under.
func Place(over, under Node, r image.Rectangle) Node {
	return PlaceNode{Over: over, Under: under, Rect: r}
}

// Size returns the size in cells of n over a shaded area of area cells.
func Size(n Node, area image.Point) image.Point {
	switch n := n.(type) {
	case RunNode:
		if len(n.Inputs) == 0 {
			return area
		}
		return Size(n.Inputs[0], area)
	case CropNode:
		return n.Rect.Size()
	case PlaceNode:
		return Size(n.Under, area)
	default:
		return area
	}
}

// Equal reports whether a and b describe the same texture: Programs are
// compared by pointer, Pictures by ID, Version and Fit, and Uniforms by
// value.
func Equal(a, b Node) bool {
	switch a := a.(type) {
	case nil:
		return b == nil
	case SourceNode:
		_, ok := b.(SourceNode)
		return ok
	case SolidNode:
		b, ok := b.(SolidNode)
		return ok && a.Color == b.Color
	case PictureNode:
		b, ok := b.(PictureNode)
		return ok && a.ID == b.ID && a.Version == b.Version && a.Fit == b.Fit
	case RunNode:
		b, ok := b.(RunNode)
		return ok && a.Program == b.Program && a.Uniforms.Equal(b.Uniforms) &&
			slices.EqualFunc(a.Inputs, b.Inputs, Equal)
	case CropNode:
		b, ok := b.(CropNode)
		return ok && a.Rect == b.Rect && Equal(a.Node, b.Node)
	case PlaceNode:
		b, ok := b.(PlaceNode)
		return ok && a.Rect == b.Rect && Equal(a.Over, b.Over) && Equal(a.Under, b.Under)
	default:
		return false
	}
}

// Walk calls fn for n and every node under it, parents first.
func Walk(n Node, fn func(Node)) {
	if n == nil {
		return
	}
	fn(n)
	switch n := n.(type) {
	case RunNode:
		for _, in := range n.Inputs {
			Walk(in, fn)
		}
	case CropNode:
		Walk(n.Node, fn)
	case PlaceNode:
		Walk(n.Over, fn)
		Walk(n.Under, fn)
	}
}

type nopShader struct{}

// Nop returns a Shader that returns what it shades.
func Nop() Shader { return nopShader{} }

func (nopShader) Shade(_, _ int, src Node) Node { return src }

type programShader struct {
	p *Program
	u Uniforms
}

// FromProgram returns a Shader that runs p over what it shades, which p
// reads as imageSrc0, with the uniforms of u and, set over them, Frame,
// Total and Progress. A generator need not read imageSrc0.
func FromProgram(p *Program, u Uniforms) Shader {
	return programShader{p: p, u: u}
}

func (s programShader) Shade(frame, total int, src Node) Node {
	u := make(Uniforms, len(s.u)+3)
	for k, v := range s.u {
		u[k] = v
	}
	u[UniformFrame] = float32(frame)
	u[UniformTotal] = float32(total)
	u[UniformProgress] = Progress(frame, total)
	return Run(s.p, u, src)
}

// Progress returns frame / total, or 0 when total is not positive.
func Progress(frame, total int) float32 {
	if total <= 0 {
		return 0
	}
	return float32(frame) / float32(total)
}
