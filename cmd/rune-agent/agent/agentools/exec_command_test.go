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
	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/configedit"
)

func newTestExecCommand(t *testing.T) (*execCommandTool, *SessionManager) {
	t.Helper()
	mgr := NewSessionManager(context.Background(), localExec{}, nil)
	t.Cleanup(func() { _ = mgr.Close() })
	dir := t.TempDir()
	tool := NewExecCommand(mgr, dirURI(dir), configedit.NopConfig()).(*execCommandTool)
	return tool, mgr
}

func TestExecCommand_success(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	result := tool.Execute(context.Background(), `{"cmd":"echo hello"}`)
	assert.False(t, result.IsError)

	var out execCommandOutput
	require.NoError(t, json.Unmarshal([]byte(result.Content), &out))
	assert.Contains(t, out.Output, "hello")
	assert.NotNil(t, out.ExitCode)
	assert.Equal(t, 0, *out.ExitCode)
	assert.Equal(t, 0, out.SessionID) // 0 means exited
	assert.Greater(t, out.WallTimeSeconds, 0.0)
}

func TestExecCommand_nonzero_exit(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	result := tool.Execute(context.Background(), `{"cmd":"exit 7"}`)
	assert.True(t, result.IsError)

	var out execCommandOutput
	require.NoError(t, json.Unmarshal([]byte(result.Content), &out))
	require.NotNil(t, out.ExitCode)
	assert.Equal(t, 7, *out.ExitCode)
}

func TestExecCommand_long_running_returns_session_id(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	result := tool.Execute(context.Background(), `{"cmd":"sleep 60","yield_time_ms":250}`)
	assert.False(t, result.IsError)

	var out execCommandOutput
	require.NoError(t, json.Unmarshal([]byte(result.Content), &out))
	assert.Greater(t, out.SessionID, 0, "should return session_id for running process")
	assert.Nil(t, out.ExitCode, "should not have exit_code for running process")
}

func TestExecCommand_workdir(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	result := tool.Execute(context.Background(), `{"cmd":"pwd","workdir":"/tmp"}`)
	assert.False(t, result.IsError)

	var out execCommandOutput
	require.NoError(t, json.Unmarshal([]byte(result.Content), &out))
	assert.Contains(t, out.Output, "/tmp")
}

func TestExecCommand_custom_shell(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	result := tool.Execute(context.Background(), `{"cmd":"echo sh_test","shell":"sh"}`)
	assert.False(t, result.IsError)

	var out execCommandOutput
	require.NoError(t, json.Unmarshal([]byte(result.Content), &out))
	assert.Contains(t, out.Output, "sh_test")
}

func TestExecCommand_empty_cmd(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	result := tool.Execute(context.Background(), `{"cmd":""}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "cmd must not be empty")
}

func TestExecCommand_invalid_json(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	result := tool.Execute(context.Background(), `bad`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "invalid arguments")
}

func TestExecCommand_yield_time_clamped(t *testing.T) {
	tool, _ := newTestExecCommand(t)

	// Very small yield — should be clamped to min (250ms).
	result := tool.Execute(context.Background(), `{"cmd":"sleep 60","yield_time_ms":1}`)
	assert.False(t, result.IsError)

	var out execCommandOutput
	require.NoError(t, json.Unmarshal([]byte(result.Content), &out))
	assert.Greater(t, out.SessionID, 0)
	// Wall time should be at least ~250ms.
	assert.GreaterOrEqual(t, out.WallTimeSeconds, 0.2)
}

func TestExecCommand_Definition(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	def := tool.Definition()
	assert.Equal(t, "exec_command", def.Function.Name)
	assert.NotEmpty(t, def.Function.Description)
	assert.NotNil(t, def.Function.Parameters)
}

func TestExecCommand_Summary(t *testing.T) {
	tool, _ := newTestExecCommand(t)
	assert.Equal(t, "echo hello", tool.Summary(`{"cmd":"echo hello"}`))
	assert.Equal(t, "", tool.Summary(`bad`))
}

func TestClamp(t *testing.T) {
	assert.Equal(t, 5, clamp(5, 1, 10))
	assert.Equal(t, 1, clamp(0, 1, 10))
	assert.Equal(t, 10, clamp(99, 1, 10))
}

func TestExecCommandWithPolicy(t *testing.T) {
	p := mustPolicy(t, []string{"bash(git log *)"}, nil)
	cases := []struct {
		args        string
		wantErr     string
		wantStarted int
	}{
		{args: `{"cmd":"git log -1"}`, wantStarted: 1},
		{args: `{"cmd":"git log -1","shell":"zsh"}`, wantStarted: 1},
		{args: `{"cmd":"git push"}`, wantErr: `"git push" matches no allowed pattern`},
		{args: `{"cmd":"git log -1","shell":"python3"}`, wantErr: `"python3 -c git log -1"`},
		{args: `{"cmd":"print(1)","shell":"python"}`, wantErr: `"python -c print(1)"`},
	}
	for _, tc := range cases {
		t.Run(tc.args, func(t *testing.T) {
			started := 0
			mgr := NewSessionManager(context.Background(), countingExec(&started), nil)
			t.Cleanup(func() { _ = mgr.Close() })
			tool := NewExecCommand(mgr, dirURI(t.TempDir()), configedit.NopConfig()).(agent.PolicyTool).WithPolicy(p)

			result := tool.Execute(context.Background(), tc.args)

			assert.Equal(t, tc.wantStarted, started, "a rejected command never starts")
			if tc.wantErr == "" {
				assert.False(t, result.IsError, result.Content)
				return
			}
			assert.True(t, result.IsError)
			assert.Contains(t, result.Content, tc.wantErr)
		})
	}
	assert.Contains(t, NewExecCommand(nil, dirURI("/w"), configedit.NopConfig()).(agent.PolicyTool).
		WithPolicy(p).Definition().Function.Description, "`git log *`")
}
