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
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/storageapi/storagestub"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/component"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"github.com/unstablebuild/rune-go-sdk/tui"
	"unstable.build/rune/internal/cell"
	"unstable.build/rune/internal/ide/ideshell"
	"unstable.build/rune/internal/text/standard"
)

// collectWidth is intentionally large so ResponsiveString.String() never
// wraps the rendered output, letting tests assert exact text.
const collectWidth = 10000

// scriptedReply is one canned runner response.
type scriptedReply struct {
	out string
	err error
}

// scriptedRunner is a programRunner/docRunner that records every program
// (and doc arg) it receives and replays canned replies by call index. If
// the index has no scripted reply it falls back to def.
type scriptedRunner struct {
	programs []string
	docArgs  []string
	replies  map[int]scriptedReply
	def      scriptedReply
	docReply scriptedReply
}

var (
	_ programRunner = (*scriptedRunner)(nil)
	_ docRunner     = (*scriptedRunner)(nil)
)

func newScriptedRunner() *scriptedRunner {
	return &scriptedRunner{replies: map[int]scriptedReply{}}
}

func (r *scriptedRunner) reply(call int, out string, err error) *scriptedRunner {
	r.replies[call] = scriptedReply{out: out, err: err}
	return r
}

func (r *scriptedRunner) run(_ context.Context, program string) (string, error) {
	call := len(r.programs)
	r.programs = append(r.programs, program)
	if reply, ok := r.replies[call]; ok {
		return reply.out, reply.err
	}
	return r.def.out, r.def.err
}

func (r *scriptedRunner) runDoc(_ context.Context, arg string) (string, error) {
	r.docArgs = append(r.docArgs, arg)
	return r.docReply.out, r.docReply.err
}

func (r *scriptedRunner) lastProgram() string {
	if len(r.programs) == 0 {
		return ""
	}
	return r.programs[len(r.programs)-1]
}

// submit feeds one raw line to the session exactly as repl.Handler would,
// splitting on single spaces into Name/Args, and returns the output rows.
func submit(
	t *testing.T, s *goSession, line string,
) ([]string, error) {
	t.Helper()
	fields := strings.Split(line, " ")
	cmd := repl.Command{Name: fields[0], Args: fields[1:]}
	it, err := s.HandleCommand(
		context.Background(), cmd, repl.NopProgressWriter())
	if err != nil {
		return nil, err
	}
	return collect(t, it), nil
}

func collect(
	t *testing.T, it iterator.Iterator[component.Responsive],
) []string {
	t.Helper()
	items, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	out := make([]string, 0, len(items))
	for _, item := range items {
		rs, ok := item.(*component.ResponsiveString)
		require.True(t, ok, "expected *ResponsiveString, got %T", item)
		rs.Resize(collectWidth, rs.Height(collectWidth))
		out = append(out, rs.String())
	}
	return out
}

func newTestSession(r programRunner) *goSession {
	return newGoSession(r, nil, nil)
}

// --- classification ---------------------------------------------------

func TestGoREPLClassify(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		kind    fragmentKind
		text    string
		imports []string
		call    bool
	}{
		{"int expression", "1+1", fragExpr, "1 + 1", nil, false},
		{"call expression", "fmt.Println(1)", fragExpr, "fmt.Println(1)", nil, true},
		{"method call expression", "buf.String()", fragExpr, "buf.String()", nil, true},
		{"composite literal", "[]int{1, 2, 3}", fragExpr, "[]int{1, 2, 3}", nil, false},
		{"short var decl", "x := 5", fragStmt, "x := 5", nil, false},
		{"return statement", "return 5", fragStmt, "return 5", nil, false},
		{"assignment", "x = 7", fragStmt, "x = 7", nil, false},
		{"var decl", "var y = 10", fragDecl, "var y = 10", nil, false},
		{"const decl", "const z = 1", fragDecl, "const z = 1", nil, false},
		{"type decl", "type T struct{ A int }", fragDecl, "type T struct{ A int }", nil, false},
		{"func decl", "func f() int { return 1 }", fragDecl, "func f() int { return 1 }", nil, false},
		{"import single", `import "strings"`, fragImport, "", []string{`"strings"`}, false},
		{"import aliased", `import w "io"`, fragImport, "", []string{`w "io"`}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			frag, err := classify(tc.input)
			require.NoError(t, err)
			require.Equal(t, tc.kind, frag.kind)
			require.Equal(t, tc.text, frag.text)
			require.Equal(t, tc.imports, frag.imports)
			require.Equal(t, tc.call, frag.call)
		})
	}
}

func TestGoREPLClassifyIncomplete(t *testing.T) {
	t.Parallel()
	// Inputs that go/parser reports as merely unfinished; the REPL keeps
	// buffering instead of surfacing an error.
	incomplete := []string{
		"if x > 0 {",
		"for i := 0; i < 3; i++ {",
		"func f() {",
		"switch x {",
		"a := []int{1, 2,",
		"foo(",
		"x +",
	}
	for _, in := range incomplete {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := classify(in)
			require.Error(t, err)
			require.ErrorIs(t, err, errIncomplete,
				"expected %q to be treated as incomplete", in)
		})
	}
}

// TestGoREPLClassifyGarbage covers genuinely broken input that can never
// be completed by appending more lines. classify must return a real error
// (not errIncomplete) so the REPL surfaces it instead of buffering forever.
func TestGoREPLClassifyGarbage(t *testing.T) {
	t.Parallel()
	garbage := []string{
		"garbage garbage",
		"foo bar baz",
		"x y",
		"1 2 3",
		"return return",
		"func func",
		"}",
		"]",
		")",
		"if else",
	}
	for _, in := range garbage {
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			_, err := classify(in)
			require.Error(t, err, "expected %q to be a syntax error", in)
			require.NotErrorIs(t, err, errIncomplete,
				"expected %q to be a hard error, not incomplete", in)
		})
	}
}

// TestGoREPLGarbageSurfacesError drives the full session: garbage input
// must return an error rather than silently buffering.
func TestGoREPLGarbageSurfacesError(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner()
	s := newTestSession(r)
	_, err := submit(t, s, "garbage garbage")
	require.Error(t, err)
	require.Empty(t, s.pending, "garbage must not be buffered")
	require.Empty(t, r.programs, "garbage must not reach the runner")
}

// --- single-line evaluation -------------------------------------------

// step is one input line plus the exact expected result of submitting it.
type step struct {
	line      string
	wantOut   []string // exact output rows
	wantErr   string   // non-empty => expect an error whose message equals this
	wantErrIs error    // non-nil => expect errors.Is match
	wantProg  string   // non-empty => exact rendered program of the LAST run
	wantNoRun bool     // true => the runner must not have been invoked this step
	wantStmts []string // when non-nil, asserts session.stmts after the step
	wantDecls []string // when non-nil, asserts session.decls after the step
}

