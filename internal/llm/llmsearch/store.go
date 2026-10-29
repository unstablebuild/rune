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
	"strconv"
	"sync"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// PartitionName is the IDE storage partition every workspace's search
	// index is nested under.
	PartitionName = "llmsearch"

	filesPartition    = "files"
	segmentsPartition = "segments"
	vectorsPartition  = "vectors"

	metaID = "meta"

	// schemaVersion identifies the layout of the persisted partitions. An
	// index written under another version is emptied and rebuilt.
	schemaVersion = 1

	// idBlock IDs are reserved at a time so that handing them out rarely
	// writes the meta document.
	idBlock = 256

	// batchOps bounds the writes of one ApplyBatch call.
	batchOps = 256
)

// File states.
const (
	statePending = "pending"
	stateDone    = "done"
	stateSkipped = "skipped"
	stateFailed  = "failed"
)

// Segment kinds: a file's name row, a stretch of lines, a notebook's cells.
const (
	segmentName  = "name"
	segmentLines = "lines"
	segmentCells = "cells"
)

// kindCode is the only kind of file this index holds.
const kindCode = "code"

// fileDoc is one file of the workspace that code search knows. Stored in
// the files partition under its ID.
type fileDoc struct {
	ID uint32
	// Path is workspace-relative, with forward slashes.
	Path string
	Kind string
	Size int64
	// ModTime is in Unix nanoseconds.
	ModTime int64
	// Inode and Device hold the bits of the file's unsigned inode and
	// device numbers (BSON has no unsigned 64-bit integer); both are 0
	// when the filesystem does not report them.
	Inode  int64
	Device int64
	State  string
	// Detail is why the file was skipped or failed.
	Detail string
	Lines  int
	// Cells is 0 for anything but a notebook.
	Cells int
	// Segments are the IDs of every segment the file owns, its name row
	// first. A segment of the file not listed here is an orphan.
	Segments []uint32
	Version  int
}

// segmentDoc is a row of keyword search, and of meaning search when
// HasVec. Stored in the segments partition under its ID.
type segmentDoc struct {
	ID   uint32
	File uint32
	Kind string
	// Loc and LocEnd are the first and last line (or cell), from 1; 0 for
	// name rows.
	Loc     int
	LocEnd  int
	Excerpt string
	// Name and Body are the keyword columns, normalized: a name row's file
	// name and folders, or "" and a stretch's text.
	Name string
	Body string
	// Original is the stretch as written when normalizing changed it.
	Original string
	HasVec   bool
}

// text is the segment's text as written.
func (d segmentDoc) text() string {
	if d.Original != "" {
		return d.Original
	}
	return d.Body
}

// line is the line a stretch of lines opens at, focus or else its first,
// as written; "" for cells and name rows, and for a focus outside it.
func (d segmentDoc) line(focus int) string {
	if d.Kind != segmentLines {
		return ""
	}
	if focus == 0 {
		focus = d.Loc
	}
	lines := splitLines(d.text())
	if i := focus - d.Loc; i >= 0 && i < len(lines) {
		return lines[i]
	}
	return ""
}

// vectorDoc is a segment's vector: dims L2-normalized binary16 values.
// Stored in the vectors partition under the segment's ID, apart from the
// segments so that loading the keyword index never decodes vectors.
type vectorDoc struct {
	ID  uint32
	Vec []byte
}

// metaDoc describes the whole index. Stored at the root of the
// workspace's database.
type metaDoc struct {
	Version int
	// Complete is true once a full scan has finished.
	Complete bool
	// Fingerprint is what the vectors were made with: "<vectorSource> |
	// <Options.fingerprint>". An index never mixes fingerprints.
	Fingerprint  string
	VectorSource string
	// NextFile and NextSegment are where ID allocation resumes: every ID
	// below them may be in use.
	NextFile    uint32
	NextSegment uint32
}

// store persists one workspace's index in a storageapi.Service. Writes of
// one file go vectors first, then segments, then the file document, so a
// crash can leave orphans (swept on load) but never a file pointing at
// segments or vectors that were not written.
type store struct {
	files, segments, vectors, db storageapi.Service
	// noBatch is set once the storage turns out not to support batches.
	noBatch atomic.Bool

	mu sync.Mutex
	m  metaDoc
	// nextFile..endFile and nextSeg..endSeg are the IDs reserved in m and
	// not handed out yet.
	nextFile, endFile uint32
	nextSeg, endSeg   uint32
}

