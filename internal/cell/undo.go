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

package cell

import (
	"context"

	"github.com/unstablebuild/rune-go-sdk/term"
)

// undoer adds undo and redo methods to a otherwise, irreversible cell.writer.
// It satifies the cell.writer interface and it should be used as a replacement.
type undoer struct {
	version         int
	w               Editor
	undoTimeline    []edits
	redoTimeline    []edits
	mergeGroupStart int
}

type edits struct {
	Version int
	Edits   []edit
}

// newUndoer returns new instance of undoer to undo/redo operations of w.
func newUndoer(w Editor) *undoer {
	u := new(undoer)
	u.init(w)
	return u
}

// Init initializes this undoer to undo/redo operations of w.
func (u *undoer) init(w Editor) {
	u.w = w
	u.undoTimeline = make([]edits, 0)
	u.redoTimeline = make([]edits, 0)
}

func popLastOp(timeline []edits) ([]edits, edits, bool) {
	lastCmd := len(timeline) - 1
	if lastCmd < 0 {
		return nil, edits{}, false
	}
	op := timeline[lastCmd]
	return timeline[:lastCmd], op, true
}

func (u *undoer) startMergeUndo() bool {
	u.mergeGroupStart = len(u.undoTimeline)
	return true
}

func (u *undoer) endMergeUndo() bool {
	return u.mergeFrom(u.mergeGroupStart)
}

// group runs fn and merges the undo entries its edits push into one. An
// enclosing startMergeUndo scope keeps covering the merged entry, even when
// that scope was opened while fn ran.
func (u *undoer) group(fn func()) {
	start := len(u.undoTimeline)
	fn()
	if u.mergeFrom(start) && u.mergeGroupStart > start {
		u.mergeGroupStart = start
	}
}

func (u *undoer) mergeFrom(start int) bool {
	if start >= len(u.undoTimeline) {
		return false
	}
	tail := u.undoTimeline[len(u.undoTimeline)-1]
	grouped := edits{Version: tail.Version}
	for i := len(u.undoTimeline) - 1; i >= start; i-- {
		op := u.undoTimeline[i]
		grouped.Edits = append(grouped.Edits, op.Edits...)
	}

	u.undoTimeline = u.undoTimeline[:start]
	u.undoTimeline = append(u.undoTimeline, grouped)

	return true
}

func (u *undoer) redo() (bool, term.Coordinates) {
	redoTimeline, ed, ok := popLastOp(u.redoTimeline)
	if !ok {
		return false, term.Coordinates{}
	}
	if ed.Version != u.version || len(ed.Edits) == 0 {
		return false, term.Coordinates{}
	}
	u.redoTimeline = redoTimeline
	u.version += len(ed.Edits)

	var op edits
	op.Version = u.version
	op.Edits = make([]edit, len(ed.Edits))
	for i, ed := range ed.Edits {
		from, to, old := u.w.Edit(context.Background(), ed.From, ed.To, ed.String)
		ed.From = from
		ed.To = to
		ed.String = old
		op.Edits[len(op.Edits)-i-1] = ed
	}

	u.pushUndo(op)
	return ok, op.Edits[0].To
}

func (u *undoer) undo() (bool, term.Coordinates) {
	if u.version == 0 {
		return false, term.Coordinates{}
	}
	undoTimeline, ed, ok := popLastOp(u.undoTimeline)
	if !ok {
		return false, term.Coordinates{}
	}
	if ed.Version != u.version || len(ed.Edits) == 0 {
		return false, term.Coordinates{}
	}
	u.undoTimeline = undoTimeline
	u.version -= len(ed.Edits)

	var op edits
	op.Version = u.version
	op.Edits = make([]edit, len(ed.Edits))
	for i, ed := range ed.Edits {
		from, to, old := u.w.Edit(context.Background(), ed.From, ed.To, ed.String)
		ed.From = from
		ed.To = to
		ed.String = old
		op.Edits[len(op.Edits)-i-1] = ed
	}
	u.pushRedo(op)

	return ok, op.Edits[0].From
}

func (u *undoer) pushUndo(cmd edits) {
	u.undoTimeline = append(u.undoTimeline, cmd)
}
func (u *undoer) pushRedo(cmd edits) {
	u.redoTimeline = append(u.redoTimeline, cmd)
}

func (u *undoer) resetRedoTimeline() {
	// Clear element slots: edits carries edit strings that would otherwise
	// stay reachable past [:0] until subsequent appends overwrite them.
	clear(u.redoTimeline)
	u.redoTimeline = u.redoTimeline[:0]
}

// Edit is an edit operation on a Buffer.
type edit struct {
	From   term.Coordinates
	To     term.Coordinates
	String string
}

// update captures underlying writer update so it can be undone. See cell.writer.Edit
func (u *undoer) Edit(ctx context.Context, start, end term.Coordinates, str string) (
	from, to term.Coordinates, old string,
) {
	u.version++
	from, to, old = u.w.Edit(ctx, start, end, str)

	var op edit
	op.From = from
	op.To = to
	op.String = old

	u.pushUndo(edits{Version: u.version, Edits: []edit{op}})
	u.resetRedoTimeline()
	return
}
