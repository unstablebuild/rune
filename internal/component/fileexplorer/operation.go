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
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// OperationType describes the kind of filesystem operation.
type OperationType int

const (
	// OpCreate indicates a new file should be created.
	OpCreate OperationType = iota
	// OpMkdir indicates a new directory should be created.
	OpMkdir
	// OpDelete indicates a file or directory should be deleted.
	OpDelete
	// OpRename indicates a file or directory should be renamed (same parent).
	OpRename
	// OpMove indicates a file or directory should be moved (different parent).
	OpMove
	// OpCopy indicates a file or directory should be copied.
	OpCopy
)

// String returns the string representation of the operation type.
func (t OperationType) String() string {
	switch t {
	case OpCreate:
		return "create"
	case OpMkdir:
		return "mkdir"
	case OpDelete:
		return "delete"
	case OpRename:
		return "rename"
	case OpMove:
		return "move"
	case OpCopy:
		return "copy"
	default:
		return "unknown"
	}
}

// Operation represents a single filesystem change detected by
// comparing the view tree against the base tree. An operation on a
// directory carries everything inside it, including entries the
// explorer never loaded, so entries that only follow their directory
// get no operation of their own. URI is where the entry is before any
// operation is applied and NewURI is where it ends up after all of them.
type Operation struct {
	Type   OperationType
	URI    workspaceapi.URI // Source URI (for delete, rename, move, copy)
	NewURI workspaceapi.URI // Destination URI (for create, mkdir, rename, move, copy)
}

// String returns a human-readable description of the operation.
func (o Operation) String() string {
	switch o.Type {
	case OpCreate:
		return fmt.Sprintf("create %s", o.NewURI.Name())
	case OpMkdir:
		return fmt.Sprintf("mkdir %s", o.NewURI.Name())
	case OpDelete:
		return fmt.Sprintf("delete %s", o.URI.Name())
	case OpRename:
		return fmt.Sprintf("rename %s -> %s", o.URI.Name(), o.NewURI.Name())
	case OpMove:
		return fmt.Sprintf("move %s -> %s", o.URI.Path(), o.NewURI.Path())
	case OpCopy:
		return fmt.Sprintf("copy %s -> %s", o.URI.Path(), o.NewURI.Path())
	default:
		return "unknown operation"
	}
}

// ChangeSet holds the result of change detection with additional metadata.
type ChangeSet struct {
	Operations []Operation
	Conflicts  []Conflict
}

// Conflict describes a conflict between two operations.
type Conflict struct {
	Path    string // The conflicting path
	Entry1  rune   // ID of first entry
	Entry2  rune   // ID of second entry (0 if conflict with existing file)
	Message string
}

// HasConflicts returns true if there are any conflicts.
func (cs *ChangeSet) HasConflicts() bool {
	return len(cs.Conflicts) > 0
}

// change is an Operation together with the nodes it was derived from,
// which schedule needs to tell which changes must wait for which.
type change struct {
	Operation
	base *node // the entry in the base tree; nil for creates
	view *node // the entry in the view tree; nil for deletes
}

// slot is where an entry sits: its name within its parent directory.
// The parent is identified by row ID rather than path, so an entry whose
// directory moved is still in the same slot and needs no operation.
type slot struct {
	parent rune
	name   string
}

// rootParent is the slot parent of the root's children. Row IDs are
// never negative, and an unsaved directory's children get parent 0,
// which matches no entry on disk.
const rootParent rune = -1

func slotOf(n *node) slot {
	if n.parent == nil || n.parent.depth < 0 {
		return slot{parent: rootParent, name: n.name}
	}
	return slot{parent: n.parent.id, name: n.name}
}

// computeChangeSet compares the view tree against the base tree and
// returns the conflicts it found together with the changes, in
// detection order.
func computeChangeSet(baseTree, viewTree *node) (*ChangeSet, []*change) {
	cs := &ChangeSet{}
	baseNodes := descendants(baseTree)
	viewNodes := descendants(viewTree)

	baseByID := make(map[rune]*node, len(baseNodes))
	for _, n := range baseNodes {
		baseByID[n.id] = n
	}
	// An ID on more than one row is copied to all but its last row.
	idCounts := make(map[rune]int, len(viewNodes))
	for _, n := range viewNodes {
		if n.id != 0 {
			idCounts[n.id]++
		}
	}
	inView := make(map[rune]bool, len(idCounts))
	for id := range idCounts {
		inView[id] = true
	}

	var changes []*change
	targetPaths := make(map[string]rune, len(viewNodes))
	for _, v := range viewNodes {
		if existingID, exists := targetPaths[v.uri.String()]; exists {
			cs.Conflicts = append(cs.Conflicts, Conflict{
				Path:    v.uri.Path(),
				Entry1:  existingID,
				Entry2:  v.id,
				Message: fmt.Sprintf("path conflict: %s", v.uri.Path()),
			})
			continue
		}
		targetPaths[v.uri.String()] = v.id

		b := baseByID[v.id]
		switch {
		case v.id == 0 || b == nil:
			typ := OpCreate
			if v.isDir {
				typ = OpMkdir
			}
			changes = append(changes, &change{
				Operation: Operation{Type: typ, NewURI: v.uri},
				view:      v,
			})
		case idCounts[v.id] > 1:
			idCounts[v.id]--
			changes = append(changes, &change{
				Operation: Operation{Type: OpCopy, URI: b.uri, NewURI: v.uri},
				base:      b,
				view:      v,
			})
		case slotOf(b) != slotOf(v):
			typ := OpMove
			if slotOf(b).parent == slotOf(v).parent {
				typ = OpRename
			}
			changes = append(changes, &change{
				Operation: Operation{Type: typ, URI: b.uri, NewURI: v.uri},
				base:      b,
				view:      v,
			})
		}
	}

	for _, b := range baseNodes {
		if inView[b.id] {
			continue
		}
		// Deleting the parent deletes this entry too.
		if b.parent != baseTree && !inView[b.parent.id] {
			continue
		}
		changes = append(changes, &change{
			Operation: Operation{Type: OpDelete, URI: b.uri},
			base:      b,
		})
	}
	return cs, changes
}

// descendants returns every node below root in pre-order, including the
// loaded children of collapsed directories.
func descendants(root *node) []*node {
	var nodes []*node
	var walk func(n *node)
	walk = func(n *node) {
		for _, child := range n.children {
			nodes = append(nodes, child)
			walk(child)
		}
	}
	walk(root)
	return nodes
}
