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

// Package symboldb keeps the persistent symbol index of a workspace,
// built from each language's Tree-sitter grammar: the qualified names
// lookups and completion resolve, and every definition the search
// commands list by file.
package symboldb

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	bluebolt "github.com/unstablebuild/blue/document/bolt"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/retry"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/languages"
	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/workspace/searchfilter"
)

// retryStrategy bounds the compare-and-swap retries of upsertSymbol.
// All of this process's writes flow through one goroutine at a time,
// so retries only fire when another process indexes the same
// workspace through the shared store.
var retryStrategy = retry.DefaultStrategy

const (
	// settleDelay lets a burst of disk events gather before the indexer
	// looks at them: a checkout touches hundreds of files, and seen
	// together they cost one round of writes rather than one each.
	settleDelay = 100 * time.Millisecond
	// maxName is how many bytes of a definition's name are kept.
	maxName = 256
)

var (
	errClosed   = errors.New("symboldb: index closed")
	errDisabled = errors.New("symboldb: the index is disabled")
	// ErrRemoved is returned by Reindex once Remove deleted the index.
	ErrRemoved = errors.New("symboldb: index removed")
)

// EditorEvents returns the editor event types the Parser must be
// subscribed to in order to keep the index fresh: in-editor saves and
// the out-of-band disk events. Buffer edits are deliberately excluded
// because they do not touch disk.
func EditorEvents() []textapi.EventType {
	return []textapi.EventType{
		textapi.EventTypeFlush,
		textapi.EventTypeChange,
		textapi.EventTypeCreate,
		textapi.EventTypeRemove,
		textapi.EventTypeRename,
	}
}

// Kind is the kind of a definition.
type Kind uint8

// Kinds of definitions. The values are persisted.
const (
	KindFunction Kind = iota + 1
	KindMethod
	KindType
	KindVar
)

func (k Kind) String() string {
	switch k {
	case KindFunction:
		return "function"
	case KindMethod:
		return "method"
	case KindType:
		return "type"
	case KindVar:
		return "variable"
	}
	return "unknown"
}

var captureKinds = map[string]Kind{
	"local.definition.function": KindFunction,
	"local.definition.method":   KindMethod,
	"local.definition.type":     KindType,
	"local.definition.var":      KindVar,
}

// Indexer parses the files the index reads; treesitter.Parser is one.
type Indexer interface {
	// NewQuerySession returns a per-goroutine batched file querier.
	NewQuerySession() symbolresolve.QuerySession
	// GrammarFingerprint identifies the grammar installed for langID:
	// equal values mean extraction reads identical grammar files. It
	// fails when the language is not installed.
	GrammarFingerprint(ctx context.Context, langID string) (string, error)
}

// Config is what a Parser is opened with.
type Config struct {
	Root workspaceapi.URI
	FS   workspaceapi.FileSystem
	// Backing serves the queries the index does not answer, and symbol
	// lookups until the index was built once.
	Backing syntaxapi.Parser
	// Indexer reads the files of the workspace. It should read only the
	// grammars installed: a scan must not offer to install the package of
	// every language it meets.
	Indexer Indexer
	// DB is where the index persists; Close leaves it open. Nil keeps no
	// index: lookups go to Backing and every definitions search parses
	// the files it covers.
	DB storageapi.Service
	// ExcludedFolders are workspace-relative folders the index leaves
	// out, as every workspace search does.
	ExcludedFolders []string
	// Progress returns what reports a scan the index starts by itself.
	// Nil reports nothing.
	Progress func() Progress
}

// Status describes the index.
type Status struct {
	// Loaded is set once the persisted index was read.
	Loaded bool
	// Scanned is set once a scan of the workspace completed since the
	// index was opened or emptied by Reindex. Until then searches may
	// miss what changed while it was closed, or what Reindex has yet to
	// read again. It is always set without Config.DB or once Removed,
	// when searches parse the files they cover.
	Scanned bool
	// Files and Records are how many files and definitions the index
	// holds, and Pending how many files it has yet to look at.
	Files, Records, Pending int
	// Skipped counts the files without definitions, by reason.
	Skipped map[string]int
	// Grammars are the fingerprints of the grammars used, by language;
	// "" when a language has none installed.
	Grammars map[string]string
	// Removed is set once Remove deleted the index.
	Removed bool
	// Err is why the index could not be read, or emptied by Reindex, until
	// a scan completes.
	Err string
}

// DefinitionQuery is a search of the definitions of the workspace.
type DefinitionQuery struct {
	// Kinds are those of the definitions listed; none lists them all.
	Kinds []Kind
	// Langs are the languages of the files searched; none searches all.
	Langs []string
	// Roots are the workspace-relative folders or files searched; none
	// searches the whole workspace.
	Roots []string
	// Limit is how many definitions are listed at most; 0 lists them all.
	Limit int
}

// Definition is one definition of the workspace.
type Definition struct {
	// Path is workspace-relative, with forward slashes.
	Path string
	// Line and Col are from 1.
	Line, Col int
	Kind      Kind
	Lang      string
	Name      string
}

// Parser is an index-backed syntaxapi.Parser that also lists the
// definitions of the workspace. It reads the index from Config.DB when
// opened, then brings it up to date with the workspace in the
// background, and keeps it so as it is told of changes: it implements
// text.EventHandler to learn of them, and io.Closer to stop.
type Parser struct {
	cfg Config

	// files, symbols and names are separate partitions because List is
	// the storage API's only enumeration primitive and it decodes a
	// whole partition: loading the catalog lists file records without
	// deserializing the (much larger) symbols table, and
	// ListReferencedSymbols streams the tiny name markers without
	// decoding any symbol locations. meta is the caller's db itself,
	// holding only the scan marker at its root. All are nil without
	// Config.DB.
	files   storageapi.Service
	symbols storageapi.Service
	names   storageapi.Service
	meta    storageapi.Service
	// batch is the symbols partition's batching interface when the
	// storage chain provides one; nil once it turns out not to.
	batch storageapi.BatchWriter
	cat   *catalog

	filterOnce sync.Once
	flt        *searchfilter.Filter

	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex
	// pending are the workspace-relative paths of the events the worker
	// has yet to look at.
	pending map[string]struct{}
	scans   []scanJob
	loaded  bool
	// scanned is set once this session's scan completed.
	scanned bool
	// scannedEver is set once any scan has ever completed over the
	// database (persisted marker); it makes index misses authoritative.
	scannedEver bool
	busy        bool
	removed     bool
	closed      bool
	// left is how many files the current scan or update has yet to read.
	left     int
	err      string
	grammars map[string]*grammarMemo
	// updated is closed (and replaced) on every state change so Wait can
	// block without polling.
	updated chan struct{}
	// wake nudges the worker when there is work.
	wake chan struct{}
	// done is closed when the worker goroutine exits.
	done chan struct{}
}

