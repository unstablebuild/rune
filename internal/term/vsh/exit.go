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
	"errors"
	"fmt"
	"syscall"

	"mvdan.cc/sh/v3/interp"
)

// ExitError is how a process that did not exit with status 0 reports
// to its workspaceapi.ProcessWatcher. It mirrors exec.ExitError: the
// message reads "exit status N" or "signal: <name>", and ExitCode is
// what callers expecting os/exec read.
type ExitError struct {
	// Code is the exit status; -1 when the process was terminated by
	// Signal, as for exec.ExitError.
	Code int
	// Signal is the signal that terminated the process, or 0.
	Signal syscall.Signal
}

// Error satisfies error.
func (e *ExitError) Error() string {
	if e.Signal != 0 {
		return "signal: " + e.Signal.String()
	}
	return fmt.Sprintf("exit status %d", e.Code)
}

// ExitCode returns the exit status, or -1 for a signalled process.
func (e *ExitError) ExitCode() int {
	if e.Signal != 0 {
		return -1
	}
	return e.Code
}

// signalCause is the cause a process context is cancelled with when a
// signal terminates it.
type signalCause struct {
	sig syscall.Signal
}

func (c signalCause) Error() string { return "signal: " + c.sig.String() }

// exitError maps the outcome of a program run to what the watcher
// gets: nil for status 0, an ExitError otherwise. ctx is the process
// context, whose cancellation cause names the terminating signal.
func exitError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if sig, ok := signalOf(ctx, err); ok {
		return &ExitError{Code: -1, Signal: sig}
	}
	if status, ok := errors.AsType[interp.ExitStatus](err); ok {
		return &ExitError{Code: int(status)}
	}
	if exit, ok := errors.AsType[*ExitError](err); ok {
		return exit
	}
	return &ExitError{Code: 1}
}

// signalOf reports the signal a cancelled context or error stands for.
// A cancellation without a named signal counts as SIGKILL, the way
// exec.CommandContext kills a process whose context ends.
func signalOf(ctx context.Context, err error) (syscall.Signal, bool) {
	if cause, ok := errors.AsType[signalCause](err); ok {
		return cause.sig, true
	}
	if ctx.Err() == nil {
		return 0, false
	}
	if cause, ok := errors.AsType[signalCause](context.Cause(ctx)); ok {
		return cause.sig, true
	}
	return syscall.SIGKILL, true
}
