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

package llmsearch

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
)

const (
	// zGate is how far a vector must stand out of a query's scores to
	// count as a meaning match.
	zGate = 2.0
	// codeWordWeight is how much finding the query's words counts against
	// meaning: code's words are everywhere, so less than in documents.
	codeWordWeight = 1.0
	// maxWordRows and maxWordFiles bound the keyword matches considered.
	maxWordRows  = 1000
	maxWordFiles = 100
	// definitionRows bounds the stretches of a file a name's definition is
	// looked for in.
	definitionRows = 300
	// definitionRanks is how many of the first results a name's
	// definition is looked for in.
	definitionRanks = 10
	excerptLimit    = 400
	snippetTokens   = 12
)

// searcher answers one query: DigUp's hybrid of meaning (each file's best
// vector, put on the query's own scale) and words (FTS5's bm25). It reads
// the segments it needs once.
type searcher struct {
	st    *store
	cat   *catalog
	cache map[uint32]segmentDoc
}

func newSearcher(st *store, cat *catalog) *searcher {
	return &searcher{st: st, cat: cat, cache: map[uint32]segmentDoc{}}
}

// meaningMatch is a file's best vector for the query.
type meaningMatch struct {
	z, cosine float64
	seg       uint32
}

// wordMatch is a file found by words: its best segment and the words
// marked in it.
type wordMatch struct {
	rank    int
	seg     uint32
	snippet string
}

