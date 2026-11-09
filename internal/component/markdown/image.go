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

package markdown

import (
	"bufio"
	"bytes"
	"html"
	"image"
	"io"
	"math"
	"strings"

	compapi "github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"github.com/yuin/goldmark/ast"
	goldhtml "github.com/yuin/goldmark/renderer/html"

	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/component"
)

// githubLineHeight is the height in CSS pixels of a line of GitHub's
// markdown text: 16px at a 1.5 line height. One cell row stands for one
// such line, so images keep GitHub's proportion to the text on any
// screen and at any font size.
const githubLineHeight = 24

// maxImageCells bounds the cols and rows an image may ask for, so that a
// document cannot make a block too tall to lay out or search.
const maxImageCells = 1000

// imageSize is the size in cells an image's attributes ask for. A zero
// side is left to the picture.
type imageSize struct {
	cols, rows int
	// stretch, set by maintain_aspect_ratio=false, fills the box instead
	// of keeping the picture's proportions.
	stretch bool
}

// imageSizeOf reads the cols, rows and maintain_aspect_ratio attributes
// of an image. A side that is not a whole number of cells from 1 to
// maxImageCells is left to the picture, and maintain_aspect_ratio is only
// turned off by false.
func imageSizeOf(n ast.Node) imageSize {
	size := imageSize{cols: cellsAttribute(n, "cols"), rows: cellsAttribute(n, "rows")}
	if v, ok := n.AttributeString("maintain_aspect_ratio"); ok {
		keep, ok := v.(bool)
		size.stretch = ok && !keep
	}
	return size
}

func cellsAttribute(n ast.Node, name string) int {
	v, _ := n.AttributeString(name)
	f, ok := v.(float64)
	if !ok || f < 1 || f > maxImageCells || f != math.Trunc(f) {
		return 0
	}
	return int(f)
}

// imageCaption is the title of an image on one line, with its escapes and
// character references resolved as in HTML. An escaped reference, such
// as \&amp;, stays literal.
func imageCaption(img *ast.Image) string {
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	goldhtml.DefaultWriter.Write(w, img.Title)
	_ = w.Flush()
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
}

// imageCells returns the cell box of an image of natural size, in CSS
// pixels, in a column width cells wide. Without a size it is GitHub's
// size for the image. A side the size sets takes that many cells, and
// the other follows the picture's aspect ratio or, when stretched, keeps
// GitHub's size. When both are set the picture is fit inside the box,
// or stretched to fill it. A box wider than the column is shrunk to it,
// keeping its proportions unless stretched. Each side is within half a
// cell of the exact size, and at least one cell.
func imageCells(
	natural image.Point, px cell.PixelSize, width int, size imageSize,
) (cols, rows int) {
	if width <= 0 {
		return 0, 0
	}
	scale := px.Height / githubLineHeight
	githubW, githubH := float64(natural.X)*scale, float64(natural.Y)*scale
	w, h := githubW, githubH
	if size.cols > 0 {
		w = float64(size.cols) * px.Width
	}
	if size.rows > 0 {
		h = float64(size.rows) * px.Height
	}
	switch {
	case size.stretch:
	case size.cols > 0 && size.rows == 0:
		h = w * githubH / githubW
	case size.rows > 0 && size.cols == 0:
		w = h * githubW / githubH
	}
	if limit := float64(width) * px.Width; w > limit {
		if !size.stretch {
			h *= limit / w
		}
		w = limit
	}
	cols = min(max(int(math.Round(w/px.Width)), 1), width)
	rows = max(int(math.Round(h/px.Height)), 1)
	return cols, rows
}

// imageLabel is the text shown in place of an image: its alt text, or
// its destination when it has none.
func imageLabel(alt, dest string) string {
	if alt != "" {
		return alt
	}
	return dest
}

// cellSize is the last cell size in pixels a component was drawn with.
type cellSize struct {
	px cell.PixelSize
	ok bool
}