// openStore opens the index persisted in db, emptying it when it was
// written under another schema version.
func openStore(ctx context.Context, db storageapi.Service) (*store, error) {
	s := &store{db: db}
	for _, p := range []struct {
		name string
		dst  *storageapi.Service
	}{
		{filesPartition, &s.files},
		{segmentsPartition, &s.segments},
		{vectorsPartition, &s.vectors},
	} {
		part, err := db.Partition(p.name)
		if err != nil {
			return nil, fmt.Errorf("llmsearch: partition %q: %w", p.name, err)
		}
		*p.dst = part
	}
	err := db.Get(ctx, metaID, &s.m)
	switch {
	case errors.Is(err, storageapi.ErrNotFound):
		s.m = metaDoc{Version: schemaVersion, NextFile: 1, NextSegment: 1}
	case err != nil:
		return nil, fmt.Errorf("llmsearch: read meta: %w", err)
	case s.m.Version != schemaVersion:
		if err := s.clear(ctx); err != nil {
			return nil, err
		}
		s.m = metaDoc{Version: schemaVersion, NextFile: 1, NextSegment: 1}
		if err := s.db.Set(ctx, metaID, s.m); err != nil {
			return nil, fmt.Errorf("llmsearch: write meta: %w", err)
		}
	}
	s.nextFile, s.endFile = s.m.NextFile, s.m.NextFile
	s.nextSeg, s.endSeg = s.m.NextSegment, s.m.NextSegment
	return s, nil
}

// clear deletes every document of the three partitions.
func (s *store) clear(ctx context.Context) error {
	for _, part := range []storageapi.Service{s.vectors, s.segments, s.files} {
		var ids []uint32
		err := list(ctx, part, func(d struct{ ID uint32 }) error {
			ids = append(ids, d.ID)
			return nil
		})
		if err == nil {
			err = s.delete(ctx, part, ids)
		}
		if err != nil {
			return fmt.Errorf("llmsearch: clear: %w", err)
		}
	}
	return nil
}

// meta returns the index's meta document.
func (s *store) meta() metaDoc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m
}

// updateMeta applies fn to the meta document and persists it.
func (s *store) updateMeta(ctx context.Context, fn func(*metaDoc)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.m
	fn(&m)
	if err := s.db.Set(ctx, metaID, m); err != nil {
		return fmt.Errorf("llmsearch: write meta: %w", err)
	}
	s.m = m
	return nil
}

func (s *store) allocFileIDs(ctx context.Context, n int) ([]uint32, error) {
	return s.alloc(ctx, n, &s.nextFile, &s.endFile, func(m *metaDoc) *uint32 { return &m.NextFile })
}

func (s *store) allocSegmentIDs(ctx context.Context, n int) ([]uint32, error) {
	return s.alloc(ctx, n, &s.nextSeg, &s.endSeg, func(m *metaDoc) *uint32 { return &m.NextSegment })
}

// alloc hands out n IDs from next..end, reserving another block in the
// meta document first when they run out, so that IDs survive a crash.
func (s *store) alloc(
	ctx context.Context, n int, next, end *uint32, mark func(*metaDoc) *uint32,
) ([]uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if have := int(*end - *next); have < n {
		m := s.m
		*mark(&m) = *end + uint32((n-have+idBlock-1)/idBlock*idBlock)
		if err := s.db.Set(ctx, metaID, m); err != nil {
			return nil, fmt.Errorf("llmsearch: reserve IDs: %w", err)
		}
		s.m = m
		*end = *mark(&m)
	}
	ids := make([]uint32, n)
	for i := range ids {
		ids[i] = *next
		*next++
	}
	return ids, nil
}

// list decodes every document of part into fn. A document that does not
// decode is logged and skipped.
func list[T any](ctx context.Context, part storageapi.Service, fn func(T) error) error {
	it, err := part.List(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = it.Close() }()
	for it.HasNext() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var doc T
		if err := it.NextTo(&doc); err != nil {
			log.Errorf("llmsearch: decode a %T: %v", doc, err)
			continue
		}
		if err := fn(doc); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) listFiles(ctx context.Context) ([]fileDoc, error) {
	var out []fileDoc
	err := list(ctx, s.files, func(d fileDoc) error {
		out = append(out, d)
		return nil
	})
	return out, err
}

func (s *store) streamSegments(ctx context.Context, fn func(segmentDoc) error) error {
	return list(ctx, s.segments, fn)
}

func (s *store) loadVectors(ctx context.Context, fn func(id uint32, vec []byte) error) error {
	return list(ctx, s.vectors, func(d vectorDoc) error { return fn(d.ID, d.Vec) })
}

// segment reads one segment; found is false when it is not stored.
func (s *store) segment(ctx context.Context, id uint32) (_ segmentDoc, found bool, _ error) {
	var d segmentDoc
	err := s.segments.Get(ctx, docID(id), &d)
	if errors.Is(err, storageapi.ErrNotFound) {
		return segmentDoc{}, false, nil
	}
	if err != nil {
		return segmentDoc{}, false, err
	}
	return d, true, nil
}

