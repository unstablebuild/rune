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
	"maps"
	"slices"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// batchOps bounds the operations of one ApplyBatch call.
	batchOps = 256
	// windowPairs bounds a window by the (file, name) pairs it holds,
	// which is about what its staged writes weigh in memory.
	windowPairs = 100_000
)

// stagedFile is a file update with the symbols it contributes, by name.
type stagedFile struct {
	u       fileUpdate
	symbols map[string][]symbolLoc
}

// symbolWindow gathers the updates of several files so that each name
// they touch is read and written once, however many of them contribute
// to it: a scan sees most names from dozens of files, and a checkout
// changes hundreds of files at once.
type symbolWindow struct {
	files []stagedFile
	// contrib is what each file now contributes to a name: nil when it
	// no longer does.
	contrib map[string]map[string][]symbolLoc
	// forced names have a contributor stamped by another schema version,
	// so their markers are reconciled rather than trusted.
	forced map[string]bool
	pairs  int
}

func (w *symbolWindow) add(u fileUpdate) {
	if w.contrib == nil {
		w.contrib = map[string]map[string][]symbolLoc{}
		w.forced = map[string]bool{}
	}
	symbols := u.symbols()
	set := func(name string, locs []symbolLoc) {
		files := w.contrib[name]
		if files == nil {
			files = map[string][]symbolLoc{}
			w.contrib[name] = files
		}
		files[u.us] = locs
		if u.force {
			w.forced[name] = true
		}
		w.pairs++
	}
	for _, name := range u.oldNames {
		set(name, nil)
	}
	for name, locs := range symbols {
		set(name, locs)
	}
	w.files = append(w.files, stagedFile{u: u, symbols: symbols})
}

// full reports whether the window should be flushed before taking more.
func (w *symbolWindow) full() bool { return w.pairs >= windowPairs }

// flushWindow stores what the window holds and empties it: the symbol
// docs, one write per name however many files changed it, then the name
// markers, then the file records, which vouch for the rest and so go
// last. sctx is for the derived records and fctx for the file records,
// whose durability differs between the scan and a re-index. Without a
// batching store the files are applied one at a time, as staged.
func (p *Parser) flushWindow(sctx, fctx context.Context, w *symbolWindow) {
	defer func() { *w = symbolWindow{} }()
	if sctx.Err() != nil {
		return
	}
	if p.batch == nil {
		for _, f := range w.files {
			p.applyFile(fctx, f.u)
		}
		return
	}
	// A map iteration would make the batch — and so the bolt page
	// writes and any conflict ordering — differ run to run.
	names := slices.Sorted(maps.Keys(w.contrib))
	writes := make([]symbolWrite, 0, len(names))
	for _, name := range names {
		if sw, ok := p.stageSymbolWrite(sctx, name, w.contrib[name]); ok {
			writes = append(writes, sw)
		}
	}
	if !p.applySymbolWrites(sctx, fmt.Sprintf("%d files", len(w.files)), writes) {
		// The store turned out not to support batching after all;
		// replay the files through the per-operation path.
		for _, f := range w.files {
			p.applyFile(fctx, f.u)
		}
		return
	}
	var nameOps []storageapi.BatchOp
	for _, sw := range writes {
		if !sw.wrote || w.forced[sw.name] {
			p.syncName(sctx, sw.name, sw.merged)
			continue
		}
		switch isListed := listed(sw.merged); {
		case isListed && !sw.wasListed:
			nameOps = append(nameOps, storageapi.BatchOp{
				Type: storageapi.BatchSet, ID: sw.name, Doc: nameDoc{Name: sw.name},
			})
		case sw.wasListed && !isListed:
			nameOps = append(nameOps, storageapi.BatchOp{Type: storageapi.BatchDelete, ID: sw.name})
		}
	}
	p.applyOps(sctx, p.names, "name", nameOps)
	fileOps := make([]storageapi.BatchOp, 0, len(w.files))
	for _, f := range w.files {
		if f.u.removed {
			fileOps = append(fileOps, storageapi.BatchOp{Type: storageapi.BatchDelete, ID: f.u.us})
			continue
		}
		fileOps = append(fileOps, storageapi.BatchOp{
			Type: storageapi.BatchSet, ID: f.u.us, Doc: f.u.record(f.symbols),
		})
	}
	p.applyOps(fctx, p.files, "file record", fileOps)
}

// applyOps writes ops to part, batchOps at a time when it takes batches
// and one at a time otherwise. A failure is logged and the rest goes on:
// a record that did not land only costs a re-index of its file.
func (p *Parser) applyOps(
	ctx context.Context, part storageapi.Service, what string, ops []storageapi.BatchOp,
) {
	if writer, ok := part.(storageapi.BatchWriter); ok {
		for len(ops) > 0 {
			n := min(len(ops), batchOps)
			results, err := writer.ApplyBatch(ctx, ops[:n])
			if status.Code(err) == codes.Unimplemented {
				break
			}
			if err != nil {
				if ctx.Err() == nil {
					log.Errorf("symboldb: persist %d %ss: %v", n, what, err)
				}
			} else {
				for i, r := range results {
					if r.Err != nil && !(ops[i].Type == storageapi.BatchDelete &&
						errors.Is(r.Err, storageapi.ErrNotFound)) {
						log.Errorf("symboldb: persist %s %q: %v", what, ops[i].ID, r.Err)
					}
				}
			}
			ops = ops[n:]
		}
	}
	for _, op := range ops {
		var err error
		if op.Type == storageapi.BatchDelete {
			if err = part.Delete(ctx, op.ID); errors.Is(err, storageapi.ErrNotFound) {
				err = nil
			}
		} else {
			err = part.Set(ctx, op.ID, op.Doc)
		}
		if err != nil && ctx.Err() == nil {
			log.Errorf("symboldb: persist %s %q: %v", what, op.ID, err)
		}
	}
}

