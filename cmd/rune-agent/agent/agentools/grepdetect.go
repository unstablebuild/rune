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

package agentools

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"unstable.build/rune/cmd/rune-agent/agent"
	"unstable.build/rune/cmd/rune-agent/configedit"
)

// grepProgramNames lists the search programs we want to intercept.
// They are matched after stripping the directory part (so
// /usr/bin/grep matches) and after stripping a leading backslash (so
// `\grep` matches — that idiom bypasses shell aliases). Includes:
//   - grep family: grep, egrep, fgrep, rgrep, plus compression
//     wrappers (zgrep, zegrep, zfgrep, bzgrep, bzegrep, bzfgrep,
//     xzgrep, xzegrep, xzfgrep, lzgrep, lzegrep, lzfgrep) and
//     PCRE/PCRE2/ugrep variants.
//   - alternative searchers: rg (ripgrep), ripgrep, ag (the silver
//     searcher), ack, ack-grep.
var grepProgramNames = map[string]bool{
	// grep family.
	"grep": true, "egrep": true, "fgrep": true, "rgrep": true,
	"zgrep": true, "zegrep": true, "zfgrep": true,
	"bzgrep": true, "bzegrep": true, "bzfgrep": true,
	"xzgrep": true, "xzegrep": true, "xzfgrep": true,
	"lzgrep": true, "lzegrep": true, "lzfgrep": true,
	"pcregrep": true, "pcre2grep": true, "ugrep": true,
	// alternative searchers.
	"rg": true, "ripgrep": true,
	"ag": true, "ack": true, "ack-grep": true,
}

// grepWrapperPrograms run another command as their effective payload.
// When we see one of these as the program word (or anywhere in the
// argument chain after one), we keep scanning the remaining literal
// arguments for a grep-like program name so wrappers like:
//
//	xargs grep foo
//	sudo grep foo
//	timeout 5 grep foo
//	env LC_ALL=C grep foo
//	git grep foo
//	find . -name '*.go' -exec grep foo {} \;
//
// are all intercepted just like a plain `grep foo` would be.
//
// `find` is intentionally included even though its `-exec` syntax can
// in theory match `-name grep` (a search for files literally named
// "grep"). That ambiguity is not seen in practice; the cost of
// missing `find -exec grep` evasion is much higher.
var grepWrapperPrograms = map[string]bool{
	"xargs":    true,
	"env":      true,
	"time":     true,
	"command":  true,
	"exec":     true,
	"sudo":     true,
	"doas":     true,
	"nohup":    true,
	"nice":     true,
	"ionice":   true,
	"watch":    true,
	"timeout":  true,
	"stdbuf":   true,
	"parallel": true,
	"git":      true,
	"hg":       true,
	"jj":       true,
	"fossil":   true,
	"find":     true,
}

