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

package agentools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/agent/toolpolicy"
	"unstable.build/rune/cmd/rune-agent/configedit"
)

func mustPolicy(t *testing.T, allowed, denied []string) toolpolicy.Policy {
	t.Helper()
	p, err := toolpolicy.Parse(allowed, denied)
	require.NoError(t, err)
	return p
}

// countingExec counts the commands started, finishing each at once.
func countingExec(started *int) *recordingExec {
	return &recordingExec{startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
		*started++
		if cmd.Watcher != nil {
			cmd.Watcher.WatchProcess() <- nil
		}
		return 1, nil
	}}
}

func TestBashWithPolicy(t *testing.T) {
	p := mustPolicy(t, []string{"bash(git log *)"}, []string{"bash(git log -p *)"})
	cases := []struct {
		command     string
		wantErr     string
		wantStarted int
	}{
		{command: "git log -1", wantStarted: 1},
		{command: "git push", wantErr: `"git push" matches no allowed pattern`},
		{command: "git log -1 > f", wantErr: "redirects output to the file f"},
		{command: "git log -p x", wantErr: "denied pattern"},
		{command: "eval git push", wantErr: "eval runs code that cannot be followed"},
		{command: "grep -r x .", wantErr: `"grep -r x ." matches no allowed pattern`},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			started := 0
			tool := newBash(countingExec(&started), dirURI("/workspace"), configedit.NopConfig()).(agent.PolicyTool).WithPolicy(p)
			args, err := json.Marshal(bashArgs{Command: tc.command, Description: "x"})
			require.NoError(t, err)

			result := tool.Execute(context.Background(), string(args))

			assert.Equal(t, tc.wantStarted, started, "a rejected command never reaches the executor")
			if tc.wantErr == "" {
				assert.False(t, result.IsError, result.Content)
				return
			}
			assert.True(t, result.IsError)
			assert.Contains(t, result.Content, tc.wantErr)
			assert.Contains(t, result.Content, "`git log *`", "the rejection lists the allowed patterns")
		})
	}
}

func TestBashWithPolicyDescribesTheRestrictions(t *testing.T) {
	base := newBash(&recordingExec{}, dirURI("/workspace"), configedit.NopConfig())
	restricted := base.(agent.PolicyTool).WithPolicy(mustPolicy(t, []string{"bash(git log *)"}, nil))

	assert.NotContains(t, base.Definition().Function.Description, "Commands are restricted")
	assert.Contains(t, restricted.Definition().Function.Description,
		"Commands are restricted: every program a command runs must match one of `git log *`.")
	unrestricted := base.(agent.PolicyTool).WithPolicy(mustPolicy(t, []string{"bash"}, nil))
	assert.Equal(t, base.Definition().Function.Description, unrestricted.Definition().Function.Description)
}
