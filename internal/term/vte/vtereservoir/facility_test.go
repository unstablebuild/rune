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

package vtereservoir

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser/browsertest"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vsh/vshtest"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vteregistry"
	"unstable.build/rune/internal/workspace/workspacetest"
)

func TestFacility(t *testing.T) {
	t.Parallel()
	t.Run("pre-allocates initial capacity", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int64
		f := newTestFacility(5, func(f *Facility) (vte.View, error) {
			called.Add(1)
			return newTestVte(), nil
		})
		assert.Equal(t, 5, int(called.Load()))

		_, err := f.Get()
		require.NoError(t, err)

		assert.Equal(t, 5, int(called.Load()))
	})

	t.Run("pre-allocates if pool is empty", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int64
		f := newTestFacility(1, func(f *Facility) (vte.View, error) {
			called.Add(1)
			return newTestVte(), nil
		})
		assert.Equal(t, 1, int(called.Load()))

		_, err := f.Get()
		require.NoError(t, err)

		assert.Equal(t, 2, int(called.Load()))
	})

	t.Run("refill failure does not cost the caller the warm VTE", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int64
		f := newTestFacility(2, func(f *Facility) (vte.View, error) {
			// Warm-up (calls 1-2) succeeds; the opportunistic
			// refill triggered by draining the pool (call 3) fails.
			if called.Add(1) > 2 {
				return nil, errors.New("stalled transport")
			}
			return newTestVte(), nil
		})

		_, err := f.Get()
		require.NoError(t, err)

		// Drains the pool: the refill fails, but the caller must
		// still receive the warm VTE it already had.
		v, err := f.Get()
		require.NoError(t, err)
		require.NotNil(t, v)
		assert.Equal(t, 3, int(called.Load()))
	})

	t.Run("Get discards dead pooled vtes", func(t *testing.T) {
		t.Parallel()
		// Regression: a remote transport drop kills the pty run
		// loop of every pooled VTE (Component.IsComplete becomes
		// true) but, past the first one-shot OnDisconnect signal,
		// nothing drains the pool. Get used to hand these dead
		// VTEs to new panes, which then rendered an empty terminal
		// that never opened.
		var mu sync.Mutex
		var created []*recordingVTE
		f := newTestFacility(2, func(f *Facility) (vte.View, error) {
			v := newRecordingVTE()
			mu.Lock()
			defer mu.Unlock()
			created = append(created, v)
			return v, nil
		})

		mu.Lock()
		warm := append([]*recordingVTE(nil), created...)
		mu.Unlock()
		require.Len(t, warm, 2)
		for _, v := range warm {
			v.complete.Store(true)
		}

		got, err := f.Get()
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.False(t, got.IsComplete(),
			"Get must not hand out a VTE whose pty run loop exited")
		for _, v := range warm {
			assert.True(t, v.closed.Load(),
				"dead pooled VTEs must be disposed, not leaked")
		}
	})

	t.Run("facility.Close closes all free vtes", func(t *testing.T) {
		t.Parallel()
		var mu sync.Mutex
		var created []*testVte
		f := newTestFacility(5, func(f *Facility) (vte.View, error) {
			tvte := newTestVte()
			mu.Lock()
			defer mu.Unlock()
			created = append(created, tvte)
			return tvte, nil
		})

		// free and returned
		vte1, err := f.Get()
		require.NoError(t, err)

		vte2, err := f.Get()
		require.NoError(t, err)

		f.Release(vte1)
		require.NoError(t, f.Close())
		f.Release(vte2)

		for _, vte := range created {
			assert.True(t, vte.calledClose)
		}
	})

	t.Run("Release puts vte back into the pool", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int32
		f := newTestFacility(2, func(f *Facility) (vte.View, error) {
			tvte := newTestVte()
			called.Add(1)
			return tvte, nil
		})

		vte, err := f.Get()
		require.NoError(t, err)

		f.Release(vte)

		_, err = f.Get()
		require.NoError(t, err)

		assert.Equal(t, 2, int(called.Load()))
	})

	t.Run("put rejects VTE above initial capacity", func(t *testing.T) {
		t.Parallel()
		f := newTestFacility(1, func(f *Facility) (vte.View, error) {
			return newTestVte(), nil
		})

		borrowed, err := f.Get()
		require.NoError(t, err)
		require.Equal(t, 1, f.Capacity())

		assert.False(t, f.put(borrowed))
		require.Equal(t, 1, f.Capacity())
	})

	t.Run("Release deletes a vte that used the alternate buffer", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int32
		f := newTestFacility(2, func(f *Facility) (vte.View, error) {
			tvte := newTestVte()
			tvte.usedAlt = true
			called.Add(1)
			return tvte, nil
		})

		vte, err := f.Get()
		require.NoError(t, err)

		f.Release(vte)
		assert.True(t, vte.(*testVte).calledClose)

		_, err = f.Get()
		require.NoError(t, err)

		assert.Equal(t, 3, int(called.Load()))
	})

	t.Run("Release clears the primary buffer when put back into pool", func(t *testing.T) {
		t.Parallel()
		f := newTestFacility(2, func(f *Facility) (vte.View, error) {
			return newTestVte(), nil
		})

		pooled, err := f.Get()
		require.NoError(t, err)
		tvte := pooled.(*testVte)

		f.Release(pooled)

		assert.True(t, tvte.clearedPrimary)
		assert.False(t, tvte.calledClose, "a pooled vte stays attached")
		assert.True(t, f.Pooled(tvte.ID()))
	})

	t.Run("Resize after new doesn't block waiting for all vtes to be created", func(t *testing.T) {
		t.Parallel()
		var wg sync.WaitGroup
		const capacity = 5
		wg.Add(capacity)
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)
		scheme, err := workspacetest.NewNopScheme("file:///tmp")(
			context.Background(), config.NopConfig(), uri)
		scheme.(*workspacetest.NopScheme).NewPtyFunc = func(ctx context.Context) (workspaceapi.Pty, error) {
			defer wg.Done()
			return workspaceapi.Pty{
				Master: scheme.NewFile(0, ""),
				Slave:  scheme.NewFile(1, ""),
			}, nil
		}
		scheme.(*workspacetest.NopScheme).StartCommandFunc = func(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
			time.Sleep(100 * time.Minute)
			return 0, nil
		}
		require.NoError(t, err)
		f := New(newTestRegistry(t, scheme, vte.DefaultConfig()), scheme, vte.DefaultConfig(), capacity)
		// this would block for 100 minutes if not implemented correctly
		f.Resize(10, 10)
		wg.Wait()
	})

	t.Run("put does not resurrect the pool after Close", func(t *testing.T) {
		t.Parallel()
		f := newTestFacility(1, func(f *Facility) (vte.View, error) {
			return newTestVte(), nil
		})

		require.NoError(t, f.Close())

		// Simulate an async Release callback that fires after the
		// facility has been closed. It must NOT re-populate
		// the pool (which would leak the underlying VTE and its
		// associated pty + scrollback memory).
		stray := newTestVte()
		assert.False(t, f.put(stray))
		assert.Equal(t, 0, f.Capacity())
	})

	t.Run("Reset drains the warm pool and lets Get re-allocate", func(t *testing.T) {
		t.Parallel()
		// Track every VTE the factory builds so we can assert that
		// the ones warm at Reset time were closed.
		var mu sync.Mutex
		var all []*testVte
		f := newTestFacility(2, func(f *Facility) (vte.View, error) {
			v := newTestVte()
			mu.Lock()
			all = append(all, v)
			mu.Unlock()
			return v, nil
		})
		require.Equal(t, 2, f.Capacity())

		mu.Lock()
		preReset := append([]*testVte(nil), all...)
		mu.Unlock()

		require.NoError(t, f.Reset())

		assert.Equal(t, 0, f.Capacity(),
			"Reset must empty the pool so the next Get re-allocates")
		for _, v := range preReset {
			assert.True(t, v.calledClose,
				"Reset must Close every warm VTE so it is not "+
					"handed out bound to the dead transport")
		}

		v, err := f.Get()
		require.NoError(t, err)
		require.NotNil(t, v)
	})

	t.Run("Reset is a no-op after Close", func(t *testing.T) {
		t.Parallel()
		f := newTestFacility(1, func(f *Facility) (vte.View, error) {
			return newTestVte(), nil
		})
		require.NoError(t, f.Close())
		require.NoError(t, f.Reset())
	})

	t.Run("remote scheme OnDisconnect drains the pool and exits", func(t *testing.T) {
		t.Parallel()
		// Drive watchRemoteScheme directly with an in-memory fake
		// to keep the test independent of the workspacessh package.
		// Use recordingVTE rather than testVte so Close does not
		// peek at f.pool without locking (which would race against
		// the concurrent Reset below).
		f := newTestFacility(1, func(f *Facility) (vte.View, error) {
			return newRecordingVTE(), nil
		})
		f.ctx, f.cancelCtx = context.WithCancel(context.Background())
		require.Equal(t, 1, f.Capacity())

		rs := newFakeRemoteScheme()
		watcherDone := make(chan struct{})
		go func() {
			defer close(watcherDone)
			f.watchRemoteScheme(rs.OnDisconnect())
		}()

		rs.broadcastDisconnect()

		// One-shot contract: after the single disconnect signal,
		// the watcher resets the pool and exits. Re-arming would
		// busy-loop on a permanently-closed channel.
		select {
		case <-watcherDone:
		case <-time.After(2 * time.Second):
			t.Fatal("watchRemoteScheme must exit after the " +
				"one-shot disconnect signal fires")
		}
		require.Equal(t, 0, f.Capacity(),
			"watchRemoteScheme must Reset the pool when "+
				"OnDisconnect fires")

		require.NoError(t, f.Close())
	})

	t.Run("watchRemoteScheme exits when facility ctx is cancelled", func(t *testing.T) {
		t.Parallel()
		f := newTestFacility(1, func(f *Facility) (vte.View, error) {
			return newRecordingVTE(), nil
		})
		f.ctx, f.cancelCtx = context.WithCancel(context.Background())

		rs := newFakeRemoteScheme()
		done := make(chan struct{})
		go func() {
			defer close(done)
			f.watchRemoteScheme(rs.OnDisconnect())
		}()

		// No disconnect: the watcher must still exit when the
		// facility shuts down, otherwise it leaks past Close.
		f.cancelCtx()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("watchRemoteScheme must exit when the facility " +
				"context is cancelled")
		}
		require.NoError(t, f.Close())
	})

	t.Run("Get is served from the pool when warm", func(t *testing.T) {
		t.Parallel()
		var called atomic.Int64
		f := newTestFacility(2, func(f *Facility) (vte.View, error) {
			called.Add(1)
			return newTestVte(), nil
		})
		// initCap synchronously fires off two news.
		assert.Equal(t, int64(2), called.Load())

		// First Get: pool has 2, take one, pool now has 1 (no
		// refill because len(pool) > 0 after pop).
		_, err := f.Get()
		require.NoError(t, err)
		assert.Equal(t, int64(2), called.Load())

		// Second Get: pool has 1, take it, pool empty so refill -> 1 new call.
		_, err = f.Get()
		require.NoError(t, err)
		assert.Equal(t, int64(3), called.Load())
	})

	t.Run("Get blocks until initCap delivers a pre-warmed VTE", func(t *testing.T) {
		t.Parallel()
		// Use New(...) so that initCap runs asynchronously - this is
		// the exact race that defeats the warm pool in production.
		release := make(chan struct{})
		var newCalls atomic.Int64
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)
		scheme, err := workspacetest.NewNopScheme("file:///tmp")(
			context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		nop := scheme.(*workspacetest.NopScheme)
		nop.NewPtyFunc = func(ctx context.Context) (workspaceapi.Pty, error) {
			// Block initCap until the test releases it.
			<-release
			newCalls.Add(1)
			return workspaceapi.Pty{
				Master: scheme.NewFile(0, ""),
				Slave:  scheme.NewFile(1, ""),
			}, nil
		}
		nop.StartCommandFunc = func(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
			return 0, nil
		}
		f := New(newTestRegistry(t, scheme, vte.DefaultConfig()), scheme, vte.DefaultConfig(), 2)
		t.Cleanup(func() { _ = f.Close() })

		// Issue a Get before initCap can deliver any VTE. It must not
		// fall through to the from-scratch path; it must wait for the
		// pool to be populated.
		got := make(chan vte.View, 1)
		errCh := make(chan error, 1)
		go func() {
			v, err := f.Get()
			if err != nil {
				errCh <- err
				return
			}
			got <- v
		}()

		// Give the goroutine a chance to enter Get and observe the
		// empty pool. With the bug, Get would immediately call
		// f.new(false) and return.
		select {
		case <-got:
			t.Fatal("Get returned before initCap delivered a VTE")
		case err := <-errCh:
			t.Fatalf("Get returned an unexpected error: %v", err)
		case <-time.After(50 * time.Millisecond):
		}

		// Release initCap so it can populate the pool.
		close(release)

		select {
		case <-got:
		case err := <-errCh:
			t.Fatalf("Get returned an unexpected error: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("Get blocked indefinitely waiting for initCap")
		}
	})

	t.Run("initCap does not resurrect a closed pool", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		f := new(Facility)
		f.cond = sync.NewCond(&f.mu)
		f.initialCapacity = 3
		f.pendingInit = 3
		f.terminals = &fakeTerminals{}
		f.new = func() (vte.View, error) {
			<-release
			return newTestVte(), nil
		}

		done := make(chan struct{})
		go func() {
			defer close(done)
			f.initCap(3)
		}()

		// Close while initCap is mid-flight.
		require.NoError(t, f.Close())
		close(release)
		<-done

		// The pool must remain empty - initCap must not re-populate
		// it after Close.
		assert.Equal(t, 0, f.Capacity())
	})

	// Pins the data race where releasing a terminal read f.pool
	// without the facility lock to decide between re-pooling and
	// deleting, while the initCap warm-up goroutines appended to the
	// pool under the lock; the race detector flags it.
	t.Run("Release races the warm-up appends safely", func(t *testing.T) {
		t.Parallel()
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)
		scheme, err := workspacetest.NewNopScheme("file:///tmp")(
			context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		nop := scheme.(*workspacetest.NopScheme)
		nop.NewPtyFunc = func(ctx context.Context) (workspaceapi.Pty, error) {
			return workspaceapi.Pty{
				Master: scheme.NewFile(0, ""),
				Slave:  scheme.NewFile(1, ""),
			}, nil
		}
		nop.StartCommandFunc = func(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
			return 0, nil
		}
		f := New(newTestRegistry(t, scheme, vte.DefaultConfig()), scheme, vte.DefaultConfig(), 8)
		t.Cleanup(func() { _ = f.Close() })

		// Get and release terminals while the warm-up is still
		// appending; each Release decides whether the facility can
		// still re-pool it, which must be synchronized with those
		// appends.
		for range 16 {
			v, err := f.Get()
			require.NoError(t, err)
			f.Release(v)
		}
	})

	// Pins the regression where a stalled remote workspace transport
	// blocked the warm-up's NewPty forever: pendingInit never drained,
	// so Get (called on the host event loop during session restore)
	// waited on the cond indefinitely and froze the UI. With the
	// SpawnTimeout plumbed through New's factory, both the warm-up
	// and Get's fallback allocation fail after the bound.
	t.Run("stalled spawn does not wedge Get indefinitely", func(t *testing.T) {
		t.Parallel()
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)
		scheme, err := workspacetest.NewNopScheme("file:///tmp")(
			context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		nop := scheme.(*workspacetest.NopScheme)
		nop.NewPtyFunc = func(ctx context.Context) (workspaceapi.Pty, error) {
			// Simulate a wedged transport: the RPC never returns
			// until its context is cancelled.
			<-ctx.Done()
			return workspaceapi.Pty{}, ctx.Err()
		}
		cfg := vte.DefaultConfig()
		cfg.SpawnTimeout = 20 * time.Millisecond
		f := New(newTestRegistry(t, scheme, vte.DefaultConfig()), scheme, cfg, 2)
		t.Cleanup(func() { _ = f.Close() })

		errCh := make(chan error, 1)
		go func() {
			_, err := f.Get()
			errCh <- err
		}()

		select {
		case err := <-errCh:
			require.Error(t, err,
				"Get must surface the spawn failure, not hand out a VTE")
		case <-time.After(10 * time.Second):
			t.Fatal("Get wedged behind a stalled spawn; SpawnTimeout " +
				"must bound the warm-up and the fallback allocation")
		}
	})

	// First-connect provisioning is unbounded (it may install
	// packages on the remote host) and remote scheme calls block
	// until the first connection attempt settles. The spawn budget
	// must not race that wait: a spurious warm-up failure would
	// fail session restore on freshly provisioned workspaces right
	// as they become usable.
	t.Run("spawn budget starts only after the remote transport resolves", func(t *testing.T) {
		t.Parallel()
		uri, err := workspaceapi.ParseURI("file:///tmp")
		require.NoError(t, err)
		scheme, err := workspacetest.NewNopScheme("file:///tmp")(
			context.Background(), config.NopConfig(), uri)
		require.NoError(t, err)
		nop := scheme.(*workspacetest.NopScheme)
		var ptyCalls atomic.Int64
		nop.NewPtyFunc = func(ctx context.Context) (workspaceapi.Pty, error) {
			ptyCalls.Add(1)
			return workspaceapi.Pty{
				Master: scheme.NewFile(0, ""),
				Slave:  scheme.NewFile(1, ""),
			}, nil
		}
		nop.StartCommandFunc = func(context.Context, workspaceapi.Cmd) (workspaceapi.Pid, error) {
			return 0, nil
		}
		gated := &connectGatedTerminal{
			Terminal:     nop,
			connected:    make(chan struct{}),
			disconnectCh: make(chan struct{}),
		}
		cfg := vte.DefaultConfig()
		cfg.SpawnTimeout = 20 * time.Millisecond
		f := New(newTestRegistry(t, gated, vte.DefaultConfig()), gated, cfg, 1)
		t.Cleanup(func() { _ = f.Close() })

		// Let several spawn-timeout windows elapse while the
		// transport is still "provisioning".
		time.Sleep(5 * cfg.SpawnTimeout)
		assert.EqualValues(t, 0, ptyCalls.Load(),
			"no spawn RPC may be issued (and no budget consumed) "+
				"while the transport is unresolved")

		close(gated.connected)
		f.WaitForInitialFill()

		v, err := f.Get()
		require.NoError(t, err,
			"Get must succeed once the transport resolves; the "+
				"connect wait must not have failed the warm-up")
		require.NotNil(t, v)
	})
}

