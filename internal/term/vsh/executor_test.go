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

package vsh_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

// The scripts, start errors, lookups and sessions every backend must
// run identically are in vshtest's suites; these cover the Executor's
// own contract.

// run starts cmd on the fixture, waits for it and returns what it
// wrote and how it exited.
func run(t *testing.T, f *vshtest.Fixture, cmd workspaceapi.Cmd) (stdout, stderr string, exit error) {
	t.Helper()
	var out, errb bytes.Buffer
	ch := make(chan error, 1)
	cmd.Stdout = &out
	cmd.Stderr = &errb
	cmd.Watcher = workspaceapi.ChanProcessWatcher(ch)
	pid, err := f.Exec.StartCommand(context.Background(), cmd)
	require.NoError(t, err)
	require.NotZero(t, pid)
	select {
	case exit = <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("command did not exit")
	}
	return out.String(), errb.String(), exit
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var exit *vsh.ExitError
	require.ErrorAs(t, err, &exit)
	return exit.ExitCode()
}

// newExecutor builds an Executor on cfg.FS, or on a new memory
// workspace when it is nil, and closes it with the test.
func newExecutor(t *testing.T, cfg vsh.Config) *vsh.Executor {
	t.Helper()
	if cfg.FS == nil {
		cfg.FS = vshtest.New(t, vshtest.Options{}).Scheme
	}
	e, err := vsh.NewExecutor(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

// seedExec writes a new executable file at name, relative to the
// fixture's home, creating its directories. The file must not exist:
// opening one keeps its mode.
func seedExec(t *testing.T, f *vshtest.Fixture, name, content string) {
	t.Helper()
	require.NoError(t, f.Scheme.MkdirAll(path.Dir(f.Path(name)), 0o755))
	file, err := f.Scheme.OpenFile(f.Path(name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	require.NoError(t, err)
	_, err = file.Write([]byte(content))
	require.NoError(t, err)
	require.NoError(t, file.Close())
}

func nopRun(context.Context, *vsh.Invocation) error { return nil }

func TestNewExecutor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		noFS      bool
		root, bin string
		commands  []vsh.Command
		err       string
		// home and shell are HOME and SHELL of the processes.
		home, shell string
	}{
		{name: "defaults", home: "/", shell: "/bin/sh"},
		{name: "an absolute root", root: "/sub", home: "/sub", shell: "/bin/sh"},
		{name: "a dot root", root: ".", home: "/", shell: "/bin/sh"},
		{name: "a dot-slash root", root: "./", home: "/", shell: "/bin/sh"},
		{name: "a relative root", root: "sub", home: "/sub", shell: "/bin/sh"},
		{name: "an unclean relative root", root: "sub/./", home: "/sub", shell: "/bin/sh"},
		{name: "an absolute bin", bin: "/tools", home: "/", shell: "/tools/sh"},
		{name: "a relative bin", bin: "tools", home: "/", shell: "/tools/sh"},
		{name: "an unclean bin", bin: "./x/../tools/", home: "/", shell: "/tools/sh"},
		{name: "a bin in the root", root: "/sub", bin: "/sub/bin", home: "/sub", shell: "/sub/bin/sh"},
		{name: "commands", commands: []vsh.Command{{Name: "x", Run: nopRun}}, home: "/", shell: "/bin/sh"},
		{name: "no filesystem", noFS: true, err: "vsh: Config.FS is required"},
		{name: "a command without a name", commands: []vsh.Command{{Run: nopRun}}, err: `vsh: command "" has no name or Run`},
		{name: "a command without Run", commands: []vsh.Command{{Name: "x"}}, err: `vsh: command "x" has no name or Run`},
		{name: "a command shadowing a shell", commands: []vsh.Command{{Name: "bash", Run: nopRun}}, err: `vsh: command "bash" shadows a shell`},
		{name: "a bin that is a file", bin: "/f", err: "install /f: "},
		{name: "a bin below a file", bin: "/f/bin", err: "install /f/bin: "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Files: map[string]string{"f": "", "sub/.keep": ""}})
			cfg := vsh.Config{FS: f.Scheme, Root: tc.root, Bin: tc.bin, Commands: tc.commands}
			if tc.noFS {
				cfg.FS = nil
			}
			e, err := vsh.NewExecutor(cfg)
			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { _ = e.Close() })
			env := e.BaseEnv(nil, "/")
			home, _ := vsh.EnvValue(env, "HOME")
			shell, _ := vsh.EnvValue(env, "SHELL")
			pathVar, _ := vsh.EnvValue(env, "PATH")
			assert.Equal(t, tc.home, home)
			assert.Equal(t, tc.shell, shell)
			assert.Equal(t, path.Dir(tc.shell), pathVar)
			info, err := f.Scheme.Stat(tc.shell)
			require.NoError(t, err, "the programs are installed in the bin directory")
			assert.Equal(t, fs.FileMode(0o755), info.Mode().Perm())
		})
	}
}

