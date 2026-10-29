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

package ideauthorizer

import (
	"cmp"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// maxScriptDepth bounds how deep shells nested in scripts are followed.
const maxScriptDepth = 8

// shellWord is a word of a command. text is its value after quote
// removal when lit, and its source otherwise.
type shellWord struct {
	text string
	lit  bool
	// word is the word in the script the shell expands, or nil when there
	// is none or what it expands to is not known until the command runs.
	word *syntax.Word
}

func literalWords(texts []string) []shellWord {
	ret := make([]shellWord, len(texts))
	for i, t := range texts {
		ret[i] = shellWord{text: t, lit: true}
	}
	return ret
}

func leadingLiterals(words []shellWord) []string {
	ret := make([]string, 0, len(words))
	for _, w := range words {
		if !w.lit {
			break
		}
		ret = append(ret, w.text)
	}
	return ret
}

type programRole int

const (
	// roleScoped programs need a decision of their own.
	roleScoped programRole = iota
	// roleBound programs, such as the shell of a script or the wrappers
	// of a command, are only bound by fingerprint.
	roleBound
)

// programInvocation is a program a command runs.
type programInvocation struct {
	// Name is the program as written. It is the source of the word when
	// Opaque.
	Name string
	// Args are the literal arguments up to the first that is not.
	Args []string
	// Scope are the arguments scopes are built from: Args without the
	// global options that don't change what runs (git -C).
	Scope []string
	// Complete reports that Args are every argument the program gets,
	// so that it can be approved exactly.
	Complete bool
	// Code is the index into Scope of the first argument that carries
	// code, len(Scope) when the code comes from the standard input, or
	// -1.
	Code int
	Role programRole
	// Shell is the index into the plan's programs of the shell a builtin
	// runs in, or -1 for programs that are files.
	Shell int
	// TopLevel reports that the program is the Cmd.Path of the command,
	// which the workspace host looks up its own way.
	TopLevel bool
	// Opaque reports that the name is not known until the command runs.
	Opaque bool
	// StdPATH reports that the program is looked up in the standard
	// PATH (command -p).
	StdPATH bool
	// Dir tells which other directory than the command's the program
	// runs in, where names relative to the directory are not known, or
	// is "".
	Dir string
	// First reports that the program runs before anything else the
	// command runs, which therefore cannot have created it.
	First bool
}

// commandPlan is what the authorizer knows of the programs a command runs.
type commandPlan struct {
	Programs []programInvocation
	// Opaque reports that the programs cannot be told, so the command is
	// only approvable exactly. Reason says why.
	Opaque bool
	Reason string
	// DirChanged reports that the command changes its directory before
	// it runs some program, so names relative to the directory are not
	// known.
	DirChanged bool
	// Deletes are the recursive deletions the command runs, of those the
	// authorizer follows.
	Deletes []RecursiveDelete
}

// envLookup looks a variable up in the environment a command starts with.
// set reports that the variable is set, and known that its value can be
// told before the command runs.
type envLookup func(name string) (value string, set, known bool)

type planner struct {
	plan  commandPlan
	funcs map[string]bool
	depth int
	// arith reports a context that evaluates arithmetic on a value not
	// known to be a number, which runs the command substitutions of the
	// array subscripts the value holds.
	arith bool
	// suspicious reports a string holding a command substitution, or
	// data the script gets while it runs, such as the output of a
	// command, which may hold one.
	suspicious bool
	// dir is the Dir of the programs added.
	dir string
	// firstStmt is the statement of the script being planned whose
	// program runs first, or nil.
	firstStmt *syntax.Stmt
	// env is the environment the command starts with, or nil when it is
	// not known.
	env envLookup
	// vars are the variables the script being planned set before the
	// statement being planned. A nil value is not known until it runs.
	vars map[string]*string
	// straight are the statements of the script being planned that run
	// one after another, whose assignments vars follows.
	straight map[*syntax.Stmt]bool
	// cmdEnv names the variables the command being planned gets in its
	// environment on top of those the script exports.
	cmdEnv []string
	// nodes enclose the node of the scripts being planned that is visited.
	nodes []syntax.Node
	// lang is the language of the script being planned.
	lang syntax.LangVariant
	// startEnv names the variables the command sets in the environment it
	// starts with.
	startEnv []string
}

type callContext struct {
	topLevel bool
	// first reports that the call runs before anything else the command
	// runs.
	first bool
	// shell is the index of the shell that runs the call, or -1 when a
	// program execs it.
	shell    int
	appended bool
	stdPATH  bool
	// noFuncs reports that the call bypasses shell functions (command).
	noFuncs bool
}

// planCommand returns the programs the command path args runs. env holds
// the variables the command sets to values other than the IDE's own, and
// hostEnv, which may be nil, looks up those it starts with.
func planCommand(path string, args []string, env []string, hostEnv envLookup) commandPlan {
	p := &planner{funcs: map[string]bool{}, env: hostEnv, vars: map[string]*string{}}
	if path == "" {
		p.opaque("it runs the login shell")
		return p.plan
	}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		// The authorizer looks programs up in the PATH a command sets.
		if name != "PATH" && dangerousEnv(name) {
			p.opaque("it sets " + name)
			return p.plan
		}
		p.startEnv = append(p.startEnv, name)
		p.suspicious = p.suspicious || hidesCommand(value)
	}
	p.expand(literalWords(append([]string{path}, args...)), callContext{
		topLevel: true, shell: -1,
	})
	if p.arith && p.suspicious {
		p.opaque("its arithmetic may run command substitutions hidden in strings")
	}
	if !slices.ContainsFunc(p.plan.Programs, func(inv programInvocation) bool {
		return inv.Role == roleScoped && !inv.Opaque
	}) {
		p.opaque("it runs no program a scope can describe")
	}
	return p.plan
}

func (p *planner) opaque(reason string) {
	if p.plan.Opaque {
		return
	}
	p.plan.Opaque = true
	p.plan.Reason = reason
}

func (p *planner) zsh() bool {
	return p.lang == syntax.LangZsh
}

func (p *planner) builtins() shellBuiltins {
	if p.zsh() {
		return zshBuiltins
	}
	return bashBuiltins
}

// dangerous reports whether setting the variable name in the script
// being planned changes what programs run.
func (p *planner) dangerous(name string) bool {
	return dangerousEnv(name) || (p.zsh() && zshDangerousEnv(name))
}

