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
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/coreutils"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

func TestAbsPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		dir, p, want string
	}{
		{dir: "/", p: "a", want: "/a"},
		{dir: "/d", p: "a/b", want: "/d/a/b"},
		{dir: "/d", p: "/abs", want: "/abs"},
		{dir: "/d", p: "/abs/../x/./y/", want: "/x/y"},
		{dir: "/d/e", p: "..", want: "/d"},
		{dir: "/d", p: "../../..", want: "/"},
		{dir: "/d", p: ".", want: "/d"},
		{dir: "/d", p: "", want: "/d"},
		{dir: "/d/", p: "a//b", want: "/d/a/b"},
		{dir: "/d", p: "~", want: "/d/~"},
	}
	for _, tc := range tests {
		t.Run(tc.dir+" "+tc.p, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, vsh.AbsPath(tc.dir, tc.p))
			assert.Equal(t, tc.want, (&vsh.Invocation{Dir: tc.dir}).Abs(tc.p))
		})
	}
}

func TestInvocationOpen(t *testing.T) {
	t.Parallel()
	const (
		rd     = os.O_RDONLY
		wr     = os.O_WRONLY
		create = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
		apnd   = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	)
	tests := []struct {
		name string
		// dir is the working directory, relative to the home.
		dir  string
		path string
		flag int
		// stdin is the invocation's standard input; noStreams leaves
		// it without any standard stream.
		stdin     string
		noStreams bool
		// write is written, then the file is read to its end when
		// read is set.
		write    string
		writeErr error
		read     bool
		want     string
		readErr  error

		openErr        error
		stdout, stderr string
		after          map[string]string
		absent         []string
	}{
		{name: "/dev/stdin reads stdin", path: "/dev/stdin", stdin: "in\n", read: true, want: "in\n"},
		{name: "/dev/stdin without stdin", path: "/dev/stdin", noStreams: true, read: true, readErr: syscall.EBADF},
		{name: "/dev/stdin cannot be written", path: "/dev/stdin", flag: wr, write: "x", writeErr: syscall.EBADF},
		{name: "/dev/stdout writes stdout", path: "/dev/stdout", flag: create, write: "out", stdout: "out"},
		{name: "/dev/stdout appends to stdout", path: "/dev/stdout", flag: apnd, write: "out", stdout: "out"},
		{name: "/dev/stdout cannot be read", path: "/dev/stdout", read: true, readErr: syscall.EBADF},
		{name: "/dev/stdout without stdout", path: "/dev/stdout", flag: wr, noStreams: true, write: "x", writeErr: syscall.EBADF},
		{name: "/dev/stderr writes stderr", path: "/dev/stderr", flag: wr, write: "err", stderr: "err"},
		{name: "/dev/null reads nothing", path: "/dev/null", read: true},
		{name: "/dev/null swallows writes", path: "/dev/null", flag: create, write: "gone"},
		{name: "an unclean device path", path: "/dev/./../dev//stdout", flag: wr, write: "out", stdout: "out"},
		{name: "/dev/tty without a terminal", path: "/dev/tty", flag: os.O_RDWR,
			openErr: &os.PathError{Op: "open", Path: "/dev/tty", Err: syscall.ENXIO}},

		{name: "a file", path: "a.txt", read: true, want: "alpha\n"},
		{name: "a file relative to the directory", dir: "d", path: "../a.txt", read: true, want: "alpha\n"},
		{name: "an absolute file", dir: "d", path: "{home}/a.txt", read: true, want: "alpha\n"},
		{name: "an empty file", path: "d/empty", read: true},
		{name: "a file opened for reading cannot be written", path: "a.txt", write: "x", writeErr: syscall.EBADF},
		{name: "a missing file", path: "missing",
			openErr: &os.PathError{Op: "open", Path: "missing", Err: syscall.ENOENT}},
		{name: "a directory cannot be read", path: "d",
			openErr: &os.PathError{Op: "open", Path: "d", Err: syscall.EISDIR}},
		{name: "a directory cannot be written", path: "d/", flag: create,
			openErr: &os.PathError{Op: "open", Path: "d/", Err: syscall.EISDIR}},
		{name: "creating a file", dir: "d", path: "new", flag: create, write: "x\n", after: map[string]string{"d/new": "x\n"}},
		{name: "creating an empty file", path: "new", flag: create, after: map[string]string{"new": ""}},
		{name: "truncating a file", path: "a.txt", flag: create, write: "b", after: map[string]string{"a.txt": "b"}},
		{name: "appending to a file", path: "a.txt", flag: apnd, write: "beta\n", after: map[string]string{"a.txt": "alpha\nbeta\n"}},
		{name: "appending creates a file", path: "new", flag: apnd, write: "x", after: map[string]string{"new": "x"}},
		{name: "overwriting the start of a file", path: "a.txt", flag: wr, write: "AL", after: map[string]string{"a.txt": "ALpha\n"}},
		{name: "a file opened for writing cannot be read", path: "a.txt", flag: wr, read: true, readErr: syscall.EBADF},
		{name: "reading and writing a file", path: "a.txt", flag: os.O_RDWR, write: "AL", read: true, want: "pha\n",
			after: map[string]string{"a.txt": "ALpha\n"}},
		{name: "an exclusive create of an existing file", path: "a.txt", flag: create | os.O_EXCL,
			openErr: &os.PathError{Op: "open", Path: "a.txt", Err: syscall.EEXIST}},
		{name: "an exclusive create", path: "new", flag: create | os.O_EXCL, write: "x", after: map[string]string{"new": "x"}},
		{name: "writing a missing file without creating it", path: "missing", flag: wr,
			openErr: &os.PathError{Op: "open", Path: "missing", Err: syscall.ENOENT}, absent: []string{"missing"}},
		{name: "creating in a missing directory", path: "no/new", flag: create,
			openErr: &os.PathError{Op: "open", Path: "no/new", Err: syscall.ENOENT}, absent: []string{"no"}},
		{name: "creating below a file", path: "a.txt/new", flag: create,
			openErr: &os.PathError{Op: "open", Path: "a.txt/new", Err: syscall.ENOTDIR}, after: map[string]string{"a.txt": "alpha\n"}},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{"a.txt": "alpha\n", "d/empty": ""}})
					var stdout, stderr bytes.Buffer
					inv := &vsh.Invocation{Name: "t", Dir: f.Path(tc.dir), FS: f.Scheme,
						Stdin: strings.NewReader(tc.stdin), Stdout: &stdout, Stderr: &stderr}
					if tc.noStreams {
						inv.Stdin, inv.Stdout, inv.Stderr = nil, nil, nil
					}
					file, err := inv.Open(strings.ReplaceAll(tc.path, "{home}", f.Home), tc.flag, 0o644)
					if tc.openErr != nil {
						assert.Equal(t, tc.openErr, err)
					} else {
						require.NoError(t, err)
						if tc.write != "" {
							_, err := file.Write([]byte(tc.write))
							assert.ErrorIs(t, err, tc.writeErr)
						}
						if tc.read {
							got, err := io.ReadAll(file)
							assert.ErrorIs(t, err, tc.readErr)
							assert.Equal(t, tc.want, string(got))
						}
						require.NoError(t, file.Close())
					}
					assert.Equal(t, tc.stdout, stdout.String(), "stdout")
					assert.Equal(t, tc.stderr, stderr.String(), "stderr")
					for name, content := range tc.after {
						assert.Equal(t, content, f.ReadFile(t, name), name)
					}
					for _, name := range tc.absent {
						_, err := f.Scheme.Lstat(f.Path(name))
						assert.ErrorIs(t, err, fs.ErrNotExist, name)
					}
				})
			}
		})
	}
}

