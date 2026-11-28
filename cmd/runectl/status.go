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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/unstablebuild/rune-go-sdk/term/progstatus"
	"golang.org/x/term"
	"unstable.build/rune/internal/debug"
)

var errStatusUnsupported = errors.New(
	"terminal does not support program status reports",
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// openStatusTarget resolves where reports are written: the explicit
// path, else stdout when it is a terminal, else the controlling tty.
func openStatusTarget(path string) (io.WriteCloser, error) {
	if path != "" {
		return os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	}
	if term.IsTerminal(int(os.Stdout.Fd())) {
		return nopWriteCloser{os.Stdout}, nil
	}
	return os.OpenFile("/dev/tty", os.O_WRONLY, 0)
}

func emitStatus(w io.Writer, r progstatus.Report, bel bool) error {
	if err := r.Validate(); err != nil {
		return err
	}
	_, err := io.WriteString(w, r.Encode(bel))
	return err
}

func newStatusCmd() *cobra.Command {
	var (
		id, kind, app, title string
		progress             int
		bel                  bool
		tty                  string
		format               string
	)

	cmd := &cobra.Command{
		Use:   "status <idle|working|done|blocked|error|clear> [message]",
		Short: "Report program status to the terminal (OSC 7501)",
		Long: "Report what the program is doing to the terminal using the" +
			" Program Status Protocol (OSC 7501). The report goes to the" +
			" terminal directly, so it works in any supporting terminal," +
			" over ssh, and inside containers.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			report := progstatus.Report{
				State: progstatus.State(args[0]),
				ID:    id,
				Kind:  progstatus.Kind(kind),
				App:   app,
				Title: title,
			}
			if progress != -1 {
				report.Progress = progstatus.Percent(progress)
			}
			if len(args) == 2 {
				report.Msg = args[1]
			}
			out, err := openStatusTarget(tty)
			if err != nil {
				return err
			}
			defer func() { _ = out.Close() }()
			if err := emitStatus(out, report, bel); err != nil {
				return err
			}
			if format == "" {
				return nil
			}
			return printOK(format)
		},
	}

	cmd.Flags().StringVar(
		&id, "id", "",
		"Record id (path such as build/test); absent means the root record",
	)
	cmd.Flags().StringVar(
		&app, "app", "",
		"Stable machine-readable program name, such as cargo or terraform",
	)
	cmd.Flags().StringVar(
		&title, "title", "",
		"Short human-readable label for the record",
	)
	cmd.Flags().StringVar(
		&kind, "kind", "",
		"What a blocked program waits for: permission|question|auth",
	)
	cmd.Flags().IntVar(
		&progress, "progress", -1,
		"Progress percentage 0-100 for working or blocked",
	)
	addStatusTargetFlags(cmd, &bel, &tty)
	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	cmd.AddCommand(
		newStatusQueryCmd(),
		newStatusWrapCmd(),
		newStatusDemoCmd(),
	)

	return cmd
}

func addStatusTargetFlags(cmd *cobra.Command, bel *bool, tty *string) {
	cmd.Flags().BoolVar(
		bel, "bel", false,
		"Terminate reports with BEL instead of ST",
	)
	cmd.Flags().StringVar(
		tty, "tty", "",
		"Terminal to write to (default: stdout if a terminal, else /dev/tty)",
	)
}

