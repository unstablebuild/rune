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
	"errors"
	"io"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"mvdan.cc/sh/v3/expand"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

// invoke runs cmd with args in dir on scheme and returns its output and
// exit code.
func invoke(t *testing.T, scheme schemeapi.Scheme, cmd vsh.Command, dir, stdin string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errb bytes.Buffer
	err := cmd.Run(context.Background(), invocation(scheme, cmd, dir, strings.NewReader(stdin), &out, &errb, args...))
	if err != nil {
		exit, ok := errors.AsType[*vsh.ExitError](err)
		require.True(t, ok, "commands report failures as ExitError: %v", err)
		code = exit.Code
	}
	return out.String(), errb.String(), code
}

func invocation(fsys schemeapi.FileSystem, cmd vsh.Command, dir string, stdin io.Reader, stdout, stderr io.Writer, args ...string) *vsh.Invocation {
	return &vsh.Invocation{
		Name: cmd.Name, Args: args, Dir: dir, FS: fsys,
		Env:   expand.ListEnviron("PATH=/bin", "HOME=/", "X=1"),
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
	}
}

// lsDate matches the modification time column of ls -l, which the
// fixture's files get from the clock.
var lsDate = regexp.MustCompile(`[A-Z][a-z]{2} [ \d]\d \d\d:\d\d`)

var files = map[string]string{
	"a.txt":          "alpha\nbeta\ngamma\n",
	"b.txt":          "one two\nthree\n",
	"empty":          "",
	"dir/c.txt":      "Gamma ray\n",
	"dir/sub/d.txt":  "delta\n",
	"dir/.hidden":    "h\n",
	"nonl":           "no newline",
	"long.txt":       "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n",
	"bin/tool":       "#!/bin/sh\n",
	"words.txt":      "the cat\nThe dog\ncatalog\n",
	"deep/x/y/z.txt": "z\n",
}

// cmdCase is one run of a command on a fresh fixture holding files
// and seed, and what it must print, exit with and leave behind.
type cmdCase struct {
	name  string
	cmd   vsh.Command
	dir   string
	stdin string
	args  []string
	// seed adds files to, or replaces files of, the shared fixture,
	// and seedDirs adds empty directories.
	seed     map[string]string
	seedDirs []string
	stdout   string
	stderr   string
	code     int
	// want are files that must exist afterwards with these contents.
	want map[string]string
	// dirs must be directories afterwards and absent must not exist.
	dirs   []string
	absent []string
}

// runCases runs each case in parallel on its own fixture. The ls -l
// date column is replaced with DATE since it comes from the clock.
func runCases(t *testing.T, tests []cmdCase) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			seeded := maps.Clone(files)
			maps.Copy(seeded, tc.seed)
			f := vshtest.New(t, vshtest.Options{Files: seeded})
			for _, name := range tc.seedDirs {
				require.NoError(t, f.Scheme.MkdirAll(f.Path(name), 0o755))
			}
			dir := path.Join("/", tc.dir)
			stdout, stderr, code := invoke(t, f.Scheme, tc.cmd, dir, tc.stdin, tc.args...)
			assert.Equal(t, tc.stdout, lsDate.ReplaceAllString(stdout, "DATE"), "stdout")
			assert.Equal(t, tc.stderr, stderr, "stderr")
			assert.Equal(t, tc.code, code, "exit code")
			for name, content := range tc.want {
				assert.Equal(t, content, f.ReadFile(t, name), "contents of %s", name)
			}
			for _, name := range tc.dirs {
				info, err := f.Scheme.Stat(f.Path(name))
				if assert.NoError(t, err, "%s exists", name) {
					assert.True(t, info.IsDir(), "%s is a directory", name)
				}
			}
			for _, name := range tc.absent {
				_, err := f.Scheme.Stat(f.Path(name))
				assert.ErrorIs(t, err, fs.ErrNotExist, "%s is absent", name)
			}
		})
	}
}

// endless is a standard input that never ends: it cancels the
// command's context on the first read and keeps producing lines, so
// that only a command that watches its context returns.
type endless struct{ cancel context.CancelFunc }

func (e endless) Read(p []byte) (int, error) {
	e.cancel()
	for i := range p {
		p[i] = "y\n"[i%2]
	}
	return len(p), nil
}

// cancelCase is a command that must stop when its context ends while
// it reads an endless standard input.
type cancelCase struct {
	name string
	cmd  vsh.Command
	args []string
}

func runCancelCases(t *testing.T, tests []cancelCase) {
	t.Helper()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go debug.CapturePanicReport(func() {
				inv := invocation(f.Scheme, tc.cmd, "/", endless{cancel}, io.Discard, io.Discard, tc.args...)
				done <- tc.cmd.Run(ctx, inv)
			})
			select {
			case err := <-done:
				assert.ErrorIs(t, err, context.Canceled)
			case <-time.After(10 * time.Second):
				t.Fatal("the command did not stop with its context")
			}
		})
	}
}

// onTerminal runs the shell's ls in dir with args on a pty cols wide
// and returns what it printed.
func onTerminal(t *testing.T, f *vshtest.Fixture, cols int, dir string, args ...string) string {
	t.Helper()
	pty, err := f.Exec.NewPty(context.Background())
	require.NoError(t, err)
	require.NoError(t, f.Exec.SetPtySize(pty, workspaceapi.PtySize{Columns: cols, Rows: 10}))
	ch := make(chan error, 1)
	_, err = f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "ls", Dir: dir, Args: args, Stdin: pty.Slave, Stdout: pty.Slave, Stderr: pty.Slave,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	})
	require.NoError(t, err)
	require.NoError(t, pty.Slave.Close())
	out, err := io.ReadAll(pty.Master)
	require.NoError(t, err)
	require.NoError(t, <-ch)
	return string(out)
}
