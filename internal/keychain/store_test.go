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

package keychain

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
)

const testScope = "/data"

type doc struct {
	Value string `json:"value"`
}

func TestStoreKeepsDocumentsInKeyring(t *testing.T) {
	kr := newFakeKeyring()
	fallback := newFakeStorage()
	s := NewStore(kr, testScope, fallback)

	require.NoError(t, s.Set(t.Context(), "tok", doc{Value: "secret"}))

	assert.JSONEq(t, `{"value":"secret"}`, kr.items[Service+"|"+testScope+"/tok"])
	assertMissing(t, fallback, "tok")
	assert.Equal(t, "secret", get(t, s, "tok"))
}

func TestStoreMovesFallbackDocumentIntoKeyring(t *testing.T) {
	kr := newFakeKeyring()
	fallback := newFakeStorage()
	require.NoError(t, fallback.Set(t.Context(), "tok", doc{Value: "legacy"}))
	s := NewStore(kr, testScope, fallback)

	assert.Equal(t, "legacy", get(t, s, "tok"))

	assert.JSONEq(t, `{"value":"legacy"}`, kr.items[Service+"|"+testScope+"/tok"])
	assertMissing(t, fallback, "tok")
	assert.Equal(t, "legacy", get(t, s, "tok"))
}

func TestStoreFallsBackWhenKeyringFails(t *testing.T) {
	unavailable := errors.New("no secret service")
	tests := []struct {
		name  string
		setup func(kr *fakeKeyring)
	}{
		{
			name:  "set fails",
			setup: func(kr *fakeKeyring) { kr.setErr = unavailable },
		},
		{
			name: "every call fails",
			setup: func(kr *fakeKeyring) {
				kr.getErr, kr.setErr, kr.deleteErr = unavailable, unavailable, unavailable
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kr := newFakeKeyring()
			tt.setup(kr)
			fallback := newFakeStorage()
			s := NewStore(kr, testScope, fallback)

			require.NoError(t, s.Set(t.Context(), "tok", doc{Value: "secret"}))
			assert.Equal(t, "secret", get(t, s, "tok"))

			var stored doc
			require.NoError(t, fallback.Get(t.Context(), "tok", &stored))
			assert.Equal(t, "secret", stored.Value)
		})
	}
}

// A keyring that failed once is not trusted with later writes, so a
// token refreshed into the fallback is never split from the keyring's.
func TestStoreStopsWritingKeyringAfterFailure(t *testing.T) {
	kr := newFakeKeyring()
	kr.setErr = errors.New("locked")
	s := NewStore(kr, testScope, newFakeStorage())
	require.NoError(t, s.Set(t.Context(), "tok", doc{Value: "first"}))

	kr.setErr = nil
	require.NoError(t, s.Set(t.Context(), "tok", doc{Value: "second"}))

	assert.Empty(t, kr.items)
	assert.Equal(t, "second", get(t, s, "tok"))
}

// The keyring copy can be stale when a later write went to the
// fallback; the fallback copy must win.
func TestStoreFallbackCopyShadowsKeyring(t *testing.T) {
	kr := newFakeKeyring()
	kr.items[Service+"|"+testScope+"/tok"] = `{"value":"stale"}`
	kr.setErr = errors.New("locked")
	fallback := newFakeStorage()
	require.NoError(t, fallback.Set(t.Context(), "tok", doc{Value: "fresh"}))

	s := NewStore(kr, testScope, fallback)

	assert.Equal(t, "fresh", get(t, s, "tok"))
}

func TestStoreSetRewritesFallbackItCannotDelete(t *testing.T) {
	kr := newFakeKeyring()
	fallback := newFakeStorage()
	require.NoError(t, fallback.Set(t.Context(), "tok", doc{Value: "old"}))
	fallback.deleteErr = errors.New("disk full")
	s := NewStore(kr, testScope, fallback)

	require.NoError(t, s.Set(t.Context(), "tok", doc{Value: "new"}))

	assert.Equal(t, "new", get(t, s, "tok"))
}

func TestStoreGetMissing(t *testing.T) {
	s := NewStore(newFakeKeyring(), testScope, newFakeStorage())

	err := s.Get(t.Context(), "tok", &doc{})

	assert.ErrorIs(t, err, storageapi.ErrNotFound)
}

// When the fallback cannot say whether it holds a copy, the keyring's
// may be stale, so it is not consulted.
func TestStoreGetDoesNotReadKeyringWhenFallbackErrs(t *testing.T) {
	kr := newFakeKeyring()
	kr.items[Service+"|"+testScope+"/tok"] = `{"value":"maybe stale"}`
	fallback := newFakeStorage()
	broken := errors.New("database not open")
	fallback.getErr = broken
	s := NewStore(kr, testScope, fallback)

	err := s.Get(t.Context(), "tok", &doc{})

	assert.ErrorIs(t, err, broken)
}

