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

package dialoguetui

import (
	"net/url"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/mouse"
	"github.com/unstablebuild/rune-go-sdk/term"
	tterm "unstable.build/rune/internal/term"
)

func newMouseDelegate(
	grid *tterm.SelectionWriter,
	comp *Component,
) *mouseDelegate {
	var list *component.ResponsiveList
	if comp != nil {
		list = &comp.messages
	}
	return &mouseDelegate{grid: grid, list: list, comp: comp}
}

var _ mouse.Delegate = (*mouseDelegate)(nil)

type mouseDelegate struct {
	grid   *tterm.SelectionWriter
	list   *component.ResponsiveList
	comp   *Component
	offset term.Coordinates // messages-area offset within the grid, updated each Draw
	// sel holds both selection endpoints in content coordinates: each Y is a
	// row index into the full conversation, independent of the scroll offset.
	// Both endpoints therefore stay pinned to their content rows however the
	// list scrolls, and copy reads them directly from the full grid.
	sel tterm.SelRange
	// anchor is the selection start in content coordinates, kept so a drag's
	// SetSelectionEnd can re-sort against the originally pressed cell.
	anchor term.Coordinates
	// fullGrid renders the entire messages list on demand so Selection can
	// extract text from rows that have scrolled out of the visible viewport.
	// The on-screen grid only ever holds the visible viewport, so reading it
	// would clip the selection to whatever is currently shown.
	fullGrid tterm.SelectionWriter
}

func (d *mouseDelegate) OnAction(ev term.Event, pos term.Coordinates, action mouse.Action) bool {
	if action != mouse.LeftClick || d.comp == nil || d.comp.cfg.OnLinkClick == nil {
		return false
	}
	link := d.comp.LinkAt(pos)
	if link == nil || link.URL == "" {
		return false
	}
	parsed, err := url.Parse(link.URL)
	if err != nil {
		return false
	}
	return d.comp.cfg.OnLinkClick(parsed)
}

func (d *mouseDelegate) ScrollUp(n int) (ok bool) {
	for range n {
		if d.list.SeekUp() {
			ok = true
		}
	}
	return
}

func (d *mouseDelegate) ScrollDown(n int) (ok bool) {
	for range n {
		if d.list.SeekDown() {
			ok = true
		}
	}
	return
}

func (d *mouseDelegate) SetSelectionStart(pos term.Coordinates) {
	d.sel = tterm.SelRange{}
	d.anchor = d.toContent(pos)
}

func (d *mouseDelegate) SetSelectionEnd(pos term.Coordinates) {
	start, end := term.CoordinatesSort(d.anchor, d.toContent(pos))
	d.sel = tterm.SelRange{Start: start, End: end, Active: true}
}

func (d *mouseDelegate) ClearSelection() {
	d.sel = tterm.SelRange{}
	d.anchor = term.Coordinates{}
}

func (d *mouseDelegate) SelectWordAt(pos term.Coordinates) {
	gridPos := term.CoordinatesSum(pos, d.offset)
	start, end, ok := d.grid.WordBoundsAt(gridPos)
	if !ok {
		d.sel = tterm.SelRange{}
		return
	}
	d.sel = tterm.SelRange{
		Start:  d.toContent(term.CoordinatesDiff(start, d.offset)),
		End:    d.toContent(term.CoordinatesDiff(end, d.offset)),
		Active: true,
	}
}

func (d *mouseDelegate) SelectLine(y int) {
	gridY := y + d.offset.Y
	start, end, ok := d.grid.LineBounds(gridY)
	if !ok {
		d.sel = tterm.SelRange{}
		return
	}
	d.sel = tterm.SelRange{
		Start:  d.toContent(term.CoordinatesDiff(start, d.offset)),
		End:    d.toContent(term.CoordinatesDiff(end, d.offset)),
		Active: true,
	}
}

func (d *mouseDelegate) Selection() (string, bool) {
	if !d.sel.Active {
		return "", false
	}
	start, end, ok := d.renderFullGrid()
	if !ok {
		return "", false
	}
	text := d.fullGrid.TextBetween(start, end)
	return text, text != ""
}

// renderFullGrid draws the entire messages list into d.fullGrid and returns
// the selection endpoints translated into that full-content grid. The visible
// grid only holds the current viewport, so off-screen selected rows must be
// re-rendered here to be copyable.
func (d *mouseDelegate) renderFullGrid() (start, end term.Coordinates, ok bool) {
	width := d.list.SizeWidth()
	if width <= 0 {
		return term.Coordinates{}, term.Coordinates{}, false
	}

	savedHeight := d.list.SizeHeight()
	savedOffset := d.list.Offset()
	total := d.list.MaxOffset() + savedHeight
	if total <= 0 {
		return term.Coordinates{}, term.Coordinates{}, false
	}

	d.fullGrid.Resize(width, total)
	d.fullGrid.SetContext(d.grid.Context())

	d.list.Resize(width, total)
	d.list.SeekStart()
	d.list.Draw(&d.fullGrid)

	d.list.Resize(width, savedHeight)
	d.restoreOffset(savedOffset)

	// d.sel is already in content coordinates and the full grid is rendered
	// fully sought toward the start, so content row r maps directly to grid row
	// r.
	return d.sel.Start, d.sel.End, true
}

// restoreOffset seeks the list back to the given raw seek offset. SeekUp and
// SeekDown are alignment-flipped, so this drives Offset() directly rather than
// reasoning about visual direction.
func (d *mouseDelegate) restoreOffset(offset int) {
	for d.list.Offset() < offset {
		if d.list.Alignment == component.AlignmentBottom {
			if !d.list.SeekUp() {
				break
			}
		} else if !d.list.SeekDown() {
			break
		}
	}
	for d.list.Offset() > offset {
		if d.list.Alignment == component.AlignmentBottom {
			if !d.list.SeekDown() {
				break
			}
		} else if !d.list.SeekUp() {
			break
		}
	}
}

func (d *mouseDelegate) Width() int {
	return d.grid.Width()
}

func (d *mouseDelegate) Height() int {
	return d.grid.Height()
}

// WindowSelection converts the content-coordinate selection into the current
// messages-relative window coordinates for the on-screen highlight. It returns
// false when there is no active selection.
func (d *mouseDelegate) WindowSelection() (tterm.SelRange, bool) {
	if !d.sel.Active {
		return tterm.SelRange{}, false
	}
	return tterm.SelRange{
		Start:  d.toWindow(d.sel.Start),
		End:    d.toWindow(d.sel.End),
		Active: true,
	}, true
}

// toContent maps a messages-relative window coordinate to a content
// coordinate whose Y is a row index into the full conversation, independent of
// the current scroll offset.
func (d *mouseDelegate) toContent(pos term.Coordinates) term.Coordinates {
	pos.Y += d.list.MaxOffset() - d.list.Offset()
	return pos
}

// toWindow is the inverse of toContent for the current scroll offset.
func (d *mouseDelegate) toWindow(pos term.Coordinates) term.Coordinates {
	pos.Y -= d.list.MaxOffset() - d.list.Offset()
	return pos
}