// search returns the limit best files for text among those allowed, best
// first. Without queryVector it is keyword search alone; with prefixLast
// the last word is still being typed.
func (s *searcher) search(
	ctx context.Context, text string, queryVector []float32, allowed func(path string) bool, limit int, prefixLast bool,
) ([]searchapi.SemanticResult, error) {
	allowedFile := s.allowed(allowed)

	best := s.meaning(queryVector, allowedFile)

	terms := Terms(text)
	ident := identifier(text)
	var phrases []phrase
	if ident != nil {
		phrases = []phrase{identifierPhrase(ident)}
	} else {
		phrases = Patterns(terms, prefixLast)
		// A word that is in no file as typed matches as the start of
		// longer words, as the last one does while it is typed. Words of
		// two letters start too many words to stand for one.
		for i, t := range terms {
			if utf8.RuneCountInString(t) < 3 || phrases[i].prefix {
				continue
			}
			found, err := s.cat.kw.isIndexed(ctx, phrases[i], s.rowText)
			if err != nil {
				return nil, err
			}
			phrases[i].prefix = !found
		}
	}
	var match keywordMatch
	words := map[uint32]wordMatch{}
	coverage := map[uint32]float64{}
	if len(phrases) > 0 {
		var err error
		if match, err = s.cat.kw.match(ctx, phrases, s.rowText); err != nil {
			return nil, err
		}
		hits := match.search(allowedFile, maxWordRows, maxWordFiles)
		files := make([]uint32, 0, len(hits))
		for _, h := range hits {
			d, _, err := s.segment(ctx, h.row)
			if err != nil {
				return nil, err
			}
			// Text that normalizing changed is quoted as it is written.
			snip, ok := "", false
			if d.Original != "" {
				snip, ok = Snippet(d.Original, terms, snippetTokens)
			}
			if !ok {
				snip = snippet(d.Body, phrases, "«", "»", "…", snippetTokens)
			}
			words[h.file] = wordMatch{rank: h.rank, seg: h.row, snippet: snip}
			files = append(files, h.file)
		}
		coverage = match.coverage(files)
	}

	// A file qualifies by standing out in meaning, or by holding at least
	// half of the query's words. One common word cannot beat a strong
	// meaning match; all the words of an exact lookup usually do.
	lookup := IsLookup(terms) || ident != nil
	meaningWeight, wordsWeight := 1.0, codeWordWeight
	if lookup {
		meaningWeight, wordsWeight = 0.5, codeWordWeight*2
	}
	candidates := map[uint32]bool{}
	for file, m := range best {
		if m.z >= zGate {
			candidates[file] = true
		}
	}
	for file, share := range coverage {
		if share >= 0.5 {
			candidates[file] = true
		}
	}
	type scored struct {
		file  uint32
		score float64
	}
	ranked := make([]scored, 0, len(candidates))
	for file := range candidates {
		ranked = append(ranked, scored{file, meaningWeight*max(best[file].z, 0) +
			wordsWeight*math.Pow(coverage[file], 1.5)})
	}
	slices.SortFunc(ranked, func(a, b scored) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return cmp.Compare(a.file, b.file)
	})

	var hits []searchapi.SemanticResult
	for _, r := range ranked[:min(limit, len(ranked))] {
		semantic, hasMeaning := best[r.file]
		keyword, hasWords := words[r.file]
		// Point at the best meaning segment if it stands out; else at
		// the segment the words matched.
		useMeaning := hasMeaning && (semantic.z >= zGate || !hasWords)
		var seg uint32
		switch {
		case useMeaning:
			seg = semantic.seg
		case hasWords:
			seg = keyword.seg
		default:
			continue
		}
		// Where a name is defined is looked for in the first results
		// only: a name used all over a big repo is in hundreds of
		// stretches.
		var lookFor []string
		if len(hits) < definitionRanks {
			lookFor = ident
		}
		wordsHere := hasWords && keyword.seg == seg || ident != nil
		seg, focus, err := s.codePlace(ctx, match, r.file, seg, lookFor, text, terms, wordsHere)
		if err != nil {
			return nil, err
		}
		f, ok := s.cat.file(r.file)
		if !ok {
			continue
		}
		d, found, err := s.segment(ctx, seg)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		// Quote the matched words only when they come from the segment
		// pointed at.
		snip := ""
		if hasWords && keyword.seg == seg && strings.Contains(keyword.snippet, "«") {
			snip = excerpt(keyword.snippet, excerptLimit)
		}
		if snip == "" && d.Excerpt == "" && d.Loc == 0 && hasWords && strings.Contains(keyword.snippet, "«") {
			if info, ok := s.cat.segment(keyword.seg); ok && info.kind != segmentName {
				snip = excerpt(keyword.snippet, excerptLimit)
			}
		}
		if snip == "" {
			snip = d.Excerpt
		}
		hit := searchapi.SemanticResult{
			Path: f.Path, Kind: kindCode, Score: r.score, Segment: d.Kind, Loc: d.Loc, LocEnd: d.LocEnd,
			Focus: focus, Excerpt: snip, Modified: f.ModTime / 1e9, Size: f.Size, Text: d.line(focus),
		}
		if hasMeaning {
			hit.Z, hit.Cosine = semantic.z, semantic.cosine
		}
		if hasWords {
			hit.KeywordRank = keyword.rank
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

// allowed turns a filter of paths into one of file IDs, asking about each
// file once.
func (s *searcher) allowed(allowed func(path string) bool) func(file uint32) bool {
	known := map[uint32]bool{}
	return func(file uint32) bool {
		ok, seen := known[file]
		if !seen {
			f, exists := s.cat.file(file)
			ok = exists && (allowed == nil || allowed(f.Path))
			known[file] = ok
		}
		return ok
	}
}

// meaning scores every vector on this query's scale and keeps each
// allowed file's best.
func (s *searcher) meaning(query []float32, allowed func(file uint32) bool) map[uint32]meaningMatch {
	best := map[uint32]meaningMatch{}
	if len(query) != dims {
		return best
	}
	s.cat.mu.Lock()
	vec := s.cat.vec
	if vec == nil || vec.rows() == 0 {
		s.cat.mu.Unlock()
		return best
	}
	scores := vec.score(query)
	rowSeg, rowFile := slices.Clone(vec.rowSeg), slices.Clone(vec.rowFile)
	s.cat.mu.Unlock()

	z := meaning(scores)
	for row, file := range rowFile {
		if b, ok := best[file]; (ok && z[row] <= b.z) || !allowed(file) {
			continue
		}
		best[file] = meaningMatch{z: z[row], cosine: float64(scores[row]), seg: rowSeg[row]}
	}
	return best
}

// codePlace is the stretch to point at and the line in it to open at. A
// name looked up points at the stretch that defines it, when one does
// (not the ones that call it). Otherwise the line is the first with the
// query's words, when they matched in this stretch; a match by meaning
// alone opens at the stretch's first line (focus 0).
func (s *searcher) codePlace(
	ctx context.Context, match keywordMatch, file, seg uint32, ident []string, query string, terms []string,
	wordsHere bool,
) (_ uint32, focus int, _ error) {
	name := strings.ReplaceAll(strings.TrimSpace(query), "()", "")
	if ident != nil {
		type row struct {
			seg uint32
			loc int
		}
		var rows []row
		for _, r := range match.rows {
			if info, ok := s.cat.segment(r.row); ok && r.file == file {
				rows = append(rows, row{r.row, info.loc})
			}
		}
		slices.SortFunc(rows, func(a, b row) int {
			if c := cmp.Compare(a.loc, b.loc); c != 0 {
				return c
			}
			return cmp.Compare(a.seg, b.seg)
		})
		var bestSeg uint32
		bestLine, bestStrength := 0, 0
		for _, r := range rows[:min(len(rows), definitionRows)] {
			if bestStrength >= 2 {
				break
			}
			d, found, err := s.segment(ctx, r.seg)
			if err != nil {
				return 0, 0, err
			}
			if !found {
				continue
			}
			if line, strength, ok := definition(name, d.text(), d.Loc); ok && strength > bestStrength {
				bestSeg, bestLine, bestStrength = r.seg, line, strength
			}
		}
		if bestStrength > 0 {
			return bestSeg, bestLine, nil
		}
	}
	if !wordsHere && ident == nil {
		return seg, 0, nil
	}
	d, found, err := s.segment(ctx, seg)
	if err != nil || !found {
		return seg, 0, err
	}
	words := terms
	if ident != nil {
		words = []string{strings.ToLower(name)}
	}
	line, _ := firstLine(words, d.text(), d.Loc)
	return seg, line, nil
}

// segment reads a segment, once per search.
func (s *searcher) segment(ctx context.Context, id uint32) (segmentDoc, bool, error) {
	if d, ok := s.cache[id]; ok {
		return d, true, nil
	}
	d, found, err := s.st.segment(ctx, id)
	if found {
		s.cache[id] = d
	}
	return d, found, err
}

// rowText is the keyword columns of a row, to confirm phrases of several
// words.
func (s *searcher) rowText(ctx context.Context, row uint32) (name, body string, found bool, err error) {
	d, found, err := s.segment(ctx, row)
	return d.Name, d.Body, found, err
}

// excerpt is text in one line of at most limit characters, for result
// lists. Only its start is cleaned up.
func excerpt(text string, limit int) string {
	start := prefixRunes(text, limit*8)
	line := strings.Join(strings.Fields(start), " ")
	if utf8.RuneCountInString(line) > limit || len(start) < len(text) {
		return prefixRunes(line, limit) + "…"
	}
	return line
}

// StrongCount is how many of hits (best first) are strong: scoring at
// least ratio times the top hit. The rest pass the gates but trail far
// behind; DigUp folds them under "weaker matches" with a ratio of 0.5.
func StrongCount(hits []searchapi.SemanticResult, ratio float64) int {
	if len(hits) == 0 || hits[0].Score <= 0 {
		return len(hits)
	}
	n := 0
	for _, h := range hits {
		if h.Score < hits[0].Score*ratio {
			break
		}
		n++
	}
	return n
}
