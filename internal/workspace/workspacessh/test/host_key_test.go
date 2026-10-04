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
	"path/filepath"
	"testing"
	"time"

	gitknownhosts "github.com/go-git/go-git/v6/plumbing/transport/ssh/knownhosts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"golang.org/x/crypto/ssh"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacessh"
)

// readTestdataFile reads testdata/<name> with an absolute path so
// the calling test does not rely on the cwd. Used by host-key tests
// to embed a different-but-valid ed25519 public key in known_hosts.
func readTestdataFile(t *testing.T, name string) []byte {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read %s: %v", abs, err)
	}
	return b
}

func TestHostKeyMatchingKnownHosts(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})
	khPath := WriteKnownHosts(t, c.HostPort, ContainerHostPubKeys(t, c.ID)...)

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	ui := &recordingUI{}
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys:    []string{keyPath},
			Insecure:       false,
			KnownHostsPath: khPath,
			Timeout:        20 * time.Second,
		})
	require.NoError(t, err,
		"dial against a server whose host key is in known_hosts must "+
			"succeed even with Insecure=false; if this fails the host "+
			"key callback is rejecting otherwise-valid keys")
	assert.Empty(t, ui.prompts,
		"matching-known_hosts dial must not prompt the user; saw %v", ui.prompts)
}

func TestHostKeyMismatchSurfacesErrHostKeyMismatch(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})

	// Pin a *different* but well-formed ed25519 public key so that
	// knownhosts.New parses the file and the dial reaches the actual
	// fingerprint comparison. We reuse id_ed25519.pub from testdata
	// (a checked-in client key) — it is a syntactically valid
	// ed25519 public key that happens not to match what the server
	// presents.
	wrong := readTestdataFile(t, "id_ed25519.pub")
	khPath := WriteKnownHosts(t, c.HostPort, wrong)

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	ui := &recordingUI{choiceCancel: true} // decline the changed key
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys:           []string{keyPath},
			Insecure:              false,
			KnownHostsPath:        khPath,
			StrictHostKeyChecking: true,
			Timeout:               20 * time.Second,
		})
	require.Error(t, err)
	assert.True(t, errors.Is(err, workspacessh.ErrHostKeyMismatch),
		"mismatched known_hosts must surface ErrHostKeyMismatch so "+
			"isRetryableConnectError stops the reconnect loop and we "+
			"never silently roll past a possible MITM; got %v", err)
	for _, p := range ui.prompts {
		assert.NotContains(t, p, "secret:",
			"a declined host-key change must never fall through to a "+
				"credential prompt — that would let an attacker harvest "+
				"credentials; saw %v", ui.prompts)
	}
}

func TestHostKeyChangedBetweenDials(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})
	khPath := WriteKnownHosts(t, c.HostPort, ContainerHostPubKeys(t, c.ID)...)

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	opts := workspacessh.AuthDialOptions{
		PrivateKeys:           []string{keyPath},
		Insecure:              false,
		KnownHostsPath:        khPath,
		StrictHostKeyChecking: true,
		Timeout:               20 * time.Second,
	}

	// First dial: original host key. Must succeed.
	require.NoError(t, workspacessh.TestAuthDial(
		context.Background(), &recordingUI{}, uri, opts),
		"baseline dial against the original host key must succeed; "+
			"the rotation half of this test is meaningless if the "+
			"first dial already fails")

	// Rotate every host key on the server. known_hosts on disk is
	// untouched, so the next dial must observe the mismatch
	// regardless of which algorithm HostKeyAlgorithms negotiates.
	RegenerateContainerHostKeys(t, c.ID)
	if err := waitForSSH(c.HostPort, 30*time.Second); err != nil {
		t.Fatalf("sshd did not come back after host key rotation: %v", err)
	}

	err = workspacessh.TestAuthDial(context.Background(),
		&recordingUI{choiceCancel: true}, uri, opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, workspacessh.ErrHostKeyMismatch),
		"after the server rotates its host key, a re-dial against "+
			"the same known_hosts entry must surface "+
			"ErrHostKeyMismatch — otherwise users would silently "+
			"connect to a server they no longer trust; got %v", err)
}

