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

// Package workspacetest contains the docker-driven integration helpers for
// the workspacessh package. The harness builds and starts containers with
// per-scenario environment / sshd_config and exposes the host:port pair
// each test should connect to.
package workspacetest

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// imageName is the Docker image used by the e2e harness. It is built by
// build_docker.sh and refreshed lazily by EnsureImage.
const imageName = "rune_ssh_workspace_test:latest"

// SSHDScenario fully describes one server-side configuration the matrix
// exercises. The harness sets the matching environment variables on the
// linuxserver/openssh-server container and overlays ExtraSSHDConfig as
// /config/sshd/sshd_config.d/test.conf, then HUPs sshd.
type SSHDScenario struct {
	// Name is the subtest name.
	Name string

	// PasswordAccess sets PASSWORD_ACCESS on the container.
	PasswordAccess bool
	// UserPassword, if non-empty, sets USER_PASSWORD on the container.
	UserPassword string

	// PublicKeyFile is the path inside the image to install as the user's
	// authorized_key. Empty disables public key install entirely.
	PublicKeyFile string

	// ExtraSSHDConfig is appended to /config/sshd/sshd_config.d/test.conf.
	ExtraSSHDConfig string

	// InstallRuneBinary, when true, cross-compiles the runesvc helper
	// (internal/workspace/workspacessh/test/cmd/runesvc) and bind-mounts it into
	// the container, then installs it at the ssh user's
	// ~/.local/bin/rune — the only supported remote location, and the
	// one Rune's install.sh uses. That directory is deliberately NOT on
	// the sshd session PATH (sshd runs a non-login shell, skipping the
	// profile files where distributions add ~/.local/bin), so every
	// scenario exercises connectScheme's PATH injection the same way a
	// production install does.
	//
	// This lets a test exercise the full connectScheme path
	// (whichCommand + StartSchemeServer) against a real Linux binary
	// without building the full `rune` package, which depends on
	// CGO/GUI libraries.
	InstallRuneBinary bool

	// ServeDelay, when non-empty, is written to the ssh user's
	// ~/.rune/serve_delay as a Go duration string. The runesvc stand-in
	// sleeps that long before it starts serving (and before it emits the
	// ServerReady sentinel), so a test can prove connectScheme waits for
	// readiness instead of handing back a client while stdout carries no
	// gRPC server yet.
	ServeDelay string
}

// Container is a running test container's external handle.
type Container struct {
	ID       string
	HostPort string // host:port
}

// EnsureImage builds the test image if it is missing. Test authors should
// call this once before launching a container.
func EnsureImage(t *testing.T) {
	t.Helper()
	out, err := exec.Command("docker", "image", "inspect", imageName).CombinedOutput()
	if err == nil {
		return
	}
	t.Logf("building docker image %s (this is a one-time per-machine cost): %s", imageName, string(out))
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	cmd := exec.Command("docker", "build",
		"-f", filepath.Join(root, "workspace/workspacessh/test/Dockerfile"),
		"-t", imageName, root)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("docker build: %v", err)
	}
}

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

