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

package workspacetest

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace/workspacessh"
)

func TestIntegrationTerminalShell(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
	})

	// Stage a wrapper login shell that simulates the bug: it
	// advertises a non-existent SHELL path before exec'ing the
	// real shell. usermod the test user to use it so every
	// subsequent SSH session — including the one that launches
	// runesvc — inherits the broken $SHELL.
	const wrapperPath = "/usr/local/bin/rune-test-loginshell"
	const bogusShell = "/usr/bin/bash" // not present on this image
	installLoginShellWrapper(t, c.ID, wrapperPath, bogusShell)
	chshUser(t, c.ID, "test", wrapperPath)

	keyPath := PrivateKeyPath(t, "id_ed25519")

	cfgs := map[string]config.Config{
		"openssh_proc_remote": config.MapConfig(map[string]any{
			"command": "ssh -o StrictHostKeyChecking=no -i " + keyPath +
				" %h -p %p",
			"timeout": "20s",
		}),
		"go_stdlib_remote": config.MapConfig(map[string]any{
			"private_keys": []any{keyPath},
			"timeout":      "20s",
			"insecure":     true,
		}),
	}

	for desc, cfg := range cfgs {
		t.Run(desc, func(t *testing.T) {
			t.Run("empty Path resolves to a remote shell that exists", func(t *testing.T) {
				s := newSchemeIntegration(t, c.HostPort, cfg)

				// Empty Path AND empty Args: the protocol contract
				// vte.Component sends when the user hasn't
				// configured terminal.shell. We use `-c "..."` via
				// non-empty Args only so the shell exits cleanly;
				// the resolution we care about (Path) is still left
				// to the executor.
				var stdout bytes.Buffer
				ch := make(chan error, 1)
				cmd := workspaceapi.Cmd{
					// Path intentionally empty.
					Args:    []string{"-c", "echo terminal-ok"},
					Stdout:  &stdout,
					Watcher: workspaceapi.ChanProcessWatcher(ch),
				}

				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				_, err := s.StartCommand(ctx, cmd)
				require.NoError(t, err,
					"empty-Path StartCommand must succeed: that's "+
						"the protocol contract vte.Component relies "+
						"on for terminal open over ssh")

				select {
				case err := <-ch:
					require.NoError(t, err,
						"shell launched by empty-Path Cmd must "+
							"exit cleanly; got %v, stdout=%q",
						err, stdout.String())
				case <-ctx.Done():
					t.Fatalf("timed out waiting for shell to exit; "+
						"stdout so far: %q", stdout.String())
				}

				assert.Equal(t, "terminal-ok\n", stdout.String(),
					"shell resolved on the remote should run our "+
						"-c command; if this is empty the executor "+
						"likely fell through to a non-existent "+
						"binary and silently swallowed the error")
			})

			t.Run("empty Path with empty Args boots a login shell", func(t *testing.T) {
				// Same as above but the *true* terminal-open
				// contract: empty Path AND empty Args. We feed the
				// shell an `exit` via stdin so it doesn't hang in
				// interactive mode (--login -i).
				s := newSchemeIntegration(t, c.HostPort, cfg)

				var stdout bytes.Buffer
				ch := make(chan error, 1)
				cmd := workspaceapi.Cmd{
					// Both empty: protocol contract for "use the
					// remote's login shell".
					Stdin:   strings.NewReader("echo login-shell-ok\nexit\n"),
					Stdout:  &stdout,
					Watcher: workspaceapi.ChanProcessWatcher(ch),
				}

				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()

				_, err := s.StartCommand(ctx, cmd)
				require.NoError(t, err)

				select {
				case err := <-ch:
					// The login shell may exit non-zero on EOF in
					// some configurations; what we care about is
					// that fork/exec succeeded and the shell ran
					// our command. Surface the error in the
					// failure message but don't fail solely on it.
					t.Logf("shell exit: %v", err)
				case <-ctx.Done():
					t.Fatalf("timed out waiting for shell; stdout=%q",
						stdout.String())
				}

				assert.Contains(t, stdout.String(), "login-shell-ok",
					"empty-Path empty-Args Cmd should boot a real "+
						"login shell on the remote and run echo from "+
						"stdin; got %q", stdout.String())
			})

			t.Run("pty + Setctty under empty-Path login shell", func(t *testing.T) {
				// Reproduces the vte.Component code path end-to-
				// end: allocate a remote pty, attach Slave to
				// stdin/stdout/stderr, set Setsid+Setctty, and let
				// the executor resolve the empty Path into the
				// remote's login shell.
				//
				// Regression: when an extension runner inadvertently
				// became the vte's executor, the pty's *remoteFile
				// was sent through a *local* fileScheme that didn't
				// recognize the fd, so it materialized as a plain
				// pipe. Setctty then failed with "inappropriate
				// ioctl for device" because the kernel was asked to
				// install a controlling tty on a non-tty fd.
				s := newSchemeIntegration(t, c.HostPort, cfg)

				ctx, cancel := context.WithTimeout(
					context.Background(), 30*time.Second)
				defer cancel()

				pty, err := s.NewPty(ctx)
				require.NoError(t, err, "remote NewPty must succeed")
				t.Cleanup(func() {
					_ = pty.Master.Close()
					_ = pty.Slave.Close()
				})

				// Drain master so the shell isn't blocked on a
				// full pipe; collect output until the shell exits.
				var output atomic.Value
				output.Store([]byte(nil))
				done := make(chan struct{})
				go func() {
					defer close(done)
					var buf bytes.Buffer
					_, _ = buf.ReadFrom(pty.Master)
					output.Store(buf.Bytes())
				}()

				ch := make(chan error, 1)
				cmd := workspaceapi.Cmd{
					// Empty Path + empty Args: same as vte sends.
					SysProcAttr: &syscall.SysProcAttr{
						Setsid:  true,
						Setctty: true,
					},
					Stdin:   pty.Slave,
					Stdout:  pty.Slave,
					Stderr:  pty.Slave,
					Watcher: workspaceapi.ChanProcessWatcher(ch),
				}

				_, err = s.StartCommand(ctx, cmd)
				require.NoError(t, err,
					"pty-attached empty-Path StartCommand must "+
						"succeed: this is exactly what "+
						"vte.Component sends when the user opens a "+
						"terminal in an SSH workspace")

				// Send a command + exit through the pty. The shell
				// runs `echo pty-ok` and exits; the read goroutine
				// drains output until EOF.
				_, err = pty.Master.Write([]byte("echo pty-ok\nexit\n"))
				require.NoError(t, err)

				select {
				case <-ch:
				case <-ctx.Done():
					t.Fatalf("timed out waiting for pty shell to exit")
				}
				_ = pty.Slave.Close()
				<-done

				out := string(output.Load().([]byte))
				assert.Contains(t, out, "pty-ok",
					"shell launched under a remote pty should run "+
						"our echo command; got %q", out)
			})
		})
	}
}

