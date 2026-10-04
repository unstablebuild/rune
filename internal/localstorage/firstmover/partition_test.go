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

package firstmover

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"go.uber.org/goleak"
)

func TestPartitionResolvesOnceWhileLeaderStable(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	tracked := &partitionCloseTracker{Service: storagestub.NewInMemoryService()}
	leader := New(factoryFor(tracked), lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })

	part, err := leader.Partition("p")
	require.NoError(t, err)

	for i := range 5 {
		require.NoError(t, part.Set(context.Background(),
			"k"+strconv.Itoa(i), &testStruct{A: "v"}))
		var got testStruct
		require.NoError(t, part.Get(context.Background(), "k"+strconv.Itoa(i), &got))
	}

	assert.Equal(t, int32(1), tracked.partitionsOpened.Load(),
		"partition chain must be resolved exactly once while leader is stable")
	assert.Equal(t, int32(0), tracked.partitionsClosed.Load(),
		"cached partition handle must stay open across operations")
}

func TestPartitionDropForwardsToTarget(t *testing.T) {
	ctx := context.Background()
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	leader := New(factoryFor(storagestub.NewInMemoryService()), lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })

	part, err := leader.Partition("p")
	require.NoError(t, err)
	require.NoError(t, part.Set(ctx, "k", &testStruct{A: "v"}))

	droppable, ok := part.(storageapi.DroppableService)
	require.True(t, ok, "partitioned views must be droppable")
	require.NoError(t, droppable.Drop(ctx))

	var got testStruct
	assert.ErrorIs(t, part.Get(ctx, "k", &got), storageapi.ErrNotFound)
}

func TestPartitionApplyBatchForwardsToTarget(t *testing.T) {
	ctx := context.Background()
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	leader := New(factoryFor(storagestub.NewInMemoryService()), lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })

	part, err := leader.Partition("p")
	require.NoError(t, err)
	require.NoError(t, part.Set(ctx, "taken", &testStruct{A: "v"}))

	writer, ok := part.(storageapi.BatchWriter)
	require.True(t, ok, "partitioned views must support batching")
	results, err := writer.ApplyBatch(ctx, []storageapi.BatchOp{
		{Type: storageapi.BatchSet, ID: "fresh", Doc: &testStruct{A: "w"}},
		{Type: storageapi.BatchCreate, ID: "taken", Doc: &testStruct{A: "w"}},
	})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.NoError(t, results[0].Err)
	assert.ErrorIs(t, results[1].Err, storageapi.ErrAlreadyExists)

	var got testStruct
	require.NoError(t, part.Get(ctx, "fresh", &got))
	assert.Equal(t, "w", got.A)
}

func TestPartitionCacheInvalidatesOnLeadershipChange(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	first := &partitionCloseTracker{Service: storagestub.NewInMemoryService()}
	leader := New(factoryFor(first), lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })

	part, err := leader.Partition("p")
	require.NoError(t, err)
	require.NoError(t, part.Set(context.Background(), "k", &testStruct{A: "v"}))
	require.Equal(t, int32(1), first.partitionsOpened.Load())
	require.Equal(t, int32(0), first.partitionsClosed.Load())

	// Simulate a leadership transition by swapping the active backend
	// under the same lock the service uses.
	second := &partitionCloseTracker{Service: storagestub.NewInMemoryService()}
	leader.mu.Lock()
	leader.svc = second
	leader.active = second
	leader.mu.Unlock()

	require.NoError(t, part.Set(context.Background(), "k", &testStruct{A: "v2"}))

	assert.Equal(t, int32(1), first.partitionsClosed.Load(),
		"old leader-side handle must be closed exactly once on invalidation")
	assert.Equal(t, int32(1), second.partitionsOpened.Load(),
		"new active backend must be resolved after leadership change")
}

func TestPartitionCacheNeverClosesFollowerHandles(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	tracked := &partitionCloseTracker{Service: storagestub.NewInMemoryService()}
	svc := New(factoryFor(tracked), lockFile, cfg)
	t.Cleanup(func() { _ = svc.Close() })

	part, err := svc.Partition("p")
	require.NoError(t, err)
	require.NoError(t, part.Set(context.Background(), "k", &testStruct{A: "v"}))

	// Force the resolved view to be treated as a follower-side handle:
	// active != svc means releaseWalked is a no-op for these handles.
	follower := &partitionCloseTracker{Service: storagestub.NewInMemoryService()}
	svc.mu.Lock()
	svc.active = follower
	svc.mu.Unlock()

	require.NoError(t, part.Set(context.Background(), "k", &testStruct{A: "v2"}))
	var got testStruct
	require.NoError(t, part.Get(context.Background(), "k", &got))
	assert.Equal(t, "v2", got.A)

	// Swap active again so the follower-cached handle is invalidated.
	other := &partitionCloseTracker{Service: storagestub.NewInMemoryService()}
	svc.mu.Lock()
	svc.active = other
	svc.mu.Unlock()
	require.NoError(t, part.Set(context.Background(), "k", &testStruct{A: "v3"}))

	assert.Equal(t, int32(0), follower.partitionsClosed.Load(),
		"follower-side cached handles must never be closed")
}