var _ syntaxapi.Parser = (*Parser)(nil)

type grammarMemo struct {
	once sync.Once
	// sum and known are guarded by Parser.mu.
	sum   string
	known bool
}

// scanJob is a scan of the whole workspace. done, when set, receives how
// it ended.
type scanJob struct {
	ctx   context.Context
	reset bool
	prog  Progress
	done  chan error
}

// work is a file to read.
type work struct {
	rel, lang, grammar string
	mtime              int64
}

// New opens the index of cfg.Root. Indexing starts immediately in the
// background; queries are served from whatever state the database is in.
func New(cfg Config) (*Parser, error) {
	ctx, cancel := context.WithCancel(context.Background())
	p := &Parser{
		cfg:      cfg,
		cat:      newCatalog(),
		ctx:      ctx,
		cancel:   cancel,
		pending:  make(map[string]struct{}),
		grammars: make(map[string]*grammarMemo),
		updated:  make(chan struct{}),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	if cfg.DB != nil {
		if err := p.open(cfg.DB); err != nil {
			cancel()
			return nil, err
		}
	}
	go debug.CapturePanicReport(p.run)
	return p, nil
}

func (p *Parser) open(db storageapi.Service) error {
	var err error
	if p.files, err = db.Partition(filesPartition); err != nil {
		return err
	}
	if p.symbols, err = db.Partition(symbolsPartition); err != nil {
		return err
	}
	if p.names, err = db.Partition(namesPartition); err != nil {
		return err
	}
	p.meta = db
	if writer, ok := p.symbols.(storageapi.BatchWriter); ok {
		p.batch = writer
	}
	// A marker written by another schema version must not enable
	// index-served listing: the derived tables it vouches for may not
	// exist under that older layout.
	var m metaDoc
	if err := db.Get(p.ctx, metaScanID, &m); err == nil && m.Version == schemaVersion {
		p.scannedEver = m.Complete
	}
	p.scans = []scanJob{{ctx: p.ctx, prog: p.progress()}}
	return nil
}

// CleanupWorkspaceHook returns a hook that drops the index of a
// workspace, given the storage symboldb databases are partitioned out
// of (the same service Config.DB is derived from). It is a
// package-level function because the workspaces it reclaims have no
// live Parser.
func CleanupWorkspaceHook(
	storage storageapi.Service,
) func(ctx context.Context, root workspaceapi.URI) error {
	return func(ctx context.Context, root workspaceapi.URI) error {
		dbs, err := storage.Partition(PartitionName)
		if err != nil {
			return fmt.Errorf("symboldb: partition storage: %w", err)
		}
		defer func() { _ = dbs.Close() }()

		db, err := dbs.Partition(root.String())
		if err != nil {
			return fmt.Errorf("symboldb: partition %q: %w", root.String(), err)
		}
		defer func() { _ = db.Close() }()
		return dropIndex(ctx, db)
	}
}

// dropIndex drops the index db holds.
func dropIndex(ctx context.Context, db storageapi.Service) error {
	// Drop is not recursive, so the derived tables go first.
	for _, name := range []string{
		filesPartition, symbolsPartition, namesPartition,
	} {
		part, err := db.Partition(name)
		if err != nil {
			return fmt.Errorf("symboldb: partition %q: %w", name, err)
		}
		err = drop(ctx, part)
		if cerr := part.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return fmt.Errorf("symboldb: drop %q: %w", name, err)
		}
	}
	if err := drop(ctx, db); err != nil {
		return fmt.Errorf("symboldb: drop the index: %w", err)
	}
	return nil
}

func drop(ctx context.Context, svc storageapi.Service) error {
	droppable, ok := svc.(storageapi.DroppableService)
	if !ok {
		return errors.New("storage service does not support dropping")
	}
	return droppable.Drop(ctx)
}

func (p *Parser) progress() Progress {
	if p.cfg.Progress == nil {
		return nopProgress{}
	}
	return p.cfg.Progress()
}

func (p *Parser) filter() *searchfilter.Filter {
	p.filterOnce.Do(func() { p.flt = searchfilter.New(p.cfg.FS, p.cfg.ExcludedFolders) })
	return p.flt
}

// Handle implements text.EventHandler. It never blocks: an event only
// marks its path for the indexer to look at once events settle.
func (p *Parser) Handle(_ context.Context, ev textapi.Event) bool {
	switch ev.Type {
	case textapi.EventTypeFlush, textapi.EventTypeChange,
		textapi.EventTypeCreate, textapi.EventTypeRemove,
		textapi.EventTypeRename:
	default:
		return false
	}
	if p.cfg.DB == nil || !workspaceapi.HasPrefix(ev.URI, p.cfg.Root) {
		return false
	}
	rel := path.Clean(filepath.ToSlash(workspaceapi.RelPath(p.cfg.Root, ev.URI)))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.removed {
		return false
	}
	p.pending[rel] = struct{}{}
	p.wakeLocked()
	p.poke()
	return false
}

// Rescan has the index scan the workspace again, in the background,
// asking for the grammar of every language anew: the files of a
// language whose grammar was installed or changed are read again.
func (p *Parser) Rescan() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.grammars = make(map[string]*grammarMemo)
	if p.cfg.DB == nil || p.removed {
		return
	}
	p.scans = append(p.scans, scanJob{ctx: p.ctx, prog: p.progress()})
	p.wakeLocked()
	p.poke()
}

// Reindex empties the index and builds it again, reporting to prog, and
// returns once it is built. Lookups go to Config.Backing until it is.
// Canceling ctx stops the build and returns: the index is then built
// again in the background, as when opened. It returns ErrRemoved when
// Remove deleted the index, before or while it waited.
func (p *Parser) Reindex(ctx context.Context, prog Progress) error {
	done := make(chan error, 1)
	p.mu.Lock()
	switch {
	case p.closed:
		p.mu.Unlock()
		return errClosed
	case p.removed:
		p.mu.Unlock()
		return ErrRemoved
	case p.cfg.DB == nil:
		p.mu.Unlock()
		return errDisabled
	}
	p.grammars = make(map[string]*grammarMemo)
	p.scans = append(p.scans, scanJob{ctx: ctx, reset: true, prog: prog, done: done})
	p.wakeLocked()
	p.poke()
	p.mu.Unlock()
	select {
	case err := <-done:
		if err == nil || p.ctx.Err() == nil {
			return err
		}
	case <-p.done:
	}
	// The index was closed or removed before the build completed.
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.removed && !p.closed {
		return ErrRemoved
	}
	return errClosed
}

