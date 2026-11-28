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

// Package gshadertest provides a fake gshader.Device and helpers to check
// the node trees shaders return.
package gshadertest

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/gshader"
)

// Call is a recorded Device.Render.
type Call struct {
	Key  term.ImageID
	Size image.Point
	Src  *gshader.Snapshot
	Node gshader.Node
}

// Device is a gshader.Device that records its calls and renders every tree
// as a solid picture of Color, CellSize pixels to a cell. Like a real
// device, it returns the same Frame for the same key and size with an
// Equal tree, and a new Version otherwise.
type Device struct {
	// Err, when set, is returned by every Render.
	Err error
	// Unsupported makes every Render with a Snapshot return
	// gshader.ErrUnsupported, as a device that cannot render cells does.
	Unsupported bool
	// Color is the colour of the pictures.
	Color color.RGBA
	// CellSize is the size of a cell in pixels; zero is 1x1.
	CellSize image.Point
	// Calls are the Renders, in order.
	Calls []Call

	targets map[targetKey]*target
}

type targetKey struct {
	key  term.ImageID
	size image.Point
}

type target struct {
	frame gshader.Frame
	node  gshader.Node
}

var _ gshader.Device = (*Device)(nil)

// Render satisfies gshader.Device.
func (d *Device) Render(
	key term.ImageID, size image.Point, src *gshader.Snapshot, n gshader.Node,
) (gshader.Frame, error) {
	d.Calls = append(d.Calls, Call{Key: key, Size: size, Src: src, Node: n})
	if d.Err != nil {
		return gshader.Frame{}, d.Err
	}
	if d.Unsupported && src != nil {
		return gshader.Frame{}, gshader.ErrUnsupported
	}
	if d.targets == nil {
		d.targets = make(map[targetKey]*target)
	}
	k := targetKey{key: key, size: size}
	t, ok := d.targets[k]
	if ok && gshader.Equal(t.node, n) {
		return t.frame, nil
	}
	if !ok {
		t = &target{frame: gshader.Frame{ID: term.NewImageID()}}
		d.targets[k] = t
	}
	cs := d.CellSize
	if cs == (image.Point{}) {
		cs = image.Pt(1, 1)
	}
	img := image.NewRGBA(image.Rect(0, 0, size.X*cs.X, size.Y*cs.Y))
	draw.Draw(img, img.Rect, image.NewUniform(d.Color), image.Point{}, draw.Src)
	t.node = n
	t.frame.Src = img
	t.frame.Version++
	return t.frame, nil
}

// Context returns ctx carrying d and a cell of d.CellSize pixels.
func (d *Device) Context(ctx context.Context) context.Context {
	cs := d.CellSize
	if cs == (image.Point{}) {
		cs = image.Pt(1, 1)
	}
	ctx = cell.ContextWithPixelSize(ctx, cell.PixelSize{Width: float64(cs.X), Height: float64(cs.Y)})
	return gshader.ContextWithDevice(ctx, d)
}

// Writer is a term.Writer that records the cells and placements drawn on
// it, in the context Ctx.
type Writer struct {
	term.NoopWriter
	Ctx context.Context
	// RejectImages makes DrawImage report false, as a writer that draws no
	// pictures does.
	RejectImages bool
	Images       []term.Image
	Cells        map[term.Coordinates]term.Cell
}

// Context satisfies term.Writer.
func (w *Writer) Context() context.Context {
	if w.Ctx == nil {
		return context.Background()
	}
	return w.Ctx
}

// SetCell satisfies term.Writer.
func (w *Writer) SetCell(pos term.Coordinates, c term.Cell) {
	if w.Cells == nil {
		w.Cells = make(map[term.Coordinates]term.Cell)
	}
	w.Cells[pos] = c
}

// DrawImage satisfies term.Writer.
func (w *Writer) DrawImage(img term.Image) bool {
	if w.RejectImages {
		return false
	}
	w.Images = append(w.Images, img)
	return true
}

// Reset forgets what was drawn, as a writer's Clear does.
func (w *Writer) Reset() {
	w.Images = nil
	w.Cells = nil
}

// Format returns n as an indented tree, one node per line, for messages.
func Format(n gshader.Node) string {
	var b strings.Builder
	format(&b, n, 0)
	return b.String()
}

func format(b *strings.Builder, n gshader.Node, depth int) {
	b.WriteString(strings.Repeat("  ", depth))
	switch n := n.(type) {
	case nil:
		b.WriteString("<nil>\n")
	case gshader.SourceNode:
		b.WriteString("Source\n")
	case gshader.SolidNode:
		fmt.Fprintf(b, "Solid(%v)\n", n.Color)
	case gshader.PictureNode:
		fmt.Fprintf(b, "Picture(id=%d version=%d fit=%d)\n", n.ID, n.Version, n.Fit)
	case gshader.RunNode:
		name := "<nil>"
		if n.Program != nil {
			name = n.Program.Name()
		}
		fmt.Fprintf(b, "Run(%s %v)\n", name, map[string]any(n.Uniforms))
		for _, in := range n.Inputs {
			format(b, in, depth+1)
		}
	case gshader.CropNode:
		fmt.Fprintf(b, "Crop(%v)\n", n.Rect)
		format(b, n.Node, depth+1)
	case gshader.PlaceNode:
		fmt.Fprintf(b, "Place(%v)\n", n.Rect)
		format(b, n.Over, depth+1)
		format(b, n.Under, depth+1)
	default:
		fmt.Fprintf(b, "%T\n", n)
	}
}

// AssertEqual asserts that got is gshader.Equal to want, printing both
// trees when it is not.
func AssertEqual(t testing.TB, want, got gshader.Node, msgAndArgs ...any) bool {
	t.Helper()
	if gshader.Equal(want, got) {
		return true
	}
	return assert.Fail(t, fmt.Sprintf("trees differ:\nwant:\n%sgot:\n%s", Format(want), Format(got)),
		msgAndArgs...)
}

// Uniform returns the value of the uniform name of the RunNode n, failing
// t when n is not one.
func Uniform(t testing.TB, n gshader.Node, name string) any {
	t.Helper()
	run, ok := n.(gshader.RunNode)
	if !ok {
		t.Fatalf("not a RunNode:\n%s", Format(n))
	}
	return run.Uniforms[name]
}