// knownHostsHasKey reports whether the known_hosts file at path records
// key for hostport, parsed the same way the ssh client does.
func knownHostsHasKey(t *testing.T, path, hostport string, key []byte) bool {
	t.Helper()
	db, err := gitknownhosts.NewDB(path)
	require.NoError(t, err)
	pub, _, _, _, err := ssh.ParseAuthorizedKey(key)
	require.NoError(t, err)
	for _, k := range db.HostKeys(hostport) {
		if string(k.Marshal()) == string(pub.Marshal()) {
			return true
		}
	}
	return false
}

func TestHostKeyUnknownTrustRecordsAndConnects(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})

	// Empty (but present) known_hosts: the host is unknown.
	khPath := WriteKnownHosts(t, c.HostPort)

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	ui := &recordingUI{choices: []int{0}} // accept
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys:           []string{keyPath},
			Insecure:              false,
			KnownHostsPath:        khPath,
			StrictHostKeyChecking: true,
			Timeout:               20 * time.Second,
		})
	require.NoError(t, err,
		"unknown host + accepting UI must trust the key and connect")

	require.NotEmpty(t, ui.prompts, "an unknown host must prompt once")

	// The recorded key must be one the server actually presents.
	var recorded bool
	for _, pub := range ContainerHostPubKeys(t, c.ID) {
		if knownHostsHasKey(t, khPath, c.HostPort, pub) {
			recorded = true
			break
		}
	}
	assert.True(t, recorded,
		"trusting an unknown host must persist its key to known_hosts")
}

func TestHostKeyRotatedTrustOverridesAndReconnects(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})
	khPath := WriteKnownHosts(t, c.HostPort, ContainerHostPubKeys(t, c.ID)...)

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	baseOpts := workspacessh.AuthDialOptions{
		PrivateKeys:           []string{keyPath},
		Insecure:              false,
		KnownHostsPath:        khPath,
		StrictHostKeyChecking: true,
		Timeout:               20 * time.Second,
	}

	require.NoError(t, workspacessh.TestAuthDial(
		context.Background(), &recordingUI{}, uri, baseOpts),
		"baseline dial against the original host key must succeed")

	RegenerateContainerHostKeys(t, c.ID)
	if err := waitForSSH(c.HostPort, 30*time.Second); err != nil {
		t.Fatalf("sshd did not come back after host key rotation: %v", err)
	}

	ui := &recordingUI{choices: []int{0}} // accept new key
	err = workspacessh.TestAuthDial(context.Background(), ui, uri, baseOpts)
	require.NoError(t, err,
		"a rotated host key + accepting UI must override the stale "+
			"entry and reconnect")
	require.NotEmpty(t, ui.prompts, "a changed host key must prompt")

	// known_hosts must now pin a key the rotated server presents.
	var recorded bool
	for _, pub := range ContainerHostPubKeys(t, c.ID) {
		if knownHostsHasKey(t, khPath, c.HostPort, pub) {
			recorded = true
			break
		}
	}
	assert.True(t, recorded,
		"trusting the rotated key must update known_hosts to the new key")
}

func TestHostKeyRotatedCancelLeavesFileUnchanged(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})
	khPath := WriteKnownHosts(t, c.HostPort, ContainerHostPubKeys(t, c.ID)...)

	before, err := os.ReadFile(khPath)
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	opts := workspacessh.AuthDialOptions{
		PrivateKeys:           []string{keyPath},
		Insecure:              false,
		KnownHostsPath:        khPath,
		StrictHostKeyChecking: true,
		Timeout:               20 * time.Second,
	}

	RegenerateContainerHostKeys(t, c.ID)
	if err := waitForSSH(c.HostPort, 30*time.Second); err != nil {
		t.Fatalf("sshd did not come back after host key rotation: %v", err)
	}

	ui := &recordingUI{choiceCancel: true}
	err = workspacessh.TestAuthDial(context.Background(), ui, uri, opts)
	require.Error(t, err)
	assert.True(t, errors.Is(err, workspacessh.ErrHostKeyMismatch),
		"cancelling a changed host key must surface ErrHostKeyMismatch; "+
			"got %v", err)

	after, err := os.ReadFile(khPath)
	require.NoError(t, err)
	assert.Equal(t, before, after,
		"cancelled prompt must not modify known_hosts")
}