func (p *planner) add(inv programInvocation) int {
	inv.Dir = p.dir
	p.plan.Programs = append(p.plan.Programs, inv)
	return len(p.plan.Programs) - 1
}

func (p *planner) invocation(name string, args []shellWord, c callContext) programInvocation {
	lits := leadingLiterals(args)
	return programInvocation{
		Name:     name,
		Args:     lits,
		Scope:    lits,
		Complete: len(lits) == len(args) && !c.appended,
		Code:     -1,
		Shell:    -1,
		TopLevel: c.topLevel,
		StdPATH:  c.stdPATH,
		First:    c.first || c.topLevel,
	}
}

func (p *planner) bound(name string, args []shellWord, c callContext) int {
	inv := p.invocation(name, args, c)
	inv.Role = roleBound
	return p.add(inv)
}

// expand adds the programs the command words runs.
func (p *planner) expand(words []shellWord, c callContext) {
	if p.plan.Opaque || len(words) == 0 {
		return
	}
	if !words[0].lit {
		p.add(programInvocation{Name: words[0].text, Opaque: true, Code: -1, Shell: -1})
		return
	}
	name, args := words[0].text, words[1:]
	if c.shell >= 0 && !strings.Contains(name, "/") && p.builtin(name, args, c) {
		return
	}
	spec, ok := lookupProgramSpec(name)
	if !ok || (spec.builtin && c.shell < 0) {
		p.recursiveDelete(name, args, c)
		p.add(p.invocation(name, args, c))
		return
	}
	base := filepath.Base(name)
	switch spec.kind {
	case kindTransparent:
		p.transparent(name, args, spec, c)
	case kindEscalating:
		p.escalating(name, args, spec, c)
	case kindSpawning:
		cmds, ok := spec.embedded(args)
		if !ok {
			p.opaque(base + " runs commands that cannot be followed")
			return
		}
		p.add(p.invocation(name, args, c))
		outer := p.dir
		if slices.ContainsFunc(args, func(w shellWord) bool { return isOption(w, spec.dirOpts) }) {
			p.dir = base + " " + spec.dir
		}
		for _, cmd := range cmds {
			p.embed(base, cmd, spec.placeholders, spec.found,
				callContext{shell: -1, appended: spec.appends})
		}
		p.dir = outer
	case kindShell:
		p.shell(name, args, spec, c)
	case kindInterpreter:
		p.interpreter(name, args, spec, c)
	case kindOpaque:
		p.opaque(spec.reason)
	}
}

// builtin adds the shell builtin name, if it is one, and reports whether
// it was.
func (p *planner) builtin(name string, args []shellWord, c callContext) bool {
	if p.funcs[name] && !c.noFuncs {
		// The function's body is planned where it is declared.
		return true
	}
	b := p.builtins()
	if b.opaque[name] {
		p.opaque(name + " runs code that cannot be followed")
		return true
	}
	if b.dir[name] {
		p.plan.DirChanged = true
	}
	if b.arith[name] && p.evaluates(name, args) {
		p.arith = true
	}
	if b.input[name] {
		p.suspicious = true
	}
	if spec, ok := b.assigning[name]; ok {
		p.checkAssigned(name, spec, args)
	}
	switch {
	case p.zsh() && (name == "print" || name == "printf"):
		p.checkZshPrint(name, args)
	case p.zsh() && name == "set":
		p.checkZshSet(args)
	case name == "printf":
		opts, _, _ := optionSpec{short: "v:"}.scan(args)
		if o, ok := hasOption(opts, []string{"-v"}); ok {
			p.checkName(o.value)
		}
	}
	if b.noProgram[name] {
		return true
	}
	if !b.programs[name] {
		return false
	}
	inv := p.invocation(name, args, c)
	inv.Shell = c.shell
	inv.TopLevel = false
	p.add(inv)
	return true
}

// checkZshPrint makes the plan opaque when zsh's print or printf, name,
// runs prompt expansions, which run code with the PROMPT_SUBST option,
// or sets a variable that changes what programs run. A word that is not
// literal may hold either option.
func (p *planner) checkZshPrint(name string, args []shellWord) {
	spec := optionSpec{short: "v:"}
	if name == "print" {
		spec = zshPrintOptions
	}
	opts, _, ok := spec.scan(args)
	if !ok {
		p.opaque("the options of " + name + " cannot be followed")
		return
	}
	if _, found := hasOption(opts, []string{"-P"}); found {
		p.opaque("print -P runs prompt expansions, which can run code")
		return
	}
	if o, found := hasOption(opts, []string{"-v"}); found {
		p.checkName(o.value)
	}
}

// checkZshSet makes the plan opaque when zsh's set changes options other
// than those zsh -c may take, or sets an array (set -A).
func (p *planner) checkZshSet(args []shellWord) {
	for _, w := range args {
		switch {
		case !w.lit:
			p.opaque("the options set changes are not known until it runs")
			return
		case w.text == "-" || w.text == "--" ||
			!strings.HasPrefix(w.text, "-") && !strings.HasPrefix(w.text, "+"):
			return
		case strings.Trim(w.text[1:], "euvx") != "":
			p.opaque("set " + w.text + " changes what the script runs")
			return
		}
	}
}

// checkAssigned makes the plan opaque when the builtin name, which takes
// the options spec, sets a variable that changes what programs run, or
// one it cannot tell.
func (p *planner) checkAssigned(name string, spec optionSpec, args []shellWord) {
	opts, operand, ok := spec.scan(args)
	if !ok {
		p.opaque("the variables " + name + " sets are not known until it runs")
		return
	}
	for _, o := range opts {
		switch {
		case p.zsh() && (o.name == "-f" || o.name == "-m") && name != "unset":
			p.opaque(name + " " + o.name + " changes functions or sets variables by pattern")
			return
		case o.name == "-C":
			p.opaque(name + " -C runs code that cannot be followed")
			return
		case o.name == "-n" && name != "unset" && name != "export" && name != "read":
			p.opaque("namerefs hide the variables they set")
			return
		case o.name == "-a" && name == "read":
			p.checkName(o.value)
		case o.name == "-f" && name == "unset":
			p.opaque(unsetFunction)
			return
		case o.name == "-f" && name == "export":
			// Functions, not variables.
			return
		}
	}
	_, varsOnly := hasOption(opts, []string{"-v"})
	names := args[operand:]
	if name == "getopts" {
		if len(names) < 2 {
			return
		}
		names = names[1:2]
	}
	for _, w := range names {
		text := w.text
		if p.zsh() && name == "read" {
			// zsh's read prompts with what follows a '?'.
			text, _, _ = strings.Cut(text, "?")
		}
		// bash unsets the function of a name no variable has.
		if name == "unset" && !p.zsh() && !varsOnly && p.funcs[text] {
			p.opaque(unsetFunction)
			return
		}
		p.checkName(text)
	}
}

