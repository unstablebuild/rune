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

package ide

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vtereservoir"
	"unstable.build/rune/internal/text/texttest"
	"unstable.build/rune/internal/workspace"
)

// replayVTE records the calls asyncVTE replays after the swap so the
// tests can assert both delivery and ordering.
type replayVTE struct {
	*testVte
	ops           []string
	events        []term.Event
	width, height int
}

func (r *replayVTE) Resize(width, height int) {
	r.ops = append(r.ops, "resize")
	r.width, r.height = width, height
}

func (r *replayVTE) RestoreFromSnapshot(s vte.Snapshot) error {
	r.ops = append(r.ops, "restore")
	// Mirror vte.Handler: a restore drives the snapshot's dimensions
	// through the component's resize path.
	r.width, r.height = max(1, s.Width), max(1, s.Height)
	return r.testVte.RestoreFromSnapshot(s)
}

func (r *replayVTE) SetDefaultAttributes(attr term.Attributes) {
	r.ops = append(r.ops, "attrs")
	r.testVte.SetDefaultAttributes(attr)
}

func (r *replayVTE) OnFocusChange(inFocus bool) {
	r.ops = append(r.ops, "focus")
	r.testVte.OnFocusChange(inFocus)
}

func (r *replayVTE) Handle(ev term.Event) (bool, bool) {
	r.ops = append(r.ops, "key")
	r.events = append(r.events, ev)
	return false, true
}

// testRemoteWorkspace decorates testLoader with the RemoteScheme
// surface so ex.doInit detects a remote workspace (non-nil
// disconnect channel).
type testRemoteWorkspace struct {
	*testLoader
	ch chan struct{}
}

func (w testRemoteWorkspace) OnDisconnect() <-chan struct{} {
	return w.ch
}

func (w testRemoteWorkspace) WaitConnected(context.Context) error {
	return nil
}

// newAsyncVTETestEx builds an ex without overriding newEmulatorHandler
// so the production remote-workspace gating installed by init stays
// in place.
func newAsyncVTETestEx(t *testing.T, ws workspace.Workspace) testEx {
	t.Helper()
	return newExForTestingVTECapacity(t, ws, 0)
}

func drawToString(t *testing.T, av *asyncVTE, width, height int) string {
	t.Helper()
	w := term.NewStringWriter(width, height)
	av.Draw(w)
	require.NoError(t, w.Flush())
	return w.String()
}

