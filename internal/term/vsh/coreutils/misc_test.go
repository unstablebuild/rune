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
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"mvdan.cc/sh/v3/expand"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

func TestMisc(t *testing.T) {
	t.Parallel()
	basename, dirname := coreutils.Basename, coreutils.Dirname
	env, clear, sleep, tee := coreutils.Env, coreutils.Clear, coreutils.Sleep, coreutils.Tee
	runCases(t, []cmdCase{
		{name: "basename", cmd: basename, args: []string{"/a/b/c.txt"}, stdout: "c.txt\n"},
		{name: "basename suffix", cmd: basename, args: []string{"/a/b/c.txt", ".txt"}, stdout: "c\n"},
		{name: "basename suffix not at the end", cmd: basename, args: []string{"a.txt.bak", ".txt"}, stdout: "a.txt.bak\n"},
		{name: "basename suffix that does not match", cmd: basename, args: []string{"a.txt", ".md"}, stdout: "a.txt\n"},
		{name: "basename suffix is the whole name", cmd: basename, args: []string{".txt", ".txt"}, stdout: ".txt\n"},
		{name: "basename suffix is the whole base", cmd: basename, args: []string{"dir/a.txt", "a.txt"}, stdout: "a.txt\n"},
		{name: "basename suffix after a trailing slash", cmd: basename, args: []string{"/a/b.txt/", ".txt"}, stdout: "b\n"},
		{name: "basename trailing slash", cmd: basename, args: []string{"/a/b/"}, stdout: "b\n"},
		{name: "basename trailing slashes", cmd: basename, args: []string{"a//b//"}, stdout: "b\n"},
		{name: "basename root", cmd: basename, args: []string{"/"}, stdout: "/\n"},
		{name: "basename slashes", cmd: basename, args: []string{"///"}, stdout: "/\n"},
		{name: "basename plain", cmd: basename, args: []string{"plain"}, stdout: "plain\n"},
		{name: "basename dot", cmd: basename, args: []string{"."}, stdout: ".\n"},
		{name: "basename dot dot", cmd: basename, args: []string{"a/.."}, stdout: "..\n"},
		{name: "basename empty", cmd: basename, args: []string{""}, stdout: "\n"},
		{name: "basename dash", cmd: basename, args: []string{"-"}, stdout: "-\n"},
		{name: "basename after --", cmd: basename, args: []string{"--", "-x"}, stdout: "-x\n"},
		{name: "basename missing", cmd: basename, stderr: "basename: missing operand\n", code: 2},
		{name: "basename extra operand", cmd: basename, args: []string{"a", "b", "c"}, stderr: "basename: extra operand 'c'\n", code: 2},
		{name: "basename bad option", cmd: basename, args: []string{"-a", "x"}, stderr: "basename: invalid option -- 'a'\n", code: 2},

		{name: "dirname", cmd: dirname, args: []string{"/a/b/c.txt", "plain"}, stdout: "/a/b\n.\n"},
		{name: "dirname trailing slash", cmd: dirname, args: []string{"a/b/"}, stdout: "a\n"},
		{name: "dirname trailing slashes", cmd: dirname, args: []string{"a/b///"}, stdout: "a\n"},
		{name: "dirname inner slashes", cmd: dirname, args: []string{"//a//b//"}, stdout: "//a\n"},
		{name: "dirname root", cmd: dirname, args: []string{"/"}, stdout: "/\n"},
		{name: "dirname slashes", cmd: dirname, args: []string{"///"}, stdout: "/\n"},
		{name: "dirname under root", cmd: dirname, args: []string{"/a"}, stdout: "/\n"},
		{name: "dirname under root with slash", cmd: dirname, args: []string{"/a/"}, stdout: "/\n"},
		{name: "dirname plain", cmd: dirname, args: []string{"a"}, stdout: ".\n"},
		{name: "dirname plain with slash", cmd: dirname, args: []string{"a/"}, stdout: ".\n"},
		{name: "dirname empty", cmd: dirname, args: []string{""}, stdout: ".\n"},
		{name: "dirname dot", cmd: dirname, args: []string{"."}, stdout: ".\n"},
		{name: "dirname is not cleaned", cmd: dirname, args: []string{"a/./b", "a/../b"}, stdout: "a/.\na/..\n"},
		{name: "dirname many", cmd: dirname, args: []string{"a/b", "/c/d/", "e"}, stdout: "a\n/c\n.\n"},
		{name: "dirname after --", cmd: dirname, args: []string{"--", "-x/y"}, stdout: "-x\n"},
		{name: "dirname missing", cmd: dirname, stderr: "dirname: missing operand\n", code: 2},

		{name: "env", cmd: env, stdout: "HOME=/\nPATH=/bin\nX=1\n"},
		{name: "env --", cmd: env, args: []string{"--"}, stdout: "HOME=/\nPATH=/bin\nX=1\n"},
		{name: "env command", cmd: env, args: []string{"ls"}, stderr: "env: running a command is not supported; run it directly\n", code: 125},
		{name: "env assignment", cmd: env, args: []string{"A=1", "ls"}, stderr: "env: running a command is not supported; run it directly\n", code: 125},
		{name: "env bad option", cmd: env, args: []string{"-i"}, stderr: "env: invalid option -- 'i'\n", code: 2},

		{name: "clear", cmd: clear, stdout: "\x1b[H\x1b[2J\x1b[3J"},
		{name: "clear ignores operands", cmd: clear, args: []string{"x"}, stdout: "\x1b[H\x1b[2J\x1b[3J"},

		{name: "sleep bad", cmd: sleep, args: []string{"x"}, stderr: "sleep: invalid time interval 'x'\n", code: 2},
		{name: "sleep zero", cmd: sleep, args: []string{"0", "0s"}},
		{name: "sleep zero in every unit", cmd: sleep, args: []string{"0m", "0h", "0d", ".0", "0.0001"}},
		{name: "sleep bad among good", cmd: sleep, args: []string{"0", "1x"}, stderr: "sleep: invalid time interval '1x'\n", code: 2},
		{name: "sleep negative", cmd: sleep, args: []string{"--", "-1"}, stderr: "sleep: invalid time interval '-1'\n", code: 2},
		{name: "sleep negative as an option", cmd: sleep, args: []string{"-1"}, stderr: "sleep: invalid option -- '1'\n", code: 2},
		{name: "sleep nan", cmd: sleep, args: []string{"nan"}, stderr: "sleep: invalid time interval 'nan'\n", code: 2},
		{name: "sleep empty", cmd: sleep, args: []string{""}, stderr: "sleep: invalid time interval ''\n", code: 2},
		{name: "sleep missing", cmd: sleep, stderr: "sleep: missing operand\n", code: 2},

		{name: "tee stdout", cmd: tee, stdin: "data\n", stdout: "data\n"},
		{name: "tee files", cmd: tee, stdin: "piped\n", args: []string{"t1", "t2"}, stdout: "piped\n",
			want: map[string]string{"t1": "piped\n", "t2": "piped\n"}},
		{name: "tee truncates", cmd: tee, seed: map[string]string{"t": "old\n"}, stdin: "new\n", args: []string{"t"}, stdout: "new\n",
			want: map[string]string{"t": "new\n"}},
		{name: "tee -a", cmd: tee, seed: map[string]string{"t": "piped\n"}, stdin: "more\n", args: []string{"-a", "t"}, stdout: "more\n",
			want: map[string]string{"t": "piped\nmore\n"}},
		{name: "tee -a creates", cmd: tee, stdin: "x", args: []string{"-a", "t"}, stdout: "x", want: map[string]string{"t": "x"}},
		{name: "tee -i", cmd: tee, stdin: "x", args: []string{"-i", "t"}, stdout: "x", want: map[string]string{"t": "x"}},
		{name: "tee empty stdin", cmd: tee, seed: map[string]string{"t": "old"}, args: []string{"t"}, want: map[string]string{"t": ""}},
		{name: "tee same file twice", cmd: tee, stdin: "x", args: []string{"t", "t"}, stdout: "x", want: map[string]string{"t": "x"}},
		{name: "tee -a same file twice", cmd: tee, stdin: "x", args: []string{"-a", "t", "t"}, stdout: "x", want: map[string]string{"t": "xx"}},
		{name: "tee relative to dir", cmd: tee, dir: "dir", stdin: "x", args: []string{"t", "../u"}, stdout: "x",
			want: map[string]string{"dir/t": "x", "u": "x"}},
		{name: "tee binary", cmd: tee, stdin: "\x00\xff", args: []string{"t"}, stdout: "\x00\xff", want: map[string]string{"t": "\x00\xff"}},
		{name: "tee keeps going past a directory", cmd: tee, stdin: "x", args: []string{"dir", "t"}, stdout: "x",
			stderr: "tee: dir: Is a directory\n", code: 1, want: map[string]string{"t": "x", "dir/c.txt": files["dir/c.txt"]}},
		{name: "tee keeps going past a missing dir", cmd: tee, stdin: "x", args: []string{"no/x", "t"}, stdout: "x",
			stderr: "tee: no/x: No such file or directory\n", code: 1, want: map[string]string{"t": "x"}, absent: []string{"no"}},
		{name: "tee bad option", cmd: tee, args: []string{"-p"}, stderr: "tee: invalid option -- 'p'\n", code: 2},
	})
}