const unsetFunction = "unsetting a function makes later calls of it run a program"

// evaluates reports whether the builtin name, which may evaluate
// arithmetic, evaluates a value not known to be a number given args.
func (p *planner) evaluates(name string, args []shellWord) bool {
	switch name {
	case "let", "integer", "float":
		return true
	case "test", "[":
		if n := len(args); name == "[" && n > 0 && args[n-1].lit && args[n-1].text == "]" {
			args = args[:n-1]
		}
		return testEvaluates(args)
	case "printf", "print":
		spec := optionSpec{short: "v:"}
		if name == "print" {
			spec = zshPrintOptions
		}
		opts, operand, ok := spec.scan(args)
		if !ok {
			return true
		}
		if o, found := hasOption(opts, []string{"-v"}); found && !isName(shellWord{text: o.value, lit: true}) {
			return true
		}
		if name == "print" {
			o, found := hasOption(opts, []string{"-f"})
			return found && formatEvaluates(o.value)
		}
		return operand < len(args) && (!args[operand].lit || formatEvaluates(args[operand].text))
	case "shift", "exit", "return", "break", "continue", "bye", "logout":
		return slices.ContainsFunc(args, func(w shellWord) bool { return !isNumberWord(w) })
	}
	spec, ok := p.builtins().assigning[name]
	if !ok {
		return true
	}
	opts, operand, ok := spec.scan(args)
	if !ok {
		return true
	}
	for _, o := range opts {
		switch {
		case (name == "mapfile" || name == "readarray") && strings.Contains("nOsuc", o.name[1:]) &&
			!numberLiteral.MatchString(o.value):
			return true
		case o.name == "-a" && name == "read" && !isName(shellWord{text: o.value, lit: true}):
			return true
		case name != "read" && p.declares(o.name):
			return true
		}
	}
	names := args[operand:]
	if name == "getopts" {
		names = names[min(1, len(names)):min(2, len(names))]
	}
	return slices.ContainsFunc(names, func(w shellWord) bool {
		if p.zsh() && name == "read" {
			w.text, _, _ = strings.Cut(w.text, "?")
		}
		return !w.lit || p.declares(w.text)
	})
}

// testEvaluates reports whether test, given args, may evaluate an
// operand not known to be a number: one of a numeric comparison, -t or
// -v, or a word that is not literal where it may expand to one of those.
func testEvaluates(args []shellWord) bool {
	operator := func(i int) bool {
		return i >= 0 && i < len(args) && args[i].lit && (strings.HasPrefix(args[i].text, "-") ||
			slices.Contains([]string{"=", "==", "!=", "<", ">", "!", "=~"}, args[i].text))
	}
	number := func(i int) bool { return i >= 0 && i < len(args) && isNumberWord(args[i]) }
	for i, w := range args {
		if !w.lit {
			if len(args) > 1 && !operator(i-1) && !operator(i+1) {
				return true
			}
			continue
		}
		switch w.text {
		case "-eq", "-ne", "-lt", "-le", "-gt", "-ge":
			if !number(i-1) || !number(i+1) {
				return true
			}
		case "-t":
			if i+1 < len(args) && !number(i+1) {
				return true
			}
		case "-v", "-R":
			if i+1 < len(args) && !isName(args[i+1]) {
				return true
			}
		}
	}
	return false
}

// formatEvaluates reports whether the printf format f converts an
// argument as a number, which evaluates it, or takes a width or
// precision from one.
func formatEvaluates(f string) bool {
	for i := 0; i < len(f); i++ {
		if f[i] != '%' {
			continue
		}
		j := i + 1
		for j < len(f) && strings.IndexByte("-+ #0123456789.'", f[j]) >= 0 {
			j++
		}
		if j >= len(f) {
			return false
		}
		switch c := f[j]; {
		case c == '%':
		case c == '*' || !strings.ContainsRune("sbcq", rune(c)):
			return true
		}
		i = j
	}
	return false
}

func (p *planner) checkName(text string) {
	n := assignedName(text)
	if n == "" {
		p.opaque("it sets a variable that is not known until it runs")
		return
	}
	p.forget(n)
	if p.dangerous(n) {
		p.opaque("it sets " + n)
	}
}

// assignedName returns the variable an operand such as "NAME=value",
// "NAME[i]" or "NAME" sets, or "" if it doesn't start with a name.
func assignedName(text string) string {
	end := 0
	for end < len(text) {
		c := text[end]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') &&
			(end == 0 || c < '0' || c > '9') {
			break
		}
		end++
	}
	if end == 0 || (end < len(text) && !strings.ContainsRune("=[+", rune(text[end]))) {
		return ""
	}
	return text[:end]
}

func (p *planner) skipAssignments(base string, rest []shellWord) []shellWord {
	for len(rest) > 0 {
		if !rest[0].lit {
			p.opaque("the environment " + base + " sets is not known until it runs")
			return nil
		}
		if base == "env" && rest[0].text == "-" {
			p.opaque("env - clears the environment")
			return nil
		}
		n, _, ok := strings.Cut(rest[0].text, "=")
		if !ok {
			break
		}
		if p.dangerous(n) {
			p.opaque("it sets " + n)
			return nil
		}
		p.cmdEnv = append(p.cmdEnv, n)
		rest = rest[1:]
	}
	return rest
}

func (p *planner) checkEnvOptions(opts []parsedOption, spec programSpec) {
	for _, o := range opts {
		if !slices.Contains(spec.envOpts, o.name) {
			continue
		}
		if n, _, _ := strings.Cut(o.value, "="); p.dangerous(n) {
			p.opaque("it sets " + n)
		}
	}
}

