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

//go:build windows

package main

import (
	"os"
	"time"
)

// resolveLoginPath returns the current PATH unchanged. Windows has no login
// shell whose startup files extend PATH, and the probe's POSIX script could
// only spend its whole timeout failing.
func resolveLoginPath(time.Duration, func() (string, error)) (string, error) {
	return os.Getenv("PATH"), nil
}
