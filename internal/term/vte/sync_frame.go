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

package vte

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// syncFrame records a Draw so it can be shown again while a program
// holds a synchronized update (DEC mode 2026), the way kitty pauses
// rendering: the program's output is parsed and its queries answered
// as it arrives, but the screen keeps the frame from before the update.
//
// Writes are replayed in order, so whatever the recorded Draw left in
// the writer is reproduced exactly, cells it skipped included.
type syncFrame struct {
	ops    []syncFrameOp
	unions []term.Attributes
	images []term.Image

	cursorPos     term.Coordinates
	cursorStyle   term.CursorStyle
	cursorVisible bool
}

// syncFrameOp is a SetCell, or a UnionAttributes of unions[union-1]
// when union is positive.
type syncFrameOp struct {
	pos   term.Coordinates
	cell  term.Cell
	union int
}

var _ term.Writer = (*syncFrame)(nil)

func (f *syncFrame) Context() context.Context {
	return context.Background()
}

func (f *syncFrame) SetCell(pos term.Coordinates, c term.Cell) {
	f.ops = append(f.ops, syncFrameOp{pos: pos, cell: c})
}

func (f *syncFrame) UnionAttributes(pos term.Coordinates, attrs term.Attributes) {
	f.unions = append(f.unions, attrs)
	f.ops = append(f.ops, syncFrameOp{pos: pos, union: len(f.unions)})
}

// DrawImage records img; whether the writer the frame is replayed to
// renders graphics is only known then.
func (f *syncFrame) DrawImage(img term.Image) bool {
	f.images = append(f.images, img)
	return true
}

func (f *syncFrame) replay(w term.Writer) {
	for _, op := range f.ops {
		if op.union > 0 {
			w.UnionAttributes(op.pos, f.unions[op.union-1])
		} else {
			w.SetCell(op.pos, op.cell)
		}
	}
	for _, img := range f.images {
		if !w.DrawImage(img) {
			break
		}
	}
}

// reset empties the frame, keeping its storage for the next one but
// not the pixels and cell extras it referenced.
func (f *syncFrame) reset() {
	clear(f.ops)
	clear(f.images)
	f.ops = f.ops[:0]
	f.unions = f.unions[:0]
	f.images = f.images[:0]
}
