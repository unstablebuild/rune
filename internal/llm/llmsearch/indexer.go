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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

// Options are the indexing settings. CodeStretch changes what vectors are
// made of, so it is part of the index's fingerprint: an index never mixes
// settings.
type Options struct {
	// CodeStretch is the most characters a stretch of code holds (whole
	// lines; a longer line is a stretch of its own).
	CodeStretch int
	// MaxCodeBytes and MaxNotebookBytes bound the files read: bigger ones
	// were written by a program.
	MaxCodeBytes     int64
	MaxNotebookBytes int64
	// ExcludedFolders are workspace-relative folders never read.
	ExcludedFolders []string
	// ExcludedExtensions are file extensions never read ("sql", ".csv").
	ExcludedExtensions []string
	// IdleTimeout is how long the vectors stay in memory after the last
	// search.
	IdleTimeout time.Duration
}

// DefaultOptions returns DigUp's code index settings.
func DefaultOptions() Options {
	return Options{
		CodeStretch:      1800,
		MaxCodeBytes:     1 << 20,
		MaxNotebookBytes: 20 << 20,
		IdleTimeout:      2 * time.Minute,
	}
}

func (o Options) fingerprint() string { return fmt.Sprintf("code1 lines%d", o.CodeStretch) }

// insertChunk files are inserted per storage round.
const insertChunk = 1024

// embedderError is a failure of the embedder itself, not of one file: the
// run stops and the files stay pending.
type embedderError struct{ err error }

func (e *embedderError) Error() string { return "llmsearch: embedder: " + e.err.Error() }
func (e *embedderError) Unwrap() error { return e.err }

// indexer keeps a workspace's index in step with its files: sync finds
// what changed, run reads and embeds what is pending.
type indexer struct {
	st *store
	fs workspaceapi.FileSystem
	// title is the workspace folder's name, the start of every file's
	// title for the model.
	title string
	opts  Options
	cat   *catalog
}

// load fills the catalog from the store, deleting the segments no file
// owns.
func (x *indexer) load(ctx context.Context) error {
	files, err := x.st.listFiles(ctx)
	if err != nil {
		return fmt.Errorf("llmsearch: list files: %w", err)
	}
	owner := map[uint32]uint32{}
	for _, f := range files {
		for _, id := range f.Segments {
			owner[id] = f.ID
		}
	}
	var orphans []uint32
	x.cat.mu.Lock()
	defer x.cat.mu.Unlock()
	err = x.st.streamSegments(ctx, func(d segmentDoc) error {
		if d.File == 0 || owner[d.ID] != d.File {
			orphans = append(orphans, d.ID)
			return nil
		}
		x.cat.segs[d.ID] = segInfo{file: d.File, kind: d.Kind, loc: d.Loc, hasVec: d.HasVec}
		x.cat.kw.load(d.ID, d.File, d.Name, d.Body)
		return nil
	})
	if err != nil {
		return fmt.Errorf("llmsearch: load segments: %w", err)
	}
	x.cat.kw.sortPostings()
	for _, f := range files {
		x.cat.putFileLocked(f)
	}
	if len(orphans) > 0 {
		if err := x.st.deleteVectors(ctx, orphans); err != nil {
			return err
		}
		if err := x.st.deleteSegments(ctx, orphans); err != nil {
			return err
		}
	}
	return nil
}

// adopt makes embed's vectors (none when nil) the index's: an index made
// with anything else is reset first. It reports whether it was.
func (x *indexer) adopt(ctx context.Context, embed Embedder) (reset bool, _ error) {
	source := ""
	if embed != nil {
		source = embed.Info().VectorSource()
	}
	fingerprint := source + " | " + x.opts.fingerprint()
	m := x.st.meta()
	if m.Fingerprint == fingerprint {
		return false, nil
	}
	if m.Fingerprint != "" {
		if err := x.resetAll(ctx); err != nil {
			return false, err
		}
		reset = true
	}
	return reset, x.st.updateMeta(ctx, func(m *metaDoc) {
		m.Fingerprint, m.VectorSource = fingerprint, source
	})
}

