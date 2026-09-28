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

// Package upgradeshell exposes the `upgrade` REPL command on the rune
// (IDE) side. It checks the release manifest, prompts the user to
// confirm, and installs the new release in place.
//
// Download and install progress is reported through the
// repl.ProgressWriter handed to it by the host shell, and results are
// returned as markdown, so it integrates with the REPL the same way
// the `pkg` shell does.
package upgradeshell

import (
	"context"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/ide/ideupgrade"
)

// CommandName is the top-level REPL command exposed by this shell.
const CommandName = "upgrade"

var commandManual = textapi.CommandManual{
	Name: CommandName,
	Summary: "Check for a new Rune release and install it in place. " +
		"When a release is available you are prompted to confirm, " +
		"postpone the reminder, or skip the version.",
}

// Manual returns the REPL command manual.
func Manual() textapi.CommandManual { return commandManual }

// Config configures a Handler.
type Config struct {
	// Manager performs the manifest check, the confirmation prompt
	// and the in-place upgrade.
	Manager *ideupgrade.Manager
	// OSPackaged reports that an OS package manager installed this
	// build. The command then explains where updates come from
	// instead of reaching for the network.
	OSPackaged bool
}

// Handler implements the `upgrade` command.
type Handler struct {
	mgr        *ideupgrade.Manager
	osPackaged bool
}

var _ textapi.REPLHandler = (*Handler)(nil)

// New returns a Handler configured with cfg. It panics if Manager is
// nil — the rune-side wiring only registers the command once the
// manager has been constructed, so a missing one is a programming
// error.
func New(cfg Config) *Handler {
	if cfg.Manager == nil {
		panic("upgradeshell: Config.Manager must not be nil")
	}
	return &Handler{mgr: cfg.Manager, osPackaged: cfg.OSPackaged}
}

// osPackagedMessage is shown instead of running an upgrade when the
// build came from an OS package: the install prefix belongs to the
// package manager, which is also where the next version comes from.
const osPackagedMessage = "This build was distributed by an OS package " +
	"manager, so auto-updates are disabled. Check your distribution's " +
	"package manager for updates."

// HandleCommand satisfies repl.CommandHandler. It runs off the IDE
// event loop, which is what lets it block on both the network fetch
// and the confirmation prompt.
func (h *Handler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	out, err := h.upgrade(ctx, cmd, pw)
	if err != nil {
		return nil, err
	}
	return markdownOutput(out), nil
}

// upgrade performs the command and returns its markdown output.
func (h *Handler) upgrade(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (string, error) {
	if len(cmd.Args) > 0 && cmd.Args[0] == "help" {
		return usageMarkdown(), nil
	}

	if h.osPackaged {
		return osPackagedMessage, nil
	}

	pw.Progress(0, 1, "checking for updates")
	manifest, available, err := h.mgr.Check(ctx)
	if err != nil {
		return "", err
	}
	if !available {
		return fmt.Sprintf(
			"Rune is up to date (**%s**)", h.mgr.CurrentVersion()), nil
	}

	choice, err := h.mgr.PromptChoice(ctx, manifest)
	if err != nil {
		return "", err
	}
	switch choice {
	case ideupgrade.ChoiceUpgradeNow:
		if err := h.mgr.Upgrade(ctx, manifest, pw); err != nil {
			return "", err
		}
		return fmt.Sprintf(
			"Upgraded to **%s** — restart Rune to apply", manifest.Version), nil
	case ideupgrade.ChoiceRemindLater:
		return fmt.Sprintf(
			"Rune **%s** is available. Reminder postponed.", manifest.Version), nil
	case ideupgrade.ChoiceSkipVersion:
		return fmt.Sprintf("Skipping Rune **%s**.", manifest.Version), nil
	default:
		return fmt.Sprintf("Rune **%s** is available.", manifest.Version), nil
	}
}

// Complete satisfies repl.CommandHandler. `upgrade` takes no
// arguments, so there is nothing to complete.
func (h *Handler) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.FromSlice[string](nil), nil
}

// Help satisfies textapi.REPLHandler.
func (h *Handler) Help(
	context.Context, []string,
) (iterator.Iterator[component.Responsive], error) {
	return markdownOutput(usageMarkdown()), nil
}

func usageMarkdown() string {
	return fmt.Sprintf("## `%s`\n\n%s\n",
		commandManual.Name, commandManual.Summary)
}

// markdownOutput wraps a markdown string into a single-shot iterator
// suitable for returning from HandleCommand. Falls back to a plain
// responsive string when the markdown parser rejects the content.
func markdownOutput(content string) iterator.Iterator[component.Responsive] {
	md, err := markdown.New(content)
	if err != nil {
		r := component.NewResponsiveString(content, component.StringResponsiveConfig{})
		return iterator.FromSlice([]component.Responsive{r})
	}
	return iterator.FromSlice([]component.Responsive{md})
}
