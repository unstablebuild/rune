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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/browserapi"
	"github.com/unstablebuild/rune-go-sdk/api/config"
	"github.com/unstablebuild/rune-go-sdk/api/textapi"
	"github.com/unstablebuild/rune-go-sdk/api/workspaceapi"
	"github.com/unstablebuild/rune-go-sdk/handler/repl"
	"github.com/unstablebuild/rune-go-sdk/iterator"
	"github.com/unstablebuild/rune-go-sdk/term"
	"unstable.build/rune/internal/extension/langext"
	"unstable.build/rune/internal/handler/handlertest"
	"unstable.build/rune/internal/ide/idelsp"
	"unstable.build/rune/internal/ide/idelsp/lspcmd"
)

func TestLocalSchemeStartCommandPreservesEnvironment(t *testing.T) {
	t.Setenv("RUNE_RUST_E2E_INHERITED", "inherited")
	t.Setenv("RUNE_RUST_E2E_OVERRIDE", "inherited")

	var stdout bytes.Buffer
	done := make(chan error, 1)
	_, err := newLocalScheme("").StartCommand(t.Context(), workspaceapi.Cmd{
		Path:    os.Args[0],
		Args:    []string{"-test.run=^TestLocalSchemeEnvironmentHelper$"},
		Env:     []string{"RUNE_RUST_E2E_HELPER=1", "RUNE_RUST_E2E_OVERRIDE=override"},
		Stdout:  &stdout,
		Watcher: workspaceapi.ChanProcessWatcher(done),
	})
	require.NoError(t, err)
	require.NoError(t, <-done)
	lines := strings.Split(stdout.String(), "\n")
	require.GreaterOrEqual(t, len(lines), 2)
	assert.Equal(t, []string{"inherited", "override"}, lines[:2])
}

func TestLocalSchemeEnvironmentHelper(t *testing.T) {
	if os.Getenv("RUNE_RUST_E2E_HELPER") != "1" {
		return
	}
	_, _ = fmt.Fprintf(os.Stdout, "%s\n%s\n",
		os.Getenv("RUNE_RUST_E2E_INHERITED"),
		os.Getenv("RUNE_RUST_E2E_OVERRIDE"),
	)
}

func TestE2ERustAnalyzerLogFilterReachesProcess(t *testing.T) {
	raBin := findRustAnalyzer(t)
	dir := setupCargoWorkspace(t)
	rootURI := "file://" + dir
	uri, err := workspaceapi.ParseURI(rootURI)
	require.NoError(t, err)

	scheme := newLocalScheme(dir)
	mgr := idelsp.New(uri, scheme, scheme, &stubPkgManager{bin: raBin}, nil, nil,
		idelsp.Config{MaxRetries: 1, Callback: &raCallback{}})
	t.Cleanup(func() { require.NoError(t, mgr.Close()) })

	cfg := config.JSONFromMap(map[string]any{
		"lsp_path": raBin,
		"debug": map[string]any{
			"log_level": "rust_analyzer=debug",
		},
	})
	err = initializeRustRoot(t.Context(), scheme, scheme, newFakeNotifications(), mgr, &langext.Tools{},
		"", "", cfg, false, langext.Root{Dir: dir, URI: rootURI})
	require.NoError(t, err)

	started, ok := scheme.startedProcess(raBin)
	require.True(t, ok, "rust-analyzer process was not started")
	assert.Equal(t, []string{"RA_LOG=rust_analyzer=debug"}, started.env)
	require.Eventually(t, func() bool {
		return strings.Contains(started.stderr.String(), "DEBUG")
	}, 5*time.Second, 20*time.Millisecond, "rust-analyzer did not write debug logs to stderr")
}

