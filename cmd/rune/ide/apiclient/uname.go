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

package apiclient

import "bytes"

type sysinfo struct {
	Name    string
	Node    string
	Release string
	Version string
	Machine string
	OS      string
}

// utsnameToString returns the NUL-terminated string at the start of a utsname field, or
// the whole field when it has no terminator. The field length differs between kernels
// (65 bytes on Linux, 256 on Darwin and most BSDs, others elsewhere), which is why it
// takes a slice.
func utsnameToString(field []byte) string {
	if i := bytes.IndexByte(field, 0); i >= 0 {
		field = field[:i]
	}
	return string(field)
}