func (p *planner) transparent(name string, args []shellWord, spec programSpec, c callContext) {
	base := filepath.Base(name)
	var opts []parsedOption
	var start int
	var ok bool
	if spec.commandStart != nil {
		start, ok = spec.commandStart(args)
	} else {
		opts, start, ok = spec.opts.scan(args)
	}
	if !ok {
		p.opaque("the options of " + base + " cannot be followed")
		return
	}
	if o, found := hasOption(opts, spec.opaque); found {
		p.opaque(base + " " + o.name + " runs a command that cannot be followed")
		return
	}
	p.checkEnvOptions(opts, spec)
	if !spec.builtin {
		p.bound(name, args, c)
	}
	if _, found := hasOption(opts, spec.none); found {
		return
	}
	rest := args[start:]
	if len(rest) < spec.operands {
		return
	}
	for _, w := range rest[:spec.operands] {
		if !w.lit {
			p.opaque("the command " + base + " runs is not known until it runs")
			return
		}
	}
	rest = rest[spec.operands:]
	if spec.envAssign {
		rest = p.skipAssignments(base, rest)
	}
	if p.plan.Opaque {
		return
	}
	if len(rest) == 0 {
		if spec.fallback == "" {
			return
		}
		rest = []shellWord{{text: spec.fallback, lit: true}}
	}
	inner := callContext{shell: -1, appended: c.appended || spec.appends}
	switch {
	case !spec.builtin:
	case p.zsh() && name == "exec":
		// zsh's exec runs builtins and functions too.
		inner.shell = c.shell
	case p.zsh() && name == "command":
		// zsh's command runs programs only, unless options of the user's
		// startup files make it run builtins too.
		if rest[0].lit && zshBuiltins.opaque[rest[0].text] {
			p.opaque("command " + rest[0].text + " may run the builtin, whose code cannot be followed")
			return
		}
	case name != "exec":
		inner.shell = c.shell
		inner.noFuncs = true
	}
	if _, found := hasOption(opts, spec.stdPATH); found {
		inner.stdPATH = true
	}
	var placeholders []string
	if o, found := hasOption(opts, spec.replaceOpts); found {
		placeholders = []string{cmp.Or(o.value, "{}")}
	}
	p.embed(base, rest, placeholders, spec.found, inner)
}

// embed adds the programs of cmd, which base runs with found in place of
// placeholders. When the command cannot be followed only because what
// replaces them is not known until it runs, the plan says so.
func (p *planner) embed(base string, cmd []shellWord, placeholders []string, found string, c callContext) {
	hidden := withPlaceholders(cmd, placeholders)
	if p.plan.Opaque || slices.Equal(hidden, cmd) {
		p.expand(cmd, c)
		return
	}
	probe := &planner{funcs: p.funcs, depth: p.depth, dir: p.dir, vars: map[string]*string{},
		lang: p.lang}
	probe.expand(cmd, c)
	if probe.plan.Opaque {
		p.opaque(probe.plan.Reason)
		return
	}
	p.expand(hidden, c)
	if p.plan.Opaque {
		p.plan.Reason = base + " puts " + found + " in the command it runs, where they can run as code"
	}
}

func (p *planner) escalating(name string, args []shellWord, spec programSpec, c callContext) {
	base := filepath.Base(name)
	opts, start, ok := spec.opts.scan(args)
	if !ok {
		p.opaque("the options of " + base + " cannot be followed")
		return
	}
	if o, found := hasOption(opts, spec.opaque); found {
		p.opaque(base + " " + o.name + " runs a shell")
		return
	}
	p.checkEnvOptions(opts, spec)
	inv := p.invocation(name, args, c)
	// Whatever follows the program runs as another user.
	inv.Code = 0
	p.add(inv)
	if _, found := hasOption(opts, spec.none); found {
		return
	}
	rest := args[start:]
	if spec.envAssign {
		rest = p.skipAssignments(base, rest)
	}
	if p.plan.Opaque {
		return
	}
	if len(rest) == 0 {
		if spec.requireCommand {
			p.opaque(base + " without a command runs a shell")
		}
		return
	}
	if rest[0].lit {
		p.recursiveDelete(rest[0].text, rest[1:], callContext{shell: -1})
		p.bound(rest[0].text, rest[1:], callContext{shell: -1})
	}
}

func (p *planner) shell(name string, args []shellWord, spec programSpec, c callContext) {
	base := filepath.Base(name)
	opts, start, ok := spec.opts.scan(args)
	if !ok {
		p.opaque("the options of " + base + " cannot be followed")
		return
	}
	if o, found := hasOption(opts, []string{"-s", "--rcfile", "--init-file"}); found {
		p.opaque(base + " " + o.name + " runs code that cannot be followed")
		return
	}
	if _, found := hasOption(opts, []string{"-c"}); !found || start >= len(args) {
		p.opaque(base + " runs a script file or its standard input")
		return
	}
	if !args[start].lit {
		p.opaque("the script " + base + " runs is not known until it runs")
		return
	}
	if p.depth >= maxScriptDepth {
		p.opaque("its shells nest too deep")
		return
	}
	if spec.lang == syntax.LangZsh {
		// zsh runs programs that variables of its environment name. The
		// variables of the script that starts it may be exported.
		names := slices.Concat(p.startEnv, p.cmdEnv, slices.Sorted(maps.Keys(p.vars)))
		if i := slices.IndexFunc(names, zshDangerousEnv); i >= 0 {
			p.opaque("it sets " + names[i])
			return
		}
	}
	// Arithmetic may evaluate the arguments the script gets, which file
	// names or input put in place of placeholders, or after the script.
	p.suspicious = p.suspicious || c.appended
	for _, w := range args[start+1:] {
		p.suspicious = p.suspicious || hidesCommand(w.text) || !w.lit && w.word == nil
	}
	shell := p.bound(name, args, c)
	p.script(args[start].text, spec.lang, shell, c.first || c.topLevel)
}

func (p *planner) interpreter(name string, args []shellWord, spec programSpec, c callContext) {
	base := filepath.Base(name)
	lits := leadingLiterals(args)
	if spec.remoteCommand {
		_, operand, ok := spec.opts.scan(args)
		if !ok {
			p.opaque("the options of " + base + " cannot be followed")
			return
		}
		if len(args)-operand > 1 {
			p.opaque(base + " runs a command on another host")
			return
		}
	}
	inv := p.invocation(name, args, c)
	switch {
	case spec.scope != nil:
		inv.Scope, inv.Code = spec.scope(lits)
	case len(spec.codeSub) > 0:
		inv.Code = slices.IndexFunc(lits, func(a string) bool {
			return slices.Contains(spec.codeSub, a)
		})
		if inv.Code < 0 && len(lits) < len(args) {
			p.opaque("the subcommand of " + base + " is not known until it runs")
			return
		}
	default:
		inv.Code = codeIndex(spec, lits, len(lits) < len(args))
	}
	if inv.Code >= 0 && inv.Code == len(inv.Scope) && !spec.codeAlways {
		inv.Complete = false
	}
	p.add(inv)
}

