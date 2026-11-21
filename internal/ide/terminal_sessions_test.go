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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/term/vte/vteregistry"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// fgPtyExecutor is a PtyExecutor whose ptys run fg in the foreground.
// Its host answers only while hold is not locked.
type fgPtyExecutor struct {
	*workspacetest.PtyExecutor
	fg   atomic.Int64
	hold sync.RWMutex
}

func (t *fgPtyExecutor) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	t.hold.RLock()
	defer t.hold.RUnlock()
	return int(t.fg.Load()), nil
}

// useTestVTEs makes the terminals e opens run on the returned executor,
// with no shell kept warm.
func useTestVTEs(t *testing.T, e *ex) *fgPtyExecutor {
	t.Helper()
	exe := &fgPtyExecutor{PtyExecutor: workspacetest.NewPtyExecutor()}
	require.NoError(t, e.terminals.close())
	cfg := e.terminals.cfg
	cfg.Terminal, cfg.ReservoirCapacity = exe, 0
	vtes := vteregistry.New(e.Browser(), e.Browser(), exe, exe, e.tm,
		vteregistry.Config{VTE: cfg.VTE})
	e.terminals = newTerminalSessions(vtes, e.terminals.tabs, cfg)
	e.newEmulatorHandler = func(args []string) (vte.View, error) {
		return e.terminals.open(args), nil
	}
	return exe
}

type nopTerminalHost struct{}

func (nopTerminalHost) PublishEvent(term.Event) error { return nil }

func (nopTerminalHost) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopTerminalHost) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopTerminalHost) UpdateNotificationProgress(string, string, int64, int64) error {
	return nil
}

// newTestTerminalTabs returns the tabs of terminals that report to a
// recordingTabManager on loop, and whose failed tabs are not kept.
func newTestTerminalTabs(loop *queuedScheduler) failedTerminalKeeper {
	return failedTerminalKeeper{
		tabNameAliaser: newTabNameAliaser(
			&recordingTabManager{names: map[string]string{}}, loop.ScheduleNextTick),
		keep: func(workspaceapi.URI) bool { return false },
	}
}

// sessionsFixture is a terminalSessions whose terminals run on exe and
// whose event loop is the test goroutine.
type sessionsFixture struct {
	s      *terminalSessions
	exe    *fgPtyExecutor
	loop   *queuedScheduler
	cancel context.CancelFunc
}

func newSessionsFixture(t *testing.T, reservoirCapacity int) *sessionsFixture {
	t.Helper()
	return newSessionsFixtureWith(t, terminalSessionsConfig{ReservoirCapacity: reservoirCapacity})
}

// newSessionsFixtureWith builds the fixture with cfg's ReservoirCapacity
// and Durable.
func newSessionsFixtureWith(t *testing.T, cfg terminalSessionsConfig) *sessionsFixture {
	t.Helper()
	f := &sessionsFixture{
		exe:  &fgPtyExecutor{PtyExecutor: workspacetest.NewPtyExecutor()},
		loop: newQueuedScheduler(),
	}
	vteCfg := vte.DefaultConfig()
	vteCfg.ScheduleNextTick = f.loop.ScheduleNextTick
	var ctx context.Context
	ctx, f.cancel = context.WithCancel(context.Background())
	tabs := newTestTerminalTabs(f.loop)
	vtes := vteregistry.New(nopTerminalHost{}, nopTerminalHost{}, f.exe, f.exe, tabs,
		vteregistry.Config{VTE: vteCfg})
	f.s = newTerminalSessions(vtes, tabs, terminalSessionsConfig{
		Ctx: ctx, Publisher: nopTerminalHost{}, Notifications: nopTerminalHost{},
		Terminal: f.exe, VTE: vteCfg,
		ReservoirCapacity: cfg.ReservoirCapacity,
		Durable:           cfg.Durable,
	})
	t.Cleanup(func() {
		f.cancel()
		f.settle()
		assert.NoError(t, f.s.close())
	})
	return f
}

// settle runs what s scheduled on the event loop until s waits on
// nothing.
func (f *sessionsFixture) settle() {
	for {
		f.s.wait()
		f.loop.mu.Lock()
		idle := len(f.loop.pending) == 0
		f.loop.mu.Unlock()
		if idle {
			return
		}
		f.loop.Flush(nil)
	}
}

// open starts a terminal that runs cmdAndArgs, and returns its first
// view and its process.
func (f *sessionsFixture) open(t *testing.T, cmdAndArgs ...string) (*asyncVTE, *workspacetest.Process) {
	t.Helper()
	av := f.s.open(cmdAndArgs)
	// As the tab that shows it lays it out.
	av.Resize(40, 10)
	f.settle()
	require.NotNil(t, av.real, "the terminal starts")
	select {
	case p := <-f.exe.Started():
		return av, p
	case <-time.After(5 * time.Second):
		t.Fatal("no process started")
		return nil, nil
	}
}

// detach closes av, leaving its terminal running.
func (f *sessionsFixture) detach(t *testing.T, av *asyncVTE) {
	t.Helper()
	av.closeMode = vteCloseDetach
	require.NoError(t, av.Close())
	f.settle()
}

// end makes the process of a terminal exit, and waits for its terminal
// to complete or, when no view shows it, to be gone.
func (f *sessionsFixture) end(t *testing.T, id vte.ID, p *workspacetest.Process) {
	t.Helper()
	detached := f.attachments(id) == 0
	p.HangUp()
	p.ReportExit(nil)
	require.Eventually(t, func() bool {
		f.settle()
		info, ok := f.s.info(id)
		if detached {
			return !ok
		}
		return info.Complete
	}, 5*time.Second, 5*time.Millisecond)
}

// attachments is how many views the terminal id has, -1 when it is
// gone.
func (f *sessionsFixture) attachments(id vte.ID) int {
	info, ok := f.s.info(id)
	if !ok {
		return -1
	}
	return info.Attachments
}

