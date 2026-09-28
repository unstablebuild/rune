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

package vtegraphics

import (
	"image"
	"time"
)

// defaultFrameGap is the gap of a new frame transmitted without 'z'
// (spec §13.1).
const defaultFrameGap = 40

// frameCacheMultiplier scales the storage limit into the quota for
// animation frames (spec §14).
const frameCacheMultiplier = 5

type animationState uint8

const (
	animationStopped animationState = iota
	animationLoading
	animationRunning
)

// frame is one animation frame, kept fully composed at image size so
// showing it is a pointer swap (kitty keeps partial frames and composes
// on demand; the visible result is the same).
type frame struct {
	pix       *image.RGBA
	gap       int32
	transient bool
}

// animation is the frame list of an image (spec §13). frames[0] is the
// root frame, whose pixels are the image's own until replaced.
type animation struct {
	frames      []*frame
	current     int
	state       animationState
	duration    int64
	maxLoops    uint32
	currentLoop uint32
	shownAt     time.Time
	drawn       bool
}

// frameLoad is the pending frame transmission of a chunked a=f.
type frameLoad struct {
	number uint32
	isNew  bool
}

func (a *animation) changeGap(f *frame, gap int32) {
	prev := int64(f.gap)
	f.gap = max(0, gap)
	if prev < a.duration {
		a.duration -= prev
	} else {
		a.duration = 0
	}
	a.duration += int64(f.gap)
}

// fillColor fills the canvas with a 0xRRGGBBAA straight-alpha colour.
func fillColor(canvas *image.RGBA, rgba uint32) {
	px := [4]byte{byte(rgba >> 24), byte(rgba >> 16), byte(rgba >> 8), byte(rgba)}
	premultiply(px[:], px[:])
	for i := 0; i+3 < len(canvas.Pix); i += 4 {
		copy(canvas.Pix[i:i+4], px[:])
	}
}

// composeOnto draws src onto dst at offset, alpha blending or replacing
// pixels. Both images are premultiplied RGBA.
func composeOnto(dst, src *image.RGBA, offset image.Point, blend bool) {
	r := image.Rect(offset.X, offset.Y, offset.X+src.Rect.Dx(), offset.Y+src.Rect.Dy()).
		Intersect(dst.Rect)
	if r.Empty() {
		return
	}
	composeRect(dst, r.Min, src, image.Pt(r.Min.X-offset.X, r.Min.Y-offset.Y), r.Size(), blend)
}

// composeRect composes a size-sized rectangle of src at srcOff onto dst
// at dstOff.
func composeRect(dst *image.RGBA, dstOff image.Point, src *image.RGBA, srcOff image.Point, size image.Point, blend bool) {
	for y := 0; y < size.Y; y++ {
		d := dst.Pix[dst.PixOffset(dstOff.X, dstOff.Y+y):][:size.X*4]
		o := src.Pix[src.PixOffset(srcOff.X, srcOff.Y+y):][:size.X*4]
		if !blend {
			copy(d, o)
			continue
		}
		for x := 0; x < size.X*4; x += 4 {
			a := uint32(o[x+3])
			if a == 0 {
				continue
			}
			if a == 0xff {
				copy(d[x:x+4], o[x:x+4])
				continue
			}
			inv := 0xff - a
			for i := range 4 {
				d[x+i] = byte(uint32(o[x+i]) + (uint32(d[x+i])*inv+127)/255)
			}
		}
	}
}