func TestAsyncVTE(t *testing.T) {
	t.Run("returns immediately and draws the loading animation while the factory blocks", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		tv := newTestVte()
		av := newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
			<-release
			return tv, nil
		})

		assert.False(t, av.IsComplete())
		assert.Equal(t, asyncVTELoadingTitle, av.Title())
		assert.True(t, strings.HasPrefix(av.URI().String(), "terminal://loading/"),
			"placeholder URI must be stable and unique, got %q", av.URI())

		av.Resize(10, 3)
		out := drawToString(t, av, 10, 3)
		assert.NotEmpty(t, strings.TrimSpace(out),
			"pre-ready Draw must render an animation frame")

		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		require.NotNil(t, av.real)
		assert.Equal(t, tv.Title(), av.Title())
		require.NoError(t, av.Close())
	})

	t.Run("replays queued operations on the real VTE after completion", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		rv := &replayVTE{testVte: newTestVte()}
		av := newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
			<-release
			return rv, nil
		})

		av.Resize(42, 17)
		snap := vte.Snapshot{
			Schema: 1,
			Title:  "saved session",
			Primary: vte.ScreenSnapshot{
				Cells: term.StringToCells("echo hi"),
			},
		}
		require.NoError(t, av.RestoreFromSnapshot(snap))
		av.SetDefaultAttributes(term.Attributes{Fg: term.ColorRed})
		av.OnFocusChange(true)
		exit, handled := av.Handle(term.Event{Type: term.EventKey, Ch: 'l'})
		assert.False(t, exit)
		assert.False(t, handled,
			"pre-ready keys stay unhandled so global bindings keep working")
		_, handled = av.Handle(term.Event{Type: term.EventKey, Ch: 's'})
		assert.False(t, handled)
		_, handled = av.Handle(term.Event{Type: term.EventMouse})
		assert.False(t, handled, "mouse events are dropped pre-ready")

		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		require.NotNil(t, av.real)
		assert.Equal(t, []string{
			"restore", "resize", "attrs", "focus", "key", "key",
		}, rv.ops, "unclaimed pre-ready keys must still replay")
		assert.Equal(t, 42, rv.width)
		assert.Equal(t, 17, rv.height)
		assert.True(t, rv.restoredSnapshot)
		assert.Equal(t, term.Attributes{Fg: term.ColorRed}, rv.defAttr)
		assert.Equal(t, []bool{true}, rv.onFocusChange)
		require.Len(t, rv.events, 2)
		assert.Equal(t, 'l', rv.events[0].Ch)
		assert.Equal(t, 's', rv.events[1].Ch)
		require.NoError(t, av.Close())
	})

	t.Run("restore precedes resize so the pane dimensions win", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		rv := &replayVTE{testVte: newTestVte()}
		av := newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
			<-release
			return rv, nil
		})

		// The WM lays out the placeholder at the pane dimensions before
		// the factory completes; the queued session restore carries the
		// dimensions the snapshot was saved at.
		av.Resize(100, 30)
		require.NoError(t, av.RestoreFromSnapshot(vte.Snapshot{
			Schema: 1, Width: 120, Height: 40,
		}))

		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		require.NotNil(t, av.real)
		assert.Equal(t, []string{"restore", "resize"}, rv.ops,
			"RestoreFromSnapshot drives the snapshot's dimensions through "+
				"the resize path, so restoring after the pane resize leaves "+
				"the terminal and its pty at the saved session's dimensions: "+
				"a taller snapshot then keeps the shell cursor below the "+
				"visible pane")
		assert.Equal(t, 100, rv.width)
		assert.Equal(t, 30, rv.height)
		require.NoError(t, av.Close())
	})

	t.Run("close before ready closes the real VTE on completion", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		tv := newTestVte()
		av := newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
			<-release
			return tv, nil
		})

		require.NoError(t, b.ex.tm.SetTabStatus(tv.URI(), programstatus.Report{
			State: programstatus.Blocked,
		}))
		b.flushScheduled()
		require.NoError(t, av.Close())
		require.NoError(t, av.Close(), "Close must be idempotent")

		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		assert.True(t, tv.calledClose,
			"abandoned VTE must be released on completion")
		assert.Nil(t, av.real, "nothing may be installed after Close")
		assert.Empty(t, b.ex.tabAliases.pending)
		require.NoError(t, b.ex.tm.SetTabStatus(tv.URI(), programstatus.Report{
			State: programstatus.Blocked,
		}))
		b.flushScheduled()
		assert.Empty(t, b.ex.tabAliases.pending)
	})

	t.Run("factory error renders and notifies", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()
		rec := &recordingNotifications{inner: b.ex.notifications}
		b.ex.notifications = rec

		av := newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
			return nil, errors.New("stalled transport")
		})
		av.Resize(40, 3)

		b.waitAsyncVTELoads()
		b.flushScheduled()

		assert.True(t, av.IsComplete(),
			"failed placeholder must report complete so it can be replaced")
		notes := rec.snapshot()
		require.NotEmpty(t, notes)
		assert.Equal(t, browserapi.LevelError, notes[0].level)
		assert.Contains(t, notes[0].msg, "stalled transport")
		out := drawToString(t, av, 40, 3)
		assert.Contains(t, out, "terminal unavailable")
		assert.Contains(t, out, "stalled transport")
		require.NoError(t, av.Close())
	})

	t.Run("pre-ready Snapshot returns the queued restore snapshot", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		av := newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
			<-release
			return newTestVte(), nil
		})
		defer func() {
			close(release)
			b.waitAsyncVTELoads()
			b.flushScheduled()
			require.NoError(t, av.Close())
		}()

		got, err := av.Snapshot()
		require.NoError(t, err)
		assert.Equal(t, vte.Snapshot{}, got,
			"no queued snapshot yields a zero snapshot")

		snap := vte.Snapshot{Schema: 1, Title: "saved session"}
		require.NoError(t, av.RestoreFromSnapshot(snap))
		got, err = av.Snapshot()
		require.NoError(t, err)
		assert.Equal(t, snap, got,
			"a save during load must keep the session")
	})
}

