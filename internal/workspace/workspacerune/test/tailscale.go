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
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"unstable.build/rune/internal/debug"
)

// tailscaleImage is the Tailscale client the tailscale:// end-to-end
// tests run Rune next to. Pinned to the version Rune links, so a test
// failure means our code changed, not the upstream image.
const tailscaleImage = "tailscale/tailscale:v1.102.3"

// headscaleAlias is the name the machines of a [DockerTailnet] reach
// its Headscale by.
const headscaleAlias = "headscale"

// runeUser is the unprivileged account Rune runs as on the machines of
// a [DockerTailnet], as it does on a user's desktop.
const runeUser = "rune"

// Lines tailscalesvc prints for the harness.
const (
	svcReadyLine    = "tailscalesvc: ready"
	svcSignInPrefix = "tailscalesvc: sign in at "
	svcErrorPrefix  = "tailscalesvc: error "
)

// tailnetTimeout bounds how long a machine of a DockerTailnet takes to
// join, and a connection between two of them takes to come up.
const tailnetTimeout = 90 * time.Second

// DockerTailnet is the user's own tailnet, in docker: a Headscale
// coordination server and machines on a docker network of their own.
// Each machine runs the real Tailscale client, or Rune's own node, and
// Rune as tailscalesvc, as an unprivileged user.
type DockerTailnet struct {
	// Control is the tailnet's Headscale.
	Control ControlPlane
	network string
}

// StartDockerTailnet runs Headscale on a docker network of its own and
// loads policy, a HuJSON policy document, before any machine joins.
func StartDockerTailnet(t *testing.T, policy string) *DockerTailnet {
	t.Helper()
	SkipIfNoDocker(t)
	EnsureHeadscaleImage(t)
	ensureImage(t, tailscaleImage)

	network := "rune-tailnet-" + randomSuffix(t)
	if out, err := exec.Command("docker", "network", "create", network).CombinedOutput(); err != nil {
		t.Fatalf("docker network create: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", network).Run() })

	const httpPort, stunPort = 8080, 3478
	serverURL := fmt.Sprintf("http://%s:%d", headscaleAlias, httpPort)
	configPath := writeHeadscaleConfig(t, serverURL, httpPort, stunPort)
	id := dockerRun(t, "--network", network, "--network-alias", headscaleAlias,
		"-v", configPath+":/etc/headscale/config.yaml:ro",
		headscaleImage, "serve")
	control := ControlPlane{URL: serverURL, headscale: id}
	deadline := time.Now().Add(60 * time.Second)
	for {
		err := exec.Command("docker", "exec", id, "headscale", "users", "list").Run()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			dumpDockerLogs(t, id)
			t.Fatalf("headscale did not come up: %v", err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	if policy != "" {
		control.SetPolicy(t, policy)
	}
	return &DockerTailnet{Control: control, network: network}
}

// DockerMachine is a machine of a DockerTailnet.
type DockerMachine struct {
	Hostname string
	// Addr is the machine's IPv4 tailnet address.
	Addr      netip.Addr
	container string
	tailnet   *DockerTailnet
}

// Join starts a machine named hostname running the Tailscale client,
// joined to the tailnet with authKey, which decides who owns it and
// whether it is tagged.
func (d *DockerTailnet) Join(t *testing.T, hostname, authKey string) *DockerMachine {
	t.Helper()
	m := d.startMachine(t, hostname,
		"--cap-add", "NET_ADMIN", "--device", "/dev/net/tun",
		"--entrypoint", "tailscaled", tailscaleImage,
		"--tun=tailscale0", "--state=mem:")
	deadline := time.Now().Add(tailnetTimeout)
	for {
		out, err := m.exec(t, "", "tailscale", "up",
			"--login-server", d.Control.URL, "--authkey", authKey,
			"--hostname", hostname, "--timeout", "30s")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not join the tailnet: %v: %s", hostname, err, out)
		}
		time.Sleep(time.Second)
	}
	out, err := m.exec(t, "", "tailscale", "ip", "-4")
	if err != nil {
		t.Fatalf("tailscale ip on %s: %v: %s", hostname, err, out)
	}
	m.Addr = netip.MustParseAddr(strings.TrimSpace(out))
	return m
}

// JoinHeadless starts a machine named hostname without a Tailscale
// client, running Rune as `rune --headless --control-url` does: Rune
// joins the tailnet as a machine of its own, which account signs in by
// URL, and serves its workspaces there.
func (d *DockerTailnet) JoinHeadless(t *testing.T, hostname string, account Account) *DockerMachine {
	t.Helper()
	m := d.startMachine(t, hostname, "--entrypoint", "sleep", tailscaleImage, "infinity")
	logs := m.startSvc(t, "headless", "-control-url", d.Control.URL,
		"-hostname", hostname, "-datadir", "/home/"+runeUser+"/.rune")
	url := logs.await(t, svcSignInPrefix)
	account.SignIn(t, strings.TrimPrefix(url, svcSignInPrefix))
	logs.await(t, svcReadyLine)
	m.Addr = d.Control.nodeAddr(t, hostname)
	return m
}

