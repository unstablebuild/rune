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

// Package shaderdevice evaluates gshader node trees on the GPU with ebiten.
package shaderdevice

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"slices"

	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component/gshader"
)

// maxImageSize bounds each side of the textures a Device allocates, which
// the graphics drivers ebiten supports all allow.
const maxImageSize = 16384

var errOutsideDraw = errors.New("shaderdevice: Render outside a draw")

// Theme is what a Device resolves colours with. Colours are premultiplied.
type Theme struct {
	// Background and Foreground are the default colours, which the
	// built-in uniforms of the same names take.
	Background, Foreground color.RGBA
	// Resolve returns the colour of c. Nil resolves ColorDefault to
	// Background and other colours to their opaque RGB.
	Resolve func(term.Color) color.RGBA
}

// Device is a gshader.Device that renders on the GPU. It must be used on
// the goroutine that draws with ebiten, and Render only between Begin and
// End.
type Device struct {
	px     cell.PixelSize
	theme  Theme
	gen    uint64
	active bool
	cells  CellRenderer
	// source holds the rendering of the Snapshot of the Render under way,
	// nil when it has none.
	source *ebiten.Image

	targets  map[targetKey]*target
	programs map[*gshader.Program]*program
	pictures map[term.ImageID]*picture
	// scratch are the textures nodes are evaluated into, free to acquire,
	// by size.
	scratch map[image.Point][]*scratchTexture

	stats stats
}

// CellRenderer draws cells, and the pictures placed over them, into all of
// dst as the window draws them, over its background.
type CellRenderer func(dst *ebiten.Image, cells [][]term.Cell, images []term.Image)

// stats count the work a Device did, for tests.
type stats struct {
	compiles, renders, uploads, passes, sources int
}

type targetKey struct {
	key  term.ImageID
	size image.Point
}

// target is the texture a key renders into at a size.
type target struct {
	img     *ebiten.Image
	out     *Image
	id      term.ImageID
	version uint64
	// tree and gen are what img holds a rendering of; tree is nil when it
	// holds none.
	tree gshader.Node
	gen  uint64
	// hasSource, cells and images are the Snapshot it was rendered from,
	// with pictures kept without their pixels.
	hasSource bool
	cells     [][]term.Cell
	images    []term.Image
	used      bool
}

type program struct {
	shader *ebiten.Shader
	err    error
	used   bool
}

type picture struct {
	img     *ebiten.Image
	version uint64
	bounds  image.Rectangle
	used    bool
}

type scratchTexture struct {
	img *ebiten.Image
	// busy is set while acquired, and used once acquired since Begin.
	busy, used bool
}

var _ gshader.Device = (*Device)(nil)

// New returns a Device with nothing allocated, that renders Snapshots with
// cells. A nil cells renders none.
func New(cells CellRenderer) *Device {
	return &Device{
		cells:    cells,
		targets:  make(map[targetKey]*target),
		programs: make(map[*gshader.Program]*program),
		pictures: make(map[term.ImageID]*picture),
		scratch:  make(map[image.Point][]*scratchTexture),
	}
}

// Begin starts a draw on cells of px pixels, in theme. A change of px or of
// the theme's default colours re-renders every tree.
func (d *Device) Begin(px cell.PixelSize, theme Theme) {
	if px != d.px || theme.Background != d.theme.Background ||
		theme.Foreground != d.theme.Foreground {
		d.gen++
	}
	d.px, d.theme = px, theme
	d.active = true
}

// Invalidate re-renders every tree, for changes Begin cannot see, such as
// to the palette Theme.Resolve resolves.
func (d *Device) Invalidate() { d.gen++ }

// End ends a draw. It frees the renderings, programs and pictures that no
// Render used since Begin.
func (d *Device) End() {
	d.active = false
	for k, t := range d.targets {
		if t.used {
			t.used = false
			continue
		}
		t.dispose()
		delete(d.targets, k)
	}
	for k, p := range d.programs {
		if p.used {
			p.used = false
			continue
		}
		p.dispose()
		delete(d.programs, k)
	}
	for id, p := range d.pictures {
		if p.used {
			p.used = false
			continue
		}
		p.img.Deallocate()
		delete(d.pictures, id)
	}
	for size, free := range d.scratch {
		kept := free[:0]
		for _, s := range free {
			if s.used {
				s.used = false
				kept = append(kept, s)
				continue
			}
			s.img.Deallocate()
		}
		clear(free[len(kept):])
		if len(kept) == 0 {
			delete(d.scratch, size)
		} else {
			d.scratch[size] = kept
		}
	}
}

