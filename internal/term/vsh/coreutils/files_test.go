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
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

func TestFileCommands(t *testing.T) {
	t.Parallel()
	cp, mv, rm := coreutils.Cp, coreutils.Mv, coreutils.Rm
	mkdir, rmdir, touch := coreutils.Mkdir, coreutils.Rmdir, coreutils.Touch
	a, b := files["a.txt"], files["b.txt"]
	tree := map[string]string{".hidden": files["dir/.hidden"], "c.txt": files["dir/c.txt"], "sub/d.txt": files["dir/sub/d.txt"]}
	under := func(dir string) map[string]string {
		out := map[string]string{}
		for name, content := range tree {
			out[dir+"/"+name] = content
		}
		return out
	}
	runCases(t, []cmdCase{
		{name: "cp file to file", cmd: cp, args: []string{"a.txt", "new.txt"}, want: map[string]string{"new.txt": a, "a.txt": a}},
		{name: "cp overwrites", cmd: cp, args: []string{"a.txt", "b.txt"}, want: map[string]string{"b.txt": a}},
		{name: "cp empty file", cmd: cp, args: []string{"empty", "e2"}, want: map[string]string{"e2": ""}},
		{name: "cp into dir", cmd: cp, args: []string{"a.txt", "dir"}, want: map[string]string{"dir/a.txt": a}},
		{name: "cp into dir with slash", cmd: cp, args: []string{"a.txt", "dir/"}, want: map[string]string{"dir/a.txt": a}},
		{name: "cp many into dir", cmd: cp, args: []string{"a.txt", "b.txt", "dir/sub"},
			want: map[string]string{"dir/sub/a.txt": a, "dir/sub/b.txt": b}},
		{name: "cp relative to dir", cmd: cp, dir: "dir", args: []string{"c.txt", "../c2"}, want: map[string]string{"c2": files["dir/c.txt"]}},
		{name: "cp absolute", cmd: cp, dir: "dir", args: []string{"/a.txt", "/deep"}, want: map[string]string{"deep/a.txt": a}},
		{name: "cp -f", cmd: cp, args: []string{"-f", "a.txt", "z"}, want: map[string]string{"z": a}},
		{name: "cp onto itself", cmd: cp, args: []string{"a.txt", "a.txt"},
			stderr: "cp: 'a.txt' and 'a.txt' are the same file\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "cp into its own dir", cmd: cp, args: []string{"a.txt", "."},
			stderr: "cp: 'a.txt' and './a.txt' are the same file\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "cp onto itself by another path", cmd: cp, args: []string{"a.txt", "dir/../a.txt"},
			stderr: "cp: 'a.txt' and 'dir/../a.txt' are the same file\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "cp into a missing dir", cmd: cp, args: []string{"a.txt", "no/x"},
			stderr: "cp: cannot create regular file 'no/x': No such file or directory\n", code: 1, absent: []string{"no"}},
		{name: "cp through a file", cmd: cp, args: []string{"a.txt", "b.txt/x"},
			stderr: "cp: cannot create regular file 'b.txt/x': Not a directory\n", code: 1, want: map[string]string{"b.txt": b}},
		{name: "cp missing source", cmd: cp, args: []string{"nope", "x"}, stderr: "cp: nope: No such file or directory\n", code: 1, absent: []string{"x"}},
		{name: "cp missing among sources", cmd: cp, args: []string{"nope", "a.txt", "dir"},
			stderr: "cp: nope: No such file or directory\n", code: 1, want: map[string]string{"dir/a.txt": a}, absent: []string{"dir/nope"}},
		{name: "cp dir without -r", cmd: cp, args: []string{"dir", "dir2"}, stderr: "cp: dir: -r not specified; omitting directory\n", code: 1,
			absent: []string{"dir2"}},
		{name: "cp dir without -r among files", cmd: cp, args: []string{"dir", "a.txt", "deep"},
			stderr: "cp: dir: -r not specified; omitting directory\n", code: 1, want: map[string]string{"deep/a.txt": a}, absent: []string{"deep/dir"}},
		{name: "cp -r tree with hidden files", cmd: cp, args: []string{"-r", "dir", "copy"}, want: under("copy"), dirs: []string{"copy/sub"}},
		{name: "cp -R", cmd: cp, args: []string{"-R", "dir", "copy"}, want: under("copy")},
		{name: "cp -r into existing dir", cmd: cp, args: []string{"-r", "dir", "deep"}, want: under("deep/dir")},
		{name: "cp -r to a name sharing the prefix", cmd: cp, args: []string{"-r", "dir", "dir-copy"}, want: under("dir-copy")},
		{name: "cp -r file", cmd: cp, args: []string{"-r", "a.txt", "x"}, want: map[string]string{"x": a}},
		{name: "cp -r nested dir", cmd: cp, args: []string{"-r", "deep/x", "x2"}, want: map[string]string{"x2/y/z.txt": "z\n"}},
		{name: "cp -r into itself", cmd: cp, args: []string{"-r", "dir", "dir/sub"},
			stderr: "cp: dir: cannot copy a directory, 'dir', into itself, 'dir/sub'\n", code: 1, absent: []string{"dir/sub/dir"}},
		{name: "cp -r onto itself", cmd: cp, args: []string{"-r", "dir", "."},
			stderr: "cp: dir: cannot copy a directory, 'dir', into itself, '.'\n", code: 1, want: under("dir")},
		{name: "cp missing dest", cmd: cp, args: []string{"a.txt"}, stderr: "cp: missing destination file operand\n", code: 2},
		{name: "cp no operands", cmd: cp, stderr: "cp: missing destination file operand\n", code: 2},
		{name: "cp many to file", cmd: cp, args: []string{"a.txt", "b.txt", "nonl"}, stderr: "cp: target 'nonl' is not a directory\n", code: 2,
			want: map[string]string{"nonl": files["nonl"]}},
		{name: "cp many to missing", cmd: cp, args: []string{"a.txt", "b.txt", "nodir"}, stderr: "cp: target 'nodir' is not a directory\n", code: 2,
			absent: []string{"nodir"}},
		{name: "cp bad option", cmd: cp, args: []string{"-z", "a", "b"}, stderr: "cp: invalid option -- 'z'\n", code: 2},

		{name: "mv rename", cmd: mv, args: []string{"a.txt", "z"}, want: map[string]string{"z": a}, absent: []string{"a.txt"}},
		{name: "mv overwrites", cmd: mv, args: []string{"a.txt", "b.txt"}, want: map[string]string{"b.txt": a}, absent: []string{"a.txt"}},
		{name: "mv into dir", cmd: mv, args: []string{"a.txt", "dir"}, want: map[string]string{"dir/a.txt": a}, absent: []string{"a.txt"}},
		{name: "mv many into dir", cmd: mv, args: []string{"a.txt", "b.txt", "deep/x"},
			want: map[string]string{"deep/x/a.txt": a, "deep/x/b.txt": b}, absent: []string{"a.txt", "b.txt"}},
		{name: "mv across dirs", cmd: mv, args: []string{"dir/sub/d.txt", "deep/x/y/d2"},
			want: map[string]string{"deep/x/y/d2": "delta\n"}, absent: []string{"dir/sub/d.txt"}, dirs: []string{"dir/sub"}},
		{name: "mv relative to dir", cmd: mv, dir: "dir/sub", args: []string{"d.txt", ".."},
			want: map[string]string{"dir/d.txt": "delta\n"}, absent: []string{"dir/sub/d.txt"}},
		{name: "mv -f", cmd: mv, args: []string{"-f", "a.txt", "z"}, want: map[string]string{"z": a}, absent: []string{"a.txt"}},
		{name: "mv dir", cmd: mv, args: []string{"dir", "d2"}, want: under("d2"), absent: []string{"dir"}},
		{name: "mv dir into dir", cmd: mv, args: []string{"dir", "deep"}, want: under("deep/dir"), absent: []string{"dir"}},
		{name: "mv dir and file into dir", cmd: mv, args: []string{"dir", "a.txt", "deep/x"},
			want: map[string]string{"deep/x/dir/sub/d.txt": "delta\n", "deep/x/a.txt": a}, absent: []string{"dir", "a.txt"}},
		{name: "mv onto itself", cmd: mv, args: []string{"a.txt", "a.txt"}, want: map[string]string{"a.txt": a}},
		{name: "mv into its own dir", cmd: mv, args: []string{"a.txt", "."}, want: map[string]string{"a.txt": a}},
		{name: "mv dir onto itself", cmd: mv, args: []string{"dir", "."}, want: under("dir")},
		{name: "mv dir into itself", cmd: mv, args: []string{"dir", "dir/sub"},
			stderr: "mv: dir: cannot move 'dir' to a subdirectory of itself, 'dir/sub'\n", code: 1, want: under("dir"), absent: []string{"dir/sub/dir"}},
		{name: "mv missing", cmd: mv, args: []string{"nope", "x"}, stderr: "mv: nope: No such file or directory\n", code: 1, absent: []string{"x"}},
		{name: "mv missing among sources", cmd: mv, args: []string{"nope", "a.txt", "dir"},
			stderr: "mv: nope: No such file or directory\n", code: 1, want: map[string]string{"dir/a.txt": a}, absent: []string{"a.txt"}},
		{name: "mv missing dest", cmd: mv, args: []string{"a.txt"}, stderr: "mv: missing destination file operand\n", code: 2,
			want: map[string]string{"a.txt": a}},
		{name: "mv many to file", cmd: mv, args: []string{"a.txt", "b.txt", "nonl"}, stderr: "mv: target 'nonl' is not a directory\n", code: 2,
			want: map[string]string{"a.txt": a, "b.txt": b}},
		{name: "mv bad option", cmd: mv, args: []string{"-r", "a.txt", "z"}, stderr: "mv: invalid option -- 'r'\n", code: 2, absent: []string{"z"}},

		{name: "rm file", cmd: rm, args: []string{"a.txt"}, absent: []string{"a.txt"}},
		{name: "rm many", cmd: rm, args: []string{"a.txt", "dir/c.txt"}, absent: []string{"a.txt", "dir/c.txt"}, dirs: []string{"dir"}},
		{name: "rm relative to dir", cmd: rm, dir: "dir", args: []string{"sub/d.txt"}, absent: []string{"dir/sub/d.txt"}},
		{name: "rm missing", cmd: rm, args: []string{"nope"}, stderr: "rm: nope: No such file or directory\n", code: 1},
		{name: "rm missing among files", cmd: rm, args: []string{"nope", "a.txt"}, stderr: "rm: nope: No such file or directory\n", code: 1,
			absent: []string{"a.txt"}},
		{name: "rm -f missing", cmd: rm, args: []string{"-f", "nope"}},
		{name: "rm -f missing among files", cmd: rm, args: []string{"-f", "nope", "a.txt"}, absent: []string{"a.txt"}},
		{name: "rm -f no operands", cmd: rm, args: []string{"-f"}},
		{name: "rm no operands", cmd: rm, stderr: "rm: missing operand\n", code: 2},
		{name: "rm dir without -r", cmd: rm, args: []string{"dir"}, stderr: "rm: dir: Is a directory\n", code: 1, want: under("dir")},
		{name: "rm -f dir without -r", cmd: rm, args: []string{"-f", "dir", "a.txt"}, stderr: "rm: dir: Is a directory\n", code: 1,
			want: under("dir"), absent: []string{"a.txt"}},
		{name: "rm -r", cmd: rm, args: []string{"-r", "dir"}, absent: []string{"dir", "dir/sub", "dir/sub/d.txt", "dir/.hidden"}},
		{name: "rm -R", cmd: rm, args: []string{"-R", "deep"}, absent: []string{"deep"}},
		{name: "rm -r file", cmd: rm, args: []string{"-r", "a.txt"}, absent: []string{"a.txt"}},
		{name: "rm -r subdir", cmd: rm, args: []string{"-r", "dir/sub"}, absent: []string{"dir/sub"}, want: map[string]string{"dir/c.txt": files["dir/c.txt"]}},
		{name: "rm -r missing", cmd: rm, args: []string{"-r", "nope"}, stderr: "rm: nope: No such file or directory\n", code: 1},
		{name: "rm -rf missing and dir", cmd: rm, args: []string{"-rf", "nope", "dir"}, absent: []string{"dir"}},
		{name: "rm -r -f", cmd: rm, args: []string{"-r", "-f", "deep", "dir"}, absent: []string{"deep", "dir"}},
		{name: "rm dot without -r", cmd: rm, args: []string{"."}, stderr: "rm: .: Is a directory\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "rm -r dot", cmd: rm, args: []string{"-r", "."},
			stderr: "rm: refusing to remove '.' or '..' directory: skipping '.'\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "rm -rf dot slash", cmd: rm, args: []string{"-rf", "./"},
			stderr: "rm: refusing to remove '.' or '..' directory: skipping './'\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "rm -rf dot dot", cmd: rm, dir: "dir", args: []string{"-rf", ".."},
			stderr: "rm: refusing to remove '.' or '..' directory: skipping '..'\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "rm -r trailing dot dot", cmd: rm, args: []string{"-r", "dir/sub/..", "a.txt"},
			stderr: "rm: refusing to remove '.' or '..' directory: skipping 'dir/sub/..'\n", code: 1, want: under("dir"), absent: []string{"a.txt"}},
		{name: "rm -rf root", cmd: rm, args: []string{"-rf", "/"},
			stderr: "rm: it is dangerous to operate recursively on '/'\nrm: use --no-preserve-root to override this failsafe\n", code: 1,
			want: map[string]string{"a.txt": a}},
		{name: "rm -r root by another path", cmd: rm, dir: "dir", args: []string{"-r", "../"},
			stderr: "rm: refusing to remove '.' or '..' directory: skipping '../'\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "rm -rf root through a dir", cmd: rm, args: []string{"-rf", "/dir/.."},
			stderr: "rm: refusing to remove '.' or '..' directory: skipping '/dir/..'\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "rm -rf double slash", cmd: rm, args: []string{"-rf", "//"},
			stderr: "rm: it is dangerous to operate recursively on '//'\nrm: use --no-preserve-root to override this failsafe\n", code: 1,
			want: map[string]string{"a.txt": a}},
		{name: "rm bad option", cmd: rm, args: []string{"-i", "a.txt"}, stderr: "rm: invalid option -- 'i'\n", code: 2, want: map[string]string{"a.txt": a}},

		{name: "mkdir", cmd: mkdir, args: []string{"newdir"}, dirs: []string{"newdir"}},
		{name: "mkdir many", cmd: mkdir, args: []string{"x", "y", "x/z"}, dirs: []string{"x", "y", "x/z"}},
		{name: "mkdir relative to dir", cmd: mkdir, dir: "dir", args: []string{"n"}, dirs: []string{"dir/n"}},
		{name: "mkdir existing", cmd: mkdir, args: []string{"dir"}, stderr: "mkdir: cannot create directory 'dir': File exists\n", code: 1},
		{name: "mkdir existing file", cmd: mkdir, args: []string{"a.txt"}, stderr: "mkdir: cannot create directory 'a.txt': File exists\n", code: 1,
			want: map[string]string{"a.txt": a}},
		{name: "mkdir keeps going", cmd: mkdir, args: []string{"dir", "x"}, stderr: "mkdir: cannot create directory 'dir': File exists\n", code: 1,
			dirs: []string{"x"}},
		{name: "mkdir -p existing", cmd: mkdir, args: []string{"-p", "dir"}},
		{name: "mkdir missing parent", cmd: mkdir, args: []string{"no/parent"},
			stderr: "mkdir: cannot create directory 'no/parent': No such file or directory\n", code: 1, absent: []string{"no"}},
		{name: "mkdir -p parents", cmd: mkdir, args: []string{"-p", "p/q/r"}, dirs: []string{"p", "p/q", "p/q/r"}},
		{name: "mkdir -p under existing", cmd: mkdir, args: []string{"-p", "dir/sub/new/deeper"}, dirs: []string{"dir/sub/new/deeper"}},
		{name: "mkdir over file", cmd: mkdir, args: []string{"-p", "a.txt"}, stderr: "mkdir: cannot create directory 'a.txt': Not a directory\n", code: 1},
		{name: "mkdir -p through a file", cmd: mkdir, args: []string{"-p", "a.txt/x"},
			stderr: "mkdir: cannot create directory 'a.txt/x': Not a directory\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "mkdir through a file", cmd: mkdir, args: []string{"a.txt/x"},
			stderr: "mkdir: cannot create directory 'a.txt/x': Not a directory\n", code: 1, want: map[string]string{"a.txt": a}},
		{name: "mkdir -m", cmd: mkdir, args: []string{"-m", "700", "x"}, dirs: []string{"x"}},
		{name: "mkdir no operand", cmd: mkdir, stderr: "mkdir: missing operand\n", code: 2},
		{name: "mkdir -m without mode", cmd: mkdir, args: []string{"-m"}, stderr: "mkdir: option requires an argument -- 'm'\n", code: 2},

		{name: "rmdir empty", cmd: rmdir, seedDirs: []string{"e"}, args: []string{"e"}, absent: []string{"e"}},
		{name: "rmdir trailing slash", cmd: rmdir, seedDirs: []string{"e"}, args: []string{"e/"}, absent: []string{"e"}},
		{name: "rmdir many", cmd: rmdir, seedDirs: []string{"e", "f/g"}, args: []string{"e", "f/g"}, absent: []string{"e", "f/g"}, dirs: []string{"f"}},
		{name: "rmdir keeps going", cmd: rmdir, seedDirs: []string{"e"}, args: []string{"dir", "e"},
			stderr: "rmdir: failed to remove 'dir': Directory not empty\n", code: 1, absent: []string{"e"}, want: under("dir")},
		{name: "rmdir -p", cmd: rmdir, seedDirs: []string{"e/f/g"}, args: []string{"-p", "e/f/g"}, absent: []string{"e"}},
		{name: "rmdir -p stops at a non-empty parent", cmd: rmdir, seedDirs: []string{"dir/e/f"}, args: []string{"-p", "dir/e/f"},
			stderr: "rmdir: failed to remove 'dir': Directory not empty\n", code: 1, absent: []string{"dir/e"}, want: under("dir")},
		{name: "rmdir -p absolute", cmd: rmdir, seedDirs: []string{"e/f"}, args: []string{"-p", "/e/f"}, absent: []string{"e"}},
		{name: "rmdir -p reports the parent", cmd: rmdir, seedDirs: []string{"dir/e"}, args: []string{"-p", "dir/e/"},
			stderr: "rmdir: failed to remove 'dir': Directory not empty\n", code: 1, absent: []string{"dir/e"}},
		{name: "rmdir reports the operand as typed", cmd: rmdir, args: []string{"nope/"},
			stderr: "rmdir: failed to remove 'nope/': No such file or directory\n", code: 1},
		{name: "rmdir missing", cmd: rmdir, args: []string{"nope"}, stderr: "rmdir: failed to remove 'nope': No such file or directory\n", code: 1},
		{name: "rmdir file", cmd: rmdir, args: []string{"a.txt"}, stderr: "rmdir: failed to remove 'a.txt': Not a directory\n", code: 1,
			want: map[string]string{"a.txt": a}},
		{name: "rmdir non-empty", cmd: rmdir, args: []string{"dir"}, stderr: "rmdir: failed to remove 'dir': Directory not empty\n", code: 1},
		{name: "rmdir only hidden entries", cmd: rmdir, seed: map[string]string{"h/.x": ""}, args: []string{"h"},
			stderr: "rmdir: failed to remove 'h': Directory not empty\n", code: 1},
		{name: "rmdir no operand", cmd: rmdir, stderr: "rmdir: missing operand\n", code: 2},

		{name: "touch new", cmd: touch, args: []string{"t"}, want: map[string]string{"t": ""}},
		{name: "touch keeps content", cmd: touch, args: []string{"a.txt"}, want: map[string]string{"a.txt": a}},
		{name: "touch many", cmd: touch, args: []string{"x", "dir/y", "nonl"},
			want: map[string]string{"x": "", "dir/y": "", "nonl": files["nonl"]}},
		{name: "touch relative to dir", cmd: touch, dir: "dir", args: []string{"t1", "sub/t2"},
			want: map[string]string{"dir/t1": "", "dir/sub/t2": ""}},
		{name: "touch dir", cmd: touch, args: []string{"dir"}, dirs: []string{"dir"}},
		{name: "touch through a file", cmd: touch, args: []string{"a.txt/x", "y"},
			stderr: "touch: cannot touch 'a.txt/x': Not a directory\n", code: 1, want: map[string]string{"a.txt": a, "y": ""}},
		{name: "touch in a missing dir", cmd: touch, args: []string{"no/x"},
			stderr: "touch: cannot touch 'no/x': No such file or directory\n", code: 1, absent: []string{"no"}},
		{name: "touch no operand", cmd: touch, stderr: "touch: missing file operand\n", code: 2},
		{name: "touch bad option", cmd: touch, args: []string{"-a", "x"}, stderr: "touch: invalid option -- 'a'\n", code: 2, absent: []string{"x"}},
	})
}

