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

// Command extension_typescript serves the TypeScript and JavaScript
// language workspace extension.
package main

import (
	"log/slog"
	"os"

	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"unstable.build/rune/internal/debug"
)

func main() {
	debug.StartPProfOnSignal()
	extension, metadata := NewExtension()
	if err := extensionapi.ServeWorkspaceExtension(extension, metadata); err != nil {
		slog.Error("serve extension", "error", err)
		os.Exit(1)
	}
}