// pooled returns the ids of the shells the reservoir keeps warm, once
// it warmed them.
func (f *sessionsFixture) pooled(t *testing.T) []vte.ID {
	t.Helper()
	f.s.reservoir.WaitForInitialFill()
	infos, err := f.s.vtes.ListVTEs(context.Background())
	require.NoError(t, err)
	var ret []vte.ID
	for _, info := range infos {
		if f.s.reservoir.Pooled(info.ID) {
			ret = append(ret, info.ID)
		}
	}
	return ret
}

func terminalIDs(infos []vte.Info) []vte.ID {
	var ret []vte.ID
	for _, info := range infos {
		ret = append(ret, info.ID)
	}
	return ret
}

// fakeVTEHost is a vte.Host of no terminals that tells its watchers the
// changes a test feeds it.
type fakeVTEHost struct {
	mu       sync.Mutex
	watchers map[*func(vte.Change)]struct{}
	closed   bool
}

func newFakeVTEHost() *fakeVTEHost {
	return &fakeVTEHost{watchers: make(map[*func(vte.Change)]struct{})}
}

func (o *fakeVTEHost) CreateVTE(context.Context, vte.CreateOptions) (vte.View, error) {
	return nil, errors.ErrUnsupported
}

func (o *fakeVTEHost) AttachVTE(context.Context, vte.ID) (vte.View, error) {
	return nil, vte.ErrNotFound
}

func (o *fakeVTEHost) PreviewVTE(context.Context, vte.ID) (vte.View, error) {
	return nil, vte.ErrNotFound
}

func (o *fakeVTEHost) ListVTEs(context.Context) ([]vte.Info, error) {
	return nil, nil
}

func (o *fakeVTEHost) VTELiveness(context.Context, vte.ID) (vte.Liveness, error) {
	return vte.LivenessUnknown, vte.ErrNotFound
}

func (o *fakeVTEHost) DeleteVTE(context.Context, vte.ID) error {
	return vte.ErrNotFound
}

func (o *fakeVTEHost) Watch(ctx context.Context, fn func(vte.Change)) {
	o.mu.Lock()
	o.watchers[&fn] = struct{}{}
	o.mu.Unlock()
	context.AfterFunc(ctx, func() {
		o.mu.Lock()
		delete(o.watchers, &fn)
		o.mu.Unlock()
	})
}

func (o *fakeVTEHost) Close() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closed = true
	return nil
}

// tell tells the watchers of change.
func (o *fakeVTEHost) tell(change vte.Change) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for fn := range o.watchers {
		(*fn)(change)
	}
}

func (o *fakeVTEHost) watching() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.watchers)
}

// newFakeVTEHostSessions returns a terminalSessions over owner, whose
// event loop is the test goroutine.
func newFakeVTEHostSessions(t *testing.T, owner *fakeVTEHost) (*terminalSessions, *queuedScheduler) {
	t.Helper()
	loop := newQueuedScheduler()
	cfg := vte.DefaultConfig()
	cfg.ScheduleNextTick = loop.ScheduleNextTick
	s := newTerminalSessions(owner, newTestTerminalTabs(loop), terminalSessionsConfig{
		Ctx: context.Background(), Publisher: nopTerminalHost{}, Notifications: nopTerminalHost{},
		VTE: cfg,
	})
	return s, loop
}

func TestTerminalSessionsOnChange(t *testing.T) {
	t.Parallel()
	const id vte.ID = "abc"
	uri := mustURI(t, "terminal:///abc")
	running := vte.Change{ID: id, URI: uri, Title: "make", Liveness: vte.LivenessRunning}
	idle := vte.Change{ID: id, URI: uri, Title: "zsh", Liveness: vte.LivenessIdle}
	attached := vte.Change{ID: id, URI: uri, Title: "zsh", Attachments: 1}
	deleted := vte.Change{ID: id, URI: uri, Deleted: true}
	type report struct {
		state programstatus.State
		title string
	}
	cases := []struct {
		desc    string
		changes []vte.Change
		// want is what the terminal is reported under its own URI.
		want []report
		// wantReported is whether the host record is posted after.
		wantReported bool
		// wantClosed is whether the terminal's records are discarded.
		wantClosed bool
	}{{
		desc:         "a detached job is reported working",
		changes:      []vte.Change{running},
		want:         []report{{programstatus.Working, "make"}},
		wantReported: true,
	}, {
		desc:         "then done, titled as it ran",
		changes:      []vte.Change{running, idle},
		want:         []report{{programstatus.Done, "make"}},
		wantReported: true,
	}, {
		desc:         "a job still running is reported once",
		changes:      []vte.Change{running, {ID: id, URI: uri, Title: "other", Liveness: vte.LivenessRunning}},
		want:         []report{{programstatus.Working, "make"}},
		wantReported: true,
	}, {
		desc:    "a detached shell at its prompt reports nothing",
		changes: []vte.Change{idle},
	}, {
		desc:    "attaching a job again withdraws the record",
		changes: []vte.Change{running, attached},
	}, {
		desc:    "attaching a done job withdraws the record",
		changes: []vte.Change{running, idle, attached},
	}, {
		desc: "a shell that exited is not reported done",
		changes: []vte.Change{running,
			{ID: id, URI: uri, Liveness: vte.LivenessIdle, Complete: true}},
		want:         []report{{programstatus.Working, "make"}},
		wantReported: true,
	}, {
		desc:       "deleting a job discards its records",
		changes:    []vte.Change{running, deleted},
		wantClosed: true,
	}, {
		desc: "a shown terminal reports nothing",
		changes: []vte.Change{attached, {ID: id, URI: uri, Attachments: 2},
			{ID: id, URI: uri, Attachments: 1, Complete: true}},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			owner := newFakeVTEHost()
			s, loop := newFakeVTEHostSessions(t, owner)
			t.Cleanup(func() { assert.NoError(t, s.close()) })
			for _, change := range tc.changes {
				before := slices.Clone(s.tabs.pending[uri.String()])
				owner.tell(change)
				assert.Equal(t, before, s.tabs.pending[uri.String()], "it is acted on on the event loop")
				loop.Flush(nil)
			}
			var got []report
			for _, r := range s.tabs.pending[uri.String()] {
				got = append(got, report{r.State, r.Title})
				assert.Equal(t, hostReportID, r.ID)
				assert.Equal(t, programstatus.HostApp, r.App)
			}
			assert.Equal(t, tc.want, got)
			_, reported := s.reported[id]
			assert.Equal(t, tc.wantReported, reported)
			assert.Equal(t, tc.wantClosed, s.tabs.closed[uri.String()])
		})
	}
}

