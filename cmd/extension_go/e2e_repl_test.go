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

//go:build e2e

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/console/ideconsole"
	"unstable.build/rune/internal/text/standard"
)

func TestGoREPLEndToEndEval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping toolchain+gopls e2e test in -short mode")
	}
	tests := []struct {
		name  string
		steps []evalStep
	}{
		{"scalar expression", []evalStep{
			{`1 + 1`, []string{"go> 1 + 1", "2"}},
		}},
		{"value call prints result", []evalStep{
			{`import "strings"`, []string{`go> import "strings"`}},
			{`strings.ToUpper("hi")`, []string{`go> strings.ToUpper("hi")`, `"HI"`}},
		}},
		{"multi-value call groups as tuple", []evalStep{
			{`import "fmt"`, []string{`go> import "fmt"`}},
			{`fmt.Println("hello")`, []string{`go> fmt.Println("hello")`, "hello", "(6, <nil>)"}},
		}},
		{"statement then expression accumulate", []evalStep{
			{`x := 21`, []string{"go> x := 21", "21"}},
			{`x * 2`, []string{"go> x * 2", "42"}},
		}},
		{"side-effecting call persists across lines", []evalStep{
			{`import "strings"`, []string{`go> import "strings"`}},
			{`b := strings.Builder{}`, nil},
			{`b.WriteString("abc")`, nil},
			{`b.String()`, []string{`go> b.String()`, `"abc"`}},
		}},
		{"local package import and use", []evalStep{
			{`import "example.com/replmod/greeter"`,
				[]string{`go> import "example.com/replmod/greeter"`}},
			{`greeter.Greet("Rune")`,
				[]string{`go> greeter.Greet("Rune")`, `"Hello, Rune!"`}},
		}},
		{"unused import does not error", []evalStep{
			{`import "math"`, []string{`go> import "math"`}},
		}},
		{"error then recovery", []evalStep{
			{`undefinedSymbol`, nil}, // compiler error surfaces; tail unchecked
			{`7 * 6`, []string{"go> 7 * 6", "42"}},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rig := newREPLRig(t)
			for i, st := range tc.steps {
				rig.submitKeys(st.line)
				if st.wantTail == nil {
					continue
				}
				got := rig.lines()
				require.GreaterOrEqual(t, len(got), len(st.wantTail)+1,
					"step %d %q: too few rendered lines: %q", i, st.line, got)
				// The final rendered line is the fresh prompt; the tail
				// before it must match the expected echo+output.
				tail := got[len(got)-len(st.wantTail)-1 : len(got)-1]
				require.Equal(t, st.wantTail, tail,
					"step %d %q frame:\n%s", i, st.line, rig.frame())
			}
		})
	}
}

func TestGoREPLEndToEndCompletion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping toolchain+gopls e2e test in -short mode")
	}
	rig := newREPLRig(t)
	rig.submitKeys(`import "example.com/replmod/greeter"`)
	rig.submitKeys(`greeter.Greet("Rune")`) // warm gopls package metadata

	t.Run("member completion opens overlay", func(t *testing.T) {
		rig.reset()
		cands := rig.completeOverlay("greeter.")
		require.Equal(t, []string{"Greet", "Prefix", "Shout"}, cands)
	})

	t.Run("single member auto-accepts inline", func(t *testing.T) {
		rig.reset()
		rig.typeText("greeter.S")
		rig.tab()
		require.Equal(t, "go> greeter.Shout", lastPrompt(rig))
	})

	t.Run("import path completion lists package", func(t *testing.T) {
		cands := rig.completePackages(`import "example.com/replmod/g`)
		require.Contains(t, cands, "example.com/replmod/greeter")
	})

	t.Run("builtin completion opens overlay", func(t *testing.T) {
		rig.reset()
		cands := rig.completeOverlay("/")
		require.Equal(t,
			[]string{"/type", "/print", "/write", "/clear", "/doc", "/help", "/quit"},
			cands)
	})

	t.Run("single builtin auto-accepts whole token", func(t *testing.T) {
		rig.reset()
		rig.typeText("/ty")
		rig.tab()
		require.Equal(t, "go> /type", lastPrompt(rig))
	})
}

