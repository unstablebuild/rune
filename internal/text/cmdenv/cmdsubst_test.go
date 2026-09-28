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

package cmdenv

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func TestNewCommandSubstResolver(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		line    string
		want    string
		wantErr bool
	}{
		{
			name: "no substitution leaves line untouched",
			line: "echo foo",
			want: "echo foo",
		},
		{
			name: "single cmdsubst is replaced by captured stdout",
			line: "echo $(/bin/echo bar)",
			want: "echo bar",
		},
		{
			name: "multi-word output is spliced without quoting",
			line: "echo $(/bin/echo a b c)",
			want: "echo a b c",
		},
		{
			name: "trailing newlines are stripped (POSIX semantics)",
			line: "echo $(/bin/echo bar)",
			want: "echo bar",
		},
		{
			name: "nested cmdsubst expands inner first then outer",
			line: "echo $(/bin/echo nested $(/bin/echo inner))",
			want: "echo nested inner",
		},
		{
			name: "backtick form behaves like $(...)",
			line: "echo `/bin/echo hi`",
			want: "echo hi",
		},
		{
			name:    "non-zero exit propagates as error",
			line:    "echo $(/bin/false)",
			wantErr: true,
		},
		{
			name: "two adjacent cmdsubsts each resolved",
			line: "echo $(/bin/echo a)-$(/bin/echo b)",
			want: "echo a-b",
		},
		{
			name: "literal $VAR outside cmdsubst is left for the outer shell",
			line: "echo $FOO $(/bin/echo bar)",
			want: "echo $FOO bar",
		},
		{
			name: "empty $() body is a no-op",
			line: "echo $()",
			want: "echo ",
		},
		{
			name: "multi-statement body joined with ; runs each",
			// interp.Runner runs `/bin/echo a; /bin/echo b`; output
			// "a\nb\n" is trimmed to "a\nb" then spliced into the
			// outer line.
			line: "echo $(/bin/echo a; /bin/echo b)",
			want: "echo a\nb",
		},
		{
			name: "deep nesting at depth 5 resolves",
			line: "$(/bin/echo $(/bin/echo $(/bin/echo $(/bin/echo $(/bin/echo deep)))))",
			want: "deep",
		},
		{
			name: "cmdsubst inside double quotes is still resolved",
			line: `echo "x $(/bin/echo hi) y"`,
			want: `echo "x hi y"`,
		},
		{
			name: "arithmetic span is NOT cmdsubst",
			// $((1+1)) is a syntax.ArithmExp, not a CmdSubst, so
			// expandCommandSubst leaves it for the outer shell.
			line: "echo $((1+1))",
			want: "echo $((1+1))",
		},
		{
			name:    "parse error surfaces as error",
			line:    "echo $(",
			wantErr: true,
		},
		{
			name: "closing-paren attack: stdout fails reparse",
			// The inner /bin/echo "); rm -rf /" produces stdout
			// `); rm -rf /`. expandCommandSubst splices that into
			// the outer line as `echo ); rm -rf /` and reparses,
			// which fails — protecting against further shell
			// execution. We document the failure mode rather than
			// asserting a benign outcome.
			line:    `echo $(/bin/echo "); rm -rf /")`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			expander := NewCommandSubstResolver(
				&osExecutor{}, nil,
			)
			got, err := expander.ExpandCommand(context.Background(), tt.line)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestCommandSubstRecordedDispatches uses a recordingExecutor (no
// real fork/exec) so we can assert exactly what argv the resolver
// hands the executor for each $(...) span, including how $VAR is
// expanded inside the body by interp.Runner.
func TestCommandSubstRecordedDispatches(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		envVars  map[string]string
		stdout   string // canned stdout for every recorded call
		wantArgv [][]string
		wantLine string
	}{
		{
			name:     "single cmdsubst",
			line:     "echo $(mycmd a b)",
			stdout:   "result",
			wantArgv: [][]string{{"mycmd", "a", "b"}},
			wantLine: "echo result",
		},
		{
			name: "env var inside body expanded by interp",
			line: "echo $(mycmd $FOO)",
			envVars: map[string]string{
				"FOO": "from-env",
			},
			stdout:   "ok",
			wantArgv: [][]string{{"mycmd", "from-env"}},
			wantLine: "echo ok",
		},
		{
			name:   "nested resolves inner before outer",
			line:   "echo $(outer $(inner one))",
			stdout: "X",
			wantArgv: [][]string{
				{"inner", "one"},
				{"outer", "X"},
			},
			wantLine: "echo X",
		},
		{
			name:     "no cmdsubst makes no calls",
			line:     "echo plain $VAR",
			wantArgv: nil,
			wantLine: "echo plain $VAR",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingExecutor{
				stdout: []byte(tt.stdout),
			}
			envSrc := Source(func(name string) (string, bool) {
				v, ok := tt.envVars[name]
				return v, ok
			})
			r := NewCommandSubstResolver(rec, envSrc)
			got, err := r.ExpandCommand(context.Background(), tt.line)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLine, got)
			assert.Equal(t, tt.wantArgv, rec.argvSnapshot())
		})
	}
}

