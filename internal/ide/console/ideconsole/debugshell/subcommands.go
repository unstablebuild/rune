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

package debugshell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/google/go-dap"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/debugapi"
	"github.com/unstablebuild/rune-go-sdk/api/syntaxapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
)

// errSessionActive is returned when the user runs `initialize`
// while a debug session is already active.
var errSessionActive = errors.New(
	"a debug session is already active; stop it via 'terminate'")

// errNoSession is returned when a subcommand that requires an
// active session is invoked before `initialize`.
var errNoSession = errors.New(
	"no debug session; start one with 'debugger initialize <langID>'")

var errNeedStart = errors.New(
	"debug session initialized; call 'debugger launch ...' or 'debugger attach ...' next")

var errNeedConfigured = errors.New(
	"debug session started; call 'debugger configured' after setting breakpoints")

var errAlreadyConfigured = errors.New(
	"debug session already configured; continue with execution commands or terminate")

func (h *Handler) dispatch(
	ctx context.Context, sub string, args []string,
	pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	switch sub {
	case subHelp:
		return h.Help(ctx, nil)
	case subInitialize:
		return h.cmdInitialize(ctx, args, pw)
	case subLaunch:
		return h.cmdLaunch(ctx, args)
	case subAttach:
		return h.cmdAttach(ctx, args, pw)
	case subConfigured:
		return h.cmdConfigured(ctx)
	case subTerminate:
		return h.cmdTerminate(ctx)
	case subRestart:
		return h.cmdRestart(ctx)
	case subContinue:
		return h.cmdContinue(ctx, args)
	case subNext:
		return h.cmdNext(ctx, args)
	case subStepIn:
		return h.cmdStepIn(ctx, args)
	case subStepOut:
		return h.cmdStepOut(ctx, args)
	case subStepBack:
		return h.cmdStepBack(ctx, args)
	case subReverseContinue:
		return h.cmdReverseContinue(ctx, args)
	case subPause:
		return h.cmdPause(ctx, args)
	case subGoto:
		return h.cmdGoto(ctx, args)
	case subThreads:
		return h.cmdThreads(ctx)
	case subStackTrace:
		return h.cmdStackTrace(ctx, args)
	case subScopes:
		return h.cmdScopes(ctx, args)
	case subVariables:
		return h.cmdVariables(ctx, args)
	case subModules:
		return h.cmdModules(ctx)
	case subLoadedSources:
		return h.cmdLoadedSources(ctx)
	case subSetBreakpoint:
		return h.cmdSetBreakpointREPL(), nil
	case subSetVariable:
		return h.cmdSetVariable(ctx, args)
	case subSetExpression:
		return h.cmdSetExpression(ctx, args)
	case subEvaluate:
		return h.cmdEvaluate(ctx, args)
	case subDisassemble:
		return h.cmdDisassemble(ctx, args)
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownSubcommand, sub)
	}
}

func (h *Handler) requireStartedAndConfigured() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessionID == "" {
		return "", errNoSession
	}
	switch h.phase {
	case phaseInitialized:
		return "", errNeedStart
	case phaseStarted:
		return "", errNeedConfigured
	case phaseConfigured:
		return h.sessionID, nil
	default:
		return "", errNoSession
	}
}

func (h *Handler) requireInitializedOnly() (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessionID == "" {
		return "", errNoSession
	}
	return h.sessionID, nil
}

func (h *Handler) replayBreakpoints(
	ctx context.Context, sid string,
) error {
	h.mu.Lock()
	snapshot := make(map[string][]int, len(h.breakpoints))
	for path, lines := range h.breakpoints {
		snapshot[path] = append([]int(nil), lines...)
	}
	h.mu.Unlock()
	for path, lines := range snapshot {
		sbps := make([]dap.SourceBreakpoint, 0, len(lines))
		for _, l := range lines {
			sbps = append(sbps, dap.SourceBreakpoint{Line: l})
		}
		if _, err := h.dbg.SetBreakpoints(ctx, sid, &dap.SetBreakpointsArguments{
			Source:      dap.Source{Path: path},
			Breakpoints: sbps,
		}); err != nil {
			return fmt.Errorf("set-breakpoints for %s: %w", path, err)
		}
	}
	return nil
}

// normalizeBreakpointLine returns the 1-based line of the next
// statement at or after line. Uses the configured tree-sitter
// parser to walk the AST so blank lines, comments, function
// bodies, etc. are handled by the language grammar instead of
// hand-rolled heuristics.
//
// Adjustment is necessary because Delve only binds breakpoints
// on lines that map to executable instructions; setting one on
// a blank or comment-only line yields an unverified breakpoint
// that never fires and the program runs to completion.
//
// Returns an error when no statement exists at or after line —
// e.g. the user pointed at the closing brace of the last
// function in the file. The caller must surface this so the
// user sees the failure rather than a silent no-op.
func (h *Handler) normalizeBreakpointLine(path string, line int) (int, error) {
	uri, err := workspaceapi.ParseURI("file://" + path)
	if err != nil {
		return 0, fmt.Errorf("parse uri: %w", err)
	}
	target, err := nextStatementLine(h.parser, h.fs, uri, path, line)
	if err != nil {
		return 0, err
	}
	if target != line && h.notify != nil {
		h.notify(browserapi.LevelInfo,
			"debugger: breakpoint at %s:%d adjusted to line %d",
			path, line, target)
	}
	return target, nil
}

