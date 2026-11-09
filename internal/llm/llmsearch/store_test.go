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
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"unstable.build/rune/internal/localstorage"
)

// storages are the backends the store must work over.
func storages(t *testing.T) map[string]func() storageapi.Service {
	return map[string]func() storageapi.Service{
		"in memory": func() storageapi.Service { return storagestub.NewInMemoryService() },
		"bolt": func() storageapi.Service {
			svc := localstorage.New(context.Background(), filepath.Join(t.TempDir(), "db"), docbson.Marshaler())
			t.Cleanup(func() { _ = svc.Close() })
			return svc
		},
	}
}

func TestStoreAllocatesIDsAcrossReopen(t *testing.T) {
	for name, newDB := range storages(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db := newDB()
			st, err := openStore(ctx, db)
			require.NoError(t, err)
			ids, err := st.allocFileIDs(ctx, 3)
			require.NoError(t, err)
			assert.Equal(t, []uint32{1, 2, 3}, ids)
			ids, err = st.allocSegmentIDs(ctx, idBlock+1)
			require.NoError(t, err)
			assert.Equal(t, uint32(1), ids[0])
			assert.Equal(t, uint32(idBlock+1), ids[idBlock])

			st, err = openStore(ctx, db)
			require.NoError(t, err)
			ids, err = st.allocFileIDs(ctx, 1)
			require.NoError(t, err)
			assert.Equal(t, []uint32{idBlock + 1}, ids, "IDs reserved before a reopen are never handed out again")
			ids, err = st.allocSegmentIDs(ctx, 1)
			require.NoError(t, err)
			assert.Equal(t, []uint32{2*idBlock + 1}, ids)
		})
	}
}

func TestSegmentDocLine(t *testing.T) {
	stretch := segmentDoc{Kind: segmentLines, Loc: 10, LocEnd: 12, Body: "func a() {\r\n\treturn\r\n}"}
	normalized := stretch
	normalized.Body, normalized.Original = "func a ( ) {", "func  a() {"
	tests := []struct {
		name  string
		doc   segmentDoc
		focus int
		want  string
	}{
		{"first line without focus", stretch, 0, "func a() {"},
		{"focus", stretch, 11, "\treturn"},
		{"last line", stretch, 12, "}"},
		{"focus before", stretch, 9, ""},
		{"focus after", stretch, 13, ""},
		{"as written", normalized, 10, "func  a() {"},
		{"cells", segmentDoc{Kind: segmentCells, Loc: 2, LocEnd: 2, Body: "x = 1"}, 0, ""},
		{"name", segmentDoc{Kind: segmentName, Name: "a go"}, 0, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.doc.line(tc.focus))
		})
	}
}

func TestStoreRoundTrip(t *testing.T) {
	for name, newDB := range storages(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st, err := openStore(ctx, newDB())
			require.NoError(t, err)
			f := fileDoc{ID: 1, Path: "a/b.go", Size: 10, ModTime: 1e18, Inode: -2, Device: 7,
				State: stateDone, Lines: 3, Segments: []uint32{1, 2}}
			segs := []segmentDoc{
				{ID: 1, File: 1, Kind: segmentName, Name: "b go", Body: "a"},
				{ID: 2, File: 1, Kind: segmentLines, Loc: 1, LocEnd: 3, Excerpt: "func B()", Body: "func b",
					Original: "func B", HasVec: true},
			}
			vec := halfBytes(make([]float32, dims))
			require.NoError(t, st.putVectors(ctx, []vectorDoc{{ID: 2, Vec: vec}}))
			require.NoError(t, st.putSegments(ctx, segs))
			require.NoError(t, st.putFiles(ctx, []fileDoc{f}))

			files, err := st.listFiles(ctx)
			require.NoError(t, err)
			f.Kind, f.Version = kindCode, schemaVersion
			assert.Equal(t, []fileDoc{f}, files)
			got, found, err := st.segment(ctx, 2)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, segs[1], got)
			assert.Equal(t, "func B", got.text())
			var vecs [][]byte
			require.NoError(t, st.loadVectors(ctx, func(id uint32, v []byte) error {
				assert.Equal(t, uint32(2), id)
				vecs = append(vecs, v)
				return nil
			}))
			assert.Equal(t, [][]byte{vec}, vecs)

			require.NoError(t, st.deleteVectors(ctx, []uint32{2}))
			require.NoError(t, st.deleteSegments(ctx, []uint32{1, 2}))
			require.NoError(t, st.deleteFiles(ctx, []uint32{1}))
			_, found, err = st.segment(ctx, 2)
			require.NoError(t, err)
			assert.False(t, found)
			files, err = st.listFiles(ctx)
			require.NoError(t, err)
			assert.Empty(t, files)
		})
	}
}

func TestStoreClearsAnotherSchemaVersion(t *testing.T) {
	ctx := context.Background()
	db := storagestub.NewInMemoryService()
	st, err := openStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, st.putSegments(ctx, []segmentDoc{{ID: 5, File: 1, Kind: segmentName}}))
	require.NoError(t, st.putFiles(ctx, []fileDoc{{ID: 1, Path: "a.go", Segments: []uint32{5}}}))
	require.NoError(t, db.Set(ctx, metaID, metaDoc{Version: schemaVersion + 1, Fingerprint: "x", NextFile: 9}))

	st, err = openStore(ctx, db)
	require.NoError(t, err)
	files, err := st.listFiles(ctx)
	require.NoError(t, err)
	assert.Empty(t, files)
	_, found, err := st.segment(ctx, 5)
	require.NoError(t, err)
	assert.False(t, found)
	assert.Equal(t, metaDoc{Version: schemaVersion, NextFile: 1, NextSegment: 1}, st.meta())
}

