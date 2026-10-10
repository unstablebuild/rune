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
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/localstorage"
)

func TestIntegrationIDEWorkspaceReloadOverSSH(t *testing.T) {
	SkipIfNoDocker(t)
	EnsureImage(t)

	c := StartContainer(t, SSHDScenario{
		PublicKeyFile:     "/id_ed25519.pub",
		InstallRuneBinary: true,
	})
	keyPath := PrivateKeyPath(t, "id_ed25519")

	// Stage a workspace dir + two files on the remote: one we open
	// before the reload, one we open after. The IDE's bootstrap
	// expects the workspace path to already exist.
	const remoteDir = "/tmp/rune_reload_test"
	workspaceURI, err := workspaceapi.ParseURI(fmt.Sprintf(
		"ssh://test@%s%s", c.HostPort, remoteDir))
	require.NoError(t, err)
	stageRemoteWorkspace(t, c.HostPort, keyPath, workspaceURI,
		"before.txt", "after.txt")

	// Local IDE config: modal mode + ":" as the command key so we
	// can drive `:workspacereload<enter>` through the handler.
	// workspace.ssh wires the stdlib remote against the container.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "rune.yaml")
	// The container's home is /config, not the SSH scheme's assumed
	// /home/test; keep swap files in the writable workspace for this test.
	cfgYAML := fmt.Sprintf(`editor:
  mode: modal
  swap_dir: false
command:
  key: ":"
workspace:
  ssh:
    private_keys: ["%s"]
    timeout: "20s"
    insecure: true
`, keyPath)
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgYAML), 0o644))

	var mu sync.Mutex
	i, err := ide.New(workspaceURI.String(), cfgPath, dir,
		pkgtrust.NewStore(dir, nil),
		localstorage.New(context.Background(), dir, docbson.Marshaler()),
		ide.WithLocker(&mu),
		ide.WithScheduleNextTick(hostScheduleNextTickIDE(&mu)),
		ide.WithPublishEvent(func(term.Event) bool { return true }),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = i.Close() })

	rootHandler := i.Ready()
	// Phase B does the docker round trip + ssh dial in a goroutine
	// — wait for it to land before driving any remote operation.
	i.WaitWorkspaces()

	beforeURI, err := workspaceapi.ParseURI(fmt.Sprintf(
		"ssh://test@%s%s/before.txt", c.HostPort, remoteDir))
	require.NoError(t, err)
	afterURI, err := workspaceapi.ParseURI(fmt.Sprintf(
		"ssh://test@%s%s/after.txt", c.HostPort, remoteDir))
	require.NoError(t, err)

	// Sanity check: pre-reload the scheme is healthy. If this
	// fails the test environment itself is broken (image, dial,
	// rune binary install) — not the reload bug.
	pollOpenURI(t, &mu, i, beforeURI,
		"baseline open before reload should succeed")

	// Drive `:workspacereload<enter>` through the handler exactly
	// like a user would.
	feedKeys(t, &mu, rootHandler, ":workspacereload<enter>")
	i.WaitWorkspaces()

	// Post-reload Open must succeed. With the bug in place it
	// surfaces "context canceled" because the cached
	// schemeWorkspace still wraps the original remoteScheme whose
	// ctx was canceled by the close-half of reload.
	mu.Lock()
	openErr := i.Open(afterURI)
	mu.Unlock()
	require.NoError(t, openErr,
		"opening a remote file on the post-reload scheme must "+
			"not return context.Canceled (regression: "+
			":workspacereload over ssh)")
}

// hostScheduleNextTickIDE mirrors hostScheduleNextTick from the IDE
// e2e tests: dispatch UserFuncs on a fresh goroutine while holding
// the same locker the host event loop would. Required so the IDE's
// async install (Phase C) lands under the lock.
func hostScheduleNextTickIDE(mu sync.Locker) func(func()) bool {
	return func(fn func()) bool {
		go func() {
			mu.Lock()
			defer mu.Unlock()
			fn()
		}()
		return true
	}
}

// stageRemoteWorkspace mkdir -p's dirURI on the remote and creates
// each named file (empty). It uses a short-lived ssh scheme of its
// own — the same code path the IDE will hit when it boots — so the
// staging itself doubles as a smoke test that the container is
// reachable before we even invoke the IDE.
func stageRemoteWorkspace(
	t *testing.T, hostPort, keyPath string,
	dirURI workspaceapi.URI, files ...string,
) {
	t.Helper()
	cfg := config.MapConfig(map[string]any{
		"private_keys": []any{keyPath},
		"timeout":      "20s",
		"insecure":     true,
	})
	scheme := newSchemeIntegration(t, hostPort, cfg)
	require.NoError(t, scheme.MkdirAll(dirURI.Path(), 0o755))
	for _, name := range files {
		f, err := scheme.Create(filepath.Join(dirURI.Path(), name))
		require.NoError(t, err, "stage remote file %s", name)
		require.NoError(t, f.Close())
	}
}

// pollOpenURI polls i.Open until the remote scheme is connected
// enough to satisfy the editFileURI flow. The remote scheme dials
// asynchronously after newScheme returns, so the first attempt may
// race against the dial even after WaitWorkspaces returns.
func pollOpenURI(
	t *testing.T, mu *sync.Mutex, i *ide.IDE,
	uri workspaceapi.URI, msg string,
) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		mu.Lock()
		err := i.Open(uri)
		mu.Unlock()
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, lastErr, msg)
}

// feedKeys parses sequence (handlertest.SequenceTestCase format) and
// dispatches each key as a term.EventKey through h. The mu lock
// matches what the production event loop (gui / tui) holds around
// each Handle call, so handlers see the same visibility guarantees
// in test as they do at runtime.
func feedKeys(t *testing.T, mu *sync.Mutex, h tui.Handler, sequence string) {
	t.Helper()
	keys, err := term.ParseKeys(sequence)
	require.NoError(t, err, "parse %q", sequence)
	for _, key := range keys {
		ev := term.Event{
			Type: term.EventKey,
			Ch:   key.Ch,
			Mod:  key.Mod,
			Key:  key.Key,
		}
		switch {
		case ev.Ch != 0:
			ev.Raw = []byte(string(ev.Ch))
		case ev.Key == term.KeySpace:
			ev.Raw = []byte(" ")
		case ev.Key == term.KeyEnter:
			ev.Raw = []byte{0x0d, 0x0a}
		}
		mu.Lock()
		_, handled := h.Handle(ev)
		mu.Unlock()
		require.True(t, handled, "%s in %q",
			key.String(), sequence)
	}
}