func newStatusQueryCmd() *cobra.Command {
	var (
		timeout time.Duration
		tty     string
		format  string
	)

	cmd := &cobra.Command{
		Use:   "query",
		Short: "Detect whether the terminal supports program status reports",
		Long: "Send the feature detection query and report whether the" +
			" terminal answered. Exits with status 1 when unsupported.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) (retErr error) {
			defer func() { retErr = formatError(format, retErr) }()
			if tty == "" {
				tty = "/dev/tty"
			}
			f, err := os.OpenFile(tty, os.O_RDWR, 0)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if fd := int(f.Fd()); term.IsTerminal(fd) {
				old, err := term.MakeRaw(fd)
				if err != nil {
					return fmt.Errorf("raw mode: %w", err)
				}
				defer func() { _ = term.Restore(fd, old) }()
			}
			supported, err := progstatus.Query(f, timeout)
			if err != nil {
				return err
			}
			if !supported {
				return errStatusUnsupported
			}
			return printString(format, "supported", []string{"result"})
		},
	}

	cmd.Flags().DurationVar(
		&timeout, "timeout", time.Second,
		"How long to wait for a reply",
	)
	cmd.Flags().StringVar(
		&tty, "tty", "",
		"Terminal to query (default: /dev/tty)",
	)
	cmd.Flags().StringVarP(
		&format, "format", "F", "",
		"Output format: table, json, or Go template",
	)

	return cmd
}

// exitError asks main to exit with the child's status without printing
// anything, since the child already reported its own failure.
type exitError struct{ code int }

func (e exitError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

func newStatusWrapCmd() *cobra.Command {
	var (
		app string
		msg string
		bel bool
		tty string
	)

	cmd := &cobra.Command{
		Use:   "wrap [flags] -- <command> [args...]",
		Short: "Run a command and report working, then done or error",
		Long: "Run a command with inherited stdio, reporting working" +
			" before it starts and done or error when it exits. SIGINT," +
			" SIGTERM, SIGHUP and SIGQUIT are forwarded to the command." +
			" A command killed by a signal is reported as an error. Exits" +
			" with the command's status.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := openStatusTarget(tty)
			if err != nil {
				return err
			}
			defer func() { _ = out.Close() }()
			if msg == "" {
				msg = strings.Join(args, " ")
			}
			emit := func(state progstatus.State, text string) error {
				return emitStatus(out, progstatus.Report{
					State: state, App: app, Msg: text,
				}, bel)
			}
			if err := emit(progstatus.Working, msg); err != nil {
				return err
			}
			status, err := runStatusWrapped(args)
			if err != nil {
				_ = emit(progstatus.Error, err.Error())
				return err
			}
			switch {
			case status.Signaled():
				if err := emit(progstatus.Error, fmt.Sprintf("signal: %s", status.Signal())); err != nil {
					return err
				}
				return exitError{code: 128 + int(status.Signal())}
			case status.ExitStatus() == 0:
				return emit(progstatus.Done, msg)
			default:
				code := status.ExitStatus()
				if err := emit(progstatus.Error, fmt.Sprintf("exit status %d", code)); err != nil {
					return err
				}
				return exitError{code: code}
			}
		},
	}
	cmd.Flags().SetInterspersed(false)

	cmd.Flags().StringVar(
		&app, "app", "",
		"Stable machine-readable program name to attach to every report",
	)
	cmd.Flags().StringVar(
		&msg, "msg", "",
		"Message for the working and done reports (default: the command line)",
	)
	addStatusTargetFlags(cmd, &bel, &tty)

	return cmd
}

func runStatusWrapped(argv []string) (syscall.WaitStatus, error) {
	child := exec.Command(argv[0], argv[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		return 0, err
	}

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigs)

	waited := make(chan error, 1)
	go debug.CapturePanicReport(func() { waited <- child.Wait() })
	for {
		select {
		case sig := <-sigs:
			_ = child.Process.Signal(sig)
		case err := <-waited:
			var exitErr *exec.ExitError
			if err != nil && !errors.As(err, &exitErr) {
				return 0, err
			}
			ws, ok := child.ProcessState.Sys().(syscall.WaitStatus)
			if !ok {
				return 0, errors.New("unexpected process state")
			}
			return ws, nil
		}
	}
}

// statusDemoStep is one step of a demo scenario. A ramped step is a
// working report played as a series of reports whose progress goes
// from 0 to 100%, ignoring the progress the step itself carries.
type statusDemoStep struct {
	progstatus.Report
	ramp bool
}

