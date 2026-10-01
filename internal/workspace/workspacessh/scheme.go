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
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	multierr "github.com/ernestrc/go-multierror"
	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi/workspacerpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/remotescheme"
)

const (
	// Scheme represents the URL scheme that this package implements
	Scheme = "ssh"

	// remotePathEnv is prepended to every remote exec string so the
	// supported install location (install.sh symlinks the binary to
	// ~/.local/bin/rune) is found even when the remote's PATH lacks
	// ~/.local/bin — the norm for sshd exec sessions, which run
	// non-login shells that skip the profile files where
	// distributions add ~/.local/bin.
	remotePathEnv = `PATH="$HOME/.local/bin:$PATH"`

	debugPathError = "You can run %q to troubleshoot this. Also, double check your workspace.ssh.command " +
		"and workspace.ssh.shell configuration, if you have any."
)

// ErrSSHConnectionClosed is reported by the gRPC dialer's close
// hook when the underlying SSH transport drops mid-session.
// Exposed so callers can match it via errors.Is.
var ErrSSHConnectionClosed = errors.New("ssh connection closed unexpectedly")

// Option customizes the ssh scheme constructed by New.
type Option func(*scheme)

// WithProvisionManifest supplies a callback that returns an encoded
// package-provisioning manifest (see cmd/rune provisionManifest). When it
// returns a non-empty string, the remote `rune -x` server is asked to mirror
// the local toolchain via a `--install <manifest>` flag. The callback is
// invoked once per connection so the manifest reflects the current local
// install state.
func WithProvisionManifest(fn func() string) Option {
	return func(s *scheme) {
		s.provisionFn = fn
	}
}

// WithRemoteDataDir forces the remote `rune -x` server to use ~/<name> as
// its data directory via an explicit `--datadir` flag, instead of relying
// on the remote's default ($HOME/.rune). name is a bare directory name
// (e.g. ".rune" or ".runedev"), taken from the local IDE's data-directory
// basename so the local client and the remote server provision into the
// same well-known location by construction. Empty name leaves the remote
// default untouched.
func WithRemoteDataDir(name string) Option {
	return func(s *scheme) {
		s.remoteDataDir = name
	}
}

// New returns a schemeapi.SchemeFunc capable of managing files over an ssh
// connection. ui drives the interactive auth flow (passphrase / password /
// kbd-interactive prompts). It is intended to be installed into a workspace
// manager:
//
//	mgr.RegisterScheme(workspacessh.Scheme, workspacessh.New(ui))
//
// New panics if ui is nil: a real UI is mandatory because the ssh dial may
// trigger interactive prompts that have no useful default.
func New(ui UI, opts ...Option) schemeapi.SchemeFunc {
	if ui == nil {
		panic("workspacessh.New: ui is required")
	}
	return func(ctx context.Context, cfg config.Config, uri workspaceapi.URI) (schemeapi.Scheme, error) {
		return newScheme(ctx, cfg, uri, ui, opts...)
	}
}

type remote interface {
	NewSession() (schemeapi.Executor, error)
	Close() error
}

var _ workspace.RemoteScheme = (*scheme)(nil)

type scheme struct {
	cfg      sshConfig
	hostPort string
	user     string
	homedir  string
	basePath string

	getUser         func() (*user.User, error)
	remoteFn        func(context.Context, sshConfig, workspaceapi.URI) (remote, error)
	connectSchemeFn remotescheme.ConnectFn
	ctx             context.Context
	cancelCtx       func()
	ui              UI
	provisionFn     func() string
	passCache       *passwordCache
	hostKeyPin      *sessionHostKeyPin
	remoteDataDir   string

	schemeapi.Scheme
}

