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

	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/extensionapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"unstable.build/rune/cmd/rune-agent/agentshell"
)

// NewExtension returns an extension and its metadata.
func NewExtension() (extensionapi.WorkspaceExtension, extensionapi.Metadata) {
	return &workspaceExtension{}, extensionapi.Metadata{
		DeveloperID:    "Unstable Build",
		DeveloperEmail: "it@unstable.build",
		DeveloperKey:   "064D4ABCFA6D9338",
		ExtensionID:    "rune-agent",
		ExtensionName:  "Rune Agent",
		Permissions: extensionapi.NewPermissions(
			permissions...,
		),
	}
}

var (
	commands = []textapi.CommandManual{
		{
			Name: commandQuery,
			Summary: "Send a coding question to your AI assistant. " +
				"The current active file is loaded and available in the model's context. " +
				"The default coding model is configured via extension configuration.",
			Synopsis: "[<message>]",
		},
		{
			Name: commandChat,
			Summary: "Open a new conversation tab with your AI assistant. " +
				"If no dialogue ID is provided, a new conversation is started. " +
				"If no model is provided, the default model configured via extension " +
				"configuration is used.",
			Synopsis: "[<dialogue_id> [<model>]]",
		},
		{
			Name: commandModel,
			Summary: "Show or switch the model of the focused agent chat. " +
				"Run from an open agent chat tab.",
			Synopsis: "[model]",
		},
		{
			Name: commandEffort,
			Summary: "Show or set the reasoning effort of the focused agent chat. " +
				"Run from an open agent chat tab.",
			Synopsis: "[none|minimal|low|medium|high|xhigh|max|ultra]",
		},
		{
			Name: commandMaxTokens,
			Summary: "Show or set the max output tokens of the focused agent chat. " +
				"Run from an open agent chat tab.",
			Synopsis: "[tokens]",
		},
		{
			Name: commandSkill,
			Summary: "Load a skill into the focused agent chat. " +
				"Run from an open agent chat tab.",
			Synopsis: "<skill> [args]",
		},
		{
			Name: commandClear,
			Summary: "Clear the focused agent chat and archive its previous " +
				"contents. Run from an open agent chat tab.",
			Synopsis: "",
		},
		{
			Name: commandCompact,
			Summary: "Compact the focused agent chat into a summarized copy, using " +
				"the compact model alias when no model is provided. " +
				"Run from an open agent chat tab.",
			Synopsis: "[<model>]",
		},
		{
			Name: commandFork,
			Summary: "Open a picker to fork the focused agent chat at a selected " +
				"message. Run from an open agent chat tab.",
			Synopsis: "",
		},
		{
			Name: commandReviewChanges,
			Summary: "Review every patch the focused agent chat applied as one " +
				"editable diff. Edits are attached as a changes review to the " +
				"next message. Run from an open agent chat tab.",
			Synopsis: "",
		},
		{
			Name: commandReviewAll,
			Summary: "Review everything uncommitted in the workspace repository " +
				"as one editable diff, untracked files included. Edits are " +
				"attached to the next message. Run from an open agent chat tab.",
			Synopsis: "",
		},
		{
			Name: commandExport,
			Summary: "Export the focused agent chat or its audit log to a temp " +
				"file. Run from an open agent chat tab.",
			Synopsis: "[--audit]",
		},
		{
			Name: commandLog,
			Summary: "Show the LLM token audit log for the focused agent chat. " +
				"Run from an open agent chat tab.",
			Synopsis: "",
		},
		{
			Name: commandRename,
			Summary: "Give the focused agent chat a meaningful name. With no " +
				"argument a popup asks for the title; with arguments the " +
				"title is set directly. The tab label updates when the " +
				"chat is reopened. Run from an open agent chat tab.",
			Synopsis: "[title]",
		},
		{
			Name: commandAddSymbol,
			Summary: "Attach a named symbol, or the symbol under the cursor, " +
				"with its definition, references and documentation to the last " +
				"focused agent chat.",
			Synopsis: "[symbol]",
		},
	}
	events = []textapi.EventType{
		textapi.EventTypeOpen, textapi.EventTypeFocus,
		textapi.EventTypeUnfocus, textapi.EventTypeFlush,
		textapi.EventTypeClose, textapi.EventTypeCursor,
	}
	permissions = []extensionapi.Permission{
		extensionapi.PermissionBrowserWindowManager,
		extensionapi.PermissionBrowserResourceOpener,
		extensionapi.PermissionNotifications,
		extensionapi.PermissionInterrupt,
		extensionapi.PermissionStorage,
		extensionapi.PermissionEditor,
		extensionapi.PermissionCommands,
		extensionapi.PermissionConfig,
		extensionapi.PermissionFileSystem,
		extensionapi.PermissionExecute,
		extensionapi.PermissionTerminal,
		extensionapi.PermissionSyntaxTree,
		extensionapi.PermissionLSP,
		extensionapi.PermissionLLM,
	}
)

type workspaceExtension struct{}

func (e *workspaceExtension) ExtendWorkspace(
	ctx context.Context, w *extensionapi.Workspace, cfg config.Config,
) error {
	h, err := newCommandEventHandler(ctx, w.Editor(ctx), w, cfg)
	if err != nil {
		return err
	}

	for _, cmd := range commands {
		err := w.RegisterCommand(cmd, h)
		if err != nil {
			return fmt.Errorf("register command %q: %w", cmd.Name, err)
		}
	}

	if err := w.RegisterREPLCommand(agentshell.Manual(), h.newAgentShell()); err != nil {
		return fmt.Errorf("register repl command %q: %w", agentshell.CommandName, err)
	}

	if err := w.RegisterResourceOpener(chatScheme, h); err != nil {
		return fmt.Errorf("register %s resource opener: %w", chatScheme, err)
	}

	ed := w.Editor(ctx)
	if err := ed.SubscribeEvents(events, h); err != nil {
		return fmt.Errorf("subscribe events: %w", err)
	}

	return nil
}
