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

// Package imageuri provides a tui.Component that displays the image
// behind a file, http or https URI, or at a path in a workspace file
// system.
//
// For URIs, the encoded bytes are cached in a storageapi.Service so
// that components showing the same URI share a single download while
// the entry is younger than its TTL. An expired entry is still served
// when refreshing it fails. A progress animation is drawn while the image
// loads. When the image cannot be loaded, or the writer cannot draw
// images, the caller's problem art is drawn centered instead.
package imageuri
