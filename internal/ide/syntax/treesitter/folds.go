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
	"context"

	log "github.com/sirupsen/logrus"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

const captureNameFoldsInitial = "initial_fold"

func (t *Tree) getFolds(initial bool) []term.Range {
	ret := make([]term.Range, 0)
	root := t.tree.RootNode()

	cur := tree_sitter.NewQueryCursor()
	defer cur.Close()

	captureNames := t.folds.CaptureNames()
	matches := cur.Matches(t.folds, root, t.content)
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		for _, cap := range m.Captures {
			n := cap.Node
			if initial && captureNames[cap.Index] != captureNameFoldsInitial {
				continue
			}
			rng, ok := t.treeSitterRangeToTerm(n.Range())
			if !ok {
				t.log(log.DebugLevel, "could not convert tree sitter range %+v to term",
					n.Range())
				continue
			}
			ret = append(ret, rng)
		}
	}
	return ret
}

func (t *Tree) getFoldsFrom(from term.Coordinates) []term.Range {
	ret := make([]term.Range, 0)

	from.X = 0 // ignore x offsets to make things easier
	byteOffset, sok := t.cellBytes.CoordinatesToByteOffset(from)
	if !sok {
		return nil
	}
	cur := tree_sitter.NewQueryCursor()
	defer cur.Close()
	matches := cur.Matches(t.folds, t.tree.RootNode(), t.content)
	cur.SetByteRange(uint(byteOffset), uint(len(t.content)))
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		for _, cap := range m.Captures {
			n := cap.Node
			rng, ok := t.treeSitterRangeToTerm(n.Range())
			if !ok {
				t.log(log.DebugLevel, "could not convert tree sitter range %+v to term",
					n.Range())
				continue
			}
			ret = append(ret, rng)
		}
	}
	return ret
}

func (t *Tree) treeSitterRangeToTerm(rng tree_sitter.Range) (term.Range, bool) {
	start, sok := t.cellBytes.RunePosToCoordinates(
		int(rng.StartPoint.Row), int(rng.StartPoint.Column))
	end, eok := t.cellBytes.RunePosToCoordinates(
		int(rng.EndPoint.Row), int(rng.EndPoint.Column))
	return term.Range{
		Start: start,
		End:   end,
	}, sok && eok
}

type foldsIterator struct {
	initial bool
	ready   chan struct{}
	tree    *Tree
	err     error
	from    term.Coordinates
	slice   iterator.Iterator[term.Range]
}

func newFoldsIterator(initial bool, t *Tree, ch chan struct{}) *foldsIterator {
	return &foldsIterator{
		tree:    t,
		ready:   ch,
		initial: initial,
	}
}

func newFoldsFromIterator(from term.Coordinates, t *Tree, ch chan struct{}) *foldsIterator {
	return &foldsIterator{
		tree:  t,
		ready: ch,
		from:  from,
	}
}

func (f *foldsIterator) Next(ctx context.Context) (term.Range, bool) {
	if f.slice == nil {
		select {
		case <-f.ready:
		case <-ctx.Done():
			f.err = ctx.Err()
			return term.Range{}, false
		}
		// the Tree may have been closed between the ready signal and
		// this first Next; its native tree-sitter objects are freed
		// under mu, so re-check closed before touching them.
		f.tree.mu.Lock()
		if f.tree.closed || f.tree.tree == nil || f.tree.folds == nil {
			f.tree.mu.Unlock()
			return term.Range{}, false
		}
		if f.from == (term.Coordinates{}) {
			f.slice = iterator.FromSlice(f.tree.getFolds(f.initial))
		} else {
			f.slice = iterator.FromSlice(f.tree.getFoldsFrom(f.from))
		}
		f.tree.mu.Unlock()
	}
	return f.slice.Next(ctx)
}

func (f *foldsIterator) Err() error {
	if f.err != nil {
		return f.err
	}
	if f.slice == nil {
		return nil
	}
	return f.slice.Err()
}

func (f *foldsIterator) Close() error {
	return nil
}
