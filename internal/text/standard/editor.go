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

package standard

import (
	"context"
	"errors"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/vctrl/vctrlcmd"
	"unstable.build/rune/internal/text"
)

// Editor allocates storage for a new Editor and initializes it.
func Editor(opts ...Option) text.Editor {
	ret := new(editor)
	ret.standardConfig = defaultConfig()
	for _, o := range opts {
		o(&ret.standardConfig)
	}
	ret.indents = text.IndentConfig{}
	ret.pub.Init()
	if ret.standardConfig.registry != nil {
		ret.fileRegistry = text.NewFileCommandRegistry(
			ret.standardConfig.workspace, ret.standardConfig.registry)
	}
	ret.opts = opts
	return ret
}

type editor struct {
	standardConfig
	indents      text.IndentConfig
	fileRegistry text.FileCommandRegistry
	pub          text.Publisher
	opts         []Option
}

type publisherEventsAdapter struct{ pub *text.Publisher }

func (a publisherEventsAdapter) SubscribeEvents(evs []textapi.EventType, sub text.EventHandler) error {
	a.pub.SubscribeEvents(evs, sub)
	return nil
}

func (a publisherEventsAdapter) UnsubscribeEvents(sub text.EventHandler) (bool, error) {
	return a.pub.UnsubscribeEvents(sub), nil
}

func (e *editor) Edit(
	ctx context.Context,
	file workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
) (ret text.Handler, err error) {
	indentRune := text.IndentRuneTab
	r, tabspaces, ok := text.IndentConfigForURI(
		file, buf, e.indents, e.standardConfig.tabspaces,
	)
	if ok {
		indentRune = r
	}
	root := NewHandler(buf, file, indentRune, tabspaces, e.opts...).(*standardHandler)
	ret = root
	cursor := &root.cursor
	if e.fileRegistry != nil {
		var err error
		ret, err = text.SubscribeLocationCommands(file, e.fileRegistry, ret)
		if err != nil {
			return nil, err
		}
		ret, err = text.SubscribeFoldCommands(file, e.fileRegistry, cursor, ret)
		if err != nil {
			return nil, err
		}
		ret, err = text.SubscribeIndentCommands(file, e.fileRegistry, cursor, e.indents, ret)
		if err != nil {
			return nil, err
		}
		ret, err = text.SubscribeCommentCommands(file, e.fileRegistry, cursor, ret)
		if err != nil {
			return nil, err
		}
		ret, err = vctrlcmd.SubscribeGitCommands(file, e.fileRegistry,
			ret, e.auxBarConfig.Service, e.clipboard, e.notifications)
		if err != nil {
			return nil, err
		}
	}
	scroll := root.less.Scroll()
	ret = e.pub.PublishEdit(file, buf, ret, cursor)
	if bars, ok := text.BarsFromContext(ctx); ok {
		auxBarConfig := e.auxBarConfig
		auxBarConfig.CommandRegistry = e.fileRegistry
		iconsBarConfig := e.iconsBarConfig
		iconsBarConfig.CommandRegistry = e.fileRegistry
		iconsBarConfig.Publisher = publisherEventsAdapter{pub: &e.pub}
		if e.enableAuxBar && !bars.DisableAuxBar {
			ret = text.WithAuxBar(ret, buf, scroll, auxBarConfig)
		}
		if e.enableIconsBar && !bars.DisableIconsBar {
			ret = text.WithIconsBar(e.auxBarConfig.Service, e.enableGitIcons, ret, buf,
				scroll, iconsBarConfig)
		}
		if e.statusBarEnabled {
			statusBarConfig := e.statusBarConfig
			if bars.StatusBar != nil {
				statusBarConfig.Workspace = bars.StatusBar.Workspace
				statusBarConfig.GitService = bars.StatusBar.GitService
			}
			bar := text.WithStatusBar(ret, buf, scroll, readOnly, recovered, statusBarConfig)
			root.setStatusBar(bar)
			if !e.commandBar {
				bar.ShowCommandBar(false)
			}
			ret = bar
		}
	}
	if e.search.WindowManager != nil {
		ret = newSearchHandler(ret, root, e.search)
	}
	return ret, nil
}

func (e *editor) SubscribeCommand(cmd textapi.CommandManual, h text.CommandHandler) error {
	return errors.New("not supported")
}

func (e *editor) RegisterREPLCommand(
	cmd textapi.CommandManual, h textapi.REPLHandler,
) error {
	return errors.New("not supported")
}

func (e *editor) REPLCommands() []textapi.CommandManual {
	return nil
}

func (c *editor) UnsubscribeCommand(cmd string) error {
	return errors.New("not supported")
}

func (c *editor) UnregisterREPLCommand(cmd string) error {
	return errors.New("not supported")
}

func (e *editor) RegisterResourceOpener(string, textapi.ResourceOpenHandler) error {
	return errors.New("not supported")
}

func (e *editor) UnregisterResourceOpener(string) error {
	return errors.New("not supported")
}

func (e *editor) Editor(file workspaceapi.URI) (text.Handler, error) {
	return nil, errors.New("not supported")
}

func (e *editor) SubscribeEvents(evs []textapi.EventType, sub text.EventHandler) error {
	e.pub.SubscribeEvents(evs, sub)
	return nil
}

func (e *editor) UnsubscribeEvents(sub text.EventHandler) (bool, error) {
	ok := e.pub.UnsubscribeEvents(sub)
	return ok, nil
}

// IsExternal reports false: the standard editor manages its buffer
// in process.
func (*editor) IsExternal() bool { return false }
