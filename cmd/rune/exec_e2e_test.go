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

//go:build e2e && (darwin || linux)

package main

import (
	"bufio"
	"context"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecOffscreen(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "rune")
	build := exec.Command("go", "build", "-o", bin, ".")
	out, err := build.CombinedOutput()
	require.NoError(t, err, "go build rune: %s", out)

	dataDir := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, configFilename), []byte(
		"editor:\n  mode: vim\n"+
			"animations:\n  intro: false\n  loading_workspace: false\n  open_workspace: false\n",
	), 0o644))
	ws := filepath.Join(dir, "ws")
	require.NoError(t, os.MkdirAll(ws, 0o755))
	// Plain text has no language package, whose install prompt would take
	// the keys.
	notes := filepath.Join(ws, "notes.txt")
	require.NoError(t, os.WriteFile(notes, []byte("package main\n"), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-G", "--offscreen", "--geometry", "800x600",
		"-d", dataDir, "-w", ws, "-e", "-")
	// Nothing in the instance may reach the Rune that runs the tests.
	cmd.Env = append(os.Environ(), "RUNE_SOCKET=", "RUNE_TOKEN=", "RUNE_CERT=",
		"RUNE_PID=", "RUNE_DATADIR="+dataDir)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	require.NoError(t, err)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("stderr:\n%s", stderr.String())
		}
	})
	replies := bufio.NewScanner(stdout)
	send := func(line string) string {
		t.Helper()
		_, err := io.WriteString(stdin, line+"\n")
		require.NoError(t, err)
		require.True(t, replies.Scan(), "no reply to %q", line)
		return replies.Text()
	}

	for _, line := range []string{
		"workspaceready",
		"windownew right",
		"echo {prompt}edit<space>notes.txt<enter>",
		"echo ix<esc>",
		"write",
	} {
		require.Equal(t, "ok", send(line), line)
	}
	got, err := os.ReadFile(notes)
	require.NoError(t, err)
	assert.Equal(t, "xpackage main\n", string(got), "write replies once the keys are saved")

	reply := send("screenshot --reload")
	path, ok := strings.CutPrefix(reply, "ok ")
	require.True(t, ok, "screenshot reply: %q", reply)
	assert.Equal(t, filepath.Join(dataDir, "screenshots"), filepath.Dir(path))
	f, err := os.Open(path)
	require.NoError(t, err)
	defer f.Close()
	img, err := png.DecodeConfig(f)
	require.NoError(t, err)
	assert.Equal(t, 800*defaultOffscreenScale, float64(img.Width))
	assert.Equal(t, 600*defaultOffscreenScale, float64(img.Height))

	require.NoError(t, stdin.Close())
	assert.False(t, replies.Scan(), "no reply after stdin ends")
	require.NoError(t, cmd.Wait(), "Rune exits once stdin ends")
}
