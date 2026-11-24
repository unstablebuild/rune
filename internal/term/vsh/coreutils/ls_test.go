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
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

func TestLs(t *testing.T) {
	t.Parallel()
	ls := coreutils.Ls
	sized := map[string]string{
		"sizes/s1023": strings.Repeat("x", 1023),
		"sizes/s1024": strings.Repeat("x", 1024),
		"sizes/s1536": strings.Repeat("x", 1536),
		"sizes/s1m":   strings.Repeat("x", 1<<20),
	}
	runCases(t, []cmdCase{
		{name: "dir", cmd: ls, args: []string{"dir"}, stdout: "c.txt\nsub\n"},
		{name: "cwd", cmd: ls, dir: "dir", stdout: "c.txt\nsub\n"},
		{name: "root", cmd: ls, stdout: "a.txt\nb.txt\nbin\ndeep\ndir\nempty\nlong.txt\nnonl\nwords.txt\n"},
		{name: "parent", cmd: ls, dir: "dir/sub", args: []string{".."}, stdout: "c.txt\nsub\n"},
		{name: "trailing slash", cmd: ls, args: []string{"dir/"}, stdout: "c.txt\nsub\n"},
		{name: "-1", cmd: ls, args: []string{"-1", "dir"}, stdout: "c.txt\nsub\n"},
		{name: "-a", cmd: ls, args: []string{"-a", "dir"}, stdout: ".hidden\nc.txt\nsub\n"},
		{name: "-A", cmd: ls, args: []string{"-A", "dir"}, stdout: ".hidden\nc.txt\nsub\n"},
		{name: "only hidden entries", cmd: ls, seed: map[string]string{"only/.h": ""}, args: []string{"only"}},
		{name: "sorted by byte order", cmd: ls, seed: map[string]string{"s/b": "", "s/a": "", "s/B": "", "s/_": "", "s/1": ""},
			args: []string{"s"}, stdout: "1\nB\n_\na\nb\n"},
		{name: "-F", cmd: ls, args: []string{"-F", "dir/sub", "dir", "/bin/ls"}, stdout: "/bin/ls*\n\ndir:\nc.txt\nsub/\n\ndir/sub:\nd.txt\n"},
		{name: "-F plain file", cmd: ls, args: []string{"-F", "a.txt"}, stdout: "a.txt\n"},
		{name: "-d", cmd: ls, args: []string{"-d", "dir", "a.txt"}, stdout: "a.txt\ndir\n"},
		{name: "-d cwd", cmd: ls, args: []string{"-d"}, stdout: ".\n"},
		{name: "-dF", cmd: ls, args: []string{"-dF", "dir"}, stdout: "dir/\n"},
		{name: "-R", cmd: ls, args: []string{"-R", "dir"}, stdout: "dir:\nc.txt\nsub\n\ndir/sub:\nd.txt\n"},
		{name: "-R nested", cmd: ls, args: []string{"-R", "deep"}, stdout: "deep:\nx\n\ndeep/x:\ny\n\ndeep/x/y:\nz.txt\n"},
		{name: "-R trailing slash", cmd: ls, args: []string{"-R", "dir/"}, stdout: "dir/:\nc.txt\nsub\n\ndir/sub:\nd.txt\n"},
		{name: "-R skips hidden directories", cmd: ls, seed: map[string]string{"h/.git/x": "", "h/f": ""}, args: []string{"-R", "h"},
			stdout: "h:\nf\n"},
		{name: "-aR enters hidden directories", cmd: ls, seed: map[string]string{"h/.git/x": "", "h/f": ""}, args: []string{"-aR", "h"},
			stdout: "h:\n.git\nf\n\nh/.git:\nx\n"},
		{name: "-R with files and dirs", cmd: ls, args: []string{"-R", "a.txt", "deep/x"},
			stdout: "a.txt\n\ndeep/x:\ny\n\ndeep/x/y:\nz.txt\n"},
		{name: "-dR does not recurse", cmd: ls, args: []string{"-dR", "dir"}, stdout: "dir\n"},
		{name: "operands sorted", cmd: ls, args: []string{"nonl", "a.txt"}, stdout: "a.txt\nnonl\n"},
		{name: "files and dir", cmd: ls, args: []string{"a.txt", "dir"}, stdout: "a.txt\n\ndir:\nc.txt\nsub\n"},
		{name: "files before dirs", cmd: ls, args: []string{"dir", "nonl", "a.txt"}, stdout: "a.txt\nnonl\n\ndir:\nc.txt\nsub\n"},
		{name: "many dirs", cmd: ls, args: []string{"dir/sub", "deep/x"}, stdout: "deep/x:\ny\n\ndir/sub:\nd.txt\n"},
		{name: "missing", cmd: ls, args: []string{"nope", "empty"}, stdout: "empty\n", stderr: "ls: nope: No such file or directory\n", code: 1},
		{name: "missing and a dir", cmd: ls, args: []string{"nope", "dir"}, stdout: "dir:\nc.txt\nsub\n",
			stderr: "ls: nope: No such file or directory\n", code: 1},
		{name: "-l", cmd: ls, args: []string{"-l", "/bin/ls", "dir"},
			stdout: "-rwxr-xr-x 1 rune rune 86 DATE /bin/ls\n\ndir:\n-rw-r--r-- 1 rune rune 10 DATE c.txt\ndrwxr-xr-x 1 rune rune  0 DATE sub\n"},
		{name: "-l aligns sizes", cmd: ls, args: []string{"-l", "empty", "long.txt"},
			stdout: "-rw-r--r-- 1 rune rune  0 DATE empty\n-rw-r--r-- 1 rune rune 27 DATE long.txt\n"},
		{name: "-ld", cmd: ls, args: []string{"-ld", "dir"}, stdout: "drwxr-xr-x 1 rune rune 0 DATE dir\n"},
		{name: "-lF", cmd: ls, args: []string{"-lF", "dir"},
			stdout: "-rw-r--r-- 1 rune rune 10 DATE c.txt\ndrwxr-xr-x 1 rune rune  0 DATE sub/\n"},
		{name: "-la", cmd: ls, args: []string{"-la", "dir/sub", "dir"},
			stdout: "dir:\n-rw-r--r-- 1 rune rune  2 DATE .hidden\n-rw-r--r-- 1 rune rune 10 DATE c.txt\ndrwxr-xr-x 1 rune rune  0 DATE sub\n\n" +
				"dir/sub:\n-rw-r--r-- 1 rune rune 6 DATE d.txt\n"},
		{name: "-lh", cmd: ls, args: []string{"-lh", "long.txt"}, stdout: "-rw-r--r-- 1 rune rune 27 DATE long.txt\n"},
		{name: "-lh units", cmd: ls, seed: sized, args: []string{"-lh", "sizes"},
			stdout: "-rw-r--r-- 1 rune rune 1023 DATE s1023\n-rw-r--r-- 1 rune rune 1.0K DATE s1024\n" +
				"-rw-r--r-- 1 rune rune 1.5K DATE s1536\n-rw-r--r-- 1 rune rune 1.0M DATE s1m\n"},
		{name: "-l without -h", cmd: ls, seed: sized, args: []string{"-l", "sizes/s1m", "sizes/s1023"},
			stdout: "-rw-r--r-- 1 rune rune    1023 DATE sizes/s1023\n-rw-r--r-- 1 rune rune 1048576 DATE sizes/s1m\n"},
		{name: "-h without -l", cmd: ls, args: []string{"-h", "dir"}, stdout: "c.txt\nsub\n"},
		{name: "bad option", cmd: ls, args: []string{"-x"}, stderr: "ls: invalid option -- 'x'\n", code: 2},
	})
}