// installLoginShellWrapper writes a tiny `sh` wrapper inside the
// container that exports SHELL=<bogus> before exec'ing /bin/sh with
// the original args. We use it to simulate the production bug where
// the remote host advertises a $SHELL that fork/exec can't find.
//
// We use `docker cp` instead of piping into `docker exec` because
// `docker exec` does not attach stdin unless `-i` is set, and adding
// `-i` here doesn't reliably propagate EOF on macOS docker desktop —
// `cat > <path>` would hang.
func installLoginShellWrapper(t *testing.T, id, dst, bogus string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"export SHELL=" + bogus + "\n" +
		"exec /bin/sh \"$@\"\n"
	tmp := filepath.Join(t.TempDir(), "rune-test-loginshell")
	if err := os.WriteFile(tmp, []byte(script), 0o755); err != nil {
		t.Fatalf("write wrapper: %v", err)
	}
	if out, err := exec.Command("docker", "cp", tmp, id+":"+dst).
		CombinedOutput(); err != nil {
		t.Fatalf("docker cp wrapper: %v: %s", err, string(out))
	}
	if out, err := exec.Command("docker", "exec", id, "chmod", "0755", dst).
		CombinedOutput(); err != nil {
		t.Fatalf("chmod wrapper: %v: %s", err, string(out))
	}
}

