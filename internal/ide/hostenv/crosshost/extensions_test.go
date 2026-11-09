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
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// extensionScript records, like recordScript, how the editor started it,
// along with the data directory of the workspace's host that the editor
// passed. Every start records to a file of its own, named after the first
// argument, since the editor starts one extension process per workspace.
const extensionScript = `#!/bin/sh
out=$1.$$
shift
{
	for arg; do printf 'arg<%s>\n' "$arg"; done
	printf 'env<%s>\n' "$RUNE_DATADIR"
	printf 'install<%s>\n' "$RUNE_INSTALLDIR"
} > "$out.tmp"
mv "$out.tmp" "$out"
`

const tutorialSource = `
def run():
    wait_command(command="edit")
tutorial(entry=run)
`

// extensionRecords waits for extensionScript to have recorded at least n
// distinct starts to records.<pid> files and returns them sorted.
func extensionRecords(t *testing.T, records string, n int) []string {
	t.Helper()
	var got []string
	waitFor(t, commandTimeout, fmt.Sprintf("%d distinct extension starts", n), func() error {
		got = got[:0]
		matches, err := filepath.Glob(records + ".*")
		if err != nil {
			return err
		}
		for _, m := range matches {
			if strings.HasSuffix(m, ".tmp") {
				continue
			}
			b, err := os.ReadFile(m)
			if err != nil {
				return err
			}
			if !slices.Contains(got, string(b)) {
				got = append(got, string(b))
			}
		}
		if len(got) < n {
			return fmt.Errorf("%d so far: %q", len(got), got)
		}
		return nil
	})
	slices.Sort(got)
	return got
}

func TestEditorRunsItsOwnExtensionsAndTutorialsFromItsDataDir(t *testing.T) {
	h := startHost(t)
	remoteWS := h.workspaceDir(t, "ws")
	e := startEditor(t, h, t.TempDir(), editorSetup{
		dataFiles: map[string]string{
			"extension":          extensionScript,
			"tutorials/e2e.star": tutorialSource,
		},
		config: `extensions:
  e2e:
    path: "$RUNE_DATADIR/extension $RUNE_DATADIR/extension-record --data=$RUNE_DATADIR"
tutorials:
  e2e: "$RUNE_DATADIR/tutorials/e2e.star"
`,
	})
	records := filepath.Join(e.dataDir, "extension-record")
	started := func(installDir string) string {
		return "arg<--data=" + e.dataDir + ">\nenv<" + e.dataDir + ">\ninstall<" + installDir + ">\n"
	}

	e.mu.Lock()
	tutorials := e.TutorialNames()
	e.mu.Unlock()
	assert.Contains(t, tutorials, "e2e", "the editor reads tutorials from its data directory")

	assert.Equal(t, []string{started(e.dataDir)}, extensionRecords(t, records, 1),
		"a local workspace's extension runs from the editor's data directory, "+
			"which is also where its workspace's packages are")

	e.openWorkspace(t, h.uri(t, remoteWS))
	want := []string{started(e.dataDir), started(remoteDataDir)}
	slices.Sort(want)
	assert.Equal(t, want, extensionRecords(t, records, 2),
		"an ssh workspace's extension runs here, from the editor's data directory, "+
			"and learns where its workspace's packages are on the remote host")
}
