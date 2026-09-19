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

package syntax

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/sirupsen/logrus"
	sitter "github.com/tree-sitter/go-tree-sitter"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/languages"
	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace/walkdir"
)

// NewParser returns the workspace-wide parser. The concrete type is
// exported so host-side consumers can use the paired query entry points
// (Query2, Search2) that are not part of the public syntaxapi.Parser
// surface.
func NewParser(
	w workspaceapi.FileSystem, pkg PkgManager, uri workspaceapi.URI,
) Parser {
	filter := &queryFilter{w: w}
	return Parser{
		w:      w,
		uri:    uri,
		pkg:    newCachingPkgManager(pkg),
		specs:  &specCache{fs: w, filter: filter},
		filter: filter,
	}
}

var defaultWorkers = runtime.NumCPU()

// Parser is the workspace-wide tree-sitter parser and searcher.
type Parser struct {
	w     workspaceapi.FileSystem
	pkg   PkgManager
	uri   workspaceapi.URI
	specs *specCache
	// filter prunes noise/dependency directories (gitignored entries and
	// hidden directories) from the workspace source-code walks. It is built
	// once per parser and shared with specCache.
	filter *queryFilter
}

// queryFilter lazily builds and caches the walkdir.Filter applied to the
// workspace source-code walks. LoadGitignore walks the tree to read
// .gitignore files, so the matcher is built at most once per parser and
// reused across every Search/SearchNode/DetectSpecs invocation.
type queryFilter struct {
	w    workspaceapi.FileSystem
	once sync.Once
	f    walkdir.Filter
}

// get returns the cached filter, building it on first use. Failure to load
// the gitignore matcher degrades to hidden-directory pruning only rather
// than failing the walk. A nil filesystem (highlight-only parsers) yields a
// hidden-directory-only filter.
func (q *queryFilter) get() walkdir.Filter {
	q.once.Do(func() {
		hidden := hiddenDirMatcher{vctrl.HiddenBaseMatcher()}
		m, err := vctrl.LoadGitignore(q.w)
		if err != nil {
			q.f = hidden
			return
		}
		q.f = vctrl.AnyMatcher(m, hidden)
	})
	return q.f
}

// hiddenDirMatcher restricts a basename-hidden matcher to directories so
// that hidden source files at visible paths (e.g. a hand-written .foo.py)
// are still scanned, while hidden directories like .venv or .git are pruned.
type hiddenDirMatcher struct{ m vctrl.Matcher }

func (h hiddenDirMatcher) Match(uri workspaceapi.URI, isDir bool) bool {
	return isDir && h.m.Match(uri, isDir)
}

func (h hiddenDirMatcher) MatchRelPath(relpath string, isDir bool) bool {
	return isDir && h.m.MatchRelPath(relpath, isDir)
}

var (
	_ syntaxapi.Parser       = Parser{}
	_ symbolresolve.Searcher = Parser{}
)

// specCache memoizes the workspace language detection so the file walk runs
// once per parser. The walk is started lazily on the first detect call and
// streamed: each detect iterator yields specs as the background walk discovers
// them, so a consumer can begin resolving against the first detected language
// before the walk completes. Later callers replay the same growing cache.
type specCache struct {
	fs walkdir.Reader
	// filter prunes noise/dependency directories from the detection walk.
	// Shared with the owning Parser so the gitignore matcher is
	// built once.
	filter *queryFilter
	// source produces the spec stream to cache. It defaults to
	// symbolresolve.DetectSpecs and exists so tests can drive detection
	// without a real filesystem walk.
	source func(context.Context, walkdir.Reader) iterator.Iterator[symbolresolve.Spec]

	mu       sync.Mutex
	started  bool
	done     bool
	detected []symbolresolve.Spec
	// updated is closed (and replaced) whenever a spec is appended or the
	// walk finishes, so blocked detect iterators wake without polling.
	updated chan struct{}
}