func TestPartitionCloseReleasesCachedHandles(t *testing.T) {
	lockFile := makeTempLockFile(t)
	cfg := testConfig()
	tracked := &partitionCloseTracker{Service: storagestub.NewInMemoryService()}
	leader := New(factoryFor(tracked), lockFile, cfg)
	t.Cleanup(func() { _ = leader.Close() })

	part, err := leader.Partition("p")
	require.NoError(t, err)
	require.NoError(t, part.Set(context.Background(), "k", &testStruct{A: "v"}))
	require.Equal(t, int32(1), tracked.partitionsOpened.Load())
	require.Equal(t, int32(0), tracked.partitionsClosed.Load())

	require.NoError(t, part.Close())
	assert.Equal(t, int32(1), tracked.partitionsClosed.Load(),
		"Close must release the cached leader-side handle exactly once")

	require.NoError(t, part.Close())
	assert.Equal(t, int32(1), tracked.partitionsClosed.Load(),
		"a second Close must not double-close cached handles")
}

// BenchmarkPartitionedGet locks in the per-op cost of a partitioned
// Get against silent reintroduction of per-op partition resolution
// (which fsyncs on a real bbolt backend).
func BenchmarkPartitionedGet(b *testing.B) {
	lockFile := makeTempLockFileB(b)
	cfg := testConfig()
	leader := New(factoryFor(storagestub.NewInMemoryService()), lockFile, cfg)
	b.Cleanup(func() { _ = leader.Close() })

	part, err := leader.Partition("p")
	require.NoError(b, err)
	require.NoError(b, part.Set(context.Background(), "k", &testStruct{A: "v"}))

	var got testStruct
	b.ResetTimer()
	for range b.N {
		if err := part.Get(context.Background(), "k", &got); err != nil {
			b.Fatal(err)
		}
	}
}

func makeTempLockFileB(b *testing.B) string {
	b.Helper()
	f, err := os.CreateTemp("", "")
	require.NoError(b, err)
	require.NoError(b, f.Close())
	require.NoError(b, os.Remove(f.Name()))
	b.Cleanup(func() { _ = os.Remove(f.Name()) })
	return f.Name()
}

// fileBackedRoot is the shared state behind a tree of
// fileBackedService handles. It models bbolt's on-disk, refcounted
// reality: every successfully opened partition handle leaves a unique
// marker file under root, and only Close removes it. The actual
// document data is delegated to one shared storagestub tree keyed by
// partition path, so values persist across handle open/close/reopen
// and across leadership changes (every peer's leader handle resolves
// to the same backend).
type fileBackedRoot struct {
	root    string
	backend storageapi.Service // shared storagestub tree (path-keyed)
	counter atomic.Int64       // unique marker file suffix source
}

func newFileBackedRoot(t *testing.T) *fileBackedRoot {
	t.Helper()
	return &fileBackedRoot{
		root:    t.TempDir(),
		backend: storagestub.NewInMemoryService(),
	}
}

// newLeaderHandle returns a fresh root-path handle over the shared
// backend, mirroring StorageFactory: each leadership acquisition opens
// its own handle (and its own marker) while the data behind it is
// shared across peers.
func (r *fileBackedRoot) newLeaderHandle() (storageapi.Service, error) {
	return newFileBackedHandle(r, nil, r.backend)
}

// openHandles counts marker files currently on disk, i.e. partition
// handles that have been opened but not yet closed.
func (r *fileBackedRoot) openHandles() int {
	return len(r.walkMarkerFiles())
}

// walkMarkerFiles lists the remaining marker files for diagnostics.
func (r *fileBackedRoot) walkMarkerFiles() []string {
	var out []string
	_ = filepath.Walk(r.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, ".handle") {
			out = append(out, path)
		}
		return nil
	})
	return out
}

// fileBackedService is a black-box storageapi.Service that owns one
// open partition handle (one marker file). Create/Set/Update/Get/
// Delete/List delegate to the shared storagestub partition bound to
// this handle's path; Partition opens a child handle for the nested
// path; Close removes exactly this handle's marker exactly once.
type fileBackedService struct {
	storageapi.Service // the shared storagestub partition for this path
	root               *fileBackedRoot
	chain              []string
	marker             string
	closeOnce          sync.Once
}

