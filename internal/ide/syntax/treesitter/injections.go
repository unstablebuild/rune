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
	"cmp"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"

	log "github.com/sirupsen/logrus"
	tree_sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide/idelsp/languages"
)

const (
	captureInjectionContent   = "injection.content"
	captureInjectionLanguage  = "injection.language"
	propertyInjectionLanguage = "injection.language"
	predicateOffset           = "offset!"
)

// Injection is a region of the buffer written in another language.
type Injection struct {
	// Language is the Rune language ID of the region, the one
	// languages.LanguageForFile gives a file of that language.
	Language string
	// Range is the region in cell coordinates, End exclusive.
	Range term.Range
}

// Injections returns the regions injections.scm marks as another language, in
// document order. ok is false until the tree is ready, when parsing failed, or
// when the grammar ships no injections query.
func (t *Tree) Injections() ([]Injection, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.ready || t.closed || t.tree == nil || t.injections == nil {
		return nil, false
	}
	return t.getInjections(), true
}

func (t *Tree) initInjections(language *tree_sitter.Language, injectionsFile string) error {
	data, err := os.ReadFile(injectionsFile)
	if err != nil {
		return fmt.Errorf("read injections file: %v", err)
	}

	injections, qerr := tree_sitter.NewQuery(language, string(data))
	if qerr != nil {
		return fmt.Errorf("compile injections query: %v", qerr)
	}
	// A predicate the binding does not evaluate never filters, so a
	// pattern relying on one would inject its language into every
	// node it structurally matches.
	for i := range injections.PatternCount() {
		if !supportedInjectionPredicates(injections.GeneralPredicates(i)) {
			t.log(log.DebugLevel, "disabling injections pattern %d: unsupported predicate", i)
			injections.DisablePattern(i)
		}
	}

	t.injections = injections
	t.log(log.DebugLevel, "injections initialized")
	return nil
}

// supportedInjectionPredicates reports whether every predicate the query
// compiler left unevaluated is one Injections applies itself.
func supportedInjectionPredicates(preds []tree_sitter.QueryPredicate) bool {
	for _, p := range preds {
		if p.Operator != predicateOffset {
			return false
		}
	}
	return true
}

func (t *Tree) getInjections() []Injection {
	contentIdx, ok := t.injections.CaptureIndexForName(captureInjectionContent)
	if !ok {
		return nil
	}
	langIdx, hasLangCapture := t.injections.CaptureIndexForName(captureInjectionLanguage)

	cur := tree_sitter.NewQueryCursor()
	defer cur.Close()

	var candidates []injectionCandidate
	matches := cur.Matches(t.injections, t.tree.RootNode(), t.content)
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		lang := t.injectionLanguage(&m, langIdx, hasLangCapture)
		if lang == "" {
			continue
		}
		offset := parseInjectionOffset(t.injections.GeneralPredicates(m.PatternIndex), contentIdx)
		for _, cap := range m.Captures {
			if uint(cap.Index) != contentIdx {
				continue
			}
			start, end, ok := offset.apply(cap.Node.StartPosition(), cap.Node.EndPosition())
			if !ok {
				continue
			}
			rng, ok := t.treeSitterRangeToTerm(tree_sitter.Range{StartPoint: start, EndPoint: end})
			if !ok {
				t.log(log.DebugLevel, "could not convert injection points %v-%v to term", start, end)
				continue
			}
			candidates = append(candidates, injectionCandidate{
				node:      cap.Node.Id(),
				pattern:   m.PatternIndex,
				Injection: Injection{Language: lang, Range: rng},
			})
		}
	}
	return resolveInjections(candidates)
}

// injectionLanguage returns the normalized language a match injects: the
// pattern's injection.language property, else the text of its
// @injection.language capture.
func (t *Tree) injectionLanguage(
	m *tree_sitter.QueryMatch, langIdx uint, hasLangCapture bool,
) string {
	for _, prop := range t.injections.PropertySettings(m.PatternIndex) {
		if prop.Key == propertyInjectionLanguage && prop.Value != nil {
			return normalizeInjectionLanguage(*prop.Value)
		}
	}
	if !hasLangCapture {
		return ""
	}
	for _, cap := range m.Captures {
		if uint(cap.Index) == langIdx {
			return normalizeInjectionLanguage(cap.Node.Utf8Text(t.content))
		}
	}
	return ""
}

