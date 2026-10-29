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

package regexsearch

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"unstable.build/rune/internal/handler/locationsearch"
)

// maxShown is how many bytes of a line its entry shows at most.
const maxShown = 240

// results is what one search found so far: the picker's entries, as grep
// prints them, and what is known about each beyond its location. It is
// written by the search and read by the picker as it draws.
type results struct {
	req searchapi.RegexpRequest
	now func() time.Time

	mu      sync.Mutex
	started time.Time
	elapsed time.Duration
	done    bool
	failed  string
	note    string
	matches int
	// files counts the matching lines of each file.
	files   map[string]int
	entries map[string]entry
}

// entry is what results knows of the entry of a match.
type entry struct {
	path      string
	line, col int
	// nth is which of its file's matching lines it is, from 1.
	nth   int
	terms []string
}

// newResults starts timing the search of req by now.
func newResults(req searchapi.RegexpRequest, now func() time.Time) *results {
	return &results{req: req, now: now, started: now(), files: map[string]int{}, entries: map[string]entry{}}
}

// add records m and returns its entry: where it is, then the line.
func (r *results) add(m searchapi.RegexpResult) string {
	col := 1
	if len(m.Spans) > 0 {
		col += utf8.RuneCountInString(m.Text[:m.Spans[0][0]])
	}
	e := fmt.Sprintf("%s:%d:%d: %s", m.Path, m.Line, col, shown(m.Text))
	var terms []string
	for _, s := range m.Spans {
		if t := m.Text[s[0]:s[1]]; t != "" && !slices.Contains(terms, t) {
			terms = append(terms, t)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.matches++
	r.files[m.Path]++
	r.entries[e] = entry{path: m.Path, line: m.Line, col: col, nth: r.files[m.Path], terms: terms}
	return e
}

// shown is what the entry of a line shows of it.
func shown(text string) string {
	text = strings.TrimLeft(text, " \t")
	if len(text) <= maxShown {
		return text
	}
	cut := maxShown
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// finish records that the search is over. note, when set, says why it
// may have been slow.
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
// describes the search, and where the focused entry is in its file.
func (r *results) annotate(e string) locationsearch.Annotation {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := locationsearch.Annotation{Status: r.statusLocked(), Error: r.failed != ""}
	if info, ok := r.entries[e]; ok {
		a.First, a.Last, a.Terms = info.line, info.line, info.terms
		a.Detail = fmt.Sprintf("%d of %d in file · %d:%d", info.nth, r.files[info.path], info.line, info.col)
	}
	return a
}

// statusLocked describes the search: what it found so far and in how
// long, how it was asked for, and why it stopped or may have been slow.
// Callers hold mu.
func (r *results) statusLocked() string {
	if r.failed != "" {
		return r.failed
	}
	var parts []string
	switch {
	case !r.done && r.matches == 0:
		parts = append(parts, fmt.Sprintf("Searching for “%s”…", r.req.Pattern))
	case !r.done:
		parts = append(parts, fmt.Sprintf("Searching for “%s”… %s", r.req.Pattern, r.countLocked()))
	case r.matches == 0:
		parts = append(parts, fmt.Sprintf("Nothing matches “%s”", r.req.Pattern), elapsed(r.elapsed))
	default:
		parts = append(parts, r.countLocked(), elapsed(r.elapsed))
	}
	if r.req.IgnoreCase {
		parts = append(parts, "ignore case")
	}
	if len(r.req.Roots) > 0 {
		parts = append(parts, "in "+strings.Join(r.req.Roots, ", "))
	}
	switch {
	case !r.done || r.req.Limit <= 0 || r.matches < r.req.Limit:
	case r.req.Limit == 1:
		parts = append(parts, "stopped at the first line")
	default:
		parts = append(parts, fmt.Sprintf("stopped at the first %d lines", r.req.Limit))
	}
	if r.note != "" {
		parts = append(parts, r.note)
	}
	return strings.Join(parts, " · ")
}

// countLocked counts the matching lines and their files. Callers hold mu.
func (r *results) countLocked() string {
	return fmt.Sprintf("%d %s in %d %s", r.matches, plural(r.matches, "line", "lines"),
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