func TestFileCommandsStopWithContext(t *testing.T) {
	t.Parallel()
	tests := []cmdCase{
		{name: "cp", cmd: coreutils.Cp, args: []string{"a.txt", "x"}, absent: []string{"x"}},
		{name: "mv", cmd: coreutils.Mv, args: []string{"a.txt", "x"}, absent: []string{"x"}},
		{name: "rm", cmd: coreutils.Rm, args: []string{"-rf", "dir"}, dirs: []string{"dir"}},
		{name: "mkdir", cmd: coreutils.Mkdir, args: []string{"x"}, absent: []string{"x"}},
		{name: "rmdir", cmd: coreutils.Rmdir, args: []string{"deep/x/y"}, dirs: []string{"deep/x/y"}},
		{name: "touch", cmd: coreutils.Touch, args: []string{"x"}, absent: []string{"x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Files: files})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var out bytes.Buffer
			err := tc.cmd.Run(ctx, invocation(f.Scheme, tc.cmd, "/", &out, &out, &out, tc.args...))
			assert.ErrorIs(t, err, context.Canceled)
			assert.Empty(t, out.String())
			for _, name := range tc.absent {
				_, err := f.Scheme.Stat(f.Path(name))
				assert.Error(t, err, "%s is absent", name)
			}
			for _, name := range tc.dirs {
				_, err := f.Scheme.Stat(f.Path(name))
				assert.NoError(t, err, "%s is kept", name)
			}
		})
	}
}
