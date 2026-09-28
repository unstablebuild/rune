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
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/vctrl"
)

// pyCommandName is the top-level REPL command exposed by this extension.
const pyCommandName = "python"

// uvRoute maps a `python` subcommand to the uv argv prefix it expands
// to. The user-supplied trailing args are appended verbatim. A nil
// prefix means the subcommand name is itself the uv subcommand.
var uvRoutes = map[string][]string{
	// Python interpreter management.
	"install":   {"python", "install"},
	"list":      {"python", "list"},
	"find":      {"python", "find"},
	"pin":       {"python", "pin"},
	"uninstall": {"python", "uninstall"},
	// Projects.
	"init":   {"init"},
	"add":    {"add"},
	"remove": {"remove"},
	"sync":   {"sync"},
	"lock":   {"lock"},
	"tree":   {"tree"},
	"build":  {"build"},
	// Run.
	"run": {"run"},
	// Tools.
	"tool": {"tool"},
	// pip interface.
	"pip":  {"pip"},
	"venv": {"venv"},
	// Utility.
	"cache": {"cache"},
	"self":  {"self"},
}

// pySubcommandNames is the deterministic completion order at depth 0.
var pySubcommandNames = []string{
	"add", "build", "cache", "disable", "enable", "find", "init",
	"install", "list", "lock", "pin", "pip", "remove", "run", "self",
	"status", "sync", "tool", "tree", "uninstall", "venv",
}

var pyManual = textapi.CommandManual{
	Name:     pyCommandName,
	Summary:  "Manage the Python toolchain through uv.",
	Synopsis: "<command> [<args>]",
	Commands: []textapi.CommandManual{
		{Name: "install", Summary: "Install a Python interpreter version.", Synopsis: "[<version>]"},
		{Name: "list", Summary: "List available and installed Python versions."},
		{Name: "find", Summary: "Find an installed Python interpreter."},
		{Name: "pin", Summary: "Pin the project to a Python version.", Synopsis: "<version>"},
		{Name: "uninstall", Summary: "Uninstall a Python interpreter version.", Synopsis: "<version>"},
		{Name: "init", Summary: "Initialize a new uv project.", Synopsis: "[<path>]"},
		{Name: "add", Summary: "Add dependencies to the project.", Synopsis: "<package>..."},
		{Name: "remove", Summary: "Remove dependencies from the project.", Synopsis: "<package>..."},
		{Name: "sync", Summary: "Sync the environment with the project lockfile."},
		{Name: "lock", Summary: "Update the project lockfile."},
		{Name: "tree", Summary: "Display the project dependency tree."},
		{Name: "build", Summary: "Build the project into distributable archives."},
		{Name: "run", Summary: "Run a command in the project environment.", Synopsis: "<command> [<args>]"},
		{Name: "tool", Summary: "Run and manage tools provided by Python packages.", Synopsis: "<args>"},
		{Name: "pip", Summary: "Manage packages with a pip-compatible interface.", Synopsis: "<args>"},
		{Name: "venv", Summary: "Create a virtual environment.", Synopsis: "[<path>]"},
		{Name: "cache", Summary: "Manage the uv cache.", Synopsis: "<args>"},
		{Name: "self", Summary: "Manage the uv executable.", Synopsis: "<args>"},
		{
			Name:     "enable",
			Summary:  "Let Rune manage the project's Python environment.",
			Synopsis: "[<path>]",
		},
		{
			Name:     "disable",
			Summary:  "Stop Rune from managing the project's Python environment.",
			Synopsis: "[<path>]",
		},
		{
			Name:     "status",
			Summary:  "Show whether Rune manages the project's Python environment.",
			Synopsis: "[<path>]",
		},
	},
}

var uvNestedSubcommands = map[string][]string{
	"tool":  {"dir", "install", "list", "run", "uninstall", "upgrade", "update-shell"},
	"pip":   {"check", "compile", "freeze", "install", "list", "show", "sync", "tree", "uninstall"},
	"cache": {"clean", "dir", "prune", "size"},
	"self":  {"update", "version"},
}

// pyEnvSubcommands take an optional project root rather than a uv
// subcommand as their argument.
var pyEnvSubcommands = map[string]bool{
	"enable": true, "disable": true, "status": true,
}

// pyRootScanDepth bounds root completion so a deep monorepo cannot stall
// the command prompt.
const pyRootScanDepth = 6

