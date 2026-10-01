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

package symboldb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	bluebolt "github.com/unstablebuild/blue/document/bolt"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/retry"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idelsp/symbolresolve"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace/walkdir"
)

// retryStrategy bounds the compare-and-swap retries of upsertSymbol.
// All of this process's writes flow through one goroutine at a time,
// so retries only fire when another process indexes the same
// workspace through the shared store.
var retryStrategy = retry.DefaultStrategy

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

// Backing is the parser the index wraps: the full editor parser surface
// for query delegation plus a factory for the batched query sessions
// the indexer runs extraction through. syntax.Parser satisfies it.
type Backing interface {
	syntaxapi.Parser
	// NewQuerySession returns a per-goroutine batched file querier.
	NewQuerySession() symbolresolve.QuerySession
}

// Parser is an index-backed syntaxapi.Parser. It also implements
// text.EventHandler to invalidate per-file index entries and io.Closer
// to stop the indexer goroutine. The storage service is caller-owned
// and is not closed by Close.
type Parser struct {
	backing Backing
	fs      workspaceapi.FileSystem
	root    workspaceapi.URI
	notify  browserapi.Notifications
	// schedule enqueues a closure onto the editor event loop. All
	// Notifications calls go through it: the production implementation
	// reads focus state and mutates UI components the loop owns.
	schedule func(func()) bool

	// files, symbols and names are separate partitions because List is
	// the storage API's only enumeration primitive and it decodes a
	// whole partition: the scan's stat-only restart path lists file
	// records without deserializing the (much larger) symbols table,
	// and ListReferencedSymbols streams the tiny name markers without
	// decoding any symbol locations. meta is the caller's db itself,
	// holding only the scan marker at its root.
	files   storageapi.Service
	symbols storageapi.Service
	names   storageapi.Service
	meta    storageapi.Service
	// batch is the symbols partition's batching interface when the
	// storage chain provides one; nil once it turns out not to.
	batch storageapi.BatchWriter

	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex
	// dirty is the coalescing set of file URIs pending re-index.
	dirty map[string]struct{}
	// busy is true while the worker processes a dirty entry.
	busy bool
	// scanned is true once this session's full scan completed.
	scanned bool
	// scannedEver is true once any scan has ever completed over this
	// database (persisted marker); it gates ListReferencedSymbols.
	scannedEver bool
	closed      bool
	// updated is closed (and replaced) on every indexer state change so
	// Wait can block without polling.
	updated chan struct{}

	// wake nudges the worker when the dirty set becomes non-empty.
	wake chan struct{}
	// done is closed when the worker goroutine exits.
	done chan struct{}
}

var _ syntaxapi.Parser = (*Parser)(nil)

// New returns an index-backed Parser over the given backing parser and
// workspace filesystem, persisting its database in db. Indexing starts
// immediately in the background; queries are served from whatever
// state the database is in.
func New(
	backing Backing, fs workspaceapi.FileSystem,
	root workspaceapi.URI, db storageapi.Service,
	notify browserapi.Notifications,
	schedule func(func()) bool,
) (*Parser, error) {
	files, err := db.Partition(filesPartition)
	if err != nil {
		return nil, err
	}
	symbols, err := db.Partition(symbolsPartition)
	if err != nil {
		return nil, err
	}
	names, err := db.Partition(namesPartition)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Parser{
		backing:  backing,
		fs:       fs,
		root:     root,
		notify:   notify,
		schedule: schedule,
		files:    files,
		symbols:  symbols,
		names:    names,
		meta:     db,
		ctx:      ctx,
		cancel:   cancel,
		dirty:    make(map[string]struct{}),
		updated:  make(chan struct{}),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
	if writer, ok := symbols.(storageapi.BatchWriter); ok {
		p.batch = writer
	}
	// A marker written by another schema version must not enable
	// index-served listing: the derived tables it vouches for may not
	// exist under that older layout.
	var m metaDoc
	if err := db.Get(ctx, metaScanID, &m); err == nil && m.Version == schemaVersion {
		p.scannedEver = m.Complete
	}
	go debug.CapturePanicReport(p.run)
	return p, nil
}

// CleanupWorkspaceHook returns a hook that drops the index of a
// workspace, given the storage symboldb databases are partitioned out
// of (the same service New's db argument is derived from). It is a
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
			return fmt.Errorf("symboldb: drop %q: %w", root.String(), err)
		}
		return nil
	}
}

