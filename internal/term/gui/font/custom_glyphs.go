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

package font

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

type style uint8

const (
	styleNone style = iota
	styleSingle
	styleBold
)

var (
	colorFill           = color.RGBA{R: 255, G: 255, B: 255, A: 255}
	colorFillAlphaStep1 = color.RGBA{R: 192, G: 192, B: 192, A: 192}
	colorFillAlphaStep2 = color.RGBA{R: 128, G: 128, B: 128, A: 128}
	colorFillAlphaStep3 = color.RGBA{R: 64, G: 64, B: 64, A: 64}
)

const halfEllipseSegments = 64

func (m *custom) plusGlyph(
	bounds image.Rectangle,
	leftHorizontal, rightHorizontal, topVertical, bottomVertical style,
) (ok bool) {
	lhstroke := m.strokeWidthFromMask(bounds, leftHorizontal)
	rhstroke := m.strokeWidthFromMask(bounds, rightHorizontal)
	tvstroke := m.strokeWidthFromMask(bounds, topVertical)
	bvstroke := m.strokeWidthFromMask(bounds, bottomVertical)

	xv, yh, xh, yv, lhsize, rhsize, tvsize, bvsize := plusGlyphBounds(
		bounds, lhstroke, rhstroke, tvstroke, bvstroke)

	m.drawHorizontalLine(bounds, m.mask, float64(bounds.Min.X), yh, lhsize, lhstroke)
	m.drawHorizontalLine(bounds, m.mask, xh, yh, rhsize, rhstroke)
	m.drawVerticalLine(bounds, m.mask, xv, float64(bounds.Min.Y), tvsize, tvstroke)
	m.drawVerticalLine(bounds, m.mask, xv, yv, bvsize, bvstroke)

	ok = true
	return
}

func (m *custom) shadeGlyph(bounds image.Rectangle, c color.RGBA) (ok bool) {
	width := float64(bounds.Max.X - bounds.Min.X - m.overlapX)
	height := float64(bounds.Max.Y - bounds.Min.Y - m.overlapY)
	x := float64(bounds.Min.X)
	y := float64(bounds.Min.Y)

	m.drawRect(bounds, m.mask, x, y, width, height, c)
	ok = true
	return
}

func (m *custom) polygonGlyph(bounds image.Rectangle, polygons ...[][2]float32) bool {
	return m.fillPath(bounds, func(z *vector.Rasterizer, w, h float32) {
		for _, vertices := range polygons {
			z.MoveTo(vertices[0][0]*w, vertices[0][1]*h)
			for _, v := range vertices[1:] {
				z.LineTo(v[0]*w, v[1]*h)
			}
			z.ClosePath()
		}
	})
}

func (m *custom) halfEllipseGlyph(bounds image.Rectangle, flatX, apexX float32) bool {
	return m.fillPath(bounds, func(z *vector.Rasterizer, w, h float32) {
		flat, apex := flatX*w, apexX*w
		z.MoveTo(flat, 0)
		for i := 1; i <= halfEllipseSegments; i++ {
			theta := math.Pi * float64(i) / halfEllipseSegments
			z.LineTo(
				flat+(apex-flat)*float32(math.Sin(theta)),
				h/2*(1-float32(math.Cos(theta))))
		}
		z.ClosePath()
	})
}