type pyHandler struct {
	exec    workspaceapi.Executor
	notify  browserapi.Notifications
	fs      workspaceapi.FileSystem
	setting *envSetting
	syncEnv func(context.Context, langext.Root) error
	wsRoot  workspaceapi.URI
	cwd     string
}

var _ textapi.REPLHandler = (*pyHandler)(nil)

// pyHandlerConfig groups the handler dependencies; the environment
// subcommands need far more of the workspace than the uv passthrough
// ones do.
type pyHandlerConfig struct {
	exec    workspaceapi.Executor
	notify  browserapi.Notifications
	fs      workspaceapi.FileSystem
	setting *envSetting
	syncEnv func(context.Context, langext.Root) error
	wsRoot  workspaceapi.URI
}

// newPyHandler builds the `python` REPL command handler and its manual.
func newPyHandler(cfg pyHandlerConfig) (textapi.CommandManual, textapi.REPLHandler) {
	return pyManual, &pyHandler{
		exec:    cfg.exec,
		notify:  cfg.notify,
		fs:      cfg.fs,
		setting: cfg.setting,
		syncEnv: cfg.syncEnv,
		wsRoot:  cfg.wsRoot,
		cwd:     cfg.wsRoot.Path(),
	}
}

// HandleCommand routes the first arg to the matching uv subcommand.
func (h *pyHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return markdownOutput(usageMarkdown(pyManual)), nil
	}
	sub := cmd.Args[0]
	switch sub {
	case "help":
		return h.Help(ctx, cmd.Args[1:])
	case "enable":
		return h.enable(ctx, cmd.Args[1:])
	case "disable":
		return h.disable(ctx, cmd.Args[1:])
	case "status":
		return h.status(ctx, cmd.Args[1:])
	}
	prefix, ok := uvRoutes[sub]
	if !ok {
		return nil, fmt.Errorf("unknown command: %s", sub)
	}
	root := h.resolveRoot(nil)
	if managed, known, err := h.setting.get(ctx, root); err == nil && known && !managed {
		return nil, fmt.Errorf(
			"rune does not manage the Python environment in %s; "+
				"run `python enable` to turn it back on", root.Dir)
	}
	args := append(append([]string{}, prefix...), cmd.Args[1:]...)
	out, err := h.runUVCapture(ctx, args...)
	if err != nil {
		return nil, err
	}
	return markdownOutput(fmt.Sprintf("```\n%s\n```", out)), nil
}

// enable records that Rune may manage root and brings the environment up
// right away. The language server was already initialized against this
// root and the extension API has no per-root restart, so the user is
// told to reload the workspace for it to see the new interpreter.
func (h *pyHandler) enable(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	root := h.resolveRoot(args)
	if err := h.setting.set(ctx, root, true); err != nil {
		return nil, fmt.Errorf("store python environment policy: %w", err)
	}
	if h.syncEnv != nil {
		if err := h.syncEnv(ctx, root); err != nil {
			return nil, fmt.Errorf("set up python environment in %s: %w", root.Dir, err)
		}
	}
	_, _ = h.notify.Notify(browserapi.LevelInfo,
		"Reload the workspace so the Python language server picks up %s", root.Dir)
	return markdownOutput(fmt.Sprintf(
		"Rune now manages the Python environment in `%s`.\n\n"+
			"Reload the workspace so the language server picks it up.\n",
		root.Dir)), nil
}

func (h *pyHandler) disable(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	root := h.resolveRoot(args)
	if err := h.setting.set(ctx, root, false); err != nil {
		return nil, fmt.Errorf("store python environment policy: %w", err)
	}
	return markdownOutput(fmt.Sprintf(
		"Rune no longer manages the Python environment in `%s`.\n\n"+
			"It will not install an interpreter, create or sync `.venv`, or\n"+
			"prepare the debugger there. The other `python` subcommands stay\n"+
			"disabled for this project until you run `python enable`.\n",
		root.Dir)), nil
}

func (h *pyHandler) status(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	root := h.resolveRoot(args)
	managed, known, err := h.setting.get(ctx, root)
	if err != nil {
		return nil, fmt.Errorf("read python environment policy: %w", err)
	}
	state := "not asked yet"
	switch {
	case known && managed:
		state = "managed by Rune"
	case known:
		state = "not managed by Rune"
	}
	return markdownOutput(fmt.Sprintf(
		"- **Project root:** `%s`\n- **Environment:** %s\n- **Detected layout:** %s\n",
		root.Dir, state, pyProjectKindNames[detectProjectAt(ctx, h.fs, root.Dir)])), nil
}

