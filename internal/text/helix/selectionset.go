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

package helix

import (
	"unicode"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/text"
)

// The handler keeps the selection set in document space and text.Cursor
// mirrors only the primary. readRange and installRange are the two
// bridges between them; everything else goes through setSelection so
// the set is always normalized and the primary always installed.
//
// A linewise cursor selection is the Helix range from the start of its
// first line to the start of the line after its last, which is what x
// and extend_line_below build there, and any range of that shape is
// installed back as a linewise selection so d, y and p keep their
// whole-line semantics.
//
// Commands that Helix writes as Selection::transform run here either
// as a pure function over the set, when the cursor has nothing to
// contribute, or through forEachRange, which lends the cursor to each
// range in turn so the primitives text.Cursor already implements can
// serve all of them.

// adoptCursor folds the cursor back into the selection set after an
// event: whatever it holds becomes the primary range and the other
// ranges are carried through any edit made since the last sync. That
// is what turns a pinned selection (a text object, select all, a
// syntax expansion) back into an anchor/head pair the next motion can
// extend.
func (h *helixHandlerImpl) adoptCursor() {
	h.carryEdits()
	r := h.readRange()
	if h.sel.len() == 0 {
		h.setSelection(single(r))
		return
	}
	p := h.sel.primaryRange()
	if r.same(p) {
		h.syncPrimary()
		return
	}
	r.col = h.desiredCol(r)
	h.setSelection(h.sel.replace(h.sel.primary, r))
}

// resetSelection drops every range but what the cursor holds. It is
// what a mouse click, the IDE placing the caret and a location list
// jump do, all of which Helix models as Selection::point.
func (h *helixHandlerImpl) resetSelection() {
	h.carryEdits()
	h.setSelection(single(h.readRange()))
}

// carryEdits maps the selection set and the jumplist through every
// edit recorded since the last sync, which is how Helix keeps a view's
// selection valid across a transaction it did not make itself. It
// reports whether there was anything to carry.
func (h *helixHandlerImpl) carryEdits() bool {
	cs := h.rec.take()
	if len(cs) == 0 {
		return false
	}
	h.sel = h.sel.mapThrough(cs)
	for i := range h.jumps {
		h.jumps[i] = h.jumps[i].mapThrough(cs)
	}
	return true
}

// desiredCol is the column the primary's vertical motions set out
// from. The handler keeps it in anchor because the cursor is clamped
// onto the row it lands on, and only a column further right than the
// caret is worth remembering.
func (h *helixHandlerImpl) desiredCol(r rng) int {
	c := r.cursor(h.buf())
	if h.anchor.Y == c.Y && h.anchor.X > c.X {
		return h.anchor.X
	}
	return 0
}

// setSelection replaces the selection set and installs its primary.
func (h *helixHandlerImpl) setSelection(s selection) {
	h.sel = h.ensureInvariants(s.normalized())
	h.syncPrimary()
}

// transformSelection is Selection::transform followed by
// set_selection, reporting whether any range moved.
func (h *helixHandlerImpl) transformSelection(fn func(rng) rng) bool {
	before := h.sel
	h.setSelection(before.transform(fn))
	return !h.sel.equal(before)
}

// ensureInvariants is Selection::ensure_invariants: outside insert mode
// every range covers at least one cell. A caret on a line ending stays
// a point: the cell model has no cell there to widen onto, so the
// one-cell selection the cursor shows on an empty row covers no text.
func (h *helixHandlerImpl) ensureInvariants(s selection) selection {
	if h.currMode == insertMode {
		return s
	}
	buf := h.buf()
	return s.transform(func(r rng) rng {
		if !r.isPoint() {
			return r
		}
		if next, ok := nextPos(buf, r.head); ok && next.Y == r.head.Y {
			r.head = next
		}
		return r
	})
}

// syncPrimary installs the primary range into the cursor and refreshes
// the highlight of the other ranges.
func (h *helixHandlerImpl) syncPrimary() {
	before := h.cursor.CursorAtScroll()
	h.installRange(h.sel.primaryRange())
	if h.cursor.CursorAtScroll() != before {
		h.anchor = h.cursorAtScroll()
	}
	h.markSecondaries()
}