func TestAsyncVTEPointerLeaves(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	for _, tc := range []struct {
		name    string
		factory func() (vtereservoir.VTE, error)
	}{
		{name: "loading", factory: func() (vtereservoir.VTE, error) {
			<-release
			return nil, errors.New("abandoned")
		}},
		{name: "failed", factory: func() (vtereservoir.VTE, error) {
			return nil, errors.New("stalled transport")
		}},
		{name: "running", factory: func() (vtereservoir.VTE, error) {
			return &replayVTE{testVte: newTestVte()}, nil
		}},
		{name: "exited with an error", factory: func() (vtereservoir.VTE, error) {
			return &exitingVTE{
				testVte: newTestVte(), exitErr: errors.New("exit status 2"),
				screen: terminalScreen{rows: []string{"$ make", "Error 2", ""}, cursorRow: 2},
			}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newExForTesting(t, texttest.NopEditor())
			t.Cleanup(func() { _ = b.Close() })
			av := newAsyncVTE(b.ex, tc.factory)
			if tc.name != "loading" {
				b.waitAsyncVTELoads()
				b.flushScheduled()
			}
			unlock := b.lock()
			defer unlock()
			handlertest.RunPointerLeaves(t, av, 80, 24)
			handlertest.RunPointerLeaves(t, av, 2, 2)
		})
	}
}

func TestNewEmulatorHandlerAlwaysAsync(t *testing.T) {
	t.Run("remote workspace wraps terminals in asyncVTE", func(t *testing.T) {
		ws := testRemoteWorkspace{
			testLoader: &testLoader{},
			ch:         make(chan struct{}),
		}
		b := newAsyncVTETestEx(t, ws)
		defer b.Close()

		h, err := b.newEmulatorHandler(nil)
		require.NoError(t, err,
			"remote spawn must not fail synchronously")
		av, ok := h.(*asyncVTE)
		require.True(t, ok,
			"remote workspace terminals must go through asyncVTE")

		b.waitAsyncVTELoads()
		b.flushScheduled()
		require.NoError(t, av.Close())
	})

	t.Run("local workspace terminals also go through asyncVTE", func(t *testing.T) {
		b := newAsyncVTETestEx(t, &testLoader{})
		defer b.Close()

		h, err := b.newEmulatorHandler(nil)
		require.NoError(t, err)
		av, ok := h.(*asyncVTE)
		require.True(t, ok,
			"every terminal spawn must go through asyncVTE")

		b.waitAsyncVTELoads()
		b.flushScheduled()
		require.NotNil(t, av.real,
			"local spawn must complete once the scheduler drains")
		require.NoError(t, h.Close())
	})
}

// recordingTabManager records SetTabName calls keyed by the URI they
// finally resolved to.
type recordingTabManager struct {
	names    map[string]string
	exits    []string
	statuses map[string]programstatus.State
	ends     []string
}

func (r *recordingTabManager) Tab(
	_ workspaceapi.URI, _ rune, _ string, h browserapi.Handler,
) (browserapi.Handler, error) {
	return h, nil
}

func (r *recordingTabManager) SetTabName(
	uri workspaceapi.URI, name string, _ term.Attributes,
) error {
	r.names[uri.String()] = name
	return nil
}

func (r *recordingTabManager) OnTabExit(uri workspaceapi.URI) bool {
	r.exits = append(r.exits, uri.String())
	return true
}

func (r *recordingTabManager) SetTabStatus(
	uri workspaceapi.URI, report programstatus.Report,
) error {
	if r.statuses == nil {
		r.statuses = make(map[string]programstatus.State)
	}
	r.statuses[uri.String()] = report.State
	return nil
}

func (r *recordingTabManager) EndTabStatus(uri workspaceapi.URI) error {
	r.ends = append(r.ends, uri.String())
	return nil
}

