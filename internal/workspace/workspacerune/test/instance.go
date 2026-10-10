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

package runetest

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/runenet"
)

// ProbePort is the mesh port every instance started by [StartInstance]
// accepts bare connections on, so a test can observe whether a peer's
// packets reach it independently of the workspace server.
const ProbePort = 7474

// Instance is a Rune instance running in its own process.
type Instance struct {
	Hostname string
	// DataDir holds the instance's network identity.
	DataDir string
	// Addr is the instance's mesh address.
	Addr netip.Addr
	// LoginName is the account the instance reported as its owner
	// once it joined, as [runenet.Status] reports it.
	LoginName string

	cmd     *exec.Cmd
	exited  chan struct{}
	accepts *acceptLog
}

// WorkspaceEndpoint is the instance's workspace server.
func (i *Instance) WorkspaceEndpoint() Endpoint {
	return Endpoint{Name: i.Hostname, Addr: i.Addr, Port: runenet.DefaultPort}
}

// ProbeEndpoint is the instance's [ProbePort] listener.
func (i *Instance) ProbeEndpoint() Endpoint {
	return Endpoint{
		Name: i.Hostname, Addr: i.Addr, Port: ProbePort, accepts: i.accepts,
	}
}

// Stop shuts the instance down gracefully, so its identity on disk is
// left the way a stopped Rune leaves it.
func (i *Instance) Stop(t *testing.T) {
	t.Helper()
	_ = i.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-i.exited:
	case <-time.After(30 * time.Second):
		t.Fatalf("rune instance %q did not stop", i.Hostname)
	}
}

// StartInstance launches a Rune instance as its own process, joined to
// control under hostname with authKey and serving its workspaces. It
// returns once the instance reports itself reachable.
func StartInstance(
	t *testing.T, control ControlPlane, hostname, authKey string,
) *Instance {
	t.Helper()

	// Not t.TempDir(): the instance holds its network identity
	// here and must be able to write it for the whole test.
	dataDir := t.TempDir()
	cmd := exec.Command(instanceBinary(t),
		"-hostname", hostname,
		"-control-url", control.URL,
		"-auth-key", authKey,
		"-datadir", dataDir,
		"-probe-port", fmt.Sprint(ProbePort),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rune instance %q: %v", hostname, err)
	}
	inst := &Instance{
		Hostname: hostname,
		DataDir:  dataDir,
		cmd:      cmd,
		exited:   make(chan struct{}),
		accepts:  &acceptLog{},
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-inst.exited
	})

	go debug.CapturePanicReport(func() {
		forwardOutput(t, hostname, stderr)
	})
	ready := make(chan struct{})
	go debug.CapturePanicReport(func() {
		inst.readStdout(t, stdout, ready)
		// Wait closes the pipes, so it must not run before stdout is
		// drained.
		_ = cmd.Wait()
		close(inst.exited)
	})

	// Blocking on the ready line rather than polling means tests never
	// race the instance's registration with the coordination server.
	select {
	case <-ready:
	case <-time.After(nodeJoinTimeout):
		t.Fatalf("rune instance %q never became ready", hostname)
	}
	return inst
}

// readStdout follows the instance's stdout until it ends, closing ready
// at the ready line and recording what the instance reports.
func (i *Instance) readStdout(t *testing.T, stdout io.Reader, ready chan<- struct{}) {
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.Contains(line, readyLine):
			close(ready)
		case strings.HasPrefix(line, addrPrefix):
			i.Addr, _ = netip.ParseAddr(strings.TrimPrefix(line, addrPrefix))
		case strings.HasPrefix(line, loginPrefix):
			i.LoginName = strings.TrimPrefix(line, loginPrefix)
		case strings.HasPrefix(line, acceptPrefix):
			addr, err := netip.ParseAddr(strings.TrimPrefix(line, acceptPrefix))
			if err == nil {
				i.accepts.record(addr)
			}
		default:
			t.Logf("%s: %s", i.Hostname, line)
		}
	}
}

func forwardOutput(t *testing.T, hostname string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		t.Logf("%s: %s", hostname, scanner.Text())
	}
}

// These mirror the lines runenetsvc prints on stdout.
const (
	readyLine    = "runenetsvc: ready"
	addrPrefix   = "runenetsvc: addr "
	loginPrefix  = "runenetsvc: login "
	acceptPrefix = "runenetsvc: accepted "
)

var (
	instanceBinaryOnce sync.Once
	instanceBinaryPath string
	instanceBinaryErr  error
)

// instanceBinary builds the peer instance once per test binary and
// caches it, so a package with several e2e tests pays for one compile.
func instanceBinary(t *testing.T) string {
	t.Helper()

	instanceBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "runenetsvc")
		if err != nil {
			instanceBinaryErr = err
			return
		}
		instanceBinaryPath = filepath.Join(dir, "runenetsvc")
		cmd := exec.Command("go", "build",
			"-o", instanceBinaryPath,
			"unstable.build/rune/internal/workspace/workspacerune/test/cmd/runenetsvc")
		cmd.Dir = repoRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			instanceBinaryErr = fmt.Errorf("go build runenetsvc: %v: %s", err, string(out))
		}
	})
	if instanceBinaryErr != nil {
		t.Fatalf("build rune instance: %v", instanceBinaryErr)
	}
	return instanceBinaryPath
}