// resetAll queues every file again: every segment but the name rows goes,
// with every vector, and the fingerprint is forgotten last, so that an
// interrupted reset is done again.
func (x *indexer) resetAll(ctx context.Context) error {
	x.cat.mu.Lock()
	defer x.cat.mu.Unlock()
	var drop, vectors []uint32
	var files []fileDoc
	dropped := map[uint32][]uint32{}
	for _, f := range x.cat.files {
		names, others, vecs := x.cat.splitLocked(f)
		drop, vectors = append(drop, others...), append(vectors, vecs...)
		dropped[f.ID] = others
		nf := *f
		nf.State, nf.Detail, nf.Lines, nf.Cells, nf.Segments = statePending, "", 0, 0, names
		files = append(files, nf)
	}
	if err := x.st.deleteVectors(ctx, vectors); err != nil {
		return err
	}
	if err := x.st.deleteSegments(ctx, drop); err != nil {
		return err
	}
	if err := x.st.putFiles(ctx, files); err != nil {
		return err
	}
	for _, f := range files {
		x.cat.dropSegmentsLocked(f.ID, dropped[f.ID])
		x.cat.putFileLocked(f)
	}
	if x.cat.vec != nil {
		x.cat.vec = &vectorMatrix{}
	}
	return x.st.updateMeta(ctx, func(m *metaDoc) { m.Fingerprint, m.VectorSource = "", "" })
}

// syncSummary is what sync found.
type syncSummary struct {
	added, changed, unchanged, moved, removed int
	// queued are the files added or changed, to index first.
	queued []uint32
	report crawlReport
	// complete is false when a walk did not finish: no file was removed.
	complete bool
}

// sync crawls roots (workspace-relative files or folders, "." for all)
// and reconciles the index with them: new files are queued, changed ones
// queued again, moved ones (same inode, size and time at a new path) only
// re-pathed, and the ones gone removed.
func (x *indexer) sync(ctx context.Context, roots []string) (syncSummary, error) {
	cands, report, walkErr := crawl(ctx, x.fs, roots, x.opts)
	if err := ctx.Err(); err != nil {
		return syncSummary{}, err
	}
	if walkErr != nil {
		log.Warnf("llmsearch: crawl %v: %v", roots, walkErr)
	}
	sum := syncSummary{report: report, complete: walkErr == nil}
	under := func(p string) bool {
		for _, root := range roots {
			if root = path.Clean(root); root == "." || p == root || strings.HasPrefix(p, root+"/") {
				return true
			}
		}
		return false
	}
	seen := make(map[string]bool, len(cands))
	for _, c := range cands {
		seen[c.path] = true
	}
	files := x.cat.snapshot()
	byPath := make(map[string]fileDoc, len(files))
	byInode := map[[2]int64]fileDoc{}
	for _, f := range files {
		byPath[f.Path] = f
		if f.Inode != 0 {
			byInode[[2]int64{f.Inode, f.Device}] = f
		}
	}
	gone := func(p string) bool {
		if under(p) {
			return !seen[p]
		}
		_, err := x.fs.Stat(p)
		return errors.Is(err, fs.ErrNotExist)
	}

	movedFrom := map[string]bool{}
	var inserts []candidate
	for _, c := range cands {
		if f, ok := byPath[c.path]; ok {
			if f.Size == c.size && f.ModTime == c.modTime {
				sum.unchanged++
				continue
			}
			if err := x.requeue(ctx, f, c); err != nil {
				return sum, err
			}
			sum.changed++
			sum.queued = append(sum.queued, f.ID)
			continue
		}
		f, ok := byInode[[2]int64{int64(c.inode), int64(c.device)}]
		if ok && c.inode != 0 && !seen[f.Path] && !movedFrom[f.Path] &&
			f.Size == c.size && f.ModTime == c.modTime && gone(f.Path) {
			if err := x.move(ctx, f, c.path); err != nil {
				return sum, err
			}
			movedFrom[f.Path] = true
			sum.moved++
			continue
		}
		inserts = append(inserts, c)
	}
	for chunk := range slices.Chunk(inserts, insertChunk) {
		ids, err := x.insert(ctx, chunk)
		if err != nil {
			return sum, err
		}
		sum.added += len(ids)
		sum.queued = append(sum.queued, ids...)
	}
	if !sum.complete {
		return sum, nil
	}
	var removed []uint32
	for _, f := range files {
		if under(f.Path) && !seen[f.Path] && !movedFrom[f.Path] {
			removed = append(removed, f.ID)
		}
	}
	if err := x.remove(ctx, removed); err != nil {
		return sum, err
	}
	sum.removed = len(removed)
	return sum, nil
}

