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
	"testing"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"unstable.build/rune/internal/handler/finder"
)

func TestNewExtensionMetadata(t *testing.T) {
	_, meta := NewExtension()

	if meta.DeveloperID == "" {
		t.Fatal("developer id is empty")
	}
	if meta.DeveloperEmail == "" {
		t.Fatal("developer email is empty")
	}
	if meta.DeveloperKey == "" {
		t.Fatal("developer key is empty")
	}
	if meta.ExtensionID != "fuzzy_search" {
		t.Fatalf("extension id = %q, want fuzzy_search", meta.ExtensionID)
	}
	if meta.ExtensionName != "Fuzzy Search" {
		t.Fatalf("extension name = %q, want Fuzzy Search", meta.ExtensionName)
	}
	if meta.ExtensionVersion == "" {
		t.Fatal("extension version is empty")
	}
	want := append([]extensionapi.Permission{
		extensionapi.PermissionCommands,
		extensionapi.PermissionConfig,
		extensionapi.PermissionSyntaxTree,
		extensionapi.PermissionPackages,
	}, finder.Permissions()...)
	for _, perm := range want {
		if _, ok := meta.Permissions[perm]; !ok {
			t.Fatalf("permission %q missing", perm)
		}
	}
}
