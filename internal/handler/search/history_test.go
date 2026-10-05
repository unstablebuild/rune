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

package search

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
)

func TestHistory(t *testing.T) {
	store := storagestub.NewInMemoryService()
	history := NewHistory(store, "id", 4)
	err := history.Load()
	require.NoError(t, err)

	require.NoError(t, history.Add("jmac"))
	require.NoError(t, history.Add("jj"))

	assert.Equal(t, "jj", history.Next())
	assert.Equal(t, "jmac", history.Next())
	assert.Equal(t, "jj", history.Next())

	// adding another one resets history
	require.NoError(t, history.Add("Kom"))
	assert.Equal(t, "Kom", history.Next())
	assert.Equal(t, "jj", history.Next())

	// queries are persisted across stores
	history2 := NewHistory(store, "id", 4)
	err = history2.Load()
	require.NoError(t, err)
	assert.Equal(t, "Kom", history2.Next())
	assert.Equal(t, "jj", history2.Next())
	assert.Equal(t, "jmac", history2.Next())
	assert.Equal(t, "Kom", history2.Next())

	// history pointers are kept in isolation
	assert.Equal(t, "jmac", history.Next())
	assert.Equal(t, "jj", history2.Next())

	// test max
	require.NoError(t, history.Add("4"))
	require.NoError(t, history.Add("5"))
	assert.Equal(t, "5", history.Next())
	assert.Equal(t, "4", history.Next())
	assert.Equal(t, "Kom", history.Next())
	assert.Equal(t, "jj", history.Next())
	assert.Equal(t, "5", history.Next())

	// queries are NOT persisted across stores with diff IDs
	history3 := NewHistory(store, "id2", 4)
	err = history3.Load()
	require.NoError(t, err)
	assert.Equal(t, "", history3.Next())
	require.NoError(t, history3.Add("a"))
	assert.Equal(t, "a", history3.Next())
	assert.Equal(t, "a", history3.Next())

	// history items can be deleted
	require.NoError(t, history.Add("ToRemove"))
	assert.Equal(t, "ToRemove", history.Next())
	require.NoError(t, history.Remove("ToRemove"))
	found := slices.Contains(history.Slice(), "ToRemove")
	assert.False(t, found)
}

func TestHistoryIterator(t *testing.T) {
	store := storagestub.NewInMemoryService()
	history := NewHistory(store, "id-iter", 4)
	require.NoError(t, history.Load())

	// fresh accessor on an empty store reports no history.
	it, ok := history.HistoryIterator(context.Background(), nil)
	assert.False(t, ok)
	assert.Nil(t, it)

	// populate the store with three entries; HistoryIterator must
	// stream them in order without depending on Load (it reads fresh
	// from storage on every call).
	require.NoError(t, history.Add("alpha"))
	require.NoError(t, history.Add("beta"))
	require.NoError(t, history.Add("gamma"))

	other := NewHistory(store, "id-iter", 4)
	// HistoryIterator does not require Load: it reads directly from
	// the store via storageapi.Get.
	it, ok = other.HistoryIterator(context.Background(), nil)
	require.True(t, ok)
	require.NotNil(t, it)

	got, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	// History.Add prepends so most-recent comes first.
	assert.Equal(t, []string{"gamma", "beta", "alpha"}, got)
}

func TestHistoryAddDeduplicates(t *testing.T) {
	store := storagestub.NewInMemoryService()
	history := NewHistory(store, "id-dedup", 8)
	require.NoError(t, history.Load())

	require.NoError(t, history.Add("alpha"))
	require.NoError(t, history.Add("beta"))
	require.NoError(t, history.Add("gamma"))

	// Re-add an existing entry: the in-memory slice must contain
	// each entry exactly once, with the re-added one promoted to
	// the front.
	require.NoError(t, history.Add("alpha"))
	assert.Equal(t, []string{"alpha", "gamma", "beta"}, history.Slice())

	// Persisted form must match: a fresh History over the same
	// store sees only the deduped, MRU-ordered entries.
	other := NewHistory(store, "id-dedup", 8)
	it, ok := other.HistoryIterator(context.Background(), nil)
	require.True(t, ok)
	got, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha", "gamma", "beta"}, got)

	// Re-adding the most-recent entry is a no-op (other than
	// resetting the Next cursor) — order is preserved, length
	// unchanged.
	require.NoError(t, history.Add("alpha"))
	assert.Equal(t, []string{"alpha", "gamma", "beta"}, history.Slice())
}

func TestHistoryAddNormalizesPersistedDuplicates(t *testing.T) {
	store := storagestub.NewInMemoryService()
	history := NewHistory(store, "id-dedup-existing", 8)
	require.NoError(t, history.Load())

	// Simulate an older persisted document, written before Add enforced
	// uniqueness, that already contains duplicates unrelated to the next
	// query being added.
	history.doc.Queries = []string{"beta", "alpha", "beta", "gamma", "alpha"}
	require.NoError(t, store.Set(context.Background(), "id-dedup-existing", &history.doc))

	require.NoError(t, history.Add("delta"))
	assert.Equal(t, []string{"delta", "beta", "alpha", "gamma"}, history.Slice())

	other := NewHistory(store, "id-dedup-existing", 8)
	it, ok := other.HistoryIterator(context.Background(), nil)
	require.True(t, ok)
	got, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"delta", "beta", "alpha", "gamma"}, got)
}