// nameRow is a file's row for keyword search by its name and folders.
func nameRow(id, file uint32, rel string) segmentDoc {
	dir, base := path.Split(rel)
	folders := strings.Join(strings.FieldsFunc(dir, func(r rune) bool { return r == '/' }), " ")
	return segmentDoc{ID: id, File: file, Kind: segmentName, Name: Normalize(base), Body: Normalize(folders)}
}

// insert adds files as pending, each with its name row.
func (x *indexer) insert(ctx context.Context, cands []candidate) ([]uint32, error) {
	fileIDs, err := x.st.allocFileIDs(ctx, len(cands))
	if err != nil {
		return nil, err
	}
	segIDs, err := x.st.allocSegmentIDs(ctx, len(cands))
	if err != nil {
		return nil, err
	}
	segs := make([]segmentDoc, len(cands))
	files := make([]fileDoc, len(cands))
	for i, c := range cands {
		segs[i] = nameRow(segIDs[i], fileIDs[i], c.path)
		files[i] = fileDoc{
			ID: fileIDs[i], Path: c.path, Kind: kindCode, Size: c.size, ModTime: c.modTime,
			Inode: int64(c.inode), Device: int64(c.device), State: statePending, Segments: []uint32{segIDs[i]},
		}
	}
	if err := x.st.putSegments(ctx, segs); err != nil {
		return nil, err
	}
	if err := x.st.putFiles(ctx, files); err != nil {
		return nil, err
	}
	x.cat.mu.Lock()
	defer x.cat.mu.Unlock()
	for i := range cands {
		x.cat.addSegmentLocked(segs[i])
		x.cat.putFileLocked(files[i])
	}
	return fileIDs, nil
}

// requeue drops what was read from a file that changed and queues it as
// it is now.
func (x *indexer) requeue(ctx context.Context, f fileDoc, c candidate) error {
	x.cat.mu.Lock()
	defer x.cat.mu.Unlock()
	names, others, vectors := x.cat.splitLocked(&f)
	if err := x.st.deleteVectors(ctx, vectors); err != nil {
		return err
	}
	if err := x.st.deleteSegments(ctx, others); err != nil {
		return err
	}
	f.Size, f.ModTime, f.Inode, f.Device = c.size, c.modTime, int64(c.inode), int64(c.device)
	f.State, f.Detail, f.Lines, f.Cells, f.Segments = statePending, "", 0, 0, names
	if err := x.st.putFiles(ctx, []fileDoc{f}); err != nil {
		return err
	}
	x.cat.dropSegmentsLocked(f.ID, others)
	x.cat.putFileLocked(f)
	return nil
}

// move re-paths a file that was moved: nothing is read again.
func (x *indexer) move(ctx context.Context, f fileDoc, to string) error {
	x.cat.mu.Lock()
	defer x.cat.mu.Unlock()
	names, _, _ := x.cat.splitLocked(&f)
	rows := make([]segmentDoc, len(names))
	for i, id := range names {
		rows[i] = nameRow(id, f.ID, to)
	}
	if err := x.st.putSegments(ctx, rows); err != nil {
		return err
	}
	f.Path = to
	if err := x.st.putFiles(ctx, []fileDoc{f}); err != nil {
		return err
	}
	for _, row := range rows {
		x.cat.addSegmentLocked(row)
	}
	x.cat.putFileLocked(f)
	return nil
}

