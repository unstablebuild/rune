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
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	bluebolt "github.com/unstablebuild/blue/document/bolt"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

const (
	// defaultLimit is how many hits a request without a limit gets.
	defaultLimit = 20
	// queryCacheSize query vectors are kept, the oldest dropped first.
	queryCacheSize = 32
	// settleDelay lets a burst of disk events gather before the indexer
	// looks at them: a rename arrives as the old path going and the new
	// one appearing, and only seen together are they a move.
	settleDelay = 100 * time.Millisecond
)

// Encoder states, as Status reports them.
const (
	encoderReady         = "ready"
	encoderNotDownloaded = "not downloaded"
	// encoderUnavailable is followed by ": " and why.
	encoderUnavailable = "unavailable"
)

// EditorEvents returns the editor event types an Index must be subscribed
// to in order to keep the index fresh: in-editor saves and the out-of-band
// disk events. Buffer edits are excluded because they do not touch disk.
func EditorEvents() []textapi.EventType {
	return []textapi.EventType{
		textapi.EventTypeFlush,
		textapi.EventTypeChange,
		textapi.EventTypeCreate,
		textapi.EventTypeRemove,
		textapi.EventTypeRename,
	}
}

// IndexConfig is what an Index is opened with. DB is caller-owned: it is
// where this workspace's index persists, and is not closed by Close.
// Schedule enqueues a closure on the editor event loop; every Notify call
// goes through it.
type IndexConfig struct {
	Root     workspaceapi.URI
	FS       workspaceapi.FileSystem
	DB       storageapi.Service
	Notify   browserapi.Notifications
	Schedule func(func()) bool
	Options  Options
}

// Status describes an Index.
type Status struct {
	// Loaded is false until the index has been read from storage; the
	// other fields are empty until then.
	Loaded bool
	// Scanned is true once this session's first scan of the workspace
	// finished.
	Scanned bool
	// Files counts the files known by state: pending, done, skipped and
	// failed.
	Files    map[string]int
	Segments int
	Vectors  int
	// Fingerprint and VectorSource are what the vectors were made with;
	// VectorSource is empty for an index without vectors.
	Fingerprint  string
	VectorSource string
	// Stalled is why pending files are not being indexed, if they are not.
	Stalled string
	// Encoder is the state of the query encoder: empty before the first
	// meaning search.
	Encoder string
	// Skipped and SkippedFolders count what the last full scan left out,
	// by reason.
	Skipped        map[string]int
	SkippedFolders map[string]int
}

// Unavailable says why meaning search cannot answer, if the last query
// could not be encoded or the indexer stalled; the query is the later
// attempt, so its reason comes first. Words-only answers are then
// partial too: the files the indexer did not reach have no words
// indexed either.
func (s Status) Unavailable() string {
	if why, ok := strings.CutPrefix(s.Encoder, encoderUnavailable+": "); ok {
		return why
	}
	return s.Stalled
}

// semantic is the status of a search answered while the index was s,
// ranked by words alone for the reason wordsOnly when it is not empty.
func (s Status) semantic(wordsOnly string) searchapi.SemanticStatus {
	var out searchapi.SemanticStatus
	var notes []string
	pending := s.Files[statePending]
	switch {
	case s.Stalled != "":
		notes = append(notes, "indexing stalled: "+s.Stalled)
	case pending > 0:
		files := "files"
		if pending == 1 {
			files = "file"
		}
		notes = append(notes, fmt.Sprintf("%d %s left to index", pending, files))
	case !s.Scanned:
		notes = append(notes, "still indexing")
	}
	out.Indexing = len(notes) > 0
	if wordsOnly != "" {
		out.WordsOnly = true
		notes = append(notes, "matched by words only: "+wordsOnly)
	}
	out.Message = strings.Join(notes, " · ")
	return out
}