// fileOp acts on the handle h opened by an offsets case: it writes
// write, or reads n bytes and expects want.
type fileOp struct {
	h     int
	write string
	n     int
	want  string
}

func TestInvocationOpenOffsets(t *testing.T) {
	t.Parallel()
	const content = "0123456789"
	tests := []struct {
		name  string
		flags []int
		ops   []fileOp
		after string
	}{
		{name: "two readers", flags: []int{os.O_RDONLY, os.O_RDONLY},
			ops:   []fileOp{{h: 0, n: 3, want: "012"}, {h: 1, n: 3, want: "012"}, {h: 0, n: 3, want: "345"}, {h: 1, n: 2, want: "34"}},
			after: content},
		{name: "two writers", flags: []int{os.O_WRONLY, os.O_WRONLY},
			ops:   []fileOp{{h: 0, write: "aaaa"}, {h: 1, write: "b"}, {h: 0, write: "c"}},
			after: "baaac56789"},
		{name: "two appenders", flags: []int{os.O_WRONLY | os.O_APPEND, os.O_WRONLY | os.O_APPEND},
			ops:   []fileOp{{h: 0, write: "a"}, {h: 1, write: "b"}, {h: 0, write: "c"}},
			after: content + "abc"},
		{name: "a writer and an appender", flags: []int{os.O_WRONLY, os.O_WRONLY | os.O_APPEND},
			ops:   []fileOp{{h: 1, write: "a"}, {h: 0, write: "b"}, {h: 1, write: "c"}},
			after: "b123456789ac"},
		{name: "a reader sees a writer's data", flags: []int{os.O_RDONLY, os.O_WRONLY | os.O_APPEND},
			ops:   []fileOp{{h: 0, n: 10, want: content}, {h: 1, write: "X"}, {h: 0, n: 1, want: "X"}},
			after: content + "X"},
		{name: "a read-write handle shares its offset", flags: []int{os.O_RDWR},
			ops:   []fileOp{{h: 0, n: 2, want: "01"}, {h: 0, write: "ab"}, {h: 0, n: 2, want: "45"}},
			after: "01ab456789"},
		{name: "a read-write handle and a reader", flags: []int{os.O_RDWR, os.O_RDONLY},
			ops:   []fileOp{{h: 0, write: "ab"}, {h: 1, n: 3, want: "ab2"}, {h: 0, n: 1, want: "2"}},
			after: "ab23456789"},
	}
	for _, backend := range vshtest.Backends {
		t.Run(backend.Name, func(t *testing.T) {
			t.Parallel()
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					f := vshtest.New(t, vshtest.Options{Backend: backend, Files: map[string]string{"f": content}})
					inv := &vsh.Invocation{Name: "t", Dir: f.Home, FS: f.Scheme}
					var handles []io.ReadWriteCloser
					for _, flag := range tc.flags {
						h, err := inv.Open("f", flag, 0o644)
						require.NoError(t, err)
						handles = append(handles, h)
					}
					for i, op := range tc.ops {
						h := handles[op.h]
						if op.write != "" {
							n, err := h.Write([]byte(op.write))
							require.NoError(t, err, "op %d", i)
							assert.Equal(t, len(op.write), n, "op %d", i)
							continue
						}
						buf := make([]byte, op.n)
						n, err := io.ReadFull(h, buf)
						require.NoError(t, err, "op %d", i)
						assert.Equal(t, op.want, string(buf[:n]), "op %d", i)
					}
					for _, h := range handles {
						require.NoError(t, h.Close())
					}
					assert.Equal(t, tc.after, f.ReadFile(t, "f"))
				})
			}
		})
	}
}