func drop(ctx context.Context, svc storageapi.Service) error {
	droppable, ok := svc.(storageapi.DroppableService)
	if !ok {
		return errors.New("storage service does not support dropping")
	}
	return droppable.Drop(ctx)
}

// Handle implements text.EventHandler. It is O(1) and never blocks:
// relevant events only mark the file dirty for the indexer goroutine.
func (p *Parser) Handle(_ context.Context, ev textapi.Event) bool {
	switch ev.Type {
	case textapi.EventTypeFlush, textapi.EventTypeChange,
		textapi.EventTypeCreate, textapi.EventTypeRemove,
		textapi.EventTypeRename:
	default:
		return false
	}
	us := ev.URI.String()
	if symbolresolve.SpecForFile(us) == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return true
	}
	p.dirty[us] = struct{}{}
	p.wakeLocked()
	select {
	case p.wake <- struct{}{}:
	default:
	}
	return false
}

// Wait blocks until the initial scan has completed and the dirty queue
// has drained. It is intended for tests and diagnostics.
func (p *Parser) Wait(ctx context.Context) error {
	for {
		p.mu.Lock()
		if p.scanned && !p.busy && len(p.dirty) == 0 {
			p.mu.Unlock()
			return nil
		}
		updated := p.updated
		p.mu.Unlock()
		select {
		case <-updated:
		case <-ctx.Done():
			return ctx.Err()
		case <-p.ctx.Done():
			return p.ctx.Err()
		}
	}
}

// Close stops the indexer goroutine. The storage service is
// caller-owned and left open.
func (p *Parser) Close() error {
	p.mu.Lock()
	p.closed = true
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

// setScanned records that this session's full scan finished.
func (p *Parser) setScanned() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scanned = true
	if p.ctx.Err() == nil {
		p.scannedEver = true
	}
	p.wakeLocked()
}

// nextDirty pops one dirty entry, tracking the worker's busy state so
// Wait cannot observe an empty queue while a job is still in flight.
func (p *Parser) nextDirty() (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for us := range p.dirty {
		delete(p.dirty, us)
		p.busy = true
		return us, true
	}
	if p.busy {
		p.busy = false
		p.wakeLocked()
	}
	return "", false
}

func (p *Parser) hasScannedEver() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scannedEver
}

// Search delegates to the backing parser.
func (p *Parser) Search(
	query string, captureNames []string, languages ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return p.backing.Search(query, captureNames, languages...)
}

// SearchNode delegates to the backing parser.
func (p *Parser) SearchNode(
	nodeTypes syntaxapi.NodeCaptureName, languages ...string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return p.backing.SearchNode(nodeTypes, languages...)
}

// Query delegates to the backing parser.
func (p *Parser) Query(
	file workspaceapi.URI, query string, captureNames []string,
) (iterator.Iterator[syntaxapi.Result], error) {
	return p.backing.Query(file, query, captureNames)
}

// QueryNode delegates to the backing parser.
func (p *Parser) QueryNode(
	file workspaceapi.URI, nodeTypes syntaxapi.NodeCaptureName,
) (iterator.Iterator[syntaxapi.Result], error) {
	return p.backing.QueryNode(file, nodeTypes)
}

// Highlight delegates to the backing parser.
func (p *Parser) Highlight(
	uri workspaceapi.URI, content string,
) (iterator.Iterator[textapi.Location], error) {
	return p.backing.Highlight(uri, content)
}

