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
	"io/fs"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// Completer completes a word of a command line. Its contract is that
// of repl.CommandHandler's Complete, so that a console command handler
// is a Completer as it is: with args nil, cmd is the partial command
// name; otherwise cmd is the command and the last of args, "" after a
// space, is the partial word. Candidates are whole unquoted words,
// each to replace the partial one, and one ending in "/" names a
// directory, which completing does not end the word at.
type Completer interface {
	Complete(ctx context.Context, cmd string, args []string) (iterator.Iterator[string], error)
}

// completer is the shell's own Completer, in dir with the PATH value
// path and the functions funcs defined.
type completer struct {
	e     *Executor
	dir   string
	path  string
	funcs []string
}

var _ Completer = (*completer)(nil)

// dirCommands are the builtins whose arguments are directories.
var dirCommands = []string{"cd", "pushd"}

// shellBuiltins are the interpreter's builtins a command name completes
// to besides the programs in the PATH, which hold the rest: those that
// work without job control.
var shellBuiltins = []string{
	".", ":", "alias", "break", "builtin", "cd", "command", "continue", "declare", "dirs",
	"eval", "exec", "exit", "export", "getopts", "let", "local", "mapfile", "popd", "pushd",
	"read", "readarray", "readonly", "return", "set", "shift", "shopt", "source", "trap",
	"type", "typeset", "umask", "unalias", "unset",
}

// fileFilter selects the files a path completes to; directories always
// complete, as a way to the files in them.
type fileFilter int

const (
	anyFile fileFilter = iota
	dirsOnly
	executables
)

// Complete satisfies Completer. A command name completes to the
// builtins, functions and programs in the PATH it starts, and to the
// directories of dir; one with a slash to the executable files and
// directories it starts. The arguments of cd and pushd complete to
// directories; those of a Command with a Completer to what it yields,
// falling back on paths when it yields nothing; any other to paths.
func (c *completer) Complete(ctx context.Context, cmd string, args []string) (iterator.Iterator[string], error) {
	if args == nil {
		return iterator.FromSlice(c.commands(cmd)), nil
	}
	var word string
	if n := len(args); n > 0 {
		word = args[n-1]
	}
	if slices.Contains(dirCommands, cmd) {
		return iterator.FromSlice(c.paths(word, dirsOnly)), nil
	}
	if command, ok := c.e.commands[cmd]; ok && command.Completer != nil {
		if cands := collect(ctx, command.Completer, cmd, args); len(cands) > 0 {
			return iterator.FromSlice(cands), nil
		}
	}
	return iterator.FromSlice(c.paths(word, anyFile)), nil
}

// collect drains what cc completes; a failing Completer completes
// nothing.
func collect(ctx context.Context, cc Completer, cmd string, args []string) []string {
	it, err := cc.Complete(ctx, cmd, args)
	if err != nil {
		return nil
	}
	cands, err := iterator.ToSlice(ctx, it)
	if err != nil {
		return nil
	}
	return cands
}

func (c *completer) commands(prefix string) []string {
	if strings.Contains(prefix, "/") {
		return c.paths(prefix, executables)
	}
	var names []string
	for _, name := range slices.Concat(shellBuiltins, c.funcs) {
		if matches(name, prefix) {
			names = append(names, name)
		}
	}
	for _, d := range c.e.pathList(c.path) {
		if d == "" {
			d = "."
		}
		dir := absPath(c.dir, d)
		entries, _ := c.e.cfg.FS.ReadDir(dir)
		for _, ent := range entries {
			if name := ent.Name(); matches(name, prefix) && c.e.isExecutable(path.Join(dir, name)) {
				names = append(names, name)
			}
		}
	}
	names = append(names, c.paths(prefix, dirsOnly)...)
	slices.Sort(names)
	return slices.Compact(names)
}

// matches reports whether name completes prefix: it starts with it,
// and is hidden only when prefix is.
func matches(name, prefix string) bool {
	return strings.HasPrefix(name, prefix) && (!strings.HasPrefix(name, ".") || strings.HasPrefix(prefix, "."))
}

