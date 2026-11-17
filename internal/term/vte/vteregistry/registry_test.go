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

package vteregistry

import (
	"bytes"
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
	"unstable.build/rune/internal/term/programstatus"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/workspace/workspacetest"
)

// fgTerminal is a PtyExecutor whose ptys run fg in the foreground, or
// the shell when fg is zero. Its host answers only while hold is not
// locked.
type fgTerminal struct {
	*workspacetest.PtyExecutor
	fg   atomic.Int64
	hold sync.RWMutex
	// input is what was typed into its ptys.
	mu    sync.Mutex
	input bytes.Buffer
}

var errNoForeground = errors.New("no foreground")

func (t *fgTerminal) ForegroundProcessGroup(workspaceapi.Pty) (int, error) {
	t.hold.RLock()
	defer t.hold.RUnlock()
	if fg := t.fg.Load(); fg != 0 {
		return int(fg), nil
	}
	return 0, errNoForeground
}

type recordingTabs struct {
	mu      sync.Mutex
	reports []programstatus.Report
	exits   int
}

func (*recordingTabs) Tab(
	workspaceapi.URI, rune, string, browserapi.Handler,
) (browserapi.Handler, error) {
	return nil, errors.ErrUnsupported
}

func (*recordingTabs) SetTabName(workspaceapi.URI, string, term.Attributes) error {
	return nil
}

func (r *recordingTabs) OnTabExit(workspaceapi.URI) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.exits++
	return true
}

func (r *recordingTabs) SetTabStatus(_ workspaceapi.URI, report programstatus.Report) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reports = append(r.reports, report)
	return nil
}

func (*recordingTabs) EndTabStatus(workspaceapi.URI) error { return nil }

type nopHost struct{}

func (nopHost) PublishEvent(term.Event) error { return nil }

func (nopHost) Notify(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopHost) NotifyOnce(browserapi.NotificationLevel, string, ...any) (string, error) {
	return "", nil
}

func (nopHost) UpdateNotificationProgress(string, string, int64, int64) error { return nil }

type fixture struct {
	r       *Registry
	term    *fgTerminal
	tabs    *recordingTabs
	deleted chan workspaceapi.URI

	mu      sync.Mutex
	changes []vte.Change
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{
		term:    &fgTerminal{PtyExecutor: workspacetest.NewPtyExecutor()},
		tabs:    &recordingTabs{},
		deleted: make(chan workspaceapi.URI, 8),
	}
	cfg := vte.DefaultConfig()
	f.r = New(nopHost{}, nopHost{}, f.term, f.term, f.tabs, Config{
		VTE:          cfg,
		PollInterval: 10 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	f.r.Watch(ctx, func(change vte.Change) {
		f.mu.Lock()
		f.changes = append(f.changes, change)
		f.mu.Unlock()
		if change.Deleted {
			f.deleted <- change.URI
		}
	})
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = f.r.Close() })
	return f
}

// told returns what the registry told of the detached terminals, in
// order: the changes that carry what one runs.
func (f *fixture) told() []vte.Liveness {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ret []vte.Liveness
	for _, change := range f.changes {
		if change.Liveness != vte.LivenessUnknown {
			ret = append(ret, change.Liveness)
		}
	}
	return ret
}

// last returns the last change told.
func (f *fixture) last() vte.Change {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changes[len(f.changes)-1]
}

// create returns a new terminal's first attachment and its process.
func (f *fixture) create(t *testing.T, width, height int) (*attachment, *workspacetest.Process) {
	t.Helper()
	v, err := f.r.CreateVTE(context.Background(), vte.CreateOptions{
		Width: width, Height: height,
	})
	require.NoError(t, err)
	select {
	case p := <-f.term.Started():
		return v.(*attachment), p
	case <-time.After(5 * time.Second):
		t.Fatal("the terminal's process never started")
	}
	return nil, nil
}

func (f *fixture) attach(t *testing.T, id vte.ID) *attachment {
	t.Helper()
	v, err := f.r.AttachVTE(context.Background(), id)
	require.NoError(t, err)
	return v.(*attachment)
}

func (f *fixture) info(t *testing.T, id vte.ID) vte.Info {
	t.Helper()
	infos, err := f.r.ListVTEs(context.Background())
	require.NoError(t, err)
	for _, info := range infos {
		if info.ID == id {
			return info
		}
	}
	t.Fatalf("terminal %s is not listed", id)
	return vte.Info{}
}

