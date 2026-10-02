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

package extension

import (
	"context"
	"fmt"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"unstable.build/rune/internal/handler/finder"
	"unstable.build/rune/internal/ide/vctrl"
)

// NewExtension returns the combined fuzzy search extension and its metadata.
func NewExtension() (extensionapi.WorkspaceExtension, extensionapi.Metadata) {
	perms := append([]extensionapi.Permission{
		extensionapi.PermissionCommands,
		extensionapi.PermissionConfig,
	}, finder.Permissions()...)
	return workspaceExtension{}, extensionapi.Metadata{
		DeveloperID:      "Unstable Build",
		DeveloperEmail:   "it@unstable.build",
		DeveloperKey:     "064D4ABCFA6D9338",
		ExtensionID:      "fuzzy_search",
		ExtensionName:    "Fuzzy Search",
		ExtensionVersion: "development",
		Permissions:      extensionapi.NewPermissions(perms...),
	}
}

type workspaceExtension struct{}

func (workspaceExtension) ExtendWorkspace(
	ctx context.Context, w *extensionapi.Workspace, c config.Config,
) error {
	fs := w.FileSystem(ctx)
	matcher, err := vctrl.LoadGitignore(fs)
	if err != nil {
		log.Warnf("fuzzy_search: load gitignore: %v", err)
		matcher = vctrl.NopMatcher(false)
	}
	clients := finder.Clients{
		Storage:        w.Storage(ctx),
		ResourceOpener: w.ResourceOpener(ctx),
		WindowManager:  w.WindowManager(ctx),
		Interrupter:    w.Interrupter(ctx),
		Notifications:  w.Notifications(ctx),
		Editor:         w.Editor(ctx),
		FileSystem:     fs,
		Executor:       w.Executor(ctx),
		IgnoreMatcher:  matcher,
	}
	dataDir := w.DataDir(ctx)

	registrations := []struct {
		cmd textapi.CommandManual
		key string
		new finder.NewFunc
	}{
		{cmd: cmdSearchFile, key: "file", new: newFileHandler},
		{cmd: cmdSearchText, key: "line", new: newLineHandler},
		{
			cmd: cmdSearchSyntax,
			key: "syntax",
			new: func(
				ctx context.Context, cmd textapi.Command,
				clients finder.Clients, invokeWindow browserapi.Window, c config.Config,
			) (finder.RedispatchHandler, error) {
				return newSyntaxHandler(ctx, cmd, clients, invokeWindow, c, dataDir)
			},
		},
	}

	for _, reg := range registrations {
		if err := w.RegisterCommand(reg.cmd, finder.NewSplitCommandHandler(
			clients, commandConfig(c, reg.key), reg.new,
		)); err != nil {
			return fmt.Errorf("register command %q: %w", reg.cmd.Name, err)
		}
	}
	return nil
}

func commandConfig(c config.Config, key string) config.Config {
	sub, err := c.GetConfig(key)
	if err == nil {
		return sub
	}
	return config.NopConfig()
}