// Index is one workspace's search index: a background indexer over the
// workspace filesystem, persisted in IndexConfig.DB, and the searcher that
// answers queries from it. It implements text.EventHandler so that editor
// saves and disk events re-index only the files they touch, and io.Closer
// to stop the indexer.
type Index struct {
	svc *Service
	cfg IndexConfig
	cat *catalog
	// st and ix are set before loaded is closed.
	st      *store
	ix      *indexer
	loaded  chan struct{}
	loadErr error

	ctx    context.Context
	cancel context.CancelFunc

	mu sync.Mutex
	// dirty is the coalescing set of workspace-relative paths to sync.
	dirty map[string]struct{}
	// full asks for a scan of the whole workspace, after a reset when
	// reset is set too.
	full, reset bool
	busy        bool
	scanned     bool
	closed      bool
	stalled     string
	encoder     string
	report      crawlReport
	queries     map[string][]float32
	queryOrder  []string
	idle        *time.Timer
	// updated is closed (and replaced) on every state change so that Wait
	// can block without polling.
	updated chan struct{}
	wake    chan struct{}
	done    chan struct{}
}

var _ io.Closer = (*Index)(nil)

func newIndex(svc *Service, cfg IndexConfig) *Index {
	ctx, cancel := context.WithCancel(context.Background())
	return &Index{
		svc:     svc,
		cfg:     cfg,
		cat:     newCatalog(),
		loaded:  make(chan struct{}),
		ctx:     ctx,
		cancel:  cancel,
		dirty:   map[string]struct{}{},
		queries: map[string][]float32{},
		updated: make(chan struct{}),
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
}

// job is one pass of the indexer: the whole workspace, or some paths.
type job struct {
	full, reset bool
	paths       []string
}

func (i *Index) run() {
	defer close(i.done)
	i.loadErr = i.open()
	close(i.loaded)
	if i.loadErr != nil {
		if i.ctx.Err() == nil {
			log.Errorf("llmsearch: open %s: %v", i.cfg.Root, i.loadErr)
		}
		i.setScanned()
		return
	}
	i.pass(job{full: true})
	i.setScanned()
	for {
		if i.ctx.Err() != nil {
			return
		}
		if j, ok := i.next(); ok {
			i.pass(j)
			continue
		}
		select {
		case <-i.wake:
		case <-i.ctx.Done():
			return
		}
		select {
		case <-time.After(settleDelay):
		case <-i.ctx.Done():
			return
		}
	}
}

func (i *Index) open() error {
	st, err := openStore(i.ctx, i.cfg.DB)
	if err != nil {
		return err
	}
	i.st = st
	i.ix = &indexer{st: st, fs: i.cfg.FS, title: path.Base(i.cfg.Root.Path()), opts: i.cfg.Options, cat: i.cat}
	return i.ix.load(i.ctx)
}

// pass brings the index in step with j's paths, or the whole workspace,
// then indexes what they queued (everything pending, for the whole
// workspace).
func (i *Index) pass(j job) {
	ctx := i.ctx
	if j.reset {
		if err := i.ix.resetAll(ctx); err != nil {
			i.logError("reset", err)
			return
		}
	}
	embed := i.svc.indexEmbedder(ctx)
	stalled := ""
	if embed == nil && i.st.meta().VectorSource != "" {
		// Vectors from the model are not made without it: what is
		// pending waits for it to be downloaded again.
		stalled = "the embedding model is not downloaded: " + i.svc.downloadHint()
	} else {
		prev := i.st.meta().VectorSource
		reset, err := i.ix.adopt(ctx, embed)
		if err != nil {
			i.logError("adopt the embedding model", err)
			return
		}
		if reset {
			j.full = true
			// An index without vectors gaining its first model is
			// what the user asked for, not a change to warn about.
			if prev != "" && prev != i.st.meta().VectorSource {
				i.schedule(func() {
					_, _ = i.cfg.Notify.Notify(browserapi.LevelInfo,
						"The embedding model changed: rebuilding the search index")
				})
			}
		}
	}
	i.setStalled(stalled)

	roots, sctx := j.paths, ctx
	if j.full {
		// The records sync adds can be rebuilt, so they skip the per-write
		// fsync; the synced meta write after it is the durability barrier.
		// What run stores is written synced.
		roots, sctx = []string{"."}, bluebolt.ContextWithNoSync(ctx)
	}
	sum, err := i.ix.sync(sctx, roots)
	if err != nil {
		i.logError("sync", err)
		return
	}
	if j.full {
		i.mu.Lock()
		i.report = sum.report
		i.mu.Unlock()
		if err := i.st.updateMeta(ctx, func(m *metaDoc) { m.Complete = m.Complete || sum.complete }); err != nil {
			i.logError("write meta", err)
			return
		}
	}
	if stalled != "" || (!j.full && len(sum.queued) == 0) {
		return
	}
	var only []uint32
	if !j.full {
		only = sum.queued
	}
	var prog *runProgress
	if j.full {
		prog = &runProgress{notify: i.cfg.Notify, schedule: i.schedule}
	}
	err = i.ix.run(ctx, embed, only, prog.event)
	if ee, ok := errors.AsType[*embedderError](err); ok {
		i.logError("stall indexing", ee)
		i.setStalled(ee.Error())
		prog.stop(ee.Error())
		return
	}
	if err != nil && ctx.Err() == nil {
		i.logError("index", err)
		prog.stop(err.Error())
	}
}

func (i *Index) logError(what string, err error) {
	if i.ctx.Err() == nil {
		log.Errorf("llmsearch: %s %s: %v", what, i.cfg.Root, err)
	}
}

// schedule runs fn on the editor event loop.
func (i *Index) schedule(fn func()) { i.cfg.Schedule(fn) }

// runProgress reports a full run in one notification. A nil runProgress
// reports nothing.
type runProgress struct {
	notify   browserapi.Notifications
	schedule func(func())
	id       string
	dir      string
}

func (p *runProgress) event(ev runEvent) {
	if p == nil {
		return
	}
	if ev.done == 0 {
		p.schedule(func() {
			id, err := p.notify.Notify(browserapi.LevelInfo,
				"Indexing the workspace for semantic search")
			if err == nil {
				p.id = id
			}
		})
		return
	}
	if ev.done == ev.total {
		p.schedule(func() {
			if p.id != "" {
				total := int64(ev.total)
				files := "files"
				if ev.total == 1 {
					files = "file"
				}
				_ = p.notify.UpdateNotificationProgress(p.id,
					fmt.Sprintf("Indexed %d %s for semantic search",
						ev.total, files), total, total)
			}
		})
		return
	}
	if dir := path.Dir(ev.path); dir != p.dir {
		p.dir = dir
		p.schedule(func() {
			if p.id != "" {
				_ = p.notify.UpdateNotificationProgress(p.id,
					"Indexing for semantic search: "+dir, int64(ev.done), int64(ev.total))
			}
		})
	}
}

// stop finishes the notification of a run that cannot go on, since a
// progress notification otherwise stays open until it is complete.
func (p *runProgress) stop(why string) {
	if p == nil {
		return
	}
	p.schedule(func() {
		if p.id != "" {
			_ = p.notify.UpdateNotificationProgress(p.id,
				"Indexing for semantic search stopped: "+why, 1, 1)
		}
	})
}

// next pops the next job, tracking the worker's busy state so Wait cannot
// observe an empty queue while a job is still in flight.
func (i *Index) next() (job, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.full {
		j := job{full: true, reset: i.reset}
		i.full, i.reset, i.busy = false, false, true
		clear(i.dirty)
		return j, true
	}
	if len(i.dirty) > 0 {
		j := job{paths: slices.Sorted(maps.Keys(i.dirty))}
		clear(i.dirty)
		i.busy = true
		return j, true
	}
	if i.busy {
		i.busy = false
		i.wakeLocked()
	}
	return job{}, false
}

// wakeLocked wakes every Wait blocked on a state change. Callers hold mu.
func (i *Index) wakeLocked() {
	close(i.updated)
	i.updated = make(chan struct{})
}

// nudgeLocked wakes the worker. Callers hold mu.
func (i *Index) nudgeLocked() {
	i.wakeLocked()
	select {
	case i.wake <- struct{}{}:
	default:
	}
}

func (i *Index) setScanned() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.scanned = true
	i.wakeLocked()
}

