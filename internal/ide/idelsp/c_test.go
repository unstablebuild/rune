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

package idelsp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/unstablebuild/rune-go-sdk/api/semanticapi"
)

// pinnedClangdMajor is the LLVM major the ccpp package distributes, kept
// in sync with rune-language-ccpp. The expectations below hold for it; a
// host clangd of another major must fail loudly rather than validate
// behavior the package does not ship.
const pinnedClangdMajor = "21"

// findClangd resolves clangd the way extension_ccpp does, LLVM installs
// before the system toolchain, skipping when none is present and failing
// when the one found is not the pinned major.
func findClangd(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join(os.Getenv("HOME"), ".rune", "bin", "clangd"),
		"/opt/homebrew/opt/llvm/bin/clangd",
		"/usr/local/opt/llvm/bin/clangd",
		"/usr/lib/llvm-" + pinnedClangdMajor + "/bin/clangd",
	}
	bin := ""
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		p, err := exec.LookPath("clangd")
		if err != nil {
			t.Skip("clangd not found, skipping ccpp e2e test")
		}
		bin = p
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	require.NoError(t, err, "clangd --version failed")
	require.Containsf(t, string(out), "clangd version "+pinnedClangdMajor+".",
		"clangd major diverges from the distributed pin %s; update the pin "+
			"and e2e expectations together", pinnedClangdMajor)
	return bin
}

// ccppFixture holds the opened fixture files and the 0-based positions
// of the symbols the subtests probe, computed from the on-disk files so
// an edit to the fixture cannot desynchronize the assertions.
type ccppFixture struct {
	dir  string
	uris map[string]string
	// addCall is `add(` in main.cpp, addDecl its declaration in
	// mathutil.h, addDef its definition in mathutil.c.
	addCall, addDecl, addDef semanticapi.Position
	// greeter is the `g` binding in main.cpp; greetCall its greet() call.
	greeter, greetCall semanticapi.Position
	mainEndLine        uint32
}

func locateIn(t *testing.T, path, marker, ident string) semanticapi.Position {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	for i, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, marker) {
			continue
		}
		col := strings.Index(line, ident)
		require.GreaterOrEqual(t, col, 0, "ident %q not in %q", ident, line)
		return semanticapi.Position{Line: uint32(i), Character: uint32(col)}
	}
	t.Fatalf("could not find %q in %s", marker, path)
	return semanticapi.Position{}
}

// ccppInitParams mirrors extension_ccpp's bring-up: langID "ccpp" so one
// clangd serves c and cpp, the bare clangd path as the command (idelsp
// tokenizes it on spaces) and code-action literal support, without which
// clangd answers code actions as bare commands.
func ccppInitParams(t *testing.T, rootURI, clangdBin string) semanticapi.InitializeParams {
	t.Helper()
	params := autoInitParams(rootURI)
	params.WorkspaceFolders = []semanticapi.WorkspaceFolder{{URI: rootURI, Name: "ccpp"}}
	initOpts, err := json.Marshal(map[string]any{
		"langID":  "ccpp",
		"command": clangdBin,
	})
	require.NoError(t, err)
	params.InitializeOptions = initOpts
	caps, err := json.Marshal(map[string]any{
		"textDocument": map[string]any{
			"codeAction": map[string]any{
				"codeActionLiteralSupport": map[string]any{
					"codeActionKind": map[string]any{"valueSet": []string{"quickfix", "refactor"}},
				},
			},
			"publishDiagnostics": map[string]any{},
		},
	})
	require.NoError(t, err)
	params.Capabilities = caps
	return params
}