// TestCommandSubstRecursesIntoSubstitutedOutput documents the
// resolver's actual behavior: stdout containing `$(...)` IS re-
// parsed and the new spans are resolved on subsequent passes. The
// test bounds the recursion by having the executor return
// non-CmdSubst output on the second call so the loop terminates.
//
// This is a deliberate design choice: the resolver runs one pass
// of `Walk → resolve innermost → splice → reparse` per iteration so
// nested `$(...)` produced by a previous substitution is honored.
// Callers concerned about untrusted output triggering further shell
// execution must sanitize stdout before splicing.
func TestCommandSubstRecursesIntoSubstitutedOutput(t *testing.T) {
	rec := &recursiveExecutor{
		responses: []string{
			"$(second)", // first call returns a fresh CmdSubst
			"final",     // second call returns a literal
		},
	}
	r := NewCommandSubstResolver(rec, nil)
	got, err := r.ExpandCommand(context.Background(), "echo $(first)")
	require.NoError(t, err)
	assert.Equal(t, "echo final", got)
	assert.Equal(t, [][]string{{"first"}, {"second"}}, rec.argvSnapshot())
}

// TestCommandSubstNonUTF8OutputFailsReparse documents that the
// resolver re-parses the spliced line; if a substitution's stdout
// contains non-utf8 bytes the next pass surfaces a parse error.
// Callers that want to retain non-utf8 output must sanitize before
// the line reaches the resolver.
func TestCommandSubstNonUTF8OutputFailsReparse(t *testing.T) {
	rec := &recordingExecutor{
		stdout: []byte("a\xffb"),
	}
	r := NewCommandSubstResolver(rec, nil)
	_, err := r.ExpandCommand(context.Background(), "echo $(inner)")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid UTF-8")
}

// TestCommandSubstHandlesNULBytesInOutput verifies a NUL in stdout
// propagates into the result string.
func TestCommandSubstHandlesNULBytesInOutput(t *testing.T) {
	rec := &recordingExecutor{
		stdout: []byte("a\x00b"),
	}
	r := NewCommandSubstResolver(rec, nil)
	got, err := r.ExpandCommand(context.Background(), "echo $(inner)")
	require.NoError(t, err)
	assert.Equal(t, "echo a\x00b", got)
}