func TestHostKeyRotatedTrustOnceConnectsWithoutRewriting(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})
	khPath := WriteKnownHosts(t, c.HostPort, ContainerHostPubKeys(t, c.ID)...)

	before, err := os.ReadFile(khPath)
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	opts := workspacessh.AuthDialOptions{
		PrivateKeys:           []string{keyPath},
		Insecure:              false,
		KnownHostsPath:        khPath,
		StrictHostKeyChecking: true,
		Timeout:               20 * time.Second,
	}

	RegenerateContainerHostKeys(t, c.ID)
	if err := waitForSSH(c.HostPort, 30*time.Second); err != nil {
		t.Fatalf("sshd did not come back after host key rotation: %v", err)
	}

	ui := &recordingUI{choices: []int{1}} // trust once
	err = workspacessh.TestAuthDial(context.Background(), ui, uri, opts)
	require.NoError(t, err,
		"trust once against a rotated key must connect for this session")
	require.NotEmpty(t, ui.prompts, "a changed host key must prompt")

	after, err := os.ReadFile(khPath)
	require.NoError(t, err)
	assert.Equal(t, before, after,
		"trust once must not modify known_hosts")
}

func TestHostKeyTrustOnceReconnectReusesSessionPin(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
	})
	khPath := WriteKnownHosts(t, c.HostPort, ContainerHostPubKeys(t, c.ID)...)
	before, err := os.ReadFile(khPath)
	require.NoError(t, err)

	RegenerateContainerHostKeys(t, c.ID)
	require.NoError(t, waitForSSH(c.HostPort, 30*time.Second))

	uri, err := workspaceapi.ParseURI(fmt.Sprintf("ssh://test@%s/tmp", c.HostPort))
	require.NoError(t, err)
	keyPath := PrivateKeyPath(t, "id_ed25519")
	cfg := config.MapConfig(map[string]any{
		"private_keys":             []any{keyPath},
		"known_hosts":              khPath,
		"strict_host_key_checking": true,
		"timeout":                  "20s",
	})

	ui := &recordingUI{choices: []int{1}}
	scheme, err := workspacessh.New(ui)(context.Background(), cfg, uri)
	require.NoError(t, err)
	remoteScheme, ok := scheme.(workspace.RemoteScheme)
	require.True(t, ok)

	_, err = scheme.Stat("/tmp")
	require.NoError(t, err)
	assert.Equal(t, 1, promptCount(ui))

	disconnected := remoteScheme.OnDisconnect()
	TerminateRemoteRuneServer(t, c.ID)
	select {
	case <-disconnected:
	case <-time.After(30 * time.Second):
		require.FailNow(t, "workspace did not report remote server disconnect")
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, err = scheme.Stat("/tmp")
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err)
	assert.Equal(t, 1, promptCount(ui))
	after, err := os.ReadFile(khPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	require.NoError(t, scheme.Close())

	freshUI := &recordingUI{choices: []int{1}}
	freshScheme, err := workspacessh.New(freshUI)(context.Background(), cfg, uri)
	require.NoError(t, err)
	defer freshScheme.Close()
	_, err = freshScheme.Stat("/tmp")
	require.NoError(t, err)
	assert.Equal(t, 1, promptCount(freshUI))
	after, err = os.ReadFile(khPath)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func promptCount(ui *recordingUI) int {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return len(ui.prompts)
}

func TestHostKeyUnparsableTrustOnceConnects(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{PublicKeyFile: "/id_ed25519.pub"})

	// A malformed known_hosts line (invalid base64 key) that the parser
	// rejects, so the host key cannot be verified against it.
	const malformed = "host.example.com ssh-ed25519 not_valid_base64!!!\n"
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(khPath, []byte(malformed), 0o600))

	uri, err := workspaceapi.ParseURI("ssh://test@" + c.HostPort + "/")
	require.NoError(t, err)

	keyPath := PrivateKeyPath(t, "id_ed25519")
	ui := &recordingUI{choices: []int{0}} // trust once (only accept option)
	err = workspacessh.TestAuthDial(context.Background(), ui, uri,
		workspacessh.AuthDialOptions{
			PrivateKeys:           []string{keyPath},
			Insecure:              false,
			KnownHostsPath:        khPath,
			StrictHostKeyChecking: true,
			Timeout:               20 * time.Second,
		})
	require.NoError(t, err,
		"unparsable known_hosts + trust once must connect for this session")
	require.NotEmpty(t, ui.prompts, "an unparsable known_hosts must prompt")

	after, err := os.ReadFile(khPath)
	require.NoError(t, err)
	assert.Equal(t, malformed, string(after),
		"a malformed known_hosts must never be rewritten")
}