func TestTerm(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		cols, rows int
		// stdinPty attaches standard input to the pty too, which makes
		// it the process's controlling terminal.
		stdinPty bool
		ttyErr   error
	}{
		{name: "a controlling terminal", cols: 80, rows: 24, stdinPty: true},
		{name: "a wide terminal", cols: 132, rows: 50, stdinPty: true},
		{name: "a tiny terminal", cols: 1, rows: 1, stdinPty: true},
		{name: "output on a terminal that does not control", cols: 100, rows: 30, ttyErr: syscall.ENXIO},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			type report struct {
				cols, rows int
				ok         bool
				ttyErr     error
			}
			reports := make(chan report, 1)
			f := vshtest.New(t, vshtest.Options{Commands: []vsh.Command{{Name: "probe", Run: func(_ context.Context, inv *vsh.Invocation) error {
				var r report
				if inv.Term != nil {
					r.cols, r.rows = inv.Term.Size()
					r.ok = true
				}
				tty, err := inv.Open("/dev/tty", os.O_WRONLY, 0)
				r.ttyErr = err
				if err == nil {
					_, err = io.WriteString(tty, "via tty")
					_ = tty.Close()
				}
				reports <- r
				return err
			}}}})
			pt, err := f.Exec.NewPty(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() { _ = pt.Master.Close() })
			require.NoError(t, f.Exec.SetPtySize(pt, workspaceapi.PtySize{Columns: tc.cols, Rows: tc.rows}))

			var mu sync.Mutex
			var out strings.Builder
			go debug.CapturePanicReport(func() {
				buf := make([]byte, 256)
				for {
					n, err := pt.Master.Read(buf)
					mu.Lock()
					out.Write(buf[:n])
					mu.Unlock()
					if err != nil {
						return
					}
				}
			})
			cmd := workspaceapi.Cmd{Path: "probe", Stdout: pt.Slave, Stderr: pt.Slave}
			if tc.stdinPty {
				cmd.Stdin = pt.Slave
			}
			_, err = f.Exec.StartCommand(context.Background(), cmd)
			require.NoError(t, err)

			var r report
			select {
			case r = <-reports:
			case <-time.After(10 * time.Second):
				t.Fatal("the command did not run")
			}
			assert.True(t, r.ok, "stdout is the terminal")
			assert.Equal(t, [2]int{tc.cols, tc.rows}, [2]int{r.cols, r.rows})
			if tc.ttyErr != nil {
				assert.ErrorIs(t, r.ttyErr, tc.ttyErr)
				return
			}
			require.NoError(t, r.ttyErr)
			assert.Eventually(t, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return strings.Contains(out.String(), "via tty")
			}, 10*time.Second, time.Millisecond, "what /dev/tty writes reaches the terminal")
		})
	}
}

