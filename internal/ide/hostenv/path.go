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

import (
	"path"
	"strings"
)

func pathListSeparator(windows bool) string {
	if windows {
		return ";"
	}
	return ":"
}

func prependPATH(goos, dir, base string) string {
	windows := goos == "windows"
	return composePATH(windows, []string{dir}, splitPATH(base, windows), nil)
}

func composePATH(windows bool, prefix, base, suffix []string) string {
	entries := append([]string(nil), prefix...)
	for _, e := range base {
		if e == "" || !hasPATHDir(prefix, e, windows) {
			entries = append(entries, e)
		}
	}
	for _, e := range suffix {
		if !hasPATHDir(entries, e, windows) {
			entries = append(entries, e)
		}
	}
	return strings.Join(entries, pathListSeparator(windows))
}

func splitPATH(list string, windows bool) []string {
	if list == "" {
		return nil
	}
	if !windows {
		return strings.Split(list, ":")
	}
	var entries []string
	start, quoted := 0, false
	for i := range len(list) {
		switch list[i] {
		case '"':
			quoted = !quoted
		case ';':
			if !quoted {
				entries = append(entries, list[start:i])
				start = i + 1
			}
		}
	}
	return append(entries, list[start:])
}

func samePATHDir(entry, dir string, windows bool) bool {
	if entry == "" {
		return false
	}
	if !windows {
		return path.Clean(entry) == path.Clean(dir)
	}
	norm := func(p string) string {
		p = strings.ReplaceAll(p, `"`, "")
		return path.Clean(strings.ReplaceAll(p, `\`, "/"))
	}
	return strings.EqualFold(norm(entry), norm(dir))
}

func hasPATHDir(entries []string, dir string, windows bool) bool {
	for _, e := range entries {
		if e == dir || samePATHDir(e, dir, windows) {
			return true
		}
	}
	return false
}

func dedupPATH(entries []string, windows bool) []string {
	var out []string
	for _, e := range entries {
		if e != "" && !hasPATHDir(out, e, windows) {
			out = append(out, e)
		}
	}
	return out
}
