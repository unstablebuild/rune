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
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/blue/retry"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/ide/ideauthorizer"
	"unstable.build/rune/internal/llm/llmrpc"
	"unstable.build/rune/internal/rpc"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/workspace/processctx"
)

// NewRunner returns a Runner with a simple protocol that
// initially exchanges metadata and secrets over stdin/stdout and secures
// resources via TLS and per rpc authentication/authorization.
func NewRunner(
	ctx context.Context, locker sync.Locker, dataDir string, opts ...Option,
) (*Runner, error) {
	return newRunner(locker, dataDir, generateRunnerCert, opts...)
}

func newRunner(
	locker sync.Locker, dataDir string, generateCert certGenerator, opts ...Option,
) (*Runner, error) {
	ret := &Runner{
		locker:  locker,
		dataDir: dataDir,
		opts:    opts,
	}
	var err error
	ret.keys, err = auth.GenerateKeys()
	if err != nil {
		return nil, fmt.Errorf("generate keys: %w", err)
	}
	for _, o := range opts {
		o(&ret.cfg)
	}
	if !ret.cfg.insecureTransport {
		ret.cert = generateCertAsync(generateCert)
	}
	return ret, nil
}

const certExpiresIn = 10 * 24 * 365 * time.Hour

type certGenerator func() (certPEM, keyPEM []byte, err error)

// generateRunnerCert issues the TLS cert shared by every workspace
// extension server in this process. Extensions pin it via RootCAs with
// hostname verification disabled, so the SAN does not need to match the
// socket and one cert can serve every workspace.
func generateRunnerCert() (certPEM, keyPEM []byte, err error) {
	return auth.GenerateSelfSignedCert(
		[]string{"rune"}, pkix.Name{CommonName: "ox"}, certExpiresIn)
}

// asyncCert holds the result of a cert generation started in the
// background. RSA-4096 keygen costs close to a second, so the runner
// starts it at construction and only blocks on it when the first
// workspace server needs the cert.
type asyncCert struct {
	done chan struct{}
	cert []byte
	key  []byte
	err  error
}

func generateCertAsync(generate certGenerator) *asyncCert {
	ret := &asyncCert{done: make(chan struct{})}
	go debug.CapturePanicReport(func() {
		defer close(ret.done)
		ret.cert, ret.key, ret.err = generate()
	})
	return ret
}

func (c *asyncCert) wait() (certPEM, keyPEM []byte, err error) {
	<-c.done
	return c.cert, c.key, c.err
}

// Runner implements the extension host gRPC server lifecycle.
type Runner struct {
	opts    []Option
	cfg     runnerConfig
	locker  sync.Locker
	keys    auth.Keys
	dataDir string
	// cert is nil when the transport is insecure.
	cert *asyncCert
}

// TrustVerifier attests that an extension entrypoint belongs to a verified
// installed package and returns its signing-key fingerprint.
type TrustVerifier interface {
	VerifyExtensionEntrypoint(path string) (fingerprint string, ok bool)
}