// Remove deletes the index Config.DB holds and stops keeping it: from
// then on lookups go to Config.Backing and every definitions search
// parses the files it covers, as without Config.DB. A scan under way
// stops first.
func (p *Parser) Remove(ctx context.Context) error {
	p.mu.Lock()
	switch {
	case p.closed:
		p.mu.Unlock()
		return errClosed
	case p.removed:
		p.mu.Unlock()
		return ErrRemoved
	case p.cfg.DB == nil:
		p.mu.Unlock()
		return errDisabled
	}
	p.removed = true
	p.scannedEver = false
	p.mu.Unlock()
	p.cancel()
	<-p.done
	p.cat.clear()
	p.mu.Lock()
	p.loaded = true
	p.scanned = true
	p.busy = false
	clear(p.pending)
	p.scans = nil
	p.left = 0
	p.err = ""
	p.wakeLocked()
	p.mu.Unlock()
	return dropIndex(ctx, p.meta)
}

// Status describes the index now.
func (p *Parser) Status() Status {
	files, records, skipped := p.cat.counts()
	p.mu.Lock()
	defer p.mu.Unlock()
	grammars := make(map[string]string, len(p.grammars))
	for lang, m := range p.grammars {
		if m.known {
			grammars[lang] = m.sum
		}
	}
	return Status{
		Loaded:   p.loaded,
		Scanned:  p.scanned,
		Files:    files,
		Records:  records,
		Pending:  len(p.pending) + p.left,
		Skipped:  skipped,
		Grammars: grammars,
		Removed:  p.removed,
		Err:      p.err,
	}
}

// Wait blocks until the index was read, the scans asked for are done and
// the events it was given were looked at. It is intended for tests and
// diagnostics.
func (p *Parser) Wait(ctx context.Context) error {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return errClosed
		}
		if p.loaded && !p.busy && len(p.scans) == 0 && len(p.pending) == 0 {
			p.mu.Unlock()
			return nil
		}
		updated := p.updated
		p.mu.Unlock()
		select {
		case <-updated:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close stops the indexer goroutine. It is safe to call more than once.
// Config.DB is left open.
func (p *Parser) Close() error {
	p.mu.Lock()
	p.closed = true
	p.wakeLocked()
	p.mu.Unlock()
	p.cancel()
	<-p.done
	return nil
}

// wakeLocked wakes every Wait call blocked on a state change.
// Callers must hold p.mu.
func (p *Parser) wakeLocked() {
	close(p.updated)
	p.updated = make(chan struct{})
}

func (p *Parser) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Parser) fail(op string, err error) {
	log.Errorf("symboldb: %s the index of %s: %v", op, p.cfg.Root, err)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err.Error()
}

func (p *Parser) hasScannedEver() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scannedEver
}

// indexed reports whether lookups may read the index.
func (p *Parser) indexed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cfg.DB != nil && !p.removed
}

// Search delegates to the backing parser.
func (p *Parser) Search(
	query string, captureNames []string, languages ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return p.cfg.Backing.Search(query, captureNames, languages...)
}

// SearchNode delegates to the backing parser.
func (p *Parser) SearchNode(
	nodeTypes syntaxapi.NodeCaptureName, languages ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return p.cfg.Backing.SearchNode(nodeTypes, languages...)
}

// Query delegates to the backing parser.
func (p *Parser) Query(
	file workspaceapi.URI, query string, captureNames []string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return p.cfg.Backing.Query(file, query, captureNames)
}

// QueryNode delegates to the backing parser.
func (p *Parser) QueryNode(
	file workspaceapi.URI, nodeTypes syntaxapi.NodeCaptureName,
) (iterator.Iterator[syntaxapi.Result], error) {
	return p.cfg.Backing.QueryNode(file, nodeTypes)
}

// Highlight delegates to the backing parser.
func (p *Parser) Highlight(
	uri workspaceapi.URI, content string,
) (iterator.Iterator[textapi.Location], error) {
	return p.cfg.Backing.Highlight(uri, content)
}

// ResolveSymbol serves a symbol lookup from the index, passing through
// to the backing parser only until the first full scan completes. Once
// the index is authoritative a miss returns no matches instead of
// falling back, so true negatives resolve without invoking the backing
// parser. Indexed results may be stale while a rebuild is in flight.
// Without an index every lookup passes through.
func (p *Parser) ResolveSymbol(
	ctx context.Context, name string, progress syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	if !strings.Contains(name, ".") {
		return nil, syntaxapi.ErrNoDot
	}
	if p.indexed() {
		matches, indexable, found := p.resolveFromIndex(ctx, name, progress)
		if found {
			return iterator.FromSlice(matches), nil
		}
		// A miss is authoritative only once a full scan has populated
		// the index; until then the symbol may simply not be indexed yet.
		if indexable && p.hasScannedEver() {
			return iterator.Empty[syntaxapi.Match](), nil
		}
	}
	return p.cfg.Backing.ResolveSymbol(ctx, name, progress)
}
func (p *Parser) resolveFromIndex(
	ctx context.Context, name string, progress syntaxapi.Progress,
) (matches []syntaxapi.Match, indexable, found bool) {
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return nil, false, false
	}
	var doc symbolDoc
	if err := p.symbols.Get(ctx, name, &doc); err != nil || len(doc.Locs) == 0 {
		return nil, true, false
	}
	// Deterministic file order: locs accumulate in indexing order,
	// which varies across scans.
	sort.SliceStable(doc.Locs, func(i, j int) bool {
		return doc.Locs[i].URI < doc.Locs[j].URI
	})
	for _, spec := range symbolresolve.AllSpecs() {
		specMatches := matchesForSpec(spec, doc.Locs, name)
		if len(specMatches) == 0 {
			continue
		}
		if progress != nil {
			progress.Report("Resolved from index", len(specMatches), 1, 1)
		}
		if len(specMatches) > 1 && len(parts) == 2 && spec.HasImports() {
			specMatches = p.dedupByImport(ctx, specMatches, parts[0])
		}
		if len(specMatches) > 1 {
			disambiguate(spec, specMatches, name)
		}
		return specMatches, true, true
	}
	return nil, true, false
}

// matchesForSpec picks the spec's matches for one symbol doc,
// preferring reference locations, then definitions, then method
// definitions — mirroring the backing resolver's phase order, where
// the symbol interpretation of a name precedes the method one.
func matchesForSpec(
	spec *symbolresolve.Spec, locs []symbolLoc, name string,
) []syntaxapi.Match {
	pick := func(kind int) []syntaxapi.Match {
		var matches []syntaxapi.Match
		seen := make(map[string]bool)
		for _, l := range locs {
			if l.Kind != kind || seen[l.URI] ||
				symbolresolve.SpecForFile(l.URI) != spec {
				continue
			}
			seen[l.URI] = true
			matches = append(matches, syntaxapi.Match{
				URI:     l.URI,
				Pos:     term.Coordinates{X: l.X, Y: l.Y},
				Display: name,
			})
		}
		return matches
	}
	for _, kind := range []int{kindRef, kindDef, kindMethodDef} {
		if matches := pick(kind); len(matches) > 0 {
			return matches
		}
	}
	return nil
}

