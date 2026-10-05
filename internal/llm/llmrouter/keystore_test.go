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

package llmrouter

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
)

func TestKeyStore_AddFirstKeyBecomesActive(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())

	require.NoError(t, s.add(ctx, ProviderOpenAI, "work", "k1", ""))
	active, err := s.active(ctx, ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "k1", active)

	name, err := s.activeName(ctx, ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "work", name)
}

func TestKeyStore_AddSecondKeyKeepsActive(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())

	require.NoError(t, s.add(ctx, ProviderOpenAI, "work", "k1", ""))
	require.NoError(t, s.add(ctx, ProviderOpenAI, "home", "k2", ""))

	active, err := s.active(ctx, ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "k1", active, "adding a second key must not change the active key")
}

func TestKeyStore_RegionTravelsWithKey(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())

	require.NoError(t, s.add(ctx, ProviderBedrock, "work", "k1", "eu-west-1"))
	key, region, err := s.activeWithRegion(ctx, ProviderBedrock)
	require.NoError(t, err)
	assert.Equal(t, "k1", key)
	assert.Equal(t, "eu-west-1", region)

	// Re-adding under a new region replaces the old scope.
	require.NoError(t, s.add(ctx, ProviderBedrock, "work", "k1", "us-east-1"))
	_, region, err = s.activeWithRegion(ctx, ProviderBedrock)
	require.NoError(t, err)
	assert.Equal(t, "us-east-1", region)

	regions, err := s.regions(ctx, ProviderBedrock)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"work": "us-east-1"}, regions)

	require.NoError(t, s.remove(ctx, ProviderBedrock, "work"))
	regions, err = s.regions(ctx, ProviderBedrock)
	require.NoError(t, err)
	assert.Empty(t, regions)
}

func TestKeyStore_RegionlessProvidersStayRegionless(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())

	require.NoError(t, s.add(ctx, ProviderOpenAI, "work", "k1", ""))
	key, region, err := s.activeWithRegion(ctx, ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "k1", key)
	assert.Empty(t, region)
}

func TestKeyStore_Use(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())
	require.NoError(t, s.add(ctx, ProviderOpenAI, "work", "k1", ""))
	require.NoError(t, s.add(ctx, ProviderOpenAI, "home", "k2", ""))

	require.NoError(t, s.use(ctx, ProviderOpenAI, "home"))
	active, err := s.active(ctx, ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "k2", active)

	err = s.use(ctx, ProviderOpenAI, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no openai api key named "missing"`)
}

func TestKeyStore_RemovePromotesActive(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())
	require.NoError(t, s.add(ctx, ProviderOpenAI, "work", "k1", ""))
	require.NoError(t, s.add(ctx, ProviderOpenAI, "home", "k2", ""))

	require.NoError(t, s.remove(ctx, ProviderOpenAI, "work"))
	active, err := s.active(ctx, ProviderOpenAI)
	require.NoError(t, err)
	assert.Equal(t, "k2", active, "removing the active key must promote a remaining key")

	require.NoError(t, s.remove(ctx, ProviderOpenAI, "missing"),
		"removing a key that does not exist must be a no-op")
}

func TestKeyStore_RemoveLastClearsActive(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())
	require.NoError(t, s.add(ctx, ProviderOpenAI, "work", "k1", ""))
	require.NoError(t, s.remove(ctx, ProviderOpenAI, "work"))

	_, err := s.active(ctx, ProviderOpenAI)
	assert.ErrorIs(t, err, ErrAPIKeyNotSet)
}

func TestKeyStore_Names(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())
	require.NoError(t, s.add(ctx, ProviderAnthropic, "work", "k1", ""))
	require.NoError(t, s.add(ctx, ProviderAnthropic, "home", "k2", ""))

	names, err := s.names(ctx, ProviderAnthropic)
	require.NoError(t, err)
	assert.Equal(t, []string{"home", "work"}, names)
}

func TestKeyStore_ActiveUnsetReturnsErr(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())
	_, err := s.active(ctx, ProviderGemini)
	assert.ErrorIs(t, err, ErrAPIKeyNotSet)
}

func TestKeyStore_NilStoragePanics(t *testing.T) {
	assert.PanicsWithValue(t,
		"llmrouter: newKeyStore: storage must not be nil",
		func() { newKeyStore(nil) })
}

func TestKeyStore_MutateLegacyDocWithoutVersion(t *testing.T) {
	ctx := context.Background()
	svc := storagestub.NewInMemoryService()
	s := newKeyStore(svc)

	type legacyKeys struct {
		Keys   map[string]string
		Active string
	}
	id := keyStoreDocID(ProviderGemini)
	require.NoError(t, svc.Create(ctx, id, &legacyKeys{
		Keys:   map[string]string{"work": "k1", "home": "k2"},
		Active: "work",
	}))

	require.NoError(t, s.remove(ctx, ProviderGemini, "work"))
	active, err := s.active(ctx, ProviderGemini)
	require.NoError(t, err)
	assert.Equal(t, "k2", active)

	require.NoError(t, s.use(ctx, ProviderGemini, "home"))
}

func TestKeyStore_VersionPreconditionIsInt64(t *testing.T) {
	ctx := context.Background()
	svc := storagestub.NewInMemoryService()
	s := newKeyStore(svc)
	require.NoError(t, s.add(ctx, ProviderGemini, "work", "k1", ""))

	id := keyStoreDocID(ProviderGemini)
	var doc providerKeys
	require.NoError(t, svc.Get(ctx, id, &doc))
	noop := []storageapi.Update{{FieldPath: []string{"Active"}, Value: "work"}}

	err := svc.Update(ctx, id, noop,
		storageapi.Precondition{FieldPath: []string{"Version"}, Value: int(doc.Version)})
	require.ErrorIs(t, err, storageapi.ErrPreconditionFailed,
		"stored Version is int64; an int precondition must not match")

	require.NoError(t, svc.Update(ctx, id, noop,
		storageapi.Precondition{FieldPath: []string{"Version"}, Value: doc.Version}),
		"stored Version must satisfy an int64 precondition")
}

func TestKeyStore_ConcurrentAddsNoLostWrites(t *testing.T) {
	ctx := context.Background()
	s := newKeyStore(storagestub.NewInMemoryService())

	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			assert.NoError(t, s.add(ctx, ProviderOpenAI, fmt.Sprintf("k%02d", i), fmt.Sprintf("v%02d", i), ""))
		}(i)
	}
	wg.Wait()

	names, err := s.names(ctx, ProviderOpenAI)
	require.NoError(t, err)
	assert.Len(t, names, n)
}
