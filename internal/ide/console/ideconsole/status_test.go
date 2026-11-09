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

package ideconsole

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
)

// recordingTabs is a TabManager that records every status it is told,
// by record id.
type recordingTabs struct {
	mu       sync.Mutex
	uris     []workspaceapi.URI
	statuses map[string][]browserapi.TabStatus
}

func (r *recordingTabs) SetTabStatus(uri workspaceapi.URI, status browserapi.TabStatus) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.statuses == nil {
		r.statuses = make(map[string][]browserapi.TabStatus)
	}
	r.uris = append(r.uris, uri)
	r.statuses[status.ID] = append(r.statuses[status.ID], status)
	return nil
}

func (r *recordingTabs) get() ([]workspaceapi.URI, map[string][]browserapi.TabStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	statuses := make(map[string][]browserapi.TabStatus, len(r.statuses))
	for id, s := range r.statuses {
		statuses[id] = append([]browserapi.TabStatus(nil), s...)
	}
	return append([]workspaceapi.URI(nil), r.uris...), statuses
}

func newStatusTestHandler(
	t *testing.T,
	fn func(context.Context, repl.Command, repl.ProgressWriter) (iterator.Iterator[component.Responsive], error),
) (*Handler, *recordingTabs, workspaceapi.URI) {
	t.Helper()
	uri, err := workspaceapi.ParseURI("console:///ws")
	require.NoError(t, err)
	tabs := &recordingTabs{}
	h, _ := New(
		func(func()) bool { return false },
		term.NopInterrupter(),
		stubEditor{},
		tabs,
		uri,
		Config{DisableShellInterpreter: &mockCmdHandler{handleFn: fn}},
	)
	t.Cleanup(func() { _ = h.Close() })
	return h, tabs, uri
}

func TestCommandStatus(t *testing.T) {
	boom := errors.New("boom")
	working := func(progress int) browserapi.TabStatus {
		return browserapi.TabStatus{
			ID: "1", State: browserapi.TabWorking, Progress: progress, Title: "build ./... -v",
		}
	}
	ended := func(state browserapi.TabState) browserapi.TabStatus {
		return browserapi.TabStatus{ID: "1", State: state, Progress: -1, Title: "build ./... -v"}
	}
	tests := []struct {
		name   string
		handle func(context.Context, repl.Command, repl.ProgressWriter) (iterator.Iterator[component.Responsive], error)
		want   []browserapi.TabStatus
	}{
		{
			name: "done",
			handle: func(context.Context, repl.Command, repl.ProgressWriter) (iterator.Iterator[component.Responsive], error) {
				return toLines("ok"), nil
			},
			want: []browserapi.TabStatus{working(-1), ended(browserapi.TabDone)},
		},
		{
			name: "progress is reported as it changes",
			handle: func(_ context.Context, _ repl.Command, pw repl.ProgressWriter) (iterator.Iterator[component.Responsive], error) {
				pw.Progress(0, 0, "B")
				pw.Progress(1, 4, "B")
				pw.Progress(1, 4, "B")
				pw.Progress(5, 4, "B")
				return toLines("ok"), nil
			},
			want: []browserapi.TabStatus{
				working(-1), working(25), working(100), ended(browserapi.TabDone),
			},
		},
		{
			name: "failing to start",
			handle: func(context.Context, repl.Command, repl.ProgressWriter) (iterator.Iterator[component.Responsive], error) {
				return nil, boom
			},
			want: []browserapi.TabStatus{working(-1), ended(browserapi.TabError)},
		},
		{
			name: "failing while running",
			handle: func(context.Context, repl.Command, repl.ProgressWriter) (iterator.Iterator[component.Responsive], error) {
				return iterator.Error[component.Responsive](boom), nil
			},
			want: []browserapi.TabStatus{working(-1), ended(browserapi.TabError)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, tabs, uri := newStatusTestHandler(t, tc.handle)
			h.Submit("build ./... -v")
			h.Wait()
			uris, statuses := tabs.get()
			assert.Equal(t, map[string][]browserapi.TabStatus{"1": tc.want}, statuses)
			for _, u := range uris {
				assert.Equal(t, uri, u)
			}
		})
	}
}

func TestCommandStatusInterrupted(t *testing.T) {
	started := make(chan struct{})
	h, tabs, _ := newStatusTestHandler(t, func(
		ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
	) (iterator.Iterator[component.Responsive], error) {
		if cmd.Name != "slow" {
			return toLines("ok"), nil
		}
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})

	h.Submit("slow")
	<-started
	h.Submit("fast")
	h.Wait()
	_, statuses := tabs.get()
	assert.Equal(t, map[string][]browserapi.TabStatus{
		"1": {
			{ID: "1", State: browserapi.TabWorking, Progress: -1, Title: "slow"},
			{ID: "1", State: browserapi.TabClear, Progress: -1, Title: "slow"},
		},
		"2": {
			{ID: "2", State: browserapi.TabWorking, Progress: -1, Title: "fast"},
			{ID: "2", State: browserapi.TabDone, Progress: -1, Title: "fast"},
		},
	}, statuses)
}

func TestCommandStatusStopsOnClose(t *testing.T) {
	started := make(chan struct{})
	h, tabs, _ := newStatusTestHandler(t, func(
		ctx context.Context, _ repl.Command, pw repl.ProgressWriter,
	) (iterator.Iterator[component.Responsive], error) {
		close(started)
		<-ctx.Done()
		pw.Progress(1, 2, "B")
		return toLines("late"), nil
	})

	h.Submit("slow")
	<-started
	require.NoError(t, h.Close())
	_, statuses := tabs.get()
	assert.Equal(t, map[string][]browserapi.TabStatus{
		"1": {{ID: "1", State: browserapi.TabWorking, Progress: -1, Title: "slow"}},
	}, statuses)
}

func TestCommandTitle(t *testing.T) {
	tests := []struct {
		name string
		cmd  repl.Command
		want string
	}{
		{name: "name", cmd: repl.Command{Name: "ls"}, want: "ls"},
		{
			name: "args",
			cmd:  repl.Command{Name: "pkg", Args: []string{"install", "rune-agent"}},
			want: "pkg install rune-agent",
		},
		{
			name: "control characters are dropped",
			cmd:  repl.Command{Name: "echo", Args: []string{"a\x1b[1mb"}},
			want: "echo a[1mb",
		},
		{
			name: "at the limit",
			cmd:  repl.Command{Name: strings.Repeat("a", statusTitleMaxBytes)},
			want: strings.Repeat("a", statusTitleMaxBytes),
		},
		{
			name: "over the limit",
			cmd:  repl.Command{Name: strings.Repeat("a", statusTitleMaxBytes+1)},
			want: strings.Repeat("a", statusTitleMaxBytes-len("…")) + "…",
		},
		{
			name: "cut between runes",
			cmd:  repl.Command{Name: strings.Repeat("é", statusTitleMaxBytes)},
			want: strings.Repeat("é", (statusTitleMaxBytes-len("…"))/2) + "…",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := commandTitle(tc.cmd)
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), statusTitleMaxBytes)
		})
	}
}
