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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"

	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/languages"
	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/syntax"
	"unstable.build/rune/internal/workspace/walkdir"
)

// SearchMulti runs every query in queries against a single parse of each
// workspace file and streams results tagged with the originating query ID.
// Reading, buffering and parsing each file happens once regardless of how
// many queries are supplied, so resolution that previously issued one
// workspace walk per query now issues one walk total. The optional langs
// restrict the walk to files of those languages.
func (p Parser) SearchMulti(
	queries []symbolresolve.MultiQuery, langs ...string,
) (iterator.Iterator[symbolresolve.MultiResult], error) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = walkdir.WithContextFilter(ctx, p.filter.get())
	paths, err := walkdir.ListFiles(ctx, p.w, ".")
	if err != nil {
		cancel()
		return nil, err
	}

	files := make(chan string)
	results := make(chan symbolresolve.MultiResult)
	closeWaitCh := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(defaultWorkers)
	errs := make([]error, defaultWorkers)
	validErrors := make([]map[string]*expectedError, defaultWorkers)
	for i := range defaultWorkers {
		validErrors[i] = make(map[string]*expectedError)
		err := &errs[i]
		expectedErrors := validErrors[i]
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			readSymbolsWorkerMulti(ctx, p.w, p.pkg, p.uri, queries, results, files,
				err, expectedErrors, langs)
		})
	}

	it := &chanIterator[symbolresolve.MultiResult]{
		ctx:         ctx,
		ch:          results,
		cancel:      cancel,
		closeWaitCh: closeWaitCh,
	}

	var itErr error
	go debug.CapturePanicReport(func() {
		defer close(closeWaitCh)
		defer close(results)
		defer paths.Close()

		for {
			file, ok := paths.Next(ctx)
			if !ok {
				break
			}
			select {
			case files <- file:
				continue
			case <-ctx.Done():
			}
			break
		}
		if err := paths.Err(); err != nil {
			itErr = err
		}
		close(files)
		wg.Wait()

		it.mu.Lock()
		defer it.mu.Unlock()
		it.err = itErr
		for _, err := range errs {
			if err != nil {
				it.err = errors.Join(it.err, err)
			}
		}
	})
	return it, nil
}

// compiledQueries pairs a loaded language with the per-query compiled
// tree-sitter queries that a worker runs against each file of that language.
type compiledQueries struct {
	lang    *loadedLanguage
	queries []compiledQuery
}

type compiledQuery struct {
	id       int
	query    *sitter.Query
	captures []string
	// perCapture emits each captured node as its own single-capture
	// match (node-capture queries) instead of grouping the declared
	// captures into one tuple per match.
	perCapture bool
}

func (c *compiledQueries) close() {
	for _, q := range c.queries {
		if q.query != nil {
			q.query.Close()
		}
	}
	if c.lang != nil {
		c.lang.close()
	}
}

