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

package idescavenger_test

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/retry"

	"unstable.build/rune/internal/ide/idescavenger"
)

func uri(t *testing.T, path string) workspaceapi.URI {
	t.Helper()
	u, err := workspaceapi.ParseURI("file://" + path)
	require.NoError(t, err)
	return u
}

type fixture struct {
	cleaner *idescavenger.Cleaner
	storage storageapi.Service
	missing map[string]struct{}
	open    []workspaceapi.URI
	openErr error
	cleaned []string
	hookErr error
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		storage: storagestub.NewInMemoryService(),
		missing: make(map[string]struct{}),
	}
	cleaner, err := idescavenger.New(idescavenger.Config{
		Storage: f.storage,
		OpenWorkspaces: func(context.Context) ([]workspaceapi.URI, error) {
			return f.open, f.openErr
		},
		Stat: func(name string) (fs.FileInfo, error) {
			if _, gone := f.missing[name]; gone {
				return nil, &fs.PathError{
					Op: "stat", Path: name, Err: fs.ErrNotExist,
				}
			}
			return nil, nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleaner.Close() })

	cleaner.AddWorkspaceHook(func(
		_ context.Context, cwd workspaceapi.URI,
	) error {
		if f.hookErr != nil {
			return f.hookErr
		}
		f.cleaned = append(f.cleaned, cwd.String())
		return nil
	})
	f.cleaner = cleaner
	return f
}

func TestRunOnce(t *testing.T) {
	ctx := context.Background()

	t.Run("cleans workspaces that no longer exist", func(t *testing.T) {
		f := newFixture(t)
		gone := uri(t, "/tmp/gone")
		alive := uri(t, "/tmp/alive")
		f.missing[gone.Path()] = struct{}{}
		require.NoError(t, f.cleaner.RegisterNewWorkspace(ctx, gone))
		require.NoError(t, f.cleaner.RegisterNewWorkspace(ctx, alive))

		require.NoError(t, f.cleaner.RunOnce(ctx))
		assert.Equal(t, []string{gone.String()}, f.cleaned)

		// the cleaned workspace is forgotten, so a second pass is a no-op
		f.cleaned = nil
		require.NoError(t, f.cleaner.RunOnce(ctx))
		assert.Empty(t, f.cleaned)
	})

	t.Run("keeps workspaces that are open", func(t *testing.T) {
		f := newFixture(t)
		gone := uri(t, "/tmp/gone")
		f.missing[gone.Path()] = struct{}{}
		f.open = []workspaceapi.URI{gone}
		require.NoError(t, f.cleaner.RegisterNewWorkspace(ctx, gone))

		require.NoError(t, f.cleaner.RunOnce(ctx))
		assert.Empty(t, f.cleaned)
	})

	t.Run("abandons the pass when open workspaces are unknown", func(t *testing.T) {
		f := newFixture(t)
		gone := uri(t, "/tmp/gone")
		f.missing[gone.Path()] = struct{}{}
		require.NoError(t, f.cleaner.RegisterNewWorkspace(ctx, gone))
		f.openErr = errors.New("event loop is gone")

		require.Error(t, f.cleaner.RunOnce(ctx))
		assert.Empty(t, f.cleaned)
	})

	t.Run("retries a workspace whose hook failed", func(t *testing.T) {
		f := newFixture(t)
		gone := uri(t, "/tmp/gone")
		f.missing[gone.Path()] = struct{}{}
		require.NoError(t, f.cleaner.RegisterNewWorkspace(ctx, gone))

		f.hookErr = errors.New("storage is busy")
		require.NoError(t, f.cleaner.RunOnce(ctx))
		assert.Empty(t, f.cleaned)

		f.hookErr = nil
		require.NoError(t, f.cleaner.RunOnce(ctx))
		assert.Equal(t, []string{gone.String()}, f.cleaned)
	})

	t.Run("ignores workspaces that are not local", func(t *testing.T) {
		f := newFixture(t)
		remote, err := workspaceapi.ParseURI("ssh://host/tmp/gone")
		require.NoError(t, err)
		f.missing["/tmp/gone"] = struct{}{}
		require.NoError(t, f.cleaner.RegisterNewWorkspace(ctx, remote))

		require.NoError(t, f.cleaner.RunOnce(ctx))
		assert.Empty(t, f.cleaned)
	})

	t.Run("leaves a workspace it cannot stat", func(t *testing.T) {
		f := newFixture(t)
		unreachable := uri(t, "/tmp/unreachable")
		require.NoError(t, f.cleaner.RegisterNewWorkspace(ctx, unreachable))

		require.NoError(t, f.cleaner.RunOnce(ctx))
		assert.Empty(t, f.cleaned)
	})
}

