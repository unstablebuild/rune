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

package browser

import (
	"image"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
)

// winDropZone is the region of a tile the cursor hovers while a
// floating window is dragged by its bar.
type winDropZone uint8

const (
	winDropNone winDropZone = iota
	winDropLeft
	winDropRight
	winDropTop
	winDropBottom
	winDropCenter
)

const (
	// winDropBandDiv makes each edge band a fifth of its axis, and the
	// center box a fifth of the tile on both axes.
	winDropBandDiv = 5
	// winDropTabIcon mirrors the :windowconverttab default icon.
	winDropTabIcon        = '\ueaee'
	winDropDefaultTabName = "window"
	winDropSplitLabel     = "Split here"
	winDropConvertLabel   = "Convert to tab"
)

// winDropState tracks the live pre-split preview of a bar drag.
type winDropState struct {
	src    *browserWindow
	target *browserWindow
	// region is the target's geometry as measured *before* the
	// pre-split, which is what the user is aiming at: the real split
	// shrinks the target, and inserting the placeholder as a new
	// sibling redistributes the others, so neither the live target
	// nor the region the split produced can be used to place zones.
	region  winDropRect
	zone    winDropZone
	preview *browserWindow
}

// winDropRect is a region of the window manager surface.
type winDropRect struct {
	pos  term.Coordinates
	w, h int
}

func windowRect(win *browserWindow) winDropRect {
	return winDropRect{pos: win.Position(), w: win.Width(), h: win.Height()}
}

func (r winDropRect) contains(pos term.Coordinates) bool {
	return pos.X >= r.pos.X && pos.X < r.pos.X+r.w &&
		pos.Y >= r.pos.Y && pos.Y < r.pos.Y+r.h
}

// local maps pos into r, clamping it to the nearest cell of r when it
// falls outside.
func (r winDropRect) local(pos term.Coordinates) term.Coordinates {
	return term.Coordinates{
		X: min(max(pos.X-r.pos.X, 0), r.w-1),
		Y: min(max(pos.Y-r.pos.Y, 0), r.h-1),
	}
}

// dropZoneAt maps a tile-local position to its drop zone. Corners
// resolve to the axis whose edge is proportionally closer.
func dropZoneAt(local term.Coordinates, w, h int) winDropZone {
	if w <= 0 || h <= 0 ||
		local.X < 0 || local.Y < 0 || local.X >= w || local.Y >= h {
		return winDropNone
	}

	if inCenterBox(local.X, w) && inCenterBox(local.Y, h) {
		return winDropCenter
	}

	bandW := max(1, w/winDropBandDiv)
	bandH := max(1, h/winDropBandDiv)

	distX, horizontal := -1, winDropNone
	if local.X < bandW {
		distX, horizontal = local.X, winDropLeft
	} else if local.X >= w-bandW {
		distX, horizontal = w-1-local.X, winDropRight
	}
	distY, vertical := -1, winDropNone
	if local.Y < bandH {
		distY, vertical = local.Y, winDropTop
	} else if local.Y >= h-bandH {
		distY, vertical = h-1-local.Y, winDropBottom
	}

	switch {
	case horizontal == winDropNone:
		return vertical
	case vertical == winDropNone:
		return horizontal
	case distY*w < distX*h:
		return vertical
	default:
		return horizontal
	}
}

// inCenterBox reports whether v lies in the middle fifth of dim.
func inCenterBox(v, dim int) bool {
	size := max(1, dim/winDropBandDiv)
	start := (dim - size) / 2
	return v >= start && v < start+size
}

func (z winDropZone) orientation() browserapi.Orientation {
	switch z {
	case winDropLeft:
		return browserapi.OrientationLeft
	case winDropRight:
		return browserapi.OrientationRight
	case winDropTop:
		return browserapi.OrientationTop
	case winDropBottom:
		return browserapi.OrientationBottom
	default:
		panic("zone does not split")
	}
}

const (
	dragVeilFPS         = 24
	dragVeilPeriodFrame = 36
	dragVeilIntensity   = 0.45
	defaultDropLabel    = "Drop files here"
)

// dragState is the drop-target overlay state. Every field except frame
// is owned by the event loop; frame is advanced by the veil ticker.
type dragState struct {
	win    *browserWindow
	cancel func()
	buf    *cell.BufferWriter
	bufW   int
	bufH   int
	frame  atomic.Int64
}

// veilRect is a region excluded from the veil.
type veilRect struct {
	pos           term.Coordinates
	width, height int
}

// veilCells dims the target rect by forcing the veil attributes onto
// every cell outside skip while keeping the content underneath legible.
func veilCells(
	cells [][]term.Cell, pos term.Coordinates,
	width, height int, attr term.Attributes, skip *veilRect,
) {
	forEachCell(cells, pos, width, height, skip, func(c *term.Cell) {
		if attr.Bg != term.ColorDefault {
			c.Bg = attr.Bg
		}
		if attr.Fg != term.ColorDefault {
			c.Fg = attr.Fg
		}
	})
}

// writeCenteredLabel writes label on the middle row of the target rect,
// keeping each cell's background so the veil tint underneath shows
// through. It returns the cells it wrote, empty when the label does not
// fit.
func writeCenteredLabel(
	cells [][]term.Cell, pos term.Coordinates,
	width, height int, label string, attr term.Attributes,
) image.Rectangle {
	runes := []rune(label)
	if len(runes) == 0 || len(runes) > width {
		return image.Rectangle{}
	}
	y := pos.Y + height/2
	if y < 0 || y >= len(cells) {
		return image.Rectangle{}
	}
	attr.Attrs |= term.AttrBold
	row := cells[y]
	start := pos.X + (width-len(runes))/2
	span := image.Rect(start, y, start+len(runes), y+1).
		Intersect(image.Rect(0, y, len(row), y+1))
	for x := span.Min.X; x < span.Max.X; x++ {
		cellAttr := attr
		cellAttr.Bg = row[x].Bg
		row[x] = term.NewCell(runes[x-start], 1, cellAttr)
	}
	return span
}

func forEachCell(
	cells [][]term.Cell, pos term.Coordinates,
	width, height int, skip *veilRect, fn func(*term.Cell),
) {
	for y := pos.Y; y < pos.Y+height; y++ {
		if y < 0 || y >= len(cells) {
			continue
		}
		row := cells[y]
		for x := pos.X; x < pos.X+width; x++ {
			if x < 0 || x >= len(row) {
				continue
			}
			if skip != nil &&
				x >= skip.pos.X && x < skip.pos.X+skip.width &&
				y >= skip.pos.Y && y < skip.pos.Y+skip.height {
				continue
			}
			fn(&row[x])
		}
	}
}