func TestE2E(t *testing.T) {
	t.Parallel()
	raBin := findRustAnalyzer(t)

	t.Run("ExtractVariable", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)

		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select the expression `(1 + 2)` on line 1 (0-based): columns
		// 17..24 of `    let result = (1 + 2) * 4;`. `rust extract` offers
		// variable/constant/static/function, so pick "Extract into variable".
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 17, Y: 1},
			End:   term.Coordinates{X: 24, Y: 1},
		})

		cmd := rustCmdAt("extract", uri, resource, 1, 17)
		runAction(t, handler, cmd, "Extract into variable")

		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "expected an extract-variable edit")
		assert.Contains(t, combined, "var_name", "extract into variable introduces var_name")
		assert.NotContains(t, combined, "${", "snippet placeholders must not leak into the buffer")
		assert.NotContains(t, combined, "$0", "snippet tab stops must not leak into the buffer")
	})

	t.Run("InlineLocalVariable", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/inline.rs"})
		handler, me, _ := newTestActionHandler(t, env)

		uri := parseTestURI(t, env.fileURIs["src/inline.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on the `x` usage in `    x * 4` (line 2, col 4).
		cmd := rustCmdAt("inline", uri, resource, 2, 4)
		runAction(t, handler, cmd, "Inline variable")

		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "expected an inline edit")
		assert.Contains(t, combined, "1 + 2", "inlining x should substitute its initializer")
	})

	t.Run("Rewrite", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/rewrite.rs"})
		handler, me, _ := newTestActionHandler(t, env)

		uri := parseTestURI(t, env.fileURIs["src/rewrite.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on the `>` operator in `    a > b` (line 1, col 6);
		// this assist swaps the operands and inverts the operator.
		cmd := rustCmdAt("rewrite", uri, resource, 1, 6)
		runAction(t, handler, cmd, "Flip binary expression")

		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "expected a rewrite edit")
		assert.Contains(t, combined, "<", "flip comparison turns > into <")
	})

	t.Run("Refactor", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/refactor.rs"})
		handler, me, _ := newTestActionHandler(t, env)

		uri := parseTestURI(t, env.fileURIs["src/refactor.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on the `if` keyword (line 1, col 4). The refactor family
		// includes "Convert to guarded return", which we pick.
		cmd := rustCmdAt("refactor", uri, resource, 1, 4)
		runAction(t, handler, cmd, "Invert if")

		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "expected a refactor edit from the picked assist")
	})

	t.Run("List", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/list.rs"})
		handler, me, _ := newTestActionHandler(t, env)

		uri := parseTestURI(t, env.fileURIs["src/list.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select `1 + 2 + 3` (line 1) so multiple assists apply; `list`
		// surfaces them all and we pick "Extract into variable".
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 16, Y: 1},
			End:   term.Coordinates{X: 25, Y: 1},
		})

		cmd := rustCmdAt("list", uri, resource, 1, 16)
		runAction(t, handler, cmd, "Extract into variable")

		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "expected an edit from the picked assist in list mode")
		assert.Contains(t, combined, "var_name")

		// The assist rewrites the `let` statement on line 1 and inserts
		// the new binding before line 2's `value`; no edit may touch
		// line 0. An edit at 0,0 means a layer between rust-analyzer
		// and the editor dropped the coordinates.
		for _, e := range me.editsFor(resource) {
			assert.GreaterOrEqual(t, e.Start.Y, 1,
				"edit %q applied at %v-%v must not touch line 0", e.NewText, e.Start, e.End)
			assert.GreaterOrEqual(t, e.End.Y, 1,
				"edit %q applied at %v-%v must not touch line 0", e.NewText, e.Start, e.End)
		}
		for _, ae := range env.capturedEdits() {
			for _, dc := range ae.Edit.DocumentChanges {
				if dc.TextDocumentEdit == nil {
					continue
				}
				for _, te := range dc.TextDocumentEdit.Edits {
					assert.GreaterOrEqual(t, int(te.Range.Start.Line), 1,
						"applyEdit %q at %+v must not touch line 0", te.NewText, te.Range)
				}
			}
		}
	})

	// A command dispatched from a context that cannot capture the editor
	// cursor (a non-text window in focus, a stale tab handler) arrives
	// with a valid resource but a zero cursor snapshot. The handler must
	// resolve the live editor cursor instead of running the request
	// against 0,0 — which offers top-of-file assists and applies the
	// picked edit at the top of the file.
	t.Run("ListLiveCursorFallback", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/inline.rs"})
		handler, me, _ := newTestActionHandler(t, env)

		uri := parseTestURI(t, env.fileURIs["src/inline.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Live cursor on the `x` usage in `    x * 4` (line 2, col 4).
		require.NoError(t, me.SetCursor(resource, term.Coordinates{X: 4, Y: 2}))

		// rustCmd carries no cursor: the dispatch snapshot was lost.
		cmd := rustCmd("list", uri, resource)
		runAction(t, handler, cmd, "Inline variable")

		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "expected an inline edit at the live cursor")
		assert.Contains(t, combined, "1 + 2", "inlining x should substitute its initializer")
		for _, e := range me.editsFor(resource) {
			assert.GreaterOrEqual(t, e.Start.Y, 1,
				"edit %q applied at %v-%v must not touch line 0", e.NewText, e.Start, e.End)
		}
	})

	t.Run("OrganizeImports", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/main.rs"})
		handler, me, mn := newTestActionHandler(t, env)

		uri := parseTestURI(t, env.fileURIs["src/main.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		cmd := rustCmdAt("organize-imports", uri, resource, 0, 0)
		requireHandleCommand(t, handler, cmd)

		// organize-imports either edits the buffer directly, applies via
		// workspace/applyEdit, or reports nothing to do — all valid. When
		// it edits, the two separate use lines merge into a braced group.
		edits := me.editsFor(resource)
		captured := env.capturedEdits()
		if len(edits) == 0 && len(captured) == 0 {
			assert.True(t, mn.hasMessage("Imports are already organized"))
			return
		}
		combined := collectEditText(edits) + capturedEditText(captured)
		assert.Contains(t, combined, "{", "merged imports should use a braced group")
	})

	t.Run("FileText", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		text := runViewer(t, handler, rustCmdAt("file-text", uri, resource, 0, 0))
		assert.Contains(t, text, "pub fn compute", "file-text should echo the buffer")
	})

	t.Run("SyntaxTree", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		text := runViewer(t, handler, rustCmdAt("syntax-tree", uri, resource, 0, 0))
		assert.Contains(t, text, "FN", "syntax tree should contain a function node")
	})

	t.Run("Hir", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _, _, parser := newTestActionHandlerParser(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor inside compute's body.
		text := runViewer(t, handler, rustCmdAt("hir", uri, resource, 1, 8))
		assert.NotEmpty(t, text, "hir should return the lowered body")
		// HIR is fenced as Rust, so the viewer requests highlighting.
		requireHighlightRequested(t, parser, ".rs")
	})

	t.Run("Mir", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _, _, parser := newTestActionHandlerParser(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		text := runViewer(t, handler, rustCmdAt("mir", uri, resource, 0, 7))
		assert.NotEmpty(t, text, "mir should return the lowered function")
		// MIR is fenced as Rust, so the viewer requests highlighting.
		requireHighlightRequested(t, parser, ".rs")
	})

	// rust-analyzer's MIR lowering panics on functions with a
	// conditionally reborrowed &mut (mirprobe.rs mirrors the alacritty
	// function that surfaced this). The extension cannot compute MIR for
	// it, but the failure must arrive as the server's own message, not
	// as gRPC status noise, and must not crash the command.
	t.Run("MirServerPanicSurfacesServerError", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/mirprobe.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/mirprobe.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on the `for` loop inside generate_hint_bindings.
		err := handler.HandleCommand(t.Context(), rustCmdAt("mir", uri, resource, 28, 23))
		if err == nil {
			// A future rust-analyzer may lower this function; the viewer
			// floating instead of an error is the desired outcome then.
			return
		}
		assert.ErrorContains(t, err, "rust-analyzer/viewMir")
		assert.NotContains(t, err.Error(), "rpc error",
			"gRPC status noise must not reach the user")
	})

	t.Run("ItemTree", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		text := runViewer(t, handler, rustCmdAt("item-tree", uri, resource, 0, 0))
		assert.Contains(t, text, "compute", "item tree should list the function")
	})

	t.Run("ExpandMacro", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/macros.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/macros.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on the answer!() invocation (line 7, col 4).
		text := runViewer(t, handler, rustCmdAt("expand-macro", uri, resource, 7, 4))
		assert.Contains(t, text, "42", "expanding answer!() yields its body")
	})

	t.Run("Status", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		text := runViewer(t, handler, rustCmdAt("status", uri, resource, 0, 0))
		assert.NotEmpty(t, text, "analyzerStatus should report a status string")
	})

	t.Run("MemoryUsage", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// memoryUsage is only answered by memory-profiling builds of
		// rust-analyzer; other builds reject it. Accept either outcome so
		// the command wiring is still exercised without requiring that
		// build.
		err := handler.HandleCommand(t.Context(), rustCmdAt("memory-usage", uri, resource, 0, 0))
		if err != nil {
			assert.Contains(t, err.Error(), "Memory profiling is not enabled")
			return
		}
		renderViewer(t, handlerWM(t, handler))
	})

	t.Run("CrateGraph", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		text := runViewer(t, handler, rustCmdAt("crate-graph", uri, resource, 0, 0))
		assert.Contains(t, text, "digraph", "crate graph is emitted as DOT")
	})

	t.Run("Dependencies", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		displays, frame := runPicker(t, handler, rustCmdAt("dependencies", uri, resource, 0, 0))
		assert.Contains(t, strings.Join(displays, "\n"), "std",
			"the dependency list includes std")
		// The first crate the server reports (core) is always within the
		// visible rows, so the rendered list must show it.
		assert.Contains(t, frame, "core", "the rendered picker lists the crates")
	})

	t.Run("ParentModule", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _, opener := newTestActionHandlerOpener(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		cmd := rustCmdAt("parent-module", uri, resource, 0, 0)
		requireHandleCommand(t, handler, cmd)
		opened := opener.openedURIs()
		require.NotEmpty(t, opened, "parent-module should open the declaring file")
		assert.Contains(t, opened[0], "lib.rs", "extract_var's parent is declared in lib.rs")
	})

	t.Run("OpenCargoToml", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _, opener := newTestActionHandlerOpener(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		cmd := rustCmdAt("open-cargo-toml", uri, resource, 0, 0)
		requireHandleCommand(t, handler, cmd)
		opened := opener.openedURIs()
		require.NotEmpty(t, opened, "open-cargo-toml should open a Cargo.toml")
		assert.Contains(t, opened[0], "Cargo.toml")
	})

	t.Run("ExternalDocs", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/main.rs"})
		handler, me, mn := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/main.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// The cursor must be on a symbol *usage*, not its `use` import:
		// rust-analyzer's externalDocs returns {web:null, local:null} for the
		// import path but a docs.rs/doc.rust-lang.org URL for the HashMap in
		// `HashMap::new()` on line 4. With localDocs advertised the response
		// is the {web, local} object and parseExternalDocs prefers web.
		cmd := rustCmdAt("external-docs", uri, resource, 4, 20)
		requireHandleCommand(t, handler, cmd)
		assert.True(t, mn.hasMessage("https://doc.rust-lang.org"),
			"external-docs reports the HashMap documentation URL; got %v", mn.messages)
	})

	t.Run("JoinLines", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/join.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/join.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select the multi-line add(...) call arguments (lines 1..3).
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 14, Y: 1},
			End:   term.Coordinates{X: 6, Y: 3},
		})
		cmd := rustCmdAt("join-lines", uri, resource, 1, 14)
		requireHandleCommand(t, handler, cmd)
		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "join-lines should produce an edit")
		assert.NotContains(t, combined, "\n", "joined arguments collapse onto one line")
	})

	t.Run("Ssr", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/ssr.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/ssr.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		cmd := rustCmd("ssr", uri, resource)
		cmd.Args = []string{"ssr", "foo($a) ==>> bar($a)"}
		cmd.Cursor.Content = term.Coordinates{X: 4, Y: 1}
		// The router strips the leading subcommand name from Args.
		requireHandleCommand(t, handler, cmd)
		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "ssr should produce an edit")
		assert.Contains(t, combined, "bar", "ssr rewrites foo(...) into bar(...)")
	})

	t.Run("MoveItemDown", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/moveitem.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/moveitem.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on first()'s signature (line 0).
		cmd := rustCmdAt("move-item-down", uri, resource, 0, 3)
		requireHandleCommand(t, handler, cmd)
		combined := allEdits(me, resource, env)
		require.NotEmpty(t, combined, "move-item-down should produce an edit")
		assert.NotContains(t, combined, "$0", "snippet tab stops must not leak into the buffer")
		assert.NotContains(t, combined, "${", "snippet placeholders must not leak into the buffer")
	})

	t.Run("OnEnter", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/onenter.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/onenter.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor at the end of the `/// first line` doc comment (line 0).
		cmd := rustCmdAt("on-enter", uri, resource, 0, 14)
		requireHandleCommand(t, handler, cmd)
		combined := allEdits(me, resource, env)
		if combined == "" {
			return // some rust-analyzer builds decline onEnter here.
		}
		assert.NotContains(t, combined, "$0", "snippet tab stops must not leak into the buffer")
	})

	t.Run("MatchingBrace", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/brace.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/brace.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on the opening paren in `(1 + 2)` (line 1, col 12).
		cmd := rustCmdAt("matching-brace", uri, resource, 1, 12)
		requireHandleCommand(t, handler, cmd)
		got := me.lastCursor(resource)
		assert.Greater(t, got.X, 12, "cursor should move to the matching close paren")
	})

	t.Run("Flycheck", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		for _, sub := range []string{"run-flycheck", "clear-flycheck", "cancel-flycheck"} {
			requireHandleCommand(t, handler, rustCmdAt(sub, uri, resource, 0, 0),
				"%s must not error", sub)
		}
	})

	t.Run("ReloadWorkspace", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, mn := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		requireHandleCommand(t, handler, rustCmdAt("reload-workspace", uri, resource, 0, 0))
		assert.True(t, mn.hasMessage("Reloaded"), "reload-workspace reports completion")
	})

	// The runnables, related-tests, interpret, recursive-memory-layout and
	// failed-obligations subcommands are best-effort: some rust-analyzer
	// builds answer them and some decline. This asserts only that the
	// command wiring encodes valid params (no serialization error), so a
	// declining server surfaces as an empty result rather than a crash.
	t.Run("BestEffortViewers", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/refactor.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/refactor.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// runnables and related-tests present a picker when the server
		// locates several targets, so they must be driven to dismissal
		// rather than awaited inline.
		for _, sub := range []string{"runnables", "related-tests"} {
			_, _, err := dismissPicker(t, handler, rustCmdAt(sub, uri, resource, 0, 7))
			if err != nil {
				assert.NotContains(t, err.Error(), "Failed to deserialize",
					"%s must send valid params", sub)
			}
		}
		wm := handlerWM(t, handler)
		for _, sub := range []string{
			"interpret", "recursive-memory-layout", "failed-obligations",
		} {
			wm.mu.Lock()
			wm.floating = nil
			wm.mu.Unlock()
			err := handler.HandleCommand(t.Context(), rustCmdAt(sub, uri, resource, 0, 7))
			if err != nil {
				assert.NotContains(t, err.Error(), "Failed to deserialize",
					"%s must send valid params", sub)
				continue
			}
			wm.mu.Lock()
			view, floated := wm.floating.(*markdownView)
			wm.mu.Unlock()
			if floated {
				renderFloating(t, view, renderW, renderH)
			}
		}
	})

	t.Run("ChildModules", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/lib.rs"})
		handler, me, _, opener := newTestActionHandlerOpener(t, env)
		uri := parseTestURI(t, env.fileURIs["src/lib.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// The cursor must sit at the crate root outside any `mod` item so
		// rust-analyzer's child_modules takes the file-to-module-defs branch
		// and returns the crate's module declarations. A cursor on a `mod x;`
		// declaration instead descends into that module; see
		// rust-analyzer/crates/ide/src/child_modules.rs. lib.rs ends with a
		// dedicated comment anchor line (line 14, 0-based) that stays outside
		// every module node regardless of how many modules precede it.
		// childModules returns the declaration locations, all in lib.rs, and
		// the command opens the first one.
		cmd := rustCmdAt("child-modules", uri, resource, 14, 0)
		requireHandleCommand(t, handler, cmd)
		opened := opener.openedURIs()
		require.NotEmpty(t, opened, "child-modules must open a declaration location")
		assert.Contains(t, opened[0], "lib.rs",
			"child-modules opens the module declaration in the crate root")
		got := me.lastCursor(resource)
		assert.Equal(t, 0, got.Y, "cursor lands on the first module declaration (line 0)")
		assert.Equal(t, 8, got.X, "cursor lands on the module name, past `pub mod `")
	})

	t.Run("Symbols", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/types.rs"})
		handler, me, _, opener := newTestActionHandlerOpener(t, env)
		uri := parseTestURI(t, env.fileURIs["src/types.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// `Widget` names exactly one workspace symbol, so the command takes
		// the single-hit path and opens its definition in types.rs. The
		// searchScope/searchKind filters are sent and accepted by the server
		// (see TestE2E/SymbolsScopeKindAccepted) even though this query does
		// not exercise their narrowing.
		cmd := rustCmd("symbols", uri, resource)
		cmd.Args = []string{"symbols", "Widget"}
		requireHandleCommand(t, handler, cmd)
		opened := opener.openedURIs()
		require.Len(t, opened, 1, "exactly one symbol is named Widget")
		assert.Contains(t, opened[0], "types.rs")
		got := me.lastCursor(resource)
		assert.Equal(t, 0, got.Y, "cursor lands on the Widget declaration line")
		assert.Equal(t, 11, got.X, "cursor lands on the struct name")
	})

	// The `deps` argument switches searchScope to workspaceAndDependencies.
	// This asserts the raw workspace/symbol request with the experimental
	// searchScope/searchKind fields is accepted by the real server (no
	// deserialize error) and still resolves the workspace symbol.
	t.Run("SymbolsScopeKindAccepted", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/types.rs"})
		handler, me, mn, opener := newTestActionHandlerOpener(t, env)
		uri := parseTestURI(t, env.fileURIs["src/types.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		cmd := rustCmd("symbols", uri, resource)
		cmd.Args = []string{"symbols", "Widget", "deps"}
		requireHandleCommand(t, handler, cmd,
			"workspace/symbol with searchScope/searchKind must be accepted")
		assert.False(t, mn.hasMessage("No matching"), "Widget resolves with dependency scope")
		assert.NotEmpty(t, opener.openedURIs())
	})

	// A query matching several types takes the picker path: the entries
	// carry workspace-relative displays and the picker renders with a
	// preview of the focused symbol's file.
	t.Run("SymbolsPicker", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/predicate.rs"})
		handler, me, _, _, parser := newTestActionHandlerParser(t, env)
		uri := parseTestURI(t, env.fileURIs["src/predicate.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// "Mark" matches both the Marker trait and the Marked struct.
		cmd := rustCmd("symbols", uri, resource)
		cmd.Args = []string{"symbols", "Mark"}
		displays, frame := runPicker(t, handler, cmd)
		joined := strings.Join(displays, "\n")
		require.Contains(t, joined, "Marker", "the trait matches; got %q", joined)
		require.Contains(t, joined, "Marked", "the struct matches; got %q", joined)
		assert.Contains(t, joined, "src/predicate.rs",
			"entries display workspace-relative paths; got %q", joined)
		assert.Contains(t, frame, "Marker", "the rendered picker lists the symbols")
		assert.Contains(t, frame, "pub trait Marker",
			"the preview pane shows the focused symbol's source; got frame %q", frame)
		// The preview pane hands the focused file to the parser.
		requireHighlightRequested(t, parser, "predicate.rs")
	})

	t.Run("TypeOfSelection", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/extract_var.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/extract_var.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Select `(1 + 2)` on line 1 (cols 17..24); hoverRange reports i32.
		me.fireEvent(t.Context(), textapi.Event{
			Type:  textapi.EventTypeSelection,
			URI:   uri,
			Start: term.Coordinates{X: 17, Y: 1},
			End:   term.Coordinates{X: 24, Y: 1},
		})
		text := runViewer(t, handler, rustCmdAt("type", uri, resource, 1, 17))
		assert.Contains(t, text, "i32", "the type of (1 + 2) is i32")
	})

	// `rust hover` renders the hover documentation as markdown in a floating
	// window. Hovering the `make` fn returns non-empty docs, so a floating
	// markdown handler (not a picker) is shown even before any action pick.
	t.Run("HoverMarkdown", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/types.rs"})
		handler, me, _, _ := newTestActionHandlerOpener(t, env)
		uri := parseTestURI(t, env.fileURIs["src/types.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on the `make` fn name (line 4, col 7). Hover has actions
		// here (Go to type Widget), so drive the picker to completion.
		titles := runHoverAction(t, handler, rustCmdAt("hover", uri, resource, 4, 7), "")
		require.NotEmpty(t, titles, "hover over `make` returns actions")
	})

	// The `make` fn's return type is `Widget`, so rust-analyzer attaches a
	// "Go to type" action (rust-analyzer.gotoLocation). Selecting it must
	// navigate to Widget's definition in types.rs. Actions only come back
	// because initialize.go advertises experimental.hoverActions plus the
	// client command list; without them the picker would be empty.
	t.Run("HoverGotoTypeAction", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/types.rs"})
		handler, me, _, opener := newTestActionHandlerOpener(t, env)
		uri := parseTestURI(t, env.fileURIs["src/types.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		titles := runHoverAction(t, handler, rustCmdAt("hover", uri, resource, 4, 7), "Widget")
		require.Contains(t, strings.Join(titles, "|"), "Widget",
			"hover over `make` offers a Go to type Widget action; got %v", titles)
		opened := opener.openedURIs()
		require.NotEmpty(t, opened, "gotoLocation opens the type definition")
		assert.Contains(t, opened[0], "types.rs", "Widget is defined in types.rs")
	})

	// Hovering a type (the `Widget` struct) attaches a
	// "N implementations"/references action (rust-analyzer.showReferences).
	// This confirms the showReferences action path is populated by the real
	// server, which again depends on the advertised client command list.
	t.Run("HoverImplementationsAction", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/types.rs"})
		handler, me, _, _ := newTestActionHandlerOpener(t, env)
		uri := parseTestURI(t, env.fileURIs["src/types.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on the `Widget` struct name (line 0, col 11).
		titles := runHoverAction(t, handler, rustCmdAt("hover", uri, resource, 0, 11), "")
		joined := strings.ToLower(strings.Join(titles, "|"))
		assert.Contains(t, joined, "implementation",
			"hover over the Widget struct offers an implementations action; got %v", titles)
	})

	// eval-predicate evaluates a where-clause predicate in the type
	// environment at the cursor. `Marked: Marker` holds because predicate.rs
	// has `impl Marker for Marked`. The cursor must be inside a function body
	// so rust-analyzer has a type environment to evaluate against.
	t.Run("EvalPredicate", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/predicate.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/predicate.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor inside eval_here's body (line 7, `    let _x = 1;`).
		cmd := rustCmd("eval-predicate", uri, resource)
		cmd.Args = []string{"eval-predicate", "Marked:", "Marker"}
		cmd.Cursor.Content = term.Coordinates{X: 8, Y: 7}
		text := runViewer(t, handler, cmd)
		assert.Contains(t, strings.ToLower(text), "holds",
			"Marked: Marker holds because impl Marker for Marked exists; got %q", text)
	})

	// diagnostics pulls rust-analyzer's diagnostics for the current file and
	// offers them in a location picker. diagbin.rs has two mismatched-types
	// errors, so a picker (rather than a direct jump) is shown. Diagnostics
	// are computed asynchronously after the file opens, so poll until they
	// show up.
	t.Run("Diagnostics", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/diagbin.rs"})
		handler, me, _ := newTestActionHandler(t, env)
		uri := parseTestURI(t, env.fileURIs["src/diagbin.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		var text, frame string
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			displays, f := runPicker(t, handler, rustCmdAt("diagnostics", uri, resource, 1, 0))
			text, frame = strings.Join(displays, "\n"), f
			if strings.Contains(text, "E0308") {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
		// rust-analyzer's own type-mismatch diagnostic for `let _x: u32 = "..."`
		// carries code E0308 and message "expected u32, found &'static str".
		assert.Contains(t, text, "E0308",
			"diagnostics should report the type-mismatch error code; got %q", text)
		assert.Contains(t, strings.ToLower(text), "expected u32",
			"diagnostics should include the diagnostic message; got %q", text)
		assert.Contains(t, frame, "E0308",
			"the rendered picker lists the diagnostics; got frame %q", frame)
		assert.Contains(t, frame, "not a number",
			"the preview pane shows the diagnosed source line; got frame %q", frame)
	})

	// run fetches the runnable at the cursor and hands the reconstructed
	// command to the workspace executor. The command reconstruction is
	// validated in TestRunnableCommand*; this asserts the end-to-end path
	// against the real rust-analyzer runnable, capturing the command instead
	// of spawning a real (slow, network-bound) cargo build.
	t.Run("Run", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/main.rs"})
		capture := &captureExecutor{fakeStdout: "1 1\n"}
		handler, me, _ := newTestActionHandlerExec(t, env, capture)
		uri := parseTestURI(t, env.fileURIs["src/main.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on `fn main` (line 3, col 3) yields several runnables (run,
		// check, test, ...), so a picker appears; select "run e2e".
		text := runRunAction(t, handler, rustCmdAt("run", uri, resource, 3, 3), "run e2e")

		cmd, ok := capture.lastCmd()
		require.True(t, ok, "run should hand a command to the executor")
		assert.Equal(t, "cargo", cmd.Path, "cargo runnable runs the cargo binary")
		assert.Contains(t, cmd.Args, "run", "the e2e main runnable is a cargo run")
		assert.Contains(t, cmd.Args, "e2e", "the runnable targets the e2e binary")
		assert.Contains(t, text, "1 1", "the viewer shows the captured program output; got %q", text)
	})

	// The hover Run action (rust-analyzer.runSingle) executes the runnable it
	// carries, not just a notification. Hovering `fn main`, then selecting the
	// Run action, hands a cargo run command to the executor.
	t.Run("HoverRunAction", func(t *testing.T) {
		t.Parallel()
		env := initRustAnalyzer(t, raBin, []string{"src/main.rs"})
		capture := &captureExecutor{fakeStdout: "1 1\n"}
		handler, me, _ := newTestActionHandlerExec(t, env, capture)
		uri := parseTestURI(t, env.fileURIs["src/main.rs"])
		resource := &stubResource{uri: uri}
		me.Register(resource)

		// Cursor on `fn main` (line 3, col 3); select the Run action.
		titles := runHoverAction(t, handler, rustCmdAt("hover", uri, resource, 3, 3), "Run")
		require.NotEmpty(t, titles, "hover over fn main offers Run/Debug actions")
		cmd, ok := capture.lastCmd()
		require.True(t, ok, "the Run hover action executes the runnable")
		assert.Equal(t, "cargo", cmd.Path)
		assert.Contains(t, cmd.Args, "run")
	})
}

// runAction runs a code-action command and applies the assist whose
// title is wantTitle. Every assist is offered through the picker, which
// blocks HandleCommand on the user's choice, so this runs it in the
// background, drives the picker to the matching entry, and confirms it.
func runAction(
	t *testing.T, handler textapi.CommandHandler, cmd textapi.Command, wantTitle string,
) {
	t.Helper()
	wm := handlerWM(t, handler)

	done := make(chan error, 1)
	go func() { done <- handler.HandleCommand(context.Background(), cmd) }()

	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			// Returning without a picker means rust-analyzer offered
			// nothing, so the assist under test never ran.
			require.NoError(t, err)
			t.Fatalf("no picker was shown; assist %q was never offered", wantTitle)
		default:
		}
		wm.mu.Lock()
		f := wm.floating
		wm.mu.Unlock()
		if picker, ok := f.(lspcmd.CodeActionPicker); ok {
			selectPickerTitle(t, picker, wantTitle)
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for code-action picker for %q", wantTitle)
		}
		time.Sleep(20 * time.Millisecond)
	}

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(20 * time.Second):
		t.Fatalf("timed out applying picked assist %q", wantTitle)
	}
}

// selectPickerTitle navigates the picker to the action titled wantTitle
// and confirms it with Enter, failing if no such action is offered.
func selectPickerTitle(t *testing.T, picker lspcmd.CodeActionPicker, wantTitle string) {
	t.Helper()
	actions := picker.Actions()
	idx := -1
	for i, a := range actions {
		if a.Title == wantTitle {
			idx = i
			break
		}
	}
	if idx < 0 {
		titles := make([]string, len(actions))
		for i, a := range actions {
			titles[i] = a.Title
		}
		t.Fatalf("assist %q not offered; got %v", wantTitle, titles)
	}
	for i := 0; i < idx; i++ {
		picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	}
	picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
}

// allEdits concatenates every edit the command produced, whether applied
// directly to the editor buffer or via workspace/applyEdit.
func allEdits(me *mockEditor, resource textapi.Handler, env *rustEnvE2E) string {
	return collectEditText(me.editsFor(resource)) + capturedEditText(env.capturedEdits())
}

// handlerWM extracts the fakeWM the handler's subcommands were wired
// with, so the picker it shows can be driven.

// requireHandleCommand runs cmd, retrying while rust-analyzer answers
// ContentModified — its standard reply when a request lands while it
// is (re)indexing, which happens routinely on loaded CI runners.
func requireHandleCommand(
	t *testing.T, handler textapi.CommandHandler, cmd textapi.Command,
	msgAndArgs ...any,
) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := handler.HandleCommand(t.Context(), cmd)
		if err != nil && strings.Contains(err.Error(), "content modified") &&
			time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		require.NoError(t, err, msgAndArgs...)
		return
	}
}

func handlerWM(t *testing.T, handler textapi.CommandHandler) *fakeWM {
	t.Helper()
	router, ok := handler.(*rustActionRouter)
	require.True(t, ok, "handler must be a rustActionRouter")
	for _, h := range router.handlers {
		lp, ok := h.(*listPickCmd)
		if !ok {
			continue
		}
		wm, ok := lp.wm.(*fakeWM)
		require.True(t, ok, "list pick command must use a fakeWM")
		return wm
	}
	t.Fatal("no list pick command found in router")
	return nil
}

// renderW/renderH size the terminal frame every floated handler is
// rendered into during e2e verification.
const (
	renderW = 100
	renderH = 40
)

// renderFloating drives a floated handler through the real draw
// pipeline — resize, draw, and a redraw-stability pass via
// handlertest.RunHandlerSequence — and returns the rendered frame.
func renderFloating(t *testing.T, f browserapi.Floating, w, h int) string {
	t.Helper()
	f.Resize(w, h)
	frame := handlertest.DrawHandler(f, w, h)
	require.NotEmpty(t, strings.TrimSpace(frame), "floated %T rendered a blank frame", f)
	handlertest.RunHandlerSequence(t, f, w, h,
		[]handlertest.SequenceTestCase{{InputSequence: "", Expected: frame}})
	return frame
}

// runViewer runs a viewer subcommand, renders the floating viewer it
// displays through the draw pipeline, and returns the rendered frame.
func runViewer(t *testing.T, handler textapi.CommandHandler, cmd textapi.Command) string {
	t.Helper()
	wm := handlerWM(t, handler)
	wm.mu.Lock()
	wm.floating = nil
	wm.mu.Unlock()
	require.NoError(t, handler.HandleCommand(context.Background(), cmd))
	return renderViewer(t, wm)
}

// renderViewer renders the markdown viewer currently floated on wm and
// returns the frame.
func renderViewer(t *testing.T, wm *fakeWM) string {
	t.Helper()
	wm.mu.Lock()
	f := wm.floating
	wm.mu.Unlock()
	view, ok := f.(*markdownView)
	require.True(t, ok, "expected a markdown viewer to be shown, got %T", f)
	return renderFloating(t, view, renderW, renderH)
}

// runHoverAction runs a `rust hover` command, waits for the hover-action
// picker, records its labels, selects the entry whose label contains want
// (or the first entry when want is empty), and drives the command to
// completion. It returns the picker labels so tests can assert which
// actions the real server attached. A hover with no actions returns nil
// once HandleCommand completes on its own.
func runHoverAction(
	t *testing.T, handler textapi.CommandHandler, cmd textapi.Command, want string,
) []string {
	t.Helper()
	wm := handlerWM(t, handler)

	deadline := time.Now().Add(30 * time.Second)
	done := make(chan error, 1)
	go func() {
		for {
			err := handler.HandleCommand(context.Background(), cmd)
			// rust-analyzer answers ContentModified while it is
			// (re)indexing under load; the hover is retryable until
			// the server settles.
			if err != nil && strings.Contains(err.Error(), "content modified") &&
				time.Now().Before(deadline) {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			done <- err
			return
		}
	}()

	for {
		select {
		case err := <-done:
			// No actions: the markdown window is shown and the command
			// returns without a picker.
			require.NoError(t, err)
			return nil
		default:
		}
		wm.mu.Lock()
		f := wm.floating
		wm.mu.Unlock()
		if picker, ok := f.(*listPicker); ok {
			labels := append([]string(nil), picker.labels...)
			renderFloating(t, picker, renderW, renderH)
			selectListPicker(picker, want)
			awaitCommand(t, wm, done, fmt.Sprintf("hover action for %q", want))
			return labels
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for hover-action picker for %q", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// awaitCommand waits for a command goroutine to finish, rendering and
// dismissing any location picker it floats along the way. A
// showReferences hover action blocks on such a picker until the user
// chooses or cancels.
func awaitCommand(t *testing.T, wm *fakeWM, done <-chan error, what string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			return
		case <-time.After(20 * time.Millisecond):
		}
		wm.mu.Lock()
		view, isPicker := wm.floating.(*pickerView)
		wm.mu.Unlock()
		if isPicker {
			renderFloating(t, view, renderW, renderH)
			require.NoError(t, view.Close())
			wm.mu.Lock()
			wm.floating = nil
			wm.mu.Unlock()
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out completing %s", what)
		}
	}
}

// selectListPicker navigates the picker to the first label containing want
// (or the first label when want is empty) and confirms it with Enter.
func selectListPicker(picker *listPicker, want string) {
	idx := 0
	if want != "" {
		for i, label := range picker.labels {
			if strings.Contains(label, want) {
				idx = i
				break
			}
		}
	}
	for i := 0; i < idx; i++ {
		picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyArrowDown})
	}
	picker.Handle(term.Event{Type: term.EventKey, Key: term.KeyEnter})
}

// runRunAction runs a `rust run` command, waits for the runnables picker,
// selects the entry whose label contains want, drives the command to
// completion, and returns the rendered frame of the output viewer it
// then shows.
func runRunAction(
	t *testing.T, handler textapi.CommandHandler, cmd textapi.Command, want string,
) string {
	t.Helper()
	wm := handlerWM(t, handler)
	wm.mu.Lock()
	wm.floating = nil
	wm.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- handler.HandleCommand(context.Background(), cmd) }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-done:
			require.NoError(t, err)
			return renderViewer(t, wm)
		default:
		}
		wm.mu.Lock()
		picker, isPicker := wm.floating.(*listPicker)
		wm.mu.Unlock()
		if isPicker {
			renderFloating(t, picker, renderW, renderH)
			selectListPicker(picker, want)
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(20 * time.Second):
				t.Fatalf("timed out completing run action for %q", want)
			}
			return renderViewer(t, wm)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for runnables picker for %q", want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// pickerDisplays returns the display strings of the floating location
// picker currently recorded on wm, or nil when none is shown.
func pickerDisplays(wm *fakeWM) []string {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	view, ok := wm.floating.(*pickerView)
	if !ok {
		return nil
	}
	entries := view.Entries()
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Display
	}
	return out
}

