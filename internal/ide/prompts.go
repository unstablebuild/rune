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
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide/idehistory"
	"unstable.build/rune/internal/ide/idetask"
	"unstable.build/rune/internal/workspace"
)

const (
	yesOpt = "    Yes    "
	noOpt  = "    No    "
)

var yesNoKeyCombs = []term.KeyComb{{Ch: 'y'}, {Ch: 'n'}}

func (h *workspaceManagerHandler) openRestorePrompt(
	ex *ex,
	workspaceURI workspaceapi.URI,
	state idehistory.State,
) {
	promptHandler := newOpenRestorePromptHandler(
		h, ex, workspaceURI, state, yesOpt, noOpt,
	).(*openRestorePromptHandler)
	promptWindow := ex.comp.Prompt(
		"Do you want to **restore** the previous session?",
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		promptHandler,
	)
	promptHandler.promptWindow = promptWindow
}

func newOpenRestorePromptHandler(
	wm *workspaceManagerHandler,
	ex *ex,
	workspaceURI workspaceapi.URI,
	state idehistory.State,
	restoreCwdOption, noRestoreOption string,
) handler.PromptHandler {
	return &openRestorePromptHandler{
		wm:               wm,
		ex:               ex,
		workspaceURI:     workspaceURI,
		state:            state,
		restoreCwdOption: restoreCwdOption,
		noRestoreOption:  noRestoreOption,
	}
}

type openRestorePromptHandler struct {
	wm               *workspaceManagerHandler
	ex               *ex
	workspaceURI     workspaceapi.URI
	promptWindow     browser.Window
	state            idehistory.State
	restoreCwdOption string
	noRestoreOption  string
}

func (h *openRestorePromptHandler) OnSelect(idx int, option string) {
	// close so if invokeWindow is Closed (called from another prompt)
	// Focus() does not return the Prompt window
	if h.promptWindow != nil {
		_ = h.promptWindow.Close()
	}

	var err error

	switch option {
	case h.restoreCwdOption:
		err = h.wm.restorePreviousSession(h.ex, h.state)
	case h.noRestoreOption:
		err = h.wm.state.ClearWorkspaceState(
			context.Background(), h.workspaceURI)
	}

	if err != nil {
		_, _ = h.wm.empty.Browser().Notify(browserapi.LevelError, "%s", err.Error())
	}
}

func (h *openRestorePromptHandler) OnClose() error {
	return nil
}

func (h *workspaceManagerHandler) openReopenSessionPrompt(
	ex *ex, targets []idehistory.SessionWorkspace,
) {
	promptHandler := &reopenSessionPromptHandler{wm: h, targets: targets}
	promptHandler.promptWindow = ex.comp.Prompt(
		h.reopenSessionPromptMessage(targets),
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		promptHandler,
	)
}

func (h *workspaceManagerHandler) reopenSessionPromptMessage(
	targets []idehistory.SessionWorkspace,
) string {
	var msg strings.Builder
	msg.WriteString("Do you want to **open** the following workspaces " +
		"from your last session?\n")
	for _, w := range targets {
		msg.WriteString("\n- " + h.workspaceDisplayPath(w.URI))
	}
	return msg.String()
}

type reopenSessionPromptHandler struct {
	wm           *workspaceManagerHandler
	promptWindow browser.Window
	targets      []idehistory.SessionWorkspace
}

func (h *reopenSessionPromptHandler) OnSelect(idx int, option string) {
	if h.promptWindow != nil {
		_ = h.promptWindow.Close()
	}
	if option == yesOpt {
		h.wm.reopenSessionWorkspaces(h.targets)
		return
	}
	// Overwrite the declined snapshot with what is actually open, so the
	// offer is not repeated on the next start.
	h.wm.persistLastSession()
}

func (h *reopenSessionPromptHandler) OnClose() error {
	return nil
}

func (h *workspaceManagerHandler) openConfirmExitPrompt(ex *ex, hasDirtyFilesOpen bool) {

	promptText := "Are you sure you want to **exit**?"

	if hasDirtyFilesOpen {
		promptText = "There are open files with changes pending to be written. " +
			promptText
	}

	promptHandler := &openConfirmExitPromptHandler{wm: h}
	promptWindow := ex.comp.Prompt(
		promptText,
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		promptHandler,
	)
	promptHandler.promptWindow = promptWindow
}

func (h *workspaceManagerHandler) openCreateWorkspacePrompt(ex *ex, uri workspaceapi.URI) {
	promptText := fmt.Sprintf(
		"workspace with URI %s does not exist. Do you want to **create** it?",
		uri.String())

	promptHandler := &createWorkspaceHandler{ex: ex, uri: uri, wm: h}
	ex.comp.Prompt(
		promptText,
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		promptHandler,
	)
}

type openConfirmExitPromptHandler struct {
	wm           *workspaceManagerHandler
	promptWindow browser.Window
}

