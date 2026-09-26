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

package main

import (
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/ide"
	"unstable.build/rune/internal/term/gui/appmenu"
	"unstable.build/rune/internal/term/gui/openpanel"
)

// appMenuCommands are the Rune commands reachable from the menu bar.
// Commands that exit the IDE must keep a key binding in every shipped
// preset: menu activation replays the bound chord, and the exit signal
// is only observable on the handler event path.
var appMenuCommands = struct {
	settings, quit []string
}{
	settings: []string{"config"},
	quit:     []string{"quit"},
}

// appMenuPanelCommands maps menu commands whose single argument is a
// path to the native open panel that collects it. Menu activation for
// these commands always shows the panel, even when the command is
// bound to a chord; the keyboard prompt flow stays reachable through
// the bindings themselves.
var appMenuPanelCommands = map[string]openpanel.Options{
	"edit":          {Multiple: true},
	"view":          {Multiple: true, Message: "Choose files to open read-only"},
	"workspaceopen": {Directories: true, Message: "Choose a project folder to open"},
}

// showOpenPanel is a seam over openpanel.Show for tests.
var showOpenPanel = openpanel.Show

// appMenuQuickMenu is the state the Quick Menu item reflects. The item
// is omitted entirely when the platform draws no quick menu or the user
// configured no buttons for it, so the menu never offers a toggle that
// cannot do anything.
type appMenuQuickMenu struct {
	available bool
	visible   bool
}

// cmdWorkspaceOpen is the command that opens a project; the Open Recent
// items dispatch it with an explicit path.
const cmdWorkspaceOpen = "workspaceopen"

// bootstrapQuitChord is the fallback for the quit accelerator. Before
// the wizard writes an editor preset the config binds nothing, and this
// is the chord the wizard itself recognizes as a clean exit.
var bootstrapQuitChord = term.KeyComb{Mod: hostAppModifier, Ch: 'q'}

// appMenuKeyBindings resolves the chords backing the menu's command
// items from the live configuration.
func appMenuKeyBindings(cfg config.Config) map[string]term.KeyComb {
	bindings := ide.CommandKeyBindings(cfg)
	if bindings == nil {
		bindings = map[string]term.KeyComb{}
	}
	quit := strings.Join(appMenuCommands.quit, " ")
	if _, ok := bindings[quit]; !ok {
		bindings[quit] = bootstrapQuitChord
	}
	return bindings
}

// quitEvent is the key event a window close request is routed to, so
// the red button and the Dock's Quit reach the same confirm-exit flow
// as the chord itself.
func quitEvent(bindings map[string]term.KeyComb) term.Event {
	key := bindings[strings.Join(appMenuCommands.quit, " ")]
	return term.Event{Type: term.EventKey, Mod: key.Mod, Key: key.Key, Ch: key.Ch}
}

