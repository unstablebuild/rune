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

// Package toolpolicy restricts the tools, shell commands, skills and
// sub-agents an agent may use, as the allowed-tools and disallowed-tools of
// a skill describe them. A policy only ever takes capabilities away: it
// approves nothing, so whatever it allows still goes through the usual
// permission checks.
package toolpolicy

import (
	"errors"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
	"unstable.build/rune/internal/term/shellplan"
)

// shellTools are the tools that run shell commands, which a "bash" entry
// stands for.
var shellTools = []string{"bash", "exec_command", "run_command"}

// harmlessBuiltins are the shell builtins that run no other program and
// change nothing outside the shell, so that they need no allow rule.
var harmlessBuiltins = map[string]bool{
	"echo": true, "printf": true, "print": true, "test": true, "[": true, "pwd": true,
	"read": true, "mapfile": true, "readarray": true, "type": true, "whence": true,
	"where": true, "which": true,
}

// writableFiles are the files redirections may write to when commands are
// restricted.
var writableFiles = map[string]bool{"/dev/null": true, "/dev/stdout": true, "/dev/stderr": true}

// Policy restricts the tools of an agent. The zero Policy allows
// everything. Combined policies allow only what every layer allows.
type Policy struct {
	layers []layer
}

// layer is the policy of one allowed-tools and disallowed-tools pair.
type layer struct {
	// allowed reports that the layer has an allow list, so that it only
	// allows what the list does.
	allowed    bool
	allowNames []string
	allowCmds  []shellplan.Rule
	// allowScripts are the skill name patterns of bash($name) entries,
	// which WithSkillDirs resolves to scriptDirs.
	allowScripts []string
	allowSkills  []string
	allowAgents  []string
	denyNames    []string
	denyCmds     []shellplan.Rule
	denySkills   []string
	denyAgents   []string
	scriptDirs   []string
}

// List is a list of allowed-tools or disallowed-tools entries. It unmarshals
// from a YAML sequence of entries, or from a string of entries that
// SplitList splits. An empty string or sequence gives an empty, non-nil
// List, so that Parse tells it from a missing one; YAML decoders leave a
// null value nil.
type List []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (l *List) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*l = append(List{}, SplitList(n.Value)...)
		return nil
	case yaml.SequenceNode:
		var entries []string
		if err := n.Decode(&entries); err != nil {
			return err
		}
		*l = append(List{}, entries...)
		return nil
	}
	return fmt.Errorf("line %d: expected a string or a list of strings", n.Line)
}