func newScheme(
	ctx context.Context, ccfg config.Config, uri workspaceapi.URI, ui UI,
	opts ...Option,
) (*scheme, error) {
	if ui == nil {
		panic("workspacessh.newScheme: ui is required")
	}
	ret := new(scheme)
	ret.ctx, ret.cancelCtx = context.WithCancel(context.Background())
	for _, opt := range opts {
		opt(ret)
	}

	cc, err := fromConfig(ccfg)
	if err != nil {
		return nil, err
	}

	ret.getUser = user.Current
	ret.ui = ui
	ret.passCache = new(passwordCache)
	ret.hostKeyPin = new(sessionHostKeyPin)
	if cc.command == "" {
		ret.remoteFn = func(c context.Context, sc sshConfig, u workspaceapi.URI) (remote, error) {
			return newStdRemote(c, sc, u, ret.ui, ret.passCache, ret.hostKeyPin)
		}
	} else {
		ret.remoteFn = newProcRemote
	}

	ret.connectSchemeFn = ret.connectScheme
	err = ret.init(ctx, cc, uri)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func parseWorkspaceURI(u workspaceapi.URI, getUser func() (*user.User, error)) (
	username, homedir, hostPort, basePath string, err error,
) {
	username = u.User()

	// respect empty username for URI creation
	// but we need its implicit value for ~ expansion to work
	usernameForHomeDir := username
	if usernameForHomeDir == "" {
		var u *user.User
		u, err = getUser()
		if err != nil {
			return
		}
		usernameForHomeDir = u.Username
	}
	// I doubt we'll ever ssh into a non-linux host. root's home is /root,
	// not /home/root, on every standard Linux distribution.
	if usernameForHomeDir == "root" {
		homedir = "/root"
	} else {
		homedir = filepath.Join("/", "home", usernameForHomeDir)
	}

	basePath, err = workspaceapi.ExpandPath(u.Path(), func() (*user.User, error) {
		return &user.User{Username: username, HomeDir: homedir}, nil
	}, func() (string, error) {
		// return host's base path, but this should never happen
		return "/", nil
	})
	if err != nil {
		return
	}

	hostPort = u.Host()
	if hostPort == "" {
		err = errors.New("ssh scheme with empty host is invalid")
		return
	}

	return
}

func (s *scheme) runAndWait(
	ctx context.Context,
	remote remote, cmdStr string, args ...string,
) (string, bool, error) {
	ses, err := remote.NewSession()
	if err != nil {
		return "", false, fmt.Errorf("new session: %v", err)
	}
	closed := false
	closeSession := func() error {
		if closed {
			return nil
		}
		closed = true
		err := ses.Close()
		if err != nil && err != io.EOF {
			return fmt.Errorf("close session: %v", err)
		}
		return nil
	}
	defer func() { _ = closeSession() }()

	if s.cfg.shell != "" {
		args = append([]string{"-c", cmdStr}, args...)
		cmdStr = s.cfg.shell
	}
	// The env assignment is applied to the outermost command word so
	// it survives the optional shell wrap above: the remote shell
	// that parses the exec string expands $HOME and the child
	// process inherits the amended PATH.
	cmdStr = remotePathEnv + " " + cmdStr
	var stderr, stdout bytes.Buffer
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    cmdStr,
		Args:    args,
		Stderr:  &stderr,
		Stdout:  &stdout,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	}
	_, err = ses.StartCommand(ctx, cmd)
	if err != nil {
		return "", false, fmt.Errorf("start command: %v", err)
	}

	select {
	case err = <-ch:
	case <-ctx.Done():
		_ = closeSession()
		return "", false, ctx.Err()
	}
	if err != nil {
		if stdout.Len() != 0 {
			err = fmt.Errorf("stdout: %v: %s", err, stdout.String())
		}
		if stderr.Len() != 0 {
			err = fmt.Errorf("stderr: %v: %s", err, stderr.String())
		}
		if procRemote, ok := remote.(*procRemote); ok {
			cmd, args := procRemote.CommandString(cmdStr, args...)
			return fmt.Sprintf("%s %s", cmd, strings.Join(args, " ")), false, err
		}
		return "", false, err
	}

	if err := closeSession(); err != nil {
		return "", false, err
	}
	return "", true, nil
}

func (s *scheme) whichCommand(ctx context.Context, remote remote, cmd string) error {
	cmdAndArgs, avail, err := s.runAndWait(ctx, remote, "which", cmd)
	if err != nil {
		err = fmt.Errorf("could not check if %s executable is in PATH: %w", cmd, err)
	}
	if !avail {
		errStr := "%q executable was not found on remote. " +
			"Make sure it's installed at ~/.local/bin/rune or available " +
			"via $PATH to a non-interactive shell. "
		if err != nil {
			errStr = fmt.Sprintf("%s %v. ", errStr, err)
		}
		if cmdAndArgs != "" {
			return fmt.Errorf(errStr+debugPathError, cmd, cmdAndArgs)
		}
		return fmt.Errorf(errStr, cmd)
	}
	return nil
}