func TestGoREPLEval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		runner func() *scriptedRunner
		steps  []step
	}{
		{
			name:   "bare expression prints value",
			runner: func() *scriptedRunner { return newScriptedRunner().reply(0, "2", nil) },
			steps: []step{
				{
					line:     "1+1",
					wantOut:  []string{"2"},
					wantProg: printedProgram(nil, nil, "1 + 1"),
				},
			},
		},
		{
			name: "statement then expression accumulate",
			runner: func() *scriptedRunner {
				return newScriptedRunner().reply(0, "", nil).reply(1, "10", nil)
			},
			steps: []step{
				{line: "x := 5", wantOut: []string{}, wantStmts: []string{"x := 5"}},
				{
					line:     "x * 2",
					wantOut:  []string{"10"},
					wantProg: printedProgram(nil, []string{"x := 5"}, "x * 2"),
				},
			},
		},
		{
			name: "statement that prints surfaces stdout",
			runner: func() *scriptedRunner {
				return newScriptedRunner().reply(0, "hello", nil)
			},
			steps: []step{
				{line: "fmt.Println(`hello`)", wantOut: []string{"hello"}},
			},
		},
		{
			name: "multi-line stdout splits into rows",
			runner: func() *scriptedRunner {
				return newScriptedRunner().reply(0, "a\nb\nc", nil)
			},
			steps: []step{
				{line: "dump()", wantOut: []string{"a", "b", "c"}},
			},
		},
		{
			name:   "source import accumulates",
			runner: func() *scriptedRunner { return newScriptedRunner() },
			steps: []step{
				{
					line:     `import "strings"`,
					wantOut:  []string{},
					wantProg: "package main\n\nimport (\n\t_ \"strings\"\n)\n\nfunc main() {\n}\n",
				},
			},
		},
		{
			name:   "import then use",
			runner: func() *scriptedRunner { return newScriptedRunner().reply(1, "3.14", nil) },
			steps: []step{
				{line: `import "math"`, wantOut: []string{}},
				{
					line:     "math.Pi",
					wantOut:  []string{"3.14"},
					wantProg: printedProgram([]string{`"math"`}, nil, "math.Pi"),
				},
			},
		},
		{
			name:   "top-level func decl then call",
			runner: func() *scriptedRunner { return newScriptedRunner().reply(1, "42", nil) },
			steps: []step{
				{
					line:      "func answer() int { return 42 }",
					wantOut:   []string{},
					wantDecls: []string{"func answer() int { return 42 }"},
				},
				{line: "answer()", wantOut: []string{"42"}},
			},
		},
		{
			name:   "var decl is a top-level declaration",
			runner: func() *scriptedRunner { return newScriptedRunner() },
			steps: []step{
				{
					line:      "var counter = 0",
					wantOut:   []string{},
					wantDecls: []string{"var counter = 0"},
					wantStmts: []string{},
				},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := tc.runner()
			s := newTestSession(r)
			for i, st := range tc.steps {
				before := len(r.programs)
				out, err := submit(t, s, st.line)
				switch {
				case st.wantErrIs != nil:
					require.ErrorIs(t, err, st.wantErrIs, "step %d", i)
				case st.wantErr != "":
					require.EqualError(t, err, st.wantErr, "step %d", i)
				default:
					require.NoError(t, err, "step %d", i)
					require.Equal(t, st.wantOut, out, "step %d output", i)
				}
				if st.wantNoRun {
					require.Equal(t, before, len(r.programs),
						"step %d should not run", i)
				}
				if st.wantProg != "" {
					require.Equal(t, st.wantProg, r.lastProgram(),
						"step %d program", i)
				}
				assertSlice(t, st.wantStmts, s.stmts, i, "stmts")
				assertSlice(t, st.wantDecls, s.decls, i, "decls")
			}
		})
	}
}

// --- multi-line buffering ---------------------------------------------

func TestGoREPLMultiLine(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		runner   func() *scriptedRunner
		steps    []step
		wantProg string // final program rendered on the completing line
	}{
		{
			name:   "if block buffered until closed",
			runner: func() *scriptedRunner { return newScriptedRunner() },
			steps: []step{
				{line: "if x > 0 {", wantOut: []string{}, wantNoRun: true},
				{line: "fmt.Println(x)", wantOut: []string{}, wantNoRun: true},
				{line: "}", wantOut: []string{}},
			},
			wantProg: "package main\n\nfunc main() {\n\tif x > 0 {\n\t\tfmt.Println(x)\n\t}\n}\n",
		},
		{
			name:   "multi-line func definition",
			runner: func() *scriptedRunner { return newScriptedRunner() },
			steps: []step{
				{line: "func double(n int) int {", wantOut: []string{}, wantNoRun: true},
				{line: "return n * 2", wantOut: []string{}, wantNoRun: true},
				{line: "}", wantOut: []string{}},
			},
			wantProg: "package main\n\nfunc double(n int) int {\n\treturn n * 2\n}\n\nfunc main() {\n}\n",
		},
		{
			name:   "multi-line composite literal",
			runner: func() *scriptedRunner { return newScriptedRunner() },
			steps: []step{
				{line: "nums := []int{", wantOut: []string{}, wantNoRun: true},
				{line: "1,", wantOut: []string{}, wantNoRun: true},
				{line: "2,", wantOut: []string{}, wantNoRun: true},
				{line: "}", wantOut: []string{}},
			},
			wantProg: assignProgram("nums := []int{\n\t\t1,\n\t\t2,\n\t}", "nums"),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := tc.runner()
			s := newTestSession(r)
			for i, st := range tc.steps {
				before := len(r.programs)
				out, err := submit(t, s, st.line)
				require.NoError(t, err, "step %d", i)
				require.Equal(t, st.wantOut, out, "step %d output", i)
				if st.wantNoRun {
					require.Equal(t, before, len(r.programs),
						"step %d must buffer, not run", i)
					require.NotEmpty(t, s.pending, "step %d expected pending", i)
				}
			}
			require.Empty(t, s.pending, "pending must drain after completion")
			require.Len(t, r.programs, 1, "only the completing line runs")
			require.Equal(t, tc.wantProg, r.lastProgram())
		})
	}
}

// TestGoREPLBufferThenBuiltinResets verifies a `:`-command issued while a
// multi-line snippet is buffering discards the partial input.
func TestGoREPLBufferThenBuiltinResets(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner()
	s := newTestSession(r)

	out, err := submit(t, s, "if x > 0 {")
	require.NoError(t, err)
	require.Empty(t, out)
	require.NotEmpty(t, s.pending)

	out, err = submit(t, s, "/clear")
	require.NoError(t, err)
	require.Equal(t, []string{"session cleared"}, out)
	require.Empty(t, s.pending)
}

// --- error scenarios and recovery -------------------------------------