type nopBrowser struct {
}

func (n nopBrowser) PublishEvent(term.Event) error {
	return nil
}
func (n nopBrowser) Notify(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return "", nil
}
func (n nopBrowser) NotifyOnce(level browserapi.NotificationLevel, msg string, args ...any) (string, error) {
	return "", nil
}
func (n nopBrowser) UpdateNotificationProgress(id, message string, progress, total int64) error {
	return nil
}
func (nopBrowser) Tab(uri workspaceapi.URI, icon rune, name string, h browserapi.Handler) (
	browserapi.Handler, error,
) {
	return browsertest.NewTestHandler(), nil
}

func (n nopBrowser) SetTabName(workspaceapi.URI, string, term.Attributes) error {
	return nil
}

func (nopBrowser) OnTabExit(workspaceapi.URI) bool {
	return false
}

func (nopBrowser) SetTabStatus(workspaceapi.URI, programstatus.Report) error { return nil }
func (nopBrowser) EndTabStatus(workspaceapi.URI) error                       { return nil }

func newTestFacility(
	initCap int, newFn func(*Facility) (vte.View, error),
) *Facility {
	// Build the facility via the same internal entrypoint
	// production [New] uses, injecting the test factory so
	// every test exercises the real lifecycle wiring (cond,
	// ctx/cancelCtx, pendingInit, remote-scheme watcher) — no
	// hand-rolled construction or private-field poking.
	f := newWithFactory(&fakeTerminals{}, newFn, nopTerminal{}, initCap)
	// initCap runs asynchronously in production; wait for it to
	// settle so callers observing Capacity / called counters
	// immediately after construction see a fully resolved pool,
	// mirroring the synchronous semantics the previous helper
	// provided.
	f.WaitForInitialFill()
	return f
}