// codeAnchorTypes are the cross-language capture kinds that
// mark a line as containing executable code: any reference,
// any definition (var/func/method/type/namespace). Scopes are
// deliberately excluded — they are only used as bounding
// regions, not as anchor candidates, since a scope spans many
// lines.
const codeAnchorTypes = syntaxapi.NodeCaptureReference |
	syntaxapi.NodeCaptureDefinitionVar |
	syntaxapi.NodeCaptureDefinitionFunc |
	syntaxapi.NodeCaptureDefinitionMethod |
	syntaxapi.NodeCaptureDefinitionType |
	syntaxapi.NodeCaptureDefinitionNamespace

// commentQuery matches every `(comment)` node in the file.
// `(comment)` is a convention shared by virtually every
// tree-sitter grammar (Go, Python, JS, YAML, Rust, ...), so
// the query is language-agnostic. nextStatementLine uses the
// comment ranges to skip comment-only lines without having
// to enumerate per-language statement node types.
const commentQuery = `(comment) @c`

// nextStatementLine returns the 1-based line of the next
// breakpoint-able location at or after line in uri.
//
// The implementation is language-agnostic. It uses two
// pieces of grammar information that every tree-sitter
// grammar exposes via the IDE's queries:
//
//   - `local.scope` from locals.scm — to bound the search to
//     the user's smallest enclosing function/block.
//   - `(comment)` nodes — by tree-sitter convention every
//     grammar names its comment node `comment`, so we can
//     skip comment-only lines without enumerating
//     per-language statement node types.
//
// Combined with a text-level blank-line check, this lets us
// accept any line that contains source code as a breakpoint
// target — including literal-only statements like
// `return false`, bare `break`, `fallthrough`, ... that
// the previous `local.reference`/`local.definition.*` based
// algorithm could not see because their operands do not
// produce identifier captures.
//
// The algorithm:
//
//  1. Find the smallest local.scope that contains line.
//  2. Walk forward from line (inclusive), stopping at the
//     scope's last row (or end of file when no scope
//     encloses line), and return the first row that is
//     non-blank and not fully covered by a comment node.
//  3. As a final safety net, if no such row is found but a
//     code anchor exists at or after line within the scope,
//     return that anchor's row. This preserves the previous
//     behaviour for files we failed to read.
//
// Returns an error when no anchor exists at or after line —
// e.g. the cursor is on the closing brace of the last
// function in the file. Callers must surface this so the
// user retries on a different line.
func nextStatementLine(
	parser syntaxapi.Parser, fs workspaceapi.FileSystem,
	uri workspaceapi.URI, path string, line int,
) (int, error) {
	scopes, err := queryNodeAll(parser, uri, syntaxapi.NodeCaptureScope)
	if err != nil {
		return 0, fmt.Errorf("query scopes: %w", err)
	}
	enclosing, hasEnclosing := smallestEnclosingScope(scopes, line)

	// Determine the row range to search within. Without an
	// enclosing scope (e.g. between top-level definitions),
	// the search is unbounded — match the previous
	// fall-back-to-anchors behaviour at the very end.
	endRow := -1 // -1 means "no upper bound"
	if hasEnclosing {
		endRow = enclosing.To.Y
	}

	// Best-effort line scan over the actual source bytes.
	// When no workspace FS is wired (test handlers) or the
	// file cannot be read for any other reason, fall back
	// to the anchor-only path so we never regress behaviour.
	if row, ok := firstCodeLine(parser, fs, uri, path, line, endRow); ok {
		return row, nil
	}

	anchors, err := queryNodeAll(parser, uri, codeAnchorTypes)
	if err != nil {
		return 0, fmt.Errorf("query code anchors: %w", err)
	}
	best := -1
	for _, a := range anchors {
		row := a.From.Y + 1
		if row < line {
			continue
		}
		if hasEnclosing && !anchorInScope(a, enclosing) {
			continue
		}
		if best < 0 || row < best {
			best = row
		}
	}
	if best < 0 {
		return 0, fmt.Errorf("no statement at or after line %d", line)
	}
	return best, nil
}

