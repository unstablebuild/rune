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
	"maps"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
)

// bm25 constants and DigUp's column weights: bm25(fts, 3.0, 1.0).
const (
	bm25K1         = 1.2
	bm25B          = 0.75
	bm25NameWeight = 3.0
	bm25BodyWeight = 1.0
	bm25MinIDF     = 1e-6
)

// phrase is one FTS5 phrase of a query: terms as the index keeps them
// (folded and stemmed), matched side by side within one column. With
// prefix, the last term matches every indexed term it starts.
type phrase struct {
	terms  []string
	prefix bool
}

// newPhrase is what FTS5 makes of the query string `"text"` (or `"text"*`
// with prefix): its tokens, through the table's tokenizer.
func newPhrase(text string, prefix bool) phrase {
	return phrase{terms: tokenTexts(ftsTokens(text)), prefix: prefix}
}

func (p phrase) matches(term, want string, last bool) bool {
	if last && p.prefix {
		return strings.HasPrefix(term, want)
	}
	return term == want
}

// instancesAt reports whether the phrase occurs in toks starting at i.
func (p phrase) instanceAt(toks []token, i int) bool {
	if len(p.terms) == 0 || i+len(p.terms) > len(toks) {
		return false
	}
	for j, want := range p.terms {
		if !p.matches(toks[i+j].text, want, j == len(p.terms)-1) {
			return false
		}
	}
	return true
}

// count is how many times the phrase occurs in toks. Occurrences may
// overlap ("data data" occurs twice in "data data data"), as in FTS5.
func (p phrase) count(toks []token) uint32 {
	var n uint32
	for i := range toks {
		if p.instanceAt(toks, i) {
			n++
		}
	}
	return n
}

// rowText returns the indexed columns of row. found is false when the row
// is no longer stored.
type rowText func(ctx context.Context, row uint32) (name, body string, found bool, err error)

// keywordIndex is the FTS5 table of DigUp's index, kept in memory: one row
// per segment with the columns name and body, ranked by bm25 exactly as
// FTS5 ranks `bm25(fts, 3.0, 1.0)`. It keeps term frequencies, not
// positions: a phrase of several terms is confirmed against the row's text.
type keywordIndex struct {
	mu       sync.Mutex
	postings map[string]*posting
	docs     map[uint32]docStats
	totalTok int64
	dict     []string
	dirty    bool
}

// posting lists the rows holding a term, sorted by row, with how often each
// column holds it.
type posting struct {
	term   string
	row    []uint32
	nameTF []uint32
	bodyTF []uint32
}

type docStats struct {
	tokens uint32
	file   uint32
	terms  []string
}

func newKeywordIndex() *keywordIndex {
	return &keywordIndex{postings: map[string]*posting{}, docs: map[uint32]docStats{}}
}

// add indexes row (replacing it if present) as belonging to file.
func (k *keywordIndex) add(row, file uint32, name, body string) {
	tf, tokens := termFrequencies(name, body)
	k.mu.Lock()
	defer k.mu.Unlock()
	k.removeLocked(row)
	st := docStats{tokens: tokens, file: file, terms: make([]string, 0, len(tf))}
	for term, c := range tf {
		p := k.postings[term]
		if p == nil {
			p = &posting{term: term}
			k.postings[term] = p
			k.dirty = true
		}
		i, _ := slices.BinarySearch(p.row, row)
		p.row = slices.Insert(p.row, i, row)
		p.nameTF = slices.Insert(p.nameTF, i, c[0])
		p.bodyTF = slices.Insert(p.bodyTF, i, c[1])
		st.terms = append(st.terms, p.term)
	}
	k.docs[row] = st
	k.totalTok += int64(st.tokens)
}

