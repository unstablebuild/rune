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

package cmdenv

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"mvdan.cc/sh/v3/shell"
	"mvdan.cc/sh/v3/syntax"
)

// Expand performs POSIX-style parameter and arithmetic expansion on
// s as a single double-quoted field. Variable lookups consult src
// first and fall back to os.Getenv. The result never contains word
// splitting or globbing.
//
// When AllowsCommandSubstitution(ctx) is true, $(...) and backtick
// spans are copied through verbatim and the caller is responsible
// for evaluating them downstream. Otherwise they are rejected.
func Expand(ctx context.Context, s string, src Source) (string, error) {
	if !AllowsCommandSubstitution(ctx) {
		return shell.Expand(s, Lookup(src))
	}
	var out strings.Builder
	out.Grow(len(s))
	lookup := Lookup(src)
	i := 0
	n := len(s)
	for i < n {
		if s[i] == '\\' && i+1 < n {
			i += 2
			continue
		}
		if s[i] == '`' {
			end := findBacktickEnd(s, i+1)
			if end < 0 {
				break
			}
			if err := expandSegment(&out, s[:i], lookup); err != nil {
				return "", err
			}
			out.WriteString(s[i : end+1])
			s = s[end+1:]
			i = 0
			n = len(s)
			continue
		}
		if s[i] == '$' && i+1 < n && s[i+1] == '(' {
			end := findCmdSubstEnd(s, i+2)
			if end < 0 {
				break
			}
			if err := expandSegment(&out, s[:i], lookup); err != nil {
				return "", err
			}
			out.WriteString(s[i : end+1])
			s = s[end+1:]
			i = 0
			n = len(s)
			continue
		}
		i++
	}
	if err := expandSegment(&out, s, lookup); err != nil {
		return "", err
	}
	return out.String(), nil
}

func expandSegment(out *strings.Builder, seg string, lookup func(string) string) error {
	if seg == "" {
		return nil
	}
	v, err := shell.Expand(seg, lookup)
	if err != nil {
		return err
	}
	out.WriteString(v)
	return nil
}

