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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

// Source files used across tests.
const (
	// mainSrc is a basic Go file with an unused import, a struct, and
	// functions that exercise various code actions.
	mainSrc = `package main

import (
	"fmt"
	"strings"
)

// Greeter holds a name.
type Greeter struct {
	Name string
}

// Greet returns a greeting.
func (g *Greeter) Greet() string {
	return fmt.Sprintf("Hello, %s!", g.Name)
}

func Add(a, b int) int {
	return a + b
}

func main() {
	g := &Greeter{Name: "World"}
	fmt.Println(g.Greet())
	_ = strings.ToUpper("test")
}
`

	// testFileSrc is a test file with a test function and a benchmark.
	testFileSrc = `package main

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Error("expected 3")
	}
}

func BenchmarkAdd(b *testing.B) {
	for i := 0; i < b.N; i++ {
		Add(1, 2)
	}
}
`

	// fillStructSrc has an incomplete struct literal.
	fillStructSrc = `package main

import "net/http"

func newRequest() *http.Request {
	return &http.Request{}
}
`

	// extractFuncSrc has code suitable for extract-function.
	extractFuncSrc = `package main

import "fmt"

func compute() {
	a := 1
	b := 2
	sum := a + b
	fmt.Println(sum)
}
`

	// invertIfSrc has an if/else block suitable for inversion.
	invertIfSrc = `package main

import "fmt"

func check(x int) {
	if x > 0 {
		fmt.Println("positive")
	} else {
		fmt.Println("non-positive")
	}
}
`

	// unusedImportSrc has an unused import that organize-imports should remove.
	unusedImportSrc = `package main

import (
	"fmt"
	"os"
)

func hello() string {
	return fmt.Sprintf("hello")
}
`

	// addTagsSrc has a struct suitable for add-tags / remove-tags.
	addTagsSrc = `package main

type Config struct {
	Host string
	Port int
}
`

	// typeSwitchSrc has an incomplete type switch suitable for fill-switch.
	typeSwitchSrc = `package main

import "fmt"

type Animal interface {
	Sound() string
}

type Dog struct{}
func (d Dog) Sound() string { return "woof" }

type Cat struct{}
func (c Cat) Sound() string { return "meow" }

func describe(a Animal) {
	switch a.(type) {
	}
	fmt.Println(a.Sound())
}
`

	// inlineCallSrc has a simple function suitable for inline-call.
	inlineCallSrc = `package main

func double(x int) int {
	return x * 2
}

func useDouble() int {
	return double(5)
}
`

	// extractMethodSrc has a struct with a method containing multiple statements.
	extractMethodSrc = `package main

import "fmt"

type Counter struct {
	total int
}

func (c *Counter) Process(a, b int) {
	sum := a + b
	c.total += sum
	fmt.Println(c.total)
}
`

	// extractConstantSrc has a function with a repeated constant literal.
	extractConstantSrc = `package main

func magicNumbers() (int, int) {
	x := 42
	y := 42
	return x, y
}
`

	// extractVarAllSrc has a function with a duplicated expression
	// suitable for extract-variable-all.
	extractVarAllSrc = `package main

import "fmt"

func compute() {
	a := 1
	b := 2
	fmt.Println(a + b)
	fmt.Println(a + b)
}
`

	// inlineVariableSrc has a function with a local variable to inline.
	inlineVariableSrc = `package main

import "fmt"

func f(x int) {
	s := fmt.Sprintf("+%d", x)
	println(s)
}
`

	// extractToNewFileSrc has two top-level functions (one to move).
	extractToNewFileSrc = `package main

import "fmt"

func Stay() {
	fmt.Println("stay")
}

func MoveMe() {
	fmt.Println("move")
}
`

	// generateSrc has a go:generate directive.
	generateSrc = `package main

//go:generate echo hello

func generated() {}
`

	// removeUnusedParamSrc has a function with an unused parameter.
	removeUnusedParamSrc = `package main

func add(a, b, unused int) int {
	return a + b
}

func main() {
	_ = add(1, 2, 0)
}
`

	// moveParamSrc has a function with two params suitable for move-param-left/right.
	moveParamSrc = `package main

func greet(greeting, name string) string {
	return greeting + " " + name
}

func main() {
	_ = greet("hello", "world")
}
`

	// changeQuoteSrc has a raw string literal suitable for change-quote.
	changeQuoteSrc = `package main

import "fmt"

func main() {
	s := ` + "`hello world`" + `
	fmt.Println(s)
}
`

	// splitLinesSrc has a multi-arg call on a single line suitable for split-lines.
	splitLinesSrc = `package main

import "fmt"

func main() {
	fmt.Println("a", "b", "c")
}
`

	// joinLinesSrc has a multi-line call suitable for join-lines.
	joinLinesSrc = `package main

import "fmt"

func main() {
	fmt.Println(
		"a",
		"b",
		"c",
	)
}
`

	// eliminateDotImportSrc has a dot import suitable for eliminate-dot-import.
	eliminateDotImportSrc = `package main

import . "fmt"

func main() {
	Println("hello")
}
`

	// cgoSrc is a minimal Go file with import "C" for regenerate-cgo test.
	cgoSrc = `package main

// #include <stdlib.h>
import "C"

func main() {}
`
)

func TestE2EGoplsLoggingConfigReachesProcess(t *testing.T) {
	goplsBin := findGopls(t)
	dir := setupWorkspace(t, "example.com/logging", []testFile{
		{name: "main.go", content: "package main\n\nfunc main() {}\n"},
	})
	rootURI := "file://" + dir
	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newTestSchemeRooted(dir)
	mgr := idelsp.New(uri, scheme, scheme, &stubPkgManager{bin: goplsBin}, nil, nil,
		idelsp.Config{MaxRetries: 1, Callback: &testCallback{},
			CloseTimeout: realLSPCloseTimeout})
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	cfg := config.JSONFromMap(map[string]any{
		"lsp_path": goplsBin,
		"debug": map[string]any{
			"log_level": "trace",
			"rpc_trace": true,
		},
	})
	err = initializeGoRoot(t.Context(), scheme, scheme, nil, mgr, &langext.Tools{}, "file", cfg,
		langext.Root{Dir: dir, URI: rootURI})
	require.NoError(t, err)

	started, ok := scheme.startedProcess(goplsBin)
	require.True(t, ok, "gopls process was not started")
	assert.Equal(t, []string{"-rpc.trace", "-vv", "serve"}, started.args)
	require.Eventually(t, func() bool {
		return strings.Contains(started.stderr.String(), "initialize")
	}, 5*time.Second, 20*time.Millisecond, "gopls did not write the initialize trace to stderr")
}

