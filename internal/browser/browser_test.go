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

package browser

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/component/markdown"
	thandler "unstable.build/rune/internal/handler"
	"unstable.build/rune/internal/handler/handlertest"
	mdhandler "unstable.build/rune/internal/handler/markdown"
	"unstable.build/rune/internal/term/programstatus"
)

func TestReportFromTabStatus(t *testing.T) {
	tests := []struct {
		name   string
		status browserapi.TabStatus
		app    string
		want   programstatus.Report
		err    string
	}{
		{
			name:   "zero value is a root idle report",
			status: browserapi.TabStatus{},
			want:   programstatus.Report{Progress: -1},
		},
		{
			name: "every field",
			status: browserapi.TabStatus{
				ID: "tool/bash", State: browserapi.TabBlocked, Kind: browserapi.TabKindPermission,
				Progress: 40, Title: "make test", Message: "Approve?",
			},
			app: "claude",
			want: programstatus.Report{
				ID: "tool/bash", State: programstatus.Blocked, Kind: programstatus.KindPermission,
				Progress: 40, App: "claude", Title: "make test", Msg: "Approve?",
			},
		},
		{
			name:   "an app outside the protocol charset is dropped",
			status: browserapi.TabStatus{State: browserapi.TabWorking, Progress: -1},
			app:    "my app",
			want:   programstatus.Report{State: programstatus.Working, Progress: -1},
		},
		{
			name:   "working",
			status: browserapi.TabStatus{State: browserapi.TabWorking, Progress: 0},
			want:   programstatus.Report{State: programstatus.Working, Progress: 0},
		},
		{
			name:   "done drops kind and progress",
			status: browserapi.TabStatus{State: browserapi.TabDone, Kind: browserapi.TabKindAuth, Progress: 100, Message: "built"},
			want:   programstatus.Report{State: programstatus.Done, Progress: -1, Msg: "built"},
		},
		{
			name:   "error",
			status: browserapi.TabStatus{State: browserapi.TabError, Progress: -1, Message: "exit 1"},
			want:   programstatus.Report{State: programstatus.Error, Progress: -1, Msg: "exit 1"},
		},
		{
			name:   "blocked question",
			status: browserapi.TabStatus{State: browserapi.TabBlocked, Kind: browserapi.TabKindQuestion, Progress: -1},
			want:   programstatus.Report{State: programstatus.Blocked, Kind: programstatus.KindQuestion, Progress: -1},
		},
		{
			name:   "blocked auth",
			status: browserapi.TabStatus{State: browserapi.TabBlocked, Kind: browserapi.TabKindAuth, Progress: -1},
			want:   programstatus.Report{State: programstatus.Blocked, Kind: programstatus.KindAuth, Progress: -1},
		},
		{
			name:   "clear keeps only the id",
			status: browserapi.TabStatus{ID: "job", State: browserapi.TabClear, Progress: 5, Title: "t", Message: "m"},
			want:   programstatus.Report{ID: "job", State: programstatus.Clear, Progress: -1},
		},
		{
			name:   "unknown state",
			status: browserapi.TabStatus{State: browserapi.TabState(200)},
			err:    "unknown tab state 200",
		},
		{
			name:   "unknown kind",
			status: browserapi.TabStatus{State: browserapi.TabBlocked, Kind: browserapi.TabBlockedKind(200)},
			err:    "unknown tab blocked kind 200",
		},
		{
			name:   "protocol limits apply",
			status: browserapi.TabStatus{State: browserapi.TabWorking, ID: "a//b"},
			err:    "invalid id",
		},
		{
			name:   "control characters rejected",
			status: browserapi.TabStatus{State: browserapi.TabDone, Message: "a\nb"},
			err:    "control character",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReportFromTabStatus(tc.status, tc.app)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				assert.Equal(t, programstatus.Report{}, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestTabStatusFromReport(t *testing.T) {
	tests := []struct {
		name   string
		report programstatus.Report
		want   browserapi.TabStatus
	}{
		{
			name:   "zero value",
			report: programstatus.Report{},
			want:   browserapi.TabStatus{},
		},
		{
			name:   "idle",
			report: programstatus.Report{State: programstatus.Idle, Progress: -1},
			want:   browserapi.TabStatus{State: browserapi.TabIdle, Progress: -1},
		},
		{
			name: "every field",
			report: programstatus.Report{
				ID: "tool/bash", State: programstatus.Blocked, Kind: programstatus.KindPermission,
				Progress: 40, App: "claude", Title: "make test", Msg: "Approve?",
			},
			want: browserapi.TabStatus{
				ID: "tool/bash", State: browserapi.TabBlocked, Kind: browserapi.TabKindPermission,
				Progress: 40, Title: "make test", Message: "Approve?",
			},
		},
		{
			name:   "working",
			report: programstatus.Report{State: programstatus.Working, Progress: 7},
			want:   browserapi.TabStatus{State: browserapi.TabWorking, Progress: 7},
		},
		{
			name:   "done",
			report: programstatus.Report{State: programstatus.Done, Progress: -1, Msg: "ok"},
			want:   browserapi.TabStatus{State: browserapi.TabDone, Progress: -1, Message: "ok"},
		},
		{
			name:   "error",
			report: programstatus.Report{State: programstatus.Error, Progress: -1},
			want:   browserapi.TabStatus{State: browserapi.TabError, Progress: -1},
		},
		{
			name:   "blocked question",
			report: programstatus.Report{State: programstatus.Blocked, Kind: programstatus.KindQuestion, Progress: -1},
			want:   browserapi.TabStatus{State: browserapi.TabBlocked, Kind: browserapi.TabKindQuestion, Progress: -1},
		},
		{
			name:   "blocked auth",
			report: programstatus.Report{State: programstatus.Blocked, Kind: programstatus.KindAuth, Progress: -1},
			want:   browserapi.TabStatus{State: browserapi.TabBlocked, Kind: browserapi.TabKindAuth, Progress: -1},
		},
		{
			name:   "clear",
			report: programstatus.Report{ID: "job", State: programstatus.Clear, Progress: -1},
			want:   browserapi.TabStatus{ID: "job", State: browserapi.TabClear, Progress: -1},
		},
		{
			name:   "unknown state and kind",
			report: programstatus.Report{State: programstatus.State(200), Kind: programstatus.Kind(200), Progress: -1},
			want:   browserapi.TabStatus{Progress: -1},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, TabStatusFromReport(tc.report))
		})
	}
}

func TestTabStatusReportRoundTrip(t *testing.T) {
	reports := []programstatus.Report{
		{Progress: -1},
		{State: programstatus.Working, Progress: -1, App: "cargo", Title: "building"},
		{ID: "build/test", State: programstatus.Working, Progress: 50},
		{State: programstatus.Done, Progress: -1, Msg: "ok"},
		{State: programstatus.Error, Progress: -1, Msg: "exit 1"},
		{State: programstatus.Blocked, Progress: -1},
		{State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1, Msg: "approve?"},
		{State: programstatus.Blocked, Kind: programstatus.KindQuestion, Progress: 30},
		{State: programstatus.Blocked, Kind: programstatus.KindAuth, Progress: -1},
		{ID: "job", State: programstatus.Clear, Progress: -1},
	}
	for _, r := range reports {
		got, err := ReportFromTabStatus(TabStatusFromReport(r), r.App)
		require.NoError(t, err, "%+v", r)
		assert.Equal(t, r, got)
	}
}

func TestHandlerWrappersPointerLeaves(t *testing.T) {
	nop := func() error { return nil }
	markdownHandler := func(t *testing.T) *mdhandler.Handler {
		comp, err := markdown.New(strings.Repeat("A paragraph with a [link](http://example.com).\n\n", 60))
		require.NoError(t, err)
		return mdhandler.New(comp)
	}
	for _, tc := range []struct {
		name string
		new  func(t *testing.T) tui.Handler
	}{
		{"FuncHandler", func(t *testing.T) tui.Handler { return FuncHandler(newPointerLess(t), nop) }},
		{"NopHandler", func(t *testing.T) tui.Handler { return NopHandler(newPointerLess(t)) }},
		{"StaticFloating", func(t *testing.T) tui.Handler {
			return StaticFloating(NopHandler(newPointerLess(t)), 20, 6)
		}},
		{"NopFloatingHandler", func(t *testing.T) tui.Handler {
			l := newPointerLess(t)
			return NopFloatingHandler(thandler.FloatingBuffer(l, l.Buffer()))
		}},
		{"FuncFloatingHandler", func(t *testing.T) tui.Handler {
			l := newPointerLess(t)
			return FuncFloatingHandler(thandler.FloatingBuffer(l, l.Buffer()), nop)
		}},
		{"NopScrollableFloatingHandler", func(t *testing.T) tui.Handler {
			return NopScrollableFloatingHandler(markdownHandler(t))
		}},
		{"FuncScrollableFloatingHandler", func(t *testing.T) tui.Handler {
			return FuncScrollableFloatingHandler(markdownHandler(t), nop)
		}},
		{"FuncFloating", func(t *testing.T) tui.Handler {
			return FuncFloating(NopHandler(newPointerLess(t)), func() (int, int) { return 20, 6 })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, size := range [][2]int{{60, 15}, {1, 1}} {
				handlertest.RunPointerLeaves(t, tc.new(t), size[0], size[1])
			}
		})
	}
}