// batchStorage wraps a storage so that each partition's ApplyBatch can be
// intercepted, by partition name, and counts the batches and the single
// writes each partition gets.
type batchStorage struct {
	storageapi.Service
	name      string
	intercept func(partition string) error
	calls     *atomic.Int64
	counts    *writeCounts
}

// writeCounts tallies writes by partition name.
type writeCounts struct {
	mu      sync.Mutex
	batches map[string]int
	singles map[string]int
}

func newBatchStorage(db storageapi.Service, intercept func(partition string) error) *batchStorage {
	if intercept == nil {
		intercept = func(string) error { return nil }
	}
	return &batchStorage{
		Service: db, intercept: intercept, calls: new(atomic.Int64),
		counts: &writeCounts{batches: map[string]int{}, singles: map[string]int{}},
	}
}

func (b *batchStorage) Partition(name string) (storageapi.Service, error) {
	child, err := b.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	return &batchStorage{Service: child, name: name, intercept: b.intercept, calls: b.calls, counts: b.counts}, nil
}

func (b *batchStorage) ApplyBatch(ctx context.Context, ops []storageapi.BatchOp) ([]storageapi.BatchOpResult, error) {
	b.calls.Add(1)
	b.counts.mu.Lock()
	b.counts.batches[b.name]++
	b.counts.mu.Unlock()
	if err := b.intercept(b.name); err != nil {
		return nil, err
	}
	return b.Service.(storageapi.BatchWriter).ApplyBatch(ctx, ops)
}

func (b *batchStorage) Set(ctx context.Context, id string, doc any) error {
	b.counts.mu.Lock()
	b.counts.singles[b.name]++
	b.counts.mu.Unlock()
	return b.Service.Set(ctx, id, doc)
}

func (b *batchStorage) Delete(ctx context.Context, id string) error {
	b.counts.mu.Lock()
	b.counts.singles[b.name]++
	b.counts.mu.Unlock()
	return b.Service.Delete(ctx, id)
}

// batches returns how many batches each partition got since the last call,
// and how many single writes, by partition name.
func (b *batchStorage) batches() (batches, singles map[string]int) {
	b.counts.mu.Lock()
	defer b.counts.mu.Unlock()
	batches, singles = b.counts.batches, b.counts.singles
	b.counts.batches, b.counts.singles = map[string]int{}, map[string]int{}
	return batches, singles
}

func TestStoreFallsBackWhenBatchingIsUnimplemented(t *testing.T) {
	ctx := context.Background()
	db := newBatchStorage(storagestub.NewInMemoryService(), func(string) error {
		return status.Error(codes.Unimplemented, "method Batch not implemented")
	})
	st, err := openStore(ctx, db)
	require.NoError(t, err)
	require.NoError(t, st.putSegments(ctx, []segmentDoc{{ID: 1, File: 1, Kind: segmentName}}))
	require.NoError(t, st.putFiles(ctx, []fileDoc{{ID: 1, Path: "a.go", Segments: []uint32{1}}}))
	assert.Equal(t, int64(1), db.calls.Load(), "a store without batches is asked once")
	files, err := st.listFiles(ctx)
	require.NoError(t, err)
	assert.Len(t, files, 1)
}

func TestStoreReportsBatchFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("disk full")
	db := newBatchStorage(storagestub.NewInMemoryService(), func(string) error { return boom })
	st, err := openStore(ctx, db)
	require.NoError(t, err)
	assert.ErrorIs(t, st.putFiles(ctx, []fileDoc{{ID: 1}}), boom)
}

func TestCleanupWorkspaceHook(t *testing.T) {
	ctx := context.Background()
	ideStorage := storagestub.NewInMemoryService()
	workspaceDB := func(root workspaceapi.URI) storageapi.Service {
		return storageapi.WithPartition(storageapi.WithPartition(ideStorage, PartitionName), root.String())
	}
	ti := openTestIndex(t, true, DefaultOptions())
	ti.db = workspaceDB(ti.root)
	require.NoError(t, ti.idx.Close())
	ti.svc = newService(t, ti.reg, NewFakeEmbedderFactory())
	ti.open(t, DefaultOptions())
	require.NoError(t, ti.idx.Close())

	other, err := workspaceapi.ParseURI(ti.root.String() + "-other")
	require.NoError(t, err)
	otherDB := workspaceDB(other)
	require.NoError(t, otherDB.Set(ctx, metaID, metaDoc{Version: schemaVersion}))

	count := func(part string) int {
		p, err := ti.db.Partition(part)
		require.NoError(t, err)
		n := 0
		require.NoError(t, list(ctx, p, func(struct{ ID uint32 }) error {
			n++
			return nil
		}))
		return n
	}
	for _, part := range []string{filesPartition, segmentsPartition, vectorsPartition} {
		require.NotZero(t, count(part), "%s must be populated before the drop", part)
	}

	require.NoError(t, CleanupWorkspaceHook(ideStorage)(ctx, ti.root))

	for _, part := range []string{filesPartition, segmentsPartition, vectorsPartition} {
		assert.Zero(t, count(part), "%s must be empty after the drop", part)
	}
	var m metaDoc
	assert.ErrorIs(t, ti.db.Get(ctx, metaID, &m), storageapi.ErrNotFound)
	assert.NoError(t, otherDB.Get(ctx, metaID, &m), "dropping one workspace must not touch another")
}