func TestGoREPLErrorRecovery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		runner func() *scriptedRunner
		steps  []step
		// after asserts session state once all steps run.
		after func(t *testing.T, s *goSession, r *scriptedRunner)
	}{
		{
			name: "expression compile error does not poison session",
			runner: func() *scriptedRunner {
				return newScriptedRunner().
					// nope() is a call: the printer form (call 0) fails
					// and surfaces, then the bare-stmt fallback (call 1)
					// also fails. The undefined symbol is reported.
					reply(0, "./prog.go:5:2: undefined: nope", errors.New("exit status 1")).
					reply(1, "./prog.go:5:2: undefined: nope", errors.New("exit status 1")).
					reply(2, "7", nil)
			},
			steps: []step{
				{line: "nope()", wantErr: "./prog.go:5:2: undefined: nope"},
				{line: "7", wantOut: []string{"7"}},
			},
			after: func(t *testing.T, s *goSession, _ *scriptedRunner) {
				require.Empty(t, s.stmts)
				require.Empty(t, s.decls)
			},
		},
		{
			name: "statement compile error rolls back",
			runner: func() *scriptedRunner {
				return newScriptedRunner().
					reply(0, "undefined: undefinedVar", errors.New("exit status 1"))
			},
			steps: []step{
				{line: "y := undefinedVar", wantErr: "undefined: undefinedVar"},
			},
			after: func(t *testing.T, s *goSession, _ *scriptedRunner) {
				require.Empty(t, s.stmts, "failed statement must be rolled back")
			},
		},
		{
			name: "decl compile error rolls back",
			runner: func() *scriptedRunner {
				return newScriptedRunner().
					reply(0, "syntax error in body", errors.New("exit status 1"))
			},
			steps: []step{
				{line: "func bad() int { return }", wantErr: "syntax error in body"},
			},
			after: func(t *testing.T, s *goSession, _ *scriptedRunner) {
				require.Empty(t, s.decls, "failed decl must be rolled back")
			},
		},
		{
			name: "import error rolls back the import",
			runner: func() *scriptedRunner {
				return newScriptedRunner().
					reply(0, `cannot find package "nope/x"`, errors.New("exit status 1"))
			},
			steps: []step{
				{line: `import "nope/x"`, wantErr: `cannot find package "nope/x"`},
			},
			after: func(t *testing.T, s *goSession, _ *scriptedRunner) {
				require.Empty(t, s.imports, "failed import must be rolled back")
			},
		},
		{
			name: "error without output surfaces the raw error",
			runner: func() *scriptedRunner {
				// boom() is a call: the printer form (call 0) fails and
				// its error surfaces; the bare-stmt fallback (call 1)
				// also fails.
				return newScriptedRunner().
					reply(0, "", errors.New("exit status 2")).
					reply(1, "", errors.New("exit status 1"))
			},
			steps: []step{
				{line: "boom()", wantErr: "exit status 2"},
			},
			after: func(t *testing.T, s *goSession, _ *scriptedRunner) {},
		},
		{
			name: "recover and continue after error",
			runner: func() *scriptedRunner {
				return newScriptedRunner().
					reply(0, "", nil).                             // x := 1 ok
					reply(1, "boom", errors.New("exit status 1")). // nope() printer form fails
					reply(2, "boom", errors.New("exit status 1")). // nope() bare-stmt fallback fails
					reply(3, "1", nil)                             // x ok again
			},
			steps: []step{
				{line: "x := 1", wantOut: []string{}, wantStmts: []string{"x := 1"}},
				{line: "nope()", wantErr: "boom", wantStmts: []string{"x := 1"}},
				{line: "x", wantOut: []string{"1"}, wantStmts: []string{"x := 1"}},
			},
			after: func(t *testing.T, s *goSession, _ *scriptedRunner) {
				require.Equal(t, []string{"x := 1"}, s.stmts)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := tc.runner()
			s := newTestSession(r)
			for i, st := range tc.steps {
				out, err := submit(t, s, st.line)
				if st.wantErr != "" {
					require.EqualError(t, err, st.wantErr, "step %d", i)
				} else {
					require.NoError(t, err, "step %d", i)
					require.Equal(t, st.wantOut, out, "step %d", i)
				}
				assertSlice(t, st.wantStmts, s.stmts, i, "stmts")
			}
			tc.after(t, s, r)
		})
	}
}

// --- builtins ---------------------------------------------------------

func TestGoREPLPrint(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner()
	s := newTestSession(r)
	require.NoError(t, mustSubmit(t, s, `import "fmt"`))
	require.NoError(t, mustSubmit(t, s, `import "strings"`))

	out, err := submit(t, s, "/print")
	require.NoError(t, err)
	want := []string{
		"package main",
		"",
		"import (",
		"\t_ \"fmt\"",
		"\t_ \"strings\"",
		")",
		"",
		"func main() {",
		"}",
		"", // rendered program ends in a trailing newline
	}
	require.Equal(t, want, out)
}

func TestGoREPLPrintEmptySession(t *testing.T) {
	t.Parallel()
	s := newTestSession(newScriptedRunner())
	out, err := submit(t, s, "/print")
	require.NoError(t, err)
	require.Equal(t,
		[]string{"package main", "", "func main() {", "}", ""}, out)
}

// TestGoREPLImportRendersBlankWhenUnused asserts that an imported but
// unreferenced package renders as a blank import so go run never trips
// the "imported and not used" error, and switches to a normal import
// once the package is referenced.
func TestGoREPLImportRendersBlankWhenUnused(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		imports []string
		decls   []string
		stmts   []string
		expr    string
		want    []string
	}{
		{
			name:    "unused import is blank",
			imports: []string{`"math"`},
			want:    []string{`_ "math"`},
		},
		{
			name:    "used import is normal and printing forces fmt",
			imports: []string{`"math"`},
			expr:    "math.Pi",
			want:    []string{`"fmt"`, `"math"`},
		},
		{
			name:    "explicit fmt stays normal when printing an expression",
			imports: []string{`"fmt"`, `"math"`},
			expr:    "math.Pi",
			want:    []string{`"fmt"`, `"math"`},
		},
		{
			name:    "aliased import referenced by alias is normal",
			imports: []string{`w "io"`},
			stmts:   []string{"_ = w.EOF"},
			want:    []string{`w "io"`},
		},
		{
			name:    "aliased import unreferenced is blank",
			imports: []string{`w "io"`},
			want:    []string{`_ "io"`},
		},
		{
			name:    "substring of a longer identifier does not count as use",
			imports: []string{`"os"`},
			stmts:   []string{"cosmos.Run()"},
			want:    []string{`_ "os"`},
		},
		{
			name:    "import referenced from a decl is normal",
			imports: []string{`"strings"`},
			decls:   []string{"func up(s string) string { return strings.ToUpper(s) }"},
			want:    []string{`"strings"`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newTestSession(newScriptedRunner())
			for _, spec := range tc.imports {
				s.imports[spec] = struct{}{}
			}
			s.decls = tc.decls
			s.stmts = tc.stmts
			require.Equal(t, tc.want, renderedImports(s.render(tc.expr)))
		})
	}
}

