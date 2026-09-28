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

package main

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/ideupgrade"
	"unstable.build/rune/internal/ide/upgradeshell"
)

// upgradeConfig captures the values read from the "upgrade" stanza of
// the user's starlark configuration.
type upgradeConfig struct {
	autoCheckEnabled bool
	checkPeriod      time.Duration
}

func defaultUpgradeConfig() upgradeConfig {
	return upgradeConfig{
		autoCheckEnabled: true,
		checkPeriod:      ideupgrade.DefaultCheckPeriod,
	}
}

// loadUpgradeConfig reads the upgrade stanza from the IDE configuration.
// It is forgiving: missing values fall back to defaults.
func loadUpgradeConfig(i *ide.IDE) upgradeConfig {
	out := defaultUpgradeConfig()
	cfg, err := i.Config().GetConfig("upgrade")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			log.Warnf("load upgrade config: %v", err)
		}
		return out
	}
	if v, err := cfg.GetBool("auto_check_enabled"); err == nil {
		out.autoCheckEnabled = v
	} else if !errors.Is(err, config.ErrNotFound) {
		log.Warnf("load upgrade.auto_check_enabled: %v", err)
	}
	if v, err := cfg.GetString("check_period"); err == nil {
		if d, perr := time.ParseDuration(v); perr == nil {
			out.checkPeriod = d
		} else {
			log.Warnf("parse upgrade.check_period %q: %v", v, perr)
		}
	} else if !errors.Is(err, config.ErrNotFound) {
		log.Warnf("load upgrade.check_period: %v", err)
	}
	return out
}

// scheduleUpgradeCheck wires up an ideupgrade.Manager and starts its
// background check loop. When auto-check is disabled the manager is
// still constructed so the `upgrade` command continues to work.
//
// Returns the Manager (which may be nil if construction failed or the
// manifest URL is unavailable) so the caller can register commands
// that operate on it.
func scheduleUpgradeCheck(
	ctx context.Context, i *ide.IDE, manifestBaseURL string,
	scheduleNextTick func(func()) bool,
) *ideupgrade.Manager {
	if manifestBaseURL == "" {
		return nil
	}
	upCfg := loadUpgradeConfig(i)

	mgr, err := ideupgrade.New(ideupgrade.Config{
		CurrentVersion:   debug.Tag,
		Arch:             fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH),
		ManifestURL:      manifestBaseURL,
		Storage:          i.Storage(),
		Notifications:    i.Notifications(),
		WindowManager:    i.WindowManager(),
		ScheduleNextTick: scheduleNextTick,
		CheckPeriod:      upCfg.checkPeriod,
	})
	if err != nil {
		_, _ = i.Notifications().Notify(browserapi.LevelError,
			"Could not initialize upgrade checker: %v", err)
		return nil
	}

	// An OS-packaged build cannot install its own upgrade, so the
	// background check is skipped outright: it would only produce a
	// nag prompt whose action is guaranteed to fail.
	if upCfg.autoCheckEnabled && debug.OSPackaged != "true" {
		mgr.Start(ctx)
	}
	return mgr
}

// registerUpgradeCommand registers the `upgrade` console command
// against the given IDE. The command checks for a new release and, if
// one is available, prompts the user to install it. The user can
// decline at the prompt, so a single command serves both "check" and
// "upgrade" intents.
//
// It is a console command rather than an ex-command so the manifest
// fetch, the confirmation prompt and the install all run off the
// editor's event loop, and so the install can stream its progress into
// the console.
func registerUpgradeCommand(i *ide.IDE, mgr *ideupgrade.Manager) error {
	if mgr == nil {
		return nil
	}
	// Still registered in OS-packaged builds: the command explains
	// where updates come from, which beats an unknown-command error.
	h := upgradeshell.New(upgradeshell.Config{
		Manager:    mgr,
		OSPackaged: debug.OSPackaged == "true",
	})
	if err := i.RegisterREPLCommand(upgradeshell.Manual(), h); err != nil {
		return fmt.Errorf("register '%s': %w", upgradeshell.CommandName, err)
	}
	return nil
}