func (f *fixture) liveness(t *testing.T, id vte.ID) vte.Liveness {
	t.Helper()
	l, err := f.r.VTELiveness(context.Background(), id)
	require.NoError(t, err)
	return l
}

// working waits for the detached terminal to be told running.
func (f *fixture) working(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool {
		return slices.Equal(f.told(), []vte.Liveness{vte.LivenessRunning})
	}, 5*time.Second, 5*time.Millisecond)
}

func TestRegistryAttachments(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	ctx := context.Background()
	first, _ := f.create(t, 10, 4)
	second, _ := f.create(t, 10, 4)
	id := first.ID()

	infos, err := f.r.ListVTEs(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 2)
	assert.Equal(t, []vte.ID{id, second.ID()},
		[]vte.ID{infos[0].ID, infos[1].ID}, "listed in creation order")

	other := f.attach(t, id)
	assert.Equal(t, id, other.ID())
	assert.Equal(t, first.URI(), other.URI(), "attachments share their terminal's uri")
	assert.Equal(t, 2, f.info(t, id).Attachments)

	require.NoError(t, first.Close())
	require.NoError(t, first.Close(), "closing twice detaches once")
	assert.Equal(t, 1, f.info(t, id).Attachments)
	require.NoError(t, other.Close())
	assert.Equal(t, 0, f.info(t, id).Attachments, "a detached terminal stays")

	again := f.attach(t, id)
	assert.Equal(t, 1, f.info(t, id).Attachments)

	require.NoError(t, f.r.DeleteVTE(ctx, id))
	assert.True(t, again.IsComplete(), "a deleted terminal's attachments are dead")
	exit, _ := again.Handle(term.Event{Type: term.EventKey, Ch: 'a'})
	assert.True(t, exit, "a deleted terminal's attachments ask to be dropped")
	assert.Equal(t, first.URI(), <-f.deleted)
	_, err = f.r.AttachVTE(ctx, id)
	require.ErrorIs(t, err, vte.ErrNotFound)
	require.ErrorIs(t, f.r.DeleteVTE(ctx, id), vte.ErrNotFound)

	require.NoError(t, f.r.Close())
	assert.True(t, second.IsComplete(), "closing the registry terminates every terminal")
	_, err = f.r.CreateVTE(ctx, vte.CreateOptions{})
	require.ErrorIs(t, err, ErrClosed)
}

func TestRegistryResizeOwner(t *testing.T) {
	t.Parallel()
	type size struct{ w, h int }
	type step struct {
		desc string
		do   func(views []*attachment)
		want size
	}
	steps := []step{
		{desc: "the sole attachment owns the size",
			do: func(v []*attachment) { v[0].Resize(80, 24) }, want: size{80, 24}},
		{desc: "a second attachment does not",
			do: func(v []*attachment) { v[1].Resize(100, 30) }, want: size{80, 24}},
		{desc: "focusing it takes the size over",
			do: func(v []*attachment) { v[1].OnFocusChange(true) }, want: size{100, 30}},
		{desc: "the first no longer resizes the pty",
			do: func(v []*attachment) { v[0].Resize(70, 20) }, want: size{100, 30}},
		{desc: "losing focus keeps the size",
			do: func(v []*attachment) { v[1].OnFocusChange(false) }, want: size{100, 30}},
		{desc: "the owner resizes it",
			do: func(v []*attachment) { v[1].Resize(90, 28) }, want: size{90, 28}},
		{desc: "the most recently focused survivor inherits it",
			do: func(v []*attachment) {
				v[2].Resize(60, 10)
				v[2].OnFocusChange(true)
				v[0].OnFocusChange(true)
				_ = v[0].Close()
			}, want: size{60, 10}},
		{desc: "and the last one standing",
			do: func(v []*attachment) { _ = v[2].Close() }, want: size{90, 28}},
	}
	f := newFixture(t)
	first, _ := f.create(t, 0, 0)
	views := []*attachment{first, f.attach(t, first.ID()), f.attach(t, first.ID())}
	for _, s := range steps {
		s.do(views)
		info := f.info(t, first.ID())
		assert.Equal(t, s.want, size{info.Width, info.Height}, s.desc)
	}
}

