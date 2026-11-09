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

// Package codesearch searches the files of a workspace for a regular
// expression, as grep does, through a trigram index that narrows the
// files a search reads to those that may match.
package codesearch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp/syntax"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/codesearch/index"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/searchapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
)

const (
	// settleDelay lets a burst of disk events gather before the index
	// looks at them.
	settleDelay = 100 * time.Millisecond
	// rebuildAfter is how many files may change before the index is
	// built again: until then, every search reads them.
	rebuildAfter = 256
)

var errClosed = errors.New("codesearch: index closed")

// EditorEvents returns the editor event types an Index must be subscribed
// to in order to keep the index fresh: in-editor saves and the out-of-band
// disk events.
func EditorEvents() []textapi.EventType {
	return []textapi.EventType{
		textapi.EventTypeFlush,
		textapi.EventTypeChange,
		textapi.EventTypeCreate,
		textapi.EventTypeRemove,
		textapi.EventTypeRename,
	}
}

// IndexFile returns where the index of the workspace root persists in
// the folder dir.
func IndexFile(dir string, root workspaceapi.URI) string {
	sum := sha256.Sum256([]byte(root.String()))
	return filepath.Join(dir, hex.EncodeToString(sum[:16])+".idx")
}

// CleanupWorkspaceHook returns a hook that deletes the index a workspace
// has in the folder dir, and what an interrupted build of it left.
func CleanupWorkspaceHook(dir string) func(ctx context.Context, root workspaceapi.URI) error {
	return func(_ context.Context, root workspaceapi.URI) error {
		files, err := filepath.Glob(IndexFile(dir, root) + "*")
		if err != nil {
			return err
		}
		var errs []error
		for _, f := range files {
			if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
}

// Config is what an Index is opened with.
type Config struct {
	Root workspaceapi.URI
	FS   workspaceapi.FileSystem
	// File is where the index of Root persists; see IndexFile. Empty
	// keeps no index: every search reads every file.
	File string
	// ExcludedFolders are workspace-relative folders never searched.
	ExcludedFolders []string
	// Notify tells the user of every build of the index. Its calls go
	// through Schedule, which runs a closure on the editor event loop.
	Notify   browserapi.Notifications
	Schedule func(func()) bool
}

// Status describes an Index.
type Status struct {
	// Ready is set once searches go through the index. Until then, and
	// without an index, they read every file.
	Ready bool
	// Building is set while the index is built.
	Building bool
	// Files is how many files the index lists, and Changed how many of
	// the workspace's files changed since it was built: every search
	// reads those.
	Files, Changed int
	// Err is why the index could not be built, if it could not.
	Err string
}

// Index searches one workspace. It keeps a trigram index of the
// workspace's files in Config.File, which it reads, or builds in the
// background, when opened. The files that change after are read by
// every search until enough of them changed that the index is built
// again. It implements text.EventHandler to learn of the changes, and
// io.Closer.
type Index struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc

	filterOnce sync.Once
	flt        *filter

	mu    sync.Mutex
	base  *index.Index
	names []string
	// unindexed files were omitted by the indexer's text heuristics or
	// could not be read. Search them directly without repeatedly rebuilding.
	unindexed map[string]struct{}
	// dirty are the files that may differ from what base has of them.
	dirty map[string]struct{}
	// since collects the files that change while the index is built:
	// they are what is dirty once it is.
	since map[string]struct{}
	// pending are the paths of the events the worker has yet to look at.
	pending map[string]struct{}
	// loaded is set once the index was read or built for the first time.
	loaded bool
	// ready is set while base can be searched.
	ready    bool
	busy     bool
	building bool
	rebuild  bool
	closed   bool
	err      string
	// updated is closed (and replaced) on every state change so that Wait
	// can block without polling.
	updated chan struct{}
	wake    chan struct{}
	done    chan struct{}
}

var _ io.Closer = (*Index)(nil)

// Open opens the index of cfg.Root, in the background. It panics when cfg
// has no Notify or Schedule.
func Open(cfg Config) *Index {
	if cfg.Notify == nil || cfg.Schedule == nil {
		panic("codesearch: Open needs notifications and a scheduler")
	}
	ctx, cancel := context.WithCancel(context.Background())
	i := &Index{
		cfg:     cfg,
		ctx:     ctx,
		cancel:  cancel,
		dirty:   map[string]struct{}{},
		pending: map[string]struct{}{},
		updated: make(chan struct{}),
		wake:    make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
	go debug.CapturePanicReport(i.run)
	return i
}

func (i *Index) filter() *filter {
	i.filterOnce.Do(func() { i.flt = newFilter(i.cfg.FS, i.cfg.ExcludedFolders) })
	return i.flt
}

func (i *Index) run() {
	defer close(i.done)
	if i.cfg.File != "" {
		i.load()
	}
	i.mu.Lock()
	i.loaded = true
	i.wakeLocked()
	i.mu.Unlock()
	if i.cfg.File == "" {
		return
	}
	for {
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
		i.update()
	}
}

// load reads the index, or builds it when there is none.
func (i *Index) load() {
	if err := os.MkdirAll(filepath.Dir(i.cfg.File), 0o755); err != nil {
		i.fail(err)
		return
	}
	if !i.reconcile() {
		i.build()
	}
}

// reconcile reads the index and marks files modified since it was built
// dirty. Files it does not list are searched directly: they may be new or
// omitted by the indexer. It reports whether there was an index to read.
func (i *Index) reconcile() bool {
	info, err := os.Stat(i.cfg.File)
	if err != nil {
		return false
	}
	base, err := index.Open(i.cfg.File)
	if err != nil {
		log.Warnf("codesearch: open the index of %s: %v", i.cfg.Root, err)
		return false
	}
	names, err := i.readNames(base)
	if err != nil {
		_ = base.Close()
		log.Warnf("codesearch: read the index of %s: %v", i.cfg.Root, err)
		return false
	}
	files, err := i.filter().walk(i.ctx, i.cfg.FS, []string{"."})
	if i.ctx.Err() != nil {
		_ = base.Close()
		return true
	}
	if err != nil {
		log.Warnf("codesearch: list the files of %s: %v", i.cfg.Root, err)
	}
	known := make(map[string]struct{}, len(names))
	for _, name := range names {
		known[name] = struct{}{}
	}
	builtAt := info.ModTime()
	unindexed := map[string]struct{}{}
	var dirty []string
	for _, rel := range files {
		if _, ok := known[rel]; !ok {
			unindexed[rel] = struct{}{}
		}
		if st, err := i.cfg.FS.Stat(rel); err != nil || !st.ModTime().Before(builtAt) {
			dirty = append(dirty, rel)
		}
	}
	i.mu.Lock()
	i.base, i.names, i.ready = base, names, true
	i.unindexed = unindexed
	for _, rel := range dirty {
		i.dirty[rel] = struct{}{}
	}
	stale := len(i.dirty) > rebuildAfter
	i.mu.Unlock()
	if stale {
		i.build()
	}
	return true
}

// update looks at the paths of the events since it last did, and
// builds the index again when too many files changed.
func (i *Index) update() {
	i.mu.Lock()
	paths := slices.Sorted(maps.Keys(i.pending))
	clear(i.pending)
	i.mu.Unlock()

	// skipped are not searched: those the filter leaves out, and folders,
	// whose files are found instead.
	var skipped, found []string
	for _, rel := range paths {
		if i.filter().skips(rel) {
			skipped = append(skipped, rel)
			continue
		}
		if info, err := i.cfg.FS.Stat(rel); err == nil && info.IsDir() {
			skipped = append(skipped, rel)
			files, _ := i.filter().walk(i.ctx, i.cfg.FS, []string{rel})
			found = append(found, files...)
		}
	}

	i.mu.Lock()
	for _, rel := range skipped {
		delete(i.dirty, rel)
		delete(i.since, rel)
	}
	for _, rel := range found {
		i.dirty[rel] = struct{}{}
		if i.since != nil {
			i.since[rel] = struct{}{}
		}
	}
	stale := i.rebuild || (i.base != nil && len(i.dirty) > rebuildAfter)
	i.rebuild = false
	i.mu.Unlock()

	if stale {
		i.build()
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.pending) == 0 {
		i.busy = false
	}
	i.wakeLocked()
}

// build builds the index of every file, and has searches go through it,
// telling the user in a notification.
func (i *Index) build() {
	started := time.Now()
	i.mu.Lock()
	i.building = true
	i.since = map[string]struct{}{}
	i.wakeLocked()
	i.mu.Unlock()

	prog := &buildProgress{notify: i.cfg.Notify, schedule: i.cfg.Schedule}
	tmp, paths, err := i.write(started, prog)
	files, err := i.finish(tmp, paths, err)
	switch {
	case err == nil:
		prog.done(files)
	case i.ctx.Err() == nil:
		prog.stop(err)
	}
}

// finish has searches go through the index write built in tmp, and
// returns how many files it lists, or why it cannot.
func (i *Index) finish(tmp string, files []string, err error) (int, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	defer i.wakeLocked()
	i.building = false
	since := i.since
	i.since = nil
	if err == nil && i.ctx.Err() != nil {
		_ = os.Remove(tmp)
		err = i.ctx.Err()
	}
	if err != nil {
		if i.ctx.Err() == nil {
			log.Errorf("codesearch: build the index of %s: %v", i.cfg.Root, err)
			i.err = err.Error()
		}
		return 0, err
	}
	if i.base != nil {
		// Windows does not replace a file that is open.
		_ = i.base.Close()
		i.base, i.names, i.ready = nil, nil, false
	}
	if err := i.install(tmp); err != nil {
		_ = os.Remove(tmp)
		log.Errorf("codesearch: install the index of %s: %v", i.cfg.Root, err)
		i.err = err.Error()
		return 0, err
	}
	i.dirty = since
	known := make(map[string]struct{}, len(i.names))
	for _, name := range i.names {
		known[name] = struct{}{}
	}
	i.unindexed = map[string]struct{}{}
	for _, rel := range files {
		if _, ok := known[rel]; !ok {
			i.unindexed[rel] = struct{}{}
		}
	}
	i.err = ""
	return len(i.names), nil
}

// install moves the index built in tmp to where it persists, and opens
// it. Callers hold mu.
func (i *Index) install(tmp string) error {
	if err := os.Rename(tmp, i.cfg.File); err != nil {
		return err
	}
	base, err := index.Open(i.cfg.File)
	if err != nil {
		return err
	}
	names, err := i.readNames(base)
	if err != nil {
		_ = base.Close()
		return err
	}
	i.base, i.names, i.ready = base, names, true
	return nil
}

func (i *Index) readNames(base *index.Index) ([]string, error) {
	ids, err := base.PostingQuery(&index.Query{Op: index.QAll})
	if err != nil {
		return nil, err
	}
	paths, err := base.Names(0, len(ids))
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ids))
	for p := range paths {
		if err := i.ctx.Err(); err != nil {
			return nil, err
		}
		name := p.String()
		if name == "" || len(names) > 0 && comparePaths(names[len(names)-1], name) >= 0 {
			return nil, &index.CorruptError{File: i.cfg.File}
		}
		names = append(names, name)
	}
	// The iterator stops at a malformed name without reporting an error.
	if len(names) != len(ids) {
		return nil, &index.CorruptError{File: i.cfg.File}
	}
	return names, nil
}

