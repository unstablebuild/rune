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

package idepkg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/blue/release"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/auth"
	"unstable.build/rune/internal/ide/idepkg/idepkgtest"
)

func TestCheckForUpdates(t *testing.T) {
	t.Parallel()

	t.Run("returns updates when newer version available", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		updates, err := CheckForUpdates(context.Background(), m)
		require.NoError(t, err)
		require.Len(t, updates, 1)
		assert.Equal(t, "go", updates[0].Package)
		assert.Equal(t, release.Version("1"), updates[0].Current)
		assert.Equal(t, release.Version("2"), updates[0].Latest)
	})

	t.Run("returns empty when up to date", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "1"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		updates, err := CheckForUpdates(context.Background(), m)
		require.NoError(t, err)
		assert.Empty(t, updates)
	})

	t.Run("skips erroring packages", func(t *testing.T) {
		t.Parallel()
		// "go" package exists in registry but "testpkg" does not have a Package entry
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles(
			[]release.Bundle{{Package: "go", Version: "1"}},
			[]release.Bundle{{Package: "testpkg", Version: "1"}},
		)
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)
		err = m.InstallPackageVersion(context.Background(), "testpkg", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		updates, err := CheckForUpdates(context.Background(), m)
		require.NoError(t, err)
		require.Len(t, updates, 1)
		assert.Equal(t, "go", updates[0].Package)
	})

	t.Run("deduplicates packages", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "3"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, _, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		err = m.InstallPackageVersion(context.Background(), "go", "2", repl.NopProgressWriter())
		require.NoError(t, err)

		updates, err := CheckForUpdates(context.Background(), m)
		require.NoError(t, err)
		// should only return one update for "go", not two
		require.Len(t, updates, 1)
		assert.Equal(t, "go", updates[0].Package)
	})

	t.Run("returns ErrNotAuthenticated when release manager is unauthenticated", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, _, rm, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		rm.ExpectReturnErr(auth.ErrNotAuthenticated)

		_, err = CheckForUpdates(context.Background(), m)
		require.Error(t, err)
		assert.True(t, errors.Is(err, auth.ErrNotAuthenticated))
	})

	t.Run("checks any PackageManager", func(t *testing.T) {
		t.Parallel()
		pm := &inUsePackageManager{
			installed: []string{"go", "six", "rust"},
			inUse:     map[string]release.Version{"go": "1", "six": "2"},
			latest:    map[string]release.Version{"go": "2", "six": "2", "rust": "9"},
		}
		updates, err := CheckForUpdates(context.Background(), pm)
		require.NoError(t, err)
		assert.Equal(t, []Update{{Package: "go", Current: "1", Latest: "2"}}, updates)
	})
}

// inUsePackageManager implements only what CheckForUpdates may call; any
// other method panics on the nil embedded interface.
type inUsePackageManager struct {
	PackageManager
	installed []string
	inUse     map[string]release.Version
	latest    map[string]release.Version
}

func (f *inUsePackageManager) ListInstalledPackages(
	context.Context,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice(f.installed), nil
}

func (f *inUsePackageManager) PackageVersionInUse(
	_ context.Context, pkgID string,
) (release.Version, bool, error) {
	v, ok := f.inUse[pkgID]
	return v, ok, nil
}

func (f *inUsePackageManager) DescribePackage(
	_ context.Context, pkgID string,
) (release.Package, error) {
	return release.Package{Name: pkgID, Latest: f.latest[pkgID]}, nil
}