func (m *custom) outlineGlyph(bounds image.Rectangle, r rune) bool {
	if m.outlines == nil {
		return false
	}
	idx, err := m.outlines.GlyphIndex(&m.buf, r)
	if err != nil || idx == 0 {
		return false
	}
	unitsPerEm := fixed.I(int(m.outlines.UnitsPerEm()))
	segments, err := m.outlines.LoadGlyph(&m.buf, idx, unitsPerEm, nil)
	if err != nil || len(segments) == 0 {
		return false
	}
	extent := fixed.Rectangle26_6{Min: segments[0].Args[0], Max: segments[0].Args[0]}
	for _, s := range segments {
		p := s.Args[segmentEnd(s.Op)]
		extent.Min.X, extent.Max.X = min(extent.Min.X, p.X), max(extent.Max.X, p.X)
		extent.Min.Y, extent.Max.Y = min(extent.Min.Y, p.Y), max(extent.Max.Y, p.Y)
	}
	if extent.Min.X == extent.Max.X || extent.Min.Y == extent.Max.Y {
		return false
	}
	return m.fillPath(bounds, func(z *vector.Rasterizer, w, h float32) {
		sx := w / float32(extent.Max.X-extent.Min.X)
		sy := h / float32(extent.Max.Y-extent.Min.Y)
		at := func(p fixed.Point26_6) (float32, float32) {
			return float32(p.X-extent.Min.X) * sx, float32(p.Y-extent.Min.Y) * sy
		}
		for _, s := range segments {
			switch s.Op {
			case sfnt.SegmentOpMoveTo:
				z.MoveTo(at(s.Args[0]))
			case sfnt.SegmentOpLineTo:
				z.LineTo(at(s.Args[0]))
			case sfnt.SegmentOpQuadTo:
				bx, by := at(s.Args[0])
				cx, cy := at(s.Args[1])
				z.QuadTo(bx, by, cx, cy)
			case sfnt.SegmentOpCubeTo:
				bx, by := at(s.Args[0])
				cx, cy := at(s.Args[1])
				dx, dy := at(s.Args[2])
				z.CubeTo(bx, by, cx, cy, dx, dy)
			}
		}
	})
}

func segmentEnd(op sfnt.SegmentOp) int {
	switch op {
	case sfnt.SegmentOpQuadTo:
		return 1
	case sfnt.SegmentOpCubeTo:
		return 2
	default:
		return 0
	}
}

func (m *custom) fillPath(
	bounds image.Rectangle, path func(z *vector.Rasterizer, w, h float32),
) bool {
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return true
	}
	if m.coverage == nil || m.coverage.Rect.Dx() < w || m.coverage.Rect.Dy() < h {
		m.coverage = image.NewAlpha(image.Rect(0, 0, w, h))
	}
	m.raster.Reset(w, h)
	m.raster.DrawOp = draw.Src
	path(&m.raster, float32(w), float32(h))
	m.raster.Draw(m.coverage, image.Rect(0, 0, w, h), image.Opaque, image.Point{})
	// Unlike the rasterizer, DrawMask clips to the mask, which bounds
	// overruns when the glyph is measured at a dot off the cell origin.
	draw.DrawMask(m.mask, bounds, image.White, image.Point{},
		m.coverage, image.Point{}, draw.Over)
	return true
}

func (m *custom) arcPlusGlyph(
	bounds image.Rectangle,
	topLeft, topRight, bottomLeft, bottomRight style,
) (ok bool) {
	if topRight == styleSingle {
		m.drawArcPlusGlyphTopRight(m.mask, bounds)
		ok = true
		return
	}

	if topLeft == styleSingle {
		m.drawArcPlusGlyphTopLeft(m.mask, bounds)
		ok = true
		return
	}

	if bottomRight == styleSingle {
		m.drawArcPlusGlyphBottomRight(bounds)
		ok = true
		return
	}

	if bottomLeft == styleSingle {
		m.drawArcPlusGlyphBottomLeft(bounds)
		ok = true
		return
	}

	return
}

func (m *custom) drawArcPlusGlyphBottomLeft(bounds image.Rectangle) {
	m.drawArcPlusGlyphTopLeft(m.altMask, bounds)
	flipVerticalInto(m.mask, m.altMask, bounds.Max.Y-bounds.Min.Y)
}

func (m *custom) drawArcPlusGlyphBottomRight(bounds image.Rectangle) {
	m.drawArcPlusGlyphTopRight(m.altMask, bounds)
	flipVerticalInto(m.mask, m.altMask, bounds.Max.Y-bounds.Min.Y)
}

func flipVerticalInto(dst, src *image.RGBA, height int) {
	b := src.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		dy := height - 1 - y
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := src.At(x, y).RGBA(); a == 0 {
				continue
			}
			dst.Set(x, dy, src.At(x, y))
		}
	}
}

