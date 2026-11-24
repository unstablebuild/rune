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

package defsearch

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/ide/syntax/symboldb"
)

// results is what one search found so far: the picker's entries and what
// is known about each beyond its location. It is written by the search
// and read by the picker as it draws.
type results struct {
	req symboldb.DefinitionQuery
	now func() time.Time

	mu      sync.Mutex
	started time.Time
	elapsed time.Duration
	done    bool
	failed  string
	note    string
	count   int
	// files counts the definitions of each file.
	files   map[string]int
	entries map[string]entry
}

// entry is what results knows of the entry of a definition.
type entry struct {
	def symboldb.Definition
	// nth is which of its file's definitions it is, from 1.
	nth int
}

// newResults starts timing the search of req by now.
func newResults(req symboldb.DefinitionQuery, now func() time.Time) *results {
	return &results{req: req, now: now, started: now(), files: map[string]int{}, entries: map[string]entry{}}
}

// add records d and returns its entry: where it is, then its name. It
// reports false for an entry it already has, as of a name a grammar
// captures as two kinds.
func (r *results) add(d symboldb.Definition) (string, bool) {
	e := fmt.Sprintf("%s:%d:%d: %s", d.Path, d.Line, d.Col, d.Name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.entries[e]; ok {
		return "", false
	}
	r.count++
	r.files[d.Path]++
	r.entries[e] = entry{def: d, nth: r.files[d.Path]}
	return e, true
}

// finish records that the search is over. note, when set, says why it
// may have missed definitions.
func (r *results) finish(note string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done, r.note, r.elapsed = true, note, r.now().Sub(r.started)
}

// fail records why the search did not finish.
func (r *results) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done, r.failed, r.elapsed = true, "Search failed: "+err.Error(), r.now().Sub(r.started)
}

// annotate implements locationsearch.Config.Annotate. The status bar
// describes the search, and what the focused entry is.
func (r *results) annotate(e string) locationsearch.Annotation {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := locationsearch.Annotation{Status: r.statusLocked(), Error: r.failed != ""}
	if info, ok := r.entries[e]; ok {
		d := info.def
		a.First, a.Last, a.Terms = d.Line, d.Line, []string{d.Name}
		a.Detail = fmt.Sprintf("%s · %s · %d of %d in file", d.Kind, d.Lang, info.nth, r.files[d.Path])
	}
	return a
}

// statusLocked describes the search: what it found so far and in how
// long, how it was asked for, and why it stopped or may have missed
// definitions. Callers hold mu.
func (r *results) statusLocked() string {
	if r.failed != "" {
		return r.failed
	}
	var parts []string
	switch {
	case !r.done && r.count == 0:
		parts = append(parts, "Searching…")
	case !r.done:
		parts = append(parts, "Searching… "+r.countLocked())
	case r.count == 0:
		parts = append(parts, "No definitions", elapsed(r.elapsed))
	default:
		parts = append(parts, r.countLocked(), elapsed(r.elapsed))
	}
	if len(r.req.Langs) > 0 {
		parts = append(parts, strings.Join(r.req.Langs, ", "))
	}
	if len(r.req.Roots) > 0 {
		parts = append(parts, "in "+strings.Join(r.req.Roots, ", "))
	}
	switch {
	case !r.done || r.req.Limit <= 0 || r.count < r.req.Limit:
	case r.req.Limit == 1:
		parts = append(parts, "stopped at the first definition")
	default:
		parts = append(parts, fmt.Sprintf("stopped at the first %d definitions", r.req.Limit))
	}
	if r.note != "" {
		parts = append(parts, r.note)
	}
	return strings.Join(parts, " · ")
}

// countLocked counts the definitions and their files. Callers hold mu.
func (r *results) countLocked() string {
	return fmt.Sprintf("%d %s in %d %s", r.count, plural(r.count, "definition", "definitions"),
		len(r.files), plural(len(r.files), "file", "files"))
}

// elapsed is how long a search took, to the millisecond below a second.
func elapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
