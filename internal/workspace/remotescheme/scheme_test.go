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

package remotescheme

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/retry"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"go.uber.org/mock/gomock"
	"unstable.build/rune/internal/workspace"
	"unstable.build/rune/internal/workspace/schemetest"
	"unstable.build/rune/internal/workspace/workspaceapitest"
)

var _ workspace.RemoteScheme = (*remoteScheme)(nil)

func alwaysRetry(error) bool { return true }

func TestRemoteScheme(t *testing.T) {
	uri, err := workspaceapi.ParseURI("ssh://unsable.build/home/ernie")
	require.NoError(t, err)

	t.Run("establish initial connection", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		var mu sync.Mutex
		ctx := context.Background()

		mock := schemetest.NewMockScheme(ctrl)
		mu.Lock()
		scheme := New(ctx,
			func(_ context.Context, uri workspaceapi.URI, closehook func(error)) (schemeapi.Scheme, error) {
				return mock, nil
			}, uri, alwaysRetry)
		mu.Unlock()

		expectSchemeAPISuccess(t, ctrl, &mu, mock, scheme)
		expectSchemeClose(t, mock, scheme)
	})

	t.Run("eventually succeed upon initial connect errors", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		var mu sync.Mutex
		ctx := context.Background()

		mock := schemetest.NewMockScheme(ctrl)
		var i int

		mu.Lock()
		scheme := New(ctx, func(
			_ context.Context, uri workspaceapi.URI, closehook func(error),
		) (schemeapi.Scheme, error) {
			i++
			if i <= 5 {
				return nil, errors.New("unable to connect")
			}
			return mock, nil
		}, uri, alwaysRetry)
		mu.Unlock()

		mock.EXPECT().StartCommand(gomock.Any(), gomock.Any()).Return(workspaceapi.Pid(0), nil).Times(1)
		retry.Retry(context.Background(), retry.ExponentialStrategy(1*time.Millisecond, 10*time.Millisecond), func(context.Context) (bool, error) {
			mu.Lock()
			defer mu.Unlock()

			_, err = scheme.StartCommand(context.Background(),
				workspaceapi.Cmd{Path: "blah"})
			return true, err
		})
		require.NoError(t, err)
		expectSchemeClose(t, mock, scheme)
	})

	t.Run("re-establish connection", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		var mu sync.Mutex
		ctx := context.Background()

		mock := schemetest.NewMockScheme(ctrl)
		var closeHook func(error)
		var wg sync.WaitGroup
		var reconnect bool
		mu.Lock()
		scheme := New(ctx, func(
			_ context.Context, uri workspaceapi.URI, _closehook func(error),
		) (schemeapi.Scheme, error) {
			closeHook = _closehook
			if reconnect {
				wg.Done()
			}
			return mock, nil
		}, uri, alwaysRetry)
		mu.Unlock()
		expectSchemeAPISuccess(t, ctrl, &mu, mock, scheme)

		wg.Add(1)
		reconnect = true
		mock.EXPECT().Close().Return(errors.New("already closed but should be fine"))
		closeHook(errors.New("kaboom"))
		wg.Wait()
		expectSchemeAPISuccess(t, ctrl, &mu, mock, scheme)

		expectSchemeClose(t, mock, scheme)
	})

	t.Run("WaitConnected blocks until the first attempt settles", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		mock := schemetest.NewMockScheme(ctrl)
		release := make(chan struct{})
		scheme := New(context.Background(), func(
			_ context.Context, _ workspaceapi.URI, _ func(error),
		) (schemeapi.Scheme, error) {
			// Simulate unbounded first-connect provisioning.
			<-release
			return mock, nil
		}, uri, alwaysRetry)
		rs := scheme.(*remoteScheme)

		// A caller-scoped context must be able to abandon the wait
		// while the first attempt is still in flight.
		ctx, cancel := context.WithTimeout(
			context.Background(), 10*time.Millisecond)
		defer cancel()
		require.ErrorIs(t, rs.WaitConnected(ctx),
			context.DeadlineExceeded)

		done := make(chan error, 1)
		go func() {
			done <- rs.WaitConnected(context.Background())
		}()
		close(release)
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("WaitConnected did not return after the first " +
				"attempt settled")
		}
		expectSchemeClose(t, mock, scheme)
	})

	t.Run("NewFile on un-opened file returns nil", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		var mu sync.Mutex
		ctx := context.Background()

		mock := schemetest.NewMockScheme(ctrl)
		mu.Lock()
		scheme := New(ctx,
			func(_ context.Context, uri workspaceapi.URI, closehook func(error)) (schemeapi.Scheme, error) {
				return mock, nil
			}, uri, alwaysRetry)
		mu.Unlock()

		expectSchemeAPISuccess(t, ctrl, &mu, mock, scheme)
		expectSchemeClose(t, mock, scheme)
		f := scheme.NewFile(1299, "blabla")
		require.Nil(t, f)
	})

	t.Run("stale close preserves reused descriptor", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		first := schemetest.NewMockScheme(ctrl)
		second := schemetest.NewMockScheme(ctrl)
		connections := make(chan schemeapi.Scheme, 2)
		connections <- first
		connections <- second
		closeHooks := make(chan func(error), 2)
		scheme := New(context.Background(), func(
			_ context.Context, _ workspaceapi.URI, closeHook func(error),
		) (schemeapi.Scheme, error) {
			closeHooks <- closeHook
			return <-connections, nil
		}, uri, alwaysRetry)
		t.Cleanup(func() {
			second.EXPECT().Close().Return(nil).AnyTimes()
			require.NoError(t, scheme.Close())
		})

		oldRPCFile := workspaceapitest.NewMockFile(ctrl)
		oldRPCFile.EXPECT().Fd().Return(uintptr(42))
		oldRPCFile.EXPECT().Name().Return("old.txt")
		first.EXPECT().OpenFile("old.txt", os.O_RDONLY, os.FileMode(0)).
			Return(oldRPCFile, nil)
		oldFile, err := scheme.OpenFile("old.txt", os.O_RDONLY, 0)
		require.NoError(t, err)

		first.EXPECT().Close().Return(nil)
		firstCloseHook := <-closeHooks
		firstCloseHook(errors.New("connection reset"))
		<-closeHooks
		require.Eventually(t, func() bool {
			return scheme.(*remoteScheme).currState.Load().(state).scheme == second
		}, time.Second, time.Millisecond)

		newRPCFile := workspaceapitest.NewMockFile(ctrl)
		newRPCFile.EXPECT().Fd().Return(uintptr(42))
		newRPCFile.EXPECT().Name().Return("new.txt")
		second.EXPECT().OpenFile("new.txt", os.O_RDONLY, os.FileMode(0)).
			Return(newRPCFile, nil)
		newFile, err := scheme.OpenFile("new.txt", os.O_RDONLY, 0)
		require.NoError(t, err)

		require.NoError(t, oldFile.Close())
		require.Same(t, newFile, scheme.NewFile(42, "new.txt"))

		transient := workspaceapitest.NewMockFile(ctrl)
		second.EXPECT().NewFile(uintptr(42), "new.txt").Return(transient)
		transient.EXPECT().Read(gomock.Any()).Return(1, nil)
		_, err = newFile.Read(make([]byte, 1))
		require.NoError(t, err)
	})

	// Reproducer: closeHook(nil) — which fires when the remote
	// process exits cleanly or s.ctx is cancelled before any
	// transport error — used to store (scheme=nil, err=nil) into
	// currState. state() then returned (nil, nil) and the next
	// scheme call (e.g. StopWatch from the FS-watcher defer in
	// ide.installPendingWorkspace) nil-derefed the embedded
	// schemeapi.Scheme.
	t.Run("closeHook(nil) does not poison currState", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()
		mock := schemetest.NewMockScheme(ctrl)
		hookCh := make(chan func(error), 1)
		scheme := New(ctx, func(
			_ context.Context, _ workspaceapi.URI, hook func(error),
		) (schemeapi.Scheme, error) {
			select {
			case hookCh <- hook:
			default:
			}
			return mock, nil
		}, uri, alwaysRetry)
		t.Cleanup(func() {
			mock.EXPECT().Close().Return(nil).AnyTimes()
			_ = scheme.Close()
		})

		mock.EXPECT().Close().Return(nil).AnyTimes()
		closeHook := <-hookCh
		// The hook is handed out before connect returns; firing it
		// earlier would be overwritten by the connection landing.
		require.NoError(t, scheme.(*remoteScheme).WaitConnected(ctx))

		// Fire the close hook with a nil error (mirrors a clean
		// remote process exit). State must still surface a typed
		// disconnect error so callers short-circuit instead of
		// dereferencing a nil scheme.
		closeHook(nil)

		// StopWatch is the call site that crashed in production
		// (see ide.installPendingWorkspace FS-watcher defer). Any
		// other scheme method exercises the same state() path.
		err := scheme.StopWatch(42)
		require.Error(t, err,
			"after a closeHook(nil) disconnect, scheme methods must "+
				"return a typed error rather than nil-deref the "+
				"underlying scheme")
		require.ErrorIs(t, err, ErrLostConnection)
	})

	t.Run("OnDisconnect is a stable channel closed once on transport drop", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		var mu sync.Mutex
		ctx := context.Background()

		mock := schemetest.NewMockScheme(ctrl)
		hookCh := make(chan func(error), 1)
		mu.Lock()
		scheme := New(ctx, func(
			_ context.Context, uri workspaceapi.URI, hook func(error),
		) (schemeapi.Scheme, error) {
			select {
			case hookCh <- hook:
			default:
			}
			return mock, nil
		}, uri, alwaysRetry)
		mu.Unlock()

		rs, ok := scheme.(*remoteScheme)
		require.True(t, ok)

		// OnDisconnect must hand out the same stable channel
		// across calls so observers can subscribe before any
		// transport drop without coordinating with each other.
		ch := rs.OnDisconnect()
		require.NotNil(t, ch)
		require.Equal(t, ch, rs.OnDisconnect(),
			"OnDisconnect must return a stable channel: "+
				"creating a fresh one per call would force "+
				"callers to coordinate or miss signals")

		// The channel closes on the first transport drop.
		// Semantic: every FD this scheme handed out so far is
		// invalid; observers drop their caches.
		mock.EXPECT().Close().Return(nil).AnyTimes()
		closeHook := <-hookCh
		closeHook(errors.New("kaboom"))

		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("OnDisconnect channel must close on transport drop")
		}

		// After the drop the same channel is still returned so
		// late subscribers wake up immediately, and the runtime
		// does not need to track per-observer state.
		require.Equal(t, ch, rs.OnDisconnect(),
			"OnDisconnect must keep returning the same "+
				"(now-closed) channel after a drop")

		// A second close hook firing on the same remote scheme
		// (e.g. SSH transport flapping) must not panic via
		// double-close of the disconnect channel.
		closeHook(errors.New("kaboom again"))

		require.NoError(t, scheme.Close())
	})

	t.Run("Close fires OnDisconnect for waiting observers", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		ctx := context.Background()
		mock := schemetest.NewMockScheme(ctrl)
		scheme := New(ctx, func(
			_ context.Context, _ workspaceapi.URI, _ func(error),
		) (schemeapi.Scheme, error) {
			return mock, nil
		}, uri, alwaysRetry)
		mock.EXPECT().Close().Return(nil).AnyTimes()

		rs, ok := scheme.(*remoteScheme)
		require.True(t, ok)
		ch := rs.OnDisconnect()

		// Closing the scheme must wake observers that were
		// blocked on OnDisconnect; otherwise watcher goroutines
		// would leak past shutdown.
		require.NoError(t, scheme.Close())
		select {
		case <-ch:
		case <-time.After(2 * time.Second):
			t.Fatal("OnDisconnect channel must close on Close")
		}
	})
}