func drawn(v *attachment, width, height int) string {
	w := term.NewStringWriter(width, height)
	v.Draw(w)
	w.Flush()
	return w.String()
}

func TestRegistryViewsCropTheGrid(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	owner, p := f.create(t, 0, 0)
	owner.Resize(6, 4)
	p.Print("one\r\ntwo\r\nthree\r\nfour56")
	require.Eventually(t, func() bool {
		return strings.Contains(drawn(owner, 6, 4), "four56")
	}, 5*time.Second, 5*time.Millisecond)

	cases := []struct {
		desc          string
		width, height int
		want          string
	}{
		{desc: "a smaller view cuts the top rows and the right columns",
			width: 4, height: 2, want: "thre\nfour"},
		{desc: "a larger view pads the grid",
			width: 8, height: 5, want: "one     \ntwo     \nthree   \nfour56  \n        "},
		{desc: "a view of the grid's size shows it whole",
			width: 6, height: 4, want: "one   \ntwo   \nthree \nfour56"},
	}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			v := f.attach(t, owner.ID())
			defer func() { _ = v.Close() }()
			v.Resize(tc.width, tc.height)
			assert.Equal(t, tc.want, drawn(v, tc.width, tc.height))
			assert.Equal(t, 4, f.info(t, owner.ID()).Height, "a view never resizes the pty")
		})
	}
}

func TestRegistryDetachedTerminal(t *testing.T) {
	t.Parallel()
	const job = 99
	cases := []struct {
		desc string
		run  func(t *testing.T, f *fixture, v *attachment, p *workspacetest.Process)
		// want is what the registry tells the detached terminal runs.
		want []vte.Liveness
	}{{
		desc: "an idle shell is detached silently",
		run: func(t *testing.T, f *fixture, v *attachment, p *workspacetest.Process) {
			f.term.fg.Store(int64(p.Pid))
			require.NoError(t, v.Close())
			require.Eventually(t, func() bool {
				v.c.mu.Lock()
				defer v.c.mu.Unlock()
				return v.c.stopWatch == nil
			}, 5*time.Second, 5*time.Millisecond, "its watcher finds it idle")
			assert.Equal(t, 0, f.info(t, v.ID()).Attachments)
			assert.Equal(t, vte.LivenessIdle, f.info(t, v.ID()).Liveness)
		},
		want: []vte.Liveness{vte.LivenessIdle},
	}, {
		desc: "a running job is told running, then idle once the shell is back",
		run: func(t *testing.T, f *fixture, v *attachment, p *workspacetest.Process) {
			assert.Equal(t, vte.LivenessUnknown, f.info(t, v.ID()).Liveness,
				"an attached terminal is not watched")
			f.term.fg.Store(job)
			require.NoError(t, v.Close())
			f.working(t)
			assert.Equal(t, vte.LivenessRunning, f.info(t, v.ID()).Liveness,
				"the listing carries what was told, for a watcher that missed it")
			f.term.fg.Store(int64(p.Pid))
			p.Print("$ ")
			require.Eventually(t, func() bool { return len(f.told()) == 2 },
				5*time.Second, 5*time.Millisecond)
			assert.Equal(t, vte.LivenessIdle, f.liveness(t, v.ID()),
				"the shell stays once its job is done")
			assert.Equal(t, vte.LivenessIdle, f.info(t, v.ID()).Liveness)
			assert.False(t, f.last().Complete)
		},
		want: []vte.Liveness{vte.LivenessRunning, vte.LivenessIdle},
	}, {
		desc: "a job's end is found without output",
		run: func(t *testing.T, f *fixture, v *attachment, p *workspacetest.Process) {
			f.term.fg.Store(job)
			require.NoError(t, v.Close())
			f.working(t)
			f.term.fg.Store(int64(p.Pid))
			require.Eventually(t, func() bool { return len(f.told()) == 2 },
				5*time.Second, 5*time.Millisecond)
		},
		want: []vte.Liveness{vte.LivenessRunning, vte.LivenessIdle},
	}, {
		desc: "attaching it again stops the watch",
		run: func(t *testing.T, f *fixture, v *attachment, p *workspacetest.Process) {
			f.term.fg.Store(job)
			require.NoError(t, v.Close())
			f.working(t)
			again := f.attach(t, v.ID())
			f.term.fg.Store(int64(p.Pid))
			time.Sleep(50 * time.Millisecond)
			assert.Equal(t, 1, f.info(t, again.ID()).Attachments)
			assert.Equal(t, vte.LivenessUnknown, f.info(t, again.ID()).Liveness,
				"attached again, it is no longer watched")
			assert.Equal(t, 1, f.last().Attachments, "the attachment is told")
		},
		want: []vte.Liveness{vte.LivenessRunning},
	}, {
		desc: "a shell that exits detached is deleted",
		run: func(t *testing.T, f *fixture, v *attachment, p *workspacetest.Process) {
			f.term.fg.Store(job)
			require.NoError(t, v.Close())
			f.working(t)
			p.HangUp()
			p.ReportExit(nil)
			select {
			case uri := <-f.deleted:
				assert.Equal(t, v.URI(), uri)
			case <-time.After(5 * time.Second):
				t.Fatal("the terminal was not deleted")
			}
			infos, err := f.r.ListVTEs(context.Background())
			require.NoError(t, err)
			assert.Empty(t, infos)
			assert.Zero(t, f.tabs.exits, "no tab shows it to drop")
			assert.True(t, f.last().Deleted, "its deletion is the last change told")
		},
		want: []vte.Liveness{vte.LivenessRunning},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			v, p := f.create(t, 10, 4)
			tc.run(t, f, v, p)
			assert.Equal(t, tc.want, f.told())
		})
	}
}