func TestTerminalSessionsOwner(t *testing.T) {
	t.Parallel()
	owner := newFakeVTEHost()
	s, loop := newFakeVTEHostSessions(t, owner)
	assert.Same(t, owner, s.vtes, "the configured owner holds the terminals")
	assert.Equal(t, 1, owner.watching(), "its changes are followed")
	assert.Nil(t, s.reservoir, "no shell is kept warm without a capacity")

	av := s.open(nil)
	loop.Flush(nil)
	s.wait()
	loop.Flush(nil)
	assert.True(t, av.IsComplete(), "a view the owner cannot serve fails")

	require.NoError(t, s.close())
	assert.True(t, owner.closed)
	require.Eventually(t, func() bool { return owner.watching() == 0 },
		5*time.Second, 5*time.Millisecond, "closing stops following the owner")
}

func TestTerminalSessionsList(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc     string
		capacity int
		// run opens the terminals of the case, and returns the ids list
		// returns in order.
		run func(t *testing.T, f *sessionsFixture) []vte.ID
	}{{
		desc: "no terminal",
		run:  func(*testing.T, *sessionsFixture) []vte.ID { return nil },
	}, {
		desc: "terminals in the order they started",
		run: func(t *testing.T, f *sessionsFixture) []vte.ID {
			a, _ := f.open(t)
			b, _ := f.open(t, "make")
			return []vte.ID{a.ID(), b.ID()}
		},
	}, {
		desc: "detached terminals first, each in the order they started",
		run: func(t *testing.T, f *sessionsFixture) []vte.ID {
			a, _ := f.open(t)
			b, _ := f.open(t)
			c, _ := f.open(t)
			f.detach(t, c)
			f.detach(t, b)
			return []vte.ID{b.ID(), c.ID(), a.ID()}
		},
	}, {
		desc: "a terminal that ended is not listed",
		run: func(t *testing.T, f *sessionsFixture) []vte.ID {
			a, p := f.open(t, "make")
			b, _ := f.open(t)
			f.end(t, a.ID(), p)
			return []vte.ID{b.ID()}
		},
	}, {
		desc:     "a pooled shell is not listed until a view takes it",
		capacity: 1,
		run: func(t *testing.T, f *sessionsFixture) []vte.ID {
			require.Len(t, f.pooled(t), 1)
			require.Empty(t, f.s.list())
			av := f.s.open(nil)
			f.settle()
			require.NotNil(t, av.real)
			return []vte.ID{av.ID()}
		},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, tc.capacity)
			want := tc.run(t, f)
			assert.Equal(t, want, terminalIDs(f.s.list()))
		})
	}
}

func TestTerminalSessionsLookup(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc     string
		capacity int
		// run returns the args to look up and the terminal they must
		// name, none when they must name none.
		run     func(t *testing.T, f *sessionsFixture) ([]string, vte.ID)
		wantErr error
	}{{
		desc: "a prefix of a terminal's id",
		run: func(t *testing.T, f *sessionsFixture) ([]string, vte.ID) {
			av, _ := f.open(t)
			_, _ = f.open(t)
			return []string{string(av.ID())[:terminalIDLength]}, av.ID()
		},
	}, {
		desc: "a detached terminal",
		run: func(t *testing.T, f *sessionsFixture) ([]string, vte.ID) {
			av, _ := f.open(t)
			f.detach(t, av)
			return []string{string(av.ID())}, av.ID()
		},
	}, {
		desc: "a terminal that ended is not found",
		run: func(t *testing.T, f *sessionsFixture) ([]string, vte.ID) {
			av, p := f.open(t, "make")
			f.end(t, av.ID(), p)
			return []string{string(av.ID())}, ""
		},
		wantErr: vte.ErrNotFound,
	}, {
		desc:     "a pooled shell is not found",
		capacity: 1,
		run: func(t *testing.T, f *sessionsFixture) ([]string, vte.ID) {
			pooled := f.pooled(t)
			require.Len(t, pooled, 1)
			return []string{string(pooled[0])}, ""
		},
		wantErr: vte.ErrNotFound,
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, tc.capacity)
			args, want := tc.run(t, f)
			info, err := f.s.lookup(args)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, want, info.ID)
		})
	}
}

func TestMatchTerminal(t *testing.T) {
	t.Parallel()
	infos := []vte.Info{{ID: "abc123"}, {ID: "abd456"}, {ID: "ff0000"}}
	cases := []struct {
		desc    string
		args    []string
		want    vte.ID
		wantErr string
	}{
		{desc: "no args", wantErr: "expected terminal id"},
		{desc: "blank args", args: []string{" ", ""}, wantErr: "expected terminal id"},
		{desc: "a unique prefix", args: []string{"ff"}, want: "ff0000"},
		{desc: "a whole id", args: []string{"abc123"}, want: "abc123"},
		{desc: "a prefix of an id only", args: []string{"abc1234"},
			wantErr: `terminal "abc1234": vte: terminal not found`},
		{desc: "an ambiguous prefix", args: []string{"ab"}, wantErr: `terminal id "ab" is ambiguous`},
		{desc: "no such terminal", args: []string{"zz"}, wantErr: `terminal "zz": vte: terminal not found`},
		{desc: "a candidate names its terminal", args: []string{"abc1 (detached, idle) make"}, want: "abc123"},
		{desc: "the first word of several args", args: []string{"abd", "(running)"}, want: "abd456"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			info, err := matchTerminal(infos, tc.args)
			if tc.wantErr != "" {
				require.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, info.ID)
		})
	}
}

