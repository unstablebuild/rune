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
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// probe is a command whose behaviour its first argument picks:
//
//	cat          writes "ready", then copies its input to its output
//	exit N       exits with status N
//	fail         fails with an error that is not an exit status
//	flood        writes far more than a terminal holds unread
//	streams N    writes N lines to its output and error streams at once
var probe = vsh.Command{Name: "probe", Run: func(ctx context.Context, inv *vsh.Invocation) error {
	switch inv.Args[0] {
	case "cat":
		if _, err := io.WriteString(inv.Stdout, "ready\n"); err != nil {
			return err
		}
		_, err := io.Copy(inv.Stdout, inv.Stdin)
		return err
	case "exit":
		code, err := strconv.Atoi(inv.Args[1])
		if err != nil {
			return err
		}
		return &vsh.ExitError{Code: code}
	case "fail":
		return errors.New("boom")
	case "flood":
		_, err := inv.Stdout.Write(make([]byte, 1<<20))
		return err
	case "streams":
		n, err := strconv.Atoi(inv.Args[1])
		if err != nil {
			return err
		}
		var wg sync.WaitGroup
		for _, w := range []struct {
			prefix string
			to     io.Writer
		}{{"o", inv.Stdout}, {"e", inv.Stderr}} {
			wg.Add(1)
			go debug.CapturePanicReport(func() {
				defer wg.Done()
				for i := range n {
					_, _ = fmt.Fprintf(w.to, "%s%d\n", w.prefix, i)
				}
			})
		}
		wg.Wait()
		return nil
	}
	return fmt.Errorf("unknown probe %q", inv.Args[0])
}}

func probeOptions() vshtest.Options {
	return vshtest.Options{Commands: append(coreutils.Commands(), probe)}
}

// lateSignal sends sig to a process that may have exited already.
func lateSignal(sig syscall.Signal) action {
	return func(s *shell) {
		if err := s.f.Exec.Signal(s.pid, sig); !errors.Is(err, os.ErrProcessDone) {
			require.NoError(s.t, err)
		}
	}
}

// cancelCtx ends the context the process was started with.
func cancelCtx(cancel context.CancelFunc) action { return func(*shell) { cancel() } }