// write writes the index of every file to a temporary file, and returns
// it and all attempted paths, including files the indexer omitted. The
// index is dated started so files modified after are known to have changed.
// It reports its progress to prog once it knows there are files to index.
func (i *Index) write(started time.Time, prog *buildProgress) (tmp string, files []string, err error) {
	files, err = i.filter().walk(i.ctx, i.cfg.FS, []string{"."})
	if i.ctx.Err() != nil {
		return "", nil, i.ctx.Err()
	}
	if err != nil {
		log.Warnf("codesearch: list the files of %s: %v", i.cfg.Root, err)
	}
	if len(files) > 0 {
		prog.start()
	}
	tmp = fmt.Sprintf("%s.%d.new", i.cfg.File, os.Getpid())
	w, err := index.Create(tmp)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = w.Close() }()
	for n, rel := range files {
		if err := i.ctx.Err(); err != nil {
			return "", nil, err
		}
		prog.file(n, len(files), rel)
		f, err := i.cfg.FS.OpenFile(rel, os.O_RDONLY, 0)
		if err != nil {
			continue
		}
		err = w.Add(i.ctx, rel, io.LimitReader(f, maxFileLen+1))
		_ = f.Close()
		if err != nil {
			if i.ctx.Err() != nil {
				return "", nil, i.ctx.Err()
			}
			log.Warnf("codesearch: index %s: %v", rel, err)
		}
	}
	if err := w.Flush(i.ctx); err != nil {
		return "", nil, err
	}
	if err := os.Chtimes(tmp, started, started); err != nil {
		_ = os.Remove(tmp)
		return "", nil, err
	}
	return tmp, files, nil
}

