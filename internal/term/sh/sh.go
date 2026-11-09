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

package sh

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"

	"github.com/unstablebuild/rune-go-sdk/api/schemeapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/debug"
)

// New returns a repl.CommandHandler that interprets
// shell syntax (pipes, semicolons, variables, etc.)
// using mvdan/sh and delegates actual command execution
// to the underlying handler. The interpreter's working
// directory is seeded from cwd; passing the zero URI leaves
// it at the process working directory.
//
// By default external commands (those the underlying handler does not
// resolve) run via mvdan/sh's default local exec handler. Pass
// WithExecutor to instead dispatch them through a workspace Executor so
// remote workspaces (ssh, in-memory) run them on the right host.
func New(
	underlying repl.CommandHandler, cwd workspaceapi.URI, opts ...Option,
) repl.CommandHandler {
	h := &commandHandler{underlying: underlying, cwd: cwd}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Option configures a shell command handler created by New.
type Option func(*commandHandler)

// WithExecutor routes external commands through the workspace Executor
// so remote workspaces run them on the right host. The executor then
// owns the working directory, so the interpreter's local Dir is left
// unset to avoid a local os.Stat on a path that only exists remotely.
//
// exec must be non-nil; a nil executor is a programming error.
func WithExecutor(exec schemeapi.Executor) Option {
	if exec == nil {
		panic("sh.WithExecutor: nil executor")
	}
	return func(h *commandHandler) { h.exec = exec }
}

const pipeWidth = 200

// consoleBuiltins are the builtins the console runs in the interpreter.
// The interpreter takes more names for builtins, such as help, history
// and kill, which in the console are Rune commands or programs.
var consoleBuiltins = map[string]bool{
	":": true, "true": true, "false": true, "exit": true, "set": true, "shift": true,
	"unset": true, "echo": true, "printf": true, "break": true, "continue": true,
	"pwd": true, "cd": true, "wait": true, "builtin": true, "trap": true, "type": true,
	"source": true, ".": true, "command": true, "dirs": true, "pushd": true,
	"popd": true, "umask": true, "alias": true, "unalias": true, "fg": true, "bg": true,
	"getopts": true, "eval": true, "test": true, "[": true, "exec": true,
	"return": true, "read": true, "mapfile": true, "readarray": true, "shopt": true,
}

// execMarker leads the arguments of a call the interpreter would take
// for a builtin that the console runs as a command, so that it reaches
// the exec handlers. No function or builtin can have its name.
const execMarker = "\x00exec"

type commandHandler struct {
	underlying repl.CommandHandler
	cwd        workspaceapi.URI
	exec       schemeapi.Executor
}

// HandleCommand parses the command line as shell syntax
// and executes it via mvdan/sh, delegating non-builtin
// commands to the underlying handler. Output is streamed
// via a channel-backed iterator.
func (h *commandHandler) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	line := reconstructLine(cmd)
	if line == "" {
		return iterator.FromSlice[component.Responsive](nil), nil
	}

	file, err := syntax.NewParser().Parse(
		strings.NewReader(line), "",
	)
	if err != nil {
		return nil, err
	}
	funcs := map[string]bool{}
	syntax.Walk(file, func(n syntax.Node) bool {
		if fd, ok := n.(*syntax.FuncDecl); ok && fd.Name != nil {
			funcs[fd.Name.Value] = true
		}
		return true
	})

	ch := make(chan component.Responsive, 64)
	outW := &lineWriter{ch: ch, ctx: ctx}
	errW := &lineWriter{ch: ch, ctx: ctx}

	opts := []interp.RunnerOption{
		interp.StdIO(nil, outW, errW),
		interp.CallHandler(func(_ context.Context, args []string) ([]string, error) {
			if interp.IsBuiltin(args[0]) && !consoleBuiltins[args[0]] && !funcs[args[0]] {
				return append([]string{execMarker}, args...), nil
			}
			return args, nil
		}),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return h.execMiddleware(pw, next)
		}),
		interp.Interactive(true),
	}
	// With an executor the remote host owns the working directory, so
	// interp.Dir must not run a local os.Stat on a path that only
	// exists on the remote. Only seed the local Dir when there is no
	// executor.
	if h.exec == nil {
		if dir := h.cwd.Path(); dir != "" {
			opts = append(opts, interp.Dir(dir))
		}
	}
	runner, err := interp.New(opts...)
	if err != nil {
		return nil, err
	}

	var runErr error
	go debug.CapturePanicReport(func() {

		defer close(ch)
		runErr = runner.Run(ctx, file)
		outW.flush()
		errW.flush()

	})

	return iterator.FromFunc(func(ctx context.Context) (component.Responsive, bool, error) {
		select {
		case item, ok := <-ch:
			if !ok {
				if exit, ok := runErr.(interp.ExitStatus); ok && int(exit) != 0 {
					return nil, false, &repl.ExitError{Code: int(exit)}
				}
				if runErr != nil && !errors.Is(runErr, context.Canceled) {
					return nil, false, runErr
				}
				return nil, false, nil
			}
			return item, true, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}, func() error { return nil }), nil
}

// Complete delegates to the underlying handler but as a fallback
// completes dirs or files.
func (h *commandHandler) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	iter, err := h.underlying.Complete(ctx, cmd, args)
	if err != nil {
		return nil, err
	}
	if args == nil {
		return withFallback(iter, func() []string {
			return pathCompletions(cmd)
		}), nil
	}
	return withFallback(iter, func() []string {
		if _, err := exec.LookPath(cmd); err != nil {
			return nil
		}
		var prefix string
		if len(args) > 0 {
			prefix = args[len(args)-1]
		}
		return fileCompletions(prefix)
	}), nil
}