func TestE2E(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)

	t.Run("OrganizeImports", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: unusedImportSrc},
		})
		handler, me, _ := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		cmd := goCmdAt("organize-imports", uri, resource, 0, 0)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		edits := me.editsFor(resource)
		require.NotEmpty(t, edits, "expected edits for organize-imports")

		combined := collectEditText(edits)
		assert.NotContains(t, combined, `"os"`)

		// Verify gopls overlay is in sync after the edit.
		// Hover on fmt.Sprintf (still present after removing unused os).
		hover, err := env.mgr.Hover(t.Context(), semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: env.fileURIs["main.go"]},
			Position:     semanticapi.Position{Line: 0, Character: 8},
		})
		require.NoError(t, err, "gopls should respond after overlay update")
		require.NotNil(t, hover, "expected hover on package name after edit")
	})

	t.Run("FixAll", func(t *testing.T) {
		t.Parallel()
		// source.fixAll applies safe diagnostic fixes (e.g. simplifyrange).
		// Whether gopls produces fixAll actions depends on the analyzer
		// configuration and gopls version. This test verifies the handler
		// runs the code action flow correctly.
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: mainSrc},
		})
		handler, me, mn := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		cmd := goCmdAt("fix-all", uri, resource, 0, 0)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		edits := me.editsFor(resource)
		if len(edits) > 0 {
			// If edits were produced, verify gopls overlay is in sync.
			hover, err := env.mgr.Hover(t.Context(), semanticapi.HoverParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: env.fileURIs["main.go"]},
				Position:     semanticapi.Position{Line: 0, Character: 8},
			})
			require.NoError(t, err, "gopls should respond after fix-all overlay update")
			require.NotNil(t, hover)
		} else {
			// No fixAll actions: handler should notify.
			assert.True(t, mn.hasMessage("No automatic fixes available"))
		}
	})

	t.Run("FillStruct", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: fillStructSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on the empty struct literal `http.Request{}` (line 5, char 18).
		cmd := goCmdAt("fill-struct", uri, resource, 5, 18)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// fill-struct returns a Command (not a direct Edit).
		// ExecuteCommand triggers workspace/applyEdit to fill fields.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "Method")
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Fill http.Request"))
	})

	t.Run("FillSwitch", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: typeSwitchSrc},
		})
		handler, me, mn := newTestHandlerWithEditorEvents(t, env)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on the empty type switch (line 15, char 2).
		cmd := goCmdAt("fill-switch", uri, resource, 15, 2)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// fill-switch returns a direct Edit (not a Command).
		edits := me.editsFor(resource)
		require.Equal(t, 1, len(edits))
		combined := collectEditText(edits)
		assert.Contains(t, combined, "Dog")
		assert.Contains(t, combined, "Cat")
		assert.Equal(t, 0, len(mn.getMessages()))
	})

	t.Run("ExtractFunction", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: extractFuncSrc},
		})
		handler, me, _ := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Fire a selection event covering all statements in compute()
		// (lines 5-8: "a := 1" through "fmt.Println(sum)").
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 1, Y: 5},
			End:   term.Coordinates{X: 17, Y: 8},
		})

		cmd := goCmdAt("extract-function", uri, resource, 5, 1)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies extract-function via workspace/applyEdit
		// (the code action returns a Command, not a direct Edit).
		captured := env.capturedEdits()
		require.NotEmpty(t, captured, "expected workspace/applyEdit from extract-function")

		// Verify the edit contains a new function definition.
		var editText strings.Builder
		for _, ae := range captured {
			for _, edits := range ae.Edit.Changes {
				for _, e := range edits {
					editText.WriteString(e.NewText)
				}
			}
			for _, dc := range ae.Edit.DocumentChanges {
				if dc.TextDocumentEdit != nil {
					for _, e := range dc.TextDocumentEdit.Edits {
						editText.WriteString(e.NewText)
					}
				}
			}
		}
		assert.Contains(t, editText.String(), "newFunction",
			"extracted function should be named newFunction (gopls default)")

		// --- Second extraction from the newly extracted function. ---
		// If gopls's overlay is in sync with the applied edit, it
		// should be able to extract a subset of newFunction's body.
		// If the overlay is stale, gopls still sees the old source
		// and the selected lines won't correspond to extractable code.
		ctx := t.Context()

		// Wait for gopls to learn about newFunction (overlay sync).
		var fnStartLine uint32
		require.Eventually(t, func() bool {
			syms, err := env.mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
				Query: "newFunction",
			})
			if err != nil {
				return false
			}
			for _, s := range syms {
				if s.Name == "newFunction" &&
					strings.Contains(s.Location.URI, env.dir) {
					fnStartLine = s.Location.Range.Start.Line
					return true
				}
			}
			return false
		}, 5*time.Second, 100*time.Millisecond,
			"gopls should know about 'newFunction' after first extract")

		// newFunction body starts at fnStartLine+1. Select the first
		// two statements (a := 1, b := 2) for the second extraction.
		// Use X=0 on the line past the selection to cover full lines.
		bodyStart := int(fnStartLine) + 1

		me.fireEvent(ctx, textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 0, Y: bodyStart},
			End:   term.Coordinates{X: 0, Y: bodyStart + 2},
		})

		prevCount := len(env.capturedEdits())
		cmd2 := goCmdAt("extract-function", uri, resource, bodyStart, 1)

		err = handler.HandleCommand(ctx, cmd2)
		require.NoError(t, err,
			"second extract-function should not error if gopls overlay is in sync")

		captured2 := env.capturedEdits()
		require.Greater(t, len(captured2), prevCount,
			"gopls overlay stale: second extract-function produced no workspace/applyEdit")
	})

	t.Run("ExtractVariable", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: extractFuncSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select the expression "a + b" on line 7 (sum := a + b).
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 8, Y: 7},
			End:   term.Coordinates{X: 13, Y: 7},
		})

		cmd := goCmdAt("extract-variable", uri, resource, 7, 8)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// extract-variable returns a Command (not a direct Edit).
		// ExecuteCommand triggers workspace/applyEdit to extract the expression.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "a + b")
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Extract variable"))
	})

	t.Run("InvertIf", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: invertIfSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on the if keyword (line 5, char 1).
		cmd := goCmdAt("invert-if", uri, resource, 5, 1)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// invert-if returns a Command (not a direct Edit).
		// ExecuteCommand triggers workspace/applyEdit to invert the condition.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "<=")
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Invert 'if' condition"))
	})

	t.Run("InlineCall", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: inlineCallSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on `double` call (line 7, char 8).
		cmd := goCmdAt("inline-call", uri, resource, 7, 8)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// inline-call returns a Command (not a direct Edit).
		// ExecuteCommand triggers workspace/applyEdit to inline the call.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		// gopls replaces `double(5)` with `5 * 2` across multiple edits;
		// the literal "5" stays in place and "* 2" is the new text.
		assert.Contains(t, editText, "* 2")
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Inline call to double"))
	})

	t.Run("ExtractMethod", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: extractMethodSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Fire a selection event covering the statements inside Process()
		// (lines 9-11: "sum := a + b" through "fmt.Println(c.total)").
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 1, Y: 9},
			End:   term.Coordinates{X: 21, Y: 11},
		})

		cmd := goCmdAt("extract-method", uri, resource, 9, 1)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies extract-method via workspace/applyEdit command.
		// The extracted method should be on the same receiver (*Counter).
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "Counter")
		assert.Contains(t, editText, "newMethod")
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Extract method"))
	})

	t.Run("ExtractVariableAll", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: extractVarAllSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select the expression "a + b" (line 7, chars 13-18) which
		// appears twice in extractVarAllSrc.
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 13, Y: 7},
			End:   term.Coordinates{X: 18, Y: 7},
		})

		cmd := goCmdAt("extract-variable-all", uri, resource, 7, 13)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies extract-variable-all via workspace/applyEdit
		// command, replacing both occurrences of "a + b" with a new variable.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "a + b")
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Extract 2 occurrences of a + b"))
	})

	t.Run("ExtractConstant", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: extractConstantSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select the literal "42" on line 4 (x := 42), chars 6-8.
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 6, Y: 4},
			End:   term.Coordinates{X: 8, Y: 4},
		})

		cmd := goCmdAt("extract-constant", uri, resource, 4, 6)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies extract-constant via workspace/applyEdit command.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Extract constant"))
	})

	t.Run("ExtractConstantAll", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: extractConstantSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select the literal "42" on line 4 (x := 42), chars 6-8.
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 6, Y: 4},
			End:   term.Coordinates{X: 8, Y: 4},
		})

		cmd := goCmdAt("extract-constant-all", uri, resource, 4, 6)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies extract-constant-all via workspace/applyEdit command,
		// replacing both occurrences of the literal 42.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Extract 2 occurrences of const expression: 42"))
	})

	t.Run("ExtractToNewFile", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: extractToNewFileSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select the MoveMe function (lines 8-10).
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 0, Y: 8},
			End:   term.Coordinates{X: 1, Y: 10},
		})

		cmd := goCmdAt("extract-to-new-file", uri, resource, 8, 0)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies extract-to-new-file via workspace/applyEdit command.
		// The edit creates a new file with MoveMe and its import.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "MoveMe")
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Extract declarations to new file"))
	})

	t.Run("InlineVariable", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: inlineVariableSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on the *use* of variable "s" in println(s) (line 6, char 9).
		// refactor.inline.variable requires the cursor on a reference, not
		// the declaration.
		cmd := goCmdAt("inline-variable", uri, resource, 6, 9)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies inline-variable via workspace/applyEdit command,
		// replacing the reference to "s" with its initializer expression.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "fmt.Sprintf")
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage(`Executed: Inline variable "s"`))
	})

	t.Run("RemoveUnusedParam", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: removeUnusedParamSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on "unused" param (line 2, char 15).
		cmd := goCmdAt("remove-unused-param", uri, resource, 2, 15)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies remove-unused-param via workspace/applyEdit command.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		// The edit removes the "unused" param from the signature and its
		// corresponding argument from the call site.
		editText := capturedEditText(captured)
		assert.NotContains(t, editText, "unused")
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Remove unused parameter"))
	})

	t.Run("MoveParamLeft", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: moveParamSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on "name" param (line 2, char 21).
		cmd := goCmdAt("move-param-left", uri, resource, 2, 21)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies move-param-left via workspace/applyEdit command.
		// The edit rewrites the param list with "name" before "greeting",
		// and reorders call-site arguments to match.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "name")
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Move parameter left"))
	})

	t.Run("MoveParamRight", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: moveParamSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on "greeting" param (line 2, char 11).
		cmd := goCmdAt("move-param-right", uri, resource, 2, 11)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies move-param-right via workspace/applyEdit command.
		// The edit rewrites the param list with "name" before "greeting",
		// and reorders call-site arguments to match.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "name")
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Move parameter right"))
	})

	t.Run("ChangeQuote", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: changeQuoteSrc},
		})
		handler, me, mn := newTestHandlerWithEditorEvents(t, env)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor inside the raw string literal (line 5, char 6).
		cmd := goCmdAt("change-quote", uri, resource, 5, 6)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies change-quote as a direct edit (raw → interpreted),
		// not via workspace/applyEdit command.
		edits := me.editsFor(resource)
		assert.Equal(t, 1, len(edits))
		assert.Contains(t, collectEditText(edits), `"hello world"`)
		assert.Equal(t, 0, len(env.cb.appliedEdits))
		assert.Equal(t, 0, len(mn.getMessages()))
	})

	t.Run("SplitLines", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: splitLinesSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor inside the Println call args (line 5, char 15).
		cmd := goCmdAt("split-lines", uri, resource, 5, 15)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies split-lines via a command (workspace/applyEdit).
		// The edit inserts newlines between arguments.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		assert.Contains(t, capturedEditText(captured), "\n")
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Split arguments into separate lines"))
	})

	t.Run("JoinLines", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: joinLinesSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor inside the multi-line Println call (line 6, char 2).
		cmd := goCmdAt("join-lines", uri, resource, 6, 2)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies join-lines via a command (workspace/applyEdit).
		// The edit collapses multi-line arguments into one line.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		assert.NotEmpty(t, capturedEditText(captured))
		assert.Equal(t, 1, len(mn.getMessages()))
		assert.True(t, mn.hasMessage("Executed: Join arguments into one line"))
	})

	t.Run("EliminateDotImport", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: eliminateDotImportSrc},
		})
		handler, me, mn := newTestHandlerWithEditorEvents(t, env)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on the dot import (line 2, char 7).
		cmd := goCmdAt("eliminate-dot-import", uri, resource, 2, 7)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls applies eliminate-dot-import as direct edits (not via
		// workspace/applyEdit): one to replace the dot import with a
		// named import, one to qualify the Println reference with "fmt.".
		edits := me.editsFor(resource)
		assert.Equal(t, 2, len(edits))
		assert.Contains(t, collectEditText(edits), "fmt.")
		assert.Equal(t, 0, len(env.cb.appliedEdits))
		assert.Equal(t, 0, len(mn.getMessages()))
	})

	t.Run("AddTags", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: addTagsSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on the Host field (line 3, char 1).
		cmd := goCmdAt("add-tags", uri, resource, 3, 1)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// add-tags returns a Command (not a direct Edit).
		// ExecuteCommand triggers workspace/applyEdit to add json tags.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "json")
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Add struct tags"))
	})

	t.Run("RemoveTags", func(t *testing.T) {
		t.Parallel()
		taggedSrc := `package main

type Config struct {
	Host string ` + "`json:\"host\"`" + `
	Port int    ` + "`json:\"port\"`" + `
}
`
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: taggedSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		cmd := goCmdAt("remove-tags", uri, resource, 3, 1)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// remove-tags returns a Command (not a direct Edit).
		// ExecuteCommand triggers workspace/applyEdit to strip tags.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.NotContains(t, editText, "json")
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Remove struct tags"))
	})

	t.Run("AddTest", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: mainSrc},
			{name: "main_test.go", content: testFileSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on the Add function (line 17, char 5).
		cmd := goCmdAt("add-test", uri, resource, 17, 5)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// add-test returns a Command (not a direct Edit).
		// ExecuteCommand triggers workspace/applyEdit with the test skeleton.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		captured := env.capturedEdits()
		require.Equal(t, 1, len(captured))
		editText := capturedEditText(captured)
		assert.Contains(t, editText, "func Test")
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Add test for Add"))
	})

	// Regression: go add-test should produce a test file with
	// the package declaration at the top, not at the bottom.
	// When gopls sends workspace/applyEdit with multiple inserts
	// at the same position, the array order must define the
	// resulting text order per the LSP spec.
	t.Run("AddTest/EditOrder", func(t *testing.T) {
		t.Parallel()

		// Use a source with only Greet (no existing test file)
		// so gopls creates a fresh test file.
		addTestSrc := "package main\n\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: addTestSrc},
		})
		handler, me, _ := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on "Add" (line 2, char 5).
		cmd := goCmdAt("add-test", uri, resource, 2, 5)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// workspace/applyEdit is sent synchronously within
		// ExecuteCommand, so captured edits are available
		// immediately after HandleCommand returns.
		captured := env.capturedEdits()
		require.NotEmpty(t, captured, "gopls should have sent workspace/applyEdit")

		// Collect all TextEdits destined for the test file.
		var testEdits []semanticapi.TextEdit
		for _, ae := range captured {
			for _, dc := range ae.Edit.DocumentChanges {
				if dc.TextDocumentEdit != nil &&
					strings.HasSuffix(dc.TextDocumentEdit.TextDocument.URI, "_test.go") {
					testEdits = append(testEdits, dc.TextDocumentEdit.Edits...)
				}
			}
			for fileURI, edits := range ae.Edit.Changes {
				if strings.HasSuffix(fileURI, "_test.go") {
					testEdits = append(testEdits, edits...)
				}
			}
		}
		require.NotEmpty(t, testEdits, "expected edits for the test file")

		// Apply the edits through lspcmd.ApplyEdits — the function
		// under test — and reconstruct the resulting buffer content.
		buf := newBufferCellEditor("")
		err = lspcmd.ApplyEdits(t.Context(), buf, testEdits)
		require.NoError(t, err)

		content := buf.String()
		assert.True(t, strings.HasPrefix(content, "package"),
			"generated test file must start with 'package', got:\n%s", content)
		assert.Contains(t, content, "func Test")
	})

	t.Run("Assembly", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: mainSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		cmd := goCmdAt("assembly", uri, resource, 17, 5)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// assembly returns a Command that opens a web view with assembly output.
		// No edits are produced; the handler notifies after execution.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		assert.Equal(t, 0, len(env.capturedEdits()))
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Browse"))
	})

	t.Run("Doc", func(t *testing.T) {
		t.Parallel()
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: mainSrc},
		})
		handler, me, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		cmd := goCmdAt("doc", uri, resource, 0, 0)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// doc returns a Command that opens a web view with package docs.
		// No edits are produced; the handler notifies after execution.
		assert.Equal(t, 0, len(me.editsFor(resource)))
		assert.Equal(t, 0, len(env.capturedEdits()))
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed: Browse documentation"))
	})

	t.Run("CodeLens/Test", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: mainSrc},
			{name: "main_test.go", content: testFileSrc},
		})
		scheme := newTestScheme()
		handler, _, mn := newTestHandler(t, env, scheme)

		uri := parseTestURI(t, env.fileURIs["main_test.go"])
		resource := &stubResource{uri: uri}
		// Cursor near TestAdd (line 4).
		cmd := goCmdAt("test", uri, resource, 4, 5)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return mn.hasMessage("passed")
		}, 30*time.Second, 100*time.Millisecond)
		msgs := mn.getMessages()
		// Expect 2 notifications: the initial progress and the final result.
		require.GreaterOrEqual(t, len(msgs), 2)
		last := msgs[len(msgs)-1]
		assert.Equal(t, browserapi.LevelSuccess, last.Level)
		assert.Contains(t, last.Message, "passed")
	})

	t.Run("CodeLens/Generate", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: generateSrc},
		})
		handler, _, mn := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		// Cursor near the go:generate directive (line 2).
		cmd := goCmdAt("generate", uri, resource, 2, 0)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return len(mn.getMessages()) > 0
		}, 30*time.Second, 100*time.Millisecond)

		// Verify the lens was found and executed, not the "no lens found" path.
		msgs := mn.getMessages()
		require.Equal(t, 1, len(msgs))
		assert.True(t, mn.hasMessage("Executed:"),
			"expected 'Executed:' notification proving the lens was found, got: %q", msgs[0].Message)
	})

	t.Run("Tidy", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: "package main\n\nfunc main() {}\n"},
		})
		handler, _, mn := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		cmd := goCmd("tidy", uri, resource)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			return mn.hasMessage("gopls.tidy")
		}, 30*time.Second, 100*time.Millisecond)
	})

	t.Run("Vendor", func(t *testing.T) {
		t.Parallel()

		// gopls panics with a nil *Snapshot in GoCommandInvocation
		// when handling gopls.vendor. This is an upstream gopls bug
		// (confirmed in v0.21.1): the run() handler receives a nil
		// snapshot from session.FileOf and dereferences it without
		// a nil check. Remove SkipNow once fixed upstream.
		t.SkipNow()

		const vendorGoMod = "module example.com/test\n\ngo 1.22\n\n" +
			"require example.com/dep v0.0.0\n\n" +
			"replace example.com/dep => ./dep\n"
		const vendorMain = "package main\n\nimport \"example.com/dep\"\n\nfunc main() { dep.Hello() }\n"

		env := initGopls(t, goplsBin, []testFile{
			{name: "go.mod", content: vendorGoMod},
			{name: "dep/go.mod", content: "module example.com/dep\n\ngo 1.22\n"},
			{name: "dep/lib.go", content: "package dep\n\nfunc Hello() string { return \"hello\" }\n"},
			{name: "main.go", content: vendorMain},
		})
		handler, _, _ := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		cmd := goCmd("vendor", uri, resource)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// Verify the LSP server is still alive after vendor.
		hover, err := env.mgr.Hover(t.Context(), semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{
				URI: env.fileURIs["main.go"],
			},
			Position: semanticapi.Position{Line: 0, Character: 8},
		})
		require.NoError(t, err, "gopls should respond after vendor")
		require.NotNil(t, hover, "expected hover on package name after vendor")
	})

	t.Run("Vulncheck", func(t *testing.T) {
		t.Parallel()

		// golang.org/x/text v0.3.7 has GO-2022-1059 (CVE-2022-32149).
		goModContent := "module example.com/test\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.7\n"
		mainContent := "package main\n\nimport _ \"golang.org/x/text/language\"\n\nfunc main() {}\n"

		dir := setupWorkspace(t, "example.com/test", []testFile{
			{name: "go.mod", content: goModContent},
			{name: "main.go", content: mainContent},
		})

		// Resolve dependencies so gopls can load packages.
		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = dir
		out, err := tidy.CombinedOutput()
		require.NoError(t, err, "go mod tidy: %s", out)

		env := initGoplsFromDir(t, goplsBin, dir, []testFile{
			{name: "main.go", content: mainContent},
		})
		handler, _, _ := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		cmd := goCmd("vulncheck", uri, resource)

		// Register a diagnostics hook BEFORE executing the command.
		// Vulncheck runs asynchronously inside gopls: the RPC returns
		// immediately but the scan continues in the background. When
		// it finds vulnerabilities, gopls publishes diagnostics on
		// the go.mod file.
		goModURI := "file://" + filepath.Join(env.dir, "go.mod")
		diagsCh := make(chan []semanticapi.Diagnostic, 1)
		env.cb.mu.Lock()
		prev := env.cb.onDiagnostics
		env.cb.onDiagnostics = func(p semanticapi.PublishDiagnosticsParams) {
			if prev != nil {
				prev(p)
			}
			if p.URI == goModURI && len(p.Diagnostics) > 0 {
				select {
				case diagsCh <- p.Diagnostics:
				default:
				}
			}
		}
		env.cb.mu.Unlock()

		err = handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// gopls.vulncheck is synchronous: the RPC blocks until the
		// scan completes. gopls itself sends a window/showMessage
		// notification with the result summary, so the handler does
		// not post its own notification on success.

		// Wait for gopls to publish vulnerability diagnostics on
		// go.mod, confirming the scan found the known vulnerability.
		select {
		case diags := <-diagsCh:
			var diagMsgs []string
			for _, d := range diags {
				diagMsgs = append(diagMsgs, d.Message)
			}
			t.Logf("vulncheck diagnostics: %v", diagMsgs)
			found := false
			for _, msg := range diagMsgs {
				if strings.Contains(msg, "GO-2022-1059") ||
					strings.Contains(msg, "golang.org/x/text") {
					found = true
					break
				}
			}
			assert.True(t, found,
				"expected vulnerability diagnostic for golang.org/x/text, got: %v", diagMsgs)
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for vulncheck diagnostics on go.mod")
		}
	})

	t.Run("ToggleCompilerOpt", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: mainSrc},
		})
		handler, me, mn := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		me.Register(resource)
		// Cursor on the Add function (line 17, char 5).
		cmd := goCmdAt("toggle-compiler-opt", uri, resource, 17, 5)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// source.toggleCompilerOptDetails returns a Command (not an Edit).
		// applyAction calls ExecuteCommand synchronously to toggle gc_details
		// diagnostics; no edits are produced.
		assert.Empty(t, me.editsFor(resource))
		require.Len(t, mn.getMessages(), 1)
		assert.True(t, mn.hasMessage("Executed:"))

		// After toggling gc_details ON, gopls publishes diagnostics with
		// compiler optimization details (inlining decisions, escape analysis).
		// mainSrc's Add function is trivially inlinable → "can inline Add".
		// These diagnostics carry Source: "compiler" so the handler can
		// classify and annotate them with category prefixes and icons.
		fileURI := env.fileURIs["main.go"]
		require.Eventually(t, func() bool {
			env.cb.mu.Lock()
			defer env.cb.mu.Unlock()
			for _, dp := range env.cb.diagnostics {
				if dp.URI != fileURI {
					continue
				}
				for _, d := range dp.Diagnostics {
					isCompilerOpt := strings.Contains(d.Message, "can inline") ||
						strings.Contains(d.Message, "inlining") ||
						strings.Contains(d.Message, "escape")
					if isCompilerOpt {
						assert.Contains(t,
							[]string{"compiler", "optimizer details"},
							d.Source,
							"gc_details diagnostics should have Source \"compiler\" or \"optimizer details\"")
						return true
					}
				}
			}
			return false
		}, 10*time.Second, 100*time.Millisecond,
			"expected gc_details diagnostics (e.g. \"can inline Add\") after toggle")
	})

	t.Run("UpgradeDependency", func(t *testing.T) {
		t.Parallel()

		// gopls panics with a nil *Snapshot when handling
		// gopls.check_upgrades. This is the same upstream gopls bug
		// as gopls.vendor (confirmed in v0.21.1). Remove SkipNow
		// once fixed upstream.
		t.SkipNow()

		// Use a real module with an outdated dependency so
		// check_upgrades has something to report.
		goModContent := "module example.com/test\n\ngo 1.22\n\nrequire golang.org/x/text v0.3.7\n"
		mainContent := "package main\n\nimport _ \"golang.org/x/text/language\"\n\nfunc main() {}\n"

		dir := setupWorkspace(t, "example.com/test", []testFile{
			{name: "go.mod", content: goModContent},
			{name: "main.go", content: mainContent},
		})

		// Resolve dependencies so gopls can load packages.
		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = dir
		out, err := tidy.CombinedOutput()
		require.NoError(t, err, "go mod tidy: %s", out)

		env := initGoplsFromDir(t, goplsBin, dir, []testFile{
			{name: "main.go", content: mainContent},
		})
		handler, _, mn := newTestHandler(t, env, nil)

		// Register diagnostics hook BEFORE executing the command.
		// check_upgrades annotates go.mod with diagnostics showing
		// available upgrades for outdated dependencies.
		goModURI := "file://" + filepath.Join(env.dir, "go.mod")
		diagsCh := make(chan []semanticapi.Diagnostic, 1)
		env.cb.mu.Lock()
		prev := env.cb.onDiagnostics
		env.cb.onDiagnostics = func(p semanticapi.PublishDiagnosticsParams) {
			if prev != nil {
				prev(p)
			}
			if p.URI == goModURI && len(p.Diagnostics) > 0 {
				select {
				case diagsCh <- p.Diagnostics:
				default:
				}
			}
		}
		env.cb.mu.Unlock()

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		cmd := goCmd("upgrade-dependency", uri, resource)

		err = handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// modCommandHandler fires ExecuteCommand in a goroutine.
		require.Eventually(t, func() bool {
			return mn.hasMessage("gopls.check_upgrades")
		}, 30*time.Second, 100*time.Millisecond)
		assert.Len(t, mn.getMessages(), 1)
		assert.Equal(t, browserapi.LevelInfo, mn.getMessages()[0].Level)

		// Wait for gopls to publish upgrade diagnostics on go.mod.
		// golang.org/x/text v0.3.7 is outdated; check_upgrades should
		// annotate the require line with the available newer version.
		select {
		case diags := <-diagsCh:
			var msgs []string
			for _, d := range diags {
				msgs = append(msgs, d.Message)
			}
			t.Logf("check_upgrades diagnostics: %v", msgs)
			found := false
			for _, msg := range msgs {
				if strings.Contains(msg, "golang.org/x/text") {
					found = true
					break
				}
			}
			assert.True(t, found,
				"expected upgrade diagnostic for golang.org/x/text, got: %v", msgs)
		case <-time.After(30 * time.Second):
			t.Fatal("timed out waiting for check_upgrades diagnostics on go.mod")
		}
	})

	t.Run("CodeLens/RegenerateCgo", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: cgoSrc},
		})
		handler, _, mn := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		// Cursor on the `import "C"` line (line 3).
		cmd := goCmdAt("regenerate-cgo", uri, resource, 3, 0)

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)

		// codeLensHandler fires asynchronously — wait for the notification.
		require.Eventually(t, func() bool {
			return len(mn.getMessages()) > 0
		}, 30*time.Second, 100*time.Millisecond)

		// Verify the lens was found and executed, not the "no lens found" path.
		msgs := mn.getMessages()
		require.Len(t, msgs, 1)
		assert.True(t, mn.hasMessage("Executed:"),
			"expected 'Executed:' notification proving the lens was found, got: %q", msgs[0].Message)
	})

	t.Run("AddImport", func(t *testing.T) {
		t.Parallel()
		simpleSrc := "package main\n\nfunc main() {}\n"
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: simpleSrc},
		})
		handler, _, mn := newTestHandler(t, env, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		cmd := textapi.Command{
			Name:     "go",
			Args:     []string{"add-import", "fmt"},
			URI:      uri,
			Resource: resource,
		}

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)
		assert.True(t, mn.hasMessage("Added import"))
	})

	// Regression: after add-import, gopls must know about the change.
	// workspace/applyEdit is sent by gopls; the client must feed the
	// edits back as textDocument/didChange so the overlay stays in sync.
	// AddImportSyncsOverlay verifies that after gopls.add_import
	// triggers workspace/applyEdit, the callbackInterceptor sends
	// didChange so gopls's overlay stays in sync — no manual
	// replay needed.
	t.Run("AddImportSyncsOverlay", func(t *testing.T) {
		t.Parallel()
		src := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: src},
		})
		handler, _, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		cmd := textapi.Command{
			Name:     "go",
			Args:     []string{"add-import", "fmt"},
			URI:      uri,
			Resource: resource,
		}

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)
		assert.True(t, mn.hasMessage("Added import"))

		captured := env.capturedEdits()
		require.NotEmpty(t, captured, "gopls should have sent workspace/applyEdit")

		// simulateEditorEvents fires Handle(EventTypeEdit) which sends
		// didChange asynchronously — give it time to be processed.
		time.Sleep(500 * time.Millisecond)

		fileURI := env.fileURIs["main.go"]
		hover, err := env.mgr.Hover(t.Context(), semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
			Position:     semanticapi.Position{Line: 5, Character: 1},
		})
		require.NoError(t, err)
		require.NotNil(t, hover, "expected hover result after add-import synced overlay")
		assert.Contains(t, hover.Contents.Value, "fmt")
	})

	// Same as above but with auto-init-like params (no
	// workspace.configuration, no workspace.workspaceEdit.documentChanges).
	t.Run("AddImportSyncsOverlay/AutoInitParams", func(t *testing.T) {
		t.Parallel()
		src := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
		env := initGoplsWithAutoInitParams(t, goplsBin, []testFile{
			{name: "main.go", content: src},
		})
		handler, _, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		cmd := textapi.Command{
			Name:     "go",
			Args:     []string{"add-import", "fmt"},
			URI:      uri,
			Resource: resource,
		}

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)
		assert.True(t, mn.hasMessage("Added import"))

		captured := env.capturedEdits()
		require.NotEmpty(t, captured, "gopls should have sent workspace/applyEdit")

		time.Sleep(500 * time.Millisecond)

		fileURI := env.fileURIs["main.go"]
		hover, err := env.mgr.Hover(t.Context(), semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
			Position:     semanticapi.Position{Line: 5, Character: 1},
		})
		require.NoError(t, err)
		require.NotNil(t, hover, "expected hover result after add-import synced overlay (auto-init params)")
		assert.Contains(t, hover.Contents.Value, "fmt")
	})

	// Regression: in the real IDE, workspace/applyEdit modifies the editor
	// buffer, which fires Handle(EventTypeEdit) → didChange to gopls.
	// Previously callbackInterceptor.notifyDidChange sent ANOTHER didChange,
	// which corrupted gopls's overlay. initGoplsWithApplyEdit simulates the
	// real editor events via simulateEditorEvents; this test verifies the
	// overlay stays clean.
	t.Run("AddImportNoDoubleNotification", func(t *testing.T) {
		t.Parallel()
		src := "package main\n\nfunc main() {\n\tstrings.Join(nil, \"\")\n}\n"
		env := initGoplsWithApplyEdit(t, goplsBin, []testFile{
			{name: "main.go", content: src},
		})

		handler, _, mn := newTestHandler(t, env.testEnv, nil)

		uri := parseTestURI(t, env.fileURIs["main.go"])
		resource := &stubResource{uri: uri}
		cmd := textapi.Command{
			Name:     "go",
			Args:     []string{"add-import", "strings"},
			URI:      uri,
			Resource: resource,
		}

		err := handler.HandleCommand(t.Context(), cmd)
		require.NoError(t, err)
		assert.True(t, mn.hasMessage("Added import"))

		captured := env.capturedEdits()
		require.NotEmpty(t, captured, "gopls should have sent workspace/applyEdit")

		// Wait for the async Handle(EventTypeEdit) to be processed by handleEvs.
		// This ensures both didChange notifications have reached gopls.
		time.Sleep(1 * time.Second)

		// Verify gopls's overlay is not corrupted.
		fileURI := env.fileURIs["main.go"]
		hover, err := env.mgr.Hover(t.Context(), semanticapi.HoverParams{
			TextDocument: semanticapi.TextDocumentIdentifier{URI: fileURI},
			Position:     semanticapi.Position{Line: 0, Character: 8},
		})
		require.NoError(t, err, "hover should succeed after add-import")
		require.NotNil(t, hover, "overlay may be corrupted: hover returned nil")

		// Wait for diagnostics to settle.
		time.Sleep(2 * time.Second)

		// Check the LAST diagnostic set for the file: if gopls got a double
		// didChange, the overlay has garbled imports producing errors like
		// "could not import stringss", "redeclared", or "undefined".
		// Earlier diagnostics include pre-import errors (undefined: strings)
		// which are expected — only the final state matters.
		env.cb.mu.Lock()
		var lastDiags []semanticapi.Diagnostic
		for _, d := range env.cb.diagnostics {
			if d.URI == fileURI {
				lastDiags = d.Diagnostics
			}
		}
		env.cb.mu.Unlock()

		var diagMsgs []string
		for _, d := range lastDiags {
			diagMsgs = append(diagMsgs, d.Message)
		}
		t.Logf("final diagnostics after add-import: %v", diagMsgs)
		for _, msg := range diagMsgs {
			if strings.Contains(msg, "redeclared") ||
				strings.Contains(msg, "could not import") {
				t.Errorf("overlay corrupted by double didChange: %s", msg)
			}
		}
		// The import should resolve — no "undefined: strings" in final state.
		for _, msg := range diagMsgs {
			if msg == "undefined: strings" {
				t.Errorf("import not applied to overlay: %s", msg)
			}
		}
	})

	t.Run("WorkspaceSymbolReturnsInterfaces", func(t *testing.T) {
		t.Parallel()
		env := initGopls(t, goplsBin, []testFile{
			{name: "main.go", content: typeSwitchSrc},
		})
		ctx := t.Context()

		// Query for "Animal" (an interface) and "Dog" (a struct).
		// Interface (kind 11) is in the default 1–18 range, but
		// Struct (kind 23) is NOT — so without an explicit
		// symbolKind.valueSet the server may downgrade Struct
		// to Class (kind 5).
		type wantSym struct {
			name string
			kind semanticapi.SymbolKind
		}
		wants := []wantSym{
			{"Animal", semanticapi.SymbolKindInterface},
			{"Dog", semanticapi.SymbolKindStruct},
		}

		for _, w := range wants {
			var syms []semanticapi.SymbolInformation
			require.Eventually(t, func() bool {
				var err error
				syms, err = env.mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{
					Query: w.name,
				})
				if err != nil || len(syms) == 0 {
					return false
				}
				return true
			}, 10*time.Second, 200*time.Millisecond,
				"gopls should return symbols matching %q", w.name)

			var found bool
			for _, s := range syms {
				if s.Name == w.name && s.Kind == w.kind {
					found = true
					break
				}
			}
			require.True(t, found,
				"workspace/symbol should return %s with SymbolKind %d; got: %v", w.name, w.kind, syms)
		}
	})
}

