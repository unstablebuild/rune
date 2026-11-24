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
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
	"unstable.build/rune/internal/debug"
)

// session is an interactive shell on a pty: one persistent interpreter
// so that variables, functions, aliases and the directory carry over
// from line to line, with an x/term editor reading each line.
type session struct {
	proc    *process
	e       *Executor
	p       *pty
	runner  *interp.Runner
	history *history
	parser  *syntax.Parser
	// complete is what Tab does at the prompt, as completer.completeLine
	// does; nil completes nothing.
	complete func(line string, pos int) (string, int, []string)

	fgMu     sync.Mutex
	fgCancel context.CancelCauseFunc
}

func newSession(proc *process) *session {
	s := &session{
		proc:    proc,
		e:       proc.e,
		p:       proc.pty,
		history: &history{},
		parser:  syntax.NewParser(syntax.Variant(syntax.LangBash)),
	}
	s.complete = s.completeLine
	return s
}

// completeLine completes line at pos in the shell's directory, with
// its PATH and functions.
func (s *session) completeLine(line string, pos int) (string, int, []string) {
	pathValue, _ := s.variable("PATH")
	c := &completer{e: s.e, dir: s.runner.Dir, path: pathValue, funcs: slices.Collect(maps.Keys(s.runner.Funcs))}
	return c.completeLine(s.proc.ctx, line, pos)
}

// write sends shell output to the terminal.
func (s *session) write(b []byte) {
	_, _ = s.p.writeOutput(s.proc.ctx, b)
}

// run is the read-eval loop. It returns the status of the last command
// as an interp.ExitStatus when non-zero.
func (s *session) run() error {
	cfg := s.proc.cfg
	cfg.interactive = true
	cfg.stdin = nil
	runner, err := s.e.newRunner(cfg)
	if err != nil {
		return err
	}
	// Reset now so that the per-statement standard input set below is
	// never captured as the original to restore.
	runner.Reset()
	s.runner = runner
	s.p.ld.setInterruptHandler(func() { s.interruptForeground() })

	if s.e.cfg.Banner != "" {
		s.write([]byte(s.e.cfg.Banner + "\n"))
	}

	status := 0
	pending := ""
	var handback []byte
	for {
		if s.proc.ctx.Err() != nil {
			return s.exitError(status)
		}
		s.p.ld.setRaw(handback)
		handback = nil
		s.ensureColumnZero()
		s.write(oscMark("A"))
		line, err := s.readLine(s.promptString(pending != "", status))
		switch {
		case errors.Is(err, errInterrupt):
			s.write([]byte("^C"))
			pending = ""
			status = s.setStatus(130)
			continue
		case errors.Is(err, io.EOF):
			if s.hungUp() {
				return s.exitError(status)
			}
			s.write([]byte("exit\n"))
			return statusError(status)
		case err != nil:
			return err
		}
		src := pending + line + "\n"
		f, perr := s.parser.Parse(strings.NewReader(src), "")
		if syntax.IsIncomplete(perr) {
			pending = src
			continue
		}
		pending = ""
		if perr == nil {
			perr = rejectInteractive(f)
		}
		if perr != nil {
			s.write(fmt.Appendf(nil, "%s: %v\n", s.proc.shellName, perr))
			status = s.setStatus(2)
			continue
		}
		if len(f.Stmts) == 0 {
			continue
		}
		s.write(oscMark("C"))
		for _, stmt := range f.Stmts {
			var exited bool
			status, exited, handback, err = s.runForeground(stmt)
			if err != nil {
				return err
			}
			if exited || s.proc.ctx.Err() != nil {
				s.write(oscMark("D;" + strconv.Itoa(status)))
				return s.exitError(status)
			}
		}
		s.write(oscMark("D;" + strconv.Itoa(status)))
	}
}

func statusError(status int) error {
	if status == 0 {
		return nil
	}
	return interp.ExitStatus(status)
}

// exitError is what run returns: the signal that ended the process
// when one did, a hangup of the terminal, else the last status.
func (s *session) exitError(status int) error {
	if s.proc.ctx.Err() != nil {
		return context.Cause(s.proc.ctx)
	}
	if s.hungUp() {
		return signalCause{sig: syscall.SIGHUP}
	}
	return statusError(status)
}

// setStatus makes n the shell's $? after a line that ran no command,
// as a ^C at the prompt or a syntax error does. A subshell exit is
// the one way the interpreter offers to set it without exiting.
func (s *session) setStatus(n int) int {
	f, err := s.parser.Parse(strings.NewReader("(exit "+strconv.Itoa(n)+")"), "")
	if err == nil && len(f.Stmts) == 1 {
		_ = s.runner.Run(s.proc.ctx, f.Stmts[0])
	}
	return n
}

