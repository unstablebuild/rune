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
	"os"
	"strings"

	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/text/cmdenv"
)

// expandTarget expands one alias target (a single step body) into an
// argv. base provides FILE/WORD/builtin/user values; positional $1..$9
// and chain captures overlay on top.
func expandTarget(
	ctx context.Context,
	target string,
	args []string,
	base cmdenv.Source,
	chain *Chain,
) (argv []string, referenced map[int]struct{}, err error) {
	env := overlay(args, base, chain)
	if head, rest, ok := splitPluginPrefix(target); ok {
		referenced = make(map[int]struct{})
		if err := checkReferences(rest, len(args)); err != nil {
			return nil, nil, err
		}
		recordReferences(rest, len(args), referenced)
		envOSFallback := cmdenv.Source(func(name string) (string, bool) {
			if v, ok := env(name); ok {
				return v, true
			}
			if v, ok := os.LookupEnv(name); ok {
				return v, true
			}
			return "", false
		})
		expanded, err := cmdenv.ExpandBody(
			cmdenv.WithCommandSubstitution(ctx), rest, envOSFallback)
		if err != nil {
			return nil, nil, err
		}
		return []string{head, expanded}, referenced, nil
	}
	tokens := command.SplitCommandLine(target)
	argv = make([]string, 0, len(tokens))
	referenced = make(map[int]struct{})
	for _, tok := range tokens {
		raw := command.UnquoteToken(tok)
		if err := checkReferences(raw, len(args)); err != nil {
			return nil, nil, err
		}
		expanded, expErr := cmdenv.Expand(ctx, cmdenv.EscapeDoubleDollar(raw), env)
		if expErr != nil {
			return nil, nil, fmt.Errorf(
				"expand alias target token %q: %v", raw, expErr)
		}
		recordReferences(raw, len(args), referenced)
		argv = append(argv, expanded)
	}
	return argv, referenced, nil
}

// overlay returns a cmdenv.Source that resolves $1..$9 against args,
// then chain captures, then delegates to base.
func overlay(args []string, base cmdenv.Source, chain *Chain) cmdenv.Source {
	return func(name string) (string, bool) {
		if len(name) == 1 && name[0] >= '1' && name[0] <= '9' {
			idx := int(name[0] - '1')
			if idx < len(args) {
				return args[idx], true
			}
			return "", false
		}
		if v, ok := chain.Get(name); ok {
			return v, true
		}
		if base != nil {
			return base(name)
		}
		return "", false
	}
}

// splitPluginPrefix returns ok=false when `!` is not a standalone
// token, so bodies like `!foo` (no whitespace) stay as ordinary
// command names.
func splitPluginPrefix(target string) (head, rest string, ok bool) {
	trimmed := strings.TrimLeft(target, " \t")
	switch {
	case strings.HasPrefix(trimmed, "!! "), strings.HasPrefix(trimmed, "!!\t"):
		return "!!", strings.TrimLeft(trimmed[2:], " \t"), true
	case strings.HasPrefix(trimmed, "! "), strings.HasPrefix(trimmed, "!\t"):
		return "!", strings.TrimLeft(trimmed[1:], " \t"), true
	}
	return "", "", false
}

// isPluginTarget reports whether target is a `!` or `!!` shell body.
// Used by callers that need to decide whether to opt the ctx into
// cmdenv command-substitution preservation before re-dispatching.
func isPluginTarget(target string) bool {
	_, _, ok := splitPluginPrefix(target)
	return ok
}
