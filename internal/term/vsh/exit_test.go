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
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"mvdan.cc/sh/v3/interp"
)

func TestExitError(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  *ExitError
		msg  string
		code int
	}{
		{name: "status 1", err: &ExitError{Code: 1}, msg: "exit status 1", code: 1},
		{name: "status 127", err: &ExitError{Code: 127}, msg: "exit status 127", code: 127},
		{name: "status 0", err: &ExitError{}, msg: "exit status 0", code: 0},
		{name: "SIGKILL", err: &ExitError{Code: -1, Signal: syscall.SIGKILL}, msg: "signal: killed", code: -1},
		{name: "SIGTERM", err: &ExitError{Code: -1, Signal: syscall.SIGTERM}, msg: "signal: terminated", code: -1},
		{name: "SIGHUP", err: &ExitError{Code: -1, Signal: syscall.SIGHUP}, msg: "signal: hangup", code: -1},
		{name: "SIGINT", err: &ExitError{Code: -1, Signal: syscall.SIGINT}, msg: "signal: interrupt", code: -1},
		{name: "a signal wins over a code", err: &ExitError{Code: 3, Signal: syscall.SIGTERM}, msg: "signal: terminated", code: -1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.msg, tc.err.Error())
			assert.Equal(t, tc.code, tc.err.ExitCode())

			var target *ExitError
			assert.True(t, errors.As(fmt.Errorf("wrapped: %w", tc.err), &target))
			assert.Same(t, tc.err, target)
			assert.ErrorIs(t, fmt.Errorf("wrapped: %w", tc.err), tc.err)
			// Callers written for os/exec read the status through this
			// interface.
			var coder interface{ ExitCode() int }
			assert.True(t, errors.As(tc.err, &coder))
			assert.False(t, errors.As(tc.err, new(*exec.ExitError)))
		})
	}
}

func TestSignalCause(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "signal: terminated", signalCause{sig: syscall.SIGTERM}.Error())
	assert.Equal(t, "signal: hangup", signalCause{sig: syscall.SIGHUP}.Error())
}

func TestExitErrorOf(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	tests := []struct {
		name string
		// ctx is the process context when the run ended.
		ctx  func() context.Context
		err  error
		want error
	}{
		{name: "success", ctx: live, err: nil, want: nil},
		{name: "success in a cancelled process", ctx: cancelled, err: nil, want: nil},
		{name: "a status", ctx: live, err: interp.ExitStatus(3), want: &ExitError{Code: 3}},
		{name: "a wrapped status", ctx: live, err: fmt.Errorf("x: %w", interp.ExitStatus(4)), want: &ExitError{Code: 4}},
		{name: "a command's exit error", ctx: live, err: &ExitError{Code: 7}, want: &ExitError{Code: 7}},
		{name: "a wrapped exit error", ctx: live, err: fmt.Errorf("x: %w", &ExitError{Code: 8}), want: &ExitError{Code: 8}},
		{name: "another error is status 1", ctx: live, err: boom, want: &ExitError{Code: 1}},
		{name: "a context error of the command's own", ctx: live, err: context.Canceled, want: &ExitError{Code: 1}},
		{name: "a signal cause as the error", ctx: live, err: signalCause{sig: syscall.SIGTERM},
			want: &ExitError{Code: -1, Signal: syscall.SIGTERM}},
		{name: "a wrapped signal cause", ctx: live, err: fmt.Errorf("x: %w", signalCause{sig: syscall.SIGINT}),
			want: &ExitError{Code: -1, Signal: syscall.SIGINT}},
		{name: "cancellation without a cause is SIGKILL", ctx: cancelled, err: context.Canceled,
			want: &ExitError{Code: -1, Signal: syscall.SIGKILL}},
		{name: "a deadline is SIGKILL", ctx: expired, err: context.DeadlineExceeded,
			want: &ExitError{Code: -1, Signal: syscall.SIGKILL}},
		{name: "cancellation by a signal", ctx: signalled(syscall.SIGHUP), err: context.Canceled,
			want: &ExitError{Code: -1, Signal: syscall.SIGHUP}},
		{name: "cancellation by another cause is SIGKILL", ctx: causedBy(boom), err: context.Canceled,
			want: &ExitError{Code: -1, Signal: syscall.SIGKILL}},
		{name: "a signalled process's status is the signal", ctx: signalled(syscall.SIGTERM), err: interp.ExitStatus(1),
			want: &ExitError{Code: -1, Signal: syscall.SIGTERM}},
		{name: "the error's signal wins over the context's", ctx: signalled(syscall.SIGTERM), err: signalCause{sig: syscall.SIGINT},
			want: &ExitError{Code: -1, Signal: syscall.SIGINT}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, exitError(tc.ctx(), tc.err))
		})
	}
}

func TestSignalOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ctx  func() context.Context
		err  error
		sig  syscall.Signal
		ok   bool
	}{
		{name: "a live process", ctx: live, err: errors.New("x")},
		{name: "a live process with no error", ctx: live},
		{name: "a signal cause", ctx: live, err: signalCause{sig: syscall.SIGQUIT}, sig: syscall.SIGQUIT, ok: true},
		{name: "a cancelled process", ctx: cancelled, sig: syscall.SIGKILL, ok: true},
		{name: "an expired process", ctx: expired, sig: syscall.SIGKILL, ok: true},
		{name: "a signalled process", ctx: signalled(syscall.SIGTERM), sig: syscall.SIGTERM, ok: true},
		{name: "a process cancelled with a wrapped signal", ctx: causedBy(fmt.Errorf("x: %w", signalCause{sig: syscall.SIGHUP})),
			sig: syscall.SIGHUP, ok: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sig, ok := signalOf(tc.ctx(), tc.err)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.sig, sig)
		})
	}
}

func live() context.Context { return context.Background() }

func cancelled() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expired() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	cancel()
	return ctx
}

func causedBy(cause error) func() context.Context {
	return func() context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(cause)
		return ctx
	}
}

func signalled(sig syscall.Signal) func() context.Context { return causedBy(signalCause{sig: sig}) }