func TestTerminalSessionsDetached(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		// run returns the uri to look up, and the terminal it must name,
		// none when it must name none.
		run func(t *testing.T, f *sessionsFixture) (workspaceapi.URI, vte.ID)
	}{{
		desc: "a detached terminal by its own URI",
		run: func(t *testing.T, f *sessionsFixture) (workspaceapi.URI, vte.ID) {
			av, _ := f.open(t)
			uri := av.real.URI()
			f.detach(t, av)
			return uri, av.ID()
		},
	}, {
		desc: "a terminal a view shows",
		run: func(t *testing.T, f *sessionsFixture) (workspaceapi.URI, vte.ID) {
			av, _ := f.open(t)
			return av.real.URI(), ""
		},
	}, {
		desc: "a view's URI",
		run: func(t *testing.T, f *sessionsFixture) (workspaceapi.URI, vte.ID) {
			av, _ := f.open(t)
			uri := av.URI()
			f.detach(t, av)
			return uri, ""
		},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, 0)
			uri, want := tc.run(t, f)
			info, ok := f.s.detached(uri)
			assert.Equal(t, want != "", ok)
			assert.Equal(t, want, info.ID)
		})
	}
}

func TestTerminalSessionsLiveness(t *testing.T) {
	t.Parallel()
	const job = 99
	cases := []struct {
		desc string
		// run returns the terminal to ask of.
		run  func(t *testing.T, f *sessionsFixture) vte.ID
		want vte.Liveness
	}{{
		desc: "a shell at its prompt is idle",
		run: func(t *testing.T, f *sessionsFixture) vte.ID {
			av, p := f.open(t)
			f.exe.fg.Store(int64(p.Pid))
			return av.ID()
		},
		want: vte.LivenessIdle,
	}, {
		desc: "a shell running a job is running",
		run: func(t *testing.T, f *sessionsFixture) vte.ID {
			av, _ := f.open(t)
			f.exe.fg.Store(job)
			return av.ID()
		},
		want: vte.LivenessRunning,
	}, {
		desc: "a detached shell running a job is running",
		run: func(t *testing.T, f *sessionsFixture) vte.ID {
			av, _ := f.open(t)
			f.exe.fg.Store(job)
			f.detach(t, av)
			return av.ID()
		},
		want: vte.LivenessRunning,
	}, {
		desc: "a command runs until it ends",
		run: func(t *testing.T, f *sessionsFixture) vte.ID {
			av, p := f.open(t, "make")
			f.exe.fg.Store(int64(p.Pid))
			return av.ID()
		},
		want: vte.LivenessRunning,
	}, {
		desc: "a command that ended is idle",
		run: func(t *testing.T, f *sessionsFixture) vte.ID {
			av, p := f.open(t, "make")
			f.end(t, av.ID(), p)
			return av.ID()
		},
		want: vte.LivenessIdle,
	}, {
		desc: "a terminal that is gone is unknown",
		run: func(t *testing.T, f *sessionsFixture) vte.ID {
			av, _ := f.open(t)
			require.NoError(t, f.s.remove(av.ID()))
			return av.ID()
		},
		want: vte.LivenessUnknown,
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, 0)
			id := tc.run(t, f)
			var got []vte.Liveness
			f.s.liveness(id, func(l vte.Liveness) { got = append(got, l) })
			assert.Empty(t, got, "it tells on the event loop")
			f.settle()
			assert.Equal(t, []vte.Liveness{tc.want}, got)
		})
	}
}

func TestTerminalSessionsRunningDetached(t *testing.T) {
	t.Parallel()
	const job = 99
	cases := []struct {
		desc string
		run  func(t *testing.T, f *sessionsFixture)
		want int
		// atOnce is whether it tells without asking any host.
		atOnce bool
	}{{
		desc:   "no terminal",
		run:    func(*testing.T, *sessionsFixture) {},
		atOnce: true,
	}, {
		desc: "a job a view shows",
		run: func(t *testing.T, f *sessionsFixture) {
			f.exe.fg.Store(job)
			_, _ = f.open(t)
		},
		atOnce: true,
	}, {
		desc: "a detached shell at its prompt",
		run: func(t *testing.T, f *sessionsFixture) {
			av, p := f.open(t)
			f.exe.fg.Store(int64(p.Pid))
			f.detach(t, av)
		},
	}, {
		desc: "a detached job",
		run: func(t *testing.T, f *sessionsFixture) {
			av, _ := f.open(t)
			f.exe.fg.Store(job)
			f.detach(t, av)
		},
		want: 1,
	}, {
		// The host tells every pty runs the first shell, so the others
		// run jobs.
		desc: "only the detached jobs",
		run: func(t *testing.T, f *sessionsFixture) {
			idle, p := f.open(t)
			running, _ := f.open(t)
			_, _ = f.open(t)
			f.exe.fg.Store(int64(p.Pid))
			f.detach(t, idle)
			f.detach(t, running)
		},
		want: 1,
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, 0)
			tc.run(t, f)
			n := -1
			f.s.runningDetached(func(running int) { n = running })
			if tc.atOnce {
				assert.Equal(t, tc.want, n, "it tells at once")
			} else {
				assert.Equal(t, -1, n, "it tells on the event loop")
			}
			f.settle()
			assert.Equal(t, tc.want, n)
		})
	}
}

func TestTerminalSessionsNeverWaitOnThePtyHost(t *testing.T) {
	t.Parallel()
	const job = 99
	f := newSessionsFixture(t, 0)
	av, _ := f.open(t)
	f.exe.fg.Store(job)
	f.detach(t, av)

	f.exe.hold.Lock()
	var (
		liveness []vte.Liveness
		running  []int
	)
	asked := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(asked)
		f.s.liveness(av.ID(), func(l vte.Liveness) { liveness = append(liveness, l) })
		f.s.runningDetached(func(n int) { running = append(running, n) })
		_, _ = f.s.lookup([]string{string(av.ID())})
		_ = f.s.list()
	})
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("asking what a terminal runs waited on the host of its pty")
	}
	f.loop.Flush(nil)
	assert.Empty(t, liveness, "nothing is told before the host answers")
	assert.Empty(t, running, "nothing is told before the host answers")

	f.exe.hold.Unlock()
	f.settle()
	assert.Equal(t, []vte.Liveness{vte.LivenessRunning}, liveness)
	assert.Equal(t, []int{1}, running)
}