func (h *openConfirmExitPromptHandler) OnSelect(
	idx int, option string,
) {
	switch option {
	case yesOpt:
		h.wm.confirmedForceExit = true
		h.wm.events.globalPublisher()(term.Event{Type: term.EventNone})
	case noOpt:
		h.wm.shaderRunner.cancel()
		h.wm.confirmedForceExit = false
		h.promptWindow.Close()
	}
}

func (h *openConfirmExitPromptHandler) OnClose() error {
	h.wm.exitPromptOpen = false
	h.wm.shaderRunner.cancel()
	return nil
}

type createWorkspaceHandler struct {
	ex  *ex
	wm  *workspaceManagerHandler
	uri workspaceapi.URI
}

func (h *createWorkspaceHandler) OnSelect(
	idx int, option string,
) {
	switch option {
	case yesOpt:
		path := h.uri.Path()
		// NOTE: if we just use the default workspace, we might
		// be using the wrong scheme (or on the wrong host). Recursively,
		// attempt to create a workspace on some parent directory of path,
		// and then proceed to MkdirAll from that root.
		parent, err := h.createParentWorkspace(h.uri)
		if err != nil {
			_, _ = h.ex.Browser().Notify(browserapi.LevelError,
				"create parent workspace: %v", err.Error())
			log.Errorf("create parent to mkdirall of %s: %v", path, err)
			return
		}
		err = parent.MkdirAll(path, 0755)
		if err != nil {
			_, _ = h.ex.Browser().Notify(browserapi.LevelError, "mkdirall: %v", err)
			log.Errorf("mkdirall %s: %v", path, err)
			return
		}

		log.Tracef("mkdirall %s: ok", path)

		err = h.wm.addWorkspace(h.uri, true, true, -1)
		if err != nil {
			_, _ = h.ex.Browser().Notify(browserapi.LevelError, "%s", err.Error())
			log.Errorf("add workspace %s: %v", h.uri, err)
			return
		}
		log.Tracef("add workspace %s: ok", h.uri)
	case noOpt:
	}
}

func (h *createWorkspaceHandler) OnClose() error {
	return nil
}

func (h *createWorkspaceHandler) createParentWorkspace(
	uri workspaceapi.URI,
) (workspace.Workspace, error) {
	parent := workspaceapi.Dir(uri)
	if parent.Equal(uri) {
		return nil, errors.New("reached root dir")
	}

	cwd, err := h.wm.workspace.AddWorkspace(context.Background(), uri)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return h.createParentWorkspace(parent)
	}
	return cwd, err
}

const (
	overwrite = "   Discard external changes   "
	discard   = "   Discard your changes   "
)

var (
	overwriteDiscardKeyComb = []term.KeyComb{{Ch: 'o'}, {Ch: 'd'}}
)

func (ex *ex) openFileChangedPrompt(
	file workspaceapi.URI, h browserapi.Handler, op string, reload bool,
) {
	promptHandler := &fileChangedPrompt{
		uri:    file,
		ex:     ex,
		h:      h,
		op:     op,
		reload: reload,
	}
	_ = ex.comp.Prompt(
		fmt.Sprintf("You have unflushed changes on file '%s', "+
			"and file was just %s disk. What do you want to do "+
			"with your changes?", file.Name(), op),
		[]string{overwrite, discard},
		overwriteDiscardKeyComb,
		promptHandler,
	)
}

func (ex *ex) openSurePrompt(
	file workspaceapi.URI, h browserapi.Handler, op string, reload bool,
) {
	promptHandler := &areYouSurePrompt{
		uri:    file,
		ex:     ex,
		h:      h,
		op:     op,
		reload: reload,
	}
	_ = ex.comp.Prompt(
		fmt.Sprintf("Are you sure you want to **discard** your changes to %s?", file.Name()),
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		promptHandler,
	)
}

func (ex *ex) openCloseDirtyTabsPrompt(message string, close func() error) {
	promptHandler := &closeDirtyTabsPrompt{ex: ex, close: close}
	promptWindow := ex.comp.Prompt(
		message,
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		promptHandler,
	)
	promptHandler.promptWindow = promptWindow
}

type closeDirtyTabsPrompt struct {
	ex           *ex
	close        func() error
	promptWindow browser.Window
}

func (h *closeDirtyTabsPrompt) OnSelect(idx int, option string) {
	switch option {
	case yesOpt:
		if h.promptWindow != nil {
			_ = h.promptWindow.Close()
		}
		if err := h.close(); err != nil {
			_, _ = h.ex.comp.Notify(browserapi.LevelError, "failed to close tab: %v", err)
		}
	case noOpt:
		if h.promptWindow != nil {
			_ = h.promptWindow.Close()
		}
	}
}

func (h *closeDirtyTabsPrompt) OnClose() error {
	return nil
}

type fileChangedPrompt struct {
	ex     *ex
	uri    workspaceapi.URI
	h      browserapi.Handler
	reload bool
	op     string

	selected bool
}

func (h *fileChangedPrompt) OnSelect(
	idx int, option string,
) {
	switch option {
	case overwrite:
		h.overwrite()
	case discard:
		h.discard()
	}
}

