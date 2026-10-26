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
	"path/filepath"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// maxScriptDepth bounds how deep shells nested in scripts are followed.
const maxScriptDepth = 8

// shellWord is a word of a command. text is its value after quote
// removal when lit, and its source otherwise.
type shellWord struct {
	text string
	lit  bool
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
}

type planner struct {
	plan  commandPlan
	funcs map[string]bool
	depth int
	// arith reports a context that evaluates arithmetic, which runs the
	// command substitutions of array subscripts in variable values.
	arith bool
	// suspicious reports a string holding a command substitution.
	suspicious bool
	// dir is the Dir of the programs added.
	dir string
	// firstStmt is the statement of the script being planned whose
	// program runs first, or nil.
	firstStmt *syntax.Stmt
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
// the variables the command sets to values other than the IDE's own.
func planCommand(path string, args []string, env []string) commandPlan {
	p := &planner{funcs: map[string]bool{}}
	if path == "" {
		p.opaque("it runs the login shell")
		return p.plan
	}
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		// The authorizer looks programs up in the PATH a command sets.
		if name != "PATH" && dangerousEnv(name) {
			p.opaque("it sets " + name)
			return p.plan
		}
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
	if shellOpaqueBuiltins[name] {
		p.opaque(name + " runs code that cannot be followed")
		return true
	}
	if shellDirBuiltins[name] {
		p.plan.DirChanged = true
	}
	if shellArithBuiltins[name] {
		p.arith = true
	}
	if shellAssigningBuiltins[name] {
		p.checkAssigned(name, args)
	}
	if name == "printf" {
		opts, _, _ := optionSpec{short: "v:"}.scan(args)
		if o, ok := hasOption(opts, []string{"-v"}); ok {
			p.checkName(o.value)
		}
	}
	if shellNoProgram[name] {
		return true
	}
	if !shellBuiltinPrograms[name] {
		return false
	}
	inv := p.invocation(name, args, c)
	inv.Shell = c.shell
	inv.TopLevel = false
	p.add(inv)
	return true
}

var declareOptions = optionSpec{short: "aAfFgiIlnprtux", plus: true}

// assigningBuiltinOptions are the options of shellAssigningBuiltins.
var assigningBuiltinOptions = map[string]optionSpec{
	"read":      {short: "ersa:d:i:n:N:p:t:u:"},
	"mapfile":   {short: "td:n:O:s:u:C:c:"},
	"readarray": {short: "td:n:O:s:u:C:c:"},
	"getopts":   {},
	"unset":     {short: "fvn"},
	"export":    {short: "fnp"},
	"declare":   declareOptions,
	"local":     declareOptions,
	"typeset":   declareOptions,
	"readonly":  declareOptions,
}

// checkAssigned makes the plan opaque when the builtin name sets a
// variable that changes what programs run, or one it cannot tell.
func (p *planner) checkAssigned(name string, args []shellWord) {
	opts, operand, ok := assigningBuiltinOptions[name].scan(args)
	if !ok {
		p.opaque("the variables " + name + " sets are not known until it runs")
		return
	}
	for _, o := range opts {
		switch {
		case o.name == "-C":
			p.opaque(name + " -C runs code that cannot be followed")
			return
		case o.name == "-n" && name != "unset" && name != "export" && name != "read":
			p.opaque("namerefs hide the variables they set")
			return
		case o.name == "-a" && name == "read":
			p.checkName(o.value)
		case o.name == "-f" && (name == "unset" || name == "export"):
			// Functions, not variables.
			return
		}
	}
	names := args[operand:]
	if name == "getopts" {
		if len(names) < 2 {
			return
		}
		names = names[1:2]
	}
	for _, w := range names {
		p.checkName(w.text)
	}
}

func (p *planner) checkName(text string) {
	n := assignedName(text)
	if n == "" {
		p.opaque("it sets a variable that is not known until it runs")
		return
	}
	if dangerousEnv(n) {
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
		if dangerousEnv(n) {
			p.opaque("it sets " + n)
			return nil
		}
		rest = rest[1:]
	}
	return rest
}

func (p *planner) checkEnvOptions(opts []parsedOption, spec programSpec) {
	for _, o := range opts {
		if !slices.Contains(spec.envOpts, o.name) {
			continue
		}
		if n, _, _ := strings.Cut(o.value, "="); dangerousEnv(n) {
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
	if spec.builtin && name != "exec" {
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
	probe := &planner{funcs: p.funcs, depth: p.depth, dir: p.dir}
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
	p.funcs = map[string]bool{}
	p.firstStmt = nil
	if first && len(file.Stmts) > 0 && runsFirst(file.Stmts[0]) {
		p.firstStmt = file.Stmts[0]
	}
	p.depth++
	defer func() {
		p.funcs, p.firstStmt = outer, outerFirst
		p.depth--
	}()
	syntax.Walk(file, func(n syntax.Node) bool {
		if fd, ok := n.(*syntax.FuncDecl); ok && fd.Name != nil {
			p.funcs[fd.Name.Value] = true
		}
		return true
	})
	syntax.Walk(file, func(n syntax.Node) bool {
		p.visit(n, shell)
		return !p.plan.Opaque
	})
}

func (p *planner) visit(n syntax.Node, shell int) {
	switch n := n.(type) {
	case *syntax.Stmt:
		call, ok := n.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return
		}
		words := make([]shellWord, len(call.Args))
		for i, w := range call.Args {
			words[i] = wordOf(w)
		}
		p.expand(words, callContext{shell: shell, first: n == p.firstStmt})
	case *syntax.Assign:
		if n.Name != nil && dangerousEnv(n.Name.Value) {
			p.opaque("it sets " + n.Name.Value)
		}
	case *syntax.DeclClause:
		p.arith = true
		if n.Variant != nil && n.Variant.Value == "nameref" {
			p.opaque("namerefs hide the variables they set")
			return
		}
		for _, a := range n.Args {
			if !a.Naked || a.Name != nil || a.Value == nil {
				continue
			}
			w := wordOf(a.Value)
			switch {
			case !w.lit:
				p.opaque("it sets a variable that is not known until it runs")
			case strings.HasPrefix(w.text, "-") && strings.Contains(w.text, "n"):
				p.opaque("namerefs hide the variables they set")
			}
		}
	case *syntax.WordIter:
		if n.Name != nil && dangerousEnv(n.Name.Value) {
			p.opaque("it sets " + n.Name.Value)
		}
	case *syntax.CoprocClause:
		if n.Name != nil {
			if w := wordOf(n.Name); !w.lit || dangerousEnv(w.text) {
				p.opaque("it sets " + w.text)
			}
		}
	case *syntax.Redirect:
		if n.N != nil && dangerousEnv(strings.Trim(n.N.Value, "{}")) {
			p.opaque("it sets " + strings.Trim(n.N.Value, "{}"))
		}
	case *syntax.ParamExp:
		if n.Excl || n.Index != nil || n.Slice != nil {
			p.arith = true
		}
		if n.Exp != nil && n.Exp.Op == syntax.OtherParamOps && n.Exp.Word != nil &&
			strings.Contains(n.Exp.Word.Lit(), "P") {
			p.opaque("prompt expansions run command substitutions")
		}
	case *syntax.ArithmExp, *syntax.ArithmCmd, *syntax.LetClause, *syntax.TestClause:
		p.arith = true
	case *syntax.Lit:
		p.suspicious = p.suspicious || hidesCommand(n.Value)
	case *syntax.SglQuoted:
		p.suspicious = p.suspicious || hidesCommand(n.Value)
	}
}

func hidesCommand(s string) bool {
	return strings.Contains(s, "$(") || strings.Contains(s, "`")
}

// runsFirst reports whether the program of s runs before anything else:
// a simple command whose words, assignments and redirections run no
// command substitution first.
func runsFirst(s *syntax.Stmt) bool {
	call, ok := s.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || len(call.Assigns) > 0 {
		return false
	}
	for _, w := range call.Args {
		if !wordOf(w).lit {
			return false
		}
	}
	for _, r := range s.Redirs {
		if (r.Word != nil && !wordOf(r.Word).lit) || (r.Hdoc != nil && !wordOf(r.Hdoc).lit) {
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
		ret[i].lit = w.lit && !slices.ContainsFunc(placeholders, func(ph string) bool {
			return strings.Contains(w.text, ph)
		})
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
// globs and brace expansions make a word not literal.
func wordOf(w *syntax.Word) shellWord {
	var b strings.Builder
	lit := true
	for _, part := range w.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			v, ok := unquotedLiteral(p.Value)
			lit = lit && ok
			b.WriteString(v)
		case *syntax.SglQuoted:
			lit = lit && !(p.Dollar && strings.Contains(p.Value, `\`))
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			lit = lit && !p.Dollar
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
		return shellWord{text: b.String(), lit: true}
	}
	var src strings.Builder
	if err := syntax.NewPrinter().Print(&src, w); err != nil {
		return shellWord{text: "?"}
	}
	return shellWord{text: src.String()}
}

// unquotedLiteral removes the backslashes of an unquoted literal, and
// reports false when it holds a glob or a brace expansion.
func unquotedLiteral(v string) (string, bool) {
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
			if braceOpen >= 0 && braceList {
				ok = false
			}
			braceOpen = -1
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
