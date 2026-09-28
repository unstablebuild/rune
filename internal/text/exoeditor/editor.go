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

// Package exo implements a "bring-your-own-editor" text.Editor that
// hosts an external TUI editor (vim, neovim, helix, kakoune, …) inside
// a Rune-managed vte. Rune keeps owning the tab, the cell.Buffer
// (read-only mirror of disk), and IDE-wide commands; the external
// editor owns the editing UX and is the source of truth for buffer
// contents.
package exoeditor

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/clipboard"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/ide/vctrl/vctrlcmd"
	"unstable.build/rune/internal/term/vte"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/cmdenv"
	"unstable.build/rune/internal/workspace"
)

// New allocates a new exo Editor.
func New(
	command, gotoTemplate, quit string,
	scheduleNextTick func(func()) bool,
	cwd workspace.Workspace,
	workspaceURI workspaceapi.URI,
	notifications browserapi.Notifications,
	publisher browser.EventPublisher,
	terminal schemeapi.Terminal,
	executor schemeapi.Executor,
	tabManager browser.TabManager,
	vteCfg vte.Config,
	reloader Reloader,
	env cmdenv.Source,
	experimentalHighlights bool,
	registry text.WorkspaceCommandRegistry,
	vctrlSvc vctrl.Service,
	clip clipboard.Register,
) *Editor {
	return new(command, gotoTemplate, quit, scheduleNextTick, cwd,
		workspaceURI, notifications, publisher, terminal, executor,
		tabManager, vteCfg, reloader, env, experimentalHighlights, registry,
		vctrlSvc, clip, gracefulQuitTimeout)
}

func new(
	command, gotoTemplate, quit string,
	scheduleNextTick func(func()) bool,
	cwd workspace.Workspace,
	workspaceURI workspaceapi.URI,
	notifications browserapi.Notifications,
	publisher browser.EventPublisher,
	terminal schemeapi.Terminal,
	executor schemeapi.Executor,
	tabManager browser.TabManager,
	vteCfg vte.Config,
	reloader Reloader,
	env cmdenv.Source,
	experimentalHighlights bool,
	registry text.WorkspaceCommandRegistry,
	vctrlSvc vctrl.Service,
	clip clipboard.Register,
	quitTimeout time.Duration,
) *Editor {
	switch {
	case command == "":
		panic("exoeditor.New: command is required")
	case scheduleNextTick == nil:
		panic("exoeditor.New: scheduleNextTick is required")
	case cwd == nil:
		panic("exoeditor.New: cwd is required")
	case notifications == nil:
		panic("exoeditor.New: notifications is required")
	case publisher == nil:
		panic("exoeditor.New: publisher is required")
	case terminal == nil:
		panic("exoeditor.New: terminal is required")
	case executor == nil:
		panic("exoeditor.New: executor is required")
	case tabManager == nil:
		panic("exoeditor.New: tabManager is required")
	case reloader == nil:
		panic("exoeditor.New: reloader is required")
	}
	tpl, err := parseGotoTemplate(gotoTemplate)
	if err != nil {
		panic("exoeditor.New: invalid gotoTemplate: " + err.Error())
	}
	quitKeys, err := term.ParseKeys(quit)
	if err != nil {
		panic("exoeditor.New: invalid quit: " + err.Error())
	}
	ret := &Editor{
		command:                command,
		gotoTemplate:           tpl,
		quitKeys:               quitKeys,
		scheduleNextTick:       scheduleNextTick,
		cwd:                    cwd,
		workspaceURI:           workspaceURI,
		notifications:          notifications,
		publisher:              publisher,
		terminal:               terminal,
		executor:               executor,
		tabManager:             tabManager,
		vteCfg:                 vteCfg,
		reloader:               reloader,
		env:                    env,
		experimentalHighlights: experimentalHighlights,
		vctrlSvc:               vctrlSvc,
		clipboard:              clip,
		quitTimeout:            quitTimeout,
	}
	if registry != nil {
		ret.fileRegistry = text.NewFileCommandRegistry(workspaceURI, registry)
	}
	ret.pub.Init()
	return ret
}

// Editor implements text.Editor. Its zero value is not usable; use
// the New constructor.
type Editor struct {
	command                string
	gotoTemplate           gotoTemplate
	quitKeys               []term.KeyComb
	scheduleNextTick       func(func()) bool
	cwd                    workspace.Workspace
	workspaceURI           workspaceapi.URI
	notifications          browserapi.Notifications
	publisher              browser.EventPublisher
	terminal               schemeapi.Terminal
	executor               schemeapi.Executor
	tabManager             browser.TabManager
	vteCfg                 vte.Config
	reloader               Reloader
	env                    cmdenv.Source
	experimentalHighlights bool
	fileRegistry           text.FileCommandRegistry
	vctrlSvc               vctrl.Service
	clipboard              clipboard.Register
	quitTimeout            time.Duration

	pub text.Publisher
}

// IsExternal reports true: exo hosts an external TUI editor that
// owns the buffer contents.
func (e *Editor) IsExternal() bool { return true }