// firstCodeLine returns the 1-based line >= startLine (and
// <= endRow+1 when endRow >= 0) in the file identified by uri
// that contains source code: a non-blank line that is not
// fully covered by a `(comment)` node.
//
// ok is false when the file cannot be read or the grammar
// has no comment query results AND the file is empty —
// callers must then fall back to the anchor-based path.
func firstCodeLine(
	parser syntaxapi.Parser, fs workspaceapi.FileSystem,
	uri workspaceapi.URI, path string,
	startLine, endRow int,
) (int, bool) {
	f, err := fs.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return 0, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(f)
	if err != nil {
		return 0, false
	}
	// Errors from the comment query are tolerated:
	// grammars without a `(comment)` node simply yield no
	// matches, which is harmless — the blank-line check
	// below still skips whitespace-only lines.
	comments, _ := queryStringAll(parser, uri, commentQuery, "c")

	lines := strings.Split(string(data), "\n")

	// A row is "comment-only" when every non-whitespace
	// character on that row falls inside a comment node.
	// This works for both line comments (// ..., # ...)
	// and inner rows of block comments (/* ... */),
	// regardless of leading indentation.
	commentRow := func(row int) bool {
		if row < 0 || row >= len(lines) {
			return false
		}
		line := lines[row]
		// Find first and last non-whitespace columns.
		firstCol, lastCol := -1, -1
		for i, r := range line {
			if r == ' ' || r == '\t' {
				continue
			}
			if firstCol < 0 {
				firstCol = i
			}
			lastCol = i
		}
		if firstCol < 0 {
			return false // blank line; handled separately
		}
		for _, c := range comments {
			if row < c.From.Y || row > c.To.Y {
				continue
			}
			startCol := 0
			if row == c.From.Y {
				startCol = c.From.X
			}
			endCol := len(line)
			if row == c.To.Y {
				endCol = c.To.X
			}
			if firstCol >= startCol && lastCol < endCol {
				return true
			}
		}
		return false
	}

	maxLine := len(lines)
	endLine := maxLine
	if endRow >= 0 && endRow+1 < endLine {
		endLine = endRow + 1
	}
	for ln := startLine; ln <= endLine; ln++ {
		if ln < 1 || ln > maxLine {
			continue
		}
		if strings.TrimSpace(lines[ln-1]) == "" {
			continue
		}
		if commentRow(ln - 1) {
			continue
		}
		// Skip lines whose only non-whitespace token is a
		// closing delimiter. Delve never binds breakpoints
		// to bare `}`, `)`, `]` lines, and accepting them
		// here would regress TestE2E_BreakpointOnClosingBrace.
		if isDelimiterOnly(lines[ln-1]) {
			continue
		}
		return ln, true
	}
	return 0, false
}

// isDelimiterOnly reports whether line, after trimming
// whitespace, contains only closing delimiters. Used to
// preserve the "no statement at or after line N" behaviour
// when the cursor sits on a closing brace.
func isDelimiterOnly(line string) bool {
	t := strings.TrimSpace(line)
	if t == "" {
		return false
	}
	for _, r := range t {
		switch r {
		case '}', ')', ']', ',', ';':
		default:
			return false
		}
	}
	return true
}

