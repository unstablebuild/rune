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

package lspconsole

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/idelsp"
)

const workspaceRoot = "file:///home/me/proj"

type controlCall struct {
	op, language, root string
}

type fakeServers struct {
	statuses []idelsp.ServerStatus
	err      error
	calls    []controlCall
}

func (f *fakeServers) RootURI() string                { return workspaceRoot }
func (f *fakeServers) Servers() []idelsp.ServerStatus { return f.statuses }

func (f *fakeServers) Start(_ context.Context, language, root string) error {
	f.calls = append(f.calls, controlCall{"start", language, root})
	return f.err
}

func (f *fakeServers) Stop(_ context.Context, language, root string) error {
	f.calls = append(f.calls, controlCall{"stop", language, root})
	return f.err
}

func (f *fakeServers) Restart(_ context.Context, language, root string) error {
	f.calls = append(f.calls, controlCall{"restart", language, root})
	return f.err
}

var now = time.Date(2026, 10, 20, 21, 30, 0, 0, time.UTC)

func newTestHandler(statuses ...idelsp.ServerStatus) (*Handler, *fakeServers) {
	servers := &fakeServers{statuses: statuses}
	h := New(servers)
	h.now = func() time.Time { return now }
	return h, servers
}

func goplsRunning() idelsp.ServerStatus {
	return idelsp.ServerStatus{
		Language: "go", Name: "go", RootURI: workspaceRoot,
		Path: "/home/me/go/bin/gopls", Args: []string{"serve"},
		State: idelsp.ServerRunning, Pid: 4242, Started: now.Add(-90 * time.Second),
		Params: json.RawMessage(`{"rootUri":"file:///home/me/proj","clientInfo":{"name":"rune"}}`),
		Result: json.RawMessage(
			`{"capabilities":{"hoverProvider":true},"serverInfo":{"name":"gopls","version":"v0.16.2"}}`),
	}
}

func goplsFailed() idelsp.ServerStatus {
	return idelsp.ServerStatus{
		Language: "go", Name: "go", RootURI: workspaceRoot + "/tools",
		Path: "/Users/me/go/bin/gopls", Args: []string{"serve"},
		State: idelsp.ServerFailed,
		LastError: errors.New("start gopls: error waiting for pid: stream receive msg: " +
			"rpc error: code = Unknown desc = start command: " +
			"fork/exec /Users/me/go/bin/gopls: no such file or directory"),
		Params: json.RawMessage(`{"rootUri":"file:///home/me/proj/tools"}`),
	}
}

func pythonChildren() []idelsp.ServerStatus {
	return []idelsp.ServerStatus{
		{Language: "python", Name: "ruff", RootURI: workspaceRoot, State: idelsp.ServerRunning,
			Pid: 7, Started: now},
		{Language: "python", Name: "ty", RootURI: workspaceRoot, State: idelsp.ServerRunning,
			Pid: 8, Started: now},
	}
}

func TestStatusMarkdown(t *testing.T) {
	h, _ := newTestHandler(goplsRunning(), goplsFailed())

	assert.Equal(t,
		"| SERVER | ROOT | STATE | PID | UPTIME | RESTARTS | LAST ERROR |\n"+
			"| --- | --- | --- | --- | --- | --- | --- |\n"+
			"| `go` | `.` | running | `4242` | `1m30s` | 0 | — |\n"+
			"| `go` | `tools` | failed | — | — | 0 | "+
			"…fork/exec /Users/me/go/bin/gopls: no such file or directory |\n",
		h.statusMarkdown())
}

func TestStatusMarkdownWithoutServers(t *testing.T) {
	h, _ := newTestHandler()
	assert.Equal(t, "No language server has been initialized in this workspace.\n",
		h.statusMarkdown())
}

func TestStatusMarkdownNamesBackendsOfMultiServerLanguages(t *testing.T) {
	h, _ := newTestHandler(pythonChildren()...)
	md := h.statusMarkdown()
	assert.Contains(t, md, "| `python/ruff` | `.` |")
	assert.Contains(t, md, "| `python/ty` | `.` |")
}

func TestParamsMarkdownIsAJSONBlock(t *testing.T) {
	h, _ := newTestHandler(goplsRunning())
	sts, err := h.resolve("go")
	require.NoError(t, err)

	assert.Equal(t,
		"### `go` at `.`\n\n"+
			"```json\n"+
			"{\n"+
			"  \"rootUri\": \"file:///home/me/proj\",\n"+
			"  \"clientInfo\": {\n"+
			"    \"name\": \"rune\"\n"+
			"  }\n"+
			"}\n"+
			"```\n",
		h.paramsMarkdown(sts))
}

func TestJSONBlockFenceOutgrowsBackticksInContent(t *testing.T) {
	h, _ := newTestHandler(idelsp.ServerStatus{
		Language: "go", Name: "go", RootURI: workspaceRoot,
		Params: json.RawMessage("{\"doc\":\"```go\"}"),
	})
	sts, err := h.resolve("go")
	require.NoError(t, err)
	assert.Contains(t, h.paramsMarkdown(sts), "````json\n")
}

func TestCapabilitiesMarkdown(t *testing.T) {
	h, _ := newTestHandler(goplsRunning(), goplsFailed())

	running, err := h.resolve("go@.")
	require.NoError(t, err)
	md := h.capabilitiesMarkdown(running)
	assert.Contains(t, md, "```json\n")
	assert.Contains(t, md, "\"hoverProvider\": true")

	failed, err := h.resolve("go@tools")
	require.NoError(t, err)
	assert.Contains(t, h.capabilitiesMarkdown(failed),
		"has not completed the initialize handshake (state: failed)")
}

