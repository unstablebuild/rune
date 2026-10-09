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

package idecmd

import (
	"context"
	"fmt"

	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"

	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/cmdenv"
)

// Expander turns a textapi.Command into a stream of fully-expanded
// textapi.Commands ready for dispatch. A non-alias command yields a
// single element with $VAR-substituted args. An alias yields one
// element per step, where each step's expansion runs lazily on Next
// so it can see chain captures written by the previous step's
// dispatch handler.
//
// Expander also owns the alias table: callers look up alias entries
// via Resolve, enumerate names via ListNames, and render command
// manuals via Manuals. The aliases map is held by reference; in-place
// mutations by callers are visible immediately.
type Expander struct {
	aliases map[string]text.CommandAlias
	env     cmdenv.Source
}

// NewExpander builds an Expander backed by aliases. env is the
// cmdenv.Source used for $VAR / $FILE / $WORD expansion; it is
// looked up on every variable reference so a source that re-reads
// focus state per call (e.g. text.Component.DispatchEnv) stays
// fresh across alias steps. Both arguments may be nil: a nil map
// disables alias resolution (every command is treated as a leaf),
// and a nil env disables shell-style variable expansion.
func NewExpander(
	aliases map[string]text.CommandAlias, env cmdenv.Source,
) *Expander {
	return &Expander{aliases: aliases, env: env}
}

// ResolveAlias returns the alias registered under name, if any.
func (e *Expander) ResolveAlias(name string) (text.CommandAlias, bool) {
	if e.aliases == nil {
		return text.CommandAlias{}, false
	}
	a, ok := e.aliases[name]
	return a, ok
}

// Aliases returns command.Manual entries for each registered alias
// so callers can fold alias listings into command completion. Order
// is not stable.
func (e *Expander) Aliases() []command.Manual {
	out := make([]command.Manual, 0, len(e.aliases))
	for name, a := range e.aliases {
		out = append(out, command.Manual{Name: name, AliasOf: a.Commands})
	}
	return out
}

// Expand resolves cmd.Name against the alias table. When matched it
// returns an iterator that yields one expanded textapi.Command per
// alias step; the consumer is expected to dispatch each yielded
// command (in order, waiting for each dispatch to return before
// calling Next again) so step N+1's expansion sees chain captures
// written by step N. For non-alias commands the iterator yields a
// single element with $VAR-substituted args.
//
// Errors returned synchronously cover upfront positional validation
// across the whole alias body. Per-step expansion errors surface
// through iterator.Err().
func (e *Expander) Expand(
	ctx context.Context, cmd textapi.Command,
) (iterator.Iterator[textapi.Command], error) {
	alias, ok := e.ResolveAlias(cmd.Name)
	if !ok {
		return e.expandLeaf(ctx, cmd)
	}
	// Alias-level args (everything the user typed after the alias
	// name) get $FILE/$WORD/... expansion once, up front. The
	// per-step expansion below then sees those values plus the $1..$9
	// positional overlay derived from the same args.
	expandedArgs, err := e.expandArgs(ctx, cmd.Args)
	if err != nil {
		return nil, err
	}
	cmd.Args = expandedArgs
	return e.ExpandAlias(ctx, cmd, alias)
}

// ExpandAlias is Expand for alias, which need not be registered under
// cmd.Name. cmd.Args are bound to $1..$9 verbatim: they must already be
// expanded, as the args of a step that invokes another alias are.
// Expanding them again would substitute variables found in their values.
func (e *Expander) ExpandAlias(
	ctx context.Context, cmd textapi.Command, alias text.CommandAlias,
) (iterator.Iterator[textapi.Command], error) {
	// Pre-scan every target to (a) reject $N references with N >
	// len(args) before we start dispatching anything, and (b) compute
	// which positional args are consumed so they can be stripped from
	// the tail passed through to each step.
	referencedAll := make(map[int]struct{})
	for _, target := range alias.Commands {
		if _, rest, ok := splitPluginPrefix(target); ok {
			if err := checkReferences(rest, len(cmd.Args)); err != nil {
				return nil, err
			}
			recordReferences(rest, len(cmd.Args), referencedAll)
			continue
		}
		for _, tok := range command.SplitCommandLine(target) {
			raw := command.UnquoteToken(tok)
			if err := checkReferences(raw, len(cmd.Args)); err != nil {
				return nil, err
			}
			recordReferences(raw, len(cmd.Args), referencedAll)
		}
	}
	remaining := make([]string, 0, len(cmd.Args))
	for i, a := range cmd.Args {
		if _, used := referencedAll[i]; !used {
			remaining = append(remaining, a)
		}
	}
	return &aliasIterator{
		exp:       e,
		aliasName: cmd.Name,
		targets:   alias.Commands,
		args:      cmd.Args,
		remaining: remaining,
		template:  cmd,
	}, nil
}

// expandLeaf returns a one-element iterator that yields cmd with its
// args expanded via the env source. This is the path text.Component
// used to take when dispatching a non-alias command.
func (e *Expander) expandLeaf(
	ctx context.Context, cmd textapi.Command,
) (iterator.Iterator[textapi.Command], error) {
	out, err := e.expandArgs(ctx, cmd.Args)
	if err != nil {
		return nil, err
	}
	cmd.Args = out
	return iterator.FromSlice([]textapi.Command{cmd}), nil
}

// expandArgs applies cmdenv.Expand to each dispatched arg using the
// component-provided env source. Each arg is expanded independently
// so values containing whitespace stay as one argv element.
func (e *Expander) expandArgs(ctx context.Context, args []string) ([]string, error) {
	out := make([]string, len(args))
	for i, a := range args {
		v, err := cmdenv.Expand(ctx, cmdenv.EscapeDoubleDollar(a), e.env)
		if err != nil {
			return nil, fmt.Errorf("expand dispatched arg %q: %v", a, err)
		}
		out[i] = v
	}
	return out, nil
}

// aliasIterator yields one expanded textapi.Command per alias step.
// Each Next call re-reads the chain attached to its ctx so captures
// written by the previous step's handler are visible to the next
// step's expansion.
type aliasIterator struct {
	exp       *Expander
	aliasName string
	targets   []string
	args      []string
	remaining []string
	template  textapi.Command

	idx   int
	err   error
	chain *Chain
}

func (it *aliasIterator) Next(ctx context.Context) (textapi.Command, bool) {
	for it.idx < len(it.targets) {
		target := it.targets[it.idx]
		it.idx++
		if it.chain == nil {
			it.chain = ChainFromContext(ctx)
			if it.chain == nil {
				it.chain = NewChain()
			}
		}
		stepCtx := WithChain(ctx, it.aliasName, it.chain)
		if isPluginTarget(target) {
			stepCtx = cmdenv.WithCommandSubstitution(stepCtx)
		}
		argv, _, err := expandTarget(stepCtx, target, it.args, it.exp.env, it.chain)
		if err != nil {
			it.err = fmt.Errorf("%s: %s", target, err)
			return textapi.Command{}, false
		}
		if len(argv) == 0 {
			continue
		}
		out := it.template
		out.Name = argv[0]
		out.Args = append(argv[1:], it.remaining...)
		return out, true
	}
	return textapi.Command{}, false
}

func (it *aliasIterator) Err() error { return it.err }

func (it *aliasIterator) Close() error { return nil }