// renderedImports extracts the import specs (without the surrounding
// parens or indentation) from a rendered program.
func renderedImports(program string) []string {
	lines := strings.Split(program, "\n")
	var out []string
	in := false
	for _, line := range lines {
		switch {
		case line == "import (":
			in = true
		case in && line == ")":
			return out
		case in:
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

// printedProgram returns the program render produces when printing expr
// with the given extra imports (beyond the implicit fmt) and statements.
// It exercises the production renderer so expectations track its output.
func printedProgram(imports, stmts []string, expr string) string {
	s := newTestSession(newScriptedRunner())
	for _, spec := range imports {
		s.imports[spec] = struct{}{}
	}
	s.stmts = stmts
	for _, stmt := range stmts {
		s.declared = append(s.declared, assignedNames(stmt)...)
	}
	return s.render(expr)
}

// statementProgram returns the program render produces when running a
// call expression as a bare trailing statement (no printer wrapper).
func statementProgram(imports, stmts []string, call string) string {
	s := newTestSession(newScriptedRunner())
	for _, spec := range imports {
		s.imports[spec] = struct{}{}
	}
	s.stmts = stmts
	for _, stmt := range stmts {
		s.declared = append(s.declared, assignedNames(stmt)...)
	}
	return s.renderStmt(call)
}

// assignProgram returns the program render produces for a single
// accumulated `:=` statement whose declared names are printed.
func assignProgram(stmt string, names ...string) string {
	s := newTestSession(newScriptedRunner())
	s.stmts = []string{stmt}
	s.declared = append(s.declared, names...)
	return s.renderProgramSource(printStmt(names), true)
}

// TestGoREPLNonCallExpressionPrints asserts a non-call expression is
// printed through the variadic printer on the first and only run.
func TestGoREPLNonCallExpressionPrints(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner().reply(0, "2", nil)
	s := newTestSession(r)

	out, err := submit(t, s, "1 + 1")
	require.NoError(t, err)
	require.Equal(t, []string{"2"}, out)

	require.Len(t, r.programs, 1, "a non-call expression needs only one run")
	require.Equal(t, printedProgram(nil, nil, "1 + 1"), r.lastProgram())
}

// TestGoREPLCallPrintsThroughPrinter asserts a call is wrapped in the
// variadic printer in a single run so its results are shown. A
// multi-valued call (e.g. fmt.Println) spreads into the printer and is
// formatted as a tuple by the printer body.
func TestGoREPLCallPrintsThroughPrinter(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner().reply(0, "hello\n(6, <nil>)", nil)
	s := newTestSession(r)

	out, err := submit(t, s, `fmt.Println("hello")`)
	require.NoError(t, err)
	require.Equal(t, []string{"hello", "(6, <nil>)"}, out)

	require.Len(t, r.programs, 1, "a value-producing call needs only one run")
	require.Equal(t, printedProgram(nil, nil, `fmt.Println("hello")`), r.lastProgram())
}

// TestGoREPLVoidCallFallsBackToStatement asserts a void call (no value to
// hand the printer) fails the printer form, then re-runs as a bare
// statement so its side effects still execute.
func TestGoREPLVoidCallFallsBackToStatement(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner().
		reply(0, "void() (no value) used as value", errors.New("exit status 1")).
		reply(1, "side effect", nil)
	s := newTestSession(r)

	out, err := submit(t, s, "void()")
	require.NoError(t, err)
	require.Equal(t, []string{"side effect"}, out)

	require.Len(t, r.programs, 2, "void call retries as a bare statement")
	require.Equal(t, printedProgram(nil, nil, "void()"), r.programs[0],
		"first attempt wraps the call in the printer")
	require.Equal(t, statementProgram(nil, nil, "void()"), r.programs[1],
		"fallback runs the call as a bare statement")
}

// TestGoREPLRenderGolden pins the exact program render emits for a
// printed expression: fmt is imported, the variadic printer is declared,
// and the expression is handed to it.
func TestGoREPLRenderGolden(t *testing.T) {
	t.Parallel()
	s := newTestSession(newScriptedRunner())
	s.imports[`"math"`] = struct{}{}
	got := s.render("math.Pi")
	want := "package main\n\n" +
		"import (\n\t\"fmt\"\n\t\"math\"\n)\n\n" +
		"func __repl_print(xs ...any) {\n" +
		"\tif len(xs) == 1 {\n\t\tfmt.Printf(\"%#v\\n\", xs[0])\n\t\treturn\n\t}\n" +
		"\tfmt.Print(\"(\")\n" +
		"\tfor i, x := range xs {\n\t\tif i > 0 {\n\t\t\tfmt.Print(\", \")\n\t\t}\n\t\tfmt.Printf(\"%#v\", x)\n\t}\n" +
		"\tfmt.Println(\")\")\n}\n\n" +
		"func main() {\n\t__repl_print(math.Pi)\n}\n"
	require.Equal(t, want, got)
}

// TestGoREPLCallSurfacesPrinterErrorWhenBothFail asserts that when both
// the printer form and the bare-statement fallback fail, the
// printer-form error (the primary attempt) is surfaced.
func TestGoREPLCallSurfacesPrinterErrorWhenBothFail(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner().
		reply(0, "undefined: nope (printer)", errors.New("exit status 1")).
		reply(1, "undefined: nope (statement)", errors.New("exit status 1"))
	s := newTestSession(r)

	_, err := submit(t, s, "nope()")
	require.EqualError(t, err, "undefined: nope (printer)")
}

func TestGoREPLClear(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner()
	s := newTestSession(r)
	require.NoError(t, mustSubmit(t, s, `import "fmt"`))
	require.NoError(t, mustSubmit(t, s, "func f() {}"))
	require.NoError(t, mustSubmit(t, s, "x := 1"))

	out, err := submit(t, s, "/clear")
	require.NoError(t, err)
	require.Equal(t, []string{"session cleared"}, out)
	require.Empty(t, s.imports)
	require.Empty(t, s.decls)
	require.Empty(t, s.stmts)
	require.Empty(t, s.pending)
}

func TestGoREPLType(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner().reply(0, "int", nil)
	s := newTestSession(r)

	out, err := submit(t, s, "/type 5")
	require.NoError(t, err)
	require.Equal(t, []string{"int"}, out)
	require.Contains(t, r.lastProgram(), printerName+"(reflect.TypeOf(5))")
	require.Contains(t, r.lastProgram(), `"reflect"`)

	// The reflect import is only borrowed for the probe.
	_, ok := s.imports[`"reflect"`]
	require.False(t, ok)
}

func TestGoREPLTypeError(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner().reply(0, "undefined: zzz", errors.New("exit status 1"))
	s := newTestSession(r)
	_, err := submit(t, s, "/type zzz")
	require.EqualError(t, err, "undefined: zzz")
	// Even on failure the borrowed import must not leak.
	_, ok := s.imports[`"reflect"`]
	require.False(t, ok)
}

func TestGoREPLDoc(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner()
	r.docReply = scriptedReply{out: "package strings // import \"strings\"", err: nil}
	s := newTestSession(r)

	out, err := submit(t, s, "/doc strings")
	require.NoError(t, err)
	require.Equal(t, []string{`package strings // import "strings"`}, out)
	require.Equal(t, []string{"strings"}, r.docArgs)
}

func TestGoREPLDocError(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner()
	r.docReply = scriptedReply{out: "no such package", err: errors.New("exit status 1")}
	s := newTestSession(r)
	_, err := submit(t, s, "/doc nope")
	require.EqualError(t, err, "no such package")
}

func TestGoREPLHelp(t *testing.T) {
	t.Parallel()
	s := newTestSession(newScriptedRunner())
	out, err := submit(t, s, "/help")
	require.NoError(t, err)
	require.Equal(t, replHelp(), out)
}

func TestGoREPLQuit(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{"/quit", "/exit"} {
		t.Run(cmd, func(t *testing.T) {
			t.Parallel()
			s := newTestSession(newScriptedRunner())
			out, err := submit(t, s, cmd)
			require.NoError(t, err)
			require.Equal(t, []string{"close the tab to exit the REPL"}, out)
		})
	}
}

func TestGoREPLBuiltinErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line    string
		wantErr string
	}{
		{"/import", "unknown command: /import"},
		{"/type", "/type requires an expression"},
		{"/doc", "/doc requires an argument"},
		{"/bogus", "unknown command: /bogus"},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			s := newTestSession(newScriptedRunner())
			_, err := submit(t, s, tc.line)
			require.EqualError(t, err, tc.wantErr)
		})
	}
}

func TestGoREPLWrite(t *testing.T) {
	t.Parallel()
	r := newScriptedRunner()
	dir := t.TempDir()
	fs := &writeFS{root: dir}
	s := newGoSession(r, fs, nil)
	require.NoError(t, mustSubmit(t, s, `import "fmt"`))

	out, err := submit(t, s, "/write out.go")
	require.NoError(t, err)
	require.Equal(t, []string{"wrote out.go"}, out)

	got, err := os.ReadFile(filepath.Join(dir, "out.go"))
	require.NoError(t, err)
	require.Equal(t,
		"package main\n\nimport (\n\t_ \"fmt\"\n)\n\nfunc main() {\n}\n",
		string(got))
}