// setupCcppManager drives a real clangd through the Manager against the
// mixed C/C++ fixture: every file, .c, .cpp and .h alike, is opened with
// the language id the IDE derives from its extension and must land on
// the one ccpp server.
func setupCcppManager(
	t *testing.T, ctx context.Context, cb *testCallback,
) (*Manager, ccppFixture) {
	t.Helper()
	clangdBin := findClangd(t)
	tmpDir := setupTestWorkspace(t, filepath.Join("testdata", "ccpp"))
	uri := makeURI(t, "file://"+tmpDir)
	scheme := newTestScheme()

	mgr := New(uri, scheme, scheme, &stubPkgManager{bin: clangdBin}, nil, nil,
		Config{
			Callback:           cb,
			MaxRetries:         1,
			NoInitializeServer: true,
			InitializeTimeout:  30 * time.Second,
		})
	t.Cleanup(func() { _ = mgr.Close() })

	_, err := mgr.Initialize(ctx, ccppInitParams(t, uri.String(), clangdBin))
	require.NoError(t, err)

	fx := ccppFixture{dir: tmpDir, uris: map[string]string{}}
	for _, name := range []string{"main.cpp", "mathutil.c", "mathutil.h", "greeter.cpp", "greeter.hpp"} {
		path := filepath.Join(tmpDir, name)
		content, err := os.ReadFile(path)
		require.NoError(t, err)
		fileURI := "file://" + path
		fx.uris[name] = fileURI
		cfg, err := languageForFilename(fileURI)
		require.NoError(t, err)
		require.Equal(t, "ccpp", cfg.id, "%s must route to the shared clangd", name)
		require.NoError(t, mgr.DidOpen(ctx, semanticapi.DidOpenTextDocumentParams{
			TextDocument: semanticapi.TextDocumentItem{
				URI:        fileURI,
				LanguageID: cfg.documentID,
				Version:    0,
				Text:       string(content),
			},
		}))
	}
	mainPath := filepath.Join(tmpDir, "main.cpp")
	fx.addCall = locateIn(t, mainPath, "int result = add(2, 3);", "add(")
	fx.addDecl = locateIn(t, filepath.Join(tmpDir, "mathutil.h"), "int add(int a, int b);", "add(")
	fx.addDef = locateIn(t, filepath.Join(tmpDir, "mathutil.c"), "int add(int a, int b) {", "add(")
	fx.greeter = locateIn(t, mainPath, `Greeter g("World");`, "g(")
	fx.greetCall = locateIn(t, mainPath, "g.greet()", "greet")
	mainData, err := os.ReadFile(mainPath)
	require.NoError(t, err)
	fx.mainEndLine = uint32(len(strings.Split(string(mainData), "\n")) - 1)

	mgr.mu.Lock()
	servers := len(mgr.servers)
	mgr.mu.Unlock()
	require.Equal(t, 1, servers, "c, cpp and h files must share one clangd")

	waitForClangdReady(t, ctx, mgr, fx)
	return mgr, fx
}

// waitForClangdReady polls clangd until it resolves the cross-file
// definition and its dynamic index knows the opened files, so the
// subtests do not race its asynchronous preamble builds.
func waitForClangdReady(t *testing.T, ctx context.Context, mgr *Manager, fx ccppFixture) {
	t.Helper()
	mainURI := fx.uris["main.cpp"]
	probes := []func() bool{
		func() bool {
			res, err := mgr.Definition(ctx, semanticapi.DefinitionParams{
				TextDocument: semanticapi.TextDocumentIdentifier{URI: mainURI},
				Position:     fx.addCall,
			})
			return err == nil && len(zigLocations(res)) > 0
		},
		func() bool {
			syms, err := mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{Query: "Greeter"})
			return err == nil && len(syms) > 0
		},
	}
	deadline := time.Now().Add(90 * time.Second)
	next := 0
	for time.Now().Before(deadline) {
		for next < len(probes) && probes[next]() {
			next++
		}
		if next == len(probes) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for clangd: %v", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for clangd to become ready: stuck on probe %d", next)
}