// isGrepInvocation reports whether the script invokes grep/rg/ag/ack
// as the first stage of execution — directly, behind &&/||, inside
// command substitution, after env assignments, or as a redirection
// source/target. We deliberately allow search tools when they appear
// as a downstream stage of a pipeline (e.g. `cat file | grep foo`,
// `find . | xargs grep foo`) because in that role they are filtering
// data that is already on stdin rather than performing a filesystem
// search.
//
// Known gap: we do not re-parse the body of `bash -c "..."`. A model
// determined enough to wrap grep in a subshell string still gets
// through, but the canonical evasion patterns from real chats no
// longer work.
func isGrepInvocation(script string) bool {
	script = strings.TrimSpace(script)
	if script == "" {
		return false
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil || file == nil {
		return false
	}
	for _, stmt := range file.Stmts {
		if stmtMentionsGrep(stmt) {
			return true
		}
	}
	return false
}

// stmtMentionsGrep returns true if the statement or any of its
// pipeline/redirection components invokes a grep-like program.
func stmtMentionsGrep(stmt *syntax.Stmt) bool {
	if stmt == nil {
		return false
	}
	// Redirections (e.g. `< $(grep ...)`) may themselves contain a
	// command substitution that invokes grep.
	for _, r := range stmt.Redirs {
		if wordMentionsGrep(r.Word) || wordMentionsGrep(r.Hdoc) {
			return true
		}
	}
	return commandMentionsGrep(stmt.Cmd)
}

// commandMentionsGrep walks every command shape the parser can produce
// and reports whether grep appears anywhere inside.
func commandMentionsGrep(cmd syntax.Command) bool {
	switch c := cmd.(type) {
	case nil:
		return false
	case *syntax.CallExpr:
		return callExprMentionsGrep(c)
	case *syntax.BinaryCmd:
		if c.Op == syntax.Pipe || c.Op == syntax.PipeAll {
			// Pipelines parse left-associatively: `a | b | c` becomes
			// BinaryCmd{X: BinaryCmd{X: a, Y: b}, Y: c}. We descend
			// only into the left subtree so we ultimately inspect the
			// leftmost (first) stage of the pipeline. Downstream
			// stages are intentionally ignored: a model running `cat
			// file | grep foo`, `find . | xargs grep`, or `cmd1 |
			// cmd2 | grep` is using grep as an output filter, not as
			// a filesystem searcher, and that is allowed.
			return stmtMentionsGrep(c.X)
		}
		// && and || — both sides are independent commands.
		return stmtMentionsGrep(c.X) || stmtMentionsGrep(c.Y)
	case *syntax.Subshell:
		return stmtsMentionGrep(c.Stmts)
	case *syntax.Block:
		return stmtsMentionGrep(c.Stmts)
	case *syntax.IfClause:
		if stmtsMentionGrep(c.Cond) || stmtsMentionGrep(c.Then) {
			return true
		}
		// c.Else is a typed-nil *IfClause when there is no
		// elif/else branch. A typed-nil pointer wrapped in the
		// syntax.Command interface does not match `case nil` in this
		// switch, so we must avoid passing it through the interface.
		if c.Else == nil {
			return false
		}
		return commandMentionsGrep(c.Else)
	case *syntax.WhileClause:
		return stmtsMentionGrep(c.Cond) || stmtsMentionGrep(c.Do)
	case *syntax.ForClause:
		return stmtsMentionGrep(c.Do)
	case *syntax.CaseClause:
		for _, item := range c.Items {
			if stmtsMentionGrep(item.Stmts) {
				return true
			}
		}
		return false
	case *syntax.FuncDecl:
		// Delegate to stmtMentionsGrep so a nil Body, a nil Cmd
		// interface, and any redirections on the body are all handled
		// uniformly.
		return stmtMentionsGrep(c.Body)
	case *syntax.TimeClause:
		// `time grep foo` — time is a reserved keyword in bash.
		return stmtMentionsGrep(c.Stmt)
	case *syntax.CoprocClause:
		return stmtMentionsGrep(c.Stmt)
	default:
		return false
	}
}

func stmtsMentionGrep(stmts []*syntax.Stmt) bool {
	for _, s := range stmts {
		if stmtMentionsGrep(s) {
			return true
		}
	}
	return false
}

// callExprMentionsGrep checks whether a simple command invokes grep,
// taking into account leading environment assignments (`FOO=bar grep
// ...`) and any grep tucked inside argument-level command substitution.
func callExprMentionsGrep(call *syntax.CallExpr) bool {
	if call == nil {
		return false
	}
	// `VAR=$(grep ...)` style — env values may themselves contain grep.
	for _, a := range call.Assigns {
		if wordMentionsGrep(a.Value) {
			return true
		}
	}
	if len(call.Args) == 0 {
		return false
	}
	// First: any argument may contain a $(grep ...) or <(grep ...)
	// substitution, regardless of what the leading program is.
	// This catches `echo $(grep foo)`, `diff <(grep a) <(grep b)`,
	// and other patterns where a non-search command's input is
	// produced by grep.
	for _, w := range call.Args {
		if wordMentionsGrep(w) {
			return true
		}
	}
	// Second: walk literal program words and chain through wrappers.
	// The rules:
	//
	//   1. A literal word whose base name is a grep program matches.
	//   2. The leading program word is allowed to be a wrapper
	//      (xargs, sudo, env, git, find, ...). Once we have seen a
	//      wrapper, every subsequent literal word is scanned for a
	//      grep program — flags, wrapper-arguments, sub-commands
	//      and find-action arguments are all transparent to us, so
	//      `timeout 5 grep`, `git grep`, and `find . -exec grep ...`
	//      all match.
	//   3. If args[0] is neither a wrapper nor a grep program, we
	//      stop literal scanning so `echo grep foo` does NOT match.
	seenWrapper := false
	for i, w := range call.Args {
		name, ok := literalProgramName(w)
		if !ok {
			// Non-literal word (variable expansion, expansion-only
			// arg, etc.). If we have already seen a wrapper, keep
			// scanning; otherwise stop.
			if !seenWrapper {
				return false
			}
			continue
		}
		base := filepath.Base(stripLeadingBackslash(name))
		if grepProgramNames[base] {
			return true
		}
		if grepWrapperPrograms[base] {
			if i == 0 || seenWrapper {
				seenWrapper = true
				continue
			}
		}
		if seenWrapper {
			// Wrapper args (numbers, paths, sub-commands, flags)
			// are transparent to us; keep scanning for grep.
			continue
		}
		// Leading non-wrapper, non-grep program — `echo grep foo`,
		// `make build`, etc. Stop scanning.
		return false
	}
	return false
}

// stripLeadingBackslash removes a single leading backslash from a
// command name. In an unquoted shell context `\grep` is the same as
// `grep` (the backslash suppresses alias expansion). The parser
// preserves the raw source so we normalize here.
func stripLeadingBackslash(s string) string {
	if len(s) > 1 && s[0] == '\\' {
		return s[1:]
	}
	return s
}

// literalProgramName extracts the literal value of a command-name word.
// Returns ("", false) if the word contains expansions or substitutions
// that we cannot statically resolve.
func literalProgramName(word *syntax.Word) (string, bool) {
	if word == nil || len(word.Parts) == 0 {
		return "", false
	}
	var b strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, inner := range p.Parts {
				lit, ok := inner.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	name := strings.TrimSpace(b.String())
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return "", false
	}
	return name, true
}

// wordMentionsGrep returns true when a word contains a command or
// process substitution that itself invokes grep. Plain literals and
// parameter expansions are ignored.
func wordMentionsGrep(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	return partsMentionGrep(word.Parts)
}

func partsMentionGrep(parts []syntax.WordPart) bool {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.CmdSubst:
			if stmtsMentionGrep(p.Stmts) {
				return true
			}
		case *syntax.ProcSubst:
			if stmtsMentionGrep(p.Stmts) {
				return true
			}
		case *syntax.DblQuoted:
			if partsMentionGrep(p.Parts) {
				return true
			}
		}
	}
	return false
}