func TestProcessSignals(t *testing.T) {
	t.Parallel()
	// steps take the cancel function of the row's own start context.
	type stepsFn func(cancel context.CancelFunc) []sessionStep
	steps := func(s ...sessionStep) stepsFn { return func(context.CancelFunc) []sessionStep { return s } }

	reading := sessionStep{do: keys(""), want: "ready\r\n"}
	atPrompt := sessionStep{do: keys(""), want: prompt}
	inCommand := []sessionStep{atPrompt,
		{do: keys("cat\r"), want: "cat\r\n" + markC},
		{do: keys("x\r"), want: "x\r\nx\r\n"},
	}
	tests := []struct {
		name  string
		args  []string
		steps stepsFn
		exit  exitStatus
	}{
		{name: "a reader ends on ^D with status 0", args: []string{"probe", "cat"},
			steps: steps(reading, sessionStep{do: keys("abc\r\x04"), want: "abc\r\nabc\r\n"})},
		{name: "SIGINT terminates a process that is not a session", args: []string{"probe", "cat"},
			steps: steps(reading, sessionStep{do: signal(syscall.SIGINT)}), exit: signalled(syscall.SIGINT)},
		{name: "SIGHUP terminates", args: []string{"probe", "cat"},
			steps: steps(reading, sessionStep{do: signal(syscall.SIGHUP)}), exit: signalled(syscall.SIGHUP)},
		{name: "SIGTERM terminates", args: []string{"probe", "cat"},
			steps: steps(reading, sessionStep{do: signal(syscall.SIGTERM)}), exit: signalled(syscall.SIGTERM)},
		{name: "SIGQUIT terminates", args: []string{"probe", "cat"},
			steps: steps(reading, sessionStep{do: signal(syscall.SIGQUIT)}), exit: signalled(syscall.SIGQUIT)},
		{name: "SIGKILL terminates", args: []string{"probe", "cat"},
			steps: steps(reading, sessionStep{do: signal(syscall.SIGKILL)}), exit: signalled(syscall.SIGKILL)},
		{name: "other signals and the probe are ignored", args: []string{"probe", "cat"},
			steps: steps(reading,
				sessionStep{do: signal(0)},
				sessionStep{do: signal(syscall.SIGUSR1)},
				sessionStep{do: signal(syscall.SIGWINCH)},
				sessionStep{do: signal(syscall.SIGSTOP)},
				sessionStep{do: signal(syscall.SIGCONT)},
				sessionStep{do: signal(syscall.SIGPIPE)},
				sessionStep{do: keys("still\r\x04"), want: "still\r\nstill\r\n"},
			)},
		{name: "the first terminating signal is the one reported", args: []string{"probe", "cat"},
			steps: steps(reading, sessionStep{do: signal(syscall.SIGTERM)}, sessionStep{do: lateSignal(syscall.SIGKILL)}),
			exit:  signalled(syscall.SIGTERM)},
		{name: "a hangup terminates with SIGHUP", args: []string{"probe", "cat"},
			steps: steps(reading, sessionStep{do: hangup}), exit: signalled(syscall.SIGHUP)},
		{name: "the start context ending is SIGKILL", args: []string{"probe", "cat"},
			steps: func(cancel context.CancelFunc) []sessionStep {
				return []sessionStep{reading, {do: cancelCtx(cancel)}}
			}, exit: signalled(syscall.SIGKILL)},
		{name: "a command's exit status", args: []string{"probe", "exit", "7"}, steps: steps(), exit: exitStatus{code: 7}},
		{name: "a script blocked on the terminal ends on SIGINT", args: []string{"sh", "-c", "echo ready; read x"},
			steps: steps(reading, sessionStep{do: signal(syscall.SIGINT)}), exit: signalled(syscall.SIGINT)},

		{name: "SIGINT at a session's prompt interrupts the line", args: []string{"sh"},
			steps: steps(atPrompt,
				sessionStep{do: signal(syscall.SIGINT), want: "^C\r\n" + prompt},
				sessionStep{do: keys("\x04"), want: "exit\r\n"},
			), exit: exitStatus{code: 130}},
		{name: "SIGTERM at a session's prompt", args: []string{"sh"},
			steps: steps(atPrompt, sessionStep{do: signal(syscall.SIGTERM)}), exit: signalled(syscall.SIGTERM)},
		{name: "SIGHUP at a session's prompt", args: []string{"sh"},
			steps: steps(atPrompt, sessionStep{do: signal(syscall.SIGHUP)}), exit: signalled(syscall.SIGHUP)},
		{name: "SIGQUIT at a session's prompt", args: []string{"sh"},
			steps: steps(atPrompt, sessionStep{do: signal(syscall.SIGQUIT)}), exit: signalled(syscall.SIGQUIT)},
		{name: "SIGKILL at a session's prompt", args: []string{"sh"},
			steps: steps(atPrompt, sessionStep{do: signal(syscall.SIGKILL)}), exit: signalled(syscall.SIGKILL)},
		{name: "the start context ending at a session's prompt", args: []string{"sh"},
			steps: func(cancel context.CancelFunc) []sessionStep {
				return []sessionStep{atPrompt, {do: cancelCtx(cancel)}}
			}, exit: signalled(syscall.SIGKILL)},
		{name: "ignored signals leave a session running", args: []string{"sh"},
			steps: steps(atPrompt,
				sessionStep{do: signal(0)},
				sessionStep{do: signal(syscall.SIGUSR1)},
				sessionStep{do: keys("exit 4\r"), want: "exit 4\r\n" + markC + markD("4")},
			), exit: exitStatus{code: 4}},
		{name: "SIGINT during a session's command interrupts only the command", args: []string{"sh"},
			steps: steps(append(inCommand,
				sessionStep{do: signal(syscall.SIGINT), want: markD("130") + prompt},
				sessionStep{do: keys("exit\r"), want: "exit\r\n" + markC + markD("130")},
			)...), exit: exitStatus{code: 130}},
		{name: "SIGTERM during a session's command", args: []string{"sh"},
			steps: steps(append(inCommand, sessionStep{do: signal(syscall.SIGTERM)})...), exit: signalled(syscall.SIGTERM)},
		{name: "the start context ending during a session's command", args: []string{"sh"},
			steps: func(cancel context.CancelFunc) []sessionStep {
				return append(inCommand, sessionStep{do: cancelCtx(cancel)})
			}, exit: signalled(syscall.SIGKILL)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			s := startOnPty(t, ctx, vshtest.New(t, probeOptions()), tc.args[0], tc.args[1:]...)
			at := 0
			for i, st := range tc.steps(cancel) {
				st.do(s)
				got := s.waitFor(at, st.want)
				at += len(got)
				assert.Equal(t, st.want, got, "step %d", i)
			}
			assert.Equal(t, tc.exit, statusOf(t, s.waitExit()))
			assert.ErrorIs(t, s.f.Exec.Signal(s.pid, 0), os.ErrProcessDone, "an exited process is gone before its watcher hears of it")
		})
	}
}

// countingWatcher counts the calls to WatchProcess.
type countingWatcher struct {
	ch     chan error
	calls  atomic.Int32
	called chan struct{}
	once   sync.Once
}

