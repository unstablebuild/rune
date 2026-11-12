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

//go:build !linux && !darwin

package ideupgrade

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDetectRunningInstallUnsupportedOS(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "rune.app", "bin", "rune")
	_, err := detectRunningInstallOS(bin, Config{})

	var notSupported *ErrUpgradeNotSupported
	require.ErrorAs(t, err, &notSupported)
	require.ErrorIs(t, err, errUnsupportedOS)
	require.Equal(t, bin, notSupported.Path)
}