// chshUser changes the login shell of the given container user.
// /etc/passwd has 7 colon-separated fields; the last is the login
// shell. We rewrite it with sed to avoid depending on chsh/usermod
// availability or PAM rules in the test image.
func chshUser(t *testing.T, id, user, shell string) {
	t.Helper()
	script := "set -e\n" +
		"sed -i 's|^\\(" + user + ":[^:]*:[^:]*:[^:]*:[^:]*:[^:]*:\\).*$|\\1" +
		shell + "|' /etc/passwd\n" +
		"getent passwd " + user + "\n"
	out, err := exec.Command("docker", "exec", id, "sh", "-c", script).
		CombinedOutput()
	if err != nil {
		t.Fatalf("chsh %s -> %s: %v: %s", user, shell, err, string(out))
	}
	t.Logf("chsh %s -> %s; passwd line: %s", user, shell,
		strings.TrimSpace(string(out)))
}

func TestIntegrationTerminalLoginShellAppliesRuneEnvironment(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
	})
	chshUser(t, c.ID, "test", "/bin/bash")

	// The image's /etc/profile assigns PATH outright, as Debian's does, and
	// the user's profile overrides a variable that gui.env also sets. The
	// runesvc stand-in reads gui.env from gui_env in the data directory.
	const setup = `set -e
home="$(getent passwd test | cut -d: -f6)"
mkdir -p "$home/.rune"
printf '%s\n' 'RUNE_E2E_VAR=from-gui-env' 'PATH=$RUNE_DATADIR/tools/bin:$PATH' \
	> "$home/.rune/gui_env"
printf '%s\n' 'export RUNE_E2E_VAR=from-profile' 'export RUNE_E2E_PROFILE=read' \
	> "$home/.profile"
chown -R test "$home/.rune" "$home/.profile"
printf %s "$home"`
	out, err := exec.Command("docker", "exec", c.ID, "sh", "-c", setup).Output()
	require.NoError(t, err, "set up gui_env and profile: %s", exitStderr(err))
	dataDir := string(out) + "/.rune"

	keyPath := PrivateKeyPath(t, "id_ed25519")
	s := newSchemeIntegration(t, c.HostPort, config.MapConfig(map[string]any{
		"private_keys": []any{keyPath},
		"timeout":      "20s",
		"insecure":     true,
	}))

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	pty, err := s.NewPty(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = pty.Master.Close()
		_ = pty.Slave.Close()
	})
	var output bytes.Buffer
	done := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(done)
		_, _ = output.ReadFrom(pty.Master)
	})

	ch := make(chan error, 1)
	_, err = s.StartCommand(ctx, workspaceapi.Cmd{
		SysProcAttr: &syscall.SysProcAttr{Setsid: true, Setctty: true},
		Stdin:       pty.Slave,
		Stdout:      pty.Slave,
		Stderr:      pty.Slave,
		Watcher:     workspaceapi.ChanProcessWatcher(ch),
	})
	require.NoError(t, err)

	// The terminal echoes this input, so the assertions below match only
	// the expanded values, never the literal variable references.
	_, err = pty.Master.Write([]byte(
		`echo "PATH<$PATH>" "VAR<$RUNE_E2E_VAR>" "PROFILE<$RUNE_E2E_PROFILE>"; exit` + "\n"))
	require.NoError(t, err)

	select {
	case <-ch:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for the terminal shell to exit")
	}
	_ = pty.Slave.Close()
	<-done

	got := output.String()
	assert.Contains(t, got, "PROFILE<read>",
		"the login shell must read the user's profile; got %q", got)
	assert.Contains(t, got, "VAR<from-gui-env>",
		"gui.env must win over the user's profile; got %q", got)
	assert.Contains(t, got, "PATH<"+dataDir+"/bin:"+dataDir+"/tools/bin:",
		"Rune's data dir and the gui.env PATH entries must lead the PATH "+
			"that /etc/profile assigned; got %q", got)
	assert.Contains(t, got, ":/usr/bin:",
		"the PATH /etc/profile assigned must follow; got %q", got)
}

