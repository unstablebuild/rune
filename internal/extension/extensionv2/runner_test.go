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

package extensionv2

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/debug"
)

func TestWithServerInterceptorsAppendsToConfig(t *testing.T) {
	t.Parallel()

	stream := func(
		srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		return handler(srv, ss)
	}
	unary := func(
		ctx context.Context, req any, info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		return handler(ctx, req)
	}

	var cfg runnerConfig
	WithServerInterceptors(stream, unary)(&cfg)
	WithServerInterceptors(nil, unary)(&cfg)
	WithServerInterceptors(stream, nil)(&cfg)
	WithServerInterceptors(nil, nil)(&cfg)

	assert.Len(t, cfg.extraStreamInterceptors, 2)
	assert.Len(t, cfg.extraUnaryInterceptors, 2)
}

func TestNewUnixListenerCreatesSocketDir(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "data")
	r := &Runner{dataDir: dataDir}
	uri, err := workspaceapi.ParseURI("file:///home/me/proj")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(dataDir, "sockets"), filepath.Dir(r.socketPath(uri)),
		"test data dir too long: the socket fell back to os.TempDir()")

	listener, err := r.newUnixListener(uri)
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })

	assert.Equal(t, r.socketPath(uri), listener.Addr().String())
	info, err := os.Stat(filepath.Join(dataDir, "sockets"))
	require.NoError(t, err)
	assert.True(t, info.IsDir())
}

func TestRunnerSocketPath(t *testing.T) {
	t.Parallel()

	r := &Runner{dataDir: "/tmp/rune-data"}

	cases := []struct {
		name string
		uri  string
	}{
		{"file scheme", "file:///home/me/proj"},
		{"ssh scheme with home alias", "ssh://10.0.0.9/~/src/blue"},
		{"ssh scheme with absolute path", "ssh://example.com/srv/app"},
	}

	socketDir := filepath.Join(r.dataDir, "sockets")
	hexSock := regexp.MustCompile(`^[0-9a-f]+\.sock$`)

	for _, tc := range cases {
		t.Run(tc.name+": socket lives under <dataDir>/sockets", func(t *testing.T) {
			uri, err := workspaceapi.ParseURI(tc.uri)
			require.NoError(t, err)

			got := r.socketPath(uri)

			assert.True(t, strings.HasPrefix(got, socketDir+string(filepath.Separator)),
				"socket %q should live under %q", got, socketDir)
			assert.True(t, hexSock.MatchString(filepath.Base(got)),
				"socket basename %q should be hex.sock; some filesystems "+
					"reject path components with special characters",
				filepath.Base(got))
			// macOS' sun_path is 104 bytes including the NUL.
			assert.Less(t, len(got), 104,
				"socket path %q is too long for sun_path", got)
		})
	}

	t.Run("same URI hashes to the same socket", func(t *testing.T) {
		uri, err := workspaceapi.ParseURI("ssh://example.com/srv/app")
		require.NoError(t, err)

		assert.Equal(t, r.socketPath(uri), r.socketPath(uri),
			"hash of the same URI must be stable across calls so a "+
				"re-opened workspace replaces its stale socket cleanly")
	})

	t.Run("different URIs hash to different sockets", func(t *testing.T) {
		a, err := workspaceapi.ParseURI("ssh://example.com/srv/app")
		require.NoError(t, err)
		b, err := workspaceapi.ParseURI("ssh://example.com/srv/other")
		require.NoError(t, err)

		assert.NotEqual(t, r.socketPath(a), r.socketPath(b),
			"distinct workspaces must not collide on the same socket")
	})

	t.Run("falls back to TempDir when dataDir would overflow sun_path", func(t *testing.T) {
		// macOS sockaddr_un.sun_path is 104 bytes. A typical
		// t.TempDir() under /var/folders/... already eats ~95
		// bytes, leaving no room for "sockets/<hash>.sock".
		// Without a fallback the ENOENT/EINVAL from bind would
		// surface deep inside extension setup; the runner
		// should silently relocate to a shorter path instead.
		long := strings.Repeat("a", 90)
		deep := &Runner{dataDir: filepath.Join(string(filepath.Separator), long)}
		uri, err := workspaceapi.ParseURI("ssh://example.com/srv/app")
		require.NoError(t, err)

		got := deep.socketPath(uri)

		assert.Less(t, len(got), 104,
			"fallback socket %q must fit in sun_path", got)
		assert.Equal(t, filepath.Dir(got), filepath.Clean(os.TempDir()),
			"fallback should live directly under os.TempDir(); got %q", got)
		assert.True(t, strings.HasPrefix(filepath.Base(got), debug.Package+"-"),
			"fallback basename should be prefixed with %q to namespace it; "+
				"got %q", debug.Package, filepath.Base(got))
	})
}