// ResolveSymbol serves a symbol lookup from the index, passing through
// to the backing parser only until the first full scan completes. Once
// the index is authoritative a miss returns no matches instead of
// falling back, so true negatives resolve without invoking the backing
// parser. Indexed results may be stale while a rebuild is in flight.
func (p *Parser) ResolveSymbol(
	ctx context.Context, name string, progress syntaxapi.Progress,
) (iterator.Iterator[syntaxapi.Match], error) {
	if !strings.Contains(name, ".") {
		return nil, syntaxapi.ErrNoDot
	}
	matches, indexable, found := p.resolveFromIndex(ctx, name, progress)
	if found {
		return iterator.FromSlice(matches), nil
	}
	// A miss is authoritative only once a full scan has populated the
	// index; until then the symbol may simply not be indexed yet.
	if indexable && p.hasScannedEver() {
		return iterator.Empty[syntaxapi.Match](), nil
	}
	return p.backing.ResolveSymbol(ctx, name, progress)
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
		if len(specMatches) > 1 && len(parts) == 2 && spec.ImportPathQuery != "" {
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
		return p.backing.ListReferencedSymbols(ctx)
	}
	it, err := p.names.List(ctx, nil)
	if err != nil {
		return p.backing.ListReferencedSymbols(ctx)
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
	p.scan()
	p.setScanned()
	q := p.backing.NewQuerySession()
	defer func() { _ = q.Close() }()
	for {
		select {
		case <-p.ctx.Done():
			return
		default:
		}
		uri, ok := p.nextDirty()
		if ok {
			p.indexFile(p.ctx, q, uri)
			continue
		}
		select {
		case <-p.wake:
		case <-p.ctx.Done():
			return
		}
	}
}

func (p *Parser) scan() {
	prog := p.newScanProgress()
	prog.start()
	defer prog.done()

	wctx := walkdir.WithContextFilter(p.ctx, p.walkFilter())
	it, err := walkdir.ListFiles(wctx, p.fs, ".")
	if err != nil {
		log.Errorf("symboldb: scan %q: %v", p.root.String(), err)
		return
	}
	// Close joins the walk's traversal goroutines.
	defer func() { _ = it.Close() }()

	// Bulk-loaded records are rebuildable, so their writes ask the bolt
	// backend to skip per-commit fsync; the completion marker below is
	// written synced, restoring durability for everything before it.
	nctx := bluebolt.ContextWithNoSync(p.ctx)
	// One memoized qualifier context serves the whole scan: workers
	// share its existence memo, so package markers are stat'ed once.
	qc := symbolresolve.NewQualifierContext(p.fs, p.root)
	workers := max(runtime.NumCPU()/2, 1)
	updates := make(chan fileUpdate, workers)
	writerDone := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(writerDone)
		for {
			select {
			case u, ok := <-updates:
				if !ok {
					return
				}
				p.applyFile(nctx, u)
			case <-p.ctx.Done():
				return
			}
		}
	})
	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go debug.CapturePanicReport(func() {
			defer wg.Done()
			q := p.backing.NewQuerySession()
			defer func() { _ = q.Close() }()
			for {
				rel, ok := it.Next(wctx)
				if !ok {
					return
				}
				if symbolresolve.SpecForFile(rel) == nil {
					continue
				}
				prog.file(rel)
				uri, err := p.fs.URI(rel)
				if err != nil {
					continue
				}
				u, ok := p.stageFile(p.ctx, q, qc, uri.String())
				if !ok {
					continue
				}
				select {
				case updates <- u:
				case <-p.ctx.Done():
					return
				}
			}
		})
	}
	wg.Wait()
	close(updates)
	<-writerDone

	if p.ctx.Err() != nil {
		return
	}
	if err := it.Err(); err != nil {
		// An incomplete walk must not wipe records for files it never
		// reached, nor claim the scan completed.
		log.Errorf("symboldb: scan %q: %v", p.root.String(), err)
		return
	}
	p.removeMissingFiles(nctx)
	if p.ctx.Err() != nil {
		return
	}
	marker := metaDoc{Complete: true, Version: schemaVersion}
	// Written with the plain context: this synced commit is the
	// durability barrier for every relaxed write above.
	if err := p.meta.Set(p.ctx, metaScanID, marker); err != nil {
		log.Errorf("symboldb: persist scan marker: %v", err)
	}
}

