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

package starlarktutorial

import (
	"context"
	"errors"
	"fmt"

	"go.starlark.net/starlark"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// builtins returns the predeclared globals exposed to a tutorial
// script: the tutorial() registration, blocking-UI builtins, side-
// effect builtins, level constants, and small helpers like exit().
func builtins(t *Tutorial) starlark.StringDict {
	return starlark.StringDict{
		// Registration + helpers.
		"tutorial":       starlark.NewBuiltin("tutorial", builtinTutorial(t)),
		"command_key":    starlark.NewBuiltin("command_key", builtinCommandKey(t)),
		"editor_mode":    starlark.NewBuiltin("editor_mode", builtinEditorMode(t)),
		"os":             starlark.NewBuiltin("os", builtinOS(t)),
		"config_path":    starlark.NewBuiltin("config_path", builtinConfigPath(t)),
		"key_for":        starlark.NewBuiltin("key_for", builtinKeyFor(t)),
		"command_exists": starlark.NewBuiltin("command_exists", builtinCommandExists(t)),
		"workspace_open": starlark.NewBuiltin("workspace_open", builtinWorkspaceOpen(t)),
		"is_lsp_server_running": starlark.NewBuiltin(
			"is_lsp_server_running", builtinLSPServerRunning(t)),
		"exit":              starlark.NewBuiltin("exit", builtinExit()),
		"cancel_on_dismiss": starlark.NewBuiltin("cancel_on_dismiss", builtinCancelOnDismiss()),

		// Notification level constants.
		"error":   starlark.MakeInt(int(browserapi.LevelError)),
		"warn":    starlark.MakeInt(int(browserapi.LevelWarn)),
		"info":    starlark.MakeInt(int(browserapi.LevelInfo)),
		"success": starlark.MakeInt(int(browserapi.LevelSuccess)),

		// Blocking UI builtins: each one is a step of the lesson
		// that ends when its milestone is met.
		"wait_command": starlark.NewBuiltin("wait_command", builtinWaitCommand(t)),
		"wait_shell":   starlark.NewBuiltin("wait_shell", builtinWaitShell(t)),
		"wait_event":   starlark.NewBuiltin("wait_event", builtinWaitEvent(t)),
		"confirm":      starlark.NewBuiltin("confirm", builtinConfirm(t)),
		"choice":       starlark.NewBuiltin("choice", builtinChoice(t)),

		// Side-effect builtins.
		"notify":           starlark.NewBuiltin("notify", builtinNotify(t)),
		"open_file":        starlark.NewBuiltin("open_file", builtinOpenFile(t)),
		"highlight_window": starlark.NewBuiltin("highlight_window", builtinHighlightWindow(t)),
	}
}

func builtinTutorial(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		if t.entry != nil {
			return nil, errors.New("tutorial(): already called in this file")
		}
		var (
			id      starlark.String
			title   starlark.String
			version starlark.String
			entry   starlark.Value
		)
		if err := starlark.UnpackArgs("tutorial", args, kwargs,
			"entry", &entry,
			"id?", &id,
			"title?", &title,
			"version?", &version,
		); err != nil {
			return nil, err
		}
		fn, ok := entry.(*starlark.Function)
		if !ok {
			return nil, fmt.Errorf("tutorial(): entry must be a function, "+
				"got %s", entry.Type())
		}
		if fn.NumParams() != 0 {
			return nil, fmt.Errorf("tutorial(): entry %q must take zero "+
				"arguments (has %d)", fn.Name(), fn.NumParams())
		}
		t.id = string(id)
		t.title = string(title)
		t.version = string(version)
		t.entry = fn
		return starlark.None, nil
	}
}

func builtinWaitCommand(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var (
			command starlark.String
			title   starlark.String
			text    starlark.String
		)
		if err := starlark.UnpackArgs("wait_command", args, kwargs,
			"command", &command,
			"title?", &title,
			"text?", &text); err != nil {
			return nil, err
		}
		req := &request{
			kind:    reqWaitCommand,
			command: string(command),
			title:   string(title),
			text:    string(text),
		}
		res, err := t.publishRequest(req)
		if err != nil {
			return nil, err
		}
		return newCommandResult(res.cmdName, res.cmdArgs), nil
	}
}

