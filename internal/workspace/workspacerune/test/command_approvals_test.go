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

package runetest

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	blueauth "github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/ide/ideauthorizer/ideauthorizertest"
	"unstable.build/rune/internal/workspace"
)

// runOnPeer runs cmd on the peer and returns its stdout.
func runOnPeer(ctx context.Context, t *testing.T, exec schemeapi.Executor, cmd workspaceapi.Cmd) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	done := make(chan error, 1)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.Watcher = workspaceapi.ChanProcessWatcher(done)
	_, err := exec.StartCommand(ctx, cmd)
	require.NoError(t, err)
	select {
	case err := <-done:
		require.NoError(t, err, "stdout=%q stderr=%q", stdout.String(), stderr.String())
	case <-ctx.Done():
		t.Fatalf("timed out running %s", cmd.Path)
	}
	return stdout.String()
}

func TestCommandApprovalDoesNotCoverWhatThePeerPATHRunsLater(t *testing.T) {
	SkipIfNoDocker(t)

	control := StartHeadscale(t)
	peer := StartInstance(t, control, "peer", control.AuthKey)
	client := StartNode(t, control, "client", control.AuthKey)
	WaitPeer(t, client, "peer")

	dir := t.TempDir()
	oldDir, newDir := filepath.Join(dir, "old"), filepath.Join(dir, "new")
	for d, out := range map[string]string{oldDir: "old", newDir: "new"} {
		require.NoError(t, os.MkdirAll(d, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(d, "tool"),
			[]byte("#!/bin/sh\necho "+out+"\n"), 0o755))
	}
	peer.SetGUIEnv(t, "PATH="+oldDir+":$PATH\n")

	scheme := OpenWorkspace(t, client, "peer", dir)
	defer scheme.Close()
	uri, err := workspaceapi.ParseURI("rune://peer" + dir)
	require.NoError(t, err)
	ws := workspace.NewSchemeWorkspace(uri, scheme, func(fn func()) bool { fn(); return true })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	prompter := ideauthorizertest.NewPrompter(ideauthorizer.PromptOptionYesAlways)
	authorizer := ideauthorizertest.NewAuthorizer(t, ws, uri, prompter)
	extCtx := ideauthorizertest.ExtensionContext(ctx)
	tool := workspaceapi.Cmd{Path: "tool"}
	require.NoError(t, authorizer.AuthorizeCommand(extCtx, tool))
	require.NoError(t, authorizer.AuthorizeCommand(extCtx, tool))
	require.Len(t, prompter.Prompts(), 1)
	assert.Contains(t, prompter.Prompts()[0], "Runs `"+oldDir+"/tool`.")
	assert.Equal(t, "old\n", runOnPeer(ctx, t, ws, tool))

	// The user of the peer changes its gui.env, which a running Rune
	// applies at once.
	peer.SetGUIEnv(t, "PATH="+newDir+":"+oldDir+":$PATH\n")
	require.Equal(t, "new\n", runOnPeer(ctx, t, ws, tool),
		"the peer now runs another tool")
	prompter.Answer(ideauthorizer.PromptOptionNo)
	require.ErrorIs(t, authorizer.AuthorizeCommand(extCtx, tool), blueauth.ErrForbidden,
		"approving %s/tool does not approve %s/tool", oldDir, newDir)
	require.Len(t, prompter.Prompts(), 2)
	assert.Contains(t, prompter.Prompts()[1], "Runs `"+newDir+"/tool`.")
}