func (s *scheme) workspaceExists(
	ctx context.Context, remote remote, uri workspaceapi.URI,
) error {
	cmdAndArgs, ok, err := s.runAndWait(ctx, remote, "ls", uri.Path())
	if err != nil {
		return fmt.Errorf("could not check if workspace path %q exists: %w", uri.Path(), err)
	}
	if !ok {
		return fmt.Errorf("path %q was not found on remote "+ //nolint:staticcheck
			debugPathError, uri.Path(), cmdAndArgs)
	}
	return nil
}

func (s *scheme) connectScheme(
	ctx context.Context, uri workspaceapi.URI, closeHook func(error),
) (schemeapi.Scheme, error) {
	// remoteWorkspaceServerBin is the binary name we expect to find on
	// the remote host. It is the `rune` binary started in workspace
	// server mode (see cmd/rune/main.go: --workspace-server / -x).
	const remoteWorkspaceServerBin = "rune"

	sshPath := s.basePath
	if sshPath == "" {
		sshPath = "."
	}

	remote, err := s.remoteFn(ctx, s.cfg, uri)
	if err != nil {
		return nil, fmt.Errorf("could not initialize remote: %w", err)
	}
	// Each attempt dials its own remote. Until the monitor goroutine below
	// takes ownership, a failed attempt must release it and the pipes, or
	// every maintainConnection retry leaks an SSH connection and six fds.
	var closers []io.Closer
	handedOff := false
	defer func() {
		if handedOff {
			return
		}
		for _, closer := range closers {
			_ = closer.Close()
		}
		_ = remote.Close()
	}()

	// NOTE: the next checks are to avoid error messages getting lost when
	// trying to connect so we can provide better error messages
	//
	// Skipped entirely when the user opts in via
	// `workspace.ssh.skip_preflight = True` — typically to play nicely
	// with servers that have a tight MaxSessions budget, since each
	// pre-flight probe opens its own session channel.
	if !s.cfg.skipPreflight {
		err = s.whichCommand(ctx, remote, remoteWorkspaceServerBin)
		if err != nil {
			return nil, err
		}

		err = s.workspaceExists(ctx, remote, uri)
		if err != nil {
			return nil, err
		}
	}

	ses, err := remote.NewSession()
	if err != nil {
		return nil, fmt.Errorf("NewSession: %v", err)
	}

	var extraArgs []string
	if log.IsLevelEnabled(log.TraceLevel) {
		extraArgs = []string{"-p", "-o", "rune-workspace-server.log"}
	}

	var dataDirArgs []string
	if s.remoteDataDir != "" {
		// The arg runs through the remote shell (goSshSession.StartCommand
		// joins Path+Args into one command string), so ~ is expanded on the
		// remote host. remoteDataDir is a bare, shell-safe directory name.
		dataDirArgs = []string{"--datadir", "~/" + s.remoteDataDir}
	}

	var installArgs []string
	if s.cfg.provisionPackages && s.provisionFn != nil {
		if manifest := s.provisionFn(); manifest != "" {
			// manifest is a single validated shell-safe token
			// (cmd/rune enforces the [A-Za-z0-9._@,%+~/-] class), so it
			// needs no quoting even though the remote shell reparses the
			// whole command.
			installArgs = []string{"--install", manifest}
		}
	}

	cmdStr := remoteWorkspaceServerBin
	args := append([]string{"-x", sshPath}, dataDirArgs...)
	args = append(args, installArgs...)
	args = append(args, extraArgs...)
	if s.cfg.shell != "" {
		args = append([]string{"-c", cmdStr}, args...)
		cmdStr = s.cfg.shell
	}
	cmdStr = remotePathEnv + " " + cmdStr
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    cmdStr,
		Args:    args,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	}

	stdoutRead, stderrRead, stdinWrite, closers, err := setPipes(&cmd, os.Pipe)
	if err != nil {
		return nil, fmt.Errorf("could not create pipes: %s", err)
	}

	// context of command should mirror the lifcycle of this scheme
	// not the ctx passed to this constructor, which could have
	// a connection timeout (i.e. retry ctx)
	_, err = ses.StartCommand(s.ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("could not create command: %s", err)
	}

	// During remote provisioning (before StartSchemeServer runs) stderr is the
	// only live back-channel: stdout is the gRPC pipe and does not serve yet.
	// Read it line by line so structured progress lines surface as browser
	// notifications immediately, while plain lines accumulate in a bounded tail
	// for the exit-error path below. This goroutine owns stderrRead, so the
	// exit path must read the tail instead of the pipe to avoid two readers
	// fighting over it.
	tail := newStderrTail()
	stderrDone := make(chan struct{})
	ready := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(stderrDone)
		s.scanRemoteStderr(stderrRead, tail, ready)
	})

	conn, err := grpc.Dial("",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		// Detect dead SSH transports promptly: without keepalive
		// pings, a remote save (Rename, Stat, ...) can block
		// indefinitely when the transport is silently broken.
		grpc.WithKeepaliveParams(remotescheme.ClientKeepalive),
		grpc.WithContextDialer(func(_ context.Context, addr string) (net.Conn, error) {
			return newStdConn(
				log.StandardLogger(), stdoutRead, stdinWrite, false, /* stdio */
				func() {
					closeHook(ErrSSHConnectionClosed)
				})
		}))
	if err != nil {
		return nil, err
	}

	// The remote runs a provisioning phase (mirror toolchain, install
	// packages, load config, apply env) before StartSchemeServer begins
	// serving on stdout. Returning a client now would let the first RPC block
	// in gRPC waitOnHeader for the whole provisioning duration — or forever if
	// provisioning stalls. Block until the remote signals it is about to serve
	// (ServerReady on stderr), or fail early if it exits, this connection
	// attempt is cancelled (retry abort / remote-scheme shutdown), or the
	// scheme is torn down first. This runs in the build/maintainConnection
	// goroutine, so the event loop is never blocked; on failure
	// maintainConnection retries.
	//
	// ctx is the per-attempt context maintainConnection derives from the
	// remoteScheme lifetime; it MUST be honored here so IDE shutdown (which
	// cancels that context and then waits for this goroutine to finish) does
	// not deadlock against a remote that never becomes serving-ready.
	var exitErr error
	select {
	case <-ready:
	case exitErr = <-ch:
		if exitErr == nil {
			exitErr = fmt.Errorf("remote exited before serving")
		}
	case <-ctx.Done():
		exitErr = ctx.Err()
	case <-s.ctx.Done():
		exitErr = s.ctx.Err()
	}
	if exitErr != nil {
		// The scanner goroutine drains stderrRead; wait for it to observe EOF
		// (process exit closed the write end) so the tail is complete before
		// formatting the error. Guard with the attempt and scheme contexts so
		// a hung remote that never closes stderr cannot block teardown.
		select {
		case <-stderrDone:
		case <-ctx.Done():
		case <-s.ctx.Done():
		}
		// conn was never wrapped in a workspacerpc.Client, so nothing else
		// owns it; close it here to release its background gRPC goroutines.
		_ = conn.Close()
		return nil, fmt.Errorf("error executing remote rune workspace "+
			"server over SSH: %s: %s", exitErr, tail.String())
	}

	handedOff = true
	go debug.CapturePanicReport(func() {
		defer remote.Close()
		// Wait for the remote process to exit OR for the scheme's
		// own context to be cancelled. Without the second case, a
		// hung remote (or a Watcher that never delivers the exit
		// status — e.g. when the watcher's send blocks because
		// nothing is reading) would leak this goroutine and the
		// associated SSH session for the lifetime of the test
		// binary.
		var err error
		select {
		case err = <-ch:
		case <-s.ctx.Done():
		}
		if err != nil {
			// The scanner goroutine drains stderrRead; wait for it to
			// observe EOF (process exit closed the write end) so the tail
			// is complete before formatting the error.
			<-stderrDone
			err = fmt.Errorf("error executing remote rune workspace "+
				"server over SSH: %s: %s", err, tail.String())
		}
		closeHook(err)
		for _, closer := range closers {
			_ = closer.Close()
		}
	})

	return workspacerpc.NewClient(s.ctx, conn), nil
}

