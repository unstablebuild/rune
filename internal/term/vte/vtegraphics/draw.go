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
	"github.com/unstablebuild/rune-go-sdk/term"
)

// View describes the screen area placements are drawn into.
type View struct {
	// Width and Height are the screen size in cells.
	Width, Height int
	// ScrolledBy is how many rows the view is scrolled back into
	// history; placements move down by as much.
	ScrolledBy int
	// Cell is the cell pixel size.
	Cell CellSize
}

// cellRefLookup resolves the top-left screen cell of the placeholder
// cells showing a virtual placement.
type cellRefLookup func(imageID uint32, virt *Placement) (row, col int, found bool)

// belowBackgroundZ is the z below which a placement is composited under
// the cell backgrounds rather than merely under the text (spec §8.5).
const belowBackgroundZ = -1073741824

// layerForZ maps a z-index onto the three compositing layers
// (spec §8.5).
func layerForZ(z int32) term.ImageLayer {
	switch {
	case z < belowBackgroundZ:
		return term.ImageLayerBelowBackground
	case z < 0:
		return term.ImageLayerBelowText
	default:
		return term.ImageLayerAboveText
	}
}

type drawItem struct {
	img   term.Image
	z     int32
	image uint64
	ref   uint64
}

// runsAnchor is the minimum row and column over the placeholder cells
// showing virt, as kitty's resolve_cell_ref.
func runsAnchor(runs []PlaceholderRun, imageID uint32, virt *Placement) (row, col int, found bool) {
	for _, run := range runs {
		if run.ImageID != imageID {
			continue
		}
		if run.PlacementID != 0 && run.PlacementID != virt.ClientID {
			continue
		}
		if !found || run.Row < row {
			row = run.Row
		}
		if !found || run.Col < col {
			col = run.Col
		}
		found = true
	}
	return row, col, found
}
