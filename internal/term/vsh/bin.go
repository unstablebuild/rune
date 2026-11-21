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

package vsh

import (
	"context"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/interp"
)

const (
	defaultBin  = "/bin"
	defaultTERM = "dumb"
)

// shellNames are the programs that run the shell itself.
var shellNames = []string{"sh", "bash", "zsh", "dash", "ksh", "fish"}

// builtinPrograms are interpreter builtins that are also programs on
// a real system, so a command whose Path names one runs.
var builtinPrograms = []string{"echo", "printf", "test", "[", "true", "false", "pwd"}

// vshBuiltinNames are the commands the shell implements itself,
// beyond the interpreter's builtins and the registry.
var vshBuiltinNames = []string{"help", "which"}

func isShellName(name string) bool {
	return slices.Contains(shellNames, name)
}

// stubContent is what a program file holds. It is stable text so that
// fingerprints of the executables are stable across sessions.
func (e *Executor) stubContent(name string) string {
	kind := "command"
	if isShellName(name) {
		kind = "shell"
	}
	return fmt.Sprintf("#!%s\n# %s: Rune built-in %s, run in-process by the Rune shell.\nexec %s \"$@\"\n",
		e.shellPath(), name, kind, name)
}

// shellPath is the program file of the login shell.
func (e *Executor) shellPath() string {
	return path.Join(e.cfg.Bin, "sh")
}

// binNames lists every program installed in the bin directory.
func (e *Executor) binNames() []string {
	names := make([]string, 0, len(shellNames)+len(vshBuiltinNames)+len(builtinPrograms)+len(e.commands))
	names = append(names, shellNames...)
	names = append(names, vshBuiltinNames...)
	names = append(names, builtinPrograms...)
	for name := range e.commands {
		names = append(names, name)
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// installBin writes the program stubs. It is idempotent so that a
// chrooted scheme sharing the same file map can reinstall harmlessly.
func (e *Executor) installBin() error {
	if err := e.cfg.FS.MkdirAll(e.cfg.Bin, 0o755); err != nil {
		return fmt.Errorf("install %s: %w", e.cfg.Bin, err)
	}
	for _, name := range e.binNames() {
		p := path.Join(e.cfg.Bin, name)
		// Truncating a file keeps its mode, and a stub that cannot be
		// executed is not found in the PATH.
		if info, err := e.cfg.FS.Stat(p); err == nil && info.Mode().IsRegular() && !isExecutableInfo(info) {
			if err := e.cfg.FS.Remove(p); err != nil {
				return fmt.Errorf("install %s: %w", p, err)
			}
		}
		f, err := e.cfg.FS.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return fmt.Errorf("install %s: %w", p, err)
		}
		if _, err := f.Write([]byte(e.stubContent(name))); err != nil {
			_ = f.Close()
			return fmt.Errorf("install %s: %w", p, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("install %s: %w", p, err)
		}
	}
	return nil
}

// isExecutable reports whether p is a regular file with an exec bit.
func (e *Executor) isExecutable(p string) bool {
	info, err := e.cfg.FS.Stat(p)
	return err == nil && isExecutableInfo(info)
}

func isExecutableInfo(info fs.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// searchPath looks a bare name up in pathList, resolving relative
// entries against dir. It returns the first executable regular file.
func (e *Executor) searchPath(pathList []string, name, dir string) (string, bool) {
	for p := range e.pathMatches(pathList, name, dir) {
		return p, true
	}
	return "", false
}

// pathMatches yields every executable regular file named name in the
// directories of pathList, in order. Relative entries are resolved
// against dir, and an empty one is ".".
func (e *Executor) pathMatches(pathList []string, name, dir string) iter.Seq[string] {
	return func(yield func(string) bool) {
		for _, d := range pathList {
			if d == "" {
				d = "."
			}
			p := path.Join(absPath(dir, d), name)
			if e.isExecutable(p) && !yield(p) {
				return
			}
		}
	}
}

// pathList splits a PATH value; an empty one is the bin directory.
func (e *Executor) pathList(value string) []string {
	if value == "" {
		return []string{e.cfg.Bin}
	}
	return strings.Split(value, ":")
}

// envValue returns the last value of name in env.
func envValue(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], name+"="); ok {
			return v, true
		}
	}
	return "", false
}

// LookPath satisfies schemeapi.Executor. It resolves name as
// StartCommand does for a command whose Dir is dir: a bare name is
// searched in the PATH the Executor's processes start with, its
// relative entries resolved against dir; a name with a slash is
// resolved against dir and must be a regular file. An empty dir is the
// root, and a relative one is taken from the root. An empty name is
// the login shell. Names holding "$" cannot be resolved without a
// shell and report fs.ErrNotExist.
func (e *Executor) LookPath(_ context.Context, name, dir string) (string, error) {
	if name == "" {
		return e.shellPath(), nil
	}
	if strings.Contains(name, "$") {
		return "", fmt.Errorf("look up %s: %w", name, fs.ErrNotExist)
	}
	dir = absPath(e.cfg.Root, e.expandHome(dir))
	if !strings.Contains(name, "/") {
		value, _ := envValue(e.cfg.Env, "PATH")
		if p, ok := e.searchPath(e.pathList(value), name, dir); ok {
			return p, nil
		}
		return "", fmt.Errorf("look up %s: %w", name, fs.ErrNotExist)
	}
	p := absPath(dir, e.expandHome(name))
	info, err := e.cfg.FS.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("look up %s: %w", name, fs.ErrNotExist)
	}
	return p, nil
}

// expandHome replaces a leading "~" with the root, which is HOME.
func (e *Executor) expandHome(p string) string {
	if p == "~" {
		return e.cfg.Root
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return path.Join(e.cfg.Root, rest)
	}
	return p
}

// isBuiltinProgram reports whether name is an interpreter builtin a
// program stub stands for.
func isBuiltinProgram(name string) bool {
	return slices.Contains(builtinPrograms, name) && interp.IsBuiltin(name)
}