func (c *specCache) detect(context.Context) iterator.Iterator[symbolresolve.Spec] {
	c.mu.Lock()
	if !c.started {
		c.started = true
		c.updated = make(chan struct{})
		go debug.CapturePanicReport(c.run)
	}
	c.mu.Unlock()
	return &cachedSpecIterator{cache: c}
}

// run drains DetectSpecs once, appending each spec to the shared cache and
// waking any blocked iterators. It uses a background context so the single
// workspace walk is independent of whichever caller happened to start it.
func (c *specCache) run() {
	ctx := context.Background()
	source := c.source
	if source == nil {
		source = symbolresolve.DetectSpecs
		// Only the real workspace walk needs noise/dependency pruning; tests
		// inject their own source and leave filter nil.
		ctx = walkdir.WithContextFilter(ctx, c.filter.get())
	}
	it := source(ctx, c.fs)
	defer func() { _ = it.Close() }()
	for {
		spec, ok := it.Next(ctx)
		if !ok {
			break
		}
		c.mu.Lock()
		c.detected = append(c.detected, spec)
		c.wakeLocked()
		c.mu.Unlock()
	}
	c.mu.Lock()
	c.done = true
	c.wakeLocked()
	c.mu.Unlock()
}

// wakeLocked signals all blocked iterators by closing the current update
// channel and installing a fresh one. Callers must hold c.mu.
func (c *specCache) wakeLocked() {
	close(c.updated)
	c.updated = make(chan struct{})
}

// cachedSpecIterator replays the specCache from its own index, blocking until
// the next spec is available or the walk completes.
type cachedSpecIterator struct {
	cache *specCache
	idx   int
}

func (it *cachedSpecIterator) Next(ctx context.Context) (symbolresolve.Spec, bool) {
	c := it.cache
	for {
		c.mu.Lock()
		if it.idx < len(c.detected) {
			spec := c.detected[it.idx]
			it.idx++
			c.mu.Unlock()
			return spec, true
		}
		if c.done {
			c.mu.Unlock()
			return symbolresolve.Spec{}, false
		}
		updated := c.updated
		c.mu.Unlock()

		select {
		case <-updated:
		case <-ctx.Done():
			return symbolresolve.Spec{}, false
		}
	}
}

func (it *cachedSpecIterator) Err() error { return nil }

func (it *cachedSpecIterator) Close() error { return nil }

// Highlight tokenizes content as file's language and streams syntax
// highlight locations.
func (p Parser) Highlight(file workspaceapi.URI, content string) (
	iterator.Iterator[textapi.Location], error,
) {
	const qfile = "highlights.scm"

	path := file.Path()
	langID, lerr := languages.LanguageForFile(path)
	if lerr != nil {
		return nil, lerr
	}

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan textapi.Location)
	closeWaitCh := make(chan struct{})
	it := &chanIterator[textapi.Location]{
		ctx:         ctx,
		ch:          results,
		cancel:      cancel,
		closeWaitCh: closeWaitCh,
	}

	data := []byte(content)

	var buf cell.Buffer
	buf.Init()
	_, _ = buf.ReadFrom(bytes.NewReader(data))
	counts := cell.NewByteCounts(nil, buf.RawCells())

	go debug.CapturePanicReport(func() {
		defer close(closeWaitCh)
		defer close(results)
		parser, perr := newParser(ctx, langID, p.pkg, qfile, "")
		if perr != nil {
			perr = fmt.Errorf("new parser for language %q: %v", langID, perr)
			it.mu.Lock()
			defer it.mu.Unlock()
			it.err = perr
			return
		}
		defer parser.Close()

		tree := parser.parser.Parse(data, nil)
		if tree == nil {
			perr = fmt.Errorf("failed to parse data: empty tree")
			it.mu.Lock()
			defer it.mu.Unlock()
			it.err = perr
			return
		}
		defer tree.Close()

		locations := getHighlights(counts, data, tree, parser.query, nil)
		for _, location := range locations {
			select {
			case results <- location:
			case <-ctx.Done():
				return
			}
		}
	})
	return it, nil
}

