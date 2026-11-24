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
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
)

func TestGrep(t *testing.T) {
	t.Parallel()
	grep := coreutils.Grep
	long := strings.Repeat("hay", 100000) + "needle"
	many := strings.Repeat("match\n", 10000)
	runCases(t, []cmdCase{
		{name: "match", cmd: grep, args: []string{"a", "a.txt"}, stdout: "alpha\nbeta\ngamma\n"},
		{name: "case sensitive", cmd: grep, args: []string{"the", "words.txt"}, stdout: "the cat\n"},
		{name: "anchor", cmd: grep, args: []string{"^t", "b.txt"}, stdout: "three\n"},
		{name: "empty pattern matches every line", cmd: grep, args: []string{"", "b.txt"}, stdout: "one two\nthree\n"},
		{name: "no final newline", cmd: grep, args: []string{"line", "nonl"}, stdout: "no newline\n"},
		{name: "no match", cmd: grep, args: []string{"zzz", "a.txt"}, code: 1},
		{name: "empty file", cmd: grep, args: []string{"x", "empty"}, code: 1},
		{name: "binary content is text", cmd: grep, seed: map[string]string{"bin.dat": "a\x00b\nzzz\n"}, args: []string{"a", "bin.dat"},
			stdout: "a\x00b\n"},
		{name: "long line", cmd: grep, seed: map[string]string{"long": "x\n" + long + "\n"}, args: []string{"needle$", "long"}, stdout: long + "\n"},
		{name: "many matches", cmd: grep, stdin: many, args: []string{"match"}, stdout: many},
		{name: "-i", cmd: grep, args: []string{"-i", "the", "words.txt"}, stdout: "the cat\nThe dog\n"},
		{name: "-i uppercase pattern", cmd: grep, args: []string{"-i", "GAMMA", "a.txt", "dir/c.txt"}, stdout: "a.txt:gamma\ndir/c.txt:Gamma ray\n"},
		{name: "-v", cmd: grep, args: []string{"-v", "o", "b.txt"}, stdout: "three\n"},
		{name: "-v empty pattern selects nothing", cmd: grep, args: []string{"-v", "", "b.txt"}, code: 1},
		{name: "-n", cmd: grep, args: []string{"-n", "e", "a.txt"}, stdout: "2:beta\n"},
		{name: "-n many files", cmd: grep, args: []string{"-n", "e", "a.txt", "b.txt"}, stdout: "a.txt:2:beta\nb.txt:1:one two\nb.txt:2:three\n"},
		{name: "-vn", cmd: grep, args: []string{"-vn", "e", "a.txt"}, stdout: "1:alpha\n3:gamma\n"},
		{name: "-c", cmd: grep, args: []string{"-c", "a", "a.txt", "b.txt"}, stdout: "a.txt:3\nb.txt:0\n"},
		{name: "-c one file", cmd: grep, args: []string{"-c", "a", "a.txt"}, stdout: "3\n"},
		{name: "-c counts lines not matches", cmd: grep, args: []string{"-c", "a", "dir/c.txt"}, stdout: "1\n"},
		{name: "-c no match", cmd: grep, args: []string{"-c", "zzz", "a.txt"}, stdout: "0\n", code: 1},
		{name: "-cv", cmd: grep, args: []string{"-cv", "a", "a.txt", "b.txt"}, stdout: "a.txt:0\nb.txt:2\n"},
		{name: "-ch", cmd: grep, args: []string{"-ch", "a", "a.txt", "b.txt"}, stdout: "3\n0\n"},
		{name: "-c stdin among files", cmd: grep, stdin: "x\n", args: []string{"-c", "x", "-", "b.txt"}, stdout: "(standard input):1\nb.txt:0\n"},
		{name: "-l", cmd: grep, args: []string{"-l", "three", "a.txt", "b.txt"}, stdout: "b.txt\n"},
		{name: "-l one file", cmd: grep, args: []string{"-l", "a", "a.txt"}, stdout: "a.txt\n"},
		{name: "-l stdin", cmd: grep, stdin: "y\n", args: []string{"-l", "y"}, stdout: "(standard input)\n"},
		{name: "-l wins over -c", cmd: grep, args: []string{"-lc", "a", "a.txt", "b.txt"}, stdout: "a.txt\n"},
		{name: "-L wins over -c", cmd: grep, args: []string{"-Lc", "a", "a.txt", "b.txt"}, stdout: "b.txt\n"},
		{name: "-l no match", cmd: grep, args: []string{"-l", "zzz", "a.txt"}, code: 1},
		{name: "-lv", cmd: grep, args: []string{"-lv", "alpha", "a.txt", "b.txt"}, stdout: "a.txt\nb.txt\n"},
		{name: "-L", cmd: grep, args: []string{"-L", "three", "a.txt", "b.txt"}, stdout: "a.txt\n"},
		{name: "-L nothing listed", cmd: grep, args: []string{"-L", "a", "a.txt"}, code: 1},
		{name: "-Lv", cmd: grep, args: []string{"-Lv", "a", "a.txt", "b.txt"}, stdout: "a.txt\n"},
		{name: "-w", cmd: grep, args: []string{"-w", "cat", "words.txt"}, stdout: "the cat\n"},
		{name: "-w punctuation is a boundary", cmd: grep, seed: map[string]string{"p": "cat.\n(cat)\ncats\nbobcat\ncat_x\n-cat-\n"},
			args: []string{"-w", "cat", "p"}, stdout: "cat.\n(cat)\n-cat-\n"},
		{name: "-w alternation", cmd: grep, args: []string{"-w", "cat|dog", "words.txt"}, stdout: "the cat\nThe dog\n"},
		{name: "-iw", cmd: grep, args: []string{"-iw", "THE", "words.txt"}, stdout: "the cat\nThe dog\n"},
		{name: "-F", cmd: grep, args: []string{"-F", "a.", "a.txt"}, code: 1},
		{name: "-F metacharacters", cmd: grep, seed: map[string]string{"code": "f(x)\nfx\n"}, args: []string{"-F", "f(x)", "code"}, stdout: "f(x)\n"},
		{name: "-iF", cmd: grep, args: []string{"-iF", "GAMMA RAY", "dir/c.txt"}, stdout: "Gamma ray\n"},
		{name: "-wF", cmd: grep, seed: map[string]string{"p": "a.b\na.bc\n"}, args: []string{"-wF", "a.b", "p"}, stdout: "a.b\n"},
		{name: "-E", cmd: grep, args: []string{"-E", "^(al|ga)", "a.txt"}, stdout: "alpha\ngamma\n"},
		{name: "-E alternation", cmd: grep, args: []string{"-E", "o|r", "b.txt"}, stdout: "one two\nthree\n"},
		{name: "-o", cmd: grep, args: []string{"-o", "[a-z]a", "a.txt"}, stdout: "ha\nta\nga\nma\n"},
		{name: "-o many per line", cmd: grep, args: []string{"-o", "o", "b.txt"}, stdout: "o\no\n"},
		{name: "-on", cmd: grep, args: []string{"-on", "t", "b.txt"}, stdout: "1:t\n2:t\n"},
		{name: "-o with names", cmd: grep, args: []string{"-o", "amma", "a.txt", "dir/c.txt"}, stdout: "a.txt:amma\ndir/c.txt:amma\n"},
		{name: "-io", cmd: grep, args: []string{"-io", "THE", "words.txt"}, stdout: "the\nThe\n"},
		{name: "-ov prints whole lines", cmd: grep, args: []string{"-ov", "o", "b.txt"}, stdout: "three\n"},
		{name: "-o skips empty matches", cmd: grep, args: []string{"-o", "x*", "a.txt"}},
		{name: "-o keeps non-empty matches among empty ones", cmd: grep, args: []string{"-o", "a*", "a.txt"}, stdout: "a\na\na\na\na\n"},
		{name: "-q", cmd: grep, args: []string{"-q", "alpha", "a.txt"}},
		{name: "-q no match", cmd: grep, args: []string{"-q", "zzz", "a.txt"}, code: 1},
		{name: "-q wins over -c", cmd: grep, args: []string{"-qc", "a", "a.txt"}},
		{name: "-q match after a missing file", cmd: grep, args: []string{"-q", "alpha", "nope", "a.txt"},
			stderr: "grep: nope: No such file or directory\n"},
		{name: "-q no match and a missing file", cmd: grep, args: []string{"-q", "zzz", "nope", "a.txt"},
			stderr: "grep: nope: No such file or directory\n", code: 2},
		{name: "-e", cmd: grep, args: []string{"-e", "-x", "a.txt"}, code: 1},
		{name: "-e takes every operand as a file", cmd: grep, args: []string{"-e", "alpha", "a.txt"}, stdout: "alpha\n"},
		{name: "-e repeated", cmd: grep, args: []string{"-e", "alpha", "-e", "three", "a.txt", "b.txt"}, stdout: "a.txt:alpha\nb.txt:three\n"},
		{name: "-e repeated -F", cmd: grep, args: []string{"-F", "-e", "a.", "-e", "on", "a.txt", "b.txt"}, stdout: "b.txt:one two\n"},
		{name: "-e repeated -w", cmd: grep, args: []string{"-w", "-e", "cat", "-e", "dog", "words.txt"}, stdout: "the cat\nThe dog\n"},
		{name: "-e repeated -c", cmd: grep, args: []string{"-c", "-e", "^al", "-e", "^ga", "a.txt"}, stdout: "2\n"},
		{name: "-h", cmd: grep, args: []string{"-h", "e", "a.txt", "b.txt"}, stdout: "beta\none two\nthree\n"},
		{name: "-H", cmd: grep, args: []string{"-H", "beta", "a.txt"}, stdout: "a.txt:beta\n"},
		{name: "-H stdin", cmd: grep, stdin: "y\n", args: []string{"-H", "y"}, stdout: "(standard input):y\n"},
		{name: "-r", cmd: grep, args: []string{"-r", "amma", "."}, stdout: "a.txt:gamma\ndir/c.txt:Gamma ray\n"},
		{name: "-rn dir", cmd: grep, args: []string{"-rn", "l", "dir"}, stdout: "dir/sub/d.txt:1:delta\n"},
		{name: "-R", cmd: grep, args: []string{"-R", "delta", "dir"}, stdout: "dir/sub/d.txt:delta\n"},
		{name: "-r default dir", cmd: grep, dir: "dir", args: []string{"-rl", "a"}, stdout: "c.txt\nsub/d.txt\n"},
		{name: "-r hidden files", cmd: grep, args: []string{"-r", "h", "dir"}, stdout: "dir/.hidden:h\n"},
		{name: "-r trailing slash", cmd: grep, args: []string{"-r", "Gamma", "dir/"}, stdout: "dir/c.txt:Gamma ray\n"},
		{name: "-r dot slash", cmd: grep, dir: "dir", args: []string{"-r", "Gamma", "./"}, stdout: "./c.txt:Gamma ray\n"},
		{name: "-r parent", cmd: grep, dir: "dir/sub", args: []string{"-r", "Gamma", ".."}, stdout: "../c.txt:Gamma ray\n"},
		{name: "-r absolute", cmd: grep, dir: "dir", args: []string{"-r", "delta", "/dir"}, stdout: "/dir/sub/d.txt:delta\n"},
		{name: "-r many operands", cmd: grep, args: []string{"-r", "z", "deep", "dir/sub"}, stdout: "deep/x/y/z.txt:z\n"},
		{name: "-rh", cmd: grep, args: []string{"-rh", "delta", "dir"}, stdout: "delta\n"},
		{name: "-rc", cmd: grep, args: []string{"-rc", "a", "dir"}, stdout: "dir/.hidden:0\ndir/c.txt:1\ndir/sub/d.txt:1\n"},
		{name: "-rL", cmd: grep, args: []string{"-rL", "a", "dir"}, stdout: "dir/.hidden\n"},
		{name: "-r stdin", cmd: grep, stdin: "y\n", args: []string{"-r", "y", "-"}, stdout: "(standard input):y\n"},
		{name: "-r missing", cmd: grep, args: []string{"-r", "x", "nope"}, stderr: "grep: nope: No such file or directory\n", code: 2},
		{name: "-r missing and match", cmd: grep, args: []string{"-r", "delta", "nope", "dir"},
			stdout: "dir/sub/d.txt:delta\n", stderr: "grep: nope: No such file or directory\n", code: 2},
		{name: "stdin", cmd: grep, stdin: "x\nyes\n", args: []string{"y"}, stdout: "yes\n"},
		{name: "dash among files", cmd: grep, stdin: "yes\n", args: []string{"y", "-", "nonl"}, stdout: "(standard input):yes\n"},
		{name: "missing", cmd: grep, args: []string{"a", "nope"}, stderr: "grep: nope: No such file or directory\n", code: 2},
		{name: "missing and match", cmd: grep, args: []string{"alpha", "nope", "a.txt"}, stdout: "a.txt:alpha\n", stderr: "grep: nope: No such file or directory\n", code: 2},
		{name: "-q missing and match", cmd: grep, args: []string{"-q", "alpha", "nope", "a.txt"}, stderr: "grep: nope: No such file or directory\n"},
		{name: "missing and no match", cmd: grep, args: []string{"zzz", "a.txt", "nope"}, stderr: "grep: nope: No such file or directory\n", code: 2},
		{name: "directory without -r", cmd: grep, args: []string{"a", "dir"}, stderr: "grep: dir: Is a directory\n", code: 2},
		{name: "bad regexp", cmd: grep, args: []string{"(", "a.txt"}, stderr: "grep: error parsing regexp: missing closing ): `(`\n", code: 2},
		{name: "bad regexp in a repeated -e", cmd: grep, args: []string{"-e", "a", "-e", "[", "a.txt"},
			stderr: "grep: error parsing regexp: missing closing ]: `[`\n", code: 2},
		{name: "-F makes a bad regexp literal", cmd: grep, seed: map[string]string{"p": "a(b\n"}, args: []string{"-F", "(", "p"}, stdout: "a(b\n"},
		{name: "no pattern", cmd: grep, stderr: "grep: usage: grep [-ivnclLrREFwHhoq] [-e PATTERN] PATTERN [FILE...]\n", code: 2},
		{name: "-e without pattern", cmd: grep, args: []string{"-e"}, stderr: "grep: option requires an argument -- 'e'\n", code: 2},
		{name: "unknown option", cmd: grep, args: []string{"-P", "x"}, stderr: "grep: invalid option -- 'P'\n", code: 2},
	})
}