// remove deletes files with everything read from them.
func (x *indexer) remove(ctx context.Context, ids []uint32) error {
	if len(ids) == 0 {
		return nil
	}
	x.cat.mu.Lock()
	defer x.cat.mu.Unlock()
	var segs, vectors []uint32
	for _, id := range ids {
		f := x.cat.files[id]
		names, others, vecs := x.cat.splitLocked(f)
		segs = append(append(segs, names...), others...)
		vectors = append(vectors, vecs...)
	}
	if err := x.st.deleteVectors(ctx, vectors); err != nil {
		return err
	}
	if err := x.st.deleteSegments(ctx, segs); err != nil {
		return err
	}
	if err := x.st.deleteFiles(ctx, ids); err != nil {
		return err
	}
	for _, id := range ids {
		x.cat.dropFileLocked(id)
	}
	return nil
}

// pending lists the files waiting to be read (only those in only, when it
// is not nil), newest first.
func (x *indexer) pending(only []uint32) []fileDoc {
	var want map[uint32]bool
	if only != nil {
		want = make(map[uint32]bool, len(only))
		for _, id := range only {
			want[id] = true
		}
	}
	var out []fileDoc
	for _, f := range x.cat.snapshot() {
		if f.State == statePending && (want == nil || want[f.ID]) {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b fileDoc) int { return cmp.Compare(b.ModTime, a.ModTime) })
	return out
}

// runEvent reports one file run settled.
type runEvent struct {
	done, total int
	path        string
	state, note string
}

// prepared is a pending file read and cut, ready to embed.
type prepared struct {
	file fileDoc
	code codeFile
	err  error
}

// run reads, embeds and stores the pending files (only those in only, when
// it is not nil), newest first, calling onEvent with done 0 before the
// first file and then as each one settles. While one file is embedded the
// next is read. With a nil embed every stretch
// gets keyword search only. A problem with one file is recorded on it; an
// embedder that fails stops the run with an *embedderError, and the files
// not done stay pending.
func (x *indexer) run(ctx context.Context, embed Embedder, only []uint32, onEvent func(runEvent)) error {
	queue := x.pending(only)
	if len(queue) == 0 {
		return nil
	}
	pctx, cancel := context.WithCancel(ctx)
	jobs := make(chan prepared, 1)
	go debug.CapturePanicReport(func() {
		defer close(jobs)
		for _, f := range queue {
			select {
			case jobs <- x.prepare(f):
			case <-pctx.Done():
				return
			}
		}
	})
	defer func() {
		cancel()
		for range jobs {
		}
	}()
	done := 0
	onEvent(runEvent{total: len(queue)})
	for p := range jobs {
		state, note, err := x.commit(ctx, embed, p)
		if err != nil {
			return err
		}
		done++
		onEvent(runEvent{done: done, total: len(queue), path: p.file.Path, state: state, note: note})
	}
	return ctx.Err()
}

// prepare reads a file and cuts it into stretches.
func (x *indexer) prepare(f fileDoc) prepared {
	limit := x.opts.MaxCodeBytes
	if extension(f.Path) == "ipynb" {
		limit = x.opts.MaxNotebookBytes
	}
	file, err := x.fs.OpenFile(f.Path, os.O_RDONLY, 0)
	if err != nil {
		return prepared{file: f, err: err}
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return prepared{file: f, err: err}
	}
	if int64(len(data)) > limit {
		return prepared{file: f, err: leftOut(skipTooBig)}
	}
	code, err := readCode(data, f.Path, x.title+"/"+f.Path, x.opts)
	return prepared{file: f, code: code, err: err}
}