// findCmdSubstEnd scans s for the ')' that closes a $(...) span
// whose body starts at start. Nested $(...) is treated as one outer
// span. Returns -1 on no match.
func findCmdSubstEnd(s string, start int) int {
	depth := 1
	i := start
	n := len(s)
	for i < n {
		switch s[i] {
		case '\\':
			if i+1 < n {
				i += 2
				continue
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		case '$':
			if i+1 < n && s[i+1] == '(' {
				depth++
				i += 2
				continue
			}
		}
		i++
	}
	return -1
}

// findBacktickEnd scans s for the closing backtick of a span that
// started at start-1, honoring `\“ escapes. Returns -1 on no match.
func findBacktickEnd(s string, start int) int {
	i := start
	n := len(s)
	for i < n {
		if s[i] == '\\' && i+1 < n {
			i += 2
			continue
		}
		if s[i] == '`' {
			return i
		}
		i++
	}
	return -1
}

// ExpandBody substitutes Rune-style $NAME, ${NAME}, and $N references
// in a plugin (! / !!) body using env, then returns the result.
// $(...) command substitutions, backtick spans, and backslash escapes
// are copied through untouched for the downstream shell interpreter
// to resolve. $$ collapses to a literal $. Names absent from env pass
// through verbatim so the downstream shell can consult its own
// environment.
//
// A resolved value reaches the shell as data, never as code: it is
// escaped for the quoting context it lands in (unquoted, "…", '…',
// $'…', a here-document or backquotes), so it stays a single field
// and the shell interprets none of its characters. Where the shell
// evaluates a value as arithmetic, only a non-negative integer is
// substituted, and where it names a variable, only a name. Values
// the body hands to a command that evaluates them again, such as
// eval or sh -c, are not protected.
//
// ExpandBody returns an error rather than a line when a value has to
// be substituted and body is not valid shell syntax, when a value
// cannot be substituted where it is referenced, or when the line
// would not have the command structure of body: a value can neither
// add nor remove commands, words, redirections or expansions.
//
// ctx is accepted for symmetry with Expand and is currently unused.
func ExpandBody(_ context.Context, s string, env Source) (string, error) {
	tmpl, substs := bodyTemplate(s, env)
	if len(substs) == 0 {
		return tmpl, nil
	}
	parser := syntax.NewParser()
	want, err := parser.Parse(strings.NewReader(tmpl), "")
	if err != nil {
		return "", fmt.Errorf("parse %q: %w", s, err)
	}
	spans := shellSpans(want)
	var b strings.Builder
	last := 0
	for _, sub := range substs {
		v, err := escapeValue(contextAt(spans, sub.start), sub.value)
		if err != nil {
			return "", fmt.Errorf("substitute %s: %w", tmpl[sub.start:sub.end], err)
		}
		b.WriteString(tmpl[last:sub.start])
		b.WriteString(v)
		last = sub.end
	}
	b.WriteString(tmpl[last:])
	line := b.String()
	// Escaping is the protection; this catches a context it gets
	// wrong before the shell runs the result.
	got, err := parser.Parse(strings.NewReader(line), "")
	if err != nil || !slices.Equal(
		shellStructure(want, substs), shellStructure(got, nil)) {
		return "", fmt.Errorf(
			"refusing to run %q: substituted values change its structure", s)
	}
	return line, nil
}

// bodySubst is a reference that ExpandBody replaces with value: the
// template holds it as ${name} at [start, end).
type bodySubst struct {
	start, end int
	value      string
}

// bodyTemplate returns s with $$ collapsed to $ and every reference
// env resolves rewritten as ${name}, which parses as one parameter
// expansion wherever it lands, along with those references in order.
func bodyTemplate(s string, env Source) (string, []bodySubst) {
	var (
		b      strings.Builder
		substs []bodySubst
	)
	b.Grow(len(s))
	resolve := func(name string) bool {
		if name == "" {
			return false
		}
		v, ok := envLookup(env, name)
		if !ok {
			return false
		}
		start := b.Len()
		b.WriteString("${")
		b.WriteString(name)
		b.WriteByte('}')
		substs = append(substs, bodySubst{start: start, end: b.Len(), value: v})
		return true
	}
	i := 0
	n := len(s)
	for i < n {
		c := s[i]
		if c == '\\' && i+1 < n {
			b.WriteByte(c)
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < n && s[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		if i+1 < n && s[i+1] == '{' {
			j := i + 2
			for j < n && s[j] != '}' {
				j++
			}
			next := min(j+1, n)
			if !resolve(s[i+2 : j]) {
				b.WriteString(s[i:next])
			}
			i = next
			continue
		}
		if i+1 < n && isDollarHead(s[i+1]) {
			j := i + 2
			if s[i+1] < '0' || s[i+1] > '9' {
				for j < n && isDollarTail(s[j]) {
					j++
				}
			}
			if !resolve(s[i+1 : j]) {
				b.WriteString(s[i:j])
			}
			i = j
			continue
		}
		b.WriteByte('$')
		i++
	}
	return b.String(), substs
}

// quoting is how the shell reads the text in a span.
type quoting int

const (
	unquoted quoting = iota
	singleQuoted
	ansiCQuoted
	doubleQuoted
	heredoc
	literalHeredoc
)

// shellSpan is a region of a shell line, [start, end) in bytes, that
// changes how a value substituted in it is read.
type shellSpan struct {
	start, end int
	quote      quoting
	// arith spans evaluate their text as arithmetic, name spans as a
	// variable name. Either evaluates array subscripts in it.
	arith, name bool
	// subscript spans index an array that is assigned to.
	subscript bool
	// subst spans are command substitutions, whose text is read in a
	// fresh quoting context; backquotes additionally strip a level of
	// backslashes from it first.
	subst, backquotes bool
}

func shellSpans(file *syntax.File) []shellSpan {
	var spans []shellSpan
	add := func(node syntax.Node, span shellSpan) {
		if node == nil || !node.Pos().IsValid() {
			return
		}
		span.start = int(node.Pos().Offset())
		span.end = int(node.End().Offset())
		spans = append(spans, span)
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.SglQuoted:
			if n.Dollar {
				add(n, shellSpan{quote: ansiCQuoted})
			} else {
				add(n, shellSpan{quote: singleQuoted})
			}
		case *syntax.DblQuoted:
			add(n, shellSpan{quote: doubleQuoted})
		case *syntax.CmdSubst:
			add(n, shellSpan{subst: true, backquotes: n.Backquotes})
		case *syntax.ProcSubst:
			add(n, shellSpan{subst: true})
		case *syntax.ArithmExp, *syntax.ArithmCmd, *syntax.LetClause,
			*syntax.CStyleLoop:
			add(n, shellSpan{arith: true})
		case *syntax.Assign:
			if n.Index != nil {
				add(n.Index, shellSpan{subscript: true})
			}
		case *syntax.ArrayElem:
			if n.Index != nil {
				add(n.Index, shellSpan{subscript: true})
			}
		case *syntax.BinaryTest:
			switch n.Op {
			case syntax.TsEql, syntax.TsNeq, syntax.TsLeq, syntax.TsGeq,
				syntax.TsLss, syntax.TsGtr:
				add(n.X, shellSpan{arith: true})
				add(n.Y, shellSpan{arith: true})
			}
		case *syntax.UnaryTest:
			if n.Op == syntax.TsVarSet || n.Op == syntax.TsRefVar {
				add(n.X, shellSpan{name: true})
			}
		case *syntax.Redirect:
			if n.Hdoc != nil {
				if quotedWord(n.Word) {
					add(n.Hdoc, shellSpan{quote: literalHeredoc})
				} else {
					add(n.Hdoc, shellSpan{quote: heredoc})
				}
			}
		}
		return true
	})
	return spans
}

// quotedWord reports whether a here-document delimiter is quoted,
// which keeps the shell from expanding anything in the document.
func quotedWord(w *syntax.Word) bool {
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		case *syntax.Lit:
			if strings.Contains(p.Value, `\`) {
				return true
			}
		}
	}
	return false
}

// shellContext is how the shell reads a value substituted at an
// offset.
type shellContext struct {
	quote                  quoting
	arith, name, subscript bool
	// backquotes is how many backquote substitutions enclose the
	// value, each of which strips a level of backslashes.
	backquotes int
}

func contextAt(spans []shellSpan, off int) shellContext {
	var enclosing []shellSpan
	for _, s := range spans {
		if s.start <= off && off < s.end {
			enclosing = append(enclosing, s)
		}
	}
	// Innermost first. A span that starts where an operand span does
	// is a quoting span within that operand.
	sort.SliceStable(enclosing, func(i, j int) bool {
		if enclosing[i].start != enclosing[j].start {
			return enclosing[i].start > enclosing[j].start
		}
		return enclosing[i].end < enclosing[j].end
	})
	var (
		ret            shellContext
		quoted, nested bool
	)
	for _, s := range enclosing {
		if s.backquotes {
			ret.backquotes++
		}
		if nested {
			continue
		}
		switch {
		case s.subst:
			nested = true
		case s.subscript:
			ret.subscript = true
		case s.arith:
			ret.arith = true
		case s.name:
			ret.name = true
		case !quoted:
			ret.quote, quoted = s.quote, true
		}
	}
	return ret
}

// escapeValue returns v as text that the shell reads back as exactly
// v in context c.
func escapeValue(c shellContext, v string) (string, error) {
	// Rune's interpreter panics on, or allocates, an element for every
	// index up to the one assigned.
	if c.subscript {
		return "", errors.New("cannot substitute into an array subscript")
	}
	if c.arith && !isInt64(v) {
		return "", fmt.Errorf("arithmetic only takes a non-negative integer, got %q", v)
	}
	if c.name && !isDigits(v) && !isShellName(v) {
		return "", fmt.Errorf("a variable name is expected, got %q", v)
	}
	if c.arith || c.name {
		// bash rejects a quoted operand, and digits and names need
		// no quoting anywhere.
		return v, nil
	}
	var ret string
	switch c.quote {
	case singleQuoted:
		ret = "'" + Quote(v) + "'"
	case ansiCQuoted:
		ret = "'" + Quote(v) + "$'"
	case doubleQuoted:
		// Not backslash-escaped in place: bash 3.2, the sh of macOS,
		// brace-expands double-quoted text nested in a command
		// substitution inside double quotes, and Rune's interpreter
		// drops the \r of a \r\n inside double quotes.
		ret = `"` + Quote(v) + `"`
	case heredoc, literalHeredoc:
		if !plainText(v) {
			return "", fmt.Errorf(
				"a here-document cannot hold the control characters in %q", v)
		}
		if c.quote == heredoc {
			ret = backslashEscape(v, "\\$`")
			break
		}
		// The shell keeps a quoted here-document verbatim, but Rune's
		// interpreter strips backslashes from it.
		if strings.Contains(v, `\`) {
			return "", fmt.Errorf(
				"a quoted here-document cannot hold the backslash in %q", v)
		}
		ret = v
	default:
		ret = Quote(v)
	}
	for range c.backquotes {
		ret = backslashEscape(ret, "\\$`")
	}
	return ret, nil
}

// plainText reports whether s is valid UTF-8 whose only control
// characters are newlines and tabs.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r != '\n' && r != '\t' && !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func backslashEscape(s, special string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if strings.IndexByte(special, s[i]) >= 0 {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isInt64(s string) bool {
	if !isDigits(s) {
		return false
	}
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}

