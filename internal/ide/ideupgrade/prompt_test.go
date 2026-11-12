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
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// keyPrompter answers each prompt with the option bound to key, as a
// user pressing it would.
type keyPrompter struct {
	t   *testing.T
	key rune
}

func (p *keyPrompter) Prompt(
	cfg browserapi.PromptConfig, h handler.PromptHandler,
) (browserapi.Window, error) {
	require.NoError(p.t, cfg.Validate())
	i := slices.Index(cfg.Bindings, term.KeyComb{Ch: p.key})
	require.GreaterOrEqual(p.t, i, 0, "no option is bound to %q", p.key)
	h.OnSelect(i, cfg.Options[i])
	return nil, h.OnClose()
}

// countingStarter counts the upgrades started and fails them with err.
type countingStarter struct {
	n   int
	err error
}

func (s *countingStarter) StartUpgrade() error {
	s.n++
	return s.err
}

func newPromptTestManager(t *testing.T, key rune) *Manager {
	t.Helper()
	storage := storagestub.NewInMemoryService()
	mgr, err := newWithPlatformOps(Config{
		CurrentVersion: "v1",
		Arch:           "darwin-arm64",
		ManifestURL:    "https://example.invalid",
		Storage:        storage,
		Prompter:       &keyPrompter{t: t, key: key},
		Starter:        &countingStarter{},
		ScheduleNextTick: func(fn func()) bool {
			fn()
			return true
		},
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}, &fakePlatformOps{})
	require.NoError(t, err)
	return mgr
}

func TestPromptSkipPersistsSkippedVersion(t *testing.T) {
	mgr := newPromptTestManager(t, 's')

	mgr.showPrompt(context.Background(), Manifest{
		Version: "v9.9.9", URL: "u", SHA256: "s",
	})

	st := mgr.loadState(context.Background())
	require.Contains(t, st.SkippedVersions, "v9.9.9")
}

func TestPromptRemindLaterSetsRemindAfter(t *testing.T) {
	mgr := newPromptTestManager(t, 'l')

	mgr.showPrompt(context.Background(), Manifest{
		Version: "v9.9.9", URL: "u", SHA256: "s",
	})

	st := mgr.loadState(context.Background())
	require.False(t, st.RemindAfter.IsZero(), "remind_after should be set")
	require.True(t, st.RemindAfter.After(mgr.cfg.Now()), "remind_after should be in the future")
	require.NotContains(t, st.SkippedVersions, "v9.9.9")
}

func TestPromptShowFallsBackToNotificationWithoutPrompter(t *testing.T) {
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
	require.Equal(t, 1, notifs.count(), "fallback should post one notification")
	st := mgr.loadState(context.Background())
	require.Empty(t, st.SkippedVersions)
	require.True(t, st.RemindAfter.IsZero())
}

func TestPromptUpgradeNowStartsUpgrade(t *testing.T) {
	for _, tc := range []struct {
		name       string
		startErr   error
		noTick     bool
		wantStarts int
		wantNotes  int
	}{
		{name: "started", wantStarts: 1},
		{name: "start fails", startErr: errors.New("no console"), wantStarts: 1, wantNotes: 1},
		// Starting it from the prompt's own event handling would run
		// the console command re-entrantly, with the prompt still open.
		{name: "not started when the loop takes no more ticks", noTick: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr := newPromptTestManager(t, 'y')
			notifs := &recordingNotifications{}
			mgr.cfg.Notifications = notifs
			starter := &countingStarter{err: tc.startErr}
			mgr.cfg.Starter = starter
			if tc.noTick {
				// The loop shows the prompt and then stops taking ticks.
				shown := false
				mgr.cfg.ScheduleNextTick = func(fn func()) bool {
					if shown {
						return false
					}
					shown = true
					fn()
					return true
				}
			}

			mgr.showPrompt(context.Background(), Manifest{
				Version: "v9.9.9", URL: "u", SHA256: "s",
			})

			require.Equal(t, tc.wantStarts, starter.n)
			require.Equal(t, tc.wantNotes, notifs.count())
			st := mgr.loadState(context.Background())
			require.Empty(t, st.SkippedVersions)
			require.True(t, st.RemindAfter.IsZero())
		})
	}
}

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
			mgr := newPromptTestManager(t, tc.key)
			got, err := mgr.PromptChoice(context.Background(), manifest)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			tc.assert(t, mgr.loadState(context.Background()))
		})
	}
}

func TestPromptChoiceWithoutPrompter(t *testing.T) {
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