// StartContainer launches a container for scenario and returns its host
// port plus a t.Cleanup-bound teardown.
func StartContainer(t *testing.T, scenario SSHDScenario) *Container {
	t.Helper()

	args := []string{"run", "-d", "--rm",
		"-e", "TZ=America/Los_Angeles",
		"-e", "USER_NAME=test",
		"-e", "PUID=1000",
		"-e", "PGID=1000",
		"-e", "SUDO_ACCESS=true",
		"-P",
	}
	if scenario.InstallRuneBinary {
		bin := buildRuneTestBinary(t)
		args = append(args,
			"-v", bin+":/runesvc:ro",
		)
	}
	if scenario.PasswordAccess {
		args = append(args, "-e", "PASSWORD_ACCESS=true")
	}
	if scenario.UserPassword != "" {
		args = append(args, "-e", "USER_PASSWORD="+scenario.UserPassword)
	}
	if scenario.PublicKeyFile != "" {
		args = append(args, "-e", "PUBLIC_KEY_FILE="+scenario.PublicKeyFile)
	}
	args = append(args, imageName)

	out, err := exec.Command("docker", args...).Output()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, exitStderr(err))
	}
	id := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		// Use rm -f rather than stop: the container ran with --rm so the
		// stop happens implicitly, but rm -f is non-blocking on the
		// daemon's side and avoids hangs when the daemon is busy.
		_ = exec.Command("docker", "rm", "-f", id).Run()
	})

	port, err := inspectHostPort(id)
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	addr := net.JoinHostPort("127.0.0.1", port)
	if err := waitForSSH(addr, 30*time.Second); err != nil {
		_ = dumpLogs(id)
		t.Fatalf("ssh did not become ready on %s: %v", addr, err)
	}

	// Apply any per-scenario sshd_config snippet AFTER sshd is up. The
	// linuxserver/openssh-server image renders /config/sshd/sshd_config
	// during cont-init; we have to wait for that to finish before
	// dropping snippets in /config/sshd/sshd_config.d, otherwise the
	// init script may overwrite our changes (and our `pkill sshd`
	// would race with the supervisor's first start).
	if scenario.ExtraSSHDConfig != "" {
		applyExtraSSHDConfig(t, id, scenario.ExtraSSHDConfig)
		if err := waitForSSH(addr, 30*time.Second); err != nil {
			_ = dumpLogs(id)
			t.Fatalf("sshd did not come back up after applying ExtraSSHDConfig: %v", err)
		}
	}
	if scenario.InstallRuneBinary {
		installUserLocalRune(t, id)
	}
	if scenario.ServeDelay != "" {
		writeServeDelay(t, id, scenario.ServeDelay)
	}
	return &Container{ID: id, HostPort: addr}
}

// writeServeDelay writes a Go duration string to the ssh user's
// ~/.rune/serve_delay inside the container. The runesvc stand-in reads it and
// sleeps that long before serving, standing in for a slow pre-serving phase.
// The home directory is resolved from /etc/passwd because the
// linuxserver/openssh-server image homes its user at /config, not /home/<user>.
func writeServeDelay(t *testing.T, id, delay string) {
	t.Helper()
	script := `home="$(getent passwd test | cut -d: -f6)" &&
mkdir -p "$home/.rune" &&
printf %s '` + delay + `' > "$home/.rune/serve_delay" &&
chown -R test "$home/.rune"`
	out, err := exec.Command("docker", "exec", id, "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("write serve delay: %v: %s", err, out)
	}
}

// installUserLocalRune links the bind-mounted runesvc helper into the
// ssh user's ~/.local/bin/rune. The home directory is resolved from
// /etc/passwd because the linuxserver/openssh-server image homes its
// user at /config, not /home/<user>. Run after waitForSSH so the
// image's cont-init has finished creating the user.
func installUserLocalRune(t *testing.T, id string) {
	t.Helper()
	const script = `home="$(getent passwd test | cut -d: -f6)" &&
mkdir -p "$home/.local/bin" &&
ln -sf /runesvc "$home/.local/bin/rune" &&
chown -R test "$home/.local"`
	out, err := exec.Command("docker", "exec", id, "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("install user-local rune: %v: %s", err, out)
	}
}

// runeBinaryCache memoises the path to the cross-compiled runesvc test
// helper across scenarios within a single `go test` process. The actual
// build is done lazily under a mutex.
var (
	runeBinaryMu    sync.Mutex
	runeBinaryPath  string
	runeBinaryError error
)