func isShellName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDollarTail(s[i]) {
			return false
		}
	}
	return true
}

// shellStructure lists the nodes of file that decide what the shell
// runs, skipping the parameter expansions at the template offsets of
// substs. Literal text and quotes, which only make up data, are left
// out, so a template and the line its values were substituted into
// list the same nodes.
func shellStructure(file *syntax.File, substs []bodySubst) []string {
	sites := make(map[uint]bool, len(substs))
	for _, s := range substs {
		sites[uint(s.start)] = true
	}
	var ret []string
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case nil, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted:
		case *syntax.ParamExp:
			if sites[n.Pos().Offset()] {
				return false
			}
			name := ""
			if n.Param != nil {
				name = n.Param.Value
			}
			ret = append(ret, "ParamExp "+name)
		case *syntax.Stmt:
			ret = append(ret, fmt.Sprintf("Stmt %t %t %t",
				n.Negated, n.Background, n.Coprocess))
		case *syntax.BinaryCmd:
			ret = append(ret, "BinaryCmd "+n.Op.String())
		case *syntax.Redirect:
			ret = append(ret, "Redirect "+n.Op.String())
		case *syntax.BinaryArithm:
			ret = append(ret, "BinaryArithm "+n.Op.String())
		case *syntax.UnaryArithm:
			ret = append(ret, fmt.Sprintf("UnaryArithm %s %t", n.Op, n.Post))
		case *syntax.BinaryTest:
			ret = append(ret, "BinaryTest "+n.Op.String())
		case *syntax.UnaryTest:
			ret = append(ret, "UnaryTest "+n.Op.String())
		case *syntax.CaseItem:
			ret = append(ret, "CaseItem "+n.Op.String())
		default:
			ret = append(ret, fmt.Sprintf("%T", n))
		}
		return true
	})
	return ret
}

func envLookup(env Source, name string) (string, bool) {
	if env == nil {
		return "", false
	}
	return env(name)
}

func isDollarHead(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		b == '_' ||
		(b >= '0' && b <= '9')
}

func isDollarTail(b byte) bool {
	return (b >= 'a' && b <= 'z') ||
		(b >= 'A' && b <= 'Z') ||
		(b >= '0' && b <= '9') ||
		b == '_'
}
