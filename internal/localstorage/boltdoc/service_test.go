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

package boltdoc_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/document/doctest"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/retry"
	"unstable.build/rune/internal/localstorage/bluestore"

	"unstable.build/rune/internal/localstorage/boltdoc"
)

func TestConcurrentServicesShareDBLifecycle(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "rune.db")

	svcA, err := boltdoc.New(dbPath, docbson.Marshaler())
	require.NoError(t, err)
	svcB, err := boltdoc.New(dbPath, docbson.Marshaler())
	require.NoError(t, err)
	t.Cleanup(func() { _ = svcB.Close() })

	type rec struct{ Name string }
	require.NoError(t, svcB.Create(ctx, "shared", &rec{Name: "b"}))

	require.NoError(t, svcA.Close())

	var got rec
	require.NoError(t, svcB.Get(ctx, "shared", &got),
		"closing one service must not close the DB shared with others")
	assert.Equal(t, "b", got.Name)
}

func TestStoreImplementsDocumentService(t *testing.T) {
	doctest.TestDocumentService(t, func(t *testing.T) document.Service {
		dir := t.TempDir()
		svc, err := boltdoc.New(filepath.Join(dir, "rune.db"), docbson.Marshaler())
		require.NoError(t, err)
		t.Cleanup(func() { _ = svc.Close() })
		return bluestore.AdaptFrom(svc)
	})
}

func TestPartitionDrop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc, err := boltdoc.New(filepath.Join(dir, "rune.db"), docbson.Marshaler())
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	type rec struct{ Name string }

	dropped, err := svc.Partition("dropped")
	require.NoError(t, err)
	kept, err := svc.Partition("kept")
	require.NoError(t, err)
	nested, err := dropped.Partition("nested")
	require.NoError(t, err)

	for _, part := range []storageapi.Service{dropped, kept, nested} {
		require.NoError(t, part.Create(ctx, "doc", &rec{Name: "v"}))
	}

	droppable, ok := dropped.(storageapi.DroppableService)
	require.True(t, ok, "bolt partitions must be droppable")
	require.NoError(t, droppable.Drop(ctx))

	var got rec
	assert.ErrorIs(t, dropped.Get(ctx, "doc", &got), storageapi.ErrNotFound)
	assert.NoError(t, kept.Get(ctx, "doc", &got))
	assert.NoError(t, nested.Get(ctx, "doc", &got))

	require.NoError(t, dropped.Create(ctx, "doc", &rec{Name: "again"}))
	require.NoError(t, dropped.Get(ctx, "doc", &got))
}

func TestPartitionApplyBatch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc, err := boltdoc.New(filepath.Join(dir, "rune.db"), docbson.Marshaler())
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	type rec struct {
		Name    string
		Version int
	}

	part, err := svc.Partition("p")
	require.NoError(t, err)
	require.NoError(t, part.Create(ctx, "taken", &rec{Name: "v", Version: 1}))

	writer, ok := part.(storageapi.BatchWriter)
	require.True(t, ok, "bolt partitions must support batching")
	results, err := writer.ApplyBatch(ctx, []storageapi.BatchOp{
		{Type: storageapi.BatchCreate, ID: "fresh",
			Doc: &rec{Name: "w", Version: 1}},
		{Type: storageapi.BatchCreate, ID: "taken",
			Doc: &rec{Name: "w", Version: 1}},
		{Type: storageapi.BatchUpdate, ID: "taken", Updates: []storageapi.Update{
			{FieldPath: []string{"Version"}, Value: 2},
		}, Preconditions: []storageapi.Precondition{
			{FieldPath: []string{"Version"}, Value: 1},
		}},
		{Type: storageapi.BatchUpdate, ID: "taken", Updates: []storageapi.Update{
			{FieldPath: []string{"Version"}, Value: 3},
		}, Preconditions: []storageapi.Precondition{
			{FieldPath: []string{"Version"}, Value: 1},
		}},
	})
	require.NoError(t, err)
	require.Len(t, results, 4)
	assert.NoError(t, results[0].Err)
	assert.ErrorIs(t, results[1].Err, storageapi.ErrAlreadyExists)
	assert.NoError(t, results[2].Err)
	assert.ErrorIs(t, results[3].Err, storageapi.ErrPreconditionFailed)

	var got rec
	require.NoError(t, part.Get(ctx, "fresh", &got))
	assert.Equal(t, "w", got.Name)
	require.NoError(t, part.Get(ctx, "taken", &got))
	assert.Equal(t, 2, got.Version)
}

func TestConsistentUpdate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc, err := boltdoc.New(filepath.Join(dir, "rune.db"), docbson.Marshaler())
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	type counter struct {
		Version int64
		Value   string
	}

	require.NoError(t, svc.Create(ctx, "counter", &counter{Version: 1, Value: "init"}))

	strategy := retry.CombinedStrategy(
		retry.ExponentialStrategy(time.Millisecond, 50*time.Millisecond),
		retry.LimitStrategy(20),
	)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			var c counter
			err := storageapi.ConsistentUpdate(ctx, svc, "counter", &c, strategy,
				func() ([]storageapi.Update, []storageapi.Precondition) {
					return []storageapi.Update{
						{FieldPath: []string{"Version"}, Value: c.Version + 1},
					}, []storageapi.Precondition{
						{FieldPath: []string{"Version"}, Value: c.Version},
					}
				})
			assert.NoError(t, err)
		})
	}
	wg.Wait()

	var got counter
	require.NoError(t, svc.Get(ctx, "counter", &got))
	assert.Equal(t, int64(3), got.Version, "both increments should have been applied")
}

func TestPartitionIsolation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	svc, err := boltdoc.New(filepath.Join(dir, "rune.db"), docbson.Marshaler())
	require.NoError(t, err)
	t.Cleanup(func() { _ = svc.Close() })

	type rec struct{ Name string }

	require.NoError(t, svc.Create(ctx, "a", &rec{Name: "root-a"}))

	part, err := svc.Partition("memories")
	require.NoError(t, err)
	require.NoError(t, part.Create(ctx, "a", &rec{Name: "partition-a"}))

	var rootGot, partGot rec
	require.NoError(t, svc.Get(ctx, "a", &rootGot))
	require.NoError(t, part.Get(ctx, "a", &partGot))
	assert.Equal(t, "root-a", rootGot.Name)
	assert.Equal(t, "partition-a", partGot.Name)
}
