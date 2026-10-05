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

package extensionv2

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/blue/auth"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/extension"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/texttest"
)

type extensionsCapturingEditor struct {
	*texttest.TestEditor
	manual  textapi.CommandManual
	handler textapi.REPLHandler
	calls   int
}

func newExtensionsCapturingEditor() *extensionsCapturingEditor {
	return &extensionsCapturingEditor{TestEditor: texttest.NopEditor()}
}

func (e *extensionsCapturingEditor) RegisterREPLCommand(
	manual textapi.CommandManual, handler textapi.REPLHandler,
) error {
	e.calls++
	e.manual = manual
	e.handler = handler
	return nil
}

var _ text.Editor = (*extensionsCapturingEditor)(nil)

func TestRegisterExtensionsREPLCommand(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	editor := newExtensionsCapturingEditor()

	require.NoError(t, registerExtensionsREPLCommand(runner, editor))
	require.Equal(t, 1, editor.calls)
	assert.Equal(t, extensionsREPLCommand, editor.manual.Name)
	require.Len(t, editor.manual.Commands, 6)
	assert.Equal(t, extensionsREPLCommandStatus, editor.manual.Commands[0].Name)
	assert.Equal(t, extensionsREPLCommandInfo, editor.manual.Commands[1].Name)
	assert.Equal(t, extensionsREPLCommandStart, editor.manual.Commands[2].Name)
	assert.Equal(t, extensionsREPLCommandStop, editor.manual.Commands[3].Name)
	assert.Equal(t, extensionsREPLCommandRestart, editor.manual.Commands[4].Name)
	assert.Equal(t, extensionsREPLCommandLogs, editor.manual.Commands[5].Name)
	assert.NotNil(t, editor.handler)
}

func TestExtensionsREPLStatusOutput(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	require.NoError(t, runner.Run("alpha", "/bin/ext --serve", config.NopConfig()))
	runner.stopExtension("alpha", assert.AnError)
	require.NoError(t, runner.Run("beta", "/bin/beta --watch", config.NopConfig()))
	handler := extensionsREPLHandler{runner: runner}

	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandStatus},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	require.Len(t, items, 1)
	md, ok := items[0].(*markdown.Component)
	require.True(t, ok, "expected *markdown.Component, got %T", items[0])
	require.NotNil(t, md)
	// Render wide enough that no cells wrap, so we can assert full values.
	out := extensionsResponsiveStringsWithWidth(t, items, 320)
	rendered := strings.Join(out, "\n")
	// Drop cell wrapping whitespace inserted by the markdown table renderer so
	// assertions target the logical values rather than visual layout.
	flattened := strings.Join(strings.Fields(rendered), " ")
	for _, want := range []string{
		"ID", "Status", "PID", "Starts",
		"alpha", "Errored",
		"beta", "Running",
	} {
		assert.Contains(t, flattened, want)
	}
	assert.NotContains(t, flattened, "Last error")
	assert.NotContains(t, flattened, "Command")
	assert.NotContains(t, flattened, "/bin/ext --serve")
	assert.NotContains(t, flattened, "/bin/beta --watch")
}

func TestExtensionsREPLInfoOutput(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	require.NoError(t, runner.Run("alpha", "/bin/ext --serve", config.MapConfig(map[string]any{"foo": "bar"})))
	handler := extensionsREPLHandler{runner: runner}

	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandInfo, "alpha"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, items, 1)
	_, ok := items[0].(*markdown.Component)
	require.True(t, ok, "expected *markdown.Component, got %T", items[0])

	out := extensionsResponsiveStringsWithWidth(t, items, 320)
	flattened := strings.Join(strings.Fields(strings.Join(out, "\n")), " ")
	for _, want := range []string{
		"alpha",
		"Running",
		"/bin/ext --serve",
		"foo",
		"bar",
		"Starts",
		"PID",
		"Uptime",
	} {
		assert.Contains(t, flattened, want)
	}
}

func TestExtensionsREPLStartCommand(t *testing.T) {
	t.Parallel()

	exec := &protocolDrivingExecutor{extensionID: "alpha"}
	runner := newTestWorkspaceRunnerWithExecutor(t, exec)
	handler := extensionsREPLHandler{runner: runner}

	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandStart, "alpha", "/bin/ext", "--serve"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	out := extensionsResponsiveStrings(t, items)
	assert.Equal(t, []string{"Started extension alpha"}, out)
	cmd := exec.snapshotCmd()
	assert.Equal(t, "/bin/ext", cmd.Path)
	assert.Equal(t, []string{"--serve"}, cmd.Args)
}

func TestExtensionsREPLStartCommandWithConfig(t *testing.T) {
	t.Parallel()

	exec := &protocolDrivingExecutor{extensionID: "alpha"}
	runner := newTestWorkspaceRunnerWithExecutor(t, exec)
	handler := extensionsREPLHandler{runner: runner}

	_, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandStart, "alpha", "/bin/ext", "--config", `{"foo":"bar"}`},
	}, repl.NopProgressWriter())
	require.NoError(t, err)

	states := runner.listExtensions()
	require.Len(t, states, 1)
	value, err := states[0].Config.GetString("foo")
	require.NoError(t, err)
	assert.Equal(t, "bar", value)
}

