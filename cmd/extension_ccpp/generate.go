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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

// bearTool is the step tool resolved to the Bear generation runs rather
// than looked up on PATH.
const bearTool = "bear"

// compdbStep is one command of a compdbPlan, run in the project
// directory.
type compdbStep struct {
	// tool is the program: a name looked up on the workspace host's
	// PATH, bearTool, or a path relative to the project such as
	// ./configure.
	tool string
	args []string
	// requires names programs the step runs in turn, such as the make a
	// Bear recording runs, looked up on PATH before the plan starts.
	requires []string
	// pipe feeds the step the standard output of the step before it.
	pipe bool
	// purpose is the reason Rune shows when it asks to run the command.
	purpose string
}

func (s compdbStep) String() string {
	return strings.Join(append([]string{s.tool}, s.args...), " ")
}

// compdbPlan is how a project's compilation database gets written: the
// commands that write it, run in order in the project directory.
type compdbPlan struct {
	system buildSystem
	steps  []compdbStep
	// output is the database the steps write, relative to the project.
	output string
	// emptyHint explains a database that lists no compile commands.
	emptyHint string
}

// String renders the plan as the shell command it amounts to.
func (p compdbPlan) String() string {
	var b strings.Builder
	for i, s := range p.steps {
		switch {
		case i == 0:
		case s.pipe:
			b.WriteString(" | ")
		default:
			b.WriteString(" && ")
		}
		b.WriteString(s.String())
	}
	return b.String()
}

const (
	purposeDryRun = "Print the commands a full build runs without running them (make -n), " +
		"so Bear can turn them into the compile_commands.json clangd reads compile flags from."
	purposeBear = "Turn the build commands make printed into compile_commands.json, " +
		"which clangd reads each file's compile flags from."
	purposeBearBuild = "Build the project with make while Bear records each compiler call into " +
		"compile_commands.json, which clangd reads compile flags from."
	emptyRecordHint = "`make -n` printed no compiler calls Bear recognizes, as when the build " +
		"hides its commands or generates sources first. Record a real build instead: run " +
		"`bear -- make` at the project root."
	emptyBuildHint = "Bear saw no compiler calls, as when the project was built before it had a " +
		"compile_commands.json and make had nothing to compile. Run `make clean`, then " +
		"`ccpp compdb generate` again."
	emptyConfigureHint = "The project builds no C or C++ sources in its default configuration."
)

// planCompdb returns how to write the compilation database of the
// project at dir, rooted by markers, and false when Rune cannot: Bazel
// needs an extractor added to the project first, and without a build
// system there is nothing to run.
//
// A Make project's database comes from a dry run, which compiles
// nothing. An Autotools project's comes from a real build instead: with
// -B, the dry run remakes an Automake tree's makefiles for real, which
// reruns configure and blanks the header dependencies make tracks in
// .deps/, and Bear cannot parse the compile lines of per-target flags or
// libtool. Bear appends to the database, so a rebuild that compiles
// little, or nothing, keeps the entries an earlier one recorded.
func planCompdb(fs workspaceapi.FileSystem, dir string, ms []marker) (compdbPlan, bool) {
	systems := buildSystems(ms)
	if len(systems) == 0 {
		return compdbPlan{}, false
	}
	switch system := systems[0]; system {
	case systemCMake:
		return compdbPlan{
			system: system,
			steps: []compdbStep{{
				tool: "cmake",
				args: []string{"-S", ".", "-B", "build", "-DCMAKE_EXPORT_COMPILE_COMMANDS=ON"},
				purpose: "Configure the CMake project in build/ with CMAKE_EXPORT_COMPILE_COMMANDS=ON, " +
					"which writes the compile_commands.json clangd reads compile flags from.",
			}},
			output:    filepath.Join("build", compileCommandsName),
			emptyHint: emptyConfigureHint,
		}, true
	case systemMeson:
		args := []string{"setup", "build"}
		// Setting up a configured build directory again fails.
		if isFile(fs, filepath.Join(dir, "build", "meson-private", "coredata.dat")) {
			args = []string{"setup", "--reconfigure", "build"}
		}
		return compdbPlan{
			system: system,
			steps: []compdbStep{{
				tool: "meson", args: args,
				purpose: "Set up the Meson build directory build/, which writes the " +
					"compile_commands.json clangd reads compile flags from.",
			}},
			output:    filepath.Join("build", compileCommandsName),
			emptyHint: emptyConfigureHint,
		}, true
	case systemAutotools:
		var steps []compdbStep
		if !isFile(fs, filepath.Join(dir, "configure")) {
			steps = append(steps, compdbStep{
				tool: "autoreconf", args: []string{"-i"},
				purpose: "Generate the configure script, which writes the Makefile the project's " +
					"compile commands are read from for clangd.",
			})
		}
		if len(steps) > 0 || !isFile(fs, filepath.Join(dir, "Makefile")) {
			steps = append(steps, compdbStep{
				tool: "./configure",
				purpose: "Configure the project, which writes the Makefile its compile commands " +
					"are read from for clangd.",
			})
		}
		return compdbPlan{
			system: system,
			steps: append(steps, compdbStep{
				tool: bearTool, args: []string{"--append", "--", "make"}, requires: []string{"make"},
				purpose: purposeBearBuild,
			}),
			output:    compileCommandsName,
			emptyHint: emptyBuildHint,
		}, true
	case systemMake:
		return compdbPlan{
			system: system,
			steps: []compdbStep{
				{tool: "make", args: []string{"-nBw"}, purpose: purposeDryRun},
				{tool: bearTool, args: []string{"parse-sh"}, pipe: true, purpose: purposeBear},
			},
			output:    compileCommandsName,
			emptyHint: emptyRecordHint,
		}, true
	}
	return compdbPlan{}, false
}

