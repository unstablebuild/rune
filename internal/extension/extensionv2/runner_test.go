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
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/text/texttest"
)

// shortTempDir returns a temp dir short enough that
// <dir>/sockets/<hash>.sock fits in sun_path; t.TempDir() bakes the
// test name into the path and overflows it on macOS.
func shortTempDir(t *testing.T) string {
	t.Helper()
	base, err := os.MkdirTemp("", "rn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	return base
}

// ecdsaSelfSignedCert is a fast stand-in for auth.GenerateSelfSignedCert,
// whose RSA-4096 keygen takes close to a second.
func ecdsaSelfSignedCert() (certPEM, keyPEM []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

func newTestWorkspaceExtensionsRunner(
	t *testing.T, r *Runner, uri workspaceapi.URI,
) (extension.Runner, error) {
	t.Helper()
	exec := &recordingExecutor{}
	runner, err := r.WorkspaceExtensionsRunner(
		uri, nil, nil, nopTrustVerifier{}, r.dataDir, r.dataDir, nil,
		exec, exec, extension.GrantAll(), texttest.NopEditor(), nil, nil,
		func(fn func()) bool { fn(); return true },
	)
	if err == nil {
		t.Cleanup(func() { _ = runner.Close() })
	}
	return runner, err
}

func envValue(env []string, key string) string {
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestRunnerSharesOneCertAcrossWorkspaces(t *testing.T) {
	t.Parallel()

	var generated atomic.Int32
	r, err := newRunner(new(sync.Mutex), shortTempDir(t), func() ([]byte, []byte, error) {
		generated.Add(1)
		return ecdsaSelfSignedCert()
	}, WithInsecureAuth())
	require.NoError(t, err)

	var certs []string
	for _, raw := range []string{"file:///home/me/shared-a", "file:///home/me/shared-b"} {
		uri, err := workspaceapi.ParseURI(raw)
		require.NoError(t, err)
		runner, err := newTestWorkspaceExtensionsRunner(t, r, uri)
		require.NoError(t, err)
		env, err := runner.(wrapCloser).commandEnvs(context.Background(), "ext", nil)
		require.NoError(t, err)
		certs = append(certs, envValue(env, "RUNE_CERT"))
	}

	assert.NotEmpty(t, certs[0], "extensions must be handed the tls cert")
	assert.Equal(t, certs[0], certs[1],
		"every workspace runner must hand extensions the same process-wide cert")
	assert.Equal(t, int32(1), generated.Load(), "cert must be generated once per process")
}

func TestRunnerSurfacesCertGenerationErrorAtFirstUse(t *testing.T) {
	t.Parallel()

	r, err := newRunner(new(sync.Mutex), shortTempDir(t), func() ([]byte, []byte, error) {
		return nil, nil, errors.New("keygen exploded")
	}, WithInsecureAuth())
	require.NoError(t, err, "the async generator must not fail construction")

	uri, err := workspaceapi.ParseURI("file:///home/me/cert-error")
	require.NoError(t, err)
	_, err = newTestWorkspaceExtensionsRunner(t, r, uri)
	require.ErrorContains(t, err, "keygen exploded")
}

func TestRunnerInsecureTransportSkipsCertGeneration(t *testing.T) {
	t.Parallel()

	r, err := newRunner(new(sync.Mutex), shortTempDir(t), func() ([]byte, []byte, error) {
		t.Error("cert generator must not run with insecure transport")
		return nil, nil, nil
	}, WithInsecureAuth(), WithInsecureTransport())
	require.NoError(t, err)

	uri, err := workspaceapi.ParseURI("file:///home/me/insecure")
	require.NoError(t, err)
	runner, err := newTestWorkspaceExtensionsRunner(t, r, uri)
	require.NoError(t, err)
	env, err := runner.(wrapCloser).commandEnvs(context.Background(), "ext", nil)
	require.NoError(t, err)
	assert.Empty(t, envValue(env, "RUNE_CERT"))
}

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

	dataDir := filepath.Join(shortTempDir(t), "d")
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