// buildRuneTestBinary cross-compiles the runesvc helper for the docker
// container's architecture and returns the absolute path to the
// produced binary. The binary is reused across scenarios within the
// same test process.
//
// We match GOARCH to the host's architecture rather than hard-coding
// linux/amd64. The test image is multi-arch (linux/amd64,
// linux/arm64), and on Apple Silicon hosts forcing linux/amd64
// pushes the resulting binary through QEMU emulation, which is
// non-deterministically buggy with the custom Go toolchain we use
// (see go.mod). Running the container natively avoids that whole
// class of flakiness.
func buildRuneTestBinary(t *testing.T) string {
	t.Helper()

	runeBinaryMu.Lock()
	defer runeBinaryMu.Unlock()

	if runeBinaryPath != "" || runeBinaryError != nil {
		if runeBinaryError != nil {
			t.Fatalf("build runesvc: %v", runeBinaryError)
		}
		return runeBinaryPath
	}

	root, err := repoRoot()
	if err != nil {
		runeBinaryError = err
		t.Fatalf("repo root: %v", err)
	}

	// runesvc lives inside the container, so always GOOS=linux
	// regardless of where the test runs. GOARCH is determined by
	// the host so docker can run it without emulation.
	goarch := containerGOARCH()

	// Use a stable cache directory under the repo's target dir so
	// successive test runs reuse the binary; it's cheap to rebuild
	// (~1s) but avoids littering os.TempDir.
	out := filepath.Join(root, "target", "runesvc-linux-"+goarch)
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		runeBinaryError = err
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := prepareRuneBinaryOutput(out); err != nil {
		runeBinaryError = err
		t.Fatalf("prepare runesvc output: %v", err)
	}

	cmd := exec.Command("go", "build",
		"-o", out,
		"./internal/workspace/workspacessh/test/cmd/runesvc/",
	)
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"GOOS=linux",
		"GOARCH="+goarch,
		"CGO_ENABLED=0",
	)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		runeBinaryError = fmt.Errorf("go build runesvc: %w: %s", err, outBytes)
		t.Fatalf("%v", runeBinaryError)
	}
	runeBinaryPath = out
	return runeBinaryPath
}

// prepareRuneBinaryOutput clears a cached output path that is not a
// regular file. Docker creates a missing bind-mount source as a
// directory on the host, and `go build -o <dir>` then writes the binary
// inside it rather than replacing it, so a single such mishap poisons
// the cache for every later run: the container gets a directory where
// the `rune` executable should be and the bootstrap's `which rune`
// fails.
func prepareRuneBinaryOutput(out string) error {
	info, err := os.Lstat(out)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() {
		return nil
	}
	return os.RemoveAll(out)
}

// containerGOARCH returns the GOARCH value to cross-compile runesvc
// for. We match the host arch so docker runs the resulting binary
// natively (no QEMU). Apple Silicon (darwin/arm64) → arm64; Intel
// macOS / linux servers → amd64. Fall back to amd64 for unknown
// hosts as that is what the linuxserver/openssh-server image
// historically tested with.
func containerGOARCH() string {
	switch runtime.GOARCH {
	case "arm64", "amd64":
		return runtime.GOARCH
	default:
		return "amd64"
	}
}

// PrivateKeyPath copies the named file under testdata/ into a
// per-test temp directory with mode 0600 and returns its absolute
// path.
//
// SSH (both the OpenSSH client binary and many libraries) refuses to
// load private keys whose mode is wider than 0600. The keys checked
// into testdata/ are tracked in git, which only preserves the
// executable bit; on every checkout/stash/restore the working tree
// gets the user's umask (typically 0644). Tests that pass the
// testdata path straight to `ssh -i` or to a Go SSH client that
// validates perms would otherwise fail intermittently with
// "WARNING: UNPROTECTED PRIVATE KEY FILE!".
//
// All integration tests should funnel through this helper instead
// of using filepath.Abs("./testdata/<name>") directly.
func PrivateKeyPath(t *testing.T, name string) string {
	t.Helper()
	src, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("abs key: %v", err)
	}
	contents, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read key %s: %v", src, err)
	}
	dst := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(dst, contents, 0o600); err != nil {
		t.Fatalf("write key %s: %v", dst, err)
	}
	return dst
}

// ContainerHostPubKeys returns ALL OpenSSH-format public host keys
// the container's sshd is currently presenting (rsa, ecdsa, ed25519).
// The linuxserver/openssh-server image generates a key per algorithm
// on first boot and stores them under /config/ssh_host_keys.
//
// Tests must record every algorithm the server may present in their
// known_hosts file: the Go ssh client negotiates HostKeyAlgorithms
// in preference order, so recording only ed25519 surfaces as
// "knownhosts: key mismatch" whenever the negotiation lands on
// rsa-sha2-256 (or another type) instead.
func ContainerHostPubKeys(t *testing.T, id string) [][]byte {
	t.Helper()
	files := []string{
		"ssh_host_ed25519_key.pub",
		"ssh_host_rsa_key.pub",
		"ssh_host_ecdsa_key.pub",
	}
	var keys [][]byte
	for _, f := range files {
		out, err := exec.Command("docker", "exec", id,
			"cat", "/config/ssh_host_keys/"+f).CombinedOutput()
		if err != nil {
			t.Fatalf("read container host key %s: %v: %s",
				f, err, string(out))
		}
		if len(out) == 0 {
			t.Fatalf("empty container host key %s — is sshd running?", f)
		}
		keys = append(keys, out)
	}
	return keys
}