// buildProgress reports a build of the index in one notification. Its
// methods are called by the build; the notification is posted and
// updated on the editor event loop, where only id is used.
type buildProgress struct {
	notify   browserapi.Notifications
	schedule func(func()) bool
	id       string
	dir      string
}

func (p *buildProgress) start() {
	p.schedule(func() {
		id, err := p.notify.Notify(browserapi.LevelInfo, "Indexing the workspace for regexp search")
		if err == nil {
			p.id = id
		}
	})
}

// file reports that the build reads rel, the file at index done of total,
// when rel is in another folder than the file before it.
func (p *buildProgress) file(done, total int, rel string) {
	dir := path.Dir(rel)
	if dir == p.dir {
		return
	}
	p.dir = dir
	p.schedule(func() {
		if p.id != "" {
			_ = p.notify.UpdateNotificationProgress(p.id,
				"Indexing for regexp search: "+dir, int64(done), int64(total))
		}
	})
}

// done finishes the notification of a build whose index lists files.
func (p *buildProgress) done(files int) {
	p.schedule(func() {
		if p.id == "" {
			return
		}
		noun := "files"
		if files == 1 {
			noun = "file"
		}
		total := int64(max(files, 1))
		_ = p.notify.UpdateNotificationProgress(p.id,
			fmt.Sprintf("Indexed %d %s for regexp search", files, noun), total, total)
	})
}

