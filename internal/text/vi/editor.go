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

package vi

import (
	"context"
	"errors"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/vctrl/vctrlcmd"
	"unstable.build/rune/internal/text"
)

type viEditor struct {
	text.Publisher
	config   viConfig
	registry text.FileCommandRegistry
	opts     []Option
}

type publisherEventsAdapter struct{ pub *text.Publisher }

func (a publisherEventsAdapter) SubscribeEvents(evs []textapi.EventType, sub text.EventHandler) error {
	a.pub.SubscribeEvents(evs, sub)
	return nil
}

func (a publisherEventsAdapter) UnsubscribeEvents(sub text.EventHandler) (bool, error) {
	return a.pub.UnsubscribeEvents(sub), nil
}

// Editor returns a Vi text.Editor.
func Editor(opts ...Option) text.Editor {
	ret := &viEditor{opts: opts, config: defaultviHandlerImplConfig()}
	for _, o := range opts {
		o(&ret.config)
	}
	ret.Publisher.Init()
	if ret.config.registry != nil {
		ret.registry = text.NewFileCommandRegistry(ret.config.workspace, ret.config.registry)
	}
	return ret
}

func (e *viEditor) Edit(
	ctx context.Context,
	file workspaceapi.URI, buf *cell.Buffer, readOnly, recovered bool,
) (text.Handler, error) {
	indentRune := text.IndentRuneTab
	r, tabspaces, ok := text.IndentConfigForURI(
		file, buf, e.config.indents, e.config.tabspaces,
	)
	if ok {
		indentRune = r
	}
	root := NewWithIndent(buf, file, indentRune, tabspaces, e.opts...)
	// publisher does not mutate cursor and it should never do so
	cursor := root.cursor
	scroll := root.less.Scroll()
	var ret text.Handler = root
	if e.registry != nil {
		var err error
		ret, err = text.SubscribeLocationCommands(file, e.registry, ret)
		if err != nil {
			return nil, err
		}
		ret, err = text.SubscribeFoldCommands(file, e.registry, cursor, ret)
		if err != nil {
			return nil, err
		}
		ret, err = text.SubscribeIndentCommands(file, e.registry, cursor, e.config.indents, ret)
		if err != nil {
			return nil, err
		}
		ret, err = text.SubscribeCommentCommands(file, e.registry, cursor, ret)
		if err != nil {
			return nil, err
		}
		ret, err = vctrlcmd.SubscribeGitCommands(
			file, e.registry, ret, e.config.auxBarConfig.Service,
			e.config.clipboard, e.config.notifications)
		if err != nil {
			return nil, err
		}
	}
	ret = e.Publisher.PublishEdit(file, buf, ret, cursor)
	bars, ok := text.BarsFromContext(ctx)
	if !ok {
		return ret, nil
	}
	auxBarConfig := e.config.auxBarConfig
	auxBarConfig.CommandRegistry = e.registry
	iconsBarConfig := e.config.iconsBarConfig
	iconsBarConfig.CommandRegistry = e.registry
	iconsBarConfig.Publisher = publisherEventsAdapter{pub: &e.Publisher}
	if e.config.enableAuxBar && !bars.DisableAuxBar {
		ret = text.WithAuxBar(ret, buf, scroll, auxBarConfig)
	}
	if e.config.enableIconsBar && !bars.DisableIconsBar {
		ret = text.WithIconsBar(e.config.auxBarConfig.Service, e.config.enableGitIcons, ret, buf,
			scroll, iconsBarConfig)
	}
	if !e.config.statusBarEnabled {
		return ret, nil
	}
	statusBarConfig := e.config.statusBarConfig
	if bars.StatusBar != nil {
		statusBarConfig.Workspace = bars.StatusBar.Workspace
		statusBarConfig.GitService = bars.StatusBar.GitService
	}
	bar := text.WithStatusBar(ret, buf, scroll,
		readOnly, recovered, statusBarConfig)
	root.setStatusBar(bar)
	return bar, nil
}

// SubscribeCommand is not supported.
func (e *viEditor) SubscribeCommand(cmd textapi.CommandManual, h text.CommandHandler) error {
	return errors.New("not supported")
}

func (e *viEditor) RegisterREPLCommand(
	cmd textapi.CommandManual, h textapi.REPLHandler,
) error {
	return errors.New("not supported")
}

func (e *viEditor) REPLCommands() []textapi.CommandManual {
	return nil
}

func (c *viEditor) UnsubscribeCommand(cmd string) error {
	return errors.New("not supported")
}

func (c *viEditor) UnregisterREPLCommand(cmd string) error {
	return errors.New("not supported")
}

func (e *viEditor) RegisterResourceOpener(string, textapi.ResourceOpenHandler) error {
	return errors.New("not supported")
}

func (e *viEditor) UnregisterResourceOpener(string) error {
	return errors.New("not supported")
}

// Editor is not supported
func (e *viEditor) Editor(file workspaceapi.URI) (text.Handler, error) {
	// NOTE: it would be dead code
	return nil, errors.New("not supported")
}

// SubscribeEvents subsribes sub to ev. Note that this Editor is only capable
// of dispatching EventTypeOpen, EventTypeEdit EventType events.
func (e *viEditor) SubscribeEvents(
	evs []textapi.EventType, sub text.EventHandler,
) error {
	e.Publisher.SubscribeEvents(evs, sub)
	return nil
}

func (e *viEditor) UnsubscribeEvents(sub text.EventHandler) (bool, error) {
	ok := e.Publisher.UnsubscribeEvents(sub)
	return ok, nil
}

// IsExternal reports false: vi manages its buffer in process.
func (*viEditor) IsExternal() bool { return false }