// runPicker is dismissPicker with the command required to succeed.
func runPicker(
	t *testing.T, handler textapi.CommandHandler, cmd textapi.Command,
) ([]string, string) {
	t.Helper()
	displays, frame, err := dismissPicker(t, handler, cmd)
	require.NoError(t, err)
	return displays, frame
}

// dismissPicker runs a location-list subcommand and returns the
// displays and rendered frame of the floating picker it shows, after
// dismissing it. Both are empty when the command completed without a
// picker (nothing to show, or a single result it jumped to directly).
func dismissPicker(
	t *testing.T, handler textapi.CommandHandler, cmd textapi.Command,
) ([]string, string, error) {
	t.Helper()
	wm := handlerWM(t, handler)
	wm.mu.Lock()
	wm.floating = nil
	wm.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- handler.HandleCommand(context.Background(), cmd) }()

	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-done:
			return nil, "", err
		default:
		}
		wm.mu.Lock()
		view, isPicker := wm.floating.(*pickerView)
		wm.mu.Unlock()
		if isPicker {
			displays := pickerDisplays(wm)
			frame := renderFloating(t, view, renderW, renderH)
			require.NoError(t, view.Close())
			select {
			case err := <-done:
				return displays, frame, err
			case <-time.After(20 * time.Second):
				t.Fatalf("timed out dismissing the %v picker", cmd.Args)
			}
			return displays, frame, nil
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the %v picker", cmd.Args)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestE2ESavedFileDiagnosticsPush(t *testing.T) {
	t.Parallel()
	raBin := findRustAnalyzer(t)
	requireClippy(t)

	env := initRustAnalyzer(t, raBin, []string{"src/diagbin.rs"})
	fileURI := env.fileURIs["src/diagbin.rs"]
	wsURI := parseTestURI(t, fileURI)

	content, err := os.ReadFile(filepath.Join(env.dir, "src", "diagbin.rs"))
	require.NoError(t, err)

	// The user's flow: the file is open and its error is saved to disk;
	// :write emits a flush (didSave), which must trigger flycheck.
	env.mgr.Handle(context.Background(), textapi.Event{
		Type:    textapi.EventTypeFlush,
		URI:     wsURI,
		Content: strings.TrimSuffix(string(content), "\n"),
	})

	hasSource := func(sources ...string) bool {
		for _, p := range env.cb.publishedDiagnostics() {
			if p.URI != fileURI {
				continue
			}
			for _, d := range p.Diagnostics {
				if !strings.Contains(strings.ToLower(d.Message), "expected") {
					continue
				}
				if slices.Contains(sources, d.Source) {
					return true
				}
			}
		}
		return false
	}

	// The first cargo clippy run compiles the crate, so give the whole
	// pipeline a generous bound. Native semantic reports may have been
	// pulled before rust-analyzer finished loading, so keep re-pulling
	// the way production does when the server sends
	// workspace/diagnostic/refresh after each flycheck run.
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if hasSource("rust-analyzer") && hasSource("rustc", "clippy") {
			return
		}
		require.NoError(t, env.mgr.RefreshDiagnostics(context.Background()))
		time.Sleep(time.Second)
	}
	assert.True(t, hasSource("rust-analyzer"),
		"native semantic diagnostics must reach the push pipeline for a "+
			"saved file (pull bridge)")
	assert.True(t, hasSource("rustc", "clippy"),
		"checkOnSave flycheck diagnostics must reach the push pipeline "+
			"after didSave")
	t.Fatalf("saved-file diagnostics never published; got %+v",
		env.cb.publishedDiagnostics())
}

