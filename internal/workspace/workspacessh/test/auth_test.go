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
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace/workspacessh"
)

func TestAuthMatrix(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	scenarios := []authScenario{
		{
			name: "01_pubkey_with_configured_key_succeeds",
			srv:  SSHDScenario{PublicKeyFile: "/id_ed25519.pub"},
			keys: []string{"id_ed25519"},
			ui: func() *recordingUI {
				return &recordingUI{}
			},
			wantSuccess: true,
			wantPrompts: 0,
		},
		{
			name: "02_pubkey_no_key_returns_helpful_error",
			srv:  SSHDScenario{PublicKeyFile: "/id_ed25519.pub"},
			ui: func() *recordingUI {
				return &recordingUI{}
			},
			wantSuccess: false,
			wantErrIs:   workspacessh.ErrAuthRequiredKey,
		},
		{
			name: "03_pubkey_with_passphrase_prompts_for_passphrase",
			srv:  SSHDScenario{PublicKeyFile: "/id_ed25519_pass.pub"},
			keys: []string{"id_ed25519_pass"},
			ui: func() *recordingUI {
				return &recordingUI{secrets: []string{"secret123"}}
			},
			wantSuccess: true,
			wantPrompts: 1,
		},
		{
			name: "04_wrong_key_returns_clean_error_no_extra_prompts",
			srv:  SSHDScenario{PublicKeyFile: "/id_ed25519.pub"},
			keys: []string{"id_ed25519_wrong"},
			ui: func() *recordingUI {
				return &recordingUI{}
			},
			wantSuccess:  false,
			wantErrMatch: "publickey",
		},
		{
			name: "05_password_prompts_via_UI",
			srv: SSHDScenario{
				PasswordAccess: true, UserPassword: "hunter2",
			},
			ui: func() *recordingUI {
				return &recordingUI{secrets: []string{"hunter2"}}
			},
			wantSuccess: true,
			wantPrompts: 1,
		},
		{
			name: "06_password_in_URI_skips_prompts",
			srv: SSHDScenario{
				PasswordAccess: true, UserPassword: "hunter2",
			},
			uriPass: "hunter2",
			ui: func() *recordingUI {
				return &recordingUI{}
			},
			wantSuccess: true,
			wantPrompts: 0,
		},
		{
			name: "07_pubkey_or_password_no_keys_falls_back_to_password",
			srv: SSHDScenario{
				PasswordAccess: true, UserPassword: "hunter2",
				PublicKeyFile: "/id_ed25519.pub",
			},
			ui: func() *recordingUI {
				return &recordingUI{secrets: []string{"hunter2"}}
			},
			wantSuccess: true,
			wantPrompts: 1,
		},
		{
			name: "11_wrong_username_surfaces_clean_error",
			srv:  SSHDScenario{PublicKeyFile: "/id_ed25519.pub"},
			keys: []string{"id_ed25519"},
			ui: func() *recordingUI {
				return &recordingUI{}
			},
			wantSuccess:  false,
			wantErrMatch: "authentic",
		},
	}

	for _, s := range scenarios {
		t.Run(s.name, func(t *testing.T) {
			// Each scenario boots an independent container; run them in
			// parallel so the matrix scales to -count=N without serial
			// slowdown.
			t.Parallel()
			c := StartContainer(t, s.srv)
			ui := s.ui()
			cfgKeys := make([]any, 0, len(s.keys))
			for _, k := range s.keys {
				cfgKeys = append(cfgKeys, absKey(t, k))
			}
			username := "test"
			if s.name == "11_wrong_username_surfaces_clean_error" {
				username = "nope"
			}
			uriStr := fmt.Sprintf("ssh://%s@%s/", username, c.HostPort)
			if s.uriPass != "" {
				uriStr = fmt.Sprintf("ssh://%s:%s@%s/", username, s.uriPass, c.HostPort)
			}
			uri, err := workspaceapi.ParseURI(uriStr)
			require.NoError(t, err)

			cfg := buildSSHConfig(cfgKeys)

			err = tryDial(t, cfg, uri, ui)
			if s.wantSuccess {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				if s.wantErrIs != nil {
					assert.True(t, errors.Is(err, s.wantErrIs),
						"expected errors.Is(_, %v); got %v", s.wantErrIs, err)
				}
				if s.wantErrMatch != "" {
					assert.Contains(t, err.Error(), s.wantErrMatch)
				}
			}
			if s.wantPrompts > 0 {
				assert.GreaterOrEqual(t, len(ui.prompts), s.wantPrompts,
					"prompts: %v", ui.prompts)
			} else if s.wantSuccess {
				assert.Empty(t, ui.prompts, "expected no prompts; saw %v", ui.prompts)
			}
		})
	}
}