func TestLsOnTerminal(t *testing.T) {
	t.Parallel()
	names := map[string]string{"aaaa": "", "bb": "", "c": "", "dddddd": "", "ee": ""}
	tests := []struct {
		name string
		seed map[string]string
		cols int
		args []string
		// terminal is the output on a pty cols wide and piped the
		// output without a terminal.
		terminal, piped string
	}{
		{name: "columns", seed: names, cols: 20,
			terminal: "aaaa    c\r\nbb      dddddd\r\nbin     ee\r\n", piped: "aaaa\nbb\nbin\nc\ndddddd\nee\n"},
		{name: "-1", seed: names, cols: 20, args: []string{"-1"},
			terminal: "aaaa\r\nbb\r\nbin\r\nc\r\ndddddd\r\nee\r\n", piped: "aaaa\nbb\nbin\nc\ndddddd\nee\n"},
		{name: "one row", seed: names, cols: 80,
			terminal: "aaaa    bb      bin     c       dddddd  ee\r\n", piped: "aaaa\nbb\nbin\nc\ndddddd\nee\n"},
		{name: "narrower than a name", seed: names, cols: 3,
			terminal: "aaaa\r\nbb\r\nbin\r\nc\r\ndddddd\r\nee\r\n", piped: "aaaa\nbb\nbin\nc\ndddddd\nee\n"},
		{name: "-F", seed: map[string]string{"d/x": "", "f": ""}, cols: 80, args: []string{"-F"},
			terminal: "bin/  d/    f\r\n", piped: "bin/\nd/\nf\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Files: tc.seed})
			assert.Equal(t, tc.terminal, onTerminal(t, f, tc.cols, "/", tc.args...), "on a terminal")
			piped, _, code := invoke(t, f.Scheme, coreutils.Ls, "/", "", tc.args...)
			assert.Zero(t, code)
			assert.Equal(t, tc.piped, piped, "without a terminal")
		})
	}
}