func TestStart(t *testing.T) {
	t.Parallel()

	t.Run("respects 24h throttle", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, n, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		now := time.Now()
		uc := NewUpdateChecker(m)
		uc.now = func() time.Time { return now }

		// First run should proceed and notify
		n.Reset()
		uc.run(context.Background())

		active := n.Active()
		assert.NotEmpty(t, active, "expected notification after first run")

		// Second run with same time should be throttled — no new notifications
		n.Reset()
		uc.run(context.Background())

		active = n.Active()
		assert.Empty(t, active, "expected no notification due to throttle")
	})

	t.Run("silently skips when not authenticated", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, n, rm, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		rm.ExpectReturnErr(auth.ErrNotAuthenticated)

		uc := NewUpdateChecker(m)
		n.Reset()
		uc.run(context.Background())

		assert.Empty(t, n.Active(),
			"update checker must not surface notifications when unauthenticated")
	})

	t.Run("sends notification when updates available", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, n, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		uc := NewUpdateChecker(m)

		n.Reset()
		uc.run(context.Background())

		active := n.Active()
		require.NotEmpty(t, active)
		var found bool
		for _, noti := range active {
			if noti.Level == browserapi.LevelInfo &&
				assert.ObjectsAreEqual(formatUpdateNotification(
					[]Update{{Package: "go", Current: "1", Latest: "2"}}), noti.Msg) {
				found = true
			}
		}
		assert.True(t, found, "expected update notification, got: %+v", active)
	})

	t.Run("shows prompt after 7 days", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, n, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		now := time.Now()
		uc := NewUpdateChecker(m)
		uc.now = func() time.Time { return now }

		// First run — sets FirstSeen
		n.Reset()
		uc.run(context.Background())

		// Advance past 7 days and un-throttle
		now = now.Add(8 * 24 * time.Hour)

		// The prompt will auto-select "Update All" (index 0) via mockWindowManager
		// which sends Enter on the first option.
		n.Reset()
		uc.run(context.Background())
	})

	t.Run("respects skipped versions", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, n, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		now := time.Now()
		uc := NewUpdateChecker(m)
		uc.now = func() time.Time { return now }

		// First run to create records
		n.Reset()
		uc.run(context.Background())

		// Skip version 2
		err = uc.SkipVersion(context.Background(), "go", "2")
		require.NoError(t, err)

		// Advance past 7 days
		now = now.Add(8 * 24 * time.Hour)

		// Run again — notification should still appear but no prompt
		// (prompt filters out skipped). Since prompt won't trigger install,
		// we shouldn't get an install notification.
		n.Reset()
		uc.run(context.Background())

		active := n.Active()
		// Should have the info notification but no install notification
		for _, noti := range active {
			assert.NotContains(t, noti.Msg, "install go@")
		}
	})

	t.Run("cleans up stale records", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, n, _, _ := newTestManager(t, pkgs, versions)
		storage := m.storage

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		uc := NewUpdateChecker(m)

		// First run creates records for "go"
		n.Reset()
		uc.run(context.Background())

		// Verify record exists
		var val updateAvailableValue
		err = storage.Get(context.Background(), "update-available:go", &val)
		require.NoError(t, err)
		assert.Equal(t, "go", val.Package)

		// Now simulate "go" becoming up-to-date (Latest == installed)
		pkgs["go"] = release.Package{Name: "go", Latest: "1"}

		// Advance past throttle
		now := time.Now().Add(25 * time.Hour)
		uc.now = func() time.Time { return now }

		n.Reset()
		uc.run(context.Background())

		// Record should be cleaned up
		err = storage.Get(context.Background(), "update-available:go", &val)
		assert.ErrorIs(t, err, storageapi.ErrNotFound, "expected stale record to be cleaned up")
	})
}

func TestSkipVersion(t *testing.T) {
	t.Parallel()

	t.Run("marks version as skipped", func(t *testing.T) {
		t.Parallel()
		m, _, _, _ := newTestManager(t,
			idepkgtest.MakePackages(), idepkgtest.MakeBundles())
		storage := m.storage

		uc := NewUpdateChecker(m)

		// Pre-create a record
		ctx := context.Background()
		key := "update-available:go"
		val := updateAvailableValue{
			Package: "go", Latest: "2", FirstSeen: time.Now(),
		}
		require.NoError(t, storage.Set(ctx, key, val))

		err := uc.SkipVersion(ctx, "go", "2")
		require.NoError(t, err)

		var got updateAvailableValue
		require.NoError(t, storage.Get(ctx, key, &got))
		assert.True(t, got.Skipped)
	})

	t.Run("no-op for different version", func(t *testing.T) {
		t.Parallel()
		m, _, _, _ := newTestManager(t,
			idepkgtest.MakePackages(), idepkgtest.MakeBundles())
		storage := m.storage

		uc := NewUpdateChecker(m)

		ctx := context.Background()
		key := "update-available:go"
		val := updateAvailableValue{
			Package: "go", Latest: "3", FirstSeen: time.Now(),
		}
		require.NoError(t, storage.Set(ctx, key, val))

		err := uc.SkipVersion(ctx, "go", "2")
		require.NoError(t, err)

		var got updateAvailableValue
		require.NoError(t, storage.Get(ctx, key, &got))
		assert.False(t, got.Skipped, "should not skip when version doesn't match")
	})

	t.Run("creates record if absent", func(t *testing.T) {
		t.Parallel()
		m, _, _, _ := newTestManager(t,
			idepkgtest.MakePackages(), idepkgtest.MakeBundles())
		storage := m.storage

		uc := NewUpdateChecker(m)

		ctx := context.Background()
		err := uc.SkipVersion(ctx, "go", "2")
		require.NoError(t, err)

		var got updateAvailableValue
		require.NoError(t, storage.Get(ctx, "update-available:go", &got))
		assert.True(t, got.Skipped)
		assert.Equal(t, release.Version("2"), got.Latest)
	})
}