// picture is what an image block draws in place of an image. Start may
// be called many times, and Size reports a settled zero size when
// loading failed. *imageuri.Component is the picture; tests draw fakes.
type picture interface {
	tui.Component
	io.Closer
	Start()
	Size() (size image.Point, settled bool)
}

// imageItem is an image in an image block.
type imageItem struct {
	alt, dest string
	// url is where a click goes: the enclosing link's destination, or
	// dest.
	url string
	// caption is drawn under the picture.
	caption string
	size    imageSize
	// img is nil when dest is not shown.
	img picture
	// gap is set when whitespace separates the item from the previous
	// one, and newline when a hard line break does.
	gap, newline bool
	// hint is the size a previous render loaded for dest, used until img
	// settles.
	hint image.Point
}

func (it *imageItem) label() string {
	return imageLabel(it.alt, it.dest)
}

// natural returns the size to lay the item out at, zero when it has
// none, and whether its picture has loaded.
func (it *imageItem) natural() (size image.Point, loaded bool) {
	if it.img == nil {
		return image.Point{}, false
	}
	size, settled := it.img.Size()
	if !settled {
		return it.hint, false
	}
	return size, size.X > 0 && size.Y > 0
}

// imageBlock is a paragraph made only of images. Images flow left to
// right, bottom-aligned, wrapping to a new row when they do not fit or
// after a hard line break. An image that has not loaded, or cannot be,
// is drawn as its label on one row. A picture with a caption is centered
// above it, in a box as wide as the wider of the two.
type imageBlock struct {
	items []imageItem
	cfg   *Config
	// cell is shared with the owning component, which sets it in Init.
	cell  *cellSize
	width int
	// laid is the layout as of the last Resize. Draw and the position
	// queries use it so that they agree with the height the component
	// allotted, even if an image settles in between.
	laid imageLayout
}

var _ block = (*imageBlock)(nil)

type imageLayout struct {
	boxes []imageBox
	// rows excludes the blank row after the block.
	rows int
}

type imageBox struct {
	// x, y, cols and rows place the picture with its caption, or the
	// label.
	x, y, cols, rows int
	// picX, picCols and picRows place the picture at the top of the box.
	// They are zero for a box sized to its label.
	picX, picCols, picRows int
	// caption is set when the row under the picture shows its caption.
	caption bool
	// loaded is set when the box draws the picture rather than the label.
	loaded bool
}

func (b *imageBox) contains(x, y int) bool {
	return x >= b.x && x < b.x+b.cols && y >= b.y && y < b.y+b.rows
}

func newImageBlock(items []imageItem, cfg *Config) *imageBlock {
	return &imageBlock{items: items, cfg: cfg}
}

func (b *imageBlock) layout(width int) imageLayout {
	l := imageLayout{boxes: make([]imageBox, len(b.items))}
	if width <= 0 {
		return l
	}
	var x, rowTop, rowRows, rowStart int
	endRow := func(end int) {
		for j := rowStart; j < end; j++ {
			l.boxes[j].y = rowTop + rowRows - l.boxes[j].rows
		}
		rowTop += rowRows
	}
	for i := range b.items {
		it := &b.items[i]
		box := b.box(it, width)
		gap := 0
		if it.gap {
			gap = 1
		}
		if i > rowStart && (it.newline || x+gap+box.cols > width) {
			endRow(i)
			x, rowRows, rowStart, gap = 0, 0, i, 0
		}
		box.x = x + gap
		x = box.x + box.cols
		rowRows = max(rowRows, box.rows)
		l.boxes[i] = box
	}
	endRow(len(b.items))
	l.rows = rowTop
	return l
}

func (b *imageBlock) box(it *imageItem, width int) imageBox {
	if b.cell != nil && b.cell.ok {
		if size, loaded := it.natural(); size.X > 0 && size.Y > 0 {
			cols, rows := imageCells(size, b.cell.px, width, it.size)
			box := imageBox{
				cols: cols, rows: rows, picCols: cols, picRows: rows, loaded: loaded,
			}
			if it.caption != "" {
				box.cols = min(max(cols, textWidth(it.caption)), width)
				box.picX = (box.cols - cols) / 2
				box.rows++
				box.caption = true
			}
			return box
		}
	}
	return imageBox{cols: max(1, min(textWidth(it.label()), width)), rows: 1}
}