// commit embeds a prepared file's worded stretches and stores them: the
// vectors, then the segments, then the file as done.
func (x *indexer) commit(ctx context.Context, embed Embedder, p prepared) (state, note string, _ error) {
	if p.err != nil {
		state = stateFailed
		if _, ok := errors.AsType[leftOut](p.err); ok {
			state = stateSkipped
		}
		return state, p.err.Error(), x.finish(ctx, p.file, state, p.err.Error())
	}
	pieces := p.code.pieces
	if len(pieces) == 0 {
		return stateSkipped, "no code", x.finish(ctx, p.file, stateSkipped, "no code")
	}
	// Stretches that are mostly numbers and symbols (a table of
	// constants) get keyword search only.
	vecs := make([][]float32, len(pieces))
	worded := 0
	if embed != nil {
		var which []int
		var prompts []string
		for i, pc := range pieces {
			if HasWords(pc.text) {
				which = append(which, i)
				prompts = append(prompts, documentPrompt(p.code.title, pc.text))
			}
		}
		if len(prompts) > 0 {
			out, err := embed.EmbedTexts(ctx, prompts)
			if err != nil {
				if ctx.Err() != nil {
					return "", "", ctx.Err()
				}
				return "", "", &embedderError{err}
			}
			for j, i := range which {
				vecs[i] = out[j]
			}
		}
		worded = len(which)
	}
	ids, err := x.st.allocSegmentIDs(ctx, len(pieces))
	if err != nil {
		return "", "", err
	}
	kind := segmentLines
	if p.code.cells > 0 {
		kind = segmentCells
	}
	segs := make([]segmentDoc, len(pieces))
	var vectors []vectorDoc
	for i, pc := range pieces {
		body := Normalize(pc.text)
		segs[i] = segmentDoc{
			ID: ids[i], File: p.file.ID, Kind: kind, Loc: pc.first, LocEnd: pc.last,
			Excerpt: summary(pc.text), Body: body,
		}
		if body != pc.text {
			segs[i].Original = pc.text
		}
		if vecs[i] != nil {
			segs[i].HasVec = true
			vectors = append(vectors, vectorDoc{ID: ids[i], Vec: halfBytes(vecs[i])})
		}
	}

	x.cat.mu.Lock()
	defer x.cat.mu.Unlock()
	if err := x.st.putVectors(ctx, vectors); err != nil {
		return "", "", err
	}
	if err := x.st.putSegments(ctx, segs); err != nil {
		return "", "", err
	}
	f := p.file
	names, _, _ := x.cat.splitLocked(&f)
	f.State, f.Detail, f.Lines, f.Cells = stateDone, "", p.code.lines, p.code.cells
	f.Segments = append(names, ids...)
	if err := x.st.putFiles(ctx, []fileDoc{f}); err != nil {
		return "", "", err
	}
	for _, d := range segs {
		x.cat.addSegmentLocked(d)
	}
	if x.cat.vec != nil {
		for _, v := range vectors {
			if err := x.cat.vec.add(v.ID, f.ID, v.Vec); err != nil {
				return "", "", err
			}
		}
	}
	x.cat.putFileLocked(f)

	note = fmt.Sprintf("%d stretches, %d lines", len(pieces), p.code.lines)
	if p.code.cells > 0 {
		note = fmt.Sprintf("%d stretches, %d cells", len(pieces), p.code.cells)
	}
	if embed != nil && worded < len(pieces) {
		note += fmt.Sprintf(", %d for keywords only", len(pieces)-worded)
	}
	return stateDone, note, nil
}

// finish settles a file that has nothing to store but its state.
func (x *indexer) finish(ctx context.Context, f fileDoc, state, detail string) error {
	f.State, f.Detail = state, detail
	if err := x.st.putFiles(ctx, []fileDoc{f}); err != nil {
		return err
	}
	x.cat.mu.Lock()
	defer x.cat.mu.Unlock()
	x.cat.putFileLocked(f)
	return nil
}