// SplitList splits s into entries separated by whitespace or commas that
// are not inside parentheses, so that "bash(git log *), read_file" holds
// two entries. Inside parentheses, shell quotes and backslashes also
// hide parentheses, as in "bash(echo ')')". Text whose parentheses or
// quotes are not closed runs to the end of s.
func SplitList(s string) []string {
	var ret []string
	depth, start := 0, -1
	var quote rune
	escaped := false
	for i, r := range s {
		switch {
		case escaped:
			escaped = false
		case quote != 0:
			if r == quote {
				quote = 0
			} else if r == '\\' && quote == '"' {
				escaped = true
			}
		case depth > 0 && (r == '\'' || r == '"'):
			quote = r
		case depth > 0 && r == '\\':
			escaped = true
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0 && (r == ',' || unicode.IsSpace(r)):
			if start >= 0 {
				ret = append(ret, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		ret = append(ret, s[start:])
	}
	return ret
}

// Parse returns the policy of the allowed and denied entries. Each entry
// is a tool name, which may be a glob ("github_*"), or one of bash, skill
// and agent with a pattern in parentheses: "bash(git log *)",
// "skill(acme-*)", "agent(explore)". "bash" stands for every tool that
// runs shell commands, and its name is case-insensitive, as are those of
// skill and agent. Skill name patterns are case-sensitive and sub-agent
// type patterns are not, as the skill and agent tools look them up. An
// allowed "skill(*)" or "agent(*)" is the same as "skill" or "agent".
//
// An allowed "bash($name)" entry lets commands run the scripts of the
// skills name matches, a glob on skill names, once WithSkillDirs
// resolves it. Denied entries cannot take that form.
//
// A nil allowed means there is no allow list: the policy allows every
// tool that denied entries don't name. A non-nil allowed allows only what
// it lists, so nothing at all when it is empty. Blank entries are
// ignored, and a nil allowed with no denied entries gives the zero
// Policy. An entry that cannot be parsed, or a name pattern holding
// characters no name does, such as spaces, slashes or quotes, is an error
// that names the entry.
func Parse(allowed, denied []string) (Policy, error) {
	l := layer{allowed: allowed != nil}
	for _, e := range allowed {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if err := l.add(e, false); err != nil {
			return Policy{}, err
		}
	}
	empty := !l.allowed
	for _, e := range denied {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		empty = false
		if err := l.add(e, true); err != nil {
			return Policy{}, err
		}
	}
	if empty {
		return Policy{}, nil
	}
	return Policy{layers: []layer{l}}, nil
}

func (l *layer) add(entry string, deny bool) error {
	name, pattern, patterned, err := splitEntry(entry)
	if err != nil {
		return err
	}
	if !patterned {
		if err := checkNamePattern(entry, name, "tool name"); err != nil {
			return err
		}
		if deny {
			l.denyNames = append(l.denyNames, name)
		} else {
			l.allowNames = append(l.allowNames, name)
		}
		return nil
	}
	tool := strings.ToLower(name)
	if (tool == "skill" || tool == "agent") && pattern == "*" && !deny {
		l.allowNames = append(l.allowNames, name)
		return nil
	}
	switch tool {
	case "bash":
		if skill, ok := strings.CutPrefix(pattern, "$"); ok {
			if deny {
				return fmt.Errorf("tool entry %q: the scripts of a skill can only be allowed", entry)
			}
			if skill == "" || checkNamePattern(entry, skill, "skill name") != nil {
				return fmt.Errorf("tool entry %q: write $ and a skill name, as in bash($acme-notes)", entry)
			}
			l.allowScripts = append(l.allowScripts, skill)
			return nil
		}
		r, err := shellplan.ParseRule(pattern)
		if err != nil {
			return fmt.Errorf("tool entry %q: %w", entry, err)
		}
		if deny {
			l.denyCmds = append(l.denyCmds, r)
		} else {
			l.allowCmds = append(l.allowCmds, r)
		}
	case "skill":
		if err := checkNamePattern(entry, pattern, "skill name"); err != nil {
			return err
		}
		if deny {
			l.denySkills = append(l.denySkills, pattern)
		} else {
			l.allowSkills = append(l.allowSkills, pattern)
		}
	case "agent":
		pattern = strings.ToLower(pattern)
		if err := checkNamePattern(entry, pattern, "sub-agent type"); err != nil {
			return err
		}
		if deny {
			l.denyAgents = append(l.denyAgents, pattern)
		} else {
			l.allowAgents = append(l.allowAgents, pattern)
		}
	default:
		return fmt.Errorf("tool entry %q: only bash, skill and agent take a pattern", entry)
	}
	return nil
}

// badNameRunes are characters no tool, skill or sub-agent name holds, so
// that a name pattern holding one is a mistake, such as two entries in
// one or a quoted name.
const badNameRunes = `/\$(){},;|&<>'"` + "`"

// checkNamePattern returns an error, naming entry, unless pattern is a
// glob on the names of kind.
func checkNamePattern(entry, pattern, kind string) error {
	if _, err := path.Match(pattern, ""); err != nil {
		return fmt.Errorf("tool entry %q: %w", entry, err)
	}
	if strings.ContainsAny(pattern, badNameRunes) || strings.ContainsFunc(pattern, unicode.IsSpace) {
		return fmt.Errorf("tool entry %q: %q is not a %s pattern", entry, pattern, kind)
	}
	return nil
}

// splitEntry splits "name(pattern)" into its name and pattern, the text
// up to the last parenthesis, and reports whether the entry has a
// pattern. Parentheses inside the pattern are left for the pattern's
// own parser to judge.
func splitEntry(entry string) (name, pattern string, patterned bool, err error) {
	open := strings.IndexByte(entry, '(')
	if open < 0 {
		if strings.ContainsRune(entry, ')') {
			return "", "", false, fmt.Errorf("tool entry %q has unbalanced parentheses", entry)
		}
		return entry, "", false, nil
	}
	if !strings.HasSuffix(entry, ")") {
		return "", "", false, fmt.Errorf("tool entry %q has unbalanced parentheses", entry)
	}
	name = strings.TrimSpace(entry[:open])
	pattern = strings.TrimSpace(entry[open+1 : len(entry)-1])
	if name == "" {
		return "", "", false, fmt.Errorf("tool entry %q has no tool name; write it right before "+
			"the parenthesis, as in bash(git log *)", entry)
	}
	if pattern == "" {
		return "", "", false, fmt.Errorf("tool entry %q has an empty pattern", entry)
	}
	return name, pattern, true, nil
}

// Intersect returns the policy that allows only what every one of ps
// allows.
func Intersect(ps ...Policy) Policy {
	var ret Policy
	for _, p := range ps {
		ret.layers = append(ret.layers, p.layers...)
	}
	return ret
}

// WithSkillDirs returns p with its bash($name) entries resolved against
// skillDirs, which maps the name of each installed skill to its
// directory. Each entry then lets commands run the programs in the
// scripts directory of the skills it matches, given by an absolute path
// inside it. An entry that matches no skill allows nothing, and skill
// directories that are not absolute paths are ignored.
func (p Policy) WithSkillDirs(skillDirs map[string]string) Policy {
	ret := Policy{layers: slices.Clone(p.layers)}
	names := slices.Sorted(maps.Keys(skillDirs))
	for i := range ret.layers {
		l := &ret.layers[i]
		l.scriptDirs = nil
		for _, name := range names {
			dir := skillDirs[name]
			matches := slices.ContainsFunc(l.allowScripts, func(pattern string) bool {
				ok, _ := path.Match(pattern, name)
				return ok
			})
			if matches && path.IsAbs(dir) {
				l.scriptDirs = append(l.scriptDirs, path.Join(dir, "scripts"))
			}
		}
	}
	return ret
}

// WithoutSkill returns p also keeping the skill tool from running the
// skill name and the agent tool from spawning it. name is compared as a
// name, not as a pattern, whatever characters it holds.
func (p Policy) WithoutSkill(name string) Policy {
	return Intersect(p, Policy{layers: []layer{{
		denySkills: []string{quoteGlob(name)},
		denyAgents: []string{quoteGlob(strings.ToLower(name))},
	}}})
}

// quoteGlob returns the path.Match pattern that matches s only.
func quoteGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// IsZero reports whether p allows everything.
func (p Policy) IsZero() bool {
	return len(p.layers) == 0
}

// AllowsTool reports whether p allows the tool name.
func (p Policy) AllowsTool(name string) bool {
	return !slices.ContainsFunc(p.layers, func(l layer) bool { return !l.allowsTool(name) })
}

// NamesTool reports whether p allows the tool name and some allow list
// names it exactly, rather than by a glob or by having no allow list.
func (p Policy) NamesTool(name string) bool {
	return p.AllowsTool(name) && slices.ContainsFunc(p.layers, func(l layer) bool {
		return slices.Contains(l.allowNames, name)
	})
}

// AllowsSkill reports whether p lets the skill tool show and run the
// skill name. It does not tell whether p allows the skill tool itself.
func (p Policy) AllowsSkill(name string) bool {
	return !slices.ContainsFunc(p.layers, func(l layer) bool {
		return !l.allows(l.allowSkills, l.denySkills, "skill", name)
	})
}

// AllowsAgent reports whether p lets the agent tool spawn the sub-agent
// type id, compared case-insensitively. It does not tell whether p allows
// the agent tool itself.
func (p Policy) AllowsAgent(id string) bool {
	id = strings.ToLower(id)
	return !slices.ContainsFunc(p.layers, func(l layer) bool {
		return !l.allows(l.allowAgents, l.denyAgents, "agent", id)
	})
}

// ConstrainsAgents reports whether p keeps the agent tool from spawning
// some sub-agent type.
func (p Policy) ConstrainsAgents() bool {
	return slices.ContainsFunc(p.layers, func(l layer) bool {
		return l.allowed && !l.namesAll("agent") || len(l.denyAgents) > 0
	})
}

// CheckCommand returns an error that tells why p does not let a shell
// tool run the command plan is of, or nil when it does.
//
// When p has command patterns, it rejects commands whose programs
// cannot be told, and programs whose name is not known until they run. A
// deny pattern rejects every program it matches, wrappers included,
// though not the shell that runs the command. When p allows commands by
// pattern only, every program the command runs must match an allow
// pattern or be inside the scripts directory of a skill a bash($name)
// entry allows, besides the shells, the wrappers that only run other
// programs and the builtins that run nothing, and redirections may only
// write to /dev/null, /dev/stdout and /dev/stderr.
func (p Policy) CheckCommand(plan shellplan.Plan) error {
	for _, l := range p.layers {
		if err := l.checkCommand(plan); err != nil {
			return err
		}
	}
	return nil
}

// CommandHelp describes the restrictions of p on shell commands for the
// description of a shell tool, or returns "" when there are none.
func (p Policy) CommandHelp() string {
	var sentences []string
	restricted := false
	for _, l := range p.layers {
		lead := "Commands are restricted: every"
		if restricted {
			lead = "Also, every"
		}
		restricted = restricted || l.restrictsCommands()
		sentences = append(sentences, l.restrictions(lead)...)
	}
	if len(sentences) == 0 {
		return ""
	}
	sentences = append(sentences, "Commands whose programs Rune cannot tell, such as eval or "+
		"a program name in a variable, are rejected.")
	if restricted {
		sentences = append(sentences, "Redirections may not write files, except /dev/null.")
	}
	return strings.Join(sentences, " ")
}

// restrictions describes the restrictions of the layer on shell commands,
// starting the sentence on the programs it allows with lead.
func (l layer) restrictions(lead string) []string {
	var sentences []string
	if l.restrictsCommands() {
		var alts []string
		if len(l.allowCmds) > 0 {
			alts = append(alts, "match one of "+formatRules(l.allowCmds))
		}
		if len(l.scriptDirs) > 0 {
			alts = append(alts, "be inside "+formatList(l.scriptDirs)+", given by its absolute path")
		}
		if len(alts) == 0 {
			alts = append(alts, "be a shell builtin")
		}
		sentences = append(sentences, lead+" program a command runs must "+strings.Join(alts, " or ")+".")
	}
	if len(l.denyCmds) > 0 {
		sentences = append(sentences, "Programs matching "+formatRules(l.denyCmds)+" may not run.")
	}
	return sentences
}

// matchesName reports whether the plain entry e names the tool.
func matchesName(e, tool string) bool {
	switch strings.ToLower(e) {
	case "bash":
		return slices.Contains(shellTools, tool)
	case "skill", "agent":
		return strings.EqualFold(e, tool)
	}
	ok, _ := path.Match(e, tool)
	return ok
}

// namesAll reports whether a plain allow entry names the tool, which
// allows it with no pattern restricting it.
func (l layer) namesAll(tool string) bool {
	return slices.ContainsFunc(l.allowNames, func(e string) bool { return matchesName(e, tool) })
}

func (l layer) allowsTool(name string) bool {
	if slices.ContainsFunc(l.denyNames, func(e string) bool { return matchesName(e, name) }) {
		return false
	}
	if !l.allowed || l.namesAll(name) {
		return true
	}
	switch {
	case slices.Contains(shellTools, name):
		return len(l.allowCmds) > 0 || len(l.allowScripts) > 0
	case name == "skill":
		return len(l.allowSkills) > 0
	case name == "agent":
		return len(l.allowAgents) > 0
	}
	return false
}

// allows reports whether the layer lets the tool use name, given the
// patterns of allow and deny entries for it.
func (l layer) allows(allow, deny []string, tool, name string) bool {
	match := func(pattern string) bool {
		ok, _ := path.Match(pattern, name)
		return ok
	}
	if slices.ContainsFunc(deny, match) {
		return false
	}
	return !l.allowed || l.namesAll(tool) || slices.ContainsFunc(allow, match)
}

// restrictsCommands reports whether the layer allows commands by pattern
// only.
func (l layer) restrictsCommands() bool {
	return l.allowed && (len(l.allowCmds) > 0 || len(l.allowScripts) > 0) &&
		!slices.ContainsFunc(shellTools, l.namesAll)
}

func (l layer) checkCommand(plan shellplan.Plan) error {
	if len(l.allowCmds) == 0 && len(l.allowScripts) == 0 && len(l.denyCmds) == 0 {
		return nil
	}
	if plan.Opaque {
		return l.reject("Rune cannot tell the programs the command runs: " + plan.Reason)
	}
	restrict := l.restrictsCommands()
	for _, prog := range plan.Programs {
		if prog.Opaque {
			return l.reject(fmt.Sprintf("the name of the program %s is not known until it runs",
				prog.Name))
		}
		if !prog.TopLevel || prog.Role != shellplan.RoleBound {
			if i := slices.IndexFunc(l.denyCmds, func(r shellplan.Rule) bool { return r.Match(prog) }); i >= 0 {
				return l.reject(fmt.Sprintf("%q matches the denied pattern %q",
					describe(prog), l.denyCmds[i].String()))
			}
		}
		if !restrict || prog.Role == shellplan.RoleBound || l.allowsProgram(plan, prog) {
			continue
		}
		return l.reject(fmt.Sprintf("%q matches no allowed pattern", describe(prog)))
	}
	if !restrict {
		return nil
	}
	for _, w := range plan.Writes {
		if !w.Literal || !writableFiles[w.Target] {
			return l.reject(fmt.Sprintf("it redirects output to the file %s", w.Target))
		}
	}
	return nil
}

// allowsProgram reports whether a program the command plan is of runs
// may run when the layer restricts commands.
func (l layer) allowsProgram(plan shellplan.Plan, prog shellplan.Program) bool {
	if prog.Shell >= 0 && harmlessBuiltins[prog.Name] {
		return true
	}
	// A relative path names another program once the directory changes.
	relative := strings.Contains(prog.Name, "/") && !path.IsAbs(prog.Name)
	if !relative || !plan.DirChanged && prog.Dir == "" {
		if slices.ContainsFunc(l.allowCmds, func(r shellplan.Rule) bool { return r.Match(prog) }) {
			return true
		}
	}
	if !path.IsAbs(prog.Name) {
		return false
	}
	name := path.Clean(prog.Name)
	return slices.ContainsFunc(l.scriptDirs, func(d string) bool {
		return strings.HasPrefix(name, strings.TrimSuffix(d, "/")+"/")
	})
}

func (l layer) reject(why string) error {
	sentences := append([]string{"command rejected: " + why + "."}, l.restrictions("Every")...)
	return errors.New(strings.Join(sentences, " "))
}

// describe returns the program as the command runs it, as far as it is
// known.
func describe(prog shellplan.Program) string {
	s := strings.Join(append([]string{prog.Name}, prog.Args...), " ")
	if !prog.Complete {
		s += " …"
	}
	return s
}

func formatRules(rules []shellplan.Rule) string {
	ret := make([]string, len(rules))
	for i, r := range rules {
		ret[i] = r.String()
	}
	return formatList(ret)
}

func formatList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "`" + s + "`"
	}
	return strings.Join(quoted, ", ")
}
