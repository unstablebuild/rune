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

package search

import (
	"strings"

	fzf "github.com/junegunn/fzf/src/algo"
	"github.com/junegunn/fzf/src/util"
)

const (
	slab16Size int = 100 * 1024 // 200KB * 32 = 12.8MB
	slab32Size int = 2048       // 8KB * 32 = 256KB
)

func init() {
	// fzf matches without case or word-boundary awareness until a scheme is
	// picked; history ranks results the way search always has.
	fzf.Init("history")
}

// Match represents a search match.
type Match struct {
	idx    int
	data   []byte
	tokens *[]int
	res    fzf.Result
}

// Data returns the matched data.
func (m Match) Data() []byte {
	return m.data
}

// Index returns the original input index of this Match.
func (m Match) Index() int {
	return m.idx
}

// Score returns the match score.
func (m Match) Score() int {
	return m.res.Score
}

// Tokens returns the match matching tokens as indeces in Data.
func (m Match) Tokens() []int {
	if m.tokens == nil {
		return nil
	}
	return *m.tokens
}

func makeSlab() *util.Slab {
	return util.MakeSlab(slab16Size, slab32Size)
}

func search(
	algo fzf.Algo, input [][]byte, searchQuery string,
	slab *util.Slab, caseSensitive bool,
	onMatch func(Match),
) {
	const (
		forward   = false
		normalize = false
		withPos   = true // return matched tokens
	)

	if !caseSensitive {
		// if caseSensitive is disabled, search query is expected
		// to be lowercase.
		searchQuery = strings.ToLower(searchQuery)
	}

	searchRunes := []rune(searchQuery)
	for i, data := range input {
		chars := util.ToChars(data)
		res, tokens := algo(caseSensitive, normalize, forward, &chars,
			searchRunes, withPos, slab)
		if res.Start < 0 {
			continue
		}
		match := Match{data: data, res: res, tokens: tokens, idx: i}
		onMatch(match)
	}
}

func simpleSearch(
	algo fzf.Algo, input [][]byte, query string, caseSensitive bool,
) []Match {
	res := make([]Match, 0)
	slab := makeSlab()
	search(algo, input, query, slab, caseSensitive,
		func(m Match) {
			res = append(res, m)
		})
	return res
}

// Fuzzy performs an approximate string matching search on input with query and
// returns a list of matching results.
func Fuzzy(input [][]byte, query string, caseSensitive bool) []Match {
	return simpleSearch(fzf.FuzzyMatchV2, input, query, caseSensitive)
}

// Equal performs a string matching search on input with query and
// returns a list of matching results.
func Equal(input [][]byte, query string, caseSensitive bool) []Match {
	return simpleSearch(fzf.EqualMatch, input, query, caseSensitive)
}

// Contains performs a string contains search on input with query and
// returns a list of matching results.
func Contains(input [][]byte, query string, caseSensitive bool) []Match {
	return simpleSearch(containsMatch, input, query, caseSensitive)
}

func containsMatch(
	caseSensitive bool, normalize bool,
	forward bool, text *util.Chars, pattern []rune, withPos bool, slab *util.Slab,
) (fzf.Result, *[]int) {
	lenPattern := len(pattern)
	if lenPattern == 0 {
		return fzf.Result{Start: 0, End: 0, Score: 1}, nil
	}

	runesStr := string(text.ToRunes())
	if !caseSensitive {
		runesStr = strings.ToLower(runesStr)
	}

	idx := strings.Index(runesStr, string(pattern))
	if idx == -1 {
		return fzf.Result{Start: -1, End: -1, Score: 0}, nil
	}

	tokens := make([]int, 0, len(pattern))
	for i := idx; i < idx+len(pattern); i++ {
		tokens = append(tokens, i)
	}

	return fzf.Result{
		Start: idx,
		End:   idx + lenPattern,
		Score: lenPattern,
	}, &tokens
}