// load indexes a row that is not indexed yet, in any row order: unlike
// add, it does not keep the postings sorted, so loading a whole index is
// linear. sortPostings must run before the index is searched.
func (k *keywordIndex) load(row, file uint32, name, body string) {
	tf, tokens := termFrequencies(name, body)
	k.mu.Lock()
	defer k.mu.Unlock()
	st := docStats{tokens: tokens, file: file, terms: make([]string, 0, len(tf))}
	for term, c := range tf {
		p := k.postings[term]
		if p == nil {
			p = &posting{term: term}
			k.postings[term] = p
			k.dirty = true
		}
		p.row = append(p.row, row)
		p.nameTF = append(p.nameTF, c[0])
		p.bodyTF = append(p.bodyTF, c[1])
		st.terms = append(st.terms, p.term)
	}
	k.docs[row] = st
	k.totalTok += int64(st.tokens)
}

// sortPostings orders every posting by row after load.
func (k *keywordIndex) sortPostings() {
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, p := range k.postings {
		if slices.IsSorted(p.row) {
			continue
		}
		order := make([]int, len(p.row))
		for i := range order {
			order[i] = i
		}
		slices.SortFunc(order, func(a, b int) int { return cmp.Compare(p.row[a], p.row[b]) })
		row, nameTF, bodyTF := make([]uint32, len(order)), make([]uint32, len(order)), make([]uint32, len(order))
		for i, j := range order {
			row[i], nameTF[i], bodyTF[i] = p.row[j], p.nameTF[j], p.bodyTF[j]
		}
		p.row, p.nameTF, p.bodyTF = row, nameTF, bodyTF
	}
}

// termFrequencies counts each term of the name and body columns, and
// the tokens of both.
func termFrequencies(name, body string) (map[string][2]uint32, uint32) {
	nameToks, bodyToks := ftsTokens(name), ftsTokens(body)
	tf := map[string][2]uint32{}
	for _, t := range nameToks {
		c := tf[t.text]
		c[0]++
		tf[t.text] = c
	}
	for _, t := range bodyToks {
		c := tf[t.text]
		c[1]++
		tf[t.text] = c
	}
	return tf, uint32(len(nameToks) + len(bodyToks))
}

// remove drops row from the index; it is a no-op for an unknown row.
func (k *keywordIndex) remove(row uint32) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.removeLocked(row)
}

func (k *keywordIndex) removeLocked(row uint32) {
	st, ok := k.docs[row]
	if !ok {
		return
	}
	for _, term := range st.terms {
		p := k.postings[term]
		i, found := slices.BinarySearch(p.row, row)
		if !found {
			continue
		}
		p.row = slices.Delete(p.row, i, i+1)
		p.nameTF = slices.Delete(p.nameTF, i, i+1)
		p.bodyTF = slices.Delete(p.bodyTF, i, i+1)
		if len(p.row) == 0 {
			delete(k.postings, term)
			k.dirty = true
		}
	}
	delete(k.docs, row)
	k.totalTok -= int64(st.tokens)
}

// prefixed calls fn with the posting of every term starting with prefix.
func (k *keywordIndex) prefixed(prefix string, fn func(*posting)) {
	if k.dirty {
		k.dict = k.dict[:0]
		for term := range k.postings {
			k.dict = append(k.dict, term)
		}
		sort.Strings(k.dict)
		k.dirty = false
	}
	for i := sort.SearchStrings(k.dict, prefix); i < len(k.dict) && strings.HasPrefix(k.dict[i], prefix); i++ {
		fn(k.postings[k.dict[i]])
	}
}

// lastTerm calls fn with the posting of each indexed term the phrase's last
// term matches.
func (k *keywordIndex) lastTerm(p phrase, fn func(*posting)) {
	last := p.terms[len(p.terms)-1]
	if p.prefix {
		k.prefixed(last, fn)
	} else if post := k.postings[last]; post != nil {
		fn(post)
	}
}

// rowMatch is one row a query matches: how many instances of each phrase
// its columns hold, and its bm25 score (higher is better; FTS5 reports the
// negation).
type rowMatch struct {
	row, file uint32
	tokens    uint32
	name      []uint32
	body      []uint32
	score     float64
}

func (r rowMatch) has(p int) bool { return r.name[p]+r.body[p] > 0 }