type envVars map[string]expand.Variable

func (e envVars) Get(name string) expand.Variable { return e[name] }

func (e envVars) Each(fn func(name string, vr expand.Variable) bool) {
	for name, vr := range e {
		if !fn(name, vr) {
			return
		}
	}
}

func TestEnvFiltersAndSorts(t *testing.T) {
	t.Parallel()
	exported := func(s string) expand.Variable {
		return expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: s}
	}
	tests := []struct {
		name string
		env  envVars
		want string
	}{
		{name: "empty", env: envVars{}, want: ""},
		{name: "sorted by name", env: envVars{"b": exported("2"), "a": exported("1"), "B": exported("3")}, want: "B=3\na=1\nb=2\n"},
		{name: "unexported", env: envVars{"a": exported("1"), "local": {Set: true, Kind: expand.String, Str: "x"}}, want: "a=1\n"},
		{name: "arrays", env: envVars{"a": exported("1"), "arr": {Set: true, Exported: true, Kind: expand.Indexed, List: []string{"x"}}}, want: "a=1\n"},
		{name: "empty value", env: envVars{"E": exported("")}, want: "E=\n"},
		{name: "value with = and newline", env: envVars{"V": exported("a=b\nc")}, want: "V=a=b\nc\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errb bytes.Buffer
			inv := &vsh.Invocation{Name: "env", Env: tc.env, Stdout: &out, Stderr: &errb}
			assert.NoError(t, coreutils.Env.Run(context.Background(), inv))
			assert.Equal(t, tc.want, out.String())
			assert.Empty(t, errb.String())
		})
	}
}

