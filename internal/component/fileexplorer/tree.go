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
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/vctrl"
)

type node struct {
	id       rune // Unique identifier from Private Use Area
	name     string
	uri      workspaceapi.URI
	isDir    bool
	expanded bool
	depth    int
	parent   *node
	children []*node
}

// deepCopy creates a deep copy of the node and all its children.
func (n *node) deepCopy() *node {
	if n == nil {
		return nil
	}
	copy := &node{
		id:       n.id,
		name:     n.name,
		uri:      n.uri,
		isDir:    n.isDir,
		expanded: n.expanded,
		depth:    n.depth,
		// parent is set below when copying children
	}
	if len(n.children) > 0 {
		copy.children = make([]*node, len(n.children))
		for i, child := range n.children {
			copy.children[i] = child.deepCopy()
			copy.children[i].parent = copy
		}
	}
	return copy
}

type parsedEntry struct {
	id    rune
	name  string
	isDir bool
	depth int
}

func readChildren(
	fs workspaceapi.FileSystem, ignore vctrl.Matcher, n *node,
) error {
	entries, err := fs.ReadDir(n.uri.Path())
	if err != nil {
		return err
	}
	visible := make([]*node, 0, len(entries))
	hidden := make([]*node, 0, len(entries))
	for _, e := range entries {
		childURI := workspaceapi.Join(n.uri, e.Name())

		child := &node{
			name:   e.Name(),
			uri:    childURI,
			isDir:  dirEntryIsDir(fs, e, childURI),
			depth:  n.depth + 1,
			parent: n,
		}
		// Match the unresolved DirEntry so a dir-only gitignore
		// pattern does not hide a symlink (git treats it as a blob).
		if ignore != nil && ignore.Match(childURI, e.IsDir()) {
			hidden = append(hidden, child)
			continue
		}
		visible = append(visible, child)
	}
	// Keep those children so the folder stays browsable; mixed
	// directories still hide ignored noise.
	if len(visible) == 0 {
		visible = hidden
	}
	sortChildren(visible)
	n.children = visible
	return nil
}

func dirEntryIsDir(
	fs workspaceapi.FileSystem, e os.DirEntry, uri workspaceapi.URI,
) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	info, err := fs.Stat(uri.Path())
	if err != nil {
		return false
	}
	return info.IsDir()
}

func sortChildren(children []*node) {
	slices.SortFunc(children, func(a, b *node) int {
		if a.isDir != b.isDir {
			if a.isDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.name, b.name)
	})
}

func flatten(root *node) []*node {
	var flat []*node
	var walk func(n *node)
	walk = func(n *node) {
		for _, child := range n.children {
			flat = append(flat, child)
			if child.isDir && child.expanded {
				walk(child)
			}
		}
	}
	walk(root)
	return flat
}

func renderLine(n *node, cfg Config) string {
	var b strings.Builder
	indent := cfg.IndentRune
	width := cfg.IndentWidth
	for range n.depth {
		b.WriteRune(indent)
		for range width - 1 {
			b.WriteRune(' ')
		}
	}
	b.WriteRune(iconFor(n, cfg))
	b.WriteRune(' ')
	b.WriteString(n.name)
	if n.isDir {
		b.WriteRune('/')
	}
	return b.String()
}

func iconFor(n *node, cfg Config) rune {
	if n.isDir {
		if n.expanded && cfg.Icons.OpenDirectory != 0 {
			return cfg.Icons.OpenDirectory
		}
		return cfg.Icons.Directory
	}
	ext := filepath.Ext(n.name)
	if icon, ok := cfg.Icons.Extensions[ext]; ok {
		return icon
	}
	return cfg.Icons.Default
}

// parseCellRow parses a single buffer row into a parsedEntry. If the
// row is blank (or only whitespace after the icon is stripped), it
// returns (_, false) so the caller skips it. Trailing whitespace is
// tolerated. The caller is responsible for populating entry.id.
func parseCellRow(
	row []term.Cell, indent rune, width int,
) (parsedEntry, bool) {
	if len(row) == 0 {
		return parsedEntry{}, false
	}
	line := cellsToRowString(row)
	line = strings.TrimRight(line, " \t")
	if line == "" {
		return parsedEntry{}, false
	}
	return parseLine(line, indent, width), true
}

func cellsToRowString(row []term.Cell) string {
	var b strings.Builder
	for _, c := range row {
		b.WriteRune(c.Ch)
		for _, comb := range c.CombiningRunes() {
			b.WriteRune(comb)
		}
	}
	return b.String()
}

// parseLine reads a rendered row and extracts (depth, name, isDir).
// The renderer emits each depth level as a run of `width` cells
// starting with `indent`. We recognise those runs here, plus a
// permissive set of user-added whitespace variants (see below).
func parseLine(line string, indent rune, width int) parsedEntry {
	var depth int
	runes := []rune(line)
	i := 0
	// Tolerate plain spaces the user may have prepended to the row.
	// The renderer never emits leading plain spaces before the first
	// indent marker or tab — so any we find here are user padding
	// (e.g. the user typed "  " to visually indent instead of
	// pressing `>>`). Eating them first keeps rowID-preserved
	// identity intact: the real indent/icon runes that follow still
	// line up with the canonical rendering.
	for i < len(runes) && runes[i] == ' ' {
		i++
	}
	// Each leading '\t' counts as one extra depth level. Vi's `>>`
	// (shift-right) inserts a literal tab at column 0 of the row.
	// The tab is not part of the rendered form, but we must still
	// treat it as an indent signal so the user can re-parent rows
	// via `>>` — both rows that keep their rowID across the edit
	// (e.g. an existing entry the user indents in place) and rows
	// whose id is unknown (e.g. a yank-paste followed by `>>`).
	// When the row DOES have a preserved id and the tab pushes it
	// past parent.depth+1, parseViewTree's stack-based builder
	// clamps depth back down, so a redundant `>>` on an already
	// correctly-indented row is still a no-op.
	for i < len(runes) && runes[i] == '\t' {
		depth++
		i++
	}
	if width < 2 {
		width = 2
	}
	for i < len(runes) && runes[i] == indent {
		// Require the indent rune followed by (width-1) spaces.
		// Bail out if we don't have enough cells or the run is
		// broken up (e.g. the user rewrote the prefix).
		if i+width > len(runes) {
			break
		}
		ok := true
		for k := 1; k < width; k++ {
			if runes[i+k] != ' ' {
				ok = false
				break
			}
		}
		if !ok {
			break
		}
		depth++
		i += width
	}
	// Tolerate extra plain spaces the user may have inserted between
	// the indent/tab prefix and the icon (or between the icon and
	// the filename separator). Filenames never begin with whitespace
	// in practice, so eating the entire run here is safe.
	for i < len(runes) && runes[i] == ' ' {
		i++
	}
	// Skip "icon + space" when present. The rendered form places
	// exactly one icon rune followed by one space between the last
	// indent marker and the filename, so we detect it by requiring
	// runes[i+1] == ' '. When the user types a fresh row without an
	// icon (e.g. "newfile.go"), runes[i+1] is typically part of the
	// filename so we leave the cursor at i and consume the whole
	// suffix as the name.
	if i+1 < len(runes) && runes[i+1] == ' ' {
		i += 2
	}
	name := string(runes[i:])
	name = strings.TrimSpace(name)
	isDir := strings.HasSuffix(name, "/")
	if isDir {
		name = strings.TrimSuffix(name, "/")
	}
	return parsedEntry{
		name:  name,
		isDir: isDir,
		depth: depth,
	}
}