func (p *Parser) dedupByImport(
	ctx context.Context, matches []syntaxapi.Match, pkg string,
) []syntaxapi.Match {
	seen := make(map[string]bool)
	result := matches[:0]
	for _, m := range matches {
		key := m.URI
		var fd fileDoc
		if err := p.files.Get(ctx, m.URI, &fd); err == nil {
			if importPath, ok := fd.Imports[pkg]; ok {
				key = importPath
				m.ImportPath = importPath
			}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, m)
	}
	return result
}

func disambiguate(spec *symbolresolve.Spec, matches []syntaxapi.Match, name string) {
	for i, m := range matches {
		prefix := m.ImportPath
		if prefix == "" {
			prefix = m.URI
			if spec.DisplayPathFromURI != nil {
				prefix = spec.DisplayPathFromURI(m.URI)
			}
		}
		matches[i].Display = prefix + ": " + name
	}
}

// ListReferencedSymbols streams the indexed symbol names once a full
// scan has ever completed, passing through to the backing parser until
// then. The index and the backing parser offer the same names,
// including method definitions, so the streamed set is stable across
// the pre-scan and post-scan paths.
func (p *Parser) ListReferencedSymbols(
	_ context.Context,
) (iterator.Iterator[string], error) {
	var src iterator.Iterator[string]
	return iterator.FromFunc(func(ctx context.Context) (string, bool, error) {
		if src == nil {
			it, err := p.openNameSource(ctx)
			if err != nil {
				return "", false, err
			}
			src = it
		}
		v, ok := src.Next(ctx)
		if !ok {
			return "", false, src.Err()
		}
		return v, true, nil
	}, func() error {
		if src == nil {
			return nil
		}
		return src.Close()
	}), nil
}

func (p *Parser) openNameSource(
	ctx context.Context,
) (iterator.Iterator[string], error) {
	if !p.hasScannedEver() {
		return p.cfg.Backing.ListReferencedSymbols(ctx)
	}
	it, err := p.names.List(ctx, nil)
	if err != nil {
		return p.cfg.Backing.ListReferencedSymbols(ctx)
	}
	return iterator.FromFunc(func(context.Context) (string, bool, error) {
		if !it.HasNext() {
			return "", false, nil
		}
		var doc nameDoc
		if err := it.NextTo(&doc); err != nil {
			return "", false, err
		}
		return doc.Name, true, nil
	}, it.Close), nil
}

func (p *Parser) run() {
	defer close(p.done)
	if p.cfg.DB != nil {
		p.load()
	}
	p.mu.Lock()
	p.loaded = true
	p.scanned = p.cfg.DB == nil
	p.wakeLocked()
	p.mu.Unlock()
	if p.cfg.DB == nil {
		return
	}
	// Updates share one session, which keeps the languages it loaded; a
	// scan replaces it, as a scan follows a grammar installed or changed.
	var q symbolresolve.QuerySession
	defer func() {
		if q != nil {
			_ = q.Close()
		}
	}()
	for {
		job, paths, ok := p.next()
		if !ok {
			return
		}
		if job != nil {
			if q != nil {
				_ = q.Close()
				q = nil
			}
			p.scan(*job)
		} else {
			if q == nil {
				q = p.cfg.Indexer.NewQuerySession()
			}
			p.update(q, paths)
		}
		p.mu.Lock()
		p.busy = false
		p.wakeLocked()
		p.mu.Unlock()
	}
}

// load reads the persisted file records into the catalog.
func (p *Parser) load() {
	it, err := p.files.List(p.ctx, nil)
	if err != nil {
		if p.ctx.Err() == nil {
			p.fail("read", err)
		}
		return
	}
	defer func() { _ = it.Close() }()
	for it.HasNext() {
		var doc fileDoc
		if err := it.NextTo(&doc); err != nil {
			if p.ctx.Err() == nil {
				p.fail("read", err)
			}
			return
		}
		if rel := p.docPath(doc); rel != "" {
			p.cat.put(rel, entryOf(doc))
		}
	}
}

// docPath returns the workspace-relative slash path of doc's file.
func (p *Parser) docPath(doc fileDoc) string {
	if doc.Path != "" {
		return filepath.ToSlash(doc.Path)
	}
	uri, err := workspaceapi.ParseURI(doc.URI)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(workspaceapi.RelPath(p.cfg.Root, uri))
}

// next waits for what the worker does next: a scan, or the paths of the
// events once they settle. It reports false once the index is closed.
func (p *Parser) next() (*scanJob, []string, bool) {
	for {
		p.mu.Lock()
		if p.closed || p.ctx.Err() != nil {
			p.mu.Unlock()
			return nil, nil, false
		}
		if len(p.scans) > 0 {
			job := p.scans[0]
			p.scans = p.scans[1:]
			p.busy = true
			p.mu.Unlock()
			return &job, nil, true
		}
		hasPending := len(p.pending) > 0
		p.mu.Unlock()
		if hasPending {
			select {
			case <-time.After(settleDelay):
			case <-p.ctx.Done():
				return nil, nil, false
			}
			p.mu.Lock()
			if len(p.scans) > 0 {
				p.mu.Unlock()
				continue
			}
			paths := slices.Sorted(maps.Keys(p.pending))
			clear(p.pending)
			p.busy = true
			p.mu.Unlock()
			return nil, paths, true
		}
		select {
		case <-p.wake:
		case <-p.ctx.Done():
			return nil, nil, false
		}
	}
}

// scan brings the index up to date with the whole workspace: it reads
// the files that changed, or whose grammar did, and forgets those gone.
func (p *Parser) scan(job scanJob) {
	ctx, cancel := context.WithCancel(p.ctx)
	defer cancel()
	if job.ctx != nil {
		defer context.AfterFunc(job.ctx, cancel)()
	}
	err := p.scanFiles(ctx, job)
	switch {
	case err != nil && ctx.Err() == nil:
		job.prog.Stop(err)
	case err != nil:
		err = ctx.Err()
		job.prog.Stop(err)
	}
	if job.reset && err != nil && p.ctx.Err() == nil {
		// The index was emptied: build it again, as when opened. The
		// scan is queued before Reindex returns so Wait covers it.
		p.mu.Lock()
		p.scans = append(p.scans, scanJob{ctx: p.ctx, prog: p.progress()})
		p.mu.Unlock()
	}
	if job.done != nil {
		job.done <- err
	}
}

