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

package symboldb

import (
	"slices"
	"strings"
	"sync"

	"unstable.build/rune/internal/workspace/searchfilter"
)

// fileEntry is what the catalog knows of a file: what decides whether it
// must be read again, and its definitions. Entries are never modified
// once in the catalog, only replaced, so readers may use one after
// letting go of the catalog's lock.
type fileEntry struct {
	uri     string
	lang    string
	grammar string
	mtime   int64
	version int
	skipped string
	defs    []byte
	count   int
}

func entryOf(doc fileDoc) *fileEntry {
	return &fileEntry{
		uri: doc.URI, lang: doc.Lang, grammar: doc.Grammar, mtime: doc.ModTime,
		version: doc.Version, skipped: doc.Skipped, defs: doc.Defs, count: defsCount(doc.Defs),
	}
}

// catalog holds the file records of the index in memory, by
// workspace-relative slash path, without the names they contributed.
type catalog struct {
	mu    sync.RWMutex
	files map[string]*fileEntry
	// paths are the keys of files in searchfilter.ComparePaths order; nil
	// when they must be sorted again.
	paths   []string
	records int
}

func newCatalog() *catalog {
	return &catalog{files: map[string]*fileEntry{}}
}

func (c *catalog) get(path string) (*fileEntry, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.files[path]
	return e, ok
}

func (c *catalog) put(path string, e *fileEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.files[path]; ok {
		c.records -= old.count
	} else {
		c.paths = nil
	}
	c.files[path] = e
	c.records += e.count
}

// remove drops path, reporting whether the catalog had it.
func (c *catalog) remove(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	old, ok := c.files[path]
	if !ok {
		return false
	}
	c.records -= old.count
	delete(c.files, path)
	c.paths = nil
	return true
}

func (c *catalog) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.files)
	c.paths, c.records = nil, 0
}

// under returns the paths of the files that are dir or in it.
func (c *catalog) under(dir string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []string
	for p := range c.files {
		if p == dir || strings.HasPrefix(p, dir+"/") {
			out = append(out, p)
		}
	}
	return out
}

// entry is a file of a snapshot.
type entry struct {
	path string
	e    *fileEntry
}

// snapshot returns the files under roots, in searchfilter.ComparePaths
// order.
func (c *catalog) snapshot(roots []string) []entry {
	c.mu.Lock()
	if c.paths == nil {
		c.paths = make([]string, 0, len(c.files))
		for p := range c.files {
			c.paths = append(c.paths, p)
		}
		slices.SortFunc(c.paths, searchfilter.ComparePaths)
	}
	out := make([]entry, 0, len(c.paths))
	for _, p := range c.paths {
		if searchfilter.Under(p, roots) {
			out = append(out, entry{path: p, e: c.files[p]})
		}
	}
	c.mu.Unlock()
	return out
}

// counts returns how many files and records the catalog holds, and how
// many files it has without records for each reason.
func (c *catalog) counts() (files, records int, skipped map[string]int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	skipped = map[string]int{}
	for _, e := range c.files {
		if e.skipped != "" {
			skipped[skipReason(e.skipped)]++
		}
	}
	return len(c.files), c.records, skipped
}

// skipReason is the reason of skipped without its detail.
func skipReason(skipped string) string {
	reason, _, _ := strings.Cut(skipped, ":")
	return reason
}