func TestExtensionsREPLStopCommand(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	require.NoError(t, runner.Run("alpha", "/bin/ext", config.NopConfig()))
	handler := extensionsREPLHandler{runner: runner}

	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandStop, "alpha"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	assert.Equal(t, []string{"Stopped extension alpha"}, extensionsResponsiveStrings(t, items))
	states := runner.listExtensions()
	require.Len(t, states, 1)
	assert.False(t, states[0].Running)
}

func TestExtensionsREPLRestartCommandReusesStoredCommandAndConfig(t *testing.T) {
	t.Parallel()

	exec := &protocolDrivingExecutor{extensionID: "alpha"}
	runner := newTestWorkspaceRunnerWithExecutor(t, exec)
	require.NoError(t, runner.Run("alpha", "/bin/ext --serve", config.MapConfig(map[string]any{"foo": "bar"})))
	handler := extensionsREPLHandler{runner: runner}

	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandRestart, "alpha"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)

	assert.Equal(t, []string{"Restarted extension alpha"}, extensionsResponsiveStrings(t, items))
	states := runner.listExtensions()
	require.Len(t, states, 1)
	assert.Equal(t, 2, states[0].StartCount)
	assert.Equal(t, workspaceapi.Pid(2), states[0].Pid)
	value, err := states[0].Config.GetString("foo")
	require.NoError(t, err)
	assert.Equal(t, "bar", value)
}

func TestExtensionsREPLCompleteSubcommandsAndIDs(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	require.NoError(t, runner.Run("alpha", "/bin/ext", config.NopConfig()))
	handler := extensionsREPLHandler{runner: runner}

	it, err := handler.Complete(context.Background(), extensionsREPLCommand, nil)
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{
		extensionsREPLCommandStatus,
		extensionsREPLCommandInfo,
		extensionsREPLCommandStart,
		extensionsREPLCommandStop,
		extensionsREPLCommandRestart,
		extensionsREPLCommandLogs,
	}, items)

	it, err = handler.Complete(context.Background(), extensionsREPLCommand, []string{"st"})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{extensionsREPLCommandStatus, extensionsREPLCommandStart, extensionsREPLCommandStop}, items)

	it, err = handler.Complete(context.Background(), extensionsREPLCommand, []string{extensionsREPLCommandStop, "al"})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha"}, items)

	it, err = handler.Complete(context.Background(), extensionsREPLCommand, []string{extensionsREPLCommandInfo, "al"})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha"}, items)
}

