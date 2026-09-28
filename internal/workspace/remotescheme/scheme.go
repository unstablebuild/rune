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

// Package remotescheme wraps a remote [schemeapi.Scheme] so it survives
// transport drops: it reconnects in the background, invalidates the
// file descriptors handed out by a previous connection, and reports
// disconnects to the IDE.
package remotescheme

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/bluectx"
	"github.com/unstablebuild/blue/logging"
	"github.com/unstablebuild/blue/retry"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/debug"
)

var (
	retryStrategy = retry.ExponentialStrategy(100*time.Millisecond, 5*time.Second)
)

// Exported sentinel errors so callers (e.g. the VTE reservoir) can
// match transport-level disconnects via errors.Is.
var (
	// ErrLostConnection is reported when an established remote
	// connection drops without a more specific error from the
	// transport.
	ErrLostConnection = errors.New("lost connection to remote")

	// ErrRemoteClosed is reported when the remote scheme is closed
	// explicitly by the caller.
	ErrRemoteClosed = errors.New("remote closed")
)

// ConnectFn establishes one connection to uri. closeHook is invoked by
// the transport when the established connection drops, which triggers a
// reconnect attempt.
type ConnectFn func(ctx context.Context,
	uri workspaceapi.URI, closeHook func(error)) (schemeapi.Scheme, error)

// RetryableFn reports whether a failed connect attempt is worth
// retrying. Permanent failures (rejected credentials, a workspace path
// that does not exist on the remote) must return false so the reconnect
// loop stops instead of pestering the user forever.
type RetryableFn func(error) bool

type state struct {
	lastSessionError error
	scheme           schemeapi.Scheme
	generation       uint64
}

type remoteFileKey struct {
	generation uint64
	fd         uintptr
}

// wraps another schemeapi.Scheme to be resilient against
// intermitent connection failures
type remoteScheme struct {
	closeChan  chan struct{}
	uri        workspaceapi.URI
	ctx        context.Context
	cancelCtx  func()
	currState  atomic.Value
	generation atomic.Uint64
	retryable  RetryableFn
	logClass   string

	// firstAttempt is closed by maintainConnection after the first
	// connect attempt completes (success or failure). state() blocks on
	// this channel so callers don't observe a transient
	// "not connected yet" error before the dial has had a chance to run.
	firstAttempt chan struct{}

	// NOTE this is not a regular map because
	// otherwise we need to worry about synchronizing deletes
	// on runtime finalizer's
	files sync.Map

	// disconnectCh is allocated once at construction and closed
	// exactly once on the first transport drop (or on Close).
	// OnDisconnect always returns this same channel: a stable
	// pre-allocated handle means observers can subscribe at any
	// time without coordination, and once-semantics fall out of
	// "close a channel handed out before anyone read it".
	disconnectCh chan struct{}
	// disconnectOnce guards close(disconnectCh) so repeated
	// closeHook firings or a Close after a drop don't double-close
	// the channel.
	disconnectOnce sync.Once

	// closed is set once Close has run. Once true, the scheme is
	// terminal: setError must not overwrite ErrRemoteClosed and
	// OnDisconnect must always return an already-closed channel so
	// observers do not block on a re-armed channel that will never
	// fire again.
	closed atomic.Bool
}