// markSecondaries draws every non-primary range through two location
// lists: the covered cells in reverse and, on top, the cell the range's
// cursor sits on dimmed so it reads as a ghost caret. The message bar
// carries the count the Helix status line would show.
func (h *helixHandlerImpl) markSecondaries() {
	if h.sel.len() < 2 {
		if h.secondariesDrawn {
			h.cursor.SetLocationList(textapi.LocationPriorityCritical, selectionsLocID, nil)
			h.cursor.SetLocationList(textapi.LocationPriorityCritical, cursorsLocID, nil)
			h.less.SetMessage("")
			h.secondariesDrawn = false
		}
		return
	}
	buf := h.buf()
	var sels, carets []textapi.Location
	for i, r := range h.sel.ranges {
		if i == h.sel.primary {
			continue
		}
		sels = append(sels, rowLocations(buf, r, term.Attributes{Attrs: term.AttrReverse})...)
		c := clampCell(buf, r.cursor(buf))
		carets = append(carets, textapi.Location{
			From: c, To: term.Coordinates{X: c.X + 1, Y: c.Y},
			Attr: term.Attributes{Attrs: term.AttrReverse | term.AttrDim},
		})
	}
	h.cursor.SetLocationList(textapi.LocationPriorityCritical, selectionsLocID,
		textapi.LocationSlice(sels))
	h.cursor.SetLocationList(textapi.LocationPriorityCritical, cursorsLocID,
		textapi.LocationSlice(carets))
	h.less.SetMessage("%d/%d sels", h.sel.primary+1, h.sel.len())
	h.secondariesDrawn = true
}

// rowLocations splits a range into one location per row it covers.
func rowLocations(buf *cell.Buffer, r rng, attr term.Attributes) []textapi.Location {
	from, to := r.from(), r.to()
	var locs []textapi.Location
	for y := from.Y; y <= to.Y && y < buf.Rows(); y++ {
		start := term.Coordinates{Y: y}
		if y == from.Y {
			start.X = from.X
		}
		end := term.Coordinates{Y: y, X: buf.Columns(y)}
		if y == to.Y {
			end.X = min(to.X, end.X)
		}
		if end.X <= start.X {
			continue
		}
		locs = append(locs, textapi.Location{From: start, To: end, Attr: attr})
	}
	return locs
}

// readRange reads the cursor's selection as a Helix range in document
// space, where head is the exclusive edge of the range. Without a
// selection the caret is a point.
func (h *helixHandlerImpl) readRange() rng {
	from, to, ok := h.cursor.SelectionRange()
	if !ok {
		caret := h.cursor.CursorAtScroll()
		return rng{anchor: caret, head: caret}
	}
	buf := h.buf()
	if mode, _ := h.cursor.SelectionMode(); mode == text.LineSelection {
		from = term.Coordinates{Y: from.Y}
		to = term.Coordinates{Y: min(to.Y+1, buf.Rows())}
	} else {
		to = docTo(buf, to)
	}
	if h.selectionBackward() && !h.anchorClamped() {
		return rng{anchor: to, head: from}
	}
	return rng{anchor: from, head: to}
}

// anchorClamped reports a cursor artefact: a caret that overshot a
// row is clamped back onto its last cell, but the anchor dropped on it
// beforehand sits on the line-ending slot one column further. That is
// the one-cell range under the caret, not a backward one.
func (h *helixHandlerImpl) anchorClamped() bool {
	anchor, caret, ok := h.cursor.SelectionBounds()
	return ok && anchor.Y == caret.Y &&
		anchor.X == h.buf().Columns(anchor.Y) && caret.X+1 == anchor.X
}

// docTo maps the far edge of a cell selection into document space. The
// cursor reports it one column past the caret, which for a caret on the
// line-ending slot lands past the last position the row has; that edge
// is the line ending itself, so the range covers no more than the row.
func docTo(buf *cell.Buffer, to term.Coordinates) term.Coordinates {
	if to.Y < 0 || to.Y >= buf.Rows() || to.X <= buf.Columns(to.Y) {
		return to
	}
	return term.Coordinates{X: buf.Columns(to.Y), Y: to.Y}
}

// lineShaped reports whether r runs from a line start to a line start,
// which is the shape a linewise selection reads back as.
func lineShaped(r rng) bool {
	from, to := r.from(), r.to()
	return from.X == 0 && to.X == 0 && to.Y > from.Y
}

