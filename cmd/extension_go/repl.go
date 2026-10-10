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

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/extension/extutil"
	"unstable.build/rune/internal/handler/command"
	"unstable.build/rune/internal/ide/console/ideconsole"
	"unstable.build/rune/internal/text"
	"unstable.build/rune/internal/text/standard"
)

// replTabURI is the stable URI used to identify the single Go REPL tab.
// Re-invoking `go repl` re-focuses the existing tab instead of opening
// a new one.
const replTabURI = "rune://go/repl"

// replHistoryDocumentID is the storage key under which the Go REPL's
// command history is persisted so reverse-search (<c-r>) and up/down
// recall survive across sessions.
const replHistoryDocumentID = "go-repl-history"

// replMaxHistory caps the number of persisted REPL history entries.
const replMaxHistory = 1000

// newREPLSubcommand returns the `go repl` subcommand handler.
func newREPLSubcommand(
	wm browserapi.WindowManager,
	interrupter term.Interrupter,
	fs workspaceapi.FileSystem,
	executor workspaceapi.Executor,
	lsp semanticapi.LSP,
	cfg config.Config,
	storage storageapi.Service,
) textapi.CommandHandler {
	return &replSubcommand{
		wm:          wm,
		interrupter: interrupter,
		fs:          fs,
		executor:    executor,
		lsp:         lsp,
		cfg:         cfg,
		storage:     storage,
	}
}

var _ textapi.CommandHandler = (*replSubcommand)(nil)

type replSubcommand struct {
	wm          browserapi.WindowManager
	interrupter term.Interrupter
	fs          workspaceapi.FileSystem
	executor    workspaceapi.Executor
	lsp         semanticapi.LSP
	cfg         config.Config
	storage     storageapi.Service

	mu     sync.Mutex
	handle browserapi.Handler
}