func TestGoREPLWriteRequiresPath(t *testing.T) {
	t.Parallel()
	s := newGoSession(newScriptedRunner(), &writeFS{root: t.TempDir()}, nil)
	_, err := submit(t, s, "/write")
	require.EqualError(t, err, "/write requires a file path")
}

// --- pure helpers -----------------------------------------------------

func TestCompleteBuiltins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		prefix string
		want   []string
	}{
		{"/", []string{"/type", "/print", "/write", "/clear", "/doc", "/help", "/quit"}},
		{"/t", []string{"/type"}},
		{"/p", []string{"/print"}},
		{"/q", []string{"/quit"}},
		{"/zzz", nil},
	}
	for _, tc := range tests {
		t.Run(tc.prefix, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, completeBuiltins(tc.prefix))
		})
	}
}

func TestGoREPLComplete(t *testing.T) {
	t.Parallel()
	s := newTestSession(newScriptedRunner())

	got, err := s.Complete(context.Background(), "/t", nil)
	require.NoError(t, err)
	items, err := iterator.ToSlice(context.Background(), got)
	require.NoError(t, err)
	require.Equal(t, []string{"/type"}, items)

	// Non-builtin input yields no completions.
	got, err = s.Complete(context.Background(), "math", nil)
	require.NoError(t, err)
	items, err = iterator.ToSlice(context.Background(), got)
	require.NoError(t, err)
	require.Empty(t, items)

	got, err = s.Complete(context.Background(), "import", []string{`"ma`})
	require.NoError(t, err)
	items, err = iterator.ToSlice(context.Background(), got)
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestNearestModuleDir(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "a", "b")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644))

	dir, ok := nearestModuleDir(sub)
	require.True(t, ok)
	require.Equal(t, root, dir)

	dir, ok = nearestModuleDir(root)
	require.True(t, ok)
	require.Equal(t, root, dir)

	_, ok = nearestModuleDir(t.TempDir())
	require.False(t, ok)
}

// --- Go fragment completion -------------------------------------------

// fakeCompletionLSP embeds semanticapi.LSP so unimplemented methods
// panic, scoping the fake to the completion path completeGo exercises.
type fakeCompletionLSP struct {
	semanticapi.LSP

	items []semanticapi.CompletionItem
	err   error

	packages []string

	sigHelp *semanticapi.SignatureHelp

	openedText string
	openedURI  string
	reqURI     string
	reqPos     semanticapi.Position
	closedURI  string
	execArgs   []json.RawMessage
}

func (f *fakeCompletionLSP) DidOpen(
	_ context.Context, p semanticapi.DidOpenTextDocumentParams,
) error {
	f.openedText = p.TextDocument.Text
	f.openedURI = p.TextDocument.URI
	return nil
}

func (f *fakeCompletionLSP) DidClose(
	_ context.Context, p semanticapi.DidCloseTextDocumentParams,
) error {
	f.closedURI = p.TextDocument.URI
	return nil
}

func (f *fakeCompletionLSP) Completion(
	_ context.Context, p semanticapi.CompletionParams,
) (semanticapi.CompletionResult, error) {
	f.reqURI = p.TextDocument.URI
	f.reqPos = p.Position
	if f.err != nil {
		return semanticapi.CompletionResult{}, f.err
	}
	return semanticapi.CompletionResult{Items: f.items}, nil
}

func (f *fakeCompletionLSP) ExecuteCommand(
	_ context.Context, p semanticapi.ExecuteCommandParams,
) (string, error) {
	if p.Command != "gopls.list_known_packages" {
		return "", nil
	}
	f.execArgs = p.Arguments
	if f.err != nil {
		return "", f.err
	}
	out, err := json.Marshal(struct{ Packages []string }{Packages: f.packages})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (f *fakeCompletionLSP) SignatureHelp(
	_ context.Context, p semanticapi.SignatureHelpParams,
) (*semanticapi.SignatureHelp, error) {
	f.reqURI = p.TextDocument.URI
	f.reqPos = p.Position
	if f.err != nil {
		return nil, f.err
	}
	return f.sigHelp, nil
}

type dirRunner struct {
	*scriptedRunner
	dir string
}

var (
	_ programRunner    = (*dirRunner)(nil)
	_ completionRunner = (*dirRunner)(nil)
)

func newDirRunner(dir string) *dirRunner {
	return &dirRunner{scriptedRunner: newScriptedRunner(), dir: dir}
}

func (r *dirRunner) programPath(content string) (string, error) {
	path := filepath.Join(r.dir, programFile)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func TestGoSessionCompletionPrefix(t *testing.T) {
	t.Parallel()
	s := newTestSession(newScriptedRunner())
	tests := []struct {
		line string
		want string
	}{
		{"fmt.Pri", "Pri"},
		{"fmt.", ""},
		{"buf.Wri", "Wri"},
		{"x", "x"},
		{"x + y", "y"},
		{"strings.ToU", "ToU"},
		{"", ""},
		{"foo(bar.Baz", "Baz"},
		{"a.b.c", "c"},
		{`import "fm`, "fm"},
		{`import "github.com/foo/`, "github.com/foo/"},
		{`import w "io`, "io"},
		{`import "`, ""},
		{"/typ", "/typ"},
		{"/", "/"},
		{"/help", "/help"},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, s.CompletionPrefix(tc.line))
		})
	}
}

func TestImportPathPrefix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line   string
		want   string
		wantOK bool
	}{
		{`import "fm`, "fm", true},
		{`import "`, "", true},
		{`import "github.com/foo/`, "github.com/foo/", true},
		{`import w "io`, "io", true},
		{`	import "fm`, "fm", true},
		{`import "fmt"`, "", false},
		{"fmt.Pri", "", false},
		{"important()", "", false},
		{"x := 1", "", false},
		{"import ", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			got, ok := importPathPrefix(tc.line)
			require.Equal(t, tc.wantOK, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestGoSessionCompletePackages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lsp := &fakeCompletionLSP{packages: []string{
		"fmt",
		"fmt/internal",
		"github.com/unstablebuild/blue/document",
		"strings",
	}}
	s := newGoSession(newDirRunner(dir), &writeFS{root: dir}, lsp)

	got, err := s.Complete(context.Background(), "import", []string{`"fm`})
	require.NoError(t, err)
	items, err := iterator.ToSlice(context.Background(), got)
	require.NoError(t, err)
	require.Equal(t, []string{"fmt", "fmt/internal"}, items)
	// gopls resolves known packages relative to a loaded file, so the
	// synthetic program is opened as an overlay, queried, then closed,
	// and its URI is forwarded as the command argument.
	wantURI := "file://" + filepath.Join(dir, programFile)
	require.Equal(t, wantURI, lsp.openedURI)
	require.Equal(t, wantURI, lsp.closedURI)
	require.Len(t, lsp.execArgs, 1)
	require.JSONEq(t, `{"URI":"`+wantURI+`"}`, string(lsp.execArgs[0]))

	got, err = s.Complete(context.Background(), "import", []string{`"github.com/`})
	require.NoError(t, err)
	items, err = iterator.ToSlice(context.Background(), got)
	require.NoError(t, err)
	require.Equal(t, []string{"github.com/unstablebuild/blue/document"}, items)
}

func TestRejoinArgs(t *testing.T) {
	t.Parallel()
	require.Equal(t, "fmt.Pri", rejoinArgs("fmt.Pri", nil))
	require.Equal(t, "x + y", rejoinArgs("x", []string{"+", "y"}))
}

func TestRenderForCompletion(t *testing.T) {
	t.Parallel()
	s := newTestSession(newScriptedRunner())
	require.NoError(t, mustSubmit(t, s, `import "fmt"`))

	src, offset := s.renderForCompletion("fmt.Pri")
	require.Equal(t, "fmt.Pri", src[offset-len("fmt.Pri"):offset])
	require.Contains(t, src, "\"fmt\"")
	require.NotContains(t, src, "_ \"fmt\"")
	require.Contains(t, src, "func main() {\n\tfmt.Pri\n}")
}

func TestByteOffsetToPosition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		src    string
		offset int
		want   semanticapi.Position
	}{
		{"start", "abc", 0, semanticapi.Position{Line: 0, Character: 0}},
		{"mid first line", "abc", 2, semanticapi.Position{Line: 0, Character: 2}},
		{"second line", "ab\ncd", 4, semanticapi.Position{Line: 1, Character: 1}},
		{"after newline", "ab\ncd", 3, semanticapi.Position{Line: 1, Character: 0}},
		{"utf16 astral", "x\n😀f", 7, semanticapi.Position{Line: 1, Character: 3}},
		{"offset past end clamps", "ab", 99, semanticapi.Position{Line: 0, Character: 2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, byteOffsetToPosition(tc.src, tc.offset))
		})
	}
}

