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

package crosshost

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/docmarshal/docbson"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension/extensionv2"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/ide/idepkg/pkgtrust"
	"unstable.build/rune/internal/localstorage"
)

// recordScript writes, atomically to the file its first argument names, its
// remaining arguments and the RUNE_DATADIR it was started with. Arguments
// show what the editor expanded; the environment shows what the workspace's
// command runner passed.
const recordScript = `#!/bin/sh
out=$1
shift
{
	for arg; do printf 'arg<%s>\n' "$arg"; done
	printf 'env<%s>\n' "$RUNE_DATADIR"
} > "$out.tmp"
mv "$out.tmp" "$out"
`

// editor is a Rune editor whose data directory, configuration and home
// workspace are private to the test.
type editor struct {
	*ide.IDE
	mu      *sync.Mutex
	dataDir string
}

// editorSetup is what a test puts in place before the editor starts.
type editorSetup struct {
	// dataFiles are executables to create in the editor's data directory,
	// keyed by path relative to it.
	dataFiles map[string]string
	// config is YAML appended to the editor's configuration.
	config string
}

// remoteDataDir is the data directory the SSH workspaces of an editor that
// startEditor started use on the remote host.
const remoteDataDir = remoteHome + "/.rune-e2e"

// startEditor starts an editor on the local workspace dir. Its data
// directory's name, .rune-e2e, is also the name of the data directory its SSH
// workspaces run their server with, in the remote home: remoteDataDir.
func startEditor(t *testing.T, h *remoteHost, dir string, setup editorSetup) *editor {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, ".rune-e2e")
	home := filepath.Join(root, "home")
	for _, d := range []string{dataDir, home} {
		require.NoError(t, os.MkdirAll(d, 0o755))
	}
	for name, content := range setup.dataFiles {
		path := filepath.Join(dataDir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
	}
	// As the editor process exports it for the commands it starts.
	t.Setenv("RUNE_DATADIR", dataDir)
	logPath := filepath.Join(root, "rune.log")
	cfgPath := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(fmt.Sprintf(`log_path: %q
log_level: debug
workspace:
  home: %q
  ssh:
    private_keys: [%q]
    timeout: "30s"
    insecure: true
`, logPath, home, h.keyPath)+setup.config), 0o644))

	mu := &sync.Mutex{}
	runner, err := extensionv2.NewRunner(context.Background(), mu, dataDir)
	require.NoError(t, err)
	uri, err := workspaceapi.CurrentUserHostURI(dir)
	require.NoError(t, err)
	i, err := ide.New(uri.String(), cfgPath, dataDir,
		pkgtrust.NewStore(dataDir, nil),
		localstorage.New(context.Background(), dataDir, docbson.Marshaler()),
		ide.WithLocker(mu),
		ide.WithScheduleNextTick(func(fn func()) bool {
			go debug.CapturePanicReport(func() {
				mu.Lock()
				defer mu.Unlock()
				fn()
			})
			return true
		}),
		ide.WithPublishEvent(func(term.Event) bool { return true }),
		ide.WithHostDataDir(dataDir),
		ide.WithExtensionsRunner(runner),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		// Not under mu: closing waits for workspace builds that take it.
		_ = i.Close()
		if t.Failed() {
			b, _ := os.ReadFile(logPath)
			t.Logf("editor log:\n%s", tail(string(b), 32<<10))
		}
	})
	i.Ready()
	i.WaitWorkspaces()
	return &editor{IDE: i, mu: mu, dataDir: dataDir}
}

// dispatch runs cmd in the workspace in focus, as typed at the prompt.
func (e *editor) dispatch(t *testing.T, cmd string, args ...string) {
	t.Helper()
	e.mu.Lock()
	err := e.DispatchCommand(cmd, args...)
	e.mu.Unlock()
	require.NoError(t, err, "dispatch %s %q", cmd, args)
}

// openWorkspace opens uri as a new workspace and waits until it is installed,
// which focuses it.
func (e *editor) openWorkspace(t *testing.T, uri workspaceapi.URI) {
	t.Helper()
	e.dispatch(t, "workspaceopen", uri.String())
	e.WaitWorkspaces()
}

// waitRecord waits for the record that recordScript writes to appear, which
// read returns once it exists.
func waitRecord(t *testing.T, what string, read func() (string, bool)) string {
	t.Helper()
	var record string
	waitFor(t, commandTimeout, what, func() error {
		var ok bool
		if record, ok = read(); !ok {
			return fmt.Errorf("no record yet")
		}
		return nil
	})
	return record
}

// localFile reads path on this machine, for waitRecord.
func localFile(path string) func() (string, bool) {
	return func() (string, bool) {
		b, err := os.ReadFile(path)
		return string(b), err == nil
	}
}

// remoteFile reads path on h, for waitRecord.
func (h *remoteHost) remoteFile(t *testing.T, path string) func() (string, bool) {
	return func() (string, bool) {
		return h.readFile(t, path)
	}
}

func TestEditorResolvesDataDirOfTheWorkspaceInFocus(t *testing.T) {
	h := startHost(t)
	h.writeFile(t, remoteHome+"/record", recordScript, 0o755)
	remoteWS := h.workspaceDir(t, "ws")
	localWS := t.TempDir()
	localRecord := filepath.Join(localWS, "record")
	require.NoError(t, os.WriteFile(localRecord, []byte(recordScript), 0o755))
	e := startEditor(t, h, localWS, editorSetup{})

	// `!` hands the command to the workspace's command runner as typed, after
	// the editor expanded its arguments.
	probe := func(record, out string) {
		e.dispatch(t, "!", record, out, "$RUNE_DATADIR")
	}

	probe(localRecord, filepath.Join(localWS, "home-focus"))
	assert.Equal(t, "arg<"+e.dataDir+">\nenv<"+e.dataDir+">\n",
		waitRecord(t, "the local workspace records its command",
			localFile(filepath.Join(localWS, "home-focus"))),
		"a local workspace resolves the editor's own data directory")

	e.openWorkspace(t, h.uri(t, remoteWS))
	probe(remoteHome+"/record", remoteHome+"/remote-focus")
	assert.Equal(t, "arg<"+remoteDataDir+">\nenv<"+remoteDataDir+">\n",
		waitRecord(t, "the ssh workspace records its command",
			h.remoteFile(t, remoteHome+"/remote-focus")),
		"an ssh workspace resolves its host's data directory, both where the "+
			"editor expands $RUNE_DATADIR and in what the command runner passes")

	e.dispatch(t, "workspacefocus", "1")
	probe(localRecord, filepath.Join(localWS, "home-refocus"))
	assert.Equal(t, "arg<"+e.dataDir+">\nenv<"+e.dataDir+">\n",
		waitRecord(t, "the local workspace records its command again",
			localFile(filepath.Join(localWS, "home-refocus"))),
		"moving the focus back resolves the editor's data directory again")
}