// TestCommandSubstStripsOnlyTrailingNewlines pins POSIX behavior:
// trailing \n is stripped, interior newlines preserved.
func TestCommandSubstStripsOnlyTrailingNewlines(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   string
	}{
		{"single trailing", "foo\n", "echo foo"},
		{"double trailing", "foo\n\n", "echo foo"},
		{"interior preserved", "a\nb\n", "echo a\nb"},
		{"no newline", "foo", "echo foo"},
		{"only newlines", "\n\n\n", "echo "},
		{"empty", "", "echo "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := &recordingExecutor{stdout: []byte(c.stdout)}
			r := NewCommandSubstResolver(rec, nil)
			got, err := r.ExpandCommand(context.Background(),
				"echo $(inner)")
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestCommandSubstContextCancellation asserts that cancelling ctx
// while an inner subst is in flight propagates an error from
// ExpandCommand.
func TestCommandSubstContextCancellation(t *testing.T) {
	rec := &recordingExecutor{
		blockUntilCancel: true,
	}
	r := NewCommandSubstResolver(rec, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := r.ExpandCommand(ctx, "echo $(slow)")
	require.Error(t, err)
}

// TestCommandSubstConcurrent runs ExpandCommand from many goroutines
// to catch data races. The resolver is documented as safe to share
// once configured.
func TestCommandSubstConcurrent(t *testing.T) {
	const goroutines = 16
	const iterations = 50
	rec := &recordingExecutor{stdout: []byte("X")}
	r := NewCommandSubstResolver(rec, nil)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				got, err := r.ExpandCommand(
					context.Background(), "echo $(inner)")
				if err != nil {
					t.Errorf("ExpandCommand: %v", err)
					return
				}
				if got != "echo X" {
					t.Errorf("got %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()
	// At minimum every goroutine ran iterations rounds.
	assert.GreaterOrEqual(t, rec.callCount(),
		int64(goroutines*iterations))
}

// recordingExecutor satisfies schemeapi.Executor without forking.
// It writes a canned stdout to whatever io.Writer the caller wired
// up, records the argv it observed, and optionally blocks until
// ctx is cancelled (for cancellation tests).
type recordingExecutor struct {
	mu               sync.Mutex
	argv             [][]string
	stdout           []byte
	startErr         error
	exitErr          error
	blockUntilCancel bool
	calls            atomic.Int64
}

func (e *recordingExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	e.calls.Add(1)
	e.mu.Lock()
	args := append([]string{cmd.Path}, cmd.Args...)
	e.argv = append(e.argv, args)
	e.mu.Unlock()
	if e.startErr != nil {
		return 0, e.startErr
	}
	if e.blockUntilCancel {
		<-ctx.Done()
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- ctx.Err()
		}
		return 0, ctx.Err()
	}
	if cmd.Stdout != nil && len(e.stdout) > 0 {
		_, _ = cmd.Stdout.Write(e.stdout)
	}
	if cmd.Watcher != nil {
		cmd.Watcher.WatchProcess() <- e.exitErr
	}
	if e.exitErr != nil {
		return 0, e.exitErr
	}
	return 1, nil
}

func (e *recordingExecutor) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}
func (e *recordingExecutor) Close() error { return nil }
func (e *recordingExecutor) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{}, errors.New("recordingExecutor: no pty")
}
func (e *recordingExecutor) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error {
	return nil
}

func (e *recordingExecutor) argvSnapshot() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.argv) == 0 {
		return nil
	}
	out := make([][]string, len(e.argv))
	for i, a := range e.argv {
		out[i] = append([]string(nil), a...)
	}
	return out
}

func (e *recordingExecutor) callCount() int64 {
	return e.calls.Load()
}

// silence unused-import warnings when individual tests are skipped.
var _ = strings.TrimSpace

// osExecutor runs the requested leaf command through os/exec on the
// host. Run is synchronous so interp.Runner can read the captured
// stdout buffer immediately.
type osExecutor struct{}

func (e *osExecutor) StartCommand(
	ctx context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Stdin = cmd.Stdin
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	runErr := c.Run()
	if cmd.Watcher != nil {
		cmd.Watcher.WatchProcess() <- runErr
	}
	if runErr != nil {
		return 0, runErr
	}
	if c.Process != nil {
		return workspaceapi.Pid(c.Process.Pid), nil
	}
	return 0, nil
}

func (e *osExecutor) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (e *osExecutor) Close() error                                  { return nil }
func (e *osExecutor) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{}, nil
}
func (e *osExecutor) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error { return nil }

// recursiveExecutor returns a different canned stdout per call.
// Calls past len(responses) error.
type recursiveExecutor struct {
	mu        sync.Mutex
	argv      [][]string
	responses []string
	idx       int
}

func (e *recursiveExecutor) StartCommand(
	_ context.Context, cmd workspaceapi.Cmd,
) (workspaceapi.Pid, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	args := append([]string{cmd.Path}, cmd.Args...)
	e.argv = append(e.argv, args)
	if e.idx >= len(e.responses) {
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- errors.New(
				"recursiveExecutor: exhausted responses")
		}
		return 0, errors.New("recursiveExecutor: exhausted responses")
	}
	out := e.responses[e.idx]
	e.idx++
	if cmd.Stdout != nil && len(out) > 0 {
		_, _ = cmd.Stdout.Write([]byte(out))
	}
	if cmd.Watcher != nil {
		cmd.Watcher.WatchProcess() <- nil
	}
	return 1, nil
}

func (e *recursiveExecutor) Signal(workspaceapi.Pid, syscall.Signal) error {
	return nil
}
func (e *recursiveExecutor) Close() error { return nil }
func (e *recursiveExecutor) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{}, errors.New("recursiveExecutor: no pty")
}
func (e *recursiveExecutor) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error {
	return nil
}

func (e *recursiveExecutor) argvSnapshot() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([][]string, len(e.argv))
	for i, a := range e.argv {
		out[i] = append([]string(nil), a...)
	}
	return out
}

// silence unused-import warnings when individual tests are skipped.
var _ = strings.TrimSpace
