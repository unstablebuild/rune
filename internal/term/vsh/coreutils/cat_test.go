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
	"fmt"
	"strings"
	"testing"

	"unstable.build/rune/internal/term/vsh/coreutils"
)

func TestCat(t *testing.T) {
	t.Parallel()
	cat := coreutils.Cat
	big := strings.Repeat("0123456789", 20000)
	runCases(t, []cmdCase{
		{name: "file", cmd: cat, args: []string{"a.txt"}, stdout: "alpha\nbeta\ngamma\n"},
		{name: "files", cmd: cat, args: []string{"nonl", "b.txt"}, stdout: "no newlineone two\nthree\n"},
		{name: "stdin", cmd: cat, stdin: "in", stdout: "in"},
		{name: "dash", cmd: cat, stdin: "in\n", args: []string{"-", "empty"}, stdout: "in\n"},
		{name: "dash in the middle", cmd: cat, stdin: "mid\n", args: []string{"b.txt", "-", "nonl"},
			stdout: "one two\nthree\nmid\nno newline"},
		{name: "dash twice reads stdin once", cmd: cat, stdin: "once\n", args: []string{"-", "-"}, stdout: "once\n"},
		{name: "empty file", cmd: cat, args: []string{"empty"}},
		{name: "empty stdin", cmd: cat},
		{name: "same file twice", cmd: cat, args: []string{"nonl", "nonl"}, stdout: "no newlineno newline"},
		{name: "large file", cmd: cat, seed: map[string]string{"big": big}, args: []string{"big"}, stdout: big},
		{name: "binary content", cmd: cat, seed: map[string]string{"bin.dat": "\x00\xff\x01\n\x00"}, args: []string{"bin.dat"},
			stdout: "\x00\xff\x01\n\x00"},
		{name: "absolute path", cmd: cat, dir: "dir", args: []string{"/a.txt"}, stdout: "alpha\nbeta\ngamma\n"},
		{name: "relative to dir", cmd: cat, dir: "dir", args: []string{"c.txt", "../nonl"}, stdout: "Gamma ray\nno newline"},
		{name: "dot dot beyond the root", cmd: cat, args: []string{"../../a.txt"}, stdout: "alpha\nbeta\ngamma\n"},
		{name: "operand after --", cmd: cat, seed: map[string]string{"-n": "dash n\n"}, args: []string{"--", "-n"}, stdout: "dash n\n"},

		{name: "-n", cmd: cat, args: []string{"-n", "a.txt"}, stdout: "     1\talpha\n     2\tbeta\n     3\tgamma\n"},
		{name: "-n continues across files", cmd: cat, args: []string{"-n", "b.txt", "a.txt"},
			stdout: "     1\tone two\n     2\tthree\n     3\talpha\n     4\tbeta\n     5\tgamma\n"},
		{name: "-n continues across stdin", cmd: cat, stdin: "in\n", args: []string{"-n", "b.txt", "-", "nonl"},
			stdout: "     1\tone two\n     2\tthree\n     3\tin\n     4\tno newline"},
		{name: "-n without a final newline", cmd: cat, args: []string{"-n", "nonl"}, stdout: "     1\tno newline"},
		{name: "-n joins a line split across files", cmd: cat, args: []string{"-n", "nonl", "b.txt"},
			stdout: "     1\tno newlineone two\n     2\tthree\n"},
		{name: "-n numbers blank lines", cmd: cat, stdin: "\n\nx\n", args: []string{"-n"},
			stdout: "     1\t\n     2\t\n     3\tx\n"},
		{name: "-n empty input", cmd: cat, args: []string{"-n", "empty"}},
		{name: "-n numbers fill their column", cmd: cat, stdin: strings.Repeat("x\n", 100000), args: []string{"-n"},
			stdout: numbered(100000, "x")},
		{name: "-n line longer than the buffer", cmd: cat, seed: map[string]string{"big": big + "\n" + big}, args: []string{"-n", "big"},
			stdout: "     1\t" + big + "\n     2\t" + big},
		{name: "-n after operands", cmd: cat, args: []string{"nonl", "-n"}, stdout: "     1\tno newline"},

		{name: "missing", cmd: cat, args: []string{"a.txt", "nope"}, stdout: "alpha\nbeta\ngamma\n",
			stderr: "cat: nope: No such file or directory\n", code: 1},
		{name: "missing between good files", cmd: cat, args: []string{"nonl", "nope", "dir", "b.txt"},
			stdout: "no newlineone two\nthree\n",
			stderr: "cat: nope: No such file or directory\ncat: dir: Is a directory\n", code: 1},
		{name: "directory", cmd: cat, args: []string{"dir"}, stderr: "cat: dir: Is a directory\n", code: 1},
		{name: "path through a file", cmd: cat, args: []string{"a.txt/x"}, stderr: "cat: a.txt/x: No such file or directory\n", code: 1},
		{name: "-n keeps numbering past a missing file", cmd: cat, args: []string{"-n", "nonl", "nope", "b.txt"},
			stdout: "     1\tno newlineone two\n     2\tthree\n", stderr: "cat: nope: No such file or directory\n", code: 1},
		{name: "bad option", cmd: cat, args: []string{"-z"}, stderr: "cat: invalid option -- 'z'\n", code: 2},
		{name: "bad option after good", cmd: cat, args: []string{"-nz", "a.txt"}, stderr: "cat: invalid option -- 'z'\n", code: 2},
		{name: "unknown long option", cmd: cat, args: []string{"--number"}, stderr: "cat: unrecognized option '--number'\n", code: 2},
	})
}

func TestCatStopsWithContext(t *testing.T) {
	t.Parallel()
	runCancelCases(t, []cancelCase{
		{name: "copy", cmd: coreutils.Cat},
		{name: "-n", cmd: coreutils.Cat, args: []string{"-n"}},
		{name: "dash", cmd: coreutils.Cat, args: []string{"-"}},
	})
}

// numbered is what cat -n prints for n lines of line.
func numbered(n int, line string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		b.WriteString(fmt.Sprintf("%6d\t%s\n", i, line))
	}
	return b.String()
}
