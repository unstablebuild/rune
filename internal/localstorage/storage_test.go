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

package localstorage

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/document"
	"github.com/unstablebuild/blue/document/docmarshal/docbson"
	"github.com/unstablebuild/blue/document/doctest"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"unstable.build/rune/internal/localstorage/bluestore"
)

type miniDoc struct {
	V string
}

func TestNewMultiProcessSafeOnBolt(t *testing.T) {
	dir := t.TempDir()
	m := docbson.Marshaler()

	a := New(context.Background(), dir, m)
	defer func() { _ = a.Close() }()
	b := New(context.Background(), dir, m)
	defer func() { _ = b.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, a.Set(ctx, "k", &miniDoc{V: "from-a"}))
	var got miniDoc
	require.NoError(t, b.Get(ctx, "k", &got))
	require.Equal(t, "from-a", got.V)
}

func TestStorageConcurrentInstances(t *testing.T) {
	doctest.TestDocumentService(t, func(t *testing.T) document.Service {
		name, err := os.MkdirTemp("", "workspace_document_service_test")
		t.Cleanup(func() {
			_ = os.RemoveAll(name)
		})
		require.NoError(t, err)
		instance := New(context.Background(), name, docbson.Marshaler())
		return bluestore.AdaptFrom(instance)
	})
}

func TestDelayedLoadingServiceGetHonorsContextWhileInitializing(t *testing.T) {
	svc := &delayedLoadingService{ready: make(chan struct{})}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		var doc any
		errCh <- svc.Get(ctx, "blocked", &doc)
	}()

	select {
	case err := <-errCh:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("Get blocked waiting for delayed storage initialization")
	}

	svc.service = storagestub.NewInMemoryService()
	close(svc.ready)
}