// Close frees everything d allocated.
func (d *Device) Close() {
	d.active = false
	for k, t := range d.targets {
		t.dispose()
		delete(d.targets, k)
	}
	for k, p := range d.programs {
		p.dispose()
		delete(d.programs, k)
	}
	for id, p := range d.pictures {
		p.img.Deallocate()
		delete(d.pictures, id)
	}
	for size, free := range d.scratch {
		for _, s := range free {
			s.img.Deallocate()
		}
		delete(d.scratch, size)
	}
}

// Render satisfies gshader.Device. It returns gshader.ErrUnsupported for a
// src that n reads when it has no CellRenderer to render it with.
func (d *Device) Render(
	key term.ImageID, size image.Point, src *gshader.Snapshot, n gshader.Node,
) (gshader.Frame, error) {
	if !d.active {
		return gshader.Frame{}, errOutsideDraw
	}
	if !readsSource(n) {
		src = nil
	}
	if src != nil && d.cells == nil {
		return gshader.Frame{}, gshader.ErrUnsupported
	}
	area := d.pixels(size)
	if err := checkSize(area); err != nil {
		return gshader.Frame{}, err
	}
	d.markUsed(n)
	k := targetKey{key: key, size: area}
	t := d.targets[k]
	if t != nil && t.tree != nil && t.gen == d.gen && gshader.Equal(t.tree, n) && t.renderedFrom(src) {
		t.used = true
		return t.frame(), nil
	}
	if t == nil {
		img := newTexture(area)
		t = &target{img: img, out: newImage(img), id: term.NewImageID()}
		d.targets[k] = t
	}
	t.used = true
	t.tree = nil
	if src != nil {
		d.source = d.acquire(area)
		defer func() {
			d.release(d.source)
			d.source = nil
		}()
		d.cells(d.source, src.Cells, src.Images)
		d.stats.sources++
	}
	if err := d.evalScaled(t.img, n, size); err != nil {
		return gshader.Frame{}, err
	}
	t.tree, t.gen = n, d.gen
	t.keep(src)
	t.version++
	t.out.invalidate()
	d.stats.renders++
	return t.frame(), nil
}

// readsSource reports whether n has a SourceNode.
func readsSource(n gshader.Node) bool {
	reads := false
	gshader.Walk(n, func(n gshader.Node) {
		if _, ok := n.(gshader.SourceNode); ok {
			reads = true
		}
	})
	return reads
}

// renderedFrom reports whether t was rendered from src: the same cells and
// the same placements of the same versions of the same pictures.
func (t *target) renderedFrom(src *gshader.Snapshot) bool {
	if src == nil {
		return !t.hasSource
	}
	return t.hasSource &&
		slices.EqualFunc(t.cells, src.Cells, func(a, b []term.Cell) bool { return slices.Equal(a, b) }) &&
		slices.EqualFunc(t.images, src.Images, func(a, b term.Image) bool {
			a.Src, b.Src = nil, nil
			return a == b
		})
}

// keep records src as what t was rendered from, copied, as its writer
// reuses its memory.
func (t *target) keep(src *gshader.Snapshot) {
	t.hasSource = src != nil
	if src == nil {
		t.cells, t.images = t.cells[:0], t.images[:0]
		return
	}
	t.cells = slices.Grow(t.cells[:0], len(src.Cells))[:len(src.Cells)]
	for y, row := range src.Cells {
		t.cells[y] = append(t.cells[y][:0], row...)
	}
	t.images = append(t.images[:0], src.Images...)
	for i := range t.images {
		t.images[i].Src = nil
	}
}

func (t *target) frame() gshader.Frame {
	return gshader.Frame{Src: t.out, ID: t.id, Version: t.version}
}