func TestIntegrationTerminalSurvivesKeepaliveIdle(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
	})

	keyPath := PrivateKeyPath(t, "id_ed25519")

	uri, err := workspaceapi.ParseURI(
		fmt.Sprintf("ssh://test@%s/tmp", c.HostPort))
	require.NoError(t, err)

	cfg := config.MapConfig(map[string]any{
		"private_keys": []any{keyPath},
		"timeout":      "20s",
		"insecure":     true,
	})

	scheme, err := workspacessh.New(errorUI{})(context.Background(), cfg, uri)
	require.NoError(t, err)
	defer scheme.Close()

	// Drive the initial connect before opening the long-lived stream we
	// care about.
	deadline := time.Now().Add(20 * time.Second)
	var fi any
	for time.Now().Before(deadline) {
		fi, err = scheme.Stat("/tmp")
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err, "initial connect must complete the bootstrap")
	require.NotNil(t, fi)

	serverBefore := remoteServerPID(t, scheme)

	// Open a long-lived remote process. StartCommand is a server-
	// streaming RPC, so this keeps a gRPC stream active with no data
	// flowing — exactly the shape of an idle terminal. We cancel it via
	// ctx at the end of the test.
	streamCtx, cancelStream := context.WithCancel(t.Context())
	defer cancelStream()

	exitCh := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    "sleep",
		Args:    []string{"120"},
		Watcher: workspaceapi.ChanProcessWatcher(exitCh),
	}
	_, err = scheme.StartCommand(streamCtx, cmd)
	require.NoError(t, err,
		"opening the long-lived stream must succeed on the first "+
			"connection")

	// Hold the stream idle past the strike threshold. The client pings
	// at 10s; the unenforced server strikes each ping (MinTime 5m) and
	// GOAWAYs on the 3rd (~30-40s after the stream opened). 45s gives a
	// safe margin.
	const idle = 45 * time.Second
	select {
	case err := <-exitCh:
		t.Fatalf("the idle terminal stream must stay open across the "+
			"keepalive window, but the process watcher reported an early "+
			"exit: %v — this is the too_many_pings GOAWAY tearing down "+
			"the transport", err)
	case <-time.After(idle):
	}

	// The connection must still serve RPCs on the same session.
	fi, err = scheme.Stat("/tmp")
	require.NoError(t, err,
		"after ~45s with an open idle stream the SSH-tunneled "+
			"connection must still serve RPCs; a too_many_pings GOAWAY "+
			"would have killed it")
	require.NotNil(t, fi)

	assert.Equal(t, serverBefore, remoteServerPID(t, scheme),
		"no reconnect must have occurred during the idle window: a new "+
			"remote server means the connection was torn down "+
			"(too_many_pings) and maintainConnection re-dialed")
}

// remoteServerPID returns the pid of the remote workspace server, which
// is the parent of the commands it runs. Every connection starts a new
// server.
func remoteServerPID(t *testing.T, scheme schemeapi.Scheme) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	var stdout bytes.Buffer
	ch := make(chan error, 1)
	_, err := scheme.StartCommand(ctx, workspaceapi.Cmd{
		Path:    "sh",
		Args:    []string{"-c", "echo $PPID"},
		Stdout:  &stdout,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	})
	require.NoError(t, err)
	select {
	case err := <-ch:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatalf("timed out reading the remote server pid")
	}
	pid := strings.TrimSpace(stdout.String())
	require.NotEmpty(t, pid)
	return pid
}