func TestExecutorRootIsAbsolute(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{
		"notes.md":     "hello\n",
		"sub/notes.md": "sub\n",
	}})
	// A workspace URI parsed from "memory://" has the path ".". The
	// shell must not pass the relative paths it would derive from it
	// to the filesystem, which reads "memory://notes.md" as a host.
	for _, tc := range []struct{ root, want, content string }{
		{root: ".", want: "/", content: "hello\n"},
		{root: "./", want: "/", content: "hello\n"},
		{root: "sub", want: "/sub", content: "sub\n"},
		{root: "sub/./", want: "/sub", content: "sub\n"},
	} {
		t.Run(tc.root, func(t *testing.T) {
			exec := newExecutor(t, vsh.Config{FS: f.Scheme, Root: tc.root, Commands: coreutils.Commands()})
			var out, errb bytes.Buffer
			ch := make(chan error, 1)
			_, err := exec.StartCommand(context.Background(), workspaceapi.Cmd{
				Path: "sh", Args: []string{"-c", "cat notes.md && pwd && echo ~"},
				Stdout: &out, Stderr: &errb, Watcher: workspaceapi.ChanProcessWatcher(ch),
			})
			require.NoError(t, err)
			require.NoError(t, <-ch, errb.String())
			assert.Equal(t, tc.content+tc.want+"\n"+tc.want+"\n", out.String())

			got, err := exec.LookPath(context.Background(), "./notes.md", "")
			require.NoError(t, err)
			assert.Equal(t, path.Join(tc.want, "notes.md"), got)
		})
	}
}

func TestStartCommandDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dir  string
		want string
		err  error
		msg  string
	}{
		{dir: "", want: "/home"},
		{dir: "~", want: "/home"},
		{dir: "~/sub", want: "/home/sub"},
		{dir: "sub", want: "/home/sub"},
		{dir: "sub/../sub/", want: "/home/sub"},
		{dir: "..", want: "/"},
		{dir: "/home/sub", want: "/home/sub"},
		{dir: "/", want: "/"},
		{dir: "missing", err: syscall.ENOENT, msg: "chdir missing: no such file or directory"},
		{dir: "~/missing", err: syscall.ENOENT, msg: "chdir ~/missing: no such file or directory"},
		{dir: "f", err: syscall.ENOTDIR, msg: "chdir f: not a directory"},
		{dir: "f/x", err: syscall.ENOENT, msg: "chdir f/x: no such file or directory"},
	}
	f := vshtest.New(t, vshtest.Options{Files: map[string]string{"home/f": "", "home/sub/.keep": ""}})
	e := newExecutor(t, vsh.Config{FS: f.Scheme, Root: "/home"})
	for _, tc := range tests {
		t.Run(tc.dir, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			ch := make(chan error, 1)
			_, err := e.StartCommand(context.Background(), workspaceapi.Cmd{
				Path: "sh", Args: []string{"-c", `echo "$PWD $(pwd)"`}, Dir: tc.dir,
				Stdout: &out, Watcher: workspaceapi.ChanProcessWatcher(ch),
			})
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
				assert.EqualError(t, err, tc.msg)
				return
			}
			require.NoError(t, err)
			require.NoError(t, <-ch)
			assert.Equal(t, tc.want+" "+tc.want+"\n", out.String())
		})
	}
}

func TestExecutorSharedOutputBuffer(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})
	var buf bytes.Buffer
	ch := make(chan error, 1)
	_, err := f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
		Path:    "sh",
		Args:    []string{"-c", "for i in 1 2 3; do echo out$i; echo err$i 1>&2; done"},
		Stdout:  &buf,
		Stderr:  &buf,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	})
	require.NoError(t, err)
	require.NoError(t, <-ch)
	assert.Equal(t, "out1\nerr1\nout2\nerr2\nout3\nerr3\n", buf.String())
}