func TestFindGoModURI(t *testing.T) {
	t.Parallel()

	// Create a workspace with nested directories.
	tmpDir := setupWorkspace(t, "example.com/test", []testFile{
		{name: "main.go", content: "package main\n"},
		{name: "pkg/foo/bar.go", content: "package foo\n"},
	})

	t.Run("FileInRoot", func(t *testing.T) {
		t.Parallel()

		fileURI := "file://" + tmpDir + "/main.go"
		result := findGoModURI(fileURI)
		assert.Equal(t, "file://"+tmpDir+"/go.mod", result)
	})

	t.Run("FileInSubdirectory", func(t *testing.T) {
		t.Parallel()

		fileURI := "file://" + tmpDir + "/pkg/foo/bar.go"
		result := findGoModURI(fileURI)
		assert.Equal(t, "file://"+tmpDir+"/go.mod", result)
	})
}

func TestGoRouter(t *testing.T) {
	t.Parallel()

	t.Run("MissingSubcommand", func(t *testing.T) {
		t.Parallel()

		router := &goRouter{handlers: map[string]textapi.CommandHandler{}}
		err := router.HandleCommand(t.Context(), textapi.Command{
			Name:     "go",
			Resource: &stubResource{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing subcommand")
	})

	t.Run("UnknownSubcommand", func(t *testing.T) {
		t.Parallel()

		router := &goRouter{handlers: map[string]textapi.CommandHandler{}}
		err := router.HandleCommand(t.Context(), textapi.Command{
			Name:     "go",
			Args:     []string{"nonexistent"},
			Resource: &stubResource{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unknown go subcommand")
	})

	t.Run("NilResourceRoutesToHandler", func(t *testing.T) {
		t.Parallel()

		mn := &mockNotifications{}
		router := &goRouter{handlers: map[string]textapi.CommandHandler{
			"tidy": &modCmd{notify: mn, goplsCommand: "gopls.tidy"},
		}}
		err := router.HandleCommand(t.Context(), textapi.Command{
			Name: "go",
			Args: []string{"tidy"},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no file open")
	})

	t.Run("NilResourceTestWithArgRoutes", func(t *testing.T) {
		t.Parallel()

		ex := &capturingExecutor{
			stdout: `{"Action":"pass","Package":"example.com/test","Test":"TestAdd","Elapsed":0.01}`,
		}
		mn := &mockNotifications{}
		router := &goRouter{handlers: map[string]textapi.CommandHandler{
			"test": &testCmd{notify: mn, executor: ex},
		}}
		err := router.HandleCommand(t.Context(), textapi.Command{
			Name: "go",
			Args: []string{"test", "TestAdd"},
		})
		require.NoError(t, err)
		assert.Equal(t,
			[]string{"test", "-json", "-count=1", "-run", "^TestAdd$", "./..."},
			ex.captured().Args)
	})
}

func TestE2EHandleOpenWorksWithNoInitializeServer(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)

	diskContent := "package main\n\nfunc main() {}\n"

	dir := setupWorkspace(t, "example.com/test", []testFile{
		{name: "main.go", content: diskContent},
	})
	rootURI := "file://" + dir

	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newTestScheme()

	ready := make(chan struct{})
	var once sync.Once
	cb := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") ||
				strings.Contains(params.Message, "background refresh finished") {
				once.Do(func() { close(ready) })
			}
		},
		onProgress: readyOnProgressCh(&once, ready),
	}

	mgr := idelsp.New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		idelsp.Config{
			MaxRetries:         1,
			Callback:           cb,
			NoInitializeServer: true,
			CloseTimeout:       realLSPCloseTimeout,
		},
	)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	ctx := context.Background()
	params, err := goplsInitializeParams(rootURI, goplsDebugOptions{}, "")
	require.NoError(t, err)

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	mainURI := "file://" + filepath.Join(dir, "main.go")
	fileURIs := map[string]string{"main.go": mainURI}
	waitGoplsReady(t, ready, mgr, fileURIs)

	// Open the file with overlay content that differs from disk.
	// The overlay has an Add function that the disk version does not.
	wsURI, err := workspaceapi.ParseURI(mainURI)
	require.NoError(t, err)

	mgr.Handle(ctx, textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     wsURI,
		Content: mainSrc, // contains Add at line 17
	})

	// Give handleEvs time to process the open event.
	time.Sleep(1 * time.Second)

	// Hover on Add (line 17, char 5) which only exists in the overlay.
	// If didOpen was sent, gopls uses the overlay and returns hover info.
	// If didOpen was dropped (bug), gopls reads from disk where line 17
	// does not exist and returns nil.
	hover, err := mgr.Hover(ctx, semanticapi.HoverParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
		Position:     semanticapi.Position{Line: 17, Character: 5},
	})
	require.NoError(t, err)
	require.NotNil(t, hover,
		"expected hover on Add — didOpen was likely not sent to gopls")
	assert.Contains(t, hover.Contents.Value, "Add")
}

func TestE2EHandleOpenRaceWithInitialize(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)

	diskContent := "package main\n\nfunc main() {}\n"

	dir := setupWorkspace(t, "example.com/test", []testFile{
		{name: "main.go", content: diskContent},
	})
	rootURI := "file://" + dir

	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newTestScheme()

	ready := make(chan struct{})
	var once sync.Once
	cb := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") ||
				strings.Contains(params.Message, "background refresh finished") {
				once.Do(func() { close(ready) })
			}
		},
		onProgress: readyOnProgressCh(&once, ready),
	}

	mgr := idelsp.New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		idelsp.Config{
			MaxRetries:         1,
			Callback:           cb,
			NoInitializeServer: true,
			CloseTimeout:       realLSPCloseTimeout,
		},
	)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	ctx := context.Background()

	mainURI := "file://" + filepath.Join(dir, "main.go")
	wsURI, err := workspaceapi.ParseURI(mainURI)
	require.NoError(t, err)

	// Send EventTypeOpen BEFORE Initialize — simulates the race
	// where the IDE re-opens session files before the extension
	// has called Initialize.
	mgr.Handle(ctx, textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     wsURI,
		Content: mainSrc, // overlay with Add at line 17
	})

	// Give handleEvs time to track the pending open (no server yet).
	time.Sleep(200 * time.Millisecond)

	// Now Initialize — this creates the gopls server and sends
	// didOpen for pending files as part of server init.
	params, err := goplsInitializeParams(rootURI, goplsDebugOptions{}, "")
	require.NoError(t, err)

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	fileURIs := map[string]string{"main.go": mainURI}
	waitGoplsReady(t, ready, mgr, fileURIs)

	// Hover on Add (line 17, char 5) which only exists in the overlay.
	// If the pending open was sent during Initialize, gopls uses the
	// overlay and returns hover info. If it was dropped, gopls reads
	// from disk where Add does not exist and returns nil.
	hover, err := mgr.Hover(ctx, semanticapi.HoverParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
		Position:     semanticapi.Position{Line: 17, Character: 5},
	})
	require.NoError(t, err)
	require.NotNil(t, hover,
		"expected hover on Add — pending didOpen was likely not sent during Initialize")
	assert.Contains(t, hover.Contents.Value, "Add")
}