// Query runs a tree-sitter query against a single file and streams one
// result per capture.
func (p Parser) Query(file workspaceapi.URI, query string, captureNames []string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return p.query(file, "", query, captureNames)
}

// Query2 runs a two-capture tree-sitter query against a single file and
// streams each match's captures as a pair in captureNames order, so
// consumers never re-associate a flattened capture stream.
func (p Parser) Query2(
	file workspaceapi.URI, query string, captureNames [2]string,
) (iterator.Iterator[[2]syntaxapi.Result], error) {
	path := file.Path()
	langID, lerr := languages.LanguageForFile(path)
	if lerr != nil {
		return nil, lerr
	}

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan [2]syntaxapi.Result)
	closeWaitCh := make(chan struct{})
	it := &chanIterator[[2]syntaxapi.Result]{
		ctx:         ctx,
		ch:          results,
		cancel:      cancel,
		closeWaitCh: closeWaitCh,
	}

	go debug.CapturePanicReport(func() {
		defer close(closeWaitCh)
		defer close(results)
		parser, perr := newParser(ctx, langID, p.pkg, "", query)
		if perr != nil {
			perr = fmt.Errorf("new parser for language %q: %v", langID, perr)
			it.mu.Lock()
			defer it.mu.Unlock()
			it.err = perr
			return
		}
		defer parser.Close()

		readErr := readFilePairs(ctx, parser, p.uri, p.w,
			path, results, captureNames)
		if readErr != nil {
			it.mu.Lock()
			defer it.mu.Unlock()
			it.err = readErr
		}
	})

	return it, nil
}

// Search2 runs a two-capture tree-sitter query across the workspace and
// streams each match's captures as a pair in captureNames order.
func (p Parser) Search2(
	query string, captureNames [2]string, langs ...string,
) (iterator.Iterator[[2]syntaxapi.Result], error) {
	it, err := p.SearchMulti([]symbolresolve.MultiQuery{
		{ID: 0, Query: query, Captures: captureNames[:]},
	}, langs...)
	if err != nil {
		return nil, err
	}
	// SearchMulti drops matches missing a declared capture, so every
	// delivered match holds exactly two results.
	return iterator.Map(it, func(r symbolresolve.MultiResult) [2]syntaxapi.Result {
		return [2]syntaxapi.Result{r.Match[0], r.Match[1]}
	}), nil
}

// QueryNode runs the built-in node-capture query against a single file.
func (p Parser) QueryNode(file workspaceapi.URI, nodeTypes syntaxapi.NodeCaptureName) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	names, err := nodeTypesToCaptureNames(nodeTypes)
	if err != nil {
		return nil, err
	}
	return p.query(file, LocalsFilename, "", names)
}

// SearchNode runs the built-in node-capture query across the workspace,
// optionally restricted to the given languages.
func (p Parser) SearchNode(
	nodeTypes syntaxapi.NodeCaptureName, languages ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	names, err := nodeTypesToCaptureNames(nodeTypes)
	if err != nil {
		return nil, err
	}
	return p.search(LocalsFilename, "", names, languages...)
}

// Search runs a single tree-sitter query across the workspace and
// streams one result per capture, optionally restricted to the given
// languages.
func (p Parser) Search(query string, captureNames []string, langs ...string) (
	iterator.Iterator[syntaxapi.Result], error,
) {
	return p.search("", query, captureNames, langs...)
}

