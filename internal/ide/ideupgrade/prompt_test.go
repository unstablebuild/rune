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

package ideupgrade

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
)

// mockWindowManager is a minimal browserapi.WindowManager that drives
// the floating prompt with a configurable input sequence and records
// the selected option index.
type mockWindowManager struct {
	feed func(browserapi.Floating)
}

func (m *mockWindowManager) Focus() (browserapi.Window, error) { return nil, nil }
func (m *mockWindowManager) Split(_ browserapi.Orientation, _ browserapi.Window, _ browserapi.Handler) (browserapi.Window, error) {
	return nil, nil
}
func (m *mockWindowManager) Floating(h browserapi.Floating, _ browserapi.FloatingConfig) (browserapi.Window, error) {
	if m.feed != nil {
		m.feed(h)
	}
	return nil, nil
}
func (m *mockWindowManager) Bar(_ browserapi.BarConfig, _ tui.Handler) error { return nil }
func (m *mockWindowManager) Tab(_ workspaceapi.URI, _ rune, _ string, _ browserapi.Handler) (browserapi.Handler, error) {
	return nil, nil
}
func (m *mockWindowManager) SetWindowContent(_ browserapi.Window, _ browserapi.Handler) error {
	return nil
}
func (m *mockWindowManager) CloseWindow(_ browserapi.Window) error { return nil }

func (m *mockWindowManager) SetTabActivity(workspaceapi.URI, bool) error { return nil }

func newPromptTestManager(t *testing.T, feed func(browserapi.Floating)) *Manager {
	t.Helper()
	storage := storagestub.NewInMemoryService()
	mgr, err := newWithPlatformOps(Config{
		CurrentVersion: "v1",
		Arch:           "darwin-arm64",
		ManifestURL:    "https://example.invalid",
		Storage:        storage,
		WindowManager:  &mockWindowManager{feed: feed},
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}, &fakePlatformOps{})
	require.NoError(t, err)
	return mgr
}

// resizeAndType drives a floating prompt by resizing it then sending
// a sequence of key events.
func resizeAndType(h browserapi.Floating, events ...term.Event) {
	h.Resize(80, 20)
	for _, ev := range events {
		h.Handle(ev)
	}
}

func TestPromptSkipPersistsSkippedVersion(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	mgr := newPromptTestManager(t, func(h browserapi.Floating) {
		defer wg.Done()
		// 's' is bound to "Skip This Version".
		resizeAndType(h, term.Event{Type: term.EventKey, Ch: 's'})
	})

	mgr.showPrompt(context.Background(), Manifest{
		Version: "v9.9.9", URL: "u", SHA256: "s",
	})
	wg.Wait()

	st := mgr.loadState(context.Background())
	require.Contains(t, st.SkippedVersions, "v9.9.9")
}

func TestPromptRemindLaterSetsRemindAfter(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	mgr := newPromptTestManager(t, func(h browserapi.Floating) {
		defer wg.Done()
		// 'l' is bound to "Remind Me Later".
		resizeAndType(h, term.Event{Type: term.EventKey, Ch: 'l'})
	})

	mgr.showPrompt(context.Background(), Manifest{
		Version: "v9.9.9", URL: "u", SHA256: "s",
	})
	wg.Wait()

	st := mgr.loadState(context.Background())
	require.False(t, st.RemindAfter.IsZero(), "remind_after should be set")
	require.True(t, st.RemindAfter.After(mgr.cfg.Now()), "remind_after should be in the future")
	require.NotContains(t, st.SkippedVersions, "v9.9.9")
}

func TestPromptShowFallsBackToNotificationWithoutWM(t *testing.T) {
	storage := storagestub.NewInMemoryService()
	notifs := &recordingNotifications{}
	mgr, err := newWithPlatformOps(Config{
		CurrentVersion: "v1",
		ManifestURL:    "https://example.invalid",
		Storage:        storage,
		Notifications:  notifs,
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
	}, &fakePlatformOps{})
	require.NoError(t, err)

	mgr.showPrompt(context.Background(), Manifest{Version: "v9", URL: "u", SHA256: "s"})
	notifyN, _ := notifs.snapshot()
	require.Equal(t, 1, notifyN, "fallback should post one notification")
	st := mgr.loadState(context.Background())
	require.Empty(t, st.SkippedVersions)
	require.True(t, st.RemindAfter.IsZero())
}

// TestPromptChoiceBlocksUntilAnswered covers the console `upgrade`
// path: the caller runs off the event loop and must block on the
// user's answer, which is then persisted before it returns.
func TestPromptChoiceBlocksUntilAnswered(t *testing.T) {
	manifest := Manifest{Version: "v9.9.9", URL: "u", SHA256: "s"}
	for _, tc := range []struct {
		name   string
		key    rune
		want   Choice
		assert func(t *testing.T, st state)
	}{
		{
			name: "upgrade now",
			key:  'y',
			want: ChoiceUpgradeNow,
			assert: func(t *testing.T, st state) {
				require.Empty(t, st.SkippedVersions)
				require.True(t, st.RemindAfter.IsZero())
			},
		},
		{
			name: "remind later",
			key:  'l',
			want: ChoiceRemindLater,
			assert: func(t *testing.T, st state) {
				require.False(t, st.RemindAfter.IsZero())
			},
		},
		{
			name: "skip version",
			key:  's',
			want: ChoiceSkipVersion,
			assert: func(t *testing.T, st state) {
				require.Contains(t, st.SkippedVersions, manifest.Version)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newPromptTestManager(t, func(h browserapi.Floating) {
				resizeAndType(h, term.Event{Type: term.EventKey, Ch: tc.key})
			})
			got, err := mgr.PromptChoice(context.Background(), manifest)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			tc.assert(t, mgr.loadState(context.Background()))
		})
	}
}

// TestPromptChoiceWithoutWindowManager covers the headless case: with
// nowhere to render the prompt there is no answer to wait for, so the
// caller must not block.
func TestPromptChoiceWithoutWindowManager(t *testing.T) {
	mgr, err := newWithPlatformOps(Config{
		CurrentVersion:   "v1",
		ManifestURL:      "https://example.invalid",
		Storage:          storagestub.NewInMemoryService(),
		ScheduleNextTick: func(fn func()) bool { fn(); return true },
	}, &fakePlatformOps{})
	require.NoError(t, err)

	got, err := mgr.PromptChoice(context.Background(),
		Manifest{Version: "v9", URL: "u", SHA256: "s"})
	require.NoError(t, err)
	require.Equal(t, ChoiceDismissed, got)
}