func TestInsertText(t *testing.T) {
	t.Parallel()
	require.Equal(t, "Println", insertText(semanticapi.CompletionItem{
		Label:      "Println(a ...any)",
		InsertText: "Println",
	}))
	require.Equal(t, "Printf", insertText(semanticapi.CompletionItem{
		Label:    "Printf",
		TextEdit: &semanticapi.TextEdit{NewText: "Printf"},
	}))
	require.Equal(t, "Sprint", insertText(semanticapi.CompletionItem{
		Label: "Sprint",
	}))
}

func TestCompletionInsertsDedupes(t *testing.T) {
	t.Parallel()
	items := []semanticapi.CompletionItem{
		{Label: "Println", InsertText: "Println"},
		{Label: "Print", InsertText: "Print"},
		{Label: "Println dup", InsertText: "Println"},
		{Label: "", InsertText: ""},
	}
	require.Equal(t, []string{"Println", "Print"}, completionInserts(items))
}

func TestGoSessionCompleteGo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lsp := &fakeCompletionLSP{items: []semanticapi.CompletionItem{
		{Label: "Println(a ...any) (n int, err error)", InsertText: "Println"},
		{Label: "Printf(format string, a ...any) (n int, err error)", InsertText: "Printf"},
		{Label: "Print", InsertText: "Print"},
	}}
	s := newGoSession(newDirRunner(dir), &writeFS{root: dir}, lsp)
	require.NoError(t, mustSubmit(t, s, `import "fmt"`))

	got, err := s.Complete(context.Background(), "fmt.Pri", nil)
	require.NoError(t, err)
	items, err := iterator.ToSlice(context.Background(), got)
	require.NoError(t, err)
	require.Equal(t, []string{"Println", "Printf", "Print"}, items)

	require.NotEmpty(t, lsp.openedURI)
	require.Equal(t, lsp.openedURI, lsp.reqURI)
	require.Equal(t, lsp.openedURI, lsp.closedURI)
	require.True(t, strings.HasPrefix(lsp.openedURI, "file://"))
	require.Contains(t, lsp.openedText, "fmt.Pri")
	require.Equal(t, uint32(len("\tfmt.Pri")), lsp.reqPos.Character)
}

func TestGoSessionCompleteGoNoLSP(t *testing.T) {
	t.Parallel()
	s := newGoSession(newDirRunner(t.TempDir()), &writeFS{root: t.TempDir()}, nil)
	got, err := s.Complete(context.Background(), "fmt.Pri", nil)
	require.NoError(t, err)
	items, err := iterator.ToSlice(context.Background(), got)
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestGoSessionCompleteGoLSPError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lsp := &fakeCompletionLSP{err: errors.New("boom")}
	s := newGoSession(newDirRunner(dir), &writeFS{root: dir}, lsp)
	got, err := s.Complete(context.Background(), "fmt.Pri", nil)
	require.NoError(t, err)
	items, err := iterator.ToSlice(context.Background(), got)
	require.NoError(t, err)
	require.Empty(t, items)
}

func TestGoSessionSignatureHelp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lsp := &fakeCompletionLSP{sigHelp: &semanticapi.SignatureHelp{
		Signatures: []semanticapi.SignatureInformation{{
			Label: "Println(a ...any) (n int, err error)",
			Parameters: []semanticapi.ParameterInformation{
				{Label: "a ...any"},
			},
		}},
		ActiveParameter: 0,
	}}
	s := newGoSession(newDirRunner(dir), &writeFS{root: dir}, lsp)
	require.NoError(t, mustSubmit(t, s, `import "fmt"`))

	line := "fmt.Println("
	label, ok := s.SignatureHelp(context.Background(), line, len([]rune(line)))
	require.True(t, ok)
	require.Equal(t, "Println([a ...any]) (n int, err error)", label)

	require.NotEmpty(t, lsp.openedURI)
	require.Equal(t, lsp.openedURI, lsp.reqURI)
	require.Equal(t, lsp.openedURI, lsp.closedURI)
	require.Contains(t, lsp.openedText, "fmt.Println(")
	require.Equal(t, uint32(len("\tfmt.Println(")), lsp.reqPos.Character)
}

func TestGoSessionSignatureHelpNoLSP(t *testing.T) {
	t.Parallel()
	s := newGoSession(newDirRunner(t.TempDir()), &writeFS{root: t.TempDir()}, nil)
	_, ok := s.SignatureHelp(context.Background(), "fmt.Println(", 12)
	require.False(t, ok)
}

func TestGoSessionSignatureHelpNoSignatures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	lsp := &fakeCompletionLSP{sigHelp: &semanticapi.SignatureHelp{}}
	s := newGoSession(newDirRunner(dir), &writeFS{root: dir}, lsp)
	_, ok := s.SignatureHelp(context.Background(), "fmt.Println(", 12)
	require.False(t, ok)
}

func TestSignatureLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		help *semanticapi.SignatureHelp
		want string
		ok   bool
	}{
		{name: "nil", help: nil, ok: false},
		{name: "empty", help: &semanticapi.SignatureHelp{}, ok: false},
		{
			name: "active param by substring",
			help: &semanticapi.SignatureHelp{
				Signatures: []semanticapi.SignatureInformation{{
					Label: "Printf(format string, a ...any) (n int, err error)",
					Parameters: []semanticapi.ParameterInformation{
						{Label: "format string"},
						{Label: "a ...any"},
					},
				}},
				ActiveParameter: 1,
			},
			want: "Printf(format string, [a ...any]) (n int, err error)",
			ok:   true,
		},
		{
			name: "active param by offsets",
			help: &semanticapi.SignatureHelp{
				Signatures: []semanticapi.SignatureInformation{{
					Label: "f(a int, b int)",
					Parameters: []semanticapi.ParameterInformation{
						{LabelOffsets: &[2]uint32{2, 7}},
						{LabelOffsets: &[2]uint32{9, 14}},
					},
				}},
				ActiveParameter: 0,
			},
			want: "f([a int], b int)",
			ok:   true,
		},
		{
			name: "no params returns plain label",
			help: &semanticapi.SignatureHelp{
				Signatures: []semanticapi.SignatureInformation{{Label: "now() time.Time"}},
			},
			want: "now() time.Time",
			ok:   true,
		},
		{
			name: "active signature selection",
			help: &semanticapi.SignatureHelp{
				Signatures: []semanticapi.SignatureInformation{
					{Label: "first()"},
					{Label: "second()"},
				},
				ActiveSignature: 1,
			},
			want: "second()",
			ok:   true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			label, ok := signatureLabel(tc.help)
			require.Equal(t, tc.ok, ok)
			if tc.ok {
				require.Equal(t, tc.want, label)
			}
		})
	}
}