func (s *remoteScheme) maintainConnection(
	connect ConnectFn, uri workspaceapi.URI,
	closeChan chan struct{},
) {
	logger := log.WithField(logging.KeyClass, s.logClass)

	var firstAttemptDone bool
	_ = retry.Retry(s.ctx, retryStrategy,
		func(ctx context.Context) (bool, error) {

			// cancel if connection is closed for some reason
			// so we can retry below
			ctx, cancel := context.WithCancel(ctx)

			logger.Debugf("attempting to connect to %s", uri)

			// close hook could be called multiple times
			scheme, err := connect(ctx, uri, func(err error) {
				defer cancel()

				select {
				case <-ctx.Done():
					return
				default:
				}
				logger.Warnf("lost connectivity to %s: %s", uri, err)
				s.setError(logger, err)
			})

			generation := s.generation.Load()
			if err == nil {
				generation = s.generation.Add(1)
			}
			prevState := s.currState.Swap(state{
				scheme:           scheme,
				lastSessionError: err,
				generation:       generation,
			})
			if prevState != nil && prevState.(state).scheme != nil {
				err := prevState.(state).scheme.Close()
				logger.Tracef("closed previous remote scheme: %v", err)
			}

			if !firstAttemptDone {
				// signal that the first attempt has completed (success
				// or failure); state() blocks on this so callers see a
				// real error / connected scheme rather than the
				// "not connected yet" sentinel.
				close(s.firstAttempt)
				firstAttemptDone = true
			}

			if err != nil {
				cancel()
				logger.Warnf("failed to connect to %s: %s", uri, err)
				if !s.retryable(err) {
					// Permanent failure: stop reconnecting so we don't
					// pester the user with prompts forever (e.g. the
					// password keeps failing, the host key changed, or
					// the workspace path doesn't exist on the remote).
					return false, err
				}
				return true, err
			}

			logger.Infof("connected to %s", uri)

			select {
			case <-ctx.Done():
				logger.Debugf("connection context to %s is done", uri)
				currState := s.currState.Load()
				var err error
				if currState != nil {
					st := currState.(state)
					if st.lastSessionError == nil {
						err = ErrLostConnection
						logger.Warn(err)
						s.setError(logger, err)
					} else {
						err = st.lastSessionError
					}
				}
				return true, err
			case <-closeChan:
				return false, nil
			}
		})

	logger.Debugf("stopped trying to re-connect to remote %s", uri)
}

func (s *remoteScheme) setError(logger *log.Entry, err error) {
	// state() relies on (err != nil) <=> (scheme == nil); never
	// store a (nil, nil) state or callers will nil-deref the
	// downstream scheme. The remote-side process can exit cleanly
	// (closeHook in scheme.go fires with err==nil) so default the
	// stored error to a typed sentinel here.
	if err == nil {
		err = ErrLostConnection
	}
	// Once Close has run, the terminal ErrRemoteClosed sentinel is
	// authoritative. A late closeHook firing after Close must not
	// overwrite it (callers rely on errors.Is(err, ErrRemoteClosed)
	// to distinguish "we shut down" from "transport flaked"). We
	// still want to broadcast on this transition because the
	// previous scheme (if any) needs to be torn down and observers
	// should wake up.
	if s.closed.Load() {
		err = ErrRemoteClosed
	}
	prevState := s.currState.Swap(state{
		lastSessionError: err,
		generation:       s.generation.Load(),
	})
	if prevState != nil && prevState.(state).scheme != nil {
		closeErr := prevState.(state).scheme.Close()
		logger.Tracef("closed previous remote scheme: %v", closeErr)
	}
	s.broadcastDisconnect()
}

// New constructs a remote scheme and starts a background
// maintain-connection goroutine. The constructor returns immediately;
// callers that need to observe the first connect attempt block via
// state() instead. This split lets the constructor be invoked from the
// IDE event loop without preventing the dial from posting interactive
// auth prompts back to that loop.
//
// retryable decides which connect failures are worth another attempt;
// see [RetryableFn].
func New(
	ctx context.Context, connect ConnectFn, uri workspaceapi.URI,
	retryable RetryableFn,
) schemeapi.Scheme {
	ret := &remoteScheme{
		uri:          uri,
		retryable:    retryable,
		logClass:     uri.Scheme(),
		closeChan:    make(chan struct{}),
		firstAttempt: make(chan struct{}),
		disconnectCh: make(chan struct{}),
	}
	ret.currState.Store(state{lastSessionError: errors.New("not connected yet")})
	ret.ctx, ret.cancelCtx = context.WithCancel(ctx)

	go debug.CapturePanicReport(func() {
		ret.maintainConnection(connect, uri, ret.closeChan)
	})
	return ret
}

// state returns the current connection state. It blocks until the first
// connect attempt has completed (success or failure) so callers don't
// observe the transient "not connected yet" sentinel that the
// constructor seeds. After the first attempt this is a non-blocking read
// of an atomic value.
//
// NOTE: this should only be called from within event loop, otherwhise
// need to sync first with locker.
func (s *remoteScheme) state() (err error, scheme schemeapi.Scheme) { //nolint:staticcheck
	scheme, _, err = s.stateWithGeneration()
	return
}