// statusDemoRampStep is the progress, in percent, between two reports
// of a ramped step.
const statusDemoRampStep = 5

// reports returns the reports that play the step.
func (s statusDemoStep) reports() []progstatus.Report {
	if !s.ramp {
		return []progstatus.Report{s.Report}
	}
	var ret []progstatus.Report
	for p := 0; p <= 100; p += statusDemoRampStep {
		r := s.Report
		r.Progress = progstatus.Percent(p)
		ret = append(ret, r)
	}
	return ret
}

// statusDemoScenarios are the examples from the protocol specification,
// with progress ramped through their main working phase.
var statusDemoScenarios = map[string][]statusDemoStep{
	"brew": {
		{Report: progstatus.Report{State: progstatus.Working, App: "brew", Msg: "Installing updates"}},
		{Report: progstatus.Report{State: progstatus.Blocked, Kind: progstatus.Auth, App: "brew", Msg: "Password required to install updates"}},
		{Report: progstatus.Report{State: progstatus.Working, App: "brew", Msg: "Installing updates"}, ramp: true},
		{Report: progstatus.Report{State: progstatus.Done, App: "brew", Msg: "Upgraded 12 packages"}},
	},
	"deploy": {
		{Report: progstatus.Report{State: progstatus.Working, App: "deploy", Msg: "Deploying v2.4.1"}},
		{Report: progstatus.Report{State: progstatus.Working, Progress: progstatus.Percent(40), ID: "us-east", Title: "US East", Msg: "Pushing image"}, ramp: true},
		{Report: progstatus.Report{State: progstatus.Blocked, Kind: progstatus.Permission, ID: "eu-west", Title: "EU West", Msg: "Approve deploy to eu-west (production)?"}},
		{Report: progstatus.Report{State: progstatus.Done, ID: "us-east", Title: "US East", Msg: "Healthy"}},
		{Report: progstatus.Report{State: progstatus.Clear, ID: "us-east"}},
		{Report: progstatus.Report{State: progstatus.Clear, ID: "eu-west"}},
		{Report: progstatus.Report{State: progstatus.Done, App: "deploy", Msg: "Deployed to 3 regions"}},
	},
	"rsync": {
		{Report: progstatus.Report{State: progstatus.Working, Msg: "Syncing photos"}, ramp: true},
		{Report: progstatus.Report{State: progstatus.Done, Msg: "Photos synced"}},
	},
}

func newStatusDemoCmd() *cobra.Command {
	var (
		scenario string
		speed    time.Duration
		progress time.Duration
		bel      bool
		tty      string
	)

	cmd := &cobra.Command{
		Use:   "demo",
		Short: "Walk through the specification examples, with progress while working",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			steps, ok := statusDemoScenarios[scenario]
			if !ok {
				return fmt.Errorf(
					"invalid scenario %q: use brew|deploy|rsync", scenario,
				)
			}
			out, err := openStatusTarget(tty)
			if err != nil {
				return err
			}
			defer func() { _ = out.Close() }()
			for i, step := range steps {
				if i > 0 {
					if err := sleepStatusDemo(cmd.Context(), speed); err != nil {
						return err
					}
				}
				reports := step.reports()
				for j, r := range reports {
					if j > 0 {
						tick := progress / time.Duration(len(reports)-1)
						if err := sleepStatusDemo(cmd.Context(), tick); err != nil {
							return err
						}
					}
					fmt.Println(r.String())
					if err := emitStatus(out, r, bel); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(
		&scenario, "scenario", "brew",
		"Example to play: brew|deploy|rsync",
	)
	cmd.Flags().DurationVar(
		&speed, "speed", time.Second,
		"Pause between reports",
	)
	cmd.Flags().DurationVar(
		&progress, "progress-duration", 8*time.Second,
		"Time a working step takes to go from 0 to 100% progress",
	)
	addStatusTargetFlags(cmd, &bel, &tty)

	return cmd
}

func sleepStatusDemo(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