func (h *replSubcommand) HandleCommand(
	ctx context.Context, cmd textapi.Command,
) error {
	win, err := h.wm.Focus()
	if err != nil {
		return fmt.Errorf("focus window: %w", err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if h.handle != nil {
		return h.wm.SetWindowContent(win, h.handle)
	}

	moduleDir := h.resolveModuleDir(cmd)
	runner := &executorRunner{
		executor:   h.executor,
		fs:         h.fs,
		moduleDir:  moduleDir,
		programDir: replProgramDir(moduleDir),
	}
	session := newGoSession(runner, h.fs, h.lsp)

	moduleURI, err := h.fs.URI(moduleDir)
	if err != nil {
		return fmt.Errorf("resolve module uri: %w", err)
	}

	sched := newTickScheduler(h.interrupter)
	editor, modal := h.resolveEditor()
	shell, registry := ideconsole.New(
		sched.schedule,
		h.interrupter,
		editor,
		h.shellConfig(session, moduleURI, modal),
	)
	if err := registry.RegisterREPLCommand(
		textapi.CommandManual{
			Name:    "go",
			Summary: "Evaluate Go expressions and statements",
		},
		session,
	); err != nil {
		_ = shell.Close()
		return fmt.Errorf("register go repl command: %w", err)
	}

	uri, err := h.fs.URI(replTabURI)
	if err != nil {
		return fmt.Errorf("resolve repl uri: %w", err)
	}

	wrapped := &drainHandler{Handler: shell, sched: sched}
	handle, err := h.wm.Tab(uri, '󰟓', "go repl", browserapi.FuncHandler(wrapped, func() error {
		h.mu.Lock()
		h.handle = nil
		h.mu.Unlock()
		_ = os.RemoveAll(runner.programDir)
		return shell.Close()
	}))
	if err != nil {
		return fmt.Errorf("create repl tab: %w", err)
	}
	h.handle = handle

	return h.wm.SetWindowContent(win, handle)
}

// resolveEditor builds the command.Editor that backs the shell input
// line from the configured compose editor, reporting whether it is
// modal. It falls back to a bare modeless editor when the workspace
// config is unavailable, mirroring dialoguetui.
func (h *replSubcommand) resolveEditor() (command.Editor, bool) {
	if h.cfg == nil {
		return commandEditor{te: standard.Editor()}, false
	}
	clip, err := extutil.Clipboard(h.cfg)
	if err != nil {
		return commandEditor{te: standard.Editor()}, false
	}
	te, err := extutil.Editor(clip, h.cfg)
	if err != nil {
		return commandEditor{te: standard.Editor()}, false
	}
	modal, err := extutil.EditorModal(h.cfg)
	if err != nil {
		modal = false
	}
	return commandEditor{te: te}, modal
}

func (h *replSubcommand) Complete(
	context.Context, string, []string,
) (iterator.Iterator[string], error) {
	return iterator.Empty[string](), nil
}

// shellConfig builds the ideconsole configuration for the Go REPL. It
// wires persistent history so reverse-search and recall work, and runs
// the shell as a pure language REPL by routing every line to session.
func (h *replSubcommand) shellConfig(
	session *goSession, moduleURI workspaceapi.URI, modal bool,
) ideconsole.Config {
	return ideconsole.Config{
		DisableShellInterpreter: session,
		Prompt:                  "go> ",
		Modal:                   modal,
		ModalStartInsert:        modal,
		Workspace:               moduleURI,
		Storage:                 h.storage,
		HistoryDocumentID:       replHistoryDocumentID,
		MaxHistory:              replMaxHistory,
		ClearHook:               session.reset,
	}
}

// commandEditor adapts a text.Editor to the command.Editor interface
// ideconsole.New expects. text.Handler (returned by text.Editor.Edit)
// already satisfies command.EditHandler, so the bridge is a thin
// per-buffer Edit call against a fresh in-memory URI.
type commandEditor struct {
	te text.Editor
}

var _ command.Editor = commandEditor{}

func (e commandEditor) Edit(buf *cell.Buffer) command.EditHandler {
	uri := workspaceapi.RandomURI("memory")
	h, err := e.te.Edit(context.Background(), uri, buf, false, false)
	if err != nil {
		// A fresh in-memory buffer never fails to open; fall back to a
		// modeless editor so the shell still has a usable input line.
		h, _ = standard.Editor().Edit(context.Background(), uri, buf, false, false)
	}
	return h
}

// resolveModuleDir picks the directory in which `go run` executes so the
// session resolves the project's go.mod dependencies. It prefers the
// nearest go.mod walking up from the focused file, falling back to the
// workspace root.
func (h *replSubcommand) resolveModuleDir(cmd textapi.Command) string {
	if cmd.Resource != nil {
		if dir, ok := nearestModuleDir(filepath.Dir(cmd.URI.Path())); ok {
			return dir
		}
	}
	if root, err := h.fs.URI("."); err == nil {
		if dir, ok := nearestModuleDir(root.Path()); ok {
			return dir
		}
		return root.Path()
	}
	return "."
}

// nearestModuleDir walks up from start to the nearest directory holding
// a go.mod file.
func nearestModuleDir(start string) (string, bool) {
	dir := start
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// replProgramDir returns a unique directory under the module root that
// holds the REPL's synthesized program. It is a subdirectory (its own
// package) so it never conflicts with a package main at the module root,
// yet stays inside the module so the dependency graph resolves. The same
// directory backs both `go run` and gopls completion.
func replProgramDir(moduleDir string) string {
	return filepath.Join(
		moduleDir, ".rune", "cache",
		fmt.Sprintf("go-repl-%d", rand.Int63()),
	)
}

// tickScheduler implements the scheduleNextTick contract repl.Handler
// expects. Extensions are not handed the raw event-loop tick scheduler,
// so we enqueue callbacks and wake the loop with an interrupt; the
// drainHandler drains the queue at the top of every Handle/Draw.
type tickScheduler struct {
	interrupter term.Interrupter
	mu          sync.Mutex
	queue       []func()
}

func newTickScheduler(interrupter term.Interrupter) *tickScheduler {
	return &tickScheduler{interrupter: interrupter}
}

func (s *tickScheduler) schedule(fn func()) bool {
	s.mu.Lock()
	s.queue = append(s.queue, fn)
	s.mu.Unlock()
	_ = s.interrupter.Interrupt(context.Background())
	return true
}

func (s *tickScheduler) drain() {
	s.mu.Lock()
	pending := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, fn := range pending {
		fn()
	}
}

// drainHandler drains scheduled callbacks before delegating to the
// embedded ideconsole.Handler so async command output produced off the
// event loop is applied before the next Handle/Draw. Extensions only
// have the interrupter, not the real event-loop tick scheduler, so the
// queue must be drained here rather than by the host.
type drainHandler struct {
	*ideconsole.Handler
	sched *tickScheduler
}

var _ tui.Handler = (*drainHandler)(nil)

func (h *drainHandler) Handle(ev term.Event) (exit, handled bool) {
	h.sched.drain()
	return h.Handler.Handle(ev)
}

func (h *drainHandler) Draw(w term.Writer) {
	h.sched.drain()
	h.Handler.Draw(w)
}

// programRunner renders+runs an accumulated session program and returns
// its combined output. It is abstracted so the session model can be unit
// tested without invoking the toolchain.
type programRunner interface {
	run(ctx context.Context, program string) (output string, err error)
}

// executorRunner runs a rendered program with `go run` via the workspace
// Executor. The program lives in its own directory under the module
// (programDir) so it is a self-contained package: it never conflicts
// with a package main at the module root, while still resolving the
// module's dependencies because the nearest enclosing go.mod is the
// project's. The same main.go is reused for completion (see
// repl_complete.go) so gopls and the build share it on disk.
type executorRunner struct {
	executor   workspaceapi.Executor
	fs         workspaceapi.FileSystem
	moduleDir  string
	programDir string
}

var _ programRunner = (*executorRunner)(nil)

func (r *executorRunner) run(
	ctx context.Context, program string,
) (string, error) {
	if err := r.writeProgram(program); err != nil {
		return "", err
	}

	var stdout, stderr bytes.Buffer
	doneCh := make(chan error, 1)
	goCmd := workspaceapi.Cmd{
		Path:    "go",
		Args:    []string{"run", "."},
		Dir:     r.programDir,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Watcher: workspaceapi.ChanProcessWatcher(doneCh),
	}
	if _, err := r.executor.Start(ctx, goCmd); err != nil {
		return "", fmt.Errorf("start go run: %w", err)
	}

	var runErr error
	select {
	case runErr = <-doneCh:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if runErr != nil {
		return strings.TrimRight(stderr.String(), "\n"), runErr
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

const programFile = "main.go"

func (r *executorRunner) writeProgram(program string) error {
	if err := os.MkdirAll(r.programDir, 0o755); err != nil {
		return fmt.Errorf("create program dir: %w", err)
	}
	if err := os.WriteFile(
		filepath.Join(r.programDir, programFile), []byte(program), 0o644,
	); err != nil {
		return fmt.Errorf("write program: %w", err)
	}
	return nil
}

var _ completionRunner = (*executorRunner)(nil)

// programPath writes content to the shared program file and returns its
// absolute path so the session can open it as a gopls overlay.
func (r *executorRunner) programPath(content string) (string, error) {
	if err := r.writeProgram(content); err != nil {
		return "", err
	}
	return filepath.Join(r.programDir, programFile), nil
}

var _ docRunner = (*executorRunner)(nil)

func (r *executorRunner) runDoc(ctx context.Context, arg string) (string, error) {
	var stdout, stderr bytes.Buffer
	doneCh := make(chan error, 1)
	goCmd := workspaceapi.Cmd{
		Path:    "go",
		Args:    []string{"doc", arg},
		Dir:     r.moduleDir,
		Stdout:  &stdout,
		Stderr:  &stderr,
		Watcher: workspaceapi.ChanProcessWatcher(doneCh),
	}
	if _, err := r.executor.Start(ctx, goCmd); err != nil {
		return "", fmt.Errorf("start go doc: %w", err)
	}
	var runErr error
	select {
	case runErr = <-doneCh:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if runErr != nil {
		return strings.TrimRight(stderr.String(), "\n"), runErr
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func stringRows(lines ...string) iterator.Iterator[component.Responsive] {
	rows := make([]component.Responsive, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, component.NewResponsiveString(line, component.StringResponsiveConfig{}))
	}
	return iterator.FromSlice(rows)
}

// errIncomplete signals that a snippet is merely unfinished, so the REPL
// should keep buffering more input rather than surfacing a syntax error.
var errIncomplete = errors.New("incomplete input")

// endsAtEOF reports whether the first parser error is caused by the parser
// running out of input (its found token is EOF). go/parser sorts errors by
// position, so the first entry is the earliest failure. An EOF failure
// means every token the user typed was accepted and only the missing tail
// is at fault, i.e. the snippet is unfinished rather than wrong.
func endsAtEOF(err error) bool {
	var list scanner.ErrorList
	if !errors.As(err, &list) || len(list) == 0 {
		return false
	}
	return strings.HasSuffix(list[0].Msg, "found 'EOF'")
}

// gofmt formats a snippet of Go source, returning the input unchanged if
// it cannot be formatted.
func gofmt(src string) string {
	out, err := format.Source([]byte(src))
	if err != nil {
		return src
	}
	return string(out)
}

// goSession is the gore-style accumulated program model. Each accepted
// input folds into the import set, top-level declarations, or the body
// of main; on every evaluation a complete program is rendered and run.
type goSession struct {
	runner programRunner
	fs     workspaceapi.FileSystem
	lsp    semanticapi.LSP

	imports map[string]struct{}
	decls   []string
	stmts   []string
	// declared lists names introduced by accumulated `:=` statements.
	// Each render emits `_ = name` so a variable that is declared but
	// not yet referenced by a later line still compiles.
	declared []string

	// pending buffers a multi-line snippet until it parses.
	pending []string
}

var _ textapi.REPLHandler = (*goSession)(nil)

func newGoSession(
	runner programRunner, fs workspaceapi.FileSystem, lsp semanticapi.LSP,
) *goSession {
	return &goSession{
		runner:  runner,
		fs:      fs,
		lsp:     lsp,
		imports: map[string]struct{}{},
	}
}

func (s *goSession) HandleCommand(
	ctx context.Context, cmd repl.Command, _ repl.ProgressWriter,
) (iterator.Iterator[component.Responsive], error) {
	line := rejoin(cmd)
	if strings.HasPrefix(line, builtinPrefix) {
		s.pending = nil
		return s.builtin(ctx, line)
	}

	if len(s.pending) > 0 {
		line = strings.Join(s.pending, "\n") + "\n" + line
	}

	snippet := strings.TrimSpace(line)
	if snippet == "" {
		s.pending = nil
		return stringRows(), nil
	}

	frag, err := classify(snippet)
	if err != nil {
		if errors.Is(err, errIncomplete) {
			s.pending = append(s.pending, strings.TrimRight(rejoin(cmd), "\n"))
			return stringRows(), nil
		}
		s.pending = nil
		return nil, err
	}
	s.pending = nil
	return s.eval(ctx, frag)
}

// Help satisfies textapi.REPLHandler, returning the REPL's built-in
// command reference (the same content as `/help`).
func (s *goSession) Help(
	_ context.Context, _ []string,
) (iterator.Iterator[component.Responsive], error) {
	return stringRows(replHelp()...), nil
}

// reset discards all accumulated session state so the next input starts
// a fresh program. It backs both the /clear builtin and the shell's
// screen-clear (<c-l>) hook wired via ideconsole.Config.ClearHook.
func (s *goSession) reset() {
	s.imports = map[string]struct{}{}
	s.decls = nil
	s.stmts = nil
	s.declared = nil
	s.pending = nil
}

// rejoin reconstructs the raw input line from the repl.Command, which
// pre-splits on spaces.
func rejoin(cmd repl.Command) string {
	if len(cmd.Args) == 0 {
		return cmd.Name
	}
	return cmd.Name + " " + strings.Join(cmd.Args, " ")
}

// fragmentKind classifies a parsed input snippet.
type fragmentKind int

const (
	fragImport fragmentKind = iota
	fragDecl
	fragStmt
	fragExpr
)

type fragment struct {
	kind    fragmentKind
	text    string
	imports []string
	// call is set when a fragExpr is a function/method call. Such a
	// call may be void or multi-valued, so it cannot be wrapped in a
	// single-value print context; it is run as a bare statement first
	// and only printed if that fails (a single-value call).
	call bool
	// assigns lists the non-blank identifiers a statement assigns via
	// := so the session can print them and mark them used (a bare
	// `x := 1` is otherwise rejected as "declared and not used").
	assigns []string
}

// classify parses snippet and decides how it folds into the session.
func classify(snippet string) (fragment, error) {
	if paths, ok := parseImport(snippet); ok {
		return fragment{kind: fragImport, imports: paths}, nil
	}
	if isTopLevelDecl(snippet) {
		return fragment{kind: fragDecl, text: snippet}, nil
	}
	if expr, isCall := parseExpr(snippet); expr != "" {
		return fragment{kind: fragExpr, text: expr, call: isCall}, nil
	}
	if err := parseStmts(snippet); err != nil {
		return fragment{}, err
	}
	return fragment{kind: fragStmt, text: snippet, assigns: assignedNames(snippet)}, nil
}

// parseImport recognizes a bare `import` declaration and returns the
// imported paths.
func parseImport(snippet string) ([]string, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", "package p\n"+snippet, parser.ImportsOnly)
	if err != nil || len(file.Imports) == 0 {
		return nil, false
	}
	if !strings.HasPrefix(strings.TrimSpace(snippet), "import") {
		return nil, false
	}
	paths := make([]string, 0, len(file.Imports))
	for _, imp := range file.Imports {
		spec := imp.Path.Value
		if imp.Name != nil {
			spec = imp.Name.Name + " " + spec
		}
		paths = append(paths, spec)
	}
	return paths, true
}

// isTopLevelDecl reports whether snippet parses as one or more top-level
// declarations (func/type/const/var at file scope).
func isTopLevelDecl(snippet string) bool {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", "package p\n"+snippet, parser.SkipObjectResolution)
	if err != nil || len(file.Decls) == 0 {
		return false
	}
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
		case *ast.GenDecl:
			if decl.Tok == token.IMPORT {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// parseExpr returns the formatted expression text if snippet is a single
// expression, or "" otherwise. The second return reports whether the
// expression is a function/method call.
func parseExpr(snippet string) (string, bool) {
	expr, err := parser.ParseExpr(snippet)
	if err != nil {
		return "", false
	}
	var buf bytes.Buffer
	if format.Node(&buf, token.NewFileSet(), expr) != nil {
		return "", false
	}
	_, isCall := expr.(*ast.CallExpr)
	return buf.String(), isCall
}

// parseStmts validates snippet as a statement list inside a function. A
// snippet that fails only because the parser ran out of input (an
// unclosed brace, paren, bracket, or a partial declaration) returns
// errIncomplete so the REPL keeps buffering; a snippet with a genuine
// syntax error returns that error.
func parseStmts(snippet string) error {
	src := "package p\nfunc _() {\n" + snippet + "\n}\n"
	_, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err == nil {
		return nil
	}
	if snippetIncomplete(snippet) {
		return errIncomplete
	}
	return err
}

// assignedNames returns the non-blank identifiers a single short-variable
// declaration (`:=`) introduces, so the session can print and mark them
// used. It returns nil for any other statement (plain assignment, call,
// control flow), which do not declare new names.
func assignedNames(snippet string) []string {
	src := "package p\nfunc _() {\n" + snippet + "\n}\n"
	file, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	fn, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok || fn.Body == nil {
		return nil
	}
	var names []string
	for _, stmt := range fn.Body.List {
		assign, ok := stmt.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE {
			continue
		}
		for _, lhs := range assign.Lhs {
			if id, ok := lhs.(*ast.Ident); ok && id.Name != "_" {
				names = append(names, id.Name)
			}
		}
	}
	return names
}

// snippetIncomplete reports whether snippet is merely unfinished. It tries
// two interpretations without a synthetic terminator — a statement body
// and a top-level declaration — and treats the input as incomplete if
// either runs off the end of input (found 'EOF'). Garbage input instead
// fails on an unexpected token mid-input.
func snippetIncomplete(snippet string) bool {
	stmtSrc := "package p\nfunc _() {\n" + snippet + "\n"
	declSrc := "package p\n" + snippet + "\n"
	for _, src := range []string{stmtSrc, declSrc} {
		_, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
		if err != nil && endsAtEOF(err) {
			return true
		}
	}
	return false
}

// eval tentatively folds frag into the session, renders the program, and
// runs it. On success the fold is committed; on a compile/run failure it
// is rolled back and the error surfaced.
func (s *goSession) eval(
	ctx context.Context, frag fragment,
) (iterator.Iterator[component.Responsive], error) {
	switch frag.kind {
	case fragImport:
		added := s.addImports(frag.imports)
		program := s.render("")
		if out, err := s.runner.run(ctx, program); err != nil {
			s.removeImports(added)
			return nil, runFailure(out, err)
		}
		return stringRows(), nil

	case fragDecl:
		s.decls = append(s.decls, frag.text)
		program := s.render("")
		if out, err := s.runner.run(ctx, program); err != nil {
			s.decls = s.decls[:len(s.decls)-1]
			return nil, runFailure(out, err)
		}
		return stringRows(), nil

	case fragStmt:
		s.stmts = append(s.stmts, frag.text)
		addedDecls := s.addDeclared(frag.assigns)
		program := s.renderProgramSource(printStmt(frag.assigns), len(frag.assigns) > 0)
		out, err := s.runner.run(ctx, program)
		if err != nil {
			s.stmts = s.stmts[:len(s.stmts)-1]
			s.removeDeclared(addedDecls)
			return nil, runFailure(out, err)
		}
		return outputRows(out), nil

	default: // fragExpr
		out, err := s.runner.run(ctx, s.render(frag.text))
		if err == nil {
			// A call may mutate state (e.g. buf.WriteString), so it must
			// be folded into the program to persist its side effects for
			// later evaluations. A non-call expression has no side effect
			// and stays ephemeral so it is not needlessly replayed.
			if frag.call {
				s.stmts = append(s.stmts, frag.text)
			}
			return outputRows(out), nil
		}
		if frag.call {
			// A void call has no value to hand the printer, so the
			// printer form fails to compile. Re-run it as a bare
			// statement so its side effects still execute (e.g. a
			// function that only prints).
			if stmtOut, stmtErr := s.runner.run(ctx, s.renderStmt(frag.text)); stmtErr == nil {
				s.stmts = append(s.stmts, frag.text)
				return outputRows(stmtOut), nil
			}
		}
		return nil, runFailure(out, err)
	}
}

func (s *goSession) addImports(specs []string) []string {
	added := make([]string, 0, len(specs))
	for _, spec := range specs {
		if _, ok := s.imports[spec]; ok {
			continue
		}
		s.imports[spec] = struct{}{}
		added = append(added, spec)
	}
	return added
}

func (s *goSession) removeImports(specs []string) {
	for _, spec := range specs {
		delete(s.imports, spec)
	}
}

// addDeclared records names newly introduced by a `:=` statement and
// returns the subset it actually added (already-declared names are
// skipped) so a failed statement can roll them back.
func (s *goSession) addDeclared(names []string) []string {
	var added []string
	for _, name := range names {
		if slices.Contains(s.declared, name) {
			continue
		}
		s.declared = append(s.declared, name)
		added = append(added, name)
	}
	return added
}

func (s *goSession) removeDeclared(names []string) {
	if len(names) == 0 {
		return
	}
	s.declared = slices.DeleteFunc(s.declared, func(n string) bool {
		return slices.Contains(names, n)
	})
}

// printStmt builds the trailing line that prints the freshly assigned
// names, or "" when the statement assigns nothing. Each name is printed
// by its own call so independently declared variables appear on separate
// lines rather than grouped as a single call's return tuple.
func printStmt(names []string) string {
	if len(names) == 0 {
		return ""
	}
	calls := make([]string, len(names))
	for i, name := range names {
		calls[i] = printerName + "(" + name + ")"
	}
	return strings.Join(calls, "; ")
}

// printerName is the name of the variadic helper that pretty-prints an
// evaluated expression. Naming it with a leading underscore-ish prefix
// keeps it from colliding with user identifiers.
const printerName = "__repl_print"

// printerDecl declares the variadic printer. Passing an expression as
// its sole argument spreads a single- or multi-valued call across its
// parameters (Go's f(g()) rule), so the same wrapper prints scalars,
// multi-return calls, and the results of side-effecting calls uniformly.
// A single value prints bare; a multi-valued call's results print as a
// parenthesized tuple so they read as one return value rather than
// separate outputs. The rendered source is gofmt'd before use.
const printerDecl = "func " + printerName + "(xs ...any) {" +
	"if len(xs) == 1 { fmt.Printf(\"%#v\\n\", xs[0]); return };" +
	"fmt.Print(\"(\");" +
	"for i, x := range xs { if i > 0 { fmt.Print(\", \") }; fmt.Printf(\"%#v\", x) };" +
	"fmt.Println(\")\") }"

// render builds a complete Go program from the session state. lastExpr,
// when non-empty, is printed as the final value via the variadic
// printer.
func (s *goSession) render(lastExpr string) string {
	if lastExpr == "" {
		return s.renderProgramSource("", false)
	}
	return s.renderProgramSource(printerName+"("+lastExpr+")", true)
}

// renderStmt builds a program whose final line is stmt run for its side
// effects rather than printed. It backs the void-call fallback, where an
// expression has no value to hand the printer.
func (s *goSession) renderStmt(stmt string) string {
	return s.renderProgramSource(stmt, false)
}

// renderProgramSource builds a complete Go program from the session
// state with trailing appended as the last line inside main, verbatim.
// When usesPrinter is set the variadic printer is declared and fmt is
// forced into the import set so the printer body compiles.
func (s *goSession) renderProgramSource(trailing string, usesPrinter bool) string {
	var b strings.Builder
	b.WriteString("package main\n\n")

	specs := s.sortedImports()
	if usesPrinter {
		// The printer body references fmt, so it must be imported (and
		// as a normal import, not blank) even when the user never did.
		specs = ensureSpec(specs, `"fmt"`)
	}
	if len(specs) > 0 {
		body := s.importBody(trailing)
		if usesPrinter {
			body += "\nfmt.Printf"
		}
		b.WriteString("import (\n")
		for _, spec := range specs {
			b.WriteString("\t" + renderImport(spec, body) + "\n")
		}
		b.WriteString(")\n\n")
	}

	if usesPrinter {
		b.WriteString(printerDecl)
		b.WriteString("\n\n")
	}

	for _, decl := range s.decls {
		b.WriteString(decl)
		b.WriteString("\n\n")
	}

	b.WriteString("func main() {\n")
	for _, stmt := range s.stmts {
		b.WriteString(stmt + "\n")
	}
	for _, name := range s.declared {
		b.WriteString("\t_ = " + name + "\n")
	}
	if trailing != "" {
		b.WriteString("\t" + trailing + "\n")
	}
	b.WriteString("}\n")

	return gofmt(b.String())
}

// ensureSpec returns specs with want inserted in sorted order if it is
// not already present.
func ensureSpec(specs []string, want string) []string {
	if slices.Contains(specs, want) {
		return specs
	}
	specs = append(specs, want)
	sort.Strings(specs)
	return specs
}

// importBody returns the source the import set must satisfy: all decls,
// stmts, and the trailing line. renderImport scans it to decide whether
// an import is referenced.
func (s *goSession) importBody(trailing string) string {
	var b strings.Builder
	for _, decl := range s.decls {
		b.WriteString(decl)
		b.WriteByte('\n')
	}
	for _, stmt := range s.stmts {
		b.WriteString(stmt)
		b.WriteByte('\n')
	}
	b.WriteString(trailing)
	return b.String()
}

// renderImport renders one import spec, demoting it to a blank import
// (`_ "path"`) when body does not reference its package identifier.
// A blank import compiles even when unused, so accumulating imports
// never trips the "imported and not used" error, while an invalid path
// still fails because the package must resolve. An import that is
// actually used stays a normal import so its selector resolves.
func renderImport(spec, body string) string {
	if strings.HasPrefix(spec, "_ ") || strings.HasPrefix(spec, ". ") {
		return spec
	}
	ident := importIdent(spec)
	if ident != "" && referencesPackage(body, ident) {
		return spec
	}
	return "_ " + importPath(spec)
}

// importPath returns the quoted path portion of an import spec, dropping
// any alias.
func importPath(spec string) string {
	if i := strings.LastIndex(spec, " "); i >= 0 {
		return spec[i+1:]
	}
	return spec
}

// importIdent returns the package identifier an import is referenced by:
// its alias when present, otherwise the last segment of the import path.
func importIdent(spec string) string {
	path := strings.Trim(importPath(spec), `"`)
	if i := strings.LastIndex(spec, " "); i >= 0 {
		return spec[:i]
	}
	if j := strings.LastIndex(path, "/"); j >= 0 {
		return path[j+1:]
	}
	return path
}

// referencesPackage reports whether body uses ident as a package
// qualifier (an `ident.` selector with no identifier character before
// it), which is the only way REPL input references an imported package.
func referencesPackage(body, ident string) bool {
	needle := ident + "."
	for i := strings.Index(body, needle); i >= 0; {
		if i == 0 || !isIdentByte(body[i-1]) {
			return true
		}
		next := strings.Index(body[i+1:], needle)
		if next < 0 {
			break
		}
		i += next + 1
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' ||
		('a' <= b && b <= 'z') ||
		('A' <= b && b <= 'Z') ||
		('0' <= b && b <= '9')
}

func (s *goSession) sortedImports() []string {
	specs := make([]string, 0, len(s.imports))
	for spec := range s.imports {
		specs = append(specs, spec)
	}
	sort.Strings(specs)
	return specs
}

// renderProgram returns the current accumulated program (no trailing
// expression) for `/print` / `/write`.
func (s *goSession) renderProgram() string {
	return s.render("")
}

// builtinPrefix marks a REPL meta-command (e.g. /help). A Go statement
// cannot start with it, so it unambiguously separates builtins from
// evaluated source.
const builtinPrefix = "/"

func (s *goSession) builtin(
	ctx context.Context, line string,
) (iterator.Iterator[component.Responsive], error) {
	name, arg, _ := strings.Cut(strings.TrimPrefix(line, builtinPrefix), " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "type":
		if arg == "" {
			return nil, errors.New("/type requires an expression")
		}
		return s.evalType(ctx, arg)
	case "print":
		return outputRows(s.renderProgram()), nil
	case "write":
		return s.write(arg)
	case "clear":
		s.reset()
		return stringRows("session cleared"), nil
	case "doc":
		if arg == "" {
			return nil, errors.New("/doc requires an argument")
		}
		return s.doc(ctx, arg)
	case "help":
		return stringRows(replHelp()...), nil
	case "quit", "exit":
		return stringRows("close the tab to exit the REPL"), nil
	default:
		return nil, fmt.Errorf("unknown command: /%s", name)
	}
}

// evalType prints the runtime type of an expression by rendering a probe
// program that reflects on it.
func (s *goSession) evalType(
	ctx context.Context, expr string,
) (iterator.Iterator[component.Responsive], error) {
	s.imports["\"reflect\""] = struct{}{}
	probe := fmt.Sprintf("reflect.TypeOf(%s)", expr)
	program := s.render(probe)
	out, err := s.runner.run(ctx, program)
	delete(s.imports, "\"reflect\"")
	if err != nil {
		return nil, runFailure(out, err)
	}
	return outputRows(out), nil
}

func (s *goSession) write(arg string) (iterator.Iterator[component.Responsive], error) {
	if arg == "" {
		return nil, errors.New("/write requires a file path")
	}
	f, err := s.fs.OpenFile(arg, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", arg, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write([]byte(s.renderProgram())); err != nil {
		return nil, fmt.Errorf("write %s: %w", arg, err)
	}
	return stringRows("wrote " + arg), nil
}

// doc runs `go doc <arg>` via the runner. The session runner only renders
// programs, so doc is delegated to runners that also implement docRunner.
func (s *goSession) doc(
	ctx context.Context, arg string,
) (iterator.Iterator[component.Responsive], error) {
	dr, ok := s.runner.(docRunner)
	if !ok {
		return nil, errors.New("/doc is not supported in this session")
	}
	out, err := dr.runDoc(ctx, arg)
	if err != nil {
		return nil, runFailure(out, err)
	}
	return outputRows(out), nil
}

func (s *goSession) Complete(
	ctx context.Context, cmd string, args []string,
) (iterator.Iterator[string], error) {
	if strings.HasPrefix(cmd, builtinPrefix) {
		if len(args) == 0 {
			return iterator.FromSlice(completeBuiltins(cmd)), nil
		}
		return iterator.Empty[string](), nil
	}
	line := rejoinArgs(cmd, args)
	if prefix, ok := importPathPrefix(line); ok {
		return s.completePackages(ctx, prefix)
	}
	return s.completeGo(ctx, line)
}

// completePackages lists importable package paths from gopls, keeping
// only those that start with prefix. gopls returns the full known set
// unfiltered, which is too large to surface raw, so the partial path the
// user has typed inside the import quotes narrows it. gopls resolves the
// known-package set relative to a file in the target module, so the
// session's synthetic program file is written and passed as the URI
// argument; without it gopls returns nothing.
func (s *goSession) completePackages(
	ctx context.Context, prefix string,
) (iterator.Iterator[string], error) {
	if s.lsp == nil {
		return iterator.Empty[string](), nil
	}
	cr, ok := s.runner.(completionRunner)
	if !ok {
		return iterator.Empty[string](), nil
	}
	src := s.renderProgram()
	path, err := cr.programPath(src)
	if err != nil {
		return iterator.Empty[string](), nil
	}
	uri, err := s.fs.URI(path)
	if err != nil {
		return iterator.Empty[string](), nil
	}
	lspURI := "file://" + uri.Path()
	if err := s.lsp.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
		TextDocument: semanticapi.TextDocumentItem{
			URI:        lspURI,
			LanguageID: completionLanguageID,
			Version:    1,
			Text:       src,
		},
	}); err != nil {
		return iterator.Empty[string](), nil
	}
	defer func() {
		_ = s.lsp.DidClose(ctx, semanticapi.DidCloseTextDocumentParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: lspURI},
		})
	}()
	arg, err := json.Marshal(map[string]string{"URI": lspURI})
	if err != nil {
		return iterator.Empty[string](), nil
	}
	result, err := s.lsp.ExecuteCommand(ctx, semanticapi.ExecuteCommandParams{
		Command:   "gopls.list_known_packages",
		Arguments: []json.RawMessage{arg},
	})
	if err != nil || result == "" {
		return iterator.Empty[string](), nil
	}
	var resp struct {
		Packages []string `json:"Packages"`
	}
	if json.Unmarshal([]byte(result), &resp) != nil {
		return iterator.Empty[string](), nil
	}
	matches := make([]string, 0, len(resp.Packages))
	for _, pkg := range resp.Packages {
		if strings.HasPrefix(pkg, prefix) {
			matches = append(matches, pkg)
		}
	}
	return iterator.FromSlice(matches), nil
}

// docRunner is implemented by runners that can execute `go doc`.
type docRunner interface {
	runDoc(ctx context.Context, arg string) (string, error)
}

// completionRunner is implemented by runners that back gopls completion.
// It exposes the on-disk program file that the session overwrites with a
// completion probe and opens as an LSP overlay; sharing the same file as
// `go run` keeps gopls's package analysis warm across evaluations.
type completionRunner interface {
	// programPath returns the absolute path of the program file
	// (programDir/main.go) and writes content to it so gopls can load
	// the enclosing package from disk before the overlay is applied.
	programPath(content string) (string, error)
}

func completeBuiltins(prefix string) []string {
	all := []string{
		"/type", "/print", "/write",
		"/clear", "/doc", "/help", "/quit",
	}
	var out []string
	for _, c := range all {
		if strings.HasPrefix(c, prefix) {
			out = append(out, c)
		}
	}
	return out
}

func replHelp() []string {
	return []string{
		`import "<path>" add an import (with path completion)`,
		"/type <expr>    print the type of an expression",
		"/print          show the accumulated program",
		"/write <file>   write the accumulated program to a file",
		"/clear          reset the session",
		"/doc <arg>      show go doc output",
		"/help           list commands",
		"/quit           close the REPL",
	}
}

func outputRows(out string) iterator.Iterator[component.Responsive] {
	if out == "" {
		return stringRows()
	}
	return stringRows(strings.Split(out, "\n")...)
}

// runFailure builds an error carrying the compiler/runtime output so the
// REPL surfaces the diagnostic.
func runFailure(out string, err error) error {
	if out == "" {
		return err
	}
	return errors.New(out)
}