// appMenus is the built-in macOS menu bar, covering the cheatsheet's
// command inventory. Command accelerators are derived from the live key
// bindings rather than hardcoded, so the menu never drifts from the
// user's configuration. Native items carry an accelerator only when the
// chord is unbound in every shipped preset: AppKit intercepts menu key
// equivalents before they reach the window, so a colliding one would
// shadow the preset binding.
func appMenus(
	bindings map[string]term.KeyComb, recents []recentEntry,
	models, tutorials []string, quickMenu appMenuQuickMenu,
) []appmenu.Menu {
	cmd := func(title string, cmdAndArgs ...string) appmenu.Command {
		return appmenu.Command{
			Title:   title,
			Command: cmdAndArgs[0],
			Args:    cmdAndArgs[1:],
			Key:     bindings[strings.Join(cmdAndArgs, " ")],
		}
	}
	// prefill opens the command prompt pre-typed with cmdAndArgs via the
	// same `echo {prompt}...` macro shape the presets bind, so the
	// accelerator lookup matches their bindings.
	prefill := func(title string, cmdAndArgs ...string) appmenu.Command {
		return cmd(title, "echo",
			"{prompt}"+strings.Join(cmdAndArgs, "<space>")+"<space>")
	}
	submenuItems := func(
		values []string, emptyTitle string, command func(string) appmenu.Command,
	) []appmenu.Item {
		if len(values) == 0 {
			return []appmenu.Item{appmenu.Command{Title: emptyTitle, Disabled: true}}
		}
		items := make([]appmenu.Item, 0, len(values))
		for _, value := range values {
			items = append(items, command(value))
		}
		return items
	}
	modelItems := submenuItems(models, "No Models Available", func(model string) appmenu.Command {
		return cmd(model, "chatmodel", model)
	})
	tutorialItems := submenuItems(
		tutorials, "No Tutorials Available", func(tutorial string) appmenu.Command {
			return cmd(tutorial, "tutorial", "start", tutorial)
		})
	effortItems := make([]appmenu.Item, 0, 8)
	for _, effort := range []struct{ label, value string }{
		{"None", "none"},
		{"Minimal", "minimal"},
		{"Low", "low"},
		{"Medium", "medium"},
		{"High", "high"},
		{"XHigh", "xhigh"},
		{"Max", "max"},
		{"Ultra", "ultra"},
	} {
		effortItems = append(effortItems, cmd(effort.label, "chateffort", effort.value))
	}
	sep := appmenu.Separator{}
	viewItems := []appmenu.Item{
		cmd("File Explorer", "fexplorer"),
		cmd("Command History", "history"),
	}
	if quickMenu.available {
		item := cmd("Quick Menu", cmdQuickMenu)
		item.Checked = quickMenu.visible
		viewItems = append([]appmenu.Item{item, sep}, viewItems...)
	}
	return []appmenu.Menu{
		{Title: "Rune", Items: []appmenu.Item{
			appmenu.Native{Title: "About Rune", Selector: "orderFrontStandardAboutPanel:"},
			sep,
			cmd("Settings…", appMenuCommands.settings...),
			sep,
			appmenu.Native{Title: "Hide Rune", Selector: "hide:"},
			cmd("Quit Rune", appMenuCommands.quit...),
		}},
		{Title: "File", Items: []appmenu.Item{
			cmd("Open File…", "edit"),
			cmd("Open Read-Only…", "view"),
			cmd("Open Project…", "workspaceopen"),
			openRecentMenu(recents),
			sep,
			cmd("Save", "write"),
			cmd("Reload File", "reloadfile!"),
			sep,
			cmd("New Tab", "tabnew"),
			cmd("Close Tab", "tabclose"),
			cmd("Next Tab", "tabnext"),
			cmd("Previous Tab", "tabprevious"),
			prefill("Focus Tab…", "tabfocus"),
			prefill("Move Tab…", "tabmove"),
		}},
		{Title: "Edit", Items: []appmenu.Item{
			cmd("Copy", "clipboardcopy"),
			cmd("Paste", "clipboardpaste"),
			sep,
			cmd("Rename Symbol…", "lsp", "rename"),
			cmd("Format File", "lsp", "format"),
			cmd("Trigger Completion", "lsp", "complete"),
		}},
		{Title: "View", Items: append(viewItems,
			prefill("Change Opacity…", "guiopacity"),
			cmd("Increase Font Size", "guifontsize", "increase"),
			cmd("Decrease Font Size", "guifontsize", "decrease"),
			sep,
			cmd("Expand Fold", "foldexpand"),
			cmd("Collapse Fold", "foldcollapse"),
			cmd("Toggle Fold", "foldtoggle"),
			cmd("Expand All Folds", "foldexpandall"),
			cmd("Collapse All Folds", "foldcollapseall"),
			cmd("Toggle All Folds", "foldtoggleall"),
			sep,
			cmd("New Window", "windownew"),
			cmd("Close Window", "windowclose"),
			cmd("Toggle Maximize", "windowtogglemaximize"),
			cmd("Convert to Tab", "windowconverttab"),
			prefill("Resize Window…", "windowresize"),
			prefill("Focus Window…", "windowfocus"),
			prefill("Move Window…", "windowmove"),
			prefill("Change Default Split…", "windowdefaultsplit"),
			sep,
			appmenu.Native{
				Title:    "Enter Full Screen",
				Selector: "toggleFullScreen:",
				Key:      term.KeyComb{Mod: term.ModCtrlMeta, Ch: 'f'},
			},
		)},
		{Title: "Find", Items: []appmenu.Item{
			cmd("Find File…", "searchfile"),
			cmd("Find in Files…", "searchtext"),
			sep,
			cmd("Find Function…", "searchfunc"),
			cmd("Find Variable…", "searchvar"),
			cmd("Find Type…", "searchtype"),
			sep,
			prefill("Jump to Function in File…", "jumptoast", "locals.scm",
				"local.definition.method|local.definition.function"),
			prefill("Jump to Variable in File…", "jumptoast", "locals.scm",
				"local.definition.var"),
			prefill("Jump to Type in File…", "jumptoast", "locals.scm",
				"local.definition.type"),
			sep,
			prefill("Find Definition…", "lsp", "definition"),
			prefill("Find Implementation…", "lsp", "implementation"),
			prefill("Find References…", "lsp", "references"),
			prefill("Find Documentation…", "lsp", "hover"),
		}},
		{Title: "Go", Items: []appmenu.Item{
			cmd("Back", "cursorhistory", "prev"),
			cmd("Forward", "cursorhistory", "next"),
			prefill("Cursor History…", "cursorhistory", "jump"),
			sep,
			cmd("Go to Definition", "lsp", "definition"),
			cmd("Show References", "lsp", "references"),
			cmd("Go to Implementation", "lsp", "implementation"),
			cmd("Show Documentation", "lsp", "hover"),
			sep,
			cmd("Next Diagnostic", "lspnextdiagnostic"),
			cmd("Previous Diagnostic", "lspprevdiagnostic"),
			cmd("Next Git Change", "gitnextchange"),
			cmd("Previous Git Change", "gitprevchange"),
		}},
		{Title: "Workspace", Items: []appmenu.Item{
			prefill("Open Workspace…", "workspaceopen"),
			cmd("Reload Workspace", "workspacereload"),
			sep,
			prefill("Focus Workspace…", "workspacefocus"),
			prefill("Move Workspace…", "workspacemove"),
			sep,
			prefill("New Worktree…", "worktreenew"),
			prefill("Open Worktree…", "worktreeopen"),
			prefill("Remove Worktree…", "worktreeremove"),
		}},
		{Title: "Network", Items: []appmenu.Item{
			// Spelled as a raw echo macro rather than prefill: prefill
			// appends a trailing <space>, which would separate the caret
			// from the scheme the workspaceopen completer extends.
			cmd("Open Remote Workspace…", "echo", "{prompt}workspaceopen<space>rune://"),
			sep,
			cmd("Status", "console", "network", "status"),
			cmd("Show Peers", "console", "network", "peers"),
			cmd("Show Machines", "console", "network", "machines"),
			sep,
			cmd("Connect", "console", "network", "up"),
			cmd("Disconnect", "console", "network", "down"),
			sep,
			prefill("Remove Machine…", "console", "network", "remove"),
		}},
		{Title: "Tools", Items: []appmenu.Item{
			cmd("New Terminal", "terminalnew"),
			cmd("New Terminal Tab", "terminalnewtab"),
			cmd("New Terminal Or Split", "terminalneworsplit"),
			cmd("Open Companion Terminal", "!"),
			prefill("Run…", "!"),
			sep,
			appmenu.Submenu{Title: "Agent", Items: []appmenu.Item{
				cmd("New", "agent"),
				appmenu.Submenu{Title: "Change Model", Items: modelItems},
				appmenu.Submenu{Title: "Change Effort", Items: effortItems},
				prefill("Change Maximum Tokens…", "chatmaxtokens"),
				sep,
				cmd("Compact", "chatcompact"),
				cmd("Fork", "chatfork"),
				cmd("Clear", "chatclear"),
				prefill("Invoke Skill…", "chatskill"),
				cmd("Export", "chatexport"),
			}},
			prefill("New Task…", "tasknew"),
			prefill("New Task Tab…", "tasknewtab"),
			sep,
			cmd("Debugger", "debugger"),
			cmd("Console", "console", "help"),
		}},
		{Title: "Help", Items: []appmenu.Item{
			cmd("Rune Help", "help"),
			cmd("Documentation", "docs"),
			sep,
			cmd("Cheatsheet", "cheatsheet"),
			cmd("Key Bindings", "keybindings"),
			sep,
			appmenu.Submenu{Title: "Start Tutorial", Items: tutorialItems},
			cmd("Stop Tutorial", "tutorial", "stop"),
			cmd("Check for Updates", "console", "upgrade"),
		}},
	}
}

// openRecentMenu builds the File ▸ Open Recent submenu. Each entry
// dispatches workspaceopen with an explicit path, which
// activateAppMenuCommand routes straight to the IDE (bypassing the open
// panel). When there are no recents the submenu shows a single disabled
// placeholder so the entry is still discoverable.
func openRecentMenu(recents []recentEntry) appmenu.Submenu {
	items := make([]appmenu.Item, 0, len(recents))
	for _, r := range recents {
		items = append(items, appmenu.Command{
			Title:   r.label,
			Command: cmdWorkspaceOpen,
			Args:    []string{r.path},
		})
	}
	if len(items) == 0 {
		items = append(items, appmenu.Command{
			Title:    "No Recent Projects",
			Disabled: true,
		})
	}
	return appmenu.Submenu{Title: "Open Recent", Items: items}
}
