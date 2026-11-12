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
	"errors"
	"log/slog"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/agentools"
)

// webSearchTool returns the web_search tool for the provider named by
// web_search.provider, or nil when none is named. The tool sends the
// agent's queries to a third party, so it stays off until the user
// picks one. getenv reads the provider's optional API key.
func webSearchTool(
	pconfig config.Config, noti browserapi.Notifications, getenv func(string) string,
) agent.Tool {
	sub, err := pconfig.GetConfig("web_search")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'web_search' from extension config", "error", err)
		}
		return nil
	}
	provider, err := sub.GetString("provider")
	if err != nil {
		if !errors.Is(err, config.ErrNotFound) {
			slog.Warn("get 'web_search.provider' from extension config", "error", err)
		}
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "":
		return nil
	case "keenable":
		return agentools.NewWebSearch(agentools.NewKeenableSearch(getenv("KEENABLE_API_KEY")))
	default:
		_, _ = noti.Notify(browserapi.LevelWarn,
			"web_search: unknown provider %q, expected keenable", provider)
		return nil
	}
}
