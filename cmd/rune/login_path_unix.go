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

//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func resolveLoginPath(timeout time.Duration, userShell func() (string, error)) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		if s, err := userShell(); err == nil && s != "" {
			shell = s
		} else {
			shell = "/bin/sh"
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := loginShellPATHCmd(ctx, shell).Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("login shell PATH probe timed out after %s: %w", timeout, ctx.Err())
		}
		return "", fmt.Errorf("shell env probe: %w", err)
	}

	p := pathFromMarkerEnv(string(out))
	if p == "" {
		return "", fmt.Errorf("shell returned empty PATH")
	}
	return p, nil
}

// loginShellPATHCmd builds the command that dumps the login shell environment.
//
// The shell is invoked interactively (-i) and as a login shell (-l) so rc
// files that mutate PATH are sourced. The probe cd's to $HOME first so
// directory-scoped tools (direnv, asdf, mise, nvm) contribute to PATH, prints
// a marker so banner chatter is ignored during parsing, dumps the env with
// `/usr/bin/env -0` (NUL-delimited so values containing newlines cannot
// corrupt parsing), and ends with `exit 0` as defense-in-depth against a shell
// that would otherwise wedge on a non-zero/interactive exit. This mirrors
// Zed's battle-tested login-shell environment probe.
//
// Interactive shells touch the controlling terminal on startup (zsh ZLE, job
// control); detachFromTerminal runs the child in a new session with no
// controlling terminal so those calls cannot raise SIGTTOU (which would stop
// the shell and hang the probe) nor can the SIGINT raised by Ctrl-C reach it.
// The context bounds the probe so it can never block startup indefinitely.
func loginShellPATHCmd(ctx context.Context, shell string) *exec.Cmd {
	script := `cd "$HOME" 2>/dev/null; printf '%s' ` + runeShellEnvMarker + `; /usr/bin/env -0; exit 0;`
	cmd := exec.CommandContext(ctx, shell, "-i", "-l", "-c", script)
	cmd.Stdin = nil
	detachFromTerminal(cmd)
	return cmd
}