func queryNodeAll(
	parser syntaxapi.Parser,
	uri workspaceapi.URI,
	kinds syntaxapi.NodeCaptureName,
) ([]syntaxapi.Result, error) {
	it, err := parser.QueryNode(uri, kinds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	var out []syntaxapi.Result
	for {
		v, ok := it.Next(context.Background())
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out, nil
}

// queryStringAll runs a raw tree-sitter query against uri and
// returns every match for the given capture name. It is the
// arbitrary-query sibling of queryNodeAll. Errors are
// returned to the caller; nextStatementLine treats them as a
// signal that the grammar does not understand the query and
// falls back to the anchor-only path.
func queryStringAll(
	parser syntaxapi.Parser,
	uri workspaceapi.URI,
	query, captureName string,
) ([]syntaxapi.Result, error) {
	it, err := parser.Query(uri, query, []string{captureName})
	if err != nil {
		return nil, err
	}
	defer func() { _ = it.Close() }()
	var out []syntaxapi.Result
	for {
		v, ok := it.Next(context.Background())
		if !ok {
			break
		}
		out = append(out, v)
	}
	return out, nil
}

// smallestEnclosingScope returns the smallest scope that
// contains line (1-based) — i.e. the deepest function/block.
// hasOk is false when no scope contains the line (typical for
// shell/yaml where only function bodies are scopes).
func smallestEnclosingScope(
	scopes []syntaxapi.Result, line int,
) (syntaxapi.Result, bool) {
	row := line - 1
	var best syntaxapi.Result
	have := false
	for _, s := range scopes {
		if row < s.From.Y || row > s.To.Y {
			continue
		}
		if !have || scopeStrictlyContains(best, s) {
			best = s
			have = true
		}
	}
	return best, have
}

// scopeStrictlyContains reports whether outer fully contains
// inner and is at least as wide. Used to pick the deepest
// scope.
func scopeStrictlyContains(outer, inner syntaxapi.Result) bool {
	if inner.From.Y < outer.From.Y || inner.To.Y > outer.To.Y {
		return false
	}
	if inner.From.Y == outer.From.Y && inner.From.X < outer.From.X {
		return false
	}
	if inner.To.Y == outer.To.Y && inner.To.X > outer.To.X {
		return false
	}
	// outer >= inner; outer-strictly-contains-inner when they
	// differ in any coordinate.
	return outer.From != inner.From || outer.To != inner.To
}

// anchorInScope reports whether the start of a is contained
// within s (end-inclusive on rows, end-exclusive otherwise).
func anchorInScope(a, s syntaxapi.Result) bool {
	if a.From.Y < s.From.Y || a.From.Y > s.To.Y {
		return false
	}
	if a.From.Y == s.From.Y && a.From.X < s.From.X {
		return false
	}
	if a.From.Y == s.To.Y && a.From.X > s.To.X {
		return false
	}
	return true
}

// cmdInitialize creates a new debug session for the given
// language ID and returns an iterator that streams DAP events
// for the session. The iterator closes when the session ends.
func (h *Handler) cmdInitialize(
	ctx context.Context, args []string, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New("usage: debugger initialize <langID>")
	}
	langID := args[0]
	// Detach from the caller's ctx for the duration of the
	// debug session. The REPL's per-command context is
	// cancelled on Ctrl-C (handler.dispatchCommand →
	// cmdCancel); the debug session itself, however, must
	// outlive that cancellation. Carrying the cancellable ctx
	// into CreateSession (and from there into watch
	// goroutines) would allow Ctrl-C to silently tear down
	// the session even though `debugger terminate` is the
	// only documented way to end it.
	ctx = context.WithoutCancel(ctx)
	sid, ch, err := h.beginSession(ctx, "initialize",
		func(ctx context.Context) (string, error) {
			sid, _, err := h.dbg.CreateSession(
				ctx, langID, defaultClientCapabilities(), h)
			return sid, err
		})
	if err != nil {
		return nil, err
	}
	return newSessionIterator(langID, sid, ch, pw,
		h.clearInstalledLocations), nil
}

// beginSession reserves the session event channel and the output
// sink, runs create to establish the adapter session, and records
// the resulting session on the handler in the initialized phase.
// what prefixes any returned error (the subcommand name).
func (h *Handler) beginSession(
	ctx context.Context, what string,
	create func(context.Context) (string, error),
) (string, chan sessionEvent, error) {
	h.mu.Lock()
	if h.sessionID != "" {
		h.mu.Unlock()
		return "", nil, errSessionActive
	}
	// Reserve the channel up front so OnEvent calls made
	// synchronously from CreateSession are not dropped.
	ch := make(chan sessionEvent, 64)
	h.events = ch
	h.mu.Unlock()

	release := func() {
		h.mu.Lock()
		if h.events == ch {
			h.events = nil
		}
		h.mu.Unlock()
	}

	// Create the per-session output sink BEFORE CreateSession
	// so OutputEvents emitted during the adapter handshake
	// (e.g. delve's "Type 'dlv help' ..." console message) are
	// captured. cmdLaunch/cmdAttach previously installed the
	// sink, which dropped any OutputEvent fired earlier.
	if _, err := h.startOutputCapture(); err != nil {
		release()
		return "", nil, fmt.Errorf("%s: %w", what, err)
	}

	sid, err := create(ctx)
	if err != nil {
		h.stopOutputCapture()
		release()
		return "", nil, fmt.Errorf("%s: %w", what, err)
	}

	h.mu.Lock()
	h.sessionID = sid
	h.phase = phaseInitialized
	h.mu.Unlock()
	return sid, ch, nil
}

func (h *Handler) cmdLaunch(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	const usage = "usage: debugger launch [-e KEY=VAL]... [--] <program> [args...]"
	if len(args) == 0 {
		return nil, errors.New(usage)
	}
	program, progArgs, env, err := parseLaunchArgs(args)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", usage, err)
	}
	// Resolve a relative program against the workspace root so the
	// adapter receives an absolute path. debugpy derives the
	// debuggee cwd from the program's directory and then re-resolves
	// a still-relative program against it, doubling path segments
	// (e.g. <root>/src/src/init.py).
	program = h.resolveProgram(program)
	sid, err := h.requireInitializedOnly()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	switch h.phaseLocked() {
	case phaseInitialized:
		// allowed
	case phaseStarted:
		h.mu.Unlock()
		return nil, errNeedConfigured
	case phaseConfigured:
		h.mu.Unlock()
		return nil, errAlreadyConfigured
	default:
		h.mu.Unlock()
		return nil, errNoSession
	}
	h.mu.Unlock()
	// Sink was created at initialize time; this call is a
	// no-op safety net that returns the active path.
	outputPath, err := h.startOutputCapture()
	if err != nil {
		return nil, fmt.Errorf("launch: %w", err)
	}
	lArgs := debugapi.LaunchRequestArguments{
		Program: program,
		Args:    progArgs,
		Env:     env,
	}
	// Arm the InitializedEvent channel before dispatching
	// Launch so the iterator we return is guaranteed to see
	// the event even if the adapter is extremely fast.
	initCh := h.armLaunchInit()
	if err := h.dbg.Launch(ctx, sid, lArgs); err != nil {
		// Launch failed: release the channel we just armed
		// to avoid leaking a one-shot blocker.
		h.signalLaunchInit()
		// Leave the sink open: the session is still
		// initialized and the user may retry or terminate.
		return nil, fmt.Errorf("launch: %w", err)
	}
	h.mu.Lock()
	h.phase = phaseStarted
	h.mu.Unlock()
	out := []string{
		fmt.Sprintf("Launch sent for `%s`.", program),
	}
	if outputPath != "" {
		out = append(out, fmt.Sprintf(
			"Debuggee stdout/stderr is being captured at `%s`.",
			outputPath))
	}
	out = append(out,
		"Run `debugger configured` when configuration is complete.")
	return newLaunchIterator(out, initCh), nil
}