func TestExecutorStdinReaderIsPumped(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})
	pr, pw := io.Pipe()
	go debug.CapturePanicReport(func() {
		_, _ = io.WriteString(pw, "one\ntwo\n")
		_ = pw.Close()
	})
	stdout, _, exit := run(t, f, workspaceapi.Cmd{Path: "sh", Args: []string{"-c", "while read l; do echo \"<$l>\"; done"}, Stdin: pr})
	assert.NoError(t, exit)
	assert.Equal(t, "<one>\n<two>\n", stdout)
}

func TestExecutorSignals(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})

	assert.ErrorIs(t, f.Exec.Signal(12345, syscall.SIGTERM), os.ErrProcessDone)

	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGKILL, syscall.SIGHUP, syscall.SIGQUIT} {
		t.Run(sig.String(), func(t *testing.T) {
			ch := make(chan error, 1)
			pid, err := f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
				Path: "sleep", Args: []string{"60"}, Watcher: workspaceapi.ChanProcessWatcher(ch),
			})
			require.NoError(t, err)
			require.NoError(t, f.Exec.Signal(pid, 0), "signal 0 probes a live process")
			require.NoError(t, f.Exec.Signal(pid, syscall.SIGUSR1), "other signals are ignored")
			require.NoError(t, f.Exec.Signal(pid, sig))
			select {
			case err := <-ch:
				var exit *vsh.ExitError
				require.ErrorAs(t, err, &exit)
				assert.Equal(t, sig, exit.Signal)
				assert.Equal(t, -1, exit.ExitCode())
				assert.Equal(t, "signal: "+sig.String(), exit.Error())
			case <-time.After(5 * time.Second):
				t.Fatal("the process did not exit")
			}
			assert.ErrorIs(t, f.Exec.Signal(pid, 0), os.ErrProcessDone)
		})
	}
}

func TestExecutorContextKills(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// early cancels the context before the command starts.
		early bool
	}{
		{name: "cancelled while running"},
		{name: "cancelled before starting", early: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{})
			ctx, cancel := context.WithCancel(context.Background())
			if tc.early {
				cancel()
			}
			ch := make(chan error, 1)
			_, err := f.Exec.StartCommand(ctx, workspaceapi.Cmd{
				Path: "sleep", Args: []string{"60"}, Watcher: workspaceapi.ChanProcessWatcher(ch),
			})
			require.NoError(t, err)
			cancel()
			select {
			case err := <-ch:
				var exit *vsh.ExitError
				require.ErrorAs(t, err, &exit)
				assert.Equal(t, syscall.SIGKILL, exit.Signal)
			case <-time.After(5 * time.Second):
				t.Fatal("the process did not exit")
			}
		})
	}
}

func TestExecutorClose(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})
	unread := make(chan error) // never read
	pid, err := f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "sleep", Args: []string{"60"}, Watcher: workspaceapi.ChanProcessWatcher(unread),
	})
	require.NoError(t, err)
	pt, err := f.Exec.NewPty(context.Background())
	require.NoError(t, err)

	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		_ = f.Exec.Close()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited on an unread watcher")
	}

	assert.NoError(t, f.Exec.Close(), "Close is idempotent")
	assert.ErrorIs(t, f.Exec.Signal(pid, 0), os.ErrProcessDone, "Close awaits the processes")
	_, err = f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{Path: "echo"})
	assert.ErrorIs(t, err, os.ErrClosed)
	_, err = f.Exec.NewPty(context.Background())
	assert.ErrorIs(t, err, os.ErrClosed)
	assert.ErrorIs(t, f.Exec.SetPtySize(pt, workspaceapi.PtySize{Columns: 1, Rows: 1}), vsh.ErrPtyClosed, "Close closes the ptys")
	assert.Nil(t, f.Exec.NewFile(pt.Master.Fd(), pt.Master.Name()))
	_, err = pt.Master.Read(make([]byte, 1))
	assert.Error(t, err)
}

