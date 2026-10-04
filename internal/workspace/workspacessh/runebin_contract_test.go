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

package workspacessh

import (
	"bytes"
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// TestRuneBinaryContract verifies the assumptions that connectScheme
// makes about the local `rune` binary. The SSH workspace bootstrap
// invokes `rune -x <path>` on the remote host (see connectScheme in
// scheme.go) and expects the resulting process to speak workspacerpc
// over its stdio. If a future change to cmd/rune/main.go (renamed
// flag, dropped short-form, pflag library upgrade, protocol drift,
// etc.) breaks any of those assumptions, this test catches it before
// the broken contract reaches a real ssh dial.
//
// The test relies on `rune` being installed on $PATH. When it is not
// (CI runners, machines that haven't run `make rune`) the test skips
// rather than failing, mirroring the convention used by SkipIfNoDocker.
func TestRuneBinaryContract(t *testing.T) {
	runePath, err := exec.LookPath("rune")
	if err != nil {
		t.Skipf("rune binary not on PATH: %v "+
			"(run `make rune` to build it; this test verifies the "+
			"workspacessh contract against the real binary)", err)
	}

	workspaceDir := t.TempDir()
	markerPath := filepath.Join(workspaceDir, "marker")
	require.NoError(t, os.WriteFile(markerPath, []byte("hello"), 0o600))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Wire up stdio as *os.File pipes so we can hand them to newStdConn
	// directly — same shape connectScheme builds in scheme.go.
	stdinR, stdinW, err := os.Pipe()
	require.NoError(t, err)
	stdoutR, stdoutW, err := os.Pipe()
	require.NoError(t, err)

	var stderr bytes.Buffer
	var stderrMu sync.Mutex
	stderrWriter := lockedWriter{mu: &stderrMu, w: &stderr}

	// `rune -x <path>` — exact form constructed by connectScheme.
	cmd := exec.CommandContext(ctx, runePath, "-x", workspaceDir)
	// Without --datadir, rune -x installs into ~/.rune, which must be the
	// test's and not the developer's.
	cmd.Env = append(os.Environ(), "HOME="+t.TempDir())
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = &stderrWriter

	require.NoError(t, cmd.Start(),
		"rune -x must launch successfully; if pflag dropped short-form "+
			"flag support or cmd/rune no longer accepts -x this is the "+
			"first thing that breaks")
	t.Cleanup(func() {
		_ = stdinW.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	// Close the FDs we handed to the child in our own process: otherwise
	// the read side of stdout / write side of stdin stay open here and
	// the gRPC side never observes EOF on close.
	_ = stdinR.Close()
	_ = stdoutW.Close()

	// Wire stdout (child writes) + stdin (child reads) into a gRPC
	// ClientConn the same way connectScheme does.
	conn, err := grpc.Dial("",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return newStdConn(log.StandardLogger(), stdoutR, stdinW,
				false, /* stdio */
				func() { /* close hook unused in test */ })
		}),
	)
	require.NoError(t, err)
	defer conn.Close()

	client := workspacerpc.NewClient(ctx, conn)

	// Round-trip a Stat against the workspace path. Same first call
	// the real IDE makes after bootstrap; if the child failed to
	// initialize a workspace scheme rooted at -x, or if the rpc
	// framing drifted, this fails.
	var fi os.FileInfo
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		fi, err = client.Stat(workspaceDir)
		if err == nil {
			break
		}
		select {
		case <-time.After(250 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("ctx done before Stat succeeded: %v", ctx.Err())
		}
	}
	if err != nil {
		stderrMu.Lock()
		s := stderr.String()
		stderrMu.Unlock()
		t.Fatalf("Stat over rune workspace server failed: %v\n"+
			"rune stderr:\n%s", err, s)
	}
	require.NotNil(t, fi)
	assert.True(t, fi.IsDir(),
		"rune -x %s should expose a directory; got %v", workspaceDir, fi)

	mfi, err := client.Stat(markerPath)
	require.NoError(t, err,
		"rune workspace server should expose files under -x path")
	assert.False(t, mfi.IsDir())
	assert.Equal(t, "marker", filepath.Base(mfi.Name()))
}

// lockedWriter is a tiny synchronized io.Writer used as Cmd.Stderr so
// the test goroutine can read what the child wrote without racing the
// process's still-running stderr writer.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