func (s *remoteScheme) stateWithGeneration() (
	scheme schemeapi.Scheme, generation uint64, err error,
) {
	select {
	case <-s.firstAttempt:
	case <-s.ctx.Done():
		// If Close cancelled the ctx, surface the dedicated
		// terminal sentinel so callers can distinguish "we shut
		// down" from a generic context.Canceled (e.g. errors.Is
		// checks in the VTE reservoir and watcher loops).
		if s.closed.Load() {
			return nil, s.generation.Load(), ErrRemoteClosed
		}
		return nil, s.generation.Load(), s.ctx.Err()
	}
	currState := s.currState.Load().(state)
	err = currState.lastSessionError
	scheme = currState.scheme
	generation = currState.generation
	return
}

func (s *remoteScheme) OpenFile(path string, flag int, perm os.FileMode) (
	workspaceapi.File, error,
) {
	scheme, generation, err := s.stateWithGeneration()
	if err != nil {
		return nil, err
	}
	f, err := scheme.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return s.track(f, generation), nil
}

// track clears the workspace client finalizer so we can manage lifecycle
// manually across clients of the remote workspace, as we potentially
// recycle through reconnections. An untracked file left to the SDK
// finalizer sends a close for a descriptor number the remote may have
// already recycled.
func (s *remoteScheme) track(
	f workspaceapi.File, generation uint64,
) workspaceapi.File {
	runtime.SetFinalizer(f, nil)
	rf := newRemoteFile(s, f.Fd(), f.Name(), generation)
	s.files.Store(rf.key(), rf)
	return rf
}

func (s *remoteScheme) lookupFile(
	generation uint64, fd uintptr, filename string,
) workspaceapi.File {
	f, _ := s.files.Load(remoteFileKey{generation: generation, fd: fd})
	if f == nil {
		return nil
	}
	rf := f.(*remoteFile)
	// Descriptor numbers are recycled within a generation, so a stale
	// request naming a closed file must not resolve to its successor.
	if rf.filename != filename {
		return nil
	}
	return rf
}

func (s *remoteScheme) Chroot(path string) (schemeapi.Scheme, error) {
	scheme, generation, err := s.stateWithGeneration()
	if err != nil {
		return nil, err
	}
	sub, err := scheme.Chroot(path)
	if err != nil {
		return nil, err
	}
	return &remoteChroot{Scheme: sub, parent: s, generation: generation}, nil
}

func (s *remoteScheme) Root() string {
	err, scheme := s.state()
	if err != nil {
		// best effort
		return s.uri.Path()
	}
	return scheme.Root()
}

func (s *remoteScheme) Symlink(target, link string) error {
	err, scheme := s.state()
	if err != nil {
		return err
	}
	return scheme.Symlink(target, link)
}

func (s *remoteScheme) TempFile(dir, prefix string) (workspaceapi.File, error) {
	scheme, generation, err := s.stateWithGeneration()
	if err != nil {
		return nil, err
	}
	f, err := scheme.TempFile(dir, prefix)
	if err != nil {
		return nil, err
	}
	return s.track(f, generation), nil
}

func (s *remoteScheme) Join(elem ...string) string {
	err, scheme := s.state()
	if err != nil {
		return filepath.Join(elem...)
	}
	return scheme.Join(elem...)
}

func (s *remoteScheme) Create(filename string) (workspaceapi.File, error) {
	scheme, generation, err := s.stateWithGeneration()
	if err != nil {
		return nil, err
	}
	f, err := scheme.Create(filename)
	if err != nil {
		return nil, err
	}
	return s.track(f, generation), nil
}

func (s *remoteScheme) Open(filename string) (workspaceapi.File, error) {
	scheme, generation, err := s.stateWithGeneration()
	if err != nil {
		return nil, err
	}
	f, err := scheme.Open(filename)
	if err != nil {
		return nil, err
	}
	return s.track(f, generation), nil
}

func (s *remoteScheme) NewFile(fd uintptr, filename string) workspaceapi.File {
	_, generation, err := s.stateWithGeneration()
	if err != nil {
		return nil
	}
	return s.lookupFile(generation, fd, filename)
}

func (s *remoteScheme) Remove(path string) error {
	err, scheme := s.state()
	if err != nil {
		return err
	}
	return scheme.Remove(path)
}

func (s *remoteScheme) Rename(oldpath, newpath string) error {
	err, scheme := s.state()
	if err != nil {
		return err
	}
	return scheme.Rename(oldpath, newpath)
}

func (s *remoteScheme) Stat(path string) (os.FileInfo, error) {
	err, scheme := s.state()
	if err != nil {
		return nil, err
	}
	return scheme.Stat(path)
}