func (t *target) dispose() {
	t.out.dispose()
	t.img.Deallocate()
}

func (p *program) dispose() {
	if p.shader != nil {
		p.shader.Deallocate()
	}
}

// markUsed keeps the programs and pictures of n from being freed by End
// even when n's rendering is reused, so that a change of the tree does not
// compile or upload them again.
func (d *Device) markUsed(n gshader.Node) {
	gshader.Walk(n, func(n gshader.Node) {
		switch n := n.(type) {
		case gshader.RunNode:
			if p, ok := d.programs[n.Program]; ok {
				p.used = true
			}
		case gshader.PictureNode:
			if p, ok := d.pictures[n.ID]; ok {
				p.used = true
			}
		}
	})
}

// pixels returns the size in pixels of cells cells.
func (d *Device) pixels(cells image.Point) image.Point {
	return d.px.Pixels(image.Rectangle{Max: cells}).Size()
}

func checkSize(size image.Point) error {
	if size.X <= 0 || size.Y <= 0 {
		return fmt.Errorf("shaderdevice: cannot render %dx%d pixels", size.X, size.Y)
	}
	if size.X > maxImageSize || size.Y > maxImageSize {
		return fmt.Errorf("shaderdevice: %dx%d pixels is larger than %dx%d",
			size.X, size.Y, maxImageSize, maxImageSize)
	}
	return nil
}

// evalScaled evaluates n over a shaded area of area cells into all of dst,
// scaling it when its size is not dst's.
func (d *Device) evalScaled(dst *ebiten.Image, n gshader.Node, area image.Point) error {
	natural := d.pixels(gshader.Size(n, area))
	if natural == dst.Bounds().Size() {
		return d.eval(dst, n, area)
	}
	if err := checkSize(natural); err != nil {
		return err
	}
	tmp := d.acquire(natural)
	defer d.release(tmp)
	if err := d.eval(tmp, n, area); err != nil {
		return err
	}
	copyScaled(dst, dst.Bounds(), tmp, tmp.Bounds())
	return nil
}

// eval evaluates n over a shaded area of area cells into dst, which is the
// size of n and whose pixels it replaces.
func (d *Device) eval(dst *ebiten.Image, n gshader.Node, area image.Point) error {
	switch n := n.(type) {
	case gshader.SourceNode:
		if d.source == nil {
			dst.Clear()
			return nil
		}
		copyScaled(dst, dst.Bounds(), d.source, d.source.Bounds())
	case gshader.SolidNode:
		dst.Fill(d.resolve(n.Color))
	case gshader.PictureNode:
		return d.drawPicture(dst, n)
	case gshader.RunNode:
		return d.run(dst, n, area)
	case gshader.CropNode:
		return d.crop(dst, n, area)
	case gshader.PlaceNode:
		return d.place(dst, n, area)
	default:
		return fmt.Errorf("shaderdevice: unknown node %T", n)
	}
	return nil
}

func (d *Device) run(dst *ebiten.Image, n gshader.RunNode, area image.Point) error {
	shader, err := d.program(n.Program)
	if err != nil {
		return err
	}
	size := dst.Bounds().Size()
	values, err := n.Uniforms.Values(n.Program, d.defaults(size), d.resolve)
	if err != nil {
		return err
	}
	opts := ebiten.DrawRectShaderOptions{Uniforms: values, Blend: ebiten.BlendCopy}
	for i, in := range n.Inputs {
		tmp := d.acquire(size)
		defer d.release(tmp)
		if err := d.evalScaled(tmp, in, area); err != nil {
			return err
		}
		opts.Images[i] = tmp
	}
	dst.DrawRectShader(size.X, size.Y, shader, &opts)
	d.stats.passes++
	return nil
}

// defaults returns the values of the built-in uniforms of a node of size
// pixels.
func (d *Device) defaults(size image.Point) gshader.Defaults {
	return gshader.Defaults{
		Resolution: [2]float32{float32(size.X), float32(size.Y)},
		CellSize:   [2]float32{float32(d.px.Width), float32(d.px.Height)},
		Background: d.theme.Background,
		Foreground: d.theme.Foreground,
	}
}