func (m *custom) drawArcPlusGlyphTopRight(dst draw.Image, bounds image.Rectangle) {
	strokeWidth := m.strokeWidthFromMask(bounds, styleSingle)

	xv := centerX(bounds)
	yh := centerY(bounds)
	vBoundsFrom := xv
	vBoundsTo := xv + float64(strokeWidth)
	hBoundsFrom := yh
	hBoundsTo := yh + float64(strokeWidth)

	rx1, ry1 := vBoundsFrom, hBoundsFrom
	for ; rx1 < vBoundsTo && ry1 < hBoundsTo; rx1, ry1 = rx1+1, ry1+1 {
		rx2 := rx1 * rx1
		ry2 := ry1 * ry1
		quarter := rx2 / math.Sqrt(rx2+ry2)

		for x := float64(bounds.Min.X); x < quarter; x++ {
			y := ry1 * math.Sqrt(1-x*x/rx2)
			x := float64(bounds.Min.X) + float64(bounds.Max.X) -
				(x + 1 - float64(bounds.Min.X))
			y = math.Min(math.Max(y, float64(bounds.Min.Y)), hBoundsTo-1)
			x = centerToX(x, bounds, strokeWidth)
			y = centerFromY(y, bounds, strokeWidth)
			dst.Set(int(x), int(y), colorFill)
		}

		quarter = ry2 / math.Sqrt(rx2+ry2)
		for y := float64(bounds.Min.Y); y < quarter; y++ {
			x := rx1 * math.Sqrt(1-y*y/ry2)
			y := math.Min(math.Max(y, float64(bounds.Min.Y)), ry1)
			x = float64(bounds.Max.X) - (x + 1 - float64(bounds.Min.X))
			x = centerToX(x, bounds, strokeWidth)
			y = centerFromY(y, bounds, strokeWidth)
			dst.Set(int(x), int(y), colorFill)
		}
	}

	// ensure the part closer to the edge of the cell is filled
	m.drawVerticalLine(bounds, dst, xv, float64(bounds.Min.Y),
		math.Max(1, float64(strokeWidth)/2), strokeWidth)
}

func (m *custom) drawArcPlusGlyphTopLeft(dst draw.Image, bounds image.Rectangle) {
	strokeWidth := m.strokeWidthFromMask(bounds, styleSingle)

	xv := centerX(bounds)
	yh := centerY(bounds)
	vBoundsFrom := centerFromX(xv, bounds, strokeWidth)
	vBoundsTo := centerToX(xv, bounds, strokeWidth)
	hBoundsFrom := centerFromY(yh, bounds, strokeWidth)
	hBoundsTo := centerToY(yh, bounds, strokeWidth)

	rx1, ry1 := vBoundsFrom, hBoundsFrom
	for ; rx1 < vBoundsTo && ry1 < hBoundsTo; rx1, ry1 = rx1+1, ry1+1 {
		rx2 := rx1 * rx1
		ry2 := ry1 * ry1
		quarter := rx2 / math.Sqrt(rx2+ry2)

		for x := float64(bounds.Min.X); x < quarter; x++ {
			y := ry1 * math.Sqrt(1-x*x/rx2)
			x := math.Min(math.Max(x+1, float64(bounds.Min.X)), rx1)
			y = math.Min(math.Max(y, float64(bounds.Min.Y)), hBoundsTo-1)
			dst.Set(int(x), int(y), colorFill)
		}

		quarter = ry2 / math.Sqrt(rx2+ry2)
		for y := float64(bounds.Min.Y); y < quarter; y++ {
			x := rx1 * math.Sqrt(1-y*y/ry2)
			y := math.Min(math.Max(y, float64(bounds.Min.Y)), ry1)
			x = math.Min(math.Max(x+1, float64(bounds.Min.X)), vBoundsTo-1)
			dst.Set(int(x), int(y), colorFill)
		}
	}

	// ensure the part closer to the edge of the cell is filled
	m.drawHorizontalLine(bounds, dst, float64(bounds.Min.X), yh,
		math.Max(1, float64(strokeWidth)/2), strokeWidth)
	m.drawVerticalLine(bounds, dst, xv, float64(bounds.Min.Y),
		math.Max(1, float64(strokeWidth)/2), strokeWidth)
}

