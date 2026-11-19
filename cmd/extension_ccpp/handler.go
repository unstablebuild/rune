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
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/extension/langext"
)

// ccppCommandName is the console command exposed by this extension.
const ccppCommandName = "ccpp"

var ccppSubcommandNames = []string{"compdb", "restart", "status", "version"}

var ccppManual = textapi.CommandManual{
	Name:     ccppCommandName,
	Summary:  "Inspect and steer the clangd language server for C and C++.",
	Synopsis: "<command> [<args>]",
	Commands: []textapi.CommandManual{
		{Name: "status", Summary: "Show the clangd in use and the compilation database in effect for the focused file."},
		{
			Name:     "compdb",
			Summary:  "List the compilation databases of the focused file's project, marking the active one; pin or unpin one.",
			Synopsis: "[use <dir> | clear]",
			Commands: []textapi.CommandManual{
				{Name: "use", Summary: "Make clangd read compile_commands.json from a directory and restart it.", Synopsis: "<dir>"},
				{Name: "clear", Summary: "Drop the compilation database override and restart clangd."},
			},
		},
		{Name: "restart", Summary: "Restart the clangd language server."},
		{Name: "version", Summary: "Show the clangd version."},
	},
}

// ccppHandlerConfig groups the dependencies of the console handler.
type ccppHandlerConfig struct {
	exec   workspaceapi.Executor
	fs     workspaceapi.FileSystem
	cfg    config.Config
	notify browserapi.Notifications
	wsRoot langext.Root
	focus  *focusTracker
	compdb *compdbSetting
	state  *serverState
	// clangd and bear resolve their binaries lazily, so one installed
	// after the workspace opened is still found.
	clangd  func(ctx context.Context) string
	bear    func(ctx context.Context) string
	restart func(ctx context.Context) error
}

type ccppHandler struct {
	ccppHandlerConfig
}

var _ textapi.REPLHandler = (*ccppHandler)(nil)

// newCcppHandler builds the `ccpp` console command and its manual.
// cfg.focus must be the tracker subscribed to the editor's focus events.
func newCcppHandler(cfg ccppHandlerConfig) (textapi.CommandManual, textapi.REPLHandler) {
	if cfg.focus == nil || cfg.compdb == nil || cfg.state == nil {
		panic("newCcppHandler: focus tracker, compdb setting and server state are required")
	}
	return ccppManual, &ccppHandler{ccppHandlerConfig: cfg}
}

func (h *ccppHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return markdownOutput(usageMarkdown(ccppManual)), nil
	}
	sub, args := cmd.Args[0], cmd.Args[1:]
	switch sub {
	case "help":
		return h.Help(ctx, args)
	case "status":
		return h.status(ctx)
	case "compdb":
		return h.compdbCommand(ctx, args)
	case "restart":
		return h.restartServer(ctx, "")
	case "version":
		return h.version(ctx)
	}
	return nil, fmt.Errorf("unknown command: %s", sub)
}

func (h *ccppHandler) status(ctx context.Context) (iterator.Iterator[component.Responsive], error) {
	var b strings.Builder
	b.WriteString("### clangd\n\n")
	bin := h.clangd(ctx)
	if bin == "" {
		b.WriteString("- **Path:** not found; set `extensions.ccpp.config.lsp_path`\n")
	} else {
		fmt.Fprintf(&b, "- **Path:** `%s`", bin)
		if version, err := commandOutput(ctx, h.exec, h.wsRoot.Dir, bin, "--version"); err == nil {
			fmt.Fprintf(&b, " (%s)", firstLine(version))
		}
		b.WriteByte('\n')
	}
	phase, opts, err := h.state.snapshot()
	switch phase {
	case "":
		b.WriteString("- **Server:** not started; it starts when a C or C++ file opens\n")
	case "starting":
		b.WriteString("- **Server:** starting\n")
	case "running":
		fmt.Fprintf(&b, "- **Server:** running as `%s`\n", clangdCommand(opts))
	case "failed":
		fmt.Fprintf(&b, "- **Server:** failed to start: %v\n", err)
	}

	file := h.focus.lastFile()
	if file == "" || !isCcppFile(mustURI(file)) {
		b.WriteString("\nFocus a C or C++ file to see the compilation database in effect for it.\n")
		return markdownOutput(b.String()), nil
	}
	fmt.Fprintf(&b, "\n### `%s`\n\n", h.rel(file))
	project := projectDirOf(h.fs, h.wsRoot.Dir, file)
	markers := projectMarkers(h.fs, project)
	fmt.Fprintf(&b, "- **Project:** `%s`", h.rel(project))
	if names := markerNames(markers); len(names) > 0 {
		fmt.Fprintf(&b, " (%s)", strings.Join(names, ", "))
	}
	b.WriteByte('\n')
	override, _, storeErr := h.compdb.get(ctx)
	if storeErr != nil {
		fmt.Fprintf(&b, "- **Override:** unreadable: %v\n", storeErr)
	}
	configured, _ := readCompileCommandsDir(h.cfg, h.notify)
	if configured != "" && !filepath.IsAbs(configured) {
		configured = filepath.Join(h.wsRoot.Dir, configured)
	}
	src := compdbInEffect(h.fs, override, configured, file)
	b.WriteString("- **Compilation database:** ")
	switch src.Kind {
	case "override":
		fmt.Fprintf(&b, "`%s` (set with `ccpp compdb use`)", h.rel(filepath.Join(src.Path, compileCommandsName)))
	case "config":
		fmt.Fprintf(&b, "`%s` (extensions.ccpp.config.compile_commands_dir)", h.rel(filepath.Join(src.Path, compileCommandsName)))
	case compileCommandsName:
		fmt.Fprintf(&b, "`%s` (found by clangd)", h.rel(filepath.Join(src.Path, compileCommandsName)))
	case "compile_flags.txt":
		fmt.Fprintf(&b, "`%s` (flags file found by clangd)", h.rel(src.Path))
	default:
		b.WriteString("none; clangd falls back to `clang <file>` with no project flags. ")
		if candidates := compdbCandidates(h.fs, project); len(candidates) > 0 {
			fmt.Fprintf(&b, "Pick one with `ccpp compdb use %s`.", h.rel(candidates[0]))
		} else {
			b.WriteString(compdbHowTo(markers, func() string { return h.bear(ctx) }))
		}
	}
	b.WriteByte('\n')
	return markdownOutput(b.String()), nil
}

