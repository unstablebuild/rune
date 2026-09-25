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
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/idetutorial"
	"unstable.build/rune/internal/ide/idetutorial/starlarktutorial"
)

const tutorialPlaylistPromptDelay = 3 * time.Second

func buildTutorials(i *IDE) map[string]idetutorial.Tutorial {
	files := i.ideConfig.tutorialFiles()
	embedded := i.options.starlarkTutorials
	i.tutorialsConfig = newTutorialsConfig(i)
	if len(files) == 0 && len(embedded) == 0 {
		return nil
	}
	tutorials := make(map[string]idetutorial.Tutorial,
		len(files)+len(embedded))
	for name, src := range embedded {
		t, err := i.tutorialsConfig.build(name, src)
		if err != nil {
			i.ideConfig.errors["tutorials."+name] = err
			continue
		}
		tutorials[name] = t
	}
	for name, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			i.ideConfig.errors["tutorials."+name] = fmt.Errorf(
				"read %q: %w", path, err)
			continue
		}
		t, err := i.tutorialsConfig.build(name, string(src))
		if err != nil {
			i.ideConfig.errors["tutorials."+name] = err
			continue
		}
		tutorials[name] = t
	}
	return tutorials
}

// onTutorialsInstalled is invoked by the package manager's post-merge hook
// (off the event loop) when a freshly-installed package added tutorials under
// the top-level `tutorials:` config key. It builds each not-yet-registered
// tutorial, then schedules their registration into the live runner onto the
// event loop. Every built tutorial is registered, but only the first one
// prompts "run it now?": prompting per tutorial would stack overlapping
// floating windows. The first return reports whether at least one tutorial was
// built so the merge result records a live-apply; the error aggregates every
// read/build failure so idepkg can notify in one place.
func (i *IDE) onTutorialsInstalled(names []string) (bool, error) {
	if len(names) == 0 {
		return false, nil
	}
	cfg, err := i.workspaceHandler.reloadConfig()
	if err != nil {
		return false, fmt.Errorf("reload config for installed tutorials: %w", err)
	}
	files := cfg.tutorialFiles()

	type built struct {
		name string
		t    idetutorial.Tutorial
	}
	var (
		ready []built
		errs  error
	)
	for _, name := range names {
		if i.tutorial.has(name) {
			continue
		}
		path, ok := files[name]
		if !ok {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("read tutorial %q: %w", name, err))
			continue
		}
		t, err := i.tutorialsConfig.build(name, string(src))
		if err != nil {
			errs = errors.Join(errs, fmt.Errorf("build tutorial %q: %w", name, err))
			continue
		}
		ready = append(ready, built{name: name, t: t})
	}
	if len(ready) == 0 {
		return false, errs
	}

	i.options.scheduleFn(func() {
		prompted := false
		// Never interrupt a tutorial the user is already running with a
		// "run this newly installed one now?" prompt.
		running := i.tutorial.running()
		for _, b := range ready {
			if !i.tutorial.register(b.name, b.t) {
				i.notifyTutorialNotRegistered(b.name)
				continue
			}
			if !prompted && !running {
				i.promptRunTutorial(b.name, "")
				prompted = true
			}
		}
	})
	return true, errs
}

// notifyTutorialNotRegistered tells the user a freshly-installed tutorial could
// not be made live because one is already registered under the same name, so a
// restart is needed to pick up the new definition. It must run on the event
// loop.
func (i *IDE) notifyTutorialNotRegistered(name string) {
	_, _ = i.workspaceHandler.notifications.current().Notify(
		browserapi.LevelWarn,
		"a tutorial named %q is already loaded; restart to use the new one.",
		name,
	)
}

func (i *IDE) onTutorialCompleted(name string) {
	next, ok := i.options.nextTutorialPlaylistItem(name)
	if !ok || !i.tutorial.has(next.Name) {
		return
	}
	i.options.afterFunc(tutorialPlaylistPromptDelay, func() {
		debug.CapturePanicReport(func() {
			i.options.scheduleFn(func() {
				if i.tutorial.running() {
					return
				}
				i.promptRunTutorial(next.Name, next.Description)
			})
		})
	})
}

// promptRunTutorial asks the user whether to run a tutorial. It must run on
// the event loop. On Yes it dispatches `tutorial start <name>`; on No or close
// it does nothing.
func (i *IDE) promptRunTutorial(name, description string) {
	ex := i.workspaceHandler.focusEx()
	if ex == nil {
		return
	}
	message := fmt.Sprintf("Do you want to run the **%s** tutorial now?", name)
	if description != "" {
		message = fmt.Sprintf("Do you want to do the **%s** tutorial now?\n\n%s",
			name, description)
	}
	var promptWindow browser.Window
	promptWindow = ex.comp.Prompt(
		message,
		[]string{yesOpt, noOpt},
		yesNoKeyCombs,
		handler.FuncPromptHandler(func(_ int, option string) {
			if promptWindow != nil {
				_ = promptWindow.Close()
			}
			if option != yesOpt {
				return
			}
			i.workspaceHandler.focusEx().Dispatch("tutorial", "start", name)
		}, func() error { return nil }),
	)
}

