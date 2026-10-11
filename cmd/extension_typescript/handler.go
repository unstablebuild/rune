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
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"unstable.build/rune/internal/component/markdown"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/vctrl"
	"unstable.build/rune/internal/workspace/walkdir"
)

// tsCommandName is the console command exposed by this extension.
const tsCommandName = "ts"

// tsRootScanDepth bounds the scans for projects below a directory, so a
// deep tree cannot stall the console.
const tsRootScanDepth = 6

var tsSubcommandNames = []string{"check", "install", "restart", "run", "test", "version"}

var tsManual = textapi.CommandManual{
	Name:     tsCommandName,
	Summary:  "Type-check, install, and run TypeScript and JavaScript projects.",
	Synopsis: "<command> [<args>]",
	Commands: []textapi.CommandManual{
		{Name: "check", Summary: "Type-check a project, or every project below a directory without a tsconfig.json.",
			Synopsis: "[<project>]"},
		{Name: "install", Summary: "Install dependencies with the package's package manager.",
			Synopsis: "[<package>/] [<args>]"},
		{Name: "run", Summary: "Run a package.json script, or list them.",
			Synopsis: "[<package>/] [<script> [<args>]]"},
		{Name: "test", Summary: "Run the package.json test script, or bun test in a bun project without one.",
			Synopsis: "[<package>/] [<args>]"},
		{Name: "restart", Summary: "Restart the TypeScript language servers."},
		{Name: "version", Summary: "Show the language server's and the project's TypeScript versions.",
			Synopsis: "[<project>]"},
	},
}

// tsProjectsNote says which project a subcommand works on, which the
// synopses cannot.
const tsProjectsNote = "Without a project, a subcommand works in the nearest directory above the " +
	"focused file that has the file it needs: a tsconfig.json for `check`, a package.json " +
	"for the others. A package must be `.` or contain a `/`, such as `packages/api/`, so " +
	"that it is not taken for a script or an argument of the test runner."

var errNoServer = errors.New("no TypeScript 7 compiler found; add typescript@7 to the project, " +
	"install the typescript package, or set extensions.typescript.config.lsp_path")

// focusTracker remembers the directory of the workspace file focused
// last, which the console falls back to when no project is named. Moving
// the focus to the console does not clear it.
type focusTracker struct {
	wsDir string

	mu  sync.Mutex
	dir string
}

var _ textapi.EventHandler = (*focusTracker)(nil)

func (f *focusTracker) Handle(_ context.Context, ev textapi.Event) bool {
	if ev.Type != textapi.EventTypeFocus {
		return false
	}
	dir := filepath.Dir(ev.URI.Path())
	if !insideDir(f.wsDir, dir) {
		return false
	}
	f.mu.Lock()
	f.dir = dir
	f.mu.Unlock()
	return false
}

// lastDir returns the directory of the file focused last, or the
// workspace root when none has been.
func (f *focusTracker) lastDir() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dir == "" {
		return f.wsDir
	}
	return f.dir
}

// tsHandler runs the `ts` console command against the projects of the
// workspace.
type tsHandler struct {
	exec  workspaceapi.Executor
	fs    workspaceapi.FileSystem
	wsURI workspaceapi.URI
	cwd   string
	focus *focusTracker
	// server resolves the native compiler of the server rooted at
	// rootDir lazily, so one installed after the workspace opened is
	// still found.
	server  func(ctx context.Context, rootDir string) string
	restart func(ctx context.Context) error
}

var _ textapi.REPLHandler = (*tsHandler)(nil)

// tsHandlerConfig groups the dependencies of the console handler.
type tsHandlerConfig struct {
	exec    workspaceapi.Executor
	fs      workspaceapi.FileSystem
	wsURI   workspaceapi.URI
	focus   *focusTracker
	server  func(ctx context.Context, rootDir string) string
	restart func(ctx context.Context) error
}

// newTSHandler builds the `ts` console command. cfg.focus must be the
// tracker subscribed to the editor's focus events.
func newTSHandler(cfg tsHandlerConfig) (textapi.CommandManual, textapi.REPLHandler) {
	if cfg.focus == nil {
		panic("newTSHandler: a focus tracker is required")
	}
	return tsManual, &tsHandler{
		exec:    cfg.exec,
		fs:      cfg.fs,
		wsURI:   cfg.wsURI,
		cwd:     cfg.wsURI.Path(),
		focus:   cfg.focus,
		server:  cfg.server,
		restart: cfg.restart,
	}
}

