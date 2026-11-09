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
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

type mockExec struct {
	startFn func(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error)
}

func (m *mockExec) Start(ctx context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
	return m.startFn(ctx, cmd)
}

func (m *mockExec) Signal(workspaceapi.Pid, syscall.Signal) error { return nil }
func (m *mockExec) Close() error                                  { return nil }

func TestRecallConversation_ValidID(t *testing.T) {
	const transcript = "### user\n\nhow do I fix this?\n\n### assistant\n\nHere's the fix."

	exec := &mockExec{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			_, _ = io.WriteString(cmd.Stdout, transcript+"\n")
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	tool := NewRecallConversation(exec, "/data/memories")
	result := tool.Execute(context.Background(), `{"memory_id":"handler-refactor"}`)
	assert.False(t, result.IsError)
	assert.Equal(t, transcript, result.Content)
}

func TestRecallConversation_NotFound(t *testing.T) {
	exec := &mockExec{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			_, _ = fmt.Fprintln(cmd.Stderr.(*bytes.Buffer), "memory not found: bad-id")
			cmd.Watcher.WatchProcess() <- fmt.Errorf("exit status 1")
			return 0, nil
		},
	}

	tool := NewRecallConversation(exec, "/data/memories")
	result := tool.Execute(context.Background(), `{"memory_id":"bad-id"}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "exit status 1")
}

func TestRecallConversation_EmptyMemoryID(t *testing.T) {
	exec := &mockExec{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	tool := NewRecallConversation(exec, "/data/memories")
	result := tool.Execute(context.Background(), `{"memory_id":""}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "memory_id is required")
}

func TestRecallConversation_InvalidJSON(t *testing.T) {
	exec := &mockExec{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	tool := NewRecallConversation(exec, "/data/memories")
	result := tool.Execute(context.Background(), `{bad json}`)
	assert.True(t, result.IsError)
	assert.Contains(t, result.Content, "invalid")
}

func TestRecallConversation_Summary(t *testing.T) {
	exec := &mockExec{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	tool := NewRecallConversation(exec, "/data/memories")
	assert.Equal(t, "handler-refactor", tool.Summary(`{"memory_id":"handler-refactor"}`))
	assert.Equal(t, "", tool.Summary(`{bad}`))
}

func TestRecallConversation_Definition(t *testing.T) {
	exec := &mockExec{
		startFn: func(_ context.Context, cmd workspaceapi.Cmd) (workspaceapi.Pid, error) {
			cmd.Watcher.WatchProcess() <- nil
			return 0, nil
		},
	}

	tool := NewRecallConversation(exec, "/data/memories")
	def := tool.Definition()
	assert.Equal(t, "recall_conversation", def.Function.Name)
}

func (*mockExec) LookPath(context.Context, string, string) (string, error) {
	return "", errors.ErrUnsupported
}