// codeIndex returns the index into args of the first that carries code,
// len(args) when the program reads code from its standard input, or -1.
// more reports that args are followed by words that are not literal.
func codeIndex(spec programSpec, args []string, more bool) int {
	if spec.codeAlways {
		return 0
	}
	operand := more
	for i, a := range args {
		if a == "-" && spec.stdinCode {
			return i
		}
		if a == "--" {
			operand = operand || i+1 < len(args)
			break
		}
		if slices.Contains(spec.code, a) {
			return i
		}
		if strings.HasPrefix(a, "--") {
			if n, _, _ := strings.Cut(a, "="); slices.Contains(spec.code, n) {
				return i
			}
			continue
		}
		if strings.HasPrefix(a, "-") && len(a) > 1 {
			for _, c := range spec.code {
				if len(c) == 2 && c[0] == '-' && strings.Contains(a[1:], c[1:]) {
					return i
				}
			}
			continue
		}
		operand = true
		// Interpreters pass what follows their script to it.
		if spec.stdinCode {
			break
		}
	}
	if spec.stdinCode && !operand {
		return len(args)
	}
	return -1
}

// script adds the programs of the script that shell runs. first reports
// that the shell runs first, and so does the first statement of the
// script unless something runs before it does.
func (p *planner) script(src string, lang syntax.LangVariant, shell int, first bool) {
	file, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(src), "")
	if err != nil {
		p.opaque("its script does not parse as " + lang.String())
		return
	}
	outer, outerFirst := p.funcs, p.firstStmt
	outerVars, outerStraight := p.vars, p.straight
	outerLang := p.lang
	p.lang = lang
	p.funcs = map[string]bool{}
	p.firstStmt = nil
	if first && len(file.Stmts) > 0 && runsFirst(file.Stmts[0], lang) {
		p.firstStmt = file.Stmts[0]
	}
	// A shell gets the variables of the script that starts it only when
	// they are exported, which vars does not follow.
	p.vars = map[string]*string{}
	for n := range outerVars {
		p.vars[n] = nil
	}
	for _, n := range p.cmdEnv {
		p.vars[n] = nil
	}
	p.straight = map[*syntax.Stmt]bool{}
	for _, s := range file.Stmts {
		p.straight[s] = !s.Background && !s.Coprocess
	}
	p.depth++
	defer func() {
		p.funcs, p.firstStmt = outer, outerFirst
		p.vars, p.straight = outerVars, outerStraight
		p.lang = outerLang
		p.depth--
	}()
	syntax.Walk(file, func(n syntax.Node) bool {
		if n == nil {
			p.nodes = p.nodes[:len(p.nodes)-1]
			return true
		}
		p.visit(n, shell)
		if p.plan.Opaque {
			return false
		}
		p.nodes = append(p.nodes, n)
		return true
	})
}

func (p *planner) visit(n syntax.Node, shell int) {
	switch n := n.(type) {
	case *syntax.Stmt:
		p.assign(n)
		call, ok := n.Cmd.(*syntax.CallExpr)
		if p.zsh() && len(n.Redirs) > 0 && (n.Cmd == nil || ok && len(call.Args) == 0) &&
			!p.readsFile(n) {
			// zsh runs $NULLCMD or $READNULLCMD in place of the command.
			p.opaque("zsh runs a program for a redirection without a command")
			return
		}
		if !ok || len(call.Args) == 0 {
			return
		}
		words := make([]shellWord, len(call.Args))
		for i, w := range call.Args {
			words[i] = wordOf(w, p.lang)
		}
		p.cmdEnv = p.cmdEnv[:0]
		for _, a := range call.Assigns {
			if a.Name != nil {
				p.cmdEnv = append(p.cmdEnv, a.Name.Value)
			}
		}
		p.expand(words, callContext{shell: shell, first: n == p.firstStmt})
		p.cmdEnv = p.cmdEnv[:0]
	case *syntax.FuncDecl:
		// A name is a function from its declaration on, unless the
		// declaration may not run: calls before it, or of one in a branch,
		// run the program of that name.
		if s, ok := p.nodes[len(p.nodes)-1].(*syntax.Stmt); !ok || !p.straight[s] {
			return
		}
		for _, name := range append([]*syntax.Lit{n.Name}, n.Names...) {
			if name != nil {
				p.funcs[name.Value] = true
			}
		}
	case *syntax.Assign:
		if n.Name != nil && p.dangerous(n.Name.Value) {
			p.opaque("it sets " + n.Name.Value)
		}
		if readsValues(n.Index) || (n.Name != nil && p.builtins().integers[n.Name.Value]) {
			p.arith = true
		}
	case *syntax.ArrayElem:
		p.arith = p.arith || readsValues(n.Index)
	case *syntax.DeclClause:
		if n.Variant != nil && n.Variant.Value == "nameref" {
			p.opaque("namerefs hide the variables they set")
			return
		}
		for _, a := range n.Args {
			if !a.Naked || a.Name != nil || a.Value == nil {
				continue
			}
			w := wordOf(a.Value, p.lang)
			switch {
			case !w.lit:
				p.opaque("it sets a variable that is not known until it runs")
			case p.zsh() && (strings.HasPrefix(w.text, "-") || strings.HasPrefix(w.text, "+")) &&
				strings.ContainsAny(w.text, "fm"):
				p.opaque(n.Variant.Value + " " + w.text +
					" changes functions or sets variables by pattern")
			case strings.HasPrefix(w.text, "-") && strings.Contains(w.text, "n"):
				p.opaque("namerefs hide the variables they set")
			}
			p.arith = p.arith || p.declares(w.text)
		}
	case *syntax.WordIter:
		if n.Name != nil {
			p.forget(n.Name.Value)
		}
		if n.Name != nil && p.dangerous(n.Name.Value) {
			p.opaque("it sets " + n.Name.Value)
		}
		if n.Name != nil && p.builtins().integers[n.Name.Value] {
			p.arith = true
		}
	case *syntax.CoprocClause:
		if n.Name != nil {
			if w := wordOf(n.Name, p.lang); !w.lit || p.dangerous(w.text) {
				p.opaque("it sets " + w.text)
			}
		}
	case *syntax.Redirect:
		if n.N != nil && p.dangerous(strings.Trim(n.N.Value, "{}")) {
			p.opaque("it sets " + strings.Trim(n.N.Value, "{}"))
		}
	case *syntax.ParamExp:
		if n.Excl || readsValues(n.Index) ||
			n.Slice != nil && (readsValues(n.Slice.Offset) || readsValues(n.Slice.Length)) {
			p.arith = true
		}
		if n.Exp != nil && n.Exp.Op == syntax.OtherParamOps && n.Exp.Word != nil &&
			strings.Contains(n.Exp.Word.Lit(), "P") {
			p.opaque("prompt expansions run command substitutions")
		}
		if n.Exp != nil && (n.Exp.Op == syntax.AssignUnset || n.Exp.Op == syntax.AssignUnsetOrNull) &&
			n.Param != nil {
			if p.dangerous(n.Param.Value) {
				p.opaque("it sets " + n.Param.Value)
			}
			if p.builtins().integers[n.Param.Value] {
				p.arith = true
			}
		}
		switch {
		case n.Flags != nil:
			p.opaque("zsh parameter flags can run code")
		case n.GlobSubst == syntax.OptOn:
			p.opaque("${~…} globs values, whose qualifiers can run code")
		}
	case *syntax.ArithmExp:
		p.arith = p.arith || readsValues(n.X)
	case *syntax.ArithmCmd:
		p.arith = p.arith || readsValues(n.X)
	case *syntax.LetClause:
		p.arith = p.arith || slices.ContainsFunc(n.Exprs, readsValues)
	case *syntax.CStyleLoop:
		p.arith = p.arith || readsValues(n.Init) || readsValues(n.Cond) || readsValues(n.Post)
	case *syntax.BinaryTest:
		if numericTests[n.Op] {
			p.arith = p.arith || !isNumberTest(n.X) || !isNumberTest(n.Y)
		}
	case *syntax.UnaryTest:
		switch n.Op {
		case syntax.TsFdTerm:
			p.arith = p.arith || !isNumberTest(n.X)
		case syntax.TsVarSet, syntax.TsRefVar:
			w, ok := n.X.(*syntax.Word)
			p.arith = p.arith || !ok || !isName(wordOf(w, p.lang))
		}
	case *syntax.CmdSubst:
		p.suspicious = true
	case *syntax.Word:
		p.arith = p.arith || oldArithmetic(n.Parts)
	case *syntax.DblQuoted:
		p.arith = p.arith || oldArithmetic(n.Parts)
	case *syntax.Lit:
		p.suspicious = p.suspicious || hidesCommand(n.Value)
		if p.zsh() && p.inGlobbedWord() && hasUnescapedParen(n.Value) {
			p.opaque("zsh glob qualifiers can run code")
		}
		if _, ok := unquotedLiteral(n.Value, p.zsh(), false); !ok && p.keepsFileNames() {
			p.suspicious = true
		}
	case *syntax.SglQuoted:
		p.suspicious = p.suspicious || hidesCommand(n.Value)
	}
}

