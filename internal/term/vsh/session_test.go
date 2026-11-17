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
	"context"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/term/vsh"
	"unstable.build/rune/internal/term/vsh/vshtest"
)

const (
	markA  = "\x1b]133;A\x1b\\"
	markB  = "\x1b]133;B\x1b\\"
	markC  = "\x1b]133;C\x1b\\"
	markD0 = "\x1b]133;D;0\x1b\\"
	// prompt is what one idle prompt puts on the wire.
	prompt = markA + "$ " + markB
	// more is what one continuation prompt puts on the wire.
	more = markA + "> " + markB
)

func markD(status string) string { return "\x1b]133;D;" + status + "\x1b\\" }

// shell is a process on a pty, driven from the master side.
type shell struct {
	t    *testing.T
	f    *vshtest.Fixture
	pid  workspaceapi.Pid
	pty  workspaceapi.Pty
	exit chan error
	eof  chan struct{}

	mu  sync.Mutex
	out strings.Builder
	// changed is closed and replaced whenever out grows.
	changed chan struct{}
}

// startShell starts an interactive sh.
func startShell(t *testing.T, opts vshtest.Options) *shell {
	t.Helper()
	return startOnPty(t, context.Background(), vshtest.New(t, opts), "sh")
}

// startOnPty starts path with args on a new pty of f, the pty slave as
// its three standard streams.
func startOnPty(t *testing.T, ctx context.Context, f *vshtest.Fixture, path string, args ...string) *shell {
	t.Helper()
	pt, err := f.Exec.NewPty(context.Background())
	require.NoError(t, err)
	s := &shell{t: t, f: f, pty: pt, exit: make(chan error, 1), eof: make(chan struct{}), changed: make(chan struct{})}
	s.pid, err = f.Exec.StartCommand(ctx, workspaceapi.Cmd{
		Path:    path,
		Args:    args,
		Stdin:   pt.Slave,
		Stdout:  pt.Slave,
		Stderr:  pt.Slave,
		Watcher: workspaceapi.ChanProcessWatcher(s.exit),
	})
	require.NoError(t, err)
	// The parent's handle goes so that the master reads EOF when the
	// process exits, as the terminal emulator expects.
	require.NoError(t, pt.Slave.Close())
	t.Cleanup(func() { _ = pt.Master.Close() })
	go debug.CapturePanicReport(func() {
		buf := make([]byte, 4096)
		for {
			n, err := pt.Master.Read(buf)
			s.mu.Lock()
			s.out.Write(buf[:n])
			close(s.changed)
			s.changed = make(chan struct{})
			s.mu.Unlock()
			if err != nil {
				close(s.eof)
				return
			}
		}
	})
	return s
}

func (s *shell) typeKeys(keys string) {
	s.t.Helper()
	_, err := s.pty.Master.Write([]byte(keys))
	require.NoError(s.t, err)
}

func (s *shell) output() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.String()
}

// waitFor blocks until the output after offset from contains substr
// and returns that output up to and including substr.
func (s *shell) waitFor(from int, substr string) string {
	s.t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		s.mu.Lock()
		out, changed := s.out.String(), s.changed
		s.mu.Unlock()
		if len(out) >= from {
			if i := strings.Index(out[from:], substr); i >= 0 {
				return out[from : from+i+len(substr)]
			}
		}
		select {
		case <-changed:
		case <-timeout:
			s.t.Fatalf("output never contained %q after offset %d:\n%q", substr, from, out)
		}
	}
}

// waitPrompt waits for the next idle prompt after offset from and
// returns the output in between and the offset past the prompt.
func (s *shell) waitPrompt(from int) (string, int) {
	s.t.Helper()
	tail := s.waitFor(from, prompt)
	return tail, from + len(tail)
}

// runLine types line at an idle prompt and returns what the shell
// wrote up to the next prompt, after the echo of the line.
func (s *shell) runLine(from int, line string) (string, int) {
	s.t.Helper()
	s.typeKeys(line + "\r")
	echo := s.waitFor(from, line+"\r\n")
	return s.waitPrompt(from + len(echo))
}

