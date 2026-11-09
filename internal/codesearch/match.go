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

package codesearch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"slices"

	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

const maxFileLen = 64 << 20

// compile returns the regular expression of req, which matches by line.
func compile(req searchapi.RegexpRequest) (string, *regexp.Regexp, error) {
	pattern := "(?m)" + req.Pattern
	if req.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	return pattern, re, err
}

// included returns the paths whose base name matches one of the globs,
// or every path when there are none. It fails on a malformed glob.
func included(paths, globs []string) ([]string, error) {
	if len(globs) == 0 {
		return paths, nil
	}
	for _, g := range globs {
		if _, err := path.Match(g, ""); err != nil {
			return nil, fmt.Errorf("include %q: %w", g, err)
		}
	}
	return slices.DeleteFunc(paths, func(rel string) bool {
		return !slices.ContainsFunc(globs, func(g string) bool {
			ok, _ := path.Match(g, path.Base(rel))
			return ok
		})
	}), nil
}

// matcher reads the files that next lists and returns their matching
// lines, as an iterator.Iterator.
type matcher struct {
	fs         workspaceapi.FileSystem
	re         *regexp.Regexp
	next       func(context.Context) (string, bool, error)
	limit      int
	maxPerFile int

	count   int
	pending []searchapi.RegexpResult
}

func newMatcher(
	fsys workspaceapi.FileSystem, re *regexp.Regexp, limit, maxPerFile int,
	next func(context.Context) (string, bool, error),
) iterator.Iterator[searchapi.RegexpResult] {
	m := &matcher{fs: fsys, re: re, next: next, limit: limit, maxPerFile: maxPerFile}
	return iterator.FromFunc(m.match, func() error { return nil })
}

func (m *matcher) match(ctx context.Context) (searchapi.RegexpResult, bool, error) {
	for len(m.pending) == 0 {
		if m.limit > 0 && m.count >= m.limit {
			return searchapi.RegexpResult{}, false, nil
		}
		if err := ctx.Err(); err != nil {
			return searchapi.RegexpResult{}, false, err
		}
		rel, ok, err := m.next(ctx)
		if err != nil || !ok {
			return searchapi.RegexpResult{}, false, err
		}
		data, ok := m.read(rel)
		if ok {
			m.pending = matchLines(m.re, rel, data, m.maxPerFile)
		}
	}
	mt := m.pending[0]
	m.pending = m.pending[1:]
	m.count++
	return mt, true, nil
}

// read reads the file rel, if it has no NUL byte and is at most maxFileLen bytes.
func (m *matcher) read(rel string) ([]byte, bool) {
	f, err := m.fs.OpenFile(rel, os.O_RDONLY, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxFileLen+1))
	if err != nil || len(data) > maxFileLen || bytes.IndexByte(data, 0) >= 0 {
		return nil, false
	}
	return data, true
}

// matchLines returns the lines of data, the content of the file rel,
// that re matches. A match across lines does not count: only the
// matches within a line do. It stops at limit lines, unless limit is 0.
func matchLines(re *regexp.Regexp, rel string, data []byte, limit int) []searchapi.RegexpResult {
	var out []searchapi.RegexpResult
	line, counted := 1, 0
	for pos := 0; pos < len(data) && (limit == 0 || len(out) < limit); {
		loc := re.FindIndex(data[pos:])
		if loc == nil {
			break
		}
		start := pos + loc[0]
		lineStart := bytes.LastIndexByte(data[:start], '\n') + 1
		lineEnd := len(data)
		if i := bytes.IndexByte(data[start:], '\n'); i >= 0 {
			lineEnd = start + i
		}
		line += bytes.Count(data[counted:lineStart], []byte{'\n'})
		counted = lineStart
		text := bytes.TrimSuffix(data[lineStart:lineEnd], []byte{'\r'})
		if spans := re.FindAllIndex(text, -1); spans != nil {
			mt := searchapi.RegexpResult{Path: rel, Line: line, Text: string(text), Spans: make([][2]int, len(spans))}
			for i, s := range spans {
				mt.Spans[i] = [2]int{s[0], s[1]}
			}
			out = append(out, mt)
		}
		if lineEnd == len(data) {
			break
		}
		pos = lineEnd + 1
	}
	return out
}