func TestInfoMarkdown(t *testing.T) {
	h, _ := newTestHandler(goplsRunning(), goplsFailed())

	running, err := h.resolve("go@.")
	require.NoError(t, err)
	md := h.infoMarkdown(running)
	assert.Contains(t, md, "### `go` at `.`")
	assert.Contains(t, md, "- **State:** running")
	assert.Contains(t, md, "- **Command:** `/home/me/go/bin/gopls serve`")
	assert.Contains(t, md, "- **Root:** `/home/me/proj`")
	assert.Contains(t, md, "- **PID:** `4242`, output in `process stdio 4242`")
	assert.Contains(t, md, "- **Uptime:** 1m30s")
	assert.Contains(t, md, "- **Server:** gopls v0.16.2")
	assert.Contains(t, md, "- **Last Error:** —")

	failed, err := h.resolve("go@tools")
	require.NoError(t, err)
	md = h.infoMarkdown(failed)
	assert.Contains(t, md, "- **PID:** —")
	assert.Contains(t, md, "```text\n"+goplsFailed().LastError.Error()+"\n```",
		"info must show the whole error")
}

func TestResolve(t *testing.T) {
	h, _ := newTestHandler(append(pythonChildren(), goplsRunning(), goplsFailed())...)
	cases := []struct {
		sel  string
		want []string
	}{
		{sel: "go", want: []string{"go@.", "go@tools"}},
		{sel: "go@.", want: []string{"go@."}},
		{sel: "go@./tools", want: []string{"go@tools"}},
		{sel: "go@/home/me/proj/tools", want: []string{"go@tools"}},
		{sel: "python", want: []string{"ruff@.", "ty@."}},
		{sel: "ruff", want: []string{"ruff@."}},
		{sel: "python/ty", want: []string{"ty@."}},
	}
	for _, tc := range cases {
		t.Run(tc.sel, func(t *testing.T) {
			sts, err := h.resolve(tc.sel)
			require.NoError(t, err)
			var got []string
			for _, st := range sts {
				got = append(got, st.Name+"@"+h.relRoot(st.RootURI))
			}
			assert.Equal(t, tc.want, got)
		})
	}

	_, err := h.resolve("rust")
	assert.ErrorContains(t, err, `no language server matches "rust"`)
}

func TestControlActsOncePerLanguageRoot(t *testing.T) {
	h, servers := newTestHandler(append(pythonChildren(), goplsRunning(), goplsFailed())...)
	ctx := context.Background()

	_, err := h.HandleCommand(ctx, repl.Command{Name: CommandName, Args: []string{"restart", "ruff"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	_, err = h.HandleCommand(ctx, repl.Command{Name: CommandName, Args: []string{"start", "go@tools"}},
		repl.NopProgressWriter())
	require.NoError(t, err)
	_, err = h.HandleCommand(ctx, repl.Command{Name: CommandName, Args: []string{"stop", "go"}},
		repl.NopProgressWriter())
	require.NoError(t, err)

	assert.Equal(t, []controlCall{
		{"restart", "python", workspaceRoot},
		{"start", "go", workspaceRoot + "/tools"},
		{"stop", "go", workspaceRoot},
		{"stop", "go", workspaceRoot + "/tools"},
	}, servers.calls, "the backends of a language at a root are controlled together")
}

func TestControlReportsFailures(t *testing.T) {
	h, servers := newTestHandler(goplsFailed())
	servers.err = idelsp.ErrNoServer

	_, err := h.HandleCommand(context.Background(),
		repl.Command{Name: CommandName, Args: []string{"start", "go"}}, repl.NopProgressWriter())
	require.ErrorIs(t, err, idelsp.ErrNoServer)
	assert.ErrorContains(t, err, "start `go` at `tools`")
}

func TestHandleCommandUsageErrors(t *testing.T) {
	h, _ := newTestHandler(goplsRunning())
	ctx := context.Background()
	for _, args := range [][]string{{"params"}, {"info", "go", "extra"}, {"bogus"}} {
		_, err := h.HandleCommand(ctx, repl.Command{Name: CommandName, Args: args},
			repl.NopProgressWriter())
		assert.Error(t, err, "%v", args)
	}
	for _, args := range [][]string{nil, {"status"}, {"params", "go"}, {"help"}} {
		it, err := h.HandleCommand(ctx, repl.Command{Name: CommandName, Args: args},
			repl.NopProgressWriter())
		require.NoError(t, err, "%v", args)
		out, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err)
		assert.NotEmpty(t, out, "%v", args)
	}
}

func TestComplete(t *testing.T) {
	h, _ := newTestHandler(append(pythonChildren(), goplsRunning(), goplsFailed())...)
	ctx := context.Background()
	complete := func(args ...string) []string {
		it, err := h.Complete(ctx, CommandName, args)
		require.NoError(t, err)
		got, err := iterator.ToSlice(ctx, it)
		require.NoError(t, err)
		return got
	}

	assert.Equal(t, []string{"start", "status", "stop"}, complete("st"))
	assert.Equal(t, []string{"go", "go@.", "go@tools"}, complete("info", "go"))
	assert.Equal(t, []string{
		"python", "python/ruff", "python/ty", "python@.",
	}, complete("restart", "py"))
	assert.Equal(t, []string{"ruff"}, complete("params", "ru"))
	assert.Empty(t, complete("status", ""))
}