// rejectInteractive refuses the constructs an interactive line may not
// use: process substitution, coproc and background jobs.
func rejectInteractive(f *syntax.File) error {
	if err := rejectUnsupported(f); err != nil {
		return err
	}
	var err error
	syntax.Walk(f, func(n syntax.Node) bool {
		if st, ok := n.(*syntax.Stmt); ok && err == nil && (st.Background || st.Disown) {
			err = &errUnsupported{what: "a background job", pos: st.Pos()}
		}
		return err == nil
	})
	return err
}

// ensureColumnZero starts the prompt on a fresh line when the last
// command left the cursor mid-line, as zsh's PROMPT_SP does; the
// editor assumes it draws from the first column.
func (s *session) ensureColumnZero() {
	s.p.mu.Lock()
	atCol0 := s.p.atCol0
	s.p.mu.Unlock()
	if !atCol0 {
		s.write([]byte("\r\n"))
	}
}

func (s *session) hungUp() bool {
	s.p.mu.Lock()
	defer s.p.mu.Unlock()
	return s.p.hungUp || s.proc.ctx.Err() != nil
}

func (s *session) setForeground(cancel context.CancelCauseFunc) {
	s.fgMu.Lock()
	s.fgCancel = cancel
	s.fgMu.Unlock()
}

// interruptForeground is what ^C typed while a command runs does.
func (s *session) interruptForeground() bool {
	s.fgMu.Lock()
	cancel := s.fgCancel
	s.fgMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel(signalCause{sig: syscall.SIGINT})
	return true
}

// interrupt is SIGINT sent to the session: it ends the foreground
// command, or interrupts the line being edited.
func (s *session) interrupt() {
	if s.interruptForeground() {
		return
	}
	s.p.mu.Lock()
	if s.p.ld.raw {
		s.p.ld.rawQ = append(s.p.ld.rawQ, ctrlC)
		s.p.cond.Broadcast()
	}
	s.p.mu.Unlock()
}

// runForeground runs one statement with the terminal in cooked mode,
// pumping typed lines into the statement's standard input. Input the
// statement did not consume is returned so the prompt can replay it.
// exited reports that the statement asked the shell to exit; an
// interrupted command does not, although the interpreter marks a
// cancelled run as exiting.
func (s *session) runForeground(stmt *syntax.Stmt) (status int, exited bool, handback []byte, err error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return 1, false, nil, err
	}
	if err := interp.StdIO(pr, s.proc.cfg.stdout, s.proc.cfg.stderr)(s.runner); err != nil {
		_ = pr.Close()
		_ = pw.Close()
		return 1, false, nil, err
	}
	ctx, cancel := context.WithCancelCause(s.proc.ctx)
	s.setForeground(cancel)
	// A command blocked reading its standard input does not see its
	// context end; the end of the pipe it reads does wake it, the way
	// a signal interrupts a read on a real terminal.
	stopClosing := context.AfterFunc(ctx, func() { _ = pw.Close() })
	if s.p.ld.setCooked() {
		cancel(signalCause{sig: syscall.SIGINT})
	}
	pumpRest := make(chan []byte, 1)
	go debug.CapturePanicReport(func() {
		pumpRest <- s.pump(ctx, pw)
	})

	runErr := s.runner.Run(ctx, stmt)

	stopClosing()
	s.setForeground(nil)
	cancel(nil)
	// Closing the write end unblocks a pump parked on a full pipe and
	// ends the read of what the command left behind.
	_ = pw.Close()
	rest := <-pumpRest
	left, _ := io.ReadAll(pr)
	_ = pr.Close()
	handback = append(left, rest...)

	// An interrupted command may still end without an error, as one
	// reading the closed pipe does, so the interrupt is what the
	// foreground context was cancelled for.
	cause, interrupted := errors.AsType[signalCause](context.Cause(ctx))
	interrupted = interrupted && cause.sig == syscall.SIGINT
	switch {
	case runErr == nil && !interrupted:
		return 0, s.runner.Exited(), handback, nil
	case s.proc.ctx.Err() != nil:
		return 1, true, nil, nil
	case interrupted:
		return s.setStatus(130), false, handback, nil
	}
	if st, ok := errors.AsType[interp.ExitStatus](runErr); ok {
		return int(st), s.runner.Exited(), handback, nil
	}
	s.write(fmt.Appendf(nil, "%s: %v\n", s.proc.shellName, runErr))
	return 1, s.runner.Exited(), handback, nil
}

// pump copies cooked lines into the statement's standard input until
// the statement ends or the terminal sends EOF. It returns the line it
// could not deliver.
func (s *session) pump(ctx context.Context, pw *os.File) []byte {
	buf := make([]byte, 4096)
	for {
		n, err := s.p.ld.read(ctx, buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = pw.Close()
			}
			return nil
		}
		line := buf[:n]
		for len(line) > 0 {
			w, werr := pw.Write(line)
			line = line[w:]
			if werr != nil {
				return append([]byte(nil), line...)
			}
		}
	}
}