// resolveProgram turns a workspace-relative program path into an
// absolute path rooted at the workspace. Absolute paths and paths
// that cannot be resolved are returned unchanged so the adapter
// still receives a usable value.
func (h *Handler) resolveProgram(program string) string {
	if program == "" || strings.HasPrefix(program, "/") {
		return program
	}
	uri, err := h.fs.URI(program)
	if err != nil {
		return program
	}
	if p := uri.Path(); p != "" {
		return p
	}
	return program
}

// parseLaunchArgs parses the argv of `debugger launch` into a program
// path, program args, and an env map. Flags accepted before the program
// (or before an optional `--` separator):
//
//	-e KEY=VAL    Set an environment variable for the debuggee.
//	              Repeatable. VAL may contain '=' characters.
//
// `--` terminates flag parsing; the next token is the program and all
// remaining tokens are program args (so program args may start with
// `-`). Without `--`, the first non-flag token is the program and no
// further flag parsing occurs.
func parseLaunchArgs(
	args []string,
) (program string, progArgs []string, env map[string]string, err error) {
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if a == "-e" {
			if i+1 >= len(args) {
				return "", nil, nil, errors.New("-e requires a KEY=VALUE argument")
			}
			kv := args[i+1]
			eq := strings.IndexByte(kv, '=')
			if eq <= 0 {
				return "", nil, nil, fmt.Errorf(
					"-e value %q must be KEY=VALUE with a non-empty KEY", kv)
			}
			if env == nil {
				env = make(map[string]string)
			}
			env[kv[:eq]] = kv[eq+1:]
			i++
			continue
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			return "", nil, nil, fmt.Errorf("unknown flag %q", a)
		}
		break
	}
	if i >= len(args) {
		return "", nil, nil, errors.New("missing <program>")
	}
	program = args[i]
	if i+1 < len(args) {
		progArgs = append([]string{}, args[i+1:]...)
	}
	return program, progArgs, env, nil
}

func (h *Handler) cmdAttach(
	ctx context.Context, args []string, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New(attachUsage)
	}
	if slices.ContainsFunc(args, isConnectEndpoint) {
		return h.cmdAttachConnect(ctx, args, pw)
	}
	sid, err := h.requireInitializedOnly()
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	switch h.phaseLocked() {
	case phaseInitialized:
		// allowed
	case phaseStarted:
		h.mu.Unlock()
		return nil, errNeedConfigured
	case phaseConfigured:
		h.mu.Unlock()
		return nil, errAlreadyConfigured
	default:
		h.mu.Unlock()
		return nil, errNoSession
	}
	h.mu.Unlock()
	aArgs := debugapi.AttachRequestArguments{}
	if pid, err := strconv.Atoi(args[0]); err == nil {
		aArgs.PID = pid
	} else {
		aArgs.Program = args[0]
	}
	// Sink was created at initialize time; this call is a
	// no-op safety net that returns the active path.
	outputPath, err := h.startOutputCapture()
	if err != nil {
		return nil, fmt.Errorf("attach: %w", err)
	}
	initCh := h.armLaunchInit()
	if err := h.dbg.Attach(ctx, sid, aArgs); err != nil {
		h.signalLaunchInit()
		// Leave the sink open: the session is still
		// initialized and the user may retry or terminate.
		return nil, fmt.Errorf("attach: %w", err)
	}
	h.mu.Lock()
	h.phase = phaseStarted
	h.mu.Unlock()
	out := []string{
		fmt.Sprintf("Attach sent for `%s`.", args[0]),
	}
	if outputPath != "" {
		out = append(out, fmt.Sprintf(
			"Debuggee stdout/stderr is being captured at `%s`.",
			outputPath))
	}
	out = append(out,
		"Run `debugger configured` when configuration is complete.")
	return newLaunchIterator(out, initCh), nil
}

const attachUsage = "usage: debugger attach <pid|program> | " +
	"debugger attach <langID> connect://host:port [program]"

// connectEndpointPrefix marks the attach argument that names an
// already listening debug adapter (e.g. one started by
// `python -m debugpy --listen host:port`).
const connectEndpointPrefix = "connect://"

func isConnectEndpoint(arg string) bool {
	return strings.HasPrefix(arg, connectEndpointPrefix)
}

// sessionConnector is the optional debugapi.Debugger capability that
// backs the endpoint form of attach. The production debugger
// (*idedebug.Manager) implements it; a debugger that does not gets a
// clear error instead of a silent fallback.
type sessionConnector interface {
	CreateSessionConnect(
		ctx context.Context, langID, addr string,
		client debugapi.ClientCapabilities, sub debugapi.EventSubscriber,
	) (string, *dap.Capabilities, error)
}

