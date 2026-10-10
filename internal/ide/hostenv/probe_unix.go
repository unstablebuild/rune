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

package hostenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// ProbeLoginPATH returns the PATH the user's login shell sets up: $SHELL, or
// userShell when $SHELL is unset, or /bin/sh. ctx bounds the probe, so a shell
// that wedges on startup cannot block the caller.
func ProbeLoginPATH(ctx context.Context, userShell func() (string, error)) (string, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		if s, err := userShell(); err == nil && s != "" {
			shell = s
		} else {
			shell = "/bin/sh"
		}
	}

	out, err := loginShellPATHCmd(ctx, shell).Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("login shell PATH probe: %w", ctx.Err())
		}
		return "", fmt.Errorf("shell env probe: %w", err)
	}

	p := pathFromMarkerEnv(string(out))
	if p == "" {
		return "", errors.New("shell returned empty PATH")
	}
	return p, nil
}

func loginShellPATHCmd(ctx context.Context, shell string) *exec.Cmd {
	script := `cd "$HOME" 2>/dev/null; printf '%s' ` + shellEnvMarker + `; /usr/bin/env -0; exit 0;`
	cmd := exec.CommandContext(ctx, shell, "-i", "-l", "-c", script)
	cmd.Stdin = nil
	detachFromTerminal(cmd)
	return cmd
}

func detachFromTerminal(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
}