// installRange makes r the cursor's selection. It is a no-op when the
// cursor already holds exactly that, so re-installing after every event
// costs nothing on the common path. Outside insert mode a point is the
// one-cell selection anchored where the caret is, line-ending slot
// included: the next event's bounds correction clamps it, as it does
// for a caret the IDE parks there.
func (h *helixHandlerImpl) installRange(r rng) {
	buf := h.buf()
	if buf.Rows() == 0 {
		return
	}
	anchor, head := r.anchor, r.head
	var anchorCell, caret term.Coordinates
	mode := text.StandardSelection
	switch {
	case r.isPoint() && h.currMode == insertMode:
		caret = clampInsert(buf, head)
		if _, ok := h.cursor.SelectionMode(); !ok && h.cursor.CursorAtScroll() == caret {
			return
		}
		h.cursor.Unselect()
		h.cursor.MoveToScroll(caret)
		return
	case r.isPoint():
		anchorCell = clampInsert(buf, head)
		caret = anchorCell
	case lineShaped(r):
		mode = text.LineSelection
		first, last := r.from().Y, r.to().Y-1
		lastCell := clampCell(buf, term.Coordinates{X: buf.Columns(last), Y: last})
		if r.backward() {
			anchorCell, caret = lastCell, term.Coordinates{Y: first}
		} else {
			anchorCell, caret = term.Coordinates{Y: first}, lastCell
		}
	case coordinatesBefore(anchor, head):
		last, ok := prevPos(buf, head)
		if !ok {
			last = anchor
		}
		anchorCell, caret = clampCell(buf, anchor), clampCell(buf, last)
	default:
		first, ok := prevPos(buf, anchor)
		if !ok {
			first = head
		}
		anchorCell, caret = clampCell(buf, first), clampCell(buf, head)
	}
	if h.cursorHolds(mode, anchorCell, caret) {
		return
	}
	if mode == text.LineSelection {
		h.cursor.Unselect()
		h.cursor.SetCursorAtScroll(anchorCell)
		h.cursor.SelectLine()
		h.cursor.MoveToScroll(caret)
		return
	}
	h.setSelectionRange(anchorCell, caret)
}

// cursorHolds reports whether the cursor's selection is exactly the
// anchor/caret pair in the given mode. A pinned selection never
// matches: its far edge is exclusive where the caret cell is not.
func (h *helixHandlerImpl) cursorHolds(mode text.SelectMode, anchor, caret term.Coordinates) bool {
	m, ok := h.cursor.SelectionMode()
	if !ok || m != mode {
		return false
	}
	a, c, ok := h.cursor.SelectionBounds()
	return ok && a == anchor && c == caret && h.cursor.CursorAtScroll() == caret
}

// clampInsert clamps a document position onto the cells the caret can
// occupy in insert mode, which includes the slot past the last cell.
func clampInsert(buf *cell.Buffer, pos term.Coordinates) term.Coordinates {
	rows := buf.Rows()
	if rows == 0 {
		return term.Coordinates{}
	}
	if pos.Y >= rows {
		return term.Coordinates{X: buf.Columns(rows - 1), Y: rows - 1}
	}
	pos.Y = max(0, pos.Y)
	pos.X = max(0, min(pos.X, buf.Columns(pos.Y)))
	return pos
}

// clampCursor is the bounds correction every event ends with, applied
// early so a range read back from the cursor mid-operation matches
// what the end of the event would have produced.
func (h *helixHandlerImpl) clampCursor() {
	if !h.config.cursorCorrections {
		return
	}
	if h.mode() == insertMode {
		h.cursor.MoveToBounds(1)
		return
	}
	h.cursor.MoveToBounds(0)
}

// --- fan-out ---

// forEachRange runs op once per range with that range installed in the
// cursor and rebuilds the set from what each run leaves behind. It
// walks from the last range in the document to the first: an edit can
// only shift the text after it, so every range still to be visited
// stays valid and only the results already collected need carrying
// through the recorded changes. With one range the cursor is the
// primary already and op runs as it always has.
func (h *helixHandlerImpl) forEachRange(op func() bool) bool {
	h.carryEdits()
	if h.sel.len() < 2 {
		return op()
	}
	src := h.sel
	buf := h.buf()
	out := selection{ranges: make([]rng, src.len()), primary: src.primary}
	done := false
	for i := src.len() - 1; i >= 0; i-- {
		r := src.ranges[i]
		h.installRange(r)
		h.anchor = h.cursorAtScroll()
		if r.col > h.anchor.X {
			h.anchor.X = r.col
		}
		if op() {
			done = true
		}
		landed := h.cursor.CursorAtScroll()
		h.clampCursor()
		next := h.readRange()
		if c := next.cursor(buf); landed.Y == c.Y && landed.X > c.X {
			next.col = landed.X
		}
		cs := h.rec.take()
		for j := i + 1; j < src.len(); j++ {
			out.ranges[j] = out.ranges[j].mapThrough(cs)
		}
		out.ranges[i] = next
	}
	h.setSelection(out)
	return done
}

