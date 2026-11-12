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
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// countingEmbedder counts the texts it embeds, failing with err (from the
// call after failAfter, when it is set) when it is not nil.
type countingEmbedder struct {
	Embedder
	info      *EmbedderInfo
	mu        sync.Mutex
	texts     int
	calls     int
	failAfter int
	err       error
}

func newCountingEmbedder() *countingEmbedder {
	return &countingEmbedder{Embedder: fakeEmbedder{}}
}

func (e *countingEmbedder) Info() EmbedderInfo {
	if e.info != nil {
		return *e.info
	}
	return e.Embedder.Info()
}

func (e *countingEmbedder) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.err != nil && e.calls > e.failAfter {
		return nil, e.err
	}
	e.texts += len(texts)
	return e.Embedder.EmbedTexts(ctx, texts)
}

func (e *countingEmbedder) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.texts
}

// testIndexer is an indexer over a copy of the fixture.
type testIndexer struct {
	dir string
	fs  workspaceapi.FileSystem
	db  storageapi.Service
	x   *indexer
}

func newTestIndexer(t *testing.T) *testIndexer {
	t.Helper()
	dir, _, fsys := newWorkspace(t)
	ti := &testIndexer{dir: dir, fs: fsys, db: storagestub.NewInMemoryService()}
	ti.reload(t)
	return ti
}

// reload opens the store again into a fresh catalog, as a restart does.
func (ti *testIndexer) reload(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	st, err := openStore(ctx, ti.db)
	require.NoError(t, err)
	ti.x = &indexer{st: st, fs: ti.fs, title: "sample", opts: DefaultOptions(), cat: newCatalog()}
	require.NoError(t, ti.x.load(ctx))
}

// index adopts embed, syncs the whole workspace and runs what is pending.
func (ti *testIndexer) index(t *testing.T, embed Embedder) (syncSummary, []runEvent) {
	t.Helper()
	ctx := context.Background()
	_, err := ti.x.adopt(ctx, embed)
	require.NoError(t, err)
	sum, err := ti.x.sync(ctx, []string{"."})
	require.NoError(t, err)
	var events []runEvent
	require.NoError(t, ti.x.run(ctx, embed, nil, func(ev runEvent) { events = append(events, ev) }))
	return sum, events
}

func (ti *testIndexer) file(t *testing.T, rel string) fileDoc {
	t.Helper()
	id, ok := ti.x.cat.byPath[rel]
	require.True(t, ok, "%s is not indexed", rel)
	f, _ := ti.x.cat.file(id)
	return f
}

// stored returns the IDs stored in a partition.
func (ti *testIndexer) stored(t *testing.T, part storageapi.Service) []uint32 {
	t.Helper()
	var ids []uint32
	require.NoError(t, list(context.Background(), part, func(d struct{ ID uint32 }) error {
		ids = append(ids, d.ID)
		return nil
	}))
	slices.Sort(ids)
	return ids
}

func states(cat *catalog) map[string]string {
	out := map[string]string{}
	for _, f := range cat.snapshot() {
		out[f.Path] = f.State
	}
	return out
}

func TestIndexerFirstScan(t *testing.T) {
	ti := newTestIndexer(t)
	embed := newCountingEmbedder()
	sum, events := ti.index(t, embed)

	assert.Equal(t, len(fixtureCode), sum.added)
	assert.Len(t, sum.queued, len(fixtureCode))
	want := map[string]string{}
	for _, p := range fixtureCode {
		want[p] = stateDone
	}
	want["gen.pb.go"] = stateSkipped
	assert.Equal(t, want, states(ti.x.cat))
	assert.Contains(t, ti.file(t, "gen.pb.go").Detail, "made by a program")

	require.Len(t, events, len(fixtureCode)+1)
	assert.Equal(t, runEvent{done: 0, total: len(fixtureCode)}, events[0])
	assert.Equal(t, len(fixtureCode), events[len(events)-1].done)

	nb := ti.file(t, "notebooks/analysis.ipynb")
	assert.Equal(t, 3, nb.Cells)
	seg, ok := ti.x.cat.segment(nb.Segments[1])
	require.True(t, ok)
	assert.Equal(t, segmentCells, seg.kind)

	f := ti.file(t, "retry/policy.go")
	require.Len(t, f.Segments, 2, "a name row and one stretch")
	name, ok := ti.x.cat.segment(f.Segments[0])
	require.True(t, ok)
	assert.Equal(t, segmentName, name.kind)
	stretch, found, err := ti.x.st.segment(context.Background(), f.Segments[1])
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, segmentLines, stretch.Kind)
	assert.Equal(t, 1, stretch.Loc)
	assert.Equal(t, f.Lines, stretch.LocEnd)
	assert.True(t, stretch.HasVec)
	assert.Equal(t, "type Policy struct {", stretch.Excerpt)
	assert.Len(t, ti.file(t, "retry/backoff.go").Segments, 3, "a long file has several stretches")

	m := ti.x.st.meta()
	assert.Equal(t, "fake@0000000 fake fp32 | code1 lines1800", m.Fingerprint)
	assert.Equal(t, "fake@0000000 fake fp32", m.VectorSource)
	assert.Equal(t, embed.count(), ti.x.cat.vectorCount())
}