func newFileBackedHandle(
	r *fileBackedRoot, chain []string, backend storageapi.Service,
) (*fileBackedService, error) {
	dir := filepath.Join(append([]string{r.root}, sanitize(chain)...)...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	marker := filepath.Join(dir,
		strconv.FormatInt(r.counter.Add(1), 10)+".handle")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		return nil, err
	}
	return &fileBackedService{
		Service: backend,
		root:    r,
		chain:   chain,
		marker:  marker,
	}, nil
}

func (s *fileBackedService) Partition(name string) (storageapi.Service, error) {
	child, err := s.Service.Partition(name)
	if err != nil {
		return nil, err
	}
	chain := make([]string, len(s.chain)+1)
	copy(chain, s.chain)
	chain[len(s.chain)] = name
	return newFileBackedHandle(s.root, chain, child)
}

func (s *fileBackedService) Close() error {
	var err error
	s.closeOnce.Do(func() {
		err = os.Remove(s.marker)
	})
	return err
}

// sanitize maps a partition chain to filesystem-safe directory
// segments so nested paths get distinct marker directories.
func sanitize(chain []string) []string {
	if len(chain) == 0 {
		return []string{"__root__"}
	}
	out := make([]string, len(chain))
	for i, seg := range chain {
		out[i] = "p_" + strings.NewReplacer("/", "_", "\\", "_").Replace(seg)
	}
	return out
}

func TestPartitionLifecycleAcrossClusterNoLeaks(t *testing.T) {
	// Capture goroutines that exist before this test starts (package
	// init, other parallel tests) so goleak only flags firstmover's own
	// leaks. gRPC transport goroutines spawned and torn down within the
	// test are caught by the eventual handle/goroutine settling below.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	root := newFileBackedRoot(t)
	factory := func() (storageapi.Service, error) { return root.newLeaderHandle() }
	lockFile := makeTempLockFile(t)
	cfg := testConfig()

	ctx := context.Background()

	// Establish the leader deterministically: a Set blocks until this
	// peer is ready as leader, so the two peers created afterwards join
	// as followers rather than racing for the socket lock.
	leader := New(factory, lockFile, cfg)
	require.NoError(t, leader.Set(ctx, "__seed", &testStruct{A: "seed"}))
	require.True(t, leader.IsLeader(), "seeded peer must be the leader")

	follower1 := New(factory, lockFile, cfg)
	follower2 := New(factory, lockFile, cfg)
	// Touch followers so they connect to the leader before any ops.
	var seed testStruct
	require.NoError(t, follower1.Get(ctx, "__seed", &seed))
	require.NoError(t, follower2.Get(ctx, "__seed", &seed))
	require.False(t, follower1.IsLeader())
	require.False(t, follower2.IsLeader())

	// The root leader handle is opened once leadership is won.
	assert.GreaterOrEqual(t, root.openHandles(), 1,
		"leader root handle must be open")

	// Closing a leader's own partitioned view must release its cached
	// handle chain. Exercise this on paths no follower touches, so the
	// leader-side gRPC server's own partition cache (which legitimately
	// pins follower-driven paths until leader shutdown) does not
	// confound the handle-count delta.
	assertLeaderViewReleasesHandles(t, ctx, root, leader, []string{"leaderonly"})
	assertLeaderViewReleasesHandles(t, ctx, root, leader, []string{"leaderonly", "deep"})

	// Full open/op/close/reopen lifecycle across all three peers for a
	// single-level and a nested partition, asserting data correctness
	// purely through the public surface.
	partitionLifecycle(t, ctx, leader, follower1, follower2, []string{"A"})
	partitionLifecycle(t, ctx, leader, follower1, follower2, []string{"A", "B"})

	// Nested isolation: A/B writes must not leak into A or sibling B.
	assertNestedIsolation(t, ctx, leader)

	require.NoError(t, follower1.Close())
	require.NoError(t, follower2.Close())
	require.NoError(t, leader.Close())

	// The leader closes its root backend asynchronously in lead()'s
	// defer after quitCh, so settle before the hard assertion.
	require.Eventually(t, func() bool { return root.openHandles() == 0 },
		5*time.Second, 10*time.Millisecond,
		"all partition handles must be released after cluster shutdown")
	require.Equal(t, 0, root.openHandles(),
		"leaked partition handles: %v", root.walkMarkerFiles())
}