func TestRegisterNewWorkspaceIsIdempotent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	gone := uri(t, "/tmp/gone")
	f.missing[gone.Path()] = struct{}{}

	for range 3 {
		require.NoError(t, f.cleaner.RegisterNewWorkspace(ctx, gone))
	}

	require.NoError(t, f.cleaner.RunOnce(ctx))
	assert.Equal(t, []string{gone.String()}, f.cleaned)
}

// seedFixture drives the seed listing a Cleaner consults for the
// workspaces that predate it.
type seedFixture struct {
	storage storageapi.Service
	mu      sync.Mutex
	calls   int
	uris    []workspaceapi.URI
	errs    []error
	release chan struct{}
	cleaned []string
}

func newSeedFixture(uris ...workspaceapi.URI) *seedFixture {
	return &seedFixture{storage: storagestub.NewInMemoryService(), uris: uris}
}

func (f *seedFixture) seed(ctx context.Context) ([]workspaceapi.URI, error) {
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		return nil, err
	}
	return f.uris, nil
}

func (f *seedFixture) seedCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *seedFixture) cleanedURIs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cleaned...)
}

// newCleaner builds a Cleaner on f's storage, as a fresh launch on the
// same datadir would, with every workspace root reported missing.
func (f *seedFixture) newCleaner(t *testing.T) *idescavenger.Cleaner {
	t.Helper()
	cleaner, err := idescavenger.New(idescavenger.Config{
		Storage: f.storage,
		Seed:    f.seed,
		Stat: func(name string) (fs.FileInfo, error) {
			return nil, &fs.PathError{
				Op: "stat", Path: name, Err: fs.ErrNotExist,
			}
		},
		StartRetry: retry.SequentialStrategy(time.Millisecond),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleaner.Close() })
	cleaner.AddWorkspaceHook(func(
		_ context.Context, cwd workspaceapi.URI,
	) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.cleaned = append(f.cleaned, cwd.String())
		return nil
	})
	return cleaner
}

// The seed listing decodes every document in the dataset it recovers
// from, so it must run once per dataset, from the background pass, and
// never on the goroutine that starts the Cleaner.
func TestStartSeedsOncePerDataset(t *testing.T) {
	gone := uri(t, "/tmp/gone")
	f := newSeedFixture(gone)
	f.release = make(chan struct{})

	f.newCleaner(t).Start(context.Background())
	assert.Equal(t, 0, f.seedCalls(),
		"Start must return without waiting on the seed listing")
	close(f.release)
	require.Eventually(t, func() bool {
		return len(f.cleanedURIs()) == 1
	}, 5*time.Second, 5*time.Millisecond,
		"the first pass must reclaim the seeded workspace")
	assert.Equal(t, []string{gone.String()}, f.cleanedURIs())
	assert.Equal(t, 1, f.seedCalls())

	f.release = nil
	for range 2 {
		next := f.newCleaner(t)
		next.Start(context.Background())
		require.NoError(t, next.RunOnce(context.Background()))
	}
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 1, f.seedCalls(),
		"a later launch on the same dataset must not list again")
}