func readSymbolsWorkerMulti(
	ctx context.Context, fs workspaceapi.FileSystem, pkg syntax.PkgManager,
	uri workspaceapi.URI, queries []symbolresolve.MultiQuery,
	results chan symbolresolve.MultiResult, files chan string,
	err *error, expectedErrors map[string]*expectedError, langs []string,
) {
	compiled := make(map[string]*compiledQueries)
	defer func() {
		for _, c := range compiled {
			c.close()
		}
	}()
	var scratch fileScratch
	for {
		select {
		case <-ctx.Done():
			return
		case path, ok := <-files:
			if !ok {
				return
			}
			langID, lerr := languages.LanguageForFile(path)
			if lerr != nil {
				continue
			}
			if len(langs) != 0 && !slices.Contains(langs, langID) {
				continue
			}
			c, ok := compiled[langID]
			if !ok {
				var cerr error
				c, cerr = compileQueriesForLang(ctx, pkg, langID, queries)
				if cerr != nil {
					if errors.Is(cerr, errNotInstalled) {
						ext := filepath.Ext(path)
						if _, ok := expectedErrors[ext]; !ok {
							expectedErrors[ext] = &expectedError{}
						}
						expectedErrors[ext].missingLanguage++
						continue
					}
					*err = errors.Join(*err, cerr)
					continue
				}
				compiled[langID] = c
			}
			emit := func(r symbolresolve.MultiResult) error {
				select {
				case results <- r:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			if readErr := readFileSymbolsMulti(ctx, c, uri, fs, path, &scratch, emit); readErr != nil {
				*err = errors.Join(*err, readErr)
			}
		}
	}
}

// NewQuerySession returns a batched per-file query session: each QueryMulti
// call parses the file once and runs every query on that single tree,
// caching the loaded language and compiled batch across calls. Not safe
// for concurrent use; create one per goroutine and Close it to release
// the cached languages.
func (p Parser) NewQuerySession() symbolresolve.QuerySession {
	return &querySession{
		w:        p.w,
		pkg:      p.pkg,
		uri:      p.uri,
		compiled: make(map[string]*langBatch),
	}
}

// langBatch caches one language's compiled query batch together with
// the queries it was compiled from, so a changed batch recompiles.
type langBatch struct {
	queries []symbolresolve.MultiQuery
	c       *compiledQueries
}

type querySession struct {
	w        workspaceapi.FileSystem
	pkg      syntax.PkgManager
	uri      workspaceapi.URI
	compiled map[string]*langBatch
	// scratch is the recycled file-content buffer; safe because the
	// session is single-goroutine and emitted results copy text out.
	scratch fileScratch
}

func (f *querySession) QueryMulti(
	ctx context.Context, file workspaceapi.URI,
	queries []symbolresolve.MultiQuery,
) ([]symbolresolve.MultiResult, error) {
	path := file.Path()
	langID, err := languages.LanguageForFile(path)
	if err != nil {
		return nil, err
	}
	b := f.compiled[langID]
	if b == nil || !slices.EqualFunc(b.queries, queries, sameMultiQuery) {
		c, cerr := compileQueriesForLang(ctx, f.pkg, langID, queries)
		if cerr != nil {
			return nil, cerr
		}
		if b != nil {
			b.c.close()
		}
		b = &langBatch{queries: slices.Clone(queries), c: c}
		f.compiled[langID] = b
	}
	var out []symbolresolve.MultiResult
	err = readFileSymbolsMulti(ctx, b.c, f.uri, f.w, path, &f.scratch,
		func(r symbolresolve.MultiResult) error {
			out = append(out, r)
			return nil
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (f *querySession) Close() error {
	for _, b := range f.compiled {
		b.c.close()
	}
	clear(f.compiled)
	return nil
}

func sameMultiQuery(a, b symbolresolve.MultiQuery) bool {
	return a.ID == b.ID && a.Query == b.Query && a.Nodes == b.Nodes &&
		slices.Equal(a.Captures, b.Captures)
}

// compileQueriesForLang loads langID once and compiles every query against
// it, so a worker shares a single parser and language handle across all
// queries for that language.
func compileQueriesForLang(
	ctx context.Context, pkg syntax.PkgManager, langID string,
	queries []symbolresolve.MultiQuery,
) (*compiledQueries, error) {
	queryFile := ""
	for _, q := range queries {
		if q.Nodes != 0 {
			queryFile = LocalsFilename
			break
		}
	}
	lang, localsText, err := loadLanguage(ctx, langID, pkg, queryFile, "")
	if err != nil {
		return nil, err
	}
	c := &compiledQueries{lang: lang, queries: make([]compiledQuery, 0, len(queries))}
	for _, q := range queries {
		text, captures, perCapture := q.Query, q.Captures, false
		if q.Nodes != 0 {
			var nerr error
			if captures, nerr = nodeTypesToCaptureNames(q.Nodes); nerr != nil {
				c.close()
				return nil, nerr
			}
			text, perCapture = localsText, true
		}
		compiledQ, qerr := compileQuery(lang.lang, text)
		if qerr != nil {
			c.close()
			return nil, fmt.Errorf("new parser for language %q: %v", langID, qerr)
		}
		c.queries = append(c.queries, compiledQuery{
			id: q.ID, query: compiledQ, captures: captures,
			perCapture: perCapture,
		})
	}
	return c, nil
}

// fileScratch holds buffers a single worker or session recycles across
// sequential file reads. Safe only because emitted results copy data out
// of them (makeSymbolItem converts to string).
type fileScratch struct {
	content []byte
	starts  []int
}

// Scratch buffers above these caps are dropped after each file instead of
// recycled: a single pathological file (generated code, minified bundles)
// must not pin its size for the lifetime of a long-lived session or
// worker.
const (
	maxScratchContent = 4 << 20
	maxScratchStarts  = 256 << 10
)

func (s *fileScratch) trim() {
	if cap(s.content) > maxScratchContent {
		s.content = nil
	}
	if cap(s.starts) > maxScratchStarts {
		s.starts = nil
	}
}

// readAllInto reads r to EOF into buf's spare capacity, growing it as
// needed, and returns the filled slice. Recycling buf across files avoids
// io.ReadAll's per-file append-doubling garbage, which dominated heap
// churn (madvise + GC) while indexing large workspaces.
func readAllInto(buf []byte, r io.Reader) ([]byte, error) {
	buf = buf[:0]
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, err := r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buf, nil
			}
			return buf, err
		}
	}
}

func readFileSymbolsMulti(
	ctx context.Context, c *compiledQueries, uri workspaceapi.URI,
	w workspaceapi.FileSystem, filename string, scratch *fileScratch,
	emit func(symbolresolve.MultiResult) error,
) (retErr error) {
	defer scratch.trim()
	file, err := w.OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open file: %v", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close file: %v", cerr))
		}
	}()

	content, err := readAllInto(scratch.content, file)
	scratch.content = content
	if err != nil {
		return err
	}

	tree := c.lang.parser.Parse(content, nil)
	if tree == nil {
		return errors.New("failed to parse data")
	}
	defer tree.Close()
	root := tree.RootNode()
	starts := lineStarts(scratch.starts, content)
	scratch.starts = starts
	fileURI := workspaceapi.Join(uri, filename)

	for _, q := range c.queries {
		if err := runQueryOnTree(ctx, q, root, content, starts, fileURI, emit); err != nil {
			retErr = errors.Join(retErr, err)
		}
	}
	return retErr
}