// paths completes word as a path relative to dir, keeping the
// directory part as written. A leading "~" is HOME; "~user" names no
// one. A prefix starting with "." also completes to "./" and "../".
func (c *completer) paths(word string, filter fileFilter) []string {
	if word == "~" {
		return []string{"~/"}
	}
	if strings.HasPrefix(word, "~") && !strings.HasPrefix(word, "~/") {
		return nil
	}
	dirPart, base := "", word
	if i := strings.LastIndexByte(word, '/'); i >= 0 {
		dirPart, base = word[:i+1], word[i+1:]
	}
	dir := absPath(c.dir, c.e.expandHome(dirPart))
	entries, err := c.e.cfg.FS.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	if strings.HasPrefix(base, ".") {
		for _, special := range []string{".", ".."} {
			if strings.HasPrefix(special, base) {
				out = append(out, dirPart+special+"/")
			}
		}
	}
	for _, ent := range entries {
		name := ent.Name()
		if !matches(name, base) {
			continue
		}
		full := path.Join(dir, name)
		isDir := ent.IsDir()
		if ent.Type()&fs.ModeSymlink != 0 {
			info, err := c.e.cfg.FS.Stat(full)
			isDir = err == nil && info.IsDir()
		}
		switch {
		case isDir:
			out = append(out, dirPart+name+"/")
		case filter == dirsOnly:
		case filter == executables && !c.e.isExecutable(full):
		default:
			out = append(out, dirPart+name)
		}
	}
	slices.Sort(out)
	return out
}

// completeLine is what Tab does at byte pos of line: it returns the
// line and cursor it leaves, or the line unchanged and the candidates
// to list when the word has several completions and none of them is
// longer than what was typed in common. A word with one completion is
// ended, its quote closed and a space added, unless it is a directory.
func (c *completer) completeLine(ctx context.Context, line string, pos int) (string, int, []string) {
	head, tail := line[:pos], line[pos:]
	lc := lexLine(head)
	if lc.comment {
		return line, pos, nil
	}
	var cands []string
	switch {
	case lc.redirect:
		cands = c.paths(lc.partial, anyFile)
	case len(lc.words) == 0:
		cands = collect(ctx, c, lc.partial, nil)
	default:
		cands = collect(ctx, c, lc.words[0], append(slices.Clone(lc.words[1:]), lc.partial))
	}
	cands = uniqueCandidates(cands)
	if len(cands) == 0 {
		return line, pos, nil
	}
	word, final := cands[0], !strings.HasSuffix(cands[0], "/")
	if len(cands) > 1 {
		word, final = commonPrefix(cands), false
		if len(word) <= len(lc.partial) || !strings.HasPrefix(word, lc.partial) {
			return line, pos, cands
		}
	}
	tilde := strings.HasPrefix(head[lc.start:], "~")
	newHead := head[:lc.start] + insertion(word, lc.quote, final, tilde)
	if final {
		if strings.HasPrefix(tail, " ") {
			return newHead + tail, len(newHead) + 1, nil
		}
		newHead += " "
	}
	return newHead + tail, len(newHead), nil
}