// ResolveSymbol resolves a dotted symbol name to its declaration and reference
// locations. It detects which languages are present in the workspace and runs
// only the relevant specs, returning the first spec that yields matches.
func (p Parser) ResolveSymbol(
	ctx context.Context, name string, progress syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	if !strings.Contains(name, ".") {
		return nil, syntaxapi.ErrNoDot
	}

	if progress != nil {
		progress = newAggregateProgress(progress)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	results := make(chan syntaxapi.Match)
	closeWaitCh := make(chan struct{})
	it := &resolveSymbolIterator{
		ctx:         runCtx,
		ch:          results,
		cancel:      cancel,
		closeWaitCh: closeWaitCh,
	}

	go debug.CapturePanicReport(func() {
		defer close(closeWaitCh)
		defer close(results)

		matches, err := symbolresolve.Resolve(
			runCtx, p, p.qualifierContext(), p.specs.detect(runCtx), name, progress)
		if err != nil {
			it.setErr(err)
			return
		}
		for _, m := range matches {
			select {
			case results <- m:
			case <-runCtx.Done():
				it.setErr(runCtx.Err())
				return
			}
		}
	})

	return it, nil
}

// ListReferencedSymbols streams the package-qualified names of every symbol
// referenced or defined across the workspace. It detects which languages are
// present once via the cached spec detection and runs only the relevant specs.
// Names may repeat across specs; callers deduplicate as needed.
func (p Parser) ListReferencedSymbols(
	_ context.Context,
) (iterator.Iterator[string], error) {
	runCtx, cancel := context.WithCancel(context.Background())
	results := make(chan string)
	closeWaitCh := make(chan struct{})
	it := &listReferencedSymbolsIterator{
		ctx:         runCtx,
		ch:          results,
		cancel:      cancel,
		closeWaitCh: closeWaitCh,
	}

	go debug.CapturePanicReport(func() {
		defer close(closeWaitCh)
		defer close(results)

		if err := symbolresolve.ListReferences(
			runCtx, p, p.qualifierContext(), p.specs.detect(runCtx), results,
		); err != nil {
			it.setErr(err)
		}
	})

	return it, nil
}

// qualifierContext builds a fresh per-call qualifier context over the
// workspace filesystem. The existence memo must not outlive one
// resolution pass, since the tree can change between calls.
func (p Parser) qualifierContext() symbolresolve.QualifierContext {
	return symbolresolve.NewQualifierContext(p.w, p.uri)
}

func (p Parser) query(
	file workspaceapi.URI, queryFile, query string, captureNameFilters []string,
) (iterator.Iterator[syntaxapi.Result], error) {
	path := file.Path()
	langID, lerr := languages.LanguageForFile(path)
	if lerr != nil {
		return nil, lerr
	}

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan syntaxapi.Result)
	closeWaitCh := make(chan struct{})
	it := &chanIterator[syntaxapi.Result]{
		ctx:         ctx,
		ch:          results,
		cancel:      cancel,
		closeWaitCh: closeWaitCh,
	}

	go debug.CapturePanicReport(func() {
		defer close(closeWaitCh)
		defer close(results)
		parser, perr := newParser(ctx, langID, p.pkg, queryFile, query)
		if perr != nil {
			perr = fmt.Errorf("new parser for language %q: %v", langID, perr)
			it.mu.Lock()
			defer it.mu.Unlock()
			it.err = perr
			return
		}
		defer parser.Close()

		var scratch fileScratch
		readErr := readFileSymbols(ctx, parser, p.uri, p.w,
			path, &scratch, results, captureNameFilters)
		if readErr != nil {
			it.mu.Lock()
			defer it.mu.Unlock()
			it.err = readErr
		}
	})

	return it, nil
}