// fakeTerminals deletes nothing: the test VTEs are owned by nobody
// else, so disposing one only closes it.
type fakeTerminals struct{}

func (*fakeTerminals) CreateVTE(
	context.Context, vte.CreateOptions,
) (vte.View, error) {
	return nil, errors.ErrUnsupported
}

func (*fakeTerminals) DeleteVTE(context.Context, vte.ID) error { return nil }

func newTestRegistry(t *testing.T, terminal schemeapi.Terminal, cfg vte.Config) *vteregistry.Registry {
	t.Helper()
	exe, _ := terminal.(schemeapi.Executor)
	if exe == nil {
		exe = terminal.(*connectGatedTerminal).Terminal.(schemeapi.Executor)
	}
	r := vteregistry.New(nopBrowser{}, nopBrowser{}, terminal, exe, nopBrowser{},
		vteregistry.Config{VTE: cfg})
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// nopTerminal satisfies the schemeapi.Terminal slot required by
// [newWithFactory]. The test factory replaces vte.NewHandler so
// these methods are never actually called; the only reason they
// exist is to type-check.
type nopTerminal struct{}

func (nopTerminal) NewPty(context.Context) (workspaceapi.Pty, error) {
	return workspaceapi.Pty{}, nil
}
func (nopTerminal) SetPtySize(workspaceapi.Pty, workspaceapi.PtySize) error { return nil }
func (nopTerminal) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	return 0, errors.ErrUnsupported
}

