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

package exoeditor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/term/vte"
)

func TestEditorTimeoutHangsUpLiveVimCleanly(t *testing.T) {
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim binary not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := t.TempDir()
	dir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)

	const relFile = "exo.txt"
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, relFile), []byte("hello\n"), 0o644))

	ws, scheme := newFileSchemeWorkspace(t, dir)
	fileURI, err := ws.URI(relFile)
	require.NoError(t, err)

	schedule := func(fn func()) bool { fn(); return true }
	cfg := vte.DefaultConfig()
	cfg.WidthHint = 80
	cfg.HeightHint = 24

	ed := new(
		"vim -Nu NONE {file}",
		"<esc>:{line}<enter>",
		// A quit sequence that does NOT quit vim, forcing the timeout
		// escalation path.
		"<esc>",
		schedule,
		ws,
		fileURI,
		stubNotifications{},
		stubPublisher{},
		scheme, // terminal
		scheme, // executor
		stubTabManager{},
		cfg,
		stubReloader{},
		nil,
		false,
		nil, nil, nil,
		200*time.Millisecond,
	)

	buf := cell.NewBuffer()
	h, err := ed.Edit(context.Background(), fileURI, buf, false, false)
	require.NoError(t, err)

	swapFile := filepath.Join(dir, "."+relFile+".swp")
	require.Eventually(t, func() bool {
		_, err := os.Stat(swapFile)
		return err == nil
	}, 5*time.Second, 50*time.Millisecond,
		"vim must create a swap file at %q while running", swapFile)

	require.NoError(t, h.Close())

	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".swp") {
				return false
			}
		}
		homeEntries, err := os.ReadDir(home)
		if err != nil {
			return false
		}
		for _, e := range homeEntries {
			name := e.Name()
			if strings.HasPrefix(name, ".viminf") &&
				strings.HasSuffix(name, ".tmp") {
				return false
			}
		}
		return true
	}, 10*time.Second, 100*time.Millisecond,
		"the timeout escalation must SIGHUP vim so it removes its .swp "+
			"and finalizes ~/.viminfo without leaving a .viminfo*.tmp")
}