// keepsFileNames reports whether the literal visited is part of a word
// whose file names the script keeps where its arithmetic may evaluate
// them: a variable, or the positional parameters of the script or of a
// function.
func (p *planner) keepsFileNames() bool {
	word, ok := p.nodes[len(p.nodes)-1].(*syntax.Word)
	if !ok || len(p.nodes) < 2 {
		return false
	}
	switch parent := p.nodes[len(p.nodes)-2].(type) {
	case *syntax.Assign, *syntax.ArrayElem, *syntax.WordIter:
		return true
	case *syntax.CallExpr:
		if len(parent.Args) == 0 || parent.Args[0] == word {
			return false
		}
		name := wordOf(parent.Args[0], p.lang)
		return !name.lit || name.text == "set" || p.funcs[name.text]
	}
	return false
}

// declares reports whether the literal operand of a declaration
// evaluates arithmetic: an option giving an integer or float attribute,
// which later assignments evaluate, or a name with a subscript or of a
// special integer parameter.
func (p *planner) declares(operand string) bool {
	if strings.HasPrefix(operand, "-") || strings.HasPrefix(operand, "+") {
		attrs := "i"
		if p.zsh() {
			attrs = "iEF"
		}
		return strings.ContainsAny(operand[1:], attrs)
	}
	name, _, _ := strings.Cut(operand, "=")
	return strings.Contains(name, "[") || p.builtins().integers[name]
}

// numericTests are the conditions that compare their operands as
// arithmetic.
var numericTests = map[syntax.BinTestOperator]bool{
	syntax.TsEql: true, syntax.TsNeq: true, syntax.TsLeq: true,
	syntax.TsGeq: true, syntax.TsLss: true, syntax.TsGtr: true,
}

// readsValues reports whether evaluating the arithmetic x may evaluate
// a value not known to be a number: whether x names a variable or
// expands anything but a number.
func readsValues(x syntax.ArithmExpr) bool {
	switch x := x.(type) {
	case nil:
		return false
	case *syntax.BinaryArithm:
		return readsValues(x.X) || readsValues(x.Y)
	case *syntax.UnaryArithm:
		return readsValues(x.X)
	case *syntax.ParenArithm:
		return readsValues(x.X)
	case *syntax.Word:
		return !isNumber(x)
	}
	return true
}

func isNumberTest(x syntax.TestExpr) bool {
	w, ok := x.(*syntax.Word)
	return ok && isNumber(w)
}

// numberLiteral matches the integers and floats of shell arithmetic, in
// any base.
var numberLiteral = regexp.MustCompile(
	`^([0-9]+(\.[0-9]*)?([eE][-+]?[0-9]+)?|0[xX][0-9a-fA-F]+|[0-9]+#[0-9a-zA-Z_@]+)$`)

// isNumber reports whether w expands to a number: a numeric literal, or
// a special parameter, length or set test that holds one.
func isNumber(w *syntax.Word) bool {
	if len(w.Parts) != 1 {
		return false
	}
	part := w.Parts[0]
	if dq, ok := part.(*syntax.DblQuoted); ok && len(dq.Parts) == 1 {
		part = dq.Parts[0]
	}
	switch part := part.(type) {
	case *syntax.Lit:
		return numberLiteral.MatchString(part.Value)
	case *syntax.ParamExp:
		if part.Param == nil || part.Flags != nil || part.Excl || part.Index != nil ||
			part.Slice != nil || part.Repl != nil || part.Exp != nil ||
			len(part.Modifiers) > 0 || part.NestedParam != nil {
			return false
		}
		return part.Length || part.IsSet || slices.Contains([]string{"#", "?", "$", "!"}, part.Param.Value)
	}
	return false
}