// waitExit waits for the process to exit and the master to read EOF.
func (s *shell) waitExit() error {
	s.t.Helper()
	var err error
	select {
	case err = <-s.exit:
	case <-time.After(10 * time.Second):
		s.t.Fatalf("the process did not exit:\n%q", s.output())
	}
	select {
	case <-s.eof:
	case <-time.After(10 * time.Second):
		s.t.Fatalf("the master did not read EOF:\n%q", s.output())
	}
	return err
}

// exitStatus is how a process exited, as its watcher reports it.
type exitStatus struct {
	code int
	sig  syscall.Signal
}

func statusOf(t *testing.T, err error) exitStatus {
	t.Helper()
	if err == nil {
		return exitStatus{}
	}
	var exit *vsh.ExitError
	require.ErrorAs(t, err, &exit)
	return exitStatus{code: exit.ExitCode(), sig: exit.Signal}
}

func signalled(sig syscall.Signal) exitStatus { return exitStatus{code: -1, sig: sig} }

// action is something done to a running shell.
type action func(s *shell)

func keys(k string) action { return func(s *shell) { s.typeKeys(k) } }

func signal(sig syscall.Signal) action {
	return func(s *shell) { require.NoError(s.t, s.f.Exec.Signal(s.pid, sig)) }
}

func resize(cols, rows int) action {
	return func(s *shell) {
		require.NoError(s.t, s.f.Exec.SetPtySize(s.pty, workspaceapi.PtySize{Columns: cols, Rows: rows}))
	}
}

func hangup(s *shell) { require.NoError(s.t, s.pty.Master.Close()) }

// sessionStep does something and expects the output that follows,
// exactly, up to the end of want.
type sessionStep struct {
	do   action
	want string
}

// ran is the step of typing line at an idle prompt and it running with
// output out and status.
func ran(line, out, status string) sessionStep {
	return sessionStep{do: keys(line + "\r"), want: line + "\r\n" + markC + out + markD(status) + prompt}
}

var start = sessionStep{do: keys(""), want: prompt}