func (b *imageBlock) Height(width int) int {
	if width <= 0 {
		return 0
	}
	return b.layout(width).rows + 1
}

// Resize lays the block out again from its images' current sizes.
func (b *imageBlock) Resize(width, _ int) {
	b.width = width
	b.laid = b.layout(width)
}

// height is Height as of the last Resize.
func (b *imageBlock) height() int {
	if b.width <= 0 {
		return 0
	}
	return b.laid.rows + 1
}

func (b *imageBlock) Draw(w term.Writer) {
	for i, box := range b.laid.boxes {
		it := &b.items[i]
		if box.caption {
			x, y := b.captionPos(it, box)
			component.WriteText(w, x, y, box.x+box.cols, it.caption, b.cfg.Paragraph)
		}
		if !box.loaded {
			component.WriteText(w, box.x, box.y, box.x+box.cols, it.label(), b.cfg.Link)
			continue
		}
		dst := w
		if it.size.stretch {
			dst = fillWriter{w}
		}
		it.img.Resize(box.picCols, box.picRows)
		it.img.Draw(&compapi.VirtualWriter{
			Writer: dst,
			Offset: term.Coordinates{X: box.x + box.picX, Y: box.y},
			Width:  box.picCols,
			Height: box.picRows,
		})
	}
}

// captionPos is where the caption of a box starts: centered under the
// picture, or at the box's left edge when cut to it.
func (b *imageBlock) captionPos(it *imageItem, box imageBox) (x, y int) {
	return box.x + max(box.cols-textWidth(it.caption), 0)/2, box.y + box.picRows
}

// fillWriter stretches the pictures drawn through it to fill their
// cells.
type fillWriter struct {
	term.Writer
}

func (w fillWriter) DrawImage(img term.Image) bool {
	img.Fit = term.ImageFitFill
	return w.Writer.DrawImage(img)
}

// Dimensions returns the size of the images on a single row.
func (b *imageBlock) Dimensions() (width, height int) {
	l := b.layout(math.MaxInt32)
	for _, box := range l.boxes {
		width = max(width, box.x+box.cols)
	}
	return width, l.rows + 1
}

func (b *imageBlock) SpanAt(x, y int) (text, url string, ok bool) {
	for i := range b.laid.boxes {
		if b.laid.boxes[i].contains(x, y) {
			return b.items[i].label(), b.items[i].url, true
		}
	}
	return
}

// CharAt finds the labels drawn in place of images and the captions under
// them, but nothing on the cells of a picture.
func (b *imageBlock) CharAt(x, y int) (rune, bool) {
	for i, box := range b.laid.boxes {
		if box.caption {
			cx, cy := b.captionPos(&b.items[i], box)
			if y == cy && x >= cx && x < box.x+box.cols {
				return charAtInLine(textRun{{text: b.items[i].caption}}, x-cx)
			}
		}
		if box.loaded || y != box.y || x < box.x || x >= box.x+box.cols {
			continue
		}
		return charAtInLine(textRun{{text: b.items[i].label()}}, x-box.x)
	}
	return 0, false
}

func (b *imageBlock) start() {
	for _, it := range b.items {
		if it.img != nil {
			it.img.Start()
		}
	}
}

func (b *imageBlock) close() {
	for _, it := range b.items {
		if it.img != nil {
			_ = it.img.Close()
		}
	}
}

// sizes records in m the size each image loaded, or was hinted while it
// loads, by destination.
func (b *imageBlock) sizes(m map[string]image.Point) {
	for i := range b.items {
		if size, _ := b.items[i].natural(); size.X > 0 && size.Y > 0 {
			m[b.items[i].dest] = size
		}
	}
}

// inherit hints each image with the size m records for its destination.
func (b *imageBlock) inherit(m map[string]image.Point) {
	for i := range b.items {
		if b.items[i].img != nil {
			b.items[i].hint = m[b.items[i].dest]
		}
	}
}