func TestGrepStopsWithContext(t *testing.T) {
	t.Parallel()
	runCancelCases(t, []cancelCase{
		{name: "no match", cmd: coreutils.Grep, args: []string{"z"}},
		{name: "-c", cmd: coreutils.Grep, args: []string{"-c", "y"}},
		{name: "-v", cmd: coreutils.Grep, args: []string{"-v", "y"}},
	})
}

func TestDisplayPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dir, operand, abs, want string
	}{
		{dir: "/", operand: ".", abs: "/a.txt", want: "a.txt"},
		{dir: "/", operand: ".", abs: "/dir/sub/d.txt", want: "dir/sub/d.txt"},
		{dir: "/dir", operand: ".", abs: "/dir/c.txt", want: "c.txt"},
		{dir: "/", operand: "./", abs: "/a.txt", want: "./a.txt"},
		{dir: "/", operand: "dir", abs: "/dir/c.txt", want: "dir/c.txt"},
		{dir: "/", operand: "dir/", abs: "/dir/c.txt", want: "dir/c.txt"},
		{dir: "/", operand: "dir//", abs: "/dir/c.txt", want: "dir//c.txt"},
		{dir: "/", operand: "./dir", abs: "/dir/c.txt", want: "./dir/c.txt"},
		{dir: "/dir/sub", operand: "..", abs: "/dir/c.txt", want: "../c.txt"},
		{dir: "/dir/sub", operand: "../", abs: "/dir/c.txt", want: "../c.txt"},
		{dir: "/dir/sub", operand: "../../deep", abs: "/deep/x/y/z.txt", want: "../../deep/x/y/z.txt"},
		{dir: "/dir", operand: "/dir", abs: "/dir/sub/d.txt", want: "/dir/sub/d.txt"},
		{dir: "/dir", operand: "/", abs: "/a.txt", want: "/a.txt"},
		{dir: "/", operand: "a.txt", abs: "/a.txt", want: "a.txt"},
		{dir: "/dir", operand: "../a.txt", abs: "/a.txt", want: "../a.txt"},
	}
	for _, tc := range tests {
		inv := &vsh.Invocation{Dir: tc.dir}
		assert.Equal(t, tc.want, coreutils.DisplayPath(inv, tc.operand, tc.abs), "%s in %s", tc.operand, tc.dir)
	}
}