func builtinWaitShell(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var (
			argList *starlark.List
			title   starlark.String
			text    starlark.String
		)
		if err := starlark.UnpackArgs("wait_shell", args, kwargs,
			"args", &argList,
			"title?", &title,
			"text?", &text); err != nil {
			return nil, err
		}
		want, err := starlarkStringList(argList, "args")
		if err != nil {
			return nil, fmt.Errorf("wait_shell: %w", err)
		}
		if len(want) == 0 {
			return nil, errors.New("wait_shell: args must be non-empty")
		}
		req := &request{
			kind:      reqWaitShell,
			shellArgs: want,
			title:     string(title),
			text:      string(text),
		}
		res, err := t.publishRequest(req)
		if err != nil {
			return nil, err
		}
		return newCommandResult(res.cmdName, res.cmdArgs), nil
	}
}

func builtinConfirm(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var message starlark.String
		if err := starlark.UnpackArgs("confirm", args, kwargs,
			"message", &message); err != nil {
			return nil, err
		}
		req := &request{
			kind:    reqConfirm,
			message: string(message),
			options: []string{"Yes", "No"},
		}
		res, err := t.publishRequest(req)
		if err != nil {
			return nil, err
		}
		return starlark.Bool(res.confirmed), nil
	}
}

// builtinWaitEvent arms a step that resolves only when the host
// observes an editor event whose type name matches event. It never
// resolves from keystrokes, so every key reaches the IDE root while
// the hint stays up. The event name is not validated against a fixed
// set: an unknown name simply never matches.
//
// The optional uri kwarg narrows the match to events whose URI
// contains it as a substring, so a step can await a write to one
// specific file rather than any buffer flush.
func builtinWaitEvent(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var (
			event starlark.String
			uri   starlark.String
			text  starlark.String
			title starlark.String
		)
		if err := starlark.UnpackArgs("wait_event", args, kwargs,
			"event", &event,
			"uri?", &uri,
			"text?", &text,
			"title?", &title); err != nil {
			return nil, err
		}
		req := &request{
			kind:     reqWaitEvent,
			event:    string(event),
			eventURI: string(uri),
			text:     string(text),
			title:    string(title),
		}
		if _, err := t.publishRequest(req); err != nil {
			return nil, err
		}
		return starlark.None, nil
	}
}

func builtinChoice(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var (
			message starlark.String
			options *starlark.List
		)
		if err := starlark.UnpackArgs("choice", args, kwargs,
			"message", &message,
			"options", &options); err != nil {
			return nil, err
		}
		opts, err := starlarkStringList(options, "options")
		if err != nil {
			return nil, fmt.Errorf("choice: %w", err)
		}
		if len(opts) == 0 {
			return nil, errors.New("choice: options must be non-empty")
		}
		req := &request{
			kind:    reqChoice,
			message: string(message),
			options: opts,
		}
		res, err := t.publishRequest(req)
		if err != nil {
			return nil, err
		}
		if !res.selected {
			return newChoiceResult(-1, "", false), nil
		}
		return newChoiceResult(res.selectedIdx, res.selectedValue, true), nil
	}
}

func builtinNotify(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var (
			message starlark.String
			level   starlark.Int
		)
		if err := starlark.UnpackArgs("notify", args, kwargs,
			"message", &message,
			"level?", &level); err != nil {
			return nil, err
		}
		lvl, _ := level.Int64()
		t.runOnTUI(func() {
			if t.notifications != nil {
				_, _ = t.notifications.Notify(
					browserapi.NotificationLevel(lvl), "%s", string(message))
			}
		})
		return starlark.None, nil
	}
}

func builtinOpenFile(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var uri starlark.String
		if err := starlark.UnpackArgs("open_file", args, kwargs,
			"uri", &uri); err != nil {
			return nil, err
		}
		var openErr error
		t.runOnTUI(func() {
			if t.editor == nil {
				openErr = errors.New("no focused editor")
				return
			}
			parsed, err := workspaceapi.ParseURI(string(uri))
			if err != nil {
				parsed, err = workspaceapi.CurrentUserHostURI(string(uri))
				if err != nil {
					openErr = fmt.Errorf("parse uri %q: %w", string(uri), err)
					return
				}
			}
			if _, err := t.editor.Edit(context.Background(), parsed, nil,
				false, false); err != nil {
				openErr = err
			}
		})
		if openErr != nil {
			t.runOnTUI(func() {
				if t.notifications != nil {
					_, _ = t.notifications.Notify(browserapi.LevelError,
						"%s: %v", t.Title(), openErr)
				}
			})
		}
		return starlark.None, nil
	}
}

