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

package coreutils_test

import (
	"strings"
	"testing"

	"unstable.build/rune/internal/term/vsh/coreutils"
)

func TestWc(t *testing.T) {
	t.Parallel()
	wc := coreutils.Wc
	runCases(t, []cmdCase{
		{name: "all", cmd: wc, args: []string{"b.txt"}, stdout: " 2  3 14 b.txt\n"},
		{name: "all wider", cmd: wc, args: []string{"long.txt"}, stdout: "12 12 27 long.txt\n"},
		{name: "-l", cmd: wc, args: []string{"-l", "a.txt"}, stdout: "3 a.txt\n"},
		{name: "-w", cmd: wc, args: []string{"-w", "words.txt"}, stdout: "5 words.txt\n"},
		{name: "-c", cmd: wc, args: []string{"-c", "nonl"}, stdout: "10 nonl\n"},
		{name: "-lwc is the default", cmd: wc, args: []string{"-lwc", "b.txt"}, stdout: " 2  3 14 b.txt\n"},
		{name: "separate flags are the default", cmd: wc, args: []string{"-c", "-w", "-l", "b.txt"}, stdout: " 2  3 14 b.txt\n"},
		{name: "-lw", cmd: wc, args: []string{"-lw", "a.txt"}, stdout: "3 3 a.txt\n"},
		{name: "-cl keeps lines before bytes", cmd: wc, args: []string{"-cl", "a.txt"}, stdout: " 3 17 a.txt\n"},
		{name: "-cw keeps words before bytes", cmd: wc, args: []string{"-cw", "b.txt"}, stdout: " 3 14 b.txt\n"},
		{name: "repeated flag", cmd: wc, args: []string{"-ll", "a.txt"}, stdout: "3 a.txt\n"},
		{name: "empty file", cmd: wc, args: []string{"empty"}, stdout: "0 0 0 empty\n"},
		{name: "no final newline", cmd: wc, args: []string{"nonl"}, stdout: " 0  2 10 nonl\n"},
		{name: "-w stdin", cmd: wc, stdin: "a b  c\n", args: []string{"-w"}, stdout: "3\n"},
		{name: "stdin", cmd: wc, stdin: "a b  c\n", stdout: "1 3 7\n"},
		{name: "empty stdin", cmd: wc, stdout: "0 0 0\n"},
		{name: "named stdin", cmd: wc, stdin: "x\n", args: []string{"-"}, stdout: "1 1 2 -\n"},
		{name: "multibyte counts bytes", cmd: wc, stdin: "héllo wörld\n", stdout: " 1  2 14\n"},
		{name: "tabs and carriage returns separate words", cmd: wc, stdin: "a\tb\r\nc", stdout: "1 3 6\n"},
		{name: "only whitespace", cmd: wc, stdin: " \n\t\n", stdout: "2 0 4\n"},
		{name: "total", cmd: wc, args: []string{"-l", "a.txt", "b.txt"}, stdout: "      3 a.txt\n      2 b.txt\n      5 total\n"},
		{name: "total of all counts", cmd: wc, args: []string{"a.txt", "b.txt"},
			stdout: "      3       3      17 a.txt\n      2       3      14 b.txt\n      5       6      31 total\n"},
		{name: "total with stdin", cmd: wc, stdin: "x\n", args: []string{"-", "b.txt"},
			stdout: "      1       1       2 -\n      2       3      14 b.txt\n      3       4      16 total\n"},
		{name: "same file twice", cmd: wc, args: []string{"-w", "a.txt", "a.txt"}, stdout: "      3 a.txt\n      3 a.txt\n      6 total\n"},
		{name: "counts wider than the minimum", cmd: wc, seed: map[string]string{"big": strings.Repeat("x", 10000000)},
			args: []string{"-c", "big", "a.txt"}, stdout: "10000000 big\n      17 a.txt\n10000017 total\n"},
		{name: "missing", cmd: wc, args: []string{"-l", "nope"}, stderr: "wc: nope: No such file or directory\n", code: 1},
		{name: "missing among files still totals", cmd: wc, args: []string{"-l", "a.txt", "nope"},
			stdout: "      3 a.txt\n      3 total\n", stderr: "wc: nope: No such file or directory\n", code: 1},
		{name: "every file missing", cmd: wc, args: []string{"-l", "nope", "gone"}, stdout: "      0 total\n",
			stderr: "wc: nope: No such file or directory\nwc: gone: No such file or directory\n", code: 1},
		{name: "directory", cmd: wc, args: []string{"dir"}, stderr: "wc: dir: Is a directory\n", code: 1},
		{name: "relative to dir", cmd: wc, dir: "dir", args: []string{"-l", "c.txt", "sub/d.txt"},
			stdout: "      1 c.txt\n      1 sub/d.txt\n      2 total\n"},
		{name: "bad option", cmd: wc, args: []string{"-m"}, stderr: "wc: invalid option -- 'm'\n", code: 2},
	})
}

func TestWcStopsWithContext(t *testing.T) {
	t.Parallel()
	runCancelCases(t, []cancelCase{
		{name: "stdin", cmd: coreutils.Wc},
		{name: "dash", cmd: coreutils.Wc, args: []string{"-l", "-"}},
	})
}