func (p *Parser) removeMissingFiles(ctx context.Context) {
	it, err := p.files.List(p.ctx, nil)
	if err != nil {
		log.Errorf("symboldb: list file records: %v", err)
		return
	}
	defer func() { _ = it.Close() }()
	for it.HasNext() {
		var doc fileDoc
		if err := it.NextTo(&doc); err != nil {
			log.Errorf("symboldb: read file record: %v", err)
			continue
		}
		if p.ctx.Err() != nil {
			return
		}
		if _, serr := p.fs.Stat(doc.Path); errors.Is(serr, fs.ErrNotExist) {
			p.applyFile(ctx, fileUpdate{
				us: doc.URI, oldNames: doc.Names, removed: true,
			})
		}
	}
}

// seen records key in set, reporting whether it was already present.
func seen(set map[string]struct{}, key string) bool {
	if _, ok := set[key]; ok {
		return true
	}
	set[key] = struct{}{}
	return false
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

type scanProgress struct {
	notify   browserapi.Notifications
	schedule func(func()) bool
	id       string

	mu       sync.Mutex
	seenDirs map[string]struct{}
	scanned  int
}

func (p *Parser) newScanProgress() *scanProgress {
	return &scanProgress{
		notify:   p.notify,
		schedule: p.schedule,
		seenDirs: make(map[string]struct{}),
	}
}

func (s *scanProgress) start() {
	s.schedule(func() {
		id, err := s.notify.Notify(browserapi.LevelInfo, "Indexing workspace symbols")
		if err == nil {
			s.id = id
		}
	})
}

func (s *scanProgress) file(rel string) {
	s.mu.Lock()
	dir := filepath.Dir(rel)
	first := !seen(s.seenDirs, dir)
	scanned := s.scanned
	s.scanned++
	s.mu.Unlock()
	if first {
		s.dir(dir, scanned)
	}
}

func (s *scanProgress) dir(dir string, scanned int) {
	s.schedule(func() {
		if s.id == "" {
			return
		}
		_ = s.notify.UpdateNotificationProgress(s.id,
			"Indexing symbols: "+dir, int64(scanned), int64(scanned)+1)
	})
}

func (s *scanProgress) done() {
	s.mu.Lock()
	scanned := s.scanned
	s.mu.Unlock()
	s.schedule(func() {
		if s.id == "" {
			return
		}
		total := int64(max(scanned, 1))
		_ = s.notify.UpdateNotificationProgress(s.id,
			fmt.Sprintf("Indexed %d files", scanned), total, total)
	})
}

type fileUpdate struct {
	us       string
	rel      string
	modTime  int64
	ext      symbolresolve.FileExtraction
	oldNames []string
	force    bool
	removed  bool
}

func (p *Parser) stageFile(
	ctx context.Context, q symbolresolve.FileQueryer,
	qc symbolresolve.QualifierContext, us string,
) (fileUpdate, bool) {
	if ctx.Err() != nil {
		return fileUpdate{}, false
	}
	uri, err := workspaceapi.ParseURI(us)
	if err != nil {
		return fileUpdate{}, false
	}
	rel := workspaceapi.RelPath(p.root, uri)
	spec := symbolresolve.SpecForFile(rel)
	if spec == nil {
		return fileUpdate{}, false
	}
	var old fileDoc
	known := p.files.Get(ctx, us, &old) == nil
	finfo, err := p.fs.Stat(rel)
	if err != nil {
		if known {
			return fileUpdate{us: us, oldNames: old.Names, removed: true}, true
		}
		return fileUpdate{}, false
	}
	modTime := finfo.ModTime().UnixNano()
	if known && old.ModTime == modTime && old.Version == schemaVersion {
		return fileUpdate{}, false
	}
	ext, err := symbolresolve.ExtractFile(ctx, q, spec, qc, uri)
	if err != nil {
		if ctx.Err() != nil {
			return fileUpdate{}, false
		}
		log.Errorf("symboldb: extract %q: %v", us, err)
		// An empty extraction still replaces the file's contributions
		// so an unparseable file does not retain stale entries.
		ext = symbolresolve.FileExtraction{}
	}
	return fileUpdate{
		us: us, rel: rel, modTime: modTime, ext: ext,
		oldNames: old.Names,
		force:    known && old.Version != schemaVersion,
	}, true
}

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
	symbols := make(map[string][]symbolLoc)
	for _, s := range u.ext.Symbols {
		kind := storedKind(s.Kind)
		// Store only the file's first occurrence per kind: resolution
		// surfaces at most one location per (file, kind) — see
		// matchesForSpec — and hot symbols appear dozens of times per
		// file, so repeat occurrences only bloat docs that the writer
		// re-encodes once per contributing file.
		if hasKind(symbols[s.Name], kind) {
			continue
		}
		symbols[s.Name] = append(symbols[s.Name], symbolLoc{
			URI: u.us, X: s.Pos.X, Y: s.Pos.Y, Kind: kind,
		})
	}
	p.upsertSymbols(nctx, u.us, u.oldNames, symbols, u.force)
	doc := fileDoc{
		URI:     u.us,
		Path:    u.rel,
		ModTime: u.modTime,
		Version: schemaVersion,
		Names:   slices.Sorted(maps.Keys(symbols)),
		Imports: u.ext.Imports,
	}
	if err := p.files.Set(ctx, u.us, doc); err != nil {
		log.Errorf("symboldb: persist file record %q: %v", u.us, err)
	}
}

