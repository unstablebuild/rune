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

package extension

import (
	"context"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagerpc"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// probeService records whether it actually served a request and how
// many times it was closed, so a test can prove that a registration
// serves the borrowed instance instead of one it constructed itself.
type probeService struct {
	storageapi.Service
	gets   int
	closes int
}

func (s *probeService) Get(ctx context.Context, id string, doc any) error {
	s.gets++
	return s.Service.Get(ctx, id, doc)
}

func (s *probeService) Close() error {
	s.closes++
	return nil
}

// serveStorageResources registers svc on a fresh gRPC server, as one
// workspace extension runner does, and returns a client for it.
func serveStorageResources(
	t *testing.T, svc storageapi.Service,
) storageapi.Service {
	t.Helper()

	resources := StorageResources(svc)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	closer, err := resources[extensionapi.PermissionStorage].Register(srv, new(sync.Mutex))
	require.NoError(t, err)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(func() {
		assert.NoError(t, closer.Close())
		srv.Stop()
	})

	client, err := storagerpc.NewClient(lis.Addr(), docbson.Marshaler(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

type storedDoc struct {
	Value string
}

func TestStorageResourcesSharesOneServiceAcrossWorkspaces(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	shared := &probeService{Service: storagestub.NewInMemoryService()}
	require.NoError(t, shared.Service.Set(ctx, "doc", storedDoc{Value: "seeded"}))

	first := serveStorageResources(t, shared)
	second := serveStorageResources(t, shared)

	// A registration that builds its own service would miss the document
	// seeded directly into the shared instance.
	for name, client := range map[string]storageapi.Service{
		"first workspace": first, "second workspace": second,
	} {
		var got storedDoc
		require.NoError(t, client.Get(ctx, "doc", &got), name)
		assert.Equal(t, "seeded", got.Value, name)
	}
	assert.Equal(t, 2, shared.gets,
		"every workspace must serve the shared storage service instance")

	assert.Zero(t, shared.closes,
		"registrations borrow the shared service and must not close it")
}