// stop finishes the notification of a build that failed, since a
// progress notification otherwise stays open until it is complete.
func (p *buildProgress) stop(err error) {
	p.schedule(func() {
		if p.id != "" {
			_ = p.notify.UpdateNotificationProgress(p.id,
				"Indexing for regexp search stopped: "+err.Error(), 1, 1)
		}
	})
}

func (i *Index) fail(err error) {
	log.Errorf("codesearch: index %s: %v", i.cfg.Root, err)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.err = err.Error()
}

// wakeLocked wakes every Wait blocked on a state change. Callers hold mu.
func (i *Index) wakeLocked() {
	close(i.updated)
	i.updated = make(chan struct{})
}

// Handle implements text.EventHandler. It never blocks: an event only
// marks its path as changed, for searches to read until the index is
// built again.
func (i *Index) Handle(_ context.Context, ev textapi.Event) bool {
	switch ev.Type {
	case textapi.EventTypeFlush, textapi.EventTypeChange,
		textapi.EventTypeCreate, textapi.EventTypeRemove, textapi.EventTypeRename:
	default:
		return false
	}
	if i.cfg.File == "" || !workspaceapi.HasPrefix(ev.URI, i.cfg.Root) {
		return false
	}
	rel := path.Clean(filepath.ToSlash(workspaceapi.RelPath(i.cfg.Root, ev.URI)))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return false
	}
	i.dirty[rel] = struct{}{}
	if i.since != nil {
		i.since[rel] = struct{}{}
	}
	i.pending[rel] = struct{}{}
	i.busy = true
	i.wakeLocked()
	select {
	case i.wake <- struct{}{}:
	default:
	}
	return false
}

// RegexpSearch returns the lines of the files under req.Roots that match
// req, by file in index order and by line. req.Roots are
// workspace-relative. It fails when req.Pattern is not a valid regular
// expression or an Include glob is malformed. The files are read as the
// iterator is.
//
// Like ripgrep, a root the workspace leaves out of searches (hidden,
// excluded or ignored) is searched all the same, and only what is below
// it is filtered. The index does not hold those files: they are all
// read.
func (i *Index) RegexpSearch(
	ctx context.Context, req searchapi.RegexpRequest,
) (iterator.Iterator[searchapi.RegexpResult], error) {
	pattern, re, err := compile(req)
	if err != nil {
		return nil, err
	}
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil, err
	}
	var indexed, direct []string
	for _, root := range cleanRoots(req.Roots) {
		if i.filter().leavesOut(root) {
			direct = append(direct, root)
		} else {
			indexed = append(indexed, root)
		}
	}
	paths, err := i.indexedCandidates(ctx, index.RegexpQuery(parsed), outermost(indexed))
	if err != nil {
		return nil, err
	}
	if len(direct) > 0 {
		files, err := i.walk(ctx, outermost(direct))
		if err != nil {
			return nil, err
		}
		paths = append(paths, files...)
	}
	slices.SortFunc(paths, comparePaths)
	paths, err = included(slices.Compact(paths), req.Include)
	if err != nil {
		return nil, err
	}
	return newMatcher(i.cfg.FS, re, req.Limit, req.MaxPerFile, func(context.Context) (string, bool, error) {
		if len(paths) == 0 {
			return "", false, nil
		}
		rel := paths[0]
		paths = paths[1:]
		return rel, true, nil
	}), nil
}