// keywordMatch is the result of an FTS5 OR-query of phrases: every row
// matching any of them, best first (ties by row).
type keywordMatch struct {
	phrases []phrase
	rows    []rowMatch
}

// match runs the OR-query of phrases. text confirms the phrases of several
// terms; it is not called under the index's lock.
func (k *keywordIndex) match(ctx context.Context, phrases []phrase, text rowText) (keywordMatch, error) {
	m := keywordMatch{phrases: phrases}
	rows := map[uint32]*rowMatch{}
	get := func(row uint32) *rowMatch {
		r := rows[row]
		if r == nil {
			st := k.docs[row]
			r = &rowMatch{row: row, file: st.file, tokens: st.tokens,
				name: make([]uint32, len(phrases)), body: make([]uint32, len(phrases))}
			rows[row] = r
		}
		return r
	}

	confirm := map[uint32][]int{}
	k.mu.Lock()
	nRows, totalTok := len(k.docs), k.totalTok
	for pi, p := range phrases {
		switch {
		case len(p.terms) == 0:
		case len(p.terms) == 1:
			k.lastTerm(p, func(post *posting) {
				for i, row := range post.row {
					r := get(row)
					r.name[pi] += post.nameTF[i]
					r.body[pi] += post.bodyTF[i]
				}
			})
		default:
			for _, row := range k.candidates(p) {
				get(row)
				confirm[row] = append(confirm[row], pi)
			}
		}
	}
	k.mu.Unlock()

	for _, row := range slices.Sorted(maps.Keys(confirm)) {
		if err := ctx.Err(); err != nil {
			return keywordMatch{}, err
		}
		name, body, found, err := text(ctx, row)
		if err != nil {
			return keywordMatch{}, err
		}
		if !found {
			continue
		}
		nameToks, bodyToks := ftsTokens(name), ftsTokens(body)
		r := rows[row]
		for _, pi := range confirm[row] {
			r.name[pi] = phrases[pi].count(nameToks)
			r.body[pi] = phrases[pi].count(bodyToks)
		}
	}

	hits := make([]int, len(phrases))
	for row, r := range rows {
		matched := false
		for pi := range phrases {
			if r.has(pi) {
				hits[pi]++
				matched = true
			}
		}
		if !matched {
			delete(rows, row)
		}
	}
	if len(rows) == 0 {
		return m, nil
	}

	idf := make([]float64, len(phrases))
	for pi, n := range hits {
		idf[pi] = math.Log((float64(nRows-n) + 0.5) / (float64(n) + 0.5))
		if idf[pi] <= 0 {
			idf[pi] = bm25MinIDF
		}
	}
	avgdl := float64(totalTok) / float64(nRows)
	m.rows = make([]rowMatch, 0, len(rows))
	for _, r := range rows {
		r.score = bm25(idf, avgdl, *r)
		m.rows = append(m.rows, *r)
	}
	slices.SortFunc(m.rows, func(a, b rowMatch) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return cmp.Compare(a.row, b.row)
	})
	return m, nil
}

// candidates are the rows holding every term of a phrase of several terms,
// before their order is confirmed.
func (k *keywordIndex) candidates(p phrase) []uint32 {
	var out []uint32
	k.lastTerm(p, func(post *posting) { out = append(out, post.row...) })
	slices.Sort(out)
	out = slices.Compact(out)
	for _, term := range p.terms[:len(p.terms)-1] {
		post := k.postings[term]
		if post == nil {
			return nil
		}
		out = slices.DeleteFunc(out, func(row uint32) bool {
			_, found := slices.BinarySearch(post.row, row)
			return !found
		})
	}
	return out
}

// bm25 is fts5Bm25Function's score, negated. The conversions keep each
// product rounded on its own, as in C, so scores are bit-identical.
func bm25(idf []float64, avgdl float64, r rowMatch) float64 {
	d := float64(r.tokens)
	norm := bm25K1 * ((1 - bm25B) + float64(bm25B*d)/avgdl)
	score := 0.0
	for pi := range idf {
		freq := float64(float64(r.name[pi])*bm25NameWeight) + float64(float64(r.body[pi])*bm25BodyWeight)
		score += float64(idf[pi] * (float64(freq*(bm25K1+1.0)) / (freq + float64(norm))))
	}
	return score
}