func expectSchemeAPISuccess(
	t *testing.T, ctrl *gomock.Controller, mu *sync.Mutex,
	mock *schemetest.MockScheme, scheme schemeapi.Scheme,
) {
	mu.Lock()
	defer mu.Unlock()

	mock.EXPECT().StartCommand(gomock.Any(), gomock.Any()).Return(workspaceapi.Pid(0), nil).Times(1)
	_, err := scheme.StartCommand(context.Background(), workspaceapi.Cmd{})
	require.NoError(t, err)

	mock.EXPECT().Signal(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	err = scheme.Signal(0, 0)
	require.NoError(t, err)

	f := workspaceapitest.NewMockFile(ctrl)
	f.EXPECT().Fd().AnyTimes()
	f.EXPECT().Name().AnyTimes()
	mock.EXPECT().OpenFile(gomock.Any(), gomock.Any(), gomock.Any()).Return(f, nil).Times(1)
	_, osErr := scheme.OpenFile("", 0, 0)
	require.Nil(t, osErr)

	mock.EXPECT().Remove(gomock.Any()).Return(nil).Times(1)
	err = scheme.Remove("")
	require.NoError(t, err)

	mock.EXPECT().Rename(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	err = scheme.Rename("", "")
	require.NoError(t, err)

	mock.EXPECT().Stat(gomock.Any()).Return(nil, nil).Times(1)
	_, err = scheme.Stat("")
	require.NoError(t, err)

	mock.EXPECT().Lstat(gomock.Any()).Return(nil, nil).Times(1)
	_, err = scheme.Lstat("")
	require.NoError(t, err)

	mock.EXPECT().Readlink(gomock.Any()).Return("", nil).Times(1)
	_, err = scheme.Readlink("")
	require.NoError(t, err)

	mock.EXPECT().StartCommand(gomock.Any(), gomock.Any()).
		Return(workspaceapi.Pid(0), nil).Times(1)
	_, err = scheme.StartCommand(context.Background(),
		workspaceapi.Cmd{Path: "blah"})
	require.NoError(t, err)

	mock.EXPECT().ReadDir(gomock.Any()).Return([]os.DirEntry{}, nil).Times(1)
	_, err = scheme.ReadDir("")
	require.NoError(t, err)
}

func expectSchemeClose(t *testing.T, mock *schemetest.MockScheme, scheme schemeapi.Scheme) {
	mock.EXPECT().Close().Return(nil)
	err := scheme.Close()
	require.NoError(t, err)
}
