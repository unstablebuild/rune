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

package debug

// BuildDateLayout is the canonical time layout for the BuildDate
// compile-time variable.
const BuildDateLayout = "2006-01-02T15:04:05Z07:00"

var (
	// Tag is a compile-time variable
	Tag = "development"
	// Package is a compile-time variable
	Package = "gotui"
	// Commit is a compile-time variable
	Commit = "HEAD"
	// ReportsDir is a compile-time variable.
	// If left empty, debug helpers will use the return
	// of os.TempDir.
	ReportsDir = ""
	// DebugBuild is a compile-time variable set to "true" by
	// the Makefile's debug target. When set, the IDE wires up
	// debug-only ex commands (`panic`, `crash`) that would be
	// unsafe to ship in release builds.
	DebugBuild = ""
	// BuildDate is a compile-time variable holding the RFC3339 UTC
	// date this binary was built (see BuildDateLayout).
	BuildDate = ""
	// OSPackaged is a compile-time variable set to "true" by the
	// distribution packaging under dist/. The package manager owns
	// the install prefix and the upgrade path, so the in-product
	// upgrader stands down rather than fighting it over files it
	// does not own.
	OSPackaged = ""
)