func (h *ccppHandler) compdbCommand(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return h.listCompdbs(ctx)
	}
	switch args[0] {
	case "use":
		if len(args) != 2 {
			return nil, fmt.Errorf("usage: %s compdb use <dir>", ccppCommandName)
		}
		dir, err := h.resolvePath(args[1])
		if err != nil {
			return nil, err
		}
		if !isFile(h.fs, filepath.Join(dir, compileCommandsName)) {
			return nil, fmt.Errorf("no %s in `%s`", compileCommandsName, h.rel(dir))
		}
		if err := h.compdb.set(ctx, dir); err != nil {
			return nil, fmt.Errorf("remember compilation database: %w", err)
		}
		return h.restartServer(ctx, fmt.Sprintf("Using `%s`.", h.rel(filepath.Join(dir, compileCommandsName))))
	case "clear":
		if err := h.compdb.clear(ctx); err != nil {
			return nil, fmt.Errorf("forget compilation database: %w", err)
		}
		return h.restartServer(ctx, "Dropped the compilation database override; clangd finds one per file again.")
	}
	return nil, fmt.Errorf("unknown compdb command: %s", args[0])
}

func (h *ccppHandler) listCompdbs(ctx context.Context) (iterator.Iterator[component.Responsive], error) {
	project := h.wsRoot.Dir
	if file := h.focus.lastFile(); file != "" {
		project = projectDirOf(h.fs, h.wsRoot.Dir, file)
	}
	active, _, err := h.compdb.get(ctx)
	if err != nil {
		return nil, fmt.Errorf("read compilation database override: %w", err)
	}
	if active == "" {
		if configured, ok := readCompileCommandsDir(h.cfg, h.notify); ok {
			active, _ = h.resolvePath(configured)
		}
	}
	candidates := compdbCandidates(h.fs, project)
	if len(candidates) == 0 {
		howTo := compdbHowTo(projectMarkers(h.fs, project), func() string { return h.bear(ctx) })
		return markdownOutput(fmt.Sprintf("No %s below `%s`. %s", compileCommandsName, h.rel(project), howTo)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Compilation databases below `%s`\n\n", h.rel(project))
	for _, dir := range candidates {
		fmt.Fprintf(&b, "- `%s`", h.rel(dir))
		if dir == active {
			b.WriteString(" (active)")
		}
		b.WriteByte('\n')
	}
	if active == "" {
		b.WriteString("\nclangd picks the nearest one above each file; `ccpp compdb use <dir>` pins one.\n")
	}
	return markdownOutput(b.String()), nil
}

// restartServer reinitializes clangd after prefix, the outcome of the
// change that calls for it. A server that never started has nothing to
// restart: the next open starts it with the new settings.
func (h *ccppHandler) restartServer(
	ctx context.Context, prefix string,
) (iterator.Iterator[component.Responsive], error) {
	phase, _, _ := h.state.snapshot()
	if phase == "" {
		msg := "clangd is not running; it starts when a C or C++ file opens."
		if prefix != "" {
			msg = prefix + " clangd will use it when it starts."
		}
		return markdownOutput(msg), nil
	}
	if err := h.restart(ctx); err != nil {
		return nil, err
	}
	msg := "clangd restarted."
	if prefix != "" {
		msg = prefix + " " + msg
	}
	return markdownOutput(msg), nil
}

func (h *ccppHandler) version(ctx context.Context) (iterator.Iterator[component.Responsive], error) {
	bin := h.clangd(ctx)
	if bin == "" {
		return nil, fmt.Errorf("clangd not found on the workspace host; " +
			"set extensions.ccpp.config.lsp_path in your config")
	}
	out, err := commandOutput(ctx, h.exec, h.wsRoot.Dir, bin, "--version")
	if err != nil {
		return nil, err
	}
	return codeOutput(out), nil
}

// Complete offers the subcommands, then the compdb actions, then the
// candidate directories after `compdb use`.
func (h *ccppHandler) Complete(
	_ context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) <= 1 {
		filter := ""
		if len(args) == 1 {
			filter = args[0]
		}
		return iterator.FromSlice(filterNames(ccppSubcommandNames, filter)), nil
	}
	if args[0] != "compdb" {
		return iterator.FromSlice[string](nil), nil
	}
	rest := args[1:]
	prefix := rest[len(rest)-1]
	switch {
	case len(rest) == 1:
		return iterator.FromSlice(filterNames([]string{"clear", "use"}, prefix)), nil
	case len(rest) == 2 && rest[0] == "use":
		project := h.wsRoot.Dir
		if file := h.focus.lastFile(); file != "" {
			project = projectDirOf(h.fs, h.wsRoot.Dir, file)
		}
		var names []string
		for _, dir := range compdbCandidates(h.fs, project) {
			names = append(names, h.rel(dir))
		}
		return iterator.FromSlice(filterNames(names, prefix)), nil
	}
	return iterator.FromSlice[string](nil), nil
}

// Help renders the manual for the command tree, descending into
// subcommands named in args.
func (h *ccppHandler) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	man := ccppManual
	for _, a := range args {
		sub, ok := findSubcommand(man, a)
		if !ok {
			break
		}
		man = sub
	}
	return markdownOutput(usageMarkdown(man)), nil
}

