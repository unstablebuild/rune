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

// Package pkgconsole exposes the `pkg` REPL command tree on the rune
// (IDE) side. It manages packages from the official distribution:
// installing, removing, switching versions, and checking for updates.
//
// The shell reports install/upgrade progress through the
// repl.ProgressWriter handed to it by the host shell and returns all
// results as markdown, so it integrates with the REPL the same way the
// `models` shell does.
package pkgconsole

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/ide/idepkg"
	"unstable.build/rune/internal/ide/idepkg/pkgrpc"
)

// CommandName is the top-level REPL command exposed by this shell.
const CommandName = "pkg"

var commandManual = textapi.CommandManual{
	Name:     CommandName,
	Summary:  "Install and manage packages from Rune's official distribution.",
	Synopsis: "<command> [<args>]",
	Commands: []textapi.CommandManual{
		{
			Name: "install",
			Summary: "Installs a package from the official distribution. " +
				"If version is omitted, the package is updated to the latest version. " +
				"If the package contains executables, they are made available to " +
				"terminal sessions via the PATH environment variable. " +
				"`use` is not necessary after running this command. " +
				"`--lang` lists and installs language (runtime) packages.",
			Synopsis: "[--lang] <package> [<version>]",
		},
		{
			Name: "remove",
			Summary: "Removes a package from local storage. " +
				"If version is specified, then only the specified version is removed, " +
				"otherwise all versions are removed. " +
				"If version is passed and it is in use, this command errors out.",
			Synopsis: "<package> [<version>]",
		},
		{
			Name: "use",
			Summary: "Use the given package version, if available. " +
				"If not available, download it first via `pkg install`.",
			Synopsis: "<package> <version>",
		},
		{
			Name:     "current",
			Summary:  "Print the given package version in use.",
			Synopsis: "<package>",
		},
		{
			Name: "describe",
			Summary: "Show a package's notes and the notes for a release version " +
				"in formatted markdown. If version is omitted, the latest version is used.",
			Synopsis: "<package> [<version>]",
		},
		{
			Name:    "update-all",
			Summary: "Upgrades all installed packages to the latest version.",
		},
		{
			Name:    "update-check",
			Summary: "Check for package updates for all installed packages.",
		},
	},
}

// Manual returns the parent REPL command manual.
func Manual() textapi.CommandManual { return commandManual }

// Config configures a Handler.
type Config struct {
	// Manager backs every subcommand. Packages are installed on the
	// host it manages, without prompting. It must not be nil.
	Manager idepkg.PackageManager
	// Host names the machine Manager manages in messages to the user.
	// It is empty for this machine.
	Host string
}

// Handler is the dispatcher for the `pkg` command tree.
type Handler struct {
	mgr  idepkg.PackageManager
	host string
}

var _ textapi.REPLHandler = (*Handler)(nil)

// New returns a Handler configured with cfg. It panics if Manager is
// nil.
func New(cfg Config) *Handler {
	if cfg.Manager == nil {
		panic("pkgconsole: Config.Manager must not be nil")
	}
	return &Handler{mgr: cfg.Manager, host: cfg.Host}
}

var subcommandNames = []string{
	"install", "remove", "use", "current", "describe", "update-all", "update-check",
}

// HandleCommand satisfies repl.CommandHandler. The shell splits the
// first arg and routes to the matching subcommand handler.
func (h *Handler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	it, err := h.handleCommand(ctx, cmd, pw)
	if errors.Is(err, pkgrpc.ErrUnsupported) {
		return nil, errors.New(pkgrpc.UpdateHostMessage(h.host))
	}
	return it, err
}

func (h *Handler) handleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return markdownOutput(usageMarkdown(commandManual)), nil
	}
	sub := cmd.Args[0]
	args := cmd.Args[1:]
	switch sub {
	case "install":
		return h.handleInstall(ctx, args, pw)
	case "remove":
		return h.handleRemove(ctx, args)
	case "use":
		return h.handleUse(ctx, args)
	case "current":
		return h.handleCurrent(ctx, args)
	case "describe":
		return h.handleDescribe(ctx, args)
	case "update-all":
		return h.handleUpdateAll(ctx, pw)
	case "update-check":
		return h.handleUpdateCheck(ctx)
	case "help":
		return markdownOutput(usageMarkdown(commandManual)), nil
	default:
		return nil, fmt.Errorf("unknown command: %s", sub)
	}
}

// Complete satisfies repl.CommandHandler.
func (h *Handler) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) <= 1 {
		filter := ""
		if len(args) == 1 {
			filter = args[0]
		}
		return iterator.FromSlice(filterNames(subcommandNames, filter)), nil
	}
	rest := args[1:]
	switch args[0] {
	case "install":
		return h.completePkgInstall(ctx, rest)
	case "describe":
		return h.completePkgInstall(ctx, rest)
	case "remove", "use":
		return h.completePkgInstalled(ctx, rest, true)
	case "current":
		return h.completePkgInstalled(ctx, rest, false)
	}
	return iterator.FromSlice[string](nil), nil
}

// Help satisfies textapi.REPLHandler.
func (h *Handler) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	man := commandManual
	for _, a := range args {
		sub, ok := findSubcommand(man, a)
		if !ok {
			break
		}
		man = sub
	}
	return markdownOutput(usageMarkdown(man)), nil
}

// findSubcommand looks up a child of man whose Name matches name.
func findSubcommand(man textapi.CommandManual, name string) (textapi.CommandManual, bool) {
	for _, c := range man.Commands {
		if c.Name == name {
			return c, true
		}
	}
	return textapi.CommandManual{}, false
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

func filterNames(names []string, prefix string) []string {
	if prefix == "" {
		return names
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// usageMarkdown renders a help page for a command manual node: a title,
// the summary as prose, a usage line, and a subcommand reference.
func usageMarkdown(man textapi.CommandManual) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## `%s`\n\n", man.Name)
	if man.Summary != "" {
		fmt.Fprintf(&b, "%s\n\n", man.Summary)
	}
	if man.Synopsis != "" {
		fmt.Fprintf(&b, "**Usage:** `%s %s`\n\n", man.Name, man.Synopsis)
	}
	if len(man.Commands) > 0 {
		b.WriteString("### Subcommands\n\n")
		for _, c := range man.Commands {
			invocation := c.Name
			if c.Synopsis != "" {
				invocation = fmt.Sprintf("%s %s", c.Name, c.Synopsis)
			}
			fmt.Fprintf(&b, "- `%s`\n  %s\n", invocation, c.Summary)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