func (m *custom) dottedHorizontalGlyph(
	bounds image.Rectangle, s style, gaps int,
) (ok bool) {
	strokeWidth := m.strokeWidthFromMask(bounds, s)
	dashGapLen := float64(m.strokeWidthFromMask(bounds, styleSingle))

	width := float64(bounds.Max.X - bounds.Min.X)
	dashLen := math.Trunc(math.Max(
		float64(width-dashGapLen*float64(gaps+1))/float64(gaps+1),
		1,
	))
	y := centerY(bounds)
	for gap := 0.; gap < float64(gaps+1); gap++ {
		x := float64(bounds.Min.X) + math.Trunc(
			math.Min(
				float64(gap*(dashLen+dashGapLen)),
				float64(bounds.Max.X)-1,
			),
		)
		if int(gap) == gaps {
			// add or reduce pixels to the last dash length such that gaps are always
			// equal, which gives a more visually pleasing result
			dashLen = float64(bounds.Max.X) - dashGapLen - x
		}
		m.drawHorizontalLine(bounds, m.mask, x, y, float64(dashLen), strokeWidth)
	}
	ok = true
	return
}

func (m *custom) dottedVerticalGlyph(
	bounds image.Rectangle, s style, gaps int,
) (ok bool) {
	strokeWidth := m.strokeWidthFromMask(bounds, s)
	dashGapLen := float64(m.strokeWidthFromMask(bounds, styleBold))

	height := float64(bounds.Max.Y - bounds.Min.Y)
	dashLen := math.Trunc(math.Max(
		float64(height-dashGapLen*float64(gaps+1))/float64(gaps+1),
		1,
	))
	x := centerX(bounds)
	for gap := 0.; gap < float64(gaps+1); gap++ {
		y := float64(bounds.Min.Y) + math.Trunc(
			math.Min(
				float64(gap*(dashLen+dashGapLen)),
				float64(bounds.Max.Y)-1,
			),
		)
		if int(gap) == gaps {
			// add or reduce pixels to the last dash length such that gaps are always
			// equal, which gives a more visually pleasing result
			dashLen = float64(bounds.Max.Y) - dashGapLen - y
		}
		m.drawVerticalLine(bounds, m.mask, x, y, float64(dashLen), strokeWidth)
	}
	ok = true
	return
}

func (m *custom) blockGlyph(bounds image.Rectangle, matrix [][]bool) (ok bool) {
	return m.blockGlyphFill(bounds, matrix, colorFill)
}

func (m *custom) blockGlyphFill(
	bounds image.Rectangle, matrix [][]bool, fill color.RGBA,
) (ok bool) {
	rows := len(matrix)
	for i, row := range matrix {
		ok = true
		cols := len(row)
		for j, render := range row {
			if !render {
				continue
			}
			width := math.Max(1, float64(bounds.Max.X-bounds.Min.X-m.overlapX)/float64(cols))
			height := math.Max(1, float64(bounds.Max.Y-bounds.Min.Y-m.overlapY)/float64(rows))
			x := math.Min(
				float64(bounds.Max.X)-width,
				float64(bounds.Min.X)+float64(j)*width,
			)
			y := math.Min(
				float64(bounds.Max.Y)-height,
				float64(bounds.Min.Y)+float64(i)*height,
			)

			m.drawRect(bounds, m.mask, x, y, width, height, fill)
		}
	}
	return
}

func (m *custom) blockGlyphOuterSquare(
	bounds image.Rectangle,
	top, bottom, left, right style,
) (ok bool) {
	topStroke := m.strokeWidthFromMask(bounds, top)
	bottomStroke := m.strokeWidthFromMask(bounds, bottom)
	leftStroke := m.strokeWidthFromMask(bounds, left)
	rightStroke := m.strokeWidthFromMask(bounds, right)

	width := float64(bounds.Max.X - bounds.Min.X)
	height := float64(bounds.Max.Y - bounds.Min.Y)

	m.drawRect(bounds, m.mask,
		float64(bounds.Min.X), float64(bounds.Min.Y),
		width, float64(topStroke),
		colorFill,
	)
	m.drawRect(bounds, m.mask,
		float64(bounds.Min.X), float64(bounds.Max.Y)-float64(bottomStroke),
		width, float64(bottomStroke),
		colorFill,
	)
	m.drawRect(bounds, m.mask,
		float64(bounds.Min.X), float64(bounds.Min.Y),
		float64(leftStroke), height,
		colorFill,
	)
	m.drawRect(bounds, m.mask,
		float64(bounds.Max.X)-float64(rightStroke), float64(bounds.Min.Y),
		float64(rightStroke), height,
		colorFill,
	)
	ok = true
	return
}