var _ vte.View = (*testVte)(nil)

var testVteIDs atomic.Uint64

type testVte struct {
	component.String
	id             vte.ID
	usedAlt        bool
	clearedPrimary bool

	calledClose bool
}

func newTestVte() *testVte {
	ret := new(testVte)
	ret.String = component.NewString("")
	ret.id = vte.ID(fmt.Sprint(testVteIDs.Add(1)))
	return ret
}

func (t *testVte) ID() vte.ID { return t.id }

// SystemCanDispatchBell answers at once: the test terminal is always
// at its prompt.
func (t *testVte) SystemCanDispatchBell(callback func(error)) { callback(nil) }

func (t *testVte) Handle(ev term.Event) (bool, bool) {
	return false, false
}

func (t *testVte) SeekUp() bool {
	return false
}

func (t *testVte) SeekDown() bool {
	return false
}

func (t *testVte) SeekOffset() int {
	return 0
}

func (t *testVte) MaxSeekOffset() int {
	return 0
}

func (t *testVte) Cursor() (ret term.Coordinates, style term.CursorStyle, show bool) {
	show = true
	return
}

func (t *testVte) Selection() (string, bool) {
	return "", false
}

func (t *testVte) Dimensions() (int, int) {
	return 0, 0
}

func (t *testVte) Close() error {
	if t.calledClose {
		return errors.New("called close twice")
	}
	t.calledClose = true
	return nil
}