func TestTerminalSessionsCandidates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		// run returns the prefixes of the candidates, in order.
		run func(t *testing.T, f *sessionsFixture) []string
	}{{
		desc: "no terminal",
		run:  func(*testing.T, *sessionsFixture) []string { return nil },
	}, {
		desc: "detached terminals first, telling what they run",
		run: func(t *testing.T, f *sessionsFixture) []string {
			shown, _ := f.open(t)
			idle, p := f.open(t)
			f.exe.fg.Store(int64(p.Pid))
			f.detach(t, idle)
			return []string{
				string(idle.ID())[:terminalIDLength] + " (detached, idle) ",
				string(shown.ID())[:terminalIDLength] + " (1 tab, running) ",
			}
		},
	}, {
		desc: "a terminal that ended is not offered",
		run: func(t *testing.T, f *sessionsFixture) []string {
			ended, p := f.open(t, "make")
			f.end(t, ended.ID(), p)
			av, _ := f.open(t, "top")
			return []string{string(av.ID())[:terminalIDLength] + " (1 tab, running) "}
		},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, 0)
			want := tc.run(t, f)
			ctx := context.Background()
			it := f.s.candidates(ctx)
			var got []string
			for {
				c, ok := it.Next(ctx)
				if !ok {
					break
				}
				got = append(got, command.UnquoteToken(c))
			}
			require.NoError(t, it.Err())
			require.Len(t, got, len(want))
			for i := range want {
				assert.True(t, strings.HasPrefix(got[i], want[i]), "%q names %q", got[i], want[i])
				info, err := f.s.lookup([]string{got[i]})
				require.NoError(t, err, "a candidate names its terminal")
				assert.True(t, strings.HasPrefix(want[i], string(info.ID)[:terminalIDLength]))
			}
		})
	}
}

func TestTerminalSessionsRemove(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		// run returns the terminal to remove, and the views that must end
		// with it.
		run func(t *testing.T, f *sessionsFixture) (vte.ID, []*asyncVTE)
	}{{
		desc: "a terminal ends with every view",
		run: func(t *testing.T, f *sessionsFixture) (vte.ID, []*asyncVTE) {
			av, _ := f.open(t)
			other := f.s.attach(av.ID())
			f.settle()
			require.Equal(t, 2, f.attachments(av.ID()))
			return av.ID(), []*asyncVTE{av, other}
		},
	}, {
		desc: "a detached terminal",
		run: func(t *testing.T, f *sessionsFixture) (vte.ID, []*asyncVTE) {
			av, _ := f.open(t)
			f.detach(t, av)
			return av.ID(), nil
		},
	}, {
		desc: "a terminal that is gone already",
		run: func(t *testing.T, f *sessionsFixture) (vte.ID, []*asyncVTE) {
			av, _ := f.open(t)
			require.NoError(t, f.s.remove(av.ID()))
			return av.ID(), nil
		},
	}, {
		desc: "no such terminal",
		run: func(*testing.T, *sessionsFixture) (vte.ID, []*asyncVTE) {
			return "nope", nil
		},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, 0)
			id, views := tc.run(t, f)
			require.NoError(t, f.s.remove(id))
			f.settle()
			assert.Equal(t, -1, f.attachments(id), "the terminal is gone")
			for _, v := range views {
				assert.True(t, v.IsComplete(), "its views end with it")
			}
		})
	}
}

func TestTerminalSessionsEnd(t *testing.T) {
	t.Parallel()
	t.Run("a view ends its terminal", func(t *testing.T) {
		t.Parallel()
		f := newSessionsFixture(t, 0)
		av, _ := f.open(t)
		require.NoError(t, f.s.end(av.real))
		assert.Equal(t, -1, f.attachments(av.ID()))
	})
	t.Run("a view of no terminal it keeps is only closed", func(t *testing.T) {
		t.Parallel()
		f := newSessionsFixture(t, 0)
		kept, _ := f.open(t)
		tv := newTestVte()
		require.NoError(t, f.s.end(tv))
		assert.True(t, tv.calledClose)
		assert.Equal(t, 1, f.attachments(kept.ID()))
	})
}

func TestTerminalSessionsPreviewOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		run  func(t *testing.T, f *sessionsFixture, a, b *asyncVTE, pa *workspacetest.Process)
	}{{
		desc: "every view is one of its own that leaves the terminal detached",
		run: func(t *testing.T, f *sessionsFixture, a, _ *asyncVTE, _ *workspacetest.Process) {
			first, err := f.s.previewOf(a.ID())
			require.NoError(t, err)
			again, err := f.s.previewOf(a.ID())
			require.NoError(t, err)
			assert.NotSame(t, first, again)
			assert.Equal(t, 0, f.attachments(a.ID()), "it leaves the terminal detached")
			require.NoError(t, first.Close())
			assert.False(t, again.IsComplete(), "closing one view leaves the other")
			require.NoError(t, again.Close())
			assert.Equal(t, 0, f.attachments(a.ID()), "closing them leaves the terminal")
		},
	}, {
		desc: "a terminal that is gone has no view",
		run: func(t *testing.T, f *sessionsFixture, a, _ *asyncVTE, pa *workspacetest.Process) {
			v, err := f.s.previewOf(a.ID())
			require.NoError(t, err)
			f.end(t, a.ID(), pa)
			_, err = f.s.previewOf(a.ID())
			require.ErrorIs(t, err, vte.ErrNotFound)
			assert.True(t, v.IsComplete(), "the view of the ended terminal is done")
		},
	}, {
		desc: "no such terminal",
		run: func(t *testing.T, f *sessionsFixture, _, _ *asyncVTE, _ *workspacetest.Process) {
			_, err := f.s.previewOf("nope")
			require.ErrorIs(t, err, vte.ErrNotFound)
		},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, 0)
			a, pa := f.open(t, "make")
			b, _ := f.open(t, "top")
			f.detach(t, a)
			f.detach(t, b)
			tc.run(t, f, a, b, pa)
		})
	}
}

