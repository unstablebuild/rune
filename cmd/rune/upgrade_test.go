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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/upgradeshell"
)

func TestRegisterUpgradeCommandWithoutManager(t *testing.T) {
	require.NoError(t, registerUpgradeCommand(nil, nil))
}

func TestUpgradeAliasTargetsConsoleCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.star")
	require.NoError(t, os.WriteFile(path, nil, 0o644))

	cfg, err := ide.Config(path, runeDefaultConfig())
	require.NoError(t, err)
	command, err := cfg.GetConfig("command")
	require.NoError(t, err)
	aliases, err := command.GetConfig("aliases")
	require.NoError(t, err)
	got, err := aliases.GetString(upgradeshell.CommandName)
	require.NoError(t, err)
	require.Equal(t, "console "+upgradeshell.CommandName, got)
}