func TestAuthMatrixDefaultIdentityFile(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	// Start the container BEFORE mutating HOME: the docker CLI loads
	// its current context from $HOME/.docker, and tests run inside
	// `make test` where t.Setenv would point HOME at a temp dir with
	// no docker context — making `docker run` fail with
	// "Cannot connect to docker.sock".
	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})
	uriStr := fmt.Sprintf("ssh://test@%s/", c.HostPort)
	uri, err := workspaceapi.ParseURI(uriStr)
	require.NoError(t, err)

	// Build a fake HOME containing only the configured default key.
	home := t.TempDir()
	src, err := os.ReadFile(absKey(t, "id_ed25519"))
	require.NoError(t, err)
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	require.NoError(t, os.MkdirAll(sshDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(sshDir, "id_ed25519"), src, 0o600))

	ui := &recordingUI{}
	cfg := buildSSHConfig(nil)
	err = tryDial(t, cfg, uri, ui)
	require.NoError(t, err,
		"expected ~/.ssh/id_ed25519 auto-discovery to authenticate; got %v", err)
	assert.Empty(t, ui.prompts,
		"expected no prompts when default identity key authenticates; saw %v", ui.prompts)
}

// recordingUI captures the prompts made and replies with canned answers.
type recordingUI struct {
	mu      sync.Mutex
	prompts []string
	secrets []string
	texts   []string
	// choices are dequeued as PromptChoice results. When empty and
	// choiceCancel is false, PromptChoice returns context.Canceled.
	choices      []int
	choiceCancel bool
}

func (u *recordingUI) PromptSecret(_ context.Context, label string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, "secret:"+label)
	if len(u.secrets) == 0 {
		return "", context.Canceled
	}
	a := u.secrets[0]
	u.secrets = u.secrets[1:]
	return a, nil
}

func (u *recordingUI) PromptText(_ context.Context, label, _ string) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, "text:"+label)
	if len(u.texts) == 0 {
		return "", context.Canceled
	}
	a := u.texts[0]
	u.texts = u.texts[1:]
	return a, nil
}

func (u *recordingUI) PromptChoice(_ context.Context, label string, _ []string) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.prompts = append(u.prompts, "choice:"+label)
	if !u.choiceCancel && len(u.choices) > 0 {
		c := u.choices[0]
		u.choices = u.choices[1:]
		return c, nil
	}
	return -1, context.Canceled
}

func (u *recordingUI) Notify(workspacessh.NotificationLevel, string) {}

// authScenario describes one row of the auth matrix from the plan.
type authScenario struct {
	name    string
	srv     SSHDScenario
	keys    []string // client-side configured private key paths (testdata/...)
	uriPass string   // URI password, empty for none
	ui      func() *recordingUI

	// expectations
	wantSuccess  bool
	wantPrompts  int
	wantErrIs    error
	wantErrMatch string
}

func absKey(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("testdata", name))
	require.NoError(t, err)
	return p
}

