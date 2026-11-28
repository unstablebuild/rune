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
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/text/textrpc"
)

// execStdin is the --exec value that stands for the lines of stdin.
const execStdin = "-"

// maxExecLine bounds a command line read from stdin.
const maxExecLine = 1 << 20

// execLoadPoll is how often the runner checks whether files have loaded.
const execLoadPoll = 10 * time.Millisecond

var errExecNotQueued = errors.New("could not queue the command on the event loop")

// execReplyEscaper keeps a reply on one line.
var execReplyEscaper = strings.NewReplacer("\r", `\r`, "\n", `\n`)

// execRunner runs the command lines of --exec in the configured IDE,
// as the command prompt runs them. It writes one reply line per command
// to out, and reads the next line only after replying: "ok",
// "ok <path>" for screenshot, or "error: <message>". The next command
// starts once the events the previous one queued, and those they queued
// in turn, have been handled, and the files being opened have loaded, so
// it sees the effect of the keys echo sends and its keys reach the
// editors of those files. It does not wait for files to load while a
// prompt is open, as a load can wait for its answer, which only the next
// command can give.
type execRunner struct {
	root *bootstrapHandler
	// lines are the --exec values in order, execStdin standing for the
	// lines of stdin.
	lines []string
	stdin io.Reader
	out   io.Writer
	// takeScreenshot is root.takeScreenshot.
	takeScreenshot func(reload bool, done func(path string, err error))
	// loading reports whether files are still loading. It runs on the
	// event loop.
	loading func() bool
	// prompting reports whether a prompt waits for an answer. It runs on
	// the event loop.
	prompting func() bool
}

func newExecRunner(
	root *bootstrapHandler, lines []string, stdin io.Reader, out io.Writer,
) *execRunner {
	return &execRunner{
		root: root, lines: lines, stdin: stdin, out: out,
		takeScreenshot: root.takeScreenshot,
		loading:        func() bool { return root.realIDE.Loading() },
		prompting:      func() bool { return root.realIDE.Prompting() },
	}
}

// run runs every line in order. When the lines include stdin, Rune exits
// once stdin ends, without the confirm-exit prompt; otherwise it stays
// open. run returns early when ctx ends. It must not run on the event
// loop.
func (r *execRunner) run(ctx context.Context) {
	fromStdin := false
	for _, line := range r.lines {
		if line != execStdin {
			r.runLine(ctx, line)
			continue
		}
		fromStdin = true
		sc := bufio.NewScanner(r.stdin)
		sc.Buffer(nil, maxExecLine)
		for sc.Scan() && ctx.Err() == nil {
			r.runLine(ctx, sc.Text())
		}
		if err := sc.Err(); err != nil {
			r.reply("", fmt.Errorf("read stdin: %w", err))
		}
	}
	if fromStdin && ctx.Err() == nil {
		r.exit()
	}
}

func (r *execRunner) runLine(ctx context.Context, line string) {
	tokens := command.SplitCommandLine(line)
	if len(tokens) == 0 {
		return
	}
	if tokens[0] == cmdScreenshot {
		r.reply(r.screenshot(ctx, tokens[1:]))
		return
	}
	err := r.dispatch(ctx, line)
	if serr := r.settle(ctx); err == nil {
		err = serr
	}
	r.reply("", err)
}

// screenshot takes a screenshot as the screenshot command does, but
// returns its path instead of notifying it, so the notification does not
// show in the next one.
func (r *execRunner) screenshot(ctx context.Context, args []string) (string, error) {
	var reload bool
	switch {
	case len(args) == 0:
	case len(args) == 1 && command.UnquoteToken(args[0]) == "--reload":
		reload = true
	default:
		return "", fmt.Errorf("unexpected arguments %q: expected [--reload]", args)
	}
	type shot struct {
		path string
		err  error
	}
	ch := make(chan shot, 1)
	r.takeScreenshot(reload, func(path string, err error) { ch <- shot{path, err} })
	select {
	case s := <-ch:
		return s.path, s.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// dispatch runs line on the event loop and waits for its result,
// including the result a command reports after its dispatch returns.
func (r *execRunner) dispatch(ctx context.Context, line string) error {
	type dispatched struct {
		err     error
		claimed bool
	}
	w := &textrpc.Waiter{Ch: make(chan error, 1)}
	ch := make(chan dispatched, 1)
	ok := r.root.scheduleNextTick(func() {
		err := r.root.realIDE.DispatchCommandLine(textrpc.ContextWithWaiter(ctx, w), line)
		ch <- dispatched{err: err, claimed: w.Claimed}
	})
	if !ok {
		return errExecNotQueued
	}
	var d dispatched
	select {
	case d = <-ch:
	case <-ctx.Done():
		return ctx.Err()
	}
	if d.err != nil || !d.claimed {
		return d.err
	}
	select {
	case err := <-w.Ch:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// settle waits until the events queued so far have been handled, and the
// events those queued in turn: the keys echo sends and what handling them
// queues, for example. Then it waits for the files being opened to load,
// as keys sent before would not reach their editors, unless a prompt is
// open.
func (r *execRunner) settle(ctx context.Context) error {
	for {
		loading, err := r.afterQueuedEvents(ctx)
		if err != nil || !loading {
			return err
		}
		select {
		case <-time.After(execLoadPoll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// afterQueuedEvents waits until the events queued so far, and those they
// queue, have been handled, and reports whether files are loading then
// with no prompt open.
func (r *execRunner) afterQueuedEvents(ctx context.Context) (loading bool, err error) {
	done := make(chan bool, 1)
	ok := r.root.scheduleNextTick(func() {
		if !r.root.scheduleNextTick(func() { done <- r.loading() && !r.prompting() }) {
			done <- false
		}
	})
	if !ok {
		return false, errExecNotQueued
	}
	select {
	case loading := <-done:
		return loading, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

// exit makes Rune exit as the confirm-exit prompt's yes does, whatever
// buffers are dirty.
func (r *execRunner) exit() {
	ok := r.root.scheduleNextTick(func() {
		r.root.realIDE.ForceExit()
		// ForceExit takes effect on the next event the IDE handles.
		if !r.root.publishEvent(term.Event{Type: term.EventNone}) {
			log.Errorf("exec: could not queue the event that exits Rune")
		}
	})
	if !ok {
		log.Errorf("exec: %v", errExecNotQueued)
	}
}

func (r *execRunner) reply(path string, err error) {
	line := "ok"
	switch {
	case err != nil:
		line = "error: " + execReplyEscaper.Replace(err.Error())
	case path != "":
		line += " " + execReplyEscaper.Replace(path)
	}
	if _, werr := fmt.Fprintln(r.out, line); werr != nil {
		log.Errorf("exec: write reply: %v", werr)
	}
}