func TestTabNameAliaser(t *testing.T) {
	mustURI := func(s string) workspaceapi.URI {
		uri, err := workspaceapi.ParseURI(s)
		require.NoError(t, err)
		return uri
	}
	ptyURI := mustURI("file:///dev/ttys042")
	wrapperURI := mustURI("terminal://loading/9000")
	sessionURI := mustURI("terminal-session://x/build")

	newAliaser := func() (*tabNameAliaser, *recordingTabManager) {
		rec := &recordingTabManager{names: map[string]string{}}
		inline := func(fn func()) bool { fn(); return true }
		return newTabNameAliaser(rec, inline), rec
	}

	t.Run("no alias passes the URI through", func(t *testing.T) {
		a, rec := newAliaser()
		require.NoError(t, a.SetTabName(ptyURI, "vim", term.Attributes{}))
		assert.Equal(t, map[string]string{ptyURI.String(): "vim"}, rec.names)
	})

	t.Run("aliases follow chains to the tab key", func(t *testing.T) {
		a, rec := newAliaser()
		a.addAlias(ptyURI, wrapperURI)
		a.addAlias(wrapperURI, sessionURI)
		require.NoError(t, a.SetTabName(ptyURI, "vim", term.Attributes{}))
		require.NoError(t, a.SetTabName(wrapperURI, "make", term.Attributes{}))
		assert.Equal(t, map[string]string{
			sessionURI.String(): "make",
		}, rec.names, "both chain entry points must resolve to the tab key")
	})

	t.Run("self aliases are ignored", func(t *testing.T) {
		a, _ := newAliaser()
		a.addAlias(ptyURI, ptyURI)
		assert.Empty(t, a.alias)
	})

	t.Run("removeAliasesTo drops direct links only", func(t *testing.T) {
		a, rec := newAliaser()
		a.addAlias(ptyURI, wrapperURI)
		a.addAlias(wrapperURI, sessionURI)
		a.removeAliasesTo(wrapperURI)
		require.NoError(t, a.SetTabName(ptyURI, "vim", term.Attributes{}))
		assert.Equal(t, "vim", rec.names[ptyURI.String()],
			"removed alias must pass through again")
		require.NoError(t, a.SetTabName(wrapperURI, "make", term.Attributes{}))
		assert.Equal(t, "make", rec.names[sessionURI.String()],
			"unrelated aliases must survive")
	})

	t.Run("OnTabExit resolves aliases to the tab key", func(t *testing.T) {
		a, rec := newAliaser()
		a.addAlias(ptyURI, wrapperURI)
		require.True(t, a.OnTabExit(ptyURI))
		assert.Equal(t, []string{wrapperURI.String()}, rec.exits)
	})

	t.Run("status calls resolve aliases on the event loop", func(t *testing.T) {
		rec := &recordingTabManager{names: map[string]string{}}
		loop := newQueuedScheduler()
		a := newTabNameAliaser(rec, loop.ScheduleNextTick)

		working := programstatus.Report{State: programstatus.Working, Progress: -1}
		require.NoError(t, a.SetTabStatus(ptyURI, working))
		require.NoError(t, a.EndTabStatus(ptyURI))
		assert.Empty(t, rec.statuses, "the tab manager is only touched on the event loop")
		assert.Empty(t, rec.ends)
		a.addAlias(ptyURI, wrapperURI)
		a.addAlias(wrapperURI, sessionURI)
		loop.Flush(nil)
		assert.Equal(t, map[string]programstatus.State{
			sessionURI.String(): programstatus.Working,
		}, rec.statuses)
		assert.Equal(t, []string{sessionURI.String()}, rec.ends)
	})

	t.Run("early status migrates before prompt cleanup", func(t *testing.T) {
		a, rec := newAliaser()
		require.NoError(t, a.SetTabStatus(ptyURI, programstatus.Report{
			State: programstatus.Blocked, Progress: -1,
		}))
		a.addAlias(wrapperURI, sessionURI)
		a.addAlias(ptyURI, wrapperURI)
		assert.Equal(t, programstatus.Clear, rec.statuses[ptyURI.String()])
		assert.Equal(t, programstatus.Blocked, rec.statuses[sessionURI.String()])
		require.NoError(t, a.EndTabStatus(ptyURI))
		assert.Equal(t, []string{sessionURI.String()}, rec.ends)
	})

	t.Run("migration preserves clear end and update order", func(t *testing.T) {
		a, _ := newAliaser()
		for _, r := range []programstatus.Report{
			{ID: "z", State: programstatus.Done, App: "original"},
			{ID: "a", State: programstatus.Done},
			{ID: "z", State: programstatus.Done},
			{ID: "prompt/child", State: programstatus.Blocked},
			{ID: "prompt", State: programstatus.Clear},
			{ID: "work", State: programstatus.Working},
		} {
			require.NoError(t, a.SetTabStatus(ptyURI, r))
		}
		require.NoError(t, a.EndTabStatus(ptyURI))
		a.addAlias(ptyURI, wrapperURI)
		a.addAlias(wrapperURI, sessionURI)
		assert.Equal(t, []programstatus.Report{
			{ID: "a", State: programstatus.Done},
			{ID: "z", State: programstatus.Done},
		}, a.pending[sessionURI.String()])
		require.NoError(t, a.SetTabStatus(ptyURI, programstatus.Report{State: programstatus.Clear}))
		assert.Empty(t, a.pending)
	})

	t.Run("closed terminals reject queued status and may reuse identity", func(t *testing.T) {
		rec := &recordingTabManager{names: map[string]string{}}
		loop := newQueuedScheduler()
		a := newTabNameAliaser(rec, loop.ScheduleNextTick)
		require.NoError(t, a.SetTabStatus(ptyURI, programstatus.Report{State: programstatus.Blocked}))
		a.discardStatus(ptyURI)
		loop.Flush(nil)
		assert.Equal(t, programstatus.Clear, rec.statuses[ptyURI.String()])
		assert.Empty(t, a.pending)
		a.addAlias(ptyURI, wrapperURI)
		require.NoError(t, a.SetTabStatus(ptyURI, programstatus.Report{State: programstatus.Working}))
		loop.Flush(nil)
		assert.Equal(t, programstatus.Working, rec.statuses[wrapperURI.String()])
	})

	t.Run("unclaimed reservoir records stay bounded", func(t *testing.T) {
		a, _ := newAliaser()
		for i := 0; i <= programstatus.MaxRecords; i++ {
			require.NoError(t, a.SetTabStatus(ptyURI, programstatus.Report{
				ID: fmt.Sprint(i), State: programstatus.Done,
			}))
		}
		a.addAlias(ptyURI, wrapperURI)
		require.Len(t, a.pending[wrapperURI.String()], programstatus.MaxRecords)
		assert.Equal(t, "1", a.pending[wrapperURI.String()][0].ID)
	})

	t.Run("SetTabName resolves on the event loop", func(t *testing.T) {
		rec := &recordingTabManager{names: map[string]string{}}
		loop := newQueuedScheduler()
		a := newTabNameAliaser(rec, loop.ScheduleNextTick)

		// A vte can title itself before its spawn completes and
		// aliases its URI to the tab.
		require.NoError(t, a.SetTabName(ptyURI, "vim", term.Attributes{}))
		assert.Empty(t, rec.names, "the tab manager is only touched on the event loop")
		a.addAlias(ptyURI, wrapperURI)
		loop.Flush(nil)
		assert.Equal(t, map[string]string{wrapperURI.String(): "vim"}, rec.names)
	})
}