func (w *countingWatcher) WatchProcess() chan error {
	w.calls.Add(1)
	w.once.Do(func() { close(w.called) })
	return w.ch
}

func TestProcessReportsItsExitOnce(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ch   chan error
		// reports is how many exits the channel carries.
		reports int
	}{
		{name: "a watcher without a channel"},
		{name: "a buffered channel", ch: make(chan error, 2), reports: 1},
		{name: "an unbuffered channel read after the exit", ch: make(chan error), reports: 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, probeOptions())
			pt, err := f.Exec.NewPty(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() { _ = pt.Master.Close() })
			w := &countingWatcher{ch: tc.ch, called: make(chan struct{})}
			_, err = f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
				Path: "probe", Args: []string{"exit", "3"}, Stdout: pt.Slave, Stdin: pt.Slave, Watcher: w,
			})
			require.NoError(t, err)
			require.NoError(t, pt.Slave.Close())
			_, err = io.ReadAll(pt.Master)
			require.NoError(t, err, "the master reads EOF once the process is gone")

			select {
			case <-w.called:
			case <-time.After(5 * time.Second):
				t.Fatal("the watcher was never asked for its channel")
			}
			var reports []exitStatus
			for range tc.reports {
				select {
				case err := <-tc.ch:
					reports = append(reports, statusOf(t, err))
				case <-time.After(5 * time.Second):
					t.Fatal("the exit was not reported")
				}
			}
			assertBlocked(t, tc.ch)
			assert.Equal(t, int32(1), w.calls.Load())
			var want []exitStatus
			for range tc.reports {
				want = append(want, exitStatus{code: 3})
			}
			assert.Equal(t, want, reports)
		})
	}
}

func TestProcessWithoutWatcherReleasesItsTerminal(t *testing.T) {
	t.Parallel()
	f := vshtest.New(t, probeOptions())
	pt, err := f.Exec.NewPty(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = pt.Master.Close() })
	_, err = f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "sh", Args: []string{"-c", "echo done"}, Stdin: pt.Slave, Stdout: pt.Slave,
	})
	require.NoError(t, err)
	require.NoError(t, pt.Slave.Close())
	out, err := io.ReadAll(pt.Master)
	require.NoError(t, err)
	assert.Equal(t, "done\r\n", string(out))
}

// assertBlocked checks that nothing arrives on ch for a while. A
// value that is never sent has no observable signal, so this waits
// for a short time.
func assertBlocked[T any](t *testing.T, ch <-chan T) {
	t.Helper()
	select {
	case v, ok := <-ch:
		if ok {
			t.Fatalf("expected nothing, got %v", v)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func TestProcessBlockedOutputEndsWithTheProcess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		end  func(f *vshtest.Fixture, pid workspaceapi.Pid, cancel context.CancelFunc)
		exit exitStatus
	}{
		{name: "SIGINT", end: func(f *vshtest.Fixture, pid workspaceapi.Pid, _ context.CancelFunc) {
			require.NoError(t, f.Exec.Signal(pid, syscall.SIGINT))
		}, exit: signalled(syscall.SIGINT)},
		{name: "SIGTERM", end: func(f *vshtest.Fixture, pid workspaceapi.Pid, _ context.CancelFunc) {
			require.NoError(t, f.Exec.Signal(pid, syscall.SIGTERM))
		}, exit: signalled(syscall.SIGTERM)},
		{name: "the start context", end: func(_ *vshtest.Fixture, _ workspaceapi.Pid, cancel context.CancelFunc) {
			cancel()
		}, exit: signalled(syscall.SIGKILL)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, probeOptions())
			pt, err := f.Exec.NewPty(context.Background())
			require.NoError(t, err)
			t.Cleanup(func() { _ = pt.Master.Close() })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			exit := make(chan error, 1)
			pid, err := f.Exec.StartCommand(ctx, workspaceapi.Cmd{
				Path: "probe", Args: []string{"flood"}, Stdout: pt.Slave, Stderr: pt.Slave,
				Watcher: workspaceapi.ChanProcessWatcher(exit),
			})
			require.NoError(t, err)
			// The first output shows the write started; nothing reads
			// the rest, so it soon waits on the terminal.
			_, err = pt.Master.Read(make([]byte, 1))
			require.NoError(t, err)
			tc.end(f, pid, cancel)
			select {
			case err := <-exit:
				assert.Equal(t, tc.exit, statusOf(t, err))
			case <-time.After(5 * time.Second):
				t.Fatal("a process blocked on its terminal did not end")
			}
		})
	}
}