func TestIndexerSecondSyncFindsNothing(t *testing.T) {
	ti := newTestIndexer(t)
	embed := newCountingEmbedder()
	ti.index(t, embed)
	before := embed.count()

	ti.reload(t)
	sum, events := ti.index(t, embed)
	assert.Equal(t, syncSummary{unchanged: len(fixtureCode), complete: true, report: sum.report}, sum)
	assert.Empty(t, events)
	assert.Equal(t, before, embed.count())
}

func TestIndexerRequeuesAnEditedFile(t *testing.T) {
	ti := newTestIndexer(t)
	embed := newCountingEmbedder()
	ti.index(t, embed)
	old := ti.file(t, "geo/distance.py")
	before := embed.count()

	writeFile(t, ti.dir, "geo/distance.py", "def bearing(a, b):\n    return compass(a, b)\n")
	ctx := context.Background()
	sum, err := ti.x.sync(ctx, []string{"geo/distance.py"})
	require.NoError(t, err)
	assert.Equal(t, 1, sum.changed)
	assert.Equal(t, []uint32{old.ID}, sum.queued)
	assert.Equal(t, statePending, ti.file(t, "geo/distance.py").State)
	require.NoError(t, ti.x.run(ctx, embed, sum.queued, func(runEvent) {}))

	assert.Equal(t, before+1, embed.count(), "only the edited file is embedded again")
	f := ti.file(t, "geo/distance.py")
	assert.Equal(t, stateDone, f.State)
	assert.Equal(t, old.Segments[0], f.Segments[0], "the name row stays")
	assert.NotEqual(t, old.Segments[1], f.Segments[1])
	_, found, err := ti.x.st.segment(ctx, old.Segments[1])
	require.NoError(t, err)
	assert.False(t, found, "the old stretch is deleted")
	assert.NotContains(t, ti.stored(t, ti.x.st.vectors), old.Segments[1])
}

func TestIndexerMovesARenamedFile(t *testing.T) {
	ti := newTestIndexer(t)
	embed := newCountingEmbedder()
	ti.index(t, embed)
	old := ti.file(t, "billing/tax.py")
	before := embed.count()

	require.NoError(t, os.MkdirAll(filepath.Join(ti.dir, "finance"), 0o755))
	require.NoError(t, os.Rename(filepath.Join(ti.dir, "billing/tax.py"), filepath.Join(ti.dir, "finance/rates.py")))
	sum, events := ti.index(t, embed)
	assert.Equal(t, 1, sum.moved)
	assert.Zero(t, sum.added+sum.removed)
	assert.Empty(t, events)
	assert.Equal(t, before, embed.count(), "a move embeds nothing")

	f := ti.file(t, "finance/rates.py")
	assert.Equal(t, old.ID, f.ID)
	assert.Equal(t, old.Segments, f.Segments)
	_, ok := ti.x.cat.byPath["billing/tax.py"]
	assert.False(t, ok)
	name, _, err := ti.x.st.segment(context.Background(), f.Segments[0])
	require.NoError(t, err)
	assert.Equal(t, segmentDoc{ID: f.Segments[0], File: f.ID, Kind: segmentName, Name: "rates.py", Body: "finance"}, name)
}

