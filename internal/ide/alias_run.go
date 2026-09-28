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

package ide

import (
	"context"
	"fmt"
	"time"

	"github.com/unstablebuild/blue/iterator"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/term"

	"unstable.build/rune/internal/debug"
	"unstable.build/rune/internal/text/textrpc"
)

// queuedDispatch is a deferred command. A non-nil waiter is already
// claimed and must receive the eventual result.
type queuedDispatch struct {
	ctx    context.Context
	cmd    string
	args   []string
	waiter *textrpc.Waiter
}

// aliasRun dispatches the steps of one alias expansion. Expansion and
// dispatch happen on the event loop, strictly in order and one step at a
// time: step N+1 expands only once step N completed, so it resolves the
// chain vars N published. The first failing step aborts the rest.
type aliasRun struct {
	ex     *ex
	ctx    context.Context
	name   string
	it     iterator.Iterator[textapi.Command]
	stack  map[string]bool
	parent *textrpc.Waiter
	nested bool

	pending  textapi.Command
	handled  bool
	detached bool
	err      error
}

// newAliasRun expands scmd into a run over its steps. stack holds the
// alias names already being expanded; a name that recurs yields an error
// instead of a run.
func newAliasRun(
	e *ex, ctx context.Context, scmd textapi.Command, stack map[string]bool,
	parent *textrpc.Waiter, nested bool,
) (*aliasRun, error) {
	if stack[scmd.Name] {
		return nil, fmt.Errorf("alias cycle through %q", scmd.Name)
	}
	// Copied rather than mutated-and-unwound: a detached run outlives
	// the frame that created it.
	descent := make(map[string]bool, len(stack)+1)
	for name := range stack {
		descent[name] = true
	}
	descent[scmd.Name] = true

	it, err := e.aliasExpander.Expand(ctx, scmd)
	if err != nil {
		return nil, err
	}
	return &aliasRun{
		ex: e, ctx: ctx, name: scmd.Name, it: it,
		stack: descent, parent: parent, nested: nested,
	}, nil
}

// step runs on the event loop and dispatches steps until one of them
// completes off-loop, one fails, or the alias is exhausted.
func (r *aliasRun) step() {
	e := r.ex
	for {
		next, ok := r.it.Next(r.ctx)
		if !ok {
			r.finish(r.it.Err())
			return
		}
		w := &textrpc.Waiter{Ch: make(chan error, 1)}
		stepCtx := textrpc.ContextWithWaiter(r.ctx, w)

		var (
			handled bool
			err     error
		)
		_, isStepAlias := e.aliasExpander.ResolveAlias(next.Name)
		if isStepAlias {
			// Expanded here rather than dispatched: the leaf dispatcher
			// only resolves subscribed commands, and sharing ctx keeps
			// the chain alive across nesting levels.
			child, cerr := newAliasRun(e, stepCtx, next, r.stack, w, true)
			if cerr != nil {
				err = cerr
			} else {
				child.step()
				handled, err = child.handled, child.err
			}
		} else {
			handled, err = e.comp.DispatchCommand(stepCtx, next)
		}
		// An error wins over the claim: a handler that failed will not
		// report, so parking on its waiter would never end.
		if err == nil && w.Claimed {
			r.detach(next, w, isStepAlias)
			return
		}
		if e.commandObserver != nil {
			e.commandObserver.observeCommand(r.name, next.Name, next.Args, err)
		}
		if err != nil {
			r.finish(fmt.Errorf("%s: %s", formatStep(next), err))
			return
		}
		r.handled = r.handled || handled
	}
}

// detach parks the run until step reports its result on w and resumes it
// on the event loop afterwards. A leaf step that does not report in time
// fails the run; a nested alias is not bounded here since its own leaf
// steps are, and a single budget would cap the whole sub-chain.
func (r *aliasRun) detach(
	step textapi.Command, w *textrpc.Waiter, isStepAlias bool,
) {
	e := r.ex
	r.detached = true
	r.pending = step
	if r.parent != nil {
		// This run can no longer answer synchronously. Claiming cascades
		// outwards until the outermost run takes the dispatch slot.
		r.parent.Claimed = true
	}
	if !r.nested {
		e.runInFlight = r
	}
	timeout := e.stepWaitTimeout
	go debug.CapturePanicReport(func() {
		var expired <-chan time.Time
		if !isStepAlias {
			timer := time.NewTimer(timeout)
			defer timer.Stop()
			expired = timer.C
		}
		var err error
		select {
		case err = <-w.Ch:
		case <-e.bgCtx.Done():
			return
		case <-expired:
			err = fmt.Errorf("did not report completion within %s", timeout)
		}
		e.sched(func() { r.resume(err) })
	})
}

func (r *aliasRun) resume(err error) {
	e := r.ex
	if e.closed {
		return
	}
	if e.commandObserver != nil {
		e.commandObserver.observeCommand(
			r.name, r.pending.Name, r.pending.Args, err)
	}
	if err != nil {
		r.finish(fmt.Errorf("%s: %s", formatStep(r.pending), err))
		return
	}
	// A waiter is only ever claimed from inside a handler, so a step that
	// detached was handled.
	r.handled = true
	r.step()
}

// finish ends the run with err. A run that never detached reports
// through its own fields; a detached one reports on its parent waiter,
// or as a notification when it has none.
func (r *aliasRun) finish(err error) {
	e := r.ex
	r.err = err
	_ = r.it.Close()
	if !r.detached {
		return
	}
	if r.parent != nil {
		r.parent.Ch <- err
	} else if err != nil {
		e.setError(err)
	}
	if r.nested {
		return
	}
	e.runInFlight = nil
	e.drainRunQueue()
	if e.exit && e.publishEvent != nil {
		// Handle already returned for the keystroke that asked to quit,
		// so nothing would observe e.exit without a new event.
		e.publishEvent(term.Event{Type: term.EventNone})
	}
}