func (p Parser) search(
	queryFile, query string, captureNames []string, langs ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	ctx, cancel := context.WithCancel(context.Background())
	ctx = walkdir.WithContextFilter(ctx, p.filter.get())
	paths, err := walkdir.ListFiles(ctx, p.w, ".")
	if err != nil {
		cancel()
		return nil, err
	}

	files := make(chan string)
	results := make(chan syntaxapi.Result)
	closeWaitCh := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(defaultWorkers)
	errs := make([]error, defaultWorkers)
	validErrors := make([]map[string]*expectedError, defaultWorkers)
	for i := range defaultWorkers {
		validErrors[i] = make(map[string]*expectedError)
		var err = &errs[i]
		var expectedErrors = validErrors[i]
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			readSymbolsWorker(ctx, p.w, p.pkg, p.uri, queryFile, query, results, files, err,
				expectedErrors, captureNames, langs)
		})
	}

	it := &chanIterator[syntaxapi.Result]{
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
		missingLanguage := mergeValidErrorsMap(validErrors)
		if len(missingLanguage) != 0 {
			logrus.Debugf("Missing language parser for the following file extensions: %#v",
				missingLanguage)
		}
	})
	return it, nil
}

func mergeValidErrorsMap(m []map[string]*expectedError) (
	missingLanguage map[string]int,
) {
	missingLanguage = make(map[string]int)
	for _, mm := range m {
		for k, v := range mm {
			if v.missingLanguage != 0 {
				if _, ok := missingLanguage[k]; !ok {
					missingLanguage[k] = 0
				}
				missingLanguage[k] += v.missingLanguage
			}
		}
	}
	return
}

func readFileSymbols(
	ctx context.Context, parser *parser, uri workspaceapi.URI,
	w workspaceapi.FileSystem, filename string, scratch *fileScratch,
	results chan syntaxapi.Result, captureNameFilters []string,
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

	tree := parser.parser.Parse(content, nil)
	if tree == nil {
		return errors.New("failed to parse data")
	}
	defer tree.Close()

	cur := sitter.NewQueryCursor()
	defer cur.Close()

	root := tree.RootNode()
	captureNames := parser.query.CaptureNames()
	starts := lineStarts(scratch.starts, content)
	scratch.starts = starts
	fileURI := workspaceapi.Join(uri, filename)
	matches := cur.Matches(parser.query, root, content)
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		for _, cap := range m.Captures {
			if int(cap.Index) >= len(captureNames) ||
				(len(captureNameFilters) != 0 &&
					!slices.Contains(captureNameFilters, captureNames[cap.Index])) {
				continue
			}
			result := makeSymbolItem(
				content, starts, cap.Node.Range(), fileURI, captureNames[cap.Index],
			)
			select {
			case results <- result:
			case <-ctx.Done():
				return retErr
			}
		}
	}

	return retErr
}

func makeSymbolItem(
	content []byte, starts []int, rng sitter.Range,
	filename workspaceapi.URI, captureName string,
) syntaxapi.Result {
	return syntaxapi.Result{
		CaptureName: captureName,
		From:        pointToCoordinates(content, starts, rng.StartPoint),
		To:          pointToCoordinates(content, starts, rng.EndPoint),
		File:        filename,
		Text:        string(content[rng.StartByte:rng.EndByte]),
	}
}

// readFilePairs parses one file and streams each query match's two
// captures as a pair in captureNames order. Matches missing a declared
// capture are dropped: they cannot form the tuple the consumer expects.
func readFilePairs(
	ctx context.Context, parser *parser, uri workspaceapi.URI,
	w workspaceapi.FileSystem, filename string,
	results chan [2]syntaxapi.Result, captureNames [2]string,
) (retErr error) {
	file, err := w.OpenFile(filename, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open file: %v", err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("close file: %v", cerr))
		}
	}()

	content, err := io.ReadAll(file)
	if err != nil {
		return err
	}

	tree := parser.parser.Parse(content, nil)
	if tree == nil {
		return errors.New("failed to parse data")
	}
	defer tree.Close()

	cur := sitter.NewQueryCursor()
	defer cur.Close()

	root := tree.RootNode()
	names := parser.query.CaptureNames()
	starts := lineStarts(nil, content)
	fileURI := workspaceapi.Join(uri, filename)
	matches := cur.Matches(parser.query, root, content)
	for {
		m, ok := matches.Next()
		if !ok {
			break
		}
		match := groupMatchCaptures(
			content, starts, m, names, fileURI, captureNames[:])
		if match == nil {
			continue
		}
		select {
		case results <- [2]syntaxapi.Result{match[0], match[1]}:
		case <-ctx.Done():
			return retErr
		}
	}

	return retErr
}