// --- selection commands ---

// These are the commands helix-term/src/commands.rs writes as a pure
// function of the selection set; none of them needs the cursor.

// collapseSelection is collapse_selection (;).
func (h *helixHandlerImpl) collapseSelection() bool {
	buf := h.buf()
	return h.transformSelection(func(r rng) rng { return point(r.cursor(buf)) })
}

// flipSelections is flip_selections (A-;).
func (h *helixHandlerImpl) flipSelections() bool {
	return h.transformSelection(rng.flipped)
}

// ensureSelectionsForward is ensure_selections_forward (A-:), which
// makes the following operator direction-independent.
func (h *helixHandlerImpl) ensureSelectionsForward() bool {
	return h.transformSelection(rng.forward)
}

// extendLine is extend_line_impl: x snaps every range to whole lines
// and, once a range already covers its lines, grows it by count lines.
func (h *helixHandlerImpl) extendLine(above bool) bool {
	count := h.motionCount()
	buf := h.buf()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	lineStart := func(y int) term.Coordinates { return term.Coordinates{Y: max(0, min(y, rows))} }
	return h.transformSelection(func(r rng) rng {
		startLine, endLine := r.lineRange(buf)
		start, end := lineStart(startLine), lineStart(endLine+1)
		covered := r.from() == start && r.to() == end
		switch {
		case above && covered:
			return rng{anchor: end, head: lineStart(startLine - count)}
		case above:
			return rng{anchor: end, head: lineStart(startLine - (count - 1))}
		case covered:
			return rng{anchor: start, head: lineStart(endLine + count + 1)}
		default:
			return rng{anchor: start, head: lineStart(endLine + count)}
		}
	})
}

// extendToLineBounds is extend_to_line_bounds (X).
func (h *helixHandlerImpl) extendToLineBounds() bool {
	buf := h.buf()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	return h.transformSelection(func(r rng) rng {
		startLine, endLine := r.lineRange(buf)
		return rng{
			anchor: term.Coordinates{Y: startLine},
			head:   term.Coordinates{Y: min(endLine+1, rows)},
		}.withDirection(r.backward())
	})
}

// shrinkToLineBounds is shrink_to_line_bounds (A-x): the partially
// covered first and last lines are dropped. A range inside one line is
// left alone.
func (h *helixHandlerImpl) shrinkToLineBounds() bool {
	buf := h.buf()
	rows := buf.Rows()
	if rows == 0 {
		return false
	}
	return h.transformSelection(func(r rng) rng {
		startLine, endLine := r.lineRange(buf)
		if startLine == endLine {
			return r
		}
		start := term.Coordinates{Y: startLine}
		end := term.Coordinates{Y: min(endLine+1, rows)}
		if start != r.from() {
			start = term.Coordinates{Y: min(startLine+1, rows)}
		}
		if end != r.to() {
			end = term.Coordinates{Y: endLine}
		}
		return rng{anchor: start, head: end}.withDirection(r.backward())
	})
}

// trimSelections is trim_selections (_): every range loses the
// whitespace at either end, and ranges that were nothing but
// whitespace are dropped. With nothing left the set collapses onto the
// primary's cursor.
func (h *helixHandlerImpl) trimSelections() bool {
	buf := h.buf()
	before := h.sel
	ranges := make([]rng, 0, before.len())
	for _, r := range before.ranges {
		if trimmed, ok := trimRange(buf, r); ok {
			ranges = append(ranges, trimmed)
		}
	}
	if len(ranges) == 0 {
		h.collapseSelection()
		h.keepPrimarySelection()
		return true
	}
	primary := before.primaryRange()
	idx := len(ranges) - 1
	for i, r := range ranges {
		if r.overlaps(primary) {
			idx = i
			break
		}
	}
	h.setSelection(selection{ranges: ranges, primary: idx})
	return !h.sel.equal(before)
}