func (m *custom) drawVerticalLine(
	bounds image.Rectangle, dst draw.Image,
	x, y, size float64, width int,
) {
	startx := centerFromX(x, bounds, width)
	endx := centerToX(x, bounds, width)
	m.drawRect(bounds, dst, startx, y, endx-startx, size, colorFill)
}

func (m *custom) drawHorizontalLine(
	bounds image.Rectangle, dst draw.Image,
	x, y, size float64, width int) {
	starty := centerFromY(y, bounds, width)
	endy := centerToY(y, bounds, width)
	m.drawRect(bounds, dst, x, starty, size, endy-starty, colorFill)
}

func (m *custom) drawRect(
	bounds image.Rectangle, dst draw.Image,
	x, y, width, height float64, color color.RGBA,
) {
	startx := int(x)
	endx := int(math.Min(x+width-1, float64(bounds.Max.X-1)))

	starty := int(y)
	endy := int(math.Min(y+height-1, float64(bounds.Max.Y-1)))

	for y := starty; y <= endy; y++ {
		for x := startx; x <= endx; x++ {
			dst.Set(x, y, color)
		}
	}
}

func (m *custom) strokeWidthFromMask(bounds image.Rectangle, mask style) int {
	switch mask {
	case styleSingle:
		return 1
	case styleBold:
		// double for bold stroke
		return 2
	default:
		return 0
	}
}

func plusGlyphBounds(
	bounds image.Rectangle,
	lhstroke, rhstroke, tvstroke, bvstroke int,
) (xv, yh, xh, yv, lhsize, rhsize, tvsize, bvsize float64) {

	xv = centerX(bounds)
	yh = centerY(bounds)

	tvBoundsFrom := centerFromX(xv, bounds, tvstroke)
	tvBoundsTo := centerToX(xv, bounds, tvstroke)

	bvBoundsFrom := centerFromX(xv, bounds, bvstroke)
	bvBoundsTo := centerToX(xv, bounds, bvstroke)

	lhBoundsFrom := centerFromY(yh, bounds, lhstroke)
	lhBoundsTo := centerToY(yh, bounds, lhstroke)

	rhBoundsFrom := centerFromY(yh, bounds, rhstroke)
	rhBoundsTo := centerToY(yh, bounds, rhstroke)

	lhsize = math.Max(tvBoundsTo, bvBoundsTo) - float64(bounds.Min.X)
	xh = math.Min(tvBoundsFrom, bvBoundsFrom)
	rhsize = float64(bounds.Max.X-bounds.Min.X) - xh + float64(bounds.Min.X)

	tvsize = math.Max(lhBoundsTo, rhBoundsTo) - float64(bounds.Min.Y)
	yv = math.Min(lhBoundsFrom, rhBoundsFrom)
	bvsize = float64(bounds.Max.Y-bounds.Min.Y) - yv + float64(bounds.Min.Y)

	return
}

func centerY(bounds image.Rectangle) float64 {
	// bounds.Min.Y could be negative
	return float64(bounds.Min.Y) + float64(bounds.Max.Y-bounds.Min.Y)/2
}

func centerX(bounds image.Rectangle) float64 {
	// bounds.Min.X could be negative
	return float64(bounds.Min.X) + float64(bounds.Max.X-bounds.Min.X)/2
}

func centerFromX(x float64, bounds image.Rectangle, strokeWidth int) float64 {
	return math.Max(
		math.Trunc(x-float64(strokeWidth)/2),
		float64(bounds.Min.X),
	)
}

func centerFromY(y float64, bounds image.Rectangle, strokeWidth int) float64 {
	return math.Max(
		math.Trunc(y-float64(strokeWidth)/2),
		float64(bounds.Min.Y),
	)
}

func centerToX(x float64, bounds image.Rectangle, strokeWidth int) float64 {
	return math.Min(
		math.Trunc(x+float64(strokeWidth)/2),
		float64(bounds.Max.X-1),
	)
}

func centerToY(y float64, bounds image.Rectangle, strokeWidth int) float64 {
	return math.Min(
		math.Trunc(y+float64(strokeWidth)/2),
		float64(bounds.Max.Y-1),
	)
}
