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
	"sort"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/ide/console/ideconsole/workspaceshell"
)

// extensionsProcessSubcommand is the sub-name under "extensions" that
// routes to the embedded workspaceshell.Executor tracking extension
// binaries. It used to live as a sibling top-level command named
// "extensions-process"; folding it under "extensions" keeps the
// user-facing CLI for a single concern in one namespace while leaving
// the two underlying trackers (workspace vs. extensions) intact —
// they wrap different scheme executors by design and cannot be merged
// without breaking the local-vs-remote invariant.
const extensionsProcessSubcommand = "process"

// extensionsREPLCommandName mirrors the top-level REPL command name
// registered by extensionv2.registerExtensionsREPLCommand. Duplicated
// here so the ide package can recognize the entry when iterating
// text.Component.REPLCommands() without taking a dependency on
// extensionv2.
const extensionsREPLCommandName = "extensions"

// extensionsREPLWithProcess intercepts "extensions process …" and
// dispatches it to the workspaceshell.Executor tracking extension
// binaries; all other "extensions <sub> …" invocations pass straight
// through to the underlying textapi.REPLHandler registered by
// extensionv2's workspace runner.
//
// The wrapper exists in the ide package — and not inside extensionv2
// — because the tracker is an IDE-side concern: it is constructed
// alongside the local fileScheme that backs extension processes, and
// it backs the IDE shell's "process" UI. Pushing it into extensionv2
// would couple the extension runner to ideconsole/workspaceshell.
type extensionsREPLWithProcess struct {
	underlying textapi.REPLHandler
	proc       *workspaceshell.Executor
}

var _ textapi.REPLHandler = extensionsREPLWithProcess{}

// HandleCommand routes "extensions process …" to the executor and
// forwards every other invocation to the underlying handler.
func (w extensionsREPLWithProcess) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) > 0 && cmd.Args[0] == extensionsProcessSubcommand {
		return w.proc.HandleCommand(ctx, repl.Command{
			Name: extensionsProcessSubcommand,
			Args: cmd.Args[1:],
		}, pw)
	}
	return w.underlying.HandleCommand(ctx, cmd, pw)
}

// Complete merges the executor's completion candidates into the
// underlying handler's view. The merge happens only when the user is
// typing the first argument; once "process" has been chosen, all
// further arguments are completed by the executor alone so PID
// completion still works the same way it did under the old top-level
// "extensions-process" command.
func (w extensionsREPLWithProcess) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) > 0 && args[0] == extensionsProcessSubcommand {
		return w.proc.Complete(ctx, extensionsProcessSubcommand, args[1:])
	}
	if len(args) == 1 {
		// First-arg completion: surface "process" alongside whatever
		// the underlying handler offers so users can discover the
		// nested command via tab completion.
		inner, err := w.underlying.Complete(ctx, cmd, args)
		if err != nil {
			return nil, err
		}
		defer func() { _ = inner.Close() }()
		got, err := iterator.ToSlice(ctx, inner)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(extensionsProcessSubcommand, args[0]) {
			got = append(got, extensionsProcessSubcommand)
		}
		sort.Strings(got)
		return iterator.FromSlice(got), nil
	}
	return w.underlying.Complete(ctx, cmd, args)
}

// Help routes "help extensions process …" to the executor so it can
// document the nested subcommands. Other paths fall through.
func (w extensionsREPLWithProcess) Help(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) > 0 && args[0] == extensionsProcessSubcommand {
		return w.proc.Help(ctx, args[1:])
	}
	return w.underlying.Help(ctx, args)
}

// extensionsProcessManual is the textapi.CommandManual entry for
// "extensions process" appended to the manuals returned by
// extensionv2 when the wrapper is installed.
func extensionsProcessManual() textapi.CommandManual {
	return textapi.CommandManual{
		Name:    extensionsProcessSubcommand,
		Summary: "Manage processes spawned by extensions.",
		Commands: []textapi.CommandManual{
			{Name: "status", Summary: "List running extension processes."},
			{Name: "audit", Summary: "List all extension processes (including exited)."},
			{Name: "tree", Summary: "Show extension process tree (parent→child)."},
			{Name: "info", Summary: "Show detailed process information.", Synopsis: "<pid>"},
			{Name: "signal", Summary: "Send a signal to an extension process.", Synopsis: "<pid> [<N>]"},
			{Name: "stop", Summary: "Gracefully stop an extension process.", Synopsis: "<pid>"},
		},
	}
}
