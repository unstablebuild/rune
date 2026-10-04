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
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/workspacessh"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestIntegrationScheme(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)
	t.Parallel()

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
		ExtraSSHDConfig: "MaxStartups 200:30:400\n" +
			"MaxSessions 200\n" +
			"LoginGraceTime 60\n",
	})

	keyPath := PrivateKeyPath(t, "id_ed25519")

	cfgs := map[string]config.Config{
		"openssh_proc_remote": config.MapConfig(map[string]any{
			"command": "ssh -o StrictHostKeyChecking=no -i " + keyPath + " %h -p %p",
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
			t.Parallel()

			workspacetest.TestWorkspaceSchemeFiles(t, func(t *testing.T) schemeapi.Scheme {
				return newSchemeIntegration(t, c.HostPort, cfg)
			})

			workspacetest.TestWorkspaceSchemeExecutor(t, func(t *testing.T) schemeapi.Scheme {
				return newSchemeIntegration(t, c.HostPort, cfg)
			})
		})
	}
}

func TestIntegrationStaleEditorCloseDuringReconnectOpen(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
	})
	cfg := config.MapConfig(map[string]any{
		"private_keys": []any{PrivateKeyPath(t, "id_ed25519")},
		"timeout":      "20s",
		"insecure":     true,
	})
	scheme := newSchemeIntegration(t, c.HostPort, cfg)

	for _, name := range []string{"before.txt", "after.txt"} {
		f, err := scheme.Create(name)
		require.NoError(t, err)
		_, err = f.Write([]byte(name + "\n"))
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}

	rootURI, err := scheme.URI(".")
	require.NoError(t, err)
	beforeURI, err := scheme.URI("before.txt")
	require.NoError(t, err)
	afterURI, err := scheme.URI("after.txt")
	require.NoError(t, err)
	beforeSwapDir, err := workspace.DefaultSwapDirectory(beforeURI)
	require.NoError(t, err)
	afterSwapDir, err := workspace.DefaultSwapDirectory(afterURI)
	require.NoError(t, err)

	blocking := &blockAfterOpenScheme{
		Scheme:  scheme,
		name:    ".after.txt" + workspace.SwapFileExtensionName,
		opened:  make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(blocking.unblock)
	ws := workspace.NewSchemeWorkspace(rootURI, blocking, func(fn func()) bool {
		fn()
		return true
	})

	stale, err := ws.Load(beforeURI, cell.NewBuffer(), beforeSwapDir, false)
	require.NoError(t, err)

	remote := scheme.(workspace.RemoteScheme)
	disconnected := remote.OnDisconnect()
	TerminateRemoteRuneServer(t, c.ID)
	select {
	case <-disconnected:
	case <-time.After(20 * time.Second):
		t.Fatal("remote scheme did not observe the server restart")
	}
	require.Eventually(t, func() bool {
		_, statErr := scheme.Stat("after.txt")
		return statErr == nil
	}, 20*time.Second, 100*time.Millisecond)

	type loadResult struct {
		file workspace.FlusherCloser
		err  error
	}
	loaded := make(chan loadResult, 1)
	go debug.CapturePanicReport(func() {
		f, loadErr := ws.Load(afterURI, cell.NewBuffer(), afterSwapDir, false)
		loaded <- loadResult{file: f, err: loadErr}
	})

	select {
	case <-blocking.opened:
	case <-time.After(20 * time.Second):
		t.Fatal("post-reconnect swap open did not reach the test barrier")
	}
	require.NoError(t, stale.Close())
	blocking.unblock()

	result := <-loaded
	if result.err == nil {
		t.Cleanup(func() { _ = result.file.Close() })
	}
	require.NoError(t, result.err,
		"a stale editor close must not invalidate a file opened by the new session")
}

type blockAfterOpenScheme struct {
	schemeapi.Scheme
	name        string
	opened      chan struct{}
	release     chan struct{}
	openedOnce  sync.Once
	releaseOnce sync.Once
}

func (s *blockAfterOpenScheme) OpenFile(
	name string, flag int, perm os.FileMode,
) (workspaceapi.File, error) {
	f, err := s.Scheme.OpenFile(name, flag, perm)
	if err == nil && filepath.Base(name) == s.name {
		s.openedOnce.Do(func() {
			close(s.opened)
			<-s.release
		})
	}
	return f, err
}

func (s *blockAfterOpenScheme) unblock() {
	s.releaseOnce.Do(func() { close(s.release) })
}

// integrationDirCounter ensures each schemeFn(t) call gets its own
// subdirectory under /tmp so the per-subtest filesystem state cannot
// leak into the next subtest. The container is reused across subtests
// (see TestIntegrationScheme) for connection-rate reasons; a unique
// workspace dir per subtest is the cheap way to keep them isolated.
var integrationDirCounter atomic.Uint64

func newSchemeIntegration(
	t *testing.T, hostname string, cfg config.Config,
) schemeapi.Scheme {
	t.Helper()

	// Pick a workspace directory unique to this subtest. Just the
	// counter would suffice, but keeping the test name in the path
	// makes it grep-friendly when something goes wrong. Strip every
	// non-[A-Za-z0-9_-] rune so we never have to worry about shell
	// quoting (the bootstrap forwards the path through `bash -c`).
	dir := path.Join("/tmp", fmt.Sprintf("rune_ssh_test_%s_%d",
		safeName(t.Name()), integrationDirCounter.Add(1)))

	workspaceURI, err := workspaceapi.ParseURI(
		"ssh://test@" + hostname + dir)
	require.NoError(t, err)

	ctx := context.Background()

	schemeFn := workspacessh.New(&errorUI{})

	// Pre-create the workspace dir on the remote so the bootstrap's
	// `ls $path` check succeeds before the gRPC channel comes up.
	mkdir, err := schemeFn(ctx, cfg, parentURI(t, workspaceURI))
	require.NoError(t, err)
	require.NoError(t, mkdir.MkdirAll(dir, 0o755))
	mkdir.Close()

	s, err := schemeFn(ctx, cfg, workspaceURI)
	require.NoError(t, err)

	t.Cleanup(func() {
		// The container is torn down by the parent test's t.Cleanup,
		// so we don't bother removing the per-subtest dir from the
		// remote — the entire fs is gone shortly after this returns,
		// and a per-file `rm` would multiply many small gRPC
		// round-trips through the SSH tunnel. Just close the
		// scheme to release its ssh session.
		_ = s.Close()
	})
	return s
}

// parentURI returns a URI pointing at /tmp on the remote — used to run
// MkdirAll for a fresh per-subtest workspace dir before connecting the
// real workspace scheme.
func parentURI(t *testing.T, u workspaceapi.URI) workspaceapi.URI {
	t.Helper()
	parent, err := workspaceapi.ParseURI(
		"ssh://" + u.User() + "@" + u.Host() + "/tmp")
	require.NoError(t, err)
	return parent
}

// errorUI fails any prompt request: this integration test only verifies
// non-interactive flows (configured key, healthy server).
type errorUI struct{}

func (errorUI) PromptSecret(context.Context, string) (string, error) {
	return "", fmt.Errorf("unexpected prompt: secret")
}

func (errorUI) PromptText(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("unexpected prompt: text")
}

func (errorUI) PromptChoice(context.Context, string, []string) (int, error) {
	return -1, fmt.Errorf("unexpected prompt: choice")
}

func (errorUI) Notify(workspacessh.NotificationLevel, string) {}

// safeName returns a shell-safe version of name suitable for substitution
// into a path the bootstrap may quote naively.
func safeName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