func TestIndexerRemovesADeletedFile(t *testing.T) {
	ti := newTestIndexer(t)
	ti.index(t, newCountingEmbedder())
	old := ti.file(t, "auth/session.ts")

	require.NoError(t, os.Remove(filepath.Join(ti.dir, "auth/session.ts")))
	sum, err := ti.x.sync(context.Background(), []string{"auth"})
	require.NoError(t, err)
	assert.Equal(t, 1, sum.removed)
	_, ok := ti.x.cat.byPath["auth/session.ts"]
	assert.False(t, ok)
	for _, id := range old.Segments {
		assert.NotContains(t, ti.stored(t, ti.x.st.segments), id)
		assert.NotContains(t, ti.stored(t, ti.x.st.vectors), id)
		_, ok := ti.x.cat.segment(id)
		assert.False(t, ok)
	}
	assert.NotContains(t, ti.stored(t, ti.x.st.files), old.ID)
}

func TestIndexerResetsOnAnotherVectorSource(t *testing.T) {
	ti := newTestIndexer(t)
	first := newCountingEmbedder()
	ti.index(t, first)
	vectors := ti.x.cat.vectorCount()
	require.NotZero(t, vectors)
	require.NoError(t, ti.x.cat.loadVectors(context.Background(), ti.x.st))

	other := newCountingEmbedder()
	other.info = &EmbedderInfo{Model: "other", Revision: "abcdef0123", Runtime: "fake", Dtype: "fp32", Dim: dims}
	reset, err := ti.x.adopt(context.Background(), other)
	require.NoError(t, err)
	assert.True(t, reset)
	for path, state := range states(ti.x.cat) {
		assert.Equal(t, statePending, state, path)
	}
	assert.Zero(t, ti.x.cat.vectorCount())
	assert.Empty(t, ti.stored(t, ti.x.st.vectors))
	assert.Zero(t, ti.x.cat.vec.rows(), "loaded vectors are dropped too")
	assert.Len(t, ti.stored(t, ti.x.st.segments), len(fixtureCode), "only the name rows stay")
	assert.Equal(t, "other@abcdef0 fake fp32", ti.x.st.meta().VectorSource)

	_, events := ti.index(t, other)
	assert.Len(t, events, len(fixtureCode)+1)
	assert.Equal(t, vectors, ti.x.cat.vectorCount())
	assert.Equal(t, vectors, ti.x.cat.vec.rows(), "loaded vectors are kept current")
}

func TestIndexerKeywordOnlyThenModel(t *testing.T) {
	ti := newTestIndexer(t)
	ti.index(t, nil)
	assert.Zero(t, ti.x.cat.vectorCount())
	assert.Equal(t, stateDone, ti.file(t, "retry/backoff.go").State)
	assert.Equal(t, " | code1 lines1800", ti.x.st.meta().Fingerprint)
	assert.Empty(t, ti.x.st.meta().VectorSource)

	embed := newCountingEmbedder()
	_, events := ti.index(t, embed)
	assert.Len(t, events, len(fixtureCode)+1, "the model's arrival indexes everything again")
	assert.NotZero(t, ti.x.cat.vectorCount())
}

func TestIndexerStopsOnADeadEmbedder(t *testing.T) {
	ti := newTestIndexer(t)
	embed := newCountingEmbedder()
	embed.err = errors.New("llama-server exited")
	ctx := context.Background()
	_, err := ti.x.adopt(ctx, embed)
	require.NoError(t, err)
	_, err = ti.x.sync(ctx, []string{"."})
	require.NoError(t, err)
	err = ti.x.run(ctx, embed, nil, func(runEvent) {})
	ee, ok := errors.AsType[*embedderError](err)
	require.True(t, ok, "%v", err)
	assert.ErrorIs(t, ee, embed.err)
	for path, state := range states(ti.x.cat) {
		assert.Equal(t, statePending, state, path)
	}
}

