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

// Package crosshost is the end-to-end suite for how $RUNE_DATADIR and gui.env
// cross the host and workspace boundaries: what a remote host expands, which
// environment it applies to the commands and terminals it starts, how packages
// it installs spell the data directory, and which host's data directory an
// editor resolves for the workspace in focus and for the debug adapters,
// extensions and tutorials it starts.
//
// The remote host is a container built from deploy/rune-headless/Dockerfile,
// extended by testimage.Dockerfile with sshd and the shells under test, so the
// suite runs the `rune -x` server the headless image ships. The first run
// builds the image; later runs reuse Docker's layer and Go build caches and
// only rebuild what the working tree changed.
//
// Every test starts a container of its own, waits on conditions rather than
// sleeping, and dumps the remote logs and configuration when it fails.
package crosshost

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/pem"
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

	"golang.org/x/crypto/ssh"
	"unstable.build/rune/internal/workspace/workspacessh"
)

const (
	imageName  = "rune_crosshost_e2e:latest"
	remoteUser = "rune"
	remoteHome = "/home/" + remoteUser
	// gitHost resolves to the container itself, where the suite serves the
	// repositories it installs packages from.
	gitHost = "git.rune.test"

	// imageBuildTimeout covers a cold build: the Go toolchain image, the
	// cross-compilation libraries and a full compile of rune. It stays under
	// the -timeout of make test-e2e so that a slow build fails with its
	// output rather than with the test binary's timeout panic.
	imageBuildTimeout = 12 * time.Minute
	// readyTimeout bounds every wait for something the container starts.
	readyTimeout = 60 * time.Second
	// pollInterval paces the waits for conditions that have no event to
	// block on, such as a file a remote command writes.
	pollInterval = 100 * time.Millisecond
)

var (
	imageOnce sync.Once
	imageErr  error
)

func skipIfNoDocker(t *testing.T) {
	t.Helper()
	// Matches the other docker-driven suites, which CI runs elsewhere.
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

// ensureImage builds the test image once per test binary. It always builds,
// rather than reusing an image that happens to exist, so the remote host runs
// the working tree's rune.
func ensureImage(t *testing.T) {
	t.Helper()
	skipIfNoDocker(t)
	imageOnce.Do(func() { imageErr = buildImage() })
	if imageErr != nil {
		t.Fatalf("build %s: %v", imageName, imageErr)
	}
}

// buildImage appends testimage.Dockerfile to the headless Dockerfile and
// builds the result in one go, which works with any buildx driver: a second
// build FROM the headless image would need the builder to see the local image
// store, which the docker-container driver cannot.
func buildImage() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	headless, err := os.ReadFile(filepath.Join(root, "deploy", "rune-headless", "Dockerfile"))
	if err != nil {
		return err
	}
	layer, err := os.ReadFile(filepath.Join(root, "internal", "ide", "hostenv", "crosshost", "testimage.Dockerfile"))
	if err != nil {
		return err
	}
	ignore, err := os.ReadFile(filepath.Join(root, "deploy", "rune-headless", "Dockerfile.dockerignore"))
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "crosshost-image")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	dockerfile := filepath.Join(dir, "Dockerfile")
	content := append(append(headless, '\n'), layer...)
	if err := os.WriteFile(dockerfile, content, 0o644); err != nil {
		return err
	}
	// buildx reads the ignore file next to the Dockerfile, not the one in
	// the context. The image only builds binaries, so leaving tests out of
	// the context keeps editing them from recompiling rune.
	ignore = append(ignore, "\n**/*_test.go\n"...)
	if err := os.WriteFile(dockerfile+".dockerignore", ignore, 0o644); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), imageBuildTimeout)
	defer cancel()
	// Fixed build metadata keeps the compile layer cacheable across runs.
	cmd := exec.CommandContext(ctx, "docker", "buildx", "build",
		"--load", "--progress=plain",
		"--platform", "linux/"+containerArch(),
		"--build-arg", "RUNE_BUILD_TAG=crosshost-e2e",
		"--build-arg", "RUNE_BUILD_COMMIT=crosshost-e2e",
		"--build-arg", "RUNE_BUILD_DATE=crosshost-e2e",
		"-f", dockerfile,
		"-t", imageName,
		root)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	start := time.Now()
	fmt.Fprintf(os.Stderr, "crosshost: building %s from the working tree\n", imageName)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker buildx build: %w\n%s", err, tail(out.String(), 64<<10))
	}
	fmt.Fprintf(os.Stderr, "crosshost: built %s in %s\n", imageName, time.Since(start).Round(time.Second))
	return nil
}

