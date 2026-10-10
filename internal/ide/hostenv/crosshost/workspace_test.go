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

//go:build e2e

package crosshost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace/workspacessh"
)

// commandTimeout bounds a single remote command or terminal session.
const commandTimeout = 60 * time.Second

// sshConfig is the workspace.ssh configuration that reaches h.
func (h *remoteHost) sshConfig() map[string]any {
	return map[string]any{
		"private_keys": []any{h.keyPath},
		"timeout":      "30s",
		"insecure":     true,
	}
}

// uri is the SSH workspace URI of dir on h.
func (h *remoteHost) uri(t *testing.T, dir string) workspaceapi.URI {
	t.Helper()
	uri, err := workspaceapi.ParseURI(fmt.Sprintf("ssh://%s@%s%s", remoteUser, h.addr, dir))
	require.NoError(t, err)
	return uri
}

// workspaceDir creates the directory name under the remote home and returns
// its path.
func (h *remoteHost) workspaceDir(t *testing.T, name string) string {
	t.Helper()
	dir := remoteHome + "/" + name
	h.exec(t, "mkdir -p "+shellQuote(dir))
	return dir
}

// connect opens the SSH workspace at dir on h the way an editor does, which
// starts `rune -x` on h, and returns once that server answers.
func (h *remoteHost) connect(
	t *testing.T, dir string, opts ...workspacessh.Option,
) schemeapi.Scheme {
	t.Helper()
	s, err := workspacessh.New(h.ui, opts...)(
		context.Background(), config.MapConfig(h.sshConfig()), h.uri(t, dir))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	waitFor(t, readyTimeout, "the ssh workspace at "+dir+" serves", func() error {
		_, err := s.Stat(dir)
		return err
	})
	return s
}

// run starts cmd through ex, requires it to exit successfully and returns
// its stdout.
func run(t *testing.T, ex schemeapi.Executor, cmd workspaceapi.Cmd) string {
	t.Helper()
	var stdout, stderr syncBuffer
	exited := make(chan error, 1)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Watcher = workspaceapi.ChanProcessWatcher(exited)
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	_, err := ex.StartCommand(ctx, cmd)
	require.NoError(t, err, "start %s %q", cmd.Path, cmd.Args)
	select {
	case err := <-exited:
		require.NoError(t, err, "%s %q exited with an error\nstdout:\n%s\nstderr:\n%s",
			cmd.Path, cmd.Args, stdout.String(), stderr.String())
	case <-ctx.Done():
		t.Fatalf("%s %q did not exit within %s\nstdout:\n%s\nstderr:\n%s",
			cmd.Path, cmd.Args, commandTimeout, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// environ runs env through ex and returns the environment it printed.
func environ(t *testing.T, ex schemeapi.Executor) map[string]string {
	t.Helper()
	env := map[string]string{}
	for _, line := range strings.Split(run(t, ex, workspaceapi.Cmd{Path: "env"}), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			env[k] = v
		}
	}
	return env
}

// terminal starts path with args on a pty it controls, as the editor's
// terminal does, types input, and returns what the terminal showed by the
// time the shell exited. input must make the shell exit.
func terminal(
	t *testing.T, s schemeapi.Scheme, path string, args []string, input string,
) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	pty, err := s.NewPty(ctx)
	require.NoError(t, err, "open a remote pty")
	defer pty.Master.Close()
	defer pty.Slave.Close()
	require.NoError(t, s.SetPtySize(pty, workspaceapi.PtySize{Columns: 120, Rows: 40}))

	master := &lockedWriter{w: pty.Master}
	var shown syncBuffer
	drained := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(drained)
		answerTerminalQueries(pty.Master, master, &shown)
	})

	exited := make(chan error, 1)
	_, err = s.StartCommand(ctx, workspaceapi.Cmd{
		Path:        path,
		Args:        args,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true, Setctty: true},
		Stdin:       pty.Slave,
		Stdout:      pty.Slave,
		Stderr:      pty.Slave,
		Watcher:     workspaceapi.ChanProcessWatcher(exited),
	})
	require.NoError(t, err, "start terminal %s %q", path, args)
	// The pty buffers what is typed before the shell reads it, as it does
	// for a user who types ahead.
	_, err = master.Write([]byte(input))
	require.NoError(t, err)

	select {
	case <-exited:
	case <-ctx.Done():
		t.Fatalf("terminal %s %q did not exit within %s; it showed:\n%s",
			path, args, commandTimeout, shown.String())
	}
	_ = pty.Slave.Close()
	select {
	case <-drained:
	case <-time.After(readyTimeout):
		t.Fatalf("terminal %s %q output never ended; it showed:\n%s",
			path, args, shown.String())
	}
	return shown.String()
}

// terminalQueries are the requests shells send a terminal at startup and may
// wait on, with the replies a terminal gives. fish 4 waits for the reply to
// the primary device attributes query before it reads input.
var terminalQueries = []struct{ query, reply string }{
	{"\x1b[c", "\x1b[?62;22c"},
	{"\x1b[0c", "\x1b[?62;22c"},
	{"\x1b[6n", "\x1b[1;1R"},
}

// answerTerminalQueries copies the terminal's output into shown until it
// ends, replying through reply to the queries in terminalQueries.
func answerTerminalQueries(r io.Reader, reply io.Writer, shown *syncBuffer) {
	var out []byte
	scanned := 0
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			_, _ = shown.Write(buf[:n])
			out = append(out, buf[:n]...)
			for {
				at, q := -1, -1
				for i, tq := range terminalQueries {
					if j := bytes.Index(out[scanned:], []byte(tq.query)); j >= 0 && (at < 0 || j < at) {
						at, q = j, i
					}
				}
				if at < 0 {
					break
				}
				_, _ = reply.Write([]byte(terminalQueries[q].reply))
				scanned += at + len(terminalQueries[q].query)
			}
			// Keep the tail that may start a query split across reads.
			if keep := len(out) - 3; keep > scanned {
				scanned = keep
			}
		}
		if err != nil {
			return
		}
	}
}

// lockedWriter serializes the writes the test and the query responder make
// to the pty master.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// marked returns the value printed between name< and > in out, which is
// how the suite's probes print what they observed: the terminal echoes the
// probe command itself, whose %s placeholders never match.
func marked(t *testing.T, out, name string) string {
	t.Helper()
	start := strings.LastIndex(out, name+"<")
	if start < 0 {
		t.Fatalf("no %s<...> in output:\n%s", name, out)
	}
	rest := out[start+len(name)+1:]
	end := strings.Index(rest, ">")
	if end < 0 {
		t.Fatalf("unterminated %s<...> in output:\n%s", name, out)
	}
	return rest[:end]
}

// pathEntries splits a PATH value into its directories.
func pathEntries(path string) []string {
	return strings.Split(path, ":")
}

// requireBefore requires that both entries are in path and that first comes
// before second.
func requireBefore(t *testing.T, path []string, first, second string) {
	t.Helper()
	i, j := slices.Index(path, first), slices.Index(path, second)
	require.GreaterOrEqual(t, i, 0, "%s is not on PATH %q", first, path)
	require.GreaterOrEqual(t, j, 0, "%s is not on PATH %q", second, path)
	require.Less(t, i, j, "%s must come before %s on PATH %q", first, second, path)
}
