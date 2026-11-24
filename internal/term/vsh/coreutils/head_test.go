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

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/term/vsh/coreutils"
)

func TestHeadTail(t *testing.T) {
	t.Parallel()
	head, tail := coreutils.Head, coreutils.Tail
	longLine := strings.Repeat("x", 100000)
	runCases(t, []cmdCase{
		{name: "head default", cmd: head, args: []string{"long.txt"}, stdout: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"},
		{name: "head -n", cmd: head, args: []string{"-n", "2", "a.txt"}, stdout: "alpha\nbeta\n"},
		{name: "head -nN", cmd: head, args: []string{"-n2", "a.txt"}, stdout: "alpha\nbeta\n"},
		{name: "head -N", cmd: head, args: []string{"-1", "a.txt"}, stdout: "alpha\n"},
		{name: "head -n -N counts lines", cmd: head, args: []string{"-n", "-1", "a.txt"}, stdout: "alpha\n"},
		{name: "head -n 0", cmd: head, args: []string{"-n", "0", "a.txt"}},
		{name: "head -0", cmd: head, args: []string{"-0", "a.txt"}},
		{name: "head more than lines", cmd: head, args: []string{"-n", "99", "a.txt"}, stdout: "alpha\nbeta\ngamma\n"},
		{name: "head exactly the lines", cmd: head, args: []string{"-3", "a.txt"}, stdout: "alpha\nbeta\ngamma\n"},
		{name: "head no newline", cmd: head, args: []string{"nonl"}, stdout: "no newline"},
		{name: "head last line without newline", cmd: head, stdin: "a\nb", args: []string{"-2"}, stdout: "a\nb"},
		{name: "head blank lines", cmd: head, stdin: "\n\n\nx\n", args: []string{"-2"}, stdout: "\n\n"},
		{name: "head empty", cmd: head, args: []string{"empty"}},
		{name: "head empty stdin", cmd: head},
		{name: "head long lines", cmd: head, stdin: longLine + "\n" + longLine + "\n", args: []string{"-1"}, stdout: longLine + "\n"},
		{name: "head -c", cmd: head, args: []string{"-c", "3", "a.txt"}, stdout: "alp"},
		{name: "head -cN", cmd: head, args: []string{"-c3", "a.txt"}, stdout: "alp"},
		{name: "head -c 0", cmd: head, args: []string{"-c", "0", "a.txt"}},
		{name: "head -c beyond size", cmd: head, args: []string{"-c", "1000", "nonl"}, stdout: "no newline"},
		{name: "head -c spans lines", cmd: head, args: []string{"-c", "8", "a.txt"}, stdout: "alpha\nbe"},
		{name: "head -c wins over -n", cmd: head, args: []string{"-n", "1", "-c", "2", "a.txt"}, stdout: "al"},
		{name: "head -c counts bytes not runes", cmd: head, stdin: "héllo", args: []string{"-c", "3"}, stdout: "h\xc3\xa9"},
		{name: "head last -n wins", cmd: head, args: []string{"-n", "3", "-n", "1", "a.txt"}, stdout: "alpha\n"},
		{name: "head stdin", cmd: head, stdin: "x\ny\nz\n", args: []string{"-n1"}, stdout: "x\n"},
		{name: "head many files", cmd: head, args: []string{"-n1", "a.txt", "b.txt"}, stdout: "==> a.txt <==\nalpha\n\n==> b.txt <==\none two\n"},
		{name: "head many files with stdin", cmd: head, stdin: "in\n", args: []string{"-n1", "-", "b.txt"},
			stdout: "==> standard input <==\nin\n\n==> b.txt <==\none two\n"},
		{name: "head many files with an empty one", cmd: head, args: []string{"-n1", "empty", "a.txt"},
			stdout: "==> empty <==\n\n==> a.txt <==\nalpha\n"},
		{name: "head many files -q", cmd: head, args: []string{"-q", "-n1", "a.txt", "b.txt"}, stdout: "alpha\none two\n"},
		{name: "head one file -v", cmd: head, args: []string{"-v", "-n1", "a.txt"}, stdout: "==> a.txt <==\nalpha\n"},
		{name: "head stdin -v", cmd: head, stdin: "in\n", args: []string{"-v"}, stdout: "==> standard input <==\nin\n"},
		{name: "head -q wins over -v", cmd: head, args: []string{"-qv", "-n1", "a.txt"}, stdout: "alpha\n"},
		{name: "head many files with a missing one", cmd: head, args: []string{"-n1", "a.txt", "nope", "b.txt"},
			stdout: "==> a.txt <==\nalpha\n\n==> b.txt <==\none two\n", stderr: "head: nope: No such file or directory\n", code: 1},
		{name: "head missing first", cmd: head, args: []string{"-n1", "nope", "a.txt"},
			stdout: "==> a.txt <==\nalpha\n", stderr: "head: nope: No such file or directory\n", code: 1},
		{name: "head directory", cmd: head, args: []string{"dir"}, stderr: "head: dir: Is a directory\n", code: 1},
		{name: "head bad count", cmd: head, args: []string{"-n", "x"}, stderr: "head: invalid number of lines: 'x'\n", code: 2},
		{name: "head bad numeric shorthand", cmd: head, args: []string{"-5x"}, stderr: "head: invalid number of lines: '5x'\n", code: 2},
		{name: "head bad byte count", cmd: head, args: []string{"-c", "1k"}, stderr: "head: invalid number of bytes: '1k'\n", code: 2},
		{name: "head missing count", cmd: head, args: []string{"-n"}, stderr: "head: option requires an argument -- 'n'\n", code: 2},
		{name: "head bad option", cmd: head, args: []string{"-z"}, stderr: "head: invalid option -- 'z'\n", code: 2},

		{name: "tail default", cmd: tail, args: []string{"long.txt"}, stdout: "3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"},
		{name: "tail -n", cmd: tail, args: []string{"-n", "1", "a.txt"}, stdout: "gamma\n"},
		{name: "tail -nN", cmd: tail, args: []string{"-n1", "a.txt"}, stdout: "gamma\n"},
		{name: "tail -2", cmd: tail, args: []string{"-2", "a.txt"}, stdout: "beta\ngamma\n"},
		{name: "tail -n 0", cmd: tail, args: []string{"-n", "0", "a.txt"}},
		{name: "tail -c", cmd: tail, args: []string{"-c", "4", "nonl"}, stdout: "line"},
		{name: "tail -c 0", cmd: tail, args: []string{"-c", "0", "nonl"}},
		{name: "tail -c beyond size", cmd: tail, args: []string{"-c", "1000", "nonl"}, stdout: "no newline"},
		{name: "tail -c includes the final newline", cmd: tail, args: []string{"-c", "3", "a.txt"}, stdout: "ma\n"},
		{name: "tail more than lines", cmd: tail, args: []string{"-n", "9", "a.txt"}, stdout: "alpha\nbeta\ngamma\n"},
		{name: "tail exactly the lines", cmd: tail, args: []string{"-3", "a.txt"}, stdout: "alpha\nbeta\ngamma\n"},
		{name: "tail no newline", cmd: tail, args: []string{"-1", "nonl"}, stdout: "no newline"},
		{name: "tail last line without newline", cmd: tail, stdin: "a\nb\nc", args: []string{"-2"}, stdout: "b\nc"},
		{name: "tail blank lines", cmd: tail, stdin: "x\n\n\n", args: []string{"-2"}, stdout: "\n\n"},
		{name: "tail only newlines", cmd: tail, stdin: "\n\n\n", args: []string{"-5"}, stdout: "\n\n\n"},
		{name: "tail empty", cmd: tail, args: []string{"empty"}},
		{name: "tail long lines", cmd: tail, stdin: "a\n" + longLine + "\n", args: []string{"-1"}, stdout: longLine + "\n"},
		{name: "tail stdin", cmd: tail, stdin: "x\ny\nz\n", args: []string{"-1"}, stdout: "z\n"},
		{name: "tail many files", cmd: tail, args: []string{"-n1", "a.txt", "nonl"}, stdout: "==> a.txt <==\ngamma\n\n==> nonl <==\nno newline"},
		{name: "tail many files -q", cmd: tail, args: []string{"-q", "-1", "a.txt", "b.txt"}, stdout: "gamma\nthree\n"},
		{name: "tail missing first", cmd: tail, args: []string{"-n1", "nope", "a.txt"},
			stdout: "==> a.txt <==\ngamma\n", stderr: "tail: nope: No such file or directory\n", code: 1},
		{name: "tail bad count", cmd: tail, args: []string{"-n", "1.5"}, stderr: "tail: invalid number of lines: '1.5'\n", code: 2},
	})
}

func TestHeadTailStopWithContext(t *testing.T) {
	t.Parallel()
	runCancelCases(t, []cancelCase{
		{name: "head", cmd: coreutils.Head},
		{name: "head -c", cmd: coreutils.Head, args: []string{"-c", "1"}},
		{name: "tail", cmd: coreutils.Tail},
	})
}

func TestFirstLastLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		data        string
		n           int
		first, last string
	}{
		{data: "", n: 0, first: "", last: ""},
		{data: "", n: 3, first: "", last: ""},
		{data: "a\nb\nc\n", n: 0, first: "", last: ""},
		{data: "a\nb\nc\n", n: 1, first: "a\n", last: "c\n"},
		{data: "a\nb\nc\n", n: 2, first: "a\nb\n", last: "b\nc\n"},
		{data: "a\nb\nc\n", n: 3, first: "a\nb\nc\n", last: "a\nb\nc\n"},
		{data: "a\nb\nc\n", n: 4, first: "a\nb\nc\n", last: "a\nb\nc\n"},
		{data: "a\nb\nc", n: 1, first: "a\n", last: "c"},
		{data: "a\nb\nc", n: 3, first: "a\nb\nc", last: "a\nb\nc"},
		{data: "abc", n: 1, first: "abc", last: "abc"},
		{data: "\n", n: 1, first: "\n", last: "\n"},
		{data: "\n\n", n: 1, first: "\n", last: "\n"},
		{data: "\n\n", n: 5, first: "\n\n", last: "\n\n"},
		{data: "x\n\n", n: 1, first: "x\n", last: "\n"},
		{data: "\nx", n: 1, first: "\n", last: "x"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.first, string(coreutils.FirstLines([]byte(tc.data), tc.n)), "first %d of %q", tc.n, tc.data)
		assert.Equal(t, tc.last, string(coreutils.LastLines([]byte(tc.data), tc.n)), "last %d of %q", tc.n, tc.data)
	}
}