// requireClippy skips when the clippy cargo subcommand is unavailable,
// since the extension configures flycheck as `cargo clippy`.
func requireClippy(t *testing.T) {
	t.Helper()
	if err := exec.Command("cargo", "clippy", "--version").Run(); err != nil {
		t.Skipf("cargo clippy unavailable: %v", err)
	}
}

func TestE2E_ResolveSysroot(t *testing.T) {
	rustcPath := findRustc(t)

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0o644))

	// The sysroot probe execs the installation-owned rustc at
	// <cargoHome>/bin/rustc, never a bare rustc, so point cargoHome at
	// the directory holding the real rustc discovered on PATH.
	cargoHome := filepath.Dir(filepath.Dir(rustcPath))

	env := runRustExtensionOnDir(t, dir, "", cargoHome)

	params, count := env.lsp.captured()
	require.Equal(t, 1, count, "the workspace-root crate must initialize exactly once")
	var opts map[string]any
	require.NoError(t, json.Unmarshal(params.InitializeOptions, &opts))
	assert.NotEmpty(t, opts["sysroot"], "a real rustc must yield a non-empty sysroot")
}

func TestE2E_RustHandlerShow(t *testing.T) {
	findRustup(t)
	dir := t.TempDir()
	_, h := newRustHandler(newDirExecutor(dir), newFakeNotifications(), dir, "rustup", nil)
	it, err := h.HandleCommand(context.Background(),
		repl.Command{Name: "rust", Args: []string{"show"}}, repl.NopProgressWriter())
	require.NoError(t, err)
	out, err := iterator.ToSlice(context.Background(), it)
	require.NoError(t, err)
	require.Len(t, out, 1)
}