func TestRegistryAttachedTerminalExit(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	v, p := f.create(t, 10, 4)
	p.HangUp()
	p.ReportExit(nil)
	require.Eventually(t, func() bool {
		f.tabs.mu.Lock()
		defer f.tabs.mu.Unlock()
		return f.tabs.exits == 1
	}, 5*time.Second, 5*time.Millisecond, "the host drops the tabs that show it")
	assert.Equal(t, 1, f.info(t, v.ID()).Attachments, "their closing decides")
	assert.Equal(t, vte.LivenessIdle, f.liveness(t, v.ID()))
}

func TestRegistryNeverWaitsOnThePtyHost(t *testing.T) {
	t.Parallel()
	const job = 99
	ctx := context.Background()
	f := newFixture(t)
	v, _ := f.create(t, 10, 4)
	f.term.fg.Store(job)
	f.term.hold.Lock()
	answer := sync.OnceFunc(f.term.hold.Unlock)
	t.Cleanup(answer)

	returned := make(chan struct{})
	go debug.CapturePanicReport(func() {
		defer close(returned)
		_, _ = f.r.ListVTEs(ctx)
		_ = v.Close()
		_, _ = f.r.ListVTEs(ctx)
	})
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("listing or detaching a terminal waited on the host of its pty")
	}
	assert.Empty(t, f.told(), "what it runs is not known yet")

	answered := make(chan vte.Liveness, 1)
	go debug.CapturePanicReport(func() {
		l, _ := f.r.VTELiveness(ctx, v.ID())
		answered <- l
	})
	select {
	case <-answered:
		t.Fatal("VTELiveness answered without the host")
	case <-time.After(20 * time.Millisecond):
	}
	answer()
	assert.Equal(t, vte.LivenessRunning, <-answered)
	f.working(t)

	_, err := f.r.VTELiveness(ctx, "missing")
	require.ErrorIs(t, err, vte.ErrNotFound)
}