func builtinHighlightWindow(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var anchor starlark.String
		if err := starlark.UnpackArgs("highlight_window", args, kwargs,
			"anchor", &anchor); err != nil {
			return nil, err
		}
		t.runOnTUI(func() {
			if t.notifications == nil {
				return
			}
			_, _ = t.notifications.Notify(browserapi.LevelInfo,
				"tutorial: highlight_window(%q) — geometry deferred",
				string(anchor))
		})
		return starlark.None, nil
	}
}

// builtinExit raises errExitRequested so the entry function can
// terminate from any depth without explicit returns at every level.
func builtinExit() func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(*starlark.Thread, *starlark.Builtin,
		starlark.Tuple, []starlark.Tuple,
	) (starlark.Value, error) {
		return nil, errExitRequested
	}
}

// builtinCancelOnDismiss exits the tutorial when result.selected is
// False; otherwise it returns the result unchanged.
func builtinCancelOnDismiss() func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var result starlark.Value
		if err := starlark.UnpackArgs("cancel_on_dismiss", args, kwargs,
			"result", &result); err != nil {
			return nil, err
		}
		ha, ok := result.(starlark.HasAttrs)
		if !ok {
			return nil, fmt.Errorf("cancel_on_dismiss: argument must be a "+
				"choice_result or confirm result, got %s", result.Type())
		}
		sel, err := ha.Attr("selected")
		if err != nil || sel == nil {
			return nil, fmt.Errorf("cancel_on_dismiss: argument has no " +
				"`selected` attribute")
		}
		if b, ok := sel.(starlark.Bool); ok && !bool(b) {
			return nil, errExitRequested
		}
		return result, nil
	}
}

// runOnTUI runs fn synchronously: in production via the host's
// scheduleNextTick (so the work lands on the TUI loop and runOnTUI
// blocks until it completes), or inline when scheduleNextTick is nil.
//
// If the run context is cancelled while runOnTUI is waiting for the
// scheduled callback, it stops waiting and returns. Stop cancels the
// context and then blocks on the run goroutine; without this, a
// finalizing runOnTUI (from handleRunResult) would wait forever for an
// event-loop tick that the Stop-wedged loop can never deliver. The
// scheduled callback may still run later on its own; that is harmless.
func (t *Tutorial) runOnTUI(fn func()) {
	t.mu.Lock()
	sched := t.scheduleNextTick
	ctx := t.runCtx
	signal := t.firstSignal
	t.firstSignal = nil
	t.mu.Unlock()
	if signal != nil {
		signal()
	}
	if sched == nil {
		fn()
		return
	}
	done := make(chan struct{})
	if !sched(func() {
		fn()
		close(done)
	}) {
		fn()
		return
	}
	if ctx == nil {
		<-done
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func starlarkStringList(list *starlark.List, key string) ([]string, error) {
	if list == nil {
		return nil, fmt.Errorf("missing required param %q", key)
	}
	out := make([]string, 0, list.Len())
	iter := list.Iterate()
	defer iter.Done()
	var item starlark.Value
	for iter.Next(&item) {
		s, ok := item.(starlark.String)
		if !ok {
			return nil, fmt.Errorf("param %q[]: want string, got %s",
				key, item.Type())
		}
		out = append(out, string(s))
	}
	return out, nil
}

// parseKeyList converts a Starlark list of key combo strings (e.g.
// "<meta-1>", "<c-s-tab>", "q") into KeyCombs. A nil list returns
// nil so callers can treat absence as "no entries" without an extra
// check. The kwarg name is used in error messages so authors can
// spot which kwarg owns a bad string.
func builtinCommandKey(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		_ starlark.Tuple, _ []starlark.Tuple,
	) (starlark.Value, error) {
		return starlark.String(t.commandKeyDisplay), nil
	}
}

// builtinEditorMode implements editor_mode(): it returns the user's
// resolved editor mode, "vim", "helix", "standard", or "emacs" (exo
// is resolved to its fallback by the host before the tutorial runs).
func builtinEditorMode(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		_ starlark.Tuple, _ []starlark.Tuple,
	) (starlark.Value, error) {
		return starlark.String(t.editorMode), nil
	}
}

