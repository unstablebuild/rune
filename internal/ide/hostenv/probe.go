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

package hostenv

import "strings"

const shellEnvMarker = "RUNE_SHELL_ENV_START"

func pathFromMarkerEnv(out string) string {
	if idx := strings.LastIndex(out, shellEnvMarker); idx >= 0 {
		out = out[idx+len(shellEnvMarker):]
	}
	for entry := range strings.SplitSeq(out, "\x00") {
		if v, ok := strings.CutPrefix(entry, "PATH="); ok {
			return v
		}
	}
	return ""
}