func (s *store) putFiles(ctx context.Context, docs []fileDoc) error {
	ops := make([]storageapi.BatchOp, len(docs))
	for i, d := range docs {
		d.Kind, d.Version = kindCode, schemaVersion
		ops[i] = storageapi.BatchOp{Type: storageapi.BatchSet, ID: docID(d.ID), Doc: d}
	}
	return s.apply(ctx, s.files, ops)
}

func (s *store) putSegments(ctx context.Context, docs []segmentDoc) error {
	ops := make([]storageapi.BatchOp, len(docs))
	for i, d := range docs {
		ops[i] = storageapi.BatchOp{Type: storageapi.BatchSet, ID: docID(d.ID), Doc: d}
	}
	return s.apply(ctx, s.segments, ops)
}

func (s *store) putVectors(ctx context.Context, docs []vectorDoc) error {
	ops := make([]storageapi.BatchOp, len(docs))
	for i, d := range docs {
		ops[i] = storageapi.BatchOp{Type: storageapi.BatchSet, ID: docID(d.ID), Doc: d}
	}
	return s.apply(ctx, s.vectors, ops)
}

func (s *store) deleteFiles(ctx context.Context, ids []uint32) error {
	return s.delete(ctx, s.files, ids)
}

func (s *store) deleteSegments(ctx context.Context, ids []uint32) error {
	return s.delete(ctx, s.segments, ids)
}

func (s *store) deleteVectors(ctx context.Context, ids []uint32) error {
	return s.delete(ctx, s.vectors, ids)
}

func (s *store) delete(ctx context.Context, part storageapi.Service, ids []uint32) error {
	ops := make([]storageapi.BatchOp, len(ids))
	for i, id := range ids {
		ops[i] = storageapi.BatchOp{Type: storageapi.BatchDelete, ID: docID(id)}
	}
	return s.apply(ctx, part, ops)
}

// apply writes ops to part in batches when the storage supports them, else
// one call at a time.
func (s *store) apply(ctx context.Context, part storageapi.Service, ops []storageapi.BatchOp) error {
	for len(ops) > 0 {
		n := min(len(ops), batchOps)
		if err := s.applyBatch(ctx, part, ops[:n]); err != nil {
			return err
		}
		ops = ops[n:]
	}
	return nil
}

func (s *store) applyBatch(ctx context.Context, part storageapi.Service, ops []storageapi.BatchOp) error {
	if w, ok := part.(storageapi.BatchWriter); ok && !s.noBatch.Load() {
		results, err := w.ApplyBatch(ctx, ops)
		switch {
		case status.Code(err) == codes.Unimplemented:
			s.noBatch.Store(true)
		case err != nil:
			return err
		default:
			for i, r := range results {
				if r.Err != nil {
					return fmt.Errorf("llmsearch: write %s: %w", ops[i].ID, r.Err)
				}
			}
			return nil
		}
	}
	for _, op := range ops {
		var err error
		switch op.Type {
		case storageapi.BatchDelete:
			err = part.Delete(ctx, op.ID)
		default:
			err = part.Set(ctx, op.ID, op.Doc)
		}
		if err != nil {
			return fmt.Errorf("llmsearch: write %s: %w", op.ID, err)
		}
	}
	return nil
}

func docID(id uint32) string { return strconv.FormatUint(uint64(id), 10) }

// CleanupWorkspaceHook returns a hook that drops the search index of a
// workspace, given the storage the indexes are partitioned out of (the
// service IndexConfig.DB is derived from). It is a package-level function
// because the workspaces it reclaims have no open Index.
func CleanupWorkspaceHook(
	storage storageapi.Service,
) func(ctx context.Context, root workspaceapi.URI) error {
	return func(ctx context.Context, root workspaceapi.URI) error {
		dbs, err := storage.Partition(PartitionName)
		if err != nil {
			return fmt.Errorf("llmsearch: partition storage: %w", err)
		}
		defer func() { _ = dbs.Close() }()

		db, err := dbs.Partition(root.String())
		if err != nil {
			return fmt.Errorf("llmsearch: partition %q: %w", root.String(), err)
		}
		defer func() { _ = db.Close() }()

		// Drop is not recursive, so the partitions go first.
		for _, name := range []string{filesPartition, segmentsPartition, vectorsPartition} {
			part, err := db.Partition(name)
			if err != nil {
				return fmt.Errorf("llmsearch: partition %q: %w", name, err)
			}
			err = drop(ctx, part)
			if cerr := part.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return fmt.Errorf("llmsearch: drop %q: %w", name, err)
			}
		}
		if err := drop(ctx, db); err != nil {
			return fmt.Errorf("llmsearch: drop %q: %w", root.String(), err)
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