// indexedCandidates returns the files under roots that may match q,
// through the index when there is one, and else all of them.
func (i *Index) indexedCandidates(ctx context.Context, q *index.Query, roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	paths, unchecked, ok, err := i.candidates(q, roots)
	if err != nil {
		return nil, err
	}
	if !ok {
		return i.walk(ctx, roots)
	}
	return append(paths, i.check(ctx, unchecked, roots)...), nil
}

// walk lists the files under roots. Only a canceled ctx fails it: a
// folder it cannot read has no files.
func (i *Index) walk(ctx context.Context, roots []string) ([]string, error) {
	files, err := i.filter().walk(ctx, i.cfg.FS, roots)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		log.Warnf("codesearch: list the files of %s: %v", i.cfg.Root, err)
	}
	return files, nil
}

// candidates returns the files under roots that may match q: those the
// index says may, and those that changed since it was built. It reports
// false when there is no index to ask. The paths of the events the worker
// has yet to look at are unchecked: they may be folders, or left out by
// the filter.
func (i *Index) candidates(q *index.Query, roots []string) (paths, unchecked []string, ok bool, err error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil, nil, false, errClosed
	}
	if !i.ready {
		return nil, nil, false, nil
	}
	ids, err := i.base.PostingQuery(q)
	if err != nil {
		var corrupt *index.CorruptError
		if !errors.As(err, &corrupt) {
			return nil, nil, false, err
		}
		log.Errorf("codesearch: search the index of %s: %v", i.cfg.Root, err)
		i.ready, i.rebuild = false, true
		i.busy = true
		i.wakeLocked()
		select {
		case i.wake <- struct{}{}:
		default:
		}
		return nil, nil, false, nil
	}
	paths = make([]string, 0, len(ids)+len(i.dirty))
	for _, id := range ids {
		name := i.names[id]
		if _, changed := i.dirty[name]; !changed && under(name, roots) {
			paths = append(paths, name)
		}
	}
	for rel := range i.dirty {
		if _, ok := i.pending[rel]; ok {
			unchecked = append(unchecked, rel)
		} else if under(rel, roots) {
			paths = append(paths, rel)
		}
	}
	for rel := range i.unindexed {
		if _, changed := i.dirty[rel]; !changed && under(rel, roots) {
			paths = append(paths, rel)
		}
	}
	return paths, unchecked, true, nil
}

// check returns the files under roots of the unchecked paths of
// candidates: those the filter does not leave out, and those in the ones
// that are folders.
func (i *Index) check(ctx context.Context, unchecked, roots []string) []string {
	var out []string
	for _, rel := range unchecked {
		if i.filter().skips(rel) || !under(rel, roots) && !slices.ContainsFunc(roots, func(root string) bool {
			return under(root, []string{rel})
		}) {
			continue
		}
		info, err := i.cfg.FS.Stat(rel)
		switch {
		case err != nil:
		case info.IsDir():
			files, _ := i.filter().walk(ctx, i.cfg.FS, []string{rel})
			for _, f := range files {
				if under(f, roots) {
					out = append(out, f)
				}
			}
		default:
			if under(rel, roots) {
				out = append(out, rel)
			}
		}
	}
	return out
}

// Status describes the index now.
func (i *Index) Status() Status {
	i.mu.Lock()
	defer i.mu.Unlock()
	return Status{
		Ready:    i.ready,
		Building: i.building,
		Files:    len(i.names),
		Changed:  len(i.dirty),
		Err:      i.err,
	}
}

// Wait blocks until the index was read or built, and the events it was
// given were looked at. It is meant for tests and diagnostics.
func (i *Index) Wait(ctx context.Context) error {
	for {
		i.mu.Lock()
		if i.closed {
			i.mu.Unlock()
			return errClosed
		}
		if i.loaded && !i.busy && !i.building {
			i.mu.Unlock()
			return nil
		}
		updated := i.updated
		i.mu.Unlock()
		select {
		case <-updated:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close stops the index from being kept up to date, and closes it. It
// is safe to call more than once.
func (i *Index) Close() error {
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		<-i.done
		return nil
	}
	i.closed = true
	i.wakeLocked()
	i.mu.Unlock()
	i.cancel()
	<-i.done
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.base == nil {
		return nil
	}
	err := i.base.Close()
	i.base, i.names, i.ready = nil, nil, false
	return err
}