// RegenerateContainerHostKeys throws away every existing host key
// in /config/ssh_host_keys and forces sshd to generate fresh ones.
// Used to simulate a host whose key changed between dials (the
// classic MITM-or-rebuild case our remoteScheme.maintainConnection
// should surface as ErrHostKeyMismatch).
func RegenerateContainerHostKeys(t *testing.T, id string) {
	t.Helper()
	const script = `set -e
cd /config/ssh_host_keys
rm -f ssh_host_*
ssh-keygen -q -N '' -t rsa     -f ssh_host_rsa_key
ssh-keygen -q -N '' -t ecdsa   -f ssh_host_ecdsa_key
ssh-keygen -q -N '' -t ed25519 -f ssh_host_ed25519_key
chmod 600 ssh_host_rsa_key ssh_host_ecdsa_key ssh_host_ed25519_key
chmod 644 ssh_host_rsa_key.pub ssh_host_ecdsa_key.pub ssh_host_ed25519_key.pub
chown test:users ssh_host_*
s6-svc -r /run/service/svc-openssh-server
`
	out, err := exec.Command("docker", "exec", id, "sh", "-c", script).
		CombinedOutput()
	if err != nil {
		t.Fatalf("regenerate host keys: %v: %s", err, string(out))
	}
}

// TerminateRemoteRuneServer stops the workspace server without disturbing
// sshd, forcing an established SSH workspace scheme to reconnect.
func TerminateRemoteRuneServer(t *testing.T, id string) {
	t.Helper()
	out, err := exec.Command(
		"docker", "exec", id, "sh", "-c", "pkill -f '[r]une -x'",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("terminate remote rune server: %v: %s", err, string(out))
	}
}

// WriteKnownHosts writes a known_hosts file pinning hostport to all
// the OpenSSH-format public keys in pubs. Suitable as the
// workspace-config "known_hosts" override. Pass every algorithm the
// server may present (typically rsa + ecdsa + ed25519) — recording
// only one type causes the Go ssh client to fail with "knownhosts:
// key mismatch" whenever HostKeyAlgorithms negotiation lands on a
// type that isn't in the file.
func WriteKnownHosts(t *testing.T, hostport string, pubs ...[]byte) string {
	t.Helper()
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		t.Fatalf("split host:port %q: %v", hostport, err)
	}
	addr := host
	if port != "" && port != "22" {
		addr = fmt.Sprintf("[%s]:%s", host, port)
	}
	dst := filepath.Join(t.TempDir(), "known_hosts")
	var content []byte
	for _, pub := range pubs {
		line := append([]byte(addr+" "), pub...)
		if len(line) > 0 && line[len(line)-1] != '\n' {
			line = append(line, '\n')
		}
		content = append(content, line...)
	}
	if err := os.WriteFile(dst, content, 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return dst
}

// SetUserPassword sets the unix password of the named user inside
// the container. The linuxserver image only seeds the password when
// USER_PASSWORD is set at container start; this helper is for
// scenarios that need to *re-set* it after boot or set it on a
// container that wasn't started with PasswordAccess (e.g. PAM-only
// kbd-interactive).
func SetUserPassword(t *testing.T, id, user, password string) {
	t.Helper()
	out, err := exec.Command("docker", "exec", id, "sh", "-c",
		fmt.Sprintf("echo %q | chpasswd", user+":"+password)).
		CombinedOutput()
	if err != nil {
		t.Fatalf("chpasswd %s: %v: %s", user, err, string(out))
	}
}

