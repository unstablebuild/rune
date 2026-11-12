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

package acpcmd

import (
	"strings"
	"testing"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/acpapi"
	"unstable.build/rune/internal/term/programstatus"
)

func TestAwayReport(t *testing.T) {
	t.Parallel()
	pending := func(title *string) acpapi.SessionState {
		return acpapi.SessionState{Busy: true, Pending: []acpapi.Permission{{
			Request: acp.RequestPermissionRequest{ToolCall: acp.ToolCallUpdate{Title: title}},
		}}}
	}
	long := strings.Repeat("é", 200)
	tests := []struct {
		name    string
		session string
		st      acpapi.SessionState
		end     *acpapi.TurnEnd
		want    programstatus.Report
	}{
		{
			name: "working", session: "brave-fox", st: acpapi.SessionState{Busy: true},
			want: programstatus.Report{State: programstatus.Working, Progress: -1, App: "brave-fox"},
		},
		{
			name: "a name no status takes is labelled by its agent", session: "code review",
			st:   acpapi.SessionState{Busy: true},
			want: programstatus.Report{State: programstatus.Working, Progress: -1, App: "claude"},
		},
		{
			name: "permission without a title", session: "brave-fox", st: pending(nil),
			want: programstatus.Report{
				State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
				App: "brave-fox", Title: "Allow the agent to continue?", Msg: "Allow the agent to continue?",
			},
		},
		{
			name: "long permission title", session: "brave-fox", st: pending(&long),
			want: programstatus.Report{
				State: programstatus.Blocked, Kind: programstatus.KindPermission, Progress: -1,
				App: "brave-fox", Title: strings.Repeat("é", 94) + "…", Msg: long,
			},
		},
		{
			name: "turn stopped short", session: "brave-fox",
			end: &acpapi.TurnEnd{StopReason: acp.StopReasonMaxTokens},
			want: programstatus.Report{
				State: programstatus.Done, Progress: -1, App: "brave-fox",
				Msg: "The agent ran out of tokens for this turn.",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := programstatus.Normalize(awayReport(tt.session, "claude", tt.st, tt.end))
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