// symbolWrite is the write one name needs so that a file's stored
// contribution matches what the file now defines, plus the before-state
// the name index needs once the write lands.
type symbolWrite struct {
	name string
	// replace is what each contributing file now defines under name.
	replace   map[string][]symbolLoc
	merged    []symbolLoc
	wasListed bool
	// wrote is true while op still has to be applied, and stays true
	// after it lands so the name index takes the cheap path.
	wrote bool
	op    storageapi.BatchOp
}

// stageSymbolWrite reads name's stored document and computes the write
// it needs so that each file in replace contributes what it maps to (and
// nothing, when nil). It reports false when the name needs no attention
// at all, either because nothing is stored and nothing would be, or
// because the read failed.
func (p *Parser) stageSymbolWrite(
	ctx context.Context, name string, replace map[string][]symbolLoc,
) (symbolWrite, bool) {
	w := symbolWrite{name: name, replace: replace}
	var doc symbolDoc
	err := p.symbols.Get(ctx, name, &doc)
	switch {
	case errors.Is(err, storageapi.ErrNotFound):
		locs := mergeLocs(nil, replace)
		if len(locs) == 0 {
			// Nothing stored and nothing to store: a tombstone for a
			// symbol that was never stored would be noise.
			return symbolWrite{}, false
		}
		w.merged, w.wrote = locs, true
		w.op = storageapi.BatchOp{
			Type: storageapi.BatchCreate, ID: name,
			Doc: symbolDoc{Name: name, Locs: locs, Version: 1},
		}
	case err != nil:
		if ctx.Err() == nil {
			log.Errorf("symboldb: read symbol %q: %v", name, err)
		}
		return symbolWrite{}, false
	default:
		w.wasListed = listed(doc.Locs)
		w.merged = mergeLocs(doc.Locs, replace)
		if sameLocs(doc.Locs, w.merged) {
			return w, true
		}
		// A doc written before the counter existed stores no Version
		// field; nil matches its absence where 0 would not.
		var current any
		if doc.Version != 0 {
			current = doc.Version
		}
		w.wrote = true
		w.op = storageapi.BatchOp{
			Type: storageapi.BatchUpdate, ID: name,
			Updates: []storageapi.Update{
				{FieldPath: []string{"Locs"}, Value: w.merged},
				{FieldPath: []string{"Version"}, Value: doc.Version + 1},
			},
			Preconditions: []storageapi.Precondition{
				{FieldPath: []string{"Version"}, Value: current},
			},
		}
	}
	return w, true
}

// applySymbolWrites applies the staged writes, retrying the operations
// a concurrent writer invalidated. It reports false when the store does
// not implement batching, leaving the writes unapplied. what names the
// writes' origin for the log.
func (p *Parser) applySymbolWrites(
	ctx context.Context, what string, writes []symbolWrite,
) bool {
	pending := make([]int, 0, len(writes))
	for i := range writes {
		if writes[i].wrote {
			pending = append(pending, i)
		}
	}
	for attempt := uint(1); len(pending) > 0; attempt++ {
		var stale []int
		for chunk := range slices.Chunk(pending, batchOps) {
			ops := make([]storageapi.BatchOp, len(chunk))
			for i, idx := range chunk {
				ops[i] = writes[idx].op
			}
			results, err := p.batch.ApplyBatch(ctx, ops)
			if err != nil {
				if status.Code(err) == codes.Unimplemented {
					p.batch = nil
					return false
				}
				if ctx.Err() == nil {
					log.Errorf("symboldb: persist %d symbols of %s: %v",
						len(ops), what, err)
				}
				for _, idx := range chunk {
					writes[idx].wrote = false
				}
				continue
			}
			for i, result := range results {
				idx := chunk[i]
				switch {
				case result.Err == nil:
				case errors.Is(result.Err, storageapi.ErrPreconditionFailed),
					errors.Is(result.Err, storageapi.ErrAlreadyExists),
					errors.Is(result.Err, storageapi.ErrNotFound):
					stale = append(stale, idx)
				default:
					if ctx.Err() == nil {
						log.Errorf("symboldb: persist symbol %q: %v",
							writes[idx].name, result.Err)
					}
					writes[idx].wrote = false
				}
			}
		}
		if len(stale) == 0 {
			return true
		}
		sleep, stop := retryStrategy(attempt)
		if stop {
			for _, idx := range stale {
				log.Errorf("symboldb: persist symbol %q: %v",
					writes[idx].name, storageapi.ErrPreconditionFailed)
				writes[idx].wrote = false
			}
			return true
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(sleep):
		}

		pending = pending[:0]
		for _, idx := range stale {
			w, ok := p.stageSymbolWrite(
				ctx, writes[idx].name, writes[idx].replace)
			if !ok {
				writes[idx].wrote = false
				continue
			}
			writes[idx] = w
			if w.wrote {
				pending = append(pending, idx)
			}
		}
	}
	return true
}