func withFallback(
	primary iterator.Iterator[string], fallback func() []string,
) iterator.Iterator[string] {
	it := &fallbackIter{primary: primary, fallback: fallback}
	return iterator.FromFunc(it.next, primary.Close)
}

type fallbackIter struct {
	primary  iterator.Iterator[string]
	fallback func() []string

	decided  bool
	usingFB  bool
	fbValues []string
}

func (f *fallbackIter) next(ctx context.Context) (string, bool, error) {
	if !f.decided {
		f.decided = true
		if v, ok := f.primary.Next(ctx); ok {
			return v, true, nil
		}
		if err := f.primary.Err(); err != nil {
			return "", false, err
		}
		f.usingFB = true
		f.fbValues = f.fallback()
	}
	if !f.usingFB {
		v, ok := f.primary.Next(ctx)
		if !ok {
			return "", false, f.primary.Err()
		}
		return v, true, nil
	}
	if len(f.fbValues) == 0 {
		return "", false, nil
	}
	v := f.fbValues[0]
	f.fbValues = f.fbValues[1:]
	return v, true, nil
}

func (h *commandHandler) execMiddleware(
	pw repl.ProgressWriter,
	next interp.ExecHandlerFunc,
) interp.ExecHandlerFunc {
	return func(ctx context.Context, args []string) error {
		if args[0] == execMarker {
			args = args[1:]
		}
		hc := interp.HandlerCtx(ctx)
		cmd := repl.Command{
			Name: args[0],
			Args: args[1:],
		}
		iter, err := h.underlying.HandleCommand(
			ctx, cmd, pw,
		)
		if errors.Is(err, repl.ErrNotFound) {
			if h.exec != nil {
				return h.workspaceExec(ctx, args)
			}
			return next(ctx, args)
		}
		if err != nil {
			_, _ = fmt.Fprintln(hc.Stderr, err.Error())
			return interp.ExitStatus(1)
		}
		defer func() { _ = iter.Close() }()

		// When stdout is the REPL's own lineWriter (i.e. the
		// command is not piped into another command or redirected
		// to a file), forward Responsives as-is so the terminal
		// layer can Resize them to its actual width. Flattening
		// to pipeWidth-wide plain text would break components
		// like markdown tables that depend on responsive layout.
		lw, direct := hc.Stdout.(*lineWriter)
		first := true
		for {
			item, ok := iter.Next(ctx)
			if !ok {
				break
			}
			if direct {
				lw.sendResponsive(item)
				continue
			}
			if !first {
				_, _ = fmt.Fprintln(hc.Stdout)
			}
			first = false
			_, _ = fmt.Fprint(hc.Stdout, responsiveToText(item, pipeWidth))
		}
		if err := iter.Err(); err != nil {
			_, _ = fmt.Fprintln(hc.Stderr, err.Error())
			return interp.ExitStatus(1)
		}
		return nil
	}
}

// workspaceExec runs an external command through the workspace
// Executor so remote workspaces execute it on the right host. It
// blocks until the process exits (per the invariant that an
// interp.ExecHandlerFunc runs synchronously so stdout is flushed
// before $(…) capture reads it).
func (h *commandHandler) workspaceExec(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return nil
	}
	hc := interp.HandlerCtx(ctx)
	watchCh := make(chan error, 1)
	cmd := workspaceapi.Cmd{
		Path:    args[0],
		Dir:     h.cwd.Path(),
		Args:    args[1:],
		Stdin:   hc.Stdin,
		Stdout:  hc.Stdout,
		Stderr:  hc.Stderr,
		Watcher: workspaceapi.ChanProcessWatcher(watchCh),
	}
	if _, startErr := h.exec.StartCommand(ctx, cmd); startErr != nil {
		return interp.ExitStatus(127)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case watchErr := <-watchCh:
		if watchErr != nil {
			return interp.ExitStatus(1)
		}
		return nil
	}
}

func reconstructLine(cmd repl.Command) string {
	if cmd.Name == "" {
		return ""
	}
	if len(cmd.Args) == 0 {
		return cmd.Name
	}
	return cmd.Name + " " + strings.Join(cmd.Args, " ")
}

func responsiveToText(r component.Responsive, width int) string {
	height := r.Height(width)
	if height <= 0 {
		return ""
	}
	w := term.NewStringWriter(width, height)
	r.Resize(width, height)
	r.Draw(w)
	_ = w.Flush()
	return trimTrailingWhitespace(w.String())
}

func trimTrailingWhitespace(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	// Remove trailing empty lines.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// pathCompletions scans $PATH directories for executable
// names that start with prefix, deduplicates and returns
// them sorted.
func pathCompletions(prefix string) []string {
	if prefix == "" {
		return nil
	}
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		return nil
	}
	seen := make(map[string]bool)
	for _, dir := range filepath.SplitList(pathEnv) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			if seen[name] {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0111 == 0 {
				continue
			}
			seen[name] = true
		}
	}
	result := make([]string, 0, len(seen))
	for name := range seen {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

// fileCompletions lists directory entries matching the
// given prefix, appending "/" for directories. Hidden
// files are only included when the prefix starts with ".".
func fileCompletions(prefix string) []string {
	dir, base := filepath.Split(prefix)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var result []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base) {
			continue
		}
		// Skip hidden files unless the prefix starts with ".".
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".") {
			continue
		}
		path := name
		if dir != "." {
			path = filepath.Join(dir, name)
		}
		if e.IsDir() {
			path += "/"
		}
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}