func TestAsyncVTETabNameAliasing(t *testing.T) {
	mustURI := func(s string) workspaceapi.URI {
		uri, err := workspaceapi.ParseURI(s)
		require.NoError(t, err)
		return uri
	}

	t.Run("workspace sources survive delayed migration and cleanup", func(t *testing.T) {
		for _, closeBeforeReplay := range []bool{false, true} {
			t.Run(fmt.Sprintf("closeBeforeReplay=%t", closeBeforeReplay), func(t *testing.T) {
				m, slot := newActivityTestManager(t, defaultConfigWithWrap(false))
				t.Cleanup(func() { require.NoError(t, m.Close()) })
				pty := mustURI("file:///dev/ttys042")
				m.locked(func() {
					w := m.workspaces[slot]
					require.NoError(t, w.ex.tm.SetTabStatus(pty, programstatus.Report{
						State: programstatus.Blocked, Progress: -1,
					}))
				})
				m.quiesce()
				var tabURI workspaceapi.URI
				m.locked(func() {
					w := m.workspaces[slot]
					sum, ok := m.status.Status(w.uri, pty)
					require.True(t, ok)
					assert.Equal(t, programstatus.Blocked, sum.State)
					av := &asyncVTE{e: w.ex, uri: mustURI("terminal://loading/9000"), animStopped: true}
					tabURI = av.URI()
					tab, err := w.ex.tm.Tab(tabURI, 'x', "terminal", av)
					require.NoError(t, err)
					tv := newTestVte()
					tv.uri = pty
					av.complete(tv, nil)
					if closeBeforeReplay {
						require.NoError(t, tab.Close())
					} else {
						require.NoError(t, w.ex.tm.EndTabStatus(pty))
					}
				})
				m.quiesce()
				m.locked(func() {
					w := m.workspaces[slot]
					_, ok := m.status.Status(w.uri, pty)
					assert.False(t, ok, "migration must remove the detached PTY source")
					// The tab is still open, so it stays listed, holding nothing.
					sum, _ := m.status.Status(w.uri, tabURI)
					assert.Empty(t, sum.Records, "delayed replay must not outlive cleanup")
					sum, _ = m.workspaceStatus(slot)
					assert.Empty(t, sum.Records)
					assert.Empty(t, w.ex.tabAliases.pending)
				})
			})
		}
	})

	t.Run("real vte title updates reach the wrapper-keyed tab", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		tv := newTestVte()
		tv.uri = mustURI("file:///dev/ttys042")
		tv.title = "vim"
		av := newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
			<-release
			return tv, nil
		})
		tab, err := b.comp.Tab(av.URI(), 'x', av.Title(), av)
		require.NoError(t, err)

		require.NoError(t, b.ex.tm.SetTabStatus(tv.uri, programstatus.Report{
			State: programstatus.Blocked, Progress: -1,
		}))
		b.flushScheduled()
		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()
		early, ok := b.comp.Browser().TabStatus(av.URI())
		require.True(t, ok)
		assert.Equal(t, programstatus.Blocked, early.State)
		require.NoError(t, b.ex.tm.EndTabStatus(tv.uri))
		b.flushScheduled()
		early, _ = b.comp.Browser().TabStatus(av.URI())
		assert.Empty(t, early.Records)

		name, _, ok := b.comp.Browser().TabName(av.URI())
		require.True(t, ok)
		assert.Equal(t, "vim", name,
			"completion must replace the placeholder title")

		require.NoError(t, b.ex.tm.SetTabName(
			tv.uri, "make test", term.Attributes{}))
		b.flushScheduled()
		name, _, ok = b.comp.Browser().TabName(av.URI())
		require.True(t, ok)
		assert.Equal(t, "make test", name,
			"dynamic pty-URI updates must reach the wrapper-keyed tab")

		working := programstatus.Report{State: programstatus.Working, Progress: 40}
		require.NoError(t, b.ex.tm.SetTabStatus(tv.uri, working))
		b.flushScheduled()
		status, ok := b.comp.Browser().TabStatus(av.URI())
		require.True(t, ok)
		assert.Equal(t, programstatus.Working, status.State,
			"pty-URI status reports must reach the wrapper-keyed tab")
		assert.Equal(t, 40, status.Progress)

		require.NoError(t, tab.Close())
		assert.Empty(t, b.ex.tabAliases.alias,
			"closing the tab must clear its aliases")
	})

	t.Run("restored session tabs receive title updates from the real vte", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		release := make(chan struct{})
		tv := newTestVte()
		tv.uri = mustURI("file:///dev/ttys043")
		tv.title = "vim"
		b.ex.newEmulatorHandler = func([]string) (vtereservoir.VTE, error) {
			return newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
				<-release
				return tv, nil
			}), nil
		}

		doc := terminalSessionDocument{
			Name:     "build",
			Snapshot: vte.Snapshot{Schema: 1, Title: "saved"},
		}
		tab, err := b.ex.restoreTerminalSessionTab(doc, nil)
		require.NoError(t, err)
		sessionURI, err := terminalSessionURI(doc.Name)
		require.NoError(t, err)

		close(release)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		name, _, ok := b.comp.Browser().TabName(sessionURI)
		require.True(t, ok)
		assert.Equal(t, "vim", name,
			"completion must chain the real title to the session tab")

		require.NoError(t, b.ex.tm.SetTabName(
			tv.uri, "make build", term.Attributes{}))
		b.flushScheduled()
		name, _, ok = b.comp.Browser().TabName(sessionURI)
		require.True(t, ok)
		assert.Equal(t, "make build", name,
			"pty-URI updates must chain through the wrapper to the session tab")

		require.NoError(t, tab.Close())
		assert.Empty(t, b.ex.tabAliases.alias,
			"closing the session tab must clear its aliases")
	})

	t.Run("title updates from the parser goroutine land on the event loop", func(t *testing.T) {
		b := newExForTesting(t, texttest.NopEditor())
		defer b.Close()

		tv := newTestVte()
		tv.uri = mustURI("file:///dev/ttys044")
		av := newAsyncVTE(b.ex, func() (vtereservoir.VTE, error) {
			return tv, nil
		})
		_, err := b.comp.Tab(av.URI(), 'x', av.Title(), av)
		require.NoError(t, err)
		b.waitAsyncVTELoads()
		b.flushScheduled()

		parsed := make(chan struct{})
		go debug.CapturePanicReport(func() {
			defer close(parsed)
			_ = b.ex.tm.SetTabName(tv.uri, "make test", term.Attributes{})
		})
		// The event loop keeps drawing the tab bar meanwhile.
		handlertest.DrawHandler(b, 40, 10)
		<-parsed
		b.flushScheduled()

		name, _, ok := b.comp.Browser().TabName(av.URI())
		require.True(t, ok)
		assert.Equal(t, "make test", name)
	})
}