// tutorialsConfig captures the host services a tutorial is built against so a
// single tutorial can be constructed without recomputing them. It is built
// once during IDE init and reused when packages install new tutorials.
type tutorialsConfig struct {
	partition        storageapi.Service
	style            idetutorial.PromptStyle
	ed               currentEditor
	parser           currentParser
	notifications    browserapi.Notifications
	scheduleNextTick func(func()) bool
	commandKey       term.KeyComb
	editorMode       string
	os               string
	keyForCommand    func(cmd string, args []string) string
	manualLookup     starlarktutorial.CommandManualLookup
	workspaceOpen    func() bool
	lspServerRunning func() bool
	configPath       string
}

func newTutorialsConfig(i *IDE) tutorialsConfig {
	partition := i.storage
	if p, err := i.storage.Partition("idetutorial"); err == nil {
		partition = p
	}
	rawKeyFor := i.ideConfig.commandKeyBindingLookup()
	return tutorialsConfig{
		partition:        partition,
		style:            newTutorialPromptStyle(i),
		ed:               currentEditor{root: i.workspaceHandler},
		parser:           currentParser{root: i.workspaceHandler},
		notifications:    i.workspaceHandler.notifications.current(),
		scheduleNextTick: i.options.scheduleFn,
		commandKey:       i.ideConfig.commandKey(),
		editorMode:       i.ideConfig.pkgEditorMode(),
		os:               runtime.GOOS,
		keyForCommand: func(cmd string, args []string) string {
			return starlarktutorial.PrettyKeySpec(rawKeyFor(cmd, args))
		},
		manualLookup: buildTutorialCommandManualLookup(i.workspaceHandler),
		workspaceOpen: func() bool {
			return !i.workspaceHandler.focusEx().home
		},
		lspServerRunning: func() bool {
			m := i.workspaceHandler.focusLSPManager()
			return m != nil && m.AnyServerRunning()
		},
		configPath: i.ideConfig.configPath,
	}
}

// newTutorialPromptStyle styles the tile's buttons and the prompt of
// a confirm or choice step like the prompts the IDE opens elsewhere.
func newTutorialPromptStyle(i *IDE) idetutorial.PromptStyle {
	cfg := i.ideConfig.promptConfig()
	return idetutorial.PromptStyle{
		TextAttr:       cfg.TextAttr,
		HighlightAttr:  cfg.HighlightAttr,
		BackgroundAttr: cfg.BackgroundAttr,
	}
}

// newTutorialTileStyle frames the tile like the IDE frames its
// windows, so it reads as one of them.
func newTutorialTileStyle(i *IDE) idetutorial.TileStyle {
	wm := i.ideConfig.windowManagerConfig()
	return idetutorial.TileStyle{
		Frame:             wm.Frame,
		FrameCharSet:      wm.FrameCharSet,
		FocusFrameCharSet: wm.FocusFrameCharSet,
		FrameAttr:         wm.FrameAttr,
		FocusFrameAttr:    wm.FocusFrameAttr,
		ScrollBarAttr:     wm.ScrollBarAttr,
		ScrollBarChar:     wm.ScrollBarChar,
		Prompt:            newTutorialPromptStyle(i),
	}
}

// build constructs a single tutorial from its starlark source.
func (c tutorialsConfig) build(
	name, src string,
) (idetutorial.Tutorial, error) {
	return starlarktutorial.New(
		name, src,
		c.style, c.ed, c.notifications, c.parser,
		c.scheduleNextTick, c.partition, c.commandKey,
		c.editorMode, c.os, c.keyForCommand, c.manualLookup,
		c.workspaceOpen,
		c.lspServerRunning,
		starlarktutorial.WithConfigPath(c.configPath),
	)
}

// buildTutorialCommandManualLookup returns a closure that resolves
// a command name to its registered command.Manual. It consults the
// focused editor's subscribed commands first and then the workspace
// handler's alias expander so authors writing `wait_command("e")`
// (an alias for `edit`) see the alias entry in the generated hint.
func buildTutorialCommandManualLookup(
	root *workspaceManagerHandler,
) starlarktutorial.CommandManualLookup {
	if root == nil {
		return nil
	}
	return func(name string) (command.Manual, bool) {
		ex := root.focusEx()
		if ex == nil {
			return command.Manual{}, false
		}
		for _, man := range ex.comp.Commands() {
			if man.Name == name {
				return man, true
			}
		}
		if ex.aliasExpander != nil {
			for _, man := range ex.aliasExpander.Aliases() {
				if man.Name == name {
					return man, true
				}
			}
		}
		return command.Manual{}, false
	}
}