func TestExecutorForegroundProcessGroupIsUnsupported(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, vshtest.Options{})
	pty, err := f.Exec.NewPty(context.Background())
	require.NoError(t, err)
	_, err = f.Exec.ForegroundProcessGroup(pty)
	assert.ErrorIs(t, err, errors.ErrUnsupported)
}

func TestSetPtySize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		// pty picks the pty to resize: the executor's own, one with its
		// master closed, one of another executor, or none.
		pty  string
		want error
	}{
		{name: "an own pty", pty: "own"},
		{name: "a pty whose master is closed", pty: "closed", want: vsh.ErrPtyClosed},
		{name: "a pty whose slave is closed", pty: "slave closed"},
		{name: "another executor's pty", pty: "foreign", want: vsh.ErrPtyClosed},
		{name: "the zero pty", pty: "zero", want: vsh.ErrPtyClosed},
		{name: "a slave as the master", pty: "swapped", want: vsh.ErrPtyClosed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{})
			other := newExecutor(t, vsh.Config{FS: f.Scheme})
			newPty := func(e *vsh.Executor) workspaceapi.Pty {
				pt, err := e.NewPty(context.Background())
				require.NoError(t, err)
				t.Cleanup(func() { _ = pt.Master.Close() })
				return pt
			}
			ptys := map[string]func() workspaceapi.Pty{
				"own": func() workspaceapi.Pty { return newPty(f.Exec) },
				"closed": func() workspaceapi.Pty {
					pt := newPty(f.Exec)
					require.NoError(t, pt.Master.Close())
					return pt
				},
				"slave closed": func() workspaceapi.Pty {
					pt := newPty(f.Exec)
					require.NoError(t, pt.Slave.Close())
					return pt
				},
				"foreign": func() workspaceapi.Pty { return newPty(other) },
				"zero":    func() workspaceapi.Pty { return workspaceapi.Pty{} },
				"swapped": func() workspaceapi.Pty {
					pt := newPty(f.Exec)
					return workspaceapi.Pty{Master: pt.Slave, Slave: pt.Master}
				},
			}
			err := f.Exec.SetPtySize(ptys[tc.pty](), workspaceapi.PtySize{Columns: 100, Rows: 40})
			assert.ErrorIs(t, err, tc.want)
		})
	}
}

func TestNewFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		fd, file string
		// want is the handle NewFile returns, empty for nil.
		want string
	}{
		{name: "the master", fd: "master", file: "master", want: "master"},
		{name: "the slave", fd: "slave", file: "slave", want: "slave"},
		{name: "the master's fd with the slave's name", fd: "master", file: "slave"},
		{name: "the slave's fd with the master's name", fd: "slave", file: "master"},
		{name: "an unknown fd", fd: "unknown", file: "master"},
		{name: "an unknown name", fd: "master", file: "unknown"},
	}
	f := vshtest.New(t, vshtest.Options{})
	pt, err := f.Exec.NewPty(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pt.Master.Close() })
	handles := map[string]workspaceapi.File{"master": pt.Master, "slave": pt.Slave}
	fds := map[string]uintptr{"master": pt.Master.Fd(), "slave": pt.Slave.Fd(), "unknown": 1 << 20}
	names := map[string]string{"master": pt.Master.Name(), "slave": pt.Slave.Name(), "unknown": "/dev/nope"}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := f.Exec.NewFile(fds[tc.fd], names[tc.file])
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			assert.Same(t, handles[tc.want], got, "the handle is the one NewPty returned")
		})
	}
}

func TestNewPtyAllocatesFds(t *testing.T) {
	t.Parallel()
	var next uintptr = 100
	f := vshtest.New(t, vshtest.Options{})
	e := newExecutor(t, vsh.Config{FS: f.Scheme, AllocFd: func() uintptr { next++; return next }})
	var fds []uintptr
	for range 2 {
		pt, err := e.NewPty(context.Background())
		require.NoError(t, err)
		t.Cleanup(func() { _ = pt.Master.Close() })
		fds = append(fds, pt.Master.Fd(), pt.Slave.Fd())
		assert.NotEqual(t, pt.Master.Name(), pt.Slave.Name())
		assert.True(t, strings.HasPrefix(pt.Slave.Name(), "/dev/"), pt.Slave.Name())
	}
	assert.Equal(t, []uintptr{101, 102, 103, 104}, fds)
}