// scanRemoteStderr reads the remote server's stderr line by line until EOF.
// Structured provisioning progress lines drive a single live progress
// notification (index/total → progress bar); the ServerReady line closes ready
// exactly once to unblock connectScheme; every other line is appended to tail,
// which the exit-error path reads to build the human-readable failure message.
// Progress and ready lines are control lines and never leak into the tail.
func (s *scheme) scanRemoteStderr(r io.Reader, tail *stderrTail, ready chan struct{}) {
	scanner := bufio.NewScanner(r)
	// Allow long remote stderr lines (default is 64 KiB, but a stack trace or
	// long path can exceed that). Cap growth to keep memory bounded.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var progress provisionProgressNotifier
	var readyClosed bool
	for scanner.Scan() {
		line := scanner.Bytes()
		if p, ok := ParseProvisionProgressLine(line); ok {
			progress.report(s.ui, p)
			continue
		}
		if parseServerReadyLine(line) {
			if !readyClosed {
				// Serving-ready is the single close point for the provisioning
				// bar: it spans connecting → serving, so the bar stays open
				// through the silent post-install finalize phase and closes
				// exactly when the remote is about to serve.
				progress.finish(s.ui)
				close(ready)
				readyClosed = true
			}
			continue
		}
		tail.append(line)
	}
}