func TestParseSleep(t *testing.T) {
	t.Parallel()
	const forever = time.Duration(math.MaxInt64)
	tests := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{in: "0", want: 0},
		{in: "1", want: time.Second},
		{in: "1.5", want: 1500 * time.Millisecond},
		{in: ".5", want: 500 * time.Millisecond},
		{in: "1e3", want: 1000 * time.Second},
		{in: "2s", want: 2 * time.Second},
		{in: "2m", want: 2 * time.Minute},
		{in: "0.5m", want: 30 * time.Second},
		{in: "3h", want: 3 * time.Hour},
		{in: "1d", want: 24 * time.Hour},
		{in: "0.25d", want: 6 * time.Hour},
		{in: "+1", want: time.Second},
		{in: "inf", want: forever},
		{in: "infinity", want: forever},
		{in: "infd", want: forever},
		{in: "1e300", want: forever},
		{in: "1e400", want: forever},
		{in: "9223372037", want: forever},
		{in: "106752d", want: forever},
		{in: "9223372036", want: 9223372036 * time.Second},
		{in: "", err: true},
		{in: "s", err: true},
		{in: "x", err: true},
		{in: "1x", err: true},
		{in: "1ms", err: true},
		{in: "1 s", err: true},
		{in: "-1", err: true},
		{in: "-0.5s", err: true},
		{in: "-inf", err: true},
		{in: "nan", err: true},
		{in: "1e-400", want: 0},
	}
	for _, tc := range tests {
		got, err := coreutils.ParseSleep(tc.in)
		assert.Equal(t, tc.err, err != nil, "error for %q: %v", tc.in, err)
		assert.Equal(t, tc.want, got, "duration of %q", tc.in)
	}
}

func TestSleepDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
		// err is what sleep returns within the deadline: nil when it
		// is done sleeping, DeadlineExceeded when it still sleeps.
		err error
	}{
		{name: "short", args: []string{"0.01"}},
		{name: "short sum", args: []string{"0.005", "0.005s"}},
		{name: "long", args: []string{"60"}, err: context.DeadlineExceeded},
		{name: "a day", args: []string{"1d"}, err: context.DeadlineExceeded},
		{name: "inf", args: []string{"inf"}, err: context.DeadlineExceeded},
		{name: "beyond a Duration", args: []string{"1e300"}, err: context.DeadlineExceeded},
		{name: "sum beyond a Duration", args: []string{"106751d", "106751d"}, err: context.DeadlineExceeded},
		{name: "inf and more", args: []string{"inf", "1"}, err: context.DeadlineExceeded},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{})
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			var out bytes.Buffer
			inv := invocation(f.Scheme, coreutils.Sleep, "/", strings.NewReader(""), &out, &out, tc.args...)
			assert.ErrorIs(t, coreutils.Sleep.Run(ctx, inv), tc.err)
			assert.Empty(t, out.String())
		})
	}
}

func TestSleepStopsWithContext(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go debug.CapturePanicReport(func() {
		done <- coreutils.Sleep.Run(ctx, &vsh.Invocation{
			Name: "sleep", Args: []string{"60"}, Dir: "/", FS: f.Scheme,
			Env: expand.ListEnviron(), Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		})
	})
	cancel()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("sleep did not stop")
	}
}

func TestTeeStopsWithContext(t *testing.T) {
	t.Parallel()
	runCancelCases(t, []cancelCase{
		{name: "stdout", cmd: coreutils.Tee},
		{name: "file", cmd: coreutils.Tee, args: []string{"t"}},
	})
}

func TestCommandsRegistry(t *testing.T) {
	t.Parallel()
	commands := coreutils.Commands()
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.Name
		assert.NotEmpty(t, c.Name)
		assert.NotNil(t, c.Run, c.Name)
		assert.True(t, c.Synopsis == c.Name || strings.HasPrefix(c.Synopsis, c.Name+" "), "%s synopsis %q starts with its name", c.Name, c.Synopsis)
	}
	assert.True(t, slices.IsSorted(names), "sorted: %v", names)
	assert.Equal(t, len(names), len(slices.Compact(slices.Clone(names))), "unique: %v", names)
	assert.Equal(t, []string{
		"basename", "cat", "clear", "cp", "dirname", "env", "grep", "head", "ls", "mkdir", "mv",
		"rm", "rmdir", "sleep", "tail", "tee", "touch", "wc",
	}, names)
}