func (h *tsHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(cmd.Args) == 0 {
		return markdownOutput(usageMarkdown(tsManual)), nil
	}
	sub, args := cmd.Args[0], cmd.Args[1:]
	switch sub {
	case "help":
		return h.Help(ctx, args)
	case "check":
		return h.check(ctx, args)
	case "install":
		dir, rest := h.packageArg(args)
		return h.install(ctx, dir, rest)
	case "run":
		dir, rest := h.packageArg(args)
		if len(rest) == 0 {
			return h.listScripts(dir)
		}
		return h.runScript(ctx, dir, rest[0], rest[1:])
	case "test":
		dir, rest := h.packageArg(args)
		return h.test(ctx, dir, rest)
	case "restart":
		if err := h.restart(ctx); err != nil {
			return nil, err
		}
		return markdownOutput("Restarted the TypeScript language servers."), nil
	case "version":
		return h.version(ctx, args)
	}
	return nil, fmt.Errorf("unknown command: %s", sub)
}

func (h *tsHandler) check(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("usage: %s check [<project>]", tsCommandName)
	}
	target := h.nearest(h.focus.lastDir(), tsconfigName)
	if len(args) == 1 {
		var err error
		if target, err = h.resolvePath(args[0]); err != nil {
			return nil, err
		}
	}
	configs, scanned, err := h.tsconfigs(ctx, target)
	if err != nil {
		return nil, err
	}
	bin := h.server(ctx, h.serverRoot(target))
	if bin == "" {
		return nil, errNoServer
	}
	var outs []string
	var errs []error
	for _, config := range configs {
		out, err := h.runCapture(ctx, h.cwd, bin, checkArgs(h.fs, config, h.rel(config))...)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if out != "" {
			outs = append(outs, out)
		}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if len(outs) > 0 {
		return codeOutput(strings.Join(outs, "\n")), nil
	}
	if !scanned {
		return markdownOutput(fmt.Sprintf("No type errors in `%s`.", h.rel(target))), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "No type errors in the projects below `%s`:\n\n", h.rel(target))
	for _, config := range configs {
		fmt.Fprintf(&b, "- `%s`\n", h.rel(config))
	}
	return markdownOutput(b.String()), nil
}

// tsconfigs returns the project configs `check` checks for target:
// target itself when it is a file, else its tsconfig.json, else every
// tsconfig.json below it, in which case scanned is true.
func (h *tsHandler) tsconfigs(
	ctx context.Context, target string,
) (configs []string, scanned bool, err error) {
	info, err := h.fs.Stat(target)
	if err != nil {
		return nil, false, err
	}
	if !info.IsDir() {
		return []string{target}, false, nil
	}
	if config := filepath.Join(target, tsconfigName); isFile(h.fs, config) {
		return []string{config}, false, nil
	}
	dirs, err := h.projectDirs(ctx, target, []string{tsconfigName})
	if err != nil {
		return nil, false, err
	}
	if len(dirs) == 0 {
		return nil, false, fmt.Errorf("no %s in `%s` or below", tsconfigName, h.rel(target))
	}
	for _, dir := range dirs {
		configs = append(configs, filepath.Join(dir, tsconfigName))
	}
	return configs, true, nil
}

func (h *tsHandler) install(
	ctx context.Context, dir string, args []string,
) (iterator.Iterator[component.Responsive], error) {
	pkg, err := h.manifest(dir)
	if err != nil {
		return nil, err
	}
	manager := detectPackageManager(h.fs, dir, pkg)
	out, err := h.runCapture(ctx, dir, manager, append([]string{"install"}, args...)...)
	if err != nil {
		return nil, err
	}
	return h.codeOutputIn(dir, out), nil
}