// systemName names a build system for the user.
func systemName(s buildSystem) string {
	switch s {
	case systemCMake:
		return "CMake"
	case systemMeson:
		return "Meson"
	case systemAutotools:
		return "Autotools"
	case systemMake:
		return "Make"
	}
	return string(s)
}

// compdbGenerator writes compilation databases by running a project's
// build tooling on the workspace host, then restarts a running clangd so
// the files already open pick the database up.
type compdbGenerator struct {
	fs      workspaceapi.FileSystem
	exec    workspaceapi.Executor
	notify  browserapi.Notifications
	state   *serverState
	restart func(ctx context.Context) error
	// bear resolves the Bear that runs `bear parse-sh`, "" when the
	// workspace host has none.
	bear func(ctx context.Context) string

	// mu runs one generation at a time: two would race on the same
	// build tree.
	mu sync.Mutex
}

// compdbResult is the outcome of a generation.
type compdbResult struct {
	// output is the absolute path of the database written.
	output    string
	entries   int
	restarted bool
	emptyHint string
}

// summary tells the user what the generation wrote, naming paths with
// rel.
func (r compdbResult) summary(rel func(string) string) string {
	path := rel(r.output)
	switch {
	case r.entries == 0:
		return fmt.Sprintf("`%s` lists no compile commands. %s", path, r.emptyHint)
	case r.restarted:
		return fmt.Sprintf("Wrote `%s` with %s. clangd restarted.", path, plural(r.entries, "compile command"))
	default:
		return fmt.Sprintf("Wrote `%s` with %s; clangd reads it when it starts.",
			path, plural(r.entries, "compile command"))
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// generate runs plan in the project at dir, named label for the user,
// reporting each step in a progress notification. Every tool is
// resolved before the first step runs, so a missing one leaves the tree
// untouched.
func (g *compdbGenerator) generate(
	ctx context.Context, dir string, plan compdbPlan, label string,
) (compdbResult, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	bins, err := g.resolveTools(ctx, dir, plan)
	if err != nil {
		return compdbResult{}, err
	}
	total := int64(len(plan.steps) + 1)
	id, _ := g.notify.Notify(browserapi.LevelInfo, "Generating %s for %s", compileCommandsName, label)
	progress := func(msg string, step int64) {
		_ = g.notify.UpdateNotificationProgress(id, msg, step, total)
	}
	fail := func(err error) (compdbResult, error) {
		progress("Generating "+compileCommandsName+" failed", total)
		return compdbResult{}, err
	}

	var piped []byte
	for i, step := range plan.steps {
		progress("Running "+filepath.Base(step.tool), int64(i))
		feeds := i+1 < len(plan.steps) && plan.steps[i+1].pipe
		out, err := runStep(ctx, g.exec, dir, bins[i], step, piped, feeds)
		if err != nil {
			return fail(err)
		}
		piped = out
	}

	res := compdbResult{output: filepath.Join(dir, plan.output), emptyHint: plan.emptyHint}
	if res.entries, err = countEntries(g.fs, res.output); err != nil {
		return fail(fmt.Errorf("`%s` finished without a readable %s: %w", plan, plan.output, err))
	}
	// A clangd still starting reads the database when the files open; a
	// failed one has nothing to restart.
	if phase, _, _ := g.state.snapshot(); phase == "running" && res.entries > 0 {
		progress("Restarting clangd", int64(len(plan.steps)))
		if err := g.restart(ctx); err != nil {
			return fail(fmt.Errorf("wrote %s, but restarting clangd failed: %w", plan.output, err))
		}
		res.restarted = true
	}
	progress(compileCommandsName+" ready", total)
	return res, nil
}

// resolveTools returns the program each step of plan runs, after making
// sure the programs the steps run in turn are there too.
func (g *compdbGenerator) resolveTools(ctx context.Context, dir string, plan compdbPlan) ([]string, error) {
	bins := make([]string, len(plan.steps))
	for i, s := range plan.steps {
		for _, name := range s.requires {
			if _, err := hostTool(ctx, g.exec, name); err != nil {
				return nil, err
			}
		}
		switch {
		case s.tool == bearTool:
			if bins[i] = g.bear(ctx); bins[i] == "" {
				return nil, errors.New("bear is not installed on the workspace host: " +
					"run `pkg install ccpp`, which ships it")
			}
		case strings.HasPrefix(s.tool, "./"):
			bins[i] = filepath.Join(dir, s.tool)
		default:
			bin, err := hostTool(ctx, g.exec, s.tool)
			if err != nil {
				return nil, err
			}
			bins[i] = bin
		}
	}
	return bins, nil
}

// hostTool resolves name on the workspace host's PATH. A host that
// cannot tell gets the bare name, which it resolves when it starts it.
func hostTool(ctx context.Context, exec workspaceapi.Executor, name string) (string, error) {
	lookCtx, cancel := context.WithTimeout(ctx, resolutionTimeout)
	defer cancel()
	bin, err := lookPath(lookCtx, exec, name)
	if errors.Is(err, errors.ErrUnsupported) {
		return name, nil
	}
	if err != nil {
		return "", fmt.Errorf("`%s` is not on the workspace host's PATH", name)
	}
	return bin, nil
}

// transcriptLines is how much of a failed step's output an error shows.
const transcriptLines = 20

// runStep runs step as bin in dir, on stdin when it is piped, and
// returns its standard output when it feeds the next step. Otherwise its
// output only surfaces, cut to its last lines, when it fails.
func runStep(
	ctx context.Context, exec workspaceapi.Executor, dir, bin string,
	step compdbStep, stdin []byte, feeds bool,
) ([]byte, error) {
	var stdout, log lockedBuffer
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:        bin,
		Args:        step.args,
		Dir:         dir,
		Stdout:      &log,
		Stderr:      &log,
		Watcher:     workspaceapi.ChanProcessWatcher(ch),
		Description: step.purpose,
	}
	if feeds {
		cmd.Stdout = &stdout
	}
	if step.pipe {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if _, err := exec.Start(ctx, cmd); err != nil {
		return nil, fmt.Errorf("start `%s`: %w", step, err)
	}
	var err error
	select {
	case err = <-ch:
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("`%s` failed: %w%s", step, err, tail(log.String(), transcriptLines))
	}
	return []byte(stdout.String()), nil
}

// tail returns the last n lines of out on their own lines, "" when out
// is blank.
func tail(out string, n int) string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return ""
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return "\n" + strings.Join(lines, "\n")
}

// countEntries returns the number of compile commands in the database
// at path.
func countEntries(fs workspaceapi.FileSystem, path string) (int, error) {
	f, err := fs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var entries []json.RawMessage
	if err := json.NewDecoder(f).Decode(&entries); err != nil {
		return 0, err
	}
	return len(entries), nil
}