// WorkspaceExtensionsRunner creates an extension runner for a workspace. It
// starts a gRPC server that hosts the extension resources and returns a runner
// that can launch extensions and execute commands in the workspace.
func (r *Runner) WorkspaceExtensionsRunner(
	uri workspaceapi.URI, res map[extensionapi.Permission]extension.ResourceRegistrar,
	authorizer *ideauthorizer.Authorizer,
	trustVerifier TrustVerifier,
	dataDir, installDir string, notifications browser.Notifications,
	executor, extExecutor schemeapi.Executor,
	grantor extension.Grantor,
	editor text.Editor,
	promptOpener ideauthorizer.PromptOpener, storage storageapi.Service,
	scheduleNextTick func(func()) bool,
) (extension.Runner, error) {
	if trustVerifier == nil {
		panic("extensionv2.WorkspaceExtensionsRunner: nil trust verifier")
	}
	var ret wrapCloser
	ret.URI = uri

	var cert, key []byte
	if !r.cfg.insecureTransport {
		var err error
		if cert, key, err = r.cert.wait(); err != nil {
			return nil, fmt.Errorf("generate tls cert: %w", err)
		}
	}

	listener, err := r.newUnixListener(uri)
	if err != nil {
		return nil, fmt.Errorf("create unix listener: %w", err)
	}
	socket := listener.Addr().String()
	listener = newPeerProcessListener(listener)

	streamInterceptors := []grpc.StreamServerInterceptor{
		rpc.StreamReportRecoveryInterceptor(),
		extensionIDStreamInterceptor(),
	}
	unaryInterceptors := []grpc.UnaryServerInterceptor{
		rpc.UnaryReportRecoveryInterceptor(),
	}
	streamInterceptors = append(streamInterceptors, r.cfg.extraStreamInterceptors...)
	unaryInterceptors = append(unaryInterceptors, r.cfg.extraUnaryInterceptors...)
	if log.IsLevelEnabled(log.DebugLevel) {
		fields := []logging.Field{
			{Key: logging.KeyClass, Value: "grpc.Server"},
			{Key: "workspace", Value: uri.String()},
		}
		streamInterceptors = append(streamInterceptors, rpc.StreamLoggingInterceptor(fields))
		unaryInterceptors = append(unaryInterceptors, rpc.UnaryLoggingInterceptor(fields))
	}
	opts := []grpc.ServerOption{
		grpc.ChainStreamInterceptor(streamInterceptors...),
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
		grpc.MaxRecvMsgSize(llmrpc.MaxRecvMsgSize),
		grpc.MaxSendMsgSize(llmrpc.MaxSendMsgSize),
	}
	if !r.cfg.insecureTransport {
		tlsCert, err := tls.X509KeyPair(cert, key)
		if err != nil {
			if cerr := listener.Close(); cerr != nil {
				err = multierror.Append(err, cerr)
			}
			return nil, fmt.Errorf("load tls credentials from cert and key: %w", err)
		}
		cfg := tls.Config{
			Certificates:       []tls.Certificate{tlsCert},
			InsecureSkipVerify: true,
		}
		creds := credentials.NewTLS(&cfg)
		if r.cfg.insecureAuth {
			opts = append(opts, grpc.Creds(creds))
		} else {
			opts = append(opts, authorizer.GRPCAuthServerOptions(r.keys, creds)...)
		}
	} else if !r.cfg.insecureAuth {
		opts = append(opts, authorizer.GRPCAuthServerOptions(r.keys, nil)...)
	}
	ret.srv = grpc.NewServer(opts...)
	for _, registrar := range res {
		closer, rerr := registrar.Register(ret.srv, r.locker)
		if rerr != nil {
			err = multierror.Append(err, rerr)
			continue
		}
		ret.closers = append(ret.closers, closer)
	}
	if err != nil {
		if cerr := ret.Close(); cerr != nil {
			err = multierror.Append(err, cerr)
			return nil, err
		}
	}

	go debug.CapturePanicReport(func() {
		_ = ret.srv.Serve(listener)
	})

	ret.workspaceRunner = newWorkspaceRunner(
		executor, extExecutor, grantor, trustVerifier, uri,
		socket, r.dataDir, installDir, cert, r.keys, r.opts...)
	if err != nil {
		err = fmt.Errorf("new workspace runner: %w", err)
		if cerr := ret.Close(); cerr != nil {
			err = multierror.Append(err, cerr)
			return nil, err
		}
		return nil, err
	}
	if err := registerExtensionsREPLCommand(ret.workspaceRunner, editor); err != nil {
		err = fmt.Errorf("register extensions repl command: %w", err)
		if cerr := ret.Close(); cerr != nil {
			err = multierror.Append(err, cerr)
			return nil, err
		}
		return nil, err
	}
	return ret, nil
}

func extensionIDStreamInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, stream grpc.ServerStream, info *grpc.StreamServerInfo,
		handler grpc.StreamHandler) error {
		claims, ok := auth.ClaimsFromContext[ideauthorizer.Extension](stream.Context())
		if ok && claims.Extra.ExtensionID != "" {
			stream = contextServerStream{
				ServerStream: stream,
				ctx: processctx.ContextWithExtensionID(
					stream.Context(), claims.Extra.ExtensionID),
			}
		}
		return handler(srv, stream)
	}
}

type contextServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s contextServerStream) Context() context.Context {
	return s.ctx
}

func (r *Runner) newUnixListener(uri workspaceapi.URI) (ret net.Listener, err error) {
	ctx := context.Background()
	socket := r.socketPath(uri)
	// Create the directory up front rather than on ENOENT: Windows reports a
	// missing parent directory from bind(2) as WSAENETDOWN.
	if err := os.MkdirAll(filepath.Dir(socket), 0766); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	err = retry.Retry(ctx, retrySocketStrategy, func(context.Context) (bool, error) {
		var cfg net.ListenConfig
		ret, err = cfg.Listen(ctx, "unix", socket)
		if err == nil {
			return false, nil
		}

		if errors.Is(err, syscall.EACCES) {
			return false, err
		}

		_ = os.Remove(socket)
		return true, err
	})
	return
}

// socketPath returns the local filesystem path where the unix listener
// for the given workspace's extension server should live. Extensions
// are always run on the IDE host (by design: the user owns extensions,
// not the remote system), so the socket is always a local path
// regardless of the workspace's scheme.
//
// The basename is a stable, content-addressed hash of the workspace
// URI. That gives us:
//   - determinism: re-opening the same workspace reuses the same
//     socket name, so a stale socket from a previous run can be
//     replaced cleanly (newUnixListener already does the unlink on
//     EADDRINUSE retry).
//   - portability: hex output is restricted to [0-9a-f] which is
//     accepted by every filesystem we care about.
//   - bounded length: 16 hex chars (8 bytes / 64 bits of SHA-256)
//     leaves plenty of headroom under the unix-socket path limit
//     (104 on macOS, 108 on Linux). 64 bits is enough entropy to
//     avoid collisions across the workspaces a single user opens;
//     the dataDir itself is per-user.
//
// If <dataDir>/sockets/<hash>.sock would exceed the OS' sun_path
// limit (notoriously short on macOS at 104 bytes including NUL), we
// fall back to placing the socket directly under os.TempDir(). That
// preserves the determinism property (same URI ⇒ same name) while
// guaranteeing we always produce a bindable path even when dataDir
// itself is deeply nested (e.g. inside a t.TempDir()).
func (r *Runner) socketPath(uri workspaceapi.URI) string {
	sum := sha256.Sum256([]byte(uri.String()))
	name := hex.EncodeToString(sum[:8]) + ".sock"
	primary := filepath.Join(r.dataDir, "sockets", name)
	if len(primary) < sunPathMax {
		return primary
	}
	return filepath.Join(os.TempDir(), debug.Package+"-"+name)
}

// sunPathMax is the conservative upper bound for the sockaddr_un
// sun_path field across the platforms we target: 104 on macOS (incl.
// NUL), 108 on Linux. Using the smaller value means a path that fits
// here fits everywhere.
const sunPathMax = 104

var _ schemeapi.Executor = wrapCloser{}

type wrapCloser struct {
	workspaceapi.URI
	*workspaceRunner
	closers []io.Closer
	srv     *grpc.Server
}

func (m wrapCloser) Signal(pid workspaceapi.Pid, sig syscall.Signal) error {
	return m.workspaceRunner.Signal(pid, sig)
}

func (m wrapCloser) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	workspaceapi.Pid, error,
) {
	return m.workspaceRunner.StartCommand(ctx, cmd)
}

func (w wrapCloser) Close() (ret error) {
	log.Tracef("closing workspace %q extensions", w.URI.String())
	for _, closer := range w.closers {
		if err := closer.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if w.workspaceRunner != nil {
		if err := w.workspaceRunner.Close(); err != nil {
			ret = multierror.Append(ret, err)
		}
	}
	if w.srv != nil {
		w.srv.Stop() // stop closes listener
	}
	return
}

var retrySocketStrategy = retry.CombinedStrategy(
	retry.LimitStrategy(4),
	retry.ExponentialStrategy(time.Millisecond, time.Second),
)
