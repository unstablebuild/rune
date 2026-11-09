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
	"slices"
	"sync"

	log "github.com/sirupsen/logrus"
)

// catalog is the index in memory: the keyword index, every file and where
// each segment is, and the vectors while they are loaded. The indexer is
// its only writer; searches read it. Storage writes that touch vectors are
// made under mu too, so loading the vectors never races them.
type catalog struct {
	kw *keywordIndex

	mu     sync.Mutex
	files  map[uint32]*fileDoc
	byPath map[string]uint32
	segs   map[uint32]segInfo
	// vec is nil until the vectors are loaded, and after they are released.
	vec *vectorMatrix
}

// segInfo is what searches need of a segment without reading it.
type segInfo struct {
	file   uint32
	kind   string
	loc    int
	hasVec bool
}

func newCatalog() *catalog {
	return &catalog{
		kw:     newKeywordIndex(),
		files:  map[uint32]*fileDoc{},
		byPath: map[string]uint32{},
		segs:   map[uint32]segInfo{},
	}
}

// file returns a copy of the file id.
func (c *catalog) file(id uint32) (fileDoc, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, ok := c.files[id]
	if !ok {
		return fileDoc{}, false
	}
	return *f, true
}

func (c *catalog) segment(id uint32) (segInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.segs[id]
	return s, ok
}

// snapshot returns a copy of every file, by ID.
func (c *catalog) snapshot() []fileDoc {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]fileDoc, 0, len(c.files))
	for _, f := range c.files {
		out = append(out, *f)
	}
	slices.SortFunc(out, func(a, b fileDoc) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// splitLocked sorts a file's segments into its name rows and the rest,
// the rest's vectors apart. Callers hold mu.
func (c *catalog) splitLocked(f *fileDoc) (names, others, vectors []uint32) {
	for _, id := range f.Segments {
		s := c.segs[id]
		if s.kind == segmentName {
			names = append(names, id)
			continue
		}
		others = append(others, id)
		if s.hasVec {
			vectors = append(vectors, id)
		}
	}
	return names, others, vectors
}

// putFileLocked adds or replaces a file. Callers hold mu.
func (c *catalog) putFileLocked(d fileDoc) {
	if old, ok := c.files[d.ID]; ok && old.Path != d.Path {
		delete(c.byPath, old.Path)
	}
	c.files[d.ID] = &d
	c.byPath[d.Path] = d.ID
}

// addSegmentLocked indexes a segment for keyword search and records where
// it is. Callers hold mu.
func (c *catalog) addSegmentLocked(d segmentDoc) {
	c.segs[d.ID] = segInfo{file: d.File, kind: d.Kind, loc: d.Loc, hasVec: d.HasVec}
	c.kw.add(d.ID, d.File, d.Name, d.Body)
}

// dropSegmentsLocked forgets segments of file, and the file's vectors
// when any of them had one. Callers hold mu.
func (c *catalog) dropSegmentsLocked(file uint32, ids []uint32) {
	vectors := false
	for _, id := range ids {
		vectors = vectors || c.segs[id].hasVec
		delete(c.segs, id)
		c.kw.remove(id)
	}
	if vectors && c.vec != nil {
		c.vec.removeFile(file)
	}
}

// dropFileLocked forgets a file and its segments. Callers hold mu.
func (c *catalog) dropFileLocked(id uint32) {
	f, ok := c.files[id]
	if !ok {
		return
	}
	c.dropSegmentsLocked(id, f.Segments)
	delete(c.files, id)
	if c.byPath[f.Path] == id {
		delete(c.byPath, f.Path)
	}
}

// vectorCount is how many segments have a vector.
func (c *catalog) vectorCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, s := range c.segs {
		if s.hasVec {
			n++
		}
	}
	return n
}

// loadVectors reads the stored vectors into memory unless they are there,
// in segment order, deleting the ones no segment owns.
func (c *catalog) loadVectors(ctx context.Context, st *store) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vec != nil {
		return nil
	}
	type row struct {
		seg uint32
		vec []byte
	}
	var rows []row
	var orphans []uint32
	err := st.loadVectors(ctx, func(id uint32, vec []byte) error {
		if s, ok := c.segs[id]; !ok || !s.hasVec || len(vec) != 2*dims {
			orphans = append(orphans, id)
			return nil
		}
		rows = append(rows, row{id, vec})
		return nil
	})
	if err != nil {
		return err
	}
	slices.SortFunc(rows, func(a, b row) int { return cmp.Compare(a.seg, b.seg) })
	m := &vectorMatrix{
		halves:  make([]uint16, 0, len(rows)*dims),
		rowSeg:  make([]uint32, 0, len(rows)),
		rowFile: make([]uint32, 0, len(rows)),
	}
	for _, r := range rows {
		if err := m.add(r.seg, c.segs[r.seg].file, r.vec); err != nil {
			return err
		}
	}
	c.vec = m
	if len(orphans) > 0 {
		if err := st.deleteVectors(ctx, orphans); err != nil {
			log.Errorf("llmsearch: delete %d orphan vectors: %v", len(orphans), err)
		}
	}
	return nil
}

// releaseVectors frees the vectors' memory; keyword search keeps working
// and the next meaning search loads them again.
func (c *catalog) releaseVectors() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vec = nil
}