func (d *Device) crop(dst *ebiten.Image, n gshader.CropNode, area image.Point) error {
	natural := d.pixels(gshader.Size(n.Node, area))
	if err := checkSize(natural); err != nil {
		return err
	}
	tmp := d.acquire(natural)
	defer d.release(tmp)
	if err := d.eval(tmp, n.Node, area); err != nil {
		return err
	}
	dst.Clear()
	sr := d.px.Pixels(n.Rect)
	visible := sr.Intersect(tmp.Bounds())
	if visible.Empty() {
		return nil
	}
	size := dst.Bounds().Size()
	sx, sy := float64(size.X)/float64(sr.Dx()), float64(size.Y)/float64(sr.Dy())
	dr := image.Rect(
		int(math.Round(float64(visible.Min.X-sr.Min.X)*sx)),
		int(math.Round(float64(visible.Min.Y-sr.Min.Y)*sy)),
		int(math.Round(float64(visible.Max.X-sr.Min.X)*sx)),
		int(math.Round(float64(visible.Max.Y-sr.Min.Y)*sy)),
	)
	copyScaled(dst, dr, tmp, visible)
	return nil
}

func (d *Device) place(dst *ebiten.Image, n gshader.PlaceNode, area image.Point) error {
	if err := d.eval(dst, n.Under, area); err != nil {
		return err
	}
	dr := d.px.Pixels(n.Rect)
	if dr.Intersect(dst.Bounds()).Empty() {
		return nil
	}
	natural := d.pixels(gshader.Size(n.Over, area))
	if err := checkSize(natural); err != nil {
		return err
	}
	tmp := d.acquire(natural)
	defer d.release(tmp)
	if err := d.eval(tmp, n.Over, area); err != nil {
		return err
	}
	copyScaled(dst, dr, tmp, tmp.Bounds())
	return nil
}

func (d *Device) drawPicture(dst *ebiten.Image, n gshader.PictureNode) error {
	tex, err := d.picture(n)
	if err != nil {
		return err
	}
	dr := dst.Bounds()
	if tex == nil || n.Fit != term.ImageFitFill {
		dst.Clear()
	}
	if tex == nil {
		return nil
	}
	sr := tex.Bounds()
	switch n.Fit {
	case term.ImageFitContain:
		copyScaled(dst, containRect(sr, dr), tex, sr)
	case term.ImageFitTile:
		for y := dr.Min.Y; y < dr.Max.Y; y += sr.Dy() {
			for x := dr.Min.X; x < dr.Max.X; x += sr.Dx() {
				copyScaled(dst, image.Rectangle{Min: image.Pt(x, y), Max: image.Pt(x, y).Add(sr.Size())}, tex, sr)
			}
		}
	default:
		copyScaled(dst, dr, tex, sr)
	}
	return nil
}

// picture returns the texture of n's pixels, uploading them once per ID
// and Version, or nil when it has none.
func (d *Device) picture(n gshader.PictureNode) (*ebiten.Image, error) {
	if n.Src == nil || n.Src.Bounds().Empty() {
		return nil, nil
	}
	if tex, ok := Texture(n.Src); ok {
		return tex, nil
	}
	b := n.Src.Bounds()
	if err := checkSize(b.Size()); err != nil {
		return nil, err
	}
	p := d.pictures[n.ID]
	if p != nil && p.version == n.Version && p.bounds == b {
		p.used = true
		return p.img, nil
	}
	if p == nil {
		p = new(picture)
		d.pictures[n.ID] = p
	} else if p.bounds.Size() != b.Size() {
		p.img.Deallocate()
		p.img = nil
	}
	if p.img == nil {
		p.img = ebiten.NewImage(b.Dx(), b.Dy())
	}
	p.img.WritePixels(rgbaPixels(n.Src))
	p.version, p.bounds, p.used = n.Version, b, true
	d.stats.uploads++
	return p.img, nil
}

