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

package treesitter

import (
	"slices"

	log "github.com/sirupsen/logrus"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// CommentCoverage returns exact comment-node ranges when rng is fully covered by
// comment captures from the active highlights query.
func (t *Tree) CommentCoverage(rng term.Range) ([]term.Range, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.ready || t.closed || t.tree == nil || t.highlights == nil {
		return nil, false
	}

	current, ok := t.normalizeSelectionRange(rng)
	if !ok {
		return nil, false
	}

	commentIdx, ok := t.highlights.CaptureIndexForName("comment")
	if !ok {
		return nil, false
	}

	root := t.tree.RootNode()
	cur := tree_sitter.NewQueryCursor()
	defer cur.Close()

	matches := cur.Matches(t.highlights, root, []byte(t.buf.String()))
	var ranges []term.Range
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		for _, cap := range m.Captures {
			if uint(cap.Index) != commentIdx {
				continue
			}
			crng, ok := t.treeSitterRangeToTerm(cap.Node.Range())
			if !ok {
				t.log(log.DebugLevel, "could not convert tree sitter range %+v to term",
					cap.Node.Range())
				continue
			}
			if !rangesIntersect(crng, current) {
				continue
			}
			ranges = append(ranges, crng)
		}
	}
	if len(ranges) == 0 {
		return nil, false
	}
	slices.SortFunc(ranges, func(a, b term.Range) int {
		if a.Start.Y != b.Start.Y {
			return a.Start.Y - b.Start.Y
		}
		return a.Start.X - b.Start.X
	})
	if !t.rangesCover(current, ranges) {
		return nil, false
	}
	return ranges, true
}

func rangesIntersect(a, b term.Range) bool {
	return coordinatesLessOrEqual(a.Start, b.End) && coordinatesLessOrEqual(b.Start, a.End)
}

func (t *Tree) rangesCover(target term.Range, ranges []term.Range) bool {
	cursor := target.Start
	for _, rng := range ranges {
		if coordinatesLessOrEqual(rng.End, cursor) {
			continue
		}
		if !coordinatesLessOrEqual(rng.Start, cursor) {
			if !(rng.Start.Y == cursor.Y+1 && rng.Start.X == 0 && cursor.X == t.buf.Columns(cursor.Y)) {
				return false
			}
		}
		if coordinatesLessOrEqual(target.End, rng.End) {
			return true
		}
		cursor = rng.End
	}
	return false
}