func TestTerminalSessionsDetachedSnapshots(t *testing.T) {
	t.Parallel()
	f := newSessionsFixture(t, 0)
	shown, ps := f.open(t)
	detached, pd := f.open(t)
	ps.Print("shown")
	pd.Print("make: building")
	f.detach(t, detached)

	var snaps []vte.Snapshot
	require.Eventually(t, func() bool {
		f.settle()
		snaps = f.s.detachedSnapshots()
		return len(snaps) == 1 &&
			strings.Contains(term.CellsToString(snaps[0].ActiveCells()), "make: building")
	}, 5*time.Second, 5*time.Millisecond, "only the detached terminal is snapshot")
	info, ok := f.s.info(detached.ID())
	require.True(t, ok)
	assert.Equal(t, info.Title, snaps[0].Title)
	assert.Equal(t, 0, info.Attachments, "snapshotting leaves it detached")
	assert.Equal(t, 1, f.attachments(shown.ID()))
}

func TestTerminalSessionsOpen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc       string
		capacity   int
		cmdAndArgs []string
		wantPooled bool
	}{
		{desc: "the shell starts anew without a reservoir"},
		{desc: "the reservoir serves the shell", capacity: 1, wantPooled: true},
		{desc: "a command bypasses the reservoir", capacity: 1, cmdAndArgs: []string{"make"}},
		{desc: "a command starts anew", cmdAndArgs: []string{"make", "test"}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, tc.capacity)
			var pooled []vte.ID
			if tc.capacity != 0 {
				pooled = f.pooled(t)
			}
			av := f.s.open(tc.cmdAndArgs)
			assert.Equal(t, asyncVTELoadingTitle, av.Title(), "the view shows at once")
			f.settle()
			require.NotNil(t, av.real)
			assert.Equal(t, tc.wantPooled, av.pool != nil)
			assert.Equal(t, tc.wantPooled, slices.Contains(pooled, av.ID()))
			info, ok := f.s.info(av.ID())
			require.True(t, ok)
			assert.Equal(t, 1, info.Attachments)
			assert.Equal(t, tc.cmdAndArgs, info.CommandAndArgs)
			assert.Equal(t, []vte.ID{av.ID()}, terminalIDs(f.s.list()))
		})
	}
}

func TestTerminalSessionsAttach(t *testing.T) {
	t.Parallel()
	t.Run("another view of a terminal", func(t *testing.T) {
		t.Parallel()
		f := newSessionsFixture(t, 0)
		av, p := f.open(t)
		other := f.s.attach(av.ID())
		f.settle()
		require.NotNil(t, other.real)
		assert.True(t, other.existing, "abandoning it must not end the terminal")
		assert.Equal(t, av.ID(), other.ID())
		assert.NotEqual(t, av.URI(), other.URI(), "each view has a tab of its own")
		assert.Equal(t, 2, f.attachments(av.ID()))
		select {
		case extra := <-f.exe.Started():
			t.Fatalf("attaching started process %d besides %d", extra.Pid, p.Pid)
		default:
		}
	})
	t.Run("a terminal that is gone", func(t *testing.T) {
		t.Parallel()
		f := newSessionsFixture(t, 0)
		other := f.s.attach("nope")
		f.settle()
		assert.Nil(t, other.real)
		assert.True(t, other.IsComplete(), "the view fails")
	})
}

func TestTerminalSessionsCloseView(t *testing.T) {
	t.Parallel()
	const job = 99
	type views struct {
		first, second *asyncVTE
		proc          *workspacetest.Process
	}
	cases := []struct {
		desc string
		// second is whether another view shows the terminal.
		second bool
		run    func(t *testing.T, f *sessionsFixture, v views)
		// wantAttachments is how many views the terminal has left, -1
		// when it ended.
		wantAttachments int
		// wantReports is what the terminal reports under its own URI.
		wantReports []programstatus.State
		// wantRunning is how many detached terminals run something.
		wantRunning int
	}{{
		desc: "an idle shell ends",
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(int64(v.proc.Pid))
			require.NoError(t, v.first.Close())
		},
		wantAttachments: -1,
	}, {
		desc: "a job is detached and reported",
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(job)
			require.NoError(t, v.first.Close())
		},
		wantReports: []programstatus.State{programstatus.Working},
		wantRunning: 1,
	}, {
		desc: "closing waits on no host to tell what runs",
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(job)
			f.exe.hold.Lock()
			answer := sync.OnceFunc(f.exe.hold.Unlock)
			t.Cleanup(answer)
			closed := make(chan error, 1)
			go debug.CapturePanicReport(func() { closed <- v.first.Close() })
			select {
			case err := <-closed:
				require.NoError(t, err)
			case <-time.After(5 * time.Second):
				t.Fatal("closing a view waited on the host of its pty")
			}
			assert.Equal(t, 1, f.attachments(v.first.ID()), "the view stays until the host tells")
			answer()
		},
		wantReports: []programstatus.State{programstatus.Working},
		wantRunning: 1,
	}, {
		desc:   "a terminal another view shows is detached",
		second: true,
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(int64(v.proc.Pid))
			require.NoError(t, v.first.Close())
		},
		wantAttachments: 1,
	}, {
		desc: "a view attached while the host tells keeps the terminal",
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(int64(v.proc.Pid))
			f.exe.hold.Lock()
			answer := sync.OnceFunc(f.exe.hold.Unlock)
			t.Cleanup(answer)
			require.NoError(t, v.first.Close())
			second := f.s.attach(v.first.ID())
			require.Eventually(t, func() bool {
				f.loop.Flush(nil)
				return second.real != nil
			}, 5*time.Second, 5*time.Millisecond)
			answer()
		},
		wantAttachments: 1,
	}, {
		desc: "the view of a terminal that is gone ends",
		run: func(t *testing.T, f *sessionsFixture, v views) {
			require.NoError(t, f.s.remove(v.first.ID()))
			require.NoError(t, v.first.Close())
		},
		wantAttachments: -1,
	}, {
		desc: "a detached idle shell stays",
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(int64(v.proc.Pid))
			v.first.closeMode = vteCloseDetach
			require.NoError(t, v.first.Close())
		},
	}, {
		desc:   "a deleted job ends with every view",
		second: true,
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(job)
			v.first.closeMode = vteCloseDelete
			require.NoError(t, v.first.Close())
			assert.True(t, v.second.IsComplete())
		},
		wantAttachments: -1,
	}, {
		desc: "a job ends with the workspace",
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(job)
			assert.False(t, f.s.closing())
			f.cancel()
			assert.True(t, f.s.closing())
			require.NoError(t, v.first.Close())
		},
		wantAttachments: -1,
	}, {
		desc: "a job detached by hand stays while the workspace closes",
		run: func(t *testing.T, f *sessionsFixture, v views) {
			f.exe.fg.Store(job)
			f.cancel()
			v.first.closeMode = vteCloseDetach
			require.NoError(t, v.first.Close())
		},
		wantReports: []programstatus.State{programstatus.Working},
		wantRunning: 1,
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, 0)
			first, proc := f.open(t)
			v := views{first: first, proc: proc}
			if tc.second {
				v.second = f.s.attach(first.ID())
				f.settle()
				require.NotNil(t, v.second.real)
			}
			uri := first.real.URI()

			tc.run(t, f, v)
			f.settle()

			n := -1
			f.s.runningDetached(func(running int) { n = running })
			f.settle()
			assert.Equal(t, tc.wantRunning, n)
			assert.Equal(t, tc.wantAttachments, f.attachments(first.ID()))
			states := func() []programstatus.State {
				var ret []programstatus.State
				for _, r := range f.s.tabs.pending[uri.String()] {
					ret = append(ret, r.State)
				}
				return ret
			}
			// The registry watches a detached terminal off the event loop.
			require.Eventually(t, func() bool {
				f.settle()
				return slices.Equal(tc.wantReports, states())
			}, 5*time.Second, 5*time.Millisecond)
		})
	}
}