func (t *testVte) OnFocusChange(inFocus bool) {
}

func (t *testVte) SetDefaultAttributes(attr term.Attributes) {
}

func (t *testVte) Snapshot() (vte.Snapshot, error) {
	return vte.Snapshot{}, nil
}

func (t *testVte) RestoreFromSnapshot(vte.Snapshot) error {
	return nil
}

func (t *testVte) IsComplete() bool {
	return false
}

func (t *testVte) URI() workspaceapi.URI {
	return workspaceapi.URI{}
}

func (t *testVte) Title() string {
	return ""
}

func (v *testVte) UsedAlternateBuffer() bool {
	return v.usedAlt
}

func (v *testVte) ClearPrimaryBuffer() bool {
	v.clearedPrimary = true
	return true
}

func (v *testVte) ExitErr() error {
	return nil
}

func (v *testVte) CursorAtScroll() term.Coordinates {
	return term.Coordinates{}
}

// fakeRemoteScheme mirrors workspace.RemoteScheme for tests that
// exercise Facility.watchRemoteScheme without spinning up a real
// SSH transport. The disconnect channel is allocated once at
// construction and closed once by broadcastDisconnect — matching
// the production contract (see workspace.RemoteScheme).
type fakeRemoteScheme struct {
	ch              chan struct{}
	closeOnce       sync.Once
	nilOnDisconnect bool
}