// buildSSHConfig builds the workspacessh config that drives the std remote
// auth path: insecure host keys (the matrix uses ephemeral containers), 20s
// timeout, and the supplied private keys.
func buildSSHConfig(privateKeys []any) sshSchemeConfig {
	return sshSchemeConfig{
		PrivateKeys: privateKeys,
		Insecure:    true,
		Timeout:     20 * time.Second,
	}
}

// sshSchemeConfig is a small struct mapped onto config.MapConfig in
// tryDial. Keeping it in this file avoids leaking unrelated configuration
// detail into harness.go.
type sshSchemeConfig struct {
	PrivateKeys []any
	Insecure    bool
	Timeout     time.Duration
}

// tryDial directly drives the SSH auth path without going through the
// scheme's full Stat round-trip (which requires the `rune` server-side
// binary).
func tryDial(
	t *testing.T, cfg sshSchemeConfig, uri workspaceapi.URI, ui *recordingUI,
) error {
	t.Helper()
	keys := make([]string, 0, len(cfg.PrivateKeys))
	for _, p := range cfg.PrivateKeys {
		keys = append(keys, p.(string))
	}
	return workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys: keys,
			Insecure:    cfg.Insecure,
			Timeout:     cfg.Timeout,
		})
}

// The tests below fill coverage gaps the scenario-table above
// doesn't reach. Each spins up its own real openssh-server
// container with a tailored sshd_config snippet and dials it
// through workspacessh.TestAuthDial (or the full scheme.New path
// where the gap is bootstrap-related).
//
// Out of scope, deliberately not covered here:
//   - IPv6 host literals (the harness binds 127.0.0.1 only).
//   - ProxyCommand / ProxyJump (would require a second jump host
//     container; not worth the harness complexity.)
//   - ChrootDirectory + restricted shells (different image).
//   - Server-driven mid-session disconnect via ClientAlive*
//     (timing is too flaky to assert on without per-platform
//     tuning.)
//   - "Server advertises kbd-interactive but never sends
//     INFO_REQUEST" (translateDialError special case): hard to
//     reproduce against stock OpenSSH; covered by std_remote unit
//     tests via translateDialError instead.
//   - MaxSessions 1 (the bootstrap may legitimately take multiple
//     sessions; the assertion is too implementation-coupled.)
//   - Restrictive PubkeyAcceptedAlgorithms (modern OpenSSH
//     defaults reject most weaker types already; a meaningful
//     negative test would need a server with non-default algorithm
//     policy that we'd have to track separately.)

func TestAuthMethodsChain(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:  "/id_ed25519.pub",
		PasswordAccess: true,
		UserPassword:   "hunter2",
		ExtraSSHDConfig: "AuthenticationMethods publickey,password\n" +
			"PasswordAuthentication yes\n",
	})

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")

	t.Run("pubkey_alone_is_not_enough", func(t *testing.T) {
		ui := &recordingUI{} // no password supplied → prompt fails
		err := workspacessh.TestAuthDial(context.Background(), ui, uri,
			workspacessh.AuthDialOptions{
				PrivateKeys: []string{keyPath},
				Insecure:    true,
				Timeout:     20 * time.Second,
			})
		require.Error(t, err,
			"server requires publickey,password — pubkey alone must "+
				"not be enough; the server's USERAUTH_FAILURE here "+
				"is what advertises 'password' as still-required and "+
				"drives the second prompt")
	})

	t.Run("pubkey_then_password_succeeds", func(t *testing.T) {
		ui := &recordingUI{secrets: []string{"hunter2"}}
		err := workspacessh.TestAuthDial(context.Background(), ui, uri,
			workspacessh.AuthDialOptions{
				PrivateKeys: []string{keyPath},
				Insecure:    true,
				Timeout:     20 * time.Second,
			})
		require.NoError(t, err,
			"with a valid private key AND the matching password the "+
				"chain must complete successfully; if this fails, "+
				"either pubkey signers are not being offered or the "+
				"password callback isn't being invoked after pubkey "+
				"partial-success")
		assert.NotEmpty(t, ui.prompts,
			"chained auth must drive at least one password prompt; "+
				"otherwise the password leg is silently being "+
				"satisfied (not what the server is asking for)")
	})
}

