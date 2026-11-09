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
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/go-dap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/debug"
)

// dapAdapter is a debug adapter on this machine, for debugger configurations
// that connect to a running adapter instead of starting one. It answers what
// the editor needs to open a session and to launch, and reports each session
// once the editor initialized it.
type dapAdapter struct {
	addr     string
	sessions chan *dapSession

	ln        net.Listener
	wg        sync.WaitGroup
	mu        sync.Mutex
	conns     []net.Conn
	closed    bool
	closeOnce sync.Once
}

// dapSession is one editor connection to a dapAdapter.
type dapSession struct {
	// launches receives the arguments of every launch request.
	launches chan map[string]any
}

func startDAPAdapter(t *testing.T) *dapAdapter {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	a := &dapAdapter{
		addr:     ln.Addr().String(),
		sessions: make(chan *dapSession, 8),
		ln:       ln,
	}
	a.wg.Add(1)
	go debug.CapturePanicReport(func() {
		defer a.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			a.mu.Lock()
			if a.closed {
				a.mu.Unlock()
				_ = conn.Close()
				return
			}
			a.conns = append(a.conns, conn)
			a.wg.Add(1)
			a.mu.Unlock()
			go debug.CapturePanicReport(func() {
				defer a.wg.Done()
				a.serve(conn)
			})
		}
	})
	t.Cleanup(a.close)
	return a
}

// close drops every connection, which ends the editor's sessions on them.
func (a *dapAdapter) close() {
	a.closeOnce.Do(func() {
		a.mu.Lock()
		a.closed = true
		for _, c := range a.conns {
			_ = c.Close()
		}
		a.mu.Unlock()
		_ = a.ln.Close()
		a.wg.Wait()
	})
}

func (a *dapAdapter) serve(conn net.Conn) {
	s := &dapSession{launches: make(chan map[string]any, 8)}
	r := bufio.NewReader(conn)
	seq := 0
	send := func(m dap.Message) {
		_ = dap.WriteProtocolMessage(conn, m)
	}
	protocol := func(typ string) dap.ProtocolMessage {
		seq++
		return dap.ProtocolMessage{Seq: seq, Type: typ}
	}
	reply := func(req *dap.Request) dap.Response {
		return dap.Response{
			ProtocolMessage: protocol("response"),
			RequestSeq:      req.Seq,
			Success:         true,
			Command:         req.Command,
		}
	}
	for {
		msg, err := dap.ReadProtocolMessage(r)
		if err != nil {
			return
		}
		switch msg := msg.(type) {
		case *dap.InitializeRequest:
			send(&dap.InitializeResponse{Response: reply(&msg.Request)})
			select {
			case a.sessions <- s:
			default:
			}
		case *dap.LaunchRequest:
			var args map[string]any
			if err := json.Unmarshal(msg.Arguments, &args); err != nil {
				args = map[string]any{"unmarshal error": err.Error()}
			}
			select {
			case s.launches <- args:
			default:
			}
			// The launch response waits for configurationDone, but the
			// adapter is ready for configuration now.
			send(&dap.InitializedEvent{
				Event: dap.Event{ProtocolMessage: protocol("event"), Event: "initialized"},
			})
		case dap.RequestMessage:
			r := reply(msg.GetRequest())
			send(&r)
		}
	}
}

// console runs line in the console of the workspace in focus, as typed at
// its prompt.
func (e *editor) console(t *testing.T, line ...string) {
	t.Helper()
	e.dispatch(t, "console", line...)
}