// cmdAttachConnect handles `debugger attach <langID>
// connect://host:port [program]`: it creates the session against the
// listening adapter and sends the attach request in one gesture, so
// the per-session endpoint never has to be written into the
// workspace configuration. It requires that no session is active and
// returns the session event iterator, since no `initialize` ran to
// produce one.
func (h *Handler) cmdAttachConnect(
	ctx context.Context, args []string, pw repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) < 2 || len(args) > 3 || !isConnectEndpoint(args[1]) {
		return nil, errors.New(attachUsage)
	}
	langID := args[0]
	addr := strings.TrimPrefix(args[1], connectEndpointPrefix)
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return nil, fmt.Errorf("%s: %w", attachUsage, err)
	}
	connector, ok := h.dbg.(sessionConnector)
	if !ok {
		return nil, errors.New(
			"attach: connecting to a listening adapter is not supported " +
				"by this debugger")
	}
	aArgs := debugapi.AttachRequestArguments{}
	if len(args) == 3 {
		aArgs.Program = args[2]
	}
	// Detach from the caller's ctx for the same reason as
	// cmdInitialize: Ctrl-C cancels the REPL's per-command context
	// but must not tear down the debug session.
	ctx = context.WithoutCancel(ctx)
	sid, ch, err := h.beginSession(ctx, subAttach,
		func(ctx context.Context) (string, error) {
			sid, _, err := connector.CreateSessionConnect(
				ctx, langID, addr, defaultClientCapabilities(), h)
			return sid, err
		})
	if err != nil {
		return nil, err
	}
	if err := h.dbg.Attach(ctx, sid, aArgs); err != nil {
		// Leave the session up: the user can retry the attach or
		// terminate it.
		return nil, fmt.Errorf("attach: %w", err)
	}
	h.mu.Lock()
	h.phase = phaseStarted
	h.mu.Unlock()
	return newSessionIterator(langID, sid, ch, pw,
		h.clearInstalledLocations), nil
}

func (h *Handler) cmdConfigured(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	h.mu.Lock()
	sid := h.sessionID
	if sid == "" {
		h.mu.Unlock()
		return nil, errNoSession
	}
	switch h.phaseLocked() {
	case phaseInitialized:
		h.mu.Unlock()
		return nil, errNeedStart
	case phaseStarted:
		// allowed
	case phaseConfigured:
		h.mu.Unlock()
		return nil, errAlreadyConfigured
	default:
		h.mu.Unlock()
		return nil, errNoSession
	}
	h.mu.Unlock()
	if err := h.replayBreakpoints(ctx, sid); err != nil {
		return nil, fmt.Errorf("configured breakpoint sync: %w", err)
	}
	if err := h.dbg.ConfigurationDone(ctx, sid); err != nil {
		return nil, fmt.Errorf("configured: %w", err)
	}
	h.mu.Lock()
	h.phase = phaseConfigured
	h.mu.Unlock()
	return lines("Configuration complete. Debuggee may now run."), nil
}

func (h *Handler) cmdTerminate(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireInitializedOnly()
	if err != nil {
		return nil, err
	}
	// Terminate is optional in DAP; some servers (or modes, like
	// `attach`) do not implement it. Fall back to Disconnect so
	// the user is never left with a half-active session that
	// blocks subsequent `debugger initialize`.
	termErr := h.dbg.Terminate(ctx, sid, &dap.TerminateArguments{})
	if termErr == nil {
		// Clear local session state even though the adapter may
		// not fire OnClose (e.g. the debuggee already exited), so
		// the next `debugger initialize` is not rejected as still
		// active.
		h.resetSession()
		return lines("Terminated debug session."), nil
	}
	discErr := h.dbg.Disconnect(ctx, sid, &dap.DisconnectArguments{
		TerminateDebuggee: true,
	})
	// Force-clear local session state on the fallback path so the
	// user can recover by running `debugger initialize` again,
	// regardless of whether the adapter also fires OnClose.
	h.resetSession()
	if discErr != nil {
		return nil, fmt.Errorf(
			"terminate: %v; disconnect fallback: %w", termErr, discErr)
	}
	return lines("Terminated debug session."), nil
}

func (h *Handler) cmdRestart(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	if err := h.dbg.Restart(ctx, sid); err != nil {
		return nil, fmt.Errorf("restart: %w", err)
	}
	return lines("Restarted debug session."), nil
}

// threadID parses an optional thread-id argument or falls back to
// the thread of the most recent StoppedEvent. Only when no stop
// is active does it ask the debugger for the thread list and
// pick threads[0]. This matters under the race detector where
// many runtime/GC goroutines outrank the user's goroutine in
// the `Threads` response.
func (h *Handler) threadID(ctx context.Context, args []string) (int, error) {
	sid, err := h.sessionIDOrError()
	if err != nil {
		return 0, err
	}
	if len(args) > 0 {
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return 0, fmt.Errorf("invalid thread-id %q: %w", args[0], err)
		}
		return id, nil
	}
	h.mu.Lock()
	stoppedID := h.stoppedThreadID
	h.mu.Unlock()
	if stoppedID != 0 {
		return stoppedID, nil
	}
	threads, err := h.dbg.Threads(ctx, sid)
	if err != nil {
		return 0, fmt.Errorf("threads: %w", err)
	}
	if len(threads) == 0 {
		return 0, errors.New("no threads")
	}
	return threads[0].Id, nil
}

func (h *Handler) cmdContinue(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	id, err := h.threadID(ctx, args)
	if err != nil {
		return nil, err
	}
	if _, err := h.dbg.Continue(ctx, sid, &dap.ContinueArguments{ThreadId: id}); err != nil {
		return nil, fmt.Errorf("continue: %w", err)
	}
	return lines(fmt.Sprintf("Continued thread %d.", id)), nil
}