// partitionLifecycle exercises open/op/close/reopen of one partition
// path across the leader and both followers, asserting data
// correctness purely through the public storageapi.Service surface.
func partitionLifecycle(
	t *testing.T, ctx context.Context,
	leader, follower1, follower2 *Service, chain []string,
) {
	t.Helper()
	name := strings.Join(chain, "/")

	// 1. Leader creates the partition and writes a value.
	aLeader := partition(t, leader, chain)
	require.NoError(t, aLeader.Set(ctx, "k", &testStruct{A: "v1"}))

	// 2. Follower1 connects, reads the leader's value, writes its own,
	//    then closes and reopens the view — data must persist.
	a1 := partition(t, follower1, chain)
	var got testStruct
	require.NoError(t, a1.Get(ctx, "k", &got))
	assert.Equal(t, "v1", got.A, "follower must read leader's value for %q", name)
	require.NoError(t, a1.Set(ctx, "k2", &testStruct{A: "f1"}))
	require.NoError(t, a1.Close()) // follower-side close: no-op on disk

	a1b := partition(t, follower1, chain)
	require.NoError(t, a1b.Get(ctx, "k", &got))
	assert.Equal(t, "v1", got.A, "reopened follower view must still read %q", name)
	require.NoError(t, a1b.Get(ctx, "k2", &got))
	assert.Equal(t, "f1", got.A)
	require.NoError(t, a1b.Close())

	// 3. Query via the leader's existing view: List must return both
	//    documents (storagestub exposes values, not IDs, via the
	//    iterator). Reusing aLeader keeps the chain owned by one view,
	//    released by its Close below.
	values := listValues(t, ctx, aLeader)
	assert.Contains(t, values, "v1")
	assert.Contains(t, values, "f1")

	// 4. Close the leader's view; it must not error.
	require.NoError(t, aLeader.Close())

	// 5. Follower2 opens the partition, sees persisted data, writes
	//    more, then closes cleanly.
	a2 := partition(t, follower2, chain)
	require.NoError(t, a2.Get(ctx, "k", &got))
	assert.Equal(t, "v1", got.A, "follower2 must read persisted value for %q", name)
	require.NoError(t, a2.Get(ctx, "k2", &got))
	assert.Equal(t, "f1", got.A)
	require.NoError(t, a2.Set(ctx, "k3", &testStruct{A: "f2"}))
	require.NoError(t, a2.Get(ctx, "k3", &got))
	assert.Equal(t, "f2", got.A)
	require.NoError(t, a2.Close())
}

// assertLeaderViewReleasesHandles verifies the RUNE-226 cache contract
// black-box: a leader-side partitioned view opens its handle chain
// lazily on first op and releases it on Close. chain must be a path no
// follower touches, so the leader-side gRPC server's partition cache
// does not also pin it.
func assertLeaderViewReleasesHandles(
	t *testing.T, ctx context.Context,
	root *fileBackedRoot, leader *Service, chain []string,
) {
	t.Helper()
	name := strings.Join(chain, "/")

	before := root.openHandles()
	view := partition(t, leader, chain)
	require.NoError(t, view.Set(ctx, "k", &testStruct{A: "v"}))
	require.Equal(t, before+len(chain), root.openHandles(),
		"leader view for %q must open one handle per chain segment", name)

	require.NoError(t, view.Close())
	require.Equal(t, before, root.openHandles(),
		"closing leader view for %q must release its whole handle chain", name)
}

func assertNestedIsolation(
	t *testing.T, ctx context.Context, leader *Service,
) {
	t.Helper()
	a := partition(t, leader, []string{"A"})
	defer func() { _ = a.Close() }()
	ab := partition(t, leader, []string{"A", "B"})
	defer func() { _ = ab.Close() }()
	b := partition(t, leader, []string{"B"})
	defer func() { _ = b.Close() }()

	require.NoError(t, ab.Set(ctx, "iso", &testStruct{A: "nested"}))

	var got testStruct
	err := a.Get(ctx, "iso", &got)
	assert.ErrorIs(t, err, storageapi.ErrNotFound,
		"A/B write must not appear in A")
	err = b.Get(ctx, "iso", &got)
	assert.ErrorIs(t, err, storageapi.ErrNotFound,
		"A/B write must not appear in sibling B")
	require.NoError(t, ab.Get(ctx, "iso", &got))
	assert.Equal(t, "nested", got.A)
}

// partition resolves a (possibly nested) partition chain from svc.
func partition(t *testing.T, svc storageapi.Service, chain []string) storageapi.Service {
	t.Helper()
	cur := svc
	for _, name := range chain {
		next, err := cur.Partition(name)
		require.NoError(t, err)
		cur = next
	}
	return cur
}

// listValues drains a partition List and returns the A field of each
// document, closing the iterator.
func listValues(t *testing.T, ctx context.Context, svc storageapi.Service) []string {
	t.Helper()
	it, err := svc.List(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = it.Close() }()
	var out []string
	for it.HasNext() {
		var doc testStruct
		require.NoError(t, it.NextTo(&doc))
		out = append(out, doc.A)
	}
	return out
}