func TestE2EHandleOpenCloseRaceWithInitialize(t *testing.T) {
	t.Parallel()
	goplsBin := findGopls(t)

	diskContent := "package main\n\nfunc main() {}\n"

	dir := setupWorkspace(t, "example.com/test", []testFile{
		{name: "main.go", content: diskContent},
	})
	rootURI := "file://" + dir

	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newTestScheme()

	ready := make(chan struct{})
	var once sync.Once
	cb := &testCallback{
		onShowMessage: func(params semanticapi.ShowMessageParams) {
			if strings.Contains(params.Message, "Finished loading packages") ||
				strings.Contains(params.Message, "background refresh finished") {
				once.Do(func() { close(ready) })
			}
		},
		onProgress: readyOnProgressCh(&once, ready),
	}

	mgr := idelsp.New(
		uri, scheme, scheme,
		&stubPkgManager{bin: goplsBin},
		nil, nil,
		idelsp.Config{
			MaxRetries:         1,
			Callback:           cb,
			NoInitializeServer: true,
			CloseTimeout:       realLSPCloseTimeout,
		},
	)
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	ctx := context.Background()

	mainURI := "file://" + filepath.Join(dir, "main.go")
	wsURI, err := workspaceapi.ParseURI(mainURI)
	require.NoError(t, err)

	// Open then close BEFORE Initialize — the file should NOT be
	// sent as didOpen during server init because it's no longer open.
	mgr.Handle(ctx, textapi.Event{
		Type:    textapi.EventTypeOpen,
		URI:     wsURI,
		Content: mainSrc, // overlay with Add at line 17
	})
	mgr.Handle(ctx, textapi.Event{
		Type: textapi.EventTypeClose,
		URI:  wsURI,
	})

	// Give handleEvs time to process both events.
	time.Sleep(200 * time.Millisecond)

	// Initialize — should NOT send didOpen (file was closed).
	params, err := goplsInitializeParams(rootURI, goplsDebugOptions{}, "")
	require.NoError(t, err)

	_, err = mgr.Initialize(ctx, params)
	require.NoError(t, err)

	fileURIs := map[string]string{"main.go": mainURI}
	waitGoplsReady(t, ready, mgr, fileURIs)

	// Hover on line 17 where Add would be in the overlay. Since the
	// file was closed before Initialize, gopls should NOT have the
	// overlay — it reads from disk where line 17 doesn't exist.
	// gopls may return nil hover or an error (line out of range).
	hover, err := mgr.Hover(ctx, semanticapi.HoverParams{
		TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
		Position:     semanticapi.Position{Line: 17, Character: 5},
	})
	if err == nil {
		assert.Nil(t, hover,
			"expected no hover at line 17 — closed file should not have been sent as didOpen")
	}
	// err != nil is also acceptable: gopls rejects the position because
	// the disk file only has a few lines (no overlay was sent).
}

func TestAbsDiff(t *testing.T) {
	t.Parallel()
	assert.Equal(t, uint32(5), absDiff(10, 5))
	assert.Equal(t, uint32(5), absDiff(5, 10))
	assert.Equal(t, uint32(0), absDiff(7, 7))
}