// upsertSymbols writes every symbol record one file contributes to:
// the names it now defines, plus the names it used to define and no
// longer does, which lose this file's locations.
func (p *Parser) upsertSymbols(
	ctx context.Context, us string, oldNames []string,
	symbols map[string][]symbolLoc, force bool,
) {
	if p.batch != nil {
		p.upsertSymbolBatch(ctx, us, oldNames, symbols, force)
		return
	}
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

// indexFile stages and applies one file inline; it serves the dirty
// drain, where staging and writing share the event goroutine.
func (p *Parser) indexFile(
	ctx context.Context, q symbolresolve.FileQueryer, us string,
) {
	qc := symbolresolve.NewQualifierContext(p.fs, p.root)
	if u, ok := p.stageFile(ctx, q, qc, us); ok {
		p.applyFile(ctx, u)
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
		kept := mergeLocs(doc.Locs, us, locs)
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

// mergeLocs replaces the locations us contributed to a stored symbol
// with locs, leaving every other file's locations in place.
func mergeLocs(stored []symbolLoc, us string, locs []symbolLoc) []symbolLoc {
	kept := make([]symbolLoc, 0, len(stored)+len(locs))
	for _, l := range stored {
		if l.URI != us {
			kept = append(kept, l)
		}
	}
	return append(kept, locs...)
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

func (p *Parser) walkFilter() walkdir.Filter {
	hidden := hiddenDirMatcher{vctrl.HiddenBaseMatcher()}
	m, err := vctrl.LoadGitignore(p.fs)
	if err != nil {
		return hidden
	}
	return vctrl.AnyMatcher(m, hidden)
}

type hiddenDirMatcher struct{ m vctrl.Matcher }

func (h hiddenDirMatcher) Match(uri workspaceapi.URI, isDir bool) bool {
	return isDir && h.m.Match(uri, isDir)
}

func (h hiddenDirMatcher) MatchRelPath(relpath string, isDir bool) bool {
	return isDir && h.m.MatchRelPath(relpath, isDir)
}