// provisionProgressBarTotal is the synthetic denominator for the provisioning
// progress bar. The bar tracks the whole connecting → serving wait on a single
// fractional scale, so it needs a fixed total independent of the package count.
const provisionProgressBarTotal = 100

// Fraction boundaries on the [0, provisionProgressBarTotal] scale. The package
// install/download work occupies [0, packageRegionEnd]; the post-install
// config/env finalize phase advances to finalizeFraction. The bar only reaches
// provisionProgressBarTotal when serving-ready closes it, so it never
// disappears mid-provision.
const (
	packageRegionEnd = 90
	finalizeFraction = 95
)

// provisionProgressNotifier maps the stream of ProvisionProgress lines onto a
// single live progress notification whose bar advances monotonically across the
// entire provisioning lifecycle (installing → downloading → activating per
// package → finalizing → serving). The bar is held strictly below the synthetic
// total until finish (serving-ready) closes it, so it stays visible through the
// otherwise-silent finalize phase instead of vanishing when installs complete.
// A failed package is additionally surfaced as its own warning notification so
// it is not lost inside the info-level progress bar.
type provisionProgressNotifier struct {
	id       string
	started  bool
	progress int
}

func (n *provisionProgressNotifier) report(ui UI, p ProvisionProgress) {
	if p.Phase == ProvisionPhaseFailed {
		ui.Notify(NotificationWarning, p.Message())
	}
	if !n.started {
		n.id = ui.Notify(NotificationInfo, p.Message())
		n.started = true
	}
	n.advance(ui, p.Message(), n.fractionFor(p))
}

// fractionFor maps a progress line to a point on the [0, provisionProgressBarTotal]
// scale. Missing sub-progress (Of == 0) or a missing package count is handled by
// holding the package's base fraction, so old-shape lines and malformed lines
// still render a sensible bar.
func (n *provisionProgressNotifier) fractionFor(p ProvisionProgress) int {
	switch p.Phase {
	case ProvisionPhaseFinalizing:
		return finalizeFraction
	case ProvisionPhaseDone:
		return packageRegionEnd
	}
	if p.Total <= 0 || p.Index <= 0 {
		return 0
	}
	slice := float64(packageRegionEnd) / float64(p.Total)
	base := float64(p.Index-1) * slice
	switch p.Phase {
	case ProvisionPhaseActivating, ProvisionPhaseFailed:
		return int(base + slice)
	case ProvisionPhaseDownloading:
		if p.Of > 0 {
			base += slice * float64(p.Done) / float64(p.Of)
		}
		return int(base)
	default: // installing and unknown phases hold the package base fraction
		return int(base)
	}
}

// advance clamps value into a monotonic, strictly-below-total range and pushes
// it to the notification. Holding below provisionProgressBarTotal keeps the bar
// open until finish closes it at serving-ready.
func (n *provisionProgressNotifier) advance(ui UI, message string, value int) {
	if value < n.progress {
		value = n.progress
	}
	if value >= provisionProgressBarTotal {
		value = provisionProgressBarTotal - 1
	}
	n.progress = value
	ui.UpdateNotificationProgress(n.id, message, value, provisionProgressBarTotal)
}

// finish completes and closes the progress bar. It is a no-op when no
// provisioning line ever opened the bar (e.g. a launch with no --install
// manifest), so a plain serving-ready launch does not synthesize a bar.
func (n *provisionProgressNotifier) finish(ui UI) {
	if !n.started {
		return
	}
	ui.UpdateNotificationProgress(n.id, "Workspace ready",
		provisionProgressBarTotal, provisionProgressBarTotal)
}

// stderrTailCap bounds the human-readable stderr the local side retains, so a
// chatty remote cannot grow the buffer without limit. Only the most recent
// bytes are kept, which is what a failure message needs.
const stderrTailCap = 8 * 1024