// builtinOS implements os(): it returns the host operating system
// (runtime.GOOS, e.g. "darwin", "linux"). Tutorials branch on it to
// teach OS-specific flows such as the native macOS menu bar.
func builtinOS(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		_ starlark.Tuple, _ []starlark.Tuple,
	) (starlark.Value, error) {
		return starlark.String(t.os), nil
	}
}

// builtinConfigPath implements config_path(): it returns the file the
// running Rune reads its user configuration from, or "" when the host
// wired none. The path follows the data directory, so copy that names
// it stays right under `rune -d`.
func builtinConfigPath(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, _ *starlark.Builtin,
		_ starlark.Tuple, _ []starlark.Tuple,
	) (starlark.Value, error) {
		return starlark.String(t.configPath), nil
	}
}

// builtinKeyFor implements key_for(command, *args): it returns the
// user's configured key spec bound to command (with optional args), or
// "" when unbound or when no lookup func is wired.
func builtinKeyFor(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, b *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		if len(kwargs) != 0 {
			return nil, fmt.Errorf("%s: unexpected keyword arguments",
				b.Name())
		}
		if len(args) == 0 {
			return nil, fmt.Errorf("%s: missing command argument", b.Name())
		}
		cmd, ok := starlark.AsString(args[0])
		if !ok {
			return nil, fmt.Errorf("%s: command must be a string, got %s",
				b.Name(), args[0].Type())
		}
		var cmdArgs []string
		for _, a := range args[1:] {
			s, ok := starlark.AsString(a)
			if !ok {
				return nil, fmt.Errorf("%s: args must be strings, got %s",
					b.Name(), a.Type())
			}
			cmdArgs = append(cmdArgs, s)
		}
		if t.keyForCommand == nil {
			return starlark.String(""), nil
		}
		return starlark.String(t.keyForCommand(cmd, cmdArgs)), nil
	}
}

// builtinCommandExists implements command_exists(command): it reports
// whether the named command is registered right now. Tutorials use it
// to branch on package-provided commands, so the lookup must stay live
// across a `pkg install` performed mid-tutorial. It returns True when
// no lookup func is wired so tutorials/tests without the wiring are
// not spuriously blocked.
func builtinCommandExists(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, b *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		var name starlark.String
		if err := starlark.UnpackArgs(b.Name(), args, kwargs,
			"command", &name); err != nil {
			return nil, err
		}
		if t.commandManualLookup == nil {
			return starlark.True, nil
		}
		_, ok := t.commandManualLookup(string(name))
		return starlark.Bool(ok), nil
	}
}

// builtinWorkspaceOpen implements workspace_open(): it reports whether
// a project workspace is open in the focused slot. It returns True
// when no lookup func is wired so tutorials/tests without the wiring
// are not spuriously blocked.
func builtinWorkspaceOpen(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, b *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		if err := starlark.UnpackArgs(b.Name(), args, kwargs); err != nil {
			return nil, err
		}
		return starlark.Bool(t.workspaceOpen == nil || t.workspaceOpen()), nil
	}
}

// builtinLSPServerRunning implements is_lsp_server_running(): it reports
// whether the focused workspace has at least one live, initialized LSP
// server. It returns True when no lookup func is wired so tutorials/tests
// without the wiring are not spuriously blocked.
func builtinLSPServerRunning(t *Tutorial) func(*starlark.Thread, *starlark.Builtin,
	starlark.Tuple, []starlark.Tuple,
) (starlark.Value, error) {
	return func(_ *starlark.Thread, b *starlark.Builtin,
		args starlark.Tuple, kwargs []starlark.Tuple,
	) (starlark.Value, error) {
		if err := starlark.UnpackArgs(b.Name(), args, kwargs); err != nil {
			return nil, err
		}
		return starlark.Bool(t.lspServerRunning == nil || t.lspServerRunning()), nil
	}
}