func runQueryOnTree(
	ctx context.Context, q compiledQuery, root *sitter.Node, content []byte,
	starts []int, fileURI workspaceapi.URI,
	emit func(symbolresolve.MultiResult) error,
) (retErr error) {
	cur := sitter.NewQueryCursor()
	defer cur.Close()

	captureNames := q.query.CaptureNames()
	matches := cur.Matches(q.query, root, content)
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		if err := ctx.Err(); err != nil {
			return retErr
		}
		if q.perCapture {
			for _, cap := range m.Captures {
				if int(cap.Index) >= len(captureNames) ||
					!slices.Contains(q.captures, captureNames[cap.Index]) {
					continue
				}
				r := makeSymbolItem(content, starts, cap.Node.Range(),
					fileURI, captureNames[cap.Index])
				if err := emit(symbolresolve.MultiResult{
					QueryID: q.id, Match: []syntaxapi.Result{r},
				}); err != nil {
					return retErr
				}
			}
			continue
		}
		match := groupMatchCaptures(
			content, starts, m, captureNames, fileURI, q.captures)
		if match == nil {
			continue
		}
		if err := emit(symbolresolve.MultiResult{QueryID: q.id, Match: match}); err != nil {
			return retErr
		}
	}
	return retErr
}

// groupMatchCaptures builds one match's captures in the declared capture
// order, so consumers receive them pre-paired instead of re-associating a
// flattened stream. Matches missing a declared capture are dropped (nil):
// they cannot form the tuple the query's consumer expects. With no
// declared captures every capture is kept in match order.
func groupMatchCaptures(
	content []byte, starts []int, m sitter.QueryMatch,
	captureNames []string, fileURI workspaceapi.URI, declared []string,
) []syntaxapi.Result {
	if len(declared) == 0 {
		out := make([]syntaxapi.Result, 0, len(m.Captures))
		for _, cap := range m.Captures {
			if int(cap.Index) >= len(captureNames) {
				continue
			}
			out = append(out, makeSymbolItem(
				content, starts, cap.Node.Range(), fileURI, captureNames[cap.Index],
			))
		}
		return out
	}
	out := make([]syntaxapi.Result, len(declared))
	for slot, name := range declared {
		found := false
		for _, cap := range m.Captures {
			if int(cap.Index) < len(captureNames) && captureNames[cap.Index] == name {
				out[slot] = makeSymbolItem(
					content, starts, cap.Node.Range(), fileURI, name)
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	return out
}
