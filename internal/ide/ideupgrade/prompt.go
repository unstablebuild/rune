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
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
)

const (
	upgradeNowOpt  = "   Upgrade Now   "
	remindLaterOpt = "   Remind Me Later   "
	skipVersionOpt = "   Skip This Version   "
)

// Choice is the user's answer to the upgrade prompt.
type Choice int

// The possible answers to the upgrade prompt. ChoiceDismissed covers
// both an explicit dismissal and the case where no Prompter is
// available to render the prompt.
const (
	ChoiceDismissed Choice = iota
	ChoiceUpgradeNow
	ChoiceRemindLater
	ChoiceSkipVersion
)

// PromptChoice renders the floating upgrade prompt and blocks until
// the user answers it or ctx is cancelled. Remind/skip answers are
// persisted before returning. It must not be called from the IDE
// event loop: the prompt is scheduled onto that loop and the answer
// arrives from it.
func (m *Manager) PromptChoice(
	ctx context.Context, manifest Manifest,
) (Choice, error) {
	if m.cfg.Prompter == nil {
		return ChoiceDismissed, nil
	}
	// Buffered so the first answer never blocks the event loop and a
	// dismissal following a selection is simply dropped.
	answer := make(chan Choice, 1)
	reply := func(c Choice) {
		select {
		case answer <- c:
		default:
		}
	}
	cfg, h := m.newPrompt(manifest, reply)
	scheduled := m.cfg.ScheduleNextTick(func() {
		if _, err := m.cfg.Prompter.Prompt(cfg, h); err != nil {
			log.WithError(err).Warn("ideupgrade: show upgrade prompt")
			reply(ChoiceDismissed)
		}
	})
	if !scheduled {
		return ChoiceDismissed, errors.New("could not schedule upgrade prompt")
	}

	select {
	case <-ctx.Done():
		return ChoiceDismissed, ctx.Err()
	case c := <-answer:
		m.persistChoice(ctx, c, manifest)
		return c, nil
	}
}

// showPrompt renders the floating upgrade prompt for manifest without
// blocking. The user's choice is persisted (remind / skip) and, when
// "Upgrade Now" is chosen, the upgrade is started with Starter.
func (m *Manager) showPrompt(ctx context.Context, manifest Manifest) {
	if m.cfg.Prompter == nil {
		_, _ = m.cfg.Notifications.Notify(browserapi.LevelInfo,
			"Rune %s is available", manifest.Version)
		return
	}

	cfg, h := m.newPrompt(manifest, func(c Choice) {
		if c != ChoiceUpgradeNow {
			m.persistChoice(ctx, c, manifest)
			return
		}
		// On the next tick, so the prompt has closed before the
		// upgrade takes over a window. Starting it here instead would
		// dispatch from inside the prompt's own event handling.
		scheduled := m.cfg.ScheduleNextTick(func() {
			if err := m.cfg.Starter.StartUpgrade(); err != nil {
				_, _ = m.cfg.Notifications.Notify(browserapi.LevelError,
					"Could not start the upgrade to %s: %v", manifest.Version, err)
			}
		})
		if !scheduled {
			// The loop takes no more ticks, so neither would a
			// notification.
			log.Warnf("ideupgrade: could not schedule the upgrade to %s", manifest.Version)
		}
	})

	m.cfg.ScheduleNextTick(func() {
		if _, err := m.cfg.Prompter.Prompt(cfg, h); err != nil {
			log.WithError(err).Warn("ideupgrade: show upgrade prompt")
		}
	})
}

// persistChoice records a remind/skip answer. Upgrade and dismissal
// leave the stored state untouched so the next check prompts again.
func (m *Manager) persistChoice(ctx context.Context, c Choice, manifest Manifest) {
	switch c {
	case ChoiceRemindLater:
		m.remindLater(ctx)
	case ChoiceSkipVersion:
		m.skipVersion(ctx, manifest.Version)
	}
}

// newPrompt returns the upgrade prompt for manifest, and the handler
// that calls onChoice with the user's answer, or with ChoiceDismissed
// when the prompt is closed without a selection.
func (m *Manager) newPrompt(
	manifest Manifest, onChoice func(Choice),
) (browserapi.PromptConfig, handler.PromptHandler) {
	message := fmt.Sprintf("A new version of Rune is available: **%s** (current: %s).\n\nUpgrade now?",
		manifest.Version, m.cfg.CurrentVersion)
	if manifest.Changelog != "" {
		message = manifest.Changelog + "\n\n---\n\n" + message
	}

	cfg := browserapi.PromptConfig{
		Message:  message,
		Options:  []string{upgradeNowOpt, remindLaterOpt, skipVersionOpt},
		Bindings: []term.KeyComb{{Ch: 'y'}, {Ch: 'l'}, {Ch: 's'}},
	}
	return cfg, handler.FuncPromptHandler(func(idx int, _ string) {
		switch idx {
		case 0:
			onChoice(ChoiceUpgradeNow)
		case 1:
			onChoice(ChoiceRemindLater)
		case 2:
			onChoice(ChoiceSkipVersion)
		}
	}, func() error {
		onChoice(ChoiceDismissed)
		return nil
	})
}
