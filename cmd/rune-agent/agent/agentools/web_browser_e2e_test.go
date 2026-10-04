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

package agentools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/llmapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// hostExec runs commands like the local workspace scheme does: cmd.Env is
// appended to this process' environment instead of replacing it.
type hostExec struct{}

func (hostExec) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args...)
	c.Dir = cmd.Dir
	c.Stdout = cmd.Stdout
	c.Stderr = cmd.Stderr
	c.Env = append(os.Environ(), cmd.Env...)
	if err := c.Start(); err != nil {
		return 0, err
	}
	go func() { cmd.Watcher.WatchProcess() <- c.Wait() }()
	return workspaceapi.Pid(c.Process.Pid), nil
}

func (hostExec) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (hostExec) Close() error                                  { return nil }

func TestWebBrowserDrivesHeadlessChrome(t *testing.T) {
	ctx := t.Context()
	bin, err := LookupAgentBrowser(ctx, hostExec{})
	if err != nil {
		t.Skipf("agent-browser not installed: %v", err)
	}
	// Short enough for agent-browser's socket path limit.
	dir, err := os.MkdirTemp("", "wb")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	page := filepath.Join(dir, "page.html")
	require.NoError(t, os.WriteFile(page, []byte(
		`<title>Rune</title><input aria-label="Name"><button onclick="document.title=document.querySelector('input').value">Go</button>`,
	), 0o600))
	tool := NewWebBrowser(hostExec{}, localFS{}, bin, dir, "e2e")
	pidFile := filepath.Join(dir, "sockets", "rune-e2e.pid")
	t.Cleanup(func() {
		tool.Close()
		require.Eventually(t, func() bool {
			_, err := os.Stat(pidFile)
			return os.IsNotExist(err)
		}, 15*time.Second, 100*time.Millisecond, "closing the chat must stop its browser")
	})

	run := func(command ...string) string {
		t.Helper()
		result := executeWebBrowser(t, tool, ctx, command...)
		require.False(t, result.IsError, result.Content)
		return result.Content
	}
	run("open", "file://"+page)
	require.FileExists(t, pidFile, "the session's browser runs from the tool's socket directory")
	snapshot := run("snapshot", "-i")
	assert.Contains(t, snapshot, untrustedOpen)
	assert.Contains(t, snapshot, `textbox "Name"`)
	run("fill", `input`, "web_browser")
	run("click", "button")
	assert.Contains(t, run("get", "title"), "web_browser")

	shot := executeWebBrowser(t, tool, ctx, "screenshot", "--annotate")
	require.False(t, shot.IsError, shot.Content)
	require.Len(t, shot.MultiContent, 2)
	assert.Equal(t, llmapi.ContentPartTypeImageURL, shot.MultiContent[1].Type)
	assert.True(t, strings.HasPrefix(shot.MultiContent[1].ImageURL, "data:image/png;base64,"))
	entries, err := os.ReadDir(filepath.Join(dir, "screenshots"))
	require.NoError(t, err)
	assert.Empty(t, entries, "screenshots are removed once returned")

	failed := executeWebBrowser(t, tool, ctx, "click", "@e999")
	assert.True(t, failed.IsError)
}