func TestRejoin(t *testing.T) {
	t.Parallel()
	require.Equal(t, "1+1", rejoin(repl.Command{Name: "1+1"}))
	require.Equal(t, "x := 5",
		rejoin(repl.Command{Name: "x", Args: []string{":=", "5"}}))
	require.Equal(t, "/doc fmt",
		rejoin(repl.Command{Name: "/doc", Args: []string{"fmt"}}))
}

func TestOutputRows(t *testing.T) {
	t.Parallel()
	require.Empty(t, collect(t, outputRows("")))
	require.Equal(t, []string{"one"}, collect(t, outputRows("one")))
	require.Equal(t, []string{"a", "b"}, collect(t, outputRows("a\nb")))
}

func TestRunFailure(t *testing.T) {
	t.Parallel()
	base := errors.New("exit status 1")
	require.Equal(t, base, runFailure("", base))
	require.EqualError(t, runFailure("compiler said no", base), "compiler said no")
}

// mustSubmit runs a line expecting success, returning only the error so
// callers can require.NoError at the call site.
func mustSubmit(t *testing.T, s *goSession, line string) error {
	t.Helper()
	_, err := submit(t, s, line)
	return err
}

// --- window wrapper / scheduler ---------------------------------------

// nopInterrupter records Interrupt calls without touching a real event
// loop. calls is atomic because the scheduler is woken from multiple
// goroutines (the SDK command dispatch and the async signature-help
// fetch), mirroring how the production scheduler is shared.
type nopInterrupter struct{ calls atomic.Int64 }

func (n *nopInterrupter) Interrupt(context.Context) error {
	n.calls.Add(1)
	return nil
}

func TestTickSchedulerDrains(t *testing.T) {
	t.Parallel()
	ti := &nopInterrupter{}
	sched := newTickScheduler(ti)

	var ran []int
	sched.schedule(func() { ran = append(ran, 1) })
	sched.schedule(func() { ran = append(ran, 2) })
	require.Equal(t, int64(2), ti.calls.Load(), "each schedule must wake the loop")
	require.Empty(t, ran, "callbacks run only on drain")

	sched.drain()
	require.Equal(t, []int{1, 2}, ran, "FIFO order")

	sched.drain()
	require.Equal(t, []int{1, 2}, ran, "drain is idempotent once empty")
}

func newTestREPLHandler(t *testing.T) (*drainHandler, *tickScheduler) {
	t.Helper()
	ti := &nopInterrupter{}
	sched := newTickScheduler(ti)
	shell, _ := ideshell.New(
		sched.schedule,
		ti,
		commandEditor{te: standard.Editor()},
		ideshell.Config{
			DisableShellInterpreter: newTestSession(newScriptedRunner()),
			Prompt:                  "go> ",
		},
	)
	t.Cleanup(func() { _ = shell.Close() })
	h := &drainHandler{Handler: shell, sched: sched}
	h.Resize(80, 24)
	return h, sched
}

// TestDrainHandlerDrainsBeforeHandle proves the wrapper applies
// scheduled (async command) callbacks before delegating an event, so
// output produced off the event loop is visible on the next interaction.
func TestDrainHandlerDrainsBeforeHandle(t *testing.T) {
	t.Parallel()
	h, sched := newTestREPLHandler(t)

	drained := false
	sched.schedule(func() { drained = true })

	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	require.True(t, drained, "Handle must drain scheduled callbacks first")
}

// TestDrainHandlerForwardsMouse proves mouse events (e.g. wheel scroll,
// which the ideshell handler supports) are forwarded to the embedded
// handler rather than swallowed by the wrapper.
func TestDrainHandlerForwardsMouse(t *testing.T) {
	t.Parallel()
	h, _ := newTestREPLHandler(t)

	for _, key := range []term.Key{term.MouseWheelUp, term.MouseWheelDown, term.MouseLeft} {
		ev := term.Event{Type: term.EventMouse, Key: key, MouseX: 1, MouseY: 1}
		require.NotPanics(t, func() { h.Handle(ev) },
			"wrapper must forward mouse event %v", key)
	}
}

// assertSlice asserts got matches want, treating a non-nil empty want as
// "must be empty" (the session leaves cleared slices nil).
func assertSlice(t *testing.T, want, got []string, step int, label string) {
	t.Helper()
	if want == nil {
		return
	}
	if len(want) == 0 {
		require.Empty(t, got, "step %d %s", step, label)
		return
	}
	require.Equal(t, want, got, "step %d %s", step, label)
}

// --- temp-dir FileSystem for /write -----------------------------------

// writeFS is a minimal workspaceapi.FileSystem backed by a real temp
// directory; only OpenFile is exercised by /write, the rest are stubs.
type writeFS struct {
	root string
}

var _ workspaceapi.FileSystem = (*writeFS)(nil)

func (w *writeFS) OpenFile(name string, flag int, mode os.FileMode) (workspaceapi.File, error) {
	return os.OpenFile(filepath.Join(w.root, name), flag, mode)
}

func (w *writeFS) URI(p string) (workspaceapi.URI, error) {
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.root, p)
	}
	return workspaceapi.ParseURI("file://" + p)
}

func (w *writeFS) Remove(name string) error {
	return os.Remove(filepath.Join(w.root, name))
}

func (w *writeFS) MkdirAll(name string, perm os.FileMode) error {
	return os.MkdirAll(filepath.Join(w.root, name), perm)
}

func (w *writeFS) Stat(name string) (os.FileInfo, error) {
	return os.Stat(filepath.Join(w.root, name))
}

func (w *writeFS) ReadDir(name string) ([]os.DirEntry, error) {
	return os.ReadDir(filepath.Join(w.root, name))
}

// --- real toolchain runner --------------------------------------------

// goRunRunner is a programRunner backed by the real `go run` toolchain in
// a throwaway module. It is what proves the rendered programs actually
// compile and run, catching multi-value / void / import regressions that
// the scripted runner cannot.
type goRunRunner struct {
	dir string
}

var _ programRunner = (*goRunRunner)(nil)

func newGoRunRunner(t *testing.T) *goRunRunner {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"), []byte("module replroot\n\ngo 1.22\n"), 0o644))
	return &goRunRunner{dir: dir}
}

func (r *goRunRunner) run(ctx context.Context, program string) (string, error) {
	f, err := os.CreateTemp(r.dir, "rune-repl-*.go")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := f.WriteString(program); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "go", "run", path)
	cmd.Dir = r.dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return strings.TrimRight(stderr.String(), "\n"), err
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