func (d *DockerTailnet) startMachine(t *testing.T, hostname string, args ...string) *DockerMachine {
	t.Helper()
	id := dockerRun(t, append([]string{"--network", d.network, "--hostname", hostname}, args...)...)
	m := &DockerMachine{Hostname: hostname, container: id, tailnet: d}
	if out, err := exec.Command("docker", "cp", tailscaleSvcBinary(t),
		id+":/usr/local/bin/tailscalesvc").CombinedOutput(); err != nil {
		t.Fatalf("copy tailscalesvc into %s: %v: %s", hostname, err, out)
	}
	if out, err := m.exec(t, "", "adduser", "-D", runeUser); err != nil {
		t.Fatalf("add user on %s: %v: %s", hostname, err, out)
	}
	return m
}

// Reconnect disconnects m's Tailscale client from the tailnet and
// connects it again, as its user does from the client's menu.
func (m *DockerMachine) Reconnect(t *testing.T) {
	t.Helper()
	if out, err := m.exec(t, "", "tailscale", "down"); err != nil {
		t.Fatalf("tailscale down on %s: %v: %s", m.Hostname, err, out)
	}
	out, err := m.exec(t, "", "tailscale", "up",
		"--login-server", m.tailnet.Control.URL, "--hostname", m.Hostname,
		"--timeout", "30s")
	if err != nil {
		t.Fatalf("tailscale up on %s: %v: %s", m.Hostname, err, out)
	}
}

// Serve runs Rune on m as the desktop app does: serving m's workspaces
// through its Tailscale client.
func (m *DockerMachine) Serve(t *testing.T) {
	t.Helper()
	m.startSvc(t, "serve", "-datadir", "/home/"+runeUser+"/.rune").await(t, svcReadyLine)
}

// WriteFile writes content to name, a path under the Rune user's home,
// and returns the file's absolute path.
func (m *DockerMachine) WriteFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join("/home", runeUser, name)
	cmd := exec.Command("docker", "exec", "-i", "-u", runeUser, m.container,
		"sh", "-c", `mkdir -p "$(dirname "$1")" && cat > "$1"`, "sh", path)
	cmd.Stdin = strings.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write %s on %s: %v: %s", path, m.Hostname, err, out)
	}
	return path
}

// OpenResult is what opening a tailscale:// workspace from a machine
// came to.
type OpenResult struct {
	// Output is the file read from the workspace, when it opened.
	Output string
	// Code is the gRPC code it failed with, or empty.
	Code string
	// Message is the message it failed with.
	Message string
}

// Open opens uri, a tailscale:// workspace, from m as Rune does, and
// reads file in it.
func (m *DockerMachine) Open(t *testing.T, uri, file string) OpenResult {
	t.Helper()
	out, err := m.exec(t, runeUser, "tailscalesvc", "open",
		"-uri", uri, "-read", file, "-timeout", tailnetTimeout.String())
	if line, ok := svcError(out); ok {
		code, msg, _ := strings.Cut(line, ": ")
		return OpenResult{Code: code, Message: msg}
	}
	if err != nil {
		t.Fatalf("open %s from %s: %v: %s", uri, m.Hostname, err, out)
	}
	return OpenResult{Output: stdoutOf(out)}
}

// Peers lists the machines `workspaceopen tailscale://` offers on m.
func (m *DockerMachine) Peers(t *testing.T) []string {
	t.Helper()
	out, err := m.exec(t, runeUser, "tailscalesvc", "peers")
	if line, ok := svcError(out); ok || err != nil {
		t.Fatalf("peers on %s: %v: %s", m.Hostname, err, line)
	}
	return strings.Fields(stdoutOf(out))
}

// exec runs args on m as user, root when empty, and returns its
// combined output.
func (m *DockerMachine) exec(t *testing.T, user string, args ...string) (string, error) {
	t.Helper()
	cmdArgs := []string{"exec"}
	if user != "" {
		cmdArgs = append(cmdArgs, "-u", user)
	}
	cmdArgs = append(cmdArgs, m.container)
	out, err := exec.Command("docker", append(cmdArgs, args...)...).CombinedOutput()
	return string(out), err
}

// startSvc runs tailscalesvc with args on m in the background, as the
// Rune user, until the test ends.
func (m *DockerMachine) startSvc(t *testing.T, args ...string) *svcLogs {
	t.Helper()
	cmd := exec.Command("docker", append([]string{"exec", "-u", runeUser, m.container,
		"tailscalesvc"}, args...)...)
	logs := &svcLogs{name: m.Hostname, lines: make(chan string, 256)}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("tailscalesvc on %s: %v", m.Hostname, err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatalf("start tailscalesvc on %s: %v", m.Hostname, err)
	}
	go debug.CapturePanicReport(func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			logs.record(scanner.Text())
		}
		close(logs.lines)
	})
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("tailscalesvc %s on %s:\n%s", args[0], m.Hostname, logs.String())
		}
	})
	return logs
}

