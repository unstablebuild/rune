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

// Command buildstamp prints the current time formatted for the
// debug.BuildDate ldflag. Go, not the host's date(1), renders the
// string, so it is identical across platforms and always parses with
// debug.BuildDateLayout.
//
// Usage:
//
//	go run ./cmd/buildstamp
package main

import (
	"time"

	"unstable.build/rune/internal/debug"
)

// formatStamp renders t as the debug.BuildDate string: RFC3339 in UTC,
// so the stamp `rune --version` reports round trips exactly.
func formatStamp(t time.Time) string {
	return t.UTC().Format(debug.BuildDateLayout)
}