// trimRange shaves the whitespace off both ends of r, reporting false
// for an empty or all-whitespace range.
func trimRange(buf *cell.Buffer, r rng) (rng, bool) {
	if r.isPoint() {
		return r, false
	}
	start, end := r.from(), r.to()
	for start != end {
		ch, ok := charAt(buf, start)
		if !ok || !unicode.IsSpace(ch) {
			break
		}
		start, _ = nextPos(buf, start)
	}
	if start == end {
		return r, false
	}
	for {
		prev, ok := prevPos(buf, end)
		if !ok {
			break
		}
		if ch, ok := charAt(buf, prev); !ok || !unicode.IsSpace(ch) {
			break
		}
		end = prev
	}
	return rng{anchor: start, head: end}.withDirection(r.backward()), true
}

// keepPrimarySelection is keep_primary_selection (,).
func (h *helixHandlerImpl) keepPrimarySelection() bool {
	h.carryEdits()
	if h.sel.len() < 2 {
		return false
	}
	h.setSelection(single(h.sel.primaryRange()))
	return true
}

// removePrimarySelection is remove_primary_selection (A-,).
func (h *helixHandlerImpl) removePrimarySelection() bool {
	h.carryEdits()
	if h.sel.len() < 2 {
		h.less.SetMessage("no selections remaining")
		return false
	}
	h.setSelection(h.sel.without(h.sel.primary))
	return true
}

// rotateSelections is rotate_selections: ( and ) move the primary
// count ranges along, wrapping at either end.
func (h *helixHandlerImpl) rotateSelections(forward bool) bool {
	h.carryEdits()
	if h.sel.len() < 2 {
		return false
	}
	n := h.motionCount()
	if !forward {
		n = -n
	}
	h.setSelection(h.sel.rotated(n))
	return true
}

// mergeSelections is merge_selections (A-minus).
func (h *helixHandlerImpl) mergeSelections() bool {
	h.carryEdits()
	if h.sel.len() < 2 {
		return false
	}
	h.setSelection(h.sel.merged())
	return true
}

// mergeConsecutiveSelections is merge_consecutive_selections (A-_).
func (h *helixHandlerImpl) mergeConsecutiveSelections() bool {
	h.carryEdits()
	before := h.sel
	h.setSelection(before.mergedConsecutive())
	return !h.sel.equal(before)
}

// splitSelectionOnNewline is split_selection_on_newline (A-s).
func (h *helixHandlerImpl) splitSelectionOnNewline() bool {
	h.carryEdits()
	before := h.sel
	h.setSelection(before.splitOnNewline(h.buf()))
	return !h.sel.equal(before)
}

// copySelectionOnLine is copy_selection_on_line: C and A-C copy every
// range count times onto the following (or preceding) lines, keeping
// each copy on the same columns and skipping lines too short to hold
// them. The last copy of the primary becomes the new primary.
func (h *helixHandlerImpl) copySelectionOnLine(down bool) bool {
	h.carryEdits()
	count := h.motionCount()
	buf := h.buf()
	rows := buf.Rows()
	src := h.sel
	out := selection{ranges: make([]rng, 0, src.len()*(count+1))}
	for i, r := range src.ranges {
		isPrimary := i == src.primary
		// The range is always head exclusive, so the cell the cursor
		// sits on is one before whichever end is further along.
		var head, anchor term.Coordinates
		if coordinatesBefore(r.anchor, r.head) {
			head, _ = prevPos(buf, r.head)
			anchor = r.anchor
		} else {
			head = r.head
			anchor, _ = prevPos(buf, r.anchor)
		}
		height := max(head.Y, anchor.Y) - min(head.Y, anchor.Y) + 1

		if isPrimary {
			out.primary = len(out.ranges)
		}
		out.ranges = append(out.ranges, r)

		for sels, i := 0, 0; sels < count; i++ {
			offset := (i + 1) * height
			anchorRow, headRow := anchor.Y+offset, head.Y+offset
			if !down {
				anchorRow, headRow = max(0, anchor.Y-offset), max(0, head.Y-offset)
			}
			if anchorRow >= rows || headRow >= rows {
				break
			}
			if buf.Columns(anchorRow) >= anchor.X && buf.Columns(headRow) >= head.X {
				if isPrimary {
					out.primary = len(out.ranges)
				}
				a := term.Coordinates{X: anchor.X, Y: anchorRow}
				hd := term.Coordinates{X: head.X, Y: headRow}
				out.ranges = append(out.ranges, point(a).putCursor(buf, hd, true))
				sels++
			}
			if anchorRow == 0 && headRow == 0 {
				break
			}
		}
	}
	h.setSelection(out)
	return !h.sel.equal(src)
}