func (h *Handler) cmdNext(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	id, err := h.threadID(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := h.dbg.Next(ctx, sid, &dap.NextArguments{ThreadId: id}); err != nil {
		return nil, fmt.Errorf("next: %w", err)
	}
	return lines(fmt.Sprintf("Stepped over on thread %d.", id)), nil
}

func (h *Handler) cmdStepIn(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	id, err := h.threadID(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := h.dbg.StepIn(ctx, sid, &dap.StepInArguments{ThreadId: id}); err != nil {
		return nil, fmt.Errorf("step-in: %w", err)
	}
	return lines(fmt.Sprintf("Stepped into on thread %d.", id)), nil
}

func (h *Handler) cmdStepOut(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	id, err := h.threadID(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := h.dbg.StepOut(ctx, sid, &dap.StepOutArguments{ThreadId: id}); err != nil {
		return nil, fmt.Errorf("step-out: %w", err)
	}
	return lines(fmt.Sprintf("Stepped out on thread %d.", id)), nil
}

func (h *Handler) cmdStepBack(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	id, err := h.threadID(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := h.dbg.StepBack(ctx, sid, &dap.StepBackArguments{ThreadId: id}); err != nil {
		return nil, fmt.Errorf("step-back: %w", err)
	}
	return lines(fmt.Sprintf("Stepped back on thread %d.", id)), nil
}

func (h *Handler) cmdReverseContinue(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	id, err := h.threadID(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := h.dbg.ReverseContinue(ctx, sid, &dap.ReverseContinueArguments{ThreadId: id}); err != nil {
		return nil, fmt.Errorf("reverse-continue: %w", err)
	}
	return lines(fmt.Sprintf("Reverse-continued on thread %d.", id)), nil
}

func (h *Handler) cmdPause(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	id, err := h.threadID(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := h.dbg.Pause(ctx, sid, &dap.PauseArguments{ThreadId: id}); err != nil {
		return nil, fmt.Errorf("pause: %w", err)
	}
	return lines(fmt.Sprintf("Paused thread %d.", id)), nil
}

func (h *Handler) cmdGoto(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New("usage: debugger goto <target>")
	}
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	target, err := strconv.Atoi(args[0])
	if err != nil {
		return nil, fmt.Errorf("invalid target id %q: %w", args[0], err)
	}
	id, err := h.threadID(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := h.dbg.Goto(ctx, sid, &dap.GotoArguments{
		ThreadId: id,
		TargetId: target,
	}); err != nil {
		return nil, fmt.Errorf("goto: %w", err)
	}
	return lines(fmt.Sprintf("Set thread %d to target %d.", id, target)), nil
}

func (h *Handler) cmdThreads(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	threads, err := h.dbg.Threads(ctx, sid)
	if err != nil {
		return nil, fmt.Errorf("threads: %w", err)
	}
	return renderThreads(threads), nil
}

func (h *Handler) cmdStackTrace(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	id, err := h.threadID(ctx, args)
	if err != nil {
		return nil, err
	}
	resp, err := h.dbg.StackTrace(ctx, sid, &dap.StackTraceArguments{ThreadId: id})
	if err != nil {
		return nil, fmt.Errorf("stack-trace: %w", err)
	}
	return renderStackFrames(resp.StackFrames), nil
}

func (h *Handler) cmdScopes(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	frameID, err := h.topFrameID(ctx, args)
	if err != nil {
		return nil, err
	}
	scopes, err := h.dbg.Scopes(ctx, sid, &dap.ScopesArguments{FrameId: frameID})
	if err != nil {
		return nil, fmt.Errorf("scopes: %w", err)
	}
	return renderScopes(scopes), nil
}

func (h *Handler) cmdVariables(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	var ref int
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil {
			return nil, fmt.Errorf("invalid ref %q: %w", args[0], err)
		}
		ref = n
	} else {
		scope, err := h.topScope(ctx)
		if err != nil {
			return nil, err
		}
		ref = scope.VariablesReference
	}
	vars, err := h.dbg.Variables(ctx, sid, &dap.VariablesArguments{VariablesReference: ref})
	if err != nil {
		return nil, fmt.Errorf("variables: %w", err)
	}
	return renderVariables(vars), nil
}

func (h *Handler) cmdModules(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	resp, err := h.dbg.Modules(ctx, sid, &dap.ModulesArguments{})
	if err != nil {
		return nil, fmt.Errorf("modules: %w", err)
	}
	return renderModules(resp.Modules), nil
}

func (h *Handler) cmdLoadedSources(
	ctx context.Context,
) (iterator.Iterator[component.Responsive], error) {
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	srcs, err := h.dbg.LoadedSources(ctx, sid)
	if err != nil {
		return nil, fmt.Errorf("loaded-sources: %w", err)
	}
	return renderSources(srcs), nil
}

func (h *Handler) cmdSetBreakpointREPL() iterator.Iterator[component.Responsive] {
	return lines(
		"Navigate to the code by placing the cursor where you want to set " +
			"a breakpoint, and invoke the command `debugger set-breakpoint` " +
			"to set a breakpoint.",
	)
}

func (h *Handler) cmdSetVariable(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) < 2 {
		return nil, errors.New("usage: debugger set-variable <name> <expr...>")
	}
	sid, err := h.sessionIDOrError()
	if err != nil {
		return nil, err
	}
	name := args[0]
	value := strings.Join(args[1:], " ")
	scope, err := h.topScope(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := h.dbg.SetVariable(ctx, sid, &dap.SetVariableArguments{
		VariablesReference: scope.VariablesReference,
		Name:               name,
		Value:              value,
	})
	if err != nil {
		return nil, fmt.Errorf("set-variable: %w", err)
	}
	return renderValue(fmt.Sprintf("%s =", name), resp.Value), nil
}

func (h *Handler) cmdSetExpression(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) < 2 {
		return nil, errors.New("usage: debugger set-expression <expr> <value>")
	}
	sid, err := h.sessionIDOrError()
	if err != nil {
		return nil, err
	}
	expr := args[0]
	value := strings.Join(args[1:], " ")
	frameID, err := h.topFrameID(ctx, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.dbg.SetExpression(ctx, sid, &dap.SetExpressionArguments{
		FrameId:    frameID,
		Expression: expr,
		Value:      value,
	})
	if err != nil {
		return nil, fmt.Errorf("set-expression: %w", err)
	}
	return renderValue(fmt.Sprintf("%s =", expr), resp.Value), nil
}

// cmdEvaluate evaluates an arbitrary expression in the context
// of the current top frame using the DAP `evaluate` request
// and renders its result.
func (h *Handler) cmdEvaluate(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New("usage: debugger evaluate <expr...>")
	}
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	expr := strings.Join(args, " ")
	frameID, err := h.topFrameID(ctx, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.dbg.Evaluate(ctx, sid, &dap.EvaluateArguments{
		FrameId:    frameID,
		Expression: expr,
		Context:    "repl",
	})
	if err != nil {
		return nil, fmt.Errorf("evaluate: %w", err)
	}
	return renderValue(fmt.Sprintf("%s =", expr), resp.Result), nil
}

// cmdDisassemble disassembles instructions around the given
// memory reference. Usage: `debugger disassemble <memref> [count]`.
func (h *Handler) cmdDisassemble(
	ctx context.Context, args []string,
) (iterator.Iterator[component.Responsive], error) {
	if len(args) == 0 {
		return nil, errors.New("usage: debugger disassemble <memref> [count]" +
			" — get <memref> from the `ip:` field of `debugger stack-trace`" +
			" (e.g. 0x10b3a40)")
	}
	sid, err := h.requireStartedAndConfigured()
	if err != nil {
		return nil, err
	}
	dArgs := &dap.DisassembleArguments{
		MemoryReference:  args[0],
		InstructionCount: 16,
		ResolveSymbols:   true,
	}
	if len(args) > 1 {
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return nil, fmt.Errorf("invalid count %q: %w", args[1], err)
		}
		dArgs.InstructionCount = n
	}
	insts, err := h.dbg.Disassemble(ctx, sid, dArgs)
	if err != nil {
		return nil, fmt.Errorf("disassemble: %w", err)
	}
	return renderDisassembly(insts), nil
}

// topFrameID returns the frame ID to use for scope/expression
// queries. If args contains a frame id, it is parsed and
// returned. Otherwise the frame currently selected by
// `debugger jump` on the most recent stop is used, falling
// back to the top frame of the current thread when no stop is
// active.
func (h *Handler) topFrameID(ctx context.Context, args []string) (int, error) {
	sid, err := h.sessionIDOrError()
	if err != nil {
		return 0, err
	}
	if len(args) > 0 {
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return 0, fmt.Errorf("invalid frame-id %q: %w", args[0], err)
		}
		return id, nil
	}
	h.mu.Lock()
	if len(h.stoppedFrames) > 0 &&
		h.stoppedFrame >= 0 && h.stoppedFrame < len(h.stoppedFrames) {
		id := h.stoppedFrames[h.stoppedFrame].Id
		h.mu.Unlock()
		return id, nil
	}
	h.mu.Unlock()
	id, err := h.threadID(ctx, nil)
	if err != nil {
		return 0, err
	}
	resp, err := h.dbg.StackTrace(ctx, sid, &dap.StackTraceArguments{
		ThreadId:   id,
		StartFrame: 0,
		Levels:     1,
	})
	if err != nil {
		return 0, fmt.Errorf("stack-trace: %w", err)
	}
	if len(resp.StackFrames) == 0 {
		return 0, errors.New("no stack frames")
	}
	return resp.StackFrames[0].Id, nil
}

// topScope returns the first (top) scope of the top frame of the
// first thread.
func (h *Handler) topScope(ctx context.Context) (dap.Scope, error) {
	sid, err := h.sessionIDOrError()
	if err != nil {
		return dap.Scope{}, err
	}
	frameID, err := h.topFrameID(ctx, nil)
	if err != nil {
		return dap.Scope{}, err
	}
	scopes, err := h.dbg.Scopes(ctx, sid, &dap.ScopesArguments{FrameId: frameID})
	if err != nil {
		return dap.Scope{}, fmt.Errorf("scopes: %w", err)
	}
	if len(scopes) == 0 {
		return dap.Scope{}, errors.New("no scopes")
	}
	return scopes[0], nil
}
