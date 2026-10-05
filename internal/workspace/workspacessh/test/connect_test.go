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
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/workspace/workspacessh"
)

func TestConnectSchemeEndToEnd(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	const serveDelay = time.Second
	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
		ServeDelay:        serveDelay.String(),
	})

	keyPath := PrivateKeyPath(t, "id_ed25519")

	// /tmp exists in the linuxserver/openssh-server image and is
	// writable by the test user; use it as the workspace root so
	// workspaceExists succeeds.
	uri, err := workspaceapi.ParseURI(
		fmt.Sprintf("ssh://test@%s/tmp", c.HostPort))
	require.NoError(t, err)

	cfg := config.MapConfig(map[string]any{
		"private_keys": []any{keyPath},
		"timeout":      "20s",
		"insecure":     true,
	})

	schemeFn := workspacessh.New(&errorUI{})
	scheme, err := schemeFn(context.Background(), cfg, uri)
	require.NoError(t, err)
	defer scheme.Close()

	// Stat the workspace path: the very first remote round trip. It blocks on
	// the background connect attempt (remoteScheme.state), which now completes
	// only once the remote is serving-ready. If the bootstrap looked for the
	// wrong binary or failed for any other reason this returns an error.
	start := time.Now()
	fi, err := scheme.Stat("/tmp")
	elapsed := time.Since(start)
	require.NoError(t, err, "Stat over the connected workspace should succeed")
	assert.NotNil(t, fi)

	// Allow scheduling slack below the injected delay: the point is that the
	// first RPC did not resolve early (which it would have without readiness
	// gating), not that it matches the delay exactly.
	assert.GreaterOrEqual(t, elapsed, serveDelay-300*time.Millisecond,
		"the first RPC must block until the remote is serving-ready; it "+
			"returned after %s but the remote delayed serving by %s",
		elapsed, serveDelay)
}

func TestConnectSchemeMultiKeyRedialBootstrap(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		ExtraSSHDConfig:   "MaxAuthTries 1\n",
		InstallRuneBinary: true,
	})

	wrong := PrivateKeyPath(t, "id_ed25519_wrong")
	right := PrivateKeyPath(t, "id_ed25519")

	uri, err := workspaceapi.ParseURI(
		fmt.Sprintf("ssh://test@%s/tmp", c.HostPort))
	require.NoError(t, err)

	cfg := config.MapConfig(map[string]any{
		"private_keys": []any{wrong, right},
		"timeout":      "20s",
		"insecure":     true,
	})

	schemeFn := workspacessh.New(&errorUI{})
	scheme, err := schemeFn(context.Background(), cfg, uri)
	require.NoError(t, err)
	defer scheme.Close()

	// Stat triggers the connect path. Loop briefly to absorb
	// the maintainConnection goroutine's first publish.
	deadline := time.Now().Add(20 * time.Second)
	var fi any
	for time.Now().Before(deadline) {
		fi, err = scheme.Stat("/tmp")
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err,
		"per-key redial under MaxAuthTries=1 must complete the full "+
			"scheme bootstrap, not just the auth handshake; if this "+
			"fails it usually means the second key authenticates but "+
			"the bootstrap then can't find `rune` on the remote")
	assert.NotNil(t, fi)
}

func TestConnectSchemeUserLocalBin(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

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

	schemeFn := workspacessh.New(&errorUI{})
	scheme, err := schemeFn(context.Background(), cfg, uri)
	require.NoError(t, err)
	defer scheme.Close()

	deadline := time.Now().Add(20 * time.Second)
	var fi any
	for time.Now().Before(deadline) {
		fi, err = scheme.Stat("/tmp")
		if err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err,
		"the bootstrap must find `rune` installed only at "+
			"~/.local/bin/rune (install.sh's location) even though "+
			"that directory is not on the sshd session PATH")
	assert.NotNil(t, fi)
}