// exclusiveWriter fails a test when two writes overlap. Its buffer is
// unguarded so that the race detector also sees an overlap.
type exclusiveWriter struct {
	busy     atomic.Bool
	overlaps atomic.Int32
	buf      bytes.Buffer
}

func (w *exclusiveWriter) Write(b []byte) (int, error) {
	if !w.busy.CompareAndSwap(false, true) {
		w.overlaps.Add(1)
		return w.buf.Write(b)
	}
	defer w.busy.Store(false)
	// Yielding while busy gives a concurrent writer the chance to
	// overlap.
	time.Sleep(time.Microsecond)
	return w.buf.Write(b)
}

func TestProcessSharedWriterSerializesStreams(t *testing.T) {
	t.Parallel()
	const n = 200
	tests := []struct {
		name string
		args []string
	}{
		{name: "a command writing both streams at once", args: []string{"probe", "streams", strconv.Itoa(n)}},
		{name: "pipeline stages writing different streams", args: []string{"sh", "-c",
			"i=0; while [ $i -lt 200 ]; do echo e$i >&2; i=$((i+1)); done | { i=0; while [ $i -lt 200 ]; do echo o$i; i=$((i+1)); done; }"}},
	}
	word := regexp.MustCompile(`[oe]\d+`)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, probeOptions())
			w := &exclusiveWriter{}
			exit := make(chan error, 1)
			_, err := f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
				Path: tc.args[0], Args: tc.args[1:], Stdout: w, Stderr: w,
				Watcher: workspaceapi.ChanProcessWatcher(exit),
			})
			require.NoError(t, err)
			select {
			case err := <-exit:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the command did not exit")
			}
			assert.Zero(t, w.overlaps.Load(), "writes to a shared writer overlapped")
			// The interpreter's echo writes a word and its newline
			// separately, so lines of two streams may interleave; the
			// words themselves never do.
			got := map[string]int{}
			for _, word := range word.FindAllString(w.buf.String(), -1) {
				got[word]++
			}
			want := map[string]int{}
			for i := range n {
				want["o"+strconv.Itoa(i)]++
				want["e"+strconv.Itoa(i)]++
			}
			assert.Equal(t, want, got)
			assert.Equal(t, 2*n, strings.Count(w.buf.String(), "\n"))
		})
	}
}

func TestProcessExitStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{name: "a command exiting 0", args: []string{"probe", "exit", "0"}},
		{name: "a command's status", args: []string{"probe", "exit", "3"}, code: 3},
		{name: "the highest status", args: []string{"probe", "exit", "255"}, code: 255},
		{name: "a command failing with an error", args: []string{"probe", "fail"}, code: 1, stderr: "probe: boom\n"},
		{name: "a script's exit", args: []string{"sh", "-c", "exit 3"}, code: 3},
		{name: "a script's last status", args: []string{"sh", "-c", "true; false"}, code: 1},
		{name: "a script ending well after a failure", args: []string{"sh", "-c", "false; true"}},
		{name: "a subshell's exit", args: []string{"sh", "-c", "(exit 4)"}, code: 4},
		{name: "a function's return", args: []string{"sh", "-c", "f() { return 5; }; f"}, code: 5},
		{name: "a pipeline's last stage", args: []string{"sh", "-c", "true | false"}, code: 1},
		{name: "a pipeline's earlier stage does not count", args: []string{"sh", "-c", "false | true"}},
		{name: "errexit stops at the failure", args: []string{"sh", "-c", "set -e; false; echo no"}, code: 1},
		{name: "a command's status through the shell", args: []string{"sh", "-c", "probe exit 9"}, code: 9},
		{name: "an unknown command", args: []string{"sh", "-c", "nope"}, code: 127, stderr: "sh: nope: command not found\n"},
		{name: "a negated failure", args: []string{"sh", "-c", "! false"}},
		{name: "output before the exit is kept", args: []string{"sh", "-c", "echo out; echo err >&2; exit 2"}, code: 2,
			stdout: "out\n", stderr: "err\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := vshtest.New(t, probeOptions())
			var stdout, stderr bytes.Buffer
			exit := make(chan error, 1)
			_, err := f.Exec.StartCommand(context.Background(), workspaceapi.Cmd{
				Path: tc.args[0], Args: tc.args[1:], Stdout: &stdout, Stderr: &stderr,
				Watcher: workspaceapi.ChanProcessWatcher(exit),
			})
			require.NoError(t, err)
			select {
			case err := <-exit:
				assert.Equal(t, exitStatus{code: tc.code}, statusOf(t, err))
			case <-time.After(10 * time.Second):
				t.Fatal("the command did not exit")
			}
			assert.Equal(t, tc.stdout, stdout.String())
			assert.Equal(t, tc.stderr, stderr.String())
		})
	}
}
