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
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	sdkiterator "github.com/unstablebuild/rune-go-sdk/iterator"
)

// stubExtensionsREPLHandler records the last command it received and
// returns canned responses. Stand-in for the textapi.REPLHandler the
// extensionv2 workspace runner registers under "extensions" — keeps
// this unit scoped to the wrapper without dragging the runner in.
type stubExtensionsREPLHandler struct {
	lastCmd      repl.Command
	lastComplete struct {
		cmd  string
		args []string
	}
	lastHelpArgs []string
	completeResp []string
}

func (s *stubExtensionsREPLHandler) HandleCommand(
	_ context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (sdkiterator.Iterator[component.Responsive], error) {
	s.lastCmd = cmd
	return sdkiterator.Empty[component.Responsive](), nil
}

func (s *stubExtensionsREPLHandler) Complete(
	_ context.Context, cmd string, args []string,
) (sdkiterator.Iterator[string], error) {
	s.lastComplete.cmd = cmd
	s.lastComplete.args = args
	return sdkiterator.FromSlice(s.completeResp), nil
}

func (s *stubExtensionsREPLHandler) Help(
	_ context.Context, args []string,
) (sdkiterator.Iterator[component.Responsive], error) {
	s.lastHelpArgs = args
	return sdkiterator.Empty[component.Responsive](), nil
}

var _ textapi.REPLHandler = (*stubExtensionsREPLHandler)(nil)

func TestExtensionsREPLWithProcessRoutesProcessSubcommand(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	// Track an extension PID so "process status" has something to
	// show. We don't assert on the rendered output here — the
	// goal is just to exercise the dispatch path.
	_, err := exe.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "/bin/ext",
	})
	require.NoError(t, err)

	stub := &stubExtensionsREPLHandler{}
	wrapper := extensionsREPLWithProcess{underlying: stub, proc: exe.shell}

	iter, err := wrapper.HandleCommand(
		context.Background(),
		repl.Command{Name: "extensions", Args: []string{"process", "status"}},
		repl.NopProgressWriter(),
	)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := renderResponsives(t, iter)
	assert.NotEmpty(t, out,
		"extensions process status must render output from the "+
			"extensions tracker shell")
	assert.Equal(t, repl.Command{}, stub.lastCmd,
		"the wrapper must not forward 'process' subcommands to "+
			"the underlying extensions handler")
}

func TestExtensionsREPLWithProcessForwardsOtherSubcommands(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	stub := &stubExtensionsREPLHandler{}
	wrapper := extensionsREPLWithProcess{underlying: stub, proc: exe.shell}

	cmd := repl.Command{Name: "extensions", Args: []string{"status"}}
	iter, err := wrapper.HandleCommand(
		context.Background(), cmd, repl.NopProgressWriter(),
	)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	assert.Equal(t, cmd, stub.lastCmd,
		"non-process subcommands must reach the underlying "+
			"extensions handler unchanged")
}

func TestExtensionsREPLWithProcessCompletesProcessPids(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	pid, err := exe.StartCommand(context.Background(), workspaceapi.Cmd{
		Path: "/bin/ext",
	})
	require.NoError(t, err)

	stub := &stubExtensionsREPLHandler{}
	wrapper := extensionsREPLWithProcess{underlying: stub, proc: exe.shell}

	iter, err := wrapper.Complete(
		context.Background(), "extensions",
		[]string{"process", "info", ""},
	)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	got, err := sdkiterator.ToSlice(context.Background(), iter)
	require.NoError(t, err)
	assert.Contains(t, got, strconv.Itoa(int(pid)),
		"PID completion under 'extensions process info' must come "+
			"from the extensions tracker executor")
	assert.Empty(t, stub.lastComplete.args,
		"the wrapper must not delegate 'process' arg completion "+
			"to the underlying extensions handler")
}

func TestExtensionsREPLWithProcessCompletesProcessSubcommandName(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	stub := &stubExtensionsREPLHandler{completeResp: []string{"status", "info"}}
	wrapper := extensionsREPLWithProcess{underlying: stub, proc: exe.shell}

	iter, err := wrapper.Complete(
		context.Background(), "extensions", []string{""},
	)
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	got, err := sdkiterator.ToSlice(context.Background(), iter)
	require.NoError(t, err)
	assert.Contains(t, got, "process",
		"first-arg completion under 'extensions' must include "+
			"the new 'process' subcommand")
	assert.Contains(t, got, "status",
		"first-arg completion must still surface the underlying "+
			"extensions handler's own subcommands")
}

func TestExtensionsREPLWithProcessHelpRoutesProcess(t *testing.T) {
	t.Parallel()

	root := &recordingRoot{}
	exe := newExtensionsExecutorFromUnderlying(root)

	stub := &stubExtensionsREPLHandler{}
	wrapper := extensionsREPLWithProcess{underlying: stub, proc: exe.shell}

	iter, err := wrapper.Help(context.Background(), []string{"process"})
	require.NoError(t, err)
	defer func() { _ = iter.Close() }()

	out := renderResponsives(t, iter)
	assert.Contains(t, out, "Process management",
		"help for 'extensions process' must be served by the "+
			"workspace shell executor, not the underlying "+
			"extensions handler")
	assert.Nil(t, stub.lastHelpArgs,
		"the wrapper must not call the underlying handler's Help "+
			"when the user explicitly asked about 'process'")
}
