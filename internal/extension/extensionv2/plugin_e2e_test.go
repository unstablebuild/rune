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

package extensionv2

import (
	"context"
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
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

func TestPluginPermissionPromptE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping e2e test in short mode")
	}
	runectl := findRunectl(t)
	var err error
	runectl, err = filepath.EvalSymlinks(runectl)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dataDir := t.TempDir()
	workspaceDir := filepath.Join("/tmp", fmt.Sprintf("rune-e2e-%d", os.Getpid()))
	_ = os.RemoveAll(workspaceDir)
	require.NoError(t, os.MkdirAll(workspaceDir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(workspaceDir) })
	uri, err := workspaceapi.ParseURI("file://" + workspaceDir)
	require.NoError(t, err)
	execScheme, err := workspace.NewFileScheme(ctx, config.MapConfig(map[string]any{}), uri)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, execScheme.Close()) })

	baseRunner, err := NewRunner(ctx, new(sync.Mutex), dataDir)
	require.NoError(t, err)

	cases := []struct {
		name           string
		cmd            workspaceapi.Cmd
		wantPrompts    int
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "direct runectl",
			cmd: workspaceapi.Cmd{
				Path: runectl,
				Args: []string{"wm", "focus"},
			},
			wantPrompts: 1,
			wantContains: []string{
				fmt.Sprintf("Program %s", runectl),
				"with args [wm focus]",
				"wants to **manage the window manager**.",
			},
			wantNotContain: []string{"running inside"},
		},
		{
			name: "shell runs runectl",
			cmd: workspaceapi.Cmd{
				Path: "/bin/sh",
				Args: []string{"-c", fmt.Sprintf("%q wm focus", runectl)},
			},
			wantPrompts: 1,
			wantContains: []string{
				fmt.Sprintf("Program %s", runectl),
				"with args [wm focus]",
				"running inside /bin/sh [-c",
				"wants to **manage the window manager**.",
			},
			wantNotContain: []string{"Program /bin/sh with args [-c"},
		},
		{
			name: "shell runs runectl twice",
			cmd: workspaceapi.Cmd{
				Path: "/bin/sh",
				Args: []string{"-c", fmt.Sprintf("%q wm focus && %q wm focus", runectl, runectl)},
			},
			wantPrompts: 2,
			wantContains: []string{
				fmt.Sprintf("Program %s", runectl),
				"with args [wm focus]",
				"running inside /bin/sh with args:\n\n```",
				"wants to **manage the window manager**.",
			},
			wantNotContain: []string{"Program /bin/sh with args [-c"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prompt := newE2EPromptOpener(ideauthorizer.PromptOptionYes)
			storage := storagestub.NewInMemoryService()
			authorizer, err := ideauthorizer.NewAuthorizer(
				texttest.NopEditor(), prompt, storage,
				func(fn func()) bool {
					fn()
					return true
				},
				nil, pkgtrust.NewStore(t.TempDir(), nil), ideauthorizer.Config{},
			)
			require.NoError(t, err)
			runner, err := baseRunner.WorkspaceExtensionsRunner(
				uri,
				extension.BrowserResources(e2eBrowser{}, func(term.Event) bool { return true }),
				authorizer,
				nopTrustVerifier{},
				dataDir,
				dataDir,
				e2eBrowser{},
				execScheme,
				execScheme,
				extension.GrantAll(),
				texttest.NopEditor(),
				prompt,
				storage,
				func(fn func()) bool {
					fn()
					return true
				},
			)
			require.NoError(t, err)
			defer func() { require.NoError(t, runner.Close()) }()

			watcher := newE2EProcessWatcher()
			cmd := tc.cmd
			cmd.Watcher = watcher
			_, err = runner.(schemeapi.Executor).StartCommand(context.Background(), cmd)
			require.NoError(t, err)
			err = watcher.wait(10 * time.Second)
			require.NoError(t, err)

			message := prompt.message(t, tc.wantPrompts)
			for _, want := range tc.wantContains {
				assert.Contains(t, message, want)
			}
			for _, want := range tc.wantNotContain {
				assert.NotContains(t, message, want)
			}
		})
	}
}

type e2eProcessWatcher struct {
	done chan error
}

func newE2EProcessWatcher() *e2eProcessWatcher {
	return &e2eProcessWatcher{done: make(chan error, 1)}
}

func (w *e2eProcessWatcher) WatchProcess() chan error {
	return w.done
}

func (w *e2eProcessWatcher) wait(timeout time.Duration) error {
	select {
	case err := <-w.done:
		return err
	case <-time.After(timeout):
		return fmt.Errorf("timed out waiting for command")
	}
}

// findRunectl locates the runectl binary or skips the test
// when none is available on the system. Mirrors findGopls /
// findDlv used elsewhere in the repo: the e2e suite is a
// development-time integration test, not a hard CI gate, so
// missing tooling should skip rather than fail the run.
func findRunectl(t *testing.T) string {
	t.Helper()
	if bin, err := exec.LookPath("runectl"); err == nil {
		return bin
	}
	for _, p := range []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "runectl"),
		filepath.Join(os.Getenv("HOME"), "go", "bin", "runectl"),
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("runectl not found in PATH, ~/.rune/bin, or ~/go/bin; " +
		"build it via `make -C cmd/runectl build` to enable this test")
	return ""
}