// A seed listing that fails, as it does while another process leads the
// storage, must be retried with the pass and must not be recorded as
// done until it succeeds.
func TestStartRetriesAFailedSeed(t *testing.T) {
	gone := uri(t, "/tmp/gone")
	f := newSeedFixture(gone)
	f.errs = []error{
		errors.New("resource exhausted"), errors.New("resource exhausted"),
	}

	f.newCleaner(t).Start(context.Background())
	require.Eventually(t, func() bool {
		return len(f.cleanedURIs()) == 1
	}, 5*time.Second, 5*time.Millisecond)
	assert.Equal(t, 3, f.seedCalls())

	f.newCleaner(t).Start(context.Background())
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 3, f.seedCalls(),
		"only a successful seed marks the dataset as seeded")
}

// Start runs its pass while the IDE is still starting up, before the
// event loop accepts work, so the open workspaces can be unknowable for
// the first attempts. The pass must be retried until they are known:
// giving up leaves the session with no reclamation at all.
func TestStartRetriesUntilOpenWorkspacesAreKnown(t *testing.T) {
	var mu sync.Mutex
	var calls int
	var cleaned []string
	cleaner, err := idescavenger.New(idescavenger.Config{
		Storage: storagestub.NewInMemoryService(),
		OpenWorkspaces: func(context.Context) ([]workspaceapi.URI, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls < 3 {
				return nil, errors.New("event loop is not accepting work")
			}
			return nil, nil
		},
		Stat: func(name string) (fs.FileInfo, error) {
			return nil, &fs.PathError{
				Op: "stat", Path: name, Err: fs.ErrNotExist,
			}
		},
		StartRetry: retry.SequentialStrategy(time.Millisecond),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleaner.Close() })
	cleaner.AddWorkspaceHook(func(
		_ context.Context, cwd workspaceapi.URI,
	) error {
		mu.Lock()
		defer mu.Unlock()
		cleaned = append(cleaned, cwd.String())
		return nil
	})
	gone := uri(t, "/tmp/gone")
	require.NoError(t, cleaner.RegisterNewWorkspace(context.Background(), gone))

	cleaner.Start(context.Background())

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(cleaned) == 1
	}, 5*time.Second, 5*time.Millisecond,
		"the pass must survive OpenWorkspaces failing while the IDE starts")
}

// Close must stop a Start whose pass keeps failing, so a shutdown does
// not leave a retry loop running against closed storage.
func TestCloseStopsStartRetries(t *testing.T) {
	var mu sync.Mutex
	var calls int
	cleaner, err := idescavenger.New(idescavenger.Config{
		Storage: storagestub.NewInMemoryService(),
		OpenWorkspaces: func(context.Context) ([]workspaceapi.URI, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			return nil, errors.New("event loop is not accepting work")
		},
		StartRetry: retry.SequentialStrategy(time.Millisecond),
	})
	require.NoError(t, err)
	gone := uri(t, "/tmp/gone")
	require.NoError(t, cleaner.RegisterNewWorkspace(context.Background(), gone))

	cleaner.Start(context.Background())
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return calls > 0
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, cleaner.Close())

	require.Eventually(t, func() bool {
		mu.Lock()
		before := calls
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		return calls == before
	}, 5*time.Second, time.Millisecond,
		"retries must stop once the Cleaner is closed")
}

// The default Stat must recognize a workspace that really is gone.
func TestRunOnceWithRealFilesystem(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	gone := uri(t, filepath.Join(dir, "gone"))
	alive := uri(t, filepath.Join(dir, "alive"))
	require.NoError(t, os.Mkdir(alive.Path(), 0o755))

	cleaner, err := idescavenger.New(idescavenger.Config{
		Storage: storagestub.NewInMemoryService(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cleaner.Close() })

	var cleaned []string
	cleaner.AddWorkspaceHook(func(
		_ context.Context, cwd workspaceapi.URI,
	) error {
		cleaned = append(cleaned, cwd.String())
		return nil
	})
	require.NoError(t, cleaner.RegisterNewWorkspace(ctx, gone))
	require.NoError(t, cleaner.RegisterNewWorkspace(ctx, alive))

	require.NoError(t, cleaner.RunOnce(ctx))
	assert.Equal(t, []string{gone.String()}, cleaned)
}