func (p *Parser) scanFiles(ctx context.Context, job scanJob) error {
	if job.reset {
		if err := p.reset(ctx); err != nil {
			if ctx.Err() == nil {
				p.fail("reset", err)
			}
			return err
		}
	}
	files, walkErr := p.filter().Walk(ctx, p.cfg.FS, []string{"."})
	if err := ctx.Err(); err != nil {
		return err
	}
	var gone []string
	if walkErr != nil {
		// An incomplete walk must not forget the files it did not
		// reach, nor claim the scan completed.
		log.Errorf("symboldb: scan %q: %v", p.cfg.Root.String(), walkErr)
	} else {
		onDisk := make(map[string]struct{}, len(files))
		for _, rel := range files {
			onDisk[rel] = struct{}{}
		}
		for _, e := range p.cat.snapshot([]string{"."}) {
			if _, ok := onDisk[e.path]; !ok {
				gone = append(gone, e.path)
			}
		}
	}
	todo := p.staleFiles(ctx, files)
	if len(todo) > 0 {
		job.prog.Start(len(todo))
	}
	read := p.index(ctx, false, nil, todo, gone, job.prog)
	if err := ctx.Err(); err != nil {
		return err
	}
	if walkErr == nil {
		marker := metaDoc{Complete: true, Version: schemaVersion}
		// Written with the plain context: this synced commit is the
		// durability barrier for every relaxed write of the scan.
		if err := p.meta.Set(ctx, metaScanID, marker); err != nil {
			log.Errorf("symboldb: persist scan marker: %v", err)
		}
	}
	p.mu.Lock()
	p.scanned = true
	if walkErr == nil {
		p.scannedEver = true
	}
	p.err = ""
	p.mu.Unlock()
	job.prog.Done(read)
	return nil
}

// reset empties the index for a rebuild.
func (p *Parser) reset(ctx context.Context) error {
	p.cat.clear()
	p.mu.Lock()
	p.scanned, p.scannedEver = false, false
	p.mu.Unlock()
	if err := p.meta.Delete(ctx, metaScanID); err != nil &&
		!errors.Is(err, storageapi.ErrNotFound) {
		return err
	}
	for _, part := range []storageapi.Service{p.files, p.symbols, p.names} {
		if err := drop(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

// staleFiles returns the work of reading the files the index must read
// again, in the order of files. Stat calls are what a warm scan spends
// its time on, so they run in parallel.
func (p *Parser) staleFiles(ctx context.Context, files []string) []work {
	works := make([]work, len(files))
	stale := make([]bool, len(files))
	var next atomic.Int64
	var wg sync.WaitGroup
	workers := min(max(runtime.NumCPU()/2, 1), len(files))
	wg.Add(workers)
	for range workers {
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			for ctx.Err() == nil {
				i := int(next.Add(1) - 1)
				if i >= len(files) {
					return
				}
				works[i], stale[i] = p.stale(files[i])
			}
		})
	}
	wg.Wait()
	var todo []work
	for i, w := range works {
		if stale[i] {
			todo = append(todo, w)
		}
	}
	return todo
}

// stale returns the work of reading rel, and whether the index must:
// when it does not hold rel but could, holds it from before rel, its
// grammar or the schema changed, or holds it but no longer can. A work
// without a grammar has the file forgotten.
func (p *Parser) stale(rel string) (work, bool) {
	old, known := p.cat.get(rel)
	w := work{rel: rel, lang: langOf(rel)}
	w.grammar = p.fileGrammar(rel, w.lang)
	if w.grammar == "" {
		return w, known
	}
	info, err := p.cfg.FS.Stat(rel)
	if err != nil {
		w.grammar = ""
		return w, known
	}
	w.mtime = info.ModTime().UnixNano()
	return w, !known || old.mtime != w.mtime || old.version != schemaVersion ||
		old.grammar != w.grammar || old.lang != w.lang
}

// fileGrammar returns the fingerprint of every grammar reading rel, of
// language lang, depends on: lang's, and for a host spec those of the
// languages it injects, whose upgrade or installation changes what its
// files contribute. It is "" when lang has no grammar installed.
func (p *Parser) fileGrammar(rel, lang string) string {
	host := p.grammar(lang)
	spec := symbolresolve.SpecForFile(rel)
	if host == "" || spec == nil || len(spec.Injected) == 0 {
		return host
	}
	grammars := make([]string, 0, 1+len(spec.Injected))
	grammars = append(grammars, host)
	for _, sub := range spec.Injected {
		grammars = append(grammars, p.grammar(sub.LangID))
	}
	return strings.Join(grammars, "+")
}

// update looks at the paths of the events since it last did: it reads
// the files that changed and forgets those gone or no longer searched.
func (p *Parser) update(q symbolresolve.FileQueryer, paths []string) {
	ctx := p.ctx
	var gone []string
	forget := func(rel string, keep map[string]struct{}) {
		for _, f := range p.cat.under(rel) {
			if _, ok := keep[f]; !ok {
				gone = append(gone, f)
			}
		}
	}
	var todo []work
	for _, rel := range paths {
		info, err := p.cfg.FS.Stat(rel)
		switch {
		case err != nil:
			forget(rel, nil)
		case info.IsDir():
			if p.filter().LeavesOut(rel) {
				forget(rel, nil)
				continue
			}
			files, err := p.filter().Walk(ctx, p.cfg.FS, []string{rel})
			keep := make(map[string]struct{}, len(files))
			for _, f := range files {
				keep[f] = struct{}{}
				if w, ok := p.stale(f); ok {
					todo = append(todo, w)
				}
			}
			// An incomplete walk must not forget what it did not reach.
			if err == nil {
				forget(rel, keep)
			}
		case info.Mode().IsRegular():
			if p.filter().Skips(rel) {
				forget(rel, nil)
				continue
			}
			if w, ok := p.stale(rel); ok {
				todo = append(todo, w)
			}
		}
	}
	p.index(ctx, true, q, todo, gone, nopProgress{})
}

// index reads todo and stores what it finds, along with forgetting the
// files gone, in windows. q reads the files inline when set; workers
// read them in parallel otherwise. Symbol and name records are derived
// from the file records, so their writes skip the per-commit fsync; the
// file records are synced when syncFiles is set, restoring durability
// for everything before them, and a crash in between only costs a
// re-index. It returns how many files it read.
func (p *Parser) index(
	ctx context.Context, syncFiles bool, q symbolresolve.FileQueryer,
	todo []work, gone []string, prog Progress,
) int {
	sctx := bluebolt.ContextWithNoSync(ctx)
	fctx := sctx
	if syncFiles {
		fctx = ctx
	}
	var w symbolWindow
	add := func(u fileUpdate) {
		w.add(u)
		if w.full() {
			p.flushWindow(sctx, fctx, &w)
		}
	}
	for _, rel := range gone {
		if u, ok := p.removal(ctx, rel); ok {
			add(u)
		}
	}
	p.mu.Lock()
	p.left = len(todo)
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.left = 0
		p.mu.Unlock()
	}()
	read := 0
	staged := func(u fileUpdate) {
		add(u)
		prog.File(read, len(todo), u.rel)
		read++
	}
	// One memoized qualifier context serves the whole batch: readers
	// share its existence memo, so package markers are stat'ed once.
	qc := symbolresolve.NewQualifierContext(p.cfg.FS, p.cfg.Root)
	if q != nil {
		for _, wk := range todo {
			u, ok := p.stageFile(ctx, q, qc, wk)
			p.readOne()
			if ok {
				staged(u)
			}
		}
	} else {
		for u := range p.stageAll(ctx, qc, todo) {
			staged(u)
		}
	}
	p.flushWindow(sctx, fctx, &w)
	return read
}

