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

package shop

import (
	"context"
	"fmt"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/internal/browser"
	"unstable.build/rune/internal/ide/console/ideconsole"
)

func (r *Root) mustInitShellTab() {
	if r.shellTab != nil {
		return
	}
	registry := ideconsole.NewRegistry()
	registry.Register("help", "Show available commands", shopHelpHandler{r: registry})
	r.registerShellCommands(registry)
	h := repl.New(registry, r.scheduleNextTick, r.interrupter,
		repl.WithPrompt("shop> "))
	r.shell = h
	r.shellHelp = registry
	uri := mustParseURI("shell:///shop")
	r.shellTab = r.b.NewTab(uri, '>', shellTabName, browser.NopHandler(h), nil)
}

type shopHelpHandler struct {
	r *ideconsole.CommandRegistry
}

func (h shopHelpHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	return h.r.Help(ctx, cmd.Args)
}

func (h shopHelpHandler) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) == 0 {
		return iterator.Empty[string](), nil
	}
	return h.r.Complete(ctx, args[len(args)-1], nil)
}

func (h shopHelpHandler) Help(
	_ context.Context, _ []string,
) (iterator.Iterator[component.Responsive], error) {
	return newStaticShellCommand(
		"Help",
		"Show available commands or help for a specific command.",
	).HandleCommand(context.Background(), repl.Command{}, repl.NopProgressWriter())
}

func (r *Root) registerShellCommands(reg *ideconsole.CommandRegistry) {
	reg.Register("download", "Download the sshshop CLI", newStaticShellCommand(
		"Download",
		"A dedicated sshshop CLI is coming soon.",
		"For now, stay in the SSH session and browse the storefront directly.",
	))
	reg.Register("sign-up", "Create a new account", newStaticShellCommand(
		"Sign up",
		"Account creation will eventually prompt for an email and send a magic link.",
		"For now this is a placeholder flow.",
	))
	reg.Register("sign-in", "Sign into an existing account", newStaticShellCommand(
		"Sign in",
		"Existing-account login will eventually ask for your email and verify a magic-link code.",
		"For now this is a placeholder flow.",
	))
}

type staticShellCommand string

func (s staticShellCommand) HandleCommand(
	_ context.Context, _ repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	parts := strings.Split(string(s), "\n")
	ret := make([]component.Responsive, 0, len(parts))
	for _, p := range parts {
		ret = append(ret, component.NewResponsiveString(p, component.StringResponsiveConfig{}))
	}
	return iterator.FromSlice(ret), nil
}

func (s staticShellCommand) Complete(
	_ context.Context, _ string, _ []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

func (s staticShellCommand) Help(
	_ context.Context, _ []string,
) (iterator.Iterator[component.Responsive], error) {
	return s.HandleCommand(context.Background(), repl.Command{}, repl.NopProgressWriter())
}

func shellHelpText(title string, lines ...string) string {
	all := []string{title}
	all = append(all, lines...)
	return strings.Join(all, "\n")
}

func newStaticShellCommand(title string, lines ...string) staticShellCommand {
	return staticShellCommand(shellHelpText(title, lines...))
}

func (r *Root) showShell() error {
	if r.shellTab == nil {
		return fmt.Errorf("shell unavailable")
	}
	return r.invokeWindow().SetContent(r.shellTab)
}