// mirroredWorkspaceDir creates a workspace directory on h at a path that
// also exists on this machine, and returns it. The debugger console writes
// a session's output log to the workspace directory through this machine's
// filesystem, so it cannot start a session in an SSH workspace whose path
// does not exist here.
func (h *remoteHost) mirroredWorkspaceDir(t *testing.T) string {
	t.Helper()
	// Under /tmp, where the remote user can create the same path.
	dir, err := os.MkdirTemp("/tmp", "crosshost-ws-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	h.exec(t, "mkdir -p "+shellQuote(dir))
	return dir
}

// awaitSession waits for the editor to open a session on a.
func (a *dapAdapter) awaitSession(t *testing.T, what string) *dapSession {
	t.Helper()
	select {
	case s := <-a.sessions:
		return s
	case <-time.After(commandTimeout):
		t.Fatalf("timed out after %s waiting for %s", commandTimeout, what)
		return nil
	}
}

// launch launches program in the debug session of the console in focus,
// which s is the adapter's side of, and returns the arguments the adapter
// received.
func (e *editor) launch(t *testing.T, s *dapSession, program string) map[string]any {
	t.Helper()
	// The editor opened the session before the console records it as its
	// own, and nothing tells when it has. Until then the console rejects the
	// launch without sending it, so a retry cannot launch twice.
	const retryInterval = time.Second
	deadline := time.Now().Add(commandTimeout)
	for {
		e.console(t, "debugger", "launch", program)
		select {
		case args := <-s.launches:
			return args
		case <-time.After(retryInterval):
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for the launch of %s", commandTimeout, program)
		}
	}
}

func TestDebugAdapterStartsFromItsWorkspaceHostDataDir(t *testing.T) {
	h := startHost(t)
	h.writeFile(t, remoteDataDir+"/record", recordScript, 0o755)
	remoteWS := h.mirroredWorkspaceDir(t)
	// The adapter records how it started and exits, which fails the session
	// right after.
	e := startEditor(t, h, t.TempDir(), editorSetup{
		dataFiles: map[string]string{"record": recordScript},
		config: `debugger:
  e2e:
    command: "$RUNE_DATADIR/record $RUNE_DATADIR/adapter --data=$RUNE_DATADIR"
`,
	})

	e.console(t, "debugger", "initialize", "e2e")
	assert.Equal(t, "arg<--data="+e.dataDir+">\nenv<"+e.dataDir+">\n",
		waitRecord(t, "the local workspace's adapter",
			localFile(filepath.Join(e.dataDir, "adapter"))),
		"a local workspace starts the adapter from the editor's data directory")

	e.openWorkspace(t, h.uri(t, remoteWS))
	e.console(t, "debugger", "initialize", "e2e")
	assert.Equal(t, "arg<--data="+remoteDataDir+">\nenv<"+remoteDataDir+">\n",
		waitRecord(t, "the ssh workspace's adapter",
			h.remoteFile(t, remoteDataDir+"/adapter")),
		"an ssh workspace starts the adapter from its host's data directory")
}

func TestDebugAdapterLaunchArgumentsResolveTheWorkspaceHostDataDir(t *testing.T) {
	h := startHost(t)
	remoteWS := h.mirroredWorkspaceDir(t)
	adapter := startDAPAdapter(t)
	// Launch arguments reach the adapter over the protocol only, so the
	// editor is the one that can expand them.
	e := startEditor(t, h, t.TempDir(), editorSetup{
		config: fmt.Sprintf(`debugger:
  e2e:
    command: "connect://%s"
    launch:
      data: "$RUNE_DATADIR/launch"
      home: "$HOME/launch"
`, adapter.addr),
	})
	// The editor's console waits for its sessions to end as it closes, which
	// dropping the adapter's connections does.
	t.Cleanup(adapter.close)

	e.console(t, "debugger", "initialize", "e2e")
	local := e.launch(t, adapter.awaitSession(t, "the local workspace's session"), "/bin/true")
	assert.Equal(t, e.dataDir+"/launch", local["data"],
		"a local workspace launches with the editor's data directory")
	assert.Equal(t, "$HOME/launch", local["home"], "only $RUNE_DATADIR is expanded")

	e.openWorkspace(t, h.uri(t, remoteWS))
	e.console(t, "debugger", "initialize", "e2e")
	remote := e.launch(t, adapter.awaitSession(t, "the ssh workspace's session"), "/bin/true")
	assert.Equal(t, remoteDataDir+"/launch", remote["data"],
		"an ssh workspace launches with its host's data directory")
	assert.Equal(t, "$HOME/launch", remote["home"], "only $RUNE_DATADIR is expanded")
}