// containerArch is the architecture the image is built for: the host's, so
// the container never runs under emulation.
func containerArch() string {
	if runtime.GOARCH == "arm64" {
		return "arm64"
	}
	return "amd64"
}

// remoteHost is a running container of the test image.
type remoteHost struct {
	id string
	// addr is the host:port its sshd is published on.
	addr    string
	keyPath string
	ui      *recordingUI
}

// startHost starts a container of the test image that accepts the SSH key it
// returns, and waits for its sshd to greet. The container is removed when t
// ends, after its diagnostics are logged if t failed.
func startHost(t *testing.T) *remoteHost {
	t.Helper()
	ensureImage(t)

	keyPath, authorized := newClientKey(t)
	out, err := exec.Command("docker", "run", "-d", "--rm",
		"--add-host", gitHost+":127.0.0.1",
		"-p", "127.0.0.1::22",
		imageName).Output()
	if err != nil {
		t.Fatalf("docker run %s: %v: %s", imageName, err, exitStderr(err))
	}
	h := &remoteHost{
		id:      strings.TrimSpace(string(out)),
		keyPath: keyPath,
		ui:      &recordingUI{},
	}
	t.Cleanup(func() {
		if t.Failed() {
			h.logDiagnostics(t)
		}
		_ = exec.Command("docker", "rm", "-f", h.id).Run()
	})

	h.writeFile(t, remoteHome+"/.ssh/authorized_keys", authorized, 0o600)
	h.addr = publishedAddr(t, h.id, "22/tcp")
	waitFor(t, readyTimeout, "sshd greets on "+h.addr, func() error {
		return sshBanner(h.addr)
	})
	return h
}

// exec runs script with sh as the remote user and returns its stdout.
func (h *remoteHost) exec(t *testing.T, script string) string {
	t.Helper()
	return h.execAs(t, remoteUser, script)
}

// execRoot runs script with sh as root and returns its stdout.
func (h *remoteHost) execRoot(t *testing.T, script string) string {
	t.Helper()
	return h.execAs(t, "root", script)
}

func (h *remoteHost) execAs(t *testing.T, user, script string) string {
	t.Helper()
	out, err := exec.Command("docker", "exec", "-u", user, "-w", remoteHome,
		h.id, "sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("docker exec as %s: %v\nscript:\n%s\nstderr:\n%s", user, err, script, exitStderr(err))
	}
	return string(out)
}

// writeFile writes content to path as the remote user, creating its parent
// directories, so the user owns everything it creates.
func (h *remoteHost) writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	h.writeFileAs(t, remoteUser, path, content, mode)
}

func (h *remoteHost) writeFileAs(t *testing.T, user, path, content string, mode os.FileMode) {
	t.Helper()
	h.execAs(t, user, fmt.Sprintf("set -e\nmkdir -p \"$(dirname %[1]s)\"\n"+
		"printf %%s %[2]s | base64 -d > %[1]s\nchmod %[3]o %[1]s\n",
		shellQuote(path), shellQuote(base64.StdEncoding.EncodeToString([]byte(content))), mode))
}

// readFile returns the content of path on the remote host and whether it
// exists.
func (h *remoteHost) readFile(t *testing.T, path string) (string, bool) {
	t.Helper()
	out := h.exec(t, fmt.Sprintf("if [ -e %[1]s ]; then printf y; cat %[1]s; fi", shellQuote(path)))
	content, ok := strings.CutPrefix(out, "y")
	return content, ok
}