func TestTermFollowsResizes(t *testing.T) {
	t.Parallel()
	sizes := make(chan string, 1)
	f := vshtest.New(t, vshtest.Options{Commands: []vsh.Command{{Name: "size", Run: func(_ context.Context, inv *vsh.Invocation) error {
		cols, rows := inv.Term.Size()
		sizes <- fmt.Sprintf("%dx%d", cols, rows)
		return nil
	}}}})
	pt, err := f.Exec.NewPty(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pt.Master.Close() })
	go debug.CapturePanicReport(func() { _, _ = io.Copy(io.Discard, pt.Master) })
	for _, size := range []workspaceapi.PtySize{{Columns: 10, Rows: 5}, {Columns: 200, Rows: 60}, {Columns: 10, Rows: 5}} {
		require.NoError(t, f.Exec.SetPtySize(pt, size))
		_, err := f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{Path: "size", Stdin: pt.Slave, Stdout: pt.Slave})
		require.NoError(t, err)
		select {
		case got := <-sizes:
			assert.Equal(t, fmt.Sprintf("%dx%d", size.Columns, size.Rows), got)
		case <-time.After(10 * time.Second):
			t.Fatal("the command did not run")
		}
	}
}

// texts is console output: one string component per text.
func texts(ts ...string) iterator.Iterator[component.Responsive] {
	items := make([]component.Responsive, len(ts))
	for i, s := range ts {
		items[i] = component.NewResponsiveString(s, component.StringResponsiveConfig{})
	}
	return iterator.FromSlice(items)
}

// replHandler is a console command handler whose output is out.
func replHandler(out func(cmd repl.Command) (iterator.Iterator[component.Responsive], error),
	cands ...string,
) repl.CommandHandler {
	return repl.FuncCommandHandler(
		func(_ context.Context, cmd repl.Command, _ repl.ProgressWriter) (iterator.Iterator[component.Responsive], error) {
			return out(cmd)
		},
		func(_ context.Context, _ string, args []string) (iterator.Iterator[string], error) {
			var matched []string
			for _, c := range cands {
				if strings.HasPrefix(c, args[len(args)-1]) {
					matched = append(matched, c)
				}
			}
			return iterator.FromSlice(matched), nil
		},
	)
}

// echoCommand writes the name and arguments it is asked for.
func echoCommand(cmd repl.Command) (iterator.Iterator[component.Responsive], error) {
	return texts(strings.Join(append([]string{cmd.Name}, cmd.Args...), " ")), nil
}

// failingOutput is console output that fails with err once Iterator
// ends.
type failingOutput struct {
	iterator.Iterator[component.Responsive]
	err error
}

func (f *failingOutput) Err() error { return f.err }

func TestREPLCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		out            func(cmd repl.Command) (iterator.Iterator[component.Responsive], error)
		script         string
		stdout, stderr string
		code           int
	}{
		{name: "the output is drawn as text", out: echoCommand, script: "hello a 'b c'", stdout: "hello a b c\n"},
		{name: "the handler is asked for its own name whatever path runs it", out: echoCommand,
			script: "{bin}/hello x", stdout: "hello x\n"},
		{name: "each component is drawn on lines of its own",
			out: func(repl.Command) (iterator.Iterator[component.Responsive], error) {
				return texts("one", "two\nthree"), nil
			},
			script: "hello", stdout: "one\ntwo\nthree\n"},
		{name: "blanks ending lines and the output are dropped",
			out: func(repl.Command) (iterator.Iterator[component.Responsive], error) {
				return texts("a   \nb \n\n\n"), nil
			},
			script: "hello", stdout: "a\nb\n"},
		{name: "a component drawing nothing writes nothing",
			out:    func(repl.Command) (iterator.Iterator[component.Responsive], error) { return texts("", "x"), nil },
			script: "hello", stdout: "x\n"},
		{name: "no output",
			out:    func(repl.Command) (iterator.Iterator[component.Responsive], error) { return texts(), nil },
			script: "hello"},
		{name: "off a terminal the output is 80 columns wide",
			out: func(repl.Command) (iterator.Iterator[component.Responsive], error) {
				return texts(strings.Repeat("x", 100)), nil
			},
			script: "hello", stdout: strings.Repeat("x", 80) + "\n" + strings.Repeat("x", 20) + "\n"},
		{name: "the output pipes", out: echoCommand, script: "hello a | wc -c", stdout: "8\n"},
		{name: "the output redirects", out: echoCommand, script: "hello a > f; cat f", stdout: "hello a\n"},
		{name: "a command the handler does not know is not found",
			out:    func(repl.Command) (iterator.Iterator[component.Responsive], error) { return nil, repl.ErrNotFound },
			script: "hello", stderr: "hello: command not found\n", code: 127},
		{name: "a failing command prints its error",
			out:    func(repl.Command) (iterator.Iterator[component.Responsive], error) { return nil, errors.New("boom") },
			script: "hello", stderr: "hello: boom\n", code: 1},
		{name: "an exit error ending the output sets the status",
			out: func(repl.Command) (iterator.Iterator[component.Responsive], error) {
				return iterator.Error[component.Responsive](&repl.ExitError{Code: 3}), nil
			},
			script: "hello; echo $?", stdout: "3\n"},
		{name: "the output before an error is written",
			out: func(repl.Command) (iterator.Iterator[component.Responsive], error) {
				return &failingOutput{Iterator: texts("partial"), err: errors.New("broken")}, nil
			},
			script: "hello", stdout: "partial\n", stderr: "hello: broken\n", code: 1},
		{name: "help shows the synopsis", out: echoCommand, script: `help | grep -c '^  hello  *hello \[WHO\]$'`,
			stdout: "1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Commands: append(coreutils.Commands(),
				vsh.REPLCommand("hello", "hello [WHO]", replHandler(tc.out)))})
			script := strings.ReplaceAll(tc.script, "{bin}", f.Bin)
			stdout, stderr, exit := run(t, f, workspaceapi.Cmd{Path: "sh", Args: []string{"-c", script}})
			assert.Equal(t, tc.stdout, stdout)
			assert.Equal(t, tc.stderr, stderr)
			assert.Equal(t, tc.code, exitCode(t, exit))
		})
	}
}

func TestREPLCommandDrawsAtTheTerminalsWidth(t *testing.T) {
	t.Parallel()
	wide := func(repl.Command) (iterator.Iterator[component.Responsive], error) {
		return texts(strings.Repeat("x", 25)), nil
	}
	s := startShell(t, vshtest.Options{Commands: []vsh.Command{vsh.REPLCommand("wide", "wide", replHandler(wide))}})
	require.NoError(t, s.f.Exec.SetPtySize(s.pty, workspaceapi.PtySize{Columns: 20, Rows: 10}))
	_, at := s.waitPrompt(0)
	out, _ := s.runLine(at, "wide")
	assert.Equal(t, markC+strings.Repeat("x", 20)+"\r\n"+strings.Repeat("x", 5)+"\r\n"+markD0+prompt, out)
}

func TestREPLCommandCompletesWithTheHandler(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, line, want string
		list             []string
	}{
		{name: "the handler's candidate", line: "hello wo", want: "hello world "},
		{name: "the handler's candidates are listed", line: "hello w", want: "hello w", list: []string{"world", "wide"}},
		{name: "paths when the handler has none", line: "hello con", want: "hello config.md "},
		{name: "the name completes as any command's", line: "hell", want: "hello "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, vshtest.Options{Files: map[string]string{"config.md": ""},
				Commands: []vsh.Command{vsh.REPLCommand("hello", "", replHandler(echoCommand, "world", "wide"))}})
			line, _, list := vsh.CompleteLine(f.Exec.NewCompleter(f.Home, ""), tc.line, len(tc.line))
			assert.Equal(t, tc.want, line)
			assert.Equal(t, tc.list, list)
		})
	}
}