func readSymbolsWorker(
	ctx context.Context, fs workspaceapi.FileSystem, pkg PkgManager,
	uri workspaceapi.URI, queryFile, query string, results chan syntaxapi.Result,
	files chan string,
	err *error, expectedErrors map[string]*expectedError,
	captureNameFilters, langs []string,
) {
	parsers := make(map[string]*parser)
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
			parser, ok := parsers[langID]
			if !ok {
				var perr error
				parser, perr = newParser(ctx, langID, pkg, queryFile, query)
				if perr != nil {
					if errors.Is(perr, errNotInstalled) {
						ext := filepath.Ext(path)
						if _, ok := expectedErrors[ext]; !ok {
							expectedErrors[ext] = &expectedError{}
						}
						expectedErrors[ext].missingLanguage++
						continue
					}
					perr = fmt.Errorf("new parser for language %q: %v", langID, perr)
					*err = errors.Join(*err, perr)
					continue
				}
				defer parser.Close()
				parsers[langID] = parser
			}

			readErr := readFileSymbols(ctx, parser, uri, fs,
				path, &scratch, results, captureNameFilters)
			if readErr != nil {
				*err = errors.Join(*err, readErr)
			}
		}
	}
}

// chanIterator adapts a producer channel to an iterator, joining
// producer errors with cancellation from either context.
type chanIterator[T any] struct {
	mu          sync.Mutex
	err         error
	ctx         context.Context
	ch          chan T
	cancel      func()
	closeWaitCh chan struct{}
}

func (l *chanIterator[T]) Next(ctx context.Context) (T, bool) {
	var zero T
	select {
	case <-ctx.Done():
		// the iterator is finished once the consumer's context is
		// canceled; stop the producer so it does not block forever on
		// the results channel holding tree-sitter natives.
		l.cancel()
		l.mu.Lock()
		defer l.mu.Unlock()
		l.err = errors.Join(l.err, ctx.Err())
		return zero, false
	case <-l.ctx.Done():
		l.mu.Lock()
		defer l.mu.Unlock()
		l.err = errors.Join(l.err, l.ctx.Err())
		return zero, false
	case v, ok := <-l.ch:
		return v, ok
	}
}

func (l *chanIterator[T]) Err() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.err == nil {
		return l.ctx.Err()
	}
	// avoid data races onto l.err which is an instance of
	// *multierr.Error by creating a new multierr.Error
	err := errors.Join(nil, l.err)
	if l.ctx.Err() == nil {
		return err
	}
	return errors.Join(err, l.ctx.Err())
}

func (l *chanIterator[T]) Close() error {
	l.cancel()
	<-l.closeWaitCh
	return nil
}

type resolveSymbolIterator struct {
	mu          sync.Mutex
	err         error
	ctx         context.Context
	ch          chan syntaxapi.Match
	cancel      func()
	closeWaitCh chan struct{}
}

// resolveStepsPerSpec is the fixed denominator each language spec reports its
// per-phase progress against (references, definitions, imports).
const resolveStepsPerSpec = 4

type aggregateProgress struct {
	mu             sync.Mutex
	next           syntaxapi.Progress
	base           int64
	lastRawStep    int64
	total          int64
	lastGlobalStep int64
}

func newAggregateProgress(next syntaxapi.Progress) *aggregateProgress {
	return &aggregateProgress{
		next:        next,
		lastRawStep: -1,
		total:       resolveStepsPerSpec,
	}
}