func (i *Index) setStalled(why string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.stalled = why
}

// Rescan reads the whole workspace again in the background, keeping what
// is still up to date. It is how the index adopts an embedding model
// downloaded since it last looked.
func (i *Index) Rescan() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.closed && !i.full {
		i.full = true
		i.nudgeLocked()
	}
}

// Handle implements text.EventHandler. It never blocks: a relevant event
// only marks its path for the indexer.
func (i *Index) Handle(_ context.Context, ev textapi.Event) bool {
	switch ev.Type {
	case textapi.EventTypeFlush, textapi.EventTypeChange,
		textapi.EventTypeCreate, textapi.EventTypeRemove, textapi.EventTypeRename:
	default:
		return false
	}
	if !workspaceapi.HasPrefix(ev.URI, i.cfg.Root) {
		return false
	}
	rel := path.Clean(filepath.ToSlash(workspaceapi.RelPath(i.cfg.Root, ev.URI)))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return false
	}
	// A file written in place is only worth a look if it is code; a path
	// created, removed or renamed may be a folder of code.
	if ev.Type == textapi.EventTypeFlush || ev.Type == textapi.EventTypeChange {
		noOpen := func() (io.ReadCloser, error) { return nil, errors.ErrUnsupported }
		if classifyCode(path.Base(rel), 0, noOpen, i.cfg.Options) != "" {
			return false
		}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return false
	}
	i.dirty[rel] = struct{}{}
	i.nudgeLocked()
	return false
}

