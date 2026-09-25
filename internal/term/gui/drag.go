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

package gui

import (
	ebiten "github.com/hajimehoshi/ebiten/v2"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// DragEventKind classifies a stage of a file drag over the GUI window.
type DragEventKind int

const (
	// DragHover reports the current position of files dragged over the
	// window. It repeats whenever the position changes.
	DragHover DragEventKind = iota
	// DragLeave reports that the drag left the window or was released.
	DragLeave
	// DragDrop reports files released on the window.
	DragDrop
)

// DragEvent describes a file drag over the GUI window. Pos is in cell
// coordinates; Paths is set for DragDrop only.
type DragEvent struct {
	Kind  DragEventKind
	Pos   term.Coordinates
	Paths []string
}

// dragPoller turns ebiten's per-frame drag state into drag transitions.
type dragPoller struct {
	mouse *mouse
	// observer is never nil; hosts install theirs with WithDragObserver.
	observer func(DragEvent)
	// position and paths are the ebiten accessors, replaced in tests.
	position func() (int, int, bool)
	paths    func() []string

	hovering bool
	last     term.Coordinates
	// tracked records that the host reported a position for the drag in
	// progress. Hosts that do not report drag positions leave it false,
	// and the drop falls back to the mouse cursor.
	tracked bool
}

func newDragPoller(m *mouse) *dragPoller {
	return &dragPoller{
		mouse:    m,
		observer: func(DragEvent) {},
		position: ebiten.DraggingPosition,
		paths:    ebiten.DroppedFilePaths,
	}
}

// poll reports the drag transitions observed since the previous frame and
// returns whether anything changed, so the caller can force a repaint.
func (d *dragPoller) poll() bool {
	var changed bool
	x, y, dragging := d.position()
	pos := d.mouse.clamp(d.mouse.cellAt(float64(x), float64(y)))
	switch {
	case dragging:
		d.tracked = true
		if !d.hovering || pos != d.last {
			d.hovering = true
			d.last = pos
			d.observer(DragEvent{Kind: DragHover, Pos: pos})
			changed = true
		}
	case d.hovering:
		d.hovering = false
		d.observer(DragEvent{Kind: DragLeave, Pos: d.last})
		changed = true
	}

	// The host reports the drop after ending the drag, so the veil is
	// already gone by the time the paths arrive. The reported position
	// still describes where the files were released, which is where the
	// drop belongs: the mouse cursor is stale during a drag, so using it
	// would send every drop to the window clicked last.
	if paths := d.paths(); len(paths) > 0 {
		drop := pos
		if !d.tracked {
			drop = d.mouse.clampedCoordinates()
		}
		d.hovering = false
		d.tracked = false
		d.observer(DragEvent{
			Kind:  DragDrop,
			Pos:   drop,
			Paths: paths,
		})
		changed = true
	}
	return changed
}

// idle reports whether poll would observe no transition and notify no
// observer, letting the caller skip work that has to hold the UI lock.
// Both accessors report per-tick state, so probing them does not consume
// the transition poll would otherwise see.
func (d *dragPoller) idle() bool {
	_, _, dragging := d.position()
	return !dragging && !d.hovering && len(d.paths()) == 0
}