func (a *aggregateProgress) Report(msg string, found int, rawStep, _ int64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.lastRawStep >= 0 && rawStep <= a.lastRawStep {
		a.base += resolveStepsPerSpec
	}
	a.lastRawStep = rawStep

	a.total = max(a.total, a.base+resolveStepsPerSpec)
	globalStep := max(a.base+rawStep, a.lastGlobalStep)
	globalStep = min(globalStep, a.total)
	a.lastGlobalStep = globalStep

	a.next.Report(msg, found, globalStep, a.total)
}

func (it *resolveSymbolIterator) setErr(err error) {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.err = errors.Join(it.err, err)
}

func (it *resolveSymbolIterator) Next(ctx context.Context) (syntaxapi.Match, bool) {
	select {
	case <-ctx.Done():
		it.cancel()
		it.setErr(ctx.Err())
		return syntaxapi.Match{}, false
	case <-it.ctx.Done():
		it.setErr(it.ctx.Err())
		return syntaxapi.Match{}, false
	case m, ok := <-it.ch:
		return m, ok
	}
}

func (it *resolveSymbolIterator) Err() error {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.err
}

func (it *resolveSymbolIterator) Close() error {
	it.cancel()
	<-it.closeWaitCh
	return nil
}

type listReferencedSymbolsIterator struct {
	mu          sync.Mutex
	err         error
	ctx         context.Context
	ch          chan string
	cancel      func()
	closeWaitCh chan struct{}
}

func (it *listReferencedSymbolsIterator) setErr(err error) {
	it.mu.Lock()
	defer it.mu.Unlock()
	it.err = errors.Join(it.err, err)
}

func (it *listReferencedSymbolsIterator) Next(ctx context.Context) (string, bool) {
	select {
	case <-ctx.Done():
		it.cancel()
		it.setErr(ctx.Err())
		return "", false
	case <-it.ctx.Done():
		it.setErr(it.ctx.Err())
		return "", false
	case s, ok := <-it.ch:
		return s, ok
	}
}

func (it *listReferencedSymbolsIterator) Err() error {
	it.mu.Lock()
	defer it.mu.Unlock()
	return it.err
}

func (it *listReferencedSymbolsIterator) Close() error {
	it.cancel()
	<-it.closeWaitCh
	return nil
}

var (
	errNotInstalled = errors.New("parser not found: language package not installed")
)

type expectedError struct {
	missingLanguage int
}

type parser struct {
	closed bool
	parser *sitter.Parser
	lang   *sitter.Language
	query  *sitter.Query
	lib    uintptr
}

func newParser(
	ctx context.Context, langID string,
	pkg PkgManager, queryFile, query string,
) (ret *parser, err error) {
	lang, queryText, err := loadLanguage(ctx, langID, pkg, queryFile, query)
	if err != nil {
		return nil, err
	}
	q, err := compileQuery(lang.lang, queryText)
	if err != nil {
		lang.close()
		return nil, err
	}
	return &parser{
		parser: lang.parser,
		lang:   lang.lang,
		query:  q,
		lib:    lang.lib,
	}, nil
}

// loadedLanguage bundles a dlopen'd tree-sitter language with its parser.
// Multiple compiled queries can share one loadedLanguage so a multi-query
// search dlopen's and parses each file only once per language.
type loadedLanguage struct {
	lib    uintptr
	lang   *sitter.Language
	parser *sitter.Parser
}

func (l *loadedLanguage) close() {
	if l.parser != nil {
		l.parser.Close()
	}
	if l.lib != 0 {
		_ = sysDlclose(l.lib)
	}
}