// stderrTail is a bounded, concurrency-safe buffer holding the last
// stderrTailCap bytes of non-progress stderr for the exit-error message.
type stderrTail struct {
	mu  sync.Mutex
	buf []byte
}

func newStderrTail() *stderrTail {
	return &stderrTail{}
}

func (t *stderrTail) append(line []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, line...)
	t.buf = append(t.buf, '\n')
	if len(t.buf) > stderrTailCap {
		t.buf = t.buf[len(t.buf)-stderrTailCap:]
	}
}

func (t *stderrTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}

// setPipes creates the command's stdio pipes with newPipe, normally os.Pipe.
// On error it has closed every pipe it created.
func setPipes(
	cmd *workspaceapi.Cmd, newPipe func() (r, w *os.File, err error),
) (stdout, stderr, stdin *os.File, closers []io.Closer, err error) {
	stdoutRead, stdoutWrite, err := newPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("pipe: %w", err)
	}
	stderrRead, stderrWrite, err := newPipe()
	if err != nil {
		closeFiles(stdoutRead, stdoutWrite)
		return nil, nil, nil, nil, fmt.Errorf("pipe: %w", err)
	}
	stdinRead, stdinWrite, err := newPipe()
	if err != nil {
		closeFiles(stdoutRead, stdoutWrite, stderrRead, stderrWrite)
		return nil, nil, nil, nil, fmt.Errorf("pipe: %w", err)
	}
	cmd.Stdout = stdoutWrite
	cmd.Stderr = stderrWrite
	cmd.Stdin = stdinRead
	closers = []io.Closer{
		stdoutWrite, stderrWrite, stdinWrite,
		stdoutRead, stderrRead, stdinRead,
	}
	return stdoutRead, stderrRead, stdinWrite, closers, nil
}

func closeFiles(files ...*os.File) {
	for _, file := range files {
		_ = file.Close()
	}
}

func (s *scheme) init(
	ctx context.Context, cc sshConfig, uri workspaceapi.URI,
) (err error) {
	if uri.Scheme() != Scheme {
		return errors.New("invalid non-ssh scheme")
	}

	s.cfg = cc
	s.user, s.homedir, s.hostPort, s.basePath, err = parseWorkspaceURI(uri, s.getUser)
	if err != nil {
		return fmt.Errorf("could not parse ssh workspaceapi.URI: %s", err)
	}

	// expand any relative path or home aliases
	uri, err = s.URI(uri.Path())
	if err != nil {
		return fmt.Errorf("URI from path %s: %v", uri.Path(), err)
	}

	s.Scheme = remotescheme.New(ctx, s.connectSchemeFn, uri,
		isRetryableConnectError)
	return nil
}

// override to provide user with an error message that guides to a solution
func (s *scheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	pid, err := s.Scheme.StartCommand(ctx, cmd)
	if err != nil && strings.Contains(err.Error(), "executable file not found in $PATH") {
		return 0, fmt.Errorf("%w. Make sure that $PATH is configured "+
			"even for non-interactive shells", err)
	}
	return pid, err
}

func (s *scheme) URI(path string) (workspaceapi.URI, error) {
	absPath, err := s.expandPath(path)
	if err != nil {
		return workspaceapi.URI{}, err
	}

	var uriStr string
	if s.user != "" {
		uriStr = fmt.Sprintf("ssh://%s@%s%s", s.user, s.hostPort, absPath)
	} else {
		uriStr = fmt.Sprintf("ssh://%s%s", s.hostPort, absPath)
	}

	return workspaceapi.ParseURI(uriStr)
}

func (s *scheme) Close() (ret error) {
	if err := s.Scheme.Close(); err != nil {
		ret = multierr.Append(ret, err)
	}
	s.cancelCtx()
	return ret
}

func (s *scheme) OnDisconnect() <-chan struct{} {
	return s.Scheme.(workspace.RemoteScheme).OnDisconnect()
}

func (s *scheme) WaitConnected(ctx context.Context) error {
	return s.Scheme.(workspace.RemoteScheme).WaitConnected(ctx)
}

func (s *scheme) expandPath(path string) (string, error) {
	return workspaceapi.ExpandPath(path, func() (*user.User, error) {
		return &user.User{Username: s.user, HomeDir: s.homedir}, nil
	}, func() (string, error) {
		return s.basePath, nil
	})
}