func TestSessionTranscripts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		opts  vshtest.Options
		steps []sessionStep
		exit  exitStatus
	}{
		{name: "no banner unless configured",
			steps: []sessionStep{{do: keys(""), want: prompt}, {do: keys("\x04"), want: "exit\r\n"}}},
		{name: "a banner",
			opts:  vshtest.Options{Banner: "Welcome to vsh"},
			steps: []sessionStep{{do: keys(""), want: "Welcome to vsh\r\n" + prompt}, {do: keys("\x04"), want: "exit\r\n"}}},
		{name: "a multi-line banner",
			opts:  vshtest.Options{Banner: "one\ntwo"},
			steps: []sessionStep{{do: keys(""), want: "one\r\ntwo\r\n" + prompt}, {do: keys("\x04"), want: "exit\r\n"}}},
		{name: "a banner ending in a newline leaves a blank line",
			opts:  vshtest.Options{Banner: "hi\n"},
			steps: []sessionStep{{do: keys(""), want: "hi\r\n\r\n" + prompt}, {do: keys("\x04"), want: "exit\r\n"}}},

		{name: "one command is marked",
			steps: []sessionStep{start, ran("echo hi", "hi\r\n", "0"), {do: keys("\x04"), want: "exit\r\n"}}},
		{name: "lines that run nothing have no command marks",
			steps: []sessionStep{start,
				{do: keys("\r"), want: "\r\n" + prompt},
				{do: keys("   \r"), want: "   \r\n" + prompt},
				{do: keys("# a comment\r"), want: "# a comment\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "statements on one line share one mark pair with the last status",
			steps: []sessionStep{start,
				ran("echo a; echo b", "a\r\nb\r\n", "0"),
				ran("true; false", "", "1"),
				ran("false; true", "", "0"),
				ran("false && echo no", "", "1"),
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 1}},
		{name: "the status reaches $? and the end mark",
			steps: []sessionStep{start,
				ran("nope", "sh: nope: command not found\r\n", "127"),
				ran("echo $?", "127\r\n", "0"),
				ran("(exit 42)", "", "42"),
				ran("echo $?", "42\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "a function persists across lines",
			steps: []sessionStep{start,
				ran("f() { echo f$1; return 3; }", "", "0"),
				ran("f x", "fx\r\n", "3"),
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 3}},
		{name: "Tab completes a function defined on an earlier line",
			steps: []sessionStep{start,
				ran("greet() { echo hi; }", "", "0"),
				{do: keys("gree\t"), want: "gree\x1b[4Dgreet "},
				{do: keys("\r"), want: "\r\n" + markC + "hi\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "Tab completes from the PATH the shell has",
			steps: []sessionStep{start,
				ran("PATH=/nowhere", "", "0"),
				{do: keys("ca\t\r"), want: "ca\r\n" + markC + "sh: ca: command not found\r\n" + markD("127") + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 127}},
		{name: "Tab lists on a continuation line and prompts it again",
			opts: vshtest.Options{Files: map[string]string{"a1": "", "a2": ""}},
			steps: []sessionStep{start,
				{do: keys("for f in 1; do\r"), want: "for f in 1; do\r\n" + more},
				{do: keys("cat a\t"), want: "cat a\r\na1  a2\r\n" + more + "cat a"},
				{do: keys("1\r"), want: "1\r\n" + more},
				{do: keys("done\r"), want: "done\r\n" + markC + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "^C after a listing drops the line",
			steps: []sessionStep{start,
				{do: keys("e\t"), want: "e\r\necho    env     eval    exec    exit    export\r\n" + prompt + "e"},
				{do: keys("\x03"), want: "^C\r\n" + prompt},
				ran("echo $?", "130\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "a listing follows the terminal's width",
			steps: []sessionStep{start,
				{do: resize(20, 10), want: "\x1b[2D\x1b[K$ "},
				{do: keys("e\t"), want: "e\r\necho    exec\r\nenv     exit\r\neval    export\r\n" + prompt + "e"},
				{do: keys("\x03"), want: "^C\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 130}},
		{name: "a console command completes with its handler",
			opts: vshtest.Options{Commands: []vsh.Command{vsh.REPLCommand("hello", "", replHandler(echoCommand, "world"))}},
			steps: []sessionStep{start,
				{do: keys("hello wo\t"), want: "hello wo\x1b[8Dhello world "},
				{do: keys("\r"), want: "\r\n" + markC + "hello world\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},

		{name: "a multi-line command",
			steps: []sessionStep{start,
				{do: keys("if true; then\r"), want: "if true; then\r\n" + more},
				{do: keys("echo yes\r"), want: "echo yes\r\n" + more},
				{do: keys("fi\r"), want: "fi\r\n" + markC + "yes\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "a quoted string across lines",
			steps: []sessionStep{start,
				{do: keys("echo \"a\r"), want: "echo \"a\r\n" + more},
				{do: keys("b\"\r"), want: "b\"\r\n" + markC + "a\r\nb\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "a here-document",
			steps: []sessionStep{start,
				{do: keys("cat <<EOF\r"), want: "cat <<EOF\r\n" + more},
				{do: keys("hi $HOME\r"), want: "hi $HOME\r\n" + more},
				{do: keys("EOF\r"), want: "EOF\r\n" + markC + "hi /\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "the continuation prompt ignores PS2",
			opts: vshtest.Options{Env: []string{"PS2=cont "}},
			steps: []sessionStep{start,
				{do: keys("echo a &&\r"), want: "echo a &&\r\n" + more},
				{do: keys("echo b\r"), want: "echo b\r\n" + markC + "a\r\nb\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},

		{name: "a syntax error is status 2 without command marks",
			steps: []sessionStep{start,
				{do: keys("done\r"), want: "done\r\nsh: 1:1: `done` can only be used to end a loop\r\n" + prompt},
				ran("echo $?", "2\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "a syntax error on a continuation line drops the whole command",
			steps: []sessionStep{start,
				{do: keys("if true; then\r"), want: "if true; then\r\n" + more},
				{do: keys("done\r"), want: "done\r\nsh: 2:1: `done` can only be used to end a loop\r\n" + prompt},
				ran("echo $?", "2\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "background jobs are rejected anywhere in a line",
			steps: []sessionStep{start,
				{do: keys("sleep 1 &\r"), want: "sleep 1 &\r\nsh: 1:1: a background job is not supported\r\n" + prompt},
				{do: keys("(sleep 1 &)\r"), want: "(sleep 1 &)\r\nsh: 1:2: a background job is not supported\r\n" + prompt},
				{do: keys("echo a; sleep 1 &\r"), want: "echo a; sleep 1 &\r\nsh: 1:9: a background job is not supported\r\n" + prompt},
				ran("echo $?", "2\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "process substitution is rejected",
			steps: []sessionStep{start,
				{do: keys("cat <(echo x)\r"), want: "cat <(echo x)\r\nsh: 1:5: process substitution is not supported\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 2}},

		{name: "exit with a status",
			steps: []sessionStep{start, {do: keys("exit 3\r"), want: "exit 3\r\n" + markC + markD("3")}},
			exit:  exitStatus{code: 3}},
		{name: "exit keeps the last status",
			steps: []sessionStep{start, ran("false", "", "1"), {do: keys("exit\r"), want: "exit\r\n" + markC + markD("1")}},
			exit:  exitStatus{code: 1}},
		{name: "exit 0 after a failure",
			steps: []sessionStep{start, ran("false", "", "1"), {do: keys("exit 0\r"), want: "exit 0\r\n" + markC + markD0}}},
		{name: "^D keeps the last status",
			steps: []sessionStep{start, ran("false", "", "1"), {do: keys("\x04"), want: "exit\r\n"}},
			exit:  exitStatus{code: 1}},
		{name: "exit stops the rest of the line",
			steps: []sessionStep{start, {do: keys("exit 2; echo no\r"), want: "exit 2; echo no\r\n" + markC + markD("2")}},
			exit:  exitStatus{code: 2}},
		{name: "exit in a function",
			steps: []sessionStep{start, {do: keys("f() { exit 5; }; f\r"), want: "f() { exit 5; }; f\r\n" + markC + markD("5")}},
			exit:  exitStatus{code: 5}},
		{name: "exit in a multi-line command",
			steps: []sessionStep{start,
				{do: keys("if true; then\r"), want: "if true; then\r\n" + more},
				{do: keys("exit 6\r"), want: "exit 6\r\n" + more},
				{do: keys("fi\r"), want: "fi\r\n" + markC + markD("6")},
			},
			exit: exitStatus{code: 6}},
		{name: "a subshell exit keeps the session",
			steps: []sessionStep{start, ran("(exit 4); echo still", "still\r\n", "0"), {do: keys("\x04"), want: "exit\r\n"}}},
		{name: "^D mid-line at the prompt does not exit",
			steps: []sessionStep{start,
				{do: keys("echo a\x04\r"), want: "echo a\r\n" + markC + "a\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "^D on an empty continuation line exits",
			steps: []sessionStep{start,
				{do: keys("for i in 1; do\r"), want: "for i in 1; do\r\n" + more},
				{do: keys("\x04"), want: "exit\r\n"},
			}},

		{name: "^C at the prompt drops the line",
			steps: []sessionStep{start,
				{do: keys("echo unfinished\x03"), want: "echo unfinished^C\r\n" + prompt},
				ran("echo $?", "130\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "SIGINT at the prompt drops the line",
			steps: []sessionStep{start,
				{do: keys("echo unfinished"), want: "echo unfinished"},
				{do: signal(syscall.SIGINT), want: "^C\r\n" + prompt},
				ran("echo $?", "130\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "^C on an empty prompt",
			steps: []sessionStep{start, {do: keys("\x03"), want: "^C\r\n" + prompt}, {do: keys("\x04"), want: "exit\r\n"}},
			exit:  exitStatus{code: 130}},
		{name: "^C during a continuation drops the whole command",
			steps: []sessionStep{start,
				{do: keys("for i in 1; do\r"), want: "for i in 1; do\r\n" + more},
				{do: keys("\x03"), want: "^C\r\n" + prompt},
				ran("echo $?", "130\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "SIGINT during a continuation drops the whole command",
			steps: []sessionStep{start,
				{do: keys("for i in 1; do\r"), want: "for i in 1; do\r\n" + more},
				{do: signal(syscall.SIGINT), want: "^C\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 130}},
		{name: "^C during a command echoes before the status",
			steps: []sessionStep{start,
				{do: keys("sleep 60\r"), want: "sleep 60\r\n" + markC},
				{do: keys("\x03"), want: "^C" + markD("130") + "\r\n" + prompt},
				ran("echo $?", "130\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "SIGINT during a command",
			steps: []sessionStep{start,
				// cat echoing a line proves it runs with the terminal
				// cooked, past the point where a SIGINT would reach the
				// prompt instead.
				{do: keys("cat\r"), want: "cat\r\n" + markC},
				{do: keys("x\r"), want: "x\r\nx\r\n"},
				{do: signal(syscall.SIGINT), want: markD("130") + prompt},
				ran("echo $?", "130\r\n", "0"),
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "^C during a command reading the terminal",
			steps: []sessionStep{start,
				{do: keys("cat\r"), want: "cat\r\n" + markC},
				{do: keys("\x03"), want: "^C" + markD("130") + "\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 130}},
		{name: "^C during read",
			steps: []sessionStep{start,
				{do: keys("read x\r"), want: "read x\r\n" + markC},
				{do: keys("\x03"), want: "^C" + markD("130") + "\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 130}},
		{name: "^C during a command discards the typeahead",
			steps: []sessionStep{start,
				{do: keys("sleep 60\r"), want: "sleep 60\r\n" + markC},
				{do: keys("echo lost\r\x03"), want: "echo lost\r\n^C" + markD("130") + "\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 130}},
		{name: "^C ends a loop, not the session",
			steps: []sessionStep{start,
				{do: keys("while true; do sleep 1; done\r"), want: "while true; do sleep 1; done\r\n" + markC},
				{do: keys("\x03"), want: "^C" + markD("130") + "\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 130}},

		{name: "a command reads cooked lines until EOF",
			steps: []sessionStep{start,
				{do: keys("cat\r"), want: "cat\r\n" + markC},
				{do: keys("hel\x7fllo\r"), want: "hel\b \bllo\r\nhello\r\n"},
				{do: keys("\x04"), want: markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "^D mid-line delivers the partial line to a command",
			steps: []sessionStep{start,
				{do: keys("cat\r"), want: "cat\r\n" + markC},
				{do: keys("abc\x04"), want: "abcabc"},
				{do: keys("\x04"), want: markD0 + "\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "read takes one line and the rest runs at the prompt",
			steps: []sessionStep{start,
				{do: keys("read x\r"), want: "read x\r\n" + markC},
				{do: keys("one\recho got $x\r"), want: "one\r\necho got $x\r\n" + markD0 + prompt +
					"echo got $x\r\n" + markC + "got one\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "lines typed during a command run next",
			steps: []sessionStep{start,
				{do: keys("sleep 0.2\r"), want: "sleep 0.2\r\n" + markC},
				{do: keys("echo after\r"), want: "echo after\r\n" + markD0 + prompt +
					"echo after\r\n" + markC + "after\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "lines typed at once run in order",
			steps: []sessionStep{start,
				{do: keys("echo 1\recho 2\recho 3\r"), want: "echo 1\r\n" + markC + "echo 2\r\necho 3\r\n" + "1\r\n" + markD0 + prompt +
					"echo 2\r\n" + markC + "echo 3\r\n" + "2\r\n" + markD0 + prompt +
					"echo 3\r\n" + markC + "3\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "output left mid-line starts the prompt on a new line",
			steps: []sessionStep{start,
				{do: keys("printf abc\r"), want: "printf abc\r\n" + markC + "abc" + markD0 + "\r\n" + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},

		{name: "PS1 set by a command",
			steps: []sessionStep{start,
				{do: keys("PS1='% '\r"), want: "PS1='% '\r\n" + markC + markD0 + markA + "% " + markB},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "PS1 follows the directory",
			opts: vshtest.Options{Files: map[string]string{"docs/a.md": "A\n"}, Env: []string{"PS1=$PWD> "}},
			steps: []sessionStep{
				{do: keys(""), want: markA + "/> " + markB},
				{do: keys("cd docs\r"), want: "cd docs\r\n" + markC + markD0 + markA + "/docs> " + markB},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "PS1 shows the last status",
			opts: vshtest.Options{Env: []string{"PS1=[$?] "}},
			steps: []sessionStep{
				{do: keys(""), want: markA + "[0] " + markB},
				{do: keys("false\r"), want: "false\r\n" + markC + markD("1") + markA + "[1] " + markB},
				{do: keys("\x03"), want: "^C\r\n" + markA + "[130] " + markB},
				{do: keys("echo )\r"), want: "echo )\r\nsh: 1:6: a command can only contain words and redirects; encountered `)`\r\n" + markA + "[2] " + markB},
				{do: keys("\x04"), want: "exit\r\n"},
			},
			exit: exitStatus{code: 2}},
		{name: "unsetting PS1 restores the default prompt",
			steps: []sessionStep{start,
				{do: keys("unset PS1\r"), want: "unset PS1\r\n" + markC + markD0 + markA + "memory:/// $ " + markB},
				{do: keys("\x04"), want: "exit\r\n"},
			}},

		{name: "the arrows walk the history",
			steps: []sessionStep{start,
				ran("echo first", "first\r\n", "0"),
				ran("echo second", "second\r\n", "0"),
				{do: keys("\x1b[A\x1b[A"), want: "echo second\x1b[11Decho first\x1b[K"},
				{do: keys("\r"), want: "\r\n" + markC + "first\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "BEL answers ^G after the redraw the keys before it caused",
			steps: []sessionStep{start,
				{do: keys("abc\x01\x07"), want: "abc\x1b[3D\x07"},
				{do: keys("\x07"), want: "\x07"},
				{do: keys("\x05\x15"), want: "\x1b[3C\x1b[3D\x1b[K"},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "a resize while editing redraws the line",
			steps: []sessionStep{start,
				{do: keys("echo"), want: "echo"},
				{do: resize(40, 10), want: "\x1b[6D\x1b[K$ echo"},
				{do: keys(" ok\r"), want: " ok\r\n" + markC + "ok\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},
		{name: "a bracketed paste runs like a typed line",
			steps: []sessionStep{start,
				{do: keys("\x1b[200~echo pasted\r\x1b[201~"), want: "echo pasted\r\n" + markC + "pasted\r\n" + markD0 + prompt},
				{do: keys("\x04"), want: "exit\r\n"},
			}},

		{name: "a hangup at the prompt",
			steps: []sessionStep{start, {do: hangup, want: ""}},
			exit:  signalled(syscall.SIGHUP)},
		{name: "a hangup during a command",
			steps: []sessionStep{start, {do: keys("sleep 60\r"), want: "sleep 60\r\n" + markC}, {do: hangup, want: ""}},
			exit:  signalled(syscall.SIGHUP)},
		{name: "a hangup ends a command writing output",
			steps: []sessionStep{start,
				{do: keys("while true; do echo spin; done\r"), want: "while true; do echo spin; done\r\n" + markC + "spin\r\n"},
				{do: hangup, want: ""},
			},
			exit: signalled(syscall.SIGHUP)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := startShell(t, tc.opts)
			at := 0
			for i, st := range tc.steps {
				st.do(s)
				got := s.waitFor(at, st.want)
				at += len(got)
				assert.Equal(t, st.want, got, "step %d", i)
			}
			assert.Equal(t, tc.exit, statusOf(t, s.waitExit()))
		})
	}
}

func TestSessionResizeLaysOutCommands(t *testing.T) {
	t.Parallel()
	s := startShell(t, vshtest.Options{})
	_, at := s.waitPrompt(0)
	require.NoError(t, s.f.Exec.SetPtySize(s.pty, workspaceapi.PtySize{Columns: 20, Rows: 5}))
	got := s.waitFor(at, "\x1b[2D\x1b[K$ ")
	at += len(got)
	assert.Equal(t, "\x1b[2D\x1b[K$ ", got, "the editor repaints the prompt for the new width")

	got, _ = s.runLine(at, "ls /bin")
	listing := strings.TrimSuffix(strings.TrimPrefix(got, markC), markD0+prompt)
	rows := strings.Split(strings.TrimSuffix(listing, "\r\n"), "\r\n")
	names := strings.Fields(listing)
	assert.Len(t, rows, (len(names)+1)/2, "two columns fit in 20 columns: %q", listing)
	for _, row := range rows {
		assert.LessOrEqual(t, len(row), 20, "ls lays out within the resized width: %q", row)
	}
}