// loadLanguage dlopens the tree-sitter shared object for langID and builds a
// parser bound to it. When queryFile is non-empty it also resolves that
// query file from the package's lib dir and returns its contents; otherwise
// it returns the inline query unchanged. The caller owns the returned
// loadedLanguage and must close it.
func loadLanguage(
	ctx context.Context, langID string, pkg PkgManager, queryFile, query string,
) (*loadedLanguage, string, error) {
	it, err := pkg.LibDir(ctx, langID)
	if err != nil {
		return nil, "", errNotInstalled
	}
	files, err := iterator.ToSlice(ctx, it)
	_ = it.Close()
	if err != nil {
		return nil, "", fmt.Errorf("list files: %w", err)
	}
	var langfile, queryFileAbsPath string
	for _, path := range files {
		switch filepath.Base(path) {
		case ParserFilename:
			langfile = path
		case queryFile:
			queryFileAbsPath = path
		}
	}
	if langfile == "" || (queryFile != "" && queryFileAbsPath == "") {
		return nil, "", errNotInstalled
	}
	if cacheableIt, ok := it.(cacheablePkgFilesIterator); ok {
		cacheableIt.cache(files)
	}

	if queryFileAbsPath != "" && queryFile != "" {
		f, ferr := os.OpenFile(queryFileAbsPath, os.O_RDONLY, 0666)
		if ferr != nil {
			return nil, "", fmt.Errorf("open lib query file %s: %v", queryFile, ferr)
		}
		data, rerr := io.ReadAll(f)
		_ = f.Close()
		if rerr != nil {
			return nil, "", fmt.Errorf("read lib query file %s: %v", queryFile, rerr)
		}
		query = string(data)
	}

	lib, err := sysDlopen(langfile, dlNow|dlGlobal)
	if err != nil {
		return nil, "", fmt.Errorf("dlopen %q: %w", langfile, err)
	}

	parserID := fmt.Sprintf("tree_sitter_%s", langID)
	sym, err := sysDlsym(lib, parserID)
	if err != nil {
		_ = sysDlclose(lib)
		return nil, "", fmt.Errorf("load symbol %q: %w", parserID, err)
	}
	var langFn func() uintptr
	purego.RegisterFunc(&langFn, sym)

	language := sitter.NewLanguage(unsafe.Pointer(langFn()))
	sitterParser := sitter.NewParser()
	if err = sitterParser.SetLanguage(language); err != nil {
		_ = sysDlclose(lib)
		sitterParser.Close()
		return nil, "", fmt.Errorf("set parser language: %v", err)
	}
	return &loadedLanguage{lib: lib, lang: language, parser: sitterParser}, query, nil
}

// compileQuery compiles a tree-sitter query against lang.
func compileQuery(lang *sitter.Language, query string) (*sitter.Query, error) {
	q, qerr := sitter.NewQuery(lang, query)
	if qerr != nil {
		return nil, fmt.Errorf("invalid query: %w", qerr)
	}
	return q, nil
}

func (t *parser) Close() (ret error) {
	if t.closed {
		return nil
	}
	t.closed = true
	t.parser.Close()
	t.query.Close()
	ret = sysDlclose(t.lib)
	return
}

func nodeTypesToCaptureNames(nodeTypes syntaxapi.NodeCaptureName) (ret []string, err error) {
	if nodeTypes&syntaxapi.NodeCaptureScope != 0 {
		ret = append(ret, "local.scope")
	}
	if nodeTypes&syntaxapi.NodeCaptureDefinitionType != 0 {
		ret = append(ret, "local.definition.type")
	}
	if nodeTypes&syntaxapi.NodeCaptureDefinitionNamespace != 0 {
		ret = append(ret, "local.definition.namespace")
	}
	if nodeTypes&syntaxapi.NodeCaptureReference != 0 {
		ret = append(ret, "local.reference")
	}
	if nodeTypes&syntaxapi.NodeCaptureDefinitionFunc != 0 {
		ret = append(ret, "local.definition.function")
	}
	if nodeTypes&syntaxapi.NodeCaptureDefinitionMethod != 0 {
		ret = append(ret, "local.definition.method")
	}
	if nodeTypes&syntaxapi.NodeCaptureDefinitionVar != 0 {
		ret = append(ret, "local.definition.var")
	}
	if len(ret) == 0 {
		err = errors.New("invalid node capture name")
	}
	return
}