func TestColumns(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		names []string
		width int
		want  string
	}{
		{name: "none", width: 80, want: ""},
		{name: "one", names: []string{"a"}, width: 80, want: "a\n"},
		{name: "one row", names: []string{"a", "b", "c"}, width: 80, want: "a  b  c\n"},
		{name: "down columns", names: []string{"aaaa", "bb", "bin", "c", "dddddd", "ee"}, width: 20,
			want: "aaaa    c\nbb      dddddd\nbin     ee\n"},
		{name: "narrower than the longest name", names: []string{"longname", "x"}, width: 4, want: "longname\nx\n"},
		{name: "zero width", names: []string{"a", "b"}, width: 0, want: "a\nb\n"},
		{name: "exactly fits with padding", names: []string{"ab", "cd"}, width: 8, want: "ab  cd\n"},
		{name: "one short of the padding", names: []string{"ab", "cd"}, width: 7, want: "ab\ncd\n"},
		{name: "many names", names: []string{"n0", "n1", "n2", "n3", "n4", "n5", "n6", "n7", "n8", "n9"}, width: 20,
			want: "n0  n2  n4  n6  n8\nn1  n3  n5  n7  n9\n"},
		{name: "short last column", names: []string{"a", "b", "c", "d", "e"}, width: 9, want: "a  c  e\nb  d\n"},
		{name: "fewer columns once rows are known", names: []string{"a", "b", "c", "d"}, width: 9, want: "a  c\nb  d\n"},
		{name: "runes not bytes", names: []string{"é", "ab"}, width: 80, want: "é   ab\n"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, coreutils.Columns(tc.names, tc.width), tc.name)
	}
}

func TestHumanSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{1, "1"},
		{1023, "1023"},
		{1024, "1.0K"},
		{1025, "1.1K"},
		{1536, "1.5K"},
		{10239, "10K"},
		{10240, "10K"},
		{10241, "11K"},
		{999 * 1024, "999K"},
		{1<<20 - 1, "1.0M"},
		{1 << 20, "1.0M"},
		{3 << 19, "1.5M"},
		{1 << 30, "1.0G"},
		{1 << 40, "1.0T"},
		{1 << 50, "1.0P"},
		{1 << 60, "1.0E"},
		{math.MaxInt64, "8.0E"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, coreutils.HumanSize(tc.n), "%d", tc.n)
	}
}

func TestModTime(t *testing.T) {
	t.Parallel()
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "            "},
		{time.Date(2024, time.March, 5, 9, 7, 0, 0, time.UTC), "Mar  5 09:07"},
		{time.Date(2025, time.December, 25, 23, 59, 59, 0, time.UTC), "Dec 25 23:59"},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, coreutils.ModTime(tc.t), "%v", tc.t)
	}
}
