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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
)

func TestReadPyOverrides(t *testing.T) {
	tests := []struct {
		name           string
		cfg            config.Config
		wantCommand    string
		wantAlternates map[string]string
		wantWarning    bool
	}{
		{name: "nil config overrides nothing"},
		{name: "empty config overrides nothing", cfg: config.JSONFromMap(map[string]any{})},
		{
			name:        "command override",
			cfg:         config.JSONFromMap(map[string]any{"command": "pyright-langserver --stdio"}),
			wantCommand: "pyright-langserver --stdio",
		},
		{
			name: "command and alternates override",
			cfg: config.JSONFromMap(map[string]any{
				"command": "pyright-langserver --stdio",
				"alternate_commands": map[string]any{
					"textDocument/formatting": "ruff server",
				},
			}),
			wantCommand:    "pyright-langserver --stdio",
			wantAlternates: map[string]string{"textDocument/formatting": "ruff server"},
		},
		{
			name: "alternates override",
			cfg: config.JSONFromMap(map[string]any{
				"alternate_commands": map[string]any{
					"textDocument/formatting": "black server",
				},
			}),
			wantAlternates: map[string]string{"textDocument/formatting": "black server"},
		},
		{
			name: "invalid alternate value warns and overrides nothing",
			cfg: config.JSONFromMap(map[string]any{
				"alternate_commands": map[string]any{
					"textDocument/formatting": 42,
				},
			}),
			wantWarning: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			notify := newFakeNotifications()
			cmd, alt := readPyOverrides(tc.cfg, notify)
			assert.Equal(t, tc.wantCommand, cmd)
			assert.Equal(t, tc.wantAlternates, alt)
			assert.Equal(t, tc.wantWarning, len(notify.notifs) > 0)
		})
	}
}

func TestPyLogLevel(t *testing.T) {
	t.Run("defaults to server default", func(t *testing.T) {
		assert.Empty(t, pyLogLevel(nil, newFakeNotifications()))
	})

	t.Run("reads debug level", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{
			"debug": map[string]any{"log_level": "debug"},
		})
		assert.Equal(t, "debug", pyLogLevel(cfg, newFakeNotifications()))
	})

	t.Run("rejects invalid level", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{
			"debug": map[string]any{"log_level": "verbose"},
		})
		notify := newFakeNotifications()
		assert.Empty(t, pyLogLevel(cfg, notify))
		assert.NotEmpty(t, notify.notifs)
	})

	t.Run("invalid level without notifications", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{
			"debug": map[string]any{"log_level": "verbose"},
		})
		assert.Empty(t, pyLogLevel(cfg, nil))
	})
}

func TestPyWatchEvents(t *testing.T) {
	onChange := []textapi.EventType{
		textapi.EventTypeOpen, textapi.EventTypeChange, textapi.EventTypeCreate,
	}
	openOnly := []textapi.EventType{textapi.EventTypeOpen}

	t.Run("absent key enables change/create", func(t *testing.T) {
		assert.Equal(t, onChange, pyWatchEvents(config.NopConfig(), newFakeNotifications()))
	})

	t.Run("true enables change/create", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"watch_events": true})
		assert.Equal(t, onChange, pyWatchEvents(cfg, newFakeNotifications()))
	})

	t.Run("false restores open-only", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"watch_events": false})
		assert.Equal(t, openOnly, pyWatchEvents(cfg, newFakeNotifications()))
	})

	t.Run("non-bool warns and defaults to change/create", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"watch_events": "yes"})
		notify := newFakeNotifications()
		assert.Equal(t, onChange, pyWatchEvents(cfg, notify))
		assert.NotEmpty(t, notify.notifs)
	})
}

func TestPyDiagnosticMode(t *testing.T) {
	t.Run("nil config returns empty", func(t *testing.T) {
		assert.Equal(t, "", pyDiagnosticMode(nil, newFakeNotifications()))
	})

	t.Run("absent key returns empty", func(t *testing.T) {
		assert.Equal(t, "", pyDiagnosticMode(config.NopConfig(), newFakeNotifications()))
	})

	t.Run("workspace accepted", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"diagnostic_mode": "workspace"})
		notify := newFakeNotifications()
		assert.Equal(t, "workspace", pyDiagnosticMode(cfg, notify))
		assert.Empty(t, notify.notifs)
	})

	t.Run("openFilesOnly accepted", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"diagnostic_mode": "openFilesOnly"})
		assert.Equal(t, "openFilesOnly", pyDiagnosticMode(cfg, newFakeNotifications()))
	})

	t.Run("off accepted", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"diagnostic_mode": "off"})
		assert.Equal(t, "off", pyDiagnosticMode(cfg, newFakeNotifications()))
	})

	t.Run("unknown value warns and is ignored", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"diagnostic_mode": "all"})
		notify := newFakeNotifications()
		assert.Equal(t, "", pyDiagnosticMode(cfg, notify))
		assert.NotEmpty(t, notify.notifs)
	})

	t.Run("non-string warns and is ignored", func(t *testing.T) {
		cfg := config.JSONFromMap(map[string]any{"diagnostic_mode": 3})
		notify := newFakeNotifications()
		assert.Equal(t, "", pyDiagnosticMode(cfg, notify))
		assert.NotEmpty(t, notify.notifs)
	})
}
