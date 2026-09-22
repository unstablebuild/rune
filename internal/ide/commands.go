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
	"errors"
	"fmt"
	"strconv"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/workspace"
)

const (
	cmdClipboardPaste = "clipboardpaste"
)

type commandAll struct {
	man       textapi.CommandManual
	handler   func(*ex, context.Context, ...string) error
	completer func(e *ex, ctx context.Context, cmd textapi.Command,
	) (iterator.Iterator[string], string, error)
}

var (
	exCommands = map[string]commandAll{
		"keydump": {
			man: textapi.CommandManual{
				Summary:  "Print the format of key combinations.",
				Synopsis: "",
			},
			handler: (*ex).keydump,
		},
		"cheatsheet": {
			man: textapi.CommandManual{
				Summary: "Open a keys-first cheatsheet rendered from your current key bindings.",
			},
			handler: (*ex).cheatsheet,
		},
		"keybindings": {
			man: textapi.CommandManual{
				Summary: "List your currently configured key bindings and the commands they run.",
			},
			handler: (*ex).keybindings,
		},
		"gitshow": {
			man: textapi.CommandManual{
				Summary: "Show the focused file's diff against HEAD in a floating window, " +
					"opened at the hunk nearest the cursor. The file must have no unsaved " +
					"changes, since the diff describes what is on disk.",
			},
			handler: (*ex).gitshow,
		},
		"history": {
			man: textapi.CommandManual{
				Summary: "Open the command prompt showing previously executed commands.",
			},
			handler: (*ex).openCommandHistoryPrompt,
		},
		"tabrename": {
			man: textapi.CommandManual{
				Summary:  "Rename the current tab in focus. Optionally set the colors of the tab title.",
				Synopsis: "<name> [<foreground> [<background>]]",
			},
			handler: (*ex).tabrename,
		},
		"echo": {
			man: textapi.CommandManual{
				Summary: "Replay the given sequence of keys back into the event loop " +
					"as if the user had dispatched them. This allows for building macros that " +
					"perform tasks that couldn't be accomplished with " +
					"combinations of commands alone. For instance, `echo :edit` opens " +
					"the command prompt with a pre-populated command. The syntax of " +
					"non-character keys is the same as in the `key_bindings` section " +
					"of the config. There is a special {wait} instruction that can be " +
					"interleaved to deterministically wait for the command prompt " +
					"auto-completer to finish populating the search list before " +
					"processing the next key. The `{prompt}` instruction can be used to " +
					"open the command prompt in a content-agnostic way.",
				Synopsis: "<sequence>",
			},
			handler: (*ex).echo,
		},
		"tabprevious": {
			man: textapi.CommandManual{
				Summary: "Set the content of the current active window to the previous tab in the tabs list. " +
					"Wraps around to the end of the tabs list.",
			},
			handler: (*ex).tabprevious,
		},
		"tabnext": {
			man: textapi.CommandManual{
				Summary: "Set the content of the current active window to the next tab in the tabs list. " +
					"Wraps around to the start of the tabs list.",
			},
			handler: (*ex).tabnext,
		},
		"tabclose": {
			man: textapi.CommandManual{
				Summary: "Close the current active window's tab. It is automatically replaced " +
					"with the next available tab in the tabs list.",
			},
			handler: (*ex).tabclose,
		},
		"tabfocus": {
			man: textapi.CommandManual{
				Summary: "Set the content of the current active window to " +
					"the tab at the given position in the tabs list.",
				Synopsis: "[<position>]",
			},
			handler: (*ex).tabfocus,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				var tabNames []string
				if len(cmd.Args) <= 1 {
					for i, tab := range e.comp.Browser().Tabs() {
						pretty := strconv.Itoa(i + 1)
						name, _, ok := e.comp.Browser().TabName(tab.URI())
						if ok {
							pretty += " " + name
						}
						tabNames = append(tabNames, pretty)
					}
				}
				if len(tabNames) == 0 {
					// return an iterator with an empty slice
					// so command prompt won't use history as auto-complete.
					return iterator.FromSlice([]string{""}), "", nil
				}
				return iterator.FromSlice(tabNames), "", nil
			},
		},
		"tabcloseall": {
			man: textapi.CommandManual{
				Summary: "Close all tabs in the tabs list.",
			},
			handler: (*ex).tabcloseall,
		},
		"tabcloseinactive": {
			man: textapi.CommandManual{
				Summary: "Close all tabs that are not currently displayed by any window.",
			},
			handler: (*ex).tabcloseinactive,
		},
		"tabmove": {
			man: textapi.CommandManual{
				Summary: "Move the tab in focus in the given direction within the tabs list, " +
					"or to an absolute position if a number is passed.",
				Synopsis: "(right|left|1|2|3|4|5|6|7|8|9)",
			},
			handler: (*ex).moveTab,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				if len(cmd.Args) <= 1 {
					options := []string{"left", "right"}
					var i int
					for i = range e.comp.Browser().Tabs() {
						options = append(options, strconv.Itoa(i+1))
					}
					return iterator.FromSlice(options), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"windowconverttab": {
			man: textapi.CommandManual{
				Summary: "Convert the content of the window in focus into a tab. " +
					"If the current window is a floating window, it is closed.",
				Synopsis: "<name> [<icon>]",
			},
			handler: (*ex).convertTab,
		},
		"windowclose": {
			man: textapi.CommandManual{
				Summary: "Close the current active window and switch focus " +
					"to the next available window. This command fails if there is only one " +
					"window remaining.",
			},
			handler: (*ex).closeFocusWindow,
		},
		"windowcloseall": {
			man: textapi.CommandManual{
				Summary: "Close all windows except the current active window. " +
					"This command fails if there is only one window remaining.",
			},
			handler: (*ex).windowcloseall,
		},
		"writequit": {
			man: textapi.CommandManual{
				Summary: "Write the current file to disk and exit, but only if " +
					"there are no files with unsaved changes pending to be written to disk.",
			},
			handler: (*ex).flushClose,
		},
		"writeforcequit!": {
			man: textapi.CommandManual{
				Summary: "Write the current file to disk and exit. If there are other files with " +
					"unsaved changes, those changes are ignored and stashed away.",
			},
			handler: (*ex).flushCloseIgnoreNonFlushed,
		},
		"write": {
			man: textapi.CommandManual{
				Summary: "Write the current file to disk, including any pending changes. " +
					"This is the standard way to save changes to a file. It fails if the file was " +
					"opened read-only or if there is another reason the file cannot be written.",
			},
			handler: (*ex).flush,
		},
		"writeall": {
			man: textapi.CommandManual{
				Summary: "Like `write` but applies to all open tabs.",
			},
			handler: (*ex).flushAll,
		},
		"write!": {
			man: textapi.CommandManual{
				Summary: "Like `write` but forcefully writes even when a file is opened in read-only mode " +
					"or there is another reason the file cannot be written.",
			},
			handler: (*ex).forceFlush,
		},
		"writeall!": {
			man: textapi.CommandManual{
				Summary: "Like `write!` but applies to all open tabs.",
			},
			handler: (*ex).forceFlushAll,
		},
		"forcequit!": {
			man: textapi.CommandManual{
				Summary: "Exit without writing any pending changes to disk.",
			},
			handler: (*ex).forcequit,
		},
		"quit": {
			man: textapi.CommandManual{
				Summary: "Exit, but only if there are no files with unsaved changes pending to be " +
					"written to disk.",
			},
			handler: (*ex).quit,
		},
		"reloadfile!": {
			man: textapi.CommandManual{
				Summary: "Reload the file in the current active window from disk, if it is a workspace file.",
			},
			handler: (*ex).reloadfile,
		},
		"windowdefaultsplit": {
			man: textapi.CommandManual{
				Summary: "Toggle the default split orientation, or set it to the " +
					"given orientation if passed via arguments. The options are `horizontal`, which " +
					"places the next split below the current active window, or `vertical`, which " +
					"places the next split to the right of the current active window.",
				Synopsis: "(horizontal|vertical)",
			},
			handler: (*ex).splitDirectionChange,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				if len(cmd.Args) <= 1 {
					return iterator.FromSlice([]string{"horizontal", "vertical"}), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"windowsplit": manSplitWindow,
		"windownew":   manSplitWindow,
		"windowfocus": {
			man: textapi.CommandManual{
				Summary: "Switch focus to the window on the given side of the current active window, " +
					"or back to the previously focused window with `other`.",
				Synopsis: "(right|left|up|down|other)",
			},
			handler: (*ex).windowfocus,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				if len(cmd.Args) <= 1 {
					return iterator.FromSlice([]string{"right", "left", "up", "down", "other"}), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"windowmove": {
			man: textapi.CommandManual{
				Summary:  "Move the content of the window in focus to the window in the given direction.",
				Synopsis: "(right|left|up|down)",
			},
			handler:   (*ex).moveWindow,
			completer: completeWithArrows,
		},
		"windowresize": {
			man: textapi.CommandManual{
				Summary:  "Resize the current window by increasing or decreasing its width or height.",
				Synopsis: "(increase|decrease|max|min|reset) (height|width)",
			},
			handler: (*ex).windowresize,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				if len(cmd.Args) == 1 {
					return iterator.FromSlice([]string{"increase", "decrease", "reset", "max", "min"}), "", nil
				}
				if len(cmd.Args) == 2 {
					return iterator.FromSlice([]string{"width", "height"}), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"windowtogglemaximize": {
			man: textapi.CommandManual{
				Summary: "Toggle version of `windowresize max height+width`. " +
					"A subsequent invocation of this command will reset the " +
					"window size via `windowresize reset`. Shifting focus to another " +
					"window also resets the size of the maximized window.",
			},
			handler: (*ex).windowtogglemaximize,
		},
		"notificationinfo": {
			man: textapi.CommandManual{
				Summary:  "Send an info-level notification.",
				Synopsis: "<message>",
			},
			handler: (*ex).sendNotificationInfo,
		},
		"notificationsuccess": {
			man: textapi.CommandManual{
				Summary:  "Send a success-level notification.",
				Synopsis: "<message>",
			},
			handler: (*ex).sendNotificationSuccess,
		},
		"notificationwarning": {
			man: textapi.CommandManual{
				Summary:  "Send a warning-level notification.",
				Synopsis: "<message>",
			},
			handler: (*ex).sendNotificationWarning,
		},
		"notificationerror": {
			man: textapi.CommandManual{
				Summary:  "Send an error-level notification.",
				Synopsis: "<message>",
			},
			handler: (*ex).sendNotificationError,
		},
		"notificationcloseall": {
			man: textapi.CommandManual{
				Summary: "Close all active notifications rendered by the browser.",
			},
			handler: (*ex).closeNotifications,
		},
		"notificationpauseall": {
			man: textapi.CommandManual{
				Summary: "Pause automatic closure of all active notifications rendered by the browser.",
			},
			handler: (*ex).pauseNotifications,
		},
		"notificationresumeall": {
			man: textapi.CommandManual{
				Summary: "Resume automatic closure of all previously paused notifications rendered by the browser.",
			},
			handler: (*ex).resumeNotifications,
		},
		"terminalnewtab": {
			man: textapi.CommandManual{
				Summary: "Open a new terminal emulator in a new tab and attach it to the current " +
					"active window. If a `shell` argument is provided, it is used as the " +
					"command line for the new terminal (the first token is the executable, " +
					"the rest are forwarded as arguments). Otherwise the default configured " +
					"in `terminal.shell` is used; if that is unset, the system shell defined " +
					"via the SHELL environment variable is used.",
				Synopsis: "[<shell>]",
			},
			handler: (*ex).terminalnewtab,
		},
		"console": {
			man: textapi.CommandManual{
				Summary: "Open a new Rune console in a durable tab and route commands through registered REPL handlers. " +
					"If arguments are provided they are submitted as a command line on the console prompt; " +
					"any in-flight command in the existing console is interrupted with <ctrl-c> first.",
				Synopsis: "[<command> [<args>...]]",
			},
			handler: (*ex).consolenewtab,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				return e.completeConsole(ctx, cmd)
			},
		},
		"terminalnew": {
			man: textapi.CommandManual{
				Summary: "Open a new terminal emulator and attach it to the current " +
					"active window. The terminal created by this command is automatically " +
					"closed when the content of the window is replaced, for example by " +
					"`tabnext` or `tabprevious`. If a `shell` argument is provided, it is " +
					"used as the command line for the new terminal (the first token is the " +
					"executable, the rest are forwarded as arguments). Otherwise the default " +
					"configured in `terminal.shell` is used; if that is unset, the system " +
					"shell defined via the SHELL environment variable is used.",
				Synopsis: "[<shell>]",
			},
			handler: (*ex).terminalnew,
		},
		"terminalneworsplit": {
			man: textapi.CommandManual{
				Summary: "Open a new terminal emulator and attach it to the " +
					"current active window if it is empty, or create a new split window if " +
					"the window is not empty. The terminal created by this " +
					"command is automatically closed when the content of the " +
					"window is replaced, for example by " +
					"`tabnext` or `tabprevious`. If a `shell` argument is provided, it is " +
					"used as the command line for the new terminal (the first token is the " +
					"executable, the rest are forwarded as arguments). Otherwise the default " +
					"configured in `terminal.shell` is used; if that is unset, the system " +
					"shell defined via the SHELL environment variable is used.",
				Synopsis: "[<shell>]",
			},
			handler: (*ex).terminalneworsplit,
		},
		"terminalsave": {
			man: textapi.CommandManual{
				Summary: "Save the current terminal buffer and scrollback under the given session name. " +
					"The saved session stores terminal output for later recovery, but does not preserve " +
					"or resume the live pty process.",
				Synopsis: "<session-name>",
			},
			handler: (*ex).terminalsave,
		},
		"terminalresume": {
			man: textapi.CommandManual{
				Summary: "Open a previously saved terminal session by name in a functioning " +
					"terminal, restoring its saved output and scrollback. This starts a new " +
					"pty process; it does not resume the original process.",
				Synopsis: "<session-name>",
			},
			handler: (*ex).terminalresume,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				return e.completeTerminalSessions(ctx, cmd)
			},
		},
		"edit": {
			man: textapi.CommandManual{
				Summary: "Open the file at the given URI for editing in the current active " +
					"window, replacing its contents. If no scheme is provided, file:// " +
					"is used by default. This allows opening files in " +
					"workspaces outside the current workspace or on a different host. " +
					"A " + workspace.SwapFileExtensionName +
					" file is created in the directory named by editor.swap_dir to " +
					"prevent multiple sessions from overwriting each other's changes. " +
					"If the file has any pending changes that were lost due to a crash, or " +
					"another session is currently editing the file, a prompt is shown " +
					"to resolve the conflict.",
				Synopsis: "[<scheme>:][//[<userinfo>@]<host>][/]<filepath>",
			},
			handler: (*ex).editFiles,
			completer: func(
				e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				e.log(log.DebugLevel, "complete command: %v", cmd)
				return e.filepathCompleter.Complete(ctx, cmd.Args)
			},
		},
		"view": {
			man: textapi.CommandManual{
				Summary:  "Like `edit` but opens the file in read-only mode.",
				Synopsis: "[<scheme>:][//[<userinfo>@]<host>][/]<filepath>",
			},
			handler: (*ex).viewFiles,
			completer: func(
				e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				return e.filepathCompleter.Complete(ctx, cmd.Args)
			},
		},
		"tabcopypath": {
			man: textapi.CommandManual{
				Summary:  "Copy the path of the file in focus to the clipboard.",
				Synopsis: "[<absolute>]",
			},
			handler: (*ex).tabcopypath,
			completer: func(
				e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				if len(cmd.Args) <= 1 {
					return iterator.FromSlice([]string{"absolute"}), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"tabcopylocation": {
			man: textapi.CommandManual{
				Summary:  "Copy the location of the file in focus to the clipboard.",
				Synopsis: "[<absolute>]",
			},
			handler: (*ex).tabcopylocation,
			completer: func(
				e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				if len(cmd.Args) <= 1 {
					return iterator.FromSlice([]string{"absolute"}), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"!": {
			man: textapi.CommandManual{
				Summary: "Open a new plugin terminal with the given executable " +
					"and arguments in a new floating window. " +
					"The stdout and stderr of the program " +
					"are printed in the window along with stats and a progress indicator until " +
					"the user closes the window or presses the ESC key. \n\n" +
					"If no executable is passed, this command opens the companion terminal emulator. " +
					"The companion terminal emulator differs " +
					"from a terminal emulator created by `terminalnewtab` in that it preserves " +
					"the session output across invocations. The floating window created by " +
					"this command can be closed via standard window or tab close commands.",
				Synopsis: "[<executable> [<args>]]",
			},
			handler: (*ex).executePlugin,
		},
		"!!": {
			man: textapi.CommandManual{
				Summary: "Run an executable like `!` but the stdout and stderr " +
					"are not rendered in a floating window. This is useful for " +
					"running programs that do not produce useful output.",
				Synopsis: "[<executable> [<args>]]",
			},
			handler: (*ex).executePluginWait,
		},
		"clipboardcopy": {
			man: textapi.CommandManual{
				Summary: "Copy the selected text into the configured clipboard.",
			},
			handler: (*ex).copyToClipboard,
		},
		cmdClipboardPaste: {
			man: textapi.CommandManual{
				Summary: "Paste the last text copied from the configured clipboard.",
			},
			handler: (*ex).pasteFromClipboard,
		},
		"setcolor": {
			man: textapi.CommandManual{
				Summary: "Change the default background and optionally foreground colors of " +
					"the content in focus. The color can be a named color or an RGB value " +
					"in hexadecimal notation (e.g. #FFFFFF).",
				Synopsis: "<background> [<foreground>]",
			},
			handler: (*ex).defaultcolors,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				var colorNames []string
				for name := range term.GetColorNames() {
					colorNames = append(colorNames, name)
				}
				return iterator.FromSlice(colorNames), "", nil
			},
		},
		"readfile": {
			man: textapi.CommandManual{
				Summary: "Insert the contents of the given file below the cursor. Takes a " +
					"URI with a scheme as an argument. If no scheme is provided, file:// is assumed.",
				Synopsis: "[<scheme>:][//[<userinfo>@]<host>][/]<filepath>",
			},
			handler: (*ex).readfile,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				return e.completeReadFile(ctx, cmd.Args)
			},
		},
		"tasknew": {
			man: textapi.CommandManual{
				Summary: "Create a task that runs the given command in response to changes " +
					"in the workspace. The filter argument can be used to " +
					"pass a glob pattern that watches only matching files. " +
					"A task can be minimized by pressing <esc>; `alignment` determines " +
					"which side is used to display the minimized task. " +
					"The name argument will appear in `tasklist` to help manage running tasks.",
				Synopsis: "<name> <alignment> [<filter>] -- <cmd> [<args>]",
			},
			handler: (*ex).newTask,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				if len(cmd.Args) == 2 {
					return iterator.FromSlice([]string{"left", "right"}), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"tasknewtab": {
			man: textapi.CommandManual{
				Summary: "Create a task like `tasknew` but convert it into a durable tab. " +
					"This is a shortcut for calling `tasknew`, focusing on the minimized " +
					"task, and then calling `windowconverttab` to convert it into a tab.",
				Synopsis: "<name> [<filter>] -- <cmd> [<args>]",
			},
			handler: (*ex).newTaskTab,
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"taskclose": {
			man: textapi.CommandManual{
				Summary:  "Stop a task previously created via `tasknew`.",
				Synopsis: "<name>",
			},
			handler:   (*ex).stopTask,
			completer: (*ex).completeTasks,
		},
		"taskfocus": {
			man: textapi.CommandManual{
				Summary:  "Focus a task previously created via `tasknew`.",
				Synopsis: "<name>",
			},
			handler:   (*ex).focusTask,
			completer: (*ex).completeTasks,
		},
		"loglevel": {
			man: textapi.CommandManual{
				Summary:  "Update the log level, overriding the level set in the config.",
				Synopsis: "(info|debug|trace|warn|error|panic|fatal)",
			},
			handler: func(e *ex, ctx context.Context, args ...string) error {
				if len(args) != 1 {
					return errors.New("expected exactly one argument with the log level")
				}
				level, err := log.ParseLevel(args[0])
				if err != nil {
					return fmt.Errorf("parse level: %w", err)
				}
				log.SetLevel(level)
				// TODO change slog level
				return nil
			},
			completer: func(e *ex, ctx context.Context, cmd textapi.Command,
			) (iterator.Iterator[string], string, error) {
				if len(cmd.Args) <= 1 {
					return iterator.FromSlice([]string{
						"info", "debug", "trace", "warn", "error", "panic", "fatal",
					}), "", nil
				}
				return iterator.FromSlice[string](nil), "", nil
			},
		},
		"fexplorer": {
			man: textapi.CommandManual{
				Summary: "Toggle the file explorer floating window. " +
					"The file explorer is a pre-minimized floating window on the left side. " +
					"Invoking this command will un-minimize and focus the file explorer, " +
					"or minimize it back if it is already open.",
			},
			handler: (*ex).fexplorer,
		},
		"locationpicker": {
			man: textapi.CommandManual{
				Summary: "Run a program and present its stdout as a list of locations " +
					"in a floating fuzzy-search picker. Each stdout line must follow " +
					"the `path[:line[:col]]` convention. The focused entry's file is " +
					"shown in a syntax-highlighted preview pane on top of the list. " +
					"Selecting an entry opens the file in the previously focused window " +
					"at the parsed coordinates.",
				Synopsis: "<program> [<args>]",
			},
			handler: (*ex).locationpicker,
		},
	}

	// exDebugCommands are commands that intentionally crash or
	// destabilize the process and must only be registered when the
	// IDE is built or configured for debugging. The workspace
	// handler only subscribes them when ide.WithDebugCommands(true)
	// is supplied.
	exDebugCommands = map[string]commandAll{
		"panic": {
			man: textapi.CommandManual{
				Summary: "Cause the editor to panic. This is for internal " +
					"debugging purposes only and is registered only in debug builds.",
			},
			handler: (*ex).panic,
		},
		"crash": {
			man: textapi.CommandManual{
				Summary: "Trigger an unrecoverable Go runtime fatal error " +
					"(stack overflow). Unlike panic, this cannot be caught " +
					"by recover and exercises the launch-log crash report path. " +
					"Registered only in debug builds.",
			},
			handler: (*ex).crash,
		},
		"datarace": {
			man: textapi.CommandManual{
				Summary: "Deliberately provoke a Go data race on a shared " +
					"variable. When the binary is built with -race (e.g. " +
					"via `make debug`) the race detector should abort the " +
					"process and the resulting report should flow through " +
					"the launch-log crash report path. Registered only in " +
					"debug builds.",
			},
			handler: (*ex).datarace,
		},
		"heapdump": {
			man: textapi.CommandManual{
				Summary: "Write a Go runtime heap dump to a temp file " +
					"using runtime/debug.WriteHeapDump. Unlike the " +
					"pprof heap profile, the dump contains the full " +
					"object graph with per-object outgoing pointers " +
					"and is the only way to walk reverse reachability " +
					"for live objects. The dump path is reported via " +
					"a notification. Registered only in debug builds.",
				Synopsis: "[<path>]",
			},
			handler: (*ex).heapdump,
		},
		"pprof": {
			man: textapi.CommandManual{
				Summary: "Start a net/http/pprof server bound to the " +
					"given TCP address (defaults to 127.0.0.1:0 for a " +
					"random localhost port). Convenience equivalent of " +
					"sending SIGUSR1 to the process. Block and mutex " +
					"profiling are enabled as a side-effect. " +
					"Registered only in debug builds.",
				Synopsis: "[<host>:<port>]",
			},
			handler: (*ex).pprof,
		},
	}

	manSplitWindow = commandAll{
		man: textapi.CommandManual{
			Summary: "Split the current active window vertically or horizontally in two, " +
				"moving focus to the new window. " +
				"If no orientation is passed, the default split orientation is used. " +
				"See `windowdefaultsplit` for more details on how the " +
				"default orientation works.",
			Synopsis: "[right|left|up|down]",
		},
		handler:   (*ex).windownew,
		completer: completeWithArrows,
	}

	runShaderCmdManual = textapi.CommandManual{
		Name: "shaderrun",
		Summary: "Run a shader from the library of shaders. " +
			"The default duration is 1s and the default FPS is 30.",
		Synopsis: "<name> [<duration>] [<fps>]",
	}

	tutorialCmdManual = textapi.CommandManual{
		Name:     "tutorial",
		Summary:  "Start or stop interactive tutorials.",
		Synopsis: "(start <name> | stop)",
	}
)

func completeWithArrows(e *ex,
	ctx context.Context, cmd textapi.Command,
) (iterator.Iterator[string], string, error) {
	if len(cmd.Args) <= 1 {
		return iterator.FromSlice([]string{"right", "left", "up", "down"}), "", nil
	}
	return iterator.FromSlice[string](nil), "", nil
}