func (s *remoteScheme) Lstat(path string) (os.FileInfo, error) {
	err, scheme := s.state()
	if err != nil {
		return nil, err
	}
	return scheme.Lstat(path)
}

func (s *remoteScheme) Readlink(path string) (string, error) {
	err, scheme := s.state()
	if err != nil {
		return "", err
	}
	return scheme.Readlink(path)
}

// URI resolves path against the remote, so "~" and relative paths
// expand on the machine that owns them rather than locally.
func (s *remoteScheme) URI(path string) (workspaceapi.URI, error) {
	err, scheme := s.state()
	if err != nil {
		return workspaceapi.URI{}, err
	}
	return scheme.URI(path)
}

func (s *remoteScheme) StartCommand(ctx context.Context, cmd workspaceapi.Cmd) (
	workspaceapi.Pid, error,
) {
	scheme, generation, err := s.stateWithGeneration()
	if err != nil {
		return 0, err
	}
	ctx, cancelFn := bluectx.First(s.ctx, ctx)
	cmd.Watcher = NewCancelWatcher(cmd.Watcher, cancelFn)
	if remoteFile, ok := cmd.Stdin.(*remoteFile); ok {
		cmd.Stdin, err = remoteFile.newFileForState(scheme, generation)
		if err != nil {
			return 0, fmt.Errorf("unwrap remote file: %v", err)
		}
	}
	if remoteFile, ok := cmd.Stdout.(*remoteFile); ok {
		cmd.Stdout, err = remoteFile.newFileForState(scheme, generation)
		if err != nil {
			return 0, fmt.Errorf("unwrap remote file: %v", err)
		}
	}
	if remoteFile, ok := cmd.Stderr.(*remoteFile); ok {
		cmd.Stderr, err = remoteFile.newFileForState(scheme, generation)
		if err != nil {
			return 0, fmt.Errorf("unwrap remote file: %v", err)
		}
	}
	return scheme.StartCommand(ctx, cmd)
}

func (s *remoteScheme) Signal(p workspaceapi.Pid, signal syscall.Signal) error {
	err, scheme := s.state()
	if err != nil {
		return err
	}
	return scheme.Signal(p, signal)
}

func (s *remoteScheme) NewPty(ctx context.Context) (workspaceapi.Pty, error) {
	scheme, generation, err := s.stateWithGeneration()
	if err != nil {
		return workspaceapi.Pty{}, err
	}
	// FIXME this temporarily leaks a goroutine, once session is closed
	// but ctx or s.ctx have not been canceled yet.
	// Since pty capability might be removed from a scheme, once sysprocattr
	// is enabled or if we decide to just remove it, it's ok to leave it
	// like this for now.
	pty, err := scheme.NewPty(ctx)
	if err == nil {
		pty.Master = s.track(pty.Master, generation)
		pty.Slave = s.track(pty.Slave, generation)
	}
	return pty, err
}

func (s *remoteScheme) ReadDir(name string) ([]os.DirEntry, error) {
	err, scheme := s.state()
	if err != nil {
		return nil, err
	}
	return scheme.ReadDir(name)
}

func (s *remoteScheme) MkdirAll(path string, perm os.FileMode) error {
	err, scheme := s.state()
	if err != nil {
		return err
	}
	return scheme.MkdirAll(path, perm)
}

func (s *remoteScheme) Watch(
	path string, c chan<- schemeapi.EventInfo, events ...schemeapi.Event,
) (int, error) {
	err, scheme := s.state()
	if err != nil {
		return 0, err
	}
	return scheme.Watch(path, c, events...)
}

func (s *remoteScheme) StopWatch(id int) error {
	err, scheme := s.state()
	if err != nil {
		return err
	}
	return scheme.StopWatch(id)
}

func (s *remoteScheme) SetPtySize(pty workspaceapi.Pty, size workspaceapi.PtySize) error {
	scheme, generation, err := s.stateWithGeneration()
	if err != nil {
		return err
	}
	if master, ok := pty.Master.(*remoteFile); ok && master.generation != generation {
		return errInvalidFd
	}
	if slave, ok := pty.Slave.(*remoteFile); ok && slave.generation != generation {
		return errInvalidFd
	}
	// unwrap for underlying scheme to avoid unexpected type assertions panics
	pty.Master = scheme.NewFile(pty.Master.Fd(), pty.Master.Name())
	pty.Slave = scheme.NewFile(pty.Slave.Fd(), pty.Slave.Name())
	// file is transient, do not Close on GC
	runtime.SetFinalizer(pty.Master, nil)
	runtime.SetFinalizer(pty.Slave, nil)
	return scheme.SetPtySize(pty, size)
}