// uniqueCandidates drops empty and repeated candidates, keeping the
// order the completer gave.
func uniqueCandidates(cands []string) []string {
	seen := make(map[string]bool, len(cands))
	out := cands[:0:0]
	for _, c := range cands {
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// commonPrefix is the longest prefix of every candidate that does not
// split a character.
func commonPrefix(cands []string) string {
	p := cands[0]
	for _, c := range cands[1:] {
		n := 0
		for n < len(p) && n < len(c) && p[n] == c[n] {
			n++
		}
		p = p[:n]
	}
	start := len(p)
	for start > 0 && !utf8.RuneStart(p[start-1]) {
		start--
	}
	if start > 0 && !utf8.FullRuneInString(p[start-1:]) {
		p = p[:start-1]
	}
	return p
}

// lineContext is what completion reads of the text before the cursor.
type lineContext struct {
	// words are the unquoted words of the command the cursor is in
	// before the partial word, leaving out redirections and the
	// assignments and keywords that precede the command name.
	words []string
	// partial is the unquoted word the cursor ends, empty after a
	// blank or an operator. Its text starts at byte start, and quote
	// is the quote open at the cursor: 0, '\'' or '"'.
	partial string
	start   int
	quote   byte
	// redirect reports that the partial word is a redirection's target.
	redirect bool
	// comment reports that the cursor is in a comment.
	comment bool
}

// commandPrefixes are the keywords after which a command name follows.
var commandPrefixes = []string{"!", "{", "if", "then", "elif", "else", "do", "while", "until", "time"}

// lexFrame is a command being lexed: the line, or a command
// substitution in it.
type lexFrame struct {
	words []string
	word  strings.Builder
	// inWord reports that a word started at byte start; quoted that
	// some of it is quoted or escaped.
	inWord bool
	quoted bool
	start  int
	quote  byte
	// wordRedirect reports that the word is a redirection's target,
	// redirect that the next word is.
	wordRedirect bool
	redirect     bool
	// closer ends the frame: ')' for $( and '`' for a backtick, with
	// depth the parentheses of a $( open; 0 for the line.
	closer byte
	depth  int
}

func (f *lexFrame) begin(i int) {
	if f.inWord {
		return
	}
	f.inWord, f.quoted, f.start = true, false, i
	f.wordRedirect, f.redirect = f.redirect, false
}

func (f *lexFrame) endWord() {
	if !f.inWord {
		return
	}
	w := f.word.String()
	f.word.Reset()
	f.inWord = false
	switch {
	case f.wordRedirect:
	case len(f.words) == 0 && !f.quoted && (isAssignment(w) || slices.Contains(commandPrefixes, w)):
	default:
		f.words = append(f.words, w)
	}
}

// newCommand ends the word and the command it is in.
func (f *lexFrame) newCommand() {
	f.endWord()
	f.words, f.redirect = nil, false
}

// isAssignment reports whether w assigns a variable, as NAME=value or
// NAME+=value.
func isAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	name = strings.TrimSuffix(name, "+")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		if r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// lexLine reads head, the text before the cursor, as the shell would
// read the start of a line: quotes, escapes, operators, redirections,
// comments and command substitutions, which the cursor may be in.
func lexLine(head string) lineContext {
	stack := []*lexFrame{{}}
	f := stack[0]
	push := func(closer byte) {
		f = &lexFrame{closer: closer, depth: 1}
		stack = append(stack, f)
	}
	pop := func() {
		stack = stack[:len(stack)-1]
		f = stack[len(stack)-1]
	}
	next := func(i int) byte {
		if i+1 < len(head) {
			return head[i+1]
		}
		return 0
	}
	for i := 0; i < len(head); i++ {
		c := head[i]
		switch f.quote {
		case '\'':
			if c == '\'' {
				f.quote = 0
			} else {
				f.word.WriteByte(c)
			}
			continue
		case '"':
			switch {
			case c == '"':
				f.quote = 0
			case c == '\\' && strings.IndexByte("$`\"\\\n", next(i)) >= 0:
				i++
				if head[i] != '\n' {
					f.word.WriteByte(head[i])
				}
			case c == '\\' && i+1 == len(head):
			case c == '$' && next(i) == '(':
				i++
				push(')')
			case c == '`' && f.closer == '`':
				pop()
			case c == '`':
				push('`')
			default:
				f.word.WriteByte(c)
			}
			continue
		}
		switch {
		case c == '\\':
			f.begin(i)
			f.quoted = true
			if i+1 < len(head) {
				i++
				if head[i] != '\n' {
					f.word.WriteByte(head[i])
				}
			}
		case c == '\'' || c == '"':
			f.begin(i)
			f.quoted = true
			f.quote = c
		case c == ' ' || c == '\t':
			f.endWord()
		case c == '\n' || c == ';' || c == '|':
			f.newCommand()
		case c == '&' && next(i) == '>':
			f.endWord()
			f.redirect = true
			i++
			if next(i) == '>' {
				i++
			}
		case c == '&':
			f.newCommand()
		case c == '<' || c == '>':
			// A number right before the operator is the descriptor it
			// redirects, not a word.
			if f.inWord && !f.quoted && isDigits(f.word.String()) {
				f.word.Reset()
				f.inWord = false
			} else {
				f.endWord()
			}
			f.redirect = true
			for strings.IndexByte("<>&|", next(i)) >= 0 {
				i++
			}
		case c == '#' && !f.inWord:
			return lineContext{comment: true}
		case c == '$' && next(i) == '(':
			f.begin(i)
			i++
			push(')')
		case c == '`' && f.closer == '`':
			f.endWord()
			pop()
		case c == '`':
			f.begin(i)
			push('`')
		case c == '(':
			if f.closer == ')' {
				f.depth++
			}
			f.newCommand()
		case c == ')':
			if f.closer == ')' {
				if f.depth--; f.depth == 0 {
					f.endWord()
					pop()
					continue
				}
			}
			f.newCommand()
		default:
			f.begin(i)
			f.word.WriteByte(c)
		}
	}
	lc := lineContext{words: f.words, quote: f.quote, start: len(head), redirect: f.redirect}
	if f.inWord {
		lc.partial, lc.start, lc.redirect = f.word.String(), f.start, f.wordRedirect
	}
	return lc
}

// bareSpecial are the characters an unquoted word escapes.
const bareSpecial = " \\'\"`$&|;<>()*?[]#!{}"

// insertion is the text that puts word in the line in place of a
// partial word that had quote open, if any, so that the shell reads it
// back as word; final closes the quote. tilde keeps a leading "~/" as
// is, the HOME the partial word started with.
func insertion(word string, quote byte, final, tilde bool) string {
	var prefix string
	if tilde && strings.HasPrefix(word, "~/") {
		prefix, word = "~/", word[2:]
		if word == "" {
			return prefix
		}
	}
	if strings.ContainsFunc(word, isControl) {
		return prefix + ansiQuote(word)
	}
	var b strings.Builder
	b.WriteString(prefix)
	switch quote {
	case '\'':
		b.WriteByte('\'')
		b.WriteString(strings.ReplaceAll(word, "'", `'\''`))
	case '"':
		b.WriteByte('"')
		for _, r := range word {
			if strings.ContainsRune("\\\"$`", r) {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	default:
		for i, r := range word {
			if strings.ContainsRune(bareSpecial, r) || (i == 0 && r == '~' && prefix == "") {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
	}
	if final && quote != 0 {
		b.WriteByte(quote)
	}
	return b.String()
}

func isControl(r rune) bool {
	return r < 0x20 || r == 0x7f
}

// ansiQuote quotes w as $'...', the one quoting that writes control
// characters as text.
func ansiQuote(w string) string {
	var b strings.Builder
	b.WriteString("$'")
	for _, r := range w {
		switch {
		case r == '\\' || r == '\'':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case isControl(r):
			b.WriteString(`\x`)
			b.WriteByte("0123456789abcdef"[r>>4])
			b.WriteByte("0123456789abcdef"[r&0xf])
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('\'')
	return b.String()
}

// formatList lays candidates out in columns across width cells, sorted
// down the columns as ls does, each line ending in CRLF. A path shows
// as its last element.
func formatList(cands []string, width int) []byte {
	names := make([]string, len(cands))
	widths := make([]int, len(cands))
	maxWidth := 0
	for i, c := range cands {
		names[i] = displayName(c)
		widths[i] = uniseg.StringWidth(names[i])
		maxWidth = max(maxWidth, widths[i])
	}
	colWidth := maxWidth + 2
	cols := max(1, (width+2)/colWidth)
	rows := (len(names) + cols - 1) / cols
	var b []byte
	for r := range rows {
		for i := r; i < len(names); i += rows {
			b = append(b, names[i]...)
			if i+rows < len(names) {
				b = append(b, strings.Repeat(" ", colWidth-widths[i])...)
			}
		}
		b = append(b, '\r', '\n')
	}
	return b
}

// displayName is how a candidate is listed: the last element of a
// path, a directory keeping its slash, with control characters as "?".
func displayName(c string) string {
	if i := strings.LastIndexByte(strings.TrimSuffix(c, "/"), '/'); i >= 0 {
		c = c[i+1:]
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return '?'
		}
		return r
	}, c)
}