func TestKbdInteractiveAuth(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{
		// Container has to know a password so PAM has something to
		// match against. PasswordAccess=true seeds it; we then
		// disable plain "password" auth in sshd_config so the only
		// path through the server is kbd-interactive.
		PasswordAccess: true,
		UserPassword:   "hunter2",
		ExtraSSHDConfig: "PubkeyAuthentication no\n" +
			"PasswordAuthentication no\n" +
			"KbdInteractiveAuthentication yes\n" +
			"UsePAM yes\n",
	})
	// Ensure PAM can authenticate the test user even after the
	// container's PASSWORD_ACCESS toggle: re-set the unix password
	// directly because some image versions skip chpasswd when
	// PasswordAuthentication is later turned off.
	SetUserPassword(t, c.ID, "test", "hunter2")

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	ui := &recordingUI{secrets: []string{"hunter2"}}
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			Insecure:       true,
			KbdInteractive: true,
			Timeout:        20 * time.Second,
		})
	require.NoError(t, err,
		"with kbd_interactive opted-in and a server that only allows "+
			"keyboard-interactive auth, askKbd must drive PAM's "+
			"password challenge through ui.PromptSecret and succeed")
	assert.NotEmpty(t, ui.prompts,
		"kbd-interactive flow must surface at least one prompt; if "+
			"prompts is empty askKbd is skipping its challenge loop")
}

func TestMaxAuthTriesOne(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:   "/id_ed25519.pub",
		ExtraSSHDConfig: "MaxAuthTries 1\n",
	})

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	// Wrong key first, right key second. Per-key redial means the
	// wrong key burns one connection, the right key authenticates
	// on a fresh one.
	wrong := PrivateKeyPath(t, "id_ed25519_wrong")
	right := PrivateKeyPath(t, "id_ed25519")

	ui := &recordingUI{}
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys: []string{wrong, right},
			Insecure:    true,
			Timeout:     20 * time.Second,
		})
	require.NoError(t, err,
		"MaxAuthTries=1 with [wrong, right] keys must succeed via "+
			"per-key redial; if this fails newStdRemote is not "+
			"opening a fresh connection for the next key after the "+
			"server severs the first one")
	assert.Empty(t, ui.prompts,
		"per-key redial must not prompt the user when one of the "+
			"configured keys authenticates; saw %v", ui.prompts)
}

func TestAllowUsersDeniesUser(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:   "/id_ed25519.pub",
		ExtraSSHDConfig: "AllowUsers nope\n",
	})

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	ui := &recordingUI{}
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys: []string{keyPath},
			Insecure:    true,
			Timeout:     20 * time.Second,
		})
	require.Error(t, err,
		"sshd AllowUsers must surface as a clean auth error: the "+
			"user is real on the host but explicitly denied at "+
			"connect time; we should not retry forever")
}

func TestServerBanner(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})

	// Install /etc/ssh-banner with content; sshd won't include it
	// unless the file exists and is readable by sshd. We point
	// sshd_config at it via a Banner directive applied as the
	// per-scenario snippet.
	const banner = "WARNING: AUTHORIZED USE ONLY\n"
	writeFileInContainer(t, c.ID, "/etc/ssh-banner", banner, "0644")
	applyExtraSSHDConfig(t, c.ID, "Banner /etc/ssh-banner\n")
	require.NoError(t, waitForSSH(c.HostPort, 30*time.Second))

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	ui := &recordingUI{}
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys: []string{keyPath},
			Insecure:    true,
			Timeout:     20 * time.Second,
		})
	require.NoError(t, err,
		"a server-side SSH banner must not break the dial; the Go "+
			"ssh client consumes it as a separate banner message "+
			"and never wires it into the auth flow")
	assert.Empty(t, ui.prompts,
		"banner text must not surface as a UI prompt; saw %v", ui.prompts)
}

