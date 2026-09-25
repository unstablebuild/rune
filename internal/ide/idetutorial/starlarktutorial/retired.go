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

package starlarktutorial

import (
	"errors"

	"go.starlark.net/starlark"
	"go.starlark.net/syntax"
)

// errRetiredDSL marks a lesson written against a tutorial DSL this
// Rune no longer implements.
var errRetiredDSL = errors.New("tutorial written for an older Rune")

// retiredBuiltinNames are builtins earlier versions of Rune exposed.
// They stay predeclared so an old lesson still compiles: a resolve
// error here would fail the whole config decode and take every other
// tutorial in the file down with it.
var retiredBuiltinNames = []string{
	"floating_window",
	"markdown",
	"wait_key",
}

// retiredKeywords are arguments earlier versions of the blocking
// builtins accepted. A lesson passing one expects behaviour this Rune
// no longer has, so it is refused rather than run with the argument
// dropped.
var retiredKeywords = []string{"on_error", "alignment", "offset"}

// retiredBuiltins are the stubs that keep an old lesson compiling.
// Calling one is only reachable when the lesson reaches the DSL
// indirectly, since parse refuses the lesson up front.
func retiredBuiltins() starlark.StringDict {
	out := make(starlark.StringDict, len(retiredBuiltinNames))
	for _, name := range retiredBuiltinNames {
		out[name] = starlark.NewBuiltin(name, func(
			_ *starlark.Thread, b *starlark.Builtin,
			_ starlark.Tuple, _ []starlark.Tuple,
		) (starlark.Value, error) {
			return nil, errRetiredDSL
		})
	}
	return out
}

// retiredUse reports the first retired builtin or argument src calls,
// so a lesson can be refused as a whole before it arms a step rather
// than failing part-way through. Keywords count only on the DSL's own
// builtins: a lesson's own helper may well take an offset.
func retiredUse(
	opts *syntax.FileOptions, filename, src string, dsl starlark.StringDict,
) string {
	file, err := opts.Parse(filename, src, 0)
	if err != nil {
		return ""
	}
	retiredName := make(map[string]bool, len(retiredBuiltinNames))
	for _, name := range retiredBuiltinNames {
		retiredName[name] = true
	}
	retiredKeyword := make(map[string]bool, len(retiredKeywords))
	for _, name := range retiredKeywords {
		retiredKeyword[name] = true
	}

	found := ""
	syntax.Walk(file, func(n syntax.Node) bool {
		if found != "" {
			return false
		}
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fn.(*syntax.Ident)
		if !ok {
			return true
		}
		if retiredName[fn.Name] {
			found = fn.Name + "()"
			return false
		}
		if _, isDSL := dsl[fn.Name]; !isDSL {
			return true
		}
		for _, arg := range call.Args {
			binary, ok := arg.(*syntax.BinaryExpr)
			if !ok || binary.Op != syntax.EQ {
				continue
			}
			keyword, ok := binary.X.(*syntax.Ident)
			if ok && retiredKeyword[keyword.Name] {
				found = fn.Name + "(" + keyword.Name + "=)"
				return false
			}
		}
		return true
	})
	return found
}