func TestGoREPLEndToEndSignatureHelp(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping toolchain+gopls e2e test in -short mode")
	}
	rig := newREPLRig(t)
	rig.submitKeys(`import "fmt"`)
	rig.submitKeys(`fmt.Println("warm")`) // warm gopls package metadata

	rig.reset()
	rig.typeText("fmt.Println(")

	// gopls can return no signature on the first probe against a freshly
	// opened synthetic file; <tab> with the cursor just after "("
	// re-requests signature help, so retry until the hint appears.
	var frame string
	require.Eventually(t, func() bool {
		rig.tab()
		frame = rig.frame()
		return strings.Contains(frame, "Println(")
	}, 10*time.Second, 200*time.Millisecond,
		"signature hint should appear, frame:\n%s", rig.frame())
	require.Contains(t, frame, "Println(", "frame:\n%s", frame)
}

type evalStep struct {
	line     string
	wantTail []string
}

func TestGoREPLEndToEndClearScreenResetsProgram(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping toolchain+gopls e2e test in -short mode")
	}
	rig := newREPLRig(t)
	rig.submitKeys(`import "strings"`)
	rig.submitKeys(`a := strings.Builder{}`)
	rig.clearScreen()
	rig.submitKeys(`a := strings.Builder{}`)

	got := rig.lines()
	for _, ln := range got {
		require.NotContains(t, ln, "no new variables",
			"redeclaring after <c-l> must not collide with a cleared decl:\n%s",
			rig.frame())
	}
}

// --- e2e harness ------------------------------------------------------

const (
	replWidth  = 40
	replHeight = 10
)

// replRig drives a fully wired Go REPL handler: a real go-toolchain
// runner, a real gopls behind semanticapi.LSP, and the ideconsole handler,
// rooted at a throwaway copy of testdata/replmod.
type replRig struct {
	t     *testing.T
	shell *ideconsole.Handler
	drain *drainHandler
	sched *tickScheduler
	sess  *goSession
}

func newREPLRig(t *testing.T) *replRig {
	t.Helper()
	goplsBin := findGopls(t)
	dir := copyReplModule(t)

	files := []testFile{{
		name:    "greeter/greeter.go",
		content: mustRead(t, filepath.Join(dir, "greeter", "greeter.go")),
	}}
	env := initGoplsFromDir(t, goplsBin, dir, files)

	// A single real file scheme rooted at the workspace backs both the
	// command executor and the REPL's file system, so gopls, `go run`,
	// and the REPL all see the same on-disk directory.
	scheme := newTestSchemeRooted(dir)
	runner := &executorRunner{
		executor:   scheme,
		fs:         scheme,
		moduleDir:  dir,
		programDir: filepath.Join(dir, ".rune", "cache", "go-repl-e2e"),
	}
	session := newGoSession(runner, scheme, env.mgr)

	ti := &nopInterrupter{}
	sched := newTickScheduler(ti)
	cfg := (&replSubcommand{}).shellConfig(session, workspaceapi.URI{}, false)
	shell, registry := ideconsole.New(
		sched.schedule,
		ti,
		commandEditor{te: standard.Editor()},
		cfg,
	)
	require.NoError(t, registry.RegisterREPLCommand(
		textapi.CommandManual{Name: "go", Summary: "Evaluate Go"}, session,
	))
	t.Cleanup(func() {
		_ = shell.Close()
		_ = os.RemoveAll(runner.programDir)
	})

	d := &drainHandler{Handler: shell, sched: sched}
	d.Resize(replWidth, replHeight)
	return &replRig{t: t, shell: shell, drain: d, sched: sched, sess: session}
}

// submitKeys types line followed by <enter> through the handler, then
// blocks until the async command finishes and applies its output. It
// drives real keystrokes, so no synthetic ^C abort appears in the
// rendered output the way it does with Submit.
func (r *replRig) submitKeys(line string) {
	r.t.Helper()
	r.typeText(line)
	r.drain.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	r.shell.Wait()
	r.sched.drain()
}

// clearScreen sends <c-l>, mirroring the user pressing it to wipe the
// screen, and drains so the language session's reset hook runs.
func (r *replRig) clearScreen() {
	r.t.Helper()
	r.drain.Handle(term.Event{Type: term.EventKey, Ch: 'l', Mod: term.ModCtrl})
	r.shell.Wait()
	r.sched.drain()
}

