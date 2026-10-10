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

package ideconsole

import (
	"context"
	"strings"
	"sync/atomic"

	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// capturedCompletion records the prefix and the live candidate
// iterator that the inputbox queried via the WordCompleter on a tab
// press. It is produced by completionShim and consumed by Handler to
// stream candidates into the fuzzy completion overlay. The iterator is
// drained off the event loop so a slow completer (e.g. a tree-sitter
// scan over a large repo) does not freeze the prompt.
type capturedCompletion struct {
	// prefix is the partial word at the cursor that the user has
	// already typed; replacing it with a candidate restores the
	// rest of the line untouched.
	prefix string
	// iter yields the completion candidates. Handler owns draining
	// and closing it.
	iter iterator.Iterator[string]
}

// completionShim wraps the user-facing repl.CommandHandler so that
// the SDK inputbox's tab handler sees zero candidates whenever the
// underlying handler returns more than one match. The shim records
// the original candidates so Handler can render them in a search.List
// overlay instead of cycling through them inline.
//
// HandleCommand is delegated unmodified, since command execution does
// not interact with completion.
type completionShim struct {
	underlying repl.CommandHandler

	captured capturedCompletion
	hasCap   bool
	// lastPrefix is the prefix that was active when the most recent
	// completion overlay was opened. Handler reads it on accept to
	// know how many runes to delete before inserting the candidate.
	lastPrefix string
	// inFlight counts commands whose output iterator the inner repl
	// has not yet closed. It is bumped here rather than read from the
	// SDK because repl.Handler exposes no "is a command running"
	// accessor. Mutated from both the event-loop goroutine (dispatch)
	// and the dispatch goroutine (Close), so it must be atomic.
	inFlight atomic.Int64
}

func (s *completionShim) HandleCommand(
	ctx context.Context, cmd repl.Command, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	iter, err := s.underlying.HandleCommand(ctx, cmd, pw)
	if err != nil {
		return nil, err
	}
	s.inFlight.Add(1)
	return &trackedIterator{Iterator: iter, shim: s}, nil
}

// running reports whether a dispatched command's output iterator is
// still open. <c-l> consults this to avoid clearing (and thereby
// aborting) a command that is still producing output.
func (s *completionShim) running() bool {
	return s.inFlight.Load() > 0
}

// trackedIterator decrements the shim's in-flight counter when the
// inner repl closes the command's output iterator, which it does once
// the iterator is fully drained.
type trackedIterator struct {
	iterator.Iterator[component.Responsive]
	shim *completionShim
	done atomic.Bool
}

func (t *trackedIterator) Close() error {
	if t.done.CompareAndSwap(false, true) {
		t.shim.inFlight.Add(-1)
	}
	return t.Iterator.Close()
}

// Complete proxies the call to the underlying handler and captures the
// live candidate iterator without draining it. Draining on the event
// loop would freeze the prompt for completers that start background
// I/O (e.g. a tree-sitter scan over a large repo). The captured
// iterator is streamed into the overlay by Handler instead. An empty
// iterator is returned so the SDK inputbox does not enter its own
// inline completion mode.
func (s *completionShim) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	iter, err := s.underlying.Complete(ctx, cmd, args)
	if err != nil {
		return nil, err
	}
	s.captured = capturedCompletion{
		prefix: s.prefix(cmd, args),
		iter:   iter,
	}
	s.hasCap = true
	return iterator.FromSlice[string](nil), nil
}

// reset clears any previously-captured completion state. It is called
// before forwarding a tab event so that a stale capture from an
// earlier press cannot leak into the current decision.
func (s *completionShim) reset() {
	s.captured = capturedCompletion{}
	s.hasCap = false
}

// consume returns and clears the most recent capture if any.
func (s *completionShim) consume() (capturedCompletion, bool) {
	if !s.hasCap {
		return capturedCompletion{}, false
	}
	c := s.captured
	s.captured = capturedCompletion{}
	s.hasCap = false
	return c, true
}

// completionPrefix returns the partial word at the cursor that the
// inputbox is asking the WordCompleter to extend. It mirrors the
// shape of repl.Handler.makeCompleter: when args is non-empty, the
// last element is the partial word; when args is empty (or nil) the
// command itself is the partial word. A trailing-space scenario is
// expressed as args[len-1] == "" by the SDK, which is the empty
// prefix we want.
func completionPrefix(cmd string, args []string) string {
	if len(args) > 0 {
		return args[len(args)-1]
	}
	// args == nil means command-name completion; the partial word is
	// the command itself.
	if strings.ContainsAny(cmd, " \t") {
		// Defensive: shouldn't happen for command-name completion,
		// but if it does, just take the trailing fragment.
		idx := strings.LastIndexAny(cmd, " \t")
		return cmd[idx+1:]
	}
	return cmd
}

// PrefixCompleter lets a command handler override how the partial word
// at the cursor is computed. A language REPL implements it so completion
// candidates replace only the trailing identifier (e.g. "fmt.Pri" ->
// "Pri") instead of the whole whitespace-delimited token, which would
// otherwise be deleted wholesale on accept.
type PrefixCompleter interface {
	CompletionPrefix(line string) string
}

// SignatureHelper lets a command handler answer signature-help requests
// for the line being typed. line is the full input line; col is the
// 0-based rune column of the cursor. The returned label is rendered as a
// transient hint; ok is false when no signature applies.
type SignatureHelper interface {
	SignatureHelp(ctx context.Context, line string, col int) (label string, ok bool)
}

// signatureHelp forwards to the underlying handler when it is a
// SignatureHelper, so a language REPL can answer the hint request. It
// returns ok=false when the handler does not support signature help.
func (s *completionShim) signatureHelp(
	ctx context.Context, line string, col int,
) (string, bool) {
	if sh, ok := s.underlying.(SignatureHelper); ok {
		return sh.SignatureHelp(ctx, line, col)
	}
	return "", false
}

// prefix computes the partial word to be replaced on accept. When the
// underlying handler is a PrefixCompleter it gets the final say over the
// (reconstructed) input line; otherwise the whitespace-token default
// applies.
func (s *completionShim) prefix(cmd string, args []string) string {
	if pc, ok := s.underlying.(PrefixCompleter); ok {
		return pc.CompletionPrefix(reconstructLine(cmd, args))
	}
	return completionPrefix(cmd, args)
}

// reconstructLine rebuilds the input line the SDK split into cmd/args so
// a PrefixCompleter can reason about identifier boundaries across the
// whole line.
func reconstructLine(cmd string, args []string) string {
	if len(args) == 0 {
		return cmd
	}
	return cmd + " " + strings.Join(args, " ")
}