// normalizeInjectionLanguage maps name, as an injections query spells it, to
// the language ID Rune gives a file with that extension, so "ts" and
// "typescript" both select the typescript grammar. It returns "" for names
// that cannot name a language package.
func normalizeInjectionLanguage(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, "/\\. \t\r\n") {
		return ""
	}
	id, err := languages.LanguageForFile("_." + name)
	if err != nil {
		return ""
	}
	return id
}

// injectionOffset is the adjustment an `#offset!` directive applies to the
// points of the content node, in rows and byte columns.
type injectionOffset struct {
	startRow, startCol, endRow, endCol int
}

// parseInjectionOffset returns the `#offset!` directive of preds that
// targets capture, or a zero offset when there is none or it is malformed.
func parseInjectionOffset(preds []tree_sitter.QueryPredicate, capture uint) injectionOffset {
	for _, p := range preds {
		if p.Operator != predicateOffset || len(p.Args) != 5 {
			continue
		}
		if p.Args[0].CaptureId == nil || *p.Args[0].CaptureId != capture {
			continue
		}
		var vals [4]int
		ok := true
		for i := range vals {
			arg := p.Args[i+1]
			if arg.String == nil {
				ok = false
				break
			}
			n, err := strconv.Atoi(*arg.String)
			if err != nil {
				ok = false
				break
			}
			vals[i] = n
		}
		if !ok {
			continue
		}
		return injectionOffset{
			startRow: vals[0], startCol: vals[1], endRow: vals[2], endCol: vals[3],
		}
	}
	return injectionOffset{}
}

// apply moves start and end by the offset. It reports false when the result
// is not a well-formed range.
func (o injectionOffset) apply(start, end tree_sitter.Point) (
	tree_sitter.Point, tree_sitter.Point, bool,
) {
	sr, sc := int(start.Row)+o.startRow, int(start.Column)+o.startCol
	er, ec := int(end.Row)+o.endRow, int(end.Column)+o.endCol
	if sr < 0 || sc < 0 || er < 0 || ec < 0 || sr > er || (sr == er && sc > ec) {
		return tree_sitter.Point{}, tree_sitter.Point{}, false
	}
	return tree_sitter.Point{Row: uint(sr), Column: uint(sc)},
		tree_sitter.Point{Row: uint(er), Column: uint(ec)}, true
}

// injectionCandidate is one capture of the injections query, before the
// patterns capturing the same content node are reconciled.
type injectionCandidate struct {
	node    uintptr
	pattern uint
	Injection
}

// resolveInjections drops empty regions, keeps for each content node the
// candidate of the last pattern in the query that captured it, and returns
// the rest in document order. Patterns of an inheriting query follow those
// it inherits, so the specific language's rule beats the generic one.
func resolveInjections(candidates []injectionCandidate) []Injection {
	byNode := make(map[uintptr]int, len(candidates))
	kept := make([]injectionCandidate, 0, len(candidates))
	for _, c := range candidates {
		if !coordinatesLess(c.Range.Start, c.Range.End) {
			continue
		}
		if i, ok := byNode[c.node]; ok {
			if c.pattern >= kept[i].pattern {
				kept[i] = c
			}
			continue
		}
		byNode[c.node] = len(kept)
		kept = append(kept, c)
	}
	slices.SortStableFunc(kept, func(a, b injectionCandidate) int {
		return cmp.Or(cmp.Compare(a.Range.Start.Y, b.Range.Start.Y),
			cmp.Compare(a.Range.Start.X, b.Range.Start.X))
	})
	out := make([]Injection, len(kept))
	for i, c := range kept {
		out[i] = c.Injection
	}
	return out
}

func coordinatesLess(a, b term.Coordinates) bool {
	if a.Y != b.Y {
		return a.Y < b.Y
	}
	return a.X < b.X
}