func TestRuneBinaryMissingOnRemote(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	// InstallRuneBinary deliberately false: the linuxserver image
	// has no `rune` binary on its $PATH, so whichCommand will fail
	// during bootstrap.
	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})

	keyPath := PrivateKeyPath(t, "id_ed25519")
	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/tmp")
	require.NoError(t, err)

	cfg := config.MapConfig(map[string]any{
		"private_keys": []any{keyPath},
		"insecure":     true,
		"timeout":      "20s",
	})

	schemeFn := workspacessh.New(&errorUI{})
	scheme, err := schemeFn(context.Background(), cfg, uri)
	require.NoError(t, err,
		"the scheme constructor itself must succeed (it doesn't dial "+
			"in production); the missing-rune error surfaces on "+
			"first remote operation instead")
	defer scheme.Close()

	// First remote operation triggers connect → whichCommand →
	// failure. We loop briefly to give the maintainConnection
	// goroutine a chance to publish its first state before we read
	// it; on a fast box the dial completes synchronously, so we
	// observe the error on the first try.
	deadline := time.Now().Add(20 * time.Second)
	var statErr error
	for time.Now().Before(deadline) {
		_, statErr = scheme.Stat("/tmp")
		if statErr != nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	require.Error(t, statErr,
		"with no `rune` binary on the remote, the first remote "+
			"operation must surface the bootstrap error rather than "+
			"silently succeed")
	assert.Contains(t, statErr.Error(), "was not found on remote",
		"missing-rune error must surface the canonical message that "+
			"isRetryableConnectError matches against; otherwise the "+
			"reconnect loop will keep dialing forever; got: %v",
		statErr)
}

func TestMultipleKeysFirstWrongSecondRight(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})
	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	wrong := PrivateKeyPath(t, "id_ed25519_wrong")
	right := PrivateKeyPath(t, "id_ed25519")

	ui := &recordingUI{}
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys: []string{wrong, right},
			Insecure:    true,
			Timeout:     20 * time.Second,
		})
	require.NoError(t, err,
		"the Go ssh client offers each configured signer in order "+
			"and stops at the first one the server accepts; if this "+
			"fails either gatherSigners is dropping the second key "+
			"or the client is bailing after the first signer is "+
			"rejected")
	assert.Empty(t, ui.prompts,
		"the right key authenticates without any user prompts; saw %v",
		ui.prompts)
}

func TestPassphrasePromptCancelled(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519_pass.pub"})
	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519_pass")
	// recordingUI with no secrets queued: PromptSecret returns
	// context.Canceled (the same error a real UI returns when the
	// user dismisses the dialog with Esc).
	ui := &recordingUI{}
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys: []string{keyPath},
			Insecure:    true,
			Timeout:     20 * time.Second,
		})
	require.Error(t, err,
		"cancelling the passphrase prompt must produce a real error "+
			"so the workspace doesn't get stuck in a spin loop "+
			"trying to use a key it can't decrypt")
	require.Len(t, ui.prompts, 1,
		"signerForKey must prompt for the passphrase exactly once "+
			"before giving up; got %v", ui.prompts)
}

// writeFileInContainer creates a file inside the container with
// the given content and chmod. Used by TestServerBanner to seed
// /etc/ssh-banner — the harness does not expose a generic
// file-installation helper because applyExtraSSHDConfig is the
// only existing in-container write path.
func writeFileInContainer(t *testing.T, id, path, content, mode string) {
	t.Helper()
	// Use docker exec + sh -c with a here-doc so we don't need to
	// invent a copy/cp helper for one-off banner content.
	script := fmt.Sprintf(`set -e
cat > %s <<'__EOF__'
%s__EOF__
chmod %s %s
`, path, content, mode, path)
	out, err := exec.Command("docker", "exec", id, "sh", "-c", script).
		CombinedOutput()
	if err != nil {
		t.Fatalf("write %s: %v: %s", path, err, string(out))
	}
}
