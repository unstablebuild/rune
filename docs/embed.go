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

// Package docs exposes the published documentation to the editor's
// docs:/// workspace. It exists only because go:embed cannot reach
// outside the embedding package's directory.
package docs

import "embed"

// FS holds only the markdown under Root: the site also carries gifs and
// other assets that the docs workspace never serves, so baking them into
// the binary would only bloat it. Patterns are listed per nesting level
// because go:embed globs do not recurse; add a deeper level here if the
// docs tree grows one.
//
//go:embed docs/*.md
//go:embed docs/*/*.md
//go:embed docs/*/*/*.md
var FS embed.FS

// Root is the directory in FS that maps to the docs workspace root, so
// "docs/develop/sdk.md" surfaces as "/develop/sdk.md".
const Root = "docs"