func (h *tsHandler) listScripts(dir string) (iterator.Iterator[component.Responsive], error) {
	pkg, err := h.manifest(dir)
	if err != nil {
		return nil, err
	}
	manifest := filepath.Join(h.rel(dir), "package.json")
	if len(pkg.Scripts) == 0 {
		return markdownOutput(fmt.Sprintf("`%s` defines no scripts.", manifest)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Scripts of `%s`\n\n", manifest)
	for _, name := range pkg.scriptNames() {
		fmt.Fprintf(&b, "- `%s`\n  `%s`\n", name, pkg.Scripts[name])
	}
	return markdownOutput(b.String()), nil
}

func (h *tsHandler) runScript(
	ctx context.Context, dir, script string, args []string,
) (iterator.Iterator[component.Responsive], error) {
	pkg, err := h.manifest(dir)
	if err != nil {
		return nil, err
	}
	if _, ok := pkg.Scripts[script]; !ok {
		return nil, fmt.Errorf("%s has no %q script", filepath.Join(h.rel(dir), "package.json"), script)
	}
	manager := detectPackageManager(h.fs, dir, pkg)
	out, err := h.runCapture(ctx, dir, manager, scriptArgs(manager, script, args)...)
	if err != nil {
		return nil, err
	}
	return h.codeOutputIn(dir, out), nil
}

// test runs the package's test script. A bun package without one is
// tested by bun's built-in runner, which is how `bun init` sets it up.
func (h *tsHandler) test(
	ctx context.Context, dir string, args []string,
) (iterator.Iterator[component.Responsive], error) {
	pkg, err := h.manifest(dir)
	if err != nil {
		return nil, err
	}
	if _, ok := pkg.Scripts["test"]; ok || detectPackageManager(h.fs, dir, pkg) != "bun" {
		return h.runScript(ctx, dir, "test", args)
	}
	out, err := h.runCapture(ctx, dir, "bun", append([]string{"test"}, args...)...)
	if err != nil {
		return nil, err
	}
	return h.codeOutputIn(dir, out), nil
}

// version reports the compiler of the language server for the project
// and, when it differs, the typescript the project installed: tsgo
// analyzes a project pinned to an older release with TypeScript 7
// semantics.
func (h *tsHandler) version(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) > 1 {
		return nil, fmt.Errorf("usage: %s version [<project>]", tsCommandName)
	}
	dir := h.nearest(h.focus.lastDir(), "package.json")
	if len(args) == 1 {
		p, err := h.resolvePath(args[0])
		if err != nil {
			return nil, err
		}
		dir = p
		if isFile(h.fs, p) {
			dir = filepath.Dir(p)
		}
	}
	bin := h.server(ctx, h.serverRoot(dir))
	if bin == "" {
		return nil, errNoServer
	}
	out, err := commandOutput(ctx, h.exec, bin, "--version")
	if err != nil {
		return nil, err
	}
	server := strings.TrimPrefix(strings.TrimSpace(out), "Version ")
	var b strings.Builder
	fmt.Fprintf(&b, "- **Language server:** TypeScript %s (`%s`)\n", server, bin)
	if version, at, ok := projectTypeScript(h.fs, dir); ok && version != server {
		fmt.Fprintf(&b, "- **Project:** TypeScript %s (`%s`)\n", version, h.display(at))
	}
	return markdownOutput(b.String()), nil
}

func (h *tsHandler) manifest(dir string) (packageJSON, error) {
	pkg, err := readPackageJSON(h.fs, dir)
	if errors.Is(err, os.ErrNotExist) {
		return packageJSON{}, fmt.Errorf("no package.json in %s", dir)
	}
	return pkg, err
}

// packageArg splits a leading package argument off args. It counts only
// if it is "." or contains a slash and names a workspace directory with a
// package.json, so a script name or a test runner's path filter is left
// alone. Without one, the package is the focused file's.
func (h *tsHandler) packageArg(args []string) (string, []string) {
	if len(args) > 0 && (args[0] == "." || strings.Contains(args[0], "/")) {
		dir, err := h.resolvePath(args[0])
		if err == nil && isFile(h.fs, filepath.Join(dir, "package.json")) {
			return dir, args[1:]
		}
	}
	return h.nearest(h.focus.lastDir(), "package.json"), args
}

// resolvePath resolves a path argument against the workspace root, which
// it must stay within.
func (h *tsHandler) resolvePath(p string) (string, error) {
	abs := filepath.Clean(p)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(h.cwd, p)
	}
	if !insideDir(h.cwd, abs) {
		return "", fmt.Errorf("%s is outside the workspace", p)
	}
	return abs, nil
}

// nearest walks up from dir, a workspace directory, to the first
// directory holding the file name, falling back to the workspace root.
func (h *tsHandler) nearest(dir, name string) string {
	for d := dir; insideDir(h.cwd, d); d = filepath.Dir(d) {
		if isFile(h.fs, filepath.Join(d, name)) {
			return d
		}
		if d == h.cwd {
			break
		}
	}
	return h.cwd
}

// serverRoot returns the root of the language server that serves the
// files of dir, the one the extension brings up for them.
func (h *tsHandler) serverRoot(dir string) string {
	// FindOutermostProjectRoot walks up from a file, so probe with a name
	// inside dir to have the walk start at dir itself.
	uri, err := h.fs.URI(filepath.Join(dir, "__probe__.ts"))
	if err != nil {
		return h.cwd
	}
	if root, ok := langext.FindOutermostProjectRoot(h.fs, h.wsURI, uri, tsMarkers); ok {
		return root.Dir
	}
	return h.cwd
}

