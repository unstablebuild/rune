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

package workspacetest

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace"
)

func TestMemoryScheme(t *testing.T) {
	ctx := context.Background()

	t.Run("at root path", func(t *testing.T) {
		TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			uri, err := workspaceapi.ParseURI("memory:///")
			require.NoError(t, err)
			mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
			require.NoError(t, err)
			return mem
		})
	})
	t.Run("at nested path", func(t *testing.T) {
		TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			uri, err := workspaceapi.ParseURI("memory:///var/log")
			require.NoError(t, err)
			mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
			require.NoError(t, err)
			return mem
		})
	})
	t.Run("at nested path with end-slash", func(t *testing.T) {
		TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
			uri, err := workspaceapi.ParseURI("memory:///var/log/")
			require.NoError(t, err)
			mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
			require.NoError(t, err)
			return mem
		})
	})
}

func TestMemorySchemeCallsAfterClose(t *testing.T) {
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI("memory:///")
	require.NoError(t, err)
	mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	closer, ok := mem.(io.Closer)
	require.True(t, ok)
	require.NoError(t, closer.Close())

	_, err = mem.Create("post-close.txt")
	require.Error(t, err,
		"Create after Close must error instead of panicking on the nil files map")

	ch := make(chan schemeapi.EventInfo, 1)
	_, err = mem.Watch("/", ch, schemeapi.Create)
	require.Error(t, err,
		"Watch after Close must error instead of panicking on the nil watchpoint maps")
}

func TestMemoryFile(t *testing.T) {
	t.Run("Write overwrites data", func(t *testing.T) {
		f := workspace.NewMemoryFile("bla", 1, 0, []byte("12345"), new(sync.Mutex))
		n, err := f.Write([]byte("ZZ"))
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		nn, err := f.Seek(0, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(0), nn)

		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "ZZ345", string(data))
	})

	t.Run("Read uses write offset", func(t *testing.T) {
		f := workspace.NewMemoryFile("bla", 2, 0, []byte("12345"), new(sync.Mutex))
		n, err := f.Write([]byte("ZZ"))
		require.NoError(t, err)
		assert.Equal(t, 2, n)

		data, err := io.ReadAll(f)
		require.NoError(t, err)
		assert.Equal(t, "345", string(data))
	})
}

func TestMemoryScheme_CloseDoesNotDoubleCloseMultiEventWatchers(t *testing.T) {
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)

	mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	ch := make(chan schemeapi.EventInfo, 1)
	_, err = mem.Watch("/workspace/...", ch,
		schemeapi.Create, schemeapi.Write, schemeapi.Remove, schemeapi.Rename)
	require.NoError(t, err)

	// Must not panic with "close of closed channel".
	require.NotPanics(t, func() {
		require.NoError(t, mem.Close())
	})
}

func TestMemorySchemeCloseWithBlockedWatchSend(t *testing.T) {
	ctx := context.Background()
	uri, err := workspaceapi.ParseURI("memory:///workspace")
	require.NoError(t, err)
	mem, err := workspace.NewMemoryScheme(ctx, config.NopConfig(), uri)
	require.NoError(t, err)

	// Unbuffered and never consumed: the Create event sender parks.
	ch := make(chan schemeapi.EventInfo)
	_, err = mem.Watch("/workspace/...", ch, schemeapi.Create)
	require.NoError(t, err)

	createDone := make(chan struct{})
	go func() {
		defer close(createDone)
		_, _ = mem.Create("blocked.txt")
	}()

	// Let the creator park on the watch channel send.
	time.Sleep(50 * time.Millisecond)

	closer, ok := mem.(io.Closer)
	require.True(t, ok)

	closeDone := make(chan error, 1)
	go func() { closeDone <- closer.Close() }()

	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close never returned with a blocked watch send in flight")
	}
	select {
	case <-createDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Create never returned after Close")
	}
}