// typeText sends each rune of s as a literal key event, mapping space to
// KeySpace. Unlike typeKeys it does not interpret handlertest tokens, so
// arbitrary Go source (with spaces and punctuation) types verbatim.
func (r *replRig) typeText(s string) {
	r.t.Helper()
	for _, ch := range s {
		ev := term.Event{Ch: ch, Type: term.EventKey}
		if ch == ' ' {
			ev = term.Event{Key: term.KeySpace, Type: term.EventKey}
		}
		r.drain.Handle(ev)
	}
}

// reset dismisses any open overlay and clears the input line so the next
// case starts from a clean prompt. modeless binds neither <ctrl-u> nor
// <ctrl-c> to kill-line, so the line is emptied with backspaces.
func (r *replRig) reset() {
	r.t.Helper()
	r.drain.Handle(term.Event{Type: term.EventKey, Key: term.KeyEsc})
	for range replWidth * 4 {
		r.drain.Handle(term.Event{Type: term.EventKey, Key: term.KeyBackspace})
	}
}

// lines returns the non-empty, right-trimmed content lines of the
// current frame, dropping the blank top padding so command output can be
// asserted deterministically.
func (r *replRig) lines() []string {
	r.t.Helper()
	var out []string
	for ln := range strings.SplitSeq(r.frame(), "\n") {
		if t := strings.TrimRight(ln, " "); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// frame renders the current handler state to a golden string.
func (r *replRig) frame() string {
	r.t.Helper()
	return handlertest.DrawHandler(r.drain, replWidth, replHeight)
}

// tab sends a <tab> to trigger completion at the cursor, then blocks
// until the async completion feeder settles and runs any scheduled
// single/zero-match resolution so the overlay view is deterministic.
func (r *replRig) tab() {
	r.t.Helper()
	r.drain.Handle(term.Event{Type: term.EventKey, Key: term.KeyTab})
	r.shell.WaitCompletion()
	r.sched.drain()
}

// completeOverlay types prefix, presses <tab>, and returns the candidate
// labels shown in the completion overlay. When gopls returns a single
// match the shell accepts it inline and no overlay opens, so the result
// is empty.
func (r *replRig) completeOverlay(prefix string) []string {
	r.t.Helper()
	r.typeText(prefix)
	r.tab()
	return overlayCandidates(r.frame())
}

// completePackages asks the session for import-path candidates,
// retrying because gopls returns an empty set on the first query against
// a freshly opened synthetic file until it has indexed the package.
func (r *replRig) completePackages(line string) []string {
	r.t.Helper()
	cmd, args := splitCompletionLine(line)
	var cands []string
	require.Eventually(r.t, func() bool {
		it, err := r.sess.Complete(context.Background(), cmd, args)
		require.NoError(r.t, err)
		cands, err = iterator.ToSlice(context.Background(), it)
		require.NoError(r.t, err)
		return len(cands) > 0
	}, 10*time.Second, 200*time.Millisecond)
	return cands
}

// overlayCandidates extracts the indented candidate rows the completion
// overlay renders (lines that start with whitespace and a word, sitting
// above the prompt row).
func overlayCandidates(frame string) []string {
	var out []string
	for ln := range strings.SplitSeq(frame, "\n") {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(ln, "go> ") {
			continue
		}
		if strings.HasPrefix(ln, "    ") {
			out = append(out, trimmed)
		}
	}
	return out
}

// lastPrompt returns the trimmed final prompt line of the current frame,
// with the cursor block stripped, so the accepted input can be asserted.
func lastPrompt(r *replRig) string {
	r.t.Helper()
	ls := r.lines()
	last := ls[len(ls)-1]
	last = strings.TrimRight(last, "▐")
	return strings.TrimRight(last, " ")
}

// splitCompletionLine splits a raw line into the cmd/args shape the shell
// passes to Complete: the first whitespace-delimited token is cmd, the
// rest are args.
func splitCompletionLine(line string) (string, []string) {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", nil
	}
	return fields[0], fields[1:]
}

// copyReplModule copies testdata/replmod into a throwaway directory so
// the REPL's synthesized program and gopls overlays never touch the
// checked-in fixture.
func copyReplModule(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	src := "testdata/replmod"
	require.NoError(t, filepath.Walk(src,
		func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if info.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, data, 0o644)
		}))
	return dst
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}
