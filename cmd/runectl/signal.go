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
	"fmt"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
)

func newSignalCmd(a *app) *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:   "signal <pid> <signal>",
		Short: "Send a signal to a process started via exec",
		Long: `Send a signal to a process started via exec.

The signal can be specified as a number or name (e.g., SIGTERM, TERM, 15).

Examples:
  runectl signal 1234 SIGTERM
  runectl signal 1234 TERM
  runectl signal 1234 15
  runectl signal 1234 SIGKILL`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()

			pid, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("invalid pid %q: %w", args[0], err)
			}

			sig, err := parseSignal(args[1])
			if err != nil {
				return err
			}

			w, err := a.getWorkspace()
			if err != nil {
				return err
			}

			executor := w.Executor(cmd.Context())
			defer func() { _ = executor.Close() }()

			if err := executor.Signal(workspaceapi.Pid(pid), sig); err != nil {
				return fmt.Errorf("signal: %w", err)
			}

			return printOK(format)
		},
	}

	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

func parseSignal(s string) (syscall.Signal, error) {
	// Try parsing as a number first
	if num, err := strconv.ParseInt(s, 10, 32); err == nil {
		return syscall.Signal(num), nil
	}

	// Normalize: uppercase and remove SIG prefix if present
	name := strings.ToUpper(s)
	name = strings.TrimPrefix(name, "SIG")

	switch name {
	case "HUP":
		return syscall.SIGHUP, nil
	case "INT":
		return syscall.SIGINT, nil
	case "QUIT":
		return syscall.SIGQUIT, nil
	case "ILL":
		return syscall.SIGILL, nil
	case "TRAP":
		return syscall.SIGTRAP, nil
	case "ABRT":
		return syscall.SIGABRT, nil
	case "FPE":
		return syscall.SIGFPE, nil
	case "KILL":
		return syscall.SIGKILL, nil
	case "BUS":
		return syscall.SIGBUS, nil
	case "SEGV":
		return syscall.SIGSEGV, nil
	case "PIPE":
		return syscall.SIGPIPE, nil
	case "ALRM":
		return syscall.SIGALRM, nil
	case "TERM":
		return syscall.SIGTERM, nil
	case "URG":
		return syscall.SIGURG, nil
	case "STOP":
		return syscall.SIGSTOP, nil
	case "TSTP":
		return syscall.SIGTSTP, nil
	case "CONT":
		return syscall.SIGCONT, nil
	case "CHLD":
		return syscall.SIGCHLD, nil
	case "TTIN":
		return syscall.SIGTTIN, nil
	case "TTOU":
		return syscall.SIGTTOU, nil
	case "IO":
		return syscall.SIGIO, nil
	case "PROF":
		return syscall.SIGPROF, nil
	case "SYS":
		return syscall.SIGSYS, nil
	case "WINCH":
		return syscall.SIGWINCH, nil
	case "USR1":
		return syscall.SIGUSR1, nil
	case "USR2":
		return syscall.SIGUSR2, nil
	default:
		return 0, fmt.Errorf(
			"unknown signal %q: use signal number or name (e.g., SIGTERM, TERM, 15)",
			s,
		)
	}
}