func TestExtensionsREPLErrors(t *testing.T) {
	t.Parallel()

	handler := extensionsREPLHandler{runner: newTestWorkspaceRunner(t)}

	_, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{"wat"},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unknown extensions subcommand "wat"`)

	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandStart, "alpha"},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage: extensions start")

	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandInfo},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage: extensions info <id>")

	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandStop, "missing"},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `extension "missing" not found`)

	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandRestart, "missing"},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `extension "missing" not found`)

	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandInfo, "missing"},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `extension "missing" not found`)

	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandStart, "alpha", "/bin/ext", "--config", "{"},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse config json")
}

func TestExtensionsREPLLogsUnknown(t *testing.T) {
	t.Parallel()

	handler := extensionsREPLHandler{runner: newTestWorkspaceRunner(t)}
	_, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandLogs, "missing"},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `extension "missing" not found`)
}

func TestExtensionsREPLLogsUsage(t *testing.T) {
	t.Parallel()

	handler := extensionsREPLHandler{runner: newTestWorkspaceRunner(t)}
	_, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandLogs},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "usage: extensions logs <id>")

	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandLogs, "alpha", "--tail"},
	}, repl.NopProgressWriter())
	require.Error(t, err)

	_, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandLogs, "alpha", "--tail", "nope"},
	}, repl.NopProgressWriter())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--tail expects")
}

func TestExtensionsREPLLogsTailFileRemoved(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	require.NoError(t, runner.Run("alpha", "/bin/ext", config.NopConfig()))
	runner.mu.Lock()
	logPath := runner.states["alpha"].logPath
	runner.mu.Unlock()
	require.NoError(t, os.Remove(logPath))

	handler := extensionsREPLHandler{runner: runner}
	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandLogs, "alpha", "--tail", "10"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"No logs captured yet."}, extensionsResponsiveStrings(t, items))
}

func TestExtensionsREPLLogsTail(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	require.NoError(t, runner.Run("alpha", "/bin/ext", config.NopConfig()))
	runner.mu.Lock()
	logPath := runner.states["alpha"].logPath
	runner.mu.Unlock()
	require.NotEmpty(t, logPath)

	var b strings.Builder
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	require.NoError(t, os.WriteFile(logPath, []byte(b.String()), 0o600))

	handler := extensionsREPLHandler{runner: runner}
	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandLogs, "alpha", "--tail", "2"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	out := extensionsResponsiveStrings(t, items)
	require.GreaterOrEqual(t, len(out), 3)
	assert.Contains(t, out[0], "Log file: ")
	assert.Contains(t, out[0], logPath)
	assert.Equal(t, []string{"line-3", "line-4"}, out[len(out)-2:])

	// --tail 0 returns all lines.
	it, err = handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandLogs, "alpha", "--tail", "0"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	out = extensionsResponsiveStrings(t, items)
	require.Len(t, out, 6) // header + 5 lines
	assert.Equal(t,
		[]string{"line-0", "line-1", "line-2", "line-3", "line-4"},
		out[1:],
	)
}

func TestExtensionsREPLLogsCompletion(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	require.NoError(t, runner.Run("alpha", "/bin/ext", config.NopConfig()))
	handler := extensionsREPLHandler{runner: runner}

	it, err := handler.Complete(context.Background(), extensionsREPLCommand, nil)
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Contains(t, items, extensionsREPLCommandLogs)

	it, err = handler.Complete(context.Background(), extensionsREPLCommand, []string{"lo"})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{extensionsREPLCommandLogs}, items)

	it, err = handler.Complete(context.Background(), extensionsREPLCommand,
		[]string{extensionsREPLCommandLogs, "al"})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"alpha"}, items)

	// After `logs <id>`, the next slot should offer --tail.
	it, err = handler.Complete(context.Background(), extensionsREPLCommand,
		[]string{extensionsREPLCommandLogs, "alpha", ""})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"--tail"}, items)

	it, err = handler.Complete(context.Background(), extensionsREPLCommand,
		[]string{extensionsREPLCommandLogs, "alpha", "--t"})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Equal(t, []string{"--tail"}, items)

	// After `logs <id> --tail`, completion is silent: the user must
	// supply a numeric argument.
	it, err = handler.Complete(context.Background(), extensionsREPLCommand,
		[]string{extensionsREPLCommandLogs, "alpha", "--tail", ""})
	require.NoError(t, err)
	items, err = sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	assert.Empty(t, items)
}

func TestExtensionsREPLInfoShowsLogPath(t *testing.T) {
	t.Parallel()

	runner := newTestWorkspaceRunner(t)
	require.NoError(t, runner.Run("alpha", "/bin/ext", config.NopConfig()))
	handler := extensionsREPLHandler{runner: runner}

	it, err := handler.HandleCommand(context.Background(), repl.Command{
		Name: extensionsREPLCommand,
		Args: []string{extensionsREPLCommandInfo, "alpha"},
	}, repl.NopProgressWriter())
	require.NoError(t, err)
	items, err := sdkiterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	out := extensionsResponsiveStringsWithWidth(t, items, 320)
	flattened := strings.Join(strings.Fields(strings.Join(out, "\n")), " ")
	assert.Contains(t, flattened, "Log file")
	assert.Contains(t, flattened, "rune-extension-")
}

func newTestWorkspaceRunner(t *testing.T) *workspaceRunner {
	t.Helper()
	return newTestWorkspaceRunnerWithExecutor(t, &recordingExecutor{})
}

func newTestWorkspaceRunnerWithExecutor(t *testing.T, exec schemeapi.Executor) *workspaceRunner {
	t.Helper()
	keys, err := auth.GenerateKeys()
	require.NoError(t, err)
	// Use a per-test workspace URI so per-extension log files (derived
	// from workspace+id) do not collide across parallel tests.
	uri, err := workspaceapi.ParseURI("file:///tmp/" + sanitizeTestName(t.Name()))
	require.NoError(t, err)
	runner := newWorkspaceRunner(
		exec, exec, extension.GrantAll(), nopTrustVerifier{}, uri,
		"/tmp/ext.sock", "/tmp/ext-data", "/tmp/ext-install", []byte("cert"), keys,
	)
	t.Cleanup(func() { _ = runner.Close() })
	return runner
}

func sanitizeTestName(name string) string {
	r := strings.NewReplacer("/", "_", " ", "_")
	return r.Replace(name)
}

func extensionsResponsiveStrings(t *testing.T, items []component.Responsive) []string {
	t.Helper()
	return extensionsResponsiveStringsWithWidth(t, items, 120)
}

func extensionsResponsiveStringsWithWidth(
	t *testing.T, items []component.Responsive, width int,
) []string {
	t.Helper()
	out := make([]string, 0, len(items))
	for _, item := range items {
		height := item.Height(width)
		if height <= 0 {
			height = 1
		}
		writer := term.NewStringWriter(width, height)
		item.Resize(width, height)
		item.Draw(writer)
		require.NoError(t, writer.Flush())
		out = append(out, strings.TrimRight(writer.String(), " \n"))
	}
	return out
}
