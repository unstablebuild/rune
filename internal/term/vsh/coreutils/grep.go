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

package coreutils

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strconv"
	"strings"

	"unstable.build/rune/internal/term/vsh"
)

const grepSynopsis = "grep [-ivnclLrREFwHhoq] [-e PATTERN] PATTERN [FILE...]"

// Grep is "grep [-ivnclLrREFwHhoq] PATTERN [FILE...]". Patterns are
// RE2 regular expressions.
var Grep = vsh.Command{Name: "grep", Synopsis: grepSynopsis, Run: grep}

type grepOptions struct {
	re                             *regexp.Regexp
	invert, number, countOnly      bool
	listMatching, listNonMatching  bool
	recursive, onlyMatching, quiet bool
	withName, withoutName          bool
	files                          []string
}

func parseGrep(inv *vsh.Invocation) (*grepOptions, error) {
	o, err := parseArgs(inv.Name, inv.Args, "ivnclLrREFwHhoqe:", 0)
	if err != nil {
		return nil, usage(inv, err)
	}
	g := &grepOptions{
		invert: o.has('v'), number: o.has('n'), countOnly: o.has('c'),
		listMatching: o.has('l'), listNonMatching: o.has('L'),
		recursive: o.has('r') || o.has('R'), onlyMatching: o.has('o'), quiet: o.has('q'),
		withName: o.has('H'), withoutName: o.has('h'),
	}
	patterns := o.lists['e']
	operands := o.operands
	if len(patterns) == 0 {
		if len(operands) == 0 {
			return nil, usage(inv, fmt.Errorf("usage: %s", grepSynopsis))
		}
		patterns, operands = operands[:1], operands[1:]
	}
	alternatives := make([]string, len(patterns))
	for i, p := range patterns {
		if o.has('F') {
			p = regexp.QuoteMeta(p)
		} else if _, err := regexp.Compile(p); err != nil {
			// Report the pattern as typed, not the combined one.
			return nil, usage(inv, err)
		}
		alternatives[i] = `(?:` + p + `)`
	}
	pattern := strings.Join(alternatives, "|")
	if o.has('w') {
		pattern = `\b(?:` + pattern + `)\b`
	}
	if o.has('i') {
		pattern = `(?i)` + pattern
	}
	g.re, err = regexp.Compile(pattern)
	if err != nil {
		return nil, usage(inv, err)
	}
	g.files = operands
	if len(g.files) == 0 {
		if g.recursive {
			g.files = []string{"."}
		} else {
			g.files = []string{"-"}
		}
	}
	return g, nil
}

func grep(ctx context.Context, inv *vsh.Invocation) error {
	g, err := parseGrep(inv)
	if err != nil {
		return err
	}
	var files []string
	failed := false
	for _, name := range g.files {
		if !g.recursive || name == "-" {
			files = append(files, name)
			continue
		}
		err := walk(ctx, inv.FS, inv.Abs(name), func(p string, info fs.FileInfo) error {
			if info.Mode().IsRegular() {
				files = append(files, displayPath(inv, name, p))
			}
			return nil
		})
		if err != nil {
			fail(inv, name, err, &failed)
		}
	}
	showName := g.withName || ((len(files) > 1 || g.recursive) && !g.withoutName)
	matched := false
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		r, err := openRead(inv, name)
		if err != nil {
			fail(inv, name, err, &failed)
			continue
		}
		m, err := g.file(ctx, inv, name, r, showName)
		_ = r.Close()
		if err != nil {
			return err
		}
		matched = matched || m
		if matched && g.quiet {
			return nil
		}
	}
	switch {
	case failed:
		// An error outweighs a match, unless -q stopped at the match.
		return &vsh.ExitError{Code: 2}
	case !matched:
		return &vsh.ExitError{Code: 1}
	}
	return nil
}

// displayPath names a file found under operand the way grep -r does:
// relative to the operand as typed.
func displayPath(inv *vsh.Invocation, operand, abs string) string {
	root := inv.Abs(operand)
	rel := strings.TrimPrefix(strings.TrimPrefix(abs, root), "/")
	if rel == "" {
		return operand
	}
	if operand == "." {
		return rel
	}
	return strings.TrimSuffix(operand, "/") + "/" + rel
}

func (g *grepOptions) file(ctx context.Context, inv *vsh.Invocation, name string, r io.Reader, showName bool) (bool, error) {
	label := name
	if name == "-" {
		label = "(standard input)"
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	n, hits := 0, 0
	var out bytes.Buffer
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n++
		line := sc.Bytes()
		match := g.re.Match(line) != g.invert
		if !match {
			continue
		}
		hits++
		if g.quiet || g.listMatching || g.listNonMatching {
			if g.quiet || g.listMatching {
				break
			}
			continue
		}
		if g.countOnly {
			continue
		}
		prefix := ""
		if showName {
			prefix = label + ":"
		}
		if g.number {
			prefix += strconv.Itoa(n) + ":"
		}
		if g.onlyMatching && !g.invert {
			for _, m := range g.re.FindAll(line, -1) {
				if len(m) == 0 {
					continue
				}
				out.WriteString(prefix)
				out.Write(m)
				out.WriteByte('\n')
			}
		} else {
			out.WriteString(prefix)
			out.Write(line)
			out.WriteByte('\n')
		}
		if out.Len() > 32<<10 {
			if _, err := inv.Stdout.Write(out.Bytes()); err != nil {
				return false, err
			}
			out.Reset()
		}
	}
	if err := sc.Err(); err != nil {
		return false, err
	}
	matched := hits > 0
	switch {
	case g.quiet:
	case g.listMatching || g.listNonMatching:
		if matched == g.listMatching {
			fmt.Fprintln(&out, label)
		}
	case g.countOnly:
		if showName {
			fmt.Fprintf(&out, "%s:%d\n", label, hits)
		} else {
			fmt.Fprintf(&out, "%d\n", hits)
		}
	}
	if out.Len() > 0 {
		if _, err := inv.Stdout.Write(out.Bytes()); err != nil {
			return false, err
		}
	}
	if g.listNonMatching {
		return !matched, nil
	}
	return matched, nil
}
