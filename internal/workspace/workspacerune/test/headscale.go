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
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"text/template"
	"time"

	"unstable.build/rune/auth"
)

// headscaleImage is the coordination server Rune's network is designed
// around. Pinned to the version the account server deploys, so a test
// failure means our code changed, not the upstream image.
const headscaleImage = "headscale/headscale:v0.29.3"

//go:embed headscale.yaml.tmpl
var headscaleConfigTemplate string

// SkipIfNoDocker skips the test when docker is not usable on this host.
func SkipIfNoDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "true" {
		t.SkipNow()
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not available: %v", err)
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
}

// HeadscaleOption configures [StartHeadscale].
type HeadscaleOption func(*headscaleOptions)

type headscaleOptions struct {
	policy string
}

// WithPolicy loads policy, a HuJSON ACL document, before any node
// registers, so no node ever sees a netmap the policy would not grant.
func WithPolicy(policy string) HeadscaleOption {
	return func(o *headscaleOptions) { o.policy = policy }
}

// StartHeadscale runs a Headscale coordination server in docker and
// returns a control plane with one account and a pre-authorization key
// for it already minted, so nodes join without an interactive login.
//
// Ports are published one-to-one rather than letting docker choose:
// Headscale advertises its own listen and STUN ports to clients, so a
// remapped port would hand nodes an address that does not exist on the
// host.
func StartHeadscale(t *testing.T, opts ...HeadscaleOption) ControlPlane {
	t.Helper()
	EnsureHeadscaleImage(t)

	var o headscaleOptions
	for _, opt := range opts {
		opt(&o)
	}

	httpPort := freeTCPPort(t)
	stunPort := freeUDPPort(t)
	serverURL := fmt.Sprintf("http://127.0.0.1:%d", httpPort)
	configPath := writeHeadscaleConfig(t, serverURL, httpPort, stunPort)

	out, err := exec.Command("docker", "run", "-d", "--rm",
		"-p", fmt.Sprintf("%d:%d", httpPort, httpPort),
		"-p", fmt.Sprintf("%d:%d/udp", stunPort, stunPort),
		"-v", configPath+":/etc/headscale/config.yaml:ro",
		headscaleImage, "serve").Output()
	if err != nil {
		t.Fatalf("docker run headscale: %v: %s", err, exitStderr(err))
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })

	if err := waitHeadscaleHealthy(serverURL, 60*time.Second); err != nil {
		dumpDockerLogs(t, id)
		t.Fatalf("headscale did not become healthy on %s: %v", serverURL, err)
	}

	control := ControlPlane{URL: serverURL, headscale: id}
	if o.policy != "" {
		control.SetPolicy(t, o.policy)
	}
	control.AuthKey = control.NewAccount(t, "rune-e2e").UserKey(t)
	return control
}

// EnsureHeadscaleImage pulls the coordination server image if it is not
// cached yet.
func EnsureHeadscaleImage(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "image", "inspect", headscaleImage).Run(); err == nil {
		return
	}
	t.Logf("pulling %s (one-time per-machine cost)", headscaleImage)
	cmd := exec.Command("docker", "pull", headscaleImage)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker pull %s: %v", headscaleImage, err)
	}
}

// Account is one Rune account on a Headscale control plane: a
// Headscale user, exactly as the account server provisions it.
type Account struct {
	// Name is the Headscale user name, which is also the login name
	// its user-owned machines report.
	Name    string
	id      int
	control ControlPlane
}

// ServeTag is the tag that marks a machine as one of the account's
// serve-only machines.
func (a Account) ServeTag() string {
	return auth.ServeTagPrefix + a.Name
}

// UserKey mints a reusable key that registers a machine owned by the
// account. Reusable because a test registers several machines with it.
func (a Account) UserKey(t *testing.T) string {
	t.Helper()
	return a.control.preAuthKey(t, a.id)
}

// ServeKey mints a reusable key that registers a serve-only machine of
// the account: one carrying the account's [Account.ServeTag].
func (a Account) ServeKey(t *testing.T) string {
	t.Helper()
	return a.control.preAuthKey(t, a.id, "--tags", a.ServeTag())
}

// NewAccount creates a Headscale user named name.
func (c ControlPlane) NewAccount(t *testing.T, name string) Account {
	t.Helper()

	c.headscaleExec(t, "users", "create", name)
	out := c.headscaleExec(t, "users", "list", "--output", "json")
	var users []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(out, &users); err != nil {
		t.Fatalf("parse headscale users: %v: %s", err, string(out))
	}
	for _, u := range users {
		if u.Name == name {
			return Account{Name: name, id: u.ID, control: c}
		}
	}
	t.Fatalf("headscale user %q not found in %s", name, string(out))
	return Account{}
}

func (c ControlPlane) preAuthKey(t *testing.T, userID int, extra ...string) string {
	t.Helper()

	args := append([]string{"preauthkeys", "create",
		"--user", fmt.Sprint(userID),
		"--reusable", "--expiration", "24h"}, extra...)
	key := c.headscaleExec(t, args...)
	// The key is the last line; earlier lines are log output.
	lines := strings.Fields(strings.TrimSpace(string(key)))
	if len(lines) == 0 {
		t.Fatalf("headscale returned an empty pre-auth key")
	}
	return lines[len(lines)-1]
}