// isIndexed reports whether p matches any row (DigUp's `isIndexed`).
func (k *keywordIndex) isIndexed(ctx context.Context, p phrase, text rowText) (bool, error) {
	if len(p.terms) == 1 && !p.prefix {
		k.mu.Lock()
		defer k.mu.Unlock()
		return k.postings[p.terms[0]] != nil, nil
	}
	m, err := k.match(ctx, []phrase{p}, text)
	return len(m.rows) > 0, err
}

// wordHit is a file found by words: the best row of that file, and its
// 1-based rank among the files found.
type wordHit struct {
	file, row uint32
	rank      int
}

// search takes the best maxRows rows and returns the first maxFiles
// distinct files among them that allowed accepts (Searcher.swift's
// `LIMIT 1000` and its 100 files).
func (m keywordMatch) search(allowed func(file uint32) bool, maxRows, maxFiles int) []wordHit {
	var out []wordHit
	seen := map[uint32]bool{}
	for _, r := range m.rows[:min(maxRows, len(m.rows))] {
		if len(out) >= maxFiles || !allowed(r.file) || seen[r.file] {
			continue
		}
		seen[r.file] = true
		out = append(out, wordHit{file: r.file, row: r.row, rank: len(out) + 1})
	}
	return out
}

// coverage is, for each of files, the share of the phrases that some row of
// the file matches.
func (m keywordMatch) coverage(files []uint32) map[uint32]float64 {
	out := map[uint32]float64{}
	if len(m.phrases) == 0 {
		return out
	}
	want := map[uint32]bool{}
	for _, f := range files {
		want[f] = true
	}
	for pi := range m.phrases {
		found := map[uint32]bool{}
		for _, r := range m.rows {
			if want[r.file] && r.has(pi) {
				found[r.file] = true
			}
		}
		for f := range found {
			out[f] += 1 / float64(len(m.phrases))
		}
	}
	return out
}

// instance is one occurrence of a query phrase in a column, by token
// position.
type instance struct {
	phrase, pos int
}

// columnInstances lists every phrase occurrence in toks in FTS5's xInst
// order: by position, then phrase.
func columnInstances(toks []token, phrases []phrase) []instance {
	var out []instance
	for i := range toks {
		for pi, p := range phrases {
			if p.instanceAt(toks, i) {
				out = append(out, instance{phrase: pi, pos: i})
			}
		}
	}
	return out
}

// snippet is FTS5's snippet() over one column of text with the given
// markers: the window of maxTokens tokens that holds the most distinct
// phrases (preferring one that starts a sentence), the phrases in it marked,
// and ellipsis where it cuts the text. The text between tokens is copied as
// written.
func snippet(text string, phrases []phrase, open, close, ellipsis string, maxTokens int) string {
	toks := ftsTokens(text)
	insts := columnInstances(toks, phrases)
	nDoc := len(toks)
	firsts := sentenceStarts(text, toks)

	bestScore, bestStart := 0, 0
	for _, in := range insts {
		score, adj := snippetScore(insts, phrases, nDoc, in.pos, maxTokens)
		if score > bestScore {
			bestScore, bestStart = score, adj
		}
		if len(firsts) == 0 || nDoc <= maxTokens {
			continue
		}
		jj := 0
		for ; jj < len(firsts)-1; jj++ {
			if firsts[jj+1] > in.pos {
				break
			}
		}
		if firsts[jj] < in.pos {
			score, _ := snippetScore(insts, phrases, nDoc, firsts[jj], maxTokens)
			if firsts[jj] == 0 {
				score += 120
			} else {
				score += 100
			}
			if score > bestScore {
				bestScore, bestStart = score, firsts[jj]
			}
		}
	}
	return highlight(text, toks, insts, phrases, bestStart, bestStart+maxTokens-1, open, close, ellipsis)
}