func applyExtraSSHDConfig(t *testing.T, id, snippet string) {
	t.Helper()
	tmp, err := os.CreateTemp(t.TempDir(), "test.conf")
	if err != nil {
		t.Fatalf("temp: %v", err)
	}
	if _, err := tmp.WriteString(snippet); err != nil {
		t.Fatalf("write snippet: %v", err)
	}
	tmp.Close()
	out, err := exec.Command("docker", "exec", id, "mkdir", "-p",
		"/config/sshd/sshd_config.d").CombinedOutput()
	if err != nil {
		t.Fatalf("mkdir sshd_config.d: %v: %s", err, string(out))
	}
	dst := id + ":/config/sshd/sshd_config.d/test.conf"
	out, err = exec.Command("docker", "cp", tmp.Name(), dst).CombinedOutput()
	if err != nil {
		t.Fatalf("docker cp: %v: %s", err, string(out))
	}
	// `docker cp` preserves the host's user/group ids, which on macOS
	// are typically 501 — sshd then refuses to read the snippet under
	// the `test` (uid 1000) user with "Permission denied". chmod to a
	// world-readable mode so the supervised sshd can include it.
	out, err = exec.Command("docker", "exec", id,
		"chmod", "0644", "/config/sshd/sshd_config.d/test.conf").
		CombinedOutput()
	if err != nil {
		t.Fatalf("chmod snippet: %v: %s", err, string(out))
	}
	// linuxserver/openssh-server's cont-init renders the sshd_config from
	// the upstream defaults at boot. To make snippets dropped under
	// /config/sshd/sshd_config.d/ take effect we (a) ensure
	// /config/sshd/sshd_config Includes that directory exactly once
	// and (b) restart sshd via s6-svc, which is the supervised path.
	// SIGHUP alone is not enough — sshd in this image re-execs only on
	// a fresh start, and `pkill sshd` mismatches the actual process
	// name (`sshd.pam`).
	const reloadScript = `set -e
if ! grep -qF '/config/sshd/sshd_config.d/*.conf' /config/sshd/sshd_config; then
	echo 'Include /config/sshd/sshd_config.d/*.conf' >> /config/sshd/sshd_config
fi
s6-svc -r /run/service/svc-openssh-server
`
	out, err = exec.Command("docker", "exec", id, "sh", "-c", reloadScript).
		CombinedOutput()
	if err != nil {
		t.Fatalf("sshd reload: %v: %s", err, string(out))
	}
}

func inspectHostPort(id string) (string, error) {
	out, err := exec.Command("docker", "inspect", id).Output()
	if err != nil {
		return "", err
	}
	var inspect []struct {
		NetworkSettings struct {
			Ports map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"Ports"`
		} `json:"NetworkSettings"`
	}
	if err := json.Unmarshal(out, &inspect); err != nil {
		return "", fmt.Errorf("parse docker inspect: %w", err)
	}
	if len(inspect) == 0 {
		return "", fmt.Errorf("docker inspect returned no entries")
	}
	bindings, ok := inspect[0].NetworkSettings.Ports["2222/tcp"]
	if !ok || len(bindings) == 0 {
		return "", fmt.Errorf("container has no 2222/tcp binding")
	}
	return bindings[0].HostPort, nil
}

func waitForSSH(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		err := pingSSHBanner(addr, 1500*time.Millisecond)
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timed out: %w", lastErr)
}

// pingSSHBanner connects and reads at least the "SSH-" prefix to ensure
// sshd is ready to negotiate.
func pingSSHBanner(addr string, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	buf := make([]byte, 4)
	if _, err := conn.Read(buf); err != nil {
		return fmt.Errorf("read banner: %w", err)
	}
	if string(buf) != "SSH-" {
		return fmt.Errorf("unexpected banner prefix %q", string(buf))
	}
	return nil
}

func dumpLogs(id string) error {
	out, err := exec.Command("docker", "logs", id).CombinedOutput()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "container %s logs:\n%s\n", id, string(out))
	return nil
}

func exitStderr(err error) string {
	if exitErr, ok := err.(*exec.ExitError); ok {
		return string(exitErr.Stderr)
	}
	return ""
}

// repoRoot walks upwards from this file's directory until it finds a
// go.mod, returning that path.
func repoRoot() (string, error) {
	_, file, _, ok := runtimeCaller()
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", filepath.Dir(file))
		}
		dir = parent
	}
}

// runtimeCaller is a tiny wrapper to keep runtime out of the import block
// when developers grep for "runtime" usage.
func runtimeCaller() (uintptr, string, int, bool) {
	return runtime.Caller(0)
}