// isNumberWord reports whether w is a number, as isNumber does.
func isNumberWord(w shellWord) bool {
	if w.word != nil {
		return isNumber(w.word)
	}
	return w.lit && numberLiteral.MatchString(w.text)
}

// isName reports whether w names a variable without a subscript.
func isName(w shellWord) bool {
	return w.lit && w.text != "" && assignedName(w.text) == w.text
}

// inGlobbedWord reports whether the node visited is part of a word that
// is not quoted, a here-document, a condition's operand nor a case
// pattern, from which zsh may generate file names.
func (p *planner) inGlobbedWord() bool {
	word, ok := p.nodes[len(p.nodes)-1].(*syntax.Word)
	if !ok {
		return false
	}
	switch parent := p.nodes[len(p.nodes)-2].(type) {
	case *syntax.Redirect:
		return parent.Hdoc != word
	case *syntax.TestClause, *syntax.BinaryTest, *syntax.UnaryTest, *syntax.ParenTest,
		*syntax.CaseItem:
		return false
	}
	return true
}

// readsFile reports whether the statement s visited is the file of a
// $(<file) substitution, whose contents zsh reads without running a
// command: the only statement, of a single input redirection and no
// assignments.
func (p *planner) readsFile(s *syntax.Stmt) bool {
	cs, ok := p.nodes[len(p.nodes)-1].(*syntax.CmdSubst)
	if !ok || len(cs.Stmts) != 1 || len(s.Redirs) != 1 || s.Negated || s.Background ||
		s.Coprocess {
		return false
	}
	if call, ok := s.Cmd.(*syntax.CallExpr); ok && len(call.Assigns) > 0 {
		return false
	}
	r := s.Redirs[0]
	return r.Op == syntax.RdrIn && (r.N == nil || r.N.Value == "0")
}

// hasUnescapedParen reports whether the unquoted literal v holds a
// parenthesis no backslash escapes, which in zsh opens a glob group or
// qualifier.
func hasUnescapedParen(v string) bool {
	for i := 0; i < len(v); i++ {
		switch v[i] {
		case '\\':
			i++
		case '(':
			return true
		}
	}
	return false
}

func hidesCommand(s string) bool {
	return strings.Contains(s, "$(") || strings.Contains(s, "`")
}