// sentenceStarts is fts5SentenceFinderCb: the positions of the tokens that
// start the text or follow a '.' or ':' and whitespace.
func sentenceStarts(text string, toks []token) []int {
	var out []int
	for pos, t := range toks {
		if pos == 0 {
			out = append(out, 0)
			continue
		}
		var c byte
		i := t.start - 1
		for ; i >= 0; i-- {
			if c = text[i]; c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				break
			}
		}
		if i != t.start-1 && (c == '.' || c == ':') {
			out = append(out, pos)
		}
	}
	return out
}

// snippetScore is fts5SnippetScore: the window of nToken tokens from pos
// scores 1000 for each phrase it holds and 1 for each repeat. adj is the
// start that centres its instances in the window.
func snippetScore(insts []instance, phrases []phrase, nDoc, pos, nToken int) (score, adj int) {
	seen := make([]bool, len(phrases))
	first, last := -1, 0
	for _, in := range insts {
		if in.pos < pos || in.pos >= pos+nToken {
			continue
		}
		if seen[in.phrase] {
			score++
		} else {
			score += 1000
		}
		seen[in.phrase] = true
		if first < 0 {
			first = in.pos
		}
		last = in.pos + len(phrases[in.phrase].terms)
	}
	adj = first - (nToken-(last-first))/2
	if adj+nToken > nDoc {
		adj = nDoc - nToken
	}
	return score, max(adj, 0)
}

// coalesced is fts5CInstIter: phrase instances, overlapping ones merged,
// as token ranges [start, end]. start is -1 past the last.
type coalesced struct {
	insts      []instance
	phrases    []phrase
	next       int
	start, end int
}

func (c *coalesced) advance() {
	c.start, c.end = -1, -1
	for ; c.next < len(c.insts); c.next++ {
		in := c.insts[c.next]
		end := in.pos - 1 + len(c.phrases[in.phrase].terms)
		if c.start < 0 {
			c.start, c.end = in.pos, end
		} else if in.pos <= c.end {
			c.end = max(c.end, end)
		} else {
			break
		}
	}
}

// highlight is fts5HighlightCb over the tokens [rangeStart, rangeEnd] of
// text, wrapped as fts5SnippetFunction wraps it.
func highlight(text string, toks []token, insts []instance, phrases []phrase, rangeStart, rangeEnd int,
	open, close, ellipsis string) string {
	var out strings.Builder
	if rangeStart > 0 {
		out.WriteString(ellipsis)
	}
	it := coalesced{insts: insts, phrases: phrases}
	it.advance()
	for it.start >= 0 && it.start < rangeStart {
		it.advance()
	}
	off, isOpen := 0, false
	for pos, t := range toks {
		if pos < rangeStart || pos > rangeEnd {
			continue
		}
		if rangeStart > 0 && pos == rangeStart {
			off = t.start
		}
		if isOpen && (pos <= it.start || it.start < 0) && t.start > off {
			out.WriteString(close)
			isOpen = false
		}
		if pos == it.start && !isOpen {
			out.WriteString(text[off:t.start])
			out.WriteString(open)
			off, isOpen = t.start, true
		}
		if pos == it.end {
			if !isOpen {
				out.WriteString(open)
				isOpen = true
			}
			out.WriteString(text[off:t.end])
			off = t.end
			it.advance()
		}
		if pos == rangeEnd {
			if isOpen {
				if it.start >= 0 && pos >= it.start {
					out.WriteString(text[off:t.end])
					off = t.end
				}
				out.WriteString(close)
				isOpen = false
			}
			out.WriteString(text[off:t.end])
			off = t.end
		}
	}
	if isOpen {
		out.WriteString(close)
	}
	if rangeEnd >= len(toks)-1 {
		out.WriteString(text[off:])
	} else {
		out.WriteString(ellipsis)
	}
	return out.String()
}
