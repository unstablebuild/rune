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

package mcp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/internal/keychain"
)

// fakeKeyring is an in-memory keychain.Keyring that fails every call
// while err is set.
type fakeKeyring struct {
	mu    sync.Mutex
	items map[string]string
	err   error
}

func newFakeKeyring() *fakeKeyring { return &fakeKeyring{items: make(map[string]string)} }

func (k *fakeKeyring) Get(_ context.Context, service, user string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return "", k.err
	}
	v, ok := k.items[service+"|"+user]
	if !ok {
		return "", keychain.ErrNotFound
	}
	return v, nil
}

func (k *fakeKeyring) Set(_ context.Context, service, user, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return k.err
	}
	k.items[service+"|"+user] = secret
	return nil
}

func (k *fakeKeyring) Delete(_ context.Context, service, user string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.err != nil {
		return k.err
	}
	delete(k.items, service+"|"+user)
	return nil
}

func (k *fakeKeyring) len() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.items)
}

func TestCredentialStoreTokens(t *testing.T) {
	ctx := t.Context()
	kr := newFakeKeyring()
	s := newCredentialStore(kr, "/data", storagestub.NewInMemoryService())

	got, err := s.LoadToken(ctx, "https://mcp.example.com/mcp")
	require.NoError(t, err)
	assert.Nil(t, got)

	doc := &tokenDoc{
		Issuer:       "https://auth.example.com",
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
		Scopes:       []string{"read"},
		Expiry:       time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	require.NoError(t, s.SaveToken(ctx, "https://mcp.example.com/mcp", doc))
	assert.Equal(t, 1, kr.len(), "tokens belong in the keychain")

	for _, spelling := range []string{
		"https://mcp.example.com/mcp",
		"https://MCP.example.com:443/mcp/",
		"https://mcp.example.com/mcp#frag",
	} {
		got, err = s.LoadToken(ctx, spelling)
		require.NoError(t, err)
		require.NotNil(t, got, spelling)
		assert.Equal(t, doc.AccessToken, got.AccessToken)
		assert.True(t, doc.Expiry.Equal(got.Expiry))
	}
	got, err = s.LoadToken(ctx, "https://mcp.example.com/other")
	require.NoError(t, err)
	assert.Nil(t, got)

	require.NoError(t, s.DeleteToken(ctx, "https://mcp.example.com/mcp"))
	got, err = s.LoadToken(ctx, "https://mcp.example.com/mcp")
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestCredentialStoreClientsAreBoundToIssuer(t *testing.T) {
	ctx := t.Context()
	s := newCredentialStore(newFakeKeyring(), "/data", storagestub.NewInMemoryService())
	const redirect = "http://localhost:1234/callback"

	require.NoError(t, s.SaveClient(ctx, "https://a.example.com", redirect, &clientDoc{ClientID: "a-client"}))

	got, err := s.LoadClient(ctx, "https://a.example.com", redirect)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "a-client", got.ClientID)

	for _, tt := range []struct{ issuer, redirect string }{
		{"https://b.example.com", redirect},
		{"https://a.example.com", "http://localhost:9999/callback"},
	} {
		got, err := s.LoadClient(ctx, tt.issuer, tt.redirect)
		require.NoError(t, err)
		assert.Nil(t, got, "%s %s", tt.issuer, tt.redirect)
	}

	require.NoError(t, s.DeleteClient(ctx, "https://a.example.com", redirect))
	got, err = s.LoadClient(ctx, "https://a.example.com", redirect)
	require.NoError(t, err)
	assert.Nil(t, got)
}

func TestCredentialStoreFallsBackWhenKeyringFails(t *testing.T) {
	ctx := t.Context()
	kr := newFakeKeyring()
	kr.err = errors.New("no secret service")
	storage := storagestub.NewInMemoryService()
	s := newCredentialStore(kr, "/data", storage)

	doc := &tokenDoc{AccessToken: "access"}
	require.NoError(t, s.SaveToken(ctx, "https://mcp.example.com", doc))

	got, err := s.LoadToken(ctx, "https://mcp.example.com")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "access", got.AccessToken)

	var stored tokenDoc
	err = storageapi.WithPartition(storage, credentialsPartition).
		Get(ctx, tokenKey("https://mcp.example.com"), &stored)
	require.NoError(t, err)
	assert.Equal(t, "access", stored.AccessToken)
}