func TestIndexerSweepsWhatACrashLeft(t *testing.T) {
	tests := []struct {
		name string
		fail string
	}{
		{"between the vectors and the segments", segmentsPartition},
		{"between the segments and the file", filesPartition},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ti := newTestIndexer(t)
			ctx := context.Background()
			embed := newCountingEmbedder()
			_, err := ti.x.adopt(ctx, embed)
			require.NoError(t, err)
			_, err = ti.x.sync(ctx, []string{"."})
			require.NoError(t, err)
			names := ti.stored(t, ti.x.st.segments)

			crash := errors.New("crash")
			healthy := ti.db
			ti.db = newBatchStorage(healthy, func(part string) error {
				if part == tt.fail {
					return crash
				}
				return nil
			})
			ti.reload(t)
			err = ti.x.run(ctx, embed, nil, func(runEvent) {})
			require.ErrorIs(t, err, crash)
			assert.NotEmpty(t, ti.stored(t, ti.x.st.vectors), "the crash left vectors behind")

			ti.db = healthy
			ti.reload(t)
			assert.Equal(t, names, ti.stored(t, ti.x.st.segments), "orphan segments are swept on load")
			require.NoError(t, ti.x.cat.loadVectors(ctx, ti.x.st))
			assert.Empty(t, ti.stored(t, ti.x.st.vectors), "orphan vectors are swept when loaded")
			assert.Zero(t, ti.x.cat.vec.rows())
			for path, state := range states(ti.x.cat) {
				assert.Equal(t, statePending, state, path)
			}

			_, events := ti.index(t, embed)
			assert.Len(t, events, len(fixtureCode)+1, "the files are indexed after the crash")
		})
	}
}