func (h *fileChangedPrompt) OnClose() error {
	if h.selected || h.ex.closed {
		return nil
	}
	h.ex.openSurePrompt(h.uri, h.h, h.op, h.reload)
	return nil
}

func (h *fileChangedPrompt) discard() {
	h.selected = true
	h.ex.openSurePrompt(h.uri, h.h, h.op, h.reload)
}

func (h *fileChangedPrompt) overwrite() {
	h.selected = true
	if err := h.ex.flusher.overwrite(h.uri, h.h); err != nil {
		_, _ = h.ex.comp.Notify(browserapi.LevelError,
			"failed to overwrite tab: %v", err)
	}
}

type areYouSurePrompt struct {
	ex     *ex
	h      browserapi.Handler
	reload bool
	uri    workspaceapi.URI
	op     string

	selected bool
}

func (h *areYouSurePrompt) OnSelect(
	idx int, option string,
) {
	switch option {
	case yesOpt:
		h.discard()
	case noOpt:
		h.reopen()
	}
}

func (h *areYouSurePrompt) OnClose() error {
	if h.selected || h.ex.closed {
		return nil
	}
	h.ex.openFileChangedPrompt(h.uri, h.h, h.op, h.reload)
	return nil
}

func (h *areYouSurePrompt) reopen() {
	h.selected = true
	h.ex.openFileChangedPrompt(h.uri, h.h, h.op, h.reload)
}

func (h *areYouSurePrompt) discard() {
	h.selected = true
	if h.reload {
		if err := h.ex.flusher.reloadAsync(h.uri, h.h, nil); err != nil {
			_, _ = h.ex.comp.Notify(browserapi.LevelError,
				"failed to reload tab: %v", err)
		}
	} else {
		if err := h.ex.comp.RemoveTab(h.h); err != nil {
			_, _ = h.ex.comp.Notify(browserapi.LevelError, "failed to remove tab: %v", err)
		}
	}
}

type replaceTaskHandler struct {
	ex *ex
	t  idetask.Task
}

func (h *replaceTaskHandler) OnSelect(
	idx int, option string,
) {
	switch option {
	case yesOpt:
		if err := h.ex.tasks.ReplaceTask(h.t); err != nil {
			_, _ = h.ex.comp.Notify(browserapi.LevelError, "replace task: %v", err)
			return
		}
	case noOpt:
	}
}

func (h *replaceTaskHandler) OnClose() error {
	return nil
}

func (e *ex) openReplaceTaskPrompt(t idetask.Task) error {
	promptText := fmt.Sprintf(
		"A task with the name %q already exists. Do you want to **replace** it?", t.Name)

	promptHandler := &replaceTaskHandler{ex: e, t: t}
	e.comp.Prompt(
		promptText,
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		promptHandler,
	)
	return nil
}

// ShowFallbackPrompt satisfies text.FallbackPrompter. It is invoked when a
// command was dispatched but no handler is registered for it (e.g. the
// providing extension is not installed).
func (e *ex) ShowFallbackPrompt(_ context.Context, command string, _ ...string) {
	// The home/empty workspace deliberately starts no extensions, so the
	// commands they provide are never registered there. Tell the user to
	// open a workspace instead of offering to install the extension.
	if e.home {
		_, _ = e.comp.Notify(browserapi.LevelInfo,
			"You are not on a workspace. Open a workspace first to use %q.",
			command)
		return
	}
	switch command {
	case "agent", "?":
		e.openInstallExtensionPrompt("rune-agent",
			"This command requires the **rune-agent** extension. "+
				"Do you want to install it now?")
	case "searchfile", "searchtext", "searchast":
		e.openInstallExtensionPrompt("fuzzy-search",
			"This command requires the **fuzzy-search** extension. "+
				"Do you want to install it now?")
	}
}

// markHome flags this ex as the home/empty workspace and routes every
// rune-agent command through the fallback prompt. The home workspace starts
// no extensions, so without these entries the chat* commands would surface a
// raw "unknown command" error instead of the "open a workspace" message.
func (e *ex) markHome() {
	e.home = true
	for _, cmd := range []string{
		"chateffort", "chatmaxtokens", "chatskill", "chatmodel",
		"chatclear", "chatcompact", "chatfork", "chatexport", "chatlog",
		"chatrename",
	} {
		e.config.CommandFallbacks[cmd] = e
	}
}

func (e *ex) openInstallExtensionPrompt(pkg, message string) {
	h := &installExtensionPrompt{ex: e, pkg: pkg}
	h.promptWindow = e.comp.Prompt(
		message,
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		h,
	)
}

type installExtensionPrompt struct {
	ex           *ex
	pkg          string
	promptWindow browser.Window
}

func (h *installExtensionPrompt) OnSelect(_ int, option string) {
	if h.promptWindow != nil {
		_ = h.promptWindow.Close()
	}
	if option == yesOpt {
		if err := h.ex.consolenewtab(context.Background(),
			"pkg", "install", h.pkg); err != nil {
			_, _ = h.ex.comp.Notify(browserapi.LevelError,
				"failed to open console: %v", err)
		}
	}
}

func (h *installExtensionPrompt) OnClose() error { return nil }