// projectDirs lists the directories at or below dir holding any of the
// markers, skipping what git ignores, node_modules among it.
func (h *tsHandler) projectDirs(ctx context.Context, dir string, markers []string) ([]string, error) {
	uri, err := h.fs.URI(dir)
	if err != nil {
		return nil, err
	}
	var filter walkdir.Filter
	if ignore, err := vctrl.LoadGitignore(h.fs); err != nil {
		slog.Warn("typescript project scan ignore rules unavailable", "error", err)
	} else {
		filter = relFilter{filter: ignore, prefix: h.rel(dir)}
	}
	roots, err := iterator.ToSlice(ctx, langext.FindProjectRoots(h.fs, uri, markers, filter, tsRootScanDepth))
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(roots))
	for _, r := range roots {
		dirs = append(dirs, r.Dir)
	}
	return dirs, nil
}

// rel names path relative to the workspace root, "." for the root.
func (h *tsHandler) rel(path string) string {
	rel, err := filepath.Rel(h.cwd, path)
	if err != nil {
		return path
	}
	return rel
}

// display names path relative to the workspace root when it is inside
// it, absolutely otherwise.
func (h *tsHandler) display(path string) string {
	if insideDir(h.cwd, path) {
		return h.rel(path)
	}
	return path
}

// codeOutputIn renders the output of a command run in dir, naming the
// package unless it is the workspace root.
func (h *tsHandler) codeOutputIn(dir, out string) iterator.Iterator[component.Responsive] {
	if dir == h.cwd {
		return codeOutput(out)
	}
	return markdownOutput(fmt.Sprintf("In `%s`:\n\n```\n%s\n```", h.rel(dir), out))
}

// Complete offers the subcommands, then project or package arguments,
// and the package.json scripts after `run`.
func (h *tsHandler) Complete(
	ctx context.Context, _ string, args []string,
) (iterator.Iterator[string], error) {
	if len(args) <= 1 {
		filter := ""
		if len(args) == 1 {
			filter = args[0]
		}
		return iterator.FromSlice(filterNames(tsSubcommandNames, filter)), nil
	}
	sub, rest := args[0], args[1:]
	prefix := rest[len(rest)-1]
	var names []string
	switch {
	case len(rest) == 1 && sub == "check":
		names = h.projectArgs(ctx, []string{tsconfigName})
	case len(rest) == 1 && sub == "version":
		names = h.projectArgs(ctx, tsMarkers)
	case len(rest) == 1 && (sub == "install" || sub == "test"):
		names = h.projectArgs(ctx, []string{"package.json"})
	case len(rest) == 1 && sub == "run":
		names = append(h.projectArgs(ctx, []string{"package.json"}),
			h.scriptNames(h.nearest(h.focus.lastDir(), "package.json"))...)
	case len(rest) == 2 && sub == "run":
		if dir, script := h.packageArg(rest); len(script) == 1 {
			names = h.scriptNames(dir)
		}
	}
	return iterator.FromSlice(filterNames(names, prefix)), nil
}

// projectArgs lists the workspace directories holding any of the markers
// as project arguments: "." for the root, "<dir>/" below it.
func (h *tsHandler) projectArgs(ctx context.Context, markers []string) []string {
	dirs, err := h.projectDirs(ctx, h.cwd, markers)
	if err != nil {
		return nil
	}
	args := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir == h.cwd {
			args = append(args, ".")
			continue
		}
		args = append(args, h.rel(dir)+"/")
	}
	return args
}

func (h *tsHandler) scriptNames(dir string) []string {
	pkg, err := readPackageJSON(h.fs, dir)
	if err != nil {
		return nil
	}
	return pkg.scriptNames()
}

// Help renders the manual for the command tree, descending into
// subcommands named in args.
func (h *tsHandler) Help(
	_ context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	man := tsManual
	for _, a := range args {
		sub, ok := findSubcommand(man, a)
		if !ok {
			break
		}
		man = sub
	}
	return markdownOutput(usageMarkdown(man)), nil
}

// runCapture runs bin in dir and returns its stdout and stderr as one
// transcript: test runners and package managers report on either stream.
func (h *tsHandler) runCapture(ctx context.Context, dir, bin string, args ...string) (string, error) {
	label := strings.Join(append([]string{filepath.Base(bin)}, args...), " ")
	if dir != h.cwd {
		label = fmt.Sprintf("%s (in %s)", label, h.rel(dir))
	}
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
	if _, err := h.exec.Start(ctx, cmd); err != nil {
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
	b.WriteString(tsProjectsNote)
	b.WriteByte('\n')
	return b.String()
}