func TestStoreGetKeyringErrorSendsWritesToFallback(t *testing.T) {
	kr := newFakeKeyring()
	kr.getErr = errors.New("user interaction is not allowed")
	fallback := newFakeStorage()
	s := NewStore(kr, testScope, fallback)

	err := s.Get(t.Context(), "tok", &doc{})
	require.Error(t, err)
	assert.NotErrorIs(t, err, storageapi.ErrNotFound)

	kr.getErr = nil
	require.NoError(t, s.Set(t.Context(), "tok", doc{Value: "secret"}))
	assert.Empty(t, kr.items)
	assert.Equal(t, "secret", get(t, s, "tok"))
}

func TestStoreDelete(t *testing.T) {
	tests := []struct {
		name     string
		keyring  string
		fallback string
	}{
		{name: "both copies", keyring: "a", fallback: "b"},
		{name: "keyring only", keyring: "a"},
		{name: "fallback only", fallback: "b"},
		{name: "neither"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kr := newFakeKeyring()
			if tt.keyring != "" {
				kr.items[Service+"|"+testScope+"/tok"] = `{"value":"` + tt.keyring + `"}`
			}
			fallback := newFakeStorage()
			if tt.fallback != "" {
				require.NoError(t, fallback.Set(t.Context(), "tok", doc{Value: tt.fallback}))
			}
			s := NewStore(kr, testScope, fallback)

			require.NoError(t, s.Delete(t.Context(), "tok"))

			assert.Empty(t, kr.items)
			assertMissing(t, fallback, "tok")
			assert.ErrorIs(t, s.Get(t.Context(), "tok", &doc{}), storageapi.ErrNotFound)
		})
	}
}

// A keyring copy that survived a sign-out must not sign the user back
// in.
func TestStoreDeleteKeyringFailureIsNotReadBack(t *testing.T) {
	kr := newFakeKeyring()
	fallback := newFakeStorage()
	s := NewStore(kr, testScope, fallback)
	require.NoError(t, s.Set(t.Context(), "tok", doc{Value: "secret"}))

	locked := errors.New("locked")
	kr.deleteErr = locked
	err := s.Delete(t.Context(), "tok")
	assert.ErrorIs(t, err, locked)

	assert.ErrorIs(t, s.Get(t.Context(), "tok", &doc{}), storageapi.ErrNotFound)
}

// A caller that gives up has not shown the keyring to be broken.
func TestStoreCancelledCallerDoesNotDisableKeyring(t *testing.T) {
	kr := newFakeKeyring()
	kr.setErr = context.Canceled
	s := NewStore(kr, testScope, newFakeStorage())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_ = s.Set(ctx, "tok", doc{Value: "first"})

	kr.setErr = nil
	require.NoError(t, s.Set(t.Context(), "tok", doc{Value: "second"}))

	assert.JSONEq(t, `{"value":"second"}`, kr.items[Service+"|"+testScope+"/tok"])
}

func TestStoreScopesDoNotShareItems(t *testing.T) {
	kr := newFakeKeyring()
	a := NewStore(kr, "/data/a", newFakeStorage())
	b := NewStore(kr, "/data/b", newFakeStorage())

	require.NoError(t, a.Set(t.Context(), "tok", doc{Value: "a"}))

	assert.ErrorIs(t, b.Get(t.Context(), "tok", &doc{}), storageapi.ErrNotFound)
	assert.Equal(t, "a", get(t, a, "tok"))
}

func get(t *testing.T, s *Store, id string) string {
	t.Helper()
	var got doc
	require.NoError(t, s.Get(t.Context(), id, &got))
	return got.Value
}

func assertMissing(t *testing.T, s Storage, id string) {
	t.Helper()
	assert.ErrorIs(t, s.Get(t.Context(), id, &doc{}), storageapi.ErrNotFound)
}

type fakeKeyring struct {
	mu        sync.Mutex
	items     map[string]string
	getErr    error
	setErr    error
	deleteErr error
}

func newFakeKeyring() *fakeKeyring {
	return &fakeKeyring{items: make(map[string]string)}
}

func (k *fakeKeyring) Get(_ context.Context, service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.getErr != nil {
		return "", k.getErr
	}
	v, ok := k.items[service+"|"+user]
	if !ok {
		return "", ErrNotFound
	}
	return v, nil
}

func (k *fakeKeyring) Set(_ context.Context, service, user, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.setErr != nil {
		return k.setErr
	}
	k.items[service+"|"+user] = secret
	return nil
}

func (k *fakeKeyring) Delete(_ context.Context, service, user string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.deleteErr != nil {
		return k.deleteErr
	}
	if _, ok := k.items[service+"|"+user]; !ok {
		return ErrNotFound
	}
	delete(k.items, service+"|"+user)
	return nil
}

// fakeStorage injects failures into an in-memory storageapi.Service.
type fakeStorage struct {
	storageapi.Service
	getErr    error
	deleteErr error
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{Service: storagestub.NewInMemoryService()}
}

func (s *fakeStorage) Get(ctx context.Context, ID string, doc any) error {
	if s.getErr != nil {
		return s.getErr
	}
	return s.Service.Get(ctx, ID, doc)
}

func (s *fakeStorage) Delete(ctx context.Context, ID string) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return s.Service.Delete(ctx, ID)
}