// forceBuiltinToolsKey is the configedit key consulted by the bash/
// exec_command grep guard. true = always reject bare grep invocations;
// false = always allow; absent = ask the user.
const forceBuiltinToolsKey = "force_builtin_tools"

// setForceBuiltinTools updates force_builtin_tools via cfg.
// When ephemeral is false the value is persisted to .rune/config.yaml;
// when true it is only written to the in-memory overlay and lost on
// restart. Subsequent Resolve calls on the corresponding configedit.Bool
// observe the new value either way.
func setForceBuiltinTools(ctx context.Context, cfg configedit.Setter, v, ephemeral bool) error {
	return cfg.SetBool(ctx, forceBuiltinToolsKey, v, ephemeral)
}

// builtinToolsErrorMessage returns the canonical message returned to
// the model when a grep invocation is rejected. The exact tool names
// depend on the provider's tool set (e.g. search_content vs
// grep_files).
func builtinToolsErrorMessage(provider string) string {
	searchTool := "search_content"
	symbolSearchTool := "search_symbols"
	outlineTool := "outline_file"
	switch provider {
	case "openai", "codex":
		searchTool = "grep_files"
	case "gemini":
		// Gemini sees Antigravity-native tool names (see package geminitools).
		searchTool = "grep_search"
		symbolSearchTool = "codebase_search"
		outlineTool = "view_file_outline"
	}
	return "Do not use grep for searching. Prefer Rune's semantic " +
		"tools, which understand code structure:\n" +
		"  • `find_definition` (takes a symbol name) — locate where a " +
		"function/type/var is defined.\n" +
		"  • `find_references` (takes a symbol name) — list every use " +
		"site of a symbol across the workspace.\n" +
		"  • `find_implementations` (takes an interface name) — list " +
		"all concrete types that satisfy an interface.\n" +
		"  • `describe_symbol` (takes a symbol name) — show the type " +
		"signature and doc comment for a symbol without reading the file.\n" +
		"  • `" + symbolSearchTool + "` (takes a fuzzy query) — find a symbol " +
		"by partial or approximate name when the exact name is unknown.\n" +
		"  • `" + outlineTool + "` (takes a file path) — inspect a file's " +
		"top-level structure instead of reading the whole file with " +
		"`read_file`.\n" +
		"Name symbols with as much dotted qualification as you know — " +
		"the full import path is never required:\n" +
		"  1. Container known: go: <package>.<Symbol>; python: " +
		"<module>.<symbol> or any longer trailing part of the module " +
		"path; methods: <Type>.<method> or <module>.<Class>.<method>.\n" +
		"  2. Container unknown: pass the bare symbol name; lookups " +
		"fall back to a fuzzy workspace-wide search.\n" +
		"  3. Name unknown: start with `" + symbolSearchTool + "` and a " +
		"partial name, then navigate with a name from its results.\n" +
		"Only when you are searching for a non-symbol string (a comment, " +
		"error message, or literal value) use the `" + searchTool +
		"` tool."
}