// Edit opens file in a fresh vte hosting the configured external
// editor. The returned handler reads/writes through the vte; Rune
// mirrors the on-disk file into buf via a workspace watcher.
func (e *Editor) Edit(
	ctx context.Context,
	file workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
) (text.Handler, error) {
	// Substitute {file}/{line}/{col} into the configured argv
	// template, then hand the resulting string to vte as a
	// single-element slice. vte.Component.createPty joins
	// CommandAndArgs with spaces and then runs shell.Fields on the
	// joined string to do POSIX-style tokenisation, so tokenising
	// here too would double-process the input (quoted segments
	// would lose their quotes and bare punctuation such as the
	// parentheses in `vim "+call cursor(1, 1)" {file}` would
	// re-tokenise as syntax errors). Pre-substituted-string in,
	// shell.Fields out, no double-tokenisation.
	cmdStr := substituteCommand(e.command, file.Path(), 1, 1)
	// Apply Rune-side shell-style expansion so $WORKSPACE,
	// $FILE, $RUNE_DATADIR, … resolve before the
	// vte's own shell.Fields pass runs. Expansion failures (e.g.
	// command substitution rejected) propagate as a clear error
	// rather than silently producing a malformed argv.
	if expanded, expErr := cmdenv.Expand(ctx, cmdStr, e.env); expErr == nil {
		cmdStr = expanded
	} else {
		return nil, fmt.Errorf("exoeditor: expand command %q: %w", cmdStr, expErr)
	}

	cfg := e.vteCfg
	cfg.CommandAndArgs = []string{cmdStr}
	cfg.Modal = false
	cfg.ScheduleNextTick = e.scheduleNextTick

	procDone := make(chan error, 1)
	cfg.Watcher = workspaceapi.ChanProcessWatcher(procDone)

	pub := newEventPublisher(e.publisher)
	vteH, err := vte.NewHandler(
		pub, e.notifications,
		e.terminal, e.executor, e.tabManager, cfg)
	if err != nil {
		return nil, fmt.Errorf("exoeditor: new vte handler: %w", err)
	}

	h := newHandler(vteH, buf, file, e.gotoTemplate,
		e.cwd, e.notifications, e.scheduleNextTick, e.reloader,
		e.experimentalHighlights, e.quitKeys, procDone, e.quitTimeout,
		e.executor)
	pub.setRefresh(h.refreshProbe)
	var ret text.Handler = h
	if e.fileRegistry != nil {
		var err error
		ret, err = text.SubscribeLocationCommands(file, e.fileRegistry, ret)
		if err != nil {
			return nil, fmt.Errorf("exoeditor: subscribe location commands: %w", err)
		}
		ret, err = vctrlcmd.SubscribeGitCommands(file, e.fileRegistry,
			ret, e.vctrlSvc, e.clipboard, e.notifications)
		if err != nil {
			return nil, fmt.Errorf("exoeditor: subscribe git commands: %w", err)
		}
	}
	if e.experimentalHighlights {
		ret = withMessageBar(ret, h)
	}
	return e.pub.PublishExternalEdit(file, buf, ret), nil
}

// SubscribeCommand returns an error: exo does not host Rune-side
// editing commands.
func (e *Editor) SubscribeCommand(textapi.CommandManual, text.CommandHandler) error {
	return errors.New("not supported")
}

// RegisterREPLCommand returns an error: exo does not host Rune-side
// editing commands.
func (e *Editor) RegisterREPLCommand(textapi.CommandManual, textapi.REPLHandler) error {
	return errors.New("not supported")
}

// REPLCommands returns nil.
func (e *Editor) REPLCommands() []textapi.CommandManual { return nil }

// UnsubscribeCommand returns an error: nothing was ever registered.
func (e *Editor) UnsubscribeCommand(string) error { return errors.New("not supported") }

// UnregisterREPLCommand returns an error: nothing was ever registered.
func (e *Editor) UnregisterREPLCommand(string) error { return errors.New("not supported") }

// RegisterResourceOpener returns an error: exo does not host Rune-side
// resource openers.
func (e *Editor) RegisterResourceOpener(string, textapi.ResourceOpenHandler) error {
	return errors.New("not supported")
}

// UnregisterResourceOpener returns an error: nothing was ever registered.
func (e *Editor) UnregisterResourceOpener(string) error { return errors.New("not supported") }

// Editor returns an error: exo does not track multiple handlers.
func (e *Editor) Editor(workspaceapi.URI) (text.Handler, error) {
	return nil, errors.New("not supported")
}

// SubscribeEvents forwards subscriptions to the internal Publisher.
func (e *Editor) SubscribeEvents(
	evs []textapi.EventType, sub text.EventHandler,
) error {
	e.pub.SubscribeEvents(evs, sub)
	return nil
}

// UnsubscribeEvents forwards unsubscriptions to the internal Publisher.
func (e *Editor) UnsubscribeEvents(sub text.EventHandler) (bool, error) {
	return e.pub.UnsubscribeEvents(sub), nil
}

// substituteCommand expands {file}/{line}/{col} placeholders inside
// the argv template before shell tokenisation. line/col are 1-based.
func substituteCommand(tpl, file string, line, col int) string {
	repl := strings.NewReplacer(
		"{file}", file,
		"{line}", strconv.Itoa(line),
		"{col}", strconv.Itoa(col),
	)
	return repl.Replace(tpl)
}
