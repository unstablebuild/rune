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

package semanticsearch

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"unstable.build/rune/internal/handler/locationsearch"
	"unstable.build/rune/internal/llm/llmsearch"
)

// weakRatio is the share of the top score below which hits are weaker
// matches, as the `search` console command lists them.
const weakRatio = 0.5

// fallbackStatus leads the status of the results of a search by words
// that stood in for a failed meaning search.
const fallbackStatus = "fallback to word search. see logs for more info"

// results is what one search found: the picker's entries, as grep
// prints them, and what is known about each beyond its location. It is
// written by the search and read by the picker as it draws.
type results struct {
	query string

	mu sync.Mutex
	// status describes the search when no entry is focused, and failed
	// says that it failed or fell back to words.
	status  string
	failed  bool
	entries map[string]locationsearch.Annotation
}

func newResults(query string) *results {
	return &results{query: query, status: fmt.Sprintf("Searching for “%s”…", query)}
}

// annotate implements locationsearch.Config.Annotate.
func (r *results) annotate(entry string) locationsearch.Annotation {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a, ok := r.entries[entry]; ok {
		return a
	}
	return locationsearch.Annotation{Status: r.status, Error: r.failed}
}

// fail records why the search found nothing.
func (r *results) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status, r.failed = "Search failed: "+err.Error(), true
}

// found records hits, best first, and returns their entries in that
// order. note, when set, says why the hits may be incomplete, and
// fallback that they were found by words because meaning search failed.
func (r *results) found(hits []searchapi.SemanticResult, note string, fallback bool) []string {
	strong := llmsearch.StrongCount(hits, weakRatio)
	places := make([]string, len(hits))
	width := 0
	for i, h := range hits {
		places[i] = fmt.Sprintf("%s:%d:", h.Path, line(h))
		width = max(width, len(places[i]))
	}
	entries := make([]string, 0, len(hits))
	annotations := make(map[string]locationsearch.Annotation, len(hits))
	for i, h := range hits {
		entry := strings.TrimRight(fmt.Sprintf("%-*s %s", width, places[i], shown(h)), " ")
		if _, dup := annotations[entry]; dup {
			continue
		}
		entries = append(entries, entry)
		annotations[entry] = annotation(h, i >= strong, note, fallback)
	}

	status := fmt.Sprintf("Nothing matches “%s”", r.query)
	if len(hits) > 0 {
		status = fmt.Sprintf("%d %s for “%s”, %d strong", len(hits), plural(len(hits), "match", "matches"), r.query, strong)
	}
	if fallback {
		status = fallbackStatus + " · " + status
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status, r.failed = joinNote(status, note), fallback
	r.entries = annotations
	return entries
}

// line is where the picker opens hit: the line it points at for a
// stretch of lines, else the top of the file.
func line(h searchapi.SemanticResult) int {
	if h.Segment == "lines" {
		return cmp.Or(h.Focus, h.Loc, 1)
	}
	return 1
}

// shown is the text the entry of hit shows after its location: the line
// it opens at, as grep shows a match, else what the excerpt says.
func shown(h searchapi.SemanticResult) string {
	if text := strings.TrimLeft(h.Text, " \t"); text != "" {
		return text
	}
	excerpt := strings.NewReplacer("«", "", "»", "").Replace(h.Excerpt)
	switch h.Segment {
	case "cells":
		return strings.TrimRight(fmt.Sprintf("[cell %d] %s", h.Loc, excerpt), " ")
	case "name":
		return cmp.Or(excerpt, "(path matches)")
	}
	return excerpt
}

// annotation describes hit to the picker. Its rank is left to the list,
// which counts the entries.
func annotation(h searchapi.SemanticResult, weak bool, note string, fallback bool) locationsearch.Annotation {
	var parts []string
	if fallback {
		parts = append(parts, fallbackStatus)
	}
	if weak {
		parts = append(parts, "weaker")
	}
	parts = append(parts, "score "+strconv.FormatFloat(h.Score, 'f', 2, 64))
	if h.Cosine != 0 {
		parts = append(parts, fmt.Sprintf("meaning z %.2f cos %.2f", h.Z, h.Cosine))
	}
	if h.KeywordRank > 0 {
		parts = append(parts, fmt.Sprintf("words #%d", h.KeywordRank))
	}
	a := locationsearch.Annotation{Error: fallback, Terms: marked(h.Excerpt)}
	switch h.Segment {
	case "lines":
		a.First, a.Last = h.Loc, max(h.Loc, h.LocEnd)
		if a.Last > a.First {
			a.Detail = fmt.Sprintf("lines %d–%d", a.First, a.Last)
		} else {
			a.Detail = fmt.Sprintf("line %d", a.First)
		}
	case "cells":
		a.Detail = fmt.Sprintf("cell %d", h.Loc)
	case "name":
		a.Detail = "path"
	}
	a.Status = joinNote(strings.Join(parts, " · "), note)
	return a
}

// marked returns the «marked» words of an excerpt.
func marked(excerpt string) []string {
	var words []string
	for {
		_, rest, ok := strings.Cut(excerpt, "«")
		if !ok {
			return words
		}
		word, after, ok := strings.Cut(rest, "»")
		if !ok {
			return words
		}
		if word != "" {
			words = append(words, word)
		}
		excerpt = after
	}
}

func joinNote(status, note string) string {
	if note == "" {
		return status
	}
	return status + " · " + note
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
