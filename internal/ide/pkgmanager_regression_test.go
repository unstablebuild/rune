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

package ide

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
)

func TestPackageManagerLibDirMissingPackageReturnsStorageNotFound(t *testing.T) {
	t.Parallel()

	rm := idepkgtest.NewReleaseManager(
		idepkgtest.MakePackages(release.Package{Name: "six", Latest: "1"}),
		idepkgtest.MakeBundles([]release.Bundle{{Package: "six", Version: "1"}}),
	)
	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer func() {
		require.NoError(t, m.Close())
	}()

	it, err := m.pkgmanager.LibDir(context.Background(), "go")
	require.Nil(t, it)
	require.ErrorIs(t, err, storageapi.ErrNotFound)
}

// TestPackageManagerInstallPromptDoesNotBlockEventLoop is a regression
// test: selecting "Yes" on the auto-install prompt used to run the
// synchronous InstallPackageVersion download directly inside the prompt
// handler, which executes on the event loop and froze the UI until the
// package finished installing. The install must run off the event loop
// so dispatching the selection key returns promptly while the download
// is still in flight.
func TestPackageManagerInstallPromptDoesNotBlockEventLoop(t *testing.T) {
	t.Parallel()

	rm := idepkgtest.NewReleaseManager(
		idepkgtest.MakePackages(release.Package{Name: "go", Latest: "1"}),
		idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}}),
	)
	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)

	it, err := m.pkgmanager.LibDir(context.Background(), "go")
	require.NoError(t, err)

	m.Resize(40, 15)

	// Block the download so it cannot complete while the prompt
	// selection is dispatched. The hook is installed only after
	// LibDir's latest-version lookup, so it gates the install download
	// rather than the version lookup.
	releaseDownload := make(chan struct{})
	rm.SetHook(func() { <-releaseDownload })

	// Dispatching the "Yes" selection must return even though the
	// download is blocked; otherwise the install ran on the event loop.
	dispatched := make(chan struct{})
	go func() {
		m.Handle(term.Event{Ch: 'Y', Type: term.EventKey})
		close(dispatched)
	}()

	select {
	case <-dispatched:
	case <-time.After(5 * time.Second):
		close(releaseDownload)
		t.Fatal("dispatching the install prompt selection blocked on the install download")
	}

	close(releaseDownload)
	slice, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.NotEmpty(t, slice)

	require.NoError(t, m.Close())
}

// TestPackageManagerPendingInstallHonorsContextCancellation is a
// regression test: an iterator handed out while the install prompt was
// unanswered blocked Next and Err on the install decision regardless of
// the caller's context. The symbol indexer's scan workers parked there,
// and Parser.Close, which cancels them and waits, hung IDE shutdown.
func TestPackageManagerPendingInstallHonorsContextCancellation(t *testing.T) {
	t.Parallel()

	rm := idepkgtest.NewReleaseManager(
		idepkgtest.MakePackages(release.Package{Name: "go", Latest: "1"}),
		idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}}),
	)
	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer func() {
		require.NoError(t, m.Close())
	}()

	it, err := m.pkgmanager.LibDir(context.Background(), "go")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		_, err := iterator.ToSlice(ctx, it)
		done <- err
	})
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("pending install iterator ignored context cancellation")
	}
}

// TestPackageManagerPendingInstallErrDoesNotBlock is a regression test:
// Err on an iterator handed out while the install prompt was unanswered
// waited for the install decision with no way to cancel, though no
// error had been encountered yet.
func TestPackageManagerPendingInstallErrDoesNotBlock(t *testing.T) {
	t.Parallel()

	rm := idepkgtest.NewReleaseManager(
		idepkgtest.MakePackages(release.Package{Name: "go", Latest: "1"}),
		idepkgtest.MakeBundles([]release.Bundle{{Package: "go", Version: "1"}}),
	)
	m := newTestWorkspaceManagerHandlerForPkgManager(t, rm, false, 0)
	defer func() {
		require.NoError(t, m.Close())
	}()

	it, err := m.pkgmanager.LibDir(context.Background(), "go")
	require.NoError(t, err)

	done := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		done <- it.Err()
	})

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Err blocked on the pending install decision")
	}
}