// resolvePath resolves a path argument against the workspace root, which
// it must stay within.
func (h *ccppHandler) resolvePath(p string) (string, error) {
	abs := filepath.Clean(p)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(h.wsRoot.Dir, p)
	}
	if !insideDir(h.wsRoot.Dir, abs) {
		return "", fmt.Errorf("%s is outside the workspace", p)
	}
	return abs, nil
}

// rel names path relative to the workspace root, "." for the root and
// absolutely when it is outside.
func (h *ccppHandler) rel(path string) string {
	if !insideDir(h.wsRoot.Dir, path) {
		return path
	}
	rel, err := filepath.Rel(h.wsRoot.Dir, path)
	if err != nil {
		return path
	}
	return rel
}

func mustURI(path string) workspaceapi.URI {
	uri, err := workspaceapi.ParseURI("file://" + path)
	if err != nil {
		return workspaceapi.URI{}
	}
	return uri
}

func firstLine(s string) string {
	first, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return first
}

// commandOutput runs bin in dir and returns its stdout and stderr as one
// transcript.
func commandOutput(
	ctx context.Context, exec workspaceapi.Executor, dir, bin string, args ...string,
) (string, error) {
	label := strings.Join(append([]string{filepath.Base(bin)}, args...), " ")
	var out lockedBuffer
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    bin,
		Args:    args,
		Dir:     dir,
		Stdout:  &out,
		Stderr:  &out,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	}
	if _, err := exec.Start(ctx, cmd); err != nil {
		return "", fmt.Errorf("start %s: %w", label, err)
	}
	var runErr error
	select {
	case runErr = <-ch:
	case <-ctx.Done():
		runErr = ctx.Err()
	}
	text := strings.TrimRight(out.String(), "\n")
	if runErr != nil {
		return "", fmt.Errorf("%s: %w\n%s", label, runErr, text)
	}
	return text, nil
}

// lockedBuffer collects a process's stdout and stderr, which the
// executor may copy concurrently.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func codeOutput(text string) iterator.Iterator[component.Responsive] {
	return markdownOutput(fmt.Sprintf("```\n%s\n```", text))
}

// markdownOutput falls back to a plain responsive string when the
// markdown parser rejects the content.
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

func findSubcommand(man textapi.CommandManual, name string) (textapi.CommandManual, bool) {
	for _, c := range man.Commands {
		if c.Name == name {
			return c, true
		}
	}
	return textapi.CommandManual{}, false
}

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
