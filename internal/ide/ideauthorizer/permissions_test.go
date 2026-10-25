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

package ideauthorizer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
)

func TestPluginPermissionProgramStorageKeyIgnoresArgs(t *testing.T) {
	t.Parallel()

	keyA := pluginPermissionProgramStorageKey("/bin/test",
		extensionapi.PermissionBrowserWindowManager)
	keyB := pluginPermissionProgramStorageKey("/bin/test",
		extensionapi.PermissionBrowserWindowManager)
	assert.Equal(t, keyA, keyB)

	assert.NotEqual(t, keyA, pluginPermissionProgramStorageKey("/bin/test",
		extensionapi.PermissionStorage),
		"key must differ by permission")
	assert.NotEqual(t, keyA, pluginPermissionProgramStorageKey("/bin/other",
		extensionapi.PermissionBrowserWindowManager),
		"key must differ by path")

	argKey := pluginPermissionStorageKey("/bin/test", []string{"a"},
		extensionapi.PermissionBrowserWindowManager)
	assert.NotEqual(t, keyA, argKey,
		"program-scoped key must not collide with the argv-scoped key")
}

func TestPermissionActionText(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		permission extensionapi.Permission
		want       string
	}{
		{extensionapi.PermissionBrowserWindowManager, "manage the window manager"},
		{extensionapi.PermissionBrowserResourceOpener, "open files and links"},
	} {
		t.Run(string(tc.permission), func(t *testing.T) {
			assert.Equal(t, tc.want, PermissionActionText(tc.permission))
		})
	}
}