func (p *Parser) readOne() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.left--
}

// stageAll stages todo with parallel readers, and closes the channel it
// returns once they are done.
func (p *Parser) stageAll(
	ctx context.Context, qc symbolresolve.QualifierContext, todo []work,
) <-chan fileUpdate {
	in := make(chan work)
	out := make(chan fileUpdate)
	workers := min(max(runtime.NumCPU()/2, 1), len(todo))
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			q := p.cfg.Indexer.NewQuerySession()
			defer func() { _ = q.Close() }()
			for wk := range in {
				u, ok := p.stageFile(ctx, q, qc, wk)
				p.readOne()
				if !ok {
					continue
				}
				select {
				case out <- u:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	go debug.CapturePanicReport(func() {
		defer close(in)
		for _, wk := range todo {
			select {
			case in <- wk:
			case <-ctx.Done():
				return
			}
		}
	})
	go debug.CapturePanicReport(func() {
		wg.Wait()
		close(out)
	})
	return out
}

// stageFile reads w into the update of its file. It reports false when
// there is nothing to store.
func (p *Parser) stageFile(
	ctx context.Context, q symbolresolve.FileQueryer,
	qc symbolresolve.QualifierContext, w work,
) (fileUpdate, bool) {
	if ctx.Err() != nil {
		return fileUpdate{}, false
	}
	old, known := p.cat.get(w.rel)
	if w.grammar == "" {
		return p.removal(ctx, w.rel)
	}
	uri, err := p.cfg.FS.URI(w.rel)
	if err != nil {
		return fileUpdate{}, false
	}
	u := fileUpdate{
		us: uri.String(), rel: w.rel, modTime: w.mtime, lang: w.lang, grammar: w.grammar,
		force: known && old.version != schemaVersion,
	}
	if known {
		var doc fileDoc
		if err := p.files.Get(ctx, old.uri, &doc); err == nil {
			u.oldNames = doc.Names
		}
	}
	if spec := symbolresolve.SpecForFile(w.rel); spec != nil {
		u.ext, err = symbolresolve.ExtractFile(ctx, q, spec, qc, uri)
	} else {
		u.ext.Definitions, err = symbolresolve.ExtractDefinitions(ctx, q, uri)
	}
	if err != nil {
		if ctx.Err() != nil {
			return fileUpdate{}, false
		}
		log.Errorf("symboldb: extract %q: %v", u.us, err)
		// An empty extraction still replaces the file's contributions
		// so an unparseable file does not retain stale entries.
		u.ext = symbolresolve.FileExtraction{}
		u.skipped = skippedParse + ": " + err.Error()
	}
	recs := records(u.ext.Definitions)
	u.defs, u.count = encodeDefs(recs), len(recs)
	return u, true
}

// removal is the update that forgets rel. It reports false when the
// index does not hold rel.
func (p *Parser) removal(ctx context.Context, rel string) (fileUpdate, bool) {
	e, ok := p.cat.get(rel)
	if !ok {
		return fileUpdate{}, false
	}
	var doc fileDoc
	if err := p.files.Get(ctx, e.uri, &doc); err != nil &&
		!errors.Is(err, storageapi.ErrNotFound) && ctx.Err() == nil {
		log.Errorf("symboldb: read file record %q: %v", e.uri, err)
	}
	return fileUpdate{us: e.uri, rel: rel, oldNames: doc.Names, removed: true}, true
}

// records returns the definitions defs captured, in the order of their
// positions.
func records(defs []symbolresolve.Definition) []record {
	var recs []record
	for _, d := range defs {
		kind, ok := captureKinds[d.Capture]
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(d.Name, "\n")
		if name = strings.TrimSpace(name); name == "" {
			continue
		}
		if len(name) > maxName {
			name = name[:maxName]
		}
		recs = append(recs, record{Kind: kind, Line: d.Pos.Y + 1, Col: d.Pos.X + 1, Name: name})
	}
	slices.SortFunc(recs, func(a, b record) int {
		return cmp.Or(cmp.Compare(a.Line, b.Line), cmp.Compare(a.Col, b.Col),
			cmp.Compare(a.Kind, b.Kind), strings.Compare(a.Name, b.Name))
	})
	return slices.Compact(recs)
}

// langOf returns the language rel is parsed as, "" when it has none.
func langOf(rel string) string {
	if lang, err := languages.LanguageForFile(rel); err == nil {
		return lang
	}
	if spec := symbolresolve.SpecForFile(rel); spec != nil {
		return spec.LangID
	}
	return ""
}

// grammar returns lang's grammar fingerprint, "" when none is installed.
// It is asked once per language until Rescan or Reindex. The lookup
// outlives the index's context: a search parses files after Remove.
func (p *Parser) grammar(lang string) string {
	if lang == "" {
		return ""
	}
	p.mu.Lock()
	m, ok := p.grammars[lang]
	if !ok {
		m = &grammarMemo{}
		p.grammars[lang] = m
	}
	p.mu.Unlock()
	m.once.Do(func() {
		sum, err := p.cfg.Indexer.GrammarFingerprint(context.Background(), lang)
		p.mu.Lock()
		defer p.mu.Unlock()
		if err == nil {
			m.sum = sum
		}
		m.known = true
	})
	p.mu.Lock()
	defer p.mu.Unlock()
	return m.sum
}

// Definitions returns the definitions under q.Roots that q asks for, by
// file in searchfilter.ComparePaths order and by position. The index
// serves what it holds, even while it is being brought up to date.
//
// As workspace searches do, a root the workspace leaves out (hidden,
// excluded or ignored) is searched all the same, only what is below it
// being filtered. The index does not hold those files: they are parsed
// as the iterator is read, as is every file without an index.
func (p *Parser) Definitions(
	ctx context.Context, q DefinitionQuery,
) (iterator.Iterator[Definition], error) {
	p.mu.Lock()
	closed := p.closed
	kept := p.cfg.DB != nil && !p.removed
	p.mu.Unlock()
	if closed {
		return nil, errClosed
	}
	var indexed, direct []string
	for _, root := range searchfilter.CleanRoots(q.Roots) {
		if !kept || p.filter().LeavesOut(root) {
			direct = append(direct, root)
		} else {
			indexed = append(indexed, root)
		}
	}
	var files []entry
	if len(indexed) > 0 {
		files = p.cat.snapshot(searchfilter.Outermost(indexed))
	}
	if len(direct) > 0 {
		walked, err := p.filter().Walk(ctx, p.cfg.FS, searchfilter.Outermost(direct))
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err != nil {
			log.Warnf("symboldb: list the files of %s: %v", p.cfg.Root, err)
		}
		for _, rel := range walked {
			files = append(files, entry{path: rel})
		}
		slices.SortStableFunc(files, func(a, b entry) int {
			return searchfilter.ComparePaths(a.path, b.path)
		})
		files = slices.CompactFunc(files, func(a, b entry) bool { return a.path == b.path })
	}
	return p.definitions(files, q), nil
}

// definitions reads the definitions of files that q asks for, parsing
// the files the catalog does not hold.
func (p *Parser) definitions(files []entry, q DefinitionQuery) iterator.Iterator[Definition] {
	var session symbolresolve.QuerySession
	var buf []Definition
	listed := 0
	fill := func(ctx context.Context, f entry) {
		e := f.e
		if e == nil {
			lang := langOf(f.path)
			if len(q.Langs) > 0 && !slices.Contains(q.Langs, lang) {
				return
			}
			if session == nil {
				session = p.cfg.Indexer.NewQuerySession()
			}
			if e = p.readDefinitions(ctx, session, f.path, lang); e == nil {
				return
			}
		}
		if len(q.Langs) > 0 && !slices.Contains(q.Langs, e.lang) {
			return
		}
		_ = decodeDefs(e.defs, func(r record) bool {
			if len(q.Kinds) == 0 || slices.Contains(q.Kinds, r.Kind) {
				buf = append(buf, Definition{
					Path: f.path, Line: r.Line, Col: r.Col, Kind: r.Kind, Lang: e.lang, Name: r.Name,
				})
			}
			return true
		})
	}
	return iterator.FromFunc(func(ctx context.Context) (Definition, bool, error) {
		for len(buf) == 0 {
			if len(files) == 0 || (q.Limit > 0 && listed >= q.Limit) {
				return Definition{}, false, nil
			}
			if err := ctx.Err(); err != nil {
				return Definition{}, false, err
			}
			fill(ctx, files[0])
			files = files[1:]
		}
		if q.Limit > 0 && listed >= q.Limit {
			return Definition{}, false, nil
		}
		d := buf[0]
		buf = buf[1:]
		listed++
		return d, true, nil
	}, func() error {
		if session != nil {
			return session.Close()
		}
		return nil
	})
}

// readDefinitions parses the definitions of rel for a search the index
// does not serve. It returns nil when rel has no grammar installed or
// fails to parse.
func (p *Parser) readDefinitions(
	ctx context.Context, q symbolresolve.FileQueryer, rel, lang string,
) *fileEntry {
	if p.grammar(lang) == "" {
		return nil
	}
	uri, err := p.cfg.FS.URI(rel)
	if err != nil {
		return nil
	}
	defs, err := symbolresolve.ExtractDefinitions(ctx, q, uri)
	if err != nil {
		return nil
	}
	recs := records(defs)
	return &fileEntry{lang: lang, defs: encodeDefs(recs), count: len(recs)}
}

type fileUpdate struct {
	us       string
	rel      string
	modTime  int64
	lang     string
	grammar  string
	ext      symbolresolve.FileExtraction
	skipped  string
	defs     []byte
	count    int
	oldNames []string
	force    bool
	removed  bool
}

// symbols is what the file now contributes, by name: only its first
// occurrence of a name per kind, since resolution surfaces at most one
// location per (file, kind) — see matchesForSpec — and hot symbols
// appear dozens of times per file, so repeat occurrences would only
// bloat docs that are re-encoded once per contributing file.
func (u fileUpdate) symbols() map[string][]symbolLoc {
	if u.removed {
		return nil
	}
	symbols := make(map[string][]symbolLoc)
	for _, s := range u.ext.Symbols {
		kind := storedKind(s.Kind)
		if hasKind(symbols[s.Name], kind) {
			continue
		}
		symbols[s.Name] = append(symbols[s.Name], symbolLoc{
			URI: u.us, X: s.Pos.X, Y: s.Pos.Y, Kind: kind,
		})
	}
	return symbols
}

// record is the file's record once symbols, what it contributes, are
// stored.
func (u fileUpdate) record(symbols map[string][]symbolLoc) fileDoc {
	return fileDoc{
		URI:     u.us,
		Path:    u.rel,
		ModTime: u.modTime,
		Version: schemaVersion,
		Lang:    u.lang,
		Grammar: u.grammar,
		Names:   slices.Sorted(maps.Keys(symbols)),
		Imports: u.ext.Imports,
		Skipped: u.skipped,
		Defs:    u.defs,
	}
}

// entry is what the catalog holds of the file once its record is stored.
func (u fileUpdate) entry() *fileEntry {
	return &fileEntry{
		uri: u.us, lang: u.lang, grammar: u.grammar, mtime: u.modTime,
		version: schemaVersion, skipped: u.skipped, defs: u.defs, count: u.count,
	}
}

// catalogFiles has the catalog hold what files now record.
func (p *Parser) catalogFiles(files []stagedFile) {
	for _, f := range files {
		if f.u.removed {
			p.cat.remove(f.u.rel)
		} else {
			p.cat.put(f.u.rel, f.u.entry())
		}
	}
}

// applyFile stores one file's update through the per-operation path, for
// a store without batching.
func (p *Parser) applyFile(ctx context.Context, u fileUpdate) {
	if ctx.Err() != nil {
		return
	}
	// Symbol and name records are derived from the file records, so
	// their writes skip the per-commit fsync: the file record written
	// below (synced outside the bulk scan) restores durability for
	// everything this call wrote before it, and a crash in between only
	// costs a re-index of this file.
	nctx := bluebolt.ContextWithNoSync(ctx)
	if u.removed {
		p.upsertSymbols(nctx, u.us, u.oldNames, nil, false)
		if err := p.files.Delete(ctx, u.us); err != nil {
			log.Errorf("symboldb: delete file record %q: %v", u.us, err)
		}
		return
	}
	symbols := u.symbols()
	p.upsertSymbols(nctx, u.us, u.oldNames, symbols, u.force)
	if err := p.files.Set(ctx, u.us, u.record(symbols)); err != nil {
		log.Errorf("symboldb: persist file record %q: %v", u.us, err)
	}
}

// upsertSymbols writes every symbol record one file contributes to, one
// operation at a time: the names it now defines, plus the names it used
// to define and no longer does, which lose this file's locations.
func (p *Parser) upsertSymbols(
	ctx context.Context, us string, oldNames []string,
	symbols map[string][]symbolLoc, force bool,
) {
	stored := make(map[string]struct{}, len(oldNames))
	for _, name := range oldNames {
		stored[name] = struct{}{}
		if _, ok := symbols[name]; ok {
			continue
		}
		p.upsertSymbol(ctx, name, us, nil, force, true)
	}
	for name, locs := range symbols {
		_, existing := stored[name]
		p.upsertSymbol(ctx, name, us, locs, force, existing)
	}
}

func (p *Parser) upsertSymbol(
	ctx context.Context, name, us string, locs []symbolLoc,
	force, existing bool,
) {
	if ctx.Err() != nil {
		return
	}
	var doc symbolDoc
	var merged []symbolLoc
	wrote, wasListed := false, false
	callback := func() ([]storageapi.Update, []storageapi.Precondition) {
		wrote = false
		wasListed = listed(doc.Locs)
		kept := mergeLocs(doc.Locs, map[string][]symbolLoc{us: locs})
		merged = kept
		if sameLocs(doc.Locs, kept) {
			return nil, nil
		}
		// A doc written before the counter existed stores no Version
		// field; nil matches its absence where 0 would not.
		var current any
		if doc.Version != 0 {
			current = doc.Version
		}
		wrote = true
		return []storageapi.Update{
			{FieldPath: []string{"Locs"}, Value: kept},
			{FieldPath: []string{"Version"}, Value: doc.Version + 1},
		}, []storageapi.Precondition{
			{FieldPath: []string{"Version"}, Value: current},
		}
	}
	// Try the one-operation Create first: a cold scan over an empty
	// database — the longest scan there is — mostly inserts brand-new
	// symbols. Dropping locations never creates: a tombstone for a
	// symbol that was never stored would be noise, and neither does a
	// name this file already contributed, whose document must exist.
	created := false
	if len(locs) > 0 && !existing {
		err := p.symbols.Create(ctx, name,
			symbolDoc{Name: name, Locs: locs, Version: 1})
		switch {
		case err == nil:
			created = true
			merged, wrote, wasListed = locs, true, false
		case !errors.Is(err, storageapi.ErrAlreadyExists):
			if ctx.Err() == nil {
				log.Errorf("symboldb: persist symbol %q: %v", name, err)
			}
			return
		}
	}
	if !created {
		err := storageapi.ConsistentUpdate(
			ctx, p.symbols, name, &doc, retryStrategy, callback)
		if errors.Is(err, storageapi.ErrNotFound) {
			if len(locs) == 0 {
				// Nothing stored and nothing to store.
				return
			}
			// The document a previously indexed name vouched for is
			// gone; insert it rather than dropping the locations.
			err = p.symbols.Create(ctx, name,
				symbolDoc{Name: name, Locs: locs, Version: 1})
			if err == nil {
				merged, wrote, wasListed = locs, true, false
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				log.Errorf("symboldb: persist symbol %q: %v", name, err)
			}
			return
		}
	}
	p.syncNameIndex(ctx, name, merged, wrote && !force, wasListed)
}

// mergeLocs replaces the locations each file in replace contributed to
// a stored symbol with what it maps to (nothing, when nil), leaving every
// other file's locations in place. The replacements come after them, by
// file, so that the same inputs give the same document.
func mergeLocs(stored []symbolLoc, replace map[string][]symbolLoc) []symbolLoc {
	kept := make([]symbolLoc, 0, len(stored))
	for _, l := range stored {
		if _, ok := replace[l.URI]; !ok {
			kept = append(kept, l)
		}
	}
	for _, us := range slices.Sorted(maps.Keys(replace)) {
		kept = append(kept, replace[us]...)
	}
	return kept
}

// syncNameIndex reflects a symbol write in the name markers. When the
// write's before-state is trustworthy — wrote reports a write this call
// made, off a document it read — only the listed transitions need a
// name write; otherwise the marker is reconciled against storage.
func (p *Parser) syncNameIndex(
	ctx context.Context, name string, merged []symbolLoc,
	wrote, wasListed bool,
) {
	if !wrote {
		p.syncName(ctx, name, merged)
		return
	}
	switch isListed := listed(merged); {
	case isListed && !wasListed:
		if err := p.names.Set(ctx, name, nameDoc{Name: name}); err != nil {
			log.Errorf("symboldb: persist name %q: %v", name, err)
		}
	case wasListed && !isListed:
		p.deleteName(ctx, name)
	}
}

func (p *Parser) syncName(ctx context.Context, name string, locs []symbolLoc) {
	if ctx.Err() != nil {
		return
	}
	if !listed(locs) {
		if p.hasName(ctx, name) {
			p.deleteName(ctx, name)
		}
		return
	}
	if p.hasName(ctx, name) {
		return
	}
	if err := p.names.Set(ctx, name, nameDoc{Name: name}); err != nil {
		log.Errorf("symboldb: persist name %q: %v", name, err)
	}
}

func sameLocs(a, b []symbolLoc) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[symbolLoc]int, len(a))
	for _, l := range a {
		counts[l]++
	}
	for _, l := range b {
		if counts[l] == 0 {
			return false
		}
		counts[l]--
	}
	return true
}

func (p *Parser) hasName(ctx context.Context, name string) bool {
	var doc nameDoc
	return p.names.Get(ctx, name, &doc) == nil
}

func (p *Parser) deleteName(ctx context.Context, name string) {
	if err := p.names.Delete(ctx, name); err != nil &&
		!errors.Is(err, storageapi.ErrNotFound) {
		log.Errorf("symboldb: delete name %q: %v", name, err)
	}
}

// hasKind reports whether locs already carry a location of kind.
func hasKind(locs []symbolLoc, kind int) bool {
	for _, l := range locs {
		if l.Kind == kind {
			return true
		}
	}
	return false
}

type nopProgress struct{}

func (nopProgress) Start(int)             {}
func (nopProgress) File(int, int, string) {}
func (nopProgress) Done(int)              {}
func (nopProgress) Stop(error)            {}