// rgbaPixels returns src's premultiplied RGBA pixels, tightly packed.
func rgbaPixels(src image.Image) []byte {
	b := src.Bounds()
	if rgba, ok := src.(*image.RGBA); ok && rgba.Stride == 4*b.Dx() {
		return rgba.Pix[:4*b.Dx()*b.Dy()]
	}
	rgba := image.NewRGBA(image.Rectangle{Max: b.Size()})
	draw.Draw(rgba, rgba.Rect, src, b.Min, draw.Src)
	return rgba.Pix
}

// program returns the compiled shader of p, compiling it, or failing to,
// once while it is used.
func (d *Device) program(p *gshader.Program) (*ebiten.Shader, error) {
	pr, ok := d.programs[p]
	if !ok {
		shader, err := ebiten.NewShader(p.Source())
		if err != nil {
			err = fmt.Errorf("%s: %w", p.Name(), err)
		}
		pr = &program{shader: shader, err: err}
		d.programs[p] = pr
		d.stats.compiles++
	}
	pr.used = true
	return pr.shader, pr.err
}

func (d *Device) resolve(c term.Color) color.RGBA {
	if d.theme.Resolve != nil {
		return d.theme.Resolve(c)
	}
	if !c.Valid() || c == term.ColorDefault {
		return d.theme.Background
	}
	r, g, b := c.TrueColor().RGB()
	return color.RGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 0xff}
}

// acquire returns a scratch texture of size, whose pixels are undefined.
func (d *Device) acquire(size image.Point) *ebiten.Image {
	free := d.scratch[size]
	for _, s := range free {
		if !s.busy {
			s.busy, s.used = true, true
			return s.img
		}
	}
	s := &scratchTexture{img: newTexture(size), busy: true, used: true}
	d.scratch[size] = append(free, s)
	return s.img
}

// newTexture returns an image programs run into. It has a texture of its
// own, where ebiten's atlas would shift the dstPos programs see by where
// it put the image.
func newTexture(size image.Point) *ebiten.Image {
	return ebiten.NewImageWithOptions(image.Rectangle{Max: size}, &ebiten.NewImageOptions{Unmanaged: true})
}

// release returns a scratch texture acquire returned, for the next acquire
// to reuse. End frees the textures no acquire took since Begin.
func (d *Device) release(img *ebiten.Image) {
	for _, s := range d.scratch[img.Bounds().Size()] {
		if s.img == img {
			s.busy = false
			return
		}
	}
}

// copyScaled replaces the pixels of dst in dr, clipped to dst, with the
// pixels of src in sr scaled to dr.
func copyScaled(dst *ebiten.Image, dr image.Rectangle, src *ebiten.Image, sr image.Rectangle) {
	clip := dr.Intersect(dst.Bounds())
	if clip.Empty() || sr.Empty() {
		return
	}
	op := ebiten.DrawImageOptions{Blend: ebiten.BlendCopy}
	op.GeoM.Scale(float64(dr.Dx())/float64(sr.Dx()), float64(dr.Dy())/float64(sr.Dy()))
	op.GeoM.Translate(float64(dr.Min.X), float64(dr.Min.Y))
	if dr.Size() != sr.Size() {
		op.Filter = ebiten.FilterLinear
	}
	target := dst
	if !clip.Eq(dst.Bounds()) {
		target = dst.SubImage(clip).(*ebiten.Image)
	}
	if !sr.Eq(src.Bounds()) {
		src = src.SubImage(sr).(*ebiten.Image)
	}
	target.DrawImage(src, &op)
}

// containRect scales src uniformly to the largest rectangle that fits in
// area and centres it there.
func containRect(src, area image.Rectangle) image.Rectangle {
	scale := math.Min(float64(area.Dx())/float64(src.Dx()), float64(area.Dy())/float64(src.Dy()))
	w := max(1, int(math.Round(float64(src.Dx())*scale)))
	h := max(1, int(math.Round(float64(src.Dy())*scale)))
	x := area.Min.X + (area.Dx()-w)/2
	y := area.Min.Y + (area.Dy()-h)/2
	return image.Rect(x, y, x+w, y+h)
}

// Compile compiles the Kage source src and frees the result, reporting the
// errors the device would report when it first renders it.
func Compile(src []byte) error {
	shader, err := ebiten.NewShader(src)
	if err != nil {
		return err
	}
	shader.Deallocate()
	return nil
}
