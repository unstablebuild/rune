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

package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/langext"
)

const (
	manageOpt   = "  Rune manages it  "
	unmanageOpt = "  I manage it  "
)

// envAnswer carries the outcome of the environment prompt. answered is
// false when the user dismissed the prompt without choosing, which is
// treated as "not now": unmanaged for this session, nothing persisted.
type envAnswer struct {
	managed  bool
	answered bool
}

// newEnvPrompt returns the prompt asking whether Rune may manage the
// environment of root, and the handler of its answer, which travels over
// ch so the bring-up goroutine stays off the window event stream.
// Selecting an option and dismissing both reach ch, in that order, so ch
// must be buffered and only the first send is kept.
func newEnvPrompt(
	root langext.Root, ch chan<- envAnswer,
) (browserapi.PromptConfig, handler.PromptHandler) {
	where := root.RelPath
	if where == "" {
		where = root.Dir
	}
	// The relative path is exactly what `python enable` accepts as its
	// optional argument; the workspace root needs no argument at all.
	target := ""
	if root.RelPath != "" {
		target = " " + root.RelPath
	}
	message := fmt.Sprintf(
		"Rune found a Python project at %s.\n\n"+
			"Let Rune manage its environment? It creates and syncs a "+
			".venv and keeps the debugger wired to it.\n\n"+
			"You can change this later with `python enable%s` or "+
			"`python disable%s`.", where, target, target)

	answer := func(a envAnswer) {
		select {
		case ch <- a:
		default:
		}
	}
	cfg := browserapi.PromptConfig{
		Message:  message,
		Options:  []string{manageOpt, unmanageOpt},
		Bindings: []term.KeyComb{{Ch: 'r'}, {Ch: 'i'}},
	}
	return cfg, handler.FuncPromptHandler(
		func(idx int, _ string) {
			answer(envAnswer{managed: idx == 0, answered: true})
		},
		func() error {
			answer(envAnswer{})
			return nil
		})
}

// envPromptMu serializes prompts so a monorepo bringing several project
// roots up at once asks one question at a time.
var envPromptMu sync.Mutex

// askManageEnvironment asks the prompt for root and blocks until the
// user answers or dismisses it. An unavailable prompter reports
// answered=false, so bring-up falls back to leaving the environment
// alone rather than acting without consent.
func askManageEnvironment(
	ctx context.Context, prompter browserapi.Prompter, root langext.Root,
) (managed, answered bool) {
	if prompter == nil {
		return false, false
	}
	envPromptMu.Lock()
	defer envPromptMu.Unlock()

	ch := make(chan envAnswer, 1)
	if _, err := prompter.Prompt(newEnvPrompt(root, ch)); err != nil {
		slog.Warn("python env prompt failed", "root", root.Dir, "error", err)
		return false, false
	}
	select {
	case a := <-ch:
		return a.managed, a.answered
	case <-ctx.Done():
		return false, false
	}
}