func TestIndexerKeepsFilesWhenTheWalkFails(t *testing.T) {
	ti := newTestIndexer(t)
	ti.index(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ti.x.sync(ctx, []string{"."})
	require.ErrorIs(t, err, context.Canceled)
	assert.Len(t, ti.x.cat.snapshot(), len(fixtureCode))
}

func TestIndexerPendingNewestFirst(t *testing.T) {
	ti := newTestIndexer(t)
	now := time.Now()
	for i, p := range []string{"README.md", "retry/policy.go", "billing/tax.py"} {
		at := now.Add(time.Duration(i) * time.Hour)
		require.NoError(t, os.Chtimes(filepath.Join(ti.dir, p), at, at))
	}
	_, err := ti.x.sync(context.Background(), []string{"README.md", "retry/policy.go", "billing/tax.py"})
	require.NoError(t, err)
	var got []string
	for _, f := range ti.x.pending(nil) {
		got = append(got, f.Path)
	}
	assert.Equal(t, []string{"billing/tax.py", "retry/policy.go", "README.md"}, got)
}

func TestIndexerRunFlushesInWindows(t *testing.T) {
	ti := newTestIndexer(t)
	db := newBatchStorage(ti.db, nil)
	ti.db = db
	ti.reload(t)
	extra := flushFiles + 5
	for i := range extra {
		writeFile(t, ti.dir, fmt.Sprintf("many/file%02d.go", i),
			fmt.Sprintf("package many\n\n// Thing%d returns the number %d.\nfunc Thing%d() int { return %d }\n", i, i, i, i))
	}
	ctx := context.Background()
	embed := newCountingEmbedder()
	_, err := ti.x.adopt(ctx, embed)
	require.NoError(t, err)
	sum, err := ti.x.sync(ctx, []string{"."})
	require.NoError(t, err)
	n := len(fixtureCode) + extra
	require.Equal(t, n, sum.added)
	db.batches()

	var events []runEvent
	require.NoError(t, ti.x.run(ctx, embed, nil, func(ev runEvent) { events = append(events, ev) }))

	windows := (n + flushFiles - 1) / flushFiles
	batches, singles := db.batches()
	assert.Equal(t, windows, batches[filesPartition], "files are stored once per window")
	assert.Equal(t, windows, batches[segmentsPartition])
	assert.LessOrEqual(t, batches[vectorsPartition], windows)
	assert.Empty(t, singles, "nothing is written one document at a time")
	require.Len(t, events, n+1)
	for i, ev := range events {
		assert.Equal(t, i, ev.done, "events are reported in order")
	}
	assert.Equal(t, embed.count(), ti.x.cat.vectorCount())
}

func TestIndexerRunKeepsTheWindowWhenTheEmbedderDies(t *testing.T) {
	ti := newTestIndexer(t)
	embed := newCountingEmbedder()
	embed.err, embed.failAfter = errors.New("llama-server exited"), 3
	ctx := context.Background()
	_, err := ti.x.adopt(ctx, embed)
	require.NoError(t, err)
	_, err = ti.x.sync(ctx, []string{"."})
	require.NoError(t, err)
	var events []runEvent
	err = ti.x.run(ctx, embed, nil, func(ev runEvent) { events = append(events, ev) })
	require.ErrorIs(t, err, embed.err)
	settled := len(events) - 1
	assert.GreaterOrEqual(t, settled, embed.failAfter, "the files embedded before the failure are reported")
	assert.Less(t, settled, len(fixtureCode))
	done := 0
	for _, f := range ti.x.cat.snapshot() {
		if f.State != statePending {
			done++
		}
	}
	assert.Equal(t, settled, done, "what was embedded before the failure is kept")
	files, err := ti.x.st.listFiles(ctx)
	require.NoError(t, err)
	stored := 0
	for _, f := range files {
		if f.State != statePending {
			stored++
		}
	}
	assert.Equal(t, settled, stored)
}

func TestIndexerSyncAppliesChangesInOneBatch(t *testing.T) {
	ti := newTestIndexer(t)
	db := newBatchStorage(ti.db, nil)
	ti.db = db
	ti.reload(t)
	embed := newCountingEmbedder()
	ti.index(t, embed)
	ids := map[string]uint32{}
	for _, f := range ti.x.cat.snapshot() {
		ids[f.Path] = f.ID
	}
	db.batches()

	writeFile(t, ti.dir, "geo/distance.py", "def bearing(a, b):\n    return compass(a, b)\n")
	writeFile(t, ti.dir, "billing/invoice.py", "def total(lines):\n    return sum(lines)\n")
	require.NoError(t, os.Remove(filepath.Join(ti.dir, "auth/session.ts")))
	writeFile(t, ti.dir, "shipping/parcel.go", "package shipping\n\n// Parcel is a parcel.\ntype Parcel struct{}\n")
	require.NoError(t, os.Rename(filepath.Join(ti.dir, "billing/tax.py"), filepath.Join(ti.dir, "billing/rates.py")))
	sum, err := ti.x.sync(context.Background(), []string{"."})
	require.NoError(t, err)
	assert.Equal(t, 2, sum.changed)
	assert.Equal(t, 1, sum.removed)
	assert.Equal(t, 1, sum.added)
	assert.Equal(t, 1, sum.moved)

	batches, singles := db.batches()
	assert.Equal(t, map[string]int{
		vectorsPartition:  1, // the edited and removed files' vectors go
		segmentsPartition: 2, // their stretches go; the new and moved files' name rows come
		filesPartition:    2, // the edited, new and moved files are written; the removed one goes
	}, batches)
	assert.Empty(t, singles)

	got := states(ti.x.cat)
	assert.Equal(t, statePending, got["geo/distance.py"])
	assert.Equal(t, statePending, got["billing/invoice.py"])
	assert.Equal(t, statePending, got["shipping/parcel.go"])
	assert.Equal(t, stateDone, got["billing/rates.py"])
	assert.NotContains(t, got, "auth/session.ts")
	assert.NotContains(t, got, "billing/tax.py")
	assert.Equal(t, ids["billing/tax.py"], ti.file(t, "billing/rates.py").ID)
	assert.NotContains(t, ti.stored(t, ti.x.st.files), ids["auth/session.ts"])
	assert.ElementsMatch(t, []uint32{ids["geo/distance.py"], ids["billing/invoice.py"], ti.file(t, "shipping/parcel.go").ID}, sum.queued)
}

func TestIndexerEventsFollowStorage(t *testing.T) {
	ti := newTestIndexer(t)
	ctx := context.Background()
	embed := newCountingEmbedder()
	_, err := ti.x.adopt(ctx, embed)
	require.NoError(t, err)
	_, err = ti.x.sync(ctx, []string{"."})
	require.NoError(t, err)
	reported := 0
	require.NoError(t, ti.x.run(ctx, embed, nil, func(ev runEvent) {
		if ev.done == 0 {
			return
		}
		reported++
		f := ti.file(t, ev.path)
		assert.Equal(t, ev.state, f.State, ev.path)
		vectors := ti.stored(t, ti.x.st.vectors)
		for _, id := range f.Segments {
			_, found, err := ti.x.st.segment(ctx, id)
			require.NoError(t, err)
			assert.True(t, found, "%s: segment %d is reported before it is stored", ev.path, id)
			if s, _ := ti.x.cat.segment(id); s.hasVec {
				assert.Contains(t, vectors, id, "%s: vector %d is reported before it is stored", ev.path, id)
			}
		}
	}))
	assert.Equal(t, len(fixtureCode), reported)
}