func TestRegistryPreview(t *testing.T) {
	t.Parallel()
	const job = 99
	f := newFixture(t)
	v, _ := f.create(t, 10, 4)
	f.term.fg.Store(job)
	require.NoError(t, v.Close())
	f.working(t)

	preview, err := f.r.PreviewVTE(context.Background(), v.ID())
	require.NoError(t, err)
	preview.Resize(30, 8)
	info := f.info(t, v.ID())
	assert.Equal(t, 0, info.Attachments, "a preview leaves the terminal detached")
	assert.Equal(t, v.URI(), info.URI)
	assert.Equal(t, []int{30, 8}, []int{info.Width, info.Height},
		"a preview owns the size of a detached terminal")
	assert.Equal(t, 0, f.last().Attachments, "a preview is no change to the terminal")

	again := f.attach(t, v.ID())
	again.Resize(20, 6)
	info = f.info(t, v.ID())
	assert.Equal(t, []int{20, 6}, []int{info.Width, info.Height},
		"an attachment takes the size over")
	preview.Resize(30, 8)
	assert.Equal(t, 6, f.info(t, v.ID()).Height)
	require.NoError(t, preview.Close())
	assert.Equal(t, 1, f.info(t, v.ID()).Attachments)
	assert.Equal(t, []vte.Liveness{vte.LivenessRunning}, f.told(),
		"only attaching stops the watch")
	assert.Equal(t, 1, f.last().Attachments, "only attaching is told")

	_, err = f.r.PreviewVTE(context.Background(), "missing")
	require.ErrorIs(t, err, vte.ErrNotFound)
}

func TestRegistryWatch(t *testing.T) {
	t.Parallel()
	type change struct {
		attachments int
		liveness    vte.Liveness
		complete    bool
		deleted     bool
	}
	brief := func(changes []vte.Change) []change {
		var ret []change
		for _, c := range changes {
			ret = append(ret, change{c.Attachments, c.Liveness, c.Complete, c.Deleted})
		}
		return ret
	}
	cases := []struct {
		desc string
		// run returns the terminal whose changes are compared.
		run  func(t *testing.T, f *fixture) vte.ID
		want []change
	}{{
		desc: "creating a terminal",
		run: func(t *testing.T, f *fixture) vte.ID {
			v, _ := f.create(t, 10, 4)
			return v.ID()
		},
		want: []change{{attachments: 1}},
	}, {
		desc: "each attachment and detachment",
		run: func(t *testing.T, f *fixture) vte.ID {
			v, p := f.create(t, 10, 4)
			f.term.fg.Store(int64(p.Pid))
			other := f.attach(t, v.ID())
			require.NoError(t, other.Close())
			require.NoError(t, v.Close())
			require.Eventually(t, func() bool { return len(f.told()) == 1 },
				5*time.Second, 5*time.Millisecond)
			return v.ID()
		},
		want: []change{{attachments: 1}, {attachments: 2}, {attachments: 1},
			{attachments: 0}, {attachments: 0, liveness: vte.LivenessIdle}},
	}, {
		desc: "a shown terminal whose process exits completes",
		run: func(t *testing.T, f *fixture) vte.ID {
			v, p := f.create(t, 10, 4)
			p.HangUp()
			p.ReportExit(nil)
			require.Eventually(t, func() bool { return f.last().Complete },
				5*time.Second, 5*time.Millisecond)
			return v.ID()
		},
		want: []change{{attachments: 1}, {attachments: 1, complete: true}},
	}, {
		desc: "deleting a terminal",
		run: func(t *testing.T, f *fixture) vte.ID {
			v, _ := f.create(t, 10, 4)
			require.NoError(t, f.r.DeleteVTE(context.Background(), v.ID()))
			return v.ID()
		},
		want: []change{{attachments: 1}, {deleted: true}},
	}, {
		desc: "closing the registry deletes every terminal",
		run: func(t *testing.T, f *fixture) vte.ID {
			v, _ := f.create(t, 10, 4)
			require.NoError(t, f.r.Close())
			return v.ID()
		},
		want: []change{{attachments: 1}, {deleted: true}},
	}}
	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			id := tc.run(t, f)
			f.mu.Lock()
			var got []vte.Change
			for _, c := range f.changes {
				if c.ID == id {
					got = append(got, c)
				}
			}
			f.mu.Unlock()
			assert.Equal(t, tc.want, brief(got))
		})
	}

	t.Run("a watch ends with its context", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		var n atomic.Int64
		f.r.Watch(ctx, func(vte.Change) { n.Add(1) })
		v, _ := f.create(t, 10, 4)
		assert.Equal(t, int64(1), n.Load())
		cancel()
		require.Eventually(t, func() bool {
			f.r.mu.Lock()
			defer f.r.mu.Unlock()
			return len(f.r.watchers) == 1
		}, 5*time.Second, 5*time.Millisecond, "the watcher is forgotten")
		require.NoError(t, f.r.DeleteVTE(context.Background(), v.ID()))
		assert.Equal(t, int64(1), n.Load())
	})
}
