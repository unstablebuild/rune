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
	"errors"
	"fmt"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/retry"
)

var retryStrategy = retry.SequentialStrategy(30 * time.Millisecond)

const (
	defaultStoreTimeout = 5 * time.Second
)

// History acts as a persisted stack of queries. It provides operations
// to push queries and retrieve previously persisted queries.
// A pop operation is not provided explicitly although calling Add
// when the number of stored queries is equal to the maximum allowed
// will remove the oldest query.
type History struct {
	store   storageapi.Service
	timeout time.Duration
	docID   string
	idx     int
	max     int
	doc     historyDocument
}

// NewHistory allocates storage for a new instance of History and initializes it.
func NewHistory(
	store storageapi.Service, documentID string, maxHistory int,
) *History {
	ret := new(History)
	ret.Init(store, documentID, maxHistory)
	return ret
}

// Init initializes this history with the given store, documentID and maximum history.
func (h *History) Init(
	store storageapi.Service, documentID string, maxHistory int,
) {
	if documentID == "" || store == nil || maxHistory == 0 {
		err := fmt.Sprintf("invalid Init args: documentID=%q, store=%v, max=%d",
			documentID, store, maxHistory)
		panic(err)
	}
	h.store = store
	h.docID = documentID
	h.timeout = defaultStoreTimeout
	h.max = maxHistory
	h.doc.Version = 1
}

// Load fetches any queries persisted in store and populates
// this instance of History.
func (h *History) Load() error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultStoreTimeout)
	defer cancel()

	err := h.store.Get(ctx, h.docID, &h.doc)
	if errors.Is(err, storageapi.ErrNotFound) {
		err = h.store.Create(ctx, h.docID, &h.doc)
	}
	if err != nil {
		return fmt.Errorf("failed to load search history from store: %s", err)
	}
	return nil
}

// Add adds query to the history. If the number of queries persisted
// is greater than the max permitted, then the first query is dropped.
func (h *History) Add(query string) error {
	// next Next should return query
	h.idx = 0

	if query == "" {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultStoreTimeout)
	defer cancel()

	err := storageapi.ConsistentUpdate(ctx, h.store, h.docID, &h.doc, retryStrategy,
		func() ([]storageapi.Update, []storageapi.Precondition) {
			newQueries := make([]string, 0, len(h.doc.Queries)+1)
			seen := map[string]struct{}{query: {}}
			newQueries = append(newQueries, query)
			for _, q := range h.doc.Queries {
				if _, ok := seen[q]; ok {
					continue
				}
				seen[q] = struct{}{}
				newQueries = append(newQueries, q)
			}
			h.doc.Queries = newQueries

			if len(h.doc.Queries) > h.max {
				h.doc.Queries = h.doc.Queries[:h.max]
			}

			return []storageapi.Update{
				{FieldPath: []string{"Queries"}, Value: h.doc.Queries},
				{FieldPath: []string{"Version"}, Value: h.doc.Version + 1},
			}, []storageapi.Precondition{
				{FieldPath: []string{"Version"}, Value: h.doc.Version},
			}
		})
	if err != nil {
		return fmt.Errorf("could not set command history: %v", err)
	}
	return nil
}

// Remove removes all entries in history that match `{baseCmd} {cmd}`, e.g. `!
// echo 2` (baseCmd: `!`; cmd: `echo 2`).
func (h *History) Remove(cmd string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultStoreTimeout)
	defer cancel()

	// anticipate capacity, since the resulting list will be roughly of similar length
	newQueries := make([]string, 0, len(h.doc.Queries))

	err := storageapi.ConsistentUpdate(ctx, h.store, h.docID, &h.doc, retryStrategy,
		func() ([]storageapi.Update, []storageapi.Precondition) {
			newQueries = newQueries[:0]

			// this would be more efficient if the results were sorted but they are not
			for _, q := range h.doc.Queries {
				if q != cmd {
					newQueries = append(newQueries, q)
				}
			}

			h.doc.Queries = newQueries

			return []storageapi.Update{
				{FieldPath: []string{"Queries"}, Value: h.doc.Queries},
				{FieldPath: []string{"Version"}, Value: h.doc.Version + 1},
			}, []storageapi.Precondition{
				{FieldPath: []string{"Version"}, Value: h.doc.Version},
			}
		})
	if err != nil {
		return fmt.Errorf("could not remove item from command history: %v", err)
	}
	return nil
}

// Next returns the next query and updates History
// such that the next call to Next would return the query after.
// If this method is called after the last query has been returned
// the first query is returned instead.
func (h *History) Next() string {
	if len(h.doc.Queries) == 0 {
		return ""
	}
	search := h.doc.Queries[h.idx]
	h.idx++
	if h.idx == len(h.doc.Queries) {
		h.idx = 0
	}
	return search
}

// Slice returns all queries as a slice.
func (h *History) Slice() []string {
	return h.doc.Queries
}

// HistoryIterator reads the persisted history fresh from the store and
// returns an iterator over its entries along with true, or a nil iterator
// and false when no entries are persisted (or the read fails). The args
// parameter is unused — included so the method signature satisfies the
// command.HistoryAccessor interface, which can be used by alias completer
// chains to expose this history as a streaming completer source.
//
// Unlike Slice, this method does not depend on Load having been called
// and reflects the latest persisted state.
func (h *History) HistoryIterator(
	ctx context.Context, _ []string,
) (iterator.Iterator[string], bool) {
	var doc historyDocument
	if err := h.store.Get(ctx, h.docID, &doc); err != nil {
		return nil, false
	}
	if len(doc.Queries) == 0 {
		return nil, false
	}
	return iterator.FromSlice(doc.Queries), true
}

type historyDocument struct {
	Queries []string
	Version int64
}