// SetPolicy replaces the control plane's ACL policy and fails the test
// unless Headscale reads back exactly what was written, so a test can
// never run against a topology other than the one it declares.
func (c ControlPlane) SetPolicy(t *testing.T, policy string) {
	t.Helper()

	// The image has no shell, so the file is copied in rather than
	// piped through one.
	src := filepath.Join(t.TempDir(), "policy.hujson")
	if err := os.WriteFile(src, []byte(policy), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	if out, err := exec.Command("docker", "cp", src,
		c.container(t)+":/tmp/policy.hujson").CombinedOutput(); err != nil {
		t.Fatalf("copy policy into headscale: %v: %s", err, string(out))
	}
	c.headscaleExec(t, "policy", "set", "-f", "/tmp/policy.hujson")

	got := c.headscaleExec(t, "policy", "get")
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace([]byte(policy))) {
		t.Fatalf("headscale policy readback differs from what was set:\n"+
			"--- set\n%s\n--- got\n%s", policy, string(got))
	}
}

// NodeInfo is the control plane's record of a registered machine.
type NodeInfo struct {
	ID       uint64
	Hostname string
	// User is the Headscale user the machine is filed under. Tagged
	// machines are owned by their tags and filed under a shared
	// pseudo-user.
	User   string
	Tags   []string
	Expiry *time.Time
	Addrs  []string
}

// Node returns the control plane's record of the machine registered as
// hostname.
func (c ControlPlane) Node(t *testing.T, hostname string) NodeInfo {
	t.Helper()

	out := c.headscaleExec(t, "nodes", "list", "--output", "json")
	var nodes []struct {
		ID        uint64   `json:"id"`
		Name      string   `json:"name"`
		GivenName string   `json:"given_name"`
		Tags      []string `json:"tags"`
		Addrs     []string `json:"ip_addresses"`
		User      *struct {
			Name string `json:"name"`
		} `json:"user"`
		Expiry *struct {
			Seconds int64 `json:"seconds"`
		} `json:"expiry"`
	}
	if err := json.Unmarshal(out, &nodes); err != nil {
		t.Fatalf("parse headscale nodes: %v: %s", err, string(out))
	}
	for _, n := range nodes {
		if n.GivenName != hostname && n.Name != hostname {
			continue
		}
		info := NodeInfo{
			ID:       n.ID,
			Hostname: hostname,
			Tags:     slices.Clone(n.Tags),
			Addrs:    n.Addrs,
		}
		if n.User != nil {
			info.User = n.User.Name
		}
		if n.Expiry != nil && n.Expiry.Seconds > 0 {
			exp := time.Unix(n.Expiry.Seconds, 0)
			info.Expiry = &exp
		}
		return info
	}
	t.Fatalf("headscale has no node %q: %s", hostname, string(out))
	return NodeInfo{}
}

func (c ControlPlane) headscaleExec(t *testing.T, args ...string) []byte {
	t.Helper()

	out, err := exec.Command("docker",
		append([]string{"exec", c.container(t), "headscale"}, args...)...).Output()
	if err != nil {
		t.Fatalf("headscale %s: %v: %s",
			strings.Join(args, " "), err, exitStderr(err))
	}
	return out
}

func (c ControlPlane) container(t *testing.T) string {
	t.Helper()
	if c.headscale == "" {
		t.Fatalf("control plane at %s is not a Headscale container", c.URL)
	}
	return c.headscale
}

func writeHeadscaleConfig(
	t *testing.T, serverURL string, httpPort, stunPort int,
) string {
	t.Helper()

	tmpl, err := template.New("headscale").Parse(headscaleConfigTemplate)
	if err != nil {
		t.Fatalf("parse headscale config template: %v", err)
	}
	// Written outside t.TempDir(): docker on macOS only bind-mounts
	// paths the VM shares, and /tmp is shared while the per-test
	// directory under /var/folders is not.
	f, err := os.CreateTemp("/tmp", "headscale-*.yaml")
	if err != nil {
		t.Fatalf("create headscale config: %v", err)
	}
	defer f.Close()
	t.Cleanup(func() { _ = os.Remove(f.Name()) })

	if err := tmpl.Execute(f, map[string]any{
		"ServerURL": serverURL,
		"HTTPPort":  httpPort,
		"STUNPort":  stunPort,
	}); err != nil {
		t.Fatalf("render headscale config: %v", err)
	}
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		t.Fatalf("chmod headscale config: %v", err)
	}
	return f.Name()
}

func waitHeadscaleHealthy(serverURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(serverURL + "/health") //nolint:noctx
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("health returned %s", resp.Status)
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out: %w", lastErr)
}

// freeTCPPort asks the kernel for an unused port. The listener is
// closed before the port is handed out, so this races any other process
// binding in between; in practice the window is small and the harness
// fails loudly rather than silently misbehaving.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp port: %v", err)
	}
	defer lis.Close()
	return lis.Addr().(*net.TCPAddr).Port
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve udp port: %v", err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

func dumpDockerLogs(t *testing.T, id string) {
	t.Helper()
	out, err := exec.Command("docker", "logs", id).CombinedOutput()
	if err != nil {
		t.Logf("docker logs %s: %v", id, err)
		return
	}
	t.Logf("headscale %s logs:\n%s", id, string(out))
}

func exitStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(exitErr.Stderr)
	}
	return ""
}

// repoRoot walks upwards from this file until it finds the go.mod that
// anchors the module.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}