func TestE2ECcpp(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cb := &testCallback{}
	mgr, fx := setupCcppManager(t, ctx, cb)
	mainURI := fx.uris["main.cpp"]
	mainDoc := semanticapi.TextDocumentIdentifier{URI: mainURI}

	tests := []struct {
		name string
		fn   func(t *testing.T)
	}{
		{
			name: "Hover in C++ describes the C function",
			fn: func(t *testing.T) {
				h, err := mgr.Hover(ctx, semanticapi.HoverParams{TextDocument: mainDoc, Position: fx.addCall})
				require.NoError(t, err)
				require.NotNil(t, h)
				assert.Contains(t, h.Contents.Value, "int add(int a, int b)")
			},
		},
		{
			name: "Hover in C describes the function",
			fn: func(t *testing.T) {
				h, err := mgr.Hover(ctx, semanticapi.HoverParams{
					TextDocument: semanticapi.TextDocumentIdentifier{URI: fx.uris["mathutil.c"]},
					Position:     fx.addDef,
				})
				require.NoError(t, err)
				require.NotNil(t, h)
				assert.Contains(t, h.Contents.Value, "add")
			},
		},
		{
			name: "Definition of the C call lands in the C sources",
			fn: func(t *testing.T) {
				// The declaration in mathutil.h is the answer until the
				// dynamic index has seen the opened mathutil.c, after
				// which clangd prefers the definition.
				res, err := mgr.Definition(ctx, semanticapi.DefinitionParams{TextDocument: mainDoc, Position: fx.addCall})
				require.NoError(t, err)
				locs := zigLocations(res)
				require.NotEmpty(t, locs)
				assert.True(t, strings.HasSuffix(locs[0].URI, "/mathutil.c") ||
					strings.HasSuffix(locs[0].URI, "/mathutil.h"), locs[0].URI)
			},
		},
		{
			name: "Declaration of the C call is the header prototype",
			fn: func(t *testing.T) {
				res, err := mgr.Declaration(ctx, semanticapi.DeclarationParams{TextDocument: mainDoc, Position: fx.addCall})
				require.NoError(t, err)
				locs := zigLocations(res)
				require.NotEmpty(t, locs)
				assert.True(t, strings.HasSuffix(locs[0].URI, "/mathutil.h"), locs[0].URI)
				assert.Equal(t, fx.addDecl.Line, locs[0].Range.Start.Line)
			},
		},
		{
			name: "TypeDefinition of the Greeter binding is the class",
			fn: func(t *testing.T) {
				res, err := mgr.TypeDefinition(ctx, semanticapi.TypeDefinitionParams{TextDocument: mainDoc, Position: fx.greeter})
				require.NoError(t, err)
				locs := zigLocations(res)
				require.NotEmpty(t, locs)
				assert.True(t, strings.HasSuffix(locs[0].URI, "/greeter.hpp"), locs[0].URI)
			},
		},
		{
			name: "References to add span the C and C++ files",
			fn: func(t *testing.T) {
				require.Eventually(t, func() bool {
					locs, err := mgr.References(ctx, semanticapi.ReferenceParams{
						TextDocument: mainDoc, Position: fx.addCall,
						Context: semanticapi.ReferenceContext{IncludeDeclaration: true},
					})
					if err != nil {
						return false
					}
					var inMain, inHeader bool
					for _, l := range locs {
						inMain = inMain || strings.HasSuffix(l.URI, "/main.cpp")
						inHeader = inHeader || strings.HasSuffix(l.URI, "/mathutil.h")
					}
					return inMain && inHeader
				}, 30*time.Second, 300*time.Millisecond,
					"references must cover the call in main.cpp and the prototype in mathutil.h")
			},
		},
		{
			name: "DocumentSymbol lists main",
			fn: func(t *testing.T) {
				res, err := mgr.DocumentSymbol(ctx, semanticapi.DocumentSymbolParams{TextDocument: mainDoc})
				require.NoError(t, err)
				assert.Contains(t, symbolNames(res), "main")
			},
		},
		{
			name: "WorkspaceSymbol finds the class and the C function",
			fn: func(t *testing.T) {
				for _, q := range []string{"Greeter", "add"} {
					syms, err := mgr.WorkspaceSymbol(ctx, semanticapi.WorkspaceSymbolParams{Query: q})
					require.NoError(t, err)
					found := false
					for _, s := range syms {
						found = found || s.Name == q
					}
					assert.True(t, found, "expected a %q workspace symbol", q)
				}
			},
		},
		{
			name: "Completion offers Greeter members without snippets",
			fn: func(t *testing.T) {
				orig, err := os.ReadFile(strings.TrimPrefix(mainURI, "file://"))
				require.NoError(t, err)
				probe := string(orig) + "\nstatic void probe() {\n  Greeter p(\"x\");\n  p.\n}\n"
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument:   semanticapi.VersionedTextDocumentIdentifier{URI: mainURI, Version: 5},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{{Text: probe}},
				}))
				baseLine := uint32(len(strings.Split(string(orig), "\n")))
				res, err := mgr.Completion(ctx, semanticapi.CompletionParams{
					TextDocument: mainDoc,
					Position:     semanticapi.Position{Line: baseLine + 2, Character: 4},
				})
				require.NoError(t, err)
				labels := make([]string, 0, len(res.Items))
				for _, it := range res.Items {
					labels = append(labels, strings.TrimSpace(it.Label))
					if it.TextEdit != nil {
						assert.NotContains(t, it.TextEdit.NewText, "${", "no snippet placeholders may reach the buffer")
					}
				}
				assert.Contains(t, labels, "greet() const")
				assert.Contains(t, labels, "count() const")
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument:   semanticapi.VersionedTextDocumentIdentifier{URI: mainURI, Version: 6},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{{Text: string(orig)}},
				}))
			},
		},
		{
			name: "Formatting runs clang-format",
			fn: func(t *testing.T) {
				_, err := mgr.Formatting(ctx, semanticapi.DocumentFormattingParams{
					TextDocument: mainDoc,
					Options:      semanticapi.FormattingOptions{TabSize: 2, InsertSpaces: true},
				})
				require.NoError(t, err)
			},
		},
		{
			name: "FoldingRange covers the function body",
			fn: func(t *testing.T) {
				ranges, err := mgr.FoldingRange(ctx, semanticapi.FoldingRangeParams{TextDocument: mainDoc})
				require.NoError(t, err)
				assert.NotEmpty(t, ranges)
			},
		},
		{
			name: "DocumentHighlight marks the g binding",
			fn: func(t *testing.T) {
				hs, err := mgr.DocumentHighlight(ctx, semanticapi.DocumentHighlightParams{TextDocument: mainDoc, Position: fx.greeter})
				require.NoError(t, err)
				assert.GreaterOrEqual(t, len(hs), 2, "the declaration and the g.greet() use")
			},
		},
		{
			name: "SignatureHelp describes the add call",
			fn: func(t *testing.T) {
				sh, err := mgr.SignatureHelp(ctx, semanticapi.SignatureHelpParams{
					TextDocument: mainDoc,
					Position:     semanticapi.Position{Line: fx.addCall.Line, Character: fx.addCall.Character + 4},
				})
				require.NoError(t, err)
				require.NotNil(t, sh)
				require.NotEmpty(t, sh.Signatures)
				assert.Contains(t, sh.Signatures[0].Label, "add(int a, int b)")
			},
		},
		{
			name: "PrepareRename accepts the add call",
			fn: func(t *testing.T) {
				res, err := mgr.PrepareRename(ctx, semanticapi.PrepareRenameParams{TextDocument: mainDoc, Position: fx.addCall})
				require.NoError(t, err)
				require.NotNil(t, res)
			},
		},
		{
			name: "Rename add rewrites the C declaration, definition and C++ call",
			fn: func(t *testing.T) {
				edit, err := mgr.Rename(ctx, semanticapi.RenameParams{
					TextDocument: mainDoc, Position: fx.addCall, NewName: "sum",
				})
				require.NoError(t, err)
				require.NotNil(t, edit)
				files := map[string]int{}
				for uri, es := range edit.Changes {
					files[filepath.Base(uri)] += len(es)
				}
				for _, dc := range edit.DocumentChanges {
					if dc.TextDocumentEdit != nil {
						files[filepath.Base(dc.TextDocumentEdit.TextDocument.URI)] += len(dc.TextDocumentEdit.Edits)
					}
				}
				assert.Positive(t, files["main.cpp"], "the call site: %v", files)
				assert.Positive(t, files["mathutil.h"], "the prototype: %v", files)
				assert.Positive(t, files["mathutil.c"], "the definition: %v", files)
			},
		},
		{
			name: "SelectionRange expands from the add call",
			fn: func(t *testing.T) {
				res, err := mgr.SelectionRange(ctx, semanticapi.SelectionRangeParams{
					TextDocument: mainDoc, Positions: []semanticapi.Position{fx.addCall},
				})
				require.NoError(t, err)
				assert.NotEmpty(t, res)
			},
		},
		{
			name: "SemanticTokensFull returns a token stream",
			fn: func(t *testing.T) {
				toks, err := mgr.SemanticTokensFull(ctx, semanticapi.SemanticTokensParams{TextDocument: mainDoc})
				require.NoError(t, err)
				require.NotNil(t, toks)
				assert.NotEmpty(t, toks.Data)
			},
		},
		{
			name: "InlayHint names the add parameters",
			fn: func(t *testing.T) {
				hints, err := mgr.InlayHint(ctx, semanticapi.InlayHintParams{
					TextDocument: mainDoc,
					Range: semanticapi.Range{
						Start: semanticapi.Position{Line: 0, Character: 0},
						End:   semanticapi.Position{Line: fx.mainEndLine, Character: 0},
					},
				})
				require.NoError(t, err)
				labels := make([]string, 0, len(hints))
				for _, h := range hints {
					// clangd sends labels as parts; a hint carries either form.
					label := h.Label
					for _, part := range h.LabelParts {
						label += part.Value
					}
					labels = append(labels, label)
				}
				assert.Contains(t, labels, "a:")
				assert.Contains(t, labels, "b:")
			},
		},
		{
			name: "CodeAction answers literals for the add call",
			fn: func(t *testing.T) {
				_, err := mgr.CodeAction(ctx, semanticapi.CodeActionParams{
					TextDocument: mainDoc,
					Range:        semanticapi.Range{Start: fx.addCall, End: fx.addCall},
					Context:      semanticapi.CodeActionContext{Diagnostics: []semanticapi.Diagnostic{}},
				})
				require.NoError(t, err)
			},
		},
		{
			name: "PushDiagnostics reports a compile error on a broken edit",
			fn: func(t *testing.T) {
				orig, err := os.ReadFile(strings.TrimPrefix(mainURI, "file://"))
				require.NoError(t, err)
				broken := string(orig) + "\nint broken = ;\n"
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument:   semanticapi.VersionedTextDocumentIdentifier{URI: mainURI, Version: 7},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{{Text: broken}},
				}))
				require.Eventually(t, func() bool {
					for _, d := range cb.PublishedDiagnostics(mainURI) {
						if d.Severity == semanticapi.DiagnosticSeverityError &&
							strings.EqualFold(d.Message, "expected expression") {
							return true
						}
					}
					return false
				}, 30*time.Second, 200*time.Millisecond, "expected clangd to publish the compile error")
				require.NoError(t, mgr.DidChange(ctx, semanticapi.DidChangeTextDocumentParams{
					TextDocument:   semanticapi.VersionedTextDocumentIdentifier{URI: mainURI, Version: 8},
					ContentChanges: []semanticapi.TextDocumentContentChangeEvent{{Text: string(orig)}},
				}))
			},
		},
		{
			name: "switchSourceHeader pairs the C++ source with its header via ExecuteRequest",
			fn: func(t *testing.T) {
				params, err := json.Marshal(semanticapi.TextDocumentIdentifier{URI: fx.uris["greeter.cpp"]})
				require.NoError(t, err)
				raw, err := mgr.ExecuteRequest(ctx, semanticapi.ExecuteRequestParams{
					Method:   "textDocument/switchSourceHeader",
					Params:   params,
					ServerID: "ccpp",
				})
				require.NoError(t, err)
				var target string
				require.NoError(t, json.Unmarshal(raw, &target))
				assert.True(t, strings.HasSuffix(target, "/greeter.hpp"), target)

				params, err = json.Marshal(semanticapi.TextDocumentIdentifier{URI: fx.uris["mathutil.h"]})
				require.NoError(t, err)
				raw, err = mgr.ExecuteRequest(ctx, semanticapi.ExecuteRequestParams{
					Method:   "textDocument/switchSourceHeader",
					Params:   params,
					ServerID: "ccpp",
				})
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &target))
				assert.True(t, strings.HasSuffix(target, "/mathutil.c"), target)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t)
		})
	}
}