func TestTerminalSessionsResetReservoir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc     string
		capacity int
	}{
		{desc: "without a reservoir"},
		{desc: "with a reservoir", capacity: 2},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newSessionsFixture(t, tc.capacity)
			if tc.capacity == 0 {
				f.s.resetReservoir()
				assert.Nil(t, f.s.reservoir)
				return
			}
			first, old := f.s.reservoir, f.pooled(t)
			require.Len(t, old, tc.capacity)
			f.s.resetReservoir()
			require.NotNil(t, f.s.reservoir)
			assert.NotSame(t, first, f.s.reservoir)
			assert.Equal(t, tc.capacity, f.s.reservoir.InitialCapacity())
			for _, id := range old {
				assert.Equal(t, -1, f.attachments(id), "the old shells end")
			}
			fresh := f.pooled(t)
			assert.Len(t, fresh, tc.capacity, "new shells warm")
			for _, id := range old {
				assert.NotContains(t, fresh, id)
			}
		})
	}
}

func TestTerminalSessionsClose(t *testing.T) {
	t.Parallel()
	f := newSessionsFixture(t, 1)
	shown, _ := f.open(t, "make")
	detached, _ := f.open(t, "top")
	f.detach(t, detached)
	_, err := f.s.previewOf(detached.ID())
	require.NoError(t, err)

	require.NoError(t, f.s.close())
	assert.Equal(t, -1, f.attachments(shown.ID()))
	assert.Equal(t, -1, f.attachments(detached.ID()))
	assert.True(t, shown.IsComplete())
	av := f.s.open(nil)
	f.settle()
	assert.True(t, av.IsComplete(), "no terminal starts once closed")
}

func TestArgsMatchEmulatorShell(t *testing.T) {
	t.Parallel()
	shell := []string{"zsh", "-l"}
	cases := []struct {
		desc       string
		cmdAndArgs []string
		want       bool
	}{
		{desc: "no command runs the shell", want: true},
		{desc: "the shell", cmdAndArgs: []string{"zsh", "-l"}, want: true},
		{desc: "the shell with other args", cmdAndArgs: []string{"zsh"}},
		{desc: "another program", cmdAndArgs: []string{"make"}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, argsMatchEmulatorShell(tc.cmdAndArgs, shell))
		})
	}
}

func TestTerminalCandidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		info vte.Info
		l    vte.Liveness
		want string
	}{
		{desc: "a long id is cut",
			info: vte.Info{ID: "0123456789abcdef", Title: "vim", Attachments: 1},
			l:    vte.LivenessRunning, want: "01234567 (1 tab, running) vim"},
		{desc: "a short id is whole, and every tab showing it is counted",
			info: vte.Info{ID: "abc", Title: "vim", Attachments: 2},
			l:    vte.LivenessIdle, want: "abc (2 tabs, idle) vim"},
		{desc: "a detached terminal",
			info: vte.Info{ID: "0123456789", Title: "make"},
			l:    vte.LivenessRunning, want: "01234567 (detached, running) make"},
		{desc: "an untitled terminal",
			info: vte.Info{ID: "0123456789", Attachments: 1},
			l:    vte.LivenessUnknown, want: "01234567 (1 tab, unknown) terminal"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			got := terminalCandidate(tc.info, tc.l)
			assert.Equal(t, tc.want, command.UnquoteToken(got))
			assert.Equal(t, command.ShellQuote(tc.want), got, "it is one word")
		})
	}
}