// --- selection grammar ---

// Helix's selection grammar, mirroring helix-core/src/movement.rs and
// Range::put_cursor in helix-core/src/selection.rs.
//
// Every range is an (anchor, head) pair and is never empty: Helix stores
// selections through Selection::ensure_invariants, which widens a
// collapsed range to one grapheme. text.Cursor with
// RightInclusiveSemantics has the same shape, so a motion is a matter of
// deciding where the anchor lands before moving the head.
//
// In normal mode put_cursor drops a fresh anchor on the landing cell; in
// select mode the existing anchor survives. Word motions additionally
// re-anchor one cell along when the caret already sits on the boundary
// the motion looks for, which is what makes repeated presses walk
// forward instead of standing still (word_move's `head == head_start`
// branch).

// anchorHere drops a fresh anchor on the caret, collapsing the selection
// to the single cell under it.
func (h *helixHandlerImpl) anchorHere() {
	h.cursor.Unselect()
	h.cursor.Select()
}

// setSelectionRange rebuilds the selection as the (anchor, head) pair,
// leaving the caret on head. The anchor is placed without seeking so
// the viewport only ever follows the caret.
func (h *helixHandlerImpl) setSelectionRange(anchor, head term.Coordinates) {
	h.cursor.Unselect()
	h.cursor.SetCursorAtScroll(anchor)
	h.cursor.Select()
	h.cursor.MoveToScroll(head)
}

// selectionAnchor returns the cell the selection is anchored on.
func (h *helixHandlerImpl) selectionAnchor() (term.Coordinates, bool) {
	anchor, _, ok := h.cursor.SelectionBounds()
	return anchor, ok
}

func (h *helixHandlerImpl) selectionBackward() bool {
	anchor, head, ok := h.cursor.SelectionBounds()
	return ok && coordinatesBefore(head, anchor)
}

// runMotion applies step count times to every range. Select mode
// restores the original anchor afterwards, matching extend_word_impl:
// the head is computed as if the motion were unextended, then put back
// on the old anchor.
func (h *helixHandlerImpl) runMotion(step func() bool) bool {
	count := h.motionCount()
	return h.forEachRange(func() bool { return h.applyMotion(step, count) })
}

// runMotionOnce is for motions that consume the count themselves.
func (h *helixHandlerImpl) runMotionOnce(step func() bool) bool {
	return h.forEachRange(func() bool { return h.applyMotion(step, 1) })
}

// primaryMotion applies step to the primary range only, which is what
// Helix's scroll does when it drags the cursor along with the view.
func (h *helixHandlerImpl) primaryMotion(step func() bool) bool {
	h.carryEdits()
	return h.applyMotion(h.caretStep(step), 1)
}

func (h *helixHandlerImpl) applyMotion(step func() bool, times int) bool {
	run := func() bool {
		moved := false
		for range max(1, times) {
			if !step() {
				break
			}
			moved = true
		}
		return moved
	}
	if !h.extend {
		return run()
	}
	if _, ok := h.cursor.SelectionMode(); !ok {
		h.cursor.Select()
	}
	anchor, ok := h.selectionAnchor()
	if !ok {
		return run()
	}
	moved := run()
	h.setSelectionRange(anchor, h.cursor.CursorAtScroll())
	return moved
}

// moveCaret runs a motion that leaves a single-cell selection behind in
// normal mode: put_cursor(extend=false) collapses to the landing cell.
func (h *helixHandlerImpl) moveCaret(fn func() bool) bool {
	return h.runMotion(h.caretStep(fn))
}

// moveCaretOnce is moveCaret for motions that apply the count inside fn.
func (h *helixHandlerImpl) moveCaretOnce(fn func() bool) bool {
	return h.runMotionOnce(h.caretStep(fn))
}

func (h *helixHandlerImpl) caretStep(fn func() bool) func() bool {
	return func() bool {
		h.cursor.Unselect()
		ok := h.moved(fn)
		h.anchorHere()
		return ok
	}
}

// moved runs a cursor primitive and reports whether the caret actually
// changed position. The primitives disagree on what their boolean
// means: Cursor.MoveRightEndWord returns false under
// RightInclusiveSemantics even when it moved, so position is the only
// reliable signal.
func (h *helixHandlerImpl) moved(fn func() bool) bool {
	before := h.cursor.CursorAtScroll()
	fn()
	return h.cursor.CursorAtScroll() != before
}
