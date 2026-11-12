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

package shellplan

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"mvdan.cc/sh/v3/pattern"
	"mvdan.cc/sh/v3/syntax"
)

// Rule describes the invocations of a program, such as "git commit *".
type Rule struct {
	// Name is the program. A name without a slash matches the programs
	// written with that name only, and one with a slash the programs
	// whose cleaned path it is ("./x" for a relative path to x).
	Name string
	// Args are shell patterns, one per argument. Quoted text in the rule
	// is escaped, so it matches literally.
	Args []string
	// Prefix reports that the program may get further arguments after
	// Args.
	Prefix bool
	src    string
}

// ParseRule parses s, written as a simple command of literal words: a
// program name and argument patterns. Unquoted glob characters in the
// arguments match as shell patterns and quoted text literally. A final
// unquoted "*" matches zero or more further arguments, and so does a
// final ":*" right after a word (as in "git add:*").
//
// Expansions, assignments, redirections, several commands (a trailing
// ";" included), brace expansions bash would expand and a detached ":*"
// are errors. So are program names that do not tell one file: names
// with glob characters, names starting with "~", whose meaning depends
// on quoting a plan does not keep, and directories.
func ParseRule(s string) (Rule, error) {
	src := strings.TrimSpace(s)
	if rest, ok := strings.CutSuffix(src, ":*"); ok {
		switch {
		case rest == "" || strings.TrimRightFunc(rest, unicode.IsSpace) != rest:
			return Rule{}, fmt.Errorf("command pattern %q: write :* right after the last word, as in git add:*", s)
		case !strings.HasSuffix(rest, `\`):
			src = rest + " *"
		}
	}
	if src == "" {
		return Rule{}, errors.New("empty command pattern")
	}
	file, err := syntax.NewParser(syntax.KeepComments(true)).Parse(strings.NewReader(src), "")
	if err != nil {
		return Rule{}, fmt.Errorf("command pattern %q does not parse: %w", s, err)
	}
	if len(file.Stmts) != 1 || len(file.Last) > 0 {
		return Rule{}, fmt.Errorf("command pattern %q must be a single command", s)
	}
	stmt := file.Stmts[0]
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if stmt.Semicolon.IsValid() {
		return Rule{}, fmt.Errorf("command pattern %q must be a single command", s)
	}
	if !ok || len(call.Args) == 0 || len(call.Assigns) > 0 || len(stmt.Redirs) > 0 ||
		len(stmt.Comments) > 0 || stmt.Negated || stmt.Background || stmt.Coprocess {
		return Rule{}, fmt.Errorf("command pattern %q must be a single command of literal words", s)
	}
	r := Rule{src: src}
	for i, w := range call.Args {
		if hasBraceExpansion(w) {
			return Rule{}, fmt.Errorf("command pattern %q may not hold brace expansions", s)
		}
		pat, glob, ok := wordPattern(w)
		if !ok {
			return Rule{}, fmt.Errorf("command pattern %q may only hold literal words", s)
		}
		if i == 0 {
			if glob {
				return Rule{}, fmt.Errorf("command pattern %q must name its program literally", s)
			}
			name := unescapePattern(pat)
			if strings.HasPrefix(name, "~") {
				return Rule{}, fmt.Errorf("command pattern %q: write the absolute path of the program instead of ~", s)
			}
			if base := path.Base(name); strings.HasSuffix(name, "/") || base == "." || base == ".." {
				return Rule{}, fmt.Errorf("command pattern %q must name a program, not a directory", s)
			}
			r.Name = cleanName(name)
			continue
		}
		if i == len(call.Args)-1 && pat == "*" {
			r.Prefix = true
			continue
		}
		if _, err := pattern.Regexp(pat, pattern.EntireString); err != nil {
			return Rule{}, fmt.Errorf("command pattern %q: %w", s, err)
		}
		r.Args = append(r.Args, pat)
	}
	return r, nil
}

// hasBraceExpansion reports whether bash expands braces in w, as it does
// "{a,b}" and "{1..3}" but not "{}" or "{a}".
func hasBraceExpansion(w *syntax.Word) bool {
	split := *w
	if !syntax.SplitBraces(&split) {
		return false
	}
	return slices.ContainsFunc(split.Parts, func(p syntax.WordPart) bool {
		_, ok := p.(*syntax.BraceExp)
		return ok
	})
}

// wordPattern returns the shell pattern of w, and whether it holds glob
// characters, or false when w is not a literal word.
func wordPattern(w *syntax.Word) (string, bool, bool) {
	var b strings.Builder
	glob := false
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			for i := 0; i < len(p.Value); i++ {
				switch c := p.Value[i]; c {
				case '\\':
					if i+1 < len(p.Value) {
						i++
						if p.Value[i] != '\n' {
							b.WriteString(pattern.QuoteMeta(p.Value[i:i+1], 0))
						}
					}
				case '*', '?', '[':
					glob = true
					b.WriteByte(c)
				default:
					b.WriteByte(c)
				}
			}
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false, false
			}
			b.WriteString(pattern.QuoteMeta(p.Value, 0))
		case *syntax.DblQuoted:
			if p.Dollar {
				return "", false, false
			}
			for _, inner := range p.Parts {
				l, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false, false
				}
				b.WriteString(pattern.QuoteMeta(doubleQuotedLiteral(l.Value), 0))
			}
		default:
			return "", false, false
		}
	}
	return b.String(), glob, true
}

// unescapePattern returns the text a pattern without glob characters
// matches.
func unescapePattern(pat string) string {
	var b strings.Builder
	for i := 0; i < len(pat); i++ {
		if pat[i] == '\\' && i+1 < len(pat) {
			i++
		}
		b.WriteByte(pat[i])
	}
	return b.String()
}

// Match reports whether r describes the program p. A program whose name
// is not known until it runs matches no rule. A rule without Prefix only
// matches programs whose arguments are all known.
func (r Rule) Match(p Program) bool {
	if p.Opaque {
		return false
	}
	if cleanName(p.Name) != r.Name {
		return false
	}
	if r.Prefix {
		if len(p.Scope) < len(r.Args) {
			return false
		}
	} else if !p.Complete || len(p.Scope) != len(r.Args) {
		return false
	}
	for i, pat := range r.Args {
		expr, err := pattern.Regexp(pat, pattern.EntireString)
		if err != nil {
			return false
		}
		if ok, _ := regexp.MatchString(expr, p.Scope[i]); !ok {
			return false
		}
	}
	return true
}

// cleanName returns the program name with its path cleaned when it has
// one, keeping a slash in it so that it never reads as a bare name.
func cleanName(name string) string {
	if !strings.Contains(name, "/") {
		return name
	}
	c := path.Clean(name)
	if !strings.Contains(c, "/") {
		c = "./" + c
	}
	return c
}

// String returns the rule as written, with a final ":*" written " *".
func (r Rule) String() string {
	if r.src != "" {
		return r.src
	}
	words := append([]string{r.Name}, r.Args...)
	if r.Prefix {
		words = append(words, "*")
	}
	return strings.Join(words, " ")
}