func TestUpdatePromptActions(t *testing.T) {
	t.Parallel()

	t.Run("Update All triggers installs", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
			{Package: "go", Version: "2"},
		})
		m, n, _, _ := newTestManager(t, pkgs, versions)

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		uc := NewUpdateChecker(m)

		updates := []Update{{Package: "go", Current: "1", Latest: "2"}}

		// Mock will auto-select first option (Update All) via Enter key
		n.Reset()
		uc.showUpdatePrompt(context.Background(), updates)

		// The install runs off the event loop, so wait for it to land.
		require.Eventually(t, func() bool {
			inUse, ok := versionInUse(t, m, "go")
			return ok && inUse == release.Version("2")
		}, 10*time.Second, 10*time.Millisecond,
			"Upgrade All should install and switch to the latest version")
	})

	t.Run("Skip persists skip", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, n, _, _ := newTestManager(t, pkgs, versions)

		// Override floating to select "Skip These Versions" (index 2)
		m.wm = &mockWindowManager{
			floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
				h.Resize(70, 20)
				// Move down twice to reach "Skip These Versions"
				h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
				h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
				h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
				return &mockWindow{}, nil
			},
		}

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		uc := NewUpdateChecker(m)
		ctx := context.Background()

		// Pre-create update record
		key := "update-available:go"
		require.NoError(t, m.storage.Set(ctx, key, updateAvailableValue{
			Package: "go", Latest: "2", FirstSeen: time.Now(),
		}))

		updates := []Update{{Package: "go", Current: "1", Latest: "2"}}

		n.Reset()
		uc.showUpdatePrompt(ctx, updates)

		var got updateAvailableValue
		require.NoError(t, m.storage.Get(ctx, key, &got))
		assert.True(t, got.Skipped, "expected version to be skipped")
	})

	t.Run("Remind Later is no-op", func(t *testing.T) {
		t.Parallel()
		pkgs := idepkgtest.MakePackages(release.Package{Name: "go", Latest: "2"})
		versions := idepkgtest.MakeBundles([]release.Bundle{
			{Package: "go", Version: "1"},
		})
		m, n, _, _ := newTestManager(t, pkgs, versions)

		// Override floating to select "Remind Later" (index 1)
		m.wm = &mockWindowManager{
			floatingFn: func(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
				h.Resize(70, 20)
				h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowRight})
				h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
				return &mockWindow{}, nil
			},
		}

		err := m.InstallPackageVersion(context.Background(), "go", "1", repl.NopProgressWriter())
		require.NoError(t, err)

		uc := NewUpdateChecker(m)
		ctx := context.Background()

		// Pre-create update record (not skipped)
		key := "update-available:go"
		require.NoError(t, m.storage.Set(ctx, key, updateAvailableValue{
			Package: "go", Latest: "2", FirstSeen: time.Now(),
		}))

		updates := []Update{{Package: "go", Current: "1", Latest: "2"}}

		n.Reset()
		uc.showUpdatePrompt(ctx, updates)

		// Verify version is NOT skipped — still should prompt next time
		var got updateAvailableValue
		require.NoError(t, m.storage.Get(ctx, key, &got))
		assert.False(t, got.Skipped, "Remind Later should not skip")
	})
}

func TestFormatUpdateSummary(t *testing.T) {
	t.Parallel()

	updates := []Update{
		{Package: "go", Current: "1.21.0", Latest: "1.22.0"},
		{Package: "rust", Current: "1.70.0", Latest: "1.72.0"},
	}
	expected := "## Updates available:\n- go 1.21.0 → 1.22.0\n- rust 1.70.0 → 1.72.0"
	assert.Equal(t, expected, formatUpdateSummary(updates))
}

func TestFormatUpdateNotification(t *testing.T) {
	t.Parallel()

	updates := []Update{
		{Package: "go", Current: "1.21.0", Latest: "1.22.0"},
		{Package: "rust", Current: "1.70.0", Latest: "1.72.0"},
	}
	expected := "Updates available:\n" +
		"- go 1.21.0 → 1.22.0\n" +
		"- rust 1.70.0 → 1.72.0\n" +
		"\n" +
		"Run 'pkg update-all' in the console to update."
	assert.Equal(t, expected, formatUpdateNotification(updates))
}
