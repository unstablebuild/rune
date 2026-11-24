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
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
)

const (
	generateYesOpt    = "  Yes  "
	generateAlwaysOpt = "  Always  "
	generateNoOpt     = "  No  "
)

// compdbOffer offers to generate the compilation database of a project
// when one of its C or C++ files opens without one. Generating runs the
// project's build scripts, so it never happens without consent: Yes
// generates once, Always also stops the asking for the workspace, and No
// or a dismissal leaves the project alone for the session.
type compdbOffer struct {
	// baseCtx outlives the per-event context, which the editor cancels
	// as soon as Handle returns.
	baseCtx  context.Context
	fs       workspaceapi.FileSystem
	cfg      config.Config
	notify   browserapi.Notifications
	prompter browserapi.Prompter
	compdb   *compdbSetting
	gen      *compdbGenerator
	wsDir    string

	mu sync.Mutex
	// considered holds the projects already offered, or found not to
	// need an offer, this session.
	considered map[string]bool
	// promptMu asks one question at a time when files of several
	// projects open at once.
	promptMu sync.Mutex
}

var _ textapi.EventHandler = (*compdbOffer)(nil)

func (o *compdbOffer) Handle(_ context.Context, ev textapi.Event) bool {
	if ev.Type != textapi.EventTypeOpen || !isCcppFile(ev.URI) || !insideDir(o.wsDir, ev.URI.Path()) {
		return false
	}
	file := ev.URI.Path()
	go debug.CapturePanicReport(func() { o.consider(o.baseCtx, file) })
	return false
}

// consider offers to generate the database of file's project, or
// generates it right away when the user chose Always.
func (o *compdbOffer) consider(ctx context.Context, file string) {
	project := projectDirOf(o.fs, o.wsDir, file)
	if !o.claim(project) {
		return
	}
	plan, ok := o.planFor(ctx, project, file)
	if !ok {
		return
	}
	label := wsRel(o.wsDir, project)
	auto, err := o.compdb.autoGenerate(ctx)
	if err != nil {
		slog.Warn("ccpp: read compile_commands.json generation preference", "error", err)
	}
	if !auto {
		switch o.ask(ctx, label, plan) {
		case generateYesOpt:
		case generateAlwaysOpt:
			if err := o.compdb.setAutoGenerate(ctx, true); err != nil {
				_, _ = o.notify.Notify(browserapi.LevelWarn,
					"Could not remember to always generate %s: %v", compileCommandsName, err)
			}
		default:
			return
		}
	}
	res, err := o.gen.generate(ctx, project, plan, label)
	if err != nil {
		_, _ = o.notify.Notify(browserapi.LevelError,
			"Generating %s for %s failed: %v\n\nRun `ccpp compdb generate` to try again.",
			compileCommandsName, label, err)
		return
	}
	level := browserapi.LevelInfo
	if res.entries == 0 {
		level = browserapi.LevelWarn
	}
	_, _ = o.notify.Notify(level, "%s", res.summary(func(p string) string { return wsRel(o.wsDir, p) }))
}

// claim reports whether project is considered for the first time this
// session.
func (o *compdbOffer) claim(project string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.considered[project] {
		return false
	}
	o.considered[project] = true
	return true
}

// planFor returns how to generate the database of project when file
// has none clangd would use, and false when there is nothing to offer:
// a database is in effect, the project already has one clangd does not
// find on its own (`ccpp compdb use` is the remedy), or Rune cannot
// generate one for its build system.
func (o *compdbOffer) planFor(ctx context.Context, project, file string) (compdbPlan, bool) {
	override, _, err := o.compdb.get(ctx)
	if err != nil {
		slog.Warn("ccpp: read compilation database override", "error", err)
		return compdbPlan{}, false
	}
	configured, _ := readCompileCommandsDir(o.cfg, o.notify)
	if compdbInEffect(o.fs, override, configured, file).Kind != "none" {
		return compdbPlan{}, false
	}
	if len(compdbCandidates(o.fs, project)) > 0 {
		return compdbPlan{}, false
	}
	return planCompdb(o.fs, project, projectMarkers(o.fs, project))
}

// ask asks whether to generate the database of the project named label
// with plan, and blocks until the user answers. A dismissal, a failed
// prompt or ctx ending answer "".
func (o *compdbOffer) ask(ctx context.Context, label string, plan compdbPlan) string {
	o.promptMu.Lock()
	defer o.promptMu.Unlock()

	options := []string{generateYesOpt, generateAlwaysOpt, generateNoOpt}
	// Selecting an option and the dismissal that follows both answer, so
	// only the first is kept.
	ch := make(chan string, 1)
	answer := func(option string) {
		select {
		case ch <- option:
		default:
		}
	}
	_, err := o.prompter.Prompt(browserapi.PromptConfig{
		Message:  generatePromptMessage(label, plan),
		Options:  options,
		Bindings: []term.KeyComb{{Ch: 'y'}, {Ch: 'a'}, {Ch: 'n'}},
	}, handler.FuncPromptHandler(
		func(idx int, _ string) { answer(options[idx]) },
		func() error {
			answer("")
			return nil
		}))
	if err != nil {
		slog.Warn("ccpp: compile_commands.json prompt failed", "project", label, "error", err)
		return ""
	}
	select {
	case a := <-ch:
		return a
	case <-ctx.Done():
		return ""
	}
}

func generatePromptMessage(label string, plan compdbPlan) string {
	where := "`" + label + "`"
	if label == "." {
		where = "the workspace root"
	}
	return fmt.Sprintf("The %s project at %s has no %s, so clangd guesses each file's compile "+
		"flags and may not resolve its includes and macros.\n\n"+
		"Generate one now? Rune runs `%s` in the project, which runs its build scripts.\n\n"+
		"Always generates one without asking for every project of this workspace "+
		"(`ccpp compdb auto off` asks again); `ccpp compdb generate` generates one any time.",
		systemName(plan.system), where, compileCommandsName, plan)
}