func newFakeRemoteScheme() *fakeRemoteScheme {
	return &fakeRemoteScheme{ch: make(chan struct{})}
}

func (r *fakeRemoteScheme) OnDisconnect() <-chan struct{} {
	if r.nilOnDisconnect {
		return nil
	}
	return r.ch
}

func (r *fakeRemoteScheme) WaitConnected(context.Context) error {
	return nil
}

// connectGatedTerminal simulates a remote scheme whose transport is
// still being established (e.g. first-connect provisioning):
// WaitConnected blocks until the test closes connected. The embedded
// Terminal (a NopScheme) serves the pty RPCs once resolved.
type connectGatedTerminal struct {
	schemeapi.Terminal
	connected    chan struct{}
	disconnectCh chan struct{}
}

func (g *connectGatedTerminal) OnDisconnect() <-chan struct{} {
	return g.disconnectCh
}

func (g *connectGatedTerminal) WaitConnected(ctx context.Context) error {
	select {
	case <-g.connected:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *fakeRemoteScheme) broadcastDisconnect() {
	r.closeOnce.Do(func() { close(r.ch) })
}

// recordingVTE is a minimal VTE used by the remote-scheme tests.
// Unlike testVte it does not peek at Facility internals from
// Close, so it is safe to drive across goroutines while another
// goroutine mutates the pool.
type recordingVTE struct {
	component.String
	id       vte.ID
	closed   atomic.Bool
	complete atomic.Bool
}

func newRecordingVTE() *recordingVTE {
	r := new(recordingVTE)
	r.String = component.NewString("")
	r.id = vte.ID(fmt.Sprint(testVteIDs.Add(1)))
	return r
}

func (r *recordingVTE) ID() vte.ID { return r.id }

func (r *recordingVTE) Handle(term.Event) (bool, bool) { return false, false }
func (r *recordingVTE) SeekUp() bool                   { return false }
func (r *recordingVTE) SeekDown() bool                 { return false }
func (r *recordingVTE) SeekOffset() int                { return 0 }
func (r *recordingVTE) MaxSeekOffset() int             { return 0 }
func (r *recordingVTE) Cursor() (term.Coordinates, term.CursorStyle, bool) {
	return term.Coordinates{}, term.CursorStyleDefault, false
}
func (r *recordingVTE) Selection() (string, bool)              { return "", false }
func (r *recordingVTE) Dimensions() (int, int)                 { return 0, 0 }
func (r *recordingVTE) OnFocusChange(bool)                     {}
func (r *recordingVTE) SetDefaultAttributes(term.Attributes)   {}
func (r *recordingVTE) Snapshot() (vte.Snapshot, error)        { return vte.Snapshot{}, nil }
func (r *recordingVTE) RestoreFromSnapshot(vte.Snapshot) error { return nil }
func (r *recordingVTE) IsComplete() bool                       { return r.complete.Load() }
func (r *recordingVTE) URI() workspaceapi.URI                  { return workspaceapi.URI{} }
func (r *recordingVTE) Title() string                          { return "" }
func (r *recordingVTE) UsedAlternateBuffer() bool              { return false }
func (r *recordingVTE) ClearPrimaryBuffer() bool               { return true }
func (r *recordingVTE) ExitErr() error                         { return nil }
func (r *recordingVTE) CursorAtScroll() term.Coordinates       { return term.Coordinates{} }
func (r *recordingVTE) Close() error                           { r.closed.Store(true); return nil }

func TestFacilityWarmsAndServesTheRuneShell(t *testing.T) {
	t.Parallel()
	fx := vshtest.New(t, vshtest.Options{})
	cfg := vte.DefaultConfig()
	cfg.CommandAndArgs = []string{"sh"}
	cfg.WidthHint, cfg.HeightHint = 20, 10
	f := New(newTestRegistry(t, fx.Exec, cfg), fx.Exec, cfg, 2)
	t.Cleanup(func() { _ = f.Close() })
	f.WaitForInitialFill()
	assert.Equal(t, 2, f.Capacity(), "the warm-up started two shells")

	v, err := f.Get()
	require.NoError(t, err)
	t.Cleanup(func() { _ = v.Close() })
	v.Resize(20, 10)
	w := term.NewStringWriter(20, 10)
	waitScreen := func(prefix string) {
		t.Helper()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			require.NoError(c, w.Clear(term.Attributes{}))
			v.Draw(w)
			require.NoError(c, w.Flush())
			assert.True(c, strings.HasPrefix(w.String(), prefix), "screen was:\n%s", w.String())
		}, 5*time.Second, 5*time.Millisecond)
	}
	// Keys typed before the shell draws its prompt are echoed above it,
	// as on any tty.
	waitScreen("$ ")
	for _, b := range []byte("echo hi\n") {
		v.Handle(term.Event{Type: term.EventKey, Ch: rune(b), Raw: []byte{b}})
	}
	waitScreen("$ echo hi           \nhi                  \n$ ")
	assert.NoError(t, v.ExitErr())
}