// svcLogs is what a tailscalesvc printed.
type svcLogs struct {
	name  string
	lines chan string

	mu  sync.Mutex
	all bytes.Buffer
}

func (l *svcLogs) record(line string) {
	l.mu.Lock()
	l.all.WriteString(line + "\n")
	l.mu.Unlock()
	select {
	case l.lines <- line:
	default:
	}
}

func (l *svcLogs) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.all.String()
}

// await waits for a line starting with prefix and returns it.
func (l *svcLogs) await(t *testing.T, prefix string) string {
	t.Helper()
	timeout := time.After(tailnetTimeout)
	for {
		select {
		case line, ok := <-l.lines:
			if !ok {
				t.Fatalf("tailscalesvc on %s exited before %q:\n%s", l.name, prefix, l)
			}
			if strings.HasPrefix(line, prefix) {
				return line
			}
		case <-timeout:
			t.Fatalf("tailscalesvc on %s never printed %q:\n%s", l.name, prefix, l)
		}
	}
}

// svcError finds the error line in a tailscalesvc's output.
func svcError(out string) (string, bool) {
	for line := range strings.Lines(out) {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), svcErrorPrefix); ok {
			return rest, true
		}
	}
	return "", false
}

// stdoutOf drops the log lines from a tailscalesvc's output.
func stdoutOf(out string) string {
	var b strings.Builder
	for line := range strings.Lines(out) {
		if strings.HasPrefix(line, "time=") {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}

// nodeAddr is the IPv4 address Headscale gave the machine registered
// as hostname.
func (c ControlPlane) nodeAddr(t *testing.T, hostname string) netip.Addr {
	t.Helper()
	for _, a := range c.Node(t, hostname).Addrs {
		if addr, err := netip.ParseAddr(a); err == nil && addr.Is4() {
			return addr
		}
	}
	t.Fatalf("headscale gave %s no IPv4 address", hostname)
	return netip.Addr{}
}

// TagKey mints a reusable key that registers a machine of account
// tagged with tags.
func (a Account) TagKey(t *testing.T, tags ...string) string {
	t.Helper()
	return a.control.preAuthKey(t, a.id, "--tags", strings.Join(tags, ","))
}

func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", append([]string{"run", "-d", "--rm"}, args...)...).Output()
	if err != nil {
		t.Fatalf("docker run %v: %v: %s", args, err, exitStderr(err))
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		if t.Failed() {
			dumpDockerLogs(t, id)
		}
		_ = exec.Command("docker", "rm", "-f", id).Run()
	})
	return id
}

func ensureImage(t *testing.T, image string) {
	t.Helper()
	if err := exec.Command("docker", "image", "inspect", image).Run(); err == nil {
		return
	}
	t.Logf("pulling %s (one-time per-machine cost)", image)
	cmd := exec.Command("docker", "pull", image)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker pull %s: %v", image, err)
	}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

var (
	tailscaleSvcOnce sync.Once
	tailscaleSvcPath string
	tailscaleSvcErr  error
)

// tailscaleSvcBinary builds tailscalesvc for the docker host's Linux.
func tailscaleSvcBinary(t *testing.T) string {
	t.Helper()
	tailscaleSvcOnce.Do(func() {
		arch, err := dockerArch()
		if err != nil {
			tailscaleSvcErr = err
			return
		}
		dir, err := os.MkdirTemp("", "tailscalesvc")
		if err != nil {
			tailscaleSvcErr = err
			return
		}
		tailscaleSvcPath = filepath.Join(dir, "tailscalesvc")
		cmd := exec.Command("go", "build", "-o", tailscaleSvcPath,
			"unstable.build/rune/internal/workspace/workspacerune/test/cmd/tailscalesvc")
		cmd.Dir = repoRoot(t)
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			tailscaleSvcErr = fmt.Errorf("go build tailscalesvc: %v: %s", err, out)
		}
	})
	if tailscaleSvcErr != nil {
		t.Fatalf("build tailscalesvc: %v", tailscaleSvcErr)
	}
	return tailscaleSvcPath
}

// dockerArch is the Go architecture of the machine docker runs
// containers on.
func dockerArch() (string, error) {
	out, err := exec.Command("docker", "info", "--format", "{{.Architecture}}").Output()
	if err != nil {
		return "", fmt.Errorf("docker info: %w", err)
	}
	switch arch := strings.TrimSpace(string(out)); arch {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	case "":
		return runtime.GOARCH, nil
	default:
		return "", fmt.Errorf("unsupported docker architecture %q", arch)
	}
}