// grepGuard encapsulates the dependencies needed to decide whether a
// bare grep invocation should be allowed. The prompter is read from
// the per-call context (agent.PrompterFromContext); the configuration
// is consulted for the persisted user preference.
//
// forceBuiltinTools is the deferred configedit.Bool view of the
// "force_builtin_tools" key. It is built once at construction time
// and resolved per call so writes through cfg.SetBool (Always/Never
// choices) are observed immediately in the same session.
type grepGuard struct {
	cfg               configedit.Config
	forceBuiltinTools configedit.Bool
}

// newGrepGuard returns a grepGuard wired to cfg. Panics if cfg is nil.
func newGrepGuard(cfg configedit.Config) grepGuard {
	if cfg == nil {
		panic("agentools: grepGuard cfg must not be nil")
	}
	return grepGuard{
		cfg:               cfg,
		forceBuiltinTools: cfg.GetBool(forceBuiltinToolsKey),
	}
}

// decideGrep decides whether to reject a bare grep invocation. The
// returned (rejected, decided) pair has these meanings:
//   - decided == false: caller should run the command (no guard is
//     active, or no prompter is configured to ask the user).
//   - decided == true && rejected == true: caller should return the
//     canonical "use builtin tools" error.
//   - decided == true && rejected == false: user explicitly allowed
//     this invocation; caller should run the command.
func (g grepGuard) decideGrep(ctx context.Context) (rejected, decided bool) {
	v, err := g.forceBuiltinTools.Resolve(ctx)
	switch {
	case err == nil:
		return v, true
	case errors.Is(err, configedit.ErrNotFound):
		// fall through to prompt
	default:
		slog.Warn("get force_builtin_tools from config", "error", err)
	}
	prompter := agent.PrompterFromContext(ctx)
	if prompter == nil {
		// No way to ask the user; default to allowing the command so
		// that workflows without a prompter (tests, e2e tools) are not
		// silently blocked.
		return false, false
	}
	resp, err := prompter.Prompt(ctx, agent.PromptRequest{
		Title:  "Allow grep via shell?",
		Header: "grep",
		Kind:   agent.PromptKindPermission,
		Body: "The model is trying to search code by shelling out to a " +
			"text-pattern tool (grep, rg, ag, ack, or a " +
			"wrapper such as `git grep` / `xargs grep`). Rune ships " +
			"builtin tools that are usually better: semantic " +
			"symbol-based search (find_definition, find_references, " +
			"find_implementations, search_symbols) and the structured " +
			"`search_content` text search. Allow the shell call anyway?",
		Options: []agent.PromptOption{
			{Value: "yes", Label: "Yes", Description: "Allow this single call"},
			{Value: "always", Label: "Always",
				Description: "Always allow; remember the choice"},
			{Value: "session_yes", Label: "Yes, this session",
				Description: "Allow for the rest of this session"},
			{Value: "no", Label: "No", Description: "Reject this single call"},
			{Value: "never", Label: "Never",
				Description: "Always reject; remember the choice"},
			{Value: "session_no", Label: "Not this session",
				Description: "Reject for the rest of this session"},
		},
	})
	if err != nil {
		// Dismissed: treat as "no" — reject this single call.
		return true, true
	}
	var choice string
	if len(resp.Values) > 0 {
		choice = resp.Values[0]
	}
	switch choice {
	case "yes":
		return false, true
	case "always":
		if err := setForceBuiltinTools(ctx, g.cfg, false, false); err != nil {
			slog.Warn("persist force_builtin_tools=false", "error", err)
		}
		return false, true
	case "session_yes":
		if err := setForceBuiltinTools(ctx, g.cfg, false, true); err != nil {
			slog.Warn("set force_builtin_tools=false (session)", "error", err)
		}
		return false, true
	case "no":
		return true, true
	case "never":
		if err := setForceBuiltinTools(ctx, g.cfg, true, false); err != nil {
			slog.Warn("persist force_builtin_tools=true", "error", err)
		}
		return true, true
	case "session_no":
		if err := setForceBuiltinTools(ctx, g.cfg, true, true); err != nil {
			slog.Warn("set force_builtin_tools=true (session)", "error", err)
		}
		return true, true
	default:
		// Unknown response: be safe and reject.
		return true, true
	}
}