func TestTerminalCloseWarning(t *testing.T) {
	t.Parallel()
	cases := []struct {
		desc string
		info vte.Info
		own  int
		l    vte.Liveness
		want string
	}{
		{desc: "an idle terminal of the own tab only",
			info: vte.Info{Title: "zsh", Attachments: 1}, own: 1, l: vte.LivenessIdle},
		{desc: "an idle detached terminal",
			info: vte.Info{Title: "zsh"}, l: vte.LivenessIdle},
		{desc: "a terminal that may run something",
			info: vte.Info{Title: "zsh", Attachments: 1}, own: 1, l: vte.LivenessUnknown},
		{desc: "a program",
			info: vte.Info{Title: "make", Attachments: 1}, own: 1, l: vte.LivenessRunning,
			want: "Terminal 'make' is running a program. Close it?"},
		{desc: "another tab",
			info: vte.Info{Title: "zsh", Attachments: 2}, own: 1, l: vte.LivenessIdle,
			want: "Terminal 'zsh' is shown in another tab. Close it?"},
		{desc: "a tab of a detached terminal",
			info: vte.Info{Title: "zsh", Attachments: 1}, l: vte.LivenessIdle,
			want: "Terminal 'zsh' is shown in another tab. Close it?"},
		{desc: "other tabs",
			info: vte.Info{Title: "zsh", Attachments: 4}, own: 1, l: vte.LivenessIdle,
			want: "Terminal 'zsh' is shown in 3 other tabs. Close it?"},
		{desc: "a program and another tab",
			info: vte.Info{Title: "make", Attachments: 2}, own: 1, l: vte.LivenessRunning,
			want: "Terminal 'make' is running a program and is shown in another tab. Close it?"},
		{desc: "an untitled terminal",
			info: vte.Info{}, l: vte.LivenessRunning,
			want: "Terminal 'terminal' is running a program. Close it?"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, terminalCloseWarning(tc.info, tc.own, tc.l))
		})
	}
}

func TestFailedTerminalKeeper(t *testing.T) {
	t.Parallel()
	view := mustURI(t, "terminal://loading/1")
	tab := mustURI(t, "terminalsession:///build")
	cases := []struct {
		desc      string
		kept      bool
		wantExits []string
	}{
		{desc: "a failed terminal's tab is kept", kept: true},
		{desc: "any other is dropped", wantExits: []string{tab.String()}},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			rec := &recordingTabManager{names: map[string]string{}}
			aliases := newTabNameAliaser(rec, func(fn func()) bool { fn(); return true })
			aliases.addAlias(view, tab)
			var asked []string
			k := failedTerminalKeeper{tabNameAliaser: aliases, keep: func(uri workspaceapi.URI) bool {
				asked = append(asked, uri.String())
				return tc.kept
			}}
			assert.True(t, k.OnTabExit(view))
			assert.Equal(t, []string{tab.String()}, asked, "it asks of the tab the view resolves to")
			assert.Equal(t, tc.wantExits, rec.exits)
		})
	}
}

// screenText renders what av shows, once it shows something.
func screenText(t *testing.T, av *asyncVTE) string {
	t.Helper()
	snap, err := av.Snapshot()
	require.NoError(t, err)
	var b strings.Builder
	for _, row := range snap.ActiveCells() {
		for _, c := range row {
			if c.Ch != 0 {
				b.WriteRune(c.Ch)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func TestTerminalSessionsRestore(t *testing.T) {
	t.Parallel()
	saved := vte.Snapshot{
		Schema: 1, Title: "saved", Width: 40, Height: 10,
		Primary: vte.ScreenSnapshot{Cells: term.StringToCells("from before")},
	}

	t.Run("attaches the terminal the session showed", func(t *testing.T) {
		t.Parallel()
		f := newSessionsFixtureWith(t, terminalSessionsConfig{Durable: true})
		before, p := f.open(t)
		p.Print("still here")
		require.Eventually(t, func() bool {
			return strings.Contains(screenText(t, before), "still here")
		}, 5*time.Second, 5*time.Millisecond)
		f.detach(t, before)

		av := f.s.restore(before.ID(), saved)
		assert.Equal(t, before.ID(), av.ID(), "the session's terminal is named while it loads")
		av.Resize(40, 10)
		f.settle()
		require.NotNil(t, av.real)
		assert.Equal(t, before.ID(), av.ID())
		assert.True(t, av.existing)
		assert.Equal(t, 1, f.attachments(before.ID()))
		text := screenText(t, av)
		assert.Contains(t, text, "still here", "the live terminal's screen is shown")
		assert.NotContains(t, text, "from before", "the snapshot does not replace it")
		assert.Equal(t, before.ID(), f.s.durableID(av), "a hosted terminal is recorded by id")
	})

	t.Run("replays the snapshot into a new terminal when it is gone", func(t *testing.T) {
		t.Parallel()
		f := newSessionsFixtureWith(t, terminalSessionsConfig{Durable: true})
		av := f.s.restore("gone", saved)
		av.Resize(40, 10)
		f.settle()
		require.NotNil(t, av.real)
		select {
		case <-f.exe.Started():
		case <-time.After(5 * time.Second):
			t.Fatal("no terminal started")
		}
		assert.NotEqual(t, vte.ID("gone"), av.ID(), "a new terminal")
		assert.False(t, av.existing)
		assert.Contains(t, screenText(t, av), "from before")
	})

	t.Run("a terminal that ends with Rune is not recorded by id", func(t *testing.T) {
		t.Parallel()
		f := newSessionsFixture(t, 0)
		av, _ := f.open(t)
		assert.Empty(t, f.s.durableID(av))
		assert.False(t, f.s.durable())
	})
}

func TestTerminalSessionsHostedOutliveTheWorkspace(t *testing.T) {
	t.Parallel()
	const job = 99
	f := newSessionsFixtureWith(t, terminalSessionsConfig{Durable: true})
	shown, _ := f.open(t)
	detached, _ := f.open(t)
	f.exe.fg.Store(job)
	f.detach(t, detached)

	n := -1
	f.s.runningDetached(func(running int) { n = running })
	assert.Equal(t, 0, n, "exiting ends no terminal of another machine")

	f.cancel()
	require.NoError(t, shown.Close())
	f.settle()
	assert.Equal(t, 0, f.attachments(shown.ID()), "the workspace closing leaves the terminal")
	assert.Equal(t, 0, f.attachments(detached.ID()))
}