var pyProjectKindNames = map[projectKind]string{
	kindNone:         "no Python project detected",
	kindProject:      "pyproject.toml",
	kindRequirements: "requirements file",
	kindVenvOnly:     ".venv only",
	kindScript:       "loose scripts",
}

// resolveRoot maps an optional path argument to the enclosing Python
// project root, falling back to the workspace root when the path has no
// enclosing project.
func (h *pyHandler) resolveRoot(args []string) langext.Root {
	fallback := langext.Root{Dir: h.wsRoot.Path(), URI: "file://" + h.wsRoot.Path()}
	if h.fs == nil {
		return fallback
	}
	target := h.cwd
	if len(args) > 0 && args[0] != "" {
		target = args[0]
		if !filepath.IsAbs(target) {
			target = filepath.Join(h.cwd, target)
		}
	}
	// FindProjectRoot walks up from a file, so probe with a name inside
	// the target directory to have the walk start at the directory itself.
	uri, err := h.fs.URI(filepath.Join(target, "__probe__.py"))
	if err != nil {
		return fallback
	}
	if root, ok := langext.FindProjectRoot(h.fs, h.wsRoot, uri, pyMarkers); ok {
		return root
	}
	return fallback
}

// Complete offers the `python` subcommands at depth 0 and, at depth 1,
// either a command group's nested uv subcommands (tool, pip, cache, self)
// or the workspace's project roots (enable, disable, status). Deeper
// positions defer to uv at runtime and return no completions.
func (h *pyHandler) Complete(
	_ context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) <= 1 {
		filter := ""
		if len(args) == 1 {
			filter = args[0]
		}
		return iterator.FromSlice(filterNames(pySubcommandNames, filter)), nil
	}
	if len(args) == 2 {
		if nested, ok := uvNestedSubcommands[args[0]]; ok {
			return iterator.FromSlice(filterNames(nested, args[1])), nil
		}
		if pyEnvSubcommands[args[0]] {
			prefix := args[1]
			return iterator.Filter(h.projectRootArgs(), func(root string) bool {
				return strings.HasPrefix(root, prefix)
			}), nil
		}
	}
	return iterator.FromSlice[string](nil), nil
}

// projectRootArgs streams the workspace's Python project roots as
// resolveRoot accepts them: workspace-relative, "." for the root itself.
//
// The matcher is rebuilt per call so an edited .gitignore takes effect.
func (h *pyHandler) projectRootArgs() iterator.Iterator[string] {
	if h.fs == nil {
		return iterator.Empty[string]()
	}
	ignore, err := vctrl.LoadGitignore(h.fs)
	if err != nil {
		slog.Warn("python root completion ignore rules unavailable", "error", err)
	}
	roots := langext.FindProjectRoots(h.fs, h.wsRoot, pyMarkers, ignore, pyRootScanDepth)
	return iterator.Map(roots, func(r langext.Root) string {
		if r.RelPath == "" {
			return "."
		}
		return r.RelPath
	})
}

// Help renders the manual for the command tree, descending into
// subcommands named in args.
func (h *pyHandler) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	man := pyManual
	for _, a := range args {
		sub, ok := findSubcommand(man, a)
		if !ok {
			break
		}
		man = sub
	}
	return markdownOutput(usageMarkdown(man)), nil
}

func (h *pyHandler) runUVCapture(
	ctx context.Context, args ...string,
) (string, error) {
	var stdout, stderr bytes.Buffer
	ch := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    "uv",
		Args:    args,
		Dir:     h.cwd,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Watcher: workspaceapi.ChanProcessWatcher(ch),
	}
	if _, err := h.exec.Start(ctx, cmd); err != nil {
		return "", fmt.Errorf("start uv %s: %w", strings.Join(args, " "), err)
	}
	var runErr error
	select {
	case runErr = <-ch:
	case <-ctx.Done():
		runErr = ctx.Err()
	}

	out := strings.TrimRight(stdout.String(), "\n")
	errOut := strings.TrimRight(stderr.String(), "\n")
	if runErr != nil {
		combined := strings.TrimSpace(out + "\n" + errOut)
		return "", fmt.Errorf("uv %s: %w\n%s", strings.Join(args, " "), runErr, combined)
	}
	if out == "" {
		out = errOut
	}
	return out, nil
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