// Wait blocks until the first scan has finished and every queued change
// has been indexed. It is meant for tests and diagnostics.
func (i *Index) Wait(ctx context.Context) error {
	for {
		i.mu.Lock()
		if i.closed {
			i.mu.Unlock()
			return errClosed
		}
		if i.scanned && !i.busy && !i.full && len(i.dirty) == 0 {
			i.mu.Unlock()
			return i.loadErr
		}
		updated := i.updated
		i.mu.Unlock()
		select {
		case <-updated:
		case <-ctx.Done():
			return ctx.Err()
		case <-i.ctx.Done():
			return i.ctx.Err()
		}
	}
}

// Reindex empties the index and reads the whole workspace again, in the
// background.
func (i *Index) Reindex() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return errClosed
	}
	i.full, i.reset = true, true
	i.nudgeLocked()
	return nil
}

// Status describes the index now.
func (i *Index) Status() Status {
	select {
	case <-i.loaded:
	default:
		return Status{}
	}
	st := Status{Loaded: true, Files: map[string]int{}}
	if i.loadErr != nil {
		st.Stalled = i.loadErr.Error()
		return st
	}
	for _, f := range i.cat.snapshot() {
		st.Files[f.State]++
		st.Segments += len(f.Segments)
	}
	st.Vectors = i.cat.vectorCount()
	m := i.st.meta()
	st.Fingerprint, st.VectorSource = m.Fingerprint, m.VectorSource
	i.mu.Lock()
	defer i.mu.Unlock()
	st.Scanned, st.Stalled, st.Encoder = i.scanned, i.stalled, i.encoder
	st.Skipped, st.SkippedFolders = maps.Clone(i.report.skipped), maps.Clone(i.report.skippedFolders)
	return st
}

// Close stops the indexer and closes the index; the storage is left open.
// It is safe to call more than once.
func (i *Index) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		<-i.done
		return nil
	}
	i.closed = true
	if i.idle != nil {
		i.idle.Stop()
	}
	i.mu.Unlock()
	i.cancel()
	<-i.done
	i.svc.unregister(i)
	return nil
}