// logDiagnostics logs what explains a failure on the remote host: sshd's
// output, the rune server logs, configuration and terminal fragments, and
// what the SSH workspace notified.
func (h *remoteHost) logDiagnostics(t *testing.T) {
	logs, _ := exec.Command("docker", "logs", h.id).CombinedOutput()
	t.Logf("container %s logs:\n%s", h.id, tail(string(logs), 16<<10))
	const script = `for f in ` + remoteHome + `/.rune*/server.log ` +
		remoteHome + `/.rune*/config.yaml ` +
		remoteHome + `/.rune*/shellrc/env.sh ` +
		remoteHome + `/.rune*/shellrc/env.fish; do
	[ -e "$f" ] || continue
	echo "==> $f"
	tail -c 16384 "$f"
done
echo "==> processes"
for p in /proc/[0-9]*; do
	tr '\0' ' ' < "$p/cmdline" 2>/dev/null && echo
done`
	out, err := exec.Command("docker", "exec", "-u", "root", h.id, "sh", "-c", script).CombinedOutput()
	if err != nil {
		t.Logf("collect remote diagnostics: %v", err)
	}
	t.Logf("remote diagnostics:\n%s", out)
	if notes := h.ui.notifications(); len(notes) > 0 {
		t.Logf("ssh workspace notifications:\n%s", strings.Join(notes, "\n"))
	}
}

// newClientKey writes a fresh ed25519 private key for the SSH client and
// returns its path and the authorized_keys line that accepts it.
func newClientKey(t *testing.T) (string, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ssh key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal ssh key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write ssh key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh public key: %v", err)
	}
	return path, string(ssh.MarshalAuthorizedKey(sshPub))
}

// publishedAddr returns the loopback address docker published port on.
func publishedAddr(t *testing.T, id, port string) string {
	t.Helper()
	out, err := exec.Command("docker", "port", id, port).Output()
	if err != nil {
		t.Fatalf("docker port %s: %v: %s", port, err, exitStderr(err))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(line, "127.0.0.1:") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("docker published %s on no loopback address: %q", port, out)
	return ""
}

// sshBanner succeeds once a server greets on addr. Docker's port proxy
// accepts connections before the container listens, so connecting is not
// enough.
func sshBanner(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	if _, err := conn.Read(buf); err != nil {
		return fmt.Errorf("read banner: %w", err)
	}
	if string(buf) != "SSH-" {
		return fmt.Errorf("unexpected banner %q", buf)
	}
	return nil
}

// waitFor polls cond until it succeeds, failing t with the last error once
// timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() error) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := cond()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting until %s: %v", timeout, what, err)
		}
		time.Sleep(pollInterval)
	}
}

// recordingUI fails every SSH authentication prompt, since the suite only
// authenticates with keys, and keeps the notifications for the diagnostics.
// It never logs to t: the workspace notifies from goroutines that may outlive
// the test.
type recordingUI struct {
	mu    sync.Mutex
	notes []string
}

func (u *recordingUI) PromptSecret(context.Context, string) (string, error) {
	return "", errors.New("unexpected secret prompt")
}

func (u *recordingUI) PromptText(context.Context, string, string) (string, error) {
	return "", errors.New("unexpected text prompt")
}

func (u *recordingUI) PromptChoice(context.Context, string, []string) (int, error) {
	return -1, errors.New("unexpected choice prompt")
}

func (u *recordingUI) Notify(_ workspacessh.NotificationLevel, msg string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.notes = append(u.notes, msg)
}

func (u *recordingUI) notifications() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.notes...)
}

// syncBuffer is a bytes.Buffer that remote output can be written to from
// the workspace's goroutines while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

func exitStderr(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(exitErr.Stderr)
	}
	return ""
}

// repoRoot walks up from this file to the directory holding go.mod.
func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", errors.New("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", filepath.Dir(file))
		}
		dir = parent
	}
}