// TestGoREPLEvalEndToEnd runs rendered programs through the real go
// toolchain to prove the basic REPL surface works: scalars, value and
// side-effecting calls, void calls, and unused imports.
func TestGoREPLEvalEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping toolchain test in -short mode")
	}
	t.Parallel()
	tests := []struct {
		name  string
		lines []string
		want  []string // output of the final line
	}{
		{"scalar expression", []string{"1 + 1"}, []string{"2"}},
		{"value call prints its result", []string{`import "strings"`, `strings.ToUpper("hi")`}, []string{`"HI"`}},
		{"multi-value call groups return values as a tuple",
			[]string{`import "fmt"`, `fmt.Println("hello")`}, []string{"hello", "(6, <nil>)"}},
		{"void call runs for side effects", []string{
			"func greet() { println(`hi`) }",
			"greet()",
		}, []string{}},
		{"unused import does not error", []string{`import "math"`}, []string{}},
		{"import then use across lines", []string{`import "math"`, "math.MaxInt8"}, []string{"127"}},
		{"statement accumulates then expression", []string{"x := 21", "x * 2"}, []string{"42"}},
		{"short var decl prints its value", []string{"x := 21"}, []string{"21"}},
		{"multi-assign prints all values", []string{"a, b := 1, 2"}, []string{"1", "2"}},
		{"declared var stays usable after later lines", []string{"x := 1", "y := 2", "x + y"}, []string{"3"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newGoSession(newGoRunRunner(t), nil, nil)
			var out []string
			for i, line := range tc.lines {
				var err error
				out, err = submit(t, s, line)
				require.NoError(t, err, "line %d: %q", i, line)
			}
			require.Equal(t, tc.want, out)
		})
	}
}

// --- ideshell wiring --------------------------------------------------

func TestGoSessionHelp(t *testing.T) {
	t.Parallel()
	s := newTestSession(newScriptedRunner())
	it, err := s.Help(context.Background(), nil)
	require.NoError(t, err)
	require.Equal(t, replHelp(), collect(t, it))
}

// TestCommandEditorAdapter proves the text.Editor -> command.Editor
// bridge returns a working command.EditHandler bound to the buffer.
func TestCommandEditorAdapter(t *testing.T) {
	t.Parallel()
	ed := commandEditor{te: standard.Editor()}
	buf := cell.NewBuffer()
	h := ed.Edit(buf)
	require.NotNil(t, h)
	h.Resize(80, 1)
	h.Handle(term.Event{Type: term.EventKey, Ch: 'x'})
	require.Equal(t, "x", strings.TrimRight(buf.String(), "\n"))
}

// fakeWindow is a minimal browserapi.Window for WM tests.
type fakeWindow struct{ id uint64 }

func (w fakeWindow) WindowID() uint64 { return w.id }

// fakeWM is a minimal browserapi.WindowManager that records the tab it
// is asked to create and the content set on its focused window.
//
// autoConfirm makes it answer a floated code-action picker by pressing
// Enter, so tests that assert what gopls produced do not each have to
// drive the confirmation UI.
type fakeWM struct {
	mu          sync.Mutex
	tabHandler  browserapi.Handler
	content     browserapi.Handler
	floating    browserapi.Floating
	autoConfirm bool
}

func (m *fakeWM) Focus() (browserapi.Window, error) {
	return fakeWindow{id: 1}, nil
}

func (m *fakeWM) Tab(
	_ workspaceapi.URI, _ rune, _ string, h browserapi.Handler,
) (browserapi.Handler, error) {
	m.tabHandler = h
	return h, nil
}

func (m *fakeWM) SetWindowContent(_ browserapi.Window, h browserapi.Handler) error {
	m.content = h
	return nil
}

func (m *fakeWM) Split(
	browserapi.Orientation, browserapi.Window, browserapi.Handler,
) (browserapi.Window, error) {
	return nil, errors.New("not implemented")
}

func (m *fakeWM) Floating(
	h browserapi.Floating, _ browserapi.FloatingConfig,
) (browserapi.Window, error) {
	m.mu.Lock()
	m.floating = h
	auto := m.autoConfirm
	m.mu.Unlock()
	if !auto {
		return nil, errors.New("not implemented")
	}
	h.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
	return fakeWindow{id: 2}, nil
}

// floated returns the most recently floated handler, if any.
func (m *fakeWM) floated() browserapi.Floating {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.floating
}

func (m *fakeWM) Bar(browserapi.BarConfig, tui.Handler) error {
	return errors.New("not implemented")
}

func (m *fakeWM) CloseWindow(browserapi.Window) error { return nil }

func (m *fakeWM) SetTabActivity(workspaceapi.URI, bool) error { return nil }

// TestNewREPLSubcommandOpensShell drives the repl subcommand end to end
// against a fake window manager and asserts it installs an ideshell
// handler whose `go` REPL command is registered and dispatches Go
// fragments through the session.
func TestNewREPLSubcommandOpensShell(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "go.mod"), []byte("module x\n\ngo 1.22\n"), 0o644))
	fs := &writeFS{root: dir}
	wm := &fakeWM{}

	sub := newREPLSubcommand(wm, &nopInterrupter{}, fs, nil, nil, nil, nil)
	err := sub.HandleCommand(context.Background(), textapi.Command{Name: "repl"})
	require.NoError(t, err)
	require.NotNil(t, wm.content, "repl must set the window content")
	require.Same(t, wm.tabHandler, wm.content,
		"the created tab must become the window content")

	// Re-invoking re-focuses the existing tab rather than building a
	// second one.
	prev := wm.tabHandler
	require.NoError(t, sub.HandleCommand(
		context.Background(), textapi.Command{Name: "repl"}))
	require.Same(t, prev, wm.tabHandler, "repl tab is single-instance")
}

// TestREPLShellConfigPersistsHistory is a regression test for reverse
// search (<c-r>) showing nothing: the Go REPL must wire storage so
// submitted commands persist and become searchable.
// TestREPLShellConfigWiresClearHook verifies that <c-l> (the shell's
// screen clear, surfaced as Config.ClearHook) resets the accumulated
// session so a redeclared variable does not collide with a definition
// that scrolled off-screen.
func TestREPLShellConfigWiresClearHook(t *testing.T) {
	t.Parallel()
	sub := &replSubcommand{}
	session := newTestSession(newScriptedRunner())
	require.NoError(t, mustSubmit(t, session, "x := 1"))
	require.NotEmpty(t, session.stmts)

	cfg := sub.shellConfig(session, workspaceapi.URI{}, false)
	require.NotNil(t, cfg.ClearHook, "the screen-clear hook must be wired")
	cfg.ClearHook()
	require.Empty(t, session.stmts,
		"clearing the screen must reset the accumulated program")
	require.Empty(t, session.declared)
}

func TestREPLShellConfigPersistsHistory(t *testing.T) {
	t.Parallel()
	store := storagestub.NewInMemoryService()
	sub := &replSubcommand{storage: store}

	cfg := sub.shellConfig(
		newTestSession(newScriptedRunner()), workspaceapi.URI{}, false)
	require.Same(t, store, cfg.Storage,
		"history search reads from storage, so it must be wired")
	require.NotEmpty(t, cfg.HistoryDocumentID,
		"a history document id is required to persist commands")

	shell, _ := ideshell.New(
		func(func()) bool { return false }, &nopInterrupter{},
		commandEditor{te: standard.Editor()}, cfg,
	)
	t.Cleanup(func() { _ = shell.Close() })
	shell.Resize(80, 24)
	// Submit serially: each command dispatches on its own goroutine, so
	// wait for one to finish before the next touches shared session
	// state.
	shell.Submit("1 + 1")
	shell.Wait()
	shell.Submit("2 + 2")
	shell.Wait()

	var doc struct {
		Items   []string
		Version int
	}
	require.NoError(t, store.Get(
		context.Background(), cfg.HistoryDocumentID, &doc))
	require.Equal(t, []string{"1 + 1", "2 + 2"}, doc.Items,
		"submitted commands must be persisted for reverse search")
}