// search answers req: by meaning too when req.Meaning, the index has the
// vectors of req.Model's embedder, and req.Model is downloaded. The status
// says why the answer may be partial or ranked by words alone.
func (i *Index) search(
	ctx context.Context, req searchapi.SemanticRequest,
) ([]searchapi.SemanticResult, searchapi.SemanticStatus, error) {
	select {
	case <-i.loaded:
	case <-ctx.Done():
		return nil, searchapi.SemanticStatus{}, ctx.Err()
	}
	if i.loadErr != nil {
		return nil, searchapi.SemanticStatus{}, i.loadErr
	}
	text := stripCodePrefix(req.Query)
	var query []float32
	var wordsOnly string
	if req.Meaning && strings.TrimSpace(text) != "" {
		query, wordsOnly = i.queryVector(ctx, req.Model, text)
	}
	if query != nil {
		i.touch()
		if err := i.cat.loadVectors(ctx, i.st); err != nil {
			return nil, searchapi.SemanticStatus{}, err
		}
	}
	var allowed func(string) bool
	if roots := cleanRoots(req.Roots); len(roots) > 0 {
		allowed = func(p string) bool {
			return slices.ContainsFunc(roots, func(root string) bool {
				return p == root || strings.HasPrefix(p, root+"/")
			})
		}
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	hits, err := newSearcher(i.st, i.cat).search(ctx, text, query, allowed, limit, req.PrefixLast)
	if err != nil {
		return nil, searchapi.SemanticStatus{}, err
	}
	hits = slices.DeleteFunc(hits, func(h searchapi.SemanticResult) bool {
		_, err := i.cfg.FS.Stat(h.Path)
		return err != nil
	})
	return hits, i.Status().semantic(wordsOnly), nil
}

// cleanRoots makes the request's roots workspace-relative prefixes; the
// whole workspace is none.
func cleanRoots(roots []string) []string {
	var out []string
	for _, r := range roots {
		r = strings.Trim(path.Clean(filepath.ToSlash(r)), "/")
		if r == "." || r == "" {
			return nil
		}
		out = append(out, r)
	}
	return out
}

// queryVector embeds text as a query with model, or returns nil and why
// meaning search cannot answer it: model is not downloaded, the index was
// not built with its vectors, or the query could not be encoded.
func (i *Index) queryVector(ctx context.Context, model llmapi.ModelEntry, text string) ([]float32, string) {
	entry := model
	if entry.BaseURL == "" {
		e, ok := i.svc.registry.Get(ctx, model.Name)
		if !ok {
			i.setEncoder(encoderNotDownloaded)
			i.notifyOnce("Meaning search needs the embedding model: " + i.svc.downloadHint())
			return nil, "the embedding model is not downloaded: " + i.svc.downloadHint()
		}
		entry = e
	}
	embed := i.svc.embed(entry)
	source := embed.Info().VectorSource()
	indexed := i.st.meta().VectorSource
	i.mu.Lock()
	stalled := i.stalled != ""
	i.mu.Unlock()
	if source != indexed {
		if entry.Name == i.svc.cfg.Model {
			// The indexer adopts the model on its next pass.
			i.Rescan()
		}
		if indexed == "" {
			return nil, "the search index has no vectors yet"
		}
		why := fmt.Sprintf("the search index was built with %s, not %s", indexed, source)
		i.notifyOnce("Meaning search is off: " + why)
		return nil, why
	}
	if stalled {
		i.Rescan()
	}
	key := source + "\x00" + text
	i.mu.Lock()
	cached, ok := i.queries[key]
	i.mu.Unlock()
	if ok {
		return cached, ""
	}
	vecs, err := embed.EmbedTexts(ctx, []string{queryPrompt(text)})
	if err != nil || len(vecs) != 1 || len(vecs[0]) != dims {
		if err == nil {
			err = errors.New("no vector for the query")
		}
		if ctx.Err() == nil {
			log.Warnf("llmsearch: embed query: %v", err)
		}
		i.setEncoder(encoderUnavailable + ": " + err.Error())
		return nil, "the query could not be encoded: " + err.Error()
	}
	i.setEncoder(encoderReady)
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, ok := i.queries[key]; !ok {
		if len(i.queryOrder) >= queryCacheSize {
			delete(i.queries, i.queryOrder[0])
			i.queryOrder = i.queryOrder[1:]
		}
		i.queries[key] = vecs[0]
		i.queryOrder = append(i.queryOrder, key)
	}
	return vecs[0], ""
}

func (i *Index) setEncoder(state string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.encoder = state
}

func (i *Index) notifyOnce(msg string) {
	i.schedule(func() { _, _ = i.cfg.Notify.NotifyOnce(browserapi.LevelInfo, "%s", msg) })
}

// touch keeps the vectors in memory for another IdleTimeout.
func (i *Index) touch() {
	timeout := i.cfg.Options.IdleTimeout
	if timeout <= 0 {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return
	}
	if i.idle == nil {
		i.idle = time.AfterFunc(timeout, func() { debug.CapturePanicReport(i.cat.releaseVectors) })
		return
	}
	i.idle.Reset(timeout)
}