// oldArithmetic reports whether parts hold the $[…] arithmetic expansion
// of zsh, which the parser takes for a literal "$" and a literal "[…".
func oldArithmetic(parts []syntax.WordPart) bool {
	for i := 0; i+1 < len(parts); i++ {
		l, ok := parts[i].(*syntax.Lit)
		next, nextOk := parts[i+1].(*syntax.Lit)
		if !ok || !nextOk || !strings.HasPrefix(next.Value, "[") ||
			!strings.HasSuffix(l.Value, "$") {
			continue
		}
		escapes := len(l.Value) - 1 - len(strings.TrimRight(l.Value[:len(l.Value)-1], `\`))
		if escapes%2 == 0 {
			return true
		}
	}
	return false
}

// runsFirst reports whether the program of s runs before anything else:
// a simple command whose words, assignments and redirections run no
// command substitution first. lang is the language of its script.
func runsFirst(s *syntax.Stmt, lang syntax.LangVariant) bool {
	call, ok := s.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) > 0 {
		return false
	}
	for _, w := range call.Args {
		if !wordOf(w, lang).lit {
			return false
		}
	}
	for _, r := range s.Redirs {
		if (r.Word != nil && !wordOf(r.Word, lang).lit) ||
			(r.Hdoc != nil && !wordOf(r.Hdoc, lang).lit) {
			return false
		}
	}
	return true
}

// withPlaceholders returns words with those that hold one of
// placeholders made not literal, since what replaces it is only known
// when the command runs.
func withPlaceholders(words []shellWord, placeholders []string) []shellWord {
	ret := slices.Clone(words)
	for i, w := range ret {
		if slices.ContainsFunc(placeholders, func(ph string) bool {
			return strings.Contains(w.text, ph)
		}) {
			ret[i].lit, ret[i].word = false, nil
		}
	}
	return ret
}

// isOption reports whether w is one of the options names, with or without
// an attached value.
func isOption(w shellWord, names []string) bool {
	return w.lit && slices.ContainsFunc(names, func(n string) bool {
		return w.text == n || strings.HasPrefix(w.text, n+"=")
	})
}

// wordOf returns the value of w when the shell would pass it on as
// written, after quote removal, and its source otherwise: expansions,
// globs and brace expansions make a word not literal. lang is the
// language of its script.
func wordOf(w *syntax.Word, lang syntax.LangVariant) shellWord {
	var b strings.Builder
	lit := !oldArithmetic(w.Parts)
	for i, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			v, ok := unquotedLiteral(p.Value, lang == syntax.LangZsh, i == 0)
			lit = lit && ok
			b.WriteString(v)
		case *syntax.SglQuoted:
			lit = lit && !(p.Dollar && strings.Contains(p.Value, `\`))
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			lit = lit && !p.Dollar && !oldArithmetic(p.Parts)
			for _, inner := range p.Parts {
				l, ok := inner.(*syntax.Lit)
				if !ok {
					lit = false
					break
				}
				b.WriteString(doubleQuotedLiteral(l.Value))
			}
		default:
			lit = false
		}
		if !lit {
			break
		}
	}
	if lit {
		return shellWord{text: b.String(), lit: true, word: w}
	}
	var src strings.Builder
	if err := syntax.NewPrinter().Print(&src, w); err != nil {
		return shellWord{text: "?"}
	}
	return shellWord{text: src.String(), word: w}
}

// unquotedLiteral removes the backslashes of an unquoted literal, and
// reports false when it holds a glob or a brace expansion. zsh reports
// that the literal is zsh's, whose extended globs and equals expansions
// make it not literal too, as do its tildes, which expand to directories
// the script can change, and start that it starts its word.
func unquotedLiteral(v string, zsh, start bool) (string, bool) {
	var b strings.Builder
	ok := true
	braceOpen, braceList := -1, false
	bracket := false
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch c {
		case '\\':
			if i+1 < len(v) {
				i++
				if v[i] != '\n' {
					b.WriteByte(v[i])
				}
			}
			continue
		case '*', '?':
			ok = false
		case '[':
			bracket = true
		case ']':
			if bracket {
				ok = false
			}
		case '{':
			braceOpen, braceList = i, false
		case ',':
			braceList = braceList || braceOpen >= 0
		case '.':
			braceList = braceList || (braceOpen >= 0 && i+1 < len(v) && v[i+1] == '.')
		case '}':
			// zsh may expand {ab} into a and b (BRACE_CCL), but not {}.
			if braceOpen >= 0 && (braceList || zsh && i > braceOpen+1) {
				ok = false
			}
			braceOpen = -1
		case '^', '#', '<', '>', '(', ')', '|', '~':
			ok = ok && !zsh
		case '=':
			ok = ok && !(zsh && i == 0 && start)
		}
		b.WriteByte(c)
	}
	return b.String(), ok
}

// doubleQuotedLiteral removes the backslashes that escape in double
// quotes.
func doubleQuotedLiteral(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) && strings.IndexByte("$`\"\\\n", v[i+1]) >= 0 {
			i++
			if v[i] != '\n' {
				b.WriteByte(v[i])
			}
			continue
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// assign follows the variables the statement s of a script sets.
func (p *planner) assign(s *syntax.Stmt) {
	var assigns []*syntax.Assign
	switch c := s.Cmd.(type) {
	case *syntax.CallExpr:
		// Assignments before a program are its environment only.
		if len(c.Args) == 0 {
			assigns = c.Assigns
		}
	case *syntax.DeclClause:
		assigns = c.Args
	}
	for _, a := range assigns {
		if a.Name == nil || a.Naked {
			continue
		}
		p.forget(a.Name.Value)
		// expand follows bash, which zsh expands values unlike.
		if !p.straight[s] || a.Append || a.Index != nil || a.Array != nil || p.zsh() {
			continue
		}
		v := ""
		if a.Value != nil {
			env := newScriptEnv(p, a.Value)
			var err error
			if v, err = expand.Literal(&expand.Config{Env: env}, a.Value); err != nil || env.missed {
				continue
			}
		}
		p.vars[a.Name.Value] = &v
	}
}

// forget records that the variable name is not known until the command
// runs.
func (p *planner) forget(name string) {
	if p.vars == nil {
		p.vars = map[string]*string{}
	}
	p.vars[name] = nil
}

// inFunc reports whether the node visited is in a function, which runs
// when it is called rather than where it is declared.
func (p *planner) inFunc() bool {
	return slices.ContainsFunc(p.nodes, func(n syntax.Node) bool {
		_, ok := n.(*syntax.FuncDecl)
		return ok
	})
}

// recursiveDelete adds the command name args to the plan when it deletes
// directories, with the words the authorizer can tell expanded.
func (p *planner) recursiveDelete(name string, args []shellWord, c callContext) {
	if !deletesDirectories(name, args) {
		return
	}
	var d RecursiveDelete
	fields := []string{name}
	for _, w := range args {
		f, ok := p.fields(w)
		if !ok {
			d.Unresolved = true
			fields = append(fields, w.text)
			continue
		}
		for _, s := range f {
			fields = append(fields, displayField(s))
		}
	}
	if c.appended && !d.Unresolved {
		d.Unresolved = true
		fields = append(fields, "…")
	}
	d.Command = strings.Join(fields, " ")
	p.plan.Deletes = append(p.plan.Deletes, d)
}

// deletesDirectories reports whether the program name deletes directories
// when it runs with args.
func deletesDirectories(name string, args []shellWord) bool {
	switch filepath.Base(name) {
	case "rmdir":
		return true
	case "rm":
		return recursiveRM(args)
	}
	return false
}

// recursiveRM reports whether the arguments of rm make it delete
// directories.
func recursiveRM(args []shellWord) bool {
	for _, w := range args {
		switch {
		case !w.lit:
		case w.text == "--":
			return false
		case w.text == "--recursive":
			return true
		case strings.HasPrefix(w.text, "--"):
		case strings.HasPrefix(w.text, "-") && strings.ContainsAny(w.text[1:], "rR"):
			return true
		}
	}
	return false
}

// fields returns the arguments w expands to, and false when they are not
// known until the command runs. Globs are left unexpanded. Words of zsh
// are not expanded, since expand follows bash, which splits them.
func (p *planner) fields(w shellWord) ([]string, bool) {
	if w.word == nil || p.zsh() {
		return []string{w.text}, w.lit
	}
	env := newScriptEnv(p, w.word)
	f, err := expand.Fields(&expand.Config{Env: env}, w.word)
	if err != nil || env.missed {
		return nil, false
	}
	return f, true
}

// displayField quotes s when a shell would read it as more or less than
// one word, leaving globs as they are.
func displayField(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`;&|<>()") {
		return s
	}
	q, err := syntax.Quote(s, syntax.LangBash)
	if err != nil {
		return s
	}
	return q
}

// scriptEnv is what the planner expands the words of scripts with: the
// variables the script set, then those the command starts with. missed
// reports that the expansion read one that is not known until it runs.
type scriptEnv struct {
	p *planner
	// refs are the variables the word expands, as opposed to those every
	// expansion reads.
	refs   map[string]bool
	missed bool
}

func newScriptEnv(p *planner, w *syntax.Word) *scriptEnv {
	e := &scriptEnv{p: p, refs: map[string]bool{}}
	syntax.Walk(w, func(n syntax.Node) bool {
		if pe, ok := n.(*syntax.ParamExp); ok && pe.Param != nil {
			e.refs[pe.Param.Value] = true
		}
		return true
	})
	return e
}

func (e *scriptEnv) Get(name string) expand.Variable {
	if e.p.inFunc() || !syntax.ValidName(name) {
		e.missed = true
		return expand.Variable{}
	}
	if v, ok := e.p.vars[name]; ok {
		if v == nil {
			e.missed = true
			return expand.Variable{}
		}
		return expand.Variable{Set: true, Kind: expand.String, Str: *v}
	}
	if (name == "IFS" || name == "PWD") && !e.refs[name] {
		return expand.Variable{}
	}
	// The shell sets PWD to its own directory, not the IDE's.
	if e.p.env == nil || name == "PWD" {
		e.missed = true
		return expand.Variable{}
	}
	v, set, known := e.p.env(name)
	e.missed = e.missed || !known
	if !set {
		return expand.Variable{}
	}
	return expand.Variable{Set: true, Exported: true, Kind: expand.String, Str: v}
}

func (e *scriptEnv) Each(func(name string, vr expand.Variable) bool) {}
