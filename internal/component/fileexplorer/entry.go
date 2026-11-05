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
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// Entry represents a file or directory in the explorer.
// This is the primary data structure for oil.nvim-like editing.
// Entries are identified by their ID (from Unicode Private Use Area),
// which persists across edits allowing move/rename detection.
type Entry struct {
	ID       rune             // Unique identifier (0 for new entries)
	Name     string           // Filename without path
	URI      workspaceapi.URI // Full URI
	IsDir    bool             // True if directory
	Expanded bool             // True if directory is expanded
	Depth    int              // Nesting level (0 = root children)
}

// collectEntries extracts all entries from a node tree into a flat list.
//
// All directories with loaded children are traversed, regardless of
// their "expanded" flag (which is a UI concern). This ensures that
// files inside collapsed directories — which are still part of the
// model — are included when diffing view vs base for change
// detection.
func collectEntries(root *node) []*Entry {
	var entries []*Entry
	var walk func(n *node)
	walk = func(n *node) {
		for _, child := range n.children {
			entries = append(entries, &Entry{
				ID:       child.id,
				Name:     child.name,
				URI:      child.uri,
				IsDir:    child.isDir,
				Expanded: child.expanded,
				Depth:    child.depth,
			})
			if child.isDir {
				walk(child)
			}
		}
	}
	walk(root)
	return entries
}