func (s *remoteScheme) Close() (ret error) {
	// Mark closed before touching state so any concurrent
	// closeHook fired by the underlying transport observes the
	// terminal sentinel via setError and does not race the swap
	// below.
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	prevState := s.currState.Swap(state{
		lastSessionError: ErrRemoteClosed,
		generation:       s.generation.Load(),
	})
	if prevState != nil && prevState.(state).scheme != nil {
		// no need to close files before closing scheme to avoid
		// closing connection before telling the remote workspace to close
		// files: remote workspace process is going to do that anyway
		// as the process will be shutdown.
		close(s.closeChan)
		prevState.(state).scheme.Close()
	}
	// Broadcast unconditionally so observers waiting on a re-armed
	// OnDisconnect channel (e.g. a Facility watcher started after a
	// previous disconnect) wake up and exit cleanly. After this
	// point OnDisconnect returns an already-closed channel forever.
	s.broadcastDisconnect()
	s.cancelCtx()
	return
}

// OnDisconnect satisfies [workspace.RemoteScheme]: it returns the
// same stable channel for the entire lifetime of the remote scheme.
// The channel is closed exactly once when the underlying transport
// drops (or Close is called), so:
//   - subscribing before a drop blocks on the channel until the
//     drop fires;
//   - subscribing after a drop returns an already-closed channel
//     and the receive returns immediately;
//   - the channel is never re-opened. The semantic is
//     "invalidate everything once" — callers that want to track
//     subsequent drops should call OnDisconnect on a freshly
//     resolved remote scheme.
//
// The signal must be interpreted as "every file descriptor handed
// out by this scheme up to now is invalid"; callers should drop
// cached handles and reload against a freshly-resolved transport.
func (s *remoteScheme) OnDisconnect() <-chan struct{} {
	return s.disconnectCh
}

// WaitConnected satisfies [workspace.RemoteScheme]: it blocks until
// the first connection attempt has settled (mirroring the gate in
// state), the scheme is closed, or ctx is done.
func (s *remoteScheme) WaitConnected(ctx context.Context) error {
	select {
	case <-s.firstAttempt:
		return nil
	case <-s.ctx.Done():
		if s.closed.Load() {
			return ErrRemoteClosed
		}
		return s.ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// broadcastDisconnect closes the disconnect channel exactly once,
// waking every observer currently blocked on OnDisconnect. Safe to
// call repeatedly (e.g. by closeHook firing on each transport
// flap and by Close).
func (s *remoteScheme) broadcastDisconnect() {
	s.disconnectOnce.Do(func() {
		close(s.disconnectCh)
	})
}

// wrap watcher to ensure that one of bluectx.First ctxs gets canceled
type wrapWatcher struct {
	watcher workspaceapi.ProcessWatcher
	ch      chan error
}

// NewCancelWatcher wraps watcher so cancelFn runs once the watched
// process reports its exit status, releasing the per-command context
// derived with [bluectx.First].
func NewCancelWatcher(
	watcher workspaceapi.ProcessWatcher, cancelFn func(),
) workspaceapi.ProcessWatcher {
	ret := wrapWatcher{
		watcher: watcher,
		// Buffered so the producer (fileScheme.StartCommand) is not
		// forced to block on the unbuffered handoff while we
		// forward to the underlying watcher. Avoids leaking this
		// goroutine if the producer eventually decides to bail
		// out via ctx.Done before sending — see comment at the
		// receive below.
		ch: make(chan error, 1),
	}
	go debug.CapturePanicReport(func() {
		// Wait for the StartCommand producer to send a final err.
		// If no err is ever sent (e.g. fileScheme.StartCommand bailed
		// out via watcherWaitTimeout), this goroutine would leak;
		// the producer is expected to always send so we just block.
		err := <-ret.ch
		cancelFn()
		if ret.watcher != nil && ret.watcher.WatchProcess() != nil {
			t := time.After(2 * time.Minute) // in case watcher is unresponsive
			select {
			case ret.watcher.WatchProcess() <- err:
			case <-t:
			}
		}
	})
	return ret
}

func (w wrapWatcher) WatchProcess() chan error {
	return w.ch
}
