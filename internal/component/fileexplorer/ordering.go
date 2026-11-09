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

package fileexplorer

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// step is one file system call made while applying a change set. Its
// paths are where entries are when the step runs, which differs from
// the Operation's URIs once a directory above them has moved.
type step struct {
	op       OperationType
	from, to string
	// scratch marks a rename to a temporary name, made to break a
	// cycle such as two entries swapping names. It must not replace
	// anything.
	scratch bool
}

// dep is a change another change waits for. A soft dep only needs the
// change's entry out of the way, which a rename to a scratch name also
// achieves; any other dep needs the change complete.
type dep struct {
	on   *change
	soft bool
}

type relocation struct{ from, to string }

var errUnorderable = errors.New("file explorer: cannot find an order to apply the changes in")

// schedule orders changes so each runs once the file system is ready
// for it:
//   - an entry is created in, moved into or copied into a directory only
//     once the directory is at its final place;
//   - an entry takes a name only once the entry holding it moved away or
//     was deleted;
//   - a directory is deleted only once every entry moving out of it left.
//
// Cycles, such as two entries swapping names, are broken by first moving
// one of the entries to a scratch name in the root. Among the changes
// that are ready, deletes go first, then renames and moves, then creates
// and copies, each in detection order. It returns the operations in the
// order they complete and the steps that apply them.
func schedule(base, view *node, changes []*change) ([]Operation, []step, error) {
	byView := make(map[*node]*change, len(changes))
	deleteOf := make(map[*node]*change)
	occupant := make(map[slot]*change)
	for _, c := range changes {
		if c.view != nil {
			byView[c.view] = c
		}
		switch c.Type {
		case OpDelete:
			deleteOf[c.base] = c
			occupant[slotOf(c.base)] = c
		case OpRename, OpMove:
			occupant[slotOf(c.base)] = c
		}
	}

	deps := make(map[*change][]dep, len(changes))
	for _, c := range changes {
		if c.view != nil {
			for a := c.view.parent; a != nil && a.depth >= 0; a = a.parent {
				if p := byView[a]; p != nil {
					deps[c] = append(deps[c], dep{on: p})
					break
				}
			}
			if o := occupant[slotOf(c.view)]; o != nil && o != c {
				deps[c] = append(deps[c], dep{on: o, soft: true})
			}
		}
		if c.Type == OpRename || c.Type == OpMove {
			for a := c.base.parent; a != nil && a.depth >= 0; a = a.parent {
				if d := deleteOf[a]; d != nil {
					deps[d] = append(deps[d], dep{on: c, soft: true})
					break
				}
			}
		}
	}

	order := slices.Clone(changes)
	slices.SortStableFunc(order, func(a, b *change) int {
		return scheduleRank(a.Type) - scheduleRank(b.Type)
	})

	done := make(map[*change]bool, len(changes))
	evacuated := make(map[*change]bool)
	satisfied := func(d dep) bool {
		return done[d.on] || (d.soft && evacuated[d.on])
	}
	var relocations []relocation
	// at returns where the entry that started at p is now.
	at := func(p string) string {
		for _, r := range relocations {
			if p == r.from || strings.HasPrefix(p, r.from+"/") {
				p = r.to + p[len(r.from):]
			}
		}
		return p
	}
	scratch := scratchNames(base, view)

	ordered := make([]Operation, 0, len(changes))
	steps := make([]step, 0, len(changes))
	// start skips the finished prefix of order, which keeps the common
	// case of independent changes linear.
	start := 0
	for len(ordered) < len(changes) {
		for done[order[start]] {
			start++
		}
		next := nextReady(order[start:], done, deps, satisfied)
		if next == nil {
			ev := nextEvacuee(order[start:], done, evacuated, deps, satisfied)
			if ev == nil {
				return nil, nil, errUnorderable
			}
			from := at(ev.base.uri.Path())
			to := scratch()
			steps = append(steps, step{op: ev.Type, from: from, to: to, scratch: true})
			relocations = append(relocations, relocation{from: from, to: to})
			evacuated[ev] = true
			continue
		}
		s := step{op: next.Type}
		if next.base != nil {
			s.from = at(next.base.uri.Path())
		}
		if next.view != nil {
			s.to = next.view.uri.Path()
		}
		if next.Type == OpRename || next.Type == OpMove {
			relocations = append(relocations, relocation{from: s.from, to: s.to})
		}
		steps = append(steps, s)
		done[next] = true
		ordered = append(ordered, next.Operation)
	}
	return ordered, steps, nil
}

func scheduleRank(t OperationType) int {
	switch t {
	case OpDelete:
		return 0
	case OpRename, OpMove:
		return 1
	default:
		return 2
	}
}

func nextReady(
	order []*change, done map[*change]bool,
	deps map[*change][]dep, satisfied func(dep) bool,
) *change {
	for _, c := range order {
		if !done[c] && !slices.ContainsFunc(deps[c], func(d dep) bool {
			return !satisfied(d)
		}) {
			return c
		}
	}
	return nil
}

// nextEvacuee returns the first pending rename or move whose entry is
// in the way of another pending change. Every cycle contains one: the
// only deps that can point back down a cycle are soft, and soft deps on
// a delete lead on to a move out of the deleted directory.
func nextEvacuee(
	order []*change, done, evacuated map[*change]bool,
	deps map[*change][]dep, satisfied func(dep) bool,
) *change {
	blocking := make(map[*change]bool)
	for _, c := range order {
		if done[c] {
			continue
		}
		for _, d := range deps[c] {
			if d.soft && !satisfied(d) {
				blocking[d.on] = true
			}
		}
	}
	for _, c := range order {
		if blocking[c] && !evacuated[c] && (c.Type == OpRename || c.Type == OpMove) {
			return c
		}
	}
	return nil
}

// scratchNames returns a generator of root paths whose names neither
// tree uses.
func scratchNames(base, view *node) func() string {
	taken := make(map[string]bool)
	for _, root := range []*node{base, view} {
		for _, child := range root.children {
			taken[child.name] = true
		}
	}
	n := 0
	return func() string {
		for {
			n++
			name := fmt.Sprintf(".fexplorer-scratch-%d", n)
			if !taken[name] {
				taken[name] = true
				return workspaceapi.Join(base.uri, name).Path()
			}
		}
	}
}
